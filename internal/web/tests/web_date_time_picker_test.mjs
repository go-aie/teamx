import {test} from "node:test";
import assert from "node:assert/strict";
import {parseDateTime, dateTimeError} from "../assets/date-time-picker.mjs";
import {launch, settle} from "./web_app_harness.mjs";
import {fixture} from "./web_member_platform_fixture.mjs";

test("wall-date inputs reject invalid dates, offsets and precision loss", () => {
  assert.equal(parseDateTime("2028-02-29", "date"), "2028-02-29");
  assert.equal(parseDateTime("09:30", "time", true), "09:30:00");
  assert.equal(parseDateTime("2026-11-19 09:30:27", "datetime", true), "2026-11-19T09:30:27");
  for (const value of ["2027-02-29", "2026-02-30", "2026-13-01", "0000-01-01", "2026-1-1"]) assert.ok(dateTimeError(value, "date"), value);
  for (const value of ["24:00", "09:60", "09:00:60", "09:00:01", "9:00"]) assert.ok(dateTimeError(value, "time"), value);
  assert.ok(dateTimeError("2026-11-19T09:30:27Z", "datetime", {seconds:true}));
  assert.ok(dateTimeError("2026-11-19T09:30:28", "datetime", {seconds:true,max:"2026-11-19T09:30:27"}));
  assert.equal(dateTimeError("2026-11-19T09:30:27", "datetime", {seconds:true,max:"2026-11-19T09:30:27"}), "");
  assert.equal(dateTimeError("", "date"), "");
  assert.ok(dateTimeError("", "date", {required:true}));
});

const gridChoice = (ui, value) => ui.el("date-time-grid").children.find(element => element.dataset.dateValue === value);
const fillClock = async (ui, hour, minute, second) => {
  for (const [part, value] of [["hour",hour],["minute",minute],["second",second]]) {
    if (value === undefined) continue;
    ui.el(`date-time-${part}`).value = value; await ui.el(`date-time-${part}`).emit("input");
  }
};

test("pager changes months, years or year groups without committing and keeps the browsing month", async () => {
  const api = fixture(), ui = await launch(api), input = ui.el("policy-expires");
  input.value = "2026-11-19 09:30:27";
  await ui.el("policy-expires-toggle").emit("click");
  assert.equal(ui.el("date-time-next").attributes["aria-label"], "下一个月");
  await ui.el("date-time-next").emit("click"); assert.equal(ui.el("date-time-month").textContent, "12月");
  await ui.el("date-time-prev").emit("click");
  await ui.el("date-time-month").emit("click");
  assert.equal(ui.el("date-time-next").attributes["aria-label"], "下一年");
  await ui.el("date-time-next").emit("click");
  assert.equal(ui.el("date-time-year").textContent, "2027年");
  assert.equal(ui.el("date-time-month").textContent, "11月");
  assert.equal(ui.el("date-time-grid").dataset.level, "months");
  await ui.el("date-time-prev").emit("click"); assert.equal(ui.el("date-time-year").textContent, "2026年");
  await ui.el("date-time-year").emit("click");
  const firstYear = Number(ui.el("date-time-grid").children[0].dataset.dateValue);
  assert.equal(ui.el("date-time-next").attributes["aria-label"], "下一组年份");
  await ui.el("date-time-next").emit("click");
  assert.equal(Number(ui.el("date-time-grid").children[0].dataset.dateValue), firstYear + 12);
  await ui.el("date-time-prev").emit("click");
  assert.equal(Number(ui.el("date-time-grid").children[0].dataset.dateValue), firstYear);
  assert.equal(input.value, "2026-11-19 09:30:27");
  assert.equal(ui.el("date-time-picker").hidden, false);
  assert.equal(api.calls.some(call => /\/update|\/save/.test(call.path)), false);
});

