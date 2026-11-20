//go:build e2e

package room

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Real native Lark CLI fixture + GitLab source runner, Store, Actors, HTTP and
// Chromium. No external platform or model calls. The clock advances between
// actual source steps; neither source is a constant/frozen state fixture.
func TestWebUpdatesNotificationScopeBrowser(t *testing.T) {
	if os.Getenv("TEAMX_WEB_UPDATES_SCOPE_BROWSER") != "1" {
		t.Skip("optional active-collector and independent-session browser acceptance")
	}
	runWebUpdatesNotificationScopeBrowser(t, false)
}

func TestWebUpdatesNotificationScopeLongWindowBrowser(t *testing.T) {
	if os.Getenv("TEAMX_WEB_UPDATES_SCOPE_LONG") != "1" {
		t.Skip("set TEAMX_WEB_UPDATES_SCOPE_LONG=1 for real sixty-second collector measurement")
	}
	runWebUpdatesNotificationScopeBrowser(t, true)
}

func runWebUpdatesNotificationScopeBrowser(t *testing.T, longWindow bool) {
	t.Helper()
	t.Setenv("USER", humanCaller.ID)
	h := newLarkHarness(t)
	h.messages(0)
	h.start()
	m := h.m
	m.mu.Lock()
	platforms, err := m.platforms.Load()
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	workspace := testGitLabWorkspace()
	for _, c := range platforms.Platforms {
		if c.Provider == "gitlab" {
			workspace.Binding.PlatformID = c.ID
			workspace.Binding.PlatformGeneration = c.Generation
		}
	}
	setTestEffectiveWorkspace(t, m, h.room, workspace, 1)
	pages := &eventPages{}
	m.notifications.Source(gitlab.EventsSourceID).(*gitlab.EventsSource).Prepare = func(context.Context, string, gitlab.Binding) (gitlab.EventFetcher, int64, error) {
		return pages, 7, nil
	}
	event, err := seedTestEvents(m, h.room.RoomIdentity, gitlab.EventsConfig{Enabled: true, Target: inboxTarget(notificationMemberIdentity(h.room.RoomIdentity, h.members[0])), IntervalSeconds: 3})
	if err != nil {
		t.Fatal(err)
	}
	state := h.state()
	m.mu.Lock()
	runners := []notification.Runner{m.notifications.NewRunnerLocked(event.Metadata().Key), m.notifications.NewRunnerLocked(state.Metadata().Key)}
	m.mu.Unlock()
	step := func() {
		h.clock.add(3 * time.Second)
		for _, runner := range runners {
			if _, stop := runner.Step(t.Context()); stop && t.Context().Err() == nil {
				t.Error("collector stopped")
			}
		}
	}
	for range 12 {
		step()
	}
	other := managerCreate(t, m, "scope-other")
	otherSvc := eventLiveService(t, &eventHarness{m: m, room: other})
	// Both authenticated sessions belong to the configured operator, as two
	// devices would. Each receives its own server-side session and queue.
	access, token, _ := accessFixture(t)
	access.operator = humanCaller.ID
	m.config.access = access
	var steps atomic.Uint64
	// The successful atomic rename precedes directory fsync and Actor state
	// installation. Measuring from here gives a conservative DOM upper bound.
	var messageInstalled, notificationInstalled atomic.Int64
	h.svc.store.mu.Lock()
	renameRoom := h.svc.store.ops.rename
	h.svc.store.ops.rename = func(from, to string) error {
		err := renameRoom(from, to)
		if err == nil && filepath.Base(to) == "room.json" {
			messageInstalled.Store(time.Now().UnixMilli())
		}
		return err
	}
	h.svc.store.mu.Unlock()
	m.mu.Lock()
	renameSource := m.store.ops.rename
	m.store.ops.rename = func(from, to string) error {
		err := renameSource(from, to)
		if err == nil && strings.HasPrefix(filepath.Base(to), "gitlab.events.") {
			notificationInstalled.Store(time.Now().UnixMilli())
		}
		return err
	}
	m.mu.Unlock()
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				step()
				steps.Add(1)
			}
		}
	}()
	t.Cleanup(func() { close(done); <-exited })
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__scope/info":
			calls := map[string]uint64{}
			m.mu.Lock()
			for _, state := range m.notifications.StatesLocked(h.room.RoomIdentity) {
				switch s := state.(type) {
				case gitlab.EventsState:
					calls["gitlab"] = s.Metrics.Calls
				case lark.State:
					calls["lark"] = s.Metrics.Calls
				}
			}
			m.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"calls": calls, "current": h.svc.info, "other": otherSvc.info, "platform": h.c.ID, "member": h.members[0], "steps": steps.Load()})
		case "/__scope/error", "/__scope/recover":
			m.mu.Lock()
			s, ok := m.eventsSubscriptionLocked(event.Metadata().Key)
			if !ok {
				m.mu.Unlock()
				http.Error(w, "missing", 500)
				return
			}
			// Delivery errors survive later successful empty queries, just like an
			// unavailable destination. This exercises an installed business change.
			s.DeliveryErrorCode = "target_unavailable"
			if strings.HasSuffix(r.URL.Path, "recover") {
				s.DeliveryErrorCode = ""
			}
			err := m.notifications.SaveLocked(s)
			installed := notificationInstalled.Load()
			m.mu.Unlock()
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"installed": installed})
		case "/__scope/message":
			receipt, err := h.svc.room.Call[PostReceipt](r.Context(), PostMessage{RoomIdentity: h.svc.info.RoomIdentity, Caller: Caller{Author: Author{Kind: "human", ID: "peer"}}, ClientMessageID: r.URL.Query().Get("id"), Text: r.URL.Query().Get("text")})
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"seq": receipt.Seq, "installed": messageInstalled.Load()})
		default:
			m.ServeHTTP(w, r)
		}
	})
	server := httptest.NewUnstartedServer(access.wrap(handler))
	endpoint := "http://" + server.Listener.Addr().String()
	access.internal, access.public, access.activeListen = endpoint, endpoint, server.Listener.Addr().String()
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	mode := "functional"
	if longWindow {
		mode = "long"
	}
	cmd := exec.CommandContext(ctx, "node", "testdata/web_updates_notification_scope.cjs", server.URL, mode)
	cmd.Env = append(os.Environ(), "TEAMX_WEB_UPDATES_TEST_TOKEN="+token)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("active collectors: %v\n%s", err, output)
	}
	t.Log(string(output))
	if longWindow && steps.Load() < 60 {
		t.Fatal("active collection did not span sixty seconds", steps.Load())
	}
}
