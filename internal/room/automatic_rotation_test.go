package room

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
)

func automaticHarness(t *testing.T, threshold uint64, closeFn func(context.Context) error, configure ...func(*dshBackend)) (*roomHarness, *service, actorx.Addr, MemberIdentity, *atomic.Int32) {
	t.Helper()
	h := newRoomHarness(t, historyRoom(0, ""), nil)
	id := h.memberID("A", "generation-A")
	opens := &atomic.Int32{}
	backend := dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
		opens.Add(1)
		return &adapterComponent{run: func(context.Context, string) (dsh.TurnResult, error) {
			return dsh.TurnResult{StopReason: "end_turn"}, nil
		}, close: closeFn}, nil
	}}
	for _, option := range configure {
		option(&backend)
	}
	svc := &service{ctx: h.ctx, store: h.store, info: ServiceInfo{RoomIdentity: h.id}, room: h.addr, targets: map[string]actorx.Addr{}, dshProfiles: map[string]dshProfile{"reviewer": {}}}
	list := roomCall[MemberList](t, h, ListMembers{h.id})
	svc.dshProfiles[list.Members[0].Runner.Profile] = dshProfile{}
	addr, err := h.spawn(&MemberAgent{Options: adapterOptions(h.store, id), Store: h.store, Room: h.addr, Backend: backend, SessionMaxTurns: threshold, RequestRotation: svc.requestMemberRotation})
	if err != nil {
		t.Fatal(err)
	}
	svc.targets[id.MembershipID] = addr
	return h, svc, addr, id, opens
}
func automaticPost(t *testing.T, h *roomHarness, addr actorx.Addr, id MemberIdentity, n int) {
	t.Helper()
	roomCall[PostReceipt](t, h, h.post(humanCaller, fmt.Sprint(n), fmt.Sprintf("input %d", n)))
	list := roomCall[MemberList](t, h, ListMembers{h.id})
	for _, m := range list.Members {
		if m.MembershipID == id.MembershipID {
			if err := addr.Send(h.ctx, UnreadChanged{MemberIdentity: id, Revision: list.Revision, LatestSeq: list.LatestSeq, NewestUnreadSeq: m.NewestUnreadSeq, UnreadCount: m.UnreadCount}); err != nil {
				t.Fatal(err)
			}
		}
	}
}
func TestAutomaticRotationTwoCyclesAndNoInput(t *testing.T) {
	h, _, addr, id, opens := automaticHarness(t, 2, nil)
	state := UnreadChanged{MemberIdentity: id}
	controlMember(t, h, addr, id, "run", state, false, false)
	generations := map[string]bool{}
	for n := 1; n <= 5; n++ {
		automaticPost(t, h, addr, id, n)
		status := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == uint64(n) })
		if status.Paused || status.TurnBudget != nil || status.SessionTurns.Total != uint64(n) || status.SessionTurns.Used != uint64((n-1)%2+1) {
			t.Fatalf("turn %d: %+v", n, status)
		}
		generations[status.SessionGeneration] = true
		if n == 2 || n == 4 {
			generation := status.SessionGeneration
			time.Sleep(120 * time.Millisecond)
			idle := inspectMember(t, h.ctx, addr, id)
			if idle.SessionGeneration != generation || idle.Active || idle.Agent.TurnNumber != uint64(n) {
				t.Fatal("idle rotated or ran", idle)
			}
		}
	}
	if len(generations) != 3 || opens.Load() != 3 {
		t.Fatal("did not cross two thresholds", generations, opens.Load())
	}
	saved, err := h.store.loadScheduling(id)
	if err != nil || !saved.Enabled || saved.Used != 5 || saved.SessionStartUsed != 4 || saved.Limit != 0 {
		t.Fatal(saved, err)
	}
	status := inspectMember(t, h.ctx, addr, id)
	if status.Agent.LastOfferedUnreadSeq != 5 || status.Rotation.Reason != "session_max_turns" || status.Rotation.BaseUsed != 4 {
		t.Fatal(status)
	}
	snapshot, _ := h.store.LoadRoom()
	if len(snapshot.Messages) != 5 || snapshot.Members[0].ReadThrough != 0 {
		t.Fatal("rotation changed history/cursor", snapshot)
	}
	controlMember(t, h, addr, id, "pause", UnreadChanged{}, false, false)
	automaticPost(t, h, addr, id, 6)
	time.Sleep(100 * time.Millisecond)
	if s := inspectMember(t, h.ctx, addr, id); s.Agent.TurnNumber != 5 || !s.Paused {
		t.Fatal("pause did not hold", s)
	}
}
func TestAutomaticRotationPauseDuringCleanupAndLifecycleAdmission(t *testing.T) {
	closing, release := make(chan struct{}, 1), make(chan struct{})
	h, svc, addr, id, opens := automaticHarness(t, 1, func(context.Context) error { closing <- struct{}{}; <-release; return nil })
	// Release cleanup even on an assertion failure.
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	controlMember(t, h, addr, id, "run", UnreadChanged{MemberIdentity: id}, false, false)
	automaticPost(t, h, addr, id, 1)
	waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
	svc.management.Lock()
	automaticPost(t, h, addr, id, 2)
	time.Sleep(80 * time.Millisecond)
	if opens.Load() != 1 {
		t.Fatal("bypassed lifecycle admission")
	}
	svc.management.Unlock()
	select {
	case <-closing:
	case <-h.ctx.Done():
		t.Fatal("cleanup not entered")
	}
	if svc.management.TryLock() {
		svc.management.Unlock()
		t.Fatal("automatic rotation lost lifecycle lease")
	}
	controlMember(t, h, addr, id, "pause", UnreadChanged{}, false, false)
	close(release)
	status := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return s.Rotation != nil && s.Rotation.Status == "completed" })
	time.Sleep(80 * time.Millisecond)
	if !status.Paused || status.SessionTurns.Total != 1 || opens.Load() != 1 {
		t.Fatal("late callback overwrote pause", status)
	}
	saved, _ := h.store.loadScheduling(id)
	if saved.Enabled || saved.SessionStartUsed != 1 {
		t.Fatal(saved)
	}
	if _, err := svc.control(h.ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID, FromGeneration: "session.json.dsh", Resume: true, automatic: true, RotationReason: "session_max_turns"}, "rotate"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale automatic request accepted", err)
	}
}
func TestAutomaticRotationSchedulingConversionRequiresFirstRotation(t *testing.T) {
	h := newRoomHarness(t, historyRoom(0, ""), nil)
	id := h.memberID("A", "generation-A")
	old := memberScheduling{Version: formatVersion, MemberIdentity: id, Enabled: false, Used: 19, Limit: 20}
	if err := h.store.saveScheduling(old); err != nil {
		t.Fatal(err)
	}
	addr, err := h.spawn(&MemberAgent{Options: adapterOptions(h.store, id), Store: h.store, Room: h.addr, Backend: dshBackend{store: h.store}, SessionMaxTurns: 2})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := h.store.loadScheduling(id)
	if err != nil || saved.Version != schedulingVersion || saved.Used != 19 || saved.SessionGeneration != "" || saved.Enabled || saved.Limit != 0 {
		t.Fatal(saved, err)
	}
	s := inspectMember(t, h.ctx, addr, id)
	if s.SessionResetRequired || !s.Paused {
		t.Fatal("legacy baseline confused with an abnormal backend", s)
	}
}
func TestAutomaticRotationConfigContract(t *testing.T) {
	for _, text := range []string{"[server]\nmax_turns = 2\n", "[server]\nmax_turns = 2\nsession_max_turns = 3\n"} {
		_, err := loadStartupConfig(writeStartupConfig(t, t.TempDir(), text), true)
		if err == nil || !strings.Contains(err.Error(), "server.session_max_turns") {
			t.Fatal("legacy config silently accepted", err)
		}
	}
	var out, diagnostic bytes.Buffer
	if err := serve([]string{"--max-turns", "2"}, &out, &diagnostic); err == nil || !strings.Contains(err.Error(), "--session-max-turns") {
		t.Fatal(err)
	}
	path := writeStartupConfig(t, t.TempDir(), "[server]\nsession_max_turns = 0\n")
	if err := serve([]string{"--config", path}, &out, &diagnostic); err == nil || !strings.Contains(err.Error(), "positive") {
		t.Fatal(err)
	}
}
func TestAutomaticRotationManagedConfigConversionIsDurable(t *testing.T) {
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	room := managerCreate(t, m, "s3-old-config")
	path := filepath.Join(m.roomDir(room.RoomID), "room-config.json")
	var c managedRoomConfig
	if err := readSnapshot(path, &c, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	c.Version, c.MaxTurns, c.SessionMaxTurns = formatVersion, 7, 0
	if err := m.store.save(path, c, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	if err := m.upgradeSessionThresholds(); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c = managedRoomConfig{}
	if err := readSnapshot(path, &c, maxPrivateFileBytes); err != nil || c.Version != managedConfigVersion || c.SessionMaxTurns != 7 || c.MaxTurns != 0 {
		t.Fatal(c, err)
	}
	m.maxTurns = 99
	if err := m.upgradeSessionThresholds(); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if !bytes.Equal(first, second) {
		t.Fatal("converted threshold changed on a later startup")
	}
}

func TestAutomaticRotationOpeningFailureDoesNotRotateAgain(t *testing.T) {
	var attempts atomic.Int32
	h, _, addr, id, _ := automaticHarness(t, 1, nil, func(b *dshBackend) {
		open := b.open
		b.open = func(ctx context.Context, opts dsh.Options) (dshComponent, error) {
			if attempts.Add(1) > 1 {
				return nil, dsh.ErrUncertain
			}
			return open(ctx, opts)
		}
	})
	controlMember(t, h, addr, id, "run", UnreadChanged{MemberIdentity: id}, false, false)
	automaticPost(t, h, addr, id, 1)
	waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
	automaticPost(t, h, addr, id, 2)
	failed := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
	if !failed.Paused || failed.SessionTurns.Total != 2 || failed.SessionStartUsed != 1 || failed.Agent.Turn.Status != "failed" {
		t.Fatal(failed)
	}
	automaticPost(t, h, addr, id, 3)
	time.Sleep(150 * time.Millisecond)
	status := inspectMember(t, h.ctx, addr, id)
	if attempts.Load() != 2 || status.SessionGeneration != failed.SessionGeneration || status.Agent.TurnNumber != 2 {
		t.Fatal("opening failure automatically retried or replaced Session", status)
	}
}

func TestAutomaticRotationDoesNotReofferRoomOrInboxOnCompletion(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	svc := referenceTestService(h)
	svc.targets = map[string]actorx.Addr{}
	svc.ctx = h.ctx
	svc.dshProfiles = map[string]dshProfile{"fixture-ignore": {}}
	// Keep both Room and Inbox unconsumed. LastOffered still prevents replay.
	roomCall[PostReceipt](t, h.roomHarness, h.post(humanCaller, "shared", "shared input"))
	inboxCall[InboxReceipt](t, h, h.receive("one"))
	b := dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
		return &adapterComponent{run: func(context.Context, string) (dsh.TurnResult, error) {
			return dsh.TurnResult{StopReason: "end_turn"}, nil
		}}, nil
	}}
	member := &MemberAgent{Options: adapterOptions(h.store, h.id), Store: h.store, Room: h.roomHarness.addr, Backend: b, SessionMaxTurns: 1, RequestRotation: svc.requestMemberRotation, Inbox: func(ctx context.Context) (InboxStatus, error) {
		return h.addr.Call[InboxStatus](ctx, InspectInbox{h.id})
	}}
	addr, err := h.spawn(member)
	if err != nil {
		t.Fatal(err)
	}
	svc.setTarget(h.id.MembershipID, addr)
	// Use the authoritative profile name for service rotation validation.
	list := roomCall[MemberList](t, h.roomHarness, ListMembers{h.id.RoomIdentity})
	svc.dshProfiles[list.Members[0].Runner.Profile] = dshProfile{}
	waitMember(t, h.ctx, addr, h.id, func(s MemberStatus) bool { return s.Inbox != nil })
	controlMember(t, h.roomHarness, addr, h.id, "run", UnreadChanged{MemberIdentity: h.id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}, false, false)
	first := waitMember(t, h.ctx, addr, h.id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
	if first.Agent.LastOfferedInboxSeq != 1 || first.Agent.LastOfferedUnreadSeq != 1 {
		t.Fatal(first)
	}
	time.Sleep(150 * time.Millisecond)
	if s := inspectMember(t, h.ctx, addr, h.id); s.Agent.TurnNumber != 1 || s.Rotation != nil {
		t.Fatal("same input triggered automatic rotation", s)
	}
	// A fresh Room message legitimately rotates once. The old Inbox watermark
	// stays intact and is not offered in the new generation.
	automaticPost(t, h.roomHarness, addr, h.id, 2)
	second := waitMember(t, h.ctx, addr, h.id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
	if second.Agent.LastOfferedInboxSeq != 1 || second.Agent.LastOfferedInboxThrough != 0 || second.Agent.Turn.InboxSnapshotSeq != 0 || second.Inbox.ConsumedThrough != 0 || second.SessionStartUsed != 1 {
		t.Fatal(second)
	}
	time.Sleep(150 * time.Millisecond)
	if s := inspectMember(t, h.ctx, addr, h.id); s.Agent.TurnNumber != 2 {
		t.Fatal("rotation reoffered old input", s)
	}
}

func TestAutomaticRotationManagedConversionResumesAfterUncertainCommit(t *testing.T) {
	for _, boundary := range []string{"room-config.json", "rooms.json"} {
		t.Run(boundary, func(t *testing.T) {
			m := testManager(t, t.TempDir(), managerTestProfiles(t))
			room := managerCreate(t, m, "s3-convert-uncertain")
			path := filepath.Join(m.roomDir(room.RoomID), "room-config.json")
			var c managedRoomConfig
			if err := readSnapshot(path, &c, maxPrivateFileBytes); err != nil {
				t.Fatal(err)
			}
			c.Version, c.MaxTurns, c.SessionMaxTurns = formatVersion, 3, 0
			if err := m.store.save(path, c, maxPrivateFileBytes); err != nil {
				t.Fatal(err)
			}
			original := m.store.ops.rename
			var injected bool
			m.store.ops.rename = func(from, to string) error {
				if err := original(from, to); err != nil {
					return err
				}
				if filepath.Base(to) == boundary && !injected {
					injected = true
					return errors.New("replacement outcome uncertain")
				}
				return nil
			}
			if err := m.upgradeSessionThresholds(); !errors.Is(err, ErrCommitUnknown) {
				t.Fatal("uncertain conversion allowed execution", err)
			}
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			restored, err := newRoomManager(m.store.dir, 99)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.close()
			c = managedRoomConfig{}
			if err := readSnapshot(path, &c, maxPrivateFileBytes); err != nil || c.Version != managedConfigVersion || c.SessionMaxTurns != 3 || c.MaxTurns != 0 {
				t.Fatal("conversion lost frozen threshold", c, err)
			}
		})
	}
}

func TestAutomaticRotationRechecksInputAfterLifecycleContention(t *testing.T) {
	h, svc, addr, id, opens := automaticHarness(t, 1, nil)
	controlMember(t, h, addr, id, "run", UnreadChanged{MemberIdentity: id}, false, false)
	automaticPost(t, h, addr, id, 1)
	waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
	svc.management.Lock()
	automaticPost(t, h, addr, id, 2)
	time.Sleep(50 * time.Millisecond)
	caller := Caller{Author: Author{Kind: "agent", ID: id.AgentID}, MembershipID: id.MembershipID}
	page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: caller, Mode: ReadUnread})
	roomCall[ReadConfirmation](t, h, ConfirmRead{MemberIdentity: id, ReadReceipt: page.ReadReceipt})
	svc.management.Unlock()
	time.Sleep(250 * time.Millisecond)
	s := inspectMember(t, h.ctx, addr, id)
	if s.Rotation != nil || s.Agent.TurnNumber != 1 || opens.Load() != 1 || s.SessionStartUsed != 0 {
		t.Fatal("stale notification rotated an idle Session", s)
	}
}
