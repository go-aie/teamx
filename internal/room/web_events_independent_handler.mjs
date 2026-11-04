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
  assert.equal(path.includes("workspace/apply"), false);
  const task = call(path, body); inflight.add(task);
  return task.finally(() => inflight.delete(task));
} });
await ui.el("open-gitlab-source").emit("click"); await settle(); await Promise.all([...inflight]); await settle();
assert.equal(ui.el("notification-manage-binding").disabled, true);
assert.equal(ui.el("gitlab-events-project").value, "remote/project");
const catalog = await call("/v1/rooms");
const params = new URLSearchParams({ ...catalog.rooms[0].service, service_epoch: catalog.service_epoch, caller_id: catalog.operator });
const before = await call(`/v1/workspace?${params}`);
assert.equal(before.workspace.repository, undefined); assert.equal(before.workspace.binding, undefined);
const previous = (await call(`/v1/platform/notifications?${params}`)).subscriptions.find(s => s.channel === "events");
ui.el("gitlab-events-project").value = "remote/other"; await ui.el("gitlab-events-project").emit("input");
assert.equal(ui.el("gitlab-events-save").disabled, false);
await ui.el("gitlab-events-save").emit("click");
assert.equal(ui.el("gitlab-events-error").hidden, true, ui.el("gitlab-events-error").textContent);
const list = await call(`/v1/platform/notifications?${params}`), current = list.subscriptions.find(s => s.channel === "events");
assert.notEqual(current.subscription_id, previous.subscription_id);
assert.equal(current.project.project_id, 8); assert.equal(current.config.project, "remote/other");
assert.equal(list.retired_subscriptions.length, 1); assert.equal(list.retired_subscriptions[0].retired, true);
ui.el("gitlab-events-enabled").checked = false; await ui.el("gitlab-events-enabled").emit("change");
await ui.el("gitlab-events-save").emit("click");
assert.equal((await call(`/v1/platform/notifications?${params}`)).subscriptions.find(s => s.channel === "events").config.enabled, false);
assert.deepEqual((await call(`/v1/workspace?${params}`)).workspace, before.workspace);
console.error("independent Events app/handler: no Git, replace project, retain predecessor, stop passed");
process.exit(0);
