import assert from "node:assert/strict";
import { request } from "./web/client.mjs";
import { launch, settle } from "./web_app_harness.mjs";

// Launched by the Go test against the real HTTP manager. Only the subprocess /
// model backend is controlled. The actual app entry point drives the requests.
const endpoint = process.env.TEAMX_WEB_ENDPOINT;
assert.ok(endpoint, "TEAMX_WEB_ENDPOINT is required");
const api = { request: (path, payload) => request(path, payload, (url, options) => fetch(endpoint + url, options)) };
const ui = await launch(api);
const catalog = () => api.request("/v1/rooms");
async function ready(name) {
  for (let i=0; i<300; i++) {
    await ui.refresh();
    let list;
    try { list = await catalog(); }
    catch (error) { if (error.code !== "busy") throw error; }
    if (!list) { await new Promise(resolve => setTimeout(resolve, 10)); continue; }
    if (!list.busy && list.active && list.rooms.find(r => r.room_id === list.active_room_id)?.name === name &&
      ui.el("room-name").textContent === name && !ui.el("new-room").disabled) return list;
    await new Promise(resolve => setTimeout(resolve, 10));
  }
  throw new Error(`Room ${name} never ready: ${ui.el("management-notice").textContent} ${ui.el("sync-error").textContent}`);
}
assert.equal((await catalog()).rooms.length, 0);
await ui.create("HTTP-A"); const a = await ready("HTTP-A");
assert.ok(ui.el("message-input").value);
const opening = ui.el("message-input").value;
assert.equal(ui.el("message-count").textContent, "0 条消息");
// Keep the scenario deterministic and do not consume model turns.
const members = await api.request("/v1/member/list", { ...a.active, caller: { kind: "human", id: a.operator } });
for (const member of members.members) await api.request("/v1/member/pause", { ...a.active,
  caller: { kind: "human", id: a.operator }, agent_id: member.agent_id, membership_id: member.membership_id });
await ui.draft("HTTP A preserved message"); await ui.el("compose").emit("submit"); await settle();
assert.equal(ui.el("message-count").textContent, "1 条消息");
await ui.draft("HTTP draft A");
const beforeRename = structuredClone(a.active);
await ui.el("rename-room").emit("click");
ui.el("rename-room-name").value = "HTTP-A-renamed";
await ui.el("rename-form").emit("submit");
assert.equal(ui.el("room-name").textContent, "HTTP-A-renamed");
assert.deepEqual((await catalog()).active, beforeRename);
assert.equal(ui.el("message-count").textContent, "1 条消息");
assert.equal(ui.el("message-input").value, "HTTP draft A");
await ui.el("rename-room").emit("click");
ui.el("rename-room-name").value = "HTTP-A";
await ui.el("rename-form").emit("submit");
await ui.create("HTTP-B"); const b = await ready("HTTP-B");
assert.equal(ui.el("message-count").textContent, "0 条消息");
assert.equal(ui.el("message-input").value, opening);
assert.notEqual(a.active.room_instance, b.active.room_instance);
await ui.draft("HTTP draft B"); await ui.select("HTTP-A"); await ready("HTTP-A");
assert.equal(ui.el("message-count").textContent, "1 条消息");
assert.equal(ui.el("message-input").value, "HTTP draft A");
await ui.select("HTTP-B"); await ready("HTTP-B");
assert.equal(ui.el("message-input").value, "HTTP draft B");
const reloaded = await launch(api, ui.saved);
assert.equal(reloaded.el("message-input").value, "HTTP draft B");
assert.equal((await catalog()).rooms.length, 2);
async function removed(name, view) {
  for (let i=0; i<300; i++) {
    await view.refresh();
    let list;
    try { list = await catalog(); }
    catch (error) { if (error.code !== "busy") throw error; }
    if (list && !list.busy && !list.rooms.some(room => room.name === name)) { await view.refresh(); return list; }
    await new Promise(resolve => setTimeout(resolve, 10));
  }
  throw new Error(`Room ${name} deletion incomplete`);
}
await reloaded.delete("HTTP-A");
assert.equal((await removed("HTTP-A", reloaded)).active_room_id, b.active_room_id);
assert.equal(reloaded.el("room-name").textContent, "HTTP-B");
await reloaded.delete("HTTP-B");
assert.equal((await removed("HTTP-B", reloaded)).rooms.length, 0);
assert.equal(reloaded.el("room-name").textContent, "选择或新建房间");
assert.equal(reloaded.el("message-input").value, "");
console.log(JSON.stringify({ scenario: "RM-04 team HTTP + app entry point", status: "passed", rooms: 2,
  messagePreserved: true, independentDrafts: true, openingDraftOnly: true, reloadPreservesDraft: true,
  deletesInactiveAndActiveRooms: true }));
