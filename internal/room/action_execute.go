package room

import (
	"strconv"
	"time"
)

type ActionRequest struct {
	MatterCommand
	ID           string          `json:"id"`
	PolicyToken  string          `json:"policy_token"`
	Authority    ActionAuthority `json:"authority"`
	Verification string          `json:"verification"`
	Operation    string          `json:"operation"`
	Target       CommentTarget   `json:"target"`
	Input        CommentInput    `json:"input"`
}
type CommentTarget struct {
	ProjectID string `json:"project_id"`
	MRIID     string `json:"mr_iid"`
}
type CommentInput struct {
	Body string `json:"body"`
}
type ActionReceipt struct {
	ID                string           `json:"id"`
	Status            string           `json:"status"`
	Attempts          int              `json:"attempts"`
	MatterVersion     uint64           `json:"matter_version"`
	RemoteID          string           `json:"remote_id,omitempty"`
	URL               string           `json:"url,omitempty"`
	PossibleDuplicate bool             `json:"possible_duplicate,omitempty"`
	Message           string           `json:"message,omitempty"`
	Policy            *agentPolicyView `json:"policy,omitempty"`
}
type actionKey struct {
	MemberIdentity
	Seq          uint64
	MatterID, ID string
}

func (q ActionRequest) key(id MemberIdentity) actionKey {
	return actionKey{id, q.Seq, q.MatterID, q.ID}
}
func (q ActionRequest) sameBusiness(other ActionRequest) bool {
	return q.Operation == other.Operation && q.Target == other.Target && q.Input == other.Input
}
func actionDecimal(s string) bool {
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == s
}
func validActionRequest(q ActionRequest) bool {
	return q.Seq > 0 && safeMembershipID(q.MatterID) && safeMembershipID(q.ID) && q.TurnNumber > 0 && q.MatterVersion > 0 &&
		q.Operation == "gitlab.mr.comment" && actionDecimal(q.Target.ProjectID) && actionDecimal(q.Target.MRIID) &&
		handlingText(q.Input.Body) && handlingText(q.Verification) && len(q.PolicyToken) == 64
}
func (s *Store) actionRecordLocked(id MemberIdentity, q ActionRequest) (InboxEntry, MatterRecord, int, error) {
	e, err := s.inboxEntryLocked(id, q.Seq)
	if err != nil {
		return e, MatterRecord{}, 0, err
	}
	r, err := s.loadMatterLocked(e)
	if err != nil {
		return e, r, 0, err
	}
	for i, m := range r.Matters {
		if m.ID == q.MatterID {
			return e, r, i, nil
		}
	}
	return e, r, 0, ErrRequest
}
func (s *Store) actionReceiptLocked(id MemberIdentity, q ActionRequest, m Matter, a MatterAction) ActionReceipt {
	status := a.Status
	if s.executingActions[q.key(id)] {
		status = "processing"
	}
	message := a.Evidence
	if status == "unknown" {
		message = "结果不确定，可能已经发送；按需查询远端并报告，不得自动重发。"
	}
	if status == "processing" {
		message = "动作处理中；重复请求不会再次启动执行。"
	}
	return ActionReceipt{ID: a.ID, Status: status, Attempts: a.Attempts, MatterVersion: m.Version, RemoteID: a.RemoteID, URL: a.RemoteURL, PossibleDuplicate: a.PossibleDuplicate, Message: message}
}
func (s *Store) existingActionLocked(id MemberIdentity, q ActionRequest, m Matter) (*ActionReceipt, error) {
	for _, a := range m.Actions {
		if a.ID != q.ID {
			continue
		}
		if a.Execution == nil || !q.sameBusiness(*a.Execution) {
			return nil, ErrConflict
		}
		receipt := s.actionReceiptLocked(id, q, m, a)
		return &receipt, nil
	}
	return nil, nil
}

