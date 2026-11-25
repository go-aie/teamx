// Current execution presentation is separate from retained turn diagnostics.
export const isSessionRecovering = member => member?.status === "recovering" || member?.execution?.notice?.code === "session_recovering";
export const memberExecutionFailed = member => !isSessionRecovering(member) && !!member?.execution?.error;
export function memberExecutionMessage(member, formatDate) {
  if (isSessionRecovering(member)) {
    let message = member?.execution?.notice?.message || "正在恢复助理，恢复后将自动继续。";
    const next = member?.execution?.session_recovery?.next_at;
    if (formatDate && next && Date.parse(next) > 0) message = message.replace(next.replace(/\.\d+/, ""), formatDate(next));
    return message;
  }
  let message = member?.execution?.notice?.message || (member?.execution?.error ? "助理本次未能完成工作。对话和任务记录已保留，可查看诊断详情。" : "");
  const next = member?.execution?.inbox_recovery?.next_at;
  if (formatDate && next && Date.parse(next) > 0) message = message.replace(next.replace(/\.\d+/, ""), formatDate(next));
  return message;
}
