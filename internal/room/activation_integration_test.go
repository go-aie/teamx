//go:build integration || e2e

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
)

func TestManagedActivationHistoryIntentBudgetAndRestart(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	m.factory = controlledActivationFactory(nil, nil)
	a, b := managerCreate(t, m, "a"), managerCreate(t, m, "b")
	sa := openRoomOK(t, m, a, "enter-a")
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
	sb := openRoomOK(t, m, b, "enter-b")
	bs, _ := sb.store.LoadRoom()
	if bs.LatestSeq != 0 {
		t.Fatal("new Room copied messages")
	}
	background := apiRequest{ServiceInfo: sa.info, Caller: humanCaller, Mode: ReadAll}
	if r := managerHTTP(m, "/v1/read", background); r.Code != 200 || !strings.Contains(r.Body.String(), "a-history") {
		t.Fatal(r.Code, r.Body.String())
	}
	sa2 := openRoomOK(t, m, a, "return-a")
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
	if sa2 != sa || sa2.info.ServiceEpoch != sa.info.ServiceEpoch {
		t.Fatal("reselection replaced a healthy instance")
	}
	// Old successful receipts cannot switch away from the current Room.
	if op := beginManagedOpen(t, m, b, "enter-b"); op.Status != "completed" {
		t.Fatal(op)
	}
	if m.runtimes[a.RoomIdentity].service != sa2 {
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
	if err := restored.restoreRuntimes(); err != nil {
		t.Fatal(err)
	}
	awaitRecovery(t, restored)
	if restored.runtimes[a.RoomIdentity].service == nil {
		t.Fatal("selected enabled Room was not restored")
	}
	after, _ := restored.runtimes[a.RoomIdentity].service.store.LoadRoom()
	if !reflect.DeepEqual(before, after) || restored.runtimes[a.RoomIdentity].service.maxTurns != 7 {
		t.Fatal("restart reinitialized Room or budget")
	}
	status, _ := restored.runtimes[a.RoomIdentity].service.members(ctx)
	if !status.Members[0].Execution.Paused || status.Members[1].Execution.TurnBudget.Used != 1 {
		t.Fatal(status)
	}
}

func TestManagedRoomsRunConcurrentlyAndKeepSessions(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	entered := make(chan MemberIdentity, 16)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var closed atomic.Int32
	m.factory = controlledActivationFactory(func(id MemberIdentity, ctx context.Context) error {
		entered <- id
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, func(MemberIdentity) { closed.Add(1) })
	a, b := managerCreate(t, m, "running-a"), managerCreate(t, m, "running-b")
	sa := openRoomOK(t, m, a, "first")
	managedPost(t, m, sa, "running-a")
	var runningA MemberIdentity
	select {
	case runningA = <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("A did not start")
	}
	sbInfo := openOK(t, m, b.RoomIdentity, "open-b")
	m.mu.Lock()
	sb := m.runtimes[b.RoomIdentity].service
	m.mu.Unlock()
	managedPost(t, m, sb, "running-b")
	select {
	case id := <-entered:
		if id.RoomIdentity != b.RoomIdentity {
			t.Fatal("same Room started a second concurrent turn", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("B did not execute concurrently with A")
	}
	if got := openOK(t, m, a.RoomIdentity, "a-again"); got != sa.info || closed.Load() != 0 || sa.closing.Load() {
		t.Fatal("opening B stopped A or replaced its Session", got, closed.Load())
	}
	if got := openRoomOK(t, m, b, "select-b"); got.info != sbInfo {
		t.Fatal("legacy selection replaced open B")
	}
	// An in-flight Agent can still publish to its original Room while viewing B.
	r := managerHTTP(m, "/v1/send", apiRequest{ServiceInfo: sa.info, Caller: agentCaller(runningA.AgentID, runningA.MembershipID), ClientMessageID: "a-tool-result", Text: "a-tool-result"})
	if r.Code != 200 {
		t.Fatal("background Agent tool rejected", r.Code, r.Body.String())
	}
	if r := managerHTTP(m, "/v1/read", apiRequest{ServiceInfo: sb.info, Caller: humanCaller, Mode: ReadAll}); strings.Contains(r.Body.String(), "a-tool-result") {
		t.Fatal("A tool result crossed into B")
	}
	releaseOnce.Do(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	waitMember(t, ctx, sa.target(runningA.MembershipID), runningA, func(status MemberStatus) bool {
		return !status.Active && status.Agent.Turn != nil && status.Agent.Turn.Status == "completed"
	})
	if got := openRoomOK(t, m, a, "return-a"); got != sa || closed.Load() != 0 {
		t.Fatal("return visit replaced A's instance or Session")
	}
}

func TestManagedActivationFailureReceiptsAndInterruptedRecovery(t *testing.T) {
	m := activationTestManager(t)
	m.factory = controlledActivationFactory(nil, nil)
	a, b := managerCreate(t, m, "a"), managerCreate(t, m, "b")
	sa := openRoomOK(t, m, a, "first")
	managedPost(t, m, sa, "retained")
	t.Setenv(m.teams["takeout-dinner"].Runtime.Model.APIKeyEnv, "")
	beginManagedOpen(t, m, b, "missing-key")
	if op := awaitLatestOpen(t, m); op.Status != "failed" || !strings.Contains(op.Error, "credential") {
		t.Fatal(op)
	}
	if m.runtimes[a.RoomIdentity].service != sa || sa.closing.Load() {
		t.Fatal("preflight stopped old room")
	}
	if op := beginManagedOpen(t, m, b, "missing-key"); op.Status != "failed" {
		t.Fatal(op)
	}
	if r := managerHTTP(m, "/v1/rooms/open", roomOpenRequest{ServiceEpoch: m.epoch, Caller: humanCaller, RoomIdentity: a.RoomIdentity, OpenID: "missing-key"}); r.Code != 409 {
		t.Fatal(r.Code)
	}
	t.Setenv(m.teams["takeout-dinner"].Runtime.Model.APIKeyEnv, "offline-test-only")
	m.factory = func(string, string, serviceConfig) (*service, error) { return nil, errors.New("assembly failed") }
	beginManagedOpen(t, m, b, "assembly")
	if op := awaitLatestOpen(t, m); op.Status != "failed" {
		t.Fatal(op)
	}
	// Simulate an exit with a retained running marker. Startup records B's
	// failure while restoring A's independent collaboration intent.
	next := m.copyIndex()
	next.Opens[len(next.Opens)-1].Status = "running"
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
	if err := restored.restoreRuntimes(); err != nil {
		t.Fatal(err)
	}
	awaitRecovery(t, restored)
	if assemblies.Load() != 1 || restored.runtimes[a.RoomIdentity].status != "ready" || restored.runtimes[b.RoomIdentity].status != "failed" || restored.index.Opens[len(restored.index.Opens)-1].Status != "failed" {
		t.Fatal("auto-replayed interrupted activation")
	}
	svc := openRoomOK(t, restored, a, "explicit")
	state, _ := svc.store.LoadRoom()
	if state.LatestSeq != 1 {
		t.Fatal(state)
	}
}

func TestManagedRecoveryKeepsUnsafeAndExhaustedMembersPaused(t *testing.T) {
	parallelFixtureTest(t)
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
			svc := openRoomOK(t, m, room, "activate")
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
	parallelFixtureTest(t)
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
			r := managerHTTP(m, "/v1/rooms/open", roomOpenRequest{ServiceEpoch: m.epoch, Caller: humanCaller, RoomIdentity: room.RoomIdentity, OpenID: "uncertain"})
			if r.Code != 500 || len(m.runtimes) != 0 || m.busy {
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
			if err := restored.restoreRuntimes(); err != nil {
				t.Fatal(err)
			}
			awaitRecovery(t, restored)
			if restored.runtimes[room.RoomIdentity].service != nil {
				t.Fatal("auto continued uncertain selection")
			}
			if after {
				if op := beginManagedOpen(t, restored, room, "uncertain"); op.Status != "failed" {
					t.Fatal(op)
				}
			} else {
				openRoomOK(t, restored, room, "uncertain")
			}
		})
	}
}
