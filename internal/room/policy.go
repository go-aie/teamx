package room

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"time"
)

const (
	maxPolicyRevisions = 256
	maxPolicyTextBytes = 32 << 10
)

// Policy is member configuration, never Session or Inbox state. Revisions are
// retained with copied user evidence so clearing public history loses no basis.
type PolicySource struct {
	User string `json:"user"`
	Seq  uint64 `json:"seq,omitempty"`
	Text string `json:"text"`
}

type PolicyRevision struct {
	ID           string                      `json:"id"`
	Version      uint64                      `json:"version"`
	Status       string                      `json:"status"` // suggested, active, revoked
	Text         string                      `json:"text"`
	ExpiresAt    *time.Time                  `json:"expires_at,omitempty"`
	Source       PolicySource                `json:"source"`
	ChangedBy    Author                      `json:"changed_by"`
	UpdatedAt    time.Time                   `json:"updated_at"`
	Conversation *policyConversationEvidence `json:"conversation,omitempty"`
}

type PolicySnapshot struct {
	Version int `json:"version"`
	MemberIdentity
	Revision uint64           `json:"revision"`
	History  []PolicyRevision `json:"history"`
}

type PolicyView struct {
	PolicySnapshot
	CurrentTurn uint64           `json:"current_turn,omitempty"`
	Current     []PolicyRevision `json:"current"`
	Effective   []PolicyRevision `json:"effective"`
}

// Agent reads and turn supply share one effective view. History remains a
// human management view and is never used as the Session's delivery marker.
type policyContext struct {
	MemberIdentity
	PolicyToken string `json:"policy_token"`
	Revision    uint64 `json:"revision"`
	CurrentTurn uint64 `json:"current_turn"`
}

type agentPolicyView struct {
	policyContext
	Effective []PolicyRevision `json:"effective"`
}

func effectivePolicy(history []PolicyRevision, now time.Time) []PolicyRevision {
	effective := []PolicyRevision{}
	for _, r := range policyCurrent(history) {
		if r.Status == "active" && (r.ExpiresAt == nil || r.ExpiresAt.After(now)) {
			effective = append(effective, r)
		}
	}
	return effective
}

func agentPolicy(p PolicySnapshot, turn uint64, now time.Time) agentPolicyView {
	effective := effectivePolicy(p.History, now)
	// Exclude storage revision, turn and read time: suggestions alone must not
	// invalidate an unchanged effective body. Expiry removes records naturally.
	body, _ := json.Marshal(struct {
		MemberIdentity
		Effective []PolicyRevision `json:"effective"`
	}{p.MemberIdentity, effective})
	digest := sha256.Sum256(body)
	return agentPolicyView{policyContext: policyContext{MemberIdentity: p.MemberIdentity,
		PolicyToken: hex.EncodeToString(digest[:]), Revision: p.Revision, CurrentTurn: turn}, Effective: effective}
}

