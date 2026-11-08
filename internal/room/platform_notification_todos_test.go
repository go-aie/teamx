package room

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

type todoFetchFunc func(context.Context, gitlab.TodoQuery) (gitlab.TodoBatch, error)

func (f todoFetchFunc) FetchTodos(ctx context.Context, q gitlab.TodoQuery) (gitlab.TodoBatch, error) {
	return f(ctx, q)
}

type todoTestPages struct {
	pending, done []gitlab.Todo
	calls         []gitlab.TodoQuery
	failState     string
	failPage      int
	hook          func()
}

func (p *todoTestPages) FetchTodos(_ context.Context, q gitlab.TodoQuery) (gitlab.TodoBatch, error) {
	p.calls = append(p.calls, q)
	if p.hook != nil {
		p.hook()
	}
	if q.State == p.failState && q.Page == p.failPage {
		return gitlab.TodoBatch{}, errors.New("private query error")
	}
	items := p.pending
	if q.State == "done" {
		items = p.done
	}
	start := min((q.Page-1)*q.PerPage, len(items))
	end := min(start+q.PerPage, len(items))
	b := gitlab.TodoBatch{Version: 1, Todos: append([]gitlab.Todo{}, items[start:end]...), Complete: end == len(items), ResponseBytes: 200}
	if !b.Complete {
		b.NextPage = q.Page + 1
	}
	return b, nil
}

type todoHarness struct {
	t          *testing.T
	m          *roomManager
	room       roomSummary
	clock      *eventTestClock
	pages      *todoTestPages
	runner     *gitlab.TodosRunner
	key        notification.Key
	connection platformConnection
	baseline   time.Time
}

func verifyTodoAccount(t *testing.T, m *roomManager, c platformConnection, id int64) platformConnection {
	t.Helper()
	if c.Validation.Status == "valid" && c.Validation.UserID == id {
		return c
	}
	return authorizeTestConnection(t, m, c, id, "test-platform-secret")
}
func newTodoHarness(t *testing.T) *todoHarness {
	t.Helper()
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	room := managerCreate(t, m, "todos-room")
	c := platformTestSave(t, m, platformTestRequest(m))
	c = verifyTodoAccount(t, m, c, 101)
	clock := &eventTestClock{now: time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)}
	m.notifications.Clock = clock.get
	h := &todoHarness{t: t, m: m, room: room, clock: clock, connection: c, pages: &todoTestPages{}}
	s, err := m.configureTodosSubscription(context.Background(), notification.Key{Owner: room.RoomIdentity, Source: gitlab.TodosSourceID}, 0, gitlab.TodosConfig{Enabled: true, ConnectionID: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	h.key = s.Metadata().Key
	h.baseline = s.Baseline
	h.install()
	h.newRunner()
	return h
}
func (h *todoHarness) install() {
	h.m.notifications.Source(gitlab.TodosSourceID).(*gitlab.TodosSource).Prepare = func(context.Context, string, RoomIdentity, gitlab.TodosQueryScope) (gitlab.TodoFetcher, error) {
		return h.pages, nil
	}
}
func (h *todoHarness) state() gitlab.TodosState {
	h.t.Helper()
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	s, ok := h.m.todosSubscriptionLocked(h.key)
	if !ok {
		h.t.Fatal("missing todo subscription")
	}
	return s
}
func (h *todoHarness) newRunner() {
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	h.runner = h.m.notifications.NewRunnerLocked(h.key).(*gitlab.TodosRunner)
}
func (h *todoHarness) step() (time.Duration, bool) {
	h.clock.add(time.Second)
	return h.runner.Step(context.Background())
}
func (h *todoHarness) full() {
	h.t.Helper()
	calls := len(h.pages.calls)
	for i := 0; i < 200; i++ {
		_, stop := h.step()
		if stop {
			h.t.Fatalf("stopped: %+v", h.state())
		}
		s := h.state()
		if s.Scan == nil && !s.LastFull.IsZero() && (len(h.pages.calls) == calls || h.pages.calls[len(h.pages.calls)-1].State == "done") {
			return
		}
	}
	h.t.Fatal("scan did not complete")
}
func (h *todoHarness) todo(id int64, seconds int, state string) gitlab.Todo {
	return gitlab.Todo{ID: id, RecipientID: 101, ProjectID: 7, Project: "group/project", Action: "review_requested", Reason: "review_requested", Kind: "merge_request", TargetType: "MergeRequest", TargetID: 70, TargetIID: 42, Actor: &gitlab.TodoActor{ID: 2, Username: "alice"}, Title: "review needed", URL: "https://gitlab.invalid/group/project/-/merge_requests/42", CreatedAt: h.baseline.Add(time.Duration(seconds) * time.Second), State: state}
}
func (h *todoHarness) reopen() {
	h.t.Helper()
	root := h.m.store.dir
	if err := h.m.close(); err != nil {
		h.t.Fatal(err)
	}
	h.m = testManager(h.t, root, managerTestProfiles(h.t))
	h.m.notifications.Clock = h.clock.get
	h.install()
	h.newRunner()
}

type todoConflictHost struct {
	notification.StepHost
	beforeCommit func(notification.State)
}

func (h *todoConflictHost) Commit(s notification.State) error {
	if h.beforeCommit != nil {
		h.beforeCommit(s)
	}
	return h.StepHost.Commit(s)
}
