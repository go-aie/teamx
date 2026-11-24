//go:build integration || e2e

package room

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestPolicyManagementHTTP(t *testing.T) {
	parallelFixtureTest(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required")
	}
	m := activationTestManager(t)
	m.teams = map[string]teamDefinition{"sample": testTeamDefinition(t)}
	m.publishRoomsCatalogLocked()
	m.factory = controlledActivationFactory(nil, nil)
	server := browserTestServer(t, m)
	m.config.endpoint = server.URL
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "../web/tests/web_policy_http.mjs")
	cmd.Env = append(os.Environ(), "TEAMX_WEB_ENDPOINT="+server.URL)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("policy management HTTP (context=%v): %v\n%s", ctx.Err(), err, output)
	}
}
