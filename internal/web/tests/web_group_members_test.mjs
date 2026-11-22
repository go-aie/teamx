import { test } from "node:test";
import assert from "node:assert/strict";
import { launch, settle } from "./web_app_harness.mjs";
import { fixture } from "./web_member_platform_fixture.mjs";

const humanRow = ui => ui.el("members").children.at(-1);

test("Room counts include the human participant without adding Agent controls or changing Agent identity", async () => {
  const api = fixture(), ui = await launch(api);
  assert.equal(ui.el("room-title-count").textContent, "(3)");
  assert.equal(String(ui.el("member-count").textContent), "3");
  assert.equal(ui.el("members").children.length, 3);
  const human = humanRow(ui), summary = human.querySelector(".member-summary");
  assert.equal(human.querySelector(".member-name").textContent, "tester");
  assert.equal(summary.getAttribute("aria-label"), "tester，你");
  assert.equal(human.querySelector(".avatar").classList.contains("user-avatar"), true);
  const calls = api.calls.length;
  await summary.emit("click");
  assert.equal(api.calls.length, calls);
  assert.equal(ui.el("activity-dialog").open, false);
  await ui.el("toggle-member-list").emit("click");
  await ui.refresh();
  assert.equal(humanRow(ui), human);
  await ui.el("members").children[0].querySelector(".member-summary").emit("click"); await settle();
  assert.equal(ui.el("activity-dialog").open, true);
  await ui.el("agent-tab-activity").emit("click"); await settle();
  assert.equal(api.calls.findLast(c => c.path === "/v1/member/activity").body.membership_id, "member-a");
  assert.equal(api.members.length, 2);
});

test("the human member follows local display name edits, reloads and other tabs", async () => {
  const api = fixture(), names = new Map();
  const localStorage = {getItem:key=>names.get(key), setItem:(key,value)=>names.set(key,value), removeItem:key=>names.delete(key)};
  const ui = await launch(api, new Map(), {localStorage});
  ui.el("user-display-name").value = "阿罗"; await ui.el("user-name-form").emit("submit");
  assert.equal(humanRow(ui).querySelector(".member-name").textContent, "阿罗");
  assert.equal(humanRow(ui).querySelector(".member-summary").title, "阿罗 (你)");
  const reopened = await launch(api, new Map(), {localStorage});
  assert.equal(humanRow(reopened).querySelector(".member-name").textContent, "阿罗");
  const key = [...names.keys()][0]; names.set(key, "另一个名字");
  await ui.window.emit("storage", {key});
  assert.equal(humanRow(ui).querySelector(".member-name").textContent, "另一个名字");
  ui.el("user-display-name").value = ""; await ui.el("user-name-form").emit("submit");
  assert.equal(humanRow(ui).querySelector(".member-name").textContent, "tester");
  assert.equal(ui.el("room-title-count").textContent, "(3)");
});

test("a Room with no Agents still shows its human, while direct chats open the profile directly", async () => {
  const api = fixture(); api.members.length = 0;
  const ui = await launch(api);
  assert.equal(ui.el("room-title-count").textContent, "(1)");
  assert.equal(ui.el("members").children.length, 1);
  assert.equal(ui.el("member-empty").hidden, false);
  assert.equal(humanRow(ui).querySelector(".member-name").textContent, "tester");

  const direct = fixture(); direct.members.length = 1;
  const original = direct.request.bind(direct);
  direct.request = async (path, body) => {
    const result = await original(path, body);
    if (path === "/v1/rooms") result.rooms[0].is_direct = true;
    return result;
  };
  const dm = await launch(direct); await settle();
  assert.equal(dm.el("room-title-count").hidden, true);
  assert.equal(dm.el("members").children.length, 1);
  assert.equal(dm.el("activity-dialog").open, true);
  assert.equal(dm.el("member-list-view").hidden,true);
  assert.equal(dm.el("agent-tab-profile").getAttribute("aria-selected"),"true");
});
