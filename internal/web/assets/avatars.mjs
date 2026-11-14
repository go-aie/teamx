import { createAssistantIcon, createUserIcon } from "./icons.mjs";

const tones = ["sage", "blue", "violet", "amber", "cyan"];
const platformLabels = { gitlab:"GL", lark:"飞", scion:"SC" };

// Identity colors are stable across Rooms, lists and message history.
export function renderAvatar(document, avatar, { kind, id = "", provider = "", direct = false, defaultAssistant = false, name = id }) {
  avatar.classList.add("avatar");
  for (const [name, match] of [["user-avatar", "human"], ["agent-avatar", "agent"], ["integration-avatar", "integration"]]) {
    avatar.classList.toggle(name, kind === match);
  }
  let hash = 0;
  for (const char of id) hash = (Math.imul(hash, 31) + char.codePointAt(0)) >>> 0;
  avatar.dataset.avatarTone = kind === "agent" ? defaultAssistant ? "assistant" : tones[hash % tones.length] : "";
  avatar.dataset.avatarProvider = kind === "integration" ? provider : "";
  avatar.replaceChildren();
  if (kind === "human") avatar.append(createUserIcon(document));
  else if (kind === "agent" && direct) avatar.append(createAssistantIcon(document));
  else avatar.textContent = kind === "integration" ? platformLabels[provider] || Array.from(provider || id).slice(0, 2).join("").toUpperCase()
    : Array.from(name).slice(0, 2).join("").toUpperCase();
}
