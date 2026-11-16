import { test } from "node:test";
import assert from "node:assert/strict";
import { launch, settle, openMemberInbox, openNotification } from "./web_app_harness.mjs";
import { fixture } from "./web_member_platform_fixture.mjs";
import { createUpdateConnection } from "../assets/updates.mjs";

const count = (api, path) => api.calls.filter(c => c.path.split("?")[0] === path).length;
const notify = (ui, topic, extra = {}) => ui.sources.at(-1).emit("invalidate", {topic, ...extra});
const messagePage = seq => ({messages:[{seq,text:`message ${seq}`,author:{kind:"agent",id:"a"},created_at:"2026-11-16T00:00:00Z"}],snapshot_seq:seq,cleared_through:0,has_more:false});

test("active page stays on one SSE with no display reads for sixty idle seconds", async () => {
  const api = fixture();
  api.hook = (path, body) => path === "/v1/platforms/interactions/list" ? {interactions:[]} : path === "/v1/human-read/state" ? {version:2,room_id:body.room_id,room_instance:body.room_instance,operator_id:body.caller.id,
    revision:0,snapshot_seq:0,cleared_through:0,opened_through:0,mention_seen_seqs:[],unseen_mention_seqs:[],unread_count:0,mention_count:0,...(body.include_new_messages?{new_message_seqs:[]}: {})} : undefined;
  const ui = await launch(api);
  await ui.draft("untouched draft");
  const before = api.calls.length;
  for (let i=0;i<4;i++) { await ui.clock.advance(15000); ui.sources[0].emit("heartbeat"); await settle(); }
  assert.equal(api.calls.length, before, JSON.stringify(api.calls.slice(before)));
  assert.equal(ui.sources.length, 1);
  assert.equal(ui.el("message-input").value, "untouched draft");
  assert.equal(ui.el("send").disabled, false);
});

test("hello reconciles after registration and messages do not pull slow configuration or activity", async () => {
  const api = fixture(), ui = await launch(api);
  assert.ok(count(api,"/v1/ui") >= 2, "bootstrap is followed by registered HTTP reconciliation");
  await openMemberInbox(ui,"member-a"); await ui.el("agent-tab-activity").emit("click"); await settle();
  const waiting = new Promise(() => {});
  api.hook = path => /\/v1\/(models|platforms)(\?|$)/.test(path) || path === "/v1/member/activity" ? waiting
    : path === "/v1/read" ? messagePage(1) : undefined;
  api.calls.length = 0;
  for (const topic of ["models","platforms","activity","messages"]) notify(ui,topic);
  await ui.clock.advance(1500);
  assert.ok(ui.el("messages").children.some(el => el.id === "message-1"));
  assert.equal(count(api,"/v1/read"),1);
  assert.equal(count(api,"/v1/workspace"),0);
  assert.equal(ui.sources.length,1);
});

test("continuous SSE invalidations retain the message minimum interval and stop after catchup", async () => {
  const api=fixture(), ui=await launch(api), times=[];
  api.hook=path=>{if(path==="/v1/read")times.push(ui.readEnvironment.performance.now());};
  for(let i=0;i<1000;i++){notify(ui,"messages");await ui.clock.advance(10);}
  await ui.clock.advance(500);
  for(let i=1;i<times.length;i++)assert.ok(times[i]-times[i-1]>=500);
  assert.ok(times.length<=22);
  const reads=times.length;
  for(let i=0;i<4;i++){ui.sources[0].emit("heartbeat");await ui.clock.advance(15000);}
  assert.equal(times.length,reads);
});

test("panel and membership changes keep the same stream; foreign member and runtime events are ignored", async () => {
  const api=fixture(), ui=await launch(api);
  await openMemberInbox(ui,"member-a"); await ui.el("agent-tab-activity").emit("click");await settle();
  api.calls.length=0;
  notify(ui,"activity",{room_id:"R",room_instance:"instance-R",room_service_epoch:"old",agent_id:"a",membership_id:"member-a"});
  notify(ui,"activity",{room_id:"R",room_instance:"instance-R",room_service_epoch:"runtime",agent_id:"b",membership_id:"member-b"});
  await ui.clock.advance(2000);assert.equal(count(api,"/v1/member/activity"),0);
  await openMemberInbox(ui,"member-b");await ui.el("close-activity").emit("click");
  assert.equal(ui.sources.length,1);
});

