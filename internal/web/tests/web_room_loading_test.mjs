import { test } from "node:test";
import assert from "node:assert/strict";
import { launch, settle } from "./web_app_harness.mjs";
import { fixture } from "./web_member_platform_fixture.mjs";

const count = (api, path) => api.calls.filter(c => c.path.split("?")[0] === path).length;
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; };
const page = (room, seq = 1) => ({ messages: [{ seq, text: `Message ${room}`, author: { kind: "agent", id: "a" }, created_at: "2026-11-16T00:00:00Z" }], snapshot_seq: seq, cleared_through: 0, has_more: false });
function roomFixture() {
  const api = fixture();
  api.globalVersions = { settings: "1", catalog: "2", platforms: "3" };
  api.rooms = ["R", "S", "T"].map(id => ({ room_id: id, room_instance: `instance-${id}`, name: id, status: "ready", runtime_status: "ready",
    service: { room_id: id, room_instance: `instance-${id}`, service_epoch: `runtime-${id}` } }));
  api.hook = (path, body) => {
    const override = api.roomHook?.(path, body);
    if (override !== undefined) return override;
    if (path === "/v1/rooms") return { service_epoch: api.epoch, operator: "tester", rooms: structuredClone(api.rooms), teams: [] };
    if (path === "/v1/platforms/interactions/list") return { interactions: [] };
    if (path === "/v1/read") return page(body.room_id);
    if (path === "/v1/human-read/state") return { version: 2, room_id: body.room_id, room_instance: body.room_instance, operator_id: body.caller.id,
      revision: 0, snapshot_seq: 1, cleared_through: 0, opened_through: 1, mention_seen_seqs: [], unseen_mention_seqs: [], unread_count: 0, mention_count: 0,
      ...(body.include_new_messages ? { new_message_seqs: [] } : {}) };
  };
  return api;
}

test("Room entry subscribes first, reconciles once, and reuses unchanged global resources", async () => {
  const api = roomFixture(), ui = await launch(api);
  await ui.draft("Draft R"); api.calls.length = 0; api.deferUpdateHello = true;
  const old = ui.sources.at(-1);
  await ui.select("S"); await ui.draft("Draft S");
  assert.equal(old.closed, true);
  assert.equal(ui.sources.length, 2);
  assert.equal(ui.el("send").disabled, true);
  for (const path of ["/v1/ui", "/v1/rooms", "/v1/workspace", "/v1/member/list", "/v1/read"]) assert.equal(count(api, path), 0, path);
  ui.sources.at(-1).emit("hello", { protocol: 1, service_epoch: api.epoch, global_versions: api.globalVersions }); await settle();
  for (const path of ["/v1/ui", "/v1/rooms", "/v1/platforms"]) assert.equal(count(api, path), 0, path);
  for (const path of ["/v1/workspace", "/v1/member/list", "/v1/read", "/v1/platforms/interactions/list", "/v1/platform/notifications"]) assert.equal(count(api, path), 1, path);
  assert.equal(ui.el("send").disabled, false);
  api.deferUpdateHello = false;
  await ui.select("R"); assert.equal(ui.el("message-input").value, "Draft R");
  assert.equal(count(api, "/v1/send"), 0);
});

test("the automatic direct-conversation sidebar shares entry configuration instead of reading it again", async () => {
  const api = roomFixture(); api.members.splice(1);
  for (const room of api.rooms) room.is_direct = true;
  const ui = await launch(api); api.calls.length = 0;
  await ui.select("S");
  assert.equal(ui.el("activity-dialog").open, true);
  assert.equal(count(api, "/v1/platforms"), 0);
  assert.equal(count(api, "/v1/platform/notifications"), 1);
  assert.equal(count(api, "/v1/tools/bindings"), 1);
  assert.equal(count(api, "/v1/member/list"), 1); assert.equal(count(api, "/v1/read"), 1);
});

test("chat and sending wait for Workspace and messages, independently of slow configuration", async () => {
  const api = roomFixture(), ui = await launch(api), workspace = deferred(), messages = deferred(), platforms = deferred();
  api.globalVersions.platforms = "4";
  api.calls.length = 0;
  api.roomHook = (path, body) => path.startsWith("/v1/workspace?") ? workspace.promise
    : path === "/v1/read" ? messages.promise : path.startsWith("/v1/platforms?") ? platforms.promise : undefined;
  await ui.select("S"); await ui.draft("Wait for verification");
  assert.equal(count(api, "/v1/read"), 0); assert.equal(ui.el("send").disabled, true);
  workspace.resolve({ workspace: { root: "workspaces" }, workspace_version: 1, members: [] }); await settle();
  assert.equal(count(api, "/v1/read"), 1); assert.equal(ui.el("send").disabled, true);
  messages.resolve(page("S")); await settle();
  assert.equal(ui.el("send").disabled, false);
  assert.ok(ui.el("messages").children.some(node => node.id === "message-1"));
  assert.equal(count(api, "/v1/member/list"), 1);
  platforms.resolve({ platforms: structuredClone(api.platforms) }); await settle();
});

