package room

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func TestGitLabEventsRestartRetainsBaselineInboxAndRescansCheckpoint(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{ETagVerified: true})
	h.pages.etag = `"cached-page"`
	for i := 1; i <= 35; i++ {
		h.pages.events = append(h.pages.events, h.event(int64(i), i))
	}
	h.prepare()
	h.clock.add(40 * time.Second)
	h.step()
	h.clock.add(time.Millisecond)
	h.step()
	before := h.state()
	if before.Scan.NextPage != 2 || before.QuickCache == nil {
		t.Fatal(before.Scan)
	}
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	restored := testManager(t, h.m.store.dir, managerTestProfiles(t))
	restored.notifications.Clock = h.clock.get
	restored.notifications.Source(gitlab.EventsSourceID).(*gitlab.EventsSource).Prepare = func(context.Context, string, gitlab.Binding) (gitlab.EventFetcher, int64, error) {
		return h.pages, 7, nil
	}
	h.m = restored
	h.newRunner()
	h.clock.add(time.Second)
	h.prepare()
	after := h.state()
	if !after.Baseline.Equal(before.Baseline) || after.SubscriptionID != before.SubscriptionID || len(after.Inbox) != 20 || after.Scan.NextPage != 1 || after.QuickCache != nil {
		t.Fatal(after)
	}
	count := len(h.pages.calls)
	h.step()
	h.full()
	if h.pages.calls[count].ETag != "" || len(h.state().Inbox) != 35 {
		t.Fatal("restart reused unconfirmed cache or duplicated/lost inbox")
	}
}

func TestGitLabEventsInitialFailureDisableAndFiltersPreserveBaseline(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{Filter: gitlab.EventsFilter{Types: []string{"push"}}})
	h.m.notifications.Source(gitlab.EventsSourceID).(*gitlab.EventsSource).Prepare = func(context.Context, string, gitlab.Binding) (gitlab.EventFetcher, int64, error) {
		return nil, 0, &gitlab.EventQueryError{Code: "query_failed", HTTPStatus: 401}
	}
	_, stop := h.runner.Step(context.Background())
	first := h.state()
	if !stop || first.Status != "credentials_invalid" || first.ProjectID != 0 || !first.Baseline.Equal(h.baseline) {
		t.Fatal(first)
	}
	h.clock.add(time.Hour)
	cfg := first.Config
	cfg.Enabled = false
	disabled, err := h.m.configureEventsSubscription(context.Background(), h.m.testEventsKey(h.room.RoomIdentity), first.SubscriptionVersion, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Status != "disabled" || !disabled.Baseline.Equal(first.Baseline) {
		t.Fatal(disabled)
	}
	cfg.Enabled = true
	cfg.Filter.Types = []string{"merge_request.opened"}
	enabled, err := h.m.configureEventsSubscription(context.Background(), h.m.testEventsKey(h.room.RoomIdentity), disabled.SubscriptionVersion, cfg)
	if err != nil || enabled.SubscriptionID != first.SubscriptionID || !enabled.Baseline.Equal(first.Baseline) {
		t.Fatal(enabled, err)
	}
	h.m.notifications.Source(gitlab.EventsSourceID).(*gitlab.EventsSource).Prepare = func(context.Context, string, gitlab.Binding) (gitlab.EventFetcher, int64, error) {
		return h.pages, 7, nil
	}
	old := h.event(1, 5)
	old.Kind = "merge_request"
	old.Action = "opened"
	recent := h.event(2, 3601)
	recent.Kind = "merge_request"
	recent.Action = "opened"
	h.pages.events = []gitlab.Event{old, recent}
	h.newRunner()
	h.prepare()
	h.clock.add(2 * time.Second)
	h.step()
	if h.state().Observed[gitlab.EventsKey(enabled.Binding, 7, 1)].Reason != "event_type" {
		t.Fatal("new filter applied to earlier unseen activity")
	}
	h.full()
	s := h.state()
	if len(s.Inbox) != 1 || s.Inbox[gitlab.EventsKey(s.Binding, 7, 2)].Event.ID != 2 {
		t.Fatal("filter change replayed earlier unseen activity", s)
	}
}

