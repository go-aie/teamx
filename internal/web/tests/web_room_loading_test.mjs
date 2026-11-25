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

test("first entry reveals the verified conversation without waiting for auxiliary configuration", async () => {
  const api = roomFixture(); api.members.splice(1);
  for (const room of api.rooms) { room.is_direct = true; room.name = "个人助理"; }
  const settings = deferred(), catalog = deferred(), members = deferred(), messages = deferred(), platforms = deferred();
  const config = await api.request("/v1/ui"), directory = await api.request("/v1/rooms");
  api.roomHook = path => path === "/v1/ui" ? settings.promise : path === "/v1/rooms" ? catalog.promise
    : path === "/v1/member/list" ? members.promise : path === "/v1/read" ? messages.promise
    : path.startsWith("/v1/platforms?") ? platforms.promise : undefined;
  let ui;
  const starting = launch(api, new Map(), {beforeStart:view=>{ ui = view; }});
  const pending = () => {
    assert.equal(ui.el("workspace").getAttribute("aria-busy"), "true");
    assert.equal(ui.el("workspace").inert, true);
    assert.equal(ui.el("app-loading").hidden, false);
    assert.equal(count(api, "/v1/human-read/confirm"), 0);
  };
  await settle(); pending();
  settings.resolve(config); await settle(); pending();
  catalog.resolve(directory); await settle(); pending();
  messages.resolve({messages:[],snapshot_seq:0,cleared_through:0,has_more:false}); await settle(); pending();
  members.resolve({members:structuredClone(api.members)}); await settle();
  assert.equal(ui.el("app-loading").hidden, true);
  assert.equal(ui.el("workspace").inert, false);
  assert.notEqual(ui.el("workspace").getAttribute("aria-busy"), "true");
  assert.equal(ui.el("room-details").hidden, true);
  assert.equal(ui.el("activity-dialog").open, true);
  assert.equal(ui.el("empty").querySelector("h2").textContent, "与个人助理聊聊");
  platforms.resolve({platforms:structuredClone(api.platforms)}); await starting;
});

test("a verified empty catalog leaves the loading state and permits creating a conversation", async () => {
  const api = roomFixture(); api.rooms = [];
  const ui = await launch(api);
  assert.equal(ui.el("app-loading").hidden, true);
  assert.equal(ui.el("workspace").inert, false);
  assert.equal(ui.el("rooms-empty").hidden, false);
  assert.equal(ui.el("new-room").disabled, false);
});

test("a failed initial read keeps the placeholder and supports an explicit read-only retry", async () => {
  const api = roomFixture(); let fail = true, view;
  api.roomHook = path => { if (fail && path === "/v1/ui") throw Error("offline"); };
  await assert.rejects(launch(api, new Map(), {beforeStart:ui=>{ view = ui; }}), /暂时无法同步/);
  assert.equal(view.el("workspace").inert, true);
  assert.equal(view.el("app-loading").hidden, false);
  assert.equal(view.el("app-loading-retry").hidden, false);
  assert.match(view.el("app-loading-status").textContent, /暂时无法加载会话/);
  fail = false;
  await view.el("app-loading-retry").emit("click"); await settle();
  assert.equal(view.el("app-loading").hidden, true);
  assert.equal(view.el("workspace").inert, false);
  assert.equal(count(api, "/v1/send"), 0);
  assert.equal(count(api, "/v1/member/run"), 0);
});

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

