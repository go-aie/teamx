package lark

import (
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func TestClientFactoriesShareQueuesWithinTheirScope(t *testing.T) {
	profile := platform.CLIProfile{Version: 1, Executable: "/fixture/lark-cli", Profile: "fixture", Brand: "feishu", CLIVersion: CLIVersion}
	first, err := New(profile)
	if err != nil {
		t.Fatal(err)
	}
	second, err := (*ClientFactory)(nil).New(profile)
	if err != nil {
		t.Fatal(err)
	}
	if first.requests != second.requests || first.selfSend != second.selfSend || first.gates != second.gates ||
		first.requests.interval != AccountRequestInterval || first.selfSend.interval != time.Second ||
		first.requests.cooldown != time.Minute || first.selfSend.capacity != 8 {
		t.Fatal("default clients must retain the shared production pacing and capacity")
	}
	zero, err := new(ClientFactory).New(profile)
	if err != nil || zero.requests != first.requests || zero.selfSend != first.selfSend || zero.gates != first.gates {
		t.Fatal("zero factory must retain production defaults", err)
	}

	factory := NewClientFactory(0, 0, 0)
	a, _ := factory.New(profile)
	b, _ := factory.New(profile)
	other, _ := NewClientFactory(0, 0, 0).New(profile)
	if a.requests != b.requests || a.selfSend != b.selfSend || a.requests == other.requests ||
		a.selfSend == other.selfSend || a.requests == first.requests || a.selfSend == first.selfSend ||
		a.gates != b.gates || a.gates == other.gates || a.gates == first.gates {
		t.Fatal("explicit factories must share internally and isolate unrelated scopes")
	}
	// Zero pacing retains queue ownership, FIFO admission and self-send capacity.
	ctx := budgetContext("fixture-account")
	release, err := a.selfSend.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a.selfSend.mu.Lock()
	busy := b.selfSend.gates["fixture-account"].busy
	a.selfSend.mu.Unlock()
	release()
	if !busy || a.selfSend.capacity != 8 {
		t.Fatal("zero pacing bypassed shared queue ownership")
	}
}
