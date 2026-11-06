import { request, readPages as collectPages, createActivityReader, createRoomDrafts, sameService, mentionRanges, unseenMentions } from "/ui/client.mjs";
import { renderMessageBody, notificationOptions } from "/ui/markdown.mjs";

const $ = (id) => document.getElementById(id);
const state = { info: null, operator: "operator", messages: new Map(), lastSeq: 0, oldest: 0,
  clearedThrough: 0,
  members: [], profiles: [], teams: [], connected: false, managed: false, catalog: null, catalogConnected: false,
  management: null, managementSending: false, managementError: "", draft: {}, restart: null };
let refreshTask = null;
let historyLoading = false;
const memberBusy = new Set();
let storage;
try { storage = sessionStorage; } catch { /* The UI also works without storage. */ }
const drafts = createRoomDrafts(storage);
const managementKey = "teamx.room-management";
const selectionKey = "teamx.selected-room";
let selectedRoom = null, selectionVersion = 0, selectionLoaded = false;
try { selectedRoom = JSON.parse(storage?.getItem(selectionKey)); } catch { /* Tab storage is optional. */ }
function selectedEntry(catalog = state.catalog) {
  return catalog?.rooms.find(room => room.room_id === selectedRoom?.room_id && room.room_instance === selectedRoom?.room_instance);
}
function selectRoom(room) {
  selectedRoom = room ? { room_id: room.room_id, room_instance: room.room_instance } : null;
  selectionVersion++;
  try {
    if (selectedRoom) storage?.setItem(selectionKey, JSON.stringify(selectedRoom));
    else storage?.removeItem(selectionKey);
  } catch { /* Keep this tab's selection in memory. */ }
  selectView(room?.service || null, state.catalog?.operator || state.operator);
  selectWorkspace(room, state.catalog?.operator || state.operator);
}
const panelsKey = "teamx.room-panels";
const panels = { rooms: true, members: true, memberWidth: null };
let memberResize = null;
try {
  const saved = JSON.parse(storage?.getItem(panelsKey));
  for (const side of ["rooms", "members"]) if (typeof saved?.[side] === "boolean") panels[side] = saved[side];
  if (Number.isFinite(saved?.memberWidth) && saved.memberWidth > 0) panels.memberWidth = saved.memberWidth;
} catch { /* Sidebars remain usable when tab storage is unavailable. */ }

