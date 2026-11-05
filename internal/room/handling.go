package room

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
)

// These are member business records, not tool grants or a remote execution RPC.
// A prepared action is already uncertain: crashing before/after the tool call
// cannot establish whether the remote side changed. Nothing here replays tools.
type MatterAction struct {
	ID                string          `json:"id"`
	Version           uint64          `json:"version"`
	TurnNumber        uint64          `json:"turn_number"`
	LastTurn          uint64          `json:"last_turn"`
	Target            ActionTarget    `json:"target"`
	Intent            string          `json:"intent"`
	Verification      string          `json:"verification"`
	Authority         ActionAuthority `json:"authority"`
	Source            PolicyRevision  `json:"source"`
	Status            string          `json:"status"` // unknown, succeeded, not_applied
	Evidence          string          `json:"evidence,omitempty"`
	Reconciliation    string          `json:"reconciliation,omitempty"`
	UpdatedAt         time.Time       `json:"updated_at"`
	Execution         *ActionRequest  `json:"execution,omitempty"`
	Attempts          int             `json:"attempts,omitempty"`
	RemoteID          string          `json:"remote_id,omitempty"`
	RemoteURL         string          `json:"remote_url,omitempty"`
	PossibleDuplicate bool            `json:"possible_duplicate,omitempty"`
}

type ActionTarget struct {
	Tool         string                   `json:"tool"`
	Provider     string                   `json:"provider"`
	Instance     string                   `json:"instance"`
	Account      string                   `json:"account"` // actual executing account, not notification recipient
	ProjectID    platform.ExternalID      `json:"project_id"`
	Project      string                   `json:"project,omitempty"` // verified path, for path-scoped rules
	Object       platform.ObjectReference `json:"object"`
	Operation    string                   `json:"operation"`
	URL          string                   `json:"url"`
	NoteID       platform.ExternalID      `json:"note_id,omitzero"`
	DiscussionID platform.ExternalID      `json:"discussion_id,omitzero"`
}

type ActionAuthority struct {
	PolicyRevision  uint64 `json:"policy_revision"`
	RuleID          string `json:"rule_id,omitempty"`
	RuleVersion     uint64 `json:"rule_version,omitempty"`
	DecisionID      string `json:"decision_id,omitempty"`
	DecisionVersion uint64 `json:"decision_version,omitempty"`
	RoomSeq         uint64 `json:"room_seq,omitempty"`
	Reason          string `json:"reason"` // why scope, conditions and exceptions cover this exact action
}

type MatterCommand struct {
	Seq           uint64 `json:"seq"`
	MatterID      string `json:"matter_id"`
	MatterVersion uint64 `json:"matter_version"`
	TurnNumber    uint64 `json:"turn_number"`
}

type ActionChange struct {
	MatterCommand
	ID             string          `json:"id"`
	Version        uint64          `json:"version"`
	Target         ActionTarget    `json:"target"`
	Intent         string          `json:"intent"`
	Verification   string          `json:"verification"`
	Authority      ActionAuthority `json:"authority"`
	Status         string          `json:"status"`
	Evidence       string          `json:"evidence"`
	Reconciliation string          `json:"reconciliation"`
}

func handlingText(text string) bool { return validText(text) && len(text) <= 4096 }

func validActionTarget(t ActionTarget) bool {
	r := platform.NotificationReference{Version: 1, ProjectID: t.ProjectID, Object: t.Object, NoteID: t.NoteID, DiscussionID: t.DiscussionID}
	_, origin, err := connection.Normalize(t.Provider, t.Instance)
	u, urlErr := url.Parse(t.URL)
	return err == nil && urlErr == nil && u.User == nil && u.Scheme+"://"+strings.ToLower(u.Host) == origin && len(t.Project) <= 4096 && r.Valid() && t.ProjectID.Value != "" && t.Object.Type != "" &&
		(t.Object.ID.Value != "" || t.Object.IID.Value != "") && handlingText(t.Tool) && handlingText(t.Provider) &&
		handlingText(t.Instance) && handlingText(t.Account) && handlingText(t.Operation) && handlingText(t.URL)
}

