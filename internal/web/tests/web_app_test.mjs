const eventView = value => ({ platform_id:value.config?.platform_id, instance:"https://gitlab.invalid", provider: "gitlab", channel: "events", subscription_id: value.subscription_version ? "a".repeat(32) : "", ...structuredClone(value) });
const todoView = value => ({ platform_id:value?.config?.platform_id, provider: "gitlab", channel: "todos", subscription_id: "", subscription_version: 0, available: true, status: "disabled", pending: 0, metrics: {},
  config: { enabled: false, platform_id: "", interval_seconds: 30, filter: {} }, ...structuredClone(value) });
const notificationList = value => { const subscriptions = [eventView(value), todoView()]; return { subscriptions, gitlab: fixtureGitLabView(subscriptions),gitlab_platforms:[fixtureGitLabView(subscriptions)] }; };
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
  let connection = common.platform_id || events.config.platform_id || todos.config.platform_id || "";
  if (events.config.enabled && todos.config.enabled && events.config.platform_id !== todos.config.platform_id) connection = "";
  const config = record?.config || { platform_id:connection, target:common.target || {}, interval_seconds:common.interval_seconds || 30, events:fixtureSourceConfig(events.config,"events"), todos:fixtureSourceConfig(todos.config,"todos") };
  return { platform_id:config.platform_id, version:record?.version || 0, config:structuredClone(config), events, todos, pending:structuredClone(record?.pending || {}), errors:structuredClone(record?.errors || {}), applying:!!record?.applying, backfill_locked:!!record?.backfill_locked || !!events.subscription_id || !!todos.subscription_id };
}
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import * as client from "../assets/client.mjs";
import { launch, settle, withOnboarding, openNotification } from "./web_app_harness.mjs";

