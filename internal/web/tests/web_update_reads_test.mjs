import { test } from "node:test";
import assert from "node:assert/strict";
import { launch, settle, openMemberInbox } from "./web_app_harness.mjs";
import { fixture } from "./web_member_platform_fixture.mjs";
import { createUpdateScheduler } from "../assets/updates.mjs";

const deferred = () => { let resolve; const promise = new Promise(yes => { resolve = yes; }); return { promise, resolve }; };
const message = (seq, text) => ({ seq, text, author: { kind: "agent", id: "a" }, created_at: "2026-11-16T00:00:00Z" });
const page = (seq, text) => ({ messages: [message(seq, text)], snapshot_seq: seq, cleared_through: 0, has_more: false });
const count = (api, path) => api.calls.filter(c => c.path.split("?")[0] === path).length;

test("explicit refresh applies chat before slow platform configuration and preserves its single-flight promise", async () => {
  const intervals = [];
  const api = fixture(), ui = await launch(api, new Map(), {
    setInterval: (_, delay) => intervals.push(delay),
  }), platforms = deferred();
  assert.deepEqual(intervals, [], "periodic display triggers are removed");
  await ui.draft("保留草稿");
  api.members[0].status = "running";
  api.members[0].execution = { active: true };
  api.hook = path => path.startsWith("/v1/platforms?") ? platforms.promise
    : path === "/v1/read" ? page(1, "配置仍在读取时收到的消息") : undefined;
  api.calls.length = 0;
  const refresh = ui.refresh(); assert.equal(ui.refresh(), refresh);
  await settle();
  assert.ok(ui.el("messages").children.some(node => node.id === "message-1"));
  assert.equal(ui.el("members").children[0].querySelector(".member-summary").dataset.state, "running");
  assert.equal(ui.el("message-input").value, "保留草稿");
  assert.equal(count(api, "/v1/platforms"), 1); assert.equal(count(api, "/v1/read"), 1);
  platforms.resolve({ platforms: structuredClone(api.platforms) }); await refresh;
  assert.equal(count(api, "/v1/send"), 0);
});

test("chat, workspace and global observation entries do not pull unrelated configuration or details", async () => {
  const api = fixture(), ui = await launch(api);
  api.calls.length = 0; await ui.reads.chat();
  assert.deepEqual(api.calls.map(c => c.path).sort(), ["/v1/member/list", "/v1/read"]);
  api.calls.length = 0; await ui.reads.workspace();
  assert.deepEqual(api.calls.map(c => c.path.split("?")[0]), ["/v1/workspace"]);
  api.calls.length = 0; await ui.reads.settings(); await ui.reads.catalog();
  assert.deepEqual(api.calls.map(c => c.path), ["/v1/ui", "/v1/rooms"]);
});

test("an unavailable Workspace observation keeps sending gated until it is verified again", async () => {
  const api = fixture(), ui = await launch(api);
  await ui.draft("等待核对再发送");
  api.hook = path => path.startsWith("/v1/workspace?") ? Promise.reject(Error("offline")) : undefined;
  await ui.refresh();
  assert.equal(ui.el("send").disabled, true);
  assert.equal(ui.el("message-input").value, "等待核对再发送");
  api.hook = null; await ui.refresh();
  assert.equal(ui.el("send").disabled, false);
  assert.equal(count(api, "/v1/send"), 0);
});

test("messages do not wait for a slow member or visible Activity read", async () => {
  const api = fixture(), ui = await launch(api), members = deferred(), activity = deferred();
  await openMemberInbox(ui, "member-a");
  await ui.el("agent-tab-activity").emit("click"); await settle();
  api.hook = path => path === "/v1/member/list" ? members.promise : path === "/v1/member/activity" ? activity.promise
    : path === "/v1/read" ? page(2, "独立补读") : undefined;
  const inspecting = ui.reads.activity(), chat = ui.reads.chat(); await settle();
  assert.ok(ui.el("messages").children.some(node => node.id === "message-2"));
  members.resolve({ members: structuredClone(api.members) }); activity.resolve({ member: api.members[0] });
  await Promise.all([chat, inspecting]);
});

