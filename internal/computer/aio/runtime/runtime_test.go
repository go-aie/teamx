package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/computer"
	"github.com/go-aie/teamx/internal/identity"
	"github.com/go-aie/teamx/internal/persistence"
)

type fakeDriver struct {
	mu                   sync.Mutex
	catalog              computer.Contract
	starts, calls, stops int
	failStart, failStop  bool
	started, release     chan struct{}
}

func (f *fakeDriver) Validate(string, json.RawMessage) error { return nil }
func (f *fakeDriver) Start(context.Context, Resource) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	if f.failStart {
		return errors.New("partial start")
	}
	return nil
}
func (f *fakeDriver) Tools(context.Context, Resource) (computer.Contract, error) {
	return f.catalog, nil
}
func (f *fakeDriver) Call(ctx context.Context, r Resource, name string, args json.RawMessage) (json.RawMessage, error) {
	f.mu.Lock()
	f.calls++
	started, release := f.started, f.release
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return json.RawMessage(`{"content":[{"type":"text","text":"native"}],"structuredContent":{"ok":true},"isError":true,"extension":{"kept":123}}`), nil
}
func (f *fakeDriver) Stop(context.Context, Resource) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	if f.failStop {
		return errors.New("process still alive")
	}
	return nil
}
func catalog(t *testing.T, provider, write, read string) computer.Contract {
	t.Helper()
	c, err := computer.NewContract(provider, map[string]string{"version": "fixed"}, []computer.Tool{
		{Name: write, NativeName: write, Definition: json.RawMessage(`{"name":"` + write + `","inputSchema":{"type":"object"},"outputSchema":{"type":"object"},"extension":true}`)},
		{Name: read, NativeName: read, ReadOnly: true, Definition: json.RawMessage(`{"name":"` + read + `","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":false}}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func setup(t *testing.T, c computer.Contract, max int) (*Engine, *fakeDriver, computer.Binding, string) {
	t.Helper()
	dir := t.TempDir()
	d := &fakeDriver{catalog: c}
	e, err := Open(context.Background(), dir, max, c, d)
	if err != nil {
		t.Fatal(err)
	}
	b := claim(t, e, "service-a")
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	return e, d, b, dir
}
func claim(t *testing.T, e *Engine, service string) computer.Binding {
	t.Helper()
	info, err := e.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	owner := computer.Owner{InstallationID: "install", OwnerID: "owner", ComputerID: "computer"}
	lease, err := e.Claim(context.Background(), computer.Claim{Owner: owner, Service: service, ExpectedLease: info.Lease})
	if err != nil {
		t.Fatal(err)
	}
	return computer.Binding{Owner: owner, Member: computer.Member{RoomIdentity: identity.RoomIdentity{RoomID: "room", RoomInstance: "instance"}, AgentID: "agent", MembershipID: "membership"}, Service: service, Lease: lease}
}
func ensure(t *testing.T, e *Engine, b computer.Binding) computer.Screen {
	t.Helper()
	s, err := e.EnsureScreen(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func call(c computer.Contract, name, id string) computer.Call {
	return computer.Call{ID: id, Scope: computer.Scope{Session: "generation", Turn: "turn"}, Tool: name, Arguments: json.RawMessage(`{"private":"do-not-persist"}`), ContractDigest: c.Digest}
}

// Different native catalogs exercise the same management contract. They are
// test providers, not evidence for a second real supplier.
func TestManagedProviderContract(t *testing.T) {
	for _, names := range [][3]string{{"test-one", "navigate_native", "snapshot_native"}, {"test-two", "vendor_act", "vendor_observe"}} {
		t.Run(names[0], func(t *testing.T) {
			c := catalog(t, names[0], names[1], names[2])
			e, d, b, _ := setup(t, c, 2)
			var wg sync.WaitGroup
			ids := make(chan string, 12)
			for range 12 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					s, err := e.EnsureScreen(context.Background(), b)
					if err != nil {
						t.Error(err)
						return
					}
					ids <- s.Info().ID
				}()
			}
			wg.Wait()
			close(ids)
			unique := ""
			for id := range ids {
				if unique == "" {
					unique = id
				}
				if id != unique {
					t.Fatal("duplicate identity created multiple screens")
				}
			}
			if d.starts != 1 {
				t.Fatalf("starts=%d", d.starts)
			}
			s := ensure(t, e, b)
			out, err := s.Call(context.Background(), call(c, names[1], "op"))
			if err != nil {
				t.Fatal(err)
			}
			if !out.ResultAvailable || !json.Valid(out.Result) {
				t.Fatalf("native result lost: %+v", out)
			}
			same, err := s.Call(context.Background(), call(c, names[1], "op"))
			if err != nil || string(same.Result) != string(out.Result) || d.calls != 1 {
				t.Fatalf("duplicate executed: %+v %v calls=%d", same, err, d.calls)
			}
			changed := call(c, names[1], "op")
			changed.Arguments = json.RawMessage(`{"changed":1}`)
			if _, err = s.Call(context.Background(), changed); !computer.Is(err, computer.Invalid) {
				t.Fatalf("changed ID accepted: %v", err)
			}
			peer := b
			peer.Member.AgentID = "peer"
			peer.Member.MembershipID = "peer-member"
			p := ensure(t, e, peer)
			third := b
			third.Member.AgentID = "third"
			if _, err = e.EnsureScreen(context.Background(), third); !computer.Is(err, computer.Capacity) {
				t.Fatalf("capacity: %v", err)
			}
			if err = s.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			replacement := b
			replacement.Member.MembershipID = "replacement"
			next := ensure(t, e, replacement)
			if next.Info().ID == s.Info().ID {
				t.Fatal("replacement inherited profile")
			}
			if _, err = s.Call(context.Background(), call(c, names[1], "stale")); !computer.Is(err, computer.Stale) {
				t.Fatalf("old member could act: %v", err)
			}
			if _, err = e.EnsureScreen(context.Background(), b); !computer.Is(err, computer.Stale) {
				t.Fatalf("old identity reclaimed: %v", err)
			}
			if _, err = p.Call(context.Background(), call(c, names[2], "peer")); err != nil {
				t.Fatalf("peer affected: %v", err)
			}
		})
	}
}
func TestReadOnlyDrainAndScopeFence(t *testing.T) {
	c := catalog(t, "test", "act", "observe")
	e, d, b, dir := setup(t, c, 2)
	s := ensure(t, e, b)
	d.started = make(chan struct{}, 1)
	d.release = make(chan struct{})
	returned := make(chan error, 1)
	go func() { _, err := s.Call(context.Background(), call(c, "observe", "read")); returned <- err }()
	<-d.started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.Drain(ctx, computer.Scope{Session: "generation", Turn: "turn"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read escaped drain: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk state
	if err = json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	if len(disk.Screens[s.Info().ID].Operations) != 0 {
		t.Fatal("readonly persisted an operation")
	}
	close(d.release)
	if err = <-returned; err != nil {
		t.Fatal(err)
	}
	if err = s.Drain(context.Background(), computer.Scope{Session: "generation"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Call(context.Background(), call(c, "act", "late")); !computer.Is(err, computer.Stale) {
		t.Fatalf("drained turn accepted: %v", err)
	}
}
func TestPartialStartAndStopIsolation(t *testing.T) {
	c := catalog(t, "test", "act", "observe")
	e, d, b, _ := setup(t, c, 2)
	d.failStart = true
	if _, err := e.EnsureScreen(context.Background(), b); err == nil {
		t.Fatal("partial start reported usable")
	}
	if d.stops != 1 {
		t.Fatal("partial processes not cleaned")
	}
	d.failStart = false
	s := ensure(t, e, b)
	d.failStop = true
	if err := s.Stop(context.Background()); err == nil {
		t.Fatal("unproven stop reported settled")
	}
	peer := b
	peer.Member.AgentID = "peer"
	p := ensure(t, e, peer)
	if _, err := p.Call(context.Background(), call(c, "observe", "peer")); err != nil {
		t.Fatal(err)
	}
	d.failStop = false
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestCancellationDoesNotSettleRemoteCall(t *testing.T) {
	c := catalog(t, "test", "act", "observe")
	e, d, b, _ := setup(t, c, 2)
	s := ensure(t, e, b)
	d.started = make(chan struct{}, 1)
	d.release = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() { _, err := s.Call(ctx, call(c, "act", "write")); returned <- err }()
	<-d.started
	cancel()
	if err := <-returned; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	pending, err := s.Operation(context.Background(), "write")
	if err != nil || pending.Operation.Settled || pending.Operation.State != "pending" {
		t.Fatalf("cancellation fabricated settlement: %+v %v", pending, err)
	}
	waitCtx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if err = s.Drain(waitCtx, computer.Scope{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	close(d.release)
	if err = s.Drain(context.Background(), computer.Scope{}); err != nil {
		t.Fatal(err)
	}
}
func TestExecutionBeforeTerminalCommitRecoversUnknown(t *testing.T) {
	c := catalog(t, "test", "act", "observe")
	e, d, b, dir := setup(t, c, 1)
	s := ensure(t, e, b)
	ops := persistence.DefaultOperations()
	saves := 0
	ops.Rename = func(from, to string) error {
		saves++
		if saves == 2 {
			return errors.New("terminal commit interrupted")
		}
		return os.Rename(from, to)
	}
	e.writer = persistence.NewWriter(ops)
	if _, err := s.Call(context.Background(), call(c, "act", "crash")); !computer.Is(err, computer.Unknown) {
		t.Fatalf("failed terminal commit: %v", err)
	}
	if d.calls != 1 {
		t.Fatal("action missing")
	}
	// Simulate process loss without a graceful Close that could establish facts.
	if err := e.unlock(); err != nil {
		t.Fatal(err)
	}
	e.closed = true
	recovered, err := Open(context.Background(), dir, 1, c, d)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close(context.Background())
	fresh := claim(t, recovered, "service-b")
	next := ensure(t, recovered, fresh)
	if next.Info().ID != s.Info().ID || next.Info().Epoch <= s.Info().Epoch {
		t.Fatal("profile identity or epoch incorrect")
	}
	op, err := next.Operation(context.Background(), "crash")
	if err != nil {
		t.Fatal(err)
	}
	if op.Operation.State != "unknown" || !op.Operation.Settled || op.ResultAvailable {
		t.Fatalf("recovery invented result: %+v", op)
	}
	if _, err = next.Call(context.Background(), call(c, "act", "crash")); err != nil {
		t.Fatal(err)
	}
	if d.calls != 1 {
		t.Fatal("recovery replayed write")
	}
	if _, err = recovered.Resolve(s.Info()); !computer.Is(err, computer.Stale) {
		t.Fatalf("old lease accepted: %v", err)
	}
	info, _ := recovered.Inspect(context.Background())
	if _, err = recovered.Claim(context.Background(), computer.Claim{Owner: b.Owner, Service: b.Service, ExpectedLease: info.Lease}); !computer.Is(err, computer.Stale) {
		t.Fatalf("retired service reclaimed ownership: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "runtime.json"))
	if strings.Contains(string(raw), "do-not-persist") || strings.Contains(string(raw), "structuredContent") {
		t.Fatal("sensitive input/result persisted")
	}
}

func TestRoomRecreationAndLeaseHandoffFenceOldOwners(t *testing.T) {
	c := catalog(t, "test", "act", "observe")
	e, _, b, _ := setup(t, c, 2)
	old := ensure(t, e, b)
	if err := old.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	nextBinding := b
	nextBinding.Member.RoomInstance = "rebuilt-room"
	nextBinding.Member.AgentID = "different-agent"
	nextBinding.Member.MembershipID = "different-membership"
	next := ensure(t, e, nextBinding)
	if _, err := e.EnsureScreen(context.Background(), b); !computer.Is(err, computer.Stale) {
		t.Fatalf("old Room restored: %v", err)
	}
	staleNewName := b
	staleNewName.Member.AgentID = "unseen-old-agent"
	if _, err := e.EnsureScreen(context.Background(), staleNewName); !computer.Is(err, computer.Stale) {
		t.Fatalf("old Room used a new name: %v", err)
	}
	info, _ := e.Inspect(context.Background())
	request := computer.Claim{Owner: b.Owner, Service: "service-b", ExpectedLease: info.Lease}
	lease, err := e.Claim(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := e.Claim(context.Background(), request)
	if err != nil || repeat != lease {
		t.Fatalf("lost claim response cannot be recovered: %s %v", repeat, err)
	}
	if _, err = next.Call(context.Background(), call(c, "act", "old-service")); !computer.Is(err, computer.Stale) {
		t.Fatalf("old service acted: %v", err)
	}
	nextBinding.Service = request.Service
	nextBinding.Lease = lease
	fresh := ensure(t, e, nextBinding)
	if fresh.Info().ID != next.Info().ID {
		t.Fatal("service handoff replaced persistent screen")
	}
	if _, err = fresh.Call(context.Background(), call(c, "act", "new-service")); err != nil {
		t.Fatal(err)
	}
}
func TestGUIAdmissionKeepsNativeSchemaButRejectsUnauditedActions(t *testing.T) {
	c, p, err := LoadCatalog("../../../../packaging/computer/aio/tools.lock.json", "../../../../packaging/computer/aio/dependencies.lock.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Tools) != 21 {
		t.Fatalf("pinned tools=%d", len(c.Tools))
	}
	for _, action := range []string{"SET_CLIPBOARD", "NODE_INVOKE", "UNKNOWN"} {
		if err = p.Validate("browser_gui_execute_action", json.RawMessage(`{"action":{"action_type":"`+action+`"}}`)); !computer.Is(err, computer.Invalid) {
			t.Fatal(action, err)
		}
	}
	if err = p.Validate("browser_gui_execute_action", json.RawMessage(`{"action":{"action_type":"CLICK","x":1,"y":2}}`)); err != nil {
		t.Fatal(err)
	}
}

func TestActionPolicyChangesFrozenContractWithoutRewritingSchema(t *testing.T) {
	toolsPath := "../../../../packaging/computer/aio/tools.lock.json"
	versionsPath := "../../../../packaging/computer/aio/dependencies.lock.json"
	before, _, err := LoadCatalog(toolsPath, versionsPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(toolsPath)
	if err != nil {
		t.Fatal(err)
	}
	var lock map[string]any
	if err = json.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	lock["gui_action_policy"].(map[string]any)["allowed_action_types"] = []string{"CLICK"}
	raw, _ = json.Marshal(lock)
	path := filepath.Join(t.TempDir(), "tools.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	after, _, err := LoadCatalog(path, versionsPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.Digest == before.Digest {
		t.Fatal("execution policy silently changed under the old contract")
	}
	for i, tool := range before.Tools {
		if computer.Hash(tool.Definition) != computer.Hash(after.Tools[i].Definition) {
			t.Fatal("admission policy rewrote native schema")
		}
	}
}

func TestOwnedScreenInventoryCannotProveAnUncertainStop(t *testing.T) {
	c := catalog(t, "aio", "write", "read")
	e, _, binding, _ := setup(t, c, 2)
	screen := ensure(t, e, binding)
	inventory, err := e.Screens(context.Background(), binding.Owner)
	if err != nil || len(inventory) != 1 || inventory[0].ID != screen.Info().ID {
		t.Fatal("owned inventory lost screen", err)
	}
	other := binding.Owner
	other.OwnerID = "different"
	if _, err = e.Screens(context.Background(), other); !computer.Is(err, computer.Stale) {
		t.Fatal("inventory crossed ownership", err)
	}
	e.writer.MarkUncertain()
	if _, err = e.Screens(context.Background(), binding.Owner); !errors.Is(err, persistence.ErrCommitUnknown) {
		t.Fatal("uncertain state became settlement evidence", err)
	}
}

func TestServiceHandoffLeavesPendingScreenFencedAndOtherScreensUsable(t *testing.T) {
	c := catalog(t, "test", "act", "observe")
	e, d, b, _ := setup(t, c, 2)
	a := ensure(t, e, b)
	second := b
	second.Member.AgentID = "B"
	second.Member.MembershipID = "membership-B"
	other := ensure(t, e, second)
	// The durable unknown belongs to A, and survives service fencing.
	e.mu.Lock()
	r := e.state.Screens[a.Info().ID]
	r.Operations["uncertain"] = computer.Operation{ID: "uncertain", State: "unknown", Scope: computer.Scope{Session: "old", Turn: "1"}}
	r.Status = "blocked"
	if err := e.save(); err != nil {
		e.mu.Unlock()
		t.Fatal(err)
	}
	e.mu.Unlock()
	info, _ := e.Inspect(context.Background())
	lease, err := e.Claim(context.Background(), computer.Claim{Owner: b.Owner, Service: "new-host", ExpectedLease: info.Lease})
	if err != nil {
		t.Fatal("one failed screen blocked owner recovery", err)
	}
	fresh := second
	fresh.Service = "new-host"
	fresh.Lease = lease
	usable := ensure(t, e, fresh)
	if usable.Info().ID != other.Info().ID {
		t.Fatal("healthy screen replaced")
	}
	if _, err = usable.Call(context.Background(), call(c, "act", "B-works")); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Call(context.Background(), call(c, "act", "old-host")); !computer.Is(err, computer.Stale) {
		t.Fatal("old host kept authority", err)
	}
	saved := a.Info()
	saved.Binding.Service = "new-host"
	saved.Binding.Lease = lease
	recovery := e.Restore(saved)
	if _, err = recovery.Call(context.Background(), call(c, "act", "must-not-act")); !computer.Is(err, computer.Unavailable) && !computer.Is(err, computer.Unknown) {
		t.Fatal("unknown screen admitted action", err)
	}
	if err = recovery.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.calls != 1 {
		t.Fatal("recovery replayed an uncertain action", d.calls)
	}
}

func TestMemberAdmissionFencesLostStartAndDelayedCalls(t *testing.T) {
	ctx := context.Background()
	contract := catalog(t, "aio", "write", "read")
	e, d, b, dir := setup(t, contract, 2)
	b.Admission = computer.AdmissionVersion{Connection: 1, Member: 1}
	admission := computer.MemberAdmission{Binding: b, Enabled: true}
	if err := e.SetMemberAdmission(ctx, admission); err != nil {
		t.Fatal(err)
	}
	screen := ensure(t, e, b)
	closed := b
	closed.Admission.Member++
	if err := e.SetMemberAdmission(ctx, computer.MemberAdmission{Binding: closed}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.EnsureScreen(ctx, b); !computer.Is(err, computer.Stale) {
		t.Fatal("late start admitted", err)
	}
	if _, err := screen.Call(ctx, call(contract, "write", "late")); !computer.Is(err, computer.Stale) {
		t.Fatal("late call admitted", err)
	}
	if err := screen.Stop(ctx); err != nil {
		t.Fatal("closed admission must retain stop authority", err)
	}
	next := closed
	next.Admission.Member++
	if err := e.SetMemberAdmission(ctx, computer.MemberAdmission{Binding: next, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	resumed := ensure(t, e, next)
	if resumed.Info().ID != screen.Info().ID || resumed.Info().Epoch <= screen.Info().Epoch {
		t.Fatal("profile was not reused with a new epoch")
	}
	if _, err := e.EnsureScreen(ctx, b); !computer.Is(err, computer.Stale) {
		t.Fatal(err)
	}
	if err := e.SetMemberAdmission(ctx, computer.MemberAdmission{Binding: next, Retired: true}); err != nil {
		t.Fatal(err)
	}
	if err := resumed.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(ctx); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(ctx, dir, 2, contract, d)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(ctx)
	rebound := claim(t, restarted, "service-b")
	rebound.Admission = computer.AdmissionVersion{Connection: 99, Member: 99}
	if err = restarted.SetMemberAdmission(ctx, computer.MemberAdmission{Binding: rebound, Enabled: true}); !computer.Is(err, computer.Stale) {
		t.Fatal("retired membership reopened", err)
	}
	if _, err = restarted.EnsureScreen(ctx, rebound); !computer.Is(err, computer.Stale) {
		t.Fatal("retired membership restarted", err)
	}
	rebound.Member.MembershipID = "replacement"
	if err = restarted.SetMemberAdmission(ctx, computer.MemberAdmission{Binding: rebound, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	_ = ensure(t, restarted, rebound)
}

func TestMemberAdmissionClosesBeforeAnyScreenExists(t *testing.T) {
	ctx := context.Background()
	contract := catalog(t, "aio", "write", "read")
	e, d, b, _ := setup(t, contract, 1)
	b.Admission = computer.AdmissionVersion{Connection: 1, Member: 1}
	if err := e.SetMemberAdmission(ctx, computer.MemberAdmission{Binding: b, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	closeBinding := b
	closeBinding.Admission.Connection++
	if err := e.SetMemberAdmission(ctx, computer.MemberAdmission{Binding: closeBinding}); err != nil {
		t.Fatal(err)
	}
	inventory, err := e.Screens(ctx, b.Owner)
	if err != nil || len(inventory) != 0 {
		t.Fatal(inventory, err)
	}
	if _, err = e.EnsureScreen(ctx, b); !computer.Is(err, computer.Stale) {
		t.Fatal("late creation escaped empty inventory", err)
	}
	if err = e.SetMemberAdmission(ctx, computer.MemberAdmission{Binding: b, Enabled: true}); !computer.Is(err, computer.Stale) {
		t.Fatal("old admission reopened", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.starts != 0 {
		t.Fatal("closed member started a process")
	}
}
