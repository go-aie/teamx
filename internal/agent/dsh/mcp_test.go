package dsh

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPHTTPConfigurationNewResumeAndCredentialRefresh(t *testing.T) {
	opts := fixtureOptions(t)
	opts.ToolEnv["TEAMX_DSH_MCP_HTTP"] = "1"
	opts.MCPServers = []MCPHTTPServer{{Type: "http", Name: "teamx", URL: "http://127.0.0.1:8088/mcp", Headers: []MCPHeader{{Name: "Authorization", Value: "Bearer first-private-token"}}}}
	checkParams := func(name, token string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(opts.Workspace, name))
		var params struct {
			Servers []MCPHTTPServer `json:"mcpServers"`
		}
		if err != nil || json.Unmarshal(data, &params) != nil || len(params.Servers) != 1 || params.Servers[0].Headers[0].Value != token {
			t.Fatal("ACP MCP declaration missing or stale", err)
		}
	}
	s, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	checkParams("mcp-new.json", "Bearer first-private-token")
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"backend.json", "home/cordis.patch.yml", "home/AGENTS.md"} {
		data, err := os.ReadFile(filepath.Join(opts.StateDir, path))
		if err != nil || strings.Contains(string(data), "first-private-token") {
			t.Fatal("MCP credential entered persistent config", path, err)
		}
	}
	before := configDigest(opts)
	opts.MCPServers[0].Headers[0].Value = "Bearer refreshed-private-token"
	if configDigest(opts) != before {
		t.Fatal("temporary token changed session identity")
	}
	s, err = Resume(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	checkParams("mcp-resume.json", "Bearer refreshed-private-token")
	opts.MCPServers[0].URL = "http://127.0.0.1:8089/mcp"
	if configDigest(opts) == before {
		t.Fatal("MCP endpoint missing from session identity")
	}
}

func TestMCPHTTPRequiresAdvertisedCapability(t *testing.T) {
	opts := fixtureOptions(t)
	opts.MCPServers = []MCPHTTPServer{{Type: "http", Name: "teamx", URL: "http://127.0.0.1:8088/mcp", Headers: []MCPHeader{}}}
	if s, err := Open(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "mcpCapabilities.http") {
		if s != nil {
			_ = s.Close(context.Background())
		}
		t.Fatal("unsupported HTTP MCP accepted", err)
	}
	if _, err := os.Stat(filepath.Join(opts.Workspace, "session-new.called")); !os.IsNotExist(err) {
		t.Fatal("session created before capability gate")
	}
}

func TestMCPHTTPRejectsMalformedDeclarations(t *testing.T) {
	valid := MCPHTTPServer{Type: "http", Name: "teamx", URL: "http://127.0.0.1:8088/mcp", Headers: []MCPHeader{{Name: "Authorization", Value: "Bearer private"}}}
	for _, change := range []func(*MCPHTTPServer){
		func(s *MCPHTTPServer) { s.Type = "stdio" },
		func(s *MCPHTTPServer) { s.Name = "bad name" },
		func(s *MCPHTTPServer) { s.URL = "http://user:private@localhost/mcp" },
		func(s *MCPHTTPServer) { s.URL = "http://localhost/mcp?token=private" },
		func(s *MCPHTTPServer) { s.Headers[0].Value = "private\r\nInjected: yes" },
		func(s *MCPHTTPServer) { s.Headers[0].Name = "bad name" },
		func(s *MCPHTTPServer) {
			s.Headers = append(s.Headers, MCPHeader{Name: "authorization", Value: "duplicate"})
		},
	} {
		s := cloneMCP([]MCPHTTPServer{valid})
		change(&s[0])
		if err := checkMCP(s); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("invalid configuration accepted or leaked", err)
		}
	}
	if err := checkMCP([]MCPHTTPServer{valid, valid}); err == nil {
		t.Fatal("duplicate namespaces accepted")
	}
}

func TestMCPCredentialsRedactedFromDiagnostics(t *testing.T) {
	s := &Session{secret: "model-key", opts: Options{ExecutionToken: "execution-key", MCPServers: []MCPHTTPServer{{Headers: []MCPHeader{{Name: "Authorization", Value: "Bearer mcp-private-credential"}}}}}}
	for _, credential := range []string{"model-key", "execution-key", "mcp-private-credential"} {
		if text := s.redact("diagnostic " + credential); strings.Contains(text, credential) || !strings.Contains(text, "[REDACTED]") {
			t.Fatal("diagnostic credential exposed")
		}
		if text := s.redactBounded("truncated "+credential[:len(credential)-2], true); !strings.HasSuffix(text, "[REDACTED]") {
			t.Fatal("partial credential exposed")
		}
	}
}
