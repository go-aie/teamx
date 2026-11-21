package room

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform/tool"
)

func TestGitlabToolEntryManagedWorkflow(t *testing.T) {
	entry := toolSelection{Definition: tool.Definition{Name: "gitlab-exact-account", Provider: "gitlab", Skill: "gitlab/SKILL.md"}, Status: "ready", Instance: "https://gitlab.invalid", Username: "user101"}
	raw, err := json.Marshal(projectToolSelections([]toolSelection{entry}))
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err = json.Unmarshal(raw, &rows); err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	row := rows[0]
	if row["skill"] != `"$TEAMX_CLI" tools skill gitlab-exact-account` || row["status"] != "ready" || !strings.Contains(row["workflow"].(string), "actions resolve/execute --tool gitlab-exact-account") || entry.Skill != "gitlab/SKILL.md" {
		t.Fatal(row)
	}
}
