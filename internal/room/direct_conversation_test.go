package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func directTestManager(t *testing.T, root string, defaults *teamRuntime) *roomManager {
	t.Helper()
	m, err := newTeamRoomManager(root, filepath.Join(root, "missing-teams"), 7, defaults)
	if err != nil {
		t.Fatal(err)
	}
	m.config = serviceConfig{endpoint: "http://127.0.0.1:1", cliPath: os.Args[0]}
	t.Cleanup(func() {
		if err := m.close(); err != nil {
			t.Error(err)
		}
	})
	return m
}

func directSelection(id string) roomSelection {
	return roomSelection{IsDirect: true, AgentRoleID: assistantRoleID, CreateID: id}
}

func createDirectConversation(t *testing.T, m *roomManager, selection roomSelection) roomSummary {
	t.Helper()
	w := managerHTTP(m, "/v1/rooms/create", roomCreateRequest{ServiceEpoch: m.epoch, Caller: humanCaller, roomSelection: selection})
	var summary roomSummary
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &summary) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if !summary.IsDirect || summary.AgentRoleID != assistantRoleID || summary.TeamID != "" || summary.Status != "ready" {
		t.Fatal("wrong direct conversation receipt", summary)
	}
	return summary
}

func TestDirectConversationCreationIsolationAndFrozenRecovery(t *testing.T) {
	root := t.TempDir()
	defaults := testTeamDefinition(t).Runtime
	m := directTestManager(t, root, defaults)
	if len(m.index.Rooms) != 0 {
		t.Fatal("manager automatically created an assistant")
	}
	selection := directSelection("direct-first")
	a := createDirectConversation(t, m, selection)
	if a.Name != "个人助理" {
		t.Fatal(a.Name)
	}
	ca, sa := readManagedRoom(t, m, a)
	if !ca.IsDirect || !sa.IsDirect || len(ca.Members) != 1 || len(sa.Members) != 1 || len(sa.Messages) != 0 {
		t.Fatal("incomplete direct conversation", ca, sa)
	}
	member := ca.Members[0]
	if member.AgentID != assistantRoleID || member.Instructions != assistantInstructions ||
		!reflect.DeepEqual(ca.Runtime, defaults) || sa.Members[0] != member.MemberState {
		t.Fatal("role or runtime not frozen", ca, sa)
	}
	if _, err := os.Stat(filepath.Join(m.roomDir(a.RoomID), member.Workspace)); err != nil {
		t.Fatal(err)
	}
	// Changing caller-owned defaults cannot change the manager or existing rooms.
	defaults.Model.ModelID = "changed-after-manager-load"
	b := createDirectConversation(t, m, directSelection("direct-second"))
	cb, sb := readManagedRoom(t, m, b)
	if a.RoomIdentity == b.RoomIdentity || member.MembershipID == cb.Members[0].MembershipID ||
		sa.Members[0].MembershipID == sb.Members[0].MembershipID || !reflect.DeepEqual(ca.Runtime, cb.Runtime) {
		t.Fatal("direct instances shared identity or mutable runtime")
	}
	if repeated := createDirectConversation(t, m, selection); repeated != a || len(m.index.Rooms) != 2 {
		t.Fatal("create retry changed receipt or duplicated a room", repeated)
	}
	for _, changed := range []roomSelection{
		{IsDirect: true, AgentRoleID: assistantRoleID, CreateID: selection.CreateID, Name: "different"},
		{TeamID: "sample", CreateID: selection.CreateID},
		{IsDirect: true, AgentRoleID: "other-role", CreateID: selection.CreateID},
	} {
		if _, err := m.create(changed); !errors.Is(err, ErrConflict) {
			t.Fatal("changed creation parameters did not conflict", changed, err)
		}
	}
	// The public catalog discovers roles and type without publishing runtime/instructions.
	w := managerHTTP(m, "/v1/rooms", nil)
	var catalog roomsResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &catalog) != nil || len(catalog.AgentRoles) != 1 ||
		!catalog.Rooms[0].IsDirect || catalog.AgentRoles[0].ID != assistantRoleID {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, private := range []string{"instructions", "api_key_env", "binary_path", "offline-test-only"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatal("catalog exposed private runtime", private)
		}
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	// Restoring and retrying need neither a current role template nor default runtime.
	restored := directTestManager(t, root, nil)
	if repeated := createDirectConversation(t, restored, selection); repeated != a {
		t.Fatal("restart changed creation receipt")
	}
	config, err := restored.activationConfig(a.RoomIdentity)
	if err != nil || config.maxTurns != 7 || config.dshProfiles[member.Runner.Profile].Instructions != member.Instructions ||
		!reflect.DeepEqual(config.dshProfiles[member.Runner.Profile].Model, ca.Runtime.Model) {
		t.Fatal("frozen activation failed", err)
	}
	info := openOK(t, restored, a.RoomIdentity, "open-direct")
	w = managerHTTP(restored, "/v1/member/list", apiRequest{ServiceInfo: info, Caller: humanCaller})
	var members serviceMembers
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &members) != nil || !members.IsDirect || len(members.Members) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	if members.Members[0].MembershipID != member.MembershipID || members.Members[0].Status != "idle" || members.Members[0].Execution.Active {
		t.Fatal("new conversation did not preserve ordinary idle startup behavior", members)
	}
	w = managerHTTP(restored, "/v1/info?room_id="+info.RoomID+"&room_instance="+info.RoomInstance+"&service_epoch="+info.ServiceEpoch+"&caller_id=user", nil)
	var public map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &public) != nil || public["is_direct"] != true {
		t.Fatal("info missing direct property", w.Code, w.Body.String())
	}
}

