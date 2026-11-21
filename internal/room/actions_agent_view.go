package room

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

// These are transport projections only. Native resolutions, action specs and
// frozen evidence remain intact in storage and in actions read's detail.
type agentTargetCandidate struct {
	Label   string                 `json:"label"`
	Locator platform.TargetLocator `json:"locator"`
}

// Discovery belongs to the same Agent workflow. Keep account and capability
// reasons, without repeating member IDs, generations or frozen grant snapshots.
type agentLarkToolEntry struct {
	Name                 string                     `json:"name"`
	Provider             string                     `json:"provider"`
	ToolType             string                     `json:"tool_type"`
	PlatformID           string                     `json:"platform_id"`
	PlatformName         string                     `json:"platform_name"`
	Instance             string                     `json:"instance"`
	Username             string                     `json:"username"`
	Account              platform.AccountIdentity   `json:"account"`
	Skill                string                     `json:"skill"`
	Workflow             string                     `json:"workflow"`
	Status               string                     `json:"status"`
	Capabilities         []string                   `json:"capabilities"`
	UnavailableReason    string                     `json:"unavailable_reason,omitempty"`
	ActionDisabledReason string                     `json:"action_disabled_reason,omitempty"`
	Availability         *agentLarkToolAvailability `json:"availability,omitempty"`
}

type agentLarkToolAvailability struct {
	CheckedAt    time.Time         `json:"checked_at"`
	AccountFixed bool              `json:"account_fixed"`
	ToolEnabled  bool              `json:"tool_enabled"`
	ImageActions []string          `json:"image_actions"`
	Capabilities []lark.Capability `json:"capabilities"`
}

func projectToolSelections(selections []toolSelection) []any {
	result := make([]any, 0, len(selections))
	for _, s := range selections {
		if s.Provider == "gitlab" {
			guidance := ""
			if s.Skill == "skills/gitlab/SKILL.md" {
				guidance = "For text/image sending guidance, read tools skill " + s.Name + " --topic managed-send directly; no complete-Skill read first. Omit --topic for development/review/merge guidance. "
			} else if s.Skill != "" {
				guidance = "Read the declared Skill when workflow guidance is missing. "
			}
			if s.Skill != "" {
				s.Skill = `"$TEAMX_CLI" tools skill ` + s.Name
			}
			result = append(result, struct {
				toolSelection
				Workflow string `json:"workflow"`
			}{s, "Managed MR comments/replies: actions resolve/execute --tool " + s.Name + " --request JSON. Reuse a verified target and valid contract; a full MR/thread URL needs no native ID lookup or Workspace. " + guidance + "Execute once, report the receipt; unknown: actions read --id ORIGINAL_ID, never resend. Native CLI: tools run " + s.Name + " ... ."})
			continue
		}
		if s.Provider != "lark" {
			result = append(result, s)
			continue
		}
		v := agentLarkToolEntry{Name: s.Name, Provider: s.Provider, ToolType: s.ToolType, PlatformID: s.PlatformID, PlatformName: s.PlatformName, Instance: s.Instance, Username: s.Username, Status: s.Status, Capabilities: s.Capabilities, UnavailableReason: s.UnavailableReason, ActionDisabledReason: s.ActionDisabledReason, Skill: `"$TEAMX_CLI" tools skill ` + s.Name, Workflow: "Managed sends/replies: actions resolve/execute --tool " + s.Name + " --request JSON. Reuse a verified target and valid contract; read the Skill when workflow guidance is missing. Current content/context: tools run " + s.Name + " read. Unknown: actions read --id ORIGINAL_ID, never resend."}
		if a := s.Availability; a != nil {
			v.Account = a.Account
			v.Availability = &agentLarkToolAvailability{a.CheckedAt, a.AccountFixed, a.ToolEnabled, a.ImageActions, a.Capabilities}
		}
		result = append(result, v)
	}
	return result
}

