package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/computer"
	"github.com/go-aie/teamx/internal/computer/aio"
	runtime "github.com/go-aie/teamx/internal/computer/aio/runtime"
	computerdocker "github.com/go-aie/teamx/internal/computer/docker"
)

type managedDockerFixture struct {
	endpoint             string
	checks, steps, stops atomic.Int32
	block                chan struct{}
	started              chan struct{}
	stopError            atomic.Bool
}

func (f *managedDockerFixture) Check(context.Context) (computerdocker.Environment, error) {
	f.checks.Add(1)
	return computerdocker.Environment{Architecture: "amd64"}, nil
}
func (f *managedDockerFixture) Step(ctx context.Context, s *computerdocker.State, key string) (bool, error) {
	f.steps.Add(1)
	if len(key) != 64 {
		return false, errors.New("missing generated private key")
	}
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	s.Host = "unix:///tmp/teamx-managed-test.sock"
	s.Daemon = "test"
	s.Container = strings.Repeat("d", 64)
	s.Endpoint = f.endpoint
	s.TokenInstalled = true
	s.Stopped = false
	return true, nil
}
func (f *managedDockerFixture) Stop(ctx context.Context, s *computerdocker.State) error {
	f.stops.Add(1)
	if f.stopError.Load() {
		return errors.New("Docker stop not confirmed")
	}
	s.Stopped = true
	return nil
}
func managedInput(c *computerController) computerChangeInput {
	in := connectionInput(c, "")
	in.RuntimeMode = "docker"
	in.CredentialAction = "keep"
	in.Credential = ""
	in.DockerCPUs = 4
	in.DockerMemoryGiB = 6
	return in
}
func managedController(t *testing.T) (*computerController, *managedDockerFixture) {
	t.Helper()
	_, _, endpoint := connectionRuntime(t)
	c, _ := connectionController(t, filepath.Join(t.TempDir(), "config.toml"), func(name, url, key string) (computer.ManagedProvider, error) { return aio.New(url, computerTestKey) })
	f := &managedDockerFixture{endpoint: endpoint}
	c.docker = f
	t.Cleanup(func() { c.cancel(); c.finishDockerWorker() })
	return c, f
}
func driveManaged(t *testing.T, c *computerController, id string, terminal string) computerChange {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		c.advance()
		c.mu.Lock()
		var found computerChange
		for _, op := range c.journal.Operations {
			if op.ID == id {
				found = op
			}
		}
		c.mu.Unlock()
		if found.Status == terminal {
			return found
		}
		time.Sleep(time.Millisecond)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t.Fatalf("operation not %s: %+v", terminal, c.journal.Operations)
	return computerChange{}
}
func TestComputerManagedEnableDisableReusesOwnedRuntimeAndUninstallsBeforeStop(t *testing.T) {
	c, f := managedController(t)
	in := managedInput(c)
	if _, err := c.save(in); err != nil {
		t.Fatal(err)
	}
	driveManaged(t, c, in.ID, "completed")
	if !c.current.Load().Enabled || !c.ready || c.current.Load().Endpoint != f.endpoint {
		t.Fatal("managed connection not applied")
	}
	c.mu.Lock()
	original := *c.journal.Docker
	c.mu.Unlock()
	// Pending stop cannot be reported as fully disabled even after config commit.
	f.stopError.Store(true)
	off := managedInput(c)
	off.Enabled = false
	if _, err := c.save(off); err != nil {
		t.Fatal(err)
	}
	op := driveManaged(t, c, off.ID, "waiting")
	if op.Stage != "runtime_stop" || c.current.Load().Enabled || c.ready {
		t.Fatal("stop failure falsely completed disable")
	}
	f.stopError.Store(false)
	driveManaged(t, c, off.ID, "completed")
	c.mu.Lock()
	stopped := *c.journal.Docker
	c.mu.Unlock()
	if !stopped.Stopped || stopped.KeyRef != original.KeyRef || stopped.Container != original.Container {
		t.Fatal("disable discarded identity")
	}
	steps := f.steps.Load()
	c.advance()
	if f.steps.Load() != steps {
		t.Fatal("disabled controller touched Docker")
	}
	again := managedInput(c)
	if _, err := c.save(again); err != nil {
		t.Fatal(err)
	}
	driveManaged(t, c, again.ID, "completed")
	c.mu.Lock()
	after := *c.journal.Docker
	c.mu.Unlock()
	if after.Creation != original.Creation || after.KeyRef != original.KeyRef {
		t.Fatal("enable created another runtime")
	}
}
func TestComputerManagedPreparationCanBeDisabledWithoutWaitingForPull(t *testing.T) {
	c, f := managedController(t)
	f.block = make(chan struct{})
	f.started = make(chan struct{}, 1)
	in := managedInput(c)
	if _, err := c.save(in); err != nil {
		t.Fatal(err)
	}
	c.advance()
	select {
	case <-f.started:
	case <-time.After(time.Second):
		t.Fatal("prepare not started")
	}
	off := managedInput(c)
	off.Enabled = false
	receipt, err := c.save(off)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ID != in.ID || receipt.DisableID != off.ID {
		t.Fatal("disable not associated with original operation")
	}
	if _, err = c.save(off); err != nil {
		t.Fatal("disable receipt was not idempotent", err)
	}
	driveManaged(t, c, in.ID, "completed")
	if c.current.Load().Enabled || c.ready || f.stops.Load() != 1 {
		t.Fatal("pending prepare was not cancelled and stopped")
	}
}
func TestComputerManagedOwnedStopWaitsForScreenSettlement(t *testing.T) {
	engine, driver, endpoint := connectionRuntime(t)
	c, _ := connectionController(t, filepath.Join(t.TempDir(), "config.toml"), func(name, url, key string) (computer.ManagedProvider, error) { return aio.New(url, computerTestKey) })
	f := &managedDockerFixture{endpoint: endpoint}
	c.docker = f
	t.Cleanup(func() { c.cancel(); c.finishDockerWorker() })
	in := managedInput(c)
	if _, err := c.save(in); err != nil {
		t.Fatal(err)
	}
	driveManaged(t, c, in.ID, "completed")
	binding := connectionBinding(t, c, engine, "A")
	screen, err := c.ensureScreen(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	driver.block = make(chan struct{})
	driver.started = make(chan struct{}, 1)
	callDone := make(chan struct{})
	go func() {
		defer close(callDone)
		_, _ = screen.Call(context.Background(), computer.Call{ID: newComputerID(), Scope: computer.Scope{Session: "test-session"}, Tool: "native", Arguments: json.RawMessage(`{}`), ContractDigest: c.current.Load().ContractDigest})
	}()
	<-driver.started
	off := managedInput(c)
	off.Enabled = false
	if _, err = c.save(off); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		c.advance()
		time.Sleep(time.Millisecond)
	}
	if f.stops.Load() != 0 {
		t.Fatal("container stopped before call settlement")
	}
	close(driver.block)
	<-callDone
	driveManaged(t, c, off.ID, "completed")
	if driver.stops.Load() == 0 || f.stops.Load() != 1 {
		t.Fatal("screen and container stops did not complete in order")
	}
}
func TestComputerManagedEnvironmentCheckIsExplicitAndRedacted(t *testing.T) {
	c, f := managedController(t)
	if f.checks.Load()+f.steps.Load() != 0 {
		t.Fatal("constructor touched Docker")
	}
	_ = c.view()
	if f.checks.Load() != 0 {
		t.Fatal("view probed Docker")
	}
	in := managedInput(c)
	raw, _ := json.Marshal(in)
	req := httptest.NewRequest(http.MethodPost, "/v1/computer/check", strings.NewReader(string(raw)))
	w := httptest.NewRecorder()
	serveComputer(w, req, c)
	if w.Code != 200 || f.checks.Load() != 1 || f.steps.Load() != 0 {
		t.Fatal("check did not remain read only", w.Code)
	}
	if strings.Contains(w.Body.String(), "credential") || strings.Contains(w.Body.String(), "/tmp/") {
		t.Fatal("private check details leaked")
	}
	in.Caller.ID = "other"
	raw, _ = json.Marshal(in)
	w = httptest.NewRecorder()
	serveComputer(w, httptest.NewRequest(http.MethodPost, "/v1/computer/check", strings.NewReader(string(raw))), c)
	if w.Code == 200 || f.checks.Load() != 1 {
		t.Fatal("foreign caller probed Docker")
	}
}