func TestDirectConversationRejectsInvalidCreationSources(t *testing.T) {
	m := directTestManager(t, t.TempDir(), testTeamDefinition(t).Runtime)
	for _, selection := range []roomSelection{
		{IsDirect: true, TeamID: "sample", AgentRoleID: assistantRoleID, CreateID: "both-sources"},
		{IsDirect: true, TeamID: "sample", CreateID: "direct-team"},
		{IsDirect: true, CreateID: "missing-role"},
		{AgentRoleID: assistantRoleID, CreateID: "role-without-direct"},
		{TeamID: "sample", AgentRoleID: assistantRoleID, CreateID: "room-with-role"},
		{IsDirect: true, AgentRoleID: "unknown", CreateID: "unknown-role"},
	} {
		w := managerHTTP(m, "/v1/rooms/create", roomCreateRequest{ServiceEpoch: m.epoch, Caller: humanCaller, roomSelection: selection})
		if w.Code != 400 || len(m.index.Rooms) != 0 {
			t.Fatal("invalid source reserved a room", selection, w.Code, w.Body.String())
		}
	}
	m.defaultRuntime = nil
	if _, err := m.create(directSelection("no-runtime")); !errors.Is(err, ErrRequest) || len(m.index.Rooms) != 0 {
		t.Fatal("missing runtime did not fail before reservation", err)
	}
}