test("Scion connection discovers a managed account and saves its profile without manual IDs", async () => {
  const api = server(); let item;
  api.hook = (path, data) => {
    if (path === "/v1/platforms/scion/discover") return {executable:"/service/teamx-scion", profiles:[{profile:"c".repeat(32),username:"alice"},{profile:"d".repeat(32),username:"bob"}]};
    if (path.startsWith("/v1/platforms?")) return {platforms:item ? [structuredClone(item)] : []};
    if (path === "/v1/platforms/save") {
      item = {...structuredClone(data), version:(item?.version || 0)+1, credential_kind:"cli_profile", validation:{status:"unverified"}};
      return structuredClone(item);
    }
    if (path === "/v1/platforms/verify") {
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
  assert.match(ui.el("platforms-feedback").textContent,/账号验证成功/);
  assert.equal(api.calls.some(c => /platforms\/(lark|gitlab)\//.test(c.path)),false);
  await ui.el("platforms-list").children[0].emit("click");
  ui.el("connection-scion-profile").value="d".repeat(32);
  await ui.el("connection-verify").emit("click");
  assert.match(ui.el("platforms-feedback").textContent,/先保存/);
});

async function expandDisclosure(element) {
  element.open=true;await element.emit("toggle");await settle();
}
const elementText = element => [element.textContent,...element.children.map(elementText)].join("\n");

const identity = (room, epoch = `epoch-${room.room_id}`) => ({ room_id: room.room_id, room_instance: room.room_instance, service_epoch: epoch });
const makeRoom = (name, id = name) => ({ room_id: id, room_instance: `instance-${id}`, name,
  status: "ready", team_id: "sample", create_id: `create-${id}`, created_at: "2026-11-01T01:00:00Z", updated_at: "2026-11-01T01:00:00Z" });
const message = (seq, text) => ({ seq, text, author: { kind: "agent", id: "reviewer" }, created_at: "2026-11-01T01:00:00Z" });

test("busy display reads preserve messages, draft and send availability until contention persists", async () => {
  const api=server([makeRoom("A")]),ui=await launch(api);
  await ui.draft("保留这条草稿");
  const node=ui.el("messages").children.find(el=>el.id==="message-1");
  ui.el("timeline").scrollTop=37;
  let busy=true;
  api.hook=path=>{if(busy && path==="/v1/rooms")throw Object.assign(Error("room commit or unread page already pending"),{code:"busy"});};
  await ui.refresh();
  assert.equal(ui.el("sync-error").hidden,true);assert.equal(ui.el("send").disabled,false);
  assert.equal(ui.el("messages").children.find(el=>el.id==="message-1"),node);
  assert.equal(ui.el("timeline").scrollTop,37);assert.equal(ui.el("message-input").value,"保留这条草稿");
  await ui.clock.advance(4500);await ui.refresh();
  assert.equal(ui.el("sync-error").hidden,false);assert.match(ui.el("sync-error").textContent,/服务繁忙/);
  assert.doesNotMatch(ui.el("sync-error").textContent,/room commit|unread page/);
  assert.equal(ui.el("send").disabled,true);
  busy=false;await ui.refresh();
  assert.equal(ui.el("sync-error").hidden,true);assert.equal(ui.el("send").disabled,false);
  assert.equal(ui.el("timeline").scrollTop,37);assert.equal(ui.el("message-input").value,"保留这条草稿");
  assert.equal(api.calls.filter(c=>c.path==="/v1/send").length,0);
});

test("operator display names restore mention reminders and remain marked after viewing",async()=>{
  const api=server([makeRoom("A")]),owner="owner_"+"a".repeat(32);
  api.catalog.operator=owner;api.history.set("A",[message(1,"@russellluo 请确认")]);
  api.hook=path=>path==="/v1/ui" ? {managed:true,operator_display_name:"russellluo"} : undefined;
  const ui=await launch(api);
  assert.equal(mentionHighlighted(ui,1),true);
  assert.deepEqual(mentionElement(ui,1).querySelector(".message-text").markdownOptions.operatorNames,[owner,"russellluo"]);
  await visibleMessages(ui,[1]);await ui.clock.advance(1000);
  assert.equal(mentionHighlighted(ui,1),false);assert.equal(ui.el("mention-notice").hidden,true);
  assert.deepEqual(mentionElement(ui,1).querySelector(".message-text").markdownOptions.operatorNames,[owner,"russellluo"]);
  assert.equal(ui.el("operator"),undefined);
});

test("message authors and references use the operator display name without changing identity", async () => {
  const api = server([makeRoom("A")]), owner = "owner_" + "a".repeat(32);
  let displayName = "russellluo";
  api.catalog.operator = owner;
  const ownMessage = {...message(1, "已处理"), author:{kind:"human",id:owner}};
  api.history.set("A", [ownMessage, {...message(2, "收到"), reply_to:1},
    {...message(3, "其他人的消息"), author:{kind:"human",id:"bob"}}]);
  api.hook = path => path === "/v1/ui" ? {managed:true,operator_display_name:displayName} : undefined;
  const ui = await launch(api);
  const own = ui.el("messages").children.find(el => el.id === "message-1");
  const reply = ui.el("messages").children.find(el => el.id === "message-2");
  assert.equal(own.querySelector(".author").textContent, "russellluo");
  assert.equal(own.querySelector(".author-kind").textContent, "你 · HUMAN");
  assert.equal(reply.querySelector(".message-reference").textContent, "↳ #1 russellluo：已处理");
  assert.equal(reply.querySelector(".author").textContent, "reviewer");
  assert.equal(ui.el("messages").children.find(el => el.id === "message-3").querySelector(".author").textContent, "bob");
  displayName = ""; await ui.refresh();
  assert.equal(ui.el("messages").children.find(el => el.id === "message-1"), own);
  assert.equal(own.querySelector(".author").textContent, "用户");
  assert.equal(reply.querySelector(".message-reference").textContent, "↳ #1 用户：已处理");
  await ui.draft("继续处理"); await ui.el("compose").emit("submit");
  assert.deepEqual(api.calls.find(c => c.path === "/v1/send").data.caller, {kind:"human",id:owner});
  assert.equal(ownMessage.author.id, owner);
});

test("browser display name edits update cached message authors and references and survive reload", async () => {
  const api = server([makeRoom("A")]), owner = "owner_" + "b".repeat(32), names = new Map();
  const localStorage = {getItem:key=>names.get(key),setItem:(key,value)=>names.set(key,value),removeItem:key=>names.delete(key)};
  const profileKey = JSON.stringify(["teamx.user-name", "installation-a", owner]);
  names.set(profileKey, "本地昵称");
  let installation = "installation-a";
  api.catalog.operator = owner;
  api.history.set("A", [{...message(1, "已处理"),author:{kind:"human",id:owner}}, {...message(2, "收到"),reply_to:1}]);
  api.hook = path => path === "/v1/ui" ? {managed:true,installation_id:installation,operator_display_name:"russellluo"} : undefined;
  const ui = await launch(api, new Map(), {localStorage});
  const own = ui.el("messages").children.find(el => el.id === "message-1");
  const reply = ui.el("messages").children.find(el => el.id === "message-2");
  assert.equal(own.querySelector(".author").textContent, "本地昵称");
  ui.el("user-display-name").value = "阿罗"; await ui.el("user-name-form").emit("submit");
  assert.equal(own.querySelector(".author").textContent, "阿罗");
  assert.equal(reply.querySelector(".message-reference").textContent, "↳ #1 阿罗：已处理");
  assert.equal(ui.el("user-name").textContent, "阿罗");
  const reopened = await launch(api, new Map(), {localStorage});
  assert.equal(reopened.el("messages").children.find(el => el.id === "message-1").querySelector(".author").textContent, "阿罗");
  names.set(profileKey, "另一个标签页的昵称"); await ui.window.emit("storage", {key:profileKey});
  assert.equal(own.querySelector(".author").textContent, "另一个标签页的昵称");
  ui.el("user-display-name").value = ""; await ui.el("user-name-form").emit("submit");
  assert.equal(own.querySelector(".author").textContent, "russellluo");
  ui.el("user-display-name").value = "当前服务的昵称"; await ui.el("user-name-form").emit("submit");
  installation = "installation-b"; await ui.refresh();
  assert.equal(ui.el("messages").children.find(el => el.id === "message-1").querySelector(".author").textContent, "russellluo");
  assert.equal(api.history.get("A")[0].author.id, owner);
});

test("apps sharing the native module graph retain independent selection, drafts and settings", async () => {
  const [first, second] = await Promise.all([
    launch(server([makeRoom("A"), makeRoom("B")])),
    launch(server([makeRoom("A"), makeRoom("B")])),
  ]);
  await first.draft("first tab");
  await second.draft("second tab");
  await first.select("B");
  await first.el("open-settings").emit("click");
  first.el("system-timezone").value = "UTC";
  await first.el("system-timezone").emit("input");
  await second.refresh();
  assert.equal(first.el("room-name").textContent, "B");
  assert.equal(second.el("room-name").textContent, "A");
  assert.equal(second.el("message-input").value, "second tab");
  await second.el("open-settings").emit("click");
  assert.equal(second.el("system-timezone").value, "Asia/Shanghai");
  assert.equal(first.el("system-timezone").value, "UTC");
  await first.select("A");
  assert.equal(first.el("message-input").value, "first tab");
});

test("unavailable browser storage preserves in-memory drafts across Room switches", async () => {
  const ui = await launch(server([makeRoom("A"), makeRoom("B")]), new Map(), {
    get sessionStorage() { throw new Error("Storage is unavailable"); },
  });
  await ui.draft("memory-only draft");
  await ui.select("B");
  await ui.draft("other draft");
  await ui.select("A");
  assert.equal(ui.el("message-input").value, "memory-only draft");
  assert.equal(ui.el("sync-error").textContent, "");
});

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
      if (path.startsWith("/v1/platforms?")) return { platforms: [{ id: "a".repeat(32), name: "GitLab", provider: "gitlab", url: "https://gitlab.invalid", validation: { status: "valid", user_id: 101, username: "alice" } }] };
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
      if(path.startsWith("/v1/platform/gitlab/notifications?")) {
      const q=new URL(path,"http://fixture").searchParams,view=notificationList(structuredClone(events.get(q.get("room_id")))).gitlab;
      view.config.platform_id=q.get("platform_id");view.platform_id=q.get("platform_id");return view;
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
  return withOnboarding(api);
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
  assert.equal(ui.el("message-input").placeholder, "发送给 新名称");
  assert.equal(ui.el("message-input-label").textContent, "发送给 新名称");
  assert.equal(ui.el("rename-room").attributes["aria-label"], "新名称，修改房间名称");
  assert.equal(ui.el("room-list").children[0].children[0].children[0].textContent, "新名称");
  assert.equal(ui.document.title, "新名称 · TeamX");
  assert.deepEqual(api.catalog.rooms[0]?.service, info);
  assert.equal(ui.el("message-input").value, "Keep this draft");
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

test("room list displays and sorts by last message time, with creation time for empty rooms", async t => {
  t.mock.method(Date, "now", () => Date.parse("2026-11-01T12:00:00Z"));
  const a = { ...makeRoom("A"), last_message_at: "2026-11-01T09:02:46Z", updated_at: "2026-11-01T09:05:00Z" };
  const b = { ...makeRoom("B"), last_message_at: "2026-11-01T09:03:00Z", updated_at: "2026-11-01T09:04:00Z" };
  const empty = { ...makeRoom("Empty"), created_at: "2026-11-01T09:01:00Z", updated_at: "2026-11-01T09:06:00Z" };
  const api = server([a, b, empty]);
  api.history.set("A", [{ ...message(1, "MR closed"), created_at: a.last_message_at }]);
  api.history.set("Empty", []);
  const ui = await launch(api);
  const format = value => new Date(value).toLocaleTimeString("en-GB", { timeZone: "Asia/Shanghai", hour: "2-digit", minute: "2-digit", hour12: false });
  const list = () => ui.el("room-list").children.map(row => row.children[0]);
  assert.deepEqual(list().map(button => button.children[0].textContent), ["B", "A", "Empty"]);
  assert.equal(list()[1].children[1].textContent, format(a.last_message_at));
  assert.equal(list()[2].children[1].textContent, format(empty.created_at));
  assert.match(list()[1].children[1].title, /^最后消息：2026年11月1日.*17:02:46/);
  assert.match(list()[2].children[1].title, /^创建于：2026年11月1日.*17:01:00/);
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

test("conversation list refreshes yesterday and year labels at system midnight without catalog changes", async t => {
  let now=Date.parse("2026-12-31T15:59:00Z");
  t.mock.method(Date,"now",()=>now);
  const recent={...makeRoom("Recent"),last_message_at:"2026-12-31T14:30:00Z"};
  const empty={...makeRoom("Empty"),created_at:"2026-12-30T10:00:00Z"};
  const older={...makeRoom("Older"),last_message_at:"2026-12-29T10:00:00Z"};
  const api=server([older,empty,recent]),ui=await launch(api);
  const labels=()=>ui.el("room-list").children.map(row=>row.children[0].children[1].textContent);
  assert.deepEqual(labels(),["22:30","昨天","12月29日"]);
  now=Date.parse("2026-12-31T16:01:00Z");await ui.refresh();
  assert.deepEqual(labels(),["昨天","2026年12月30日","2026年12月29日"]);
});

test("conversation timestamps remain separate from exceptional runtime and management states", async () => {
  const room=makeRoom("A"),api=server([room]),ui=await launch(api);
  const choice=()=>ui.el("room-list").children[0].children[0];
  assert.equal(choice().children[2].textContent,"房间已创建");
  room.runtime_status="closed";await ui.refresh();
  assert.equal(choice().children[2].textContent,"房间已创建");
  for(const [state,label] of [
    [{runtime_status:"opening"},"正在打开…"],
    [{runtime_status:"failed",runtime_error:"backend unavailable"},"运行受阻 · backend unavailable"],
    [{runtime_status:"ready",runtime_error:"",busy:true},"会话操作进行中…"],
    [{busy:false,status:"creating"},"创建待完成 · 可重试原创建请求"],
    [{status:"deleting",busy:true},"正在删除…"],
  ]) {
    Object.assign(room,state);await ui.refresh();
    assert.match(choice().children[1].title,/^创建于：/);
    assert.equal(choice().children[2].textContent,label);
  }
});

test("conversation previews show latest text and author, creation hints and priority operation states", async () => {
  const team={...makeRoom("Team"),members:[{id:"reviewer",name:"审阅者"}],last_message_at:"2026-11-01T09:00:00Z",
    last_message_preview:{author:{kind:"agent",id:"reviewer"},text:"完成审阅\n\t请确认下一步"}};
  const direct={...makeRoom("Direct"),is_direct:true,last_message_at:team.last_message_at,
    last_message_preview:{author:{kind:"human",id:"tester"},text:"检查一下"}};
  const empty=makeRoom("Empty"),emptyDirect={...makeRoom("Empty direct"),is_direct:true};
  const api=server([team,direct,empty,emptyDirect]),ui=await launch(api);
  const choice=name=>[...ui.el("direct-list").children,...ui.el("room-list").children].find(row=>row.children[0].children[0].textContent===name).children[0];
  assert.equal(choice("Team").children[2].textContent,"审阅者：完成审阅 请确认下一步");
  assert.equal(choice("Direct").children[2].textContent,"你：检查一下");
  assert.equal(choice("Empty").children[2].textContent,"房间已创建");
  assert.equal(choice("Empty direct").children[2].textContent,"私信已创建");
  direct.last_message_preview={author:{kind:"agent",id:"chief-of-staff"},text:"<img src=x> 已完成"};
  await ui.refresh();assert.equal(choice("Direct").children[2].textContent,"助理：<img src=x> 已完成");
  team.runtime_status="opening";await ui.refresh();
  assert.equal(choice("Team").children[2].textContent,"正在打开…");
  team.runtime_status="ready";team.last_message_preview.text="等待反馈";await ui.refresh();
  assert.equal(choice("Team").children[2].textContent,"审阅者：等待反馈");
  delete team.last_message_at;delete team.last_message_preview;await ui.refresh();
  assert.equal(choice("Team").children[2].textContent,"房间已创建");
  assert.match(choice("Team").children[1].title,/^创建于：/);
});

test("settings and room creation dismiss only when the pointer starts and clicks outside the dialog", async () => {
  const api = server(), ui = await launch(api);
  for (const [opener, id] of [["open-settings", "settings-dialog"], ["new-team-room", "create-dialog"]]) {
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
  assert.equal(api.calls.some(call => /\/v1\/rooms\/(create|delete)|platforms\/(save|delete)/.test(call.path)), false);
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
  assert.equal(ui.el("message-input").value, "Opening");
  assert.equal(ui.el("reply-context").hidden, true);
  await ui.draft("draft-B"); await ui.select("A");
  assert.equal(ui.el("message-input").value, "draft-A");
  assert.equal(ui.el("reply-context").hidden, false);
  assert.match(ui.el("reply-label").textContent, /quote-A/);
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
  assert.equal(ui.el("send").getAttribute("aria-label"), "发送");
  const sent = api.calls.filter(call => call.path === "/v1/send");
  assert.equal(sent.length, 1); assert.equal(sent[0].data.room_instance, "instance-A");
  await ui.select("A"); assert.equal(ui.el("message-input").value, "");
});

test("quote preview separates author and text, locates its source and cancels without losing the draft", async () => {
  const api=server([makeRoom("A")]);api.history.set("A",[message(1,"原消息\n\t第二行")]);
  const ui=await launch(api);await ui.draft("保留正文");
  const source=ui.el("messages").children.find(el=>el.id==="message-1");
  await source.querySelector(".reply-button").emit("click");
  assert.equal(ui.el("reply-author").textContent,"引用 reviewer 的消息");
  assert.equal(ui.el("reply-sequence").textContent,"#1");
  assert.equal(ui.el("reply-label").textContent,"原消息 第二行");
  assert.equal(ui.document.activeElement,ui.el("message-input"));
  await ui.el("reply-preview").emit("click");
  assert.equal(source.scrollIntoViewCount,1);assert.equal(source.classList.contains("flash"),true);
  const restored=await launch(api,ui.saved);
  assert.equal(restored.el("reply-author").textContent,"引用 reviewer 的消息");
  assert.equal(restored.el("reply-label").textContent,"原消息 第二行");
  await restored.el("cancel-reply").emit("click");
  assert.equal(restored.el("reply-context").hidden,true);
  assert.equal(restored.el("message-input").value,"保留正文");
  assert.equal(restored.document.activeElement,restored.el("message-input"));
  const cancelled=await launch(api,restored.saved);
  assert.equal(cancelled.el("reply-context").hidden,true);assert.equal(cancelled.el("message-input").value,"保留正文");
});

test("lost send is isolated and explicit retry retains the original epoch after A/B/A", async () => {
  const api = server([makeRoom("A"), makeRoom("B")]), ui = await launch(api);
  await ui.el("messages").children.find(el=>el.id==="message-1").querySelector(".reply-button").emit("click");
  api.hook = path => path === "/v1/send" ? Promise.reject(new Error("response lost")) : undefined;
  await ui.draft("uncertain-A"); await ui.el("compose").emit("submit");
  const original = api.calls.find(call => call.path === "/v1/send").data;
  assert.equal(original.reply_to,1);assert.equal(ui.el("cancel-reply").disabled,true);
  await ui.el("cancel-reply").emit("click");assert.equal(ui.el("reply-context").hidden,false);
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
  const ui = await launch(api), toggle = ui.el("toggle-members"), panel = ui.el("activity-dialog");
  const list = ui.el("member-list-view");
  const reads = () => api.calls.filter(call => call.path === "/v1/member/activity");
  const open = async () => {
    await ui.el("members").children[1].querySelector(".member-summary").emit("click"); await ui.el("agent-tab-activity").emit("click"); await settle();
  };
  assert.equal(ui.el("toggle-activity"), undefined); assert.equal(list.hidden, false);
  members.push(...["observer", "reviewer"].map((id, index) => ({ agent_id: id, membership_id: `generation-${id}`,
    status: index ? "running" : "idle", runner: { profile: "runtime" }, execution: { active: !!index, agent: { turn_number: 1 } } })));
  await ui.refresh(); await settle();
  await open();
  assert.equal(panel.open, true); assert.equal(panel.modal, false);
  assert.equal(toggle.attributes["aria-expanded"], "true"); assert.equal(list.hidden, true); assert.equal(list.inert, true);
  assert.equal(ui.selectedAgent().membership_id, "generation-reviewer"); assert.equal(ui.el("activity-title").textContent, "reviewer");
  assert.equal(ui.el("activity-member"), undefined);
  await ui.el("close-activity").emit("click");await ui.el("members").children[0].querySelector(".member-summary").emit("click");await ui.el("agent-tab-activity").emit("click");await settle();
  assert.equal(ui.selectedAgent().membership_id, "generation-observer"); assert.equal(reads().at(-1).data.agent_id, "observer");
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
  assert.equal(ui.selectedAgent()?.membership_id, "generation-reviewer");
  assert.equal(reads().at(-1).data.membership_id, members[0].membership_id);
  assert.equal(ui.el("activity-timeline").children[0].children[1].children[0].textContent, "activity-reviewer");
  await close();
  members[1].status = "running"; members[1].execution.active = true;
  await ui.refresh(); await settle();
  assert.deepEqual(buttons().map(button => button.textContent), ["reviewer", "developer"]);
  await open(buttons()[1]); assert.equal(ui.selectedAgent()?.membership_id, members[1].membership_id); await close();
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
  await open(buttons()[0]); assert.equal(ui.selectedAgent()?.membership_id, members[1].membership_id); await close();
  assert.equal(api.calls.some(call => /^\/v1\/(send|member\/(run|pause|cancel))$/.test(call.path)), false);
});


test("stop icon cancels only the current execution and reports stopped after settlement", async () => {
  const api = server([makeRoom("A")]);
  const member = { agent_id:"reviewer", membership_id:"generation-reviewer", status:"running", runner:{profile:"runtime"},
    execution:{active:true, paused:false, execution_id:7, agent:{turn_number:6}} };
  let accept;
  api.hook = (path, data) => {
    if (path === "/v1/member/list") return {members:[structuredClone(member)]};
    if (path === "/v1/member/activity") return {member:structuredClone(member)};
    if (path === "/v1/member/cancel") {
      assert.equal(data.agent_id, member.agent_id); assert.equal(data.membership_id, member.membership_id);
      assert.equal(data.execution_id, 7); assert.equal(data.room_instance,"instance-A");
      return new Promise(resolve => accept = () => {member.execution.stopping=true;resolve(structuredClone(member.execution));});
    }
  };
  const ui = await launch(api);
  const stop = () => ui.el("execution-text").children.find(child => child.dataset.stopMember);
  const detail = () => ui.el("execution-text").children.find(child => child.dataset.activityMember);
  const original = stop();
  assert.equal(original.getAttribute("aria-label"), "停止 reviewer");
  await detail().emit("click"); await settle();
  assert.deepEqual(ui.el("agent-actions").children.map(button => button.textContent), ["暂停"]);
  const pending = original.emit("click"); await settle();
  assert.equal(stop().disabled,true); assert.match(detail().textContent,/正在停止/);
  await original.emit("click"); await settle();
  assert.equal(api.calls.filter(call => call.path === "/v1/member/cancel").length,1);
  accept(); await pending;
  assert.match(detail().textContent,/正在停止/);
  assert.doesNotMatch(elementText(ui.el("execution-text")),/已停止/);
  member.status="idle";member.execution.active=false;member.execution.stopping=false;delete member.execution.execution_id;
  await ui.refresh();
  assert.equal(ui.el("execution-text").children[0].textContent,"已停止");
  assert.equal(ui.el("execution-hint").hidden,false);
  assert.equal(ui.el("execution-hint").dataset.phase,"stopped");
  assert.equal(member.execution.paused,false);
  await ui.draft("纠正：只检查，不要修改"); await ui.el("compose").emit("submit");
  member.status="waiting";await ui.refresh();
  assert.match(detail().textContent,/正在等待执行/);
  assert.doesNotMatch(elementText(ui.el("execution-text")),/已停止/);
  member.status="running";member.execution.active=true;member.execution.execution_id=8;
  await ui.refresh();
  assert.match(detail().textContent,/正在处理/);assert.equal(stop().disabled,false);
  await original.emit("click");
  assert.equal(api.calls.filter(call => call.path === "/v1/member/cancel").length,1);
  assert.equal(api.calls.some(call => /^\/v1\/member\/(pause|run)$/.test(call.path)),false);
});

test("stop icons isolate members and unconfirmed requests never replay on refresh", async () => {
  const api = server([makeRoom("A")]);
  const members = ["reviewer","developer"].map((id,index) => ({agent_id:id, membership_id:`member-${id}`, status:"running",
    runner:{profile:"runtime"},execution:{active:true,paused:false,execution_id:index+1,agent:{turn_number:0}}}));
  let fail = true;
  api.hook = (path,data) => {
    if (path === "/v1/member/list") return {members:structuredClone(members)};
    if (path === "/v1/member/cancel") {
      assert.equal(data.membership_id,"member-developer"); assert.equal(data.execution_id,2);
      if (fail) throw new Error("response lost");
      members[1].execution.stopping=true;
      return structuredClone(members[1].execution);
    }
  };
  const ui = await launch(api);
  const stop = id => ui.el("execution-text").children.find(child => child.dataset.stopMember===`member-${id}`);
  await stop("developer").emit("click");
  assert.match(elementText(ui.el("execution-text")),/停止未确认/);
  assert.equal(stop("reviewer").disabled,false);assert.equal(stop("developer").disabled,false);
  await ui.refresh();await ui.refresh();
  assert.equal(api.calls.filter(call => call.path === "/v1/member/cancel").length,1);
  fail=false;await stop("developer").emit("click");
  assert.equal(stop("developer").disabled,true);assert.equal(stop("reviewer").disabled,false);
  members[1].status="idle";members[1].execution.active=false;members[1].execution.stopping=false;
  await ui.refresh();
  assert.match(elementText(ui.el("execution-text")),/developer 已停止/);
  assert.equal(stop("reviewer").disabled,false);
});

test("late stop responses cannot change another Room or a replacement member", async () => {
  const api = server([makeRoom("A"),makeRoom("B")]);
  const member = {agent_id:"reviewer",membership_id:"member-reviewer",status:"running",runner:{profile:"runtime"},
    execution:{active:true,paused:false,execution_id:1,agent:{turn_number:0}}};
  let accept;
  api.hook = (path,data) => {
    if (path === "/v1/member/list") return {members:[structuredClone(member)]};
    if (path === "/v1/member/cancel") return new Promise(resolve => accept=()=>resolve({active:false,paused:false}));
  };
  const ui = await launch(api);
  const stop = () => ui.el("execution-text").children.find(child => child.dataset.stopMember);
  const original = stop(), pending = original.emit("click");await settle();
  await ui.select("B");accept();await pending;
  await original.emit("click");
  assert.doesNotMatch(elementText(ui.el("execution-text")),/已停止|正在停止/);
  const old = stop();member.membership_id="replacement";await ui.refresh();await old.emit("click");
  assert.equal(api.calls.filter(call => call.path === "/v1/member/cancel").length,1);
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
  await ui.el("members").children[0].querySelector(".member-summary").emit("click"); await ui.el("agent-tab-activity").emit("click");
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
  assert.equal(ui.el("activity-output-title").textContent, "最近回合 · 已完成");
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

test("activity distinguishes running, stopping and stopped while preserving real failures", async () => {
  const api = server([makeRoom("A")]);
  const member = {agent_id:"reviewer",membership_id:"generation-reviewer",status:"running",read_through:0,unread_count:1,
    runner:{profile:"runtime"},execution:{active:true,stopping:false,error:"",agent:{turn_number:1,turn:{status:"running"}}}};
  const result = {events:[{seq:1,kind:"thought",text:"检查配置"}]};
  let output = {member,current_output:{result}};
  api.hook = path => path === "/v1/member/list" ? {members:[structuredClone(member)]}
    : path === "/v1/member/activity" ? structuredClone(output) : undefined;
  const ui = await launch(api);
  await ui.el("members").children[0].querySelector(".member-summary").emit("click");
  await ui.el("agent-tab-activity").emit("click");await settle();
  assert.equal(ui.el("activity-output-title").textContent,"当前回合 · 运行中");
  const thought = ui.el("activity-timeline").children[0];thought.open=true;
  member.execution.stopping=true;await ui.refresh();await settle();
  assert.equal(ui.el("activity-output-title").textContent,"当前回合 · 正在停止");
  assert.equal(ui.el("activity-status").textContent,"正在停止");
  result.events=[];await ui.refresh();await settle();
  assert.equal(ui.el("activity-timeline-empty").textContent,"等待当前执行结束…");
  output={member};await ui.refresh();await settle();
  assert.equal(ui.el("activity-notice").textContent,"等待当前执行结束…");

  result.events=[{seq:1,kind:"thought",text:"检查配置"}];result.stopReason="cancelled";
  const raw = "context canceled\nDSH turn cancelled";
  member.status="idle";member.execution.active=false;member.execution.stopping=false;
  member.execution.agent.turn.status="interrupted";
  output={member,last_output:{result,error:raw}};
  await ui.refresh();await settle();
  assert.equal(ui.el("activity-output-title").textContent,"最近回合 · 已停止");
  assert.equal(ui.el("activity-turn").textContent,"1 · 已停止");
  assert.equal(ui.el("activity-stop").hidden,true);
  assert.equal(ui.el("activity-output-error").hidden,true);
  assert.equal(ui.el("activity-execution-error").hidden,true);
  assert.equal(ui.el("activity-timeline").children[0].children[1].children[0].textContent,"检查配置");
  assert.equal(output.last_output.error,raw); // Presentation does not rewrite diagnostics.

  for (const failure of ["context deadline exceeded",`${raw}\nbackend cleanup failed`]) {
    member.execution.error=failure;output.last_output.error=failure;
    member.execution.session_reset_required=failure.includes("cleanup");
    await ui.refresh();await settle();
    assert.equal(ui.el("activity-output-title").textContent,"最近回合 · 执行失败");
    assert.equal(ui.el("activity-output-error").hidden,false);
    assert.equal(ui.el("activity-output-error").textContent,failure);
    assert.equal(ui.el("activity-execution-error").textContent,failure);
  }
  member.execution.error="";member.execution.session_reset_required=false;
  member.execution.agent.turn.status="failed";
  await ui.refresh();await settle();
  assert.equal(ui.el("activity-output-error").hidden,false);
  assert.equal(ui.el("activity-output-title").textContent,"最近回合 · 执行失败");
  output.last_output.error="";await ui.refresh();await settle();
  assert.equal(ui.el("activity-output-title").textContent,"最近回合 · 执行失败");

  member.execution.agent.turn.status="completed";
  output.last_output.error="";result.stopReason="end_turn";
  await ui.refresh();await settle();
  assert.equal(ui.el("activity-output-title").textContent,"最近回合 · 已完成");
  assert.equal(ui.el("activity-output-error").hidden,true);
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
    await ui.el("members").children[0].querySelector(".member-summary").emit("click"); await ui.el("agent-tab-activity").emit("click"); await settle();
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
  assert.equal(ui.el("room-name").textContent, "选择或新建会话");
  assert.equal(ui.el("message-input").value, "");
  assert.equal(ui.el("send").disabled, true);
  assert.equal(ui.saved.has(JSON.stringify(["teamx.draft", "instance-A", "tester"])), false);
  assert.match(ui.el("management-notice").textContent, /已删除/);
  await ui.select("B");
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
  api.platforms = [{ id: "a".repeat(32), name: "Company GitLab", provider: "gitlab", url: "https://gitlab.invalid", version: 1, credential_configured: true, validation: { status: "unverified" } }];
  api.workspaceHook = null;
  api.request = async (path, data) => {
    if (path.startsWith("/v1/platforms?")) return { platforms: structuredClone(api.platforms) };
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
      if (target.binding?.platform_id) { target.binding.project_id = 7; target.binding.platform_generation = "f".repeat(32); }
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
  assert.equal(ui.el("message-input").value, "Unsent draft stays");
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
    const api = workspaceServer(); api.platforms[0].url = origin; const ui = await launch(api);
    assert.equal(ui.el("workspace-platform-url"), undefined); assert.equal(ui.el("workspace-project"), undefined);
    await editWorkspace(ui, true);
    ui.el("workspace-repo-url").value = url;

    await ui.el("workspace-form").emit("submit");
    const sent = api.calls.find(c => c.path === "/v1/workspace/apply").data;
    assert.equal(sent.target.repository.url, `${origin}/${project}.git`);
    assert.equal(ui.el("workspace-repo-url").value, `${origin}/${project}.git`);
    assert.deepEqual(sent.target.binding, { provider: "gitlab", url: origin, project, platform_id: "a".repeat(32) });
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
    assert.equal(first.target.binding.platform_id, "a".repeat(32)); assert.equal(first.platform_version, 1);
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
  assert.equal(sent.pat, undefined); assert.equal(sent.target.binding.platform_id, "a".repeat(32));
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
  assert.match(ui.el("management-notice").textContent, /工作目录/);
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
    if (path.startsWith("/v1/platforms?")) return new Promise(resolve => { finishRead = resolve; });
    if (path === "/v1/platforms/save") return new Promise(resolve => { finishSave = resolve; });
  };
  const ui = await launch(api);
  const opening = ui.el("open-settings").emit("click");
 finishRead({platforms:[item]});await opening;
 await ui.el("platforms-list").children[0].emit("click");
  ui.el("connection-name").value = "Confirmed GitLab";
  ui.el("connection-url").value = "https://gitlab.invalid";
  const saving = ui.el("connection-form").emit("submit");
  assert.equal(ui.el("connection-form").hidden, false);
  assert.equal(ui.el("platforms-list-view").hidden, true);
  assert.equal(ui.el("connection-fields").disabled, true);
  const dialog = ui.el("settings-dialog"), outside = { button: 0, clientX: -10, clientY: -10 };
  await dialog.emit("pointerdown", outside); await dialog.emit("click", outside);
  assert.equal(dialog.open, true);
  finishSave({ id: "a".repeat(32), name: "Confirmed GitLab", provider: "gitlab", url: "https://gitlab.invalid", version: 1, credential_kind: "oauth", credential_configured: true, validation: { status: "unverified" } });
  await saving;
  assert.equal(ui.el("connection-form").hidden, true);
  assert.equal(ui.el("platforms-list-view").hidden, false);
  assert.equal(ui.el("platforms-list").children.length, 1);

  assert.equal(ui.el("platforms-list").children.length, 1);
  assert.equal(ui.el("platforms-empty").hidden, true);
  assert.equal(ui.el("platforms-list-view").attributes["aria-busy"], "false");
  assert.equal(ui.el("platforms-feedback").textContent, "名称已保存。");
});

test("platform platforms load on open and return, with retry only after a failed read", async () => {
  const api = server([]);
  let finish, reads = 0;
  const item = { id: "a".repeat(32), name: "Company GitLab", provider: "gitlab", url: "https://gitlab.invalid", validation: { status: "valid" } };
  api.hook = path => {
    if (path.startsWith("/v1/platforms?")) {
      reads++;
      return new Promise((resolve, reject) => { finish = { resolve, reject }; });
    }
  };
  const ui = await launch(api);
  assert.equal(ui.el("platforms-reload"), undefined);
  const opening = ui.el("open-settings").emit("click");
  assert.equal(reads, 1);
  assert.equal(ui.el("platforms-list-view").attributes["aria-busy"], "true");
  assert.equal(ui.el("platforms-load-error").hidden, true);
  assert.equal(ui.el("platforms-feedback").textContent, "");
  finish.resolve({ platforms: [structuredClone(item)] });
  await opening;
  const row = ui.el("platforms-list").children[0];
  assert.equal(ui.el("platforms-list-view").attributes["aria-busy"], "false");
  assert.equal(ui.el("platforms-empty").hidden, true);
  assert.equal(ui.el("platforms-load-error").hidden, true);

  await ui.el("close-settings").emit("click");
  const reopening = ui.el("open-settings").emit("click");
  assert.equal(reads, 2);
  assert.equal(ui.el("platforms-list").children[0], row);
  finish.reject(new Error("fixture failure"));
  await reopening;
  assert.equal(ui.el("platforms-load-error").hidden, false);
  assert.equal(ui.el("platforms-empty").hidden, true);
  assert.equal(ui.el("platforms-retry").disabled, false);
  const failedRetry = ui.el("platforms-retry").emit("click");
  assert.equal(ui.el("platforms-load-error").hidden, false);
  assert.equal(ui.el("platforms-retry").disabled, true);
  assert.equal(ui.el("platforms-retry").textContent, "重试中…");
  await ui.el("platforms-retry").emit("click");
  assert.equal(reads, 3);
  finish.reject(new Error("still offline"));
  await failedRetry;
  assert.equal(ui.el("platforms-list").children[0], row);
  assert.equal(ui.el("platforms-load-error").hidden, false);
  assert.equal(ui.el("platforms-retry").disabled, false);
  assert.equal(ui.el("platforms-retry").textContent, "重试");

  const recovered = ui.el("platforms-retry").emit("click");
  finish.resolve({ platforms: [] });
  await recovered;
  assert.equal(reads, 4);
  assert.equal(ui.el("platforms-load-error").hidden, true);
  assert.equal(ui.el("platforms-empty").hidden, false);
  assert.equal(ui.el("platforms-list-view").attributes["aria-busy"], "false");
  await ui.el("connection-new").emit("click");
  const returning = ui.el("connection-back").emit("click");
  assert.equal(reads, 5);
  finish.resolve({ platforms: [structuredClone(item)] });
  await returning;
  assert.equal(ui.el("platforms-list").children.length, 1);
  assert.equal(ui.el("platforms-load-error").hidden, true);
});




test("Workspace connection manager preserves selection, draft and project without applying", async () => {
  const api = workspaceServer(), ui = await launch(api);
  await ui.draft("draft while setting up project");
  await editWorkspace(ui, true);
  ui.el("workspace-branch").value = "feature/setup";
  await ui.el("workspace-manage-platforms").emit("click");
  assert.equal(ui.el("workspace-dialog").open, true);
  assert.equal(ui.el("settings-dialog").open, true);
  await ui.el("close-settings").emit("click");
  assert.equal(ui.el("workspace-dialog").open, true);
  assert.equal(ui.el("workspace-repo-url").value, "https://gitlab.invalid/group/project.git");
  assert.equal(ui.el("workspace-connection").value, "a".repeat(32));
  assert.equal(ui.el("workspace-branch").value, "feature/setup");
  assert.equal(ui.el("message-input").value, "draft while setting up project");
  assert.equal(api.calls.some(call => call.path === "/v1/workspace/apply"), false);
});


function notificationServer() {
  const api = workspaceServer();
  const events = new Map(["A", "B"].map(id => [id, { available: true, subscription_version: 1,
    config: { platform_id: "a".repeat(32), project: `group/project-${id}`, enabled: true, interval_seconds: id === "A" ? 5 : 17, filter: {} }, status: "running", pending: 0, metrics: { calls: 1, response_bytes: 100 } }]));
  for (const [id, ws] of api.workspaces) {
    ws.workspace.repository = { url: `https://gitlab.invalid/group/project-${id}.git` };
    ws.workspace.binding = { provider: "gitlab", url: "https://gitlab.invalid", project: `group/project-${id}`, project_id: id === "A" ? 7 : 8, platform_id: "a".repeat(32) };
  }
  api.platforms[0].validation = { status: "valid", user_id: 101, username: "alice" };
  for (const [id, value] of events) value.project = structuredClone(api.workspaces.get(id).workspace.binding);
  api.hook = (path, data) => {
    if(path.startsWith("/v1/platform/gitlab/notifications?")) {
      const q=new URL(path,"http://fixture").searchParams,view=notificationList(structuredClone(events.get(q.get("room_id")))).gitlab;
      view.config.platform_id=q.get("platform_id");view.platform_id=q.get("platform_id");return view;
    }
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
  assert.equal(ui.el("activity-dialog").hidden, true); assert.equal(ui.el("notification-detail").open, true);
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
  api.platforms[0].validation = { status: "valid", user_id: 101, username: "alice" };
  api.platforms.push({ ...structuredClone(api.platforms[0]), id: "b".repeat(32), name: "Personal", validation: { status: "valid", user_id: 202, username: "bob" } });
  const todos = new Map(["A", "B"].map(id => [id, todoView({ subscription_id: enabled ? (id === "A" ? "c" : "d").repeat(32) : "", subscription_version: enabled ? 3 : 0,
    recipient_id: enabled ? 101 : undefined, username: enabled ? "alice" : undefined, instance: enabled ? "https://gitlab.invalid" : undefined,
    config: { enabled, platform_id: enabled ? api.platforms[0].id : "", interval_seconds: id === "A" ? 30 : 45, filter: {} }, status: enabled ? "running" : "disabled" })]));
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
      return { subscriptions: [eventView(events.get(id)), structuredClone(todos.get(id))], gitlab: viewFor(id),gitlab_platforms:[viewFor(id)] };
    }
    if(path.startsWith("/v1/platform/gitlab/notifications?")) {
      const q=new URL(path,"http://fixture").searchParams,v=viewFor(q.get("room_id"));
      v.config.platform_id=q.get("platform_id");v.platform_id=q.get("platform_id");
      if(q.get("target_mode")==="inbox")v.config.target={mode:"inbox",agent_id:q.get("agent_id"),membership_id:q.get("membership_id")};
      return v;
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
        const desired = { ...config[channel], platform_id: config.platform_id, target: config.target, interval_seconds: config.interval_seconds, backfill_seconds:config.backfill_seconds || 0, ...(config.backfill_start ? {backfill_start:config.backfill_start} : {}) };
        const account = api.platforms.find(c => c.id === config.platform_id);
        const changed = JSON.stringify(fixtureSourceConfig(runtime.config, channel)) !== JSON.stringify(fixtureSourceConfig(desired, channel)) || runtime.config.interval_seconds !== desired.interval_seconds || runtime.config.platform_id !== desired.platform_id || JSON.stringify(runtime.config.target || {}) !== JSON.stringify(desired.target) || runtime.recipient_id !== account?.validation.user_id;
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

test("GT-03 message navigation opens direct platform details and the requested channel", async () => {
  const { api } = todosServer({enabled:true}); api.history.set("A", ["events", "todos"].map((channel, index) => ({ ...message(index + 1, "integration fact"), author: { kind: "integration", id: "gitlab" }, source: { provider: "gitlab", channel, subscription_id:(channel==="todos" ? "c" : "a").repeat(32), platform_id:"a".repeat(32) } })));
  const ui = await launch(api);
  for (let i = 1; i <= 2; i++) {
    const article = ui.document.getElementById(`message-${i}`), channel = i === 2 ? "todos" : "events";
    assert.equal(article.querySelector(".author").textContent, `GitLab · ${i === 2 ? "我的待办" : "项目活动"}`);
    await article.querySelector(".author").emit("click"); assert.equal(ui.el("notification-panel-gitlab").hidden, false); assert.equal(ui.el(`gitlab-${channel}-settings`).open, true);
    await ui.el("close-notification").emit("click"); await article.querySelector(".avatar").emit("keydown", { key: "Enter" });
    assert.equal(ui.el("notification-panel-gitlab").hidden, false); assert.equal(ui.el(`gitlab-${channel}-settings`).open, true);
  }
  assert.equal(ui.el("notification-tab-gitlab"),undefined);
  assert.equal(ui.el("notification-settings-details"),undefined);
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
  await ui.el("members").children[0].querySelector(".member-summary").emit("click"); await ui.el("agent-tab-activity").emit("click"); await settle();
  assert.equal(ui.el("activity-output").hidden, true);
  assert.match(ui.el("activity-notice").textContent, /格式无效/);
  result = {events:[],stopReason:"cancelled"};
  await ui.refresh(); await settle();
  assert.equal(ui.el("activity-output").hidden, false);
  assert.match(ui.el("activity-timeline-empty").textContent, /没有活动记录/);
});






test("Room mentions filter members and select by keyboard without sending or changing delivery", async () => {
  const api = server([makeRoom("A")]);
  const members = ["reviewer", "developer", "开发助理"].map(agent_id => ({agent_id, membership_id:`member-${agent_id}`, status:"idle", runner:{profile:"runtime"}}));
  api.hook = path => path === "/v1/member/list" ? {members} : undefined;
  const ui = await launch(api), input = ui.el("message-input"), list = ui.el("mention-options");
  const type = async text => { input.value=text; input.selectionStart=input.selectionEnd=text.length; await input.emit("input"); };
  await type("请 @");
  assert.deepEqual(list.children.map(el => el.textContent), ["@reviewer", "@developer", "@开发助理"]);
  await input.emit("keydown", {key:"ArrowDown"});
  assert.equal(list.children[1].getAttribute("aria-selected"), "true");
  await input.emit("keydown", {key:"Enter",repeat:true});
  assert.equal(input.value,"请 @");
  await input.emit("keydown", {key:"Enter"});
  assert.equal(input.value,"请 @developer ");assert.equal(list.hidden,true);
  assert.equal(api.calls.filter(c => c.path === "/v1/send").length,0);
  const reload = await launch(api,ui.saved);assert.equal(reload.el("message-input").value,"请 @developer ");
  await type("请 @ReV");assert.deepEqual(list.children.map(el => el.textContent),["@reviewer"]);
  await input.emit("keydown",{key:"Tab"});assert.equal(input.value,"请 @reviewer ");
  await type("请 @开");await list.children[0].emit("click");assert.equal(input.value,"请 @开发助理 ");
  await input.emit("keydown",{key:"Enter"});await settle();
  const send=api.calls.find(c=>c.path==="/v1/send");
  assert.equal(send.data.text,"请 @开发助理 ");assert.equal(send.data.room_id,"A");
  assert.equal(Object.hasOwn(send.data,"recipient"),false);
});

test("mention editing preserves suffixes, ignores emails and IME confirmation, and allows dismissal", async () => {
  const api=server([makeRoom("A")]);
  api.hook=path=>path==="/v1/member/list" ? {members:[{agent_id:"开发助理",membership_id:"member-dev",status:"idle",runner:{profile:"runtime"}}]} : undefined;
  const ui=await launch(api),input=ui.el("message-input"),list=ui.el("mention-options");
  const type=async(text,caret=text.length)=>{input.value=text;input.selectionStart=input.selectionEnd=caret;await input.emit("input");};
  for(const text of ["mail@example.com","@不存在","@@","plain text"]){await type(text);assert.equal(list.hidden,true);}
  await type("@开 关闭你的通知",2);await input.emit("keydown",{key:"Enter"});
  assert.equal(input.value,"@开发助理 关闭你的通知");
  await type("@");await input.emit("compositionstart");await type("@开");
  assert.equal(list.hidden,true);await input.emit("keydown",{key:"Enter",isComposing:true});
  assert.equal(api.calls.filter(c=>c.path==="/v1/send").length,0);
  await input.emit("compositionend");assert.equal(list.hidden,false);
  await input.emit("keydown",{key:"Enter",keyCode:229});assert.equal(input.value,"@开");
  await input.emit("keydown",{key:"Escape"});assert.equal(list.hidden,true);
  await ui.refresh();assert.equal(list.hidden,true);
  await input.emit("click");assert.equal(list.hidden,false);
  await input.emit("blur");assert.equal(list.hidden,true);
});

test("complete member mentions delete as a unit at their boundaries while ordinary editing stays native", async () => {
  const api=server([makeRoom("A")]);
  api.hook=path=>path==="/v1/member/list" ? {members:["reviewer","开发助理"].map(agent_id=>({agent_id,membership_id:`member-${agent_id}`,status:"idle",runner:{profile:"runtime"}}))} : undefined;
  const ui=await launch(api),input=ui.el("message-input");
  const erase=async(text,caret,key,keys={})=>{
    input.value=text;input.selectionStart=input.selectionEnd=caret;await input.emit("input");
    let prevented=false;await input.emit("keydown",{key,...keys,preventDefault(){prevented=true;}});return prevented;
  };
  for(const [text,caret,key,expected] of [
    ["@reviewer ",10,"Backspace",""],
    ["请 @reviewer 关闭通知",12,"Backspace","请 关闭通知"],
    ["@开发助理",5,"Backspace",""],
    ["@reviewer，好的",9,"Backspace","，好的"],
    ["请 @reviewer 关闭通知",2,"Delete","请 关闭通知"],
    ["@reviewer @开发助理 ",16,"Backspace","@reviewer "],
  ]){
    assert.equal(await erase(text,caret,key),true,`${text}: ${key}`);assert.equal(input.value,expected);
    assert.equal(ui.el("mention-options").hidden,true);
  }
  for(const [text,caret,key,keys] of [
    ["@reviewer",4,"Backspace",{}],
    ["@reviewer2",10,"Backspace",{}],
    ["@开发助理甲",5,"Backspace",{}],
    ["x@reviewer",10,"Backspace",{}],
    ["@reviewer  ",11,"Backspace",{}],
    ["普通文本",4,"Backspace",{}],
    ["@reviewer ",10,"Backspace",{isComposing:true}],
    ["@reviewer ",10,"Backspace",{ctrlKey:true}],
  ]){
    assert.equal(await erase(text,caret,key,keys),false,`${text}: native edit`);assert.equal(input.value,text);
  }
  input.value="@reviewer";input.selectionStart=2;input.selectionEnd=5;
  let prevented=false;await input.emit("keydown",{key:"Backspace",preventDefault(){prevented=true;}});assert.equal(prevented,false);
  await erase("@reviewer ",10,"Backspace");
  const reload=await launch(api,ui.saved);assert.equal(reload.el("message-input").value,"");
  assert.equal(api.calls.filter(c=>c.path==="/v1/send").length,0);
});

test("composer and message highlights follow member names while preserving text, scrolling and Room drafts", async () => {
  const api=server([makeRoom("A"),makeRoom("B")]);
  let members=[{agent_id:"reviewer",membership_id:"member-reviewer",status:"idle",runner:{profile:"runtime"}}];
  api.history.set("A",[message(1,"@reviewer 请确认；@tester 请看")]);
  api.hook=path=>path==="/v1/member/list" ? {members} : undefined;
  const ui=await launch(api),input=ui.el("message-input"),mirror=ui.el("message-mirror");
  const text="@reviewer 请看 <script>text</script>\n普通文字 @unknown mail@reviewer.com";
  await ui.draft(text);
  assert.equal(mirror.hidden,false);
  assert.equal(input.classList.contains("has-mentions"),true);
  assert.equal(mirror.children.map(el=>el.textContent).join(""),text);
  assert.deepEqual(mirror.children.filter(el=>el.className==="mention-member").map(el=>el.textContent),["@reviewer"]);
  input.scrollTop=44;await input.emit("scroll");assert.equal(mirror.scrollTop,44);
  assert.deepEqual(ui.document.getElementById("message-1").querySelector(".message-text").markdownOptions.memberNames,["reviewer"]);
  members=[];await ui.refresh();assert.equal(mirror.hidden,true);assert.equal(input.classList.contains("has-mentions"),false);
  assert.deepEqual(ui.document.getElementById("message-1").querySelector(".message-text").markdownOptions.memberNames,[]);
  await ui.select("B");assert.equal(mirror.hidden,true);assert.equal(input.value,"");
  members=[{agent_id:"reviewer",membership_id:"member-reviewer",status:"idle",runner:{profile:"runtime"}}];
  await ui.select("A");assert.equal(input.value,text);assert.equal(mirror.hidden,false);
});

test("mention candidates follow current members and close when switching Rooms", async () => {
  const api=server([makeRoom("A"),makeRoom("B")]);
  let members=[{agent_id:"reviewer",membership_id:"member-reviewer",status:"idle",runner:{profile:"runtime"}}];
  api.hook=path=>path==="/v1/member/list" ? {members} : undefined;
  const ui=await launch(api),input=ui.el("message-input"),list=ui.el("mention-options");
  input.value="@";input.selectionStart=input.selectionEnd=1;await input.emit("input");
  const old=list.children[0];members=[];await ui.refresh();await old.emit("click");
  assert.equal(input.value,"@");assert.equal(list.hidden,true);
  await ui.refresh();await input.emit("click");assert.equal(list.hidden,true);
  members=[{agent_id:"developer",membership_id:"member-developer",status:"paused",runner:{profile:"runtime"}}];
  await ui.refresh();await input.emit("click");assert.equal(list.children[0].textContent,"@developer");
  await ui.select("B");assert.equal(list.hidden,true);assert.equal(input.getAttribute("aria-expanded"),"false");
  await ui.select("A");assert.equal(input.value,"@");assert.equal(list.hidden,true);
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
  assert.match(b.el("room-list").children.find(row => row.children[0].children[0].textContent === "A").children[0].children[2].textContent, /A cleanup incomplete/);
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


function inboxFixture(latest = 60) {
  const api = server([makeRoom("A"),makeRoom("B")]);
  api.member = {agent_id:"assistant",membership_id:"1".repeat(32),status:"idle",runner:{profile:"runtime"},execution:{paused:true,agent:{turn_number:0}}};
  api.latest = latest; api.expired = 0; api.chunk = 25; api.inboxHook = null;
  api.inboxStatus = () => ({latest_seq:api.latest,expired_through:api.expired,retained_entries:api.latest-api.expired,consumed_through:0,skipped_through:api.expired,skipped_entries:0,bytes:100,limits:{bytes:1000,entries:10000}});
  api.inboxPage = data => {
    if(data.after<api.expired)throw Object.assign(new Error("expired"),{code:"inbox_expired"});
    const end=Math.min(data.snapshot_seq,data.after+data.limit,data.after+api.chunk);
    return {snapshot_seq:data.snapshot_seq,expired_through:api.expired,next_after:end,has_more:end<data.snapshot_seq,status:api.inboxStatus(),entries:Array.from({length:end-data.after},(_,i)=>({seq:data.after+i+1,item:{provider:"gitlab",channel:"todos",text:`notice-${data.after+i+1}`},source:{status:"not_checked"}}))};
  };
  api.hook=(path,data)=>{
    const result=api.inboxHook?.(path,data);if(result!==undefined)return result;
    if(path==="/v1/member/list")return {members:[api.member]};
    if(path==="/v1/member/activity")return {member:api.member};
    if(path==="/v1/member/inbox/status")return api.inboxStatus();
    if(path==="/v1/member/inbox")return api.inboxPage(data);
  };
  return api;
}
async function openInboxFixture(api) {
  const ui=await launch(api);
  await ui.el("members").children[0].querySelector(".member-summary").emit("click");
  await ui.el("agent-tab-inbox").emit("click");await settle();return ui;
}
const inboxSequences = ui => ui.el("inbox-entries").children.map(row=>Number(row.dataset.inboxSeq));

test("Inbox renders mixed internal events without platform controls and retains rows on refresh", async () => {
  const api = inboxFixture(3), original = api.inboxPage;
  api.inboxPage = data => {
    const page = original(data);
    page.entries = page.entries.map(entry => entry.seq === 2 ? {
      type:"task_event", seq:2, task_event:{event_id:"event-safe", operation_id:"connect-original", result_version:2, source_seq:1,
        event_type:"operation_completed", result_ref:"connect/original/2", created_at:"2026-11-11T01:00:00Z"},
    } : entry.seq === 3 ? {type:"unknown",seq:3} : {...entry,type:"platform_notification"});
    return page;
  };
  const ui = await openInboxFixture(api), rows = ui.el("inbox-entries").children;
  assert.deepEqual(inboxSequences(ui), [1,2,3]);
  assert.match(elementText(rows[0]), /GitLab/);
  assert.match(elementText(rows[1]), /内部事件/);
  assert.match(elementText(rows[1]), /操作结果已保存/);
  assert.match(elementText(rows[1]), /不代表原需求已完成/);
  assert.match(elementText(rows[1]), /connect-original/);
  assert.doesNotMatch(elementText(rows[1]), /通知来源设置|尚未解析|undefined/);
  assert.match(elementText(rows[2]), /无法识别的 Inbox 条目类型/);
  const row = rows[1]; row.children.at(-1).open = true;
  await ui.el("inbox-reload").emit("click");
  assert.equal(ui.el("inbox-entries").children[1], row);
  assert.equal(row.children.at(-1).open, true);
  assert.equal(api.calls.some(call => /confirm|resolve/.test(call.path)), false);
});

test("Inbox opens the latest page, fills short byte-limited reads, and browses a fixed snapshot",async()=>{
  const api=inboxFixture();api.chunk=7;const ui=await openInboxFixture(api);
  assert.deepEqual(inboxSequences(ui),Array.from({length:25},(_,i)=>i+36));
  assert.equal(ui.el("inbox-page").textContent,"第 1 页");
  assert.match(ui.el("inbox-status").textContent,/保留 60 条/);
  assert.equal(ui.el("inbox-prev").disabled,true);
  const reads=()=>api.calls.filter(c=>c.path==="/v1/member/inbox");
  assert.deepEqual(reads().map(c=>c.data.after),[35,42,49,56]);
  ui.el("inbox-member-view").scrollTop=500;
  await ui.el("inbox-next-bottom").emit("click");
  assert.deepEqual(inboxSequences(ui),Array.from({length:25},(_,i)=>i+11));
  assert.equal(ui.el("inbox-member-view").scrollTop,0);
  assert.equal(ui.el("inbox-page-bottom").textContent,ui.el("inbox-page").textContent);
  assert.equal(ui.el("inbox-page").textContent,"第 2 页");
  api.latest=65;
  await ui.el("inbox-next").emit("click");assert.deepEqual(inboxSequences(ui),Array.from({length:10},(_,i)=>i+1));
  assert.equal(ui.el("inbox-page").textContent,"第 3 页");
  assert.equal(ui.el("inbox-next").disabled,true);
  await ui.el("inbox-prev-bottom").emit("click");
  // Return using sequence windows; the last partial old page must not skip #11.
  assert.deepEqual(inboxSequences(ui),Array.from({length:25},(_,i)=>i+11));
  assert.equal(ui.el("inbox-page").textContent,"第 2 页");
  assert.ok(reads().every(c=>c.data.snapshot_seq===60));
  assert.equal(api.calls.some(c=>/confirm|member\/run/.test(c.path)),false);
});

test("Inbox polling and manual refresh preserve history, expanded evidence, focus and position; latest is explicit",async()=>{
  const api=inboxFixture(),ui=await openInboxFixture(api);
  await ui.el("inbox-next").emit("click");
  const row=ui.el("inbox-entries").children[0],details=row.children.at(-1);details.open=true;
  ui.el("inbox-member-view").scrollTop=450;details.children[0].focus();
  const before=api.calls.filter(c=>c.path==="/v1/member/inbox").length;
  api.latest=63;await ui.refresh();await settle();
  assert.equal(api.calls.filter(c=>c.path==="/v1/member/inbox").length,before,"polling only inspects status");
  assert.match(ui.el("inbox-latest").textContent,/有更新/);
  assert.match(ui.el("inbox-status").textContent,/保留 63 条/);
  await ui.el("inbox-reload").emit("click");
  assert.equal(ui.el("inbox-entries").children[0],row);assert.equal(details.open,true);
  assert.equal(ui.el("inbox-member-view").scrollTop,450);
  assert.equal(ui.document.activeElement,details.children[0]);
  assert.equal(ui.el("inbox-page").textContent,"第 2 页");
  assert.equal(inboxSequences(ui)[0],11);
  await ui.el("inbox-latest").emit("click");
  assert.equal(inboxSequences(ui).at(-1),63);assert.equal(ui.el("inbox-member-view").scrollTop,0);
  assert.equal(ui.el("inbox-latest").textContent,"最新");
  assert.equal(ui.el("inbox-page").textContent,"第 1 页");
});

test("Inbox cleanup invalidates visible history and races in short reads relocate once",async()=>{
  const api=inboxFixture(),ui=await openInboxFixture(api);
  await ui.el("inbox-next").emit("click");
  api.expired=20;await ui.refresh();await settle();
  assert.equal(inboxSequences(ui)[0],36);assert.equal(ui.el("inbox-page").textContent,"第 1 页");assert.match(ui.el("inbox-notice").textContent,/已过期.*重新定位/);
  assert.match(ui.el("inbox-status").textContent,/保留 40 条/);
  api.chunk=5;let race=true;
  api.inboxHook=(path,data)=>{if(path==="/v1/member/inbox" && data.after===40 && race){race=false;api.expired=45;return api.inboxPage(data);}};
  await ui.el("inbox-reload").emit("click");
  assert.equal(inboxSequences(ui)[0],46);assert.equal(inboxSequences(ui).at(-1),60);
  assert.equal(ui.el("inbox-page").textContent,"第 1 页");
  api.expired=api.latest;await ui.refresh();await settle();
  assert.deepEqual(inboxSequences(ui),[]);assert.equal(ui.el("inbox-next").disabled,true);
});

test("Inbox read failures retain the page and retry, and loading blocks duplicate navigation",async()=>{
  const api=inboxFixture(),ui=await openInboxFixture(api);let reject;
  api.inboxHook=path=>{if(path==="/v1/member/inbox")return new Promise((_,r)=>reject=r);};
  const row=ui.el("inbox-entries").children[0];
  const pending=ui.el("inbox-next").emit("click");await settle();
  for(const id of ["inbox-reload","inbox-latest","inbox-prev","inbox-next","inbox-next-bottom"])assert.equal(ui.el(id).disabled,true);
  const count=api.calls.length;await ui.el("inbox-next-bottom").emit("click");assert.equal(api.calls.length,count);
  reject(new Error("offline"));await pending;
  assert.equal(ui.el("inbox-entries").children[0],row);assert.match(ui.el("inbox-notice").textContent,/当前页已保留/);
  assert.equal(ui.el("inbox-page").textContent,"第 1 页");
  await ui.refresh();await settle();assert.match(ui.el("inbox-notice").textContent,/读取失败/);
  api.inboxHook=null;await ui.el("inbox-next").emit("click");assert.equal(inboxSequences(ui)[0],11);
  assert.equal(ui.el("inbox-page").textContent,"第 2 页");
});

test("Inbox late reads and status checks cannot cross Room or Membership contexts",async()=>{
  for(const operation of ["page","status"]) {
    const api=inboxFixture(),ui=await openInboxFixture(api);let finish;
    api.inboxHook=(path,data)=>{
      if(path===(operation==="page"?"/v1/member/inbox":"/v1/member/inbox/status"))return new Promise(resolve=>finish=()=>resolve(operation==="page"?api.inboxPage(data):api.inboxStatus()));
    };
    const pending=operation==="page"?ui.el("inbox-next").emit("click"):ui.refresh();await settle();
    api.inboxHook=null;await ui.select("B");finish();await pending;await settle();
    assert.equal(ui.el("inbox-entries").children.length,0);
    await ui.el("members").children[0].querySelector(".member-summary").emit("click");await ui.el("agent-tab-inbox").emit("click");await settle();
    api.inboxHook=(path,data)=>path==="/v1/member/inbox"?new Promise(resolve=>finish=()=>resolve(api.inboxPage(data))):undefined;
    const late=ui.el("inbox-next").emit("click");await settle();
    api.member={...api.member,membership_id:"2".repeat(32)};api.inboxHook=null;
    await ui.refresh();await settle();finish();await late;
    assert.equal(ui.el("activity-dialog").open,false);
  }
});

test("Inbox initial failures and empty snapshots retain a working refresh entry",async()=>{
  const api=inboxFixture(0);api.inboxHook=path=>{if(path==="/v1/member/inbox/status")throw new Error("offline");};
  const ui=await openInboxFixture(api);assert.match(ui.el("inbox-notice").textContent,/请点击刷新重试/);
  api.inboxHook=null;await ui.el("inbox-reload").emit("click");
  assert.equal(ui.el("inbox-page").textContent,"暂无通知");assert.equal(ui.el("inbox-next").disabled,true);
  api.latest=1;await ui.refresh();await settle();assert.match(ui.el("inbox-latest").textContent,/有更新/);
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
    if(path==="/v1/member/inbox/status")return {latest_seq:items.length,expired_through:0,retained_entries:items.length,bytes:100,limits:{bytes:1000}};
    if(path==="/v1/member/inbox")return {snapshot_seq:items.length,next_after:items.length,has_more:false,status:{latest_seq:items.length,expired_through:0,retained_entries:items.length,bytes:100,limits:{bytes:1000}},entries:items.map((item,i)=>({seq:i+1,origin:i===7?{kind:"agent",agent_id:"assistant",status:"succeeded"}:undefined,source:{source_ref:`src_${i}`,status:"not_checked"},received_at:"2026-11-06T08:06:57Z",item:{...item,created_at:"2026-11-06T07:00:00Z"}}))};
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
  const ordinary = ui.el("messages").children.find(el => el.id === "message-2").querySelector(".message-text").markdownOptions;
  assert.equal(ordinary.attachmentURL, undefined);
  assert.equal(ordinary.quotedMarkdown, undefined);
  assert.deepEqual(ordinary.memberNames, []);
});

test("Lark Inbox preview binds image requests to the saved membership and sequence", async () => {
  const api = server([makeRoom("A")]);
  const member = {agent_id:"assistant",membership_id:"1".repeat(32),status:"idle",runner:{profile:"runtime"},execution:{paused:true,agent:{turn_number:0}}};
  const source = {provider:"lark",channel:"messages",reference:{version:2,target:{type:"message"}}};
  api.hook = (path) => {
    if (path === "/v1/member/list") return {members:[member]};
    if (path === "/v1/member/activity") return {member};
    if (path === "/v1/member/inbox/status") return {latest_seq:9,expired_through:8,retained_entries:1,bytes:100,limits:{bytes:1000}};
    if (path === "/v1/member/inbox") return {snapshot_seq:9,next_after:9,has_more:false,status:{latest_seq:9,expired_through:8,retained_entries:1,bytes:100,limits:{bytes:1000}},entries:[{seq:9,item:{provider:"lark",channel:"messages",message_id:{value:"om_fixture"},text:"image_key: img_fixture\n请分析图片"}}]};
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
 assert.match(elementText(card.children[3]),/会话消息 #18.*来源用户 tester/s);
 assert.match(elementText(card.children[3]),/原始来源.*记住这类报错.*Agent 理解：上下文明确/s);
 await card.children[0].children[1].children[0].emit("click");
 assert.equal(ui.el("policy-editor-title").textContent,"修改规则");
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
 const before=api.calls.filter(c=>c.path.startsWith("/v1/platforms")).length;
 await ui.el("policy-new").emit("click");await settle();
 for(const id of ["policy-kind","policy-rule-fields","policy-connection","policy-project","policy-object-type"])assert.equal(ui.el(id),undefined);
 ui.el("policy-text").value="项目 A 的使用问题核实现行文档后直接回复；涉及交付时间先问我。";
 await ui.el("policy-form").emit("submit");await settle();
 const change=api.calls.find(c=>c.path==="/v1/policy/update").data.policy;
 assert.equal(change.text,"项目 A 的使用问题核实现行文档后直接回复；涉及交付时间先问我。");
 for(const key of ["kind","scope","scope_from","actions","conditions","exceptions"])assert.equal(Object.hasOwn(change,key),false);
 assert.equal(api.calls.filter(c=>c.path.startsWith("/v1/platforms")).length,before);
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
 assert.equal(api.calls.filter(c=>c.path==="/v1/policy/update").length,1);assert.match(ui.el("policy-status").textContent,/重试读取.*核对/);
 const readsBeforeRetry=api.calls.filter(c=>c.path==="/v1/policy/read").length;await ui.refresh();await settle();assert.equal(api.calls.filter(c=>c.path==="/v1/policy/read").length,readsBeforeRetry);
 delay=true;const pending=ui.el("policy-retry").emit("click");await settle();await ui.select("B");finish();await pending;await settle();
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
  if(path==="/v1/matters/source" && data.followup.seq===2)return {source:{url:"https://gitlab.test/p/-/merge_requests/42"},delivery_id:"expired-delivery",resolution:{status:"resolved"},entry_status:"expired"};
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
 await expandDisclosure(sourceList.children.at(-1));assert.match(elementText(sourceList.children.at(-1)),/原通知正文已过期.*跟进与动作记录继续保留/);
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
async function openGitLab(ui) { await openNotification(ui,"gitlab"); await settle(); }
async function editGitLab(ui, id, value, event = "change") { ui.el(id).value = value; await ui.el(id).emit(event); }
async function selectGitLabProject(ui, path) {
  await expandDisclosure(ui.el("gitlab-project-picker"));
  const input=ui.el("gitlab-project-options").children.map(row=>row.children[0]).find(input=>input.value===path);
  assert.ok(input,`project ${path} is listed`);input.checked=true;await input.emit("change");
}
async function toggleGitLab(ui, id, checked) { ui.el(id).checked = checked; await ui.el(id).emit("change"); }

test("notification account status stays separate from connection management",async()=>{
  const html=await readFile(new URL("../assets/index.html",import.meta.url),"utf8"),ids=[...html.matchAll(/\bid="([^"]+)"/g)].map(match=>match[1]);
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
test("GitLab channel switches preserve disclosure state and retain independent drafts",async()=>{
  const {api,events}=todosServer();events.get("A").config.enabled=false;
  const ui=await launch(api);await openGitLab(ui);
  assert.equal(ui.el("gitlab-sync").hidden,false);assert.equal(ui.el("gitlab-sync").disabled,true);
  await toggleGitLab(ui,"gitlab-todos-enabled",true);
  assert.equal(ui.el("gitlab-todos-settings").open,false);assert.equal(ui.el("gitlab-events-enabled").checked,false);
  await toggleGitLab(ui,"gitlab-events-enabled",true);
  assert.equal(ui.el("gitlab-events-settings").open,false);
  await expandDisclosure(ui.el("gitlab-events-settings"));
  await toggleGitLab(ui,"gitlab-events-enabled",false);assert.equal(ui.el("gitlab-events-settings").open,true);
  await toggleGitLab(ui,"gitlab-events-enabled",true);assert.equal(ui.el("gitlab-events-settings").open,true);
  await ui.el("close-notification").emit("click");await openGitLab(ui);
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
  await openNotification(ui,"gitlab","b".repeat(32));
  await editGitLab(ui,"gitlab-interval","60","input");
  assert.equal(ui.el("gitlab-events-save"), undefined); assert.equal(ui.el("gitlab-todos-save"), undefined); await ui.el("gitlab-save").emit("click");
  assert.equal(gitlabWrites(api).length,1);
  const body=gitlabWrites(api)[0].data;
  assert.equal(body.version,0); assert.equal(body.channel,undefined); assert.equal(body.subscription_id,undefined);
  assert.equal(body.config.platform_id,"b".repeat(32));
  assert.equal(body.config.interval_seconds,60);
  for(const source of [body.config.events,body.config.todos]) { assert.equal(source.platform_id,undefined); assert.equal(source.target,undefined); assert.equal(source.interval_seconds,undefined); }
  assert.equal(body.config.todos.project_scope,undefined);assert.equal(body.config.todos.inactive_interval_seconds,undefined);
  for (const source of [events,todos]) assert.equal(source.get("A").config.interval_seconds,60);
  assert.equal(events.get("A").config.platform_id,"b".repeat(32)); assert.equal(todos.get("A").config.platform_id,"b".repeat(32));
  assert.equal(ui.el("gitlab-save").disabled,true);
  await ui.el("gitlab-sync").emit("click");
  const sync=api.calls.find(c=>c.path===gitlabPath("sync")); assert.equal(sync.data.version,1); assert.equal(sync.data.config,undefined);
});

test("GitLab can save common settings while both message types are disabled", async()=>{
  const {api,events}=todosServer(); Object.assign(events.get("A"),{subscription_version:0,status:"disabled"});events.get("A").config.enabled=false;
  const ui=await launch(api);await openGitLab(ui);await editGitLab(ui,"gitlab-connection","b".repeat(32));
  await ui.el("gitlab-save").emit("click");assert.equal(gitlabWrites(api).length,1);
  assert.equal(api.gitlabRecords.get("A").config.platform_id,"b".repeat(32));
  await ui.refresh();assert.equal(ui.el("gitlab-connection").value,"b".repeat(32));assert.equal(ui.el("gitlab-events-enabled").checked,false);
});

test("GitLab room target is fixed and ignores a forged recipient selector",async()=>{
  const {api}=todosServer({enabled:true}),ui=await launch(api);await openGitLab(ui);
  await editGitLab(ui,"gitlab-target",JSON.stringify({mode:"inbox",agent_id:"other",membership_id:"other"}));
  await editGitLab(ui,"gitlab-interval","60","input");await ui.el("gitlab-save").emit("click");
  assert.deepEqual(gitlabWrites(api)[0].data.config.target,{mode:"room"});
  assert.equal(ui.el("gitlab-target").disabled,true);
});

test("GitLab enables one selected type while retaining one common config",async()=>{
  const {api,events,todos}=todosServer({bound:false});events.get("A").config.enabled=false;events.get("A").subscription_version=0;
  const ui=await launch(api);await openGitLab(ui);await editGitLab(ui,"gitlab-connection","a".repeat(32));
  await toggleGitLab(ui,"gitlab-todos-enabled",true);await ui.el("gitlab-save").emit("click");
  assert.equal(gitlabWrites(api).length,1);assert.equal(events.get("A").config.enabled,false);assert.equal(todos.get("A").config.enabled,true);
});

test("GitLab public draft survives polling, detail reopening, rail closing and connection management",async()=>{
  const {api}=todosServer({enabled:true}),ui=await launch(api);await openGitLab(ui);
  await selectGitLabProject(ui,"group/changed");await editGitLab(ui,"gitlab-interval","60","input");
  await ui.refresh();await ui.el("close-notification").emit("click");await openGitLab(ui);
  await ui.el("gitlab-manage-platforms").emit("click");await ui.el("close-settings").emit("click");await settle();
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
  await toggleGitLab(ui,"gitlab-todos-all-reasons",false);
  await ui.el("gitlab-save").emit("click");assert.deepEqual(gitlabWrites(api)[0].data.config.todos.filter.reasons,[]);
  await ui.refresh();assert.equal(ui.el("gitlab-todos-reason-mentioned").checked,false);
});

test("GitLab todo select-all controls track partial selections and keep groups independent",async()=>{
  const {api}=todosServer({enabled:true}),ui=await launch(api);await openGitLab(ui);
  const groups = [
    ["reasons","reason",["mentioned","assigned","review_requested","other"]],
    ["types","type",["merge_request","work_item"]],
  ];
  for (const [group,prefix,values] of groups) {
    const all=ui.el(`gitlab-todos-all-${group}`),first=`gitlab-todos-${prefix}-${values[0]}`;
    const other=ui.el(`gitlab-todos-all-${group === "reasons" ? "types" : "reasons"}`);
    assert.equal(all.checked,true);assert.equal(all.indeterminate,false);
    await toggleGitLab(ui,first,false);
    assert.equal(all.checked,false);assert.equal(all.indeterminate,true);
    await toggleGitLab(ui,`gitlab-todos-all-${group}`,true);
    assert.equal(all.checked,true);assert.equal(all.indeterminate,false);
    for(const value of values)assert.equal(ui.el(`gitlab-todos-${prefix}-${value}`).checked,true);
    await toggleGitLab(ui,`gitlab-todos-all-${group}`,false);
    assert.equal(all.checked,false);assert.equal(all.indeterminate,false);
    for(const value of values)assert.equal(ui.el(`gitlab-todos-${prefix}-${value}`).checked,false);
    assert.equal(other.checked,true);assert.equal(ui.el("gitlab-events-all-types").checked,true);
    await toggleGitLab(ui,first,true);
    assert.equal(all.checked,false);assert.equal(all.indeterminate,true);
    await toggleGitLab(ui,`gitlab-todos-all-${group}`,true);
  }
  await toggleGitLab(ui,"gitlab-todos-all-reasons",false);
  await toggleGitLab(ui,"gitlab-todos-type-work_item",false);
  assert.equal(gitlabWrites(api).length,0);
  await ui.el("gitlab-save").emit("click");
  assert.deepEqual(gitlabWrites(api)[0].data.config.todos.filter,{reasons:[],types:["merge_request"]});
  await ui.refresh();
  assert.equal(ui.el("gitlab-todos-all-reasons").checked,false);
  assert.equal(ui.el("gitlab-todos-all-reasons").indeterminate,false);
  assert.equal(ui.el("gitlab-todos-all-types").checked,false);
  assert.equal(ui.el("gitlab-todos-all-types").indeterminate,true);
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
  assert.equal(api.gitlabRecords.get("A").config.platform_id,"b".repeat(32));assert.equal(todos.get("A").config.platform_id,"a".repeat(32));
  assert.equal(ui.el("gitlab-save").textContent,"重试应用");assert.equal(ui.el("gitlab-sync").disabled,true);assert.equal(ui.el("gitlab-todos-settings").open,true);
  await ui.refresh();assert.equal(ui.el("gitlab-save").textContent,"重试应用");assert.equal(gitlabWrites(api).length,1);
  api.gitlabApplyFailure.clear();await ui.el("gitlab-save").emit("click");
  assert.equal(gitlabWrites(api).length,2);assert.equal(gitlabWrites(api)[1].data.version,1);assert.equal(events.get("A").subscription_version,eventVersion);
  assert.equal(todos.get("A").config.platform_id,"b".repeat(32));assert.equal(ui.el("gitlab-save").disabled,true);
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
  api.gitlabRecords.set("A",{version:1,config:structuredClone(fixtureGitLabView([eventView({config:{enabled:true,platform_id:"a".repeat(32),project:"group/project-A",interval_seconds:5,filter:{}},metrics:{}}),todoView({config:{enabled:true,platform_id:"a".repeat(32),interval_seconds:30,filter:{}}})]).config)});
  api.platforms[0].validation={status:"valid",user_id:303,username:"carol"};
  await ui.el("gitlab-manage-platforms").emit("click");await ui.el("close-settings").emit("click");await settle();
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
  api.platforms.push({id:"c".repeat(32),provider:"scion",name:"Scion demo",url:"https://scion.invalid",version:3,validation:{status:"valid",username:"alice"}});
  api.platforms.push({id:"d".repeat(32),provider:"scion",name:"Unverified",url:"https://scion.invalid",version:1,validation:{status:"unverified"}});
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
  assert.deepEqual(sent.target.binding,{provider:"scion",url:"https://scion.invalid",project:"OpenCSG/teamx-demo",platform_id:"c".repeat(32)});
  assert.equal(sent.platform_version,3);
  await ui.el("open-workspace").emit("click");
  assert.equal(ui.el("workspace-kind").value,"scion");assert.equal(ui.el("workspace-kind").disabled,true);
  assert.match(ui.el("workspace-credential-status").textContent,/Scion.*OpenCSG\/teamx-demo/);
  const reload=await launch(api,ui.saved);await reload.el("open-workspace").emit("click");
  assert.equal(reload.el("workspace-kind").value,"scion");assert.equal(reload.el("workspace-connection").value,"c".repeat(32));
});

test("Scion Workspace rejects unverified and mismatched platforms before retaining an operation", async () => {
  for(const valid of [false,true]){
    const api=workspaceServer();api.platforms.push({id:"c".repeat(32),provider:"scion",name:"Scion",url:"https://other.invalid",version:1,validation:{status:valid ? "valid" : "unverified"}});
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
  assert.equal(first.search,"other");assert.equal(first.page,1);assert.equal(first.platform_id,"a".repeat(32));
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
  await openNotification(ui,"gitlab","b".repeat(32));await expandDisclosure(ui.el("gitlab-project-picker"));await settle();
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
  assert.equal(ui.el("gitlab-project-summary").textContent,"请先选择平台");
  assert.equal(ui.el("gitlab-project-summary").attributes["aria-disabled"],"true");
  assert.equal(ui.el("gitlab-project-hint").hidden,false);assert.match(ui.el("gitlab-project-hint").textContent,/在上方选择平台/);
  api.platforms[0].validation.status="unverified";
  await openGitLab(ui);
  await editGitLab(ui,"gitlab-connection","a".repeat(32));
  assert.equal(ui.el("gitlab-project-summary").textContent,"请先验证平台");
  assert.match(ui.el("gitlab-project-hint").textContent,/管理平台/);
  api.platforms[0].validation.status="valid";
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
 await ui.el("close-notification").emit("click");await openGitLab(ui);
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

test("DSH threshold shows Session counts and keeps pause available during automatic rotation", async () => {
  const api = server([makeRoom("A")]);
  const member = {agent_id:"reviewer",membership_id:"generation-reviewer",status:"rotating",runner:{profile:"runtime"},unread_count:1,read_through:6,
    execution:{paused:false,active:false,session_turns:{used:2,limit:2,total:6,baseline_known:true},agent:{turn_number:6},rotation:{reason:"session_max_turns",phase:"old_closed",status:"closing"}}};
  api.hook = path => {
    if(path==="/v1/member/list")return {members:[structuredClone(member)]};
    if(path==="/v1/member/activity")return {member:structuredClone(member)};
    if(path==="/v1/member/pause"){member.execution.paused=true;return structuredClone(member.execution)};
  };
  const ui=await launch(api);
  await ui.el("members").children[0].querySelector(".member-summary").emit("click"); await ui.el("agent-tab-activity").emit("click");await settle();
  assert.equal(ui.el("activity-budget").textContent,"2 / 2 · 累计 6");
  assert.match(ui.el("activity-rotation").textContent,/达到会话回合阈值 · old_closed/);
  assert.equal(ui.el("agent-enable-hint").hidden,true);
  const pause=ui.el("agent-actions").children[0];
  assert.equal(pause.textContent,"暂停");assert.equal(pause.disabled,false);
  await pause.emit("click");await settle();
  assert.equal(api.calls.filter(c=>c.path==="/v1/member/pause").length,1);
  assert.equal(api.calls.some(c=>c.path==="/v1/member/run"||c.path==="/v1/member/session/rotate"),false);
});

test("GitLab platform selection reads saved preferences without copying another platform's draft", async () => {
  const {api} = todosServer({enabled:true}), hook = api.hook;
  let first;
  const second = structuredClone(fixtureGitLabView([todoView({config:{enabled:false,platform_id:"b".repeat(32),interval_seconds:60,filter:{reasons:["assigned"],types:["work_item"]}}})]));
  second.platform_id = "b".repeat(32); second.version = 9; second.config.enabled = false;
  api.hook = (path, body) => {
    if (path.startsWith("/v1/platform/notifications?")) {
      const data = hook(path, body);
      first = structuredClone(data.gitlab); first.platform_id = "a".repeat(32); first.config.enabled = true;
      return {...data, gitlab:first, gitlab_platforms:[first,second]};
    }
    if (path.startsWith("/v1/platform/gitlab/notifications?")) return structuredClone(new URL(path,"http://fixture").searchParams.get("platform_id") === second.platform_id ? second : first);
    return hook(path, body);
  };
  const ui = await launch(api); await openGitLab(ui);
  await editGitLab(ui,"gitlab-interval","73","input");
  await editGitLab(ui,"gitlab-connection",second.platform_id);
  assert.equal(String(ui.el("gitlab-interval").value),"60");
  assert.equal(ui.el("gitlab-todos-enabled").checked,false);
  assert.equal(ui.el("gitlab-todos-reason-assigned").checked,true);
  assert.equal(ui.el("gitlab-todos-reason-mentioned").checked,false);
  assert.equal(ui.el("gitlab-save").disabled,true);
  assert.equal(gitlabWrites(api).length,0);
});


function ruleRefreshFixture() {
  const api=server([makeRoom("A"),makeRoom("B")]);
  const member={agent_id:"assistant",membership_id:"rule-member",status:"idle",runner:{profile:"runtime"}};
  const rule={id:"reply",version:1,status:"active",text:"先核实再回复",source:{user:"tester",text:"先核实再回复"},changed_by:{kind:"human",id:"tester"},updated_at:"2026-11-10T00:00:00Z"};
  api.rules={revision:1,current:[rule],history:[rule],effective:[rule]};
  api.readRules=()=>structuredClone(api.rules);
  api.hook=(path)=>{
    if(path==="/v1/member/list")return {members:[member]};
    if(path==="/v1/policy/read")return api.readRules();
    if(path==="/v1/policy/update")return {};
    if(path==="/v1/member/inbox/status")return {latest_seq:0,expired_through:0,retained_entries:0,limits:{bytes:1000},bytes:0};
    if(path==="/v1/member/inbox")return {snapshot_seq:0,entries:[],has_more:false,status:{latest_seq:0,expired_through:0,retained_entries:0,limits:{bytes:1000},bytes:0}};
    if(path==="/v1/matters/list")return {items:[],total:0,complete:true};
  };
  return api;
}
async function openRuleView(api) {
  const ui=await launch(api);await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();
  await ui.el("agent-tab-policy").emit("click");await settle();return ui;
}
const ruleReads=api=>api.calls.filter(c=>c.path==="/v1/policy/read").length;

test("rules update automatically without rebuilding unchanged cards or moving reading position",async()=>{
  const api=ruleRefreshFixture(),ui=await openRuleView(api);
  assert.equal(ui.el("policy-reload"),undefined);assert.equal(ui.el("policy-retry").hidden,true);
  const original=ui.el("policy-entries").children[0];original.children[3].open=true;ui.el("policy-member-view").scrollTop=123;
  await ui.refresh();await settle();assert.equal(ui.el("policy-entries").children[0],original);
  api.rules.current[0].text="先查最新文档再回复";api.rules.revision=2;
  await ui.refresh();await settle();
  const updated=ui.el("policy-entries").children[0];assert.match(elementText(updated),/先查最新文档再回复/);
  assert.equal(updated.children[3].open,true);assert.equal(ui.el("policy-member-view").scrollTop,123);
  assert.equal(api.calls.some(c=>/policy\/update|inbox\/(read|confirm)|member\/run/.test(c.path)),false);
});

test("rule polling protects draft fields and rejects a response started before editing",async()=>{
  const api=ruleRefreshFixture(),ui=await openRuleView(api);let finish;
  api.readRules=()=>new Promise(resolve=>finish=resolve);
  await ui.refresh();await settle();
  await ui.el("policy-new").emit("click");ui.el("policy-text").value="未保存的草稿";ui.el("policy-expires").value="2099-01-01T12:30";
  const reads=ruleReads(api);await ui.refresh();await settle();assert.equal(ruleReads(api),reads);
  finish({revision:9,current:[],history:[],effective:[]});await settle();
  assert.equal(ui.el("policy-form").hidden,false);assert.equal(ui.el("policy-text").value,"未保存的草稿");assert.equal(ui.el("policy-expires").value,"2099-01-01T12:30");
  assert.equal(ui.el("policy-entries").children.length,1);
  api.readRules=()=>({revision:2,current:[],history:[],effective:[]});
  await ui.el("policy-cancel").emit("click");await settle();assert.equal(ui.el("policy-entries").children.length,0);
});

test("rule read errors retain the list and show Retry only until a successful read",async()=>{
  const api=ruleRefreshFixture(),ui=await openRuleView(api),card=ui.el("policy-entries").children[0];
  api.readRules=()=>{throw Error("offline");};await ui.refresh();await settle();
  assert.equal(ui.el("policy-entries").children[0],card);assert.equal(ui.el("policy-retry").hidden,false);assert.match(ui.el("policy-status").textContent,/读取失败/);
  api.readRules=()=>structuredClone(api.rules);await ui.el("policy-retry").emit("click");await settle();
  assert.equal(ui.el("policy-retry").hidden,true);assert.equal(ui.el("policy-new").disabled,false);
  await ui.el("agent-tab-inbox").emit("click");const reads=ruleReads(api);await ui.refresh();await settle();assert.equal(ruleReads(api),reads);
});

test("an initial rule read failure recovers automatically and enables editing",async()=>{
  const api=ruleRefreshFixture();api.readRules=()=>{throw Error("offline");};const ui=await openRuleView(api);
  assert.equal(ui.el("policy-retry").hidden,false);assert.equal(ui.el("policy-new").disabled,true);
  api.readRules=()=>structuredClone(api.rules);await ui.refresh();await settle();
  assert.equal(ui.el("policy-retry").hidden,true);assert.equal(ui.el("policy-new").disabled,false);assert.equal(ui.el("policy-entries").children.length,1);
});

test("a late automatic rule response cannot replace a new tab context",async()=>{
  const api=ruleRefreshFixture(),ui=await openRuleView(api);let finish;
  api.readRules=()=>new Promise(resolve=>finish=resolve);await ui.refresh();await settle();
  api.readRules=()=>structuredClone(api.rules);await ui.el("agent-tab-inbox").emit("click");await ui.el("agent-tab-policy").emit("click");await settle();
  const card=ui.el("policy-entries").children[0];finish({revision:99,current:[],history:[],effective:[]});await settle();assert.equal(ui.el("policy-entries").children[0],card);
});

test("direct conversations prefer the exact default, keep the inspector stable, and isolate Room drafts", async () => {
  const room=makeRoom("Team"),dm={...makeRoom("个人助理","dm"),is_direct:true,team_id:"",agent_role_id:"assistant"};
  const api=server([room,dm]);api.catalog.default_assistant={status:"completed",room_id:dm.room_id,room_instance:dm.room_instance};
  const member={agent_id:"assistant",membership_id:"dm-member",status:"paused",runner:{profile:"runtime"},execution:{paused:true}};
  api.hook=(path,data)=>{
    if(path==="/v1/member/list")return {members:[{...member,membership_id:data.room_id==="dm" ? "dm-member" : "team-member"}]};
    if(path.startsWith("/v1/tools/bindings?"))return {catalog:[],bindings:[]};
    if(path==="/v1/member/activity")return {member};
  };
  const ui=await launch(api);await settle();
  assert.equal(ui.el("room-name").textContent,"个人助理");
  assert.equal(ui.el("message-input").placeholder,"发送给 个人助理");
  assert.equal(ui.el("message-input-label").textContent,"发送给 个人助理");
  assert.equal(ui.el("direct-list").children.length,1);assert.equal(ui.el("room-list").children.length,1);
  assert.equal(ui.el("direct-list").children[0].children.length,1);
  assert.equal(ui.el("room-list").children[0].children.length,2);
  assert.equal(ui.el("activity-dialog").open,true);assert.equal(ui.el("close-activity").hidden,true);
  assert.equal(ui.el("direct-status"),undefined);assert.equal(ui.el("toggle-members").hidden,true);
  assert.equal(ui.el("direct-details").hidden,false);assert.equal(ui.el("direct-details").textContent,"收起助理详情");
  ui.el("room-menu").open=true;await ui.el("direct-details").emit("click");
  assert.equal(ui.el("activity-dialog").open,false);assert.equal(ui.el("room-menu").open,false);
  assert.equal(ui.el("direct-details").textContent,"助理详情");
  await ui.el("direct-details").emit("click");assert.equal(ui.el("activity-dialog").open,true);
  assert.equal(ui.el("agent-tab-platforms").getAttribute("aria-selected"),"true");
  ui.el("platforms-member-view").scrollTop=123;
  const reads=api.calls.filter(call=>call.path.startsWith("/v1/tools/bindings?")).length;
  await ui.refresh();await settle();
  assert.equal(ui.el("platforms-member-view").scrollTop,123);
  assert.equal(api.calls.filter(call=>call.path.startsWith("/v1/tools/bindings?")).length,reads);
  await ui.draft("DM draft");await ui.select("Team");assert.equal(ui.el("activity-dialog").open,false);
  assert.equal(ui.el("message-input").placeholder,"发送给 Team");
  assert.equal(ui.el("message-input-label").textContent,"发送给 Team");
  assert.equal(ui.el("toggle-members").hidden,false);assert.equal(ui.el("direct-details").hidden,true);
  assert.equal(ui.el("member-list-view").hidden,false);await ui.draft("Room draft");
  await ui.select("个人助理");assert.equal(ui.el("message-input").value,"DM draft");
  await ui.el("direct-notifications").emit("click");assert.equal(ui.el("member-list-view").hidden,false);
  assert.equal(ui.el("activity-dialog").hidden,true);await ui.el("close-conversation-notifications").emit("click");
  assert.equal(ui.el("activity-dialog").hidden,false);assert.equal(ui.el("member-list-view").hidden,true);
  ui.window.innerWidth=390;await ui.window.emit("resize");assert.equal(ui.el("member-rail").inert,true);
  await ui.el("direct-details").emit("click");assert.equal(ui.el("activity-dialog").open,true);
  await ui.el("close-activity").emit("click");await ui.refresh();assert.equal(ui.el("member-rail").inert,true);
  assert.equal(ui.document.activeElement,ui.el("room-menu-label"));
  const saved=new Map([["teamx.selected-room",JSON.stringify(room)]]),reloaded=await launch(api,saved);
  assert.equal(reloaded.el("room-name").textContent,"Team");assert.equal(reloaded.el("activity-dialog").open,false);
});

test("default delete protection follows identity after rename and clears old unaccepted deletion requests", async()=>{
  const fixed={...makeRoom("已改名的助理","fixed"),is_direct:true},manual={...makeRoom("个人助理","manual"),is_direct:true};
  const api=server([fixed,manual]);api.catalog.default_assistant={status:"completed",room_id:fixed.room_id,room_instance:fixed.room_instance};
  const saved=new Map([["teamx.room-management",JSON.stringify({kind:"delete",operator:"tester",payload:{room_id:fixed.room_id,room_instance:fixed.room_instance}})]]);
  const ui=await launch(api,saved);await settle();
  assert.equal(ui.el("direct-list").children[0].children.length,1);assert.equal(ui.el("direct-list").children[1].children.length,2);
  assert.equal(saved.has("teamx.room-management"),false);assert.equal(api.calls.some(call=>call.path==="/v1/rooms/delete"),false);
  assert.match(ui.el("management-notice").textContent,/不可删除/);
  await ui.delete("个人助理");await settle();assert.equal(api.catalog.rooms.length,1);assert.equal(api.catalog.rooms[0].room_id,fixed.room_id);
});

test("default initialization retry and explicit restoration preserve the current selection", async()=>{
  const api=server([makeRoom("Existing")]);api.catalog.default_assistant={status:"pending",error_code:"configuration_required",error:"模型未配置",next_step:"teamx start init"};
  api.hook=(path,data)=>{
    if(path==="/v1/rooms/default-assistant/retry"){
      assert.deepEqual(data,{service_epoch:"manager",caller:{kind:"human",id:"tester"}});
      api.catalog.default_assistant={status:"deleted",room_id:"old-assistant",room_instance:"old-instance"};return {};
    }
    if(path==="/v1/rooms/default-assistant/restore"){
      assert.deepEqual(data,{service_epoch:"manager",caller:{kind:"human",id:"tester"},room_id:"old-assistant",room_instance:"old-instance"});
      const room={...makeRoom("恢复的助理","restored"),is_direct:true};room.service=identity(room);room.runtime_status="ready";
      api.catalog.rooms.push(room);api.history.set(room.room_id,[]);
      api.catalog.default_assistant={status:"completed",room_id:room.room_id,room_instance:room.room_instance};return {};
    }
  };
  const ui=await launch(api);
  assert.match(ui.el("default-assistant-notice").textContent,/teamx start init.*重启/);
  assert.equal(ui.el("room-name").textContent,"Existing");
  await ui.el("retry-default-assistant").emit("click");await settle();
  assert.equal(ui.el("retry-default-assistant").hidden,false);assert.match(ui.el("retry-default-assistant").textContent,/恢复默认助理/);
  assert.match(ui.el("default-assistant-notice").textContent,/原消息、规则和订阅不会恢复/);
  assert.equal(ui.el("direct-list").children.length,0);
  await ui.refresh();assert.equal(api.calls.some(call=>call.path.endsWith("/restore")),false);
  await ui.el("retry-default-assistant").emit("click");await settle();
  assert.equal(ui.el("retry-default-assistant").hidden,true);assert.equal(ui.el("default-assistant-notice").hidden,true);
  assert.equal(ui.el("direct-list").children.length,1);assert.equal(ui.el("room-name").textContent,"Existing");
  assert.equal(api.calls.some(call=>call.path==="/v1/rooms/create"),false);
});

test("deleted default restoration stays explicit, reports uncertainty and selects only without valid history", async()=>{
  const api=server();api.catalog.default_assistant={status:"deleted",room_id:"old-assistant",room_instance:"old-instance"};
  let fail=true;
  api.hook=(path)=>{
    if(path==="/v1/rooms/default-assistant/restore"){
      if(fail)throw Error("offline");
      const room={...makeRoom("新个人助理","restored"),is_direct:true};room.service=identity(room);room.runtime_status="ready";
      api.catalog.rooms.push(room);api.history.set(room.room_id,[]);
      api.catalog.default_assistant={status:"completed",room_id:room.room_id,room_instance:room.room_instance};return {};
    }
  };
  const ui=await launch(api);assert.equal(api.calls.some(call=>call.path.endsWith("/restore")),false);
  await ui.el("retry-default-assistant").emit("click");await settle();assert.match(ui.el("default-assistant-notice").textContent,/恢复未确认/);
  await ui.refresh();assert.equal(api.calls.filter(call=>call.path.endsWith("/restore")).length,1);
  fail=false;await ui.el("retry-default-assistant").emit("click");await settle();assert.equal(ui.el("room-name").textContent,"新个人助理");
  assert.equal(ui.el("direct-list").children[0].children.length,1);
});

test("new direct creation uses a role without a team or seeded human task",async()=>{
  const api=server();api.catalog.teams=[];api.catalog.agent_roles=[{id:"assistant",name:"个人助理"}];
  const ui=await launch(api);await ui.el("new-room").emit("click");await ui.el("new-direct").emit("click");
  assert.equal(ui.el("create-team-fields").hidden,true);assert.equal(ui.el("team").required,false);
  assert.equal(ui.el("create-room").disabled,false);ui.el("new-room-name").value="独立助理";
  await ui.el("create-form").emit("submit");await settle();
  const request=api.calls.find(call=>call.path==="/v1/rooms/create").data;
  assert.equal(request.is_direct,true);assert.equal(request.agent_role_id,"assistant");assert.equal(request.team_id,undefined);
  assert.equal(ui.el("message-input").value,"");assert.equal(api.calls.some(call=>call.path==="/v1/send"),false);
});

test("a pending default selects once when ready, while late DM Inbox reads cannot populate a Room",async()=>{
  const dm={...makeRoom("助理","pending-dm"),is_direct:true},room=makeRoom("团队");
  const api=server();api.catalog.default_assistant={status:"pending"};const ui=await launch(api);
  api.catalog.rooms.push(dm);dm.service=identity(dm);dm.runtime_status="ready";api.history.set(dm.room_id,[]);
  api.catalog.default_assistant={status:"completed",room_id:dm.room_id,room_instance:dm.room_instance};
  const member={agent_id:"assistant",membership_id:"unique-dm-member",status:"paused",runner:{profile:"runtime"}};
  let resolveInbox;
  api.hook=(path)=>{
    if(path==="/v1/member/list")return {members:[member]};
    if(path.startsWith("/v1/tools/bindings?"))return {catalog:[],bindings:[]};
    if(path==="/v1/member/inbox/status")return new Promise(resolve=>{resolveInbox=resolve;});
  };
  await ui.refresh();await settle();assert.equal(ui.el("room-name").textContent,"助理");
  const opening=ui.el("agent-tab-inbox").emit("click");await settle();assert.ok(resolveInbox);
  room.service=identity(room);room.runtime_status="ready";api.catalog.rooms.push(room);api.history.set(room.room_id,[]);
  await ui.refresh();await ui.select("团队");
  resolveInbox({latest_seq:900,expired_through:0,limits:{entries:1000,bytes:100000}});await opening;await settle();
  assert.equal(ui.el("room-name").textContent,"团队");assert.equal(ui.el("activity-dialog").open,false);
  assert.equal(ui.el("inbox-entries").children.length,0);
  await ui.refresh();assert.equal(ui.el("room-name").textContent,"团队");
});
