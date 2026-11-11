package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func ensureAssistant(t *testing.T, m *roomManager) defaultAssistantRecord {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	record, err := m.ensureDefaultAssistantLocked()
	if err != nil {
		t.Fatal(err)
	}
	return *record
}

func assistantSummary(t *testing.T, m *roomManager) roomSummary {
	t.Helper()
	for _, e := range m.index.Rooms {
		if e.RoomIdentity == m.index.DefaultAssistant.RoomIdentity {
			return e.roomSummary
		}
	}
	t.Fatal("missing default assistant Room")
	return roomSummary{}
}

func TestDefaultAssistantOnceForNewAndExistingCatalogs(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[existing], func(t *testing.T) {
			root := t.TempDir()
			m := directTestManager(t, root, testTeamDefinition(t).Runtime)
			var preserved map[string][]byte
			if existing {
				// A same-name, same-role manual DM must never be adopted.
				manual := createDirectConversation(t, m, directSelection("manual-assistant"))
				m.teams = map[string]teamDefinition{"sample": testTeamDefinition(t)}
				room := createTeamRoom(t, m, "legacy-room")
				preserved = map[string][]byte{}
				for _, id := range []string{manual.RoomID, room.RoomID} {
					for _, name := range []string{"room.json", "room-config.json"} {
						path := filepath.Join(m.roomDir(id), name)
						data, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						preserved[path] = data
					}
				}
			}
			record := ensureAssistant(t, m)
			if record.Status != "completed" || record.CreateID != defaultAssistantCreateID {
				t.Fatal(record)
			}
			summary := assistantSummary(t, m)
			config, state := readManagedRoom(t, m, summary)
			if !state.IsDirect || len(state.Members) != 1 || len(state.Messages) != 0 || config.Members[0].MembershipID == "" {
				t.Fatal("default assistant is incomplete", state)
			}
			wantCount := map[bool]int{false: 1, true: 3}[existing]
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					m.mu.Lock()
					defer m.mu.Unlock()
					got, err := m.ensureDefaultAssistantLocked()
					if err != nil || *got != record {
						t.Error("concurrent observation changed default", got, err)
					}
				})
			}
			wg.Wait()
			w := managerHTTP(m, "/v1/rooms/rename", roomRenameRequest{
				ServiceEpoch: m.epoch, Caller: humanCaller, RoomIdentity: summary.RoomIdentity,
				Name: "我的助理", ExpectedName: summary.Name,
			})
			if w.Code != 200 || ensureAssistant(t, m) != record {
				t.Fatal("rename changed default identity", w.Code, w.Body.String())
			}
			for path, before := range preserved {
				after, err := os.ReadFile(path)
				if err != nil || string(before) != string(after) {
					t.Fatal("initialization rewrote an existing conversation", path, err)
				}
			}
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			restored := directTestManager(t, root, nil)
			if got := ensureAssistant(t, restored); got != record || len(restored.index.Rooms) != wantCount {
				t.Fatal("restart replaced or duplicated default", got)
			}
			_, after := readManagedRoom(t, restored, summary)
			if !reflect.DeepEqual(state, after) {
				t.Fatal("restart changed membership or history")
			}
		})
	}
}

func TestDefaultAssistantRestoresFrozenRoleAfterBuiltinRename(t *testing.T) {
	root := t.TempDir()
	m := directTestManager(t, root, testTeamDefinition(t).Runtime)
	record := ensureAssistant(t, m)
	summary := assistantSummary(t, m)
	if summary.AgentRoleID != "assistant" {
		t.Fatal("new default did not use the assistant role", summary)
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}

	// Simulate an earlier builtin name in the complete persisted identity,
	// including the frozen runtime profile. Renaming must not migrate it.
	const previousRole = "previous-assistant-role"
	preserved := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".json" {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		data = bytes.ReplaceAll(data, []byte(`"assistant"`), []byte(`"`+previousRole+`"`))
		data = bytes.ReplaceAll(data, []byte(`"agent/assistant"`), []byte(`"agent/`+previousRole+`"`))
		preserved[path] = data
		return os.WriteFile(path, data, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}

	restored := directTestManager(t, root, nil)
	if got := ensureAssistant(t, restored); got != record {
		t.Fatal("rename replaced the default assistant", got, record)
	}
	config, state := readManagedRoom(t, restored, assistantSummary(t, restored))
	if config.AgentRoleID != previousRole || config.Members[0].AgentID != previousRole ||
		state.Members[0].AgentID != previousRole || config.Members[0].Runner.Profile != "agent/"+previousRole {
		t.Fatal("rename rewrote the frozen role or runner", config, state)
	}
	for path, before := range preserved {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("restoring a frozen role changed persisted data", path, err)
		}
	}
}

