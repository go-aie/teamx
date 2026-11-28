package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/authn"
	"github.com/pelletier/go-toml/v2"
)

func TestHostedConfiguredServerAndLocalManagementCLI(t *testing.T) {
	home := t.TempDir()
	config := map[string]any{
		"server":     map[string]any{"listen": "127.0.0.1:0", "public_base_url": "https://teamx.example", "trusted_proxies": []string{"127.0.0.1"}},
		"deployment": map[string]any{"mode": "multi-user", "authentication": externalAuthConfig{Provider: "fixture", AllowedSubjects: []string{"alice", "bob"}}},
		"runtime":    testTeamDefinition(t).Runtime,
	}
	path := filepath.Join(home, "config.toml")
	data, err := toml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := initializeTeamTemplates(resetSeedForTest(t), teamDefaultsPath(path)); err != nil {
		t.Fatal(err)
	}
	selection := personalDeployment(controlledActivationFactory(nil, nil))
	selection.factories.authentication = func(externalAuthConfig) (authn.Provider, error) { return hostedProviderFixture{}, nil }
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- runConfiguredServer(ctx, []string{"--home", home}, io.Discard, io.Discard, serverOptions{
			deployment:      &selection,
			platformCatalog: defaultPlatformCatalog(),
			// This test verifies shared host assembly, not Linux SRT acceptance.
			execution: func(_ context.Context, c *startupConfig, _ ...string) error { return checkDeploymentRoot(*c) },
			ready: func(info any) error {
				data, _ := json.Marshal(info)
				var status struct {
					Endpoint string `json:"endpoint"`
					Status   string `json:"status"`
				}
				if err := json.Unmarshal(data, &status); err != nil {
					return err
				}
				ready <- status.Endpoint
				return nil
			},
		})
	}()
	var endpoint string
	select {
	case endpoint = <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("bootstrap timeout")
	}
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(35 * time.Second):
			t.Error("shutdown timeout")
		}
	}()
	// CLI uses an internal loopback endpoint. Publish the actual test listener
	// in this private test configuration without changing the running service.
	config["server"].(map[string]any)["listen"] = endpoint[len("http://"):]
	data, _ = toml.Marshal(config)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runUserManagement([]string{"list", "--config", path}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var view struct {
		Users []externalUser `json:"users"`
	}
	if json.Unmarshal(out.Bytes(), &view) != nil || len(view.Users) != 0 {
		t.Fatal("new service created a fallback user")
	}
	req, _ := http.NewRequest(http.MethodGet, endpoint+"/v1/access/method", nil)
	req.Host = "teamx.example"
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var method map[string]string
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&method) != nil || method["provider"] != "fixture" || method["method"] != "external" {
		t.Fatal("configured login entry unavailable")
	}
	if _, err := os.Stat(filepath.Join(home, "personal-space-registration.json")); !os.IsNotExist(err) {
		t.Fatal("hosted startup created personal identity")
	}
}

func TestHostedProviderFailurePrecedesPrivateRegistration(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[deployment]\nmode='multi-user'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	selection := personalDeployment(nil)
	selection.factories.authentication = func(externalAuthConfig) (authn.Provider, error) { return nil, errors.New("provider unavailable") }
	err := runConfiguredServer(t.Context(), []string{"--home", home}, io.Discard, io.Discard, serverOptions{deployment: &selection})
	if err == nil {
		t.Fatal("missing authentication accepted")
	}
	for _, name := range []string{"auth", "users.json", "spaces.json", "multi-user-registration.json"} {
		if _, err := os.Stat(filepath.Join(home, name)); !os.IsNotExist(err) {
			t.Fatal("authentication failure reached private registration", name)
		}
	}
}

func TestHostedRejectsRetiredProviderWithoutRewritingConfiguration(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.toml")
	source := "[deployment]\nmode='multi-user'\n[deployment.authentication]\nprovider='casdoor'\nissuer='https://opencsg.example'\nclient_id='teamx-client'\nallow_all_authenticated=true\n"
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	selection := personalDeployment(nil)
	selection.factories.authentication = externalAuthConfig.provider
	err := runConfiguredServer(t.Context(), []string{"--home", home}, io.Discard, io.Discard, serverOptions{deployment: &selection})
	if err == nil || !strings.Contains(err.Error(), "supported authentication provider") {
		t.Fatal("retired provider was accepted or converted", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != source {
		t.Fatal("unsupported configuration was rewritten", err)
	}
	for _, name := range []string{"auth", "users.json", "spaces.json", "multi-user-registration.json"} {
		if _, err := os.Stat(filepath.Join(home, name)); !os.IsNotExist(err) {
			t.Fatal("unsupported provider reached private registration", name)
		}
	}
}
