package room

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This builds cmd/teamx, not the deterministic fixture launcher. No Session is
// run: checking paused membership and CLI rejection needs no model credentials.
func TestProductionCLIOnlyExposesDSHMembers(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "teamx")
	if output, err := exec.Command("go", "build", "-o", binary, "../../cmd/teamx").CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, args := range [][]string{{"demo"}, {"member", "add", "--agent", "A", "--runner", "manual"}, {"member", "add", "--agent", "A", "--runner", "scripted"}, {"member", "add", "--agent", "A", "--script", "read"}} {
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = cleanRoomEnv()
		output, err := cmd.CombinedOutput()
		if err == nil || strings.Contains(string(output), "dial tcp") {
			t.Fatal("retired CLI reached transport or succeeded", args, err, string(output))
		}
	}
	config := filepath.Join(t.TempDir(), "profiles.json")
	data := `{"reviewer":{"binary_path":"/bin/sh","artifact":{"source":"test-not-launched","version":"1"},"workspace":"/tmp","instructions":"not launched","model":{"protocol":"openai-completions","base_url":"https://model.invalid/v1","model_id":"unused","api_key_env":"TEAMX_UNUSED_TEST_KEY"}}}`
	if err := os.WriteFile(config, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	p := startRecoveryCLI(t, ctx, binary, t.TempDir(), "--dsh-config", config)
	var member MemberInfo
	if err := json.Unmarshal(p.run(t, "member", "add", "--agent", "A", "--profile", "reviewer", "--json"), &member); err != nil {
		t.Fatal(err)
	}
	list := p.list(t)
	if member.Runner != (RunnerConfig{Kind: "dsh", Profile: "reviewer"}) || len(list.Members) != 1 || list.Members[0].Status != "paused" || list.Members[0].Execution.Agent.TurnNumber != 0 {
		t.Fatal(member, list)
	}
	p.run(t, "send", "--text", "human message", "--id", "one")
	list = p.list(t)
	if list.Members[0].UnreadCount != 1 || list.Members[0].Execution.Active {
		t.Fatal(list)
	}
	p.run(t, "member", "remove", "--agent", "A")
	p.stop(t)
}
