package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/go-aie/teamx/internal/computer"
	"github.com/go-aie/teamx/internal/computer/aio"
	runtime "github.com/go-aie/teamx/internal/computer/aio/runtime"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type nativeComputerDriver struct {
	contract computer.Contract
	calls    atomic.Int32
	stops    atomic.Int32
	entered  chan struct{}
	release  chan struct{}
	result   json.RawMessage
}

func (d *nativeComputerDriver) Validate(string, json.RawMessage) error        { return nil }
func (d *nativeComputerDriver) Start(context.Context, runtime.Resource) error { return nil }
func (d *nativeComputerDriver) Stop(context.Context, runtime.Resource) error {
	d.stops.Add(1)
	return nil
}
func (d *nativeComputerDriver) Tools(context.Context, runtime.Resource) (computer.Contract, error) {
	return d.contract, nil
}
func (d *nativeComputerDriver) Call(ctx context.Context, r runtime.Resource, name string, args json.RawMessage) (json.RawMessage, error) {
	d.calls.Add(1)
	if d.entered != nil {
		d.entered <- struct{}{}
		select {
		case <-d.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if d.result != nil {
		return d.result, nil
	}
	return json.RawMessage(`{"content":[{"type":"text","text":"native-result","vendor_block":{"kept":true}}],"structuredContent":{"value":9007199254740993},"isError":true,"vendor_result":[1,2,3]}`), nil
}

type computerAgentFixture struct {
	h          *roomHarness
	input      *inputSession
	c          *computerController
	x          *computerMembers
	driver     *nativeComputerDriver
	engine     *runtime.Engine
	access     *serverAccess
	token      string
	capability computerCapability
	opts       SessionOptions
}

func newComputerAgentFixture(t *testing.T, provider, tool string) *computerAgentFixture {
	t.Helper()
	h, input := unifiedReaderFixture(t, 1)
	defs := []computer.Tool{{Name: tool, NativeName: tool, Definition: json.RawMessage(`{"name":"` + tool + `","description":"native operation","inputSchema":{"type":"object","properties":{"payload":{"type":"string"}},"additionalProperties":false,"vendor_schema":{"n":9007199254740993}},"outputSchema":{"type":"object","vendor_output":true},"annotations":{"readOnlyHint":true,"vendor_annotation":"kept"},"vendor_definition":{"revision":2}}`)}, {Name: "observe_pixels", NativeName: "observe_pixels", RequiresImages: true, ReadOnly: true, Definition: json.RawMessage(`{"name":"observe_pixels","inputSchema":{"type":"object"}}`)}}
	contract, err := computer.NewContract(provider, map[string]string{"build": "fixed"}, defs)
	if err != nil {
		t.Fatal(err)
	}
	d := &nativeComputerDriver{contract: contract}
	engine, err := runtime.Open(context.Background(), t.TempDir(), 4, contract, d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close(context.Background()) })
	handler, err := runtime.Handler(engine, computerTestKey)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	var count atomic.Int32
	c, settings := connectionController(t, filepath.Join(t.TempDir(), "config.toml"), connectionFactory(&count))
	c.owner.OwnerID = "owner"
	info, err := engine.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ref, err := c.credentials.put(computerTestKey)
	if err != nil {
		t.Fatal(err)
	}
	cfg := computerConfig{Enabled: true, Provider: provider, Endpoint: server.URL, CredentialRef: ref, MaxScreens: 4, Version: 1, InstanceID: info.InstanceID, ContractDigest: contract.Digest}
	c.current.Store(&cfg)
	p, err := aio.New(server.URL, computerTestKey)
	if err != nil {
		t.Fatal(err)
	}
	c.active = p
	c.ready = true
	var saved managedRoomConfig
	saved.RoomIdentity = h.id
	saved.Version = managedConfigVersion
	for _, member := range h.list(t).Members {
		saved.Members = append(saved.Members, managedMember{MemberState: member.MemberState, ComputerEnabled: member.AgentID == "A", ComputerSettingsVersion: 1})
	}
	if err = h.store.save(filepath.Join(h.store.dir, "room-config.json"), saved, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	input.service.settings = settings
	x, err := newComputerMembers(c, nil, input.service)
	if err != nil {
		t.Fatal(err)
	}
	c.members = x
	t.Cleanup(func() { _ = x.host.Close(context.Background()) })
	if _, _, err = c.inspect(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	opts := SessionOptions{MemberIdentity: input.storage.id, StatePath: filepath.Join(h.store.dir, "agents", input.storage.id.MembershipID, "session.json")}
	cap, err := x.prepare(h.ctx, opts, input.execution.Generation, false, false)
	if err != nil {
		t.Fatalf("capability: %v; identity=%+v config=%+v", err, opts.MemberIdentity, saved)
	}
	if err = x.beginTurn(h.ctx, opts, input.execution.Generation, 1, cap); err != nil {
		t.Fatal(err)
	}
	a, token, _ := historyAccess(t, input)
	return &computerAgentFixture{h, input, c, x, d, engine, a, token, cap, opts}
}
func (f *computerAgentFixture) request(t *testing.T, token, call, method, tool string, args any) map[string]json.RawMessage {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if tool != "" {
		body["params"] = map[string]any{"name": tool, "arguments": args}
	}
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "http://localhost/mcp", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Teamx-Call", call)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	w := httptest.NewRecorder()
	f.access.serveMCP(w, r)
	var out map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return out
}
func computerToolNames(t *testing.T, envelope map[string]json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var result struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(envelope["result"], &result); err != nil {
		t.Fatal(err, envelope)
	}
	out := map[string]json.RawMessage{}
	for _, raw := range result.Tools {
		var tool struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(raw, &tool)
		out[tool.Name] = raw
	}
	return out
}
func TestComputerNativeMCPBothCredentialsAndInboxModes(t *testing.T) {
	parallelFixtureTest(t)
	for _, provider := range []string{"aio", "other"} {
		for _, mode := range []string{"http", "cli"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				tool := "vendor_focus"
				if provider == "other" {
					tool = "vendor_scroll"
				}
				f := newComputerAgentFixture(t, provider, tool)
				f.input.service.inboxMode = mode
				names := computerToolNames(t, f.request(t, f.token, "discover", "tools/list", "", nil))
				if names[tool] == nil || names["observe_pixels"] != nil || names["computer_status"] == nil || names["history_read"] == nil || (names["inbox_read"] != nil) != (mode == "http") {
					t.Fatal(names)
				}
				if computer.Hash(json.RawMessage(names[tool])) != computer.Hash(f.driver.contract.Tools[1].Definition) {
					t.Fatal("native definition lost fields", string(names[tool]))
				}
				result := f.request(t, f.token, "one-native-call", "tools/call", tool, map[string]any{"payload": "value"})
				if computer.Hash(json.RawMessage(result["result"])) != computer.Hash(f.driver.resultOrDefault()) {
					t.Fatal("native result transformed", string(result["result"]))
				}
				// The same framework ID returns the original native outcome; no second action.
				_ = f.request(t, f.token, "one-native-call", "tools/call", tool, map[string]any{"payload": "value"})
				if f.driver.calls.Load() != 1 || f.h.list(t).Members[0].ReadThrough != 0 {
					t.Fatal("action replayed or Computer consumed input")
				}
				// Session discovery uses the same facade as the per-turn input credential.
				binding := executionBinding{ServiceInfo: f.input.service.info, Caller: agentCallerIdentity(f.input.storage.id)}
				ref := sessionReference{Version: formatVersion, MemberIdentity: f.input.storage.id, Directory: "session.json.dsh"}
				if err := f.h.store.save(f.opts.StatePath, ref, maxPrivateFileBytes); err != nil {
					t.Fatal(err)
				}
				if err := f.c.writer.Save(computerSnapshotPath(f.opts, ref.Directory), f.capability, maxPrivateFileBytes); err != nil {
					t.Fatal(err)
				}
				if err := f.x.beginTurn(f.h.ctx, f.opts, ref.Directory, 1, f.capability); err != nil {
					t.Fatal(err)
				}
				token, revoke, err := f.access.issueMCP(binding, f.h.store, ref.Directory, &mcpInboxSession{service: f.input.service})
				if err != nil {
					t.Fatal(err)
				}
				defer revoke()
				sessionNames := computerToolNames(t, f.request(t, token, "session-list", "tools/list", "", nil))
				if !reflect.DeepEqual(names, sessionNames) {
					t.Fatal("credential paths differ")
				}
				// A raw session token still needs a trusted running turn and call ID.
				r := f.request(t, token, "session-action", "tools/call", tool, map[string]any{"payload": "another"})
				if !bytes.Contains(r["result"], []byte("vendor_result")) {
					t.Fatal(r)
				}
			})
		}
	}
}
func (d *nativeComputerDriver) resultOrDefault() json.RawMessage {
	if d.result != nil {
		return d.result
	}
	return json.RawMessage(`{"content":[{"type":"text","text":"native-result","vendor_block":{"kept":true}}],"structuredContent":{"value":9007199254740993},"isError":true,"vendor_result":[1,2,3]}`)
}
func TestComputerMemberRevocationFencesCallsAndPreservesProfile(t *testing.T) {
	parallelFixtureTest(t)
	f := newComputerAgentFixture(t, "aio", "native")
	if err := checkComputerSettled(f.opts, f.input.execution.Generation); err == nil {
		t.Fatal("prepared remote turn passed settlement")
	}
	input := memberComputerInput{MemberIdentity: f.input.storage.id, ServiceEpoch: f.c.service.ServiceEpoch, Caller: Caller{Author: Author{Kind: "human", ID: "owner"}}, OperationID: newComputerID(), ExpectedVersion: 1, Enabled: false}
	op, err := f.x.request(input, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if names := computerToolNames(t, f.request(t, f.token, "closed-list", "tools/list", "", nil)); names["native"] != nil {
		t.Fatal("pending disable advertised native tools")
	}
	result := f.request(t, f.token, "closed-call", "tools/call", "native", map[string]any{})
	if !bytes.Contains(result["result"], []byte("computer_disabled")) || f.driver.calls.Load() != 0 {
		t.Fatal(result)
	}
	duplicate, err := f.x.request(input, "owner")
	if err != nil || duplicate.ID != op.ID {
		t.Fatal("lost receipt not idempotent", err)
	}
	bad := input
	bad.Enabled = true
	if _, err = f.x.request(bad, "owner"); !errors.Is(err, ErrConflict) {
		t.Fatal("changed operation accepted", err)
	}
	for range 5 {
		f.x.advance()
	}
	f.x.mu.Lock()
	finished := f.x.changes[op.ID]
	f.x.mu.Unlock()
	if finished.Status != "completed" {
		t.Fatal(finished)
	}
	if err = checkComputerSettled(f.opts, f.input.execution.Generation); err != nil {
		t.Fatal(err)
	}
	e := f.x.entry(input.MemberIdentity)
	e.mu.Lock()
	screenID := e.resource.Screen.ID
	e.mu.Unlock()
	// Re-enable the same full identity; a new epoch reuses the profile/screen ID.
	input.OperationID = newComputerID()
	input.ExpectedVersion = 2
	input.Enabled = true
	if _, err = f.x.request(input, "owner"); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		f.x.advance()
	}
	e.mu.Lock()
	sameID := e.resource.Screen.ID
	epoch := e.resource.Screen.Epoch
	e.mu.Unlock()
	if sameID != screenID || epoch < 2 {
		t.Fatal("profile identity changed", screenID, sameID, epoch)
	}
	// Disabled B cannot discover or call A's tools using its own input credential.
	id := f.h.memberID("B", "generation-B")
	opts := SessionOptions{MemberIdentity: id, StatePath: filepath.Join(f.h.store.dir, "agents", id.MembershipID, "session.json")}
	cap, err := f.x.prepare(f.h.ctx, opts, "another-generation", false, false)
	if err != nil || cap.Enabled {
		t.Fatal(cap, err)
	}
}
func TestComputerTurnFinishWaitsForRemoteAndClosedScopesStayClosed(t *testing.T) {
	parallelFixtureTest(t)
	f := newComputerAgentFixture(t, "aio", "native")
	f.driver.entered = make(chan struct{}, 1)
	f.driver.release = make(chan struct{})
	callDone := make(chan struct{})
	go func() {
		defer close(callDone)
		_, _ = f.x.call(context.Background(), f.opts.MemberIdentity, computer.Scope{Session: f.input.execution.Generation, Turn: "1"}, f.capability, "trusted", "native", json.RawMessage(`{}`))
	}()
	<-f.driver.entered
	wait, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	err := f.x.settleScope(wait, f.opts.MemberIdentity, computer.Scope{Session: f.input.execution.Generation, Turn: "1"}, false)
	if err == nil {
		t.Fatal("remote activity treated as settled")
	}
	if err = checkComputerSettled(f.opts, f.input.execution.Generation); err == nil {
		t.Fatal("failed cleanup cleared fence")
	}
	close(f.driver.release)
	<-callDone
	if err = f.x.finish(f.opts.MemberIdentity, f.input.execution.Generation, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = f.x.call(context.Background(), f.opts.MemberIdentity, computer.Scope{Session: f.input.execution.Generation, Turn: "1"}, f.capability, "late", "native", json.RawMessage(`{}`)); !computer.Is(err, computer.Stale) {
		t.Fatal("retired turn admitted", err)
	}
}
func TestComputerUncertainTransportReconcilesOriginalOperation(t *testing.T) {
	parallelFixtureTest(t)
	f := newComputerAgentFixture(t, "aio", "native")
	// Lose only the response after the Runtime accepted and committed the action.
	proxy := &computerLostResponse{Screen: f.x.entry(f.opts.MemberIdentity).screen}
	e := f.x.entry(f.opts.MemberIdentity)
	e.mu.Lock()
	e.screen = proxy
	e.mu.Unlock()
	scope := computer.Scope{Session: f.input.execution.Generation, Turn: "1"}
	_, err := f.x.call(context.Background(), f.opts.MemberIdentity, scope, f.capability, "lost", "native", json.RawMessage(`{}`))
	if err == nil || f.driver.calls.Load() != 1 || len(f.x.host.Pending()) != 1 {
		t.Fatal("missing uncertain association", err)
	}
	if err = f.x.finish(f.opts.MemberIdentity, f.input.execution.Generation, 1); err != nil {
		t.Fatal(err)
	}
	if f.driver.calls.Load() != 1 || len(f.x.host.Pending()) != 0 {
		t.Fatal("recovery replayed action or retained settled pending")
	}
}

type computerLostResponse struct {
	computer.Screen
	once sync.Once
}

func (s *computerLostResponse) Call(ctx context.Context, c computer.Call) (computer.Outcome, error) {
	out, err := s.Screen.Call(ctx, c)
	s.once.Do(func() { err = errors.New("response lost") })
	return out, err
}
func TestComputerCapabilityFreezeAndNoProviderIOWhenDisabled(t *testing.T) {
	parallelFixtureTest(t)
	f := newComputerAgentFixture(t, "aio", "native")
	if err := f.x.finish(f.opts.MemberIdentity, f.input.execution.Generation, 1); err != nil {
		t.Fatal(err)
	}
	saved := f.capability
	target := *f.c.current.Load()
	target.ContractDigest = "changed"
	f.c.current.Store(&target)
	if _, err := f.x.prepare(f.h.ctx, f.opts, f.input.execution.Generation, false, true); !computer.Is(err, computer.ContractMismatch) {
		t.Fatal("resumed changed contract", err)
	}
	target.ContractDigest = saved.ContractDigest
	target.Enabled = false
	f.c.current.Store(&target)
	f.c.connect = func(string, string, string) (computer.ManagedProvider, error) {
		t.Fatal("disabled capability constructed provider")
		return nil, nil
	}
	id := f.h.memberID("B", "generation-B")
	opts := SessionOptions{MemberIdentity: id, StatePath: filepath.Join(f.h.store.dir, "agents", id.MembershipID, "session.json")}
	cap, err := f.x.prepare(f.h.ctx, opts, "session.json.dsh", false, false)
	if err != nil || cap.Enabled {
		t.Fatal(cap, err)
	}
	if err = f.x.beginTurn(f.h.ctx, opts, "session.json.dsh", 1, cap); err != nil {
		t.Fatal(err)
	}
	if err = f.x.finish(id, "session.json.dsh", 1); err != nil {
		t.Fatal(err)
	}
	if err = f.x.settleScope(context.Background(), id, computer.Scope{}, true); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"credential", "lease", "epoch"} {
		b, _ := json.Marshal(saved)
		if strings.Contains(string(b), field) {
			t.Fatal("ephemeral identity in capability", field)
		}
	}
}
func TestComputerConcurrentScreensUseIndependentAdmission(t *testing.T) {
	parallelFixtureTest(t)
	f := newComputerAgentFixture(t, "aio", "native")
	id := f.h.memberID("B", "generation-B")
	var config managedRoomConfig
	path := filepath.Join(f.h.store.dir, "room-config.json")
	if err := readSnapshot(path, &config, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	config.Members[1].ComputerEnabled = true
	if err := f.h.store.save(path, config, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	if err := f.h.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, InputStateRef: "input-state.json", TurnNumber: 1, Turn: &TurnRecord{Number: 1, Status: "running"}}); err != nil {
		t.Fatal(err)
	}
	opts := SessionOptions{MemberIdentity: id, StatePath: filepath.Join(f.h.store.dir, "agents", id.MembershipID, "session.json")}
	cap, err := f.x.prepare(f.h.ctx, opts, "session.json.dsh", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.x.beginTurn(f.h.ctx, opts, "session.json.dsh", 1, cap); err != nil {
		t.Fatal(err)
	}
	f.driver.entered = make(chan struct{}, 2)
	f.driver.release = make(chan struct{})
	done := make(chan struct{}, 2)
	for i, member := range []MemberIdentity{f.opts.MemberIdentity, id} {
		go func(i int, id MemberIdentity) {
			defer func() { done <- struct{}{} }()
			generation := f.input.execution.Generation
			c := f.capability
			if i == 1 {
				generation = "session.json.dsh"
				c = cap
			}
			_, _ = f.x.call(context.Background(), id, computer.Scope{Session: generation, Turn: "1"}, c, strconv.Itoa(i), "native", json.RawMessage(`{}`))
		}(i, member)
	}
	for range 2 {
		select {
		case <-f.driver.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("screen B waited for A")
		}
	}
	close(f.driver.release)
	<-done
	<-done
	eA, eB := f.x.entry(f.opts.MemberIdentity), f.x.entry(id)
	if eA.resource.Screen.ID == eB.resource.Screen.ID {
		t.Fatal("shared member screen")
	}
}

func TestComputerHostRestartReconcilesPendingWithoutReplayingOrStartingScreen(t *testing.T) {
	parallelFixtureTest(t)
	f := newComputerAgentFixture(t, "aio", "native")
	e := f.x.entry(f.opts.MemberIdentity)
	e.mu.Lock()
	oldInfo := e.resource.Screen
	e.screen = &computerLostResponse{Screen: e.screen}
	e.mu.Unlock()
	if _, err := f.x.call(context.Background(), f.opts.MemberIdentity, computer.Scope{Session: f.input.execution.Generation, Turn: "1"}, f.capability, "crash-before-reply", "native", json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected transport failure")
	}
	if err := f.x.host.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A different service generation reads only the host associations and Runtime
	// facts. It must not ask EnsureScreen to recreate the old desktop for cleanup.
	f.c.service.ServiceEpoch = newComputerID()
	recovered, err := newComputerMembers(f.c, nil, f.input.service)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.host.Close(context.Background())
	f.c.members = recovered
	recovered.recover(context.Background())
	if err := checkComputerSettled(f.opts, f.input.execution.Generation); err != nil {
		t.Fatal("restart did not settle original operation", err)
	}
	if f.driver.calls.Load() != 1 || len(recovered.host.Pending()) != 0 {
		t.Fatal("restart replayed action")
	}
	fresh := recovered.entry(f.opts.MemberIdentity)
	fresh.mu.Lock()
	info := fresh.resource.Screen
	fresh.mu.Unlock()
	if info.ID != oldInfo.ID || info.Epoch != oldInfo.Epoch {
		t.Fatal("reconciliation recreated a screen", oldInfo, info)
	}
	if _, err = f.c.active.Restore(oldInfo).Call(context.Background(), computer.Call{ID: newComputerID(), Scope: computer.Scope{Session: "stale", Turn: "1"}, Tool: "native", Arguments: json.RawMessage(`{}`), ContractDigest: f.capability.ContractDigest}); !computer.Is(err, computer.Stale) {
		t.Fatal("old service retained execution authority", err)
	}
}

func managedComputerFixture(t *testing.T) (*roomManager, *computerController, *computerMembers, string) {
	t.Helper()
	m, _, startup := onlineModelManager(t)
	c, err := newComputerController(context.Background(), startup, m.settings, ServiceInfo{ServiceEpoch: m.epoch}, "managed-installation", humanOperator(), func(MemberIdentity) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	c.connect = func(_ string, endpoint, key string) (computer.ManagedProvider, error) { return aio.New(endpoint, key) }
	m.settings.computer = c
	x, err := newComputerMembers(c, m, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.members = x
	t.Cleanup(func() {
		_ = m.close()
		_ = x.host.Close(context.Background())
		c.cancel()
		_ = c.writer.Close(context.Background())
		_ = unlockDirectory(c.lock)
	})
	_, _, endpoint := connectionRuntime(t)
	input := connectionInput(c, endpoint)
	input.Caller.ID = humanOperator()
	op := applyConnection(t, c, input)
	if op.Status != "completed" {
		t.Fatal(op)
	}
	return m, c, x, endpoint
}
func applyMemberComputer(t *testing.T, x *computerMembers, id MemberIdentity, enabled bool) memberComputerChange {
	t.Helper()
	member, err := x.preference(id, x.memberDirectory(id))
	if err != nil {
		t.Fatal(err)
	}
	input := memberComputerInput{MemberIdentity: id, Caller: Caller{Author: Author{Kind: "human", ID: x.connection.owner.OwnerID}}, ServiceEpoch: x.connection.service.ServiceEpoch, ExpectedVersion: member.ComputerSettingsVersion, OperationID: newComputerID(), Enabled: enabled}
	op, err := x.request(input, x.connection.owner.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	if os.Getenv("TEAMX_COMPUTER_AGENT_REAL") == "1" {
		deadline = time.Now().Add(2 * time.Minute)
	}
	for time.Now().Before(deadline) {
		x.advance()
		x.mu.Lock()
		saved := x.changes[op.ID]
		x.mu.Unlock()
		if saved.Status == "completed" {
			return saved
		}
		time.Sleep(15 * time.Millisecond)
	}
	x.mu.Lock()
	saved := x.changes[op.ID]
	x.mu.Unlock()
	t.Fatal("member change incomplete", saved)
	return saved
}
func TestComputerManagedActiveAndClosedSessionCapabilityRotation(t *testing.T) {
	parallelFixtureTest(t)
	m, c, x, _ := managedComputerFixture(t)
	room := createDirectConversation(t, m, directSelection("computer-member"))
	id := modelMember(t, m, room)
	applyMemberComputer(t, x, id, true)
	_ = openOK(t, m, room.RoomIdentity, "computer-open")
	svc, _ := m.runtimeServiceLocked(id.RoomIdentity)
	managedPost(t, m, svc, "computer-first")
	waitMember(t, context.Background(), svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber >= 1 })
	opts := SessionOptions{MemberIdentity: id, StatePath: filepath.Join(svc.store.dir, "agents", id.MembershipID, "session.json")}
	ref, _, err := readSessionReference(opts.StatePath, id)
	if err != nil {
		t.Fatal(err)
	}
	cap, err := loadComputerCapability(opts, ref.Directory)
	if err != nil || !cap.Enabled {
		t.Fatal("Session omitted Computer capability", cap, err)
	}
	before := ref.Directory
	profile := x.entry(id).resource.Screen.ID
	applyMemberComputer(t, x, id, false)
	ref, _, err = readSessionReference(opts.StatePath, id)
	if err != nil || ref.Directory == before || !ref.Pending {
		t.Fatal("active Session was not rotated", ref, err)
	}
	status := inspectMember(t, context.Background(), svc.target(id.MembershipID), id)
	if status.Paused {
		t.Fatal("capability rotation changed collaboration intent")
	}
	member, _ := x.preference(id, m.roomDir(id.RoomID))
	if member.ComputerEnabled || member.ComputerSettingsVersion != 2 {
		t.Fatal(member)
	}
	// Leave the Room closed, then re-enable: no actor, DSH or turn is started.
	if err := svc.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.setRuntimeServiceLocked(room.RoomIdentity, nil)
	m.mu.Unlock()
	applyMemberComputer(t, x, id, true)
	if active, _ := m.runtimeServiceLocked(id.RoomIdentity); active != nil {
		t.Fatal("closed capability application opened Room")
	}
	if x.entry(id).resource.Screen.ID != profile {
		t.Fatal("member profile changed")
	}
	// Global disable keeps the member preference and completes its Session rotation.
	input := connectionInput(c, c.current.Load().Endpoint)
	input.Enabled = false
	input.CredentialAction = "keep"
	input.Credential = ""
	if op := applyConnection(t, c, input); op.Status != "completed" {
		t.Fatal("global disable incomplete", op)
	}
	member, _ = x.preference(id, m.roomDir(id.RoomID))
	if !member.ComputerEnabled {
		t.Fatal("global disable erased member preference")
	}
}

func TestComputerPendingOwnedCloseCanBeVerifiedOnSameResource(t *testing.T) {
	parallelFixtureTest(t)
	var calls atomic.Int32
	resource := &ownedSession{Session: &computerClosingSession{close: func(context.Context) error {
		if calls.Add(1) == 1 {
			return &pendingBackendClose{computer.Fail(computer.Unknown, "remote pending")}
		}
		return nil
	}}}
	if err := resource.close(); backendCleanupSettled(err) {
		t.Fatal("pending close presented settlement")
	}
	if err := resource.close(); err != nil {
		t.Fatal("same resource could not retry verification", err)
	}
	if err := resource.close(); err != nil || calls.Load() != 2 {
		t.Fatal("settled resource closed repeatedly", err, calls.Load())
	}
}

type computerClosingSession struct{ close func(context.Context) error }

func (s *computerClosingSession) Run(context.Context, UnreadChanged) error { return nil }
func (s *computerClosingSession) Close(ctx context.Context) error          { return s.close(ctx) }

type computerNeverSent struct{ computer.Screen }

func (s *computerNeverSent) Call(context.Context, computer.Call) (computer.Outcome, error) {
	return computer.Outcome{}, errors.New("connection lost before acceptance")
}
func TestComputerLostBeforeAcceptanceRequiresStopAndRetainsUnknownHint(t *testing.T) {
	parallelFixtureTest(t)
	f := newComputerAgentFixture(t, "aio", "native")
	e := f.x.entry(f.opts.MemberIdentity)
	e.mu.Lock()
	e.screen = &computerNeverSent{Screen: e.screen}
	e.mu.Unlock()
	if _, err := f.x.call(context.Background(), f.opts.MemberIdentity, computer.Scope{Session: f.input.execution.Generation, Turn: "1"}, f.capability, "not-sent", "native", json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected transport error")
	}
	if err := f.x.finish(f.opts.MemberIdentity, f.input.execution.Generation, 1); err != nil {
		t.Fatal(err)
	}
	if f.driver.calls.Load() != 0 || f.driver.stops.Load() == 0 || len(f.x.host.Pending()) != 0 {
		t.Fatal("unknown request was replayed or not stopped")
	}
	e.mu.Lock()
	unknown := e.resource.Unknown
	e.mu.Unlock()
	if !unknown {
		t.Fatal("lost the website-outcome warning")
	}
	if _, err := f.x.screen(context.Background(), f.opts.MemberIdentity, filepath.Dir(f.opts.StatePath)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.x.turnNotice(f.opts.MemberIdentity), "unknown website outcome") {
		t.Fatal("screen rebuild erased unknown action warning")
	}
}
func TestComputerLocalAdmissionRefusalNeverLeavesRemotePending(t *testing.T) {
	parallelFixtureTest(t)
	f := newComputerAgentFixture(t, "aio", "native")
	input := connectionInput(f.c, f.c.current.Load().Endpoint)
	input.Enabled = false
	input.CredentialAction = "keep"
	input.Credential = ""
	if _, err := f.c.save(input); err != nil {
		t.Fatal(err)
	}
	if _, err := f.x.call(context.Background(), f.opts.MemberIdentity, computer.Scope{Session: f.input.execution.Generation, Turn: "1"}, f.capability, "rejected", "native", json.RawMessage(`{}`)); !computer.Is(err, computer.Unavailable) {
		t.Fatal(err)
	}
	if len(f.x.host.Pending()) != 0 || f.driver.calls.Load() != 0 {
		t.Fatal("local rejection invented a remote operation")
	}
}

func TestComputerMemberSettlementPreservesScreenForNextSession(t *testing.T) {
	parallelFixtureTest(t)
	f := newComputerAgentFixture(t, "aio", "native")
	e := f.x.entry(f.opts.MemberIdentity)
	original := e.resource.Screen
	if err := f.x.settleScope(context.Background(), f.opts.MemberIdentity, computer.Scope{}, false); err != nil {
		t.Fatal(err)
	}
	generation := sessionGenerationForOperation(newComputerID())
	if err := f.x.beginTurn(context.Background(), f.opts, generation, 2, f.capability); err != nil {
		t.Fatal(err)
	}
	if _, err := f.x.call(context.Background(), f.opts.MemberIdentity, computer.Scope{Session: generation, Turn: "2"}, f.capability, "new-session", "native", json.RawMessage(`{}`)); err != nil {
		t.Fatal("member drain fenced future Session", err)
	}
	e.mu.Lock()
	current := e.resource.Screen
	e.mu.Unlock()
	if current.ID != original.ID || current.Epoch != original.Epoch {
		t.Fatal("ordinary capability settlement rebuilt screen")
	}
}
func TestComputerRemovedMemberOperationStopsOriginalScreenWithoutAdoptingReplacement(t *testing.T) {
	parallelFixtureTest(t)
	f := newComputerAgentFixture(t, "aio", "native")
	input := memberComputerInput{MemberIdentity: f.opts.MemberIdentity, Caller: Caller{Author: Author{Kind: "human", ID: "owner"}}, ServiceEpoch: f.c.service.ServiceEpoch, ExpectedVersion: 1, OperationID: newComputerID(), Enabled: false}
	op, err := f.x.request(input, "owner")
	if err != nil {
		t.Fatal(err)
	}
	roomCall[MemberInfo](t, f.h, RemoveMember{MemberIdentity: f.opts.MemberIdentity})
	for range 4 {
		f.x.advance()
	}
	f.x.mu.Lock()
	finished := f.x.changes[op.ID]
	f.x.mu.Unlock()
	if finished.Status != "completed" {
		t.Fatal(finished)
	}
	e := f.x.entry(f.opts.MemberIdentity)
	e.mu.Lock()
	retired := e.resource.Retired
	e.mu.Unlock()
	if !retired || f.driver.stops.Load() == 0 {
		t.Fatal("removed member kept its Computer")
	}
	if _, err = f.x.screen(context.Background(), f.opts.MemberIdentity, filepath.Dir(f.opts.StatePath)); !computer.Is(err, computer.Stale) {
		t.Fatal("removed identity recreated screen", err)
	}
}

func TestComputerSameInstanceUpdateRefreshesTransportWithoutSessionCapabilityChange(t *testing.T) {
	parallelFixtureTest(t)
	m, c, x, endpoint := managedComputerFixture(t)
	room := createDirectConversation(t, m, directSelection("computer-transport"))
	id := modelMember(t, m, room)
	applyMemberComputer(t, x, id, true)
	entry := x.entry(id)
	entry.mu.Lock()
	old := entry.screen
	original := entry.resource.Screen
	entry.mu.Unlock()
	before, err := x.desired(id, m.roomDir(id.RoomID), false)
	if err != nil {
		t.Fatal(err)
	}
	update := connectionInput(c, endpoint)
	update.CredentialAction = "keep"
	update.Credential = ""
	if op := applyConnection(t, c, update); op.Status != "completed" {
		t.Fatal(op)
	}
	after, err := x.desired(id, m.roomDir(id.RoomID), false)
	if err != nil || before != after {
		t.Fatal("transport changed Session capability", err)
	}
	if _, err = old.Call(context.Background(), computer.Call{}); !computer.Is(err, computer.Unavailable) {
		t.Fatal("old connection admitted new action", err)
	}
	fresh, err := x.screen(context.Background(), id, filepath.Join(m.roomDir(id.RoomID), "agents", id.MembershipID))
	if err != nil {
		t.Fatal(err)
	}
	fresh.mu.Lock()
	current := fresh.resource.Screen
	fresh.mu.Unlock()
	if current.ID != original.ID || current.Epoch != original.Epoch {
		t.Fatal("transport update rebuilt screen")
	}
	if x.enabledCount() != 1 {
		t.Fatal("settings lost enabled-member count")
	}
}

func TestComputerPendingRecoveryDoesNotEraseLocalResumeEligibility(t *testing.T) {
	parallelFixtureTest(t)
	m, _, x, _ := managedComputerFixture(t)
	room := createDirectConversation(t, m, directSelection("computer-recovery-gate"))
	id := modelMember(t, m, room)
	applyMemberComputer(t, x, id, true)
	_ = openOK(t, m, room.RoomIdentity, "computer-recovery-open")
	svc, _ := m.runtimeServiceLocked(id.RoomIdentity)
	managedPost(t, m, svc, "normal-local-turn")
	waitMember(t, context.Background(), svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber >= 1 })
	if err := svc.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	opts := SessionOptions{MemberIdentity: id, StatePath: filepath.Join(svc.store.dir, "agents", id.MembershipID, "session.json")}
	ref, _, err := readSessionReference(opts.StatePath, id)
	if err != nil {
		t.Fatal(err)
	}
	// A retained external scope is not local DSH corruption. Simulate its host
	// association after the independently verified local normal close.
	entry := x.entry(id)
	entry.mu.Lock()
	entry.scopes = []computer.Scope{{Session: ref.Directory, Turn: "1"}}
	entry.blocked = true
	if err = x.saveFence(entry); err != nil {
		entry.mu.Unlock()
		t.Fatal(err)
	}
	entry.mu.Unlock()
	store, err := openStoreWithCatalog(svc.store.dir, id.RoomID, nil, m.platformCatalog)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot, err := store.LoadAgent(id)
	if err != nil {
		t.Fatal(err)
	}
	svc.store = store
	recovered, err := svc.inspectBackendRecovery(opts, snapshotRunner(t, store, id), snapshot, true)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered.resume || recovered.blocked != "" {
		t.Fatal("remote gate erased resumable local evidence", recovered)
	}
	if svc.computerReady(id) {
		t.Fatal("pending member was allowed to execute")
	}
	if err = storedSessionBackend(store).checkReset(opts, ref.Directory); err == nil {
		t.Fatal("closed lifecycle ignored external pending")
	}
	if err = x.settleScope(context.Background(), id, computer.Scope{}, false); err != nil {
		t.Fatal(err)
	}
	if !svc.computerReady(id) {
		t.Fatal("settled member never reopened its dynamic gate")
	}
}
func snapshotRunner(t *testing.T, store *Store, id MemberIdentity) RunnerConfig {
	t.Helper()
	room, err := store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range room.Members {
		if m.AgentID == id.AgentID && m.MembershipID == id.MembershipID {
			return m.Runner
		}
	}
	t.Fatal("member missing")
	return RunnerConfig{}
}

func TestComputerStandaloneMemberPreferencePersistsWithoutProvider(t *testing.T) {
	parallelFixtureTest(t)
	h, input := unifiedReaderFixture(t, 0)
	c, settings := connectionController(t, filepath.Join(t.TempDir(), "config.toml"), func(string, string, string) (computer.ManagedProvider, error) {
		t.Fatal("disabled standalone preference performed Provider IO")
		return nil, nil
	})
	input.service.settings = settings
	x, err := newComputerMembers(c, nil, input.service)
	if err != nil {
		t.Fatal(err)
	}
	c.members = x
	defer func() { _ = x.host.Close(context.Background()) }()
	id := input.storage.id
	op, err := x.request(memberComputerInput{MemberIdentity: id, Caller: Caller{Author: Author{Kind: "human", ID: "owner"}}, ServiceEpoch: c.service.ServiceEpoch, ExpectedVersion: 0, OperationID: newComputerID(), Enabled: true}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		x.advance()
	}
	x.mu.Lock()
	finished := x.changes[op.ID]
	x.mu.Unlock()
	if finished.Status != "completed" {
		t.Fatal(finished)
	}
	member, err := x.preference(id, h.store.dir)
	if err != nil || !member.ComputerEnabled || member.ComputerSettingsVersion != 1 {
		t.Fatal(member, err)
	}
	if x.enabledCount() != 1 {
		t.Fatal("standalone enabled count lost preference")
	}
	if _, err = os.Stat(computerPreferencesPath(h.store.dir, id)); err != nil {
		t.Fatal(err)
	}
	if err = x.host.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted, err := newComputerMembers(c, nil, input.service)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.host.Close(context.Background())
	saved, err := restarted.preference(id, h.store.dir)
	if err != nil || !saved.ComputerEnabled || saved.ComputerSettingsVersion != 1 {
		t.Fatal("standalone preference lost on reopen", saved, err)
	}
}

func TestComputerCapacityWaitAllowsGlobalIncreaseAndDisable(t *testing.T) {
	parallelFixtureTest(t)
	m, c, x, endpoint := managedComputerFixture(t)
	var ids []MemberIdentity
	for i := 0; i < 4; i++ {
		room := createDirectConversation(t, m, directSelection("capacity-"+strconv.Itoa(i)))
		ids = append(ids, modelMember(t, m, room))
	}
	for _, id := range ids[:2] {
		applyMemberComputer(t, x, id, true)
	}
	begin := func(id MemberIdentity) memberComputerChange {
		t.Helper()
		op, err := x.request(memberComputerInput{MemberIdentity: id, Caller: Caller{Author: Author{Kind: "human", ID: c.owner.OwnerID}}, ServiceEpoch: c.service.ServiceEpoch, OperationID: newComputerID(), Enabled: true}, c.owner.OwnerID)
		if err != nil {
			t.Fatal(err)
		}
		for range 3 {
			x.advance()
		}
		return op
	}
	pending := begin(ids[2])
	x.mu.Lock()
	state := x.changes[pending.ID]
	x.mu.Unlock()
	if state.Status != "waiting" {
		t.Fatal("capacity did not block enable", state)
	}
	increase := connectionInput(c, endpoint)
	increase.CredentialAction = "keep"
	increase.Credential = ""
	increase.MaxScreens = 3
	if op := applyConnection(t, c, increase); op.Status != "completed" {
		t.Fatal("waiting member prevented capacity increase", op)
	}
	for range 4 {
		x.advance()
	}
	x.mu.Lock()
	state = x.changes[pending.ID]
	x.mu.Unlock()
	if state.Status != "completed" {
		t.Fatal("original enable never resumed", state)
	}
	pending = begin(ids[3])
	disable := connectionInput(c, endpoint)
	disable.CredentialAction = "keep"
	disable.Credential = ""
	disable.Enabled = false
	if op := applyConnection(t, c, disable); op.Status != "completed" {
		t.Fatal("waiting member prevented global disable", op)
	}
	for range 5 {
		x.advance()
	}
	x.mu.Lock()
	state = x.changes[pending.ID]
	x.mu.Unlock()
	if state.Status != "completed" {
		t.Fatal("superseded member operation never settled", state)
	}
	for _, id := range ids {
		member, err := x.preference(id, m.roomDir(id.RoomID))
		if err != nil || !member.ComputerEnabled {
			t.Fatal("disable discarded pending preference", err)
		}
	}
	if c.current.Load().Enabled {
		t.Fatal("global disable lost its intent")
	}
}

func TestComputerRotatedCapabilityMetadataDoesNotBlockRoomDeletion(t *testing.T) {
	parallelFixtureTest(t)
	m, _, x, _ := managedComputerFixture(t)
	room := createDirectConversation(t, m, directSelection("computer-delete-rotated"))
	id := modelMember(t, m, room)
	applyMemberComputer(t, x, id, true)
	_ = openOK(t, m, room.RoomIdentity, "computer-delete-open")
	svc, _ := m.runtimeServiceLocked(id.RoomIdentity)
	managedPost(t, m, svc, "before-capability-rotation")
	waitMember(t, context.Background(), svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber >= 1 })
	applyMemberComputer(t, x, id, false)
	managedPost(t, m, svc, "after-capability-rotation")
	waitMember(t, context.Background(), svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber >= 2 })
	opts := SessionOptions{MemberIdentity: id, StatePath: filepath.Join(svc.store.dir, "agents", id.MembershipID, "session.json")}
	ref, _, err := readSessionReference(opts.StatePath, id)
	if err != nil || ref.Directory == initialSessionGeneration(opts) {
		t.Fatal("rotated Session not opened", err)
	}
	if _, err = os.Stat(computerSnapshotPath(opts, ref.Directory)); err != nil {
		t.Fatal(err)
	}
	beginDeletion(t, m, room)
	if pending := awaitDeletion(t, m, room); pending != nil {
		t.Fatal("capability metadata blocked settled deletion", pending.Error)
	}
	if _, err = os.Stat(m.roomDir(id.RoomID)); !os.IsNotExist(err) {
		t.Fatal("Room data was not removed", err)
	}
}

func TestComputerDeletionFindsAcceptedScreenWithLostEnsureResponse(t *testing.T) {
	parallelFixtureTest(t)
	m, c, x, _ := managedComputerFixture(t)
	room := createDirectConversation(t, m, directSelection("computer-orphan-start"))
	id := modelMember(t, m, room)
	op, err := x.request(memberComputerInput{MemberIdentity: id, Caller: Caller{Author: Author{Kind: "human", ID: c.owner.OwnerID}}, ServiceEpoch: c.service.ServiceEpoch, OperationID: newComputerID(), Enabled: true}, c.owner.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	x.advance() // durable preference; the host has not acquired a Screen handle
	provider := c.active
	info, err := provider.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := provider.Claim(context.Background(), computer.Claim{Owner: c.owner, Service: c.service.ServiceEpoch, ExpectedLease: info.Lease})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := c.ensureScreen(context.Background(), computer.Binding{Owner: c.owner, Member: id, Service: c.service.ServiceEpoch, Lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	if x.entry(id) != nil {
		t.Fatal("fixture unexpectedly has a host handle")
	}
	beginDeletion(t, m, room)
	if pending := awaitDeletion(t, m, room); pending != nil {
		t.Fatal("orphan screen blocked deletion", pending.Error)
	}
	inventory, err := provider.Screens(context.Background(), c.owner)
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	for _, screen := range inventory {
		if screen.ID == accepted.Info().ID {
			stopped = screen.Status == "stopped"
		}
	}
	if !stopped {
		t.Fatal("lost Ensure response leaked the member desktop")
	}
	if _, err = provider.EnsureScreen(context.Background(), accepted.Info().Binding); !computer.Is(err, computer.Stale) {
		t.Fatal("late Ensure escaped deletion", err)
	}
	for range 3 {
		x.advance()
	}
	x.mu.Lock()
	finished := x.changes[op.ID]
	x.mu.Unlock()
	if finished.Status != "completed" {
		t.Fatal(finished)
	}
}

func TestComputerDisableAfterReplacementNeverRevisitsSettledOldInstance(t *testing.T) {
	parallelFixtureTest(t)
	m, c, x, oldEndpoint := managedComputerFixture(t)
	room := createDirectConversation(t, m, directSelection("computer-old-offline"))
	id := modelMember(t, m, room)
	applyMemberComputer(t, x, id, true)
	applyMemberComputer(t, x, id, false)
	_, _, endpoint := connectionRuntime(t)
	replacement := connectionInput(c, endpoint)
	if op := applyConnection(t, c, replacement); op.Status != "completed" {
		t.Fatal(op)
	}
	original := c.connect
	c.connect = func(name, url, key string) (computer.ManagedProvider, error) {
		if url == oldEndpoint {
			t.Error("settled old instance was contacted")
			return nil, computer.Fail(computer.Unavailable, "old instance offline")
		}
		return original(name, url, key)
	}
	disable := connectionInput(c, endpoint)
	disable.Enabled = false
	disable.CredentialAction = "keep"
	disable.Credential = ""
	if op := applyConnection(t, c, disable); op.Status != "completed" {
		t.Fatal(op)
	}
}

func TestComputerReplacementFindsLostStartBehindOldStoppedHandle(t *testing.T) {
	parallelFixtureTest(t)
	m, c, x, _ := managedComputerFixture(t)
	room := createDirectConversation(t, m, directSelection("computer-new-orphan"))
	id := modelMember(t, m, room)
	applyMemberComputer(t, x, id, true)
	applyMemberComputer(t, x, id, false)
	_, _, endpoint := connectionRuntime(t)
	if op := applyConnection(t, c, connectionInput(c, endpoint)); op.Status != "completed" {
		t.Fatal(op)
	}
	preference, _ := x.preference(id, x.memberDirectory(id))
	_, err := x.request(memberComputerInput{MemberIdentity: id, Caller: Caller{Author: Author{Kind: "human", ID: c.owner.OwnerID}}, ServiceEpoch: c.service.ServiceEpoch, ExpectedVersion: preference.ComputerSettingsVersion, OperationID: newComputerID(), Enabled: true}, c.owner.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	x.advance() // persist preference; model the subsequent lost Ensure response
	provider := c.active
	info, err := provider.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := provider.Claim(context.Background(), computer.Claim{Owner: c.owner, Service: c.service.ServiceEpoch, ExpectedLease: info.Lease})
	if err != nil {
		t.Fatal(err)
	}
	preference, _ = x.preference(id, x.memberDirectory(id))
	binding := computer.Binding{Owner: c.owner, Member: id, Service: c.service.ServiceEpoch, Lease: lease, Admission: computer.AdmissionVersion{Connection: c.current.Load().Version, Member: preference.ComputerSettingsVersion}}
	if err = provider.SetMemberAdmission(context.Background(), computer.MemberAdmission{Binding: binding, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	orphan, err := provider.EnsureScreen(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	disable := connectionInput(c, endpoint)
	disable.Enabled = false
	disable.CredentialAction = "keep"
	disable.Credential = ""
	if op := applyConnection(t, c, disable); op.Status != "completed" {
		t.Fatal(op)
	}
	inventory, err := provider.Screens(context.Background(), c.owner)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, screen := range inventory {
		if screen.ID == orphan.Info().ID {
			found = screen.Status == "stopped"
		}
	}
	if !found {
		t.Fatal("new instance's untracked screen was not stopped")
	}
	if _, err = provider.EnsureScreen(context.Background(), binding); !computer.Is(err, computer.Stale) {
		t.Fatal("late start escaped replacement disable", err)
	}
}
