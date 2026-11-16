import { test } from "node:test";
import assert from "node:assert/strict";
import { launch, settle, openMemberInbox, openNotification } from "./web_app_harness.mjs";
import { fixture, target, key } from "./web_member_platform_fixture.mjs";

const row = (ui, id) => ui.el("member-notification-platforms").children.find(r => r.dataset.platformId === id);
const tool = (ui, id) => row(ui, id).children[1].children[2].children[0];
const notification = (ui, id) => row(ui, id).children[1].children[1];
const reads = (api, path) => api.calls.filter(c => c.path.startsWith(`${path}?`)).length;

async function subscribedUI(api) {
  const ui = await launch(api);
  const poll = () => { for (const topic of ["platforms", "notifications", "tools"]) ui.invalidate(topic); };
  await openMemberInbox(ui, "member-a");
  await ui.el("agent-tab-platforms").emit("click");
  return { ui, poll, tick: async () => { poll(); await ui.clock.advance(2000); await settle(); } };
}

for (const id of ["gl", "lark"]) test(`resource updates reflects conversational ${id} tool and notification changes together`, async () => {
  const api = fixture(), { ui, tick } = await subscribedUI(api);
  for (const enabled of [true, false]) {
    const binding = { agent_id: "a", membership_id: "member-a", tool: id === "gl" ? "gitlab" : "lark", platform_id: id };
    api.tools.bindings = enabled ? [binding] : [];
    api.tools.disabled = enabled ? [] : [binding];
    api.records.get(key(id, target())).config.enabled = enabled;
    api.platforms.find(p => p.id === id).name = "更新后的平台";
    api.calls.length = 0;
    await tick();
    assert.equal(tool(ui, id).checked, enabled);
    assert.equal(notification(ui, id).checked, enabled);
    assert.equal(row(ui, id).children[0].children[1].children[0].textContent, "更新后的平台");
    for (const path of ["/v1/platforms", "/v1/tools/bindings", "/v1/platform/notifications"]) assert.equal(reads(api, path), 1, path);
    assert.equal(api.calls.some(c => ["/v1/tools/bind", "/v1/platform/notifications/configure", "/v1/platform/gitlab/notifications/configure"].includes(c.path)), false);
  }
});

for (const failed of ["tools/bindings", "platform/notifications", "platforms"]) test(`resource updates updates the other setting when ${failed} fails and recovers without writes`, async () => {
  const api = fixture(), { ui, tick } = await subscribedUI(api);
  api.tools.bindings = [{ agent_id: "a", membership_id: "member-a", tool: "gitlab", platform_id: "gl" }];
  api.records.get(key("gl", target())).config.enabled = false;
  api.hook = path => path.startsWith(`/v1/${failed}?`) ? Promise.reject(Error("offline")) : undefined;
  await tick();
  if (failed === "tools/bindings") {
    assert.equal(notification(ui, "gl").checked, false);
    assert.equal(tool(ui, "gl").disabled, true);
  } else assert.equal(tool(ui, "gl").checked, true);
  if (failed === "platforms") {
    assert.equal(notification(ui, "gl").checked, false);
    assert.equal(tool(ui, "gl").disabled, true);
  }
  assert.equal(ui.el("member-notification-list-reload").hidden, false);
  api.hook = null;
  await tick();
  assert.equal(tool(ui, "gl").checked, true);
  assert.equal(notification(ui, "gl").checked, false);
  assert.equal(ui.el("member-notification-list-reload").hidden, true);
  assert.equal(api.calls.some(c => c.path === "/v1/tools/bind"), false);
});

test("resource updates skips closed member settings and keeps reading notification summaries", async () => {
  const api = fixture(), { ui, tick } = await subscribedUI(api);
  await ui.el("close-activity").emit("click");
  api.calls.length = 0;
  await tick();
  assert.equal(reads(api, "/v1/tools/bindings"), 0);
  assert.equal(reads(api, "/v1/platform/notifications"), 1);
});

test("a late resource updates read cannot replace another member's tool settings", async () => {
  const api = fixture(), { ui, poll } = await subscribedUI(api);
  let finish;
  api.hook = path => path.startsWith("/v1/tools/bindings?") ? new Promise(resolve => { finish = resolve; }) : undefined;
  poll(); await ui.clock.advance(1000); await settle(); assert.ok(finish);
  api.hook = null;
  await openMemberInbox(ui, "member-b");
  finish({ ...structuredClone(api.tools), bindings: [{ agent_id: "b", membership_id: "member-b", tool: "gitlab", platform_id: "gl" }] });
  await ui.refresh(); await settle();
  assert.equal(ui.selectedAgent().membership_id, "member-b");
  assert.equal(tool(ui, "gl").checked, false);
  assert.ok(ui.el("member-notification-platforms").children.every(r => r.dataset.membershipId === "member-b"));
});

test("resource updates shares an in-flight sync and preserves notification drafts", async () => {
  const api = fixture(), { ui, poll } = await subscribedUI(api);
  await openNotification(ui, "gitlab", "gl", true);
  ui.el("gitlab-interval").value = "72";
  await ui.el("gitlab-interval").emit("input");
  let finish;
  api.hook = path => path.startsWith("/v1/tools/bindings?") ? new Promise(resolve => { finish = resolve; }) : undefined;
  api.calls.length = 0;
  poll(); await ui.clock.advance(1000); await settle(); assert.ok(finish);
  poll(); poll(); await settle();
  assert.equal(reads(api, "/v1/tools/bindings"), 1);
  assert.equal(reads(api, "/v1/platforms"), 1);
  api.hook = null; finish(structuredClone(api.tools));
  await ui.refresh(); await settle();
  assert.equal(Number(ui.el("gitlab-interval").value), 72);
});
