//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/registry"
)

func TestOAuthBusinessReferencesAndMRWritePreservesRetryContract(t *testing.T) {
	parallelFixtureTest(t)
	for _, status := range []int{201, 401, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := managedActionTest(t, false)
			installOAuthFixture(t, f.m, f.connection, time.Now().Add(time.Second))
			var refreshes, posts atomic.Int32
			refresh := oauthTransport(&refreshes, nil)
			managedReferenceHTTP(t, f.m, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/oauth/") {
					response, err := refresh.RoundTrip(r)
					if err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
					defer response.Body.Close()
					w.WriteHeader(response.StatusCode)
					io.Copy(w, response.Body)
					return
				}
				if r.Header.Get("Authorization") != "Bearer rotated-access" || r.Header.Get("PRIVATE-TOKEN") != "" {
					t.Error("OAuth credential lost")
				}
				if r.Method == "POST" {
					posts.Add(1)
					w.WriteHeader(status)
					if status == 201 {
						io.WriteString(w, managedActionNote)
					}
					return
				}
				if r.URL.Path == "/api/v4/projects/123" {
					io.WriteString(w, `{"id":123,"path_with_namespace":"g/p"}`)
					return
				}
				managedReferencePayload(w, r)
			})
			receipt, err := f.run()
			if err != nil {
				t.Fatal(err)
			}
			if status == 201 && receipt.Status != "succeeded" {
				t.Fatal(receipt.Status)
			}
			if status != 201 && receipt.Status == "succeeded" {
				t.Fatal("failed write accepted")
			}
			wantPosts := int32(1)
			if status == 500 {
				wantPosts = 2
			} // Existing object-comment policy; OAuth adds no retry.
			if posts.Load() != wantPosts || refreshes.Load() != 1 {
				t.Fatal("unexpected write/refresh replay", posts.Load(), refreshes.Load())
			}
			_, err = f.run()
			if err != nil || posts.Load() != wantPosts {
				t.Fatal("receipt retry resent write", err, posts.Load())
			}
			assertActionCallsSettled(t, f.m)
		})
	}
}

func TestOAuthBusinessAttachmentAndTool(t *testing.T) {
	parallelFixtureTest(t)
	t.Run("saved attachment", func(t *testing.T) {
		h, _, _, q, data := attachmentFixture(t)
		c := platformTestList(t, h.m)[0]
		installOAuthFixture(t, h.m, c, time.Now().Add(time.Second))
		var refreshes atomic.Int32
		refresh := oauthTransport(&refreshes, nil)
		h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasPrefix(r.URL.Path, "/oauth/") || r.URL.Path == "/api/v4/user" {
				return refresh.RoundTrip(r)
			}
			if r.Header.Get("Authorization") != "Bearer rotated-access" || r.Header.Get("PRIVATE-TOKEN") != "" {
				t.Error("attachment did not use OAuth")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(data))}, nil
		})
		response := attachmentHTTP(h.m, q)
		if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), data) || refreshes.Load() != 1 {
			t.Fatal("OAuth attachment failed", response.Code, response.Body.String())
		}
		assertActionCallsSettled(t, h.m)
	})
	t.Run("process contract", func(t *testing.T) {
		m, room, svc, c, members := cliToolFixture(t, `test "$GITLAB_TOKEN" = rotated-access || exit 90
 test -z "$GITLAB_PAT$OAUTH_TOKEN$CI_JOB_TOKEN" || exit 92
 printf 'oauth-process-ok'
`)
		installOAuthFixture(t, m, c, time.Now().Add(time.Second))
		bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
		var refreshes atomic.Int32
		m.platformHTTP = oauthTransport(&refreshes, nil)
		client, endpoint := toolPipeServer(t, m)
		var out, stderr bytes.Buffer
		err := runToolsWithClient(context.Background(), []string{"run", c.ToolEntryID, "api", "user"}, strings.NewReader(""), &out, &stderr, cliToolEnv(endpoint, svc, members[0]), client)
		if err != nil || out.String() != "oauth-process-ok" || refreshes.Load() != 1 {
			t.Fatal(err, out.String(), stderr.String())
		}
		assertActionCallsSettled(t, m)
	})
}