func TestDirectConversationInterruptedCreationKeepsReservedIdentity(t *testing.T) {
	root := t.TempDir()
	m := directTestManager(t, root, testTeamDefinition(t).Runtime)
	selection := directSelection("interrupted-direct")
	e, err := m.prepare(selection)
	if err != nil {
		t.Fatal(err)
	}
	next := m.copyIndex()
	next.Rooms = append(next.Rooms, e)
	if err := m.saveIndex(next); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after durable configuration but before room.json.
	store, err := OpenStore(m.roomDir(e.RoomID), e.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	err = store.save(filepath.Join(store.dir, "room-config.json"), e.Initialization.Config, maxPrivateFileBytes)
	closeErr := store.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	restored := directTestManager(t, root, nil)
	result := createDirectConversation(t, restored, selection)
	c, snapshot := readManagedRoom(t, restored, result)
	if result.RoomIdentity != e.RoomIdentity || c.Members[0].MembershipID != e.Initialization.Config.Members[0].MembershipID ||
		snapshot.Members[0] != c.Members[0].MemberState || !snapshot.IsDirect {
		t.Fatal("retry replaced reserved identity")
	}
}

func TestDirectConversationFixedMembership(t *testing.T) {
	state := historyRoom(1, "hello")
	state.IsDirect = true
	h := newRoomHarness(t, state, nil)
	member := state.Members[0]
	attached := roomCall[MemberInfo](t, h, AttachMember{MemberIdentity: h.memberID(member.AgentID, member.MembershipID), Target: h.target})
	// Retrying the original add remains idempotent, unlike a new join.
	roomCall[MemberInfo](t, h, AddMember{MemberIdentity: h.memberID(member.AgentID, member.MembershipID), Runner: member.Runner, Target: h.target})
	roomError[MemberInfo](t, h, AddMember{MemberIdentity: h.memberID("B", "other-generation"), Runner: member.Runner, Target: h.target}, ErrConflict)
	roomError[MemberInfo](t, h, RemoveMember{h.memberID(member.AgentID, member.MembershipID)}, ErrConflict)
	if list := h.list(t); !list.IsDirect || len(list.Members) != 1 || list.Members[0] != attached || list.Revision != state.Revision {
		t.Fatal("rejected membership change mutated the direct conversation", list)
	}
	// Ordinary messages still work under the unchanged caller identity.
	roomCall[PostReceipt](t, h, h.post(humanCaller, "user-next", "continue"))
	h.idle(t)
	saved, err := h.store.LoadRoom()
	if err != nil || !saved.IsDirect || len(saved.Messages) != 2 {
		t.Fatal(saved, err)
	}
}

func TestDirectConversationSnapshotValidation(t *testing.T) {
	for _, count := range []int{0, 2} {
		state := historyRoom(0, "")
		state.IsDirect = true
		if count == 0 {
			state.Members = []MemberState{}
		} else {
			state.Members = append(state.Members, MemberState{AgentID: "B", MembershipID: "generation-B", Runner: state.Members[0].Runner})
		}
		if err := state.validate(); err == nil {
			t.Fatal("accepted invalid DM member count", count)
		}
	}
}

func TestDirectConversationRejectsInconsistentPersistence(t *testing.T) {
	for _, change := range []string{"catalog-type", "selection-role", "config-type", "snapshot-type", "snapshot-member"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			m := directTestManager(t, root, testTeamDefinition(t).Runtime)
			summary := createDirectConversation(t, m, directSelection("inconsistent"))
			c, snapshot := readManagedRoom(t, m, summary)
			switch change {
			case "catalog-type":
				m.index.Rooms[0].IsDirect = false
			case "selection-role":
				m.index.Rooms[0].Selection.AgentRoleID = "other"
			case "config-type":
				c.IsDirect = false
			case "snapshot-type":
				snapshot.IsDirect = false
			case "snapshot-member":
				snapshot.Members[0].MembershipID = "replacement"
			}
			if err := m.saveIndex(m.index); err != nil {
				t.Fatal(err)
			}
			for name, value := range map[string]any{"room-config.json": c, "room.json": snapshot} {
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(m.roomDir(summary.RoomID), name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := m.activationConfig(summary.RoomIdentity); err == nil {
				t.Fatal("opened inconsistent direct conversation")
			}
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			if restored, err := newTeamRoomManager(root, filepath.Join(root, "missing-teams"), 7, nil); err == nil {
				restored.close()
				t.Fatal("startup accepted inconsistent direct conversation")
			}
		})
	}
}

func TestRoomWithoutDirectFieldsRemainsOrdinary(t *testing.T) {
	root := t.TempDir()
	m := directTestManager(t, root, nil)
	m.teams = map[string]teamDefinition{"sample": testTeamDefinition(t)}
	summary := createTeamRoom(t, m, "legacy-room")
	// Reproduce old files by removing the new keys recursively.
	var strip func(any)
	strip = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			delete(v, "is_direct")
			delete(v, "agent_role_id")
			for _, item := range v {
				strip(item)
			}
		case []any:
			for _, item := range v {
				strip(item)
			}
		}
	}
	paths := []string{m.indexPath(), filepath.Join(m.roomDir(summary.RoomID), "room-config.json"), filepath.Join(m.roomDir(summary.RoomID), "room.json")}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		strip(value)
		data, _ = json.Marshal(value)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := make([][]byte, len(paths))
	for i, path := range paths {
		before[i], _ = os.ReadFile(path)
	}
	restored := directTestManager(t, root, nil)
	c, state := readManagedRoom(t, restored, summary)
	if c.IsDirect || state.IsDirect || restored.index.Rooms[0].IsDirect || len(state.Members) != 1 {
		t.Fatal("inferred direct conversation from legacy data")
	}
	for i, path := range paths {
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before[i], after) {
			t.Fatal("startup rewrote legacy conversation", path)
		}
	}
	// The unchanged actor membership behavior still permits removal in ordinary rooms.
	h := newRoomHarness(t, state, nil)
	member := state.Members[0]
	roomCall[MemberInfo](t, h, RemoveMember{h.memberID(member.AgentID, member.MembershipID)})
	if h.idle(t).IsDirect {
		t.Fatal("ordinary room changed type")
	}
}

// Guard against accidentally adding the property to identity checks: DM callers
// carry exactly the same Room/Agent/ServiceEpoch fields as ordinary callers.
func TestDirectConversationServiceRejectsMembershipChangesBeforeSideEffects(t *testing.T) {
	m := directTestManager(t, t.TempDir(), testTeamDefinition(t).Runtime)
	summary := createDirectConversation(t, m, directSelection("service-direct"))
	info := openOK(t, m, summary.RoomIdentity, "service-open")
	m.mu.Lock()
	svc, _ := m.runtimeServiceLocked(summary.RoomIdentity)
	m.mu.Unlock()
	list, err := svc.members(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	member := list.Members[0]
	if _, err := svc.add(apiRequest{ServiceInfo: info, AgentID: "other", Runner: member.Runner}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	called := false
	svc.beginPlatformMemberChange = func(context.Context, MemberIdentity) (func(), error) {
		called = true
		return func() {}, nil
	}
	if _, err := svc.remove(apiRequest{ServiceInfo: info, AgentID: member.AgentID, MembershipID: member.MembershipID}); !errors.Is(err, ErrConflict) || called {
		t.Fatal("rejected removal started platform cleanup", err)
	}
}
