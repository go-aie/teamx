package lark

import (
	"os"
	"path/filepath"
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

func TestRuntimeProgramDiscoveryAndFactoryComposition(t *testing.T) {
	original, _ := authTestProfile(t)
	next, _ := authTestProfile(t)
	if err := os.Remove(original.Executable); err != nil {
		t.Fatal(err)
	}
	factory := NewClientFactory(0, 0, 0)
	bound, err := factory.WithExecutable(next.Executable)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	bound, err = bound.WithTemporaryRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	client, err := bound.New(original)
	if err != nil {
		t.Fatal(err)
	}
	if client.profile != original || client.executable != next.Executable || client.gates != factory.gates || client.requests != factory.requests {
		t.Fatal("factory composition lost binding/queues")
	}
	if _, err := client.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	discovery, err := bound.Discover(t.Context(), "", "feishu")
	if err != nil || discovery.Executable != next.Executable || !discovery.ManagedProgram || discovery.ManagedStorage || len(discovery.Profiles) != 1 {
		t.Fatal(discovery, err)
	}
	if _, err := bound.Discover(t.Context(), original.Executable, "feishu"); ErrorCode(err) != "cli_unavailable" {
		t.Fatal("managed request selected history", err)
	}
	external, err := New(original)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := external.Verify(t.Context()); ErrorCode(err) != "cli_unavailable" {
		t.Fatal("external profile was silently replaced", err)
	}
	// A client already admitted under B stays on B after another factory binds C.
	third, _ := authTestProfile(t)
	rebound, err := bound.WithExecutable(third.Executable)
	if err != nil {
		t.Fatal(err)
	}
	if client.executable != next.Executable || rebound.Executable(original) != third.Executable {
		t.Fatal("existing call switched programs")
	}
	if err := os.Chmod(next.Executable, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Verify(t.Context()); ErrorCode(err) != "cli_unavailable" {
		t.Fatal("availability was cached", err)
	}
	if err := os.Chmod(next.Executable, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Verify(t.Context()); err != nil {
		t.Fatal("repaired program did not recover", err)
	}
}
