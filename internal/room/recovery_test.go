package room

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/actorx"
)

func TestAttachPreservesCommittedMembership(t *testing.T) {
	state := historyRoom(3, "body")
	state.Members[0].ReadThrough = 1
	h := newRoomHarness(t, state, nil)
	id := h.memberID("A", "generation-A")
	attach := AttachMember{MemberIdentity: id, Target: h.target}
	for range 2 {
		member := roomCall[MemberInfo](t, h, attach)
		if !member.Attached || member.ReadThrough != 1 || member.UnreadCount != 2 {
			t.Fatal(member)
		}
	}
	got, err := h.store.LoadRoom()
	if err != nil || !reflect.DeepEqual(got, state) {
		t.Fatal("reattachment changed durable Room state", got, err)
	}
	other, err := h.rt.Spawn(notificationSink{})
	if err != nil {
		t.Fatal(err)
	}
	attach.Target = other
	roomError[MemberInfo](t, h, attach, ErrConflict)
	attach.MembershipID = "old-generation"
	roomError[MemberInfo](t, h, attach, ErrIdentity)
	attach.MemberIdentity, attach.Target = id, actorx.Addr{}
	roomError[MemberInfo](t, h, attach, ErrRequest)
}

func recoveryFixture(t *testing.T) (string, MemberIdentity) {
	t.Helper()
	dir := t.TempDir()
	store := openTestStore(t, dir)
	state := historyRoom(3, "body")
	state.Members[0].Runner = RunnerConfig{Kind: "dsh", Profile: "fixture-" + "fail,ignore"}
	if err := store.SaveRoom(state); err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: state.RoomIdentity, AgentID: "A", MembershipID: "generation-A"}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, id
}

func closeService(t *testing.T, svc *service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.close(ctx); err != nil {
		t.Error(err)
	}
}

func recoveryConfig() serviceConfig {
	return serviceConfig{endpoint: "http://127.0.0.1:1", cliPath: os.Args[0]}
}

func TestRecoveryMissingPrivateStateAndOrphans(t *testing.T) {
	dir, id := recoveryFixture(t)
	orphan := filepath.Join(dir, "agents", "removed-generation")
	if err := os.MkdirAll(orphan, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "agent.json"), []byte("corrupt orphan ignored"), 0600); err != nil {
		t.Fatal(err)
	}
	svc, err := newFixtureService(dir, id.RoomID, recoveryConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	status := inspectMember(t, context.Background(), svc.target(id.MembershipID), id)
	if !status.Paused || status.Agent.TurnNumber != 0 || svc.recovered[0].notice == "" {
		t.Fatal(status, svc.recovered)
	}
	if _, err := svc.store.LoadAgent(id); err != nil {
		t.Fatal("missing checkpoint was not initialized", err)
	}
	if _, err := svc.store.loadVirtualSession(id); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Session should open lazily", err)
	}
	list, err := svc.members(context.Background())
	if err != nil || len(list.Members) != 1 || !list.Members[0].Attached || list.Members[0].ReadThrough != 0 {
		t.Fatal(list, err)
	}
}

func TestRecoveryRejectsConflictingPrivateFiles(t *testing.T) {
	for _, file := range []string{"agent.json", "session.json"} {
		for _, fault := range []string{"corrupt", "identity", "version", "progress"} {
			t.Run(file+"/"+fault, func(t *testing.T) {
				dir, id := recoveryFixture(t)
				var value any
				if file == "agent.json" {
					v := AgentSnapshot{Version: formatVersion, MemberIdentity: id}
					switch fault {
					case "identity":
						v.MembershipID = "other-generation"
					case "version":
						v.Version++
					case "progress":
						v.LastOfferedUnreadSeq, v.TurnNumber = 4, 1
						v.Turn = &TurnRecord{Number: 1, Status: "running", SnapshotSeq: 4, OfferedUnreadSeq: 4}
					}
					value = v
				} else {
					v := virtualSnapshot{Version: formatVersion, MemberIdentity: id, SessionID: "private-session"}
					switch fault {
					case "identity":
						v.RoomInstance = "other-room"
					case "version":
						v.Version++
					case "progress":
						v.ProcessedThrough = 4
					}
					value = v
				}
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if fault == "corrupt" {
					data = []byte("{broken")
				}
				path := filepath.Join(dir, "agents", id.MembershipID, file)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				if svc, err := newFixtureService(dir, id.RoomID, recoveryConfig()); err == nil {
					closeService(t, svc)
					t.Fatal("conflicting state accepted")
				}
				store, err := OpenStore(dir, id.RoomID)
				if err != nil {
					t.Fatal("failed startup retained the lock", err)
				}
				defer store.Close()
				room, err := store.LoadRoom()
				if err != nil || room.LatestSeq != 3 || len(room.Members) != 1 {
					t.Fatal("failed recovery reset Room", room, err)
				}
			})
		}
	}
}

