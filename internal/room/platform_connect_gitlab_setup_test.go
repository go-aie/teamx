package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func gitlabConnectSetupFixture(t *testing.T) (*gitlabLoginHarness, *service, MemberState, platformConnectRequest) {
	t.Helper()
	g, svc, member, req := gitlabConnectFixtureFor(t, assistantConnectFixture)
	m := g.h.m
	file, _ := m.platforms.Load()
	file.OAuthApplications = nil
	if err := m.store.save(m.platformsPathForTest(), file, 2<<20); err != nil {
		t.Fatal(err)
	}
	req.Input.ApplicationID = ""
	return g, svc, member, req
}
func gitlabSetupConnectControl(m *roomManager, req platformConnectRequest, version uint64, appID, importID string, cookie *http.Cookie) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(platformConnectInteractionRequest{ServiceInfo: req.ServiceInfo, Caller: humanCaller, OperationID: req.OperationID, Version: version, ApplicationID: appID, ClientImportID: importID})
	r := httptest.NewRequest("POST", m.config.endpoint+gitlabCookiePath+"connect", bytes.NewReader(raw))
	r.Header.Set("Origin", m.config.endpoint)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	return w
}
func importConnectApplication(t *testing.T, m *roomManager, req platformConnectRequest) string {
	t.Helper()
	w := managerHTTP(m, gitlabCookiePath+"initialize", gitlabSetupRequest{ServiceEpoch: m.epoch, Caller: humanCaller, Origin: req.Input.Origin, Client: &gitlabClientImport{ClientID: "private-client-id", ClientSecret: "private-client-secret", RedirectURI: m.config.endpoint + gitlabCallbackPath, Scopes: []string{"api"}}})
	var view gitlabSetupView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil || view.ClientImportID == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	return view.ClientImportID
}
func TestGitLabConnectFirstApplicationReturnAndContinuation(t *testing.T) {
	for _, scenario := range []string{"owned", "foreign_owner", "remote_callback", "remote_scope", "receipt_interrupted"} {
		t.Run(scenario, func(t *testing.T) {
			g, svc, member, req := gitlabConnectSetupFixture(t)
			m := g.h.m
			mode := "inbox"
			req.Notify = &mode
			pending := connectHTTP(t, m, "start", req)
			if pending.Status != "waiting" || pending.Stage != "application" || pending.Setup == nil || pending.Setup.ApplicationsURL != req.Input.Origin+"/-/user_settings/applications" || pending.Setup.RedirectURI != m.config.endpoint+gitlabCallbackPath || !reflect.DeepEqual(pending.Setup.Scopes, []string{"api"}) {
				t.Fatal(pending)
			}
			before := pending.Version
			m.mu.Lock()
			err := m.recoverPlatformConnectLocked()
			m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if op := connectHTTP(t, m, "status", connectObservation(req)); op.Version != before || op.Status != "waiting" {
				t.Fatal("application draft was terminated by recovery", op)
			}
			importID := importConnectApplication(t, m, req)
			original := m.platformHTTP
			m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/api/v4/user/applications" {
					return original.RoundTrip(r)
				}
				body := "[]"
				if scenario != "foreign_owner" {
					callback := m.config.endpoint + gitlabCallbackPath
					if scenario == "remote_callback" {
						callback = "http://localhost:9999" + gitlabCallbackPath
					}
					body = fmt.Sprintf(`[{"id":41,"application_id":"private-client-id","application_name":"TeamX fixture","callback_url":%q,"confidential":true}]`, callback)
					if scenario == "remote_scope" {
						body = strings.Replace(body, `"confidential":true`, `"confidential":true,"scopes":["read_api"]`, 1)
					}
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			w := gitlabSetupConnectControl(m, req, before, "", importID, nil)
			if w.Code != 200 || len(w.Result().Cookies()) != 1 {
				t.Fatal(w.Code, w.Body.String())
			}
			cookie := w.Result().Cookies()[0]
			bound := connectHTTP(t, m, "status", connectObservation(req))
			if bound.Input.ApplicationID != "" || bound.BoundApplicationID != importID || !bound.ApplicationImport || bound.Setup != nil {
				t.Fatal(bound)
			}
			// A lost start response still queries the original fixed input after binding.
			if replay := connectHTTP(t, m, "start", req); replay.Version != bound.Version {
				t.Fatal("start rewrote binding")
			}
			if w := gitlabSetupConnectControl(m, req, before, "", importID, cookie); w.Code != 409 {
				t.Fatal("stale form admitted", w.Code)
			}
			if w := gitlabSetupConnectControl(m, req, bound.Version, strings.Repeat("f", 32), "", cookie); w.Code != 409 {
				t.Fatal("bound application changed", w.Code)
			}
			m.mu.Lock()
			session := m.gitlabConnectSessionLocked(req.OperationID)
			auth := session.gitlabAuthView
			m.mu.Unlock()
			realOps := m.store.ops
			if scenario == "receipt_interrupted" {
				m.mu.Lock()
				m.store.ops.rename = func(from, to string) error {
					if to == m.connectPath(req.OperationID) {
						raw, _ := os.ReadFile(from)
						var completed platformConnectOperation
						if json.Unmarshal(raw, &completed) == nil && completed.Status == "completed" {
							return errors.New("injected completion receipt interruption")
						}
					}
					return realOps.rename(from, to)
				}
				m.mu.Unlock()
			}
			if w := g.callback(auth, cookie, nil); w.Code != 303 {
				t.Fatal(w.Code, w.Body.String())
			}
			if scenario == "receipt_interrupted" {
				before, _ := os.ReadFile(m.platformsPathForTest())
				m.mu.Lock()
				m.store.ops = realOps
				m.mu.Unlock()
				if err := m.close(); err != nil {
					t.Fatal(err)
				}
				reopened := testManager(t, m.store.dir, nil)
				reopened.mu.Lock()
				restored, err := reopened.readPlatformConnectLocked(req.OperationID)
				reopened.mu.Unlock()
				after, _ := os.ReadFile(reopened.platformsPathForTest())
				if err != nil || restored.Status != "completed" || !bytes.Equal(before, after) || g.posts.Load() != 1 || g.revokes.Load() != 0 {
					t.Fatal("imported result recovery changed credentials", err, restored.Status)
				}
				return
			}
			done := connectHTTP(t, m, "status", connectObservation(req))
			file, err := m.platforms.Load()
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "foreign_owner" || scenario == "remote_callback" || scenario == "remote_scope" {
				if done.Status != "failed" || len(file.Platforms) != 0 || len(file.OAuthApplications) != 0 {
					t.Fatal("invalid remote application adopted", done)
				}
				return
			}
			if done.Status != "completed" || done.PlatformID != pending.PlatformID || done.Application.Status != "applied" || len(file.Platforms) != 1 || len(file.OAuthApplications) != 1 || file.OAuthApplications[0].OwnerID != 101 {
				t.Fatal("wrong original target", done)
			}
			n := waitConnectDelivery(t, m, done.ID)
			if n.Event.OperationID != done.ID || n.Event.ResultVersion != 1 {
				t.Fatal(n)
			}
			raw, _ := os.ReadFile(m.connectPath(done.ID))
			for _, v := range []string{"private-client-secret", "candidate-access", "candidate-refresh", "authorization_url", "redirect_uri", "code_verifier", "session_id"} {
				if strings.Contains(string(raw), v) {
					t.Fatal("persisted secret/live setup", v)
				}
			}
			if g.posts.Load() != 1 {
				t.Fatal("duplicate exchange")
			}
			// Same original source can independently apply personal notifications once;
			// synchronous observation and late signal handling retain its baseline/version.
			id, _ := newOpaqueID()
			enabled := true
			config := platformMemberRequest{ServiceInfo: svc.info, Caller: req.Caller, OperationID: id, SourceSeq: req.SourceSeq, Input: &platformMemberInput{PlatformID: done.PlatformID, PlatformVersion: done.Connection.Version, TodosEnabled: &enabled}}
			receipt := gitlabMemberApply(t, m, "notifications", config)
			view := gitlabMemberGet(t, m, config)
			if view.ToolEnabled || !view.GitLab.Config.Todos.Enabled || view.GitLab.Config.Events.Enabled || view.MemberIdentity.AgentID != member.AgentID {
				t.Fatal(view)
			}
			if replay := gitlabMemberApply(t, m, "notifications", config); !reflect.DeepEqual(replay, receipt) {
				t.Fatal("configuration replay")
			}
			query := connectObservation(req)
			query.ResultVersion = done.ResultVersion
			connectHTTP(t, m, "acknowledge", query)
			if after := gitlabMemberGet(t, m, config); after.NotificationVersion != view.NotificationVersion || after.Baseline != view.Baseline {
				t.Fatal("late observation changed config")
			}
			client, endpoint := toolPipeServer(t, m)
			var out bytes.Buffer
			if err := runPlatformConnectWithClient(context.Background(), []string{"connect", "wait", "--operation-id", done.ID}, &out, &out, cliToolEnv(endpoint, svc, member), client); err != nil || !strings.Contains(out.String(), `"pending":false`) {
				t.Fatal(err, out.String())
			}
		})
	}
}

