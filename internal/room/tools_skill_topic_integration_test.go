//go:build integration || e2e

package room

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolSkillTopicUsesBoundEntryWithoutCredentials(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, c, members := cliToolFixture(t, "exit 99")
	m.toolCatalog[0].Skill = "skills/gitlab/SKILL.md"
	client, endpoint := toolPipeServer(t, m)
	env := cliToolEnv(endpoint, svc, members[0])
	var out, errs bytes.Buffer
	read := func(entry, topic string) error {
		out.Reset()
		args := []string{"skill", entry}
		if topic != "" {
			args = append(args, "--topic", topic)
		}
		return runToolsWithClient(t.Context(), args, nil, &out, &errs, env, client)
	}
	if err := read(c.ToolEntryID, "managed-send"); err == nil {
		t.Fatal("unbound member read topic")
	}
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	// Skills are readable without live OAuth, native binaries or a Workspace.
	m.mu.Lock()
	file, err := m.platforms.Load()
	if err == nil {
		file.OAuthCredentials[0].State = "reauthorization_required"
		err = m.store.save(filepath.Join(m.store.dir, "platform-connections.json"), file, 1<<20)
	}
	m.toolCatalog[0].Executable = filepath.Join(t.TempDir(), "missing-tool")
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(m.toolBindingsPath(room.RoomIdentity))
	if err != nil {
		t.Fatal(err)
	}
	for _, topic := range []string{"", "managed-send"} {
		want, err := entrySkillTopic(m.toolCatalog[0], c.ToolEntryID, topic)
		if err != nil {
			t.Fatal(err)
		}
		if err = read(c.ToolEntryID, topic); err != nil || !bytes.Equal(out.Bytes(), want) {
			t.Fatal(err, out.String())
		}
		if strings.Contains(out.String(), "test-platform-secret") || strings.Contains(out.String(), "tools run gitlab ") || strings.Contains(out.String(), "tools skill gitlab ") || strings.Contains(out.String(), "tools skill gitlab`") {
			t.Fatal("secret or provider alias in resource")
		}
		if topic != "" && (!strings.Contains(out.String(), "actions execute --tool "+c.ToolEntryID) || strings.Contains(out.String(), "--tool ENTRY")) {
			t.Fatal("topic did not substitute the selected exact account")
		}
	}
	for _, topic := range []string{"unknown", "../SKILL.md", "Managed-send"} {
		if err := read(c.ToolEntryID, topic); err == nil || !strings.Contains(err.Error(), "topic") || !strings.Contains(err.Error(), "managed-send") {
			t.Fatal(topic, err)
		}
	}
	if err := read("gitlab", "managed-send"); err == nil {
		t.Fatal("provider alias accepted")
	}
	req := toolsRequest{ServiceInfo: svc.info, Caller: agentCaller(members[0].AgentID, members[0].MembershipID), Tool: c.ToolEntryID, Topic: "managed-send"}
	for _, caller := range []Caller{humanCaller, agentCaller(members[0].AgentID, strings.Repeat("f", 32)), agentCaller(members[1].AgentID, members[1].MembershipID)} {
		bad := req
		bad.Caller = caller
		if w := managerHTTP(m, "/v1/tools/skill", bad); w.Code < 400 {
			t.Fatal("foreign identity read topic", w.Code)
		}
	}
	bad := req
	bad.Tool = ""
	if w := managerHTTP(m, "/v1/tools/list", bad); w.Code != http.StatusBadRequest {
		t.Fatal("list ignored topic", w.Code)
	}
	data, _ := json.Marshal(req)
	w := httptest.NewRecorder()
	m.serveTools(w, httptest.NewRequest(http.MethodPost, "/v1/tools/run", bytes.NewReader(append(data, '\n'))))
	if w.Code != http.StatusBadRequest {
		t.Fatal("run ignored topic", w.Code)
	}
	// Other embedded Skills retain their full default resource; topic support is
	// confined to the built-in GitLab resource, not inferred from a provider name.
	for _, path := range []string{"skills/lark/SKILL.md", "skills/scion/SKILL.md", ""} {
		m.toolCatalog[0].Skill = path
		if err := read(c.ToolEntryID, "managed-send"); err == nil || !strings.Contains(err.Error(), "topic") {
			t.Fatal("unrelated Skill accepted topic", path, err)
		}
		err := read(c.ToolEntryID, "")
		if path == "" {
			if err == nil {
				t.Fatal("undeclared Skill acquired resource")
			}
		} else {
			want, e := entrySkill(m.toolCatalog[0], c.ToolEntryID)
			if e != nil || err != nil || !bytes.Equal(out.Bytes(), want) {
				t.Fatal("default Skill changed", path, e, err)
			}
		}
	}
	after, err := os.ReadFile(m.toolBindingsPath(room.RoomIdentity))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("resource read changed binding", err)
	}
}
