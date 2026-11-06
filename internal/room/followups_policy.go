package room

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"
)

// Conversation changes retain real human evidence and the Agent interpretation.
type conversationPolicyChange struct {
	Change      PolicyChange `json:"change"`
	Mode        string       `json:"mode"`  // explicit_long_term, inferred_habit, confirm_suggestion
	Basis       string       `json:"basis"` // Agent's interpretation, retained separately from actual user text
	ProposalSeq uint64       `json:"proposal_seq,omitempty"`
}

type policyConversationEvidence struct {
	Mode          string                        `json:"mode"`
	Basis         string                        `json:"basis"`
	RequestDigest string                        `json:"request_digest"`
	Message       RoomMessage                   `json:"message"`
	Proposal      *policySuggestionConfirmation `json:"proposal,omitempty"`
}

type policySuggestionConfirmation struct {
	Version uint64 `json:"version"`
	Seq     uint64 `json:"room_seq"`
	Text    string `json:"text"`
}

// Record is the exact committed revision, including when an old request is
// reused after later amendments. PolicyView still describes the current policy.
type conversationPolicyResult struct {
	PolicyView
	Record PolicyRevision
}

// Only update adds record; reads, turn supply and policy_changed keep their
// existing effective-policy contract.
type agentPolicyUpdateResult struct {
	agentPolicyView
	Record PolicyRevision `json:"record"`
}

func policyConversationDigest(q conversationPolicyChange) string {
	q.Change.TurnNumber = 0 // a new Session/turn does not create a new business change
	b, _ := json.Marshal(q)
	return noticeDigest(string(b))
}

func samePolicyBody(a, b PolicyRevision) bool {
	a, b = naturalPolicy(a), naturalPolicy(b)
	return a.Kind == b.Kind && a.Text == b.Text && a.Scope == b.Scope && a.Actions == b.Actions && a.Conditions == b.Conditions && a.Exceptions == b.Exceptions && reflect.DeepEqual(a.ExpiresAt, b.ExpiresAt)
}

