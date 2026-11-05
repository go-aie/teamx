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

func (s *Store) changeConversationPolicy(id MemberIdentity, caller Caller, q conversationPolicyChange) (PolicyView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if caller != agentCallerIdentity(id) {
		return PolicyView{}, ErrIdentity
	}
	if err := s.checkFollowupsLocked(id, true); err != nil {
		return PolicyView{}, err
	}
	c := q.Change
	if c.TurnNumber == 0 {
		return PolicyView{}, ErrRequest
	}
	if _, err := s.runningTurnLocked(id, c.TurnNumber); err != nil {
		return PolicyView{}, err
	}
	if !validPolicyChange(c) || c.Kind == "decision" || c.SourceSeq == 0 || c.SourceText != "" || !handlingText(q.Basis) {
		return PolicyView{}, ErrRequest
	}
	p, path, err := s.loadPolicyLocked(id, true)
	if err != nil {
		return PolicyView{}, err
	}
	digest := policyConversationDigest(q)
	for _, r := range p.History {
		if r.Conversation != nil && r.Conversation.RequestDigest == digest {
			return policyView(p), nil
		}
	}
	if p.Revision != c.Revision {
		return PolicyView{}, ErrConflict
	}
	var old PolicyRevision
	for _, r := range policyCurrent(p.History) {
		if r.ID == c.ID {
			old = r
		}
	}
	if old.Version != c.Version {
		return PolicyView{}, ErrConflict
	}
	if old.Version != 0 && old.Kind != c.Kind {
		return PolicyView{}, ErrRequest
	}
	if len(p.History) >= maxPolicyRevisions {
		return PolicyView{}, ErrCapacity
	}
	room, err := s.loadRoom()
	if err != nil {
		return PolicyView{}, err
	}
	message, err := roomHumanMessage(room, c.SourceSeq)
	if err != nil {
		return PolicyView{}, err
	}
	if old.Version != 0 && old.Source.User != message.Author.ID {
		return PolicyView{}, ErrIdentity
	}
	if old.Version != 0 && old.Source.Seq != 0 && message.Seq <= old.Source.Seq {
		return PolicyView{}, ErrConflict
	}
	r := PolicyRevision{ID: c.ID, Version: c.Version + 1, Kind: c.Kind, Status: c.Status, Text: c.Text, Scope: c.Scope, ExpiresAt: c.ExpiresAt,
		Source: PolicySource{User: message.Author.ID, Seq: message.Seq, Text: message.Text}, ChangedBy: caller.Author, UpdatedAt: time.Now().UTC(),
		Conversation: &policyConversationEvidence{Mode: q.Mode, Basis: q.Basis, RequestDigest: digest, Message: message}}
	if q.Mode == "confirm_suggestion" {
		receipt, ok := room.FollowupNoticeReceipts[policySuggestionNoticeID(id, old)]
		if !ok || receipt.Seq != q.ProposalSeq || receipt.MembershipID != id.MembershipID || receipt.Author != caller.Author || receipt.Digest != noticeDigest(policySuggestionText(old)) {
			return PolicyView{}, ErrIdentity
		}
		r.Conversation.Proposal = &policySuggestionConfirmation{Version: old.Version, Seq: receipt.Seq, Text: policySuggestionText(old)}
	} else if q.ProposalSeq != 0 {
		return PolicyView{}, ErrRequest
	}
	if !validPolicyRevision(r) || validatePolicyConversation(r, old) != nil {
		return PolicyView{}, ErrRequest
	}
	// Exact duplicate scope must amend its existing rule. This is a deterministic
	// guard, not a claim to detect semantic overlap or resolve conflicting rules.
	if old.Version == 0 && r.Kind == "rule" {
		for _, v := range policyCurrent(p.History) {
			if v.Kind == r.Kind && v.Status != "revoked" && naturalPolicy(v).Scope == r.Scope {
				return PolicyView{}, ErrConflict
			}
		}
	}
	p.History = append(p.History, r)
	p.Revision++
	encoded, err := json.Marshal(p)
	if err != nil {
		return PolicyView{}, err
	}
	if int64(len(encoded)+1) > maxPrivateFileBytes {
		return PolicyView{}, ErrCapacity
	}
	if err := s.save(path, p, maxPrivateFileBytes); err != nil {
		return PolicyView{}, err
	}
	return policyView(p), nil
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
		if revision.Version != version || revision.Status != "suggested" || revision.Conversation == nil {
			err = ErrConflict
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

func (s *service) referencePolicyChange(_ context.Context, id MemberIdentity, caller Caller, q conversationPolicyChange) (agentPolicyView, error) {
	view, err := s.store.changeConversationPolicy(id, caller, q)
	if err != nil {
		return agentPolicyView{}, err
	} // never report "remembered" before durable save
	return agentPolicy(view.PolicySnapshot, q.Change.TurnNumber, time.Now()), nil
}
