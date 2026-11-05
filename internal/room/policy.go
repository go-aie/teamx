package room

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"time"
)

const maxPolicyRevisions = 256

// Policy is member configuration, never Session or Inbox state. Revisions are
// retained with copied user evidence so clearing public history loses no basis.
type PolicySource struct {
	User string `json:"user"`
	Seq  uint64 `json:"seq,omitempty"`
	Text string `json:"text"`
}

type PolicyScope struct {
	Description string `json:"description"`
	Provider    string `json:"provider,omitempty"`
	Instance    string `json:"instance,omitempty"`
	Account     string `json:"account,omitempty"`
	Project     string `json:"project,omitempty"`
	ObjectType  string `json:"object_type,omitempty"`
}

type PolicyRevision struct {
	ID         string       `json:"id"`
	Version    uint64       `json:"version"`
	Kind       string       `json:"kind"`   // preference, rule, decision (only in a matter)
	Status     string       `json:"status"` // suggested, active, revoked
	Text       string       `json:"text"`
	Scope      PolicyScope  `json:"scope"`
	Actions    string       `json:"actions,omitempty"`
	Conditions string       `json:"conditions,omitempty"`
	Exceptions string       `json:"exceptions,omitempty"`
	ExpiresAt  *time.Time   `json:"expires_at,omitempty"`
	Source     PolicySource `json:"source"`
	ChangedBy  Author       `json:"changed_by"`
	UpdatedAt  time.Time    `json:"updated_at"`
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
// User/source text is supplied only by a human management action. Agent writes
// must reference an actual human Room message and can only save suggestions.
type PolicyChange struct {
	ID            string      `json:"id"`
	Version       uint64      `json:"version"`
	Revision      uint64      `json:"revision"`
	Kind          string      `json:"kind"`
	Status        string      `json:"status"`
	Text          string      `json:"text"`
	Scope         PolicyScope `json:"scope"`
	Actions       string      `json:"actions,omitempty"`
	Conditions    string      `json:"conditions,omitempty"`
	Exceptions    string      `json:"exceptions,omitempty"`
	ExpiresAt     *time.Time  `json:"expires_at,omitempty"`
	SourceSeq     uint64      `json:"source_seq,omitempty"`
	SourceText    string      `json:"source_text,omitempty"`
	Seq           uint64      `json:"seq,omitempty"` // exact Inbox/matter identity for a one-off decision
	MatterID      string      `json:"matter_id,omitempty"`
	MatterVersion uint64      `json:"matter_version,omitempty"`
	TurnNumber    uint64      `json:"turn_number,omitempty"`
}

type DecisionHistory struct {
	History []PolicyRevision `json:"history"`
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
	return PolicyView{PolicySnapshot: p, Current: policyCurrent(p.History), Effective: effectivePolicy(p.History, time.Now())}
}

func validPolicyRevision(r PolicyRevision) bool {
	if !safeMembershipID(r.ID) || r.Version == 0 || !validText(r.Text) || !validText(r.Scope.Description) ||
		(r.Kind != "preference" && r.Kind != "rule" && r.Kind != "decision") ||
		(r.Status != "suggested" && r.Status != "active" && r.Status != "revoked") ||
		!validText(r.Source.User) || !validText(r.Source.Text) || r.UpdatedAt.IsZero() ||
		(r.ChangedBy.Kind != "human" && r.ChangedBy.Kind != "agent") || !validText(r.ChangedBy.ID) || (r.ChangedBy.Kind == "human" && r.ChangedBy.ID != r.Source.User) {
		return false
	}
	for _, s := range []string{r.Text, r.Scope.Description, r.Scope.Provider, r.Scope.Instance, r.Scope.Account, r.Scope.Project, r.Scope.ObjectType, r.Actions, r.Conditions, r.Exceptions} {
		if len(s) > 4096 {
			return false
		}
	}
	if len(r.Source.Text) > maxTextBytes {
		return false
	}
	if r.Kind == "preference" && (r.Actions != "" || r.Conditions != "" || r.Exceptions != "") {
		return false
	}
	if r.Kind == "rule" && (!validText(r.Scope.Provider) || !validText(r.Scope.Instance) || !validText(r.Scope.Account) || !validText(r.Scope.Project) || !validText(r.Actions) || !validText(r.Conditions) || !validText(r.Exceptions)) {
		return false
	}
	if r.Kind == "decision" && !validText(r.Actions) {
		return false
	}
	return r.ExpiresAt == nil || !r.ExpiresAt.IsZero()
}

func validatePolicyHistory(history []PolicyRevision, decision bool) error {
	if len(history) > maxPolicyRevisions {
		return ErrInvalid
	}
	latest := map[string]PolicyRevision{}
	for _, r := range history {
		if !validPolicyRevision(r) || (r.Kind == "decision") != decision {
			return ErrInvalid
		}
		old, exists := latest[r.ID]
		if r.Version != old.Version+1 || (exists && (r.Kind != old.Kind || r.Source.User != old.Source.User)) {
			return ErrInvalid
		}
		if r.ChangedBy.Kind == "agent" && (r.Status != "suggested" || r.Source.Seq == 0 || (exists && old.Status != "suggested")) {
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
	return p, path, validatePolicyHistory(p.History, false)
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
	r := PolicyRevision{ID: c.ID, Version: c.Version + 1, Kind: c.Kind, Status: c.Status, Text: c.Text, Scope: c.Scope, Actions: c.Actions, Conditions: c.Conditions, Exceptions: c.Exceptions, ExpiresAt: c.ExpiresAt, ChangedBy: caller.Author, UpdatedAt: time.Now().UTC()}
	var old PolicyRevision
	for _, v := range history {
		if v.ID == c.ID {
			old = v
		}
	}
	if old.Version != c.Version {
		return r, ErrConflict
	}
	if old.Version != 0 && old.Kind != c.Kind {
		return r, ErrRequest
	}
	if len(history) >= maxPolicyRevisions {
		return r, ErrCapacity
	}
	if caller.Kind == "agent" {
		if caller.ID != id.AgentID || caller.MembershipID != id.MembershipID || c.Status != "suggested" || c.SourceSeq == 0 || c.SourceText != "" || (old.Version != 0 && old.Status != "suggested") {
			return r, ErrIdentity
		}
		if c.TurnNumber == 0 {
			return r, ErrConflict
		}
		if _, err := s.runningTurnLocked(id, c.TurnNumber); err != nil {
			return r, err
		}
	} else if caller.Kind != "human" || caller.MembershipID != "" || !validText(caller.ID) {
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
	if c.Kind == "decision" || c.Seq != 0 || c.MatterID != "" {
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

func (s *Store) changeDecision(id MemberIdentity, caller Caller, c PolicyChange) (MatterRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, _, err := s.memberPath(id, "policy.json", true); err != nil {
		return MatterRecord{}, err
	}
	if c.Kind != "decision" || c.Seq == 0 || !safeMembershipID(c.MatterID) {
		return MatterRecord{}, ErrRequest
	}
	e, err := s.inboxEntryLocked(id, c.Seq)
	if err != nil {
		return MatterRecord{}, err
	}
	record, err := s.loadMatterLocked(e)
	if err != nil {
		return record, err
	}
	for i := range record.Matters {
		m := &record.Matters[i]
		if m.ID != c.MatterID {
			continue
		}
		if m.Version != c.MatterVersion {
			return record, ErrConflict
		}
		history := []PolicyRevision{}
		if m.Decisions != nil {
			history = m.Decisions.History
		}
		r, err := s.policyRevisionLocked(id, caller, c, history)
		if err != nil {
			return record, err
		}
		m.Decisions = &DecisionHistory{History: append(history, r)}
		m.Version++
		m.ProgressVersion++
		m.UpdatedAt = time.Now().UTC()
		return record, s.save(s.matterPath(e), record, matterRecordBytes)
	}
	return record, ErrRequest
}
