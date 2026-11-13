package room

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/pelletier/go-toml/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testAccessConfig(t *testing.T, home string) (startupConfig, string) {
	t.Helper()
	path := filepath.Join(home, "config.toml")
	if err := runAccessInit([]string{"--config", path}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	c, err := readStartupConfig(path, true, false)
	if err != nil {
		t.Fatal(err)
	}
	token, err := readAdminToken(adminTokenPath(c))
	if err != nil {
		t.Fatal(err)
	}
	return c, token
}
func accessFixture(t *testing.T) (*serverAccess, string, http.Handler) {
	t.Helper()
	c, token := testAccessConfig(t, t.TempDir())
	a, err := newServerAccess(c, "http://127.0.0.1:8088")
	if err != nil {
		t.Fatal(err)
	}
	return a, token, a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		if r.Body != nil {
			io.Copy(w, r.Body)
		}
	}))
}
func accessRequest(handler http.Handler, method, path, token, origin, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8088"+path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:45000"
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}
func accessLogin(t *testing.T, h http.Handler, token string) *http.Cookie {
	t.Helper()
	w := accessRequest(h, "POST", "/v1/access/login", "", "http://127.0.0.1:8088", `{"token":"`+token+`"}`, nil)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	if cookie.Value == token || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatal("bad session cookie", cookie.Name)
	}
	return cookie
}
func TestAccessAllPrivateRoutesAndCallerBinding(t *testing.T) {
	a, token, h := accessFixture(t)
	for _, path := range []string{"/v1/ui", "/v1/info", "/v1/rooms", "/v1/settings", "/v1/platforms", "/v1/platform/attachments", "/v1/member/activity", "/v1/tools/list", "/v1/setup", "/v1/models", "/v1/models/operation", "/ui/app.js", "/unknown"} {
		for _, bad := range []string{"", strings.Repeat("a", 43)} {
			if w := accessRequest(h, "GET", path, bad, "", "", nil); w.Code != 401 {
				t.Fatal(path, w.Code)
			}
		}
	}
	for _, caller := range []Caller{{Author: Author{Kind: "human", ID: "forged"}}, {Author: Author{Kind: "agent", ID: "agent"}, MembershipID: "membership"}} {
		body, _ := json.Marshal(apiRequest{Caller: caller})
		if w := accessRequest(h, "POST", "/v1/send", token, "", string(body), nil); w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	if w := accessRequest(h, "GET", "/v1/info?caller_id=forged", token, "", "", nil); w.Code != 403 {
		t.Fatal("forged query caller accepted", w.Code)
	}
	body := `{"caller":{"kind":"human","id":"` + a.operator + `"},"text":"untouched"}`
	w := accessRequest(h, "POST", "/v1/send", token, "", body, nil)
	if w.Code != 200 || w.Body.String() != body {
		t.Fatal("request bytes changed", w.Code)
	}
	if w := accessRequest(h, "POST", "/v1/send", token, "https://evil.invalid", body, nil); w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestAccessSessionLogoutExpiryRestartRotationAndLimit(t *testing.T) {
	a, token, h := accessFixture(t)
	cookie := accessLogin(t, h, token)
	if _, ok := a.sessions[sha256.Sum256([]byte(cookie.Value))]; !ok {
		t.Fatal("missing session hash")
	}
	body := `{"caller":{"kind":"human","id":"` + a.operator + `"}}`
	if w := accessRequest(h, "POST", "/v1/send", "", "", body, cookie); w.Code != 403 {
		t.Fatal("empty origin accepted", w.Code)
	}
	if w := accessRequest(h, "POST", "/v1/send", "", "http://127.0.0.1:8088", body, cookie); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := accessRequest(h, "POST", "/v1/access/logout", "", "http://127.0.0.1:8088", "{}", cookie); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := accessRequest(h, "GET", "/v1/ui", "", "", "", cookie); w.Code != 401 {
		t.Fatal("logout retained session")
	}
	cookie = accessLogin(t, h, token)
	now := time.Now()
	a.now = func() time.Time { return now.Add(25 * time.Hour) }
	if w := accessRequest(h, "GET", "/v1/ui", "", "", "", cookie); w.Code != 401 {
		t.Fatal("expired session accepted")
	}
	for range 18 {
		accessLogin(t, h, token)
	}
	if len(a.sessions) != 16 {
		t.Fatal("session limit", len(a.sessions))
	}
	c, newToken := testAccessConfig(t, t.TempDir())
	fresh, err := newServerAccess(c, a.internal)
	if err != nil {
		t.Fatal(err)
	}
	freshHandler := fresh.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	if w := accessRequest(freshHandler, "GET", "/v1/ui", "", "", "", cookie); w.Code != 401 {
		t.Fatal("session survived restart")
	}
	if w := accessRequest(freshHandler, "GET", "/v1/ui", token, "", "", nil); w.Code != 401 {
		t.Fatal("old token survived rotation")
	}
	if w := accessRequest(freshHandler, "GET", "/v1/ui", newToken, "", "", nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
}
func TestAccessLoginOriginAndRateLimit(t *testing.T) {
	a, token, h := accessFixture(t)
	if w := accessRequest(h, "POST", "/v1/access/login", "", "", `{"token":"`+token+`"}`, nil); w.Code != 403 {
		t.Fatal(w.Code)
	}
	for range 5 {
		if w := accessRequest(h, "POST", "/v1/access/login", "", a.internal, `{"token":"bad"}`, nil); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	if w := accessRequest(h, "POST", "/v1/access/login", "", a.internal, `{"token":"`+token+`"}`, nil); w.Code != 429 {
		t.Fatal(w.Code)
	}
	now := time.Now()
	a.now = func() time.Time { return now.Add(2 * time.Minute) }
	accessLogin(t, h, token)
}
func TestAccessExecutionScopeLifecycleAndNoFixedTTL(t *testing.T) {
	a, _, h := accessFixture(t)
	binding := executionBinding{ServiceInfo: ServiceInfo{RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: "instance"}, ServiceEpoch: "epoch"}, Caller: Caller{Author: Author{Kind: "agent", ID: "agent"}, MembershipID: "member"}}
	token, revoke, err := a.issueExecution(binding)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(binding)
	// Large elapsed time does not expire a live execution.
	a.now = func() time.Time { return time.Now().Add(365 * 24 * time.Hour) }
	for _, path := range []string{"/v1/read", "/v1/inbox/read", "/v1/tools/list", "/v1/actions/execute", "/v1/platforms/connect/start", "/v1/platforms/member/status", "/v1/platforms/chats"} {
		if w := accessRequest(h, "POST", path, token, "", string(body), nil); w.Code != 200 {
			t.Fatal(path, w.Code)
		}
	}
	for _, path := range []string{"/v1/rooms", "/v1/ui", "/v1/settings", "/v1/access/session", "/v1/setup", "/v1/platforms/gitlab/start"} {
		if w := accessRequest(h, "GET", path, token, "", "", nil); w.Code != 403 {
			t.Fatal(path, w.Code)
		}
	}
	for _, mutate := range []func(*executionBinding){func(b *executionBinding) { b.Caller = Caller{Author: Author{Kind: "human", ID: a.operator}} }, func(b *executionBinding) { b.RoomID = "other" }, func(b *executionBinding) { b.RoomInstance = "other" }, func(b *executionBinding) { b.ServiceEpoch = "other" }, func(b *executionBinding) { b.Caller.ID = "other" }, func(b *executionBinding) { b.Caller.MembershipID = "other" }} {
		changed := binding
		mutate(&changed)
		body, _ := json.Marshal(changed)
		if w := accessRequest(h, "POST", "/v1/read", token, "", string(body), nil); w.Code != 403 {
			t.Fatal("forged binding accepted", w.Code)
		}
	}
	revoke()
	if w := accessRequest(h, "POST", "/v1/read", token, "", string(body), nil); w.Code != 401 {
		t.Fatal("revoked execution accepted")
	}
}
func TestAccessCanonicalProxyAndSecureCookie(t *testing.T) {
	c, token := testAccessConfig(t, t.TempDir())
	c.Server.PublicBaseURL = "https://assistant.example.com"
	c.Server.TrustedProxies = []string{"127.0.0.1"}
	a, err := newServerAccess(c, "http://127.0.0.1:8088")
	if err != nil {
		t.Fatal(err)
	}
	h := a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			t.Error("canonical proxy rejected")
		}
	}))
	request := func(peer, host, proto, origin string) int {
		r := httptest.NewRequest("GET", "http://"+host+"/v1/ui", nil)
		r.RemoteAddr = peer
		r.Header.Set("Authorization", "Bearer "+token)
		if proto != "" {
			r.Header.Set("X-Forwarded-Proto", proto)
		}
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if got := request("127.0.0.1:1", "assistant.example.com", "https", a.public); got != 200 {
		t.Fatal(got)
	}
	for _, tc := range [][4]string{{"192.0.2.1:1", "assistant.example.com", "https", a.public}, {"127.0.0.1:1", "evil.invalid", "https", ""}, {"127.0.0.1:1", "assistant.example.com", "https, http", ""}, {"127.0.0.1:1", "assistant.example.com", "", ""}, {"127.0.0.1:1", "assistant.example.com", "https", "https://evil.invalid"}} {
		if got := request(tc[0], tc[1], tc[2], tc[3]); got != 403 {
			t.Fatal(tc, got)
		}
	}
	if got := request("127.0.0.1:1", "127.0.0.1:8088", "", ""); got != 200 {
		t.Fatal("internal CLI rejected", got)
	}
	m := &roomManager{config: serviceConfig{endpoint: a.internal, access: a}}
	redirect, err := m.gitlabRedirectLocked()
	if err != nil || redirect != a.public+gitlabCallbackPath {
		t.Fatal("OAuth did not use public origin", redirect, err)
	}
	cookie := a.cookie("value", 86400)
	if cookie.Name != "__Host-teamx-session" || !cookie.Secure {
		t.Fatal(cookie.Name)
	}
	for _, invalid := range []string{"http://assistant.example.com", "https://user@assistant.example.com", "https://assistant.example.com/", "https://assistant.example.com?x=1", "https://assistant.example.com/#x"} {
		c.Server.PublicBaseURL = invalid
		if _, err := newServerAccess(c, a.internal); err == nil {
			t.Fatal(invalid)
		}
	}
}
func TestAccessInitPreservesOperatorTokenAndPrivateFileChecks(t *testing.T) {
	home := t.TempDir()
	c, token := testAccessConfig(t, home)
	again, againToken := testAccessConfig(t, home)
	if c.Access.OperatorID != again.Access.OperatorID || token != againToken {
		t.Fatal("initialization replaced identity")
	}
	path := adminTokenPath(c)
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readAdminToken(path); err == nil {
		t.Fatal("public token accepted")
	}
	os.Chmod(path, 0600)
	link := filepath.Join(home, "link")
	os.Symlink(path, link)
	if _, err := readAdminToken(link); err == nil {
		t.Fatal("symlink token accepted")
	}
	os.WriteFile(path, []byte(strings.Repeat("a", 43)+"\n\n"), 0600)
	if _, err := readAdminToken(path); err == nil {
		t.Fatal("invalid encoding accepted")
	}
	var out bytes.Buffer
	if err := runAccessInit([]string{"--config", filepath.Join(t.TempDir(), "config.toml")}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), token) {
		t.Fatal("token printed")
	}
}
func TestAccessCLIUsesFixedHumanAndBearer(t *testing.T) {
	c, token := testAccessConfig(t, t.TempDir())
	var a *serverAccess
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/info" {
				writeJSON(w, ServiceInfo{RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: "instance"}, ServiceEpoch: "epoch"})
			}
		})).ServeHTTP(w, r)
	}))
	defer server.Close()
	var err error
	a, err = newServerAccess(c, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := cliClient{endpoint: server.URL, http: server.Client()}
	identity, err := client.identity(context.Background(), "room", func(key string) string {
		if key == "TEAMX_ADMIN_TOKEN_FILE" {
			return adminTokenPath(c)
		}
		if key == "USER" {
			return "forged"
		}
		return ""
	})
	if err != nil || identity.Caller.ID != c.Access.OperatorID || client.token != token {
		t.Fatal(identity.Caller, err)
	}
}

