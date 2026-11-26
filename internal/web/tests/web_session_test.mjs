import { test } from "node:test";
import assert from "node:assert/strict";
import { createSessionController } from "../assets/session.mjs";
const settle = async () => { for (let i=0;i<5;i++) await new Promise(r=>setImmediate(r)); };
function fixture(overrides={}) {
  let clock=0, id=0; const timers=new Map(),listeners=new Map(),saved=new Map(),navigation=[],states=[];
  const metadata={operator:"alice",installation_id:"install",renewal_supported:true,renew_after:new Date(60000).toISOString(),verified_until:new Date(120000).toISOString()};
  const document={visibilityState:"visible",addEventListener:(key,fn)=>listeners.set(key,fn),removeEventListener:key=>listeners.delete(key)};
  const location={pathname:`/s/${"a".repeat(32)}/`,hash:"#platform-settings",assign:path=>navigation.push(path),replace:path=>navigation.push(path)};
  const controller=createSessionController({read:async()=>metadata,renew:async()=>metadata,document,location,
    storage:{getItem:key=>saved.get(key),setItem:(key,value)=>saved.set(key,value),removeItem:key=>saved.delete(key)},
    now:()=>clock,setTimer:(fn,delay)=>{timers.set(++id,{fn,delay});return id;},clearTimer:id=>timers.delete(id),changed:value=>states.push(value),...overrides});
  return {controller,metadata,document,location,saved,navigation,timers,states, async visibility(value){document.visibilityState=value;listeners.get("visibilitychange")?.();await settle();},async tick(){const [id,timer]=timers.entries().next().value;timers.delete(id);clock+=timer.delay;timer.fn();await settle();}};
}
test("session renews without user activity, pauses hidden timers, checks on return", async()=>{
 let renewals=0;const f=fixture({renew:async()=>{renewals++;return {...f.metadata,renew_after:new Date(180000).toISOString()};}});
 f.controller.start();await settle();assert.equal(f.timers.size,1);assert.equal([...f.timers.values()][0].delay,60000);
 await f.tick();assert.equal(renewals,1);
 await f.visibility("hidden");assert.equal(f.timers.size,0);
 await f.visibility("visible");assert.equal(renewals,2);f.controller.stop();assert.equal(f.timers.size,0);
});
test("page shares one renewal and never replays a failed business write", async()=>{
 let release,hits=0;const f=fixture({renew:()=>{hits++;return new Promise(resolve=>{release=resolve;});}});
 f.controller.accept(f.metadata);const first=f.controller.check(),second=f.controller.check();assert.equal(first,second);await settle();assert.equal(hits,1);release(f.metadata);await first;
 f.controller.observe(503,{code:"authentication_temporarily_unavailable"},"/v1/send");
 assert.equal(f.controller.unavailable,true);assert.throws(()=>f.controller.beforeRequest("/v1/send",{}),/暂时无法验证登录/);
 assert.doesNotThrow(()=>f.controller.beforeRequest("/v1/access/logout",{}));assert.equal(hits,1);assert.deepEqual(f.navigation,[]);
 f.controller.accept(f.metadata);assert.doesNotThrow(()=>f.controller.beforeRequest("/v1/send",{}));assert.equal(hits,1);
});
test("temporary authentication failure retains location and retries before enabling writes",async()=>{
 let available=false;const f=fixture({renew:async()=>{if(!available){const e=new Error();e.status=503;throw e;}return f.metadata;}});
 f.controller.start();await settle();await assert.rejects(f.controller.check());assert.equal(f.controller.unavailable,true);assert.deepEqual(f.navigation,[]);
 available=true;await f.tick();assert.equal(f.controller.unavailable,false);assert.deepEqual(f.states,[true,false]);f.controller.stop();
});
test("401 navigates once and restores only the same installation and user",async()=>{
 const f=fixture();f.controller.accept(f.metadata);f.controller.observe(401,{},"/v1/messages");f.controller.observe(401,{},"/v1/messages");
 assert.deepEqual(f.navigation,["/access"]);assert.equal(f.saved.size,1);assert.equal(f.controller.restore(f.metadata),true);
 assert.equal(f.navigation[1],f.location.pathname+f.location.hash);
 const foreign=fixture();foreign.controller.accept(foreign.metadata);foreign.controller.login();assert.equal(foreign.controller.restore({...foreign.metadata,operator:"bob"}),false);
 assert.deepEqual(foreign.navigation,["/access"]);
});
test("recovery rejects external paths and never stores callback queries",()=>{
 const f=fixture();f.location.pathname="//evil.invalid/";f.controller.accept(f.metadata);f.controller.login();assert.equal(f.saved.size,0);
 const injected=fixture();injected.saved.set("teamx.login-recovery",JSON.stringify({installation:"install",operator:"alice",path:"https://evil.invalid/"}));assert.equal(injected.controller.restore(injected.metadata),false);
});
test("personal sessions schedule no active renewal",async()=>{
 const f=fixture({read:async()=>({operator:"alice",installation_id:"install",renewal_supported:false})});f.controller.start();await settle();assert.equal(f.timers.size,0);f.controller.stop();
});
