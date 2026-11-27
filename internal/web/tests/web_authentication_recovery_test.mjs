import {test} from "node:test";
import assert from "node:assert/strict";
import {mountAuthenticationRecovery} from "../assets/authentication-recovery.mjs";
const settle=async()=>{for(let i=0;i<5;i++)await new Promise(r=>setImmediate(r));};
function fixture() {
  let status=503,reloads=0,clock=0;const requests=[],timers=new Map(),navigation=[];
  const location={pathname:`/s/${"a".repeat(32)}/`,reload:()=>reloads++,assign:path=>navigation.push(path)};
  let retry;
  const document={visibilityState:"visible",getElementById:()=>({addEventListener:(event,fn)=>{retry=fn;}}),addEventListener(){},removeEventListener(){}};
  const controller=mountAuthenticationRecovery({document,location,now:()=>clock,setTimer:(fn,delay)=>{const id=Symbol();timers.set(id,{fn,delay});return id;},clearTimer:id=>timers.delete(id),
    fetcher:async(path,options)=>{requests.push({path,options});return {ok:status===200,status,json:async()=>status===200?{operator:"alice",installation_id:"install"}:{code:status===401?"login_required":"authentication_temporarily_unavailable"}};}});
  return {controller,location,requests,navigation,setStatus(value){status=value;},get reloads(){return reloads;},retry:()=>retry(),async tick(){const [id,timer]=timers.entries().next().value;timers.delete(id);clock+=timer.delay;timer.fn();await settle();}};
}
test("failed navigation retries automatically and reloads the same Space after recovery",async()=>{
 const f=fixture();await settle();assert.equal(f.reloads,0);
 await f.tick();assert.equal(f.requests.length,2);assert.equal(f.reloads,0);
 f.setStatus(200);await f.tick();assert.equal(f.reloads,1);assert.deepEqual(f.navigation,[]);
 assert.equal(f.location.pathname,`/s/${"a".repeat(32)}/`);
 assert.ok(f.requests.every(({path,options})=>path==="/v1/access/session"&&!options.method&&options.cache==="no-store"));
 f.controller.stop();
});
test("recovery shares manual checks and requires login only on 401",async()=>{
 const f=fixture();await settle();f.setStatus(401);f.retry();f.retry();await settle();
 assert.equal(f.requests.length,2);assert.deepEqual(f.navigation,["/access"]);assert.equal(f.reloads,0);f.controller.stop();
});
