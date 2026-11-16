import { agentName, resolveAgentName, messageDisplayText } from "../agent-names.mjs";
import { randomUUID } from "../random-id.mjs";
import { readPages as collectPages, createRoomDrafts, mentionRanges, compactViewport } from "../client.mjs";
import { createHumanRead } from "./human-read.mjs";
import { notificationOptions } from "../markdown.mjs";
import { createAssistantIcon } from "../icons.mjs";
import { renderAvatar } from "../avatars.mjs";
import { createMentionPicker } from "../mention-picker.mjs";
import { captureReturnView } from "../view-return.mjs";

// Messages, drafts, mentions and the send/restart lifecycle.
export function createConversation({ env, runtime, roomsUI, notificationsUI, membersUI, settingsUI, interactionsUI, workspaceUI, layoutUI, userMenuUI }) {
  const { document, window, requestAnimationFrame, performance, setTimeout, ResizeObserver, renderMessageBody } = env;
  const { state, $, notice, rpc, currentView, refresh, discussionBusy, reconcileChildren, storage, confirmAction } = runtime;

  let historyTask = null, composing = false, compositionEnded = -Infinity;
  let notificationsReturn = null;

  function returnConversationNotifications() {
    const back = notificationsReturn; notificationsReturn = null;
    layoutUI.hideConversationNotifications();
    if (back?.view.current()) back.panel();
    else { layoutUI.collapseMembers(); layoutUI.renderPanels(); }
    $("activity-dialog").hidden = false;
    back?.view.restore($("room-menu-label"));
  }

  const drafts = createRoomDrafts(storage, () => state.installationID);
  const sendIcon = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  sendIcon.setAttribute("viewBox", "0 0 24 24");
  sendIcon.setAttribute("aria-hidden", "true");
  sendIcon.setAttribute("focusable", "false");
  const sendIconPath = document.createElementNS("http://www.w3.org/2000/svg", "path");
  sendIcon.append(sendIconPath);
  const mentionPicker = createMentionPicker({ document, input: $("message-input"), list: $("mention-options"), mirror: $("message-mirror"),
    members: () => state.members, operator: () => state.operator, enabled: () => !!state.info && !state.draft.pending && !discussionBusy(),
    draft: () => state.draft, changed: () => { saveDraft(); composerState(); } });

  let restartNoticeID = "", restartNoticeExpires = 0;

  let mentionMessages = [];
  const unreadCandidates = new Map();
  const humanRead = createHumanRead({ env, runtime, changed: renderReading, canConfirm: () => state.connected && !discussionBusy() });

  // Only this component owns the current visit. A new visit never restores its bookmark.
  let visit = null, viewingFramePending = false, viewingPaused = false, positioning = false, positioningID = 0;
  const newDivider = document.createElement("div");
  newDivider.className = "new-message-divider";
  newDivider.textContent = "以下为新消息";
  const ownMessage = message => message.author.kind === "human" && message.author.id === state.operator;
  const readingKey = () => state.info ? JSON.stringify([state.installationID, state.info, state.operator]) : "";

  function leaveConversation() {
    cancelHistory();
    visit = null;
    viewingPaused = false;
    positioning = false;
    positioningID++;
    humanRead.reset({ force: true });
    newDivider.remove();
    notice("review-notice", "");
    unreadCandidates.clear();
  }

  function conversationVisibilityChanged() {
    const outside = !state.info || compactViewport(window) && layoutUI.roomsPanelOpen();
    if (outside) { if (visit) leaveConversation(); return; }
    if (!visit || visit.key !== readingKey()) {
      leaveConversation();
      visit = { key: readingKey(), entrySnapshot: null, dividerSeq: null, remainingSeqs: new Set(),
        observed: new Set(), loaded: false, opened: false, dividerEstablished: false, task: null, metadata: null };
    }
    if (visit.entrySnapshot === null) void initializeVisit(visit);
    // Details and dialogs pause observation but retain the same visit.
    scheduleMessageViewing();
  }

  function pollHumanRead() {
    conversationVisibilityChanged();
    if (!visit) return;
    if (visit.entrySnapshot === null) void initializeVisit(visit);
    else humanRead.poll();
  }

  async function initializeVisit(current) {
    if (current.task) return;
    current.task = humanRead.entryState();
    const result = await current.task;
    current.task = null;
    if (visit !== current || !result || result.cleared_through < state.clearedThrough) return;
    const { new_message_seqs, ...metadata } = result;
    current.metadata = metadata;
    current.entrySnapshot = result.snapshot_seq;
    current.dividerSeq = result.new_message_seqs[0] || null;
    current.dividerEstablished = current.dividerSeq !== null;
    current.remainingSeqs = new Set(new_message_seqs);
    // Messages can arrive while the independent entry request is in flight.
    for (const message of state.messages.values()) if (message.seq > result.snapshot_seq && !ownMessage(message)) {
      current.remainingSeqs.add(message.seq);
      if (!current.dividerEstablished) { current.dividerSeq = message.seq; current.dividerEstablished = true; }
    }
    for (const seq of current.observed) current.remainingSeqs.delete(seq);
    current.observed = null;
    renderReading();
    renderDivider();
    scheduleMessageViewing();
  }

  function readingActive() {
    if (!visit || state.suspended || document.hidden || !document.hasFocus() ||
      !layoutUI.conversationVisible() || document.querySelector("dialog:modal")) return false;
    const root = $("timeline").getBoundingClientRect(), viewport = window.visualViewport;
    const top = viewport?.offsetTop || 0, left = viewport?.offsetLeft || 0;
    return Math.min(root.bottom, top + (viewport?.height ?? window.innerHeight)) > Math.max(root.top, top) &&
      Math.min(root.right, left + (viewport?.width ?? window.innerWidth)) > Math.max(root.left, left);
  }

  function confirmOpened() {
    if (!readingActive() || !state.connected || discussionBusy() || !visit.loaded || visit.entrySnapshot === null ||
      state.lastSeq < visit.entrySnapshot) return;
    if (!visit.opened) {
      humanRead.openThrough(visit.entrySnapshot);
      visit.opened = true;
    }
  }

  // Called only for a successful main message read, never for history or reading polls.
  function messagesRead(read, { after, stick }) {
    if (!visit) return;
    visit.loaded = true;
    if (visit.entrySnapshot !== null) for (const message of read.messages) {
      if (message.seq <= after || message.seq <= visit.entrySnapshot || ownMessage(message)) continue;
      visit.remainingSeqs.add(message.seq);
      if (!visit.dividerEstablished) { visit.dividerSeq = message.seq; visit.dividerEstablished = true; }
    }
    const alreadyOpened = visit.opened;
    confirmOpened();
    if (alreadyOpened && stick && readingActive() && state.connected && !discussionBusy()) humanRead.openThrough(read.snapshot);
    renderReading();
    renderDivider();
    scheduleMessageViewing();
  }

  function renderDivider() {
    const target = visit?.dividerSeq && messageNodes.get(visit.dividerSeq)?.element;
    if (target?.parentElement) target.parentElement.insertBefore(newDivider, target);
    else newDivider.remove();
  }
  const operatorNames = () => [state.operator, state.operatorDisplayName].filter(Boolean);
  const authorName = (author, message) => author.kind === "human" && author.id === state.operator ? userMenuUI.displayName()
    : author.kind === "agent" ? resolveAgentName(state.members,author.id,message?.author_membership_id,message?.agent_name || author.id) : author.id;
  const mentionLabel = at=>at.kind==="agent" ? resolveAgentName(state.members,at.id,at.membership_id,at.agent_name || at.id) : at.id===state.operator ? userMenuUI.displayName() : at.id;
  const displayText = message => messageDisplayText(message, mentionLabel);
  const messageOptions = message => ({ mentions:message.mentions, mentionLabel, ...(message.author.kind === "integration"
    ? notificationOptions(state.info, state.operator, message.source, { seq: message.seq })
    : { memberNames: state.members.map(m => m.agent_id) }), operatorNames: operatorNames() });

  function renderRestart() {
    const restart = state.restart;
    const stages = { pausing: "正在暂停成员并取消旧回合…", rotating: "正在结清旧执行并更换会话…",
      clearing: "正在清空公共消息…", resuming: "正在恢复协作…" };
    let text = state.draft.restartError;
    if (restart?.status === "running") text = stages[restart.phase] || "正在重新开始讨论…";
    else if (state.draft.restartRequest) text = "操作结果尚未确认。正在同步状态，可用原请求重试。";
    else if (state.draft.restartError) text = state.draft.restartError;
    else if (restart?.status === "failed") text = `重新开始未完成，成员保持暂停。部分会话可能已更换；处理失败原因后可重新执行。${restart.error}`;
    else if (restart?.status === "completed" && restart.id === restartNoticeID) {
      // Show success only for this tab's action, without renewing it on polls.
      if (!restartNoticeExpires) {
        restartNoticeExpires = Date.now() + 5000;
        setTimeout(renderRestart, 5000);
      }
      if (Date.now() < restartNoticeExpires) text = "已重新开始讨论：公共消息已清空，所有成员的私有会话已重置。" +
        (restart.paused_members?.length ? ` ${restart.paused_members.join("、")} 的回合预算已用尽，仍保持暂停。` : "可以发送新的开场消息。");
    }
    notice("restart-notice", text);
  }

  function nearBottom() {
    const el = $("timeline");
    return el.scrollHeight - el.scrollTop - el.clientHeight < 90;
  }

  function bottom() {
    const id = ++positioningID;
    positioning = true;
    $("timeline").scrollTop = $("timeline").scrollHeight;
    requestAnimationFrame(() => requestAnimationFrame(() => {
      if (id === positioningID) { positioning = false; scheduleMessageViewing(); }
    }));
    $("jump-latest").hidden = true;
  }

  function unreadMentions() {
    const summary = humanRead.summary() || visit?.metadata;
    if (!summary) return null;
    const seqs = new Set(summary.unseen_mention_seqs);
    for (const message of mentionMessages) if (message.seq > summary.snapshot_seq) seqs.add(message.seq);
    return [...seqs].filter(seq => seq > state.clearedThrough && !humanRead.seen(seq)).sort((a,b) => a-b);
  }

  function reviewTarget() {
    const mentions = unreadMentions();
    return !mentions ? null : mentions[0] || (visit?.remainingSeqs.size ? Math.min(...visit.remainingSeqs) : null);
  }

  function renderMentionNotice() {
    const mentions = unreadMentions(), remaining = visit?.remainingSeqs.size || 0;
    const count = mentions?.length || 0, target = reviewTarget();
    const node = target && messageNodes.get(target)?.element;
    const root = $("timeline").getBoundingClientRect(), rect = node?.getBoundingClientRect();
    const direction = !rect || rect.top < root.top ? "↑" : rect.bottom > root.bottom ? "↓" : "";
    $("mention-notice").hidden = !visit || !mentions || !target;
    $("view-mention").textContent = count ? `${direction} 有人 @我 · ${count} 条`.trim() : `${direction} ${remaining} 条新消息`.trim();
    const message = state.messages.get(target);
    const description = count ? `查看最早未查看的提及${message ? `：${authorName(message.author, message)} 的消息` : ""}` : "查看本次尚未查看的新消息";
    $("view-mention").setAttribute("aria-label", count ? `${count} 条消息提及你，${description}` : `${remaining} 条新消息，${description}`);
    $("view-mention").title = description;
    document.title = `${count ? `(${count} 条 @你) ` : ""}${roomsUI.roomName()} · TeamX`;
  }

  // Reading-only sync must not rebuild the timeline or change its scroll state.
  function renderReading() {
    for (const message of mentionMessages) messageNodes.get(message.seq)?.element.classList.toggle("mentioned", !humanRead.seen(message.seq));
    if (visit) for (const seq of visit.remainingSeqs) if (humanRead.seen(seq) || seq <= state.clearedThrough) visit.remainingSeqs.delete(seq);
    unreadCandidates.clear();
    for (const [seq, cached] of messageNodes) if (!ownMessage(state.messages.get(seq)) &&
      (visit?.entrySnapshot === null || visit?.remainingSeqs.has(seq) || cached.mentioned && !humanRead.seen(seq))) {
      unreadCandidates.set(seq, cached.element);
    }
    renderMentionNotice();
    scheduleMessageViewing();
  }

  function scheduleMessageViewing() {
    if (viewingFramePending) return;
    viewingFramePending = true;
    requestAnimationFrame(() => { viewingFramePending = false; updateMessageViewing(); });
  }

  function updateMessageViewing() {
    if (viewingPaused || positioning || !readingActive()) return;
    const root = $("timeline").getBoundingClientRect();
    const viewport = window.visualViewport;
    const viewportTop = viewport?.offsetTop || 0, viewportLeft = viewport?.offsetLeft || 0;
    const viewportHeight = viewport?.height ?? window.innerHeight, viewportWidth = viewport?.width ?? window.innerWidth;
    const top = Math.max(viewportTop, root.top), bottom = Math.min(viewportTop + viewportHeight, root.bottom);
    const left = Math.max(viewportLeft, root.left), right = Math.min(viewportLeft + viewportWidth, root.right);
    if (bottom <= top || right <= left) return;
    confirmOpened();
    let changed = false;
    // Messages form one continuous ordered timeline. Search its visible slice
    // rather than measuring thousands of off-screen nodes on every scroll.
    const candidates = messageOrder.length > 200
      ? visibleMessages(top, bottom).map(seq => [seq, unreadCandidates.get(seq)]).filter(([,el]) => el)
      : unreadCandidates;
    for (const [seq, el] of candidates) {
      const rect = el.getBoundingClientRect();
      if (!rect || rect.height <= 0 || rect.width <= 0) continue;
      // Half a message must be visible; tall messages need half the viewport.
      const height = Math.min(bottom, rect.bottom) - Math.max(top, rect.top);
      const width = Math.min(right, rect.right) - Math.max(left, rect.left);
      if (height < Math.min(rect.height, root.height, viewportHeight) / 2 ||
        width < Math.min(rect.width, root.width, viewportWidth) / 2) continue;
      if (visit.observed) visit.observed.add(seq);
      visit.remainingSeqs.delete(seq);
      const message = state.messages.get(seq);
      if (message && mentionRanges(message.text, operatorNames()).length && state.connected && !discussionBusy()) humanRead.observe(seq);
      // Keep an unconfirmed mention eligible while the service is read-only/busy.
      if (!message || !mentionRanges(message.text, operatorNames()).length || humanRead.seen(seq)) unreadCandidates.delete(seq);
      el.classList.toggle("mentioned", !!message && mentionRanges(message.text, operatorNames()).length > 0 && !humanRead.seen(seq));
      changed = true;
    }
    if (changed) renderMentionNotice();
  }

  function resetReadingContext() {
    humanRead.reset();
    conversationVisibilityChanged();
  }

  // Each read fixes a snapshot. All continuation pages keep that exact boundary.
  function readPages(params, info, signal) {
    return collectPages((page) => rpc("/v1/read", page, info, signal), params, signal);
  }

  const messageNodes = new Map(), dayNodes = new Map(), references = new Map();
  let renderedKey = null, messageOrder = [];
  const renderingKey = () => JSON.stringify([state.members.map(m => [m.agent_id,m.membership_id,agentName(m)]),
    operatorNames(), userMenuUI.displayName(), roomsUI.isDefaultAssistant(), settingsUI.systemTime.zone, settingsUI.systemTime.error]);
  const messageDay = message => settingsUI.systemTime.day(new Date(message.created_at));
  function dayElement(message, day = messageDay(message)) {
    let divider = dayNodes.get(day);
    if (!divider) { divider = document.createElement("div"); divider.className = "day-divider"; dayNodes.set(day, divider); }
    divider.textContent = settingsUI.systemTime.error ? settingsUI.formatDate(message.created_at)
      : `${settingsUI.formatDate(message.created_at, {month:"long",day:"numeric"})} ${settingsUI.formatDate(message.created_at, {weekday:"short"})}`;
    return divider;
  }

  function rememberMessage(message, cached) {
    messageNodes.set(message.seq, cached);
    if (message.reply_to) {
      if (!references.has(message.reply_to)) references.set(message.reply_to, new Set());
      references.get(message.reply_to).add(message.seq);
    }
  }

  function renderReference(message, element) {
    const target = state.messages.get(message.reply_to);
    element.querySelector(".message-reference").textContent = target
      ? `↳ #${target.seq} ${authorName(target.author,target)}：${displayText(target)}`
      : message.reply_to <= state.clearedThrough ? `↳ 引用消息 #${message.reply_to} · 已清空`
        : `↳ 引用消息 #${message.reply_to} · 加载更早消息可查看`;
  }

  function refreshReferences(seq) {
    for (const referring of references.get(seq) || []) {
      const cached = messageNodes.get(referring), message = state.messages.get(referring);
      if (cached && message) renderReference(message,cached.element);
    }
  }

  function trackMessage(message, cached) {
    if (ownMessage(message)) return;
    if (cached.mentioned) mentionMessages.push(message);
    if (visit?.entrySnapshot === null || visit?.remainingSeqs.has(message.seq) || cached.mentioned && !humanRead.seen(message.seq)) {
      unreadCandidates.set(message.seq, cached.element);
    }
  }

  function finishMessages() {
    $("empty").hidden = state.messages.size !== 0;
    $("load-history").hidden = state.oldest <= state.clearedThrough + 1;
    renderDivider(); renderReply(); renderMentionNotice();
    scheduleMessageViewing();
  }

  function messageElement(message) {
    const el = $("message-template").content.firstElementChild.cloneNode(true);
    el.id = `message-${message.seq}`;
    el.classList.toggle("own", ownMessage(message));
    const agent = message.author.kind === "agent", integration = message.author.kind === "integration";
    el.classList.toggle("integration", integration);
    const avatar = el.querySelector(".avatar");
    renderAvatar(document, avatar, {kind:message.author.kind, id:message.author.id,
      provider:message.source?.provider || message.author.id, direct:roomsUI.isDirect(), defaultAssistant:roomsUI.isDefaultAssistant(), name:authorName(message.author,message)});
    const channel = message.source?.provider === "gitlab" && ["events", "todos"].includes(message.source?.channel) ? message.source.channel : null;
    el.querySelector(".author").textContent = integration ? channel ? `GitLab · ${channel === "todos" ? "我的待办" : "项目活动"}` : `${message.author.id} · 通知` : authorName(message.author,message);
    if (integration && channel) {
      const author = el.querySelector(".author");
      author.disabled = false;
      author.setAttribute("aria-label", "查看 GitLab 通知源");
      author.setAttribute("aria-controls", "notification-detail");
      author.addEventListener("click", () => notificationsUI.openNotificationMessage(author, message.source));
      avatar.setAttribute("role", "button"); avatar.setAttribute("tabindex", "0");
      avatar.setAttribute("aria-label", "查看 GitLab 通知源");
      avatar.setAttribute("aria-controls", "notification-detail");
      avatar.addEventListener("click", () => notificationsUI.openNotificationMessage(avatar, message.source));
      avatar.addEventListener("keydown", event => {
        if (event.key === "Enter" || event.key === " ") { event.preventDefault(); return notificationsUI.openNotificationMessage(avatar, message.source); }
      });
    } else if (agent) {
      const author = el.querySelector(".author");
      const open = opener => {
        const member = state.members.find(item => item.agent_id === message.author.id && (!message.author_membership_id || item.membership_id === message.author_membership_id));
        if (member) membersUI.openMemberActivity(member, opener, undefined, {fromConversation:true});
      };
      author.disabled = false;
      author.setAttribute("aria-label", `查看 ${authorName(message.author,message)} 的详情`);
      author.setAttribute("aria-controls", "activity-dialog");
      author.addEventListener("click", () => open(author));
      avatar.setAttribute("role", "button"); avatar.setAttribute("tabindex", "0");
      avatar.setAttribute("aria-label", `查看 ${authorName(message.author,message)} 的详情`);
      avatar.setAttribute("aria-controls", "activity-dialog");
      avatar.addEventListener("click", () => open(avatar));
      avatar.addEventListener("keydown", event => {
        if (event.key === "Enter" || event.key === " ") { event.preventDefault(); open(avatar); }
      });
    }
    const text = el.querySelector(".message-text");
    const ranges = mentionRanges(message.text, operatorNames());
    renderMessageBody(text, message.text, state.operator, messageOptions(message));
    el.classList.toggle("mentioned", !humanRead.seen(message.seq) && ranges.length > 0 && !(message.author.kind === "human" && message.author.id === state.operator));
    const time = el.querySelector("time");
    time.textContent = settingsUI.formatDate(message.created_at, {hour:"2-digit",minute:"2-digit"});
    time.dateTime = message.created_at;
    time.title = settingsUI.formatDate(message.created_at);
    el.querySelector(".reply-button").addEventListener("click", () => beginReply(message));
    if (message.reply_to) {
      const ref = el.querySelector(".message-reference");
      ref.hidden = false;
      renderReference(message,el);
      ref.addEventListener("click", () => revealMessage(message.reply_to));
    }
    return el;
  }

  function renderMessages() {
    humanRead.prune();
    // Ordinary polling after a long history load only constructs new messages.
    // Identity/time-zone changes still use the existing complete reconciliation.
    const key = renderingKey();
    if (renderedKey === key && messageNodes.size <= state.messages.size) {
      const added = [...state.messages.values()].filter(message => !messageNodes.has(message.seq)).sort((a,b) => a.seq-b.seq);
      const last = messageOrder.at(-1) || 0;
      if (!added.length || added[0].seq > last) {
        let day = last ? messageNodes.get(last).day : "";
        const fragment = document.createDocumentFragment();
        for (const message of added) {
          if (messageDay(message) !== day) { day = messageDay(message); fragment.append(dayElement(message)); }
          const cached = {element:messageElement(message),memberNames:key,mentioned:mentionRanges(message.text,operatorNames()).length>0,day:messageDay(message)};
          rememberMessage(message,cached); trackMessage(message,cached); fragment.append(cached.element);
        }
        $("messages").append(fragment);
        messageOrder.push(...added.map(message => message.seq));
        interactionsUI.renderPlatformInteractions();
        if (!state.oldest) state.oldest = added[0]?.seq || 0;
        finishMessages();
        return;
      }
    }
    unreadCandidates.clear(); mentionMessages = [];
    const nodes = [];
    const messages = [...state.messages.values()].sort((a, b) => a.seq - b.seq);
    const memberNamesKey = renderingKey();
    let day = "";
    for (const message of messages) {
      const date = new Date(message.created_at);
      const nextDay = settingsUI.systemTime.day(date);
      if (nextDay !== day) {
        day = nextDay;
        let divider=dayNodes.get(day);
        if(!divider){divider=document.createElement("div");divider.className="day-divider";dayNodes.set(day,divider);}
        divider.textContent = settingsUI.systemTime.error ? settingsUI.formatDate(date) : `${settingsUI.formatDate(date, {month:"long",day:"numeric"})} ${settingsUI.formatDate(date, {weekday:"short"})}`;
        nodes.push(divider);
      }
      let cached=messageNodes.get(message.seq);
      if(!cached){cached={element:messageElement(message),memberNames:memberNamesKey};rememberMessage(message,cached);}
      const el=cached.element;
      el.classList.toggle("own", message.author.kind === "human" && message.author.id === state.operator);
      if (message.author.kind !== "integration") el.querySelector(".author").textContent = authorName(message.author,message);
      if(message.author.kind==="agent")for(const selector of [".avatar",".author"])el.querySelector(selector).setAttribute("aria-label",`查看 ${authorName(message.author,message)} 的详情`);
      if(message.author.kind==="agent" && cached.memberNames!==memberNamesKey)renderAvatar(document,el.querySelector(".avatar"),{kind:"agent",id:message.author.id,name:authorName(message.author,message),direct:roomsUI.isDirect(),defaultAssistant:roomsUI.isDefaultAssistant()});
      if (cached.memberNames !== memberNamesKey) {
        renderMessageBody(el.querySelector(".message-text"), message.text, state.operator, messageOptions(message));
        cached.memberNames = memberNamesKey;
      }
      const mentioned=mentionRanges(message.text,operatorNames()).length>0;
      cached.mentioned = mentioned; cached.day = day;
      const own = message.author.kind === "human" && message.author.id === state.operator;
      el.classList.toggle("mentioned", !humanRead.seen(message.seq) && mentioned && !own);
      if (!own) {
        if (mentioned) mentionMessages.push(message);
        if (visit?.entrySnapshot === null || visit?.remainingSeqs.has(message.seq) || mentioned && !humanRead.seen(message.seq)) unreadCandidates.set(message.seq, el);
      }
      el.querySelector("time").textContent=settingsUI.formatDate(message.created_at,{hour:"2-digit",minute:"2-digit"});
      el.querySelector("time").title=settingsUI.formatDate(message.created_at);
      if (message.reply_to) renderReference(message,el);
      nodes.push(el);
    }
    for(const seq of messageNodes.keys())if(!state.messages.has(seq))messageNodes.delete(seq);
    reconcileChildren($("messages"),nodes);
    interactionsUI.renderPlatformInteractions();
    state.oldest = messages[0]?.seq || 0;
    messageOrder = messages.map(message => message.seq);
    renderedKey = memberNamesKey;
    finishMessages();
  }

  function firstVisible(top) {
    let lo = 0, hi = messageOrder.length;
    while (lo < hi) {
      const mid = (lo + hi) >>> 1;
      if (messageNodes.get(messageOrder[mid]).element.getBoundingClientRect().bottom <= top) lo = mid + 1;
      else hi = mid;
    }
    return lo;
  }

  function visibleMessages(top, bottom) {
    const result = [];
    for (let i = firstVisible(top); i < messageOrder.length; i++) {
      const seq = messageOrder[i];
      if (messageNodes.get(seq).element.getBoundingClientRect().top >= bottom) break;
      result.push(seq);
    }
    return result;
  }

  // One cancellable task owns both manual history and automatic review loading.
  // A cancelled response can finish in the transport, but can never commit nodes.
  function cancelHistory(text = "") {
    if (historyTask) historyTask.controller.abort();
    historyTask = null;
    $("history-progress").hidden = true;
    $("load-history").disabled = false;
    $("load-history").textContent = "加载更早消息";
    $("view-mention").disabled = false;
    if (text) notice("review-notice", text);
  }

  const yieldHistory = task => new Promise(resolve => {
    const signal = task.controller.signal;
    const finish = () => { signal.removeEventListener("abort",finish); resolve(); };
    signal.addEventListener("abort",finish,{once:true});
    if (signal.aborted) finish(); else requestAnimationFrame(finish);
  });
  const validHistory = task => historyTask === task && !task.controller.signal.aborted && currentView(task.version) &&
    task.visit === visit && task.key === readingKey() && task.cleared === state.clearedThrough;

  function positionReview(seq) {
    const target = messageNodes.get(seq)?.element;
    if (!target || !visit || seq <= state.clearedThrough) return;
    const current = visit;
    const id = ++positioningID;
    positioning = true;
    target.scrollIntoView({block:"center", behavior:"instant"});
    target.tabIndex = -1; target.focus({preventScroll:true});
    target.classList.remove("flash");
    requestAnimationFrame(() => requestAnimationFrame(() => {
      if (id !== positioningID) return;
      positioning = false;
      if (visit === current && messageNodes.get(seq)?.element === target) {
        target.classList.add("flash"); scheduleMessageViewing();
      }
    }));
  }

  function locateReview() {
    const seq = reviewTarget();
    if (!seq || historyTask) return;
    notice("review-notice", "");
    if (messageNodes.has(seq)) { positionReview(seq); return; }
    return loadHistory(seq);
  }

  async function insertHistory(task, messages) {
    const key = renderingKey(), built = new Map();
    let start = performance.now();
    for (const message of messages) {
      if (!validHistory(task)) return false;
      if (!messageNodes.has(message.seq)) built.set(message.seq, {
        element:messageElement(message), memberNames:key, mentioned:mentionRanges(message.text,operatorNames()).length>0, day:messageDay(message) });
      if (performance.now() - start >= 6) {
        await yieldHistory(task); start = performance.now();
      }
    }
    if (!validHistory(task)) return false;
    if (key !== renderingKey()) throw new Error("会话显示设置已变化，请重试。");
    const added = messages.filter(message => !messageNodes.has(message.seq));
    if (!added.length) throw new Error("历史分页没有前进，请重试。");
    const timeline = $("timeline"), top = timeline.getBoundingClientRect().top;
    const anchor = messageNodes.get(messageOrder[firstVisible(top)])?.element;
    const offset = anchor?.getBoundingClientRect().top;
    const height = timeline.scrollHeight, scroll = timeline.scrollTop;
    viewingPaused = true;
    try {
      // Reuse the boundary day's existing divider, moving it before the new
      // prefix only in this short synchronous commit. Never rebuild old nodes.
      const fragment = document.createDocumentFragment();
      let day = "";
      for (const message of added) {
        const cached = built.get(message.seq);
        if (cached.day !== day) { day = cached.day; fragment.append(dayElement(message, day)); }
        state.messages.set(message.seq,message); rememberMessage(message,cached);
        trackMessage(message,cached); fragment.append(cached.element);
      }
      for (const message of added) refreshReferences(message.seq);
      $("messages").insertBefore(fragment, $("messages").firstChild);
      messageOrder = [...added.map(message => message.seq), ...messageOrder];
      state.oldest = messageOrder[0];
      // Moving the fixed new-message divider can also change height above us.
      renderDivider();
      if (anchor) timeline.scrollTop += anchor.getBoundingClientRect().top - offset;
      else timeline.scrollTop = scroll + timeline.scrollHeight - height;
      interactionsUI.renderPlatformInteractions();
      finishMessages();
    } finally {
      requestAnimationFrame(() => { viewingPaused = false; scheduleMessageViewing(); });
    }
    return true;
  }

  async function loadHistory(target = null) {
    if (historyTask || !state.info || state.oldest <= state.clearedThrough + 1) return;
    const task = {controller:new AbortController(), version:runtime.viewVersion, key:readingKey(), visit,
      cleared:state.clearedThrough, info:{...state.info}, loaded:0, target};
    historyTask = task;
    $("load-history").disabled = true; $("load-history").textContent = "加载中…";
    $("view-mention").disabled = true;
    $("history-progress").hidden = false;
    $("cancel-history").textContent = target ? "取消定位" : "取消加载";
    notice("review-notice", "");
    try {
      do {
        $("history-status").textContent = `正在加载更早消息 · 已加载 ${task.loaded} 条`;
        const oldest = state.oldest;
        // If the target lands at the first node of a batch, fetch just the
        // missing preceding context without expanding a snapshot beyond 50.
        const after = Math.max(task.cleared, target && messageNodes.has(target) ? target-6 : oldest-51);
        const read = await readPages({mode:"range", after, snapshot_seq:oldest-1}, task.info, task.controller.signal);
        if (!validHistory(task) || read.clearedThrough < state.clearedThrough) return;
        if (read.clearedThrough > task.cleared) {
          applyClear(read.clearedThrough);
          renderMessages();
          void refresh();
          return;
        }
        if (!await insertHistory(task,read.messages)) return;
        task.loaded += read.messages.length;
        // Yield between bounded snapshots so input, cancellation and live polling
        // can run while the retained timeline remains continuous.
        await yieldHistory(task);
        if (!validHistory(task)) return;
        if (target && reviewTarget() !== target) {
          target = reviewTarget();
          if (!target) return;
        }
      } while (target && state.oldest > task.cleared+1 &&
        (!messageNodes.has(target) || state.oldest > Math.max(task.cleared+1,target-5)));
      if (target && validHistory(task)) {
        if (!messageNodes.has(target)) throw new Error("目标消息已不可用，请重新选择。");
        positionReview(target);
      }
    } catch (error) {
      if (validHistory(task)) notice("review-notice", `历史加载失败，再次点击可继续。${error.message}`);
    } finally {
      if (historyTask === task) cancelHistory();
    }
  }

  function applyClear(through) {
    if (through <= state.clearedThrough) return false;
    state.clearedThrough = through;
    if (historyTask) cancelHistory("历史已清空，请重新选择回看目标。");
    if (visit) {
      for (const seq of visit.remainingSeqs) if (seq <= through) visit.remainingSeqs.delete(seq);
      if (visit.observed) for (const seq of visit.observed) if (seq <= through) visit.observed.delete(seq);
      if (visit.dividerSeq <= through) { visit.dividerSeq = null; newDivider.remove(); }
    }
    for (const seq of state.messages.keys()) if (seq <= through) {
      const replyTo = state.messages.get(seq).reply_to;
      if (replyTo) { references.get(replyTo)?.delete(seq); if (!references.get(replyTo)?.size) references.delete(replyTo); }
      state.messages.delete(seq); messageNodes.get(seq)?.element.remove(); messageNodes.delete(seq); unreadCandidates.delete(seq);
    }
    for (const target of references.keys()) if (target <= through) refreshReferences(target);
    messageOrder = messageOrder.filter(seq => seq > through);
    mentionMessages = mentionMessages.filter(message => message.seq > through);
    state.oldest = messageOrder[0] || 0;
    const firstDay = messageNodes.get(state.oldest)?.day;
    for (const [day, divider] of dayNodes) if (!firstDay || day < firstDay) { divider.remove(); dayNodes.delete(day); }
    if (state.draft.replyTo && state.draft.replyTo <= through) clearReply();
    return true;
  }

  function beginReply(message) {
    if(state.draft.pending || !state.connected || discussionBusy()) return;
    state.draft.replyTo=message.seq;
    state.draft.replyAuthor=message.author.id;
    state.draft.replyLabel=displayText(message);
    renderReply();saveDraft();$("message-input").focus();
  }

  function renderReply() {
    const seq=state.draft.replyTo, message=state.messages.get(seq);
    const author=message?.author.id || state.draft.replyAuthor;
    $("reply-context").hidden=!seq;
    $("reply-author").textContent=author ? `引用 ${author === state.operator ? "你" : message ? authorName(message.author,message) : author} 的消息` : "引用消息";
    $("reply-sequence").textContent=seq ? `#${seq}` : "";
    $("reply-label").textContent=seq ? (message ? displayText(message) : state.draft.replyLabel || "原消息未载入").replace(/\s+/g," ").trim() : "";
    $("reply-preview").title=message ? "定位原消息" : "加载更早消息后可定位原文";
    $("reply-preview").setAttribute("aria-label",`定位引用的消息 #${seq}`);
  }

  function revealMessage(seq) {
    const target=messageNodes.get(seq)?.element;
    if (!target) { $("load-history").focus(); return; }
    target.scrollIntoView({block:"center"});
    target.tabIndex=-1; target.focus({preventScroll:true});
    target.classList.remove("flash");
    requestAnimationFrame(()=>target.classList.add("flash"));
  }

  function saveDraft() {
    if (!state.info) return;
    state.draft.text = $("message-input").value;
    state.draft.mentions = mentionPicker.boundMentions();
    state.draft.replyLabel = $("reply-label").textContent;
    drafts.save(state.info, state.operator, state.draft);
  }

  function showDraft() {
    mentionPicker.close();
    $("message-input").value = state.draft.text || "";
    mentionPicker.renderText();
    renderReply();
    notice("send-error", state.draft.pending ? "有一条尚未确认的消息，重试保留原会话和服务身份。请先核对历史。" : "");
  }

  let emptyConversationKey = "";

  // Keep welcome content stable across polling and composer updates. In
  // particular, never show temporary room copy while a direct read is pending.
  function renderEmptyConversation(direct, name) {
    const suggestions = $("direct-suggestions");
    const hidden = !direct || !state.info || state.messages.size > 0;
    if (suggestions.hidden !== hidden) suggestions.hidden = hidden;
    const hasLark = !!workspaceUI.workspaceView?.allPlatforms?.some(item => item.provider === "lark" && item.validation?.status === "valid");
    const key = JSON.stringify([direct, !!state.info, name, hasLark]);
    if (key === emptyConversationKey) return;
    emptyConversationKey = key;
    const icon = $("empty-conversation-icon"), kind = direct ? "assistant" : "room";
    if (icon.dataset.kind !== kind) {
      if (direct) icon.replaceChildren(createAssistantIcon(document));
      else icon.textContent = "#";
      icon.dataset.kind = kind;
    }
    $("empty").querySelector("h2").textContent = !state.info ? "开始一个新任务"
      : direct ? `与${name || "个人助理"}聊聊` : "从一条消息开始";
    $("empty").querySelector("p").textContent = !state.info ? "从左侧新建私信与助理交流，或新建房间与团队协作。"
      : direct ? "梳理待办、总结信息、起草内容，也可以连接飞书，关注群消息。" : "发送任务或想法，让房间里的 Agent 一起参与。";
    if (direct && state.info) {
      const prompts = [
        ["整理待办", "帮我整理一下待办事项"],
        hasLark ? ["总结内容", "帮我总结以下内容：\n"] : ["你能做什么", "介绍一下你能帮我做什么"],
        hasLark ? ["起草回复", "帮我起草一段回复：\n"] : ["连接飞书", "帮我连接飞书"],
      ];
      Array.from(suggestions.children).forEach((button, index) => {
        const [label, prompt] = prompts[index];
        button.textContent = label;
        button.dataset.suggestion = prompt;
      });
    }
  }

  function renderConversation() {
    const direct = roomsUI.isDirect(), type = roomsUI.conversationType(), name = roomsUI.roomName();
    renderEmptyConversation(direct, name);
    $("conversation").setAttribute("aria-label", `${type}消息`);
    $("member-rail").setAttribute("aria-label", direct ? "助理详情与会话通知" : "房间详情");
    $("member-sidebar").setAttribute("aria-label", direct ? "会话通知" : "成员与房间通知");
    $("policy-empty").textContent = `可以在${direct ? "私信" : "房间"}中告诉 Agent 今后应如何处理。`;
    const recipient = `发送给 ${name || (direct ? "个人助理" : "会话")}`;
    $("message-input").placeholder = recipient;
    $("message-input-label").textContent = recipient;
    $("rename-room").title = `修改${type}名称`;
    $("rename-form").setAttribute("aria-label", `修改${type}名称`);
    $("rename-room-name").setAttribute("aria-label", `${type}名称`);
    $("room-menu-label").setAttribute("aria-label", direct ? "助理详情" : "房间详情");
    $("room-detail-name").textContent = name;
    $("room-title-count").textContent = `(${membersUI.participantCount()})`;
    $("room-title-count").hidden = direct || !state.info;
    $("room-details").hidden = direct;
    $("room-members-heading").hidden = direct;
    $("restart-discussion").title = direct ? "重新开始私信，清空消息并重置助理会话" : "重新开始房间讨论";
    $("direct-notifications").hidden = !direct || !state.managed;
    $("direct-notifications").disabled = !state.connected || !workspaceUI.workspaceView;
    $("toggle-add").hidden = state.managed || direct;
    if (direct) $("add-form").hidden = true;
    $("notification-sources-label").textContent = direct ? "会话通知" : "房间通知";
    $("notification-empty").textContent = `将平台通知发送到当前${direct ? "会话" : "房间"}。`;
    $("notification-platforms").setAttribute("aria-label", `发送到当前${direct ? "会话" : "房间"}的通知`);
    $("notification-add-toggle").setAttribute("aria-label", `添加${direct ? "会话" : "房间"}通知`);
    $("notification-add-toggle").title = `添加${direct ? "会话" : "房间"}通知`;
  }

  function composerState() {
    renderConversation();
    settingsUI.renderSettingsNavigation();
    const namedRoom = state.catalog?.rooms.find(room => room.room_instance === state.info?.room_instance);
    $("rename-room").setAttribute("aria-label", namedRoom ? `${namedRoom.name}，修改${roomsUI.conversationType()}名称` : $("room-name").textContent);
    $("rename-room").disabled = roomsUI.isDirect() || !state.managed || !state.catalogConnected || discussionBusy() || !!state.catalog?.write_error || namedRoom?.status !== "ready" || roomsUI.renameSending;
    $("save-room-name").disabled = $("rename-room").disabled || !roomsUI.renameTarget;
    $("rename-room-name").disabled = roomsUI.renameSending;
    $("save-room-name").textContent = roomsUI.renameSending ? "保存中…" : "保存";
    membersUI.renderExecutionHint();
    $("message-input").disabled = !state.info;
    $("message-input").readOnly = !!state.draft.pending || discussionBusy();
    mentionPicker.refresh();
    $("send").disabled = !state.connected || state.draft.sending || discussionBusy() || (!state.draft.pending && !$("message-input").value.trim());
    $("cancel-reply").disabled=!!state.draft.pending;
    $("discard-send").hidden = !state.draft.pending || state.draft.sending;
    $("restart-discussion").disabled = !state.connected || workspaceUI.workspaceBusy() || roomsUI.managementBusy() || state.draft.restartStarting || state.restart?.status === "running" || !!state.draft.pending || state.draft.sending;
    $("restart-discussion").textContent = state.draft.restartStarting || state.restart?.status === "running" ? "重新开始中…" : state.draft.restartRequest ? "核对并重试" : "重新开始";
    $("toggle-add").disabled = !state.connected || discussionBusy();
    $("add-member").disabled = !state.connected || !state.profiles.length || discussionBusy();
    renderRestart(); workspaceUI.renderWorkspace(); syncComposerLayout();
  }

  function syncComposerLayout() {
    const input = $("message-input");
    const hasMessage = !!input.value.trim() || !!state.draft.pending || !!state.draft.sending;
    $("compose").classList.toggle("has-message", hasMessage);
    const sendLabel = state.draft.sending ? "发送中…" : state.draft.pending ? "重试发送" : "发送";
    $("send").setAttribute("aria-label", sendLabel);
    $("send").setAttribute("aria-busy", String(!!state.draft.sending));
    $("send").title = sendLabel;
    sendIconPath.setAttribute("d", state.draft.sending ? "M20 12a8 8 0 1 1-5-7.4"
      : state.draft.pending ? "M20 11a8 8 0 1 0-2.3 6.7M20 4v7h-7" : "M12 19V5M5 12l7-7 7 7");
    $("send").replaceChildren(sendIcon);
    if (hasMessage) workspaceUI.closeWorkspacePicker();
    const atBottom = nearBottom(), previousHeight = input.getBoundingClientRect().height;
    input.rows = 1;
    input.style.setProperty("height", "auto");
    input.style.setProperty("height", `${input.scrollHeight}px`);
    if (atBottom && input.getBoundingClientRect().height !== previousHeight) bottom();
    mentionPicker.renderText();
  }

  function clearReply() {
    state.draft.replyTo = 0;
    state.draft.replyAuthor = "";
    state.draft.replyLabel = "";
    renderReply();
    saveDraft();
  }

  function resetRestartNotice() { restartNoticeID = ""; restartNoticeExpires = 0; }

  function resetMessageNodes() { cancelHistory(); renderedKey = null; messageOrder = []; messageNodes.clear(); dayNodes.clear(); references.clear(); }

  function interactionHost(seq) {
    return messageNodes.get(seq)?.element.querySelector(".message-interactions");
  }

  function mount() {
    $("direct-suggestions").addEventListener("click", event => {
      const suggestion = event.target.closest?.("[data-suggestion]");
      if (!suggestion || !roomsUI.isDirect() || !state.info || $("message-input").readOnly || state.draft.pending) return;
      $("message-input").value = suggestion.dataset.suggestion; saveDraft(); composerState(); $("message-input").focus();
    });

    $("direct-notifications").addEventListener("click", () => {
      if ($("direct-notifications").disabled) return;
      notificationsReturn = { view: captureReturnView(env, runtime, { opener: $("direct-notifications") }), panel: layoutUI.captureMemberPanel() };
      layoutUI.showConversationNotifications();
      $("activity-dialog").hidden = true;
      layoutUI.expandMembers();
      layoutUI.renderPanels(); notificationsUI.renderNotificationPlatforms(); $("close-conversation-notifications").focus();
    });

    $("close-conversation-notifications").addEventListener("click", returnConversationNotifications);
    $("member-list-view").addEventListener("keydown", event => {
      if (event.key !== "Escape" || event.defaultPrevented || !layoutUI.directNotificationsOpen ||
        $("notification-detail").open || document.querySelector("dialog:modal")) return;
      event.preventDefault(); returnConversationNotifications();
    });

    $("view-mention").addEventListener("click", () => locateReview());
    $("cancel-history").addEventListener("click", () => cancelHistory("已取消定位；再次点击可继续。"));

    $("restart-discussion").addEventListener("click", async () => {
      if ($("restart-discussion").disabled) return;
      const info = state.info, operator = state.operator, draft = state.draft, version = runtime.viewVersion;
      if (!draft.restartRequest && !await confirmAction({ title:roomsUI.isDirect() ? "重新开始私信？" : "重新开始房间讨论？", message:roomsUI.isDirect() ? "将停止助理当前的执行，清空消息并重置助理上下文。工作目录中的文件保留。" : "将停止所有 Agent 当前的执行，清空消息并重置 Agent 上下文。成员、角色和工作目录中的文件保留，回合预算不重置。", confirmLabel:"重新开始" })) return;
      if (!currentView(version) || state.draft !== draft || state.operator !== operator || $("restart-discussion").disabled) return;
      draft.restartRequest ||= { id: randomUUID().replaceAll("-", ""), info, previous_id: state.restart?.id || "" };
      restartNoticeID = draft.restartRequest.id; restartNoticeExpires = 0;
      draft.restartStarting = true; draft.restartError = ""; saveDraft();
      composerState(); membersUI.renderMembers(); roomsUI.renderCreateSelection(); roomsUI.renderRooms();
      try {
        const result = await rpc("/v1/discussion/restart", { restart_id: draft.restartRequest.id,
          previous_restart_id: draft.restartRequest.previous_id || "" }, draft.restartRequest.info);
        draft.restartRequest = null;
        if (currentView(version)) { state.restart = result; clearReply(); }
      } catch (error) {
        if (["request", "identity", "conflict", "busy"].includes(error.code)) draft.restartRequest = null;
        draft.restartError = `重新开始未确认，请同步状态或用原请求重试。${error.message}`;
      } finally {
        draft.restartStarting = false; drafts.save(info, operator, draft);
        await refresh(); composerState(); membersUI.renderMembers(); roomsUI.renderCreateSelection();
      }
    });

    $("compose").addEventListener("submit", async (event) => {
      event.preventDefault();
      if (composing || state.draft.sending || discussionBusy() || !state.connected) return;
      const text = $("message-input").value;
      if (!state.draft.pending && !text.trim()) return;
      if (!state.draft.pending && new TextEncoder().encode(text).length > 16384) {
        notice("send-error", "消息过长，请控制在 16 KiB 内。"); return;
      }
      const info = state.info, operator = state.operator, draft = state.draft, version = runtime.viewVersion;
      draft.pending ||= { text, mentions:mentionPicker.boundMentions(), client_message_id: randomUUID(), reply_to: draft.replyTo, info, operator };
      saveDraft(); draft.sending = true;
      composerState(); notice("send-error", "");
      try {
        const pending = draft.pending;
        const receipt = await rpc("/v1/send", { text: pending.text, client_message_id: pending.client_message_id,
          reply_to: pending.reply_to, mentions:pending.mentions || [], caller: { kind: "human", id: pending.operator } }, pending.info);
        draft.pending = null; draft.text = ""; draft.mentions=[]; draft.replyTo = 0; draft.replyLabel = ""; draft.replyAuthor = "";
        drafts.save(info, operator, draft);
        if (state.draft === draft) showDraft();
        if (currentView(version)) {
          $("send-success").textContent = `消息 #${receipt.seq} 已发送`;
          await refresh(); if (currentView(version)) bottom();
        }
      } catch (error) {
        if (currentView(version)) notice("send-error", error.code === "identity"
          ? "原发送身份已失效。请先核对本会话历史，再放弃重试；不会自动换用新身份发送。"
          : `未确认发送成功，重试保留原会话身份与消息 ID。${error.message}`);
      } finally {
        draft.sending = false;
        if (currentView(version)) { composerState(); $("message-input").focus(); }
      }
    });

    $("discard-send").addEventListener("click", () => {
      state.draft.pending = null; saveDraft(); composerState();
      notice("send-error", "已放弃重试。原消息可能已提交，请先查看消息记录，再决定是否重新发送。");
    });

    mentionPicker.mount();
    if (typeof ResizeObserver !== "undefined") new ResizeObserver(mentionPicker.renderText).observe($("message-input"));
    $("message-input").addEventListener("input", () => { saveDraft(); composerState(); });
    window.addEventListener("resize", syncComposerLayout);

    $("message-input").addEventListener("compositionstart", () => { composing = true; });
    $("message-input").addEventListener("compositionend", () => { composing = false; compositionEnded = performance.now(); saveDraft(); });
    $("message-input").addEventListener("keydown", (event) => {
      if (composing || event.isComposing || event.keyCode === 229 || event.key === "Enter" && performance.now() - compositionEnded < 50) return;
      if (mentionPicker.keydown(event)) return;
      // IME confirmation (including browsers reporting keyCode 229) never sends.
      if (event.key !== "Enter" || event.isComposing || event.keyCode === 229 || event.altKey) return;
      if (event.ctrlKey || event.metaKey) {
        event.preventDefault();
        const input = $("message-input");
        if (input.disabled || input.readOnly) return;
        input.setRangeText("\n", input.selectionStart, input.selectionEnd, "end");
        saveDraft(); composerState();
      } else if (!event.shiftKey) {
        event.preventDefault();
        if (!event.repeat && !$("send").disabled) $("compose").requestSubmit();
      }
    });

    $("cancel-reply").addEventListener("click", () => { if (!state.draft.pending) { clearReply(); $("message-input").focus(); } });
    $("reply-preview").addEventListener("click", () => revealMessage(state.draft.replyTo));

    $("jump-latest").addEventListener("click", () => {
      bottom();
      confirmOpened();
      if (readingActive() && state.connected && !discussionBusy() && visit?.opened) humanRead.openThrough(state.lastSeq);
      scheduleMessageViewing();
    });

    $("timeline").addEventListener("scroll", () => {
      if (nearBottom()) {
        $("jump-latest").hidden = true;
        if (!positioning && visit?.opened && readingActive() && state.connected && !discussionBusy()) humanRead.openThrough(state.lastSeq);
      }
      renderMentionNotice();
      scheduleMessageViewing();
    });

    $("timeline").addEventListener("load", scheduleMessageViewing, true);

    window.addEventListener("resize", scheduleMessageViewing);

    window.visualViewport?.addEventListener("resize", scheduleMessageViewing);
    window.visualViewport?.addEventListener("scroll", scheduleMessageViewing);

    window.addEventListener("scroll", scheduleMessageViewing, { passive: true });

    window.addEventListener("focus", updateMessageViewing);

    if (typeof ResizeObserver !== "undefined") new ResizeObserver(scheduleMessageViewing).observe($("timeline"));

    $("load-history").addEventListener("click", () => loadHistory());
  }

  return {
    mount,
    returnConversationNotifications,
    resetRestartNotice,
    resetMessageNodes,
    interactionHost,
    renderMessages,
    composerState,
    beginReply,
    nearBottom,
    saveDraft,
    resetReadingContext,
    pollHumanRead,
    conversationVisibilityChanged,
    leaveConversation,
    messagesRead,
    humanReadSummary: humanRead.summary,
    drafts,
    showDraft,
    readPages,
    applyClear,
    bottom,
    renderMentionNotice,
    updateMessageViewing,
    scheduleMessageViewing
  };
}
