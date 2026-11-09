const eventView = value => ({ provider: "gitlab", channel: "events", subscription_id: value.subscription_version ? "a".repeat(32) : "", ...structuredClone(value) });
const todoView = value => ({ provider: "gitlab", channel: "todos", subscription_id: "", subscription_version: 0, available: true, status: "disabled", pending: 0, metrics: {},
  config: { enabled: false, connection_id: "", interval_seconds: 30, filter: {} }, ...structuredClone(value) });
const notificationList = value => { const subscriptions = [eventView(value), todoView()]; return { subscriptions, gitlab: fixtureGitLabView(subscriptions) }; };
function fixtureSourceConfig(config, channel) {
  if (channel === "events") return { enabled: !!config.enabled, project: config.project || "", etag_verified: !!config.etag_verified,
    filter: { types: [...(config.filter?.types || [])] } };
  return { enabled: !!config.enabled,
    filter: { reasons: [...(config.filter?.reasons ?? ["mentioned","assigned","review_requested","other"])], types: [...(config.filter?.types ?? ["merge_request","work_item"])] } };
}
function fixtureGitLabView(sources, record) {
  const events = sources.find(s => s.channel === "events") || eventView({ available:true, config:{}, metrics:{} });
  const todos = sources.find(s => s.channel === "todos") || todoView();
  let common = events.config.enabled ? events.config : todos.config;
  let connection = common.connection_id || events.config.connection_id || todos.config.connection_id || "";
  if (events.config.enabled && todos.config.enabled && events.config.connection_id !== todos.config.connection_id) connection = "";
  const config = record?.config || { connection_id:connection, target:common.target || {}, interval_seconds:common.interval_seconds || 30, events:fixtureSourceConfig(events.config,"events"), todos:fixtureSourceConfig(todos.config,"todos") };
  return { version:record?.version || 0, config:structuredClone(config), events, todos, pending:structuredClone(record?.pending || {}), errors:structuredClone(record?.errors || {}), applying:!!record?.applying, backfill_locked:!!record?.backfill_locked || !!events.subscription_id || !!todos.subscription_id };
}
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import * as client from "./web/client.mjs";
import { launch, settle } from "./web_app_harness.mjs";

