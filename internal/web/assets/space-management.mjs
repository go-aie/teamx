import { addDialogClose } from "./dialogs.mjs";

// Space maintenance uses the global directory, including Spaces whose runtime
// has stopped. It never needs to open their conversation APIs.
export function createSpaceManager({ document, currentSpaceID, spaceName, isDefault, canEnter, readDirectory, applyDirectory, afterCurrentArchive, failure }) {
  let directory, sending = false, reading = false, timer, editing, changing;
  let selectedTab = "active";
  const restored = new Set();
  function dialog(title, key, submitText) {
    const surface = document.createElement("dialog"); surface.className = "space-dialog"; surface.setAttribute("aria-labelledby", `${key}-title`);
    const form = document.createElement("form"), heading = document.createElement("h2"); heading.id = `${key}-title`; heading.textContent = title;
    const header = document.createElement("header"); header.className = "space-dialog-header"; header.append(heading);
    const content = document.createElement("div"), message = document.createElement("p"); message.className = "space-error"; message.setAttribute("role", "status"); message.setAttribute("aria-live", "polite");
    const actions = document.createElement("div"); actions.className = "space-actions";
    let cancel, submit;
    if (submitText) {
      cancel = document.createElement("button"); cancel.type = "button"; cancel.className = "secondary-button"; cancel.textContent = "取消";
      submit = document.createElement("button"); submit.type = "submit"; submit.className = "primary"; submit.textContent = submitText; actions.append(cancel, submit);
    }
    form.append(header, content, message); if (submitText) form.append(actions);
    surface.append(form); document.body.append(surface);
    const ui = {surface, form, header, heading, content, message, actions, cancel, submit, onDismiss:() => surface.close()};
    const dismissal = addDialogClose({document, dialog:surface, header, canClose:() => !sending, onClose:() => ui.onDismiss()});
    ui.close = dismissal.button; cancel?.addEventListener("click", dismissal.close);
    return ui;
  }
  const manager = dialog("管理空间", "space-management"); manager.surface.id = "space-management"; manager.surface.classList.add("space-manager");
  const refreshButton = document.createElement("button"); refreshButton.id = "space-management-refresh"; refreshButton.type = "button"; refreshButton.className = "icon-button";
  refreshButton.setAttribute("aria-label", "刷新列表"); refreshButton.title = "刷新列表";
  const refreshIcon = document.createElementNS("http://www.w3.org/2000/svg", "svg"); refreshIcon.setAttribute("viewBox", "0 0 24 24"); refreshIcon.setAttribute("aria-hidden", "true");
  const refreshPath = document.createElementNS("http://www.w3.org/2000/svg", "path"); refreshPath.setAttribute("d", "M20 4v6h-6M4 20v-6h6M5 8a7 7 0 0 1 11.8-3L20 8M4 16l3.2 3A7 7 0 0 0 19 16"); refreshIcon.append(refreshPath); refreshButton.append(refreshIcon);
  manager.header.insertBefore(refreshButton, manager.close);
  const rename = dialog("修改空间名称", "space-name", "保存");
  const label = document.createElement("label"); label.textContent = "空间名称";
  const name = document.createElement("input"); name.id = "space-name"; name.className = "field-control"; name.required = true; name.autofocus = true; name.maxLength = 64; label.append(name); rename.content.append(label);
  const lifecycle = dialog("归档空间", "space-lifecycle", "确认归档");
  const hint = document.createElement("p"); hint.className = "space-hint"; lifecycle.content.append(hint);
  const controls = () => [manager, rename, lifecycle].flatMap(ui => [...ui.form.querySelectorAll("button,input")]);
  function updateBusy() {
    for (const control of controls()) control.disabled = sending || reading || control.dataset.unavailable === "true";
    // Reading the directory may be cancelled without abandoning a write.
    manager.close.disabled = sending;
  }
  function stopPolling() { clearTimeout(timer); timer = undefined; }
  function scheduleRefresh() {
    stopPolling();
    if (manager.surface.open && directory?.spaces.some(space => space.lifecycle_busy)) timer = setTimeout(() => void refresh(false), 1000);
  }
  function tag(text) { const badge = document.createElement("span"); badge.className = "space-badge"; badge.textContent = text; return badge; }
  function rowAction(row, text, action, disabled = false, danger = false) {
    const button = document.createElement("button"); button.type = "button"; button.className = ["enter", "restore"].includes(action) ? "secondary-button" : `text-button${danger ? " danger-button" : ""}`; button.textContent = text;
    button.dataset.action = action; button.dataset.unavailable = String(disabled); button.disabled = disabled; row.append(button); return button;
  }
  function openLifecycle(space) {
    const archived = space.status === "suspended";
    changing = {...space}; lifecycle.heading.textContent = archived ? "恢复空间" : "归档空间";
    lifecycle.submit.textContent = archived ? "确认恢复" : "确认归档"; lifecycle.submit.className = archived ? "primary" : "primary danger-button";
    hint.textContent = archived ? `恢复「${spaceName(space)}」后，可继续使用原有会话与工作文件。` : `归档「${spaceName(space)}」将停止 Agent、通知采集和定时任务。会话与工作文件会保留，可稍后恢复。`;
    lifecycle.message.textContent = ""; manager.surface.close(); lifecycle.surface.showModal();
  }
  function moreMenu(space, actions, unavailable) {
    const toggle = rowAction(actions, "⋯", "more", unavailable); toggle.className = "icon-button space-row-more";
    toggle.setAttribute("aria-label", `${spaceName(space)}的更多操作`); toggle.setAttribute("aria-haspopup", "menu"); toggle.setAttribute("aria-expanded", "false");
    const menu = document.createElement("div"); menu.className = "space-row-menu menu-surface"; menu.id = `space-row-menu-${space.id}`;
    menu.setAttribute("role", "menu"); menu.setAttribute("aria-label", `${spaceName(space)}的操作`); menu.setAttribute("popover", "auto");
    toggle.setAttribute("popovertarget", menu.id); toggle.setAttribute("aria-controls", menu.id); actions.append(menu);
    const close = () => { menu.hidePopover(); toggle.focus(); };
    if (space.status === "active") rowAction(menu, "改名", "rename").addEventListener("click", () => {
      menu.hidePopover(); editing = {...space}; name.value = spaceName(space); rename.message.textContent = ""; manager.surface.close(); rename.surface.showModal();
    });
    rowAction(menu, space.status === "archiving" ? "重试归档" : "归档", "archive", false, true).addEventListener("click", () => { menu.hidePopover(); openLifecycle(space); });
    const items = () => [...menu.querySelectorAll("button")];
    for (const item of items()) { item.setAttribute("role", "menuitem"); item.tabIndex = -1; }
    function position() {
      const rect = toggle.getBoundingClientRect(), view = document.defaultView;
      menu.style.left = `${Math.max(8, Math.min(rect.right - 160, view.innerWidth - 168))}px`;
      menu.style.top = `${rect.bottom + 94 < view.innerHeight ? rect.bottom + 4 : Math.max(8, rect.top - 90)}px`;
    }
    toggle.addEventListener("click", position);
    toggle.addEventListener("keydown", event => {
      if (["ArrowDown", "ArrowUp"].includes(event.key)) { event.preventDefault(); position(); menu.showPopover(); (event.key === "ArrowUp" ? items().at(-1) : items()[0]).focus(); }
    });
    menu.addEventListener("toggle", () => {
      const open = menu.matches(":popover-open"); toggle.setAttribute("aria-expanded", String(open));
      if (open && document.activeElement === toggle) items()[0]?.focus();
    });
    menu.addEventListener("keydown", event => {
      const options = items(), index = options.indexOf(document.activeElement);
      const next = {ArrowDown:(index + 1) % options.length, ArrowUp:(index - 1 + options.length) % options.length, Home:0, End:options.length - 1}[event.key];
      if (next !== undefined) { event.preventDefault(); options[next]?.focus(); }
      if (event.key === "Escape" || event.key === "Tab") { event.preventDefault(); event.stopPropagation(); close(); }
    });
  }
  function selectTab(value, focus = false) {
    selectedTab = value; render(); if (focus) manager.content.querySelector('[aria-selected="true"]')?.focus();
  }
  function render() {
    manager.content.replaceChildren();
    const tabs = document.createElement("div"); tabs.className = "space-management-tabs"; tabs.setAttribute("role", "tablist"); tabs.setAttribute("aria-label", "空间状态");
    for (const archived of [false, true]) {
      const value = archived ? "archived" : "active", tab = document.createElement("button"); tab.type = "button"; tab.id = `space-tab-${value}`;
      tab.setAttribute("role", "tab"); tab.setAttribute("aria-selected", String(selectedTab === value)); tab.setAttribute("aria-controls", "space-management-list"); tab.tabIndex = selectedTab === value ? 0 : -1;
      tab.textContent = `${archived ? "已归档" : "使用中"} ${directory.spaces.filter(space => (space.status === "suspended") === archived).length}`;
      tab.addEventListener("click", () => selectTab(value, true));
      tab.addEventListener("keydown", event => {
        if (["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) { event.preventDefault(); selectTab(event.key === "Home" ? "active" : event.key === "End" ? "archived" : selectedTab === "active" ? "archived" : "active", true); }
      });
      tabs.append(tab);
    }
    manager.content.append(tabs);
    const archived = selectedTab === "archived";
    const section = document.createElement("section"); section.id = "space-management-list"; section.setAttribute("role", "tabpanel"); section.setAttribute("aria-labelledby", `space-tab-${selectedTab}`);
    if (archived) { const note = document.createElement("p"); note.className = "space-hint space-note"; note.textContent = "会话与工作文件已保留，恢复后可查看。"; section.append(note); }
    const spaces = directory.spaces.filter(space => (space.status === "suspended") === archived);
    for (const space of spaces) {
      const row = document.createElement("div"); row.className = "space-management-row"; row.dataset.spaceId = space.id;
      row.classList.toggle("space-management-current", !archived && space.id === currentSpaceID);
      const identity = document.createElement("div"); identity.className = "space-management-identity";
      const nameLabel = document.createElement("span"); nameLabel.className = "space-management-name"; nameLabel.textContent = spaceName(space); identity.append(nameLabel);
      if (isDefault(space)) identity.append(tag("默认"));
      const resetting = space.reset && space.reset.phase !== "completed";
      const status = resetting ? "重置中" : space.status === "archiving" ? "归档中" : space.runtime_state === "failed" ? "启动失败" : "";
      if (status) identity.append(tag(status));
      row.append(identity);
      const actions = document.createElement("div"); actions.className = "space-management-row-actions"; row.append(actions);
      const unavailable = space.lifecycle_busy === true || !!resetting;
      if (archived) rowAction(actions, "恢复", "restore", unavailable).addEventListener("click", () => openLifecycle(space));
      else {
        if (canEnter(space) && (space.id !== currentSpaceID || restored.has(space.id))) rowAction(actions, "进入", "enter").addEventListener("click", () => location.assign(`/s/${space.id}/`));
        else { const current = tag(space.id === currentSpaceID ? "当前" : "不可用"); current.classList.add("space-row-state"); actions.append(current); }
        moreMenu(space, actions, unavailable);
      }
      section.append(row);
      if (space.lifecycle_error) { const error = document.createElement("p"); error.className = "space-error"; error.textContent = "上次归档未完成，请重试。"; section.append(error); }
    }
    if (!spaces.length) { const empty = document.createElement("p"); empty.className = "space-hint"; empty.textContent = archived ? "暂无已归档空间。" : "暂无使用中的空间。"; section.append(empty); }
    manager.content.append(section);
    updateBusy();
  }
  async function refresh(explicit = true) {
    if (sending || reading) return;
    const focused = document.activeElement, focusRow = focused?.closest?.(".space-management-row")?.dataset.spaceId, focusAction = focused?.dataset?.action;
    stopPolling(); reading = true; updateBusy();
    if (explicit) manager.message.textContent = "";
    try {
      const latest = await readDirectory();
      if (!applyDirectory(latest)) return;
      directory = latest; render(); scheduleRefresh();
    } catch { manager.message.textContent = "暂时无法读取空间信息，请刷新列表重试。"; }
    finally {
      reading = false; updateBusy();
      if (manager.surface.open && [document.body, manager.surface, focused].includes(document.activeElement)) {
        const row = focusRow && manager.content.querySelector(`[data-space-id="${focusRow}"]`);
        const target = row ? row.querySelector(`[data-action="${focusAction}"]:not(:disabled)`) || row.querySelector("button:not(:disabled)") : focused;
        if (target?.isConnected && !target.disabled) target.focus();
      }
    }
  }
  async function write(ui, space, action, body) {
    if (sending || reading) return;
    sending = true; updateBusy(); ui.message.textContent = "";
    let failureMessage;
    try {
      const response = await fetch(`/v1/spaces/${space.id}${action ? `/${action}` : ""}`, {method:"POST", headers:{"Content-Type":"application/json"}, body:JSON.stringify({...body, expected_version:space.version})});
      const record = await response.json();
      if (!response.ok) { failureMessage = failure(record, "操作结果尚未确认，请返回管理空间刷新列表后核对。"); throw Error(); }
      if (action === "restore") restored.add(record.id);
      if (action) selectedTab = action === "restore" ? "active" : "archived";
      directory.spaces = directory.spaces.map(item => item.id === record.id ? {...item, ...record} : item);
      applyDirectory(directory);
      if (action === "archive" && space.id === currentSpaceID) {
        let latest;
        try { latest = await readDirectory(); } catch { /* The archived document retains global management. */ }
        afterCurrentArchive(latest); return;
      }
      ui.surface.close();
      manager.message.textContent = "";
      render(); manager.surface.showModal();
    } catch { ui.message.textContent = failureMessage || "操作结果尚未确认，请返回管理空间刷新列表后核对。"; }
    finally {
      sending = false; updateBusy();
      if (manager.surface.open) void refresh(false);
    }
  }
  manager.form.addEventListener("submit", event => event.preventDefault());
  manager.surface.addEventListener("close", stopPolling);
  refreshButton.addEventListener("click", () => void refresh());
  for (const ui of [rename, lifecycle]) {
    ui.onDismiss = () => { ui.surface.close(); manager.surface.showModal(); void refresh(); };
  }
  rename.form.addEventListener("submit", event => {
    event.preventDefault(); const value = name.value.trim();
    if (!value) { rename.message.textContent = "请输入空间名称。"; name.focus(); return; }
    void write(rename, editing, "", {name:value});
  });
  lifecycle.form.addEventListener("submit", event => { event.preventDefault(); void write(lifecycle, changing, changing.status === "suspended" ? "restore" : "archive", {}); });
  return { open(initial) {
    directory = initial; selectedTab = directory.spaces.some(space => space.status !== "suspended") ? "active" : "archived";
    manager.message.textContent = ""; render(); manager.surface.showModal(); void refresh();
  } };
}