func TestDefaultAssistantCreationCommitRecovery(t *testing.T) {
	for _, boundary := range []string{"after-reservation", "before-ready", "after-ready"} {
		t.Run(boundary, func(t *testing.T) {
			root := t.TempDir()
			m := directTestManager(t, root, testTeamDefinition(t).Runtime)
			rename := m.store.ops.rename
			fired := false
			m.store.ops.rename = func(from, to string) error {
				var next roomsIndex
				if err := readSnapshot(from, &next, maxRoomsFileBytes); err != nil {
					return err
				}
				if !fired && next.DefaultAssistant != nil &&
					(boundary == "after-reservation" && next.DefaultAssistant.Status == "pending" ||
						boundary != "after-reservation" && next.DefaultAssistant.Status == "completed") {
					fired = true
					if boundary != "before-ready" {
						if err := rename(from, to); err != nil {
							return err
						}
					}
					return errors.New("injected creation commit interruption")
				}
				return rename(from, to)
			}
			m.mu.Lock()
			_, err := m.ensureDefaultAssistantLocked()
			m.mu.Unlock()
			if !fired || !errors.Is(err, ErrCommitUnknown) {
				t.Fatal("did not interrupt expected commit", err)
			}
			var disk roomsIndex
			if err := readSnapshot(m.indexPath(), &disk, maxRoomsFileBytes); err != nil {
				t.Fatal(err)
			}
			if len(disk.Rooms) != 1 || disk.DefaultAssistant.RoomIdentity != disk.Rooms[0].RoomIdentity {
				t.Fatal("receipt and reservation were not atomic", disk)
			}
			owner := disk.DefaultAssistant.RoomIdentity
			var membership string
			if init := disk.Rooms[0].Initialization; init != nil {
				membership = init.Config.Members[0].MembershipID
			} else {
				config, _ := readManagedRoom(t, m, disk.Rooms[0].roomSummary)
				membership = config.Members[0].MembershipID
			}
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			// Recovery uses the frozen reservation even with no current defaults.
			restored := directTestManager(t, root, nil)
			got := ensureAssistant(t, restored)
			config, state := readManagedRoom(t, restored, assistantSummary(t, restored))
			if got.Status != "completed" || got.RoomIdentity != owner || len(restored.index.Rooms) != 1 ||
				config.Members[0].MembershipID != membership || state.Members[0].MembershipID != membership {
				t.Fatal("crash recovery replaced reserved identity", got)
			}
		})
	}
}

