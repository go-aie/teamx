package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// This host models committed snapshots and optimistic candidates only. Real
// writer failure, unknown commits, receipts and restart remain Room integrations.
type todoPollHost struct {
	state                  TodosState
	scope                  notification.Scope
	now                    time.Time
	limits                 notification.Limits
	revision, readRevision uint64
	readScope              string
	ctx                    context.Context
	force                  bool
	fail                   error
	beforeCommit           func()
}

func (h *todoPollHost) Read(ctx context.Context) (notification.State, notification.Scope, error) {
	if err := ctx.Err(); err != nil {
		return nil, notification.Scope{}, err
	}
	h.ctx, h.readRevision, h.readScope = ctx, h.revision, h.scope.Revision
	return h.state.Clone(), h.scope, nil
}
func (h *todoPollHost) Commit(candidate notification.State) error {
	if h.beforeCommit != nil {
		h.beforeCommit()
	}
	if err := h.ctx.Err(); err != nil {
		return err
	}
	if h.revision != h.readRevision || h.scope.Revision != h.readScope {
		return notification.ErrBusy
	}
	next, ok := candidate.(TodosState)
	if !ok || next.Metadata().Key != h.state.Metadata().Key || next.SubscriptionVersion != h.state.SubscriptionVersion {
		return notification.ErrIdentity
	}
	if err := next.Validate(h.state.RoomIdentity); err != nil {
		return err
	}
	if h.fail != nil {
		return h.fail
	}
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(next.Observed) > h.limits.Observed || len(next.Inbox) > h.limits.Inbox || int64(len(data)) > h.limits.Bytes {
		return notification.ErrCapacity
	}
	h.state = next.Clone()
	h.revision++
	h.readRevision = h.revision
	return nil
}
func (h *todoPollHost) Now() time.Time                     { return h.now }
func (*todoPollHost) Jitter(d time.Duration) time.Duration { return d }
func (h *todoPollHost) Limits() notification.Limits        { return h.limits }
func (h *todoPollHost) ConsumeForce() bool                 { v := h.force; h.force = false; return v }

type todoPollPages struct {
	pending, done []Todo
	calls         []TodoQuery
	failState     string
	failPage      int
	hook          func()
}

func (p *todoPollPages) FetchTodos(_ context.Context, q TodoQuery) (TodoBatch, error) {
	p.calls = append(p.calls, q)
	if p.hook != nil {
		p.hook()
	}
	if q.State == p.failState && q.Page == p.failPage {
		return TodoBatch{}, errors.New("private query error")
	}
	items := p.pending
	if q.State == "done" {
		items = p.done
	}
	start := min((q.Page-1)*q.PerPage, len(items))
	end := min(start+q.PerPage, len(items))
	batch := TodoBatch{Version: TodosProtocolVersion, Todos: append([]Todo{}, items[start:end]...), Complete: end == len(items), ResponseBytes: 200}
	if !batch.Complete {
		batch.NextPage = q.Page + 1
	}
	return batch, nil
}

type todoPollFetch func(context.Context, TodoQuery) (TodoBatch, error)

func (f todoPollFetch) FetchTodos(ctx context.Context, q TodoQuery) (TodoBatch, error) {
	return f(ctx, q)
}

type todoPollHarness struct {
	t        *testing.T
	host     *todoPollHost
	source   *TodosSource
	runner   *TodosRunner
	pages    *todoPollPages
	baseline time.Time
}

