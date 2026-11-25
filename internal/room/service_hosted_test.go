//go:build darwin || linux

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestServiceHostedInstallGuidesAccountLoginWithoutPrintingTokenCommand(t *testing.T) {
	home := t.TempDir()
	serviceTestFile(t, filepath.Join(home, "config.toml"), "[server]\naccess_mode='proxy'\npublic_base_url='https://assistant.example.com'\ntrusted_proxies=['127.0.0.1']\n[deployment]\nmode='multi-user'\n[deployment.authentication]\nprovider='opencsg'\nallowed_subjects=['subject']\n", 0600)
	var stdout, stderr bytes.Buffer
	if err := writeServiceInstallResult(serviceRegistration{Home: home}, map[string]any{"authenticated": true}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "https://assistant.example.com") || !strings.Contains(stderr.String(), "使用部署配置的账号登录") || strings.Contains(stderr.String(), "cat '") {
		t.Fatal("incorrect multi-user login guidance", stderr.String())
	}
}

func TestHostedDeploymentStatusRequiresLocalAdministration(t *testing.T) {
	s := hostedFixture(t, t.TempDir())
	s.access.installationID = "installed-hosted-instance"
	token, err := readAdminToken(adminTokenPath(s.access.config))
	if err != nil {
		t.Fatal(err)
	}
	// No users, rooms or model are needed for the deployment to accept SSO.
	w := hostedRequest(s, "/v1/deployment/status", nil, nil, token)
	var status map[string]any
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &status) != nil || status["installation_id"] != s.access.installationID || status["ready"] != true {
		t.Fatalf("deployment status: %d %s", w.Code, w.Body.String())
	}
	for _, forbidden := range []string{"operator", "users", "spaces", "models"} {
		if _, ok := status[forbidden]; ok {
			t.Fatalf("business data in deployment status: %s", forbidden)
		}
	}
	cookie, _, _ := hostedLogin(t, s, "alice")
	for _, variant := range []string{"anonymous", "browser", "origin", "forwarded", "remote", "wrong-token", "post"} {
		t.Run(variant, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "https://teamx.example/v1/deployment/status", nil)
			r.RemoteAddr = "127.0.0.1:1234"
			r.Header.Set("Authorization", "Bearer "+token)
			switch variant {
			case "anonymous":
				r.Header.Del("Authorization")
			case "browser":
				r.Header.Del("Authorization")
				r.AddCookie(cookie)
			case "origin":
				r.Header.Set("Origin", "https://teamx.example")
			case "forwarded":
				r.Header.Set("X-Forwarded-Proto", "https")
			case "remote":
				r.RemoteAddr = "192.0.2.1:1234"
			case "wrong-token":
				r.Header.Set("Authorization", "Bearer invalid")
			case "post":
				r.Method = http.MethodPost
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden && !(variant == "post" && w.Code == http.StatusMethodNotAllowed) {
				t.Fatalf("unexpected status: %d %s", w.Code, w.Body.String())
			}
		})
	}
	if w := hostedRequest(s, "/v1/access/session", nil, nil, token); w.Code != http.StatusUnauthorized {
		t.Fatal("deployment token gained a business session", w.Code)
	}
	s.access.users.mu.Lock()
	s.access.users.failed = true
	s.access.users.mu.Unlock()
	if w := hostedRequest(s, "/v1/deployment/status", nil, nil, token); w.Code != http.StatusServiceUnavailable {
		t.Fatal("failed user directory reported ready", w.Code)
	}
	s.access.users.mu.Lock()
	s.access.users.failed = false
	s.access.users.mu.Unlock()
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	if w := hostedRequest(s, "/v1/deployment/status", nil, nil, token); w.Code != http.StatusServiceUnavailable {
		t.Fatal("closing deployment reported ready", w.Code)
	}
}

func TestServiceHostedStatusWithoutBusinessAccess(t *testing.T) {
	for _, variant := range []string{"ready", "other-installation", "not-ready", "wrong-mode", "missing-identity"} {
		t.Run(variant, func(t *testing.T) {
			s := hostedFixture(t, t.TempDir())
			s.access.installationID = "installed-hosted-instance"
			server := httptest.NewServer(s)
			defer server.Close()
			s.access.internal = server.URL
			c := s.access.config
			c.Server.Listen = new(strings.TrimPrefix(server.URL, "http://"))
			r := serviceRegistration{Home: filepath.Dir(c.path), InstallationID: s.access.installationID}
			token, err := readAdminToken(adminTokenPath(c))
			if err != nil {
				t.Fatal(err)
			}
			if variant == "other-installation" {
				r.InstallationID = "foreign-instance"
			}
			if variant == "not-ready" {
				s.access.users.failed = true
			}
			if variant == "wrong-mode" || variant == "missing-identity" {
				server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					status := map[string]any{"status": "running", "ready": true, "installation_id": s.access.installationID, "deployment_mode": "personal"}
					if variant == "missing-identity" {
						delete(status, "installation_id")
						status["deployment_mode"] = "multi-user"
					}
					json.NewEncoder(w).Encode(status)
				})
			}
			e := &serviceEndpoint{config: c, r: r, listen: *c.Server.Listen, token: token, client: server.Client()}
			defer e.close()
			err = e.authenticate(context.Background())
			if variant != "ready" {
				if err == nil {
					t.Fatal("unready or foreign deployment authenticated")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			status, err := e.deploymentStatus(context.Background())
			if err != nil || status["authenticated"] != true || status["ready"] != true {
				t.Fatal(status, err)
			}
			// Use the same configuration reader and token/UID checks as the
			// real lifecycle command; a business session is never requested.
			data, err := toml.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(c.path, data, 0600); err != nil {
				t.Fatal(err)
			}
			r.UID = os.Getuid()
			status, err = authenticatedServiceStatus(context.Background(), r)
			if err != nil || status["authenticated"] != true {
				t.Fatal(status, err)
			}
			if err := checkServicePort(context.Background(), r, true); err != nil {
				t.Fatal("same installation not recognized before stop", err)
			}
		})
	}
}
