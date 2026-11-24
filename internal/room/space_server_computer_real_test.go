//go:build e2e

package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/computer"
	"github.com/go-aie/teamx/internal/computer/aio"
	"github.com/gorilla/websocket"
)

func TestSpacesRealComputerAssignmentAndStreams(t *testing.T) { testSpacesRealComputer(t, false) }
func TestSpacesRealComputerArchiveAndStreams(t *testing.T)    { testSpacesRealComputer(t, true) }
func testSpacesRealComputer(t *testing.T, archive bool) {
	if os.Getenv("TEAMX_SPACES_COMPUTER_REAL") != "1" {
		t.Skip("explicit disposable local Docker acceptance only")
	}
	t.Setenv("TEAMX_COMPUTER_AGENT_REAL", "1") // actual screen startup gets the existing two-minute acceptance budget
	output := os.Getenv("TEAMX_SPACES_COMPUTER_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("fresh absolute output required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"status": "failed", "scope": "two actual Linux AIO runtimes under macOS personal Space assembly", "external_business_writes": 0}
	defer func() {
		raw, _ := json.MarshalIndent(report, "", "  ")
		_ = os.WriteFile(filepath.Join(output, "report.json"), raw, 0600)
	}()
	s, endpoint, stop := startSpaceServerFixture(t, t.TempDir(), func(r *SpaceRuntime) {
		create := r.factories.computer
		r.factories.computer = func(ctx context.Context, startup startupConfig, settings *systemSettings, info ServiceInfo, installation, owner string, current func(MemberIdentity) (bool, error)) (*computerController, error) {
			c, err := create(ctx, startup, settings, info, installation, owner, current)
			if err == nil {
				c.connect = func(_ string, endpoint, key string) (computer.ManagedProvider, error) { return aio.New(endpoint, key) }
			}
			return c, err
		}
	})
	controllers := []*computerController{s.initial.computer}
	defer func() {
		stop()
		for _, c := range controllers {
			cleanupManagedAcceptance(t, c)
		}
	}()
	id := strings.Repeat("b", 32)
	if _, err := s.create(spaceChangeInput{RequestID: id, Name: "Computer B"}); err != nil {
		t.Fatal(err)
	}
	waitSpaceState(t, s, id, "setup_required")
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(id)+"/v1/setup/model", setupModelInput{RequestID: "b-model", ExpectedVersion: 1, ModelParameters: modelTestEntry("b").ModelParameters, APIKey: "synthetic-private-key"}, nil); status != 202 {
		t.Fatal(status)
	}
	b := waitSpaceState(t, s, id, "ready")
	controllers = append(controllers, b.computer)
	instances, containers, volumes := map[string]string{}, map[string]string{}, map[string]string{}
	for _, runtime := range []*SpaceRuntime{s.initial, b} {
		c := runtime.computer
		// Package tests do not import the product's provider-registration init.
		// Bind the same actual AIO adapter selected by builtin/computer.
		c.mu.Lock()
		c.connect = func(_ string, endpoint, key string) (computer.ManagedProvider, error) { return aio.New(endpoint, key) }
		c.mu.Unlock()
		input := managedInput(c)
		input.Caller = Caller{Author: Author{Kind: "human", ID: runtime.OwnerUserID}}
		input.DockerCPUs, input.DockerMemoryGiB = 1, 2
		input.MaxScreens = 1
		if status := spaceHTTPRequest(t, endpoint, spaceRoute(runtime.SpaceID)+"/v1/computer/save", input, nil); status != 200 && status != 202 {
			t.Fatal(status)
		}
		deadline := time.Now().Add(4 * time.Minute)
		for {
			c.mu.Lock()
			op := c.journal.Operations[len(c.journal.Operations)-1]
			c.mu.Unlock()
			if op.Status == "completed" {
				break
			}
			if op.Status == "failed" {
				t.Fatal("actual managed Computer apply failed", op.Stage, op.Message)
			}
			if op.Status == "waiting" && op.Stage == "validate" {
				checkCtx, stopCheck := context.WithTimeout(t.Context(), 10*time.Second)
				_, info, inspectErr := c.inspect(checkCtx, op.Target)
				stopCheck()
				contract, contractErr := computer.NewContract(info.Contract.Provider, info.Contract.Versions, info.Contract.Tools)
				names := []string{}
				for _, tool := range info.Contract.Tools {
					names = append(names, tool.Name)
				}
				t.Fatalf("actual Computer validation: %v; contract: %v, digest_matches=%t, owner_matches=%t; tools=%v", inspectErr, contractErr, contract.Digest == info.Contract.Digest, info.Owner == (computer.Owner{}) || info.Owner == c.owner, names)
			}
			if time.Now().After(deadline) {
				t.Fatal("actual managed Computer timed out", op.Stage, op.Message)
			}
			time.Sleep(100 * time.Millisecond)
		}
		instances[runtime.SpaceID] = c.current.Load().InstanceID
		c.mu.Lock()
		containers[runtime.SpaceID] = c.journal.Docker.Container
		volumes[runtime.SpaceID] = c.journal.Docker.Name
		c.mu.Unlock()
		s.claims.mu.Lock()
		owner := s.claims.saved.Computers[c.current.Load().InstanceID]
		s.claims.mu.Unlock()
		if owner != runtime.SpaceID {
			t.Fatal("actual Runtime lacks a durable Space assignment")
		}
	}
	a := s.initial.SpaceID
	if instances[a] == instances[id] || containers[a] == containers[id] || volumes[a] == volumes[id] || s.initial.computer.owner.ComputerID == b.computer.owner.ComputerID {
		t.Fatal("actual Computer resources were shared")
	}
	if err := s.claims.reserveComputer(id, instances[a]); !errors.Is(err, ErrConflict) {
		t.Fatal("another Space acquired the actual Runtime", err)
	}
	// Opening B's real screen exercises the routed native WebSocket transport,
	// including the periodic session check after the initial handshake.
	svc := waitSpaceAssistant(t, b)
	snapshot, err := svc.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	member := MemberIdentity{RoomIdentity: snapshot.RoomIdentity, AgentID: snapshot.Members[0].AgentID, MembershipID: snapshot.Members[0].MembershipID}
	applyMemberComputer(t, b.computer.members, member, true)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	display, err := b.computer.members.display(ctx, member)
	if err != nil {
		t.Fatal("actual browser screen", err)
	}
	_ = display
	key := environmentID(b.manager.epoch, member)
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(a)+"/v1/environment/"+key+"/viewer", nil, nil); status != 404 {
		t.Fatal("foreign Space acquired viewer", status)
	}
	viewer := spaceRoute(id) + "/v1/environment/" + key + "/websockify"
	headers := http.Header{"Origin": []string{endpoint}}
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Subprotocols: []string{"binary"}}
	ws, response, err := dialer.DialContext(ctx, strings.Replace(endpoint, "http:", "ws:", 1)+viewer, headers)
	if err != nil {
		if response != nil {
			t.Log("WebSocket status", response.StatusCode)
		}
		t.Fatal("routed WebSocket", err)
	}
	defer ws.Close()
	_ = ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	kind, banner, err := ws.ReadMessage()
	if err != nil || kind != websocket.BinaryMessage || !strings.HasPrefix(string(banner), "RFB ") {
		t.Fatal("real RFB greeting", err)
	}
	time.Sleep(1500 * time.Millisecond)
	if err := ws.WriteMessage(websocket.BinaryMessage, banner); err != nil {
		t.Fatal(err)
	}
	if _, security, err := ws.ReadMessage(); err != nil || len(security) == 0 {
		t.Fatal("Space stream failed periodic authorization", err)
	}
	if archive {
		oldScreen := display.Info()
		streamClosed := make(chan struct{})
		go func() {
			defer close(streamClosed)
			for {
				if _, _, err := ws.ReadMessage(); err != nil {
					return
				}
			}
		}()
		archived := archiveSpaceForTest(t, s, id)
		select {
		case <-streamClosed:
		case <-time.After(5 * time.Second):
			t.Fatal("archive retained old real WebSocket")
		}
		if err := s.claims.reserveComputer(a, instances[id]); !errors.Is(err, ErrConflict) {
			t.Fatal("archive released Computer assignment", err)
		}
		if _, err := s.changeSpaceStatus(id, "restore", archived.Version); err != nil {
			t.Fatal(err)
		}
		fresh := waitSpaceState(t, s, id, "ready")
		if fresh.computer == b.computer || fresh.computer.owner != b.computer.owner || fresh.RuntimeEpoch == b.RuntimeEpoch {
			t.Fatal("Computer restore changed owner or reused controller")
		}
		if fresh.computer.current.Load().InstanceID != instances[id] {
			t.Fatal("restore changed real Runtime instance")
		}
		fresh.computer.mu.Lock()
		sameVolume := fresh.computer.journal.Docker.Name == volumes[id] && fresh.computer.journal.Docker.Container == containers[id]
		fresh.computer.mu.Unlock()
		if !sameVolume {
			t.Fatal("restore replaced Computer profile volume")
		}
		var restored computer.DisplayScreen
		deadline := time.Now().Add(30 * time.Second)
		for {
			restored, err = fresh.computer.members.display(ctx, member)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("restored real screen", err)
			}
			time.Sleep(100 * time.Millisecond)
		}
		if info := restored.Info(); info.ID != oldScreen.ID || info.Binding.Key() != oldScreen.Binding.Key() {
			t.Fatal("restore changed profile/screen identity")
		}
		if status := spaceHTTPRequest(t, endpoint, spaceRoute(id)+"/v1/environment/"+key+"/viewer", nil, nil); status != 404 {
			t.Fatal("old viewer epoch survived restore", status)
		}
		report["archive_stream_revoked"], report["restored_screen_identity"], report["same_runtime_and_profile_volume"] = true, true, true
	}
	ws.Close()
	report["status"], report["instances"], report["independent_containers_and_volumes"], report["duplicate_assignment_rejected"], report["scoped_rfb_stream"], report["foreign_viewer_rejected"] = "passed", instances, true, true, true, true
	report["no_real_model"] = true

}
