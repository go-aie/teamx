import { compactViewport } from "../client.mjs";

// One action surface: an anchored desktop menu or an expanded mobile composer.
export function createComposerActions({ env, runtime, nearBottom, scrollToBottom }) {
  const { document, window } = env, { $, state } = runtime;
  const menu = $("compose-menu"), trigger = $("compose-more");
  let mobile = false, drag = null;
  const items = () => [$("add-images")].filter(item => !item.hidden && !item.disabled);

  function position() {
    const viewport = window.visualViewport;
    const top = viewport?.offsetTop || 0, left = viewport?.offsetLeft || 0;
    const height = viewport?.height || window.innerHeight, width = viewport?.width || window.innerWidth;
    if (mobile) {
      const available = height - $("compose").getBoundingClientRect().height - $("room-header").getBoundingClientRect().height - 32;
      menu.style.setProperty("--compose-menu-height", `${Math.max(44, available)}px`);
      return;
    }
    menu.style.setProperty("--compose-menu-height", `${Math.max(0, height - 16)}px`);
    const anchor = trigger.getBoundingClientRect(), box = menu.getBoundingClientRect();
    const below = anchor.bottom + 8;
    const above = anchor.top - box.height - 8;
    const y = below + box.height <= top + height - 8 ? below : above;
    menu.style.setProperty("--compose-menu-top", `${Math.max(top + 8, Math.min(y, top + height - box.height - 8))}px`);
    menu.style.setProperty("--compose-menu-left", `${Math.max(left + 8, Math.min(anchor.left, left + width - box.width - 8))}px`);
  }

  function close(focus = false) {
    if (!menu.open) return;
    const follow = mobile && nearBottom();
    menu.close();
    trigger.setAttribute("aria-expanded", "false");
    drag = null;
    menu.style.setProperty("--compose-sheet-drag", "0px");
    if (focus) trigger.focus({ preventScroll: true });
    if (follow) scrollToBottom();
    runtime.navigationChanged();
  }

  function open(last = false) {
    if (trigger.disabled || menu.open) return;
    mobile = compactViewport(window);
    const follow = mobile && nearBottom();
    menu.classList.toggle("compose-sheet", mobile);
    menu.setAttribute("role", mobile ? "region" : "menu");
    if (mobile) trigger.removeAttribute("aria-haspopup"); else trigger.setAttribute("aria-haspopup", "menu");
    $("add-images").setAttribute("role", mobile ? "button" : "menuitem");
    position();
    menu.show();
    position();
    trigger.setAttribute("aria-expanded", "true");
    const available = items();
    (last ? available.at(-1) : available[0])?.focus({ preventScroll: true });
    if (follow) scrollToBottom();
    runtime.navigationChanged();
  }

  function render() {
    trigger.disabled = !state.info || !state.connected || !!state.draft.pending || runtime.discussionBusy();
    if (compactViewport(window)) trigger.removeAttribute("aria-haspopup"); else trigger.setAttribute("aria-haspopup", "menu");
    if (trigger.disabled) close();
  }

  function mount() {
    trigger.addEventListener("click", () => menu.open ? close(true) : open());
    trigger.addEventListener("keydown", event => {
      if (["ArrowDown", "ArrowUp"].includes(event.key)) { event.preventDefault(); open(event.key === "ArrowUp"); }
      else if (event.key === "Escape") { event.preventDefault(); close(true); }
    });
    menu.addEventListener("cancel", event => { event.preventDefault(); close(true); });
    menu.addEventListener("keydown", event => {
      if (event.key === "Escape") { event.preventDefault(); close(true); return; }
      if (!mobile && ["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
        event.preventDefault();
        const available = items(), index = available.indexOf(document.activeElement);
        const next = event.key === "Home" ? 0 : event.key === "End" ? available.length - 1
          : (index + (event.key === "ArrowUp" ? -1 : 1) + available.length) % available.length;
        available[next]?.focus();
      }
    });
    document.addEventListener("click", event => {
      if (!menu.open || trigger.contains(event.target)) return;
      if (!menu.contains(event.target)) close();
    });
    menu.addEventListener("focusout", event => {
      if (!mobile && event.relatedTarget && !menu.contains(event.relatedTarget) && event.relatedTarget !== trigger) close();
    });
    // Editing replaces the action panel with the keyboard, retaining the draft.
    $("message-input").addEventListener("focus", () => { if (mobile) close(); });
    const grip = $("compose-menu-grip");
    grip.addEventListener("pointerdown", event => {
      if (!mobile || event.button > 0) return;
      drag = { id: event.pointerId, y: event.clientY };
      grip.setPointerCapture(event.pointerId);
    });
    grip.addEventListener("pointermove", event => {
      if (drag?.id === event.pointerId) menu.style.setProperty("--compose-sheet-drag", `${Math.max(0, event.clientY - drag.y)}px`);
    });
    const release = event => {
      if (drag?.id !== event.pointerId) return;
      const dismiss = event.type !== "pointercancel" && event.clientY - drag.y >= 48;
      if (grip.hasPointerCapture(event.pointerId)) grip.releasePointerCapture(event.pointerId);
      drag = null; menu.style.setProperty("--compose-sheet-drag", "0px");
      if (dismiss) close(true);
    };
    grip.addEventListener("pointerup", release);
    grip.addEventListener("pointercancel", release);
    window.addEventListener("resize", () => {
      render();
      if (!menu.open) return;
      if (mobile !== compactViewport(window)) { close(); open(); } else position();
    });
    window.visualViewport?.addEventListener("resize", position);
    window.visualViewport?.addEventListener("scroll", position);
    document.addEventListener("scroll", () => { if (menu.open && !mobile) position(); }, true);
  }
  return { mount, close, render };
}
