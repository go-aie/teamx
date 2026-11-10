import { request, readPages as collectPages, createActivityReader, createRoomDrafts, sameService, mentionRanges, unseenMentions } from "/ui/client.mjs";
import { renderMessageBody, notificationOptions } from "/ui/markdown.mjs";
import { createSystemTime, createTimeInput } from "/ui/time.mjs";

const $ = (id) => document.getElementById(id);
const state = { info: null, operator: "operator", messages: new Map(), lastSeq: 0, oldest: 0,
  clearedThrough: 0,
  members: [], profiles: [], teams: [], connected: false, managed: false, catalog: null, catalogConnected: false,
  management: null, managementSending: false, managementError: "", draft: {}, restart: null };
let refreshTask = null;
let historyLoading = false;
const systemTime = createSystemTime();
const timeInputs = new Map(["lark-backfill-start", "gitlab-backfill-start", "onboarding-backfill-start", "policy-expires"].map(id => [id, createTimeInput(systemTime)]));
const timezoneForm = {dirty:false, saving:false, message:""};
let settingsTab = "general";
const timezonePicker = {open:false, query:"", active:-1, candidates:[], shown:[]};
let browserTimeZone = "";
try { browserTimeZone = Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch { /* Detection is optional. */ }
function formatDate(value, options) { return systemTime.format(value, options); }
function bindSystemTime(element, render) {
  element.dataset.systemTime = ""; element.refreshSystemTime = render; render();
}
function fillTimeInput(id, value) { $(id).value = timeInputs.get(id).fill(value); }
function readTimeInput(id) {
  if ($(id).validity?.badInput) throw new Error("请输入完整有效的时间；若系统时区已变更，请重新确认或修改。");
  return timeInputs.get(id).read($(id).value);
}
for (const [id, input] of timeInputs) {
  const edit = () => input.edit($(id).value, $(id).validity?.badInput);
  $(id).addEventListener("input", edit);
  $(id).addEventListener("change", edit);
}
function timeZoneHasChanges() { return $("system-timezone").value.trim() !== systemTime.zone; }
function canSaveTimeZone() {
  return !timezoneForm.saving && !!systemTime.epoch && !!$("system-timezone").value.trim() && timeZoneHasChanges();
}
function isTimeZoneDraftHint() { return ["有未保存的更改", "已填入浏览器时区，保存后生效。"].includes(timezoneForm.message); }
function renderTimeZoneFeedback() {
  const draftHint = isTimeZoneDraftHint();
  $("timezone-draft-hint").hidden = !draftHint;
  $("timezone-draft-hint").textContent = draftHint ? "有未保存的更改" : "";
  $("timezone-feedback").textContent = draftHint ? "" : timezoneForm.message;
}
function renderTimeZone() {
  $("timezone-current").textContent = systemTime.error || `当前生效：${systemTime.zone}`;
  $("timezone-current").dataset.kind = systemTime.error ? "error" : "";
  if (!timezoneForm.dirty) $("system-timezone").value = systemTime.zone || "";
  $("system-timezone").disabled = timezoneForm.saving;
  $("timezone-picker-toggle").disabled = timezoneForm.saving;
  $("timezone-save").disabled = !canSaveTimeZone();
  $("timezone-save").textContent = timezoneForm.saving ? "保存中…" : "保存时区";
  $("timezone-browser").hidden = !browserTimeZone || browserTimeZone === systemTime.zone;
  $("timezone-browser").disabled = timezoneForm.saving;
  $("timezone-browser").textContent = `使用浏览器时区（${browserTimeZone}）`;
  if (!timeZoneHasChanges() && isTimeZoneDraftHint()) timezoneForm.message = "";
  renderTimeZoneFeedback();
  let supported = [];
  try { supported = Intl.supportedValuesOf("timeZone"); } catch { /* The server validates manually entered names too. */ }
  timezonePicker.candidates = [...new Set(["Asia/Shanghai", "UTC", systemTime.zone, browserTimeZone, ...supported])].filter(Boolean);
  if (timezoneForm.saving) timezonePicker.open = false;
  renderTimeZoneOptions(); renderSettingsNavigation();
  notice("timezone-error", systemTime.error);
}
function renderTimeZoneOptions() {
  const picker = timezonePicker, query = picker.query.trim().toLowerCase();
  picker.shown = picker.candidates.filter(zone => zone.toLowerCase().includes(query));
  if (picker.active >= picker.shown.length) picker.active = -1;
  $("timezone-options").hidden = !picker.open;
  for (const id of ["system-timezone", "timezone-picker-toggle"]) $(id).setAttribute("aria-expanded", String(picker.open));
  $("system-timezone").setAttribute("aria-activedescendant", picker.open && picker.active >= 0 ? `timezone-option-${picker.active}` : "");
  if (!picker.open) return;
  const nodes = picker.shown.map((zone,index) => {
    const option = document.createElement("button"); option.type = "button"; option.id = `timezone-option-${index}`;
    option.setAttribute("role", "option"); option.setAttribute("tabindex", "-1"); option.setAttribute("aria-selected", String(zone === $("system-timezone").value));
    option.dataset.active = String(index === picker.active); option.textContent = zone;
    option.addEventListener("click", () => chooseTimeZone(zone)); return option;
  });
  if (!nodes.length) { const empty=document.createElement("p"); empty.textContent="未找到候选时区，可输入完整 IANA 名称后保存验证。"; nodes.push(empty); }
  $("timezone-options").replaceChildren(...nodes);
}
function chooseTimeZone(zone) {
  $("system-timezone").value = zone; timezoneForm.dirty = true; timezoneForm.message = timeZoneHasChanges() ? "有未保存的更改" : "";
  timezonePicker.open = false; renderTimeZone(); $("system-timezone").focus();
}
function settingsNavigationBusy() {
  return timezoneForm.saving || platforms.sending || !!platforms.auth || !!platforms.gitlabAuth || !!platforms.scionAuth ||
    !!platforms.larkStarting || !!platforms.gitlabStarting || !!platforms.scionStarting;
}
function renderSettingsNavigation() {
  if (!state.managed) settingsTab = "general";
  $("general-settings").hidden = settingsTab !== "general";
  $("platform-settings").hidden = settingsTab !== "platforms";
  $("settings-tab-platforms").hidden = !state.managed;
  $("settings-navigation").setAttribute("aria-orientation", window.innerWidth <= 640 ? "horizontal" : "vertical");
  for (const tab of ["general", "platforms"]) {
    const button = $(`settings-tab-${tab}`), selected = settingsTab === tab;
    button.setAttribute("aria-selected", String(selected)); button.setAttribute("tabindex", selected ? "0" : "-1");
    button.disabled = settingsNavigationBusy();
  }
}
function selectSettingsTab(tab) {
  if (settingsNavigationBusy() || tab === "platforms" && !state.managed) return;
  settingsTab = tab; timezonePicker.open = false; renderTimeZoneOptions(); renderSettingsNavigation();
}
for (const tab of ["general", "platforms"]) {
  $(`settings-tab-${tab}`).addEventListener("click", () => selectSettingsTab(tab));
  $(`settings-tab-${tab}`).addEventListener("keydown", event => {
    if (!["ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key) || settingsNavigationBusy()) return;
    event.preventDefault();
    const next = event.key === "Home" || !state.managed ? "general" : event.key === "End" ? "platforms" : tab === "general" ? "platforms" : "general";
    selectSettingsTab(next); $(`settings-tab-${next}`).focus();
  });
}
function applyTimeZone(data) {
  if (!systemTime.update(data)) return;
  for (const [id, input] of timeInputs) {
    if ($(id).validity?.badInput) { input.edit("", true); input.rebase(); }
    else $(id).value = input.rebase();
  }
  for (const prefix of ["lark", "gitlab"]) $(`${prefix}-backfill-start`).max = systemTime.local(Date.now());
  $("policy-expires-hint").textContent = `按系统时区 ${systemTime.zone || "（异常）"} 输入；留空则不设到期时间。`;
  renderTimeZone();
  const top = $("timeline").scrollTop;
  renderMessages(); $("timeline").scrollTop = top;
  for (const element of document.querySelectorAll("[data-system-time]")) element.refreshSystemTime?.();
  renderRooms(); renderWorkspace();
  if (policyEditor && ($("policy-expires").value || $("policy-expires").validity?.badInput)) {
    try { readTimeInput("policy-expires"); }
    catch (error) { $("policy-status").textContent = error.message; }
  }
}
$("system-timezone").addEventListener("input", () => {
  timezoneForm.dirty = true; timezoneForm.message = timeZoneHasChanges() ? "有未保存的更改" : ""; renderTimeZoneFeedback();
  $("timezone-save").disabled = !canSaveTimeZone();
  timezonePicker.open = true; timezonePicker.query = $("system-timezone").value; timezonePicker.active = -1; renderTimeZoneOptions();
});
$("system-timezone").addEventListener("click", () => {
  if (timezoneForm.saving || timezonePicker.open) return;
  timezonePicker.open = true; timezonePicker.query = ""; timezonePicker.active = -1; renderTimeZoneOptions();
});
$("timezone-picker-toggle").addEventListener("click", () => { timezonePicker.open = !timezonePicker.open; timezonePicker.query = ""; timezonePicker.active = -1; renderTimeZoneOptions(); });
$("system-timezone").addEventListener("keydown", event => {
  const picker = timezonePicker;
  if (event.key === "Escape" && picker.open) { event.preventDefault(); event.stopPropagation?.(); picker.open = false; renderTimeZoneOptions(); }
  else if (["ArrowDown", "ArrowUp"].includes(event.key)) {
    event.preventDefault(); if (!picker.open) { picker.open = true; picker.query = ""; }
    const count = picker.candidates.filter(zone => zone.toLowerCase().includes(picker.query.trim().toLowerCase())).length;
    picker.active = count ? (picker.active + (event.key === "ArrowDown" ? 1 : picker.active < 0 ? 0 : -1) + count) % count : -1;
    renderTimeZoneOptions(); if (picker.active >= 0) $(`timezone-option-${picker.active}`).scrollIntoView({block:"nearest"});
  } else if (event.key === "Enter" && picker.open && picker.active >= 0) { event.preventDefault(); chooseTimeZone(picker.shown[picker.active]); }
});
document.addEventListener("pointerdown", event => { if (timezonePicker.open && !event.target.closest?.("#timezone-picker")) { timezonePicker.open = false; renderTimeZoneOptions(); } });
$("timezone-browser").addEventListener("click", () => { $("system-timezone").value = browserTimeZone; timezoneForm.dirty = true; timezoneForm.message = "已填入浏览器时区，保存后生效。"; renderTimeZone(); });
$("timezone-form").addEventListener("submit", async event => {
  event.preventDefault();
  if (!canSaveTimeZone()) return;
  const identity = state.managed ? {service_epoch:systemTime.epoch} : {...state.info};
  const epoch = systemTime.epoch;
  timezoneForm.saving = true; timezoneForm.message = ""; renderTimeZone();
  try {
    const data = await request("/v1/settings/save", {...identity, caller:{kind:"human",id:state.operator}, expected_version:systemTime.version, time_zone:$("system-timezone").value.trim()});
    if (epoch !== systemTime.epoch) return;
    applyTimeZone({...data, service_epoch:epoch});
    timezoneForm.dirty = false;
    timezoneForm.message = `已保存；当前生效：${systemTime.zone}。影响全部 Room 和 Agent；页面立即生效，Agent 从下一回合生效，无需重启。`;
  } catch (error) {
    if (epoch !== systemTime.epoch) return;
    timezoneForm.message = `保存失败，草稿已保留：${error.message}`;
    if (error.code === "conflict") {
      try { const current = await request("/v1/settings"); if (current.service_epoch === epoch && systemTime.epoch === epoch) applyTimeZone(current); } catch { /* Keep the draft for explicit retry. */ }
    }
  } finally { timezoneForm.saving = false; renderTimeZone(); }
});
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
const panels = { rooms: true, members: true, roomWidth: null, memberWidth: null };
let sidebarResize = null;
try {
  const saved = JSON.parse(storage?.getItem(panelsKey));
  for (const side of ["rooms", "members"]) if (typeof saved?.[side] === "boolean") panels[side] = saved[side];
  for (const key of ["roomWidth", "memberWidth"]) if (Number.isFinite(saved?.[key]) && saved[key] > 0) panels[key] = saved[key];
} catch { /* Sidebars remain usable when tab storage is unavailable. */ }

// Workspace state is scoped to a Room instance + operator, independent of the
// member-service epoch. Only bounded operation IDs reach browser storage.
// Workspace connection choices stay in memory until an explicit application.
let memberToolsView = null;
let workspaceView = null;
let notificationNavigation = 0;
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
  const notificationEpoch=JSON.stringify([state.catalog?.service_epoch,room?.service?.service_epoch]);
  if (key === workspaceView?.key) {
    if(!workspaceView)return;
    if(workspaceView.notificationEpoch!==notificationEpoch){
      notificationNavigation++;stopLarkChatSearch();$("notification-detail").close();
      // Workspace operation recovery keeps its durable key; notification forms
      // and requests must start afresh across manager or Room service epochs.
      for(const field of Object.keys(workspaceView))if(/^(notification|gitlab|lark|events|todos|messages|memberSource|memberPlatform|memberTool)/.test(field))delete workspaceView[field];
      delete workspaceView.allSubscriptions;delete workspaceView.retiredSources;
      delete workspaceView.allPlatforms;memberToolsView=null;
      workspaceView.notificationEpoch=notificationEpoch;
      if($("activity-dialog").open && selectedAgent())void openMemberTools(selectedAgent());
    }
    return;
  }

  closeWorkspacePicker();
  memberToolsView = null;
  $("notification-detail").close();
  $("workspace-dialog").close();
  workspaceView = room ? { room: { room_id: room.room_id, room_instance: room.room_instance }, operator, key,
    notificationEpoch, data: null, sending: false, pending: null, closeOnComplete: null, error: "", formVersion: null, missing: false, connected: false } : null;
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
  if (!ws.platforms) await loadWorkspacePlatforms();
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
    state.catalog?.service_epoch === v.identity.service_epoch && v.runtimeEpoch===state.info?.service_epoch &&
    state.operator === v.identity.caller.id && state.info?.room_id===v.identity.room_id && state.info?.room_instance === v.identity.room_instance;
}
function toolsIdentity() {
  return { service_epoch: state.catalog.service_epoch, room_id: state.info.room_id,
    room_instance: state.info.room_instance, caller: { kind: "human", id: state.operator } };
}
async function openMemberTools(member) {
  if (!state.managed || !state.info || !state.catalogConnected) {
    renderNotificationPlatforms();return;
  }
  const v = memberToolsView = { identity: toolsIdentity(), runtimeEpoch:state.info.service_epoch, member, cli: null, cliConnected:false };
  await loadCLITools(v);
}
// CLI bindings have a separate revision and never inherit legacy grants.
async function loadCLITools(v) {
  const read = v.cliRead = (v.cliRead || 0) + 1;
  try {
    const q = new URLSearchParams({ service_epoch: v.identity.service_epoch, caller_id: v.identity.caller.id,
      room_id: v.identity.room_id, room_instance: v.identity.room_instance });
    const cq = new URLSearchParams({ service_epoch: v.identity.service_epoch, caller_id: v.identity.caller.id });
    const [data, platforms] = await Promise.all([request(`/v1/tools/bindings?${q}`), request(`/v1/platforms?${cq}`)]);
    if (!toolsCurrent(v) || read !== v.cliRead) return;
    v.cli = data;v.cliConnected=true; v.cliPlatforms = platforms.platforms; if(workspaceView)workspaceView.allPlatforms=platforms.platforms; v.cliResult = null;
    renderMemberTools(v);
  } catch {
    if (!toolsCurrent(v) || read !== v.cliRead) return;
    v.cliConnected=false;
    renderMemberTools(v);
  }
}
function toolBinding(v, item) {
  const name=typeof item==="string" ? item : v?.cli?.catalog?.find(d=>d.provider===item.provider)?.name;
  return v?.cli?.bindings?.find(b=>b.agent_id===v.member.agent_id && b.membership_id===v.member.membership_id && b.tool===name && (typeof item==="string" ? !b.platform_id : b.platform_id===item.id));
}
function toolIssue(v,item) {
  const selection=v?.cli?.selections?.find(s=>s.agent_id===v.member.agent_id && s.membership_id===v.member.membership_id && s.platform_id===item.id);
  const unavailable=toolBinding(v,item) && selection?.unavailable_reason ? ({executable_unavailable:"程序缺失，已保留启用设置；修复后会重新检查。",account_unavailable:"账号暂不可用，已保留启用设置，请重新核验。",platform_changed:"平台已变化，需重新启用。"}[selection.unavailable_reason] || "工具暂不可用，已保留启用设置。") : "";
  return [v?.cliResult?.tool===item.id ? v.cliResult.message : "",unavailable].filter(Boolean).join(" ");
}
function toolSwitch(v,item) {
  const label=document.createElement("label"),input=document.createElement("input"),text=document.createElement("span");
  const d=typeof item==="string" ? v?.cli?.catalog?.find(d=>d.name===item) : v?.cli?.catalog?.find(d=>d.provider===item.provider);
  const key=typeof item==="string" ? item : item.id,b=toolBinding(v,item);
  label.className="member-platform-tool workspace-check";input.type="checkbox";input.className="notification-switch";input.setAttribute("role","switch");
  input.setAttribute("aria-label",`${typeof item==="string" ? item : item.name} 使用工具`);
  input.checked=v?.cliPending?.tool===key ? v.cliPending.enabled : !!b;
  input.disabled=!v?.cli || !v.cliConnected || !d || !!workspaceView?.memberToolBusy || onboarding.busy || !state.connected || !state.catalogConnected || discussionBusy() || (!b && typeof item!=="string" && item.validation?.status!=="valid");
  text.textContent="工具";label.append(input,text);
  input.addEventListener("change",()=>changeMemberTool(v,item,input.checked));return label;
}
async function changeMemberTool(v,item,enabled) {
  const ws=workspaceView;if(!toolsCurrent(v) || !v.cliConnected || ws.memberToolBusy || onboarding.busy)return;
  const d=typeof item==="string" ? v.cli.catalog.find(d=>d.name===item) : v.cli.catalog.find(d=>d.provider===item.provider);
  if(!d || enabled && typeof item!=="string" && item.validation?.status!=="valid")return;
  const key=typeof item==="string" ? item : item.id,b=toolBinding(v,item);
  const binding={agent_id:v.member.agent_id,membership_id:v.member.membership_id,tool:d.name,...(typeof item!=="string" ? {platform_id:item.id} : {})};
  if(enabled && d.provider==="lark")binding.action_authorization={version:1,revision:b?.action_authorization?.revision || 1,grants:(d.actions || []).filter(s=>!s.disabled_reason).flatMap(s=>(s.object_types || []).map(object_type=>({operation:s.operation,target_kind:s.target_kind,object_type})))};
  else if(enabled && b?.action_authorization)binding.action_authorization=structuredClone(b.action_authorization);
  ws.memberToolBusy=true;v.cliPending={tool:key,enabled};v.cliResult=null;v.cliRead=(v.cliRead || 0)+1;renderMemberTools(v);
  let message="";
  try {
    const next=await request("/v1/tools/bind",{...v.identity,revision:v.cli.revision,binding,enabled,platform_version:enabled && typeof item!=="string" ? item.version : 0},undefined,binding.action_authorization?.grants.some(g=>d.actions?.some(s=>s.operation===g.operation && s.binding_evidence)) ? 40000 : 10000);
    if(toolsCurrent(v))v.cli={...v.cli,...next};
    message=`${typeof item==="string" ? item : item.name} 工具${enabled ? "已启用" : "已停用"}。`;
  } catch(error) {
    message=["conflict","identity","request","capacity"].includes(error.code) ? "保存失败，请核对当前成员、平台与权限后重试。" : "保存结果未确认，请核对读回状态；不会自动重发。";
  } finally {
    ws.memberToolBusy=false;v.cliPending=null;
    // A read resolves unknown local writes; no bind request is replayed.
    if(toolsCurrent(v)){await loadCLITools(v);if(!v.cliConnected)message+=" 当前状态暂不可读，请重新读取。";v.cliResult={tool:key,message,error:!v.cliConnected || /失败|未确认/.test(message)};renderMemberTools(v);}
    else if(memberToolsView?.cli && toolsCurrent(memberToolsView))await loadCLITools(memberToolsView);
  }
}
function renderMemberTools(v) {
  if(!toolsCurrent(v))return;
  renderNotificationPlatforms();
}
function memberPlatformItems(ws=workspaceView,v=memberToolsView) {
  const items=new Map((ws?.allPlatforms || notificationPlatforms(ws)).map(p=>[p.id,p]));
  for(const view of [...(ws?.gitlabPlatforms || []),...(ws?.larkSources || [])]){
    const id=view.platform_id || view.config.platform_id;
    if(id && !items.has(id))items.set(id,{id,name:"原平台暂不可用",provider:view.provider || "gitlab",validation:{status:"unavailable"}});
  }
  // Keep unavailable and explicitly disabled bindings inspectable.
  for(const b of [...(v?.cli?.bindings || []),...(v?.cli?.disabled || [])]) {
    if(b.agent_id!==v.member.agent_id || b.membership_id!==v.member.membership_id || !b.platform_id || items.has(b.platform_id))continue;
    const d=v.cli.catalog.find(d=>d.name===b.tool);items.set(b.platform_id,{id:b.platform_id,name:"原平台暂不可用",provider:d?.provider,validation:{status:"unavailable"}});
  }
  return [...items.values()];
}
function memberHasPlatform(item,target,v=memberToolsView) {
  return notificationConfigured(item,notificationView(workspaceView,item,target)) || [...(v?.cli?.bindings || []),...(v?.cli?.disabled || [])].some(b=>b.agent_id===target.agent_id && b.membership_id===target.membership_id && b.platform_id===item.id);
}
function layoutMemberPlatforms() {
  const list=$("member-notification-platforms"),width=list.getBoundingClientRect().width;
  if(!width)return; // The inspector or this tab may be hidden until it is opened.
  list.classList.add("member-platform-measuring");
  const required=Math.max(0,...Array.from(list.children,row=>
    row.children[0].getBoundingClientRect().width + row.children[1].getBoundingClientRect().width +
    (Number.parseFloat(window.getComputedStyle(row).columnGap) || 0)
  ));
  list.classList.remove("member-platform-measuring");
  list.dataset.stacked=String(required>width);
}
if(typeof ResizeObserver!=="undefined")new ResizeObserver(layoutMemberPlatforms).observe($("member-notification-platforms"));
document.fonts?.ready.then(layoutMemberPlatforms);
function renderMemberPlatforms() {
  const ws=workspaceView,target=memberNotificationTarget(),v=memberToolsView;
  $("member-notification-sources").hidden=!state.managed || !ws || !target;
  if(!ws || !target)return;
  const items=memberPlatformItems(ws,v),configured=items.filter(item=>memberHasPlatform(item,target,v));
  $("member-notification-platforms").replaceChildren(...configured.map(item=>{
    const view=notificationView(ws,item,target),hasNotifications=["gitlab","lark"].includes(item.provider);
    const row=notificationRow(ws,item,target,true);
    row.className+=" member-platform-row";
    row.children[0].setAttribute("aria-label",hasNotifications ? `编辑 ${item.name} 的通知设置` : `${item.name} 平台详情`);
    const heading=row.children[0].children[1].children[0],name=document.createElement("span"),edit=document.createElementNS("http://www.w3.org/2000/svg","svg"),path=document.createElementNS("http://www.w3.org/2000/svg","path");
    name.className="member-platform-name";name.textContent=item.name;
    edit.setAttribute("class","platform-edit-icon");edit.setAttribute("viewBox","0 0 24 24");edit.setAttribute("aria-hidden","true");edit.setAttribute("focusable","false");
    path.setAttribute("d","m15 5 4 4M4 20l4-1L20 7a2.8 2.8 0 0 0-4-4L4 15Z");edit.append(path);heading.replaceChildren(name,edit);
    row.children[0].children[1].children[1].textContent=[hasNotifications ? notificationConfigured(item,view) ? notificationDescription(view.config) : "尚未开启通知" : "无通知能力",item.validation?.username ? `@${item.validation.username}` : "账号待核验"].join(" · ");
    row.children[0].title=`${item.name} · ${row.children[0].children[1].children[1].textContent}`;
    const input=row.children[1],label=document.createElement("label");input.id=`member-platform-notify-${item.id}`;label.htmlFor=input.id;label.className="member-platform-notify-label";label.textContent=hasNotifications ? "通知" : "无通知能力";
    input.hidden=!hasNotifications;if(!notificationConfigured(item,view) && onboarding.busy)input.disabled=true;label.hidden=!hasNotifications;
    row.children[2].textContent=[notificationConfigured(item,view) ? notificationIssue(ws,item,view) : "",toolIssue(v,item)].filter(Boolean).join(" ");row.children[2].hidden=!row.children[2].textContent;row.children[2].dataset.kind=v?.cliResult?.tool===item.id && !v.cliResult.error ? "success" : "error";
    const info=row.children[0],issue=row.children[2],controls=document.createElement("div");controls.className="member-platform-controls";
    controls.append(label,input,toolSwitch(v,item));row.replaceChildren(info,controls,issue);return row;
  }));
  layoutMemberPlatforms();
  $("member-notification-source-count").textContent=String(configured.length);
  $("member-notification-empty").hidden=!!configured.length;
  $("member-notification-empty").textContent="添加平台，接收通知或使用工具。";
  const issues=configured.filter(item=>{
    const view=notificationView(ws,item,target);
    const availability=v?.cli?.selections?.find(s=>s.agent_id===target.agent_id && s.membership_id===target.membership_id && s.platform_id===item.id);
    return notificationConfigured(item,view) && notificationIssue(ws,item,view) || !!availability?.unavailable_reason || v?.cliResult?.tool===item.id && v.cliResult.error;
  });
  $("member-platform-issues").replaceChildren(...issues.map(item=>{
    const button=document.createElement("button");button.type="button";button.className="text-button";button.textContent=`${item.name}：查看异常`;button.addEventListener("click",()=>openMemberPlatform(button,item,target));return button;
  }));
  notice("member-notification-list-error",!ws.notificationsConnected || !v?.cliConnected ? "部分设置暂不可读，请重新读取。" : "");$("member-notification-list-reload").hidden=!!ws.notificationsConnected && !!v?.cliConnected;
  const q=$("member-notification-add-search").value.trim().toLowerCase();
  $("member-notification-add-options").replaceChildren(...items.filter(item=>!configured.includes(item) && [item.name,item.validation?.username,item.url].join(" ").toLowerCase().includes(q)).map(item=>{
    const button=document.createElement("button"),title=document.createElement("strong"),account=document.createElement("small");button.type="button";button.className="notification-add-option";button.dataset.platformId=item.id;button.dataset.provider=item.provider;
    title.textContent=item.name;account.textContent=item.validation?.username ? `@${item.validation.username}` : "账号待核验";button.append(title,account);button.disabled=!v?.cliConnected || !ws.notificationsConnected || !!ws.memberToolBusy;
    button.addEventListener("click",()=>selectMemberPlatformAddition(item));return button;
  }));
  renderMemberPlatformAddition();
  const subscriptions=configured.filter(item=>notificationConfigured(item,notificationView(ws,item,target)));
  $("inbox-empty-hint").hidden=!inboxView?.page || !!inboxView.page.entries.length;
  $("inbox-empty-hint").textContent=!ws.notificationsConnected ? "通知状态暂不可读，请查看平台设置。" : !subscriptions.length ? "添加平台后开始接收；查看此页不会启动自动处理。" : issues.length ? "尚未收到通知，请查看平台异常提示。" : subscriptions.some(item=>notificationView(ws,item,target).config.enabled) ? "平台已配置，尚未收到通知；可在设置查看采集状态。" : "通知已暂停，已收到的内容会保留。";
  $("inbox-add-platform").hidden=!!subscriptions.length;
  $("inbox-platform-issues").replaceChildren(...issues.map(item=>{const b=document.createElement("button");b.type="button";b.className="text-button";b.textContent=`${item.name}：查看设置`;b.addEventListener("click",()=>openMemberPlatform(b,item,target));return b;}));
}
async function openMemberPlatform(opener,item,target=memberNotificationTarget()) {
  return openNotificationSource(opener,item.provider==="lark" ? "messages" : item.provider==="gitlab" ? "gitlab" : "tools",item.id,target);
}
const gitlabEventStatuses = { disabled: "已关闭", initializing: "初始化中", running: "运行中", backoff: "退避中", credentials_invalid: "凭据失效", capacity: "接收容量已满", binding_changed: "绑定已变化" };
const gitlabEventErrors = { credential_unavailable: "凭据不可用，请验证所选平台。", credential_rejected: "GitLab 拒绝访问，请检查权限和凭据。", binding_changed: "绑定已变化，请核对后保存设置。", project_identity_changed: "项目身份已变化，需要核查绑定。", capacity: "接收容量已满，进度未推进。", query_failed: "查询失败，将按退避计划重试。", room_capacity: "房间消息或投递回执容量已满，事件保留在积压中。" };
const notificationTargetErrors = { target_unavailable: "投递目标暂不可用，通知保留在来源积压中。", target_removed: "接收成员已移除，旧积压保留原目标；同名新成员不会继承。", inbox_capacity: "成员收件箱容量已满，新通知保留在来源积压中。" };
function notificationTarget(config) { const t = config?.target; return t?.mode === "inbox" ? { mode: "inbox", agent_id: t.agent_id, membership_id: t.membership_id } : {}; }
function sameNotificationTarget(a, b) { return JSON.stringify(notificationTarget({target:a})) === JSON.stringify(notificationTarget({target:b})); }
function memberNotificationTarget(member = selectedAgent()) { return member ? {mode:"inbox",agent_id:member.agent_id,membership_id:member.membership_id} : null; }
function activeNotificationTarget(ws = workspaceView) { return ws?.notificationTarget || {}; }
function notificationScopeKey(target, id = "") { return JSON.stringify([state.catalog?.service_epoch,state.info?.service_epoch,notificationTarget({target}), id]); }
function notificationRequestTarget(target) { return target?.mode === "inbox" ? notificationTarget({target}) : {mode:"room"}; }
function notificationTargetQuery(target) { return target?.mode === "inbox" ? {target_mode:"inbox",agent_id:target.agent_id,membership_id:target.membership_id} : {target_mode:"room"}; }
function notificationWritePending(ws, key = ws?.notificationScope) { return !!ws?.notificationWrites?.has(key); }
function beginNotificationWrite(ws, key) { (ws.notificationWrites ||= new Set()).add(key); }
function notificationContextCurrent(ws, key, epoch) { return ws === workspaceView && ws.notificationScope === key && state.catalog?.service_epoch === epoch; }
function rememberNotificationDraft(ws) {
  if (!ws?.notificationScope) return;
  ws.notificationDrafts ||= new Map();
  const messages = ws.notificationChannel === "messages";
  if (messages && ws.messages && ws.larkDirty) ws.notificationDrafts.set(ws.notificationScope, {config:larkMessagesDraft(),directory:ws.larkChatDirectory});
  else if (!messages && ws.gitlabDirty) ws.notificationDrafts.set(ws.notificationScope, {config:structuredClone(ws.gitlabDraft),emptyTypes:ws.eventsEmptyTypes});
  else ws.notificationDrafts.delete(ws.notificationScope);
}
function gitlabConfig(config = {}) {
  const e = config.events || {}, t = todoConfig(config.todos || {});
  return { enabled: config.enabled ?? true, platform_id: config.platform_id || "", target: notificationTarget(config), interval_seconds: config.interval_seconds ?? 30,
    backfill_seconds: config.backfill_seconds || 0, ...(config.backfill_start ? {backfill_start: new Date(config.backfill_start).toISOString()} : {}),
    events: { enabled: !!e.enabled, project: e.project || "", etag_verified: !!e.etag_verified,
      filter: { types: [...(e.filter?.types || [])] } }, todos: t };
}
function gitlabDraft() { return workspaceView?.gitlabDraft || gitlabConfig(); }
function gitlabSettingsBusy(ws) { return !ws?.gitlabConnected || notificationWritePending(ws) || !!ws.gitlabSending || !!ws.notificationToggle || !!ws.gitlab?.applying || managementBusy(); }
function gitlabConfigKey(config) {
  const c = gitlabConfig(config);
  return JSON.stringify([c.enabled, c.platform_id, c.target, c.interval_seconds, c.backfill_seconds, config.backfill_start === "" ? "custom" : c.backfill_start || "", gitlabEventConfigKey(c.events), todoConfigKey(c.todos)]);
}
function acceptGitLabView(ws, view, saved = false) {
  if(!notificationConfigured({provider:"gitlab"},view))view={...view,config:{...view.config,enabled:false}};
  ws.gitlab = view; ws.gitlabConnected = true;
  ws.events = view.events; ws.todos = view.todos; ws.eventsConnected = ws.todosConnected = true;
  if (saved || !ws.gitlabDirty || !ws.eventsEmptyTypes && gitlabConfigKey(ws.gitlabDraft) === gitlabConfigKey(view.config)) { ws.gitlabDraft = gitlabConfig(view.config); ws.gitlabDirty = false; ws.eventsEmptyTypes = false; ws.gitlabBackfillEditing = false; }
  else if (view.version && ws.gitlabDraft.platform_id===view.config.platform_id) {
    ws.gitlabDraft.enabled=view.config.enabled;
    ws.gitlabDirty=!!ws.eventsEmptyTypes || gitlabConfigKey(ws.gitlabDraft)!==gitlabConfigKey(view.config);
  }
}
function editGitLabConfig() {
  const ws = workspaceView;
  ws.gitlabDirty = !!ws.eventsEmptyTypes || gitlabConfigKey(ws.gitlabDraft) !== gitlabConfigKey(ws.gitlab.config);
  ws.gitlabError = ""; ws.gitlabFeedback = "";
  renderGitLabEvents();
}
function gitlabPlatformOptions(selected, placeholder) {
  const platforms = workspaceView?.platforms || [];
  const options = [new Option(placeholder, ""), ...platforms.map(item => new Option(`${item.name} · ${item.validation?.username ? `@${item.validation.username}` : "待验证"}`, item.id))];
  if (selected && !platforms.some(item => item.id === selected)) options.push(new Option("原连接暂不可用", selected));
  return options;
}
const gitlabEventTypeIDs = ["opened", "merged", "closed", "note", "push"];
const gitlabEventTypeInputs = () => gitlabEventTypeIDs.map(id => $(`gitlab-events-type-${id}`));
function gitlabEventConfigKey(config) {
  const unique = values => [...new Set(values || [])].sort(), types = unique(config.filter?.types);
  return JSON.stringify({ project: config.project || "", enabled: config.enabled, etag_verified: !!config.etag_verified,
    types: types.length === gitlabEventTypeIDs.length ? [] : types });
}
function gitlabEventDraftState(data) {
  const root = gitlabDraft(), config = root.events, connection = workspaceView?.platforms?.find(c => c.id === root.platform_id);
  const identityChanged = !!(config.enabled && data.subscription_id && data.recipient_id > 0 && todoVerified(connection) && (data.recipient_id !== connection.validation.user_id || data.instance !== connection.url));
  const targetChanged = root.platform_id !== workspaceView?.gitlab?.config.platform_id || config.project !== data.config.project || identityChanged;
  const targetInvalid = config.enabled && (!todoVerified(connection) || !config.project);
  const count = workspaceView?.eventsEmptyTypes ? 0 : config.filter.types.length || gitlabEventTypeIDs.length;
  return { config, count, dirty: !!workspaceView?.eventsEmptyTypes || identityChanged || gitlabEventConfigKey(config) !== gitlabEventConfigKey(workspaceView?.gitlab?.config.events || config), targetChanged, targetInvalid,
    invalid: targetInvalid || config.enabled && count === 0 };
}
async function syncGitLabNotifications(catalog, ws) {
  if (ws !== workspaceView) return;
  const revisions = {messages:ws.messagesRevision,gitlab:ws.gitlabRevision};
  const read = ws.notificationsRead = (ws.notificationsRead || 0) + 1;
  const scope = ws.notificationScope, context = ws.notificationEpoch;
  try {
    const params = new URLSearchParams({service_epoch:catalog.service_epoch,caller_id:ws.operator,...ws.room});
    const data = await request(`/v1/platform/notifications?${params}`);
    if (ws !== workspaceView || state.catalog?.service_epoch !== catalog.service_epoch || read !== ws.notificationsRead || context !== ws.notificationEpoch) return;
    ws.notificationsConnected = true;
    ws.allSubscriptions = data.subscriptions || [];
    ws.retiredSources = data.retired_subscriptions || [];
    ws.larkSources = ws.allSubscriptions.filter(v=>v.provider === "lark" && v.channel === "messages");
    ws.gitlabPlatforms = data.gitlab_platforms || [];
    if (scope !== ws.notificationScope) return;
    const target = activeNotificationTarget(ws), id = ws.notificationPlatformID;
    if (!ws.notificationToggle && !ws.messagesSending && revisions.messages === ws.messagesRevision) {
      const next=ws.larkSources.find(v=>v.config.platform_id===id && sameNotificationTarget(v.config.target,target));
      if (next) { ws.messages=next; ws.messagesConnected=true; if(ws.larkDirty && next.subscription_id)$("lark-enabled").checked=!!next.config.enabled; }
    }
    if (!ws.notificationToggle && !ws.gitlabSending && revisions.gitlab === ws.gitlabRevision) {
      const next=ws.gitlabPlatforms.find(v=>v.platform_id===id && sameNotificationTarget(v.config.target,target));
      if(next)acceptGitLabView(ws,next);
    }
  } catch {
    if (ws !== workspaceView || state.catalog?.service_epoch !== catalog.service_epoch || read !== ws.notificationsRead || context !== ws.notificationEpoch) return;
    ws.notificationsConnected=false;
    if(scope===ws.notificationScope){ws.gitlabConnected=false;ws.messagesConnected=false;}
  }
}
function larkConfigKey(c = {}) {return JSON.stringify({enabled:!!c.enabled,platform_id:c.platform_id || "",target:notificationTarget(c),private:{enabled:!!c.private?.enabled,scope:c.private?.scope || "all",chat_ids:[...(c.private?.chat_ids || [])].sort()},groups:{enabled:!!c.groups?.enabled,chat_ids:[...(c.groups?.chat_ids || [])].sort(),message_mode:c.groups?.message_mode || "mentions",at_me:!!c.groups?.at_me,at_all:!!c.groups?.at_all},interval_seconds:c.interval_seconds || 60,backfill_seconds:c.backfill_seconds || 0,backfill_start:c.backfill_start ? new Date(c.backfill_start).toISOString() : ""});}
function larkMessagesDraft() {
  const ws=workspaceView,ids=larkChatIDs().sort(),groups=[],privateIDs=[];
  for(const id of ids){
    const chat=ws?.larkChatDirectory?.known.get(id) || ws?.messages?.lark?.chats?.find(c=>c.chat_id===id);
    const group=ws?.messages?.config.groups?.chat_ids?.includes(id) || ["group","topic"].includes(chat?.chat_mode || chat?.mode);
    (group ? groups : privateIDs).push(id);
  }
  return {enabled:$("lark-enabled").checked,platform_id:$("lark-connection").value,target:structuredClone(activeNotificationTarget(ws)),
    private:{enabled:$("lark-private-enabled").checked,scope:larkAllChats() ? "all" : "specified",chat_ids:privateIDs},
    groups:{enabled:$("lark-groups-enabled").checked,chat_ids:groups,message_mode:$("lark-group-mode").value || "mentions",at_me:$("lark-group").checked,at_all:$("lark-all").checked},
    interval_seconds:Number($("lark-interval").value),...backfillDraft("lark")};
}
function selectLarkPlatform(ws,id) {
  stopLarkChatSearch();
  ws.messages=structuredClone(ws.larkSources?.find(s=>s.config.platform_id===id && sameNotificationTarget(s.config.target,activeNotificationTarget(ws))) || {provider:"lark",channel:"messages",available:true,subscription_id:"",subscription_version:0,status:"disabled",config:{enabled:false,platform_id:id,target:structuredClone(activeNotificationTarget(ws)),private:{enabled:true,scope:"all",chat_ids:[]},groups:{enabled:false,chat_ids:[],message_mode:"mentions",at_me:true,at_all:false},interval_seconds:60,backfill_seconds:0},lark:{chats:[]}});
  ws.larkDirty=false;ws.larkFilled=null;ws.larkSelectedChats=[];ws.messagesFeedback="";ws.messagesError="";ws.messagesRevision=(ws.messagesRevision || 0)+1;
}
function larkAllChats() { return $("lark-scope").checked; }
function notificationLocalTime(value) { return systemTime.local(value); }
function backfillDraft(prefix) {
  const mode=$(`${prefix}-backfill`).value;
  let start=""; try { start=readTimeInput(`${prefix}-backfill-start`); } catch { /* renderBackfill reports the conversion error. */ }
  return {backfill_seconds:mode==="custom" ? 0 : Number(mode || 0),
    ...(mode==="custom" ? {backfill_start:start} : {})};
}
function fillBackfill(prefix, config) {
  $(`${prefix}-backfill`).value=config.backfill_start ? "custom" : String(config.backfill_seconds || 0);
  fillTimeInput(`${prefix}-backfill-start`,config.backfill_start);
}
function renderBackfill(prefix, locked, baseline, busy=false) {
  const mode=$(`${prefix}-backfill`).value, start=Date.parse(backfillDraft(prefix).backfill_start || "");
  $(`${prefix}-backfill`).disabled=locked || busy;$(`${prefix}-backfill-start`).disabled=locked || busy;
  $(`${prefix}-backfill`).hidden=locked;$(`${prefix}-backfill-locked`).hidden=!locked;
  $(`${prefix}-backfill-custom`).hidden=locked || mode!=="custom";
  $(`${prefix}-backfill-start`).max=notificationLocalTime(Date.now());
  const time=Date.parse(baseline || "");
  $(`${prefix}-backfill-value`).textContent=Number.isFinite(time) ? formatDate(time) : "首次接收时已确定";
  let conversionError=""; try { readTimeInput(`${prefix}-backfill-start`); } catch(error) { conversionError=error.message; }
  const error=systemTime.error || (mode==="custom" && conversionError) || (mode==="custom" && !Number.isFinite(start) ? "请选择要开始接收消息的日期和时间。" : mode==="custom" && start<=0 ? "请选择 1970 年之后的有效时间。" : mode==="custom" && start>Date.now() ? "起始时间不能晚于当前时间。" : "");
  $(`${prefix}-backfill-start-label`).textContent=`起始时间（${systemTime.zone || "系统时区异常"}）`;
  $(`${prefix}-backfill-help`).textContent=locked ? "更早消息需新建房间。" : "首次开启后固定接收起点。";
  notice(`${prefix}-backfill-error`,error);$(`${prefix}-backfill-start`).setAttribute("aria-invalid",String(!!error));
  return error;
}
function notificationSyncSummary(data) {
  const date=value=>formatDate(value);
  const parts=[data.last_check ? `最近检查 ${date(data.last_check)}` : data.subscription_id ? (data.config.enabled ? "等待首次检查" : "已暂停") : "尚未开始同步"];
  if(data.pending>0)parts.push(`待投递 ${data.pending}`);
  if(data.retry_at)parts.push(`下次重试 ${date(data.retry_at)}`);
  if(data.scan)parts.push(`历史检查中（第 ${data.scan.next_page} 页）`);
  if(data.gap)parts.push("部分历史消息可能缺失");
  return parts.join(" · ");
}
function larkChatIDs() { return [...new Set(workspaceView?.larkSelectedChats || [])]; }
function larkChatType(chat) { return chat?.chat_mode==="p2p" || chat?.mode==="p2p" ? "私聊" : "群聊"; }
function larkChatLabel(chat, id) { return chat?.name || `未命名${chat ? larkChatType(chat) : "会话"} · ${id}`; }
function larkChatDirectory(ws, connection) {
  const key=JSON.stringify([state.catalog?.service_epoch,ws.notificationScope,connection?.id,connection?.version,connection?.validation?.account]);
  if(ws.larkChatDirectory?.key!==key){
    clearTimeout(ws.larkChatDirectory?.timer);ws.larkChatDirectory?.controller?.abort();
    ws.larkChatDirectory={key,query:"",type:"all",known:new Map(),chats:new Map(),loaded:false,loading:false,hasMore:false,token:"",error:"",pages:0,warnings:[]};
    $("lark-chat-search").value="";$("lark-chat-type").value="all";
  }
  return ws.larkChatDirectory;
}
function selectLarkChat(id, selected) {
  const ids=larkChatIDs();
  if(selected && !ids.includes(id)){
    if(ids.length>=32){notice("lark-chat-selection-error","最多选择 32 个会话，请先移除已选会话。");return;}
    ids.push(id);
  }
  if(workspaceView)workspaceView.larkSelectedChats=selected ? ids : ids.filter(x=>x!==id);
  notice("lark-chat-selection-error","");
  if(workspaceView)workspaceView.larkDirty=true;
  renderLarkMessages();
}
function renderLarkChatPicker(ws, connection) {
  const dir=larkChatDirectory(ws,connection), ids=larkChatIDs();
  const ready=connection?.validation?.status==="valid",busy=$("lark-fields").disabled,blocked=!ready || busy;
  const needsPlatform=!$("lark-connection").value;
  $("lark-scope").disabled=!ready || busy;
  $("lark-chat-selection").hidden=false;
  $("lark-chat-picker-summary").setAttribute("aria-disabled",String(blocked));
  $("lark-chat-picker-hint").textContent=needsPlatform ? "请先在上方选择平台，再选择会话。" : !connection ? "所选平台不可用，请在上方重新选择。" : !ready ? "请先在「管理平台」中验证所选连接，再选择会话。" : "";
  $("lark-chat-picker-hint").hidden=ready;
  $("lark-chat-search").disabled=blocked;$("lark-chat-type").disabled=blocked;
  if(blocked)$("lark-chat-picker").open=false;
  const known=new Map((ws.messages?.config.platform_id===connection?.id ? ws.messages?.lark?.chats || [] : []).map(chat=>[chat.chat_id,chat]));
  for(const [id,chat] of dir.known)known.set(id,chat);
  $("lark-group-settings").hidden=!$("lark-groups-enabled").checked;
  $("lark-chat-selected").replaceChildren(...ids.map(id=>{
    const chat=known.get(id),label=larkChatLabel(chat,id),button=document.createElement("button");
    button.type="button";button.textContent=`${label} ×`;button.title=id;button.setAttribute("aria-label",`移除会话：${label}`);
    button.addEventListener("click",()=>selectLarkChat(id,false));return button;
  }));
  $("lark-chat-picker-summary").textContent=!ready ? needsPlatform ? "请先选择平台" : "请先验证平台" : ids.length ? `已选 ${ids.length} 个会话 · 编辑` : "选择私聊或群聊";
  const query=dir.query,type=dir.type;
  const chats=[...dir.chats.values()].filter(chat=>type==="all" || (type==="p2p" ? chat.chat_mode==="p2p" : chat.chat_mode!=="p2p"));
  if(!query)chats.sort((a,b)=>(a.name || a.chat_id).localeCompare(b.name || b.chat_id,"zh-CN"));
  $("lark-chat-options").replaceChildren(...chats.map(chat=>{
    const label=document.createElement("label"),input=document.createElement("input"),text=document.createElement("span"),name=document.createElement("span"),detail=document.createElement("small");
    label.className="lark-chat-option";input.type="checkbox";input.value=chat.chat_id;input.checked=ids.includes(chat.chat_id);input.disabled=blocked || !input.checked && ids.length>=32;
    name.textContent=larkChatLabel(chat,chat.chat_id);detail.textContent=larkChatType(chat);label.title=chat.chat_id;
    text.append(name,detail);label.append(input,text);input.addEventListener("change",()=>{
      if(ws!==workspaceView || ws.larkChatDirectory!==dir || $("lark-fields").disabled || input.disabled)return;
      selectLarkChat(chat.chat_id,input.checked);
    });return label;
  }));
  const warningText=dir.warnings.map(w=>{
    const name={group:"群聊",user:"人员私聊",bot:"机器人私聊"}[w.source] || "部分会话";
    if(w.code==="refine_query")return `${name}结果较多，请输入更完整的名称`;
    if(w.code==="permission_denied")return `${name}搜索权限不足，请补充授权；也可清空关键词浏览会话列表`;
    return `${name}搜索暂不可用，可重试`;
  }).join("；");
  const summary=dir.loading ? (query ? "正在搜索…" : `正在加载…已有 ${dir.chats.size} 个会话`) : !dir.loaded ? (query ? "等待搜索…" : "可选择私聊和已加入群聊，输入名称可远程搜索。") : query ? `已找到 ${dir.chats.size} 个会话${dir.hasMore ? "，可加载更多群聊" : ""}${!chats.length ? (dir.warnings.length ? " · 当前未返回匹配会话" : " · 无匹配会话") : ""}` : `已加载 ${dir.chats.size} 个会话${dir.hasMore ? "，可加载更多或输入名称搜索" : ""}${!chats.length ? " · 暂无会话" : ""}`;
  $("lark-chat-picker-status").textContent=!ready ? "请先选择并验证飞书连接。" : dir.error || `${summary}${warningText ? `；${warningText}` : ""}`;
  $("lark-chat-more").hidden=!dir.hasMore;$("lark-chat-more").disabled=blocked || dir.loading || dir.chats.size>=10000 || dir.pages>=100;
  $("lark-chat-more").textContent=query ? "加载更多群聊" : "加载更多";
  $("lark-chat-refresh").textContent=query ? "重新搜索" : "刷新会话列表";
  $("lark-chat-refresh").disabled=blocked || dir.loading || !!dir.timer;
}
async function loadLarkChats(reset=false) {
  const ws=workspaceView,epoch=state.catalog?.service_epoch,connection=ws?.larkPlatforms?.find(x=>x.id===$("lark-connection").value);
  if(!ws || !epoch || connection?.validation?.status!=="valid" || $("lark-fields").disabled || !$("notification-detail").open || !$("lark-chat-picker").open)return;
  let dir=larkChatDirectory(ws,connection);
  if(dir.loading || !reset && dir.loaded && !dir.hasMore)return;
  if(reset)dir=resetLarkChatSearch(ws,dir,dir.query,dir.type);
  if([...dir.query].length>50){dir.error="搜索名称最多 50 个字符。";renderLarkMessages();return;}
  clearTimeout(dir.timer);dir.timer=null;dir.controller=new AbortController();
  dir.loading=true;dir.error="";renderLarkMessages();
  try{
    const page=await request("/v1/platform/lark/chats",{service_epoch:epoch,...ws.room,caller:{kind:"human",id:ws.operator},platform_id:connection.id,platform_version:connection.version,page_token:dir.token,query:dir.query,type:dir.type},undefined,40000,dir.controller.signal);
    if(ws!==workspaceView || epoch!==state.catalog?.service_epoch || ws.larkChatDirectory!==dir || !$("notification-detail").open || !$("lark-chat-picker").open)return;
    if(!Array.isArray(page.chats) || typeof page.has_more!=="boolean" || page.has_more && (!page.page_token || page.page_token===dir.token))throw Error("invalid page");
    for(const chat of page.chats){dir.chats.set(chat.chat_id,chat);dir.known.set(chat.chat_id,chat);}
    for(const warning of page.search_warnings || [])if(!dir.warnings.some(w=>w.source===warning.source && w.code===warning.code))dir.warnings.push(warning);
    dir.loaded=true;dir.hasMore=page.has_more;dir.token=page.page_token || "";dir.pages++;
    if(dir.hasMore && (dir.chats.size>=10000 || dir.pages>=100))dir.error="已达会话列表加载上限，请输入更完整的名称搜索。";
  }catch(e){
    if(ws===workspaceView && ws.larkChatDirectory===dir && !dir.controller.signal.aborted){
      const errors={conflict:"连接已变化，请重新读取通知设置并核对连接。",identity_mismatch:"飞书登录账号已变化，请重新验证连接后刷新会话列表。",permission_denied:"读取会话的权限不足，请检查授权并重新验证连接。",credential:"飞书连接未验证或登录已失效，请重新验证连接。",cursor_expired:"会话列表已变化，请刷新会话列表后继续选择。"};
      dir.error=errors[e.code] || "会话列表读取失败，已选会话保留；可刷新列表重试。";
    }
  }finally{
    dir.loading=false;
    if(ws===workspaceView && ws.larkChatDirectory===dir)renderLarkMessages();
  }
}
function resetLarkChatSearch(ws,dir,query,type) {
  clearTimeout(dir.timer);dir.controller?.abort();
  // Retain only selected labels across queries; result caches stay bounded.
  const known=new Map([...dir.known].filter(([id])=>larkChatIDs().includes(id)));
  return ws.larkChatDirectory={key:dir.key,query,type,known,chats:new Map(),loaded:false,loading:false,hasMore:false,token:"",error:"",pages:0,warnings:[]};
}
function queueLarkChatSearch(composing=false,delay=300) {
  const ws=workspaceView;if(!ws || $("lark-chat-search").disabled)return;
  const connection=ws.larkPlatforms?.find(x=>x.id===$("lark-connection").value);
  let dir=larkChatDirectory(ws,connection);
  const query=$("lark-chat-search").value.trim(),type=$("lark-chat-type").value || "all";
  if(!query && !dir.query)dir.type=type;
  else if(query!==dir.query || type!==dir.type)dir=resetLarkChatSearch(ws,dir,query,type);
  clearTimeout(dir.timer);dir.timer=null;
  if([...query].length>50)dir.error="搜索名称最多 50 个字符。";
  else if(!composing && !dir.loaded && !dir.loading){
    dir.timer=setTimeout(()=>{dir.timer=null;if(ws===workspaceView && ws.larkChatDirectory===dir)void loadLarkChats();},query ? delay : 0);
  }
  renderLarkMessages();
}
function stopLarkChatSearch() {
  const ws=workspaceView,dir=ws?.larkChatDirectory;if(!dir)return;
  ws.larkChatComposing=false;
  clearTimeout(dir.timer);dir.controller?.abort();
  ws.larkChatDirectory={...dir,loading:false,timer:null,controller:null};
}
function renderLarkMessages() {
  const ws=workspaceView,data=ws?.messages;
  $("lark-fields").disabled=!data || notificationWritePending(ws) || !!ws?.messagesSending || !!ws?.notificationToggle || managementBusy();
  $("lark-save").disabled=true;$("lark-sync").disabled=true;
  if(!data) return;
  const fresh=!!ws.larkRestore || ws.larkFilled !== data.config.platform_id+":"+data.subscription_id+":"+data.subscription_version && !ws.larkDirty;
  const c=ws.larkRestore || (fresh ? data.config : larkMessagesDraft());
  const platforms=ws.larkPlatforms || [];
  const options=[new Option("请选择连接", ""), ...platforms.map(x=>new Option(`${x.name} · ${x.validation?.username || "待验证"}`,x.id))];
  if(c.platform_id && !platforms.some(x=>x.id===c.platform_id))options.push(new Option("原连接暂不可用",c.platform_id));
  setSelectOptions("lark-connection", options, c.platform_id || "");$("lark-connection").hidden=true;
  const target=JSON.stringify(activeNotificationTarget(ws));
  setSelectOptions("lark-target",[new Option("固定接收者",target)],target);$("lark-target").disabled=true;
  if(fresh){
    $("lark-scope").checked=c.private?.scope==="all";$("lark-enabled").checked=!!c.enabled;
    $("lark-private-enabled").checked=!!c.private?.enabled;$("lark-groups-enabled").checked=!!c.groups?.enabled;
    ws.larkSelectedChats=[...(c.private?.chat_ids || []),...(c.groups?.chat_ids || [])];
    $("lark-group").checked=!!c.groups?.at_me;$("lark-group-mode").value=c.groups?.message_mode || "mentions";$("lark-all").checked=!!c.groups?.at_all;
    $("lark-interval").value=c.interval_seconds || 60;fillBackfill("lark",c);
    ws.larkFilled=data.config.platform_id+":"+data.subscription_id+":"+data.subscription_version;
    ws.larkRestore=null;
  }
  ws.larkSelectionPlatform=c.platform_id;
  $("lark-group-mode-all").disabled=false;
  const groupAll=$("lark-group-mode").value==="all";
  $("lark-group").disabled=groupAll;$("lark-all").disabled=groupAll;$("lark-group-mentions").hidden=groupAll;
  $("lark-scope-option").hidden=!$("lark-private-enabled").checked;
  $("lark-scope-help").hidden=!$("lark-private-enabled").checked || !larkAllChats();
  const draft=larkMessagesDraft(), dirty=larkConfigKey(draft)!==larkConfigKey(data.config) || !data.subscription_id && !!draft.platform_id, connection=platforms.find(x=>x.id===draft.platform_id);
  const scopeError=draft.enabled && !draft.private.enabled && !draft.groups.enabled ? "请选择至少一种接收内容。" : draft.private.enabled && draft.private.scope==="specified" && !draft.private.chat_ids.length ? "请至少选择一个人员私聊，或关闭人员私聊接收。" : draft.groups.enabled && !draft.groups.chat_ids.length ? "请至少选择一个群聊，或关闭群聊接收。" : "";
  const backfillError=renderBackfill("lark",!!data.subscription_id && (!!data.lark?.baseline || data.status!=="disabled"),data.lark?.baseline || data.config.backfill_start);
  const valid=!backfillError && (!draft.enabled || connection?.validation?.status==="valid") && draft.target.mode==="inbox" && (!draft.enabled || draft.private.enabled || draft.groups.enabled) && (!draft.private.enabled || draft.private.scope==="all" || draft.private.chat_ids.length>0) && (!draft.groups.enabled || draft.groups.chat_ids.length>0) && draft.interval_seconds>=3 && draft.interval_seconds<=3600;
  $("lark-save").disabled=notificationWritePending(ws)||!ws.messagesConnected||!!ws.messagesSending||managementBusy()||!valid||(!dirty && !["credentials_invalid","binding_changed","capacity"].includes(data.status));
  $("lark-sync").disabled=notificationWritePending(ws)||!ws.messagesConnected||!!ws.messagesSending||managementBusy()||dirty||!data.config.enabled;
  $("lark-save").textContent=ws.messagesSending==="configure" ? "保存中…" : ws.notificationAddition?.key===ws.notificationScope ? "确认并返回添加" : "保存设置";
  $("lark-sync").textContent=ws.messagesSending==="sync" ? "请求中…" : "立即同步";
  $("lark-state").textContent=!ws.messagesConnected ? "状态不可读" : dirty ? "待保存" : !data.config.enabled ? "通知已暂停" : data.error_code ? "需处理" : data.status==="disabled" ? "采集未启动" : "";
  notice("lark-account",connection?.validation?.status==="valid" ? `接收账号：${connection.validation.username}` : draft.platform_id ? "请先核验此平台账号。" : "请从上方选择平台详情。");
  renderLarkChatPicker(ws,connection);
  $("lark-feedback").textContent=ws.messagesSending ? "正在处理…" : ws.messagesFeedback || (dirty ? "有未保存的更改" : "");
  const errors={...connectionValidationErrors,...notificationTargetErrors,capacity:"来源容量已满，进度保留；释放容量后显式同步。",binding_changed:"连接身份已变化；旧订阅保留，请核对原账号。",identity_mismatch:"账号已变化，进度保留；请核对原账号并重新验证连接。",credential_unavailable:"登录凭据不可用，进度保留；请验证连接，原账号下已开启的通知会自动恢复。",credential_rejected:"登录已失效，进度保留；请在原 profile 完成用户授权并验证连接，原账号下已开启的通知会自动恢复。",permission_denied:"读取权限不足，进度保留；请检查用户授权范围并验证连接，原账号下已开启的通知会自动恢复。",cursor_expired:"游标已过期，将在同一时间窗重扫。",query_failed:"读取失败，将退避重试。"};
  notice("lark-error",ws.messagesError || scopeError || (!ws.messagesConnected ? "通知状态暂不可读。" : errors[data.delivery_error_code] || errors[data.error_code] || data.error_code || ""));
  const d=data.lark || {}, date=x=>formatDate(x);
  const diagnostics=[notificationSyncSummary(data)];
  if(d.excluded_items>0)diagnostics.push(`本人自聊历史已排除 ${d.excluded_items}`);
  if(data.subscription_id && data.config.private?.enabled && data.config.private?.scope==="all" && !d.directory_complete)diagnostics.push(d.directory_scanning ? `会话目录加载中（${d.directory_pages || 0} 页）` : "会话目录尚未加载完整");
  for(const [count,label] of [[d.failed_chats,"读取受阻"],[d.unlisted_chats,"不在最新目录"],[d.gap_chats,"积压超过 24 小时"],[d.unchecked,"待检查会话"]])if(count>0)diagnostics.push(`${label} ${count}`);
  $("lark-diagnostics").textContent=diagnostics.join(" · ");
  const chats=(d.chats || []).filter(x=>x.relationship==="self" || x.relationship==="pending" || x.error_code || x.scan || !x.completed);
  $("lark-chat-status").hidden=!chats.length;
  $("lark-chat-status").replaceChildren(...chats.map(x=>{const li=document.createElement("li");li.textContent=`${x.name || x.chat_id} · ${x.relationship==="self" ? "本人自聊，已排除自动接收与处理" : x.relationship==="pending" ? `会话关系待核验${x.error_code ? `（${x.error_code}）` : ""}` : x.error_code ? `读取受阻 ${x.error_code}${x.retry_at ? `，重试 ${date(x.retry_at)}` : ""}` : x.scan ? `检查中（${x.scan.pages} 页）` : "待检查"}`;return li;}));
}
async function changeLarkMessages(action) {
  const ws=workspaceView;if(!ws?.messages || ws.messagesSending || $(action==="configure"?"lark-save":"lark-sync").disabled)return;
  const payload={service_epoch:state.catalog.service_epoch,caller:{kind:"human",id:ws.operator},...ws.room,provider:"lark",channel:"messages",subscription_id:ws.messages.subscription_id,subscription_version:ws.messages.subscription_version};
  const scope=ws.notificationScope;
  if(action==="configure")payload.config=larkMessagesDraft();
  else payload.target=notificationRequestTarget(activeNotificationTarget(ws));
  if(action==="configure" && captureMemberPlatformDraft(payload.config))return;
  beginNotificationWrite(ws,scope);
  ws.messagesRevision=(ws.messagesRevision||0)+1;ws.messagesSending=action;ws.messagesError="";
  const revision=ws.messagesRevision;
  const current=()=>notificationContextCurrent(ws,scope,payload.service_epoch) && ws.messagesRevision===revision;
  renderLarkMessages();
  try{
    const data=await request(`/v1/platform/notifications/${action}`,payload);
    if(!current())return;if(data.config.platform_id!==ws.notificationPlatformID || !sameNotificationTarget(data.config.target,activeNotificationTarget(ws)))throw Error("Notification scope mismatch");const replaced=ws.messages.subscription_id && ws.messages.subscription_id!==data.subscription_id;ws.messages=data;ws.larkSources=[...(ws.larkSources || []).filter(s=>s.config.platform_id!==data.config.platform_id || !sameNotificationTarget(s.config.target,data.config.target)),data];ws.messagesConnected=true;ws.larkDirty=false;ws.larkFilled=null;ws.messagesFeedback=action==="sync" ? "已发起同步，结果将自动更新。" : replaced ? "已为新连接开启订阅，旧订阅已暂停。" : "设置已保存。";
  }catch(e){if(current())ws.messagesError=e.code==="conflict"?"设置或账号已变化，请重新读取后核对。":"结果未确认，请重新读取状态；不会自动重发。";}
  finally{ws.notificationWrites?.delete(scope);if(current()){ws.messagesRevision++;ws.messagesSending=false;renderGitLabEvents();}else if(ws===workspaceView)renderGitLabEvents();}
}
for(const id of ["enabled","connection","target","private-enabled","groups-enabled","scope","group","group-mode","all","interval","backfill","backfill-start"]) $("lark-"+id).addEventListener("change",()=>{
  if(id==="connection" && workspaceView)selectLarkPlatform(workspaceView,$("lark-connection").value);
  else if(workspaceView)workspaceView.larkDirty=true;renderLarkMessages();
  if((id==="connection" || id==="scope") && $("lark-chat-picker").open)void loadLarkChats();
});
$("lark-chat-picker-summary").addEventListener("click",event=>{if($("lark-chat-search").disabled)event.preventDefault();});
$("lark-chat-picker").addEventListener("toggle",()=>{
  if($("lark-chat-search").disabled)$("lark-chat-picker").open=false;
  if(!$("lark-chat-picker").open)stopLarkChatSearch();
  else if(!workspaceView?.larkChatDirectory?.loaded)void loadLarkChats();
});
$("lark-chat-search").addEventListener("compositionstart",()=>{if(workspaceView)workspaceView.larkChatComposing=true;queueLarkChatSearch(true);});
$("lark-chat-search").addEventListener("compositionend",()=>{if(workspaceView)workspaceView.larkChatComposing=false;queueLarkChatSearch();});
$("lark-chat-search").addEventListener("input",event=>queueLarkChatSearch(event.isComposing || workspaceView?.larkChatComposing));
$("lark-backfill-start").addEventListener("input",()=>{if(workspaceView)workspaceView.larkDirty=true;renderLarkMessages();});
$("lark-chat-type").addEventListener("change",()=>queueLarkChatSearch(false,0));
$("lark-chat-more").addEventListener("click",()=>loadLarkChats());
$("lark-chat-refresh").addEventListener("click",()=>loadLarkChats(true));
$("lark-save").addEventListener("click",()=>changeLarkMessages("configure"));
$("lark-sync").addEventListener("click",()=>changeLarkMessages("sync"));
$("lark-manage-platforms").addEventListener("click",async()=>{await openSettings("platforms");});
function gitlabProjectDirectory(ws) {
  const connection=todoPlatform(ws);
  const key=JSON.stringify([state.catalog?.service_epoch,ws.notificationScope,connection?.id,connection?.version,connection?.validation]);
  if(ws.gitlabProjectDirectory?.key!==key){
    clearTimeout(ws.gitlabProjectDirectory?.timer);
    ws.gitlabProjectDirectory={key,search:"",projects:new Map(),loaded:false,loading:false,nextPage:0,error:""};
    $("gitlab-project-search").value="";
  }
  return ws.gitlabProjectDirectory;
}
function renderGitLabProjectPicker(ws) {
  const dir=gitlabProjectDirectory(ws),connection=todoPlatform(ws),ready=todoVerified(connection),busy=gitlabSettingsBusy(ws);
  const selected=ws.gitlabDraft.events.project;
  const needsPlatform=!ws.gitlabDraft.platform_id;
  $("gitlab-project-summary").textContent=ready ? selected || "选择项目" : needsPlatform ? "请先选择平台" : "请先验证平台";
  $("gitlab-project-hint").textContent=needsPlatform ? "请先在上方选择平台，再选择项目。" : !connection ? "所选平台不可用，请在上方重新选择。" : !ready ? "请先在「管理平台」中验证所选连接，再选择项目。" : "";
  $("gitlab-project-hint").hidden=ready;
  if(!ready)$("gitlab-project-picker").open=false;
  $("gitlab-project-summary").setAttribute("aria-disabled",String(!ready || busy));
  $("gitlab-project-search").disabled=!ready || busy;
  $("gitlab-project-options").replaceChildren(...[...dir.projects.values()].map(project=>{
    const label=document.createElement("label"),input=document.createElement("input"),text=document.createElement("span"),name=document.createElement("span"),path=document.createElement("small");
    label.className="gitlab-project-option";input.type="radio";input.name="gitlab-project";input.value=project.path_with_namespace;input.checked=selected===input.value;input.disabled=!ready || busy;
    name.textContent=project.name;path.textContent=project.path_with_namespace;text.append(name,path);label.append(input,text);
    input.addEventListener("change",()=>{
      if(ws!==workspaceView || gitlabProjectDirectory(ws)!==dir || gitlabSettingsBusy(ws))return;
      ws.gitlabDraft.events.project=project.path_with_namespace;
      $("gitlab-project-picker").open=false;editGitLabConfig();$("gitlab-project-summary").focus();
    });return label;
  }));
  $("gitlab-project-status").textContent=!ready ? "请先选择并验证 GitLab 连接。" : dir.error || (dir.loading || dir.timer ? "正在搜索项目…" : !dir.loaded ? "展开后加载可访问的项目。" : !dir.projects.size ? "没有匹配的项目，请更换关键词。" : `已加载 ${dir.projects.size} 个项目${dir.nextPage ? " · 可加载更多" : ""}`);
  $("gitlab-project-more").hidden=!dir.nextPage;
  $("gitlab-project-more").disabled=!ready || busy || dir.loading || !!dir.timer;
  $("gitlab-project-refresh").disabled=!ready || busy || dir.loading || !!dir.timer;
}
async function loadGitLabProjects(reset=false) {
  const ws=workspaceView,epoch=state.catalog?.service_epoch,connection=ws && todoPlatform(ws);
  if(!ws?.gitlabDraft || !epoch || !todoVerified(connection) || gitlabSettingsBusy(ws) || !$("gitlab-project-picker").open)return;
  let dir=gitlabProjectDirectory(ws);
  if(reset){
    clearTimeout(dir.timer);
    dir=ws.gitlabProjectDirectory={...dir,projects:new Map(),loaded:false,loading:false,nextPage:0,error:"",timer:null};
  }
  if(dir.loading || dir.timer || dir.loaded && !dir.nextPage)return;
  const number=dir.loaded ? dir.nextPage : 1;
  dir.loading=true;dir.error="";renderGitLabProjectPicker(ws);
  try{
    const page=await request("/v1/platform/gitlab/projects",{service_epoch:epoch,...ws.room,caller:{kind:"human",id:ws.operator},platform_id:connection.id,platform_version:connection.version,search:dir.search,page:number},undefined,45000);
    if(ws!==workspaceView || epoch!==state.catalog?.service_epoch || gitlabProjectDirectory(ws)!==dir)return;
    if(!Array.isArray(page.projects) || !Number.isInteger(page.next_page) || page.next_page!==0 && page.next_page<=number || page.projects.some(p=>!Number.isSafeInteger(p.id) || p.id<=0 || typeof p.name!=="string" || typeof p.path_with_namespace!=="string"))throw Error("invalid page");
    for(const project of page.projects)dir.projects.set(project.id,project);
    dir.loaded=true;dir.nextPage=page.next_page;
  }catch(e){
    if(ws===workspaceView && gitlabProjectDirectory(ws)===dir){
      dir.error=e.code==="conflict" ? "连接已变化，请重新读取通知设置后重试。" : e.code==="credential" ? "连接已失效，请重新验证后重试。" : "项目列表读取失败，可刷新列表重试；已选项目保留。";
    }
  }finally{
    dir.loading=false;
    if(ws===workspaceView && gitlabProjectDirectory(ws)===dir)renderGitLabProjectPicker(ws);
  }
}
$("gitlab-project-summary").addEventListener("click",event=>{if(!todoVerified(workspaceView && todoPlatform(workspaceView)) || gitlabSettingsBusy(workspaceView))event.preventDefault();});
$("gitlab-project-picker").addEventListener("toggle",()=>{if($("gitlab-project-picker").open && workspaceView?.gitlabDraft && !gitlabProjectDirectory(workspaceView).loaded)void loadGitLabProjects();});
$("gitlab-project-search").addEventListener("input",()=>{
  const ws=workspaceView;if(!ws?.gitlabDraft)return;
  const old=gitlabProjectDirectory(ws),search=$("gitlab-project-search").value.trim();
  if(search===old.search)return;
  clearTimeout(old.timer);
  const dir=ws.gitlabProjectDirectory={...old,search,projects:new Map(),loaded:false,loading:false,nextPage:0,error:""};
  dir.timer=setTimeout(()=>{dir.timer=null;if(ws===workspaceView && gitlabProjectDirectory(ws)===dir)void loadGitLabProjects();},300);
  renderGitLabProjectPicker(ws);
});
$("gitlab-project-picker").addEventListener("keydown",event=>{if(event.key==="Escape" && $("gitlab-project-picker").open){event.preventDefault();$("gitlab-project-picker").open=false;$("gitlab-project-summary").focus();}});
$("gitlab-project-more").addEventListener("click",()=>loadGitLabProjects());
$("gitlab-project-refresh").addEventListener("click",()=>loadGitLabProjects(true));
function notificationPlatforms(ws = workspaceView) { return [...(ws?.platforms || []), ...(ws?.larkPlatforms || [])]; }
function notificationView(ws, item, target = activeNotificationTarget(ws)) {
  return item.provider === "gitlab" ? ws?.gitlabPlatforms?.find(v => v.platform_id === item.id && sameNotificationTarget(v.config.target,target))
    : ws?.larkSources?.find(v => v.config.platform_id === item.id && sameNotificationTarget(v.config.target,target));
}
function notificationConfigured(item, view) { return item.provider === "gitlab" ? !!view?.version || !!view?.events?.subscription_id || !!view?.todos?.subscription_id : !!view?.subscription_id; }
function notificationIssue(ws, item, view) {
  const error = view?.target_error_code || (item.provider === "gitlab" ? Object.values(view?.errors || {}).find(Boolean) || view?.events?.error_code || view?.todos?.error_code || view?.events?.delivery_error_code || view?.todos?.delivery_error_code : view?.error_code || view?.delivery_error_code);
  return !ws.notificationsConnected ? "通知状态暂不可读" : ws.notificationToggleError?.key === notificationScopeKey(view?.config.target,item.id) ? ws.notificationToggleError.message
    : error ? notificationTargetErrors[error] || gitlabEventErrors[error] || "通知需处理，请查看详情"
    : item.validation?.status !== "valid" ? "账号需要核验" : item.provider === "gitlab" && Object.values(view?.pending || {}).some(Boolean) ? "部分通知等待应用" : "";
}
function notificationDescription(config) {
  if (!config) return "设置接收";
  const content = config.todos || config.events ? [config.todos?.enabled ? "我的待办" : "", config.events?.enabled ? "项目互动" : ""].filter(Boolean).join("、")
    : [config.private?.enabled ? config.private.scope === "all" ? "全部人员私聊" : "指定私聊" : "", config.groups?.enabled ? `指定群聊 · ${config.groups.message_mode === "all" ? "全部消息" : [config.groups.at_me ? "@我" : "",config.groups.at_all ? "@所有人" : ""].filter(Boolean).join("、")}` : ""].filter(Boolean).join("、");
  return `${config.enabled ? "" : "已暂停 · "}${content || "未选择内容"}`;
}
function notificationRow(ws, item, target, editable = false) {
  const view=notificationView(ws,item,target) || {config:{}}, row=document.createElement("div"), detail=document.createElement("button");
  row.className="notification-source-row";row.dataset.platformId=item.id;row.dataset.membershipId=target.membership_id || "";
  detail.type="button";detail.className="notification-source";detail.dataset.provider=item.provider;
  detail.setAttribute("aria-controls","notification-detail");detail.setAttribute("aria-label",`${item.name} 通知详情`);
  const avatar=document.createElement("span"),label=document.createElement("span"),title=document.createElement("strong"),summary=document.createElement("small"),arrow=editable ? null : document.createElement("button");
  avatar.className="avatar integration-avatar";avatar.textContent=item.provider==="gitlab" ? "GL" : item.provider==="scion" ? "SC" : "飞";avatar.setAttribute("aria-hidden","true");
  label.className="notification-source-name";title.textContent=item.name;summary.textContent=notificationDescription(view.config);label.append(title,summary);detail.append(avatar,label);
  if(arrow){arrow.type="button";arrow.className="notification-source-arrow";arrow.textContent="›";arrow.setAttribute("aria-label",`${item.name} 通知详情`);}
  const open=opener=>target.mode==="inbox" ? openMemberPlatform(opener,item,target) : openNotificationSource(opener,"gitlab",item.id,target);
  detail.addEventListener("click",()=>open(detail));if(arrow)arrow.addEventListener("click",()=>open(arrow));
  const toggle=document.createElement("input");toggle.type="checkbox";toggle.className="notification-switch";
  toggle.checked=ws.notificationToggle===item.id && sameNotificationTarget(ws.notificationToggleTarget,target) ? ws.notificationToggleEnabled : !!view.config.enabled;
  toggle.setAttribute("role","switch");toggle.setAttribute("aria-label",`${item.name} 接收通知`);
  toggle.disabled=notificationWritePending(ws,notificationScopeKey(target,item.id)) || !state.catalogConnected || !ws.notificationsConnected || !!ws.notificationToggle || !!ws.gitlabSending || !!ws.messagesSending || managementBusy();
  toggle.addEventListener("change",()=>notificationConfigured(item,notificationView(ws,item,target)) ? togglePlatformNotification(ws,item,toggle.checked,target) : initializeMemberNotifications(item,target,toggle.checked));
  const issue=document.createElement("p");issue.className="notification-source-issue";issue.setAttribute("role","status");issue.textContent=notificationIssue(ws,item,view);issue.hidden=!issue.textContent;
  row.append(detail,toggle,...(arrow ? [arrow] : []),issue);return row;
}
function renderNotificationList(prefix, target) {
  const ws=workspaceView,items=notificationPlatforms(ws).filter(item=>target.mode==="inbox" || item.provider==="gitlab");
  const configured=items.filter(item=>notificationConfigured(item,notificationView(ws,item,target)));
  $(`${prefix}platforms`).replaceChildren(...configured.map(item=>notificationRow(ws,item,target)));
  $(`${prefix}source-count`).textContent=String(configured.length);
  $(`${prefix}empty`).hidden=!!configured.length;
  $(`${prefix}empty`).textContent=ws?.platformsLoading ? "正在读取平台…" : target.mode==="inbox" ? "尚未配置来源，添加通知来源后开始接收。" : "将平台通知发送到当前房间。";
  const unavailable=!!ws && (!ws.notificationsConnected || !!ws.repositoryPlatformsError);
  notice(`${prefix}list-error`,unavailable ? ws.repositoryPlatformsError || "通知状态暂不可读，请重新读取。" : "");
  $(`${prefix}list-reload`).hidden=!unavailable;
  const query=$(`${prefix}add-search`).value.trim().toLowerCase();
  $(`${prefix}add-options`).replaceChildren(...items.filter(item=>!configured.includes(item) && [item.name,item.validation?.username,item.url].join(" ").toLowerCase().includes(query)).map(item=>{
    const button=document.createElement("button");button.type="button";button.className="notification-add-option";button.dataset.provider=item.provider;button.dataset.platformId=item.id;
    const name=document.createElement("strong"),account=document.createElement("small");name.textContent=item.name;account.textContent=`${item.validation?.username ? `@${item.validation.username} · ` : ""}设置接收 ›`;button.append(name,account);
    button.disabled=!ws.notificationsConnected || managementBusy();
    button.addEventListener("click",()=>{ setNotificationPicker(prefix,false);return openNotificationSource(button,item.provider==="lark" ? "messages" : "gitlab",item.id,target); });return button;
  }));
  return configured;
}
function renderNotificationPlatforms() {
  const ws=workspaceView;
  renderNotificationList("notification-",{});
  renderMemberPlatforms();
  const orphanViews=[...(ws?.gitlabPlatforms || []),...(ws?.larkSources || [])].filter(v=>v.config.target?.mode==="inbox" && !state.members.some(m=>sameNotificationTarget(memberNotificationTarget(m),v.config.target)));
  $("notification-orphans").hidden=!orphanViews.length;
  $("notification-orphan-items").replaceChildren(...orphanViews.map(v=>{
    const id=v.platform_id || v.config.platform_id;
    const item=notificationPlatforms(ws).find(p=>p.id===id) || {id,name:"原平台暂不可用",provider:v.provider || "gitlab"};
    const row=notificationRow(ws,item,v.config.target);
    row.children[0].children[1].children[0].textContent=`${v.config.target.agent_id} · ${item.name}`;
    row.children[0].children[1].children[1].textContent="成员已移除 · 查看诊断";
    return row;
  }));
}
function setNotificationPicker(prefix, open) {
  $(`${prefix}add-picker`).hidden=!open;
  $(`${prefix}add-toggle`).setAttribute("aria-expanded",String(open));
  if(open)$(`${prefix}add-search`).focus({preventScroll:true});
}
for(const prefix of ["notification-","member-notification-"]) {
  $(`${prefix}add-toggle`).addEventListener("click",()=>setNotificationPicker(prefix,$(`${prefix}add-picker`).hidden));
  $(`${prefix}add-search`).addEventListener("input",renderNotificationPlatforms);
  $(`${prefix}add-picker`).addEventListener("keydown",event=>{
    if(event.key!=="Escape")return;
    event.preventDefault();event.stopPropagation?.();setNotificationPicker(prefix,false);$(`${prefix}add-toggle`).focus();
  });
}
async function togglePlatformNotification(ws, item, enabled, target=activeNotificationTarget(ws)) {
  if(ws!==workspaceView || !ws.notificationsConnected || ws.notificationToggle || ws.gitlabSending || ws.messagesSending || managementBusy())return;
  const view=notificationView(ws,item,target);if(!notificationConfigured(item,view) || notificationWritePending(ws,notificationScopeKey(target,item.id)))return;
  const epoch=state.catalog.service_epoch,scope=ws.notificationScope,gitlab=item.provider==="gitlab",key=notificationScopeKey(target,item.id);
  beginNotificationWrite(ws,key);
  const revisionKey=gitlab ? "gitlabRevision" : "messagesRevision";ws[revisionKey]=(ws[revisionKey] || 0)+1;
  const revision=ws[revisionKey];
  const current=()=>notificationContextCurrent(ws,scope,epoch) && ws[revisionKey]===revision;
  ws.notificationToggle=item.id;ws.notificationToggleTarget=target;ws.notificationToggleEnabled=enabled;ws.notificationToggleError=null;renderGitLabEvents();
  try {
    const body={service_epoch:epoch,caller:{kind:"human",id:ws.operator},...ws.room,config:{...structuredClone(view.config),enabled,target:notificationRequestTarget(target)}};
    if(gitlab)Object.assign(body,{platform_id:item.id,version:view.version});
    else Object.assign(body,{provider:"lark",channel:"messages",subscription_id:view.subscription_id,subscription_version:view.subscription_version});
    const next=await request(gitlab ? "/v1/platform/gitlab/notifications/configure" : "/v1/platform/notifications/configure",body,undefined,50000);
    if(!current())return;
    if(!sameNotificationTarget(next.config.target,target) || next.config.platform_id!==item.id)throw Error("Notification scope mismatch");
    if(gitlab){
      ws.gitlabPlatforms=[...(ws.gitlabPlatforms || []).filter(v=>notificationScopeKey(v.config.target,v.platform_id)!==key),next];
      if(ws.gitlab?.config.platform_id===item.id && sameNotificationTarget(activeNotificationTarget(ws),target))acceptGitLabView(ws,next);
    } else {
      ws.larkSources=[...(ws.larkSources || []).filter(v=>notificationScopeKey(v.config.target,v.config.platform_id)!==key),next];
      if(ws.messages?.config.platform_id===item.id && sameNotificationTarget(activeNotificationTarget(ws),target)){ws.messages=next;$("lark-enabled").checked=!!next.config.enabled;}
    }
  }catch(error){
    if(current())ws.notificationToggleError={key,message:error.code==="request" ? "请先在详情完善接收设置" : error.code==="conflict" ? "设置已变化，请核对后重试" : "结果未确认，请重新读取"};
  }finally{
    ws.notificationWrites?.delete(key);
    if(current()){ws[revisionKey]++;ws.notificationToggle=null;await syncGitLabNotifications(state.catalog,ws);renderGitLabEvents();}
    else if(ws===workspaceView)renderGitLabEvents();
  }
}
function renderGitLabEvents() {
  renderNotificationPlatforms(); renderGitLabTodos(); renderLarkMessages(); renderNotificationSource();
  const ws = workspaceView, data = ws?.events, config = ws?.gitlabDraft?.events;
  $("gitlab-events").hidden = !data || !data.available && !data.subscription_version;
  $("gitlab-events-fields").disabled = !config || gitlabSettingsBusy(ws);
  if (!data || !config) { renderGitLabSettings(); return; }
  renderGitLabProjectPicker(ws);
  $("gitlab-events-enabled").checked = config.enabled;
  for (const input of gitlabEventTypeInputs()) input.checked = !ws.eventsEmptyTypes && (!config.filter.types.length || config.filter.types.includes(input.value));
  const draft = gitlabEventDraftState(data);
  $("gitlab-events-all-types").checked = draft.count === gitlabEventTypeIDs.length;
  $("gitlab-events-all-types").indeterminate = draft.count > 0 && draft.count < gitlabEventTypeIDs.length;
  $("gitlab-events-feedback").textContent = draft.invalid ? draft.targetInvalid ? "请选择已验证连接和项目" : "请检查活动筛选" : "";
  $("gitlab-events-action-hint").textContent = draft.targetChanged && data.subscription_id ? "更换连接、账号或项目将新建订阅；旧进度与积压保留。" : "";
  notice("gitlab-events-error", gitlabApplyNotice(ws, "events") || notificationTargetErrors[data.delivery_error_code] || gitlabEventErrors[data.delivery_error_code] || gitlabEventErrors[data.error_code] || "");
  renderGitLabSettings();
}
function editGitLabEvents(event) {
  const ws = workspaceView;
  if (!ws?.gitlabDraft || gitlabSettingsBusy(ws)) { renderGitLabEvents(); return; }
  const types = gitlabEventTypeInputs().filter(input => input.checked).map(input => input.value);
  ws.eventsEmptyTypes = !types.length;
  Object.assign(ws.gitlabDraft.events, { enabled: $("gitlab-events-enabled").checked,
    filter: { types: types.length === gitlabEventTypeIDs.length ? [] : types } });
  if (event?.target === $("gitlab-events-enabled")) $("gitlab-events-settings").open = event.target.checked;
  editGitLabConfig();
}
for (const id of ["gitlab-events-enabled", ...gitlabEventTypeIDs.map(id => `gitlab-events-type-${id}`)]) {
  for (const event of ["input", "change"]) $(id).addEventListener(event, editGitLabEvents);
}
$("gitlab-events-all-types").addEventListener("change", () => {
  for (const input of gitlabEventTypeInputs()) input.checked = $("gitlab-events-all-types").checked;
  editGitLabEvents();
});
const gitlabTodoReasons = ["mentioned", "assigned", "review_requested", "other"];
const gitlabTodoTypes = ["merge_request", "work_item"];
const gitlabTodoErrors = { ...gitlabEventErrors, credential_unavailable: "个人连接凭据不可用，请在平台中重新验证。", binding_changed: "账号身份已变化，请核对后显式保存。", scan_incomplete: "补查尚未完成，将按检查周期继续；完整同步时间尚未更新。" };
function setSelectOptions(id, options, value) {
  const select = $(id), selectedValue = value ?? select.value;
  const signature = JSON.stringify(options.map(option => [option.value, option.textContent, !!option.disabled]));
  const customizable = globalThis.CSS?.supports?.("appearance", "base-select") && select.closest?.(".notification-settings,.settings-dialog");
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
function todoConfig(config) {
  return { enabled: !!config.enabled,
    filter: { reasons: [...(config.filter?.reasons ?? gitlabTodoReasons)], types: [...(config.filter?.types ?? gitlabTodoTypes)] } };
}
function todoConfigKey(config) {
  const c = todoConfig(config);
  c.filter.reasons.sort(); c.filter.types.sort();
  return JSON.stringify(c);
}
function todoPlatform(ws) { return ws.platforms?.find(item => item.id === ws.gitlabDraft?.platform_id); }
function todoVerified(connection) { return connection?.validation?.status === "valid" && connection.validation.user_id > 0; }
function gitlabTodoDraftState(ws) {
  const data = ws.todos, config = ws.gitlabDraft.todos;
  const connection = todoPlatform(ws);
  const switching = data.subscription_id && todoVerified(connection) && (data.recipient_id !== connection.validation.user_id || data.instance !== connection.url);
  const dirty = todoConfigKey(config) !== todoConfigKey(ws.gitlab.config.todos) || !!switching && config.enabled;
  const unavailable = config.enabled && !todoVerified(connection);
  return { connection, switching, dirty, unavailable };
}
function fillGitLabTodos(config) {
  $("gitlab-todos-enabled").checked = config.enabled;
  for (const [group, values] of [["reason", gitlabTodoReasons], ["type", gitlabTodoTypes]])
    for (const value of values) $(`gitlab-todos-${group}-${value}`).checked = config.filter[group === "reason" ? "reasons" : "types"].includes(value);
}
function renderGitLabTodos() {
  const ws = workspaceView, data = ws?.todos, config = ws?.gitlabDraft?.todos;
  $("gitlab-todos-fields").disabled = !config || gitlabSettingsBusy(ws);
  if (!data || !config) { notice("gitlab-todos-error", ""); return; }
  fillGitLabTodos(config);
  const { switching, unavailable } = gitlabTodoDraftState(ws);
  $("gitlab-todos-feedback").textContent = unavailable ? "请检查接收账号的连接" : "";
  $("gitlab-todos-action-hint").textContent = switching && config.enabled ? "保存所选平台的通知设置，其他平台保留各自设置和进度。" : "";
  if (!config.filter.reasons.length || !config.filter.types.length) $("gitlab-todos-action-hint").textContent += " 当前过滤为空，不接收匹配项。";
  notice("gitlab-todos-error", gitlabApplyNotice(ws, "todos") || notificationTargetErrors[data.delivery_error_code] || gitlabTodoErrors[data.delivery_error_code] || gitlabTodoErrors[data.error_code] || "");
}
function editGitLabTodos(event) {
  const ws = workspaceView;
  if (!ws?.gitlabDraft || gitlabSettingsBusy(ws)) { renderGitLabEvents(); return; }
  Object.assign(ws.gitlabDraft.todos, { enabled: $("gitlab-todos-enabled").checked,
    filter: { reasons: gitlabTodoReasons.filter(value => $(`gitlab-todos-reason-${value}`).checked), types: gitlabTodoTypes.filter(value => $(`gitlab-todos-type-${value}`).checked) } });
  if (event?.target === $("gitlab-todos-enabled")) $("gitlab-todos-settings").open = event.target.checked;
  editGitLabConfig();
}
for (const id of ["enabled", ...gitlabTodoReasons.map(value => `reason-${value}`), ...gitlabTodoTypes.map(value => `type-${value}`)]) {
  for (const event of ["input", "change"]) $(`gitlab-todos-${id}`).addEventListener(event, editGitLabTodos);
}

const gitlabChannels = ["events", "todos"];
function gitlabApplyNotice(ws, channel) {
  const code = ws?.gitlab?.errors?.[channel];
  if (code === "sync_failed") return "同步未完成，请核对连接后重试同步。";
  return code ? "设置已保存，但此类消息尚未成功应用。核对连接与设置后，点击重试应用。" : ws?.gitlab?.pending?.[channel] ? "设置等待应用，请点击重试应用。" : "";
}
function gitlabChannelDirty(channel) {
  const ws = workspaceView;
  if (!ws?.gitlabDraft) return false;
  return channel === "events" ? gitlabEventDraftState(ws.events).dirty : gitlabTodoDraftState(ws).dirty;
}
function gitlabNeedsApply(ws) {
  if (!ws?.gitlabDraft) return false;
  return gitlabChannels.some(channel => ws.gitlab.pending?.[channel] || gitlabChannelDirty(channel));
}
function renderGitLabSettings() {
  const ws = workspaceView, shared = gitlabDraft();
  const busy = gitlabSettingsBusy(ws);
  $("gitlab-shared-fields").disabled = busy;
  $("gitlab-interval").disabled = busy;
  $("gitlab-interval").value = shared.interval_seconds;
  if (!ws?.gitlabBackfillEditing) fillBackfill("gitlab",shared);
  const backfillLocked=!!ws?.gitlab?.backfill_locked || !!ws?.events?.subscription_id || !!ws?.todos?.subscription_id;
  const backfillError=renderBackfill("gitlab",backfillLocked,shared.backfill_start || ws?.events?.baseline || ws?.todos?.baseline,busy);
  if (!backfillLocked) $("gitlab-backfill-help").textContent="首次开启后固定接收起点。";
  const connectionID = shared.platform_id, options = gitlabPlatformOptions(connectionID, "请选择连接");
  setSelectOptions("gitlab-connection", options, connectionID);$("gitlab-connection").hidden=true;
  const connection = ws?.platforms?.find(item => item.id === connectionID);
  $("gitlab-connection").title = connection?.url || "";
  $("gitlab-notification-account").textContent = todoVerified(connection) ? `接收账号：@${connection.validation.username}（ID ${connection.validation.user_id}，已验证）`
    : ws?.platformsError || (connectionID ? "请先验证所选连接。" : "");
  $("gitlab-notification-account").hidden = !$("gitlab-notification-account").textContent;
  const target = JSON.stringify(activeNotificationTarget(ws));
  setSelectOptions("gitlab-target", [new Option("固定接收者",target)], target);$("gitlab-target").disabled=true;
  for (const channel of gitlabChannels) {
    $(`gitlab-${channel}-enabled`).disabled = busy;
    if (gitlabApplyNotice(ws, channel)) $(`gitlab-${channel}-settings`).open = true;
    const data = ws?.[channel], error = ws?.gitlab?.errors?.[channel] || data?.error_code || data?.delivery_error_code;
    $(`gitlab-${channel}-state`).textContent = !ws?.gitlabConnected ? "状态不可读" : ws.gitlab.pending?.[channel] ? "等待应用" : gitlabChannelDirty(channel) ? "待保存"
      : !shared[channel].enabled || !shared.enabled ? "" : error ? "需处理" : !data?.status || data.status==="disabled" ? "采集未启动" : "";
    $(`gitlab-${channel}-summary`).setAttribute("data-error", String(!!error));
    const errors = channel === "events" ? gitlabEventErrors : gitlabTodoErrors;
    const problem = gitlabApplyNotice(ws, channel) || notificationTargetErrors[data?.delivery_error_code] ||
      errors[data?.delivery_error_code] || errors[data?.error_code] || data?.delivery_error_code || data?.error_code || "";
    const status = $(`gitlab-${channel}-status`);
    status.textContent = !ws?.gitlabConnected || !data ? "状态暂不可读，请重新读取。" :
      [notificationSyncSummary(data), problem].filter(Boolean).join(" · ");
    status.setAttribute("data-error", String(!!problem || !ws?.gitlabConnected));
  }
  const pending = gitlabNeedsApply(ws), dirty = !!ws?.gitlabDirty || !!shared.platform_id && !ws?.gitlab?.version && !ws?.events?.subscription_id && !ws?.todos?.subscription_id;
  const invalid = !!backfillError || !Number.isInteger(shared.interval_seconds) || shared.interval_seconds < 3 || shared.interval_seconds > 120 || !!ws?.gitlabDraft && (gitlabEventDraftState(ws.events).invalid || gitlabTodoDraftState(ws).unavailable);
  $("gitlab-save").disabled = busy || !(dirty || pending) || invalid;
  $("gitlab-save").textContent = ws?.gitlabSending === "configure" ? "保存中…" : ws?.notificationAddition?.key===ws?.notificationScope ? "确认并返回添加" : !dirty && pending ? "重试应用" : "保存设置";
  const active = ws?.gitlab?.config.enabled !== false && (ws?.gitlab?.config.events.enabled || ws?.gitlab?.config.todos.enabled);
  $("gitlab-sync").hidden = false;
  $("gitlab-sync").disabled = busy || dirty || pending || invalid || !active;
  $("gitlab-sync").textContent = ws?.gitlabSending === "sync" ? "请求中…" : "立即同步";
  $("gitlab-feedback").textContent = ws?.gitlabError || (ws?.gitlabSending ? "正在处理 GitLab 通知设置…"
    : ws?.gitlab?.applying ? "正在应用 GitLab 通知设置…"
    : ws?.gitlab?.errors?.config === "settings_mismatch" ? "现有订阅设置不一致，请选择公共连接、投递位置和检查周期后保存。"
    : pending && !dirty ? "设置已保存，部分消息等待应用。请核对提示后重试应用。" : dirty ? invalid ? "请完善设置后保存。" : "有未保存的更改" : ws?.gitlabFeedback || "");
}
async function editGitLabShared(field) {
  const ws = workspaceView;
  if (!ws?.gitlabDraft || $("gitlab-shared-fields").disabled) { renderGitLabEvents(); return; }
  const draft = ws.gitlabDraft;
  if (field === "connection") {
    const id = $("gitlab-connection").value;
    if (id && ws.gitlabPlatforms) {
      const revision = ws.gitlabRevision = (ws.gitlabRevision || 0) + 1;
      const epoch = state.catalog.service_epoch;
      ws.gitlabSending = "select"; renderGitLabEvents();
      try {
        const params = new URLSearchParams({service_epoch:epoch,caller_id:ws.operator,...ws.room,platform_id:id,...notificationTargetQuery(activeNotificationTarget(ws))});
        const view = await request(`/v1/platform/gitlab/notifications?${params}`);
        if (ws !== workspaceView || state.catalog?.service_epoch !== epoch || ws.gitlabRevision !== revision) return;
        acceptGitLabView(ws, view, true); ws.gitlabError = ""; ws.gitlabFeedback = "";
      } catch {
        if (ws !== workspaceView || ws.gitlabRevision !== revision) return;
        ws.gitlabError = "平台通知设置暂不可读，请重新选择。";
      } finally {
        if (ws === workspaceView && ws.gitlabRevision === revision) { ws.gitlabRevision++; ws.gitlabSending = false; renderGitLabEvents(); }
      }
      return;
    }
    draft.platform_id = id;
  }
  else if (field === "interval") draft.interval_seconds = Number($("gitlab-interval").value);
  else return;
  editGitLabConfig();
  if(field==="connection" && $("gitlab-project-picker").open)void loadGitLabProjects();
}
for (const field of ["connection", "target", "interval"]) $(`gitlab-${field}`).addEventListener("change", () => editGitLabShared(field));
$("gitlab-interval").addEventListener("input", () => editGitLabShared("interval"));
function editGitLabBackfill() {
  const ws=workspaceView;
  if (!ws?.gitlabDraft || $("gitlab-backfill").disabled || $("gitlab-shared-fields").disabled) return;
  ws.gitlabBackfillEditing=true;
  delete ws.gitlabDraft.backfill_start;
  Object.assign(ws.gitlabDraft,backfillDraft("gitlab"));
  editGitLabConfig();
}
$("gitlab-backfill").addEventListener("change",editGitLabBackfill);
$("gitlab-backfill-start").addEventListener("change",editGitLabBackfill);
$("gitlab-backfill-start").addEventListener("input",editGitLabBackfill);
async function changeGitLabSettings(action) {
  const ws = workspaceView;
  if (!ws || $(action === "configure" ? "gitlab-save" : "gitlab-sync").disabled) return;
  const payload = { service_epoch: state.catalog.service_epoch, caller: { kind: "human", id: ws.operator }, ...ws.room, version: ws.gitlab.version };
  payload.platform_id = action === "configure" ? ws.gitlabDraft.platform_id : ws.gitlab.config.platform_id;
  const scope=ws.notificationScope;
  if(action === "sync") payload.target=notificationRequestTarget(activeNotificationTarget(ws));
  if (action === "configure") {
    payload.config = {...structuredClone(ws.gitlabDraft),target:notificationRequestTarget(activeNotificationTarget(ws))};
    if (ws.gitlab.config.platform_id !== payload.platform_id && ws.gitlabPlatforms) payload.version = ws.gitlabPlatforms.find(view => view.platform_id === payload.platform_id && sameNotificationTarget(view.config.target,activeNotificationTarget(ws)))?.version || 0;
  }
  if(action==="configure" && captureMemberPlatformDraft(payload.config))return;
  beginNotificationWrite(ws,scope);
  ws.gitlabRevision = (ws.gitlabRevision || 0) + 1; ws.gitlabSending = action; ws.gitlabError = "";
  const revision=ws.gitlabRevision;
  const current=()=>notificationContextCurrent(ws,scope,payload.service_epoch) && ws.gitlabRevision===revision;
  renderGitLabEvents();
  try {
    const view = await request(`/v1/platform/gitlab/notifications/${action}`, payload);
    if (!current()) return;
    if(view.config.platform_id!==payload.platform_id || !sameNotificationTarget(view.config.target,activeNotificationTarget(ws)))throw Error("Notification scope mismatch");
    acceptGitLabView(ws, view, true);
    ws.gitlabPlatforms = [...(ws.gitlabPlatforms || []).filter(v => v.platform_id !== view.config.platform_id || !sameNotificationTarget(v.config.target,view.config.target)), {...view, platform_id:view.config.platform_id}];
    ws.gitlabFeedback = action === "sync" ? "已发起同步，结果将自动更新。" : "设置已保存。";
  } catch (error) {
    if (!current()) return;
    ws.gitlabError = error.code === "conflict" ? action === "sync" ? "配置或账号已变化，请核对最新状态。" : "设置版本已变化，请核对最新状态后重新保存。" : error.code === "request" ? "设置无效，请检查连接、项目和筛选。" : "结果未确认，请核对最新状态后显式重试；不会自动重发。";
    ws.gitlabSending = false; await syncGitLabNotifications(state.catalog, ws);
  } finally {
    ws.notificationWrites?.delete(scope);
    if (current()) { ws.gitlabRevision++; ws.gitlabSending = false; renderGitLabEvents(); }
    else if(ws===workspaceView)renderGitLabEvents();
  }
}
$("gitlab-save").addEventListener("click", () => changeGitLabSettings("configure"));
$("gitlab-sync").addEventListener("click", () => changeGitLabSettings("sync"));
let notificationFocus = null;
function renderContextViews() {
  const detail = $("activity-dialog").open || $("notification-detail").open;
  $("member-list-view").hidden = detail;
  $("member-list-view").inert = detail || !panels.members;
  $("member-list-view").setAttribute("aria-hidden", String(detail || !panels.members));
}
function renderNotificationSource() {
  const ws = workspaceView, messages = ws?.notificationChannel === "messages";
  const id = ws?.notificationPlatformID || (messages ? ws?.messages?.config.platform_id : ws?.gitlabDraft?.platform_id);
  const item = memberPlatformItems(ws).find(p => p.id === id), view = item && notificationView(ws, item);
  const target=activeNotificationTarget(ws),member=state.members.find(m=>sameNotificationTarget(memberNotificationTarget(m),target));
  const name=target.mode==="inbox" ? memberRole(member || {})?.name || target.agent_id : "房间";
  $("notification-title").textContent=`${name} · ${item?.name || memberPlatformItems(ws).find(p=>p.id===id)?.name || (messages ? "飞书" : "GitLab")} ${target.mode==="inbox" ? "设置" : "通知"}`;
  let instance="";try{instance=item?.provider==="gitlab" ? new URL(item.url).host : "";}catch{instance=item?.url || "";}
  $("notification-account").textContent=item ? [item.validation?.username ? `@${item.validation.username}` : "账号待核验",instance].filter(Boolean).join(" · ") : "";
  $("notification-account").title=item?.url || "";
  const coexist=target.mode==="inbox" && notificationView(ws,item || {},{})?.config.enabled || target.mode!=="inbox" && ws?.gitlabPlatforms?.some(v=>v.platform_id===id && v.config.target?.mode==="inbox" && v.config.enabled);
  $("notification-destination").textContent=(target.mode==="inbox" ? `发送到 ${name} 的收件箱${member ? "" : "（原成员已移除）"}` : "发送到当前房间，成员可见") + (coexist ? "。成员和房间分别订阅时，可能同时看到同一通知。" : "");
  $("notification-sources").hidden=!ws;
  const matches=!!id && (messages ? ws?.messages?.config.platform_id : ws?.gitlabDraft?.platform_id)===id && sameNotificationTarget(messages ? ws?.messages?.config.target : ws?.gitlabDraft?.target,target);
  const connected=matches && (messages ? ws?.messagesConnected : ws?.gitlabConnected) && !ws?.notificationHistory;
  $("notification-panel-messages").hidden=!messages || !connected;$("notification-panel-gitlab").hidden=messages || !connected;
  notice("notification-notice",ws?.notificationHistory || (!connected ? "通知状态暂不可读，请重新读取。" : item && item.validation?.status!=="valid" ? "账号需要核验，请在管理平台中处理。" : ""));
  $("notification-reload").hidden=!!connected || !!ws?.notificationHistory;
  const configured=item && notificationConfigured(item,view),enabled=configured ? !!view.config.enabled : messages ? !!ws?.messages?.config.enabled : !!ws?.gitlabDraft?.enabled;
  const toolsOnly=ws?.notificationChannel==="tools",history=!!ws?.notificationHistory;
  $("notification-destination").hidden=toolsOnly || history;
  $("notification-destination").classList.toggle("sr-only",!coexist && (target.mode!=="inbox" || !!member));
  const status=!connected || toolsOnly || history ? "" : configured && !enabled ? "通知已暂停" : !configured ? "尚未开启通知" : "";
  notice("notification-enabled-state",status);
  notice("notification-toggle-error",ws?.notificationToggleError?.key===notificationScopeKey(target,id) ? ws.notificationToggleError.message : "");
  const backLabel=ws?.notificationReturn ? "返回成员详情" : "返回房间通知";
  $("close-notification").setAttribute("aria-label",backLabel);$("close-notification").title=backLabel;
  if(history){$("notification-title").textContent="通知来源诊断";$("notification-account").textContent="";$("notification-destination").textContent="";}
  if(toolsOnly){$("notification-panel-messages").hidden=true;$("notification-panel-gitlab").hidden=true;notice("notification-notice","此平台暂无可调整的配置，工具启停请返回平台列表。");$("notification-reload").hidden=true;}
  $("gitlab-manage-platforms").disabled = !ws;
  renderContextViews();
}
async function openNotificationSource(opener, channel, platformID, target = {}) {
  const ws=workspaceView;if(!ws || !platformID || channel==="messages" && target.mode!=="inbox")return;
  const navigation=++notificationNavigation;
  const epoch=state.catalog?.service_epoch,opening=ws.notificationOpen=(ws.notificationOpen || 0)+1;
  rememberNotificationDraft(ws);
  const key=notificationScopeKey(target,platformID),same=ws.notificationScope===key;
  notificationFocus=opener;
  if($("activity-dialog").open && !ws.notificationReturn)ws.notificationReturn={info:{...state.info},member:{...activityMember},tab:agentTab,platformScroll:$("platforms-member-view").scrollTop,activityScroll:$("activity-dialog").scrollTop,bodyScroll:$("activity-body").scrollTop,policyScroll:$("policy-member-view").scrollTop,scroll:$("inbox-member-view").scrollTop,windowScroll:{left:window.scrollX,top:window.scrollY}};
  ws.notificationTarget=structuredClone(target);ws.notificationScope=key;ws.notificationChannel=channel==="messages" ? "messages" : channel==="tools" ? "tools" : "gitlab";ws.notificationPlatformID=platformID;ws.notificationHistory="";
  if(!same){
    stopLarkChatSearch();ws.gitlabRevision=(ws.gitlabRevision || 0)+1;ws.messagesRevision=(ws.messagesRevision || 0)+1;
    ws.gitlabSending=false;ws.messagesSending=false;ws.notificationToggle=null;ws.notificationToggleError=null;
    ws.gitlab=null;ws.gitlabDraft=null;ws.events=null;ws.todos=null;ws.messages=null;ws.gitlabConnected=false;ws.messagesConnected=false;
    ws.gitlabDirty=false;ws.larkDirty=false;ws.eventsEmptyTypes=false;ws.gitlabBackfillEditing=false;ws.gitlabError="";ws.gitlabFeedback="";ws.messagesError="";ws.messagesFeedback="";ws.larkChatDirectory=null;ws.gitlabProjectDirectory=null;
  }
  $("activity-dialog").hidden=true;activity.close();panels.members=true;savePanels();renderPanels();
  if(!$("notification-detail").open)$("notification-detail").show();
  renderGitLabEvents();
  await loadWorkspacePlatforms();
  if(!notificationContextCurrent(ws,key,epoch) || opening!==ws.notificationOpen || navigation!==notificationNavigation)return;
  if(channel==="tools"){renderNotificationSource();return;}
  if(channel==="messages"){
    if(!same){
      selectLarkPlatform(ws,platformID);
      const cached=ws.notificationDrafts?.get(key);
      if(cached){ws.larkRestore={...cached.config,enabled:ws.messages.config.enabled};ws.larkChatDirectory=cached.directory;ws.larkDirty=true;}
    }
    ws.messagesConnected=!!ws.notificationsConnected;
  }else if(!same || !ws.gitlabConnected){
    const revision=ws.gitlabRevision=(ws.gitlabRevision || 0)+1;
    ws.gitlabSending="select";renderGitLabEvents();
    try{
      const params=new URLSearchParams({service_epoch:epoch,caller_id:ws.operator,...ws.room,platform_id:platformID,...notificationTargetQuery(target)});
      const view=await request(`/v1/platform/gitlab/notifications?${params}`);
      if(!notificationContextCurrent(ws,key,epoch) || revision!==ws.gitlabRevision || navigation!==notificationNavigation)return;
      if(view.config.platform_id!==platformID || !sameNotificationTarget(view.config.target,target))throw Error("Notification scope mismatch");
      acceptGitLabView(ws,view,true);
      const cached=ws.notificationDrafts?.get(key);
      if(cached){ws.gitlabDraft=structuredClone(cached.config);ws.gitlabDraft.enabled=view.config.enabled;ws.eventsEmptyTypes=cached.emptyTypes;ws.gitlabDirty=!!cached.emptyTypes || gitlabConfigKey(ws.gitlabDraft)!==gitlabConfigKey(view.config);fillBackfill("gitlab",ws.gitlabDraft);}
    }catch{
      if(!notificationContextCurrent(ws,key,epoch) || revision!==ws.gitlabRevision || navigation!==notificationNavigation)return;
      ws.gitlabConnected=false;ws.gitlabError="平台通知设置暂不可读，请重新读取。";
    }finally{if(notificationContextCurrent(ws,key,epoch) && revision===ws.gitlabRevision){ws.gitlabSending=false;ws.gitlabRevision++;}}
  }
  if(!notificationContextCurrent(ws,key,epoch) || opening!==ws.notificationOpen || navigation!==notificationNavigation)return;
  if(channel==="events" || channel==="todos")$(`gitlab-${channel}-settings`).open=true;
  renderGitLabEvents();$("notification-title").focus();
  if(window.innerWidth<=640)$("notification-detail").scrollIntoView({block:"start"});
}
async function openNotificationMessage(opener, source) {
  const ws=workspaceView,epoch=state.catalog?.service_epoch,navigation=++notificationNavigation;if(!ws)return;
  const showHistory=message=>{
    if(ws!==workspaceView || state.catalog?.service_epoch!==epoch || navigation!==notificationNavigation)return;
    ws.notificationHistory=message;ws.notificationPlatformID=source?.platform_id || "";notificationFocus=opener;
    $("notification-detail").show();renderNotificationSource();$("notification-title").focus();
  };
  if(!source?.subscription_id || !source?.provider || !source?.channel){showHistory("此历史消息缺少完整来源身份，无法唯一定位原配置。");return;}
  try{
    const params=new URLSearchParams({service_epoch:epoch,caller_id:ws.operator,...ws.room,provider:source.provider,channel:source.channel,subscription_id:source.subscription_id,...(source.platform_id ? {platform_id:source.platform_id} : {})});
    const data=await request(`/v1/platform/notifications?${params}`);
    if(ws!==workspaceView || state.catalog?.service_epoch!==epoch || navigation!==notificationNavigation)return;
    const views=[...(data.subscriptions || []),...(data.retired_subscriptions || [])];
    const exact=views.filter(v=>v.subscription_id===source.subscription_id && v.provider===source.provider && v.channel===source.channel && (!source.platform_id || v.platform_id===source.platform_id));
    if(exact.length!==1){showHistory("无法唯一定位此消息的原订阅；请检查来源诊断。");return;}
    const original=exact[0];
    if(source.instance && (original.instance || original.account?.instance)!==source.instance){showHistory("原订阅的实例身份与消息不匹配，请检查历史来源。");return;}
    if(original.retired){showHistory(`此消息来自已退役订阅 ${original.subscription_id}；原目标和历史消息保持不变。${notificationSyncSummary(original)}`);return;}
    await openNotificationSource(opener,source.channel==="messages" ? "messages" : source.channel,original.platform_id,original.target || original.config.target);
  }catch{showHistory("原订阅暂不可读或身份不匹配，未打开其他成员的设置。");}
}
$("close-notification").addEventListener("click", () => $("notification-detail").close());
$("notification-detail").addEventListener("close",()=>{
  if($("notification-detail").open)return;
  notificationNavigation++;
  const ws=workspaceView,back=ws?.notificationReturn;
  if(ws){ws.notificationOpen=(ws.notificationOpen || 0)+1;if(ws.gitlabSending==="select"){ws.gitlabRevision++;ws.gitlabSending=false;}}
  rememberNotificationDraft(ws);stopLarkChatSearch();
  $("activity-dialog").hidden=false;
  if(ws){ws.notificationReturn=null;ws.notificationAddition=null;}
  if(back && sameService(state.info,back.info) && selectedAgent()?.membership_id===back.member.membership_id && $("activity-dialog").open){
    (notificationFocus?.isConnected ? notificationFocus : $(`agent-tab-${agentTab}`)).focus({preventScroll:true});
    $("platforms-member-view").scrollTop=back.platformScroll;
    if(back.tab==="activity" && agentTab==="activity")void activity.select({...selectedAgent(),info:state.info}).then(()=>{if($("activity-dialog").open && selectedAgent()?.membership_id===back.member.membership_id && agentTab==="activity")$("activity-body").scrollTop=back.bodyScroll;});
    $("activity-dialog").scrollTop=back.activityScroll;$("activity-body").scrollTop=back.bodyScroll;$("policy-member-view").scrollTop=back.policyScroll;
    $("inbox-member-view").scrollTop=back.scroll;
    window.scrollTo?.(back.windowScroll);
  }else if(!$("activity-dialog").open && !$("workspace-dialog").open)(notificationFocus?.isConnected ? notificationFocus : $("notification-add-toggle")).focus();
  notificationFocus=null;renderContextViews();
});
$("notification-detail").addEventListener("keydown", event => {
  if (event.key === "Escape") {
    if (event.defaultPrevented || event.target?.closest?.("select:open")) return;
    event.preventDefault(); $("notification-detail").close();
  }
});
async function reloadNotifications() {
  const ws=workspaceView,id=ws?.notificationPlatformID,channel=ws?.notificationChannel,target=structuredClone(activeNotificationTarget(ws)),scope=ws?.notificationScope;
  await loadWorkspacePlatforms();
  if(ws!==workspaceView)return;
  await syncGitLabNotifications(state.catalog,ws);
  if(ws!==workspaceView || ws.notificationScope!==scope)return;
  if(ws.notificationsConnected)ws.notificationToggleError=null;
  if($("notification-detail").open && id)await openNotificationSource(notificationFocus,channel,id,target);
  renderGitLabEvents();
}
$("notification-reload").addEventListener("click",reloadNotifications);
$("notification-list-reload").addEventListener("click",reloadNotifications);
$("member-notification-list-reload").addEventListener("click",async()=>{await reloadNotifications();if(memberToolsView && toolsCurrent(memberToolsView))await loadCLITools(memberToolsView);});
$("gitlab-manage-platforms").addEventListener("click", () => openSettings("platforms"));
function renderWorkspacePlatforms() {
  const ws = workspaceView;
  if (!ws) return;
  const options = [new Option("请选择已有连接", "")];
  if (ws.data?.workspace?.binding && !ws.data.workspace.binding.platform_id) options.push(new Option("保留当前旧认证（尚未迁移）", "keep"));
  const provider = $("workspace-kind").value === "scion" ? "scion" : "gitlab";
  for (const item of ws.repositoryPlatforms || ws.platforms || []) {
    if (item.provider !== provider || provider === "scion" && item.validation?.status !== "valid") continue;
    options.push(new Option(`${item.name} · ${item.validation.username || item.validation.account?.id?.value || ""} · ${item.url}`, item.id));
  }
  $("workspace-connection").replaceChildren(...options);
  $("workspace-connection").value = ws.selectedPlatform || "";
}
async function loadWorkspacePlatforms() {
  const ws = workspaceView, epoch = state.catalog?.service_epoch;
  if (!ws || !epoch) return;
  const read = ws.platformsRead = (ws.platformsRead || 0) + 1;
  ws.platformsLoading = true; ws.platformsError = "正在加载平台…"; renderWorkspace();
  try {
    const query = new URLSearchParams({ service_epoch: epoch, caller_id: ws.operator });
    const data = await request(`/v1/platforms?${query}`);
    if (ws !== workspaceView || state.catalog?.service_epoch !== epoch || read !== ws.platformsRead) return;
    ws.allPlatforms = data.platforms;
    ws.repositoryPlatforms = data.platforms.filter(item => ["gitlab", "scion"].includes(item.provider));
    ws.repositoryPlatformsError = "";
    ws.platforms = data.platforms.filter(item => item.provider === "gitlab");
    ws.larkPlatforms = data.platforms.filter(item => item.provider === "lark");
    ws.platformsError = ws.platforms.length ? "" : "尚无平台，请先管理平台。";
    renderWorkspacePlatforms();
  } catch { if (ws === workspaceView && state.catalog?.service_epoch === epoch && read === ws.platformsRead) { ws.platformsError = "平台加载失败，请重新打开通知源重试。"; ws.repositoryPlatformsError = "平台加载失败，请重新打开 Workspace 设置重试。"; } }
  if (ws === workspaceView) { ws.platformsLoading = false; renderWorkspace(); }
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
  $("workspace-kind").value = target.repository?.kind || suggestion.repository?.kind || "git";
  $("workspace-repo-url").value = target.repository?.url || suggestion.repository?.url || "";
  $("workspace-branch").value = target.repository ? target.repository.branch || "" : suggestion.repository?.branch || "main";
  $("workspace-gitlab").checked = !!target.binding;
  ws.selectedPlatform = target.binding?.platform_id || (current.binding ? "keep" : "");
  renderWorkspacePlatforms();

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
  const scion = $("workspace-kind").value === "scion";
  if (scion) $("workspace-gitlab").checked = true;
  const git = $("workspace-repository").checked, gitlab = git && $("workspace-gitlab").checked;
  $("workspace-kind").disabled = repo;
  $("workspace-binding-label").textContent = scion ? "绑定 Scion 连接（必需）" : "绑定 GitLab";
  $("workspace-fields").disabled = !data || !ws.connected || workspaceFormBusy() || managementBusy();
  $("workspace-root").readOnly = repo || git;
  $("workspace-root-hint").textContent = repo ? "仓库目录已固定；各成员使用独立 clone。" : git
    ? "首次关联会在新的受管仓库目录中分别 clone；旧目录文件保留。" : "切换目录不搬迁文件，原目录保留。";
  $("workspace-repository").disabled = repo;
  $("workspace-repo-url").readOnly = repo;
  $("workspace-branch").readOnly = repo;
  $("workspace-gitlab").disabled = binding || scion;
  $("workspace-git-fields").hidden = !git;
  $("workspace-gitlab-fields").hidden = !gitlab;
  $("workspace-git-auth-hint").hidden = gitlab;
  $("workspace-repo-url").required = git;
  $("workspace-repo-url").disabled = !git;
  $("workspace-connection").disabled = !gitlab;
  $("workspace-connection-feedback").textContent = ws.repositoryPlatformsError || ($("workspace-connection").children.length <= 1 ? `尚无可选的 ${scion ? "Scion 已验证" : "GitLab"} 连接，请先管理平台。` : "选择同类型连接，应用时核实仓库访问权限；Scion 会核实分支存在。");
  $("workspace-credential-status").hidden = !binding;
  $("workspace-credential-status").textContent = binding ? data.credential_status === "configured" ? current.binding.platform_id ? current.binding.provider === "scion" ? `Scion 连接已配置 · ${current.binding.project}` : `共享连接已配置 · 项目 ID ${current.binding.project_id}（绑定时已核实）` : "旧凭据已配置（远端权限未验证）" : "凭据缺失，请检查平台或旧认证。" : "";
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
function platformProjectFromRepository(value, provider = "gitlab") {
  // Match the server's HTTPS origin and project-path rules. Keep explicit ports
  // (including :443) so the derived origin exactly matches the repository host.
  const raw = value.trim(), parts = /^https:\/\/([^/?#]+)(\/[^?#]*)?$/.exec(raw);
  const invalid = () => new Error("请填写有效的仓库 HTTPS URL，以解析平台仓库；地址不能含用户名、密码、Token 或查询参数。");
  if (!parts || raw.length > 4096 || /[\s\\]/.test(raw)) throw invalid();
  let url;
  try { url = new URL(raw); } catch { throw invalid(); }
  if (!url.hostname || url.username || url.password) throw invalid();
  const path = (parts[2] || "").replace(/^\//, "").replace(/\/$/, "");
  const project = provider === "scion" ? path : path.replace(/\.git$/, "");
  if (path.length > 2048 || !project.includes("/") || project.split("/").some(part =>
    !/^[A-Za-z0-9_.-]+$/.test(part) || part === "." || part === "..")) throw invalid();
  return { provider, url: `https://${parts[1].toLowerCase()}`, project };
}
function workspaceTarget() {
  const old = workspaceView.data.workspace;
  const git = $("workspace-repository").checked;
  const target = { root: old?.repository || !git ? $("workspace-root").value.trim() : old?.root || "" };
  if (git) {
    const kind = $("workspace-kind").value || "git";
    target.repository = old?.repository ? { ...old.repository } : { url: $("workspace-repo-url").value.trim() };
    if (!old?.repository) target.repository.kind = kind;
    if (!old?.repository && $("workspace-branch").value.trim()) target.repository.branch = $("workspace-branch").value.trim();
    if ($("workspace-gitlab").checked) {
      target.binding = platformProjectFromRepository(target.repository.url, kind === "scion" ? "scion" : "gitlab");
      if (!old?.repository) {
        target.repository.url = `${target.binding.url}/${target.binding.project}${kind === "scion" ? "" : ".git"}`;
        $("workspace-repo-url").value = target.repository.url;
      }
      const selected = $("workspace-connection").value;
      if (selected === "keep") {
        if (!old?.binding || old.binding.platform_id) throw new Error("请选择平台。");
        target.binding = { ...old.binding };
      } else {
        const connection = (workspaceView.repositoryPlatforms || workspaceView.platforms)?.find(item => item.id === selected);
        if (!connection || kind === "scion" && connection.validation?.status !== "valid") throw new Error("请选择已验证的平台，或前往管理平台。");
        if (connection.provider !== target.binding.provider || connection.url !== target.binding.url) throw new Error("所选连接与仓库实例不一致。");
        target.binding.platform_id = connection.id;
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
      if (payload.target.binding?.platform_id) payload.platform_version = (ws.repositoryPlatforms || ws.platforms).find(item => item.id === payload.target.binding.platform_id).version;
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
 ws.sending = false;
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
  $("workspace-dialog").showModal(); renderWorkspace(); $("close-workspace").focus(); await loadWorkspacePlatforms();
}
$("open-workspace").addEventListener("click", openWorkspace);
$("close-workspace").addEventListener("click", () => $("workspace-dialog").close());
$("workspace-dialog").addEventListener("close", () => {
  ($("notification-detail").open ? $("gitlab-project-summary") : $("workspace-trigger")).focus();
});
$("workspace-kind").addEventListener("change", () => { if (workspaceView) workspaceView.selectedPlatform = ""; renderWorkspacePlatforms(); renderWorkspace(); });
for (const id of ["workspace-repository", "workspace-gitlab"]) $(id).addEventListener("change", renderWorkspace);
$("workspace-connection").addEventListener("change", () => { if (workspaceView) workspaceView.selectedPlatform = $("workspace-connection").value; });
$("workspace-manage-platforms").addEventListener("click", async () => {
  $("workspace-dialog").close(); await openSettings("platforms");
});
$("workspace-form").addEventListener("submit", event => { event.preventDefault(); return submitWorkspace(); });
$("workspace-retry").addEventListener("click", () => submitWorkspace(true));
$("workspace-refresh").addEventListener("click", async () => { await refresh(); await loadWorkspacePlatforms(); });
$("workspace-new").addEventListener("click", () => {
  const ws = workspaceView;
  if (!ws?.missing || ws.sending) return;
  ws.pending = null; ws.missing = false; ws.error = "请核对目标和平台，再点击应用；原请求不会自动重发。";
   saveWorkspacePending(ws); renderWorkspace(); composerState(); renderRooms();
});

function savePanels() {
  try { storage?.setItem(panelsKey, JSON.stringify(panels)); } catch { /* Keep this page's selection in memory. */ }
}

const sidebarConfig = {
  rooms: { rail: "room-rail", handle: "room-resizer", key: "roomWidth", min: 200, max: 360 },
  members: { rail: "member-rail", handle: "member-resizer", key: "memberWidth", min: 220, max: 720 },
};
function sidebarBudget() {
  const rooms = state.managed ? (panels.rooms ? 200 : 52) : 0;
  const members = panels.members ? 220 : 0;
  const total = $("workspace").clientWidth;
  // Preserve 480px for messages where space permits; both sidebars retain their minimums on small desktops.
  return total - Math.min(480, Math.max(0, total - rooms - members));
}
function sidebarWidthLimits(side) {
  const config = sidebarConfig[side], other = side === "rooms" ? "members" : "rooms";
  const otherWidth = other === "rooms" && !state.managed ? 0 : !panels[other] ? (other === "rooms" ? 52 : 0)
    : $(sidebarConfig[other].rail).getBoundingClientRect().width;
  return { min: config.min, max: Math.max(config.min, Math.min(config.max, sidebarBudget() - otherWidth)) };
}
function applySidebarWidths(priority = "rooms") {
  if (window.innerWidth <= 640) return;
  const workspace = $("workspace"), styles = window.getComputedStyle(workspace);
  const widths = {};
  for (const [side, config] of Object.entries(sidebarConfig)) {
    const prefix = side === "rooms" ? "rooms" : "members";
    const preferred = panels[config.key] ?? Number.parseFloat(styles.getPropertyValue(`--${prefix}-default-width`));
    widths[side] = side === "rooms" && !state.managed ? 0 : !panels[side] ? (side === "rooms" ? 52 : 0)
      : Math.max(config.min, Math.min(config.max, preferred));
  }
  const budget = sidebarBudget();
  // Clamp displayed widths without overwriting the user's saved preference on a narrower window.
  for (const side of [priority === "rooms" ? "members" : "rooms", priority]) {
    if (!panels[side] || side === "rooms" && !state.managed) continue;
    const other = side === "rooms" ? "members" : "rooms";
    widths[side] = Math.max(sidebarConfig[side].min, Math.min(widths[side], budget - widths[other]));
  }
  for (const [side, config] of Object.entries(sidebarConfig)) {
    if (!panels[side] || side === "rooms" && !state.managed) continue;
    workspace.style.setProperty(`--${side === "rooms" ? "rooms" : "members"}-open-width`, `${widths[side]}px`);
  }
  for (const [side, config] of Object.entries(sidebarConfig)) {
    const limits = sidebarWidthLimits(side), handle = $(config.handle);
    handle.setAttribute("aria-valuemin", String(limits.min)); handle.setAttribute("aria-valuemax", String(limits.max));
    handle.setAttribute("aria-valuenow", String(Math.round(widths[side])));
  }
}
function setSidebarWidth(side, width) {
  const limits = sidebarWidthLimits(side);
  panels[sidebarConfig[side].key] = Math.round(Math.max(limits.min, Math.min(limits.max, width)));
  applySidebarWidths(side);
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
  applySidebarWidths();
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
for (const [side, config] of Object.entries(sidebarConfig)) {
  const handle = $(config.handle), direction = side === "rooms" ? 1 : -1;
  const enabled = () => panels[side] && (side !== "rooms" || state.managed) && window.innerWidth > 640;
  handle.addEventListener("pointerdown", event => {
    if (event.button !== 0 || event.isPrimary === false || !enabled() || sidebarResize) return;
    event.preventDefault();
    sidebarResize = { side, pointerId: event.pointerId, x: event.clientX, width: $(config.rail).getBoundingClientRect().width };
    handle.setPointerCapture(event.pointerId);
    $("workspace").classList.add(`resizing-${side}`);
  });
  handle.addEventListener("pointermove", event => {
    if (sidebarResize?.side !== side || sidebarResize.pointerId !== event.pointerId) return;
    setSidebarWidth(side, sidebarResize.width + direction * (event.clientX - sidebarResize.x));
  });
  const finish = event => {
    if (sidebarResize?.side !== side || sidebarResize.pointerId !== event.pointerId) return;
    sidebarResize = null;
    $("workspace").classList.remove(`resizing-${side}`);
    if (handle.hasPointerCapture(event.pointerId)) handle.releasePointerCapture(event.pointerId);
    savePanels();
  };
  for (const type of ["pointerup", "pointercancel", "lostpointercapture"]) handle.addEventListener(type, finish);
  handle.addEventListener("keydown", event => {
    if (!enabled()) return;
    const limits = sidebarWidthLimits(side), width = $(config.rail).getBoundingClientRect().width;
    const next = { ArrowLeft: width - direction * 20, ArrowRight: width + direction * 20, Home: limits.min, End: limits.max }[event.key];
    if (next === undefined) return;
    event.preventDefault(); setSidebarWidth(side, next); savePanels();
  });
}
window.addEventListener("resize", () => { applySidebarWidths(); renderSettingsNavigation(); });
renderPanels();
let viewVersion = 0;
const currentView = (version) => version === viewVersion;
let restartNoticeID = "", restartNoticeExpires = 0;
let mentionKey = "", mentionSeenThrough = 0;
let mentionSeen = new Set();
const mentionVisibleSince = new Map();
let mentionTimer = null, mentionFramePending = false;
const mentionDwell = 1000;

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
    author.addEventListener("click", () => openNotificationMessage(author, message.source));
    avatar.setAttribute("role", "button"); avatar.setAttribute("tabindex", "0");
    avatar.setAttribute("aria-label", "查看 GitLab 通知源");
    avatar.setAttribute("aria-controls", "notification-detail");
    avatar.addEventListener("click", () => openNotificationMessage(avatar, message.source));
    avatar.addEventListener("keydown", event => {
      if (event.key === "Enter" || event.key === " ") { event.preventDefault(); return openNotificationMessage(avatar, message.source); }
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
  time.textContent = formatDate(message.created_at, {hour:"2-digit",minute:"2-digit"});
  time.dateTime = message.created_at;
  time.title = formatDate(message.created_at);
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
    const nextDay = systemTime.day(date);
    if (nextDay !== day) {
      day = nextDay;
      const divider = document.createElement("div");
      divider.className = "day-divider";
      divider.textContent = formatDate(date, {month:"long",day:"numeric",weekday:"short"});
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

function openMemberActivity(member, opener, tab = state.managed ? "platforms" : "activity") {
  notice("notification-next-step", "");
  notificationNavigation++;
  if (!state.connected || !state.info) return;
  const current = state.members.find(item => item.agent_id === member.agent_id && item.membership_id === member.membership_id);
  if (!current) return;
  activityFocus = { element: opener, membership: current.membership_id };
  $("notification-detail").close();
  $("activity-dialog").hidden=false;
  activityMember = { agent_id: current.agent_id, membership_id: current.membership_id };
  panels.members = true; savePanels(); renderPanels();
  if (!$("activity-dialog").open) $("activity-dialog").show();
  memberToolsView = null;
  renderActivityControls();
  renderNotificationPlatforms();
  if(state.managed){void openMemberTools(current);void loadWorkspacePlatforms();void restoreOnboarding()?.then(()=>renderMemberPlatforms());}
  void selectAgentTab(tab);
  if (window.innerWidth <= 640) $("activity-dialog").scrollIntoView({ block: "start" });
}

function renderActivityControls() {
  const panel = $("activity-dialog");
  if (panel.open && !state.members.some(member => member.membership_id === activityMember?.membership_id)) panel.close();
  renderContextViews();
  const selected=selectedAgent();
  $("activity-title").textContent=selected ? memberRole(selected)?.name || selected.agent_id : "Agent 详情";
  $("agent-tabs").hidden = false;
  $("agent-tab-platforms").hidden = !state.managed;
  const member = selectedAgent();
  if (!member || !panel.open) return;
  $("agent-status").textContent = member.execution?.active ? "运行中" : statusNames[member.status] || member.status;
  $("agent-status").classList.toggle("running", !!member.execution?.active);
  $("agent-status").classList.toggle("failed", /failed|blocked|unavailable|interrupted/.test(member.status));
  renderMemberActions(member, $("agent-actions"));
  const execution = member.execution;
  const reset = execution?.session_reset_required || member.status === "backend_blocked";
  const budget = execution?.turn_budget;
  const exhausted = !execution?.session_turns && budget && budget.used >= budget.limit;
  const effects = [];
  if (reset) effects.push("重置会话，清除私有上下文");
  if (exhausted) effects.push(`追加 ${budget.grant || 16} 回合`);
  notice("agent-enable-hint", effects.length ? `启用协作将${effects.join("，并")}。` : "");
  notice("agent-error", member.execution?.error || "");
  if (memberToolsView?.cli) {
    const signature = JSON.stringify([state.connected, state.catalogConnected, discussionBusy()]);
    if (memberToolsView.availability !== signature) {
      memberToolsView.availability = signature; renderMemberTools(memberToolsView);
    }
  }
}
function selectedAgent() {
  return state.members.find(m => m.agent_id === activityMember?.agent_id && m.membership_id === activityMember?.membership_id);
}
async function selectAgentTab(tab) {
  notificationNavigation++;
  const member = selectedAgent();
  if (!$("activity-dialog").open || !member) return;
  if(agentTab==="inbox" && inboxCurrent(inboxView))inboxView.scrollTop=$("inbox-member-view").scrollTop;
  agentTab = tab === "platforms" && state.managed ? "platforms" : tab === "policy" ? "policy" : tab === "inbox" ? "inbox" : "activity";
  for (const key of ["platforms", "policy", "inbox", "activity"]) {
    const button = $(`agent-tab-${key}`);
    button.setAttribute("aria-selected", String(agentTab === key));
    button.setAttribute("tabindex", agentTab === key ? "0" : "-1");
  }
  $("activity-body").hidden = $("activity-footer").hidden = agentTab !== "activity";
  $("platforms-member-view").hidden = agentTab !== "platforms";
  $("inbox-member-view").hidden = agentTab !== "inbox";
  $("policy-member-view").hidden = agentTab !== "policy";
  if (agentTab === "activity") await activity.select({ ...member, info: state.info });
  else if (agentTab === "inbox") {
    activity.close();renderNotificationPlatforms();void loadWorkspacePlatforms();
    if(inboxView?.page && !inboxView.loading && sameService(inboxView.info,state.info) && inboxView.member.agent_id===member.agent_id && inboxView.member.membership_id===member.membership_id)$("inbox-member-view").scrollTop=inboxView.scrollTop || 0;
    else await loadMemberInbox();
  }
  else if (agentTab === "platforms") { activity.close();renderNotificationPlatforms(); }
  else if (agentTab === "policy") { activity.close(); await loadMemberPolicy(true); }
}
for (const key of ["platforms", "policy", "inbox", "activity"]) {
  const button = $(`agent-tab-${key}`);
  button.addEventListener("click", () => selectAgentTab(key));
  button.addEventListener("keydown", event => {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const keys = state.managed ? ["platforms", "policy", "inbox", "activity"] : ["policy", "inbox", "activity"];
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
  if (!view || !policyCurrent(view) || view.saving || view.loading || !view.data) return;
  view.editVersion=(view.editVersion || 0)+1;
  policyEditor = {view, record, id:record?.id || `p-${Date.now()}-${Math.random().toString(36).slice(2,10)}`};
  renderPolicyEmptyHint();
  $("policy-form").hidden = false;
  $("policy-editor-title").textContent = record ? "修改规则" : "新增规则";
  $("policy-text").value = record?.text || "";
  fillTimeInput("policy-expires",record?.expires_at);
  $("policy-text").focus();
}
function policyDateText(value) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : formatDate(date, {year:"numeric",month:"2-digit",day:"2-digit",hour:"2-digit",minute:"2-digit"});
}
function policyMetadata(record) {
  const meta = document.createElement("div"); meta.className = "policy-entry-meta";
  if (record.expires_at) {
    const expires = document.createElement("p"); bindSystemTime(expires, () => { expires.textContent = `有效期至：${policyDateText(record.expires_at)}`; }); meta.append(expires);
  }
  return meta;
}
function policyCard(view, r, history) {
  const section = document.createElement("section"); section.className = "activity-section policy-entry";section.dataset.policyId=r.id;
  const header = document.createElement("div"); header.className = "policy-entry-header";
  const title = document.createElement("h4"), status = document.createElement("span");
  const expired = r.expires_at && new Date(r.expires_at) <= new Date();
  status.className = "policy-entry-state"; status.dataset.status = expired ? "expired" : r.status;
  status.textContent = expired ? `${policyStates[r.status]} · 已过期` : policyStates[r.status]; title.append(status);
  const body = document.createElement("p"); body.className = "inbox-text policy-entry-body"; body.textContent = r.text;
  const source = document.createElement("details"), summary = document.createElement("summary"); source.className = "policy-history";
  summary.textContent = "来源与历史";
  summary.title = `当前版本 v${r.version}`;
  const identity = document.createElement("p"); identity.textContent = `规则 ID：${r.id} · 当前版本 v${r.version}`;
  source.append(summary, identity);
  for (const v of history.filter(v => v.id === r.id)) {
    const revision = document.createElement("article"); revision.className = "policy-revision";
    const heading = document.createElement("h5"); heading.textContent = `v${v.version} · ${policyStates[v.status]}`;
    const metadata = document.createElement("p"); metadata.className = "policy-revision-meta";
    const time = document.createElement("time"); time.dateTime = v.updated_at; bindSystemTime(time, () => { time.title = formatDate(v.updated_at); time.textContent = policyDateText(v.updated_at); });
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
  edit.setAttribute("aria-label", `${r.status === "suggested" ? "审核并编辑" : "编辑"}规则：${r.text}`);
  edit.addEventListener("click",()=> { if (policyCurrent(view)) editPolicy(r); }); actions.append(edit);
  if (r.status !== "revoked") {
    const revoke = document.createElement("button"); revoke.type = "button"; revoke.className = "policy-revoke"; revoke.textContent = "撤销";
    revoke.title = "撤销后不再生效，历史记录仍会保留";
    revoke.setAttribute("aria-label", `撤销规则：${r.text}`);
    revoke.addEventListener("click",()=> { if (policyCurrent(view)) void savePolicyChange(view,{...policyChange(r),status:"revoked",source_text:`撤销规则 ${r.id} v${r.version}：${r.text}`}); }); actions.append(revoke);
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
      const actions={"object.comment":"发表评论","discussion.reply":"回复讨论","message.reply":"回复消息","message.send":"发送消息"}, results={succeeded:"已成功",not_applied:"未执行",unknown:"结果待核实"};
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
  renderPolicyEmptyHint();
  $("policy-status").textContent=`${view.data.effective.length} 条有效`;
}
function renderPolicyEmptyHint() {
  $("policy-empty").hidden = !policyView?.data || !policyCurrent(policyView) || policyView.data.current.length > 0 || !!policyEditor;
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
  const view={member:{agent_id:member.agent_id,membership_id:member.membership_id},info:{...state.info},objectRef:keepOpen ? policyView.objectRef : "",followupRequest:0,followupsLoaded:false,loading:true};
  $("policy-followups").open=!!keepOpen;
  if(!keepOpen)$("followups-history").checked=false;
  policyView=view; policyEditor=null; for(const id of ["policy-save","policy-suggest","policy-new","policy-retry","policy-next"]) $(id).disabled=false; $("policy-form").hidden=true; $("policy-entries").replaceChildren(); $("policy-matters").replaceChildren(); $("policy-next").hidden=true; $("policy-status").textContent="正在读取…";
  $("policy-retry").hidden=true;$("policy-retry").disabled=true;$("policy-new").disabled=true;
  renderPolicyEmptyHint();
  $("followups-status").textContent="";
  try {
    const data=await rpc("/v1/policy/read",view.member,view.info);
    if (!policyCurrent(view)) return;
    view.data=data;renderPolicy(view);
    if($("policy-followups").open && !view.followupsLoaded && !view.followupsLoading)await loadPolicyFollowups(view);
  } catch(error) { if(policyCurrent(view)){view.readFailed=true;$("policy-retry").hidden=false;$("policy-status").textContent=`读取失败：${error.message}`;} }
  finally {view.loading=false;if(policyCurrent(view)){$("policy-retry").disabled=false;$("policy-new").disabled=!view.data;}}
}
function canRefreshPolicy(view) {
  return view && policyCurrent(view) && !view.loading && !view.saving && !view.needsRefresh && !policyEditor && !document.hidden && document.hasFocus() && !$("notification-detail").open && !document.querySelector("dialog:modal");
}
async function refreshMemberPolicy() {
  const view=policyView;if(!canRefreshPolicy(view) || view.refreshing)return;
  view.refreshing=true;const editVersion=view.editVersion || 0;
  try {
    const data=await rpc("/v1/policy/read",view.member,view.info);
    if(!canRefreshPolicy(view) || editVersion!==(view.editVersion || 0))return;
    const changed=JSON.stringify(view.data)!==JSON.stringify(data);
    const scrollTop=$("policy-member-view").scrollTop;
    const openHistory=new Set([...$("policy-entries").children].filter(row=>row.children[3]?.open).map(row=>row.dataset.policyId));
    view.data=data;view.readFailed=false;$("policy-retry").hidden=true;$("policy-new").disabled=false;
    if(changed){renderPolicy(view);for(const row of $("policy-entries").children)if(openHistory.has(row.dataset.policyId))row.children[3].open=true;$("policy-member-view").scrollTop=scrollTop;}
    else $("policy-status").textContent=`${data.effective.length} 条有效`;
  } catch(error) {
    if(canRefreshPolicy(view) && editVersion===(view.editVersion || 0)){view.readFailed=true;$("policy-retry").hidden=false;$("policy-status").textContent=`读取失败：${error.message}`;}
  } finally {view.refreshing=false;}
}
async function savePolicyChange(view,change) {
  if(!policyCurrent(view) || view.saving || view.needsRefresh || !view.data) return;
  view.saving=true; for(const id of ["policy-save","policy-suggest","policy-new","policy-retry","policy-next"]) $(id).disabled=true;
  $("policy-retry").hidden=true;$("policy-status").textContent="正在保存…";
  try {
    await rpc("/v1/policy/update",{...view.member,policy:change},view.info);
    if(!policyCurrent(view)) return;
    view.saving=false; await loadMemberPolicy();
  } catch(error) { view.needsRefresh=true; if(policyCurrent(view)){$("policy-retry").hidden=false;$("policy-status").textContent=`保存失败或结果待核实：${error.message}。请重试读取，核对当前版本后再修改。`;} }
  finally { view.saving=false; if(policyView===view) for(const id of ["policy-save","policy-suggest","policy-new","policy-retry","policy-next"]) $(id).disabled=(policyView===view && view.needsRefresh && ["policy-save","policy-suggest"].includes(id)) || false; }
}
async function savePolicyForm(status) {
  const editor=policyEditor; if(!editor || !policyCurrent(editor.view)) return;
  const c={id:editor.id,version:editor.record?.version || 0,revision:editor.view.data.revision,status,text:$("policy-text").value};
  try { const expires=readTimeInput("policy-expires"); if(expires)c.expires_at=expires; }
  catch(error) { $("policy-status").textContent=error.message; return; }
  c.source_text=`成员设置：${status === "active" ? "保存并生效" : "保存为建议"} · 规则\n${c.text}` + (c.expires_at ? `\n有效期至：${c.expires_at}` : "");
  await savePolicyChange(editor.view,c);
}
$("policy-new").addEventListener("click",()=>editPolicy());
$("policy-cancel").addEventListener("click",()=>{$("policy-form").hidden=true;policyEditor=null;renderPolicyEmptyHint();void refreshMemberPolicy();});
$("policy-retry").addEventListener("click",()=>loadMemberPolicy());
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
    return text;
  };
  const id = (label, value) => add(label, value?.value, value?.namespace);
  const date = value => value && new Date(value).getTime() > 0 ? formatDate(value) : "";
  add("实例",item.instance); id("来源租户",item.tenant); id("接收账号租户",item.receiver?.tenant);
  add("项目",item.project); add("项目 ID",item.project_id && String(item.project_id));
  add("会话",item.conversation?.name); id("会话 ID",item.conversation?.id);
  if (item.sender) add("发送者",notificationParticipant(item.sender));
  if (entry.origin?.kind === "agent") add("代发",`Agent ${entry.origin.agent_id} · ${entry.origin.status}`);
  if (entry.origin?.uncertain_send) add("发送状态","该会话有结果未确定的代发动作；仅作进展上下文");
  if (item.context_only) add("用途","进展上下文，不触发新的自动回复或待办");
  if (entry.automatic_admission) {
    const admission = entry.automatic_admission;
    const reasons = {self_excluded:"本人自聊历史，已排除；需要办理时请在 Room 提出",relationship_pending:"会话关系待核验，暂不作为 Agent 输入"};
    add("自动处理",reasons[admission.reason_code] || (!admission.verified ? "来源证据待核验，暂不作为 Agent 输入" : admission.decision === "excluded" ? "已排除自动处理，历史记录保留" : "可自动处理"));
  }
  id("发送者 ID",item.sender?.id); id("接收账号 ID",item.receiver?.id);
  for (const [label,value] of [["消息时间",item.created_at],["接收时间",entry.received_at]]) {
    const element=add(label,date(value));
    if(element)bindSystemTime(element,()=>{element.textContent=date(value);});
  }
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
  if(entry.notification_source){
    const settings=document.createElement("button");settings.type="button";settings.className="text-button";settings.textContent="查看通知来源设置";
    settings.addEventListener("click",()=>openNotificationMessage(settings,entry.notification_source));details.append(settings);
  }
  details.append(summary,metadata); return details;
}
function inboxCurrent(view) {
  return view && inboxView === view && state.info?.room_id === view.info.room_id && state.info?.room_instance === view.info.room_instance && state.info?.service_epoch === view.info.service_epoch && selectedAgent()?.membership_id === view.member.membership_id && agentTab === "inbox" && $("activity-dialog").open;
}
function renderInboxPagination(view) {
  const entries = view.page?.entries || [];
  const label = view.page ? entries.length ? `第 ${view.pageIndex + 1} 页` : "暂无通知" : "—";
  for (const suffix of ["", "-bottom"]) {
    $("inbox-prev" + suffix).disabled = view.loading || !view.page || view.pageIndex === 0;
    $("inbox-next" + suffix).disabled = view.loading || !view.page?.has_more;
    $("inbox-page" + suffix).textContent = label;
    $("inbox-page" + suffix).title = entries.length ? `消息 #${entries[0].seq}–#${entries.at(-1).seq}` : "";
  }
  $("inbox-reload").disabled = view.loading;
  $("inbox-pagination-bottom").hidden = !entries.length;
  $("inbox-entries").setAttribute("aria-busy", String(view.loading));
}
async function loadMemberInbox(action = "reset") {
  const member = selectedAgent();
  if (!member || agentTab !== "inbox") return;
  const prior = action === "reset" ? null : inboxCurrent(inboxView) ? inboxView : null;
  if (action !== "reset" && (!prior?.page || prior.loading || action === "prev" && prior.pageIndex === 0 || action === "next" && !prior.page.has_more)) return;
  const target = prior ? prior.pageIndex + (action === "prev" ? -1 : 1) : 0;
  const cursors = prior ? [...prior.cursors] : [0];
  const after = cursors[target];
  const snapshot = prior?.page?.snapshot_seq;
  const view = { member, info: state.info, cursors, pageIndex: prior?.pageIndex || 0, page: prior?.page, loading: true };
  inboxView = view;
  $("inbox-status").textContent = "正在读取…";
  $("inbox-status").title = "只读预览，不会推进成员读取进度，也不会启动自动处理。";
  if (!prior) $("inbox-entries").replaceChildren();
  renderInboxPagination(view);
  try {
    const data = await rpc("/v1/member/inbox", {agent_id:member.agent_id, membership_id:member.membership_id, after, ...(snapshot === undefined ? {} : {snapshot_seq:snapshot}), limit:25}, view.info);
    if (!inboxCurrent(view)) return;
    view.page = data;
    view.pageIndex = target;
    if (data.has_more) view.cursors[target + 1] = data.next_after;
    $("inbox-status").textContent = `共 ${data.snapshot_seq} 条` + (data.status.write_error ? ` · ${data.status.write_error}` : "");
    $("inbox-status").title += `\n已接收 ${data.status.latest_seq} 条 · 占用 ${data.status.bytes} / ${data.status.limits.bytes} 字节 · 当前快照 ${data.snapshot_seq}`;
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
        bindSystemTime(time, () => {
          time.textContent = formatDate(date, {month:"numeric",day:"numeric",hour:"2-digit",minute:"2-digit"});
          time.title = `${created.getTime() > 0 ? "消息时间" : "接收时间"}：${formatDate(date)}`;
        });
      } else time.textContent = "时间未获取";
      heading.append(seq,channel,time); section.append(heading);
      const lark = source.provider === "lark", context = item.conversation || item.context;
      const identity = document.createElement("div"); identity.className = "inbox-message-heading";
      if (item.sender) {
        const sender = document.createElement("h3"); sender.className = "inbox-sender"; sender.textContent = notificationParticipant(item.sender) + (entry.origin?.kind === "agent" ? " · Agent 代发" : "") + (item.action ? ` · ${item.action}` : "");
        (lark ? identity : section).append(sender);
      }
      let chat;
      if (lark ? ["group","topic"].includes(item.conversation?.type) : context) {
        chat = document.createElement(lark ? "span" : "p"); chat.className = "inbox-chat";
        const label = context?.name || "会话名称未获取";
        if(lark)chat.setAttribute("aria-label",`群聊：${label}`);
        let url;
        try { url = new URL(context?.url); } catch { /* A source may have no original link. */ }
        if (url && ["https:","http:"].includes(url.protocol) && !url.username && !url.password) {
          const link = document.createElement("a"); link.textContent = label; link.href = url.href;
          link.target = "_blank"; link.rel = "noopener noreferrer"; chat.append(link);
        } else chat.textContent = label;
        if(lark)identity.append(chat);
      }
      if(lark && identity.children.length)section.append(identity);
      if (item.sender && item.changed) {
        const change = document.createElement("p"); change.className = "inbox-change quiet";
        change.textContent = "消息已变更 · 关联原消息，不自动重做已完成动作"; section.append(change);
      }
      const body = document.createElement("div"); body.className = "message-text";
      const options = notificationOptions(view.info, state.operator, source, { seq: entry.seq, agent_id: member.agent_id, membership_id: member.membership_id });
      renderMessageBody(body, item.text, state.operator, lark ? {...options, compactLarkMentions:true} : options);
      section.append(body);
      if(!lark && chat)section.append(chat);
      if (item.truncated || item.metadata_truncated) {
        const notice = document.createElement("p"); notice.className = "inbox-change quiet";
        notice.textContent = item.truncated ? "正文已截断，内容可能不完整" : "部分名称、提及或附件信息已截断"; section.append(notice);
      }
      section.append(inboxSourceDetails(entry));
      return section;
    }));
    $("inbox-member-view").scrollTop = 0;
    if (prior) $("inbox-page").focus();
  } catch(error) { if (inboxCurrent(view)) $("inbox-status").textContent = `读取失败：${error.message}` + (prior ? "。当前页已保留，可重试翻页。" : "。请点击刷新重试。"); }
  finally { if (inboxCurrent(view)) { view.loading = false; renderInboxPagination(view); renderNotificationPlatforms(); } }
}
$("inbox-reload").addEventListener("click", () => loadMemberInbox());
for (const suffix of ["", "-bottom"]) {
  $("inbox-prev" + suffix).addEventListener("click", () => loadMemberInbox("prev"));
  $("inbox-next" + suffix).addEventListener("click", () => loadMemberInbox("next"));
}
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
    button.disabled = memberBusy.has(member.membership_id) || !state.connected || discussionBusy() || member.status === "checkpoint_failed" || member.status === "rotating" && action !== "pause";
    button.addEventListener("click", () => controlMember(member, action, cancel)); actions.append(button);
  };
  add(enable ? "启用协作" : "暂停", enable ? "run" : "pause", false);
  if (execution.active) add("取消回合", "pause", true);
}

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
    button.addEventListener("click", () => openMemberActivity(member, button, "activity"));
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
      (execution?.session_turns ? `\n${execution.session_turns.baseline_known ? `本会话 ${execution.session_turns.used} / ${execution.session_turns.limit}` : "待接入轮换"} · 累计 ${execution.session_turns.total}` : budget ? `\n回合 ${budget.used} / ${budget.limit}` : "");
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
  notice("notification-next-step", "");
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

function roomListDate(room) {
  return formatDate(roomListTime(room), {month:"2-digit",day:"2-digit",hour:"2-digit",minute:"2-digit"});
}
function renderRooms() {
  document.querySelector(".workspace").classList.toggle("managed", state.managed);
  renderPanels();
  $("open-general-settings").hidden = state.managed;
  $("open-general-settings").disabled = !state.connected;
  $("open-settings").disabled = state.managed ? !state.catalogConnected : !state.connected;
  $("toggle-add").hidden = state.managed;
  if (state.managed) $("add-form").hidden = true;
  if (!state.managed || !state.catalog) return;
  const catalog = state.catalog;
  $("rooms-empty").hidden = catalog.rooms.length > 0;
  $("new-room").disabled = !state.catalogConnected || discussionBusy() || !!state.management || state.managementSending || !!catalog.write_error;
  const signature = JSON.stringify({ rooms: catalog.rooms, selected: selectedRoom, timeZone:systemTime.zone, timeError:systemTime.error,
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
        : `${room.runtime_status === "ready" ? "运行中 · " : "未打开 · "}${roomListDate(room)}`;
      name.title = room.name; updated.title = updated.textContent;
      button.title = `${room.name}\n${updated.textContent}`;
      button.append(name, updated);
      button.disabled = room.status !== "ready" || !state.catalogConnected || !!catalog.busy;
      button.addEventListener("click", () => startSelection(room));
      const remove = document.createElement("button"); remove.type = "button"; remove.className = "room-delete";
      const icon = document.createElementNS("http://www.w3.org/2000/svg", "svg");
      icon.setAttribute("viewBox", "0 0 24 24"); icon.setAttribute("aria-hidden", "true");
      const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
      path.setAttribute("d", "M3 6h18M9 6V4h6v2M5 6l1 14h12l1-14M10 10v6m4-6v6");
      icon.append(path); remove.append(icon);
      remove.title = `${room.status === "deleting" ? "重试删除" : "删除"}房间「${room.name}」`;
      remove.setAttribute("aria-label", remove.title);
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
  renderSettingsNavigation();
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
    applyTimeZone(config);
    if (state.managed) {
      const catalog = await request("/v1/rooms");
      if (!currentView(version)) return;
      state.catalog = catalog; state.catalogConnected = true;
      applyTimeZone(catalog);
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
    renderMembers(); void activity.refresh();void refreshMemberPolicy();
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
  const session = execution?.session_turns;
  $("activity-budget").textContent = session ? `${session.baseline_known ? `${session.used} / ${session.limit}` : "待接入轮换"} · 累计 ${session.total}` : budget ? `${budget.used} / ${budget.limit}` : "暂不可用";
  const rotation = execution?.rotation;
  const rotationReasons = { session_max_turns: "达到会话回合阈值", manual: "手动轮换" };
  notice("activity-rotation", rotation ? `${rotationReasons[rotation.reason] || rotation.reason} · ${rotation.phase}${rotation.error ? ` · ${rotation.error}` : ""}` : "");
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
  inboxView = null; $("inbox-entries").replaceChildren();
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

$("new-room").addEventListener("click", () => {
  if ($("new-room").disabled) return;
  notice("create-error", ""); $("new-room-name").value = "";
  renderCreateSelection(); $("create-dialog").showModal(); $("team").focus();
});
$("close-create").addEventListener("click", () => $("create-dialog").close());
dismissOnBackdrop($("create-dialog"), () => $("create-dialog").close());
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
// Application secrets live only in memory and are never retried automatically.
const platforms = { items: [], selected: null, sending: false, generation: 0, formVersion: 0, discovery: null, detecting: false, auth: null, authTimer: null, larkPending:null, larkStarting:null, gitlabAuth: null, gitlabTimer: null, gitlabPending: null, gitlabSetup:null, gitlabSetupVersion:0, gitlabEditingImportID:null, scionDiscovery:null, scionAuth:null, scionTimer:null, scionPending:null, scionLogout:null };
// Authentication and settings use the same durable P5 operation. Only its ID
// and non-secret prepare request are kept in this tab, never OAuth URLs/tokens.
const onboarding = {active:false, operation:null, pending:null, members:[], fixed:null, busy:false, error:"", notices:new Set(),owner:null};
function onboardingStorageKey() { return `teamx.platform-onboarding:${state.operator}`; }
function onboardingDone(op = onboarding.operation) {
  return !!op && (op.cancelled || op.connection.status === "connected" && [op.notifications,op.tool].every(s => ["applied","preserved","skipped"].includes(s.status)));
}
function saveOnboarding() {
  try { if (onboarding.pending) storage?.setItem(onboardingStorageKey(),JSON.stringify(onboarding.pending)); else storage?.removeItem(onboardingStorageKey()); } catch { /* The current tab still retains the operation. */ }
}
function acceptOnboarding(op) {
  onboarding.owner=state.operator;onboarding.operation=op; onboarding.error="";
  onboarding.pending={operation_id:op.operation_id}; saveOnboarding(); renderOnboarding();
}
function onboardingIdentity() { return {epoch:state.catalog?.service_epoch,operator:state.operator}; }
function onboardingCurrent(version, identity) { return version===platforms.formVersion && identity.epoch===state.catalog?.service_epoch && identity.operator===state.operator; }
function onboardingRoom() { return state.catalog?.rooms.find(r=>r.room_instance===$("onboarding-room").value); }
function onboardingMember() { return onboarding.members.find(m=>m.membership_id===$("onboarding-member").value); }
function onboardingProvider() { return connectionIsLark() ? "lark" : connectionIsScion() ? "scion" : "gitlab"; }
function onboardingOrigin() {
  if(connectionIsLark())return connectionOrigin();
  const value=$("connection-url").value.trim();
  try {
    const url=new URL(value);
    if(url.protocol!=="https:" || url.username || url.password || url.pathname!=="/" || value.includes("?") || value.includes("#") || /[\s\\]/.test(value))throw Error("invalid origin");
    return value.replace(url.host,url.host.toLowerCase()).replace(/\/$/,"");
  } catch {throw Error("请输入不含账号、密码、路径或查询参数的 HTTPS 实例地址。");}
}
function renderOnboarding() {
  const active=onboarding.active, fixed=!!onboarding.pending, only=$("onboarding-only").checked, provider=onboardingProvider();
  $("onboarding-options").hidden=!active || !onboarding.fixed || fixed && !!onboarding.operation?.input.only_platform;
  $("onboarding-targets").hidden=only || !!onboarding.fixed?.membership_id || onboarding.members.length===1;
  $("onboarding-room").parentElement?.classList.toggle("onboarding-room-fixed", !!onboarding.fixed);
  $("onboarding-member-field").hidden=!!onboarding.fixed?.membership_id || onboarding.members.length===1;
  $("onboarding-only-option").hidden=!onboarding.fixed;
  $("onboarding-enable-options").hidden=only;
  $("onboarding-notification-details").hidden=provider==="scion" || !$("onboarding-notifications").checked;
  $("onboarding-notifications").disabled=provider==="scion" || fixed;
  for (const id of ["onboarding-room","onboarding-member","onboarding-only","onboarding-tool","onboarding-backfill","onboarding-backfill-start"]) $(id).disabled=fixed || !!onboarding.fixed && (id==="onboarding-room" || id==="onboarding-member" && !!onboarding.fixed.membership_id);
  $("onboarding-backfill-custom").hidden=$("onboarding-backfill").value!=="custom";
  $("onboarding-backfill-start-label").textContent=`起始时间（${systemTime.zone || "系统时区异常"}）`;
  const member=onboardingMember(), room=onboardingRoom();
  const content=provider==="gitlab" ? "我的待办（项目互动未开启）" : "全部人员私聊（群聊未开启）";
  $("onboarding-summary").textContent=only ? "仅连接并核验账号；不创建通知或成员工具设置。" : `为「${member?.agent_id || "所选成员"}」接入后，${provider==="scion" ? "此平台没有通知能力，" : $("onboarding-notifications").checked ? `接收${content} → ${room?.name || "所选 Room"} 的成员 Inbox；` : "通知关闭；"}${$("onboarding-tool").checked ? "启用对应平台工具。" : "工具关闭。"}已有设置保留，范围可在通知详情中调整。`;
  if(active && fixed){$("connection-provider").disabled=true;$("connection-url").readOnly=true;}
  const op=onboarding.operation;
  const waitingForAccount=active && op?.connection.status==="waiting" && !op.cancelled && !onboarding.error && [op.notifications,op.tool].every(step=>["pending","skipped"].includes(step.status));
  $("onboarding-result").hidden=(!onboarding.pending && !op) || waitingForAccount;
  $("onboarding-result-target").textContent=op ? op.input.only_platform ? "仅添加平台" : `${state.catalog?.rooms.find(r=>r.room_instance===op.input.room_instance)?.name || op.input.room_id} · ${op.input.agent_id}` : "正在恢复上次接入记录…";
  const item=platforms.items.find(p=>p.id===op?.platform_id);
  $("onboarding-result-account").textContent=op ? `${item?.name || "平台"}${item?.validation?.username ? ` · @${item.validation.username}` : ""}` : "";
  const status={waiting:"等待账号核验",connected:"账号已连接",pending:"等待应用",applied:"已保存",preserved:"保留原设置",skipped:"本次不修改",failed:"未完成"};
  const errors={account_unavailable:"请先完成登录或核验账号，再读取进度。",account_changed:"账号已变化，请停止本次接入并核对平台。",target_changed:"原 Room 或成员已变化，请重新选择目标发起接入。",settings_changed:"设置已被修改；请核对当前选择，再发起新的接入。",executable_unavailable:"程序缺失，启用意图已保存；安装后会重新检查。",legacy_tool_choice_unknown:"原工具选择未确认，已保留。请在成员工具设置中明确启用。",cancelled:"剩余步骤已停止，已保存的设置保留。",busy:"正在处理，请稍后读取进度。",binding_changed:"绑定已变化，请核对当前平台与成员。"};
  $("onboarding-steps").replaceChildren(...(op ? [["账号",op.connection],["通知",op.notifications],["工具",op.tool]].map(([title,step])=>{
    const row=document.createElement("div"),dt=document.createElement("dt"),dd=document.createElement("dd");dt.textContent=title;
    dd.textContent=`${status[step.status] || step.status}${step.error_code ? ` · ${errors[step.error_code] || "暂不可用，请核对平台、权限及当前设置后重试。"}` : ""}`;row.append(dt,dd);return row;
  }) : []));
  notice("onboarding-result-error",onboarding.error);
  const done=onboardingDone(op);
  $("onboarding-use-room").hidden=!done || !op?.input.only_platform || !workspaceView || !["gitlab","lark"].includes(op.input.provider);
  $("onboarding-prepare-retry").hidden=!onboarding.pending?.prepare || !!op;
  $("onboarding-continue-login").hidden=!op || done || op.connection.status==="connected";
  $("onboarding-apply").hidden=!op || done || op.connection.status!=="connected";
  $("onboarding-cancel").hidden=!onboarding.pending || done;
  $("onboarding-dismiss").hidden=!done;
  $("onboarding-notification-settings").hidden=!op || op.connection.status!=="connected" || op.input.only_platform || op.schema!==2 || !["gitlab","lark"].includes(op.input.provider);
  for (const id of ["onboarding-prepare-retry","onboarding-resume","onboarding-continue-login","onboarding-apply","onboarding-cancel","onboarding-dismiss","onboarding-notification-settings"]) $(id).disabled=onboarding.busy || platforms.sending || !!platforms.gitlabAuth || !!platforms.auth || !!platforms.scionAuth;
}
async function loadOnboardingMembers(preferred = "") {
  const version=platforms.formVersion, identity=onboardingIdentity(), room=onboardingRoom();
  onboarding.members=[];setSelectOptions("onboarding-member",[new Option("正在读取成员…","")],"");
  notice("onboarding-target-error","");$("onboarding-target-retry").hidden=true;
  if (!room) { setSelectOptions("onboarding-member",[new Option("先选择 Room","")],"");renderOnboarding();return; }
  try {
    const q=new URLSearchParams({service_epoch:identity.epoch,caller_id:identity.operator,room_id:room.room_id,room_instance:room.room_instance});
    const data=await request(`/v1/platforms/onboarding/targets?${q}`);
    if (!onboardingCurrent(version,identity) || room.room_instance!==onboardingRoom()?.room_instance) return;
    onboarding.members=data.members;
    const options=data.members.map(m=>new Option(room.members?.find(r=>r.id===m.agent_id)?.name || m.agent_id,m.membership_id));
    const selected=data.members.some(m=>m.membership_id===preferred) ? preferred : data.members.length===1 && !preferred ? data.members[0].membership_id : "";
    if (!selected) options.unshift(new Option(data.members.length ? "选择一次使用成员" : "此 Room 没有适用的 Agent",""));
    setSelectOptions("onboarding-member",options,selected);
    if (preferred && !selected) notice("onboarding-target-error","原成员已变化，请重新打开成员详情。");
  } catch { if (!onboardingCurrent(version,identity) || room.room_instance!==onboardingRoom()?.room_instance) return;
    notice("onboarding-target-error","成员读取失败，请重试。也可在详情选择仅添加平台。");$("onboarding-target-retry").hidden=false;
  }
  renderOnboarding();
}
async function startOnboardingOptions(fixed = null) {
  onboarding.active=true;onboarding.fixed=fixed;onboarding.notices=new Set();
  const rooms=(state.catalog?.rooms || []).filter(r=>r.status==="ready" && !r.busy);
  const preferred=fixed?.room_instance || selectedRoom?.room_instance;
  const selected=rooms.find(r=>r.room_instance===preferred)?.room_instance || (!fixed && rooms.length===1 ? rooms[0].room_instance : "");
  const options=rooms.map(r=>new Option(r.name,r.room_instance));if (!selected) options.unshift(new Option("选择使用的 Room",""));
  setSelectOptions("onboarding-room",options,selected);
  $("onboarding-only").checked=!fixed;$("onboarding-details").open=false;
  $("onboarding-notifications").checked=true;$("onboarding-tool").checked=true;$("onboarding-backfill").value="0";fillTimeInput("onboarding-backfill-start","");
  renderOnboarding();
  if (fixed) await loadOnboardingMembers(fixed.membership_id || "");
}
async function prepareOnboarding() {
  if (!onboarding.active) return null;
  if (onboarding.pending) {
    if (onboarding.operation) {if(onboarding.operation.cancelled)throw Error("此接入已停止，请新建接入。");return onboarding.operation;}
    if (!onboarding.pending.prepare) throw Error("请先读取接入进度。");
  }
  const only=$("onboarding-only").checked, member=onboardingMember(), room=onboardingRoom();
  if (!only && (!member || !room || onboarding.fixed && (onboarding.fixed.membership_id && member.membership_id!==onboarding.fixed.membership_id || room.room_instance!==onboarding.fixed.room_instance))) {
    notice("onboarding-target-error","请选择有效的 Room 和使用成员，再连接并启用。");throw Error("请选择使用成员。");
  }
  let spec={provider:onboardingProvider(),origin:onboardingOrigin(),...(platforms.selected ? {platform_id:platforms.selected.id} : {}),...(only ? {only_platform:true} : {room_id:room.room_id,room_instance:room.room_instance,agent_id:member.agent_id,membership_id:member.membership_id})};
  if (!only) {
    spec.preserve_existing=true;
    for (const name of ["notifications","tool"]) if (onboarding.notices.has(name) || !$("onboarding-"+name).checked) spec[name]=$("onboarding-"+name).checked;
    if (onboarding.notices.has("backfill") && spec.provider!=="scion") {
      const mode=$("onboarding-backfill").value;
      const backfill={backfill_seconds:mode==="custom" ? 0 : Number(mode),...(mode==="custom" ? {backfill_start:readTimeInput("onboarding-backfill-start")} : {})};
      if (mode==="custom" && !backfill.backfill_start) throw Error("请选择有效的开始接收时间。");
      spec.backfill={seconds:backfill.backfill_seconds,...(backfill.backfill_start ? {start:backfill.backfill_start} : {})};
    }
  }
  const pending=onboarding.pending || {operation_id:crypto.randomUUID().replaceAll("-",""),prepare:spec};
  onboarding.owner=state.operator;onboarding.pending=pending;saveOnboarding();renderOnboarding();
  const version=platforms.formVersion,identity=onboardingIdentity();
  let op;
  try {op=await request("/v1/platforms/onboarding/prepare",connectionPayload({operation_id:pending.operation_id,...pending.prepare}));}
  catch(error) {if(onboardingCurrent(version,identity) && ["request","identity"].includes(error.code)) {onboarding.pending=null;saveOnboarding();renderOnboarding();}throw error;}
  if (!onboardingCurrent(version,identity)) throw Error("页面目标已变化，请读取接入进度。");
  acceptOnboarding(op);return op;
}
async function onboardingAction(action, {discardUnconnected = false} = {}) {
  if (!onboarding.pending || onboarding.busy || platforms.sending || platforms.gitlabAuth || platforms.auth || platforms.scionAuth) return;
  const version=platforms.formVersion,identity=onboardingIdentity(),id=onboarding.pending.operation_id;
  onboarding.busy=true;onboarding.error="";renderOnboarding();
  try {
    const op=await request(`/v1/platforms/onboarding/${action}`,connectionPayload({operation_id:id}),undefined,50000);
    if (!onboardingCurrent(version,identity) || id!==onboarding.pending?.operation_id) return;
    acceptOnboarding(op);
    // Leaving an account login also abandons its unused setup record. Read the
    // server receipt first so a late login or partial apply remains recoverable.
    if (discardUnconnected && !op.cancelled && op.connection.status==="waiting" &&
        !platforms.items.some(item=>item.id===op.platform_id) &&
        [op.notifications,op.tool].every(step=>["pending","skipped"].includes(step.status))) {
      const cancelled=await request("/v1/platforms/onboarding/cancel",connectionPayload({operation_id:id}));
      if (!onboardingCurrent(version,identity) || id!==onboarding.pending?.operation_id) return;
      acceptOnboarding(cancelled);
      if (cancelled.cancelled && cancelled.connection.status==="waiting") clearOnboarding();
    }
    if (action==="apply") {await loadPlatforms();if(workspaceView)await syncGitLabNotifications(state.catalog,workspaceView);}
  } catch(error) { if (onboardingCurrent(version,identity)) onboarding.error=error.code==="identity" ? "接入记录或操作人不匹配，请核对当前服务与账号。" : "接入结果未确认；请读取进度后再继续，不会重新登录或重放远端动作。"; }
  finally {onboarding.busy=false;renderOnboarding();}
}
function restoreOnboarding() {
  if(onboarding.owner!==state.operator){onboarding.pending=null;onboarding.operation=null;onboarding.active=false;onboarding.owner=state.operator;}
  if (!onboarding.pending) {try {const p=JSON.parse(storage?.getItem(onboardingStorageKey()));if (/^[a-f0-9]{32}$/.test(p?.operation_id)) onboarding.pending=p;}catch { /* Ignore an invalid tab record. */ }}
  renderOnboarding();if(onboarding.pending) return onboardingAction("status",{discardUnconnected:true});
}
function clearOnboarding() {onboarding.pending=null;onboarding.operation=null;onboarding.active=false;onboarding.error="";saveOnboarding();renderOnboarding();}
async function addPlatformForMember(fixed = null) {
  await openSettings("platforms");
  if (onboarding.pending && !onboardingDone()) {onboarding.error="请先继续上次接入，或停止其剩余步骤后重新添加。";renderOnboarding();return;}
  clearOnboarding();await editPlatform();await startOnboardingOptions(fixed);
}
$("notification-add-platform").addEventListener("click",()=> { setNotificationPicker("member-notification-",false); const member=selectedAgent();if(!member || !workspaceView)return; return addPlatformForMember({...workspaceView.room,agent_id:member.agent_id,membership_id:member.membership_id}); });
$("onboarding-room").addEventListener("change",()=>loadOnboardingMembers());
$("onboarding-target-retry").addEventListener("click",()=>loadOnboardingMembers(onboarding.fixed?.membership_id || ""));
$("onboarding-member").addEventListener("change",renderOnboarding);
for (const name of ["only","notifications","tool","backfill","backfill-start"]) $("onboarding-"+name).addEventListener("change",()=>{onboarding.notices.add(name.startsWith("backfill") ? "backfill" : name);renderOnboarding();if(onboardingProvider()==="gitlab")renderGitLabAccount();else if(connectionIsLark())renderLarkAccount();});
for (const [id,action] of [["onboarding-resume","status"],["onboarding-apply","apply"],["onboarding-cancel","cancel"]]) $(id).addEventListener("click",()=>onboardingAction(action));
$("onboarding-continue-login").addEventListener("click",async()=>{
  const op=onboarding.operation;if(!op || op.cancelled)return;
  const item=platforms.items.find(p=>p.id===op.platform_id);
  await editPlatform(item || null);
  if(onboarding.operation!==op)return;
  if(!item){$("connection-provider").value=op.input.provider==="lark" ? op.input.origin.includes("larksuite") ? "lark" : "feishu" : op.input.provider;$("connection-url").value=op.input.origin;connectionAuthFields();}
  onboarding.active=true;onboarding.fixed=op.input;
  setSelectOptions("onboarding-room",[new Option(state.catalog?.rooms.find(r=>r.room_instance===op.input.room_instance)?.name || op.input.room_id || "仅添加平台",op.input.room_instance || "")],op.input.room_instance || "");
  onboarding.members=op.input.only_platform ? [] : [op.input];setSelectOptions("onboarding-member",[new Option(op.input.agent_id || "",op.input.membership_id || "")],op.input.membership_id || "");
  $("onboarding-only").checked=!!op.input.only_platform;$("onboarding-notifications").checked=op.input.notifications!==false;$("onboarding-tool").checked=op.input.tool!==false;
  renderOnboarding();
  if(connectionIsLark())await detectLarkPlatform();else if(connectionIsScion())await detectScionPlatform();else await discoverGitLabSetup();
});
$("onboarding-prepare-retry").addEventListener("click",async()=>{
  if(onboarding.busy || !onboarding.pending?.prepare)return;
  onboarding.busy=true;onboarding.active=true;renderOnboarding();
  try {await prepareOnboarding();}catch(error){onboarding.error=error.code ? "准备结果未确认，请读取进度后再继续。" : error.message;}
  finally{onboarding.busy=false;renderOnboarding();}
});
$("onboarding-dismiss").addEventListener("click",()=>{clearOnboarding();showPlatformList();});
$("onboarding-use-room").addEventListener("click",async()=>{
  const op=onboarding.operation,ws=workspaceView;
  if(!op || !ws)return;
  const item=platforms.items.find(p=>p.id===op.platform_id);if(!item)return;
  await closeSettings();if(ws!==workspaceView)return;
  $("notification-detail").close();$("activity-dialog").close();
  panels.members=true;savePanels();renderPanels();await loadWorkspacePlatforms();
  if(ws!==workspaceView)return;
  notice("notification-next-step",`选择成员，在顶部平台区添加「${item.name}」。`);
  $("notification-next-step").focus({preventScroll:true});
});
$("onboarding-notification-settings").addEventListener("click",async()=>{
  const op=onboarding.operation,room=state.catalog?.rooms.find(r=>r.room_id===op?.input.room_id && r.room_instance===op?.input.room_instance);
  if(!room || op.schema!==2 || op.input.only_platform){onboarding.error="接入结果缺少有效的成员定位，请检查原 Room 和成员。";renderOnboarding();return;}
  await closeSettings();
  if(state.info?.room_id!==room.room_id || state.info?.room_instance!==room.room_instance){selectRoom(room);await refresh();}
  if(state.info?.room_instance!==room.room_instance)return;
  const member=state.members.find(m=>m.agent_id===op.input.agent_id && m.membership_id===op.input.membership_id);
  if(!member){await openSettings("platforms");onboarding.error="原接收成员已移除或替换，账号连接已保留。";renderOnboarding();return;}
  if(!$("activity-dialog").open || selectedAgent()?.membership_id!==member.membership_id)openMemberActivity(member,$("onboarding-notification-settings"));
  await openNotificationSource($("member-notification-add-toggle"),op.input.provider==="lark" ? "messages" : "gitlab",op.platform_id,memberNotificationTarget(member));
});
function selectMemberPlatformAddition(item) {
  const v=memberToolsView;if(!v || !toolsCurrent(v))return;
  v.addition={item,notifications:["gitlab","lark"].includes(item.provider),tool:!!v.cli?.catalog?.some(d=>d.provider===item.provider)};
  $("member-platform-add-notifications").checked=v.addition.notifications;$("member-platform-add-tool").checked=v.addition.tool;renderMemberPlatformAddition();
}
function renderMemberPlatformAddition() {
  const v=memberToolsView,a=v?.addition,op=onboarding.operation;
  const own=op && op.input.room_instance===workspaceView?.room.room_instance && op.input.agent_id===v?.member.agent_id && op.input.membership_id===v?.member.membership_id;
  $("member-platform-add-selection").hidden=!a;
  if(a){
    $("member-platform-add-title").textContent=a.item.name;
    const notifications=["gitlab","lark"].includes(a.item.provider),tool=!!v.cli?.catalog?.some(d=>d.provider===a.item.provider);
    $("member-platform-add-notifications").disabled=!notifications || onboarding.busy;
    $("member-platform-add-tool").disabled=!tool || onboarding.busy;
    $("member-platform-add-customize").hidden=!notifications || !a.notifications;
    $("member-platform-add-summary").textContent=[notifications ? a.notifications ? a.config ? notificationDescription(a.config) : a.item.provider==="gitlab" ? "我的待办 · 从首次开启时刻接收" : "全部人员私聊 · 从首次开启时刻接收" : "不接收通知" : "无通知能力",a.tool ? "使用平台工具" : "不使用工具","已有设置保留，关闭项需用对应开关开启。"].join("；");
    $("member-platform-add-submit").disabled=!v.cliConnected || !workspaceView?.notificationsConnected || onboarding.busy || !!workspaceView?.memberToolBusy || a.item.validation?.status!=="valid" || !!onboarding.pending && !onboardingDone();
  }
  const status={applied:"已保存",preserved:"保留原设置",skipped:"本次不修改",pending:"等待应用",failed:"未完成"};
  $("member-platform-add-result").textContent=own ? `通知：${status[op.notifications.status]}；工具：${status[op.tool.status]}。${onboarding.error || (op.notifications.status==="failed" || op.tool.status==="failed" ? "已成功部分保留，请读取进度后继续未完成步骤。" : "")}` : onboarding.pending && !onboardingDone() ? `有未完成的接入，请读取进度后继续。${onboarding.error}` : onboarding.error;
  $("member-platform-add-resume").hidden=!onboarding.pending || onboardingDone();
  const canApply=own && (op.connection.status==="connected" || op.connection.status==="waiting" && memberPlatformItems().some(p=>p.id===op.platform_id && p.validation?.status==="valid"));
  $("member-platform-add-retry").hidden=!onboarding.pending || onboardingDone() || !!op && !canApply;
  $("member-platform-add-resume").disabled=$("member-platform-add-retry").disabled=onboarding.busy;
}
async function runMemberOnboarding(v,spec) {
  if(!toolsCurrent(v) || !v.cliConnected || workspaceView?.memberToolBusy || onboarding.busy)return;
  if(onboarding.pending && !onboardingDone()){onboarding.error="请先读取并完成上次接入，或在全局设置停止剩余步骤。";renderMemberPlatformAddition();return;}
  clearOnboarding();
  const identity=onboardingIdentity(),ws=workspaceView;
  onboarding.pending={operation_id:crypto.randomUUID().replaceAll("-",""),prepare:spec};onboarding.owner=state.operator;saveOnboarding();onboarding.busy=true;renderMemberPlatforms();
  const id=onboarding.pending.operation_id;let preparing=true;
  const current=()=>identity.epoch===state.catalog?.service_epoch && identity.operator===state.operator && onboarding.pending?.operation_id===id;
  try {
    let op=await request("/v1/platforms/onboarding/prepare",{service_epoch:identity.epoch,caller:{kind:"human",id:identity.operator},operation_id:id,...spec});
    if(!current())return;acceptOnboarding(op);preparing=false;
    op=await request("/v1/platforms/onboarding/apply",{service_epoch:identity.epoch,caller:{kind:"human",id:identity.operator},operation_id:id},undefined,50000);
    if(!current())return;acceptOnboarding(op);
    if(op.notifications.status==="applied"){
      const key=notificationScopeKey({mode:"inbox",agent_id:spec.agent_id,membership_id:spec.membership_id},spec.platform_id);
      ws.notificationDrafts?.delete(key);
      if(ws.notificationScope===key){ws.gitlabDirty=false;ws.larkDirty=false;ws.larkFilled=null;}
    }
    if(onboardingDone(op) && toolsCurrent(v))v.addition=null;
  }catch(error){if(current()){
    if(preparing && ["request","identity"].includes(error.code)){onboarding.pending=null;saveOnboarding();onboarding.error="接入未准备，请核对成员、平台与通知范围后重试。";}
    else onboarding.error="接入结果未确认，请读取进度；不会自动重发。";
  }}
  finally{
    onboarding.busy=false;
    if(ws===workspaceView && identity.epoch===state.catalog?.service_epoch){await syncGitLabNotifications(state.catalog,ws);if(memberToolsView && toolsCurrent(memberToolsView))await loadCLITools(memberToolsView);renderGitLabEvents();}
  }
}
async function initializeMemberNotifications(item,target,enabled=true) {
  const v=memberToolsView;
  if(!v || !toolsCurrent(v) || !sameNotificationTarget(memberNotificationTarget(v.member),target))return;
  return runMemberOnboarding(v,{...workspaceView.room,agent_id:target.agent_id,membership_id:target.membership_id,platform_id:item.id,provider:item.provider,origin:item.url,notifications:enabled,skip_tool:true});
}
function captureMemberPlatformDraft(config) {
  const ws=workspaceView,a=ws?.notificationAddition,v=memberToolsView;
  if(!a || a.key!==ws.notificationScope || a.v!==v || !toolsCurrent(v) || !v.addition || v.addition.item.id!==config.platform_id)return false;
  v.addition.config=structuredClone(config);ws.gitlabDirty=false;ws.larkDirty=false;ws.notificationDrafts?.delete(a.key);ws.notificationAddition=null;$("notification-detail").close();renderMemberPlatformAddition();return true;
}
for(const [id,key] of [["member-platform-add-notifications","notifications"],["member-platform-add-tool","tool"]])$(id).addEventListener("change",()=>{if(memberToolsView?.addition)memberToolsView.addition[key]=$(id).checked;renderMemberPlatformAddition();});
$("member-platform-add-submit").addEventListener("click",()=>{
  const v=memberToolsView,a=v?.addition;if(!a || $("member-platform-add-submit").disabled)return;
  const spec={...workspaceView.room,agent_id:v.member.agent_id,membership_id:v.member.membership_id,platform_id:a.item.id,provider:a.item.provider,origin:a.item.url,preserve_existing:true,notifications:a.notifications,tool:a.tool};
  if(a.config && a.notifications)spec[a.item.provider]=a.config;
  return runMemberOnboarding(v,spec);
});
$("member-platform-add-customize").addEventListener("click",async()=>{
  const ws=workspaceView,v=memberToolsView,a=v?.addition;if(!a || !toolsCurrent(v))return;
  await openMemberPlatform($("member-platform-add-customize"),a.item,memberNotificationTarget(v.member));
  if(ws!==workspaceView || !toolsCurrent(v) || a!==v.addition)return;
  ws.notificationAddition={key:ws.notificationScope,v};
  if(a.config){if(a.item.provider==="gitlab"){ws.gitlabDraft=structuredClone(a.config);ws.gitlabDirty=true;fillBackfill("gitlab",a.config);}else{ws.larkRestore=structuredClone(a.config);ws.larkDirty=true;}}
  renderGitLabEvents();
});
$("member-platform-add-resume").addEventListener("click",async()=>{await onboardingAction("status");renderMemberPlatformAddition();});
$("member-platform-add-retry").addEventListener("click",async()=>{
  if(!onboarding.operation && onboarding.pending?.prepare){
    if(onboarding.busy)return;
    const pending=onboarding.pending,identity=onboardingIdentity();onboarding.busy=true;renderMemberPlatformAddition();
    const current=()=>onboarding.pending===pending && state.catalog?.service_epoch===identity.epoch && state.operator===identity.operator;
    try{const op=await request("/v1/platforms/onboarding/prepare",connectionPayload({...pending.prepare,operation_id:pending.operation_id}));if(current())acceptOnboarding(op);}catch{if(current())onboarding.error="准备结果未确认，请读取进度。";}finally{onboarding.busy=false;renderMemberPlatformAddition();}return;
  }
  await onboardingAction("apply");if(memberToolsView && toolsCurrent(memberToolsView))await loadCLITools(memberToolsView);if(workspaceView)await loadWorkspacePlatforms();renderMemberPlatformAddition();
});
$("inbox-add-platform").addEventListener("click",async()=>{
  const ws=workspaceView,target=memberNotificationTarget();if(!ws || !target)return;
  await selectAgentTab("platforms");
  if(ws!==workspaceView || !sameNotificationTarget(target,memberNotificationTarget()))return;
  renderMemberPlatforms();setNotificationPicker("member-notification-",true);
});
const gitlabScopeOptions = [
  {id:"gitlab-scope-api",scope:"api",required:true,defaultSelected:true},
  {id:"gitlab-scope-write-repository",scope:"write_repository",required:false,defaultSelected:true},
];
let gitlabScopeVersion = 0;
let gitlabEditingApplication = false;
const connectionValidationErrors = { login_failed:"PAT 登录失败，请核对实例、令牌及权限。", timeout:"操作超时，请刷新账号核对结果；不会自动重新登录。", cancelled:"操作已取消，请刷新账号核对状态。", credential_unavailable: "登录凭据不可用，请核对账号授权或服务凭据配置。", credential_rejected: "认证失败，请核对当前账号登录和权限。", identity_mismatch: "profile 账号或应用已变化，请核对后重新验证。", permission_denied: "当前用户缺少读取权限，请检查授权。", cli_version_mismatch: "CLI 版本不匹配，请使用对应工具已验收的固定版本。", cli_unavailable: "CLI 无法执行，请核对路径与后台运行权限。", query_failed: "连接失败，请检查地址或网络后重试验证。", invalid_response: "实例返回了无效的身份信息。" };
function connectionFeedback(message = "", kind = "status", detail = "") {
  renderSettingsNavigation();
  const feedback = $("platforms-feedback");
  if (typeof message === "function") bindSystemTime(feedback, () => { feedback.textContent = message(); });
  else { feedback.refreshSystemTime = null; feedback.textContent = message; }
  feedback.dataset.kind = kind;
  feedback.setAttribute("role", kind === "error" ? "alert" : "status");
  const details = $("platforms-feedback-details"), explanation = $("platforms-feedback-detail");
  details.hidden = !detail; details.open = false;
  if (typeof detail === "function") bindSystemTime(explanation, () => { explanation.textContent = detail(); });
  else { explanation.refreshSystemTime = null; explanation.textContent = detail; }
}
const connectionInputs = ["connection-name", "connection-url", "connection-executable", "connection-profile"];
function clearPlatformErrors() {
  for (const id of connectionInputs) $(id).setAttribute("aria-invalid", "false");
}
function connectionInputError(id, message) {
  $(id).setAttribute("aria-invalid", "true");
  connectionFeedback(message, "error");
  $(id).focus();
}
function connectionAuthFields() {
  const isLark = connectionIsLark(), isScion = connectionIsScion();
  for (const id of ["connection-executable", "connection-profile"]) $(id).setAttribute("aria-required", String(isLark));
  clearPlatformErrors();
  connectionFeedback();
  $("connection-validation").hidden=false;
  $("connection-notes").hidden = isScion || !platforms.selected;
  if (isLark || isScion) $("connection-save").textContent = "保存连接";
  if (isLark || isScion) {$("connection-save").hidden=false;$("connection-actions").hidden=false;}
  $("connection-scion-row").hidden = !isScion;
  $("connection-profile-row").hidden = !isLark;
  $("connection-url-row").hidden = isLark;
  $("connection-url").required = !isLark;
  $("gitlab-account-row").hidden = isLark || isScion;
  $("gitlab-app-config-row").hidden = isLark || isScion;
  $("connection-name").placeholder = isScion ? "公司 Scion" : isLark ? "我的飞书" : "公司 GitLab";
  $("connection-url").placeholder = isScion ? "https://scion.example.com" : "https://gitlab.example.com";
  renderScionPATLink();
  if (!isScion) $("connection-scion-pat").value = "";
  if (isLark || isScion) $("gitlab-client-secret").value = "";
  else renderGitLabAccount();
}
function connectionIsLark() { return ["feishu", "lark"].includes($("connection-provider").value); }
function connectionIsScion() { return $("connection-provider").value === "scion"; }
function renderScionPATLink() {
  const link = $("connection-scion-pat-link"), origin = $("connection-url").value.trim();
  let href = "";
  try {
    const url = new URL(origin);
    if (connectionIsScion() && url.protocol === "https:" && !url.username && !url.password &&
        url.pathname === "/" && !origin.includes("?") && !origin.includes("#") && !/[\s\\]/.test(origin)) {
      href = new URL("/profile?tab=tokens", url.origin).href;
    }
  } catch { /* Wait for a valid instance address. */ }
  link.href = href;
  link.hidden = !href;
}
function scionProfileDraft() { return {version:1, executable:$("connection-scion-executable").value.trim(), profile:$("connection-scion-profile").value.trim(), brand:"scion", cli_version:"0.1.13"}; }
function renderScionAccount() {
  renderSettingsNavigation();
  $("connection-save").textContent=onboarding.active ? $("onboarding-only").checked ? "连接并核验" : "连接并启用" : "保存连接";
  const active = !!platforms.scionAuth || !!platforms.scionLogout, pending = !!platforms.scionPending;
  for (const id of ["connection-scion-executable", "connection-scion-profile", "connection-scion-detect", "connection-scion-login-open", "connection-scion-login", "connection-scion-login-dismiss", "connection-scion-pat", "connection-scion-logout"]) $(id).disabled = active || pending || platforms.detecting || platforms.sending;
  $("connection-scion-login-open").setAttribute("aria-expanded",String(!$("connection-scion-login-panel").hidden));
  $("connection-scion-login-dismiss").hidden = active;
  $("connection-url").readOnly = !!platforms.selected || active || pending || onboarding.active && !!onboarding.pending;
  $("connection-provider").disabled = !!platforms.selected || active || pending || onboarding.active && !!onboarding.pending;
  $("connection-name").disabled = pending || platforms.sending;
  $("connection-scion-cancel").hidden = !platforms.scionAuth;
  $("connection-save").disabled = active || pending || platforms.detecting || platforms.sending;
  $("connection-verify").disabled = active || pending || platforms.detecting || platforms.sending;
  $("connection-delete").disabled = active || pending || platforms.sending || !!platforms.selected?.references?.length;
  const selected = platforms.scionDiscovery?.profiles.find(p => p.profile === $("connection-scion-profile").value);
  $("connection-scion-login-open").textContent = selected ? "重新登录" : "登录";
  $("connection-scion-logout").hidden = !selected;
  $("connection-scion-account").title = "";
  const status = $("connection-scion-status");
  status.hidden = true; status.textContent = ""; status.title = ""; status.dataset.kind = "status";
  renderPlatformReferences();
  if (active) { $("connection-scion-account").textContent = platforms.scionLogout ? "正在退出登录…" : "正在登录并核验账号…"; return; }
  if (platforms.detecting) { $("connection-scion-account").textContent = "正在检测账号…"; return; }
  $("connection-scion-account").textContent = selected ? `已识别账号 · ${selected.username}` : !$("connection-url").value.trim() ? "填写实例地址后自动检测账号。" : platforms.scionDiscovery ? platforms.scionDiscovery.profiles.length ? "请选择账号，或点击登录。" : "尚未登录，请点击登录。" : "请刷新账号以完成检测。";
  if (!selected) return;
  const item = platforms.selected, validation = item?.validation;
  const same = item?.provider === "scion" && JSON.stringify(scionProfileDraft()) === JSON.stringify(item.cli_profile);
  status.hidden = false;
  if (!item) status.textContent = "保存连接后自动验证账号。";
  else if (!same) status.textContent = "账号配置已更改，保存后自动验证。";
  else if (validation?.status === "valid" && selected.username !== validation.username) {
    status.textContent = "账号信息已变化，请重新验证。"; status.dataset.kind = "error";
  } else if (validation?.status === "valid") {
    $("connection-scion-account").textContent = `已连接 · ${selected.username}`;
    $("connection-scion-account").title = $("connection-validation").title;
    status.hidden = true;
  } else if (validation?.status === "failed") {
    status.textContent = connectionValidationErrors[validation.error_code] || "验证失败，请重新验证连接。"; status.dataset.kind = "error";
  } else status.textContent = "尚未验证连接。";
}
function scionProfileOptions(data, selected) {
  const users = new Map();
  for (const p of data.profiles) users.set(p.username, (users.get(p.username) || 0) + 1);
  const options = data.profiles.map(p => new Option(users.get(p.username) > 1 ? `${p.username} · ${p.profile.slice(-6)}` : p.username, p.profile));
  if (data.profiles.length !== 1 || selected && !data.profiles.some(p => p.profile === selected)) options.unshift(new Option("选择账号", ""));
  setSelectOptions("connection-scion-profile", options, data.profiles.some(p => p.profile === selected) ? selected : !selected && data.profiles.length === 1 ? data.profiles[0].profile : "");
  $("connection-scion-choice").hidden = !data.profiles.length || data.profiles.length === 1 && (!selected || data.profiles.some(p => p.profile === selected));
}
async function detectScionPlatform() {
  if (!connectionIsScion() || platforms.scionAuth) return;
  clearScionConfirmation();
  const version = ++platforms.formVersion, url = $("connection-url").value.trim();
  const selected = $("connection-scion-profile").value;
  platforms.scionDiscovery = null;
  if (!url) { renderScionAccount(); return; }
  platforms.detecting = true; renderScionAccount();
  try {
    const data = await request("/v1/platforms/scion/discover", connectionPayload({url, executable:$("connection-scion-executable").value.trim()}), undefined, 70000);
    if (version !== platforms.formVersion) return;
    platforms.scionDiscovery = {...data, origin:url};
    $("connection-scion-executable").value = data.executable;
    scionProfileOptions(data, selected);
    connectionFeedback(data.unavailable ? "部分账号不可用，请刷新或使用 PAT 登录。" : "");
  } catch (error) {
    if (version !== platforms.formVersion) return;
    if (["cli_unavailable", "cli_version_mismatch"].includes(error.code)) $("connection-scion-advanced").open = true;
    connectionFeedback(connectionValidationErrors[error.code] || (error.code === "request" ? "请填写有效的 HTTPS 实例地址和 CLI 路径。" : "账号检测失败，请核对程序或网络后刷新。"), "error");
  } finally {
    if (version === platforms.formVersion) { platforms.detecting = false; renderScionAccount(); }
  }
}
async function startScionLogin() {
  if (!connectionIsScion() || platforms.scionAuth || platforms.scionPending || platforms.sending || platforms.detecting) return;
  const pat = $("connection-scion-pat").value.trim();
  $("connection-scion-pat").value = "";
  if (!pat || !$("connection-url").value.trim()) { connectionFeedback("请填写实例地址和 PAT。", "error"); return; }
  try {if(onboarding.active)await prepareOnboarding();}
  catch(error){connectionFeedback(error.code ? "接入目标未确认，请读取接入进度。" : error.message,"error");return;}
  const auth = {session_id:crypto.randomUUID().replaceAll("-", ""), version:platforms.formVersion, origin:$("connection-url").value.trim()};
  platforms.scionAuth = auth; renderScionAccount();
  connectionFeedback("正在通过 Scion CLI 登录…");
  try {
    await request("/v1/platforms/scion/login", connectionPayload({session_id:auth.session_id,url:$("connection-url").value.trim(),executable:$("connection-scion-executable").value.trim(),pat}), undefined, 15000);
  } catch (error) {
    if (platforms.scionAuth !== auth) return;
    const rejected = {request:"请填写有效的 HTTPS 实例地址和 PAT。", identity:"服务身份已变化，请刷新页面。", capacity:"登录操作已达上限，请稍后刷新账号。", busy:"服务正忙，请稍后重试登录。"};
    if (rejected[error.code]) { platforms.scionAuth=null; renderScionAccount(); connectionFeedback(rejected[error.code],"error"); return; }
    // A lost start response is recovered by ID through status, never by sending
    // the PAT or login again. The operator still owns cancellation on close.
  }
  if (platforms.scionAuth === auth) await pollScionAuth();
}
async function pollScionAuth() {
  const auth = platforms.scionAuth;
  if (!auth) return;
  try {
    const result = await request("/v1/platforms/scion/status", connectionPayload({session_id:auth.session_id}));
    if (platforms.scionAuth !== auth || platforms.formVersion !== auth.version) return;
    if (result.status === "starting") { platforms.scionTimer = setTimeout(() => { void pollScionAuth(); }, 1000); return; }
    platforms.scionAuth = null; clearTimeout(platforms.scionTimer);
    if (result.status === "succeeded" && result.cli_profile) {
      const profile = result.cli_profile;
      const data = {origin:auth.origin, executable:profile.executable, profiles:(platforms.scionDiscovery?.profiles || []).filter(p => p.profile !== profile.profile), unavailable:0};
      data.profiles.push({profile:profile.profile,username:result.username});
      platforms.scionDiscovery = data;
      $("connection-scion-executable").value = profile.executable;
      scionProfileOptions(data, profile.profile);
      $("connection-scion-login-panel").hidden = true;
      renderScionAccount(); connectionFeedback(`账号已验证，请保存连接。`, "success");
    } else {
      renderScionAccount(); connectionFeedback(result.status === "cancelled" ? "本次登录已取消。" : connectionValidationErrors[result.error_code] || "PAT 登录失败，请检查实例、令牌及权限。", "error");
    }
  } catch {
    if (platforms.scionAuth !== auth) return;
    await cancelScionAuth();
    connectionFeedback("登录结果未确认，请刷新账号核对。", "error");
  }
}
async function cancelScionAuth() {
  const auth = platforms.scionAuth;
  clearScionConfirmation();
  $("connection-scion-pat").value = "";
  clearTimeout(platforms.scionTimer);
  if (!auth) return;
  platforms.scionAuth = null;
  try { await request("/v1/platforms/scion/cancel", connectionPayload({session_id:auth.session_id}), undefined, 12000); }
  catch { connectionFeedback("登录取消结果未确认，请稍后刷新账号核对。", "error"); }
  if (connectionIsScion()) renderScionAccount();
}

function clearScionConfirmation() {
  const pending = platforms.scionPending;
  platforms.scionPending = null;
  if (pending) renderScionAccount();
}
function confirmScionLogout() {
  if (!connectionIsScion() || platforms.scionAuth || platforms.scionPending || platforms.sending || platforms.detecting) return;
  const profile = scionProfileDraft(), discovery = platforms.scionDiscovery;
  const selected = discovery?.profiles.find(p => p.profile === profile.profile);
  if (!selected || discovery.executable !== profile.executable || discovery.origin !== $("connection-url").value.trim()) return;
  const affected = platforms.items.filter(c => c.provider === "scion" && c.cli_profile?.profile === profile.profile);
  platforms.scionPending = {version:platforms.formVersion, profile, discovery, action:"logout",
    title:`退出「${selected.username}」的登录？将停止以上房间中使用此账号的操作。`,
    description:"将清除本地登录凭据，保留连接配置。平台上的 PAT 仍然有效，可在令牌管理页面撤销。", confirmLabel:"确认退出",
    affected:affected.map(c => ({id:c.id, version:c.version, referenced_rooms:(c.references || []).map(({room_id,room_instance}) => ({room_id,room_instance}))}))};
  connectionFeedback(); renderScionAccount(); $("connection-auth-confirm").focus();
}
async function logoutScion() {
  const pending = platforms.scionPending;
  if (!pending || pending.version !== platforms.formVersion || !connectionIsScion() || platforms.scionAuth || platforms.sending || platforms.detecting) return;
  const {version, profile, discovery, affected} = pending;
  platforms.scionLogout = pending;
  clearScionConfirmation();
  platforms.sending = true; $("connection-fields").disabled = true;
  $("connection-back").disabled = true; $("close-settings").disabled = true;
  $("connection-scion-pat").value = "";
  renderScionAccount(); connectionFeedback("正在退出登录…");
  try {
    const result = await request("/v1/platforms/scion/logout", connectionPayload({url:discovery.origin, cli_profile:profile, affected}), undefined, 15000);
    if (version !== platforms.formVersion) return;
    if (result.status !== "succeeded" || !Array.isArray(result.platforms)) throw new Error("unconfirmed logout");
    for (const item of result.platforms) updatePlatformItem(item);
    platforms.scionDiscovery = {...discovery, profiles:discovery.profiles.filter(p => p.profile !== profile.profile)};
    scionProfileOptions(platforms.scionDiscovery, profile.profile);
    $("connection-scion-login-panel").hidden = true;
    connectionFeedback("已退出登录，平台 PAT 仍有效。", "success");
  } catch (error) {
    if (version !== platforms.formVersion) return;
    platforms.scionDiscovery = null;
    connectionFeedback(error.code === "conflict" ? "账号或引用已变化，请返回列表重新加载后重试。" : error.code === "busy" ? "操作尚未结束，请刷新账号后重试。" : "退出结果未确认，请刷新账号核对。", "error");
  } finally {
    platforms.scionLogout = null;
    platforms.sending = false; renderSettingsNavigation(); $("connection-fields").disabled = false;
    $("connection-back").disabled = false; $("close-settings").disabled = false;
    if (version === platforms.formVersion) {
      renderScionAccount();
      if (platforms.scionDiscovery) $("connection-scion-login-open").focus();
    }
  }
}

function renderPlatformReferences() {
  const item = platforms.selected;
  const pending = connectionIsLark() ? platforms.larkPending : connectionIsScion() ? platforms.scionPending : platforms.gitlabPending;
  const active = connectionIsLark() ? platforms.larkStarting || platforms.auth : connectionIsScion() ? platforms.scionLogout : platforms.gitlabAuth;
  const gitlab=!connectionIsLark() && !connectionIsScion();
  const flow = pending || active, affected = flow?.affected || (gitlab ? platforms.gitlabSetup?.affected : undefined);
  const groups = affected ? affected.map(a => {
    const connection = platforms.items.find(c => c.id === a.id);
    return { name: connection?.name || "其他共享连接", references: (a.referenced_rooms || []).map(ref => ({
      ...ref, ...connection?.references?.find(r => r.room_id === ref.room_id && r.room_instance === ref.room_instance),
    })) };
  }) : item ? [{ name: item.name, references: item.references || [] }] : [];
  if (flow && !groups.length) groups.push({name:"",references:[]});
  $("connection-auth-confirmation").hidden = !pending;
  $("connection-auth-confirmation-title").textContent = pending?.title || "";
  $("connection-auth-confirmation-description").textContent = pending?.description || "";
  $("connection-auth-confirmation-description").hidden = !pending?.description;
  $("connection-auth-confirm").textContent = pending?.confirmLabel || "继续登录";
  $("connection-auth-confirm").classList.toggle("settings-danger-button",pending?.action === "logout");
  for (const id of ["connection-auth-confirm","connection-auth-dismiss"]) $(id).disabled = platforms.sending;
  const count = new Set(groups.flatMap(group => group.references.map(ref => JSON.stringify([ref.room_id, ref.room_instance])))).size;
  $("connection-references").hidden = !groups.length;
  $("connection-references").dataset.impact = String(!!flow);
  $("connection-references-title").textContent = `引用房间 · ${count}`;
  $("connection-reference-list").replaceChildren(...groups.map(group => {
    const section = document.createElement("div"); section.className = "connection-reference-group";
    if (groups.length > 1) { const title = document.createElement("h5"); title.textContent = group.name; section.append(title); }
    const list = document.createElement("ul");
    for (const ref of group.references) {
      const row = document.createElement("li"), name = document.createElement("span");
      name.textContent = ref.name || "房间信息待刷新"; row.append(name);
      if (ref.pending) { const status = document.createElement("small"); status.textContent = "待完成绑定"; row.append(status); }
      list.append(row);
    }
    if (!group.references.length) { const empty = document.createElement("li"); empty.className = "quiet"; empty.textContent = "暂无房间引用"; list.append(empty); }
    section.append(list); return section;
  }));
}
function renderGitLabReuse() {
  const prepared=platforms.gitlabSetup, status=prepared?.status, creating=!platforms.selected;
  $("gitlab-reuse").hidden=(!creating && status!=="application_incompatible") || !prepared || connectionIsLark() || connectionIsScion();
  const candidates=prepared?.applications || [], compatible=candidates.filter(c=>c.callback_compatible && !c.missing_scopes?.length);
  const missingScopes=[...new Set(candidates.flatMap(c=>c.missing_scopes || []))];
  const reason=[candidates.some(c=>!c.callback_compatible) ? "回调地址不匹配" : "",missingScopes.length ? `缺少权限 ${missingScopes.join(" + ")}` : ""].filter(Boolean).join("；") || "GitLab 应用配置不兼容";
  $("gitlab-repair-application").hidden=creating || status!=="application_incompatible" || !platforms.selected?.authorization_status || gitlabEditingApplication;
  $("gitlab-repair-application").disabled=platforms.sending || !!platforms.gitlabPending || !!platforms.gitlabAuth;
  $("gitlab-application-choice").hidden=!creating || status!=="application_selection_required";
  setSelectOptions("gitlab-application",[new Option("选择兼容应用",""),...compatible.map(c=>new Option(`Application ID · ${c.client_id}`,c.application_id))],platforms.gitlabApplicationID || "");
  $("gitlab-reuse-summary").textContent=status==="ready" ? "复用此实例的已配置应用，新平台的账号需单独授权。" : status==="application_selection_required" ? "此实例有多个兼容应用，请明确选择一个后登录。" : status==="application_incompatible" ? `${reason}。${creating ? "请先到已有平台的「修改应用配置」中更新。" : "请修改应用配置后重新授权。"}` : "此实例尚无兼容应用，请完成下方应用配置并核验所有者。";
  $("gitlab-compatibility-details").hidden=status!=="application_incompatible" || !candidates.length;
  $("gitlab-application-issues").replaceChildren(...(status==="application_incompatible" ? candidates.map(c=>{
    const li=document.createElement("li");li.textContent=`${candidates.length>1 ? `${c.client_id}：` : ""}${[!c.callback_compatible ? `回调地址不匹配（需要 ${prepared.redirect_uri}）` : "",c.missing_scopes?.length ? `缺少权限 ${c.missing_scopes.join(" + ")}` : ""].filter(Boolean).join("；")}`;return li;
  }) : []));
}
$("gitlab-application").addEventListener("change",async()=>{platforms.gitlabApplicationID=$("gitlab-application").value;await discoverGitLabSetup();});
function renderGitLabAccount() {
  renderSettingsNavigation();
  const item = platforms.selected, active = !!platforms.gitlabAuth;
  const pending = !!platforms.gitlabPending;
  const oauth = item?.credential_kind === "oauth";
  const status = item?.authorization_status;
  const reusable=!item && platforms.gitlabSetup?.status==="ready";
  const needsChoice=!item && ["application_selection_required","application_incompatible"].includes(platforms.gitlabSetup?.status);
  const incompatible=platforms.gitlabSetup?.status==="application_incompatible";
  const editing=(!status && !reusable) || gitlabEditingApplication;
  const origin=$("connection-url").value.trim().replace(/\/$/,"");
  const connectedAccounts=[...new Set(platforms.items.filter(c=>c.provider==="gitlab" && c.url===origin).map(c=>c.validation?.username).filter(Boolean))];
  $("gitlab-account-choice").hidden=!reusable || pending || active;
  $("gitlab-account-choice-hint").textContent=connectedAccounts.length ? `已有平台账号：${connectedAccounts.map(name=>`@${name}`).join("、")}。新平台请使用另一个账号。` : "请在 GitLab 网页登录本次要连接的账号。";
  $("gitlab-switch-account").href=reusable ? `${origin}/` : "";
  $("gitlab-switch-account").textContent=connectedAccounts.length ? "前往 GitLab 切换账号 ↗" : "前往 GitLab 登录账号 ↗";
  $("gitlab-account-switch-help").hidden=!connectedAccounts.length;
  $("gitlab-app-config-row").hidden=!editing;
  $("gitlab-advanced").open=editing;
  $("gitlab-setup").hidden=!editing;
  $("gitlab-account").textContent = active ? platforms.gitlabAuth.status === "waiting" ? "等待 GitLab 授权…" : "正在处理授权…" : oauth ? status === "active" ? `已连接 · @${item.validation.username}` : status === "logged_out" ? item.revocation_status === "confirmed" ? "已退出登录" : "本地已断开，远端撤销未确认" : status ? "需要重新授权" : "未连接" : "未连接";
  $("gitlab-login").textContent = status && status !== "logged_out" ? "重新授权" : reusable ? "授权新账号" : onboarding.active && !$("onboarding-only").checked ? "连接并启用" : "登录 GitLab";
  $("gitlab-login").disabled = active;
  $("connection-validation").hidden = oauth;
  $("connection-notes").hidden = !item || oauth;
  $("gitlab-login").hidden = (!item && !reusable) || pending || editing || incompatible;
  $("gitlab-logout").hidden = !status || status === "logged_out" || pending || editing;
  $("gitlab-logout").disabled = active;
  $("gitlab-edit-application").hidden=!status || editing || pending || incompatible;
  $("gitlab-edit-application").disabled=active;
  $("gitlab-edit-cancel").hidden=!status || !editing || active || pending;
  $("gitlab-cancel").hidden = !active;
  const hasApp=!!status;
  $("gitlab-advanced-title").hidden=true;
  if (!hasApp) {$("gitlab-advanced").open=!reusable;$("gitlab-setup").hidden=reusable;}
  $("gitlab-advanced-title").setAttribute("aria-disabled",String(active || pending || platforms.sending));
  $("gitlab-import").textContent=hasApp ? "更新并授权" : onboarding.active && !$("onboarding-only").checked ? "连接并启用" : "连接 GitLab";
  $("gitlab-initial-hint").hidden=hasApp;
  if (!hasApp && !platforms.gitlabSetup) $("gitlab-setup").hidden=false;
  renderGitLabReuse();renderOnboarding();
  renderGitLabSave();
  renderPlatformReferences();
  $("connection-verify").hidden = oauth || !item;
  for (const id of ["connection-name", "connection-url", "connection-provider", "gitlab-import"]) $(id).disabled = active || pending || (id === "connection-provider" && !!item) || (id === "connection-name" && gitlabEditingApplication);
  for (const option of gitlabScopeOptions) {
    $(option.id).disabled=option.required || active || pending;
    if (option.required) $(option.id).checked=true;
  }
  if (needsChoice) $("gitlab-import").disabled=true;
  $("gitlab-client-fields").hidden=needsChoice;
  if(onboarding.active && onboarding.pending){$("connection-provider").disabled=true;$("connection-url").readOnly=true;}
}
function renderGitLabSave() {
  const item=platforms.selected, name=$("connection-name").value.trim();
  $("connection-save").textContent=onboarding.active && item ? "连接并启用" : item ? "保存名称" : "连接并启用";
  $("connection-save").hidden=(!item || name===item.name) && !(onboarding.active && item);
  const editing=$("gitlab-advanced").open && !$("gitlab-setup").hidden;
  $("connection-delete").hidden=!item || !!item.references?.length || editing;
  $("connection-actions").hidden=$("connection-save").hidden && $("connection-delete").hidden;
  $("connection-save").disabled=platforms.sending || !!platforms.gitlabAuth || !!platforms.gitlabPending || !!item && (!name || name===item.name && !onboarding.active);
}
function selectedGitLabScopes() {
  return gitlabScopeOptions.filter(option=>option.required || $(option.id).checked).map(option=>option.scope);
}
function setGitLabScopes(scopes = null) {
  for (const option of gitlabScopeOptions) $(option.id).checked=option.required || (Array.isArray(scopes) ? scopes.includes(option.scope) : option.defaultSelected);
}
function gitlabPreparePayload() {
  const item=platforms.selected;
  // Existing accounts retain their application's scopes unless explicitly edited.
  const scopes=item?.authorization_status && !gitlabEditingApplication ? {} : {scopes:selectedGitLabScopes()};
  return {...(item ? {id:item.id,version:item.version} : {origin:$("connection-url").value.trim().replace(/\/$/,"")}),...scopes,...(!item && platforms.gitlabApplicationID ? {application_id:platforms.gitlabApplicationID} : {})};
}
function gitlabSetupFeedback(message = "", kind = "status") {
  const feedback=$("gitlab-setup-feedback");
  feedback.textContent=message;feedback.hidden=!message;feedback.dataset.kind=kind;
  feedback.setAttribute("role",kind === "error" ? "alert" : "status");
}
async function prepareGitLabPlatform(editApplication = false) {
  const version = platforms.formVersion, lookup=++platforms.gitlabSetupVersion, scopeVersion=gitlabScopeVersion;
  const wasOpen=$("gitlab-advanced").open;
  const source=gitlabPreparePayload();
  let result;
  try {result = await request("/v1/platforms/gitlab/prepare", connectionPayload({...source,...(editApplication ? {edit_application:true} : {})}), undefined, 50000);}
  catch (error) {if (version !== platforms.formVersion || lookup !== platforms.gitlabSetupVersion) return null;throw error;}
  if (version !== platforms.formVersion || lookup !== platforms.gitlabSetupVersion || connectionIsLark() || connectionIsScion() || source.origin !== gitlabPreparePayload().origin) return null;
  if (!editApplication && platforms.selected?.authorization_status && !gitlabEditingApplication && scopeVersion === gitlabScopeVersion && result.scopes) setGitLabScopes(result.scopes);
  if (!editApplication && platforms.gitlabEditingImportID === null) {
    $("gitlab-setup").hidden = !!platforms.selected?.authorization_status;
    if ($("gitlab-advanced").open === wasOpen) $("gitlab-advanced").open=!$("gitlab-setup").hidden;
    if (!$("gitlab-setup").hidden) {
      platforms.gitlabEditingImportID=result.client_import_id || "";
      if (scopeVersion === gitlabScopeVersion) setGitLabScopes(result.scopes || source.scopes);
      if (!$("gitlab-client-id").value) $("gitlab-client-id").value=result.client_id || "";
    }
  }
  $("gitlab-redirect-uri").value=result.redirect_uri;
  $("gitlab-applications-link").href=result.applications_url;
  platforms.gitlabSetup = result;
  renderPlatformReferences();
  return result;
}
async function discoverGitLabSetup() {
  if (connectionIsLark() || connectionIsScion() || !$("connection-url").value.trim()) return;
  const version=platforms.formVersion, origin=$("connection-url").value;
  try {if (await prepareGitLabPlatform()) {gitlabSetupFeedback();renderGitLabAccount();}}
  catch {if(version === platforms.formVersion && origin === $("connection-url").value) gitlabSetupFeedback("读取失败，请核对实例地址后重新保存。", "error");}
}
async function requestGitLabApplicationEdit() {
  if (platforms.sending || platforms.gitlabAuth || platforms.gitlabPending || gitlabEditingApplication) return;
  if (connectionDirty()) {connectionFeedback("请先保存名称，再修改应用配置。","error");return;}
  const version=platforms.formVersion;
  platforms.sending=true;$("connection-fields").disabled=true;
  $("connection-back").disabled=true;$("close-settings").disabled=true;
  connectionFeedback();gitlabSetupFeedback();
  try {
    const prepared=await prepareGitLabPlatform(true);if (!prepared) return;
    if (version!==platforms.formVersion) return;
    platforms.gitlabPending={action:"configure",version,prepared,affected:prepared.affected || [],title:"修改应用配置将影响以上连接和房间。",confirmLabel:"修改配置"};
  } catch {connectionFeedback("读取应用配置失败，请重试。", "error");}
  finally {platforms.sending=false;$("connection-fields").disabled=false;$("connection-back").disabled=false;$("close-settings").disabled=false;renderGitLabAccount();}
}
function beginGitLabApplicationEdit() {
  const pending=platforms.gitlabPending;
  if (pending?.action!=="configure" || pending.version!==platforms.formVersion) return;
  clearGitLabConfirmation();
  if (connectionDirty()) {renderGitLabAccount();connectionFeedback("请先保存名称，再修改应用配置。","error");return;}
  gitlabEditingApplication=true;
  platforms.gitlabSetup=pending.prepared;
  platforms.gitlabEditingImportID=pending.prepared.client_import_id || "";
  setGitLabScopes(pending.prepared.scopes);
  $("gitlab-client-id").value=pending.prepared.client_id || "";$("gitlab-client-secret").value="";
  connectionFeedback();gitlabSetupFeedback();renderGitLabAccount();$("gitlab-client-id").focus();
}
function cancelGitLabApplicationEdit() {
  if (platforms.sending || platforms.gitlabAuth) return;
  gitlabEditingApplication=false;
  platforms.gitlabEditingImportID=null;
  $("gitlab-client-secret").value="";
  gitlabSetupFeedback();renderGitLabAccount();
}
function openGitLabPopup() {
  try {
    const popup=window.open?.("about:blank", "_blank");
    if (popup) popup.opener=null;
    return popup || null;
  } catch {return null;}
}
async function startGitLabAuth(action, candidate = null, popup) {
  if (platforms.sending || platforms.gitlabAuth || platforms.gitlabPending || connectionIsLark()) return;
  const item = platforms.selected, version = platforms.formVersion;
  if (item && connectionDirty()) {connectionFeedback("请先保存名称，再操作授权。", "error");return;}
  if (!item && !$("connection-name").value.trim()) {connectionInputError("connection-name", "请输入连接名称。");return;}
  if (action === "login" && popup === undefined) popup=openGitLabPopup();
  platforms.sending = true; $("connection-fields").disabled = true;
  $("connection-back").disabled = true; $("close-settings").disabled = true;
  connectionFeedback("正在检查 GitLab 应用配置…");
  let pending;
  try {
    const prepared = candidate || await prepareGitLabPlatform(); if (!prepared) return;
    if (version !== platforms.formVersion) return;
    if (!["ready", "owner_verification_required"].includes(prepared.status)) {
      $("gitlab-setup").hidden = false;
      renderGitLabReuse();connectionFeedback(prepared.status==="application_selection_required" ? "请先选择一个兼容应用。" : prepared.status==="application_incompatible" ? "请先修改应用配置，再重新授权。" : "请先完成一次应用配置，然后登录 GitLab。", "error");return;
    }
    const affected = prepared.affected || [];
    const target = item ? {id:item.id,version:item.version} : {name:$("connection-name").value.trim(),origin:$("connection-url").value.trim()};
    pending = { action, version, target, prepared, affected, popup, updatingApplication:!!candidate && !!item?.authorization_status, creatingPlatform:!item };
    if (action === "logout" && (affected.length > 1 || affected.some(c=>c.referenced_rooms?.length))) {
      pending.title="退出登录将暂停以上房间的通知。";
      pending.confirmLabel="确认退出";
      platforms.gitlabPending = pending;
      connectionFeedback();
    }
  } catch {
    connectionFeedback("GitLab 应用配置检查失败，请稍后重试。", "error");
  } finally {
    if (!pending && popup && !popup.closed) popup.close();
    platforms.sending=false;renderSettingsNavigation();$("connection-fields").disabled=false;$("connection-back").disabled=false;$("close-settings").disabled=false;renderGitLabAccount();
  }
  if (platforms.gitlabPending) $("connection-auth-confirm").focus();
  else if (pending) await beginGitLabAuth(pending);
}
function clearGitLabConfirmation() {
  platforms.gitlabPending = null;
  renderPlatformReferences();
}
async function beginGitLabAuth({action, version, target, prepared, affected, popup, updatingApplication, creatingPlatform}) {
  if (platforms.sending || platforms.gitlabAuth || version !== platforms.formVersion || connectionIsLark()) return;
  clearGitLabConfirmation();
  platforms.sending = true; $("connection-fields").disabled = true;
  $("connection-back").disabled = true; $("close-settings").disabled = true;
  connectionFeedback(action === "logout" ? "正在退出 GitLab…" : "正在打开 GitLab 授权页面…");
  try {
    if (action === "login" && popup === undefined) popup=openGitLabPopup();
    const op=action==="login" && onboarding.active ? await prepareOnboarding() : null;
    const auth = await request(`/v1/platforms/gitlab/${action}`,connectionPayload({...target,application_id:prepared.application_id,client_import_id:prepared.client_import_id,scopes:prepared.scopes,...(op ? {onboarding_id:op.operation_id} : {}),affected}));
    if (version !== platforms.formVersion) return;
    platforms.gitlabAuth = {...auth, affected, updatingApplication, creatingPlatform, requestedScopes:prepared.scopes || ["api"], clientImportID:prepared.client_import_id || ""}; renderGitLabAccount();
    if (auth.authorization_url) {$("gitlab-login-link").href = auth.authorization_url; $("gitlab-login-link").hidden = false; if (popup && !popup.closed) popup.location.replace(auth.authorization_url);}
    connectionFeedback(action === "logout" ? "正在退出并核对撤销结果…" : "等待 GitLab 授权…");
    await pollGitLabAuth();
  } catch (error) {
    connectionFeedback(!error.code && error.message ? error.message : error.code === "identity" ? "登录需要从服务配置的规范地址打开 TeamX；请核对应用回调地址，并刷新页面。" : "授权结果未确认，请返回列表核对。", "error");
  } finally {if (popup && !platforms.gitlabAuth && !popup.closed) popup.close(); platforms.sending=false;renderSettingsNavigation();$("connection-fields").disabled=false;$("connection-back").disabled=false;$("close-settings").disabled=false;renderGitLabAccount();}
}
async function pollGitLabAuth() {
  const auth = platforms.gitlabAuth, version = platforms.formVersion;
  if (!auth) return;
  try {
    const result = await request("/v1/platforms/gitlab/status",connectionPayload({session_id:auth.session_id}));
    if (platforms.gitlabAuth !== auth || version !== platforms.formVersion) return;
    if (["waiting", "exchanging", "settling", "committing", "cleaning", "disconnecting"].includes(result.status)) {
      auth.status=result.status;renderGitLabAccount();
      platforms.gitlabTimer=setTimeout(()=>{void pollGitLabAuth();},1500);return;
    }
    clearGitLabAuthView();platforms.gitlabAuth=null;
    for (const item of result.platforms || []) updatePlatformItem(item);
    const saved=(result.platforms || []).find(c=>c.id===platforms.selected?.id) || (result.platforms || []).find(c=>c.id===auth.platform_id) || result.platforms?.[0];
    if (saved) await editPlatform(saved);else renderGitLabAccount();
    if (auth.onboarding_id && onboarding.pending?.operation_id===auth.onboarding_id) {
      const resultVersion=platforms.formVersion,resultIdentity=onboardingIdentity();
      try {const op=await request("/v1/platforms/onboarding/status",connectionPayload({operation_id:auth.onboarding_id}));
        if (onboardingCurrent(resultVersion,resultIdentity) && onboarding.pending?.operation_id===auth.onboarding_id) acceptOnboarding(op);
      } catch {if(onboardingCurrent(resultVersion,resultIdentity)){onboarding.error="账号登录结果已返回，通知与工具的进度未确认，请读取接入进度。";renderOnboarding();}}
    }
    const savedMessage=auth.updatingApplication ? "应用配置已更新。" : auth.creatingPlatform ? "已连接 GitLab。" : "授权已更新。";
    const messages={succeeded:savedMessage + (auth.affected.some(c=>c.referenced_rooms?.length) ? "通知请手动同步恢复。" : ""),logged_out:"已退出登录，远端撤销已确认。",disconnected:"本地已断开，远端撤销或调用结清未确认。",cancelled:"本次登录已取消，请核对连接状态。",expired:"本次登录已过期，可以重新登录。",failed:"授权未完成，保存或切换结果请返回列表核对。"};
    const requestedScopes=result.requested_scopes || auth.requestedScopes, missingScopes=result.missing_scopes || [];
    const scopeError=`${missingScopes.length ? `GitLab 应用缺少权限：${missingScopes.join(" + ")}。` : `GitLab 拒绝了申请的权限：${requestedScopes.join(" + ")}。`}请打开 GitLab 应用设置，核对当前 Application ID 并勾选 ${requestedScopes.join(" + ")}，保存后重新登录。${requestedScopes.includes("write_repository") ? "若不需要拉取和推送代码，可取消 write_repository 后重新授权。" : ""}`;
    const duplicateAccount=result.error_code==="account_already_connected";
    const errors={account_already_connected:`${result.account_username ? `@${result.account_username}` : "本次授权的 GitLab 账号"} 已连接其他平台，请切换账号后重试。`,invalid_client:"应用凭据无效，请检查 Application ID 和 Secret。",invalid_scope:scopeError,invalid_grant:"授权已失效，请重新登录 GitLab。",identity_mismatch:"GitLab 账号与原账号不符，请为另一个账号新建连接；原连接未切换。"};
    const cleanupMessage=result.cleanup_status === "manual_required" ? "候选授权待人工清理，未撤销现用授权。" : ["pending","unconfirmed"].includes(result.cleanup_status) ? "候选令牌或远端撤销未确认，请在 GitLab 个人应用页面核对授权。" : result.cleanup_status === "revoked" && result.status === "failed" ? "本次未采用的候选令牌已撤销。" : "";
    const message=`${errors[result.error_code] || messages[result.status] || "请核对连接状态。"}${duplicateAccount ? "" : cleanupMessage}`;
    if (result.status === "failed" && auth.clientImportID) {
      gitlabEditingApplication=!!platforms.selected?.authorization_status;
      platforms.gitlabEditingImportID=auth.clientImportID;
      $("gitlab-setup").hidden=false;$("gitlab-advanced").open=true;
      connectionFeedback();gitlabSetupFeedback(message,"error");
      renderGitLabAccount();
    } else connectionFeedback(message,["succeeded","logged_out"].includes(result.status)?"success":"error",duplicateAccount ? result.cleanup_status==="manual_required" ? "本次未使用的授权尚未自动撤销，已有平台的授权保持不变。" : cleanupMessage : "");
  } catch {
    if (platforms.gitlabAuth===auth && version===platforms.formVersion) {connectionFeedback("授权状态读取失败，请重新检查连接。", "error");clearGitLabAuthView();renderGitLabAccount();}
  }
}
function clearGitLabAuthView() {clearTimeout(platforms.gitlabTimer);platforms.gitlabTimer=null;$("gitlab-login-link").hidden=true;$("gitlab-login-link").href="";}
async function cancelGitLabAuth(keepalive = false) {
  clearGitLabConfirmation();
  const auth=platforms.gitlabAuth;clearGitLabAuthView();platforms.gitlabAuth=null;platforms.formVersion++;
  if (auth) {try {const body=connectionPayload({session_id:auth.session_id}); if (keepalive) await globalThis.fetch("/v1/platforms/gitlab/cancel",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body),credentials:"same-origin",keepalive:true}); else await request("/v1/platforms/gitlab/cancel",body);} catch { /* Explicit list refresh reconciles a late commit. */ }}
  if (!connectionIsLark() && !connectionIsScion()) renderGitLabAccount();
}
async function cancelPlatformAuth() {await cancelScionAuth();await cancelGitLabAuth();await cancelLarkAuth();}
$("gitlab-login").addEventListener("click",()=>startGitLabAuth("login"));
$("gitlab-edit-application").addEventListener("click",requestGitLabApplicationEdit);
$("gitlab-repair-application").addEventListener("click",requestGitLabApplicationEdit);
$("gitlab-edit-cancel").addEventListener("click",cancelGitLabApplicationEdit);
$("gitlab-logout").addEventListener("click",()=>startGitLabAuth("logout"));
$("connection-auth-confirm").addEventListener("click",()=>{
  if (connectionIsScion()) return logoutScion();
  if (connectionIsLark()) { if (platforms.larkPending) return beginLarkAuth(platforms.larkPending); }
  else if (platforms.gitlabPending?.action === "configure") return beginGitLabApplicationEdit();
  else if (platforms.gitlabPending) return beginGitLabAuth(platforms.gitlabPending);
});
$("connection-auth-dismiss").addEventListener("click",()=>{
  if (connectionIsScion()) {clearScionConfirmation();$("connection-scion-logout").focus();}
  else if (connectionIsLark()) {const action=platforms.larkPending?.action;clearLarkConfirmation();$(larkAuthButton(action)).focus();}
  else {const action=platforms.gitlabPending?.action;clearGitLabConfirmation();renderGitLabAccount();$(action === "configure" ? "gitlab-edit-application" : action === "logout" ? "gitlab-logout" : "gitlab-login").focus();}
  connectionFeedback("已取消本次操作。");
});
$("gitlab-cancel").addEventListener("click",async()=>{await cancelGitLabAuth();connectionFeedback("已请求取消，请核对连接状态；已提交的授权不会回滚。");});
for (const option of gitlabScopeOptions) $(option.id).addEventListener("change",async()=>{
  gitlabScopeVersion++;if (option.required) $(option.id).checked=true;
  if (!platforms.selected && platforms.gitlabSetup?.status==="application_incompatible") await discoverGitLabSetup();
});
async function configureGitLabPlatform() {
  if (platforms.sending || platforms.gitlabAuth || platforms.gitlabPending) return;
  if (!platforms.selected && !$("connection-name").value.trim()) {connectionInputError("connection-name","请输入连接名称。");return;}
  if (platforms.selected && connectionDirty()) {connectionFeedback("请先保存名称，再更新应用配置。","error");return;}
  const version=platforms.formVersion;
  const popup=openGitLabPopup();
  let candidate;
  platforms.sending=true;$("connection-fields").disabled=true;$("connection-back").disabled=true;$("close-settings").disabled=true;
  connectionFeedback();gitlabSetupFeedback("正在准备 GitLab 授权…");
  try {
    const prepared=await prepareGitLabPlatform(gitlabEditingApplication);if (!prepared) return;
    const source=gitlabPreparePayload();
    const replacing=platforms.gitlabEditingImportID;
    const reusable=!platforms.selected?.authorization_status && !$("gitlab-client-secret").value && ["ready","owner_verification_required"].includes(prepared.status) && (prepared.status==="ready" || $("gitlab-client-id").value.trim()===prepared.client_id) && source.scopes.join(" ")===(prepared.scopes || []).join(" ");
    if (["application_selection_required","application_incompatible"].includes(prepared.status)) {renderGitLabReuse();gitlabSetupFeedback("请先选择兼容应用或处理上方兼容性问题。","error");return;}
    let result=prepared;
    if (!reusable) {
      if (!$("gitlab-client-id").value.trim() || !$("gitlab-client-secret").value) {gitlabSetupFeedback(!$("gitlab-client-id").value.trim() ? "请填写 Application ID。" : "请填写 Secret。","error");return;}
      result=await request("/v1/platforms/gitlab/initialize",connectionPayload({...source,...(replacing ? {client_import_id:replacing} : {}),client:{client_id:$("gitlab-client-id").value.trim(),client_secret:$("gitlab-client-secret").value,redirect_uri:prepared.redirect_uri,scopes:source.scopes}}),undefined,50000);
    }
    if (version!==platforms.formVersion) return;
    platforms.gitlabSetupVersion++;platforms.gitlabSetup=result;
    platforms.gitlabEditingImportID=platforms.selected?.authorization_status ? null : result.client_import_id || "";
    if (["ready","owner_verification_required"].includes(result.status)) candidate=result;
    if (candidate) gitlabSetupFeedback();
    else gitlabSetupFeedback("应用配置未就绪，请检查填写内容。","error");
  } catch (error) {
    $("gitlab-setup").hidden=false;$("gitlab-advanced").open=true;
    if(error.code === "conflict") platforms.gitlabEditingImportID=null;
    const reload=platforms.selected?.authorization_status ? "请取消修改，再点击「修改应用配置」重新填写。" : "请返回列表重新打开连接，再填写。";
    gitlabSetupFeedback(error.code === "busy" ? "登录仍在处理中，请等待结束后重新保存。" : error.code === "conflict" ? `配置已变化，${reload}` : error.code === "request" ? "配置无效，请检查 Application ID、Secret 和回调地址。" : "保存结果未确认，请重新打开连接核对。", "error");
  }
  finally {if ((!candidate || version !== platforms.formVersion) && popup && !popup.closed) popup.close();$("gitlab-client-secret").value="";platforms.sending=false;renderSettingsNavigation();$("connection-fields").disabled=false;$("connection-back").disabled=false;$("close-settings").disabled=false;renderGitLabAccount();}
  if (candidate && version === platforms.formVersion) await startGitLabAuth("login",candidate,popup);
}
$("gitlab-import").addEventListener("click",configureGitLabPlatform);
function connectionBrand() { return $("connection-provider").value === "lark" ? "lark" : "feishu"; }
function connectionOrigin() { return connectionBrand() === "lark" ? "https://open.larksuite.com" : "https://open.feishu.cn"; }
function updatePlatformItem(item) {
  platforms.generation++; // A confirmed mutation supersedes earlier list reads.
  const index = platforms.items.findIndex(c => c.id === item.id);
  if (index < 0) platforms.items.push(item); else platforms.items[index] = item;
  if (platforms.selected?.id === item.id) platforms.selected = item;
}
function clearLarkAuthView() {
  clearTimeout(platforms.authTimer); platforms.authTimer = null;
  $("connection-login-panel").hidden = true;
  $("connection-login-link").href = ""; $("connection-login-qr").src = "";
  $("connection-login-cancel").hidden = true;
}
// Setup results belong to this form only. A discovered application is not a verified user.
function resetLarkSetup() { platforms.larkSetupResult = null; platforms.larkSetupUncertain = false; platforms.larkAccountVerified = null; }
function rememberLarkSetup(result) {
  platforms.larkSetupResult = result;
  platforms.larkSetupUncertain = true;
}
function larkSetupFailure(result) {
  const authorization = result.stage === "authorization";
  const outcome = result.status === "cancelled" ? "已取消" : result.status === "expired" ? "已过期" : "失败";
  if (authorization && result.configuration_found) {
    const reason = result.error_code === "permission_denied" ? "读取权限不足，请检查后继续授权。" : `授权${outcome}，请继续授权。`;
    return `应用已配置。${reason}`;
  }
  const reason = {configuration_unconfirmed:"结果未确认，请重新检测。",configuration_changed:"本机配置已变化，请核对后重新检测。",identity_mismatch:"应用或品牌不符，请核对后重新检测。",setup_brand_unverified:"Lark 网页配置尚未验证，请使用「修复连接」。"}[result.error_code];
  if (reason) return `${result.configuration_found ? "已发现本机配置。" : ""}${reason}`;
  return `${authorization ? "账号授权" : "应用配置"}${outcome}。${result.configuration_found ? "已发现本机配置" : "尚未确认本机配置"}，请重新检测。`;
}
function renderLarkSetupCommand() {
  const existing = $("connection-setup-existing").checked;
  const executable = $("connection-executable").value || "lark-cli";
  const profile = platforms.discovery?.setup_profile || `teamx-${connectionBrand()}`;
  const command = `'${executable.replaceAll("'", "'\\''")}' config init${existing ? "" : " --new"} --brand ${connectionBrand()} --name ${profile}`;
  $("connection-setup-hint").textContent = existing
    ? "准备好已有开放平台应用的 App ID 和 App Secret，执行命令后按提示完成配置。"
    : "执行命令后，按提示在浏览器中创建并配置应用。";
  if ($("connection-setup-command").textContent !== command) {
    $("connection-setup-command").textContent = command;
    $("connection-setup-copy").textContent = "复制命令";
    $("connection-setup-copy-status").textContent = "";
  }
}
function closeLarkControls() {
  $("connection-application-picker").hidden = true;
  $("connection-application-change").setAttribute("aria-expanded", "false");
  $("connection-account-manage").open = false;
}
function larkApplicationLabel(option) {
  const setup = platforms.larkSetupResult;
  if (option && setup?.cli_profile?.profile === option.name) return setup.app_name || setup.app_id || option.name;
  return option?.appId || option?.name || "尚未选择应用";
}
function renderLarkAccount() {
  renderSettingsNavigation();
  const option = platforms.discovery?.profiles.find(p => p.name === $("connection-profile").value);
  const active = !!platforms.auth || !!platforms.larkStarting, pending = !!platforms.larkPending;
  const setup = platforms.larkSetupResult, hasProfiles = !!platforms.discovery?.profiles.length;
  const blocked = !!setup && setup.stage !== "authorization" && setup.status !== "succeeded";
  const incomplete = !!setup && setup.status !== "succeeded";
  const application = (platforms.auth || platforms.larkStarting)?.stage === "application";
  const v = platforms.selected?.validation;
  const same = !!platforms.selected && JSON.stringify(larkProfileDraft()) === JSON.stringify(platforms.selected.cli_profile);
  const expired = !!option?.user && ["expired", "error", "no_token"].includes(option.tokenStatus);
  const changedUser = same && v?.status === "valid" && !!option?.user && option.user !== v.username;
  const flowVerified = setup?.status === "succeeded" && setup.username === option?.user;
  const loginVerified = platforms.larkAccountVerified?.username === option?.user && JSON.stringify(platforms.larkAccountVerified?.profile) === JSON.stringify(larkProfileDraft());
  const verified = !expired && !changedUser && (same && v?.status === "valid" || flowVerified || loginVerified);
  const needsAuth = !option?.user || expired || setup?.stage === "authorization" && incomplete;
  const needsRepair = !!platforms.larkDiscoveryError || !!platforms.larkSetupUncertain || expired || same && v?.status === "failed" || connectionBrand() === "lark" && !hasProfiles && !!platforms.discovery;
  if (active || pending || platforms.detecting) closeLarkControls();

  $("connection-application").hidden = !hasProfiles && !setup?.configuration_found && !active;
  $("connection-application-name").textContent = application ? "正在配置…" : setup?.configuration_found && active ? setup.app_name || setup.app_id : option ? larkApplicationLabel(option) : setup?.app_name || setup?.app_id || (platforms.selected ? "原应用不可用，请重新选择" : "尚未选择应用");
  $("connection-application-change").textContent = option ? "更换…" : "选择…";
  $("connection-application-change").disabled = active || pending || platforms.detecting;
  $("connection-profile-choice").hidden = !hasProfiles;
  $("connection-setup-other").hidden = connectionBrand() !== "feishu";
  $("connection-setup-other").disabled = active || pending || platforms.detecting || platforms.larkSetupUncertain;
  $("connection-account-section").hidden = !option && !active && !platforms.detecting && !platforms.larkDiscoveryError;
  $("connection-account-manage").hidden = !option?.user || active || pending || platforms.detecting || blocked;
  $("connection-reauthorize").hidden = needsAuth;
  $("connection-account-reverify").hidden = !platforms.selected || !same;
  $("connection-account-reverify").disabled = active || pending || platforms.detecting;
  $("connection-logout").hidden = !option?.user || active || blocked || platforms.detecting;
  $("connection-login").hidden = !option || !needsAuth || active || blocked || platforms.detecting;
  $("connection-login").textContent = setup?.stage === "authorization" && incomplete ? "继续账号授权" : expired ? "重新授权" : `登录${connectionBrand() === "lark" ? "Lark" : "飞书"}`;
  for (const id of ["connection-profile", "connection-executable", "connection-detect", "connection-login", "connection-logout", "connection-reauthorize"]) $(id).disabled = active || pending || platforms.detecting;
  $("connection-name").disabled = active || pending;
  $("connection-provider").disabled = active || pending || !!platforms.selected || onboarding.active && !!onboarding.pending;
  $("connection-save").disabled = active || pending || incomplete || platforms.detecting || !!platforms.selected && !connectionDirty() && !onboarding.active;
  $("connection-save").textContent = onboarding.active ? $("onboarding-only").checked ? "连接并核验" : "连接并启用" : platforms.selected ? "保存更改" : "保存连接";
  $("connection-save").hidden = !platforms.selected && (!option?.user || active || incomplete);
  $("connection-actions").hidden = !platforms.selected && $("connection-save").hidden;
  $("connection-verify").hidden = true;
  $("connection-notes").hidden = true;
  $("connection-validation").hidden = true;
  $("connection-delete").disabled = active || pending || !!platforms.selected?.references?.length;
  $("connection-login-cancel").hidden = !platforms.auth;
  $("connection-login-cancel").textContent = platforms.auth?.action === "setup" ? "取消本次连接" : "取消本次登录";
  $("connection-guided").hidden = active || pending || platforms.detecting || !platforms.discovery || hasProfiles || !!setup;
  $("connection-guided-hint").textContent = connectionBrand() === "lark" ? "Lark 网页配置尚未验证，请展开「修复连接」配置本机应用。" : "通过官方页面创建新应用或连接已有应用，无需手动填写凭据。";
  $("connection-setup-start").hidden = connectionBrand() !== "feishu";
  $("connection-setup-start").textContent = "配置并连接";
  $("connection-setup-start").disabled = active || pending || platforms.detecting || platforms.larkSetupUncertain;
  $("connection-recheck").hidden = active || pending || !platforms.larkSetupUncertain;
  $("connection-recheck").disabled = platforms.detecting;
  $("connection-advanced").hidden = active || pending || platforms.detecting || !needsRepair;
  if (!needsRepair) $("connection-advanced").open = false;
  $("connection-setup").hidden = hasProfiles && !blocked;
  $("connection-detect").textContent = platforms.detecting ? "正在检测…" : "重新检测";
  renderLarkSetupCommand();

  const sendProfile = JSON.stringify(larkProfileDraft());
  if (platforms.sendProfile !== sendProfile) {
    platforms.sendProfile = sendProfile;
    $("connection-send-status").textContent = "请先检查账号与发送权限。";
    $("connection-sending").open = false;
  }
  const sendAvailable = !!option?.user && !expired && !changedUser && !incomplete && !active && !pending && !platforms.detecting;
  $("connection-sending").hidden = !sendAvailable;
  for (const id of ["connection-send-check", "connection-send-authorize", "connection-target-authorize", "connection-send-status"]) $(id).hidden = !sendAvailable;
  renderPlatformReferences();

  const account = $("connection-account"), status = $("connection-account-state");
  status.hidden = true; status.textContent = ""; status.dataset.kind = "status";
  if (active) {
    account.textContent = application ? platforms.auth?.qrURL ? "等待你完成应用配置…" : "正在准备应用配置页面…" : (platforms.auth || platforms.larkStarting).action === "logout" ? "正在退出登录…" : platforms.auth?.qrURL ? "等待你完成账号授权…" : "正在准备账号授权…";
  } else if (platforms.detecting) account.textContent = "正在检测…";
  else if (!platforms.discovery) account.textContent = "检测失败，请展开「修复连接」检查本机工具。";
  else if (blocked) account.textContent = "应用配置待核对";
  else if (setup?.stage === "authorization" && incomplete) account.textContent = "账号授权未完成";
  else {
    account.textContent = option?.user || "尚未登录";
    if (option?.user) {
      status.hidden = false;
      status.textContent = expired ? "需要重新授权" : changedUser ? "账号已变化" : verified ? "已验证" : same && v?.status === "failed" ? "验证失败" : "已登录，待验证";
      status.dataset.kind = verified ? "success" : expired || changedUser || v?.status === "failed" ? "error" : "status";
    }
  }
}
async function detectLarkPlatform({profile, exact = false, preserveSetup = false} = {}) {
  if (!connectionIsLark() || platforms.auth || platforms.larkStarting) return;
  clearLarkConfirmation();
  const version = ++platforms.formVersion, brand = connectionBrand();
  const selected = profile ?? platforms.larkSetupResult?.cli_profile?.profile ?? $("connection-profile").value;
  exact ||= !!platforms.larkSetupUncertain;
  platforms.detecting = true; platforms.larkDiscoveryError = null; platforms.discovery = null; renderLarkAccount();
  try {
    const data = await request("/v1/platforms/lark/discover", connectionPayload({ executable: $("connection-executable").value.trim(), brand }), undefined, 40000);
    if (version !== platforms.formVersion) return;
    platforms.discovery = data;
    $("connection-executable").value = data.executable;
    const labels = data.profiles.map(p => larkApplicationLabel(p));
    const options = data.profiles.map((p, i) => new Option(labels.filter(label => label === labels[i]).length > 1 ? `${labels[i]} · 配置 ${p.name}` : labels[i], p.name));
    if (exact || data.profiles.length !== 1 || selected && !data.profiles.some(p => p.name === selected)) options.unshift(new Option("选择应用配置", ""));
    setSelectOptions("connection-profile", options, data.profiles.some(p => p.name === selected) ? selected : selected || exact ? "" : data.profiles.length === 1 ? data.profiles[0].name : "");
    $("connection-profile-choice").hidden = !data.profiles.length;
    if (!preserveSetup) resetLarkSetup();
    connectionFeedback();
  } catch (error) {
    if (version !== platforms.formVersion) return;
    platforms.larkDiscoveryError = error.code || "query_failed";
    $("connection-advanced").open = true;
    connectionFeedback(connectionValidationErrors[error.code] || "账号检测失败，请核对 CLI 配置或网络后重试。", "error");
  } finally {
    if (version === platforms.formVersion) { platforms.detecting = false; renderLarkAccount(); }
  }
}
async function cancelLarkAuth(keepalive = false) {
  clearLarkConfirmation();
  const auth = platforms.auth;
  clearLarkAuthView(); platforms.formVersion++;
  const version = platforms.formVersion;
  if (!auth) return;
  // Stop local polling immediately. Cancellation is never a logout/revocation.
  platforms.auth = null;
  if (auth.action === "setup") platforms.larkSetupUncertain = true;
  let result;
  try {
    const body = {...auth.owner, session_id: auth.session_id};
    if (keepalive) await globalThis.fetch("/v1/platforms/lark/cancel", {method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body),credentials:"same-origin",keepalive:true});
    else result = await request("/v1/platforms/lark/cancel", body);
    if (version !== platforms.formVersion) return;
    if (result && auth.action === "setup") rememberLarkSetup(result);
  }
  catch { if (version === platforms.formVersion) connectionFeedback("取消结果未确认，请重新检测。", "error"); }
  if (version === platforms.formVersion) renderLarkAccount();
  return result;
}
function larkAuthButton(action) {return action === "logout" ? "connection-logout" : action === "authorize-targets" ? "connection-target-authorize" : action === "authorize-sending" ? "connection-send-authorize" : "connection-login";}
function clearLarkConfirmation() {
  const pending = platforms.larkPending;
  platforms.larkPending = null;
  if (pending) renderLarkAccount();
}
async function startLarkAuth(action) {
  if (platforms.sending || platforms.auth || platforms.larkPending || platforms.larkStarting || platforms.detecting || !platforms.discovery) return;
  const profile = larkProfileDraft();
  if (!profile.profile) return;
  const affected = platforms.items.filter(c => c.provider === "lark" && c.cli_profile?.profile === profile.profile);
  const pending = {action,profile,version:platforms.formVersion,
    affected:affected.map(c => ({id:c.id,version:c.version,referenced_rooms:(c.references || []).map(({room_id,room_instance}) => ({room_id,room_instance}))})),
    title:action === "logout" ? "退出登录将暂停以上房间的通知。" : "授权会暂停相关通知，原账号验证后自动恢复。",
    confirmLabel:action === "logout" ? "确认退出" : "继续授权",
    description:`${action === "authorize-targets" ? "仅申请目标核验权限，不启用发送。" : action === "authorize-sending" ? "申请发送权限，具体发送仍需授权。" : ""}共用此应用配置的 CLI 和其他工具也会受影响。${action === "logout" ? "退出登录不等于取消应用的全部授权。" : ""}`};
  if (action === "logout" || action === "authorize-sending" || action === "authorize-targets" || affected.length) {
    platforms.larkPending = pending;
    connectionFeedback();renderLarkAccount();$("connection-auth-confirm").focus();
    return;
  }
  await beginLarkAuth(pending);
}
async function startLarkSetup() {
  if (!connectionIsLark() || connectionBrand() !== "feishu" || platforms.sending || platforms.auth || platforms.larkStarting || platforms.larkPending || platforms.detecting || !platforms.discovery || platforms.larkSetupUncertain) return;
  if (!$("connection-name").value.trim()) {connectionInputError("connection-name", "请输入连接名称。");return;}
  resetLarkSetup();
  await beginLarkAuth({action:"setup",stage:"application",version:platforms.formVersion});
}
async function beginLarkAuth(pending) {
  const {action,profile,version,affected} = pending;
  if (!connectionIsLark() || platforms.sending || platforms.auth || platforms.larkStarting || version !== platforms.formVersion) return;
  platforms.larkStarting = pending;
  clearLarkConfirmation();
  platforms.sending = true; $("connection-fields").disabled = true; $("connection-back").disabled = true; $("close-settings").disabled = true;
  renderLarkAccount();
  connectionFeedback(action === "setup" ? "正在准备配置页面…" : action === "logout" ? "正在退出登录…" : "正在发起授权…");
  const owner = connectionPayload({});
  try {
    if(onboarding.active && ["login","setup"].includes(action))await prepareOnboarding();
    const payload = action === "setup" ? {executable:$("connection-executable").value.trim(),brand:connectionBrand()} : {cli_profile:profile,affected};
    const result = await request(`/v1/platforms/lark/${action}`, {...owner,...payload}, undefined, 40000);
    if (version !== platforms.formVersion) {
      void request("/v1/platforms/lark/cancel", {...owner,session_id:result.session_id}).catch(() => {});
      return;
    }
    for (const c of result.platforms || []) updatePlatformItem(c);
    platforms.auth = {session_id:result.session_id,action,profile,affected,owner,stage:result.stage};
    platforms.larkStarting = null;
    $("connection-account").textContent = action === "logout" ? "正在退出登录…" : "正在准备授权页面…";
    await pollLarkAuth();
  } catch (error) {
    if (version !== platforms.formVersion) return;
    if (action === "setup") {
      platforms.larkSetupUncertain = true;
      connectionFeedback(connectionValidationErrors[error.code] || (error.code === "busy" ? "已有操作正在处理，请稍后重新检测。" : error.code === "setup_brand_unverified" ? "此品牌的网页配置尚未验证，请展开「修复连接」检查本机应用。" : "配置结果未确认，请先重新检测。"), "error");
      return;
    }
    connectionFeedback(error.code === "conflict" ? "连接或引用范围已变化，请返回列表重新加载。" : error.code === "busy" ? "操作尚未结束，请稍后重新检测。" : "授权结果未确认，请重新检测。", "error");
  } finally {
    platforms.larkStarting = null;
    platforms.sending = false; renderSettingsNavigation(); $("connection-fields").disabled = false; $("connection-back").disabled = false; $("close-settings").disabled = false;
    renderLarkAccount();
  }
}
async function pollLarkAuth() {
  const auth = platforms.auth;
  if (!auth) return;
  let finished = false, version = platforms.formVersion;
  try {
    const result = await request("/v1/platforms/lark/status", {...auth.owner,session_id:auth.session_id});
    if (platforms.auth !== auth || version !== platforms.formVersion) return;
    if (auth.action === "setup" && (result.configuration_found || !["starting", "waiting"].includes(result.status))) rememberLarkSetup(result);
    if (result.status === "starting" || result.status === "waiting") {
      const application = result.stage === "application";
      // Remove the preceding stage's link and QR, including during OAuth startup.
      if (auth.stage !== result.stage || !result.verification_url || !result.qrcode_url) {clearLarkAuthView();auth.qrURL = null;}
      auth.stage = result.stage;
      $("connection-login-hint").textContent = application ? "创建或选择应用，完成后自动继续授权。" : "打开页面或扫码授权账号。";
      $("connection-login-link").textContent = application ? "打开配置页面 ↗" : "打开授权页面 ↗";
      $("connection-login-qr").alt = application ? "飞书应用配置二维码" : "飞书 / Lark 账号授权二维码";
      if (result.verification_url && result.qrcode_url) {
        $("connection-login-link").href = result.verification_url;
        if (auth.qrURL !== result.qrcode_url) { $("connection-login-qr").src = result.qrcode_url; auth.qrURL = result.qrcode_url; }
        $("connection-login-panel").hidden = false;
      }
      renderLarkAccount();
      connectionFeedback();
      platforms.authTimer = setTimeout(() => { void pollLarkAuth(); }, 1500);
      return;
    }
    clearLarkAuthView(); platforms.auth = null; finished = true;
    const setup = auth.action === "setup";
    const detecting = detectLarkPlatform(setup ? {profile:result.cli_profile?.profile || "",exact:true,preserveSetup:true} : {preserveSetup:!!platforms.larkSetupResult}); version = platforms.formVersion;
    await detecting;
    if (version !== platforms.formVersion) return;
    if (result.status === "succeeded") {
      if (auth.action === "logout") {resetLarkSetup();renderLarkAccount();}
      if (setup && (!result.cli_profile || $("connection-profile").value !== result.cli_profile.profile || !platforms.discovery)) {
        connectionFeedback("应用配置未确认，连接未保存。请重新检测。", "error");return;
      }
      if ((setup || platforms.larkSetupResult) && platforms.discovery?.profiles.find(p => p.name === $("connection-profile").value)?.user !== result.username) {
        rememberLarkSetup({...platforms.larkSetupResult,status:"failed",stage:"application",error_code:"identity_mismatch"});renderLarkAccount();
        connectionFeedback("账号与核验结果不符，连接未保存。请重新检测。", "error");return;
      }
      if (setup) {platforms.larkSetupUncertain = false;$("connection-advanced").open = false;renderLarkAccount();}
      else if (auth.action === "login" && platforms.larkSetupResult) {platforms.larkSetupResult = {...platforms.larkSetupResult,status:"succeeded",username:result.username};platforms.larkSetupUncertain = false;renderLarkAccount();}
      if (auth.action === "login" && platforms.discovery?.profiles.find(p => p.name === auth.profile.profile)?.user === result.username) {
        platforms.larkAccountVerified = {profile:auth.profile,username:result.username};renderLarkAccount();
      }
      if (auth.action === "login" && platforms.selected && JSON.stringify(auth.profile) === JSON.stringify(platforms.selected.cli_profile)) {
        const item = platforms.selected;
        const verified = await request("/v1/platforms/verify", connectionPayload({ id: item.id, version: item.version }), undefined, 40000);
        if (version !== platforms.formVersion) return;
        updatePlatformItem(verified); renderLarkAccount();
        if (verified.validation.status !== "valid") { connectionFeedback(`登录已完成，连接验证失败：${connectionValidationErrors[verified.validation.error_code] || "请重试验证。"}`, "error"); return; }
      }
      const savedProfile = platforms.selected && JSON.stringify(auth.profile) === JSON.stringify(platforms.selected.cli_profile);
      const sharedCount = (auth.affected || []).filter(c => c.id !== platforms.selected?.id).length;
      const message = auth.action === "logout" ? "已退出登录。" : auth.action === "authorize-sending" ? "发送权限已授权。" : auth.action === "authorize-targets" ? "目标核验权限已授权。" : savedProfile ? "账号已验证。" : "账号已验证，请保存连接。";
      const shared = auth.action !== "logout" && sharedCount ? `另有 ${sharedCount} 个共享连接需重新验证。` : "";
      const renewal = result.renewal_unavailable ? "登录无法自动续期，请联系应用管理员。" : "";
      const detail = auth.action === "logout" ? "退出登录不等于取消应用的全部授权。如需完全取消，请前往飞书 / Lark 授权管理页。" : result.renewal_unavailable ? () => `登录将于 ${formatDate(result.expires_at)} 到期。请应用管理员在开发者后台开启「刷新 user_access_token」并发布新版本，再重新授权。` : "";
      connectionFeedback(message + shared + renewal, result.renewal_unavailable ? "status" : "success", detail);
    } else connectionFeedback(setup ? larkSetupFailure(result) : result.status === "cancelled" ? "登录已取消，请重新检测。" : connectionValidationErrors[result.error_code] || "认证失败或已过期，请重新发起。", "error");
  } catch (error) {
    if (finished) { if (version === platforms.formVersion) connectionFeedback("授权已完成，验证结果未确认。请重新验证。", "error"); return; }
    if (platforms.auth !== auth) return;
    // Ensure an unobserved flow cannot keep changing credentials later.
    const cancelVersion = platforms.formVersion + 1;
    await cancelLarkAuth();
    if (cancelVersion !== platforms.formVersion) return;
    connectionFeedback(auth.action === "setup" ? "配置或授权结果未确认，请重新检测。" : "授权结果未确认，请重新检测。", "error");
  }
}
function connectionPayload(extra) {
  return { service_epoch: state.catalog.service_epoch, caller: { kind: "human", id: state.operator }, ...extra };
}
function renderPlatforms() {
  $("platforms-empty").hidden = platforms.items.length > 0;
  $("platforms-list").replaceChildren(...platforms.items.map(item => {
    const button = document.createElement("button"); button.type = "button"; button.className = "connection-row";button.dataset.platformId=item.id;
    const label = document.createElement("span"), name = document.createElement("strong"), detail = document.createElement("small"), status = document.createElement("span");
    name.textContent = item.name; detail.textContent = item.provider === "lark" ? `${item.cli_profile?.brand === "lark" ? "Lark" : "飞书"}${item.validation.username ? ` · ${item.validation.username}` : ""}` : `${item.provider === "scion" ? "Scion" : "GitLab"} · ${item.url}`;
    status.textContent = item.credential_kind === "oauth" && item.authorization_status !== "active" ? (item.authorization_status === "logged_out" ? "已退出 ›" : "需要重新授权 ›") : item.validation.status === "valid" ? "已验证 ›" : item.validation.status === "failed" ? "验证失败 ›" : "未验证 ›";
    label.append(name, detail); button.append(label, status);
    button.addEventListener("click", () => editPlatform(item)); return button;
  }));
}
async function loadPlatforms() {
  const generation = ++platforms.generation;
  // Keep the current content while reading; only a failed read needs an action.
  $("platforms-list-view").setAttribute("aria-busy", "true");
  $("platforms-retry").disabled = true;
  $("platforms-retry").textContent = "重试中…";
  try {
    const query = new URLSearchParams({ service_epoch: state.catalog.service_epoch, caller_id: state.operator });
    const data = await request(`/v1/platforms?${query}`);
    if (generation !== platforms.generation) return;
    platforms.items = data.platforms; renderPlatforms();
    $("platforms-load-error").hidden = true;
    if (!$("platforms-list-view").hidden) connectionFeedback();
  } catch {
    if (generation === platforms.generation) {
      $("platforms-empty").hidden = true;
      $("platforms-load-error").hidden = false;
    }
  } finally {
    if (generation === platforms.generation) {
      $("platforms-list-view").setAttribute("aria-busy", "false");
      $("platforms-retry").disabled = false;
      $("platforms-retry").textContent = "重试";
    }
  }
}
function showPlatformList() {
  clearLarkConfirmation();
  clearScionConfirmation();
  $("connection-scion-pat").value="";
  clearGitLabConfirmation();
  platforms.formVersion++; platforms.discovery = null; platforms.detecting = false; clearLarkAuthView(); resetLarkSetup(); closeLarkControls(); platforms.larkDiscoveryError = null;
  onboarding.active=false;renderOnboarding();
  platforms.selected = null; platforms.gitlabSetup=null;platforms.gitlabEditingImportID=null;$("gitlab-client-secret").value = "";
  settingsTab = "platforms"; renderSettingsNavigation();
  $("connection-form").hidden = true; $("platforms-list-view").hidden = !state.managed;
  $("platforms-load-error").hidden = true;
  connectionFeedback();
}
function editPlatform(item = null) {
  if(!$("platforms-list-view").hidden){platforms.listReturn={scroll:$("settings-panels").scrollTop,id:item?.id};$("settings-panels").scrollTop=0;}
  settingsTab = "platforms"; renderSettingsNavigation();
  clearLarkConfirmation();
  clearScionConfirmation();
  $("connection-scion-pat").value=""; platforms.scionDiscovery=null;
  $("connection-scion-login-panel").hidden=true;
  $("connection-scion-advanced").open=false;
  clearGitLabConfirmation();
  platforms.formVersion++; platforms.discovery = null; platforms.detecting = false; clearLarkAuthView(); resetLarkSetup(); closeLarkControls(); platforms.larkDiscoveryError = null;
  clearPlatformErrors();
  platforms.selected = item;onboarding.active=false;onboarding.fixed=null;platforms.gitlabApplicationID="";
  renderOnboarding();
  gitlabEditingApplication=false;
  gitlabSetupFeedback();
  platforms.gitlabSetup=null;platforms.gitlabEditingImportID=null;
  $("gitlab-compatibility-details").open=false;
  $("gitlab-advanced").open=!item?.authorization_status;
  setGitLabScopes(); $("gitlab-setup").hidden = true; $("gitlab-client-id").value = ""; $("gitlab-client-secret").value = "";
  $("connection-form").hidden = false; $("platforms-list-view").hidden = true;
  $("connection-title").textContent = item ? "平台详情" : "添加平台";
  $("connection-name").value = item?.name || "";
  setSelectOptions("connection-provider",[new Option("GitLab","gitlab"),new Option("Scion","scion"),new Option("飞书","feishu"),new Option("Lark","lark")],item?.provider === "lark" ? item.cli_profile?.brand || "feishu" : item?.provider || "gitlab");
  $("connection-provider").disabled = !!item;
  $("connection-url").value = item?.url || ""; $("connection-url").readOnly = !!item;
  setSelectOptions("connection-profile",[new Option("", item?.cli_profile?.profile || "")],item?.cli_profile?.profile || "");
  $("connection-executable").value = item?.cli_profile?.executable || "";
  $("connection-scion-executable").value = item?.provider === "scion" ? item.cli_profile.executable : "";
  setSelectOptions("connection-scion-profile",[new Option("", item?.provider === "scion" ? item.cli_profile.profile : "")],item?.provider === "scion" ? item.cli_profile.profile : "");
  $("connection-advanced").open = false;
  $("connection-sending").open = false;
  $("connection-setup-new").checked = true;
  $("connection-setup-existing").checked = false;
  $("connection-setup-copy").textContent = "复制命令";
  $("connection-setup-copy-status").textContent = "";
  $("connection-notes").hidden = !item;
  const v = item?.validation;
  $("connection-validation").textContent = v?.status === "valid" ? `已验证 · @${v.username}`
    : v?.status === "failed" ? connectionValidationErrors[v.error_code] || "验证失败。" : "尚未验证连接。";
  bindSystemTime($("connection-validation"), () => { $("connection-validation").title = [v?.user_id ? `账号 ID ${v.user_id}` : "", v?.checked_at ? `验证于 ${formatDate(v.checked_at)}` : ""].filter(Boolean).join(" · "); });
  renderPlatformReferences();
  $("connection-delete").disabled = !!item?.references?.length;
  for (const id of ["connection-delete", "connection-verify"]) $(id).hidden = !item;
  connectionFeedback(); connectionAuthFields();
  $("connection-fields").disabled = false; $("connection-name").focus();
  for (const id of ["connection-save", "connection-verify"]) $(id).disabled = false;
  renderOnboarding();
  if (connectionIsLark()) return detectLarkPlatform();
  if (connectionIsScion()) return detectScionPlatform();
  renderGitLabAccount();
  return discoverGitLabSetup();
}
function connectionDirty() {
 const item=platforms.selected;
 return !item || $("connection-name").value.trim()!==item.name || (item.provider==="lark" && JSON.stringify(larkProfileDraft())!==JSON.stringify(item.cli_profile)) || (item?.provider==="scion" && JSON.stringify(scionProfileDraft())!==JSON.stringify(item.cli_profile));
}
async function connectionAction(action) {
  if (platforms.sending || platforms.auth || platforms.larkPending || platforms.gitlabAuth || platforms.gitlabPending || platforms.scionAuth || platforms.scionPending || platforms.detecting || !state.catalog) return;
  const item = platforms.selected;
  if (action==="save" && item && onboarding.active) {
    onboarding.busy=true;platforms.sending=true;$("connection-fields").disabled=true;$("close-settings").disabled=true;$("connection-back").disabled=true;renderOnboarding();
    try {
      const version=platforms.formVersion,identity=onboardingIdentity(),op=await prepareOnboarding();
      const applied=await request("/v1/platforms/onboarding/apply",connectionPayload({operation_id:op.operation_id}),undefined,50000);
      if(onboardingCurrent(version,identity)){acceptOnboarding(applied);await loadPlatforms();if(workspaceView)await syncGitLabNotifications(state.catalog,workspaceView);}
    }
    catch(error) {onboarding.error=error.code ? "启用结果未确认，请读取进度。" : error.message;renderOnboarding();}
    finally {onboarding.busy=false;platforms.sending=false;$("connection-fields").disabled=false;$("close-settings").disabled=false;$("connection-back").disabled=false;renderOnboarding();}return;
  }
  if (connectionIsLark() && platforms.larkSetupResult && platforms.larkSetupResult.status !== "succeeded") {connectionFeedback("请先核对应用并完成授权，再保存连接。", "error");return;}
  if (action === "save" && item?.provider === "gitlab" && !connectionDirty()) return;
  if (action === "save" && !connectionIsLark() && !connectionIsScion() && !item) return startGitLabAuth("login");
  if (action !== "save" && !item) return;
  if (action === "verify" && connectionDirty()) { connectionFeedback("请先保存更改，再验证连接。", "error"); return; }
  if (action === "delete" && !window.confirm(`删除「${item.name}」连接？`)) return;
  let payload = { id: item?.id || crypto.randomUUID().replaceAll("-", ""), version: item?.version || 0 };
  if (action === "save") {
    clearPlatformErrors();
    payload = { ...payload, name: $("connection-name").value.trim(), provider: connectionIsLark() ? "lark" : connectionIsScion() ? "scion" : "gitlab", url: connectionIsLark() ? connectionOrigin() : $("connection-url").value.trim() };
    if (payload.provider === "scion") {
      if ($("connection-scion-pat").value) { connectionFeedback("尚有未提交的 PAT，请先点击「登录」，再保存连接。", "error"); return; }
      const profile = scionProfileDraft();
      if (!platforms.scionDiscovery?.profiles.some(p=>p.profile===profile.profile) || platforms.scionDiscovery.executable!==profile.executable || platforms.scionDiscovery.origin!==payload.url) { connectionFeedback("连接未保存：请刷新账号并选择可用账号，或先使用 PAT 登录。", "error"); return; }
      if (!item || JSON.stringify(profile) !== JSON.stringify(item.cli_profile)) payload.cli_profile = profile;
    }
    if (payload.provider === "lark") {
      const profileName = $("connection-profile").value.trim();
      if (!profileName || !platforms.discovery?.profiles.some(p => p.name === profileName)) { connectionFeedback("连接未保存：请先选择或连接应用。", "error"); return; }
      if (!/^[A-Za-z0-9_.-]{1,128}$/.test(profileName) || profileName === "." || profileName === "..") {
        connectionInputError("connection-profile", "连接未保存：应用配置名称无效，请点击「重新检测」。", "error"); return;
      }
      const executable = $("connection-executable").value.trim();
      if (!executable.startsWith("/") || /[\x00\r\n]/.test(executable)) {
        connectionInputError("connection-executable", "连接未保存：请填写 lark-cli 可执行文件的绝对路径，例如 /opt/homebrew/bin/lark-cli。"); return;
      }
      const profile = larkProfileDraft();
      if (!item || JSON.stringify(profile) !== JSON.stringify(item.cli_profile)) payload.cli_profile = profile;
    }
  }
  if (payload.cli_profile && item?.references?.length) {
    if (!window.confirm(`替换「${item.name}」账号配置？影响房间：${item.references.map(ref=>ref.name).join("、")}。在途工具将结清，请重新验证连接并核对绑定。`)) return;
    payload.referenced_rooms=item.references.map(({room_id,room_instance})=>({room_id,room_instance}));
  }
  platforms.sending = true; $("connection-fields").disabled = true;
  $("connection-back").disabled = true; $("close-settings").disabled = true;
  connectionFeedback(action === "verify" ? "正在验证…" : action === "delete" ? "正在删除…" : "正在保存…");
  let savedResult = null;
  try {
    const op=action==="save" && onboarding.active ? await prepareOnboarding() : null;
    if (op) payload={...payload,id:op.platform_id,url:op.input.origin,onboarding_id:op.operation_id};
    let result = await request(`/v1/platforms/${action}`, connectionPayload(payload), undefined, 40000);
    if (result.onboarding) acceptOnboarding(result.onboarding);
    if (action === "delete") { showPlatformList(); await loadPlatforms(); connectionFeedback("连接已删除。", "success"); }
    else if (action === "save") {
      savedResult = result;
      updatePlatformItem(result);
      if (["lark", "scion"].includes(result.provider) && result.validation.status !== "valid") {
        connectionFeedback("连接已保存，正在验证账号…");
        result = await request("/v1/platforms/verify", connectionPayload({ id: result.id, version: result.version }), undefined, 40000);
        savedResult = result; updatePlatformItem(result);
        if (result.validation.status !== "valid") {
          await editPlatform(result);
          connectionFeedback(`连接已保存，但验证失败：${connectionValidationErrors[result.validation.error_code] || "请核对账号授权后重试。"}`, "error");
          return;
        }
      }
      // Use the confirmed save response immediately; an earlier list read must
      // not replace it with an older version after returning to the list.
      platforms.generation++;
      let index = platforms.items.findIndex(item => item.id === result.id);
      if (index < 0) { index = platforms.items.length; platforms.items.push(result); }
      else platforms.items[index] = result;
      showPlatformList(); renderPlatforms();
      $("platforms-list-view").setAttribute("aria-busy", "false");
      connectionFeedback(["lark", "scion"].includes(result.provider) ? "连接已保存，账号验证成功。" : "名称已保存。", "success");
      $("platforms-list").children[index].focus();
    } else { updatePlatformItem(result); await editPlatform(result); connectionFeedback(result.validation.status === "valid" ? "连接验证成功。" : connectionValidationErrors[result.validation.error_code] || "验证失败。", result.validation.status === "valid" ? "success" : "error"); }
  } catch (error) {
    // Reconcile only by explicit list refresh. Never resend a secret request.
    const errors = { conflict: "连接已变化，请返回列表重新加载后再编辑。", request: payload.provider === "lark" ? "连接未保存：请检查连接名称及自动检测的 CLI 配置。" : "连接未保存：请检查连接名称、HTTPS 实例地址及认证来源。", identity: "服务身份已变化，请刷新页面后重试。", capacity: "平台数量已达上限。", busy: "操作尚未结束，请稍后返回列表核对。" };
    if (savedResult) { await editPlatform(savedResult); connectionFeedback("连接已保存，验证结果未确认。请重新验证。", "error"); }
    else connectionFeedback(errors[error.code] || "操作结果未确认，请返回列表核对。", "error");
  } finally {

    platforms.sending = false; renderSettingsNavigation(); if (!connectionIsLark() && !connectionIsScion()) renderGitLabSave(); $("connection-fields").disabled = false;
    $("connection-back").disabled = false; $("close-settings").disabled = false;
  }
}
async function openSettings(tab = settingsTab) {
  notificationNavigation++;
  if (state.managed ? !state.catalog : !state.info) return;
  showPlatformList(); settingsTab = state.managed ? tab : "general";
  timezonePicker.open = false; renderTimeZone(); $("settings-dialog").showModal(); if(state.managed) {await loadPlatforms();await restoreOnboarding();}
}
$("open-settings").addEventListener("click", () => openSettings());
$("open-general-settings").addEventListener("click", () => openSettings());
async function closeSettings() {
  if ($("close-settings").disabled) return;
  await cancelPlatformAuth(); await onboardingAction("status",{discardUnconnected:true}); $("settings-dialog").close();
}
$("close-settings").addEventListener("click", closeSettings);
dismissOnBackdrop($("settings-dialog"), closeSettings);
$("settings-dialog").addEventListener("cancel", event => { if (platforms.sending) event.preventDefault(); });
$("settings-dialog").addEventListener("close", () => { timezonePicker.open = false; renderTimeZoneOptions(); void cancelPlatformAuth(); $("gitlab-client-secret").value = ""; platforms.formVersion++; platforms.generation++;
  if ($("notification-detail").open) { $(workspaceView?.notificationChannel === "messages" ? "lark-manage-platforms" : "gitlab-manage-platforms").focus(); void loadWorkspacePlatforms(); }
  else if($("activity-dialog").open){$("member-notification-add-toggle").focus({preventScroll:true});void loadWorkspacePlatforms();if(memberToolsView && toolsCurrent(memberToolsView))void loadCLITools(memberToolsView);}
  else $(state.managed ? "open-settings" : "open-general-settings").focus(); });
$("connection-new").addEventListener("click",async()=>{
  if (onboarding.pending && !onboardingDone()) {onboarding.error="请先继续上次接入，或停止剩余步骤。";renderOnboarding();return;}
  clearOnboarding();await editPlatform();await startOnboardingOptions();
});
$("connection-name").addEventListener("input",()=>{if (connectionIsLark()) renderLarkAccount();else if (!connectionIsScion()) renderGitLabSave();});
$("connection-back").addEventListener("click", async () => { if (platforms.auth || platforms.gitlabAuth || platforms.scionAuth) await cancelPlatformAuth(); if (onboarding.pending) await onboardingAction("status",{discardUnconnected:true}); showPlatformList(); await loadPlatforms();
  if(platforms.listReturn && $("settings-dialog").open && settingsTab==="platforms" && $("connection-form").hidden){const back=platforms.listReturn;([...$("platforms-list").children].find(row=>row.dataset.platformId===back.id) || $("connection-new")).focus({preventScroll:true});$("settings-panels").scrollTop=back.scroll;}
});
$("platforms-retry").addEventListener("click", loadPlatforms);
$("connection-provider").addEventListener("change", async () => {
  resetLarkSetup(); closeLarkControls(); platforms.larkDiscoveryError = null;
  platforms.gitlabApplicationID="";platforms.gitlabSetup=null;platforms.gitlabEditingImportID=null;$("gitlab-setup").hidden=true;
  clearLarkConfirmation();
  clearScionConfirmation();
  if (platforms.scionAuth) await cancelScionAuth();
  $("connection-scion-pat").value=""; platforms.scionDiscovery=null;
  setSelectOptions("connection-scion-profile",[],"");
  $("connection-scion-login-panel").hidden=true;
  $("connection-scion-advanced").open=false;
  platforms.formVersion++; platforms.discovery = null; platforms.detecting = false;
  setSelectOptions("connection-profile",[],"");
  $("connection-setup-new").checked = true;
  $("connection-setup-existing").checked = false;
  if (!platforms.selected) {
    $("connection-url").value = connectionIsLark() ? connectionOrigin() : "";
    if (!$("connection-name").value.trim() || ["飞书", "Lark"].includes($("connection-name").value)) $("connection-name").value = connectionIsLark() ? connectionBrand() === "lark" ? "Lark" : "飞书" : "";
  }
  connectionAuthFields();
  renderOnboarding();
  if (connectionIsLark()) return detectLarkPlatform();
  if (connectionIsScion()) return detectScionPlatform();
  renderGitLabAccount();
});
$("connection-scion-executable").addEventListener("change", detectScionPlatform);
$("connection-url").addEventListener("input", renderScionPATLink);
$("connection-url").addEventListener("change", () => { platforms.gitlabApplicationID="";renderScionPATLink(); if(connectionIsScion()) return detectScionPlatform(); return discoverGitLabSetup(); });
$("connection-scion-detect").addEventListener("click", detectScionPlatform);
$("connection-scion-profile").addEventListener("change", () => {clearScionConfirmation();connectionFeedback();renderScionAccount();});
$("connection-scion-login").addEventListener("click", startScionLogin);
$("connection-scion-login-open").addEventListener("click", () => {
  if (platforms.scionAuth || platforms.scionPending || platforms.sending || platforms.detecting) return;
  $("connection-scion-login-panel").hidden=false;
  renderScionAccount();$("connection-scion-pat").focus();
});
$("connection-scion-login-dismiss").addEventListener("click", () => {
  if (platforms.scionAuth || platforms.sending) return;
  $("connection-scion-pat").value="";$("connection-scion-login-panel").hidden=true;
  renderScionAccount();$("connection-scion-login-open").focus();
});
$("connection-scion-logout").addEventListener("click", confirmScionLogout);
$("connection-scion-pat").addEventListener("keydown", event => { if(event.key==="Enter" && !event.isComposing) { event.preventDefault(); if(!event.repeat) return startScionLogin(); } });
$("connection-scion-cancel").addEventListener("click", async () => {await cancelScionAuth();await detectScionPlatform();});
$("connection-profile").addEventListener("change", () => {clearLarkConfirmation();resetLarkSetup();closeLarkControls();connectionFeedback();renderLarkAccount();$("connection-application-change").focus();});
$("connection-detect").addEventListener("click", detectLarkPlatform);
$("connection-recheck").addEventListener("click", detectLarkPlatform);
$("connection-setup-start").addEventListener("click", startLarkSetup);
$("connection-setup-other").addEventListener("click", startLarkSetup);
$("connection-application-change").addEventListener("click", () => {
  if (platforms.auth || platforms.larkStarting || platforms.larkPending || platforms.detecting || platforms.sending) return;
  const open = $("connection-application-picker").hidden;
  $("connection-application-picker").hidden = !open;
  $("connection-application-change").setAttribute("aria-expanded", String(open));
  $("connection-account-manage").open = false;
  if (open) $("connection-profile").focus();
});
$("connection-application-dismiss").addEventListener("click", () => {closeLarkControls();$("connection-application-change").focus();});
$("connection-application-picker").addEventListener("keydown", event => {if(event.key === "Escape") {event.preventDefault();event.stopPropagation();closeLarkControls();$("connection-application-change").focus();}});
$("connection-account-manage").addEventListener("toggle", () => {if ($("connection-account-manage").open) {$("connection-application-picker").hidden = true;$("connection-application-change").setAttribute("aria-expanded", "false");}});
$("connection-account-manage").addEventListener("keydown", event => {if (event.key === "Escape") {event.preventDefault();event.stopPropagation();$("connection-account-manage").open = false;$("connection-account-manage").querySelector("summary").focus();}});
document.addEventListener("pointerdown", event => {if ($("connection-account-manage").open && !event.target.closest?.("#connection-account-manage")) $("connection-account-manage").open = false;});
$("connection-reauthorize").addEventListener("click", () => startLarkAuth("login"));
$("connection-account-reverify").addEventListener("click", () => {closeLarkControls();return connectionAction("verify");});

for (const id of ["connection-setup-new", "connection-setup-existing"]) $(id).addEventListener("change", renderLarkSetupCommand);
$("connection-setup-copy").addEventListener("click", async () => {
  const command = $("connection-setup-command").textContent, version = platforms.formVersion;
  try {
    if (!window.navigator?.clipboard?.writeText) throw new Error("Clipboard unavailable");
    await window.navigator.clipboard.writeText(command);
    if (version !== platforms.formVersion || command !== $("connection-setup-command").textContent) return;
    $("connection-setup-copy").textContent = "已复制";
    $("connection-setup-copy-status").textContent = "";
  } catch {
    if (version !== platforms.formVersion || command !== $("connection-setup-command").textContent) return;
    $("connection-setup-copy-status").textContent = "无法自动复制，请选中上方命令复制。";
  }
});
$("connection-executable").addEventListener("change", detectLarkPlatform);
$("connection-login").addEventListener("click", () => startLarkAuth("login"));
$("connection-target-authorize").addEventListener("click",()=>startLarkAuth("authorize-targets"));
$("connection-send-authorize").addEventListener("click",()=>startLarkAuth("authorize-sending"));
$("connection-send-check").addEventListener("click",async()=>{
  const version=platforms.formVersion;
  $("connection-send-check").disabled=true;
  $("connection-send-status").textContent="正在检查发送权限…";
  try {
    const state=await request("/v1/platforms/lark/sending-status",connectionPayload({cli_profile:larkProfileDraft()}),undefined,65000);
    if(version!==platforms.formVersion)return;
    const available = [state.reply_enabled && "普通回复", state.group_send_enabled && "群消息", state.self_send_enabled && "本人自聊", state.user_send_enabled && "主动私信"].filter(Boolean);
    const missing = [!state.account_fixed && "重新验证账号", state.application_scopes !== "granted" && "开通应用发送权限", state.user_scopes !== "granted" && "完成用户发送授权"].filter(Boolean);
    $("connection-send-status").textContent = missing.length ? `发送条件未齐备，请${missing.join("、")}。` : available.length ? `可用：${available.join("、")}。具体可用能力以成员平台设置为准。${state.group_send_enabled && !state.user_send_enabled ? "主动私信暂未开放。" : ""}` : "发送暂不可用，请检查账号与权限。";
  }catch(error){if(version===platforms.formVersion)$("connection-send-status").textContent="发送条件检查失败，请检查账号与权限。";}
  finally{$("connection-send-check").disabled=false;}
});
$("connection-logout").addEventListener("click", () => startLarkAuth("logout"));
$("connection-login-cancel").addEventListener("click", async () => {
  const setup = platforms.auth?.action === "setup";
  const cancelVersion = platforms.formVersion + 1;
  const result = await cancelLarkAuth();
  if (cancelVersion !== platforms.formVersion) return;
  const detecting = detectLarkPlatform(setup ? {profile:result?.cli_profile?.profile || "",exact:true,preserveSetup:true} : {}), version = platforms.formVersion;
  await detecting;
  if (version !== platforms.formVersion) return;
  if (setup && result?.status === "succeeded") {
    const option = platforms.discovery?.profiles.find(p => p.name === $("connection-profile").value);
    if (!result.cli_profile || option?.name !== result.cli_profile.profile || option?.user !== result.username) {
      rememberLarkSetup({...result,status:"failed",stage:"application",error_code:"identity_mismatch"});renderLarkAccount();
      connectionFeedback("流程已结束，核验结果未确认。请重新检测。", "error");return;
    }
    platforms.larkSetupUncertain = false;$("connection-advanced").open = false;renderLarkAccount();
    connectionFeedback("取消前授权已完成，请核对账号并保存连接。", "success");return;
  }
  connectionFeedback(setup ? result ? larkSetupFailure(result) : "取消结果未确认，请重新检测。" : "已请求取消，请重新检测。", setup ? "error" : "status", "取消只停止等待，不撤销已完成的应用配置或账号授权。");
});
function larkProfileDraft() {return {version:1, executable:$("connection-executable").value.trim(), profile:$("connection-profile").value.trim(), brand:connectionBrand(), cli_version:"1.0.99"};}
window.addEventListener("pagehide", () => { $("gitlab-client-secret").value = ""; void cancelGitLabAuth(true); void cancelLarkAuth(true); });
$("connection-form").addEventListener("submit", event => {
  event.preventDefault();
  if (!connectionIsLark() && !connectionIsScion() && (!platforms.selected || !connectionDirty() && $("gitlab-advanced").open && $("gitlab-client-secret").value)) return configureGitLabPlatform();
  return connectionAction("save");
});
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
