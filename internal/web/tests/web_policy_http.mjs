import assert from "node:assert/strict";
import { launch, settle } from "./web_app_harness.mjs";
import { request } from "../assets/client.mjs";

const endpoint = process.env.TEAMX_WEB_ENDPOINT;
const call = (path, payload) => request(endpoint + path, payload);
const waitFor = async predicate => {
  for (let i = 0; i < 500; i++) {
    if (predicate()) return;
    await new Promise(resolve => setTimeout(resolve, 10));
  }
  assert.fail("policy page did not finish refreshing");
};
// Actual page handlers call the real manager/service and policy store.
const ui = await launch({ request: call });
await ui.create("Policy HTTP");
for (let i = 0; i < 100; i++) {
  await ui.refresh();
  if (ui.el("members").children.length) break;
  await new Promise(resolve => setTimeout(resolve, 10));
}
let catalog;
for (let i = 0; i < 300; i++) {
  try { catalog = await call("/v1/rooms"); break; }
  catch (error) { if (error.code !== "busy") throw error; }
  await new Promise(resolve => setTimeout(resolve, 10));
}
assert.ok(catalog, "Room catalog did not settle");
const room = catalog.rooms[0];
const caller = { kind: "human", id: catalog.operator };
assert.ok(room.service);
const members = await call("/v1/member/list", { ...room.service, caller });
const member = members.members[0];
const context = { ...room.service, caller, agent_id: member.agent_id, membership_id: member.membership_id };
const update = policy => call("/v1/policy/update", { ...context, policy });
const read = () => call("/v1/policy/read", context);
let view = await read();
assert.equal(view.version, 1);
assert.deepEqual(view.effective, []);
const owner = { id: "owner", version: 0, status: "active", text: "AgenticHub 产品负责人是 huayi", source_text: "记住，产品负责人是 huayi" };
view = await update(owner);
assert.equal(view.record.id, owner.id);
assert.equal(view.record.version, 1);
assert.match(view.policy_token, /^[a-f0-9]{64}$/);
assert.equal(view.effective.length, 1);
const contacts = { id: "contacts", version: 0, status: "active", text: "AgenticHub 的这类报错，优先建议找 hongwei 和 luojun", source_text: "记住，这类报错优先找 hongwei 和 luojun" };
view = await update(contacts);
assert.equal(view.effective.length, 2);
for (const key of ["kind", "scope", "scope_from", "actions", "conditions", "exceptions"]) {
  await assert.rejects(update({ ...contacts, version: 1, [key]: null }), /unknown JSON field/);
}
view = await update({ ...contacts, version: 1, text: "这类报错先建议找 hongwei；未明确授权时先询问是否联系", source_text: "修改处理约定" });
assert.equal(view.current[1].version, 2);
view = await read();
assert.equal(view.revision, 3);
assert.match(view.current[1].text, /未明确授权/);
view = await update({ ...contacts, version: 2, status: "revoked", text: view.current[1].text, source_text: "撤销处理约定" });
assert.equal(view.revision, 4);
assert.equal(view.effective.length, 1);
assert.equal(view.current[1].status, "revoked");
for (const record of view.history) {
  for (const key of ["kind", "scope", "scope_from", "actions", "conditions", "exceptions"]) assert.equal(Object.hasOwn(record, key), false);
}
await ui.el("members").children[0].querySelector(".member-summary").emit("click");
await settle();
await ui.el("agent-tab-policy").emit("click");
await waitFor(() => ui.el("policy-entries").children.length === 2);
assert.equal(ui.el("policy-entries").children.length, 2);
await ui.el("policy-new").emit("click");
ui.el("policy-text").value = "以后先给结论，再给理由";
await ui.el("policy-form").emit("submit");
await waitFor(() => ui.el("policy-entries").children.length === 3);
view = await read();
const saved = view.current.find(r => r.text === "以后先给结论，再给理由");
assert.ok(saved);
assert.equal(saved.status, "active");
assert.equal(saved.version, 1);
assert.equal(saved.source.user, catalog.operator);
const card = () => ui.el("policy-entries").children[2];
await card().children[0].children[1].children[0].emit("click");
ui.el("policy-text").value = "以后先给结论，再给必要理由；保留限制";
await ui.el("policy-form").emit("submit");
await waitFor(() => card()?.children[1].textContent.includes("保留限制"));
view = await read();
assert.equal(view.current[2].version, 2);
assert.match(view.current[2].text, /保留限制/);
await card().children[0].children[1].children[1].emit("click");
await waitFor(() => card()?.children[0].children[0].children[0].textContent === "已撤销");
await ui.el("policy-retry").emit("click");
await waitFor(() => card()?.children[0].children[0].children[0].textContent === "已撤销");
view = await read();
assert.equal(view.current[2].status, "revoked");
assert.equal(view.effective.length, 1);
assert.equal(view.history.filter(r => r.id === saved.id).length, 3);
assert.equal(card().children[0].children[0].children[0].textContent, "已撤销");
assert.equal(card().children[3].children.length, 5); // summary + ID + all revisions
for (const record of view.history) {
  for (const key of ["kind", "scope", "scope_from", "actions", "conditions", "exceptions"]) assert.equal(Object.hasOwn(record, key), false);
}
console.log("Policy management HTTP: page save/edit/revoke/refresh/history; natural rules coexist; removed fields rejected; version remains 1.");

// A draft based on a record version survives another rule's save.
await card().children[0].children[1].children[0].emit("click");
ui.el("policy-text").value = "独立编辑：中文简明并保留限制";
await update({ ...owner, version: 1, text: "AgenticHub 产品负责人是 huayi；核对最新职责", source_text: "补充负责人知识" });
await ui.el("policy-form").emit("submit");
await waitFor(() => card()?.children[1].textContent.includes("独立编辑"));
view = await read();
const independent = view.current.find(r => r.id === saved.id);
assert.equal(independent.version, 4);
assert.equal(independent.status, "active");
assert.equal(view.current.find(r => r.id === owner.id).version, 2);

// Same-record concurrency rejects the old form and preserves its local input.
await card().children[0].children[1].children[0].emit("click");
ui.el("policy-text").value = "尚未保存的本地规则草稿";
await update({ id: saved.id, version: 4, status: "active", text: "另一入口保存的最新规则", source_text: "新的人类修改" });
await ui.el("policy-form").emit("submit");
await waitFor(() => ui.el("policy-form-status").textContent.includes("保存失败"));
assert.equal(ui.el("policy-form").hidden, false);
assert.equal(ui.el("policy-text").value, "尚未保存的本地规则草稿");
view = await read();
assert.equal(view.current.find(r => r.id === saved.id).version, 5);
assert.equal(view.current.find(r => r.id === saved.id).text, "另一入口保存的最新规则");
console.log("Policy observation HTTP: unrelated rule edits merge; same-rule conflict preserves the draft.");

assert.match(ui.el("policy-form-status").textContent, /本次修改未保存/);
await ui.el("policy-form-retry").emit("click");
await settle();
assert.equal(ui.el("policy-text").value, "尚未保存的本地规则草稿");
assert.equal(ui.el("policy-save").disabled, false);
assert.match(card().children[1].textContent, /另一入口保存的最新规则/);
await ui.el("policy-form").emit("submit");
await waitFor(() => card()?.children[1].textContent.includes("尚未保存的本地规则草稿"));
view = await read();
assert.equal(view.current.find(r => r.id === saved.id).version, 6);
console.log("Policy conflict recovery HTTP: inline failure, refreshed current rule, retained draft, explicit save with the latest version.");
