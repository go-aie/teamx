import { createBrowserLifecycle } from "./features/browser-lifecycle.mjs";
import { createBrowserNavigation } from "./features/browser-navigation.mjs";
import { request, sameService, compactViewport } from "./client.mjs";
import { renderMessageBody } from "./markdown.mjs";
import { createSettings } from "./features/settings.mjs";
import { createRooms } from "./features/rooms.mjs";
import { createLayout } from "./features/layout.mjs";
import { createWorkspace } from "./features/workspace.mjs";
import { createNotifications } from "./features/notifications.mjs";
import { createMembers } from "./features/members.mjs";
import { createPolicy } from "./features/policy.mjs";
import { createInbox } from "./features/inbox.mjs";
import { createCalendar } from "./features/calendar.mjs";
import { createConversation } from "./features/conversation.mjs";
import { createPlatformInteractions } from "./features/platform-interactions.mjs";
import { createConnections } from "./features/connections.mjs";
import { createOnboarding } from "./features/onboarding.mjs";
import { createGitLabConnection } from "./features/gitlab-connection.mjs";
import { createLarkConnection } from "./features/lark-connection.mjs";
import { createScionConnection } from "./features/scion-connection.mjs";
import { createConfirmation } from "./confirmation.mjs";
import { createUserMenu } from "./features/user-menu.mjs";
import { createSharedRead, createUpdateScheduler, createUpdateConnection } from "./updates.mjs";
import { createRoomViewCache, roomViewCacheLimits } from "./room-view-cache.mjs";

// Browser services can be replaced per instance; feature state never lives in
// module globals. Tests import this same module graph and supply a bounded DOM.
function browserEnvironment(overrides) {
  const env = { request, renderMessageBody };
  Object.defineProperties(env, Object.getOwnPropertyDescriptors(overrides));
  for (const key of ["document", "window", "sessionStorage", "localStorage", "fetch", "Option", "requestAnimationFrame", "performance", "setTimeout", "clearTimeout", "EventSource", "ResizeObserver", "MutationObserver"]) {
    if (!(key in env)) Object.defineProperty(env, key, { get: () => globalThis[key] });
  }
  return env;
}

// Factories keep peer handles for callbacks. Install all APIs before mounting
// events, and preserve getters so a replaced local view is read live.
function install(target, feature) {
  Object.defineProperties(target, Object.getOwnPropertyDescriptors(feature));
  Object.freeze(target);
}