func TestAccessBrowserModelSetupStartsNormalServer(t *testing.T) {
	home := t.TempDir()
	runtime := testTeamDefinition(t).Runtime
	runtime.Model.ContextWindow = 32768
	runtime.Model.MaxTokens = 4096
	supportsDeveloper := false
	runtime.Model.Compat.SupportsDeveloperRole = &supportsDeveloper
	// A nonempty but expired credential can be repaired with an explicit startup mode.
	// Keep a pre-existing setting to prove the model save preserves it.
	source, err := toml.Marshal(map[string]any{"runtime": runtime, "server": map[string]any{"time_zone": "UTC"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(home, "config.toml"), source, 0600); err != nil {
		t.Fatal(err)
	}
	info, stop := startTestServer(t, []string{"--home", home, "--setup-model"}, serverOptions{create: controlledActivationFactory(nil, nil), probe: successfulModelProbe})
	defer stop()
	client := serverTestCLI(t, home, info.Endpoint)
	var result map[string]any
	bad := map[string]string{"request_id": "bad-request", "protocol": "invalid", "base_url": "https://model.invalid/v1", "model_id": "fixture", "api_key": "model-test-secret"}
	if err = client.request(context.Background(), "/v1/setup/model", bad, &result); err == nil {
		t.Fatal("invalid model saved")
	}
	if _, err = os.Stat(filepath.Join(home, "data", "rooms.json")); !os.IsNotExist(err) {
		t.Fatal("setup started business early", err)
	}
	good := setupModelInput{RequestID: "good-request", ModelParameters: parametersFromLegacyModel(runtime.Model), APIKey: "model-test-secret"}
	good.ModelID = "fixture"
	if err = client.request(context.Background(), "/v1/setup/model", good, &result); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		result = map[string]any{}
		err = client.request(context.Background(), "/v1/setup", nil, &result)
		if err == nil && result["setup_required"] == false {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("saved model did not start normal server", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	c, err := readStartupConfig(filepath.Join(home, "config.toml"), true, true)
	if err != nil || *c.Server.TimeZone != "UTC" || c.Models == nil || c.Models.Entries[0].ModelID != "fixture" || c.Models.Entries[0].Name != "fixture" || c.Runtime.Model.ModelID != "" || len(c.Env) != 0 {
		t.Fatal("setup lost saved configuration", err)
	}
	file, err := os.Stat(c.path)
	if err != nil || file.Mode().Perm() != 0600 {
		t.Fatal("model config not private", err)
	}
	var receipt setupOperation
	if err := client.request(context.Background(), "/v1/setup/model", good, &receipt); err != nil || receipt.Status != "saved" {
		t.Fatal("accepted request lost after listener transition", receipt, err)
	}
	conflicting := good
	conflicting.ModelID = "different"
	if err := client.request(context.Background(), "/v1/setup/model", conflicting, &receipt); err == nil {
		t.Fatal("request ID accepted different input")
	}
	entry := c.Models.Entries[0]
	if entry.ContextWindow != 32768 || entry.MaxTokens != 4096 || entry.Compat.SupportsDeveloperRole == nil || *entry.Compat.SupportsDeveloperRole {
		t.Fatal("advanced parameters lost")
	}
	stop()
	restarted, stopAgain := startTestServer(t, []string{"--home", home}, serverOptions{create: controlledActivationFactory(nil, nil)})
	defer stopAgain()
	if restarted.Status == "setup_required" || restarted.ServiceEpoch == "" {
		t.Fatal("managed home returned to setup", restarted)
	}
	restartedClient := serverTestCLI(t, home, restarted.Endpoint)
	if err := restartedClient.request(context.Background(), "/v1/setup/model", good, &receipt); err != nil || receipt.Status != "saved" {
		t.Fatal("accepted request lost after restart", receipt, err)
	}
}
func TestAccessRejectsMissingInitializationAndToken(t *testing.T) {
	c := startupConfig{}
	c.path = filepath.Join(t.TempDir(), "config.toml")
	if _, err := newServerAccess(c, "http://127.0.0.1:8088"); err == nil {
		t.Fatal("missing access accepted")
	}
	c, _ = testAccessConfig(t, t.TempDir())
	os.Remove(adminTokenPath(c))
	if _, err := newServerAccess(c, "http://127.0.0.1:8088"); err == nil {
		t.Fatal("missing token accepted")
	}
}
func TestAccessStreamingHeaderIsPreservedBeforeStdinArrives(t *testing.T) {
	a, _, _ := accessFixture(t)
	binding := executionBinding{ServiceInfo: ServiceInfo{RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: "instance"}, ServiceEpoch: "epoch"}, Caller: Caller{Author: Author{Kind: "agent", ID: "agent"}, MembershipID: "member"}}
	token, revoke, err := a.issueExecution(binding)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	header, _ := json.Marshal(binding)
	gotHeader := make(chan struct{})
	server := httptest.NewServer(a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reader := bufio.NewReader(r.Body)
		line, err := reader.ReadBytes('\n')
		if err != nil || !bytes.Equal(bytes.TrimSpace(line), header) {
			t.Error("tool header changed", err)
		}
		close(gotHeader)
		data, _ := io.ReadAll(reader)
		w.Write(data)
	})))
	defer server.Close()
	a.internal = server.URL
	a.public = server.URL
	reader, writer := io.Pipe()
	defer writer.Close()
	done := make(chan error, 1)
	go func() {
		req, _ := http.NewRequest("POST", server.URL+"/v1/tools/run", reader)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := server.Client().Do(req)
		if err == nil {
			defer res.Body.Close()
			data, _ := io.ReadAll(res.Body)
			if res.StatusCode != 200 || string(data) != "tool stdin" {
				err = fmt.Errorf("stream result %d", res.StatusCode)
			}
		}
		done <- err
	}()
	writer.Write(append(header, '\n'))
	select {
	case <-gotHeader:
	case <-time.After(3 * time.Second):
		t.Fatal("authentication buffered stdin before tool header")
	}
	writer.Write([]byte("tool stdin"))
	writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAccessDSHExecutionCredentialRevokedOnCloseAndFailedOpen(t *testing.T) {
	a, _, _ := accessFixture(t)
	dir, id := recoveryFixture(t)
	store, err := OpenStore(dir, id.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	opts := adapterOptions(store, id)
	var token string
	backend := dshBackend{access: a, store: store, profile: dshProfile{}, open: func(ctx context.Context, options dsh.Options) (dshComponent, error) {
		token = options.ExecutionToken
		if !validAccessToken(token) || options.ToolEnv["TEAMX_EXECUTION_TOKEN"] != "" || options.ToolEnv["TEAMX_ADMIN_TOKEN_FILE"] != "" {
			t.Error("execution credential did not stay in ephemeral field")
		}
		return &adapterComponent{}, nil
	}}
	session, err := backend.OpenSession(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.executions[sha256.Sum256([]byte(token))]; !ok {
		t.Fatal("session not authorized")
	}
	if err = session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(a.executions) != 0 {
		t.Fatal("closed session remains authorized")
	}
	// The failed backend transfers no cleanup ownership, so issuance must roll back.
	opts.StatePath = filepath.Join(t.TempDir(), "session.json")
	backend.open = func(ctx context.Context, options dsh.Options) (dshComponent, error) {
		return nil, errors.New("offline failed open")
	}
	if _, err = backend.OpenSession(context.Background(), opts); err == nil {
		t.Fatal("failed open succeeded")
	}
	if len(a.executions) != 0 {
		t.Fatal("failed open retained execution token")
	}
}
func TestAccessCLIDoesNotSendCredentialsToRemoteHTTPOrRedirect(t *testing.T) {
	if err := validateAccessEndpoint("http://remote.example.com"); err == nil {
		t.Fatal("remote cleartext accepted")
	}
	called := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 302) }))
	defer redirect.Close()
	token, _ := randomAccessToken()
	client := cliClient{endpoint: redirect.URL, http: redirect.Client(), token: token}
	var out any
	if err := client.request(context.Background(), "/v1/info", nil, &out); err == nil || called {
		t.Fatal("credential followed redirect", err)
	}
}

