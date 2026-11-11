//go:build integration || e2e

package room

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestTodosAppHandler(t *testing.T) {
	parallelFixtureTest(t)
	m, room := todosAppManager(t)
	runAppHandler(t, m, "../web/tests/web_todos_http.mjs")
	checkTodosAppResult(t, m, room)
}

func TestTodosAppHTTP(t *testing.T) {
	parallelFixtureTest(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required")
	}
	m, room := todosAppManager(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: m}}
	server.Start()
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
