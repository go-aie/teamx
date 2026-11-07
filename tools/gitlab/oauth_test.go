package main

import (
	"context"
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

func TestOAuthExtensionAuthenticationAndNoWriteReplay(t *testing.T) {
	calls := 0
	a, _, _ := testApp(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer secret-test-token" || r.Header.Get("PRIVATE-TOKEN") != "" {
			t.Error("OAuth header mismatch")
		}
		return &http.Response{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	c, err := a.api()
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.request(context.Background(), "POST", "/api/v4/projects/7/issues/1/notes", map[string]string{"body": "fixture"}, new(any))
	if err == nil || calls != 1 {
		t.Fatal("write replayed", calls, err)
	}
}

// The real pinned executable is opt-in; no external account or network is used.
func TestOAuthPinnedGlab(t *testing.T) {
	bin := os.Getenv("TEAMX_G4_GLAB_BIN")
	if bin == "" {
		t.Skip("set TEAMX_G4_GLAB_BIN to glab 1.127.0")
	}
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v4/user" {
			t.Errorf("unexpected route %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer pinned-fixture-token" || r.Header.Get("PRIVATE-TOKEN") != "" {
			t.Error("real glab did not use OAuth Bearer")
		}
		io.WriteString(w, `{"id":101,"username":"fixture"}`)
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
	if code := a.run(context.Background(), []string{"api", "user"}); code != 0 {
		t.Fatalf("real glab exit %d: %s", code, stderr)
	}
	if !strings.Contains(out.String(), `"username":"fixture"`) {
		t.Fatal("missing real response", out.String())
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected requests", calls.Load())
	}
}
