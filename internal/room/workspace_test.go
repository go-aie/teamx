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
	"strings"
	"testing"

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
