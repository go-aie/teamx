package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/tool"
)

func cliToolFixture(t *testing.T, body string) (*roomManager, roomSummary, *service, platformConnection, []MemberState) {
	t.Helper()
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
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
func TestCLIDuplexStreamsAndExitWithoutWorkspace(t *testing.T) {
	m, room, svc, c, members := cliToolFixture(t, `test "$GITLAB_TOKEN" = test-platform-secret || exit 90
 test "$GITLAB_HOST" = https://gitlab.invalid || exit 91
 printf '%s\n' "$@"
 cat
 printf 'stderr-data' >&2
 exit 37
`)
	f := bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	if len(f.Bindings) != 1 {
		t.Fatal(f)
	}
	client, endpoint := toolPipeServer(t, m)
	env := cliToolEnv(endpoint, svc, members[0])
	var out, errOut bytes.Buffer
	if err := runToolsWithClient(context.Background(), []string{"list"}, nil, &out, &errOut, env, client); err != nil || !strings.Contains(out.String(), "gitlab.invalid") || strings.Contains(out.String(), "secret") {
		t.Fatal(err, out.String())
	}
	out.Reset()
	err := runToolsWithClient(context.Background(), []string{"run", "gitlab", "api", "a b", "--", "$HOME"}, bytes.NewBufferString("stdin\x00data"), &out, &errOut, env, client)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 37 || out.String() != "api\na b\n--\n$HOME\nstdin\x00data" || errOut.String() != "stderr-data" {
		t.Fatal(err, out.String(), errOut.String())
	}
	// Each member must explicitly select its own binding.
	out.Reset()
	if err := runToolsWithClient(context.Background(), []string{"list"}, nil, &out, &errOut, cliToolEnv(endpoint, svc, members[1]), client); err != nil || strings.TrimSpace(out.String()) != "[]" {
		t.Fatal(err, out.String())
	}
	disk, err := os.ReadFile(m.toolBindingsPath(room.RoomIdentity))
	if err != nil || bytes.Contains(disk, []byte("secret")) {
		t.Fatal(err, string(disk))
	}
	m.mu.Lock()
	refs, err := m.platformConnectionReferencesLocked(c.ID)
	m.mu.Unlock()
	if err != nil || len(refs) != 1 {
		t.Fatal(refs, err)
	}
}
func TestHelpSkipsCredentialAndDisableJoins(t *testing.T) {
	m, room, svc, c, members := cliToolFixture(t, `if test "$1" = --help; then
 test -z "$GITLAB_TOKEN$GITLAB_HOST" || exit 90
 printf 'native and extension help'; exit 0
 fi
 printf 'started'
 sleep 30
`)
	f := bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	// Break credential resolution after binding: help must still work.
	m.mu.Lock()
	file, _ := m.connections.Load()
	old := file.OAuthCredentials[0].State
	file.OAuthCredentials[0].State = "reauthorization_required"
	_ = m.store.save(filepath.Join(m.store.dir, "platform-connections.json"), file, 1<<20)
	m.mu.Unlock()
	client, endpoint := toolPipeServer(t, m)
	env := cliToolEnv(endpoint, svc, members[0])
	var out, errOut bytes.Buffer
	if err := runToolsWithClient(context.Background(), []string{"run", "gitlab", "--help"}, bytes.NewReader(nil), &out, &errOut, env, client); err != nil || !strings.Contains(out.String(), "extension help") {
		t.Fatal(err, out.String(), errOut.String())
	}
	m.mu.Lock()
	file.OAuthCredentials[0].State = old
	_ = m.store.save(filepath.Join(m.store.dir, "platform-connections.json"), file, 1<<20)
	m.mu.Unlock()
	// Read the first output before stdin EOF, then disable and join the process.
	req := toolsRequest{ServiceInfo: svc.info, Caller: agentCaller(members[0].AgentID, members[0].MembershipID), Tool: "gitlab", Args: []string{"api"}}
	data, _ := json.Marshal(req)
	upload, send := io.Pipe()
	defer send.Close()
	go func() { _, _ = send.Write(append(data, '\n')) }()
	request, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/tools/run", upload)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	d := json.NewDecoder(response.Body)
	var frame toolFrame
	if err := d.Decode(&frame); err != nil || string(frame.Data) != "started" {
		t.Fatal(err, frame)
	}
	exitResult := make(chan toolFrame, 1)
	go func() {
		for frame.Stream != "exit" {
			if err := d.Decode(&frame); err != nil {
				exitResult <- toolFrame{Code: -1}
				return
			}
		}
		exitResult <- frame
	}()
	changed := managerHTTP(m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, Revision: f.Revision, Binding: tool.Binding{AgentID: members[0].AgentID, MembershipID: members[0].MembershipID, Tool: "gitlab"}})
	if changed.Code != 200 {
		t.Fatal(changed.Code, changed.Body.String())
	}
	frame = <-exitResult

	if frame.Code != 130 {
		t.Fatal(frame)
	}
	m.mu.Lock()
	pending := len(m.platformCalls)
	m.mu.Unlock()
	if pending != 0 {
		t.Fatal(pending)
	}
}
func TestBindingCASAndAccountlessTool(t *testing.T) {
	m, room, svc, c, members := cliToolFixture(t, "printf local\n")
	f := bindCLITool(t, m, room.RoomIdentity, members[0], platformConnection{}, "local", 0)
	client, endpoint := toolPipeServer(t, m)
	var out, errOut bytes.Buffer
	if err := runToolsWithClient(context.Background(), []string{"run", "local"}, bytes.NewReader(nil), &out, &errOut, cliToolEnv(endpoint, svc, members[0]), client); err != nil || out.String() != "local" {
		t.Fatal(err, out.String())
	}
	req := toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, Revision: 0, Binding: tool.Binding{AgentID: members[0].AgentID, MembershipID: members[0].MembershipID, Tool: "gitlab", ConnectionID: c.ID}, Enabled: true, ConnectionVersion: c.Version}
	if r := managerHTTP(m, "/v1/tools/bind", req); r.Code == 200 {
		t.Fatal("stale revision accepted")
	}
	req.Revision = f.Revision
	req.Binding.MembershipID = "stale"
	if r := managerHTTP(m, "/v1/tools/bind", req); r.Code == 200 {
		t.Fatal("stale member accepted")
	}
	// Every call sees the latest explicitly verified replacement credential.
	f = bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", f.Revision)
	change := platformTestRequest(m)
	change.ID, change.Version = c.ID, c.Version
	change.ReferencedRooms = []RoomIdentity{room.RoomIdentity}
	rotated, err := testReauthorizeConnection(m, change)
	if err != nil {
		t.Fatal(err)
	}
	rotated = verifyTodoAccount(t, m, rotated, 101)
	m.mu.Lock()
	p, call, _, err := m.prepareToolLocked(context.Background(), toolsRequest{ServiceInfo: svc.info, Caller: agentCaller(members[0].AgentID, members[0].MembershipID), Tool: "gitlab", Args: []string{"api", "user"}})
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	defer m.finishPlatformCall(call)
	if p.Values["token"] != "" || call.account.Authorization.CredentialID == "" {
		t.Fatal("stale credential")
	}
	_ = rotated
}
func TestNoRetryOnFailureAndWorkspaceFiles(t *testing.T) {
	m, room, svc, c, members := cliToolFixture(t, "cat input.txt\nprintf x >> calls\nexit 9\n")
	entry, _ := m.workspaceRoom(room.RoomIdentity)
	saved, err := m.readWorkspaceRoomConfig(entry)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(m.roomDir(room.RoomID), saved.Members[0].Workspace)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "input.txt"), []byte("file-content"), 0600); err != nil {
		t.Fatal(err)
	}
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	client, endpoint := toolPipeServer(t, m)
	var out, errOut bytes.Buffer
	err = runToolsWithClient(context.Background(), []string{"run", "gitlab", "api", "write"}, bytes.NewReader(nil), &out, &errOut, cliToolEnv(endpoint, svc, members[0]), client)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 9 || out.String() != "file-content" {
		t.Fatal(err, out.String(), errOut.String())
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if string(calls) != "x" {
		t.Fatal(fmt.Sprintf("calls=%q", calls))
	}
}

