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
    if(path==="/v1/rooms")return {service_epoch:"manager",operator:"tester",rooms:[{room_id:"A",room_instance:"instance-A",name:"A",status:"ready",runtime_status:"ready",service:{room_id:"A",room_instance:"instance-A",service_epoch:"active-A"}}],teams:[]};
    if(path.startsWith("/v1/workspace?"))return {workspace:{root:"workspaces"},workspace_version:1,members:[],credential_status:"missing"};
    if(path.startsWith("/v1/platform/notifications?"))return {subscriptions:[]};
    if(path.startsWith("/v1/platforms?"))return {platforms:[connection]};
    if(path.startsWith("/v1/tools/bindings?"))return structuredClone(data);
    if(path==="/v1/tools/bind") {
      assert.equal(body.revision,data.revision);assert.equal(body.service_epoch,"manager");
      const b=body.binding;assert.ok(!("operations" in b));assert.ok(!("scope" in b));
      data.bindings=data.bindings.filter(old=>old.tool!==b.tool||old.membership_id!==b.membership_id||old.platform_id!==b.platform_id);
      if(body.enabled)data.bindings.push({...b,platform_generation:b.platform_id?"generation":undefined});
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
test("same-type platform rows preserve independent bindings and dependency feedback",async()=>{
 const api=fixture();
 const platforms=["one","two"].map(id=>({id,name:id,provider:"gitlab",version:1,url:"https://gitlab.invalid",validation:{status:"valid",username:id}}));
 api.hook=path=>path.startsWith("/v1/platforms?")?{platforms}:undefined;
 api.data.bindings=platforms.map(p=>({agent_id:"agent-a",membership_id:"member-a",tool:"gitlab",platform_id:p.id}));
 api.data.selections=[{agent_id:"agent-a",membership_id:"member-a",tool_type:"gitlab",platform_id:"two",capabilities:["read"],unavailable_reason:"executable_unavailable"}];
 const ui=await launch(api);await open(ui);
 assert.equal(select(ui,0).value,"one");assert.equal(select(ui,1).value,"two");
 assert.match(feedback(ui,1).textContent,/程序缺失.*保留启用设置/);
 select(ui,0).value="";await apply(ui,0).emit("click");
 const sent=api.calls.filter(c=>c.path==="/v1/tools/bind").at(-1).body;
 assert.equal(sent.enabled,false);assert.equal(sent.binding.platform_id,"one");assert.equal(sent.platform_version,0);
 assert.deepEqual(api.data.bindings.map(b=>b.platform_id),["two"]);
 assert.equal(select(ui,1).value,"two");assert.match(feedback(ui,1).textContent,/程序缺失/);
});
test("Scion member binding requires an explicit verified connection selection",async()=>{
  const api=fixture();api.data.catalog.push({name:"scion",provider:"scion",version:"scn 0.1.13"});
  const scion={id:"scion-profile",name:"Scion demo",provider:"scion",url:"https://scion.invalid",version:2,validation:{status:"valid",username:"alice"}};
  api.hook=path=>path.startsWith("/v1/platforms?")?{platforms:[scion,{...scion,id:"unverified",validation:{status:"unverified"}}]}:undefined;
  const ui=await launch(api);await open(ui);
  assert.equal(select(ui,2).value,"");assert.equal(select(ui,2).options.length,2);
  assert.equal(api.calls.some(c=>c.path==="/v1/tools/bind"),false);
  select(ui,2).value="scion-profile";await apply(ui,2).emit("click");
  const {body}=api.calls.find(c=>c.path==="/v1/tools/bind");
  assert.equal(body.binding.tool,"scion");assert.equal(body.binding.platform_id,"scion-profile");
  assert.equal(body.platform_version,2);assert.equal(body.binding.action_authorization,undefined);
  assert.match(feedback(ui,2).textContent,/Scion demo.*alice/);
});
test("Lark settings use a single connection apply and expose no per-mode switches",async()=>{
  const api=fixture();api.data.catalog.push({name:"lark",provider:"lark",version:"1.0.99"});
  api.hook=path=>path.startsWith("/v1/platforms?")?{platforms:[{id:"lark",name:"飞书测试账号",provider:"lark",url:"https://open.feishu.cn",version:1,validation:{status:"valid",username:"测试用户"}}]}:undefined;
  const ui=await launch(api);await open(ui);
  assert.equal(row(ui,2).children.length,5);
  assert.equal(row(ui,2).children[4].tag,"p");
  assert.match(row(ui,2).children[4].textContent,/应用后.*按你的指令/);
  assert.equal(api.calls.some(c=>c.path==="/v1/tools/bind"),false);
  assert.equal(select(ui,2).options[1].textContent,"启用");assert.match(row(ui,2).children[0].children[0].textContent,/测试用户/);
  select(ui,2).value="lark";await apply(ui,2).emit("click");
  const binding=api.calls.find(c=>c.path==="/v1/tools/bind").body.binding;
  assert.deepEqual(binding.action_authorization.grants,[]);
});
test("member tool settings use only explicit CLI bindings and retire legacy controls",async()=>{
  const api=fixture(),ui=await launch(api);await open(ui);
  assert.equal(ui.el("tools-dialog"),undefined);assert.equal(ui.el("tools-member-options"),undefined);
  assert.equal(api.calls.some(c=>c.path.startsWith("/v1/platform/tools")),false);
  assert.equal(select(ui).value,"");assert.equal(api.data.bindings.length,0);
  assert.equal(select(ui).options[1].textContent,"启用");assert.match(row(ui).children[0].children[0].textContent,/@me.*gitlab.invalid/);
  select(ui).value="connection";await select(ui).emit("change");
  assert.equal(api.calls.filter(c=>c.path==="/v1/tools/bind").length,0);
  await apply(ui).emit("click");
  const call=api.calls.find(c=>c.path==="/v1/tools/bind");
  assert.equal(call.body.binding.platform_id,"connection");assert.equal(call.body.platform_version,3);
  assert.match(feedback(ui).textContent,/已应用.*Personal.*@me/);
  await ui.refresh();assert.match(feedback(ui).textContent,/已应用/);
  select(ui,1).value="on";await apply(ui,1).emit("click");
  assert.equal(api.data.bindings[1].tool,"local");assert.equal(api.data.bindings[1].platform_id,undefined);
  assert.match(feedback(ui,1).textContent,/local 已启用/);
  select(ui).value="";await apply(ui).emit("click");assert.equal(api.data.bindings.length,1);
  assert.match(feedback(ui).textContent,/Personal 已停用/);
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
  release({...api.data,revision:1,bindings:[{agent_id:"agent-a",membership_id:"member-a",tool:"gitlab",platform_id:"connection"}]});
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

test("Lark applies all accepted capabilities by default on the selected connection",async()=>{
  const api=fixture();
  api.data.catalog.push({name:"lark",provider:"lark",version:"1.0.99",actions:[
    {operation:"message.reply",target_kind:"message",object_types:["message"]},
    {operation:"message.send",target_kind:"object",object_types:["chat"]}
  ]});
  const platforms=["one","two"].map(id=>({id,name:id,provider:"lark",url:"https://open.feishu.cn",version:1,validation:{status:"valid",username:id}}));
  api.hook=path=>path.startsWith("/v1/platforms?")?{platforms}:undefined;
  const ui=await launch(api);await open(ui);
  const grants=[{operation:"message.reply",target_kind:"message",object_type:"message"},{operation:"message.send",target_kind:"object",object_type:"chat"}];
  // Merely loading an existing read-only binding must not mutate it.
  api.data.bindings.push({agent_id:"agent-a",membership_id:"member-a",tool:"lark",platform_id:"one"});
  await ui.el("cli-member-refresh").emit("click");
  assert.equal(api.data.bindings[0].action_authorization,undefined);
  assert.equal(api.calls.filter(c=>c.path==="/v1/tools/bind").length,0);
  assert.equal(row(ui,2).children.length,5);
  await apply(ui,2).emit("click");
  let binding=api.calls.filter(c=>c.path==="/v1/tools/bind").at(-1).body.binding;
  assert.equal(binding.platform_id,"one");assert.deepEqual(binding.action_authorization.grants,grants);
  select(ui,3).value="two";await select(ui,3).emit("change");
  assert.equal(api.calls.filter(c=>c.path==="/v1/tools/bind").length,1);
  await apply(ui,3).emit("click");
  binding=api.calls.filter(c=>c.path==="/v1/tools/bind").at(-1).body.binding;
  assert.equal(binding.platform_id,"two");assert.deepEqual(binding.action_authorization.grants,grants);
  select(ui,2).value="";await apply(ui,2).emit("click");
  assert.equal(api.calls.filter(c=>c.path==="/v1/tools/bind").at(-1).body.enabled,false);
  assert.equal(api.data.bindings.length,1);
  assert.equal(api.data.bindings[0].platform_id,"two");
});

test("Lark defaults exclude unavailable capabilities without exposing acceptance details",async()=>{
 const api=fixture();
 api.data.catalog.push({name:"lark",provider:"lark",version:"1.0.99",actions:[
   {operation:"message.reply",target_kind:"message",object_types:["message"]},
   {operation:"message.send",target_kind:"object",object_types:["user","chat"],disabled_reason:"lark_send_acceptance_pending: 真实验收说明。"},
   {binding_evidence:"self_delivery",operation:"message.send_self",target_kind:"object",object_types:["user"],disabled_reason:"unavailable: 本人自聊不可用。"}
 ]});
 api.hook=path=>path.startsWith("/v1/platforms?")?{platforms:[{id:"lark",name:"测试",provider:"lark",url:"https://open.feishu.cn",version:1,validation:{status:"valid",username:"测试用户"}}]}:undefined;
 const ui=await launch(api);await open(ui);
 select(ui,2).value="lark";await select(ui,2).emit("change");
 assert.equal(row(ui,2).children.length,5);
 assert.doesNotMatch(row(ui,2).children[4].textContent,/验收|acceptance_pending/);
 await apply(ui,2).emit("click");
 const binding=api.calls.filter(c=>c.path==="/v1/tools/bind").at(-1).body.binding;
 assert.deepEqual(binding.action_authorization.grants,[{operation:"message.reply",target_kind:"message",object_type:"message"}]);
});
test("self delivery is included on a single apply without a separate control or client evidence",async()=>{
 const api=fixture();
 api.data.catalog.push({name:"lark",provider:"lark",version:"1.0.99",actions:[
  {operation:"message.reply",target_kind:"message",object_types:["message"]},
  {binding_evidence:"self_delivery",operation:"message.send_self",target_kind:"object",object_types:["user"]}
 ]});
 const platforms=["one","two"].map(id=>({id,name:id,provider:"lark",url:"https://open.feishu.cn",version:1,validation:{status:"valid",username:id}}));
 api.hook=path=>path.startsWith("/v1/platforms?")?{platforms}:undefined;
 // Existing bindings acquire the default only when explicitly applied.
 api.data.bindings.push({agent_id:"agent-a",membership_id:"member-a",tool:"lark",platform_id:"one"});
 const ui=await launch(api);await open(ui);
 assert.equal(row(ui,2).children.length,5);
 assert.match(row(ui,2).children[4].textContent,/按你的指令.*发送到本人自聊/);
 assert.equal(api.calls.filter(c=>c.path==="/v1/tools/bind").length,0);
 const grants=[{operation:"message.reply",target_kind:"message",object_type:"message"},{operation:"message.send_self",target_kind:"object",object_type:"user"}];
 for (const [index,id] of ["one","two"].entries()) {
  select(ui,2+index).value=id;await select(ui,2+index).emit("change");
  await apply(ui,2+index).emit("click");
  const sent=api.calls.filter(c=>c.path==="/v1/tools/bind").at(-1).body.binding;
  assert.equal(sent.platform_id,id);
  assert.deepEqual(sent.action_authorization.grants,grants);
  assert.equal(sent.capability_evidence,undefined);
  assert.equal(row(ui,2).children.length,5);
 }
});

test("an unavailable self connection can be disabled without exposing historical target IDs",async()=>{
 const api=fixture();
 api.data.catalog.push({name:"lark",provider:"lark",version:"1.0.99",actions:[{binding_evidence:"self_delivery",operation:"message.send_self",target_kind:"object",object_types:["user"]}]});
 api.data.bindings.push({agent_id:"agent-a",membership_id:"member-a",tool:"lark",platform_id:"old",action_authorization:{grants:[{operation:"message.send_self",target_kind:"object",object_type:"user"}]},capability_evidence:[{kind:"self_delivery",account:{id:{value:"ou_old"}},target:{containers:[{id:{value:"oc_old"}}]}}]});
 api.hook=path=>path.startsWith("/v1/platforms?")?{platforms:[{id:"old",provider:"lark",validation:{status:"unverified"}}]}:undefined;
 const ui=await launch(api);await open(ui);
 assert.equal(row(ui,2).children.length,5);
 assert.equal(apply(ui,2).disabled,true);
 assert.match(select(ui,2).options.at(-1).textContent,/待验证或不可用/);
 assert.doesNotMatch(row(ui,2).children[4].textContent,/oc_old|ou_old/);
 select(ui,2).value="";await select(ui,2).emit("change");
 assert.equal(apply(ui,2).disabled,false);
 await apply(ui,2).emit("click");
 assert.equal(api.calls.filter(c=>c.path==="/v1/tools/bind").at(-1).body.enabled,false);
});
