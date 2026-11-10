//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/tool"
)

func TestCLIDuplexStreamsAndExitWithoutWorkspace(t *testing.T) {
	parallelFixtureTest(t)
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
	err := runToolsWithClient(context.Background(), []string{"run", c.ToolEntryID, "api", "a b", "--", "$HOME"}, bytes.NewBufferString("stdin\x00data"), &out, &errOut, env, client)
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
	refs, err := m.managedPlatformReferencesLocked(c.ID)
	m.mu.Unlock()
	if err != nil || len(refs) != 1 {
		t.Fatal(refs, err)
	}
}

func TestHelpSkipsCredentialAndDisableJoins(t *testing.T) {
	parallelFixtureTest(t)
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
	file, _ := m.platforms.Load()
	old := file.OAuthCredentials[0].State
	file.OAuthCredentials[0].State = "reauthorization_required"
	_ = m.store.save(filepath.Join(m.store.dir, "platform-connections.json"), file, 1<<20)
	m.mu.Unlock()
	client, endpoint := toolPipeServer(t, m)
	env := cliToolEnv(endpoint, svc, members[0])
	var out, errOut bytes.Buffer
	if err := runToolsWithClient(context.Background(), []string{"run", c.ToolEntryID, "--help"}, bytes.NewReader(nil), &out, &errOut, env, client); err != nil || !strings.Contains(out.String(), "extension help") {
		t.Fatal(err, out.String(), errOut.String())
	}
	m.mu.Lock()
	file.OAuthCredentials[0].State = old
	_ = m.store.save(filepath.Join(m.store.dir, "platform-connections.json"), file, 1<<20)
	m.mu.Unlock()
	// Read the first output before stdin EOF, then disable and join the process.
	req := toolsRequest{ServiceInfo: svc.info, Caller: agentCaller(members[0].AgentID, members[0].MembershipID), Tool: c.ToolEntryID, Args: []string{"api"}}
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
	changed := managerHTTP(m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, Revision: f.Revision, Binding: tool.Binding{AgentID: members[0].AgentID, MembershipID: members[0].MembershipID, Tool: "gitlab", PlatformID: c.ID}})
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
	parallelFixtureTest(t)
	m, room, svc, c, members := cliToolFixture(t, "printf local\n")
	f := bindCLITool(t, m, room.RoomIdentity, members[0], managedPlatform{}, "local", 0)
	client, endpoint := toolPipeServer(t, m)
	var out, errOut bytes.Buffer
	if err := runToolsWithClient(context.Background(), []string{"run", "local"}, bytes.NewReader(nil), &out, &errOut, cliToolEnv(endpoint, svc, members[0]), client); err != nil || out.String() != "local" {
		t.Fatal(err, out.String())
	}
	req := toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, Revision: 0, Binding: tool.Binding{AgentID: members[0].AgentID, MembershipID: members[0].MembershipID, Tool: "gitlab", PlatformID: c.ID}, Enabled: true, PlatformVersion: c.Version}
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
	rotated, err := testReauthorizePlatform(m, change)
	if err != nil {
		t.Fatal(err)
	}
	rotated = verifyTodoAccount(t, m, rotated, 101)
	m.mu.Lock()
	p, call, _, err := m.prepareToolLocked(context.Background(), toolsRequest{ServiceInfo: svc.info, Caller: agentCaller(members[0].AgentID, members[0].MembershipID), Tool: c.ToolEntryID, Args: []string{"api", "user"}})
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
	parallelFixtureTest(t)
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
	err = runToolsWithClient(context.Background(), []string{"run", c.ToolEntryID, "api", "write"}, bytes.NewReader(nil), &out, &errOut, cliToolEnv(endpoint, svc, members[0]), client)
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
	parallelFixtureTest(t)
	m, room, svc, c, members := cliToolFixture(t, "exit 99\n")
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	m.toolCatalog[0].Executable = filepath.Join(t.TempDir(), "missing-tool")
	client, endpoint := toolPipeServer(t, m)
	var out, errOut bytes.Buffer
	err := runToolsWithClient(context.Background(), []string{"run", c.ToolEntryID, "--help"}, bytes.NewReader(nil), &out, &errOut, cliToolEnv(endpoint, svc, members[0]), client)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 127 || out.Len() != 0 {
		t.Fatal(err, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "unavailable on TeamX server") || !strings.Contains(errOut.String(), "restart TeamX") || strings.Contains(errOut.String(), "test-platform-secret") {
		t.Fatal(errOut.String())
	}
}

// Opt-in offline check of the independently built GitLab wrapper through the
// tool launcher. TEAMX_GLAB_BIN selects the pinned real dependency.
func TestIndependentGitLabHelp(t *testing.T) {
	parallelFixtureTest(t)
	bin := os.Getenv("TEAMX_TEST_GITLAB_BIN")
	if bin == "" {
		t.Skip("set TEAMX_TEST_GITLAB_BIN to the independently built GitLab tool")
	}
	m, room, svc, c, members := cliToolFixture(t, "exit 99\n")
	m.toolCatalog[0] = tool.Catalog()[0]
	m.toolCatalog[0].Executable = bin
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	// Help resolves entry metadata without requiring an active authorization.
	m.mu.Lock()
	file, err := m.platforms.Load()
	if err == nil {
		file.OAuthCredentials[0].State = "reauthorization_required"
		err = m.store.save(filepath.Join(m.store.dir, "platform-connections.json"), file, 1<<20)
	}
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	client, endpoint := toolPipeServer(t, m)
	for _, path := range []string{"", "issue update", "work-items", "work-items close", "work-items discussions", "issue discussions reply"} {
		t.Run(path, func(t *testing.T) {
			args := append([]string{"run", c.ToolEntryID}, strings.Fields(path)...)
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

func TestProcessLifecycleSettlement(t *testing.T) {
	parallelFixtureTest(t)
	for _, action := range []string{"rotation", "deletion", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			m, room, svc, c, members := cliToolFixture(t, "printf ready\nsleep 30\n")
			bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
			m.mu.Lock()
			p, call, ctx, err := m.prepareToolLocked(context.Background(), toolsRequest{ServiceInfo: svc.info, Caller: agentCaller(members[0].AgentID, members[0].MembershipID), Tool: c.ToolEntryID, Args: []string{"api"}})
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
				if _, err := testReauthorizePlatform(m, req); err != nil {
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
	parallelFixtureTest(t)
	m, room, svc, c, members := cliToolFixture(t, "printf ready\nsleep 30\n")
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	client, endpoint := toolPipeServer(t, m)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := &toolReadyWriter{started: make(chan struct{})}
	var stderr bytes.Buffer
	result := make(chan error, 1)
	go func() {
		result <- runToolsWithClient(ctx, []string{"run", c.ToolEntryID, "api"}, bytes.NewReader(nil), ready, &stderr, cliToolEnv(endpoint, svc, members[0]), client)
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
	parallelFixtureTest(t)
	m, _, svc, c, members := cliToolFixture(t, "exit 99\n")
	client, endpoint := toolPipeServer(t, m)
	input, feed := io.Pipe()
	defer input.Close()
	defer feed.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out, stderr bytes.Buffer
	err := runToolsWithClient(ctx, []string{"run", c.ToolEntryID, "api"}, input, &out, &stderr, cliToolEnv(endpoint, svc, members[0]), client)
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
