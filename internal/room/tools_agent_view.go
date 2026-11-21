package room

import (
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Discovery describes the caller's bound entries, not definitions or management
// snapshots. Keep decision evidence here so using an entry needs no detail read.
type agentToolEntry struct {
	Name                 string                     `json:"name,omitempty"`
	Description          string                     `json:"description,omitempty"`
	Provider             string                     `json:"provider,omitempty"`
	ToolType             string                     `json:"tool_type"`
	PlatformID           string                     `json:"platform_id,omitempty"`
	PlatformName         string                     `json:"platform_name,omitempty"`
	Instance             string                     `json:"instance,omitempty"`
	Username             string                     `json:"username,omitempty"`
	Status               string                     `json:"status,omitempty"`
	Capabilities         []string                   `json:"capabilities"`
	ConfiguredActions    *[]tool.ActionGrant        `json:"configured_actions,omitempty"`
	UnavailableReason    string                     `json:"unavailable_reason,omitempty"`
	ActionDisabledReason string                     `json:"action_disabled_reason,omitempty"`
	Skill                string                     `json:"skill,omitempty"`
	Workflow             string                     `json:"workflow,omitempty"`
	Account              *platform.AccountIdentity  `json:"account,omitempty"`
	Availability         *agentLarkToolAvailability `json:"availability,omitempty"`
}

type agentLarkToolAvailability struct {
	CheckedAt    time.Time         `json:"checked_at"`
	AccountFixed bool              `json:"account_fixed"`
	ToolEnabled  bool              `json:"tool_enabled"`
	ImageActions []string          `json:"image_actions"`
	Capabilities []lark.Capability `json:"capabilities"`
}

func projectToolSelections(selections []toolSelection) []agentToolEntry {
	result := make([]agentToolEntry, 0, len(selections))
	for _, s := range selections {
		v := agentToolEntry{
			Name: s.Name, Description: s.Description, Provider: s.Provider, ToolType: s.ToolType,
			PlatformID: s.PlatformID, PlatformName: s.PlatformName, Instance: s.Instance, Username: s.Username,
			Status: s.Status, Capabilities: s.Capabilities, UnavailableReason: s.UnavailableReason,
			ActionDisabledReason: s.ActionDisabledReason,
		}
		if v.Capabilities == nil {
			v.Capabilities = []string{}
		}
		if a := s.ActionAuthorization; a != nil {
			// Preserve target/object restrictions and explicit empty intent, without
			// presenting a saved grant revision as current business authority.
			grants := append([]tool.ActionGrant{}, a.Grants...)
			v.ConfiguredActions = &grants
		}
		if a := s.Availability; a != nil {
			account := a.Account
			v.Account = &account
			v.Availability = &agentLarkToolAvailability{a.CheckedAt, a.AccountFixed, a.ToolEnabled, a.ImageActions, a.Capabilities}
		}
		// A replaced/missing connection has no invocable exact entry. Keep its
		// platform ID and repair reason; never advertise the provider alias.
		if s.PlatformID != "" && s.Name == s.ToolType {
			v.Name = ""
			result = append(result, v)
			continue
		}
		if s.Skill != "" {
			v.Skill = `"$TEAMX_CLI" tools skill ` + s.Name
		}
		switch s.Provider {
		case "gitlab":
			guidance := ""
			if s.Skill == "skills/gitlab/SKILL.md" {
				guidance = "For text/image sending guidance, read tools skill " + s.Name + " --topic managed-send directly; no complete-Skill read first. Omit --topic for development/review/merge guidance. "
			} else if s.Skill != "" {
				guidance = "Read the declared Skill when workflow guidance is missing. "
			}
			v.Workflow = "Managed MR comments/replies: actions resolve/execute --tool " + s.Name + " --request JSON. Reuse a verified target and valid contract; a full MR/thread URL needs no native ID lookup or Workspace. " + guidance + "Execute once, report the receipt; unknown: actions read --id ORIGINAL_ID, never resend. Native CLI: tools run " + s.Name + " ... ."
		case "lark":
			// Lark already had a compact view; retain all readiness and repair
			// evidence, and only advertise guidance actually declared by the tool.
			v.Description = ""
			guidance := ""
			if s.Skill != "" {
				guidance = "read the Skill when workflow guidance is missing. "
			}
			v.Workflow = "Managed sends/replies: actions resolve/execute --tool " + s.Name + " --request JSON. Reuse a verified target and valid contract; " + guidance + "Current content/context: tools run " + s.Name + " read. Unknown: actions read --id ORIGINAL_ID, never resend."
		}
		result = append(result, v)
	}
	return result
}
