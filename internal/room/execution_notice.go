package room

import (
	"strings"

	"github.com/go-aie/teamx/internal/agent/dsh"
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
	if notice := inboxRecoveryNotice(s); notice != nil {
		return notice
	}
	if s.Error == "" {
		return nil
	}
	code, message := "execution_failed", "助理本次未能完成工作，可重试。对话和任务记录已保留。"
	switch {
	case strings.Contains(s.Error, "Inbox output confirmation failed"):
		code, message = "inbox_confirmation_failed", "收件箱输出确认失败，未确认的输入仍保留。请查看诊断后继续。"
	case strings.Contains(s.Error, "Inbox read failed"):
		code, message = "inbox_read_failed", "收件箱正式读取失败，输入仍保留。请查看诊断后继续。"
	case strings.Contains(s.Error, "action outcomes require inspection"):
		code, message = "action_outcome_unknown", "有操作的执行结果尚未确认，助理已暂停以避免重复执行。对话和任务记录已保留。"
	case strings.Contains(s.Error, dsh.ErrAuthentication.Error()):
		code, message = "model_authentication", "模型连接验证失败，请在设置中检查模型凭据。"
	case strings.Contains(s.Error, dsh.ErrRateLimited.Error()):
		code, message = "model_rate_limited", "模型服务请求受限，请稍后重试。"
	case strings.Contains(s.Error, dsh.ErrModelNotFound.Error()):
		code, message = "model_not_found", "当前模型不可用，请在设置中检查或更换模型。"
	case s.Status == "checkpoint_failed":
		code, message = "state_unavailable", "助理暂时无法保存运行状态，已停止执行。可查看诊断详情。"
	case s.SessionResetRequired:
		code, message = "session_unavailable", "助理暂时无法恢复。对话和任务记录已保留，可查看诊断详情。"
	}
	return &ExecutionNotice{Code: code, Message: message}
}
