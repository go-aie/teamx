//go:build e2e

package room

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Opt-in: persisted TeamX Room + real noVNC from an operator-configured gateway.
// Agent execution is substituted; no navigation or model invocation is needed.
func TestEnvironmentBrowser(t *testing.T) {
	config := os.Getenv("TEAMX_ENVIRONMENT_CONFIG")
	if config == "" {
		t.Skip("set TEAMX_ENVIRONMENT_CONFIG to a private viewer binding file")
	}
	bindings, err := loadAgentMCPBindings(config)
	if err != nil {
		t.Fatal(err)
	}
	var environment *environmentConfig
	for _, binding := range bindings {
		if binding.Environment != nil {
			environment = binding.Environment
			break
		}
	}
	if environment == nil {
		t.Fatal("viewer environment required")
	}
	t.Setenv("USER", humanCaller.ID)
	m := directTestManager(t, t.TempDir(), testTeamDefinition(t).Runtime)
	m.factory = controlledActivationFactory(nil, nil)
	m.config.unifiedInputs = true
	room := createDirectConversation(t, m, directSelection("environment"))
	saved, _ := readManagedRoom(t, m, room)
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: saved.Members[0].AgentID, MembershipID: saved.Members[0].MembershipID}
	m.config.agentMCP = agentMCPBindings{id: {Environment: environment}}
	other := createDirectConversation(t, m, directSelection("other-environment"))
	openRoomOK(t, m, other, "other-open")
	openRoomOK(t, m, room, "environment-open")
	server := browserTestServer(t, m)
	fixture, _ := json.Marshal(map[string]any{"room": room.RoomIdentity, "other_room": other.RoomIdentity})
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/environment_browser.cjs", server.URL, string(fixture))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("environment browser: %v\n%s", err, output)
	}
	t.Log(string(output))
}

func TestEnvironmentHandoffBrowser(t *testing.T) {
	if os.Getenv("TEAMX_ENVIRONMENT_INPUT_BROWSER") != "1" {
		t.Skip("opt-in real VNC input against a temporary form")
	}
	bindings, err := loadAgentMCPBindings(os.Getenv("TEAMX_ENVIRONMENT_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	var environment *environmentConfig
	for _, binding := range bindings {
		if binding.Environment != nil {
			environment = binding.Environment
			break
		}
	}
	if environment == nil {
		t.Fatal("environment configuration required")
	}
	key, err := randomAccessToken()
	if err != nil {
		t.Fatal(err)
	}
	var loggedIn, continued atomic.Bool
	form := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/"+key+"/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/login") {
			if r.Method != http.MethodPost || r.ParseForm() != nil || r.Form.Get("username") != "demo-user" || r.Form.Get("password") != "demo-pass-42" {
				http.Error(w, "invalid test login", 403)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "teamx_demo_session", Value: key, Path: "/" + key + "/", HttpOnly: true, MaxAge: 120})
			http.Redirect(w, r, "/"+key+"/welcome", http.StatusSeeOther)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if strings.HasSuffix(r.URL.Path, "/welcome") {
			cookie, e := r.Cookie("teamx_demo_session")
			if e != nil || cookie.Value != key {
				http.Error(w, "not signed in", 403)
				return
			}
			loggedIn.Store(true)
			fmt.Fprint(w, `<!doctype html><title>Signed in</title><h1>Signed in</h1><p>The same browser session can continue.</p>`)
			return
		}
		fmt.Fprintf(w, `<!doctype html><title>TeamX handoff test</title><style>body{margin:0;background:#eef2f5;font:20px sans-serif}form{margin:120px 160px;width:480px;padding:40px;background:white}label{display:block;margin:16px 0}input,button{display:block;font:20px sans-serif;padding:12px;width:360px;box-sizing:border-box}button{margin-top:24px}</style><form method="post" action="/%s/login"><h1>Demo sign in</h1><label>Username<input id="username" name="username" autocomplete="off"></label><label>Password<input id="password" name="password" type="password" autocomplete="off"></label><button id="login">Sign in</button></form>`, key)
	}))
	form.Listener.Close()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	form.Listener = listener
	form.Start()
	defer form.Close()
	host := os.Getenv("TEAMX_ENVIRONMENT_FIXTURE_HOST")
	if host == "" {
		host = "host.docker.internal"
	}
	remoteForm := fmt.Sprintf("http://%s:%d/%s/form", host, listener.Addr().(*net.TCPAddr).Port, key)
	t.Setenv("USER", humanCaller.ID)
	m := directTestManager(t, t.TempDir(), testTeamDefinition(t).Runtime)
	m.config.unifiedInputs = true
	m.factory = controlledActivationFactory(func(MemberIdentity, context.Context) error {
		if loggedIn.Load() {
			continued.Store(true)
		}
		return nil
	}, nil)
	room := createDirectConversation(t, m, directSelection("handoff-input"))
	saved, _ := readManagedRoom(t, m, room)
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: saved.Members[0].AgentID, MembershipID: saved.Members[0].MembershipID}
	m.config.agentMCP = agentMCPBindings{id: {Environment: environment}}
	server := browserTestServer(t, m)
	openRoomOK(t, m, room, "handoff-input-open")
	m.mu.Lock()
	svc, _ := m.runtimeServiceLocked(room.RoomIdentity)
	m.mu.Unlock()
	if _, err := svc.control(svc.ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
		t.Fatal(err)
	}
	fixture, _ := json.Marshal(map[string]any{"room": room.RoomIdentity, "fixture_url": remoteForm})
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/environment_handoff_browser.cjs", server.URL, string(fixture))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("VNC handoff browser: %v\n%s", err, output)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !continued.Load() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !loggedIn.Load() || !continued.Load() {
		status, _ := svc.target(id.MembershipID).Call[MemberStatus](svc.ctx, InspectMember{id})
		data, _ := json.Marshal(status)
		t.Log(string(output), string(data))
		t.Fatal("login or controlled Agent continuation missing", loggedIn.Load(), continued.Load())
	}
	t.Log(string(output))
	t.Log("controlled Agent resumed after the authenticated browser receipt; no real model used")
}
