package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func gitlabConnectFixture(t *testing.T) (*gitlabLoginHarness, *service, MemberState, platformConnectRequest) {
	return gitlabConnectFixtureFor(t, connectFixture)
}
func gitlabConnectFixtureFor(t *testing.T, fixture platformConnectionFixture) (*gitlabLoginHarness, *service, MemberState, platformConnectRequest) {
	t.Helper()
	g := newGitlabLoginHarness(t)
	old := g.h.m
	m, svc, member, req := fixture(t)
	m.config.endpoint = old.config.endpoint
	file, err := old.platforms.Load()
	if err != nil {
		t.Fatal(err)
	}
	file.Platforms = nil
	if err = m.store.save(m.platformsPathForTest(), file, 2<<20); err != nil {
		t.Fatal(err)
	}
	m.platformHTTP = old.platformHTTP
	g.h.m = m
	req.Input = &platformConnectInput{Provider: "gitlab", Name: "Work GitLab", Origin: g.h.server.URL, ApplicationID: g.req.ApplicationID, AccountUsername: "tester"}
	return g, svc, member, req
}
func gitlabConnectControl(m *roomManager, action string, req platformConnectRequest, version uint64, cookie *http.Cookie) *httptest.ResponseRecorder {
	data, _ := json.Marshal(platformConnectInteractionRequest{ServiceInfo: req.ServiceInfo, Caller: humanCaller, OperationID: req.OperationID, Version: version})
	path := "/v1/platforms/interactions/" + action
	if action == "authorize" {
		path = gitlabCookiePath + "connect"
	}
	r := httptest.NewRequest("POST", m.config.endpoint+path, bytes.NewReader(data))
	r.Header.Set("Origin", m.config.endpoint)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	return w
}
func authorizeGitlabConnect(t *testing.T, g *gitlabLoginHarness, req platformConnectRequest, op platformConnectOperation) (gitlabAuthView, *http.Cookie) {
	t.Helper()
	w := gitlabConnectControl(g.h.m, "authorize", req, op.Version, nil)
	var result struct {
		URL string `json:"authorization_url"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.URL == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly {
		t.Fatal("missing browser binding")
	}
	g.h.m.mu.Lock()
	s := g.h.m.gitlabConnectSessionLocked(op.ID)
	v := s.gitlabAuthView
	g.h.m.mu.Unlock()
	if result.URL != v.URL || strings.Contains(w.Body.String(), "client_secret") {
		t.Fatal("invalid bridge")
	}
	return v, cookies[0]
}
func TestGitLabConnectCallbackReceiptInboxAndCLI(t *testing.T) {
	g, svc, member, req := gitlabConnectFixture(t)
	m := g.h.m
	mode := "inbox"
	req.Notify = &mode
	op := connectHTTP(t, m, "start", req)
	if op.Status != "waiting" || op.Interaction.Kind != "gitlab_authorization" || op.Interaction.URL != "" {
		t.Fatal(op)
	}
	if len(m.gitlabAuthSessions) != 0 {
		t.Fatal("Agent created browser session")
	}
	replay := connectHTTP(t, m, "start", req)
	if !reflect.DeepEqual(op, replay) {
		t.Fatal("repeated start changed operation")
	}
	v, cookie := authorizeGitlabConnect(t, g, req, op)
	again := gitlabConnectControl(m, "authorize", req, op.Version, cookie)
	if again.Code != 200 || len(again.Result().Cookies()) != 0 || len(m.gitlabAuthSessions) != 1 {
		t.Fatal("repeat click created session", again.Code)
	}
	if w := g.callback(v, cookie, nil); w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	done := connectHTTP(t, m, "status", connectObservation(req))
	if done.Status != "completed" || done.Account.ID.Value != "101" || done.Connection.Status != "connected" || done.ResultVersion != 1 {
		t.Fatal(done)
	}
	n := waitConnectDelivery(t, m, op.ID)
	if n.Event.EventType != "operation_completed" || n.Event.SourceSeq != req.SourceSeq {
		t.Fatal(n)
	}
	if w := g.callback(v, cookie, nil); w.Code != 403 {
		t.Fatal("replayed callback", w.Code)
	}
	if g.posts.Load() != 1 {
		t.Fatal("replayed token exchange")
	}
	client, endpoint := toolPipeServer(t, m)
	env := cliToolEnv(endpoint, svc, member)
	for _, args := range [][]string{
		{"connect", "wait", "--operation-id", op.ID},
		{"connect", "status", "--source-seq", "1"},
		{"connect", "start", "--provider", "gitlab", "--name", req.Input.Name, "--origin", req.Input.Origin, "--application-id", req.Input.ApplicationID, "--account-username", "tester", "--operation-id", op.ID, "--source-seq", "1"},
	} {
		var out bytes.Buffer
		if err := runPlatformConnectWithClient(context.Background(), args, &out, &out, env, client); err != nil || !strings.Contains(out.String(), `"completed"`) {
			t.Fatal(args, err, out.String())
		}
	}
	file, err := m.platforms.Load()
	if err != nil || len(file.Platforms) != 1 || len(file.OAuthCredentials) != 1 {
		t.Fatal("duplicate save", err)
	}
	m.mu.Lock()
	bindings, e := m.readToolBindingsLocked(req.RoomIdentity)
	m.mu.Unlock()
	if e != nil || len(bindings.Bindings) != 0 {
		t.Fatal("enabled tools", e)
	}
	if _, err := os.Stat(m.gitlabNotificationPath(req.RoomIdentity)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("connection initialized GitLab notification configuration", err)
	}
	raw, _ := os.ReadFile(m.connectPath(op.ID))
	for _, secret := range []string{"candidate-access", "candidate-refresh", "private-client-secret", "authorization_url", "code_verifier", "session_id"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("persisted temporary authorization", secret)
		}
	}
	// Reuse is explicit, versioned and does not create another account.
	source, err := svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: req.RoomIdentity, Caller: humanCaller, ClientMessageID: "reuse-gitlab", Text: "复用 Work GitLab"})
	if err != nil {
		t.Fatal(err)
	}
	req.SourceSeq = source.Seq
	req.OperationID, _ = newOpaqueID()
	req.Input.PlatformID = done.PlatformID
	req.Input.PlatformVersion = done.Connection.Version
	reused := connectHTTP(t, m, "start", req)
	if reused.PlatformID != done.PlatformID || reused.Authorization.Status != "reused" {
		t.Fatal(reused)
	}
	req.Input.PlatformVersion++
	if w := managerHTTP(m, "/v1/platforms/connect/start", req); w.Code != 409 {
		t.Fatal("changed fixed input", w.Code)
	}
}
func TestGitLabConnectBrowserAndAccountBoundaries(t *testing.T) {
	for _, scenario := range []string{"wrong_browser", "wrong_origin", "wrong_actor", "agent", "mismatch", "denied", "cancelled", "expired", "member_removed", "app_changed", "already_connected"} {
		t.Run(scenario, func(t *testing.T) {
			g, svc, _, req := gitlabConnectFixture(t)
			m := g.h.m
			if scenario == "mismatch" {
				req.Input.AccountUsername = "somebody_else"
			}
			op := connectHTTP(t, m, "start", req)
			if scenario == "wrong_actor" || scenario == "agent" {
				caller := Caller{Kind: "human", ID: "other"}
				if scenario == "agent" {
					caller = req.Caller
				}
				w := managerHTTP(m, gitlabCookiePath+"connect", platformConnectInteractionRequest{ServiceInfo: req.ServiceInfo, Caller: caller, OperationID: op.ID, Version: op.Version})
				if w.Code != 403 {
					t.Fatal("actor admitted", w.Code)
				}
				return
			}
			v, cookie := authorizeGitlabConnect(t, g, req, op)
			if scenario == "wrong_browser" {
				if w := gitlabConnectControl(m, "authorize", req, op.Version, nil); w.Code != 403 || !strings.Contains(w.Body.String(), `"field":"browser_session"`) {
					t.Fatal("session stolen", w.Code)
				}
				if w := g.callback(v, nil, nil); w.Code != 403 {
					t.Fatal("callback without browser", w.Code)
				}
				if g.posts.Load() != 0 {
					t.Fatal("exchange before browser check")
				}
				return
			}
			if scenario == "wrong_origin" {
				u, _ := url.Parse(v.URL)
				r := httptest.NewRequest("GET", "http://other.invalid"+gitlabCallbackPath+"?state="+url.QueryEscape(u.Query().Get("state"))+"&code=fixture", nil)
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				m.ServeHTTP(w, r)
				if w.Code == 303 || g.posts.Load() != 0 {
					t.Fatal("wrong callback host admitted", w.Code)
				}
				return
			}
			switch scenario {
			case "cancelled":
				connectHTTP(t, m, "cancel", connectObservation(req))
			case "expired":
				m.mu.Lock()
				s := m.gitlabConnectSessionLocked(op.ID)
				m.finishGitLabAuthLocked(s, "expired", "")
				m.mu.Unlock()
			case "member_removed":
				if _, err := svc.room.Call[MemberInfo](context.Background(), RemoveMember{op.MemberIdentity}); err != nil {
					t.Fatal(err)
				}
			case "app_changed":
				m.mu.Lock()
				file, _ := m.platforms.Load()
				file.OAuthApplications[0].ClientSecret = "new-secret"
				err := m.store.save(m.platformsPathForTest(), file, 2<<20)
				m.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			case "already_connected":
				// Same instance/native account already connected through the existing callback.
				m.mu.Lock()
				s := m.gitlabConnectSessionLocked(op.ID)
				other := *s
				other.operationID = ""
				other.targetID, _ = newOpaqueID()
				other.name = "Other connection"
				other.PlatformID = other.targetID
				m.mu.Unlock()
				// Commit exact validated fixture tokens using the existing registry boundary.
				seedGitLabConnectAccount(t, m, &other)
			}
			change := func(q url.Values) {}
			if scenario == "denied" {
				change = func(q url.Values) { q.Del("code"); q.Set("error", "access_denied") }
			}
			g.callback(v, cookie, change)
			var done platformConnectOperation
			if scenario == "member_removed" {
				m.mu.Lock()
				var e error
				done, e = m.readPlatformConnectLocked(op.ID)
				m.mu.Unlock()
				if e != nil {
					t.Fatal(e)
				}
			} else {
				done = connectHTTP(t, m, "status", connectObservation(req))
			}
			if done.Status == "completed" || done.Connection.Status == "connected" {
				t.Fatal("wrong target saved", done)
			}
			if scenario == "already_connected" && done.ErrorCode != "account_already_connected" {
				t.Fatal(done)
			}
			file, err := m.platforms.Load()
			want := 0
			if scenario == "already_connected" {
				want = 1
			}
			if err != nil || len(file.Platforms) != want {
				t.Fatal("unexpected connection", err)
			}
			if scenario == "cancelled" && done.Status != "cancelled" || scenario == "expired" && done.Status != "expired" {
				t.Fatal(done)
			}
		})
	}
}
func seedGitLabConnectAccount(t *testing.T, m *roomManager, s *gitlabAuthSession) {
	t.Helper()
	validation := platform.Validation{Status: "valid", UserID: 101, Username: "tester", CheckedAt: time.Now().UTC()}
	validation.Account, _ = gitlab.LegacyAccountIdentity(s.app.Origin, validation)
	tokens := platform.OAuthTokens{AccessToken: platform.NewSecret("candidate-access"), RefreshToken: platform.NewSecret("candidate-refresh"), ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"api"}}
	if _, err := m.platforms.CommitOAuthLogin(nil, s.targetID, s.name, s.app, "", humanCaller.ID, tokens, validation); err != nil {
		t.Fatal(err)
	}
}
func TestGitLabConnectMissingApplicationAndFixedInputs(t *testing.T) {
	g, _, _, req := gitlabConnectFixture(t)
	m := g.h.m
	for _, mutate := range []func(*platformConnectInput){
		func(in *platformConnectInput) { in.ApplicationID = strings.Repeat("f", 32) },
		func(in *platformConnectInput) { in.Origin += "/" },
		func(in *platformConnectInput) { in.Provider = "unsupported" },
		func(in *platformConnectInput) { in.Provider = "lark" },
	} {
		bad := req
		in := *req.Input
		bad.Input = &in
		mutate(&in)
		if w := managerHTTP(m, "/v1/platforms/connect/start", bad); w.Code == 200 {
			t.Fatal("invalid provider/input admitted", in)
		}
	}
	// Discovery exposes references and compatibility, never credentials.
	read := platformConnectRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller}
	w := managerHTTP(m, "/v1/platforms/list", read)
	if w.Code != 200 || !strings.Contains(w.Body.String(), req.Input.ApplicationID) || !strings.Contains(w.Body.String(), `"compatible":true`) || strings.Contains(w.Body.String(), "private-client-secret") {
		t.Fatal(w.Code, w.Body.String())
	}
	m.mu.Lock()
	file, _ := m.platforms.Load()
	file.OAuthApplications[0].RedirectURI = "http://127.0.0.1:9999" + gitlabCallbackPath
	err := m.store.save(m.platformsPathForTest(), file, 2<<20)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if w := managerHTTP(m, "/v1/platforms/connect/start", req); w.Code != 409 || !strings.Contains(w.Body.String(), "Settings") {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestGitLabConnectRestartReconcilesOnlyExactSavedTarget(t *testing.T) {
	for _, saved := range []bool{false, true} {
		t.Run(map[bool]string{false: "lost_session", true: "committed_before_receipt"}[saved], func(t *testing.T) {
			g, _, _, req := gitlabConnectFixture(t)
			m := g.h.m
			op := connectHTTP(t, m, "start", req)
			v, c := authorizeGitlabConnect(t, g, req, op)
			if saved {
				g.callback(v, c, nil)
			}
			m.mu.Lock()
			if saved {
				op, _ = m.readPlatformConnectLocked(op.ID)
				op.Status, op.Stage, op.ResultVersion = "running", "connection", 0
				op.Connection = onboardingStep{Status: "pending"}
				if err := m.savePlatformConnectLocked(&op); err != nil {
					t.Fatal(err)
				}
			}
			// Close the temporary session without checkpointing, as in process loss.
			s := m.gitlabConnectSessionLocked(op.ID)
			s.operationID = ""
			m.finishGitLabAuthLocked(s, "cancelled", "")
			delete(m.gitlabAuthSessions, s.SessionID)
			before, _ := os.ReadFile(m.platformsPathForTest())
			err := m.recoverPlatformConnectLocked()
			m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			done := connectHTTP(t, m, "status", connectObservation(req))
			want := "unknown"
			if saved {
				want = "completed"
			}
			after, _ := os.ReadFile(m.platformsPathForTest())
			if done.Status != want || !bytes.Equal(before, after) || g.posts.Load() != map[bool]int32{false: 0, true: 1}[saved] {
				t.Fatal(done, "recovery replayed OAuth")
			}
		})
	}
}
func TestGitLabConnectCancelledDuringExchangeCannotCommit(t *testing.T) {
	g, _, _, req := gitlabConnectFixture(t)
	m := g.h.m
	op := connectHTTP(t, m, "start", req)
	v, c := authorizeGitlabConnect(t, g, req, op)
	g.before = func(*http.Request) { connectHTTP(t, m, "cancel", connectObservation(req)) }
	g.callback(v, c, nil)
	done := connectHTTP(t, m, "status", connectObservation(req))
	file, err := m.platforms.Load()
	if done.Status != "cancelled" || err != nil || len(file.Platforms) != 0 {
		t.Fatal("late exchange revived cancelled operation", done, err)
	}
}
func TestGitLabConnectReceiptInterruptionDoesNotReplayOrRevokeSavedToken(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_replace", true: "after_replace"}[replaced], func(t *testing.T) {
			g, _, _, req := gitlabConnectFixture(t)
			m := g.h.m
			op := connectHTTP(t, m, "start", req)
			v, c := authorizeGitlabConnect(t, g, req, op)
			real := m.store.ops
			m.mu.Lock()
			m.store.ops.rename = func(from, to string) error {
				if to == m.connectPath(op.ID) {
					raw, _ := os.ReadFile(from)
					var pending platformConnectOperation
					if json.Unmarshal(raw, &pending) == nil && pending.Status == "completed" {
						if replaced {
							if err := real.rename(from, to); err != nil {
								return err
							}
						}
						return errors.New("injected result receipt crash")
					}
				}
				return real.rename(from, to)
			}
			m.mu.Unlock()
			g.callback(v, c, nil)
			if w := managerHTTP(m, "/v1/platforms/connect/status", connectObservation(req)); w.Code == 200 {
				t.Fatal("uncertain receipt confirmed")
			}
			before, _ := os.ReadFile(m.platformsPathForTest())
			m.mu.Lock()
			m.store.ops = real
			m.mu.Unlock()
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			reopened := testManager(t, m.store.dir, nil)
			reopened.mu.Lock()
			restored, err := reopened.readPlatformConnectLocked(op.ID)
			reopened.mu.Unlock()
			after, _ := os.ReadFile(reopened.platformsPathForTest())
			if err != nil || restored.Status != "completed" || !bytes.Equal(before, after) || g.posts.Load() != 1 || g.revokes.Load() != 0 {
				t.Fatal("recovery replayed OAuth or revoked adopted token", err, restored)
			}
		})
	}
}
func TestGitLabConnectCallbackAutomaticallyContinuesThroughOrdinaryInbox(t *testing.T) {
	g, svc, _, req := gitlabConnectFixture(t)
	m := g.h.m
	op := connectHTTP(t, m, "start", req)
	v, c := authorizeGitlabConnect(t, g, req, op)
	b := newControlledBackend()
	id := op.MemberIdentity
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	member, err := spawnScheduledMember(svc.rt, &MemberAgent{Options: SessionOptions{MemberIdentity: id}, Backend: b, Store: svc.store, Room: svc.room, Inbox: func(ctx context.Context) (InboxStatus, error) { return svc.memberInboxStatus(ctx, id) }}, svc.scheduler)
	if err != nil {
		t.Fatal(err)
	}
	svc.setTarget(id.MembershipID, member)
	if _, err = member.Call[MemberStatus](ctx, ControlMember{MemberIdentity: id, Action: "run", State: UnreadChanged{MemberIdentity: id}}); err != nil {
		t.Fatal(err)
	}
	query := connectObservation(req)
	mode := "inbox"
	query.Notify = &mode
	connectHTTP(t, m, "notify", query)
	g.callback(v, c, nil)
	select {
	case input := <-b.entered:
		if input.Inbox == nil || input.Inbox.SnapshotSeq != 1 || input.LatestSeq != req.SourceSeq {
			t.Fatal(input)
		}
	case <-ctx.Done():
		t.Fatal("no automatic GitLab callback continuation")
	}
	if err := svc.store.initReferenceFollowups(id); err != nil {
		t.Fatal(err)
	}
	page, err := svc.readReferenceInbox(ctx, id, 1, 25, nil)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].TaskEvent.OperationID != op.ID {
		t.Fatal(err, page)
	}
	if _, err = svc.inboxes[id.MembershipID].Call[InboxStatus](ctx, ConfirmInboxBatch{MemberIdentity: id, TurnNumber: 1, Receipt: page.Batch.Receipt}); err != nil {
		t.Fatal(err)
	}
	b.finish <- nil
	waitMember(t, ctx, member, id, func(s MemberStatus) bool {
		return !s.Active && s.Inbox != nil && s.Inbox.ConsumedThrough == 1
	})
	snapshot, err := svc.store.LoadRoom()
	if err != nil || len(snapshot.Messages) != 1 {
		t.Fatal("new chat was required for continuation", err)
	}
}

func TestDefaultAssistantGitLabConnectBrowserBridge(t *testing.T) {
	g, _, _, req := gitlabConnectFixtureFor(t, assistantConnectFixture)
	op := connectHTTP(t, g.h.m, "start", req)
	v, c := authorizeGitlabConnect(t, g, req, op)
	if w := g.callback(v, c, nil); w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	done := connectHTTP(t, g.h.m, "status", connectObservation(req))
	if done.Status != "completed" {
		t.Fatal(done)
	}
}

func TestGitLabConnectBridgeIdentifiesStalePageAndNoncanonicalOrigin(t *testing.T) {
	for _, scenario := range []string{"service_epoch", "service_origin"} {
		t.Run(scenario, func(t *testing.T) {
			g, _, _, req := gitlabConnectFixture(t)
			m := g.h.m
			op := connectHTTP(t, m, "start", req)
			var w *httptest.ResponseRecorder
			if scenario == "service_epoch" {
				req.ServiceEpoch = "previous-service"
				w = gitlabConnectControl(m, "authorize", req, op.Version, nil)
			} else {
				raw, _ := json.Marshal(platformConnectInteractionRequest{ServiceInfo: req.ServiceInfo, Caller: humanCaller, OperationID: op.ID, Version: op.Version})
				r := httptest.NewRequest("POST", "http://other.invalid"+gitlabCookiePath+"connect", bytes.NewReader(raw))
				r.Header.Set("Origin", "http://other.invalid")
				w = httptest.NewRecorder()
				m.ServeHTTP(w, r)
			}
			if w.Code != 403 || !strings.Contains(w.Body.String(), `"field":"`+scenario+`"`) {
				t.Fatal(w.Code, w.Body.String())
			}
			if len(m.gitlabAuthSessions) != 0 {
				t.Fatal("rejected page created authorization session")
			}
		})
	}
}
