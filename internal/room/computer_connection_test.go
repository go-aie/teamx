package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/computer"
	"github.com/go-aie/teamx/internal/computer/aio"
	runtime "github.com/go-aie/teamx/internal/computer/aio/runtime"
)

const computerTestKey = "computer-test-credential-0123456789abcdef"

type connectionDriver struct {
	contract  computer.Contract
	calls     atomic.Int32
	stops     atomic.Int32
	callError atomic.Bool
	stopError atomic.Bool
	block     chan struct{}
	started   chan struct{}
}

func (*connectionDriver) Validate(string, json.RawMessage) error        { return nil }
func (*connectionDriver) Start(context.Context, runtime.Resource) error { return nil }
func (d *connectionDriver) Tools(context.Context, runtime.Resource) (computer.Contract, error) {
	return d.contract, nil
}
func (d *connectionDriver) Stop(context.Context, runtime.Resource) error {
	d.stops.Add(1)
	if d.stopError.Load() {
		return errors.New("screen stop not verified")
	}
	return nil
}
func (d *connectionDriver) Call(ctx context.Context, _ runtime.Resource, _ string, _ json.RawMessage) (json.RawMessage, error) {
	d.calls.Add(1)
	if d.callError.Load() {
		return nil, errors.New("native response lost")
	}
	if d.started != nil {
		d.started <- struct{}{}
	}
	if d.block != nil {
		select {
		case <-d.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return json.RawMessage(`{"content":[],"isError":false}`), nil
}
func connectionRuntime(t *testing.T) (*runtime.Engine, *connectionDriver, string) {
	t.Helper()
	contract, err := computer.NewContract("aio", map[string]string{"build": "pinned"}, []computer.Tool{{Name: "native", NativeName: "native", Definition: json.RawMessage(`{"name":"native","inputSchema":{"type":"object"}}`)}})
	if err != nil {
		t.Fatal(err)
	}
	d := &connectionDriver{contract: contract}
	engine, err := runtime.Open(context.Background(), t.TempDir(), 4, contract, d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	h, err := runtime.Handler(engine, computerTestKey)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	return engine, d, server.URL
}
func connectionController(t *testing.T, path string, factory computer.FactoryForName) (*computerController, *systemSettings) {
	t.Helper()
	startup, err := loadStartupConfig(path, false)
	if err != nil {
		t.Fatal(err)
	}
	settings := newSystemSettings(startup)
	c, err := newComputerController(context.Background(), startup, settings, ServiceInfo{ServiceEpoch: newComputerID()}, "installation", "owner", func(computer.Member) (bool, error) { return false, nil })
	if err != nil {
		t.Fatal(err)
	}
	c.connect = factory
	settings.computer = c
	t.Cleanup(func() { c.cancel(); _ = c.writer.Close(context.Background()); _ = unlockDirectory(c.lock) })
	return c, settings
}
func connectionFactory(counter *atomic.Int32) computer.FactoryForName {
	return func(name, url, key string) (computer.ManagedProvider, error) {
		counter.Add(1)
		return aio.New(url, key)
	}
}
func connectionInput(c *computerController, endpoint string) computerChangeInput {
	return computerChangeInput{ServiceInfo: c.service, InstallationID: c.owner.InstallationID, Caller: Caller{Author: Author{Kind: "human", ID: "owner"}}, ID: newComputerID(), ExpectedVersion: c.current.Load().Version, Enabled: true, Provider: "aio", Endpoint: endpoint, MaxScreens: 2, CredentialAction: "replace", Credential: computerTestKey}
}
func applyConnection(t *testing.T, c *computerController, input computerChangeInput) computerChange {
	t.Helper()
	if _, err := c.save(input); err != nil {
		t.Fatal(err)
	}
	for range 12 {
		c.advance()
		c.mu.Lock()
		op := c.journal.Operations[len(c.journal.Operations)-1]
		c.mu.Unlock()
		if op.Status == "completed" || op.Status == "failed" || op.Status == "waiting" {
			return op
		}
	}
	t.Fatal("connection did not settle")
	return computerChange{}
}
func connectionBinding(t *testing.T, c *computerController, e *runtime.Engine, agent string) computer.Binding {
	t.Helper()
	info, err := e.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := e.Claim(context.Background(), computer.Claim{Owner: c.owner, Service: c.service.ServiceEpoch, ExpectedLease: info.Lease})
	if err != nil {
		t.Fatal(err)
	}
	return computer.Binding{Owner: c.owner, Service: c.service.ServiceEpoch, Lease: lease, Member: MemberIdentity{RoomIdentity: RoomIdentity{RoomID: "controlled", RoomInstance: "instance"}, AgentID: agent, MembershipID: agent}}
}
func TestComputerDisabledHasNoProviderIO(t *testing.T) {
	for _, source := range []string{"", "[computer]\nendpoint='http://127.0.0.1:1'\n", "[computer]\nenabled=false\nendpoint='http://127.0.0.1:1'\n"} {
		t.Run(source, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			c, _ := connectionController(t, path, connectionFactory(&calls))
			c.advance()
			w := httptest.NewRecorder()
			serveComputer(w, httptest.NewRequest("GET", "/v1/computer", nil), c)
			if w.Code != 200 || calls.Load() != 0 || c.current.Load().Enabled {
				t.Fatalf("disabled read probed provider: %d %s", calls.Load(), w.Body.String())
			}
			input := connectionInput(c, "http://127.0.0.1:1")
			input.Enabled = false
			input.CredentialAction = "keep"
			input.Credential = ""
			if op := applyConnection(t, c, input); op.Status != "completed" {
				t.Fatalf("offline save: %+v", op)
			}
			if calls.Load() != 0 {
				t.Fatal("disabled save constructed provider")
			}
			input = connectionInput(c, "http://127.0.0.1:1")
			if op := applyConnection(t, c, input); op.Status != "failed" {
				t.Fatal("offline enable claimed success")
			}
			if calls.Load() != 1 {
				t.Fatal("explicit enable did not probe")
			}
		})
	}
}
func TestComputerOnlineConnectionCASCredentialsAndCapacity(t *testing.T) {
	engine, driver, url := connectionRuntime(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	source := "# keep header\n[server]\ntime_zone='UTC' # keep timezone\n[env]\nOTHER='untouched'\n"
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	c, settings := connectionController(t, path, connectionFactory(&calls))
	first := connectionInput(c, url)
	if op := applyConnection(t, c, first); op.Status != "completed" {
		t.Fatalf("first enable: %+v", op)
	}
	original := *c.current.Load()
	if _, err := c.save(first); err != nil {
		t.Fatal("lost save response could not query original ID", err)
	}
	conflict := connectionInput(c, url)
	conflict.ExpectedVersion = 1
	if _, err := c.save(conflict); !errors.Is(err, ErrConflict) {
		t.Fatal("stale page overwrote config", err)
	}
	if _, err := settings.save("Asia/Shanghai", 1); err != nil {
		t.Fatal(err)
	}
	bind := connectionBinding(t, c, engine, "A")
	if _, err := c.ensureScreen(context.Background(), bind); err != nil {
		t.Fatal(err)
	}
	bindB := bind
	bindB.Member.AgentID = "B"
	bindB.Member.MembershipID = "B"
	if _, err := c.ensureScreen(context.Background(), bindB); err != nil {
		t.Fatal(err)
	}
	lower := connectionInput(c, url)
	lower.MaxScreens = 1
	lower.CredentialAction = "keep"
	lower.Credential = ""
	if op := applyConnection(t, c, lower); op.Status != "completed" {
		t.Fatal(op)
	}
	if driver.stops.Load() != 0 {
		t.Fatal("lowering capacity stopped existing screen")
	}
	third := bind
	third.Member.AgentID = "C"
	third.Member.MembershipID = "C"
	if _, err := c.ensureScreen(context.Background(), third); !computer.Is(err, computer.Capacity) {
		t.Fatal("capacity not enforced", err)
	}
	// A second authenticated endpoint to the same instance is an ordinary update.
	h, _ := runtime.Handler(engine, strings.Repeat("new-key-", 8))
	server := httptest.NewServer(h)
	defer server.Close()
	update := connectionInput(c, server.URL)
	update.Credential = strings.Repeat("new-key-", 8)
	update.MaxScreens = 1
	if op := applyConnection(t, c, update); op.Status != "completed" {
		t.Fatal(op)
	}
	if c.current.Load().InstanceID != original.InstanceID || driver.stops.Load() != 0 {
		t.Fatal("same instance update disrupted screens")
	}
	bad := connectionInput(c, url)
	bad.Credential = "wrong-credential-0123456789abcdef012345"
	if op := applyConnection(t, c, bad); op.Status != "failed" {
		t.Fatal("invalid credential applied")
	}
	if c.current.Load().Endpoint != server.URL {
		t.Fatal("validation failure replaced active config")
	}
	w := httptest.NewRecorder()
	serveComputer(w, httptest.NewRequest("GET", "/v1/computer", nil), c)
	if strings.Contains(w.Body.String(), computerTestKey) || strings.Contains(w.Body.String(), "credential_ref") || strings.Contains(w.Body.String(), "new-key-") {
		t.Fatal("credential leaked", w.Body.String())
	}
	var receipt map[string]any
	w = httptest.NewRecorder()
	serveComputer(w, httptest.NewRequest("GET", "/v1/computer/operations/"+first.ID, nil), c)
	if json.Unmarshal(w.Body.Bytes(), &receipt) != nil || receipt["status"] != "completed" {
		t.Fatal("lost response receipt unavailable")
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(saved, []byte("# keep timezone")) || !bytes.Contains(saved, []byte("OTHER='untouched'")) || bytes.Contains(saved, []byte(computerTestKey)) {
		t.Fatal("unrelated config or credential changed", string(saved))
	}
	// Private immutable credentials are separate from model credentials.
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(path), "computer-credentials"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, _ := entry.Info()
		if info.Mode().Perm() != 0600 {
			t.Fatal("credential is not private")
		}
	}
}
func TestComputerReplacementWaitsForRemoteSettlementAndGuardsMembers(t *testing.T) {
	old, driver, url := connectionRuntime(t)
	newEngine, newDriver, newURL := connectionRuntime(t)
	var calls atomic.Int32
	c, _ := connectionController(t, filepath.Join(t.TempDir(), "config.toml"), connectionFactory(&calls))
	if op := applyConnection(t, c, connectionInput(c, url)); op.Status != "completed" {
		t.Fatal(op)
	}
	bind := connectionBinding(t, c, old, "A")
	screen, err := c.ensureScreen(context.Background(), bind)
	if err != nil {
		t.Fatal(err)
	}
	c.bound = func(computer.Member) (bool, error) { return true, nil }
	replace := connectionInput(c, newURL)
	op := applyConnection(t, c, replace)
	if op.Status != "waiting" || driver.stops.Load() != 0 || newDriver.calls.Load() != 0 || c.current.Load().Endpoint != url {
		t.Fatal("member replacement bypassed coordination", op)
	}
	c.bound = func(computer.Member) (bool, error) { return false, nil }
	driver.started = make(chan struct{}, 1)
	driver.block = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := c.active.Restore(screen.Info()).Call(context.Background(), computer.Call{ID: newComputerID(), Scope: computer.Scope{Session: "s", Turn: "t"}, Tool: "native", Arguments: json.RawMessage(`{}`), ContractDigest: driver.contract.Digest})
		done <- err
	}()
	<-driver.started
	// Drain waits for the real call; settings reads and new task refusal remain live.
	advanced := make(chan struct{})
	go func() { c.advance(); c.advance(); close(advanced) }()
	time.Sleep(20 * time.Millisecond)
	if _, err := c.ensureScreen(context.Background(), bind); !computer.Is(err, computer.Unavailable) {
		t.Fatal("new task admitted during replacement", err)
	}
	// ensureScreen must fail promptly, including while settlement holds its gate.
	close(driver.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	<-advanced
	for range 8 {
		c.advance()
	}
	if c.current.Load().Endpoint != newURL || driver.stops.Load() != 1 || newDriver.calls.Load() != 0 {
		t.Fatal("replacement did not settle old connection first")
	}
	info, _ := newEngine.Inspect(context.Background())
	if info.Running != 0 {
		t.Fatal("candidate executed before opening admission")
	}
}

func TestComputerConcurrentSaveAdmitsOnePendingOperation(t *testing.T) {
	var calls atomic.Int32
	c, _ := connectionController(t, filepath.Join(t.TempDir(), "config.toml"), connectionFactory(&calls))
	var wg sync.WaitGroup
	var successes atomic.Int32
	for range 8 {
		wg.Go(func() {
			input := connectionInput(c, "http://127.0.0.1:1")
			input.Enabled = false
			if _, err := c.save(input); err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrBusy) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("more than one pending operation", successes.Load())
	}
}

func TestComputerConnectionResumesEveryDurableStep(t *testing.T) {
	for boundary := 0; boundary < 8; boundary++ {
		t.Run(string(rune('0'+boundary)), func(t *testing.T) {
			old, driver, url := connectionRuntime(t)
			_, newDriver, newURL := connectionRuntime(t)
			path := filepath.Join(t.TempDir(), "config.toml")
			var calls atomic.Int32
			c, _ := connectionController(t, path, connectionFactory(&calls))
			if op := applyConnection(t, c, connectionInput(c, url)); op.Status != "completed" {
				t.Fatal(op)
			}
			binding := connectionBinding(t, c, old, "A")
			screen, err := c.ensureScreen(context.Background(), binding)
			if err != nil {
				t.Fatal(err)
			}
			id := screen.Info().ID
			input := connectionInput(c, newURL)
			if _, err = c.save(input); err != nil {
				t.Fatal(err)
			}
			for range boundary {
				c.advance()
			}
			c.cancel()
			if err = c.writer.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err = unlockDirectory(c.lock); err != nil {
				t.Fatal(err)
			}
			restarted, _ := connectionController(t, path, connectionFactory(&calls))
			// The logical ID is stable and a new service epoch can query/retry the same
			// candidate without adopting a different endpoint or credential version.
			input.ServiceInfo = restarted.service
			if _, err = restarted.save(input); err != nil {
				t.Fatal("same operation recovery", err)
			}
			for range 12 {
				restarted.advance()
			}
			if restarted.current.Load().Endpoint != newURL || restarted.current.Load().Version != 3 || newDriver.calls.Load() != 0 {
				t.Fatal("recovery changed target or replayed a task")
			}
			inventory, err := old.Screens(context.Background(), c.owner)
			if err != nil {
				t.Fatal(err)
			}
			if len(inventory) != 1 || inventory[0].ID != id || inventory[0].Status != "stopped" || driver.stops.Load() != 1 {
				t.Fatal("recovery deleted profile or failed stop", inventory)
			}
		})
	}
}

func TestComputerDisablePersistsIntentAndRestartsOffline(t *testing.T) {
	engine, driver, url := connectionRuntime(t)
	var calls atomic.Int32
	path := filepath.Join(t.TempDir(), "config.toml")
	c, _ := connectionController(t, path, connectionFactory(&calls))
	if op := applyConnection(t, c, connectionInput(c, url)); op.Status != "completed" {
		t.Fatal(op)
	}
	binding := connectionBinding(t, c, engine, "A")
	screen, err := c.ensureScreen(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	disable := connectionInput(c, url)
	disable.Enabled = false
	disable.CredentialAction = "keep"
	disable.Credential = ""
	if _, err = c.save(disable); err != nil {
		t.Fatal(err)
	}
	if _, err = screen.Call(context.Background(), computer.Call{}); !computer.Is(err, computer.Unavailable) {
		t.Fatal("disable admitted a new tool call", err)
	}
	for range 10 {
		c.advance()
	}
	if c.current.Load().Enabled || driver.stops.Load() != 1 {
		t.Fatal("disable did not finish")
	}
	c.cancel()
	_ = c.writer.Close(context.Background())
	_ = unlockDirectory(c.lock)
	var offline atomic.Int32
	restarted, _ := connectionController(t, path, func(string, string, string) (computer.ManagedProvider, error) {
		offline.Add(1)
		return nil, errors.New("no container or helper")
	})
	restarted.advance()
	if offline.Load() != 0 || restarted.current.Load().Enabled {
		t.Fatal("disabled restart touched provider")
	}
	screens, err := engine.Screens(context.Background(), c.owner)
	if err != nil || len(screens) != 1 || screens[0].ID != screen.Info().ID {
		t.Fatal("disable deleted persistent screen")
	}
}

func TestComputerHTTPIdentityValidationAndLostSaveReceipt(t *testing.T) {
	_, _, url := connectionRuntime(t)
	var calls atomic.Int32
	c, _ := connectionController(t, filepath.Join(t.TempDir(), "config.toml"), connectionFactory(&calls))
	input := connectionInput(c, url)
	post := func(path string, input computerChangeInput) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(input)
		w := httptest.NewRecorder()
		serveComputer(w, httptest.NewRequest("POST", path, bytes.NewReader(raw)), c)
		return w
	}
	for _, wrong := range []string{"installation", "caller", "epoch"} {
		bad := input
		switch wrong {
		case "installation":
			bad.InstallationID = "other"
		case "caller":
			bad.Caller.Kind = "agent"
		case "epoch":
			bad.ServiceEpoch = "stale"
		}
		if w := post("/v1/computer/save", bad); w.Code == 202 {
			t.Fatal("identity bypass", wrong)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("identity rejection contacted provider")
	}
	if w := post("/v1/computer/validate", input); w.Code != 200 || strings.Contains(w.Body.String(), computerTestKey) {
		t.Fatal(w.Code, w.Body.String())
	}
	if c.current.Load().Enabled || len(c.journal.Operations) != 0 {
		t.Fatal("validation saved state")
	}
	if w := post("/v1/computer/save", input); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	for range 8 {
		c.advance()
	}
	duplicate := post("/v1/computer/save", input)
	if duplicate.Code != 202 || !strings.Contains(duplicate.Body.String(), "completed") {
		t.Fatal("lost response caused new operation", duplicate.Body.String())
	}
	input.ID = newComputerID()
	if w := post("/v1/computer/save", input); w.Code != 409 {
		t.Fatal("stale page did not conflict", w.Code)
	}
}

func TestComputerConfigCommitLostReceiptRecovers(t *testing.T) {
	_, _, url := connectionRuntime(t)
	var calls atomic.Int32
	path := filepath.Join(t.TempDir(), "config.toml")
	c, _ := connectionController(t, path, connectionFactory(&calls))
	input := connectionInput(c, url)
	if _, err := c.save(input); err != nil {
		t.Fatal(err)
	}
	c.advance()
	c.advance() // frozen candidate; commit is next
	originalSync := c.document.ops.syncDir
	c.document.ops.syncDir = func(string) error { return errors.New("lost directory sync acknowledgement") }
	c.advance()
	if c.current.Load().Enabled {
		t.Fatal("uncertain config was published")
	}
	c.document.ops.syncDir = originalSync
	c.cancel()
	_ = c.writer.Close(context.Background())
	_ = unlockDirectory(c.lock)
	restarted, _ := connectionController(t, path, connectionFactory(&calls))
	for range 8 {
		restarted.advance()
	}
	if !restarted.current.Load().Enabled || restarted.current.Load().OperationID != input.ID || restarted.current.Load().Version != 2 {
		t.Fatal("uncertain commit was replayed or lost")
	}
}

func TestComputerFrozenCandidateCannotChangeInstanceDuringApply(t *testing.T) {
	_, _, oldURL := connectionRuntime(t)
	first, _, _ := connectionRuntime(t)
	other, _, _ := connectionRuntime(t)
	firstHandler, _ := runtime.Handler(first, computerTestKey)
	otherHandler, _ := runtime.Handler(other, computerTestKey)
	var changed atomic.Bool
	candidate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if changed.Load() {
			otherHandler.ServeHTTP(w, r)
		} else {
			firstHandler.ServeHTTP(w, r)
		}
	}))
	defer candidate.Close()
	var connections atomic.Int32
	c, _ := connectionController(t, filepath.Join(t.TempDir(), "config.toml"), connectionFactory(&connections))
	if op := applyConnection(t, c, connectionInput(c, oldURL)); op.Status != "completed" {
		t.Fatal(op)
	}
	input := connectionInput(c, candidate.URL)
	if _, err := c.save(input); err != nil {
		t.Fatal(err)
	}
	c.advance() // Candidate identity is now durable, before old settlement.
	c.mu.Lock()
	frozen := c.journal.Operations[len(c.journal.Operations)-1].Target.InstanceID
	c.mu.Unlock()
	changed.Store(true)
	for range 8 {
		c.advance()
	}
	c.mu.Lock()
	op := c.journal.Operations[len(c.journal.Operations)-1]
	c.mu.Unlock()
	if c.current.Load().Endpoint != oldURL || op.Target.InstanceID != frozen || op.Status != "waiting" {
		t.Fatal("candidate was silently re-resolved", op)
	}
}

func TestComputerDisabledServerRunsWithoutContainerOrHelper(t *testing.T) {
	// This actual TeamX server uses no executable discovery, Docker, Runtime, or
	// real model. Ordinary human Room operations remain independent of Computer.
	t.Setenv("PATH", t.TempDir())
	for _, source := range []string{"", "[computer]\nendpoint='http://127.0.0.1:1'\n", "[computer]\nenabled=false\nendpoint='http://127.0.0.1:1'\n"} {
		t.Run(source, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(source+"\n[access]\nlogin_enabled=false\noperator_id='owner_00000000000000000000000000000001'\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var svc *service
			ready, stop := startTestServer(t, []string{"--config", path, "--room", "offline", "--state-dir", t.TempDir()}, serverOptions{create: func(dir, room string, config serviceConfig) (*service, error) {
				var err error
				svc, err = newService(dir, room, config)
				return svc, err
			}})
			defer stop()
			c := svc.settings.computer
			var count atomic.Int32
			c.mu.Lock()
			c.connect = func(string, string, string) (computer.ManagedProvider, error) {
				count.Add(1)
				return nil, errors.New("container and helper absent")
			}
			c.mu.Unlock()
			response, err := http.Get(ready.Endpoint + "/v1/computer")
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != 200 || !bytes.Contains(raw, []byte(`"enabled":false`)) || c.active != nil {
				t.Fatal("disabled settings unavailable")
			}
			request := apiRequest{ServiceInfo: svc.info, Caller: Caller{Author: Author{Kind: "human", ID: c.owner.OwnerID}}, Text: "ordinary offline Room", ClientMessageID: newComputerID()}
			raw, _ = json.Marshal(request)
			req, _ := http.NewRequest("POST", ready.Endpoint+"/v1/send", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", ready.Endpoint)
			response, err = http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ = io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != 200 || count.Load() != 0 {
				t.Fatal("ordinary Room was blocked by Computer", response.StatusCode, string(raw))
			}
		})
	}
}

func TestComputerOfflineDisableAcceptsIntentWithoutClaimingSettlement(t *testing.T) {
	engine, driver, endpoint := connectionRuntime(t)
	var connections atomic.Int32
	var offline atomic.Bool
	factory := func(name, url, key string) (computer.ManagedProvider, error) {
		connections.Add(1)
		if offline.Load() {
			return nil, computer.Fail(computer.Unavailable, "offline Runtime")
		}
		return aio.New(url, key)
	}
	c, _ := connectionController(t, filepath.Join(t.TempDir(), "config.toml"), factory)
	if op := applyConnection(t, c, connectionInput(c, endpoint)); op.Status != "completed" {
		t.Fatal(op)
	}
	binding := connectionBinding(t, c, engine, "A")
	screen, err := c.ensureScreen(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	offline.Store(true)
	before := connections.Load()
	input := connectionInput(c, endpoint)
	input.Enabled = false
	if _, err = c.save(input); err != nil {
		t.Fatal("offline disable intent rejected", err)
	}
	if connections.Load() != before {
		t.Fatal("accepting disable required an online Runtime")
	}
	if _, err = screen.Call(context.Background(), computer.Call{}); !computer.Is(err, computer.Unavailable) {
		t.Fatal("offline disable allowed a new call")
	}
	c.advance()
	c.advance()
	c.mu.Lock()
	op := c.journal.Operations[len(c.journal.Operations)-1]
	c.mu.Unlock()
	if op.Status != "waiting" || driver.stops.Load() != 0 || !c.current.Load().Enabled {
		t.Fatal("offline pending was called settled", op)
	}
	before = connections.Load()
	w := httptest.NewRecorder()
	serveComputer(w, httptest.NewRequest("GET", "/v1/computer", nil), c)
	if w.Code != 200 || connections.Load() != before {
		t.Fatal("reading pending settings contacted Runtime")
	}
}

func TestComputerConnectionChangeVerifiesBlockedScreenStop(t *testing.T) {
	for _, disable := range []bool{false, true} {
		name := "replace"
		if disable {
			name = "disable"
		}
		t.Run(name, func(t *testing.T) {
			engine, driver, endpoint := connectionRuntime(t)
			_, replacementDriver, replacement := connectionRuntime(t)
			var connections atomic.Int32
			c, _ := connectionController(t, filepath.Join(t.TempDir(), "config.toml"), connectionFactory(&connections))
			if op := applyConnection(t, c, connectionInput(c, endpoint)); op.Status != "completed" {
				t.Fatal(op)
			}
			binding := connectionBinding(t, c, engine, "A")
			screen, err := c.ensureScreen(t.Context(), binding)
			if err != nil {
				t.Fatal(err)
			}
			driver.callError.Store(true)
			call := computer.Call{ID: newComputerID(), Scope: computer.Scope{Session: "session"}, Tool: "native", Arguments: json.RawMessage(`{}`), ContractDigest: driver.contract.Digest}
			if _, err = screen.Call(t.Context(), call); !computer.Is(err, computer.Unknown) {
				t.Fatal("lost response did not block screen", err)
			}
			change := connectionInput(c, replacement)
			if disable {
				change.Enabled = false
				change.Endpoint = endpoint
				change.CredentialAction = "keep"
				change.Credential = ""
			}
			driver.stopError.Store(true)
			t.Cleanup(func() { driver.stopError.Store(false) })
			op := applyConnection(t, c, change)
			if op.Stage != "stop" || op.Status != "waiting" || op.Stopped != 0 || driver.stops.Load() != 1 || !c.current.Load().Enabled || c.current.Load().Endpoint != endpoint {
				t.Fatal("unverified stop advanced connection", op)
			}
			driver.stopError.Store(false)
			op = applyConnection(t, c, change)
			if op.Status != "completed" || op.Stopped != 1 || driver.stops.Load() != 2 {
				t.Fatal("original change did not recover verified stop", op)
			}
			inventory, err := engine.Screens(t.Context(), c.owner)
			if err != nil || len(inventory) != 1 || inventory[0].Status != "stopped" {
				t.Fatal("screen stop not confirmed", inventory, err)
			}
			fact, err := screen.Operation(t.Context(), call.ID)
			if err != nil || fact.Operation.State != "unknown" || !fact.Operation.Settled || fact.ResultAvailable || driver.calls.Load() != 1 || replacementDriver.calls.Load() != 0 {
				t.Fatal("unknown outcome lost or business action replayed", fact, err)
			}
			if c.current.Load().Enabled == disable || !disable && c.current.Load().Endpoint != replacement {
				t.Fatal("target configuration was not applied")
			}
		})
	}
}

func TestComputerStopReceiptLossResumesWithoutRestartingDesktop(t *testing.T) {
	engine, driver, url := connectionRuntime(t)
	_, _, replacement := connectionRuntime(t)
	var connections atomic.Int32
	path := filepath.Join(t.TempDir(), "config.toml")
	c, _ := connectionController(t, path, connectionFactory(&connections))
	if op := applyConnection(t, c, connectionInput(c, url)); op.Status != "completed" {
		t.Fatal(op)
	}
	binding := connectionBinding(t, c, engine, "A")
	if _, err := c.ensureScreen(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	input := connectionInput(c, replacement)
	if _, err := c.save(input); err != nil {
		t.Fatal(err)
	}
	c.advance()
	c.advance() // Durable stop list, but no acknowledged stop yet.
	c.mu.Lock()
	op := c.journal.Operations[len(c.journal.Operations)-1]
	c.mu.Unlock()
	if len(op.Screens) != 1 || op.Stage != "stop" {
		t.Fatal("missing original stop list")
	}
	provider, _ := aio.New(url, computerTestKey)
	if err := provider.Restore(op.Screens[0]).Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The service dies before it can persist the stop acknowledgement.
	c.cancel()
	_ = c.writer.Close(context.Background())
	_ = unlockDirectory(c.lock)
	recovered, _ := connectionController(t, path, connectionFactory(&connections))
	for range 8 {
		recovered.advance()
	}
	if recovered.current.Load().Endpoint != replacement || driver.stops.Load() != 1 || driver.calls.Load() != 0 {
		t.Fatal("recovery repeated stop or created a new desktop")
	}
}

func TestComputerConfigPatchPreservesOtherSettingsAndComments(t *testing.T) {
	for _, source := range []string{
		"# header\n[server]\ntime_zone='UTC' # zone\n",
		"computer={enabled=false,provider='aio'} # connection\nserver.time_zone='UTC' # zone\n",
		"computer.enabled=false # enabled\n[server]\ntime_zone='UTC' # zone\n",
		"['computer'] # connection\nenabled=false # enabled\n[server]\ntime_zone='UTC' # zone\n[env]\nOTHER='''[computer]\nthis is a multiline value'''\n",
	} {
		target := defaultComputerConfig(computerConfig{})
		target.Endpoint = "http://127.0.0.1:1"
		target.Version = 2
		patched, err := patchComputerConfig([]byte(source), target)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(patched, []byte("# zone")) || !bytes.Contains(patched, []byte("time_zone='UTC'")) {
			t.Fatal("unrelated field/comment rewritten", string(patched))
		}
		if strings.Contains(source, "# connection") && !bytes.Contains(patched, []byte("# connection")) {
			t.Fatal("Computer comment lost")
		}
	}
}

func TestComputerJournalFailureCannotPublishACompletedReceipt(t *testing.T) {
	_, _, url := connectionRuntime(t)
	var count atomic.Int32
	path := filepath.Join(t.TempDir(), "config.toml")
	c, _ := connectionController(t, path, connectionFactory(&count))
	input := connectionInput(c, url)
	if _, err := c.save(input); err != nil {
		t.Fatal(err)
	}
	c.advance()
	c.advance()
	c.writer.MarkUncertain()
	c.advance() // Configuration is durable; its journal receipt is not confirmed.
	c.mu.Lock()
	op := c.journal.Operations[len(c.journal.Operations)-1]
	ready := c.ready
	c.mu.Unlock()
	if ready || op.Status != "waiting" || !c.current.Load().Enabled {
		t.Fatal("uncertain receipt published success", op)
	}
	c.cancel()
	_ = c.writer.Close(context.Background())
	_ = unlockDirectory(c.lock)
	recovered, _ := connectionController(t, path, connectionFactory(&count))
	for range 8 {
		recovered.advance()
	}
	if recovered.current.Load().Version != 2 || recovered.current.Load().OperationID != input.ID {
		t.Fatal("journal recovery changed committed configuration")
	}
}
