//go:build e2e

package room

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Reads an explicitly selected managed model. All writes and both real DSH
// Sessions belong to a fresh acceptance home; no external business writes.
func TestSpaceRuntimeInstalledDSHRecovery(t *testing.T) {
	source := os.Getenv("TEAMX_SPACE_REAL_CONFIG")
	if source == "" {
		t.Skip("explicit managed source config and fresh TEAMX_SPACE_REAL_OUTPUT required")
	}
	output := os.Getenv("TEAMX_SPACE_REAL_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("fresh absolute output required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"status": "failed", "external_business_writes": 0, "scope": "actual personal assembly, installed DSH/model, private credentials and original Session recovery"}
	defer func() {
		data, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(filepath.Join(output, "report.json"), data, 0600); err != nil {
			t.Error(err)
		}
	}()
	original, err := loadStartupConfig(source, true)
	if err != nil || original.Runtime == nil || original.Models == nil {
		t.Fatal("managed model source required", err)
	}
	entry, err := original.Models.resolve(modelSelection{Mode: "default"})
	if err != nil {
		t.Fatal(err)
	}
	// A read-only credential handle; do not initialize or migrate the source home.
	credential, err := (&modelCredentials{dir: filepath.Join(filepath.Dir(original.path), "model-credentials")}).get(entry.CredentialRef)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(output, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	launcher := *original.Runtime
	parameters := entry.ModelParameters.clone()
	content, err := toml.Marshal(map[string]any{"server": map[string]any{"listen": "127.0.0.1:0"}, "runtime": map[string]any{"dsh": launcher.DSH}, "models": modelCatalog{SchemaVersion: 1}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	config, err := loadStartupConfig(path, true)
	if err != nil {
		t.Fatal(err)
	}
	model, err := newModelService(config, newSystemSettings(config), home)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	if _, err := model.save(ctx, modelSave{Entry: modelEntry{ID: "daily", Name: "Acceptance", ModelParameters: parameters}, APIKey: credential.Value, ExpectedVersion: 0, MakeDefault: true}); err != nil {
		t.Fatal(err)
	}
	var originalRoom RoomIdentity
	var originalMember MemberIdentity
	var originalSpace SpaceContext
	var sessionID string
	binary := productionFixtureCLI(t)
	address := ""
	listen := func(network, requested string) (net.Listener, error) {
		if address != "" {
			requested = address
		}
		listener, err := net.Listen(network, requested)
		if err == nil {
			address = listener.Addr().String()
		}
		return listener, err
	}
	for iteration := 0; iteration < 2; iteration++ {
		selected := personalDeployment(nil)
		selected.factories.service = func(dir, id string, config serviceConfig) (*service, error) {
			// The test executable is not the product CLI. Only this executable binding
			// differs from the foreground production assembly.
			config.cliPath = binary
			return newConfiguredService(dir, id, config)
		}
		info, stop := startTestServer(t, []string{"--home", home}, serverOptions{deployment: &selected, platformCatalog: defaultPlatformCatalog(), listen: listen})
		client := serverTestCLI(t, home, info.Endpoint)
		m := selected.runtime.manager
		var target ServiceInfo
		var member MemberIdentity
		var svc *service
		for target.ServiceEpoch == "" {
			var rooms roomsResponse
			if err := serverTestRequest(ctx, client, "/v1/rooms", nil, &rooms); err != nil {
				t.Fatal(err)
			}
			for _, room := range rooms.Rooms {
				if rooms.DefaultAssistant != nil && room.RoomIdentity == rooms.DefaultAssistant.RoomIdentity && room.RuntimeStatus == "ready" && room.Service != nil {
					target = *room.Service
					m.mu.Lock()
					svc, _ = m.runtimeServiceLocked(room.RoomIdentity)
					m.mu.Unlock()
					snapshot, err := svc.store.LoadRoom()
					if err != nil {
						t.Fatal(err)
					}
					member = MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: snapshot.Members[0].AgentID, MembershipID: snapshot.Members[0].MembershipID}
				}
			}
			if ctx.Err() != nil {
				t.Fatal("assistant recovery timed out")
			}
			if target.ServiceEpoch == "" {
				time.Sleep(20 * time.Millisecond)
			}
		}
		if iteration == 0 {
			originalRoom, originalMember, originalSpace = target.RoomIdentity, member, selected.runtime.SpaceContext
		} else {
			if target.RoomIdentity != originalRoom || member != originalMember || selected.runtime.SpaceID != originalSpace.SpaceID || selected.runtime.OwnerUserID != originalSpace.OwnerUserID || selected.runtime.RuntimeEpoch == originalSpace.RuntimeEpoch {
				t.Fatal("restart changed identity or reused epoch")
			}
		}
		marker := []string{"TEAMX_SPACE_FIRST_OK", "TEAMX_SPACE_RESUMED_OK"}[iteration]
		var posted PostReceipt
		if err := serverTestRequest(ctx, client, "/v1/send", apiRequest{ServiceInfo: target, Caller: serverTestCaller(t, home), ClientMessageID: marker, Text: "这是隔离的启动与恢复验收。请通过 send 在当前会话仅回复 " + marker + "。不要调用平台、联网、读写文件或执行其他操作。"}, &posted); err != nil {
			t.Fatal(err)
		}
		for {
			snapshot, err := svc.store.LoadRoom()
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, message := range snapshot.Messages {
				if message.Author.Kind == "agent" && message.Text == marker {
					found = true
				}
			}
			if found {
				agent, err := svc.store.LoadAgent(member)
				if err != nil {
					t.Fatal(err)
				}
				if agent.Turn != nil && agent.Turn.Status == "completed" {
					break
				}
			}
			if ctx.Err() != nil {
				t.Fatal("real DSH reply timed out", ctx.Err())
			}
			time.Sleep(100 * time.Millisecond)
		}
		stop()
		statePath := filepath.Join(m.roomDir(member.RoomID), "agents", member.MembershipID, "session.json")
		ref, _, err := readSessionReference(statePath, member)
		if err != nil {
			t.Fatal(err)
		}
		var backend struct {
			SessionID string `json:"sessionId"`
			Stopped   bool   `json:"stopped"`
		}
		backendData, err := os.ReadFile(filepath.Join(filepath.Dir(statePath), ref.Directory, "backend.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(backendData, &backend); err != nil {
			t.Fatal(err)
		}
		if !backend.Stopped || backend.SessionID == "" || (iteration == 1 && backend.SessionID != sessionID) {
			t.Fatal("normal restart did not preserve original DSH Session")
		}
		sessionID = backend.SessionID
	}
	report["status"], report["model_id"], report["session_id"] = "passed", entry.ModelID, sessionID
	report["space_id"], report["owner_user_id"] = originalSpace.SpaceID, originalSpace.OwnerUserID
	report["original_session_resumed"], report["installed_artifact"] = true, launcher.DSH.Artifact
}
