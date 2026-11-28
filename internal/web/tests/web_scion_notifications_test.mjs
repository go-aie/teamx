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
 assert.equal(ui.el("scion-backfill").hidden,false);assert.equal(ui.el("scion-backfill-locked").hidden,true);
 ui.el("scion-repository").value="Owner/repo";await ui.el("scion-repository").emit("input");ui.el("scion-backfill").value="86400";await ui.el("scion-backfill").emit("change");
 assert.equal(ui.el("scion-save").disabled,false);
 await ui.el("scion-save").emit("click");await settle();
 const call=api.calls.find(c=>c.path==="/v1/platform/notifications/configure");assert.equal(call.body.provider,"scion");assert.equal(call.body.subscription_version,0);assert.equal(call.body.config.platform_id,"scion");assert.deepEqual(call.body.config.target,target());assert.deepEqual(call.body.config.object_types,["issue"]);
 assert.equal(ui.el("scion-repository").disabled,true);assert.equal(ui.el("scion-backfill").disabled,true);assert.equal(ui.el("scion-sync").disabled,false);
 assert.equal(ui.el("scion-backfill").hidden,true);assert.equal(ui.el("scion-backfill-locked").hidden,false);assert.match(ui.el("scion-backfill-value").textContent,/2026/);
 assert.equal(api.calls.some(c=>c.path==="/v1/tools/bind" || c.path.includes("scion/login") || c.path.includes("onboarding/prepare")),false);
 const bindings=structuredClone(api.tools.bindings);await ui.el("close-notification").emit("click");await settle();const toggle=row(ui).children[1].children[1];toggle.checked=false;await toggle.emit("change");await settle();assert.equal(api.scion[0].config.enabled,false);assert.deepEqual(api.tools.bindings,bindings);
});
test("Scion dirty drafts survive refresh and late saves cannot fill another member's editor",async()=>{
 const api=scionFixture();api.tools.bindings.push({agent_id:"b",membership_id:"member-b",tool:"scion",platform_id:"scion"});const ui=await launch(api);await open(ui);ui.el("scion-repository").value="Owner/draft";await ui.el("scion-repository").emit("input");await ui.refresh();assert.equal(ui.el("scion-repository").value,"Owner/draft");
 const hook=api.hook;let finish;api.hook=(path,body)=>path.endsWith("notifications/configure") ? new Promise(resolve=>{finish=()=>resolve(hook(path,body));}) : hook(path,body);
 const pending=ui.el("scion-save").emit("click");await settle();await ui.el("close-notification").emit("click");await settle();await open(ui,"member-b");assert.equal(ui.el("scion-repository").value,"");finish();await pending;await settle();
 assert.equal(ui.el("notification-detail").open,true);assert.equal(ui.el("scion-repository").value,"");assert.match(ui.el("notification-destination").textContent,/b/);assert.equal(api.calls.filter(c=>c.path.endsWith("notifications/configure")).length,1);
});

test("Scion conversation changes refresh the platform switch and visible diagnostics through SSE",async()=>{
 const api=scionFixture(),ui=await launch(api);
 await openMemberInbox(ui,"member-a");await ui.el("agent-tab-platforms").emit("click");await settle();
 const toggle=()=>row(ui).children[1].children[1];
 assert.equal(toggle().checked,false);
 api.scion=[{provider:"scion",channel:"workitems",platform_id:"scion",subscription_id:"sub-issue-a",subscription_version:1,
  config:{enabled:true,platform_id:"scion",target:target(),repository:"Owner/repo",object_types:["issue"],interval_seconds:60,backfill_seconds:0},
  baseline:"2026-11-27T00:00:00Z",scion:{},status:"running"}];
 const notify=kind=>ui.sources.at(-1).emit("invalidate",{topic:"notifications",kind,room_id:"R",room_instance:"instance-R",room_service_epoch:"runtime",agent_id:"a",membership_id:"member-a"});
 notify("summary");await ui.clock.advance(2000);await settle();
 assert.equal(toggle().checked,true);
 await row(ui).children[0].emit("click");await settle();
 assert.equal(ui.el("scion-repository").value,"Owner/repo");
 assert.match(ui.el("scion-status").textContent,/初始化中/);
 await ui.clock.advance(2500);api.calls.length=0;
 api.scion=structuredClone(api.scion);api.scion[0].scion.last_full="2026-11-27T01:00:00Z";
 notify("diagnostics");await ui.clock.advance(2000);await settle();
 assert.equal(api.calls.filter(c=>c.path.startsWith("/v1/platform/notifications?")).length,1);
 assert.match(ui.el("scion-status").textContent,/同步正常/);
 ui.el("scion-interval").value="180";await ui.el("scion-interval").emit("input");
 notify("diagnostics");await ui.clock.advance(2000);await settle();
 assert.equal(ui.el("scion-interval").value,"180");
 assert.equal(api.calls.some(c=>c.path==="/v1/platform/notifications/configure" || c.path==="/v1/tools/bind"),false);
 await ui.el("close-notification").emit("click");await settle();await ui.clock.advance(2500);api.calls.length=0;
 notify("diagnostics");await ui.clock.advance(2000);await settle();
 assert.equal(api.calls.filter(c=>c.path.startsWith("/v1/platform/notifications?")).length,0);
});

