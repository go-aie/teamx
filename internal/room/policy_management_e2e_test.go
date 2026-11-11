//go:build integration || e2e

package room

import (
	"context"
	"net/http/httptest"
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
	m.factory = controlledActivationFactory(nil, nil)
	server := httptest.NewUnstartedServer(m)
	m.config.endpoint = "http://" + server.Listener.Addr().String()
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "../web/tests/web_policy_http.mjs")
	cmd.Env = append(os.Environ(), "TEAMX_WEB_ENDPOINT="+server.URL)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("policy management HTTP: %v\n%s", err, output)
	}
}
