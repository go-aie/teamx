import { randomUUID } from "../random-id.mjs";
import { readPages as collectPages, createRoomDrafts, mentionRanges, unseenMentions } from "../client.mjs";
import { notificationOptions } from "../markdown.mjs";
import { createAssistantIcon, createUserIcon } from "../icons.mjs";
import { createMentionPicker } from "../mention-picker.mjs";

// Messages, drafts, mentions and the send/restart lifecycle.
export function createConversation({ env, runtime, roomsUI, notificationsUI, membersUI, settingsUI, interactionsUI, workspaceUI, layoutUI }) {
  const { document, window, requestAnimationFrame, performance, setTimeout, clearTimeout, ResizeObserver, renderMessageBody } = env;
  const { state, $, notice, rpc, currentView, refresh, discussionBusy, reconcileChildren, storage, confirmAction } = runtime;

  let historyLoading = false, composing = false, compositionEnded = -Infinity;

  const drafts = createRoomDrafts(storage, () => state.installationID);
  const mentionPicker = createMentionPicker({ document, input: $("message-input"), list: $("mention-options"), mirror: $("message-mirror"),
    members: () => state.members, operator: () => state.operator, enabled: () => !!state.info && !state.draft.pending && !discussionBusy(),
    changed: () => { saveDraft(); composerState(); } });

  let restartNoticeID = "", restartNoticeExpires = 0;

  let mentionKey = "", mentionSeenThrough = 0;

  let mentionSeen = new Set();

  const mentionVisibleSince = new Map();

  let mentionTimer = null, mentionFramePending = false;

  const mentionDwell = 1000;
  const operatorNames = () => [state.operator, state.operatorDisplayName].filter(Boolean);
  const messageOptions = message => ({ ...(message.author.kind === "integration"
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
    $("timeline").scrollTop = $("timeline").scrollHeight;
    $("jump-latest").hidden = true;
  }

  function renderMentionNotice() {
    const mentions = unreadMentions();
    $("mention-notice").hidden = mentions.length === 0;
    $("view-mention").textContent = mentions.length ? `有人 @你 · ${mentions.length} 条消息 · 查看 ${mentions[0].author.id} 的消息 ↗` : "";
    document.title = `${mentions.length ? `(${mentions.length} 条 @你) ` : ""}${roomsUI.roomName()} · TeamX`;
    scheduleMentionViewing();
  }

  function unreadMentions() {
    return unseenMentions(state.messages.values(), state.operator, mentionSeen, mentionSeenThrough, operatorNames());
  }

  function resetMentionViewing() {
    clearTimeout(mentionTimer); mentionTimer = null;
    mentionVisibleSince.clear();
  }

  function scheduleMentionViewing() {
    if (mentionFramePending) return;
    mentionFramePending = true;
    requestAnimationFrame(() => { mentionFramePending = false; updateMentionViewing(); });
  }

  function updateMentionViewing() {
    clearTimeout(mentionTimer); mentionTimer = null;
    if (!state.info || document.hidden || !document.hasFocus() || document.querySelector("dialog:modal")) {
      mentionVisibleSince.clear(); return;
    }
    const root = $("timeline").getBoundingClientRect();
    const top = Math.max(0, root.top), bottom = Math.min(window.innerHeight, root.bottom);
    const left = Math.max(0, root.left), right = Math.min(window.innerWidth, root.right);
    const now = performance.now(), visible = new Set();
    let remaining = Infinity, changed = false;
    for (const message of unreadMentions()) {
      const el = $(`message-${message.seq}`), rect = el?.getBoundingClientRect();
      if (!rect || rect.height <= 0 || rect.width <= 0 || bottom <= top || right <= left) continue;
      // Half a message must be visible; tall messages need half the viewport.
      const height = Math.min(bottom, rect.bottom) - Math.max(top, rect.top);
      const width = Math.min(right, rect.right) - Math.max(left, rect.left);
      if (height < Math.min(rect.height, root.height, window.innerHeight) / 2 ||
        width < Math.min(rect.width, root.width, window.innerWidth) / 2) continue;
      visible.add(message.seq);
      const since = mentionVisibleSince.get(message.seq) ?? now;
      mentionVisibleSince.set(message.seq, since);
      const wait = mentionDwell - (now - since);
      if (wait > 0) { remaining = Math.min(remaining, wait); continue; }
      mentionSeen.add(message.seq); mentionVisibleSince.delete(message.seq);
      el.classList.remove("mentioned"); changed = true;
    }
    for (const seq of mentionVisibleSince.keys()) if (!visible.has(seq)) mentionVisibleSince.delete(seq);
    if (changed) {
      try { storage.setItem(mentionKey, JSON.stringify({ seenThrough: mentionSeenThrough, seen: [...mentionSeen] })); }
      catch { /* Keep progress in memory. */ }
      renderMentionNotice();
    }
    if (Number.isFinite(remaining)) mentionTimer = setTimeout(updateMentionViewing, remaining);
  }

  function restoreMentionProgress() {
    // Tab-local viewing progress is separate from Agent cursors and decisions.
    const key = JSON.stringify(["teamx.mentions", state.info.room_instance, state.operator]);
    if (key === mentionKey) return;
    resetMentionViewing();
    mentionKey = key; mentionSeenThrough = 0; mentionSeen = new Set();
    try {
      const value = JSON.parse(storage.getItem(key));
      // Preserve old click-based progress, but never advance that watermark.
      const through = typeof value === "number" ? value : value?.seenThrough;
      if (Number.isSafeInteger(through) && through >= 0) mentionSeenThrough = through;
      if (Array.isArray(value?.seen)) mentionSeen = new Set(value.seen.filter(seq => Number.isSafeInteger(seq) && seq > mentionSeenThrough));
    } catch { /* Reminders still work without browser storage. */ }
  }

  // Each read fixes a snapshot. All continuation pages keep that exact boundary.
  function readPages(params, info) {
    return collectPages((page) => rpc("/v1/read", page, info), params);
  }

  const messageNodes = new Map(), dayNodes = new Map();

  function messageElement(message) {
    const el = $("message-template").content.firstElementChild.cloneNode(true);
    el.id = `message-${message.seq}`;
    const agent = message.author.kind === "agent", integration = message.author.kind === "integration";
    el.classList.toggle("integration", integration);
    const avatar = el.querySelector(".avatar");
    avatar.textContent = integration ? "GL" : message.author.id.slice(0, 2).toUpperCase();
    if (message.author.kind === "human") avatar.replaceChildren(createUserIcon(document));
    if (agent && roomsUI.isDirect()) avatar.replaceChildren(createAssistantIcon(document));
    avatar.classList.toggle("integration-avatar", integration);
    avatar.classList.toggle("agent-avatar", agent);
    avatar.classList.toggle("user-avatar", message.author.kind === "human");
    const channel = message.source?.provider === "gitlab" && ["events", "todos"].includes(message.source?.channel) ? message.source.channel : null;
    el.querySelector(".author").textContent = integration ? channel ? `GitLab · ${channel === "todos" ? "我的待办" : "项目活动"}` : `${message.author.id} · 通知` : message.author.id;
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
        const member = state.members.find(item => item.agent_id === message.author.id);
        if (member) membersUI.openMemberActivity(member, opener);
      };
      author.disabled = false;
      author.setAttribute("aria-label", `查看 ${message.author.id} 的详情`);
      author.setAttribute("aria-controls", "activity-dialog");
      author.addEventListener("click", () => open(author));
      avatar.setAttribute("role", "button"); avatar.setAttribute("tabindex", "0");
      avatar.setAttribute("aria-label", `查看 ${message.author.id} 的详情`);
      avatar.setAttribute("aria-controls", "activity-dialog");
      avatar.addEventListener("click", () => open(avatar));
      avatar.addEventListener("keydown", event => {
        if (event.key === "Enter" || event.key === " ") { event.preventDefault(); open(avatar); }
      });
    }
    el.querySelector(".author-kind").textContent = integration ? "通知" : agent ? "AGENT" : message.author.id === state.operator ? "你 · HUMAN" : "HUMAN";
    const text = el.querySelector(".message-text");
    const ranges = mentionRanges(message.text, operatorNames());
    renderMessageBody(text, message.text, state.operator, messageOptions(message));
    el.classList.toggle("mentioned", message.seq > mentionSeenThrough && !mentionSeen.has(message.seq) && ranges.length > 0 && !(message.author.kind === "human" && message.author.id === state.operator));
    const time = el.querySelector("time");
    time.textContent = settingsUI.formatDate(message.created_at, {hour:"2-digit",minute:"2-digit"});
    time.dateTime = message.created_at;
    time.title = settingsUI.formatDate(message.created_at);
    el.querySelector(".sequence").textContent = `#${message.seq}`;
    el.querySelector(".reply-button").addEventListener("click", () => beginReply(message));
    if (message.reply_to) {
      const ref = el.querySelector(".message-reference");
      const target = state.messages.get(message.reply_to);
      ref.hidden = false;
      ref.textContent = target ? `↳ #${target.seq} ${target.author.id}：${target.text}` : message.reply_to <= state.clearedThrough
        ? `↳ 引用消息 #${message.reply_to} · 已清空` : `↳ 引用消息 #${message.reply_to} · 加载更早消息可查看`;
      ref.addEventListener("click", () => revealMessage(message.reply_to));
    }
    return el;
  }

  function renderMessages() {
    const nodes = [];
    const messages = [...state.messages.values()].sort((a, b) => a.seq - b.seq);
    const memberNamesKey = JSON.stringify([state.members.map(m => m.agent_id), operatorNames()]);
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
      if(!cached){cached={element:messageElement(message),memberNames:memberNamesKey};messageNodes.set(message.seq,cached);}
      const el=cached.element;
      if (cached.memberNames !== memberNamesKey) {
        renderMessageBody(el.querySelector(".message-text"), message.text, state.operator, messageOptions(message));
        cached.memberNames = memberNamesKey;
      }
      const mentioned=mentionRanges(message.text,operatorNames()).length>0;
      el.classList.toggle("mentioned",message.seq>mentionSeenThrough && !mentionSeen.has(message.seq) && mentioned && !(message.author.kind==="human" && message.author.id===state.operator));
      el.querySelector("time").textContent=settingsUI.formatDate(message.created_at,{hour:"2-digit",minute:"2-digit"});
      el.querySelector("time").title=settingsUI.formatDate(message.created_at);
      if(message.reply_to){const target=state.messages.get(message.reply_to);el.querySelector(".message-reference").textContent=target ? `↳ #${target.seq} ${target.author.id}：${target.text}` : message.reply_to<=state.clearedThrough ? `↳ 引用消息 #${message.reply_to} · 已清空` : `↳ 引用消息 #${message.reply_to} · 加载更早消息可查看`;}
      nodes.push(el);
    }
    for(const seq of messageNodes.keys())if(!state.messages.has(seq))messageNodes.delete(seq);
    reconcileChildren($("messages"),nodes);
    interactionsUI.renderPlatformInteractions();
    state.oldest = messages[0]?.seq || 0;
    $("empty").hidden = messages.length !== 0;
    $("load-history").hidden = state.oldest <= state.clearedThrough + 1;
    renderReply(); renderMentionNotice();
  }

  function applyClear(through) {
    if (through <= state.clearedThrough) return false;
    state.clearedThrough = through;
    for (const seq of state.messages.keys()) if (seq <= through) state.messages.delete(seq);
    if (state.draft.replyTo && state.draft.replyTo <= through) clearReply();
    return true;
  }

  function beginReply(message) {
    if(state.draft.pending || !state.connected || discussionBusy()) return;
    state.draft.replyTo=message.seq;
    state.draft.replyAuthor=message.author.id;
    state.draft.replyLabel=message.text;
    renderReply();saveDraft();$("message-input").focus();
  }

  function renderReply() {
    const seq=state.draft.replyTo, message=state.messages.get(seq);
    const author=message?.author.id || state.draft.replyAuthor;
    $("reply-context").hidden=!seq;
    $("reply-author").textContent=author ? `引用 ${author === state.operator ? "你" : author} 的消息` : "引用消息";
    $("reply-sequence").textContent=seq ? `#${seq}` : "";
    $("reply-label").textContent=seq ? (message?.text || state.draft.replyLabel || "原消息未载入").replace(/\s+/g," ").trim() : "";
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

  function renderConversation() {
    const direct = roomsUI.isDirect(), type = roomsUI.conversationType(), name = roomsUI.roomName();
    $("conversation").setAttribute("aria-label", `${type}消息`);
    $("member-rail").setAttribute("aria-label", direct ? "助理详情与会话通知" : "团队成员、通知源与详情");
    $("member-sidebar").setAttribute("aria-label", direct ? "会话通知" : "成员与房间通知");
    for (const id of ["conversation-icon", "empty-conversation-icon"]) {
      if (direct) $(id).replaceChildren(createAssistantIcon(document));
      else $(id).textContent = "#";
    }
    $("policy-empty").textContent = `可以在${direct ? "私信" : "房间"}中告诉 Agent 今后应如何处理。`;
    $("message-input").placeholder = direct ? `给${name || "个人助理"}发消息…` : "发送到房间，开始一段协作…";
    $("message-input-label").textContent = direct ? `给${name || "个人助理"}发消息` : "发送到房间";
    $("rename-room").title = `修改${type}名称`;
    $("rename-form").setAttribute("aria-label", `修改${type}名称`);
    $("rename-room-name").setAttribute("aria-label", `${type}名称`);
    $("room-menu-label").setAttribute("aria-label", `${type}操作`);
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
    $("direct-suggestions").hidden = !direct || !state.info || state.messages.size > 0;
    if (direct && state.info) {
      $("empty").querySelector("h2").textContent = `与${name || "个人助理"}聊聊`;
      $("empty").querySelector("p").textContent = "梳理待办、总结信息、起草内容，也可以连接飞书，关注群消息。";
      const hasLark = workspaceUI.workspaceView?.allPlatforms?.some(item => item.provider === "lark" && item.validation?.status === "valid");
      const suggestions = [
        ["整理待办", "帮我整理一下待办事项"],
        hasLark ? ["总结内容", "帮我总结以下内容：\n"] : ["你能做什么", "介绍一下你能帮我做什么"],
        hasLark ? ["起草回复", "帮我起草一段回复：\n"] : ["连接飞书", "帮我连接飞书"],
      ];
      Array.from($("direct-suggestions").children).forEach((button, index) => {
        const [label, prompt] = suggestions[index];
        button.textContent = label;
        button.dataset.suggestion = prompt;
      });
    }
  }

  function composerState() {
    renderConversation();
    settingsUI.renderSettingsNavigation();
    const namedRoom = state.catalog?.rooms.find(room => room.room_instance === state.info?.room_instance);
    $("rename-room").setAttribute("aria-label", namedRoom ? `${namedRoom.name}，修改${roomsUI.conversationType()}名称` : $("room-name").textContent);
    $("rename-room").disabled = !state.managed || !state.catalogConnected || discussionBusy() || !!state.catalog?.write_error || namedRoom?.status !== "ready" || roomsUI.renameSending;
    $("save-room-name").disabled = $("rename-room").disabled || !roomsUI.renameTarget;
    $("rename-room-name").disabled = roomsUI.renameSending;
    $("save-room-name").textContent = roomsUI.renameSending ? "保存中…" : "保存";
    membersUI.renderExecutionHint();
    $("message-input").disabled = !state.info;
    $("message-input").readOnly = !!state.draft.pending || discussionBusy();
    mentionPicker.refresh();
    $("send").disabled = !state.connected || state.draft.sending || discussionBusy() || (!state.draft.pending && !$("message-input").value.trim());
    $("send").textContent = state.draft.sending ? "发送中…" : state.draft.pending ? "重试发送 ↗" : "发送 ↗";
    $("cancel-reply").disabled=!!state.draft.pending;
    $("discard-send").hidden = !state.draft.pending || state.draft.sending;
    $("restart-discussion").disabled = !state.connected || workspaceUI.workspaceBusy() || roomsUI.managementBusy() || state.draft.restartStarting || state.restart?.status === "running" || !!state.draft.pending || state.draft.sending;
    $("restart-discussion").textContent = state.draft.restartStarting || state.restart?.status === "running" ? "重新开始中…" : state.draft.restartRequest ? "核对并重试" : "重新开始";
    $("toggle-add").disabled = !state.connected || discussionBusy();
    $("add-member").disabled = !state.connected || !state.profiles.length || discussionBusy();
    renderRestart(); workspaceUI.renderWorkspace();
  }

  function clearReply() {
    state.draft.replyTo = 0;
    state.draft.replyAuthor = "";
    state.draft.replyLabel = "";
    renderReply();
    saveDraft();
  }

  function resetRestartNotice() { restartNoticeID = ""; restartNoticeExpires = 0; }

  function resetMessageNodes() { messageNodes.clear(); dayNodes.clear(); }

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
      layoutUI.showConversationNotifications(); $("room-menu").open = false;
      $("activity-dialog").hidden = true;
      layoutUI.expandMembers();
      layoutUI.renderPanels(); notificationsUI.renderNotificationPlatforms(); $("close-conversation-notifications").focus();
    });

    $("close-conversation-notifications").addEventListener("click", () => {
      layoutUI.hideConversationNotifications(); $("activity-dialog").hidden = false;
      layoutUI.renderPanels(); membersUI.syncDirectDetail(); $("agent-tab-platforms").focus();
    });

    $("view-mention").addEventListener("click", () => {
      const message = unreadMentions()[0];
      if (!message) return;
      const target = $(`message-${message.seq}`);
      target.scrollIntoView({ block: "center" });
      target.tabIndex = -1;
      target.focus({ preventScroll: true });
      target.classList.remove("flash");
      requestAnimationFrame(() => target.classList.add("flash"));
      scheduleMentionViewing();
    });

    $("restart-discussion").addEventListener("click", async () => {
      if ($("restart-discussion").disabled) return;
      $("room-menu").open = false;
      $("room-menu-label").focus();
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
      draft.pending ||= { text, client_message_id: randomUUID(), reply_to: draft.replyTo, info, operator };
      saveDraft(); draft.sending = true;
      composerState(); notice("send-error", "");
      try {
        const pending = draft.pending;
        const receipt = await rpc("/v1/send", { text: pending.text, client_message_id: pending.client_message_id,
          reply_to: pending.reply_to, caller: { kind: "human", id: pending.operator } }, pending.info);
        draft.pending = null; draft.text = ""; draft.replyTo = 0; draft.replyLabel = ""; draft.replyAuthor = "";
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

    $("jump-latest").addEventListener("click", bottom);

    $("timeline").addEventListener("scroll", () => {
      if (nearBottom()) $("jump-latest").hidden = true;
      scheduleMentionViewing();
    });

    $("timeline").addEventListener("load", scheduleMentionViewing, true);

    window.addEventListener("resize", scheduleMentionViewing);

    window.addEventListener("scroll", scheduleMentionViewing, { passive: true });

    window.addEventListener("focus", updateMentionViewing);

    window.addEventListener("blur", resetMentionViewing);

    if (typeof ResizeObserver !== "undefined") new ResizeObserver(scheduleMentionViewing).observe($("timeline"));

    $("load-history").addEventListener("click", async () => {
      if (historyLoading || !state.info || state.oldest <= state.clearedThrough + 1) return;
      historyLoading = true;
      $("load-history").disabled = true;
      $("load-history").textContent = "加载中…";
      const info = state.info, version = runtime.viewVersion;
      const oldest = state.oldest;
      try {
        const read = await readPages({ mode: "range", after: Math.max(0, oldest - 51), snapshot_seq: oldest - 1 }, info);
        if (!currentView(version)) return;
        if (read.clearedThrough < state.clearedThrough) return;
        applyClear(read.clearedThrough);
        const el = $("timeline"), height = el.scrollHeight, top = el.scrollTop;
        for (const message of read.messages) state.messages.set(message.seq, message);
        renderMessages();
        el.scrollTop = top + el.scrollHeight - height;
      } catch (error) { if (currentView(version)) notice("sync-error", `历史加载失败，可重试。${error.message}`); }
      finally { historyLoading = false; $("load-history").disabled = false; $("load-history").textContent = "加载更早消息"; }
    });
  }

  return {
    mount,
    resetRestartNotice,
    resetMessageNodes,
    interactionHost,
    renderMessages,
    composerState,
    beginReply,
    nearBottom,
    saveDraft,
    resetMentionViewing,
    restoreMentionProgress,
    drafts,
    showDraft,
    readPages,
    applyClear,
    bottom,
    renderMentionNotice,
    updateMentionViewing,
    scheduleMentionViewing
  };
}
