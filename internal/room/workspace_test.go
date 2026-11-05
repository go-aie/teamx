package room

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func workspaceGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}

type workspaceFixture struct {
	p            workspacePreparer
	dir, source  string
	config       managedRoomConfig
	calls        []string
	failClone    bool
	projectError bool
}

func newWorkspaceFixture(t *testing.T) *workspaceFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GITLAB_PAT", "offline-pat")
	f := &workspaceFixture{dir: t.TempDir(), source: t.TempDir()}
	workspaceGit(t, f.source, "init", "--initial-branch=main")
	workspaceGit(t, f.source, "config", "user.name", "Source")
	workspaceGit(t, f.source, "config", "user.email", "source@example.test")
	os.WriteFile(filepath.Join(f.source, "README"), []byte("initial\n"), 0600)
	workspaceGit(t, f.source, "add", "README")
	workspaceGit(t, f.source, "commit", "-m", "initial")
	f.config = managedRoomConfig{WorkspaceVersion: 1, Workspace: &workspaceConfig{Root: "workspaces/repos/stable", Repository: &workspaceRepository{URL: "https://gitlab.invalid/group/project.git", Branch: "main"}}, RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: "generation"}}
	for _, id := range []string{"member-one", "member-two"} {
		f.config.Members = append(f.config.Members, managedMember{MemberState: MemberState{MembershipID: id}, Workspace: workspaceMemberPath(*f.config.Workspace, id)})
	}
	f.p = workspacePreparer{run: f.run, transport: workspaceHTTPFunc(f.http), remove: func(name string) error {
		if err := os.Remove(name); err != nil {
			return err
		}
		return syncDirectory(filepath.Dir(name))
	}, write: func(name string, v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return os.WriteFile(name, data, 0600)
	}}
	return f
}

func (f *workspaceFixture) run(ctx context.Context, dir string, env map[string]string, program string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, program+" "+strings.Join(args, " "))
	for _, arg := range args {
		if arg == "clone" {
			target := args[len(args)-1]
			if f.failClone {
				os.MkdirAll(target, 0700)
				os.WriteFile(filepath.Join(target, "partial"), []byte("evidence"), 0600)
				return nil, errors.New("offline clone failure")
			}
			local := append([]string(nil), args...)
			local[len(local)-2] = f.source
			data, err := runPreparation(ctx, dir, env, program, local...)
			if err != nil {
				return nil, err
			}
			_, err = runPreparation(ctx, target, env, "git", "config", "remote.origin.url", args[len(args)-2])
			return data, err
		}
	}
	return runPreparation(ctx, dir, env, program, args...)
}
func (f *workspaceFixture) ensure(initial bool) (preparedWorkspaces, error) {
	return f.p.ensure(context.Background(), f.dir, f.config, initial)
}
func (f *workspaceFixture) member(i int) string {
	return filepath.Join(f.dir, filepath.FromSlash(f.config.Members[i].Workspace))
}