type agentSendCapability struct {
	Operation      platform.Operation  `json:"operation"`
	InputSchema    json.RawMessage     `json:"input_schema"`
	Required       []string            `json:"required"`
	Authority      *agentSendAuthority `json:"authority,omitempty"`
	Executable     bool                `json:"executable"`
	DisabledReason string              `json:"disabled_reason,omitempty"`
	ReasonCode     string              `json:"reason_code,omitempty"`
	NextAction     string              `json:"next_action,omitempty"`
}

// The account carries provider/instance/tenant once for this send projection.
// The target retains every native object/container ID; full scoped identities
// are available in the unchanged stored action returned by actions read.
type agentNativeTarget struct {
	Containers []platform.NativeIdentity `json:"containers,omitempty"`
	Object     platform.NativeIdentity   `json:"object"`
	Discussion platform.ExternalID       `json:"discussion,omitzero"`
	Message    platform.ExternalID       `json:"message,omitzero"`
}

func projectNativeTarget(t *platform.TargetIdentity) *agentNativeTarget {
	if t == nil {
		return nil
	}
	return &agentNativeTarget{t.Containers, t.Object, t.Discussion, t.Message}
}

func isLarkSend(op platform.Operation) bool {
	return op == platform.OperationMessageSend || op == platform.OperationMessageSendSelf
}

type agentSendAuthority struct {
	Type     string   `json:"type"`
	Required []string `json:"required"`
}

type agentSendResolution struct {
	Status                platform.ResolutionStatus `json:"status"`
	Account               platform.AccountIdentity  `json:"account"`
	AccountLabel          string                    `json:"account_label,omitempty"`
	AccountOwnerConfirmed bool                      `json:"account_owner_confirmed,omitempty"`
	Target                *agentNativeTarget        `json:"target,omitempty"`
	TargetLabel           string                    `json:"target_label,omitempty"`
	Candidates            []agentTargetCandidate    `json:"candidates,omitempty"`
	CandidatesIncomplete  bool                      `json:"candidates_incomplete,omitempty"`
	Complete              bool                      `json:"complete"`
	ReasonCode            string                    `json:"reason_code,omitempty"`
	Reason                string                    `json:"reason,omitempty"`
}

type agentSendTargetView struct {
	TargetRef      string                `json:"target_ref,omitempty"`
	Resolution     agentSendResolution   `json:"resolution"`
	Actions        []agentSendCapability `json:"actions"`
	DisabledReason string                `json:"disabled_reason,omitempty"`
}

func projectActionTarget(v actionTargetView) any {
	r := v.Resolution
	if r.Account.Provider == "gitlab" {
		return projectGitlabActionTarget(v)
	}
	if r.Account.Provider != "lark" || r.Target != nil && (r.Target.Message.Valid() || r.Target.Discussion.Valid() || r.Target.IdentityScope != r.Account.IdentityScope) {
		return v
	}
	candidates := make([]agentTargetCandidate, 0, len(r.Candidates))
	for _, c := range r.Candidates {
		target := c.Target
		candidates = append(candidates, agentTargetCandidate{Label: c.Label, Locator: platform.TargetLocator{Candidate: &target}})
	}
	actions := make([]agentSendCapability, 0, len(v.Actions))
	for _, a := range v.Actions {
		// Keep unrelated or a future, different authority contract in its original
		// projection rather than describing it as a direct Room send.
		if !isLarkSend(a.Operation) || a.BindingEvidence == "" && !a.DirectRoomInstructionOnly {
			return v
		}
		c := agentSendCapability{Operation: a.Operation, InputSchema: a.InputSchema, Executable: a.Executable, DisabledReason: a.DisabledReason, ReasonCode: a.ReasonCode, NextAction: a.NextAction, Required: []string{"target_ref", "turn_number", "id", "operation", "input"}}
		if a.BindingEvidence == "" {
			c.Required = append(c.Required, "policy_token", "authority")
			c.Authority = &agentSendAuthority{Type: "human_reply", Required: []string{"room_seq", "reason"}}
		}
		actions = append(actions, c)
	}
	return agentSendTargetView{TargetRef: v.TargetRef, Resolution: agentSendResolution{
		Status: r.Status, Account: r.Account, AccountLabel: r.AccountLabel, AccountOwnerConfirmed: r.AccountOwnerConfirmed,
		Target: projectNativeTarget(r.Target), TargetLabel: r.TargetLabel, Candidates: candidates, CandidatesIncomplete: r.CandidatesIncomplete,
		Complete: r.Complete, ReasonCode: r.ReasonCode, Reason: r.Reason,
	}, Actions: actions, DisabledReason: v.DisabledReason}
}

