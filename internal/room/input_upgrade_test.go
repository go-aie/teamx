package room

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func upgradeFixtureCopy(t *testing.T) (string, MemberIdentity) {
	t.Helper()
	h := newInboxHarness(t, InboxLimits{})
	roomCall[PostReceipt](t, h.roomHarness, PostMessage{RoomIdentity: h.id.RoomIdentity, Caller: humanCaller, ClientMessageID: "request", Text: "original request"})
	receiveTestTaskEvent(t, h, taskEventFixture("saved-task"))
	item := storedGitLab(h.id, "one", "events", "7", "42", "original notification")
	inboxCall[InboxReceipt](t, h, ReceiveInbox{MemberIdentity: h.id, Item: item})
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(h.store.dir)); err != nil {
		t.Fatal(err)
	}
	return dir, h.id
}

func readUpgradeProgress(t *testing.T, dir string) inputUpgradeProgress {
	t.Helper()
	var progress inputUpgradeProgress
	if err := readSnapshot(filepath.Join(dir, inputUpgradeJournal), &progress, inputUpgradeLimit); err != nil {
		t.Fatal(err)
	}
	return progress
}

func stopInputUpgradeAt(t *testing.T, dir, room, phase string) {
	t.Helper()
	stopped := errors.New("interrupted startup")
	_, err := openStore(dir, room, &inputUpgradeOptions{stop: func(point string) error {
		if point == phase {
			return stopped
		}
		return nil
	}})
	if !errors.Is(err, stopped) {
		t.Fatalf("expected interruption at %s, got %v", phase, err)
	}
}

