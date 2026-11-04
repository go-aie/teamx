package room

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func TestPlatformNotificationInboxReloadClearAndContinuation(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	data, err := os.ReadFile("../platform/gitlab/testdata/events.json")
	if err != nil {
		t.Fatal(err)
	}
	var saved gitlab.EventsState
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	saved.RoomIdentity = h.room.RoomIdentity
	saved.SubscriptionID = h.state().SubscriptionID
	for key, entry := range saved.Inbox {
		entry.RoomIdentity = h.room.RoomIdentity
		entry.SubscriptionID = saved.SubscriptionID
		entry.DeliveryID = saved.DeliveryID(key)
		saved.Inbox[key] = entry
	}
	if saved.Validate(h.room.RoomIdentity) != nil {
		t.Fatal("invalid source fixture")
	}
	// Persist through the same atomic envelope writer used in production.
	h.m.mu.Lock()
	err = h.m.notifications.SaveLocked(saved)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	w := testGitLabWorkspace()
	w.Binding = &saved.Binding
	w.Repository.URL = "https://gitlab.invalid/group/project.git"
	setTestEffectiveWorkspace(t, h.m, h.room, w, 1)
	statePath, err := h.m.notifications.StatePathLocked(saved.Metadata().Key)
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	root := h.m.store.dir
	profiles := managerTestProfiles(t)
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	h.m = testManager(t, root, profiles)
	if got := h.state(); !reflect.DeepEqual(got, saved) {
		t.Fatal("reopen reset saved progress", got)
	}
	if after, err := os.ReadFile(statePath); err != nil || string(after) != string(data) {
		t.Fatal("load rewrote saved state", err)
	}
	configPath := filepath.Join(h.m.roomDir(h.room.RoomID), "room-config.json")
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	svc := eventLiveService(t, h)
	key42 := gitlab.EventsKey(saved.Binding, 7, 42)
	post := gitlabEventMessage(saved, saved.Inbox[key42])
	first, err := svc.room.Call[PostReceipt](context.Background(), post)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.room.Call[ClearReceipt](context.Background(), ClearMessages{RoomIdentity: h.room.RoomIdentity, Caller: humanCaller, ThroughSeq: first.Seq}); err != nil {
		t.Fatal(err)
	}
	// Persist the real crash boundary: Room receipt exists, old inbox unacked.
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	h.m = testManager(t, root, profiles)
	svc = eventLiveService(t, h)
	if err := h.m.notifications.Deliver(context.Background(), h.m.testEventsKey(h.room.RoomIdentity), saved.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	_, room := readManagedRoom(t, h.m, h.room)
	if room.LatestSeq != first.Seq || len(room.Messages) != 0 || len(h.state().Inbox) != 1 || room.IntegrationReceipts[post.DeliveryID].Source != post.Source {
		t.Fatal("cleared saved receipt did not deduplicate old pending item", room)
	}
	key43 := gitlab.EventsKey(saved.Binding, 7, 43)
	if err := h.m.notifications.Deliver(context.Background(), h.m.testEventsKey(h.room.RoomIdentity), saved.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	_, room = readManagedRoom(t, h.m, h.room)
	if len(room.Messages) != 1 || room.Messages[0].Source.ItemKey != key43 || room.Messages[0].ClientMessageID != saved.Inbox[key43].DeliveryID || len(room.IntegrationReceipts) != 2 || len(h.state().Inbox) != 0 {
		t.Fatal("old pending item lost its identity", room)
	}
	// Delivery acknowledgements may remove inbox items only; scan, cache,
	// observations, filters, retry bounds and baseline remain untouched.
	want := saved.Clone()
	want.Inbox = map[string]gitlab.EventsInboxEntry{}
	if got := h.state(); !reflect.DeepEqual(got, want) {
		t.Fatal("delivery rewrote source progress", got)
	}
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	h.m = testManager(t, root, profiles)
	eventLiveService(t, h)
	h.clock.now = time.Date(2026, 11, 2, 0, 2, 31, 0, time.UTC)
	h.m.notifications.Clock = h.clock.get
	h.m.notifications.Source(gitlab.EventsSourceID).(*gitlab.EventsSource).Prepare = func(context.Context, string, gitlab.Binding) (gitlab.EventFetcher, int64, error) {
		return h.pages, 7, nil
	}
	for _, entry := range saved.Inbox {
		h.pages.events = append(h.pages.events, entry.Event)
	}
	h.newRunner()
	h.prepare()
	h.step()
	if len(h.pages.calls) != 1 || h.pages.calls[0].Mode != "quick" || h.pages.calls[0].ETag != "" || len(h.state().Inbox) != 0 {
		t.Fatal("restart reused conditional cache or replayed observed events", h.pages.calls)
	}
	h.full()
	if got := h.state(); got.Scan != nil || got.Watermark.Before(saved.Watermark) || got.SubscriptionID != saved.SubscriptionID || !got.Baseline.Equal(saved.Baseline) || len(got.Inbox) != 0 {
		t.Fatal("old scan did not continue", got)
	}
	if after, err := os.ReadFile(configPath); err != nil || string(after) != string(configBefore) {
		t.Fatal("notification recovery changed frozen room config", err)
	}
}