// The parent kills this test process at an explicit durable boundary. No
// timing guesses or production crash flags are needed.
func TestRecoveryCrashHelper(t *testing.T) {
	mode, dir := os.Getenv("TEAMX_CRASH_MODE"), os.Getenv("TEAMX_CRASH_DIR")
	if mode == "" {
		return
	}
	store, err := OpenStore(dir, "design-review")
	if err != nil {
		t.Fatal(err)
	}
	if mode == "commit" {
		if err := store.SaveRoom(emptyRoom()); err != nil {
			t.Fatal(err)
		}
	}
	r, err := NewRoom(store)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := actorx.NewRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mode == "commit" {
		r.save = func(state RoomSnapshot) error {
			if err := store.SaveRoom(state); err != nil {
				return err
			}
			fmt.Println("durable boundary")
			select {} // Never return to the commit callback or reply.
		}
	}
	addr, err := rt.Spawn(r)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "commit" {
		_, err = addr.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: r.state.RoomIdentity,
			Caller: humanCaller, ClientMessageID: "lost-reply", Text: "committed before reply"})
		t.Fatal("commit unexpectedly returned", err)
	}
	id := MemberIdentity{RoomIdentity: r.state.RoomIdentity, AgentID: "A", MembershipID: "generation-A"}
	scheduler, err := rt.Spawn(&turnScheduler{RoomIdentity: r.state.RoomIdentity})
	if err != nil {
		t.Fatal(err)
	}
	member, err := spawnScheduledMember(rt, &MemberAgent{Options: SessionOptions{MemberIdentity: id}, Store: store, Room: addr,
		Backend: crashTurnBackend{store: store, room: addr}}, scheduler)
	if err != nil {
		t.Fatal(err)
	}
	_, err = member.Call[MemberStatus](context.Background(), ControlMember{MemberIdentity: id, Action: "run",
		State: UnreadChanged{MemberIdentity: id, Revision: 3, LatestSeq: 3, NewestUnreadSeq: 3, UnreadCount: 3}})
	if err != nil {
		t.Fatal(err)
	}
	select {}
}

type crashTurnBackend struct {
	store *Store
	room  actorx.Addr
}

func (b crashTurnBackend) OpenSession(_ context.Context, opts SessionOptions) (Session, error) {
	return &crashTurnSession{crashTurnBackend: b, id: opts.MemberIdentity}, nil
}

type crashTurnSession struct {
	crashTurnBackend
	id MemberIdentity
}

func (s *crashTurnSession) Run(ctx context.Context, input UnreadChanged) error {
	if err := s.store.saveVirtualSession(virtualSnapshot{Version: formatVersion, MemberIdentity: s.id,
		SessionID: "crashed-private-session", Turns: 1, ScriptPosition: 1, LastInput: &input}); err != nil {
		return err
	}
	page, err := s.room.Call[ReadPage](ctx, ReadMessages{RoomIdentity: s.id.RoomIdentity,
		Caller: agentCaller(s.id.AgentID, s.id.MembershipID), Mode: ReadUnread, Limit: 1})
	if err != nil {
		return err
	}
	if _, err := s.room.Call[ReadConfirmation](ctx, ConfirmRead{MemberIdentity: s.id, ReadReceipt: page.ReadReceipt}); err != nil {
		return err
	}
	if _, err := s.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: s.id.RoomIdentity,
		Caller: agentCaller(s.id.AgentID, s.id.MembershipID), ClientMessageID: "effect-before-crash", Text: "effect persists"}); err != nil {
		return err
	}
	fmt.Println("durable boundary")
	<-ctx.Done()
	return ctx.Err()
}

func (*crashTurnSession) Close(context.Context) error { return nil }

func crashAtBoundary(t *testing.T, ctx context.Context, dir, mode string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRecoveryCrashHelper$")
	cmd.Env = append(cleanRoomEnv(), "TEAMX_CRASH_MODE="+mode, "TEAMX_CRASH_DIR="+dir)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	scanner := bufio.NewScanner(pipe)
	if !scanner.Scan() || scanner.Text() != "durable boundary" {
		t.Fatal("crash barrier not reached", scanner.Text(), scanner.Err())
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("helper was not killed")
	}
}

