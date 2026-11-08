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

func TestDiscoverProfilesWithoutChangingDefaultOrExposingSecrets(t *testing.T) {
	p, dir := authTestProfile(t)
	t.Setenv("LARKSUITE_CLI_PROFILE", "wrong-profile")
	t.Setenv("LARKSUITE_CLI_WORKSPACE", "wrong-workspace")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, executable := range []string{p.Executable, ""} {
		d, err := Discover(context.Background(), executable, "feishu")
		if err != nil || d.Executable != p.Executable || len(d.Profiles) != 1 || d.Profiles[0].Name != "fixture" {
			t.Fatal(d, err)
		}
		if d.SetupProfile != "teamx-feishu-2" {
			t.Fatal("setup would overwrite an existing profile")
		}
		data, _ := json.Marshal(d)
		if strings.Contains(string(data), "private") || strings.Contains(string(data), "other") {
			t.Fatal("discovery exposed fields outside selected platform")
		}
	}
	setMode(t, dir, "version")
	if _, err := Discover(context.Background(), p.Executable, "feishu"); ErrorCode(err) != "cli_version_mismatch" {
		t.Fatal(err)
	}
}

func TestLoginSplitFlowVerifiesIdentityAndReportsRenewal(t *testing.T) {
	for _, mode := range []string{"ok", "no-refresh"} {
		t.Run(mode, func(t *testing.T) {
			p, dir := authTestProfile(t)
			setMode(t, dir, mode)
			ready := 0
			result, err := Login(context.Background(), p, func(link, qr string) {
				ready++
				if link != "https://accounts.feishu.cn/oauth?code=A%2BB&x=1" || !strings.HasPrefix(qr, "data:image/png;base64,") {
					t.Error("link changed or QR missing")
				}
			})
			if err != nil || ready != 1 || result.Validation.Status != "valid" || result.Validation.Username != "测试用户" || result.RenewalUnavailable != (mode == "no-refresh") || result.ExpiresAt.IsZero() {
				t.Fatal(result, err)
			}
		})
	}
}

func TestLoginRejectsWrongPlatformUnsafeLinksAndMissingScopes(t *testing.T) {
	for _, tc := range []struct{ mode, code string }{{"brand", "identity_mismatch"}, {"url", "invalid_response"}, {"scopes", "permission_denied"}, {"version", "cli_version_mismatch"}} {
		t.Run(tc.mode, func(t *testing.T) {
			p, dir := authTestProfile(t)
			setMode(t, dir, tc.mode)
			_, err := Login(context.Background(), p, func(string, string) {})
			if ErrorCode(err) != tc.code || strings.Contains(err.Error(), "private") {
				t.Fatal(err)
			}
		})
	}
}

func TestLoginCancellationSettlesProcessAndReleasesReaderGate(t *testing.T) {
	p, dir := authTestProfile(t)
	setMode(t, dir, "block")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { _, err := Login(ctx, p, func(string, string) { close(ready) }); done <- err }()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("login did not publish ready link")
	}
	c, _ := New(p)
	readCtx, readCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer readCancel()
	if _, err := c.Verify(readCtx); ErrorCode(err) != "timeout" {
		t.Fatal("read bypassed auth profile gate", err)
	}
	cancel()
	select {
	case err := <-done:
		if ErrorCode(err) != "cancelled" {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("auth process not settled")
	}
	if _, err := c.Verify(context.Background()); err != nil {
		t.Fatal("profile gate leaked after cancellation", err)
	}
}

func TestLogoutReportsCLIErrorWithoutLeakingCredentials(t *testing.T) {
	p, dir := authTestProfile(t)
	setMode(t, dir, "logout-fail")
	if err := Logout(context.Background(), p); ErrorCode(err) != "credential_rejected" || strings.Contains(err.Error(), "private-token") {
		t.Fatal(err)
	}
	setMode(t, dir, "ok")
	if err := Logout(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "logged-out")); err != nil {
		t.Fatal("logout was not executed")
	}
}

func TestLarkLoginKeepsTargetAndSendScopesOptIn(t *testing.T) {
	for _, mode := range []string{"read", "send", "targets"} {
		t.Run(mode, func(t *testing.T) {
			p, dir := authTestProfile(t)
			script := strings.Replace(authFixture, "assert 'offline_access' in scopes and 'im:message:readonly' in scopes", "assert 'offline_access' in scopes and 'im:message:readonly' in scopes\n open(base+'/requested-scopes','w').write(' '.join(scopes))", 1)
			if err := os.WriteFile(p.Executable, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			login := Login
			if mode == "send" {
				login = LoginSending
			}
			if mode == "targets" {
				login = LoginSendTargets
			}
			if _, err := login(context.Background(), p, func(string, string) {}); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(dir + "/requested-scopes")
			if err != nil {
				t.Fatal(err)
			}
			scopes := string(raw)
			if strings.Contains(scopes, "contact:user:search") || strings.Contains(scopes, "--domain") {
				t.Fatal("unrequested directory scope", scopes)
			}
			if strings.Contains(scopes, "im:message.send_as_user") != (mode == "send") || strings.Contains(scopes, "im:chat.members:read") != (mode == "targets") || strings.Contains(scopes, "im:chat.moderation:read") {
				t.Fatal(mode, scopes)
			}
		})
	}
}