func TestDefaultAssistantBlockedConfigurationAndRetry(t *testing.T) {
	root := t.TempDir()
	m := directTestManager(t, root, nil)
	// Existing conversations remain usable while assistant configuration is absent.
	m.teams = map[string]teamDefinition{"sample": testTeamDefinition(t)}
	old := createTeamRoom(t, m, "existing")
	m.factory = controlledActivationFactory(nil, nil)
	m.mu.Lock()
	_, err := m.ensureDefaultAssistantLocked()
	m.mu.Unlock()
	if err == nil || m.index.DefaultAssistant.Status != "pending" || len(m.index.Rooms) != 1 {
		t.Fatal("missing configuration did not leave a blocked receipt", err)
	}
	w := managerHTTP(m, "/v1/rooms", nil)
	var catalog roomsResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &catalog) != nil || catalog.DefaultAssistant.ErrorCode != "configuration_required" ||
		catalog.DefaultAssistant.NextStep != "teamx start init" {
		t.Fatal(w.Code, w.Body.String())
	}
	openOK(t, m, old.RoomIdentity, "open-existing-while-blocked")
	if err := m.restoreRuntimes(); err != nil {
		t.Fatal(err)
	}
	awaitRecovery(t, m)
	m.defaultRuntime = testTeamDefinition(t).Runtime
	w = managerHTTP(m, "/v1/rooms/default-assistant/retry", map[string]any{"service_epoch": m.epoch, "caller": humanCaller})
	var got defaultAssistantRecord
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Status != "completed" || len(m.index.Rooms) != 2 {
		t.Fatal(w.Code, w.Body.String())
	}
	awaitRecovery(t, m)
	m.mu.Lock()
	rt := m.runtimes[got.RoomIdentity]
	m.mu.Unlock()
	if rt == nil || rt.service == nil || rt.status != "ready" {
		t.Fatal("retry did not join runtime recovery", rt)
	}
}

