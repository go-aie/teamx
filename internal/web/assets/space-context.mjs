// A document has one immutable Space. Switching navigates to a new document,
// disposing its requests, caches and streams while server work keeps running.
export const pageSpaceID = /^\/s\/([a-f0-9]{32})\//.exec(globalThis.location?.pathname || "")?.[1] || "";
export function spaceURL(path) {
  const id=pageSpaceID || resolvedSpaceID;
  if (!id || typeof path !== "string" || !path.startsWith("/") || path.startsWith("//") || path.startsWith("/s/") || path.startsWith("/ui/")) return path;
  return `/s/${id}${path}`;
}
let defaultSpaceID = "", resolvedSpaceID = pageSpaceID, pageResetID, cacheWritable = true;
const resetMarker = id => `teamx.space-reset:${id}`;
const businessKey = key => key.startsWith("teamx.") && !key.startsWith("teamx.space:") && !key.startsWith("teamx.space-reset:") && !key.startsWith("teamx.space-reset-pending:") && !key.startsWith("teamx.space-create:") || /^\["teamx\./.test(key);
function stores() { const result=[]; for (const name of ["localStorage","sessionStorage"]) { try { if (globalThis[name]) result.push(globalThis[name]); } catch {} } return result; }
function clearSpaceStorage(storage, id, resetID, expectedOld) {
  const marker=resetMarker(id), prefix=`teamx.space:${id}:`;
  const previous=storage.getItem(marker);
  if (previous === resetID || expectedOld !== undefined && previous != null && previous !== expectedOld) return;
  if (resetID || previous) {
    const keys=[]; for(let i=0;i<storage.length;i++) keys.push(storage.key(i));
    for(const key of keys) if (key?.startsWith(prefix) || id === defaultSpaceID && key && businessKey(key)) storage.removeItem(key);
  }
  storage.setItem(marker,resetID);
}
export function spaceCacheCurrent() {
  if (!cacheWritable) return false;
  if (pageResetID !== undefined && resolvedSpaceID) {
    try { const shared=globalThis.localStorage?.getItem(resetMarker(resolvedSpaceID)); if (shared != null && shared !== pageResetID) { freezeSpaceCache(); return false; } } catch {}
  }
  return true;
}
export function freezeSpaceCache() {
  if (!cacheWritable) return;
  cacheWritable=false;
  if (typeof globalThis.dispatchEvent === "function" && typeof globalThis.Event === "function") globalThis.dispatchEvent(new Event("teamx:space-reset"));
}
export function observeSpaceReset(space) {
  if (!space?.id && !space?.space_id) return true;
  const id=space.id || space.space_id, next=space.reset?.request_id || space.reset_request_id || "";
  if (resolvedSpaceID && resolvedSpaceID!==id) return false;
  resolvedSpaceID=id;
  if (pageResetID !== undefined && pageResetID !== next) {
    freezeSpaceCache();
    // A newer tab may already have initialized new caches. Only clear stores
    // whose own marker is old, never the fresh data written by that tab.
    for (const storage of stores()) try { clearSpaceStorage(storage,id,next,pageResetID); } catch {}
    globalThis.location?.replace?.(`/s/${id}/`);
    return false;
  }
  if (pageResetID === undefined) {
    cacheWritable=false;
    for (const storage of stores()) try { clearSpaceStorage(storage,id,next); } catch {}
    pageResetID=next;cacheWritable=true;
  }
  return spaceCacheCurrent();
}
export function spaceStorage(storage) {
  if (!storage) return storage;
  const key = value => `teamx.space:${resolvedSpaceID}:${value}`;
  return {
    getItem(value) {
      if (!spaceCacheCurrent() || !resolvedSpaceID) return null;
      let saved = storage.getItem(key(value));
      if (saved == null && resolvedSpaceID === defaultSpaceID && !pageResetID) {
        saved = storage.getItem(value);
        if (saved != null) storage.setItem(key(value), saved);
      }
      return saved;
    },
    setItem(value, data) { if (spaceCacheCurrent() && resolvedSpaceID) storage.setItem(key(value), data); },
    removeItem(value) { if (!spaceCacheCurrent() || !resolvedSpaceID) return; storage.removeItem(key(value)); if (resolvedSpaceID === defaultSpaceID && !pageResetID) storage.removeItem(value); },
  };
}

let picker, capabilities = {};
export function spaceCapabilities() { return capabilities; }
export function spaceAfterArchive(directory, archivedID) {
  const candidates = (directory.spaces || []).filter(space => space.id !== archivedID && space.status === "active" && (!space.reset || space.reset.phase === "completed"));
  for (const states of [["ready"], ["setup_required", "migration_required"], ["starting"]]) {
    const available = candidates.filter(space => states.includes(space.runtime_state));
    const next = available.find(space => space.id === directory.default_space_id) || available[0];
    if (next) return next.id;
  }
  return "";
}
export async function mountSpacePicker() {
  if (picker) return;
  let result;
  try { const response = await fetch("/v1/spaces", {cache:"no-store", signal:globalThis.AbortSignal?.timeout?.(5000)}); if (!response.ok) return; result = await response.json(); } catch { return; }
  defaultSpaceID = result.default_space_id || "";
  const id = pageSpaceID || result.default_space_id;
  const current = result.spaces?.find(space => space.id === id);
  if (!current) return;
  if (!observeSpaceReset(current)) return;
  capabilities = current.capabilities || {};
  for (const brand of document.querySelectorAll('a.brand[href="/"]')) brand.href = spaceURL("/");
  // Effective capability declarations come from the server, including builds
  // whose available platform catalog differs from the standard distribution.
  if (capabilities.computer === false) {
    const tab = document.getElementById("settings-tab-computer"); if (tab) { tab.disabled = true; tab.title = "当前会话不支持电脑"; }
  }
  if (capabilities.space_management !== true || result.can_create !== true || picker) return;
  picker = document.createElement("div"); picker.className = "space-picker";
  const select = document.createElement("select"); select.id = "space-select"; select.setAttribute("aria-label", "切换空间");
  for (const space of result.spaces) select.add(new Option(space.name + ({suspended:" · 已归档", archiving:" · 归档中", failed:" · 启动失败"}[space.runtime_state] || ""), space.id));
  select.value = id;
  select.add(new Option("＋ 新建空间…", "create"));
  const feedback = document.createElement("span"); feedback.className = "space-feedback"; feedback.setAttribute("role", "status");
  const button = document.createElement("button"); button.type = "button"; button.textContent = "空间设置";
  button.setAttribute("aria-haspopup", "dialog");
  picker.append(select, button, feedback);
  const host = document.querySelector(".room-rail-header") || document.querySelector("main"); host?.append(picker);

  function makeDialog(heading, key, submitText, withName = true) {
    const dialog = document.createElement("dialog"); dialog.className = "space-dialog"; dialog.setAttribute("aria-labelledby", `${key}-title`);
    const form = document.createElement("form");
    const title = document.createElement("h2"); title.id = `${key}-title`; title.textContent = heading;
    const label = document.createElement("label"); label.textContent = "空间名称";
    const name = document.createElement("input"); name.id = key; name.maxLength = 64; name.required = withName; name.className = "field-control"; label.append(name);
    label.hidden = !withName;
    const hint = document.createElement("p"); hint.className = "space-hint";
    const message = document.createElement("p"); message.className = "space-error"; message.setAttribute("role", "status"); message.setAttribute("aria-live", "polite");
    const cancel = document.createElement("button"); cancel.type = "button"; cancel.className = "secondary-button"; cancel.textContent = "取消";
    const submit = document.createElement("button"); submit.type = "submit"; submit.className = "primary"; submit.textContent = submitText;
    const actions = document.createElement("div"); actions.className = "space-actions"; actions.append(cancel, submit);
    form.append(title, label, hint, message, actions); dialog.append(form); document.body.append(dialog);
    cancel.addEventListener("click", () => dialog.close());
    dialog.addEventListener("cancel", event => { if (sending) event.preventDefault(); });
    return {dialog, form, title, name, hint, message, cancel, submit, actions};
  }
  const settings = makeDialog("空间设置", "space-name", "保存");
  const creation = makeDialog("新建空间", "space-create-name", "创建并进入");
  creation.name.placeholder = "例如：工作、个人项目";
  creation.hint.textContent = "优先沿用当前空间默认模型并自动验证，后续可独立修改。连接需单独设置；飞书和 Scion 仅默认空间可用。";
  const recovery = makeDialog("继续上次创建", "space-create-recovery", "核对并继续", false);
  const lifecycle = makeDialog("归档空间", "space-lifecycle", "确认归档", false);
  const more = document.createElement("details"); more.className = "space-more";
  const summary = document.createElement("summary"); summary.textContent = "更多操作";
  const lifecycleButton = document.createElement("button"); lifecycleButton.id = "space-lifecycle-action"; lifecycleButton.type = "button"; lifecycleButton.className = "text-button danger-button";
  more.append(summary, lifecycleButton); settings.form.append(more);
  function updateLifecycle() {
    lifecycleButton.textContent = current.status === "suspended" ? "恢复空间…" : current.status === "archiving" ? "重试归档…" : "归档空间…";
    lifecycleButton.disabled = current.lifecycle_busy === true;
  }
  updateLifecycle();
  let sending = false;
  function busy(value) {
    sending = value;
    for (const ui of [settings, creation, recovery, lifecycle]) ui.cancel.disabled = ui.submit.disabled = value;
    button.disabled = select.disabled = value;
    lifecycleButton.disabled = value || current.lifecycle_busy === true;
  }
  function failure(record, fallback) {
    const code = record.code || record.error || record.message;
    return ({directory_conflict:"新空间的数据目录与已有工作目录重叠，暂时无法创建。请调整已使用的工作目录后重试。",
      conflict:"空间信息已变化，请重新打开设置后再试。", busy:"空间正在处理其他操作，请稍后重试。",
      identity:"当前登录已失效，请刷新页面后重试。", request:"请输入有效的空间名称，最多 64 个字。",
      commit_unknown:"操作结果尚未确认，请刷新核对后重试。"})[code] || fallback;
  }
  // Preserve an uncertain creation's exact ID and body so retrying cannot duplicate it.
  let pending;
  const pendingKey = `teamx.space-create:${location.origin}:${id}`;
  const forgetPending = () => { pending = undefined; try { sessionStorage.removeItem(pendingKey); } catch {} };
  try {
    const saved = JSON.parse(sessionStorage.getItem(pendingKey));
    if (/^[a-f0-9]{32}$/.test(saved?.request_id) && typeof saved.name === "string" && saved.name.trim() && saved.name.length <= 256 && (!saved.model_source_space_id || /^[a-f0-9]{32}$/.test(saved.model_source_space_id))) pending = saved;
    if (result.spaces.some(space => space.id === pending?.request_id)) forgetPending();
  } catch { /* Creation remains usable when browser storage is unavailable. */ }
  function showCreation() {
    if (pending) {
      recovery.hint.textContent = `上次请求创建「${pending.name}」。继续后会核对结果并进入该空间。`;
      recovery.message.textContent = ""; recovery.dialog.showModal();
    } else {
      creation.name.value = ""; creation.message.textContent = ""; creation.dialog.showModal();
    }
  }
  select.addEventListener("change", () => {
    if (select.value === "create") {
      select.value = id;
      showCreation();
    } else if (select.value !== id) location.assign(`/s/${select.value}/`);
  });
  button.addEventListener("click", async () => {
    if (sending) return;
    button.disabled = true; feedback.textContent = "";
    try {
      const response = await fetch("/v1/spaces", {cache:"no-store", signal:globalThis.AbortSignal?.timeout?.(5000)});
      if (!response.ok) throw Error();
      const directory = await response.json(), latest = directory.spaces?.find(space => space.id === id);
      if (!latest) throw Error();
      if (!observeSpaceReset(latest)) return;
      Object.assign(current, latest); updateLifecycle();
      settings.name.value = current.name; settings.hint.textContent = "修改当前空间的名称。";
      settings.message.textContent = current.lifecycle_error ? "上次归档未完成，可在更多操作中重试。" : "";
      more.open = false; settings.dialog.showModal();
    } catch { feedback.textContent = "暂时无法读取空间信息，请重试。"; }
    finally { button.disabled = false; }
  });
  async function save(ui, creating) {
    if (sending) return;
    const value = creating && pending ? pending.name : ui.name.value.trim();
    if (!value) { ui.message.textContent = "请输入空间名称。"; ui.name.focus(); return; }
    const input = creating ? (pending ||= {request_id:crypto.randomUUID().replaceAll("-", ""), name:value, model_source_space_id:id}) : {name:value, expected_version:current.version};
    if (creating) { ui.name.value = input.name; try { sessionStorage.setItem(pendingKey, JSON.stringify(input)); } catch {} }
    busy(true); ui.message.textContent = "";
    let failureMessage;
    try {
      const response = await fetch(creating ? "/v1/spaces" : `/v1/spaces/${id}`, {method:"POST", headers:{"Content-Type":"application/json"}, body:JSON.stringify(input)});
      const record = await response.json();
      if (!response.ok) {
        if (creating && response.status >= 400 && response.status < 500 && record.code !== "conflict") forgetPending();
        failureMessage = failure(record, "空间未保存，请刷新核对后重试。");
        throw Error(failureMessage);
      }
      if (creating) { forgetPending(); location.assign(`/s/${record.id}/`); }
      else { Object.assign(current, record); select.selectedOptions[0].textContent = record.name; ui.dialog.close(); }
    } catch {
      if (creating && pending) {
        if (ui !== recovery) { ui.dialog.close(); showCreation(); }
        recovery.message.textContent = failureMessage || "暂时无法确认创建结果，请稍后重试。";
      } else if (ui === recovery) {
        ui.dialog.close(); showCreation(); creation.name.value = value;
        creation.message.textContent = failureMessage || "未能创建空间，请稍后重试。";
      } else ui.message.textContent = failureMessage || "暂时无法保存，请刷新核对后重试。";
    }
    finally { busy(false); }
  }
  settings.form.addEventListener("submit", event => { event.preventDefault(); void save(settings, false); });
  creation.form.addEventListener("submit", event => { event.preventDefault(); void save(creation, true); });
  recovery.form.addEventListener("submit", event => { event.preventDefault(); void save(recovery, true); });
  lifecycleButton.addEventListener("click", () => {
    settings.dialog.close();
    const restoring = current.status === "suspended";
    lifecycle.title.textContent = restoring ? "恢复空间" : "归档空间";
    lifecycle.submit.textContent = restoring ? "恢复并进入" : "确认归档";
    lifecycle.submit.className = restoring ? "primary" : "primary danger-button";
    lifecycle.hint.textContent = restoring ? `恢复「${current.name}」后，可继续使用原有会话与工作文件。` : `归档「${current.name}」将停止 Agent、通知采集和定时任务。会话与工作文件会保留，可稍后恢复。`;
    lifecycle.message.textContent = ""; lifecycle.dialog.showModal();
  });
  lifecycle.form.addEventListener("submit", async event => {
    event.preventDefault(); if (sending) return;
    busy(true); lifecycle.message.textContent = "";
    const action = current.status === "suspended" ? "restore" : "archive";
    try {
      const response = await fetch(`/v1/spaces/${id}/${action}`, {method:"POST", headers:{"Content-Type":"application/json"}, body:JSON.stringify({expected_version:current.version})});
      const record = await response.json();
      if (!response.ok) throw Error(failure(record, "操作未完成，请刷新核对空间状态后重试。"));
      if (action === "archive") {
        let next = "";
        try {
          const response = await fetch("/v1/spaces", {cache:"no-store", signal:globalThis.AbortSignal?.timeout?.(5000)});
          if (response.ok) next = spaceAfterArchive(await response.json(), id);
        } catch { /* The archived Space retains its picker and explicit recovery. */ }
        location.replace(`/s/${next || id}/`);
      } else location.assign(`/s/${id}/`);
    } catch (error) { lifecycle.message.textContent = error.message; }
    finally { busy(false); }
  });
}
