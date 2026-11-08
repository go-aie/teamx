//go:build e2e

package lark

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// Read-only native preflight; it has no writer and grants no member capability.
func TestRealLarkSendTargetPreflight(t *testing.T) {
	profile, group := os.Getenv("TEAMX_LARK_TEST_PROFILE"), os.Getenv("TEAMX_LARK_TEST_GROUP")
	if profile == "" || group == "" {
		t.Skip("set TEAMX_LARK_TEST_PROFILE and TEAMX_LARK_TEST_GROUP for the authorized test group")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	p := platform.CLIProfile{Version: 1, Executable: os.Getenv("TEAMX_LARK_TEST_BIN"), Profile: profile, Brand: "feishu", CLIVersion: CLIVersion}
	c, err := New(p)
	if err != nil {
		t.Fatal(ErrorCode(err))
	}
	v, err := c.Verify(ctx)
	if err != nil {
		t.Fatal(ErrorCode(err))
	}
	pin, err := Pin(ctx, p, v.Account)
	if err != nil {
		t.Fatal(ErrorCode(err))
	}
	defer pin.Close()
	chat, err := pin.client.SendGroup(ctx, v.Account, group)
	if err != nil {
		t.Fatal("group native checks:", ErrorCode(err))
	}
	if chat.ID != group || chat.Mode != "group" || chat.TenantKey != v.Account.Tenant.Value {
		t.Fatal("group identity mismatch")
	}
	access := platform.AccessContext{RoomIdentity: platform.RoomIdentity{RoomID: "preflight", RoomInstance: "preflight"}, AgentID: "preflight", MembershipID: "preflight", Account: v.Account, ConnectionID: "preflight", Generation: "preflight", ConnectionVersion: 1, CredentialVersion: 1}
	target := platform.TargetIdentity{IdentityScope: v.Account.IdentityScope, Object: platform.NativeIdentity{Type: "user", ID: v.Account.ID}}
	r, err := ResolveSendTarget(ctx, platform.TargetLocator{Candidate: &target}, access, pin.SendReader())
	if err != nil || r.Status != platform.ResolutionResolved || r.Target == nil || r.Target.Object.ID != v.Account.ID || len(r.Target.Containers) != 1 {
		t.Fatal("native self relationship unverified", r.Status, ErrorCode(err))
	}
	permission, err := pin.SendPermission(ctx)
	if err != nil {
		t.Fatal(ErrorCode(err))
	}
	t.Logf("CLI=%s fixed_account=true group_type=group joined=true speaking=all_members self_mode=%s application_scopes=%s user_scopes=%s writes=0", CLIVersion, r.Status, permission.ApplicationScopes, permission.UserScopes)
}