func TestDefaultAssistantInitialEnableAndPauseSurviveRestart(t *testing.T) {
	root := t.TempDir()
	m := directTestManager(t, root, testTeamDefinition(t).Runtime)
	var turns atomic.Int32
	m.factory = controlledActivationFactory(func(MemberIdentity, context.Context) error {
		turns.Add(1)
		return nil
	}, nil)
	record := ensureAssistant(t, m)
	if err := m.restoreRuntimes(); err != nil {
		t.Fatal(err)
	}
	awaitRecovery(t, m)
	svc := m.runtimes[record.RoomIdentity].service
	if svc == nil {
		t.Fatal("enabled default did not recover")
	}
	list, err := svc.members(context.Background())
	if err != nil || len(list.Members) != 1 || list.Members[0].Execution.Paused || list.Members[0].Execution.Active {
		t.Fatal("default not initially enabled and idle", list, err)
	}
	member := list.Members[0]
	if _, err := os.Stat(filepath.Join(svc.store.dir, "agents", member.MembershipID, "session.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("opening default assistant created a backend session", err)
	}
	if turns.Load() != 0 {
		t.Fatal("initialization started a model turn")
	}
	m.mu.Lock()
	ops, err := m.platformConnectOperationsLocked()
	m.mu.Unlock()
	if err != nil || len(ops) != 0 {
		t.Fatal("initialization started platform authorization", ops, err)
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	// An enabled but untouched default recovers without creating any work.
	previous := m
	m = directTestManager(t, root, nil)
	m.factory = previous.factory
	if ensureAssistant(t, m) != record {
		t.Fatal("untouched restart changed the default")
	}
	if err := m.restoreRuntimes(); err != nil {
		t.Fatal(err)
	}
	awaitRecovery(t, m)
	svc = m.runtimes[record.RoomIdentity].service
	if svc == nil {
		t.Fatal("untouched enabled default did not recover")
	}
	list, err = svc.members(context.Background())
	if err != nil || list.Members[0].Execution.Paused || turns.Load() != 0 {
		t.Fatal("untouched restart changed enable intent or created work", list, err)
	}
	managedPost(t, m, svc, "real-user-input")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id := MemberIdentity{RoomIdentity: record.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	waitMember(t, ctx, svc.target(member.MembershipID), id, func(s MemberStatus) bool {
		return !s.Active && s.Agent.TurnNumber == 1
	})
	if turns.Load() != 1 {
		t.Fatal("enabled default did not process real user input")
	}
	if _, err := svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "pause"); err != nil {
		t.Fatal(err)
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	restored := directTestManager(t, root, nil)
	restored.factory = m.factory
	if ensureAssistant(t, restored) != record {
		t.Fatal("restart changed default reference")
	}
	if err := restored.restoreRuntimes(); err != nil {
		t.Fatal(err)
	}
	awaitRecovery(t, restored)
	openOK(t, restored, record.RoomIdentity, "view-paused-default")
	svc = restored.runtimes[record.RoomIdentity].service
	list, err = svc.members(ctx)
	if err != nil || !list.Members[0].Execution.Paused || list.Members[0].Execution.SessionTurns.Total != 1 {
		t.Fatal("restart or selection re-enabled paused default", list, err)
	}
	if turns.Load() != 1 {
		t.Fatal("restart replayed a turn")
	}
}

// Reproduce an already authorized deletion from the older, deletable default.
// New deletion requests must not be used to construct this historical state.
func seedLegacyAssistantDeletion(t *testing.T, m *roomManager, summary roomSummary) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.copyIndex()
	for i := range next.Rooms {
		if next.Rooms[i].RoomIdentity == summary.RoomIdentity {
			next.Rooms[i].Status, next.Rooms[i].DeletionPhase = "deleting", "closing"
		}
	}
	next.DefaultAssistant.Status = "deleting"
	if err := m.saveIndex(next); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultAssistantCannotBeDeletedAfterRename(t *testing.T) {
	m := directTestManager(t, t.TempDir(), testTeamDefinition(t).Runtime)
	record := ensureAssistant(t, m)
	summary := assistantSummary(t, m)
	w := managerHTTP(m, "/v1/rooms/rename", roomRenameRequest{ServiceEpoch: m.epoch, Caller: humanCaller,
		RoomIdentity: summary.RoomIdentity, ExpectedName: summary.Name, Name: "我的固定助理"})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	before, err := os.ReadFile(m.indexPath())
	if err != nil {
		t.Fatal(err)
	}
	w = managerHTTP(m, "/v1/rooms/delete", deletionRequest{m.epoch, humanCaller, summary.RoomIdentity})
	if w.Code != http.StatusConflict {
		t.Fatal("renamed default was deletable", w.Code, w.Body.String())
	}
	after, err := os.ReadFile(m.indexPath())
	if err != nil || !bytes.Equal(before, after) || ensureAssistant(t, m) != record {
		t.Fatal("rejected deletion changed default state", err)
	}
	if _, err := os.Stat(m.roomDir(summary.RoomID)); err != nil {
		t.Fatal("rejected deletion removed default data", err)
	}
	manual := createDirectConversation(t, m, directSelection("deletable-manual-assistant"))
	beginDeletion(t, m, manual)
	if failed := awaitDeletion(t, m, manual); failed != nil || ensureAssistant(t, m) != record {
		t.Fatal("manual DM deletion affected protected default", failed)
	}
}

func TestDefaultAssistantLegacyDeletionTombstoneAndManualReplacement(t *testing.T) {
	for _, phase := range []string{"closing", "removing"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			m := directTestManager(t, root, testTeamDefinition(t).Runtime)
			record := ensureAssistant(t, m)
			summary := assistantSummary(t, m)
			config, _ := readManagedRoom(t, m, summary)
			if phase != "ready" {
				// Stop at a durable deletion boundary, before worker execution.
				next := m.copyIndex()
				next.Rooms[0].Status, next.Rooms[0].DeletionPhase = "deleting", phase
				next.DefaultAssistant.Status = "deleting"
				if err := m.saveIndex(next); err != nil {
					t.Fatal(err)
				}
				if phase == "removing" {
					if err := os.RemoveAll(m.roomDir(summary.RoomID)); err != nil {
						t.Fatal(err)
					}
				}
				if err := m.close(); err != nil {
					t.Fatal(err)
				}
				m = directTestManager(t, root, nil)
				if got := ensureAssistant(t, m); got.Status != "deleting" || got.RoomIdentity != record.RoomIdentity || len(m.index.Rooms) != 1 {
					t.Fatal("restart replayed or replaced interrupted deletion", got)
				}
			}
			beginDeletion(t, m, summary)
			if failed := awaitDeletion(t, m, summary); failed != nil {
				t.Fatal("default deletion failed", failed)
			}
			if m.index.DefaultAssistant.Status != "deleted" || len(m.index.Rooms) != 0 {
				t.Fatal("default deletion lost its tombstone")
			}
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			restored := directTestManager(t, root, testTeamDefinition(t).Runtime)
			if got := ensureAssistant(t, restored); got.Status != "deleted" || got.RoomIdentity != record.RoomIdentity || len(restored.index.Rooms) != 0 {
				t.Fatal("restart resurrected deleted default", got)
			}
			manual := createDirectConversation(t, restored, directSelection("manual-replacement"))
			mc, _ := readManagedRoom(t, restored, manual)
			if manual.RoomIdentity == record.RoomIdentity || mc.Members[0].MembershipID == config.Members[0].MembershipID ||
				ensureAssistant(t, restored).Status != "deleted" || len(restored.index.Rooms) != 1 {
				t.Fatal("manual DM inherited default identity")
			}
		})
	}
}

func TestDefaultAssistantReservedIDAndRetryAuthorization(t *testing.T) {
	m := directTestManager(t, t.TempDir(), testTeamDefinition(t).Runtime)
	for _, initialized := range []bool{false, true} {
		if initialized {
			ensureAssistant(t, m)
		}
		w := managerHTTP(m, "/v1/rooms/create", roomCreateRequest{ServiceEpoch: m.epoch, Caller: humanCaller, roomSelection: defaultAssistantSelection()})
		if w.Code != http.StatusBadRequest {
			t.Fatal("ordinary create adopted reserved ID", w.Code, w.Body.String())
		}
		if _, err := m.create(defaultAssistantSelection()); !errors.Is(err, ErrRequest) || !strings.Contains(err.Error(), "reserved") {
			t.Fatal("internal ordinary create did not protect system ID", err)
		}
	}
	for _, req := range []map[string]any{
		{"service_epoch": "stale", "caller": humanCaller},
		{"service_epoch": m.epoch, "caller": Caller{Kind: "agent", ID: assistantRoleID, MembershipID: "member"}},
		{"service_epoch": m.epoch, "caller": humanCaller, "room_id": "other-room"},
	} {
		w := managerHTTP(m, "/v1/rooms/default-assistant/retry", req)
		if w.Code == 200 {
			t.Fatal("invalid retry admitted", req)
		}
	}
}

func deletedAssistantFixture(t *testing.T) (*roomManager, defaultAssistantRecord, managedRoomConfig) {
	t.Helper()
	m := directTestManager(t, t.TempDir(), testTeamDefinition(t).Runtime)
	record := ensureAssistant(t, m)
	summary := assistantSummary(t, m)
	config, _ := readManagedRoom(t, m, summary)
	seedLegacyAssistantDeletion(t, m, summary)
	beginDeletion(t, m, summary)
	if failed := awaitDeletion(t, m, summary); failed != nil {
		t.Fatal(failed)
	}
	return m, record, config
}

func TestDefaultAssistantExplicitRestoreCreatesIndependentInstance(t *testing.T) {
	m, old, config := deletedAssistantFixture(t)
	root := m.store.dir
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	m = directTestManager(t, root, testTeamDefinition(t).Runtime)
	if got := ensureAssistant(t, m); got.Status != "deleted" || got.RoomIdentity != old.RoomIdentity {
		t.Fatal("startup restored default without a request", got)
	}
	manual := createDirectConversation(t, m, directSelection("manual-before-restore"))
	req := deletionRequest{m.epoch, humanCaller, old.RoomIdentity}
	for _, mutate := range []func(*deletionRequest){
		func(r *deletionRequest) { r.ServiceEpoch = "stale" },
		func(r *deletionRequest) {
			r.Caller = Caller{Kind: "agent", ID: assistantRoleID, MembershipID: config.Members[0].MembershipID}
		},
		func(r *deletionRequest) { r.RoomInstance = "stale-instance" },
		func(r *deletionRequest) { r.RoomIdentity = RoomIdentity{} },
	} {
		bad := req
		mutate(&bad)
		if w := managerHTTP(m, "/v1/rooms/default-assistant/restore", bad); w.Code == 200 || ensureAssistant(t, m).Status != "deleted" {
			t.Fatal("invalid restore changed tombstone", w.Code, w.Body.String())
		}
	}
	w := managerHTTP(m, "/v1/rooms/default-assistant/restore", req)
	var restored defaultAssistantRecord
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &restored) != nil || restored.Status != "completed" {
		t.Fatal(w.Code, w.Body.String())
	}
	if restored.RoomIdentity == old.RoomIdentity || restored.RoomIdentity == manual.RoomIdentity || len(m.index.Rooms) != 2 {
		t.Fatal("restore adopted an old or manually created instance", restored)
	}
	current, snapshot := readManagedRoom(t, m, assistantSummary(t, m))
	if current.Members[0].MembershipID == config.Members[0].MembershipID || len(snapshot.Messages) != 0 || len(snapshot.Members) != 1 {
		t.Fatal("restored default inherited the deleted member", snapshot)
	}
	before, _ := os.ReadFile(m.indexPath())
	if w := managerHTTP(m, "/v1/rooms/default-assistant/restore", req); w.Code != http.StatusConflict {
		t.Fatal("stale restore was accepted", w.Code, w.Body.String())
	}
	after, _ := os.ReadFile(m.indexPath())
	if !bytes.Equal(before, after) || ensureAssistant(t, m) != restored {
		t.Fatal("stale restore duplicated the default")
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	m = directTestManager(t, root, nil)
	if ensureAssistant(t, m) != restored {
		t.Fatal("restart changed the explicitly restored default")
	}
	if w := managerHTTP(m, "/v1/rooms/delete", deletionRequest{m.epoch, humanCaller, restored.RoomIdentity}); w.Code != http.StatusConflict {
		t.Fatal("restored default was deletable", w.Code)
	}
}

func TestDefaultAssistantExplicitRestoreWaitsForConfiguration(t *testing.T) {
	m, old, _ := deletedAssistantFixture(t)
	m.defaultRuntime = nil
	w := managerHTTP(m, "/v1/rooms/default-assistant/restore", deletionRequest{m.epoch, humanCaller, old.RoomIdentity})
	if w.Code == 200 || m.index.DefaultAssistant.Status != "pending" || m.index.DefaultAssistant.ErrorCode != "configuration_required" || len(m.index.Rooms) != 0 {
		t.Fatal("restore did not retain explicit intent while blocked", w.Code, w.Body.String())
	}
	root := m.store.dir
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	m = directTestManager(t, root, testTeamDefinition(t).Runtime)
	if got := ensureAssistant(t, m); got.Status != "completed" || got.RoomIdentity == old.RoomIdentity {
		t.Fatal("configured restart did not continue the explicit restore", got)
	}
}

func TestDefaultAssistantRestoreIntentCommitRecovery(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-commit", true: "after-commit"}[commit], func(t *testing.T) {
			m, old, _ := deletedAssistantFixture(t)
			rename := m.store.ops.rename
			m.store.ops.rename = func(from, to string) error {
				var next roomsIndex
				if err := readSnapshot(from, &next, maxRoomsFileBytes); err != nil {
					return err
				}
				if next.DefaultAssistant.Status == "pending" {
					if commit {
						if err := rename(from, to); err != nil {
							return err
						}
					}
					return errors.New("injected restore intent commit interruption")
				}
				return rename(from, to)
			}
			w := managerHTTP(m, "/v1/rooms/default-assistant/restore", deletionRequest{m.epoch, humanCaller, old.RoomIdentity})
			if w.Code == 200 || !m.store.uncertain {
				t.Fatal("restore commit uncertainty was not exercised", w.Code)
			}
			root := m.store.dir
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			m = directTestManager(t, root, testTeamDefinition(t).Runtime)
			got := ensureAssistant(t, m)
			if commit && (got.Status != "completed" || got.RoomIdentity == old.RoomIdentity) || !commit && (got.Status != "deleted" || got.RoomIdentity != old.RoomIdentity) {
				t.Fatal("restart did not honor the committed restore intent", got)
			}
		})
	}
}