func TestOAuthBusinessWorkspaceAndManagedGit(t *testing.T) {
	m, _ := sharedWorkspaceManager(t)
	room := managerCreate(t, m, "oauth-workspace")
	openRoomOK(t, m, room, "initial")
	c := verifyTodoAccount(t, m, platformTestSave(t, m, platformTestRequest(m)), 101)
	sharedWorkspace(t, m, room, c, 1)
	installOAuthFixture(t, m, c, time.Now().Add(time.Hour))
	// Existing API-only grants are explicitly refused for repository access.
	saved, _ := readManagedRoom(t, m, room)
	ran := false
	run := m.connectionExecute(room.RoomIdentity, *saved.Workspace, func(context.Context, string, map[string]string, string, ...string) ([]byte, error) {
		ran = true
		return nil, nil
	})
	if _, err := run(context.Background(), "", nil, "git", "fetch"); err == nil || ran {
		t.Fatal("API-only grant reached Git")
	}
	m.mu.Lock()
	file, err := m.platforms.Load()
	if err != nil {
		t.Fatal(err)
	}
	app := file.OAuthApplications[0]
	app.Scopes = []string{"api", "write_repository"}
	file.OAuthApplications[0] = app
	// Use the normal login commit to establish the explicitly expanded grant.
	records := append([]registry.Record{}, file.Platforms...)
	if err := m.store.save(m.platformsPathForTest(), file, 2<<20); err != nil {
		t.Fatal(err)
	}
	_, err = m.platforms.CommitOAuthLogin(records, c.ID, c.Name, app, "", humanCaller.ID, platform.OAuthTokens{AccessToken: platform.NewSecret("git-access"), RefreshToken: platform.NewSecret("git-refresh"), ExpiresAt: time.Now().Add(time.Hour), Scopes: app.Scopes}, platform.Validation{Status: "valid", UserID: 101, Username: "user101", CheckedAt: time.Now().UTC()})
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	run = m.connectionExecute(room.RoomIdentity, *saved.Workspace, func(ctx context.Context, _ string, env map[string]string, _ string, _ ...string) ([]byte, error) {
		ran = true
		if env["TEAMX_PLATFORM_TOKEN"] != "git-access" {
			t.Error("Git did not receive current access token")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > maxGitLabTokenUse {
			t.Error("unbounded Git")
		}
		return []byte("git-ok"), nil
	})
	out, err := run(context.Background(), "", nil, "git", "fetch")
	if err != nil || string(out) != "git-ok" {
		t.Fatal(err, string(out))
	}
	// Full Workspace preparation also uses typed API reads and private Git injection.
	var workspaceReads atomic.Int32
	managedReferenceHTTP(t, m, func(w http.ResponseWriter, r *http.Request) {
		workspaceReads.Add(1)
		if r.Header.Get("Authorization") != "Bearer git-access" || r.Header.Get("PRIVATE-TOKEN") != "" {
			t.Error("Workspace API lost OAuth")
		}
		if r.URL.Path == "/api/v4/user" {
			io.WriteString(w, `{"name":"Bound User","email":"bound@example.test"}`)
		} else {
			io.WriteString(w, `{"id":7,"path_with_namespace":"group/project","http_url_to_repo":"https://gitlab.invalid/group/project.git"}`)
		}
	})
	prepared, err := m.ensureWorkspaces(context.Background(), saved, true)
	if err != nil || workspaceReads.Load() == 0 || !strings.Contains(prepared.Instructions, "workspace-git") || strings.Contains(fmt.Sprint(prepared.ToolEnv), "git-access") {
		t.Fatal("OAuth Workspace preparation", err)
	}
	// Managed network Git derives its directory, repository and cancellation owner.
	svc, _ := m.runtimeServiceLocked(room.RoomIdentity)
	member := saved.Members[0]
	m.mu.Lock()
	process, call, _, err := m.prepareToolLocked(context.Background(), toolsRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}), Tool: "workspace-git", Args: []string{"fetch", "main"}})
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if process.Definition.Executable != "git" || !strings.Contains(strings.Join(process.Args, " "), "https://gitlab.invalid/group/project.git") || process.Directory == "" {
		t.Fatal("managed Git lost binding")
	}
	m.finishPlatformCall(call)
	gitDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(gitDir, "git"), []byte("#!/bin/sh\ntest \"$TEAMX_PLATFORM_TOKEN\" = git-access || exit 90\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", gitDir)
	client, endpoint := toolPipeServer(t, m)
	var output, stderr bytes.Buffer
	err = runToolsWithClient(context.Background(), []string{"run", "workspace-git", "fetch", "main"}, bytes.NewReader(nil), &output, &stderr, cliToolEnv(endpoint, svc, member.MemberState), client)
	if err != nil || !strings.Contains(output.String(), "https://gitlab.invalid/group/project.git") {
		t.Fatal("managed Git process", err, output.String(), stderr.String())
	}
	// The old credential endpoint cannot leak OAuth to an untracked parent Git.
	payload, _ := json.Marshal(struct {
		ServiceInfo
		Caller Caller `json:"caller"`
	}{svc.info, agentCallerIdentity(MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID})})
	request := httptest.NewRequest("POST", "/v1/workspace/tool-credential", bytes.NewReader(payload))
	request.Header.Set("X-TeamX-Tool", "credential")
	response := httptest.NewRecorder()
	m.ServeHTTP(response, request)
	if response.Code == 200 || strings.Contains(response.Body.String(), "git-access") {
		t.Fatal("unmanaged Git received OAuth")
	}

	assertActionCallsSettled(t, m)
}

