package room

import (
	"strings"
	"testing"
)

func TestRemovedMemberMCPConfigRequiresExplicitMigration(t *testing.T) {
	for _, value := range []string{"''", "'missing.json'", "'private-credential-path'"} {
		path := writeStartupConfig(t, t.TempDir(), "[server]\nagent_mcp_file = "+value+"\n")
		_, err := loadStartupConfig(path, true)
		if err == nil || !strings.Contains(err.Error(), "server.agent_mcp_file was removed") || strings.Contains(err.Error(), "private-credential-path") {
			t.Fatalf("missing safe migration error: %v", err)
		}
	}
}