func TestDefaultAssistantInitializationFailureRetriesFrozenMember(t *testing.T) {
	root := t.TempDir()
	m := directTestManager(t, root, testTeamDefinition(t).Runtime)
	rename := m.store.ops.rename
	var blockedPath string
	m.store.ops.rename = func(from, to string) error {
		if err := rename(from, to); err != nil {
			return err
		}
		var next roomsIndex
		if err := readSnapshot(to, &next, maxRoomsFileBytes); err != nil {
			return err
		}
		if blockedPath == "" && next.DefaultAssistant != nil && next.DefaultAssistant.Status == "pending" {
			blockedPath = filepath.Join(m.roomDir(next.DefaultAssistant.RoomID), "room-config.json")
			return os.MkdirAll(blockedPath, 0700)
		}
		return nil
	}
	m.mu.Lock()
	record, err := m.ensureDefaultAssistantLocked()
	m.mu.Unlock()
	if err == nil || record.Status != "pending" || record.ErrorCode != "initialization_failed" || record.NextStep != "" {
		t.Fatal("filesystem error presented as missing model configuration", record, err)
	}
	owner := record.RoomIdentity
	membership := m.index.Rooms[0].Initialization.Config.Members[0].MembershipID
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	// Partial default files must remain an assistant-local initialization
	// error on restart, rather than stopping the entire catalog at upgrade.
	m = directTestManager(t, root, nil)
	m.mu.Lock()
	record, err = m.ensureDefaultAssistantLocked()
	m.mu.Unlock()
	if err == nil || record.RoomIdentity != owner || record.ErrorCode != "initialization_failed" {
		t.Fatal("restart lost blocked default diagnostic", record, err)
	}
	if err := os.Remove(blockedPath); err != nil {
		t.Fatal(err)
	}
	m.defaultRuntime = nil
	got := ensureAssistant(t, m)
	config, _ := readManagedRoom(t, m, assistantSummary(t, m))
	if got.RoomIdentity != owner || got.Error != "" || config.Members[0].MembershipID != membership {
		t.Fatal("retry replaced frozen member or retained failure", got)
	}
}