export async function createApp(overrides = {}) {
  const env = browserEnvironment(overrides);
  const { document, window } = env;
  const request = env.request;
  const settingsUI = {};
  const roomsUI = {};
  const layoutUI = {};
  const workspaceUI = {};
  const notificationsUI = {};
  const membersUI = {};
  const policyUI = {};
  const inboxUI = {};
  const conversationUI = {};
 const calendarUI = {};
  const interactionsUI = {};
  const connectionsUI = {};
  const onboardingUI = {};
  const gitlabUI = {};
  const larkUI = {};
  const scionUI = {};
  const userMenuUI = {};

  const $ = (id) => document.getElementById(id);
  const confirmAction = env.confirmAction || createConfirmation(document);

  const state = { info: null, operator: "operator", messages: new Map(), lastSeq: 0, oldest: 0,
    clearedThrough: 0,
    messagePreview: false, messagesReady: false, membersReady: false,
    members: [], profiles: [], teams: [], connected: false, managed: false, catalog: null, catalogConnected: false,
    isDirect: false, management: null, managementSending: false, managementError: "", draft: {}, restart: null };

  let refreshTask = null, refreshResult = null, recoveryTask = null, syncFailureSince = null, updateConnection;
  const scheduler = createUpdateScheduler(env), resources = {};
  state.suspended = document.hidden || window.navigator?.onLine === false;
  state.recovering = false;
  let serverIdentity = null;
  const confirmedGlobalVersions = {};
  const roomViews = createRoomViewCache();
  const navigation = createBrowserNavigation({ env, layers: () => {
    const layers = [];
    if (compactViewport(window)) {
      if (layoutUI.mobileProfileOpen) layers.push({ id:"mobile-profile", close:layoutUI.showMobileRooms });
      if (state.managed && !layoutUI.roomsPanelOpen?.()) layers.push({ id:"mobile-conversation", close:layoutUI.showMobileRooms });
      if (layoutUI.membersPanelOpen?.()) layers.push({ id:"members", close:layoutUI.closeMobilePanels });
    }
    if ($("workspace-picker").open) layers.push({ id:"workspace-picker", close:workspaceUI.closeWorkspacePicker });
    if (!$("conversation-filter-menu").hidden) layers.push({ id:"conversation-filter", close:roomsUI.closeFilterMenu });
    for (const [id, button] of [["calendar-detail","calendar-detail-close"], ["activity-dialog","close-activity"], ["notification-detail","close-notification"],
      ["workspace-dialog","close-workspace"], ["create-dialog","close-create"], ["settings-dialog","close-settings"], ["access-confirm-dialog","access-confirm-cancel"]]) {
      if ($(id)?.open && !$(id).hidden) layers.push({id, close:() => id === "settings-dialog" ? settingsUI.backSettings() : $(button)?.click?.()});
      if (id === "settings-dialog" && $(id).open) {
        if (settingsUI.settingsTab === "platforms" && !$("connection-form").hidden) layers.push({id:"connection-form",close:connectionsUI.returnFromPlatform});
        if (settingsUI.settingsTab === "models" && !$("model-edit-form").hidden) layers.push({id:"model-editor",close:settingsUI.returnFromModelEditor});
        if (settingsUI.settingsTab === "general" && !$("timezone-options").hidden) layers.push({id:"timezone-picker",close:settingsUI.backSettings});
      }
    }
    if ($("directory-dialog").open) layers.push({id:"directory-dialog",close:() => $("directory-cancel").click()});
    if ($("confirmation-dialog").open) layers.push({id:"confirmation",close:() => $("confirmation-dialog").close()});
    return layers;
  }});
  function suspend() {
    updateConnection?.stop(); scheduler.pause();
    if (!state.suspended) viewVersion++;
    state.suspended = true; state.connected = false; state.catalogConnected = false;
    $("connection").textContent = window.navigator?.onLine === false ? "网络已断开 · 草稿已保留" : "页面已暂停刷新";
    $("connection").classList.add("offline");
    if (window.navigator?.onLine === false) notice("sync-error", "网络已断开，草稿已保留。");
    conversationUI.composerState();
  }
  function resume() {
    if (recoveryTask) return recoveryTask;
    state.suspended = false; state.recovering = true;
    scheduler.resume();
    state.connected = false; state.catalogConnected = false;
    $("connection").textContent = "正在核对连接与会话…";
    conversationUI.composerState();
    recoveryTask = Promise.resolve(refreshTask).then(() => refresh({ background: true })).finally(() => {
      state.recovering = false; recoveryTask = null; updateConnection?.start();
    });
    return recoveryTask;
  }
  const lifecycle = createBrowserLifecycle({ env, suspend, resume, save:() => conversationUI.saveDraft() });

  let storage;
  try { storage = env.sessionStorage; } catch { /* The UI also works without storage. */ }

  function setSelectOptions(id, options, value) {
    const select = $(id), selectedValue = value ?? select.value;
    const signature = JSON.stringify(options.map(option => [option.value, option.textContent, !!option.disabled]));
    const customizable = globalThis.CSS?.supports?.("appearance", "base-select") && select.classList.contains("field-control");
    if (select.dataset.options !== signature) {
      const content = [];
      // Keep long selected labels inside the text area of customizable native selects.
      if (customizable) {
        const button = document.createElement("button"); button.type = "button";
        button.append(document.createElement("selectedcontent")); content.push(button);
      }
      select.replaceChildren(...content,...options); select.dataset.options = signature;
    }
    select.value = selectedValue;
    if (customizable) {
      // Options populated while hidden do not always update selectedcontent in
      // the browser. Keep its visible label in sync without dispatching changes.
      const syncLabel = () => { const content=select.querySelector("selectedcontent"); if(content) content.textContent=select.selectedOptions[0]?.textContent || ""; };
      syncLabel();
      if (!select.dataset.captionBound) { select.addEventListener("change",syncLabel); select.dataset.captionBound="true"; }
    }
  }

  let viewVersion = 0;

  const platformRead = createSharedRead({
    scope: () => [state.installationID, state.catalog?.service_epoch, state.operator],
    read: () => {
      const query = new URLSearchParams({ service_epoch: state.catalog.service_epoch, caller_id: state.operator });
      return request(`/v1/platforms?${query}`);
    },
  });

  const currentView = (version) => version === viewVersion && !state.suspended;

  function rpc(path, data = {}, info = state.info, signal) {
    return request(path, { ...info, caller: { kind: "human", id: state.operator }, ...data }, undefined, 10000, signal);
  }

  function notice(id, text) {
    $(id).textContent = text;
    $(id).hidden = !text;
  }

  function discussionBusy() {
    return workspaceUI.workspaceBusy() || roomsUI.managementBusy() || state.draft.restartStarting || state.restart?.status === "running" || !!state.draft.restartRequest;
  }

  function reconcileChildren(parent, children) {
    for (let i=0; i<children.length; i++) if (parent.children[i] !== children[i]) parent.insertBefore(children[i], parent.children[i] || null);
    const retained=new Set(children);
    for (const child of [...parent.children]) if (!retained.has(child)) child.remove();
  }

  function selectView(info, operator) {
    if (sameService(state.info, info) && state.operator === operator) return false;
    roomViews.scope([state.installationID, state.catalog?.service_epoch || serverIdentity?.[1], state.operator]);
    const previous = state.catalog?.rooms.find(room => sameService(room.service, state.info));
    if (state.info && state.messagesReady && state.membersReady && !state.messagePreview &&
      (!state.managed || previous?.status === "ready" && state.catalog.operator === state.operator)) {
      const messages = [...state.messages.values()].sort((a, b) => a.seq - b.seq).slice(-roomViewCacheLimits.messages);
      const position = conversationUI.capturePosition(new Set(messages.map(message => message.seq)));
      if (position) roomViews.put(state.info, { messages, members: state.members, lastSeq: state.lastSeq, clearedThrough: state.clearedThrough, position });
      else roomViews.forget(state.info);
    }
    roomViews.scope([state.installationID, state.catalog?.service_epoch || serverIdentity?.[1], operator]);
    const entry = state.catalog?.rooms.find(room => room.room_id === info?.room_id && room.room_instance === info?.room_instance);
    const cached = info && (!state.managed || entry?.status === "ready") ? roomViews.get(info, entry?.human_read?.cleared_through || 0) : null;
    conversationUI.saveDraft();
    roomsUI.closeRename(false);
    $("notification-detail").close();
    viewVersion++;
    interactionsUI.resetPlatformInteractions();
    membersUI.resetActivity();
    state.info = info; state.operator = operator; state.connected = false;
    state.messagesReady = state.membersReady = false; state.messagePreview = !!cached;
    $("connection").textContent = cached ? "正在同步…" : "正在加载会话…";
    $("connection").classList.add("offline");
    syncFailureSince = null; notice("sync-error", "");
    notice("notification-next-step", "");
    state.draft = info ? conversationUI.drafts.get(info, operator) : {};
    state.messages.clear(); state.lastSeq = 0; state.clearedThrough = 0; state.restart = null; state.members = [];
    if (cached) {
      for (const message of cached.messages) state.messages.set(message.seq, message);
      state.lastSeq = cached.lastSeq; state.clearedThrough = cached.clearedThrough; state.members = cached.members;
    }
    calendarUI.reset?.();
 conversationUI.resetMessageNodes();
    conversationUI.resetReadingContext();
    conversationUI.resetRestartNotice();
    conversationUI.showDraft(); conversationUI.renderMessages(); membersUI.renderMembers();
    $("jump-latest").hidden = true;
    if (cached) conversationUI.restorePosition(cached.position);
    notice("restart-notice", "");
    return true;
  }

  // HTTP readers keep their original view and business guards. The scheduler
  // supplies an additional stream/write fence before any state is installed.
  async function readSettings({ current = () => true } = {}) {
    const version = viewVersion;
    const config = await request("/v1/ui");
    if (!currentView(version) || !current()) return;
    const identity = [config.installation_id || "", config.service_epoch];
    if (serverIdentity && identity.some((part, i) => part !== serverIdentity[i])) {
      conversationUI.saveDraft();
      notice("service-change", "服务已重新启动或切换，已重新读取状态；草稿保留，未确认请求不会自动重发。");
    }
    if (serverIdentity && serverIdentity[0] !== identity[0]) selectView(null, state.operator);
    state.installationID = config.installation_id || "";
    roomViews.scope([state.installationID, config.service_epoch, state.operator]);
    const mentionIdentityChanged = state.operatorDisplayName !== (config.operator_display_name || "");
    state.operatorDisplayName = config.operator_display_name || "";
    serverIdentity = identity;
    state.managed = !!config.managed;
    state.isDirect = config.is_direct === true;
    settingsUI.applyTimeZone(config);
    if (mentionIdentityChanged) { conversationUI.renderMessages(); membersUI.renderMembers(); }
    return config;
  }

  async function readCatalog({ current = () => true } = {}) {
    const version = viewVersion;
    if (!state.managed) return;
    const catalog = await request("/v1/rooms");
    if (!currentView(version) || !current()) return;
    roomsUI.acceptCatalog(catalog);
    state.catalog = catalog; state.catalogConnected = true;
    roomViews.scope([state.installationID, catalog.service_epoch, catalog.operator]);
    roomViews.reconcile(catalog.rooms);
    const currentRoom = catalog.rooms.find(room => room.room_id === state.info?.room_id && room.room_instance === state.info?.room_instance);
    if (state.messagePreview && currentRoom?.human_read?.cleared_through > state.clearedThrough &&
      conversationUI.applyClear(currentRoom.human_read.cleared_through)) conversationUI.renderMessages();
    settingsUI.applyTimeZone(catalog);
    await roomsUI.reconcileManagement(catalog, { current });
    if (!current()) return;
    interactionsUI.restoreAuthorizationSelection(catalog);
    roomsUI.initializeSelection(catalog);
    const room = roomsUI.selectedEntry(catalog);
    if (room?.service) selectView(room.service, catalog.operator);
    roomsUI.renderTeams(catalog.teams || []); roomsUI.renderRooms();
    if (!state.info) state.operator = catalog.operator;
    return catalog;
  }

  async function readWorkspace(options) {
    if (state.managed && state.catalog) await workspaceUI.syncWorkspace(state.catalog, options);
  }

  function readConfiguration(options) {
    return connectionsUI.refreshPlatformState({ background: true, ...options });
  }

  async function readMembers({ current = () => true } = {}) {
    const version = viewVersion, info = state.info;
    if (!chatReadable()) return;
    const members = await rpc("/v1/member/list", {}, info);
    if (!currentView(version) || !current()) return;
    state.restart = members.discussion_restart || null;
    if (state.draft.restartRequest && state.draft.restartRequest.id === state.restart?.id) {
      state.draft.restartRequest = null; state.draft.restartError = ""; conversationUI.saveDraft();
    }
    calendarUI.sync?.();
 const nextMembers = members.members.map(member=>({...member,agent_name:member.agent_name || (roomsUI.isDirect() ? roomsUI.roomName() : roomsUI.selectedEntry()?.members?.find(m=>m.id===member.agent_id)?.agent_name) || member.agent_id}));
    const names = list=>JSON.stringify(list.map(m=>[m.agent_id,m.membership_id,m.agent_name]));
    const changed = names(state.members) !== names(nextMembers);
    state.members = nextMembers;
    state.membersReady = true;
    if (changed) conversationUI.renderMessages();
    membersUI.renderMembers();
    notice("sync-error", members.write_error ? `${roomsUI.conversationType()}写入暂停：${members.write_error}` : "");
    return members;
  }

  function chatReadable() {
    if (!state.info || state.suspended) return false;
    if (!state.managed) return true;
    const room = roomsUI.selectedEntry();
    return state.catalogConnected && !state.catalog?.busy && !room?.busy && !!room?.service &&
      !!workspaceUI.workspaceView?.connected && !workspaceUI.workspaceBusy();
  }

  async function readMessages({ current = () => true, observation = false } = {}) {
    const version = viewVersion, info = state.info;
    if (!observation && !chatReadable()) return;
    const preview = state.messagePreview, initial = preview || state.messages.size === 0;
    const stick = !preview && initial || conversationUI.nearBottom(), scrollTop = $("timeline").scrollTop;
    const after = state.lastSeq;
    const read = await conversationUI.readPages(initial ? { mode: "latest", latest: 50 } : { mode: "range", after }, info);
    if (!currentView(version) || !current() || read.clearedThrough < state.clearedThrough || read.snapshot < state.lastSeq) return;
    const position = preview ? conversationUI.capturePosition() : null;
    if (preview) { state.messages.clear(); conversationUI.resetMessageNodes(); }
    const cleared = conversationUI.applyClear(read.clearedThrough);
    state.messagePreview = false; state.messagesReady = !observation;
    if (read.messages.length || cleared || preview) {
      for (const message of read.messages) state.messages.set(message.seq, message);
      conversationUI.renderMessages();
      if (preview && position) conversationUI.restorePosition(position);
      else if (stick) conversationUI.bottom();
      else { $("timeline").scrollTop = scrollTop; if (read.messages.length) $("jump-latest").hidden = false; }
    }
    state.lastSeq = read.snapshot;
    conversationUI.renderMentionNotice();
    if (!observation) conversationUI.messagesRead(read, { after, stick });
    return read;
  }

  function readChat(options) {
    return Promise.all([readMembers(options), readMessages(options)]);
  }

  function readDetails(options) {
    return Promise.all([interactionsUI.refreshPlatformInteractions(options), membersUI.activity.refresh(options),
      policyUI.refreshMemberPolicy(options), inboxUI.refreshMemberInbox(options)]);
  }

  async function sync(round, reuse = {}) {
    let version = viewVersion;
    try {
      let config = reuse.settings ? { managed: state.managed } : await resources.settings.refresh();
      if (!config) return;
      round.settings = true;
      version = viewVersion;
      if (state.managed) {
        const catalog = reuse.catalog ? state.catalog : await resources.catalog.refresh();
        if (!catalog) return;
        round.catalog = true;
        version = viewVersion;
        const room = roomsUI.selectedEntry(catalog);
        config = { ...room?.service, operator: catalog.operator };
        await resources.workspace.refresh();
        if (!currentView(version)) return;
        round.configuration = refreshConfiguration({ reusePlatforms: reuse.platforms });
        const workspaceObservation = workspaceUI.workspaceObservation();
        if (workspaceObservation) {
          selectView(workspaceObservation, catalog.operator); version = viewVersion;
          $("room-name").textContent = roomsUI.roomName();
          await resources.messages.refresh();
          if (!currentView(version)) return;
          syncFailureSince = null; state.connected = false;
          $("connection").textContent = workspaceUI.workspaceView?.data?.directory_error
            ? "工作目录不可用 · 可查看历史并修复目录" : "Workspace 设置待完成 · 可查看历史";
          membersUI.activity.close(); $("activity-dialog").close(); membersUI.renderMembers();
          return;
        }
        if (catalog.busy || room?.busy) {
          state.connected = false; viewVersion++;
          membersUI.activity.close(); $("activity-dialog").close();
          $("connection").textContent = "会话操作进行中";
          membersUI.renderMembers(); return;
        }
        if (!room?.service) {
          syncFailureSince = null;
          selectView(null, catalog.operator);
          $("room-name").textContent = room?.name || "选择或新建会话";
          document.title = `${$("room-name").textContent} · TeamX`;
          $("connection").textContent = room ? (room.runtime_error ? `运行受阻 · ${room.runtime_error}` : `点击${roomsUI.conversationType(room)}打开`) : "已连接 · 尚未选择会话";
          notice("sync-error", ""); return;
        }
        await syncChat(config);
      } else {
        selectView({ room_id: config.room_id, room_instance: config.room_instance, service_epoch: config.service_epoch }, config.operator);
        version = viewVersion;
        await syncChat(config);
      }
    } catch (error) {
      if (!currentView(version)) return;
      syncFailureSince ??= env.performance.now();
      // Failed observations stay dirty in their resource scheduler. Busy reads
      // retain the last view and never replay mutations.
      if (error.code === "busy" && env.performance.now() - syncFailureSince < 4500) return;
      state.connected = false;
      const message = error.code === "busy" ? "服务繁忙，正在重试同步；已有消息和草稿已保留。"
        : window.navigator?.onLine === false ? "网络已断开，草稿已保留。"
        : error.code === "identity" ? "会话身份已变化，正在重新核对；已有消息和草稿已保留。"
        : "暂时无法同步，正在重连；已有消息和草稿已保留。";
      $("connection").textContent = message;
      $("connection").classList.add("offline");
      notice("sync-error", message);
      $("members").dataset.signature = ""; membersUI.renderMembers();
    } finally { userMenuUI.render(); conversationUI.composerState(); roomsUI.renderCreateSelection(); roomsUI.renderRooms(); }
  }

  async function syncChat(config) {
    const version = viewVersion;
    conversationUI.resetReadingContext();
    void resources.human_read.refresh().catch(() => {});
    if (!state.managed) { membersUI.renderProfiles(config.profiles || []); roomsUI.renderRooms(); }
    $("room-name").textContent = roomsUI.roomName();
    if (!chatReadable()) {
      state.connected = false;
      $("connection").textContent = "工作目录状态暂时无法核对";
      return;
    }
    const [members, messages] = await Promise.all([resources.members.refresh(), resources.messages.refresh()]);
    if (!currentView(version) || !members || !messages || !state.membersReady || !state.messagesReady) return;
    state.connected = !updateConnection || updateConnection.connected;
    membersUI.renderMembers();
    refreshDetails();
    syncFailureSince = null;
    $("connection").textContent = "服务已连接";
    $("connection").classList.remove("offline");
    conversationUI.composerState();
  }

  function refresh({ background = false, reuse } = {}) {
    if (state.suspended || document.hidden || window.navigator?.onLine === false) return Promise.resolve();
    if (!refreshTask) {
      const round = {};
      refreshTask = sync(round, reuse).then(() => round).finally(() => { refreshTask = null; updateConnection?.ensure(); });
      refreshResult = refreshTask.then(() => round.configuration);
    }
    return background ? refreshTask : refreshResult;
  }

  function enterRoom() {
    if (state.suspended) return;
    roomsUI.renderRooms(); conversationUI.composerState();
    if (!updateConnection) { void refresh(); return; }
    // hello owns the entry read. Opening the subscription first closes the
    // message race without reading the entire page twice.
    if (!updateConnection.ensure({ roomOnly: true }) && chatReadable()) {
      // Returning from the phone's Room list keeps the same live subscription,
      // but starts a new reading visit and catches up its visible messages.
      void resources.messages.refresh().catch(() => {});
    }
  }

  async function reconcileStream({ roomOnly = false, globalVersions, current = () => true } = {}) {
    await refreshTask;
    if (!current()) return;
    const reusable = topic => roomOnly && state.managed && state.catalogConnected &&
      state.catalog?.service_epoch === serverIdentity?.[1] && state.operator === state.catalog.operator &&
      typeof globalVersions?.[topic] === "string" && globalVersions[topic] === confirmedGlobalVersions[topic];
    const reuse = Object.fromEntries(["settings", "catalog", "platforms"].map(topic => [topic, reusable(topic)]));
    // The platform projection also contains Room references and display names.
    reuse.platforms &&= reuse.catalog;
    const round = await refresh({ background: true, reuse });
    if (!current() || !round) return;
    for (const topic of ["settings", "catalog"]) {
      if (round[topic]) confirmedGlobalVersions[topic] = globalVersions?.[topic];
    }
    // Configuration is independent of chat readiness. Confirm its reuse token
    // only after the authoritative shared read has actually succeeded.
    void Promise.resolve(round.configuration).then(results => {
      if (current() && results?.[0]?.status === "fulfilled" && platformRead.value) {
        confirmedGlobalVersions.platforms = globalVersions?.platforms;
      }
    });
  }

  function dismissOnBackdrop(dialog, close) {
    let pressedOutside = false;
    const outside = event => {
      const rect = dialog.getBoundingClientRect();
      return event.target === dialog && (event.clientX < rect.left || event.clientX >= rect.right ||
        event.clientY < rect.top || event.clientY >= rect.bottom);
    };
    dialog.addEventListener("pointerdown", event => {
      pressedOutside = event.button === 0 && event.isPrimary !== false && outside(event);
    });
    dialog.addEventListener("pointercancel", () => { pressedOutside = false; });
    dialog.addEventListener("close", () => { pressedOutside = false; });
    dialog.addEventListener("click", event => {
      const dismiss = pressedOutside && outside(event);
      pressedOutside = false;
      if (dialog.open && dismiss) return close();
    });
  }

  const runtime = {
    $,
    notice,
    state,
    storage,
    selectView,
    forgetRoomView: room => roomViews.forget(room),
    enterRoom,
    navigationChanged: navigation.sync,
    conversationVisibilityChanged: () => {
      conversationUI.conversationVisibilityChanged();
    },
    discussionBusy,
    setSelectOptions,
    refresh,
    platformRead,
    observe: (topic, { cached = false } = {}) => topic === "configuration"
      ? refreshConfiguration({ reusePlatforms: cached, reuseNotifications: cached && !!workspaceUI.workspaceView?.notificationsConnected })
      : resources[topic]?.refresh().catch(() => {}),
    changed: topic => resources[topic]?.invalidate(),
    wake: topic => resources[topic]?.wake(),
    supersede: topic => resources[topic]?.supersede(),
    rpc,
    reconcileChildren,
    get viewVersion() { return viewVersion; },
    currentView,
    dismissOnBackdrop,
    confirmAction
  };

  install(calendarUI,createCalendar({env,runtime,roomsUI,conversationUI,settingsUI}));
  install(userMenuUI, createUserMenu({ env, runtime, conversationUI, roomsUI, membersUI }));
  install(settingsUI, createSettings({
    env, runtime, connectionsUI, conversationUI, roomsUI, workspaceUI, policyUI, notificationsUI, onboardingUI, membersUI
  }));
  install(roomsUI, createRooms({
    env, runtime, layoutUI, workspaceUI, conversationUI, settingsUI, membersUI, userMenuUI
  }));
  install(layoutUI, createLayout({
    env, roomsUI, runtime, notificationsUI, membersUI, settingsUI, userMenuUI
  }));
  install(workspaceUI, createWorkspace({
    env, runtime, notificationsUI, membersUI, roomsUI, conversationUI, settingsUI, connectionsUI
  }));
  install(notificationsUI, createNotifications({
    env, membersUI, workspaceUI, runtime, roomsUI, settingsUI, connectionsUI, onboardingUI, layoutUI
  }));
  install(membersUI, createMembers({
    env, runtime, notificationsUI, workspaceUI, onboardingUI, inboxUI, roomsUI, layoutUI, policyUI, conversationUI, connectionsUI, settingsUI, userMenuUI
  }));
  install(policyUI, createPolicy({
    env, membersUI, runtime, settingsUI, conversationUI
  }));
  install(inboxUI, createInbox({
    env, settingsUI, notificationsUI, runtime, membersUI, policyUI
  }));
  install(conversationUI, createConversation({
    calendarUI,
    env, runtime, roomsUI, membersUI, settingsUI, interactionsUI, workspaceUI, layoutUI, userMenuUI
  }));
  install(interactionsUI, createPlatformInteractions({
    env, runtime, conversationUI, settingsUI, connectionsUI, gitlabUI, roomsUI
  }));
  install(connectionsUI, createConnections({
    env, settingsUI, runtime, scionUI, gitlabUI, larkUI, onboardingUI, workspaceUI, notificationsUI, membersUI
  }));
  install(onboardingUI, createOnboarding({
    env, runtime, connectionsUI, larkUI, settingsUI, roomsUI, workspaceUI, notificationsUI, membersUI, gitlabUI, scionUI
  }));
  install(gitlabUI, createGitLabConnection({
    env, connectionsUI, runtime, settingsUI, onboardingUI, interactionsUI
  }));
  install(larkUI, createLarkConnection({
    env, runtime, connectionsUI, settingsUI, onboardingUI
  }));
  install(scionUI, createScionConnection({
    env, runtime, connectionsUI, settingsUI, onboardingUI
  }));

  const globalScope = () => []; // Service changes fence all readers in the stream owner.
  const roomScope = () => [...globalScope(), viewVersion, state.info];
  const memberScope = () => [...roomScope(), membersUI.selectedAgent()?.agent_id, membersUI.selectedAgent()?.membership_id, membersUI.agentTab];
  const active = () => !state.suspended && !document.hidden && window.navigator?.onLine !== false;
  const detail = tab => active() && $("activity-dialog").open && membersUI.agentTab === tab && !!membersUI.selectedAgent();
  const notificationVisible = () => active() && !!state.catalog && !!workspaceUI.workspaceView &&
    (layoutUI.membersPanelOpen() || detail("platforms") || $("notification-detail").open);
  // Both categories share one reader and one throttle. A summary remains
  // pending until a read starts; diagnostics cannot make a hidden inspector
  // read the full subscription merely because the summary sidebar is open.
  let notificationSummaryPending = true;
  const notificationDiagnosticsVisible = () => active() && $("notification-detail").open &&
    !!workspaceUI.workspaceView && ["gitlab", "messages"].includes(workspaceUI.workspaceView.notificationChannel) &&
    !workspaceUI.workspaceView.notificationHistory;
  function readError(topic, error) {
    if (topic === "messages") state.messagesReady = false;
    if (topic === "members") state.membersReady = false;
    if (error.code === "identity" && ["settings", "catalog", "workspace", "members", "messages"].includes(topic)) updateConnection?.reconnect();
    if (["settings", "catalog", "workspace", "members", "messages"].includes(topic)) {
      syncFailureSince ??= env.performance.now();
      if (error.code === "busy" && env.performance.now() - syncFailureSince < 4500) return;
      if (["settings", "catalog"].includes(topic)) state.catalogConnected = false;
      state.connected = false;
      $("connection").textContent = "暂时无法同步，正在核对；已有消息和草稿已保留。";
      $("connection").classList.add("offline"); conversationUI.composerState();
    }
  }
  function register(topic, read, enabled = active, scope = roomScope, interval) {
    resources[topic] = scheduler.resource(topic.split(":")[0], { scope, enabled, read, interval,
      onError: error => readError(topic, error) });
  }
  const observeChat = () => {
    if (chatReadable()) {
      for (const topic of ["members", "messages", "human_read", "interactions"]) resources[topic].invalidate();
    }
    conversationUI.composerState();
  };
  register("settings", async options => {
    const before = JSON.stringify(serverIdentity), result = await readSettings(options);
    if (updateConnection && !refreshTask && before !== JSON.stringify(serverIdentity)) updateConnection.ensure();
    return result;
  }, active, globalScope);
  register("catalog", async options => {
    const before = viewVersion, result = await readCatalog(options);
    if (result && updateConnection && !refreshTask) {
      updateConnection.ensure();
      if (before !== viewVersion || !workspaceUI.workspaceView?.connected || !state.connected) {
        resources.workspace.invalidate(); observeChat();
      }
    }
    return result;
  }, () => active() && state.managed, globalScope);
  register("workspace", async options => {
    await readWorkspace(options);
    if (options.current()) {
      if (!chatReadable()) { state.connected = false; conversationUI.composerState(); }
      else if (!state.connected && !refreshTask) observeChat();
    }
  }, () => active() && !!state.catalog, roomScope);
  register("messages", async options => {
    const result = await readMessages({ ...options, observation: !!workspaceUI.workspaceObservation() });
    if (result && options.current() && !refreshTask && !state.connected && !state.recovering && chatReadable()) resources.members.invalidate();
    return result;
  },
    () => active() && (!!state.info && (!!workspaceUI.workspaceObservation() || chatReadable())));
  register("members", async options => {
    const result = await readMembers(options);
    if (result && options.current() && state.messagesReady && state.membersReady && !state.recovering && chatReadable() && (!updateConnection || updateConnection.connected)) {
      state.connected = true; $("connection").textContent = "服务已连接"; $("connection").classList.remove("offline");
      conversationUI.composerState();
    }
    return result;
  }, () => active() && chatReadable());
  register("calendar",calendarUI.refresh,()=>active() && chatReadable());
  register("human_read", conversationUI.pollHumanRead, () => active() && chatReadable());
  register("interactions", interactionsUI.refreshPlatformInteractions, () => active() && chatReadable());
  register("activity", membersUI.activity.read, () => detail("activity"), memberScope);
  register("inbox", inboxUI.refreshMemberInbox, () => detail("inbox") && !inboxUI.inboxView?.loading, memberScope);
  register("policy", policyUI.refreshMemberPolicy, () => detail("policy") && !policyUI.policyView?.loading && !policyUI.policyView?.saving && !policyUI.policyView?.needsRefresh && !policyUI.policyEditor, memberScope);
  resources.notifications = scheduler.resource("notifications", {
    scope: roomScope, enabled: notificationVisible,
    needed: () => notificationSummaryPending || notificationDiagnosticsVisible(),
    read: async options => {
      const summary = notificationSummaryPending;
      notificationSummaryPending = false;
      try { return await notificationsUI.loadNotificationSubscriptions(state.catalog, workspaceUI.workspaceView, options); }
      catch (error) { if (options.current()) notificationSummaryPending ||= summary; throw error; }
    },
    onError: error => readError("notifications", error),
  });
  register("tools", options => membersUI.loadCLITools(membersUI.memberToolsView, { background: true, ...options }),
    () => active() && !!membersUI.memberToolsView && membersUI.toolsCurrent(membersUI.memberToolsView) &&
      ($("activity-dialog").open || $("notification-detail").open), memberScope);
  register("platforms", async options => {
    platformRead.invalidate();
    await Promise.all([workspaceUI.loadWorkspacePlatforms({ background: true, ...options }),
      $("settings-dialog").open && settingsUI.settingsTab === "platforms" ? connectionsUI.loadPlatforms(options) : undefined]);
    if (options.current()) notificationsUI.renderNotificationPlatforms();
  }, () => active() && !!state.catalog, globalScope);
  register("models", settingsUI.readModels, () => active() && settingsUI.modelsNeedRead(), globalScope);
  for (const [provider, feature, field] of [["gitlab", gitlabUI, "gitlabAuth"], ["lark", larkUI, "auth"], ["scion", scionUI, "scionAuth"]]) {
    register(`authorization:${provider}`, feature.readAuthorization, () => active() && !!connectionsUI.platforms[field],
      () => [...globalScope(), connectionsUI.platforms[field]?.session_id, connectionsUI.platforms.formVersion]);
  }
  function refreshDetails() {
    for (const topic of ["activity", "inbox", "policy"])
      if (resources[topic]) void resources[topic].refresh().catch(() => {});
    void resources.interactions.refresh().catch(() => {});
  }
  function refreshConfiguration({ reusePlatforms = false, reuseNotifications = false } = {}) {
    return Promise.allSettled([reusePlatforms ? workspaceUI.loadWorkspacePlatforms({ background: true, cached: true }) : resources.platforms.refresh(),
      reuseNotifications ? undefined : resources.notifications.refresh(), resources.tools.refresh(),
      resources.models.refresh(), ...["gitlab", "lark", "scion"].map(provider => resources[`authorization:${provider}`].refresh())]);
  }
  const streamIdentity = () => ({ installation_id: state.installationID,
    service_epoch: state.managed ? state.catalog?.service_epoch : serverIdentity?.[1],
    ...(state.info ? {room_id:state.info.room_id, room_instance:state.info.room_instance, room_service_epoch:state.info.service_epoch} : {}) });
  function streamStatus(connected) {
    state.recovering = !connected;
    if (!connected) {
      state.messagesReady = state.membersReady = false;
      state.connected = false; $("connection").textContent = "实时同步已断开，正在重新核对…"; $("connection").classList.add("offline");
    } else if (state.connected && chatReadable()) {
      state.connected = true; $("connection").textContent = "服务已连接"; $("connection").classList.remove("offline");
    }
    conversationUI.composerState();
  }
  function invalidate(data) {
    const info = state.info;
    if (data.room_id && (!info || data.room_id !== info.room_id || data.room_instance !== info.room_instance || data.room_service_epoch !== info.service_epoch)) return;
    if (data.agent_id && (data.agent_id !== membersUI.selectedAgent()?.agent_id || data.membership_id !== membersUI.selectedAgent()?.membership_id)) {
      // Member status and subscription summaries cover the whole Room; private
      // inspectors only observe the selected membership.
      if (!["members", "notifications"].includes(data.topic)) return;
    }
    delete confirmedGlobalVersions[data.topic];
    if (data.topic === "notifications" && data.kind !== "diagnostics") notificationSummaryPending = true;
    if (data.topic === "authorization") for (const provider of ["gitlab", "lark", "scion"]) resources[`authorization:${provider}`].invalidate();
    else resources[data.topic]?.invalidate();
  }
  function wakeDetails() { for (const resource of Object.values(resources)) resource.wake(); }
  document.addEventListener("toggle", wakeDetails, true);
  document.addEventListener("close", wakeDetails, true);
  window.addEventListener("focus", wakeDetails);

  calendarUI.mount();
  settingsUI.mount();
  roomsUI.mount();
  layoutUI.mount();
  workspaceUI.mount();
  notificationsUI.mount();
  membersUI.mount();
  policyUI.mount();
  inboxUI.mount();
  conversationUI.mount();
  interactionsUI.mount();
  connectionsUI.mount();
  onboardingUI.mount();
  gitlabUI.mount();
  larkUI.mount();
  scionUI.mount();
  userMenuUI.mount();

  roomsUI.restoreManagement();

  await refresh();
  updateConnection = createUpdateConnection({ env, identity: streamIdentity, reconcile: reconcileStream, invalidate,
    status: streamStatus, fence: () => scheduler.reset(), authenticate: () => request("/v1/access/session") });
  if (!state.suspended) updateConnection.start();

  if (window.location?.hash === "#platform-settings" && state.catalogConnected) {
    window.history?.replaceState(null,"",window.location.pathname+window.location.search);
    await settingsUI.openSettings("platforms");
  }

  if (state.catalogConnected) await gitlabUI.restoreAuthorization();

  navigation.mount();
  lifecycle.mount();

  document.addEventListener("visibilitychange", conversationUI.updateMessageViewing);

  document.addEventListener("toggle", conversationUI.scheduleMessageViewing, true);

  document.addEventListener("close", conversationUI.scheduleMessageViewing, true);

  window.addEventListener("pagehide", conversationUI.leaveConversation);

  window.addEventListener("pagehide", conversationUI.saveDraft);

  return {
    refresh,
    reads: {
      settings: readSettings, catalog: readCatalog, workspace: readWorkspace,
      chat: readChat, messages: readMessages, members: readMembers,
      configuration: readConfiguration, details: readDetails,
      human_read: conversationUI.pollHumanRead,
      platforms: workspaceUI.loadWorkspacePlatforms,
      notifications: options => state.catalog && workspaceUI.workspaceView
        ? notificationsUI.loadNotificationSubscriptions(state.catalog, workspaceUI.workspaceView, options) : Promise.resolve(),
      tools: options => membersUI.memberToolsView && membersUI.toolsCurrent(membersUI.memberToolsView)
        ? membersUI.loadCLITools(membersUI.memberToolsView, { background: true, ...options }) : Promise.resolve(),
      interactions: interactionsUI.refreshPlatformInteractions,
      activity: membersUI.activity.read,
      inbox: inboxUI.refreshMemberInbox, policy: policyUI.refreshMemberPolicy,
    },
    openNotificationSource: notificationsUI.openNotificationSource,
    selectedAgent: membersUI.selectedAgent,
    notificationSource: notificationsUI.currentSource
  };
}
