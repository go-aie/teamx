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
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func sharedWorkspace(t *testing.T, m *roomManager, owner roomSummary, c platformConnection, version uint64) workspaceOperation {
	t.Helper()
	w := workspaceConfig{Repository: &workspaceRepository{URL: c.URL + "/group/project.git"}, Binding: &workspaceBinding{Provider: "gitlab", URL: c.URL, Project: "group/project", ConnectionID: c.ID}}
	if version > 1 {
		saved, _ := readManagedRoom(t, m, owner)
		w.Root, w.Repository = saved.Workspace.Root, saved.Workspace.Repository
	}
	req := workspaceRequest(m, owner, w)
	req.ExpectedVersion, req.ConnectionVersion = version, c.Version
	applyWorkspace(t, m, req)
	op := awaitWorkspace(t, m)
	if op.Status != "completed" {
		t.Fatal(op.Status, op.ErrorCode, op.Phase)
	}
	// The exact initial intent remains a receipt, without another project query.
	queries := 0
	old := m.platformProject
	m.platformProject = func(context.Context, string, string, workspaceSecret) (int64, error) { queries++; return 0, ErrRequest }
	applyWorkspace(t, m, req)
	m.platformProject = old
	if queries != 0 {
		t.Fatal("duplicate operation queried project")
	}
	return op
}
func sharedWorkspaceManager(t *testing.T) (*roomManager, *workspaceFixture) {
	f := newWorkspaceFixture(t)
	m := activationTestManager(t)
	m.factory = controlledActivationFactory(nil, nil)
	m.workspaceRun = f.run
	m.platformHTTP = workspaceHTTPFunc(f.http)

	m.platformProject = func(_ context.Context, origin, project string, secret workspaceSecret) (int64, error) {
		if origin != "https://gitlab.invalid" || project != "group/project" || !validWorkspacePAT(secret.value) {
			return 0, ErrRequest
		}
		return 7, nil
	}
	return m, f
}

func TestWorkspaceSharedVerifiedConnection(t *testing.T) {
	for _, kind := range []string{"pat", "env"} {
		t.Run(kind, func(t *testing.T) {
			m, _ := sharedWorkspaceManager(t)
			owner := managerCreate(t, m, "verified-connection")
			activateOK(t, m, owner, "initial")
			req := platformTestRequest(m)
			if kind == "env" {
				t.Setenv("WORKSPACE_TEST_TOKEN", req.PAT)
				req.PAT, req.TokenEnv = "", "WORKSPACE_TEST_TOKEN"
			}
			c := platformTestSave(t, m, req)
			m.platformVerify = func(context.Context, string, workspaceSecret) platformConnectionValidation {
				return platformConnectionValidation{Status: "valid", UserID: 101, Username: "tester", CheckedAt: time.Now().UTC()}
			}
			c, err := m.verifyPlatformConnection(context.Background(), platformConnectionRequest{ID: c.ID, Version: c.Version})
			if err != nil {
				t.Fatal(err)
			}
			file, err := m.readPlatformConnections()
			if err != nil || file.Connections[0].IdentityBinding == nil {
				t.Fatal("verified connection lacks identity binding", err)
			}
			sharedWorkspace(t, m, owner, c, 1)
		})
	}
}

