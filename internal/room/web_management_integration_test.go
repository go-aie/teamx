//go:build integration || e2e

package room

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestManagedWebUIEndToEnd(t *testing.T) {
	parallelFixtureTest(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for the app entry point acceptance")
	}
	m := activationTestManager(t)
	m.teams = map[string]teamDefinition{"sample": testTeamDefinition(t)}
	m.factory = controlledActivationFactory(nil, nil)
	server := httptest.NewUnstartedServer(m)
	m.config.endpoint = "http://" + server.Listener.Addr().String()
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "../web/tests/web_management_e2e.mjs")
	cmd.Env = append(os.Environ(), "TEAMX_WEB_ENDPOINT="+server.URL)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("app/HTTP acceptance: %v\n%s", err, output)
	}
	t.Log(string(output))
	if *webPreviewDuration > 0 {
		t.Log("isolated browser preview:", server.URL)
		<-time.After(*webPreviewDuration)
	}
}

func TestWorkspaceAppHTTPBinding(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required")
	}
	f := newWorkspaceFixture(t)
	m := activationTestManager(t)
	m.teams = map[string]teamDefinition{"sample": testTeamDefinition(t)}
	m.factory = controlledActivationFactory(nil, nil)
	m.workspaceRun = f.run
	m.platformHTTP = workspaceHTTPFunc(f.http)
	m.platformProject = func(context.Context, string, string, workspaceSecret) (int64, error) { return 7, nil }
	c := platformTestSave(t, m, platformTestRequest(m))
	server := httptest.NewUnstartedServer(m)
	m.config.endpoint = "http://" + server.Listener.Addr().String()
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, phase := range []string{"create", "bind"} {
		cmd := exec.CommandContext(ctx, node, "../web/tests/web_workspace_http.mjs")
		cmd.Env = append(os.Environ(), "TEAMX_WEB_ENDPOINT="+server.URL, "TEAMX_WORKSPACE_UI_PHASE="+phase, "TEAMX_WORKSPACE_REPOSITORY="+strings.TrimSuffix(f.config.Workspace.Repository.URL, ".git")+"/", "TEAMX_WORKSPACE_GITLAB_URL=https://gitlab.invalid", "TEAMX_WORKSPACE_PROJECT=group/project", "TEAMX_WORKSPACE_CONNECTION_ID="+c.ID)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s app/HTTP: %v\n%s", phase, err, output)
		}
	}
	m.mu.Lock()
	op := cloneWorkspaceOperation(m.index.WorkspaceOperations[0])
	m.mu.Unlock()
	for _, phase := range []string{"local_checks", "waiting", "preparing", "committing", "target_check", "sessions"} {
		if elapsed, ok := op.Timings[phase]; !ok || elapsed < 0 {
			t.Fatal("missing timing", phase, op.Timings)
		}
	}
	op.Timings["waiting"] = -1
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.index.WorkspaceOperations[0].Timings["waiting"] < 0 {
		t.Fatal("mutable receipt shared with catalog")
	}
}