func TestGitLabEventsCapacityStopsWithoutProgressOrCache(t *testing.T) {
	for _, kind := range []string{"inbox", "observed", "bytes"} {
		t.Run(kind, func(t *testing.T) {
			h := newEventHarness(t, gitlab.EventsConfig{ETagVerified: true})
			h.prepare()
			h.clock.add(time.Second)
			before := h.state()
			switch kind {
			case "inbox":
				h.m.notifications.Limits.Inbox = 1
			case "observed":
				h.m.notifications.Limits.Observed = 1
			case "bytes":
				data, _ := json.Marshal(before)
				h.m.notifications.Limits.Bytes = int64(len(data)) + 1000
			}
			one, two := h.event(1, 0), h.event(2, 0)
			one.NoteBody = strings.Repeat("x", 4096)
			h.pages.events = []gitlab.Event{one, two}
			_, stop := h.runner.Step(context.Background())
			after := h.state()
			if !stop || after.Status != "capacity" || len(after.Inbox) != 0 || len(after.Observed) != 0 || !after.Watermark.Equal(before.Watermark) || !after.LastCheck.Equal(before.LastCheck) || after.QuickCache != nil {
				t.Fatal(after)
			}
			count := len(h.pages.calls)
			_, stop = h.runner.Step(context.Background())
			if !stop || len(h.pages.calls) != count {
				t.Fatal("capacity pause kept receiving")
			}
			if kind == "inbox" {
				if err := h.m.close(); err != nil {
					t.Fatal(err)
				}
				restored := testManager(t, h.m.store.dir, managerTestProfiles(t))
				restored.notifications.Clock = h.clock.get
				h.m = restored
				h.newRunner()
				_, stop = h.runner.Step(context.Background())
				if s := h.state(); !stop || s.Status != "capacity" || len(s.Inbox) != 0 || !s.Watermark.Equal(before.Watermark) {
					t.Fatal("restart reset capacity pause or progress", s)
				}
			}
		})
	}
}

func TestGitLabEventsPageCommitFailureIsAtomicAndUncertainWriteLatches(t *testing.T) {
	for _, kind := range []string{"before-replace", "after-replace", "sync"} {
		t.Run(kind, func(t *testing.T) {
			h := newEventHarness(t, gitlab.EventsConfig{})
			h.prepare()
			h.clock.add(10 * time.Second)
			h.pages.quick = []gitlab.Event{}
			h.pages.events = []gitlab.Event{h.event(1, 1), h.event(2, 2)}
			h.step()
			originalRename, originalSync, originalCreate := h.m.store.ops.rename, h.m.store.ops.syncDir, h.m.store.ops.createTemp
			commits := 0
			if kind == "before-replace" {
				h.m.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
					commits++
					if commits == 2 {
						return nil, errors.New("disk full")
					}
					return originalCreate(dir, pattern)
				}
			} else if kind == "after-replace" {
				h.m.store.ops.rename = func(from, to string) error {
					commits++
					if err := originalRename(from, to); err != nil {
						return err
					}
					if commits == 2 {
						return errors.New("lost replace result")
					}
					return nil
				}
			} else {
				h.m.store.ops.syncDir = func(path string) error {
					commits++
					if commits == 2 {
						return errors.New("lost sync result")
					}
					return originalSync(path)
				}
			}
			_, stop := h.runner.Step(context.Background()) // checkpoint commits, final page fails
			after := h.state()
			if !stop || len(after.Inbox) != 0 || !after.Watermark.Equal(h.baseline) || !after.LastFull.IsZero() || after.Scan == nil || after.Scan.NextPage != 1 {
				t.Fatal("page made partial in-memory progress", after)
			}
			check := h.m.store.check(true)
			if kind == "before-replace" && check != nil || kind != "before-replace" && !errors.Is(check, ErrCommitUnknown) {
				t.Fatal("wrong uncertainty boundary", check)
			}
			h.m.store.ops = defaultSnapshotOps()
			if err := h.m.close(); err != nil {
				t.Fatal(err)
			}
			reopened := testManager(t, h.m.store.dir, managerTestProfiles(t))
			disk, err := reopened.testEventsState(h.room.RoomIdentity)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "before-replace" {
				if len(disk.Inbox) != 0 || !disk.Watermark.Equal(h.baseline) {
					t.Fatal("failed precommit changed disk", disk)
				}
			} else if len(disk.Inbox) != 2 || !disk.Watermark.After(h.baseline) || disk.Scan != nil {
				t.Fatal("reload did not recover whole committed batch", disk)
			}
		})
	}
}

