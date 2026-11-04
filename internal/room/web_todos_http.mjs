import assert from "node:assert/strict";
import { launch, settle } from "./web_app_harness.mjs";
import { request } from "./web/client.mjs";
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
await ui.el("open-gitlab-source").emit("click"); await settle(); await ui.el("notification-tab-todos").emit("click");
await Promise.all([...inflight]); await settle();
assert.equal(ui.el("notification-sources").hidden, false);
assert.equal(ui.el("gitlab-events").hidden, false, "Events can be configured without a Workspace project binding");
const connections = ui.el("gitlab-todos-connection").children.filter(option => option.value);
assert.equal(connections.length, 2, ui.el("gitlab-todos-connection-notice").textContent + " / " + ui.el("gitlab-todos-error").textContent);
async function edit(id, value) { ui.el(id).value = value; await ui.el(id).emit("change"); }
async function toggle(id, checked) { ui.el(id).checked = checked; await ui.el(id).emit("change"); }
await edit("gitlab-todos-connection", connections[0].value);
assert.match(ui.el("gitlab-todos-recipient").textContent, /ID 101，已验证/);
assert.equal(ui.el("gitlab-todos-scope").value, "all");
await toggle("gitlab-todos-enabled", true); await ui.el("gitlab-todos-save").emit("click");
assert.equal(mutations[0].body.subscription_id, ""); assert.equal(mutations[0].body.subscription_version, 0);
assert.match(ui.el("gitlab-todos-feedback").textContent, /设置已保存/);
const initialID = ui.el("gitlab-todos-status").textContent.match(/当前订阅：([a-f0-9]{32})/)[1];
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
await message.querySelector(".author").emit("click"); assert.equal(ui.el("notification-tab-todos").attributes["aria-selected"], "true");
await ui.el("gitlab-todos-sync").emit("click"); assert.equal(mutations[1].body.subscription_id, initialID);
assert.equal(mutations[1].body.subscription_version, 1); assert.equal(mutations[1].body.config, undefined);
for (const reason of ["mentioned", "assigned", "review_requested", "other"]) await toggle(`gitlab-todos-reason-${reason}`, false);
await ui.el("gitlab-todos-save").emit("click"); assert.deepEqual(mutations.at(-1).body.config.filter.reasons, []);
await toggle("gitlab-todos-enabled", false); await ui.el("gitlab-todos-save").emit("click");
assert.equal(ui.el("gitlab-todos-state").textContent, "已关闭");
await edit("gitlab-todos-connection", connections[1].value); await toggle("gitlab-todos-enabled", true);
for (const reason of ["mentioned", "assigned", "review_requested", "other"]) await toggle(`gitlab-todos-reason-${reason}`, true);
assert.equal(ui.el("gitlab-todos-save").textContent, "为此账号重新开启");
await ui.el("gitlab-todos-save").emit("click"); await untilMessageCount(2);
assert.match(ui.el("gitlab-todos-recipient").textContent, /ID 202，已验证/);
const nextID = ui.el("gitlab-todos-status").textContent.match(/当前订阅：([a-f0-9]{32})/)[1]; assert.notEqual(nextID, initialID);
await toggle("gitlab-todos-enabled", false); await ui.el("gitlab-todos-save").emit("click");
await ui.refresh(); assert.equal(messages().length, 2);
assert.equal(mutations.every(call => call.body.provider === "gitlab" && call.body.channel === "todos"), true);
console.error("GT-03 app/handler: unbound room, personal identity, configure, sync, empty filter, account replacement, channel navigation and disable passed");
await Promise.all([...inflight]); await settle();
if (process.env.TEAMX_WEB_TRANSPORT === "stdio") process.stdin.destroy();
