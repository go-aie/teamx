package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

func TestTodosScansPendingDoneAndNeverUsesAnIDCursor(t *testing.T) {
	h := newTodoHarness(t)
	h.pages.pending = []gitlab.Todo{h.todo(99, 0, "pending"), h.todo(2, 0, "pending"), h.todo(7, -1, "pending")}
	h.pages.done = []gitlab.Todo{h.todo(1000, 0, "done")}
	h.full()
	s := h.state()
	if len(s.Inbox) != 3 || len(s.Observed) != 3 || len(h.pages.calls) != 2 || h.pages.calls[0].State != "pending" || h.pages.calls[1].State != "done" {
		t.Fatal(s, h.pages.calls)
	}
	// An item completed between checks, and a known ID became pending again.
	h.pages.pending = []gitlab.Todo{h.todo(1000, 0, "pending")}
	h.pages.done = append(h.pages.done, h.todo(1, 1, "done"))
	h.clock.add(120 * time.Second)
	h.full()
	if s = h.state(); len(s.Inbox) != 4 || len(s.Observed) != 4 || s.SubscriptionID != h.key.SubscriptionID || !s.Baseline.Equal(h.baseline) {
		t.Fatal(s)
	}
	if s.Inbox[gitlab.TodosKey(s.Binding, 1000)].Todo.State != "done" {
		t.Fatal("known todo was re-imported")
	}
}
func TestTodosFailureRestartAndMovingPagesRescanBothStates(t *testing.T) {
	h := newTodoHarness(t)
	for i := 1; i <= 101; i++ {
		h.pages.pending = append(h.pages.pending, h.todo(int64(i), 0, "pending"))
	}
	h.pages.failState, h.pages.failPage = "pending", 2
	h.step()
	s := h.state()
	if len(s.Inbox) != 100 || s.Scan.NextPage != 2 || !s.LastFull.IsZero() {
		t.Fatal(s)
	}
	h.step()
	s = h.state()
	if s.Scan.NextPage != 1 || s.Scan.State != "pending" || !s.LastFull.IsZero() || s.ErrorCode != "query_failed" {
		t.Fatal(s)
	}
	// A newly visible low ID occupies a different offset; recovery cannot resume
	// from the previous numeric page. The old page-one inbox remains committed.
	h.pages.pending = append([]gitlab.Todo{h.todo(999, 0, "pending")}, h.pages.pending...)
	h.pages.failPage = 0
	h.clock.add(10 * time.Second)
	h.reopen()
	h.full()
	if len(h.state().Inbox) != 102 || h.pages.calls[2].Page != 1 {
		t.Fatal(h.state(), h.pages.calls)
	}
	// Restart while done is only partially scanned also restarts pending page one.
	h.clock.add(130 * time.Second)
	h.newRunner()
	h.step()
	if h.state().Scan.State != "pending" {
		t.Fatal("expected pending pagination checkpoint")
	}
	h.reopen()
	h.step()
	if q := h.pages.calls[len(h.pages.calls)-1]; q.State != "pending" || q.Page != 1 {
		t.Fatal(q)
	}
}
func TestTodosInitializationFailurePreservesBaselineAndFilterHistory(t *testing.T) {
	h := newTodoHarness(t)
	h.m.notifications.Source(gitlab.TodosSourceID).(*gitlab.TodosSource).Prepare = func(context.Context, string, RoomIdentity, gitlab.TodosQueryScope) (gitlab.TodoFetcher, error) {
		return nil, errors.New("private initialization")
	}
	h.step()
	s := h.state()
	if !s.Baseline.Equal(h.baseline) || s.Status != "backoff" || !s.LastFull.IsZero() {
		t.Fatal(s)
	}
	cfg := s.Config
	cfg.Filter.Reasons = []string{"mentioned"}
	h.clock.add(10 * time.Second)
	next, err := h.m.configureTodosSubscription(context.Background(), h.key, s.SubscriptionVersion, cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.install()
	h.newRunner()
	h.pages.pending = []gitlab.Todo{h.todo(1, 0, "pending"), h.todo(2, 20, "pending")}
	h.clock.add(25 * time.Second)
	h.full()
	s = h.state()
	if !s.Baseline.Equal(h.baseline) || next.SubscriptionID != s.SubscriptionID || len(s.Inbox) != 1 || len(s.Observed) != 2 || s.Observed[gitlab.TodosKey(s.Binding, 2)].Reason != "reason" {
		t.Fatal(s)
	}
	cfg = s.Config
	cfg.Enabled = false
	disabled, err := h.m.configureTodosSubscription(context.Background(), h.key, s.SubscriptionVersion, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = true
	cfg.Filter.Reasons = nil
	enabled, err := h.m.configureTodosSubscription(context.Background(), h.key, disabled.SubscriptionVersion, cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.newRunner()
	h.full()
	if len(h.state().Inbox) != 1 || !enabled.Baseline.Equal(h.baseline) {
		t.Fatal("config replayed filtered todo")
	}
}
func TestTodosScanBudgetReportsIncompleteAndKeepsCheckpoint(t *testing.T) {
	h := newTodoHarness(t)
	for i := 1; i <= 101; i++ {
		h.pages.pending = append(h.pages.pending, h.todo(int64(i), 0, "pending"))
	}
	h.m.notifications.Limits.SlicePages = 1
	h.step()
	h.step()
	s := h.state()
	if s.Scan == nil || s.Scan.NextPage != 2 || !s.Scan.Limited || s.ErrorCode != "scan_incomplete" || !s.LastFull.IsZero() || len(s.Inbox) != 100 {
		t.Fatal(s)
	}
	calls := len(h.pages.calls)
	h.step()
	if len(h.pages.calls) != calls {
		t.Fatal("budget yield ignored")
	}
	h.clock.add(125 * time.Second)
	h.step()
	if q := h.pages.calls[len(h.pages.calls)-1]; q.Page != 2 || q.State != "pending" {
		t.Fatal("budget yield lost its checkpoint", q)
	}
	// A short ordinary yield continues its checkpoint without discarding page 2.
	h.clock.add(3 * time.Second)
	h.m.notifications.Limits.SlicePages = 20
	h.full()
	if len(h.state().Inbox) != 101 {
		t.Fatal("budget dropped unqueried page")
	}
}
func TestTodosPageCandidatesIncludeConcurrentAcknowledgementsAndMetadata(t *testing.T) {
	h := newTodoHarness(t)
	h.pages.pending = []gitlab.Todo{h.todo(1, 0, "pending")}
	h.step()
	old := h.state()
	h.pages.hook = func() {
		h.m.mu.Lock()
		s, _ := h.m.todosSubscriptionLocked(h.key)
		_ = h.m.notifications.SaveLocked(s.Acknowledge(gitlab.TodosKey(s.Binding, 1)))
		h.m.mu.Unlock()
	}
	h.step()
	if len(h.state().Inbox) != 0 || h.state().LastFull.IsZero() {
		t.Fatal("concurrent ack overwritten")
	}
	h.pages.hook = nil
	cfg := old.Config
	h.clock.add(130 * time.Second)
	h.newRunner()
	h.pages.hook = func() {
		h.pages.hook = nil
		r := platformTestRequest(h.m)
		r.Version = h.connection.Version

		r.Name = "metadata only"
		h.connection = platformTestSave(t, h.m, r)
	}
	before := h.state()
	h.step()
	after := h.state()
	if !after.LastFull.Equal(before.LastFull) || len(after.Inbox) != 0 || after.Scan.NextPage != 1 {
		t.Fatal("accepted stale metadata candidate", after)
	}
	h.full()
	if !h.state().Config.Enabled || cfg.ConnectionID != h.state().Config.ConnectionID {
		t.Fatal("metadata edit changed binding")
	}
}
func TestTodosPageSaveFailureIsAtomicAndCommitUnknownLatches(t *testing.T) {
	for _, mode := range []string{"write", "unknown", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			h := newTodoHarness(t)
			h.pages.pending = nil
			h.step() // pending completes, preparing a done page with no observations
			h.pages.done = []gitlab.Todo{h.todo(1, 0, "done")}

			if mode == "capacity" {
				h.m.notifications.Limits.Observed = 0
			} else {
				ops := h.m.store.ops
				defer func() { h.m.store.ops = ops }()
				if mode == "write" {
					h.m.store.ops.rename = func(string, string) error { return errors.New("write failed") }
				} else {
					h.m.store.ops.syncDir = func(string) error { return errors.New("sync failed") }
				}
			}
			h.step()
			got := h.state()
			if len(got.Inbox) != 0 || len(got.Observed) != 0 || !got.LastFull.IsZero() {
				t.Fatal("partial page published", got)
			}
			if mode == "unknown" {
				if err := h.m.store.check(true); !errors.Is(err, ErrCommitUnknown) {
					t.Fatal("unknown commit not latched", err)
				}
			}
		})
	}
}
func TestTodosStrictEnvelopeAndSnapshotValidation(t *testing.T) {
	h := newTodoHarness(t)
	h.pages.pending = []gitlab.Todo{h.todo(1, 0, "pending")}
	h.full()
	s := h.state()
	data, _ := json.Marshal(s)
	source := h.m.notifications.Source(gitlab.TodosSourceID)
	for _, bad := range []string{string(data) + "{}", strings.Replace(string(data), `"user_id":101`, `"user_id":0`, 1), strings.Replace(string(data), `"retired":false`, `"retired":true`, 1), strings.Replace(string(data), `"state":"pending"`, `"state":"unknown"`, 1), strings.Replace(string(data), `"todo":{`, `"todo":{"unknown":1,`, 1)} {
		if _, err := source.Decode([]byte(bad), h.room.RoomIdentity); err == nil {
			t.Fatal("malformed state accepted")
		}
	}
	h.m.mu.Lock()
	path, err := h.m.notifications.StatePathLocked(h.key)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	root := h.m.store.dir
	if err = h.m.close(); err != nil {
		t.Fatal(err)
	}
	envelope, _ := os.ReadFile(path)
	other := filepath.Join(filepath.Dir(path), "gitlab.todos."+strings.Repeat("f", 32)+".json")
	if err = os.WriteFile(other, envelope, 0600); err != nil {
		t.Fatal(err)
	}
	if m, err := newRoomManager(root, 8); err == nil {
		_ = m.close()
		t.Fatal("filename identity mismatch loaded")
	}
}
func TestTodosPublicHTTPWorksWithoutWorkspaceAndGuardsIdentity(t *testing.T) {
	h := newTodoHarness(t)
	q := url.Values{"service_epoch": {h.m.epoch}, "caller_id": {"operator"}, "room_id": {h.room.RoomID}, "room_instance": {h.room.RoomInstance}}
	response := managerHTTP(h.m, "/v1/platform/notifications?"+q.Encode(), nil)
	var list struct {
		Subscriptions []notificationView `json:"subscriptions"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &list) != nil || len(list.Subscriptions) != 3 || list.Subscriptions[1].RecipientID != 101 || !list.Subscriptions[1].Available || !list.Subscriptions[0].Available || strings.Contains(response.Body.String(), "fingerprint") {
		t.Fatal(response.Code, response.Body.String())
	}
	cfg := gitlab.NotificationConfig{ConnectionID: h.connection.ID, Todos: gitlab.TodoSettings{Enabled: true}}
	request := func() map[string]any {
		return map[string]any{"service_epoch": h.m.epoch, "room_id": h.room.RoomID, "room_instance": h.room.RoomInstance, "version": 0, "caller": humanCaller, "config": cfg}
	}
	for _, change := range []func(map[string]any){func(r map[string]any) { r["version"] = 9 }, func(r map[string]any) { r["channel"] = "todos" }, func(r map[string]any) { r["room_instance"] = "old" }, func(r map[string]any) { r["caller"] = Caller{Kind: "agent", ID: "developer"} }, func(r map[string]any) { r["config"] = map[string]any{"enabled": true, "unknown": 1} }} {
		r := request()
		change(r)
		if got := managerHTTP(h.m, "/v1/platform/gitlab/notifications/configure", r); got.Code == 200 {
			t.Fatal("invalid request accepted")
		}
	}
	if got := managerHTTP(h.m, "/v1/platform/gitlab/notifications/configure", request()); got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	r := request()
	r["version"] = 1
	delete(r, "config")
	if got := managerHTTP(h.m, "/v1/platform/gitlab/notifications/sync", r); got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
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
func TestTodosConflictRetriesPageWithoutRestoringAnAcknowledgedItem(t *testing.T) {
	h := newTodoHarness(t)
	for i := 1; i <= 101; i++ {
		h.pages.pending = append(h.pages.pending, h.todo(int64(i), 0, "pending"))
	}
	h.step()
	wrapper := &todoConflictHost{StepHost: h.runner.Host}
	h.runner.Host = wrapper
	wrapper.beforeCommit = func(candidate notification.State) {
		if candidate.Metadata().Observed != 101 {
			return
		}
		wrapper.beforeCommit = nil
		h.m.mu.Lock()
		s, _ := h.m.todosSubscriptionLocked(h.key)
		err := h.m.notifications.SaveLocked(s.Acknowledge(gitlab.TodosKey(s.Binding, 1)))
		h.m.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	h.step()
	s := h.state()
	if len(s.Inbox) != 99 || len(s.Observed) != 100 || s.Scan.NextPage != 2 {
		t.Fatal("conflicting page advanced", s)
	}
	h.step()
	s = h.state()
	if len(s.Inbox) != 100 || len(s.Observed) != 101 || s.Scan.State != "done" {
		t.Fatal(s)
	}
	if _, ok := s.Inbox[gitlab.TodosKey(s.Binding, 1)]; ok {
		t.Fatal("acknowledged item restored")
	}
}
func TestTodosTimeoutCommitsBackoffWithLifecycleContext(t *testing.T) {
	h := newTodoHarness(t)
	h.m.notifications.Limits.SliceTime = 10 * time.Millisecond
	h.m.notifications.Source(gitlab.TodosSourceID).(*gitlab.TodosSource).Prepare = func(context.Context, string, RoomIdentity, gitlab.TodosQueryScope) (gitlab.TodoFetcher, error) {
		return todoFetchFunc(func(ctx context.Context, _ gitlab.TodoQuery) (gitlab.TodoBatch, error) {
			<-ctx.Done()
			return gitlab.TodoBatch{}, ctx.Err()
		}), nil
	}
	h.step()
	s := h.state()
	if s.Status != "backoff" || s.Metrics.Failures != 1 || s.Metrics.Calls != 1 || s.Scan.State != "pending" || s.Scan.NextPage != 1 {
		t.Fatal("request timeout lost failure commit", s)
	}
}
func TestTodosCapacityRejectsWholePage(t *testing.T) {
	h := newTodoHarness(t)
	h.pages.pending = []gitlab.Todo{h.todo(1, 0, "pending"), h.todo(2, 0, "pending")}
	h.m.notifications.Limits.Inbox = 1
	h.step()
	s := h.state()
	if s.Status != "capacity" || len(s.Inbox) != 0 || len(s.Observed) != 0 || s.Scan.NextPage != 1 || !s.LastFull.IsZero() {
		t.Fatal(s)
	}
}
func TestTodosAccountReplacementRetiresAndKeepsOldProgress(t *testing.T) {
	h := newTodoHarness(t)
	h.pages.pending = []gitlab.Todo{h.todo(1, 0, "pending")}
	h.full()
	old := h.state()
	req := platformTestRequest(h.m)
	req.ID = strings.Repeat("b", 32)
	req.Name = "Other personal account"
	req.URL = "https://other.gitlab.invalid"

	other := verifyTodoAccount(t, h.m, platformTestSave(t, h.m, req), 102)
	cfg := old.Config
	cfg.ConnectionID = other.ID
	h.clock.add(time.Minute)
	next, err := h.m.configureTodosSubscription(context.Background(), h.key, old.SubscriptionVersion, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if next.SubscriptionID == old.SubscriptionID || !next.Baseline.After(old.Baseline) || len(next.Inbox) != 0 || len(next.Observed) != 0 || next.Binding.UserID != 102 || next.Binding.URL != other.URL {
		t.Fatal(next)
	}
	retired := h.state()
	if !retired.Retired || retired.Config.Enabled || len(retired.Inbox) != 1 || len(retired.Observed) != 1 || !retired.Baseline.Equal(old.Baseline) {
		t.Fatal(retired)
	}
	if err = h.m.notifications.Deliver(context.Background(), h.key, retired.SubscriptionVersion); !errors.Is(err, notification.ErrIdentity) {
		t.Fatal("retired inbox delivered", err)
	}
	remove := platformConnectionRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, ID: h.connection.ID, Version: h.connection.Version}
	if _, err = h.m.updatePlatformConnection(remove, true); !errors.Is(err, errPlatformReferenced) {
		t.Fatal("retired subscription lost connection reference", err)
	}
	root := h.m.store.dir
	if err = h.m.close(); err != nil {
		t.Fatal(err)
	}
	h.m = testManager(t, root, managerTestProfiles(t))
	h.m.notifications.Clock = h.clock.get
	h.m.mu.Lock()
	current, err := h.m.currentTodosLocked(h.room.RoomIdentity)
	views, viewErr := h.m.notificationViewsLocked(h.room.RoomIdentity)
	history, histErr := h.m.retiredNotificationViewsLocked(h.room.RoomIdentity)
	h.m.mu.Unlock()
	if err != nil || viewErr != nil || histErr != nil || current.SubscriptionID != next.SubscriptionID || len(views) != 3 || views[1].SubscriptionID != next.SubscriptionID || len(history) != 1 || !history[0].Retired || history[0].Pending != 1 {
		t.Fatal(current, views, history, err, viewErr, histErr)
	}
}
func TestTodosReplacementFailureNeverEnablesBothAccounts(t *testing.T) {
	for _, phase := range []string{"retire", "create", "unknown"} {
		t.Run(phase, func(t *testing.T) {
			h := newTodoHarness(t)
			h.pages.pending = []gitlab.Todo{h.todo(1, 0, "pending")}
			h.full()
			old := h.state()
			// Re-verifying the same connection with another stable account cannot make
			// sync adopt that user, even when the username or token spelling is similar.
			h.connection = verifyTodoAccount(t, h.m, h.connection, 102)
			if err := h.m.syncTodosSubscription(context.Background(), h.key, old.SubscriptionVersion); !errors.Is(err, ErrConflict) {
				t.Fatal("sync switched account", err)
			}
			old = h.state()
			ops := h.m.store.ops
			if phase == "unknown" {
				h.m.store.ops.syncDir = func(string) error { return errors.New("uncertain retirement") }
			} else {
				h.m.store.ops.rename = func(from, to string) error {
					if phase == "retire" || !strings.Contains(to, old.SubscriptionID) {
						return errors.New("injected switch failure")
					}
					return ops.rename(from, to)
				}
			}
			_, err := h.m.configureTodosSubscription(context.Background(), h.key, old.SubscriptionVersion, old.Config)
			h.m.store.ops = ops
			if err == nil {
				t.Fatal("switch failure ignored")
			}
			h.m.mu.Lock()
			all := h.m.notifications.StatesLocked(h.room.RoomIdentity)
			h.m.mu.Unlock()
			if len(all) != 1 || len(h.state().Inbox) != 1 {
				t.Fatal("failure lost history or created second subscription")
			}
			if phase == "create" && !h.state().Retired {
				t.Fatal("creation preceded retirement")
			}
			root := h.m.store.dir
			if err = h.m.close(); err != nil {
				t.Fatal(err)
			}
			h.m = testManager(t, root, managerTestProfiles(t))
			if err = h.m.notifications.Start(); err != nil {
				t.Fatal(err)
			}
			h.m.mu.Lock()
			stored, _ := h.m.todosSubscriptionLocked(h.key)
			done := h.m.notifications.DoneLocked(h.key)
			h.m.mu.Unlock()
			if done != nil || len(stored.Inbox) != 1 || (!stored.Retired && stored.Status != "binding_changed") {
				t.Fatal("old identity resumed on restart", stored)
			}
		})
	}
}
func TestTodosCredentialRotationPausesUntilSameAccountExplicitSync(t *testing.T) {
	h := newTodoHarness(t)
	h.pages.pending = []gitlab.Todo{h.todo(1, 0, "pending")}
	h.full()
	before := h.state()
	req := platformTestRequest(h.m)
	req.Version = h.connection.Version

	req.ReferencedRooms = []RoomIdentity{h.room.RoomIdentity}
	h.connection = platformTestReauthorize(t, h.m, req)
	paused := h.state()
	if paused.Status != "credentials_invalid" || !paused.Baseline.Equal(before.Baseline) || len(paused.Inbox) != 1 {
		t.Fatal(paused)
	}
	h.connection = verifyTodoAccount(t, h.m, h.connection, 101)
	// A fresh worker after verification still cannot automatically resume.
	h.newRunner()
	if _, stop := h.step(); !stop {
		t.Fatal("verification implicitly resumed query")
	}
	if err := h.m.notifications.Deliver(context.Background(), h.key, h.state().SubscriptionVersion); err == nil {
		t.Fatal("paused backlog delivered")
	}
	if err := h.m.syncTodosSubscription(context.Background(), h.key, h.state().SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	resumed := h.state()
	if resumed.SubscriptionID != before.SubscriptionID || !resumed.Baseline.Equal(before.Baseline) || len(resumed.Inbox) != 1 || resumed.Binding.CredentialVersion == before.Binding.CredentialVersion || resumed.Status == "credentials_invalid" {
		t.Fatal(resumed)
	}
	h.newRunner()
	h.full()
	if len(h.state().Inbox) != 1 {
		t.Fatal("rotation duplicated backlog")
	}
}
func TestTodosWorkspaceChangesDoNotChangeAccountScope(t *testing.T) {
	h := newTodoHarness(t)
	before := h.state()
	w := testGitLabWorkspace()
	w.Binding.URL = "https://other.invalid"
	w.Repository.URL = "https://other.invalid/group/project.git"
	setTestEffectiveWorkspace(t, h.m, h.room, w, 1)
	h.pages.pending = []gitlab.Todo{h.todo(1, 1, "pending"), h.todo(2, 2, "pending")}
	h.pages.pending[1].ProjectID = 8
	h.pages.pending[1].Project = "group/other"
	h.pages.pending[1].URL = "https://gitlab.invalid/group/other/-/merge_requests/42"
	h.full()
	after := h.state()
	if len(after.Inbox) != 2 || after.Status == "binding_changed" || after.SubscriptionID != before.SubscriptionID || !after.Baseline.Equal(before.Baseline) {
		t.Fatal("Workspace affected personal notifications", after)
	}
	if err := h.m.syncTodosSubscription(context.Background(), h.key, after.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
}
func TestTodosReceiptsSurviveClearAndRestart(t *testing.T) {
	h := newTodoHarness(t)
	h.pages.pending = []gitlab.Todo{h.todo(1, 0, "pending"), h.todo(2, 0, "pending")}
	h.full()
	saved := h.state()
	svc := eventLiveService(t, &eventHarness{m: h.m, room: h.room})
	item, _ := saved.Notification(gitlab.TodosKey(saved.Binding, 1))
	receipt, err := svc.room.Call[PostReceipt](context.Background(), postIntegrationMessage{RoomIdentity: h.room.RoomIdentity, DeliveryID: item.DeliveryID, Text: item.Text, Source: item.Source})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.room.Call[ClearReceipt](context.Background(), ClearMessages{RoomIdentity: h.room.RoomIdentity, Caller: humanCaller, ThroughSeq: receipt.Seq}); err != nil {
		t.Fatal(err)
	}
	h.reopen()
	eventLiveService(t, &eventHarness{m: h.m, room: h.room})
	if err = h.m.notifications.Deliver(context.Background(), h.key, saved.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	_, room := readManagedRoom(t, h.m, h.room)
	if len(room.Messages) != 0 || room.LatestSeq != receipt.Seq || len(h.state().Inbox) != 1 {
		t.Fatal("cleared receipt did not deduplicate", room)
	}
	if err = h.m.notifications.Deliver(context.Background(), h.key, saved.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	_, room = readManagedRoom(t, h.m, h.room)
	if len(room.Messages) != 1 || room.Messages[0].Source.Channel != "todos" || room.Messages[0].Author.Kind != "integration" || len(room.IntegrationReceipts) != 2 || len(h.state().Inbox) != 0 {
		t.Fatal(room)
	}
	// Reappearing as pending does not create another notification after ack.
	h.clock.add(130 * time.Second)
	h.full()
	if len(h.state().Inbox) != 0 {
		t.Fatal("acknowledged todo replayed")
	}
}

func TestTodosRetryAfterAndAuthenticationPause(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			h := newTodoHarness(t)
			calls := 0
			h.m.notifications.Source(gitlab.TodosSourceID).(*gitlab.TodosSource).Prepare = func(context.Context, string, RoomIdentity, gitlab.TodosQueryScope) (gitlab.TodoFetcher, error) {
				return todoFetchFunc(func(context.Context, gitlab.TodoQuery) (gitlab.TodoBatch, error) {
					calls++
					return gitlab.TodoBatch{}, &gitlab.TodoQueryError{Code: "query_failed", HTTPStatus: status, RetryAfterSeconds: 17}
				}), nil
			}
			h.step()
			s := h.state()
			paused := status == 401 || status == 403
			if (s.Status == "credentials_invalid") != paused || s.RetryAt.Before(h.clock.get().Add(17*time.Second)) || s.ServerNotBefore.Before(h.clock.get().Add(17*time.Second)) || s.Metrics.Failures != 1 {
				t.Fatal(s)
			}
			h.step()
			if calls != 1 {
				t.Fatal("retried before server hint")
			}
			if err := h.m.syncTodosSubscription(context.Background(), h.key, s.SubscriptionVersion); err != nil {
				t.Fatal(err)
			}
			h.install()
			h.newRunner()
			h.step()
			if len(h.pages.calls) != 0 {
				t.Fatal("manual sync bypassed Retry-After")
			}
			h.clock.add(17 * time.Second)
			h.full()
			if h.state().Status != "running" {
				t.Fatal(h.state())
			}
		})
	}
}

func TestTodosSkippedObjectsAreCountedWithoutDelivery(t *testing.T) {
	h := newTodoHarness(t)
	noProject := h.todo(1, 0, "pending")
	noProject.ProjectID = 0
	noProject.Project = ""
	noProject.TargetID = 0
	noProject.TargetIID = 0
	noProject.URL = ""
	noProject.Title = ""
	noProject.SkipReason = "no_project"
	unknown := h.todo(2, 0, "pending")
	unknown.TargetType = "Epic"
	unknown.Kind = "unknown"
	unknown.TargetID = 0
	unknown.TargetIID = 0
	unknown.URL = ""
	unknown.Title = ""
	unknown.SkipReason = "unsupported_target"
	h.pages.pending = []gitlab.Todo{noProject, unknown}
	h.full()
	s := h.state()
	h.m.mu.Lock()
	view, err := h.m.todosNotificationViewLocked(h.room.RoomIdentity, &s)
	h.m.mu.Unlock()
	if err != nil || view.Skipped["no_project"] != 1 || view.Skipped["unsupported_target"] != 1 || len(s.Inbox) != 0 || len(s.Observed) != 2 {
		t.Fatal(view, s, err)
	}
}

func TestTodosAllProjectsDoesNotDependOnWorkspaceRecovery(t *testing.T) {
	h := newTodoHarness(t)
	h.m.mu.Lock()
	h.m.index.WorkspaceOperations = append(h.m.index.WorkspaceOperations, workspaceOperation{RoomIdentity: h.room.RoomIdentity, Status: "failed", OperationID: strings.Repeat("e", 32)})
	h.m.mu.Unlock()
	h.pages.pending = []gitlab.Todo{h.todo(1, 0, "pending")}
	h.full()
	s := h.state()
	if len(s.Inbox) != 1 {
		t.Fatal("Workspace recovery blocked personal query")
	}
	if _, err := h.m.configureTodosSubscription(context.Background(), h.key, s.SubscriptionVersion, s.Config); err != nil {
		t.Fatal("Workspace recovery blocked all-project configure", err)
	}
	if err := h.m.syncTodosSubscription(context.Background(), h.key, h.state().SubscriptionVersion); err != nil {
		t.Fatal("Workspace recovery blocked all-project sync", err)
	}
}
func TestTodosTCPHTTPConfigureSyncDisableAndRoomDelivery(t *testing.T) {
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	room := managerCreate(t, m, "http-todos")
	c := verifyTodoAccount(t, m, platformTestSave(t, m, platformTestRequest(m)), 101)
	eventLiveService(t, &eventHarness{m: m, room: room})
	m.notifications.Source(gitlab.TodosSourceID).(*gitlab.TodosSource).Prepare = func(_ context.Context, _ string, _ RoomIdentity, b gitlab.TodosQueryScope) (gitlab.TodoFetcher, error) {
		return todoFetchFunc(func(_ context.Context, q gitlab.TodoQuery) (gitlab.TodoBatch, error) {
			items := []gitlab.Todo{}
			if q.State == "pending" {
				items = append(items, gitlab.Todo{ID: 9, RecipientID: b.Binding.UserID, ProjectID: 7, Project: "group/project", Action: "assigned", Reason: "assigned", Kind: "work_item", TargetType: "WorkItem", TargetID: 70, TargetIID: 42, Title: "HTTP to room", URL: b.Binding.URL + "/group/project/-/work_items/42", CreatedAt: time.Now().UTC(), State: "pending"})
			}
			return gitlab.TodoBatch{Version: 1, Todos: items, Complete: true, ResponseBytes: 200}, nil
		}), nil
	}
	if err := m.notifications.Start(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(m)
	defer server.Close()
	cfg := gitlab.NotificationConfig{ConnectionID: c.ID, Todos: gitlab.TodoSettings{Enabled: true}}
	payload := map[string]any{"service_epoch": m.epoch, "room_id": room.RoomID, "room_instance": room.RoomInstance, "version": 0, "caller": humanCaller, "config": cfg}
	call := func(path string) notificationView {
		t.Helper()
		data, _ := json.Marshal(payload)
		response, err := http.Post(server.URL+path, "application/json", bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != 200 || strings.Contains(string(body), "test-platform-secret") || strings.Contains(string(body), "fingerprint") {
			t.Fatal(response.StatusCode, string(body), err)
		}
		var unified gitlabNotificationView
		if err = json.Unmarshal(body, &unified); err != nil {
			t.Fatal(err)
		}
		payload["version"] = unified.Version
		cfg = unified.Config
		return unified.Todos
	}
	view := call("/v1/platform/gitlab/notifications/configure")
	if view.SubscriptionID == "" || view.RecipientID != 101 {
		t.Fatal(view)
	}
	delete(payload, "config")
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, snapshot := readManagedRoom(t, m, room)
		if len(snapshot.Messages) == 1 {
			if snapshot.Messages[0].Source.Channel != "todos" || snapshot.Messages[0].Author.Kind != "integration" {
				t.Fatal(snapshot)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("HTTP-configured worker did not deliver")
		}
		time.Sleep(5 * time.Millisecond)
	}
	view = call("/v1/platform/gitlab/notifications/sync")
	cfg.Todos.Enabled = false
	payload["config"] = cfg
	view = call("/v1/platform/gitlab/notifications/configure")
	if view.Status != "disabled" {
		t.Fatal(view)
	}
	_, snapshot := readManagedRoom(t, m, room)
	if len(snapshot.Messages) != 1 || len(snapshot.IntegrationReceipts) != 1 {
		t.Fatal("sync repeated todo delivery", snapshot)
	}
}

func TestTodosInactiveBacklogDeliversOnlyAfterItsRoomIsActive(t *testing.T) {
	h := newTodoHarness(t)
	h.pages.pending = []gitlab.Todo{h.todo(1, 0, "pending")}
	h.full()
	s := h.state()
	other := managerCreate(t, h.m, "other-room")
	svc := eventLiveService(t, &eventHarness{m: h.m, room: other})
	if err := h.m.notifications.Deliver(context.Background(), h.key, s.SubscriptionVersion); !errors.Is(err, notification.ErrBusy) {
		t.Fatal("inactive todo delivered", err)
	}
	_, otherState := readManagedRoom(t, h.m, other)
	if len(otherState.Messages) != 0 || len(h.state().Inbox) != 1 {
		t.Fatal("backlog crossed rooms")
	}
	if err := svc.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventLiveService(t, &eventHarness{m: h.m, room: h.room})
	if err := h.m.notifications.Deliver(context.Background(), h.key, s.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	_, current := readManagedRoom(t, h.m, h.room)
	if len(current.Messages) != 1 || current.Messages[0].Source.Channel != "todos" || len(h.state().Inbox) != 0 {
		t.Fatal(current)
	}
}

func TestTodosCurrentIntervalAndFixedInactiveInterval(t *testing.T) {
	h := newTodoHarness(t)
	h.full()
	// An unavailable Room collects again after 120 seconds, regardless of the
	// default active interval. Step once per second to avoid suspension recovery.
	calls := len(h.pages.calls)
	for i := 0; i < 119; i++ {
		if _, stop := h.step(); stop || len(h.pages.calls) != calls {
			t.Fatal("inactive collector polled early", i, stop, h.pages.calls)
		}
	}
	h.step()
	if len(h.pages.calls) != calls+1 {
		t.Fatal("inactive collector did not poll at 120 seconds")
	}
	h.full()
	// Opening a Room checks immediately, then uses the common active default.
	makeEventHarnessActive(t, &eventHarness{m: h.m, room: h.room})
	calls = len(h.pages.calls)
	h.step()
	if len(h.pages.calls) != calls+1 {
		t.Fatal("activation did not immediately check")
	}
	h.full()
	calls = len(h.pages.calls)
	for i := 0; i < 29; i++ {
		h.step()
		if len(h.pages.calls) != calls {
			t.Fatal("active collector polled early", i)
		}
	}
	h.step()
	if len(h.pages.calls) != calls+1 {
		t.Fatal("active collector did not use the common 30 second interval")
	}
}

func TestTodosQuickPollsBoundRequestsAndFullReconciliationFindsDeeperItems(t *testing.T) {
	h := newTodoHarness(t)
	h.full()
	full := h.state().LastFull
	for i := 0; i < 25; i++ {
		h.pages.pending = append(h.pages.pending, h.todo(int64(100+i), -1, "pending"))
		h.pages.done = append(h.pages.done, h.todo(int64(200+i), -1, "done"))
	}
	// Neither an old timestamp nor a known/high ID is a safe early-stop cursor.
	h.pages.pending = append(h.pages.pending, h.todo(1, 0, "pending"))
	h.pages.done = append(h.pages.done, h.todo(2, 0, "done"))
	calls := len(h.pages.calls)
	for i := 0; i < 120; i++ {
		h.step()
	}
	h.step() // Finish the second quick page.
	if len(h.pages.calls) != calls+2 {
		t.Fatal("ordinary poll traversed historical pages", h.pages.calls[calls:])
	}
	for _, q := range h.pages.calls[calls:] {
		if q.Page != 1 || q.PerPage != 20 {
			t.Fatal(q)
		}
	}
	if !h.state().LastFull.Equal(full) || h.state().Scan != nil || len(h.state().Inbox) != 0 {
		t.Fatal("quick check claimed full coverage", h.state())
	}
	for i := 0; i < 400 && h.state().LastFull.Equal(full); i++ {
		h.step()
	}
	s := h.state()
	if !s.LastFull.After(full) || len(s.Inbox) != 2 || len(s.Observed) != 2 {
		t.Fatal("reconciliation missed items beyond page one", s)
	}
	if _, ok := s.Inbox[gitlab.TodosKey(s.Binding, 1)]; !ok {
		t.Fatal("low ID pending item lost")
	}
	if _, ok := s.Inbox[gitlab.TodosKey(s.Binding, 2)]; !ok {
		t.Fatal("done item lost")
	}
	// Manual sync still performs a complete scan immediately.
	calls = len(h.pages.calls)
	if err := h.m.syncTodosSubscription(context.Background(), h.key, s.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	h.newRunner()
	h.full()
	if len(h.pages.calls) != calls+2 || h.pages.calls[calls].PerPage != 100 || len(h.state().Inbox) != 2 {
		t.Fatal("manual sync did not reconcile or deduplicate", h.pages.calls[calls:])
	}
}
