//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntimeRecoveryRestoresEnabledRoomsWithoutSelection(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	m.factory = controlledActivationFactory(nil, nil)
	a, b, untouched := managerCreate(t, m, "a"), managerCreate(t, m, "b"), managerCreate(t, m, "untouched")
	for _, room := range []roomSummary{a, b} {
		openOK(t, m, room.RoomIdentity, "open-"+room.CreateID)
		m.mu.Lock()
		svc := m.runtimes[room.RoomIdentity].service
		m.mu.Unlock()
		list, err := svc.members(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.control(context.Background(), apiRequest{AgentID: list.Members[0].AgentID, MembershipID: list.Members[0].MembershipID}, "pause"); err != nil {
			t.Fatal(err)
		}
		managedPost(t, m, svc, "old-"+room.CreateID)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		for _, member := range list.Members[1:] {
			id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
			waitMember(t, ctx, svc.target(member.MembershipID), id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
		}
		cancel()
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	restored := restoredTestManager(t, m)
	if err := restored.restoreRuntimes(); err != nil {
		t.Fatal(err)
	}
	awaitRecovery(t, restored)
	for _, room := range []roomSummary{a, b} {
		rt := restored.runtimes[room.RoomIdentity]
		if rt.status != "ready" || rt.service == nil {
			t.Fatal("enabled background Room did not recover", room, rt)
		}
		list, err := rt.service.members(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for i, member := range list.Members {
			if member.Execution == nil || member.Execution.Paused != (i == 0) || member.Execution.TurnBudget.Used != map[bool]uint64{true: 0, false: 1}[i == 0] {
				t.Fatal("pause or budget changed on recovery", member)
			}
		}
		info := rt.service.info
		if got := openOK(t, restored, room.RoomIdentity, "view-"+room.CreateID); got != info {
			t.Fatal("view replaced restored service")
		}
		managedPost(t, restored, rt.service, "new-"+room.CreateID)
	}
	if restored.runtimes[untouched.RoomIdentity].service != nil {
		t.Fatal("recovery used UI selection or assembled untouched team")
	}
	if len(restored.index.Opens) != 4 {
		t.Fatal("recovery created or replayed user receipts")
	}
}

func TestRuntimeRecoveryFailureAndSlowFactoryAreLocal(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	a, b := managerCreate(t, m, "a"), managerCreate(t, m, "b")
	m.factory = controlledActivationFactory(nil, nil)
	openOK(t, m, a.RoomIdentity, "a")
	openOK(t, m, b.RoomIdentity, "b")
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	restored := restoredTestManager(t, m)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var assemblies atomic.Int32
	restored.factory = func(dir, id string, config serviceConfig) (*service, error) {
		assemblies.Add(1)
		if id == a.RoomID {
			close(entered)
			<-release
			return nil, errors.New("injected A recovery failure")
		}
		return controlledActivationFactory(nil, nil)(dir, id, config)
	}
	if err := restored.restoreRuntimes(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("A recovery did not start")
	}
	awaitOpenRuntime(t, restored, b.RoomIdentity)
	sb := restored.runtimes[b.RoomIdentity].service
	managedPost(t, restored, sb, "healthy-b")
	if r := managerHTTP(restored, "/v1/rooms/open", roomOpenRequest{RoomIdentity: a.RoomIdentity, OpenID: "duplicate-a", ServiceEpoch: restored.epoch, Caller: humanCaller}); r.Code != 409 {
		t.Fatal("recovery did not reserve its Room", r.Code)
	}
	if err := restored.restoreRuntimes(); err != nil || assemblies.Load() != 2 {
		t.Fatal("repeated recovery duplicated assembly", err, assemblies.Load())
	}
	unblock()
	awaitRecovery(t, restored)
	if rt := restored.runtimes[a.RoomIdentity]; rt.status != "failed" || !strings.Contains(rt.err, "injected A") || rt.service != nil {
		t.Fatal("recovery lost A's diagnostic", rt)
	}
	if !hasRoomOwner(restored.index.OwnedRooms, a.RoomIdentity) || sb.closing.Load() {
		t.Fatal("failed recovery lost ownership or stopped B")
	}
}

func TestRuntimeRecoveryDoesNotReplayInterruptedOperations(t *testing.T) {
	parallelFixtureTest(t)
	for _, kind := range []string{"open", "shutdown"} {
		t.Run(kind, func(t *testing.T) {
			m := activationTestManager(t)
			a, b := managerCreate(t, m, "a"), managerCreate(t, m, "b")
			m.factory = controlledActivationFactory(nil, nil)
			openOK(t, m, a.RoomIdentity, "a")
			openOK(t, m, b.RoomIdentity, "b")
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			index := m.copyIndex()
			switch kind {
			case "open":
				index.Opens = append(index.Opens, roomOpen{RoomIdentity: a.RoomIdentity, OpenID: "interrupted", Status: "running"})
			case "shutdown":
				index.forgetShutdown(a.RoomIdentity)
				index.Shutdowns = append(index.Shutdowns, roomShutdown{RoomIdentity: a.RoomIdentity, Error: "Session cleanup unverified"})
			}
			data, _ := json.Marshal(index)
			if err := os.WriteFile(m.indexPath(), data, 0600); err != nil {
				t.Fatal(err)
			}
			restored := restoredTestManager(t, m)
			if err := restored.restoreRuntimes(); err != nil {
				t.Fatal(err)
			}
			awaitRecovery(t, restored)
			if rt := restored.runtimes[a.RoomIdentity]; rt.status != "failed" || rt.service != nil || rt.err == "" {
				t.Fatal("replayed interrupted operation", rt)
			}
			if restored.runtimes[b.RoomIdentity].status != "ready" || !hasRoomOwner(restored.index.OwnedRooms, a.RoomIdentity) {
				t.Fatal("failure blocked B or forgot A")
			}
			// Read-only startup must preserve private evidence and user receipts.
			if _, err := os.Stat(filepath.Join(restored.roomDir(a.RoomID), "room.json")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRuntimeRecoveryInterruptedTurnAndCorruptIntentDoNotAffectHealthyRoom(t *testing.T) {
	parallelFixtureTest(t)
	for _, kind := range []string{"running", "interrupted", "corrupt-intent"} {
		t.Run(kind, func(t *testing.T) {
			m := activationTestManager(t)
			a, b := managerCreate(t, m, "a"), managerCreate(t, m, "b")
			m.factory = controlledActivationFactory(nil, nil)
			openOK(t, m, a.RoomIdentity, "a")
			openOK(t, m, b.RoomIdentity, "b")
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			store, err := OpenStore(m.roomDir(a.RoomID), a.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			state, _ := store.LoadRoom()
			state.LatestSeq, state.Revision = 1, 1
			state.Messages = []RoomMessage{{Seq: 1, Author: Author{Kind: "human", ID: "operator"}, ClientMessageID: "old", Text: "old", CreatedAt: time.Now().UTC()}}
			if err := store.SaveRoom(state); err != nil {
				t.Fatal(err)
			}
			for _, member := range state.Members {
				id := MemberIdentity{RoomIdentity: a.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
				if kind == "corrupt-intent" {
					if err := os.WriteFile(filepath.Join(store.dir, "agents", id.MembershipID, "collaboration.json"), []byte("{"), 0600); err != nil {
						t.Fatal(err)
					}
					continue
				}
				agent := AgentSnapshot{Version: formatVersion, MemberIdentity: id, LastOfferedUnreadSeq: 1, TurnNumber: 1,
					Turn: &TurnRecord{Number: 1, Status: kind, SnapshotSeq: 1, OfferedUnreadSeq: 1}}
				if err := store.SaveAgent(agent); err != nil {
					t.Fatal(err)
				}
				if err := store.saveScheduling(memberScheduling{Version: formatVersion, MemberIdentity: id, Enabled: true, Used: 1}); err != nil {
					t.Fatal(err)
				}
			}
			store.Close()
			restored := restoredTestManager(t, m)
			if err := restored.restoreRuntimes(); err != nil {
				t.Fatal(err)
			}
			awaitRecovery(t, restored)
			ra, rb := restored.runtimes[a.RoomIdentity], restored.runtimes[b.RoomIdentity]
			if rb.status != "ready" {
				t.Fatal("A's evidence blocked B", rb)
			}
			managedPost(t, restored, rb.service, "healthy-b")
			if kind == "corrupt-intent" {
				if ra.status != "failed" || ra.err == "" || ra.service != nil {
					t.Fatal("invalid intent did not fail locally", ra)
				}
				return
			}
			list, err := ra.service.members(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			managedPost(t, restored, ra.service, "new-a")
			for _, member := range list.Members {
				status := member.Execution
				if status == nil || !status.Paused || status.Active || status.Agent.Turn.Status != "interrupted" || status.TurnBudget.Used != 1 {
					t.Fatal("unsafe execution was replayed or budget changed", member)
				}
			}
		})
	}
}

func TestRuntimeRecoveryLeavesSettledPausedRoomsLazy(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	m.factory = controlledActivationFactory(nil, nil)
	a, selected := managerCreate(t, m, "paused"), managerCreate(t, m, "selected")
	openOK(t, m, a.RoomIdentity, "paused")
	svc := m.runtimes[a.RoomIdentity].service
	list, err := svc.members(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range list.Members {
		if _, err := svc.control(context.Background(), apiRequest{AgentID: member.AgentID, MembershipID: member.MembershipID}, "pause"); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	restored := restoredTestManager(t, m)
	if err := restored.restoreRuntimes(); err != nil {
		t.Fatal(err)
	}
	awaitRecovery(t, restored)
	for _, rt := range restored.runtimes {
		if rt.service != nil || rt.status != "closed" {
			t.Fatal("paused team or UI default caused assembly", rt)
		}
	}
	if hasRoomOwner(restored.index.OwnedRooms, selected.RoomIdentity) {
		t.Fatal("UI selection established execution ownership")
	}
}

func TestRuntimeRecoveryExplicitRetrySupersedesOpenFailure(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	a := managerCreate(t, m, "a")
	m.factory = func(string, string, serviceConfig) (*service, error) { return nil, errors.New("old failure") }
	beginOpen(t, m, a.RoomIdentity, "failed")
	if op := awaitOpen(t, m, a.RoomIdentity, "failed"); op.Status != "failed" {
		t.Fatal(op)
	}
	m.factory = controlledActivationFactory(nil, nil)
	openRoomOK(t, m, a, "explicit-success")
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	restored := restoredTestManager(t, m)
	if err := restored.restoreRuntimes(); err != nil {
		t.Fatal(err)
	}
	awaitRecovery(t, restored)
	if rt := restored.runtimes[a.RoomIdentity]; rt.status != "ready" {
		t.Fatal(rt)
	}
}