func TestComputerManagedDurablePreparationAndStopResumeAfterRestart(t *testing.T) {
	for _, targetStage := range []string{"runtime_prepare", "runtime_ready", "validate", "settle", "commit", "runtime_stop"} {
		t.Run(targetStage, func(t *testing.T) {
			c, f := managedController(t)
			in := managedInput(c)
			if _, err := c.save(in); err != nil {
				t.Fatal(err)
			}
			if targetStage == "runtime_stop" {
				driveManaged(t, c, in.ID, "completed")
				in = managedInput(c)
				in.Enabled = false
				if _, err := c.save(in); err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				c.mu.Lock()
				stage := c.pendingLocked().Stage
				c.mu.Unlock()
				if stage == targetStage {
					break
				}
				c.advance()
				time.Sleep(time.Millisecond)
			}
			c.mu.Lock()
			op := *c.pendingLocked()
			c.mu.Unlock()
			if op.Stage != targetStage {
				t.Fatal(op)
			}
			c.cancel()
			c.finishDockerWorker()
			_ = c.writer.Close(context.Background())
			_ = unlockDirectory(c.lock)
			var count atomic.Int32
			reopened, _ := connectionController(t, c.document.path, func(name, url, key string) (computer.ManagedProvider, error) {
				count.Add(1)
				return aio.New(url, computerTestKey)
			})
			reopened.docker = f
			t.Cleanup(func() { reopened.cancel(); reopened.finishDockerWorker() })
			driveManaged(t, reopened, in.ID, "completed")
			if reopened.current.Load().Enabled != in.Enabled {
				t.Fatal("restart lost target intent")
			}
			if _, err := reopened.save(in); err != nil {
				t.Fatal("original receipt not recoverable", err)
			}
		})
	}
}

