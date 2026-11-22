package room

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func environmentFixture(t *testing.T, base string) (*roomManager, MemberIdentity, string) {
	t.Helper()
	t.Setenv("USER", humanCaller.ID)
	m, _ := directoryTestManager(t)
	room := managerCreate(t, m, "environment")
	saved, _ := readManagedRoom(t, m, room)
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: saved.Members[0].AgentID, MembershipID: saved.Members[0].MembershipID}
	c := &environmentConfig{BaseURL: base, Headers: []dsh.MCPHeader{{Name: "Authorization", Value: "Bearer upstream-secret"}}}
	if !c.valid() {
		t.Fatal("invalid fixture environment")
	}
	m.config.agentMCP = agentMCPBindings{id: {Environment: c}}
	return m, id, "/v1/environment/" + environmentID(m.epoch, id) + "/"
}

func TestEnvironmentPrivateConfigAndIdentity(t *testing.T) {
	m, id, prefix := environmentFixture(t, "http://127.0.0.1:18080")
	p := filepath.Join(t.TempDir(), "bindings.json")
	b, _ := json.Marshal(map[string]any{"version": 1, "bindings": []any{map[string]any{"room_id": id.RoomID, "room_instance": id.RoomInstance, "agent_id": id.AgentID, "membership_id": id.MembershipID, "environment": map[string]any{"base_url": "http://127.0.0.1:18080", "headers": []any{map[string]any{"name": "Authorization", "value": "Bearer upstream-secret"}}}}}})
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	bindings, err := loadAgentMCPBindings(p)
	if err != nil || len(bindings.servers(id)) != 0 || bindings[id].Environment.WebSocketPath != "/websockify" {
		t.Fatal("independent viewer binding", err)
	}
	q := url.Values{"room_id": {id.RoomID}, "room_instance": {id.RoomInstance}, "service_epoch": {m.epoch}, "caller_id": {humanCaller.ID}}
	r := managerHTTP(m, "/v1/environments?"+q.Encode(), nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), prefix+"viewer") || strings.Contains(r.Body.String(), "upstream-secret") || strings.Contains(r.Body.String(), "18080") {
		t.Fatal(r.Code, r.Body.String())
	}
	for _, bad := range []environmentConfig{
		{BaseURL: "http://user:password@localhost"}, {BaseURL: "http://localhost?api_key=secret"}, {BaseURL: "file:///tmp/a"},
		{BaseURL: "http://localhost", VNCPath: "/vnc/../api/"}, {BaseURL: "http://localhost", WebSocketPath: "//evil/path"},
		{BaseURL: "http://localhost", Headers: []dsh.MCPHeader{{Name: "Cookie", Value: "session=bad"}}},
	} {
		if bad.valid() {
			t.Fatalf("accepted invalid environment: %+v", bad)
		}
	}
	if r := managerHTTP(m, prefix+"viewer", nil); r.Code != 200 || r.Header().Get("Content-Security-Policy") == "" {
		t.Fatal(r.Code, r.Header())
	}
	for _, field := range []string{"room_instance", "service_epoch", "caller_id"} {
		old := q.Get(field)
		q.Set(field, "stale")
		r = managerHTTP(m, "/v1/environments?"+q.Encode(), nil)
		q.Set(field, old)
		if r.Code == 200 && strings.Contains(r.Body.String(), prefix) {
			t.Fatal("crossed identity", field)
		}
	}
	original := id
	id.MembershipID = "replacement"
	m.config.agentMCP = agentMCPBindings{id: bindings[original]}
	if r := managerHTTP(m, prefix+"viewer", nil); r.Code != 404 {
		t.Fatal("old member still visible", r.Code)
	}
	if r := managerHTTP(m, "/v1/environment/"+environmentID(m.epoch, id)+"/viewer", nil); r.Code != 404 {
		t.Fatal("viewer attached to an unconfigured replacement member", r.Code)
	}
}

