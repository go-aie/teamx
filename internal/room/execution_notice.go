package room

import (
	"fmt"
	"strings"
	"time"
)

func (m *MemberAgent) recoveringSession() bool {
	if !m.enabled || m.fault || m.ModelGate != "" {
		return false
	}
	if r := m.rotation; r != nil && (r.Status == "waiting" || r.Status == "closing") {
		return r.Reason == "backend_recovery"
	}
	return m.RecoveryPending && !m.recoveryAttempted
}

// Safe authored messages are separate from the full error chain retained for
// diagnostics. Saved snapshots contain strings, so only known diagnostics are
// recognized; unrecognized text is never copied into the user-facing message.
func executionNotice(s MemberStatus) *ExecutionNotice {
	if s.Status == "recovering" {
		return &ExecutionNotice{Code: "session_recovering", Message: "正在恢复助理，恢复后将自动继续。"}
	}
	if r := s.InputRecovery; r != nil && !s.Active && s.Error == "" {
		code, message := "inbox_recovery_waiting", fmt.Sprintf("收件箱还有 %d 条待交付输入。", r.Pending)
		switch {
		case s.Paused:
			message += "协作已暂停。"
		case s.Status == "budget_exhausted":
			code, message = "inbox_recovery_blocked", message+"回合预算已用尽。"
		case r.Blocked != "":
			code, message = "inbox_recovery_blocked", message+"恢复检查尚未通过，请先查看诊断。"
		case r.Attempts >= 3:
			code, message = "inbox_recovery_exhausted", message+"已停止自动恢复（3/3）；可继续读取收件箱。"
		default:
			message += fmt.Sprintf("等待恢复 %d/3，下次 %s。", min(r.Attempts+1, 3), r.NextAt.Format(time.RFC3339))
		}
		return &ExecutionNotice{Code: code, Message: message}
	}
	if notice := inboxRecoveryNotice(s); notice != nil {
		return notice
	}
	if s.Error == "" {
		return nil
	}
	modelNotice := backendModelNotice(s.Error)
	code, message := "execution_failed", "助理本次未能完成工作，可重试。对话和任务记录已保留。"
	switch {
	case strings.Contains(s.Error, "Inbox output confirmation failed"):
		code, message = "inbox_confirmation_failed", "收件箱输出确认失败，未确认的输入仍保留。请查看诊断后继续。"
	case strings.Contains(s.Error, "Inbox read failed"):
		code, message = "inbox_read_failed", "收件箱正式读取失败，输入仍保留。请查看诊断后继续。"
	case strings.Contains(s.Error, "action outcomes require inspection"):
		code, message = "action_outcome_unknown", "有操作的执行结果尚未确认，助理已暂停以避免重复执行。对话和任务记录已保留。"
	case modelNotice != nil:
		return modelNotice
	case s.Status == "checkpoint_failed":
		code, message = "state_unavailable", "助理暂时无法保存运行状态，已停止执行。可查看诊断详情。"
	case s.SessionResetRequired:
		code, message = "session_unavailable", "助理暂时无法恢复。对话和任务记录已保留，可查看诊断详情。"
	}
	return &ExecutionNotice{Code: code, Message: message}
}