func TestMissingToolReturnsServerGuidance(t *testing.T) {
	m, room, svc, c, members := cliToolFixture(t, "exit 99\n")
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	m.toolCatalog[0].Executable = filepath.Join(t.TempDir(), "missing-tool")
	client, endpoint := toolPipeServer(t, m)
	var out, errOut bytes.Buffer
	err := runToolsWithClient(context.Background(), []string{"run", "gitlab", "--help"}, bytes.NewReader(nil), &out, &errOut, cliToolEnv(endpoint, svc, members[0]), client)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 127 || out.Len() != 0 {
		t.Fatal(err, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "unavailable on TeamX server") || !strings.Contains(errOut.String(), "restart TeamX") || strings.Contains(errOut.String(), "test-platform-secret") {
		t.Fatal(errOut.String())
	}
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

// Opt-in offline check of the independently built GitLab wrapper through the
// tool launcher. TEAMX_GLAB_BIN selects the pinned real dependency.
func TestIndependentGitLabHelp(t *testing.T) {
	bin := os.Getenv("TEAMX_TEST_GITLAB_BIN")
	if bin == "" {
		t.Skip("set TEAMX_TEST_GITLAB_BIN to the independently built GitLab tool")
	}
	m, room, svc, c, members := cliToolFixture(t, "exit 99\n")
	m.toolCatalog[0] = tool.Catalog()[0]
	m.toolCatalog[0].Executable = bin
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	// A help request must not even need a resolvable connection snapshot.
	path := filepath.Join(m.store.dir, "platform-connections.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(path, original, 0600) })
	client, endpoint := toolPipeServer(t, m)
	for _, path := range []string{"", "issue update", "work-items", "work-items close", "work-items discussions", "issue discussions reply"} {
		t.Run(path, func(t *testing.T) {
			args := append([]string{"run", "gitlab"}, strings.Fields(path)...)
			args = append(args, "--help")
			var out, errOut bytes.Buffer
			if err := runToolsWithClient(context.Background(), args, bytes.NewReader(nil), &out, &errOut, cliToolEnv(endpoint, svc, members[0]), client); err != nil || out.Len() == 0 {
				t.Fatal(err, out.String(), errOut.String())
			}
			if path == "" || path == "work-items" {
				if !strings.Contains(out.String(), "Additional commands:") {
					t.Fatal("missing extensions")
				}
			}
		})
	}
}

type toolReadyWriter struct {
	started chan struct{}
	once    sync.Once
}

func (w *toolReadyWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	return len(p), nil
}
func TestProcessLifecycleSettlement(t *testing.T) {
	for _, action := range []string{"rotation", "deletion", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			m, room, svc, c, members := cliToolFixture(t, "printf ready\nsleep 30\n")
			bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
			m.mu.Lock()
			p, call, ctx, err := m.prepareToolLocked(context.Background(), toolsRequest{ServiceInfo: svc.info, Caller: agentCaller(members[0].AgentID, members[0].MembershipID), Tool: "gitlab", Args: []string{"api"}})
			m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			ready := &toolReadyWriter{started: make(chan struct{})}
			p.Output = ready
			p.Error = io.Discard
			result := make(chan int, 1)
			go func() { code := p.Run(ctx, []string{"api"}); m.finishPlatformCall(call); result <- code }()
			select {
			case <-ready.started:
			case <-time.After(3 * time.Second):
				t.Fatal("process did not start")
			}
			switch action {
			case "rotation":
				req := platformTestRequest(m)
				req.Version = c.Version

				req.ReferencedRooms = []RoomIdentity{room.RoomIdentity}
				if _, err := testReauthorizeConnection(m, req); err != nil {
					t.Fatal(err)
				}
			case "deletion":
				m.mu.Lock()
				_, err := m.startDeletion(room.RoomIdentity)
				done := m.runtimes[room.RoomIdentity].operationDone
				m.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("deletion did not join process")
				}
			case "shutdown":
				if err := m.close(); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case code := <-result:
				if code != 130 {
					t.Fatal(code)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("lifecycle did not join process")
			}
			m.mu.Lock()
			pending := len(m.platformCalls)
			m.mu.Unlock()
			if pending != 0 {
				t.Fatal(pending)
			}
		})
	}
}
func TestCLICancellationJoinsServerProcess(t *testing.T) {
	m, room, svc, c, members := cliToolFixture(t, "printf ready\nsleep 30\n")
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	client, endpoint := toolPipeServer(t, m)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := &toolReadyWriter{started: make(chan struct{})}
	var stderr bytes.Buffer
	result := make(chan error, 1)
	go func() {
		result <- runToolsWithClient(ctx, []string{"run", "gitlab", "api"}, bytes.NewReader(nil), ready, &stderr, cliToolEnv(endpoint, svc, members[0]), client)
	}()
	select {
	case <-ready.started:
	case <-time.After(3 * time.Second):
		t.Fatal("process did not start")
	}
	cancel()
	err := <-result
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 130 || !strings.Contains(stderr.String(), "unconfirmed") {
		t.Fatal(err, stderr.String())
	}
	m.mu.Lock()
	done := []<-chan struct{}{}
	for c := range m.platformCalls {
		done = append(done, c.done)
	}
	m.mu.Unlock()
	for _, ch := range done {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("client cancellation left process running")
		}
	}
}

func TestAdmissionFailureDoesNotWaitForStdinEOF(t *testing.T) {
	m, _, svc, _, members := cliToolFixture(t, "exit 99\n")
	client, endpoint := toolPipeServer(t, m)
	input, feed := io.Pipe()
	defer input.Close()
	defer feed.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out, stderr bytes.Buffer
	err := runToolsWithClient(ctx, []string{"run", "gitlab", "api"}, input, &out, &stderr, cliToolEnv(endpoint, svc, members[0]), client)
	if ctx.Err() != nil || err == nil {
		t.Fatal("admission waited for stdin", err)
	}
	m.mu.Lock()
	pending := len(m.platformCalls)
	m.mu.Unlock()
	if pending != 0 {
		t.Fatal(pending)
	}
}