type agentSendResult struct {
	Target     *agentNativeTarget       `json:"target"`
	Account    platform.AccountIdentity `json:"account"`
	Verified   bool                     `json:"verified"`
	ResultID   platform.ExternalID      `json:"result_id,omitzero"`
	ReasonCode string                   `json:"reason_code,omitempty"`
	Message    string                   `json:"message,omitempty"`
	URL        string                   `json:"url,omitempty"`
}

func projectActionReceipt(r referenceActionReceipt, q referenceActionRequest) any {
	if r.Result != nil && r.Result.Account.Provider == "gitlab" {
		return projectGitlabActionReceipt(r, q)
	}
	provider := ""
	if r.Result != nil {
		provider = r.Result.Account.Provider
	} else if r.Account != nil {
		provider = r.Account.Provider
	}
	if provider != "lark" || !isLarkSend(q.Operation) {
		return r
	}
	if r.Result != nil && r.Result.Target.IdentityScope != r.Result.Account.IdentityScope {
		return r
	}
	var result *agentSendResult
	if a := r.Result; a != nil {
		result = &agentSendResult{projectNativeTarget(&a.Target), a.Account, r.Status == "succeeded" && a.Status == "succeeded", a.ResultID, a.ReasonCode, a.Message, a.URL}
	}
	return agentSendReceipt{r.ID, r.Status, r.Attempts, r.TargetRef, q.Operation, q.Input, projectActionAuthority(q.Authority), r.Target, r.Account, result, r.UnitVersion, r.PossibleDuplicate}
}

type agentSendReceipt struct {
	ID                string                    `json:"id"`
	Status            string                    `json:"status"`
	Attempts          int                       `json:"attempts"`
	TargetRef         string                    `json:"target_ref"`
	Operation         platform.Operation        `json:"operation"`
	Input             ActionInput               `json:"input"`
	Authority         agentActionAuthority      `json:"authority,omitzero"`
	Target            *platform.TargetIdentity  `json:"target,omitempty"`
	Account           *platform.AccountIdentity `json:"account,omitempty"`
	Result            *agentSendResult          `json:"result,omitempty"`
	UnitVersion       uint64                    `json:"unit_version,omitempty"`
	PossibleDuplicate bool                      `json:"possible_duplicate,omitempty"`
}

// GitLab projection is local to verified MR comments/replies. It does not adopt
// Lark self/group grants, and never projects policy repair feedback away.
func isGitlabComment(op platform.Operation) bool {
	return op == platform.OperationObjectComment || op == platform.OperationDiscussionReply
}

