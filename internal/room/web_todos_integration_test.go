//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The full Todos application flow runs over TCP below. This transport contract
// needs only an HTTP handler, without a Room, credentials or notification worker.
func TestAppHandlerBridge(t *testing.T) {
	calls := 0
	runAppHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/echo":
			if r.Method != http.MethodPost || r.URL.Query().Get("q") != "中文 & 空格" {
				t.Error("request encoding changed", r.Method, r.URL)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(body)
		case "/read":
			if r.Method != http.MethodGet {
				t.Error("null body changed GET", r.Method)
			}
			_, _ = io.WriteString(w, `{"ok":true}`)
		case "/reject":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":"身份已失效","code":"identity_mismatch"}`)
		default:
			t.Error("unexpected bridge request", r.URL)
			w.WriteHeader(404)
		}
	}), "../web/tests/web_handler_bridge.mjs")
	if calls != 3 {
		t.Fatal("bridge omitted or repeated requests", calls)
	}
}

func TestTodosAppHTTP(t *testing.T) {
	parallelFixtureTest(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required")
	}
	m, room := todosAppManager(t)
	server := browserTestServer(t, m)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "../web/tests/web_todos_http.mjs")
	cmd.Env = append(os.Environ(), "TEAMX_WEB_ENDPOINT="+server.URL)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("app/HTTP: %v\n%s", err, output)
	}
	checkTodosAppResult(t, m, room)
	if *webPreviewDuration > 0 {
		t.Log("isolated Todos browser preview:", server.URL)
		<-time.After(*webPreviewDuration)
	}
}

// An intentional Fatal must fail the child and still finish manager cleanup.
func TestTodosAppAssertionFailureClosesManager(t *testing.T) {
	const key = "TEAMX_TODOS_ASSERTION_CLEANUP"
	if marker := os.Getenv(key); marker != "" {
		t.Cleanup(func() {
			if err := os.WriteFile(marker, []byte("closed"), 0600); err != nil {
				t.Error(err)
			}
		})
		m := testManager(t, t.TempDir(), managerTestProfiles(t))
		room := managerCreate(t, m, "assertion-failure")
		checkTodosAppResult(t, m, room)
		return
	}
	marker := filepath.Join(t.TempDir(), "cleanup")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTodosAppAssertionFailureClosesManager$")
	cmd.Env = append(os.Environ(), key+"="+marker)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), "platform subscriptions were replaced") {
		t.Fatalf("expected bounded assertion failure: %v, context=%v\n%s", err, ctx.Err(), out)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "closed" {
		t.Fatalf("manager cleanup did not finish: %q, %v\n%s", got, err, out)
	}
}
