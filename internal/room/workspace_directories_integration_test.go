//go:build integration || e2e

package room

import (
	"bytes"
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

func applyMemberDirectories(t *testing.T, m *roomManager, room roomSummary, dirs map[string]string) workspaceOperation {
	t.Helper()
	saved, _ := readManagedRoom(t, m, room)
	w, err := normalizeWorkspace(*saved.Workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	w.MemberDirectories = dirs
	id, _ := newOpaqueID()
	applyWorkspace(t, m, workspaceApplyRequest{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, OperationID: id, ExpectedVersion: saved.WorkspaceVersion, Target: w})
	op := awaitWorkspace(t, m)
	if op.Status != "completed" {
		t.Fatalf("directory application: %s/%s/%s", op.Status, op.Phase, op.ErrorCode)
	}
	return op
}

func workspaceMemberReferences(t *testing.T, m *roomManager, room roomSummary) map[string]sessionReference {
	t.Helper()
	saved, _ := readManagedRoom(t, m, room)
	refs := map[string]sessionReference{}
	for _, member := range saved.Members {
		id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
		ref, _, err := readSessionReference(filepath.Join(m.roomDir(room.RoomID), "agents", member.MembershipID, "session.json"), id)
		if err != nil {
			t.Fatal(err)
		}
		refs[member.MembershipID] = ref
	}
	return refs
}

func TestWorkspaceDirectoryLifecycleRetainsOtherSessionsAndFiles(t *testing.T) {
	parallelFixtureTest(t)
	m, root := directoryTestManager(t)
	room := managerCreate(t, m, "directory-lifecycle")
	svc := openRoomOK(t, m, room, "initial")
	saved, beforeRoom := readManagedRoom(t, m, room)
	a, b := saved.Members[0], saved.Members[1]
	managedPost(t, m, svc, "create-private-sessions")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, member := range saved.Members {
		id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
		waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
	}
	refs := workspaceMemberReferences(t, m, room)
	marker := filepath.Join(root, "uncommitted.txt")
	if err := os.WriteFile(marker, []byte("user-owned dirty content"), 0600); err != nil {
		t.Fatal(err)
	}
	op := applyMemberDirectories(t, m, room, map[string]string{a.MembershipID: root})
	if !reflect.DeepEqual(op.RotateMembers, []string{a.MembershipID}) {
		t.Fatal("rotated unaffected members", op.RotateMembers)
	}
	after := workspaceMemberReferences(t, m, room)
	if after[a.MembershipID].Directory == refs[a.MembershipID].Directory || !after[a.MembershipID].Pending || after[b.MembershipID] != refs[b.MembershipID] {
		t.Fatal("incorrect generation selection", after)
	}
	current, roomState := readManagedRoom(t, m, room)
	if current.Members[0].Workspace != a.Workspace || current.Members[0].Instructions != a.Instructions || !reflect.DeepEqual(current.Members[0].ToolEnv, a.ToolEnv) || roomState.RoomIdentity != beforeRoom.RoomIdentity {
		t.Fatal("directory changed unrelated configuration")
	}
	status := queryWorkspace(t, m, room, "")
	if status.Members[0].EffectiveDirectory != root || status.Members[0].Workspace != a.Workspace || status.Members[0].DirectorySource != "external" {
		t.Fatal(status.Members)
	}
	if got := m.runtimes[room.RoomIdentity].service.dshProfiles[a.Runner.Profile].Workspace; got != root {
		t.Fatal("DSH cwd", got)
	}
	// A same-target request has no receipt, version increase, close or rotation.
	id, _ := newOpaqueID()
	r := applyWorkspace(t, m, workspaceApplyRequest{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, OperationID: id, ExpectedVersion: current.WorkspaceVersion, Target: *current.Workspace})
	if !r.Unchanged || len(m.index.WorkspaceOperations) != 1 || !reflect.DeepEqual(after, workspaceMemberReferences(t, m, room)) {
		t.Fatal("unchanged request had effects", r)
	}
	// Serial members can see exactly the same uncommitted files.
	op = applyMemberDirectories(t, m, room, map[string]string{a.MembershipID: root, b.MembershipID: root})
	if !reflect.DeepEqual(op.RotateMembers, []string{b.MembershipID}) {
		t.Fatal(op.RotateMembers)
	}
	if workspaceMemberReferences(t, m, room)[a.MembershipID] != after[a.MembershipID] {
		t.Fatal("shared directory rotated first member again")
	}
	other := filepath.Join(root, "next")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	// All sharers move together: parent/child external bindings are forbidden.
	applyMemberDirectories(t, m, room, map[string]string{a.MembershipID: other, b.MembershipID: other})
	config := m.config
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	restored := testManager(t, m.store.dir, managerTestProfiles(t))
	restored.config, restored.factory, restored.workspaceRoots = config, controlledActivationFactory(nil, nil), &[]string{root}
	openRoomOK(t, restored, room, "restart")
	if got := restored.runtimes[room.RoomIdentity].service.dshProfiles[a.Runner.Profile].Workspace; got != other {
		t.Fatal("restart lost directory", got)
	}
	applyMemberDirectories(t, restored, room, nil)
	if got := restored.runtimes[room.RoomIdentity].service.dshProfiles[a.Runner.Profile].Workspace; got != filepath.Join(restored.roomDir(room.RoomID), a.Workspace) {
		t.Fatal("default directory", got)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "user-owned dirty content" {
		t.Fatal("external content changed", err)
	}
}

func TestWorkspaceDirectoryUnavailableCanRepairWithoutOpeningService(t *testing.T) {
	parallelFixtureTest(t)
	m, root := directoryTestManager(t)
	room := managerCreate(t, m, "repair-directory")
	openRoomOK(t, m, room, "initial")
	saved, _ := readManagedRoom(t, m, room)
	dir := filepath.Join(root, "temporarily-mounted")
	os.Mkdir(dir, 0700)
	applyMemberDirectories(t, m, room, map[string]string{saved.Members[0].MembershipID: dir})
	config := m.config
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	restored := testManager(t, m.store.dir, managerTestProfiles(t))
	restored.config, restored.factory, restored.workspaceRoots = config, controlledActivationFactory(nil, nil), &[]string{root}
	status := queryWorkspace(t, restored, room, "")
	if status.Workspace.MemberDirectories[saved.Members[0].MembershipID] != dir || status.DirectoryError != "directory_not_found" {
		t.Fatal("lost unavailable path")
	}
	beginManagedOpen(t, restored, room, "unavailable")
	if op := awaitLatestOpen(t, restored); op.Status != "failed" {
		t.Fatal("silently used default cwd", op)
	}
	// Uses the catalog identity and verifies durable settlement, without a service epoch.
	op := applyMemberDirectories(t, restored, room, nil)
	if op.SourceEpoch != "" {
		t.Fatal("invented source service epoch")
	}
}

func TestWorkspaceDirectoryClaimsRejectOverlapAndSurviveRestart(t *testing.T) {
	parallelFixtureTest(t)
	m, root := directoryTestManager(t)
	first, second := managerCreate(t, m, "first"), managerCreate(t, m, "second")
	openRoomOK(t, m, first, "initial")
	a, _ := readManagedRoom(t, m, first)
	b, _ := readManagedRoom(t, m, second)
	applyMemberDirectories(t, m, first, map[string]string{a.Members[0].MembershipID: root})
	child := filepath.Join(root, "child")
	os.Mkdir(child, 0700)
	for _, dir := range []string{root, child} {
		w := *b.Workspace
		w.MemberDirectories = map[string]string{b.Members[0].MembershipID: dir}
		id, _ := newOpaqueID()
		response := managerHTTP(m, "/v1/workspace/apply", workspaceApplyRequest{ServiceEpoch: m.epoch, RoomIdentity: second.RoomIdentity, Caller: humanCaller, OperationID: id, ExpectedVersion: 1, Target: w})
		if response.Code == 200 || !strings.Contains(response.Body.String(), "directory_conflict") {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	// Releasing the old binding makes the directory available to another Room.
	applyMemberDirectories(t, m, first, nil)
	applyMemberDirectories(t, m, second, map[string]string{b.Members[0].MembershipID: root})
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	restored := testManager(t, m.store.dir, managerTestProfiles(t))
	restored.config, restored.factory, restored.workspaceRoots = m.config, controlledActivationFactory(nil, nil), &[]string{root}
	if err := restored.checkWorkspaceDirectoryClaimsLocked(first.RoomIdentity, schedulingSerial, workspaceConfig{MemberDirectories: map[string]string{"target": root}}); err == nil {
		t.Fatal("restart lost directory claims")
	}
}

func TestWorkspaceDirectoryParallelAndParentChildAdmission(t *testing.T) {
	parallelFixtureTest(t)
	for _, mode := range []schedulingMode{schedulingSerial, schedulingParallel} {
		t.Run(string(mode), func(t *testing.T) {
			m, root := directoryTestManager(t)
			owner := RoomIdentity{RoomID: "room", RoomInstance: "instance"}
			for _, paths := range [][]string{{root, root}, {root, filepath.Join(root, "child")}, {filepath.Join(root, "a"), filepath.Join(root, "b")}} {
				w := workspaceConfig{MemberDirectories: map[string]string{"a": paths[0], "b": paths[1]}}
				err := m.checkWorkspaceDirectoryClaimsLocked(owner, mode, w)
				want := paths[0] == paths[1] && mode == schedulingSerial || paths[0] != root
				if (err == nil) != want {
					t.Fatal(mode, paths, err)
				}
			}
		})
	}
}

func TestWorkspaceDirectoryRetryFreezesSymlinkTargetAndRetainsSessions(t *testing.T) {
	parallelFixtureTest(t)
	for _, boundary := range []string{"config", "member-progress"} {
		t.Run(boundary, func(t *testing.T) {
			m, root := directoryTestManager(t)
			room := managerCreate(t, m, "interrupted-directory")
			svc := openRoomOK(t, m, room, "initial")
			saved, _ := readManagedRoom(t, m, room)
			managedPost(t, m, svc, "sessions")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for _, member := range saved.Members {
				id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
				waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
			}
			refs := workspaceMemberReferences(t, m, room)
			a, b := saved.Members[0], saved.Members[1]
			dir, next, alias := filepath.Join(root, "original"), filepath.Join(root, "other"), filepath.Join(root, "alias")
			os.Mkdir(dir, 0700)
			os.Mkdir(next, 0700)
			os.Symlink(dir, alias)
			w := *saved.Workspace
			w.MemberDirectories = map[string]string{a.MembershipID: alias}
			id, _ := newOpaqueID()
			req := workspaceApplyRequest{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, OperationID: id, ExpectedVersion: 1, Target: w}
			rename := m.store.ops.rename
			var injected atomic.Bool
			m.store.ops.rename = func(from, to string) error {
				inject := boundary == "config" && filepath.Base(to) == "room-config.json"
				if boundary == "member-progress" && to == m.indexPath() {
					var index roomsIndex
					data, _ := os.ReadFile(from)
					inject = json.Unmarshal(data, &index) == nil && len(index.WorkspaceOperations) > 0 && len(index.WorkspaceOperations[0].SessionsUpdated) == 1
				}
				if inject && injected.CompareAndSwap(false, true) {
					if err := rename(from, to); err != nil {
						return err
					}
					return errors.New("injected uncertain commit")
				}
				return rename(from, to)
			}
			applyWorkspace(t, m, req)
			failed := awaitWorkspace(t, m)
			if !injected.Load() || failed.Status != "failed" {
				t.Fatal("boundary not exercised", failed)
			}
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			os.Remove(alias)
			os.Symlink(next, alias)
			restored := testManager(t, m.store.dir, managerTestProfiles(t))
			restored.config, restored.factory, restored.workspaceRoots = m.config, controlledActivationFactory(nil, nil), &[]string{root}
			req.ServiceEpoch = restored.epoch
			if op := applyWorkspace(t, restored, req); op.Target.MemberDirectories[a.MembershipID] != dir {
				t.Fatal("duplicate followed changed symlink")
			}
			retryWorkspaceHTTP(t, restored, room, id)
			completed := awaitWorkspace(t, restored)
			if completed.Status != "completed" {
				t.Fatal(completed)
			}
			after := workspaceMemberReferences(t, restored, room)
			if after[b.MembershipID] != refs[b.MembershipID] || !after[a.MembershipID].Pending || !strings.HasSuffix(after[a.MembershipID].Directory, id) {
				t.Fatal("retry changed generations", after)
			}
		})
	}
}

func TestWorkspaceDirectoryToolsUseEffectiveCWDAndManagedGitRejectsExternal(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, connection, members := cliToolFixture(t, "pwd\n")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.workspaceRoots = &[]string{root}
	m.config.endpoint, m.config.cliPath = "http://127.0.0.1:1", os.Args[0]
	m.factory = controlledActivationFactory(nil, nil)
	bindCLITool(t, m, room.RoomIdentity, members[0], connection, "gitlab", 0)
	applyMemberDirectories(t, m, room, map[string]string{members[0].MembershipID: root})
	svc = m.runtimes[room.RoomIdentity].service
	client, endpoint := toolPipeServer(t, m)
	var out, errOut bytes.Buffer
	if err := runToolsWithClient(context.Background(), []string{"run", connection.ToolEntryID, "api"}, bytes.NewReader(nil), &out, &errOut, cliToolEnv(endpoint, svc, members[0]), client); err != nil || strings.TrimSpace(out.String()) != root {
		t.Fatal("tool cwd", err, out.String(), errOut.String())
	}
	m.mu.Lock()
	_, _, _, err = m.prepareWorkspaceGitLocked(context.Background(), toolsRequest{ServiceInfo: svc.info, Caller: agentCaller(members[0].AgentID, members[0].MembershipID), Args: []string{"fetch"}})
	m.mu.Unlock()
	if err == nil || err.Error() != "managed_repository_required" {
		t.Fatal("managed Git silently used another directory", err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	var response workspaceDirectoryError
	if err := svc.checkWorkspace(members[0].Runner.Profile); !errors.As(err, &response) {
		t.Fatal("Session admission skipped filesystem validation", err)
	}
}

func TestWorkspaceDirectoryPendingOperationKeepsPreviousClaimUntilSettlement(t *testing.T) {
	parallelFixtureTest(t)
	m, root := directoryTestManager(t)
	first, other := managerCreate(t, m, "claim-owner"), managerCreate(t, m, "claim-contender")
	openRoomOK(t, m, first, "initial")
	saved, _ := readManagedRoom(t, m, first)
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	os.Mkdir(a, 0700)
	os.Mkdir(b, 0700)
	applyMemberDirectories(t, m, first, map[string]string{saved.Members[0].MembershipID: a})
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	factory := m.factory
	m.factory = func(dir, id string, c serviceConfig) (*service, error) {
		close(entered)
		<-release
		return factory(dir, id, c)
	}
	saved, _ = readManagedRoom(t, m, first)
	w := *saved.Workspace
	w.MemberDirectories = map[string]string{saved.Members[0].MembershipID: b}
	id, _ := newOpaqueID()
	applyWorkspace(t, m, workspaceApplyRequest{ServiceEpoch: m.epoch, RoomIdentity: first.RoomIdentity, Caller: humanCaller, OperationID: id, ExpectedVersion: saved.WorkspaceVersion, Target: w})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("did not reach post-commit boundary")
	}
	contender, _ := readManagedRoom(t, m, other)
	for _, dir := range []string{a, b} {
		target := *contender.Workspace
		target.MemberDirectories = map[string]string{contender.Members[0].MembershipID: dir}
		requestID, _ := newOpaqueID()
		r := managerHTTP(m, "/v1/workspace/apply", workspaceApplyRequest{ServiceEpoch: m.epoch, RoomIdentity: other.RoomIdentity, Caller: humanCaller, OperationID: requestID, ExpectedVersion: 1, Target: target})
		if r.Code == 200 || !strings.Contains(r.Body.String(), "directory_conflict") {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	unblock()
	if op := awaitWorkspace(t, m); op.Status != "completed" {
		t.Fatal(op)
	}
	m.factory = factory
	applyMemberDirectories(t, m, other, map[string]string{contender.Members[0].MembershipID: a})
}

func TestWorkspaceDirectoryApplyIsLocalEvenWithManagedRepository(t *testing.T) {
	parallelFixtureTest(t)
	m, root := directoryTestManager(t)
	f := newWorkspaceFixture(t)
	m.workspaceRun, m.platformHTTP = f.run, workspaceHTTPFunc(f.http)
	room := managerCreate(t, m, "repository-directory")
	openRoomOK(t, m, room, "initial")
	applyWorkspace(t, m, workspaceRequest(t, m, room, workspaceConfig{Repository: f.config.Workspace.Repository}))
	if op := awaitWorkspace(t, m); op.Status != "completed" {
		t.Fatal(op)
	}
	saved, _ := readManagedRoom(t, m, room)
	// Removing the provider/subprocess paths proves directory changes have no
	// implicit clone/configure/credential refresh dependency.
	m.workspaceRun = func(context.Context, string, map[string]string, string, ...string) ([]byte, error) {
		t.Error("directory change invoked repository command")
		return nil, errors.New("forbidden")
	}
	m.platformProject = func(context.Context, string, string, workspaceSecret) (int64, error) {
		t.Error("directory change queried platform")
		return 0, errors.New("forbidden")
	}
	applyMemberDirectories(t, m, room, map[string]string{saved.Members[0].MembershipID: root})
	after, _ := readManagedRoom(t, m, room)
	if !reflect.DeepEqual(saved.Members, after.Members) || !sameManagedWorkspace(*saved.Workspace, *after.Workspace) {
		t.Fatal("changed repository, role or saved environment")
	}
	applyMemberDirectories(t, m, room, nil)
}

func TestWorkspaceDirectoryModelProfileAndDeletionKeepExternalFiles(t *testing.T) {
	parallelFixtureTest(t)
	m, root := directoryTestManager(t)
	room := managerCreate(t, m, "directory-delete")
	openRoomOK(t, m, room, "initial")
	saved, _ := readManagedRoom(t, m, room)
	member := saved.Members[0]
	marker := filepath.Join(root, "keep.txt")
	os.WriteFile(marker, []byte("outside managed lifecycle"), 0600)
	applyMemberDirectories(t, m, room, map[string]string{member.MembershipID: root})
	m.mu.Lock()
	profile, err := (&modelController{manager: m}).memberProfileLocked(MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID})
	m.mu.Unlock()
	if err != nil || profile.Workspace != root {
		t.Fatal("model profile cwd", profile.Workspace, err)
	}
	// Backups enumerate the managed tree, never follow directory references.
	if entries, err := serviceInventory(m.store.dir); err != nil {
		t.Fatal(err)
	} else {
		for _, entry := range entries {
			if strings.HasSuffix(entry.Path, "keep.txt") {
				t.Fatal("backup traversed external directory")
			}
		}
	}
	m.mu.Lock()
	_, err = m.startDeletion(room.RoomIdentity)
	done := m.runtimes[room.RoomIdentity].operationDone
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deletion did not settle")
	}
	if body, err := os.ReadFile(marker); err != nil || string(body) != "outside managed lifecycle" {
		t.Fatal("deletion touched external directory", err)
	}
	if _, err := os.Stat(m.roomDir(room.RoomID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("managed room was not deleted", err)
	}
}

func TestWorkspaceDirectoryModelChangeUsesEffectiveProfile(t *testing.T) {
	parallelFixtureTest(t)
	m, controller, _ := onlineModelManager(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.workspaceRoots = &[]string{root}
	room := createDirectConversation(t, m, directSelection("directory-model"))
	openRoomOK(t, m, room, "initial")
	saved, _ := readManagedRoom(t, m, room)
	member := saved.Members[0]
	applyMemberDirectories(t, m, room, map[string]string{member.MembershipID: root})
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	m.mu.Lock()
	profile, err := controller.memberProfileLocked(id)
	m.mu.Unlock()
	if err != nil || profile.Workspace != root {
		t.Fatal("current model profile", profile.Workspace, err)
	}
	requireModelComplete(t, modelChange(t, controller, modelChangeInput{Kind: "selection", MemberIdentity: id, Selection: modelSelection{Mode: "explicit", EntryID: "B"}, ExpectedSelectionVersion: 1}))
	if profile := m.runtimes[room.RoomIdentity].service.dshProfiles[member.Runner.Profile]; profile.Workspace != root {
		t.Fatal("model change lost directory", profile.Workspace)
	}
}

func TestWorkspaceDirectoryRotationFreezesManagedModelBeforeNextTurn(t *testing.T) {
	parallelFixtureTest(t)
	m, controller, _ := onlineModelManager(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.workspaceRoots = &[]string{root}
	room := createDirectConversation(t, m, directSelection("directory-model-rotation"))
	svc := openRoomOK(t, m, room, "initial")
	saved, _ := readManagedRoom(t, m, room)
	member := saved.Members[0]
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	opts := adapterOptions(svc.store, id)
	m.mu.Lock()
	profile, err := controller.memberProfileLocked(id)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	old, err := m.config.models.target(id, *member.ModelSelection, member.ModelSelectionVersion, profile)
	if err != nil {
		t.Fatal(err)
	}
	backend := dshBackend{store: svc.store, model: &old}
	ref := sessionReference{Version: formatVersion, MemberIdentity: id, Directory: "session.json.dsh", ManagedModel: true}
	if err := backend.saveModelSnapshot(opts, ref); err != nil {
		t.Fatal(err)
	}
	svc.store.mu.Lock()
	err = svc.store.save(opts.StatePath, ref, maxPrivateFileBytes)
	svc.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dshStateDir(opts.StatePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dshStateDir(opts.StatePath), "backend.json"), []byte(`{"version":1,"sessionId":"closed-fixture","configSHA256":"fixture","stopped":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(filepath.Dir(opts.StatePath), ref.Directory+".model.json")
	oldBytes, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{root, ""} {
		dirs := map[string]string{}
		if directory != "" {
			dirs[id.MembershipID] = directory
		}
		op := applyMemberDirectories(t, m, room, dirs)
		store := m.runtimes[room.RoomIdentity].service.store
		rotation, err := store.loadRotation(id)
		if err != nil || rotation.ModelTarget == nil || rotation.Phase != "completed" {
			t.Fatal("missing durable model rotation target", err, rotation)
		}
		next := workspaceMemberReferences(t, m, room)[id.MembershipID]
		frozen, err := readModelSessionSnapshot(filepath.Join(filepath.Dir(opts.StatePath), next.Directory+".model.json"), id, next.Directory)
		want := directory
		if want == "" {
			want = filepath.Join(store.dir, member.Workspace)
		}
		if err != nil || !next.Pending || !next.ManagedModel || frozen.Profile.Workspace != want || frozen.Generation != "session.json.dsh-"+op.OperationID || frozen.ExecutionDigest != old.ExecutionDigest || frozen.CredentialRef != old.CredentialRef {
			t.Fatal("new pending generation cannot restore its cwd/model", err, next, frozen)
		}
		// A completed receipt must not allocate another snapshot or generation.
		before, _ := os.ReadFile(filepath.Join(filepath.Dir(opts.StatePath), next.Directory+".model.json"))
		retryWorkspaceHTTP(t, m, room, op.OperationID)
		if after, err := os.ReadFile(filepath.Join(filepath.Dir(opts.StatePath), next.Directory+".model.json")); err != nil || !bytes.Equal(before, after) {
			t.Fatal("receipt replay rewrote frozen model", err)
		}
	}
	if after, err := os.ReadFile(oldPath); err != nil || !bytes.Equal(oldBytes, after) {
		t.Fatal("old model/cwd evidence changed", err)
	}
}

func TestWorkspaceDirectoryRequestIdentityVersionAndLocalPolicy(t *testing.T) {
	parallelFixtureTest(t)
	m, root := directoryTestManager(t)
	room := managerCreate(t, m, "directory-admission")
	openRoomOK(t, m, room, "initial")
	saved, _ := readManagedRoom(t, m, room)
	member := saved.Members[0]
	base := *saved.Workspace
	base.MemberDirectories = map[string]string{member.MembershipID: root}
	for _, mutate := range []func(*workspaceApplyRequest){
		func(r *workspaceApplyRequest) { r.Caller = agentCaller(member.AgentID, member.MembershipID) },
		func(r *workspaceApplyRequest) { r.ExpectedVersion = 9 },
		func(r *workspaceApplyRequest) {
			r.Target.MemberDirectories = map[string]string{"stale-membership": root}
		},
		func(r *workspaceApplyRequest) { r.Target.Root = "different-managed-root" },
		func(r *workspaceApplyRequest) {
			r.Target.MemberDirectories = map[string]string{member.MembershipID: filepath.Join(root, "missing")}
		},
	} {
		w, _ := normalizeWorkspace(base, false)
		operationID, _ := newOpaqueID()
		r := workspaceApplyRequest{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, OperationID: operationID, ExpectedVersion: 1, Target: w}
		mutate(&r)
		response := managerHTTP(m, "/v1/workspace/apply", r)
		if response.Code == 200 {
			t.Fatal("invalid request accepted")
		}
	}
	if len(m.index.WorkspaceOperations) != 0 {
		t.Fatal("rejected request reserved operation")
	}
	m.workspaceRoots = &[]string{}
	id, _ := newOpaqueID()
	response := managerHTTP(m, "/v1/workspace/apply", workspaceApplyRequest{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, OperationID: id, ExpectedVersion: 1, Target: base})
	if response.Code == 200 {
		t.Fatal("manual path bypassed disabled roots")
	}
}
