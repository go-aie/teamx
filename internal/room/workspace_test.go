package room

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func (f *workspaceFixture) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = preparationEnvironment(f.env)
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
	env          map[string]string
	calls        []string
	failClone    bool
	projectError bool
}

func newWorkspaceFixture(t *testing.T) *workspaceFixture {
	t.Helper()
	f := newWorkspacePreflightFixture(t)
	f.git(t, f.source, "init", "--initial-branch=main")
	f.git(t, f.source, "config", "user.name", "Source")
	f.git(t, f.source, "config", "user.email", "source@example.test")
	if err := os.WriteFile(filepath.Join(f.source, "README"), []byte("initial\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.git(t, f.source, "add", "README")
	f.git(t, f.source, "commit", "-m", "initial")
	return f
}

func newWorkspacePreflightFixture(t *testing.T) *workspaceFixture {
	t.Helper()
	home := t.TempDir()
	// Own all Git configuration and initialization material. Never mutate the
	// process environment: these fixtures also run alongside other workspaces.
	empty := filepath.Join(home, "empty")
	if err := os.Mkdir(empty, 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, ".gitconfig")
	if err := os.WriteFile(config, []byte("[core]\n\thooksPath = "+filepath.ToSlash(empty)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f := &workspaceFixture{dir: t.TempDir(), source: t.TempDir(), env: map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": home,
		"GIT_CONFIG_GLOBAL": config, "GIT_CONFIG_SYSTEM": os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1", "GIT_ATTR_NOSYSTEM": "1",
		"GIT_TEMPLATE_DIR": empty,
	}}
	f.config = managedRoomConfig{WorkspaceVersion: 1, Workspace: &workspaceConfig{Root: "workspaces/repos/stable", Repository: &workspaceRepository{URL: "https://gitlab.invalid/group/project.git", Branch: "main"}}, RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: "generation"}}
	for _, id := range []string{"member-one", "member-two"} {
		f.config.Members = append(f.config.Members, managedMember{MemberState: MemberState{MembershipID: id}, Workspace: workspaceMemberPath(*f.config.Workspace, id)})
	}
	f.p = workspacePreparer{run: f.run, repository: workspaceRepositoryAdapter, remove: func(name string) error {
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
	access := gitLabWorkspaceAccess{write: f.p.write}
	access.connection = func(owner RoomIdentity, w workspaceConfig) (projectPlatformResolution, error) {
		return projectPlatformResolution{owner: owner, record: managedPlatformRecord{Platform: platform.Platform{ID: w.Binding.PlatformID}, Generation: w.Binding.PlatformGeneration}}, nil
	}
	access.connectionRead = func(projectPlatformResolution) gitlab.ReadAPI {
		return gitlab.NewReadAPI("https://gitlab.invalid", platform.NewSecret("offline-access"), workspaceHTTPFunc(f.http))
	}
	f.p.access = access.prepare
	return f
}

// Preserve the real preparer's credentials and options while pinning home and
// Git configuration to this fixture. Each command receives its own map.
func (f *workspaceFixture) environment(env map[string]string) map[string]string {
	result := maps.Clone(f.env)
	for key, value := range env {
		if _, private := f.env[key]; !private {
			result[key] = value
		}
	}
	return result
}

func (f *workspaceFixture) run(ctx context.Context, dir string, env map[string]string, program string, args ...string) ([]byte, error) {
	env = f.environment(env)
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

var _ platform.Execute = runPreparation

type workspaceHTTPFunc func(*http.Request) (*http.Response, error)

func (f workspaceHTTPFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func (f *workspaceFixture) http(r *http.Request) (*http.Response, error) {
	f.calls = append(f.calls, "api "+r.URL.Path)
	if r.Header.Get("Authorization") == "" {
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
