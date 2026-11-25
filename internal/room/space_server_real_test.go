//go:build e2e

package room

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func TestSpacesInstalledDSHRecovery(t *testing.T)        { testSpacesInstalledDSHRecovery(t, false) }
func TestSpacesInstalledDSHArchiveRecovery(t *testing.T) { testSpacesInstalledDSHRecovery(t, true) }
func testSpacesInstalledDSHRecovery(t *testing.T, archive bool) {
	source, output := os.Getenv("TEAMX_SPACES_REAL_CONFIG"), os.Getenv("TEAMX_SPACES_REAL_OUTPUT")
	if source == "" {
		t.Skip("explicit model source and fresh output required")
	}
	if !filepath.IsAbs(output) {
		t.Fatal("fresh absolute output required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"status": "failed", "external_business_writes": 0, "spaces": 2, "scope": "installed DSH/model, independent Space setup and normal restart with original Sessions"}
	report["archive_restore"] = archive
	if archive {
		report["scope"] = "installed DSH/model, archive both Spaces, restart while suspended and explicitly restore original Sessions"
	}
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
	credential, err := (&modelCredentials{dir: filepath.Join(filepath.Dir(source), "model-credentials")}).get(entry.CredentialRef)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(output, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := toml.Marshal(map[string]any{"server": map[string]any{"listen": "127.0.0.1:0"}, "runtime": map[string]any{"dsh": original.Runtime.DSH}, "models": modelCatalog{SchemaVersion: 1}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	startup, err := loadStartupConfig(path, true)
	if err != nil {
		t.Fatal(err)
	}
	models, err := newModelService(startup, newSystemSettings(startup), home)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
	defer cancel()
	if _, err := models.save(ctx, modelSave{Entry: modelEntry{ID: "daily", Name: "Acceptance A", ModelParameters: entry.ModelParameters.clone()}, APIKey: credential.Value, ExpectedVersion: 0, MakeDefault: true}); err != nil {
		t.Fatal(err)
	}
	binary := productionFixtureCLI(t)
	var address string
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
	type evidence struct {
		SpaceContext
		MemberIdentity
		SessionID string `json:"session_id"`
	}
	first := map[string]evidence{}
	sessions := map[string]string{}
	var a, b string
	for iteration := 0; iteration < 2; iteration++ {
		selected := personalDeployment(nil)
		selected.factories.service = func(dir, id string, config serviceConfig) (*service, error) {
			config.cliPath = binary
			return newConfiguredService(dir, id, config)
		}
		info, stop := startTestServer(t, []string{"--home", home}, serverOptions{deployment: &selected, platformCatalog: defaultPlatformCatalog(), listen: listen})
		client := serverTestCLI(t, home, info.Endpoint)
		s := selected.spaces
		if iteration == 0 {
			a = s.directory.DefaultSpaceID
			b, _ = newOpaqueID()
			var record SpaceRecord
			if err := client.request(ctx, "/v1/spaces", spaceChangeInput{RequestID: b, Name: "Acceptance B"}, &record); err != nil {
				t.Fatal(err)
			}
			waitSpaceState(t, s, b, "setup_required")
			child := client
			child.endpoint += spaceRoute(b)
			var operation setupOperation
			if err := child.request(ctx, "/v1/setup/model", setupModelInput{RuntimeEpoch: setupEpochForTest(t, child), RequestID: "real-space-b", ExpectedVersion: 1, ModelParameters: entry.ModelParameters.clone(), APIKey: credential.Value}, &operation); err != nil {
				t.Fatal(err)
			}
		}
		if iteration == 1 && archive {
			s.mu.Lock()
			count := len(s.entries)
			s.mu.Unlock()
			if count != 0 {
				t.Fatal("archived DSH restarted before explicit restore")
			}
			for _, space := range []string{a, b} {
				record := spaceRecordForTest(t, s, space)
				var restored SpaceRecord
				if err := client.request(ctx, "/v1/spaces/"+space+"/restore", spaceChangeInput{ExpectedVersion: record.Version}, &restored); err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, space := range []string{a, b} {
			child := client
			child.endpoint += spaceRoute(space)
			var runtime *SpaceRuntime
			for {
				s.mu.Lock()
				serverEntry := s.entries[space]
				s.mu.Unlock()
				serverEntry.mu.RLock()
				runtime, err = serverEntry.runtime, serverEntry.err
				serverEntry.mu.RUnlock()
				if err != nil {
					t.Fatal(err)
				}
				if runtime != nil && runtime.State() == "ready" {
					break
				}
				if ctx.Err() != nil {
					t.Fatal(ctx.Err())
				}
				time.Sleep(100 * time.Millisecond)
			}
			m := runtime.manager
			var target ServiceInfo
			var member MemberIdentity
			var svc *service
			for target.ServiceEpoch == "" {
				var rooms roomsResponse
				if err := serverTestRequest(ctx, child, "/v1/rooms", nil, &rooms); err != nil {
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
						member = MemberIdentity{RoomIdentity: target.RoomIdentity, AgentID: snapshot.Members[0].AgentID, MembershipID: snapshot.Members[0].MembershipID}
					}
				}
				if ctx.Err() != nil {
					t.Fatal(ctx.Err())
				}
				if target.ServiceEpoch == "" {
					time.Sleep(50 * time.Millisecond)
				}
			}
			if iteration == 1 {
				before := first[space]
				if member != before.MemberIdentity || runtime.OwnerUserID != before.OwnerUserID || runtime.RuntimeEpoch == before.RuntimeEpoch {
					t.Fatal("restart changed identity or reused epoch")
				}
			}
			marker := "TEAMX_SPACE_" + space + []string{"_FIRST_OK", "_RESUMED_OK"}[iteration]
			var posted PostReceipt
			if err := serverTestRequest(ctx, child, "/v1/send", apiRequest{ServiceInfo: target, Caller: serverTestCaller(t, home), ClientMessageID: marker, Text: "这是独立空间验收。请通过 send 在当前会话仅回复 " + marker + "。不要调用平台、联网、读写文件或执行其他操作。"}, &posted); err != nil {
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
					t.Fatal("real DSH reply timeout", ctx.Err())
				}
				time.Sleep(100 * time.Millisecond)
			}
			if iteration == 0 {
				first[space] = evidence{SpaceContext: runtime.SpaceContext, MemberIdentity: member}
			}
		}
		if iteration == 0 && archive {
			for _, space := range []string{a, b} {
				archiveSpaceForTest(t, s, space)
			}
		}
		stop()
		for _, space := range []string{a, b} {
			member := first[space].MemberIdentity
			s.mu.Lock()
			serverEntry := s.entries[space]
			s.mu.Unlock()
			serverEntry.mu.RLock()
			runtime := serverEntry.runtime
			serverEntry.mu.RUnlock()
			statePath := filepath.Join(runtime.manager.roomDir(member.RoomID), "agents", member.MembershipID, "session.json")
			ref, _, err := readSessionReference(statePath, member)
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(filepath.Join(filepath.Dir(statePath), ref.Directory, "backend.json"))
			if err != nil {
				t.Fatal(err)
			}
			var backend struct {
				SessionID string `json:"sessionId"`
				Stopped   bool   `json:"stopped"`
			}
			data, err := io.ReadAll(file)
			file.Close()
			if err != nil || json.Unmarshal(data, &backend) != nil || !backend.Stopped || backend.SessionID == "" {
				t.Fatal("missing normal-close evidence", err)
			}
			if iteration == 1 && sessions[space] != backend.SessionID {
				t.Fatal("restart rotated original Session")
			}
			sessions[space] = backend.SessionID
		}
	}
	if sessions[a] == sessions[b] {
		t.Fatal("DSH Session shared across Spaces")
	}
	report["status"], report["original_sessions_resumed"], report["sessions"], report["model_id"] = "passed", true, sessions, entry.ModelID
}
