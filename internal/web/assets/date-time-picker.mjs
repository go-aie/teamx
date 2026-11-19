import { Temporal } from "./vendor/temporal.mjs";

const pad = n => String(n).padStart(2, "0");
const datePattern = /^\d{4}-\d{2}-\d{2}$/;
const timePattern = /^\d{2}:\d{2}(?::\d{2})?$/;

// Wall dates/times stay separate from instants. The owner still interprets a
// committed value in the system or task zone, including DST validation.
export function parseDateTime(value, kind, seconds = false) {
  if (kind === "date") {
    if (!datePattern.test(value)) throw Error("请按 YYYY-MM-DD 输入日期。");
    const date = Temporal.PlainDate.from(value);
    if (date.year < 1 || date.year > 9999) throw Error("年份须在 1 至 9999 之间。");
    return date.toString();
  }
  if (kind === "time") {
    if (!timePattern.test(value)) throw Error(`请按 HH:mm${seconds ? ":ss" : ""} 输入时刻。`);
    const [hour, minute, second = 0] = value.split(":").map(Number);
    if (hour > 23 || minute > 59 || second > 59) throw new RangeError("invalid clock");
    const time = Temporal.PlainTime.from(value);
    if (!seconds && time.second) throw Error("执行时刻精确到分钟。");
    return time.toString({smallestUnit: seconds ? "second" : "minute"});
  }
  const parts = value.split(/[T ]/);
  if (parts.length !== 2) throw Error("请按 YYYY-MM-DD HH:mm:ss 输入日期和时间。");
  return `${parseDateTime(parts[0], "date")}T${parseDateTime(parts[1], "time", seconds)}`;
}

export function dateTimeError(value, kind, {seconds = false, min = "", max = "", required = false} = {}) {
  if (!value) return required ? "请填写完整的日期或时间。" : "";
  try {
    const parsed = parseDateTime(value, kind, seconds);
    if (min && parsed < parseDateTime(min, kind, seconds)) return `不能早于 ${min.replace("T", " ")}。`;
    if (max && parsed > parseDateTime(max, kind, seconds)) return `不能晚于 ${max.replace("T", " ")}。`;
    return "";
  } catch (error) {
    return error instanceof RangeError ? "请输入有效的日期和时间。" : error.message;
  }
}