func TestRecoveryAfterCommitBeforeReply(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dir := t.TempDir()
	crashAtBoundary(t, ctx, dir, "commit")
	svc, err := newFixtureService(dir, "design-review", serviceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	req := PostMessage{RoomIdentity: svc.info.RoomIdentity, Caller: humanCaller,
		ClientMessageID: "lost-reply", Text: "committed before reply"}
	receipt, err := svc.room.Call[PostReceipt](ctx, req)
	if err != nil || receipt.Seq != 1 {
		t.Fatal(receipt, err)
	}
	req.ClientMessageID, req.Text = "next", "next message"
	receipt, err = svc.room.Call[PostReceipt](ctx, req)
	if err != nil || receipt.Seq != 2 {
		t.Fatal(receipt, err)
	}
}

func TestRecoveryInterruptedTurnRequiresExplicitAction(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprintf("retry=%v", retry), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			dir, id := recoveryFixture(t)
			crashAtBoundary(t, ctx, dir, "turn")
			for restart := range 2 {
				svc, err := newFixtureService(dir, id.RoomID, recoveryConfig())
				if err != nil {
					t.Fatal(err)
				}
				if err := svc.resumeMembers(ctx); err != nil {
					t.Fatal(err)
				}
				status := inspectMember(t, ctx, svc.target(id.MembershipID), id)
				checkpoint, err := svc.store.LoadAgent(id)
				if err != nil || checkpoint.Turn.Status != "interrupted" || checkpoint.Turn.StartReadThrough != 0 ||
					!status.Paused || status.Active || status.Agent.TurnNumber != 1 {
					t.Fatal(status, checkpoint, err)
				}
				room, err := svc.store.LoadRoom()
				if err != nil || room.LatestSeq != 4 || room.Members[0].ReadThrough != 1 || room.Messages[3].Text != "effect persists" {
					t.Fatal(room, err)
				}
				if restart == 0 {
					closeService(t, svc) // Paused interruption survives another restart.
					continue
				}
				defer closeService(t, svc)
				if !retry {
					_, err := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller,
						ClientMessageID: "new-input", Text: "new input waits for explicit continue"})
					if err != nil {
						t.Fatal(err)
					}
					if status := inspectMember(t, ctx, svc.target(id.MembershipID), id); !status.Paused || status.Active {
						t.Fatal(status)
					}
				}
				if _, err := svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID, Retry: retry}, "run"); err != nil {
					t.Fatal(err)
				}
				settled := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool {
					return !s.Active && s.Agent.TurnNumber == 2
				})
				if settled.Agent.Turn.Status != "completed" {
					t.Fatal("reset or replayed first failing script action", settled)
				}
				private, err := svc.store.loadVirtualSession(id)
				if err != nil || private.SessionID != "crashed-private-session" || private.Turns != 2 || private.ScriptPosition != 2 {
					t.Fatal(private, err)
				}
			}
		})
	}
}

func runningControlledService(t *testing.T) (*service, *controlledBackend, MemberIdentity, context.Context) {
	t.Helper()
	svc, err := newFixtureService(t.TempDir(), "design-review", serviceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	b := newControlledBackend()
	b.settle = make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-b.settle:
		default:
			close(b.settle)
		}
		closeService(t, svc)
	})
	id := MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: "A", MembershipID: "generation-A"}
	target, err := spawnScheduledMember(svc.rt, &MemberAgent{Options: SessionOptions{MemberIdentity: id}, Store: svc.store, Room: svc.room, Backend: b}, svc.scheduler)
	if err != nil {
		t.Fatal(err)
	}
	svc.setTarget(id.MembershipID, target)
	if _, err := svc.room.Call[MemberInfo](ctx, AddMember{MemberIdentity: id, Target: target, Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-" + "ignore"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: "one", Text: "body"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return svc, b, id, ctx
}

func TestServiceCloseWaitsForMemberSettlementAndRetainsLock(t *testing.T) {
	svc, b, id, ctx := runningControlledService(t)
	closeCtx, stopWaiting := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- svc.close(closeCtx) }()
	select {
	case <-b.canceled:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	stopWaiting()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "store still owned") {
		t.Fatal(err)
	}
	if store, err := OpenStore(svc.store.dir, id.RoomID); !errors.Is(err, ErrLocked) {
		if store != nil {
			_ = store.Close()
		}
		t.Fatal("unfinished execution lost directory ownership", err)
	}
	close(b.settle)
	if err := svc.close(ctx); err != nil || b.closes.Load() != 1 {
		t.Fatal(err, b.closes.Load())
	}
	if err := svc.close(ctx); err != nil {
		t.Fatal("close not idempotent", err)
	}
	store, err := OpenStore(svc.store.dir, id.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	checkpoint, err := store.LoadAgent(id)
	if err != nil || checkpoint.Turn.Status != "interrupted" {
		t.Fatal(checkpoint, err)
	}
}

func TestServiceRemovalWaitsForActiveMember(t *testing.T) {
	svc, b, id, ctx := runningControlledService(t)
	done := make(chan error, 1)
	go func() {
		_, err := svc.remove(apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID})
		done <- err
	}()
	select {
	case <-b.canceled:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	list, err := svc.members(ctx)
	if err != nil || len(list.Members) != 0 || b.closes.Load() != 0 {
		t.Fatal(list, err, b.closes.Load())
	}
	select {
	case err := <-done:
		t.Fatal("removal returned before backend settlement", err)
	default:
	}
	if _, err := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity,
		Caller: agentCaller(id.AgentID, id.MembershipID), ClientMessageID: "late-tool", Text: "late tool"}); !errors.Is(err, ErrIdentity) {
		t.Fatal("removed tool identity remained valid", err)
	}
	close(b.settle)
	if err := <-done; err != nil || b.closes.Load() != 1 {
		t.Fatal(err, b.closes.Load())
	}
	if !svc.target(id.MembershipID).IsZero() {
		t.Fatal("settled target retained")
	}
	// The worker's terminal save cannot recreate a deleted membership.
	if _, err := svc.store.LoadAgent(id); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
}

