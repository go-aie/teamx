//go:build integration || e2e

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

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func TestWorkspaceSharedPlatformReuseFailureAndLateProjectCheck(t *testing.T) {
	m, _ := sharedWorkspaceManager(t)
	a, b := managerCreate(t, m, "A"), managerCreate(t, m, "B")
	req := platformTestRequest(m)
	c := platformTestSave(t, m, req)
	openRoomOK(t, m, a, "A")
	sharedWorkspace(t, m, a, c, 1)
	openRoomOK(t, m, b, "B")
	before, raw := readManagedRoom(t, m, b)
	target := workspaceConfig{Repository: &workspaceRepository{URL: c.URL + "/group/project.git"}, Binding: &workspaceBinding{Provider: "gitlab", URL: c.URL, Project: "group/project", PlatformID: c.ID}}
	bad := workspaceRequest(t, m, b, target)
	bad.PlatformVersion = c.Version
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

func TestGitLabEventsSharedPlatformRotationDropsLateResultsAndPreservesProgress(t *testing.T) {
	parallelFixtureTest(t)
	h, req, c := eventHarnessSharedPlatform(t)
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

func TestWorkspaceSharedPlatformRestartAndUncertainStore(t *testing.T) {
	parallelFixtureTest(t)
	h, _, _ := eventHarnessSharedPlatform(t)
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
	resolution, err := restored.resolveWorkspacePlatformLocked(h.room.RoomIdentity, *saved.Workspace)
	if err != nil || resolution.secret.value != "" || resolution.account.Authorization.CredentialID == "" {
		restored.mu.Unlock()
		t.Fatal("restart failed to resolve shared credential", err)
	}
	restored.store.uncertain = true
	_, err = restored.resolveWorkspacePlatformLocked(h.room.RoomIdentity, *saved.Workspace)
	restored.store.uncertain = false
	restored.mu.Unlock()
	if !errors.Is(err, ErrCommitUnknown) {
		t.Fatal("uncertain store released credential", err)
	}
}

func TestGitLabEventsSharedRotationJoinsOldWorkerBeforeCredentialCommit(t *testing.T) {
	parallelFixtureTest(t)
	h, req, c := eventHarnessSharedPlatform(t)
	blocked := startBlockedEventWorker(t, h)
	req.Version, req.ReferencedRooms = c.Version, []RoomIdentity{h.room.RoomIdentity}
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		c, err := testReauthorizePlatform(h.m, req)
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
	file, err := h.m.readPlatforms()
	busy := h.m.busy
	h.m.mu.Unlock()
	if err != nil || file.Platforms[0].CredentialVersion != c.CredentialVersion || !busy {
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
	parallelFixtureTest(t)
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			h, _, _ := eventHarnessSharedPlatform(t)
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
