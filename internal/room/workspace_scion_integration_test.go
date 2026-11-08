//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestScionWorkspaceApplyThreeMembersAndLocalRevisit(t *testing.T) {
	t.Setenv("SCION_TOKEN", "must-not-inherit")
	t.Setenv("MODEL_API_KEY", "must-not-inherit")
	m, room, c, dir := scionWorkspaceManager(t)
	req := scionWorkspaceRequest(m, room, c)
	applyWorkspace(t, m, req)
	op := awaitWorkspace(t, m)
	if op.Status != "completed" {
		t.Fatal(op.Status, op.Phase, op.ErrorCode)
	}
	saved, _ := readManagedRoom(t, m, room)
	if len(saved.Members) != 3 || saved.Workspace.Repository.Branch != "main" || saved.Workspace.Binding.ProjectID != 0 || len(op.SessionsUpdated) != 3 {
		t.Fatal(saved.Workspace, op)
	}
	for _, member := range saved.Members {
		if member.Workspace != workspaceMemberPath(*saved.Workspace, member.MembershipID) {
			t.Fatal(member)
		}
	}
	// Receipt lookup does not re-query a remote and does not copy clone authority
	// into a member's independent CLI-tool bindings.
	before, _ := os.ReadFile(filepath.Join(dir, "calls"))
	applyWorkspace(t, m, req)
	after, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if string(after) != string(before) {
		t.Fatal("duplicate queried remote")
	}
	for _, member := range saved.Members {
		target := filepath.Join(m.roomDir(room.RoomID), member.Workspace)
		data, _ := os.ReadFile(filepath.Join(target, ".scion/state.json"))
		var state map[string]any
		json.Unmarshal(data, &state)
		state["ref"] = "task/independent"
		state["detached"] = true
		data, _ = json.Marshal(state)
		os.WriteFile(filepath.Join(target, ".scion/state.json"), data, 0600)
		os.WriteFile(filepath.Join(target, "task.txt"), []byte(member.MembershipID), 0600)
		for k := range member.ToolEnv {
			if strings.HasPrefix(k, "SCION_") {
				t.Fatal("private runtime leaked", k)
			}
		}
	}
	// Revisit must work offline with logged-out credentials, preserving dirty and
	// detached state. Restored config is not rewritten or reinitialized.
	scionWriteCredential(t, m, c.CLIProfile.Profile, "bob")
	configBefore, _ := json.Marshal(saved)
	if _, err := m.ensureWorkspaces(context.Background(), saved, false); err != nil {
		t.Fatal(err)
	}
	current, _ := readManagedRoom(t, m, room)
	configAfter, _ := json.Marshal(current)
	if string(configBefore) != string(configAfter) {
		t.Fatal("revisit changed snapshot")
	}
	after, _ = os.ReadFile(filepath.Join(dir, "calls"))
	if string(after) != string(before) {
		t.Fatal("revisit queried platform")
	}
	count, _ := os.ReadFile(filepath.Join(dir, "clones"))
	if string(count) != "3" {
		t.Fatal(string(count))
	}
	for _, member := range saved.Members {
		data, _ := os.ReadFile(filepath.Join(m.roomDir(room.RoomID), member.Workspace, "task.txt"))
		if string(data) != member.MembershipID {
			t.Fatal("shared directory")
		}
	}
}

func TestScionWorkspaceRefAndIdentityPreflight(t *testing.T) {
	parallelFixtureTest(t)
	for _, mode := range []string{"empty", "wrong", "missing-branch", "changed-account"} {
		t.Run(mode, func(t *testing.T) {
			m, room, c, dir := scionWorkspaceManager(t)
			req := scionWorkspaceRequest(m, room, c)
			switch mode {
			case "missing-branch":
				req.Target.Repository.Branch = "typo"
			case "changed-account":
				scionWriteCredential(t, m, c.CLIProfile.Profile, "bob")
			default:
				os.WriteFile(filepath.Join(dir, "mode"), []byte(mode), 0600)
			}
			before, _ := readManagedRoom(t, m, room)
			r := managerHTTP(m, "/v1/workspace/apply", req)
			if r.Code == 200 {
				t.Fatal("accepted", mode)
			}
			after, _ := readManagedRoom(t, m, room)
			if !reflect.DeepEqual(before, after) || len(m.index.WorkspaceOperations) != 0 {
				t.Fatal("preflight mutated state")
			}
			if _, err := os.Stat(filepath.Join(dir, "clones")); !os.IsNotExist(err) {
				t.Fatal("clone started")
			}
		})
	}
}

