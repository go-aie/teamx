//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/lark"
)

func larkPermissionCheckHTTP(h *larkHarness) *httptest.ResponseRecorder {
	return managerHTTP(h.m, "/v1/platforms/lark/permissions", larkAuthRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, Profile: h.c.CLIProfile})
}

func installLarkPermissionFixture(t *testing.T, h *larkHarness, commands string) {
	t.Helper()
	script := strings.Replace(larkFixtureScript(), "if a==['--version']:", commands+"\nif a==['--version']:", 1)
	if err := os.WriteFile(h.c.CLIProfile.Executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestLarkAccountChecksRecoverSharedProfileSubscriptions(t *testing.T) {
	parallelFixtureTest(t)
	for _, entry := range []string{"permissions", "verify"} {
		t.Run(entry, func(t *testing.T) {
			h := newLarkHarness(t)
			h.start()
			installLarkPermissionFixture(t, h, roomAuthCommands)
			other := platformTestSave(t, h.m, managedPlatformRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, ID: strings.Repeat("d", 32), Name: "shared", Provider: "lark", URL: h.c.URL, CLIProfile: h.c.CLIProfile})
			before := h.state()
			paused := before.Clone()
			paused.Status, paused.ErrorCode = "credentials_invalid", "credential_rejected"
			h.m.mu.Lock()
			err := h.m.notifications.SaveLocked(paused)
			h.m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if entry == "verify" {
				if _, err := h.m.verifyPlatform(context.Background(), managedPlatformRequest{ID: other.ID, Version: other.Version}); err != nil {
					t.Fatal(err)
				}
			} else {
				r := larkPermissionCheckHTTP(h)
				var check lark.PermissionCheck
				if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &check) != nil {
					t.Fatal("permission check", r.Code, r.Body.String())
				}
				if check.Account != h.c.Validation.Account || len(check.Capabilities) == 0 {
					t.Fatal("missing native account observation")
				}
			}
			resumed := h.state()
			if resumed.QueryPaused() || resumed.ErrorCode != "" || resumed.SubscriptionVersion != paused.SubscriptionVersion+1 {
				t.Fatal("credential-paused subscription was not resumed")
			}
			want := before.Clone()
			want.SubscriptionVersion = resumed.SubscriptionVersion
			want.RuntimeState = resumed.RuntimeState
			// Scheduling backoff is reset by recovery; business query progress is retained.
			for i := range want.Search.Branches {
				want.Search.Branches[i].RetryAt = time.Time{}
			}
			if !reflect.DeepEqual(want, resumed) {
				t.Fatal("account recovery changed subscription identity, choices, progress or backlog")
			}
			if r := larkPermissionCheckHTTP(h); r.Code != 200 {
				t.Fatal(r.Code, r.Body.String())
			}
			if !reflect.DeepEqual(h.state(), resumed) {
				t.Fatal("checking again restarted an already-running subscription")
			}
		})
	}
}

func TestLarkPermissionChecksPreserveIneligibleSubscriptions(t *testing.T) {
	parallelFixtureTest(t)
	for _, mode := range []string{"disabled", "retired", "capacity", "binding_changed", "different_generation", "different_account", "verification_failed"} {
		t.Run(mode, func(t *testing.T) {
			h := newLarkHarness(t)
			h.start()
			installLarkPermissionFixture(t, h, roomAuthCommands)
			s := h.state()
			s.Status, s.ErrorCode = "credentials_invalid", "credential_rejected"
			switch mode {
			case "disabled", "retired":
				s.Config.Enabled = false
				s.RuntimeState.Reset(false, false)
				s.Retired = mode == "retired"
			case "capacity", "binding_changed":
				s.Status, s.ErrorCode = mode, mode
			case "different_generation":
				s.Binding.Generation = strings.Repeat("f", 32)
				s = refreshLarkSearchFixture(t, s)
			case "different_account":
				script := strings.ReplaceAll(larkFixtureScript(), "ou_reader", "ou_other")
				if err := os.WriteFile(h.c.CLIProfile.Executable, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			case "verification_failed":
				if err := os.WriteFile(h.c.CLIProfile.Executable, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			h.m.mu.Lock()
			err := h.m.notifications.SaveLocked(s)
			h.m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			r := larkPermissionCheckHTTP(h)
			failed := mode == "different_account" || mode == "verification_failed"
			if (r.Code != 200) != failed {
				t.Fatal("unexpected check result", r.Code, r.Body.String())
			}
			h.m.mu.Lock()
			got, ok := h.m.notifications.StateLocked(s.Metadata().Key)
			h.m.mu.Unlock()
			if !ok || !reflect.DeepEqual(got, s) {
				t.Fatal("account check changed an ineligible subscription")
			}
		})
	}
}

func TestLarkAccountCheckDoesNotRecoverAnotherProfile(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.start()
	s := h.state()
	s.Status, s.ErrorCode = "credentials_invalid", "credential_rejected"
	h.m.mu.Lock()
	err := h.m.notifications.SaveLocked(s)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	otherProfile := *h.c.CLIProfile
	otherProfile.Profile = "other"
	otherProfile.Executable = filepath.Join(h.dir, "other-cli")
	script := strings.ReplaceAll(larkFixtureScript(), "'profile':'fixture'", "'profile':'other'")
	if err := os.WriteFile(otherProfile.Executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	other := platformTestSave(t, h.m, managedPlatformRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, ID: strings.Repeat("d", 32), Name: "other profile", Provider: "lark", URL: h.c.URL, CLIProfile: &otherProfile})
	checked, err := h.m.verifyPlatform(context.Background(), managedPlatformRequest{ID: other.ID, Version: other.Version})
	if err != nil || checked.Validation.Status != "valid" || checked.Validation.Account != h.c.Validation.Account {
		t.Fatal("fixture did not verify the same account through a distinct profile", err)
	}
	if !reflect.DeepEqual(h.state(), s) {
		t.Fatal("checking a distinct profile resumed an unrelated subscription")
	}
}

func TestLarkPermissionCheckRejectsConcurrentConnectionChanges(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.start()
	commands := strings.Replace(roomAuthCommands, "if a==['auth','scopes','--json']:", "if a==['auth','scopes','--json']:\n import time\n open(base+'/check-started','w').close()\n while not os.path.exists(base+'/check-release'):time.sleep(0.01)", 1)
	installLarkPermissionFixture(t, h, commands)
	s := h.state()
	s.Status, s.ErrorCode = "credentials_invalid", "credential_rejected"
	h.m.mu.Lock()
	err := h.m.notifications.SaveLocked(s)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- larkPermissionCheckHTTP(h) }()
	release := filepath.Join(h.dir, "check-release")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(h.dir, "check-started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("permission check did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	platformTestSave(t, h.m, managedPlatformRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, ID: h.c.ID, Version: h.c.Version, Name: "renamed", Provider: h.c.Provider, URL: h.c.URL})
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.Code != 409 {
			t.Fatal("stale permission check was applied", r.Code, r.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("permission check did not finish")
	}
	if !reflect.DeepEqual(h.state(), s) {
		t.Fatal("stale check resumed a paused subscription")
	}
}
