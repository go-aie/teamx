package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func TestProductionNotificationsUseGoHTTPWithoutWorkspace(t *testing.T) {
	t.Run("events", func(t *testing.T) {
		h, _ := newIndependentEventHarness(t)
		h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("Authorization") != "Bearer test-platform-secret" {
				t.Error("incorrect credential")
			}
			body := `{"id":7,"path_with_namespace":"remote/project","http_url_to_repo":"https://gitlab.invalid/remote/project.git"}`
			if strings.HasSuffix(r.URL.Path, "/events") {
				body = fmt.Sprintf(`[{"id":9,"project_id":7,"action_name":"opened","target_type":"MergeRequest","target_id":70,"target_iid":42,"target_title":"Go notification","created_at":%q,"author":{"id":2,"username":"alice"}}]`, h.baseline.Add(time.Second).Format(time.RFC3339))
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"X-Next-Page": {""}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		h.m.notifications.Source(gitlab.EventsSourceID).(*gitlab.EventsSource).Prepare = h.m.preparePlatformEvents
		h.newRunner()
		h.prepare()
		h.clock.add(5 * time.Second)
		h.full()
		s := h.state()
		if len(s.Inbox) != 1 || len(s.Observed) != 1 || s.Status == "backoff" {
			t.Fatalf("Go event query did not reach durable inbox: %+v", s)
		}
		h.full()
		if len(h.state().Inbox) != 1 {
			t.Fatal("rescan duplicated event")
		}
	})
	t.Run("todos", func(t *testing.T) {
		h := newTodoHarness(t)
		h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/api/v4/todos" || r.Header.Get("Authorization") != "Bearer test-platform-secret" {
				t.Error("incorrect personal query")
			}
			body := "[]"
			if r.URL.Query().Get("state") == "pending" {
				body = fmt.Sprintf(`[{"id":9,"project":{"id":7,"path_with_namespace":"group/project"},"action_name":"review_requested","target_type":"MergeRequest","target":{"id":70,"iid":42,"title":"Go todo"},"target_url":"https://gitlab.invalid/group/project/-/merge_requests/42","state":"pending","created_at":%q}]`, h.baseline.Format(time.RFC3339))
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"X-Next-Page": {""}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		h.m.notifications.Source(gitlab.TodosSourceID).(*gitlab.TodosSource).Prepare = h.m.preparePlatformTodos
		h.newRunner()
		h.full()
		s := h.state()
		if len(s.Inbox) != 1 || len(s.Observed) != 1 {
			t.Fatalf("Go todo query did not reach durable inbox: %+v", s)
		}
		for _, entry := range s.Inbox {
			if entry.Todo.RecipientID != 101 {
				t.Fatal("lost recipient")
			}
		}
	})
}
func TestNativeReadRejectsCredentialChangeAfterBody(t *testing.T) {
	h, _ := newIndependentEventHarness(t)

	connections := platformTestList(t, h.m)
	req := platformTestRequest(h.m)
	req.Version = connections[0].Version
	req.ReferencedRooms = []RoomIdentity{h.room.RoomIdentity}
	verifyTodoAccount(t, h.m, platformTestReauthorize(t, h.m, req), 101)
	h.m.mu.Lock()
	b, err := h.m.eventsBindingLocked(h.stateUnlocked())
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer rotated-token" {
			t.Error("wrong admitted token")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &credentialDriftBody{Reader: strings.NewReader("[]"), onRead: func() { changeTestConnectionRevision(t, h.m, connections[0].ID) }}}, nil
	})
	if _, err := h.m.eventsRead(b)(context.Background(), "/projects/7/events", nil, nil, 1024); !errors.Is(err, ErrConflict) {
		t.Fatal("late response accepted after credential change", err)
	}
}

// Only used while the manager gate is held.
func (h *eventHarness) stateUnlocked() gitlab.EventsState {
	s, _ := h.m.eventsSubscriptionLocked(h.m.testEventsKeyLocked(h.room.RoomIdentity))
	return s
}

type credentialDriftBody struct {
	io.Reader
	onRead func()
}

func (b *credentialDriftBody) Read(p []byte) (int, error) {
	if b.onRead != nil {
		b.onRead()
		b.onRead = nil
	}
	return b.Reader.Read(p)
}
func (b *credentialDriftBody) Close() error { return nil }

func TestWorkspaceGitCLI(t *testing.T) {
	b := gitlab.Binding{Version: 1, RoomID: "room", RoomInstance: "instance", URL: "https://gitlab.example.com", Project: "group/project", ConnectionID: strings.Repeat("a", 32), ConnectionGeneration: strings.Repeat("b", 32), ProjectID: 7}
	path := filepath.Join(t.TempDir(), "binding.json")
	data, _ := json.Marshal(b)
	os.WriteFile(path, data, 0600)
	var out, stderr bytes.Buffer
	env := func(k string) string {
		if k == "TEAMX_PLATFORM_TOKEN" {
			return "offline-secret"
		}
		return ""
	}
	err := runCLI(context.Background(), []string{"workspace-git", "askpass", "--binding", path, "Password for 'https://oauth2@gitlab.example.com/group/project.git': "}, &out, &stderr, env)
	if err != nil || out.String() != "offline-secret\n" || stderr.Len() != 0 {
		t.Fatal("Go askpass CLI failed", err)
	}
	out.Reset()
	err = runCLI(context.Background(), []string{"workspace-git", "askpass", "--binding", path, "Password for 'https://evil.invalid/group/project.git': "}, &out, &stderr, env)
	if err == nil || out.Len() != 0 {
		t.Fatal("foreign Git target received credentials")
	}
}

// Optional production binary proof: Git invokes the real Go CLI through the
// GIT_ASKPASS executable path. No listening socket or platform is used.
func TestProductionGitAskpassWithoutPythonOrGlab(t *testing.T) {
	bin := os.Getenv("TEAMX_TEST_CLI_BIN")
	if bin == "" {
		t.Skip("set TEAMX_TEST_CLI_BIN to the built TeamX CLI")
	}
	dir := t.TempDir()
	b := gitlab.Binding{Version: 1, RoomID: "room", RoomInstance: "instance", URL: "https://gitlab.example.com", Project: "group/project", ConnectionID: strings.Repeat("a", 32), ConnectionGeneration: strings.Repeat("b", 32), ProjectID: 7}
	data, _ := json.Marshal(b)
	path := filepath.Join(dir, "binding.json")
	os.WriteFile(path, data, 0600)
	// A PATH containing Git alone proves the credential helper cannot use Python
	// or glab, including through a frozen script.
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	search := filepath.Join(dir, "bin")
	os.Mkdir(search, 0700)
	os.Symlink(gitPath, filepath.Join(search, "git"))

	cmd := exec.Command(gitPath, "-c", "credential.helper=", "-c", "credential.useHttpPath=true", "credential", "fill")
	cmd.Env = []string{"PATH=" + search, "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "TEAMX_PLATFORM_TOKEN=offline-secret", "GIT_ASKPASS=" + bin, "TEAMX_GITLAB_TOOLS=" + dir}
	cmd.Stdin = strings.NewReader("protocol=https\nhost=gitlab.example.com\npath=group/project.git\n\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil || !bytes.Contains(output, []byte("username=oauth2\n")) || !bytes.Contains(output, []byte("password=offline-secret\n")) {
		t.Fatal("production Git/Go askpass failed", err, strings.ReplaceAll(stderr.String(), "offline-secret", "[REDACTED]"))
	}
}