for (const topic of ["settings", "catalog", "platforms"]) test(`a ${topic} change in the subscription gap is rechecked`, async () => {
  const api = roomFixture(), ui = await launch(api);
  api.globalVersions[topic] = "changed"; api.calls.length = 0;
  await ui.select("S");
  assert.equal(count(api, "/v1/ui"), topic === "settings" ? 1 : 0);
  assert.equal(count(api, "/v1/rooms"), topic === "catalog" ? 1 : 0);
  assert.equal(count(api, "/v1/platforms"), topic === "platforms" || topic === "catalog" ? 1 : 0);
  assert.equal(count(api, "/v1/read"), 1);
  assert.equal(count(api, "/v1/member/list"), 1);
});

test("a changed directory is verified before reading its selected Workspace", async () => {
  const api = roomFixture(), ui = await launch(api), catalog = deferred();
  api.globalVersions.catalog = "changed"; api.calls.length = 0;
  api.roomHook = path => path === "/v1/rooms" ? catalog.promise : undefined;
  await ui.select("S");
  assert.equal(count(api, "/v1/rooms"), 1); assert.equal(count(api, "/v1/workspace"), 0);
  catalog.resolve({ service_epoch: api.epoch, operator: "tester", rooms: structuredClone(api.rooms), teams: [] }); await settle();
  assert.equal(count(api, "/v1/workspace"), 1); assert.equal(count(api, "/v1/read"), 1);
});

test("missing reuse tokens and native reconnects perform full recovery without duplicate chat reads", async () => {
  const api = roomFixture(), ui = await launch(api);
  delete api.globalVersions; api.calls.length = 0;
  await ui.select("S");
  for (const path of ["/v1/ui", "/v1/rooms", "/v1/workspace", "/v1/read", "/v1/member/list", "/v1/platforms"]) assert.equal(count(api, path), 1, path);
  api.calls.length = 0;
  ui.sources.at(-1).emit("hello", { protocol: 1, service_epoch: api.epoch }); await settle();
  for (const path of ["/v1/ui", "/v1/rooms", "/v1/read", "/v1/member/list", "/v1/platforms"]) assert.equal(count(api, path), 1, path);
});

test("an expired platform reuse token forces a fresh read without reloading unchanged globals", async () => {
  const api = roomFixture(), ui = await launch(api);
  delete api.globalVersions.platforms; api.calls.length = 0;
  await ui.select("S");
  assert.equal(count(api, "/v1/platforms"), 1);
  assert.equal(count(api, "/v1/ui"), 0); assert.equal(count(api, "/v1/rooms"), 0);
});

test("a slow member read does not delay messages or release sending before verification", async () => {
  const api = roomFixture(), ui = await launch(api), members = deferred();
  api.roomHook = path => path === "/v1/member/list" ? members.promise : undefined;
  await ui.select("S"); await ui.draft("Waiting for members");
  assert.ok(ui.el("messages").children.some(node => node.id === "message-1"));
  assert.equal(ui.el("send").disabled, true);
  members.resolve({ members: structuredClone(api.members) }); await settle();
  assert.equal(ui.el("send").disabled, false);
});

test("late reads from a superseded Room never install messages or release the new send gate", async () => {
  const api = roomFixture(), ui = await launch(api), old = deferred(), latest = deferred();
  api.roomHook = (path, body) => path === "/v1/read" ? body.room_id === "S" ? old.promise : latest.promise : undefined;
  await ui.select("S"); const source = ui.sources.at(-1);
  await ui.select("T"); await ui.draft("Draft T");
  assert.equal(source.closed, true);
  old.resolve(page("S", 99)); await settle();
  assert.equal(ui.el("send").disabled, true);
  assert.equal(ui.el("messages").children.some(node => node.id === "message-99"), false);
  latest.resolve(page("T", 2)); await settle();
  assert.equal(ui.el("send").disabled, false);
  assert.ok(ui.el("messages").children.some(node => node.id === "message-2"));
  assert.equal(ui.sources.filter(source => !source.closed).length, 1);
  assert.equal(count(api, "/v1/send"), 0);
});
