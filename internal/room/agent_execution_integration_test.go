//go:build integration && (darwin || linux)

package room

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/agent"
	"github.com/go-aie/teamx/internal/agent/dsh"
)

type supervisedFixtureExecution struct {
	agent.ExecutionFactory
	mu       sync.Mutex
	requests []agent.ExecutionRequest
	gate     <-chan struct{}
}

func (f *supervisedFixtureExecution) Start(ctx context.Context, r agent.ExecutionRequest, failed func(error)) (agent.Execution, error) {
	p, err := f.ExecutionFactory.Start(ctx, r, failed)
	if p == nil {
		return nil, err
	}
	f.mu.Lock()
	f.requests = append(f.requests, r)
	gate := f.gate
	f.mu.Unlock()
	return &supervisedFixtureHandle{Execution: p, gate: gate}, err
}

type supervisedFixtureHandle struct {
	agent.Execution
	gate <-chan struct{}
}

func (p *supervisedFixtureHandle) Stop(grace time.Duration) {
	if p.gate != nil {
		<-p.gate
	}
	p.Execution.Stop(grace)
}

// Exercise the ordinary member host, checkpoint writer, quota and rotation
// owner with a real ACP subprocess and a replaceable execution environment.
func TestMemberExecutionRetainsOwnershipAndResumesThenRotates(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for the offline ACP subprocess")
	}
	node, err = filepath.EvalSymlinks(node)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs("../../scripts/fixtures/background-acp.mjs")
	if err != nil {
		t.Fatal(err)
	}
	dir, id := recoveryFixture(t)
	workspace := t.TempDir()
	gate := filepath.Join(workspace, "release-prompt")
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	selected := &supervisedFixtureExecution{ExecutionFactory: dsh.NativeExecution()}
	resources := newExecutionResources(executionResourceLimits{Sessions: 1})
	access, _, _ := accessFixture(t)
	config := recoveryConfig()
	config.access, config.inboxMode = access, "cli"
	config.execution, config.executionResources, config.ownerUserID = selected, resources, access.space.OwnerUserID
	config.environment = []string{"PATH=/usr/bin:/bin", "TEAMX_EXECUTION_FIXTURE_KEY=synthetic", "TEAMX_BACKGROUND_LOG=" + filepath.Join(workspace, "methods.jsonl"), "TEAMX_BACKGROUND_RELEASE=" + gate}
	config.dshProfiles = map[string]dshProfile{"execution": {BinaryPath: node, BinaryArgs: []string{fixture}, Artifact: dsh.Artifact{Source: "offline-fixture", Version: "1"}, Workspace: workspace, Instructions: "offline execution fixture", Model: dsh.ModelConfig{Protocol: "openai-completions", BaseURL: "https://example.invalid/v1", ModelID: "background-fixture", APIKeyEnv: "TEAMX_EXECUTION_FIXTURE_KEY"}}}
	svc, err := newService(dir, id.RoomID, config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	backend := svc.memberBackend(RunnerConfig{Kind: "dsh", Profile: "execution"})
	opts := adapterOptions(svc.store, id)
	s, err := backend.OpenSession(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	ref, _, err := backend.reference(opts)
	if err != nil {
		t.Fatal(err)
	}
	closedGate := make(chan struct{})
	// The handle captured its gate at acquisition. Close this first Session
	// normally, then resume with a delayed supervisor cleanup.
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	selected.mu.Lock()
	selected.gate = closedGate
	selected.mu.Unlock()
	backend.recover = true
	resumed, err := backend.OpenSession(t.Context(), opts)
	if err != nil {
		close(closedGate)
		t.Fatal(err)
	}
	defer resumed.Close(context.Background())
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	err = resumed.Close(ctx)
	cancel()
	if !errors.Is(err, dsh.ErrCleanupPending) {
		close(closedGate)
		t.Fatal("supervisor cleanup released Session", err)
	}
	if release, err := resources.acquireSession("other", "other"); !errors.Is(err, ErrBusy) {
		if release != nil {
			release()
		}
		close(closedGate)
		t.Fatal("supervisor cleanup released quota", err)
	}
	if err := backend.checkRotationClosed(opts, ref.Directory, false); err == nil {
		close(closedGate)
		t.Fatal("unsettled execution allowed a new generation")
	}
	close(closedGate)
	if err := resumed.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := backend.checkRotationClosed(opts, ref.Directory, false); err != nil {
		t.Fatal(err)
	}
	selected.mu.Lock()
	selected.gate = nil
	selected.mu.Unlock()
	nextGeneration := sessionGenerationForOperation("11111111111111111111111111111111")
	next, err := backend.switchSession(opts, ref.Directory, nextGeneration)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := next.OpenSession(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer rotated.Close(context.Background())
	if err := rotated.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	selected.mu.Lock()
	defer selected.mu.Unlock()
	if len(selected.requests) != 3 {
		t.Fatal("unexpected execution acquisition count", len(selected.requests))
	}
	for i, r := range selected.requests {
		generation := ref.Directory
		if i == 2 {
			generation = nextGeneration
		}
		if r.Binding.SpaceID != access.space.SpaceID || r.Binding.MemberIdentity != id || r.Binding.UserID != config.ownerUserID || r.Binding.Generation != generation {
			t.Fatal("member scope changed", r.Binding)
		}
	}
}

func TestManagerReleasesRootAfterSettledExecutionFailure(t *testing.T) {
	m := activationTestManager(t)
	room := managerCreate(t, m, "settled-execution")
	openOK(t, m, room.RoomIdentity, room.CreateID)
	svc := m.runtimes[room.RoomIdentity].service
	if err := svc.close(t.Context()); err != nil {
		t.Fatal(err)
	}
	diagnostic := errors.New("original Session cannot resume")
	svc.closeError = &settledBackendClose{diagnostic}
	defer func() { svc.closeError = nil }()
	if err := m.shutdown(t.Context()); !errors.Is(err, diagnostic) || !backendCleanupSettled(err) {
		t.Fatal("lost settled diagnostic", err)
	}
	reopened, err := newRoomManager(m.store.dir, 7)
	if err != nil {
		t.Fatal("settled manager retained root", err)
	}
	reopened.close()
}
