package gitlab

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

type eventPollPages struct {
	events   []Event
	quick    []Event // nil uses the first page of events; empty overrides it
	calls    []EventQuery
	failPage int
	etag     string
}

func (f *eventPollPages) FetchEvents(_ context.Context, q EventQuery) (EventBatch, error) {
	f.calls = append(f.calls, q)
	if f.failPage > 0 && q.Mode == "window" && q.Page == f.failPage {
		return EventBatch{}, errors.New("never-echo-private-transport")
	}
	events := append([]Event{}, f.events...)
	sort.Slice(events, func(i, j int) bool { return events[i].CreatedAt.After(events[j].CreatedAt) })
	if q.Mode == "quick" && f.quick != nil {
		events = f.quick
	}
	if q.Mode == "window" {
		events = slices.DeleteFunc(events, func(e Event) bool { return e.CreatedAt.Before(q.Start) || !e.CreatedAt.Before(q.End) })
	}
	start := min((q.Page-1)*20, len(events))
	end := min(start+20, len(events))
	b := EventBatch{Version: 1, Events: append([]Event{}, events[start:end]...), Complete: end == len(events), ResponseBytes: int64(200 + 50*(end-start))}
	if !b.Complete {
		b.NextPage = q.Page + 1
	}
	if q.Mode == "quick" {
		b.ETag = f.etag
	}
	return b, nil
}

// Provider algorithm tests need only a deterministic StepHost, not a Room,
// Workspace, credential store or directory manager.
type eventPollHarness struct {
	t        *testing.T
	host     *sourceStepHost
	runner   *EventsRunner
	pages    *eventPollPages
	baseline time.Time
}

func newEventPollHarness(t *testing.T, cfg EventsConfig) *eventPollHarness {
	t.Helper()
	source, host, runner := standaloneEventsConfig(t, cfg)
	host.scope.Current = false
	pages := &eventPollPages{}
	source.Prepare = func(context.Context, string, Binding) (EventFetcher, int64, error) { return pages, 7, nil }
	return &eventPollHarness{t: t, host: host, runner: runner, pages: pages, baseline: host.state.Baseline}
}
func (h *eventPollHarness) state() EventsState      { return h.host.state.Clone() }
func (h *eventPollHarness) advance(d time.Duration) { h.host.now = h.host.now.Add(d) }
func (h *eventPollHarness) step() time.Duration {
	h.t.Helper()
	delay, stop := h.runner.Step(context.Background())
	if stop {
		h.t.Fatalf("runner stopped: %+v", h.state())
	}
	return delay
}
func (h *eventPollHarness) prepare() {
	h.t.Helper()
	h.step()
	if h.state().ProjectID != 7 {
		h.t.Fatal("project unverified")
	}
}
func (h *eventPollHarness) event(id int64, seconds int) Event {
	return Event{ID: id, ProjectID: 7, Kind: "push", Action: "pushed to", CreatedAt: h.baseline.Add(time.Duration(seconds) * time.Second), Branch: "main", Author: EventAuthor{Username: "alice"}, SHA: strings.Repeat("a", 40)}
}
func (h *eventPollHarness) full() {
	h.t.Helper()
	for i := 0; i < 100; i++ {
		h.advance(150 * time.Millisecond)
		h.step()
		if s := h.state(); s.Scan == nil && !s.LastFull.IsZero() && !h.runner.NeedFull {
			return
		}
	}
	h.t.Fatal("window did not finish")
}
func TestGitLabEventsBurstAcrossMidnightAndIdleQuickPage(t *testing.T) {
	h := newEventPollHarness(t, EventsConfig{})
	for i := 1; i <= 35; i++ {
		h.pages.events = append(h.pages.events, h.event(int64(i), i))
	}
	h.prepare()
	h.advance(40 * time.Second)
	h.step()
	quick := h.state()
	if len(quick.Inbox) != 20 || !quick.Watermark.Equal(h.baseline) || quick.LastCheck.IsZero() || !quick.LastFull.IsZero() {
		t.Fatal("quick check advanced full watermark or lost immediate inbox", quick)
	}
	h.advance(time.Millisecond)
	h.step() // window page one
	pageOne := h.state()
	if pageOne.Scan == nil || pageOne.Scan.NextPage != 2 || !pageOne.Watermark.Equal(h.baseline) {
		t.Fatal(pageOne)
	}
	if pageOne.Scan.Start.Day() != 1 || pageOne.Scan.End.Day() != 2 {
		t.Fatal("did not cross UTC midnight", pageOne.Scan)
	}
	h.full()
	done := h.state()
	if len(done.Inbox) != 35 || len(done.Observed) != 35 || !done.Watermark.After(h.baseline) || done.LastFull.IsZero() {
		t.Fatal(done)
	}
	count := len(h.pages.calls)
	watermark := done.Watermark
	h.advance(30 * time.Second)
	h.step()
	if len(h.pages.calls) != count+1 || h.pages.calls[count].Mode != "quick" || !h.state().Watermark.Equal(watermark) || h.runner.NeedFull {
		t.Fatal("unchanged page caused an unnecessary scan", h.pages.calls)
	}
}

