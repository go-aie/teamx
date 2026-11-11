import { createAssistantIcon } from "../icons.mjs";

// Room catalog, selection, creation, deletion and default assistant recovery.
export function createRooms({ env, runtime, layoutUI, workspaceUI, conversationUI, settingsUI, membersUI }) {
  const { document, window, Option, request } = env;
  const { state, $, notice, currentView, refresh, discussionBusy, dismissOnBackdrop, storage } = runtime;

  let defaultAssistantRetrying = false, defaultAssistantError = "";

  const managementKey = "teamx.room-management";

  const selectionKey = "teamx.selected-room";

  let selectedRoom = null, selectionVersion = 0, selectionLoaded = false, awaitDefaultSelection = false;

  function selectedEntry(catalog = state.catalog) {
    return catalog?.rooms.find(room => room.room_id === selectedRoom?.room_id && room.room_instance === selectedRoom?.room_instance);
  }

  function isDirect() { return state.managed ? selectedEntry()?.is_direct === true : state.isDirect; }

  function conversationType(room = selectedEntry()) { return room?.is_direct || !state.managed && state.isDirect ? "私信" : "房间"; }

  function isDefaultAssistant(room, catalog = state.catalog) {
    const ref = catalog?.default_assistant;
    return !!room && ref?.status !== "deleted" && room.room_id === ref?.room_id && room.room_instance === ref?.room_instance;
  }

  function selectRoom(room) {
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
    return state.catalog?.rooms.find(room => room.room_instance === state.info?.room_instance)?.name || state.info?.room_id || "Room";
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
    $("team-hint").textContent = createDirect ? "新助理默认暂停；进入详情后可启用。创建不会自动发送消息或连接平台。" : "成员与会话独立创建。开场消息仅填入草稿，由你发送。";
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

  async function reconcileManagement(catalog) {
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
        state.management = null; state.managementError = "会话及其 Agent 私有会话数据已删除。"; saveManagement();
      } else if (!room.busy && room.status === "deleting") {
        state.managementError = `删除尚未完成，请核查后明确重试。${room.error || ""}`;
      }
    } else if (op.kind === "create") {
      const room = catalog.rooms.find(room => room.create_id === op.payload.create_id);
      if (room?.status === "ready") seedCreated(op, room);
    } else if (op.kind === "open") {
      const query = new URLSearchParams({ service_epoch: catalog.service_epoch, caller_id: op.operator, open_id: op.payload.open_id });
      try {
        const receipt = await request(`/v1/rooms/open?${query}`);
        finishOpen(op, receipt);
      } catch (error) {
        if (state.management === op) state.managementError = `打开结果尚未确认，可查询或用原请求重试。${error.message}`;
      }
    }
  }

  function finishOpen(op, receipt) {
    if (state.management !== op || receipt.status === "running") return;
    state.managementError = receipt.status === "failed" ? `打开失败，历史已保留。${receipt.error}` : "";
    if (receipt.status === "completed" && receipt.service && op.selectOnComplete !== false && op.selectionVersion === selectionVersion) {
      selectRoom({ ...receipt, service: receipt.service });
    }
    state.management = null; saveManagement();
  }

  function forgetRoom(room) {
    if (selectedRoom?.room_instance === room.room_instance) selectRoom(null);
    if (state.info?.room_instance === room.room_instance) runtime.selectView(null, state.operator);
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
      : author?.kind === "agent" && room.is_direct ? "助理"
      : author?.kind === "agent" ? room.members?.find(member => member.id === author.id)?.name || author.id
      : author?.id || "";
    const text = preview.text?.replace(/\s+/g, " ").trim() || "[消息]";
    return sender ? `${sender}：${text}` : text;
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
    $("rooms-empty").hidden = catalog.rooms.length > 0;
    $("new-room").disabled = !state.catalogConnected || discussionBusy() || !!state.management || state.managementSending || !!catalog.write_error;
    const now = Date.now();
    const signature = JSON.stringify({ rooms: catalog.rooms, selected: selectedRoom, timeZone:settingsUI.systemTime.zone, timeError:settingsUI.systemTime.error, day:settingsUI.systemTime.day(now),
      defaultAssistant: catalog.default_assistant, busy: discussionBusy(), management: !!state.management, sending: state.managementSending, connected: state.catalogConnected });
    if ($("room-list").dataset.signature !== signature) {
      $("room-list").dataset.signature = signature;
      const rows = [...catalog.rooms].sort((a,b) => Number(isDefaultAssistant(b)) - Number(isDefaultAssistant(a)) || new Date(roomListTime(b)) - new Date(roomListTime(a))).map(room => {
        const row = document.createElement("div"); row.className = "room-entry"; row.dataset.direct = String(!!room.is_direct);
        const button = document.createElement("button"); button.type = "button"; button.className = "room-choice";
        button.setAttribute("aria-current", String(room.room_instance === selectedRoom?.room_instance));
        const name = document.createElement("strong"), updated = document.createElement("time");
        name.textContent = room.name;
        if (room.is_direct) name.insertBefore(createAssistantIcon(document), name.firstChild);
        row.dataset.defaultAssistant = String(isDefaultAssistant(room));
        const statusText = room.status === "deleting" ? (room.busy ? "正在删除…" : `删除待重试${room.error ? ` · ${room.error}` : ""}`)
          : room.status === "creating" ? `创建待完成${room.error ? ` · ${room.error}` : " · 可重试原创建请求"}` : room.runtime_status === "opening" ? "正在打开…"
          : room.runtime_error ? `运行受阻 · ${room.runtime_error}`
          : room.busy ? "会话操作进行中…" : "";
        updated.className = "room-time";
        updated.textContent = settingsUI.systemTime.recent(roomListTime(room), now);
        updated.dateTime = roomListTime(room);
        updated.title = `${room.last_message_at ? "最后消息" : "创建于"}：${settingsUI.formatDate(roomListTime(room), {year:"numeric",month:"long",day:"numeric",hour:"2-digit",minute:"2-digit",second:"2-digit"})}`;
        name.title = room.name;
        const detailText = statusText || roomListPreview(room);
        button.title = [room.name, updated.title, detailText].join("\n");
        button.setAttribute("aria-label", `${conversationType(room)}：${room.name}${isDefaultAssistant(room) ? "，默认助理" : ""}，${updated.title}，${detailText}`);
        button.append(name, updated);
        const detail = document.createElement("span"); detail.className = statusText ? "room-status" : "room-preview"; detail.textContent = detailText; detail.title = detailText; button.append(detail);
        if (isDefaultAssistant(room)) { const badge = document.createElement("small"); badge.className = "default-assistant-badge"; badge.textContent = "默认"; button.append(badge); }
        button.disabled = room.status !== "ready" || !state.catalogConnected || !!catalog.busy;
        button.addEventListener("click", () => startSelection(room));
        row.append(button);
        if (isDefaultAssistant(room) && room.status !== "deleting") return row;
        const remove = document.createElement("button"); remove.type = "button"; remove.className = "room-delete";
        const icon = document.createElementNS("http://www.w3.org/2000/svg", "svg");
        icon.setAttribute("viewBox", "0 0 24 24"); icon.setAttribute("aria-hidden", "true");
        const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
        path.setAttribute("d", "M3 6h18M9 6V4h6v2M5 6l1 14h12l1-14M10 10v6m4-6v6");
        icon.append(path); remove.append(icon);
        remove.title = `${room.status === "deleting" ? "重试删除" : "删除"}${conversationType(room)}「${room.name}」`;
        remove.setAttribute("aria-label", remove.title);
        remove.disabled = !state.catalogConnected || !!catalog.busy || !!room.busy || !!state.management || !!catalog.write_error;
        remove.addEventListener("click", () => startDeletion(room));
        row.append(remove);
        return row;
      });
      $("direct-list").replaceChildren(...rows.filter(row => row.dataset.direct === "true"));
      $("room-list").replaceChildren(...rows.filter(row => row.dataset.direct !== "true"));
      $("direct-group-title").textContent = `私信 · ${catalog.rooms.filter(room => room.is_direct).length}`;
      $("room-group-title").textContent = `房间 · ${catalog.rooms.filter(room => !room.is_direct).length}`;
    }
    let text = state.managementError || catalog.write_error || "";
    if (workspaceUI.workspaceView?.data?.operation?.status === "running") text = `Workspace · ${workspaceUI.workspaceStages[workspaceUI.workspaceView.data.operation.phase] || "正在应用"}`;
    else if (state.management?.kind === "delete" && selectedEntry()?.busy) text = `正在停止${conversationType()}、结清会话并删除数据…`;
    else if (catalog.busy) text = "共享资源操作进行中…";
    else if (state.managementSending) text = "正在提交会话操作…";
    else if (state.management) text = [text, state.management.room && state.management.kind === "create"
      ? "会话已创建，点击进入。" : "操作结果尚未确认。可核对列表后用原请求重试；不会自动续跑。"].filter(Boolean).join(" ");
    notice("management-notice", text);
    $("retry-management").hidden = !state.management || catalog.busy || state.managementSending;
    $("retry-management").disabled = !state.catalogConnected;
    $("retry-management").textContent = state.management?.kind === "create" && state.management.room ? `进入已创建${conversationType(state.management.room)}` : "核对并重试原请求";
    $("dismiss-management").hidden = !state.management || catalog.busy || state.managementSending;
  }

  async function runManagement() {
    const op = state.management;
    if (!op || state.managementSending || !state.catalogConnected || state.catalog.busy) return;
    state.managementSending = true; state.managementError = "";
    renderRooms(); conversationUI.composerState(); membersUI.renderMembers(); renderCreateSelection();
    try {
      const manage = (path, payload) => request(path, { service_epoch: state.catalog.service_epoch,
        caller: { kind: "human", id: op.operator }, ...payload });
      if (op.kind === "create") {
        const room = op.room || await manage("/v1/rooms/create", op.payload);
        if (room.status !== "ready") throw new Error("会话创建尚未完成，请核查后重试。");
        seedCreated(op, room);
        op.kind = "open"; op.selectionVersion ??= selectionVersion; op.payload = { open_id: crypto.randomUUID(), room_id: room.room_id, room_instance: room.room_instance };
        saveManagement();
      }
      const result = await manage(op.kind === "delete" ? "/v1/rooms/delete" : "/v1/rooms/open", op.payload);
      if (op.kind === "open") finishOpen(op, result);
      $("create-dialog").close();
    } catch (error) {
      if (state.management === op) {
        state.managementError = op.kind === "delete" ? `删除结果尚未确认，请核对会话列表。${error.message}` : `操作未确认，会话和历史已保留。${error.message}`;
        // A definitive rejection has no execution to recover. Other failures keep
        // the original ID, including a create interrupted after index publication.
        if (["request", "identity", "conflict", "capacity"].includes(error.code)) {
          state.management = null; saveManagement();
        }
      }
      notice("create-error", state.managementError);
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
      selectRoom(room); void refresh(); return;
    }
    if (state.management || state.managementSending || room.busy) return;
    selectionVersion++; awaitDefaultSelection = false;
    state.management = { kind: "open", selectOnComplete: true, selectionVersion, operator: state.operator, payload: {
      open_id: crypto.randomUUID(), room_id: room.room_id, room_instance: room.room_instance } };
    saveManagement(); void runManagement();
  }

  function startDeletion(room) {
    if (isDefaultAssistant(room) && room.status !== "deleting") return;
    if (state.management || room.busy || !state.catalogConnected || state.catalog.busy || state.catalog.write_error) return;
    if (!window.confirm(`永久删除${conversationType(room)}「${room.name}」？\n将停止该会话并删除消息、所有 Agent 私有会话（含历史会话）、诊断和工作区文件。此操作无法撤销。`)) return;
    state.management = { kind: "delete", operator: state.operator, payload: {
      room_id: room.room_id, room_instance: room.room_instance } };
    saveManagement(); void runManagement();
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
    renameTarget = null; $("rename-form").hidden = true; $("room-heading").hidden = false;
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
    if (!selectionLoaded) awaitDefaultSelection = !selectedRoom && catalog.default_assistant?.status === "pending";
    selectionLoaded = true;
    if (awaitDefaultSelection && !selectedRoom && !state.management && !$("create-dialog").open && catalog.default_assistant?.status === "completed") {
      const assistant = catalog.rooms.find(room => room.status === "ready" && isDefaultAssistant(room, catalog));
      if (assistant) selectRoom(assistant);
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
      $("room-heading").hidden = true; $("rename-form").hidden = false;
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
    const showCreateMenu = (last = false) => {
      if ($("new-room").disabled) return;
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
        payload: { create_id: crypto.randomUUID(), name, ...(createDirect ? {is_direct:true, agent_role_id:"assistant"} : {team_id:team.id}) } };
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
    initializeSelection,
    restoreManagement,
    renderRooms,
    isDirect,
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