func TestGitLabConnectApplicationSetupBoundaries(t *testing.T) {
	for _, scenario := range []string{"callback", "foreign_owner", "other_origin", "replacement", "cancelled", "wrong_human", "agent"} {
		t.Run(scenario, func(t *testing.T) {
			g, _, _, req := gitlabConnectSetupFixture(t)
			m := g.h.m
			op := connectHTTP(t, m, "start", req)
			imported := importConnectApplication(t, m, req)
			file, _ := m.platforms.Load()
			switch scenario {
			case "callback":
				file.OAuthClientImports[0].RedirectURI = "http://localhost:9999" + gitlabCallbackPath
			case "foreign_owner":
				file.OAuthClientImports[0].ActorID = "other"
			case "other_origin":
				file.OAuthClientImports[0].Origin = "https://other.invalid"
			case "replacement":
				file.OAuthClientImports[0].ReplacesApplicationID = strings.Repeat("d", 32)
			case "cancelled":
				connectHTTP(t, m, "cancel", connectObservation(req))
			}
			if err := m.store.save(m.platformsPathForTest(), file, 2<<20); err != nil {
				t.Fatal(err)
			}
			if scenario == "wrong_human" || scenario == "agent" {
				caller := Caller{Kind: "human", ID: "other"}
				if scenario == "agent" {
					caller = req.Caller
				}
				w := managerHTTP(m, gitlabCookiePath+"connect", platformConnectInteractionRequest{ServiceInfo: req.ServiceInfo, Caller: caller, OperationID: req.OperationID, Version: op.Version, ClientImportID: imported})
				if w.Code != 403 {
					t.Fatal(w.Code, w.Body.String())
				}
				return
			}
			if w := gitlabSetupConnectControl(m, req, op.Version, "", imported, nil); w.Code == 200 {
				t.Fatal("invalid setup admitted", scenario)
			}
			if len(m.gitlabAuthSessions) != 0 || g.posts.Load() != 0 {
				t.Fatal("invalid setup started auth")
			}
		})
	}
}

