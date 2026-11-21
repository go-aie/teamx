package room

import (
	"encoding/json"
	"strings"

	"github.com/go-aie/teamx/internal/platform"
)

// These are transport projections only. Native resolutions, action specs and
// frozen evidence remain intact in storage and in actions read's detail.
type agentTargetCandidate struct {
	Label   string                 `json:"label"`
	Locator platform.TargetLocator `json:"locator"`
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
