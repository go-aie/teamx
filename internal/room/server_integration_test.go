//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/pelletier/go-toml/v2"
)

// A real ACP subprocess exercises the cancellable core, filesystem, HTTP and
// process ownership. Its model is an offline fixture, with no platform access.
func TestServerCancellationReapsACPAndRestartsSameHome(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for the offline ACP integration fixture")
	}
	node, err = filepath.EvalSymlinks(node)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "持久 home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	log, gate := filepath.Join(home, "acp.jsonl"), filepath.Join(home, "release-prompt")
	if err := os.WriteFile(gate, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs("../../scripts/fixtures/background-acp.mjs")
	if err != nil {
		t.Fatal(err)
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	wrapper := filepath.Join(home, "fixture-dsh")
	script := "#!/bin/sh\nif [ \"${1:-}\" = --version ]; then echo 1.0.0; exit 0; fi\nexec " + quote(node) + " " + quote(fixture) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{
		"server": map[string]any{"listen": "127.0.0.1:0"},
		"runtime": map[string]any{
			"dsh":   map[string]any{"binary_path": wrapper},
			"model": map[string]any{"protocol": "openai-completions", "base_url": "http://127.0.0.1:1/v1", "model_id": "background-fixture", "api_key_env": "TEAMX_SERVER_FIXTURE_KEY"},
		},
		"env": map[string]string{"TEAMX_SERVER_FIXTURE_KEY": "offline-only", "TEAMX_BACKGROUND_LOG": log, "TEAMX_BACKGROUND_RELEASE": gate},
	}
	data, err := toml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), data, 0600); err != nil {
		t.Fatal(err)
	}
	legacy, err := newRoomManager(filepath.Join(home, "data"), defaultMaxTurns)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.close(); err != nil {
		t.Fatal(err)
	}
	// Migration intentionally leaves imports unverified. This lifecycle-only
	// fixture supplies a synthetic receipt; provider validation has its own tests.
	startup, err := readStartupConfig(filepath.Join(home, "config.toml"), true, false)
	if err != nil {
		t.Fatal(err)
	}
	startup, err = migrateModelConfiguration(startup, nil)
	if err != nil {
		t.Fatal(err)
	}
	models, err := newModelService(startup, newSystemSettings(startup), home)
	if err != nil {
		t.Fatal(err)
	}
	models.probe = func(ctx context.Context, options dsh.Options) (dsh.ProbeResult, error) {
		identity, err := dsh.InspectRuntime(ctx, options)
		identity.Agent = dsh.Artifact{Source: "background-fixture", Version: "1"}
		return dsh.ProbeResult{Identity: identity, Text: true, ToolRoundTrip: true, Cleanup: true}, err
	}
	if _, err := models.save(context.Background(), modelSave{Entry: startup.Models.Entries[0], ExpectedVersion: startup.Models.Version}); err != nil {
		t.Fatal(err)
	}
	info, stop := startTestServer(t, []string{"--home", home}, serverOptions{})
	client := serverTestCLI(t, home, info.Endpoint)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var target ServiceInfo
	for target.ServiceEpoch == "" {
		var rooms roomsResponse
		if err := serverTestRequest(ctx, client, "/v1/rooms", nil, &rooms); err != nil {
			t.Fatal(err)
		}
		for _, room := range rooms.Rooms {
			if rooms.DefaultAssistant != nil && room.RoomIdentity == rooms.DefaultAssistant.RoomIdentity && room.RuntimeStatus == "ready" && room.Service != nil {
				target = *room.Service
			}
		}
		if ctx.Err() != nil {
			t.Fatal("assistant did not recover", ctx.Err())
		}
		time.Sleep(10 * time.Millisecond)
	}
	var receipt PostReceipt
	if err := serverTestRequest(ctx, client, "/v1/send", apiRequest{ServiceInfo: target, Caller: serverTestCaller(t, home), ClientMessageID: "server-lifecycle", Text: "offline lifecycle fixture"}, &receipt); err != nil {
		t.Fatal(err)
	}
	readEvents := func() []struct {
		PID    int    `json:"pid"`
		Method string `json:"method"`
	} {
		b, _ := os.ReadFile(log)
		var events []struct {
			PID    int    `json:"pid"`
			Method string `json:"method"`
		}
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var event struct {
				PID    int    `json:"pid"`
				Method string `json:"method"`
			}
			if json.Unmarshal([]byte(line), &event) == nil {
				events = append(events, event)
			}
		}
		return events
	}
	for {
		completed := false
		for _, event := range readEvents() {
			completed = completed || event.Method == "prompt_completed"
		}
		if completed {
			break
		}
		if ctx.Err() != nil {
			var members serviceMembers
			diagnosticCtx, diagnosticCancel := context.WithTimeout(context.Background(), time.Second)
			diagnosticErr := client.request(diagnosticCtx, "/v1/member/list", apiRequest{ServiceInfo: target, Caller: serverTestCaller(t, home)}, &members)
			diagnosticCancel()
			b, _ := json.Marshal(members)
			t.Fatal("ACP did not complete", ctx.Err(), readEvents(), string(b), diagnosticErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	closed := false
	for _, event := range readEvents() {
		closed = closed || event.Method == "session/close"
		if event.PID <= 0 {
			t.Fatal("fixture omitted process identity")
		}
		if err := syscall.Kill(event.PID, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatal("owned ACP process survived server cancellation", event.PID, err)
		}
	}
	if !closed {
		t.Fatal("normal stop omitted ACP session/close")
	}
	// The same home reopens with the original assistant and message receipt.
	restarted, stopAgain := startTestServer(t, []string{"--home", home}, serverOptions{})
	if restarted.ServiceEpoch == info.ServiceEpoch {
		t.Fatal("restart did not advance the server epoch")
	}
	stopAgain()
	var index roomsIndex
	if err := readSnapshot(filepath.Join(home, "data/rooms.json"), &index, maxRoomsFileBytes); err != nil || index.DefaultAssistant == nil || index.DefaultAssistant.RoomIdentity != target.RoomIdentity {
		t.Fatal("restart replaced the assistant", err)
	}
	var snapshot RoomSnapshot
	if err := readSnapshot(filepath.Join(home, "data/rooms", target.RoomID, "room.json"), &snapshot, maxRoomFileBytes); err != nil || len(snapshot.Messages) == 0 || snapshot.Messages[0].Seq != receipt.Seq {
		t.Fatal("restart lost the accepted message", err)
	}
}
