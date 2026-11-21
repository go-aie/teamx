package room

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func TestToolDiscoveryDecisionEvidence(t *testing.T) {
	grants := []tool.ActionGrant{{Operation: "message.reply", TargetKind: "message", ObjectType: "message"}}
	selections := []toolSelection{
		{Definition: tool.Definition{Name: "gitlab-work", Provider: "gitlab", Skill: "skills/gitlab/SKILL.md", Version: "glab 1.127.0"}, ToolType: "gitlab", PlatformID: "work", PlatformName: "Work", Instance: "https://gitlab.invalid", Username: "work-user", Status: "valid", Capabilities: []string{"read", "run"}},
		{Definition: tool.Definition{Name: "gitlab-personal", Provider: "gitlab"}, ToolType: "gitlab", PlatformID: "personal", PlatformName: "Personal", Instance: "https://gitlab.invalid", Username: "personal-user", Status: "unavailable", UnavailableReason: "account_unavailable"},
		{Definition: tool.Definition{Name: "scion-work", Provider: "scion", Skill: "skills/scion/SKILL.md"}, ToolType: "scion", PlatformID: "scion", Instance: "https://scion.invalid", Username: "developer", Status: "valid", Capabilities: []string{"run"}, ActionDisabledReason: "No sending adapter is implemented"},
		{Definition: tool.Definition{Name: "local", Description: "Read local build results", Version: "fixture", Executable: "/private/tool", Environment: map[string]string{"TOKEN": "secret"}}, ToolType: "local", Capabilities: []string{"run"}},
		{Definition: tool.Definition{Name: "lark-work", Provider: "lark"}, ToolType: "lark", PlatformID: "lark", ActionAuthorization: &tool.ActionAuthorization{Version: 1, Revision: 17, Grants: grants}, Availability: &larkMemberCapabilityResult{larkPlatformCapabilityResult: larkPlatformCapabilityResult{PermissionCheck: lark.PermissionCheck{CheckedAt: time.Unix(42, 0).UTC(), Capabilities: []lark.Capability{{Name: "message.reply", State: "unchecked", ReasonCode: "check_expired", NextAction: "retry_check", EvidenceState: "stale"}}}}, ToolEnabled: true}},
	}
	for i := range selections {
		selections[i].AgentID, selections[i].MembershipID = "private-agent", "private-membership"
	}
	before, _ := json.Marshal(selections)
	views := projectToolSelections(selections)
	raw, err := json.Marshal(views)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err = json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		for _, field := range []string{"agent_id", "membership_id", "version", "internal_only", "action_authorization", "environment", "executable"} {
			if _, ok := row[field]; ok {
				t.Fatalf("internal field %s in %v", field, row)
			}
		}
	}
	if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), "secret") {
		t.Fatal("internal data in discovery")
	}
	for i, s := range selections {
		v := views[i]
		if v.Name != s.Name || v.PlatformID != s.PlatformID || v.PlatformName != s.PlatformName || v.Instance != s.Instance || v.Username != s.Username || v.Status != s.Status || v.UnavailableReason != s.UnavailableReason || v.ActionDisabledReason != s.ActionDisabledReason || len(v.Capabilities) != len(s.Capabilities) {
			t.Fatalf("lost decision evidence for %s: %+v", s.Name, v)
		}
	}
	if views[2].Skill != `"$TEAMX_CLI" tools skill scion-work` || views[3].Description != selections[3].Description {
		t.Fatal("missing declared guidance or local purpose")
	}
	for _, i := range []int{1, 3} {
		for _, field := range []string{"skill", "account", "availability", "configured_actions"} {
			if _, ok := rows[i][field]; ok {
				t.Fatalf("invented %s for %s", field, selections[i].Name)
			}
		}
	}
	if _, ok := rows[3]["platform_id"]; ok || views[3].Workflow != "" {
		t.Fatal("local tool acquired platform workflow")
	}
	if rows[4]["skill"] != nil || !reflect.DeepEqual(*views[4].ConfiguredActions, grants) || views[4].Availability.CheckedAt != selections[4].Availability.CheckedAt || !reflect.DeepEqual(views[4].Availability.Capabilities, selections[4].Availability.Capabilities) {
		t.Fatal("lost declared intent/readiness or invented Lark Skill")
	}
	after, _ := json.Marshal(selections)
	if string(before) != string(after) {
		t.Fatal("projection changed management data")
	}
	(*views[4].ConfiguredActions)[0].Operation = "altered"
	if grants[0].Operation != "message.reply" {
		t.Fatal("projected grants alias stored intent")
	}
}

func TestToolDiscoveryEmptyIntentAndChangedConnection(t *testing.T) {
	views := projectToolSelections([]toolSelection{
		{Definition: tool.Definition{Name: "lark-old", Provider: "lark"}, ActionAuthorization: &tool.ActionAuthorization{Version: 1, Revision: 2}},
		{Definition: tool.Definition{Name: "gitlab", Provider: "gitlab", Skill: "skills/gitlab/SKILL.md"}, ToolType: "gitlab", PlatformID: "old-connection", Status: "unavailable", UnavailableReason: "platform_changed"},
	})
	raw, _ := json.Marshal(views)
	if !strings.Contains(string(raw), `"configured_actions":[]`) || views[1].Name != "" || views[1].Skill != "" || views[1].Workflow != "" || views[1].PlatformID != "old-connection" || views[1].UnavailableReason != "platform_changed" {
		t.Fatal(string(raw))
	}
	if raw, _ := json.Marshal(projectToolSelections(nil)); string(raw) != "[]" {
		t.Fatal("empty catalog is not an array", string(raw))
	}
}
