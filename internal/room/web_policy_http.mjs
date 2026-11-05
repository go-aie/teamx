import assert from "node:assert/strict";
import { launch, settle } from "./web_app_harness.mjs";
import { request } from "./web/client.mjs";
const elementText = element => element ? [element.textContent,...element.children.map(elementText)].join(" ") : "";
const endpoint=process.env.TEAMX_WEB_ENDPOINT;
const call=(path,payload)=>request(endpoint+path,payload);
const ui=await launch({request:call});
await ui.create("Policy HTTP");
for(let i=0;i<100;i++) {
 await ui.refresh();
 if(ui.el("members").children.length)break;
 await new Promise(resolve=>setTimeout(resolve,10));
}
const catalog=await call("/v1/rooms"),room=catalog.rooms[0],caller={kind:"human",id:catalog.operator};
assert.ok(room.service);
let connection=await call("/v1/platform/connections/save",{service_epoch:catalog.service_epoch,caller,id:"b".repeat(32),version:0,name:"Synthetic GitLab",provider:"gitlab",url:"https://gitlab.invalid",pat:"local-test-token"});
connection=await call("/v1/platform/connections/verify",{service_epoch:catalog.service_epoch,caller,id:connection.id,version:connection.version});
await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();
await ui.el("agent-tab-policy").emit("click");await ui.el("policy-new").emit("click");await settle();
ui.el("policy-text").value="普通结果简短中文回复";
await ui.el("policy-form").emit("submit");await settle();
assert.match(elementText(ui.el("policy-entries").children[0].children[0]),/用户偏好.*有效/);
await ui.el("policy-new").emit("click");await settle();
ui.el("policy-kind").value="rule";await ui.el("policy-kind").emit("change");
for(let i=0;i<100 && ui.el("policy-connection").value!==connection.id;i++)await new Promise(resolve=>setTimeout(resolve,10));
assert.equal(ui.el("policy-connection").value,connection.id,ui.el("policy-connection-hint").textContent);
ui.el("policy-project").value="group/project";
ui.el("policy-text").value="有现行文档依据的使用问题直接回复；涉及交付时间先问我。";
await ui.el("policy-form").emit("submit");await settle();
const rule=ui.el("policy-entries").children[1];assert.match(elementText(rule.children[0]),/长期规则.*有效/);
await rule.children[0].children[1].children[0].emit("click");
ui.el("policy-text").value="核实文档后回复使用问题；排期、审批先问我。";
await ui.el("policy-form").emit("submit");await settle();
await ui.el("policy-reload").emit("click");await settle();
assert.match(ui.el("policy-entries").children[1].children[1].textContent,/排期、审批先问我/);
await ui.el("policy-entries").children[1].children[0].children[1].children[1].emit("click");await settle();
for(let i=0;i<100 && !/已撤销/.test(elementText(ui.el("policy-entries").children[1]?.children[0]));i++)await new Promise(resolve=>setTimeout(resolve,10));
assert.match(elementText(ui.el("policy-entries").children[1].children[0]),/已撤销/,ui.el("policy-status").textContent);
const members=await call("/v1/member/list",{...room.service,caller});
const member=members.members[0];
const policy=await call("/v1/policy/read",{...room.service,caller,agent_id:member.agent_id,membership_id:member.membership_id});
assert.equal(policy.revision,4);assert.equal(policy.effective.length,1);
for(const record of policy.history) {
 for(const key of ["actions","conditions","exceptions"])assert.equal(Object.hasOwn(record,key),false);
 assert.equal(Object.hasOwn(record.scope,"description"),false);
}
assert.deepEqual(policy.current[1].scope,{provider:"gitlab",instance:"https://gitlab.invalid",account:"policy-test",project:"group/project"});
console.log("Policy actual app/HTTP: preference create; rule create/edit/reload/revoke; stored body and machine scope verified.");