func TestComputerManagedBootLeaseRotatesWithoutWeakeningOldGenerationFence(t *testing.T) {
	c, _ := managedController(t)
	contract, _ := computer.NewContract("aio", map[string]string{"build": "boot-test"}, []computer.Tool{{Name: "native", NativeName: "native", Definition: json.RawMessage(`{"name":"native","inputSchema":{"type":"object"}}`)}})
	dir := t.TempDir()
	driver := &connectionDriver{contract: contract}
	e, err := runtime.Open(context.Background(), dir, 2, contract, driver)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := e.Inspect(context.Background())
	cfg := defaultComputerConfig(computerConfig{RuntimeMode: "docker"})
	oldService := c.runtimeService(cfg, info)
	if _, err = e.Claim(context.Background(), computer.Claim{Owner: c.owner, Service: oldService, ExpectedLease: info.Lease}); err != nil {
		t.Fatal(err)
	}
	if err = e.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	e, err = runtime.Open(context.Background(), dir, 2, contract, driver)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close(context.Background())
	info, _ = e.Inspect(context.Background())
	newService := c.runtimeService(cfg, info)
	if newService == oldService {
		t.Fatal("Runtime restart reused retired service")
	}
	if _, err = e.Claim(context.Background(), computer.Claim{Owner: c.owner, Service: oldService, ExpectedLease: info.Lease}); !computer.Is(err, computer.Stale) {
		t.Fatal("old Runtime service regained control", err)
	}
	if _, err = e.Claim(context.Background(), computer.Claim{Owner: c.owner, Service: newService, ExpectedLease: info.Lease}); err != nil {
		t.Fatal("same TeamX process could not recover new Runtime boot", err)
	}
	if c.runtimeService(defaultComputerConfig(computerConfig{}), info) != c.service.ServiceEpoch {
		t.Fatal("manual connection lease semantics changed")
	}
}

func TestComputerManagedLimitsRemainEditableUntilContainerCreationIsPrepared(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(map[bool]string{false: "not_created", true: "creation_prepared"}[prepared], func(t *testing.T) {
			c, f := managedController(t)
			ref, err := c.credentials.put(strings.Repeat("k", 64))
			if err != nil {
				t.Fatal(err)
			}
			d := computerdocker.NewState(c.owner.ComputerID, newComputerID(), ref, computerdocker.Limits{CPUs: 4, MemoryGiB: 6})
			d.FilesVolume = prepared
			d.Stopped = true
			c.mu.Lock()
			c.journal.Docker = &d
			if err = c.persistLocked(); err != nil {
				c.mu.Unlock()
				t.Fatal(err)
			}
			c.mu.Unlock()
			in := managedInput(c)
			in.Enabled = false
			in.DockerCPUs = 2
			in.DockerMemoryGiB = 4
			_, err = c.save(in)
			if prepared {
				if err == nil {
					t.Fatal("changed limits after possible create")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			driveManaged(t, c, in.ID, "completed")
			if c.journal.Docker.Limits.CPUs != 2 || c.current.Load().DockerMemoryGiB != 4 || f.steps.Load()+f.checks.Load() != 0 {
				t.Fatal("disabled limit correction required Docker IO or was lost")
			}
		})
	}
}

func TestComputerManagedDisableDuringPreparationPreservesChosenModeAndLimits(t *testing.T) {
	c, _ := managedController(t)
	in := managedInput(c)
	in.DockerCPUs = 2
	in.DockerMemoryGiB = 4
	if _, err := c.save(in); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.advance()
		c.mu.Lock()
		ready := c.pendingLocked().Stage == "runtime_ready"
		c.mu.Unlock()
		if ready {
			break
		}
		time.Sleep(time.Millisecond)
	}
	off := in
	off.ID = newComputerID()
	off.Enabled = false
	if _, err := c.save(off); err != nil {
		t.Fatal(err)
	}
	driveManaged(t, c, in.ID, "completed")
	if cfg := c.current.Load(); cfg.Enabled || cfg.RuntimeMode != "docker" || cfg.DockerCPUs != 2 || cfg.DockerMemoryGiB != 4 {
		t.Fatal("cancel lost selected managed configuration", cfg)
	}
	on := in
	on.ID = newComputerID()
	on.ExpectedVersion = c.current.Load().Version
	if _, err := c.save(on); err != nil {
		t.Fatal("preserved custom resources could not be re-enabled", err)
	}
	driveManaged(t, c, on.ID, "completed")
}

