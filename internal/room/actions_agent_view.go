package room

import (
	"encoding/json"
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
		if s.Provider != "lark" {
			result = append(result, s)
			continue
		}
		v := agentLarkToolEntry{Name: s.Name, Provider: s.Provider, ToolType: s.ToolType, PlatformID: s.PlatformID, PlatformName: s.PlatformName, Instance: s.Instance, Username: s.Username, Status: s.Status, Capabilities: s.Capabilities, UnavailableReason: s.UnavailableReason, ActionDisabledReason: s.ActionDisabledReason, Skill: `"$TEAMX_CLI" tools skill ` + s.Name, Workflow: "Read the Skill before the first action in each Session. Resolve selects a target; actions read queries an existing action ID. Use TeamX commands and receipts only."}
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
