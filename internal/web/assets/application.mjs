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
import { createConversation } from "./features/conversation.mjs";
import { createPlatformInteractions } from "./features/platform-interactions.mjs";
import { createConnections } from "./features/connections.mjs";
import { createOnboarding } from "./features/onboarding.mjs";
import { createGitLabConnection } from "./features/gitlab-connection.mjs";
import { createLarkConnection } from "./features/lark-connection.mjs";
import { createScionConnection } from "./features/scion-connection.mjs";
import { createConfirmation } from "./confirmation.mjs";
import { createUserMenu } from "./features/user-menu.mjs";

// Browser services can be replaced per instance; feature state never lives in
// module globals. Tests import this same module graph and supply a bounded DOM.
function browserEnvironment(overrides) {
  const env = { request, renderMessageBody };
  Object.defineProperties(env, Object.getOwnPropertyDescriptors(overrides));
  for (const key of ["document", "window", "sessionStorage", "localStorage", "fetch", "Option", "setInterval", "requestAnimationFrame", "performance", "setTimeout", "clearTimeout", "ResizeObserver", "MutationObserver"]) {
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
  const { document, window, setInterval } = env;
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
    members: [], profiles: [], teams: [], connected: false, managed: false, catalog: null, catalogConnected: false,
    isDirect: false, management: null, managementSending: false, managementError: "", draft: {}, restart: null };

  let refreshTask = null, recoveryTask = null, syncFailureSince = null;
  state.suspended = document.hidden || window.navigator?.onLine === false;
  state.recovering = false;
  let serverIdentity = null;
  const navigation = createBrowserNavigation({ env, layers: () => {
    const layers = [];
    if (compactViewport(window)) {
      if (layoutUI.roomsPanelOpen?.()) layers.push({ id:"rooms", close:layoutUI.closeMobilePanels });
      if (layoutUI.membersPanelOpen?.()) layers.push({ id:"members", close:layoutUI.closeMobilePanels });
    }
    for (const [id, button] of [["activity-dialog","close-activity"], ["notification-detail","close-notification"],
      ["workspace-dialog","close-workspace"], ["create-dialog","close-create"], ["settings-dialog","close-settings"], ["access-confirm-dialog","access-confirm-cancel"]]) {
      if ($(id)?.open && !$(id).hidden) layers.push({id, close:() => id === "settings-dialog" ? settingsUI.closeSettings() : $(button)?.click?.()});
    }
    return layers;
  }});
  function suspend() {
    if (!state.suspended) viewVersion++;
    state.suspended = true; state.connected = false; state.catalogConnected = false;
    $("connection").textContent = window.navigator?.onLine === false ? "网络已断开 · 草稿已保留" : "页面已暂停刷新";
    $("connection").classList.add("offline");
    if (window.navigator?.onLine === false) notice("sync-error", "网络已断开，草稿已保留。");
    conversationUI.composerState();
  }
  function resume(recover = true) {
    if (!recover) return refresh();
    if (recoveryTask) return recoveryTask;
    state.suspended = false; state.recovering = true;
    state.connected = false; state.catalogConnected = false;
    $("connection").textContent = "正在核对连接与会话…";
    conversationUI.composerState();
    // An old read may still finish after a mobile page freeze. Settle it before
    // reading the authenticated service identity and current operations again.
    recoveryTask = Promise.resolve(refreshTask).then(() => refresh()).finally(() => {
      state.recovering = false; recoveryTask = null;
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

  const currentView = (version) => version === viewVersion && !state.suspended;

  function rpc(path, data = {}, info = state.info) {
    return request(path, { ...info, caller: { kind: "human", id: state.operator }, ...data });
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
    conversationUI.saveDraft();
    roomsUI.closeRename(false);
    $("notification-detail").close();
    viewVersion++;
    interactionsUI.resetPlatformInteractions();
    membersUI.resetActivity(); layoutUI.hideConversationNotifications();
    $("room-menu").open = false;
    state.info = info; state.operator = operator; state.connected = false;
    syncFailureSince = null; notice("sync-error", "");
    notice("notification-next-step", "");
    conversationUI.resetMentionViewing();
    if (info) conversationUI.restoreMentionProgress();
    state.draft = info ? conversationUI.drafts.get(info, operator) : {};
    state.messages.clear(); state.lastSeq = 0; state.clearedThrough = 0; state.restart = null; state.members = [];
    conversationUI.resetRestartNotice();
    conversationUI.showDraft(); conversationUI.renderMessages(); membersUI.renderMembers();
    $("jump-latest").hidden = true;
    notice("restart-notice", "");
    return true;
  }

  async function sync() {
    let version = viewVersion;
    try {
      let config = await request("/v1/ui");
      if (!currentView(version)) return;
      const identity = [config.installation_id || "", config.service_epoch];
      if (serverIdentity && identity.some((part, i) => part !== serverIdentity[i])) {
        conversationUI.saveDraft();
        notice("service-change", "服务已重新启动或切换，已重新读取状态；草稿保留，未确认请求不会自动重发。");
      }
      if (serverIdentity && serverIdentity[0] !== identity[0]) { selectView(null, state.operator); version = viewVersion; }
      state.installationID = config.installation_id || "";
      const mentionIdentityChanged = state.operatorDisplayName !== (config.operator_display_name || "");
      state.operatorDisplayName = config.operator_display_name || "";
      serverIdentity = identity;
      state.managed = !!config.managed;
      state.isDirect = config.is_direct === true;
      settingsUI.applyTimeZone(config);
      if (state.managed) {
        const catalog = await request("/v1/rooms");
        if (!currentView(version)) return;
        state.catalog = catalog; state.catalogConnected = true;
        settingsUI.applyTimeZone(catalog);
        await roomsUI.reconcileManagement(catalog);
        roomsUI.initializeSelection(catalog);
        version = viewVersion;
        const room = roomsUI.selectedEntry(catalog);
        config = { ...room?.service, operator: catalog.operator };
        if (room?.service) selectView(room.service, catalog.operator);
        version = viewVersion;
        roomsUI.renderTeams(catalog.teams || []); roomsUI.renderRooms();
        if (!state.info) state.operator = catalog.operator;
        await workspaceUI.syncWorkspace(catalog);
        if (!currentView(version)) return;
        if (workspaceUI.workspaceView?.data?.observation && workspaceUI.workspaceBusy()) {
          const observation = workspaceUI.workspaceView.data.observation;
          selectView(observation, catalog.operator); version = viewVersion;
          $("room-name").textContent = roomsUI.roomName();
          const initial = state.messages.size === 0, stick = initial || conversationUI.nearBottom(), scrollTop = $("timeline").scrollTop;
          const read = await conversationUI.readPages(initial ? { mode: "latest", latest: 50 } : { mode: "range", after: state.lastSeq }, observation);
          if (!currentView(version)) return;
          const cleared = conversationUI.applyClear(read.clearedThrough);
          if (read.messages.length || cleared) {
            for (const message of read.messages) state.messages.set(message.seq, message);
            conversationUI.renderMessages();
            if (stick) conversationUI.bottom(); else $("timeline").scrollTop = scrollTop;
          }
          state.lastSeq = read.snapshot;
          syncFailureSince = null;
          state.connected = false;
          $("connection").textContent = "Workspace 设置待完成 · 可查看历史";
          membersUI.activity.close(); $("activity-dialog").close(); membersUI.renderMembers(); return;
        }
        if (catalog.busy || room?.busy) {
          // Keep the old discussion visible but invalidate every in-flight view.
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
          $("empty").querySelector("h2").textContent = "开始一个新任务";
          $("empty").querySelector("p").textContent = "从左侧新建私信与助理交流，或新建房间与团队协作。";
          $("connection").textContent = room ? (room.runtime_error ? `运行受阻 · ${room.runtime_error}` : `点击${roomsUI.conversationType(room)}打开`) : "已连接 · 尚未选择会话";
          notice("sync-error", ""); return;
        }
      }
      const info = { room_id: config.room_id, room_instance: config.room_instance, service_epoch: config.service_epoch };
      const changedRoom = selectView(info, config.operator);
      version = viewVersion;
      conversationUI.restoreMentionProgress();
      if (!state.managed) { membersUI.renderProfiles(config.profiles || []); roomsUI.renderRooms(); }
      $("room-name").textContent = roomsUI.roomName();
      $("empty").querySelector("h2").textContent = "从一条消息开始";
      $("empty").querySelector("p").textContent = "发送任务或想法，让房间里的 Agent 一起参与。";
      const members = await rpc("/v1/member/list", {}, info);
      if (!currentView(version)) return;
      state.restart = members.discussion_restart || null;
      if (state.draft.restartRequest && state.draft.restartRequest.id === state.restart?.id) {
        state.draft.restartRequest = null; state.draft.restartError = ""; conversationUI.saveDraft();
      }
      const initial = state.messages.size === 0;
      const stick = initial || conversationUI.nearBottom();
      const scrollTop = $("timeline").scrollTop;
      const read = await conversationUI.readPages(initial ? { mode: "latest", latest: 50 } : { mode: "range", after: state.lastSeq }, info);
      if (!currentView(version) || read.clearedThrough < state.clearedThrough) return;
      const cleared = conversationUI.applyClear(read.clearedThrough);
      const membersChanged = JSON.stringify(state.members.map(m => m.agent_id)) !== JSON.stringify(members.members.map(m => m.agent_id));
      state.members = members.members;
      if (read.messages.length || changedRoom || cleared || membersChanged || mentionIdentityChanged) {
        for (const message of read.messages) state.messages.set(message.seq, message);
        conversationUI.renderMessages();
        if (stick) conversationUI.bottom();
        else { $("timeline").scrollTop = scrollTop; $("jump-latest").hidden = false; }
      }
      state.lastSeq = read.snapshot;
      conversationUI.renderMentionNotice();
      state.connected = true;
      void interactionsUI.refreshPlatformInteractions();
      membersUI.renderMembers(); void membersUI.activity.refresh();void policyUI.refreshMemberPolicy();void inboxUI.refreshMemberInbox();
      syncFailureSince = null;
      $("connection").textContent = "服务已连接";
      $("connection").classList.remove("offline");
      notice("sync-error", members.write_error ? `${roomsUI.conversationType()}写入暂停：${members.write_error}` : "");
    } catch (error) {
      if (!currentView(version)) return;
      syncFailureSince ??= env.performance.now();
      // A normal poll will read again. Busy reads do not invalidate the last
      // successful view or replay mutations; surface only sustained contention.
      if (error.code === "busy" && env.performance.now() - syncFailureSince < 4500) return;
      state.connected = false; state.catalogConnected = false;
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

  function refresh() {
    if (state.suspended || document.hidden || window.navigator?.onLine === false) return Promise.resolve();
    if (!refreshTask) refreshTask = sync().finally(() => { refreshTask = null; });
    return refreshTask;
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
    navigationChanged: navigation.sync,
    canPoll: () => lifecycle.active() && !state.recovering && (state.managed ? state.catalogConnected : state.connected),
    discussionBusy,
    setSelectOptions,
    refresh,
    rpc,
    reconcileChildren,
    get viewVersion() { return viewVersion; },
    currentView,
    dismissOnBackdrop,
    confirmAction
  };

  install(userMenuUI, createUserMenu({ env, runtime }));
  install(settingsUI, createSettings({
    env, runtime, connectionsUI, conversationUI, roomsUI, workspaceUI, policyUI, notificationsUI, onboardingUI, membersUI
  }));
  install(roomsUI, createRooms({
    env, runtime, layoutUI, workspaceUI, conversationUI, settingsUI, membersUI
  }));
  install(layoutUI, createLayout({
    env, roomsUI, runtime, notificationsUI, membersUI, settingsUI
  }));
  install(workspaceUI, createWorkspace({
    env, runtime, notificationsUI, membersUI, roomsUI, conversationUI, settingsUI
  }));
  install(notificationsUI, createNotifications({
    env, membersUI, workspaceUI, runtime, roomsUI, settingsUI, connectionsUI, onboardingUI, layoutUI
  }));
  install(membersUI, createMembers({
    env, runtime, notificationsUI, workspaceUI, onboardingUI, inboxUI, roomsUI, layoutUI, policyUI, conversationUI, connectionsUI, settingsUI
  }));
  install(policyUI, createPolicy({
    env, membersUI, runtime, settingsUI, conversationUI
  }));
  install(inboxUI, createInbox({
    env, settingsUI, notificationsUI, runtime, membersUI, policyUI
  }));
  install(conversationUI, createConversation({
    env, runtime, roomsUI, notificationsUI, membersUI, settingsUI, interactionsUI, workspaceUI, layoutUI
  }));
  install(interactionsUI, createPlatformInteractions({
    env, runtime, conversationUI, settingsUI, connectionsUI, gitlabUI
  }));
  install(connectionsUI, createConnections({
    env, settingsUI, runtime, scionUI, gitlabUI, larkUI, onboardingUI, workspaceUI, notificationsUI
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

  navigation.mount();
  lifecycle.mount();

  document.addEventListener("visibilitychange", conversationUI.updateMentionViewing);

  document.addEventListener("toggle", conversationUI.scheduleMentionViewing, true);

  document.addEventListener("close", conversationUI.scheduleMentionViewing, true);

  window.addEventListener("pagehide", conversationUI.resetMentionViewing);

  window.addEventListener("pagehide", conversationUI.saveDraft);

  return {
    refresh,
    openNotificationSource: notificationsUI.openNotificationSource,
    selectedAgent: membersUI.selectedAgent,
    notificationSource: notificationsUI.currentSource
  };
}
