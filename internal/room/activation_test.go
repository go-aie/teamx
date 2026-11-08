package room

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
)

func activationTestManager(t *testing.T) *roomManager {
	t.Helper()
	profiles := managerTestProfiles(t)
	p := profiles["runtime"]
	p.BinaryPath, p.BinaryArgs = os.Args[0], nil
	profiles["runtime"] = p
	m := testManager(t, t.TempDir(), profiles)
	m.config = serviceConfig{endpoint: "http://127.0.0.1:1", cliPath: os.Args[0]}
	return m
}

func beginManagedOpen(t *testing.T, m *roomManager, target roomSummary, id string) roomOpen {
	t.Helper()
	r := managerHTTP(m, "/v1/rooms/open", roomOpenRequest{ServiceEpoch: m.epoch, Caller: humanCaller, RoomIdentity: target.RoomIdentity, OpenID: id})
	var op roomOpen
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &op) != nil {
		t.Fatal(r.Code, r.Body.String())
	}
	return op
}

func awaitLatestOpen(t *testing.T, m *roomManager) roomOpen {
	t.Helper()
	m.mu.Lock()
	op := m.index.Opens[len(m.index.Opens)-1]
	done := m.runtimes[op.RoomIdentity].operationDone
	m.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("activation did not settle")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.index.Opens[len(m.index.Opens)-1]
}

func openRoomOK(t *testing.T, m *roomManager, target roomSummary, id string) *service {
	t.Helper()
	beginManagedOpen(t, m, target, id)
	if op := awaitLatestOpen(t, m); op.Status != "completed" {
		t.Fatal(op)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runtimes[target.RoomIdentity].service
}

// Use the production adapter, persisted references, recovery and scheduler.
// Only DSH's subprocess/model component is substituted with a controlled one.
func controlledActivationFactory(run func(MemberIdentity, context.Context) error, closeSession func(MemberIdentity)) func(string, string, serviceConfig) (*service, error) {
	return func(dir, id string, config serviceConfig) (*service, error) {
		return startService(dir, id, config, func(s *service, state RoomSnapshot) error {
			if err := s.loadMembers(state); err != nil {
				return err
			}
			s.memberFactory = func(id MemberIdentity, runner RunnerConfig, restored *AgentSnapshot) actorx.Actor {
				m := s.newMember(id, runner, restored).(*MemberAgent)
				backend := m.Backend.(dshBackend)
				backend.open = func(_ context.Context, opts dsh.Options) (dshComponent, error) {
					if err := os.MkdirAll(opts.StateDir, 0700); err != nil {
						return nil, err
					}
					// The fake backend exposes only enough durable normal-close evidence
					// for the existing CheckResume gate; it is not real ACP recovery proof.
					record := map[string]any{"version": 1, "sessionId": id.MembershipID, "configSHA256": "controlled", "stopped": false}
					save := func() error {
						data, _ := json.Marshal(record)
						return os.WriteFile(filepath.Join(opts.StateDir, "backend.json"), data, 0600)
					}
					if err := save(); err != nil {
						return nil, err
					}
					return &adapterComponent{run: func(ctx context.Context, _ string) (dsh.TurnResult, error) {
						var err error
						if run != nil {
							err = run(id, ctx)
						}
						return dsh.TurnResult{StopReason: "end_turn"}, err
					}, close: func(context.Context) error {
						if closeSession != nil {
							closeSession(id)
						}
						record["stopped"] = true
						return save()
					}}, nil
				}
				m.Backend = backend
				return m
			}
			return s.attachMembers(state)
		})
	}
}

func managedPost(t *testing.T, m *roomManager, s *service, id string) {
	t.Helper()
	r := managerHTTP(m, "/v1/send", apiRequest{ServiceInfo: s.info, Caller: humanCaller, ClientMessageID: id, Text: id})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
}
