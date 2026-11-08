//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform/tool"
)

func TestExplicitToolBindingPreservesRolesAndReadsSkillWithoutCredentials(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, c, members := cliToolFixture(t, "printf 'native-output'")
	m.toolCatalog[0].Skill = tool.Catalog()[0].Skill
	dir := m.roomDir(room.RoomID)
	frozen, err := os.ReadFile(filepath.Join(dir, "room-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	client, endpoint := toolPipeServer(t, m)
	env := cliToolEnv(endpoint, svc, members[0])
	var out, stderr bytes.Buffer
	if err := runToolsWithClient(context.Background(), []string{"list"}, nil, &out, &stderr, env, client); err != nil || strings.TrimSpace(out.String()) != "[]" {
		t.Fatal(err, out.String())
	}
	if err := runToolsWithClient(context.Background(), []string{"skill", "gitlab"}, nil, &out, &stderr, env, client); err == nil {
		t.Fatal("unbound member inherited a tool")
	}
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	out.Reset()
	if err := runToolsWithClient(context.Background(), []string{"run", "gitlab", "mr", "view", "42", "--repo", "group/project"}, bytes.NewReader(nil), &out, &stderr, env, client); err != nil || out.String() != "native-output" {
		t.Fatal(err, out.String(), stderr.String())
	}
	// Reading an embedded workflow does not load credentials, run a process,
	// query GitLab or require a Workspace.
	m.mu.Lock()
	connectionPath := filepath.Join(m.store.dir, "platform-connections.json")
	connectionData, err := os.ReadFile(connectionPath)
	if err == nil {
		err = os.WriteFile(connectionPath, []byte("unavailable"), 0600)
	}
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(connectionPath, connectionData, 0600) })
	out.Reset()
	if err := runToolsWithClient(context.Background(), []string{"skill", "gitlab"}, nil, &out, &stderr, env, client); err != nil || !strings.Contains(out.String(), "准确版本评审") || !strings.Contains(out.String(), "--auto-merge=false") {
		t.Fatal(err, out.String())
	}
	if strings.Contains(out.String(), "test-platform-secret") {
		t.Fatal("credential leaked")
	}
	after, err := os.ReadFile(filepath.Join(dir, "room-config.json"))
	if err != nil || !bytes.Equal(frozen, after) {
		t.Fatal("binding rewrote frozen roles", err)
	}
}

func TestRetiredRPCAndCLIRejectWithoutEffects(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, c, members := cliToolFixture(t, "exit 99")
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	for _, suffix := range []string{"", "/save", "/enable", "/delete", "/discover", "/execute"} {
		r := managerHTTP(m, "/v1/platform/tools"+suffix, map[string]any{"operation_id": "retired"})
		if r.Code < 400 {
			t.Fatal(suffix, r.Code, r.Body.String())
		}
	}
	var out, stderr bytes.Buffer
	if err := runCLI(context.Background(), []string{"platform", "tools", "execute"}, &out, &stderr, cliToolEnv("http://unused.invalid", svc, members[0])); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatal(err)
	}
	for _, name := range []string{"platform-tools.json", "platform-tool-operations.json"} {
		if _, err := os.Stat(filepath.Join(m.roomDir(room.RoomID), name)); !os.IsNotExist(err) {
			t.Fatal("retired protocol wrote state", name, err)
		}
	}
	// New member resources reject the retired business request fields.
	req := toolsRequest{ServiceInfo: svc.info, Caller: agentCaller(members[0].AgentID, members[0].MembershipID), Tool: "gitlab"}
	data, _ := json.Marshal(req)
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
	raw["operation_id"] = "retired"
	if r := managerHTTP(m, "/v1/tools/skill", raw); r.Code == http.StatusOK {
		t.Fatal("accepted retired field")
	}
}
