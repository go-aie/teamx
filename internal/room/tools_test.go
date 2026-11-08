package room

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func cliToolFixture(t *testing.T, body string) (*roomManager, roomSummary, *service, platformConnection, []MemberState) {
	t.Helper()
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	// Native CLI fixtures retain account queues and cancellation without remote pacing.
	m.larkClients = lark.NewClientFactory(0, 0, 0)
	room := managerCreate(t, m, "tools")
	c := verifyTodoAccount(t, m, platformTestSave(t, m, platformTestRequest(m)), 101)
	_, snapshot := readManagedRoom(t, m, room)
	svc := eventLiveService(t, &eventHarness{m: m, room: room})
	members := snapshot.Members
	bin := filepath.Join(t.TempDir(), "fake-tool")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	m.toolCatalog = []tool.Definition{{Name: "gitlab", Provider: "gitlab", Version: "fixture", Executable: bin, Environment: map[string]string{"GITLAB_TOKEN": "token", "GITLAB_HOST": "instance"}}, {Name: "local", Executable: bin, Version: "fixture"}}
	return m, room, svc, c, members
}
func bindCLITool(t *testing.T, m *roomManager, owner RoomIdentity, member MemberState, c platformConnection, name string, revision uint64) toolBindingsFile {
	t.Helper()
	b := tool.Binding{AgentID: member.AgentID, MembershipID: member.MembershipID, Tool: name}
	version := uint64(0)
	if c.ID != "" {
		b.ConnectionID = c.ID
		version = c.Version
	}
	r := managerHTTP(m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: owner, Caller: humanCaller, Revision: revision, Binding: b, Enabled: true, ConnectionVersion: version})
	var f toolBindingsFile
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &f) != nil {
		t.Fatal(r.Code, r.Body.String())
	}
	return f
}
func cliToolEnv(endpoint string, s *service, member MemberState) func(string) string {
	env := map[string]string{"TEAMX_ENDPOINT": endpoint, "TEAMX_ROOM_ID": s.info.RoomID, "TEAMX_ROOM_INSTANCE": s.info.RoomInstance, "TEAMX_SERVICE_EPOCH": s.info.ServiceEpoch, "TEAMX_AGENT_ID": member.AgentID, "TEAMX_MEMBERSHIP_ID": member.MembershipID}
	return func(k string) string { return env[k] }
}

// Real net/http HTTP/1 duplex on in-memory connections; no TCP listener.
// A separate opt-in test covers loopback where the environment permits it.
type toolPipeListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *toolPipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *toolPipeListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *toolPipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 80} }
func toolPipeServer(t *testing.T, h http.Handler) (*http.Client, string) {
	t.Helper()
	listener := &toolPipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
	server := &http.Server{Handler: h}
	go func() { _ = server.Serve(listener) }()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		client, peer := net.Pipe()
		select {
		case listener.conns <- peer:
			return client, nil
		case <-ctx.Done():
			client.Close()
			peer.Close()
			return nil, ctx.Err()
		}
	}}
	t.Cleanup(func() { transport.CloseIdleConnections(); server.Close(); listener.Close() })
	return &http.Client{Transport: transport}, "http://tool-pipe.invalid"
}

type toolReadyWriter struct {
	started chan struct{}
	once    sync.Once
}

func (w *toolReadyWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	return len(p), nil
}
