package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/actorx"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func TestRotationWaitsForTurnAndCleanupWithoutRenewingBudget(t *testing.T) {
	for _, cancelTurn := range []bool{false, true} {
		t.Run(map[bool]string{false: "finish", true: "cancel"}[cancelTurn], func(t *testing.T) {
			state := historyRoom(8, "body")
			state.Members[0].Runner = RunnerConfig{Kind: "dsh", Profile: "reviewer"}
			h := newRoomHarness(t, state, nil)
			id := h.memberID("A", "generation-A")
			opts := adapterOptions(h.store, id)
			entered, finish, canceled := make(chan struct{}, 2), make(chan struct{}), make(chan struct{}, 1)
			closing, closeRelease := make(chan struct{}, 1), make(chan struct{})
			var opens atomic.Int32
			backend := dshBackend{store: h.store, open: func(_ context.Context, o dsh.Options) (dshComponent, error) {
				n := opens.Add(1)
				if n > 1 && !strings.Contains(o.Instructions, "private context has been reset") {
					t.Error("missing handoff instructions")
				}
				return &adapterComponent{run: func(ctx context.Context, _ string) (dsh.TurnResult, error) {
					entered <- struct{}{}
					if n > 1 {
						return dsh.TurnResult{StopReason: "end_turn"}, nil
					}
					select {
					case <-ctx.Done():
						canceled <- struct{}{}
						<-finish
						return dsh.TurnResult{}, ctx.Err()
					case <-finish:
						return dsh.TurnResult{StopReason: "end_turn"}, nil
					}
				}, close: func(context.Context) error {
					if n == 1 {
						closing <- struct{}{}
						<-closeRelease
					}
					return nil
				}}, nil
			}}
			addr, err := h.spawn(&MemberAgent{Options: opts, Store: h.store, Room: h.addr, Backend: backend, MaxTurns: 2})
			if err != nil {
				t.Fatal(err)
			}
			before, _ := h.store.LoadRoom()
			input := UnreadChanged{MemberIdentity: id, Revision: 8, LatestSeq: 8, NewestUnreadSeq: 8, UnreadCount: 8}
			controlMember(t, h, addr, id, "run", input, false, false)
			<-entered
			svc := &service{info: ServiceInfo{RoomIdentity: id.RoomIdentity}, room: h.addr,
				targets: map[string]actorx.Addr{id.MembershipID: addr}, dshProfiles: map[string]dshProfile{"reviewer": {}}}
			wait, cancel := context.WithTimeout(h.ctx, 30*time.Millisecond)
			_, err = svc.rotateSession(wait, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID, Cancel: cancelTurn})
			cancel()
			if err == nil || (!strings.Contains(err.Error(), "incomplete") && !strings.Contains(err.Error(), "unknown")) {
				t.Fatal("command did not report unfinished rotation", err)
			}
			if cancelTurn {
				<-canceled
			}
			for _, action := range []string{"run", "rotate"} {
				if _, err := addr.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: id, Action: action, State: input}); !errors.Is(err, ErrBusy) {
					t.Fatal(action, err)
				}
			}
			s := inspectMember(t, h.ctx, addr, id)
			if s.Rotation.Status != "waiting" || opens.Load() != 1 {
				t.Fatal(s)
			}
			close(finish)
			<-closing
			ref, _, err := backend.reference(opts)
			if err != nil || ref.Pending {
				t.Fatal("changed reference before cleanup", ref, err)
			}
			s = inspectMember(t, h.ctx, addr, id)
			if s.Status != "rotating" || !s.Paused || opens.Load() != 1 {
				t.Fatal(s)
			}
			close(closeRelease)
			s = waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return s.Rotation.Status == "completed" })
			if !s.Paused || s.Active || s.TurnBudget.Used != 1 || opens.Load() != 1 {
				t.Fatal(s)
			}
			after, _ := h.store.LoadRoom()
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rotation changed Room", after)
			}
			ref, exists, err := backend.reference(opts)
			if err != nil || exists || !ref.Pending {
				t.Fatal(ref, exists, err)
			}
			controlMember(t, h, addr, id, "run", input, true, false)
			<-entered
			s = waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
			if opens.Load() != 2 || s.Status != "budget_exhausted" || s.Agent.LastOfferedUnreadSeq != 8 {
				t.Fatal(s, opens.Load())
			}
			if _, err := addr.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: id, Action: "run", State: input, Retry: true}); !errors.Is(err, ErrTurnBudget) {
				t.Fatal(err)
			}
		})
	}
}

