package dsh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/agent"
)

// This substitute wraps the real local ACP fixture, not the protocol backend.
// It verifies that changing execution does not duplicate ACP or its owner.
type alternateExecution struct {
	native    agent.ExecutionFactory
	mu        sync.Mutex
	requests  []agent.ExecutionRequest
	identity  agent.ExecutionIdentity
	openError error
	stopGate  <-chan struct{}
}

func (f *alternateExecution) Identify(r agent.ExecutionRequest) (agent.ExecutionIdentity, error) {
	id := f.identity
	if r.StateDir != "" {
		data, _ := json.Marshal(r.Binding)
		id.PolicySHA256 = digestForTest(data)
	}
	return id, nil
}
func (f *alternateExecution) Start(ctx context.Context, r agent.ExecutionRequest, failed func(error)) (agent.Execution, error) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	f.mu.Unlock()
	p, err := f.native.Start(ctx, r, failed)
	if p == nil {
		return nil, err
	}
	if f.stopGate != nil {
		p = &delayedExecution{Execution: p, gate: f.stopGate}
	}
	return p, errors.Join(err, f.openError)
}

type delayedExecution struct {
	agent.Execution
	gate <-chan struct{}
}

func (p *delayedExecution) Stop(grace time.Duration) { <-p.gate; p.Execution.Stop(grace) }
func digestForTest(data []byte) string { // use the same stable primitive as config identity
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestExecutionSelectionResumeAndIdentity(t *testing.T) {
	opts := fixtureOptions(t)
	factory := &alternateExecution{native: nativeExecution{inspect: opts.inspect}, identity: agent.ExecutionIdentity{Kind: "substitute", Version: "1", RuntimeSHA256: strings.Repeat("a", 64)}}
	opts.Execution = factory
	opts.ExecutionBinding = agent.ExecutionBinding{UserID: "u", SpaceID: "a", Generation: "state", MemberIdentity: agent.MemberIdentity{RoomIdentity: agent.RoomIdentity{RoomID: "r", RoomInstance: "i"}, AgentID: "a", MembershipID: "m"}}
	s := openFixture(t, opts)
	if _, err := s.Run(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	ref, err := recoveryReference(opts.StateDir)
	binary, _ := filepath.EvalSymlinks(opts.BinaryPath)
	if err != nil || ref.Execution == nil || ref.Execution.Kind != "substitute" || ref.BinaryPath != binary {
		t.Fatal("execution replaced DSH identity", ref, err)
	}
	for range 2 {
		if err := CheckResume(opts.StateDir); err != nil {
			t.Fatal(err)
		}
		if err := CheckResumeConfig(opts); err != nil {
			t.Fatal(err)
		}
	}
	if len(factory.requests) != 1 {
		t.Fatal("metadata check started execution")
	}
	for _, mutate := range []func(*Options){
		func(o *Options) { o.ExecutionBinding.SpaceID = "b" },
		func(o *Options) { o.ExecutionBinding.Generation = "other" },
		func(o *Options) { o.Execution = nil },
	} {
		changed := opts
		mutate(&changed)
		if err := CheckResumeConfig(changed); !errors.Is(err, ErrRecoveryUnsupported) {
			t.Fatal("accepted changed execution", err)
		}
	}
	if err := CheckResume(opts.StateDir); err != nil {
		t.Fatal("read-only rejection consumed shutdown evidence", err)
	}
	resumed, err := Resume(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close(context.Background())
	if resumed.id != s.id || len(factory.requests) != 2 || factory.requests[1].Binding != opts.ExecutionBinding {
		t.Fatal("resume replaced Session or binding")
	}
	if err := resumed.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionPartialOpenRetainsHandleAndLock(t *testing.T) {
	opts := fixtureOptions(t)
	opts.Limits.CleanupTimeout = 30 * time.Millisecond
	gate := make(chan struct{})
	factory := &alternateExecution{native: nativeExecution{inspect: opts.inspect}, identity: agent.ExecutionIdentity{Kind: "substitute", Version: "1"}, openError: errors.New("proxy failed after allocation"), stopGate: gate}
	opts.Execution = factory
	s, err := Open(t.Context(), opts)
	if s == nil || !errors.Is(err, ErrCleanupPending) {
		close(gate)
		t.Fatal("partial execution lost", s, err)
	}
	if lock, err := lockDirectory(opts.StateDir); !errors.Is(err, ErrLocked) {
		if lock != nil {
			lock.Close()
		}
		close(gate)
		t.Fatal("partial execution released lock", err)
	}
	if err := CheckReset(opts.StateDir); err == nil {
		close(gate)
		t.Fatal("partial execution authorized replacement")
	}
	close(gate)
	opts.Limits.CleanupTimeout = time.Second
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for {
		err = s.Close(ctx)
		if !errors.Is(err, ErrCleanupPending) {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(err)
		}
	}
	if !CleanupSettled(err) {
		t.Fatal("original execution did not settle", err)
	}
	if err := CheckReset(opts.StateDir); err != nil {
		t.Fatal(err)
	}
	if err := CheckResume(opts.StateDir); err == nil {
		t.Fatal("failed open became resumable")
	}
}

func TestResumeVerifiesDSHScriptSeparately(t *testing.T) {
	opts := fixtureOptions(t)
	script := filepath.Join(opts.Workspace, "fixture.mjs")
	if err := os.WriteFile(script, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	// The Go ACP fixture ignores arguments following --. The hash is still a
	// real script artifact and must remain independent of the selected wrapper.
	opts.BinaryArgs = append(opts.BinaryArgs, script)
	s := openFixture(t, opts)
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckResumeConfig(opts); !errors.Is(err, ErrRecoveryUnsupported) {
		t.Fatal("changed DSH script accepted", err)
	}
	if err := CheckResume(opts.StateDir); err != nil {
		t.Fatal("script check consumed evidence", err)
	}
}

func TestRuntimeInspectionRetainsFailedSpaceWithoutBlockingAnother(t *testing.T) {
	opts := fixtureOptions(t)
	opts.Limits.CleanupTimeout = 30 * time.Millisecond
	gate := make(chan struct{})
	factory := &alternateExecution{native: nativeExecution{inspect: opts.inspect}, stopGate: gate,
		identity: agent.ExecutionIdentity{Kind: "substitute", Version: "1"}}
	opts.Execution = factory
	opts.ExecutionBinding = agent.ExecutionBinding{UserID: "user", SpaceID: "a"}
	if _, err := InspectRuntime(t.Context(), opts); !errors.Is(err, ErrCleanupPending) {
		close(gate)
		t.Fatal("inspection lost pending execution", err)
	}
	if len(factory.requests) != 1 {
		close(gate)
		t.Fatal("unexpected version acquisition")
	}
	root := filepath.Dir(factory.requests[0].StateDir)
	if _, err := os.Stat(root); err != nil {
		close(gate)
		t.Fatal("pending inspection removed its directory", err)
	}
	if _, err := InspectRuntime(t.Context(), opts); !errors.Is(err, ErrCleanupPending) || len(factory.requests) != 1 {
		close(gate)
		t.Fatal("same space bypassed pending inspection", err)
	}
	factory.stopGate = nil
	other := opts
	other.ExecutionBinding.SpaceID = "b"
	if _, err := InspectRuntime(t.Context(), other); err != nil {
		close(gate)
		t.Fatal("failed space blocked another space", err)
	}
	close(gate)
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("settled inspection leaked its directory")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := InspectRuntime(t.Context(), opts); err != nil {
		t.Fatal("settlement did not reopen original space", err)
	}
}

func TestRelayConnectionsDoNotBecomeInfrastructureOrTools(t *testing.T) {
	live := map[int]processIdentity{
		1: {pid: 1, started: "supervisor"},
		2: {pid: 2, parent: 1, started: "relay"},
		3: {pid: 3, parent: 2, started: "connection"},
		4: {pid: 4, parent: 3, started: "nested-connection"},
		5: {pid: 5, parent: 1, started: "dsh"},
		6: {pid: 6, parent: 5, started: "tool"},
	}
	relays := map[int]processIdentity{2: live[2], 3: live[3]}
	baseline := withoutRelayChildren(live, relays)
	if len(baseline) != 4 || baseline[2].started != "relay" || baseline[6].started != "tool" {
		t.Fatal(baseline)
	}
	delete(live, 3)
	delete(live, 4)
	if !sameProcessesPresent(baseline, live) {
		t.Fatal("connection completion broke infrastructure")
	}
	delete(live, 2)
	if sameProcessesPresent(baseline, live) {
		t.Fatal("listener exit was ignored")
	}
	live[2] = processIdentity{pid: 2, parent: 1, started: "reused-pid"}
	live[7] = processIdentity{pid: 7, parent: 2, started: "unrelated"}
	if got := withoutRelayChildren(live, relays); got[7].started != "unrelated" {
		t.Fatal("reused PID acquired old relay authority")
	}
}