func TestGitLabEventsCacheIsConfirmedOnlyWithPageCommit(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{ETagVerified: true})
	h.prepare()
	h.clock.add(time.Second)
	h.pages.etag = `"new-page"`
	h.pages.events = []gitlab.Event{h.event(1, 0)}
	original := h.m.store.ops.createTemp
	h.m.store.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, errors.New("precommit disk failure") }
	_, stop := h.runner.Step(context.Background())
	if s := h.state(); !stop || s.QuickCache != nil || !s.LastCheck.IsZero() || len(s.Inbox) != 0 || h.runner.CacheReady {
		t.Fatal("confirmed uncommitted ETag", s)
	}
	h.m.store.ops.createTemp = original
	h.newRunner()
	h.prepare()
	h.step()
	s := h.state()
	if h.pages.calls[len(h.pages.calls)-1].ETag != "" || s.QuickCache == nil || s.QuickCache.ETag != `"new-page"` || len(s.Inbox) != 1 {
		t.Fatal("did not fetch/commit uncached page", s)
	}
}

func TestGitLabEventsConfigurationSnapshotsCannotMutateLiveFilters(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{Filter: gitlab.EventsFilter{Types: []string{"push"}}})
	first := h.state()
	changed, err := h.m.configureEventsSubscription(context.Background(), h.m.testEventsKey(h.room.RoomIdentity), first.SubscriptionVersion, first.Config)
	if err != nil {
		t.Fatal(err)
	}
	changed.Config.Filter.Types[0] = "mutated"
	changed.Filters[0].Filter.Types[0] = "also-mutated"
	if s := h.state(); s.Config.Filter.Types[0] != "push" || s.Filters[0].Filter.Types[0] != "push" {
		t.Fatal("configuration response mutated live filters", s)
	}
}

func TestGitLabEventsDefaultInboxLimitReportsCapacityAndKeepsPageAtomic(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	h.prepare()
	h.clock.add(time.Second)
	seed := h.state()
	// Seed an almost-full valid snapshot using the actual production limit.
	for i := 1; i <= gitlab.MaxEventsInbox-10; i++ {
		if _, _, err := seed.Accept([]gitlab.Event{h.event(int64(i), 0)}, h.clock.get()); err != nil {
			t.Fatal(err)
		}
	}
	h.m.mu.Lock()
	err := h.m.notifications.SaveLocked(seed)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for i := gitlab.MaxEventsInbox; i < gitlab.MaxEventsInbox+20; i++ {
		h.pages.events = append(h.pages.events, h.event(int64(i), 0))
	}
	_, stop := h.runner.Step(context.Background())
	after := h.state()
	if !stop || after.Status != "capacity" || len(after.Inbox) != len(seed.Inbox) || len(after.Observed) != len(seed.Observed) || !after.LastCheck.IsZero() || !after.Watermark.Equal(seed.Watermark) {
		t.Fatal("default hard limit did not preserve the whole page")
	}
}