func TestEnvironmentProxyCredentialAndPathBoundaries(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-secret" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Forwarded-Host") != "" || r.URL.RawQuery != "" {
			t.Error("forwarded human credentials or query")
		}
		if r.URL.Path != "/vnc/core/rfb.js" {
			t.Error("unexpected upstream path", r.URL.Path)
		}
		w.Header().Set("Set-Cookie", "upstream=secret")
		io.WriteString(w, "export default class RFB {}")
	}))
	defer upstream.Close()
	m, _, prefix := environmentFixture(t, upstream.URL)
	a, token, _ := accessFixture(t)
	h := a.wrap(m)
	for _, endpoint := range []string{"/v1/environments", prefix + "viewer", prefix + "vnc/core/rfb.js", prefix + "websockify"} {
		if r := accessRequest(h, "GET", endpoint, "", "", "", nil); r.Code != 401 {
			t.Fatal("anonymous environment access", r.Code)
		}
		if r := accessRequest(h, "GET", endpoint, token, "http://evil.invalid", "", nil); r.Code != 403 {
			t.Fatal("cross-origin environment access", r.Code)
		}
	}
	r := accessRequest(h, "GET", prefix+"vnc/core/rfb.js?api_key=human-secret", token, "", "", &http.Cookie{Name: "teamx", Value: "human-secret"})
	if r.Code != 200 || r.Header().Get("Set-Cookie") != "" || !strings.HasPrefix(r.Header().Get("Content-Type"), "text/javascript") {
		t.Fatal(r.Code, r.Header(), r.Body.String())
	}
	for _, asset := range []string{"vnc/index.html", "vnc/core/../rfb.js", "vnc/core/%2e%2e/rfb.js", "vnc/core/%252e%252e/rfb.js", "vnc/core/a.json", "vnc/core/a\\b.js", "mcp", "v1/sandbox"} {
		if r := accessRequest(h, "GET", prefix+asset, token, "", "", nil); r.Code != 404 {
			t.Fatal("arbitrary proxy path", asset, r.Code)
		}
	}
	execution, revoke, err := a.issueExecution(executionBinding{Caller: Caller{Author: Author{Kind: "agent", ID: "agent"}, MembershipID: "member"}})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	if r := accessRequest(h, "GET", prefix+"viewer", execution, "", "", nil); r.Code != 403 {
		t.Fatal("Agent viewer access", r.Code)
	}
}

func TestEnvironmentViewerAccessExpiresOnLogout(t *testing.T) {
	a, token, _ := accessFixture(t)
	var viewerRequest *http.Request
	h := a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		viewerRequest = r
		w.WriteHeader(200)
	}))
	cookie := accessLogin(t, h, token)
	if response := accessRequest(h, "GET", "/v1/environment/viewer", "", "", "", cookie); response.Code != 200 {
		t.Fatal(response.Code)
	}
	if !environmentAccessCurrent(viewerRequest) {
		t.Fatal("valid viewer session rejected")
	}
	if response := accessRequest(h, "POST", "/v1/access/logout", "", "http://127.0.0.1:8088", "", cookie); response.Code != 200 {
		t.Fatal(response.Code)
	}
	if environmentAccessCurrent(viewerRequest) {
		t.Fatal("viewer session survived logout")
	}
}

func TestEnvironmentWebSocketRelayAndMemberRevocation(t *testing.T) {
	closed := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/websockify" || r.Header.Get("Authorization") != "Bearer upstream-secret" {
			t.Error("bad stream upstream")
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		defer close(closed)
		hash := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(hash[:]))
		rw.Write(append([]byte{0x82, 12}, []byte("RFB 003.008\n")...))
		rw.Flush()
		io.Copy(io.Discard, rw)
	}))
	defer upstream.Close()
	m, id, prefix := environmentFixture(t, upstream.URL)
	server := httptest.NewServer(m)
	defer server.Close()
	u, _ := url.Parse(server.URL)
	conn, err := net.DialTimeout("tcp", u.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(8 * time.Second))
	fmt.Fprintf(conn, "GET %swebsockify HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", prefix, u.Host, server.URL)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil || response.StatusCode != 101 {
		t.Fatal(response, err)
	}
	frame := make([]byte, 14)
	if _, err := io.ReadFull(reader, frame); err != nil || string(frame[2:]) != "RFB 003.008\n" {
		t.Fatal("missing VNC frame", err)
	}
	m.mu.Lock()
	for i := range m.index.Rooms {
		if m.index.Rooms[i].RoomIdentity == id.RoomIdentity {
			m.index.Rooms[i].Status = "deleting"
		}
	}
	m.mu.Unlock()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("revoked viewer stream remained open")
	}
	if len(environmentStreams) != 0 { // Handler teardown may trail upstream EOF.
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		for len(environmentStreams) != 0 {
			select {
			case <-ctx.Done():
				t.Fatal("stream slot leaked")
			case <-time.After(time.Millisecond):
			}
		}
	}
}
