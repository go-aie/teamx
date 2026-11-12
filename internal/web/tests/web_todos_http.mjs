import assert from "node:assert/strict";
import { launch, settle, openNotification } from "./web_app_harness.mjs";
import { request } from "../assets/client.mjs";
let call = (path, body) => request(process.env.TEAMX_WEB_ENDPOINT + path, body);
if (process.env.TEAMX_WEB_TRANSPORT === "stdio") {
  const { createInterface } = await import("node:readline");
  const pending = new Map(); let seq = 0;
  createInterface({ input: process.stdin }).on("line", line => {
    const response = JSON.parse(line), receipt = pending.get(response.id); pending.delete(response.id);
    if (response.status === 200) receipt.resolve(response.body);
    else receipt.reject(Object.assign(new Error(response.body.error), { code: response.body.code }));
  });
  call = (path, body) => new Promise((resolve, reject) => {
    const id = ++seq; pending.set(id, { resolve, reject }); process.stdout.write(JSON.stringify({ id, path, body }) + "\n");
  });
}
const mutations = [];
const inflight = new Set();
const ui = await launch({ request: (path, body) => {
  if (/notifications\/(configure|sync)$/.test(path)) mutations.push({ path, body: structuredClone(body) });
  assert.equal(path.includes("workspace/apply"), false, "personal channel must not apply Workspace");
  const task = call(path, body); inflight.add(task);
  return task.finally(() => inflight.delete(task));
} });
await Promise.all([...inflight]); await settle();
const platforms = ui.el("notification-add-options").children.map(button=>({value:button.dataset.platformId}));
await openNotification(ui,"gitlab",platforms[0].value,false); ui.el("gitlab-todos-settings").open = true;
assert.equal(ui.el("notification-sources").hidden, false);
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
// the newly configured Room source through its actual platform-list control.
async function enableRoomSource(id) {
  await ui.el("close-notification").emit("click");
  const row=ui.el("notification-platforms").children.find(row=>row.dataset.platformId===id);
  assert.ok(row,"saved Room notification source is listed");
  const input=row.children[1];input.checked=true;await input.emit("change");
  await openNotification(ui,"gitlab",id,false);
}
await enableRoomSource(platforms[0].value);
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
async function untilMessageCount(count) {
  for (let i = 0; i < 100; i++) {
    await ui.refresh();
    if (messages().length === count) return;
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  assert.fail(`expected ${count} integration messages; got ${messages().length}; ${ui.el("gitlab-todos-status").textContent}; ${ui.el("gitlab-todos-error").textContent}; ${ui.el("sync-error").textContent}`);
}
await untilMessageCount(1);
const message = messages()[0];
assert.equal(message.querySelector(".author").textContent, "GitLab · 我的待办");
await message.querySelector(".author").emit("click"); assert.equal(ui.el("notification-panel-gitlab").hidden, false); assert.match(ui.el("notification-title").textContent,/房间/); assert.equal(ui.el("gitlab-todos-settings").open, true);
await ui.el("gitlab-sync").emit("click"); assert.equal(mutations.at(-1).body.version, 2);
assert.equal(mutations.at(-1).body.config, undefined);
for (const reason of ["mentioned", "assigned", "review_requested", "other"]) await toggle(`gitlab-todos-reason-${reason}`, false);
await ui.el("gitlab-save").emit("click"); assert.deepEqual(mutations.at(-1).body.config.todos.filter.reasons, []);
await toggle("gitlab-todos-enabled", false); await ui.el("gitlab-save").emit("click");
assert.equal(ui.el("gitlab-todos-enabled").checked, false);
await ui.el("close-notification").emit("click"); await openNotification(ui,"gitlab",platforms[1].value,false); await toggle("gitlab-todos-enabled", true);
for (const reason of ["mentioned", "assigned", "review_requested", "other"]) await toggle(`gitlab-todos-reason-${reason}`, true);
assert.equal(ui.el("gitlab-save").disabled, false);
await ui.el("gitlab-save").emit("click"); await enableRoomSource(platforms[1].value); await untilMessageCount(2);
assert.match(ui.el("gitlab-notification-account").textContent, /ID 202，已验证/);
const nextID = (await savedTodo()).subscription_id; assert.notEqual(nextID, initialID);
await toggle("gitlab-todos-enabled", false); await ui.el("gitlab-save").emit("click");
await ui.refresh(); assert.equal(messages().length, 2);
assert.equal(mutations.every(call => call.body.channel === undefined && call.body.subscription_id === undefined), true);
console.error("GT-03 app/handler: unbound room, personal identity, configure, sync, empty filter, independent platforms, channel navigation and disable passed");
await Promise.all([...inflight]); await settle();
if (process.env.TEAMX_WEB_TRANSPORT === "stdio") process.stdin.destroy();
