package aio_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-aie/teamx/internal/computer"
	"github.com/go-aie/teamx/internal/computer/aio"
	"github.com/go-aie/teamx/internal/computer/aio/runtime"
	"github.com/go-aie/teamx/internal/identity"
)

type driver struct {
	catalog computer.Contract
	calls   atomic.Int32
}

func (*driver) Validate(string, json.RawMessage) error        { return nil }
func (*driver) Start(context.Context, runtime.Resource) error { return nil }
func (d *driver) Tools(context.Context, runtime.Resource) (computer.Contract, error) {
	return d.catalog, nil
}
func (d *driver) Call(context.Context, runtime.Resource, string, json.RawMessage) (json.RawMessage, error) {
	d.calls.Add(1)
	return json.RawMessage(`{"content":[{"type":"image","mimeType":"image/png","data":"AA=="}],"isError":true,"structuredContent":{"native":1},"unknown":{"preserve":true}}`), nil
}
func (*driver) Stop(context.Context, runtime.Resource) error { return nil }

func TestControlledHTTPAndLostMutationResponse(t *testing.T) {
	ctx := context.Background()
	contract, err := computer.NewContract("test", map[string]string{"build": "one"}, []computer.Tool{{Name: "vendor_write", NativeName: "vendor_write", Definition: json.RawMessage(`{"name":"vendor_write","inputSchema":{"type":"object"},"extension":true}`)}})
	if err != nil {
		t.Fatal(err)
	}
	d := &driver{catalog: contract}
	e, err := runtime.Open(ctx, t.TempDir(), 2, contract, d)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close(ctx)
	token := strings.Repeat("s", 32)
	handler, err := runtime.Handler(e, token)
	if err != nil {
		t.Fatal(err)
	}
	var reject atomic.Bool
	var drop atomic.Bool
	drop.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/screen/call" && reject.CompareAndSwap(true, false) {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(computer.Error{Code: computer.Busy, Message: "screen already executing"})
			return
		}
		if r.URL.Path == "/screen/call" && drop.CompareAndSwap(true, false) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)
			if rec.Code != 200 {
				t.Errorf("native call failed: %s", rec.Body.String())
			}
			w.WriteHeader(503)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	p, err := aio.New(server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	info, err := p.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := computer.Owner{InstallationID: "install", OwnerID: "owner", ComputerID: "computer"}
	lease, err := p.Claim(ctx, computer.Claim{Owner: owner, Service: "service", ExpectedLease: info.Lease})
	if err != nil {
		t.Fatal(err)
	}
	binding := computer.Binding{Owner: owner, Service: "service", Lease: lease, Member: computer.Member{RoomIdentity: identity.RoomIdentity{RoomID: "room", RoomInstance: "incarnation"}, AgentID: "agent", MembershipID: "member"}}
	screen, err := p.EnsureScreen(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	service, err := computer.OpenService(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := computer.Call{ID: "lost-response", Scope: computer.Scope{Session: "session", Turn: "turn"}, Tool: "vendor_write", Arguments: json.RawMessage(`{"password":"never-on-disk"}`), ContractDigest: contract.Digest}
	if _, err = service.Call(ctx, screen, c); !computer.Is(err, computer.Unavailable) {
		t.Fatalf("expected lost response: %v", err)
	}
	if d.calls.Load() != 1 || len(service.Pending()) != 1 {
		t.Fatal("missing host recovery association")
	}
	if err = service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	service, err = computer.OpenService(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(ctx)
	out, err := service.Call(ctx, p.Restore(screen.Info()), c)
	if err != nil {
		t.Fatal(err)
	}
	if !out.ResultAvailable || !strings.Contains(string(out.Result), `"unknown":{"preserve":true}`) || !strings.Contains(string(out.Result), `"isError":true`) {
		t.Fatalf("native result changed: %s", out.Result)
	}
	if d.calls.Load() != 1 || len(service.Pending()) != 0 {
		t.Fatal("response recovery replayed mutation or retained settled marker")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "pending.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "never-on-disk") || strings.Contains(string(raw), "content") {
		t.Fatal("host duplicated sensitive execution data")
	}
	reject.Store(true)
	busy := c
	busy.ID = "admission-refused"
	if _, err = service.Call(ctx, screen, busy); !computer.Is(err, computer.Busy) {
		t.Fatal(err)
	}
	if len(service.Pending()) != 0 {
		t.Fatal("verified nonacceptance became an uncertain operation")
	}
	if _, err = service.Call(ctx, screen, busy); err != nil {
		t.Fatal(err)
	}
	if d.calls.Load() != 2 {
		t.Fatal("retry after verified nonacceptance did not execute exactly once")
	}
	bad, _ := aio.New(server.URL, strings.Repeat("x", 32))
	if _, err = bad.Inspect(ctx); err == nil {
		t.Fatal("unauthenticated management access")
	}
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("raw MCP bypass exists: %d", rec.Code)
	}
	// Lowering an origin to an arbitrary proxy, userinfo, or plaintext remote is rejected locally.
	for _, endpoint := range []string{"http://example.com", "http://x:y@127.0.0.1", "http://127.0.0.1/proxy", "http://127.0.0.1?key=x"} {
		if _, err = aio.New(endpoint, token); err == nil {
			t.Fatalf("unsafe origin: %s", endpoint)
		}
	}
}
