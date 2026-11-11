//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"

	"github.com/go-aie/teamx/internal/platform/tool"
)

func TestUsageSkillStandaloneCLIWithoutPlatformBinding(t *testing.T) {
	m, svc, member, req := assistantConnectFixture(t)
	server := httptest.NewServer(m)
	defer server.Close()
	env := cliToolEnv(server.URL, svc, member)
	want, err := tool.ReadSkill(tool.Definition{Skill: "skills/usage/SKILL.md"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runCLI(context.Background(), []string{"skill", "usage"}, &out, &out, env); err != nil || !bytes.Equal(out.Bytes(), want) {
		t.Fatal("bound CLI did not read embedded usage guidance", err, out.String())
	}
	cmd := exec.Command(productionFixtureCLI(t), "skill", "usage")
	cmd.Dir, cmd.Env = t.TempDir(), os.Environ()
	for _, key := range []string{"TEAMX_ENDPOINT", "TEAMX_ROOM_ID", "TEAMX_ROOM_INSTANCE", "TEAMX_SERVICE_EPOCH", "TEAMX_AGENT_ID", "TEAMX_MEMBERSHIP_ID"} {
		cmd.Env = append(cmd.Env, key+"="+env(key))
	}
	if data, err := cmd.CombinedOutput(); err != nil || !bytes.Equal(data, want) {
		t.Fatal("standalone binary needed a source checkout or binding", err, string(data))
	}
	if w := managerHTTP(m, "/v1/platforms/connect/status", connectObservation(req)); w.Code != http.StatusNotFound {
		t.Fatal("reading usage guidance started onboarding", w.Code)
	}
	m.mu.Lock()
	bindings, err := m.readToolBindingsLocked(req.RoomIdentity)
	file, fileErr := m.platforms.Load()
	m.mu.Unlock()
	if err != nil || fileErr != nil || len(bindings.Bindings) != 0 || len(file.Platforms) != 0 {
		t.Fatal("reading usage guidance changed platform configuration", err, fileErr)
	}
}
