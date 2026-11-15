// Current execution presentation is separate from retained turn diagnostics.
export const isSessionRecovering = member => member?.status === "recovering" || member?.execution?.notice?.code === "session_recovering";
export const memberExecutionFailed = member => !isSessionRecovering(member) && !!member?.execution?.error;
export function memberExecutionMessage(member) {
  if (isSessionRecovering(member)) return "正在恢复助理，恢复后将自动继续。";
  return member?.execution?.notice?.message || (member?.execution?.error ? "助理本次未能完成工作。对话和任务记录已保留，可查看诊断详情。" : "");
}
