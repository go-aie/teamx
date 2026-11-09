//go:build integration || e2e

package lark

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupFixture(t *testing.T, mode string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	data, err := os.ReadFile("testdata/setup_cli.py")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "lark cli")
	for name, data := range map[string][]byte{"mode": []byte(mode), "profiles.json": []byte(`[{"name":"baseline","appId":"cli_baseline","brand":"feishu","active":true,"private":"never projected"}]`), "lark cli": data} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return bin, dir
}

func TestApplicationSetupResultAndProfilePreservation(t *testing.T) {
	bin, dir := setupFixture(t, "ok")
	var progress []SetupProgress
	result, err := ConfigureApplication(context.Background(), bin, "feishu", func(p SetupProgress) { progress = append(progress, p) })
	if err != nil || !result.Found || result.AppID != "cli_selected" || result.AppName != "Selected app" || !strings.HasPrefix(result.Profile.Profile, "teamx-feishu-") {
		t.Fatal(result, err)
	}
	if len(progress) != 2 || progress[0].URL != "" || !validRegistrationURL(progress[1].URL, "feishu") || !strings.HasPrefix(progress[1].QRCode, "data:image/png;base64,") || progress[0].Profile != result.Profile {
		t.Fatal("incorrect setup progress")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "profiles.json"))
	var profiles []map[string]any
	if json.Unmarshal(data, &profiles) != nil || len(profiles) != 2 || profiles[0]["appId"] != "cli_baseline" || profiles[0]["active"] != true || profiles[0]["private"] != "never projected" || profiles[1]["name"] != result.Profile.Profile {
		t.Fatal("setup replaced an existing profile or default")
	}
	data, _ = json.Marshal(result)
	if strings.Contains(string(data), "synthetic-secret") || strings.Contains(string(data), "private-device-code") {
		t.Fatal("private output escaped result projection")
	}
}

func TestApplicationSetupRejectsUnconfirmedAndChangedResults(t *testing.T) {
	for _, tc := range []struct {
		mode, code string
		found      bool
	}{
		{"version", "cli_version_mismatch", false},
		{"pre-conflict", "configuration_changed", false},
		{"unsafe-url", "invalid_response", false},
		{"second-url", "invalid_response", false},
		{"overflow", "output_limit", false},
		{"expired", "timeout", false},
		{"receipt", "configuration_unconfirmed", true},
		{"no-link", "configuration_unconfirmed", true},
		{"brand", "identity_mismatch", true},
		{"overwrite", "configuration_changed", true},
		{"foreign-change", "configuration_changed", true},
		{"exit-after-save", "credential_rejected", true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			bin, dir := setupFixture(t, tc.mode)
			result, err := ConfigureApplication(context.Background(), bin, "feishu", func(SetupProgress) {})
			if ErrorCode(err) != tc.code || result.Found != tc.found || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal(result, err)
			}
			if tc.mode == "pre-conflict" || tc.mode == "version" {
				if _, err := os.Stat(filepath.Join(dir, "setup-started")); err == nil {
					t.Fatal("preflight failure started registration")
				}
			}
		})
	}
	if _, err := ConfigureApplication(context.Background(), "/unused", "lark", func(SetupProgress) {}); ErrorCode(err) != "setup_brand_unverified" {
		t.Fatal("unverified brand was enabled", err)
	}
}

func TestApplicationSetupSerializesOtherConfigurationWriters(t *testing.T) {
	bin, dir := setupFixture(t, "block")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = ConfigureApplication(ctx, bin, "feishu", func(p SetupProgress) {
			if p.URL != "" {
				close(ready)
			}
		})
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("setup never became ready")
	}
	p, authDir := authTestProfile(t)
	for _, action := range []string{"login", "logout"} {
		blocked, stop := context.WithTimeout(context.Background(), 60*time.Millisecond)
		var err error
		if action == "login" {
			_, err = Login(blocked, p, func(string, string) { t.Error("login bypassed config writer") })
		} else {
			err = Logout(blocked, p)
		}
		stop()
		if ErrorCode(err) != "timeout" {
			t.Fatal("config writer bypassed setup", action, err)
		}
	}
	if _, err := os.Stat(filepath.Join(authDir, "calls")); err == nil {
		t.Fatal("blocked login or logout started a CLI process")
	}
	cancel()
	<-done
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureApplication(context.Background(), bin, "feishu", func(SetupProgress) {}); err != nil {
		t.Fatal("configuration gate leaked", err)
	}
}

func TestApplicationSetupCancellationRecoversSavedConfigurationAndReleasesWriter(t *testing.T) {
	for _, mode := range []string{"block", "saved-block"} {
		t.Run(mode, func(t *testing.T) {
			bin, dir := setupFixture(t, mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			var result SetupResult
			var err error
			go func() { result, err = ConfigureApplication(ctx, bin, "feishu", func(SetupProgress) {}); close(done) }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				data, _ := os.ReadFile(filepath.Join(dir, "profiles.json"))
				_, started := os.Stat(filepath.Join(dir, "setup-started"))
				if started == nil && (mode == "block" || strings.Contains(string(data), "cli_selected")) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("setup never blocked")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if _, e := ConfigureApplication(ctx, bin, "feishu", func(SetupProgress) {}); ErrorCode(e) != "configuration_busy" {
				t.Fatal("overlapping configuration admitted", e)
			}
			cancel()
			select {
			case <-done:
			case <-time.After(4 * time.Second):
				t.Fatal("cancel did not settle process")
			}
			if ErrorCode(err) != "cancelled" || result.Found != (mode == "saved-block") {
				t.Fatal(result, err)
			}
			unlock, err := lockConfiguration(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			unlock()
		})
	}
}
