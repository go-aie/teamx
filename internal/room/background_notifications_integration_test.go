//go:build integration || e2e

package room

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

// Real notification worker, durable store and Room delivery; remote events
// are a fixture. The browser connection never owns the collector context.
func TestNotificationWorkerSurvivesWebClientDisconnect(t *testing.T) {
	parallelFixtureTest(t)
	h := newEventHarness(t, gitlab.EventsConfig{})
	svc := eventLiveService(t, h)
	entered, release, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce, cancelOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(finish)
	event := h.event(1, 0)
	h.m.notifications.Source(gitlab.EventsSourceID).(*gitlab.EventsSource).Prepare = func(context.Context, string, gitlab.Binding) (gitlab.EventFetcher, int64, error) {
		return eventFetchFunc(func(ctx context.Context, _ gitlab.EventQuery) (gitlab.EventBatch, error) {
			enterOnce.Do(func() { close(entered) })
			select {
			case <-ctx.Done():
				cancelOnce.Do(func() { close(canceled) })
				return gitlab.EventBatch{}, ctx.Err()
			case <-release:
				return gitlab.EventBatch{Version: 1, Events: []gitlab.Event{event}, Complete: true}, nil
			}
		}), 7, nil
	}
	h.clock.add(time.Second)
	if err := h.m.notifications.Start(); err != nil {
		t.Fatal(err)
	}
	awaitEventSignal(t, entered, "collector did not start")
	web := httptest.NewServer(h.m)
	defer web.Close()
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	response, err := client.Get(web.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	response.Body.Close()
	transport.CloseIdleConnections()
	if response.StatusCode != http.StatusOK || readErr != nil {
		t.Fatal("web request failed", response.StatusCode, readErr)
	}
	// Complete the source query after every client connection has gone away.
	finish()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-canceled:
			t.Fatal("client disconnect canceled collector")
		default:
		}
		saved, err := svc.store.LoadRoom()
		state := h.state()
		if err == nil && len(saved.Messages) == 1 && len(state.Inbox) == 0 && len(state.Observed) == 1 {
			if saved.Messages[0].Author.Kind != "integration" {
				t.Fatal("wrong delivery author")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("collector did not durably deliver after web client disconnected")
}