test("platform settings and Workspace use one shared HTTP result", async () => {
  const api = fixture(), ui = await launch(api), response = deferred();
  api.hook = path => path.startsWith("/v1/platforms?") ? response.promise : undefined;
  api.calls.length = 0;
  const workspace = ui.reads.platforms(), settings = ui.el("open-settings").emit("click");
  await settle(); assert.equal(count(api, "/v1/platforms"), 1);
  response.resolve({ platforms: structuredClone(api.platforms) }); await Promise.all([workspace, settings]);
  assert.equal(ui.el("platforms-list").children.length, api.platforms.length);
  assert.equal(count(api, "/v1/platform/notifications"), 0);
});

test("scheduler injection into the real message entry limits HTTP rounds and rejects an old stream response", async () => {
  const api = fixture(), ui = await launch(api), updates = createUpdateScheduler(ui.readEnvironment);
  const r = updates.resource("messages", { read: ui.reads.messages });
  api.calls.length = 0;
  // An empty snapshot keeps rendering bounded; this measures actual HTTP calls
  // through the application, separately from the scheduler's unit round counts.
  for (let i = 0; i <= 1000; i++) {
    updates.invalidate("messages"); await settle(); await ui.clock.advance(10);
  }
  await ui.clock.advance(500);
  assert.equal(count(api, "/v1/read"), 22);
  assert.equal(count(api, "/v1/member/list"), 0);
  assert.equal(count(api, "/v1/platforms"), 0);
  const old = deferred(); api.hook = path => path === "/v1/read" ? old.promise : undefined;
  const pending = r.refresh(); await settle(); const oldToken = updates.generation;
  updates.reset(); old.resolve(page(99, "旧连接结果")); await pending;
  assert.equal(ui.el("messages").children.some(node => node.id === "message-99"), false);
  api.hook = path => path === "/v1/read" ? page(1, "新连接结果") : undefined;
  await r.refresh(); assert.ok(ui.el("messages").children.some(node => node.id === "message-1"));
  const before = count(api, "/v1/read"); updates.invalidate("messages", oldToken); await ui.clock.advance(10000);
  assert.equal(count(api, "/v1/read"), before); updates.dispose();
});

test("a rendered feature read failure remains dirty for scheduler retry; other resources keep updating", async () => {
  const api = fixture(), ui = await launch(api), updates = createUpdateScheduler(ui.readEnvironment);
  let failing = true;
  api.hook = path => path.startsWith("/v1/platform/notifications?") && failing ? Promise.reject(Error("offline")) : undefined;
  updates.resource("notifications", { read: ui.reads.notifications });
  updates.resource("messages", { read: ui.reads.messages });
  api.calls.length = 0;
  updates.invalidate("notifications"); updates.invalidate("messages"); await settle();
  assert.equal(count(api, "/v1/platform/notifications"), 1); assert.equal(count(api, "/v1/read"), 1);
  failing = false; await ui.clock.advance(1999); assert.equal(count(api, "/v1/platform/notifications"), 1);
  await ui.clock.advance(1); assert.equal(count(api, "/v1/platform/notifications"), 2);
  await ui.clock.advance(10000); assert.equal(count(api, "/v1/platform/notifications"), 2); updates.dispose();
});

test("stream/write fences also protect a visible Activity result inside its feature", async () => {
  const api = fixture(), ui = await launch(api), response = deferred();
  await openMemberInbox(ui, "member-a"); await ui.el("agent-tab-activity").emit("click"); await settle();
  const before = ui.el("activity-status").textContent;
  api.hook = path => path === "/v1/member/activity" ? response.promise : undefined;
  let current = true;
  const reading = ui.reads.activity({ current: () => current }); await settle(); current = false;
  response.resolve({ member: { ...api.members[0], status: "failed" } }); await reading;
  assert.equal(ui.el("activity-status").textContent, before);
});
