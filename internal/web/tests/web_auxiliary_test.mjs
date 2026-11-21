import { test } from "node:test";
import assert from "node:assert/strict";
import { launch, settle } from "./web_app_harness.mjs";
import { fixture } from "./web_member_platform_fixture.mjs";

function auxiliaryFixture() {
  const api = fixture(), request = api.request.bind(api);
  api.members.splice(1);
  api.request = async (path, ...args) => {
    const value = await request(path, ...args);
    if (path === "/v1/rooms") value.rooms[0].is_direct = true;
    return value;
  };
  api.sources = [{membership_id:"member-a",root_id:`wr1_${"a".repeat(64)}`,label:"助理 A",available:true}];
  api.hook = path => {
    if (path === "/v1/calendar/list") return {revision:1,time_zone:"Asia/Shanghai",tasks:[],occurrences:[],days:[]};
    if (path.startsWith("/v1/workspace/files/sources?")) return {sources:structuredClone(api.sources),limits:{}};
    if (path.startsWith("/v1/workspace/files?")) return {source:api.sources[0],path:"",entries:[{name:"note.md",path:"note.md",kind:"file",size:16}]};
  };
  return api;
}
async function files(ui) {
  await ui.el("room-menu-label").emit("click");
  await ui.el("conversation-open-files").emit("click");
  await settle();
}
async function calendar(ui) { await ui.el("conversation-calendar").emit("click"); await settle(); }

test("auxiliary switching preserves calendar dates, file filters and live chat position", async () => {
  const api = auxiliaryFixture(), ui = await launch(api);
  await ui.draft("边查看边继续编辑");
  await calendar(ui);
  assert.equal(ui.el("calendar-page").hidden, false);
  assert.equal(ui.el("timeline-wrap").hidden, false);
  assert.equal(ui.el("compose").hidden, false);
  assert.equal(ui.el("conversation").inert, false);
  await ui.el("calendar-next").emit("click"); await settle();
  const date = ui.el("calendar-date").value;
  await files(ui);
  assert.equal(ui.el("calendar-page").hidden, true);
  ui.el("files-filter").value = "note"; await ui.el("files-filter").emit("input");
  ui.el("files-body").scrollTop = 78;
  await calendar(ui);
  assert.equal(ui.el("files-page").hidden, true);
  assert.equal(ui.el("calendar-date").value, date);
  await files(ui);
  assert.equal(ui.el("files-filter").value, "note");
  assert.equal(ui.el("files-body").scrollTop, 78);
  ui.el("timeline").scrollTop = 91;
  await ui.draft("编辑后的草稿");
  await ui.el("files-close").emit("click");
  assert.equal(ui.el("timeline").scrollTop, 91);
  assert.equal(ui.el("message-input").value, "编辑后的草稿");
  await calendar(ui);
  await ui.el("room-menu-label").emit("click");
  await ui.el("conversation-open-details").emit("click");
  assert.equal(ui.el("calendar-page").hidden, true);
  assert.equal(ui.el("activity-dialog").open, true);
  assert.equal(ui.el("conversation-open-details").getAttribute("aria-expanded"), "true");
});

test("calendar and files resize independently, expand and return on narrow screens", async () => {
  const ui = await launch(auxiliaryFixture());
  await calendar(ui);
  await ui.el("member-resizer").emit("keydown", {key:"ArrowLeft"});
  const calendarWidth = JSON.parse(ui.saved.get("teamx.room-panels")).calendarWidth;
  await files(ui);
  await ui.el("member-resizer").emit("keydown", {key:"ArrowRight"});
  const widths = JSON.parse(ui.saved.get("teamx.room-panels"));
  assert.equal(widths.calendarWidth, calendarWidth);
  assert.notEqual(widths.fileWidth, calendarWidth);
  await calendar(ui);
  assert.equal(ui.el("workspace").style.getPropertyValue("--members-open-width"), `${calendarWidth}px`);
  await ui.el("calendar-expand").emit("click");
  assert.equal(ui.el("conversation").inert, true);
  await ui.el("calendar-page").emit("keydown", {key:"Escape"});
  assert.equal(ui.el("conversation").inert, false);
  assert.equal(ui.el("calendar-page").hidden, false);
  ui.window.innerWidth = 390; await ui.window.emit("resize");
  assert.equal(ui.el("conversation").inert, true);
  await ui.el("calendar-close").emit("click");
  assert.equal(ui.el("conversation").inert, false);
  assert.equal(ui.document.activeElement, ui.el("room-menu-label"));
});

test("switching fences late calendar reads and invalidates a retained file source", async () => {
  const api = auxiliaryFixture(), hook = api.hook, ui = await launch(api);
  let release;
  api.hook = path => path === "/v1/calendar/list" ? new Promise(resolve => { release = resolve; }) : hook(path);
  await calendar(ui);
  await files(ui);
  release({revision:5,time_zone:"Asia/Shanghai",tasks:[],occurrences:[],days:[]}); await settle();
  assert.equal(ui.el("calendar-page").hidden, true);
  assert.equal(ui.el("files-page").hidden, false);
  api.hook = hook;
  await calendar(ui);
  api.sources[0].root_id = `wr1_${"b".repeat(64)}`;
  await files(ui);
  assert.match(ui.el("files-error").textContent, /来源已变化/);
  assert.equal(ui.el("files-body").children.length, 0);
  await calendar(ui);
  api.epoch = "new-manager"; await ui.refresh();
  await files(ui);
  assert.equal(ui.el("files-filter").value, "");
  assert.ok(api.calls.filter(call => call.path.startsWith("/v1/workspace/files/sources?")).length >= 2);
});
