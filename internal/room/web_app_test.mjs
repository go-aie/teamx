const eventView = value => ({ provider: "gitlab", channel: "events", subscription_id: value.subscription_version ? "a".repeat(32) : "", ...structuredClone(value) });
const todoView = value => ({ provider: "gitlab", channel: "todos", subscription_id: "", subscription_version: 0, available: true, status: "disabled", pending: 0, metrics: {},
  config: { enabled: false, connection_id: "", project_scope: "all", interval_seconds: 30, inactive_interval_seconds: 120, filter: {} }, ...structuredClone(value) });
const notificationList = value => ({ subscriptions: [eventView(value), todoView()] });
import { test } from "node:test";
import assert from "node:assert/strict";
import * as client from "./web/client.mjs";
import { launch, settle } from "./web_app_harness.mjs";

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
  const format = value => "运行中 · " + new Date(value).toLocaleString("zh-CN", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
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
  rail.getBoundingClientRect = () => ({ width: Number.parseFloat(workspace.style.getPropertyValue("--members-open-width")) || rail.clientWidth });
  const calls = api.calls.length;
  await handle.emit("pointerdown", { button: 0, pointerId: 1, clientX: 1000 });
  assert.equal(handle.hasPointerCapture(1), true); assert.equal(workspace.classList.contains("resizing-members"), true);
  await handle.emit("pointermove", { pointerId: 2, clientX: 0 });
  assert.equal(workspace.style.getPropertyValue("--members-open-width"), "");
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
  assert.equal(workspace.style.getPropertyValue("--members-open-width"), "340px");
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


test("GitLab settings use their own version and preserve drafts; uncertain saves never replay", async () => {
  const api = server([makeRoom("A")]);
  let events = { available: true, subscription_version: 0, config: { enabled: false, interval_seconds: 5, filter: {} }, status: "disabled", pending: 0, metrics: { calls: 12, response_bytes: 300 } };
  let loseResponse = false;
  api.hook = (path, data) => {
    if (path.startsWith("/v1/platform/notifications?")) return notificationList(structuredClone(events));
    if (path === "/v1/platform/notifications/configure") {
      assert.equal(data.service_epoch, "manager"); assert.equal(data.room_instance, "instance-A"); assert.equal(data.caller.kind, "human");
      assert.equal(data.subscription_version, events.subscription_version);
      events = { ...events, config: data.config, subscription_version: events.subscription_version + 1, status: data.config.enabled ? "running" : "disabled", pending: 3 };
      if (loseResponse) return Promise.reject(new Error("response lost after save"));
      return eventView(events);
    }
    if (path === "/v1/platform/notifications/sync") { assert.equal(data.subscription_version, events.subscription_version); return eventView(events); }
  };
  const ui = await launch(api);
  await ui.el("open-gitlab-source").emit("click"); await settle();
  ui.el("gitlab-events-connection").value = "a".repeat(32); await ui.el("gitlab-events-connection").emit("change");
  ui.el("gitlab-events-project").value = "group/project"; await ui.el("gitlab-events-project").emit("input");
  assert.equal(ui.el("gitlab-events").hidden, false);
  ui.el("gitlab-events-enabled").checked = true;
  ui.el("gitlab-events-interval").value = "3";
  ui.el("gitlab-events-branches").value = "main\nrelease";
  ui.el("gitlab-events-ignore").value = "bot";
  for (const type of ["opened", "merged", "closed", "note", "push"]) ui.el(`gitlab-events-type-${type}`).checked = type === "opened";
  await ui.refresh();
  assert.equal(ui.el("gitlab-events-interval").value, "3", "polling preserves unsaved edits");
  await ui.el("gitlab-events-save").emit("click");
  assert.deepEqual(events.config.filter, { types: ["merge_request.opened"], branches: ["main", "release"], ignore_users: ["bot"] });
  assert.equal(ui.el("gitlab-events-sync").disabled, false);
  assert.match(ui.el("gitlab-events-status").textContent, /待投递 3/);
  assert.match(ui.el("gitlab-events-status").textContent, /完整补查 尚无/);
  await ui.el("gitlab-events-sync").emit("click");
  loseResponse = true;
  ui.el("gitlab-events-enabled").checked = false;
  await ui.el("gitlab-events-enabled").emit("change");
  await ui.el("gitlab-events-save").emit("click");
  await ui.refresh(); await ui.refresh();
  assert.equal(api.calls.filter(call => call.path === "/v1/platform/notifications/configure").length, 2);
  assert.equal(events.config.enabled, false);
  assert.equal(ui.el("gitlab-events-sync").disabled, true);
  assert.match(ui.el("gitlab-events-error").textContent, /不会自动重发/);
  assert.equal(api.calls.filter(call => call.path === "/v1/workspace/apply").length, 0);
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

test("GitLab activity checkboxes preserve explicit choices and never save an empty choice as all", async () => {
  const api = server([makeRoom("A")]);
  let events = { available: true, subscription_version: 1, config: { enabled: true, interval_seconds: 5, filter: {} }, status: "running", pending: 0, metrics: { calls: 1, response_bytes: 100 } };
  api.hook = (path, data) => {
    if (path.startsWith("/v1/platform/notifications?")) return notificationList(structuredClone(events));
    if (path === "/v1/platform/notifications/configure") {
      events = { ...events, config: data.config, subscription_version: events.subscription_version + 1 };
      return eventView(events);
    }
  };
  const ui = await launch(api);
  const types = ["opened", "merged", "closed", "note", "push"].map(type => ui.el(`gitlab-events-type-${type}`));
  assert.ok(types.every(input => input.checked), "empty API filter visibly selects all types");
  assert.equal(ui.el("gitlab-events-save").textContent, "已保存");
  assert.equal(ui.el("gitlab-events-save").disabled, true);
  ui.el("gitlab-events-all-types").checked = false;
  await ui.el("gitlab-events-all-types").emit("change");
  assert.ok(types.every(input => !input.checked));
  assert.match(ui.el("gitlab-events-feedback").textContent, /至少选择/);
  await ui.el("gitlab-events-save").emit("click");
  assert.equal(api.calls.filter(call => call.path.endsWith("/configure")).length, 0);
  ui.el("gitlab-events-type-note").checked = true;
  await ui.el("gitlab-events-type-note").emit("change");
  assert.equal(ui.el("gitlab-events-all-types").indeterminate, true);
  assert.equal(ui.el("gitlab-events-save").textContent, "保存更改");
  assert.equal(ui.el("gitlab-events-sync").disabled, true, "sync cannot silently ignore an unsaved filter");
  await ui.refresh();
  assert.deepEqual(types.map(input => input.checked), [false, false, false, true, false]);
  await ui.el("gitlab-events-save").emit("click");
  assert.deepEqual(events.config.filter.types, ["note"]);
  assert.equal(ui.el("gitlab-events-save").disabled, true);
  assert.equal(ui.el("gitlab-events-sync").disabled, false);
  assert.match(ui.el("gitlab-events-feedback").textContent, /设置已保存/);
  ui.el("gitlab-events-all-types").checked = true;
  await ui.el("gitlab-events-all-types").emit("change");
  await ui.el("gitlab-events-save").emit("click");
  assert.deepEqual(events.config.filter.types, [], "all choices retain the existing server contract");
  assert.ok(types.every(input => input.checked));
});

test("GitLab actions distinguish start, save, stop, sync and retry with request feedback", async () => {
  const api = server([makeRoom("A")]);
  let events = { available: true, subscription_version: 0, config: { enabled: false, interval_seconds: 5, filter: {} }, status: "disabled", pending: 0, metrics: { calls: 0, response_bytes: 0 } };
  let releaseSave, releaseSync;
  api.hook = (path, data) => {
    if (path.startsWith("/v1/platform/notifications?")) return notificationList(structuredClone(events));
    if (path === "/v1/platform/notifications/configure") return new Promise(resolve => {
      releaseSave = () => { events = { ...events, config: data.config, subscription_version: events.subscription_version + 1, status: data.config.enabled ? "running" : "disabled" }; resolve(eventView(events)); };
    });
    if (path === "/v1/platform/notifications/sync") return new Promise(resolve => { releaseSync = () => resolve(eventView(events)); });
  };
  const ui = await launch(api);
  await ui.el("open-gitlab-source").emit("click"); await settle();
  ui.el("gitlab-events-connection").value = "a".repeat(32); await ui.el("gitlab-events-connection").emit("change");
  ui.el("gitlab-events-project").value = "group/project"; await ui.el("gitlab-events-project").emit("input");
  assert.equal(ui.el("gitlab-events-sync").hidden, true);
  ui.el("gitlab-events-enabled").checked = true;
  await ui.el("gitlab-events-enabled").emit("change");
  assert.equal(ui.el("gitlab-events-save").textContent, "开启监听");
  assert.match(ui.el("gitlab-events-feedback").textContent, /未保存/);
  const saving = ui.el("gitlab-events-save").emit("click"); await settle();
  assert.equal(ui.el("gitlab-events-save").textContent, "保存中…");
  assert.equal(ui.el("gitlab-events-fields").disabled, true);
  await ui.el("gitlab-events-save").emit("click");
  assert.equal(api.calls.filter(call => call.path.endsWith("/configure")).length, 1);
  releaseSave(); await saving;
  assert.equal(ui.el("gitlab-events-sync").hidden, false);
  assert.equal(ui.el("gitlab-events-sync-label").textContent, "立即同步");
  events.status = "backoff"; events.error_code = "query_failed"; await ui.refresh();
  assert.equal(ui.el("gitlab-events-sync-label").textContent, "重试同步");
  const syncing = ui.el("gitlab-events-sync").emit("click"); await settle();
  assert.equal(ui.el("gitlab-events-sync-label").textContent, "请求中…");
  assert.equal(ui.el("gitlab-events-save").disabled, true);
  releaseSync(); await syncing;
  assert.match(ui.el("gitlab-events-feedback").textContent, /已发起同步/);
  assert.doesNotMatch(ui.el("gitlab-events-feedback").textContent, /同步完成/);
  ui.el("gitlab-events-interval").value = "6";
  await ui.el("gitlab-events-interval").emit("input");
  await ui.el("gitlab-events-sync").emit("click");
  assert.equal(api.calls.filter(call => call.path.endsWith("/sync")).length, 1);
  ui.el("gitlab-events-enabled").checked = false;
  await ui.el("gitlab-events-enabled").emit("change");
  assert.equal(ui.el("gitlab-events-save").textContent, "关闭监听");
  const stopping = ui.el("gitlab-events-save").emit("click"); await settle(); releaseSave(); await stopping;
  assert.equal(ui.el("gitlab-events-sync").hidden, true);
  assert.equal(ui.el("gitlab-events-state").textContent, "已关闭");
  assert.equal(ui.el("gitlab-events-feedback").textContent, "监听已关闭");
});

test("late GitLab save response stays with its original room after switching", async () => {
  const api = server([makeRoom("A"), makeRoom("B")]);
  const view = (version, interval) => ({ available: true, subscription_version: version, config: { enabled: true, interval_seconds: interval, filter: {} }, status: "running", pending: 0, metrics: { calls: 0, response_bytes: 0 } });
  let release;
  api.hook = (path, data) => {
    if (path.startsWith("/v1/platform/notifications?")) return notificationList(new URL(path, "http://fixture").searchParams.get("room_id") === "A" ? view(1, 5) : view(8, 17));
    if (path === "/v1/platform/notifications/configure") return new Promise(resolve => { release = resolve; });
  };
  const ui = await launch(api);
  ui.el("gitlab-events-interval").value = "3";
  await ui.el("gitlab-events-interval").emit("input");
  const saving = ui.el("gitlab-events-save").emit("click"); await settle();
  await ui.select("B"); await ui.refresh();
  release(view(2, 3)); await saving;
  assert.equal(String(ui.el("gitlab-events-interval").value), "17");
  assert.equal(api.calls.filter(call => call.path === "/v1/platform/notifications/configure").length, 1);
  assert.equal(api.calls.find(call => call.path === "/v1/platform/notifications/configure").data.room_instance, "instance-A");
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
  finishSave({ id: "a".repeat(32), name: "Confirmed GitLab", provider: "gitlab", url: "https://gitlab.invalid", version: 1, credential_kind: "oauth", credential_configured: true, validation: { status: "unverified" } });
  await saving;
  assert.equal(ui.el("connection-form").hidden, true);
  assert.equal(ui.el("connections-list-view").hidden, false);
  assert.equal(ui.el("connections-list").children.length, 1);

  assert.equal(ui.el("connections-list").children.length, 1);
  assert.equal(ui.el("connections-empty").hidden, true);
  assert.equal(ui.el("connections-list-view").attributes["aria-busy"], "false");
  assert.equal(ui.el("connections-feedback").textContent, "连接已保存。");
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

test("notification source shares the rail, preserves edits through back, collapse and binding navigation, and saves without Workspace apply", async () => {
  const { api, events } = notificationServer(), ui = await launch(api);
  const source = ui.el("open-gitlab-source"), panel = ui.el("notification-detail"), list = ui.el("member-list-view");
  assert.equal(ui.el("notification-sources").hidden, false);
  assert.equal(ui.el("notification-source-count").textContent, "2");
  assert.equal(ui.el("notification-source-state").textContent, "运行中");
  assert.equal(String(ui.el("member-count").textContent), "0");
  await source.emit("click"); await settle();
  assert.equal(panel.open, true); assert.equal(panel.modal, false); assert.equal(list.hidden, true); assert.equal(list.inert, true);
  assert.match(ui.el("notification-connection").textContent, /Company GitLab/);
  assert.match(ui.el("notification-project").textContent, /group\/project-A · #7/);
  ui.el("gitlab-events-branches").value = "main"; await ui.el("gitlab-events-branches").emit("input");
  await ui.el("close-notification").emit("click");
  assert.equal(list.hidden, false); assert.equal(list.inert, false);
  await source.emit("click"); await ui.refresh();
  assert.equal(ui.el("gitlab-events-branches").value, "main");
  assert.match(ui.el("gitlab-events-feedback").textContent, /未保存/);
  await ui.el("notification-manage-binding").emit("click");
  assert.equal(ui.el("workspace-dialog").open, false);
  assert.equal(ui.el("gitlab-events-project").value, "group/project-A");
  assert.equal(panel.open, true); assert.equal(ui.el("gitlab-events-branches").value, "main");
  await ui.el("toggle-members").emit("click");
  assert.equal(panel.open, false); assert.equal(ui.el("member-rail").inert, true);
  await ui.el("toggle-members").emit("click"); await source.emit("click");
  assert.equal(ui.el("gitlab-events-branches").value, "main");
  await ui.el("gitlab-events-save").emit("click");
  assert.deepEqual(events.get("A").config.filter.branches, ["main"]);
  assert.equal(api.calls.filter(c => c.path === "/v1/workspace/apply" || c.path === "/v1/member/activity" || c.path === "/v1/member/run").length, 0);
  await panel.emit("keydown", { key: "Escape" }); assert.equal(list.hidden, false);
});

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

test("notification details close on room switch and ignore a late save while retaining B settings", async () => {
  const { api } = notificationServer(), baseHook = api.hook;
  let release;
  api.hook = (path, data) => path === "/v1/platform/notifications/configure" ? new Promise(resolve => { release = resolve; }) : baseHook(path, data);
  const ui = await launch(api);
  await ui.el("open-gitlab-source").emit("click");
  ui.el("gitlab-events-interval").value = "3"; await ui.el("gitlab-events-interval").emit("input");
  const saving = ui.el("gitlab-events-save").emit("click"); await settle();
  await ui.el("close-notification").emit("click"); await ui.el("open-gitlab-source").emit("click");
  assert.equal(ui.el("gitlab-events-save").textContent, "保存中…");
  await ui.select("B"); assert.equal(ui.el("notification-detail").open, false);
  await ui.el("open-gitlab-source").emit("click");
  release({ available: true, subscription_version: 99, config: { enabled: true, interval_seconds: 3, filter: {} }, status: "running", metrics: {} }); await saving;
  assert.equal(String(ui.el("gitlab-events-interval").value), "17");
  assert.match(ui.el("notification-project").textContent, /project-B/);
  assert.equal(api.calls.filter(c => c.path === "/v1/platform/notifications/configure").length, 1);
});

test("notification source handles missing binding and reconnect without replaying mutations", async () => {
  const { api } = notificationServer(), baseHook = api.hook;
  let offline = false;
  api.hook = (path, data) => path.startsWith("/v1/platform/notifications?") && offline ? Promise.reject(new Error("offline")) : baseHook(path, data);
  const ui = await launch(api);
  await ui.el("open-gitlab-source").emit("click");
  ui.el("gitlab-events-ignore").value = "bot"; await ui.el("gitlab-events-ignore").emit("input");
  offline = true; await ui.refresh();
  assert.equal(ui.el("gitlab-events-save").disabled, true); assert.equal(ui.el("notification-reload").hidden, false);
  assert.match(ui.el("notification-notice").textContent, /暂不可读/);
  offline = false; await ui.el("notification-reload").emit("click");
  assert.equal(ui.el("gitlab-events-ignore").value, "bot"); assert.equal(ui.el("gitlab-events-save").disabled, false);
  assert.equal(api.calls.filter(c => c.path === "/v1/platform/notifications/configure").length, 0);
  const unbound = server([makeRoom("A")]);
  unbound.history.set("A", [{ ...message(1, "unbound notification"), author: { kind: "integration", id: "gitlab" }, source: { provider: "gitlab", channel: "events" } }]);
  const other = await launch(unbound);
  assert.equal(other.el("notification-sources").hidden, false);
  await other.document.getElementById("message-1").querySelector(".avatar").emit("click");
  assert.equal(other.el("notification-detail").open, true); assert.equal(other.el("gitlab-events").hidden, false);
  assert.equal(other.el("notification-notice").hidden, true);
  assert.equal(other.el("notification-manage-binding").disabled, true);
});

function todosServer({ bound = true, enabled = false } = {}) {
  const { api, events } = notificationServer();
  api.connections[0].validation = { status: "valid", user_id: 101, username: "alice" };
  api.connections.push({ ...structuredClone(api.connections[0]), id: "b".repeat(32), name: "Personal", validation: { status: "valid", user_id: 202, username: "bob" } });
  const todos = new Map(["A", "B"].map(id => [id, todoView({ subscription_id: enabled ? (id === "A" ? "c" : "d").repeat(32) : "", subscription_version: enabled ? 3 : 0,
    recipient_id: enabled ? 101 : undefined, username: enabled ? "alice" : undefined, instance: enabled ? "https://gitlab.invalid" : undefined,
    config: { enabled, connection_id: enabled ? api.connections[0].id : "", project_scope: "all", interval_seconds: id === "A" ? 30 : 45, inactive_interval_seconds: 120, filter: {} }, status: enabled ? "running" : "disabled" })]));
  if (!bound) for (const ws of api.workspaces.values()) { delete ws.workspace.binding; delete ws.workspace.repository; }
  const eventHook = api.hook;
  api.hook = (path, data) => {
    if (path.startsWith("/v1/platform/notifications?")) {
      const room = new URL(path, "http://fixture").searchParams.get("room_id");
      return { subscriptions: [eventView(events.get(room)), structuredClone(todos.get(room))], retired_subscriptions: [todoView({ subscription_id: "f".repeat(32), retired: true })] };
    }
    if (data?.channel === "todos" && ["/v1/platform/notifications/configure", "/v1/platform/notifications/sync"].includes(path)) {
      const saved = todos.get(data.room_id);
      assert.equal(data.subscription_id, saved.subscription_id); assert.equal(data.subscription_version, saved.subscription_version);
      assert.equal(data.room_instance, `instance-${data.room_id}`); assert.equal(data.service_epoch, "manager"); assert.equal(data.caller.id, "tester");
      if (data.config) {
        const previous = saved.config;
        saved.config = structuredClone(data.config);
        if (saved.config.enabled) {
          const connection = api.connections.find(item => item.id === saved.config.connection_id);
          Object.assign(saved, { subscription_id: connection?.validation.user_id === 202 ? "e".repeat(32) : "c".repeat(32), recipient_id: connection?.validation.user_id, username: connection?.validation.username, instance: connection?.url });
          saved.project = saved.config.project_scope === "current" ? structuredClone(api.workspaces.get(data.room_id).workspace.binding) : undefined;
        } else {
          saved.config.connection_id = previous.connection_id; saved.config.project_scope = previous.project_scope;
        }
      } else assert.equal(data.config, undefined);
      saved.subscription_version++; saved.status = saved.config.enabled ? "running" : "disabled"; delete saved.error_code;
      return structuredClone(saved);
    }
    return eventHook(path, data);
  };
  return { api, events, todos };
}
async function openTodos(ui) {
  await ui.el("open-gitlab-source").emit("click"); await settle();
  await ui.el("notification-tab-todos").emit("click");
}
async function selectTodoConnection(ui, id = "a".repeat(32)) {
  ui.el("gitlab-todos-connection").value = id; await ui.el("gitlab-todos-connection").emit("change");
}

test("GT-03 unbound room exposes both channels and connection management without a personal connection", async () => {
  const { api } = todosServer({ bound: false }); api.connections = [];
  const ui = await launch(api); await openTodos(ui);
  assert.equal(ui.el("notification-sources").hidden, false);
  assert.equal(ui.el("notification-panel-events").hidden, true); assert.equal(ui.el("notification-panel-todos").hidden, false);
  assert.match(ui.el("notification-summary").textContent, /项目活动：运行中 · 我的待办：已关闭/);
  assert.equal(ui.el("gitlab-todos-save").disabled, true);
  assert.equal(ui.el("gitlab-todos-scope").value, "all"); assert.equal(ui.el("gitlab-todos-scope").children[0].disabled, true);
  await ui.el("gitlab-todos-manage-connections").emit("click");
  assert.equal(ui.el("settings-dialog").open, true); assert.equal(ui.el("notification-detail").open, true);
  api.connections = [{ id: "a".repeat(32), name: "Verified", provider: "gitlab", url: "https://gitlab.invalid", validation: { status: "valid", user_id: 101, username: "alice" } }];
  await ui.el("close-settings").emit("click"); await settle(); await selectTodoConnection(ui);
  assert.match(ui.el("gitlab-todos-recipient").textContent, /@alice（ID 101，已验证）/);
  assert.equal(ui.el("gitlab-todos-scope").value, "all");
  ui.el("gitlab-todos-enabled").checked = true; await ui.el("gitlab-todos-enabled").emit("change");
  await ui.el("gitlab-todos-save").emit("click");
  const call = api.calls.find(call => call.path.endsWith("/configure"));
  assert.equal(call.data.channel, "todos"); assert.equal(call.data.subscription_id, ""); assert.equal(call.data.subscription_version, 0);
  assert.equal(call.data.config.project_scope, "all");
  assert.equal(api.calls.some(call => /workspace\/apply|member\/add|member\/restart/.test(call.path)), false);
  assert.equal(JSON.stringify([...ui.saved]).includes("alice"), false);
});

test("GT-03 project scope defaults to the selected instance and old identity needs revalidation", async () => {
  const { api } = todosServer(); const ui = await launch(api); await openTodos(ui); await selectTodoConnection(ui);
  assert.equal(ui.el("gitlab-todos-scope").value, "current");
  assert.match(ui.el("gitlab-todos-project").textContent, /project-A · #7/);
  api.connections[1].url = "https://other.invalid";
  await ui.el("close-notification").emit("click"); await openTodos(ui); await selectTodoConnection(ui, "b".repeat(32));
  assert.equal(ui.el("gitlab-todos-scope").value, "all"); assert.equal(ui.el("gitlab-todos-scope").children[0].disabled, true);
  api.connections[1].validation = { status: "valid", username: "legacy" };
  await ui.el("close-notification").emit("click"); await openTodos(ui);
  ui.el("gitlab-todos-enabled").checked = true; await ui.el("gitlab-todos-enabled").emit("change");
  assert.match(ui.el("gitlab-todos-recipient").textContent, /需重新验证/); assert.equal(ui.el("gitlab-todos-save").disabled, true);
});

test("GT-03 tabs retain independent drafts, saved versions, requests and diagnostics", async () => {
  const { api, events, todos } = todosServer({ enabled: true }); const ui = await launch(api); await openTodos(ui);
  ui.el("gitlab-todos-reason-mentioned").checked = false; await ui.el("gitlab-todos-reason-mentioned").emit("change");
  await ui.el("notification-tab-events").emit("click");
  ui.el("gitlab-events-ignore").value = "bot"; await ui.el("gitlab-events-ignore").emit("input");
  await ui.el("notification-tab-todos").emit("click"); await ui.el("close-notification").emit("click"); await openTodos(ui);
  assert.equal(ui.el("gitlab-todos-reason-mentioned").checked, false); assert.equal(ui.el("gitlab-todos-sync").disabled, true);
  await ui.el("gitlab-todos-save").emit("click");
  assert.equal(todos.get("A").subscription_version, 4); assert.equal(events.get("A").subscription_version, 1);
  assert.equal(ui.el("gitlab-events-ignore").value, "bot"); assert.equal(ui.el("gitlab-events-save").disabled, false);
  await ui.el("gitlab-events-save").emit("click");
  assert.equal(events.get("A").subscription_version, 2); assert.equal(todos.get("A").subscription_version, 4);
  await ui.el("gitlab-todos-sync").emit("click");
  assert.equal(todos.get("A").subscription_version, 5);
  const sync = api.calls.find(call => call.path.endsWith("/sync")); assert.equal(sync.data.channel, "todos"); assert.equal(sync.data.subscription_version, 4); assert.equal(sync.data.config, undefined);
  todos.get("A").last_check = "2026-11-03T01:00:00Z"; todos.get("A").scan = { state: "done", next_page: 9, limited: true }; todos.get("A").error_code = "scan_incomplete";
  todos.get("A").skipped = { no_project: 3 }; await ui.refresh();
  assert.match(ui.el("gitlab-todos-status").textContent, /已完成待办第 9 页（补查未完成）/); assert.match(ui.el("gitlab-todos-status").textContent, /无项目归属 3/);
  assert.match(ui.el("gitlab-todos-error").textContent, /补查尚未完成/); assert.equal(ui.el("gitlab-events-error").hidden, true);
  assert.match(ui.el("notification-summary").textContent, /项目活动：运行中 · 我的待办：运行中/);
});

test("GT-03 default filters select all and explicit empty filters stay empty across save, sync and reload", async () => {
  const { api, todos } = todosServer({ enabled: true }); const ui = await launch(api); await openTodos(ui);
  for (const reason of ["mentioned", "assigned", "review_requested", "other"]) { assert.equal(ui.el(`gitlab-todos-reason-${reason}`).checked, true); ui.el(`gitlab-todos-reason-${reason}`).checked = false; }
  for (const type of ["merge_request", "work_item"]) { assert.equal(ui.el(`gitlab-todos-type-${type}`).checked, true); ui.el(`gitlab-todos-type-${type}`).checked = false; }
  await ui.el("gitlab-todos-reason-mentioned").emit("change"); await ui.el("gitlab-todos-save").emit("click");
  assert.deepEqual(todos.get("A").config.filter, { reasons: [], types: [] }); assert.match(ui.el("gitlab-todos-action-hint").textContent, /过滤为空/);
  await ui.el("gitlab-todos-sync").emit("click"); await ui.refresh();
  assert.equal(ui.el("gitlab-todos-reason-mentioned").checked, false);
  const reload = await launch(api); await openTodos(reload); assert.equal(reload.el("gitlab-todos-type-work_item").checked, false);
});

test("GT-03 personal all-project channel remains usable when Workspace query fails or recovery is pending", async () => {
  const { api } = todosServer({ enabled: true, bound: false });
  api.workspaceHook = () => Promise.reject(new Error("workspace offline"));
  const ui = await launch(api); await openTodos(ui);
  assert.equal(ui.el("gitlab-events-save").disabled, true); assert.equal(ui.el("gitlab-todos-sync").disabled, false);
  ui.el("gitlab-todos-interval").value = "60"; await ui.el("gitlab-todos-interval").emit("input");
  await ui.el("gitlab-todos-save").emit("click"); assert.match(ui.el("gitlab-todos-feedback").textContent, /设置已保存/);
  assert.equal(api.calls.filter(call => call.path.endsWith("/configure")).length, 1);
});

test("GT-03 changed project requires explicit review and save even when config fields are unchanged", async () => {
  const { api, todos } = todosServer({ enabled: true });
  const saved = todos.get("A"); saved.config.project_scope = "current"; saved.project = structuredClone(api.workspaces.get("A").workspace.binding);
  const ui = await launch(api); await openTodos(ui);
  assert.equal(ui.el("gitlab-todos-sync").disabled, false);
  Object.assign(api.workspaces.get("A").workspace.binding, { project: "group/new-project", project_id: 99 });
  await ui.refresh();
  assert.equal(ui.el("gitlab-todos-sync").disabled, true); assert.equal(ui.el("gitlab-todos-save").textContent, "核对并保存");
  assert.match(ui.el("gitlab-todos-project").textContent, /new-project.*原订阅项目：group\/project-A/);
  await ui.el("gitlab-todos-sync").emit("click"); assert.equal(api.calls.some(call => call.path.endsWith("/sync")), false);
  await ui.el("gitlab-todos-save").emit("click");
  assert.equal(saved.project.project_id, 99); assert.equal(ui.el("gitlab-todos-sync").disabled, false);
});

test("GT-03 late save cannot overwrite a newly selected recipient or another room", async () => {
  const { api } = todosServer({ enabled: true }); const hook = api.hook; let release;
  api.hook = (path, data) => path.endsWith("/configure") && data.channel === "todos" ? new Promise(resolve => { release = () => resolve(hook(path, data)); }) : hook(path, data);
  const ui = await launch(api); await openTodos(ui);
  ui.el("gitlab-todos-interval").value = "60"; await ui.el("gitlab-todos-interval").emit("input");
  const saving = ui.el("gitlab-todos-save").emit("click"); await settle();
  await selectTodoConnection(ui, "b".repeat(32)); release(); await saving;
  assert.equal(ui.el("gitlab-todos-connection").value, "b".repeat(32)); assert.match(ui.el("gitlab-todos-recipient").textContent, /@bob/);
  assert.match(ui.el("gitlab-todos-action-hint").textContent, /停用旧账号/); assert.equal(ui.el("gitlab-todos-save").textContent, "为此账号重新开启");
  const second = ui.el("gitlab-todos-save").emit("click"); await settle();
  await ui.select("B"); release(); await second; await openTodos(ui);
  assert.equal(String(ui.el("gitlab-todos-interval").value), "45"); assert.match(ui.el("gitlab-todos-recipient").textContent, /@alice/);
  assert.equal(api.calls.filter(call => call.path.endsWith("/configure")).length, 2);
});

test("GT-03 a stale notification read cannot roll back a completed save, including account changes", async () => {
  const { api, todos } = todosServer({ enabled: true }); const ui = await launch(api); await openTodos(ui); const hook = api.hook; let release;
  api.hook = (path, data) => { if (path.startsWith("/v1/platform/notifications?")) { const old = hook(path, data); return new Promise(resolve => { release = () => resolve(old); }); } return hook(path, data); };
  const reading = ui.refresh(); await settle();
  await selectTodoConnection(ui, "b".repeat(32)); await ui.el("gitlab-todos-save").emit("click");
  assert.equal(todos.get("A").recipient_id, 202); release(); await reading;
  assert.equal(ui.el("gitlab-todos-sync").disabled, false); assert.match(ui.el("gitlab-todos-recipient").textContent, /@bob/);
  assert.match(ui.el("gitlab-todos-status").textContent, /@bob（ID 202）/); assert.equal(ui.el("gitlab-todos-save").textContent, "已保存");
});

test("GT-03 uncertain save reads back once, preserves drafts and never replays a mutation", async () => {
  const { api, todos } = todosServer({ enabled: true }); const hook = api.hook;
  api.hook = (path, data) => { if (path.endsWith("/configure") && data.channel === "todos") { hook(path, data); throw new Error("response lost"); } return hook(path, data); };
  const ui = await launch(api); await openTodos(ui); await selectTodoConnection(ui, "b".repeat(32)); await ui.el("gitlab-todos-save").emit("click");
  assert.equal(todos.get("A").recipient_id, 202); assert.match(ui.el("gitlab-todos-error").textContent, /结果待核查/);
  await ui.refresh(); await ui.el("notification-reload").emit("click");
  assert.equal(api.calls.filter(call => call.path.endsWith("/configure")).length, 1); assert.equal(ui.el("gitlab-todos-connection").value, "b".repeat(32));
  assert.equal(ui.el("gitlab-todos-sync").disabled, false);
});

test("GT-03 todo periods validate independently and disabling works after a connection disappears", async () => {
  const { api, todos } = todosServer({ enabled: true }); const ui = await launch(api); await openTodos(ui);
  ui.el("gitlab-todos-interval").value = "3601"; await ui.el("gitlab-todos-interval").emit("input"); assert.equal(ui.el("gitlab-todos-save").disabled, true);
  ui.el("gitlab-todos-interval").value = "30"; ui.el("gitlab-todos-inactive-interval").value = "29"; await ui.el("gitlab-todos-interval").emit("input"); assert.equal(ui.el("gitlab-todos-save").disabled, true);
  ui.el("gitlab-todos-inactive-interval").value = "86400"; await ui.el("gitlab-todos-inactive-interval").emit("input"); assert.equal(ui.el("gitlab-todos-save").disabled, false);
  api.connections = []; await ui.el("close-notification").emit("click"); await openTodos(ui);
  ui.el("gitlab-todos-enabled").checked = false; await ui.el("gitlab-todos-enabled").emit("change"); assert.equal(ui.el("gitlab-todos-save").disabled, false);
  await ui.el("gitlab-todos-save").emit("click"); assert.equal(todos.get("A").config.enabled, false); assert.equal(ui.el("gitlab-todos-sync").hidden, true);
});

test("GT-03 message navigation requires explicit channels and keyboard tabs preserve selection", async () => {
  const { api } = todosServer(); api.history.set("A", ["events", "todos"].map((channel, index) => ({ ...message(index + 1, "integration fact"), author: { kind: "integration", id: "gitlab" }, source: { provider: "gitlab", channel } })));
  const ui = await launch(api);
  for (let i = 1; i <= 2; i++) {
    const article = ui.document.getElementById(`message-${i}`), channel = i === 2 ? "todos" : "events";
    assert.equal(article.querySelector(".author").textContent, `GitLab · ${i === 2 ? "我的待办" : "项目活动"}`);
    await article.querySelector(".author").emit("click"); assert.equal(ui.el(`notification-tab-${channel}`).attributes["aria-selected"], "true");
    await ui.el("close-notification").emit("click"); await article.querySelector(".avatar").emit("keydown", { key: "Enter" });
    assert.equal(ui.el(`notification-panel-${channel}`).hidden, false);
  }
  await ui.el("notification-tab-todos").emit("keydown", { key: "ArrowLeft" }); assert.equal(ui.el("notification-tab-events").attributes["tabindex"], "0");
  await ui.el("notification-tab-events").emit("keydown", { key: "End" }); assert.equal(ui.el("notification-tab-messages").attributes["aria-selected"], "true");
});

test("GT-03 reverified connection with the same ID but a new account requires explicit new subscription", async () => {
  const { api } = todosServer({ enabled: true }); const ui = await launch(api); await openTodos(ui);
  api.connections[0].validation = { status: "valid", user_id: 303, username: "carol" };
  await ui.el("gitlab-todos-manage-connections").emit("click"); await ui.el("close-settings").emit("click"); await settle();
  assert.match(ui.el("gitlab-todos-recipient").textContent, /@carol/);
  assert.equal(ui.el("gitlab-todos-sync").disabled, true); assert.equal(ui.el("gitlab-todos-save").disabled, false);
  assert.equal(ui.el("gitlab-todos-save").textContent, "为此账号重新开启");
  assert.equal(api.calls.some(call => call.path.endsWith("/configure") || call.path.endsWith("/sync")), false);
  ui.el("gitlab-todos-enabled").checked = false; await ui.el("gitlab-todos-enabled").emit("change");
  await ui.el("gitlab-todos-save").emit("click");
  assert.equal(ui.el("gitlab-todos-save").disabled, true, "a disabled old identity must not remain perpetually dirty");
});

test("GT-03 both channel mutations can be in flight with separate feedback and versions", async () => {
  const { api } = todosServer({ enabled: true }); const hook = api.hook; const release = {};
  api.hook = (path, data) => path.endsWith("/configure") ? new Promise(resolve => { release[data.channel] = () => resolve(hook(path, data)); }) : hook(path, data);
  const ui = await launch(api); await openTodos(ui);
  ui.el("gitlab-todos-interval").value = "60"; await ui.el("gitlab-todos-interval").emit("input");
  const todoSave = ui.el("gitlab-todos-save").emit("click"); await settle();
  await ui.el("notification-tab-events").emit("click");
  ui.el("gitlab-events-ignore").value = "bot"; await ui.el("gitlab-events-ignore").emit("input");
  assert.equal(ui.el("gitlab-events-save").disabled, false);
  const eventSave = ui.el("gitlab-events-save").emit("click"); await settle();
  release.todos(); await todoSave;
  assert.match(ui.el("gitlab-todos-feedback").textContent, /设置已保存/); assert.equal(ui.el("gitlab-events-save").textContent, "保存中…");
  release.events(); await eventSave; assert.equal(ui.el("gitlab-events-save").textContent, "已保存");
  assert.equal(api.calls.filter(call => call.path.endsWith("/configure")).length, 2);
});

test("GT-03 account retirement followed by failed replacement is read back and only explicit save creates anew", async () => {
  const { api, todos } = todosServer({ enabled: true }); const hook = api.hook; let fail = true;
  api.hook = (path, data) => {
    if (path.endsWith("/configure") && data.channel === "todos" && fail) { todos.set("A", todoView()); fail = false; throw new Error("creation failed after retirement"); }
    return hook(path, data);
  };
  const ui = await launch(api); await openTodos(ui); await selectTodoConnection(ui, "b".repeat(32)); await ui.el("gitlab-todos-save").emit("click");
  await ui.refresh(); assert.equal(api.calls.filter(call => call.path.endsWith("/configure")).length, 1);
  assert.equal(ui.el("gitlab-todos-connection").value, "b".repeat(32)); assert.equal(ui.el("gitlab-todos-save").disabled, false);
  await ui.el("gitlab-todos-save").emit("click");
  const create = api.calls.filter(call => call.path.endsWith("/configure")).at(-1).data;
  assert.equal(create.subscription_id, ""); assert.equal(create.subscription_version, 0); assert.equal(todos.get("A").recipient_id, 202);
});

test("GT-03 late response from an earlier service epoch cannot replace current channel state", async () => {
  const { api } = todosServer({ enabled: true }); const hook = api.hook; let release;
  api.hook = (path, data) => path.endsWith("/configure") && data.channel === "todos" ? new Promise(resolve => { release = resolve; }) : hook(path, data);
  const ui = await launch(api); await openTodos(ui);
  ui.el("gitlab-todos-interval").value = "60"; await ui.el("gitlab-todos-interval").emit("input");
  const saving = ui.el("gitlab-todos-save").emit("click"); await settle();
  api.catalog.service_epoch = "new-manager";
  api.workspaceHook = path => path.startsWith("/v1/workspace?") ? structuredClone(api.workspaces.get("A")) : undefined;
  await ui.refresh();
  release(todoView({ subscription_id: "f".repeat(32), subscription_version: 99, recipient_id: 909, username: "stale", config: { enabled: true, connection_id: "a".repeat(32), project_scope: "all", interval_seconds: 90, inactive_interval_seconds: 120 } }));
  await saving; await ui.refresh();
  assert.equal(String(ui.el("gitlab-todos-interval").value), "60"); assert.doesNotMatch(ui.el("gitlab-todos-status").textContent, /stale|909/);
  assert.equal(ui.el("gitlab-todos-save").textContent, "保存更改");
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


test("Events selects a remote project without Git and Workspace prefill never changes saved target", async () => {
  const { api, events } = notificationServer();
  const saved = events.get("A");
  saved.subscription_version = 0; saved.config.enabled = false;
  delete saved.project; delete saved.config.connection_id; delete saved.config.project;
  delete api.workspaces.get("A").workspace.binding; delete api.workspaces.get("A").workspace.repository;
  const ui = await launch(api);
  await ui.el("open-gitlab-source").emit("click"); await settle();
  assert.equal(ui.el("gitlab-events").hidden, false);
  assert.equal(ui.el("notification-manage-binding").disabled, true);
  ui.el("gitlab-events-enabled").checked = true; await ui.el("gitlab-events-enabled").emit("change");
  assert.equal(ui.el("gitlab-events-save").disabled, true);
  ui.el("gitlab-events-connection").value = api.connections[0].id; await ui.el("gitlab-events-connection").emit("change");
  ui.el("gitlab-events-project").value = "remote/project"; await ui.el("gitlab-events-project").emit("input");
  assert.match(ui.el("gitlab-events-account").textContent, /alice.*101/);
  await ui.el("gitlab-events-save").emit("click");
  assert.equal(saved.config.project, "remote/project"); assert.equal(saved.config.connection_id, api.connections[0].id);
  // Later Git configuration does not retarget the saved subscription.
  api.workspaces.get("A").workspace.binding = { ...api.workspaces.get("B").workspace.binding };
  await ui.refresh();
  assert.equal(ui.el("gitlab-events-project").value, "remote/project");
  await ui.el("notification-manage-binding").emit("click");
  assert.equal(ui.el("gitlab-events-project").value, "group/project-B");
  assert.equal(saved.config.project, "remote/project");
  assert.equal(ui.el("gitlab-events-sync").disabled, true);
  assert.match(ui.el("gitlab-events-action-hint").textContent, /新建订阅/);
  await ui.refresh(); assert.equal(ui.el("gitlab-events-project").value, "group/project-B");
  assert.equal(api.calls.filter(c => c.path === "/v1/workspace/apply" || c.path === "/v1/platform/tools/save").length, 0);
});

test("Events continues editing and syncing when only the Workspace read is unavailable", async () => {
  const { api } = notificationServer(), ui = await launch(api);
  api.workspaceHook = path => path.startsWith("/v1/workspace?") ? Promise.reject(new Error("local state unavailable")) : undefined;
  await ui.refresh(); await ui.el("open-gitlab-source").emit("click"); await settle();
  assert.equal(ui.el("notification-notice").hidden, true);
  assert.equal(ui.el("gitlab-events-sync").disabled, false);
  ui.el("gitlab-events-branches").value = "independent"; await ui.el("gitlab-events-branches").emit("input");
  assert.equal(ui.el("gitlab-events-save").disabled, false);
});


test("Events offers explicit rebinding when a verified connection changes account", async () => {
  const { api, events } = notificationServer();
  Object.assign(events.get("A"), { recipient_id: 101, instance: "https://gitlab.invalid" });
  const ui = await launch(api);
  await ui.el("open-gitlab-source").emit("click"); await settle();
  assert.equal(ui.el("gitlab-events-save").disabled, true);
  api.connections[0].validation = { status: "valid", user_id: 202, username: "bob" };
  await ui.el("notification-reload").emit("click");
  assert.equal(ui.el("gitlab-events-save").disabled, false);
  assert.equal(ui.el("gitlab-events-sync").disabled, true);
  assert.match(ui.el("gitlab-events-action-hint").textContent, /新建订阅/);
  assert.equal(api.calls.filter(c => c.path === "/v1/platform/notifications/configure").length, 0);
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

test("AN-01.2 selects an exact membership in source settings without starting collaboration", async () => {
  const api = server([makeRoom("A")]);
  const member = {agent_id:"assistant",membership_id:"1".repeat(32),status:"idle",runner:{profile:"runtime"},execution:{paused:true,agent:{turn_number:0}}};
  const events = eventView({available:true,subscription_version:1,config:{enabled:true,connection_id:"a".repeat(32),project:"remote/project",interval_seconds:5,filter:{}},status:"running",pending:0,metrics:{}});
  api.hook = (path, data) => {
    if (path === "/v1/member/list") return {members:[member]};
    if (path.startsWith("/v1/platform/notifications?")) return {subscriptions:[events,todoView()]};
    if (path === "/v1/platform/notifications/configure") { events.config = structuredClone(data.config);events.subscription_version++;return structuredClone(events); }
  };
  const ui=await launch(api);
  await ui.el("open-gitlab-source").emit("click");await settle();
  const target=ui.el("gitlab-events-target"),value=JSON.stringify({mode:"inbox",agent_id:member.agent_id,membership_id:member.membership_id});
  assert.ok(target.options.some(option=>option.value===value));
  target.value=value;await target.emit("change");
  assert.equal(ui.el("gitlab-events-save").disabled,false);
  await ui.refresh();await settle();assert.equal(target.value,value);
  await ui.el("gitlab-events-save").emit("click");await settle();
  assert.deepEqual(api.calls.find(call=>call.path==="/v1/platform/notifications/configure").data.config.target,JSON.parse(value));
  assert.equal(api.calls.some(call=>call.path==="/v1/member/run"),false);
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

test("Lark Inbox separates channel, sender and conversation while retaining source evidence and attachment identity", async () => {
  const api=server([makeRoom("A")]);
  const member={agent_id:"assistant",membership_id:"1".repeat(32),status:"idle",runner:{profile:"runtime"},execution:{paused:true,agent:{turn_number:0}}};
  const common={provider:"lark",channel:"messages",instance:"https://open.feishu.cn",message_id:{namespace:"lark:message_id:cli_fixture",value:"om_saved"},receiver:{id:{namespace:"lark:open_id:cli_fixture",value:"ou_self"}},structured:true};
  const actor=(name,id,is_receiver=false)=>({name,id:{namespace:"lark:open_id:cli_fixture",value:id},is_receiver});
  const chat=name=>({name,id:{namespace:"lark:chat_id:cli_fixture",value:"oc_saved"}});
  const items=[
    {...common,sender:actor("","ou_self",true),conversation:chat(""),text:"好的"},
    {...common,sender:actor("张三","ou_peer"),conversation:chat(""),text:"![图片](lark-image:img_fixture)\n发送者：这是正文，必须保留"},
    {...common,sender:actor("","ou_123456789"),conversation:chat("研发群"),text:"收到"},
    {...common,structured:false,text:"飞书 · 旧消息\n原始正文，没有发送者元信息"},
    {...common,sender:actor("本人姓名","ou_self",true),text:"新消息"},
    {...common,sender:actor("张三","ou_peer"),changed:true,text:"更正后的正文"},
    {...common,sender:actor("雷九零","ou_peer"),conversation:chat("迭代v0.10.0"),mentions:[actor("罗彭","ou_self",true)],text:"@罗彭（open_id: ou_self；当前接收账号） 请确认"},
  ];
  api.hook=(path)=>{
    if(path==="/v1/member/list")return {members:[member]};
    if(path==="/v1/member/activity")return {member};
    if(path==="/v1/member/inbox")return {snapshot_seq:items.length,next_after:items.length,has_more:false,status:{latest_seq:items.length,bytes:100,limits:{bytes:1000}},entries:items.map((item,i)=>({seq:i+1,source:{source_ref:`src_${i}`,status:"not_checked"},received_at:"2026-11-06T08:06:57Z",item:{...item,created_at:"2026-11-06T07:00:00Z"}}))};
  };
  const ui=await launch(api);
  await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();
  await ui.el("agent-tab-inbox").emit("click");await settle();
  const rows=ui.el("inbox-entries").children;
  const part=(row,name)=>row.children.find(child=>child.className===name);
  const heading=part(rows[0],"inbox-entry-header");
  assert.equal(heading.children[0].textContent,"#1");
  assert.equal(heading.children[1].textContent,"飞书消息");
  assert.equal(heading.children[2].dateTime,"2026-11-06T07:00:00.000Z");
  assert.equal(part(rows[0],"inbox-sender").textContent,"我");
  assert.equal(part(rows[0],"inbox-chat").textContent,"会话名称未获取");
  assert.equal(part(rows[0],"message-text").textContent,"好的");
  const details=part(rows[0],"inbox-source-details");
  assert.equal(details.tag,"details");assert.equal(details.open,false);
  assert.equal(details.children[0].textContent,"来源详情");
  assert.match(elementText(details.children[1]),/ou_self/);assert.match(elementText(details.children[1]),/oc_saved/);
  assert.equal(part(rows[1],"inbox-sender").textContent,"张三");
  assert.equal(part(rows[1],"message-text").textContent,items[1].text);
  const attachment=part(rows[1],"message-text").markdownOptions.attachmentURL("lark-image:img_fixture");
  const query=new URL(attachment,"http://fixture").searchParams;
  assert.equal(query.get("seq"),"2");assert.equal(query.get("membership_id"),member.membership_id);
  assert.equal(part(rows[2],"inbox-sender").textContent,"飞书用户 · …456789");
  assert.equal(part(rows[2],"inbox-chat").textContent,"研发群");
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
 await ui.el("policy-new").emit("click");ui.el("policy-text").value="简短中文回复";
 await ui.el("policy-suggest").emit("click");await settle();
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
 assert.match(elementText(ui.el("policy-entries")),/尚未保存长期规则/);assert.equal(listCalls,0);
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