func TestAccessRestartRotatesTokenWithoutChangingOwner(t *testing.T) {
	c, oldToken := testAccessConfig(t, t.TempDir())
	old, err := newServerAccess(c, "http://127.0.0.1:8088")
	if err != nil {
		t.Fatal(err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	cookie := accessLogin(t, old.wrap(next), oldToken)
	execution, revoke, err := old.issueExecution(executionBinding{})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	newToken, err := randomAccessToken()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(adminTokenPath(c), []byte(newToken+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fresh, err := newServerAccess(c, old.internal)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.operator != old.operator {
		t.Fatal("rotation changed human identity")
	}
	handler := fresh.wrap(next)
	for _, token := range []string{oldToken, execution} {
		if w := accessRequest(handler, "GET", "/v1/info", token, "", "", nil); w.Code != 401 {
			t.Fatal("old credential accepted", w.Code)
		}
	}
	if w := accessRequest(handler, "GET", "/v1/info", "", "", "", cookie); w.Code != 401 {
		t.Fatal("old cookie accepted")
	}
	if w := accessRequest(handler, "GET", "/v1/info", newToken, "", "", nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
}

func TestAccessUserDisplayNamePreservesCaller(t *testing.T) {
	t.Setenv("USER", "desktop-user")
	c, token := testAccessConfig(t, t.TempDir())
	access, err := newServerAccess(c, "http://127.0.0.1:8088")
	if err != nil {
		t.Fatal(err)
	}
	svc, _ := testService(t)
	manager := testManager(t, t.TempDir(), nil)
	for name, next := range map[string]http.Handler{"room": svc, "managed": manager} {
		t.Run(name, func(t *testing.T) {
			handler := access.wrap(next)
			response := accessRequest(handler, http.MethodGet, "/v1/ui", token, "", "", nil)
			var bootstrap map[string]any
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &bootstrap) != nil || bootstrap["operator_display_name"] != "desktop-user" {
				t.Fatal(response.Code, response.Body.String())
			}
			if name == "managed" {
				response = accessRequest(handler, http.MethodGet, "/v1/rooms", token, "", "", nil)
				if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &bootstrap) != nil {
					t.Fatal(response.Code, response.Body.String())
				}
			}
			if bootstrap["operator"] != c.Access.OperatorID {
				t.Fatal("display name changed authenticated identity", bootstrap)
			}
		})
	}
}

func TestAccessModelFormModuleLoadsBeforeLogin(t *testing.T) {
	_, _, handler := accessFixture(t)
	for path, contentType := range map[string]string{
		"/ui/access.mjs": "javascript", "/ui/model-form.mjs": "javascript", "/ui/brand-mark.svg": "image/svg+xml",
	} {
		response := accessRequest(handler, http.MethodGet, path, "", "", "", nil)
		if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), contentType) {
			t.Fatal(path, response.Code)
		}
	}
}
