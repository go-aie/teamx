import { test } from "node:test";
import assert from "node:assert/strict";
import { launch, settle } from "./web_app_harness.mjs";
import { fixture } from "./web_member_platform_fixture.mjs";

const kinds = ["files", "calendar", "environment", "settings"];
const shortcuts = ["conversation-files", "conversation-calendar", "conversation-environment", "conversation-details"];
function toolFixture() {
  const api = fixture(), request = api.request.bind(api);
  api.members.splice(1); api.direct = true; api.operator = "tester";
  api.request = async (path, ...args) => {
    const result = await request(path, ...args);
    if (path === "/v1/rooms") { result.rooms[0].is_direct = api.direct; result.operator = api.operator; }
    return result;
  };
  api.hook = path => path === "/v1/calendar/list" ? {revision:1,time_zone:"Asia/Shanghai",tasks:[],occurrences:[],days:[]} : undefined;
  return api;
}
function localStorage() {
  const values = new Map();
  return { values, getItem:key=>values.get(key)||null, setItem:(key,value)=>values.set(key,value) };
}
const pin = (ui, kind) => ui.el(`conversation-pin-${kind}`).emit("click");
const menu = ui => ui.el("room-menu-label").emit("click");
const shown = ui => shortcuts.filter(id => !ui.el(id).hidden);

test("all available conversation tools start pinned and share the existing settings icon", async () => {
  const api = toolFixture(), ui = await launch(api);
  assert.deepEqual(shown(ui), shortcuts);
  const geometry = id => ui.el(id).children[0].children[0].getAttribute("d");
  assert.equal(geometry("conversation-details"), geometry("open-settings"));
  assert.equal(geometry("conversation-open-details"), geometry("open-settings"));
  await menu(ui);
  const calls = api.calls.length;
  for (const kind of kinds) {
    await pin(ui, kind);
    assert.equal(ui.el("conversation-menu").hidden, false);
    assert.equal(ui.el(`conversation-pin-${kind}`).getAttribute("aria-checked"), "false");
    assert.equal(ui.el(`conversation-tool-${kind}`).hidden, false);
  }
  assert.deepEqual(shown(ui), []);
  assert.equal(api.calls.length, calls, "pinning does not open panels or mutate the service");
  assert.equal(ui.el("calendar-page").hidden, true);
  assert.equal(ui.el("files-page").hidden, true);
});

test("pin preferences survive reload, remain scoped to the account and tolerate malformed values", async () => {
  const api = toolFixture(), storage = localStorage();
  const ui = await launch(api, new Map(), {localStorage:storage});
  await menu(ui); await pin(ui, "files"); await pin(ui, "settings");
  const reloaded = await launch(api, new Map(), {localStorage:storage});
  assert.deepEqual(shown(reloaded), ["conversation-calendar", "conversation-environment"]);
  api.operator = "another-user"; await reloaded.refresh();
  assert.deepEqual(shown(reloaded), shortcuts);
  api.operator = "tester"; await reloaded.refresh();
  assert.deepEqual(shown(reloaded), ["conversation-calendar", "conversation-environment"]);
  const key = [...storage.values.keys()][0];
  storage.values.set(key, '{"files":"false","calendar":false,"settings":null}');
  await reloaded.window.emit("storage", {key});
  assert.deepEqual(shown(reloaded), ["conversation-files", "conversation-environment", "conversation-details"]);
  storage.values.set(key, "malformed"); await reloaded.window.emit("storage", {key});
  assert.deepEqual(shown(reloaded), shortcuts);
});

test("phone menus open unpinned tools and restore desktop preferences after resizing", async () => {
  const ui = await launch(toolFixture());
  await menu(ui); await pin(ui, "calendar");
  ui.window.innerWidth = 320; await ui.window.emit("resize");
  assert.deepEqual(shown(ui), []);
  for (const kind of kinds) assert.equal(ui.el(`conversation-pin-${kind}`).hidden, true);
  await menu(ui); await ui.el("conversation-open-calendar").emit("click"); await settle();
  assert.equal(ui.el("calendar-page").hidden, false);
  assert.equal(ui.el("conversation-menu").hidden, true);
  await ui.el("calendar-close").emit("click");
  assert.equal(ui.document.activeElement, ui.el("room-menu-label"));
  ui.window.innerWidth = 1440; await ui.window.emit("resize");
  assert.deepEqual(shown(ui), ["conversation-files", "conversation-environment", "conversation-details"]);
});

test("calendar availability never overwrites its pin preference and menus support keyboard navigation", async () => {
  const api = toolFixture(), ui = await launch(api);
  api.direct = false; await ui.refresh();
  assert.equal(ui.el("conversation-tool-calendar").hidden, true);
  assert.deepEqual(shown(ui), ["conversation-files", "conversation-environment", "conversation-details"]);
  api.direct = true; await ui.refresh();
  assert.deepEqual(shown(ui), shortcuts);
  await ui.el("room-menu-label").emit("keydown", {key:"ArrowDown"});
  assert.equal(ui.document.activeElement, ui.el("conversation-open-files"));
  await ui.el("conversation-menu").emit("keydown", {key:"ArrowDown"});
  assert.equal(ui.document.activeElement, ui.el("conversation-open-calendar"));
  await ui.el("conversation-menu").emit("keydown", {key:"ArrowRight"});
  assert.equal(ui.document.activeElement, ui.el("conversation-pin-calendar"));
  await pin(ui, "calendar");
  assert.equal(ui.el("conversation-menu").hidden, false);
  await ui.el("conversation-menu").emit("keydown", {key:"Escape"});
  assert.equal(ui.document.activeElement, ui.el("room-menu-label"));
  assert.equal(ui.el("conversation-menu").hidden, true);
});

test("blocked browser storage retains pin choices for the current page", async () => {
  const storage = {getItem(){throw Error("blocked");},setItem(){throw Error("blocked");}};
  const ui = await launch(toolFixture(), new Map(), {localStorage:storage});
  await menu(ui); await pin(ui, "files"); await ui.refresh();
  assert.equal(ui.el("conversation-files").hidden, true);
  await pin(ui, "files"); assert.equal(ui.el("conversation-files").hidden, false);
});