test("the direct settings reuse entry configuration while opening the profile", async () => {
  const api = roomFixture(); api.members.splice(1);
  for (const room of api.rooms) room.is_direct = true;
  const ui = await launch(api); api.calls.length = 0;
  await ui.select("S");
  assert.equal(ui.el("activity-dialog").open, true);
  assert.equal(ui.el("member-list-view").hidden,true);
  assert.equal(count(api, "/v1/platforms"), 0);
  assert.equal(count(api, "/v1/platform/notifications"), 1);
  assert.equal(count(api, "/v1/tools/bindings"), 1);
  assert.equal(count(api, "/v1/member/list"), 1); assert.equal(count(api, "/v1/read"), 1);

  assert.equal(ui.el("agent-tab-profile").getAttribute("aria-selected"),"true");
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

const messageText = (ui, seq) => ui.el("messages").children.find(node => node.id === `message-${seq}`)?.querySelector(".message-text").textContent;

for (const seen of [true, false]) test(`cached mentions wait for the current reading state before highlighting (seen=${seen})`, async () => {
  const api = roomFixture(), reading = deferred();
  const readingState = body => ({version:2,room_id:body.room_id,room_instance:body.room_instance,operator_id:body.caller.id,
    revision:1,snapshot_seq:1,cleared_through:0,opened_through:1,mention_seen_seqs:seen ? [1] : [],unseen_mention_seqs:seen ? [] : [1],
    unread_count:seen ? 0 : 1,mention_count:seen ? 0 : 1,...(body.include_new_messages ? {new_message_seqs:[]} : {})});
  let pending = false, input;
  api.roomHook = (path, body) => {
    if (path === "/v1/read") return {...page(body.room_id),messages:[{...page(body.room_id).messages[0],text:"@tester 请确认"}]};
    if (path === "/v1/human-read/state") {
      if (pending && body.room_id === "R") { input = body; return reading.promise; }
      return readingState(body);
    }
  };
  const ui = await launch(api); await ui.select("S"); pending = true;
  await ui.select("R");
  const node = ui.el("messages").children.find(node => node.id === "message-1");
  assert.equal(node.classList.contains("mentioned"), false, "unknown reading state must not highlight old mentions");
  assert.equal(node.querySelector(".message-text").textContent, "@tester 请确认");
  reading.resolve(readingState(input)); await settle();
  assert.equal(node.classList.contains("mentioned"), !seen, "verified unread mentions still highlight");
  assert.equal(ui.el("messages").children.find(node => node.id === "message-1"), node);
});

for (const direct of [false, true]) test(`uncached ${direct ? "direct" : "group"} entry waits for verified empty states`, async () => {
  const api = roomFixture(), messages = deferred(), members = deferred();
  for (const room of api.rooms) room.is_direct = direct;
  if (direct) api.members.splice(1);
  const ui = await launch(api);
  api.roomHook = path => path === "/v1/read" ? messages.promise : path === "/v1/member/list" ? members.promise : undefined;
  await ui.select("S");
  assert.equal(ui.el("conversation-loading").hidden, false);
  assert.equal(ui.el("empty").hidden, true);
  assert.equal(ui.el("member-empty").hidden, true);
  assert.equal(ui.el("member-loading").hidden, direct);
  assert.equal(ui.el("room-title-count").hidden, true);
  assert.equal(messageText(ui, 1), undefined, "never display another Room's messages while loading");
  messages.resolve({messages:[],snapshot_seq:0,cleared_through:0,has_more:false}); await settle();
  assert.equal(ui.el("conversation-loading").hidden, true);
  assert.equal(ui.el("empty").hidden, false, "only a verified empty read shows the welcome view");
  assert.equal(ui.el("member-empty").hidden, true);
  assert.equal(ui.el("send").disabled, true);
  members.resolve({members:[]}); await settle();
  assert.equal(ui.el("member-loading").hidden, true);
  assert.equal(ui.el("member-empty").hidden, false);
});

test("preview revalidation retains unchanged message bodies without replaying entry animations", async () => {
  const api = roomFixture(), ui = await launch(api), messages = deferred();
  await ui.select("S");
  api.roomHook = path => path === "/v1/read" ? messages.promise : undefined;
  await ui.select("R");
  await ui.draft("Draft R");
  const node = ui.el("messages").children.find(node => node.id === "message-1");
  const body = node.querySelector(".message-text");
  assert.equal(node.classList.contains("message-history"), true);
  assert.equal(ui.el("conversation-loading").hidden, true);
  messages.resolve(page("R")); await settle();
  assert.equal(ui.el("messages").children.find(node => node.id === "message-1"), node);
  assert.equal(node.querySelector(".message-text"), body);
  assert.equal(ui.el("send").disabled, false);
});

test("preview reconciliation preserves retained nodes while removing missing messages and replacing changed bodies", async () => {
  const api = roomFixture();
  const original = {...page("R"),messages:[...page("R").messages,...page("R",2).messages],snapshot_seq:2};
  api.roomHook = path => path === "/v1/read" ? original : undefined;
  const ui = await launch(api); await ui.select("S");
  const messages = deferred();
  api.roomHook = path => path === "/v1/read" ? messages.promise : undefined;
  await ui.select("R");
  const node = ui.el("messages").children.find(node => node.id === "message-1");
  messages.resolve({...original,messages:[original.messages[0],...page("R",3).messages],snapshot_seq:3}); await settle();
  assert.equal(ui.el("messages").children.find(node => node.id === "message-1"), node);
  assert.equal(messageText(ui, 2), undefined);
  assert.equal(messageText(ui, 3), "Message R");
  await ui.select("S");
  api.roomHook = path => path === "/v1/read" ? {...page("R",3),messages:[{...original.messages[0],text:"Updated body"}]} : undefined;
  await ui.select("R");
  assert.equal(messageText(ui, 1), "Updated body");
  assert.equal(messageText(ui, 3), undefined);
});

test("returning to a Room displays cached messages and members before HTTP and gates sending until both reads succeed", async () => {
  const api = roomFixture(), ui = await launch(api), workspace = deferred(), members = deferred();
  await ui.draft("Draft R"); await ui.select("S");
  assert.equal(messageText(ui, 1), "Message S");
  api.calls.length = 0;
  api.roomHook = (path, body) => path.startsWith("/v1/workspace?") ? workspace.promise
    : path === "/v1/member/list" ? members.promise : path === "/v1/read" ? page(body.room_id, 2) : undefined;
  await ui.select("R");
  assert.equal(messageText(ui, 1), "Message R");
  assert.equal(ui.el("members").children.length, 3, "cached Agent entries and the human remain visible");
  assert.equal(ui.el("message-input").value, "Draft R");
  assert.equal(count(api, "/v1/read"), 0); assert.equal(ui.el("send").disabled, true);
  await ui.clock.advance(300);
  assert.equal(count(api, "/v1/human-read/confirm"), 0, "a preview does not confirm reading");
  workspace.resolve({ workspace: { root: "workspaces" }, workspace_version: 1, members: [] }); await settle();
  assert.equal(messageText(ui, 1), undefined); assert.equal(messageText(ui, 2), "Message R");
  assert.equal(api.calls.find(call => call.path === "/v1/read").body.mode, "latest");
  assert.equal(ui.el("send").disabled, true, "cached members cannot satisfy verification");
  members.resolve({ members: structuredClone(api.members) }); await settle();
  assert.equal(ui.el("send").disabled, false);
});

test("a cached Room restores its message position and preserves it through revalidation", async () => {
  const api = roomFixture(), ui = await launch(api), workspace = deferred();
  ui.el("timeline").scrollHeight = 1200; ui.el("timeline").scrollTop = 200;
  await ui.select("S");
  api.roomHook = path => path.startsWith("/v1/workspace?") ? workspace.promise : undefined;
  await ui.select("R");
  assert.equal(messageText(ui, 1), "Message R"); assert.equal(ui.el("timeline").scrollTop, 200);
  workspace.resolve({ workspace: { root: "workspaces" }, workspace_version: 1, members: [] }); await settle();
  assert.equal(ui.el("timeline").scrollTop, 200);
});

test("a known clear or replacement runtime prevents displaying the cached Room", async () => {
  for (const kind of ["clear", "runtime"]) {
    const api = roomFixture(), ui = await launch(api), workspace = deferred();
    await ui.select("S");
    if (kind === "clear") api.rooms[0].human_read = { status: "ready", revision: 1, snapshot_seq: 1, cleared_through: 1, unread_count: 0, mention_count: 0 };
    else api.rooms[0].service.service_epoch = "replacement";
    await ui.reads.catalog();
    api.roomHook = path => path.startsWith("/v1/workspace?") ? workspace.promise : undefined;
    await ui.select("R");
    assert.equal(messageText(ui, 1), undefined, kind);
    workspace.resolve({ workspace: { root: "workspaces" }, workspace_version: 1, members: [] }); await settle();
  }
});

test("an entry reading snapshot can clear a stale preview before Workspace finishes", async () => {
  const api = roomFixture(), ui = await launch(api), workspace = deferred();
  await ui.select("S");
  api.roomHook = (path, body) => path.startsWith("/v1/workspace?") ? workspace.promise
    : path === "/v1/human-read/state" ? { version: 2, room_id: body.room_id, room_instance: body.room_instance, operator_id: body.caller.id,
      revision: 1, snapshot_seq: 1, cleared_through: 1, opened_through: 1, mention_seen_seqs: [], unseen_mention_seqs: [], unread_count: 0, mention_count: 0,
      ...(body.include_new_messages ? { new_message_seqs: [] } : {}) } : undefined;
  await ui.select("R"); assert.equal(messageText(ui, 1), undefined);
  assert.equal(count(api, "/v1/human-read/confirm"), 0);
  workspace.resolve({ workspace: { root: "workspaces" }, workspace_version: 1, members: [] }); await settle();
});

test("viewing cached content does not consume the new visit's unread boundary", async () => {
  const api = roomFixture(), workspace = deferred(); let snapshot = 1;
  api.roomHook = (path, body) => path === "/v1/human-read/state" ? { version: 2, room_id: body.room_id, room_instance: body.room_instance, operator_id: body.caller.id,
    revision: 0, snapshot_seq: snapshot, cleared_through: 0, opened_through: 0, mention_seen_seqs: [], unseen_mention_seqs: [], unread_count: snapshot, mention_count: 0,
    ...(body.include_new_messages ? { new_message_seqs: Array.from({ length: snapshot }, (_, i) => i + 1) } : {}) } : undefined;
  const ui = await launch(api); await ui.select("S");
  const previous = api.roomHook; snapshot = 2;
  api.roomHook = (path, body) => path.startsWith("/v1/workspace?") ? workspace.promise : previous(path, body);
  await ui.select("R");
  ui.el("messages").children.find(node => node.id === "message-1").rect = { top: 0, bottom: 80, left: 0, right: 300, width: 300, height: 80 };
  await ui.window.emit("focus"); await ui.clock.advance(300);
  assert.match(ui.el("view-mention").textContent, /2 条新消息/);
  assert.equal(count(api, "/v1/human-read/confirm"), 0);
  workspace.resolve({ workspace: { root: "workspaces" }, workspace_version: 1, members: [] }); await settle();
});

test("cached Room content stays isolated while a superseded message request settles", async () => {
  const api = roomFixture(), ui = await launch(api), old = deferred(), current = deferred();
  await ui.select("S"); await ui.select("R");
  api.roomHook = (path, body) => path === "/v1/read" ? body.room_id === "S" ? old.promise : current.promise : undefined;
  await ui.select("S"); assert.equal(messageText(ui, 1), "Message S");
  await ui.select("R"); assert.equal(messageText(ui, 1), "Message R");
  old.resolve(page("S", 99)); await settle();
  assert.equal(messageText(ui, 99), undefined); assert.equal(messageText(ui, 1), "Message R");
  current.resolve(page("R", 2)); await settle();
  assert.equal(messageText(ui, 2), "Message R"); assert.equal(ui.sources.filter(source => !source.closed).length, 1);
});

for (const closeWhileLoading of [false,true]) test(`direct settings wait for readiness without a member-list fallback (close=${closeWhileLoading})`, async () => {
 const api=roomFixture();api.members.splice(1);for(const room of api.rooms)room.is_direct=true;
 const ui=await launch(api);
 await ui.el("close-activity").emit("click");await settle();
 api.deferUpdateHello=true;
 const pending=deferred();api.roomHook=path=>path==="/v1/member/list" ? pending.promise : undefined;
 await ui.select("S");await ui.el("conversation-open-details").emit("click");
 assert.equal(ui.el("activity-dialog").open,false);
 assert.equal(ui.el("direct-settings-loading").hidden,false);
 ui.sources.at(-1).emit("hello",{protocol:1,service_epoch:api.epoch,global_versions:api.globalVersions});await settle();
 assert.equal(ui.el("activity-dialog").open,false);
 assert.equal(ui.el("direct-settings-loading").hidden,false);
 if(closeWhileLoading)await ui.el("conversation-open-details").emit("click");
 pending.resolve({members:structuredClone(api.members)});await settle();
 assert.equal(ui.el("activity-dialog").open,!closeWhileLoading);
 if(closeWhileLoading){
  await ui.el("conversation-open-details").emit("click");
  assert.equal(ui.el("activity-dialog").open,true);
 }
 assert.equal(ui.el("direct-settings-loading").hidden,true);
 assert.equal(ui.el("agent-tab-profile").getAttribute("aria-selected"),"true");
 assert.equal(ui.el("activity-title").textContent,"设置");
 assert.equal(api.calls.some(c=>c.path==="/v1/send" || c.path==="/v1/member/run"),false);
});
