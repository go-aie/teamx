//go:build integration || e2e

package room

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/lark"
)

// The lower adapter tests own provisioning and native encryption. Here a saved
// private profile represents the existing Space, without copying live data.
func managedLarkUpgradeFixture(t *testing.T, h *larkHarness, next string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(h.c.CLIProfile.Profile))
	dir := filepath.Join(home, "platform-cli", "lark", hex.EncodeToString(sum[:]))
	for _, p := range []string{dir, filepath.Join(dir, "config"), filepath.Join(dir, "data")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeJSONFixture(t, filepath.Join(dir, "profile.json"), h.c.CLIProfile)
	h.m.config.hostTools.restricted = true
	h.m.config.platformBinDir = filepath.Dir(next)
	h.m.config.environment = []string{"PATH=/must-not-search"}
	if err := h.m.configureLarkRuntime(home); err != nil {
		t.Fatal(err)
	}
	if h.m.larkClients.Executable(*h.c.CLIProfile) != next {
		t.Fatal("assembly selected another program")
	}
}

func TestManagedLarkUpgradeResumesSavedToolStep(t *testing.T) {
	parallelFixtureTest(t)
	h := larkToolConfigurationHarnessWithMembers(t, 1)
	req := onboardingRequest(t, h.m, h.room, h.members[0], h.c)
	onboardingHTTP(t, h.m, "prepare", req)
	payload, err := os.ReadFile(h.c.CLIProfile.Executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(h.c.CLIProfile.Executable); err != nil {
		t.Fatal(err)
	}
	failed := onboardingApply(t, h.m, req)
	if failed.Notifications.Status != "applied" || failed.Tool.Status != "failed" || failed.Tool.ErrorCode != "cli_unavailable" {
		t.Fatal(failed.Notifications, failed.Tool)
	}
	before := larkOnboardingSubscriptionForTest(t, h.m, req)
	snapshot, err := h.m.platforms.Load()
	if err != nil {
		t.Fatal(err)
	}
	nextDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(nextDir, "lark-cli")
	if err := os.WriteFile(next, payload, 0700); err != nil {
		t.Fatal(err)
	}
	managedLarkUpgradeFixture(t, h, next)
	// This is the original durable operation. No login, subscription or connection
	// replacement is needed to continue its unsubmitted tool step.
	resumed := onboardingApply(t, h.m, req)
	if resumed.ID != failed.ID || resumed.Tool.Status != "applied" {
		t.Fatal(resumed.Tool)
	}
	after := larkOnboardingSubscriptionForTest(t, h.m, req)
	if before.SubscriptionID != after.SubscriptionID || before.SubscriptionVersion != after.SubscriptionVersion || !before.Baseline.Equal(after.Baseline) || !reflect.DeepEqual(before.Search, after.Search) {
		t.Fatal("resume changed notification progress")
	}
	current, err := h.m.platforms.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.Platforms, current.Platforms) {
		t.Fatal("runtime upgrade rewrote connection evidence")
	}
	h.m.mu.Lock()
	bindings, err := h.m.readToolBindingsLocked(h.room.RoomIdentity)
	if err != nil {
		h.m.mu.Unlock()
		t.Fatal(err)
	}
	b, d, err := h.m.resolveToolBindingLocked(bindings, toolsRequest{Tool: current.Platforms[0].ToolEntryID, Caller: agentCaller(h.members[0].AgentID, h.members[0].MembershipID)})
	if err == nil {
		s := toolSelection{}
		h.m.toolAvailability(current, current.Platforms[0], b, d, &s)
		if s.UnavailableReason != "" {
			t.Error("availability still checked removed program", s.UnavailableReason)
		}
	}
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// An ambiguous prior submission remains history, even after program repair.
	resumed.Tool.Status, resumed.Tool.ErrorCode = "unknown", "submission_unconfirmed"
	h.m.mu.Lock()
	err = h.m.saveOnboardingLocked(resumed)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	replay := onboardingApply(t, h.m, req)
	if replay.Tool.Status != "unknown" || replay.Tool.Version != resumed.Tool.Version {
		t.Fatal("unknown tool step was replayed")
	}
}

func TestManagedLarkNotificationsUseRuntimeWithoutMemberTools(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkSearchHarness(t)
	h.start()
	before := h.state()
	payload, err := os.ReadFile(h.c.CLIProfile.Executable)
	if err != nil {
		t.Fatal(err)
	}
	// Preserve the fixture's read-only dataset independently of program location.
	nextDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(nextDir, "lark-cli")
	if err := os.WriteFile(next, payload, 0700); err != nil {
		t.Fatal(err)
	}
	// This fixture derives its synthetic data from its program directory.
	for _, name := range []string{"messages.json", "directory.json"} {
		body, err := os.ReadFile(filepath.Join(h.dir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nextDir, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(h.c.CLIProfile.Executable); err != nil {
		t.Fatal(err)
	}
	managedLarkUpgradeFixture(t, h, next)
	h.clock.add(2 * time.Minute)
	h.m.mu.Lock()
	runner := h.m.notifications.NewRunnerLocked(before.Metadata().Key)
	h.m.mu.Unlock()
	for i := 0; i < 32; i++ {
		_, stop := runner.Step(context.Background())
		after := h.state()
		if stop || after.ErrorCode != "" {
			t.Fatal("upgraded notification failed", after.ErrorCode)
		}
		if after.LastFull.After(before.LastFull) {
			if after.SubscriptionID != before.SubscriptionID || !after.Baseline.Equal(before.Baseline) {
				t.Fatal("upgrade replaced subscription")
			}
			h.m.mu.Lock()
			bindings, err := h.m.readToolBindingsLocked(h.room.RoomIdentity)
			h.m.mu.Unlock()
			if err != nil || len(bindings.Bindings) != 0 {
				t.Fatal("notification required member tools", err)
			}
			return
		}
	}
	t.Fatal("upgraded collector did not finish")
}

func TestOnboardingAdapterFailureCategories(t *testing.T) {
	for _, code := range []string{"cli_unavailable", "cli_version_mismatch", "invalid_cli_storage", "identity_mismatch", "permission_denied", "credential_rejected", "network_error"} {
		if got := onboardingFailure(&lark.Error{Code: code}); got != code {
			t.Fatal(code, got)
		}
	}
	if onboardingFailure(errors.Join(ErrIdentity, &lark.Error{Code: "cli_unavailable"})) != "target_changed" {
		t.Fatal("identity precedence lost")
	}
	if onboardingFailure(errors.New("private details")) != "apply_interrupted" {
		t.Fatal("unknown error exposed")
	}
}