func TestPlatformNotificationPollingDoesNotCreateChatOrSessions(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	_, before := readManagedRoom(t, h.m, h.room)
	h.pages.events = []gitlab.Event{h.event(1, 1)}
	h.prepare()
	h.clock.add(2 * time.Second)
	h.step()
	h.full()
	if len(h.state().Inbox) != 1 {
		t.Fatal("polling did not persist pending item")
	}
	_, after := readManagedRoom(t, h.m, h.room)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("polling changed chat/agent state")
	}
	if _, err := os.Stat(filepath.Join(h.m.roomDir(h.room.RoomID), "agents")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("polling created private Sessions", err)
	}
}

func TestGitLabEventsCurrentThirtySecondsAndInactiveTwoMinutes(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	h.prepare()
	h.clock.add(time.Second)
	h.step()
	h.full()
	if h.runner.NextQuick.Sub(h.state().LastCheck) != 120*time.Second {
		t.Fatal("inactive interval", h.runner.NextQuick)
	}
	makeEventHarnessActive(t, h)
	h.clock.add(time.Second)
	calls := len(h.pages.calls)
	h.step()
	if len(h.pages.calls) != calls+1 || h.pages.calls[calls].Mode != "quick" || h.runner.NextQuick.Sub(h.clock.get()) != 30*time.Second || !h.runner.NeedFull {
		t.Fatal("activation did not immediately check both paths")
	}
	h.m.mu.Lock()
	delete(h.m.runtimes, h.room.RoomIdentity)
	h.m.mu.Unlock()
	h.clock.add(time.Second)
	h.step()
	if h.runner.NextQuick.Sub(h.clock.get()) != 120*time.Second {
		t.Fatal("old room kept active interval")
	}
}

func TestGitLabEventsRetryHintsAuthPauseAndReconnect(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	h.prepare()
	h.clock.add(time.Second)
	calls := 0
	h.runner.Fetcher = eventFetchFunc(func(context.Context, gitlab.EventQuery) (gitlab.EventBatch, error) {
		calls++
		return gitlab.EventBatch{}, &gitlab.EventQueryError{Code: "query_failed", HTTPStatus: 429, RetryAfterSeconds: 17}
	})
	delay, stop := h.runner.Step(context.Background())
	if stop || delay != 17*time.Second || h.state().Status != "backoff" || !h.state().LastCheck.IsZero() {
		t.Fatal(delay, stop, h.state())
	}
	h.step()
	if calls != 1 {
		t.Fatal("ignored Retry-After")
	}
	if err := h.m.syncEventsSubscription(h.m.testEventsKey(h.room.RoomIdentity), h.state().SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	h.step()
	if calls != 1 {
		t.Fatal("explicit sync bypassed server Retry-After")
	}
	h.clock.add(delay)
	h.runner.Fetcher = eventFetchFunc(func(context.Context, gitlab.EventQuery) (gitlab.EventBatch, error) {
		calls++
		return gitlab.EventBatch{}, &gitlab.EventQueryError{Code: "query_failed", HTTPStatus: 403}
	})
	_, stop = h.runner.Step(context.Background())
	if !stop || h.state().Status != "credentials_invalid" {
		t.Fatal(h.state())
	}
	h.clock.add(time.Hour)
	h.newRunner()
	prepares := 0
	h.m.notifications.Source(gitlab.EventsSourceID).(*gitlab.EventsSource).Prepare = func(context.Context, string, gitlab.Binding) (gitlab.EventFetcher, int64, error) {
		prepares++
		return h.pages, 7, nil
	}
	_, stop = h.runner.Step(context.Background())
	if !stop || prepares != 0 {
		t.Fatal("restart implicitly resumed rejected credentials")
	}
	s := h.state()
	if err := h.m.syncEventsSubscription(h.m.testEventsKey(h.room.RoomIdentity), s.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	h.newRunner()
	h.prepare()
	h.step()
	h.full()
	if h.state().Status != "running" || !h.state().Watermark.After(h.baseline) || !h.state().Baseline.Equal(h.baseline) {
		t.Fatal(h.state())
	}
}
