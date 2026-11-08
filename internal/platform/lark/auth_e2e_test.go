//go:build e2e

package lark

import (
	"context"
	"os"
	"testing"
	"time"
)

// Explicit read-only acceptance; never creates, switches or logs out a profile.
func TestLarkDiscoveryReal(t *testing.T) {
	if os.Getenv("TEAMX_LARK_DISCOVERY_REAL") != "1" {
		t.Skip("set TEAMX_LARK_DISCOVERY_REAL=1 for local CLI discovery")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	result, err := Discover(ctx, "", "feishu")
	if err != nil || result.Executable == "" {
		t.Fatal("real CLI discovery failed", ErrorCode(err))
	}
	t.Logf("detected pinned CLI and %d Feishu profile(s)", len(result.Profiles))
}