func unresolvedAction(m Matter) bool {
	for _, a := range m.Actions {
		if a.Status == "unknown" {
			return true
		}
	}
	return false
}

func touchMatter(m *Matter, turn uint64, progress bool) {
	m.Version++
	if progress {
		m.ProgressVersion++
	}
	m.TurnNumber, m.UpdatedAt = turn, time.Now().UTC()
}

func (s *Store) handlingMatterLocked(id MemberIdentity, c MatterCommand) (InboxEntry, MatterRecord, int, error) {
	if c.Seq == 0 || !safeMembershipID(c.MatterID) || c.TurnNumber == 0 {
		return InboxEntry{}, MatterRecord{}, 0, ErrRequest
	}
	if _, err := s.runningTurnLocked(id, c.TurnNumber); err != nil {
		return InboxEntry{}, MatterRecord{}, 0, err
	}
	e, err := s.inboxEntryLocked(id, c.Seq)
	if err != nil {
		return e, MatterRecord{}, 0, err
	}
	r, err := s.loadMatterLocked(e)
	if err != nil {
		return e, r, 0, err
	}
	for i, m := range r.Matters {
		if m.ID == c.MatterID {
			if m.Version != c.MatterVersion {
				return e, r, i, ErrConflict
			}
			return e, r, i, nil
		}
	}
	return e, r, 0, ErrRequest
}

// Room sources and linked answers are copied server-side. They survive a clear;
// an unrelated/ambiguous answer still cannot become authorization by linkage.
func (s *Store) humanSourceLocked(seq uint64, m Matter) (PolicySource, error) {
	r, err := s.loadRoom()
	if err != nil {
		return PolicySource{}, err
	}
	for _, msg := range r.Messages {
		if msg.Seq == seq && msg.Author.Kind == "human" {
			return PolicySource{User: msg.Author.ID, Seq: seq, Text: msg.Text}, nil
		}
	}
	for _, n := range m.Notices {
		for _, a := range n.Answers {
			if a.Message.Seq == seq {
				return PolicySource{User: a.Message.Author.ID, Seq: seq, Text: a.Message.Text}, nil
			}
		}
	}
	for _, a := range m.Actions {
		if a.Source.Kind == "instruction" && a.Source.Source.Seq == seq {
			return a.Source.Source, nil
		}
	}
	return PolicySource{}, ErrIdentity
}

func (s *Store) actionAuthorityLocked(id MemberIdentity, m Matter, a ActionAuthority) (PolicyRevision, error) {
	p, _, err := s.loadPolicyLocked(id, false)
	if err != nil {
		return PolicyRevision{}, err
	}
	if a.PolicyRevision != p.Revision {
		return PolicyRevision{}, ErrConflict
	}
	if !handlingText(a.Reason) {
		return PolicyRevision{}, ErrRequest
	}
	count := 0
	if a.RuleID != "" {
		count++
	}
	if a.DecisionID != "" {
		count++
	}
	if a.RoomSeq != 0 {
		count++
	}
	if count != 1 || (a.RuleID == "") != (a.RuleVersion == 0) || (a.DecisionID == "") != (a.DecisionVersion == 0) {
		return PolicyRevision{}, ErrRequest
	}
	history, key, version := p.History, a.RuleID, a.RuleVersion
	if a.DecisionID != "" {
		history, key, version = nil, a.DecisionID, a.DecisionVersion
		if m.Decisions != nil {
			history = m.Decisions.History
		}
	}
	if a.RoomSeq != 0 {
		source, err := s.humanSourceLocked(a.RoomSeq, m)
		return PolicyRevision{Kind: "instruction", Status: "active", Source: source}, err
	}
	for _, r := range policyCurrent(history) {
		if r.ID == key && r.Version == version && r.Status == "active" && r.Kind != "preference" &&
			(r.ExpiresAt == nil || r.ExpiresAt.After(time.Now())) {
			return r, nil
		}
	}
	return PolicyRevision{}, ErrConflict
}