test("pager bounds apply to buttons and PageUp/PageDown, including the first and last year groups", async t => {
  t.mock.method(Date, "now", () => Date.parse("2026-11-19T01:30:27Z"));
  const ui = await launch(fixture({empty:true})), input = ui.el("gitlab-backfill-start");
  input.disabled = false; input.value = "2026-11-19 09:30:27"; input.min = "2026-11-01T00:00:00";
  await ui.el("gitlab-backfill-start-toggle").emit("click");
  assert.equal(ui.el("date-time-prev").disabled, true); assert.equal(ui.el("date-time-next").disabled, true);
  await ui.el("date-time-grid").emit("keydown", {target:gridChoice(ui,"2026-11-19"),key:"PageDown"});
  assert.equal(ui.el("date-time-month").textContent, "11月");
  await ui.el("date-time-month").emit("click");
  assert.equal(ui.el("date-time-prev").disabled, true); assert.equal(ui.el("date-time-next").disabled, true);
  await ui.el("date-time-year").emit("click");
  assert.equal(ui.el("date-time-prev").disabled, true); assert.equal(ui.el("date-time-next").disabled, true);
  await ui.el("date-time-cancel").emit("click");
  const date = ui.el("calendar-start-date");
  for (const [value,blocked,allowed,edge] of [["0012-11-19","prev","next","1"],["9990-11-19","next","prev","9999"]]) {
    date.value = value; await ui.el("calendar-start-date-toggle").emit("click"); await ui.el("date-time-year").emit("click");
    // A partial final/first group remains reachable before the boundary disables.
    await ui.el(`date-time-${blocked}`).emit("click");
    assert.ok(gridChoice(ui,edge));
    assert.equal(ui.el(`date-time-${blocked}`).disabled, true);
    assert.equal(ui.el(`date-time-${allowed}`).disabled, false);
    await ui.el("date-time-cancel").emit("click");
  }
});

test("month/year browsing keeps the month and never commits until a date or confirmation is chosen", async () => {
  const api = fixture(), ui = await launch(api);
  ui.el("policy-expires").value = "2026-11-19T09:30:27";
  await ui.el("policy-expires-toggle").emit("click");
  assert.equal(ui.el("date-time-second-field").hidden, false);
  assert.equal(ui.el("date-time-zone").hidden, true);
  assert.doesNotMatch(ui.el("policy-expires-hint").textContent, /时区|Asia\/Shanghai/);
  await ui.el("date-time-month").emit("click");
  assert.equal(ui.el("date-time-grid").children.length, 12);
  await ui.el("date-time-grid").emit("keydown", {target:gridChoice(ui, "11"),key:"Home"});
  assert.equal(ui.document.activeElement.dataset.dateValue, "1");
  await ui.el("date-time-grid").emit("keydown", {target:gridChoice(ui, "1"),key:"End"});
  assert.equal(ui.document.activeElement.dataset.dateValue, "12");
  await gridChoice(ui, "5").emit("click");
  await ui.document.emit("click", {target:{},composedPath:()=>[ui.el("date-time-grid"),ui.el("date-time-picker")]});
  assert.equal(ui.el("date-time-picker").hidden, false);
  await ui.el("date-time-year").emit("click");
  await gridChoice(ui, "2027").emit("click");
  assert.equal(ui.el("date-time-month").textContent, "5月");
  assert.equal(ui.el("policy-expires").value, "2026-11-19T09:30:27");
  await gridChoice(ui, "2027-05-17").emit("click");
  await fillClock(ui, "14", "25", "37");
  assert.equal(ui.el("policy-expires").value, "2026-11-19T09:30:27");
  await ui.el("date-time-apply").emit("click"); await settle();
  assert.equal(ui.el("policy-expires").value, "2027-05-17 14:25:37");
  assert.equal(ui.el("date-time-picker").hidden, true);
  assert.equal(api.calls.some(call => /\/update|\/save/.test(call.path)), false);
});

