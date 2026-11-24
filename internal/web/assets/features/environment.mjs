import { spaceURL } from "../space-context.mjs";
import { captureReturnView } from "../view-return.mjs";
import { randomUUID } from "../random-id.mjs";

// Only an owned, settled handoff can switch this page to interactive mode.
export function createEnvironment({env, runtime, layoutUI, conversationToolsUI}) {
  const {document, window, request, setTimeout:delay, clearTimeout:clearDelay} = env, {$, state, rpc} = runtime;
  let opened = false, generation = 0, identity = null, returnView = null, controller = null;
  let environments = [], selected = "", frame = null, connected = false, interactive = false;
  let handoff = null, controlAvailable = false, busy = false, polling = null, timer = null, pending = null, feedback = "";
  // Each document owns a distinct lease, including duplicated browser tabs.
  const clientID = randomUUID().replaceAll("-", "");
  const context = () => JSON.stringify([state.installationID, state.operator, state.catalog?.service_epoch, state.info?.room_id, state.info?.room_instance]);
  const currentEnvironment = () => environments.find(item => item.id === selected);
  const controlURL = () => currentEnvironment()?.viewer_url.replace(/\/viewer$/, "/control");
  const holding = () => handoff?.owned && ["waiting", "ready", "blocked", "finishing"].includes(handoff.phase);
  function status(text) { $("environment-status").textContent = text; }
  function applyMode() {
    frame?.contentWindow?.postMessage?.({type:"teamx-environment-mode", interactive:controlAvailable && handoff?.owned && handoff.phase === "ready" && !busy,
      client_id:clientID, control_id:handoff?.id}, window.location?.origin);
  }
  function renderControl() {
    const phase = handoff?.phase, owned = handoff?.owned, button = $("environment-control");
    button.textContent = owned && phase === "ready" ? "完成接管" : owned && ["waiting", "blocked"].includes(phase) ? "取消接管" : owned && phase === "finishing" ? "重试完成" : "接管操作";
    button.disabled = busy || !controlAvailable || !connected && !holding() || !owned && ["waiting", "ready", "blocked", "finishing"].includes(phase);
    if (feedback) status(feedback);
    else if (owned && phase === "waiting") status("已暂停后续执行，正在等待当前回合结束…");
    else if (owned && phase === "blocked") status("当前执行未正常结束，暂不能操作。可取消接管后检查成员状态。");
    else if (owned && phase === "ready" && handoff.review) status("仅供人工核验：先前操作结果未知，交还后 Agent 保持暂停。请勿重复提交。");
    else if (owned && phase === "ready") status(interactive ? "正在接管 · 可直接输入，完成后交还 Agent" : "正在连接操作画面…");
    else if (owned && phase === "finishing") status("完成结果待确认，请点击重试完成。");
    else if (!owned && ["waiting", "ready", "blocked", "finishing"].includes(phase)) status("其他页面正在接管，当前仅观看。");
    else if (owned && phase === "completed" && handoff.resume_failed) status("接管已完成，但恢复 Agent 失败。成员保持暂停，请在成员设置检查后再启用。");
    else if (owned && phase === "completed") status(handoff.resumed ? "已交还 Agent，继续当前任务。" : "接管已完成，Agent 保持暂停，可在成员设置启用。");
    else if (owned && ["expired", "abandoned"].includes(phase)) status("接管已收回，Agent 保持暂停。");
    else if (connected) status("已连接 · 仅观看");
  }
  function stopPoll() { clearDelay(timer); timer = null; }
  function schedulePoll() {
    stopPoll();
    if (opened && !state.suspended && (holding() || pending || handoff && ["waiting", "ready", "blocked", "finishing"].includes(handoff.phase))) timer = delay(() => void inspectControl(), 2000);
  }
  function acceptControl(data) {
    handoff = data.control || null; controlAvailable = true;
    if (handoff?.owned && pending?.id === handoff.id && (pending.action !== "finish" || handoff.phase === "completed")) pending = null;
    applyMode(); renderControl(); schedulePoll();
  }
  async function inspectControl() {
    const url = controlURL(), key = context(), version = generation;
    if (!opened || !url || polling === version) return;
    polling = version;
    try {
      const data = await request(url+"?client_id="+clientID, undefined, undefined, 5000);
      if (opened && key === context() && version === generation) { feedback = ""; acceptControl(data); }
    } catch {
      if (opened && key === context() && version === generation) {
        controlAvailable = false; feedback = holding() ? "接管连接未确认，已停止输入，请重新连接核对。" : "暂时无法接管，请重新连接后重试。";
        applyMode(); renderControl(); schedulePoll();
      }
    } finally { if (polling === version) polling = null; }
  }
  async function operate(action) {
    const url = controlURL(), info = state.info, key = context();
    if (!url || busy) return;
    const id = pending?.action === action ? pending.id : action === "takeover" ? randomUUID().replaceAll("-", "") : handoff?.id;
    if (!id) return;
    pending = {action, id}; busy = true; feedback = action === "finish" ? "正在完成接管…" : action === "takeover" ? "正在暂停 Agent…" : "正在收回接管…";
    applyMode(); renderControl();
    try {
      const data = await rpc(url, {action, control_id:id, client_id:clientID}, info);
      if (opened && key === context() && url === controlURL()) { feedback = ""; pending = null; acceptControl(data); }
    } catch {
      if (opened && key === context() && url === controlURL()) { feedback = "操作结果尚未确认，请核对状态后重试。"; schedulePoll(); }
    } finally {
      busy = false;
      if (opened && key === context()) { applyMode(); renderControl(); void inspectControl(); }
    }
  }
  function abandon() {
    const url = controlURL(), id = handoff?.owned ? handoff.id : pending?.action === "takeover" ? pending.id : null;
    if (id && url && state.info) void rpc(url, {action:"abandon", control_id:id, client_id:clientID}, state.info).catch(() => {});
    handoff = null; pending = null; applyMode();
  }
  function disconnect() { generation++; polling = null; stopPoll(); controller?.abort(); controller = null; frame = null; connected = interactive = false; $("environment-screen").replaceChildren(); renderControl(); }
  function connect() {
    disconnect();
    const current = currentEnvironment();
    if (!current) return;
    if (!/^\/v1\/environment\/[a-f0-9]{64}\/viewer$/.test(current.viewer_url)) { status("电脑入口无效，请重新连接。"); return; }
    frame = document.createElement("iframe"); frame.title = `${current.label}的电脑画面`;
    frame.setAttribute("sandbox", "allow-scripts allow-same-origin"); frame.setAttribute("referrerpolicy", "no-referrer"); frame.src = spaceURL(current.viewer_url);
    $("environment-screen").append(frame); status("正在连接电脑…"); void inspectControl();
  }
  async function refresh() {
    disconnect(); feedback = ""; controlAvailable = false; renderControl(); status("正在读取电脑…"); $("environment-source-field").hidden = true;
    if (!state.managed || !state.info || !state.catalog) { status("此会话暂未配置可观看的电脑。请在全局设置连接电脑，并在成员设置中开启“启用电脑”。"); return; }
    const version = generation, key = context(); controller = new AbortController();
    const current = () => opened && generation === version && key === context();
    const q = new URLSearchParams({room_id:state.info.room_id, room_instance:state.info.room_instance, service_epoch:state.catalog.service_epoch, caller_id:state.operator});
    try {
      const data = await request("/v1/environments?"+q, undefined, undefined, 10000, controller.signal);
      if (!current()) return;
      environments = data.environments || [];
      const select = $("environment-source"); select.replaceChildren();
      for (const item of environments) { const option = document.createElement("option"); option.value = item.id; option.textContent = item.label; select.append(option); }
      $("environment-source-field").hidden = environments.length < 2;
      if (!environments.some(item => item.id === selected)) selected = environments[0]?.id || "";
      select.value = selected;
      if (!selected) { status("此会话暂未配置可观看的电脑。请在全局设置连接电脑，并在成员设置中开启“启用电脑”。"); return; }
      connect();
    } catch (error) { if (current()) status(error.code === "identity" ? "会话已变化，请重新打开电脑面板。" : "电脑暂不可用，请重新连接。"); }
  }
  function hide() { if (!opened) return; abandon(); disconnect(); opened = false; $("environment-page").hidden = true; }
  function close({focus = true} = {}) {
    if (!opened && !identity) return;
    hide(); identity = null; environments = []; selected = ""; layoutUI.closeAuxiliary("environment");
    if (focus) returnView?.restore($("room-menu-label")); returnView = null; runtime.navigationChanged();
  }
  function back() { if (layoutUI.auxiliaryExpanded) layoutUI.toggleAuxiliaryExpanded(); else close(); }
  async function open(opener = $("room-menu-label")) {
    if (opened) return;
    conversationToolsUI.close(); returnView = captureReturnView(env, runtime, {opener, restoreScroll:false});
    identity = context(); opened = true; $("environment-page").hidden = false; layoutUI.openAuxiliary("environment"); $("environment-close").focus({preventScroll:true}); await refresh();
  }
  function syncIdentity() { if (identity && identity !== context()) close({focus:false}); }
  function suspend() { if (opened) { abandon(); disconnect(); status("观看已暂停，返回页面后重新连接。"); } }
  function resume() { if (opened && !state.suspended) { syncIdentity(); if (opened) void refresh(); } }
  function mount() {
    $("environment-page").hidden = true; renderControl();
    $("environment-close").addEventListener("click", () => close());
    $("environment-refresh").addEventListener("click", () => void refresh());
    $("environment-expand").addEventListener("click", () => layoutUI.toggleAuxiliaryExpanded());
    $("environment-control").addEventListener("click", () => void operate(handoff?.owned && ["ready", "finishing"].includes(handoff.phase) ? "finish" : holding() ? "abandon" : "takeover"));
    $("environment-source").addEventListener("change", () => { abandon(); selected = $("environment-source").value; feedback = ""; connect(); });
    $("environment-page").addEventListener("keydown", event => { if (event.key === "Escape" && !event.defaultPrevented) { event.preventDefault(); back(); } });
    window.addEventListener("message", event => {
      if (!opened || !frame || event.source !== frame.contentWindow || event.origin !== window.location?.origin || event.data?.type !== "teamx-environment") return;
      connected = event.data.state === "connected"; interactive = connected && event.data.interactive === true;
      const messages = {connecting:"正在连接电脑…", disconnected:"连接已断开，请重新连接。", unavailable:"电脑暂不可用，请重新连接。"};
      if (messages[event.data.state]) status(messages[event.data.state]);
      if (connected) applyMode(); renderControl();
    });
  }
  return {mount, open, close, hide, back, syncIdentity, suspend, resume, get isOpen() { return opened; }};
}
