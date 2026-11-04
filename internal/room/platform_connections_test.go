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
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func platformTestRequest(m *roomManager) platformConnectionRequest {
	return platformConnectionRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("a", 32), Name: "Company GitLab", Provider: "gitlab", URL: "https://gitlab.invalid/", PAT: "test-platform-secret"}
}
func platformTestSave(t *testing.T, m *roomManager, req platformConnectionRequest) platformConnection {
	t.Helper()
	response := managerHTTP(m, "/v1/platform/connections/save", req)
	var c platformConnection
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &c) != nil {
		t.Fatal(response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "test-platform-secret") {
		t.Fatal("API exposed PAT")
	}
	return c
}
func platformTestList(t *testing.T, m *roomManager) []platformConnection {
	t.Helper()
	response := managerHTTP(m, "/v1/platform/connections?service_epoch="+m.epoch+"&caller_id=tester", nil)
	var list struct {
		Connections []platformConnection `json:"connections"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &list) != nil {
		t.Fatal(response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "test-platform-secret") {
		t.Fatal("list exposed PAT")
	}
	return list.Connections
}

func TestPlatformConnectionsPersistenceIsolationAndRotation(t *testing.T) {
	dir := t.TempDir()
	profiles := managerTestProfiles(t)
	m := testManager(t, dir, profiles)
	room := managerCreate(t, m, "unmodified-room")
	configPath := filepath.Join(m.roomDir(room.RoomID), "room-config.json")
	before, _ := os.ReadFile(configPath)
	req := platformTestRequest(m)
	c := platformTestSave(t, m, req)
	if c.Version != 1 || c.CredentialVersion != 1 || c.URL != "https://gitlab.invalid" || c.Validation.Status != "unverified" {
		t.Fatal(c)
	}
	m.platformVerify = func(_ context.Context, origin string, secret workspaceSecret) platformConnectionValidation {
		if origin != c.URL || secret.value != req.PAT {
			t.Error("wrong saved credential or origin")
		}
		return platformConnectionValidation{Status: "valid", UserID: 101, Username: "tester", CheckedAt: time.Now().UTC()}
	}
	verify := platformConnectionRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: c.ID, Version: c.Version}
	response := managerHTTP(m, "/v1/platform/connections/verify", verify)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	c = platformTestList(t, m)[0]
	if c.Version != 2 || c.Validation.Username != "tester" {
		t.Fatal(c)
	}
	req.Version = c.Version
	req.PAT = ""
	req.Name = "Renamed"
	c = platformTestSave(t, m, req)
	if c.CredentialVersion != 1 || c.Validation.Status != "valid" {
		t.Fatal("metadata edit reset credential", c)
	}
	stale := req
	stale.PAT = "replacement" // req version is now stale.
	if r := managerHTTP(m, "/v1/platform/connections/save", stale); r.Code != 409 {
		t.Fatal(r.Code, r.Body.String())
	}
	req.Version = c.Version
	req.PAT = "test-platform-secret-replacement"
	c = platformTestSave(t, m, req)
	if c.CredentialVersion != 2 || c.Validation.Status != "unverified" {
		t.Fatal(c)
	}
	file, err := m.readPlatformConnections()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%+v", file), req.PAT) {
		t.Fatal("format exposed PAT")
	}
	secret, err := resolvePlatformCredential(file, file.Connections[0], c.Version, c.CredentialVersion)
	if err != nil || secret.value != req.PAT {
		t.Fatal("resolve failed", err)
	}
	if _, err = resolvePlatformCredential(file, file.Connections[0], c.Version, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("stale credential accepted")
	}
	info, err := os.Stat(filepath.Join(dir, "platform-connections.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private permissions", err)
	}
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Fatal("connection management modified room")
	}
	if err = m.close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newRoomManager(dir, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	if got := platformTestList(t, reopened); len(got) != 1 || got[0].CredentialVersion != 2 {
		t.Fatal(got)
	}
	// Shared records are outside the room-owned credential cleanup lifecycle.
	if err = reopened.cleanupWorkspaceCredentials(room.RoomIdentity); err != nil {
		t.Fatal(err)
	}
	if len(platformTestList(t, reopened)) != 1 {
		t.Fatal("room cleanup deleted shared connection")
	}
}

func TestPlatformConnectionEnvironmentAndHTTPGuards(t *testing.T) {
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	req := platformTestRequest(m)
	req.PAT = ""
	req.TokenEnv = "TEAMX_TEST_CONNECTION_PAT"
	t.Setenv(req.TokenEnv, "")
	c := platformTestSave(t, m, req)
	verify := platformConnectionRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: c.ID, Version: c.Version}
	response := managerHTTP(m, "/v1/platform/connections/verify", verify)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "credential_unavailable") {
		t.Fatal(response.Code, response.Body.String())
	}
	file, _ := m.readPlatformConnections()
	record := file.Connections[0]
	t.Setenv(req.TokenEnv, "test-platform-secret")
	secret, err := resolvePlatformCredential(file, record, record.Version, record.CredentialVersion)
	if err != nil || secret.value != "test-platform-secret" {
		t.Fatal("env resolution failed")
	}
	data, _ := os.ReadFile(filepath.Join(m.store.dir, "platform-connections.json"))
	if strings.Contains(string(data), secret.value) {
		t.Fatal("env value persisted")
	}
	for _, change := range []func(*platformConnectionRequest){
		func(r *platformConnectionRequest) { r.ServiceEpoch = "stale" },
		func(r *platformConnectionRequest) {
			r.Caller = Caller{Kind: "agent", ID: "developer", MembershipID: "member"}
		},
		func(r *platformConnectionRequest) { r.Caller.MembershipID = "member" },
	} {
		invalid := req
		change(&invalid)
		if r := managerHTTP(m, "/v1/platform/connections/save", invalid); r.Code != 403 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	for _, change := range []func(*platformConnectionRequest){
		func(r *platformConnectionRequest) { r.URL = "https://user:secret@gitlab.invalid" },
		func(r *platformConnectionRequest) { r.URL = "http://gitlab.invalid" },
		func(r *platformConnectionRequest) { r.URL = "https://gitlab.invalid/path" },
		func(r *platformConnectionRequest) { r.Provider = "github" },
		func(r *platformConnectionRequest) { r.PAT = "test-platform-secret" },
		func(r *platformConnectionRequest) { r.TokenEnv = "BAD=ENV" },
		func(r *platformConnectionRequest) { r.URL = "https://other.invalid" },
	} {
		invalid := req
		invalid.Version = record.Version
		change(&invalid)
		if r := managerHTTP(m, "/v1/platform/connections/save", invalid); r.Code != 400 || strings.Contains(r.Body.String(), "test-platform-secret") {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	cross := httptest.NewRequest("POST", "/v1/platform/connections/save", strings.NewReader(`{}`))
	cross.Header.Set("Origin", "http://other.invalid")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, cross)
	if w.Code != 403 {
		t.Fatal("cross origin accepted")
	}
	if r := managerHTTP(m, "/v1/platform/connections/save", map[string]any{"pat": "test-platform-secret", "unknown": true}); r.Code != 400 || strings.Contains(r.Body.String(), "test-platform-secret") {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestPlatformConnectionDropsLateValidation(t *testing.T) {
	for _, action := range []string{"rotate", "delete", "env", "recreate"} {
		t.Run(action, func(t *testing.T) {
			m := testManager(t, t.TempDir(), managerTestProfiles(t))
			req := platformTestRequest(m)
			if action == "env" {
				req.PAT = ""
				req.TokenEnv = "TEAMX_LATE_CONNECTION_PAT"
				t.Setenv(req.TokenEnv, "initial-token")
			}
			c := platformTestSave(t, m, req)
			started, release := make(chan struct{}), make(chan struct{})
			m.platformVerify = func(context.Context, string, workspaceSecret) platformConnectionValidation {
				close(started)
				<-release
				return platformConnectionValidation{Status: "valid", UserID: 101, Username: "old-user", CheckedAt: time.Now().UTC()}
			}
			result := make(chan error, 1)
			go func() {
				_, err := m.verifyPlatformConnection(context.Background(), platformConnectionRequest{ID: c.ID, Version: c.Version})
				result <- err
			}()
			<-started
			if action == "env" {
				t.Setenv(req.TokenEnv, "new-token")
			} else {
				req.Version = c.Version
				req.PAT = "replacement-token"
				if _, err := m.updatePlatformConnection(req, action == "delete" || action == "recreate"); err != nil {
					t.Fatal(err)
				}
			}
			if action == "recreate" {
				req.Version = 0
				req.PAT = "test-platform-secret"
				req.URL = "https://other.invalid"
				platformTestSave(t, m, req)
			}
			close(release)
			if err := <-result; !errors.Is(err, ErrConflict) {
				t.Fatal("late validation accepted", err)
			}
			items := platformTestList(t, m)
			if len(items) > 0 && items[0].Validation.Status != "unverified" {
				t.Fatal("late result stored")
			}
		})
	}
}

func TestPlatformConnectionsRejectUnsafeStorageAndUnknownCommit(t *testing.T) {
	for _, kind := range []string{"malformed", "permissions", "symlink", "commit_unknown"} {
		t.Run(kind, func(t *testing.T) {
			m := testManager(t, t.TempDir(), managerTestProfiles(t))
			req := platformTestRequest(m)
			platformTestSave(t, m, req)
			name := filepath.Join(m.store.dir, "platform-connections.json")
			switch kind {
			case "malformed":
				if err := os.WriteFile(name, []byte(`{"test-platform-secret":`), 0600); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(name, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				data, _ := os.ReadFile(name)
				target := filepath.Join(t.TempDir(), "target.json")
				if err := os.WriteFile(target, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, name); err != nil {
					t.Fatal(err)
				}
			case "commit_unknown":
				m.store.ops.syncDir = func(string) error { return errors.New("injected directory sync failure") }
				req.Version = 1
				req.Name = "Uncertain"
				if _, err := m.updatePlatformConnection(req, false); !errors.Is(err, ErrCommitUnknown) {
					t.Fatal(err)
				}
			}
			req.Version = 1
			r := managerHTTP(m, "/v1/platform/connections/save", req)
			if r.Code == 200 || strings.Contains(r.Body.String(), req.PAT) {
				t.Fatal("unsafe write or secret diagnostic", r.Code, r.Body.String())
			}
		})
	}
}

func TestPlatformConnectionsAppHTTP(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required")
	}
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	m.platformVerify = func(_ context.Context, origin string, secret workspaceSecret) platformConnectionValidation {
		if origin != "https://gitlab.invalid" || secret.value != "http-only-test-token" {
			t.Error("wrong HTTP credential/target")
		}
		return platformConnectionValidation{Status: "valid", UserID: 101, Username: "http-user", CheckedAt: time.Now().UTC()}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: m}}
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "web_platform_connections_http.mjs")
	cmd.Env = append(os.Environ(), "TEAMX_WEB_ENDPOINT="+server.URL)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("UI/HTTP: %v\n%s", err, output)
	}
	if *webPreviewDuration > 0 {
		// Browser preview gets only synthetic credentials and its own temporary root.
		m.mu.Lock()
		m.platformVerify = func(context.Context, string, workspaceSecret) platformConnectionValidation {
			return platformConnectionValidation{Status: "valid", UserID: 101, Username: "preview-user", CheckedAt: time.Now().UTC()}
		}
		m.mu.Unlock()
		t.Log("isolated platform browser preview:", server.URL)
		<-time.After(*webPreviewDuration)
	}
}

// The real application drives the Go HTTP handler through a JSON pipe so this
// integration gate can run even on hosts that prohibit loopback listeners.
// It does not claim to exercise TCP or browser layout.
func TestPlatformConnectionsAppHandler(t *testing.T) {
	_, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required")
	}
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	m.platformVerify = func(_ context.Context, origin string, secret workspaceSecret) platformConnectionValidation {
		if origin != "https://gitlab.invalid" || secret.value != "http-only-test-token" {
			t.Error("wrong handler credential/target")
		}
		return platformConnectionValidation{Status: "valid", UserID: 101, Username: "http-user", CheckedAt: time.Now().UTC()}
	}
	runAppHandler(t, m, "web_platform_connections_http.mjs")
}

func runAppHandler(t *testing.T, m *roomManager, script string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, script)
	cmd.Env = append(os.Environ(), "TEAMX_WEB_TRANSPORT=stdio")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	decoder, encoder := json.NewDecoder(output), json.NewEncoder(input)
	for {
		var call struct {
			ID   uint64          `json:"id"`
			Path string          `json:"path"`
			Body json.RawMessage `json:"body"`
		}
		if err = decoder.Decode(&call); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		method := "GET"
		if len(call.Body) > 0 && string(call.Body) != "null" {
			method = "POST"
		}
		r := httptest.NewRequest(method, call.Path, bytes.NewReader(call.Body))
		w := httptest.NewRecorder()
		m.ServeHTTP(w, r)
		if err = encoder.Encode(struct {
			ID     uint64          `json:"id"`
			Status int             `json:"status"`
			Body   json.RawMessage `json:"body"`
		}{call.ID, w.Code, w.Body.Bytes()}); err != nil {
			input.Close()
			_ = cmd.Wait()
			t.Fatalf("app/handler response: %v\n%s", err, diagnostics.String())
		}
	}
	if err = cmd.Wait(); err != nil {
		t.Fatalf("app/handler: %v\n%s", err, diagnostics.String())
	}
}

func TestPlatformConnectionsIndependentCredentialsAndSourceSwitch(t *testing.T) {
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	req := platformTestRequest(m)
	first := platformTestSave(t, m, req)
	req.ID = strings.Repeat("b", 32)
	second := platformTestSave(t, m, req) // Same name/token deliberately remain separate.
	file, err := m.readPlatformConnections()
	if err != nil || len(file.Connections) != 2 || len(file.Credentials) != 2 {
		t.Fatal("connections merged", err)
	}
	if file.Connections[0].CredentialID == file.Connections[1].CredentialID {
		t.Fatal("credentials merged")
	}
	req.ID = first.ID
	req.Version = first.Version
	req.PAT = ""
	req.TokenEnv = "TEAMX_SOURCE_SWITCH_PAT"
	t.Setenv(req.TokenEnv, "from-environment")
	first = platformTestSave(t, m, req)
	file, err = m.readPlatformConnections()
	if err != nil || len(file.Credentials) != 1 || first.CredentialVersion != 2 || first.CredentialKind != "env" {
		t.Fatal("source switch failed", err)
	}
	for _, cred := range file.Credentials {
		if cred.ConnectionID == first.ID {
			t.Fatal("old PAT retained")
		}
	}
	secret, err := resolvePlatformCredential(file, file.Connections[0], first.Version, first.CredentialVersion)
	if err != nil || secret.value != "from-environment" {
		t.Fatal("new environment source unresolved", err)
	}
	req.ID = second.ID
	req.Version = second.Version
	if r := managerHTTP(m, "/v1/platform/connections/delete", platformConnectionRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: second.ID, Version: second.Version}); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	file, err = m.readPlatformConnections()
	if err != nil || len(file.Connections) != 1 || len(file.Credentials) != 0 {
		t.Fatal("deletion retained PAT or affected other connection", err)
	}
}

func TestPlatformConnectionStableIdentityPublicAndReload(t *testing.T) {
	dir := t.TempDir()
	profiles := managerTestProfiles(t)
	m := testManager(t, dir, profiles)
	req := platformTestRequest(m)
	req.PAT, req.TokenEnv = "", "TEAMX_PUBLIC_IDENTITY_TOKEN"
	t.Setenv(req.TokenEnv, "test-platform-secret")
	c := platformTestSave(t, m, req)
	m.platformVerify = func(context.Context, string, workspaceSecret) platformConnectionValidation {
		return platformConnectionValidation{Status: "valid", UserID: 91, Username: "verified-user", CheckedAt: time.Now().UTC()}
	}
	response := managerHTTP(m, "/v1/platform/connections/verify", platformConnectionRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: c.ID, Version: c.Version})
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"user_id":91`) || strings.Contains(response.Body.String(), "fingerprint") || strings.Contains(response.Body.String(), "test-platform-secret") {
		t.Fatal(response.Code, response.Body.String())
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	m = testManager(t, dir, profiles)
	listed := platformTestList(t, m)
	if len(listed) != 1 || listed[0].Validation.UserID != 91 || listed[0].Validation.Status != "valid" {
		t.Fatal("identity not persisted")
	}
	t.Setenv(req.TokenEnv, "replacement-platform-secret")
	listed = platformTestList(t, m)
	if listed[0].Validation.Status != "unverified" || listed[0].Validation.UserID != 0 {
		t.Fatal("changed credential retained public verified identity")
	}
}