func newTodoPollHarness(t *testing.T) *todoPollHarness {
	t.Helper()
	state := sourceTodoState(t)
	host := &todoPollHost{state: state, now: state.Baseline, limits: notification.DefaultLimits()}
	data, err := json.Marshal(TodosQueryScope{Binding: state.Binding, PlatformVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	host.scope = notification.Scope{Data: data, Revision: "scope-1", CredentialStamp: "credential-1"}
	h := &todoPollHarness{t: t, host: host, pages: &todoPollPages{}, baseline: state.Baseline}
	h.source = &TodosSource{Prepare: func(context.Context, string, platform.RoomIdentity, TodosQueryScope) (TodoFetcher, error) {
		return h.pages, nil
	}}
	h.runner = h.source.NewRunner(host).(*TodosRunner)
	return h
}
func (h *todoPollHarness) state() TodosState       { return h.host.state.Clone() }
func (h *todoPollHarness) advance(d time.Duration) { h.host.now = h.host.now.Add(d) }
func (h *todoPollHarness) step() (time.Duration, bool) {
	h.advance(time.Second)
	return h.runner.Step(h.t.Context())
}
func (h *todoPollHarness) full() {
	h.t.Helper()
	calls := len(h.pages.calls)
	for range 200 {
		if _, stop := h.step(); stop {
			h.t.Fatalf("stopped: %+v", h.state())
		}
		s := h.state()
		if s.Scan == nil && !s.LastFull.IsZero() && (len(h.pages.calls) == calls || h.pages.calls[len(h.pages.calls)-1].State == "done") {
			return
		}
	}
	h.t.Fatal("scan did not complete")
}
func (h *todoPollHarness) todo(id int64, seconds int, state string) Todo {
	return Todo{ID: id, RecipientID: 101, ProjectID: 7, Project: "group/project", Action: "review_requested", Reason: "review_requested", Kind: "merge_request", TargetType: "MergeRequest", TargetID: 70, TargetIID: 42, Actor: &TodoActor{ID: 2, Username: "alice"}, Title: "review needed", URL: h.host.state.Binding.URL + "/group/project/-/merge_requests/42", CreatedAt: h.baseline.Add(time.Duration(seconds) * time.Second), State: state}
}
func TestTodosScansPendingDoneAndNeverUsesAnIDCursor(t *testing.T) {
	h := newTodoPollHarness(t)
	subscriptionID := h.state().SubscriptionID
	h.pages.pending = []Todo{h.todo(99, 0, "pending"), h.todo(2, 0, "pending"), h.todo(7, -1, "pending")}
	h.pages.done = []Todo{h.todo(1000, 0, "done")}
	h.full()
	s := h.state()
	if len(s.Inbox) != 3 || len(s.Observed) != 3 || len(h.pages.calls) != 2 || h.pages.calls[0].State != "pending" || h.pages.calls[1].State != "done" {
		t.Fatal(s, h.pages.calls)
	}
	// An item completed between checks, and a known ID became pending again.
	h.pages.pending = []Todo{h.todo(1000, 0, "pending")}
	h.pages.done = append(h.pages.done, h.todo(1, 1, "done"))
	h.advance(120 * time.Second)
	h.full()
	if s = h.state(); len(s.Inbox) != 4 || len(s.Observed) != 4 || s.SubscriptionID != subscriptionID || !s.Baseline.Equal(h.baseline) {
		t.Fatal(s)
	}
	if s.Inbox[TodosKey(s.Binding, 1000)].Todo.State != "done" {
		t.Fatal("known todo was re-imported")
	}
}

func TestTodosScanBudgetReportsIncompleteAndKeepsCheckpoint(t *testing.T) {
	for _, kind := range []string{"pages", "bytes", "time"} {
		t.Run(kind, func(t *testing.T) {
			h := newTodoPollHarness(t)
			for i := 1; i <= 101; i++ {
				h.pages.pending = append(h.pages.pending, h.todo(int64(i), 0, "pending"))
			}
			h.step()
			switch kind {
			case "pages":
				h.host.limits.SlicePages = 1
			case "bytes":
				h.host.limits.SliceBytes = 200
			case "time":
				h.runner.sliceStarted = time.Now().Add(-h.host.limits.SliceTime)
			}
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
			h.advance(125 * time.Second)
			h.step()
			if q := h.pages.calls[len(h.pages.calls)-1]; q.Page != 2 || q.State != "pending" {
				t.Fatal("budget yield lost its checkpoint", q)
			}
			// A short ordinary yield continues its checkpoint without discarding page 2.
			h.advance(3 * time.Second)
			h.host.limits = notification.DefaultLimits()
			h.full()
			if len(h.state().Inbox) != 101 {
				t.Fatal("budget dropped unqueried page")
			}
		})
	}
}

func TestTodosCapacityRejectsWholePage(t *testing.T) {
	h := newTodoPollHarness(t)
	h.pages.pending = []Todo{h.todo(1, 0, "pending"), h.todo(2, 0, "pending")}
	h.host.limits.Inbox = 1
	h.step()
	s := h.state()
	if s.Status != "capacity" || len(s.Inbox) != 0 || len(s.Observed) != 0 || s.Scan.NextPage != 1 || !s.LastFull.IsZero() {
		t.Fatal(s)
	}
}

func TestTodosCurrentIntervalAndFixedInactiveInterval(t *testing.T) {
	h := newTodoPollHarness(t)
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
	h.host.scope.Current = true
	calls = len(h.pages.calls)
	h.step()
	if len(h.pages.calls) != calls+1 {
		t.Fatal("activation did not immediately check")
	}
	h.full()
	calls = len(h.pages.calls)
	for i := 0; i < DefaultIntervalSeconds-1; i++ {
		h.step()
		if len(h.pages.calls) != calls {
			t.Fatal("active collector polled early", i)
		}
	}
	h.step()
	if len(h.pages.calls) != calls+1 {
		t.Fatal("active collector did not use the common default interval")
	}
}

func TestTodosQuickPollsBoundRequestsAndFullReconciliationFindsDeeperItems(t *testing.T) {
	h := newTodoPollHarness(t)
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
	if _, ok := s.Inbox[TodosKey(s.Binding, 1)]; !ok {
		t.Fatal("low ID pending item lost")
	}
	if _, ok := s.Inbox[TodosKey(s.Binding, 2)]; !ok {
		t.Fatal("done item lost")
	}
}

func TestTodosRetryAfterAndAuthenticationPause(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			h := newTodoPollHarness(t)
			calls := 0
			h.source.Prepare = func(context.Context, string, platform.RoomIdentity, TodosQueryScope) (TodoFetcher, error) {
				return todoPollFetch(func(context.Context, TodoQuery) (TodoBatch, error) {
					calls++
					return TodoBatch{}, &TodoQueryError{Code: "query_failed", HTTPStatus: status, RetryAfterSeconds: 17}
				}), nil
			}
			delay, stop := h.step()
			s := h.state()
			paused := status == 401 || status == 403
			if stop != paused || (s.Status == "credentials_invalid") != paused || delay != 17*time.Second || !s.RetryAt.Equal(h.host.now.Add(delay)) || !s.ServerNotBefore.Equal(s.RetryAt) || s.Metrics.Failures != 1 || s.Metrics.Calls != 1 {
				t.Fatal(s, delay, stop)
			}
			h.advance(16 * time.Second)
			h.runner.Step(t.Context())
			if calls != 1 {
				t.Fatal("retried before server hint")
			}
			h.advance(time.Second)
			h.runner.Step(t.Context())
			if calls != map[bool]int{true: 1, false: 2}[paused] {
				t.Fatal("incorrect retry/pause", calls)
			}
			if paused {
				h.advance(time.Hour)
				h.runner.Step(t.Context())
				if calls != 1 {
					t.Fatal("authentication pause expired")
				}
			}
		})
	}
}