func TestRotationCleanupFailureNeverAuthorizesReplacement(t *testing.T) {
	h := newRoomHarness(t, historyRoom(1, "body"), nil)
	id := h.memberID("A", "generation-A")
	opts := adapterOptions(h.store, id)
	backend := dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
		return &adapterComponent{run: func(context.Context, string) (dsh.TurnResult, error) { return dsh.TurnResult{}, nil }, close: func(context.Context) error { return errors.New("unconfirmed cleanup") }}, nil
	}}
	addr, err := h.spawn(&MemberAgent{Options: opts, Store: h.store, Room: h.addr, Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	input := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
	controlMember(t, h, addr, id, "run", input, false, false)
	waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
	before, _ := os.ReadFile(opts.StatePath)
	controlMember(t, h, addr, id, "rotate", UnreadChanged{}, false, false)
	s := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return s.Rotation.Status == "failed" })
	after, _ := os.ReadFile(opts.StatePath)
	if string(before) != string(after) || !s.Paused {
		t.Fatal(s)
	}
	if _, err := addr.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: id, Action: "run", State: input, Retry: true}); err == nil {
		t.Fatal("ran after failed cleanup")
	}
	// A repeat still sees the same failed cleanup; no replacement authority.
	controlMember(t, h, addr, id, "rotate", UnreadChanged{}, false, false)
	waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return s.Rotation.Status == "failed" })
	_ = addr.Stop(nil)
	exit, err := addr.Wait(h.ctx)
	if err != nil || exit.Reason == nil {
		t.Fatal(exit, err)
	}
}

func TestRotationRecoversFailedButSettledSession(t *testing.T) {
	h := newRoomHarness(t, historyRoom(1, "pending human request"), nil)
	id := h.memberID("A", "generation-A")
	opts := adapterOptions(h.store, id)
	var opens atomic.Int32
	backend := dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
		failed := opens.Add(1) == 1
		return &adapterComponent{run: func(context.Context, string) (dsh.TurnResult, error) {
			if failed {
				return dsh.TurnResult{}, errors.Join(dsh.ErrUncertain, dsh.ErrRateLimited)
			}
			return dsh.TurnResult{StopReason: "end_turn"}, nil
		}, close: func(context.Context) error {
			if failed {
				return &dsh.ClosedError{Err: dsh.ErrRecoveryUnsupported}
			}
			return nil
		}}, nil
	}}
	addr, err := h.spawn(&MemberAgent{Options: opts, Store: h.store, Room: h.addr, Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	input := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
	controlMember(t, h, addr, id, "run", input, false, false)
	failed := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
	if failed.Status != "backend_blocked" || failed.Agent.Turn.Status != "failed" {
		t.Fatal("disabled session was presented as runnable", failed)
	}
	if _, err := addr.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: id, Action: "run", State: input, Retry: true}); err == nil {
		t.Fatal("failed original session was reused")
	}
	controlMember(t, h, addr, id, "rotate", UnreadChanged{}, false, false)
	waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return s.Rotation.Status == "completed" })
	controlMember(t, h, addr, id, "run", input, true, false)
	recovered := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
	if recovered.Agent.Turn.Status != "completed" || recovered.TurnBudget.Used != 2 || opens.Load() != 2 {
		t.Fatal("explicit reset did not recover pending work or preserve budget", recovered, opens.Load())
	}
}

func TestRotationRequiresDurableSettlementAfterRestart(t *testing.T) {
	for _, mode := range []string{"stopped", "settled", "unsettled", "pending", "consumed", "invalid-path"} {
		t.Run(mode, func(t *testing.T) {
			dir, id := recoveryFixture(t)
			store := openTestStore(t, dir)
			state, _ := store.LoadRoom()
			state.Members[0].Runner = RunnerConfig{Kind: "dsh", Profile: "reviewer"}
			if err := store.SaveRoom(state); err != nil {
				t.Fatal(err)
			}
			opts := adapterOptions(store, id)
			ref := dshReference{Version: formatVersion, MemberIdentity: id, Directory: "session.json.dsh"}
			if mode == "pending" || mode == "consumed" {
				ref.Directory += "-0123456789abcdef0123456789abcdef"
				ref.Pending = mode == "pending"
			}
			if mode == "invalid-path" {
				ref.Directory = "../session.json.dsh"
			}
			store.mu.Lock()
			err := store.save(opts.StatePath, ref, maxPrivateFileBytes)
			store.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if mode == "stopped" || mode == "settled" || mode == "unsettled" {
				path := dshStateDir(opts.StatePath)
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
				stopped := "false"
				if mode == "stopped" {
					stopped = "true"
				}
				if err := os.WriteFile(filepath.Join(path, "backend.json"), []byte(`{"version":1,"sessionId":"old","configSHA256":"digest","stopped":`+stopped+`}`), 0600); err != nil {
					t.Fatal(err)
				}
				if mode == "settled" {
					if err := os.WriteFile(filepath.Join(path, "settled.json"), []byte(`{"version":1}`), 0600); err != nil {
						t.Fatal(err)
					}
					if err := dsh.CheckResume(path); err == nil {
						t.Fatal("settled failure became resumable")
					}
				}
			}
			b := dshBackend{store: store}
			before, _ := os.ReadFile(opts.StatePath)
			_, err = b.RotateSession(opts, false)
			allowed := mode == "stopped" || mode == "settled" || mode == "pending"
			if (err == nil) != allowed {
				t.Fatal(mode, err)
			}
			if !allowed {
				after, _ := os.ReadFile(opts.StatePath)
				if string(before) != string(after) {
					t.Fatal("changed blocked reference")
				}
				return
			}
			// Rotation stays durable before first Open, including across restart.
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			config := recoveryConfig()
			config.dshProfiles = map[string]dshProfile{"reviewer": {}}
			svc, err := newService(dir, id.RoomID, config)
			if err != nil {
				t.Fatal(err)
			}
			defer closeService(t, svc)
			r := svc.recovered[0]
			if r.blocked != "" || r.backendResume || r.resume {
				t.Fatal(r)
			}
			current := inspectMember(t, context.Background(), svc.target(id.MembershipID), id)
			if !current.Paused || current.Active {
				t.Fatal(current)
			}
		})
	}
}

