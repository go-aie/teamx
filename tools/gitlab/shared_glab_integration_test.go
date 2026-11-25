//go:build integration || e2e

package main

import (
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

func TestSharedPinnedGlabUsesSelectedHostWithoutGit(t *testing.T) {
	bin := os.Getenv("TEAMX_G4_GLAB_BIN")
	if bin == "" {
		t.Skip("set TEAMX_G4_GLAB_BIN to the pinned glab executable")
	}
	t.Run("native", func(t *testing.T) {
		checkSharedPinnedGlab(t, bin)
	})
	t.Run("packaged-launcher", func(t *testing.T) {
		checkSharedPinnedGlab(t, packagedGlab(t, bin))
	})
}

func checkSharedPinnedGlab(t *testing.T, bin string) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v4/user" || r.Header.Get("Authorization") != "Bearer shared-fixture-token" {
			t.Errorf("unexpected target/account %s", r.URL.Path)
		}
		io.WriteString(w, `{"id":101,"username":"shared-fixture"}`)
	}))
	defer server.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	gitCalls := filepath.Join(dir, "git-calls")
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+gitCalls+"'\nexit 77\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SSL_CERT_FILE", ca)
	a, out, stderr := testApp(t, nil)
	a.shared = true
	a.getenv = func(k string) string {
		switch k {
		case "TEAMX_GLAB_BIN":
			return bin
		case "GITLAB_HOST":
			return server.URL
		case "GITLAB_TOKEN":
			return "shared-fixture-token"
		}
		return ""
	}
	if code := a.run(t.Context(), []string{"api", "user"}); code != 0 || !strings.Contains(out.String(), "shared-fixture") {
		t.Fatal(code, out.String(), stderr.String())
	}
	if data, err := os.ReadFile(gitCalls); !os.IsNotExist(err) {
		t.Fatalf("native API executed host Git: %s (%v)", data, err)
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected API retries", calls.Load())
	}
}