func TestTodosSkippedObjectsAreCountedWithoutDelivery(t *testing.T) {
	h := newTodoPollHarness(t)
	noProject := h.todo(1, 0, "pending")
	noProject.ProjectID, noProject.TargetID, noProject.TargetIID = 0, 0, 0
	noProject.Project, noProject.URL, noProject.Title = "", "", ""
	noProject.SkipReason = "no_project"
	unknown := h.todo(2, 0, "pending")
	unknown.TargetType, unknown.Kind = "Epic", "unknown"
	unknown.TargetID, unknown.TargetIID = 0, 0
	unknown.URL, unknown.Title = "", ""
	unknown.SkipReason = "unsupported_target"
	filtered := h.todo(3, -1, "pending")
	h.pages.pending = []Todo{noProject, unknown, filtered}
	h.full()
	s := h.state()
	if len(s.Inbox) != 0 || len(s.Observed) != 2 || s.Metrics.NewItems != 2 {
		t.Fatal(s)
	}
	for id, reason := range map[int64]string{1: "no_project", 2: "unsupported_target"} {
		if s.Observed[TodosKey(s.Binding, id)].Reason != reason {
			t.Fatal(id, s.Observed)
		}
	}
	h.host.force = true
	h.full()
	if h.state().Metrics.NewItems != 2 {
		t.Fatal("skipped objects counted again")
	}
}