// CLI parsing and HTTP routing run through the real service without a socket.
type rotationTransport struct{ svc *service }

func (r rotationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	r.svc.ServeHTTP(recorder, req)
	return recorder.Result(), nil
}

func TestRotationCLIAndOperatorBoundary(t *testing.T) {
	config := recoveryConfig()
	config.dshProfiles = map[string]dshProfile{"reviewer": {}}
	svc, err := newService(t.TempDir(), "rotation", config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	old := http.DefaultTransport
	http.DefaultTransport = rotationTransport{svc}
	defer func() { http.DefaultTransport = old }()
	env := func(key string) string {
		if key == "TEAMX_ENDPOINT" {
			return config.endpoint
		}
		return ""
	}
	command := func(args ...string) []byte {
		t.Helper()
		var out, logs bytes.Buffer
		if err := runCLI(context.Background(), args, &out, &logs, env); err != nil {
			t.Fatal(args, err, logs.String())
		}
		return out.Bytes()
	}
	command("member", "add", "--agent", "A", "--runner", "dsh", "--profile", "reviewer")
	command("send", "--text", "preserved", "--id", "one")
	var result MemberStatus
	if err := json.Unmarshal(command("member", "session", "rotate", "--agent", "A", "--json"), &result); err != nil {
		t.Fatal(err)
	}
	if result.Rotation == nil || result.Rotation.Status != "completed" || !result.Rotation.ContextReset {
		t.Fatal(result)
	}
	command("member", "pause", "--agent", "A", "--cancel")
	list, err := svc.members(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := list.Members[0]
	req := apiRequest{ServiceInfo: svc.info, Caller: Caller{Author: Author{Kind: "agent", ID: "A"}, MembershipID: m.MembershipID}, AgentID: "A", MembershipID: m.MembershipID}
	data, _ := json.Marshal(req)
	recorder := httptest.NewRecorder()
	svc.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/member/session/rotate", bytes.NewReader(data)))
	if recorder.Code != http.StatusForbidden {
		t.Fatal("member managed rotation", recorder.Code)
	}
}

func TestRotationCommitFailureDoesNotLaunch(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-replace", true: "after-replace"}[uncertain], func(t *testing.T) {
			h := newRoomHarness(t, historyRoom(1, "body"), nil)
			opts := adapterOptions(h.store, h.memberID("A", "generation-A"))
			var opens atomic.Int32
			b := dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
				opens.Add(1)
				return nil, errors.New("unexpected open")
			}}
			h.store.mu.Lock()
			if uncertain {
				h.store.ops.rename = func(from, to string) error {
					if err := os.Rename(from, to); err != nil {
						return err
					}
					return errors.New("ambiguous replacement")
				}
			} else {
				h.store.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, errors.New("disk unavailable") }
			}
			h.store.mu.Unlock()
			if _, err := b.RotateSession(opts, false); err == nil {
				t.Fatal("rotation succeeded")
			}
			if uncertain {
				if _, err := b.RotateSession(opts, false); !errors.Is(err, ErrCommitUnknown) {
					t.Fatal(err)
				}
			}
			if opens.Load() != 0 {
				t.Fatal("launched on failed commit")
			}
		})
	}
}

