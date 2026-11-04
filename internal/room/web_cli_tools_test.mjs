import { test } from "node:test";
import assert from "node:assert/strict";
import { launch, settle } from "./web_app_harness.mjs";

function fixture() {
  const members = ["a", "b"].map(id => ({agent_id:`agent-${id}`,membership_id:`member-${id}`,status:"idle",runner:{profile:"p"},unread_count:0,read_through:0}));
  const data = { revision:0, bindings:[], catalog:[{name:"gitlab",provider:"gitlab",version:"glab 1.127.0"},{name:"local",version:"fixture"}] };
  const connection = {id:"connection",name:"Personal",provider:"gitlab",url:"https://gitlab.invalid",version:3,validation:{status:"valid",username:"me"}};
  const api = {members,data,calls:[],hook:null,async request(path,body) {
    api.calls.push({path,body:structuredClone(body)});
    const overridden=api.hook?.(path,body);if(overridden!==undefined)return overridden;
    if(path==="/v1/ui")return {managed:true};
    if(path==="/v1/rooms")return {service_epoch:"manager",operator:"tester",active_room_id:"A",active:{room_id:"A",room_instance:"instance-A",service_epoch:"active-A"},rooms:[{room_id:"A",room_instance:"instance-A",name:"A",status:"ready"}],teams:[]};
    if(path.startsWith("/v1/workspace?"))return {workspace:{root:"workspaces"},workspace_version:1,members:[],credential_status:"missing"};
    if(path.startsWith("/v1/platform/notifications?"))return {subscriptions:[]};
    if(path.startsWith("/v1/platform/connections?"))return {connections:[connection]};
    if(path.startsWith("/v1/tools/bindings?"))return structuredClone(data);
    if(path==="/v1/tools/bind") {
      assert.equal(body.revision,data.revision);assert.equal(body.service_epoch,"manager");
      const b=body.binding;assert.ok(!("operations" in b));assert.ok(!("scope" in b));
      data.bindings=data.bindings.filter(old=>old.tool!==b.tool||old.membership_id!==b.membership_id);
      if(body.enabled)data.bindings.push({...b,connection_generation:b.connection_id?"generation":undefined});
      data.revision++;return structuredClone(data);
    }
    if(path==="/v1/member/list")return {members:structuredClone(members),latest_seq:0};
    if(path==="/v1/member/activity")return {member:structuredClone(members.find(m=>m.membership_id===body.membership_id))};
    if(path==="/v1/read")return {messages:[],snapshot_seq:0,has_more:false};
    throw Error(`Unexpected ${path}`);
  }};return api;
}
async function open(ui) {
  await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();
  await ui.el("agent-tab-tools").emit("click");await settle();
}
const row=(ui,n=0)=>ui.el("cli-member-options").children[n];
const select=(ui,n=0)=>row(ui,n).children[1].children[0];
const apply=(ui,n=0)=>row(ui,n).children[2];
const feedback=(ui,n=0)=>row(ui,n).children[3];
test("member tool settings use only explicit CLI bindings and retire legacy controls",async()=>{
  const api=fixture(),ui=await launch(api);await open(ui);
  assert.equal(ui.el("tools-dialog"),undefined);assert.equal(ui.el("tools-member-options"),undefined);
  assert.equal(api.calls.some(c=>c.path.startsWith("/v1/platform/tools")),false);
  assert.equal(select(ui).value,"");assert.equal(api.data.bindings.length,0);
  assert.match(select(ui).options[1].textContent,/@me.*gitlab.invalid/);
  select(ui).value="connection";await select(ui).emit("change");
  assert.equal(api.calls.filter(c=>c.path==="/v1/tools/bind").length,0);
  await apply(ui).emit("click");
  const call=api.calls.find(c=>c.path==="/v1/tools/bind");
  assert.equal(call.body.binding.connection_id,"connection");assert.equal(call.body.connection_version,3);
  assert.match(feedback(ui).textContent,/已应用.*Personal.*@me.*账号权限/);
  await ui.refresh();assert.match(feedback(ui).textContent,/已应用/);
  select(ui,1).value="on";await apply(ui,1).emit("click");
  assert.equal(api.data.bindings[1].tool,"local");assert.equal(api.data.bindings[1].connection_id,undefined);
  assert.match(feedback(ui,1).textContent,/local 已启用/);
  select(ui).value="";await apply(ui).emit("click");assert.equal(api.data.bindings.length,1);
  assert.match(feedback(ui).textContent,/gitlab 已停用/);
  select(ui).value="connection";await select(ui).emit("change");assert.equal(feedback(ui).textContent,"");
});
test("pending binding stays visible and blocks duplicate submissions until confirmed",async()=>{
  const api=fixture(),ui=await launch(api);await open(ui);
  let release;api.hook=path=>path==="/v1/tools/bind"?new Promise(r=>release=r):undefined;
  select(ui).value="connection";const saving=apply(ui).emit("click");await settle();
  assert.equal(apply(ui).textContent,"应用中…");assert.equal(select(ui).value,"connection");
  assert.match(feedback(ui).textContent,/正在保存/);
  assert.equal(select(ui).disabled,true);assert.equal(apply(ui,1).disabled,true);
  await apply(ui).emit("click");await ui.el("cli-member-refresh").emit("click");
  assert.equal(api.calls.filter(c=>c.path==="/v1/tools/bind").length,1);
  release({...api.data,revision:1,bindings:[{agent_id:"agent-a",membership_id:"member-a",tool:"gitlab",connection_id:"connection"}]});
  await saving;assert.match(feedback(ui).textContent,/已应用/);assert.equal(select(ui).disabled,false);
});
test("uncertain binding is read back explicitly without replay",async()=>{
  const api=fixture(),ui=await launch(api);await open(ui);
  api.hook=path=>path==="/v1/tools/bind"?Promise.reject(Error("lost")):undefined;
  select(ui).value="connection";await apply(ui).emit("click");
  assert.match(ui.el("cli-member-feedback").textContent,/未确认.*不会自动重发/);
  assert.equal(ui.el("cli-member-feedback").dataset.kind,"error");
  assert.equal(api.calls.filter(c=>c.path==="/v1/tools/bind").length,1);
  assert.equal(ui.el("cli-member-options").children.length,0);
  api.hook=null;await ui.el("cli-member-refresh").emit("click");assert.equal(select(ui).value,"");
});
test("binding rejection explains failure while busy remains an uncertain outcome",async()=>{
  const api=fixture(),ui=await launch(api);await open(ui);
  for (const code of ["conflict","identity","request","capacity","busy"]) {
    api.hook=path=>path==="/v1/tools/bind"?Promise.reject(Object.assign(Error(code),{code})):undefined;
    select(ui).value="connection";await apply(ui).emit("click");
    assert.match(ui.el("cli-member-feedback").textContent,code==="busy"?/结果未确认/:/应用失败/);
    await ui.el("cli-member-refresh").emit("click");assert.equal(ui.el("cli-member-feedback").textContent,"");
  }
  assert.equal(api.calls.filter(c=>c.path==="/v1/tools/bind").length,5);
});
test("late CLI reads cannot populate a different member",async()=>{
  const api=fixture(),ui=await launch(api);await open(ui);
  let release;api.hook=path=>path.startsWith("/v1/tools/bindings?")?new Promise(r=>release=r):undefined;
  const reading=ui.el("cli-member-refresh").emit("click");await settle();api.hook=null;
  ui.el("activity-member").value="member-b";await ui.el("activity-member").emit("change");await settle();
  const rows=ui.el("cli-member-options").children;
  release({...structuredClone(api.data),catalog:[]});await reading;
  assert.equal(ui.el("cli-member-options").children,rows);
  select(ui).value="connection";await apply(ui).emit("click");
  assert.equal(api.calls.find(c=>c.path==="/v1/tools/bind").body.binding.membership_id,"member-b");
});