func TestGitLabEventsProcessesUnknownAfterKnownIDAndObservesFiltered(t *testing.T) {
	h := newEventPollHarness(t, EventsConfig{Filter: EventsFilter{Branches: []string{"main"}, IgnoreUsers: []string{"bot"}}})
	known := h.event(100, 1)
	filtered := h.event(101, 2)
	filtered.Kind = "merge_request"
	filtered.Action = "opened"
	filtered.Branch = ""
	unknown := h.event(102, 3)
	unknown.Kind = "unknown"
	unknown.SkipReason = "unsupported_event"
	ignored := h.event(103, 4)
	ignored.Author.Username = "BOT"
	h.pages.events = []Event{known, filtered, unknown, ignored}
	h.prepare()
	h.advance(10 * time.Second)
	h.step()
	h.full()
	s := h.state()
	if len(s.Inbox) != 1 || len(s.Observed) != 4 {
		t.Fatal(s)
	}
	for id, reason := range map[int64]string{101: "missing_branch", 102: "unsupported_event", 103: "ignored_user"} {
		if s.Observed[EventsKey(s.Binding, 7, id)].Reason != reason {
			t.Fatal(id, s.Observed)
		}
	}
	// ID 99 is unknown after an already-known higher ID. Process every item.
	late := h.event(99, 5)
	h.pages.quick = []Event{known, late, filtered, unknown, ignored}
	before := len(h.pages.calls)
	h.advance(30 * time.Second)
	h.step()
	s = h.state()
	if len(s.Inbox) != 2 || s.Metrics.NewItems != 5 || h.runner.NeedFull {
		t.Fatal("stopped early at known/max ID", s)
	}
	h.advance(30 * time.Second)
	h.step()
	if h.state().Metrics.NewItems != 5 || len(h.pages.calls) != before+2 {
		t.Fatal("filtered/unknown IDs treated as new repeatedly")
	}
}

func TestGitLabEventsPeriodicOverlapFindsLateVisibleEvent(t *testing.T) {
	h := newEventPollHarness(t, EventsConfig{})
	for i := 1; i <= 35; i++ {
		h.pages.events = append(h.pages.events, h.event(int64(i), i+10))
	}
	h.prepare()
	h.advance(50 * time.Second)
	h.step()
	h.full()
	original := h.state()
	// Late visibility with an old ID/time, hidden behind the 20 latest events.
	h.pages.events = append(h.pages.events, h.event(999, 2))
	for i := 0; i < 9; i++ {
		h.advance(30 * time.Second)
		h.step()
		h.step()
	}
	if !h.state().LastFull.Equal(original.LastFull) || len(h.state().Inbox) != 35 {
		t.Fatal("periodic scan ran too early")
	}
	h.advance(31 * time.Second)
	h.step()
	h.full()
	s := h.state()
	if len(s.Inbox) != 36 || !s.Watermark.After(original.Watermark) {
		t.Fatal("overlap lost late-visible old event", s)
	}
	for _, q := range h.pages.calls {
		if q.Mode == "window" && q.Start.Before(h.baseline) {
			t.Fatal("scan crossed initial baseline", q)
		}
	}
}

