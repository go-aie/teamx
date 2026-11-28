import { createSettingsIcon, createPlusIcon } from "./icons.mjs";
import { createSpaceManager } from "./space-management.mjs";
import { addDialogClose } from "./dialogs.mjs";

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
export function spaceCanEnter(space) {
  return space.status === "active" && (!space.reset || space.reset.phase === "completed") && ["ready", "starting", "setup_required", "migration_required"].includes(space.runtime_state);
}
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
  // Existing installations may still have the original default name. Keep
  // explicit user renames, while presenting the original default as TeamX.
  const spaceName = space => space.id === defaultSpaceID && space.name === "默认空间" ? "TeamX" : space.name;
  const trigger = document.createElement("button"); trigger.id = "space-trigger"; trigger.type = "button"; trigger.className = "space-trigger";
  trigger.dataset.spaceId = id;
  trigger.setAttribute("aria-haspopup", "menu"); trigger.setAttribute("aria-controls", "space-menu"); trigger.setAttribute("aria-expanded", "false");
  const mark = document.createElement("img"); mark.className = "brand-mark"; mark.src = "/ui/brand-mark.svg"; mark.alt = ""; mark.width = 28; mark.height = 29;
  const nameLabel = document.createElement("span"); nameLabel.className = "space-trigger-name";
  const chevron = document.createElement("span"); chevron.className = "space-chevron"; chevron.setAttribute("aria-hidden", "true");
  trigger.append(mark, nameLabel, chevron); picker.append(trigger);
  const menu = document.createElement("div"); menu.id = "space-menu"; menu.className = "space-menu menu-surface";
  menu.setAttribute("popover", "auto"); menu.setAttribute("role", "menu"); menu.setAttribute("aria-label", "空间");
  trigger.setAttribute("popovertarget", menu.id);
  const options = document.createElement("div"); options.className = "space-menu-options"; options.setAttribute("role", "group"); options.setAttribute("aria-label", "切换空间");
  function renderOptions() {
    options.replaceChildren();
    for (const space of result.spaces.filter(spaceCanEnter)) {
      const option = document.createElement("button"); option.type = "button"; option.className = "space-menu-item"; option.tabIndex = -1;
      option.dataset.spaceId = space.id; option.setAttribute("role", "menuitemradio"); option.setAttribute("aria-checked", String(space.id === id));
      const label = document.createElement("span"); label.className = "space-menu-name"; label.textContent = spaceName(space);
      option.append(label);
      if (space.id === defaultSpaceID) { const badge = document.createElement("span"); badge.className = "space-badge"; badge.textContent = "默认"; option.append(badge); }
      const check = document.createElement("span"); check.className = "space-menu-check"; check.textContent = "✓"; check.setAttribute("aria-hidden", "true"); option.append(check);
      option.addEventListener("click", () => { closeMenu(true); if (space.id !== id) location.assign(`/s/${space.id}/`); });
      options.append(option);
    }
  }
  renderOptions();
  const actions = document.createElement("div"); actions.className = "space-menu-actions"; actions.setAttribute("role", "group"); actions.setAttribute("aria-label", "空间操作");
  function menuAction(key, text, icon) {
    const action = document.createElement("button"); action.id = key; action.type = "button"; action.className = "space-menu-item";
    const label = document.createElement("span"); label.textContent = text;
    action.append(icon, label); action.tabIndex = -1; action.setAttribute("role", "menuitem"); action.setAttribute("aria-haspopup", "dialog"); actions.append(action); return action;
  }
  const button = menuAction("space-settings", "管理空间", createSettingsIcon(document));
  const create = menuAction("space-create", "新建空间", createPlusIcon(document));
  const feedback = document.createElement("span"); feedback.className = "space-feedback"; feedback.setAttribute("role", "status");
  feedback.setAttribute("aria-live", "polite");
  menu.append(options, actions, feedback); document.body.append(menu);
  const brand = document.querySelector(".room-rail-header .brand");
  if (brand) { picker.id = brand.id; brand.replaceWith(picker); }
  else document.querySelector("main")?.append(picker);
  function updateName() {
    nameLabel.textContent = spaceName(current);
    trigger.title = spaceName(current); trigger.setAttribute("aria-label", `${spaceName(current)} · 切换空间`);
  }
  updateName();
  function closeMenu(restoreFocus = false) {
    menu.hidePopover(); trigger.setAttribute("aria-expanded", "false");
    if (restoreFocus) trigger.focus();
  }
  const menuItems = () => [...menu.querySelectorAll("button:not(:disabled)")];
  function positionMenu() {
    const rect = trigger.getBoundingClientRect();
    menu.style.left = `${Math.max(8, Math.min(rect.left, innerWidth - Math.min(280, innerWidth - 16) - 8))}px`;
    menu.style.top = `${Math.min(rect.bottom + 6, innerHeight - 48)}px`;
    menu.style.maxHeight = `${Math.max(40, innerHeight - rect.bottom - 14)}px`;
  }
  function openMenu(last = false) {
    if (trigger.disabled) return;
    positionMenu();
    menu.showPopover(); trigger.setAttribute("aria-expanded", "true");
    (last ? menuItems().at(-1) : options.querySelector('[aria-checked="true"]') || menuItems()[0])?.focus();
  }
  trigger.addEventListener("click", () => { if (!menu.matches(":popover-open")) positionMenu(); });
  trigger.addEventListener("keydown", event => {
    if (event.key === "ArrowDown" || event.key === "ArrowUp") { event.preventDefault(); openMenu(event.key === "ArrowUp"); }
  });
  menu.addEventListener("keydown", event => {
    const items = menuItems(), index = items.indexOf(document.activeElement);
    const next = {ArrowDown:(index + 1) % items.length, ArrowUp:(index - 1 + items.length) % items.length, Home:0, End:items.length - 1}[event.key];
    if (next !== undefined) { event.preventDefault(); items[next]?.focus(); }
    if (event.key === "Escape" || event.key === "Tab") { event.preventDefault(); event.stopPropagation(); closeMenu(true); }
  });
  menu.addEventListener("toggle", () => {
    const open = menu.matches(":popover-open"); trigger.setAttribute("aria-expanded", String(open));
    if (open && document.activeElement === trigger) (options.querySelector('[aria-checked="true"]') || menuItems()[0])?.focus();
  });
  document.addEventListener("keydown", event => {
    if (event.key === "Escape" && menu.matches(":popover-open")) { event.preventDefault(); event.stopPropagation(); closeMenu(true); }
  }, true);
  window.addEventListener("resize", () => { if (menu.matches(":popover-open")) positionMenu(); });
  document.getElementById("toggle-rooms")?.addEventListener("click", () => closeMenu());

  function makeDialog(heading, key, submitText, withName = true) {
    const dialog = document.createElement("dialog"); dialog.className = "space-dialog"; dialog.setAttribute("aria-labelledby", `${key}-title`);
    const form = document.createElement("form");
    const title = document.createElement("h2"); title.id = `${key}-title`; title.textContent = heading;
    const header = document.createElement("header"); header.className = "space-dialog-header"; header.append(title);
    const label = document.createElement("label"); label.textContent = "空间名称";
    const name = document.createElement("input"); name.id = key; name.maxLength = 64; name.required = withName; name.autofocus = withName; name.className = "field-control"; label.append(name);
    label.hidden = !withName;
    const hint = document.createElement("p"); hint.className = "space-hint";
    const message = document.createElement("p"); message.className = "space-error"; message.setAttribute("role", "status"); message.setAttribute("aria-live", "polite");
    const cancel = document.createElement("button"); cancel.type = "button"; cancel.className = "secondary-button"; cancel.textContent = "取消";
    const submit = document.createElement("button"); submit.type = "submit"; submit.className = "primary"; submit.textContent = submitText;
    const actions = document.createElement("div"); actions.className = "space-actions"; actions.append(cancel, submit);
    form.append(header, label, hint, message, actions); dialog.append(form); document.body.append(dialog);
    const dismissal = addDialogClose({document, dialog, header, canClose:() => !sending});
    cancel.addEventListener("click", dismissal.close);
    return {dialog, form, title, name, hint, message, cancel, submit, actions, close:dismissal.button};
  }
  const creation = makeDialog("新建空间", "space-create-name", "创建并进入");
  creation.name.placeholder = "例如：工作、个人项目";
  creation.hint.textContent = "优先沿用当前空间的默认模型，平台连接需单独配置。";
  const creationNote = document.createElement("p"); creationNote.className = "space-hint space-note";
  creationNote.textContent = "飞书和 Scion 仅默认空间可用。";
  creation.form.insertBefore(creationNote, creation.message);
  const recovery = makeDialog("继续上次创建", "space-create-recovery", "核对并继续", false);
  let sending = false;
  function busy(value) {
    sending = value;
    for (const ui of [creation, recovery]) ui.close.disabled = ui.cancel.disabled = ui.submit.disabled = value;
    button.disabled = create.disabled = trigger.disabled = value;
  }
  function failure(record, fallback) {
    const code = record.code || record.error || record.message;
    return ({directory_conflict:"新空间的数据目录与已有工作目录重叠，暂时无法创建。请调整已使用的工作目录后重试。",
      conflict:"空间信息已变化，请刷新核对后重试。", busy:"空间正在处理其他操作，请稍后重试。",
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
  create.addEventListener("click", () => { closeMenu(true); showCreation(); });
  async function readDirectory() {
    const response = await fetch("/v1/spaces", {cache:"no-store", signal:globalThis.AbortSignal?.timeout?.(5000)});
    if (!response.ok) throw Error();
    return response.json();
  }
  function applyDirectory(directory) {
    const latest = directory.spaces?.find(space => space.id === id);
    if (!latest || !observeSpaceReset(latest)) return false;
    Object.assign(current, latest); result = directory; updateName(); renderOptions(); return true;
  }
  const management = createSpaceManager({document, currentSpaceID:id, spaceName, isDefault:space => space.id === defaultSpaceID,
    canEnter:spaceCanEnter, readDirectory, applyDirectory, failure,
    afterCurrentArchive:directory => location.replace(`/s/${directory && spaceAfterArchive(directory, id) || id}/`)});
  button.addEventListener("click", () => {
    if (sending) return;
    closeMenu(true); management.open(result);
  });
  async function save(ui) {
    if (sending) return;
    const value = pending ? pending.name : ui.name.value.trim();
    if (!value) { ui.message.textContent = "请输入空间名称。"; ui.name.focus(); return; }
    const input = pending ||= {request_id:crypto.randomUUID().replaceAll("-", ""), name:value, model_source_space_id:id};
    ui.name.value = input.name; try { sessionStorage.setItem(pendingKey, JSON.stringify(input)); } catch {}
    busy(true); ui.message.textContent = "";
    let failureMessage;
    try {
      const response = await fetch("/v1/spaces", {method:"POST", headers:{"Content-Type":"application/json"}, body:JSON.stringify(input)});
      const record = await response.json();
      if (!response.ok) {
        if (response.status >= 400 && response.status < 500 && record.code !== "conflict") forgetPending();
        failureMessage = failure(record, "空间未保存，请刷新核对后重试。");
        throw Error(failureMessage);
      }
      forgetPending(); location.assign(`/s/${record.id}/`);
    } catch {
      if (pending) {
        if (ui !== recovery) { ui.dialog.close(); showCreation(); }
        recovery.message.textContent = failureMessage || "暂时无法确认创建结果，请稍后重试。";
      } else if (ui === recovery) {
        ui.dialog.close(); showCreation(); creation.name.value = value;
        creation.message.textContent = failureMessage || "未能创建空间，请稍后重试。";
      } else ui.message.textContent = failureMessage || "暂时无法保存，请刷新核对后重试。";
    }
    finally { busy(false); }
  }
  creation.form.addEventListener("submit", event => { event.preventDefault(); void save(creation); });
  recovery.form.addEventListener("submit", event => { event.preventDefault(); void save(recovery); });
}