// Workspace state is scoped to a Room instance + operator, independent of the
// member-service epoch. Only bounded operation IDs reach browser storage.
// Workspace connection choices stay in memory until an explicit application.
let memberToolsView = null;
let workspaceView = null;
const workspaceKey = (room, operator) => JSON.stringify(["teamx.workspace", room.room_instance, operator]);
const workspaceStages = { waiting: "正在结清旧执行", preparing: "正在初始化目录与绑定", committing: "正在保存配置", sessions: "正在更新成员 Session", completed: "设置已生效" };
const workspaceErrors = { interrupted: "服务曾中断，请核查后显式重试。", cleanup_failed: "旧执行未能结清，请先检查服务状态。", preparation_failed: "初始化失败，原配置和目录现场已保留，请检查仓库、认证或目标目录。", commit_unknown: "配置提交结果待核查，请重新打开服务后查看状态。", session_failed: "成员 Session 更新未完成，请核查后显式重试。", credential_missing: "凭据缺失，请检查服务端凭据或指定的环境变量。" };
function workspaceBusy() {
  return !!workspaceView && (workspaceView.sending || !!workspaceView.pending ||
    !!workspaceView.data?.operation && workspaceView.data.operation.status !== "completed");
}
function workspaceCanRevise() {
  return workspaceView?.data?.operation?.status === "failed" && workspaceView.data.can_revise === true;
}
function workspaceFormBusy() {
  return workspaceView?.sending || !!workspaceView?.pending || workspaceBusy() && !workspaceCanRevise();
}
function saveWorkspacePending(ws) {
  try {
    if (ws.pending) storage?.setItem(ws.key, JSON.stringify(ws.pending));
    else storage?.removeItem(ws.key);
  } catch { /* The server still retains accepted operation IDs. */ }
}
function selectWorkspace(room, operator) {
  const key = room && workspaceKey(room, operator);
  if (key === workspaceView?.key) return;

  closeWorkspacePicker();
  memberToolsView = null;
  $("notification-detail").close();
  $("workspace-dialog").close();
  workspaceView = room ? { room: { room_id: room.room_id, room_instance: room.room_instance }, operator, key,
    data: null, sending: false, pending: null, closeOnComplete: null, error: "", formVersion: null, missing: false, connected: false } : null;
  if (!workspaceView) return;
  try {
    const pending = JSON.parse(storage?.getItem(key));
    if (/^[a-f0-9]{32}$/.test(pending?.operation_id)) {
      workspaceView.pending = { operation_id: pending.operation_id };
      workspaceView.closeOnComplete = pending.operation_id;
    }
  } catch { /* Never replay requests from storage. */ }
}
async function syncWorkspace(catalog) {
  const room = selectedEntry(catalog);
  selectWorkspace(room, catalog.operator);
  const ws = workspaceView;
  if (!ws) { renderWorkspace(); return; }
  let completed = false;
  const query = (operationID = "") => {
    const params = new URLSearchParams({ service_epoch: catalog.service_epoch, caller_id: ws.operator, ...ws.room });
    if (operationID) params.set("operation_id", operationID);
    return request(`/v1/workspace?${params}`);
  };
  try {
    let data = await query();
    if (ws !== workspaceView) return;
    if (ws.pending && data.operation?.operation_id !== ws.pending.operation_id) {
      try { data = await query(ws.pending.operation_id); }
      catch (error) {
        if (error.code !== "operation_not_found") throw error;
        ws.missing = true;
        ws.error = "未查到原操作收据，无法确认凭据是否曾保存。不会自动重发；可重新选择连接并显式申请新操作。";
      }
    }
    if (ws !== workspaceView) return;
    const operation = data.operation, previous = ws.data?.operation;
    completed = operation?.status === "completed" && (ws.closeOnComplete === operation.operation_id ||
      previous?.operation_id === operation.operation_id && previous.status === "running");
    ws.data = data; ws.connected = true;
    if (ws.queryFailed && !ws.missing) { ws.error = ""; ws.queryFailed = false; }
    if (ws.pending && ws.pending.operation_id === data.operation?.operation_id) {
      ws.pending = null; ws.missing = false; ws.error = ""; saveWorkspacePending(ws);
    }
  } catch {
    if (ws !== workspaceView) return;
    ws.queryFailed = true; ws.connected = false; ws.error = "暂时无法查询 Workspace，正在重连；不会自动重发操作。";
  }
  await syncGitLabNotifications(catalog, ws);
  if (ws !== workspaceView) return;
  renderWorkspace();
  if (completed) {
    ws.closeOnComplete = null;
    $("workspace-dialog").close();
  }
}
// Tool views are scoped to their Room/operator/epoch and member generation.
// Late reads and mutations cannot populate another Room or Agent's settings.
function toolsCurrent(v) {
  return v === memberToolsView &&
    (!v.member || $("activity-dialog").open && activityMember?.membership_id === v.member.membership_id &&
      state.members.some(m => m.agent_id === v.member.agent_id && m.membership_id === v.member.membership_id)) &&
    state.catalog?.service_epoch === v.identity.service_epoch &&
    state.operator === v.identity.caller.id && state.info?.room_instance === v.identity.room_instance;
}
function toolsIdentity() {
  return { service_epoch: state.catalog.service_epoch, room_id: state.info.room_id,
    room_instance: state.info.room_instance, caller: { kind: "human", id: state.operator } };
}
function cliToolsFeedback(message, kind = "") {
  const feedback = $("cli-member-feedback");
  feedback.textContent = message;
  feedback.dataset.kind = kind;
}
async function openMemberTools(member) {
  $("cli-member-options").replaceChildren();
  cliToolsFeedback("");
  if (!state.managed || !state.info || !state.catalogConnected) {
    cliToolsFeedback("设置暂不可读，请恢复连接后重新读取。", "error"); return;
  }
  const v = memberToolsView = { identity: toolsIdentity(), member, cli: null, cliBusy: false };
  await loadCLITools(v);
}
// CLI bindings have a separate revision and never inherit legacy grants.
async function loadCLITools(v) {
  const read = v.cliRead = (v.cliRead || 0) + 1;
  cliToolsFeedback("正在读取 CLI 工具…");
  try {
    const q = new URLSearchParams({ service_epoch: v.identity.service_epoch, caller_id: v.identity.caller.id,
      room_id: v.identity.room_id, room_instance: v.identity.room_instance });
    const cq = new URLSearchParams({ service_epoch: v.identity.service_epoch, caller_id: v.identity.caller.id });
    const [data, connections] = await Promise.all([request(`/v1/tools/bindings?${q}`), request(`/v1/platform/connections?${cq}`)]);
    if (!toolsCurrent(v) || read !== v.cliRead) return;
    v.cli = data; v.cliConnections = connections.connections; v.cliBusy = false; v.cliResult = null;
    cliToolsFeedback(""); renderCLITools(v);
  } catch {
    if (!toolsCurrent(v) || read !== v.cliRead) return;
    v.cli = null; $("cli-member-options").replaceChildren();
    cliToolsFeedback("CLI 工具暂不可读，请重新读取。", "error");
  }
}
function renderCLITools(v) {
  const live = state.connected && state.catalogConnected && !discussionBusy();
  $("cli-member-options").replaceChildren(...v.cli.catalog.map(d => {
    const b = v.cli.bindings.find(b => b.agent_id === v.member.agent_id && b.membership_id === v.member.membership_id && b.tool === d.name);
    const row = document.createElement("div"); row.className = "cli-tool-row";
    const pending = v.cliPending?.tool === d.name;
    row.setAttribute("aria-busy", String(pending));
    const title = document.createElement("strong"); title.textContent = d.name;
    const version = document.createElement("small"); version.textContent = d.version; title.append(version);
    const label = document.createElement("label"); label.textContent = d.provider ? "连接账号与实例" : "工具状态";
    const select = document.createElement("select"); select.setAttribute("aria-label", `${d.name} ${label.textContent}`);
    const connections = v.cliConnections.filter(c => c.provider === d.provider && c.validation?.status === "valid");
    select.append(new Option("未启用", ""));
    if (d.provider) {
      select.append(...connections.map(c => new Option(`${c.name} · @${c.validation.username} · ${c.url}`, c.id)));
      if (b && !connections.some(c => c.id === b.connection_id)) select.append(new Option(`连接 ${b.connection_id} 待验证或不可用`, b.connection_id));
    } else select.append(new Option("启用", "on"));
    select.value = pending ? v.cliPending.value : b ? b.connection_id || "on" : "";
    select.disabled = v.cliBusy || !live; label.append(select);
    const apply = document.createElement("button"); apply.type = "button"; apply.className = "primary"; apply.textContent = pending ? "应用中…" : "应用";
    const feedback = document.createElement("p"); feedback.className = "cli-tool-feedback";
    feedback.setAttribute("role", "status"); feedback.setAttribute("aria-live", "polite");
    feedback.textContent = pending ? "正在保存工具设置…" : v.cliResult?.tool === d.name ? v.cliResult.message : "";
    feedback.dataset.kind = pending ? "pending" : "success";
    const valid = () => !select.value || !d.provider || connections.some(c => c.id === select.value);
    apply.disabled = v.cliBusy || !live || !valid();
    select.addEventListener("change", () => {
      apply.disabled = v.cliBusy || !live || !valid();
      if (v.cliResult?.tool === d.name) { v.cliResult = null; feedback.textContent = ""; }
    });
    apply.addEventListener("click", async () => {
      if (!toolsCurrent(v) || v.cliBusy || !live || !valid()) return;
      v.cliBusy = true; v.cliPending = { tool: d.name, value: select.value }; v.cliResult = null;
      v.cliRead = (v.cliRead || 0) + 1; cliToolsFeedback(""); renderCLITools(v);
      const selected = connections.find(c => c.id === select.value);
      const binding = { agent_id: v.member.agent_id, membership_id: v.member.membership_id, tool: d.name };
      if (selected) binding.connection_id = selected.id;
      try {
        const data = await request("/v1/tools/bind", { ...v.identity, revision: v.cli.revision, binding,
          enabled: Boolean(select.value), connection_version: selected?.version || 0 });
        if (!toolsCurrent(v)) return;
        v.cli = { ...v.cli, ...data }; v.cliBusy = false; v.cliPending = null;
        v.cliResult = { tool: d.name, message: !select.value ? `✓ ${d.name} 已停用。` : selected ?
          `✓ 已应用：${selected.name} · @${selected.validation.username}。沿用所选账号权限。` : `✓ ${d.name} 已启用。` };
        renderCLITools(v);
      } catch (error) {
        if (!toolsCurrent(v)) return;
        v.cli = null; v.cliBusy = false; v.cliPending = null; $("cli-member-options").replaceChildren();
        const rejected = { conflict: "工具设置或连接已变化，请重新读取后再应用。", identity: "房间或成员身份已变化，请重新打开工具设置。",
          request: "工具或连接不可用，请重新读取并选择有效连接。", capacity: "工具配置已达容量上限，请调整后再试。" };
        cliToolsFeedback(rejected[error.code] ? `应用失败：${rejected[error.code]}` :
          "应用结果未确认，不会自动重发。请重新读取后核对。", "error");
      }
    });
    row.append(title, label, apply, feedback); return row;
  }));
}
$("cli-member-refresh").addEventListener("click", () => {
  const v = memberToolsView; if (v && toolsCurrent(v) && !v.cliBusy) return loadCLITools(v);
});
const gitlabEventStatuses = { disabled: "已关闭", initializing: "初始化中", running: "运行中", backoff: "退避中", credentials_invalid: "凭据失效", capacity: "接收容量已满", binding_changed: "绑定已变化" };
const gitlabEventErrors = { credential_unavailable: "凭据不可用，请验证所选平台连接。", credential_rejected: "GitLab 拒绝访问，请检查权限和凭据。", binding_changed: "绑定已变化，请核对后保存设置。", project_identity_changed: "项目身份已变化，需要核查绑定。", capacity: "接收容量已满，进度未推进。", query_failed: "查询失败，将按退避计划重试。", room_capacity: "房间消息或投递回执容量已满，事件保留在积压中。" };
const notificationTargetErrors = { target_unavailable: "投递目标暂不可用，通知保留在来源积压中。", target_removed: "接收成员已移除，旧积压保留原目标；同名新成员不会继承。", inbox_capacity: "成员收件箱容量已满，新通知保留在来源积压中。" };
function notificationTarget(config) { const t = config?.target; return t?.mode === "inbox" ? { mode: "inbox", agent_id: t.agent_id, membership_id: t.membership_id } : {}; }
function fillNotificationTarget(channel, target) {
  const value = JSON.stringify(target || {}), options = [new Option("房间消息（公开）", "{}"), ...state.members.map(m => new Option(`${memberRole(m)?.name || m.agent_id} · 收件箱`, JSON.stringify({mode:"inbox", agent_id:m.agent_id, membership_id:m.membership_id})))];
  if (!options.some(o => o.value === value)) options.push(new Option("原接收成员已失效", value));
  setNotificationOptions(`gitlab-${channel}-target`, options); $(`gitlab-${channel}-target`).value = value;
}
function draftNotificationTarget(channel) { try { return JSON.parse($(`gitlab-${channel}-target`).value || "{}"); } catch { return {}; } }
const gitlabEventTypeIDs = ["opened", "merged", "closed", "note", "push"];
const gitlabEventTypeInputs = () => gitlabEventTypeIDs.map(id => $(`gitlab-events-type-${id}`));
function gitlabEventDraft() {
  const types = gitlabEventTypeInputs().filter(input => input.checked).map(input => input.value);
  const lines = id => $(id).value.split(/\r?\n/).map(value => value.trim()).filter(Boolean);
  return { target: draftNotificationTarget("events"), connection_id: $("gitlab-events-connection").value, project: $("gitlab-events-project").value.trim(),
    enabled: $("gitlab-events-enabled").checked, interval_seconds: Number($("gitlab-events-interval").value),
    etag_verified: workspaceView?.events?.config.etag_verified || false,
    filter: { types: types.length === gitlabEventTypeIDs.length ? [] : types, branches: lines("gitlab-events-branches"), ignore_users: lines("gitlab-events-ignore") } };
}
function gitlabEventConfigKey(config) {
  const unique = values => [...new Set(values || [])].sort();
  const types = unique(config.filter.types);
  return JSON.stringify({ target: notificationTarget(config), connection: config.connection_id || "", project: config.project || "", enabled: config.enabled, interval: config.interval_seconds,
    types: types.length === gitlabEventTypeIDs.length ? [] : types, branches: unique(config.filter.branches),
    users: unique((config.filter.ignore_users || []).map(user => user.toLowerCase())) });
}
function gitlabEventDraftState(data) {
  const config = gitlabEventDraft();
  const saved = { ...data.config, connection_id: data.config.connection_id || data.project?.connection_id || "", project: data.config.project || data.project?.project || "" };
  const connection = workspaceView?.connections?.find(c => c.id === config.connection_id);
  const identityChanged = !!(config.enabled && data.subscription_id && data.recipient_id > 0 && todoVerified(connection) &&
    (data.recipient_id !== connection.validation.user_id || data.instance !== connection.url));
  const targetChanged = config.connection_id !== saved.connection_id || config.project !== saved.project || identityChanged;
  const targetInvalid = config.enabled && (!data.subscription_version || targetChanged) && (!todoVerified(connection) || !config.project);
  const count = gitlabEventTypeInputs().filter(input => input.checked).length;
  return { config, count, dirty: count === 0 || identityChanged || gitlabEventConfigKey(config) !== gitlabEventConfigKey(saved), targetChanged, targetInvalid,
    invalid: targetInvalid || config.enabled && (count === 0 || !Number.isInteger(config.interval_seconds) || config.interval_seconds < 3 || config.interval_seconds > 30) };
}
async function syncGitLabNotifications(catalog, ws) {
  if (ws !== workspaceView) return;
  const revisions = { events: ws.eventsRevision, todos: ws.todosRevision, messages: ws.messagesRevision };
  const read = ws.notificationsRead = (ws.notificationsRead || 0) + 1;
  try {
    const params = new URLSearchParams({ service_epoch: catalog.service_epoch, caller_id: ws.operator, ...ws.room });
    const data = await request(`/v1/platform/notifications?${params}`);
    if (ws !== workspaceView || state.catalog.service_epoch !== catalog.service_epoch || read !== ws.notificationsRead) return;
    for (const channel of ["events", "todos", "messages"]) {
      if (ws[`${channel}Sending`] || ws[`${channel}Revision`] !== revisions[channel]) continue;
      const next = data.subscriptions.find(source => source.provider === (channel === "messages" ? "lark" : "gitlab") && source.channel === channel);
      if (channel === "todos" && next && ws.todosDraft && ws.todos && todoConfigKey(ws.todosDraft) === todoConfigKey(ws.todos.config)) {
        ws.todosDraft = todoConfig(next.config); fillGitLabTodos(ws.todosDraft);
      }
      ws[channel] = next;
      ws[`${channel}Connected`] = !!ws[channel];
    }
  } catch {
    if (ws !== workspaceView || state.catalog?.service_epoch !== catalog.service_epoch || read !== ws.notificationsRead) return;
    for (const channel of ["events", "todos", "messages"]) if (!ws[`${channel}Sending`] && ws[`${channel}Revision`] === revisions[channel]) ws[`${channel}Connected`] = false;
  }
}
function larkConfigKey(c = {}) {return JSON.stringify({scope_mode:c.scope_mode || "specified",enabled:!!c.enabled,connection_id:c.connection_id || "",chats:[...(c.chats || [])].sort(),target:notificationTarget(c),private:!!c.private,group_mentions:!!c.group_mentions,at_all:!!c.at_all,bots:!!c.bots,interval_seconds:c.interval_seconds || 60,backfill_seconds:c.backfill_seconds || 0});}
function larkMessagesDraft() {
  const data=workspaceView?.messages;
  return {scope_mode:$("lark-scope").value || "specified",enabled:$("lark-enabled").checked, connection_id:$("lark-connection").value,
    target:JSON.parse($("lark-target").value || "{}"), chats:$("lark-scope").value==="all" ? [] : $("lark-chats").value.split(/\s+/).filter(Boolean).sort(),
    private:$("lark-private").checked, group_mentions:$("lark-group").checked, at_all:$("lark-all").checked, bots:$("lark-bots").checked,
    interval_seconds:Number($("lark-interval").value), backfill_seconds:Number($("lark-backfill").value || data?.config.backfill_seconds || 0)};
}
function renderLarkMessages() {
  const ws=workspaceView,data=ws?.messages;
  $("lark-fields").disabled=!data || !!ws?.messagesSending || managementBusy();
  $("lark-save").disabled=true;$("lark-sync").disabled=true;
  if(!data) return;
  const fresh=ws.larkFilled !== data.subscription_id+":"+data.subscription_version && !ws.larkDirty;
  const c=fresh ? data.config : larkMessagesDraft();
  const connections=ws.larkConnections || [];
  const options=[new Option("请选择已验证连接", ""), ...connections.map(x=>new Option(`${x.name} · ${x.validation?.username || "待验证"}`,x.id))];
  if(c.connection_id && !connections.some(x=>x.id===c.connection_id))options.push(new Option("原连接暂不可用",c.connection_id));
  setNotificationOptions("lark-connection", options);$("lark-connection").value=c.connection_id || "";
  const target=JSON.stringify(c.target?.mode === "inbox" ? c.target : {});
  const targets=[new Option("请选择接收成员", "{}"), ...state.members.map(x=>new Option(`${memberRole(x)?.name || x.agent_id} · 收件箱`,JSON.stringify({mode:"inbox",agent_id:x.agent_id,membership_id:x.membership_id})))];
  if(target!=="{}"&&!targets.some(x=>x.value===target))targets.push(new Option("原接收成员已移除",target));
  setNotificationOptions("lark-target",targets);$("lark-target").value=target;
  if(fresh){
    $("lark-scope").value=c.scope_mode || "specified";$("lark-enabled").checked=!!c.enabled;$("lark-chats").value=(c.chats || []).join("\n");
    $("lark-private").checked=!!c.private;$("lark-group").checked=!!c.group_mentions;$("lark-all").checked=!!c.at_all;$("lark-bots").checked=!!c.bots;
    $("lark-interval").value=c.interval_seconds || 60;$("lark-backfill").value=String(c.backfill_seconds || 0);
    ws.larkFilled=data.subscription_id+":"+data.subscription_version;
  }
  $("lark-backfill").disabled=!!data.subscription_id;$("lark-chats").disabled=$("lark-scope").value==="all";
  const draft=larkMessagesDraft(), dirty=larkConfigKey(draft)!==larkConfigKey(data.config), connection=connections.find(x=>x.id===draft.connection_id);
  const valid=(!draft.enabled || connection?.validation?.status==="valid") && draft.target.mode==="inbox" && (draft.scope_mode==="all" || draft.chats.length>0 && draft.chats.length<=32 && draft.chats.every(x=>/^oc_[A-Za-z0-9_-]+$/.test(x))) && draft.interval_seconds>=3 && draft.interval_seconds<=3600;
  $("lark-save").disabled=!ws.messagesConnected||!!ws.messagesSending||managementBusy()||!valid||(!dirty && !["credentials_invalid","binding_changed","capacity"].includes(data.status))||(!data.subscription_id && !draft.enabled);
  $("lark-sync").disabled=!ws.messagesConnected||!!ws.messagesSending||managementBusy()||dirty||!data.config.enabled;
  $("lark-state").textContent=gitlabEventStatuses[data.status] || data.status;
  $("lark-account").textContent=connection?.validation?.status==="valid" ? `读取账号：${connection.validation.username} · ${connection.validation.account?.id?.value || ""}` : "请选择连接并先验证用户身份。";
  $("lark-feedback").textContent=ws.messagesSending ? "正在处理…" : ws.messagesFeedback || (dirty ? "有尚未保存的设置。" : "");
  const errors={...connectionValidationErrors,...notificationTargetErrors,capacity:"来源容量已满，进度保留；释放容量后显式同步。",binding_changed:"连接身份已变化；旧订阅保留，请核对原账号。",identity_mismatch:"账号已变化，进度保留；核对原账号并重新验证连接后同步。",credential_rejected:"登录已失效，进度保留；请在原 profile 完成用户授权、验证连接后同步。",permission_denied:"读取权限不足，进度保留；请检查用户授权范围，验证连接后同步。",cursor_expired:"游标已过期，将在同一时间窗重扫。",query_failed:"读取失败，将退避重试。"};
  notice("lark-error",ws.messagesError || (!ws.messagesConnected ? "通知状态暂不可读。" : errors[data.delivery_error_code] || errors[data.error_code] || data.error_code || ""));
  const d=data.lark || {}, date=x=>x?new Date(x).toLocaleString("zh-CN"):"尚无";
  $("lark-diagnostics").textContent=`目录 ${d.directory_complete ? "完整" : "未完成"}${d.directory_scanning ? `（${d.directory_pages || 0} 页）` : ""} · 目录最近完成 ${date(d.directory_last_full)} · 读取受阻 ${d.failed_chats || 0} · 不在最新目录 ${d.unlisted_chats || 0} · 积压超过 24 小时 ${d.gap_chats || 0} · 最近检查 ${date(data.last_check)} · 全部会话检查上界 ${date(data.last_full)} · 待扫描 ${d.unchecked || 0} · 最久落后 ${d.oldest_lag_seconds || 0} 秒 · 来源积压 ${data.pending || 0} · 重试 ${date(data.retry_at)} · CLI ${d.cli_version || ""}`;
  $("lark-chat-status").replaceChildren(...(d.chats || []).map(x=>{const li=document.createElement("li");li.textContent=`${x.name || x.chat_id} · ${x.mode || "待核验"} · ${x.error_code ? `读取受阻 ${x.error_code}，重试 ${date(x.retry_at)}` : x.scan ? `分页未完成（${x.scan.pages} 页）` : x.completed ? "窗口完成" : "待扫描"} · 最近完成 ${date(x.last_check)}`;return li;}));
}
async function changeLarkMessages(action) {
  const ws=workspaceView;if(!ws?.messages || ws.messagesSending || $(action==="configure"?"lark-save":"lark-sync").disabled)return;
  const payload={service_epoch:state.catalog.service_epoch,caller:{kind:"human",id:ws.operator},...ws.room,provider:"lark",channel:"messages",subscription_id:ws.messages.subscription_id,subscription_version:ws.messages.subscription_version};
  if(action==="configure")payload.config=larkMessagesDraft();
  ws.messagesRevision=(ws.messagesRevision||0)+1;ws.messagesSending=action;ws.messagesError="";renderLarkMessages();
  try{
    const data=await request(`/v1/platform/notifications/${action}`,payload);
    if(ws!==workspaceView)return;const replaced=ws.messages.subscription_id && ws.messages.subscription_id!==data.subscription_id;ws.messages=data;ws.messagesConnected=true;ws.larkDirty=false;ws.larkFilled=null;ws.messagesFeedback=replaced ? "已为新连接开启订阅，旧订阅及积压保留并暂停。" : "已保存。积压保留原接收成员。";
  }catch(e){if(ws===workspaceView)ws.messagesError=e.code==="conflict"?"设置或账号已变化，请重新读取后核对。":"结果未确认，请重新读取状态；不会自动重发。";}
  finally{if(ws===workspaceView){ws.messagesRevision++;ws.messagesSending=false;renderGitLabEvents();}}
}
for(const id of ["enabled","connection","target","scope","chats","private","group","all","bots","interval","backfill"]) $("lark-"+id).addEventListener("change",()=>{if(workspaceView)workspaceView.larkDirty=true;renderLarkMessages();});
$("lark-save").addEventListener("click",()=>changeLarkMessages("configure"));
$("lark-sync").addEventListener("click",()=>changeLarkMessages("sync"));
$("lark-manage-connections").addEventListener("click",async()=>{showConnectionList();$("settings-dialog").showModal();await loadConnections();});
function renderGitLabEvents() {
  renderNotificationSource();
  renderGitLabTodos();
  renderLarkMessages();
  const ws = workspaceView, data = ws?.events;
  $("gitlab-events").hidden = !data || !data.available && !data.subscription_version;
  if (!data) { $("gitlab-events-fields").disabled = true; return; }
  const formVersion = JSON.stringify([data.subscription_id, data.subscription_version]);
  if (ws.eventsFormVersion !== formVersion && !ws.eventsSending) {
    fillNotificationTarget("events", notificationTarget(data.config));
    ws.eventsConnection = data.config.connection_id || data.project?.connection_id || "";
    $("gitlab-events-project").value = data.config.project || data.project?.project || "";
    $("gitlab-events-enabled").checked = data.config.enabled;
    $("gitlab-events-interval").value = data.config.interval_seconds;
    for (const input of gitlabEventTypeInputs()) input.checked = !data.config.filter.types?.length || data.config.filter.types.includes(input.value);
    $("gitlab-events-branches").value = (data.config.filter.branches || []).join("\n");
    $("gitlab-events-ignore").value = (data.config.filter.ignore_users || []).join("\n");
    ws.eventsFormVersion = formVersion;
  }
  fillNotificationTarget("events", draftNotificationTarget("events"));
  const options = [new Option("请选择平台连接", ""), ...(ws.connections || []).map(item => new Option(`${item.name} · ${item.url}`, item.id))];
  if (ws.eventsConnection && !ws.connections?.some(c => c.id === ws.eventsConnection)) options.push(new Option("原连接暂不可用", ws.eventsConnection));
  setNotificationOptions("gitlab-events-connection", options); $("gitlab-events-connection").value = ws.eventsConnection || "";
  const connection = ws.connections?.find(c => c.id === ws.eventsConnection);
  $("gitlab-events-account").textContent = todoVerified(connection) ? `接收账号：@${connection.validation.username}（ID ${connection.validation.user_id}，已验证）` : ws.eventsConnection ? "接收账号：请先验证平台连接" : ws.connectionsError || "请选择平台连接";
  const busy = !!(ws.eventsSending || !ws.eventsConnected || managementBusy());
  const draft = gitlabEventDraftState(data);
  $("gitlab-events-all-types").checked = draft.count === gitlabEventTypeIDs.length;
  $("gitlab-events-all-types").indeterminate = draft.count > 0 && draft.count < gitlabEventTypeIDs.length;
  $("gitlab-events-fields").disabled = busy;
  $("gitlab-events-enabled").disabled = !data.available && !data.config.enabled;
  $("gitlab-events-save").disabled = busy || !draft.dirty || draft.invalid || !data.subscription_version && !draft.config.enabled;
  $("gitlab-events-save").textContent = ws.eventsSending === "configure" ? "保存中…"
    : draft.config.enabled !== data.config.enabled ? draft.config.enabled ? "开启监听" : "关闭监听"
    : draft.dirty ? "保存更改" : data.subscription_version ? "已保存" : "开启监听";
  $("gitlab-events-sync").hidden = !data.config.enabled;
  $("gitlab-events-sync").disabled = busy || draft.dirty || !data.config.enabled;
  const retry = data.error_code || data.delivery_error_code || ["backoff", "credentials_invalid", "capacity", "binding_changed"].includes(data.status);
  $("gitlab-events-sync-label").textContent = ws.eventsSending === "sync" ? "请求中…" : retry ? "重试同步" : "立即同步";
  $("gitlab-events-state").textContent = gitlabEventStatuses[data.status] || data.status;
  $("gitlab-events-feedback").textContent = ws.eventsSending ? ws.eventsSending === "configure" ? "正在保存设置…" : "正在请求同步…"
    : draft.invalid ? draft.targetInvalid ? "请选择已验证连接并填写项目" : draft.count === 0 ? "请至少选择一种活动" : "检查周期须为 3～30 秒"
    : draft.dirty ? "有未保存的更改" : ws.eventsFeedback || (data.subscription_version ? "设置已保存" : "尚未开启");
  $("gitlab-events-feedback").setAttribute("data-pending", String(draft.dirty));
  $("gitlab-events-action-hint").textContent = draft.config.enabled && draft.count === 0 ? "至少勾选一种活动；停止接收请取消「接收 GitLab 活动」。"
    : draft.dirty && draft.targetChanged && data.subscription_id ? "更换连接、账号或项目将新建订阅，从保存时刻接收；旧进度与积压保留在旧订阅。"
    : draft.dirty ? "保存后生效；立即同步使用已保存的设置。"
    : !data.config.enabled ? "开启后接收新活动；关闭时保留进度和积压。"
    : retry ? "修复认证或错误原因后，点击重试同步。"
    : "已按检查周期自动接收活动；立即同步可提前检查并补查遗漏。";
  const date = value => value ? new Date(value).toLocaleString("zh-CN") : "尚无";
  $("gitlab-events-status").textContent = `${gitlabEventStatuses[data.status] || data.status} · 待投递 ${data.pending} · 最近检查 ${date(data.last_check)} · 完整补查 ${date(data.last_full)}` +
    (data.retry_at ? ` · 下次重试 ${date(data.retry_at)}` : "") + (data.gap ? " · 长时间离线区间无法保证补齐" : "") +
    ` · 请求 ${data.metrics.calls} 次 / ${data.metrics.response_bytes} 字节`;
  notice("gitlab-events-error", ws.eventsError || (!ws.eventsConnected ? "事件状态暂不可读，正在重连。" : notificationTargetErrors[data.delivery_error_code] || gitlabEventErrors[data.delivery_error_code] || gitlabEventErrors[data.error_code] || ""));
}
async function changeGitLabEvents(action) {
  const ws = workspaceView;
  if (!ws?.events || ws.eventsSending || !ws.eventsConnected || managementBusy()) return;
  const payload = { service_epoch: state.catalog.service_epoch, caller: { kind: "human", id: ws.operator }, ...ws.room, provider: "gitlab", channel: "events", subscription_id: ws.events.subscription_id, subscription_version: ws.events.subscription_version };
  const draft = gitlabEventDraftState(ws.events);
  if (action === "configure") {
    if (!draft.dirty || draft.invalid) { renderGitLabEvents(); return; }
    payload.config = draft.config;
  } else if (draft.dirty || !ws.events.config.enabled) {
    renderGitLabEvents(); return;
  }
  ws.eventsRevision = (ws.eventsRevision || 0) + 1;
  ws.eventsSending = action; ws.eventsError = ""; ws.eventsFeedback = ""; renderGitLabEvents();
  try {
    const data = await request(`/v1/platform/notifications/${action}`, payload);
    if (ws !== workspaceView || state.catalog?.service_epoch !== payload.service_epoch) return;
    ws.events = data;
    ws.eventsFormVersion = undefined;
    ws.eventsFeedback = action === "sync" ? "已发起同步，结果将自动更新" : data.config.enabled ? "设置已保存，正在接收活动" : "监听已关闭";
  } catch (error) {
    if (ws !== workspaceView || state.catalog?.service_epoch !== payload.service_epoch) return;
    ws.eventsError = error.code === "request" ? "活动设置无效，请检查连接验证、项目访问权限、周期及过滤内容。" : error.code === "conflict" ? "设置版本已变化，请核对最新设置后重新保存。" : "设置结果待核查，请查看最新状态后显式重试；不会自动重发。";
    ws.eventsSending = false;
    await syncGitLabNotifications(state.catalog, ws);
  } finally {
    if (ws === workspaceView) { ws.eventsRevision++; ws.eventsSending = false; renderGitLabEvents(); }
  }
}
function editGitLabEvents(event) {
  if (workspaceView && event?.target === $("gitlab-events-connection")) workspaceView.eventsConnection = $("gitlab-events-connection").value;
  if (workspaceView) { workspaceView.eventsFeedback = ""; workspaceView.eventsError = ""; }
  renderGitLabEvents();
}
for (const id of ["gitlab-events-connection", "gitlab-events-project", "gitlab-events-enabled", "gitlab-events-interval", "gitlab-events-branches", "gitlab-events-ignore", ...gitlabEventTypeIDs.map(id => `gitlab-events-type-${id}`)]) {
  $(id).addEventListener("input", editGitLabEvents);
  $(id).addEventListener("change", editGitLabEvents);
}
$("gitlab-events-all-types").addEventListener("change", () => {
  for (const input of gitlabEventTypeInputs()) input.checked = $("gitlab-events-all-types").checked;
  editGitLabEvents();
});
$("gitlab-events-save").addEventListener("click", () => changeGitLabEvents("configure"));
$("gitlab-events-sync").addEventListener("click", () => changeGitLabEvents("sync"));
const gitlabTodoReasons = ["mentioned", "assigned", "review_requested", "other"];
const gitlabTodoTypes = ["merge_request", "work_item"];
const gitlabTodoErrors = { ...gitlabEventErrors, credential_unavailable: "个人连接凭据不可用，请在平台连接中重新验证。", binding_changed: "账号或当前项目绑定已变化，请核对后显式保存。", scan_incomplete: "补查尚未完成，将按检查周期继续；完整同步时间尚未更新。" };
const gitlabTodoSkipReasons = { no_project: "无项目归属", unsupported_target: "不支持的对象类型", target_unavailable: "对象暂不可用", untrusted_url: "链接无法核实", project: "项目范围不匹配", reason: "原因未勾选", type: "类型未勾选", before_baseline: "首次开启前创建" };
function setNotificationOptions(id, options) {
  const select = $(id), signature = JSON.stringify(options.map(option => [option.value, option.textContent, !!option.disabled]));
  if (select.dataset.options !== signature) { select.replaceChildren(...options); select.dataset.options = signature; }
}
function todoConfig(config) {
  return { target: notificationTarget(config), enabled: !!config.enabled, connection_id: config.connection_id || "", project_scope: config.project_scope || "all",
    interval_seconds: config.interval_seconds ?? 30, inactive_interval_seconds: config.inactive_interval_seconds ?? 120,
    filter: { reasons: [...(config.filter?.reasons ?? gitlabTodoReasons)], types: [...(config.filter?.types ?? gitlabTodoTypes)] } };
}
function todoConfigKey(config) {
  const c = todoConfig(config);
  c.filter.reasons.sort(); c.filter.types.sort();
  return JSON.stringify(c);
}
function todoConnection(ws, config) { return ws.connections?.find(item => item.id === config.connection_id); }
function todoVerified(connection) { return connection?.validation?.status === "valid" && connection.validation.user_id > 0; }
function todoCurrentProject(ws, config) {
  const binding = ws.data?.workspace?.binding, connection = todoConnection(ws, config);
  return ws.connected && binding?.project_id > 0 && connection?.url === binding.url ? binding : null;
}
function todoProjectChanged(ws) {
  const data = ws.todos;
  if (!data?.config.enabled || data.config.project_scope !== "current") return false;
  if (!ws.connected || !ws.connections) return data.status === "binding_changed";
  const current = todoCurrentProject(ws, data.config);
  const key = p => JSON.stringify([p?.url, p?.project_id, p?.project, p?.connection_id, p?.connection_generation]);
  return data.status === "binding_changed" || !!data.project && key(data.project) !== key(current);
}
function fillGitLabTodos(config) {
  fillNotificationTarget("todos", notificationTarget(config));
  $("gitlab-todos-enabled").checked = config.enabled;
  $("gitlab-todos-interval").value = config.interval_seconds;
  $("gitlab-todos-inactive-interval").value = config.inactive_interval_seconds;
  for (const [group, values] of [["reason", gitlabTodoReasons], ["type", gitlabTodoTypes]])
    for (const value of values) $(`gitlab-todos-${group}-${value}`).checked = config.filter[group === "reason" ? "reasons" : "types"].includes(value);
}
function renderGitLabTodos() {
  const ws = workspaceView, data = ws?.todos;
  $("gitlab-todos-fields").disabled = !data || !!ws.todosSending || !ws.todosConnected || managementBusy();
  $("gitlab-todos-save").disabled = true; $("gitlab-todos-sync").disabled = true;
  $("gitlab-todos-manage-connections").disabled = !ws;
  if (!data) {
    fillGitLabTodos(todoConfig({}));
    setNotificationOptions("gitlab-todos-connection", [new Option("正在读取…", "")]);
    setNotificationOptions("gitlab-todos-scope", [new Option("所有可访问项目", "all")]);
    for (const id of ["recipient", "project", "status", "feedback", "action-hint", "connection-notice"]) $(`gitlab-todos-${id}`).textContent = "";
    $("gitlab-todos-state").textContent = "状态不可读";
    notice("gitlab-todos-error", "待办状态暂不可读，请重新读取。");
    return;
  }
  if (!ws.todosDraft) { ws.todosDraft = todoConfig(data.config); fillGitLabTodos(ws.todosDraft); }
  const config = ws.todosDraft;
  fillNotificationTarget("todos", notificationTarget(config));
  const options = [new Option("请选择个人连接", ""), ...(ws.connections || []).map(item => new Option(`${item.name} · ${item.url}`, item.id))];
  if (config.connection_id && !todoConnection(ws, config)) options.push(new Option("原连接暂不可用", config.connection_id));
  setNotificationOptions("gitlab-todos-connection", options); $("gitlab-todos-connection").value = config.connection_id;
  const connection = todoConnection(ws, config), project = todoCurrentProject(ws, config);
  $("gitlab-todos-recipient").textContent = todoVerified(connection) ? `接收账号：@${connection.validation.username}（ID ${connection.validation.user_id}，已验证）`
    : config.connection_id ? "接收账号：需重新验证个人连接" : "接收账号：请先选择个人连接";
  const current = new Option(project ? "当前绑定项目" : "当前绑定项目（同实例有效绑定不可用）", "current");
  current.disabled = !project;
  setNotificationOptions("gitlab-todos-scope", [current, new Option("所有可访问项目", "all")]); $("gitlab-todos-scope").value = config.project_scope;
  const changed = todoProjectChanged(ws);
  $("gitlab-todos-project").textContent = project ? `当前项目：${project.project} · #${project.project_id}` : "无同实例项目绑定时，请选择所有可访问项目。";
  if (changed) $("gitlab-todos-project").textContent += ` 原订阅项目：${data.project?.project || "绑定已变化"}。请核对范围后保存；立即同步不能跟随新项目。`;
  const switching = data.subscription_id && todoVerified(connection) && (data.recipient_id !== connection.validation.user_id || data.instance !== connection.url);
  const dirty = todoConfigKey(config) !== todoConfigKey(data.config) || changed || !!switching && config.enabled;
  const invalid = !Number.isInteger(config.interval_seconds) || config.interval_seconds < 3 || config.interval_seconds > 3600 ||
    !Number.isInteger(config.inactive_interval_seconds) || config.inactive_interval_seconds < config.interval_seconds || config.inactive_interval_seconds > 86400;
  const busy = !!ws.todosSending || !ws.todosConnected || managementBusy();
  const unavailable = config.enabled && (!todoVerified(connection) || config.project_scope === "current" && !project);
  $("gitlab-todos-save").disabled = busy || !dirty || invalid || unavailable || !data.subscription_id && !config.enabled;
  $("gitlab-todos-save").textContent = ws.todosSending === "configure" ? "保存中…" : !config.enabled && data.config.enabled ? "关闭监听"
    : switching && config.enabled ? "为此账号重新开启" : changed ? "核对并保存" : !data.config.enabled && config.enabled ? "开启监听" : dirty ? "保存更改" : data.subscription_id ? "已保存" : "开启监听";
  $("gitlab-todos-sync").hidden = !data.config.enabled;
  $("gitlab-todos-sync").disabled = busy || dirty || unavailable || !data.config.enabled;
  $("gitlab-todos-sync").textContent = ws.todosSending === "sync" ? "请求中…" : data.error_code || data.delivery_error_code ? "重试同步" : "立即同步";
  $("gitlab-todos-state").textContent = gitlabEventStatuses[data.status] || data.status;
  $("gitlab-todos-feedback").textContent = ws.todosSending ? ws.todosSending === "configure" ? "正在保存设置…" : "正在请求同步…"
    : invalid ? "请检查房间可接收与暂存时的检查周期" : dirty ? "有未保存的更改" : ws.todosFeedback || (data.subscription_id ? "设置已保存" : "尚未开启");
  $("gitlab-todos-feedback").setAttribute("data-pending", String(dirty));
  $("gitlab-todos-action-hint").textContent = switching && config.enabled ? "保存将停用旧账号订阅，并为新账号建立新进度；旧消息和回执保留。"
    : dirty ? "保存后生效；立即同步只使用已保存配置。" : "关闭保留进度和积压；凭据重新验证后，需显式同步恢复。";
  if (!config.filter.reasons.length || !config.filter.types.length) $("gitlab-todos-action-hint").textContent += " 当前过滤为空，不接收匹配项。";
  $("gitlab-todos-connection-notice").textContent = ws.connectionsError || (!todoVerified(connection) ? "在平台连接中添加或重新验证账号后，返回此处选择。" : "个人连接仅用于待办查询。");
  const date = value => value ? new Date(value).toLocaleString("zh-CN") : "尚无";
  const skipped = Object.entries(data.skipped || {}).map(([reason, count]) => `${gitlabTodoSkipReasons[reason] || "其他原因"} ${count}`).join("、");
  $("gitlab-todos-status").textContent = `当前订阅：${data.subscription_id || "尚未开启"} · 接收账号 ${data.username ? `@${data.username}（ID ${data.recipient_id}）` : "尚无"} · 待投递 ${data.pending || 0} · 最近检查 ${date(data.last_check)} · 完整同步 ${date(data.last_full)}` +
    (data.retry_at ? ` · 下次重试 ${date(data.retry_at)}` : "") + (data.scan ? ` · ${data.scan.state === "done" ? "已完成待办" : "待处理待办"}第 ${data.scan.next_page} 页${data.scan.limited ? "（补查未完成）" : ""}` : "") +
    ` · 请求 ${data.metrics?.calls || 0} 次 / ${data.metrics?.response_bytes || 0} 字节` + (skipped ? ` · 跳过：${skipped}` : "");
  notice("gitlab-todos-error", ws.todosError || (!ws.todosConnected ? "待办状态暂不可读，正在重连。" : notificationTargetErrors[data.delivery_error_code] || gitlabTodoErrors[data.delivery_error_code] || gitlabTodoErrors[data.error_code] || ""));
}
function editGitLabTodos(event) {
  const ws = workspaceView;
  if (!ws?.todosDraft) return;
  const previous = ws.todosDraft;
  const config = { target: draftNotificationTarget("todos"), enabled: $("gitlab-todos-enabled").checked, connection_id: $("gitlab-todos-connection").value, project_scope: $("gitlab-todos-scope").value,
    interval_seconds: Number($("gitlab-todos-interval").value), inactive_interval_seconds: Number($("gitlab-todos-inactive-interval").value),
    filter: { reasons: gitlabTodoReasons.filter(value => $(`gitlab-todos-reason-${value}`).checked), types: gitlabTodoTypes.filter(value => $(`gitlab-todos-type-${value}`).checked) } };
  if (event?.target === $("gitlab-todos-connection") && !ws.todos.subscription_id && previous.connection_id !== config.connection_id)
    config.project_scope = todoCurrentProject(ws, config) ? "current" : "all";
  ws.todosDraft = config; ws.todosEdit = (ws.todosEdit || 0) + 1; ws.todosError = ""; ws.todosFeedback = "";
  renderGitLabTodos();
}
async function changeGitLabTodos(action) {
  const ws = workspaceView;
  if (!ws?.todos || ws.todosSending || !ws.todosConnected || managementBusy()) return;
  renderGitLabTodos();
  if ($(action === "configure" ? "gitlab-todos-save" : "gitlab-todos-sync").disabled) return;
  const edit = ws.todosEdit, payload = { service_epoch: state.catalog.service_epoch, caller: { kind: "human", id: ws.operator }, ...ws.room,
    provider: "gitlab", channel: "todos", subscription_id: ws.todos.subscription_id, subscription_version: ws.todos.subscription_version };
  if (action === "configure") payload.config = structuredClone(ws.todosDraft);
  ws.todosRevision = (ws.todosRevision || 0) + 1;
  ws.todosSending = action; ws.todosError = ""; ws.todosFeedback = ""; renderGitLabTodos();
  try {
    const data = await request(`/v1/platform/notifications/${action}`, payload);
    if (ws !== workspaceView || state.catalog?.service_epoch !== payload.service_epoch) return;
    ws.todos = data;
    if (edit === ws.todosEdit) {
      ws.todosDraft = todoConfig(data.config); fillGitLabTodos(ws.todosDraft);
      ws.todosFeedback = action === "sync" ? "已发起同步，结果将自动更新" : data.config.enabled ? "设置已保存，正在接收待办" : "监听已关闭";
    }
  } catch (error) {
    if (ws !== workspaceView || state.catalog?.service_epoch !== payload.service_epoch) return;
    if (edit === ws.todosEdit) ws.todosError = error.code === "conflict" ? "订阅、账号或项目已变化，请核对最新状态后显式保存。"
      : error.code === "request" ? "待办设置无效，请检查个人连接、范围与周期。" : "设置结果待核查，请读取最新状态后显式重试；不会自动重发。";
    // A failed mutation may have committed. Reconcile by reading, never replay it.
    ws.todosSending = false;
    await syncGitLabNotifications(state.catalog, ws);
  } finally {
    if (ws === workspaceView) { ws.todosRevision++; ws.todosSending = false; renderGitLabEvents(); }
  }
}
for (const id of ["target", "enabled", "connection", "scope", "interval", "inactive-interval", ...gitlabTodoReasons.map(value => `reason-${value}`), ...gitlabTodoTypes.map(value => `type-${value}`)]) {
  for (const event of ["input", "change"]) $(`gitlab-todos-${id}`).addEventListener(event, editGitLabTodos);
}
$("gitlab-todos-save").addEventListener("click", () => changeGitLabTodos("configure"));
$("gitlab-todos-sync").addEventListener("click", () => changeGitLabTodos("sync"));
$("gitlab-todos-manage-connections").addEventListener("click", async () => {
  showConnectionList(); $("settings-dialog").showModal(); await loadConnections();
});