func TestGitLabEventsEmptyWindowAdvancesAnd304KeepsFullScan(t *testing.T) {
	h := newEventPollHarness(t, EventsConfig{ETagVerified: true})
	h.prepare()
	h.advance(time.Second)
	calls := 0
	h.runner.Fetcher = sourceEventsFunc(func(_ context.Context, q EventQuery) (EventBatch, error) {
		calls++
		if q.Mode == "quick" && q.ETag != "" {
			return EventBatch{Version: 1, Events: []Event{}, NotModified: true, ETag: `"page"`}, nil
		}
		return EventBatch{Version: 1, Events: []Event{}, Complete: true, ETag: `"page"`, ResponseBytes: 2}, nil
	})
	h.step()
	h.full()
	first := h.state()
	if !first.Watermark.After(h.baseline) || first.QuickCache == nil {
		t.Fatal("empty complete window did not advance", first)
	}
	h.advance(30 * time.Second)
	h.runner.RequestFull(h.host.Now())
	h.step() // 304
	middle := h.state()
	if !middle.Watermark.Equal(first.Watermark) || !middle.LastFull.Equal(first.LastFull) || !h.runner.NeedFull {
		t.Fatal("304 advanced/canceled full scan", middle)
	}
	h.full()
	if !h.state().Watermark.After(first.Watermark) || calls != 4 {
		t.Fatal("304 prevented pending window", calls, h.state())
	}
}

func TestGitLabEventsPaginationFailureRescansBeforeAdvancing(t *testing.T) {
	h := newEventPollHarness(t, EventsConfig{})
	for i := 1; i <= 35; i++ {
		h.pages.events = append(h.pages.events, h.event(int64(i), i))
	}
	h.prepare()
	h.advance(40 * time.Second)
	h.step()
	h.advance(time.Millisecond)
	h.step()
	h.pages.failPage = 2
	h.advance(150 * time.Millisecond)
	delay, stop := h.runner.Step(context.Background())
	s := h.state()
	if stop || delay <= 0 || s.Status != "backoff" || s.Scan.NextPage != 1 || !s.Watermark.Equal(h.baseline) || !s.LastFull.IsZero() {
		t.Fatal(s, delay, stop)
	}
	// Offset contents shift during outage. A restart of the window must revisit
	// page one instead of trusting the interrupted page-two checkpoint.
	h.pages.failPage = 0
	h.pages.events = append(h.pages.events, h.event(999, 38))
	h.advance(delay)
	h.step()
	h.full()
	s = h.state()
	if len(s.Inbox) != 36 || s.Scan != nil || s.ConsecutiveFailures != 0 {
		t.Fatal(s)
	}
	pages := []int{}
	for _, q := range h.pages.calls {
		if q.Mode == "window" {
			pages = append(pages, q.Page)
		}
	}
	// Finish the interrupted window, then a fresh one through reconnect time.
	if !reflect.DeepEqual(pages, []int{1, 2, 1, 2, 1, 2}) {
		t.Fatal("did not restart unstable offset pages", pages)
	}
}

func TestGitLabEventsCompactionKeepsPendingAndRejectsOldReappearance(t *testing.T) {
	h := newEventPollHarness(t, EventsConfig{})
	s := h.state()
	s.ProjectID = 7
	pending, filtered := h.event(1, 1), h.event(2, 2)
	filtered.Kind = "unknown"
	if _, _, err := s.Accept([]Event{pending, filtered}, h.host.Now()); err != nil {
		t.Fatal(err)
	}
	s.Watermark = h.baseline.Add(10 * time.Minute)
	s.IgnoreBefore = s.Watermark.Add(-EventsOverlap)
	s.Compact()
	if len(s.Observed) != 1 || len(s.Inbox) != 1 {
		t.Fatal("dropped pending or retained expired filtered observation", s)
	}
	// Simulate a future receipt removing the delivered payload. The admission
	// floor, not a count-based eviction, protects the removed observation.
	key := EventsKey(s.Binding, 7, 1)
	delete(s.Inbox, key)
	s.Compact()
	unknown, known, err := s.Accept([]Event{pending}, h.host.Now())
	if err != nil || unknown != 0 || known != 1 || len(s.Observed) != 0 || len(s.Inbox) != 0 {
		t.Fatal("compaction allowed replay", s, unknown, known, err)
	}
}

