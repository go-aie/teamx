import { test } from "node:test";
import assert from "node:assert/strict";
import { launch, settle } from "./web_app_harness.mjs";

function fixture() {
  const members = ["a", "b"].map(id => ({agent_id:`agent-${id}`,membership_id:`member-${id}`,status:"idle",runner:{profile:"p"},unread_count:0,read_through:0}));
  const data = { revision:0, bindings:[],disabled:[], catalog:[{name:"gitlab",provider:"gitlab",version:"glab 1.127.0"},{name:"local",version:"fixture"}] };
  const connection = {id:"connection",name:"Personal",provider:"gitlab",url:"https://gitlab.invalid",version:3,validation:{status:"valid",username:"me"}};
  const api = {members,data,calls:[],hook:null,async request(path,body) {
    api.calls.push({path,body:structuredClone(body)});
    const overridden=api.hook?.(path,body);if(overridden!==undefined)return overridden;
    if(path==="/v1/ui")return {managed:true};
    if(path==="/v1/rooms")return {service_epoch:"manager",operator:"tester",rooms:[{room_id:"A",room_instance:"instance-A",name:"A",status:"ready",runtime_status:"ready",service:{room_id:"A",room_instance:"instance-A",service_epoch:"active-A"}}],teams:[]};
    if(path.startsWith("/v1/workspace?"))return {workspace:{root:"workspaces"},workspace_version:1,members:[],credential_status:"missing"};
    if(path.startsWith("/v1/platform/notifications?"))return {subscriptions:[]};
    if(path.startsWith("/v1/platforms?"))return {platforms:[connection]};
    if(path.startsWith("/v1/tools/bindings?")){
      if(!api.initialized){
        const {platforms}=await api.request("/v1/platforms?fixture");
        for(const p of platforms){const d=data.catalog.find(d=>d.provider===p.provider);if(d && !data.bindings.some(b=>b.platform_id===p.id))data.disabled.push({agent_id:"agent-a",membership_id:"member-a",tool:d.name,platform_id:p.id});}
        api.initialized=true;
      }
      return structuredClone(data);
    }
    if(path==="/v1/tools/bind") {
      assert.equal(body.revision,data.revision);assert.equal(body.service_epoch,"manager");
      const b=body.binding;assert.ok(!("operations" in b));assert.ok(!("scope" in b));
      data.bindings=data.bindings.filter(old=>old.tool!==b.tool||old.membership_id!==b.membership_id||old.platform_id!==b.platform_id);
      data.disabled=data.disabled.filter(old=>old.tool!==b.tool||old.membership_id!==b.membership_id||old.platform_id!==b.platform_id);
      if(!body.enabled)data.disabled.push(structuredClone(b));
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
}
const row=(ui,id="connection")=>ui.el("member-notification-platforms").children.find(r=>r.dataset.platformId===id);
const toggle=(ui,id)=>row(ui,id).children[5].children[0];
const feedback=(ui,id)=>row(ui,id).children[3].textContent;
const binds=api=>api.calls.filter(c=>c.path==="/v1/tools/bind");
async function change(ui,id,enabled){const input=toggle(ui,id);input.checked=enabled;await input.emit("change");}

test("same-type platforms keep independent immediate switches and unavailable intent",async()=>{
 const api=fixture(),platforms=["one","two"].map(id=>({id,name:id,provider:"gitlab",version:1,validation:{status:"valid",username:id}}));
 api.hook=path=>path.startsWith("/v1/platforms?")?{platforms}:undefined;
 api.data.bindings=platforms.map(p=>({agent_id:"agent-a",membership_id:"member-a",tool:"gitlab",platform_id:p.id}));
 api.data.selections=[{agent_id:"agent-a",membership_id:"member-a",tool_type:"gitlab",platform_id:"two",unavailable_reason:"executable_unavailable"}];
 const ui=await launch(api);await open(ui);
 assert.equal(toggle(ui,"one").checked,true);assert.match(feedback(ui,"two"),/程序缺失.*保留启用设置/);
 await change(ui,"one",false);
 assert.equal(binds(api)[0].body.platform_version,0);assert.equal(binds(api)[0].body.binding.platform_id,"one");
 assert.equal(toggle(ui,"one").checked,false);assert.equal(toggle(ui,"two").checked,true);assert.match(feedback(ui,"two"),/程序缺失/);
});
test("Scion has no notification switch and only verified accounts can enable tools",async()=>{
 const api=fixture();api.data.catalog.push({name:"scion",provider:"scion"});
 const platforms=[{id:"scion",name:"Scion demo",provider:"scion",version:2,validation:{status:"valid",username:"alice"}},{id:"bad",name:"待验证",provider:"scion",validation:{status:"unverified"}}];
 api.hook=path=>path.startsWith("/v1/platforms?")?{platforms}:undefined;
 const ui=await launch(api);await open(ui);
 assert.equal(row(ui,"scion").children[1].hidden,true);assert.equal(toggle(ui,"bad").disabled,true);assert.equal(binds(api).length,0);
 await change(ui,"scion",true);assert.equal(binds(api)[0].body.binding.tool,"scion");assert.equal(binds(api)[0].body.platform_version,2);
 await row(ui,"scion").children[2].emit("click");assert.match(ui.el("notification-title").textContent,/Scion demo 设置/);assert.match(ui.el("notification-notice").textContent,/无通知能力/);
});
test("member platforms omit the empty other-tools section and routine reload controls",async()=>{
 const api=fixture(),ui=await launch(api);await open(ui);
 assert.equal(ui.el("agent-tab-tools"),undefined);assert.equal(ui.el("tools-member-view"),undefined);
 assert.equal(ui.el("member-other-tools"),undefined);assert.equal(ui.el("cli-member-refresh"),undefined);assert.equal(ui.el("member-notification-list-reload").hidden,true);
 assert.equal(toggle(ui).checked,false);assert.equal(binds(api).length,0);
 await change(ui,"connection",true);assert.equal(binds(api)[0].body.binding.platform_id,"connection");assert.equal(binds(api)[0].body.platform_version,3);
 assert.match(feedback(ui),/已启用/);await ui.refresh();assert.match(feedback(ui),/已启用/);
 await change(ui,"connection",false);assert.equal(toggle(ui).checked,false);assert.equal(row(ui).dataset.platformId,"connection");
 assert.equal(api.calls.some(c=>c.path.startsWith("/v1/platform/tools")),false);
});
test("pending tool writes block duplicates even after switching away and back",async()=>{
 const api=fixture(),ui=await launch(api);await open(ui);let release;
 api.hook=path=>path==="/v1/tools/bind" ? new Promise(r=>release=r) : undefined;
 const saving=change(ui,"connection",true);await settle();assert.equal(toggle(ui).checked,true);assert.equal(toggle(ui).disabled,true);
 await toggle(ui).emit("change");assert.equal(binds(api).length,1);
 ui.el("activity-member").value="member-b";await ui.el("activity-member").emit("change");await settle();
 ui.el("activity-member").value="member-a";await ui.el("activity-member").emit("change");await settle();assert.equal(toggle(ui).disabled,true);
 api.data.bindings.push({agent_id:"agent-a",membership_id:"member-a",tool:"gitlab",platform_id:"connection"});api.data.disabled=[];api.data.revision=1;release(structuredClone(api.data));await saving;
 assert.equal(toggle(ui).checked,true);assert.equal(toggle(ui).disabled,false);assert.equal(binds(api).length,1);
});
test("unknown tool results read back committed intent and never replay writes",async()=>{
 const api=fixture(),ui=await launch(api);await open(ui);
 api.hook=(path,body)=>{if(path==="/v1/tools/bind"){api.data.bindings.push(body.binding);api.data.disabled=[];api.data.revision++;return Promise.reject(Error("lost"));}};
 await change(ui,"connection",true);assert.equal(toggle(ui).checked,true);assert.match(feedback(ui),/未确认.*不会自动重发/);assert.equal(binds(api).length,1);
 assert.equal(ui.el("member-notification-list-reload").hidden,true);assert.equal(toggle(ui).checked,true);assert.equal(binds(api).length,1);
});
test("rejected tool writes roll back confirmed intent",async()=>{
 const api=fixture(),ui=await launch(api);await open(ui);
 for(const code of ["conflict","identity","request","capacity","busy"]){
  api.hook=path=>path==="/v1/tools/bind"?Promise.reject(Object.assign(Error(code),{code})):undefined;
  await change(ui,"connection",true);assert.equal(toggle(ui).checked,false);assert.match(feedback(ui),code==="busy" ? /未确认/ : /保存失败/);
 }
 assert.equal(binds(api).length,5);
});
test("late retry reads cannot replace a different member's platforms",async()=>{
 const api=fixture(),ui=await launch(api);await open(ui);let release;
 api.hook=path=>path.startsWith("/v1/tools/bindings?")?Promise.reject(Error("offline")):undefined;
 await change(ui,"connection",true);assert.equal(ui.el("member-notification-list-reload").hidden,false);
 api.hook=path=>path.startsWith("/v1/tools/bindings?")?new Promise(r=>release=r):undefined;
 const reading=ui.el("member-notification-list-reload").emit("click");await settle();api.hook=null;
 api.data.disabled.push({agent_id:"agent-b",membership_id:"member-b",tool:"gitlab",platform_id:"connection"});
 ui.el("activity-member").value="member-b";await ui.el("activity-member").emit("change");await settle();
 const rows=ui.el("member-notification-platforms").children;release({...api.data,catalog:[]});await reading;
 assert.equal(ui.el("member-notification-platforms").children,rows);assert.equal(rows.length,1);
 await change(ui,"connection",true);assert.equal(binds(api).at(-1).body.binding.membership_id,"member-b");
});
for(const self of [false,true])test(`Lark switch retains accepted capabilities, excludes unavailable actions and sends no client evidence (self=${self})`,async()=>{
 const api=fixture();api.data.catalog.push({name:"lark",provider:"lark",actions:[
  {operation:"message.reply",target_kind:"message",object_types:["message"]},
  {operation:"message.send",target_kind:"object",object_types:["user","chat"],disabled_reason:"unavailable: private detail"},
  ...(self ? [{binding_evidence:"self_delivery",operation:"message.send_self",target_kind:"object",object_types:["user"]}] : [])
 ]});
 const platforms=["one","two"].map(id=>({id,name:id,provider:"lark",url:"https://open.feishu.cn",version:1,validation:{status:"valid",username:id}}));
 api.hook=path=>path.startsWith("/v1/platforms?")?{platforms}:undefined;
 api.data.bindings.push({agent_id:"agent-a",membership_id:"member-a",tool:"lark",platform_id:"one"});
 const ui=await launch(api);await open(ui);assert.equal(binds(api).length,0);assert.equal(api.data.bindings[0].action_authorization,undefined);
 await change(ui,"one",false);await change(ui,"one",true);await change(ui,"two",true);
 const sent=binds(api).at(-1).body.binding;assert.equal(sent.platform_id,"two");assert.equal(sent.capability_evidence,undefined);
 assert.deepEqual(sent.action_authorization.grants,[{operation:"message.reply",target_kind:"message",object_type:"message"},...(self ? [{operation:"message.send_self",target_kind:"object",object_type:"user"}] : [])]);
 await row(ui,"one").children[2].emit("click");assert.doesNotMatch(ui.el("member-platform-tool-help").textContent,/private detail|验收|oc_old/);
 if(self)assert.match(ui.el("member-platform-tool-help").textContent,/发送到本人自聊/);
 await change(ui,"one",false);assert.equal(toggle(ui,"two").checked,true);
});
test("unavailable accounts can be disabled without exposing historical target IDs",async()=>{
 const api=fixture();api.data.catalog.push({name:"lark",provider:"lark"});
 api.data.bindings.push({agent_id:"agent-a",membership_id:"member-a",tool:"lark",platform_id:"old",capability_evidence:[{target:{containers:[{id:{value:"oc_old"}}]}}]});
 api.hook=path=>path.startsWith("/v1/platforms?")?{platforms:[{id:"old",name:"旧平台",provider:"lark",validation:{status:"unverified"}}]}:undefined;
 const ui=await launch(api);await open(ui);assert.equal(toggle(ui,"old").disabled,false);await change(ui,"old",false);
 assert.equal(toggle(ui,"old").checked,false);assert.equal(toggle(ui,"old").disabled,true);assert.equal(binds(api)[0].body.enabled,false);
});

test("failed automatic tool readback exposes only the existing failure retry",async()=>{
 const api=fixture(),ui=await launch(api);await open(ui);
 assert.equal(ui.el("member-notification-list-reload").hidden,true);
 api.hook=path=>path.startsWith("/v1/tools/bindings?")?Promise.reject(Error("offline")):undefined;
 await change(ui,"connection",true);assert.ok(row(ui));assert.equal(toggle(ui).disabled,true);
 assert.equal(ui.el("member-notification-list-reload").hidden,false);assert.match(ui.el("member-notification-list-error").textContent,/暂不可读/);
 api.hook=null;await ui.el("member-notification-list-reload").emit("click");assert.equal(toggle(ui).disabled,false);assert.equal(toggle(ui).checked,true);
 assert.equal(ui.el("member-notification-list-reload").hidden,true);assert.equal(binds(api).length,1);
});
