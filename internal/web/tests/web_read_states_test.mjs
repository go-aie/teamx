import { test } from "node:test";
import assert from "node:assert/strict";
import { launch, settle, openMemberInbox } from "./web_app_harness.mjs";
import { fixture, target } from "./web_member_platform_fixture.mjs";

function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
const row = (ui, id) => ui.el("member-notification-platforms").children.find(row => row.dataset.platformId === id);
const tool = (ui, id) => row(ui, id).children[1].children[2].children[0];
const quiet = (ui, id) => assert.equal(ui.el(id).hidden, true, `${id} stays quiet`);

test("read indicators call browser timer functions without the environment as receiver", async () => {
  const timers=new Map();let next=0;
  const ui=await launch(fixture(),new Map(),{
    setTimeout:function(fn,delay){assert.equal(this,undefined);const id=++next;timers.set(id,{fn,delay});return id;},
    clearTimeout:function(id){assert.equal(this,undefined);timers.delete(id);},
  });
  await openMemberInbox(ui,"member-a");
  quiet(ui,"sync-error");quiet(ui,"member-notification-list-error");
  await ui.openNotificationSource(row(ui,"gl").children[0],"gitlab","gl",target());
  quiet(ui,"notification-notice");
});

test("quick member and notification reads finish before any loading notice appears", async () => {
  const api=fixture(),ui=await launch(api);
  await openMemberInbox(ui,"member-a");
  quiet(ui,"member-notification-list-error");
  await ui.openNotificationSource(row(ui,"gl").children[0],"gitlab","gl",target());
  quiet(ui,"notification-notice");
  await ui.clock.advance(1000);
  quiet(ui,"member-notification-list-error"); quiet(ui,"notification-list-error"); quiet(ui,"notification-notice");
});

test("member settings wait quietly, show delayed neutral progress and retain confirmed tools on refresh", async () => {
  const api = fixture(), ui = await launch(api), read = deferred();
  api.tools.bindings.push({agent_id:"a",membership_id:"member-a",tool:"gitlab",platform_id:"gl"});
  api.hook = path => path.startsWith("/v1/tools/bindings?") ? read.promise : undefined;
  await openMemberInbox(ui, "member-a");
  quiet(ui, "member-notification-list-error");
  quiet(ui, "member-notification-empty");
  assert.equal(ui.el("member-platform-count").hidden, true);
  assert.equal(ui.el("member-notification-platforms").getAttribute("aria-busy"), "true");
  await ui.clock.advance(349);
  quiet(ui, "member-notification-list-error");
  await ui.clock.advance(1);
  assert.match(ui.el("member-notification-list-error").textContent, /正在读取/);
  assert.equal(ui.el("member-notification-list-error").classList.contains("form-error"), false);
  quiet(ui, "member-notification-list-reload");
  read.resolve(structuredClone(api.tools)); await settle();
  quiet(ui, "member-notification-list-error");
  assert.equal(tool(ui, "gl").checked, true);
  assert.equal(ui.el("member-platform-count").hidden, false);
  assert.equal(ui.el("member-notification-platforms").getAttribute("aria-busy"), "false");

  const again = deferred();
  api.hook = path => path.startsWith("/v1/tools/bindings?") ? again.promise : undefined;
  const refreshing = ui.refresh(); await settle();
  assert.equal(tool(ui, "gl").checked, true);
  quiet(ui, "member-notification-list-error");
  again.resolve(structuredClone(api.tools)); await refreshing; await settle();
  await ui.clock.advance(1000);
  quiet(ui, "member-notification-list-error");
});

for (const path of ["/v1/platforms?", "/v1/platform/notifications?", "/v1/tools/bindings?"]) {
  test(`confirmed read failure at ${path} keeps recovery until retry succeeds`, async () => {
    const api = fixture(), ui = await launch(api), read = deferred();
    api.hook = value => value.startsWith(path) ? read.promise : undefined;
    await openMemberInbox(ui, "member-a");
    quiet(ui, "member-notification-list-error");
    read.reject(Error("offline")); await settle();
    assert.match(ui.el("member-notification-list-error").textContent, /暂不可读/);
    assert.equal(ui.el("member-notification-list-error").classList.contains("form-error"), true);
    assert.equal(ui.el("member-notification-list-reload").hidden, false);
    const retry = deferred(), request = api.request.bind(api);
    api.hook = value => value.startsWith(path) ? retry.promise : undefined;
    const rereading = ui.el("member-notification-list-reload").emit("click"); await settle();
    assert.equal(ui.el("member-notification-list-reload").disabled, true);
    assert.match(ui.el("member-notification-list-error").textContent, /暂不可读/);
    api.hook = null;
    retry.resolve(await request(api.calls.findLast(call => call.path.startsWith(path)).path));
    await rereading; await settle();
    quiet(ui, "member-notification-list-error");
    quiet(ui, "member-notification-list-reload");
  });
}