func TestTodosPageFailureRescansMovingOffsets(t *testing.T) {
	h := newTodoPollHarness(t)
	for i := 1; i <= 101; i++ {
		h.pages.pending = append(h.pages.pending, h.todo(int64(i), 0, "pending"))
	}
	h.step()
	h.pages.failState, h.pages.failPage = "pending", 2
	delay, stop := h.step()
	s := h.state()
	if stop || s.Scan.State != "pending" || s.Scan.NextPage != 1 || len(s.Inbox) != 100 || !s.LastFull.IsZero() || s.ErrorCode != "query_failed" {
		t.Fatal(s)
	}
	h.pages.failPage = 0
	h.pages.pending = append([]Todo{h.todo(999, 0, "pending")}, h.pages.pending...)
	h.advance(delay)
	h.full()
	if len(h.state().Inbox) != 102 || h.pages.calls[2].Page != 1 {
		t.Fatal(h.state(), h.pages.calls)
	}
}

func TestTodosCandidatesHonorAcknowledgementsConflictsAndCancellation(t *testing.T) {
	for _, mode := range []string{"ack-during-fetch", "ack-before-commit", "scope", "cancel", "save-error"} {
		t.Run(mode, func(t *testing.T) {
			h := newTodoPollHarness(t)
			h.pages.pending = []Todo{h.todo(1, 0, "pending")}
			h.step()
			before := h.state()
			pages := h.runner.slicePages
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			acknowledge := func() {
				h.host.state = h.host.state.Acknowledge(TodosKey(before.Binding, 1)).(TodosState)
				h.host.revision++
			}
			h.pages.hook = func() {
				switch mode {
				case "ack-during-fetch":
					acknowledge()
				case "scope":
					h.host.scope.Revision = "changed"
				case "cancel":
					cancel()
				}
			}
			if mode == "ack-before-commit" {
				h.host.beforeCommit = func() { h.host.beforeCommit = nil; acknowledge() }
			}
			if mode == "save-error" {
				h.host.fail = errors.New("save failed")
			}
			_, stop := h.runner.Step(ctx)
			after := h.state()
			if mode == "ack-during-fetch" {
				if stop || len(after.Inbox) != 0 || after.LastFull.IsZero() {
					t.Fatal("ack overwritten", after)
				}
				return
			}
			if mode == "ack-before-commit" {
				if stop || len(after.Inbox) != 0 {
					t.Fatal("conflicting candidate committed", after)
				}
			} else if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected candidate changed snapshot", after)
			}
			if h.runner.slicePages != pages || after.Scan == nil || after.Scan.State != "done" || !after.LastFull.IsZero() {
				t.Fatal("uncommitted progress escaped", after)
			}
			if stop != (mode == "cancel" || mode == "save-error") {
				t.Fatal("wrong stop classification", stop)
			}
			if mode == "ack-before-commit" {
				h.pages.hook = nil
				h.full()
				if len(h.state().Inbox) != 0 || h.state().LastFull.IsZero() {
					t.Fatal("retry restored acknowledged item")
				}
			}
		})
	}
}