func TestComputerManagedReenableRefreshesMemberEndpointBeforeCapabilityApplication(t *testing.T) {
	engine, _, endpoint := connectionRuntime(t)
	c, _ := connectionController(t, filepath.Join(t.TempDir(), "config.toml"), func(name, url, key string) (computer.ManagedProvider, error) { return aio.New(url, computerTestKey) })
	f := &managedDockerFixture{endpoint: endpoint}
	c.docker = f
	t.Cleanup(func() { c.cancel(); c.finishDockerWorker() })
	in := managedInput(c)
	if _, err := c.save(in); err != nil {
		t.Fatal(err)
	}
	driveManaged(t, c, in.ID, "completed")
	original := *c.current.Load()
	off := managedInput(c)
	off.Enabled = false
	if _, err := c.save(off); err != nil {
		t.Fatal(err)
	}
	driveManaged(t, c, off.ID, "completed")
	id := MemberIdentity{RoomIdentity: RoomIdentity{RoomID: "port-rebind", RoomInstance: "instance"}, AgentID: "A", MembershipID: "member"}
	e := &computerMemberExecution{resource: computerResource{MemberIdentity: id, Directory: t.TempDir(), Config: original}}
	x := &computerMembers{connection: c, path: filepath.Join(filepath.Dir(c.path), "members.json"), records: computerResources{Version: 1}, members: map[MemberIdentity]*computerMemberExecution{id: e}}
	c.members = x
	h, err := runtime.Handler(engine, computerTestKey)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	f.endpoint = server.URL
	on := managedInput(c)
	if _, err = c.save(on); err != nil {
		t.Fatal(err)
	}
	driveManaged(t, c, on.ID, "completed")
	e.mu.Lock()
	saved := e.resource.Config
	e.mu.Unlock()
	if saved.Endpoint != server.URL || saved.InstanceID != original.InstanceID || saved.Version != c.current.Load().Version {
		t.Fatal("member retained old Docker-assigned endpoint", saved)
	}
}

func TestComputerManagedKeyIsNotReusedForManualEndpointAndOwnedInstanceCannotDetach(t *testing.T) {
	c, f := managedController(t)
	in := managedInput(c)
	if _, err := c.save(in); err != nil {
		t.Fatal(err)
	}
	driveManaged(t, c, in.ID, "completed")
	original := *c.current.Load()
	manual := connectionInput(c, f.endpoint)
	manual.CredentialAction = "keep"
	manual.Credential = ""
	if _, err := c.save(manual); err == nil {
		t.Fatal("generated managed key reused for manual endpoint")
	}
	if *c.current.Load() != original {
		t.Fatal("invalid manual change altered active managed connection")
	}
	manual.ID = newComputerID()
	manual.CredentialAction = "replace"
	manual.Credential = computerTestKey
	if _, err := c.save(manual); err != nil {
		t.Fatal(err)
	}
	op := driveManaged(t, c, manual.ID, "failed")
	if op.Message != errComputerManagedManualAlias.Error() || f.stops.Load() != 0 || *c.current.Load() != original {
		t.Fatal("same managed Runtime was detached or stopped", op)
	}
	off := connectionInput(c, "")
	off.ID = newComputerID()
	off.Enabled = false
	off.CredentialAction = "keep"
	off.Credential = ""
	if _, err := c.save(off); err != nil {
		t.Fatal(err)
	}
	driveManaged(t, c, off.ID, "completed")
	if c.current.Load().CredentialRef != "" || c.journal.Docker.KeyRef != original.CredentialRef {
		t.Fatal("manual disabled config exposed or discarded managed key")
	}
}