test("resync and native reconnect fence late responses without replaying an uncertain send", async () => {
  const api=fixture(),ui=await launch(api);
  api.hook=path=>path==="/v1/send"?Promise.reject(Error("receipt lost")):undefined;
  await ui.draft("once");await ui.el("compose").emit("submit");
  let release;
  api.hook=path=>path==="/v1/read"?new Promise(resolve=>{release=resolve;}):undefined;
  notify(ui,"messages");await ui.clock.advance(500);assert.ok(release);
  const source=ui.sources[0];source.readyState=0;source.onerror();await settle();
  assert.equal(ui.el("send").disabled,true);
  api.hook=null;source.emit("hello",{protocol:1,service_epoch:"manager",connection_id:"reconnected"});
  release(messagePage(99));await settle();
  assert.equal(ui.el("messages").children.some(el=>el.id==="message-99"),false);
  source.emit("resync",{connection_id:"reconnected"});await settle();
  assert.equal(count(api,"/v1/send"),1);
  assert.equal(ui.sources.length,1,"native reconnect owns the same EventSource");
});

test("hidden and offline close the stream and fence reads; foreground recovers with one replacement",async()=>{
  const api=fixture(),ui=await launch(api);await ui.draft("keep");
  ui.document.hidden=true;await ui.document.emit("visibilitychange");assert.equal(ui.sources[0].closed,true);
  const before=api.calls.length;await ui.clock.advance(60000);assert.equal(api.calls.length,before);
  ui.document.hidden=false;await ui.document.emit("visibilitychange");await settle();
  assert.equal(ui.sources.length,2);assert.equal(ui.sources.filter(s=>!s.closed).length,1);
  ui.window.navigator={onLine:false};await ui.window.emit("offline");assert.equal(ui.sources.at(-1).closed,true);
  ui.window.navigator.onLine=true;await ui.window.emit("online");await settle();
  assert.equal(ui.sources.filter(s=>!s.closed).length,1);assert.equal(ui.el("message-input").value,"keep");
  assert.equal(count(api,"/v1/send"),0);
});

test("heartbeat timeout uses one bounded replacement; repeated errors leave native reconnect in charge",async()=>{
  const api=fixture(),ui=await launch(api);const first=ui.sources[0];
  first.readyState=0;first.onerror();first.onerror();await ui.clock.advance(44999);
  assert.equal(ui.sources.length,1);
  await ui.clock.advance(1);assert.equal(first.closed,true);
  await ui.clock.advance(1200);assert.equal(ui.sources.length,2);assert.equal(ui.sources.filter(s=>!s.closed).length,1);
});

test("controller accepts only the owned connection, closes before replacement and retries constructor failures with backoff",async()=>{
  let now=0,serial=0;const timers=new Map(),sources=[],applied=[],states=[];let attempts=0,reconciles=0;
  const env={performance:{now:()=>now},random:()=>0.5,setTimeout:(fn,ms)=>{timers.set(++serial,{fn,at:now+ms});return serial;},clearTimeout:id=>timers.delete(id),
    EventSource:class{constructor(url){if(++attempts<3)throw Error("unavailable");this.url=url;this.listeners={};sources.push(this);}addEventListener(t,fn){this.listeners[t]=fn;}close(){this.closed=true;}}};
  const step=async ms=>{now+=ms;for(const [id,t]of [...timers])if(t.at<=now){timers.delete(id);t.fn();}await settle();};
  let info={service_epoch:"manager"};
  const controller=createUpdateConnection({env,identity:()=>info,reconcile:async()=>{reconciles++;},invalidate:e=>applied.push(e),status:v=>states.push(v)});
  controller.start();assert.equal(attempts,1);await step(999);assert.equal(attempts,1);await step(1);assert.equal(attempts,2);await step(1999);assert.equal(attempts,2);await step(1);assert.equal(attempts,3);
  const first=sources[0],emit=(s,event,data)=>s.listeners[event]({data:JSON.stringify(data)});
  emit(first,"hello",{protocol:1,connection_id:"current",service_epoch:"manager"});await settle();
  emit(first,"invalidate",{connection_id:"foreign",topic:"messages"});assert.equal(applied.length,0);
  info={service_epoch:"new"};controller.ensure();assert.equal(first.closed,true);
  emit(first,"invalidate",{connection_id:"current",topic:"messages"});assert.equal(applied.length,0);
  controller.stop();assert.ok(sources.every(s=>s.closed));assert.equal(timers.size,0);assert.ok(reconciles>=3);
});

test("inbox invalidation during its initial fixed-snapshot page survives until status can be read",async()=>{
 const api=fixture(),ui=await launch(api);let release,latest=50;
 await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();
 const original=api.request.bind(api);
 api.hook=(path,body)=>{
  if(path==="/v1/member/inbox/status")return {latest_seq:latest,expired_through:0,retained_entries:latest,bytes:100,limits:{bytes:1000}};
  if(path==="/v1/member/inbox")return new Promise(resolve=>{release=async()=>{api.hook=null;const page=await original(path,body);resolve(page);};});
 };
 const opening=ui.el("agent-tab-inbox").emit("click");await settle();assert.ok(release);
 latest=53;notify(ui,"inbox",{room_id:"R",room_instance:"instance-R",room_service_epoch:"runtime",agent_id:"a",membership_id:"member-a"});
 await settle();const before=count(api,"/v1/member/inbox/status");
 await release();await opening;await settle();
 assert.equal(count(api,"/v1/member/inbox/status"),before+1);
 assert.equal(ui.el("inbox-page").textContent,"第 1 页");
 assert.equal(ui.sources.length,1);
});