func TestDefaultAssistantLegacyDeletionCommitRecovery(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-commit", true: "after-commit"}[commit], func(t *testing.T) {
			root := t.TempDir()
			m := directTestManager(t, root, testTeamDefinition(t).Runtime)
			record := ensureAssistant(t, m)
			summary := assistantSummary(t, m)
			seedLegacyAssistantDeletion(t, m, summary)
			rename := m.store.ops.rename
			m.store.ops.rename = func(from, to string) error {
				var next roomsIndex
				if err := readSnapshot(from, &next, maxRoomsFileBytes); err != nil {
					return err
				}
				if next.DefaultAssistant.Status == "deleted" {
					if commit {
						if err := rename(from, to); err != nil {
							return err
						}
					}
					return errors.New("injected deletion commit interruption")
				}
				return rename(from, to)
			}
			beginDeletion(t, m, summary)
			if failed := awaitDeletion(t, m, summary); failed == nil || !m.store.uncertain {
				t.Fatal("expected an uncertain deletion receipt", failed)
			}
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			restored := directTestManager(t, root, testTeamDefinition(t).Runtime)
			got := ensureAssistant(t, restored)
			if got.RoomIdentity != record.RoomIdentity || got.Status != map[bool]string{false: "deleting", true: "deleted"}[commit] {
				t.Fatal("deletion recovery forgot exact default", got)
			}
			if !commit {
				beginDeletion(t, restored, summary)
				if failed := awaitDeletion(t, restored, summary); failed != nil {
					t.Fatal(failed)
				}
			}
			if len(restored.index.Rooms) != 0 || ensureAssistant(t, restored).Status != "deleted" {
				t.Fatal("deletion commit recovery resurrected default")
			}
		})
	}
}

