//go:build integration || e2e

package room

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestNotificationOwnerBlockedRoomAllowsOtherRoomHTTPAndDelivery(t *testing.T) {
	for _, boundary := range []string{"source-save", "target-receipt"} {
		t.Run(boundary, func(t *testing.T) {
			h, c := newIndependentEventHarness(t)
			h.m.config.unifiedInputs = true
			first := eventLiveService(t, h)
			h.m.bindPlatformReferences(first)
			otherRoom := managerCreate(t, h.m, "independent-delivery")
			cfg := h.state().Config
			cfg.PlatformID = c.ID
			cfg.Target = fixtureInboxTarget(t, h.m, otherRoom.RoomIdentity)
			state, err := h.m.configureEventsSubscription(t.Context(), notification.Key{Owner: otherRoom.RoomIdentity, Source: gitlab.EventsSourceID}, 0, cfg)
			if err != nil {
				t.Fatal(err)
			}
			other := &eventHarness{t: t, m: h.m, room: otherRoom, clock: h.clock, baseline: state.Baseline}
			second := eventLiveService(t, other)
			h.m.bindPlatformReferences(second)
			seedEventInbox(t, h, 1)
			seedEventInbox(t, other, 2)
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			done := make(chan error, 1)
			if boundary == "source-save" {
				save := h.m.store.ops.syncDir
				h.m.store.ops.syncDir = func(dir string) error {
					if dir == filepath.Join(h.m.roomDir(h.room.RoomID), "notifications") {
						close(entered)
						<-release
					}
					return save(dir)
				}
				previous := h.state()
				next := previous.Clone()
				next.LastCheck = h.clock.get()
				go func() { done <- h.m.notifications.SaveFrom(next, previous) }()
			} else {
				save := first.store.ops.syncDir
				var once sync.Once
				first.store.ops.syncDir = func(dir string) error { once.Do(func() { close(entered); <-release }); return save(dir) }
				source := h.state()
				go func() {
					done <- h.m.notifications.Deliver(t.Context(), source.Metadata().Key, source.SubscriptionVersion)
				}()
			}
			select {
			case <-entered:
			case err := <-done:
				t.Fatal("missed blocked boundary", err)
			case <-time.After(5 * time.Second):
				t.Fatal("boundary not reached")
			}
			independent := make(chan error, 1)
			go func() {
				base := other.state()
				next := base.Clone()
				next.LastCheck = h.clock.get()
				if err := h.m.notifications.SaveFrom(next, base); err != nil {
					independent <- err
					return
				}
				if err := h.m.notifications.Deliver(t.Context(), base.Metadata().Key, base.SubscriptionVersion); err != nil {
					independent <- err
					return
				}
				samples := make([]time.Duration, 0, 100)
				for range 100 {
					start := time.Now()
					for _, room := range []roomSummary{h.room, otherRoom} {
						q := url.Values{"service_epoch": {h.m.epoch}, "caller_id": {"operator"}, "room_id": {room.RoomID}, "room_instance": {room.RoomInstance}}
						for _, path := range []string{"/v1/rooms", "/v1/platform/notifications?" + q.Encode()} {
							response := httptest.NewRecorder()
							h.m.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
							if response.Code != 200 {
								independent <- fmt.Errorf("%s: HTTP %d", path, response.Code)
								return
							}
						}
					}
					samples = append(samples, time.Since(start))
				}
				slices.Sort(samples)
				t.Logf("before release: 400/400 HTTP 200; 100 rounds p50=%s p95=%s p99=%s", samples[49], samples[94], samples[98])
				independent <- nil
			}()
			select {
			case err := <-independent:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("other Room did not complete before release")
			}
			if len(other.state().Inbox) != 0 {
				t.Fatal("other Room acknowledgement did not install")
			}
			unblock()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("accepted operation did not settle")
			}
		})
	}
}

// The helper exits from the source snapshot writer after target publication.
// Parent recovery must use disk authorities and acquire fresh directory locks.
func TestNotificationOwnerReceiptCrashRecovery(t *testing.T) {
	const childKey = "TEAMX_NOTIFICATION_RECEIPT_CRASH_ROOT"
	if root := os.Getenv(childKey); root != "" {
		h, _ := independentEventHarnessAt(t, root)
		h.m.config.unifiedInputs = true
		svc := eventLiveService(t, h)
		h.m.bindPlatformReferences(svc)
		seedEventInbox(t, h, 1)
		source := h.state()
		create := h.m.store.ops.createTemp
		h.m.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
			if dir == filepath.Join(h.m.roomDir(h.room.RoomID), "notifications") {
				os.Exit(86)
			}
			return create(dir, pattern)
		}
		_ = h.m.notifications.Deliver(t.Context(), source.Metadata().Key, source.SubscriptionVersion)
		t.Fatal("helper did not reach the source acknowledgement boundary")
	}
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), executable, "-test.run=^TestNotificationOwnerReceiptCrashRecovery$", "-test.timeout=30s")
	command.Env = append(os.Environ(), childKey+"="+root)
	output, err := command.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 86 {
		t.Fatalf("unexpected helper exit: %v %s", err, output)
	}
	m := testManager(t, root, managerTestProfiles(t))
	m.config.unifiedInputs = true
	clock := &eventTestClock{now: time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)}
	m.notifications.Clock = clock.get
	if len(m.index.Rooms) != 1 {
		t.Fatal("crashed Room missing")
	}
	room := m.index.Rooms[0].roomSummary
	h := &eventHarness{t: t, m: m, room: room, clock: clock}
	source := h.state()
	if len(source.Inbox) != 1 {
		t.Fatal("source acknowledgement appeared without commit")
	}
	svc := eventLiveService(t, h)
	m.bindPlatformReferences(svc)
	id := subscriptionMember(source.Metadata())
	p, err := svc.inputStorage(id)
	if err != nil {
		t.Fatal(err)
	}
	before, err := p.previewHistory("")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.notifications.Deliver(t.Context(), source.Metadata().Key, source.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	after, err := p.previewHistory("")
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Entries) != 1 || len(after.Entries) != 1 || len(h.state().Inbox) != 0 || before.Entries[0].Partition != after.Entries[0].Partition || before.Entries[0].Seq != after.Entries[0].Seq {
		t.Fatal("receipt replay duplicated or lost source confirmation", len(before.Entries), len(after.Entries))
	}
	t.Log("process exited after target publication; fresh owners replayed the original receipt and acknowledged the source, one retained body")
}