test("cancel and Escape discard drafts; clearing an optional expiry dispatches the existing input events", async () => {
  const ui = await launch(fixture());
  ui.el("policy-expires").value = "2026-11-19T09:30:27";
  await ui.el("policy-expires-toggle").emit("click");
  await fillClock(ui, "15", "20", "30");
  await ui.el("date-time-cancel").emit("click");
  assert.equal(ui.el("policy-expires").value, "2026-11-19T09:30:27");
  await ui.el("policy-expires-toggle").emit("click");
  await ui.el("date-time-picker").emit("keydown", {key:"Escape"});
  assert.equal(ui.el("date-time-picker").hidden, true);
  assert.equal(ui.document.activeElement, ui.el("policy-expires-toggle"));
  await ui.el("policy-expires-toggle").emit("click");
  await ui.el("date-time-clear").emit("click");
  assert.equal(ui.el("policy-expires").value, "");
});

test("notification bounds include seconds, disable future days and follow the clock while open", async t => {
  let tick = Date.parse("2026-11-19T01:30:27Z");
  t.mock.method(Date, "now", () => tick);
  const ui = await launch(fixture({empty:true})), input = ui.el("gitlab-backfill-start");
  input.disabled = false; input.value = "2026-11-19T09:30:20"; input.max = "2026-11-19T09:30:27";
  await ui.el("gitlab-backfill-start-toggle").emit("click");
  assert.equal(gridChoice(ui, "2026-11-20").disabled, true);
  await ui.el("date-time-year").emit("click");
  assert.equal(gridChoice(ui, "2027").disabled, true);
  await ui.el("date-time-year").emit("click");
  await fillClock(ui, "9", "30", "28");
  assert.equal(ui.el("date-time-apply").disabled, true);
  assert.match(ui.el("date-time-error").textContent, /不能晚于/);
  await fillClock(ui, "9", "30", "27");
  assert.equal(ui.el("date-time-apply").disabled, false);
  tick += 5000;
  await ui.el("date-time-today").emit("click");
  assert.equal(ui.el("date-time-second").value, "32");
  assert.equal(ui.el("date-time-apply").disabled, false);
  await ui.el("date-time-cancel").emit("click");
  assert.equal(input.value, "2026-11-19T09:30:20");
});

test("task date selection commits immediately; clock-only selection stays at minute precision", async () => {
  const ui = await launch(fixture()), input = ui.el("calendar-start-date");
  input.value = "2026-12-31"; input.required = true;
  await ui.el("calendar-start-date-toggle").emit("click");
  assert.equal(ui.el("date-time-clock").hidden, true);
  assert.equal(ui.el("date-time-clear").hidden, true);
  await ui.el("date-time-grid").emit("keydown", {target:gridChoice(ui, "2026-12-31"), key:"ArrowRight"});
  assert.equal(ui.el("date-time-year").textContent, "2027年");
  await gridChoice(ui, "2027-01-01").emit("click");
  assert.equal(input.value, "2027-01-01");
  assert.equal(ui.el("date-time-picker").hidden, true);
  ui.el("calendar-local-time").value = "09:30";
  await ui.el("calendar-local-time-toggle").emit("click");
  assert.equal(ui.el("date-time-calendar").hidden, true);
  assert.equal(ui.el("date-time-zone").hidden, true);
  assert.equal(ui.el("date-time-second-field").hidden, true);
  await fillClock(ui, "14", "5");
  await ui.el("date-time-apply").emit("click");
  assert.equal(ui.el("calendar-local-time").value, "14:05");
  ui.el("calendar-local-time").value = "09:00:00";
  await ui.el("calendar-local-time").emit("change");
  assert.equal(ui.el("calendar-local-time").value, "09:00");
});

test("incomplete clock drafts survive date browsing and never become valid defaults", async () => {
  const ui = await launch(fixture());
  ui.el("policy-expires").value = "2026-11-19T09:30:27";
  await ui.el("policy-expires-toggle").emit("click");
  await fillClock(ui, "", "30", "27");
  await gridChoice(ui, "2026-11-20").emit("click");
  assert.equal(ui.el("date-time-hour").value, "");
  assert.equal(ui.el("date-time-apply").disabled, true);
  await ui.el("date-time-cancel").emit("click");
  assert.equal(ui.el("policy-expires").value, "2026-11-19T09:30:27");
});
