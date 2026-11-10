//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

func roomSetupFixture(t *testing.T, mode string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	script, err := os.ReadFile("../platform/lark/testdata/setup_cli.py")
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"lark-cli": script, "mode": []byte(mode),
		"profiles.json": []byte(`[{"name":"baseline","appId":"cli_baseline","brand":"feishu","active":true}]`),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "lark-cli"), dir
}

func setupWait(t *testing.T, m *roomManager, req larkAuthRequest, stage, status string) larkAuthView {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		view := authHTTP(t, m, "status", req)
		if view.Stage == stage && view.Status == status {
			return view
		}
		if view.Status == "failed" || view.Status == "expired" {
			t.Fatal("unexpected setup failure", view.Stage, view.ErrorCode)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("setup did not reach", stage, status)
	return larkAuthView{}
}

func setupRelease(t *testing.T, dir, step string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, step+"-release"), []byte("yes"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLarkSetupHTTPStagesOwnershipAndLoginHandoff(t *testing.T) {
	// This process fixture writes synthetic CLI configuration, never real accounts.
	m := testManager(t, t.TempDir(), nil)
	bin, dir := roomSetupFixture(t, "block")
	startReq := larkAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, Executable: bin, Brand: "feishu"}
	start := authHTTP(t, m, "setup", startReq)
	req := larkAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, SessionID: start.SessionID}
	waiting := setupWait(t, m, req, "application", "waiting")
	if !strings.HasPrefix(waiting.URL, "https://open.feishu.cn/page/cli?") || waiting.QRCodeURL == "" || waiting.ConfigurationFound || waiting.Username != "" {
		t.Fatal("application setup misreported as account success")
	}
	if qr := managerHTTP(m, waiting.QRCodeURL, nil); qr.Code != 200 || qr.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(qr.Body.String(), "\x89PNG") {
		t.Fatal("application QR missing")
	}
	if r := managerHTTP(m, "/v1/platforms/lark/setup", startReq); r.Code != 409 {
		t.Fatal("duplicate setup admitted", r.Code)
	}
	other := req
	other.Caller.ID = "different-operator"
	if r := managerHTTP(m, "/v1/platforms/lark/status", other); r.Code == 200 {
		t.Fatal("session exposed to a different operator")
	}
	other = req
	other.ServiceEpoch = "another-service"
	if r := managerHTTP(m, "/v1/platforms/lark/cancel", other); r.Code == 200 {
		t.Fatal("different service epoch cancelled setup")
	}
	p := platform.CLIProfile{Version: 1, Executable: bin, Profile: "baseline", Brand: "feishu", CLIVersion: lark.CLIVersion}
	if r := managerHTTP(m, "/v1/platforms/lark/login", larkAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, Profile: &p}); r.Code != 409 {
		t.Fatal("login overlapped configuration writer", r.Code)
	}
	setupRelease(t, dir, "app")
	login := setupWait(t, m, req, "authorization", "waiting")
	if !login.ConfigurationFound || login.Profile == nil || login.AppID != "cli_selected" || login.AppName != "Selected app" || login.Username != "" || !strings.HasPrefix(login.URL, "https://accounts.feishu.cn/") {
		t.Fatal("application result was not verified before OAuth")
	}
	data, _ := json.Marshal(login)
	if strings.Contains(string(data), "synthetic-secret") || strings.Contains(string(data), "private-device-code") || strings.Contains(string(data), "data:image") {
		t.Fatal("private configuration or flow material escaped")
	}
	setupRelease(t, dir, "auth")
	done := setupWait(t, m, req, "complete", "succeeded")
	if done.Username != "Verified user" || done.URL != "" || done.QRCodeURL != "" || len(platformTestList(t, m)) != 0 {
		t.Fatal("setup failed to verify or saved a connection implicitly")
	}
	if r := managerHTTP(m, login.QRCodeURL, nil); r.Code == 200 {
		t.Fatal("completed OAuth image remains readable")
	}
	data, _ = os.ReadFile(filepath.Join(dir, "requested-scopes"))
	if strings.Contains(string(data), "send_as_user") || !strings.Contains(string(data), "offline_access") {
		t.Fatal("setup requested unexpected scopes")
	}
}