func TestWorkspaceSharedReadRechecksConnectionContents(t *testing.T) {
	for _, change := range []string{"unchanged", "version", "generation", "identity_binding", "credential"} {
		t.Run(change, func(t *testing.T) {
			m, _ := sharedWorkspaceManager(t)
			c := platformTestSave(t, m, platformTestRequest(m))
			m.platformVerify = func(context.Context, string, workspaceSecret) platformConnectionValidation {
				return platformConnectionValidation{Status: "valid", UserID: 101, Username: "tester", CheckedAt: time.Now().UTC()}
			}
			if _, err := m.verifyPlatformConnection(context.Background(), platformConnectionRequest{ID: c.ID, Version: c.Version}); err != nil {
				t.Fatal(err)
			}
			file, err := m.readPlatformConnections()
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(file.Connections[0])
			if err != nil {
				t.Fatal(err)
			}
			queried := false
			p := workspacePreparer{connection: func(RoomIdentity, workspaceConfig) (projectConnectionResolution, error) {
				var current projectConnectionResolution
				// Each resolution reloads the record, including a fresh identity pointer.
				if err := json.Unmarshal(data, &current.record); err != nil {
					return current, err
				}
				current.secret.value = "test-platform-secret"
				if queried {
					switch change {
					case "version":
						current.record.Version++
					case "generation":
						current.record.Generation = "changed"
					case "identity_binding":
						current.record.IdentityBinding.CredentialFingerprint = "changed"
					case "credential":
						current.secret.value = "changed"
					}
				}
				return current, nil
			}, transport: workspaceHTTPFunc(func(*http.Request) (*http.Response, error) {
				queried = true
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"id":7}`))}, nil
			})}
			read := p.gitLabRead(RoomIdentity{}, workspaceConfig{}, gitlab.Binding{URL: c.URL, ConnectionID: c.ID})
			response, err := read(context.Background(), "/projects/7", nil, nil, 32<<10)
			if !queried {
				t.Fatal("project query did not run")
			}
			if change == "unchanged" {
				if err != nil || response.Status != http.StatusOK {
					t.Fatal("unchanged connection rejected", err)
				}
			} else if !errors.Is(err, ErrConflict) || len(response.Data) != 0 {
				t.Fatal("changed connection result accepted", err)
			}
		})
	}
}

func TestWorkspaceSharedConnectionMigrationRotationAndToolScope(t *testing.T) {
	m, _ := sharedWorkspaceManager(t)
	owner := managerCreate(t, m, "legacy-auth")
	s := activateOK(t, m, owner, "initial")
	legacy := workspaceRequest(m, owner, workspaceConfig{Repository: &workspaceRepository{URL: "https://gitlab.invalid/group/project.git"}, Binding: &workspaceBinding{Provider: "gitlab", URL: "https://gitlab.invalid", Project: "group/project"}})
	legacy.PAT = "old-room-pat"
	applyWorkspace(t, m, legacy)
	if op := awaitWorkspace(t, m); op.Status != "completed" {
		t.Fatal(op.Status, op.ErrorCode)
	}
	before, _ := readManagedRoom(t, m, owner)
	originalTool := filepath.Join(m.roomDir(owner.RoomID), "resources/platforms/gitlab/git-askpass.sh")
	originalScript, _ := os.ReadFile(originalTool)
	req := platformTestRequest(m)
	c := platformTestSave(t, m, req)
	// Merely adding a global connection never changes an old Room.
	unchanged, _ := readManagedRoom(t, m, owner)
	if !reflect.DeepEqual(before, unchanged) {
		t.Fatal("implicit migration")
	}
	sharedWorkspace(t, m, owner, c, 2)
	saved, configBytes := readManagedRoom(t, m, owner)
	if saved.Workspace.Binding.ProjectID != 7 || saved.Workspace.Binding.CredentialID != "" || saved.Workspace.Binding.TokenEnv != "" {
		t.Fatal(saved.Workspace.Binding)
	}
	if script, _ := os.ReadFile(originalTool); !bytes.Equal(script, originalScript) {
		t.Fatal("old frozen script overwritten")
	}
	if saved.Members[0].ToolEnv["TEAMX_PLATFORM_TOKEN"] != "" || strings.Contains(saved.Members[0].Instructions, req.PAT) {
		t.Fatal("credential in Session config")
	}
	tool := filepath.Join(saved.Members[0].ToolEnv["TEAMX_GITLAB_TOOLS"], "binding.json")
	toolBytes, _ := os.ReadFile(tool)
	active := m.active
	refs := platformTestList(t, m)[0].References
	if len(refs) != 1 || refs[0].RoomIdentity != owner.RoomIdentity {
		t.Fatal(refs)
	}
	if response := managerHTTP(m, "/v1/platform/connections/delete", platformConnectionRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: c.ID, Version: c.Version}); response.Code != 409 {
		t.Fatal("referenced delete", response.Code)
	}
	req.Version, req.PAT = c.Version, "rotated-shared-pat"
	if response := managerHTTP(m, "/v1/platform/connections/save", req); response.Code != 409 {
		t.Fatal("rotation without impacted Rooms accepted")
	}
	req.ReferencedRooms = []RoomIdentity{owner.RoomIdentity}
	c = platformTestSave(t, m, req)
	if m.active != active || c.CredentialVersion != 2 {
		t.Fatal("rotation rebuilt service")
	}
	_, currentConfig := readManagedRoom(t, m, owner)
	currentTool, _ := os.ReadFile(tool)
	if !reflect.DeepEqual(currentConfig, configBytes) || !bytes.Equal(currentTool, toolBytes) {
		t.Fatal("rotation changed frozen config/resources")
	}
	status := queryWorkspace(t, m, owner, "")
	if status.CredentialStatus != "configured" {
		t.Fatal(status.CredentialStatus)
	}
	member := saved.Members[0]
	payload := struct {
		ServiceInfo
		Caller Caller `json:"caller"`
	}{active.info, Caller{Kind: "agent", ID: member.AgentID, MembershipID: member.MembershipID}}
	call := func(info any, browser bool) *httptest.ResponseRecorder {
		data, _ := json.Marshal(info)
		r := httptest.NewRequest(http.MethodPost, "/v1/workspace/tool-credential", bytes.NewReader(data))
		r.Header.Set("X-TeamX-Tool", "credential")
		if browser {
			r.Header.Set("Sec-Fetch-Mode", "cors")
		}
		w := httptest.NewRecorder()
		m.ServeHTTP(w, r)
		return w
	}
	result := call(payload, false)
	var private struct {
		Token     string `json:"token"`
		ProjectID int64  `json:"project_id"`
	}
	if result.Code != 200 || json.Unmarshal(result.Body.Bytes(), &private) != nil || private.Token != req.PAT || private.ProjectID != 7 {
		t.Fatal("private tool resolution failed", result.Code)
	}
	if result.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("credential cache enabled")
	}
	if call(payload, true).Code != 403 {
		t.Fatal("browser obtained tool credential")
	}
	payload.Caller = humanCaller
	if call(payload, false).Code != 403 {
		t.Fatal("human obtained tool credential")
	}
	payload.Caller = Caller{Kind: "agent", ID: member.AgentID, MembershipID: "other-membership"}
	if call(payload, false).Code != 403 {
		t.Fatal("foreign membership obtained credential")
	}
	payload.ServiceInfo, payload.Caller = s.info, Caller{Kind: "agent", ID: member.AgentID, MembershipID: member.MembershipID}
	if call(payload, false).Code != 403 {
		t.Fatal("stale service obtained credential")
	}
}

func TestWorkspaceSharedConnectionReuseFailureAndLateProjectCheck(t *testing.T) {
	m, _ := sharedWorkspaceManager(t)
	a, b := managerCreate(t, m, "A"), managerCreate(t, m, "B")
	req := platformTestRequest(m)
	c := platformTestSave(t, m, req)
	activateOK(t, m, a, "A")
	sharedWorkspace(t, m, a, c, 1)
	activateOK(t, m, b, "B")
	before, raw := readManagedRoom(t, m, b)
	target := workspaceConfig{Repository: &workspaceRepository{URL: c.URL + "/group/project.git"}, Binding: &workspaceBinding{Provider: "gitlab", URL: c.URL, Project: "group/project", ConnectionID: c.ID}}
	bad := workspaceRequest(m, b, target)
	bad.ConnectionVersion = c.Version
	verify := m.platformProject
	m.platformProject = func(context.Context, string, string, workspaceSecret) (int64, error) {
		return 0, errors.New("private-response-must-not-escape")
	}
	denied := managerHTTP(m, "/v1/workspace/apply", bad)
	if denied.Code != 400 || strings.Contains(denied.Body.String(), "private-response") {
		t.Fatal(denied.Code, denied.Body.String())
	}
	after, current := readManagedRoom(t, m, b)
	if !reflect.DeepEqual(raw, current) || !reflect.DeepEqual(before, after) || len(m.index.WorkspaceOperations) != 1 {
		t.Fatal("failed bind mutated Room")
	}
	m.platformProject = verify
	// Rotation during a project query invalidates its result before reservation.
	started, release := make(chan struct{}), make(chan struct{})
	m.platformProject = func(context.Context, string, string, workspaceSecret) (int64, error) {
		close(started)
		<-release
		return 7, nil
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- managerHTTP(m, "/v1/workspace/apply", bad) }()
	<-started
	req.Version, req.PAT, req.ReferencedRooms = c.Version, "new-connection-pat", []RoomIdentity{a.RoomIdentity}
	c = platformTestSave(t, m, req)
	close(release)
	if result := <-done; result.Code != 409 {
		t.Fatal("late project check admitted", result.Code)
	}
	if len(m.index.WorkspaceOperations) != 1 {
		t.Fatal("late query reserved operation")
	}
	m.platformProject = verify
	sharedWorkspace(t, m, b, c, 1)
	if len(platformTestList(t, m)[0].References) != 2 {
		t.Fatal("connection not shared")
	}
}

func eventHarnessSharedConnection(t *testing.T) (*eventHarness, platformConnectionRequest, platformConnection) {
	t.Helper()
	h := newEventHarness(t, gitlab.EventsConfig{})
	req := platformTestRequest(h.m)
	c := platformTestSave(t, h.m, req)
	h.m.mu.Lock()
	file, _ := h.m.readPlatformConnections()
	w := testGitLabWorkspace()
	w.Binding.TokenEnv = ""
	w.Binding.ConnectionID, w.Binding.ConnectionGeneration, w.Binding.ProjectID = c.ID, file.Connections[0].Generation, 7
	h.m.mu.Unlock()
	setTestEffectiveWorkspace(t, h.m, h.room, w, 1)
	h.m.mu.Lock()
	state, _ := h.m.eventsSubscriptionLocked(h.m.testEventsKeyLocked(h.room.RoomIdentity))
	state.Binding = *w.Binding
	err := h.m.notifications.SaveLocked(state)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	h.baseline = state.Baseline
	h.newRunner()
	h.prepare()
	return h, req, c
}

func TestGitLabEventsSharedConnectionRotationDropsLateResultsAndPreservesProgress(t *testing.T) {
	h, req, c := eventHarnessSharedConnection(t)
	h.clock.add(time.Minute)
	h.step()
	h.full()
	before := h.state()
	started, release := make(chan struct{}), make(chan struct{})
	h.runner.NextQuick = h.clock.get()
	h.runner.Fetcher = eventFetchFunc(func(context.Context, gitlab.EventQuery) (gitlab.EventBatch, error) {
		close(started)
		<-release
		return gitlab.EventBatch{Version: 1, Events: []gitlab.Event{h.event(999, 1)}, Complete: true}, nil
	})
	done := make(chan bool, 1)
	go func() { _, stop := h.runner.Step(context.Background()); done <- stop }()
	<-started
	req.Version, req.PAT, req.ReferencedRooms = c.Version, "new-events-pat", []RoomIdentity{h.room.RoomIdentity}
	platformTestSave(t, h.m, req)
	close(release)
	if !<-done {
		t.Fatal("old query did not stop")
	}
	after := h.state()
	if after.Status != "credentials_invalid" || after.QuickCache != nil || !after.Baseline.Equal(before.Baseline) || !after.Watermark.Equal(before.Watermark) || after.SubscriptionID != before.SubscriptionID || len(after.Inbox) != len(before.Inbox) {
		t.Fatal("rotation changed progress")
	}
	h.newRunner()
	if _, stop := h.runner.Step(context.Background()); !stop {
		t.Fatal("rotation resumed without explicit sync")
	}
	if err := h.m.syncEventsSubscription(h.m.testEventsKey(h.room.RoomIdentity), after.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	h.newRunner()
	h.prepare()
	if h.state().SubscriptionID != before.SubscriptionID || !h.state().Baseline.Equal(before.Baseline) {
		t.Fatal("retry rebaselined")
	}
}

func TestGitLabEventsSharedEnvironmentChangeRejectsLatePage(t *testing.T) {
	h, req, c := eventHarnessSharedConnection(t)
	t.Setenv("SHARED_TEST_TOKEN", "first-env-token")
	req.Version, req.PAT, req.TokenEnv, req.ReferencedRooms = c.Version, "", "SHARED_TEST_TOKEN", []RoomIdentity{h.room.RoomIdentity}
	platformTestSave(t, h.m, req)
	if err := h.m.syncEventsSubscription(h.m.testEventsKey(h.room.RoomIdentity), h.state().SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	h.newRunner()
	h.prepare()
	h.clock.add(time.Second)
	h.runner.Fetcher = eventFetchFunc(func(context.Context, gitlab.EventQuery) (gitlab.EventBatch, error) {
		os.Setenv("SHARED_TEST_TOKEN", "second-env-token")
		return gitlab.EventBatch{Version: 1, Events: []gitlab.Event{h.event(999, 1)}, Complete: true}, nil
	})
	if _, stop := h.runner.Step(context.Background()); !stop {
		t.Fatal("environment change did not stop query")
	}
	s := h.state()
	if s.Status != "credentials_invalid" || len(s.Inbox) != 0 {
		t.Fatal("accepted stale credential page")
	}
}

func TestWorkspaceSharedPendingReferenceAndFailedPreparationPreserveLegacy(t *testing.T) {
	m, _ := sharedWorkspaceManager(t)
	owner := managerCreate(t, m, "pending")
	activateOK(t, m, owner, "initial")
	req := platformTestRequest(m)
	c := platformTestSave(t, m, req)
	original, _ := readManagedRoom(t, m, owner)
	entered, release := make(chan struct{}), make(chan struct{})
	transport := m.platformHTTP
	m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-release
		return nil, errors.New("private-query-error")
	})

	target := workspaceConfig{Repository: &workspaceRepository{URL: c.URL + "/group/project.git"}, Binding: &workspaceBinding{Provider: "gitlab", URL: c.URL, Project: "group/project", ConnectionID: c.ID}}
	apply := workspaceRequest(m, owner, target)
	apply.ConnectionVersion = c.Version
	applyWorkspace(t, m, apply)
	<-entered
	refs := platformTestList(t, m)[0].References
	if len(refs) != 1 || !refs[0].Pending {
		t.Fatal("pending target not referenced", refs)
	}
	if r := managerHTTP(m, "/v1/platform/connections/delete", platformConnectionRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: c.ID, Version: c.Version}); r.Code != 409 {
		t.Fatal("deleted pending target connection")
	}
	close(release)
	op := awaitWorkspace(t, m)
	if op.Status != "failed" || op.ErrorCode != "preparation_failed" {
		t.Fatal(op.Status, op.ErrorCode)
	}
	saved, _ := readManagedRoom(t, m, owner)
	if !reflect.DeepEqual(saved, original) {
		t.Fatal("failed preparation overwrote effective config")
	}
	if len(platformTestList(t, m)[0].References) != 1 {
		t.Fatal("failed recoverable target lost reference")
	}
	// Editing a failed target may select a shared connection with no active
	// per-room service. The same ownership/version checks still apply.
	m.platformHTTP = transport
	replacement := workspaceRequest(m, owner, target)
	replacement.ConnectionVersion, replacement.ReplacesOperationID = c.Version, apply.OperationID
	applyWorkspace(t, m, replacement)
	if op := awaitWorkspace(t, m); op.Status != "completed" {
		t.Fatal(op.Status, op.ErrorCode)
	}
}

func TestWorkspaceSharedConnectionAppHandler(t *testing.T) {
	m, _ := sharedWorkspaceManager(t)
	m.platformVerify = func(context.Context, string, workspaceSecret) platformConnectionValidation {
		return platformConnectionValidation{Status: "valid", UserID: 101, Username: "tester", CheckedAt: time.Now().UTC()}
	}
	m.teams = map[string]teamDefinition{"sample": testTeamDefinition(t)}
	runAppHandler(t, m, "web_platform_workspace_handler.mjs")
}

func TestWorkspaceSharedConnectionRestartAndUncertainStore(t *testing.T) {
	h, _, _ := eventHarnessSharedConnection(t)
	before := h.state()
	dir, profiles := h.m.store.dir, managerTestProfiles(t)
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	restored := testManager(t, dir, profiles)
	state, err := restored.testEventsState(h.room.RoomIdentity)
	if err != nil || state.SubscriptionID != before.SubscriptionID || !state.Baseline.Equal(before.Baseline) || !state.Watermark.Equal(before.Watermark) || state.Binding != before.Binding {
		t.Fatal("restart changed subscription", err)
	}
	restored.mu.Lock()
	entry, _ := restored.workspaceRoom(h.room.RoomIdentity)
	saved, err := restored.readWorkspaceRoomConfig(entry)
	if err != nil {
		restored.mu.Unlock()
		t.Fatal(err)
	}
	resolution, err := restored.resolveWorkspaceConnectionLocked(h.room.RoomIdentity, *saved.Workspace)
	if err != nil || resolution.secret.value != "test-platform-secret" {
		restored.mu.Unlock()
		t.Fatal("restart failed to resolve shared credential", err)
	}
	restored.store.uncertain = true
	_, err = restored.resolveWorkspaceConnectionLocked(h.room.RoomIdentity, *saved.Workspace)
	restored.store.uncertain = false
	restored.mu.Unlock()
	if !errors.Is(err, ErrCommitUnknown) {
		t.Fatal("uncertain store released credential", err)
	}
}

func TestGitLabEventsSharedRotationJoinsOldWorkerBeforeCredentialCommit(t *testing.T) {
	h, req, c := eventHarnessSharedConnection(t)
	blocked := startBlockedEventWorker(t, h)
	req.Version, req.PAT, req.ReferencedRooms = c.Version, "joined-rotation-token", []RoomIdentity{h.room.RoomIdentity}
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() { finished <- managerHTTP(h.m, "/v1/platform/connections/save", req) }()
	<-blocked.canceled
	h.m.mu.Lock()
	file, err := h.m.readPlatformConnections()
	busy := h.m.busy
	h.m.mu.Unlock()
	if err != nil || file.Connections[0].CredentialVersion != c.CredentialVersion || !busy {
		t.Error("credential committed before old worker settled")
	}
	select {
	case <-finished:
		t.Error("rotation returned before settlement")
	default:
	}
	blocked.finish()
	if response := <-finished; response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	if platformTestList(t, h.m)[0].CredentialVersion != c.CredentialVersion+1 {
		t.Fatal("new credential not committed after settlement")
	}
}

// Exercise the production shared-credential Go HTTP boundary and error mapping.
func TestGitLabEventsSharedPreparationPreservesAuthenticationErrors(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			h, _, _ := eventHarnessSharedConnection(t)
			before := h.state()
			calls := 0
			h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("PRIVATE-TOKEN") != "test-platform-secret" {
					t.Fatal("incorrect credential")
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {"17"}}, Body: io.NopCloser(strings.NewReader("never-echo-secret"))}, nil
			})
			h.m.notifications.Source(gitlab.EventsSourceID).(*gitlab.EventsSource).Prepare = h.m.preparePlatformEvents
			h.newRunner()
			if _, stop := h.runner.Step(context.Background()); !stop {
				t.Fatalf("authentication failure did not stop; calls=%d state=%+v", calls, h.state())
			}
			after := h.state()
			if after.Status != "credentials_invalid" || after.ErrorCode != "credential_rejected" || after.SubscriptionID != before.SubscriptionID || !after.Baseline.Equal(before.Baseline) || !after.Watermark.Equal(before.Watermark) {
				t.Fatal("lost auth status or progress")
			}
			if _, stop := h.runner.Step(context.Background()); !stop || calls != 1 {
				t.Fatal("auth failure retried")
			}
		})
	}
}
