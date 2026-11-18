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
const field=ui=>ui.el("notification-attention").children[0];
const radios=ui=>field(ui).children[1].children[0].children.map(choice=>choice.children[0]);
const selected=ui=>radios(ui).find(input=>input.checked)?.value;
const label=ui=>field(ui).children[1].children[1].textContent;
const row=(ui,id="gl")=>ui.el("member-notification-platforms").children.find(r=>r.dataset.platformId===id);
const summary=(ui,id="gl")=>row(ui,id).children[0].children[1].children[0].children.find(c=>c.className==="member-platform-attention")?.textContent || "";
const feedback=ui=>ui.document.getElementById("notification-attention-status");
async function openAttention(ui,provider="gitlab",id="gl"){await openNotification(ui,provider,id,true);}
async function choose(ui,value){const input=radios(ui).find(input=>input.value===value);input.checked=true;await input.emit("change");}

test("three attention stars hot-save independently and show two-character levels without a success notice",async()=>{
 const api=attentionFixture(),ui=await launch(api);await openMemberInbox(ui,"member-a");
 assert.equal(row(ui).children[1].children.some(c=>c.className==="member-input-attention"),false);
 assert.equal(summary(ui),"");await openAttention(ui);
 assert.equal(selected(ui),"normal");assert.equal(label(ui),"普通");
 assert.deepEqual(radios(ui).map(input=>input.type),["radio","radio","radio"]);
 for(const [value,text,count] of [["high","重点",3],["low","较少",1],["normal","普通",2]]){
  await choose(ui,value);
  assert.equal(selected(ui),value);assert.equal(label(ui),text);
  assert.equal(radios(ui).filter(input=>input.checked).length,1);
  assert.equal(field(ui).children[1].children[0].children.filter(choice=>choice.dataset.filled==="true").length,count);
  assert.equal(api.preferences.get("member-a-gl").attention,value);assert.equal(feedback(ui).hidden,true);
 }
 assert.equal(api.calls.some(c=>c.path.endsWith("notifications/configure")||c.path.endsWith("member/run")),false);
 assert.equal(ui.el("notification-attention").parentElement,ui.el("gitlab-attention-slot"));
 await openAttention(ui);assert.equal(selected(ui),"normal");
});

test("selecting the current star level does not clear it or save again",async()=>{
 const api=attentionFixture(),ui=await launch(api);await openMemberInbox(ui,"member-a");await openAttention(ui);
 await choose(ui,"normal");assert.equal(selected(ui),"normal");
 assert.equal(api.calls.some(c=>c.path==="/v1/member/input-attention"&&c.body.attention),false);
});

test("failed attention saves restore the observed stars and expose a retryable error",async()=>{
 const api=attentionFixture(),ui=await launch(api);await openMemberInbox(ui,"member-a");await openAttention(ui);api.conflict=true;
 await choose(ui,"low");
 assert.equal(selected(ui),"normal");assert.equal(label(ui),"普通");assert.match(ui.el("notification-attention").children.at(-1).textContent,/变化/);
 api.conflict=false;await choose(ui,"low");assert.equal(selected(ui),"low");assert.equal(label(ui),"较少");
});

test("attention save blocks every star and a late reply cannot overwrite another member",async()=>{
 const api=attentionFixture(),ui=await launch(api);await openMemberInbox(ui,"member-a");await openAttention(ui);let resolve;
 const prior=api.hook;api.hook=(path,body)=>path==="/v1/member/input-attention"&&body.attention?new Promise(r=>resolve=()=>r({available:true,sources:[{platform_id:"gl",attention:"high",version:1,source_version:"source-gl"}]})):prior(path,body);
 const saving=choose(ui,"high");await settle();assert.equal(radios(ui).every(input=>input.disabled),true);
 assert.equal(label(ui),"重点");assert.equal(feedback(ui).textContent,"正在保存…");
 await choose(ui,"low");
 await ui.el("close-notification").emit("click");await ui.el("close-activity").emit("click");await openMemberInbox(ui,"member-b");
 await openAttention(ui);resolve();await saving;
 assert.equal(selected(ui),"normal");assert.equal(feedback(ui).hidden,true);
 assert.equal(api.calls.filter(c=>c.path==="/v1/member/input-attention"&&c.body.attention).length,1);
});

test("both platform details keep attention below reception scope and feedback belongs to its source",async()=>{
 const api=attentionFixture(),ui=await launch(api);await openMemberInbox(ui,"member-a");await openAttention(ui);
 await choose(ui,"low");assert.equal(summary(ui),"较少");
 await ui.el("close-notification").emit("click");await openAttention(ui,"lark","lark");
 assert.equal(ui.el("notification-attention").parentElement,ui.el("lark-attention-slot"));
 assert.equal(selected(ui),"normal");assert.equal(feedback(ui).hidden,true);
 assert.match(ui.document.getElementById("notification-attention-help").textContent,/自动保存/);
 assert.match(radios(ui)[0].getAttribute("aria-label"),/较少关注/);
});

test("a failed attention write and failed readback preserve the prior stars",async()=>{
 const api=attentionFixture(),ui=await launch(api);await openMemberInbox(ui,"member-a");await openAttention(ui);api.unavailable=true;
 await choose(ui,"high");assert.equal(selected(ui),"normal");assert.match(ui.el("notification-attention").children.at(-1).textContent,/保存失败/);
 assert.equal(summary(ui),"");
});
