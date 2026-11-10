package room

import (
	"context"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func sharedWorkspace(t *testing.T, m *roomManager, owner roomSummary, c managedPlatform, version uint64) workspaceOperation {
	t.Helper()
	w := workspaceConfig{Repository: &workspaceRepository{URL: c.URL + "/group/project.git"}, Binding: &workspaceBinding{Provider: "gitlab", URL: c.URL, Project: "group/project", PlatformID: c.ID}}
	if version > 1 {
		saved, _ := readManagedRoom(t, m, owner)
		w.Root, w.Repository = saved.Workspace.Root, saved.Workspace.Repository
	}
	req := workspaceRequest(t, m, owner, w)
	req.ExpectedVersion, req.PlatformVersion = version, c.Version
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

func eventHarnessSharedPlatform(t *testing.T) (*eventHarness, managedPlatformRequest, managedPlatform) {
	t.Helper()
	h := newEventHarness(t, gitlab.EventsConfig{})
	req := platformTestRequest(h.m)
	c := platformTestList(t, h.m)[0]
	h.m.mu.Lock()
	file, _ := h.m.readPlatforms()
	w := testGitLabWorkspace()

	w.Binding.PlatformID, w.Binding.PlatformGeneration, w.Binding.ProjectID = c.ID, file.Platforms[0].Generation, 7
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