func TestWorkspaceGitLabBrowserURLUsesCanonicalCloneURL(t *testing.T) {
	for name, bound := range map[string]bool{"git-only": false, "gitlab": true} {
		t.Run(name, func(t *testing.T) {
			f := newWorkspaceFixture(t)
			browserURL := "https://gitlab.invalid/group/project"
			f.config.Workspace.Repository.URL = browserURL
			cloneURL := browserURL
			if bound {
				f.config.Workspace.Binding = testGitLabWorkspace().Binding
				cloneURL += ".git"
			}
			run := f.p.run
			clones := 0
			f.p.run = func(ctx context.Context, dir string, env map[string]string, program string, args ...string) ([]byte, error) {
				if slices.Contains(args, "clone") {
					clones++
					if args[len(args)-2] != cloneURL || !slices.Contains(args, "http.followRedirects=false") {
						t.Fatalf("unexpected clone command: %v", args)
					}
				}
				return run(ctx, dir, env, program, args...)
			}
			if _, err := f.ensure(true); err != nil {
				t.Fatal(err)
			}
			if clones != len(f.config.Members) || f.config.Workspace.Repository.URL != browserURL {
				t.Fatal("clone count or saved configuration changed")
			}
			for i := range f.config.Members {
				if workspaceGit(t, f.member(i), "remote", "get-url", "origin") != cloneURL {
					t.Fatal("unexpected origin")
				}
			}
			f.calls = nil
			if _, err := f.ensure(false); err != nil {
				t.Fatal(err)
			}
			for _, call := range f.calls {
				if strings.Contains(call, " clone ") || strings.HasPrefix(call, "api ") {
					t.Fatal("revisit performed remote work", call)
				}
			}
			// Previously created origins remain valid; unrelated projects do not.
			workspaceGit(t, f.member(0), "remote", "set-url", "origin", browserURL)
			if _, err := f.ensure(false); err != nil {
				t.Fatal(err)
			}
			workspaceGit(t, f.member(0), "remote", "set-url", "--push", "origin", "https://gitlab.invalid/other/project.git")
			if _, err := f.ensure(false); err == nil {
				t.Fatal("accepted a different push project")
			}
		})
	}
}

