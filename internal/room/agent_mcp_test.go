package room

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func TestAgentMCPPrivateConfigAndFullMemberIsolation(t *testing.T) {
	id := MemberIdentity{RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: "instance"}, AgentID: "assistant", MembershipID: "member"}
	server := dsh.MCPHTTPServer{Type: "http", Name: "sandbox", URL: "http://127.0.0.1:18080/mcp", Headers: []dsh.MCPHeader{{Name: "Authorization", Value: "Bearer private-credential"}}}
	binding := map[string]any{"room_id": id.RoomID, "room_instance": id.RoomInstance, "agent_id": id.AgentID, "membership_id": id.MembershipID, "servers": []dsh.MCPHTTPServer{server}}
	path := filepath.Join(t.TempDir(), "agent-mcp.json")
	write := func(value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	valid := map[string]any{"version": 1, "bindings": []any{binding}}
	write(valid)
	config, err := loadStartupConfig(writeStartupConfig(t, filepath.Dir(path), "[server]\nagent_mcp_file = 'agent-mcp.json'\n"), true)
	if err != nil || config.Server.AgentMCPFile != path {
		t.Fatal("startup binding unavailable", err)
	}
	bindings := config.agentMCP
	got := bindings.servers(id)
	if len(got) != 1 || got[0].Headers[0].Value != server.Headers[0].Value {
		t.Fatal("binding missing")
	}
	got[0].Headers[0].Value = "mutated"
	if bindings.servers(id)[0].Headers[0].Value != server.Headers[0].Value {
		t.Fatal("ephemeral launch changed startup credential")
	}
	for _, other := range []MemberIdentity{
		{RoomIdentity: RoomIdentity{RoomID: "other", RoomInstance: id.RoomInstance}, AgentID: id.AgentID, MembershipID: id.MembershipID},
		{RoomIdentity: RoomIdentity{RoomID: id.RoomID, RoomInstance: "recreated"}, AgentID: id.AgentID, MembershipID: id.MembershipID},
		{RoomIdentity: id.RoomIdentity, AgentID: "other", MembershipID: id.MembershipID},
		{RoomIdentity: id.RoomIdentity, AgentID: id.AgentID, MembershipID: "replacement"},
	} {
		if len(bindings.servers(other)) != 0 {
			t.Fatal("binding crossed member identity", other)
		}
	}
	for _, invalid := range []any{
		map[string]any{"version": 2, "bindings": []any{binding}},
		map[string]any{"version": 1, "bindings": []any{binding, binding}},
		map[string]any{"version": 1, "bindings": []any{binding}, "private-credential": "unknown"},
	} {
		write(invalid)
		if _, err := loadAgentMCPBindings(path); err == nil || strings.Contains(err.Error(), "private-credential") {
			t.Fatal("invalid configuration accepted or leaked", err)
		}
	}
	write(valid)
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAgentMCPBindings(path); err == nil {
		t.Fatal("public credential file accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := path + ".link"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAgentMCPBindings(link); err == nil {
		t.Fatal("symlink accepted")
	}
	for _, name := range []string{"teamx", "bad name"} {
		server.Name = name
		binding["servers"] = []dsh.MCPHTTPServer{server}
		write(valid)
		if _, err := loadAgentMCPBindings(path); err == nil {
			t.Fatal("invalid/reserved namespace accepted")
		}
	}
}
