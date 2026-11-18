import {test} from "node:test";
import assert from "node:assert/strict";
import {launch,openMemberInbox,openNotification,settle} from "./web_app_harness.mjs";
import {fixture} from "./web_member_platform_fixture.mjs";

function attentionFixture(){
 const api=fixture();api.preferences=new Map();api.conflict=false;api.unavailable=false;
 const prior=api.hook;
 api.hook=(path,body)=>{
  if(path!=="/v1/member/input-attention")return prior?.(path,body);
  if(api.unavailable)throw Error("保存失败，设置未更改");
  const id=body.membership_id,key=id+"-gl";
  const old=api.preferences.get(key)||{attention:"normal",version:0};
  if(body.attention){if(api.conflict||body.expected_version!==old.version)throw Object.assign(Error("关注设置已变化，请重试"),{code:"conflict"});api.preferences.set(key,{attention:body.attention,version:old.version+1});}
  const current=api.preferences.get(key)||old;
  return {available:true,sources:api.platforms.map(p=>({platform_id:p.id,source_version:`source-${p.id}`,attention:p.id==="gl"?current.attention:"normal",version:p.id==="gl"?current.version:0}))};
 };
 return api;
}
function select(ui,id="gl"){
 const row=ui.el("member-notification-platforms").children.find(r=>r.dataset.platformId===id);
 return row.children[1].children.find(c=>c.className==="member-input-attention")?.children[0];
}
test("member attention hot-save is independent of notification switches and appears in detail",async()=>{
 const api=attentionFixture(),ui=await launch(api);await openMemberInbox(ui,"member-a");
 let control=select(ui);assert.ok(control);assert.equal(control.value,"normal");control.value="high";await control.emit("change");
 assert.equal(select(ui).value,"high");assert.equal(api.preferences.get("member-a-gl").attention,"high");
 assert.equal(api.calls.some(c=>c.path.endsWith("notifications/configure")||c.path.endsWith("member/run")),false);
 await openNotification(ui,"gitlab","gl",true);
 assert.equal(ui.el("notification-attention").hidden,false);assert.equal(ui.el("notification-attention").children[0].children[0].value,"high");
});
test("failed attention saves retain the observed value and expose a retryable error",async()=>{
 const api=attentionFixture(),ui=await launch(api);await openMemberInbox(ui,"member-a");api.conflict=true;
 const control=select(ui);control.value="low";await control.emit("change");
 assert.equal(select(ui).value,"normal");assert.match(ui.el("member-notification-platforms").children[0].children[2].textContent,/变化/);
 api.conflict=false;const retry=select(ui);retry.value="low";await retry.emit("change");assert.equal(select(ui).value,"low");
});
test("attention save blocks duplicates and a late reply cannot overwrite another member",async()=>{
 const api=attentionFixture(),ui=await launch(api);await openMemberInbox(ui,"member-a");let resolve;
 const prior=api.hook;api.hook=(path,body)=>path==="/v1/member/input-attention"&&body.attention?new Promise(r=>resolve=()=>r({available:true,sources:[{platform_id:"gl",attention:"high",version:1,source_version:"source-gl"}]})):prior(path,body);
 const control=select(ui);control.value="high";const saving=control.emit("change");await settle();assert.equal(control.disabled,true);
 await ui.el("close-activity").emit("click");await openMemberInbox(ui,"member-b");resolve();await saving;
 assert.equal(select(ui).value,"normal");assert.equal(api.calls.filter(c=>c.path==="/v1/member/input-attention"&&c.body.attention).length,1);
});
