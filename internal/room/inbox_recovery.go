package room

import (
	"fmt"
	"time"
)

// This is scheduling state, never a delivery receipt. LatestSeq is deliberately
// absent: arrivals and Session changes cannot renew the stalled range's budget.
type inboxRecovery struct {
	Start           uint64    `json:"start"`
	SnapshotSeq     uint64    `json:"snapshot_seq"`
	ConsumedThrough uint64    `json:"consumed_through"`
	Since           time.Time `json:"since"`
	Attempts        int       `json:"attempts"`
	NextAt          time.Time `json:"next_at"`
}

var inboxRecoveryBackoff = [...]time.Duration{15 * time.Second, time.Minute, 5 * time.Minute}

// A later Room-only turn does not replace the last offered Inbox range.
// LastOffered survives such turns; Turn's Inbox fields describe only that turn.
func stalledInboxRange(agent AgentSnapshot, status InboxStatus) bool {
	return agent.Turn != nil && agent.Turn.Status == "completed" &&
		agent.LastOfferedInboxSeq > status.inputThrough() && status.inputThrough() <= agent.LastOfferedInboxThrough
}

func (m *MemberAgent) reconcileInboxRecovery(status InboxStatus) {
	if m.active || m.fault {
		return
	}
	changed := false
	if r := m.inboxRecovery; r != nil && status.ConsumedThrough > r.ConsumedThrough {
		m.inboxRecovery = nil
		changed = true
	}
	m.inboxRecoveryBlock = status.RecoveryBlocked
	if stalledInboxRange(m.state, status) {
		if m.inboxRecovery == nil {
			m.inboxRecovery = &inboxRecovery{Start: m.state.LastOfferedInboxThrough, SnapshotSeq: m.state.LastOfferedInboxSeq,
				ConsumedThrough: status.ConsumedThrough, Since: m.now(), NextAt: m.now().Add(inboxRecoveryBackoff[0])}
			changed = true
		} else if r := m.inboxRecovery; r.Attempts < 3 && r.NextAt.IsZero() {
			r.NextAt = m.now().Add(inboxRecoveryBackoff[r.Attempts])
			changed = true
		}
	}
	if changed {
		_ = m.persistScheduling(m.enabled, m.turnsUsed)
	}
}

func (m *MemberAgent) continueInbox() error {
	if m.active || m.paused || m.fault || m.ModelGate != "" || m.BackendBlocked != "" ||
		m.rotationWork != nil || m.rotationRequest || (m.rotationRecord != nil && m.rotationRecord.Phase != "completed") ||
		(m.ExecutionFrozen != nil && m.ExecutionFrozen.Load()) {
		return ErrBusy
	}
	if !m.automaticRotation() && m.turnsUsed >= m.turnLimit {
		return ErrTurnBudget
	}
	if m.inboxRecovery == nil || m.state.Turn == nil || m.inbox == nil || m.inbox.WriteError != "" {
		return ErrConflict
	}
	if m.inbox.LatestSeq <= m.inbox.inputThrough() {
		return ErrConflict
	}
	if m.inboxRecoveryBlock != "" {
		return fmt.Errorf("%w: %s", ErrConflict, m.inboxRecoveryBlock)
	}
	if m.inboxContinueCount == ^uint64(0) {
		return ErrCapacity
	}
	// The last operator continuation and cumulative count form a durable audit
	// independent of the recovery record, which clears only on consumption.
	m.inboxContinuedAt, m.inboxContinueCount = m.now(), m.inboxContinueCount+1
	m.inboxRecovery.Attempts, m.inboxRecovery.NextAt = 0, m.now()
	if err := m.persistScheduling(m.enabled, m.turnsUsed); err != nil {
		return err
	}
	m.inboxContinuePending = true
	m.lastError = "" // Explicit continuation acknowledges the settled diagnostic.
	return nil
}

func inboxRecoveryNotice(s MemberStatus) *ExecutionNotice {
	r := s.InboxRecovery
	if r == nil || s.Active {
		return nil
	}
	backlog := uint64(0)
	if s.Inbox != nil {
		backlog = s.Inbox.LatestSeq - min(s.Inbox.LatestSeq, s.Inbox.inputThrough())
	}
	message := fmt.Sprintf("收件箱 #%d–#%d 尚未读取，积压 %d 条。", r.Start+1, r.SnapshotSeq, backlog)
	if s.Inbox != nil && s.Inbox.inputThrough() >= r.SnapshotSeq {
		message = fmt.Sprintf("历史停滞范围 #%d–#%d 已过期或跳过，当前有效积压 %d 条。", r.Start+1, r.SnapshotSeq, backlog)
	}
	code := "inbox_recovery_waiting"
	switch {
	case s.Error != "":
		return nil // Preserve model, checkpoint and confirmation diagnostics.
	case s.Paused:
		message += "协作已暂停，恢复读取不会越过暂停。"
	case s.Status == "budget_exhausted":
		code, message = "inbox_recovery_blocked", message+"回合预算已用尽，恢复不会追加预算。"
	case s.Inbox != nil && s.Inbox.WriteError != "":
		code, message = "inbox_recovery_blocked", message+"收件箱存储不可用，请先检查诊断。"
	case s.Inbox != nil && s.Inbox.RecoveryBlocked != "":
		code, message = "inbox_recovery_blocked", message+"有操作结果尚未确认或恢复检查失败，请先核实。"
	case s.Inbox != nil && s.Inbox.AutomaticBlocked:
		code, message = "inbox_recovery_blocked", message+"来源证据待核验，自动恢复暂不可用。"
	case backlog == 0:
		code, message = "inbox_recovery_retained", message+"恢复额度保持，等待有效输入。"
	case s.Agent != nil && s.Agent.Turn != nil && s.Agent.Turn.Status != "completed":
		code, message = "inbox_recovery_stopped", message+"上次回合失败或中断，请先处理运行状态。"
	case r.Attempts >= 3:
		code, message = "inbox_recovery_exhausted", message+"已停止自动恢复（3/3）；可继续读取收件箱。"
	default:
		message += fmt.Sprintf("等待恢复 %d/3，下次 %s。", min(r.Attempts+1, 3), r.NextAt.Format(time.RFC3339))
	}
	return &ExecutionNotice{Code: code, Message: message}
}
