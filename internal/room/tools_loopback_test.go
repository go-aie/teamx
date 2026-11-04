//go:build e2e

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

// Explicit opt-in: runs the production CLI binary over actual loopback TCP.
func TestLoopbackProductionCLI(t *testing.T) {
	bin := os.Getenv("TEAMX_TEST_CLI_BIN")
	if bin == "" {
		t.Skip("set TEAMX_TEST_CLI_BIN to the built TeamX CLI")
	}
	m, room, svc, c, members := cliToolFixture(t, "printf '%s' \"$1\"\ncat\nprintf failure >&2\nexit 43\n")
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	server := httptest.NewServer(m)
	defer server.Close()
	env := cliToolEnv(server.URL, svc, members[0])
	cmd := exec.CommandContext(context.Background(), bin, "tools", "run", "gitlab", "a b")
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