func TestOAuthLocalWorkspaceCommandDoesNotRefreshOrExposeToken(t *testing.T) {
	m, _ := sharedWorkspaceManager(t)
	room := managerCreate(t, m, "local-oauth-workspace")
	openRoomOK(t, m, room, "initial")
	c := platformTestSave(t, m, platformTestRequest(m))
	sharedWorkspace(t, m, room, c, 1)
	installOAuthFixture(t, m, c, time.Now().Add(-time.Minute))
	saved, _ := readManagedRoom(t, m, room)
	requests := 0
	m.platformHTTP = workspaceHTTPFunc(func(*http.Request) (*http.Response, error) { requests++; return nil, ErrRequest })
	called := false
	run := m.connectionExecute(room.RoomIdentity, *saved.Workspace, func(_ context.Context, _ string, env map[string]string, _ string, _ ...string) ([]byte, error) {
		called = true
		if env["TEAMX_PLATFORM_TOKEN"] != "" {
			t.Error("local Git received access token")
		}
		return []byte("local"), nil
	})
	out, err := run(context.Background(), "", map[string]string{"TEAMX_PLATFORM_TOKEN": "stale"}, "git", "-c", "http.followRedirects=false", "config", "--get", "user.name")
	if err != nil || string(out) != "local" || !called || requests != 0 {
		t.Fatal("local command required remote authorization", err, requests)
	}
}

func TestOAuthBusinessDifferentPlatformsAndLogoutFence(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, c, members := cliToolFixture(t, "printf ready\nsleep 30\n")
	account := installOAuthFixture(t, m, c, time.Now().Add(time.Hour))
	req := platformTestRequest(m)
	req.ID = strings.Repeat("d", 32)
	req.Name = "Independent OAuth"

	second := verifyTodoAccount(t, m, platformTestSave(t, m, req), 202)
	m.mu.Lock()
	otherAccount, secret, err := m.resolvePlatformAccountLocked(room.RoomIdentity, second.ID, "gitlab")
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer test-platform-secret" || r.Header.Get("PRIVATE-TOKEN") != "" {
			t.Error("connection isolation lost")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":202}`))}, nil
	})
	if _, err = m.gitlabAccountRead(room.RoomIdentity, otherAccount, secret)(context.Background(), "/user", nil, nil, 1024); err != nil {
		t.Fatal(err)
	}
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	client, endpoint := toolPipeServer(t, m)
	ready := &toolReadyWriter{started: make(chan struct{})}
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runToolsWithClient(context.Background(), []string{"run", c.ToolEntryID, "api"}, bytes.NewReader(nil), ready, &stderr, cliToolEnv(endpoint, svc, members[0]), client)
	}()
	select {
	case <-ready.started:
	case <-time.After(5 * time.Second):
		t.Fatal("OAuth process did not start")
	}
	m.mu.Lock()
	_, _, err = m.platforms.LogoutOAuth(account.Authorization.CredentialID)
	pending := m.cancelPlatformCallsLocked(func(call *platformCall) bool { return call.platformID == c.ID })
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range pending {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("logout did not settle OAuth process")
		}
	}
	if err := <-done; err == nil {
		t.Fatal("canceled OAuth process reported success")
	}
	if _, _, _, err = m.acquireGitLabAccess(context.Background(), room.RoomIdentity, account, platform.Secret{}, time.Second); err == nil {
		t.Fatal("logged out grant admitted")
	}
	if _, err = m.gitlabAccountRead(room.RoomIdentity, otherAccount, secret)(context.Background(), "/user", nil, nil, 1024); err != nil {
		t.Fatal("OAuth logout invalidated independent grant", err)
	}
	assertActionCallsSettled(t, m)
}