func policySuggestionText(r PolicyRevision) string {
	text := fmt.Sprintf("建议今后采用以下%s：\n%s", map[string]string{"rule": "规则", "preference": "偏好"}[r.Kind], r.Text)
	// Preserve exact historical proposal receipts; new records have no description.
	if r.Scope.Description != "" {
		text += "\n适用范围：" + r.Scope.Description
	}
	for _, field := range [][2]string{{"平台", r.Scope.Provider}, {"实例", r.Scope.Instance}, {"账号", r.Scope.Account}, {"项目", r.Scope.Project}, {"对象类型", r.Scope.ObjectType}, {"允许动作", r.Actions}, {"条件", r.Conditions}, {"例外", r.Exceptions}} {
		if field[1] != "" {
			text += "\n" + field[0] + "：" + field[1]
		}
	}
	if r.Scope.AccountID.Valid() {
		text += "\n原生账号：" + r.Scope.AccountID.Namespace + ":" + r.Scope.AccountID.Value
		if r.Scope.Tenant.Valid() {
			text += "\n租户：" + r.Scope.Tenant.Namespace + ":" + r.Scope.Tenant.Value
		}
	}
	if r.Scope.NoContainers {
		text += "\n容器范围：仅无父容器的对象"
	}
	if r.ExpiresAt != nil {
		text += "\n有效期至：" + r.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return text + "\n是否将这份建议作为今后的处理方式？"
}

func policySuggestionNoticeID(id MemberIdentity, r PolicyRevision) string {
	return followupNoticeID(id, "policy", r.ID, r.Version, policySuggestionText(r))
}

func validatePolicyConversation(r, old PolicyRevision) error {
	e := r.Conversation
	if e == nil || r.ChangedBy.Kind != "agent" || !validHumanEvidence(e.Message) || !handlingText(e.Basis) ||
		!validOpaqueRef(e.RequestDigest, "") || r.Source != (PolicySource{User: e.Message.Author.ID, Seq: e.Message.Seq, Text: e.Message.Text}) {
		return ErrInvalid
	}
	if old.Version != 0 && old.Source.Seq != 0 && e.Message.Seq <= old.Source.Seq {
		return ErrInvalid
	}
	switch e.Mode {
	case "explicit_long_term":
		if (r.Status != "active" && r.Status != "revoked") || (r.Status == "revoked" && old.Version == 0) || e.Proposal != nil {
			return ErrInvalid
		}
	case "inferred_habit":
		if r.Status != "suggested" || e.Proposal != nil || (old.Version != 0 && old.Status != "suggested") {
			return ErrInvalid
		}
	case "confirm_suggestion":
		if e.Proposal == nil || old.Status != "suggested" || old.Version != e.Proposal.Version || r.Status != "active" ||
			!samePolicyBody(r, old) || e.Proposal.Seq == 0 || e.Proposal.Text != policySuggestionText(old) ||
			e.Message.Seq <= e.Proposal.Seq || (e.Message.ReplyTo != 0 && e.Message.ReplyTo != e.Proposal.Seq) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func (s *Store) changeConversationPolicy(id MemberIdentity, caller Caller, q conversationPolicyChange) (conversationPolicyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changeConversationPolicyLocked(id, caller, q)
}

func (s *Store) changeConversationPolicyLocked(id MemberIdentity, caller Caller, q conversationPolicyChange) (conversationPolicyResult, error) {
	if caller != agentCallerIdentity(id) {
		return conversationPolicyResult{}, ErrIdentity
	}
	if err := s.checkFollowupsLocked(id, true); err != nil {
		return conversationPolicyResult{}, err
	}
	c := q.Change
	if c.TurnNumber == 0 {
		return conversationPolicyResult{}, invalidField("change.turn_number", "required positive current_turn")
	}
	if _, err := s.runningTurnLocked(id, c.TurnNumber); err != nil {
		return conversationPolicyResult{}, prefixDiagnostic(err, "change.")
	}
	if !validPolicyChange(c) {
		return conversationPolicyResult{}, policyChangeDiagnostic(c)
	}
	if c.Kind == "decision" {
		return conversationPolicyResult{}, invalidField("change.kind", "must be rule or preference")
	}
	if c.SourceSeq == 0 {
		return conversationPolicyResult{}, invalidField("change.source_seq", "required real human Room message sequence")
	}
	if c.SourceText != "" {
		return conversationPolicyResult{}, invalidField("change.source_text", "must be omitted; server reads the original human message")
	}
	if !handlingText(q.Basis) {
		return conversationPolicyResult{}, invalidField("basis", "required nonempty text, at most 4096 bytes")
	}
	p, path, err := s.loadPolicyLocked(id, true)
	if err != nil {
		return conversationPolicyResult{}, err
	}
	digest := policyConversationDigest(q)
	for _, r := range p.History {
		if r.Conversation != nil && r.Conversation.RequestDigest == digest {
			return conversationPolicyResult{PolicyView: policyView(p), Record: r}, nil
		}
	}
	if p.Revision != c.Revision {
		return conversationPolicyResult{}, versionConflict("change.revision", c.Revision, p.Revision, "Read policy read and reassess; do not silently replace the observed revision.")
	}
	var old PolicyRevision
	for _, r := range policyCurrent(p.History) {
		if r.ID == c.ID {
			old = r
		}
	}
	if old.Version != c.Version {
		return conversationPolicyResult{}, versionConflict("change.version", c.Version, old.Version, "Read policy read --id ID and reassess the exact record.")
	}
	if old.Version != 0 && old.Kind != c.Kind {
		return conversationPolicyResult{}, invalidField("change.kind", "cannot change the kind of an existing record")
	}
	if len(p.History) >= maxPolicyRevisions {
		return conversationPolicyResult{}, ErrCapacity
	}
	room, err := s.loadRoom()
	if err != nil {
		return conversationPolicyResult{}, err
	}
	message, err := roomHumanMessage(room, c.SourceSeq)
	if err != nil {
		return conversationPolicyResult{}, &requestDiagnostic{cause: err, Field: "change.source_seq", Message: "does not reference a saved valid human Room message", Hint: "Use the actual saved human Room message sequence; never guess a sequence number."}
	}
	if old.Version != 0 && old.Source.User != message.Author.ID {
		return conversationPolicyResult{}, ErrIdentity
	}
	if old.Version != 0 && old.Source.Seq != 0 && message.Seq <= old.Source.Seq {
		return conversationPolicyResult{}, conflictReason("change.source_seq", "human evidence is not newer than the saved record", "Read policy read --id ID and use the actual new human instruction.")
	}
	r := PolicyRevision{ID: c.ID, Version: c.Version + 1, Kind: c.Kind, Status: c.Status, Text: c.Text, Scope: c.Scope, ExpiresAt: c.ExpiresAt,
		Source: PolicySource{User: message.Author.ID, Seq: message.Seq, Text: message.Text}, ChangedBy: caller.Author, UpdatedAt: time.Now().UTC(),
		Conversation: &policyConversationEvidence{Mode: q.Mode, Basis: q.Basis, RequestDigest: digest, Message: message}}
	if q.Mode == "confirm_suggestion" {
		receipt, ok := room.FollowupNoticeReceipts[policySuggestionNoticeID(id, old)]
		if !ok || receipt.Seq != q.ProposalSeq || receipt.MembershipID != id.MembershipID || receipt.Author != caller.Author || receipt.Digest != noticeDigest(policySuggestionText(old)) {
			return conversationPolicyResult{}, ErrIdentity
		}
		r.Conversation.Proposal = &policySuggestionConfirmation{Version: old.Version, Seq: receipt.Seq, Text: policySuggestionText(old)}
	} else if q.ProposalSeq != 0 {
		return conversationPolicyResult{}, invalidField("proposal_seq", "only supported with confirm_suggestion")
	}
	if !validPolicyRevision(r) {
		return conversationPolicyResult{}, invalidField("change", "invalid record ID/kind/status/text/scope/expiry; see policy update --help")
	}
	if validatePolicyConversation(r, old) != nil {
		if q.Mode == "inferred_habit" && old.Version != 0 && old.Status != "suggested" {
			return conversationPolicyResult{}, invalidField("mode", "suggest can only create or amend a suggested record; use save with a real new lasting instruction to amend an active/revoked record")
		}
		return conversationPolicyResult{}, invalidField("mode", "mode/status or suggestion confirmation does not match the saved record and real human evidence; see policy update --help")
	}
	// Exact duplicate scope must amend its existing rule. This is a deterministic
	// guard, not a claim to detect semantic overlap or resolve conflicting rules.
	if old.Version == 0 && r.Kind == "rule" {
		for _, v := range policyCurrent(p.History) {
			if v.Kind == r.Kind && v.Status != "revoked" && naturalPolicy(v).Scope == r.Scope {
				return conversationPolicyResult{}, conflictReason("change.scope", "a non-revoked rule already has this exact scope", "Read policy read and amend the existing rule/version rather than creating a duplicate.")
			}
		}
	}
	p.History = append(p.History, r)
	p.Revision++
	encoded, err := json.Marshal(p)
	if err != nil {
		return conversationPolicyResult{}, err
	}
	if int64(len(encoded)+1) > maxPrivateFileBytes {
		return conversationPolicyResult{}, ErrCapacity
	}
	if err := s.save(path, p, maxPrivateFileBytes); err != nil {
		return conversationPolicyResult{}, err
	}
	return conversationPolicyResult{PolicyView: policyView(p), Record: r}, nil
}

// Publishes the exact saved suggestion version. Room's durable receipt prevents
// repeated questions after a lost response, history clear, or Session rotation.
func (s *service) publishPolicySuggestion(ctx context.Context, id MemberIdentity, turn uint64, key string, version uint64) (PostReceipt, error) {
	s.store.mu.Lock()
	err := s.store.checkFollowupsLocked(id, false)
	if err == nil && turn == 0 {
		err = ErrRequest
	}
	if err == nil {
		_, err = s.store.runningTurnLocked(id, turn)
	}
	var p PolicySnapshot
	if err == nil {
		p, _, err = s.store.loadPolicyLocked(id, false)
	}
	var revision PolicyRevision
	if err == nil {
		for _, r := range policyCurrent(p.History) {
			if r.ID == key {
				revision = r
			}
		}
		if revision.Version != version {
			err = versionConflict("version", version, revision.Version, "Read policy read --id ID before publishing the exact suggestion.")
		} else if revision.Status != "suggested" || revision.Conversation == nil {
			err = conflictReason("id", "record is not a conversation-backed suggestion", "Read policy read --id ID before publishing.")
		}
	}
	s.store.mu.Unlock()
	if err != nil {
		return PostReceipt{}, err
	}
	if len(policySuggestionText(revision)) > maxTextBytes {
		return PostReceipt{}, ErrCapacity
	}
	return s.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: agentCallerIdentity(id),
		ClientMessageID: policySuggestionNoticeID(id, revision), Text: policySuggestionText(revision), FollowupNotice: true})
}

func (s *service) referencePolicyChange(_ context.Context, id MemberIdentity, caller Caller, q conversationPolicyChange) (agentPolicyUpdateResult, error) {
	view, err := s.store.changeConversationPolicy(id, caller, q)
	if err != nil {
		return agentPolicyUpdateResult{}, err
	} // never report "remembered" before durable save
	return agentPolicyUpdateResult{agentPolicyView: agentPolicy(view.PolicySnapshot, q.Change.TurnNumber, time.Now()), Record: view.Record}, nil
}

func (s *service) changeAgentPolicy(_ context.Context, id MemberIdentity, caller Caller, q agentPolicyChange) (agentPolicyUpdateResult, error) {
	view, err := s.store.changeAgentPolicy(id, caller, q)
	if err != nil {
		return agentPolicyUpdateResult{}, agentPolicyDiagnostic(err)
	}
	return agentPolicyUpdateResult{agentPolicyView: agentPolicy(view.PolicySnapshot, q.TurnNumber, time.Now()), Record: view.Record}, nil
}
