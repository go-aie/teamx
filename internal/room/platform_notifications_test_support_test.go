package room

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func gitlabEventMessage(s gitlab.EventsState, entry gitlab.EventsInboxEntry) postIntegrationMessage {
	item := gitlab.EventsMessage(s, entry)
	return postIntegrationMessage{RoomIdentity: s.RoomIdentity, DeliveryID: item.DeliveryID, Text: item.Text, Source: item.Source}
}

func (m *roomManager) testEventsKeyLocked(owner RoomIdentity) notification.Key {
	for _, s := range m.notifications.StatesLocked(owner) {
		if e, ok := s.(gitlab.EventsState); ok && !e.Retired {
			return s.Metadata().Key
		}
	}
	return notification.Key{Owner: owner, Source: gitlab.EventsSourceID}
}
func (m *roomManager) testEventsKey(owner RoomIdentity) notification.Key {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.testEventsKeyLocked(owner)
}
func (m *roomManager) testEventsState(owner RoomIdentity) (gitlab.EventsState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.eventsSubscriptionLocked(m.testEventsKeyLocked(owner))
	if !ok {
		return state, ErrIdentity
	}
	return state, nil
}
func (m *roomManager) testNotificationPath(t *testing.T, owner RoomIdentity) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	path, err := m.notifications.StatePathLocked(m.testEventsKeyLocked(owner))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

type eventTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *eventTestClock) get() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *eventTestClock) add(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

type eventFetchFunc func(context.Context, gitlab.EventQuery) (gitlab.EventBatch, error)

func (f eventFetchFunc) FetchEvents(ctx context.Context, q gitlab.EventQuery) (gitlab.EventBatch, error) {
	return f(ctx, q)
}

type eventPages struct {
	events   []gitlab.Event
	quick    []gitlab.Event // nil uses the first page of events; empty overrides it
	calls    []gitlab.EventQuery
	failPage int
	etag     string
}

func (f *eventPages) FetchEvents(_ context.Context, q gitlab.EventQuery) (gitlab.EventBatch, error) {
	f.calls = append(f.calls, q)
	if f.failPage > 0 && q.Mode == "window" && q.Page == f.failPage {
		return gitlab.EventBatch{}, errors.New("never-echo-private-transport")
	}
	events := append([]gitlab.Event{}, f.events...)
	sort.Slice(events, func(i, j int) bool { return events[i].CreatedAt.After(events[j].CreatedAt) })
	if q.Mode == "quick" && f.quick != nil {
		events = f.quick
	}
	if q.Mode == "window" {
		events = slices.DeleteFunc(events, func(e gitlab.Event) bool { return e.CreatedAt.Before(q.Start) || !e.CreatedAt.Before(q.End) })
	}
	start := min((q.Page-1)*20, len(events))
	end := min(start+20, len(events))
	b := gitlab.EventBatch{Version: 1, Events: append([]gitlab.Event{}, events[start:end]...), Complete: end == len(events), ResponseBytes: int64(200 + 50*(end-start))}
	if !b.Complete {
		b.NextPage = q.Page + 1
	}
	if q.Mode == "quick" {
		b.ETag = f.etag
	}
	return b, nil
}

type eventHarness struct {
	t        *testing.T
	m        *roomManager
	room     roomSummary
	clock    *eventTestClock
	pages    *eventPages
	runner   *gitlab.EventsRunner
	baseline time.Time
}

func newEventHarness(t *testing.T, cfg gitlab.EventsConfig) *eventHarness {
	t.Helper()
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	room := managerCreate(t, m, "event-room")
	c := platformTestSave(t, m, platformTestRequest(m))
	m.mu.Lock()
	file, _ := m.platforms.Load()
	generation := file.Platforms[0].Generation
	m.mu.Unlock()
	w := testGitLabWorkspace()
	w.Binding.PlatformID = c.ID
	w.Binding.PlatformGeneration = generation
	setTestEffectiveWorkspace(t, m, room, w, 1)

	clock := &eventTestClock{now: time.Date(2026, 11, 1, 23, 59, 50, 0, time.UTC)}
	m.notifications.Clock = clock.get
	pages := &eventPages{}
	m.notifications.Source(gitlab.EventsSourceID).(*gitlab.EventsSource).Prepare = func(context.Context, string, gitlab.Binding) (gitlab.EventFetcher, int64, error) {
		return pages, 7, nil
	}
	cfg.Enabled = true
	s, err := seedTestEvents(m, room.RoomIdentity, cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := &eventHarness{t: t, m: m, room: room, clock: clock, pages: pages, baseline: s.Baseline}
	h.newRunner()
	return h
}
func (h *eventHarness) newRunner() {
	s := h.state()
	h.m.mu.Lock()
	h.runner = h.m.notifications.NewRunnerLocked(s.Metadata().Key).(*gitlab.EventsRunner)
	h.m.mu.Unlock()
}
func (h *eventHarness) state() gitlab.EventsState {
	h.t.Helper()
	s, err := h.m.testEventsState(h.room.RoomIdentity)
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}
func (h *eventHarness) step() time.Duration {
	h.t.Helper()
	delay, stop := h.runner.Step(context.Background())
	if stop {
		h.t.Fatalf("runner stopped: %+v", h.state())
	}
	return delay
}
func (h *eventHarness) event(id int64, seconds int) gitlab.Event {
	return gitlab.Event{ID: id, ProjectID: 7, Kind: "push", Action: "pushed to", CreatedAt: h.baseline.Add(time.Duration(seconds) * time.Second), Branch: "main", Author: gitlab.EventAuthor{Username: "alice"}, SHA: strings.Repeat("a", 40)}
}
func (h *eventHarness) full() {
	h.t.Helper()
	for i := 0; i < 100; i++ {
		h.clock.add(150 * time.Millisecond)
		h.step()
		if s := h.state(); s.Scan == nil && !s.LastFull.IsZero() && !h.runner.NeedFull {
			return
		}
	}
	h.t.Fatal("window did not finish")
}
func (h *eventHarness) prepare() {
	h.t.Helper()
	h.step()
	if h.state().ProjectID != 7 {
		h.t.Fatal("project unverified")
	}
}

func makeEventHarnessActive(t *testing.T, h *eventHarness) {
	t.Helper()
	h.m.mu.Lock()
	h.m.runtimes[h.room.RoomIdentity] = &roomRuntime{service: &service{info: ServiceInfo{RoomIdentity: h.room.RoomIdentity}}, status: "ready"}
	h.m.mu.Unlock()
	t.Cleanup(func() { h.m.mu.Lock(); delete(h.m.runtimes, h.room.RoomIdentity); h.m.mu.Unlock() })
}

// Existing persistence/poller fixtures exercise snapshots created before the
// independent project selector. Production creation requires an explicit target.
func seedTestEvents(m *roomManager, owner RoomIdentity, cfg gitlab.EventsConfig) (gitlab.EventsState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, err := m.workspaceRoom(owner)
	if err != nil {
		return gitlab.EventsState{}, err
	}
	saved, err := m.readWorkspaceRoomConfig(entry)
	if err != nil {
		return gitlab.EventsState{}, err
	}
	binding := *saved.Workspace.Binding
	account, _, err := m.resolvePlatformAccountLocked(owner, binding.PlatformID, "gitlab")
	if err != nil {
		return gitlab.EventsState{}, err
	}
	binding.PlatformGeneration = account.Generation
	cfg.PlatformID = binding.PlatformID
	cfg.Project = binding.Project
	s, err := gitlab.ConfigureEvents(owner, nil, &binding, cfg, m.notifications.Now())
	s.UserID = account.UserID
	s.Username = account.Username
	if err == nil {
		err = m.notifications.SaveLocked(s)
	}
	if err == nil {
		m.notifications.EnsureLocked(s.Metadata().Key)
	}
	return s, err
}