export function createDateTimePicker({env}) {
  const {document, window} = env;
  const $ = id => document.getElementById(id);
  const bindings = new Map();
  let active = null, draftDate = "", draftTime = "", cursor = null, level = "days", focusValue = "";
  const panel = () => $("date-time-picker");
  const node = (tag, text, className) => {
    const element = document.createElement(tag);
    if (text !== undefined) element.textContent = text;
    if (className) element.className = className;
    return element;
  };
  function button(text, handler, className) {
    const element = node("button", text, className); element.type = "button";
    element.addEventListener("click", handler); return element;
  }
  function now() {
    try {
      const zone = active.zone();
      if (!zone || zone === "Local") return null;
      return Temporal.Instant.fromEpochMilliseconds(Date.now()).toZonedDateTimeISO(zone);
    } catch { return null; }
  }
  const limits = binding => ({seconds:binding.seconds, min:binding.input.min, max:binding.max ? binding.max() : binding.input.max, required:binding.input.required});
  function validate(input) {
    const binding = bindings.get(input);
    if (!binding) return "";
    const error = dateTimeError(input.value, binding.kind, limits(binding));
    input.setCustomValidity?.(error);
    return error;
  }
  function candidate() { return active.kind === "time" ? draftTime : active.kind === "date" ? draftDate : `${draftDate}T${draftTime}`; }
  function status() {
    if (!active) return;
    const error = active.kind !== "date" && !now() ? "时区未确定，请在设置中检查。" : dateTimeError(candidate(), active.kind, {...limits(active), required:true});
    $("date-time-error").textContent = error;
    $("date-time-error").hidden = !error;
    $("date-time-apply").disabled = !!error || active.input.disabled;
  }
  function position() {
    if (!active) return;
    const viewport = window.visualViewport;
    const width = viewport?.width || window.innerWidth, height = viewport?.height || window.innerHeight;
    const top = viewport?.offsetTop || 0, left = viewport?.offsetLeft || 0;
    panel().style.setProperty("max-height", `${height - 16}px`); panel().style.setProperty("max-width", `${width - 16}px`);
    const rect = active.trigger.getBoundingClientRect(), box = panel().getBoundingClientRect();
    const x = Math.max(left + 8, Math.min(rect.left, left + width - box.width - 8));
    const below = rect.bottom + 6;
    const y = below + box.height <= top + height - 8 ? below : Math.max(top + 8, rect.top - box.height - 6);
    panel().style.setProperty("left", `${x}px`); panel().style.setProperty("top", `${y}px`);
  }
  function close(restore = false) {
    if (!active) return;
    const binding = active; active = null;
    if (panel().hidePopover && panel().matches?.(":popover-open")) panel().hidePopover();
    panel().hidden = true;
    binding.trigger.setAttribute("aria-expanded", "false");
    if (restore) binding.trigger.focus({preventScroll:true});
  }
  function emit(input, type) {
    input.dispatchEvent(new window.Event(type, {bubbles:true}));
  }
  function commit(value = candidate()) {
    if (!active || active.input.disabled || active.input.readOnly) { close(); return; }
    const binding = active;
    const error = dateTimeError(value, binding.kind, limits(binding));
    if (error) { status(); return; }
    const normalized = value ? parseDateTime(value, binding.kind, binding.seconds) : "";
    let changed = true;
    try { changed = (binding.input.value ? parseDateTime(binding.input.value, binding.kind, binding.seconds) : "") !== normalized; } catch { /* Invalid text is replaced by the chosen value. */ }
    // An unchanged selection must keep the owner's resolved instant, including
    // milliseconds and the chosen offset in a repeated DST hour.
    const notify = binding.navigation || changed || binding.confirmUnchanged?.();
    if (changed) binding.input.value = binding.kind === "datetime" ? normalized.replace("T", " ") : normalized;
    validate(binding.input);
    close(true);
    // Existing business handlers own dirty state, time-zone conversion and save.
    if (notify) { emit(binding.input, "input"); emit(binding.input, "change"); }
  }
  function dayAllowed(date) {
    const bounds = limits(active), min = bounds.min?.slice(0,10), max = bounds.max?.slice(0,10);
    return (!min || date >= min) && (!max || date <= max);
  }
  function periodAllowed(start, end) {
    const bounds = limits(active);
    return (!bounds.min || end >= bounds.min.slice(0,10)) && (!bounds.max || start <= bounds.max.slice(0,10));
  }
  const yearPageStart = year => Math.max(1, Math.min(9988, Math.floor(year / 12) * 12));
  function pageTarget(direction) {
    const delta = level === "days" ? {months:direction} : {years:direction * (level === "years" ? 12 : 1)};
    let next = cursor.add(delta);
    if (level === "years") {
      next = cursor.with({year:Math.max(1, Math.min(9999, next.year))});
      if (yearPageStart(next.year) === yearPageStart(cursor.year)) return null;
    } else if (next.year < 1 || next.year > 9999) return null;
    const start = level === "days" ? next.with({day:1}) : next.with({year:level === "years" ? yearPageStart(next.year) : next.year, month:1, day:1});
    const end = level === "days" ? start.with({day:start.daysInMonth}) : start.with({year:start.year + (level === "years" ? 11 : 0), month:12, day:31});
    return periodAllowed(start.toString(), end.toString()) ? next : null;
  }
  function focusGrid(value = focusValue) {
    const buttons = [...$("date-time-grid").children].filter(element => element.dataset.dateValue);
    const selected = buttons.find(element => element.dataset.dateValue === value && !element.disabled) || buttons.find(element => !element.disabled);
    for (const element of buttons) element.tabIndex = element === selected ? 0 : -1;
    selected?.focus({preventScroll:true});
  }
  function render(focus = false) {
    const clock = now(), today = clock?.toPlainDate().toString();
    const root = $("date-time-grid"); root.replaceChildren(); root.dataset.level = level;
    const year = cursor.year, month = cursor.month;
    const yearStart = yearPageStart(year);
    $("date-time-year").textContent = level === "years" ? `${yearStart} — ${yearStart + 11}年` : `${year}年`;
    $("date-time-month").textContent = `${month}月`;
    $("date-time-month").style.setProperty("visibility", level === "years" ? "hidden" : "visible");
    $("date-time-year").setAttribute("aria-label", level === "years" ? "返回日期选择" : "选择年份");
    $("date-time-month").setAttribute("aria-label", level === "months" ? "返回日期选择" : "选择月份");
    $("date-time-year").setAttribute("aria-expanded", String(level === "years"));
    $("date-time-month").setAttribute("aria-expanded", String(level === "months"));
    root.setAttribute("aria-label", level === "days" ? `${year}年${month}月的日期` : level === "months" ? `${year}年的月份` : "年份");
    const interval = level === "days" ? "个月" : level === "months" ? "年" : "组年份";
    $("date-time-navigation").dataset.level = level;
    for (const [id, direction, prefix] of [["date-time-prev", -1, "上一"], ["date-time-next", 1, "下一"]]) {
      $(id).setAttribute("aria-label", `${prefix}${interval}`);
      $(id).title = `${prefix}${interval}`;
      $(id).disabled = !pageTarget(direction);
    }
    const range = new Set(active.range?.(draftDate) || []);
    const selectedDate = draftDate || today;
    const add = (label, value, handler, disabled = false) => {
      const element = button("", handler, level === "days" ? "date-time-day" : "date-time-choice");
      element.append(node("span", label)); element.dataset.dateValue = value; element.disabled = disabled;
      element.tabIndex = value === focusValue && !disabled ? 0 : -1;
      root.append(element); return element;
    };
    if (level === "days") {
      for (const label of ["一", "二", "三", "四", "五", "六", "日"]) {
        const header = node("span", label, "date-time-weekday"); header.setAttribute("aria-hidden", "true"); root.append(header);
      }
      const first = cursor.with({day:1});
      const start = first.subtract({days:first.dayOfWeek - 1});
      for (let i = 0; i < 42; i++) {
        const date = start.add({days:i}), value = date.toString();
        const element = add(String(date.day), value, () => {
          draftDate = value; focusValue = value; cursor = date;
          if (active.kind === "date") commit(value); else render(true);
        }, date.year < 1 || date.year > 9999 || !dayAllowed(value));
        element.dataset.currentMonth = String(date.month === month);
        element.dataset.inRange = String(range.has(value));
        element.setAttribute("aria-label", `${date.year}年${date.month}月${date.day}日${value === today ? "，今天" : ""}`);
        element.setAttribute("aria-pressed", String(value === draftDate));
        if (value === today) element.setAttribute("aria-current", "date");
      }
    } else {
      for (let i = 0; i < 12; i++) {
        const value = level === "months" ? i + 1 : yearStart + i;
        const target = level === "months" ? cursor.with({month:value, day:1}) : cursor.with({year:value, month:1, day:1});
        const end = target.add(level === "months" ? {months:1} : {years:1}).subtract({days:1});
        const element = add(`${value}${level === "months" ? "月" : "年"}`, String(value), () => {
          cursor = level === "months" ? cursor.with({month:value}) : cursor.with({year:value});
          level = "days"; focusValue = cursor.toString(); render(true);
        }, !periodAllowed(target.toString(), end.toString()));
        element.setAttribute("aria-pressed", String(selectedDate && (level === "months" ? Number(selectedDate.slice(0,4)) === year && Number(selectedDate.slice(5,7)) === value : Number(selectedDate.slice(0,4)) === value)));
      }
    }
    if (![...root.children].some(element => element.tabIndex === 0)) {
      const first = [...root.children].find(element => element.dataset.dateValue && !element.disabled); if (first) first.tabIndex = 0;
    }
    try {
      const time = Temporal.PlainTime.from(draftTime);
      for (const [key, value] of [["hour", time.hour], ["minute", time.minute], ["second", time.second]]) $("date-time-" + key).value = pad(value);
    } catch { /* Browsing dates must preserve incomplete clock edits. */ }
    $("date-time-second-field").hidden = !active.seconds;
    const showZone = typeof active.showZone === "function" ? active.showZone() : active.showZone;
    $("date-time-zone").textContent = showZone ? `时区：${active.zone() || "未确定"}` : "";
    $("date-time-zone").hidden = !$("date-time-zone").textContent;
    $("date-time-today").textContent = active.kind === "date" ? "今天" : "现在";
    $("date-time-today").disabled = !clock || active.kind === "date" && !dayAllowed(today);
    $("date-time-clear").hidden = !!active.input.required || !!active.navigation;
    $("date-time-confirm").hidden = !!active.navigation;
    $("date-time-apply").hidden = active.kind === "date";
    status(); position(); if (focus) focusGrid();
  }
  function open(binding) {
    if (binding.input.disabled || binding.input.readOnly) return;
    if (active === binding) { close(true); return; }
    close(); active = binding; level = "days";
    const clock = now();
    let value = ""; try { if (binding.input.value) value = parseDateTime(binding.input.value, binding.kind, binding.seconds); } catch { /* Keep invalid text for the owner to correct. */ }
    draftDate = binding.kind === "time" ? "" : value.slice(0,10) || clock?.toPlainDate().toString() || "2000-01-01";
    draftTime = binding.kind === "date" ? "00:00" : (binding.kind === "time" ? value : value.split("T")[1]) || "00:00";
    cursor = Temporal.PlainDate.from(draftDate || clock?.toPlainDate().toString() || "2000-01-01"); focusValue = cursor.toString();
    const element = panel();
    element.dataset.navigation = String(binding.navigation);
    (binding.trigger.closest?.("dialog") || document.body)?.append(element);
    $("date-time-calendar").hidden = binding.kind === "time";
    $("date-time-clock").hidden = binding.kind === "date";
    element.setAttribute("aria-label", binding.label);
    binding.trigger.setAttribute("aria-expanded", "true");
    element.hidden = false; element.showPopover?.();
    render();
    if (binding.kind === "time") $("date-time-hour").focus(); else focusGrid();
  }
  function register(input, {kind, seconds = false, trigger, zone, label, navigation = false, range, showZone = false, confirmUnchanged, max}) {
    const binding = {input, kind, seconds, trigger, zone, label, navigation, range, showZone, confirmUnchanged, max};
    bindings.set(input, binding);
    trigger.setAttribute("aria-haspopup", "dialog"); trigger.setAttribute("aria-controls", "date-time-picker"); trigger.setAttribute("aria-expanded", "false");
    trigger.addEventListener("click", () => open(binding));
    input.addEventListener("input", () => validate(input));
    input.addEventListener("change", () => {
      if (!validate(input) && input.value && kind === "time") input.value = parseDateTime(input.value, kind, seconds);
    });
    input.addEventListener("keydown", event => {
      if (event.key === "ArrowDown" && event.altKey) { event.preventDefault(); open(binding); }
    });
  }
  function move(direction) {
    const next = pageTarget(direction);
    if (!next) return;
    cursor = next; focusValue = level === "days" ? cursor.toString() : String(level === "months" ? cursor.month : cursor.year); render();
  }
  function mount() {
    $("date-time-prev").addEventListener("click", () => move(-1)); $("date-time-next").addEventListener("click", () => move(1));
    $("date-time-month").addEventListener("click", () => { level = level === "months" ? "days" : "months"; focusValue = level === "months" ? String(cursor.month) : cursor.toString(); render(true); });
    $("date-time-year").addEventListener("click", () => { level = level === "years" ? "days" : "years"; focusValue = level === "years" ? String(cursor.year) : cursor.toString(); render(true); });
    $("date-time-cancel").addEventListener("click", () => close(true));
    $("date-time-apply").addEventListener("click", () => commit());
    $("date-time-clear").addEventListener("click", () => commit(""));
    $("date-time-today").addEventListener("click", () => {
      const clock = now(); if (!clock) return;
      draftDate = clock.toPlainDate().toString(); draftTime = clock.toPlainTime().toString({smallestUnit:active.seconds ? "second" : "minute"});
      cursor = clock.toPlainDate(); focusValue = draftDate; level = "days";
      if (active.kind === "date") commit(draftDate); else render();
    });
    for (const part of ["hour", "minute", "second"]) $("date-time-" + part).addEventListener("input", () => {
      if (!active) return;
      const partValue = name => { const value = $("date-time-" + name).value; return value ? pad(value) : ""; };
      draftTime = `${partValue("hour")}:${partValue("minute")}${active.seconds ? `:${partValue("second")}` : ""}`;
      status();
    });
    $("date-time-grid").addEventListener("keydown", event => {
      const value = event.target.dataset.dateValue; if (!value) return;
      const step = {ArrowLeft:-1, ArrowRight:1, ArrowUp:level === "days" ? -7 : -3, ArrowDown:level === "days" ? 7 : 3}[event.key];
      if (step) {
        event.preventDefault();
        if (level === "days") {
          const next = Temporal.PlainDate.from(value).add({days:step});
          if (next.year < 1 || next.year > 9999 || !dayAllowed(next.toString())) return;
          cursor = next; focusValue = next.toString(); render(true);
        } else {
          const next = Number(value) + step;
          if (level === "months" && (next < 1 || next > 12) || level === "years" && (next < 1 || next > 9999)) return;
          if (level === "years") cursor = cursor.with({year:next});
          focusValue = String(next); render(true);
        }
      } else if (event.key === "PageUp" || event.key === "PageDown") { event.preventDefault(); move(event.key === "PageUp" ? -1 : 1); focusGrid(); }
      else if (event.key === "Home" || event.key === "End") {
        event.preventDefault();
        if (level === "days") {
          const date = Temporal.PlainDate.from(value), offset = event.key === "Home" ? 1 - date.dayOfWeek : 7 - date.dayOfWeek;
          const next = date.add({days:offset});
          if (next.year < 1 || next.year > 9999 || !dayAllowed(next.toString())) return;
          cursor = next; focusValue = cursor.toString(); render(true);
        } else {
          const choices = [...$("date-time-grid").children].filter(element => !element.disabled);
          const next = event.key === "Home" ? choices[0] : choices.at(-1);
          if (next) { focusValue = next.dataset.dateValue; focusGrid(); }
        }
      }
    });
    panel().addEventListener("keydown", event => {
      if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); close(true); }
      else if (event.key === "Enter" && event.target.tagName === "INPUT") { event.preventDefault(); if (!$("date-time-apply").disabled) commit(); }
    });
    document.addEventListener("click", event => {
      // The clicked grid cell may have been replaced while switching levels.
      // Its original event path still belongs to this popover.
      if (active && !event.composedPath?.().includes(panel()) && !panel().contains?.(event.target) && !active.trigger.contains?.(event.target)) close();
    });
    document.addEventListener("focusin", event => {
      if (active && !panel().contains?.(event.target) && !active.trigger.contains?.(event.target)) close();
    });
    document.addEventListener("close", event => { if (active && event.target.contains?.(active.trigger)) close(); }, true);
    window.addEventListener("resize", position); window.visualViewport?.addEventListener("resize", position); window.visualViewport?.addEventListener("scroll", position);
    document.addEventListener("scroll", event => { if (active && !panel().contains?.(event.target)) position(); }, true);
    if (env.MutationObserver) new env.MutationObserver(() => {
      for (const binding of bindings.values()) {
        const disabled = !!(binding.input.disabled || binding.input.readOnly);
        if (binding.trigger.disabled !== disabled) binding.trigger.disabled = disabled;
      }
      if (active && (active.input.disabled || active.trigger.getClientRects().length === 0)) close();
    }).observe(document.body, {attributes:true, subtree:true, attributeFilter:["hidden", "open", "disabled", "readonly", "class"]});
  }
  return {mount, register, validate, close, get isOpen() { return !!active; }};
}
