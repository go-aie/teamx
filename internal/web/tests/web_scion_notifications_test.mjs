import {test} from "node:test";
import assert from "node:assert/strict";
import {launch,settle,openMemberInbox} from "./web_app_harness.mjs";
import {fixture,target} from "./web_member_platform_fixture.mjs";
function scionFixture(){
 const api=fixture(),scion={id:"scion",name:"Scion",provider:"scion",supports_notifications:true,url:"https://scion.invalid",version:1,validation:{status:"valid",username:"alice"}};
 api.platforms.push(scion);api.tools.bindings.push({agent_id:"a",membership_id:"member-a",tool:"scion",platform_id:"scion"});
 api.scion=[];api.hook=(path,body)=>{
  if(path.startsWith("/v1/platform/notifications?"))return {subscriptions:[...api.scion],gitlab_platforms:[...api.records.values()].filter(v=>!v.provider)};
  if(path==="/v1/platform/notifications/configure" && body.provider==="scion"){
   const before=api.scion.find(v=>v.config.target.membership_id===body.config.target.membership_id);
   assert.equal(body.subscription_version,before?.subscription_version || 0);
   const next={provider:"scion",channel:"workitems",platform_id:"scion",subscription_id:before?.subscription_id || "sub-issue-a",subscription_version:body.subscription_version+1,config:structuredClone(body.config),baseline:"2026-11-27T00:00:00Z",scion:{last_full:"2026-11-27T01:00:00Z"},status:"running"};
   api.scion=[...api.scion.filter(v=>v!==before),next];return structuredClone(next);
  }
 };
 return api;
}
const row=ui=>ui.el("member-notification-platforms").children.find(r=>r.dataset.platformId==="scion");
async function open(ui,member="member-a"){await openMemberInbox(ui,member);await ui.el("agent-tab-platforms").emit("click");await settle();await row(ui).children[0].emit("click");await settle();}
test("Scion reuses the member platform row, saves only Issue notifications and locks the initial range",async()=>{
 const api=scionFixture(),ui=await launch(api);await open(ui);
 assert.equal(ui.el("notification-panel-workitems").hidden,false);assert.equal(ui.el("notification-panel-gitlab").hidden,true);assert.equal(ui.el("notification-panel-messages").hidden,true);
 assert.match(ui.el("notification-account").textContent,/alice/);
 ui.el("scion-repository").value="Owner/repo";await ui.el("scion-repository").emit("input");ui.el("scion-backfill").value="86400";await ui.el("scion-backfill").emit("change");
 assert.equal(ui.el("scion-save").disabled,false);
 await ui.el("scion-save").emit("click");await settle();
 const call=api.calls.find(c=>c.path==="/v1/platform/notifications/configure");assert.equal(call.body.provider,"scion");assert.equal(call.body.subscription_version,0);assert.equal(call.body.config.platform_id,"scion");assert.deepEqual(call.body.config.target,target());assert.deepEqual(call.body.config.object_types,["issue"]);
 assert.equal(ui.el("scion-repository").disabled,true);assert.equal(ui.el("scion-backfill").disabled,true);assert.equal(ui.el("scion-sync").disabled,false);
 assert.equal(api.calls.some(c=>c.path==="/v1/tools/bind" || c.path.includes("scion/login") || c.path.includes("onboarding/prepare")),false);
 const bindings=structuredClone(api.tools.bindings);await ui.el("close-notification").emit("click");await settle();const toggle=row(ui).children[1].children[1];toggle.checked=false;await toggle.emit("change");await settle();assert.equal(api.scion[0].config.enabled,false);assert.deepEqual(api.tools.bindings,bindings);
});
test("Scion dirty drafts survive refresh and late saves cannot fill another member's editor",async()=>{
 const api=scionFixture();api.tools.bindings.push({agent_id:"b",membership_id:"member-b",tool:"scion",platform_id:"scion"});const ui=await launch(api);await open(ui);ui.el("scion-repository").value="Owner/draft";await ui.el("scion-repository").emit("input");await ui.refresh();assert.equal(ui.el("scion-repository").value,"Owner/draft");
 const hook=api.hook;let finish;api.hook=(path,body)=>path.endsWith("notifications/configure") ? new Promise(resolve=>{finish=()=>resolve(hook(path,body));}) : hook(path,body);
 const pending=ui.el("scion-save").emit("click");await settle();await ui.el("close-notification").emit("click");await settle();await open(ui,"member-b");assert.equal(ui.el("scion-repository").value,"");finish();await pending;await settle();
 assert.equal(ui.el("notification-detail").open,true);assert.equal(ui.el("scion-repository").value,"");assert.match(ui.el("notification-destination").textContent,/b/);assert.equal(api.calls.filter(c=>c.path.endsWith("notifications/configure")).length,1);
});
