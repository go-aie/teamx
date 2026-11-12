//go:build e2e

package lark

import (
	"context"
	"os"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// Read-only acceptance on the operator-selected existing profile. In particular
// this test never authorizes, removes a scope, or dispatches a message.
func TestRealLarkPermissionCheck(t *testing.T) {
	profile := os.Getenv("TEAMX_LARK_TEST_PROFILE")
	if profile == "" {
		t.Skip("set TEAMX_LARK_TEST_PROFILE")
	}
	p := platform.CLIProfile{Version: 1, Executable: os.Getenv("TEAMX_LARK_TEST_BIN"), Profile: profile, Brand: "feishu", CLIVersion: CLIVersion}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c, err := New(p)
	if err != nil {
		t.Fatal(ErrorCode(err))
	}
	v, err := c.Verify(ctx)
	if err != nil {
		t.Fatal(ErrorCode(err))
	}
	result, err := CheckPermissions(ctx, p, v.Account)
	if err != nil || result.Account != v.Account || result.CheckedAt.IsZero() {
		t.Fatal("check:", ErrorCode(err))
	}
	requested, err := result.RequestScopes()
	if err != nil {
		t.Fatal("request scope calculation:", ErrorCode(err))
	}
	// This opt-in turns a live administrator-selected application gap into an
	// assertion, rather than accepting an all-granted observation as evidence.
	missing := os.Getenv("TEAMX_LARK_TEST_MISSING_APPLICATION_SCOPE")
	if missing != "" {
		if missing == "offline_access" || !slices.Contains(DefaultScopes(), missing) {
			t.Fatal("expected gap must be a supported application business scope")
		}
		if result.Application.State != "missing" || !slices.Contains(result.Application.Missing, missing) || slices.Contains(requested, missing) {
			t.Fatal("application gap missing from check or included in OAuth request")
		}
		dependencies := CapabilityDependencies()
		for i, capability := range result.Capabilities {
			if slices.Contains(dependencies[i].Scopes, missing) {
				if capability.State != "blocked" || capability.ReasonCode != "application_scopes_missing" || capability.NextAction != "configure_application" {
					t.Fatal("application gap did not identify its exact dependency", capability.Name)
				}
			} else if dependencies[i].RequiresPin && runtime.GOOS != "darwin" {
				if capability.State != "unsupported" {
					t.Fatal("non-macOS sending limitation lost", capability.Name)
				}
			} else if capability.State != "ready" {
				t.Fatal("independent capability degraded", capability.Name, capability.ReasonCode)
			}
		}
		t.Logf("verified_application_gap=%s excluded_from_oauth_request=true independent_capabilities_preserved=true", missing)
	}
	t.Logf("CLI=%s application=%s user=%s renewal=%s account_fixed=%t requested=%d writes=0", CLIVersion, result.Application.State, result.User.State, result.Renewal.State, result.AccountFixed, len(requested))
	for _, v := range result.Capabilities {
		if v.Configured != nil {
			t.Fatal("platform check fabricated a member grant")
		}
		t.Logf("capability=%s state=%s reason=%s missing_application=%v missing_user=%v", v.Name, v.State, v.ReasonCode, v.MissingApplicationScopes, v.MissingUserScopes)
	}
}
