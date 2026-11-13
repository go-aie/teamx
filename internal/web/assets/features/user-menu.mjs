import { createUserIcon } from "../icons.mjs";

// A browser-local display name never changes the authenticated operator.
export function createUserMenu({ env, runtime, conversationUI }) {
  const { document, window, request, fetch } = env;
  const { $, state } = runtime;
  let storage;
  try { storage = env.localStorage; } catch { /* Display names also work in memory. */ }
  let profileKey, customName = "", signingOut = false;
  const names = new Map();
  const items = () => [$("open-settings"), $("access-logout")].filter(el => !el.hidden && !el.disabled);

  function defaultName() {
    const name = (state.operatorDisplayName || state.operator || "").trim();
    return name && name !== "operator" && !/^owner_[0-9a-f]{32}$/i.test(name) ? name : "用户";
  }

  function displayName() {
    const key = JSON.stringify(["teamx.user-name", state.installationID || "", state.operator]);
    if (key !== profileKey) {
      profileKey = key;
      let saved;
      try { saved = storage?.getItem(key); } catch { /* Storage is optional. */ }
      customName = names.has(key) ? names.get(key) : saved?.trim().slice(0, 40) || "";
      $("user-display-name").value = customName || defaultName();
      $("user-name-feedback").textContent = "";
    }
    return customName || defaultName();
  }

  function render() {
    const name = displayName();
    $("user-display-name").placeholder = defaultName();
    $("user-name").textContent = name;
    $("user-menu-toggle").title = `${name} · 用户菜单`;
    $("user-menu-toggle").setAttribute("aria-label", `${name} · 用户菜单`);
  }

  function close(restoreFocus = false) {
    $("user-menu").hidden = true;
    $("user-menu-toggle").setAttribute("aria-expanded", "false");
    if (restoreFocus) $("user-menu-toggle").focus();
  }

  function open(last = false) {
    $("user-menu").hidden = false;
    $("user-menu-toggle").setAttribute("aria-expanded", "true");
    (last ? items().at(-1) : items()[0])?.focus();
  }

  function within(event, id) {
    return event.composedPath?.().includes($(id)) || $(id).contains?.(event.target) || event.target === $(id);
  }

  async function loadSession() {
    // This probe must not redirect installations that have no access gate.
    try {
      const response = await fetch("/v1/access/session", { cache: "no-store" });
      const session = response.ok ? await response.json() : null;
      const enabled = response.ok && session?.login_enabled !== false;
      $("access-logout").hidden = !enabled;
      $("user-menu-divider").hidden = !enabled;
    } catch { /* Keep settings available when the session probe fails. */ }
  }

  function mount() {
    $("user-avatar").replaceChildren(createUserIcon(document));
    render(); void loadSession();
    $("user-menu-toggle").addEventListener("click", () => $("user-menu").hidden ? open() : close(true));
    $("user-menu-toggle").addEventListener("keydown", event => {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault(); open(event.key === "ArrowUp");
      }
    });
    $("user-menu").addEventListener("keydown", event => {
      const options = items(), current = options.indexOf(document.activeElement);
      const next = { ArrowDown: (current + 1) % options.length, ArrowUp: (current - 1 + options.length) % options.length,
        Home: 0, End: options.length - 1 }[event.key];
      if (next !== undefined) { event.preventDefault(); options[next]?.focus(); }
      if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); close(true); }
      if (event.key === "Tab") { event.preventDefault(); close(true); }
    });
    $("open-settings").addEventListener("click", () => close());
    document.addEventListener("pointerdown", event => {
      if (!within(event, "user-menu") && !within(event, "user-menu-toggle")) close();
    });
    document.addEventListener("focusin", event => {
      if (!within(event, "user-menu") && !within(event, "user-menu-toggle")) close();
    });
    window.addEventListener("resize", () => close());
    window.addEventListener("pagehide", () => close());
    window.addEventListener("storage", event => {
      if (event.key !== profileKey && event.key !== null) return;
      names.delete(profileKey); profileKey = undefined; render();
      conversationUI.renderMessages();
    });
    $("user-name-form").addEventListener("submit", event => {
      event.preventDefault();
      customName = $("user-display-name").value.trim().slice(0, 40);
      if (customName === defaultName()) customName = "";
      names.set(profileKey, customName);
      let persisted = false;
      try {
        if (customName) storage?.setItem(profileKey, customName);
        else storage?.removeItem(profileKey);
        persisted = !!storage;
      } catch { /* Retain the name in memory. */ }
      render(); $("user-display-name").value = customName || defaultName();
      conversationUI.renderMessages();
      $("user-name-feedback").textContent = persisted ? "名称已保存" : "本次已更新，浏览器未能保存";
    });
    $("access-logout").addEventListener("click", async () => {
      if (signingOut) return;
      signingOut = true; $("access-logout").disabled = true; $("user-menu-feedback").hidden = true;
      try {
        await request("/v1/access/logout", {});
        window.location.assign("/access");
      } catch {
        $("user-menu-feedback").textContent = "退出失败，请重试。";
        $("user-menu-feedback").hidden = false;
      } finally { signingOut = false; $("access-logout").disabled = false; }
    });
  }

  return { mount, render, displayName };
}
