package room

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform/gitlaboauth"
)

func TestServiceGitLabLocalManagementCLIConfiguresRotatesAndDisables(t *testing.T) {
	home := t.TempDir()
	s := hostedFixture(t, home)
	s.access.config.Deployment.GitLab.Enabled = true
	if err := s.openGitLabApplications(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s)
	defer server.Close()
	s.access.mu.Lock()
	s.access.executionOrigin = server.URL
	s.access.mu.Unlock()
	config := filepath.Join(home, "admin-config.toml")
	body := fmt.Sprintf("[server]\nlisten = %q\npublic_base_url = %q\n[deployment]\nmode = \"multi-user\"\n[deployment.gitlab]\nenabled = true\n", strings.TrimPrefix(server.URL, "http://"), s.access.public)
	if err := os.WriteFile(config, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "application-input.json")
	input := map[string]any{"version": 0, "application": map[string]any{"name": "Company", "origin": "https://gitlab.example", "owner_id": 999, "client_id": "service-client", "secret": "only-control-secret"}}
	save := func() {
		t.Helper()
		data, _ := json.Marshal(input)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	run := func(args ...string) []byte {
		t.Helper()
		var out bytes.Buffer
		cmd := append([]string{"access", "gitlab-applications"}, args...)
		cmd = append(cmd, "--config", config)
		if err := RunWithCatalog(cmd, &out, io.Discard, defaultPlatformCatalog()); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "only-control-secret") || strings.Contains(out.String(), "rotated-control-secret") {
			t.Fatal("CLI exposed secret")
		}
		return out.Bytes()
	}
	data := run("configure", "--input", path)
	var app gitlaboauth.Application
	if json.Unmarshal(data, &app) != nil || app.Version != 1 || app.Source != gitlaboauth.ServiceSource || app.Secret != "" {
		t.Fatal("configuration receipt")
	}
	app.Secret = "rotated-control-secret"
	input = map[string]any{"version": 1, "application": app}
	save()
	data = run("configure", "--input", path)
	if json.Unmarshal(data, &app) != nil || app.Version != 2 {
		t.Fatal("rotation receipt")
	}
	app.Secret = ""
	app.Status = "disabled"
	input = map[string]any{"version": 2, "application": app}
	save()
	run("configure", "--input", path)
	run("settle", "--application", app.ID)
	data = run("list")
	if !strings.Contains(string(data), `"disabled"`) {
		t.Fatal("disabled state missing")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := runGitLabApplicationManagement([]string{"configure", "--config", config, "--input", path}, io.Discard, io.Discard); err == nil {
		t.Fatal("world-readable secret input accepted")
	}
}
