// Display labels never become routing keys.
export const agentName = member => member?.agent_name || member?.agent_id || member?.id || "";
export function resolveAgentName(members, id, membership, fallback = id) {
  const member = members.find(m => m.agent_id === id && (!membership || m.membership_id === membership));
  return member ? agentName(member) : fallback;
}

// Plain-text previews use the same bound identities as the message body.
export function messageDisplayText(message, label) {
  const text = message.text;
  let end = 0, result = "";
  for (const at of message.mentions || []) {
    if (!Number.isSafeInteger(at.start) || !Number.isSafeInteger(at.end) || at.start < end || at.end <= at.start + 1 || at.end > text.length || text[at.start] !== "@") continue;
    result += text.slice(end, at.start) + "@" + label(at);
    end = at.end;
  }
  return result + text.slice(end);
}