func targetMatchesSource(t ActionTarget, source platform.SourceRef) bool {
	_, origin, err := connection.Normalize(source.Provider, source.Instance)
	_, targetOrigin, targetErr := connection.Normalize(t.Provider, t.Instance)
	if err != nil || targetErr != nil || t.Provider != source.Provider || origin != targetOrigin {
		return false
	}
	r := source.Reference
	if r.ProjectID.Value != "" && t.ProjectID != r.ProjectID {
		return false
	}
	if r.Object.Type != "" && t.Object.Type != r.Object.Type {
		return false
	}
	// Do not manufacture aliases when one native identity is already known.
	if r.Object.ID.Value != "" && t.Object.ID != r.Object.ID {
		return false
	}
	if r.Object.IID.Value != "" && t.Object.IID != r.Object.IID {
		return false
	}
	return true
}

func (s *Store) changeAction(id MemberIdentity, c ActionChange, prepare bool) (MatterRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, r, i, err := s.handlingMatterLocked(id, c.MatterCommand)
	if err != nil {
		return r, err
	}
	m := &r.Matters[i]
	if !safeMembershipID(c.ID) {
		return r, ErrRequest
	}
	if prepare {
		if err := s.checkMatterPrimaryLocked(id, MatterRef{Seq: c.Seq, ID: c.MatterID}); err != nil {
			return r, err
		}
		if c.Version != 0 || !validActionTarget(c.Target) || !handlingText(c.Intent) || !handlingText(c.Verification) ||
			!targetMatchesSource(c.Target, r.Source) || c.Status != "" || c.Evidence != "" || c.Reconciliation != "" {
			return r, ErrRequest
		}
		for _, a := range m.Actions {
			if a.ID == c.ID {
				return r, ErrConflict
			}
		}
		if len(m.Actions) >= 32 {
			return r, ErrCapacity
		}
		source, err := s.actionAuthorityLocked(id, *m, c.Authority)
		if err != nil {
			return r, err
		}
		if !authorityCoversTarget(source, c.Target) {
			return r, ErrConflict
		}
		m.Actions = append(m.Actions, MatterAction{ID: c.ID, Version: 1, TurnNumber: c.TurnNumber, LastTurn: c.TurnNumber,
			Target: c.Target, Intent: c.Intent, Verification: c.Verification, Authority: c.Authority, Source: source, Status: "unknown", UpdatedAt: time.Now().UTC()})
		m.Status, m.Next = "unknown", "核实动作 "+c.ID+" 的远端结果；不得自动重发"
	} else {
		if c.Target != (ActionTarget{}) || c.Authority != (ActionAuthority{}) || c.Intent != "" || c.Verification != "" ||
			(c.Status != "unknown" && c.Status != "succeeded" && c.Status != "not_applied") || !handlingText(c.Evidence) || len(c.Reconciliation) > 4096 {
			return r, ErrRequest
		}
		index := -1
		for j := range m.Actions {
			if m.Actions[j].ID == c.ID {
				index = j
			}
		}
		if index < 0 {
			return r, ErrRequest
		}
		a := &m.Actions[index]
		if a.Version != c.Version || a.Status != "unknown" {
			return r, ErrConflict
		}
		// A later turn must query, not turn an old intent into a fresh tool call.
		if (a.TurnNumber != c.TurnNumber || a.Version > 1 || c.Status == "not_applied") && !handlingText(c.Reconciliation) {
			return r, ErrRequest
		}
		a.Version++
		a.Status, a.Evidence, a.Reconciliation, a.LastTurn, a.UpdatedAt = c.Status, c.Evidence, c.Reconciliation, c.TurnNumber, time.Now().UTC()
		if c.Status == "unknown" {
			m.Status, m.Next = "unknown", "查询远端核实动作 "+c.ID+"，不得自动重发"
		} else {
			m.Status, m.Next = "in_progress", "动作结果已记录；按业务完成条件决定办结或后续"
		}
	}
	touchMatter(m, c.TurnNumber, true)
	return r, s.save(s.matterPath(e), r, matterRecordBytes)
}

