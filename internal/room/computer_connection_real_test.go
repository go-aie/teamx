package room

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/computer"
	"github.com/go-aie/teamx/internal/computer/aio"
)

// A disposable two-Runtime launcher is required. No ordinary test discovers
// Docker, creates a provider connection, or changes the user's installation.
func TestComputerRealConnectionSettings(t *testing.T) {
	if os.Getenv("TEAMX_COMPUTER_CONNECTION_REAL") != "1" {
		t.Skip("explicit real connection-settings acceptance only")
	}
	keyBytes, err := os.ReadFile(os.Getenv("TEAMX_COMPUTER_TOKEN_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	key := strings.TrimSpace(string(keyBytes))
	endpoint := os.Getenv("TEAMX_COMPUTER_ENDPOINT")
	replacement := os.Getenv("TEAMX_COMPUTER_REPLACEMENT")
	home := os.Getenv("TEAMX_COMPUTER_HOME")
	if home == "" {
		home = t.TempDir()
	} else if err = os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.toml")
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		if err = os.WriteFile(path, []byte("# preserve comment\n[server]\ntime_zone='UTC' # preserve timezone\n[access]\nlogin_enabled=false\noperator_id='owner_00000000000000000000000000000001'\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var svc *service
	ready, stop := startTestServer(t, []string{"--config", path, "--room", "settings-acceptance", "--state-dir", filepath.Join(home, "data")}, serverOptions{create: func(dir, room string, config serviceConfig) (*service, error) {
		var err error
		svc, err = newService(dir, room, config)
		return svc, err
	}})
	c := svc.settings.computer
	var connections atomic.Int32
	c.mu.Lock()
	c.connect = connectionFactory(&connections)
	c.mu.Unlock()
	request := func(method, path string, body any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(raw)
		}
		req, err := http.NewRequest(method, ready.Endpoint+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", ready.Endpoint)
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err = io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode >= 300 {
			t.Fatalf("%s: HTTP %d: %s", path, response.StatusCode, raw)
		}
		var out map[string]any
		if json.Unmarshal(raw, &out) != nil {
			t.Fatal("invalid HTTP JSON")
		}
		if bytes.Contains(raw, []byte(key)) || bytes.Contains(raw, []byte("credential_ref")) {
			t.Fatal("credential leaked in API")
		}
		return out
	}
	initial := request("GET", "/v1/computer", nil)
	if initial["enabled"] != false || connections.Load() != 0 {
		t.Fatal("disabled startup probed provider")
	}
	if os.Getenv("TEAMX_COMPUTER_CONNECTION_PHASE") == "offline" {
		if c.current.Load().Version <= 1 || c.active != nil {
			t.Fatal("offline restart lost committed disable")
		}
		request("POST", "/v1/send", map[string]any{"room_id": c.service.RoomID, "room_instance": c.service.RoomInstance, "service_epoch": c.service.ServiceEpoch, "caller": map[string]string{"kind": "human", "id": c.owner.OwnerID}, "text": "ordinary Room works after Runtime containers stopped and TeamX restarted", "client_message_id": newComputerID()})
		if connections.Load() != 0 {
			t.Fatal("offline restart constructed provider")
		}
		report := map[string]any{"passed": true, "enabled": false, "provider_connections": connections.Load(), "ordinary_room_send": true, "version": c.current.Load().Version, "service_epoch": c.service.ServiceEpoch}
		raw, _ := json.MarshalIndent(report, "", "  ")
		if err = os.WriteFile(filepath.Join(os.Getenv("TEAMX_COMPUTER_EVIDENCE"), "offline-restart.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		stop()
		return
	}

	save := func(enabled bool, target string, max int) string {
		t.Helper()
		view := request("GET", "/v1/computer", nil)
		input := computerChangeInput{ServiceInfo: c.service, InstallationID: c.owner.InstallationID, Caller: Caller{Author: Author{Kind: "human", ID: c.owner.OwnerID}}, ID: newComputerID(), ExpectedVersion: uint64(view["version"].(float64)), Enabled: enabled, Provider: "aio", Endpoint: target, MaxScreens: max, CredentialAction: "replace", Credential: key}
		result := request("POST", "/v1/computer/save", input)
		id := result["id"].(string)
		deadline := time.Now().Add(90 * time.Second)
		for {
			result = request("GET", "/v1/computer/operations/"+id, nil)
			if result["status"] == "completed" {
				break
			}
			if result["status"] == "failed" || time.Now().After(deadline) {
				t.Fatal("connection application incomplete", result)
			}
			time.Sleep(50 * time.Millisecond)
		}
		return id
	}
	first := save(true, endpoint, 2)
	provider, err := aio.New(endpoint, key)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	info, err := provider.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := provider.Claim(ctx, computer.Claim{Owner: c.owner, Service: c.service.ServiceEpoch, ExpectedLease: info.Lease})
	if err != nil {
		t.Fatal(err)
	}
	binding := computer.Binding{Owner: c.owner, Service: c.service.ServiceEpoch, Lease: lease, Member: MemberIdentity{RoomIdentity: RoomIdentity{RoomID: "controlled-probe", RoomInstance: newComputerID()}, AgentID: "probe", MembershipID: newComputerID()}}
	screen, err := c.ensureScreen(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	native := func(s computer.Screen, tool string, args any) computer.Outcome {
		t.Helper()
		raw, _ := json.Marshal(args)
		out, err := s.Call(ctx, computer.Call{ID: newComputerID(), Scope: computer.Scope{Session: "controlled-session", Turn: "controlled-turn"}, Tool: tool, Arguments: raw, ContractDigest: info.Contract.Digest})
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			IsError bool `json:"isError"`
		}
		if json.Unmarshal(out.Result, &result) != nil || result.IsError {
			t.Fatal("native MCP task failed", string(out.Result))
		}
		return out
	}
	native(screen, "browser_navigate", map[string]any{"url": "http://127.0.0.1:18765/?screen=A"})
	native(screen, "browser_form_input_fill", map[string]any{"selector": "#marker", "value": "connection-settings-acceptance", "clear": true})
	if out := native(screen, "browser_get_text", map[string]any{}); !bytes.Contains(out.Result, []byte("connection-settings-acceptance")) {
		t.Fatal("native screen lost marker")
	}
	// Same instance through a new origin and a fresh private credential reference.
	targetURL, _ := url.Parse(endpoint)
	proxy := httptest.NewServer(httputil.NewSingleHostReverseProxy(targetURL))
	defer proxy.Close()
	same := save(true, proxy.URL, 1)
	old := screen.Info()
	if _, err := screen.Call(ctx, computer.Call{}); !computer.Is(err, computer.Unavailable) {
		t.Fatal("old transport admitted new work", err)
	}
	screen, err = c.ensureScreen(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	if screen.Info().ID != old.ID || screen.Info().Epoch != old.Epoch {
		t.Fatal("transport refresh rebuilt screen")
	}
	native(screen, "browser_get_text", map[string]any{})
	if screen.Info().ID == "" {
		t.Fatal("screen identity lost")
	}
	oldScreens, err := provider.Screens(ctx, c.owner)
	if err != nil || len(oldScreens) != 1 || oldScreens[0].Status != "running" {
		t.Fatal("same-instance update stopped a screen", oldScreens, err)
	}
	replaced := save(true, replacement, 1)
	oldScreens, err = provider.Screens(ctx, c.owner)
	if err != nil || len(oldScreens) != 1 || oldScreens[0].Status != "stopped" {
		t.Fatal("old screen not stopped before replacement", oldScreens, err)
	}
	newProvider, _ := aio.New(replacement, key)
	newInfo, err := newProvider.Inspect(ctx)
	if err != nil || newInfo.Running != 0 {
		t.Fatal("candidate started a task during validation")
	}
	save(false, replacement, 1)
	// Returning to the original instance restores the same persistent profile,
	// while a new desktop epoch invalidates the old screen handle.
	save(true, endpoint, 1)
	restored, err := c.ensureScreen(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Info().ID != screen.Info().ID || restored.Info().Epoch <= screen.Info().Epoch {
		t.Fatal("profile identity was replaced")
	}
	native(restored, "browser_navigate", map[string]any{"url": "http://127.0.0.1:18765/?screen=A"})
	if out := native(restored, "browser_get_text", map[string]any{}); !bytes.Contains(out.Result, []byte("connection-settings-acceptance")) {
		t.Fatal("persistent profile marker was lost")
	}
	disabled := save(false, endpoint, 1)
	if request("GET", "/v1/info", nil)["service_epoch"] != ready.ServiceEpoch {
		t.Fatal("service restarted during configuration")
	}
	// Ordinary Room operation remains available after all connection changes.
	request("POST", "/v1/send", map[string]any{"room_id": c.service.RoomID, "room_instance": c.service.RoomInstance, "service_epoch": c.service.ServiceEpoch, "caller": map[string]string{"kind": "human", "id": c.owner.OwnerID}, "text": "ordinary Room works after Computer settings", "client_message_id": newComputerID()})
	stop()
	if os.Getenv("TEAMX_COMPUTER_EVIDENCE") != "" {
		report := map[string]any{"passed": true, "service_epoch_unchanged": true, "initial_provider_connections": 0, "first": first, "same_instance": same, "replacement": replaced, "disable": disabled, "old_screen_id": screen.Info().ID, "old_profile_preserved": len(oldScreens) == 1, "profile_marker_restored": true, "replacement_running_before_tasks": newInfo.Running, "ordinary_room_send": true, "credentials_redacted": true}
		raw, _ := json.MarshalIndent(report, "", "  ")
		if err = os.WriteFile(filepath.Join(os.Getenv("TEAMX_COMPUTER_EVIDENCE"), "connection.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
