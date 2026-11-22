//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Real app, HTTP, scheduling and Inbox persistence; synthetic agent/platform.
func TestInboxRecoveryBrowser(t *testing.T) {
	if os.Getenv("TEAMX_INBOX_RECOVERY_BROWSER") != "1" {
		t.Skip("optional local Chromium acceptance")
	}
	t.Setenv("USER", humanCaller.ID)
	out := os.Getenv("TEAMX_INBOX_RECOVERY_OUTPUT")
	if !filepath.IsAbs(out) {
		t.Fatal("fresh absolute output required")
	}
	if err := os.Mkdir(out, 0700); err != nil {
		t.Fatal(err)
	}
	m := testManager(t, filepath.Join(out, "rooms"), managerTestProfiles(t))
	m.config.endpoint, m.config.cliPath = "http://127.0.0.1:1", os.Args[0]
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	var reading atomic.Bool
	var selected MemberIdentity
	m.factory = func(dir, roomID string, config serviceConfig) (*service, error) {
		config.inboxMode = "cli" // Component seam has no ACP/MCP client; tested separately.
		return startService(dir, roomID, config, func(s *service, state RoomSnapshot) error {
			s.referenceResolver = referenceTestResolver
			first := state.Members[0]
			selected = MemberIdentity{RoomIdentity: state.RoomIdentity, AgentID: first.AgentID, MembershipID: first.MembershipID}
			for n := 1; n <= 61; n++ {
				source := referenceTestSource(fmt.Sprint(n))
				item := notification.Item{Key: fmt.Sprint(n), DeliveryID: fmt.Sprint(n), Text: fmt.Sprintf("隔离浏览器通知 #%d", n), Source: source, CreatedAt: time.Now().UTC()}
				if err := s.receiveInbox(context.Background(), inboxTarget(selected), item); err != nil {
					return err
				}
			}
			if err := s.store.initReferenceFollowups(selected); err != nil {
				return err
			}
			agent := AgentSnapshot{Version: formatVersion, MemberIdentity: selected, TurnNumber: 1, LastOfferedInboxSeq: 25, Turn: &TurnRecord{Number: 1, Status: "completed", InboxSnapshotSeq: 25}}
			if err := s.store.SaveAgent(agent); err != nil {
				return err
			}
			opts := SessionOptions{MemberIdentity: selected, StatePath: filepath.Join(s.store.dir, "agents", selected.MembershipID, "session.json")}
			generation, err := (dshBackend{store: s.store}).sessionGeneration(opts)
			if err != nil {
				return err
			}
			if err := s.store.saveScheduling(memberScheduling{Version: schedulingVersion, MemberIdentity: selected, Enabled: true, Used: 1, SessionGeneration: generation}); err != nil {
				return err
			}
			s.memberFactory = func(id MemberIdentity, runner RunnerConfig, _ *AgentSnapshot) actorx.Actor {
				var restored *AgentSnapshot
				if id == selected {
					restored = &agent
				}
				member := s.newMember(id, runner, restored).(*MemberAgent)
				member.now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
				b := member.Backend.(dshBackend)
				b.open = func(_ context.Context, options dsh.Options) (dshComponent, error) {
					return &adapterComponent{run: func(ctx context.Context, _ string) (dsh.TurnResult, error) {
						if !reading.Load() {
							return dsh.TurnResult{}, nil
						}
						current, err := s.store.LoadAgent(id)
						if err != nil {
							return dsh.TurnResult{}, err
						}
						for {
							page, err := s.readReferenceInbox(ctx, id, current.TurnNumber, 7, referenceTestResolver)
							if err != nil {
								return dsh.TurnResult{}, err
							}
							if page.Batch.Receipt != "" {
								s.inboxMu.Lock()
								addr, err := s.inboxLocked(ctx, id)
								s.inboxMu.Unlock()
								if err != nil {
									return dsh.TurnResult{}, err
								}
								if _, err = addr.Call[InboxStatus](ctx, ConfirmInboxBatch{MemberIdentity: id, TurnNumber: current.TurnNumber, Receipt: page.Batch.Receipt}); err != nil {
									return dsh.TurnResult{}, err
								}
							}
							if !page.HasMore {
								break
							}
						}
						return dsh.TurnResult{}, nil
					}}, nil
				}
				member.Backend = b
				return member
			}
			for _, item := range state.Members {
				id := MemberIdentity{RoomIdentity: state.RoomIdentity, AgentID: item.AgentID, MembershipID: item.MembershipID}
				addr, err := s.spawnMember(id, item.Runner, nil)
				if err != nil {
					return err
				}
				s.setTarget(id.MembershipID, addr)
				if _, err = s.room.Call[MemberInfo](context.Background(), AttachMember{MemberIdentity: id, Target: addr}); err != nil {
					return err
				}
			}
			return nil
		})
	}
	room := managerCreate(t, m, "inbox-recovery-browser")
	svc := openRoomOK(t, m, room, "open-inbox-recovery-browser")
	// The fixture resolver supplies synthetic account evidence; no managed platform connection is used.
	svc.referenceCommit = nil
	// Restore enabled intent without a chat message. Unknown/paused checks stay live.
	if _, err := svc.control(context.Background(), apiRequest{AgentID: selected.AgentID, MembershipID: selected.MembershipID}, "run"); err != nil {
		t.Fatal(err)
	}
	target := svc.target(selected.MembershipID)
	waitMember(t, context.Background(), target, selected, func(s MemberStatus) bool { return s.InboxRecovery != nil })
	server := browserTestServer(t, m, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__inbox_state" {
			status, err := target.Call[MemberStatus](r.Context(), InspectMember{selected})
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(status)
			return
		}
		if r.URL.Path != "/__inbox_recovery" {
			m.ServeHTTP(w, r)
			return
		}
		var input struct {
			Seconds int64 `json:"seconds"`
			Read    bool  `json:"read"`
		}
		if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&input) != nil {
			http.Error(w, "invalid command", 400)
			return
		}
		clock.Add(int64(time.Duration(input.Seconds) * time.Second))
		if input.Read {
			reading.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/inbox_recovery_browser.cjs", server.URL, out, selected.MembershipID)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, data)
	} else {
		t.Log(string(data))
	}
	final := waitMember(t, ctx, target, selected, func(s MemberStatus) bool { return !s.Active && s.Inbox != nil && s.Inbox.ConsumedThrough == 61 })
	roomState, _ := svc.store.LoadRoom()
	saved, err := svc.store.loadScheduling(selected)
	if err != nil || final.InboxRecovery != nil || len(roomState.Messages) != 0 || saved.InboxContinueCount != 1 {
		t.Fatal("continue did not drain without chat", final, saved, err)
	}
}
