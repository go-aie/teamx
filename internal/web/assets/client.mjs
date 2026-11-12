export async function request(path, payload, fetcher = globalThis.fetch, timeout = 10000, signal) {
  const controller = new AbortController();
  const abort = () => controller.abort();
  if (signal?.aborted) abort();
  else signal?.addEventListener("abort", abort, { once: true });
  const timer = setTimeout(() => controller.abort(), timeout);
  try {
    const response = await fetcher(path, { method: payload ? "POST" : "GET", cache: "no-store",
      headers: payload ? { "Content-Type": "application/json" } : {},
      body: payload ? JSON.stringify(payload) : undefined, signal: controller.signal });
    const result = await response.json();
    if (!response.ok) {
      if (response.status === 401 && globalThis.location?.pathname !== "/access") globalThis.location?.assign("/access");
      const error = new Error(result.message || result.error || `请求失败 (${response.status})`);
      error.code = result.code;
      error.field = result.field;
      throw error;
    }
    return result;
  } finally { clearTimeout(timer); signal?.removeEventListener("abort", abort); }
}

// Never overwrite an existing draft or an uncertain send when loading a team.
export function teamOpeningDraft(current, pending, opening) {
  return !pending && !current.trim() ? opening : current;
}

export function sameService(a, b) {
  return !!a && !!b && a.room_id === b.room_id && a.room_instance === b.room_instance && a.service_epoch === b.service_epoch;
}

// A draft belongs to a Room instance and operator, across activation epochs.
// Pending sends retain the exact identity that first submitted the message.
export function createRoomDrafts(storage, installation = () => "") {
  const drafts = new Map();
  const owners = new WeakMap();
  const removed = new Set();
  const key = (info, operator) => JSON.stringify(["teamx.draft", ...(installation() ? [installation()] : []), info.room_instance, operator]);
  function get(info, operator) {
    const id = key(info, operator);
    if (!drafts.has(id)) {
      let saved;
      try { saved = JSON.parse(storage?.getItem(id)); } catch { /* Keep working in memory. */ }
      const valid = saved && typeof saved.text === "string" && Number.isSafeInteger(saved.replyTo) && saved.replyTo >= 0;
      const draft = valid ? saved : { text: "", replyTo: 0, replyLabel: "", pending: null, restartRequest: null };
      draft.sending = false; draft.restartStarting = false;
      // Storage is optional, but malformed or foreign requests cannot gain a route.
      if (draft.pending && (draft.pending.info?.room_instance !== info.room_instance || draft.pending.operator !== operator ||
        typeof draft.pending.text !== "string" || !draft.pending.client_message_id || !draft.pending.info?.service_epoch)) draft.pending = null;
      drafts.set(id, draft); owners.set(draft, id);
    }
    return drafts.get(id);
  }
  function save(info, operator, draft) {
    const id = owners.get(draft) || key(info, operator);
    if (removed.has(id)) return;
    const { text, replyTo, replyLabel, replyAuthor, pending, restartRequest, openingSeeded } = draft;
    try { storage?.setItem(id, JSON.stringify({ text, replyTo, replyLabel, replyAuthor, pending, restartRequest, openingSeeded })); }
    catch { /* Retain the draft in memory if storage is unavailable or full. */ }
  }
  function seed(info, operator, opening) {
    const draft = get(info, operator);
    if (draft.openingSeeded) return;
    draft.text = teamOpeningDraft(draft.text, draft.pending, opening);
    draft.openingSeeded = true;
    save(info, operator, draft);
  }
  function remove(info, operator) {
    removed.add(key(info, operator));
    drafts.delete(key(info, operator));
    try { storage?.removeItem(key(info, operator)); } catch { /* Storage is optional. */ }
  }
  return { get, save, seed, remove };
}

// Match the exact display identity, with common Chinese/English punctuation.
// Avoid email addresses and longer names such as @ann2 or @ann.smith.
export function mentionRanges(text, operator) {
  if (!operator) return [];
  const name = operator.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const pattern = new RegExp(`(?<![A-Za-z0-9_@.+-])@${name}(?![A-Za-z0-9_@+-]|\\.[A-Za-z0-9_])`, "gu");
  return [...text.matchAll(pattern)].map(match => ({ start: match.index, end: match.index + match[0].length }));
}

export function unseenMentions(messages, operator, seen, seenThrough = 0) {
  return [...messages].filter(message => message.seq > seenThrough && !seen.has(message.seq) &&
    !(message.author.kind === "human" && message.author.id === operator) &&
    mentionRanges(message.text, operator).length > 0).sort((a, b) => a.seq - b.seq);
}

// Complete known names only; prefer the longest name where identities overlap.
export function memberMentionRanges(text, names) {
  const ranges = [...new Set(names.filter(Boolean))].flatMap(name => mentionRanges(text, name)
    .filter(at => !/[\p{L}\p{N}_@.+-]/u.test(text.charAt(at.end)))
    .map(at => ({ ...at, name }))).sort((a, b) => a.start - b.start || b.end - a.end);
  let end = 0;
  return ranges.filter(at => { if (at.start < end) return false; end = at.end; return true; });
}

// All continuation pages belong to the first page's fixed snapshot. Neither
// history previews nor live refreshes ever confirm a member's unread receipt.
export async function readPages(read, params) {
  const messages = [];
  let page;
  let snapshot = params.snapshot_seq;
  let clearedThrough;
  do {
    page = await read(params);
    snapshot ??= page.snapshot_seq;
    if (page.snapshot_seq !== snapshot) throw new Error("消息快照已变化，请刷新重试。");
    clearedThrough ??= page.cleared_through || 0;
    if ((page.cleared_through || 0) !== clearedThrough) throw new Error("消息已被清空，请刷新重试。");
    messages.push(...page.messages);
    if (page.has_more && page.next_after <= (params.after || 0)) throw new Error("消息分页没有前进，请刷新重试。");
    params = { mode: "range", after: page.next_after, snapshot_seq: snapshot };
  } while (page.has_more);
  return { messages, snapshot, clearedThrough };
}

// Late responses for a closed drawer or a different membership must never
// replace the selected member's private output. Polls share one in-flight read.
export function createActivityReader(read, render) {
  let selected = null;
  let generation = 0;
  let pending = null;
  function refresh() {
    if (!selected) return Promise.resolve();
    if (pending) return pending;
    const current = generation, member = selected;
    const task = Promise.resolve().then(() => read(member)).then(
      (data) => { if (current === generation) render({ data }, member); },
      (error) => { if (current === generation) render({ error }, member); },
    ).finally(() => { if (current === generation) pending = null; });
    pending = task;
    return task;
  }
  return {
    select(member) {
      generation++; selected = member; pending = null;
      render({ loading: true }, member);
      return refresh();
    },
    close() { generation++; selected = null; pending = null; },
    refresh,
  };
}

// A rotated touch phone still needs the single-conversation layout.
export function compactViewport(window) {
  return window.innerWidth <= 640 || !!window.matchMedia?.("(max-height:500px) and (pointer:coarse)").matches;
}
