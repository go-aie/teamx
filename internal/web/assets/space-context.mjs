// A document has one immutable Space. Switching navigates to a new document,
// disposing its requests, caches and streams while server work keeps running.
export const pageSpaceID = /^\/s\/([a-f0-9]{32})\//.exec(globalThis.location?.pathname || "")?.[1] || "";
export function spaceURL(path) {
  if (!pageSpaceID || typeof path !== "string" || !path.startsWith("/") || path.startsWith("//") || path.startsWith("/s/") || path.startsWith("/ui/")) return path;
  return `/s/${pageSpaceID}${path}`;
}
let defaultSpaceID = "";
export function spaceStorage(storage) {
  if (!storage || !pageSpaceID) return storage;
  const key = value => `teamx.space:${pageSpaceID}:${value}`;
  return {
    getItem(value) {
      let saved = storage.getItem(key(value));
      if (saved == null && pageSpaceID === defaultSpaceID) {
        saved = storage.getItem(value);
        if (saved != null) storage.setItem(key(value), saved);
      }
      return saved;
    },
    setItem: (value, data) => storage.setItem(key(value), data),
    removeItem(value) { storage.removeItem(key(value)); if (pageSpaceID === defaultSpaceID) storage.removeItem(value); },
  };
}

let picker, capabilities = {};
export function spaceCapabilities() { return capabilities; }
export async function mountSpacePicker() {
  if (picker) return;
  let result;
  try { const response = await fetch("/v1/spaces", {cache:"no-store", signal:globalThis.AbortSignal?.timeout?.(5000)}); if (!response.ok) return; result = await response.json(); } catch { return; }
  defaultSpaceID = result.default_space_id || "";
  if (result.can_create !== true || !result.spaces?.length || picker) return;
  const id = pageSpaceID || result.default_space_id;
  const current = result.spaces.find(space => space.id === id);
  if (!current) return;
  capabilities = current.capabilities || {};
  picker = document.createElement("div"); picker.className = "space-picker";
  const select = document.createElement("select"); select.id = "space-select"; select.setAttribute("aria-label", "切换空间");
  for (const space of result.spaces) select.add(new Option(space.name + ({suspended:" · 已归档", archiving:" · 归档中", failed:" · 启动失败"}[space.runtime_state] || ""), space.id));
  select.value = id;
  const feedback = document.createElement("span"); feedback.className = "space-feedback"; feedback.setAttribute("role", "status");
  const button = document.createElement("button"); button.type = "button"; button.textContent = "空间设置";
  const dialog = document.createElement("dialog"); dialog.className = "space-dialog"; dialog.setAttribute("aria-label", "空间设置");
  const title = document.createElement("h2"); title.textContent = "空间设置";
  const label = document.createElement("label"); label.textContent = "空间名称";
  const name = document.createElement("input"); name.id = "space-name"; name.maxLength = 64; name.value = current.name; label.append(name);
  const rename = document.createElement("button"); rename.type = "button"; rename.textContent = "保存名称";
  const create = document.createElement("button"); create.type = "button"; create.textContent = "创建空间";
  const lifecycle = document.createElement("button"); lifecycle.id = "space-lifecycle-action"; lifecycle.type = "button";
  lifecycle.dataset.action = current.status === "suspended" ? "restore" : "archive";
  lifecycle.textContent = current.status === "suspended" ? "恢复空间" : current.status === "archiving" ? "重试归档" : "归档空间";
  lifecycle.disabled = current.lifecycle_busy === true;
  const close = document.createElement("button"); close.type = "button"; close.textContent = "关闭";
  const message = document.createElement("p"); message.setAttribute("role", "status");
  const hint = document.createElement("p"); hint.className = "space-hint"; hint.textContent = "新空间单独设置模型与连接。飞书和 Scion 目前仅在默认空间可用。";
  const actions = document.createElement("div"); actions.className = "space-actions"; actions.append(rename, create, lifecycle, close);
  dialog.append(title, label, hint, message, actions); document.body.append(dialog);
  picker.append(select, button, feedback);
  const host = document.querySelector(".room-rail-header") || document.querySelector("main"); host?.append(picker);
  select.addEventListener("change", () => { if (select.value !== id) location.assign(`/s/${select.value}/`); });
  button.addEventListener("click", () => { name.value = current.name; message.textContent = current.lifecycle_error || ""; dialog.showModal(); });
  close.addEventListener("click", () => dialog.close());
  // Keep an uncertain creation's ID and body. A retry cannot create a duplicate.
  let pending;
  const pendingKey = `teamx.space-create:${location.origin}:${id}`;
  try {
    const saved = JSON.parse(sessionStorage.getItem(pendingKey));
    if (/^[a-f0-9]{32}$/.test(saved?.request_id) && typeof saved.name === "string" && saved.name.trim() && saved.name.length <= 256) pending = saved;
    if (result.spaces.some(space => space.id === pending?.request_id)) { pending = undefined; sessionStorage.removeItem(pendingKey); }
  } catch { /* Creation remains usable when browser storage is unavailable. */ }
  async function save(creating) {
    if (create.disabled) return;
    const value = name.value.trim(); if (!value) { message.textContent = "请输入空间名称。"; name.focus(); return; }
    const input = creating ? (pending ||= {request_id:crypto.randomUUID().replaceAll("-", ""), name:value}) : {name:value, expected_version:current.version};
    if (creating) { name.value = input.name; try { sessionStorage.setItem(pendingKey, JSON.stringify(input)); } catch {} }
    rename.disabled = create.disabled = lifecycle.disabled = true;
    try {
      const response = await fetch(creating ? "/v1/spaces" : `/v1/spaces/${id}`, {method:"POST", headers:{"Content-Type":"application/json"}, body:JSON.stringify(input)});
      const record = await response.json();
      if (!response.ok) throw new Error(record.message || "空间未保存，请刷新核对后重试。");
      if (creating) { try { sessionStorage.removeItem(pendingKey); } catch {} location.assign(`/s/${record.id}/`); }
      else { Object.assign(current, record); select.selectedOptions[0].textContent = record.name; dialog.close(); }
    } catch (error) { message.textContent = error.message; }
    finally { rename.disabled = create.disabled = false; lifecycle.disabled = current.lifecycle_busy === true; }
  }
  rename.addEventListener("click", () => save(false)); create.addEventListener("click", () => save(true));
  let confirmingArchive = false;
  lifecycle.addEventListener("click", async () => {
    if (lifecycle.disabled || create.disabled) return;
    const action = lifecycle.dataset.action;
    if (action === "archive" && current.status === "active" && !confirmingArchive) {
      confirmingArchive = true; lifecycle.textContent = "确认归档";
      message.textContent = "归档将停止此空间的 Agent、通知采集和定时任务。会话与工作文件会保留，可稍后恢复。";
      return;
    }
    rename.disabled = create.disabled = lifecycle.disabled = true;
    try {
      const response = await fetch(`/v1/spaces/${id}/${action}`, {method:"POST", headers:{"Content-Type":"application/json"}, body:JSON.stringify({expected_version:current.version})});
      const record = await response.json();
      if (!response.ok) throw new Error(record.message || "操作未完成，请刷新核对空间状态后重试。");
      location.assign(`/s/${id}/`);
    } catch (error) {
      message.textContent = `${error.message} 请刷新核对空间状态后重试。`;
      rename.disabled = create.disabled = lifecycle.disabled = false;
    }
  });
  for (const brand of document.querySelectorAll('a.brand[href="/"]')) brand.href = spaceURL("/");
  // Effective capability declarations come from the server, including builds
  // whose available platform catalog differs from the standard distribution.
  if (current.capabilities?.computer === false) {
    const tab = document.getElementById("settings-tab-computer"); if (tab) { tab.disabled = true; tab.title = "电脑目前仅在默认空间可用"; }
  }
}