// Version is the expected record version; Revision is the expected file revision.
// Human management supplies source text; Agent changes use the conversation
// service with real human Room evidence.
type PolicyChange struct {
	ID         string     `json:"id"`
	Version    uint64     `json:"version"`
	Revision   uint64     `json:"revision"`
	Status     string     `json:"status"`
	Text       string     `json:"text"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	SourceSeq  uint64     `json:"source_seq,omitempty"`
	SourceText string     `json:"source_text,omitempty"`
	TurnNumber uint64     `json:"turn_number,omitempty"`
}

func policyCurrent(history []PolicyRevision) []PolicyRevision {
	current := []PolicyRevision{}
	indexes := map[string]int{}
	for _, r := range history {
		if i, ok := indexes[r.ID]; ok {
			current[i] = r
		} else {
			indexes[r.ID] = len(current)
			current = append(current, r)
		}
	}
	return current
}

func policyView(p PolicySnapshot) PolicyView {
	current := policyCurrent(p.History)
	return PolicyView{PolicySnapshot: p, Current: current, Effective: effectivePolicy(p.History, time.Now())}
}

func validPolicyChange(c PolicyChange) bool {
	return validText(c.Text) && len(c.Text) <= maxPolicyTextBytes
}

func validPolicyRevision(r PolicyRevision) bool {
	if !safeMembershipID(r.ID) || r.Version == 0 || !validText(r.Text) || len(r.Text) > maxPolicyTextBytes ||
		(r.Status != "suggested" && r.Status != "active" && r.Status != "revoked") ||
		!validText(r.Source.User) || !validText(r.Source.Text) || r.UpdatedAt.IsZero() ||
		(r.ChangedBy.Kind != "human" && r.ChangedBy.Kind != "agent") || !validText(r.ChangedBy.ID) || (r.ChangedBy.Kind == "human" && r.ChangedBy.ID != r.Source.User) {
		return false
	}
	if len(r.Source.Text) > maxPolicyTextBytes {
		return false
	}
	return r.ExpiresAt == nil || !r.ExpiresAt.IsZero()
}

func validatePolicyHistory(history []PolicyRevision) error {
	if len(history) > maxPolicyRevisions {
		return ErrInvalid
	}
	latest := map[string]PolicyRevision{}
	for _, r := range history {
		if !validPolicyRevision(r) {
			return ErrInvalid
		}
		old, exists := latest[r.ID]
		if r.Version != old.Version+1 || (exists && r.Source.User != old.Source.User) {
			return ErrInvalid
		}
		if r.Conversation != nil {
			if validatePolicyConversation(r, old) != nil {
				return ErrInvalid
			}
		} else if r.ChangedBy.Kind == "agent" {
			return ErrInvalid
		}
		latest[r.ID] = r
	}
	return nil
}

func (s *Store) loadPolicyLocked(id MemberIdentity, writing bool) (PolicySnapshot, string, error) {
	path, _, err := s.memberPath(id, "policy.json", writing)
	var p PolicySnapshot
	if err != nil {
		return p, path, err
	}
	err = readSnapshot(path, &p, maxPrivateFileBytes)
	if errors.Is(err, os.ErrNotExist) {
		return PolicySnapshot{Version: 1, MemberIdentity: id, History: []PolicyRevision{}}, path, nil
	}
	if err != nil {
		return p, path, err
	}
	if p.Version != 1 || p.MemberIdentity != id || p.Revision != uint64(len(p.History)) || p.History == nil {
		return p, path, ErrInvalid
	}
	return p, path, validatePolicyHistory(p.History)
}

func (s *Store) readPolicy(id MemberIdentity) (PolicyView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, _, err := s.loadPolicyLocked(id, false)
	return policyView(p), err
}

func (s *Store) readAgentPolicy(id MemberIdentity) (agentPolicyView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, _, err := s.loadPolicyLocked(id, false)
	if err != nil {
		return agentPolicyView{}, err
	}
	agent, err := s.runningTurnLocked(id, 0)
	if err != nil && !errors.Is(err, ErrConflict) && !errors.Is(err, os.ErrNotExist) {
		return agentPolicyView{}, err
	}
	turn := uint64(0)
	if err == nil {
		turn = agent.TurnNumber
	}
	return agentPolicy(p, turn, time.Now()), nil
}

func (s *Store) policyRevisionLocked(id MemberIdentity, caller Caller, c PolicyChange, history []PolicyRevision) (PolicyRevision, error) {
	r := PolicyRevision{ID: c.ID, Version: c.Version + 1, Status: c.Status, Text: c.Text, ExpiresAt: c.ExpiresAt, ChangedBy: caller.Author, UpdatedAt: time.Now().UTC()}
	var old PolicyRevision
	for _, v := range history {
		if v.ID == c.ID {
			old = v
		}
	}
	if old.Version != c.Version {
		return r, ErrConflict
	}
	if len(history) >= maxPolicyRevisions {
		return r, ErrCapacity
	}
	if caller.Kind != "human" || caller.MembershipID != "" || !validText(caller.ID) {
		return r, ErrIdentity
	}

	if c.SourceSeq != 0 {
		room, err := s.loadRoom()
		if err != nil {
			return r, err
		}
		for _, m := range room.Messages {
			if m.Seq == c.SourceSeq && m.Author.Kind == "human" {
				r.Source = PolicySource{User: m.Author.ID, Seq: m.Seq, Text: m.Text}
				break
			}
		}
		if r.Source.Seq == 0 || (caller.Kind == "human" && caller.ID != r.Source.User) {
			return r, ErrIdentity
		}
	} else {
		r.Source = PolicySource{User: caller.ID, Text: c.SourceText}
	}
	if old.Version != 0 && old.Source.User != r.Source.User {
		return r, ErrIdentity
	}
	if !validPolicyRevision(r) {
		return r, ErrRequest
	}
	return r, nil
}

func (s *Store) changePolicy(id MemberIdentity, caller Caller, c PolicyChange) (PolicyView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, path, err := s.loadPolicyLocked(id, true)
	if err != nil {
		return PolicyView{}, err
	}
	if !validPolicyChange(c) {
		return PolicyView{}, ErrRequest
	}
	if p.Revision != c.Revision {
		return PolicyView{}, ErrConflict
	}
	r, err := s.policyRevisionLocked(id, caller, c, p.History)
	if err != nil {
		return PolicyView{}, err
	}
	p.History = append(p.History, r)
	p.Revision++
	if err := s.save(path, p, maxPrivateFileBytes); err != nil {
		return PolicyView{}, err
	}
	return policyView(p), nil
}