$("gitlab-events-target").addEventListener("change", renderGitLabEvents);
let notificationFocus = null;
function renderContextViews() {
  const detail = $("activity-dialog").open || $("notification-detail").open;
  $("open-gitlab-source").setAttribute("aria-expanded", String($("notification-detail").open));
  $("member-list-view").hidden = detail;
  $("member-list-view").inert = detail || !panels.members;
  $("member-list-view").setAttribute("aria-hidden", String(detail || !panels.members));
}
function renderNotificationSource() {
  const ws = workspaceView, data = ws?.events, binding = data?.project;
  const available = !!ws;
  const channel = ws?.notificationChannel || "events";
  const label = (key) => ws?.[`${key}Connected`] ? gitlabEventStatuses[ws[key]?.status] || "已关闭" : "状态不可读";
  $("notification-summary").textContent = `项目活动：${label("events")} · 我的待办：${label("todos")} · 飞书：${label("messages")}`;
  for (const key of ["events", "todos", "messages"]) {
    $(`notification-panel-${key}`).hidden = key !== channel;
    $(`notification-tab-${key}`).setAttribute("aria-selected", String(key === channel));
    $(`notification-tab-${key}`).setAttribute("tabindex", key === channel ? "0" : "-1");
  }
  $("notification-sources").hidden = !available;
  $("notification-source-count").textContent = available ? "2" : "0";
  $("lark-source-state").textContent = label("messages");
  $("notification-source-state").textContent = !ws?.eventsConnected && !ws?.todosConnected ? "状态不可读" : data?.config.enabled && ws?.todos?.config.enabled ? "两路已开启" : ws?.todos?.config.enabled ? `待办 · ${label("todos")}` : label("events");
  const connection = ws?.connections?.find(item => item.id === binding?.connection_id);
  $("notification-connection").textContent = !binding ? "尚未绑定" : binding.connection_id
    ? `${connection?.name || "共享连接"} · ${binding.url}` : `旧认证（尚未迁移） · ${binding.url}`;
  $("notification-project").textContent = binding ? `${binding.project}${binding.project_id ? ` · #${binding.project_id}` : ""}` : "尚未绑定项目";
  $("notification-manage-binding").disabled = !ws?.connected || !ws.data?.workspace?.binding?.connection_id || !!ws.eventsSending;
  notice("notification-notice", !ws ? "请先打开房间，再管理通知源。" : !ws[`${channel}Connected`] ? "通知状态暂不可读，请重新读取。" : "");
  $("notification-reload").hidden = !ws || !!ws[`${channel}Connected`];
  renderContextViews();
}
function openNotificationSource(opener, channel) {
  notificationFocus = opener;
  if (workspaceView && channel) workspaceView.notificationChannel = channel;
  $("activity-dialog").close();
  panels.members = true; savePanels(); renderPanels();
  if (!$("notification-detail").open) $("notification-detail").show();
  renderGitLabEvents(); $("close-notification").focus();
  if (window.innerWidth <= 640) $("notification-detail").scrollIntoView({ block: "start" });
  if (workspaceView) void loadWorkspaceConnections();
}
$("open-gitlab-source").addEventListener("click", () => openNotificationSource($("open-gitlab-source"),"events"));
$("open-lark-source").addEventListener("click", () => openNotificationSource($("open-lark-source"),"messages"));
$("close-notification").addEventListener("click", () => $("notification-detail").close());
$("notification-detail").addEventListener("close", () => {
  if ($("notification-detail").open) return;
  renderContextViews();
  if (!$("activity-dialog").open && !$("workspace-dialog").open)
    (panels.members && notificationFocus?.isConnected ? notificationFocus : $("toggle-members")).focus();
  notificationFocus = null;
});
$("notification-detail").addEventListener("keydown", event => {
  if (event.key === "Escape") { event.preventDefault(); $("notification-detail").close(); }
});
$("notification-reload").addEventListener("click", async () => { await refresh(); await loadWorkspaceConnections(); });
$("notification-manage-binding").addEventListener("click", () => {
  const ws = workspaceView, binding = ws?.data?.workspace?.binding;
  if (!ws || !binding?.connection_id || $("notification-manage-binding").disabled) return;
  ws.eventsConnection = binding.connection_id;
  $("gitlab-events-project").value = binding.project;
  editGitLabEvents();
});
$("gitlab-events-manage-connections").addEventListener("click", async () => {
  showConnectionList(); $("settings-dialog").showModal(); await loadConnections();
});
for (const channel of ["events", "todos", "messages"]) {
  const tab = $(`notification-tab-${channel}`);
  tab.addEventListener("click", () => { if (workspaceView) workspaceView.notificationChannel = channel; renderGitLabEvents(); });
  tab.addEventListener("keydown", event => {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const tabs = ["events", "todos", "messages"];
    const next = event.key === "Home" ? tabs[0] : event.key === "End" ? tabs.at(-1) : tabs[(tabs.indexOf(channel) + (event.key === "ArrowRight" ? 1 : 2)) % tabs.length];
    if (workspaceView) workspaceView.notificationChannel = next;
    renderGitLabEvents(); $(`notification-tab-${next}`).focus();
  });
}
function renderWorkspaceConnections() {
  const ws = workspaceView;
  if (!ws) return;
  const options = [new Option("请选择已有连接", "")];
  if (ws.data?.workspace?.binding && !ws.data.workspace.binding.connection_id) options.push(new Option("保留当前旧认证（尚未迁移）", "keep"));
  for (const item of ws.connections || []) options.push(new Option(`${item.name} · ${item.url}`, item.id));
  $("workspace-connection").replaceChildren(...options);
  $("workspace-connection").value = ws.selectedConnection || "";
}
async function loadWorkspaceConnections() {
  const ws = workspaceView, epoch = state.catalog?.service_epoch;
  if (!ws || !epoch) return;
  const read = ws.connectionsRead = (ws.connectionsRead || 0) + 1;
  ws.connectionsError = "正在加载平台连接…"; renderWorkspace();
  try {
    const query = new URLSearchParams({ service_epoch: epoch, caller_id: ws.operator });
    const data = await request(`/v1/platform/connections?${query}`);
    if (ws !== workspaceView || state.catalog?.service_epoch !== epoch || read !== ws.connectionsRead) return;
    ws.connections = data.connections.filter(item => item.provider === "gitlab");
    ws.larkConnections = data.connections.filter(item => item.provider === "lark");
    ws.connectionsError = ws.connections.length ? "" : "尚无平台连接，请先管理平台连接。";
    renderWorkspaceConnections();
  } catch { if (ws === workspaceView && state.catalog?.service_epoch === epoch && read === ws.connectionsRead) ws.connectionsError = "平台连接加载失败，请重新打开通知源重试。"; }
  if (ws === workspaceView) renderWorkspace();
}
function fillWorkspaceForm() {
  const ws = workspaceView;
  const operation = ws?.data?.operation;
  const unfinished = operation && ["running", "failed"].includes(operation.status);
  const formVersion = ws?.data && JSON.stringify([ws.data.workspace_version, unfinished ? operation.operation_id : ""]);
  if (!ws?.data || ws.formVersion === formVersion) return;
  const current = ws.data.workspace || {}, suggestion = ws.data.suggestions || {};
  const target = unfinished ? operation.target || current : current;
  ws.formVersion = formVersion;
  $("workspace-root").value = current.root || "workspaces";
  // Suggestions only prefill text. Their checkboxes remain off until selected.
  $("workspace-repository").checked = !!target.repository;
  $("workspace-repo-url").value = target.repository?.url || suggestion.repository?.url || "";
  $("workspace-branch").value = target.repository ? target.repository.branch || "" : suggestion.repository?.branch || "main";
  $("workspace-gitlab").checked = !!target.binding;
  ws.selectedConnection = target.binding?.connection_id || (current.binding ? "keep" : "");
  renderWorkspaceConnections();

}
function renderWorkspace() {
  renderGitLabEvents();
  const ws = workspaceView, data = ws?.data, current = data?.workspace, op = data?.operation;
  $("workspace-control").hidden = !state.managed || !ws;
  $("open-workspace").disabled = !ws;
  const label = current?.repository ? current.repository.url.split("/").pop().replace(/\.git$/, "")
    : !current || current.root === "workspaces" ? "默认目录" : current.root;
  $("workspace-label").textContent = label;
  $("workspace-trigger").title = `工作目录：${label}`;
  $("workspace-selected-label").textContent = label;
  $("workspace-selected-description").textContent = current?.repository ? "各成员使用独立的仓库目录"
    : current && current.root !== "workspaces" ? `房间内目录 · ${current.root}` : "沿用各成员自己的工作目录";
  let status = ws?.pending ? "提交结果待确认" : op?.status === "running" ? workspaceStages[op.phase] || "正在应用"
    : op?.status === "failed" ? workspaceCanRevise() ? "初始化失败 · 可修改后重新应用" : "设置未完成 · 请核查后重试"
    : op?.status === "completed" ? "设置已生效" : "";
  $("workspace-summary").textContent = status;
  $("workspace-summary").hidden = !status || !state.managed || !ws;
  if (!$("workspace-dialog").open) return;
  fillWorkspaceForm();
  $("workspace-current").textContent = current ? `当前目录：${current.root} · 配置版本 ${data.workspace_version}` : "沿用原成员目录";
  $("workspace-paths").replaceChildren(...(data?.members || []).map(member => {
    const item = document.createElement("li"); item.textContent = `${member.agent_id} · ${member.workspace}`; return item;
  }));
  const repo = !!current?.repository, binding = !!current?.binding;
  const git = $("workspace-repository").checked, gitlab = git && $("workspace-gitlab").checked;
  $("workspace-fields").disabled = !data || !ws.connected || workspaceFormBusy() || managementBusy();
  $("workspace-root").readOnly = repo || git;
  $("workspace-root-hint").textContent = repo ? "仓库目录已固定；各成员使用独立 clone。" : git
    ? "首次关联会在新的受管仓库目录中分别 clone；旧目录文件保留。" : "切换目录不搬迁文件，原目录保留。";
  $("workspace-repository").disabled = repo;
  $("workspace-repo-url").readOnly = repo;
  $("workspace-branch").readOnly = repo;
  $("workspace-gitlab").disabled = binding;
  $("workspace-git-fields").hidden = !git;
  $("workspace-gitlab-fields").hidden = !gitlab;
  $("workspace-git-auth-hint").hidden = gitlab;
  $("workspace-repo-url").required = git;
  $("workspace-repo-url").disabled = !git;
  $("workspace-connection").disabled = !gitlab;
  $("workspace-connection-feedback").textContent = ws.connectionsError || "选择连接后，应用时核实项目访问权限。旧认证仅在显式应用后迁移。";
  $("workspace-credential-status").hidden = !binding;
  $("workspace-credential-status").textContent = binding ? data.credential_status === "configured" ? current.binding.connection_id ? `共享连接已配置 · 项目 ID ${current.binding.project_id}（绑定时已核实）` : "旧凭据已配置（远端权限未验证）" : "凭据缺失，请检查平台连接或旧认证。" : "";
  $("workspace-status").textContent = status;
  notice("workspace-error", ws.error || (op?.status === "failed" ? workspaceErrors[op.error_code] || "操作未完成，请检查服务状态。" : ""));
  $("workspace-operation-detail").hidden = !op && !ws.pending;
  $("workspace-operation-target").textContent = op ? JSON.stringify({ operation_id: op.operation_id, status: op.status, phase: op.phase, target: op.target }, null, 2) : ws.pending?.operation_id || "";
  $("workspace-apply").disabled = !data || !ws.connected || workspaceFormBusy() || managementBusy() ||
    state.draft.sending || !!state.draft.pending || state.draft.restartStarting || !!state.draft.restartRequest || state.restart?.status === "running";
  $("workspace-apply").textContent = ws.sending ? "正在提交…" : workspaceCanRevise() ? "修改并重新应用" : git ? repo ? "应用绑定设置" : "绑定并初始化" : "应用";
  $("workspace-retry").hidden = op?.status !== "failed" || !!ws.pending;
  $("workspace-retry").disabled = ws.sending || !ws.connected || !!state.catalog?.busy;
  $("workspace-new").hidden = !ws.missing;
  $("workspace-refresh").disabled = ws.sending;
}
function gitlabProjectFromRepository(value) {
  // Match the server's HTTPS origin and project-path rules. Keep explicit ports
  // (including :443) so the derived origin exactly matches the repository host.
  const raw = value.trim(), parts = /^https:\/\/([^/?#]+)(\/[^?#]*)?$/.exec(raw);
  const invalid = () => new Error("请填写有效的仓库 HTTPS URL，以解析 GitLab 项目；地址不能含用户名、密码、Token 或查询参数。");
  if (!parts || raw.length > 4096 || /[\s\\]/.test(raw)) throw invalid();
  let url;
  try { url = new URL(raw); } catch { throw invalid(); }
  if (!url.hostname || url.username || url.password) throw invalid();
  const path = (parts[2] || "").replace(/^\//, "").replace(/\/$/, "");
  const project = path.replace(/\.git$/, "");
  if (path.length > 2048 || !project.includes("/") || project.split("/").some(part =>
    !/^[A-Za-z0-9_.-]+$/.test(part) || part === "." || part === "..")) throw invalid();
  return { provider: "gitlab", url: `https://${parts[1].toLowerCase()}`, project };
}
function workspaceTarget() {
  const old = workspaceView.data.workspace;
  const git = $("workspace-repository").checked;
  const target = { root: old?.repository || !git ? $("workspace-root").value.trim() : old?.root || "" };
  if (git) {
    target.repository = old?.repository ? { ...old.repository } : { url: $("workspace-repo-url").value.trim() };
    if (!old?.repository && $("workspace-branch").value.trim()) target.repository.branch = $("workspace-branch").value.trim();
    if ($("workspace-gitlab").checked) {
      target.binding = gitlabProjectFromRepository(target.repository.url);
      if (!old?.repository) {
        target.repository.url = `${target.binding.url}/${target.binding.project}.git`;
        $("workspace-repo-url").value = target.repository.url;
      }
      const selected = $("workspace-connection").value;
      if (selected === "keep") {
        if (!old?.binding || old.binding.connection_id) throw new Error("请选择平台连接。");
        target.binding = { ...old.binding };
      } else {
        const connection = workspaceView.connections?.find(item => item.id === selected);
        if (!connection) throw new Error("请选择已有平台连接，或前往管理平台连接。");
        if (connection.provider !== target.binding.provider || connection.url !== target.binding.url) throw new Error("所选连接与仓库实例不一致。");
        target.binding.connection_id = connection.id;
      }
    }
  }
  // Reject embedded credentials before any non-sensitive request is retained.
  for (const value of [target.repository?.url, target.binding?.url].filter(Boolean)) {
    let url;
    try { url = new URL(value); } catch { throw new Error("请填写有效的 HTTPS 地址。"); }
    if (url.protocol !== "https:" || url.username || url.password || url.search || url.hash) throw new Error("请使用无用户名、密码、Token 或查询参数的 HTTPS 地址。");
  }
  return target;
}
async function submitWorkspace(retry = false) {
  const ws = workspaceView;
  if (!ws || ws.sending || !ws.connected || (retry ? $("workspace-retry").disabled : $("workspace-apply").disabled)) return;
  let payload;
  try {
    payload = { ...ws.room, service_epoch: state.catalog.service_epoch, caller: { kind: "human", id: ws.operator },
      operation_id: retry ? ws.data.operation.operation_id : crypto.randomUUID().replaceAll("-", "") };
    if (!retry) {
      payload.expected_version = ws.data.workspace_version; payload.target = workspaceTarget();
      if (workspaceCanRevise()) payload.replaces_operation_id = ws.data.operation.operation_id;
      if (payload.target.binding?.connection_id) payload.connection_version = ws.connections.find(item => item.id === payload.target.binding.connection_id).version;
    }
  } catch (error) { ws.error = error.message; renderWorkspace(); return; }
  ws.pending = { operation_id: payload.operation_id }; ws.missing = false; saveWorkspacePending(ws);
  ws.closeOnComplete = payload.operation_id;
  ws.sending = true; ws.error = "";

  renderWorkspace(); composerState(); renderMembers(); renderRooms();
  try {
    const op = await request(retry ? "/v1/workspace/retry" : "/v1/workspace/apply", payload);
    ws.data.operation = op; ws.pending = null; saveWorkspacePending(ws);
  } catch (error) {
    if (["request", "identity", "conflict", "capacity", "busy"].includes(error.code)) {
      ws.closeOnComplete = null;
      ws.pending = null; saveWorkspacePending(ws);
      ws.error = error.code === "conflict" ? "配置已变化，请查看最新状态后重新应用。" : "请求被拒绝，请核对配置、当前房间和服务状态；共享连接变化后请重新打开设置加载连接。";
    } else ws.error = "提交结果尚未确认，正在查询原操作；不会自动重发。";
  } finally {
    delete payload.pat; ws.sending = false;
    await refresh(); renderWorkspace(); composerState(); renderRooms();
  }
}
function closeWorkspacePicker() {
  $("workspace-picker").open = false;
  $("workspace-trigger").setAttribute("aria-expanded", "false");
}
$("workspace-picker").addEventListener("toggle", () => {
  $("workspace-trigger").setAttribute("aria-expanded", String($("workspace-picker").open));
});
$("workspace-picker").addEventListener("keydown", event => {
  if (event.key === "Escape" && $("workspace-picker").open) {
    event.preventDefault(); closeWorkspacePicker(); $("workspace-trigger").focus();
  }
});
document.addEventListener("click", event => {
  if ($("workspace-picker").open && !$("workspace-picker").contains(event.target)) closeWorkspacePicker();
});
$("workspace-picker").addEventListener("focusout", event => {
  if (event.relatedTarget && !$("workspace-picker").contains(event.relatedTarget)) closeWorkspacePicker();
});
async function openWorkspace() {
  if (!workspaceView) return;
  closeWorkspacePicker();
  $("workspace-dialog").showModal(); renderWorkspace(); $("close-workspace").focus(); await loadWorkspaceConnections();
}
$("open-workspace").addEventListener("click", openWorkspace);
$("close-workspace").addEventListener("click", () => $("workspace-dialog").close());
$("workspace-dialog").addEventListener("close", () => {
  ($("notification-detail").open ? $("notification-manage-binding") : $("workspace-trigger")).focus();
});
for (const id of ["workspace-repository", "workspace-gitlab"]) $(id).addEventListener("change", renderWorkspace);
$("workspace-connection").addEventListener("change", () => { if (workspaceView) workspaceView.selectedConnection = $("workspace-connection").value; });
$("workspace-manage-connections").addEventListener("click", async () => {
  $("workspace-dialog").close(); showConnectionList(); $("settings-dialog").showModal(); await loadConnections();
});
$("workspace-form").addEventListener("submit", event => { event.preventDefault(); return submitWorkspace(); });
$("workspace-retry").addEventListener("click", () => submitWorkspace(true));
$("workspace-refresh").addEventListener("click", async () => { await refresh(); await loadWorkspaceConnections(); });
$("workspace-new").addEventListener("click", () => {
  const ws = workspaceView;
  if (!ws?.missing || ws.sending) return;
  ws.pending = null; ws.missing = false; ws.error = "请核对目标和平台连接，再点击应用；原请求不会自动重发。";
   saveWorkspacePending(ws); renderWorkspace(); composerState(); renderRooms();
});

function savePanels() {
  try { storage?.setItem(panelsKey, JSON.stringify(panels)); } catch { /* Keep this page's selection in memory. */ }
}

function memberWidthLimits() {
  const available = $("workspace").clientWidth - (state.managed ? $("room-rail").getBoundingClientRect().width : 0);
  const max = Math.max(180, Math.min(720, available - 240));
  return { min: Math.min(220, max), max };
}

function applyMemberWidth() {
  const limits = memberWidthLimits();
  let width = $("member-rail").getBoundingClientRect().width;
  if (Number.isFinite(panels.memberWidth)) {
    width = Math.max(limits.min, Math.min(limits.max, panels.memberWidth));
    $("workspace").style.setProperty("--members-open-width", `${width}px`);
  }
  const handle = $("member-resizer");
  handle.setAttribute("aria-valuemin", String(limits.min)); handle.setAttribute("aria-valuemax", String(limits.max));
  handle.setAttribute("aria-valuenow", String(Math.round(width)));
}

function setMemberWidth(width) {
  const limits = memberWidthLimits();
  panels.memberWidth = Math.round(Math.max(limits.min, Math.min(limits.max, width)));
  applyMemberWidth();
}

function renderPanels() {
  const workspace = document.querySelector(".workspace");
  workspace.classList.toggle("rooms-collapsed", !panels.rooms);
  workspace.classList.toggle("members-collapsed", !panels.members);
  $("room-rail").hidden = !state.managed;
  $("room-nav").hidden = !state.managed;
  $("room-brand").inert = !panels.rooms;
  $("room-brand").setAttribute("aria-hidden", String(!panels.rooms));
  for (const [side, id] of [["rooms", "room-nav"], ["members", "member-sidebar"]]) {
    // Keep content mounted for the slide, while removing collapsed controls from keyboard/AT access.
    $(id).inert = !panels[side];
    $(id).setAttribute("aria-hidden", String(!panels[side]));
  }
  $("toggle-rooms").hidden = !state.managed;
  $("member-rail").inert = !panels.members;
  $("member-rail").setAttribute("aria-hidden", String(!panels.members));
  if (!panels.members && $("activity-dialog").open) $("activity-dialog").close();
  if (!panels.members && $("notification-detail").open) $("notification-detail").close();
  renderContextViews();
  applyMemberWidth();
  for (const [side, name] of [["rooms", "房间侧栏"], ["members", "团队成员"]]) {
    const button = $("toggle-" + side);
    const label = `${panels[side] ? "收起" : "展开"}${name}`;
    button.setAttribute("aria-expanded", String(panels[side]));
    button.setAttribute("aria-label", label);
    button.setAttribute("title", label);
  }
}
for (const side of ["rooms", "members"]) $("toggle-" + side).addEventListener("click", () => {
  panels[side] = !panels[side];
  savePanels();
  renderPanels();
});
$("member-resizer").addEventListener("pointerdown", event => {
  if (event.button !== 0 || event.isPrimary === false || !panels.members || window.innerWidth <= 640) return;
  event.preventDefault();
  memberResize = { pointerId: event.pointerId, x: event.clientX, width: $("member-rail").getBoundingClientRect().width };
  $("member-resizer").setPointerCapture(event.pointerId);
  $("workspace").classList.add("resizing-members");
});
$("member-resizer").addEventListener("pointermove", event => {
  if (memberResize?.pointerId !== event.pointerId) return;
  setMemberWidth(memberResize.width + memberResize.x - event.clientX);
});
function finishMemberResize(event) {
  if (!memberResize || memberResize.pointerId !== event.pointerId) return;
  memberResize = null;
  $("workspace").classList.remove("resizing-members");
  if ($("member-resizer").hasPointerCapture(event.pointerId)) $("member-resizer").releasePointerCapture(event.pointerId);
  savePanels();
}
for (const type of ["pointerup", "pointercancel", "lostpointercapture"]) $("member-resizer").addEventListener(type, finishMemberResize);
$("member-resizer").addEventListener("keydown", event => {
  const limits = memberWidthLimits(), width = $("member-rail").getBoundingClientRect().width;
  const next = { ArrowLeft: width + 20, ArrowRight: width - 20, Home: limits.min, End: limits.max }[event.key];
  if (next === undefined) return;
  event.preventDefault(); setMemberWidth(next); savePanels();
});
window.addEventListener("resize", applyMemberWidth);
renderPanels();
let viewVersion = 0;
const currentView = (version) => version === viewVersion;
let restartNoticeID = "", restartNoticeExpires = 0;
let mentionKey = "", mentionSeenThrough = 0;
let mentionSeen = new Set();
const mentionVisibleSince = new Map();
let mentionTimer = null, mentionFramePending = false;
const mentionDwell = 1000;
const dateFormat = new Intl.DateTimeFormat("zh-CN", { month: "long", day: "numeric", weekday: "short" });
const timeFormat = new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit" });
let activitySignature = "";
let activityTimelineContext = "";
let activityScrollMember = "";
let activityFollowing = true, activityScrollTop = 0;
const activityTimelineRows = new Map();
let activityFocus = null;
let activityMember = null;
let agentTab = "activity";
const activity = createActivityReader((member) => rpc("/v1/member/activity", {
  agent_id: member.agent_id, membership_id: member.membership_id,
}, member.info), renderActivity);

function rpc(path, data = {}, info = state.info) {
  return request(path, { ...info, caller: { kind: "human", id: state.operator }, ...data });
}

function notice(id, text) {
  $(id).textContent = text;
  $(id).hidden = !text;
}

function discussionBusy() {
  return workspaceBusy() || managementBusy() || state.draft.restartStarting || state.restart?.status === "running" || !!state.draft.restartRequest;
}

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
  document.title = `${mentions.length ? `(${mentions.length} 条 @你) ` : ""}${roomName()} · TeamX`;
  scheduleMentionViewing();
}

function unreadMentions() {
  return unseenMentions(state.messages.values(), state.operator, mentionSeen, mentionSeenThrough);
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
    try { sessionStorage.setItem(mentionKey, JSON.stringify({ seenThrough: mentionSeenThrough, seen: [...mentionSeen] })); }
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
    const value = JSON.parse(sessionStorage.getItem(key));
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

function messageElement(message) {
  const el = $("message-template").content.firstElementChild.cloneNode(true);
  el.id = `message-${message.seq}`;
  const agent = message.author.kind === "agent", integration = message.author.kind === "integration";
  el.classList.toggle("integration", integration);
  const avatar = el.querySelector(".avatar");
  avatar.textContent = integration ? "GL" : message.author.id.slice(0, 2).toUpperCase();
  avatar.classList.toggle("integration-avatar", integration);
  avatar.classList.toggle("agent-avatar", agent);
  const channel = message.source?.provider === "gitlab" && ["events", "todos"].includes(message.source?.channel) ? message.source.channel : null;
  el.querySelector(".author").textContent = integration ? channel ? `GitLab · ${channel === "todos" ? "我的待办" : "项目活动"}` : `${message.author.id} · 通知` : message.author.id;
  if (integration && channel) {
    const author = el.querySelector(".author");
    author.disabled = false;
    author.setAttribute("aria-label", "查看 GitLab 通知源");
    author.setAttribute("aria-controls", "notification-detail");
    author.addEventListener("click", () => openNotificationSource(author, channel));
    avatar.setAttribute("role", "button"); avatar.setAttribute("tabindex", "0");
    avatar.setAttribute("aria-label", "查看 GitLab 通知源");
    avatar.setAttribute("aria-controls", "notification-detail");
    avatar.addEventListener("click", () => openNotificationSource(avatar, channel));
    avatar.addEventListener("keydown", event => {
      if (event.key === "Enter" || event.key === " ") { event.preventDefault(); openNotificationSource(avatar, channel); }
    });
  } else if (agent) {
    const author = el.querySelector(".author");
    const open = opener => {
      const member = state.members.find(item => item.agent_id === message.author.id);
      if (member) openMemberActivity(member, opener);
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
  const ranges = mentionRanges(message.text, state.operator);
  renderMessageBody(text, message.text, state.operator, integration ? notificationOptions(state.info, state.operator, message.source, { seq: message.seq }) : undefined);
  el.classList.toggle("mentioned", message.seq > mentionSeenThrough && !mentionSeen.has(message.seq) && ranges.length > 0 && !(message.author.kind === "human" && message.author.id === state.operator));
  const time = el.querySelector("time");
  time.textContent = timeFormat.format(new Date(message.created_at));
  time.dateTime = message.created_at;
  time.title = new Date(message.created_at).toLocaleString("zh-CN");
  el.querySelector(".sequence").textContent = `#${message.seq}`;
  el.querySelector(".reply-button").addEventListener("click", () => {
    if (state.draft.pending || !state.connected || discussionBusy()) return;
    state.draft.replyTo = message.seq;
    $("reply-label").textContent = `引用 #${message.seq} · ${message.author.id}：${message.text}`;
    $("reply-context").hidden = false;
    saveDraft();
    $("message-input").focus();
  });
  if (message.reply_to) {
    const ref = el.querySelector(".message-reference");
    const target = state.messages.get(message.reply_to);
    ref.hidden = false;
    ref.textContent = target ? `↳ #${target.seq} ${target.author.id}：${target.text}` : message.reply_to <= state.clearedThrough
      ? `↳ 引用消息 #${message.reply_to} · 已清空` : `↳ 引用消息 #${message.reply_to} · 加载更早消息可查看`;
    ref.addEventListener("click", () => {
      const target = $(`message-${message.reply_to}`);
      if (!target) { $("load-history").focus(); return; }
      target.scrollIntoView({ block: "center" });
      target.classList.remove("flash");
      requestAnimationFrame(() => target.classList.add("flash"));
    });
  }
  return el;
}

function renderMessages() {
  const fragment = document.createDocumentFragment();
  const messages = [...state.messages.values()].sort((a, b) => a.seq - b.seq);
  let day = "";
  for (const message of messages) {
    const date = new Date(message.created_at);
    const nextDay = date.toLocaleDateString();
    if (nextDay !== day) {
      day = nextDay;
      const divider = document.createElement("div");
      divider.className = "day-divider";
      divider.textContent = dateFormat.format(date);
      fragment.append(divider);
    }
    fragment.append(messageElement(message));
  }
  $("messages").replaceChildren(fragment);
  state.oldest = messages[0]?.seq || 0;
  $("empty").hidden = messages.length !== 0;
  $("load-history").hidden = state.oldest <= state.clearedThrough + 1;
  renderMentionNotice();
}

function applyClear(through) {
  if (through <= state.clearedThrough) return false;
  state.clearedThrough = through;
  for (const seq of state.messages.keys()) if (seq <= through) state.messages.delete(seq);
  if (state.draft.replyTo && state.draft.replyTo <= through) clearReply();
  return true;
}

const statusNames = { paused: "已暂停", idle: "等待消息", waiting: "等待执行", running: "运行中", completed: "已完成",
  failed: "执行失败", interrupted: "已中断", unavailable: "暂不可用", detached: "未连接",
  budget_exhausted: "预算用尽", checkpoint_failed: "检查点失败", backend_blocked: "需重置会话",
  rotating: "轮换中", closing: "清理中", merging: "合并通知" };

function memberRole(member) {
  return selectedEntry()?.members?.find(role => role.id === member.agent_id);
}

function openMemberActivity(member, opener) {
  if (!state.connected || !state.info) return;
  const current = state.members.find(item => item.agent_id === member.agent_id && item.membership_id === member.membership_id);
  if (!current) return;
  if (opener !== $("activity-member")) activityFocus = { element: opener, membership: current.membership_id };
  $("notification-detail").close();
  activityMember = { agent_id: current.agent_id, membership_id: current.membership_id };
  panels.members = true; savePanels(); renderPanels();
  if (!$("activity-dialog").open) $("activity-dialog").show();
  memberToolsView = null;
  renderActivityControls();
  void selectAgentTab(opener === $("activity-member") ? agentTab : "activity");
  if (window.innerWidth <= 640) $("activity-dialog").scrollIntoView({ block: "start" });
}

function renderActivityControls() {
  const panel = $("activity-dialog"), select = $("activity-member");
  if (panel.open && !state.members.some(member => member.membership_id === activityMember?.membership_id)) panel.close();
  renderContextViews();
  const signature = JSON.stringify(state.members.map(member => [member.agent_id, member.membership_id, memberRole(member)?.name]));
  if (select.dataset.signature !== signature) {
    select.dataset.signature = signature;
    select.replaceChildren(...state.members.map(member => new Option(memberRole(member)?.name || member.agent_id, member.membership_id)));
  }
  select.value = activityMember?.membership_id || "";
  select.hidden = !state.members.length; select.disabled = !state.connected || state.members.length < 2;
  $("agent-tabs").hidden = false;
  $("agent-tab-tools").hidden = !state.managed;
  const member = selectedAgent();
  if (!member || !panel.open) return;
  $("agent-status").textContent = member.execution?.active ? "运行中" : statusNames[member.status] || member.status;
  $("agent-status").classList.toggle("running", !!member.execution?.active);
  $("agent-status").classList.toggle("failed", /failed|blocked|unavailable|interrupted/.test(member.status));
  renderMemberActions(member, $("agent-actions"));
  const execution = member.execution;
  const reset = execution?.session_reset_required || member.status === "backend_blocked";
  const budget = execution?.turn_budget;
  const exhausted = budget && budget.used >= budget.limit;
  const effects = [];
  if (reset) effects.push("重置会话，清除私有上下文");
  if (exhausted) effects.push(`追加 ${budget.grant || 16} 回合`);
  notice("agent-enable-hint", effects.length ? `启用协作将${effects.join("，并")}。` : "");
  notice("agent-error", member.execution?.error || "");
  if (memberToolsView?.cli) {
    const signature = JSON.stringify([state.connected, state.catalogConnected, discussionBusy()]);
    if (memberToolsView.availability !== signature) {
      memberToolsView.availability = signature; renderCLITools(memberToolsView);
    }
  }
}
function selectedAgent() {
  return state.members.find(m => m.agent_id === activityMember?.agent_id && m.membership_id === activityMember?.membership_id);
}
async function selectAgentTab(tab) {
  const member = selectedAgent();
  if (!$("activity-dialog").open || !member) return;
  agentTab = tab === "policy" ? "policy" : tab === "inbox" ? "inbox" : state.managed && tab === "tools" ? "tools" : "activity";
  for (const key of ["activity", "inbox", "policy", "tools"]) {
    const button = $(`agent-tab-${key}`);
    button.setAttribute("aria-selected", String(agentTab === key));
    button.setAttribute("tabindex", agentTab === key ? "0" : "-1");
  }
  $("activity-body").hidden = $("activity-footer").hidden = agentTab !== "activity";
  $("tools-member-view").hidden = agentTab !== "tools";
  $("inbox-member-view").hidden = agentTab !== "inbox";
  $("policy-member-view").hidden = agentTab !== "policy";
  if (agentTab === "activity") await activity.select({ ...member, info: state.info });
  else if (agentTab === "inbox") { activity.close(); await loadMemberInbox(true); }
  else if (agentTab === "policy") { activity.close(); await loadMemberPolicy(true); }
  else {
    activity.close();
    if (!memberToolsView || !toolsCurrent(memberToolsView)) await openMemberTools(member);
  }
}
for (const key of ["activity", "inbox", "policy", "tools"]) {
  const button = $(`agent-tab-${key}`);
  button.addEventListener("click", () => selectAgentTab(key));
  button.addEventListener("keydown", event => {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const keys = state.managed ? ["activity", "inbox", "policy", "tools"] : ["activity", "inbox", "policy"];
    const current = keys.indexOf(key);
    const next = event.key === "Home" ? keys[0] : event.key === "End" ? keys.at(-1) : keys[(current + (event.key === "ArrowLeft" ? keys.length - 1 : 1)) % keys.length];
    $(`agent-tab-${next}`).focus(); void selectAgentTab(next);
  });
}
let policyView = null, policyEditor = null;
const policyStates = {suggested:"建议 · 未生效", active:"有效", revoked:"已撤销"};
function policyCurrent(view) {
  return policyView === view && agentTab === "policy" && $("activity-dialog").open && selectedAgent()?.membership_id === view.member.membership_id && state.info?.room_id === view.info.room_id && state.info?.room_instance === view.info.room_instance && state.info?.service_epoch === view.info.service_epoch;
}
function editPolicy(record = null) {
  const view = policyView;
  if (!view || !policyCurrent(view) || view.saving) return;
  policyEditor = {view, record, id:record?.id || `p-${Date.now()}-${Math.random().toString(36).slice(2,10)}`};
  $("policy-form").hidden = false;
  $("policy-editor-title").textContent = record ? "修改长期规则" : "新增长期规则";
  $("policy-text").value = record?.text || "";
  $("policy-expires").value = record?.expires_at ? new Date(new Date(record.expires_at).getTime()-new Date(record.expires_at).getTimezoneOffset()*60000).toISOString().slice(0,16) : "";
  $("policy-text").focus();
}
function policyDateText(value) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString("zh-CN", {year:"numeric",month:"2-digit",day:"2-digit",hour:"2-digit",minute:"2-digit",hour12:false});
}
function policyMetadata(record) {
  const meta = document.createElement("div"); meta.className = "policy-entry-meta";
  if (record.expires_at) {
    const expires = document.createElement("p"); expires.textContent = `有效期至：${policyDateText(record.expires_at)}`; meta.append(expires);
  }
  return meta;
}
function policyCard(view, r, history) {
  const section = document.createElement("section"); section.className = "activity-section policy-entry";
  const header = document.createElement("div"); header.className = "policy-entry-header";
  const title = document.createElement("h4"), status = document.createElement("span");
  const expired = r.expires_at && new Date(r.expires_at) <= new Date();
  status.className = "policy-entry-state"; status.dataset.status = expired ? "expired" : r.status;
  status.textContent = expired ? `${policyStates[r.status]} · 已过期` : policyStates[r.status]; title.append(status);
  const body = document.createElement("p"); body.className = "inbox-text policy-entry-body"; body.textContent = r.text;
  const source = document.createElement("details"), summary = document.createElement("summary"); source.className = "policy-history";
  summary.textContent = `来源与修订记录 · v${r.version}`;
  const identity = document.createElement("p"); identity.textContent = `规则 ID：${r.id}`;
  source.append(summary, identity);
  for (const v of history.filter(v => v.id === r.id)) {
    const revision = document.createElement("article"); revision.className = "policy-revision";
    const heading = document.createElement("h5"); heading.textContent = `v${v.version} · ${policyStates[v.status]}`;
    const metadata = document.createElement("p"); metadata.className = "policy-revision-meta";
    const time = document.createElement("time"); time.dateTime = v.updated_at; time.title = v.updated_at; time.textContent = policyDateText(v.updated_at);
    const origin = document.createElement("span");
    origin.textContent = ` · 操作者 ${v.changed_by.id}\n${v.source.seq ? "Room 消息 #"+v.source.seq : "成员设置"} · 来源用户 ${v.source.user}`;
    metadata.append(time, origin);
    const content = document.createElement("p"); content.className = "inbox-text policy-revision-body"; content.textContent = v.text;
    const raw = document.createElement("details"), rawTitle = document.createElement("summary"); raw.className = "policy-source"; rawTitle.textContent = "原始来源";
    const original = document.createElement("p"); original.className = "inbox-text";
    original.textContent = v.source.text;
    raw.append(rawTitle, original);
    revision.append(heading, metadata, content, policyMetadata(v), raw);
    if (v.conversation) {
      const basis = document.createElement("p"); basis.className = "inbox-text";
      basis.textContent = `Agent 理解：${v.conversation.basis}`; revision.append(basis);
    }
    source.append(revision);
  }
  const actions = document.createElement("div"); actions.className = "member-actions";
  const edit = document.createElement("button"); edit.type = "button"; edit.textContent = r.status === "suggested" ? "审核并编辑" : "编辑";
  edit.addEventListener("click",()=> { if (policyCurrent(view)) editPolicy(r); }); actions.append(edit);
  if (r.status !== "revoked") {
    const revoke = document.createElement("button"); revoke.type = "button"; revoke.className = "policy-revoke"; revoke.textContent = "撤销";
    revoke.addEventListener("click",()=> { if (policyCurrent(view)) void savePolicyChange(view,{...policyChange(r),status:"revoked",source_text:`撤销长期规则 ${r.id} v${r.version}：${r.text}`}); }); actions.append(revoke);
  }
  header.append(title, actions); section.append(header, body, policyMetadata(r), source); return section;
}
function followupResultURL(raw) { try { const url=new URL(raw);return ["https:","http:"].includes(url.protocol) && !url.username && !url.password ? url.href : ""; } catch { return ""; } }
function beginReply(message) {
  if(state.draft.pending || !state.connected || discussionBusy()) return;
  state.draft.replyTo=message.seq;
  $("reply-label").textContent=`引用 #${message.seq} · ${message.author.id}：${message.text}`;
  $("reply-context").hidden=false;saveDraft();$("message-input").focus();
}
function followupParagraph(parent,text) { const p=document.createElement("p");p.textContent=text;parent.append(p); }
const followupStates = {pending:"待处理",in_progress:"办理中",waiting_user:"待你答复",waiting_followup:"等待后续",completed:"已完成",archived:"已归档",unknown:"结果待核实"};
const sourceStates = {resolved:"已关联到此对象",partial:"部分信息已核实",unavailable:"暂无法核实",unsupported:"暂不支持解析",mismatch:"身份不符",permission_denied:"无权核实"};
function followupsCurrent(view, request = view.followupRequest) {
  return policyCurrent(view) && $("policy-followups").open && view.followupRequest === request;
}
function followupDisclosure(parent, title, className = "") {
  const detail=document.createElement("details"), summary=document.createElement("summary");
  detail.className=className; summary.textContent=title; detail.append(summary); parent.append(detail); return detail;
}
async function loadFollowupDetail(view,item,section) {
  const request=view.followupRequest;
  let panel=section.followupPanel;
  if(!panel) { panel=document.createElement("div");panel.className="followup-detail";section.followupPanel=panel;section.append(panel); }
  panel.replaceChildren(); section.followupLoading=true;
  try {
    if(item.kind === "unresolved_source") {
      const event=await rpc("/v1/matters/source",{...view.member,followup:{seq:item.seq,source_ref:item.ref}},view.info);
      if(!followupsCurrent(view,request)) return;
      renderFollowupSource(panel,event);
      section.followupLoaded=true;
      return;
    }
    const data=await rpc("/v1/matters/read",{...view.member,followup:{object_ref:item.object_ref,unit_ref:item.ref}},view.info);
    if(!followupsCurrent(view,request)) return;
    const unit=data.unit,target=data.target.target;
    const progress=document.createElement("section");progress.className="followup-progress";
    const heading=document.createElement("h4");heading.textContent="处理进展";progress.append(heading);panel.append(progress);
    followupParagraph(progress,`办理状态：${followupStates[unit.status] || unit.status}`);
    if(unit.summary) followupParagraph(progress,unit.summary);
    if(unit.next) followupParagraph(progress,`下一步：${unit.next}`);
    if(!unit.summary && !unit.next) followupParagraph(progress,"暂无进展摘要或下一步记录。");
    if(unit.evidence) followupParagraph(progress,`进展依据：${unit.evidence}`);
    for(const question of unit.questions || []) {
      followupParagraph(progress,`${question.answers?.length ? "已答问题" : "待你答复"}：${question.text}`);
      for(const answer of question.answers || []) followupParagraph(progress,`用户 ${answer.message.author.id} #${answer.message.seq}：${answer.message.text}`);
      if(question.room_seq && !question.answers?.length) { const reply=document.createElement("button");reply.type="button";reply.textContent="在 Room 回答";reply.addEventListener("click",()=>{ if(!followupsCurrent(view,request))return; const message=state.messages.get(question.room_seq); if(message) { beginReply(message); $("activity-dialog").close(); } else { followupParagraph(progress,`请在 Room 加载消息 #${question.room_seq} 并就地回复。`); } });progress.append(reply); }
    }
    const instructionStates={active:"有效",fulfilled:"已落实",revoked:"已撤销",expired:"已过期"};
    for(const instruction of unit.instructions || []) followupParagraph(progress,`单次指令 · ${instructionStates[instruction.status] || instruction.status} · ${instruction.intent.body || instruction.intent.constraints} · 用户 #${instruction.source.seq}：${instruction.source.text}`);
    for(const action of unit.actions || []) {
      const actions={"object.comment":"发表评论","discussion.reply":"回复讨论"}, results={succeeded:"已成功",not_applied:"未执行",unknown:"结果待核实"};
      followupParagraph(progress,`${actions[action.request.operation] || action.request.operation} · ${results[action.status] || action.status} · ${action.attempts} 次尝试${action.possible_duplicate ? " · 可能重复" : ""}`);
      followupParagraph(progress,`账号 ${action.inspection.account_label || action.access.account.id.value} · ${action.request.input.body} · ${action.result?.message || "结果待核实"}`);
      if(followupResultURL(action.result?.url)) { const link=document.createElement("a");link.textContent="查看远端回执";link.href=followupResultURL(action.result.url);link.target="_blank";link.rel="noopener noreferrer";progress.append(link); }
    }
    const sources=followupDisclosure(panel,"相关通知","followup-sources");
    const body=document.createElement("div");sources.append(body);
    sources.addEventListener("toggle",async()=>{
      if(!sources.open || sources.loaded || sources.loading || !followupsCurrent(view,request))return;
      sources.loading=true;body.replaceChildren();
      try { await loadFollowupSources(view,item.object_ref,body,0,request);if(followupsCurrent(view,request))sources.loaded=true; }
      catch(error) { if(followupsCurrent(view,request))followupParagraph(body,`读取失败：${error.message}。收起后展开可重试。`); }
      finally { sources.loading=false; }
    });
    const technical=followupDisclosure(panel,"技术信息","followup-technical");
    followupParagraph(technical,`${target.provider} · ${target.instance} · ${target.object.type} 原生 ID ${target.object.id.value} · 跟进版本 ${unit.revision}`);
    for(const action of unit.actions || []) followupParagraph(technical,`动作 ID：${action.request.id}`);
    section.followupLoaded=true;
  } catch(error) { if(followupsCurrent(view,request)) followupParagraph(panel,`读取失败：${error.message}。收起后展开可重试。`); }
  finally { section.followupLoading=false; }
}
function renderFollowupSource(panel,event) {
  const resolution=event.last_attempt?.resolution || event.resolution;
  if(resolution.reason) followupParagraph(panel,resolution.reason);
  const body=document.createElement("div");body.className="inbox-text";
  renderMessageBody(body,event.entry?.item?.text || "没有通知正文。");panel.append(body);
  if(followupResultURL(event.source?.url)) { const link=document.createElement("a");link.textContent="打开来源";link.href=followupResultURL(event.source.url);link.target="_blank";link.rel="noopener noreferrer";panel.append(link); }
  const technical=followupDisclosure(panel,"技术信息","followup-technical");
  followupParagraph(technical,`投递 ID：${event.delivery_id} · 解析状态：${resolution.status}`);
}
async function loadFollowupSources(view,objectRef,panel,after,request) {
  const page=await rpc("/v1/matters/sources",{...view.member,followup:{object_ref:objectRef},after,limit:5},view.info);
  if(!followupsCurrent(view,request))return;
  if(!page.items.length && !after)followupParagraph(panel,"暂无已关联通知。");
  for(const source of page.items) {
    const detail=followupDisclosure(panel,`收件箱 #${source.seq} · ${sourceStates[source.status] || source.status}`,"followup-source");
    const body=document.createElement("div");detail.append(body);
    detail.addEventListener("toggle",async()=>{
      if(!detail.open || detail.loaded || detail.loading || !followupsCurrent(view,request))return;
      detail.loading=true;body.replaceChildren();
      try { const event=await rpc("/v1/matters/source",{...view.member,followup:{seq:source.seq,source_ref:source.source_ref}},view.info);if(followupsCurrent(view,request)){renderFollowupSource(body,event);detail.loaded=true;} }
      catch(error) { if(followupsCurrent(view,request))followupParagraph(body,`读取失败：${error.message}。收起后展开可重试。`); }
      finally {detail.loading=false;}
    });
  }
  if(!page.complete) { const more=document.createElement("button");more.type="button";more.textContent="更多通知";more.addEventListener("click",async()=>{more.disabled=true;try{await loadFollowupSources(view,objectRef,panel,page.next_after,request);more.hidden=true;}catch(error){more.disabled=false;if(followupsCurrent(view,request))followupParagraph(panel,error.message);}});panel.append(more); }
}
function policyChange(record) {
  const {id,version,status,text,expires_at} = record;
  return {id,version,status,text,...(expires_at ? {expires_at} : {}),revision:policyView.data.revision};
}
function renderPolicy(view) {
  if (!policyCurrent(view)) return;
  $("policy-entries").replaceChildren(...view.data.current.map(r=>policyCard(view,r,view.data.history)));
  if(!view.data.current.length)followupParagraph($("policy-entries"),"尚未保存长期规则。你可以在 Room 中告诉 Agent 今后应如何处理。");
  $("policy-status").textContent=`${view.data.effective.length} 条有效`;
}
function renderPolicyFollowups(view) {
  if(!followupsCurrent(view))return;
  $("policy-matters").replaceChildren(...view.matters.items.map(item => {
    const section=document.createElement("details"); section.className="followup-record";
    const title=document.createElement("summary"), label=document.createElement("span"), status=document.createElement("span");
    label.textContent=`${item.kind === "object" ? "对象跟进" : item.kind === "discussion" ? "讨论跟进" : "通知待核实"}${item.target_label ? " · "+item.target_label : item.kind === "unresolved_source" ? " · 收件箱 #"+item.seq : ""}`;
    status.className="followup-state";
    status.textContent=item.kind === "unresolved_source" ? sourceStates[item.status] || item.status : item.all_completed === false && ["completed","archived"].includes(item.status) ? "有未完成跟进" : followupStates[item.status] || item.status;
    status.dataset.attention=String(item.status === "waiting_user" || item.status === "unknown" || !!item.actions?.length);
    title.append(label,status);section.append(title);
    if(item.summary)followupParagraph(section,item.summary);
    if(item.next)followupParagraph(section,`${item.kind === "unresolved_source" ? "核实说明" : "下一步"}：${item.next}`);
    section.addEventListener("toggle",()=>{if(section.open && !section.followupLoaded && !section.followupLoading && followupsCurrent(view))void loadFollowupDetail(view,item,section);});
    if(item.object_ref) { const related=document.createElement("button");related.type="button";related.className="text-button";related.textContent="查看同对象记录";related.addEventListener("click",()=>{if(followupsCurrent(view))void loadPolicyFollowups(view,"",item.object_ref);});section.append(related); }
    for(const q of item.questions || []) { const p=document.createElement("p");p.className="followup-attention";p.textContent=`待你答复：${q.text}`;section.append(p); }
    for(const action of item.actions || []) { const p=document.createElement("p");p.className="followup-attention";p.textContent=`执行结果待核实 · ${action.attempts} 次尝试`;section.append(p); }
    return section;
  }));
  if(!view.matters.items.length)followupParagraph($("policy-matters"),"暂无跟进记录。");
  $("policy-next").hidden=view.matters.complete;
  $("followups-all").hidden=!view.objectRef;
  $("followups-status").textContent=`${view.objectRef ? "当前对象 · " : ""}${view.matters.total} 条${$("followups-history").checked ? "" : "活跃"}记录`;
}
async function loadPolicyFollowups(view, cursor = "", objectRef = view.objectRef) {
  if(!followupsCurrent(view))return;
  const request=++view.followupRequest;
  view.followupsLoading=true;view.followupsLoaded=false;view.objectRef=objectRef;
  $("policy-matters").replaceChildren();$("policy-next").hidden=true;$("followups-status").textContent="正在读取跟进记录…";
  for(const id of ["followups-reload","followups-all","policy-next","followups-history"])$(id).disabled=true;
  try {
    const matters=await rpc("/v1/matters/list",{...view.member,followup:{cursor,object_ref:objectRef,include_completed:$("followups-history").checked},limit:5},view.info);
    if(!followupsCurrent(view,request))return;
    view.matters=matters;view.followupsLoaded=true;renderPolicyFollowups(view);
  } catch(error) { if(followupsCurrent(view,request))$("followups-status").textContent=`读取失败：${error.message}。可刷新记录重试。`; }
  finally { if(followupsCurrent(view,request)){view.followupsLoading=false;for(const id of ["followups-reload","followups-all","policy-next","followups-history"])$(id).disabled=false;} }
}
async function loadMemberPolicy(resetFollowups = false) {
  const member=selectedAgent(); if (!member || agentTab!=="policy" || (policyView?.saving && policyCurrent(policyView))) return;
  const keepOpen=!resetFollowups && policyView && policyCurrent(policyView) && $("policy-followups").open;
  const view={member:{agent_id:member.agent_id,membership_id:member.membership_id},info:{...state.info},objectRef:keepOpen ? policyView.objectRef : "",followupRequest:0,followupsLoaded:false};
  $("policy-followups").open=!!keepOpen;
  if(!keepOpen)$("followups-history").checked=false;
  policyView=view; policyEditor=null; for(const id of ["policy-save","policy-suggest","policy-new","policy-reload","policy-next"]) $(id).disabled=false; $("policy-form").hidden=true; $("policy-entries").replaceChildren(); $("policy-matters").replaceChildren(); $("policy-next").hidden=true; $("policy-status").textContent="正在读取…";
  $("followups-status").textContent="";
  try {
    const data=await rpc("/v1/policy/read",view.member,view.info);
    if (!policyCurrent(view)) return;
    view.data=data;renderPolicy(view);
    if($("policy-followups").open && !view.followupsLoaded && !view.followupsLoading)await loadPolicyFollowups(view);
  } catch(error) { if(policyCurrent(view)) $("policy-status").textContent=`读取失败：${error.message}`; }
}
async function savePolicyChange(view,change) {
  if(!policyCurrent(view) || view.saving || view.needsRefresh || !view.data) return;
  view.saving=true; for(const id of ["policy-save","policy-suggest","policy-new","policy-reload","policy-next"]) $(id).disabled=true;
  $("policy-status").textContent="正在保存…";
  try {
    await rpc("/v1/policy/update",{...view.member,policy:change},view.info);
    if(!policyCurrent(view)) return;
    view.saving=false; await loadMemberPolicy();
  } catch(error) { view.needsRefresh=true; if(policyCurrent(view)) $("policy-status").textContent=`保存失败或结果待核实：${error.message}。请刷新核对当前版本后再修改。`; }
  finally { view.saving=false; if(policyView===view) for(const id of ["policy-save","policy-suggest","policy-new","policy-reload","policy-next"]) $(id).disabled=(policyView===view && view.needsRefresh && ["policy-save","policy-suggest"].includes(id)) || false; }
}
async function savePolicyForm(status) {
  const editor=policyEditor; if(!editor || !policyCurrent(editor.view)) return;
  const c={id:editor.id,version:editor.record?.version || 0,revision:editor.view.data.revision,status,text:$("policy-text").value};
  if($("policy-expires").value) c.expires_at=new Date($("policy-expires").value).toISOString();
  c.source_text=`成员设置：${status === "active" ? "保存并生效" : "保存为建议"} · 长期规则\n${c.text}` + (c.expires_at ? `\n有效期至：${c.expires_at}` : "");
  await savePolicyChange(editor.view,c);
}
$("policy-new").addEventListener("click",()=>editPolicy());
$("policy-cancel").addEventListener("click",()=>{$("policy-form").hidden=true;policyEditor=null;});
$("policy-reload").addEventListener("click",()=>loadMemberPolicy());
$("policy-followups").addEventListener("toggle",()=>{
  const view=policyView;if(!view || !policyCurrent(view))return;
  if(!$("policy-followups").open){view.followupRequest++;view.followupsLoading=false;view.followupsLoaded=false;$("policy-matters").replaceChildren();$("followups-status").textContent="";$("policy-next").hidden=true;return;}
  if(!view.followupsLoaded && !view.followupsLoading)void loadPolicyFollowups(view);
});
$("followups-reload").addEventListener("click",()=>{if(policyView)void loadPolicyFollowups(policyView);});
$("followups-history").addEventListener("change",()=>{if(policyView)void loadPolicyFollowups(policyView);});
$("followups-all").addEventListener("click",()=>{if(policyView)void loadPolicyFollowups(policyView,"","");});
$("policy-next").addEventListener("click",()=>{if(policyView)void loadPolicyFollowups(policyView,policyView.matters?.next || "");});
$("policy-form").addEventListener("submit",event=>{event.preventDefault();return savePolicyForm("active");});
$("policy-suggest").addEventListener("click",()=>savePolicyForm("suggested"));
let inboxView = null;
function notificationParticipant(person) {
  if (!person) return "未知发送者";
  let name = person.name || (person.is_receiver ? "我" : person.type === "app" ? "机器人" : person.id?.value ? `飞书用户 · …${person.id.value.slice(-6)}` : "未知发送者");
  if (person.is_receiver && name !== "我") name += "（我）";
  return name;
}
function inboxSourceDetails(entry) {
  const item = entry.item, source = entry.source || {};
  const details = document.createElement("details"); details.className = "inbox-source-details";
  const summary = document.createElement("summary"); summary.textContent = "来源详情";
  const metadata = document.createElement("dl"); metadata.className = "inbox-source-fields";
  const add = (label, value, namespace) => {
    if (!value) return;
    const key = document.createElement("dt"), text = document.createElement("dd");
    key.textContent = label; text.textContent = value;
    if (namespace) text.title = namespace;
    metadata.append(key,text);
  };
  const id = (label, value) => add(label, value?.value, value?.namespace);
  const date = value => value && new Date(value).getTime() > 0 ? new Date(value).toLocaleString("zh-CN") : "";
  add("实例",item.instance); id("来源租户",item.tenant); id("接收账号租户",item.receiver?.tenant);
  add("项目",item.project); add("项目 ID",item.project_id && String(item.project_id));
  add("会话",item.conversation?.name); id("会话 ID",item.conversation?.id);
  if (item.sender) add("发送者",notificationParticipant(item.sender));
  id("发送者 ID",item.sender?.id); id("接收账号 ID",item.receiver?.id);
  add("消息时间",date(item.created_at)); add("接收时间",date(entry.received_at));
  id("消息 ID",item.message_id); id("回复消息",item.reply_to); id("根消息",item.root_id); id("话题 ID",item.thread_id);
  if (item.mentions?.length) add("提及",item.mentions.map(at => `${at.all ? "@所有人" : "@" + notificationParticipant(at)}${at.id?.value ? `（${at.id.value}）` : ""}`).join("、"));
  if (item.attachments?.length) add("附件",item.attachments.map(a => `${a.type} · ${a.reference} · ${a.status === "not_read" ? "未读取" : a.status}`).join("\n"));
  const statuses = {not_checked:"尚未解析",resolved:"已核实",partial:"部分核实",unavailable:"暂不可读",unsupported:"暂不支持",mismatch:"来源不匹配"};
  add("来源状态",source.status && (statuses[source.status] || source.status));
  add("原因",source.reason || source.reason_code);
  add("来源引用",source.source_ref); add("来源版本",source.source_version && String(source.source_version));
  add("对象引用",source.object_ref); add("目标引用",source.target_ref);
  if (item.structured === false) add("结构化信息","历史通知，仅保留已知信息；缺失字段未补全");
  if (item.truncated) add("内容状态","正文已截断，内容可能不完整");
  if (item.metadata_truncated) add("元数据状态","部分名称、提及或附件信息已截断");
  details.append(summary,metadata); return details;
}
async function loadMemberInbox(reset) {
  const member = selectedAgent();
  if (!member || agentTab !== "inbox") return;
  const prior = reset ? null : inboxView;
  const view = { member, info: state.info, snapshot: prior?.page?.snapshot_seq, after: prior?.page?.next_after || 0 };
  inboxView = view;
  $("inbox-status").textContent = "正在读取…";
  $("inbox-entries").replaceChildren(); $("inbox-next").hidden = true;
  $("inbox-reload").disabled = true;
  const current = () => inboxView === view && state.info?.room_id === view.info.room_id && state.info?.room_instance === view.info.room_instance && state.info?.service_epoch === view.info.service_epoch && selectedAgent()?.membership_id === member.membership_id && agentTab === "inbox" && $("activity-dialog").open;
  try {
    const data = await rpc("/v1/member/inbox", {agent_id:member.agent_id, membership_id:member.membership_id, after:view.after, ...(view.snapshot === undefined ? {} : {snapshot_seq:view.snapshot}), limit:25}, view.info);
    if (!current()) return;
    view.page = data;
    $("inbox-status").textContent = `已接收 ${data.status.latest_seq} 条 · 占用 ${data.status.bytes} / ${data.status.limits.bytes} 字节 · 当前快照 ${data.snapshot_seq}` + (data.status.write_error ? ` · ${data.status.write_error}` : "") + (!data.entries.length ? " · 暂无通知" : "");
    $("inbox-entries").replaceChildren(...data.entries.map(entry => {
      const section = document.createElement("section"); section.className = "activity-section";
      const item = entry.item;
      const source = item;
      const provider = source.provider === "lark" ? "飞书消息" : source.provider === "gitlab" ? `GitLab · ${{events:"项目活动",todos:"我的待办"}[source.channel] || source.channel}` : `${source.provider} / ${source.channel}`;
      const heading = document.createElement("header"); heading.className = "inbox-entry-header";
      const seq = document.createElement("span"); seq.className = "inbox-entry-seq"; seq.textContent = `#${entry.seq}`;
      const channel = document.createElement("span"); channel.className = "inbox-channel"; channel.textContent = provider;
      const time = document.createElement("time");
      const created = new Date(entry.item.created_at), received = new Date(entry.received_at);
      const date = created.getTime() > 0 ? created : received;
      if (Number.isFinite(date.getTime())) {
        time.dateTime = date.toISOString();
        time.textContent = date.toLocaleString("zh-CN", {month:"numeric",day:"numeric",hour:"2-digit",minute:"2-digit",hour12:false});
        time.title = `${created.getTime() > 0 ? "消息时间" : "接收时间"}：${date.toLocaleString("zh-CN")}`;
      } else time.textContent = "时间未获取";
      heading.append(seq,channel,time); section.append(heading);
      if (item.sender) {
        const sender = document.createElement("h3"); sender.className = "inbox-sender"; sender.textContent = notificationParticipant(item.sender) + (item.action ? ` · ${item.action}` : "");
        section.append(sender);
        if (item.changed) {
          const change = document.createElement("p"); change.className = "inbox-change quiet";
          change.textContent = "消息已变更 · 关联原消息，不自动重做已完成动作"; section.append(change);
        }
      }
      const body = document.createElement("div"); body.className = "message-text";
      const options = notificationOptions(view.info, state.operator, source, { seq: entry.seq, agent_id: member.agent_id, membership_id: member.membership_id });
      renderMessageBody(body, item.text, state.operator, source.provider === "lark" ? {...options, compactLarkMentions:true} : options);
      section.append(body);
      const context = item.conversation || item.context;
      if (context || source.provider === "lark") {
        const chat = document.createElement("p"); chat.className = "inbox-chat";
        const label = context?.name || "会话名称未获取";
        let url;
        try { url = new URL(context?.url); } catch { /* A source may have no original link. */ }
        if (url && ["https:","http:"].includes(url.protocol) && !url.username && !url.password) {
          const link = document.createElement("a"); link.textContent = label; link.href = url.href;
          link.target = "_blank"; link.rel = "noopener noreferrer"; chat.append(link);
        } else chat.textContent = label;
        section.append(chat);
      }
      if (item.truncated || item.metadata_truncated) {
        const notice = document.createElement("p"); notice.className = "inbox-change quiet";
        notice.textContent = item.truncated ? "正文已截断，内容可能不完整" : "部分名称、提及或附件信息已截断"; section.append(notice);
      }
      section.append(inboxSourceDetails(entry));
      return section;
    }));
    $("inbox-next").hidden = !data.has_more;
  } catch(error) { if (current()) $("inbox-status").textContent = `读取失败：${error.message}`; }
  finally { if (current()) $("inbox-reload").disabled = false; }
}
$("inbox-reload").addEventListener("click", () => loadMemberInbox(true));
$("inbox-next").addEventListener("click", () => loadMemberInbox(false));
function renderMemberActions(member, actions) {
  const execution = member.execution;
  const signature = JSON.stringify([member.agent_id, member.membership_id, member.status, execution?.paused, execution?.active,
    execution?.session_reset_required, state.connected, discussionBusy(), memberBusy.has(member.membership_id), member.unread_count, execution?.agent?.turn?.status]);
  if (actions.dataset.signature === signature) return;
  actions.dataset.signature = signature;
  actions.replaceChildren();
  if (!execution) return;
  const enable = execution.paused || execution.session_reset_required || member.status === "backend_blocked" || member.status === "budget_exhausted";
  const add = (label, action, cancel) => {
    const button = document.createElement("button"); button.type = "button"; button.textContent = label;
    button.setAttribute("aria-label", `${label} ${member.agent_id}`);
    button.disabled = memberBusy.has(member.membership_id) || !state.connected || discussionBusy() || ["rotating", "checkpoint_failed"].includes(member.status);
    button.addEventListener("click", () => controlMember(member, action, cancel)); actions.append(button);
  };
  add(enable ? "启用协作" : "暂停", enable ? "run" : "pause", false);
  if (execution.active) add("取消回合", "pause", true);
}

$("activity-member").addEventListener("change", () => {
  const member = state.members.find(item => item.membership_id === $("activity-member").value);
  if (member) openMemberActivity(member, $("activity-member"));
});

function renderExecutionHint() {
  const available = state.connected && state.info && !discussionBusy();
  const running = available ? state.members.filter(member => member.execution?.active || member.status === "running") : [];
  const waiting = available ? state.members.filter(member => member.status === "waiting" && !member.execution?.active && !member.execution?.paused) : [];
  const names = members => members.map(member => memberRole(member)?.name || member.agent_id).join("、");
  // Active covers Session opening, model work and tools; it does not identify a thinking phase.
  const text = running.length ? `${names(running)} 正在处理…` : waiting.length ? `${names(waiting)} 正在等待执行…` : "";
  const queue = running.length && waiting.length ? `${names(waiting)} 等待执行` : "";
  const hint = $("execution-hint");
  const primary = running.length ? running : waiting;
  const phaseText = running.length ? "正在处理…" : "正在等待执行…";
  const signature = JSON.stringify([text, queue, primary.map(member => member.membership_id)]);
  if (hint.dataset.signature === signature) return;
  const follow = nearBottom();
  hint.dataset.signature = signature;
  hint.hidden = !text;
  hint.dataset.phase = running.length ? "running" : "waiting";
  const content = $("execution-text");
  content.replaceChildren();
  for (const [index, member] of primary.entries()) {
    if (index) {
      const separator = document.createElement("span"); separator.textContent = "、"; content.append(separator);
    }
    const button = document.createElement("button");
    const name = memberRole(member)?.name || member.agent_id;
    button.type = "button"; button.className = "execution-member";
    button.textContent = primary.length === 1 ? `${name} ${phaseText}` : name;
    button.dataset.activityMember = member.membership_id;
    button.setAttribute("aria-label", `查看详情 ${name}`);
    button.title = `查看 ${name} 的详情`;
    button.addEventListener("click", () => openMemberActivity(member, button));
    content.append(button);
  }
  if (primary.length > 1) {
    const phase = document.createElement("span"); phase.textContent = ` ${phaseText}`; content.append(phase);
  }
  $("execution-queue").textContent = queue;
  $("execution-queue").hidden = !queue;
  hint.title = [text, queue].filter(Boolean).join(" · ");
  $("timeline-wrap").classList.toggle("has-activity", !!text);
  if (follow) $("timeline").scrollTop = $("timeline").scrollHeight;
}

function renderMembers() {
  renderActivityControls();
  renderExecutionHint();
  $("member-count").textContent = state.members.length;
  $("member-empty").hidden = state.members.length > 0;
  // Keep focus and controls stable when polling reports no visible changes.
  const signature = JSON.stringify(state.members) + [...memberBusy].join(",") + state.connected + discussionBusy();
  if ($("members").dataset.signature === signature) return;
  $("members").dataset.signature = signature;
  const fragment = document.createDocumentFragment();
  for (const member of state.members) {
    const el = $("member-template").content.firstElementChild.cloneNode(true);
    const execution = member.execution;
    const running = execution?.active;
    const label = running ? "运行中" : statusNames[member.status] || member.status;
    const role = memberRole(member);
    el.querySelector(".member-name").textContent = role?.name || member.agent_id;
    const summary = el.querySelector(".member-summary");
    summary.dataset.activityMember = member.membership_id;
    summary.setAttribute("aria-label", `查看 ${role?.name || member.agent_id} 的详情`);
    summary.disabled = !state.connected;
    summary.addEventListener("click", () => openMemberActivity(member, summary));
    el.querySelector(".avatar").textContent = member.agent_id.slice(0, 2).toUpperCase();
    el.querySelector(".member-profile").textContent = role ? member.agent_id : member.runner.profile;
    const status = el.querySelector(".member-status");
    status.textContent = label;
    status.classList.toggle("running", !!running);
    status.classList.toggle("failed", /failed|blocked|unavailable|interrupted/.test(member.status));
    const budget = execution?.turn_budget;
    el.querySelector(".member-details").textContent = `${member.unread_count} 条未读 · 已读至 #${member.read_through}` +
      (budget ? `\n回合 ${budget.used} / ${budget.limit}` : "");
    if (execution?.error) {
      const error = el.querySelector(".member-error");
      error.hidden = false;
      error.textContent = execution.error;
    }
    fragment.append(el);
  }
  $("members").replaceChildren(fragment);
}

function renderProfiles(profiles) {
  if (JSON.stringify(profiles) === JSON.stringify(state.profiles) && $("profile").options.length) return;
  state.profiles = profiles;
  const selected = $("profile").value;
  $("profile").replaceChildren(...profiles.map((name) => new Option(name, name)));
  if (profiles.includes(selected)) $("profile").value = selected;
  $("add-member").disabled = profiles.length === 0 || discussionBusy();
  notice("profile-hint", profiles.length ? "新成员默认暂停，添加后点击「启用协作」。" : "还没有可用角色。请在启动服务时通过 --dsh-config 指定配置文件。");
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

function renderCreateSelection() {
  const team = selectedTeam();
  $("team-description").textContent = team?.description || "暂无可用团队。请在团队目录添加 team.json，并重启服务。";
  $("team-members").replaceChildren(...(team?.members || []).map(member => {
    const item = document.createElement("li"); item.textContent = `${member.name} · ${member.id}`; return item;
  }));
  const disabled = !state.catalogConnected || discussionBusy() || !!state.management || state.managementSending;
  $("team").disabled = disabled || !state.teams.length;
  $("new-room-name").disabled = disabled;
  $("create-room").disabled = disabled || !team;
  $("create-room").textContent = state.managementSending ? "正在创建…" : "创建并进入";
  $("team-hint").textContent = "成员与会话独立创建。开场消息仅填入草稿，由你发送。";
}

function saveDraft() {
  if (!state.info) return;
  state.draft.text = $("message-input").value;
  state.draft.replyLabel = $("reply-label").textContent;
  drafts.save(state.info, state.operator, state.draft);
}

function showDraft() {
  $("message-input").value = state.draft.text || "";
  $("reply-label").textContent = state.draft.replyLabel || `引用 #${state.draft.replyTo}`;
  $("reply-context").hidden = !state.draft.replyTo;
  notice("send-error", state.draft.pending ? "有一条尚未确认的消息，重试保留原房间和服务身份。请先核对历史。" : "");
}

function selectView(info, operator) {
  if (sameService(state.info, info) && state.operator === operator) return false;
  saveDraft();
  closeRename(false);
  $("notification-detail").close();
  viewVersion++;
  activity.close(); $("activity-dialog").close(); activitySignature = "";
  activityMember = null;
  $("room-menu").open = false;
  state.info = info; state.operator = operator; state.connected = false;
  resetMentionViewing();
  if (info) restoreMentionProgress();
  state.draft = info ? drafts.get(info, operator) : {};
  state.messages.clear(); state.lastSeq = 0; state.clearedThrough = 0; state.restart = null; state.members = [];
  restartNoticeID = ""; restartNoticeExpires = 0;
  showDraft(); renderMessages(); renderMembers();
  $("message-count").textContent = "0 条消息";
  $("jump-latest").hidden = true;
  notice("restart-notice", "");
  return true;
}

function saveManagement() {
  try {
    if (state.management) storage?.setItem(managementKey, JSON.stringify(state.management));
    else storage?.removeItem(managementKey);
  } catch { /* Retain deduplication IDs in memory. */ }
}

function seedCreated(operation, room) {
  drafts.seed(room, operation.operator, operation.opening || "");
  operation.room = room;
  saveManagement();
  if (state.info?.room_instance === room.room_instance) showDraft();
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
    if (!room) {
      forgetRoom(op.payload);
      state.management = null; state.managementError = "房间及其 Agent 私有会话数据已删除。"; saveManagement();
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
  if (state.info?.room_instance === room.room_instance) selectView(null, state.operator);
  drafts.remove(room, state.operator);
  try { storage?.removeItem(workspaceKey(room, state.operator)); } catch { /* Storage is optional. */ }
  try { storage?.removeItem(JSON.stringify(["teamx.mentions", room.room_instance, state.operator])); } catch { /* Storage is optional. */ }
}

function roomListTime(room) {
  return room.last_message_at || room.created_at;
}

function renderRooms() {
  document.querySelector(".workspace").classList.toggle("managed", state.managed);
  renderPanels();
  $("open-settings").disabled = !state.catalogConnected;
  $("toggle-add").hidden = state.managed;
  if (state.managed) $("add-form").hidden = true;
  if (!state.managed || !state.catalog) return;
  const catalog = state.catalog;
  $("rooms-empty").hidden = catalog.rooms.length > 0;
  $("new-room").disabled = !state.catalogConnected || discussionBusy() || !!state.management || state.managementSending || !!catalog.write_error;
  const signature = JSON.stringify({ rooms: catalog.rooms, selected: selectedRoom,
    busy: discussionBusy(), management: !!state.management, sending: state.managementSending, connected: state.catalogConnected });
  if ($("room-list").dataset.signature !== signature) {
    $("room-list").dataset.signature = signature;
    $("room-list").replaceChildren(...[...catalog.rooms].sort((a,b) => new Date(roomListTime(b)) - new Date(roomListTime(a))).map(room => {
      const row = document.createElement("div"); row.className = "room-entry";
      const button = document.createElement("button"); button.type = "button"; button.className = "room-choice";
      button.setAttribute("aria-current", String(room.room_instance === selectedRoom?.room_instance));
      const name = document.createElement("strong"), updated = document.createElement("span");
      name.textContent = room.name;
      updated.textContent = room.status === "deleting" ? (room.busy ? "正在删除…" : `删除待重试${room.error ? ` · ${room.error}` : ""}`)
        : room.status === "creating" ? `创建待完成${room.error ? ` · ${room.error}` : " · 可重试原创建请求"}` : room.runtime_status === "opening" ? "正在打开…"
        : room.runtime_error ? `运行受阻 · ${room.runtime_error}`
        : room.busy ? "房间操作进行中…"
        : `${room.runtime_status === "ready" ? "运行中 · " : "未打开 · "}${new Date(roomListTime(room)).toLocaleString("zh-CN", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })}`;
      button.append(name, updated);
      button.disabled = room.status !== "ready" || !state.catalogConnected || !!catalog.busy;
      button.addEventListener("click", () => startSelection(room));
      const remove = document.createElement("button"); remove.type = "button"; remove.className = "room-delete";
      remove.textContent = room.status === "deleting" ? "重试删除" : "删除";
      remove.setAttribute("aria-label", `删除房间「${room.name}」`);
      remove.disabled = !state.catalogConnected || !!catalog.busy || !!room.busy || !!state.management || !!catalog.write_error;
      remove.addEventListener("click", () => startDeletion(room));
      row.append(button, remove);
      return row;
    }));
  }
  let text = state.managementError || catalog.write_error || "";
  if (workspaceView?.data?.operation?.status === "running") text = `Workspace · ${workspaceStages[workspaceView.data.operation.phase] || "正在应用"}`;
  else if (state.management?.kind === "delete" && selectedEntry()?.busy) text = "正在停止房间、结清会话并删除数据…";
  else if (catalog.busy) text = "共享资源操作进行中…";
  else if (state.managementSending) text = "正在提交房间操作…";
  else if (state.management) text = [text, state.management.room && state.management.kind === "create"
    ? "房间已创建，点击进入。开场草稿已保留。" : "操作结果尚未确认。可核对列表后用原请求重试；不会自动续跑。"].filter(Boolean).join(" ");
  notice("management-notice", text);
  $("retry-management").hidden = !state.management || catalog.busy || state.managementSending;
  $("retry-management").disabled = !state.catalogConnected;
  $("retry-management").textContent = state.management?.kind === "create" && state.management.room ? "进入已创建房间" : "核对并重试原请求";
  $("dismiss-management").hidden = !state.management || catalog.busy || state.managementSending;
}

async function runManagement() {
  const op = state.management;
  if (!op || state.managementSending || !state.catalogConnected || state.catalog.busy) return;
  state.managementSending = true; state.managementError = "";
  renderRooms(); composerState(); renderMembers(); renderCreateSelection();
  try {
    const manage = (path, payload) => request(path, { service_epoch: state.catalog.service_epoch,
      caller: { kind: "human", id: op.operator }, ...payload });
    if (op.kind === "create") {
      const room = op.room || await manage("/v1/rooms/create", op.payload);
      if (room.status !== "ready") throw new Error("房间创建尚未完成，请核查后重试。");
      seedCreated(op, room);
      op.kind = "open"; op.selectionVersion ??= selectionVersion; op.payload = { open_id: crypto.randomUUID(), room_id: room.room_id, room_instance: room.room_instance };
      saveManagement();
    }
    const result = await manage(op.kind === "delete" ? "/v1/rooms/delete" : "/v1/rooms/open", op.payload);
    if (op.kind === "open") finishOpen(op, result);
    $("create-dialog").close();
  } catch (error) {
    if (state.management === op) {
      state.managementError = op.kind === "delete" ? `删除结果尚未确认，请核对房间列表。${error.message}` : `操作未确认，房间和历史已保留。${error.message}`;
      // A definitive rejection has no execution to recover. Other failures keep
      // the original ID, including a create interrupted after index publication.
      if (["request", "identity", "conflict", "capacity"].includes(error.code)) {
        state.management = null; saveManagement();
      }
    }
    notice("create-error", state.managementError);
  } finally {
    state.managementSending = false;
    await refresh(); renderRooms(); composerState(); renderCreateSelection();
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
  selectionVersion++;
  state.management = { kind: "open", selectOnComplete: true, selectionVersion, operator: state.operator, payload: {
    open_id: crypto.randomUUID(), room_id: room.room_id, room_instance: room.room_instance } };
  saveManagement(); void runManagement();
}

function startDeletion(room) {
  if (state.management || room.busy || !state.catalogConnected || state.catalog.busy || state.catalog.write_error) return;
  if (!window.confirm(`永久删除房间「${room.name}」？\n将停止该房间并删除公共消息、所有 Agent 私有会话（含历史会话）、诊断和房间工作区文件。此操作无法撤销。`)) return;
  state.management = { kind: "delete", operator: state.operator, payload: {
    room_id: room.room_id, room_instance: room.room_instance } };
  saveManagement(); void runManagement();
}

function composerState() {
  const namedRoom = state.catalog?.rooms.find(room => room.room_instance === state.info?.room_instance);
  $("rename-room").setAttribute("aria-label", namedRoom ? `${namedRoom.name}，修改房间名称` : $("room-name").textContent);
  $("rename-room").disabled = !state.managed || !state.catalogConnected || discussionBusy() || !!state.catalog?.write_error || namedRoom?.status !== "ready" || renameSending;
  $("save-room-name").disabled = $("rename-room").disabled || !renameTarget;
  $("rename-room-name").disabled = renameSending;
  $("save-room-name").textContent = renameSending ? "保存中…" : "保存";
  renderExecutionHint();
  $("message-input").disabled = !state.info;
  $("message-input").readOnly = !!state.draft.pending || discussionBusy();
  $("send").disabled = !state.connected || state.draft.sending || discussionBusy() || (!state.draft.pending && !$("message-input").value.trim());
  $("send").textContent = state.draft.sending ? "发送中…" : state.draft.pending ? "重试发送 ↗" : "发送 ↗";
  $("discard-send").hidden = !state.draft.pending || state.draft.sending;
  $("restart-discussion").disabled = !state.connected || workspaceBusy() || managementBusy() || state.draft.restartStarting || state.restart?.status === "running" || !!state.draft.pending || state.draft.sending;
  $("restart-discussion").textContent = state.draft.restartStarting || state.restart?.status === "running" ? "重新开始中…" : state.draft.restartRequest ? "核对并重试" : "重新开始";
  $("toggle-add").disabled = !state.connected || discussionBusy();
  $("add-member").disabled = !state.connected || !state.profiles.length || discussionBusy();
  renderRestart(); renderWorkspace();
}

async function sync() {
  let version = viewVersion;
  try {
    let config = await request("/v1/ui");
    if (!currentView(version)) return;
    state.managed = !!config.managed;
    if (state.managed) {
      const catalog = await request("/v1/rooms");
      if (!currentView(version)) return;
      state.catalog = catalog; state.catalogConnected = true;
      if (!selectionLoaded) {
        selectionLoaded = true;
        if (!selectedRoom) {
          const initial = catalog.rooms[0];
          if (initial) selectRoom(initial);
        }
      }
      await reconcileManagement(catalog);
      version = viewVersion;
      const room = selectedEntry(catalog);
      config = { ...room?.service, operator: catalog.operator };
      if (room?.service) selectView(room.service, catalog.operator);
      version = viewVersion;
      renderTeams(catalog.teams || []); renderRooms();
      if (!state.info) state.operator = catalog.operator;
      await syncWorkspace(catalog);
      if (!currentView(version)) return;
      if (workspaceView?.data?.observation && workspaceBusy()) {
        const observation = workspaceView.data.observation;
        selectView(observation, catalog.operator); version = viewVersion;
        $("room-name").textContent = roomName();
        const initial = state.messages.size === 0, stick = initial || nearBottom(), scrollTop = $("timeline").scrollTop;
        const read = await readPages(initial ? { mode: "latest", latest: 50 } : { mode: "range", after: state.lastSeq }, observation);
        if (!currentView(version)) return;
        const cleared = applyClear(read.clearedThrough);
        if (read.messages.length || cleared) {
          for (const message of read.messages) state.messages.set(message.seq, message);
          renderMessages();
          if (stick) bottom(); else $("timeline").scrollTop = scrollTop;
        }
        state.lastSeq = read.snapshot;
        $("message-count").textContent = `${read.snapshot - state.clearedThrough} 条消息`;
        state.connected = false;
        $("connection").textContent = "Workspace 设置待完成 · 可查看历史";
        activity.close(); $("activity-dialog").close(); renderMembers(); return;
      }
      if (catalog.busy || room?.busy) {
        // Keep the old discussion visible but invalidate every in-flight view.
        state.connected = false; viewVersion++;
        activity.close(); $("activity-dialog").close();
        $("connection").textContent = "房间操作进行中";
        renderMembers(); return;
      }
      if (!room?.service) {
        selectView(null, catalog.operator);
        $("room-name").textContent = room?.name || "选择或新建房间";
        $("empty").querySelector("h2").textContent = "开始一个新任务";
        $("empty").querySelector("p").textContent = state.teams.length ? "从左侧新建房间，选择团队开始协作。" : "暂无可用团队。请在团队目录添加 team.json，并重启服务。";
        $("connection").textContent = room ? (room.runtime_error ? `运行受阻 · ${room.runtime_error}` : "点击房间打开") : "已连接 · 尚未选择房间";
        notice("sync-error", ""); return;
      }
    }
    const info = { room_id: config.room_id, room_instance: config.room_instance, service_epoch: config.service_epoch };
    const changedRoom = selectView(info, config.operator);
    version = viewVersion;
    restoreMentionProgress();
    if (!state.managed) { renderProfiles(config.profiles || []); renderRooms(); }
    $("operator").textContent = config.operator;
    $("room-name").textContent = roomName();
    $("empty").querySelector("h2").textContent = "从一条消息开始";
    $("empty").querySelector("p").textContent = "发送任务或想法，让房间里的 Agent 一起参与。";
    const members = await rpc("/v1/member/list", {}, info);
    if (!currentView(version)) return;
    state.restart = members.discussion_restart || null;
    if (state.draft.restartRequest && state.draft.restartRequest.id === state.restart?.id) {
      state.draft.restartRequest = null; state.draft.restartError = ""; saveDraft();
    }
    const initial = state.messages.size === 0;
    const stick = initial || nearBottom();
    const scrollTop = $("timeline").scrollTop;
    const read = await readPages(initial ? { mode: "latest", latest: 50 } : { mode: "range", after: state.lastSeq }, info);
    if (!currentView(version) || read.clearedThrough < state.clearedThrough) return;
    const cleared = applyClear(read.clearedThrough);
    if (read.messages.length || changedRoom || cleared) {
      for (const message of read.messages) state.messages.set(message.seq, message);
      renderMessages();
      if (stick) bottom();
      else { $("timeline").scrollTop = scrollTop; $("jump-latest").hidden = false; }
    }
    state.lastSeq = read.snapshot;
    renderMentionNotice();
    $("message-count").textContent = `${read.snapshot - state.clearedThrough} 条消息`;
    state.members = members.members;
    state.connected = true;
    renderMembers(); void activity.refresh();
    $("connection").textContent = "已连接 · 自动刷新";
    $("connection").classList.remove("offline");
    notice("sync-error", members.write_error ? `房间写入暂停：${members.write_error}` : "");
  } catch (error) {
    if (!currentView(version)) return;
    state.connected = false; state.catalogConnected = false;
    $("connection").textContent = "连接中断 · 正在重连";
    $("connection").classList.add("offline");
    notice("sync-error", `暂时无法同步，已有消息保留。${error.message}`);
    $("members").dataset.signature = ""; renderMembers();
  } finally { composerState(); renderCreateSelection(); renderRooms(); }
}

function renderActivity({ loading, data, error }, selected) {
  const signature = JSON.stringify({ loading, data, error: error?.message, selected: selected.membership_id });
  if (signature === activitySignature) return;
  activitySignature = signature;
  const scrollMember = JSON.stringify([selected.info, selected.membership_id]);
  if (activityScrollMember !== scrollMember) {
    activityFollowing = true; activityScrollTop = 0;
  } else if (!$("activity-output").hidden) {
    activityFollowing = activityNearBottom(); activityScrollTop = $("activity-body").scrollTop;
  }
  const follow = activityFollowing, scrollTop = activityScrollTop;
  activityScrollMember = scrollMember;
  $("activity-body").setAttribute("aria-busy", String(!!loading));
  $("activity-state").hidden = !data;
  $("activity-timeline-empty").hidden = true;
  $("activity-timeline-truncated").hidden = true;
  if (loading || error) {
    $("activity-output").hidden = true;
    $("activity-jump-latest").hidden = true;
    notice("activity-notice", loading ? "正在读取私有活动…" : `私有活动读取失败，将自动重试。${error.message}`);
    return;
  }
  const member = data.member, execution = member.execution;
  $("activity-status").textContent = execution?.active ? "运行中" : statusNames[member.status] || member.status;
  $("activity-progress").textContent = `已读至 #${member.read_through} · ${member.unread_count} 条未读`;
  const budget = execution?.turn_budget;
  $("activity-budget").textContent = budget ? `${budget.used} / ${budget.limit}` : "暂不可用";
  const turn = execution?.agent?.turn;
  $("activity-turn").textContent = execution?.agent ? `${execution.agent.turn_number}` +
    (turn ? ` · ${statusNames[turn.status] || turn.status}` : " · 尚未运行") : "暂不可用";
  notice("activity-execution-error", execution?.error || "");
  const live = !!data.current_output, output = data.current_output || data.last_output;
  if (!output) {
    $("activity-output").hidden = true;
    $("activity-jump-latest").hidden = true;
    notice("activity-notice", data.output_error || (execution?.active ? "回合已启动，等待模型或工具活动…" : "暂无私有活动。"));
    return;
  }
  notice("activity-notice", "");
  $("activity-output").hidden = false;
  const result = output.result;
  $("activity-output-title").textContent = live ? "当前回合 · 运行中" : "最近完成的回合";
  const reasons = { end_turn: "正常结束", cancelled: "已取消", max_tokens: "达到 token 上限", refusal: "拒绝执行" };
  $("activity-stop").textContent = live ? (result.stopReason ? "正在结清" : "运行中") : reasons[result.stopReason] || result.stopReason || "结束原因未记录";
  notice("activity-output-error", output.error || "");
  if (!Array.isArray(result.events)) {
    $("activity-output").hidden = true;
    $("activity-jump-latest").hidden = true;
    notice("activity-notice", "活动记录格式无效，无法显示。");
    return;
  }
  renderActivityTimeline(result, JSON.stringify([selected.info, selected.membership_id, execution?.agent?.turn_number]));
  $("activity-timeline").hidden = false;
  notice("activity-timeline-empty", result.events.length ? "" : live ? "等待模型或工具活动…" : "该回合没有活动记录。");
  $("activity-timeline-truncated").hidden = !result.eventsTruncated;
  restoreActivityScroll(follow, scrollTop);
}

function activityNearBottom() {
  const body = $("activity-body");
  return body.scrollHeight - body.scrollTop - body.clientHeight < 90;
}

function restoreActivityScroll(follow, top) {
  const body = $("activity-body");
  body.scrollTop = follow ? body.scrollHeight : top;
  activityScrollTop = body.scrollTop; activityFollowing = follow || activityNearBottom();
  $("activity-jump-latest").hidden = activityFollowing;
}

$("activity-body").addEventListener("scroll", () => {
  // A failed read temporarily hides output and clamps the browser's scrollTop.
  // Keep the reading position until a successful snapshot restores it.
  if ($("activity-output").hidden) return;
  activityScrollTop = $("activity-body").scrollTop; activityFollowing = activityNearBottom();
  $("activity-jump-latest").hidden = activityFollowing;
});
$("activity-jump-latest").addEventListener("click", () => restoreActivityScroll(true, 0));

function renderActivityTimeline(result, context) {
  const timeline = $("activity-timeline");
  if (activityTimelineContext !== context) {
    activityTimelineContext = context;
    activityTimelineRows.clear(); timeline.replaceChildren();
  }
  const retained = new Set(result.events.map(event => event.seq));
  // Only retention eviction removes existing nodes. Ordinary refreshes update
  // content in place, preserving focus, scroll and both open and closed rows.
  if ([...activityTimelineRows.keys()].some(seq => !retained.has(seq))) {
    for (const seq of activityTimelineRows.keys()) if (!retained.has(seq)) activityTimelineRows.delete(seq);
    timeline.replaceChildren(...[...activityTimelineRows.values()].map(row => row.details));
  }
  const statuses = { pending: "等待中", in_progress: "执行中", completed: "已完成", failed: "失败" };
  for (const event of result.events) {
    let row = activityTimelineRows.get(event.seq);
    if (!row) {
      const details = document.createElement("details"), summary = document.createElement("summary");
      const title = document.createElement("span"), status = document.createElement("span"), body = document.createElement("div");
      details.className = "activity-group"; details.dataset.seq = String(event.seq); details.open = event.kind === "text";
      status.className = "activity-event-status"; body.className = "activity-group-content";
      summary.append(title, status); details.append(summary, body);
      const text = document.createElement("div"), inputLabel = document.createElement("p"), input = document.createElement("pre");
      const outputLabel = document.createElement("p"), output = document.createElement("pre"), truncated = document.createElement("p");
      text.className = "activity-text"; inputLabel.className = outputLabel.className = truncated.className = "activity-caption";
      inputLabel.textContent = "参数"; outputLabel.textContent = "结果";
      body.append(text, inputLabel, input, outputLabel, output, truncated);
      row = { details, title, status, text, inputLabel, input, outputLabel, output, truncated };
      activityTimelineRows.set(event.seq, row); timeline.append(details);
    }
    const tool = event.tool;
    row.title.textContent = tool ? `工具 · ${tool.title || tool.id || "未知调用"}` : event.kind === "thought" ? "思考" : "内部回复";
    row.status.textContent = tool ? statuses[tool.status] || tool.status || "状态未记录" : "";
    row.status.hidden = !tool;
    row.text.textContent = event.text || ""; row.text.hidden = !!tool;
    row.inputLabel.hidden = row.input.hidden = !tool?.input;
    row.outputLabel.hidden = row.output.hidden = !tool?.output;
    row.input.textContent = tool?.input || ""; row.output.textContent = tool?.output || "";
    row.truncated.textContent = tool ? "调用内容已截断" : "内容超出保留上限，文本已截断。";
    row.truncated.hidden = !(event.textTruncated || tool?.detailsTruncated);
  }
}

$("close-activity").addEventListener("click", () => $("activity-dialog").close());
$("activity-dialog").addEventListener("close", () => {
  activity.close(); memberToolsView = null; activityMember = null; agentTab = "activity"; activitySignature = "";
  activityScrollMember = ""; activityFollowing = true; activityScrollTop = 0; $("activity-jump-latest").hidden = true;
  activityTimelineContext = ""; activityTimelineRows.clear(); $("activity-timeline").replaceChildren();
  renderActivityControls();
  const opener = !panels.members ? null : activityFocus?.element?.isConnected ? activityFocus.element
    : [...document.querySelectorAll("[data-activity-member]")].find(button => button.dataset.activityMember === activityFocus?.membership);
  if (!$("notification-detail").open) (opener || $("toggle-members")).focus();
  activityFocus = null;
});
$("activity-dialog").addEventListener("keydown", (event) => {
  if (event.key === "Escape") { event.preventDefault(); $("activity-dialog").close(); }
});

function refresh() {
  if (!refreshTask) refreshTask = sync().finally(() => { refreshTask = null; });
  return refreshTask;
}

function clearReply() {
  state.draft.replyTo = 0;
  $("reply-context").hidden = true;
  saveDraft();
}

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
  if (!state.draft.restartRequest && !window.confirm("重新开始讨论将取消并结清所有成员当前的回合，清空公共消息，并重置所有成员的私有会话。成员、角色和工作区文件保留，回合预算不重置。是否继续？")) return;
  const info = state.info, operator = state.operator, draft = state.draft, version = viewVersion;
  draft.restartRequest ||= { id: crypto.randomUUID().replaceAll("-", ""), info, previous_id: state.restart?.id || "" };
  restartNoticeID = draft.restartRequest.id; restartNoticeExpires = 0;
  draft.restartStarting = true; draft.restartError = ""; saveDraft();
  composerState(); renderMembers(); renderCreateSelection(); renderRooms();
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
    await refresh(); composerState(); renderMembers(); renderCreateSelection();
  }
});

$("compose").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (state.draft.sending || discussionBusy() || !state.connected) return;
  const text = $("message-input").value;
  if (!state.draft.pending && !text.trim()) return;
  if (!state.draft.pending && new TextEncoder().encode(text).length > 16384) {
    notice("send-error", "消息过长，请控制在 16 KiB 内。"); return;
  }
  const info = state.info, operator = state.operator, draft = state.draft, version = viewVersion;
  draft.pending ||= { text, client_message_id: crypto.randomUUID(), reply_to: draft.replyTo, info, operator };
  saveDraft(); draft.sending = true;
  composerState(); notice("send-error", "");
  try {
    const pending = draft.pending;
    const receipt = await rpc("/v1/send", { text: pending.text, client_message_id: pending.client_message_id,
      reply_to: pending.reply_to, caller: { kind: "human", id: pending.operator } }, pending.info);
    draft.pending = null; draft.text = ""; draft.replyTo = 0; draft.replyLabel = "";
    drafts.save(info, operator, draft);
    if (state.draft === draft) showDraft();
    if (currentView(version)) {
      $("send-success").textContent = `消息 #${receipt.seq} 已发送`;
      await refresh(); if (currentView(version)) bottom();
    }
  } catch (error) {
    if (currentView(version)) notice("send-error", error.code === "identity"
      ? "原发送身份已失效。请先核对本房间历史，再放弃重试；不会自动换用新身份发送。"
      : `未确认发送成功，重试保留原房间身份与消息 ID。${error.message}`);
  } finally {
    draft.sending = false;
    if (currentView(version)) { composerState(); $("message-input").focus(); }
  }
});

$("discard-send").addEventListener("click", () => {
  state.draft.pending = null; saveDraft(); composerState();
  notice("send-error", "已放弃重试。原消息可能已提交，请先查看消息记录，再决定是否重新发送。");
});
$("message-input").addEventListener("input", () => { saveDraft(); composerState(); });
$("message-input").addEventListener("keydown", (event) => {
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
$("cancel-reply").addEventListener("click", () => { if (!state.draft.pending) clearReply(); });
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
  const info = state.info, version = viewVersion;
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

async function controlMember(member, action, cancel) {
  const info = state.info, version = viewVersion;
  memberBusy.add(member.membership_id); renderMembers();
  try {
    const identity = { agent_id: member.agent_id, membership_id: member.membership_id };
    const failedUnread = member.unread_count > 0 && ["failed", "interrupted"].includes(member.execution?.agent?.turn?.status);
    await rpc(`/v1/member/${action}`, { ...identity, cancel, retry: action === "run" && failedUnread }, info);
    await refresh();
  } catch (error) { if (currentView(version)) notice("sync-error", `${member.agent_id} 操作未确认，请查看成员状态。${error.message}`); }
  finally { memberBusy.delete(member.membership_id); renderMembers(); }
}

$("toggle-add").addEventListener("click", () => {
  $("add-form").hidden = !$("add-form").hidden;
  $("toggle-add").setAttribute("aria-expanded", String(!$("add-form").hidden));
  if (!$("add-form").hidden) $("agent-name").focus();
});
$("add-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const agent = $("agent-name").value.trim(), profile = $("profile").value;
  if (!agent || !profile || $("add-member").disabled) return;
  const info = state.info, version = viewVersion;
  $("add-member").disabled = true; notice("add-error", "");
  try {
    await rpc("/v1/member/add", { agent_id: agent, runner: { kind: "dsh", profile } }, info);
    if (!currentView(version)) return;
    $("agent-name").value = ""; $("add-form").hidden = true;
    $("toggle-add").setAttribute("aria-expanded", "false");
    await refresh();
  } catch (error) { if (currentView(version)) notice("add-error", `添加未确认，请先查看成员列表；相同名称可以重试。${error.message}`); }
  finally { composerState(); }
});

let renameTarget = null, renameSending = false;
function closeRename(focus = true) {
  renameTarget = null; $("rename-form").hidden = true; $("room-heading").hidden = false;
  if (focus) $("rename-room").focus();
}
$("rename-room").addEventListener("click", () => {
  if ($("rename-room").disabled) return;
  const room = state.catalog.rooms.find(room => room.room_instance === state.info?.room_instance);
  renameTarget = { room_id: room.room_id, room_instance: room.room_instance, expected_name: room.name,
    service_epoch: state.catalog.service_epoch, caller: { kind: "human", id: state.operator } };
  $("rename-room-name").value = room.name; notice("rename-error", "");
  $("room-heading").hidden = true; $("rename-form").hidden = false;
  composerState(); $("rename-room-name").focus();
});
$("cancel-rename").addEventListener("click", () => closeRename());
$("rename-form").addEventListener("keydown", event => {
  if (event.key === "Escape" && !event.isComposing) { event.preventDefault(); closeRename(); }
});
$("rename-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!renameTarget || $("save-room-name").disabled) return;
  const name = $("rename-room-name").value.trim();
  if (!name) { notice("rename-error", "请输入房间名称。"); return; }
  if (new TextEncoder().encode(name).length > 256) { notice("rename-error", "房间名称请控制在 256 字节内。"); return; }
  const target = renameTarget, version = viewVersion;
  renameSending = true; notice("rename-error", ""); composerState();
  try {
    await request("/v1/rooms/rename", { ...target, name });
    if (currentView(version) && renameTarget === target) closeRename();
  } catch (error) {
    if (currentView(version) && renameTarget === target) notice("rename-error", error.code === "conflict"
      ? "名称已在其他页面修改，请取消后重新编辑。"
      : "保存未确认，请取消并核对房间名称后再修改。");
  } finally {
    renameSending = false; await refresh(); composerState();
    if (currentView(version) && !renameTarget) $("rename-room").focus();
  }
});

$("new-room").addEventListener("click", () => {
  if ($("new-room").disabled) return;
  notice("create-error", ""); $("new-room-name").value = "";
  renderCreateSelection(); $("create-dialog").showModal(); $("team").focus();
});
$("close-create").addEventListener("click", () => $("create-dialog").close());
$("create-dialog").addEventListener("close", () => $("new-room").focus());
$("team").addEventListener("change", renderCreateSelection);
$("create-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const team = selectedTeam();
  if (!team || $("create-room").disabled) return;
  const name = $("new-room-name").value.trim();
  if (new TextEncoder().encode(name).length > 256) { notice("create-error", "房间名称请控制在 256 字节内。"); return; }
  state.management = { kind: "create", selectOnComplete: true, selectionVersion, operator: state.operator, opening: team.opening_message,
    payload: { create_id: crypto.randomUUID(), name, team_id: team.id } };
  saveManagement(); void runManagement();
});
$("retry-management").addEventListener("click", () => { void runManagement(); });
$("dismiss-management").addEventListener("click", () => {
  if (state.catalog?.busy || state.managementSending) return;
  state.management = null; saveManagement(); state.managementError = ""; void refresh();
});
// Connection forms live only in memory. Neither a PAT nor its request is
// persisted or retried automatically after a timeout.
const connections = { items: [], selected: null, sending: false, generation: 0 };
const connectionValidationErrors = { credential_unavailable: "凭据不可用，请检查服务环境变量。", credential_rejected: "认证失败，请核对当前账号登录和权限。", identity_mismatch: "profile 账号或应用已变化，请核对后重新验证。", permission_denied: "当前用户缺少读取权限，请检查授权。", cli_version_mismatch: "CLI 版本不匹配，需要已验收的 1.0.99。", cli_unavailable: "CLI 无法执行，请核对路径与后台运行权限。", query_failed: "连接失败，请检查地址或网络后重试验证。", invalid_response: "实例返回了无效的身份信息。" };
function connectionFeedback(message = "", kind = "status") {
  const feedback = $("connections-feedback");
  feedback.textContent = message;
  feedback.dataset.kind = kind;
  feedback.setAttribute("role", kind === "error" ? "alert" : "status");
}
const connectionInputs = ["connection-name", "connection-url", "connection-executable", "connection-profile", "connection-env", "connection-pat"];
function clearConnectionErrors() {
  for (const id of connectionInputs) $(id).setAttribute("aria-invalid", "false");
}
function connectionInputError(id, message) {
  $(id).setAttribute("aria-invalid", "true");
  connectionFeedback(message, "error");
  $(id).focus();
}
function connectionAuthFields() {
  const isLark = $("connection-provider").value === "lark";
  const env = $("connection-auth").value === "env";
  for (const id of ["connection-executable", "connection-profile"]) $(id).setAttribute("aria-required", String(isLark));
  clearConnectionErrors();
  connectionFeedback();
  $("connection-profile-row").hidden = !isLark;
  $("connection-auth-row").hidden = isLark;
  $("connection-env-row").hidden = isLark || !env; $("connection-pat-row").hidden = isLark || env;
  if (env) $("connection-pat").value = "";
  else $("connection-env").value = "";
}
function connectionPayload(extra) {
  return { service_epoch: state.catalog.service_epoch, caller: { kind: "human", id: state.operator }, ...extra };
}
function renderConnections() {
  $("connections-empty").hidden = connections.items.length > 0;
  $("connections-list").replaceChildren(...connections.items.map(item => {
    const button = document.createElement("button"); button.type = "button"; button.className = "connection-row";
    const label = document.createElement("span"), name = document.createElement("strong"), detail = document.createElement("small"), status = document.createElement("span");
    name.textContent = item.name; detail.textContent = `${item.provider === "lark" ? "飞书" : "GitLab"} · ${item.url}`;
    status.textContent = item.validation.status === "valid" ? "已验证 ›" : item.validation.status === "failed" ? "验证失败 ›" : "未验证 ›";
    label.append(name, detail); button.append(label, status);
    button.addEventListener("click", () => editConnection(item)); return button;
  }));
}
async function loadConnections() {
  const generation = ++connections.generation;
  // Keep the current content while reading; only a failed read needs an action.
  $("connections-list-view").setAttribute("aria-busy", "true");
  $("connections-retry").disabled = true;
  $("connections-retry").textContent = "重试中…";
  try {
    const query = new URLSearchParams({ service_epoch: state.catalog.service_epoch, caller_id: state.operator });
    const data = await request(`/v1/platform/connections?${query}`);
    if (generation !== connections.generation) return;
    connections.items = data.connections; renderConnections();
    $("connections-load-error").hidden = true;
    if (!$("connections-list-view").hidden) connectionFeedback();
  } catch {
    if (generation === connections.generation) {
      $("connections-empty").hidden = true;
      $("connections-load-error").hidden = false;
    }
  } finally {
    if (generation === connections.generation) {
      $("connections-list-view").setAttribute("aria-busy", "false");
      $("connections-retry").disabled = false;
      $("connections-retry").textContent = "重试";
    }
  }
}
function showConnectionList() {
  connections.selected = null; $("connection-pat").value = "";
  $("connection-form").hidden = true; $("connections-list-view").hidden = false;
  $("connections-load-error").hidden = true;
  connectionFeedback();
}
function editConnection(item = null) {
  clearConnectionErrors();
  connections.selected = item;
  $("connection-form").hidden = false; $("connections-list-view").hidden = true;
  $("connection-title").textContent = item ? "编辑连接" : "新增连接";
  $("connection-name").value = item?.name || "";
  $("connection-provider").value = item?.provider || "gitlab";
  $("connection-provider").disabled = !!item;
  $("connection-url").value = item?.url || ""; $("connection-url").readOnly = !!item;
  $("connection-auth").value = item?.credential_kind === "env" ? "env" : "pat";
  $("connection-brand").value = item?.cli_profile?.brand || "feishu";
  $("connection-profile").value = item?.cli_profile?.profile || "";
  $("connection-executable").value = item?.cli_profile?.executable || "";
  $("connection-env").value = item?.token_env || ""; $("connection-pat").value = "";
  $("connection-pat").placeholder = item ? "留空保留现有 Token；输入新值替换" : "输入 PAT";
  $("connection-credential").hidden = !!item?.credential_configured;
  $("connection-notes").hidden = !item;
  const v = item?.validation;
  $("connection-validation").textContent = v?.status === "valid" ? `已验证 · @${v.username}`
    : v?.status === "failed" ? connectionValidationErrors[v.error_code] || "验证失败。" : "尚未验证连接。";
  $("connection-validation").title = [v?.user_id ? `账号 ID ${v.user_id}` : "", v?.checked_at ? `验证于 ${new Date(v.checked_at).toLocaleString("zh-CN")}` : ""].filter(Boolean).join(" · ");
  $("connection-references").textContent = item?.references?.length ? `引用房间：${item.references.map(ref => ref.name + (ref.pending ? "（待完成绑定）" : "")).join("、")}` : "";
  $("connection-references").hidden = !item?.references?.length;
  $("connection-delete").disabled = !!item?.references?.length;
  for (const id of ["connection-delete", "connection-verify"]) $(id).hidden = !item;
  connectionFeedback(); connectionAuthFields();
  $("connection-fields").disabled = false; $("connection-name").focus();
}
function connectionDirty() {
  const item = connections.selected;
  return !item || $("connection-name").value.trim() !== item.name || (item.provider === "lark" ? JSON.stringify(larkProfileDraft()) !== JSON.stringify(item.cli_profile) : $("connection-auth").value !== item.credential_kind) ||
    $("connection-pat").value !== "" || ($("connection-auth").value === "env" && $("connection-env").value !== item.token_env);
}
async function connectionAction(action) {
  if (connections.sending || !state.catalog) return;
  const item = connections.selected;
  if (action !== "save" && !item) return;
  if (action === "verify" && connectionDirty()) { connectionFeedback("请先保存更改，再验证连接。", "error"); return; }
  if (action === "delete" && !window.confirm(`删除「${item.name}」连接？`)) return;
  let payload = { id: item?.id || crypto.randomUUID().replaceAll("-", ""), version: item?.version || 0 };
  if (action === "save") {
    clearConnectionErrors();
    const auth = $("connection-auth").value;
    payload = { ...payload, name: $("connection-name").value.trim(), provider: $("connection-provider").value, url: $("connection-url").value.trim() };
    if (payload.provider === "lark") {
      const profileName = $("connection-profile").value.trim();
      if (!profileName) { connectionInputError("connection-profile", "连接未保存：请填写已登录的 profile。运行 lark-cli whoami --as user 查看 profile 字段。"); return; }
      if (!/^[A-Za-z0-9_.-]{1,128}$/.test(profileName) || profileName === "." || profileName === "..") {
        connectionInputError("connection-profile", "连接未保存：profile 请使用 CLI 返回的名称，限 1～128 位字母、数字、下划线、点或连字符。"); return;
      }
      const executable = $("connection-executable").value.trim();
      if (!executable.startsWith("/") || /[\x00\r\n]/.test(executable)) {
        connectionInputError("connection-executable", "连接未保存：请填写 lark-cli 可执行文件的绝对路径，例如 /opt/homebrew/bin/lark-cli。"); return;
      }
      if (!item) payload.snapshot_version = 2;
      const profile = larkProfileDraft();
      if (!item || JSON.stringify(profile) !== JSON.stringify(item.cli_profile)) payload.cli_profile = profile;
    } else if (auth === "env") {
      const tokenEnv = $("connection-env").value.trim();
      if (!tokenEnv) { connectionInputError("connection-env", "连接未保存：请填写服务环境变量名称。"); return; }
      if (!item || item.credential_kind !== "env" || item.token_env !== tokenEnv) payload.token_env = tokenEnv;
    } else if ($("connection-pat").value) payload.pat = $("connection-pat").value;
    else if (!item || item.credential_kind !== "pat") { connectionInputError("connection-pat", "连接未保存：请填写 PAT。"); return; }
  }
  if ((payload.pat || payload.token_env || payload.cli_profile) && item?.references?.length) {
    if (!window.confirm(`替换「${item.name}」凭据？影响房间：${item.references.map(ref => ref.name).join("、")}。通知将暂停，需显式同步。`)) return;
    payload.referenced_rooms = item.references.map(({ room_id, room_instance }) => ({ room_id, room_instance }));
  }
  connections.sending = true; $("connection-fields").disabled = true;
  $("connection-back").disabled = true; $("close-settings").disabled = true;
  connectionFeedback(action === "verify" ? "正在验证…" : action === "delete" ? "正在删除…" : "正在保存…");
  try {
    const result = await request(`/v1/platform/connections/${action}`, connectionPayload(payload), undefined, 40000);
    if (action === "delete") { showConnectionList(); await loadConnections(); connectionFeedback("连接已删除。", "success"); }
    else if (action === "save") {
      // Use the confirmed save response immediately; an earlier list read must
      // not replace it with an older version after returning to the list.
      connections.generation++;
      let index = connections.items.findIndex(item => item.id === result.id);
      if (index < 0) { index = connections.items.length; connections.items.push(result); }
      else connections.items[index] = result;
      showConnectionList(); renderConnections();
      $("connections-list-view").setAttribute("aria-busy", "false");
      connectionFeedback(result.provider === "lark" && result.validation.status !== "valid" ? "连接已保存，尚未验证。请打开该连接，点击「验证连接」。" : "连接已保存。", "success");
      $("connections-list").children[index].focus();
    } else { editConnection(result); connectionFeedback(result.validation.status === "valid" ? "连接验证成功。" : connectionValidationErrors[result.validation.error_code] || "验证失败。", result.validation.status === "valid" ? "success" : "error"); }
  } catch (error) {
    // Reconcile only by explicit list refresh. Never resend a secret request.
    const errors = { conflict: "连接已变化，请返回列表重新加载后再编辑。", request: payload.provider === "lark" ? "连接未保存：请检查连接名称、CLI 绝对路径、profile，以及品牌与实例地址是否匹配。" : "连接未保存：请检查连接名称、HTTPS 实例地址及认证来源。", identity: "服务身份已变化，请刷新页面后重试。", capacity: "平台连接数量已达上限。", busy: "管理操作或旧查询尚未结清，请返回列表核对连接和通知状态，再显式重试。" };
    connectionFeedback(errors[error.code] || "操作结果未确认，请返回列表核对；不会自动重发。", "error");
  } finally {
    delete payload.pat; $("connection-pat").value = "";
    connections.sending = false; $("connection-fields").disabled = false;
    $("connection-back").disabled = false; $("close-settings").disabled = false;
  }
}
$("open-settings").addEventListener("click", async () => {
  if (!state.managed || !state.catalog) return;
  showConnectionList(); $("settings-dialog").showModal(); await loadConnections();
});
$("close-settings").addEventListener("click", () => $("settings-dialog").close());
$("settings-dialog").addEventListener("cancel", event => { if (connections.sending) event.preventDefault(); });
$("settings-dialog").addEventListener("close", () => { connections.generation++; $("connection-pat").value = "";
  if ($("notification-detail").open) { $(workspaceView?.notificationChannel === "todos" ? "gitlab-todos-manage-connections" : "gitlab-events-manage-connections").focus(); void loadWorkspaceConnections(); }
  else $("open-settings").focus(); });
$("connection-new").addEventListener("click", () => editConnection());
$("connection-back").addEventListener("click", async () => { showConnectionList(); await loadConnections(); });
$("connections-retry").addEventListener("click", loadConnections);
$("connection-auth").addEventListener("change", connectionAuthFields);
$("connection-provider").addEventListener("change", () => {
  if (!connections.selected) $("connection-url").value = $("connection-provider").value === "lark" ? "https://open.feishu.cn" : "";
  connectionAuthFields();
});
$("connection-brand").addEventListener("change", () => { if (!connections.selected) $("connection-url").value = $("connection-brand").value === "lark" ? "https://open.larksuite.com" : "https://open.feishu.cn"; });
function larkProfileDraft() {return {version:1, executable:$("connection-executable").value.trim(), profile:$("connection-profile").value.trim(), brand:$("connection-brand").value, cli_version:"1.0.99"};}
window.addEventListener("pagehide", () => { $("connection-pat").value = ""; });
$("connection-form").addEventListener("submit", event => { event.preventDefault(); return connectionAction("save"); });
$("connection-verify").addEventListener("click", () => connectionAction("verify"));
$("connection-delete").addEventListener("click", () => connectionAction("delete"));

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
await refresh();
setInterval(() => { void refresh(); }, 1500);
document.addEventListener("visibilitychange", () => { updateMentionViewing(); if (!document.hidden) void refresh(); });
document.addEventListener("toggle", scheduleMentionViewing, true);
document.addEventListener("close", scheduleMentionViewing, true);
window.addEventListener("pagehide", resetMentionViewing);
window.addEventListener("pagehide", saveDraft);