// Identity precedes deduplication; current turn and versions apply only to NEW actions.
func (s *Store) existingAction(id MemberIdentity, q ActionRequest) (*ActionReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, r, i, err := s.actionRecordLocked(id, q)
	if err != nil {
		return nil, err
	}
	return s.existingActionLocked(id, q, r.Matters[i])
}
func (s *Store) executeAuthorityLocked(id MemberIdentity, q ActionRequest, m Matter, target ActionTarget) (PolicyRevision, *agentPolicyView, error) {
	p, _, err := s.loadPolicyLocked(id, false)
	if err != nil {
		return PolicyRevision{}, nil, err
	}
	view := agentPolicy(p, q.TurnNumber, time.Now())
	if view.PolicyToken != q.PolicyToken {
		return PolicyRevision{}, &view, nil
	}
	authority := q.Authority
	authority.PolicyRevision = p.Revision // suggestions may have changed storage revision only
	source, err := s.actionAuthorityLocked(id, m, authority)
	if err == nil && !authorityCoversTarget(source, target) {
		err = ErrConflict
	}
	return source, nil, err
}
func (s *Store) beginAction(id MemberIdentity, q ActionRequest, target ActionTarget) (ActionReceipt, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, r, i, err := s.actionRecordLocked(id, q)
	if err != nil {
		return ActionReceipt{}, false, err
	}
	m := &r.Matters[i]
	if prior, err := s.existingActionLocked(id, q, *m); prior != nil || err != nil {
		if prior != nil {
			return *prior, false, nil
		}
		return ActionReceipt{}, false, err
	}
	if !validActionRequest(q) || !validActionTarget(target) || !targetMatchesSource(target, r.Source) {
		return ActionReceipt{}, false, ErrRequest
	}
	if _, err := s.runningTurnLocked(id, q.TurnNumber); err != nil {
		return ActionReceipt{}, false, err
	}
	if m.Version != q.MatterVersion || m.Status == "completed" || m.Status == "archived" {
		return ActionReceipt{}, false, ErrConflict
	}
	if err := s.checkMatterPrimaryLocked(id, MatterRef{Seq: q.Seq, ID: q.MatterID}); err != nil {
		return ActionReceipt{}, false, err
	}
	for _, a := range m.Actions {
		// Changing the ID cannot resend a previously uncertain identical comment.
		if a.Status == "unknown" && a.Execution != nil && q.sameBusiness(*a.Execution) {
			return ActionReceipt{}, false, ErrConflict
		}
	}
	if len(m.Actions) >= 32 {
		return ActionReceipt{}, false, ErrCapacity
	}
	source, view, err := s.executeAuthorityLocked(id, q, *m, target)
	if err != nil {
		return ActionReceipt{}, false, err
	}
	if view != nil {
		return ActionReceipt{ID: q.ID, Status: "policy_changed", MatterVersion: m.Version, Policy: view, Message: "规则已变化；请根据完整有效视图重新判断，不得只替换 token 后自动执行。"}, false, nil
	}
	q.Authority.PolicyRevision = 0 // authority revision is resolved server-side, never supplied by Agent
	a := MatterAction{ID: q.ID, Version: 1, TurnNumber: q.TurnNumber, LastTurn: q.TurnNumber, Target: target, Intent: q.Input.Body, Verification: q.Verification, Authority: q.Authority, Source: source, Status: "unknown", UpdatedAt: time.Now().UTC(), Execution: &q}
	p, _, err := s.loadPolicyLocked(id, false)
	if err != nil {
		return ActionReceipt{}, false, err
	}
	a.Authority.PolicyRevision = p.Revision
	m.Actions = append(m.Actions, a)
	m.Status, m.Next = "in_progress", "动作执行中；执行回执与事项办结分别记录"
	touchMatter(m, q.TurnNumber, true)
	if err := s.save(s.matterPath(e), r, matterRecordBytes); err != nil {
		return ActionReceipt{}, false, err
	}
	if s.executingActions == nil {
		s.executingActions = map[actionKey]bool{}
	}
	s.executingActions[q.key(id)] = true
	return s.actionReceiptLocked(id, q, *m, a), true, nil
}

// Called before EACH dispatch. Version changes from unrelated work are allowed;
// authority, source linkage and current turn must still hold.
func (s *Store) actionAttempt(id MemberIdentity, q ActionRequest) (*agentPolicyView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, r, i, err := s.actionRecordLocked(id, q)
	if err != nil {
		return nil, err
	}
	m := &r.Matters[i]
	if !s.executingActions[q.key(id)] {
		return nil, ErrConflict
	}
	if _, err := s.runningTurnLocked(id, q.TurnNumber); err != nil {
		return nil, err
	}
	if err := s.checkMatterPrimaryLocked(id, MatterRef{Seq: q.Seq, ID: q.MatterID}); err != nil {
		return nil, err
	}
	for j := range m.Actions {
		a := &m.Actions[j]
		if a.ID != q.ID {
			continue
		}
		_, view, err := s.executeAuthorityLocked(id, q, *m, a.Target)
		if err != nil || view != nil {
			return view, err
		}
		if a.Attempts >= 2 || a.Status != "unknown" {
			return nil, ErrConflict
		}
		a.Attempts++
		a.Version++
		a.UpdatedAt = time.Now().UTC()
		touchMatter(m, q.TurnNumber, false)
		return nil, s.save(s.matterPath(e), r, matterRecordBytes)
	}
	return nil, ErrRequest
}
func (s *Store) finishAction(id MemberIdentity, q ActionRequest, result ActionReceipt) (ActionReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer delete(s.executingActions, q.key(id))
	e, r, i, err := s.actionRecordLocked(id, q)
	if err != nil {
		return ActionReceipt{}, err
	}
	m := &r.Matters[i]
	for j := range m.Actions {
		a := &m.Actions[j]
		if a.ID != q.ID {
			continue
		}
		a.Status, a.Evidence, a.RemoteID, a.RemoteURL, a.PossibleDuplicate = result.Status, result.Message, result.RemoteID, result.URL, result.PossibleDuplicate
		a.Version++
		a.UpdatedAt = time.Now().UTC()
		if a.Status == "unknown" {
			m.Status, m.Next = "unknown", "结果不确定，可能已经发送；按需查询远端并报告，不得自动重发"
		} else {
			m.Status, m.Next = "in_progress", "动作结果已保存；按业务完成条件决定后续"
		}
		touchMatter(m, q.TurnNumber, true)
		if err := s.save(s.matterPath(e), r, matterRecordBytes); err != nil {
			return ActionReceipt{}, err
		}
		receipt := s.actionReceiptLocked(id, q, *m, *a)
		// The deferred deletion has not run yet.
		receipt.Status, receipt.Message, receipt.Policy = a.Status, a.Evidence, result.Policy
		return receipt, nil
	}
	return ActionReceipt{}, ErrRequest
}