func TestScionWorkspacePartialFailureExplicitRetry(t *testing.T) {
	parallelFixtureTest(t)
	m, room, c, dir := scionWorkspaceManager(t)
	os.WriteFile(filepath.Join(dir, "mode"), []byte("partial"), 0600)
	req := scionWorkspaceRequest(m, room, c)
	applyWorkspace(t, m, req)
	failed := awaitWorkspace(t, m)
	if failed.Status != "failed" || failed.ConfigCommitted {
		t.Fatal(failed.Status)
	}
	first := filepath.Join(m.roomDir(room.RoomID), failed.Members[0].Workspace)
	second := filepath.Join(m.roomDir(room.RoomID), failed.Members[1].Workspace)
	if _, err := os.Stat(second); !os.IsNotExist(err) {
		t.Fatal("failed clone target exists")
	}
	marker := second + ".clone-pending"
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(first, "keep.txt"), []byte("completed member"), 0600)
	retryWorkspaceHTTP(t, m, room, req.OperationID)
	again := awaitWorkspace(t, m)
	if again.Status != "failed" {
		t.Fatal("uninspected marker replayed")
	}
	count, _ := os.ReadFile(filepath.Join(dir, "clones"))
	if string(count) != "2" {
		t.Fatal("cloned on unchecked retry")
	}
	// A normal manager restart preserves the failed receipt and does not replay
	// missing targets, even if the native CLI removed its incomplete directory.
	root := m.store.dir
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	m = testManager(t, root, managerTestProfiles(t))
	m.config = serviceConfig{endpoint: "http://127.0.0.1:1", cliPath: os.Args[0]}
	m.factory = controlledActivationFactory(nil, nil)
	status := queryWorkspace(t, m, room, req.OperationID)
	if status.Operation.Status != "failed" || status.Version != 1 {
		t.Fatal("restart adopted failed target")
	}
	count, _ = os.ReadFile(filepath.Join(dir, "clones"))
	if string(count) != "2" {
		t.Fatal("restart replayed clone")
	}
	// Simulate the operator inspecting the missing target and removing only its
	// marker. A second explicit retry reuses the original operation and first copy.
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "mode"), []byte("ok"), 0600)
	retryWorkspaceHTTP(t, m, room, req.OperationID)
	done := awaitWorkspace(t, m)
	if done.Status != "completed" || done.OperationID != failed.OperationID {
		t.Fatal(done.Status, done.Phase, done.ErrorCode)
	}
	data, _ := os.ReadFile(filepath.Join(first, "keep.txt"))
	if string(data) != "completed member" {
		t.Fatal("first member replaced")
	}
	count, _ = os.ReadFile(filepath.Join(dir, "clones"))
	if string(count) != "4" {
		t.Fatal("wrong clone count", string(count))
	}
}

func TestScionWorkspaceCloneCancelSettles(t *testing.T) {
	parallelFixtureTest(t)
	for _, mode := range []string{"operation-cancel", "profile-rotation"} {
		t.Run(mode, func(t *testing.T) {
			m, room, c, dir := scionWorkspaceManager(t)
			os.WriteFile(filepath.Join(dir, "mode"), []byte("block"), 0600)
			req := scionWorkspaceRequest(m, room, c)
			applyWorkspace(t, m, req)
			deadline := time.Now().Add(5 * time.Second)
			var private string
			for time.Now().Before(deadline) {
				if data, err := os.ReadFile(filepath.Join(dir, "started")); err == nil && len(data) > 0 {
					private = string(data)
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if private == "" {
				t.Fatal("clone did not start")
			}
			if mode == "operation-cancel" {
				m.mu.Lock()
				rt := m.runtimes[room.RoomIdentity]
				rt.operationCancel()
				m.mu.Unlock()
			} else {
				profile := *c.CLIProfile
				profile.Profile = strings.Repeat("e", 32)
				scionWriteCredential(t, m, profile.Profile, "alice")
				rotated, err := m.updatePlatformConnection(platformConnectionRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: c.ID, Version: c.Version, Name: c.Name, Provider: c.Provider, URL: c.URL, CLIProfile: &profile, ReferencedRooms: []RoomIdentity{room.RoomIdentity}}, false)
				if err != nil || rotated.CredentialVersion <= c.CredentialVersion {
					t.Fatal("rotation", err)
				}
			}
			op := awaitWorkspace(t, m)
			if op.Status != "failed" || op.Preparation != nil || op.ConfigCommitted {
				t.Fatal("unsettled clone", op.Status, op.Preparation)
			}
			if _, err := os.Stat(private); !os.IsNotExist(err) {
				t.Fatal("private credentials remain")
			}
			m.mu.Lock()
			calls := len(m.platformCalls)
			m.mu.Unlock()
			if calls != 0 {
				t.Fatal("platform call remains")
			}
		})
	}
}

func TestScionWorkspaceConfigCompatibility(t *testing.T) {
	parallelFixtureTest(t)
	m, room, c, _ := scionWorkspaceManager(t)
	base := scionWorkspaceRequest(m, room, c).Target
	valid := base
	valid.Repository = &workspaceRepository{Kind: "scion", URL: base.Repository.URL, Branch: "main"}
	b := *base.Binding
	b.ConnectionGeneration = strings.Repeat("a", 32)
	valid.Binding = &b
	if _, err := normalizeWorkspace(valid, false); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*workspaceConfig){
		func(w *workspaceConfig) { w.Repository.Kind = "unknown" },
		func(w *workspaceConfig) { w.Repository.Kind = "git" },
		func(w *workspaceConfig) { w.Repository.Kind = "" },
		func(w *workspaceConfig) { w.Binding = nil },
		func(w *workspaceConfig) { w.Binding.Provider = "gitlab" },
		func(w *workspaceConfig) { w.Binding.ProjectID = 7 },
		func(w *workspaceConfig) { w.Binding.Project = "wrong/repo" },
		func(w *workspaceConfig) { w.Repository.Branch = "" },
	} {
		data, _ := json.Marshal(valid)
		var w workspaceConfig
		json.Unmarshal(data, &w)
		change(&w)
		if _, err := normalizeWorkspace(w, false); err == nil {
			t.Fatal("invalid Scion configuration accepted", w)
		}
	}
	git := workspaceConfig{Root: "workspaces", Repository: &workspaceRepository{URL: "https://git.test/org/repo.git", Branch: "main"}}
	normalized, err := normalizeWorkspace(git, false)
	if err != nil || normalized.Repository.Kind != "" {
		t.Fatal("kind-less Git changed", err)
	}
}