func TestDefaultAssistantRejectsInconsistentReceipt(t *testing.T) {
	for _, change := range []string{"missing", "identity", "status", "source", "deleted-with-room"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			m := directTestManager(t, root, testTeamDefinition(t).Runtime)
			ensureAssistant(t, m)
			next := m.copyIndex()
			switch change {
			case "missing":
				next.DefaultAssistant = nil
			case "identity":
				next.DefaultAssistant.RoomInstance = "foreign-instance"
			case "status":
				next.DefaultAssistant.Status = "pending"
			case "source":
				next.DefaultAssistant.CreateID = "manual-create"
			case "deleted-with-room":
				next.DefaultAssistant.Status = "deleted"
			}
			if err := m.saveIndex(next); err != nil {
				t.Fatal(err)
			}
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			if restored, err := newRoomManager(root, 7); err == nil {
				_ = restored.close()
				t.Fatal("accepted inconsistent default receipt", change)
			}
		})
	}
}

type assistantReadyWriter struct{ err error }

func (w assistantReadyWriter) Write([]byte) (int, error) { return 0, w.err }

func TestDefaultAssistantManagedStartup(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "configuration-blocked", true: "configured"}[configured], func(t *testing.T) {
			root := t.TempDir()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			var defaults *teamRuntime
			if configured {
				defaults = testTeamDefinition(t).Runtime
			}
			stop := errors.New("stop after readiness")
			var stderr bytes.Buffer
			err = serveRoomManager(listener, root, filepath.Join(root, "missing-teams"),
				serviceConfig{endpoint: "http://" + listener.Addr().String(), maxTurns: 7}, defaults,
				controlledActivationFactory(nil, nil), assistantReadyWriter{stop}, &stderr)
			if !errors.Is(err, stop) {
				t.Fatal("startup did not reach readiness", err, stderr.String())
			}
			m := directTestManager(t, root, nil)
			if m.index.DefaultAssistant == nil || m.index.DefaultAssistant.Status != map[bool]string{false: "pending", true: "completed"}[configured] {
				t.Fatal("startup did not initialize default receipt", m.index)
			}
			if !configured && !strings.Contains(stderr.String(), "teamx start init") {
				t.Fatal("blocked startup omitted configuration guidance", stderr.String())
			}
		})
	}
}

