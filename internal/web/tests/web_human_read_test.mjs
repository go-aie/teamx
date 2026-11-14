import { test } from "node:test";
import assert from "node:assert/strict";
import { createHumanRead } from "../assets/features/human-read.mjs";

const settle = async () => { for (let i=0;i<20;i++) await Promise.resolve(); };
function fixture() {
  let now=0, id=0;
  const timers=new Map(), calls=[], notices=[];
  const state={info:{room_id:"room",room_instance:"instance",service_epoch:"epoch"},operator:"owner",installationID:"install",lastSeq:250,clearedThrough:0};
  const saved=new Set(); let revision=0;
  const response=()=>({...state.info,operator_id:state.operator,revision,snapshot_seq:state.lastSeq,cleared_through:state.clearedThrough,seen_seqs:[...saved]});
  const api={hook:null};
  const env={document:{hidden:false},window:{},performance:{now:()=>now},
    setTimeout:(fn,delay)=>{const n=++id;timers.set(n,{fn,at:now+delay});return n;},clearTimeout:n=>timers.delete(n),
    request:async(path,body)=>{
      calls.push({path,body:structuredClone(body)});
      const result=api.hook?.(path,body);if(result!==undefined)return result;
      if(path.endsWith("confirm")){for(const seq of body.seen_seqs)saved.add(seq);revision++;}
      return response();
    }};
  let changes=0,refreshes=0;
  const read=createHumanRead({env,runtime:{state,notice:(_,text)=>notices.push(text),refresh:()=>{refreshes++;}},changed:()=>{changes++;}});
  read.reset();
  return {read,state,env,api,saved,calls,notices,response,get changes(){return changes;},get refreshes(){return refreshes;},
    async advance(ms){const end=now+ms;for(;;){const next=[...timers].filter(([,t])=>t.at<=end).sort((a,b)=>a[1].at-b[1].at)[0];if(!next)break;now=next[1].at;timers.delete(next[0]);next[1].fn();await settle();}now=end;await settle();}};
}

test("viewed sequences batch in groups of at most 100, deduplicate and retain their observed snapshot",async()=>{
  const f=fixture();
  for(let seq=1;seq<=250;seq++){assert.equal(f.read.observe(seq),true);assert.equal(f.read.observe(seq),false);}
  f.state.lastSeq=300;
  await f.advance(299);assert.equal(f.calls.length,0);await f.advance(601);
  const batches=f.calls.filter(c=>c.path.endsWith("confirm"));
  assert.deepEqual(batches.map(c=>c.body.seen_seqs.length),[100,100,50]);
  assert.ok(batches.every(c=>c.body.snapshot_seq===250));assert.equal(f.saved.size,250);
  await f.advance(10000);assert.equal(f.calls.length,3);assert.equal(f.refreshes,3);
});

test("one confirmation stays in flight while new observations queue; a lost response retries the exact increment",async()=>{
  const f=fixture();let release;
  f.api.hook=path=>path.endsWith("confirm") ? new Promise((_,reject)=>{release=reject;}) : undefined;
  f.read.observe(100);await f.advance(300);
  f.read.observe(21);await f.advance(300);assert.equal(f.calls.length,1);
  f.state.lastSeq=500;release(new Error("network"));await settle();
  f.api.hook=null;await f.advance(1000);await f.advance(300);
  assert.deepEqual(f.calls[1],f.calls[0]);assert.deepEqual(f.calls[2].body.seen_seqs,[21]);
  assert.equal(f.calls[2].body.snapshot_seq,250);assert.deepEqual(f.saved,new Set([21,100]));
});

