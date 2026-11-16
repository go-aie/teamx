//go:build e2e

package room

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebUpdatesBrowserSubscription(t *testing.T) {
	if os.Getenv("TEAMX_WEB_UPDATES_BROWSER") != "1" {
		t.Skip("optional Chromium EventSource acceptance")
	}
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprint("managed=", managed), func(t *testing.T) {
			svc, m, _, server, _, token := updateHTTPService(t, managed)
			epoch := svc.info.ServiceEpoch
			if m != nil {
				epoch = m.epoch
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "node", "testdata/web_updates_subscription.cjs", server.URL, updatesURL(epoch, svc.info))
			cmd.Env = append(os.Environ(), "TEAMX_WEB_UPDATES_TEST_TOKEN="+token)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("EventSource: %v\n%s", err, output)
			}
			t.Log(string(output))
		})
	}
}

// Optional real-browser measurement against the local test manager. It never
// contacts a model or an external provider; login is tested separately above.
func TestWebUpdatesPollingBaseline(t *testing.T) {
	if os.Getenv("TEAMX_WEB_UPDATES_BASELINE") != "1" {
		t.Skip("optional Chromium polling baseline")
	}
	t.Setenv("USER", humanCaller.ID)
	m := directTestManager(t, t.TempDir(), testTeamDefinition(t).Runtime)
	ensureAssistant(t, m)
	assistant := assistantSummary(t, m)
	m.factory = controlledActivationFactory(nil, nil)
	openRoomOK(t, m, assistant, "baseline-open")
	server := browserTestServer(t, m)
	defer server.Close()
	m.config.endpoint = server.URL
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/web_updates_baseline.cjs", server.URL)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("baseline: %v\n%s", err, output)
	}
	t.Log(string(output))
}

// Production application and native EventSource against isolated real services.
// Providers are controlled; this test never contacts a model or remote platform.
func TestWebUpdatesApplicationBrowser(t *testing.T) {
	if os.Getenv("TEAMX_WEB_UPDATES_BROWSER") != "1" {
		t.Skip("optional Chromium application update acceptance")
	}
	t.Setenv("USER", humanCaller.ID)
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprint("managed=", managed), func(t *testing.T) {
			var handler http.Handler
			if managed {
				m := directTestManager(t, t.TempDir(), testTeamDefinition(t).Runtime)
				ensureAssistant(t, m)
				m.factory = controlledActivationFactory(nil, nil)
				openRoomOK(t, m, assistantSummary(t, m), "updates-open")
				handler = m
			} else {
				svc, err := newService(t.TempDir(), "updates-browser", serviceConfig{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { closeService(t, svc) })
				handler = svc
			}
			server := browserTestServer(t, handler)
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "node", "testdata/web_updates_application.cjs", server.URL)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("application: %v\n%s", err, output)
			}
			t.Log(string(output))
		})
	}
}

func TestWebUpdatesModelOutcomeBrowser(t *testing.T) {
	if os.Getenv("TEAMX_WEB_UPDATES_BROWSER") != "1" {
		t.Skip("optional Chromium model outcome acceptance")
	}
	t.Setenv("USER", humanCaller.ID)
	manager, _, _ := onlineModelManager(t)
	var writes atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models/change" && r.Method == http.MethodPost {
			writes.Add(1)
		}
		manager.ServeHTTP(w, r)
	})
	server := browserTestServer(t, manager, handler)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/web_updates_model_outcome.cjs", server.URL)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("model outcome: %v\n%s", err, output)
	}
	if writes.Load() != 1 {
		t.Fatalf("model change replayed: %d", writes.Load())
	}
	t.Log(string(output))
}

func TestWebUpdatesAuthenticationBrowser(t *testing.T) {
	if os.Getenv("TEAMX_WEB_UPDATES_BROWSER") != "1" {
		t.Skip("optional Chromium authentication recovery")
	}
	_, _, _, server, _, token := updateHTTPService(t, false)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/web_updates_authentication.cjs", server.URL)
	cmd.Env = append(os.Environ(), "TEAMX_WEB_UPDATES_TEST_TOKEN="+token)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("authentication: %v\n%s", err, output)
	}
	t.Log(string(output))
}

// Reconstruct the manager from the same isolated durable directory while the
// production page remains open. Access middleware remains the same front door.
func TestWebUpdatesRestartBrowser(t *testing.T) {
	if os.Getenv("TEAMX_WEB_UPDATES_BROWSER") != "1" {
		t.Skip("optional Chromium restart recovery")
	}
	t.Setenv("USER", humanCaller.ID)
	root, defaults := t.TempDir(), testTeamDefinition(t).Runtime
	manager := directTestManager(t, root, defaults)
	ensureAssistant(t, manager)
	manager.factory = controlledActivationFactory(nil, nil)
	openRoomOK(t, manager, assistantSummary(t, manager), "restart-before")
	var active atomic.Pointer[roomManager]
	active.Store(manager)
	restart := make(chan chan error)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__test/restart" && r.Method == "POST" {
			result := make(chan error, 1)
			select {
			case restart <- result:
			case <-r.Context().Done():
				return
			}
			select {
			case err := <-result:
				if err != nil {
					http.Error(w, err.Error(), 500)
				} else {
					_, _ = w.Write([]byte("{}"))
				}
			case <-r.Context().Done():
			}
			return
		}
		active.Load().ServeHTTP(w, r)
	})
	server := browserTestServer(t, manager, handler)
	access := manager.config.access
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/web_updates_restart.cjs", server.URL)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case result := <-restart:
			if err := manager.close(); err != nil {
				result <- err
				continue
			}
			manager = directTestManager(t, root, defaults)
			manager.config.access = access
			manager.factory = controlledActivationFactory(nil, nil)
			openRoomOK(t, manager, assistantSummary(t, manager), "restart-after")
			active.Store(manager)
			result <- nil
		case err := <-done:
			if err != nil {
				t.Fatalf("restart browser: %v\n%s", err, output.String())
			}
			t.Log(output.String())
			return
		}
	}
}