test("catalog runtime replacement closes the old stream and ignores its later invalidations",async()=>{
 const api=fixture(),ui=await launch(api),old=ui.sources[0];await ui.draft("runtime draft");
 api.runtimeEpoch="replacement";ui.invalidate("catalog");await ui.clock.advance(1500);await settle();
 assert.equal(old.closed,true);assert.equal(ui.sources.filter(s=>!s.closed).length,1);
 assert.match(ui.sources.at(-1).url,/room_service_epoch=replacement/);
 const before=count(api,"/v1/read");old.emit("invalidate",{topic:"messages"});await settle();assert.equal(count(api,"/v1/read"),before);
 assert.equal(ui.el("message-input").value,"runtime draft");
});

test("hello never releases the send gate before failed chat reconciliation succeeds",async()=>{
 const api=fixture(),ui=await launch(api);await ui.draft("wait for verification");
 let unavailable=true;
 api.hook=path=>path==="/v1/read"&&unavailable?Promise.reject(Error("read unavailable")):undefined;
 ui.sources.at(-1).emit("hello",{protocol:1,service_epoch:"manager"});await settle();
 assert.equal(ui.el("send").disabled,true);
 unavailable=false;await ui.clock.advance(2000);
 assert.equal(ui.el("send").disabled,false);
 assert.equal(count(api,"/v1/send"),0);
});


test("active diagnostics do not refresh chat or summary sidebar; summary updates and inspector reentry stay live", async () => {
  const api=fixture(); api.hook=path=>path==="/v1/platforms/interactions/list"?{interactions:[]}:undefined;
  const ui=await launch(api);
  await openMemberInbox(ui,"member-a");
  await ui.el("close-activity").emit("click");
  await ui.clock.advance(2500);api.calls.length=0;
  for(let i=0;i<120;i++) { notify(ui,"notifications",{kind:"diagnostics"}); await ui.clock.advance(500); if(i%20===0)ui.sources.at(-1).emit("heartbeat"); }
  assert.equal(api.calls.length,0,JSON.stringify(api.calls));
  // Legacy notifications remain summary changes; subsequent diagnostics may
  // never override their relevance to the visible source list.
  notify(ui,"notifications");notify(ui,"notifications",{kind:"diagnostics"});await settle();
  assert.equal(count(api,"/v1/platform/notifications"),1);
  await openNotification(ui,"lark","lark","member-a");
  const times=[];api.calls.length=0;
  api.hook=path=>{if(path.startsWith("/v1/platform/notifications?"))times.push(ui.readEnvironment.performance.now());};
  for(let i=0;i<100;i++) {notify(ui,"notifications",{kind:"diagnostics"});await ui.clock.advance(100);}
  for(let i=1;i<times.length;i++)assert.ok(times[i]-times[i-1]>=2000);
  assert.ok(times.length>=4 && times.length<=6);
  for(const path of ["/v1/rooms","/v1/member/list","/v1/member/inbox/status"])assert.equal(count(api,path),0,path);
  await ui.el("close-notification").emit("click");
  await ui.clock.advance(2500);api.calls.length=0;
  for(let i=0;i<10;i++){notify(ui,"notifications",{kind:"diagnostics"});await ui.clock.advance(500);}
  assert.equal(count(api,"/v1/platform/notifications"),0);
  await openNotification(ui,"lark","lark","member-a");await ui.document.emit("toggle");await ui.clock.advance(2000);
  assert.ok(count(api,"/v1/platform/notifications")>=1,"opening reads the retained diagnostic dirty state");
});

test("a summary arriving during a diagnostic read survives closing the inspector and later diagnostics",async()=>{
 const api=fixture(),ui=await launch(api);await openMemberInbox(ui,"member-a");await openNotification(ui,"lark","lark","member-a");await ui.clock.advance(2500);
 let release;api.calls.length=0;
 api.hook=path=>path.startsWith("/v1/platform/notifications?")?new Promise(resolve=>{release=resolve;}):undefined;
 notify(ui,"notifications",{kind:"diagnostics"});await settle();assert.ok(release);
 notify(ui,"notifications",{kind:"summary"});notify(ui,"notifications",{kind:"diagnostics"});
 await ui.el("close-notification").emit("click");
 api.hook=null;release({subscriptions:[],gitlab_platforms:[]});await settle();await ui.clock.advance(2000);
 assert.equal(count(api,"/v1/platform/notifications"),2,"summary needs one further serial read on the sidebar");
});
