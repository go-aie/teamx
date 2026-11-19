// Keep execution instructions and identities in the saved event for Agents and
// delivery receipts; the conversation shows only its human-readable summary.
export function roomEventDisplayText(message) {
  const text = message.text;
  const event = message.event;
  if (message.type !== "event" || event?.event_type !== "calendar_due" || !event.operation_id) return text;

  // Match the complete producer-owned suffix, including the bound run identity.
  // This also handles existing events without rewriting their immutable bodies.
  const suffix = `。run_id=${event.operation_id}；请先阅读 teamx calendar --help，核对最新人类意图后用 calendar claim 领取，再办理并以 calendar finish 发布报告。`;
  return text.startsWith("定时任务到期：") && text.endsWith(suffix) ? text.slice(0, -suffix.length) : text;
}