func TestLarkSetupCancelRecoversResultAndShutdownSettles(t *testing.T) {
	for _, mode := range []string{"block", "saved-block"} {
		t.Run(mode, func(t *testing.T) {
			m := testManager(t, t.TempDir(), nil)
			bin, dir := roomSetupFixture(t, mode)
			start := authHTTP(t, m, "setup", larkAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, Executable: bin, Brand: "feishu"})
			req := larkAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, SessionID: start.SessionID}
			waiting := setupWait(t, m, req, "application", "waiting")
			if mode == "saved-block" {
				deadline := time.Now().Add(3 * time.Second)
				for {
					data, _ := os.ReadFile(filepath.Join(dir, "profiles.json"))
					if strings.Contains(string(data), "cli_selected") {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("fixture did not persist config")
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			cancelled := authHTTP(t, m, "cancel", req)
			if cancelled.Status != "cancelled" || cancelled.URL != "" || cancelled.ConfigurationFound != (mode == "saved-block") {
				t.Fatal("cancellation lost state or revived setup", cancelled.Status, cancelled.ErrorCode)
			}
			if _, err := os.Stat(filepath.Join(dir, "auth-started")); err == nil {
				t.Fatal("cancelled setup started OAuth")
			}
			if r := managerHTTP(m, waiting.QRCodeURL, nil); r.Code == 200 {
				t.Fatal("cancelled setup QR remained accessible")
			}
			// Start again with a fresh server-owned name, then close the service.
			start = authHTTP(t, m, "setup", larkAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, Executable: bin, Brand: "feishu"})
			req.SessionID = start.SessionID
			setupWait(t, m, req, "application", "waiting")
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			s := m.larkAuthSessions[start.SessionID]
			m.mu.Unlock()
			select {
			case <-s.done:
			default:
				t.Fatal("shutdown leaked configuration process")
			}
		})
	}
}

func TestLarkSetupLoginFailureKeepsConfigurationForExistingLogin(t *testing.T) {
	m := testManager(t, t.TempDir(), nil)
	bin, dir := roomSetupFixture(t, "login-fail")
	start := authHTTP(t, m, "setup", larkAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, Executable: bin, Brand: "feishu"})
	req := larkAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, SessionID: start.SessionID}
	deadline := time.Now().Add(5 * time.Second)
	var failed larkAuthView
	for time.Now().Before(deadline) {
		failed = authHTTP(t, m, "status", req)
		if failed.Status == "failed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if failed.Status != "failed" || failed.Stage != "authorization" || !failed.ConfigurationFound || failed.Profile == nil || failed.ErrorCode != "network_error" {
		t.Fatal("login failure discarded configured application", failed)
	}
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	setupRelease(t, dir, "auth")
	login := authHTTP(t, m, "login", larkAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, Profile: failed.Profile})
	req.SessionID = login.SessionID
	authWait(t, m, req, "succeeded")
	data, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if strings.Count(string(data), `"init", "--new"`) != 1 {
		t.Fatal("OAuth recovery repeated app registration")
	}
}

func TestLarkSetupRejectsUnverifiedBrandAndExpiresWithoutRevival(t *testing.T) {
	m := testManager(t, t.TempDir(), nil)
	bin, _ := roomSetupFixture(t, "block")
	req := larkAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, Executable: bin, Brand: "lark"}
	r := managerHTTP(m, "/v1/platforms/lark/setup", req)
	if r.Code == 200 || !strings.Contains(r.Body.String(), "setup_brand_unverified") {
		t.Fatal("unverified Lark page enabled")
	}
	for _, caller := range []Caller{{Kind: "agent", ID: "assistant"}, {Kind: "human", ID: "operator", MembershipID: "member"}} {
		req.Caller, req.Brand = caller, "feishu"
		if r := managerHTTP(m, "/v1/platforms/lark/setup", req); r.Code == 200 {
			t.Fatal("non-operator started configuration")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	s := &larkAuthSession{larkAuthView: larkAuthView{SessionID: "expired", Stage: "application"}, owner: humanCaller.ID, setup: true, cancel: cancel, done: make(chan struct{}), expires: time.Now().Add(time.Minute)}
	m.mu.Lock()
	m.larkAuthSessions = map[string]*larkAuthSession{s.SessionID: s}
	m.mu.Unlock()
	m.runLarkSetup(ctx, s, bin, "feishu")
	if s.Status != "expired" || s.ErrorCode != "timeout" || s.URL != "" {
		t.Fatal("expired configuration was revived", s.Status, s.ErrorCode)
	}
}