test("notification detail waits quietly and exposes retry only after a failed read", async () => {
  const api = fixture(), ui = await launch(api), read = deferred();
  await openMemberInbox(ui, "member-a");
  api.hook = path => path.startsWith("/v1/platform/gitlab/notifications?") ? read.promise : undefined;
  const opening = ui.openNotificationSource(row(ui,"gl").children[0], "gitlab", "gl", target());
  await settle();
  quiet(ui, "notification-notice"); quiet(ui, "notification-reload");
  assert.equal(ui.el("notification-panel-gitlab").hidden, true);
  await ui.clock.advance(350);
  assert.match(ui.el("notification-notice").textContent, /正在读取/);
  quiet(ui, "notification-reload");
  read.reject(Error("offline")); await opening; await settle();
  assert.match(ui.el("notification-notice").textContent, /暂不可读/);
  assert.equal(ui.el("notification-reload").hidden, false);
  assert.equal(ui.el("notification-panel-gitlab").hidden, true);
  api.hook = null;
  await ui.el("notification-reload").emit("click"); await settle();
  quiet(ui, "notification-notice"); quiet(ui, "notification-reload");
  assert.equal(ui.el("notification-panel-gitlab").hidden, false);
});

test("a slow notification detail cannot publish loading or failures over a different member", async () => {
  const api = fixture(), ui = await launch(api), read = deferred();
  await openMemberInbox(ui, "member-a");
  api.hook = path => path.startsWith("/v1/platform/gitlab/notifications?") ? read.promise : undefined;
  const opening = ui.openNotificationSource(row(ui,"gl").children[0], "gitlab", "gl", target());
  await settle();
  api.hook = null;
  await ui.el("close-notification").emit("click");
  await openMemberInbox(ui, "member-b");
  await ui.openNotificationSource(row(ui,"gl").children[0], "gitlab", "gl", target("b"));
  await ui.clock.advance(350);
  read.reject(Error("old request failed")); await opening; await settle();
  assert.match(ui.el("notification-title").textContent, /^b ·/);
  assert.equal(ui.el("gitlab-interval").value, 45);
  quiet(ui, "notification-notice"); quiet(ui, "notification-reload");
});

test("source summaries and empty Inbox wait for fresh reads after a service epoch change", async () => {
  const api = fixture({empty:true});
  const emptyInbox = path => path === "/v1/member/inbox" ? {snapshot_seq:0,entries:[],has_more:false,status:{latest_seq:0,limits:{bytes:1000},bytes:0}} : undefined;
  api.hook = emptyInbox;
  const ui = await launch(api);
  await openMemberInbox(ui, "member-a");
  const platforms = deferred(), sources = deferred();
  api.hook = path => path.startsWith("/v1/platforms?") ? platforms.promise : path.startsWith("/v1/platform/notifications?") ? sources.promise : emptyInbox(path);
  api.epoch = "next-manager";
  const refreshing = ui.refresh(); await settle();
  quiet(ui, "notification-list-error"); quiet(ui, "notification-empty");
  quiet(ui, "member-notification-list-error"); quiet(ui, "member-notification-empty");
  assert.equal(ui.el("inbox-empty-hint").textContent, "");
  assert.equal(ui.el("inbox-backlog").textContent, "");
  quiet(ui, "inbox-add-platform");
  await ui.clock.advance(350);
  assert.equal(ui.el("notification-platforms").getAttribute("aria-busy"), "true");
  assert.equal(ui.el("notification-list-error").classList.contains("form-error"), false);
  assert.equal(ui.el("inbox-empty-hint").textContent, "");
  quiet(ui, "notification-list-reload");
  platforms.resolve({platforms:structuredClone(api.platforms)});
  sources.resolve({subscriptions:[],gitlab_platforms:[]});
  await refreshing; await settle();
  quiet(ui, "notification-list-error"); quiet(ui, "member-notification-list-error");
  assert.match(ui.el("inbox-empty-hint").textContent, /添加平台/);
  assert.equal(ui.el("inbox-add-platform").hidden, false);
});

test("refresh retains confirmed empty source lists while waiting for new data", async () => {
  const api=fixture({empty:true}),ui=await launch(api),read=deferred();
  await openMemberInbox(ui,"member-a");
  assert.equal(ui.el("notification-empty").hidden,false);
  assert.equal(ui.el("member-notification-empty").hidden,false);
  api.hook=path=>path.startsWith("/v1/platform/notifications?") ? read.promise : undefined;
  const refreshing=ui.refresh();await settle();
  assert.equal(ui.el("notification-empty").hidden,false);
  assert.equal(ui.el("member-notification-empty").hidden,false);
  quiet(ui,"member-notification-list-error");
  read.resolve({subscriptions:[],gitlab_platforms:[]});await refreshing;await settle();
});

test("a platform read from the prior Room runtime cannot settle a new loading state", async () => {
  const api=fixture(),ui=await launch(api),old=deferred(),fresh=deferred();
  await openMemberInbox(ui,"member-a");
  api.hook=path=>path.startsWith("/v1/platforms?") ? old.promise : undefined;
  const opening=ui.openNotificationSource(row(ui,"gl").children[0],"gitlab","gl",target());await settle();
  api.runtimeEpoch="next-runtime";
  api.hook=path=>path.startsWith("/v1/platforms?") ? fresh.promise : undefined;
  const refreshing=ui.refresh();await settle();
  old.resolve({platforms:[]});await opening;await settle();
  quiet(ui,"notification-empty");
  assert.equal(ui.el("notification-platforms").getAttribute("aria-busy"),"true");
  await ui.clock.advance(350);
  assert.equal(ui.el("notification-platforms").getAttribute("aria-busy"),"true");
  fresh.resolve({platforms:structuredClone(api.platforms)});await refreshing;await settle();
  assert.equal(ui.el("notification-platforms").children.length,1);
  quiet(ui,"notification-list-error");
});
