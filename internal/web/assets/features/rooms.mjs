import { randomUUID } from "../random-id.mjs";
import { agentName } from "../agent-names.mjs";
import { renderAvatar } from "../avatars.mjs";
import { compactViewport, mentionRanges } from "../client.mjs";

// Room catalog, selection, creation, deletion and default assistant recovery.
export function createRooms({ env, runtime, layoutUI, workspaceUI, conversationUI, settingsUI, membersUI, userMenuUI }) {
  const { document, window, Option, request, setTimeout, clearTimeout } = env;
  const { state, $, notice, currentView, refresh, discussionBusy, dismissOnBackdrop, storage } = runtime;

  let defaultAssistantRetrying = false, defaultAssistantError = "";
  let deletion = null;
  let readingScope = "";
  const readingSummaries = new Map();

  function acceptCatalog(catalog) {
    const scope = JSON.stringify([state.installationID, catalog.operator, catalog.service_epoch]);
    if (scope !== readingScope) { readingScope = scope; readingSummaries.clear(); }
    const keys = new Set();
    for (const room of catalog.rooms) {
      const key = JSON.stringify([room.room_id, room.room_instance]); keys.add(key);
      const previous = readingSummaries.get(key), next = room.human_read;
      const current = catalog.operator === state.operator && room.room_id === state.info?.room_id && room.room_instance === state.info?.room_instance;
      const boundary = current ? conversationUI.humanReadSummary?.() : null;
      const valid = next?.status === "ready" &&
        ["revision", "snapshot_seq", "cleared_through", "unread_count", "mention_count"].every(field => Number.isSafeInteger(next[field]) && next[field] >= 0) &&
        next.cleared_through <= next.snapshot_seq && next.mention_count <= next.unread_count &&
        next.revision >= Math.max(previous?.revision || 0, boundary?.revision || 0) &&
        next.snapshot_seq >= Math.max(previous?.snapshot_seq || 0, boundary?.snapshot_seq || 0, current ? state.lastSeq : 0) &&
        next.cleared_through >= Math.max(previous?.cleared_through || 0, boundary?.cleared_through || 0, current ? state.clearedThrough : 0);
      if (valid) readingSummaries.set(key, { ...next });
      else if (next || previous) room.human_read = { ...previous, status: "unavailable" };
    }
    for (const key of readingSummaries.keys()) if (!keys.has(key)) readingSummaries.delete(key);
  }

  const managementKey = "teamx.room-management";

  const selectionKey = "teamx.selected-room";

  let selectedRoom = null, selectionVersion = 0, selectionLoaded = false, awaitDefaultSelection = false;
  let revealedRoom = "", suppressedRoomClick = "", roomPress = null;
  const roomKey = room => JSON.stringify([room.room_id, room.room_instance]);

  function cancelRoomPress() {
    if (roomPress) clearTimeout(roomPress.timer);
    roomPress = null;
  }

  function closeRoomActions() {
    cancelRoomPress(); revealedRoom = "";
    for (const row of $("room-list").children) row.classList.remove("delete-revealed");
  }

  function showRoomActions(key) {
    const row = Array.from($("room-list").children).find(row => row.dataset.roomKey === key);
    if (!row?.children[1] || row.children[1].disabled || document.hidden || state.suspended) return false;
    revealedRoom = key;
    for (const item of $("room-list").children) item.classList.toggle("delete-revealed", item === row);
    return true;
  }

  const pressActions = event => event.pointerType === "touch" || event.pointerType === "pen" || window.innerWidth <= 640 ||
    window.matchMedia?.("(hover: none), (pointer: coarse)").matches;

  function bindRoomActions(button, remove, key) {
    button.setAttribute("aria-keyshortcuts", "Shift+F10");
    button.addEventListener("pointerdown", event => {
      cancelRoomPress();
      if (!pressActions(event) || event.isPrimary === false || event.button !== 0 || remove.disabled) return;
      const press = {key, pointerId:event.pointerId, x:event.clientX, y:event.clientY};
      press.timer = setTimeout(() => {
        if (roomPress === press && showRoomActions(key)) suppressedRoomClick = key;
      }, 500);
      roomPress = press;
    });
    button.addEventListener("contextmenu", event => {
      if (!pressActions(event)) return;
      event.preventDefault(); cancelRoomPress();
      if (showRoomActions(key)) suppressedRoomClick = key;
    });
    button.addEventListener("keydown", event => {
      if (event.key !== "ContextMenu" && !(event.key === "F10" && event.shiftKey)) return;
      event.preventDefault(); suppressedRoomClick = "";
      if (showRoomActions(key)) remove.focus();
    });
  }

  let conversationFilter = "all";
  const filterLabels = {all:"全部", direct:"私信", room:"房间"};
  const matchesFilter = room => conversationFilter === "all" || !!room.is_direct === (conversationFilter === "direct");

  function renderConversationFilter() {
    const mobile = compactViewport(window);
    let unread = 0, filteredUnread = 0;
    for (const room of state.catalog?.rooms || []) {
      const count = room.human_read?.unread_count || 0;
      unread += count;
      if (matchesFilter(room)) filteredUnread += count;
    }
    const badge = count => count > 99 ? "99+" : count > 0 ? String(count) : "";
    $("conversation-unread-total").textContent = badge(filteredUnread);
    $("conversation-unread-total").hidden = mobile || filteredUnread === 0;
    $("mobile-messages-unread").textContent = badge(unread);
    $("mobile-messages-unread").hidden = unread === 0;
    $("mobile-tab-messages").setAttribute("aria-label", `消息，${unread} 条未读消息`);
    $("conversation-filter-label").textContent = !mobile && conversationFilter === "all" ? "消息" : filterLabels[conversationFilter];
    const label = `筛选消息：${filterLabels[conversationFilter]}` + (mobile ? "" : `，${filteredUnread} 条未读消息`);
    $("conversation-filter").setAttribute("aria-label", label);
    $("conversation-filter").setAttribute("title", label);
    $("conversation-filter").dataset.filtered = String(conversationFilter !== "all");
    for (const filter of Object.keys(filterLabels)) {
      $("conversation-filter-" + filter).setAttribute("aria-checked", String(conversationFilter === filter));
      $("conversation-filter-" + filter).setAttribute("aria-label", filterLabels[filter]);
    }
  }

  function closeFilterMenu() {
    $("conversation-filter-menu").hidden = true;
    $("conversation-filter").setAttribute("aria-expanded", "false");
    runtime.navigationChanged();
  }

  function setConversationFilter(filter) {
    closeRoomActions();
    conversationFilter = filter;
    closeFilterMenu();
    $("room-nav").scrollTop = 0;
    renderRooms();
    $("conversation-filter").focus();
  }

  function selectedEntry(catalog = state.catalog) {
    return catalog?.rooms.find(room => room.room_id === selectedRoom?.room_id && room.room_instance === selectedRoom?.room_instance);
  }

  function isDirect() { return state.managed ? selectedEntry()?.is_direct === true : state.isDirect; }

  function conversationType(room = selectedEntry()) { return room?.is_direct || !state.managed && state.isDirect ? "私信" : "房间"; }

  function isDefaultAssistant(room = selectedEntry(), catalog = state.catalog) {
    const ref = catalog?.default_assistant;
    return !!room && ref?.status !== "deleted" && room.room_id === ref?.room_id && room.room_instance === ref?.room_instance;
  }

  function deletionOperation(room) {
    return state.management?.kind === "delete" && roomKey(state.management.payload) === roomKey(room) ? state.management : null;
  }

  const hasDeletion = room => room.status === "deleting" || !!deletionOperation(room);

  function selectRoom(room) {
    const entry = room && (state.catalog?.rooms.find(entry => entry.room_id === room.room_id && entry.room_instance === room.room_instance) || room);
    if (entry && !matchesFilter(entry)) conversationFilter = "all";
    selectedRoom = room ? { room_id: room.room_id, room_instance: room.room_instance } : null;
    selectionVersion++; awaitDefaultSelection = false;
    layoutUI.resetDirectNavigation();
    try {
      if (selectedRoom) storage?.setItem(selectionKey, JSON.stringify(selectedRoom));
      else storage?.removeItem(selectionKey);
    } catch { /* Keep this tab's selection in memory. */ }
    runtime.selectView(room?.service || null, state.catalog?.operator || state.operator);
    workspaceUI.selectWorkspace(room, state.catalog?.operator || state.operator);
  }

  function selectedTeam() {
    return state.teams.find(team => team.id === $("team").value);
  }

  function managementBusy() {
    const op = state.management;
    return !!(state.managed && (state.catalog?.busy || selectedEntry()?.busy ||
      op && op.kind !== "open" && op.payload.room_instance === selectedRoom?.room_instance));
  }

  function roomName() {
    return state.catalog?.rooms.find(room => room.room_instance === state.info?.room_instance)?.name || state.info?.room_id || "会话";
  }

  function renderTeams(teams) {
    const signature = JSON.stringify(teams);
    if ($("create-form").dataset.signature !== signature) {
      $("create-form").dataset.signature = signature;
      state.teams = teams;
      const selected = $("team").value;
      $("team").replaceChildren(...teams.map(team => new Option(team.name, team.id)));
      if (teams.some(team => team.id === selected)) $("team").value = selected;
    }
    renderCreateSelection();
  }

  let createDirect = false;

  function renderCreateSelection() {
    const team = selectedTeam();
    $("create-title").textContent = createDirect ? "新建私信" : "新建房间";
    $("close-create").setAttribute("aria-label", createDirect ? "关闭新建私信" : "关闭新建房间");
    $("create-team-fields").hidden = createDirect;
    $("create-direct-description").hidden = !createDirect;
    $("team").required = !createDirect;
    $("create-name-label").textContent = createDirect ? "私信名称" : "房间名称";
    $("new-room-name").placeholder = createDirect ? "个人助理" : "默认使用团队名";
    $("team-description").textContent = team?.description || "暂无可用团队。请在团队目录添加 team.json，并重启服务。";
    $("team-members").replaceChildren(...(team?.members || []).map(member => {
      const item = document.createElement("li"); item.textContent = `${member.name} · ${member.id}`; return item;
    }));
    const disabled = !state.catalogConnected || discussionBusy() || !!state.management || state.managementSending;
    $("team").disabled = createDirect || disabled || !state.teams.length;
    $("new-room-name").disabled = disabled;
    $("create-room").disabled = disabled || (createDirect ? !state.catalog?.agent_roles?.some(role => role.id === "assistant") : !team);
    $("create-room").textContent = state.managementSending ? "正在创建…" : "创建并进入";
    $("team-hint").textContent = createDirect ? "创建后，在助理详情中启用即可。" : "每个房间独立运行。开场消息会填入草稿，由你发送。";
  }

  function saveManagement() {
    try {
      if (state.management) storage?.setItem(managementKey, JSON.stringify(state.management));
      else storage?.removeItem(managementKey);
    } catch { /* Retain deduplication IDs in memory. */ }
  }

  function seedCreated(operation, room) {
    conversationUI.drafts.seed(room, operation.operator, operation.opening || "");
    operation.room = room;
    saveManagement();
    if (state.info?.room_instance === room.room_instance) conversationUI.showDraft();
  }

  async function reconcileManagement(catalog, { current = () => true } = {}) {
    // Other tabs may delete a Room too. Clear this tab's saved drafts only after
    // the catalog confirms removal, and invalidate any old private-output read.
    for (const room of state.knownRooms || []) {
      if (!catalog.rooms.some(current => current.room_instance === room.room_instance)) forgetRoom(room);
    }
    state.knownRooms = catalog.rooms;
    const op = state.management;
    if (!op) return;
    if (op.kind === "delete") {
      const room = catalog.rooms.find(room => room.room_instance === op.payload.room_instance);
      if (room && isDefaultAssistant(room, catalog) && room.status !== "deleting") {
        state.management = null; state.managementError = "默认个人助理不可删除，可暂停或重新开始。"; saveManagement();
      } else if (!room) {
        forgetRoom(op.payload);
        if (deletion && roomKey(deletion.room) === roomKey(op.payload)) closeDeletion();
        state.management = null; state.managementError = ""; saveManagement();
      } else if (!room.busy && room.status === "deleting") {
        state.managementError = "";
      }
    } else if (op.kind === "create") {
      const room = catalog.rooms.find(room => room.create_id === op.payload.create_id);
      if (room?.status === "ready") seedCreated(op, room);
    } else if (op.kind === "open") {
      const query = new URLSearchParams({ service_epoch: catalog.service_epoch, caller_id: op.operator, open_id: op.payload.open_id });
      try {
        const receipt = await request(`/v1/rooms/open?${query}`);
        if (current()) finishOpen(op, receipt);
      } catch (error) {
        if (current() && state.management === op) state.managementError = `打开结果尚未确认，可查询或用原请求重试。${error.message}`;
      }
    }
  }

  function finishOpen(op, receipt) {
    if (state.management !== op || receipt.status === "running") return;
    state.managementError = receipt.status === "failed" ? `打开失败，历史已保留。${receipt.error}` : "";
    if (receipt.status === "completed" && receipt.service && op.selectOnComplete !== false && op.selectionVersion === selectionVersion) {
      selectRoom({ ...op.room, ...receipt, service: receipt.service });
    } else if (receipt.status === "failed" && /^directory_/.test(receipt.error || "") && op.selectOnComplete !== false && op.selectionVersion === selectionVersion) {
      // Directory repair uses catalog identity even when no member service can
      // open. Keep the failed Room selected so its Workspace controls target it.
      selectRoom({ ...receipt, service: null });
    }
    state.management = null; saveManagement();
  }

  function forgetRoom(room) {
    if (selectedRoom?.room_instance === room.room_instance) selectRoom(null);
    if (state.info?.room_instance === room.room_instance) runtime.selectView(null, state.operator);
    runtime.forgetRoomView(room);
    conversationUI.drafts.remove(room, state.operator);
    try { storage?.removeItem(workspaceUI.workspaceKey(room, state.operator)); } catch { /* Storage is optional. */ }
    try { storage?.removeItem(JSON.stringify(["teamx.mentions", room.room_instance, state.operator])); } catch { /* Storage is optional. */ }
  }

  function roomListTime(room) {
    return room.last_message_at || room.created_at;
  }

  function roomListPreview(room) {
    const preview = room.last_message_preview;
    if (!preview) return room.last_message_at ? "已有消息" : room.is_direct ? "私信已创建" : "房间已创建";
    const author = preview.author;
    const sender = author?.kind === "human" && author.id === state.catalog.operator ? "你"
      : author?.kind === "agent" ? room.is_direct ? "" : preview.agent_name || agentName(room.members?.find(member => member.id === author.id)) || author.id
      : author?.id || "";
    let text = preview.text || "";
    // Catalog previews are plain text; resolve the same operator aliases as the
    // conversation body without changing stored messages or routing identities.
    for (const at of mentionRanges(text, [state.catalog.operator, state.operatorDisplayName]).reverse()) {
      text = text.slice(0, at.start) + "@" + userMenuUI.displayName() + text.slice(at.end);
    }
    text = text.replace(/\s+/g, " ").trim() || "[消息]";
    return sender ? `${sender}：${text}` : text;
  }

  function roomListAvatar(room) {
    const wrapper = document.createElement("span"); wrapper.className = "room-avatar";
    const image = document.createElement("span"); image.className = "room-avatar-image";
    image.setAttribute("aria-hidden", "true");
    renderAvatar(document, image, {kind:"agent", id:room.is_direct ? room.members?.[0]?.id || room.room_id : room.room_id,
      name:room.name, direct:!!room.is_direct, defaultAssistant:isDefaultAssistant(room)});
    if (!room.is_direct) {
      const label = document.createElement("span"); label.className = "room-avatar-name"; label.textContent = room.name;
      image.replaceChildren(label);
    }
    wrapper.append(image);
    return wrapper;
  }

  function renderRooms() {
    document.querySelector(".workspace").classList.toggle("managed", state.managed);
    layoutUI.renderPanels();
    $("open-general-settings").hidden = state.managed;
    $("open-general-settings").disabled = !state.connected;
    $("open-settings").disabled = state.managed ? !state.catalogConnected : !state.connected;
    $("toggle-add").hidden = state.managed || isDirect();
    if (state.managed) $("add-form").hidden = true;
    if (!state.managed || !state.catalog) return;
    const catalog = state.catalog;
    renderDefaultAssistant();
    const visibleRooms = catalog.rooms.filter(matchesFilter);
    $("rooms-empty").hidden = visibleRooms.length > 0;
    $("rooms-empty").textContent = !catalog.rooms.length ? "还没有会话。点击右上角 ＋，新建私信或房间。"
      : `暂无${filterLabels[conversationFilter]}。点击右上角 ＋ 新建，或切换筛选查看其他会话。`;
    $("new-room").disabled = !state.catalogConnected || discussionBusy() || !!state.management || state.managementSending || !!catalog.write_error;
    const now = Date.now();
    const signature = JSON.stringify({ rooms: catalog.rooms, selected: selectedRoom, timeZone:settingsUI.systemTime.zone, timeError:settingsUI.systemTime.error, day:settingsUI.systemTime.day(now),
      operator:catalog.operator, operatorDisplayName:state.operatorDisplayName, displayName:userMenuUI.displayName(),
      defaultAssistant: catalog.default_assistant, filter:conversationFilter, busy: discussionBusy(), management: !!state.management, sending: state.managementSending, connected: state.catalogConnected });
    if ($("room-list").dataset.signature !== signature) {
      $("room-list").dataset.signature = signature;
      const rows = visibleRooms.sort((a,b) => Number(isDefaultAssistant(b)) - Number(isDefaultAssistant(a)) || new Date(roomListTime(b)) - new Date(roomListTime(a))).map(room => {
        const row = document.createElement("div"); row.className = "room-entry"; row.dataset.direct = String(!!room.is_direct);
        const key = roomKey(room); row.dataset.roomKey = key;
        row.classList.toggle("delete-revealed", key === revealedRoom);
        const button = document.createElement("button"); button.type = "button"; button.className = "room-choice";
        button.setAttribute("aria-current", String(room.room_instance === selectedRoom?.room_instance));
        const name = document.createElement("strong"), updated = document.createElement("time");
        name.textContent = room.name;
        const avatar = roomListAvatar(room);
        row.dataset.defaultAssistant = String(isDefaultAssistant(room));
        const statusText = hasDeletion(room) ? (room.busy || state.managementSending ? "正在删除…" : room.status === "deleting" ? "删除未完成" : "删除结果待确认")
          : room.status === "creating" ? `创建待完成${room.error ? ` · ${room.error}` : " · 可重试原创建请求"}` : room.runtime_status === "opening" ? "正在打开…"
          : room.runtime_error ? `运行受阻 · ${room.runtime_error}`
          : room.busy ? "会话操作进行中…"
          : room.summary_status === "unavailable" ? "会话摘要暂不可用" : "";
        updated.className = "room-time";
        updated.textContent = settingsUI.systemTime.recent(roomListTime(room), now);
        updated.dateTime = roomListTime(room);
        updated.title = `${room.last_message_at ? "最后消息" : "创建于"}：${settingsUI.formatDate(roomListTime(room), {year:"numeric",month:"long",day:"numeric",hour:"2-digit",minute:"2-digit",second:"2-digit"})}`;
        name.title = room.name;
        const detailText = statusText || roomListPreview(room);
        button.title = [room.name, updated.title, detailText].join("\n");
        button.setAttribute("aria-label", `${conversationType(room)}：${room.name}${isDefaultAssistant(room) ? "，默认助理" : ""}，${updated.title}，${detailText}`);
        button.append(name, updated);
        const detail = document.createElement("span"); detail.className = "room-detail"; detail.title = detailText;
        const preview = document.createElement("span"); preview.className = statusText ? "room-status" : "room-preview"; preview.textContent = detailText;
        button.append(detail, avatar);
        const reading = room.human_read, labels = [];
        if (reading) {
          if (reading.unread_count > 0) {
            const count = document.createElement("span"); count.className = "room-unread";
            count.textContent = reading.unread_count > 99 ? "99+" : String(reading.unread_count);
            count.setAttribute("aria-label", `${reading.unread_count} 条未读消息`);
            avatar.append(count); labels.push(`${reading.unread_count} 条未读消息`);
          }
          if (!room.is_direct && reading.mention_count > 0) {
            const mention = document.createElement("span"); mention.className = "room-mention"; mention.textContent = "[有人@我]";
            mention.setAttribute("aria-label", `${reading.mention_count} 条未读消息提及你`);
            detail.append(mention); labels.push(`${reading.mention_count} 条未读消息提及你`);
          }
          if (reading.status === "unavailable") {
            const sync = document.createElement("span"); sync.className = "room-read-unavailable"; sync.textContent = "暂未同步";
            sync.title = "阅读状态暂未同步，保留上次计数"; detail.append(sync); labels.push(sync.title);
          }
        }
        detail.append(preview);
        if (labels.length) {
          button.title += "\n" + labels.join("，");
          button.setAttribute("aria-label", button.getAttribute("aria-label") + "，" + labels.join("，"));
        }
        button.disabled = !state.catalogConnected || !hasDeletion(room) && (room.status !== "ready" || !!catalog.busy);
        if (hasDeletion(room)) button.setAttribute("aria-haspopup", "dialog");
        button.addEventListener("click", event => {
          if (suppressedRoomClick === key) { event.preventDefault(); suppressedRoomClick = ""; return; }
          closeRoomActions();
          if (hasDeletion(room)) startDeletion(room);
          else startSelection(room);
        });
        row.append(button);
        if (isDefaultAssistant(room) || hasDeletion(room)) return row;
        const remove = document.createElement("button"); remove.type = "button"; remove.className = "room-delete";
        const icon = document.createElementNS("http://www.w3.org/2000/svg", "svg");
        icon.setAttribute("viewBox", "0 0 24 24"); icon.setAttribute("aria-hidden", "true");
        const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
        path.setAttribute("d", "M3 6h18M9 6V4h6v2M5 6l1 14h12l1-14M10 10v6m4-6v6");
        icon.append(path); remove.append(icon);
        remove.title = `${room.status === "deleting" ? "重试删除" : "删除"}${conversationType(room)}「${room.name}」`;
        remove.setAttribute("aria-label", remove.title);
        remove.disabled = !state.catalogConnected || !!catalog.busy || !!room.busy || !!state.management || !!catalog.write_error;
        remove.addEventListener("click", () => { closeRoomActions(); startDeletion(room); });
        bindRoomActions(button, remove, key);
        row.append(remove);
        return row;
      });
      $("room-list").replaceChildren(...rows);
      if (revealedRoom && !rows.some(row => row.dataset.roomKey === revealedRoom && row.children.length > 1)) closeRoomActions();
    }
    const operation = state.management;
    const deleting = operation?.kind === "delete";
    const pending = catalog.busy || state.managementSending;
    let text = state.managementError || catalog.write_error || "";
    if (workspaceUI.workspaceView?.data?.operation?.status === "running") text = `工作目录 · ${workspaceUI.workspaceStages[workspaceUI.workspaceView.data.operation.phase] || "正在应用"}`;
    else if (catalog.busy) text = "共享资源操作进行中…";
    else if (state.managementSending) text = "正在提交会话操作…";
    else if (operation && !text) text = operation.room && operation.kind === "create"
      ? "会话已创建，点击进入。" : "操作结果尚未确认，请核对列表后重试。";
    if (deleting) text = "";
    notice("management-notice", text);
    $("management-feedback").hidden = deleting || !text && !operation;
    $("retry-management").hidden = !operation || deleting || !!pending;
    $("retry-management").disabled = !state.catalogConnected;
    $("retry-management").textContent = operation?.kind === "create" && operation.room ? `进入已创建${conversationType(operation.room)}` : operation?.kind === "delete" ? "重试删除" : "重试";
    $("dismiss-management").hidden = !operation || deleting || !!pending;
    renderDeletion();
  }

  async function runManagement() {
    const op = state.management;
    if (!op || state.managementSending || !state.catalogConnected || state.catalog.busy) return;
    if (op.kind === "delete" && state.catalog.rooms.find(room => roomKey(room) === roomKey(op.payload))?.busy) return;
    state.managementSending = true; state.managementError = "";
    renderRooms(); conversationUI.composerState(); membersUI.renderMembers(); renderCreateSelection();
    try {
      const manage = (path, payload) => request(path, { service_epoch: state.catalog.service_epoch,
        caller: { kind: "human", id: op.operator }, ...payload });
      if (op.kind === "create") {
        const room = op.room || await manage("/v1/rooms/create", op.payload);
        if (room.status !== "ready") throw new Error("会话创建尚未完成，请核查后重试。");
        seedCreated(op, room);
        op.kind = "open"; op.selectionVersion ??= selectionVersion; op.payload = { open_id: randomUUID(), room_id: room.room_id, room_instance: room.room_instance };
        saveManagement();
      }
      const result = await manage(op.kind === "delete" ? "/v1/rooms/delete" : "/v1/rooms/open", op.payload);
      if (op.kind === "open") finishOpen(op, result);
      $("create-dialog").close();
    } catch (error) {
      if (state.management === op) {
        state.managementError = op.kind === "delete" ? "" : `操作未确认，会话和历史已保留。${error.message}`;
        if (op.kind === "delete") {
          op.error = error.message; saveManagement();
          if (deletion && roomKey(deletion.room) === roomKey(op.payload)) {
            deletion.error = error.message;
            deletion.rejected = ["request", "identity", "conflict", "capacity"].includes(error.code);
          }
        }
        // A definitive rejection has no execution to recover. Other failures keep
        // the original ID, including a create interrupted after index publication.
        if (["request", "identity", "conflict", "capacity"].includes(error.code)) {
          state.management = null; saveManagement();
        }
      }
      if (op.kind !== "delete") notice("create-error", state.managementError);
    } finally {
      state.managementSending = false;
      await refresh(); renderRooms(); conversationUI.composerState(); renderCreateSelection();
    }
  }

  function startSelection(room) {
    if (!state.catalogConnected || state.catalog.busy) return;
    if (room.service && room.runtime_status === "ready") {
      if (state.management && state.management.kind !== "delete") {
        state.management.selectOnComplete = false; saveManagement();
      }
      selectRoom(room); runtime.enterRoom(); return;
    }
    if (state.management || state.managementSending || room.busy) return;
    if (!room.service && /^directory_/.test(room.runtime_error || "")) {
      selectRoom(room); runtime.enterRoom(); void refresh(); return;
    }
    selectionVersion++; awaitDefaultSelection = false;
    state.management = { kind: "open", selectOnComplete: true, selectionVersion, operator: state.operator, payload: {
      open_id: randomUUID(), room_id: room.room_id, room_instance: room.room_instance } };
    saveManagement(); void runManagement();
  }

  function closeDeletion() {
    const target = deletion?.room;
    if (deletion?.submitted && state.management?.kind === "delete" && roomKey(deletion.room) === roomKey(state.management.payload)) {
      state.management.background = true; saveManagement();
    }
    deletion = null;
    $("delete-dialog").close();
    const row = target && state.catalog?.rooms.some(room => roomKey(room) === roomKey(target))
      ? Array.from($("room-list").children).find(row => row.dataset.roomKey === roomKey(target)) : null;
    (row?.children[0] || $("room-nav")).focus({preventScroll:true});
  }

  function renderDeletion() {
    const op = state.management;
    if (!deletion && op?.kind === "delete" && !op.background && op.operator === state.operator) {
      const room = state.catalog.rooms.find(room => roomKey(room) === roomKey(op.payload));
      if (room) startDeletion(room);
    }
    if (!deletion) return;
    const room = state.catalog.rooms.find(room => roomKey(room) === roomKey(deletion.room));
    if (deletion.submitted && !room && state.catalogConnected) { closeDeletion(); return; }
    const pending = deletion.submitted && (state.managementSending || room?.busy);
    const confirm = !deletion.submitted;
    const stale = deletion.operator !== state.operator || !room || confirm && deletion.epoch !== state.catalog.service_epoch;
    const error = deletion.error || room?.error || "";
    const failed = deletion.rejected || room?.status === "deleting" && !!room.error;
    $("delete-dialog").dataset.phase = confirm ? "confirm" : pending ? "running" : failed ? "failed" : "unknown";
    $("delete-title").textContent = `${pending ? "正在删除" : "删除"}${conversationType(deletion.room)}「${deletion.room.name}」${confirm ? "？" : ""}`;
    $("delete-message").textContent = stale ? "会话或服务状态已变化，请关闭弹窗后重新选择会话。"
      : confirm ? "将停止该会话并删除消息、所有 Agent 私有会话（含历史会话）、诊断和工作区文件。此操作无法撤销。"
      : pending ? "正在停止 Agent、等待会话结束并删除数据…"
      : failed ? "删除未完成，修复问题后可重试。" : "删除结果尚未确认，请核对会话状态后重试。";
    $("delete-background-hint").hidden = !pending;
    $("delete-details").hidden = !error || confirm || !!pending;
    $("delete-error-detail").textContent = error;
    if (!error || confirm || pending) $("delete-details").open = false;
    $("delete-dismiss").textContent = confirm ? "取消" : pending ? "转到后台" : "稍后处理";
    $("delete-submit").hidden = !!pending;
    $("delete-submit").disabled = stale || !state.catalogConnected || !!state.catalog.busy || !!state.catalog.write_error ||
      !!state.management && (state.management.kind !== "delete" || roomKey(state.management.payload) !== roomKey(deletion.room));
    $("delete-submit").textContent = confirm ? "永久删除" : failed ? "重试删除" : "核对并重试";
  }

  function startDeletion(room) {
    if ($("delete-dialog").open) return;
    room = state.catalog?.rooms.find(current => roomKey(current) === roomKey(room));
    if (!room) return;
    if (isDefaultAssistant(room) && room.status !== "deleting") return;
    const op = deletionOperation(room);
    if (!state.catalogConnected || !hasDeletion(room) && (state.management || room.busy || state.catalog.busy || state.catalog.write_error)) return;
    deletion = { room, operator:state.operator, epoch:state.catalog.service_epoch, submitted:room.status === "deleting" || !!op, error:op?.error || "" };
    if (op) { op.background = false; saveManagement(); }
    renderDeletion(); $("delete-dialog").showModal(); $("delete-dismiss").focus();
  }

  function submitDeletion() {
    if (!deletion || $("delete-submit").disabled || $("delete-submit").hidden) return;
    const current = state.catalog?.rooms.find(room => roomKey(room) === roomKey(deletion.room));
    if (!current || deletion.operator !== state.operator || !deletion.submitted && deletion.epoch !== state.catalog.service_epoch ||
      !state.catalogConnected || current.busy || state.managementSending || state.catalog.busy || state.catalog.write_error ||
      isDefaultAssistant(current) && current.status !== "deleting") return;
    if (state.management && (state.management.kind !== "delete" || roomKey(state.management.payload) !== roomKey(current))) return;
    state.management = { kind:"delete", operator:state.operator, payload:{room_id:current.room_id, room_instance:current.room_instance} };
    deletion.submitted = true; deletion.error = ""; deletion.rejected = false;
    saveManagement(); void runManagement();
    $("delete-dismiss").focus();
  }

  function renderDefaultAssistant() {
    const record = state.catalog?.default_assistant, pending = record?.status === "pending", deleted = record?.status === "deleted";
    const text = pending ? record.error_code === "configuration_required"
      ? `个人助理初始化受阻：${record.error}。请运行 ${record.next_step || "teamx start init"} 完成配置并重启服务，再核对原初始化操作。`
      : record.error ? `个人助理初始化失败：${record.error}` : "个人助理正在初始化…"
      : deleted ? "默认助理此前已删除。恢复会创建新的独立会话，原消息、规则和订阅不会恢复。"
      : record?.status === "deleting" ? "默认助理的历史删除操作尚未完成，请在列表重试结清后再恢复。" : "";
    notice("default-assistant-notice", [text, pending || deleted ? defaultAssistantError : ""].filter(Boolean).join(" "));
    $("retry-default-assistant").hidden = !pending && !deleted;
    $("retry-default-assistant").disabled = defaultAssistantRetrying || !state.catalogConnected || !!state.catalog?.busy || !!state.catalog?.write_error;
    $("retry-default-assistant").textContent = deleted
      ? defaultAssistantRetrying ? "正在恢复助理…" : "恢复默认助理（新建会话）"
      : defaultAssistantRetrying ? "正在核对初始化…" : "核对并重试助理初始化";
  }

  let renameTarget = null, renameSending = false;

  function closeRename(focus = true) {
    renameTarget = null; $("rename-form").hidden = true; $("room-name-row").hidden = false;
    if (focus) $("rename-room").focus();
  }

  function openCreate(direct) {
    if ($("new-room").disabled) return;
    createDirect = direct; $("create-menu").hidden = true; $("new-room").setAttribute("aria-expanded", "false");
    notice("create-error", ""); $("new-room-name").value = "";
    renderCreateSelection(); $("create-dialog").showModal(); $(direct ? "new-room-name" : "team").focus();
  }

  function initializeSelection(catalog) {
    if (!selectionLoaded && !selectedEntry(catalog)) {
      const initial = catalog.rooms.find(room => room.status === "ready" && isDefaultAssistant(room, catalog)) || catalog.rooms.find(room => room.status === "ready");
      if (initial) selectRoom(initial);
      else if (selectedRoom) selectRoom(null);
    }
    if (!selectionLoaded) {
      awaitDefaultSelection = !selectedRoom && catalog.default_assistant?.status === "pending";
      if (compactViewport(window)) layoutUI.showMobileRooms();
    }
    selectionLoaded = true;
    if (awaitDefaultSelection && !selectedRoom && !state.management && !$("create-dialog").open && !layoutUI.mobileProfileOpen && catalog.default_assistant?.status === "completed") {
      const assistant = catalog.rooms.find(room => room.status === "ready" && isDefaultAssistant(room, catalog));
      if (assistant) {
        const homeOpen = compactViewport(window) && layoutUI.roomsPanelOpen();
        selectRoom(assistant);
        if (homeOpen) layoutUI.showMobileRooms();
      }
    }
  }

  function restoreManagement() {
    try {
      const saved = JSON.parse(storage?.getItem(managementKey));
      if (saved?.payload && saved.operator &&
        ((saved.kind === "create" && saved.payload.create_id) || (saved.kind === "open" && saved.payload.open_id) ||
          (saved.kind === "delete" && saved.payload.room_id && saved.payload.room_instance))) {
        state.management = saved;
        // Reconcile the original receipt after reload; never repeat the POST.
        if (saved.selectOnComplete !== false) saved.selectionVersion = selectionVersion;
      }
    } catch { /* Server polling remains authoritative; never replay on reload. */ }
  }

  function mount() {
    $("delete-submit").addEventListener("click", submitDeletion);
    $("delete-dismiss").addEventListener("click", closeDeletion);
    $("delete-dialog").addEventListener("cancel", event => { event.preventDefault(); closeDeletion(); });
    $("delete-dialog").addEventListener("close", () => {
      if (!$("delete-dialog").open && deletion) closeDeletion();
    });
    document.addEventListener("pointerdown", event => {
      cancelRoomPress(); suppressedRoomClick = "";
      if (event.target.closest?.(".room-entry")?.dataset.roomKey !== revealedRoom) closeRoomActions();
    }, true);
    document.addEventListener("pointermove", event => {
      if (!roomPress || roomPress.pointerId !== event.pointerId) return;
      if (Math.hypot(event.clientX - roomPress.x, event.clientY - roomPress.y) > 10) {
        suppressedRoomClick = roomPress.key; closeRoomActions();
      }
    }, {passive:true});
    for (const type of ["pointerup", "pointercancel"]) document.addEventListener(type, event => {
      if (roomPress?.pointerId === event.pointerId) cancelRoomPress();
    });
    document.addEventListener("keydown", event => {
      if (event.key !== "Escape" || !revealedRoom) return;
      const row = Array.from($("room-list").children).find(row => row.dataset.roomKey === revealedRoom);
      event.preventDefault(); event.stopPropagation?.(); closeRoomActions(); row?.children[0].focus();
    });
    document.addEventListener("focusin", event => {
      const key = event.target.closest?.(".room-entry")?.dataset.roomKey;
      if (revealedRoom && key !== revealedRoom || roomPress && key !== roomPress.key) closeRoomActions();
    });
    document.addEventListener("visibilitychange", () => { if (document.hidden) closeRoomActions(); });
    for (const type of ["blur", "pagehide", "resize", "popstate"]) window.addEventListener(type, closeRoomActions);
    $("room-nav").addEventListener("scroll", closeRoomActions, {passive:true});

    try { selectedRoom = JSON.parse(storage?.getItem(selectionKey)); } catch { /* Tab storage is optional. */ }

    $("retry-default-assistant").addEventListener("click", async () => {
      if ($("retry-default-assistant").disabled) return;
      const epoch = state.catalog.service_epoch, record = state.catalog.default_assistant, restoring = record?.status === "deleted";
      if (restoring && !selectedEntry()) awaitDefaultSelection = true;
      defaultAssistantRetrying = true; defaultAssistantError = ""; renderDefaultAssistant();
      try {
        await request(`/v1/rooms/default-assistant/${restoring ? "restore" : "retry"}`, {service_epoch:epoch, caller:{kind:"human",id:state.operator},
          ...(restoring ? {room_id:record.room_id, room_instance:record.room_instance} : {})});
      } catch (error) {
        if (state.catalog?.service_epoch === epoch) defaultAssistantError = `${restoring ? "恢复" : "重试"}未确认，请核对默认助理状态。${error.message}`;
      } finally { defaultAssistantRetrying = false; await refresh(); renderDefaultAssistant(); }
    });

    $("rename-room").addEventListener("click", () => {
      if ($("rename-room").disabled) return;
      const room = state.catalog.rooms.find(room => room.room_instance === state.info?.room_instance);
      renameTarget = { room_id: room.room_id, room_instance: room.room_instance, expected_name: room.name,
        service_epoch: state.catalog.service_epoch, caller: { kind: "human", id: state.operator } };
      $("rename-room-name").value = room.name; notice("rename-error", "");
      $("room-name-row").hidden = true; $("rename-form").hidden = false;
      conversationUI.composerState(); $("rename-room-name").focus();
    });

    $("cancel-rename").addEventListener("click", () => closeRename());

    $("rename-form").addEventListener("keydown", event => {
      if (event.key === "Escape" && !event.isComposing) { event.preventDefault(); closeRename(); }
    });

    $("rename-form").addEventListener("submit", async (event) => {
      event.preventDefault();
      if (!renameTarget || $("save-room-name").disabled) return;
      const name = $("rename-room-name").value.trim();
      if (!name) { notice("rename-error", `请输入${conversationType()}名称。`); return; }
      if (new TextEncoder().encode(name).length > 256) { notice("rename-error", `${conversationType()}名称请控制在 256 字节内。`); return; }
      const target = renameTarget, version = runtime.viewVersion;
      renameSending = true; notice("rename-error", ""); conversationUI.composerState();
      try {
        await request("/v1/rooms/rename", { ...target, name });
        if (currentView(version) && renameTarget === target) closeRename();
      } catch (error) {
        if (currentView(version) && renameTarget === target) notice("rename-error", error.code === "conflict"
          ? "名称已在其他页面修改，请取消后重新编辑。"
          : "保存未确认，请取消并核对会话名称后再修改。");
      } finally {
        renameSending = false; await refresh(); conversationUI.composerState();
        if (currentView(version) && !renameTarget) $("rename-room").focus();
      }
    });

    const closeCreateMenu = () => {
      $("create-menu").hidden = true;
      $("new-room").setAttribute("aria-expanded", "false");
    };
    closeFilterMenu();
    const filterOptions = Object.keys(filterLabels).map(filter => $("conversation-filter-" + filter));
    const showFilterMenu = (last = false) => {
      closeCreateMenu();
      const menu = $("conversation-filter-menu");
      menu.hidden = false;
      const anchor = $("conversation-filter").getBoundingClientRect(), bounds = menu.getBoundingClientRect(), inset = 12;
      menu.style.setProperty("--filter-menu-left", `${Math.max(inset, Math.min(anchor.left, window.innerWidth - bounds.width - inset))}px`);
      menu.style.setProperty("--filter-menu-top", `${Math.max(inset, Math.min(anchor.bottom + 6, window.innerHeight - bounds.height - inset))}px`);
      $("conversation-filter").setAttribute("aria-expanded", "true");
      (last ? filterOptions.at(-1) : $("conversation-filter-" + conversationFilter)).focus();
      runtime.navigationChanged();
    };
    $("conversation-filter").addEventListener("click", () => {
      if ($("conversation-filter-menu").hidden) showFilterMenu();
      else closeFilterMenu();
    });
    $("conversation-filter").addEventListener("keydown", event => {
      if (!["ArrowDown", "ArrowUp"].includes(event.key)) return;
      event.preventDefault(); showFilterMenu(event.key === "ArrowUp");
    });
    for (const filter of Object.keys(filterLabels)) {
      $("conversation-filter-" + filter).addEventListener("click", () => setConversationFilter(filter));
    }
    $("conversation-filter-menu").addEventListener("keydown", event => {
      if (["Escape", "Tab"].includes(event.key)) {
        if (event.key === "Escape") { event.preventDefault(); event.stopPropagation?.(); }
        closeFilterMenu(); $("conversation-filter").focus();
      }
      if (["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
        event.preventDefault();
        const current = filterOptions.indexOf(document.activeElement);
        const index = event.key === "Home" ? 0 : event.key === "End" ? filterOptions.length - 1
          : (current + (event.key === "ArrowDown" ? 1 : -1) + filterOptions.length) % filterOptions.length;
        filterOptions[index].focus();
      }
    });
    for (const eventType of ["pointerdown", "focusin"]) document.addEventListener(eventType, event => {
      if (!$("conversation-filter-menu").hidden && !event.target.closest?.("#conversation-filter-menu, #conversation-filter")) closeFilterMenu();
    });
    window.addEventListener("resize", closeFilterMenu);
    window.addEventListener("scroll", closeFilterMenu);
    $("room-nav").addEventListener("scroll", closeFilterMenu);
    const showCreateMenu = (last = false) => {
      if ($("new-room").disabled) return;
      closeFilterMenu();
      const menu = $("create-menu");
      menu.hidden = false;
      // The menu lives outside the scrolling rail so it can open over the workspace.
      const anchor = $("new-room").getBoundingClientRect();
      const bounds = menu.getBoundingClientRect(), inset = 12, gap = 8;
      const beside = anchor.right + gap + bounds.width <= window.innerWidth - inset;
      const left = beside ? anchor.right + gap : anchor.right - bounds.width;
      const top = beside ? anchor.top : anchor.bottom + 6;
      menu.style.setProperty("--create-menu-left", `${Math.max(inset, Math.min(left, window.innerWidth - bounds.width - inset))}px`);
      menu.style.setProperty("--create-menu-top", `${Math.max(inset, Math.min(top, window.innerHeight - bounds.height - inset))}px`);
      $("new-room").setAttribute("aria-expanded", "true");
      $(last ? "new-team-room" : "new-direct").focus();
    };
    window.addEventListener("resize", closeCreateMenu);
    window.addEventListener("scroll", closeCreateMenu);
    $("room-nav").addEventListener("scroll", closeCreateMenu);
    $("new-room").addEventListener("click", () => {
      if ($("new-room").disabled) return;
      if ($("create-menu").hidden) showCreateMenu();
      else closeCreateMenu();
    });
    $("new-room").addEventListener("keydown", event => {
      if (!["ArrowDown", "ArrowUp"].includes(event.key)) return;
      event.preventDefault(); showCreateMenu(event.key === "ArrowUp");
    });

    $("new-direct").addEventListener("click", () => openCreate(true));

    $("new-team-room").addEventListener("click", () => openCreate(false));

    $("create-menu").addEventListener("keydown", event => {
      if (event.key === "Escape") { event.preventDefault(); closeCreateMenu(); $("new-room").focus(); }
      if (event.key === "Tab") { closeCreateMenu(); $("new-room").focus(); }
      if (["ArrowDown", "ArrowUp"].includes(event.key)) { event.preventDefault(); $(document.activeElement === $("new-direct") ? "new-team-room" : "new-direct").focus(); }
      if (["Home", "End"].includes(event.key)) { event.preventDefault(); $(event.key === "Home" ? "new-direct" : "new-team-room").focus(); }
    });

    document.addEventListener("pointerdown", event => {
      if (!$("create-menu").hidden && !event.target.closest?.("#create-menu, #new-room")) closeCreateMenu();
    });
    document.addEventListener("focusin", event => {
      if (!$("create-menu").hidden && !event.target.closest?.("#create-menu, #new-room")) closeCreateMenu();
    });

    $("close-create").addEventListener("click", () => $("create-dialog").close());

    dismissOnBackdrop($("create-dialog"), () => $("create-dialog").close());

    $("create-dialog").addEventListener("close", () => $("new-room").focus());

    $("team").addEventListener("change", renderCreateSelection);

    $("create-form").addEventListener("submit", (event) => {
      event.preventDefault();
      const team = selectedTeam();
      if ((!createDirect && !team) || $("create-room").disabled) return;
      const name = $("new-room-name").value.trim();
      if (new TextEncoder().encode(name).length > 256) { notice("create-error", `${createDirect ? "私信" : "房间"}名称请控制在 256 字节内。`); return; }
      state.management = { kind: "create", selectOnComplete: true, selectionVersion, operator: state.operator, opening: createDirect ? "" : team.opening_message,
        payload: { create_id: randomUUID(), name, ...(createDirect ? {is_direct:true, agent_role_id:"assistant"} : {team_id:team.id}) } };
      saveManagement(); void runManagement();
    });

    $("retry-management").addEventListener("click", () => { void runManagement(); });

    $("dismiss-management").addEventListener("click", () => {
      if (state.catalog?.busy || state.managementSending) return;
      state.management = null; saveManagement(); state.managementError = ""; void refresh();
    });
  }

  return {
    mount,
    acceptCatalog,
    initializeSelection,
    restoreManagement,
    renderRooms,
    renderConversationFilter,
    isDirect,
    isDefaultAssistant,
    closeFilterMenu,
    selectedEntry,
    managementBusy,
    conversationType,
    roomName,
    closeRename,
    get renameSending() { return renameSending; },
    get renameTarget() { return renameTarget; },
    reconcileManagement,
    selectRoom,
    get selectedRoom() { return selectedRoom; },
    renderTeams,
    renderCreateSelection,
  };
}
