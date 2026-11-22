const labels = {off:"关闭", minimal:"极低", low:"低", medium:"中", high:"高", xhigh:"极高", max:"最高"};
const label = value => labels[value] || "提供方默认";

export function createMemberModelPicker({document, window, $, onChange, onModelChange, onDetails}) {
  const trigger = $("member-model-trigger"), panel = $("member-model-popover"), range = $("member-reasoning-effort");
  const modelTrigger = $("member-model-open-list"), menu = $("member-model-menu");
  let current, preview, opened = false, menuOpened = false, optionsKey, modelOptionsKey, menuIndex = 0;

  function position() {
    if (!opened) return;
    place(panel, trigger, 248);
    if (menuOpened) place(menu, modelTrigger, 248, true);
  }
  function place(surface, anchorNode, preferredWidth, centered = false) {
    const anchor = anchorNode.getBoundingClientRect(), width = Math.min(preferredWidth, window.innerWidth - 24);
    surface.style.setProperty("width", `${width}px`);
    const height = surface.getBoundingClientRect().height;
    const top = anchor.bottom + 6 + height <= window.innerHeight - 12 ? anchor.bottom + 6 : Math.max(12, anchor.top - height - 6);
    const left = centered ? (anchor.left + anchor.right - width) / 2 : anchor.right - width;
    surface.style.setProperty("left", `${Math.max(12, Math.min(left, window.innerWidth - width - 12))}px`);
    surface.style.setProperty("top", `${top}px`);
  }
  function paint() {
    if (!current) return;
    const selected = preview ?? current.selected, effective = selected || current.defaultEffort;
    const index = Math.max(0, current.efforts.indexOf(effective)), inherited = selected === "";
    range.min = "0"; range.max = String(current.efforts.length - 1); range.step = "1"; range.value = String(index);
    range.disabled = current.disabled;
    range.setAttribute("aria-valuetext", inherited ? `跟随模型默认：${label(effective)}` : label(selected));
    $("member-reasoning-value").textContent = label(effective);
    $("member-reasoning-summary").textContent = effective ? label(effective) : "默认";
    $("member-model-summary").textContent = current.modelName;
    $("member-model-name").textContent = current.modelName;
    trigger.disabled = current.disabled;
    const description = `${current.modelInherited ? "跟随默认模型：" : "模型："}${current.modelName}；推理强度：${inherited ? "跟随模型默认，" : ""}${label(effective)}`;
    trigger.setAttribute("aria-label", description); trigger.title = description;
    modelTrigger.disabled = current.disabled;
    modelTrigger.title = `${current.modelInherited ? "跟随默认模型。" : ""}切换模型；当前回合结束后应用，私有上下文将重置。`;
    $("member-picker-status").textContent = current.notice || "";
    $("member-picker-status").hidden = !current.notice;
    const reset = $("member-reasoning-default");
    reset.disabled = current.disabled; reset.setAttribute("aria-pressed", String(inherited));
    panel.style.setProperty("--reasoning-position", String(index / Math.max(1, current.efforts.length - 1)));
    panel.classList.toggle("reasoning-unset", inherited && !current.defaultEffort);
    panel.classList.toggle("reasoning-disabled", current.disabled);
    for (const [i, tick] of [...$("member-reasoning-ticks").children].entries()) {
      tick.classList.toggle("is-filled", i <= index && !!effective);
    }
    for (const option of $("member-model-options").children) {
      option.disabled = current.disabled;
      option.setAttribute("aria-checked", String(option.dataset.value === current.modelValue));
    }
  }
  function closeMenu(returnFocus = false) {
    if (!menuOpened) return;
    menuOpened = false; menu.hidePopover?.(); menu.hidden = true;
    modelTrigger.setAttribute("aria-expanded", "false");
    if (returnFocus) modelTrigger.focus();
  }
  function close(returnFocus = false) {
    if (!opened) return;
    opened = false; preview = undefined; closeMenu();
    panel.hidePopover?.(); panel.hidden = true;
    trigger.setAttribute("aria-expanded", "false"); paint();
    if (returnFocus) trigger.focus();
  }
  async function commit(value) {
    if (!current || current.disabled || value === undefined) return;
    preview = undefined;
    await onChange(value); paint();
  }
  function render(next) {
    if (current && (current.modelValue !== next.modelValue || current.efforts.join() !== next.efforts.join() || current.selected !== next.selected && preview !== next.selected)) preview = undefined;
    current = next;
    const key = next.efforts.join();
    if (key !== optionsKey) {
      optionsKey = key;
      const ticks = next.efforts.map((effort, i) => {
        const tick = document.createElement("span");
        tick.title = label(effort);
        tick.style.setProperty("--tick-position", String(i / Math.max(1, next.efforts.length - 1)));
        return tick;
      });
      $("member-reasoning-ticks").replaceChildren(...ticks);
    }
    const modelKey = JSON.stringify(next.models);
    if (modelKey !== modelOptionsKey) {
      modelOptionsKey = modelKey;
      $("member-model-options").replaceChildren(...next.models.map(model => {
        const option = document.createElement("button");
        option.type = "button"; option.className = "member-model-option"; option.dataset.value = model.value; option.title = model.name;
        option.setAttribute("role", "menuitemradio");
        const name = document.createElement("span"); name.textContent = model.name;
        const check = document.createElement("span"); check.className = "member-model-check"; check.textContent = "✓"; check.setAttribute("aria-hidden", "true");
        option.append(name, check);
        option.addEventListener("click", async () => {
          if (current.disabled) return;
          closeMenu(true); preview = undefined;
          await onModelChange(model.value); paint();
        });
        return option;
      }));
    }
    paint(); if (opened) position();
  }
  function mount() {
    panel.hidden = true; menu.hidden = true;
    trigger.addEventListener("click", () => {
      if (opened) { close(); return; }
      if (!current || current.disabled) return;
      opened = true; panel.hidden = false; trigger.setAttribute("aria-expanded", "true");
      panel.showPopover?.(); position(); range.focus({preventScroll:true});
    });
    range.addEventListener("input", () => {
      if (!current || current.disabled) return;
      preview = current.efforts[Number(range.value)]; paint();
    });
    range.addEventListener("change", () => commit(current?.efforts[Number(range.value)]));
    $("member-reasoning-default").addEventListener("click", () => commit(""));
    modelTrigger.addEventListener("click", () => {
      if (menuOpened) { closeMenu(); return; }
      if (!current || current.disabled) return;
      menuOpened = true; menu.hidden = false; modelTrigger.setAttribute("aria-expanded", "true");
      menu.showPopover?.(); position();
      menuIndex = Math.max(0, current.models.findIndex(model => model.value === current.modelValue));
      $("member-model-options").children[menuIndex]?.focus();
    });
    menu.addEventListener("keydown", event => {
      const options = [...$("member-model-options").children];
      if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key) || !options.length) return;
      event.preventDefault();
      menuIndex = event.key === "Home" ? 0 : event.key === "End" ? options.length - 1 : (menuIndex + (event.key === "ArrowDown" ? 1 : -1) + options.length) % options.length;
      options[menuIndex].focus();
    });
    $("member-model-info").addEventListener("click", () => { close(true); onDetails(); });
    panel.addEventListener("toggle", event => { if (event.newState === "closed") close(); });
    const dismissOutside = event => {
      if (menuOpened && !menu.contains(event.target) && !modelTrigger.contains(event.target)) closeMenu();
      if (opened && !panel.contains(event.target) && !menu.contains(event.target) && !trigger.contains(event.target)) close();
    };
    document.addEventListener("pointerdown", dismissOutside);
    document.addEventListener("click", dismissOutside);
    // Consume Escape before the owning member dialog handles the same event.
    window.addEventListener("keydown", event => {
      if (opened && event.key === "Escape") { event.preventDefault(); event.stopPropagation(); if (menuOpened) closeMenu(true); else close(true); }
    }, true);
    window.addEventListener("resize", position);
    document.addEventListener("scroll", position, true);
    $("activity-dialog").addEventListener("close", () => close());
  }
  return {mount, render, close};
}
