import assert from "node:assert/strict";
import { launch, settle } from "./web_app_harness.mjs";
import { request } from "./web/client.mjs";
const endpoint = process.env.TEAMX_WEB_ENDPOINT;
let call = (path, body) => request(endpoint + path, body);
if (process.env.TEAMX_WEB_TRANSPORT === "stdio") {
  const { createInterface } = await import("node:readline");
  const pending = new Map(); let seq = 0;
  createInterface({ input: process.stdin }).on("line", line => {
    const response = JSON.parse(line), receipt = pending.get(response.id);
    pending.delete(response.id);
    if (response.status === 200) receipt.resolve(response.body);
    else receipt.reject(Object.assign(new Error(response.body.error), { code: response.body.code }));
  });
  call = (path, body) => new Promise((resolve, reject) => {
    const id = ++seq; pending.set(id, { resolve, reject });
    process.stdout.write(JSON.stringify({ id, path, body }) + "\n");
  });
}
const inflight = new Set();
const ui = await launch({ request: (path, body) => {
  const task = call(path, body); inflight.add(task);
  return task.finally(() => inflight.delete(task));
} });
await ui.el("open-settings").emit("click");
await ui.el("connection-new").emit("click");
ui.el("connection-name").value = "Shared GitLab";
ui.el("connection-url").value = "https://gitlab.invalid";
ui.el("connection-pat").value = "handler-initial-token";
await ui.el("connection-form").emit("submit");
assert.equal(ui.el("connections-feedback").textContent, "连接已保存。");
await ui.el("close-settings").emit("click");
await ui.create("Shared Workspace");
async function ready() {
  for (let i = 0; i < 500; i++) {
    await ui.refresh();
    let catalog;
    try { catalog = await call("/v1/rooms"); } catch (error) { if (error.code !== "busy") throw error; }
    if (catalog && !catalog.busy && catalog.rooms[0]?.service && !catalog.rooms[0].busy) return catalog;
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  throw Error("manager did not settle");
}
await ready();
await ui.el("open-workspace").emit("click");
ui.el("workspace-repository").checked = true; await ui.el("workspace-repository").emit("change");
ui.el("workspace-repo-url").value = "https://gitlab.invalid/group/project.git";
ui.el("workspace-gitlab").checked = true; await ui.el("workspace-gitlab").emit("change");
// Activation can briefly reject a connection-list read during an index commit.
// Explicitly refresh the read-only view; never replay create/save/apply here.
let connection;
for (let attempt = 0; attempt < 50; attempt++) {
  connection = ui.el("workspace-connection").options.find(option => option.value);
  if (connection) break;
  await ui.el("workspace-refresh").emit("click"); await settle();
  await new Promise(resolve => setTimeout(resolve, 20));
}
assert.ok(connection, "shared connection list did not become readable");
ui.el("workspace-connection").value = connection.value; await ui.el("workspace-connection").emit("change");
await ui.el("workspace-form").emit("submit");
const before = await ready();
const params = new URLSearchParams({ ...before.rooms[0].service, service_epoch: before.service_epoch, caller_id: before.operator });
const workspace = await call(`/v1/workspace?${params}`);
assert.equal(workspace.operation.status, "completed");
assert.equal(workspace.workspace.binding.connection_id, connection.value);
assert.equal(workspace.workspace.binding.project_id, 7);
await ui.el("open-settings").emit("click");
await ui.el("connections-list").children[0].emit("click");
assert.match(ui.el("connection-references").textContent, /Shared Workspace/);
assert.equal(ui.el("connection-delete").disabled, true);
ui.el("connection-pat").value = "handler-rotated-token";
await ui.el("connection-form").emit("submit");
assert.equal(ui.el("connections-feedback").textContent, "连接已保存。");
const after = await call("/v1/rooms");
assert.deepEqual(before.rooms[0].service, after.rooms[0].service);
const unchanged = await call(`/v1/workspace?${params}`);
assert.equal(unchanged.workspace_version, workspace.workspace_version);
assert.equal(unchanged.operation.operation_id, workspace.operation.operation_id);
assert.equal(JSON.stringify([...ui.saved]).includes("handler-initial-token"), false);
assert.equal(JSON.stringify([...ui.saved]).includes("handler-rotated-token"), false);
await ui.el("connections-list").children[0].emit("click");
await ui.el("connection-verify").emit("click");
assert.equal(ui.el("connections-feedback").textContent, "连接验证成功。");
await ui.el("close-settings").emit("click");
await ui.refresh();
await ui.el("open-gitlab-source").emit("click"); await settle(); await Promise.all([...inflight]); await settle();
await ui.el("notification-manage-binding").emit("click");
ui.el("gitlab-events-enabled").checked = true;
await ui.el("gitlab-events-enabled").emit("change");
assert.equal(ui.el("gitlab-events-save").disabled, false, ui.el("gitlab-events-account").textContent);
await ui.el("gitlab-events-save").emit("click");
let notifications = await call(`/v1/platform/notifications?${params}`);
assert.equal(notifications.subscriptions.length, 2);
assert.equal(notifications.subscriptions.find(s => s.channel === "todos").subscription_version, 0);
const subscription = notifications.subscriptions.find(s => s.provider === "gitlab" && s.channel === "events");
assert.equal(subscription.provider, "gitlab");
assert.equal(subscription.channel, "events");
assert.match(subscription.subscription_id, /^[0-9a-f]{32}$/);
assert.equal(subscription.subscription_version, 1);
ui.el("gitlab-events-interval").value = "7";
await ui.el("gitlab-events-interval").emit("input");
await ui.el("gitlab-events-save").emit("click");
await ui.el("gitlab-events-sync").emit("click");
notifications = await call(`/v1/platform/notifications?${params}`);
assert.equal(notifications.subscriptions.find(s => s.provider === "gitlab" && s.channel === "events").subscription_id, subscription.subscription_id);
assert.equal(notifications.subscriptions.find(s => s.provider === "gitlab" && s.channel === "events").subscription_version, 2);
assert.equal(notifications.subscriptions.find(s => s.provider === "gitlab" && s.channel === "events").config.interval_seconds, 7);
ui.el("gitlab-events-enabled").checked = false;
await ui.el("gitlab-events-enabled").emit("change");
await ui.el("gitlab-events-save").emit("click");
notifications = await call(`/v1/platform/notifications?${params}`);
assert.equal(notifications.subscriptions.find(s => s.provider === "gitlab" && s.channel === "events").config.enabled, false);
assert.equal(notifications.subscriptions.find(s => s.provider === "gitlab" && s.channel === "events").subscription_version, 3);
assert.equal((await call(`/v1/workspace?${params}`)).workspace_version, workspace.workspace_version);
console.error("shared connection app/handler: create, bind, references, rotate without Session rebuild passed");
if (process.env.TEAMX_WEB_TRANSPORT === "stdio") process.stdin.destroy();
