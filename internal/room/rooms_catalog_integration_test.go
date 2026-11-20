//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Stop at real persistence boundaries while the existing notification Gate is
// held. The list requests must finish before the blocked writer is released.
func TestRoomsCatalogDuringBlockedNotificationWork(t *testing.T) {
	for _, boundary := range []string{"source-save", "private-maintenance"} {
		t.Run(boundary, func(t *testing.T) {
			h := newEventHarness(t, gitlab.EventsConfig{})
			h.m.config.unifiedInputs = true
			svc := eventLiveService(t, h)
			policy := notification.ExpiryPolicy{AfterDays: 3, SkipExpired: true}
			svc.inboxExpiry = policy
			seedEventInbox(t, h, 1)
			state := h.state()
			if err := h.m.notifications.Deliver(t.Context(), state.Metadata().Key, state.SubscriptionVersion); err != nil {
				t.Fatal(err)
			}
			state = h.state()
			h.clock.add(4 * 24 * time.Hour)
			server := httptest.NewServer(h.m)
			defer server.Close()
			measure := func() {
				durations := make([]time.Duration, 0, 100)
				for range 100 {
					started := time.Now()
					ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
					req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/rooms", nil)
					response, err := server.Client().Do(req)
					if err != nil {
						cancel()
						t.Fatal(err)
					}
					var catalog roomsResponse
					err = json.NewDecoder(response.Body).Decode(&catalog)
					response.Body.Close()
					cancel()
					if response.StatusCode != 200 || err != nil || len(catalog.Rooms) != 1 || catalog.Rooms[0].RoomIdentity != h.room.RoomIdentity || catalog.Rooms[0].SummaryStatus != "ready" {
						t.Fatalf("list: status=%d err=%v catalog=%+v", response.StatusCode, err, catalog)
					}
					durations = append(durations, time.Since(started))
				}
				slices.Sort(durations)
				t.Logf("100/100 HTTP 200: p50=%s p95=%s p99=%s max=%s", durations[49], durations[94], durations[98], durations[99])
			}
			t.Log("without blocked work")
			measure()
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			var once sync.Once
			unblock := sync.OnceFunc(func() {
				close(release)
				if boundary == "private-maintenance" {
					svc.store.mu.Unlock()
				}
			})
			defer unblock()
			store := h.m.store
			if boundary == "private-maintenance" {
				// Freeze local storage at maintenance entry. The existing maintenance
				// path holds manager.mu while waiting for this persistence gate.
				svc.store.mu.Lock()
			}
			original := store.ops.syncDir
			defer func() {
				unblock()
				select {
				case <-done:
					store.ops.syncDir = original
				case <-time.After(5 * time.Second):
					t.Error("blocked notification work did not settle during cleanup")
				}
			}()
			store.ops.syncDir = func(dir string) error { once.Do(func() { close(entered); <-release }); return original(dir) }
			go func() {
				defer close(done)
				if boundary == "source-save" {
					next := state.Clone()
					next.LastCheck = h.clock.get()
					done <- h.m.notifications.SaveLocked(next)
				} else {
					once.Do(func() { close(entered) })
					done <- h.m.maintainMemberInbox(context.Background(), svc, MemberIdentity{RoomIdentity: state.RoomIdentity, AgentID: state.Config.Target.AgentID, MembershipID: state.Config.Target.MembershipID})
				}
			}()
			select {
			case <-entered:
			case err := <-done:
				t.Fatalf("did not reach persistence: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("writer did not reach hook")
			}
			t.Log("while blocked, before release")
			measure()
			select {
			case err := <-done:
				t.Fatalf("writer escaped hook: %v", err)
			default:
			}
			unblock()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A slow connection is allowed to retain its immutable response, never mu.
func TestRoomsCatalogEncodingDoesNotHoldManager(t *testing.T) {
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	managerCreate(t, m, "slow-reader")
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	w := &blockedCatalogWriter{header: http.Header{}, entered: entered, release: release}
	go func() { defer close(done); m.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rooms", nil)) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("encoding never reached writer")
	}
	if !m.mu.TryLock() {
		t.Fatal("response encoding holds manager")
	}
	m.mu.Unlock()
	if response := managerHTTP(m, "/v1/rooms", nil); response.Code != 200 {
		t.Fatal(response.Code)
	}
	unblock()
	<-done
}

type blockedCatalogWriter struct {
	header  http.Header
	entered chan struct{}
	release chan struct{}
}

func (w *blockedCatalogWriter) Header() http.Header { return w.header }
func (w *blockedCatalogWriter) WriteHeader(int)     {}
func (w *blockedCatalogWriter) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.release
	return io.Discard.Write(p)
}
