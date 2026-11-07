//go:build e2e

package lark

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// Explicit opt-in, read-only. This cannot send or change authorization.
func TestRealLarkPinnedReadiness(t *testing.T) {
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
	pinned, err := Pin(ctx, p, v.Account)
	if err != nil {
		t.Fatal("pin:", ErrorCode(err))
	}
	defer pinned.Close()
	fixed, err := pinned.Verify(ctx)
	if err != nil || fixed.Account != v.Account {
		t.Fatal("fixed identity:", ErrorCode(err))
	}
	permission, err := pinned.SendPermission(ctx)
	if err != nil {
		t.Fatal("permission:", ErrorCode(err))
	}
	t.Logf("CLI=%s account_fixed=%t application_scopes=%s user_scopes=%s missing_application=%d missing_user=%d adapter_implemented=%t", CLIVersion, permission.AccountFixed, permission.ApplicationScopes, permission.UserScopes, len(permission.MissingApplication), len(permission.MissingUser), permission.AdapterImplemented)
	t.Logf("missing_application=%v missing_user=%v", permission.MissingApplication, permission.MissingUser)
}