type agentGitlabTarget struct {
	agentNativeTarget
	MRIID platform.ExternalID `json:"mr_iid,omitzero"`
}
type agentGitlabResolution struct {
	Status       platform.ResolutionStatus `json:"status"`
	Account      platform.AccountIdentity  `json:"account"`
	AccountLabel string                    `json:"account_label,omitempty"`
	Target       *agentGitlabTarget        `json:"target,omitempty"`
	URL          string                    `json:"url,omitempty"`
	Complete     bool                      `json:"complete"`
	ReasonCode   string                    `json:"reason_code,omitempty"`
	Reason       string                    `json:"reason,omitempty"`
}
type agentGitlabCapability struct {
	Operation      platform.Operation `json:"operation"`
	InputSchema    json.RawMessage    `json:"input_schema"`
	Required       []string           `json:"required"`
	Authority      string             `json:"authority"`
	Executable     bool               `json:"executable"`
	DisabledReason string             `json:"disabled_reason,omitempty"`
	ReasonCode     string             `json:"reason_code,omitempty"`
	NextAction     string             `json:"next_action,omitempty"`
}
type agentGitlabTargetView struct {
	TargetRef      string                  `json:"target_ref,omitempty"`
	Resolution     agentGitlabResolution   `json:"resolution"`
	Actions        []agentGitlabCapability `json:"actions"`
	DisabledReason string                  `json:"disabled_reason,omitempty"`
}

func projectGitlabActionTarget(v actionTargetView) any {
	r := v.Resolution
	if r.Target == nil || r.Target.Object.Type != "MergeRequest" || r.Target.IdentityScope != r.Account.IdentityScope || r.Target.Message.Valid() || len(r.Candidates) > 0 {
		return v
	}
	target := &agentGitlabTarget{agentNativeTarget: *projectNativeTarget(r.Target)}
	for _, alias := range r.Aliases {
		if alias.Object.ID.Namespace == "gitlab:rest:iid" {
			target.MRIID = alias.Object.ID
		}
	}
	actions := make([]agentGitlabCapability, 0, len(v.Actions))
	for _, a := range v.Actions {
		if !isGitlabComment(a.Operation) || a.BindingEvidence != "" {
			return v
		}
		authority := "Text: existing rule, instruction or human_reply authority plus current policy_token. Reuse this verified target and valid schema with current turn_number/policy_token; resolve only if missing, ambiguous or invalidated. Ask first for an unspecified different discussion; do not guess a locator or fall back to object.comment."
		if strings.Contains(string(a.InputSchema), `"images"`) {
			authority += " With images: only human_reply {room_seq,reason} from the direct Room instruction and current policy_token; one attempt, no unknown retry."
		}
		actions = append(actions, agentGitlabCapability{a.Operation, a.InputSchema, []string{"target_ref", "turn_number", "id", "operation", "input", "policy_token", "authority"}, authority, a.Executable, a.DisabledReason, a.ReasonCode, a.NextAction})
	}
	return agentGitlabTargetView{v.TargetRef, agentGitlabResolution{r.Status, r.Account, r.AccountLabel, target, r.URL, r.Complete, r.ReasonCode, r.Reason}, actions, v.DisabledReason}
}

type agentGitlabReceipt struct {
	ID                string             `json:"id"`
	Status            string             `json:"status"`
	Attempts          int                `json:"attempts"`
	TargetRef         string             `json:"target_ref"`
	Operation         platform.Operation `json:"operation"`
	Input             ActionInput        `json:"input"`
	Result            agentSendResult    `json:"result"`
	UnitVersion       uint64             `json:"unit_version,omitempty"`
	PossibleDuplicate bool               `json:"possible_duplicate,omitempty"`
}

func projectGitlabActionReceipt(r referenceActionReceipt, q referenceActionRequest) any {
	a := r.Result
	if r.Policy != nil || a == nil || !isGitlabComment(q.Operation) || a.Target.Provider != "gitlab" || a.Target.Object.Type != "MergeRequest" || a.Target.IdentityScope != a.Account.IdentityScope || (r.Status != "succeeded" && r.Status != "unknown" && r.Status != "not_applied") {
		return r
	}
	return agentGitlabReceipt{r.ID, r.Status, r.Attempts, r.TargetRef, q.Operation, q.Input, agentSendResult{projectNativeTarget(&a.Target), a.Account, r.Status == "succeeded" && a.Status == "succeeded", a.ResultID, a.ReasonCode, a.Message, a.URL}, r.UnitVersion, r.PossibleDuplicate}
}
