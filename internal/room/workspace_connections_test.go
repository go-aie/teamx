package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func sharedWorkspace(t *testing.T, m *roomManager, owner roomSummary, c platformConnection, version uint64) workspaceOperation {
	t.Helper()
	w := workspaceConfig{Repository: &workspaceRepository{URL: c.URL + "/group/project.git"}, Binding: &workspaceBinding{Provider: "gitlab", URL: c.URL, Project: "group/project", ConnectionID: c.ID}}
	if version > 1 {
		saved, _ := readManagedRoom(t, m, owner)
		w.Root, w.Repository = saved.Workspace.Root, saved.Workspace.Repository
	}
	req := workspaceRequest(t, m, owner, w)
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
		if origin != "https://gitlab.invalid" || project != "group/project" || !platform.ValidCredential(secret.value) {
			return 0, ErrRequest
		}
		return 7, nil
	}
	return m, f
}

func TestWorkspaceSharedConnectionReuseFailureAndLateProjectCheck(t *testing.T) {
	m, _ := sharedWorkspaceManager(t)
	a, b := managerCreate(t, m, "A"), managerCreate(t, m, "B")
	req := platformTestRequest(m)
	c := platformTestSave(t, m, req)
	openRoomOK(t, m, a, "A")
	sharedWorkspace(t, m, a, c, 1)
	openRoomOK(t, m, b, "B")
	before, raw := readManagedRoom(t, m, b)
	target := workspaceConfig{Repository: &workspaceRepository{URL: c.URL + "/group/project.git"}, Binding: &workspaceBinding{Provider: "gitlab", URL: c.URL, Project: "group/project", ConnectionID: c.ID}}
	bad := workspaceRequest(t, m, b, target)
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
	m.platformProject = func(ctx context.Context, _ string, _ string, _ workspaceSecret) (int64, error) {
		close(started)
		<-ctx.Done()
		return 7, nil
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- managerHTTP(m, "/v1/workspace/apply", bad) }()
	<-started
	req.Version, req.ReferencedRooms = c.Version, []RoomIdentity{a.RoomIdentity}
	c = platformTestReauthorize(t, m, req)
	close(release)
	if result := <-done; result.Code == 200 {
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
	c := platformTestList(t, h.m)[0]
	h.m.mu.Lock()
	file, _ := h.m.readPlatformConnections()
	w := testGitLabWorkspace()

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
	req.Version, req.ReferencedRooms = c.Version, []RoomIdentity{h.room.RoomIdentity}
	platformTestReauthorize(t, h.m, req)
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
	if err != nil || resolution.secret.value != "" || resolution.account.Authorization.CredentialID == "" {
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
	req.Version, req.ReferencedRooms = c.Version, []RoomIdentity{h.room.RoomIdentity}
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		c, err := testReauthorizeConnection(h.m, req)
		r := httptest.NewRecorder()
		if err != nil {
			writeAPIError(r, err)
		} else {
			_ = json.NewEncoder(r).Encode(c)
		}
		finished <- r
	}()
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
				if r.Header.Get("Authorization") != "Bearer test-platform-secret" {
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