func TestOAuthBusinessEventsTodosRefreshKeepsProgress(t *testing.T) {
	parallelFixtureTest(t)
	h, c := newIndependentEventHarness(t)
	h.prepare()
	seedEventInbox(t, h, 1)
	before := h.state()
	account := installOAuthFixture(t, h.m, c, time.Now().Add(time.Second))
	var refreshes, business atomic.Int32
	refresh := oauthTransport(&refreshes, nil)
	h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Path, "/oauth/") || r.URL.Path == "/api/v4/user" {
			return refresh.RoundTrip(r)
		}
		business.Add(1)
		if r.Header.Get("Authorization") != "Bearer rotated-access" || r.Header.Get("PRIVATE-TOKEN") != "" {
			t.Error("business request did not use rotated OAuth")
		}
		body := `[]`
		if strings.Contains(r.URL.Path, "/projects/") && !strings.HasSuffix(r.URL.Path, "/events") {
			body = `{"id":7,"path_with_namespace":"remote/project","http_url_to_repo":"https://gitlab.invalid/remote/project.git"}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	h.m.mu.Lock()
	scope, err := h.m.admitEventsLocked(h.stateUnlocked())
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	var b gitlab.Binding
	if json.Unmarshal(scope.Data, &b) != nil {
		t.Fatal("scope")
	}
	if _, err := h.m.eventsRead(b)(context.Background(), "/projects/7/events", nil, nil, 4096); err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	afterScope, err := h.m.admitEventsLocked(h.stateUnlocked())
	todos, _, todoErr := h.m.resolveTodosPlatformLocked(h.room.RoomIdentity, c.ID)
	h.m.mu.Unlock()
	if err != nil || todoErr != nil || !reflect.DeepEqual(scope, afterScope) {
		t.Fatal("refresh changed admission", err, todoErr)
	}
	fetcher, err := h.m.preparePlatformTodos(context.Background(), "", h.room.RoomIdentity, todos)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fetcher.FetchTodos(context.Background(), gitlab.TodoQuery{State: "pending", Page: 1, PerPage: 10}); err != nil {
		t.Fatal(err)
	}
	after := h.state()
	if !reflect.DeepEqual(before, after) || refreshes.Load() != 1 || business.Load() < 2 {
		t.Fatal("progress changed or duplicate refresh")
	}
	h.m.mu.Lock()
	current, _, err := h.m.resolvePlatformAccountLocked(h.room.RoomIdentity, c.ID, "gitlab")
	h.m.mu.Unlock()
	if err != nil || current != account {
		t.Fatal("normal renewal changed stable evidence", err)
	}
	assertActionCallsSettled(t, h.m)
}

func TestOAuthBusinessExplicitRepositoryScopeInitializationAndLogin(t *testing.T) {
	parallelFixtureTest(t)
	// The callback must verify the exact expanded grant, not accept API-only info.
	for _, granted := range []string{`["api","write_repository"]`, `["api"]`} {
		g := newGitlabLoginHarness(t)
		m := g.h.m
		m.mu.Lock()
		file, err := m.platforms.Load()
		if err == nil {
			file.OAuthApplications[0].Scopes = []string{"api", "write_repository"}

			err = m.store.save(m.platformsPathForTest(), file, 2<<20)
		}
		m.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		original := m.platformHTTP
		m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/oauth/token/info" {
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"resource_owner_id":101,"scope":` + granted + `,"application":{"uid":"private-client-id"}}`))}, nil
			}
			return original.RoundTrip(r)
		})
		v, cookie := g.login(t)
		if !strings.Contains(v.URL, "api+write_repository") {
			t.Fatal("repository scope missing from authorization URL")
		}
		g.callback(v, cookie, nil)
		status := g.status(t, v, cookie)
		if (status.Status == "succeeded") != (granted == `["api","write_repository"]`) {
			t.Fatal("incorrect granted scopes admitted", granted, status.Status, status.ErrorCode)
		}
	}
}