test("remote sets merge monotonically and older revisions, message snapshots and clears cannot undo them",async()=>{
  const f=fixture();let result={...f.response(),revision:3,seen_seqs:[100]};
  f.api.hook=()=>structuredClone(result);f.read.poll();await settle();
  f.read.observe(21);
  result={...result,revision:2,seen_seqs:[]};f.read.poll();await settle();assert.equal(f.read.seen(100),true);assert.equal(f.read.seen(21),true);
  result={...result,revision:4,snapshot_seq:200,seen_seqs:[22]};f.read.poll();await settle();assert.equal(f.read.seen(22),false);
  f.state.clearedThrough=50;f.read.prune();
  result={...result,snapshot_seq:250,cleared_through:0,seen_seqs:[101]};f.read.poll();await settle();assert.equal(f.read.seen(101),false);
  result={...result,cleared_through:50};f.read.poll();await settle();assert.equal(f.read.seen(101),true);assert.equal(f.read.seen(100),true);
  await f.advance(1000);assert.equal(f.calls.some(c=>c.path.endsWith("confirm")),false,"a cleared pending message never confirms a newer arrival");
});

for(const field of ["room_id","room_instance","service_epoch","operator","installationID"]) {
  test(`changing ${field} drops pending batches and ignores an old response, including on return to the old identity`,async()=>{
    const f=fixture();let release;
    f.api.hook=path=>path.endsWith("state") ? new Promise(resolve=>{release=resolve;}) : undefined;
    f.read.poll();await settle();const old=f.response();f.read.observe(21);
    const holder=field in f.state ? f.state : f.state.info, before=holder[field];holder[field]+="-new";f.read.reset();
    holder[field]=before;f.read.reset();
    release({...old,revision:1,seen_seqs:[100]});await settle();await f.advance(1000);
    assert.equal(f.read.seen(100),false);assert.equal(f.read.seen(21),false);assert.equal(f.calls.length,1);
  });
}

test("a confirmation timeout keeps pending progress and does not install a late receipt",async()=>{
  const f=fixture();let release;
  f.api.hook=()=>new Promise(resolve=>{release=resolve;});f.read.observe(21);await f.advance(5300);
  assert.match(f.notices.at(-1),/暂未同步/);assert.equal(f.read.seen(21),true);
  release({...f.response(),revision:1,seen_seqs:[21,100]});await settle();
  assert.equal(f.read.seen(100),false);assert.equal(f.changes,0);
  f.api.hook=null;await f.advance(1000);assert.deepEqual(f.calls[1],f.calls[0]);assert.equal(f.notices.at(-1),"");
});

test("unavailable state pauses automatic confirmation and keeps a visible failure after successful queries",async()=>{
  const f=fixture();f.api.hook=()=>Promise.reject(Object.assign(Error("broken"),{code:"human_read_unavailable"}));
  f.read.poll();await settle();f.read.observe(21);await f.advance(1000);
  f.api.hook=null;f.read.poll();await settle();await f.advance(30000);
  assert.equal(f.calls.some(c=>c.path.endsWith("confirm")),false);assert.equal(f.read.seen(21),true);assert.match(f.notices.at(-1),/确认已暂停/);
});

test("malformed reading responses show a reading-only failure without installing their sequences",async()=>{
  const f=fixture();f.api.hook=()=>({...f.response(),revision:-1,seen_seqs:[100]});
  f.read.poll();await settle();assert.equal(f.read.seen(100),false);assert.match(f.notices.at(-1),/暂未同步/);
  f.api.hook=null;await f.advance(1000);f.read.poll();await settle();assert.equal(f.notices.at(-1),"");
});

test("a state query verifies a lost confirmation receipt and clears the unsynced notice without another write",async()=>{
  const f=fixture();f.api.hook=path=>{
    if(path.endsWith("confirm")){f.saved.add(21);throw Error("lost after commit");}
  };
  f.read.observe(21);await f.advance(300);assert.match(f.notices.at(-1),/暂未同步/);
  f.read.poll();await settle();assert.equal(f.notices.at(-1),"");
  await f.advance(30000);assert.equal(f.calls.filter(c=>c.path.endsWith("confirm")).length,1);
});