func TestRotationRunResumesOnlyNewInput(t *testing.T) {
	h := newRoomHarness(t, historyRoom(8, "body"), nil)
	id := h.memberID("A", "generation-A")
	opts := adapterOptions(h.store, id)
	var opens atomic.Int32
	backend := dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
		opens.Add(1)
		return &adapterComponent{run: func(context.Context, string) (dsh.TurnResult, error) {
			return dsh.TurnResult{StopReason: "end_turn"}, nil
		}}, nil
	}}
	addr, err := h.spawn(&MemberAgent{Options: opts, Store: h.store, Room: h.addr, Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	roomCall[MemberInfo](t, h, AttachMember{MemberIdentity: id, Target: addr})
	controlMember(t, h, addr, id, "run", UnreadChanged{MemberIdentity: id, Revision: 8, LatestSeq: 8, NewestUnreadSeq: 8, UnreadCount: 8}, false, false)
	waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
	if _, err := addr.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: id, Action: "rotate", Resume: true}); err != nil {
		t.Fatal(err)
	}
	s := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return s.Rotation.Status == "completed" })
	if s.Paused || s.Active || s.Agent.TurnNumber != 1 || opens.Load() != 1 {
		t.Fatal("rotation replayed offered input", s)
	}
	roomCall[PostReceipt](t, h, h.post(humanCaller, "new-after-rotation", "new input"))
	s = waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
	if opens.Load() != 2 || s.Agent.LastOfferedUnreadSeq != 9 {
		t.Fatal(s)
	}
}

func TestRotatedSessionResumeUsesSameDirectoryAndRole(t *testing.T) {
	dir, id := recoveryFixture(t)
	store := openTestStore(t, dir)
	state, err := store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	state.Members[0].Runner = RunnerConfig{Kind: "dsh", Profile: "reviewer"}
	if err := store.SaveRoom(state); err != nil {
		t.Fatal(err)
	}
	opts := adapterOptions(store, id)
	var opened []dsh.Options
	fake := func(_ context.Context, o dsh.Options) (dshComponent, error) {
		opened = append(opened, o)
		if err := os.MkdirAll(o.StateDir, 0700); err != nil {
			return nil, err
		}
		return &adapterComponent{close: func(context.Context) error {
			return os.WriteFile(filepath.Join(o.StateDir, "backend.json"), []byte(`{"version":1,"sessionId":"rotated-original","configSHA256":"digest","stopped":true}`), 0600)
		}}, nil
	}
	profile := dshProfile{Instructions: "reviewer role"}
	backend := dshBackend{store: store, profile: profile, open: fake}
	next, err := backend.RotateSession(opts, false)
	if err != nil {
		t.Fatal(err)
	}
	session, err := next.OpenSession(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedUnreadSeq: 3, Turn: &TurnRecord{Number: 1, Status: "completed", SnapshotSeq: 3, OfferedUnreadSeq: 3}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	config := recoveryConfig()
	config.dshProfiles = map[string]dshProfile{"reviewer": profile}
	svc, err := newService(dir, id.RoomID, config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	r := svc.recovered[0]
	if !r.backendResume || !r.resume || r.blocked != "" {
		t.Fatal(r)
	}
	resumed := dshBackend{store: svc.store, profile: profile, recover: true, open: fake}
	opts.CLIEnv = svc.toolEnv(id)
	session, err = resumed.OpenSession(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 2 || opened[0].StateDir != opened[1].StateDir || opened[0].Instructions != opened[1].Instructions || !strings.Contains(opened[0].Instructions, "private context has been reset") || opened[0].ToolEnv["TEAMX_SERVICE_EPOCH"] == opened[1].ToolEnv["TEAMX_SERVICE_EPOCH"] {
		t.Fatal("rotated resume changed directory/role or kept stale tool identity", opened)
	}
}

func TestRotationShutdownJoinsOldCleanupBeforeReleasingActor(t *testing.T) {
	h := newRoomHarness(t, historyRoom(1, "body"), nil)
	id := h.memberID("A", "generation-A")
	opts := adapterOptions(h.store, id)
	closing, release := make(chan struct{}, 1), make(chan struct{})
	var opens atomic.Int32
	backend := dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
		opens.Add(1)
		return &adapterComponent{run: func(context.Context, string) (dsh.TurnResult, error) { return dsh.TurnResult{}, nil }, close: func(context.Context) error { closing <- struct{}{}; <-release; return nil }}, nil
	}}
	addr, err := h.spawn(&MemberAgent{Options: opts, Store: h.store, Room: h.addr, Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	controlMember(t, h, addr, id, "run", UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}, false, false)
	waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
	before, _ := os.ReadFile(opts.StatePath)
	controlMember(t, h, addr, id, "rotate", UnreadChanged{}, false, false)
	<-closing
	_ = addr.Stop(nil)
	wait, cancel := context.WithTimeout(h.ctx, 30*time.Millisecond)
	_, err = addr.Wait(wait)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("released ownership before old cleanup", err)
	}
	close(release)
	if _, err := addr.Wait(h.ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(opts.StatePath)
	if string(before) != string(after) || opens.Load() != 1 {
		t.Fatal("shutdown launched or committed a replacement")
	}
}
