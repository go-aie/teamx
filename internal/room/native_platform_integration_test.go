//go:build integration || e2e

package room

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func TestProductionNotificationsUseGoHTTPWithoutWorkspace(t *testing.T) {
	parallelFixtureTest(t)
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
	parallelFixtureTest(t)
	h, _ := newIndependentEventHarness(t)

	platforms := platformTestList(t, h.m)
	req := platformTestRequest(h.m)
	req.Version = platforms[0].Version
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
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &credentialDriftBody{Reader: strings.NewReader("[]"), onRead: func() { changeTestPlatformRevision(t, h.m, platforms[0].ID) }}}, nil
	})
	if _, err := h.m.eventsRead(b)(context.Background(), "/projects/7/events", nil, nil, 1024); !errors.Is(err, ErrConflict) {
		t.Fatal("late response accepted after credential change", err)
	}
}
