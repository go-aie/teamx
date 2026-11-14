//go:build e2e

package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// Exercises the real pinned executable against a local TLS fixture.
func TestOAuthPinnedGlab(t *testing.T) {
	bin := os.Getenv("TEAMX_G4_GLAB_BIN")
	if bin == "" {
		t.Skip("set TEAMX_G4_GLAB_BIN to glab 1.127.0")
	}
	// TeamX's process allowlist excludes coding-agent detection variables.
	// Clear them here too: otherwise running this test from Codex suppresses
	// glab's release banner and hides the production JSON parsing failure.
	for _, key := range []string{"AI_AGENT", "CLAUDECODE", "CODEX_THREAD_ID", "COPILOT_CLI", "CURSOR_AGENT", "GEMINI_CLI", "OPENCODE", "ROO_CLI_RUNTIME", "TERM_PROGRAM"} {
		t.Setenv(key, "")
	}
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer pinned-fixture-token" || r.Header.Get("PRIVATE-TOKEN") != "" {
			t.Error("real glab did not use OAuth Bearer")
		}
		switch r.URL.Path {
		case "/api/v4/user":
			io.WriteString(w, `{"id":101,"username":"fixture"}`)
		case "/api/v4/projects/group/project/issues":
			q := r.URL.Query()
			if q.Get("search") != "browser" || q.Get("milestone") != "0.11.0" {
				t.Errorf("unexpected issue search %s", r.URL.RawQuery)
			}
			io.WriteString(w, `[{"iid":4244,"title":"Browser use","state":"opened"}]`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", ca)
	a, out, stderr := testApp(t, nil)
	a.getenv = func(key string) string {
		switch key {
		case "GITLAB_HOST":
			return server.URL
		case "GITLAB_TOKEN":
			return "pinned-fixture-token"
		case "TEAMX_GLAB_BIN":
			return bin
		}
		return ""
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"api", []string{"api", "user"}, `"username":"fixture"`},
		{"issue-search", []string{"issue", "list", "--repo", "group/project", "--search", "browser", "--milestone", "0.11.0", "--all", "--output", "json"}, `"iid":4244`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Each invocation gets a fresh glab config. Even consumers that merge
			// stderr into stdout must receive a single JSON document, without a
			// first-run release banner appended to a successful response.
			out.Reset()
			stderr.Reset()
			if code := a.run(context.Background(), tc.args); code != 0 {
				t.Fatalf("real glab exit %d: %s", code, stderr)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatal("missing real response", out.String())
			}
			if combined := out.String() + stderr.String(); !json.Valid([]byte(combined)) {
				t.Fatalf("successful JSON output contains non-JSON text: %q", combined)
			}
		})
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected requests", calls.Load())
	}
}
