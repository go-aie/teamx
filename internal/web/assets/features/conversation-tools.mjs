import { compactViewport } from "../client.mjs";
import { createFolderIcon, createSettingsIcon } from "../icons.mjs";

// One registry drives desktop shortcuts and the complete conversation menu.
export function createConversationTools({ env, runtime, roomsUI, layoutUI, filesUI, calendarUI, environmentUI }) {
  const { document, window } = env, { $, state } = runtime;
  const tools = [
    {key:"files", label:"文件", shortcut:"conversation-files", action:"conversation-open-files", open:opener=>filesUI.open(opener), active:()=>filesUI.isOpen},
    {key:"calendar", label:"日历", shortcut:"conversation-calendar", action:"conversation-open-calendar", open:()=>calendarUI.open(), active:()=>calendarUI.visible()},
    {key:"environment", label:"电脑", shortcut:"conversation-environment", action:"conversation-open-environment", open:opener=>environmentUI.open(opener), active:()=>environmentUI.isOpen},
    {key:"settings", label:"设置", shortcut:"conversation-details", action:"conversation-open-details", open:()=>layoutUI.toggleDetails(), active:()=>layoutUI.membersPanelOpen()},
  ];
  const preferences = new Map();
  let key = "", pinned = {}, storage;
  try { storage = env.localStorage; } catch { /* Preferences also work in memory. */ }

  const available = tool => tool.key !== "calendar" || roomsUI.isDirect();
  const visibleTools = () => tools.filter(available);
  function load() {
    const next = JSON.stringify(["teamx.conversation-tools", state.installationID || "", state.operator]);
    if (next === key) return;
    key = next;
    let saved = preferences.get(key);
    try { saved ??= JSON.parse(storage?.getItem(key) || "null"); } catch { /* Ignore malformed preferences. */ }
    pinned = Object.fromEntries(tools.map(tool => [tool.key, typeof saved?.[tool.key] === "boolean" ? saved[tool.key] : true]));
    preferences.set(key, pinned);
  }
  function close(focus = false) {
    $("conversation-menu").hidden = true; $("room-menu-label").setAttribute("aria-expanded", "false");
    if (focus) $("room-menu-label").focus({preventScroll:true});
    runtime.navigationChanged();
  }
  function render() {
    load();
    const mobile = compactViewport(window);
    for (const tool of tools) {
      const enabled = available(tool), shortcut = $(tool.shortcut), action = $(tool.action), pin = $(`conversation-pin-${tool.key}`);
      shortcut.hidden = mobile || !enabled || !pinned[tool.key]; shortcut.disabled = !state.info;
      shortcut.setAttribute("aria-expanded", String(tool.active()));
      $(`conversation-tool-${tool.key}`).hidden = !enabled;
      action.disabled = !state.info; action.setAttribute("aria-expanded", String(tool.active()));
      action.setAttribute("aria-label", tool.label); action.title = tool.label;
      pin.hidden = mobile; pin.setAttribute("aria-checked", String(pinned[tool.key]));
      const label = `${pinned[tool.key] ? "取消固定" : "固定"}${tool.label}`;
      pin.setAttribute("aria-label", label); pin.title = label;
    }
  }
  function openMenu(last = false) {
    render(); $("conversation-menu").hidden = false; $("room-menu-label").setAttribute("aria-expanded", "true");
    const items = visibleTools(); $(items[last ? items.length - 1 : 0].action).focus({preventScroll:true});
    runtime.navigationChanged();
  }
  function activate(tool, opener, toggle = false) {
    close();
    if (toggle && tool.key === "files" && tool.active()) { filesUI.close({focus:false}); opener.focus({preventScroll:true}); }
    else if (toggle && tool.key === "calendar" && tool.active()) calendarUI.backToChat();
    else if (toggle && tool.key === "environment" && tool.active()) environmentUI.close();
    else void tool.open(opener);
    render();
  }
  function mount() {
    for (const [kind, createIcon] of [["files", createFolderIcon], ["settings", createSettingsIcon]]) {
      const tool = tools.find(tool => tool.key === kind);
      for (const id of [tool.shortcut, tool.action]) $(id).insertBefore(createIcon(document), $(id).firstChild);
    }
    for (const tool of tools) {
      $(tool.shortcut).addEventListener("click", () => activate(tool, $(tool.shortcut), true));
      $(tool.action).addEventListener("click", () => activate(tool, $(tool.action)));
      $(`conversation-pin-${tool.key}`).addEventListener("click", () => {
        load(); pinned = {...pinned, [tool.key]:!pinned[tool.key]}; preferences.set(key, pinned);
        try { storage?.setItem(key, JSON.stringify(pinned)); } catch { /* Keep this page's preference. */ }
        render();
      });
    }
    $("room-menu-label").addEventListener("click", () => $("conversation-menu").hidden ? openMenu() : close(true));
    $("room-menu-label").addEventListener("keydown", event => {
      if (!["ArrowDown", "ArrowUp"].includes(event.key)) return;
      event.preventDefault(); openMenu(event.key === "ArrowUp");
    });
    $("conversation-menu").addEventListener("keydown", event => {
      const items = visibleTools(), active = document.activeElement;
      const index = items.findIndex(tool => active === $(tool.action) || active === $(`conversation-pin-${tool.key}`));
      const next = {ArrowDown:(index+1)%items.length, ArrowUp:(index+items.length-1)%items.length, Home:0, End:items.length-1}[event.key];
      if (next !== undefined) { event.preventDefault(); $(items[next].action).focus(); }
      if (index >= 0 && ["ArrowLeft", "ArrowRight"].includes(event.key)) {
        event.preventDefault();
        $(event.key === "ArrowRight" && !compactViewport(window) ? `conversation-pin-${items[index].key}` : items[index].action).focus();
      }
      if (["Escape", "Tab"].includes(event.key)) { event.preventDefault(); event.stopPropagation(); close(true); }
    });
    document.addEventListener("pointerdown", event => {
      if (!$("conversation-menu").hidden && !$("conversation-menu").contains(event.target) && !$("room-menu-label").contains(event.target)) close();
    });
    document.addEventListener("keydown", event => {
      if (event.key !== "Escape" || event.defaultPrevented || $("conversation-menu").hidden) return;
      event.preventDefault(); close(true);
    });
    window.addEventListener("resize", () => { close(); render(); });
    window.addEventListener("storage", event => {
      if (event.key !== key && event.key !== null) return;
      preferences.delete(key); key = ""; render();
    });
    render();
  }
  return { mount, render, close };
}