func TestWorkspaceIndependentClonesPreserveOldFilesAndRevisit(t *testing.T) {
	f := newWorkspaceFixture(t)
	old := filepath.Join(f.dir, "workspaces", f.config.Members[0].MembershipID)
	os.MkdirAll(old, 0700)
	os.WriteFile(filepath.Join(old, "discussion.txt"), []byte("old files"), 0600)
	result, err := f.ensure(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ToolEnv) != 0 || result.Instructions != "" {
		t.Fatal("Git-only workspace enabled a platform")
	}
	for i := range f.config.Members {
		if workspaceGit(t, f.member(i), "branch", "--show-current") != "main" {
			t.Fatal("initial branch")
		}
	}
	if data, _ := os.ReadFile(filepath.Join(old, "discussion.txt")); string(data) != "old files" {
		t.Fatal("old files lost")
	}
	workspaceGit(t, f.member(0), "checkout", "-b", "task")
	os.WriteFile(filepath.Join(f.member(0), "README"), []byte("dirty\n"), 0600)
	before := workspaceGit(t, f.member(0), "status", "--porcelain")
	f.calls = nil
	if _, err = f.ensure(false); err != nil {
		t.Fatal(err)
	}
	if before != workspaceGit(t, f.member(0), "status", "--porcelain") || workspaceGit(t, f.member(0), "branch", "--show-current") != "task" {
		t.Fatal("revisit changed branch or files")
	}
	for _, call := range f.calls {
		if strings.Contains(call, "clone") || strings.Contains(call, "fetch") || strings.Contains(call, "checkout") || strings.HasPrefix(call, "api ") {
			t.Fatal("revisit did remote work", call)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(f.member(1), "README")); string(data) != "initial\n" {
		t.Fatal("clones share files")
	}
}

func TestWorkspaceRejectsUnsafeOrIncompleteTargets(t *testing.T) {
	for _, kind := range []string{"foreign-origin", "foreign-push", "non-repository", "partial-git", "pending-clone", "missing-on-visit", "symlink", "overlap"} {
		t.Run(kind, func(t *testing.T) {
			f := newWorkspaceFixture(t)
			initial := true
			switch kind {
			case "foreign-origin":
				if _, err := f.ensure(true); err != nil {
					t.Fatal(err)
				}
				workspaceGit(t, f.member(0), "remote", "set-url", "origin", "https://gitlab.invalid/other/project.git")
			case "foreign-push":
				if _, err := f.ensure(true); err != nil {
					t.Fatal(err)
				}
				workspaceGit(t, f.member(0), "config", "remote.origin.pushurl", "https://gitlab.invalid/other/project.git")
			case "non-repository":
				os.MkdirAll(f.member(0), 0700)
				os.WriteFile(filepath.Join(f.member(0), "keep"), []byte("evidence"), 0600)
			case "partial-git":
				os.MkdirAll(filepath.Join(f.member(0), ".git"), 0700)
			case "pending-clone":
				os.MkdirAll(filepath.Dir(f.member(0)), 0700)
				os.WriteFile(f.member(0)+".clone-pending", []byte("{}"), 0600)
			case "missing-on-visit":
				initial = false
			case "symlink":
				os.MkdirAll(filepath.Dir(f.member(0)), 0700)
				os.Symlink(t.TempDir(), f.member(0))
			case "overlap":
				f.config.Members[1] = f.config.Members[0]
			}
			f.calls = nil
			if _, err := f.ensure(initial); err == nil {
				t.Fatal("unsafe target accepted")
			}
			for _, call := range f.calls {
				if strings.Contains(call, " clone ") || strings.HasPrefix(call, "api ") {
					t.Fatal("mutated before all local checks", call)
				}
			}
			if kind == "non-repository" {
				if data, _ := os.ReadFile(filepath.Join(f.member(0), "keep")); string(data) != "evidence" {
					t.Fatal("lost evidence")
				}
			}
		})
	}
}

func TestWorkspaceCloneFailureRetainsEvidenceAndBlocksReplay(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.failClone = true
	if _, err := f.ensure(true); err == nil {
		t.Fatal("expected clone failure")
	}
	f.failClone = false
	f.calls = nil
	if _, err := f.ensure(true); err == nil {
		t.Fatal("overwrote failed clone")
	}
	if len(f.calls) != 0 {
		t.Fatal(f.calls)
	}
	if data, _ := os.ReadFile(filepath.Join(f.member(0), "partial")); string(data) != "evidence" {
		t.Fatal("lost partial clone")
	}
	// Even an empty directory after Git's cleanup retains the failed receipt.
	os.Remove(filepath.Join(f.member(0), "partial"))
	if _, err := f.ensure(true); err == nil {
		t.Fatal("replayed cleaned-up failed clone")
	}
}

func TestWorkspaceSupplementGitLabAndFrozenResources(t *testing.T) {
	f := newWorkspaceFixture(t)
	if _, err := f.ensure(true); err != nil {
		t.Fatal(err)
	}
	workspaceGit(t, f.member(0), "checkout", "-b", "task")
	os.WriteFile(filepath.Join(f.member(0), "README"), []byte("dirty\n"), 0600)
	workspaceGit(t, f.member(0), "config", "user.name", "Existing Author")
	workspaceGit(t, f.member(0), "config", "user.email", "existing@example.test")
	f.config.Workspace.Binding = testGitLabWorkspace().Binding
	f.calls = nil
	result, err := f.ensure(true)
	if err != nil {
		t.Fatal(err)
	}
	if workspaceGit(t, f.member(0), "branch", "--show-current") != "task" || workspaceGit(t, f.member(0), "config", "user.name") != "Existing Author" {
		t.Fatal("supplement changed branch/author")
	}
	if workspaceGit(t, f.member(1), "config", "user.name") != "Bound User" {
		t.Fatal("did not fill missing author")
	}
	if len(result.ToolEnv) == 0 || !strings.Contains(result.Instructions, "tools run gitlab --help") {
		t.Fatal(result)
	}
	for _, call := range f.calls {
		if strings.Contains(call, " clone ") {
			t.Fatal("supplement cloned again")
		}
	}
	tools := result.ToolEnv["TEAMX_GITLAB_TOOLS"]
	for _, file := range []string{"git-askpass.sh", "USAGE.md", "binding.json"} {
		if _, err := os.Stat(filepath.Join(tools, file)); err != nil {
			t.Fatal(err)
		}
	}
	// Revisit requires current resources and never replaces unexpected bytes.
	usage := filepath.Join(tools, "USAGE.md")
	current, err := os.ReadFile(usage)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(usage, []byte("frozen instructions"), 0600)
	if _, err := f.ensure(false); err == nil {
		t.Fatal("accepted outdated resources")
	}
	if got, err := os.ReadFile(usage); err != nil || string(got) != "frozen instructions" {
		t.Fatal("rewrote unexpected resource", err)
	}
	if err := os.WriteFile(usage, current, 0600); err != nil {
		t.Fatal(err)
	}
	// Current resources reopen without an API query; writes still need credentials.
	f.calls = nil
	t.Setenv("GITLAB_PAT", "")
	visited, err := f.ensure(false)
	if err != nil || visited.Instructions != string(current) {
		t.Fatal(visited, err)
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "api ") {
			t.Fatal("revisit queried GitLab", call)
		}
	}
	os.RemoveAll(f.member(1))
	if _, err := f.ensure(false); err == nil {
		t.Fatal("recreated missing bound member during revisit")
	}
}