func TestGitLabConnectApplicationCorrectionKeepsOriginalOperation(t *testing.T) {
	g, _, _, req := gitlabConnectFixture(t)
	m := g.h.m
	appID := req.Input.ApplicationID
	req.Input.ApplicationID = ""
	file, _ := m.platforms.Load()
	callback := file.OAuthApplications[0].RedirectURI
	file.OAuthApplications[0].RedirectURI = "http://localhost:9999" + gitlabCallbackPath
	if err := m.store.save(m.platformsPathForTest(), file, 2<<20); err != nil {
		t.Fatal(err)
	}
	op := connectHTTP(t, m, "start", req)
	if len(op.Setup.Applications) != 1 || op.Setup.Applications[0].Reason != "redirect_mismatch" {
		t.Fatal(op.Setup)
	}
	if w := gitlabSetupConnectControl(m, req, op.Version, appID, "", nil); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if observed := connectHTTP(t, m, "status", connectObservation(req)); observed.Version != op.Version || observed.Stage != "application" {
		t.Fatal("rejected candidate changed draft", observed)
	}
	file.OAuthApplications[0].RedirectURI = callback
	if err := m.store.save(m.platformsPathForTest(), file, 2<<20); err != nil {
		t.Fatal(err)
	}
	if w := gitlabSetupConnectControl(m, req, op.Version, appID, "", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	bound := connectHTTP(t, m, "status", connectObservation(req))
	if bound.BoundApplicationID != appID || bound.ApplicationImport || bound.Input != *req.Input || bound.PlatformID != op.PlatformID || bound.Source.Seq != req.SourceSeq {
		t.Fatal(bound)
	}
}