func authorityCoversTarget(source PolicyRevision, target ActionTarget) bool {
	if source.Kind == "instruction" {
		return true
	}
	scope := source.Scope
	if scope.Provider != "" && scope.Provider != target.Provider || scope.Account != "" && scope.Account != target.Account ||
		scope.Project != "" && scope.Project != target.ProjectID.Value && scope.Project != target.Project ||
		scope.ObjectType != "" && scope.ObjectType != target.Object.Type {
		return false
	}
	if scope.Instance != "" {
		_, origin, err := connection.Normalize(target.Provider, scope.Instance)
		_, targetOrigin, targetErr := connection.Normalize(target.Provider, target.Instance)
		if err != nil || targetErr != nil || origin != targetOrigin {
			return false
		}
	}
	return true
}

func validateHandling(m Matter) error {
	if len(m.Actions) > 32 || len(m.Notices) > 32 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	unknown := 0
	for _, a := range m.Actions {
		if !safeMembershipID(a.ID) || seen[a.ID] || a.Version == 0 || a.TurnNumber == 0 || a.LastTurn < a.TurnNumber ||
			!validActionTarget(a.Target) || !handlingText(a.Intent) || !handlingText(a.Verification) || !handlingText(a.Authority.Reason) ||
			!validText(a.Source.Source.User) || !validText(a.Source.Source.Text) || a.UpdatedAt.IsZero() ||
			(a.Status != "unknown" && a.Status != "succeeded" && a.Status != "not_applied") ||
			(a.Status != "unknown" && !handlingText(a.Evidence)) || len(a.Evidence) > 4096 || len(a.Reconciliation) > 4096 {
			return ErrInvalid
		}
		if len(a.Source.Source.Text) > maxTextBytes || !validRecordedAuthority(a) {
			return ErrInvalid
		}
		if a.Execution != nil {
			q := *a.Execution
			if !validActionRequest(q) || q.ID != a.ID || q.MatterID != m.ID || q.TurnNumber != a.TurnNumber ||
				q.Input.Body != a.Intent || q.Operation != a.Target.Operation || q.Target.ProjectID != a.Target.ProjectID.Value ||
				q.Target.MRIID != a.Target.Object.IID.Value || a.Attempts < 0 || a.Attempts > 2 ||
				(a.PossibleDuplicate && a.Attempts != 2) || len(a.RemoteID) > 128 || len(a.RemoteURL) > 4096 {
				return ErrInvalid
			}
		}
		seen[a.ID] = true
		if a.Status == "unknown" {
			unknown++
		}
	}
	if unknown != 0 && (m.Status == "completed" || m.Status == "archived") {
		return ErrInvalid
	}
	if err := validateNotices(m); err != nil {
		return fmt.Errorf("%w: notices", err)
	}
	return nil
}

func validRecordedAuthority(a MatterAction) bool {
	b := a.Authority
	if b.RoomSeq != 0 {
		return b.RuleID == "" && b.RuleVersion == 0 && b.DecisionID == "" && b.DecisionVersion == 0 &&
			a.Source.Kind == "instruction" && a.Source.Status == "active" && a.Source.Source.Seq == b.RoomSeq
	}
	if !validPolicyRevision(a.Source) || a.Source.Status != "active" || !authorityCoversTarget(a.Source, a.Target) {
		return false
	}
	return b.RuleID != "" && b.DecisionID == "" && b.DecisionVersion == 0 && a.Source.Kind == "rule" && a.Source.ID == b.RuleID && a.Source.Version == b.RuleVersion ||
		b.DecisionID != "" && b.RuleID == "" && b.RuleVersion == 0 && a.Source.Kind == "decision" && a.Source.ID == b.DecisionID && a.Source.Version == b.DecisionVersion
}