func TestWorkspacePlatformFailureAndCredentialOwnership(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.config.Workspace.Binding = testGitLabWorkspace().Binding
	f.projectError = true
	if _, err := f.ensure(true); err == nil {
		t.Fatal("platform failure ignored")
	}
	if _, err := os.Stat(f.member(0)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cloned after platform failure", err)
	}
	f.projectError = false
	b := f.config.Workspace.Binding
	b.TokenEnv = ""
	b.CredentialID = strings.Repeat("a", 32)
	f.p.credential = func(owner RoomIdentity, id string) (workspaceSecret, error) { return workspaceSecret{}, ErrIdentity }
	if _, err := f.ensure(true); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
}

func TestWorkspaceProviderWithoutSoftwareTeam(t *testing.T) {
	// Only a custom team.json and inline role exist; the working directory has
	// not the shipped .teamx/teams/software-development template.
	root, teams := t.TempDir(), t.TempDir()
	team := testTeamDefinition(t)
	writeTestTeam(t, teams, team)
	m, err := newTeamRoomManager(root, teams, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	summary := createTeamRoom(t, m, "custom-provider")
	saved, _ := readManagedRoom(t, m, summary)
	if len(saved.Members) != 1 || saved.TeamID == "software-development" {
		t.Fatal(saved)
	}
	f := newWorkspaceFixture(t)
	saved.Workspace = f.config.Workspace
	saved.Workspace.Binding = testGitLabWorkspace().Binding
	credentialID, err := m.saveWorkspaceCredential(summary.RoomIdentity, "offline-room-secret")
	if err != nil {
		t.Fatal(err)
	}
	saved.Workspace.Binding.TokenEnv = ""
	saved.Workspace.Binding.CredentialID = credentialID
	f.p.credential = m.loadWorkspaceCredential
	f.p.credentialDir = filepath.Join(m.store.dir, "credentials")
	for i := range saved.Members {
		saved.Members[i].Workspace = workspaceMemberPath(*saved.Workspace, saved.Members[i].MembershipID)
	}
	previous, _ := os.Getwd()
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(previous)
	result, err := f.p.ensure(context.Background(), m.roomDir(summary.RoomID), saved, true)
	if err != nil {
		t.Fatal(err)
	}
	// Resolve the actual private credential reference with the Go reader; the
	// HTTP fixtures above exercised project/author preparation without Python.
	b, err := gitlab.ReadWorkspaceBinding(filepath.Join(result.ToolEnv["TEAMX_GITLAB_TOOLS"], "binding.json"))
	if err != nil {
		t.Fatal(err)
	}
	secret, err := gitlab.WorkspaceCredential(context.Background(), b, os.Getenv, nil)
	if err != nil || secret.Reveal() != "offline-room-secret" {
		t.Fatal("Go credential reference reader failed", err)
	}

	if !strings.Contains(result.Instructions, "tools list") {
		t.Fatal(result)
	}
}

func TestWorkspaceOrdinaryDirectoryAndActivationRecovery(t *testing.T) {
	m := activationTestManager(t)
	target := managerCreate(t, m, "missing-plain")
	saved, _ := readManagedRoom(t, m, target)
	before := saved
	dir := filepath.Join(m.roomDir(target.RoomID), filepath.FromSlash(saved.Members[0].Workspace))
	os.RemoveAll(dir)
	openRoomOK(t, m, target, "restore-directory")
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatal("did not create missing ordinary directory", err)
	}
	after, _ := readManagedRoom(t, m, target)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("preparation rewrote configuration")
	}
}