test("Scion repository picker retains selection across filtering, larger reads and directory failures",async()=>{
 const api=scionFixture(),hook=api.hook,repositories=[{name:"Owner/repo",created_at_ms:100,description:"Issue tracker"},{name:"Owner/other",created_at_ms:200,description:"Other"}];
 api.hook=(path,body)=>path==="/v1/platform/scion/repositories" ? {repositories:body.query ? repositories.filter(r=>r.name.includes(body.query)) : body.page_token ? repositories : repositories.slice(0,1),truncated:!body.page_token,next_page_token:body.page_token ? "" : "200"} : hook(path,body);
 const ui=await launch(api);await open(ui);
 ui.el("scion-repository-picker").open=true;await ui.el("scion-repository-picker").emit("toggle");await settle();
 assert.equal(ui.el("scion-repository").hidden,true);assert.equal(ui.el("scion-repository-options").children.length,1);assert.match(ui.el("scion-repository-status").textContent,/尚未读全/);
 const call=api.calls.find(c=>c.path==="/v1/platform/scion/repositories");assert.equal(call.body.platform_version,1);assert.deepEqual(call.body.target,target());
 await ui.el("scion-repository-more").emit("click");await settle();assert.equal(ui.el("scion-repository-options").children.length,2);
 const pick=ui.el("scion-repository-options").children[1].children[0];pick.checked=true;await pick.emit("change");assert.equal(ui.el("scion-repository-summary").textContent,"Owner/other");
 ui.el("scion-repository-picker").open=true;ui.el("scion-repository-search").value="missing";await ui.el("scion-repository-search").emit("input");await ui.clock.advance(250);
 assert.equal(ui.el("scion-repository-options").children.length,0);assert.equal(ui.el("scion-repository").value,"Owner/other");
 assert.equal(api.calls.filter(c=>c.path==="/v1/platform/scion/repositories").at(-1).body.page_token,"200");
 api.hook=(path,body)=>{if(path==="/v1/platform/scion/repositories")throw Error("unavailable");return hook(path,body);};
 await ui.el("scion-repository-refresh").emit("click");await settle();assert.match(ui.el("scion-repository-status").textContent,/读取失败/);assert.equal(ui.el("scion-repository").value,"Owner/other");
 await ui.el("scion-save").emit("click");await settle();assert.equal(ui.el("scion-repository-picker").hidden,true);assert.equal(ui.el("scion-repository").hidden,false);
});

test("Scion repository candidates cannot cross searches, members or connection versions",async()=>{
 const api=scionFixture(),hook=api.hook,pending=[];
 api.hook=(path,body)=>path==="/v1/platform/scion/repositories" ? new Promise(resolve=>pending.push({body,resolve})) : hook(path,body);
 api.tools.bindings.push({agent_id:"b",membership_id:"member-b",tool:"scion",platform_id:"scion"});
 const ui=await launch(api);await open(ui);ui.el("scion-repository-picker").open=true;void ui.el("scion-repository-picker").emit("toggle");await settle();
 ui.el("scion-repository-search").value="new";await ui.el("scion-repository-search").emit("input");await ui.clock.advance(250);
 const page=name=>({repositories:[{name,created_at_ms:100,description:""}],truncated:false});
 pending[1].resolve(page("Owner/new"));await settle();pending[0].resolve(page("Owner/old"));await settle();
 assert.equal(ui.el("scion-repository-options").children[0].children[0].value,"Owner/new");
 const waiting=ui.el("scion-repository-refresh").emit("click");await settle();
 await ui.el("close-notification").emit("click");await open(ui,"member-b");pending[2].resolve(page("Owner/stale"));await waiting;await settle();
 assert.equal(ui.el("scion-repository-options").children.length,0);assert.equal(ui.el("scion-repository").value,"");
 ui.el("scion-repository-picker").open=true;void ui.el("scion-repository-picker").emit("toggle");await settle();
 api.platforms.find(p=>p.id==="scion").version++;await ui.refresh();pending[3].resolve(page("Owner/old-version"));await settle();
 assert.equal(ui.el("scion-repository-options").children.length,0);
});
