//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
)

// Runs a shared production build over local TCP; an explicit binary is optional.
func TestLoopbackProductionCLI(t *testing.T) {
	parallelFixtureTest(t)
	bin := os.Getenv("TEAMX_TEST_CLI_BIN")
	if bin == "" {
		bin = productionFixtureCLI(t)
	}
	m, room, svc, c, members := cliToolFixture(t, "printf '%s' \"$1\"\ncat\nprintf failure >&2\nexit 43\n")
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	server := httptest.NewServer(m)
	defer server.Close()
	env := cliToolEnv(server.URL, svc, members[0])
	cmd := exec.CommandContext(context.Background(), bin, "tools", "run", c.ToolEntryID, "a b")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for _, key := range []string{"TEAMX_ENDPOINT", "TEAMX_ROOM_ID", "TEAMX_ROOM_INSTANCE", "TEAMX_SERVICE_EPOCH", "TEAMX_AGENT_ID", "TEAMX_MEMBERSHIP_ID"} {
		cmd.Env = append(cmd.Env, key+"="+env(key))
	}
	var out, stderr bytes.Buffer
	cmd.Stdin = bytes.NewBufferString("input")
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 43 || out.String() != "a binput" || stderr.String() != "failure" {
		t.Fatal(err, out.String(), stderr.String())
	}
}
