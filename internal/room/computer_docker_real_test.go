package room

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/computer"
	"github.com/go-aie/teamx/internal/computer/aio"
	computerdocker "github.com/go-aie/teamx/internal/computer/docker"
)

// This test touches only a newly generated owner and its disposable resources.
// It never adopts the user's existing Computer or changes the daily service.
func TestComputerRealManagedDockerLifecycle(t *testing.T) {
	if os.Getenv("TEAMX_COMPUTER_DOCKER_REAL") != "1" {
		t.Skip("explicit local Docker acceptance only")
	}
	output := os.Getenv("TEAMX_COMPUTER_EVIDENCE")
	if output == "" {
		t.Fatal("evidence directory required")
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	c, _ := connectionController(t, path, func(name, endpoint, key string) (computer.ManagedProvider, error) { return aio.New(endpoint, key) })
	c.owner.InstallationID = "managed-test-" + newComputerID()
	c.owner.ComputerID = computer.Hash(c.owner.InstallationID)
	docker := computerdocker.New()
	c.docker = docker
	t.Cleanup(func() {
		c.cancel()
		c.finishDockerWorker()
		c.mu.Lock()
		var state *computerdocker.State
		if c.journal.Docker != nil {
			copy := *c.journal.Docker
			state = &copy
		}
		c.mu.Unlock()
		if state == nil {
			return
		}
		if t.Failed() {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			logs := exec.CommandContext(ctx, "docker", "--host", state.Host, "exec", state.Container, "python3", "-c", "from pathlib import Path\nfor p in [*Path('/run/teamx-computer').glob('*/*.log'),Path('/sys/fs/cgroup/memory.events')]:\n print(str(p));print(p.read_text(errors='replace')[-16000:])")
			if b, err := logs.CombinedOutput(); err == nil {
				_ = os.WriteFile(filepath.Join(output, "screen-startup.log"), b, 0600)
			}
			cancel()
		}
		// The controller must already have proved normal shutdown. Cleanup is
		// explicit test-only disposal, never part of production disable.
		ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
		defer cancel()
		if err := docker.Stop(ctx, state); err != nil {
			t.Error("disposable cleanup stop", err)
			return
		}
		for _, args := range [][]string{{"rm", state.Name}, {"volume", "rm", state.Name + "-state", state.Name + "-files"}} {
			cmd := exec.CommandContext(ctx, "docker", append([]string{"--host", state.Host}, args...)...)
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Error("disposable resource cleanup", err, string(b))
			}
		}
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveComputer(w, r, c) }))
	defer server.Close()
	request := func(route string, input any) map[string]any {
		t.Helper()
		var body io.Reader
		method := "GET"
		if input != nil {
			raw, _ := json.Marshal(input)
			body = bytes.NewReader(raw)
			method = "POST"
		}
		req, _ := http.NewRequest(method, server.URL+route, body)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode >= 300 {
			t.Fatalf("HTTP %s: %d %s %v", route, res.StatusCode, raw, err)
		}
		if bytes.Contains(raw, []byte("credential_ref")) {
			t.Fatal("private reference exposed")
		}
		var v map[string]any
		if json.Unmarshal(raw, &v) != nil {
			t.Fatal("invalid HTTP response")
		}
		return v
	}
	report := map[string]any{"passed": false, "image": computerdocker.DefaultImage(), "host_os": os.Getenv("OSTYPE"), "model": "not run in lifecycle-only acceptance"}
	t.Cleanup(func() {
		raw, _ := json.MarshalIndent(report, "", "  ")
		_ = os.WriteFile(filepath.Join(output, "managed-lifecycle.json"), raw, 0600)
	})
	initial := request("/v1/computer", nil)
	if initial["enabled"] != false {
		t.Fatal("default not disabled")
	}
	environment := request("/v1/computer/check", managedInput(c))
	report["environment"] = environment
	drive := func(in computerChangeInput) {
		t.Helper()
		request("/v1/computer/save", in)
		deadline := time.Now().Add(6 * time.Minute)
		for time.Now().Before(deadline) {
			c.advance()
			view := request("/v1/computer/operations/"+in.ID, nil)
			if view["status"] == "completed" {
				t.Log("completed", in.Enabled, view["stage"])
				return
			}
			if view["status"] == "failed" {
				t.Fatal(view)
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("Docker apply timeout", request("/v1/computer", nil))
	}
	started := time.Now()
	drive(managedInput(c))
	report["first_enable_ms"] = time.Since(started).Milliseconds()
	c.mu.Lock()
	original := *c.journal.Docker
	c.mu.Unlock()
	key, err := c.credentials.get(original.KeyRef)
	if err != nil {
		t.Fatal(err)
	}
	p, err := aio.New(c.current.Load().Endpoint, key)
	if err != nil {
		t.Fatal(err)
	}
	info, err := p.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := p.Claim(context.Background(), computer.Claim{Owner: c.owner, Service: c.runtimeService(*c.current.Load(), info), ExpectedLease: info.Lease})
	if err != nil {
		t.Fatal(err)
	}
	var screens []computer.Screen
	for _, agent := range []string{"A", "B"} {
		binding := computer.Binding{Owner: c.owner, Service: c.runtimeService(*c.current.Load(), info), Lease: lease, Member: MemberIdentity{RoomIdentity: RoomIdentity{RoomID: "managed-real", RoomInstance: "instance"}, AgentID: agent, MembershipID: agent}}
		t.Log("starting screen", agent)
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		screen, err := c.ensureScreen(ctx, binding)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		screens = append(screens, screen)
	}
	// Disposable loopback site inside the new Computer; no host mounts, tokens,
	// real account, or external website writes are involved.
	site := "from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer\nclass Site(BaseHTTPRequestHandler):\n def do_GET(self):\n  self.send_response(200);self.send_header('Content-Type','text/html')\n  if self.path=='/signin-a':self.send_header('Set-Cookie','managed_session=fictional; Path=/; Max-Age=604800; HttpOnly')\n  self.end_headers();self.wfile.write(('<html><title>Managed Computer acceptance</title><body>'+('authenticated' if 'managed_session=fictional' in self.headers.get('Cookie','') else 'anonymous')+'</body></html>').encode())\nThreadingHTTPServer(('127.0.0.1',18765),Site).serve_forever()"
	startSite := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "docker", "--host", original.Host, "exec", "-d", original.Container, "python3", "-c", site)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(b))
		}
	}
	startSite()
	call := func(screen computer.Screen, tool, args string) json.RawMessage {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		out, err := screen.Call(ctx, computer.Call{ID: newComputerID(), Scope: computer.Scope{Session: "acceptance-session"}, Tool: tool, Arguments: json.RawMessage(args), ContractDigest: c.current.Load().ContractDigest})
		if err != nil {
			t.Fatal(tool, err)
		}
		return out.Result
	}
	for _, screen := range screens {
		result := call(screen, "browser_navigate", `{"url":"http://127.0.0.1:18765"}`)
		if strings.Contains(string(result), `"isError":true`) {
			t.Fatal(string(result))
		}
	}
	call(screens[0], "browser_navigate", `{"url":"http://127.0.0.1:18765/signin-a"}`)
	call(screens[0], "browser_navigate", `{"url":"http://127.0.0.1:18765/private"}`)
	if out := call(screens[0], "browser_get_text", `{}`); !strings.Contains(string(out), "authenticated") {
		t.Fatal("fictional cookie not set", string(out))
	}
	off := managedInput(c)
	off.Enabled = false
	started = time.Now()
	drive(off)
	report["disable_ms"] = time.Since(started).Milliseconds()
	c.mu.Lock()
	stopped := *c.journal.Docker
	c.mu.Unlock()
	if !stopped.Stopped {
		t.Fatal("container not confirmed stopped")
	}
	// Disabled operation completion and view must not issue Docker I/O.
	start := time.Now()
	drive(managedInput(c))
	report["warm_enable_ms"] = time.Since(start).Milliseconds()
	c.mu.Lock()
	reused := *c.journal.Docker
	c.mu.Unlock()
	if reused.Container != original.Container || reused.KeyRef != original.KeyRef || reused.Instance != original.Instance {
		t.Fatal("re-enable changed persistent runtime")
	}
	startSite()
	p, err = aio.New(c.current.Load().Endpoint, key)
	if err != nil {
		t.Fatal(err)
	}
	info, err = p.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lease, err = p.Claim(context.Background(), computer.Claim{Owner: c.owner, Service: c.runtimeService(*c.current.Load(), info), ExpectedLease: info.Lease})
	if err != nil {
		t.Fatal("same TeamX process could not reclaim restarted Runtime", err)
	}
	for i, old := range screens {
		binding := old.Info().Binding
		binding.Service = c.runtimeService(*c.current.Load(), info)
		binding.Lease = lease
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		screen, err := c.ensureScreen(ctx, binding)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if screen.Info().ID != old.Info().ID || screen.Info().Epoch <= old.Info().Epoch {
			t.Fatal("screen identity or restart generation not preserved")
		}
		call(screen, "browser_navigate", `{"url":"http://127.0.0.1:18765/private"}`)
		out := call(screen, "browser_get_text", `{}`)
		if strings.Contains(string(out), "authenticated") != (i == 0) {
			t.Fatal("login persistence or member isolation failed", i, string(out))
		}
	}
	off = managedInput(c)
	off.Enabled = false
	drive(off)
	report["container_id"] = original.Container
	report["instance_id"] = original.Instance
	report["two_screens"] = true
	report["reused_container_volumes_and_key"] = true
	report["fictional_login_preserved_and_isolated_after_container_stop_start"] = true
	report["stopped_after_disable"] = true
	report["passed"] = true
}