func TestPreparationBoundedOutputFailureAndTimeout(t *testing.T) {
	for _, code := range []string{"import sys; print('secret',file=sys.stderr); sys.exit(1)", "print('x'*100000)", "import time; time.sleep(10)"} {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		data, err := runPreparation(ctx, t.TempDir(), nil, "python3", "-c", code)
		cancel()
		if err == nil || len(data) != 0 || strings.Contains(err.Error(), "secret") {
			t.Fatal("unsafe preparation result", len(data), err)
		}
	}
}

var _ platform.Execute = runPreparation

func TestPreparationStopsChildAfterParentExit(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "late-write")
	code := `import subprocess, sys
subprocess.Popen([sys.executable,"-c","import time; from pathlib import Path; time.sleep(.6); Path("+repr(sys.argv[1])+").write_text('escaped')"],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)`
	_, _ = runPreparation(context.Background(), dir, nil, "python3", "-c", code, marker)
	time.Sleep(800 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preparation child survived parent", err)
	}
}

func TestWorkspaceReturnRejectsChangedAuthenticationWithoutRemote(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.config.Workspace.Binding = testGitLabWorkspace().Binding
	if _, err := f.ensure(true); err != nil {
		t.Fatal(err)
	}
	workspaceGit(t, f.member(0), "config", "--replace-all", "credential.helper", "store")
	f.calls = nil
	if _, err := f.ensure(false); err == nil {
		t.Fatal("accepted a different credential helper")
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "api ") {
			t.Fatal("authentication check queried remote")
		}
	}
	workspaceGit(t, f.member(0), "config", "--replace-all", "credential.helper", "")
	workspaceGit(t, f.member(0), "config", "--replace-all", "core.askPass", "/other/room/askpass")
	if _, err := f.ensure(false); err == nil {
		t.Fatal("accepted another room's askpass")
	}
}

func TestWorkspaceExistingAuthorsAvoidUserQuery(t *testing.T) {
	f := newWorkspaceFixture(t)
	if _, err := f.ensure(true); err != nil {
		t.Fatal(err)
	}
	for i := range f.config.Members {
		workspaceGit(t, f.member(i), "config", "user.name", "Local Author")
		workspaceGit(t, f.member(i), "config", "user.email", "local@example.test")
	}
	f.config.Workspace.Binding = testGitLabWorkspace().Binding
	f.calls = nil
	if _, err := f.ensure(true); err != nil {
		t.Fatal(err)
	}
	for _, call := range f.calls {
		if strings.HasSuffix(call, " user") || strings.Contains(call, "personal_access_tokens") {
			t.Fatal("unnecessary user/token query", call)
		}
	}
}

func TestPreparationTimeoutStopsChild(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "late-write")
	code := `import subprocess, sys, time
subprocess.Popen([sys.executable,"-c","import time; from pathlib import Path; time.sleep(.6); Path("+repr(sys.argv[1])+").write_text('escaped')"],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
time.sleep(10)`
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := runPreparation(ctx, dir, nil, "python3", "-c", code, marker); err == nil {
		t.Fatal("timeout was reported as success")
	}
	time.Sleep(800 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preparation child survived timeout", err)
	}
}

type workspaceHTTPFunc func(*http.Request) (*http.Response, error)

func (f workspaceHTTPFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func (f *workspaceFixture) http(r *http.Request) (*http.Response, error) {
	f.calls = append(f.calls, "api "+r.URL.Path)
	if r.Header.Get("PRIVATE-TOKEN") == "" {
		return nil, errors.New("missing private credential")
	}
	body := `{"id":7,"path_with_namespace":"group/project","http_url_to_repo":"https://gitlab.invalid/group/project.git"}`
	if r.URL.Path == "/api/v4/user" {
		body = `{"name":"Bound User","email":"bound@example.test"}`
	} else if f.projectError {
		return nil, errors.New("offline platform unavailable")
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}