func TestInputUpgradeResumesEveryPublicationBoundary(t *testing.T) {
	for _, phase := range []string{"backup-file", "backup-ready", "converted", "prepared", "header", "replace-file", "remove-file", "room-published", "completed"} {
		t.Run(phase, func(t *testing.T) {
			dir, id := upgradeFixtureCopy(t)
			stopInputUpgradeAt(t, dir, id.RoomID, phase)
			before := readUpgradeProgress(t, dir)
			if phase == "header" || phase == "replace-file" || phase == "remove-file" {
				if store, err := OpenStore(dir, id.RoomID); err == nil {
					_ = store.Close()
					t.Fatal("partially upgraded Room opened without recovery")
				}
			}
			store, err := openStore(dir, id.RoomID, &inputUpgradeOptions{})
			if err != nil {
				t.Fatal(err)
			}
			state, err := store.LoadRoom()
			if err != nil || state.InputLog == nil || len(state.Messages) != 2 || len(state.EventReceipts) != 1 ||
				state.Messages[0].Text != "original request" || state.Messages[1].Event.ID != "saved-task" {
				t.Fatal(state, err)
			}
			progress := readUpgradeProgress(t, dir)
			if progress.Phase != "completed" || progress.ID != before.ID {
				t.Fatal("upgrade did not resume its original backup", progress, before.ID)
			}
			work := filepath.Join(dir, inputUpgradeDirectory)
			generations, err := os.ReadDir(work)
			if err != nil || len(generations) != 1 {
				t.Fatal(generations, err)
			}
			if _, err := os.Stat(filepath.Join(work, before.ID, "staging")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("temporary stage remains", err)
			}
			p, err := store.openPlatformInputStorage(id, defaultNotificationAdapters(nil))
			if err != nil {
				t.Fatal(err)
			}
			pending, err := p.repository.PendingLocations(nil)
			if err != nil || len(pending) != 1 || pending[0].Seq != 2 {
				t.Fatal(pending, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if phase == "completed" {
				if err := os.RemoveAll(filepath.Join(work, before.ID, "backup")); err != nil {
					t.Fatal(err)
				}
			}
			store, err = openStore(dir, id.RoomID, &inputUpgradeOptions{stop: func(point string) error {
				t.Errorf("completed upgrade ran checkpoint %s again", point)
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			after, err := store.LoadRoom()
			if err != nil || !reflect.DeepEqual(state, after) {
				t.Fatal("reopen changed target", after, err)
			}
		})
	}
}

func TestInputUpgradeRefreshesLegacySourceBeforeFence(t *testing.T) {
	dir, id := upgradeFixtureCopy(t)
	stopInputUpgradeAt(t, dir, id.RoomID, "prepared")
	before := readUpgradeProgress(t, dir)
	legacy, err := OpenStore(dir, id.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := legacy.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	state.LatestSeq++
	state.Revision++
	state.Messages = append(state.Messages, RoomMessage{Seq: state.LatestSeq, Author: humanCaller.Author, ClientMessageID: "later", Text: "written after staging", CreatedAt: time.Now().UTC()})
	if err := legacy.SaveRoom(state); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := openStore(dir, id.RoomID, &inputUpgradeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	after, err := store.LoadRoom()
	if err != nil || len(after.Messages) != 3 || after.Messages[1].Text != "written after staging" ||
		after.Messages[2].Event == nil || before.ID == readUpgradeProgress(t, dir).ID {
		t.Fatal("stale target used", after, err)
	}
}

func TestInputUpgradeFailsClosedOnInvalidAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, phase string
		path        func(string, MemberIdentity, inputUpgradeProgress) string
	}{
		{"journal", "prepared", func(dir string, _ MemberIdentity, _ inputUpgradeProgress) string {
			return filepath.Join(dir, inputUpgradeJournal)
		}},
		{"backup", "header", func(dir string, _ MemberIdentity, p inputUpgradeProgress) string {
			return filepath.Join(dir, inputUpgradeDirectory, p.ID, "backup", upgradeRoomFile)
		}},
		{"staging", "replace-file", func(dir string, _ MemberIdentity, p inputUpgradeProgress) string {
			return filepath.Join(dir, inputUpgradeDirectory, p.ID, "staging", upgradeRoomFile)
		}},
		{"published", "room-published", func(dir string, id MemberIdentity, _ inputUpgradeProgress) string {
			return filepath.Join(dir, "agents", id.MembershipID, "capacity.json")
		}},
		{"legacy", "", func(dir string, id MemberIdentity, _ inputUpgradeProgress) string {
			return filepath.Join(dir, "inboxes", id.MembershipID, inboxEntryName(2))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, id := upgradeFixtureCopy(t)
			var progress inputUpgradeProgress
			if tc.phase != "" {
				stopInputUpgradeAt(t, dir, id.RoomID, tc.phase)
				progress = readUpgradeProgress(t, dir)
			}
			path := tc.path(dir, id, progress)
			if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
				t.Fatal(err)
			}
			roomPath := filepath.Join(dir, "room.json")
			before, err := os.ReadFile(roomPath)
			if err != nil {
				t.Fatal(err)
			}
			store, err := openStore(dir, id.RoomID, &inputUpgradeOptions{})
			if err == nil {
				_ = store.Close()
				t.Fatal("corrupt data admitted")
			}
			after, err := os.ReadFile(roomPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("authoritative header was replaced", err)
			}
		})
	}
}

func TestInputUpgradeAdmissionFailures(t *testing.T) {
	dir, id := upgradeFixtureCopy(t)
	lock, err := OpenStore(dir, id.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(dir, id.RoomID, &inputUpgradeOptions{}); !errors.Is(err, ErrLocked) {
		t.Fatal("upgrade bypassed Room lock", err)
	}
	_ = lock.Close()
	roomPath := filepath.Join(dir, "room.json")
	before, err := os.ReadFile(roomPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(dir, id.RoomID, &inputUpgradeOptions{space: func(string) (uint64, error) {
		return 0, nil
	}}); err == nil || !strings.Contains(err.Error(), "磁盘空间不足") {
		t.Fatal("expected disk diagnostic", err)
	}
	if _, err := openStore(dir, id.RoomID, &inputUpgradeOptions{space: func(string) (uint64, error) {
		return 0, os.ErrPermission
	}}); !errors.Is(err, os.ErrPermission) {
		t.Fatal("expected permission diagnostic", err)
	}
	after, err := os.ReadFile(roomPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed preflight changed legacy Room", err)
	}
	progress := readUpgradeProgress(t, dir)
	if progress.Phase != "backup" {
		t.Fatal(progress)
	}
	legacy := RoomSnapshot{}
	if err := readSnapshot(roomPath, &legacy, maxRoomFileBytes); err != nil {
		t.Fatal(err)
	}
	legacy.Version = 99
	if err := (&snapshotWriter{ops: defaultSnapshotOps()}).save(roomPath, legacy, maxRoomFileBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(dir, id.RoomID, &inputUpgradeOptions{}); !errors.Is(err, ErrVersion) {
		t.Fatal("unsupported legacy schema accepted", err)
	}
}

func TestNormalServerUpgradesRoomBeforeReady(t *testing.T) {
	dir := t.TempDir()
	legacy := emptyRoom()
	legacy.LatestSeq, legacy.Revision = 1, 1
	legacy.Messages = []RoomMessage{{Seq: 1, Author: humanCaller.Author, ClientMessageID: "saved", Text: "saved before restart", CreatedAt: time.Now().UTC()}}
	store, err := OpenStore(dir, legacy.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRoom(legacy); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	config, _ := testAccessConfig(t, t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var log bytes.Buffer
	err = runServer(ctx, []string{"--config", config.path, "--listen", "127.0.0.1:0", "--room", legacy.RoomID, "--state-dir", dir}, io.Discard, &log,
		serverOptions{ready: func(any) error {
			var state RoomSnapshot
			if err := readSnapshot(filepath.Join(dir, "room.json"), &state, maxRoomFileBytes); err != nil {
				return err
			}
			if state.InputLog == nil || state.LatestSeq != legacy.LatestSeq || len(state.Messages) != 0 {
				return errors.New("ready before migrated data became authoritative")
			}
			cancel()
			return nil
		}})
	if err != nil || !strings.Contains(log.String(), "输入格式升级完成") {
		t.Fatal(err, log.String())
	}
	if phase := readUpgradeProgress(t, dir).Phase; phase != "completed" {
		t.Fatal(phase)
	}
	store, err = openStore(dir, legacy.RoomID, &inputUpgradeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.LoadRoom()
	if err != nil || !reflect.DeepEqual(state.Messages, legacy.Messages) {
		t.Fatal(state, err)
	}
}

func TestManagedInputUpgradePreservesSavedOperationState(t *testing.T) {
	for _, tt := range []struct {
		name, action, state string
		pause, consume      bool
	}{
		{name: "cancelled", action: "cancel", state: "page_only"},
		{name: "paused-after-delivery", action: "submit", state: "none", pause: true},
		{name: "consumed-and-reclaimed", action: "submit", state: "consumed", consume: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, svc, req := selectionFixture(t)
			mode := "inbox"
			req.Notify = &mode
			v := selectionCall(t, m, "start", req, false)
			q := selectionQuery(req, true)
			q.Version = v.Version
			selectionCall(t, m, tt.action, q, true)
			m.deliverConnectNotifications(t.Context())
			op, err := m.readPlatformSelectionLocked(req.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			n, err := m.readSelectionNotificationLocked(op)
			if err != nil || n.Receipt == nil || n.RoomReceipt != nil {
				t.Fatal(n, err)
			}
			if tt.pause {
				q := selectionQuery(req, false)
				mode := "none"
				q.Notify = &mode
				selectionCall(t, m, "notify", q, false)
			}
			if tt.consume {
				agent := AgentSnapshot{Version: formatVersion, MemberIdentity: op.MemberIdentity, TurnNumber: 1, LastOfferedInboxSeq: n.Receipt.Seq,
					Turn: &TurnRecord{Number: 1, Status: "running", InboxSnapshotSeq: n.Receipt.Seq}}
				if err := svc.store.SaveAgent(agent); err != nil {
					t.Fatal(err)
				}
				consumeConnectTestInbox(t, svc, op.MemberIdentity, 1)
				agent.Turn.Status = "completed"
				if err := svc.store.SaveAgent(agent); err != nil {
					t.Fatal(err)
				}
				if err := svc.maintainInboxLocal(t.Context(), op.MemberIdentity, nil, time.Now().Add(8*24*time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			opFile, err := os.ReadFile(m.selectionPath(op.ID))
			if err != nil {
				t.Fatal(err)
			}
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if err := os.CopyFS(dir, os.DirFS(m.store.dir)); err != nil {
				t.Fatal(err)
			}
			interrupted := errors.New("stop managed startup at format fence")
			if _, err := openRoomManager(dir, defaultMaxTurns, &inputUpgradeOptions{stop: func(point string) error {
				if point == "header" {
					return interrupted
				}
				return nil
			}}); !errors.Is(err, interrupted) {
				t.Fatal("managed upgrade did not leave an in-progress Room", err)
			}
			reopened, err := openRoomManager(dir, defaultMaxTurns, &inputUpgradeOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.close()
			saved, err := reopened.readSelectionNotificationLocked(op)
			if err != nil || saved.RoomReceipt == nil || saved.Receipt != nil || saved.RoomConsumed != tt.consume {
				t.Fatal(saved, err)
			}
			wantTargets := []string{}
			if tt.pause {
				wantTargets = append(wantTargets, op.MembershipID)
			}
			if !reflect.DeepEqual(saved.RoomEvent.Targets, wantTargets) {
				t.Fatal("delivery audience changed", saved.RoomEvent.Targets)
			}
			result, err := reopened.operationContinuationLocked(op.MemberIdentity, true, saved)
			if err != nil || result.State != tt.state || !tt.pause && result.RoomSeq != saved.RoomReceipt.Seq {
				t.Fatal(result, err)
			}
			after, err := os.ReadFile(reopened.selectionPath(op.ID))
			if err != nil || !bytes.Equal(after, opFile) {
				t.Fatal("original operation was changed", err)
			}
			store, err := OpenStore(reopened.roomDir(svc.info.RoomID), svc.info.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			state, err := store.LoadRoom()
			if err != nil || len(state.EventReceipts) != 1 || state.Messages[saved.RoomReceipt.Seq-1].Event.ResultRef != saved.Event.ResultRef {
				t.Fatal(state, err)
			}
		})
	}
}

func TestInputUpgradeKeepsUnknownActionGate(t *testing.T) {
	f := referenceExecuteTest(t, "discussion")
	f.result = func(int) platform.TargetActionResult { return f.outcome("unknown", "never") }
	if _, err := f.run(); err != nil {
		t.Fatal(err)
	}
	id := f.h.id
	receipt := f.h.store.actionPath(id, f.q.ID)
	before, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(f.h.store.dir)); err != nil {
		t.Fatal(err)
	}
	svc, err := startService(dir, id.RoomID, serviceConfig{unifiedInputs: true}, func(*service, RoomSnapshot) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	after, err := os.ReadFile(svc.store.actionPath(id, f.q.ID))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("unknown receipt changed during input upgrade", err)
	}
	if err := svc.checkSessionRecovery(id); err == nil || !strings.Contains(err.Error(), "1 action outcomes") {
		t.Fatal("unknown receipt did not gate automatic recovery", err)
	}
}

func TestInputUpgradeRecoversAfterProcessDeath(t *testing.T) {
	dir, id := upgradeFixtureCopy(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestInputUpgradeProcessHelper$")
	cmd.Env = append(os.Environ(), "TEAMX_UPGRADE_TEST_DIR="+dir, "TEAMX_UPGRADE_TEST_ROOM="+id.RoomID)
	err := cmd.Run()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 91 {
		t.Fatal("expected exit after replacing a target file", err)
	}
	progress := readUpgradeProgress(t, dir)
	store, err := openStore(dir, id.RoomID, &inputUpgradeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.LoadRoom()
	if err != nil || len(state.EventReceipts) != 1 || readUpgradeProgress(t, dir).ID != progress.ID {
		t.Fatal(state, err)
	}
}

func TestInputUpgradeProcessHelper(t *testing.T) {
	dir := os.Getenv("TEAMX_UPGRADE_TEST_DIR")
	if dir == "" {
		return
	}
	store, err := openStore(dir, os.Getenv("TEAMX_UPGRADE_TEST_ROOM"), &inputUpgradeOptions{stop: func(point string) error {
		if point == "replace-file" {
			os.Exit(91)
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	t.Fatal("process interruption point not reached")
}

func TestInputUpgradeMergesTaskEventQueuesByMembership(t *testing.T) {
	room := historyRoom(1, "saved operation")
	a := MemberIdentity{room.RoomIdentity, "A", "generation-A"}
	b := MemberIdentity{room.RoomIdentity, "B", "generation-B"}
	duplicate := taskEventFixture("shared")
	first := taskEventFixture("earlier-a")
	now := time.Now()
	queues := [][]legacyInputEvent{
		{{a, first, 1, now, true}, {a, duplicate, 2, now.Add(-time.Second), true}},
		{{b, duplicate, 1, now, false}},
	}
	events, err := appendUpgradedEvents(&room, queues, nil)
	if err != nil || len(events) != 2 || room.LatestSeq != 3 || len(room.EventReceipts) != 2 {
		t.Fatal(room, err)
	}
	if room.Messages[1].Event.ID != first.EventID || len(room.Messages[1].Event.Targets) != 0 ||
		room.Messages[2].Event.ID != duplicate.EventID || !reflect.DeepEqual(room.Messages[2].Event.Targets, []string{b.MembershipID}) {
		t.Fatal("event order or delivery audience changed", room.Messages)
	}
	if err := room.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestInputUpgradeMissingJournalDoesNotReinitialize(t *testing.T) {
	dir, id := upgradeFixtureCopy(t)
	stopInputUpgradeAt(t, dir, id.RoomID, "prepared")
	if err := os.Remove(filepath.Join(dir, inputUpgradeJournal)); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(dir, id.RoomID, &inputUpgradeOptions{}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "no progress journal") {
		t.Fatal("missing progress was not reported", err)
	}
}

func TestInputUpgradeDistinguishesFreshAndOrphanedData(t *testing.T) {
	fresh := t.TempDir()
	svc, err := startService(fresh, "fresh", serviceConfig{unifiedInputs: true}, func(*service, RoomSnapshot) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.store.LoadRoom()
	if err != nil || state.InputLog == nil || state.LatestSeq != 0 {
		t.Fatal(state, err)
	}
	closeService(t, svc)
	if _, err := os.Stat(filepath.Join(fresh, inputUpgradeJournal)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fresh Room entered legacy conversion", err)
	}
	for _, name := range []string{"agents", "inboxes", "room-input", inputUpgradeDirectory} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
				t.Fatal(err)
			}
			if store, err := openStore(dir, "orphaned", &inputUpgradeOptions{}); !errors.Is(err, ErrInvalid) {
				if store != nil {
					_ = store.Close()
				}
				t.Fatal("orphaned data accepted as fresh", err)
			}
		})
	}
}