test("Scion connection discovers a managed account and saves its profile without manual IDs", async () => {
  const api = server(); let item;
  api.hook = (path, data) => {
    if (path === "/v1/platform/connections/scion/discover") return {executable:"/service/teamx-scion", profiles:[{profile:"c".repeat(32),username:"alice"},{profile:"d".repeat(32),username:"bob"}]};
    if (path.startsWith("/v1/platform/connections?")) return {connections:item ? [structuredClone(item)] : []};
    if (path === "/v1/platform/connections/save") {
      item = {...structuredClone(data), version:(item?.version || 0)+1, credential_kind:"cli_profile", validation:{status:"unverified"}};
      return structuredClone(item);
    }
    if (path === "/v1/platform/connections/verify") {
      assert.equal(data.id, item.id); assert.equal(data.version, item.version);
      item.version++; item.validation={status:"valid",username:"alice",account:{provider:"scion",instance:item.url,id:{namespace:"scion:subject",value:"alice"}}};
      return structuredClone(item);
    }
  };
  const ui = await launch(api);
  await ui.el("open-settings").emit("click"); await ui.el("connection-new").emit("click");
  ui.el("connection-provider").value="scion"; await ui.el("connection-provider").emit("change");
  assert.equal(ui.el("connection-scion-row").hidden,false);
  assert.equal(ui.el("connection-profile-row").hidden,true);
  assert.equal(ui.el("gitlab-account-row").hidden,true);
  ui.el("connection-name").value="Scion demo";ui.el("connection-url").value="https://scion.invalid";
  ui.el("connection-scion-executable").value="/service/teamx-scion";
  await ui.el("connection-scion-executable").emit("change");
  ui.el("connection-scion-profile").value="c".repeat(32);
  await ui.el("connection-form").emit("submit");
  assert.equal(item.provider,"scion");assert.equal(item.cli_profile.brand,"scion");assert.equal(item.cli_profile.cli_version,"0.1.13");
  assert.equal(item.cli_profile.profile,"c".repeat(32));assert.equal(item.validation.username,"alice");
  assert.equal(ui.el("connection-validation").hidden,false);
  assert.equal(ui.el("connection-scion-profile").value,"c".repeat(32));
  assert.match(ui.el("connections-feedback").textContent,/账号验证成功/);
  assert.equal(api.calls.some(c => /connections\/(lark|gitlab)\//.test(c.path)),false);
  await ui.el("connections-list").children[0].emit("click");
  ui.el("connection-scion-profile").value="d".repeat(32);
  await ui.el("connection-verify").emit("click");
  assert.match(ui.el("connections-feedback").textContent,/先保存/);
});

async function expandDisclosure(element) {
  element.open=true;await element.emit("toggle");await settle();
}
const elementText = element => [element.textContent,...element.children.map(elementText)].join("\n");

const identity = (room, epoch = `epoch-${room.room_id}`) => ({ room_id: room.room_id, room_instance: room.room_instance, service_epoch: epoch });
const makeRoom = (name, id = name) => ({ room_id: id, room_instance: `instance-${id}`, name,
  status: "ready", team_id: "sample", create_id: `create-${id}`, created_at: "2026-11-01T01:00:00Z", updated_at: "2026-11-01T01:00:00Z" });
const message = (seq, text) => ({ seq, text, author: { kind: "agent", id: "reviewer" }, created_at: "2026-11-01T01:00:00Z" });
function server(rooms = []) {
  for (const room of rooms) { room.runtime_status = "ready"; room.service = identity(room); }
  const catalog = { service_epoch: "manager", operator: "tester", busy: false, rooms,
    teams: [{ id: "sample", name: "Sample", description: "Example", opening_message: "Opening", members: [] }] };
  const history = new Map(rooms.map(room => [room.room_id, [message(1, `history-${room.name}`)]]));
  const calls = [];
  let next = 0;
  const api = {
    catalog, calls, history, opens: new Map(), hook: null,
    async request(path, data) {
      calls.push({ path, data: structuredClone(data) });
      if (api.hook) { const intercepted = api.hook(path, data); if (intercepted !== undefined) return intercepted; }
      if (path.startsWith("/v1/platform/connections?")) return { connections: [{ id: "a".repeat(32), name: "GitLab", provider: "gitlab", url: "https://gitlab.invalid", validation: { status: "valid", user_id: 101, username: "alice" } }] };
      if (path === "/v1/ui") return { managed: true };
      if (path === "/v1/rooms") {
        return structuredClone(catalog);
      }
      if (path.startsWith("/v1/rooms/open?")) {
        const receipt = api.opens.get(new URLSearchParams(path.split("?")[1]).get("open_id"));
        if (!receipt) throw Object.assign(new Error("open not found"), { code: "request" });
        return structuredClone(receipt);
      }
      if (path.startsWith("/v1/workspace?")) {
        const q = new URL(path, "http://fixture").searchParams;
        return { room_id: q.get("room_id"), room_instance: q.get("room_instance"), workspace: { root: "workspaces" }, workspace_version: 1, members: [], credential_status: "missing" };
      }
      if (path.startsWith("/v1/platform/notifications?")) return notificationList({ available: true, subscription_version: 0, config: { enabled: false, interval_seconds: 5, filter: {} }, status: "disabled", pending: 0, metrics: { calls: 0, response_bytes: 0 } });
      if (path === "/v1/rooms/create") {
        assert.equal(data.caller.id, "tester");
        let room = catalog.rooms.find(room => room.create_id === data.create_id);
        if (!room) {
          room = { ...makeRoom(data.name || "Sample", `new-${++next}`), create_id: data.create_id, team_id: data.team_id };
          catalog.rooms.push(room); history.set(room.room_id, []);
        }
        return structuredClone(room);
      }
      if (path === "/v1/rooms/rename") {
        assert.equal(data.service_epoch, catalog.service_epoch);
        assert.equal(data.caller.kind, "human");
        const room = catalog.rooms.find(room => room.room_id === data.room_id && room.room_instance === data.room_instance);
        assert.ok(room);
        if (room.name !== data.expected_name) throw Object.assign(new Error("name changed"), { code: "conflict" });
        room.name = data.name;
        return structuredClone(room);
      }
      if (path === "/v1/rooms/open") {
        const room = catalog.rooms.find(room => room.room_id === data.room_id);
        room.service ||= identity(room); room.runtime_status = "ready";
        const receipt = { ...data, status: "completed", service: room.service };
        api.opens.set(data.open_id, receipt);
        return structuredClone(receipt);
      }
      if (path === "/v1/rooms/delete") {
        assert.equal(data.service_epoch, catalog.service_epoch);
        assert.equal(data.caller.kind, "human");
        const room = catalog.rooms.find(room => room.room_id === data.room_id && room.room_instance === data.room_instance);
        assert.ok(room);
        catalog.rooms = catalog.rooms.filter(r => r !== room);
        history.delete(room.room_id);
        return { ...room, status: "deleting", deletion_phase: "closing" };
      }
      if (!catalog.rooms.some(room => client.sameService(data, room.service)) && !(path === "/v1/read" && client.sameService(data, api.workspaces?.get(data.room_id)?.observation))) throw Object.assign(new Error("stale identity"), { code: "identity" });
      if (path === "/v1/member/list") return { members: [], latest_seq: history.get(data.room_id).length };
      if (path === "/v1/read") {
        const all = history.get(data.room_id);
        return { messages: all.filter(m => m.seq > (data.after || 0)), snapshot_seq: all.at(-1)?.seq || 0, has_more: false };
      }
      if (path === "/v1/send") {
        const all = history.get(data.room_id); all.push(message(all.length+1, data.text)); return { seq: all.length };
      }
      throw new Error(`Unexpected request ${path}`);
    },
  };
  return api;
}

test("rename updates title, navigation and reload while preserving identity, history and draft", async () => {
  const api = server([makeRoom("A")]), ui = await launch(api);
  const info = structuredClone(api.catalog.rooms[0]?.service);
  await ui.draft("Keep this draft");
  await ui.el("rename-room").emit("click");
  assert.equal(ui.el("rename-form").hidden, false);
  assert.equal(ui.el("room-heading").hidden, true);
  assert.equal(ui.el("rename-room-name").value, "A");
  ui.el("rename-room-name").value = "  新名称  ";
  await ui.el("rename-form").emit("submit");
  assert.equal(ui.el("rename-form").hidden, true);
  assert.equal(ui.el("room-name").textContent, "新名称");
  assert.equal(ui.el("rename-room").attributes["aria-label"], "新名称，修改房间名称");
  assert.equal(ui.el("room-list").children[0].children[0].children[0].textContent, "新名称");
  assert.equal(ui.document.title, "新名称 · TeamX");
  assert.deepEqual(api.catalog.rooms[0]?.service, info);
  assert.equal(ui.el("message-input").value, "Keep this draft");
  assert.equal(ui.el("message-count").textContent, "1 条消息");
  const reload = await launch(api, ui.saved);
  assert.equal(reload.el("room-name").textContent, "新名称");
  assert.equal(reload.el("message-input").value, "Keep this draft");
  assert.equal(api.calls.filter(c => c.path === "/v1/rooms/rename").length, 1);
});

test("inline rename cancels with Escape or the cancel button without saving", async () => {
  const api = server([makeRoom("A")]), ui = await launch(api);
  await ui.el("rename-room").emit("click");
  ui.el("rename-room-name").value = "Unsaved";
  await ui.el("rename-form").emit("keydown", { key: "Escape", isComposing: true });
  assert.equal(ui.el("rename-form").hidden, false);
  await ui.el("rename-form").emit("keydown", { key: "Escape" });
  assert.equal(ui.el("rename-form").hidden, true);
  assert.equal(ui.el("room-heading").hidden, false);
  assert.equal(ui.el("room-name").textContent, "A");
  await ui.el("rename-room").emit("click");
  assert.equal(ui.el("rename-room-name").value, "A");
  await ui.el("cancel-rename").emit("click");
  assert.equal(ui.el("rename-form").hidden, true);
  assert.equal(api.calls.some(c => c.path === "/v1/rooms/rename"), false);
});

test("rename validates UTF-8 length, preserves edits on polling, and rejects a concurrent edit", async () => {
  const api = server([makeRoom("A")]), ui = await launch(api);
  await ui.el("rename-room").emit("click");
  for (const invalid of ["   ", "中".repeat(86)]) {
    ui.el("rename-room-name").value = invalid;
    await ui.el("rename-form").emit("submit");
    assert.equal(api.calls.some(c => c.path === "/v1/rooms/rename"), false);
    assert.equal(ui.el("rename-error").hidden, false);
  }
  ui.el("rename-room-name").value = "My edit";
  api.catalog.rooms[0].name = "Other tab";
  await ui.refresh();
  assert.equal(ui.el("rename-room-name").value, "My edit");
  await ui.el("rename-form").emit("submit");
  assert.equal(api.catalog.rooms[0].name, "Other tab");
  assert.match(ui.el("rename-error").textContent, /其他页面/);
  await ui.el("cancel-rename").emit("click");
  await ui.el("rename-room").emit("click");
  assert.equal(ui.el("rename-room-name").value, "Other tab");
});

test("lost rename response is read back without replay and late completion cannot affect another room", async () => {
  const api = server([makeRoom("A"), makeRoom("B")]), ui = await launch(api);
  api.hook = (path, data) => {
    if (path !== "/v1/rooms/rename") return;
    api.catalog.rooms[0].name = data.name;
    throw new Error("lost after commit");
  };
  await ui.el("rename-room").emit("click");
  ui.el("rename-room-name").value = "Renamed A";
  await ui.el("rename-form").emit("submit");
  assert.equal(ui.el("room-name").textContent, "Renamed A");
  assert.match(ui.el("rename-error").textContent, /保存未确认/);
  await ui.refresh();
  assert.equal(api.calls.filter(c => c.path === "/v1/rooms/rename").length, 1);
  await ui.el("cancel-rename").emit("click");
  let rejectRename;
  api.hook = path => path === "/v1/rooms/rename" ? new Promise((_, reject) => { rejectRename = reject; }) : undefined;
  await ui.el("rename-room").emit("click");
  ui.el("rename-room-name").value = "Late A";
  const pending = ui.el("rename-form").emit("submit");
  await settle();
  await ui.select("B");
  await ui.refresh();
  assert.equal(ui.el("rename-form").hidden, true);
  rejectRename(new Error("late failure")); await pending;
  assert.equal(ui.el("room-name").textContent, "B");
  assert.equal(ui.el("rename-form").hidden, true);
});

test("team member header toggle collapses the whole panel and retains the choice across reloads", async () => {
  const api = server([makeRoom("A")]), ui = await launch(api);
  const toggle = ui.el("toggle-members"), rail = ui.el("member-rail");
  assert.equal(toggle.attributes["aria-expanded"], "true"); assert.equal(rail.inert, false);
  const calls = api.calls.length;
  await toggle.emit("click");
  assert.equal(toggle.attributes["aria-expanded"], "false");
  assert.equal(toggle.attributes["aria-label"], "展开团队成员"); assert.equal(rail.inert, true);
  assert.equal(rail.attributes["aria-hidden"], "true");
  assert.equal(api.calls.length, calls);
  const reload = await launch(api, ui.saved);
  assert.equal(reload.el("toggle-members").attributes["aria-expanded"], "false");
  assert.equal(reload.el("member-rail").inert, true);
  await reload.el("toggle-members").emit("click");
  assert.equal(reload.el("toggle-members").attributes["aria-expanded"], "true");
  assert.equal(reload.el("member-rail").inert, false);
  assert.equal(reload.el("toggle-rooms").attributes["aria-expanded"], "true");
});

test("room list displays and sorts by last message time, with creation time for empty rooms", async () => {
  const a = { ...makeRoom("A"), last_message_at: "2026-11-01T09:02:46Z", updated_at: "2026-11-01T09:05:00Z" };
  const b = { ...makeRoom("B"), last_message_at: "2026-11-01T09:03:00Z", updated_at: "2026-11-01T09:04:00Z" };
  const empty = { ...makeRoom("Empty"), created_at: "2026-11-01T09:01:00Z", updated_at: "2026-11-01T09:06:00Z" };
  const api = server([a, b, empty]);
  api.history.set("A", [{ ...message(1, "MR closed"), created_at: a.last_message_at }]);
  api.history.set("Empty", []);
  const ui = await launch(api);
  const format = value => "运行中 · 11/01 " + new Date(value).toLocaleTimeString("en-GB", { timeZone: "Asia/Shanghai", hour: "2-digit", minute: "2-digit", hour12: false });
  const list = () => ui.el("room-list").children.map(row => row.children[0]);
  assert.deepEqual(list().map(button => button.children[0].textContent), ["B", "A", "Empty"]);
  assert.equal(list()[1].children[1].textContent, format(a.last_message_at));
  assert.equal(list()[2].children[1].textContent, format(empty.created_at));
  // Session/cursor writes do not change either the displayed date or ordering.
  a.updated_at = "2026-11-01T09:10:00Z";
  await ui.refresh();
  assert.deepEqual(list().map(button => button.children[0].textContent), ["B", "A", "Empty"]);
  assert.equal(list()[1].children[1].textContent, format(a.last_message_at));
  a.last_message_at = "2026-11-01T09:11:00Z";
  api.history.get("A").push({ ...message(2, "follow up"), created_at: a.last_message_at });
  await ui.refresh();
  assert.deepEqual(list().map(button => button.children[0].textContent), ["A", "B", "Empty"]);
  assert.equal(list()[0].children[1].textContent, format(a.last_message_at));
});

test("settings and room creation dismiss only when the pointer starts and clicks outside the dialog", async () => {
  const api = server(), ui = await launch(api);
  for (const [opener, id] of [["open-settings", "settings-dialog"], ["new-room", "create-dialog"]]) {
    await ui.el(opener).emit("click");
    const dialog = ui.el(id);
    dialog.rect = { left: 100, top: 100, right: 500, bottom: 500 };
    const inside = { clientX: 200, clientY: 200 }, outside = { clientX: 20, clientY: 20 };
    const gesture = async (down, click, button = 0) => {
      await dialog.emit("pointerdown", { ...down, button });
      await dialog.emit("click", click);
    };
    await gesture(inside, inside); assert.equal(dialog.open, true);
    await gesture(inside, outside); assert.equal(dialog.open, true);
    await gesture(outside, inside); assert.equal(dialog.open, true);
    await gesture(outside, outside, 2); assert.equal(dialog.open, true);
    await dialog.emit("pointerdown", { ...outside, button: 0 });
    await dialog.emit("pointercancel"); await dialog.emit("click", outside);
    assert.equal(dialog.open, true);
    await gesture(outside, outside); assert.equal(dialog.open, false);
  }
  assert.equal(api.calls.some(call => /\/v1\/rooms\/(create|delete)|connections\/(save|delete)/.test(call.path)), false);
});

test("empty catalog -> create -> A/B/A retains history, draft and quote; opening is never sent", async () => {
  const api = server(), ui = await launch(api);
  assert.equal(ui.el("rooms-empty").hidden, false);
  assert.equal(ui.el("runtime-field"), undefined);
  await ui.create("A");
  assert.equal(ui.el("room-name").textContent, "A");
  assert.equal(ui.el("message-input").value, "Opening");
  assert.equal(api.calls.filter(c => c.path === "/v1/send").length, 0);
  await ui.draft("draft-A");
  // Quoting uses the actual rendered message handler in a later read.
  api.history.get(api.catalog.rooms[0]?.service.room_id).push(message(1, "quote-A"));
  await ui.refresh();
  const article = ui.el("messages").children.find(el => el.id === "message-1");
  await article.querySelector(".reply-button").emit("click");
  await ui.create("B");
  assert.equal(ui.el("message-count").textContent, "0 条消息");
  assert.equal(ui.el("message-input").value, "Opening");
  assert.equal(ui.el("reply-context").hidden, true);
  await ui.draft("draft-B"); await ui.select("A");
  assert.equal(ui.el("message-input").value, "draft-A");
  assert.equal(ui.el("reply-context").hidden, false);
  assert.match(ui.el("reply-label").textContent, /quote-A/);
  assert.equal(ui.el("message-count").textContent, "1 条消息");
  await ui.select("B"); assert.equal(ui.el("message-input").value, "draft-B");
  const reload = await launch(api, ui.saved);
  assert.equal(reload.el("message-input").value, "draft-B");
  assert.equal(api.calls.filter(c => c.path === "/v1/rooms/create").length, 2);
});

test("a late A send response cannot clear B's draft or post with B's identity", async () => {
  const api = server([makeRoom("A"), makeRoom("B")]), ui = await launch(api);
  let resolveSend;
  api.hook = path => path === "/v1/send" ? new Promise(resolve => { resolveSend = resolve; }) : undefined;
  await ui.draft("send-A"); const sending = ui.el("compose").emit("submit");
  await settle(); await ui.select("B"); await ui.draft("draft-B");
  resolveSend({ seq: 2 }); await sending;
  assert.equal(ui.el("message-input").value, "draft-B");
  assert.equal(ui.el("send").textContent, "发送 ↗");
  const sent = api.calls.filter(call => call.path === "/v1/send");
  assert.equal(sent.length, 1); assert.equal(sent[0].data.room_instance, "instance-A");
  await ui.select("A"); assert.equal(ui.el("message-input").value, "");
});

test("lost send is isolated and explicit retry retains the original epoch after A/B/A", async () => {
  const api = server([makeRoom("A"), makeRoom("B")]), ui = await launch(api);
  api.hook = path => path === "/v1/send" ? Promise.reject(new Error("response lost")) : undefined;
  await ui.draft("uncertain-A"); await ui.el("compose").emit("submit");
  const original = api.calls.find(call => call.path === "/v1/send").data;
  await ui.select("B"); await ui.draft("B stays editable");
  assert.equal(ui.el("message-input").readOnly, false);
  await ui.select("A");
  assert.equal(ui.el("message-input").value, "uncertain-A");
  assert.equal(ui.el("message-input").readOnly, true);
  api.hook = null; await ui.el("compose").emit("submit");
  assert.deepEqual(api.calls.filter(c => c.path === "/v1/send").at(-1).data, original);
  assert.equal(ui.el("send-error").textContent, "");
  assert.equal(ui.el("message-input").value, "");
  assert.equal(api.history.get("A").at(-1).text, "uncertain-A");
});

test("lost create response is found by create_id, survives reload and needs explicit activation", async () => {
  const api = server(), request = api.request;
  let lost = false;
  api.request = async (path, data) => {
    const result = await request(path, data);
    if (path === "/v1/rooms/create" && !lost) { lost = true; throw new Error("lost after commit"); }
    return result;
  };
  const ui = await launch(api);
  await ui.create("A");
  assert.equal(api.catalog.rooms.length, 1); assert.equal(api.catalog.rooms[0]?.service, undefined);
  assert.equal(ui.el("retry-management").textContent, "进入已创建房间");
  const reload = await launch(api, ui.saved);
  assert.equal(api.calls.filter(c => c.path === "/v1/rooms/open").length, 0);
  await reload.el("retry-management").emit("click"); await settle();
  assert.equal(reload.el("room-name").textContent, "A");
  assert.equal(reload.el("message-input").value, "Opening");
  assert.equal(api.catalog.rooms.length, 1);
  assert.equal(api.calls.filter(c => c.path === "/v1/rooms/create").length, 1);
});

test("duplicate clicks and failed opening preserve the current running room", async () => {
  const api = server([makeRoom("A"), makeRoom("B")]);
  delete api.catalog.rooms[1].service; api.catalog.rooms[1].runtime_status = "closed";
  const ui = await launch(api);
  let activate;
  api.hook = (path, data) => {
    if (path !== "/v1/rooms/open") return;
    activate = data;
    const receipt = { ...data, status: "running" };
    api.opens.set(data.open_id, receipt);
    return receipt;
  };
  await ui.select("B"); await ui.select("B");
  assert.equal(api.calls.filter(c => c.path === "/v1/rooms/open").length, 1);
  assert.equal(ui.el("message-input").readOnly, false);
  assert.match(ui.el("management-notice").textContent, /尚未确认/);
  api.catalog.busy = false;
  api.opens.set(activate.open_id, { ...activate, status: "failed", error: "assembly failed" });
  await ui.refresh();
  assert.equal(ui.el("room-name").textContent, "A");
  assert.match(ui.el("management-notice").textContent, /assembly failed/);
  assert.equal(api.catalog.rooms.length, 2);
});

test("a delayed history page cannot overwrite a locally selected room", async () => {
  const api = server([makeRoom("A"), makeRoom("B")]);
  api.history.set("A", [message(60, "last A")]);
  const ui = await launch(api);
  await ui.draft("A draft");
  let resolveHistory;
  api.hook = (path, data) => path === "/v1/read" && data.snapshot_seq !== undefined
    ? new Promise(resolve => { resolveHistory = resolve; }) : undefined;
  const history = ui.el("load-history").emit("click"); await settle();
  // A different tab performs the management action; this tab only polls it.
  await ui.select("B");
  await ui.refresh(); await ui.draft("B draft");
  resolveHistory({ messages: [message(2, "late A")], snapshot_seq: 59, has_more: false }); await history;
  assert.equal(ui.el("room-name").textContent, "B");
  assert.equal(ui.el("message-input").value, "B draft");
  assert.equal(ui.el("messages").children.some(el => el.id === "message-2"), false);
});

test("a partial create retries the same request and never allocates a second ID", async () => {
  const api = server(), actual = api.request;
  let first = true;
  api.request = async (path, payload) => {
    if (path === "/v1/rooms/create" && first) {
      first = false;
      const room = await actual(path, payload);
      api.catalog.rooms[0].status = "creating";
      throw new Error("disk unavailable after index commit");
    }
    if (path === "/v1/rooms/create") api.catalog.rooms[0].status = "ready";
    return actual(path, payload);
  };
  const ui = await launch(api); await ui.create("A");
  const id = api.calls.find(c => c.path === "/v1/rooms/create").data.create_id;
  await ui.el("retry-management").emit("click"); await settle();
  const creates = api.calls.filter(c => c.path === "/v1/rooms/create");
  assert.equal(creates.length, 2); assert.equal(creates[1].data.create_id, id);
  assert.equal(api.catalog.rooms.length, 1);
  assert.equal(ui.el("room-name").textContent, "A");
});

test("opening response loss and reload keep the original open_id without replay", async () => {
  const api = server([makeRoom("A"), makeRoom("B")]);
  delete api.catalog.rooms[1].service; api.catalog.rooms[1].runtime_status = "closed";
  let first = true;
  api.hook = (path, data) => {
    if (path !== "/v1/rooms/open") return;
    if (first) { first = false; return Promise.reject(new Error("connection lost before receipt")); }
    return { ...data, status: "failed", error: "interrupted operation" };
  };
  const ui = await launch(api); await ui.select("B");
  const original = api.calls.find(c => c.path === "/v1/rooms/open").data;
  api.catalog.service_epoch = "restarted-manager";
  const reload = await launch(api, ui.saved);
  assert.equal(api.calls.filter(c => c.path === "/v1/rooms/open").length, 1);
  await reload.el("retry-management").emit("click"); await settle();
  const retried = api.calls.filter(c => c.path === "/v1/rooms/open").at(-1).data;
  assert.equal(retried.open_id, original.open_id);
  assert.equal(retried.service_epoch, "restarted-manager");
  assert.equal(reload.el("room-name").textContent, "A");
  assert.match(reload.el("management-notice").textContent, /interrupted operation/);
});

test("creation selects only a team and an empty team catalog explains configuration", async () => {
 const api=server();
 api.catalog.teams[0].members=[{id:"reviewer",name:"Reviewer"}];
 const ui=await launch(api);
 assert.equal(ui.el("team").options[0].textContent,"Sample");
 assert.equal(ui.el("team-members").children[0].textContent,"Reviewer · reviewer");
 await ui.create("A");
 const create=api.calls.find(call=>call.path==="/v1/rooms/create").data;
 assert.equal(create.team_id,"sample");assert.equal("runtime_profile" in create,false);assert.equal("example_id" in create,false);
 api.catalog.teams=[];await ui.refresh();await ui.el("new-room").emit("click");
 assert.equal(ui.el("create-room").disabled,true);assert.match(ui.el("team-description").textContent,/team.json/);
});

test("unconfirmed discussion restart blocks new rooms but permits explicit retry with its original ID", async () => {
  const api = server([makeRoom("A")]);
  const ui = await launch(api);
  let first = true;
  api.hook = (path, data) => {
    if (path !== "/v1/discussion/restart") return;
    if (first) { first = false; return Promise.reject(new Error("lost response")); }
    return { id: data.restart_id, status: "completed" };
  };
  await ui.el("restart-discussion").emit("click");
  assert.equal(ui.el("new-room").disabled, true);
  assert.equal(ui.el("restart-discussion").disabled, false);
  await ui.el("restart-discussion").emit("click");
  const calls = api.calls.filter(call => call.path === "/v1/discussion/restart");
  assert.equal(calls.length, 2); assert.deepEqual(calls[1].data, calls[0].data);
  assert.equal(ui.el("new-room").disabled, false);
});

const mentionElement = (ui, seq) => ui.el("messages").children.find(el => el.id === `message-${seq}`);
const mentionHighlighted = (ui, seq) => mentionElement(ui, seq).classList.contains("mentioned");
async function visibleMessages(ui, seqs) {
  for (const el of ui.el("messages").children) {
    el.rect = { top: 200, bottom: 280, left: 0, right: 300, width: 300, height: 80 };
    if (seqs.some(seq => el.id === `message-${seq}`)) el.rect = { ...el.rect, top: 0, bottom: 80 };
  }
  await ui.el("timeline").emit("scroll");
}

test("only viewed mentions lose their background; navigation waits for dwell, with room-specific progress retained on reload", async () => {
  const api = server([makeRoom("A"), makeRoom("B")]);
  api.history.set("A", [message(1, "@tester A question"), message(2, "ordinary message"), message(3, "@tester another question"),
    { ...message(4, "@tester self mention"), author: { kind: "human", id: "tester" } }]);
  api.history.set("B", [message(1, "@tester B question")]);
  const ui = await launch(api);
  const highlighted = mentionHighlighted;
  await visibleMessages(ui, []);
  assert.equal(ui.el("mention-notice").hidden, false);
  assert.equal(highlighted(ui, 1), true); assert.equal(highlighted(ui, 3), true);
  assert.equal(highlighted(ui, 2), false); assert.equal(highlighted(ui, 4), false);
  await ui.el("view-mention").emit("click");
  assert.equal(mentionElement(ui, 1).scrollIntoViewCount, 1);
  assert.equal(highlighted(ui, 1), true);
  await visibleMessages(ui, [1]);
  await ui.clock.advance(999);
  assert.equal(highlighted(ui, 1), true);
  await ui.clock.advance(1);
  assert.equal(highlighted(ui, 1), false); assert.equal(highlighted(ui, 3), true);
  assert.equal(ui.el("mention-notice").hidden, false);
  await ui.el("view-mention").emit("click");
  assert.equal(mentionElement(ui, 3).scrollIntoViewCount, 1);
  await visibleMessages(ui, [3]);
  await ui.clock.advance(1000);
  assert.equal(highlighted(ui, 3), false);
  assert.equal(ui.el("mention-notice").hidden, true);
  await ui.select("B"); assert.equal(ui.el("mention-notice").hidden, false);
  assert.match(ui.document.title, /1 条 @你/);
  await ui.select("A"); assert.equal(ui.el("mention-notice").hidden, true);
  assert.equal(highlighted(ui, 1), false); assert.equal(highlighted(ui, 3), false);
  api.history.get("A").push(message(5, "@tester new question"));
  await ui.refresh();
  assert.equal(highlighted(ui, 1), false); assert.equal(highlighted(ui, 5), true);
  const unreadReload = await launch(api, ui.saved);
  assert.equal(highlighted(unreadReload, 1), false); assert.equal(highlighted(unreadReload, 3), false);
  assert.equal(highlighted(unreadReload, 5), true);
  await unreadReload.el("view-mention").emit("click");
  await visibleMessages(unreadReload, [5]);
  await unreadReload.clock.advance(1000);
  assert.equal(highlighted(unreadReload, 5), false);
  const reload = await launch(api, ui.saved);
  assert.equal(reload.el("mention-notice").hidden, true);
  assert.equal(highlighted(reload, 1), false); assert.equal(highlighted(reload, 3), false); assert.equal(highlighted(reload, 5), false);
});

test("viewing two of five mentions out of order leaves exactly three, including earlier unseen messages", async () => {
  const api = server([makeRoom("A")]);
  api.history.set("A", [1, 2, 3, 4, 5].map(seq => message(seq, `@tester question ${seq}`)));
  const ui = await launch(api);
  await visibleMessages(ui, [4, 5]);
  const calls = api.calls.length;
  await ui.clock.advance(1000);
  assert.equal(api.calls.length, calls, "human viewing never changes Agent cursors");
  assert.match(ui.el("view-mention").textContent, /3 条消息/);
  assert.match(ui.document.title, /3 条 @你/);
  for (const seq of [1, 2, 3]) assert.equal(mentionHighlighted(ui, seq), true);
  for (const seq of [4, 5]) assert.equal(mentionHighlighted(ui, seq), false);
  await ui.el("view-mention").emit("click");
  assert.equal(mentionElement(ui, 1).scrollIntoViewCount, 1);
  assert.match(ui.el("view-mention").textContent, /3 条消息/);
  const reload = await launch(api, ui.saved);
  assert.match(reload.el("view-mention").textContent, /3 条消息/);
  for (const seq of [1, 2, 3]) assert.equal(mentionHighlighted(reload, seq), true);
  for (const seq of [4, 5]) assert.equal(mentionHighlighted(reload, seq), false);
  await visibleMessages(reload, [1, 2, 3]); await reload.clock.advance(1000);
  assert.equal(reload.el("mention-notice").hidden, true);
});

test("mention dwell must be continuous, sufficiently visible and in a foreground focused page", async () => {
  const api = server([makeRoom("A")]); api.history.set("A", [message(1, "@tester question")]);
  const ui = await launch(api);
  await visibleMessages(ui, [1]); await ui.clock.advance(600);
  await visibleMessages(ui, []); await ui.clock.advance(1000);
  assert.equal(mentionHighlighted(ui, 1), true);
  await visibleMessages(ui, [1]); await ui.clock.advance(600);
  ui.document.hidden = true; await ui.document.emit("visibilitychange"); await ui.clock.advance(1000);
  assert.equal(mentionHighlighted(ui, 1), true);
  ui.document.hidden = false; await ui.document.emit("visibilitychange");
  await visibleMessages(ui, [1]); await ui.clock.advance(600);
  ui.document.focused = false; await ui.window.emit("blur"); await ui.clock.advance(1000);
  assert.equal(mentionHighlighted(ui, 1), true);
  ui.document.focused = true; await ui.window.emit("focus");
  await visibleMessages(ui, [1]);
  mentionElement(ui, 1).rect = { top: 99, bottom: 179, left: 0, right: 300, width: 300, height: 80 };
  await ui.el("timeline").emit("scroll"); await ui.clock.advance(1000);
  assert.equal(mentionHighlighted(ui, 1), true, "a one-pixel glimpse does not count");
  // A message taller than the viewport can still be read, without fitting entirely.
  mentionElement(ui, 1).rect = { top: -450, bottom: 550, left: 0, right: 300, width: 300, height: 1000 };
  await ui.el("timeline").emit("scroll"); await ui.clock.advance(999);
  assert.equal(mentionHighlighted(ui, 1), true);
  await ui.clock.advance(1); assert.equal(mentionHighlighted(ui, 1), false);
});

test("room switching cancels partial dwell and retains legacy read progress without advancing it", async () => {
  const api = server([makeRoom("A"), makeRoom("B")]);
  api.history.set("A", [1, 2, 3, 4].map(seq => message(seq, "@tester question")));
  api.history.set("B", [message(1, "@tester B question")]);
  const saved = new Map([[JSON.stringify(["teamx.mentions", "instance-A", "tester"]), "2"]]);
  const ui = await launch(api, saved);
  assert.equal(mentionHighlighted(ui, 1), false); assert.equal(mentionHighlighted(ui, 2), false);
  await visibleMessages(ui, [4]); await ui.clock.advance(600);
  await ui.select("B"); await visibleMessages(ui, []); await ui.clock.advance(1000);
  assert.equal(mentionHighlighted(ui, 1), true);
  await ui.select("A"); await visibleMessages(ui, [4]); await ui.clock.advance(600);
  assert.equal(mentionHighlighted(ui, 4), true);
  await ui.clock.advance(400);
  assert.equal(mentionHighlighted(ui, 3), true); assert.equal(mentionHighlighted(ui, 4), false);
  const reload = await launch(api, saved);
  assert.equal(mentionHighlighted(reload, 1), false); assert.equal(mentionHighlighted(reload, 2), false);
  assert.equal(mentionHighlighted(reload, 3), true); assert.equal(mentionHighlighted(reload, 4), false);
});

test("viewport clipping and modal dialogs pause mention viewing; polling does not reset continuous dwell", async () => {
  const api = server([makeRoom("A")]); api.history.set("A", [message(1, "@tester question")]);
  const ui = await launch(api);
  await visibleMessages(ui, [1]);
  ui.el("timeline").rect = { top: 890, bottom: 990, left: 0, right: 300, width: 300, height: 100 };
  mentionElement(ui, 1).rect = { top: 899, bottom: 979, left: 0, right: 300, width: 300, height: 80 };
  await ui.window.emit("resize"); await ui.clock.advance(1000);
  assert.equal(mentionHighlighted(ui, 1), true, "clipped outside the browser viewport");
  ui.el("timeline").rect = null;
  mentionElement(ui, 1).rect = { top: 0, bottom: 80, left: 0, right: 300, width: 300, height: 80 };
  await ui.window.emit("scroll");
  await ui.clock.advance(600);
  ui.el("create-dialog").showModal(); await ui.document.emit("toggle"); await ui.clock.advance(1000);
  assert.equal(mentionHighlighted(ui, 1), true);
  ui.el("create-dialog").close(); await ui.document.emit("close");
  await ui.clock.advance(600); await ui.refresh(); await ui.clock.advance(399);
  assert.equal(mentionHighlighted(ui, 1), true);
  await ui.clock.advance(1); assert.equal(mentionHighlighted(ui, 1), false);
});

test("explicit single-room bootstrap retains composer and member addition without management navigation", async () => {
  const room = makeRoom("single"), api = server([room]);
  api.hook = path => path === "/v1/ui" ? { ...api.catalog.rooms[0]?.service, operator: "tester", profiles: ["runtime"] } : undefined;
  const ui = await launch(api);
  assert.equal(ui.el("room-nav").hidden, true);
  assert.equal(ui.el("toggle-add").hidden, false);
  assert.equal(ui.el("room-name").textContent, "single");
  await ui.draft("single draft"); assert.equal(ui.el("send").disabled, false);
});

test("member list and activity details share the right sidebar, with back, collapse and room changes stopping reads", async () => {
  const api = server([makeRoom("A"), makeRoom("B")]), members = [];
  api.hook = (path, request) => {
    if (path === "/v1/member/list") return { members: structuredClone(members) };
    if (path === "/v1/member/activity") {
      const member = members.find(item => item.membership_id === request.membership_id);
      assert.ok(member);
      return structuredClone({ member, current_output: { result: { events: [{ seq: 1, kind: "text", text: member.agent_id }] } } });
    }
  };
  const ui = await launch(api), toggle = ui.el("toggle-members"), panel = ui.el("activity-dialog"), select = ui.el("activity-member");
  const list = ui.el("member-list-view");
  const reads = () => api.calls.filter(call => call.path === "/v1/member/activity");
  const open = async () => {
    await ui.el("members").children[1].querySelector(".member-summary").emit("click"); await settle();
  };
  assert.equal(ui.el("toggle-activity"), undefined); assert.equal(list.hidden, false);
  members.push(...["observer", "reviewer"].map((id, index) => ({ agent_id: id, membership_id: `generation-${id}`,
    status: index ? "running" : "idle", runner: { profile: "runtime" }, execution: { active: !!index, agent: { turn_number: 1 } } })));
  await ui.refresh(); await settle();
  await open();
  assert.equal(panel.open, true); assert.equal(panel.modal, false);
  assert.equal(toggle.attributes["aria-expanded"], "true"); assert.equal(list.hidden, true); assert.equal(list.inert, true);
  assert.equal(select.value, "generation-reviewer"); assert.equal(select.options.find(option => option.value === select.value).textContent, "reviewer");
  assert.equal(select.hidden, false);
  select.value = "generation-observer"; await select.emit("change"); await settle();
  assert.equal(select.value, "generation-observer"); assert.equal(reads().at(-1).data.agent_id, "observer");
  await ui.el("close-activity").emit("click"); await settle();
  assert.equal(panel.open, false); assert.equal(list.hidden, false); assert.equal(list.inert, false);
  const closedReads = reads().length; await ui.refresh(); await settle(); assert.equal(reads().length, closedReads);
  await open();
  await panel.emit("keydown", { key: "Escape" }); await settle();
  assert.equal(panel.open, false); assert.equal(list.hidden, false);
  await open();
  await toggle.emit("click"); await settle();
  assert.equal(panel.open, false); assert.equal(ui.el("member-rail").inert, true);
  const collapsedReads = reads().length; await ui.refresh(); await settle(); assert.equal(reads().length, collapsedReads);
  await ui.el("execution-text").children[0].emit("click"); await settle();
  assert.equal(panel.open, true); assert.equal(ui.el("member-rail").inert, false);
  assert.equal(toggle.attributes["aria-expanded"], "true"); assert.equal(list.hidden, true);
  await ui.select("B");
  assert.equal(panel.open, false); assert.equal(list.hidden, false);
  await open();
  members.length = 0; await ui.refresh(); await settle();
  assert.equal(panel.open, false); assert.equal(list.hidden, false);
  const reload = await launch(api, ui.saved);
  assert.equal(reload.el("activity-dialog").open, false);
});

test("shared sidebar drag resizing clamps width, persists it and supports keyboard resizing", async () => {
  const api = server([makeRoom("A")]), ui = await launch(api);
  const handle = ui.el("member-resizer"), workspace = ui.el("workspace"), rail = ui.el("member-rail");
  rail.clientWidth = 320;
  workspace.style.setProperty("--members-open-width", "320px");
  rail.getBoundingClientRect = () => ({ width: Number.parseFloat(workspace.style.getPropertyValue("--members-open-width")) || rail.clientWidth });
  const calls = api.calls.length;
  await handle.emit("pointerdown", { button: 0, pointerId: 1, clientX: 1000 });
  assert.equal(handle.hasPointerCapture(1), true); assert.equal(workspace.classList.contains("resizing-members"), true);
  await handle.emit("pointermove", { pointerId: 2, clientX: 0 });
  assert.equal(workspace.style.getPropertyValue("--members-open-width"), "320px");
  await handle.emit("pointermove", { pointerId: 1, clientX: 820 });
  assert.equal(workspace.style.getPropertyValue("--members-open-width"), "500px");
  assert.equal(handle.attributes["aria-valuenow"], "500");
  await handle.emit("pointerup", { pointerId: 1 });
  assert.equal(handle.hasPointerCapture(1), false); assert.equal(workspace.classList.contains("resizing-members"), false);
  assert.equal(api.calls.length, calls);
  const reload = await launch(api, ui.saved);
  assert.equal(reload.el("workspace").style.getPropertyValue("--members-open-width"), "500px");
  const afterReloadCalls = api.calls.length;
  workspace.clientWidth = 800; await ui.window.emit("resize");
  assert.equal(workspace.style.getPropertyValue("--members-open-width"), "220px");
  workspace.clientWidth = 1440; await ui.window.emit("resize");
  assert.equal(workspace.style.getPropertyValue("--members-open-width"), "500px");
  await handle.emit("pointerdown", { button: 0, pointerId: 3, clientX: 1000 });
  await handle.emit("pointermove", { pointerId: 3, clientX: -5000 });
  assert.equal(workspace.style.getPropertyValue("--members-open-width"), "720px");
  await handle.emit("pointermove", { pointerId: 3, clientX: 5000 });
  assert.equal(workspace.style.getPropertyValue("--members-open-width"), "220px");
  await handle.emit("pointercancel", { pointerId: 3 });
  assert.equal(workspace.classList.contains("resizing-members"), false);
  await handle.emit("keydown", { key: "ArrowLeft" });
  assert.equal(workspace.style.getPropertyValue("--members-open-width"), "240px");
  await handle.emit("keydown", { key: "End" });
  assert.equal(workspace.style.getPropertyValue("--members-open-width"), "720px");
  await handle.emit("keydown", { key: "Home" });
  assert.equal(workspace.style.getPropertyValue("--members-open-width"), "220px");
  await handle.emit("keydown", { key: "ArrowRight" });
  assert.equal(workspace.style.getPropertyValue("--members-open-width"), "220px");
  ui.window.innerWidth = 390;
  await handle.emit("pointerdown", { button: 0, pointerId: 4, clientX: 300 });
  assert.equal(handle.hasPointerCapture(4), false);
  assert.equal(api.calls.length, afterReloadCalls);
});

test("left sidebar resizing persists, shares the message-space budget, and preserves preferences on narrow windows", async () => {
  const api = server([makeRoom("A")]), ui = await launch(api);
  const workspace = ui.el("workspace"), handle = ui.el("room-resizer");
  await handle.emit("pointerdown", { button: 0, pointerId: 10, clientX: 240 });
  await handle.emit("pointermove", { pointerId: 11, clientX: 1000 });
  assert.equal(workspace.style.getPropertyValue("--rooms-open-width"), "240px");
  await handle.emit("pointermove", { pointerId: 10, clientX: 320 });
  await handle.emit("lostpointercapture", { pointerId: 10 });
  assert.equal(workspace.style.getPropertyValue("--rooms-open-width"), "320px");
  assert.equal(workspace.classList.contains("resizing-rooms"), false);
  assert.equal(JSON.parse(ui.saved.get("teamx.room-panels")).roomWidth, 320);
  const reload = await launch(api, ui.saved);
  assert.equal(reload.el("workspace").style.getPropertyValue("--rooms-open-width"), "320px");
  const afterReloadCalls = api.calls.length;
  workspace.clientWidth = 800; await ui.window.emit("resize");
  assert.equal(workspace.style.getPropertyValue("--rooms-open-width"), "200px");
  assert.equal(workspace.style.getPropertyValue("--members-open-width"), "220px");
  assert.equal(JSON.parse(ui.saved.get("teamx.room-panels")).roomWidth, 320);
  workspace.clientWidth = 1440; await ui.window.emit("resize");
  assert.equal(workspace.style.getPropertyValue("--rooms-open-width"), "320px");
  await handle.emit("keydown", { key: "Home" });
  assert.equal(workspace.style.getPropertyValue("--rooms-open-width"), "200px");
  await handle.emit("keydown", { key: "ArrowRight" });
  assert.equal(workspace.style.getPropertyValue("--rooms-open-width"), "220px");
  await handle.emit("keydown", { key: "End" });
  assert.equal(workspace.style.getPropertyValue("--rooms-open-width"), "360px");
  await ui.el("toggle-rooms").emit("click");
  await handle.emit("pointerdown", { button: 0, pointerId: 12, clientX: 52 });
  assert.equal(handle.hasPointerCapture(12), false);
  assert.equal(api.calls.length, afterReloadCalls);
});

test("execution hints open the matching member activity without sending or changing execution", async () => {
  const api = server([makeRoom("A")]);
  const members = ["reviewer", "developer"].map((id, index) => ({ agent_id: id, membership_id: `generation-${id}`,
    status: index ? "waiting" : "running", read_through: 0, unread_count: 1, runner: { profile: "runtime" },
    execution: { active: !index, agent: { turn_number: 1 } } }));
  api.hook = (path, request) => {
    if (path === "/v1/member/list") return { members: structuredClone(members) };
    if (path === "/v1/member/activity") {
      const member = members.find(item => item.membership_id === request.membership_id);
      assert.ok(member);
      return structuredClone({ member, [member.execution.active ? "current_output" : "last_output"]:
        { result: { events: [{ seq: 1, kind: "text", text: `activity-${member.agent_id}` }] } } });
    }
  };
  const ui = await launch(api);
  const buttons = () => ui.el("execution-text").children.filter(child => child.tag === "button");
  const reads = () => api.calls.filter(call => call.path === "/v1/member/activity");
  const open = async button => {
    await button.emit("click"); await settle();
    assert.equal(ui.el("activity-dialog").open, true);
  };
  const close = async () => { await ui.el("close-activity").emit("click"); await settle(); };
  assert.equal(buttons()[0].textContent, "reviewer 正在处理…");
  await open(buttons()[0]);
  assert.equal(ui.el("activity-member").value, "generation-reviewer");
  assert.equal(reads().at(-1).data.membership_id, members[0].membership_id);
  assert.equal(ui.el("activity-timeline").children[0].children[1].children[0].textContent, "activity-reviewer");
  await close();
  members[1].status = "running"; members[1].execution.active = true;
  await ui.refresh(); await settle();
  assert.deepEqual(buttons().map(button => button.textContent), ["reviewer", "developer"]);
  await open(buttons()[1]); assert.equal(ui.el("activity-member").value, members[1].membership_id); await close();
  const oldButton = buttons()[1];
  members[1].membership_id = "replacement-developer";
  await ui.refresh(); await settle();
  assert.notEqual(buttons()[1], oldButton);
  const before = reads().length; await oldButton.emit("click"); await settle();
  assert.equal(reads().length, before); assert.equal(ui.el("activity-dialog").open, false);
  await open(buttons()[1]); assert.equal(reads().at(-1).data.membership_id, "replacement-developer"); await close();
  members[0].status = "idle"; members[0].execution.active = false;
  members[1].status = "waiting"; members[1].execution.active = false;
  await ui.refresh(); await settle();
  assert.equal(buttons()[0].textContent, "developer 正在等待执行…");
  await open(buttons()[0]); assert.equal(ui.el("activity-member").value, members[1].membership_id); await close();
  assert.equal(api.calls.some(call => /^\/v1\/(send|member\/(run|pause|cancel))$/.test(call.path)), false);
});


test("activity flow keeps interleaved order, row identity and disclosure choices through completion and retention", async () => {
  const api = server([makeRoom("A")]);
  const member = { agent_id: "reviewer", membership_id: "generation-1", status: "running", read_through: 0,
    unread_count: 1, runner: { profile: "runtime" }, execution: { active: true, agent: { turn_number: 1 } } };
  const result = { events: [
    { seq: 1, kind: "thought", text: "检查文件" },
    { seq: 2, kind: "tool", tool: { id: "shell", title: "运行测试", status: "in_progress", input: "go test" } },
    { seq: 3, kind: "thought", text: "等待结果" },
    { seq: 4, kind: "text", text: "处理中" },
  ] };
  let output = { member, current_output: { result } };
  api.hook = path => path === "/v1/member/list" ? { members: [member] }
    : path === "/v1/member/activity" ? structuredClone(output) : undefined;
  const ui = await launch(api);
  await ui.el("members").children[0].querySelector(".member-summary").emit("click");
  await settle();
  assert.equal(ui.el("activity-timeline").hidden, false);
  const rows = () => ui.el("activity-timeline").children;
  const titles = () => rows().map(row => row.children[0].children[0].textContent);
  assert.deepEqual(titles(), ["思考", "工具 · 运行测试", "思考", "内部回复"]);
  const [thought, tool, laterThought, reply] = rows();
  assert.deepEqual(rows().map(row => row.open), [false, false, false, true]);
  thought.open = true; tool.open = true; reply.open = false;
  result.events[0].text += "，准备测试";
  result.events.push({ seq: 5, kind: "tool", tool: { id: "shell", title: "运行测试", status: "completed", output: "tests passed" } });
  result.events.push({ seq: 6, kind: "text", text: "<script>literal reply</script>" });
  await ui.refresh(); await settle();
  assert.equal(rows()[0], thought); assert.equal(rows()[1], tool); assert.equal(rows()[3], reply);
  assert.deepEqual(rows().slice(0, 4).map(row => row.open), [true, true, false, false]);
  assert.equal(thought.children[1].children[0].textContent, "检查文件，准备测试");
  assert.deepEqual(titles(), ["思考", "工具 · 运行测试", "思考", "内部回复", "工具 · 运行测试", "内部回复"]);
  assert.equal(rows()[1].children[0].children[1].textContent, "执行中");
  assert.equal(rows()[4].children[0].children[1].textContent, "已完成");
  assert.equal(rows()[4].children[1].children[4].textContent, "tests passed");
  assert.equal(rows()[5].children[1].children[0].textContent, "<script>literal reply</script>");
  member.execution.active = false;
  result.stopReason = "end_turn";
  output = { member, last_output: { result } };
  await ui.refresh(); await settle();
  assert.equal(rows()[0], thought); assert.equal(rows()[3].open, false);
  assert.equal(ui.el("activity-output-title").textContent, "最近完成的回合");
  result.events = result.events.slice(2); result.eventsTruncated = true;
  await ui.refresh(); await settle();
  assert.equal(rows().length, 4); assert.equal(rows()[0], laterThought); assert.equal(rows()[1], reply);
  assert.equal(ui.el("activity-timeline-truncated").hidden, false);
  member.execution.agent.turn_number = 2; member.execution.active = true;
  output = { member, current_output: { result: { events: [] } } };
  await ui.refresh(); await settle();
  assert.equal(rows().length, 0); assert.equal(ui.el("activity-timeline").hidden, false);
  assert.equal(ui.el("activity-timeline-empty").textContent, "等待模型或工具活动…");
  output.current_output.result.events.push({ seq: 1, kind: "text", text: "新回合" });
  await ui.refresh(); await settle();
  assert.equal(rows().length, 1); assert.notEqual(rows()[0], thought); assert.equal(rows()[0].open, true);
  await ui.el("close-activity").emit("click"); await settle();
  assert.equal(rows().length, 0);
});

test("activity follows appended spans and growing replies, pauses while reading earlier activity and resumes at the bottom", async () => {
  const api = server([makeRoom("A")]);
  const member = { agent_id: "reviewer", membership_id: "generation-1", status: "running", read_through: 0,
    unread_count: 1, runner: { profile: "runtime" }, execution: { active: true, agent: { turn_number: 1 } } };
  const result = { events: [{ seq: 1, kind: "text", text: "正在检查文件。" }] };
  let output = { member, current_output: { result } };
  let failedRead = false;
  api.hook = path => path === "/v1/member/list" ? { members: [member] }
    : path === "/v1/member/activity" ? failedRead ? Promise.reject(new Error("offline")) : structuredClone(output) : undefined;
  const ui = await launch(api), body = ui.el("activity-body"), jump = ui.el("activity-jump-latest");
  // Simulate browser layout growth and scrollTop clamping. Heights change only
  // after the real renderer updates its rows, including an existing text span.
  body.clientHeight = 200;
  let top = 0;
  Object.defineProperty(body, "scrollHeight", { get: () => ui.el("activity-output").hidden ? 200 :
    300 + ui.el("activity-timeline").children.reduce((height, row) => height + 90 + row.children[1].children[0].textContent.length, 0) });
  Object.defineProperty(body, "scrollTop", { get: () => top, set: value => { top = Math.max(0, Math.min(value, body.scrollHeight - body.clientHeight)); } });
  const atBottom = () => assert.equal(body.scrollTop, body.scrollHeight - body.clientHeight);
  const open = async () => {
    await ui.el("members").children[0].querySelector(".member-summary").emit("click"); await settle();
  };
  await open(); atBottom(); assert.equal(jump.hidden, true);
  result.events.push({ seq: 2, kind: "tool", tool: { id: "test", title: "运行测试", status: "in_progress" } });
  await ui.refresh(); await settle(); atBottom();
  result.events.push({ seq: 3, kind: "text", text: "测试中。" });
  await ui.refresh(); await settle(); atBottom();
  result.events[2].text += "持续输出。".repeat(40);
  await ui.refresh(); await settle(); atBottom();
  body.scrollTop = 40; await body.emit("scroll"); assert.equal(jump.hidden, false);
  result.events[2].text += "更多内容。".repeat(40);
  await ui.refresh(); await settle(); assert.equal(body.scrollTop, 40); assert.equal(jump.hidden, false);
  failedRead = true; await ui.refresh(); await settle();
  assert.equal(ui.el("activity-output").hidden, true);
  body.scrollTop = 0; await body.emit("scroll");
  failedRead = false; await ui.refresh(); await settle();
  assert.equal(body.scrollTop, 40); assert.equal(jump.hidden, false);
  await jump.emit("click"); atBottom(); assert.equal(jump.hidden, true);
  result.events[2].text += "继续输出。".repeat(20);
  await ui.refresh(); await settle(); atBottom();
  body.scrollTop = body.scrollHeight - body.clientHeight - 30;
  await body.emit("scroll"); assert.equal(jump.hidden, true);
  result.events[2].text += "接近结束。".repeat(20);
  await ui.refresh(); await settle(); atBottom();
  body.scrollTop = 60; await body.emit("scroll");
  member.execution.active = false; result.stopReason = "end_turn";
  output = { member, last_output: { result } };
  await ui.refresh(); await settle(); assert.equal(body.scrollTop, 60); assert.equal(jump.hidden, false);
  await ui.el("close-activity").emit("click"); await settle(); assert.equal(jump.hidden, true);
  await open(); atBottom(); assert.equal(jump.hidden, true);
});

test("deletion requires confirmation and removes the active view and draft while preserving another Room", async () => {
  const api = server([makeRoom("A"), makeRoom("B")]), ui = await launch(api);
  const remove = ui.el("room-list").children[0].children[1];
  assert.equal(remove.textContent, "");
  assert.equal(remove.children[0].tag, "svg");
  assert.equal(remove.attributes["aria-label"], "删除房间「A」");
  await ui.draft("private A draft");
  let confirmation;
  api.confirm = text => { confirmation = text; return false; };
  await ui.delete("A");
  assert.match(confirmation, /所有 Agent 私有会话.*历史会话/);
  assert.match(confirmation, /工作区文件.*无法撤销/);
  assert.equal(api.calls.some(call => call.path === "/v1/rooms/delete"), false);
  assert.equal(ui.el("room-name").textContent, "A");
  api.confirm = () => true;
  await ui.delete("A");
  assert.deepEqual(api.catalog.rooms.map(room => room.name), ["B"]);
  assert.equal(ui.el("room-name").textContent, "选择或新建房间");
  assert.equal(ui.el("message-count").textContent, "0 条消息");
  assert.equal(ui.el("message-input").value, "");
  assert.equal(ui.el("send").disabled, true);
  assert.equal(ui.saved.has(JSON.stringify(["teamx.draft", "instance-A", "tester"])), false);
  assert.match(ui.el("management-notice").textContent, /已删除/);
  await ui.select("B");
  assert.equal(ui.el("message-count").textContent, "1 条消息");
  assert.equal(api.calls.filter(call => call.path === "/v1/rooms/delete").length, 1);
});

test("lost deletion response is resolved by the catalog without resubmitting", async () => {
  const api = server([makeRoom("A"), makeRoom("B")]), actual = api.request;
  api.request = async (path, payload) => {
    const result = await actual(path, payload);
    if (path === "/v1/rooms/delete") throw new Error("lost after removal");
    return result;
  };
  const ui = await launch(api);
  await ui.delete("B");
  assert.equal(ui.el("room-name").textContent, "A");
  assert.match(ui.el("management-notice").textContent, /已删除/);
  const reload = await launch(api, ui.saved);
  assert.equal(reload.el("room-list").children.length, 1);
  assert.equal(api.calls.filter(call => call.path === "/v1/rooms/delete").length, 1);
});

test("running or failed deletion remains visible and reload waits for an explicit retry", async () => {
  const api = server([makeRoom("A"), makeRoom("B")]);
  api.hook = (path, payload) => {
    if (path !== "/v1/rooms/delete") return;
    const room = api.catalog.rooms.find(room => room.room_id === payload.room_id);
    room.status = "deleting"; room.deletion_phase = "closing"; room.busy = true;
    return structuredClone(room);
  };
  const ui = await launch(api);
  await ui.delete("A");
  assert.equal(ui.el("send").disabled, true);
  assert.equal(ui.el("new-room").disabled, true);
  assert.match(ui.el("management-notice").textContent, /结清会话并删除/);
  await ui.delete("B");
  assert.equal(api.calls.filter(call => call.path === "/v1/rooms/delete").length, 1);
  api.catalog.rooms[0].busy = false;
  api.catalog.rooms[0].error = "Session cleanup incomplete";
  delete api.catalog.rooms[0]?.service;
  const reload = await launch(api, ui.saved);
  assert.match(reload.el("management-notice").textContent, /Session cleanup incomplete/);
  assert.equal(reload.el("retry-management").hidden, false);
  assert.equal(api.calls.filter(call => call.path === "/v1/rooms/delete").length, 1);
  api.hook = null;
  await reload.el("retry-management").emit("click"); await settle();
  assert.equal(api.calls.filter(call => call.path === "/v1/rooms/delete").length, 2);
  assert.deepEqual(api.catalog.rooms.map(room => room.name), ["B"]);
});

test("another tab's deletion clears the view and late sends cannot restore a deleted draft", async () => {
  const api = server([makeRoom("A"), makeRoom("B")]), ui = await launch(api);
  let complete;
  api.hook = path => path === "/v1/send" ? new Promise(resolve => { complete = resolve; }) : undefined;
  await ui.draft("late send A");
  const sending = ui.el("compose").emit("submit"); await settle();
  api.catalog.rooms.shift();
  await ui.refresh();
  const key = JSON.stringify(["teamx.draft", "instance-A", "tester"]);
  assert.equal(ui.saved.has(key), false);
  complete({ seq: 2 }); await sending;
  assert.equal(ui.saved.has(key), false);
  assert.equal(ui.el("message-input").value, "");
  await ui.select("B");
  assert.equal(ui.el("room-name").textContent, "B");
});

function workspaceServer(rooms = [makeRoom("A"), makeRoom("B")]) {
  const api = server(rooms), base = api.request;
  const workspaces = new Map(rooms.map(room => [room.room_id, { ...identity(room), workspace: { root: "workspaces" }, workspace_version: 1,
    credential_status: "missing", members: [{ agent_id: "developer", membership_id: `member-${room.room_id}`, workspace: `workspaces/member-${room.room_id}` }] }]));
  api.workspaces = workspaces;
  api.connections = [{ id: "a".repeat(32), name: "Company GitLab", provider: "gitlab", url: "https://gitlab.invalid", version: 1, credential_configured: true, validation: { status: "unverified" } }];
  api.workspaceHook = null;
  api.request = async (path, data) => {
    if (path.startsWith("/v1/platform/connections?")) return { connections: structuredClone(api.connections) };
    if (!path.startsWith("/v1/workspace")) return base(path, data);
    api.calls.push({ path, data: structuredClone(data) });
    if (api.workspaceHook) {
      const intercepted = api.workspaceHook(path, data);
      if (intercepted !== undefined) return intercepted;
    }
    if (path.startsWith("/v1/workspace?")) {
      const q = new URL(path, "http://fixture").searchParams;
      assert.equal(q.get("service_epoch"), "manager"); assert.equal(q.get("caller_id"), "tester");
      const ws = workspaces.get(q.get("room_id"));
      assert.equal(ws.room_instance, q.get("room_instance"));
      if (q.get("operation_id") && ws.operation?.operation_id !== q.get("operation_id")) throw Object.assign(new Error("no receipt"), { code: "operation_not_found" });
      return structuredClone(ws);
    }
    assert.equal(data.service_epoch, "manager");
    const ws = workspaces.get(data.room_id);
    assert.equal(data.room_instance, ws.room_instance);
    if (path === "/v1/workspace/retry") {
      assert.equal(data.operation_id, ws.operation.operation_id);
      assert.equal(data.pat, undefined); assert.equal(data.target, undefined); assert.equal(data.expected_version, undefined);
      ws.operation.status = "completed"; ws.operation.phase = "completed";
    } else {
      assert.equal(data.expected_version, ws.workspace_version);
      const target = structuredClone(data.target);
      if (target.repository && !ws.workspace.repository) target.root = `workspaces/repos/${data.operation_id}`;
      assert.equal(data.pat, undefined);
      if (target.binding?.connection_id) { target.binding.project_id = 7; target.binding.connection_generation = "f".repeat(32); }
      ws.workspace = target; ws.workspace_version++;
      ws.credential_status = target.binding ? "configured" : "missing";
      ws.members[0].workspace = `${target.root}/${ws.members[0].membership_id}`;
      ws.operation = { operation_id: data.operation_id, status: "completed", phase: "completed", target };
    }
    const room = api.catalog.rooms.find(r => r.room_id === data.room_id);
    room.service = identity(room, `workspace-${ws.workspace_version}`);
    return structuredClone(ws.operation);
  };
  return api;
}
async function editWorkspace(ui, gitlab = false) {
  await ui.el("open-workspace").emit("click");
  ui.el("workspace-repository").checked = true; await ui.el("workspace-repository").emit("change");
  ui.el("workspace-repo-url").value = "https://gitlab.invalid/group/project.git";
  if (gitlab) {
    ui.el("workspace-gitlab").checked = true; await ui.el("workspace-gitlab").emit("change");
    ui.el("workspace-connection").value = "a".repeat(32); await ui.el("workspace-connection").emit("change");
  }
}

test("workspace picker previews the directory and closes on Escape, settings and Room switch without applying changes", async () => {
  const api = workspaceServer(), ui = await launch(api);
  const picker = ui.el("workspace-picker");
  const open = async () => { picker.open = true; await picker.emit("toggle"); };
  assert.equal(ui.el("workspace-label").textContent, "默认目录");
  assert.equal(ui.el("workspace-selected-description").textContent, "沿用各成员自己的工作目录");
  await open(); assert.equal(ui.el("workspace-trigger").attributes["aria-expanded"], "true");
  await picker.emit("keydown", { key: "Escape" });
  assert.equal(picker.open, false); assert.equal(ui.el("workspace-trigger").attributes["aria-expanded"], "false");
  await open(); await ui.el("open-workspace").emit("click");
  assert.equal(picker.open, false); assert.equal(ui.el("workspace-dialog").open, true);
  await ui.el("close-workspace").emit("click");
  await open(); await ui.select("B");
  assert.equal(picker.open, false);
  assert.equal(api.calls.filter(c => c.path === "/v1/workspace/apply" || c.path === "/v1/send").length, 0);
});

test("Workspace allows discussion before explicit Git-only binding; keeps draft/history and locks saved repository", async () => {
  const api = workspaceServer(), ui = await launch(api);
  await ui.draft("Discuss before binding"); await ui.el("compose").emit("submit");
  await ui.draft("Unsent draft stays");
  await editWorkspace(ui);
  assert.equal(ui.el("workspace-branch").value, "main");
  assert.equal(api.calls.filter(c => c.path === "/v1/workspace/apply").length, 0);
  assert.equal(ui.el("workspace-root").readOnly, true);
  await ui.el("workspace-form").emit("submit");
  const sent = api.calls.find(c => c.path === "/v1/workspace/apply").data;
  assert.equal(sent.target.repository.branch, "main");
  assert.equal(sent.target.repository.kind, "git");
  assert.match(sent.operation_id, /^[0-9a-f]{32}$/); assert.equal(sent.pat, undefined); assert.equal(sent.target.binding, undefined);
  assert.equal(ui.el("message-input").value, "Unsent draft stays"); assert.equal(ui.el("message-count").textContent, "2 条消息");
  assert.equal(ui.el("workspace-repository").disabled, true); assert.equal(ui.el("workspace-repo-url").readOnly, true);
  assert.equal(ui.el("workspace-dialog").open, false);
  assert.match(ui.el("workspace-current").textContent, /workspaces\/repos/);
  assert.equal(ui.el("workspace-label").textContent, "project");
  assert.equal(ui.el("workspace-selected-label").textContent, "project");
  await ui.el("open-workspace").emit("click"); await ui.refresh();
  assert.equal(ui.el("workspace-dialog").open, true); // Completed settings remain available for inspection.
  assert.equal(api.calls.filter(c => c.path === "/v1/send").length, 1);
});

test("Workspace waits for asynchronous completion before closing and can be reopened afterward", async () => {
  const api = workspaceServer(), ui = await launch(api), ws = api.workspaces.get("A");
  api.workspaceHook = (path, data) => {
    if (path !== "/v1/workspace/apply") return;
    ws.operation = { operation_id: data.operation_id, status: "running", phase: "preparing", target: data.target };
    ws.observation = identity(api.catalog.rooms[0]); api.catalog.rooms[0].busy = true; delete api.catalog.rooms[0]?.service;
    return structuredClone(ws.operation);
  };
  await editWorkspace(ui);
  await ui.el("workspace-form").emit("submit");
  assert.equal(ui.el("workspace-dialog").open, true);
  await ui.refresh(); assert.equal(ui.el("workspace-dialog").open, true);
  ws.operation.status = "completed"; ws.operation.phase = "completed";
  ws.workspace = ws.operation.target; ws.workspace_version++;
  api.catalog.rooms[0].busy = false; api.catalog.rooms[0].service = identity(api.catalog.rooms[0], "workspace-completed");
  await ui.refresh();
  assert.equal(ui.el("workspace-dialog").open, false);
  assert.equal(ui.el("workspace-summary").textContent, "设置已生效");
  await ui.el("open-workspace").emit("click"); await ui.refresh();
  assert.equal(ui.el("workspace-dialog").open, true);
});

test("GitLab binding derives its origin and nested project only from the final repository URL", async () => {
  for (const [url, origin, project] of [
    ["https://gitlab.invalid/group/subgroup/project.git", "https://gitlab.invalid", "group/subgroup/project"],
    ["https://gitlab.invalid:8443/group/project/", "https://gitlab.invalid:8443", "group/project"],
    ["https://GITLAB.invalid:443/group/project.git/", "https://gitlab.invalid:443", "group/project"],
  ]) {
    const api = workspaceServer(); api.connections[0].url = origin; const ui = await launch(api);
    assert.equal(ui.el("workspace-platform-url"), undefined); assert.equal(ui.el("workspace-project"), undefined);
    await editWorkspace(ui, true);
    ui.el("workspace-repo-url").value = url;

    await ui.el("workspace-form").emit("submit");
    const sent = api.calls.find(c => c.path === "/v1/workspace/apply").data;
    assert.equal(sent.target.repository.url, `${origin}/${project}.git`);
    assert.equal(ui.el("workspace-repo-url").value, `${origin}/${project}.git`);
    assert.deepEqual(sent.target.binding, { provider: "gitlab", url: origin, project, connection_id: "a".repeat(32) });
    assert.equal(sent.pat, undefined);
  }
});

test("GitLab binding rejects invalid or ambiguous repository paths before retaining a request", async () => {
  for (const url of ["http://gitlab.invalid/group/project", "https://gitlab.invalid/project", "https://gitlab.invalid/group/../project", "https://gitlab.invalid/group%2fproject", "https://gitlab.invalid/group/project?token=fake-secret"]) {
    const api = workspaceServer(), ui = await launch(api);
    await editWorkspace(ui, true); ui.el("workspace-repo-url").value = url;

    await ui.el("workspace-form").emit("submit");
    assert.equal(api.calls.filter(c => c.path === "/v1/workspace/apply").length, 0);
    assert.match(ui.el("workspace-error").textContent, /仓库 HTTPS URL/);
    assert.equal(ui.el("workspace-error").textContent.includes("fake-secret"), false);
  }
});

test("Workspace selects an existing connection for initial and supplemental binding without receiving a PAT", async () => {
  for (const supplement of [false, true]) {
    const api = workspaceServer(), ui = await launch(api);
    await editWorkspace(ui);
    if (supplement) { await ui.el("workspace-form").emit("submit"); await ui.el("open-workspace").emit("click"); }
    ui.el("workspace-gitlab").checked = true; await ui.el("workspace-gitlab").emit("change");
    ui.el("workspace-connection").value = "a".repeat(32); await ui.el("workspace-connection").emit("change");
    await ui.refresh(); assert.equal(ui.el("workspace-connection").value, "a".repeat(32));
    await ui.el("workspace-form").emit("submit");
    const first = api.calls.filter(c => c.path === "/v1/workspace/apply").at(-1).data;
    assert.equal(first.pat, undefined); assert.equal(first.target.binding.credential_id, undefined);
    assert.equal(first.target.binding.connection_id, "a".repeat(32)); assert.equal(first.connection_version, 1);
    assert.equal(ui.el("workspace-gitlab").disabled, true);
    const reload = await launch(api, ui.saved); await reload.el("open-workspace").emit("click");
    assert.equal(reload.el("workspace-connection").value, "a".repeat(32));
    assert.equal(reload.el("workspace-pat"), undefined);
  }
});


test("Workspace plain root, connection choice and suggestions require an explicit application", async () => {
  const api = workspaceServer();
  api.workspaces.get("A").suggestions = { repository: { url: "https://gitlab.invalid/group/project.git", branch: "main" },
    binding: { provider: "gitlab", url: "https://gitlab.invalid", project: "group/project" } };
  const ui = await launch(api); await ui.el("open-workspace").emit("click");
  assert.equal(ui.el("workspace-repository").checked, false); assert.equal(ui.el("workspace-gitlab").checked, false);
  assert.equal(ui.el("workspace-repo-url").value, "https://gitlab.invalid/group/project.git");
  ui.el("workspace-root").value = "outputs"; await ui.el("workspace-form").emit("submit");
  assert.deepEqual(api.calls.find(c => c.path === "/v1/workspace/apply").data.target, { root: "outputs" });
  await editWorkspace(ui, true);
  await ui.el("workspace-form").emit("submit");
  const sent = api.calls.filter(c => c.path === "/v1/workspace/apply").at(-1).data;
  assert.equal(sent.pat, undefined); assert.equal(sent.target.binding.connection_id, "a".repeat(32));
});

test("Workspace lost response observes original ID across reload, blocks duplicates, and explicitly retries only the receipt", async () => {
  const api = workspaceServer(), ui = await launch(api);
  await editWorkspace(ui, true);
  let reject, submitted;
  api.workspaceHook = (path, data) => {
    if (path === "/v1/workspace/apply") {
      submitted = data;
      return new Promise((_, fail) => { reject = fail; });
    }
  };
  const task = ui.el("workspace-form").emit("submit"); await settle();

  await ui.el("workspace-form").emit("submit");
  assert.equal(api.calls.filter(c => c.path === "/v1/workspace/apply").length, 1);
  const ws = api.workspaces.get("A");
  ws.operation = { operation_id: submitted.operation_id, target: submitted.target, status: "running", phase: "preparing" };
  ws.observation = identity(api.catalog.rooms[0]); api.catalog.rooms[0].busy = true; delete api.catalog.rooms[0]?.service;
  reject(new Error("response lost")); await task;
  assert.equal(ui.el("workspace-dialog").open, true);
  assert.equal(ui.el("message-input").readOnly, true); assert.match(ui.el("workspace-summary").textContent, /正在初始化/);
  assert.match(ui.el("management-notice").textContent, /Workspace/);
  assert.equal(ui.el("message-count").textContent, "1 条消息");
  const reload = await launch(api, ui.saved); await reload.el("open-workspace").emit("click");

  assert.equal(api.calls.filter(c => c.path === "/v1/workspace/apply").length, 1);
  ws.operation.status = "failed"; ws.operation.error_code = "preparation_failed"; api.catalog.rooms[0].busy = false;
  await reload.refresh(); assert.equal(reload.el("workspace-retry").hidden, false);
  assert.equal(reload.el("workspace-dialog").open, true);
  assert.equal(reload.el("workspace-apply").disabled, true);
  await reload.el("workspace-retry").emit("click");
  assert.equal(reload.el("workspace-dialog").open, false);
  const retry = api.calls.find(c => c.path === "/v1/workspace/retry").data;
  assert.equal(retry.operation_id, submitted.operation_id); assert.equal(retry.target, undefined); assert.equal(retry.pat, undefined);
});

test("failed initialization restores an editable target, keeps message nodes stable, and replaces only on explicit apply", async () => {
  const api = workspaceServer(), ws = api.workspaces.get("A"), room = api.catalog.rooms[0];
  const failedID = "a".repeat(32);
  ws.operation = { operation_id: failedID, status: "failed", phase: "preparing", error_code: "preparation_failed",
    target: { root: `workspaces/repos/${failedID}`, repository: { url: "https://gitlab.invalid/group/old", branch: "main" },
      binding: { provider: "gitlab", url: "https://gitlab.invalid", project: "group/old" } } };
  ws.can_revise = true; ws.observation = identity(room); delete api.catalog.rooms[0]?.service;
  const ui = await launch(api);
  await ui.el("open-workspace").emit("click");
  assert.equal(ui.el("workspace-fields").disabled, false);
  assert.equal(ui.el("workspace-repository").disabled, false);
  assert.equal(ui.el("workspace-repository").checked, true);
  assert.equal(ui.el("workspace-repo-url").value, "https://gitlab.invalid/group/old");
  assert.equal(ui.el("workspace-root").value, "workspaces");
  assert.equal(ui.el("workspace-apply").disabled, false);
  assert.equal(ui.el("message-input").readOnly, true);
  const nodes = [...ui.el("messages").children];
  ui.el("timeline").scrollTop = 17;
  ui.el("workspace-repo-url").value = "https://gitlab.invalid/group/corrected/";
  await ui.refresh(); await ui.refresh();
  assert.deepEqual(ui.el("messages").children, nodes);
  assert.equal(ui.el("timeline").scrollTop, 17);
  assert.equal(ui.el("workspace-repo-url").value, "https://gitlab.invalid/group/corrected/");
  const reads = api.calls.filter(call => call.path === "/v1/read");
  assert.equal(reads.at(-1).data.after, 1);
  assert.equal(api.calls.some(call => call.path === "/v1/workspace/apply" || call.path === "/v1/workspace/retry"), false);
  ui.el("workspace-connection").value = "a".repeat(32); await ui.el("workspace-connection").emit("change");

  await ui.el("workspace-form").emit("submit");
  const sent = api.calls.find(call => call.path === "/v1/workspace/apply").data;
  assert.equal(sent.replaces_operation_id, failedID); assert.notEqual(sent.operation_id, failedID);
  assert.equal(sent.target.repository.url, "https://gitlab.invalid/group/corrected.git");
  assert.equal(sent.expected_version, 1); assert.equal(sent.pat, undefined);
});

test("Workspace unrecorded timeout never resends; new application needs explicit action and a newly selected connection", async () => {
  const api = workspaceServer(), ui = await launch(api);
  await editWorkspace(ui, true);
  api.workspaceHook = path => path === "/v1/workspace/apply" ? Promise.reject(new Error("not received")) : undefined;
  await ui.el("workspace-form").emit("submit");
  assert.equal(ui.el("workspace-new").hidden, false); assert.equal(ui.el("workspace-apply").disabled, true);
  await ui.refresh(); assert.equal(api.calls.filter(c => c.path === "/v1/workspace/apply").length, 1);
  const reload = await launch(api, ui.saved); await reload.el("open-workspace").emit("click");
  assert.equal(reload.el("workspace-new").hidden, false);
  await reload.el("workspace-new").emit("click");
  await editWorkspace(reload, true); reload.el("workspace-connection").value = ""; await reload.el("workspace-connection").emit("change"); await reload.el("workspace-form").emit("submit");
  assert.equal(api.calls.filter(c => c.path === "/v1/workspace/apply").length, 1); // Empty connection selection fails locally.
  api.workspaceHook = null; reload.el("workspace-connection").value = "a".repeat(32); await reload.el("workspace-connection").emit("change");
  await reload.el("workspace-form").emit("submit");
  const calls = api.calls.filter(c => c.path === "/v1/workspace/apply");
  assert.notEqual(calls[0].data.operation_id, calls[1].data.operation_id);
});

test("Workspace switch clears form fields; delayed old-room queries never populate a new room", async () => {
  const api = workspaceServer(), ui = await launch(api);
  await editWorkspace(ui, true);
  await ui.select("B"); assert.equal(ui.el("workspace-dialog").open, false);
  await ui.el("open-workspace").emit("click"); assert.equal(ui.el("workspace-repo-url").value, "");
  assert.equal(ui.el("workspace-repository").checked, false);
  const reload = await launch(api, ui.saved); await reload.el("open-workspace").emit("click");

  let release;
  api.workspaceHook = path => path.startsWith("/v1/workspace?") ? new Promise(resolve => { release = resolve; }) : undefined;
  const refresh = ui.refresh(); await settle();
  const switchTask = ui.select("A"); await settle();
  release({ ...api.workspaces.get("B"), workspace: { root: "stale-B" } });
  await refresh; await switchTask; api.workspaceHook = null; await ui.refresh();
  await ui.el("open-workspace").emit("click");
  assert.equal(ui.el("workspace-root").value, "workspaces"); assert.equal(ui.el("workspace-repo-url").value, "");
});

test("Workspace rejects credential-bearing URLs before retention and preserves no credentials", async () => {
  const api = workspaceServer(), ui = await launch(api);
  await editWorkspace(ui, true); ui.el("workspace-repo-url").value = "https://name:fake-url-token@gitlab.invalid/group/project.git";
   await ui.el("workspace-form").emit("submit");
  assert.equal(api.calls.filter(c => c.path === "/v1/workspace/apply").length, 0);
  assert.equal([...ui.saved.values()].join("").includes("fake-url-token"), false);
  assert.equal(ui.el("workspace-error").textContent.includes("fake-url-token"), false);
  await ui.el("close-workspace").emit("click");
});

test("Workspace version conflict refreshes saved configuration, loads saved configuration and never retries automatically", async () => {
  const api = workspaceServer(), ui = await launch(api);
  await editWorkspace(ui, true);
  api.workspaceHook = path => {
    if (path === "/v1/workspace/apply") {
      const ws = api.workspaces.get("A"); ws.workspace_version++; ws.workspace.root = "other-tab-outputs";
      return Promise.reject(Object.assign(new Error("conflict"), { code: "conflict" }));
    }
  };
  await ui.el("workspace-form").emit("submit");
   assert.equal(ui.el("workspace-root").value, "other-tab-outputs");
  assert.match(ui.el("workspace-error").textContent, /配置已变化/);
  await ui.refresh(); assert.equal(api.calls.filter(c => c.path === "/v1/workspace/apply").length, 1);
});

test("Workspace query reconnect keeps edits within one version and clears only the connection error", async () => {
  const api = workspaceServer(), ui = await launch(api); await editWorkspace(ui, true);

  api.workspaceHook = path => path.startsWith("/v1/workspace?") ? Promise.reject(new Error("offline")) : undefined;
  await ui.refresh(); assert.equal(ui.el("workspace-apply").disabled, true);
  assert.match(ui.el("workspace-error").textContent, /暂时无法查询/);
  api.workspaceHook = null; await ui.refresh();
  assert.equal(ui.el("workspace-error").hidden, true); assert.equal(ui.el("workspace-connection").value, "a".repeat(32));
  assert.equal(ui.el("workspace-apply").disabled, false);
});



test("integration messages display GitLab author without becoming members", async () => {
  const api = server([makeRoom("A")]);
  api.history.set("A", [{ ...message(1, "GitLab · MR !42 已开启"), author: { kind: "integration", id: "gitlab" }, source: { provider: "gitlab", channel: "events", instance: "https://gitlab.invalid", subscription_id: "a".repeat(32), item_key: "https://gitlab.invalid|7|9", project_id: 7 } }]);
  const ui = await launch(api);
  const el = ui.document.getElementById("message-1");
  assert.equal(el.querySelector(".author").textContent, "GitLab · 项目活动");
  assert.equal(el.querySelector(".author-kind").textContent, "通知");
  assert.equal(el.classList.contains("integration"), true);
  assert.equal(String(ui.el("member-count").textContent), "0");
});





test("connection save returns only after confirmation and a late list read cannot hide the saved item", async () => {
  const api = server([]);
 const item={id:"a".repeat(32),name:"Original GitLab",provider:"gitlab",url:"https://gitlab.invalid",version:1,credential_kind:"oauth",credential_configured:true,authorization_status:"active",validation:{status:"valid",username:"tester"}};
  let finishRead, finishSave;
  api.hook = path => {
    if (path.startsWith("/v1/platform/connections?")) return new Promise(resolve => { finishRead = resolve; });
    if (path === "/v1/platform/connections/save") return new Promise(resolve => { finishSave = resolve; });
  };
  const ui = await launch(api);
  const opening = ui.el("open-settings").emit("click");
 finishRead({connections:[item]});await opening;
 await ui.el("connections-list").children[0].emit("click");
  ui.el("connection-name").value = "Confirmed GitLab";
  ui.el("connection-url").value = "https://gitlab.invalid";
  const saving = ui.el("connection-form").emit("submit");
  assert.equal(ui.el("connection-form").hidden, false);
  assert.equal(ui.el("connections-list-view").hidden, true);
  assert.equal(ui.el("connection-fields").disabled, true);
  const dialog = ui.el("settings-dialog"), outside = { button: 0, clientX: -10, clientY: -10 };
  await dialog.emit("pointerdown", outside); await dialog.emit("click", outside);
  assert.equal(dialog.open, true);
  finishSave({ id: "a".repeat(32), name: "Confirmed GitLab", provider: "gitlab", url: "https://gitlab.invalid", version: 1, credential_kind: "oauth", credential_configured: true, validation: { status: "unverified" } });
  await saving;
  assert.equal(ui.el("connection-form").hidden, true);
  assert.equal(ui.el("connections-list-view").hidden, false);
  assert.equal(ui.el("connections-list").children.length, 1);

  assert.equal(ui.el("connections-list").children.length, 1);
  assert.equal(ui.el("connections-empty").hidden, true);
  assert.equal(ui.el("connections-list-view").attributes["aria-busy"], "false");
  assert.equal(ui.el("connections-feedback").textContent, "名称已保存。");
});

test("platform connections load on open and return, with retry only after a failed read", async () => {
  const api = server([]);
  let finish, reads = 0;
  const item = { id: "a".repeat(32), name: "Company GitLab", provider: "gitlab", url: "https://gitlab.invalid", validation: { status: "valid" } };
  api.hook = path => {
    if (path.startsWith("/v1/platform/connections?")) {
      reads++;
      return new Promise((resolve, reject) => { finish = { resolve, reject }; });
    }
  };
  const ui = await launch(api);
  assert.equal(ui.el("connections-reload"), undefined);
  const opening = ui.el("open-settings").emit("click");
  assert.equal(reads, 1);
  assert.equal(ui.el("connections-list-view").attributes["aria-busy"], "true");
  assert.equal(ui.el("connections-load-error").hidden, true);
  assert.equal(ui.el("connections-feedback").textContent, "");
  finish.resolve({ connections: [structuredClone(item)] });
  await opening;
  const row = ui.el("connections-list").children[0];
  assert.equal(ui.el("connections-list-view").attributes["aria-busy"], "false");
  assert.equal(ui.el("connections-empty").hidden, true);
  assert.equal(ui.el("connections-load-error").hidden, true);

  await ui.el("close-settings").emit("click");
  const reopening = ui.el("open-settings").emit("click");
  assert.equal(reads, 2);
  assert.equal(ui.el("connections-list").children[0], row);
  finish.reject(new Error("fixture failure"));
  await reopening;
  assert.equal(ui.el("connections-load-error").hidden, false);
  assert.equal(ui.el("connections-empty").hidden, true);
  assert.equal(ui.el("connections-retry").disabled, false);
  const failedRetry = ui.el("connections-retry").emit("click");
  assert.equal(ui.el("connections-load-error").hidden, false);
  assert.equal(ui.el("connections-retry").disabled, true);
  assert.equal(ui.el("connections-retry").textContent, "重试中…");
  await ui.el("connections-retry").emit("click");
  assert.equal(reads, 3);
  finish.reject(new Error("still offline"));
  await failedRetry;
  assert.equal(ui.el("connections-list").children[0], row);
  assert.equal(ui.el("connections-load-error").hidden, false);
  assert.equal(ui.el("connections-retry").disabled, false);
  assert.equal(ui.el("connections-retry").textContent, "重试");

  const recovered = ui.el("connections-retry").emit("click");
  finish.resolve({ connections: [] });
  await recovered;
  assert.equal(reads, 4);
  assert.equal(ui.el("connections-load-error").hidden, true);
  assert.equal(ui.el("connections-empty").hidden, false);
  assert.equal(ui.el("connections-list-view").attributes["aria-busy"], "false");
  await ui.el("connection-new").emit("click");
  const returning = ui.el("connection-back").emit("click");
  assert.equal(reads, 5);
  finish.resolve({ connections: [structuredClone(item)] });
  await returning;
  assert.equal(ui.el("connections-list").children.length, 1);
  assert.equal(ui.el("connections-load-error").hidden, true);
});




test("Workspace connection manager preserves selection, draft and project without applying", async () => {
  const api = workspaceServer(), ui = await launch(api);
  await ui.draft("draft while setting up project");
  await editWorkspace(ui, true);
  ui.el("workspace-branch").value = "feature/setup";
  await ui.el("workspace-manage-connections").emit("click");
  assert.equal(ui.el("workspace-dialog").open, false);
  assert.equal(ui.el("settings-dialog").open, true);
  await ui.el("close-settings").emit("click");
  await ui.el("open-workspace").emit("click");
  assert.equal(ui.el("workspace-repo-url").value, "https://gitlab.invalid/group/project.git");
  assert.equal(ui.el("workspace-connection").value, "a".repeat(32));
  assert.equal(ui.el("workspace-branch").value, "feature/setup");
  assert.equal(ui.el("message-input").value, "draft while setting up project");
  assert.equal(api.calls.some(call => call.path === "/v1/workspace/apply"), false);
});


function notificationServer() {
  const api = workspaceServer();
  const events = new Map(["A", "B"].map(id => [id, { available: true, subscription_version: 1,
    config: { connection_id: "a".repeat(32), project: `group/project-${id}`, enabled: true, interval_seconds: id === "A" ? 5 : 17, filter: {} }, status: "running", pending: 0, metrics: { calls: 1, response_bytes: 100 } }]));
  for (const [id, ws] of api.workspaces) {
    ws.workspace.repository = { url: `https://gitlab.invalid/group/project-${id}.git` };
    ws.workspace.binding = { provider: "gitlab", url: "https://gitlab.invalid", project: `group/project-${id}`, project_id: id === "A" ? 7 : 8, connection_id: "a".repeat(32) };
  }
  api.connections[0].validation = { status: "valid", user_id: 101, username: "alice" };
  for (const [id, value] of events) value.project = structuredClone(api.workspaces.get(id).workspace.binding);
  api.hook = (path, data) => {
    if (path.startsWith("/v1/platform/notifications?")) return notificationList(structuredClone(events.get(new URL(path, "http://fixture").searchParams.get("room_id"))));
    if (path === "/v1/platform/notifications/configure") {
      const saved = events.get(data.room_id);
      Object.assign(saved, { config: data.config, subscription_version: saved.subscription_version + 1 });
      return eventView(saved);
    }
  };
  return { api, events };
}


test("GitLab message author and keyboard avatar open notification details and close member activity reads", async () => {
  const { api } = notificationServer(), baseHook = api.hook;
  const member = { agent_id: "reviewer", membership_id: "reviewer-generation", status: "idle", runner: { profile: "runtime" } };
  api.hook = (path, data) => {
    if (path === "/v1/member/list") return { members: [member] };
    if (path === "/v1/member/activity") return { member };
    return baseHook(path, data);
  };
  api.history.set("A", [{ ...message(1, "MR opened"), author: { kind: "integration", id: "gitlab" }, source: { provider: "gitlab", channel: "events", instance: "https://gitlab.invalid", subscription_id: "a".repeat(32), item_key: "https://gitlab.invalid|7|9", project_id: 7 } }]);
  const ui = await launch(api), msg = ui.document.getElementById("message-1");
  await ui.el("members").children[0].querySelector(".member-summary").emit("click"); await settle();
  assert.equal(ui.el("activity-dialog").open, true);
  await msg.querySelector(".author").emit("click"); await settle();
  assert.equal(ui.el("activity-dialog").open, false); assert.equal(ui.el("notification-detail").open, true);
  const reads = api.calls.filter(c => c.path === "/v1/member/activity").length;
  await ui.refresh(); assert.equal(api.calls.filter(c => c.path === "/v1/member/activity").length, reads);
  await ui.el("close-notification").emit("click");
  await ui.el("toggle-members").emit("click");
  await msg.querySelector(".avatar").emit("keydown", { key: "Enter" });
  assert.equal(ui.el("notification-detail").open, true); assert.equal(ui.el("member-rail").inert, false);
  await ui.el("close-notification").emit("click");
  await msg.querySelector(".avatar").emit("keydown", { key: " " });
  assert.equal(ui.el("notification-detail").open, true);
  await ui.el("close-notification").emit("click");
  await ui.el("members").children[0].querySelector(".member-summary").emit("click"); await settle();
  assert.equal(ui.el("notification-detail").open, false); assert.equal(ui.el("activity-dialog").open, true);
  assert.equal(String(ui.el("member-count").textContent), "1");
});



function todosServer({ bound = true, enabled = false } = {}) {
  const { api, events } = notificationServer();
  api.connections[0].validation = { status: "valid", user_id: 101, username: "alice" };
  api.connections.push({ ...structuredClone(api.connections[0]), id: "b".repeat(32), name: "Personal", validation: { status: "valid", user_id: 202, username: "bob" } });
  const todos = new Map(["A", "B"].map(id => [id, todoView({ subscription_id: enabled ? (id === "A" ? "c" : "d").repeat(32) : "", subscription_version: enabled ? 3 : 0,
    recipient_id: enabled ? 101 : undefined, username: enabled ? "alice" : undefined, instance: enabled ? "https://gitlab.invalid" : undefined,
    config: { enabled, connection_id: enabled ? api.connections[0].id : "", interval_seconds: id === "A" ? 30 : 45, filter: {} }, status: enabled ? "running" : "disabled" })]));
  if (!bound) for (const ws of api.workspaces.values()) { delete ws.workspace.binding; delete ws.workspace.repository; }
  const sourceHook = api.hook, records = new Map();
  api.gitlabApplyFailure = new Set();
  const viewFor = id => {
    const sources = [eventView(events.get(id)), structuredClone(todos.get(id))];
    return fixtureGitLabView(sources, records.get(id));
  };
  api.hook = (path, data) => {
    if (path === "/v1/platform/gitlab/projects") return api.projectPage ? api.projectPage(data) : {projects:["group/project-A","group/changed","other/project"].map((path,index)=>({id:index+1,name:path.split("/").at(-1),path_with_namespace:path})),next_page:0};
    if (path.startsWith("/v1/platform/notifications?")) {
      const id = new URL(path, "http://fixture").searchParams.get("room_id");
      return { subscriptions: [eventView(events.get(id)), structuredClone(todos.get(id))], gitlab: viewFor(id) };
    }
    if (path === "/v1/platform/gitlab/notifications/configure") {
      const previous = viewFor(data.room_id);
      if (data.version !== previous.version) throw Object.assign(new Error("conflict"), { code: "conflict" });
      const config = structuredClone(data.config);
      const backfillLocked = previous.backfill_locked || config.events.enabled || config.todos.enabled;
      if (!previous.backfill_locked && (config.events.enabled || config.todos.enabled)) {
        config.backfill_start ||= new Date(Math.floor((Date.now()-(config.backfill_seconds || 0)*1000)/1000)*1000).toISOString();
        config.backfill_seconds = 0;
      }
      const saved = {backfill_locked:backfillLocked,  version: previous.version + (JSON.stringify(config) !== JSON.stringify(previous.config) || !previous.version ? 1 : 0), config, pending: {}, errors: {} };
      records.set(data.room_id, saved);
      for (const channel of ["events", "todos"]) {
        const runtime = channel === "events" ? events.get(data.room_id) : todos.get(data.room_id);
        const desired = { ...config[channel], connection_id: config.connection_id, target: config.target, interval_seconds: config.interval_seconds, backfill_seconds:config.backfill_seconds || 0, ...(config.backfill_start ? {backfill_start:config.backfill_start} : {}) };
        const account = api.connections.find(c => c.id === config.connection_id);
        const changed = JSON.stringify(fixtureSourceConfig(runtime.config, channel)) !== JSON.stringify(fixtureSourceConfig(desired, channel)) || runtime.config.interval_seconds !== desired.interval_seconds || runtime.config.connection_id !== desired.connection_id || JSON.stringify(runtime.config.target || {}) !== JSON.stringify(desired.target) || runtime.recipient_id !== account?.validation.user_id;
        if (!desired.enabled && !runtime.config.enabled || !changed) continue;
        if (api.gitlabApplyFailure.has(channel)) { saved.pending[channel] = true; saved.errors[channel] = "apply_failed"; continue; }
        runtime.config = desired; runtime.subscription_version++; runtime.status = desired.enabled ? "running" : "disabled";
        runtime.recipient_id = account?.validation.user_id; runtime.username = account?.validation.username; runtime.instance = account?.url;
        runtime.subscription_id ||= (channel === "events" ? "a" : "c").repeat(32);
      }
      return viewFor(data.room_id);
    }
    if (path === "/v1/platform/gitlab/notifications/sync") {
      const view = viewFor(data.room_id);
      assert.equal(data.version, view.version); assert.equal(data.config, undefined);
      return view;
    }
    return sourceHook(path, data);
  };
  api.gitlabRecords = records;
  return { api, events, todos };
}

test("GT-03 message navigation requires explicit channels and keyboard tabs preserve selection", async () => {
  const { api } = todosServer(); api.history.set("A", ["events", "todos"].map((channel, index) => ({ ...message(index + 1, "integration fact"), author: { kind: "integration", id: "gitlab" }, source: { provider: "gitlab", channel } })));
  const ui = await launch(api);
  for (let i = 1; i <= 2; i++) {
    const article = ui.document.getElementById(`message-${i}`), channel = i === 2 ? "todos" : "events";
    assert.equal(article.querySelector(".author").textContent, `GitLab · ${i === 2 ? "我的待办" : "项目活动"}`);
    await article.querySelector(".author").emit("click"); assert.equal(ui.el("notification-tab-gitlab").attributes["aria-selected"], "true"); assert.equal(ui.el(`gitlab-${channel}-settings`).open, true);
    await ui.el("close-notification").emit("click"); await article.querySelector(".avatar").emit("keydown", { key: "Enter" });
    assert.equal(ui.el("notification-panel-gitlab").hidden, false); assert.equal(ui.el(`gitlab-${channel}-settings`).open, true);
  }
  await ui.el("notification-tab-messages").emit("keydown", { key: "ArrowLeft" }); assert.equal(ui.el("notification-tab-gitlab").attributes["tabindex"], "0");
  await ui.el("notification-tab-gitlab").emit("keydown", { key: "End" }); assert.equal(ui.el("notification-tab-messages").attributes["aria-selected"], "true");
});





test("notifications with missing or unknown channels never open the Events source", async () => {
  const { api } = todosServer();
  api.history.set("A", [undefined, "unknown"].map((channel, index) => ({ ...message(index+1, "unsupported source"), author: { kind:"integration", id:"gitlab" }, source: { provider:"gitlab", channel } })));
  const ui = await launch(api);
  for (const seq of [1, 2]) {
    const row = ui.document.getElementById(`message-${seq}`);
    assert.equal(row.querySelector(".author").textContent, "gitlab · 通知");
    await row.querySelector(".author").emit("click");
    await row.querySelector(".avatar").emit("click");
    assert.equal(ui.el("notification-detail").open, false);
  }
});

test("private activity rejects unordered diagnostics instead of displaying legacy summaries", async () => {
  const api = server([makeRoom("A")]);
  const member = { agent_id:"developer", membership_id:"generation-1", status:"idle", read_through:0, unread_count:0, runner:{profile:"runtime"}, execution:{active:false, agent:{turn_number:1}} };
  let result = { text:"old private summary", thought:"old thought", tools:[] };
  api.hook = path => path === "/v1/member/list" ? {members:[member]}
    : path === "/v1/member/activity" ? structuredClone({member,last_output:{result}}) : undefined;
  const ui = await launch(api);
  await ui.el("members").children[0].querySelector(".member-summary").emit("click"); await settle();
  assert.equal(ui.el("activity-output").hidden, true);
  assert.match(ui.el("activity-notice").textContent, /格式无效/);
  result = {events:[],stopReason:"cancelled"};
  await ui.refresh(); await settle();
  assert.equal(ui.el("activity-output").hidden, false);
  assert.match(ui.el("activity-timeline-empty").textContent, /没有活动记录/);
});






test("Enter sends once while composing, modified Enter and held keys never submit messages", async () => {
  const api = server([makeRoom("A")]), ui = await launch(api), input = ui.el("message-input");
  const sends = () => api.calls.filter(call => call.path === "/v1/send");
  await ui.draft("中文输入");
  for (const keys of [{ isComposing: true }, { keyCode: 229 }, { shiftKey: true }, { altKey: true }, { repeat: true }]) {
    await input.emit("keydown", { key: "Enter", ...keys }); await settle();
    assert.equal(sends().length, 0);
  }
  await input.emit("keydown", { key: "Enter" }); await settle();
  assert.equal(sends().length, 1); assert.equal(sends()[0].data.text, "中文输入");
  assert.equal(input.value, "");
  await ui.draft("下一条草稿");
  await input.emit("keydown", { key: "Enter", repeat: true }); await settle();
  assert.equal(sends().length, 1); assert.equal(input.value, "下一条草稿");
  await ui.draft("   ");
  await input.emit("keydown", { key: "Enter" }); await settle();assert.equal(sends().length, 1);
});

test("Ctrl/Command Enter inserts a newline at the selection, retains drafts and leaves pending messages untouched", async () => {
  const api = server([makeRoom("A")]), ui = await launch(api), input = ui.el("message-input");
  for (const keys of [{ctrlKey:true}, {metaKey:true}]) {
    await ui.draft("前文选中后文"); input.selectionStart = 2; input.selectionEnd = 4;
    let prevented = false;
    await input.emit("keydown", {key:"Enter", ...keys, preventDefault(){prevented=true;}});
    assert.equal(prevented, true); assert.equal(input.value, "前文\n后文"); assert.equal(input.selectionStart, 3);
    assert.equal(api.calls.filter(call => call.path === "/v1/send").length, 0);
    const reload = await launch(api, ui.saved);assert.equal(reload.el("message-input").value,"前文\n后文");
  }
  let shiftPrevented = false;
  await input.emit("keydown", {key:"Enter",shiftKey:true,preventDefault(){shiftPrevented=true;}});
  assert.equal(shiftPrevented, false); // Keep the textarea's native Shift+Enter editing behavior.
  let finishSend;
  api.hook = path => path === "/v1/send" ? new Promise(resolve => {finishSend=resolve;}) : undefined;
  await input.emit("keydown", {key:"Enter"});await settle();assert.equal(input.readOnly,true);
  const text = input.value;
  await input.emit("keydown", {key:"Enter",ctrlKey:true});assert.equal(input.value,text);
  await input.emit("keydown", {key:"Enter"});await settle();
  assert.equal(api.calls.filter(call => call.path === "/v1/send").length, 1);
  finishSend({seq:2});await settle();
});

test("two tabs keep separate selections, drafts, pending sends and member activity across polling and reload", async () => {
  const api = server([makeRoom("A"), makeRoom("B")]);
  const a = await launch(api), b = await launch(api);
  await a.draft("draft-A");
  await b.select("B"); await b.draft("draft-B");
  await Promise.all([a.refresh(), b.refresh()]);
  assert.equal(a.el("room-name").textContent, "A");
  assert.equal(b.el("room-name").textContent, "B");
  api.hook = (path, data) => path === "/v1/send" && data.room_id === "A" ? Promise.reject(new Error("lost A receipt")) : undefined;
  await a.el("compose").emit("submit");
  assert.equal(a.el("message-input").readOnly, true);
  assert.equal(b.el("message-input").readOnly, false);
  await b.refresh(); assert.equal(b.el("message-input").value, "draft-B");
  const reloadA = await launch(api, a.saved), reloadB = await launch(api, b.saved);
  assert.equal(reloadA.el("room-name").textContent, "A");
  assert.equal(reloadA.el("message-input").value, "draft-A");
  assert.equal(reloadB.el("room-name").textContent, "B");
  assert.equal(reloadB.el("message-input").value, "draft-B");
  assert.equal(api.calls.filter(call => call.path === "/v1/rooms/open").length, 0);
  assert.ok(api.calls.filter(call => call.path === "/v1/member/list").some(call => call.data.room_id === "A"));
  assert.ok(api.calls.filter(call => call.path === "/v1/member/list").some(call => call.data.room_id === "B"));
});

test("a slow open receipt cannot replace a newer tab selection; reload reconciles without another POST", async () => {
  const api = server([makeRoom("A"), makeRoom("B"), makeRoom("C")]);
  const roomB = api.catalog.rooms[1]; delete roomB.service; roomB.runtime_status = "closed";
  api.hook = (path, data) => {
    if (path !== "/v1/rooms/open") return;
    const receipt = { ...data, status: "running" };
    api.opens.set(data.open_id, receipt); roomB.busy = true; roomB.runtime_status = "opening";
    return structuredClone(receipt);
  };
  const ui = await launch(api); await ui.select("B");
  await ui.select("C"); await ui.draft("C retained");
  const [receipt] = api.opens.values(); receipt.status = "completed"; receipt.service = identity(roomB);
  roomB.service = receipt.service; roomB.busy = false; roomB.runtime_status = "ready";
  await ui.refresh();
  assert.equal(ui.el("room-name").textContent, "C");
  assert.equal(ui.el("message-input").value, "C retained");
  const reload = await launch(api, ui.saved);
  assert.equal(reload.el("room-name").textContent, "C");
  assert.equal(api.calls.filter(call => call.path === "/v1/rooms/open").length, 1);
});

test("another room's lifecycle operation and runtime failure leave this tab usable", async () => {
  const api = server([makeRoom("A"), makeRoom("B")]);
  const a = await launch(api), b = await launch(api); await b.select("B");
  api.catalog.rooms[0].busy = true; api.catalog.rooms[0].runtime_status = "opening";
  await Promise.all([a.refresh(), b.refresh()]); await b.draft("B can send");
  assert.equal(a.el("send").disabled, true);
  assert.equal(b.el("send").disabled, false);
  await a.select("B"); assert.equal(a.el("room-name").textContent, "B");
  api.catalog.rooms[0].busy = false; api.catalog.rooms[0].runtime_status = "failed";
  api.catalog.rooms[0].runtime_error = "A cleanup incomplete"; delete api.catalog.rooms[0].service;
  await b.refresh();
  assert.equal(b.el("send").disabled, false);
  assert.match(b.el("room-list").children.find(row => row.children[0].children[0].textContent === "A").children[0].children[1].textContent, /A cleanup incomplete/);
});

test("duplicate open clicks still enter the completed room, and a reload preserves a newer selection", async () => {
  const api = server([makeRoom("A"), makeRoom("B")]);
  const room = api.catalog.rooms[1]; delete room.service; room.runtime_status = "closed";
  api.hook = (path,data) => {
    if (path !== "/v1/rooms/open") return;
    const receipt = { ...data, status: "running" }; api.opens.set(data.open_id, receipt);
    return structuredClone(receipt);
  };
  const ui = await launch(api); await ui.select("B"); await ui.select("B");
  const [receipt] = api.opens.values(); receipt.status = "completed"; receipt.service = identity(room);
  room.service = receipt.service; room.runtime_status = "ready";
  await ui.refresh(); assert.equal(ui.el("room-name").textContent,"B");
  assert.equal(api.calls.filter(call => call.path === "/v1/rooms/open").length,1);

  const pendingApi = server([makeRoom("A"),makeRoom("B"),makeRoom("C")]);
  const pendingRoom = pendingApi.catalog.rooms[1]; delete pendingRoom.service; pendingRoom.runtime_status = "closed";
  pendingApi.hook = (path,data) => {
    if (path !== "/v1/rooms/open") return;
    const receipt = { ...data, status:"running" }; pendingApi.opens.set(data.open_id,receipt); return receipt;
  };
  const pendingUI = await launch(pendingApi); await pendingUI.select("B"); await pendingUI.select("C");
  const reload = await launch(pendingApi,pendingUI.saved);
  const [pending] = pendingApi.opens.values(); pending.status="completed"; pending.service=identity(pendingRoom);
  pendingRoom.service=pending.service; pendingRoom.runtime_status="ready";
  await reload.refresh(); assert.equal(reload.el("room-name").textContent,"C");
});

test("duplicate create submissions retain one ID while an unrelated ready room stays selectable", async () => {
  const api=server([makeRoom("A"),makeRoom("C")]), ui=await launch(api);
  let finish;
  api.hook=(path,data)=>path==="/v1/rooms/create" ? new Promise(resolve=>{ finish=()=>{
    const room={...makeRoom("B"),create_id:data.create_id};api.catalog.rooms.push(room);api.history.set("B",[]);resolve(room);
  }; }) : undefined;
  await ui.create("B"); await ui.el("create-form").emit("submit");
  assert.equal(api.calls.filter(call=>call.path==="/v1/rooms/create").length,1);
  await ui.select("C"); await ui.draft("C retained through create");
  finish(); await settle();
  assert.equal(ui.el("room-name").textContent,"C");
  assert.equal(ui.el("message-input").value,"C retained through create");
  assert.equal(api.catalog.rooms.filter(room=>room.name==="B").length,1);
});


test("AN-01.2 Inbox snapshot paging never confirms, and late reads cannot cross rooms", async () => {
  const api=server([makeRoom("A"),makeRoom("B")]);
  const member={agent_id:"assistant",membership_id:"1".repeat(32),status:"idle",runner:{profile:"runtime"},execution:{paused:true,agent:{turn_number:0}}};
  let finish;
  const page=(after)=>({snapshot_seq:2,next_after:after+1,has_more:after===0,status:{latest_seq:2,bytes:100,limits:{bytes:1000}},entries:[{seq:after+1,received_at:"2026-11-04T01:00:00Z",item:{provider:"gitlab",channel:"todos",instance:"https://gitlab.invalid",project_id:312,text:`notice-${after+1}`}}]});
  api.hook=(path,data)=>{
    if(path==="/v1/member/list")return {members:[member]};
    if(path==="/v1/member/activity")return {member};
    if(path==="/v1/member/inbox")return data.after===1 ? new Promise(resolve=>finish=()=>resolve(page(1))) : page(0);
  };
  const ui=await launch(api);
  await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();
  await ui.el("agent-tab-inbox").emit("click");await settle();
  assert.equal(ui.el("inbox-member-view").hidden,false);
  assert.equal(ui.el("inbox-entries").children[0].children[1].textContent,"notice-1");
  const imagePath = "/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/image.png";
  const imageURL = ui.el("inbox-entries").children[0].children[1].markdownOptions.attachmentURL(imagePath);
  const imageQuery = new URL(imageURL, "http://fixture").searchParams;
  assert.equal(imageQuery.get("agent_id"), member.agent_id);
  assert.equal(imageQuery.get("membership_id"), member.membership_id);
  assert.equal(imageQuery.get("seq"), "1");
  assert.equal(imageQuery.get("room_id"), "A");
  assert.equal(imageQuery.get("service_epoch"), "epoch-A");
  const pending=ui.el("inbox-next").emit("click");await settle();
  const calls=api.calls.filter(call=>call.path==="/v1/member/inbox");
  assert.equal(calls[1].data.snapshot_seq,2);assert.equal(calls[1].data.after,1);
  await ui.select("B");finish();await pending;await settle();
  assert.equal(ui.el("inbox-entries").children.length,0);
  assert.equal(api.calls.some(call=>/confirm|member\/run/.test(call.path)),false);
});

test("Inbox navigation keeps cursor history and snapshot, synchronizes both pagers, and refresh starts over", async () => {
  const api=server([makeRoom("A")]);
  const member={agent_id:"assistant",membership_id:"1".repeat(32),status:"idle",runner:{profile:"runtime"},execution:{paused:true,agent:{turn_number:0}}};
  let latest=55;
  api.hook=(path,data)=>{
    if(path==="/v1/member/list")return {members:[member]};
    if(path==="/v1/member/activity")return {member};
    if(path==="/v1/member/inbox") {
      // Byte limits can produce shorter pages; use returned cursors, not page size arithmetic.
      const after=data.after, end=after===0 ? 3 : after===3 ? 9 : 12;
      return {snapshot_seq:data.snapshot_seq ?? latest,next_after:end,has_more:end<12,status:{latest_seq:latest,bytes:100,limits:{bytes:1000}},entries:Array.from({length:end-after},(_,i)=>({seq:after+i+1,item:{provider:"gitlab",channel:"todos",text:`notice-${after+i+1}`}}))};
    }
  };
  const ui=await launch(api);
  await ui.el("members").children[0].querySelector(".member-summary").emit("click");
  await ui.el("agent-tab-inbox").emit("click");await settle();
  assert.equal(ui.el("inbox-prev").disabled,true);
  assert.equal(ui.el("inbox-page").textContent,"第 1 页");
  ui.el("inbox-member-view").scrollTop=500;
  await ui.el("inbox-next-bottom").emit("click");await settle();
  assert.equal(ui.el("inbox-member-view").scrollTop,0);
  assert.equal(ui.el("inbox-page").textContent,"第 2 页");
  assert.equal(ui.el("inbox-page-bottom").textContent,ui.el("inbox-page").textContent);
  latest=60;
  await ui.el("inbox-next").emit("click");await settle();
  assert.equal(ui.el("inbox-next").disabled,true);
  assert.equal(ui.el("inbox-next-bottom").disabled,true);
  await ui.el("inbox-prev-bottom").emit("click");await settle();
  assert.equal(ui.el("inbox-page").textContent,"第 2 页");
  await ui.el("inbox-prev").emit("click");await settle();
  assert.equal(ui.el("inbox-prev-bottom").disabled,true);
  await ui.el("inbox-next").emit("click");await settle();
  const reads=()=>api.calls.filter(call=>call.path==="/v1/member/inbox").map(call=>call.data);
  assert.deepEqual(reads().map(data=>data.after),[0,3,9,3,0,3]);
  assert.ok(reads().slice(1).every(data=>data.snapshot_seq===55));
  await ui.el("inbox-reload").emit("click");await settle();
  assert.equal(reads().at(-1).after,0);
  assert.equal(reads().at(-1).snapshot_seq,undefined);
  assert.equal(ui.el("inbox-page").textContent,"第 1 页");
  assert.equal(api.calls.some(call=>/confirm|member\/run/.test(call.path)),false);
});

test("Inbox loading blocks duplicate navigation and failures preserve the page and retry cursor", async () => {
  const api=server([makeRoom("A")]);
  const member={agent_id:"assistant",membership_id:"1".repeat(32),status:"idle",runner:{profile:"runtime"},execution:{paused:true,agent:{turn_number:0}}};
  let rejectPage, fail=true;
  api.hook=(path,data)=>{
    if(path==="/v1/member/list")return {members:[member]};
    if(path==="/v1/member/activity")return {member};
    if(path==="/v1/member/inbox") {
      if(data.after && fail)return new Promise((resolve,reject)=>rejectPage=reject);
      return {snapshot_seq:2,next_after:data.after+1,has_more:!data.after,status:{latest_seq:2,bytes:100,limits:{bytes:1000}},entries:[{seq:data.after+1,item:{provider:"gitlab",channel:"todos",text:`notice-${data.after+1}`}}]};
    }
  };
  const ui=await launch(api);
  await ui.el("members").children[0].querySelector(".member-summary").emit("click");
  await ui.el("agent-tab-inbox").emit("click");await settle();
  const pending=ui.el("inbox-next").emit("click");await settle();
  for(const id of ["inbox-reload","inbox-prev","inbox-next","inbox-prev-bottom","inbox-next-bottom"])assert.equal(ui.el(id).disabled,true);
  await ui.el("inbox-next-bottom").emit("click");await settle();
  assert.equal(api.calls.filter(call=>call.path==="/v1/member/inbox").length,2);
  rejectPage(new Error("temporarily offline"));await pending;await settle();
  assert.equal(ui.el("inbox-page").textContent,"第 1 页");
  assert.match(ui.el("inbox-status").textContent,/当前页已保留/);
  assert.equal(ui.el("inbox-entries").children[0].children[1].textContent,"notice-1");
  assert.equal(ui.el("inbox-next").disabled,false);
  fail=false;
  await ui.el("inbox-next-bottom").emit("click");await settle();
  assert.equal(ui.el("inbox-page").textContent,"第 2 页");
  assert.equal(ui.el("inbox-entries").attributes["aria-busy"],"false");
});

test("Inbox empty state and initial read failure allow refresh without enabling pagination", async () => {
  const api=server([makeRoom("A")]);
  const member={agent_id:"assistant",membership_id:"1".repeat(32),status:"idle",runner:{profile:"runtime"},execution:{paused:true,agent:{turn_number:0}}};
  let fail=true;
  api.hook=(path)=>{
    if(path==="/v1/member/list")return {members:[member]};
    if(path==="/v1/member/activity")return {member};
    if(path==="/v1/member/inbox") {
      if(fail)throw new Error("temporarily offline");
      return {snapshot_seq:0,next_after:0,has_more:false,status:{latest_seq:0,bytes:0,limits:{bytes:1000}},entries:[]};
    }
  };
  const ui=await launch(api);
  await ui.el("members").children[0].querySelector(".member-summary").emit("click");
  await ui.el("agent-tab-inbox").emit("click");await settle();
  assert.match(ui.el("inbox-status").textContent,/请点击刷新重试/);
  assert.equal(ui.el("inbox-reload").disabled,false);
  assert.equal(ui.el("inbox-next").disabled,true);
  fail=false;
  await ui.el("inbox-reload").emit("click");await settle();
  assert.equal(ui.el("inbox-page").textContent,"暂无通知");
  assert.equal(ui.el("inbox-prev").disabled,true);
  assert.equal(ui.el("inbox-next").disabled,true);
  assert.equal(ui.el("inbox-pagination-bottom").hidden,true);
});

test("Lark Inbox separates channel, sender and conversation while retaining source evidence and attachment identity", async () => {
  const api=server([makeRoom("A")]);
  const member={agent_id:"assistant",membership_id:"1".repeat(32),status:"idle",runner:{profile:"runtime"},execution:{paused:true,agent:{turn_number:0}}};
  const common={provider:"lark",channel:"messages",instance:"https://open.feishu.cn",message_id:{namespace:"lark:message_id:cli_fixture",value:"om_saved"},receiver:{id:{namespace:"lark:open_id:cli_fixture",value:"ou_self"}},structured:true};
  const actor=(name,id,is_receiver=false)=>({name,id:{namespace:"lark:open_id:cli_fixture",value:id},is_receiver});
  const chat=(name,type="p2p")=>({name,type,id:{namespace:"lark:chat_id:cli_fixture",value:"oc_saved"}});
  const items=[
    {...common,sender:actor("","ou_self",true),conversation:chat(""),text:"好的"},
    {...common,sender:actor("张三","ou_peer"),conversation:chat(""),text:"![图片](lark-image:img_fixture)\n发送者：这是正文，必须保留"},
    {...common,sender:actor("","ou_123456789"),conversation:chat("研发群","group"),text:"收到"},
    {...common,structured:false,text:"飞书 · 旧消息\n原始正文，没有发送者元信息"},
    {...common,sender:actor("本人姓名","ou_self",true),text:"新消息"},
    {...common,sender:actor("张三","ou_peer"),changed:true,text:"更正后的正文"},
    {...common,sender:actor("雷九零","ou_peer"),conversation:chat("迭代v0.10.0","topic"),mentions:[actor("罗彭","ou_self",true)],text:"@罗彭（open_id: ou_self；当前接收账号） 请确认"},
    {...common,sender:actor("本人姓名","ou_self",true),context_only:true,text:"已代发"},
  ];
  api.hook=(path)=>{
    if(path==="/v1/member/list")return {members:[member]};
    if(path==="/v1/member/activity")return {member};
    if(path==="/v1/member/inbox")return {snapshot_seq:items.length,next_after:items.length,has_more:false,status:{latest_seq:items.length,bytes:100,limits:{bytes:1000}},entries:items.map((item,i)=>({seq:i+1,origin:i===7?{kind:"agent",agent_id:"assistant",status:"succeeded"}:undefined,source:{source_ref:`src_${i}`,status:"not_checked"},received_at:"2026-11-06T08:06:57Z",item:{...item,created_at:"2026-11-06T07:00:00Z"}}))};
  };
  const ui=await launch(api);
  await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();
  await ui.el("agent-tab-inbox").emit("click");await settle();
  const rows=ui.el("inbox-entries").children;
  const part=(row,name)=>row.className===name ? row : row.children.map(child=>part(child,name)).find(Boolean);
  const heading=part(rows[0],"inbox-entry-header");
  assert.equal(heading.children[0].textContent,"#1");
  assert.equal(heading.children[1].textContent,"飞书消息");
  assert.equal(heading.children[2].dateTime,"2026-11-06T07:00:00.000Z");
  assert.equal(part(rows[0],"inbox-sender").textContent,"我");
  assert.equal(part(rows[0],"inbox-chat"),undefined);
  assert.equal(part(rows[0],"message-text").textContent,"好的");
  const details=part(rows[0],"inbox-source-details");
  assert.equal(details.tag,"details");assert.equal(details.open,false);
  assert.equal(details.children[0].textContent,"来源详情");
  assert.match(elementText(details.children[1]),/ou_self/);assert.match(elementText(details.children[1]),/oc_saved/);
  assert.equal(part(rows[1],"inbox-sender").textContent,"张三");
  assert.equal(part(rows[1],"inbox-chat"),undefined);
  assert.equal(part(rows[1],"message-text").textContent,items[1].text);
  const attachment=part(rows[1],"message-text").markdownOptions.attachmentURL("lark-image:img_fixture");
  const query=new URL(attachment,"http://fixture").searchParams;
  assert.equal(query.get("seq"),"2");assert.equal(query.get("membership_id"),member.membership_id);
  assert.equal(part(rows[2],"inbox-sender").textContent,"飞书用户 · …456789");
  assert.equal(part(rows[2],"inbox-chat").textContent,"研发群");
  assert.deepEqual(part(rows[2],"inbox-message-heading").children.map(child=>child.className),["inbox-sender","inbox-chat"]);
  assert.match(elementText(part(rows[2],"inbox-source-details")),/oc_saved/);
  assert.equal(part(rows[3],"message-text").textContent,items[3].text);
  assert.equal(part(rows[3],"inbox-sender"),undefined);
  assert.equal(part(rows[4],"inbox-sender").textContent,"本人姓名（我）");
  assert.equal(part(rows[5],"inbox-sender").textContent,"张三");
  assert.match(part(rows[5],"inbox-change quiet").textContent,/不自动重做已完成动作/);
  assert.equal(part(rows[5],"message-text").textContent,"更正后的正文");
  assert.equal(part(rows[6],"inbox-chat").textContent,"迭代v0.10.0");
  assert.equal(part(rows[6],"message-text").markdownOptions.compactLarkMentions,true);
  assert.equal(part(rows[6],"message-text").textContent,items[6].text);
  assert.match(elementText(part(rows[6],"inbox-source-details")),/罗彭（我）/);
  assert.equal(part(rows[7],"inbox-sender").textContent,"本人姓名（我） · Agent 代发");
  assert.match(elementText(part(rows[7],"inbox-source-details")),/进展上下文，不触发新的自动回复或待办/);
  assert.equal(api.calls.some(call=>/confirm|member\/run/.test(call.path)),false);
});

test("Room attachment rendering binds the saved integration message and excludes ordinary messages", async () => {
  const api = server([makeRoom("A")]);
  const source = { provider:"gitlab", channel:"events", instance:"https://gitlab.invalid", project_id:312 };
  api.history.set("A", [
    {...message(1, "图片评论"), author:{kind:"integration",id:"gitlab"}, source},
    {...message(2, "普通消息"), source},
  ]);
  const ui = await launch(api);
  const options = ui.el("messages").children.find(el => el.id === "message-1").querySelector(".message-text").markdownOptions;
  const query = new URL(options.attachmentURL("/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/image.png"), "http://fixture").searchParams;
  assert.equal(query.get("seq"), "1");
  assert.equal(query.get("room_id"), "A");
  assert.equal(query.get("room_instance"), "instance-A");
  assert.equal(query.get("service_epoch"), "epoch-A");
  assert.equal(query.has("membership_id"), false);
  assert.equal(ui.el("messages").children.find(el => el.id === "message-2").querySelector(".message-text").markdownOptions, undefined);
});

test("Lark Inbox preview binds image requests to the saved membership and sequence", async () => {
  const api = server([makeRoom("A")]);
  const member = {agent_id:"assistant",membership_id:"1".repeat(32),status:"idle",runner:{profile:"runtime"},execution:{paused:true,agent:{turn_number:0}}};
  const source = {provider:"lark",channel:"messages",reference:{version:2,target:{type:"message"}}};
  api.hook = (path) => {
    if (path === "/v1/member/list") return {members:[member]};
    if (path === "/v1/member/activity") return {member};
    if (path === "/v1/member/inbox") return {snapshot_seq:9,next_after:9,has_more:false,status:{latest_seq:9,bytes:100,limits:{bytes:1000}},entries:[{seq:9,item:{provider:"lark",channel:"messages",message_id:{value:"om_fixture"},text:"image_key: img_fixture\n请分析图片"}}]};
  };
  const ui = await launch(api);
  await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();
  await ui.el("agent-tab-inbox").emit("click");await settle();
  const options = ui.el("inbox-entries").children[0].children[1].markdownOptions;
  const query = new URL(options.attachmentURL("lark-image:img_fixture"),"http://fixture").searchParams;
  assert.equal(query.get("seq"),"9");
  assert.equal(query.get("agent_id"),member.agent_id);
  assert.equal(query.get("membership_id"),member.membership_id);
  assert.equal(query.get("service_epoch"),"epoch-A");
  assert.equal(api.calls.some(call => /confirm|member\/run/.test(call.path)),false);
});

test("natural-language policy settings distinguish suggestions and never run members", async()=> {
 const api=server([makeRoom("A")]);
 const member={agent_id:"assistant",membership_id:"1".repeat(32),status:"idle",runner:{profile:"runtime"}};
 let history=[],revision=0;
 api.hook=(path,data)=>{
  if(path==="/v1/member/list")return {members:[member]};
  if(path==="/v1/member/activity")return {member};
  if(path==="/v1/policy/read") {const current=[...new Map(history.map(r=>[r.id,r])).values()];return {revision,history:structuredClone(history),current:structuredClone(current),effective:current.filter(r=>r.status==="active")};}
  if(path==="/v1/matters/list")return {items:[],total:0,complete:true};
  if(path==="/v1/policy/update"){
   assert.equal(data.caller.kind,"human");assert.equal(data.membership_id,member.membership_id);assert.equal(data.policy.revision,revision);assert.equal(data.policy.conversation,undefined);
   history.push({...structuredClone(data.policy),conversation:{mode:"explicit_long_term",basis:"真实对话来源"},version:data.policy.version+1,source:{user:"tester",text:data.policy.source_text},changed_by:{kind:"human",id:"tester"},updated_at:"2026-11-04T01:00:00Z"});revision++;return {};
  }
 };
 const ui=await launch(api);await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();
 await ui.el("agent-tab-policy").emit("click");await settle();
 assert.equal(ui.el("policy-member-view").hidden,false);
 assert.equal(ui.el("policy-empty").hidden,false);
 await ui.el("policy-new").emit("click");assert.equal(ui.el("policy-empty").hidden,true);
 await ui.el("policy-cancel").emit("click");assert.equal(ui.el("policy-empty").hidden,false);
 await ui.el("policy-new").emit("click");ui.el("policy-text").value="简短中文回复";
 await ui.el("policy-suggest").emit("click");await settle();
 assert.equal(ui.el("policy-empty").hidden,true);
 assert.match(elementText(ui.el("policy-entries").children[0].children[0]),/建议.*未生效/);
 await ui.el("policy-entries").children[0].children[0].children[1].children[0].emit("click");await ui.el("policy-form").emit("submit");await settle();
 assert.match(elementText(ui.el("policy-entries").children[0].children[0]),/有效/);
 const change=api.calls.filter(c=>c.path==="/v1/policy/update").at(-1).data.policy;
 assert.equal(change.version,1);for(const key of ["kind","scope","scope_from","actions","conditions","exceptions"])assert.equal(Object.hasOwn(change,key),false);
 await ui.el("policy-entries").children[0].children[0].children[1].children[1].emit("click");await settle();
 assert.match(elementText(ui.el("policy-entries").children[0].children[0]),/已撤销/);
 assert.equal(history.length,3);assert.equal(api.calls.some(c=>/member\/run|inbox\/confirm/.test(c.path)),false);
});

test("long-term rule editing preserves expiry and separates source from interpretation",async()=>{
 const api=server([makeRoom("A")]);
 const member={agent_id:"assistant",membership_id:"1".repeat(32),status:"idle",runner:{profile:"runtime"}};
 const record={id:"contacts",version:1,status:"active",text:"AgenticHub 的这类报错，优先建议找 hongwei 和 luojun",expires_at:"2099-01-01T00:00:00Z",source:{user:"tester",seq:18,text:"记住这类报错优先找 hongwei 和 luojun"},conversation:{basis:"上下文明确指向 AgenticHub 报错处理约定"},changed_by:{kind:"agent",id:"assistant"},updated_at:"2026-11-06T01:00:00Z"};
 api.hook=(path,data)=>{
  if(path==="/v1/member/list")return {members:[member]};
  if(path==="/v1/member/activity")return {member};
  if(path==="/v1/policy/read")return {revision:1,history:[record],current:[record],effective:[record]};
  if(path==="/v1/policy/update")return {};
 };
 const ui=await launch(api);await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();await ui.el("agent-tab-policy").emit("click");await settle();
 const card=ui.el("policy-entries").children[0];
 assert.match(elementText(card),/有效期至/);
 assert.match(elementText(card.children[3]),/Room 消息 #18.*来源用户 tester/s);
 assert.match(elementText(card.children[3]),/原始来源.*记住这类报错.*Agent 理解：上下文明确/s);
 await card.children[0].children[1].children[0].emit("click");
 assert.equal(ui.el("policy-editor-title").textContent,"修改长期规则");
 assert.equal(ui.el("policy-text").value,record.text);
 ui.el("policy-text").value="AgenticHub 这类报错先建议找 hongwei；联系前询问我";await ui.el("policy-form").emit("submit");await settle();
 let change=api.calls.filter(c=>c.path==="/v1/policy/update").at(-1).data.policy;
 assert.equal(new Date(change.expires_at).getTime(),new Date(record.expires_at).getTime());assert.equal(change.version,1);
 for(const key of ["kind","scope","scope_from","actions","conditions","exceptions","conversation"])assert.equal(Object.hasOwn(change,key),false);
 await ui.el("policy-entries").children[0].children[0].children[1].children[0].emit("click");
 ui.el("policy-expires").value="";await ui.el("policy-form").emit("submit");await settle();
 change=api.calls.filter(c=>c.path==="/v1/policy/update").at(-1).data.policy;assert.equal(Object.hasOwn(change,"expires_at"),false);
 assert.equal(api.calls.some(c=>/member\/run|actions\/execute|inbox\/confirm|matters\/list/.test(c.path)),false);
});

test("long-term rules save without type, platform connection or project fields",async()=>{
 const api=server([makeRoom("A")]);
 const member={agent_id:"assistant",membership_id:"1".repeat(32),status:"idle",runner:{profile:"runtime"}};
 api.hook=(path,data)=>{
  if(path==="/v1/member/list")return {members:[member]};
  if(path==="/v1/member/activity")return {member};
  if(path==="/v1/policy/read")return {revision:0,history:[],current:[],effective:[]};
  if(path==="/v1/policy/update")return {};
 };
 const ui=await launch(api);await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();
 await ui.el("agent-tab-policy").emit("click");
 const before=api.calls.filter(c=>c.path.startsWith("/v1/platform/connections")).length;
 await ui.el("policy-new").emit("click");await settle();
 for(const id of ["policy-kind","policy-rule-fields","policy-connection","policy-project","policy-object-type"])assert.equal(ui.el(id),undefined);
 ui.el("policy-text").value="项目 A 的使用问题核实现行文档后直接回复；涉及交付时间先问我。";
 await ui.el("policy-form").emit("submit");await settle();
 const change=api.calls.find(c=>c.path==="/v1/policy/update").data.policy;
 assert.equal(change.text,"项目 A 的使用问题核实现行文档后直接回复；涉及交付时间先问我。");
 for(const key of ["kind","scope","scope_from","actions","conditions","exceptions"])assert.equal(Object.hasOwn(change,key),false);
 assert.equal(api.calls.filter(c=>c.path.startsWith("/v1/platform/connections")).length,before);
 assert.equal(api.calls.some(c=>/actions\/execute|member\/run/.test(c.path)),false);
});

test("AN-02.2 lost saves require a fresh version and late reads cannot cross rooms",async()=>{
 const api=server([makeRoom("A"),makeRoom("B")]);
 const member={agent_id:"assistant",membership_id:"1".repeat(32),status:"idle",runner:{profile:"runtime"}};
 let finish,delay=false;
 api.hook=(path,data)=>{
  if(path==="/v1/member/list")return {members:[member]};
  if(path==="/v1/member/activity")return {member};
  if(path==="/v1/policy/read")return delay ? new Promise(resolve=>finish=()=>resolve({revision:99,history:[],current:[],effective:[]})) : {revision:0,history:[],current:[],effective:[]};
  if(path==="/v1/matters/list")return {items:[],total:0,complete:true};
  if(path==="/v1/policy/update")throw Error("response lost");
 };
 const ui=await launch(api);await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();await ui.el("agent-tab-policy").emit("click");await settle();
 await ui.el("policy-new").emit("click");ui.el("policy-text").value="简短回复";
 await ui.el("policy-form").emit("submit");await settle();await ui.el("policy-form").emit("submit");await settle();
 assert.equal(api.calls.filter(c=>c.path==="/v1/policy/update").length,1);assert.match(ui.el("policy-status").textContent,/刷新核对/);
 delay=true;const pending=ui.el("policy-reload").emit("click");await settle();await ui.select("B");finish();await pending;await settle();
 assert.doesNotMatch(ui.el("policy-status").textContent,/99/);
});

test("R5 management navigates native followups, human evidence, receipts and source pages",async()=>{
 const api=server([makeRoom("A")]);
 const member={agent_id:"assistant",membership_id:"1".repeat(32),status:"idle",runner:{profile:"runtime"}};
 const summary={ref:"unit-object",kind:"object",object_ref:"ref-object",target_ref:"ref-object",revision:4,status:"waiting_followup",summary:"已回复，等待确认",next:"等待客户确认",all_completed:false};
 let sourceRequests=0;
 api.hook=(path,data)=>{
  if(path==="/v1/member/list")return {members:[member]};
  if(path==="/v1/member/activity")return {member};
  if(path==="/v1/policy/read")return {revision:1,current:[],history:[],effective:[]};
  if(path==="/v1/matters/list")return {items:[summary],total:1,complete:true};
  if(path==="/v1/matters/read"){
   assert.equal(data.followup.unit_ref,"unit-object");assert.equal(data.caller.kind,"human");
   return {target:{target:{provider:"gitlab",instance:"https://gitlab.test",object:{type:"MergeRequest",id:{value:"9001"}}}},unit:{revision:4,status:"waiting_followup",summary:"已回复",next:"等待确认",questions:[{text:"选 A 吗？",room_seq:5,answers:[{message:{author:{id:"tester"},seq:6,text:"这次选 A"}}]}],instructions:[{status:"fulfilled",intent:{body:"准确回复正文"},source:{seq:6,text:"这次选 A"}}],actions:[{request:{id:"reply-1",operation:"object.comment",input:{body:"准确回复正文"}},status:"succeeded",attempts:1,inspection:{account_label:"bot"},access:{account:{id:{value:"101"}}},result:{message:"已回复",url:"https://gitlab.test/p/-/merge_requests/42#note_99"}}]}};
  }
  if(path==="/v1/matters/sources"){sourceRequests++;return {items:[{seq:data.after ? 2 : 1,source_ref:data.after ? "src-two":"src-one",status:"resolved"}],total:2,next_after:1,complete:!!data.after};}
  if(path==="/v1/matters/source")return {source:{url:"https://gitlab.test/p/-/merge_requests/42"},delivery_id:"immutable-delivery",resolution:{status:"resolved"},entry:{item:{text:"<script>原始通知正文</script>"}}};
 };
 const ui=await launch(api);await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();await ui.el("agent-tab-policy").emit("click");await settle();
 assert.equal(api.calls.some(call=>call.path==="/v1/matters/list"),false);
 assert.equal(ui.el("policy-followups").open,false);assert.equal(ui.el("policy-matters").children.length,0);
 await expandDisclosure(ui.el("policy-followups"));
 const row=ui.el("policy-matters").children[0];assert.match(elementText(row.children[0]),/等待后续/);
 await expandDisclosure(row);
 const detail=row.followupPanel;
 const progress=detail.children[0];
 assert.match(elementText(progress),/处理进展/);assert.match(elementText(progress),/用户 tester #6：这次选 A/);assert.match(elementText(progress),/已落实/);assert.match(elementText(progress),/发表评论 · 已成功 · 1 次尝试/);
 assert.doesNotMatch(elementText(progress),/reply-1/);
 assert.equal(sourceRequests,0);
 const sources=detail.children[1];assert.equal(sources.children[0].textContent,"相关通知");
 await expandDisclosure(sources);assert.equal(sourceRequests,1);
 const sourceList=sources.children[1],source=sourceList.children[0];
 assert.match(source.children[0].textContent,/已关联到此对象/);
 assert.equal(api.calls.some(call=>call.path==="/v1/matters/source"),false);
 await expandDisclosure(source);assert.match(elementText(source.children[1]),/<script>原始通知正文<\/script>/);
 source.open=false;await source.emit("toggle");await expandDisclosure(source);
 assert.equal(api.calls.filter(call=>call.path==="/v1/matters/source").length,1);
 await sourceList.children[1].emit("click");await settle();assert.equal(sourceRequests,2);
 ui.el("followups-history").checked=true;await ui.el("followups-history").emit("change");await settle();
 assert.equal(api.calls.filter(call=>call.path==="/v1/matters/list").at(-1).data.followup.include_completed,true);
 assert.equal(api.calls.some(call=>/decisions\/|member\/run|inbox\/confirm|actions\/execute/.test(call.path)),false);
});

test("R5 late followup details cannot enter another Room",async()=>{
 const api=server([makeRoom("A"),makeRoom("B")]);
 const member={agent_id:"assistant",membership_id:"1".repeat(32),status:"idle",runner:{profile:"runtime"}};
 let finish;
 api.hook=(path,data)=>{
  if(path==="/v1/member/list")return {members:[member]};
  if(path==="/v1/member/activity")return {member};
  if(path==="/v1/policy/read")return {revision:0,current:[],history:[],effective:[]};
  if(path==="/v1/matters/list")return {items:[{ref:"unit-one",object_ref:"ref-one",kind:"object",status:"pending"}],total:1,complete:true};
  if(path==="/v1/matters/read")return new Promise(resolve=>finish=()=>resolve({target:{target:{provider:"gitlab",instance:"https://gitlab.test",object:{type:"MergeRequest",id:{value:"private-9001"}}}},unit:{revision:1,status:"pending"}}));
 };
 const ui=await launch(api);await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();await ui.el("agent-tab-policy").emit("click");await settle();
 await expandDisclosure(ui.el("policy-followups"));
 const row=ui.el("policy-matters").children[0];await expandDisclosure(row);await ui.select("B");finish();await settle();
 assert.equal(row.followupPanel.children.length,0);
});

test("followup inspection ignores a closed panel's response and resets on tab reentry",async()=>{
 const api=server([makeRoom("A")]);
 const member={agent_id:"assistant",membership_id:"1".repeat(32),status:"idle",runner:{profile:"runtime"}};
 let finish,listCalls=0;
 api.hook=(path)=>{
  if(path==="/v1/member/list")return {members:[member]};
  if(path==="/v1/member/activity")return {member};
  if(path==="/v1/policy/read")return {revision:1,current:[],history:[],effective:[]};
  if(path==="/v1/matters/list"){
   listCalls++;
   if(listCalls===1)return new Promise(resolve=>finish=()=>resolve({items:[{ref:"old",kind:"unresolved_source",seq:1,status:"partial"}],total:1,complete:true}));
   return {items:[],total:0,complete:true};
  }
 };
 const ui=await launch(api);await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();await ui.el("agent-tab-policy").emit("click");await settle();
 assert.equal(ui.el("policy-entries").children.length,0);assert.equal(ui.el("policy-empty").hidden,false);assert.equal(listCalls,0);
 await expandDisclosure(ui.el("policy-followups"));
 ui.el("policy-followups").open=false;await ui.el("policy-followups").emit("toggle");finish();await settle();
 assert.equal(ui.el("policy-matters").children.length,0);assert.equal(ui.el("followups-status").textContent,"");
 await expandDisclosure(ui.el("policy-followups"));assert.equal(listCalls,2);assert.match(elementText(ui.el("policy-matters")),/暂无跟进记录/);
 await ui.el("agent-tab-inbox").emit("click");await settle();await ui.el("agent-tab-policy").emit("click");await settle();
 assert.equal(ui.el("policy-followups").open,false);assert.equal(listCalls,2);
 assert.equal(api.calls.some(call=>/member\/run|inbox\/confirm|actions\/execute|matters\/resolve/.test(call.path)),false);
});

test("followup inspection errors do not hide rules and refresh only the records",async()=>{
 const api=server([makeRoom("A")]);
 const member={agent_id:"assistant",membership_id:"1".repeat(32),status:"idle",runner:{profile:"runtime"}};
 let fail=true;
 api.hook=(path)=>{
  if(path==="/v1/member/list")return {members:[member]};
  if(path==="/v1/member/activity")return {member};
  if(path==="/v1/policy/read")return {revision:1,current:[],history:[],effective:[]};
  if(path==="/v1/matters/list"){
   if(fail)throw Error("records unavailable");
   return {items:[{ref:"question",kind:"discussion",status:"waiting_user",questions:[{text:"需要你的决定"}]}],total:1,complete:true};
  }
 };
 const ui=await launch(api);await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();await ui.el("agent-tab-policy").emit("click");await settle();
 const rules=ui.el("policy-entries").children[0];
 await expandDisclosure(ui.el("policy-followups"));assert.match(ui.el("followups-status").textContent,/records unavailable/);
 assert.equal(ui.el("policy-entries").children[0],rules);assert.doesNotMatch(ui.el("policy-status").textContent,/失败/);
 fail=false;await ui.el("followups-reload").emit("click");await settle();
 assert.equal(api.calls.filter(call=>call.path==="/v1/policy/read").length,1);
 const row=ui.el("policy-matters").children[0];assert.equal(row.children[0].children[1].dataset.attention,"true");assert.match(elementText(row),/待你答复：需要你的决定/);
});

test("rules failing to load still allow explicit read-only followup inspection",async()=>{
 const api=server([makeRoom("A")]);
 const member={agent_id:"assistant",membership_id:"1".repeat(32),status:"idle",runner:{profile:"runtime"}};
 api.hook=path=>{
  if(path==="/v1/member/list")return {members:[member]};
  if(path==="/v1/member/activity")return {member};
  if(path==="/v1/policy/read")throw Error("rules unavailable");
  if(path==="/v1/matters/list")return {items:[],total:0,complete:true};
 };
 const ui=await launch(api);await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();await ui.el("agent-tab-policy").emit("click");await settle();
 assert.match(ui.el("policy-status").textContent,/rules unavailable/);
 assert.equal(api.calls.some(call=>call.path==="/v1/matters/list"),false);
 await expandDisclosure(ui.el("policy-followups"));
 assert.match(elementText(ui.el("policy-matters")),/暂无跟进记录/);
 assert.match(ui.el("policy-status").textContent,/rules unavailable/);
});

for (const [name, reset, exhausted] of [["paused", false, false], ["budget", false, true], ["session", true, false], ["session and budget", true, true]]) {
  test(`enable collaboration handles ${name} through the existing action`, async () => {
    const api = server([makeRoom("A")]);
    const member = { agent_id: "reviewer", membership_id: "generation-reviewer", status: exhausted ? "budget_exhausted" : reset ? "backend_blocked" : "paused",
      runner: { profile: "runtime" }, unread_count: 1,
      execution: { paused: true, active: false, session_reset_required: reset, turn_budget: { used: exhausted ? 7 : 1, limit: 7, grant: 7 }, agent: { turn_number: 1, turn: { status: "failed" } } } };
    api.hook = (path) => {
      if (path === "/v1/member/list") return { members: [structuredClone(member)] };
      if (path === "/v1/member/activity") return { member: structuredClone(member) };
      if (path === "/v1/member/run") {
        member.execution.paused = false; member.execution.session_reset_required = false; member.status = "idle";
        if (exhausted) member.execution.turn_budget.limit += 7;
        return structuredClone(member.execution);
      }
    };
    const ui = await launch(api);
    await ui.el("members").children[0].querySelector(".member-summary").emit("click"); await settle();
    const hint = ui.el("agent-enable-hint");
    assert.equal(hint.hidden, !reset && !exhausted);
    assert.equal(hint.textContent.includes("重置会话，清除私有上下文"), reset);
    assert.equal(hint.textContent.includes("追加 7 回合"), exhausted);
    const button = ui.el("agent-actions").children[0];
    assert.equal(button.textContent, "启用协作"); assert.equal(button.disabled, false);
    await button.emit("click"); await settle();
    assert.equal(api.calls.filter(call => call.path === "/v1/member/run").length, 1);
    assert.equal(api.calls.some(call => call.path === "/v1/member/session/rotate"), false);
    assert.equal(ui.el("agent-actions").children[0].textContent, "暂停");
    assert.equal(hint.hidden, true);
    assert.equal(member.execution.turn_budget.used, exhausted ? 7 : 1);
  });
}

test("an unconfirmed enable is observed without automatically replaying the mutation", async () => {
  const api = server([makeRoom("A")]);
  const member = { agent_id: "reviewer", membership_id: "generation-reviewer", status: "budget_exhausted", runner: { profile: "runtime" },
    execution: { paused: true, active: false, turn_budget: { used: 16, limit: 16, grant: 16 }, agent: { turn_number: 16 } } };
  api.hook = path => {
    if (path === "/v1/member/list") return { members: [structuredClone(member)] };
    if (path === "/v1/member/activity") return { member: structuredClone(member) };
    if (path === "/v1/member/run") throw new Error("response lost");
  };
  const ui = await launch(api);
  await ui.el("members").children[0].querySelector(".member-summary").emit("click"); await settle();
  await ui.el("agent-actions").children[0].emit("click"); await settle();
  assert.match(ui.el("sync-error").textContent, /操作未确认/);
  await ui.refresh(); await settle();
  assert.equal(api.calls.filter(call => call.path === "/v1/member/run").length, 1);
});

const gitlabPath = action => `/v1/platform/gitlab/notifications/${action}`;
const gitlabWrites = api => api.calls.filter(c => c.path === gitlabPath("configure"));
async function openGitLab(ui) { await ui.el("open-gitlab-source").emit("click"); await settle(); }
async function editGitLab(ui, id, value, event = "change") { ui.el(id).value = value; await ui.el(id).emit(event); }
async function selectGitLabProject(ui, path) {
  await expandDisclosure(ui.el("gitlab-project-picker"));
  const input=ui.el("gitlab-project-options").children.map(row=>row.children[0]).find(input=>input.value===path);
  assert.ok(input,`project ${path} is listed`);input.checked=true;await input.emit("change");
}
async function toggleGitLab(ui, id, checked) { ui.el(id).checked = checked; await ui.el(id).emit("change"); }

test("notification account status stays separate from connection management",async()=>{
  const html=await readFile(new URL("./web/index.html",import.meta.url),"utf8"),ids=[...html.matchAll(/\bid="([^"]+)"/g)].map(match=>match[1]);
  assert.equal(new Set(ids).size,ids.length,"HTML IDs must be unique across notification and connection dialogs");
  const {api}=todosServer(),ui=await launch(api);await openGitLab(ui);
  await editGitLab(ui,"gitlab-connection","a".repeat(32));
  const account=ui.el("gitlab-notification-account").textContent;assert.match(account,/@alice/);
  await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");
  assert.equal(ui.el("gitlab-notification-account").textContent,account);
  assert.notEqual(ui.el("gitlab-account").textContent,account);
});
test("Escape dismisses an open native select before the notification panel",async()=>{
  const {api}=todosServer(),ui=await launch(api);await openGitLab(ui);
  const select=ui.el("gitlab-connection");let pickerOpen=true;
  select.closest=selector=>selector==="select:open" && pickerOpen ? select : null;
  await ui.el("notification-detail").emit("keydown",{key:"Escape",target:select});
  assert.equal(ui.el("notification-detail").open,true);
  pickerOpen=false;
  await ui.el("notification-detail").emit("keydown",{key:"Escape",target:select,defaultPrevented:true});
  assert.equal(ui.el("notification-detail").open,true);
  await ui.el("notification-detail").emit("keydown",{key:"Escape",target:select});
  assert.equal(ui.el("notification-detail").open,false);
});
test("GitLab channel switches reveal their own settings and retain independent drafts",async()=>{
  const {api,events}=todosServer();events.get("A").config.enabled=false;
  const ui=await launch(api);await openGitLab(ui);
  assert.equal(ui.el("gitlab-sync").hidden,false);assert.equal(ui.el("gitlab-sync").disabled,true);
  await toggleGitLab(ui,"gitlab-todos-enabled",true);
  assert.equal(ui.el("gitlab-todos-settings").open,true);assert.equal(ui.el("gitlab-events-enabled").checked,false);
  await toggleGitLab(ui,"gitlab-events-enabled",true);
  assert.equal(ui.el("gitlab-events-settings").open,true);
  await toggleGitLab(ui,"gitlab-events-enabled",false);assert.equal(ui.el("gitlab-events-settings").open,false);
  await toggleGitLab(ui,"gitlab-events-enabled",true);assert.equal(ui.el("gitlab-events-settings").open,true);
  await ui.el("notification-tab-messages").emit("click");await ui.el("notification-tab-gitlab").emit("click");
  assert.equal(ui.el("gitlab-todos-enabled").checked,true);assert.equal(ui.el("gitlab-events-enabled").checked,true);
  assert.equal(gitlabWrites(api).length,0);
});
test("GitLab shared sync status preserves independent failures and rejects stale success",async()=>{
  const {api,events,todos}=todosServer({enabled:true}),original=api.hook;let offline=false;
  Object.assign(events.get("A"),{status:"backoff",error_code:"query_failed",pending:2,last_check:"2026-11-07T01:00:00Z",retry_at:"2026-11-07T01:01:00Z"});
  Object.assign(todos.get("A"),{last_check:"2026-11-07T01:00:30Z"});
  api.hook=(path,body)=>offline && path.startsWith("/v1/platform/notifications?") ? Promise.reject(Error("offline")) : original(path,body);
  const ui=await launch(api);await openGitLab(ui);
  ui.el("gitlab-events-settings").open=false;ui.el("gitlab-todos-settings").open=false;
  assert.match(ui.el("gitlab-events-status").textContent,/最近检查.*待投递 2.*下次重试.*查询失败/);
  assert.equal(ui.el("gitlab-events-status").attributes["data-error"],"true");
  assert.match(ui.el("gitlab-todos-status").textContent,/最近检查/);
  assert.doesNotMatch(ui.el("gitlab-todos-status").textContent,/查询失败|下次重试|待投递/);
  assert.equal(ui.el("gitlab-todos-status").attributes["data-error"],"false");
  offline=true;await ui.refresh();await settle();
  for(const channel of ["events","todos"]){
    assert.match(ui.el(`gitlab-${channel}-status`).textContent,/状态暂不可读/);
    assert.doesNotMatch(ui.el(`gitlab-${channel}-status`).textContent,/最近检查/);
  }
  assert.equal(gitlabWrites(api).length,0);
});

test("GitLab saves one provider model with one version and no repeated shared fields", async () => {
  const { api, events, todos } = todosServer({ enabled:true }), ui = await launch(api); await openGitLab(ui);
  for (const id of ["gitlab-events-interval", "gitlab-todos-interval", "gitlab-todos-inactive-interval", "gitlab-events-branches", "gitlab-events-ignore", "gitlab-todos-scope"]) assert.equal(ui.el(id), undefined);
  await editGitLab(ui,"gitlab-interval","60","input");
  assert.equal(ui.el("gitlab-events-save"), undefined); assert.equal(ui.el("gitlab-todos-save"), undefined);
  await editGitLab(ui,"gitlab-connection","b".repeat(32)); await ui.el("gitlab-save").emit("click");
  assert.equal(gitlabWrites(api).length,1);
  const body=gitlabWrites(api)[0].data;
  assert.equal(body.version,0); assert.equal(body.channel,undefined); assert.equal(body.subscription_id,undefined);
  assert.equal(body.config.connection_id,"b".repeat(32));
  assert.equal(body.config.interval_seconds,60);
  for(const source of [body.config.events,body.config.todos]) { assert.equal(source.connection_id,undefined); assert.equal(source.target,undefined); assert.equal(source.interval_seconds,undefined); }
  assert.equal(body.config.todos.project_scope,undefined);assert.equal(body.config.todos.inactive_interval_seconds,undefined);
  for (const source of [events,todos]) assert.equal(source.get("A").config.interval_seconds,60);
  assert.equal(events.get("A").config.connection_id,"b".repeat(32)); assert.equal(todos.get("A").config.connection_id,"b".repeat(32));
  assert.equal(ui.el("gitlab-save").disabled,true);
  await ui.el("gitlab-sync").emit("click");
  const sync=api.calls.find(c=>c.path===gitlabPath("sync")); assert.equal(sync.data.version,1); assert.equal(sync.data.config,undefined);
});

test("GitLab can save common settings while both message types are disabled", async()=>{
  const {api,events}=todosServer(); Object.assign(events.get("A"),{subscription_version:0,status:"disabled"});events.get("A").config.enabled=false;
  const ui=await launch(api);await openGitLab(ui);await editGitLab(ui,"gitlab-connection","b".repeat(32));
  await ui.el("gitlab-save").emit("click");assert.equal(gitlabWrites(api).length,1);
  assert.equal(api.gitlabRecords.get("A").config.connection_id,"b".repeat(32));
  await ui.refresh();assert.equal(ui.el("gitlab-connection").value,"b".repeat(32));assert.equal(ui.el("gitlab-events-enabled").checked,false);
});

test("GitLab common target submits the exact membership once for both message types",async()=>{
  const {api,events,todos}=todosServer({enabled:true}),hook=api.hook;
  const member={agent_id:"reviewer",membership_id:"reviewer-generation-2",runner:{profile:"runtime"},status:"paused"};
  api.hook=(path,body)=>path==="/v1/member/list"?{members:[member],latest_seq:1}:hook(path,body);
  const ui=await launch(api);await openGitLab(ui);
  const value=JSON.stringify({mode:"inbox",agent_id:member.agent_id,membership_id:member.membership_id});
  await editGitLab(ui,"gitlab-target",value);await ui.el("gitlab-save").emit("click");
  assert.deepEqual(gitlabWrites(api)[0].data.config.target,JSON.parse(value));
  for (const source of [events,todos]) assert.deepEqual(source.get("A").config.target,JSON.parse(value));
  assert.equal(api.calls.some(c=>/member\/(restart|enable|resume)/.test(c.path)),false);
});

test("GitLab enables one selected type while retaining one common config",async()=>{
  const {api,events,todos}=todosServer({bound:false});events.get("A").config.enabled=false;events.get("A").subscription_version=0;
  const ui=await launch(api);await openGitLab(ui);await editGitLab(ui,"gitlab-connection","a".repeat(32));
  await toggleGitLab(ui,"gitlab-todos-enabled",true);await ui.el("gitlab-save").emit("click");
  assert.equal(gitlabWrites(api).length,1);assert.equal(events.get("A").config.enabled,false);assert.equal(todos.get("A").config.enabled,true);
});

test("GitLab public draft survives polling, platform tabs, rail closing and connection management",async()=>{
  const {api}=todosServer({enabled:true}),ui=await launch(api);await openGitLab(ui);
  await selectGitLabProject(ui,"group/changed");await editGitLab(ui,"gitlab-interval","60","input");
  await ui.refresh();await ui.el("notification-tab-messages").emit("click");await ui.el("notification-tab-gitlab").emit("click");
  await ui.el("gitlab-manage-connections").emit("click");await ui.el("close-settings").emit("click");await settle();
  await ui.el("close-notification").emit("click");await openGitLab(ui);
  assert.equal(ui.el("gitlab-project-summary").textContent,"group/changed");assert.equal(String(ui.el("gitlab-interval").value),"60");assert.equal(gitlabWrites(api).length,0);
  await ui.el("gitlab-save").emit("click");assert.equal(gitlabWrites(api)[0].data.config.events.project,"group/changed");
  assert.equal(gitlabWrites(api)[0].data.config.interval_seconds,60);
});

test("GitLab rejects an empty activity selection and preserves explicit empty todo filters",async()=>{
  const {api}=todosServer({enabled:true}),ui=await launch(api);await openGitLab(ui);
  await toggleGitLab(ui,"gitlab-events-all-types",false);assert.equal(ui.el("gitlab-save").disabled,true);
  await ui.refresh();assert.equal(ui.el("gitlab-events-type-opened").checked,false);
  await toggleGitLab(ui,"gitlab-events-all-types",true);
  for(const reason of ["mentioned","assigned","review_requested","other"])await toggleGitLab(ui,`gitlab-todos-reason-${reason}`,false);
  await ui.el("gitlab-save").emit("click");assert.deepEqual(gitlabWrites(api)[0].data.config.todos.filter.reasons,[]);
  await ui.refresh();assert.equal(ui.el("gitlab-todos-reason-mentioned").checked,false);
});

test("GitLab validates the common interval before the one save",async()=>{
  const {api}=todosServer({enabled:true}),ui=await launch(api);await openGitLab(ui);
  await editGitLab(ui,"gitlab-interval","121","input");assert.equal(ui.el("gitlab-save").disabled,true);
  await editGitLab(ui,"gitlab-interval","2","input");assert.equal(ui.el("gitlab-save").disabled,true);
  await editGitLab(ui,"gitlab-interval","10","input");assert.equal(ui.el("gitlab-save").disabled,false);
  await ui.el("gitlab-save").emit("click");assert.equal(gitlabWrites(api).length,1);
});

test("GitLab saved settings with a failed source expose an explicit apply retry",async()=>{
  const {api,events,todos}=todosServer({enabled:true}),ui=await launch(api);await openGitLab(ui);
  api.gitlabApplyFailure.add("todos");await editGitLab(ui,"gitlab-connection","b".repeat(32));await ui.el("gitlab-save").emit("click");
  const eventVersion=events.get("A").subscription_version;
  assert.equal(api.gitlabRecords.get("A").config.connection_id,"b".repeat(32));assert.equal(todos.get("A").config.connection_id,"a".repeat(32));
  assert.equal(ui.el("gitlab-save").textContent,"重试应用");assert.equal(ui.el("gitlab-sync").disabled,true);assert.equal(ui.el("gitlab-todos-settings").open,true);
  await ui.refresh();assert.equal(ui.el("gitlab-save").textContent,"重试应用");assert.equal(gitlabWrites(api).length,1);
  api.gitlabApplyFailure.clear();await ui.el("gitlab-save").emit("click");
  assert.equal(gitlabWrites(api).length,2);assert.equal(gitlabWrites(api)[1].data.version,1);assert.equal(events.get("A").subscription_version,eventVersion);
  assert.equal(todos.get("A").config.connection_id,"b".repeat(32));assert.equal(ui.el("gitlab-save").disabled,true);
});

test("GitLab lost committed response is read back without a second save",async()=>{
  const {api}=todosServer({enabled:true}),hook=api.hook;let lose=true;
  api.hook=(path,data)=>{const result=hook(path,data);if(path===gitlabPath("configure")&&lose){lose=false;throw new Error("response lost");}return result;};
  const ui=await launch(api);await openGitLab(ui);await editGitLab(ui,"gitlab-interval","61","input");await ui.el("gitlab-save").emit("click");
  await ui.refresh();assert.equal(gitlabWrites(api).length,1);assert.equal(String(ui.el("gitlab-interval").value),"61");assert.equal(ui.el("gitlab-save").disabled,true);
});

test("GitLab uncommitted timeout retains its draft and needs an explicit retry",async()=>{
  const {api}=todosServer({enabled:true}),hook=api.hook;let fail=true;
  api.hook=(path,data)=>{if(path===gitlabPath("configure")&&fail)throw new Error("timeout");return hook(path,data);};
  const ui=await launch(api);await openGitLab(ui);await editGitLab(ui,"gitlab-interval","62","input");await ui.el("gitlab-save").emit("click");
  await ui.refresh();assert.equal(gitlabWrites(api).length,1);assert.equal(String(ui.el("gitlab-interval").value),"62");assert.equal(ui.el("gitlab-save").disabled,false);
  fail=false;await ui.el("gitlab-save").emit("click");assert.equal(gitlabWrites(api).length,2);
});

test("GitLab provider version conflicts read current state without replay",async()=>{
  const {api}=todosServer({enabled:true}),hook=api.hook;let conflict=true;
  api.hook=(path,data)=>{if(path===gitlabPath("configure")&&conflict)throw Object.assign(new Error("conflict"),{code:"conflict"});return hook(path,data);};
  const ui=await launch(api);await openGitLab(ui);await editGitLab(ui,"gitlab-interval","63","input");await ui.el("gitlab-save").emit("click");
  assert.equal(gitlabWrites(api).length,1);assert.match(ui.el("gitlab-feedback").textContent,/版本已变化/);
  conflict=false;await ui.el("gitlab-save").emit("click");assert.equal(gitlabWrites(api).length,2);
});

test("GitLab locks every setting during one save, blocks duplicate clicks and isolates late room replies",async()=>{
  const {api}=todosServer({enabled:true}),hook=api.hook;let release;
  api.hook=(path,data)=>path===gitlabPath("configure")?new Promise(resolve=>{release=()=>resolve(hook(path,data));}):hook(path,data);
  const ui=await launch(api);await openGitLab(ui);await editGitLab(ui,"gitlab-connection","b".repeat(32));
  const saving=ui.el("gitlab-save").emit("click");await settle();assert.equal(ui.el("gitlab-shared-fields").disabled,true);assert.equal(ui.el("gitlab-events-fields").disabled,true);assert.equal(ui.el("gitlab-todos-fields").disabled,true);
  await ui.el("gitlab-save").emit("click");assert.equal(gitlabWrites(api).length,1);
  await ui.select("B");release();await saving;await openGitLab(ui);
  assert.equal(ui.el("gitlab-connection").value,"a".repeat(32));assert.equal(String(ui.el("gitlab-interval").value),"17");assert.equal(ui.el("gitlab-feedback").textContent,"");
});

test("GitLab stale polling cannot roll back a completed provider save",async()=>{
  const {api}=todosServer({enabled:true}),ui=await launch(api);await openGitLab(ui);const hook=api.hook;let release;
  api.hook=(path,data)=>{if(path.startsWith("/v1/platform/notifications?")){const old=hook(path,data);return new Promise(resolve=>{release=()=>resolve(old);});}return hook(path,data);};
  const reading=ui.refresh();await settle();await editGitLab(ui,"gitlab-connection","b".repeat(32));await editGitLab(ui,"gitlab-interval","64","input");await ui.el("gitlab-save").emit("click");
  release();await reading;assert.equal(String(ui.el("gitlab-interval").value),"64");assert.equal(ui.el("gitlab-save").disabled,true);
});

test("GitLab project selection works without Workspace and only edits the public draft",async()=>{
  const {api}=todosServer({enabled:true}),ui=await launch(api);await openGitLab(ui);
  assert.equal(ui.el("notification-manage-binding"),undefined);assert.equal(ui.el("gitlab-events-project"),undefined);
  await selectGitLabProject(ui,"other/project");
  assert.equal(ui.el("gitlab-project-summary").textContent,"other/project");assert.equal(gitlabWrites(api).length,0);
  const hook=api.hook;api.hook=(path,data)=>path.startsWith("/v1/workspace?")?Promise.reject(new Error("offline")):hook(path,data);
  await ui.refresh();await editGitLab(ui,"gitlab-interval","65","input");assert.equal(ui.el("gitlab-save").disabled,false);
  await ui.el("gitlab-save").emit("click");assert.equal(gitlabWrites(api).length,1);assert.equal(api.calls.some(c=>c.path==="/v1/workspace/apply"),false);
});

test("GitLab same connection reverified as a new account requires explicit apply",async()=>{
  const {api}=todosServer({enabled:true}),ui=await launch(api);await openGitLab(ui);
  // Runtime identity is evidence of the previously accepted account.
  api.gitlabRecords.set("A",{version:1,config:structuredClone(fixtureGitLabView([eventView({config:{enabled:true,connection_id:"a".repeat(32),project:"group/project-A",interval_seconds:5,filter:{}},metrics:{}}),todoView({config:{enabled:true,connection_id:"a".repeat(32),interval_seconds:30,filter:{}}})]).config)});
  api.connections[0].validation={status:"valid",user_id:303,username:"carol"};
  await ui.el("gitlab-manage-connections").emit("click");await ui.el("close-settings").emit("click");await settle();
  assert.match(ui.el("gitlab-notification-account").textContent,/@carol/);assert.equal(ui.el("gitlab-save").disabled,false);assert.equal(gitlabWrites(api).length,0);
  await ui.el("gitlab-save").emit("click");assert.equal(gitlabWrites(api).length,1);
});

test("GitLab reflects a provider operation started in another tab and keeps this tab's draft",async()=>{
  const {api,events,todos}=todosServer({enabled:true}),ui=await launch(api);await openGitLab(ui);
  await editGitLab(ui,"gitlab-interval","66","input");
  api.gitlabRecords.set("A",{version:1,config:fixtureGitLabView([eventView(events.get("A")),todoView(todos.get("A"))]).config,applying:true});
  await ui.refresh();assert.equal(ui.el("gitlab-shared-fields").disabled,true);assert.equal(ui.el("gitlab-save").disabled,true);
  assert.match(ui.el("gitlab-feedback").textContent,/正在应用/);assert.equal(String(ui.el("gitlab-interval").value),"66");
  api.gitlabRecords.get("A").applying=false;await ui.refresh();assert.equal(ui.el("gitlab-save").disabled,false);
  await ui.el("gitlab-save").emit("click");assert.equal(gitlabWrites(api)[0].data.version,1);assert.equal(gitlabWrites(api)[0].data.config.interval_seconds,66);
});

test("Scion Workspace chooses its type and verified connection without Git suffixes or numeric project identity", async () => {
  const api=workspaceServer();
  api.connections.push({id:"c".repeat(32),provider:"scion",name:"Scion demo",url:"https://scion.invalid",version:3,validation:{status:"valid",username:"alice"}});
  api.connections.push({id:"d".repeat(32),provider:"scion",name:"Unverified",url:"https://scion.invalid",version:1,validation:{status:"unverified"}});
  const ui=await launch(api);await editWorkspace(ui);
  ui.el("workspace-kind").value="scion";await ui.el("workspace-kind").emit("change");
  assert.equal(ui.el("workspace-gitlab").checked,true);assert.equal(ui.el("workspace-gitlab").disabled,true);
  const choices=ui.el("workspace-connection").children.map(x=>x.value);
  assert.deepEqual(choices,["","c".repeat(32)]);
  assert.match(ui.el("workspace-connection").children[1].textContent,/alice/);
  ui.el("workspace-repo-url").value="https://scion.invalid/OpenCSG/teamx-demo";
  ui.el("workspace-connection").value="c".repeat(32);await ui.el("workspace-connection").emit("change");
  await ui.el("workspace-form").emit("submit");
  const sent=api.calls.find(c=>c.path==="/v1/workspace/apply").data;
  assert.deepEqual(sent.target.repository,{kind:"scion",url:"https://scion.invalid/OpenCSG/teamx-demo",branch:"main"});
  assert.deepEqual(sent.target.binding,{provider:"scion",url:"https://scion.invalid",project:"OpenCSG/teamx-demo",connection_id:"c".repeat(32)});
  assert.equal(sent.connection_version,3);
  await ui.el("open-workspace").emit("click");
  assert.equal(ui.el("workspace-kind").value,"scion");assert.equal(ui.el("workspace-kind").disabled,true);
  assert.match(ui.el("workspace-credential-status").textContent,/Scion.*OpenCSG\/teamx-demo/);
  const reload=await launch(api,ui.saved);await reload.el("open-workspace").emit("click");
  assert.equal(reload.el("workspace-kind").value,"scion");assert.equal(reload.el("workspace-connection").value,"c".repeat(32));
});

test("Scion Workspace rejects unverified and mismatched connections before retaining an operation", async () => {
  for(const valid of [false,true]){
    const api=workspaceServer();api.connections.push({id:"c".repeat(32),provider:"scion",name:"Scion",url:"https://other.invalid",version:1,validation:{status:valid ? "valid" : "unverified"}});
    const ui=await launch(api);await editWorkspace(ui);
    ui.el("workspace-kind").value="scion";await ui.el("workspace-kind").emit("change");
    ui.el("workspace-repo-url").value="https://scion.invalid/OpenCSG/teamx-demo";
    ui.el("workspace-connection").value="c".repeat(32);
    await ui.el("workspace-form").emit("submit");
    assert.equal(api.calls.some(c=>c.path==="/v1/workspace/apply"),false);
    assert.match(ui.el("workspace-error").textContent,valid ? /实例不一致/ : /已验证/);
  }
});

test("GitLab project picker searches remotely, pages the current query and saves a single project",async()=>{
  const {api}=todosServer({enabled:true});
  const project=(id,path)=>({id,name:"同名项目",path_with_namespace:path});
  api.projectPage=data=>data.search==="other" ? {projects:[project(data.page+10,`other/project-${data.page}`)],next_page:data.page===1 ? 2 : 0} : {projects:[project(1,"team/project")],next_page:2};
  const ui=await launch(api);await openGitLab(ui);
  assert.equal(api.calls.some(c=>c.path==="/v1/platform/gitlab/projects"),false);
  await expandDisclosure(ui.el("gitlab-project-picker"));
  assert.equal(ui.el("gitlab-project-options").children.length,1);
  ui.el("gitlab-project-search").value="other";await ui.el("gitlab-project-search").emit("input");
  assert.equal(ui.el("gitlab-project-options").children.length,0);
  await ui.clock.advance(299);
  assert.equal(api.calls.filter(c=>c.path==="/v1/platform/gitlab/projects").length,1);
  await ui.clock.advance(1);
  const first=api.calls.filter(c=>c.path==="/v1/platform/gitlab/projects").at(-1).data;
  assert.equal(first.search,"other");assert.equal(first.page,1);assert.equal(first.connection_id,"a".repeat(32));
  assert.equal(ui.el("gitlab-project-more").hidden,false);
  await ui.el("gitlab-project-more").emit("click");
  assert.equal(api.calls.filter(c=>c.path==="/v1/platform/gitlab/projects").at(-1).data.page,2);
  assert.equal(ui.el("gitlab-project-options").children.length,2);assert.equal(ui.el("gitlab-project-more").hidden,true);
  const input=ui.el("gitlab-project-options").children[1].children[0];input.checked=true;await input.emit("change");
  assert.equal(ui.el("gitlab-project-summary").textContent,"other/project-2");assert.equal(ui.el("gitlab-project-picker").open,false);
  assert.equal(gitlabWrites(api).length,0);
  await ui.el("gitlab-save").emit("click");assert.equal(gitlabWrites(api)[0].data.config.events.project,"other/project-2");
});

test("GitLab project picker discards late search, connection and room responses",async()=>{
  const {api}=todosServer({enabled:true});let finish;
  const project=path=>({id:7,name:"Project",path_with_namespace:path});
  api.projectPage=data=>data.search==="new" ? {projects:[project("new/project")],next_page:0} : new Promise(resolve=>{finish=resolve;});
  const ui=await launch(api);await openGitLab(ui);await expandDisclosure(ui.el("gitlab-project-picker"));
  const old=finish;
  ui.el("gitlab-project-search").value="new";await ui.el("gitlab-project-search").emit("input");await ui.clock.advance(300);
  old({projects:[project("old/project")],next_page:2});await settle();
  assert.equal(ui.el("gitlab-project-options").children[0].children[0].value,"new/project");
  api.projectPage=()=>new Promise(resolve=>{finish=resolve;});
  const pending=ui.el("gitlab-project-refresh").emit("click");await settle();const previous=finish;
  api.projectPage=()=>({projects:[project("other/project")],next_page:0});
  await editGitLab(ui,"gitlab-connection","b".repeat(32));await settle();
  previous({projects:[project("stale/project")],next_page:2});await pending;
  assert.equal(ui.el("gitlab-project-search").value,"");assert.equal(ui.el("gitlab-project-options").children[0].children[0].value,"other/project");
  api.projectPage=()=>new Promise(resolve=>{finish=resolve;});
  const late=ui.el("gitlab-project-refresh").emit("click");await settle();const roomResponse=finish;
  await ui.select("B");await openGitLab(ui);
  roomResponse({projects:[project("room-a/project")],next_page:0});await late;
  assert.equal(ui.el("gitlab-project-options").children.length,0);
});

test("GitLab project picker retains selection on failure and exposes refresh and empty results",async()=>{
  const {api}=todosServer({enabled:true});api.projectPage=()=>Promise.reject(Error("offline"));
  const ui=await launch(api);await openGitLab(ui);await expandDisclosure(ui.el("gitlab-project-picker"));
  assert.match(ui.el("gitlab-project-status").textContent,/读取失败/);assert.equal(ui.el("gitlab-project-summary").textContent,"group/project-A");
  assert.equal(ui.el("gitlab-project-refresh").disabled,false);
  api.projectPage=()=>({projects:[],next_page:0});await ui.el("gitlab-project-refresh").emit("click");
  assert.match(ui.el("gitlab-project-status").textContent,/没有匹配/);assert.equal(gitlabWrites(api).length,0);
  await editGitLab(ui,"gitlab-connection","");
  assert.equal(ui.el("gitlab-project-summary").textContent,"请先选择平台连接");
  assert.equal(ui.el("gitlab-project-summary").attributes["aria-disabled"],"true");
  assert.equal(ui.el("gitlab-project-hint").hidden,false);assert.match(ui.el("gitlab-project-hint").textContent,/在上方选择平台连接/);
  api.connections[0].validation.status="unverified";
  await openGitLab(ui);
  await editGitLab(ui,"gitlab-connection","a".repeat(32));
  assert.equal(ui.el("gitlab-project-summary").textContent,"请先验证平台连接");
  assert.match(ui.el("gitlab-project-hint").textContent,/管理连接/);
  api.connections[0].validation.status="valid";
  await openGitLab(ui);
  await editGitLab(ui,"gitlab-connection","a".repeat(32));await settle();
  assert.equal(ui.el("gitlab-project-hint").hidden,true);assert.equal(ui.el("gitlab-project-summary").textContent,"group/project-A");
});

function freshGitLabServer() {
 const result=todosServer();
 Object.assign(result.events.get("A"),{subscription_id:"",subscription_version:0,status:"disabled"});
 result.events.get("A").config.enabled=false;
 return result;
}
function gitlabLocalTime(date) {
 const parts=Object.fromEntries(new Intl.DateTimeFormat("en-CA",{timeZone:"Asia/Shanghai",year:"numeric",month:"2-digit",day:"2-digit",hour:"2-digit",minute:"2-digit",second:"2-digit",hourCycle:"h23"}).formatToParts(date).map(p=>[p.type,p.value]));
 return `${parts.year}-${parts.month}-${parts.day}T${parts.hour}:${parts.minute}:${parts.second}`;
}
test("GitLab custom reception start survives drafts and reload and is shared by both channels",async()=>{
 const {api,events,todos}=freshGitLabServer(),ui=await launch(api);await openGitLab(ui);
 assert.equal(ui.el("gitlab-backfill").disabled,false);assert.equal(ui.el("gitlab-backfill").value,"0");
 await toggleGitLab(ui,"gitlab-events-enabled",true);await toggleGitLab(ui,"gitlab-todos-enabled",true);
 await editGitLab(ui,"gitlab-backfill","custom");
 assert.equal(ui.el("gitlab-backfill-custom").hidden,false);assert.equal(ui.el("gitlab-save").disabled,true);
 assert.match(ui.el("gitlab-backfill-error").textContent,/请选择/);
 await ui.refresh();assert.equal(ui.el("gitlab-backfill").value,"custom");assert.equal(ui.el("gitlab-save").disabled,true);
 const start=new Date(Math.floor((Date.now()-7*86400000)/1000)*1000),local=gitlabLocalTime(start);
 await editGitLab(ui,"gitlab-backfill-start",local,"input");
 await ui.refresh();assert.equal(ui.el("gitlab-backfill-start").value,local);
 await ui.el("notification-tab-messages").emit("click");await ui.el("notification-tab-gitlab").emit("click");
 assert.equal(ui.el("gitlab-backfill-start").value,local);assert.equal(ui.el("gitlab-save").disabled,false);
 await ui.el("gitlab-save").emit("click");
 assert.equal(gitlabWrites(api)[0].data.config.backfill_start,start.toISOString());
 for(const source of [events,todos])assert.equal(source.get("A").config.backfill_start,start.toISOString());
 assert.equal(ui.el("gitlab-backfill").disabled,true);assert.equal(ui.el("gitlab-backfill").hidden,true);
 assert.equal(ui.el("gitlab-backfill-locked").hidden,false);assert.equal(ui.el("gitlab-save").disabled,true);
 const reloaded=await launch(api);await openGitLab(reloaded);
 assert.equal(reloaded.el("gitlab-backfill-start").value,local);assert.equal(reloaded.el("gitlab-backfill").disabled,true);
 await editGitLab(reloaded,"gitlab-interval","10","input");await reloaded.el("gitlab-save").emit("click");
 assert.equal(gitlabWrites(api)[1].data.config.backfill_start,start.toISOString());
});
for(const seconds of [0,3600,86400])test(`GitLab reception preset ${seconds} freezes and is retained on later channel enable`,async()=>{
 const {api,todos}=freshGitLabServer(),ui=await launch(api);await openGitLab(ui);
 await toggleGitLab(ui,"gitlab-events-enabled",true);
 await editGitLab(ui,"gitlab-backfill",String(seconds));await ui.el("gitlab-save").emit("click");
 const submitted=gitlabWrites(api)[0].data.config;
 assert.equal(submitted.backfill_seconds,seconds);assert.equal(submitted.backfill_start,undefined);
 const saved=api.gitlabRecords.get("A").config,start=saved.backfill_start;
 assert.ok(Math.abs(Date.now()-Date.parse(start)-seconds*1000)<2000);
 assert.equal(ui.el("gitlab-backfill").disabled,true);
 await toggleGitLab(ui,"gitlab-todos-enabled",true);await ui.el("gitlab-save").emit("click");
 assert.equal(gitlabWrites(api)[1].data.config.backfill_start,start);assert.equal(todos.get("A").config.backfill_start,start);
});
test("GitLab invalid custom times block saves and returning to the default clears the timestamp",async()=>{
 const {api}=freshGitLabServer(),ui=await launch(api);await openGitLab(ui);
 await toggleGitLab(ui,"gitlab-todos-enabled",true);
 await editGitLab(ui,"gitlab-backfill","custom");
 await editGitLab(ui,"gitlab-backfill-start",gitlabLocalTime(new Date(Date.now()+86400000)),"input");
 assert.equal(ui.el("gitlab-save").disabled,true);assert.match(ui.el("gitlab-backfill-error").textContent,/不能晚于/);
 await ui.el("gitlab-save").emit("click");assert.equal(gitlabWrites(api).length,0);
 await editGitLab(ui,"gitlab-backfill","0");
 assert.equal(ui.el("gitlab-backfill-custom").hidden,true);assert.equal(ui.el("gitlab-backfill-error").hidden,true);
 await ui.el("gitlab-save").emit("click");
 assert.equal(gitlabWrites(api)[0].data.config.backfill_start,undefined);
});
