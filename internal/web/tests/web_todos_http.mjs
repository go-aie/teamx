import assert from "node:assert/strict";
import { launch, settle, openMemberInbox, openNotification } from "./web_app_harness.mjs";
import { request } from "../assets/client.mjs";
const call = (path, body) => request(process.env.TEAMX_WEB_ENDPOINT + path, body);
const mutations = [];
const inflight = new Set();
const ui = await launch({ request: (path, body) => {
  if (/notifications\/(configure|sync)$/.test(path)) mutations.push({ path, body: structuredClone(body) });
  assert.equal(path.includes("workspace/apply"), false, "personal channel must not apply Workspace");
  const task = call(path, body); inflight.add(task);
  return task.finally(() => inflight.delete(task));
} });
for (let attempt=0; attempt<100 && ui.el("connection").textContent!=="服务已连接"; attempt++) {
  await Promise.all([...inflight]);
  await new Promise(resolve => setTimeout(resolve,20));
}
assert.equal(ui.el("connection").textContent,"服务已连接");
await openMemberInbox(ui);
await ui.el("agent-tab-platforms").emit("click");
await ui.refresh();
await Promise.all([...inflight]); await settle();
assert.equal(ui.el("member-notification-add-options").children.length,2,JSON.stringify({
  member:ui.selectedAgent(), status:ui.el("member-notification-list-error").textContent, sync:ui.el("sync-error").textContent
}));
const platforms = ui.el("member-notification-add-options").children.map(button=>({value:button.dataset.platformId}));
await openNotification(ui,"gitlab",platforms[0].value,false); ui.el("gitlab-todos-settings").open = true;
assert.equal(ui.el("member-notification-sources").hidden, false);
assert.equal(ui.el("gitlab-events").hidden, false, "Events can be configured without a Workspace project binding");
assert.equal(platforms.length, 2, ui.el("gitlab-notification-account").textContent + " / " + ui.el("gitlab-todos-error").textContent);
async function edit(id, value) { ui.el(id).value = value; await ui.el(id).emit("change"); }
async function toggle(id, checked) { ui.el(id).checked = checked; await ui.el(id).emit("change"); }
assert.match(ui.el("gitlab-notification-account").textContent, /ID 101，已验证/);
assert.equal(ui.el("gitlab-todos-scope"), undefined);
assert.equal(String(ui.el("gitlab-interval").value), "10");
await toggle("gitlab-todos-enabled", true); await ui.el("gitlab-save").emit("click");
assert.equal(mutations[0].body.version, 0); assert.equal(mutations[0].body.config.todos.enabled, true);
assert.match(ui.el("gitlab-feedback").textContent, /设置已保存/);
// Saving category settings preserves the independent reception switch. Enable
// the newly configured member source through its actual platform-list control.
async function setMemberSourceEnabled(id, enabled) {
  await ui.el("close-notification").emit("click");
  const row=ui.el("member-notification-platforms").children.find(row=>row.dataset.platformId===id);
  assert.ok(row,"saved member notification source is listed");
  const input=row.children[1].children[1];input.checked=enabled;await input.emit("change");
  await openNotification(ui,"gitlab",id,false);
}
await setMemberSourceEnabled(platforms[0].value, true);
const savedTodo = async () => {
  const {service_epoch,room_id,room_instance,caller} = mutations.at(-1).body;
  const view = await call(`/v1/platform/notifications?${new URLSearchParams({service_epoch,room_id,room_instance,caller_id:caller.id})}`);
  const found = view.subscriptions.find(source => source.channel === "todos" && source.platform_id === mutations.at(-1).body.platform_id);
  assert.ok(found, JSON.stringify({subscriptions:view.subscriptions,requested:mutations.at(-1).body}));
  return found;
};
const initialID = (await savedTodo()).subscription_id;
assert.match(initialID, /^[a-f0-9]{32}$/);
const messages = () => ui.el("messages").children.filter(child => /^message-\d+$/.test(child.id));
async function untilInboxCount(count) {
  const {room_id,room_instance,caller} = mutations.at(-1).body;
  const {agent_id,membership_id} = ui.selectedAgent();
  const catalog = await call("/v1/rooms");
  const room = catalog.rooms.find(item=>item.room_id===room_id);
  for (let i=0;i<100;i++) {
    const view = await call("/v1/member/inbox",{...room.service,room_id,room_instance,agent_id,membership_id,caller,limit:25,after:0});
    if(view.snapshot_seq===count){assert.ok(view.entries.every(e=>e.item.channel==="todos"));return;}
    await new Promise(resolve=>setTimeout(resolve,20));
  }
  assert.fail(`expected ${count} Inbox notifications`);
}
await untilInboxCount(1);
await ui.el("close-notification").emit("click");
await openMemberInbox(ui);
await ui.el("inbox-reload").emit("click"); await settle();
const entry = ui.el("inbox-entries").children[0];
assert.ok(entry,"Inbox displays the received Todo");
await entry.children.at(-1).children.find(child=>child.tag==="button").emit("click");
assert.equal(ui.el("notification-panel-gitlab").hidden,false);
assert.equal(ui.el("gitlab-todos-settings").open,true);
await ui.el("gitlab-sync").emit("click"); assert.equal(mutations.at(-1).body.version, 2);
assert.equal(mutations.at(-1).body.config, undefined);
for (const reason of ["mentioned", "assigned", "review_requested", "other"]) await toggle(`gitlab-todos-reason-${reason}`, false);
await ui.el("gitlab-save").emit("click"); assert.deepEqual(mutations.at(-1).body.config.todos.filter.reasons, []);
await setMemberSourceEnabled(platforms[0].value, false);
await toggle("gitlab-todos-enabled", false); await ui.el("gitlab-save").emit("click");
assert.match(ui.el("gitlab-feedback").textContent, /设置已保存/);
assert.equal(ui.el("gitlab-todos-enabled").checked, false);
await ui.el("close-notification").emit("click"); await openNotification(ui,"gitlab",platforms[1].value,false); await toggle("gitlab-todos-enabled", true);
for (const reason of ["mentioned", "assigned", "review_requested", "other"]) await toggle(`gitlab-todos-reason-${reason}`, true);
assert.equal(ui.el("gitlab-save").disabled, false);
await ui.el("gitlab-save").emit("click"); await setMemberSourceEnabled(platforms[1].value, true); await untilInboxCount(2);
assert.match(ui.el("gitlab-notification-account").textContent, /ID 202，已验证/);
const nextID = (await savedTodo()).subscription_id; assert.notEqual(nextID, initialID);
await setMemberSourceEnabled(platforms[1].value, false);
await toggle("gitlab-todos-enabled", false); await ui.el("gitlab-save").emit("click");
assert.match(ui.el("gitlab-feedback").textContent, /设置已保存/);
await ui.refresh(); assert.equal(messages().length, 0);
assert.equal(mutations.every(call => call.body.channel === undefined && call.body.subscription_id === undefined), true);
console.error("GT-03 app/handler: unbound room, personal identity, configure, sync, empty filter, independent platforms, Inbox source navigation and disable passed");
await Promise.all([...inflight]); await settle();