func TestGitLabEventsScanBudgetsPreserveCheckpointAndYieldToQuick(t *testing.T) {
	for _, kind := range []string{"pages", "bytes", "time"} {
		t.Run(kind, func(t *testing.T) {
			h := newEventPollHarness(t, EventsConfig{})
			h.host.scope.Current = true
			for i := 1; i <= 55; i++ {
				h.pages.events = append(h.pages.events, h.event(int64(i), i))
			}
			h.prepare()
			h.advance(60 * time.Second)
			h.step()
			h.advance(time.Millisecond)
			h.step()
			s := h.state()
			if s.Scan == nil || s.Scan.NextPage != 2 {
				t.Fatal(s.Scan)
			}
			switch kind {
			case "pages":
				h.host.limits.SlicePages = 1
			case "bytes":
				h.host.limits.SliceBytes = 1
			case "time":
				h.runner.SliceStarted = time.Now().Add(-h.host.limits.SliceTime)
			}
			h.advance(150 * time.Millisecond)
			count := len(h.pages.calls)
			delay := h.step()
			if delay <= 0 || len(h.pages.calls) != count || h.state().Scan.NextPage != 2 || !h.state().Watermark.Equal(h.baseline) {
				t.Fatal("scan cap lost/advanced checkpoint", delay, h.state())
			}
			// An overdue quick check wins over the remaining multi-page backlog.
			h.advance(5 * time.Second)
			h.step()
			if h.pages.calls[len(h.pages.calls)-1].Mode != "quick" || h.state().Scan.NextPage != 2 {
				t.Fatal("backlog starved quick check")
			}
		})
	}
}

func TestGitLabEventsSuccessPollingHintAndCappedBackoff(t *testing.T) {
	h := newEventPollHarness(t, EventsConfig{})
	h.prepare()
	h.advance(time.Second)
	calls := 0
	h.runner.Fetcher = sourceEventsFunc(func(context.Context, EventQuery) (EventBatch, error) {
		calls++
		return EventBatch{Version: 1, Events: []Event{}, Complete: true, RetryAfterSeconds: 20}, nil
	})
	h.step()
	h.advance(5 * time.Second)
	h.step()
	if calls != 1 || h.state().Scan != nil {
		t.Fatal("ignored successful polling hint")
	}
	h.advance(15 * time.Second)
	h.step()
	if calls != 2 || h.state().LastFull.IsZero() {
		t.Fatal("hint canceled rather than delayed scan")
	}
	h.runner.Fetcher = sourceEventsFunc(func(context.Context, EventQuery) (EventBatch, error) {
		return EventBatch{}, errors.New("never-echo-network-secret")
	})
	h.advance(20 * time.Second)
	for i := 0; i < 12; i++ {
		delay, stop := h.runner.Step(context.Background())
		if stop || delay <= 0 || delay > 5*time.Minute || h.state().ErrorCode != "query_failed" {
			t.Fatal(i, delay, stop, h.state())
		}
		h.advance(delay)
	}
}

func TestGitLabEventsComputerResumeRestartsUnstablePageAndMarksRetentionGap(t *testing.T) {
	h := newEventPollHarness(t, EventsConfig{})
	for i := 1; i <= 35; i++ {
		h.pages.events = append(h.pages.events, h.event(int64(i), i))
	}
	h.prepare()
	h.advance(40 * time.Second)
	h.step()
	h.advance(time.Millisecond)
	h.step()
	before := h.state()
	if before.Scan.NextPage != 2 {
		t.Fatal(before.Scan)
	}
	h.advance(48 * time.Hour)
	h.step() // immediate quick, same process after suspension
	after := h.state()
	if after.Scan == nil || after.Scan.NextPage != 1 || !after.Watermark.Equal(before.Watermark) || after.Gap == nil || after.Gap.Reason != "retention_unverified" {
		t.Fatal(after)
	}
	if h.pages.calls[len(h.pages.calls)-1].Mode != "quick" {
		t.Fatal("resume skipped immediate quick check")
	}
	h.full()
	if h.state().Gap == nil || !h.state().Baseline.Equal(h.baseline) {
		t.Fatal("scan success claimed retained-history completeness")
	}
}

func TestGitLabEventsLongPollingHintDoesNotStarveCurrentRoomCatchup(t *testing.T) {
	h := newEventPollHarness(t, EventsConfig{})
	h.host.scope.Current = true
	h.prepare()
	h.advance(time.Second)
	modes := []string{}
	h.runner.Fetcher = sourceEventsFunc(func(_ context.Context, q EventQuery) (EventBatch, error) {
		modes = append(modes, q.Mode)
		return EventBatch{Version: 1, Events: []Event{}, Complete: true, RetryAfterSeconds: 20}, nil
	})
	h.step() // current-room quick interval is 5s, server gate is 20s
	h.advance(20 * time.Second)
	h.step()
	if len(modes) != 2 || modes[0] != "quick" || modes[1] != "window" || h.state().LastFull.IsZero() {
		t.Fatal("every permitted slot was consumed by overdue quick checks", modes)
	}
	h.advance(20 * time.Second)
	h.step()
	if len(modes) != 3 || modes[2] != "quick" {
		t.Fatal("catchup starved the next quick check", modes)
	}
}