func TestDefaultAssistantRuntimeFailureDoesNotReinitialize(t *testing.T) {
	root := t.TempDir()
	m := directTestManager(t, root, testTeamDefinition(t).Runtime)
	var turns atomic.Int32
	m.factory = controlledActivationFactory(func(MemberIdentity, context.Context) error {
		turns.Add(1)
		return errors.New("model temporarily unavailable")
	}, nil)
	record := ensureAssistant(t, m)
	if err := m.restoreRuntimes(); err != nil {
		t.Fatal(err)
	}
	awaitRecovery(t, m)
	svc := m.runtimes[record.RoomIdentity].service
	list, err := svc.members(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	member := list.Members[0]
	id := MemberIdentity{RoomIdentity: record.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	managedPost(t, m, svc, "user-input-before-model-failure")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	waitMember(t, ctx, svc.target(member.MembershipID), id, func(s MemberStatus) bool {
		return !s.Active && s.Agent.Turn != nil && s.Agent.Turn.Status == "failed"
	})
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	restored := directTestManager(t, root, nil)
	restored.factory = m.factory
	if got := ensureAssistant(t, restored); got != record || got.ErrorCode != "" {
		t.Fatal("runtime failure was treated as initialization failure", got)
	}
	if err := restored.restoreRuntimes(); err != nil {
		t.Fatal(err)
	}
	awaitRecovery(t, restored)
	svc = restored.runtimes[record.RoomIdentity].service
	if svc == nil {
		t.Fatal("failed member lost recovery diagnostics")
	}
	list, err = svc.members(ctx)
	if err != nil || list.Members[0].MembershipID != id.MembershipID || !list.Members[0].Execution.Paused ||
		turns.Load() != 1 || len(restored.index.Rooms) != 1 {
		t.Fatal("restart replaced or resumed failed default", list, err)
	}
}