func TestFixtureRecoveryCheckpointFailureAndMissingCompletedSession(t *testing.T) {
	for _, missingSession := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing-completed-session=%v", missingSession), func(t *testing.T) {
			dir, id := recoveryFixture(t)
			store := openTestStore(t, dir)
			state, err := store.LoadRoom()
			if err != nil {
				t.Fatal(err)
			}
			if missingSession {
				if err := store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1,
					LastOfferedUnreadSeq: 3, Turn: &TurnRecord{Number: 1, Status: "completed", SnapshotSeq: 3, OfferedUnreadSeq: 3}}); err != nil {
					t.Fatal(err)
				}
			} else {
				store.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, errors.New("recovery disk failure") }
			}
			s := &service{store: store, endpoint: recoveryConfig().endpoint, cliPath: recoveryConfig().cliPath}
			if err := s.loadFixtureMembers(state); err == nil {
				t.Fatal("incomplete recovery accepted")
			}
			got, err := store.LoadRoom()
			if err != nil || !reflect.DeepEqual(got, state) {
				t.Fatal("recovery failure changed Room", got, err)
			}
		})
	}
}

type cleanupFailureBackend struct{ *controlledBackend }

var errCleanupTest = errors.New("test Session cleanup failed")

func (b cleanupFailureBackend) OpenSession(ctx context.Context, opts SessionOptions) (Session, error) {
	session, err := b.controlledBackend.OpenSession(ctx, opts)
	return cleanupFailureSession{Session: session}, err
}

type cleanupFailureSession struct{ Session }

func (s cleanupFailureSession) Close(ctx context.Context) error {
	return errors.Join(s.Session.Close(ctx), errCleanupTest)
}

func TestDiscardCleanupFailureIsRetainedInMemberExit(t *testing.T) {
	b := newControlledBackend()
	h, addr, id := memberHarness(t, cleanupFailureBackend{b}, nil)
	input := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
	controlMember(t, h, addr, id, "run", input, false, false)
	awaitTurn(t, h, b)
	if err := addr.Stop(nil); err != nil {
		t.Fatal(err)
	}
	exit, err := addr.Wait(h.ctx)
	if err != nil || !errors.Is(exit.Reason, errCleanupTest) || b.closes.Load() != 1 {
		t.Fatal("discard cleanup failure was hidden or cleanup ran twice", exit, err, b.closes.Load())
	}
}

func TestRecoveryRejectsLegacyRunnersWithoutChangingData(t *testing.T) {
	for _, kind := range []string{"manual", "scripted"} {
		t.Run(kind, func(t *testing.T) {
			dir, id := recoveryFixture(t)
			path := filepath.Join(dir, "room.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = bytes.Replace(data, []byte(`"kind":"dsh"`), []byte(`"kind":"`+kind+`"`), 1)
			if kind == "scripted" {
				data = bytes.Replace(data, []byte(`"profile":"fixture-fail,ignore"`), []byte(`"script":"fail,ignore"`), 1)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if svc, err := newService(dir, id.RoomID, recoveryConfig()); err == nil {
				closeService(t, svc)
				t.Fatal("legacy runner restored")
			} else if !strings.Contains(err.Error(), "unsupported runner") && !strings.Contains(err.Error(), "unknown field") {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, after) {
				t.Fatal("legacy data changed", err)
			}
		})
	}
}
