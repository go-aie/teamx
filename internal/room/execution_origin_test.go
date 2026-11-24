package room

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/agent/srt"
)

func TestExecutionOriginRequiresBoundAgentCredential(t *testing.T) {
	a, admin, _ := accessFixture(t)
	origin, err := srt.New(srt.Config{}).RouteEndpoint(a.internal)
	if err != nil {
		t.Fatal(err)
	}
	a.executionOrigin = origin
	binding := executionBinding{ServiceInfo: ServiceInfo{RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: "instance"}, ServiceEpoch: "epoch"}, Caller: Caller{Author: Author{Kind: "agent", ID: "agent"}, MembershipID: "member"}}
	token, revoke, err := a.issueExecution(binding)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(binding)
	h := a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	request := func(auth, peer, browserOrigin string) int {
		r := httptest.NewRequest("POST", origin+"/v1/read", strings.NewReader(string(body)))
		r.RemoteAddr = peer
		if auth != "" {
			r.Header.Set("Authorization", "Bearer "+auth)
		}
		if browserOrigin != "" {
			r.Header.Set("Origin", browserOrigin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if code := request(token, "127.0.0.1:12345", ""); code != 200 {
		t.Fatal("delegated callback rejected", code)
	}
	for _, args := range [][3]string{{"", "127.0.0.1:12345", ""}, {admin, "127.0.0.1:12345", ""}, {token, "192.0.2.1:12345", ""}, {token, "127.0.0.1:12345", origin}} {
		if code := request(args[0], args[1], args[2]); code != 403 {
			t.Fatal("invalid execution origin accepted", args, code)
		}
	}
	revoke()
	if code := request(token, "127.0.0.1:12345", ""); code != 401 {
		t.Fatal("revoked callback accepted", code)
	}
}

func TestExecutionOriginMCPInitialization(t *testing.T) {
	_, _, a, token := mcpInboxFixture(t)
	alias, err := srt.New(srt.Config{}).RouteEndpoint(a.internal)
	if err != nil {
		t.Fatal(err)
	}
	a.executionOrigin = alias
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"execution-test","version":"1"}}}`
	request := func(origin, credential string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", origin+"/mcp", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("Authorization", "Bearer "+credential)
		w := httptest.NewRecorder()
		a.wrap(http.NotFoundHandler()).ServeHTTP(w, r)
		return w
	}
	if w := request(alias, token); w.Code != 200 || !strings.Contains(w.Body.String(), `"protocolVersion"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(alias, "invalid"); w.Code != 401 {
		t.Fatal("invalid credential", w.Code)
	}
	if w := request("http://other.execution.invalid:1234", token); w.Code != 403 {
		t.Fatal("unapproved host", w.Code)
	}
}

func TestExecutionCLIUsesOnlyDelegatedLocalProxy(t *testing.T) {
	alias, _ := srt.New(srt.Config{}).RouteEndpoint("http://127.0.0.1:8088")
	token, err := randomAccessToken()
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.String() != alias+"/probe" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("Proxy-Authorization") != "Basic c2Vzc2lvbjpzeW50aGV0aWMtcmVsYXktc2VjcmV0" {
			t.Error("lost exact proxy destination or credential")
		}
		w.WriteHeader(204)
	}))
	defer proxy.Close()
	values := map[string]string{"TEAMX_AGENT_ID": "agent", "TEAMX_MEMBERSHIP_ID": "member", "TEAMX_EXECUTION_TOKEN": token, "DSH_TEAMX_EXECUTION_PROXY": strings.Replace(strings.Replace(proxy.URL, "127.0.0.1", "localhost", 1), "http://", "http://session:synthetic-relay-secret@", 1) + "/"}
	client := cliClient{endpoint: alias, http: &http.Client{}}
	if err := client.loadCredential(func(k string) string { return values[k] }); err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest("GET", alias+"/probe", nil)
	response, err := client.do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatal(response.StatusCode)
	}
	for _, bad := range []string{"", "http://192.0.2.1:3128", "http://user@192.0.2.1:3128"} {
		values["DSH_TEAMX_EXECUTION_PROXY"] = bad
		other := cliClient{endpoint: alias, http: &http.Client{}}
		if err := other.loadCredential(func(k string) string { return values[k] }); err == nil {
			t.Fatal("untrusted proxy accepted", bad)
		}
	}
	if err := validateAccessEndpoint(alias); err == nil {
		t.Fatal("ordinary credentials allowed a cleartext remote alias")
	}
	admin := cliClient{endpoint: alias, http: &http.Client{}, token: token}
	request, _ = http.NewRequest("GET", alias+"/probe", nil)
	if _, err := admin.do(request); err == nil {
		t.Fatal("admin credential used an execution alias")
	}
}

func TestExecutionAccessCannotDisableAuthentication(t *testing.T) {
	a, admin, _ := accessFixture(t)
	a.executionRequiresAuth = true
	a.document = newSystemSettings(a.config).configDocument
	before, err := os.ReadFile(a.config.path)
	if err != nil {
		t.Fatal(err)
	}
	h := a.wrap(http.NotFoundHandler())
	w := accessRequest(h, "POST", "/v1/access/settings", admin, a.internal, settingsBody(a, false, false, "", admin, ""), nil)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "login_enabled") {
		t.Fatal(w.Code, w.Body.String())
	}
	after, err := os.ReadFile(a.config.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || a.disabled {
		t.Fatal("rejected downgrade changed access state")
	}
}
