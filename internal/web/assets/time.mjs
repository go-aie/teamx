import { Temporal } from "./vendor/temporal.mjs";

const unavailable = "系统时区异常";
export function createSystemTime() {
  let epoch = "", version = 0, zone = "", error = unavailable;
  const formatters = new Map();
  function update(data) {
    if (data.service_epoch === epoch && data.settings_version < version) return false;
    if (data.service_epoch === epoch && data.settings_version === version && data.time_zone === zone) return false;
    epoch = data.service_epoch;
    version = data.settings_version;
    zone = data.time_zone;
    error = "";
    try {
      if (!epoch || !Number.isSafeInteger(version) || version < 1 || !zone || zone === "Local") throw new Error();
      new Intl.DateTimeFormat("zh-CN", {timeZone: zone}).format(0);
    } catch { error = "系统时区缺失或当前浏览器不支持，请在设置中检查。"; }
    formatters.clear();
    return true;
  }
  function requireZone() { if (error) throw new Error(error); return zone; }
  function format(value, options = {year:"numeric",month:"2-digit",day:"2-digit",hour:"2-digit",minute:"2-digit",second:"2-digit"}) {
    if (error) return unavailable;
    const date = new Date(value);
    if (!Number.isFinite(date.getTime())) return "时间未获取";
    const key = JSON.stringify(options);
    if (!formatters.has(key)) formatters.set(key, new Intl.DateTimeFormat("zh-CN", {hourCycle:"h23", ...options, timeZone:zone}));
    return formatters.get(key).format(date);
  }
  function local(value) {
    if (error) return "";
    return Temporal.Instant.from(new Date(value).toISOString()).toZonedDateTimeISO(zone).toPlainDateTime().toString({smallestUnit:"second"});
  }
  function parse(value) {
    requireZone();
    try {
      return new Date(Number(Temporal.PlainDateTime.from(value).toZonedDateTime(zone, {disambiguation:"reject"}).epochMilliseconds)).toISOString();
    } catch { throw new Error(`请输入 ${zone} 的有效时间；夏令时跳过或重复的时间无法确定，请选择其他时间。`); }
  }
  function day(value) {
    if (error) return unavailable;
    return Temporal.Instant.from(new Date(value).toISOString()).toZonedDateTimeISO(zone).toPlainDate().toString();
  }
  function recent(value, now = Date.now()) {
    if (error || !Number.isFinite(new Date(value).getTime())) return format(value);
    const date = Temporal.PlainDate.from(day(value)), today = Temporal.PlainDate.from(day(now));
    if (date.equals(today)) return format(value, {hour:"2-digit",minute:"2-digit"});
    if (date.equals(today.subtract({days:1}))) return "昨天";
    return `${date.year === today.year ? "" : `${date.year}年`}${date.month}月${date.day}日`;
  }
  return { update, format, local, parse, day, recent, get zone() { return zone; }, get version() { return version; }, get epoch() { return epoch; }, get error() { return error; } };
}

// Keep a resolved instant even when the displayed wall clock repeats at DST end.
// Unresolved drafts require an explicit edit/confirmation after a zone change.
export function createTimeInput(clock) {
  let raw = "", instant = "", error = "", confirm = false;
  function sameWallTime(value) {
    if (value === raw) return true;
    // datetime-local normalizes :00 seconds away. That must not discard a
    // saved instant's offset/precision, especially during a repeated DST hour.
    try { return Temporal.PlainDateTime.from(value).equals(Temporal.PlainDateTime.from(raw)); }
    catch { return false; }
  }
  return {
    fill(value) { instant = value || ""; raw = instant ? clock.local(instant) : ""; error = ""; confirm = false; return raw; },
    edit(value, incomplete = false) {
      raw = value; instant = ""; error = ""; confirm = false;
      if (incomplete) { error = "请输入完整有效的时间。"; return; }
      if (value) { try { instant = clock.parse(value); } catch (e) { error = e.message; } }
    },
    rebase() { if (instant) raw = clock.local(instant); else if (raw || error) confirm = true; return raw; },
    read(value) {
      if (!sameWallTime(value)) this.edit(value);
      if (confirm) throw new Error("系统时区已变更，请重新确认或修改此时间。");
      if (clock.error && value) throw new Error(clock.error);
      if (error) throw new Error(error);
      return instant;
    },
  };
}
