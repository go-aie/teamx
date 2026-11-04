package room

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
)

type activationRequest struct {
	ServiceEpoch string `json:"service_epoch"`
	Caller       Caller `json:"caller"`
	RoomIdentity
	ActivateID string `json:"activate_id"`
}

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

func beginActivation(t *testing.T, m *roomManager, target roomSummary, id string) roomActivation {
	t.Helper()
	r := managerHTTP(m, "/v1/rooms/activate", activationRequest{ServiceEpoch: m.epoch, Caller: humanCaller, RoomIdentity: target.RoomIdentity, ActivateID: id})
	var op roomActivation
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &op) != nil {
		t.Fatal(r.Code, r.Body.String())
	}
	return op
}

func awaitActivation(t *testing.T, m *roomManager) roomActivation {
	t.Helper()
	m.mu.Lock()
	done := m.operationDone
	m.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("activation did not settle")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.index.Activations[len(m.index.Activations)-1]
}

func activateOK(t *testing.T, m *roomManager, target roomSummary, id string) *service {
	t.Helper()
	beginActivation(t, m, target, id)
	if op := awaitActivation(t, m); op.Status != "completed" {
		t.Fatal(op)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
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

func TestManagedActivationHistoryIntentBudgetAndRestart(t *testing.T) {
	m := activationTestManager(t)
	m.factory = controlledActivationFactory(nil, nil)
	a, b := managerCreate(t, m, "a"), managerCreate(t, m, "b")
	sa := activateOK(t, m, a, "enter-a")
	list, err := sa.members(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	paused := list.Members[0]
	if _, err := sa.control(context.Background(), apiRequest{AgentID: paused.AgentID, MembershipID: paused.MembershipID}, "pause"); err != nil {
		t.Fatal(err)
	}
	managedPost(t, m, sa, "a-history")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, member := range list.Members[1:] {
		id := MemberIdentity{RoomIdentity: a.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
		waitMember(t, ctx, sa.target(member.MembershipID), id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
	}
	sb := activateOK(t, m, b, "enter-b")
	bs, _ := sb.store.LoadRoom()
	if bs.LatestSeq != 0 {
		t.Fatal("new Room copied messages")
	}
	stale := apiRequest{ServiceInfo: sa.info, Caller: humanCaller, ClientMessageID: "late", Text: "must not cross rooms"}
	if r := managerHTTP(m, "/v1/send", stale); r.Code != 403 {
		t.Fatal(r.Code, r.Body.String())
	}
	sa2 := activateOK(t, m, a, "return-a")
	as, _ := sa2.store.LoadRoom()
	if as.LatestSeq != 1 || as.Messages[0].Text != "a-history" {
		t.Fatal(as)
	}
	list, _ = sa2.members(ctx)
	for _, member := range list.Members {
		status := member.Execution
		if member.MembershipID == paused.MembershipID {
			if !status.Paused || status.TurnBudget.Used != 0 {
				t.Fatal("manual pause lost", status)
			}
		} else if status.Paused || status.TurnBudget.Used != 1 {
			t.Fatal("healthy enable intent or budget lost", status)
		}
	}
	if sa2.info.ServiceEpoch == sa.info.ServiceEpoch {
		t.Fatal("reactivation reused tool epoch")
	}
	// Old successful receipts cannot switch away from the current Room.
	if op := beginActivation(t, m, b, "enter-b"); op.Status != "completed" {
		t.Fatal(op)
	}
	if m.active != sa2 {
		t.Fatal("replayed old activation")
	}
	before := as
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	restored, err := newRoomManager(m.store.dir, 99)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.close()
	restored.config, restored.factory = m.config, m.factory
	if err := restored.restoreActive(); err != nil {
		t.Fatal(err)
	}
	if op := awaitActivation(t, restored); op.Status != "completed" {
		t.Fatal(op)
	}
	after, _ := restored.active.store.LoadRoom()
	if !reflect.DeepEqual(before, after) || restored.active.maxTurns != 7 {
		t.Fatal("restart reinitialized Room or budget")
	}
	status, _ := restored.active.members(ctx)
	if !status.Members[0].Execution.Paused || status.Members[1].Execution.TurnBudget.Used != 1 {
		t.Fatal(status)
	}
}

func TestManagedSwitchWaitsForRunningTurnAndCleanup(t *testing.T) {
	m := activationTestManager(t)
	entered, cancelled, cleanup, release := make(chan MemberIdentity, 4), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once, cleanupOnce sync.Once
	var a roomSummary
	m.factory = controlledActivationFactory(func(id MemberIdentity, ctx context.Context) error {
		entered <- id
		if id.RoomID == a.RoomID {
			<-ctx.Done()
			once.Do(func() { close(cancelled) })
			return ctx.Err()
		}
		return nil
	}, func(id MemberIdentity) {
		if id.RoomID == a.RoomID {
			// A return visit can open another pending member Session. This
			// channel signals room cleanup once; every Session still waits.
			cleanupOnce.Do(func() { close(cleanup) })
			<-release
		}
	})
	a = managerCreate(t, m, "running-a")
	b := managerCreate(t, m, "running-b")
	targetConfig, _ := readManagedRoom(t, m, b)
	missing := filepath.Join(m.roomDir(b.RoomID), filepath.FromSlash(targetConfig.Members[0].Workspace))
	if err := os.RemoveAll(missing); err != nil {
		t.Fatal(err)
	}
	sa := activateOK(t, m, a, "first")
	managedPost(t, m, sa, "running")
	var running MemberIdentity
	select {
	case running = <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no turn")
	}
	beginActivation(t, m, b, "switch")
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("no cancellation")
	}
	select {
	case <-cleanup:
	case <-time.After(5 * time.Second):
		t.Fatal("no cleanup")
	}
	if op := beginActivation(t, m, b, "switch"); op.Status != "running" {
		t.Fatal(op)
	}
	if r := managerHTTP(m, "/v1/rooms/create", roomCreateRequest{ServiceEpoch: m.epoch, Caller: humanCaller, roomSelection: roomSelection{CreateID: "busy", TeamID: "takeout-dinner"}}); r.Code != 409 {
		t.Fatal(r.Code)
	}
	// A caller-side shutdown timeout must keep root and old execution owned.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.shutdown(ctx); err == nil {
		t.Fatal("released unsettled management")
	}
	if _, err := newRoomManager(m.store.dir, 1); !errors.Is(err, ErrLocked) {
		t.Fatal(err)
	}
	m.mu.Lock()
	if m.active != sa {
		t.Error("target started before old cleanup")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Error("prepared target before old cleanup settled", err)
	}
	m.closing = false
	m.mu.Unlock()
	close(release)
	if op := awaitActivation(t, m); op.Status != "completed" {
		t.Fatal(op)
	}
	if info, err := os.Stat(missing); err != nil || !info.IsDir() {
		t.Fatal("target workspace was not prepared", err)
	}
	select {
	case id := <-entered:
		t.Fatal("unexpected target execution", id)
	default:
	}
	sa2 := activateOK(t, m, a, "return")
	status := inspectMember(t, context.Background(), sa2.target(running.MembershipID), running)
	if !status.Paused || status.Agent.Turn.Status != "interrupted" || status.TurnBudget.Used != 1 {
		t.Fatal(status)
	}
}

func TestManagedActivationFailureReceiptsAndInterruptedRecovery(t *testing.T) {
	m := activationTestManager(t)
	m.factory = controlledActivationFactory(nil, nil)
	a, b := managerCreate(t, m, "a"), managerCreate(t, m, "b")
	sa := activateOK(t, m, a, "first")
	managedPost(t, m, sa, "retained")
	t.Setenv(m.teams["takeout-dinner"].Runtime.Model.APIKeyEnv, "")
	beginActivation(t, m, b, "missing-key")
	if op := awaitActivation(t, m); op.Status != "failed" || !strings.Contains(op.Error, "credential") {
		t.Fatal(op)
	}
	if m.active != sa || sa.closing.Load() {
		t.Fatal("preflight stopped old room")
	}
	if op := beginActivation(t, m, b, "missing-key"); op.Status != "failed" {
		t.Fatal(op)
	}
	if r := managerHTTP(m, "/v1/rooms/activate", activationRequest{ServiceEpoch: m.epoch, Caller: humanCaller, RoomIdentity: a.RoomIdentity, ActivateID: "missing-key"}); r.Code != 409 {
		t.Fatal(r.Code)
	}
	t.Setenv(m.teams["takeout-dinner"].Runtime.Model.APIKeyEnv, "offline-test-only")
	m.factory = func(string, string, serviceConfig) (*service, error) { return nil, errors.New("assembly failed") }
	beginActivation(t, m, b, "assembly")
	if op := awaitActivation(t, m); op.Status != "failed" || op.Phase != "opening" {
		t.Fatal(op)
	}
	if m.index.ActiveRoomID != a.RoomID {
		t.Fatal("failed switch changed last selection")
	}
	// Simulate an exit with a retained running marker. Startup records failure,
	// does not continue it or assemble the last selected Room automatically.
	next := m.copyIndex()
	next.Activations[len(next.Activations)-1].Status = "running"
	if err := m.saveIndex(next); err != nil {
		t.Fatal(err)
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	restored, err := newRoomManager(m.store.dir, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.close()
	var assemblies atomic.Int32
	restored.config = m.config
	restored.factory = func(dir, id string, c serviceConfig) (*service, error) {
		assemblies.Add(1)
		return newService(dir, id, c)
	}
	if err := restored.restoreActive(); err != nil {
		t.Fatal(err)
	}
	if assemblies.Load() != 0 || restored.index.Activations[len(restored.index.Activations)-1].Status != "failed" {
		t.Fatal("auto-replayed interrupted activation")
	}
	svc := activateOK(t, restored, a, "explicit")
	state, _ := svc.store.LoadRoom()
	if state.LatestSeq != 1 {
		t.Fatal(state)
	}
}

func TestManagedRecoveryKeepsUnsafeAndExhaustedMembersPaused(t *testing.T) {
	for _, kind := range []string{"healthy", "manual-pause", "failed", "interrupted", "running", "budget", "blocked"} {
		t.Run(kind, func(t *testing.T) {
			m := activationTestManager(t)
			room := managerCreate(t, m, "seeded")
			store, err := OpenStore(m.roomDir(room.RoomID), room.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, _ := store.LoadRoom()
			snapshot.LatestSeq, snapshot.Revision = 1, 1
			snapshot.Messages = []RoomMessage{{Seq: 1, Author: Author{Kind: "human", ID: "operator"}, ClientMessageID: "old", Text: "old", CreatedAt: time.Now().UTC()}}
			if err := store.SaveRoom(snapshot); err != nil {
				t.Fatal(err)
			}
			for _, member := range snapshot.Members {
				id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
				status := "completed"
				if kind == "failed" || kind == "interrupted" || kind == "running" {
					status = kind
				}
				state := AgentSnapshot{Version: formatVersion, MemberIdentity: id, LastOfferedUnreadSeq: 1, TurnNumber: 1, Turn: &TurnRecord{Number: 1, Status: status, SnapshotSeq: 1, OfferedUnreadSeq: 1}}
				if err := store.SaveAgent(state); err != nil {
					t.Fatal(err)
				}
				used := uint64(1)
				if kind == "budget" {
					used = 7
				}
				if err := store.saveScheduling(memberScheduling{Version: formatVersion, MemberIdentity: id, Enabled: kind != "manual-pause", Used: used}); err != nil {
					t.Fatal(err)
				}
				opts := adapterOptions(store, id)
				store.mu.Lock()
				err = store.save(opts.StatePath, dshReference{Version: formatVersion, MemberIdentity: id, Directory: "session.json.dsh"}, maxPrivateFileBytes)
				store.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				dir := dshStateDir(opts.StatePath)
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				data, _ := json.Marshal(map[string]any{"version": 1, "sessionId": id.MembershipID, "configSHA256": "controlled", "stopped": kind != "blocked"})
				if err := os.WriteFile(filepath.Join(dir, "backend.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			store.Close()
			svc := activateOK(t, m, room, "activate")
			list, err := svc.members(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, member := range list.Members {
				status := member.Execution
				if status.Active || status.Paused != (kind != "healthy") {
					t.Fatal(kind, status)
				}
				if kind == "budget" && status.Status != "budget_exhausted" {
					t.Fatal(status)
				}
				if kind == "running" && status.Agent.Turn.Status != "interrupted" {
					t.Fatal(status)
				}
				if kind == "blocked" && status.Status != "backend_blocked" {
					t.Fatal(status)
				}
			}
		})
	}
}

func TestManagedActivationCommitUnknownRequiresReopen(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before replace", true: "after replace"}[after], func(t *testing.T) {
			m := activationTestManager(t)
			room := managerCreate(t, m, "a")
			rename := m.store.ops.rename
			m.store.ops.rename = func(from, to string) error {
				if after {
					if err := rename(from, to); err != nil {
						return err
					}
				}
				return errors.New("lost activation reservation result")
			}
			r := managerHTTP(m, "/v1/rooms/activate", activationRequest{ServiceEpoch: m.epoch, Caller: humanCaller, RoomIdentity: room.RoomIdentity, ActivateID: "uncertain"})
			if r.Code != 500 || m.active != nil || m.busy {
				t.Fatal(r.Code, r.Body.String())
			}
			if r := managerHTTP(m, "/v1/rooms", nil); !strings.Contains(r.Body.String(), "write_error") {
				t.Fatal(r.Body.String())
			}
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			restored, err := newRoomManager(m.store.dir, 7)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.close()
			restored.config = m.config
			if err := restored.restoreActive(); err != nil {
				t.Fatal(err)
			}
			if restored.active != nil {
				t.Fatal("auto continued uncertain selection")
			}
			if after {
				if op := beginActivation(t, restored, room, "uncertain"); op.Status != "failed" {
					t.Fatal(op)
				}
			} else {
				activateOK(t, restored, room, "uncertain")
			}
		})
	}
}

func TestManagedCrossRoomActivationCannotBypassUnsettledBackend(t *testing.T) {
	m := activationTestManager(t)
	a, b := managerCreate(t, m, "a"), managerCreate(t, m, "b")
	activateOK(t, m, a, "a")
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	// Emulate a process exit after a target acquired resources, before it could
	// replace the last successful selection. That target must remain the owner.
	index := m.index
	index.OwnedRoomID = b.RoomID
	index.Activations = append(index.Activations, roomActivation{RoomIdentity: b.RoomIdentity, ActivateID: "crashed", Status: "running", Phase: "resuming"})
	data, _ := json.Marshal(index)
	if err := os.WriteFile(m.indexPath(), data, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(m.roomDir(b.RoomID), b.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := store.LoadRoom()
	member := state.Members[0]
	id := MemberIdentity{RoomIdentity: b.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	opts := adapterOptions(store, id)
	store.mu.Lock()
	err = store.save(opts.StatePath, dshReference{Version: formatVersion, MemberIdentity: id, Directory: "session.json.dsh"}, maxPrivateFileBytes)
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	backendDir := dshStateDir(opts.StatePath)
	if err := os.MkdirAll(backendDir, 0700); err != nil {
		t.Fatal(err)
	}
	// No normal-close or settlement record exists, even though the lock is free.
	store.Close()
	restored, err := newRoomManager(m.store.dir, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.close()
	restored.config = m.config
	beginActivation(t, restored, a, "cannot-bypass-b")
	if op := awaitActivation(t, restored); op.Status != "failed" || !strings.Contains(op.Error, "settlement") {
		t.Fatal(op)
	}
	if restored.active != nil {
		t.Fatal("started a different Room without settlement")
	}
	// A verified terminal cleanup can authorize an explicit new switch; it does
	// not authorize resuming the unhealthy original Session in B.
	if err := os.WriteFile(filepath.Join(backendDir, "settled.json"), []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	activateOK(t, restored, a, "settled-explicit-retry")
}
