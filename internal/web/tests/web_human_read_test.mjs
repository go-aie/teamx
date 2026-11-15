import { test } from "node:test";
import assert from "node:assert/strict";
import { createHumanRead } from "../assets/features/human-read.mjs";

const settle = async () => { for (let i=0;i<20;i++) await Promise.resolve(); };
function fixture(options = {}) {
  let now=0, id=0;
  const timers=new Map(), calls=[], notices=[];
  const state={info:{room_id:"room",room_instance:"instance",service_epoch:"epoch"},operator:"owner",installationID:"install",lastSeq:250,clearedThrough:0};
  const saved=new Set(); let revision=0, opened=0;
  const response=()=>({version:2,...state.info,operator_id:state.operator,revision,snapshot_seq:state.lastSeq,cleared_through:state.clearedThrough,
    opened_through:Math.max(opened,state.clearedThrough),mention_seen_seqs:[...saved].filter(seq=>seq>state.clearedThrough).sort((a,b)=>a-b),
    unseen_mention_seqs:[],unread_count:0,mention_count:0});
  const api={hook:null};
  const env={document:{hidden:false},window:{},performance:{now:()=>now},
    setTimeout:(fn,delay)=>{const n=++id;timers.set(n,{fn,at:now+delay});return n;},clearTimeout:n=>timers.delete(n),
    request:async(path,body)=>{
      calls.push({path,body:structuredClone(body)});
      const result=api.hook?.(path,body);if(result!==undefined)return result;
      if(path.endsWith("confirm")){for(const seq of body.mention_seen_seqs || [])saved.add(seq);opened=Math.max(opened,body.opened_through || 0);revision++;}
      const value=response();
      if(body.include_new_messages) value.new_message_seqs=Array.from({length:value.snapshot_seq-value.opened_through},(_,i)=>value.opened_through+i+1);
      return value;
    }};
  let changes=0,refreshes=0;
  const read=createHumanRead({env,...options,runtime:{state,notice:(_,text)=>notices.push(text),refresh:()=>{refreshes++;}},changed:()=>{changes++;}});
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
  assert.deepEqual(batches.map(c=>c.body.mention_seen_seqs.length),[100,100,50]);
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
  assert.deepEqual(f.calls[1],f.calls[0]);assert.deepEqual(f.calls[2].body.mention_seen_seqs,[21]);
  assert.equal(f.calls[2].body.snapshot_seq,250);assert.deepEqual(f.saved,new Set([21,100]));
});

test("remote sets merge monotonically and older revisions, message snapshots and clears cannot undo them",async()=>{
  const f=fixture();let result={...f.response(),revision:3,mention_seen_seqs:[100]};
  f.api.hook=()=>structuredClone(result);f.read.poll();await settle();
  f.read.observe(21);
  result={...result,revision:2,mention_seen_seqs:[]};f.read.poll();await settle();assert.equal(f.read.seen(100),true);assert.equal(f.read.seen(21),true);
  result={...result,revision:4,snapshot_seq:200,mention_seen_seqs:[22]};f.read.poll();await settle();assert.equal(f.read.seen(22),false);
  f.state.clearedThrough=50;f.read.prune();
  result={...result,snapshot_seq:250,cleared_through:0,mention_seen_seqs:[101]};f.read.poll();await settle();assert.equal(f.read.seen(101),false);
  result={...result,cleared_through:50,opened_through:50};f.read.poll();await settle();assert.equal(f.read.seen(101),true);assert.equal(f.read.seen(100),true);
  await f.advance(1000);assert.equal(f.calls.some(c=>c.path.endsWith("confirm")),false,"a cleared pending message never confirms a newer arrival");
});

for(const field of ["room_id","room_instance","service_epoch","operator","installationID"]) {
  test(`changing ${field} drops pending batches and ignores an old response, including on return to the old identity`,async()=>{
    const f=fixture();let release;
    f.api.hook=path=>path.endsWith("state") ? new Promise(resolve=>{release=resolve;}) : undefined;
    f.read.poll();await settle();const old=f.response();f.read.observe(21);f.read.openThrough(200);
    const holder=field in f.state ? f.state : f.state.info, before=holder[field];holder[field]+="-new";f.read.reset();
    holder[field]=before;f.read.reset();
    release({...old,revision:1,mention_seen_seqs:[100]});await settle();await f.advance(1000);
    assert.equal(f.read.seen(100),false);assert.equal(f.read.seen(21),false);assert.equal(f.read.openedThrough(),0);assert.equal(f.calls.length,1);
  });
}

test("a confirmation timeout keeps pending progress and does not install a late receipt",async()=>{
  const f=fixture();let release;
  f.api.hook=()=>new Promise(resolve=>{release=resolve;});f.read.observe(21);await f.advance(5300);
  assert.match(f.notices.at(-1),/暂未同步/);assert.equal(f.read.seen(21),true);
  release({...f.response(),revision:1,mention_seen_seqs:[21,100]});await settle();
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
  const f=fixture();f.api.hook=()=>({...f.response(),revision:-1,mention_seen_seqs:[100]});
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

test("opening and mention observations share a batch but keep pending and confirmed progress independent",async()=>{
  const f=fixture();
  assert.equal(f.read.openThrough(200),true);assert.equal(f.read.openThrough(190),false);
  assert.equal(f.read.openThrough(251),false);
  f.read.observe(21);f.read.observe(99);
  assert.equal(f.read.openedThrough(),200);assert.equal(f.read.confirmedOpenedThrough(),0);
  assert.equal(f.read.seen(100),false,"opening cannot confirm a mention");
  await f.advance(300);
  assert.deepEqual(f.calls[0].body,{...f.state.info,caller:{kind:"human",id:"owner"},
    snapshot_seq:250,opened_through:200,mention_seen_seqs:[21,99]});
  assert.equal(f.read.confirmedOpenedThrough(),200);assert.equal(f.read.confirmed(21),true);
  assert.equal(f.read.seen(100),false);
  await f.advance(10000);assert.equal(f.calls.length,1);
});

test("opening only sends its fixed K even after a newer message refresh, and duplicate positions do not send",async()=>{
  const f=fixture();f.read.openThrough(250);f.state.lastSeq=251;
  await f.advance(300);
  assert.equal(f.calls[0].body.snapshot_seq,250);assert.equal(f.calls[0].body.opened_through,250);
  assert.equal("mention_seen_seqs" in f.calls[0].body,false);
  assert.equal(f.read.confirmedOpenedThrough(),250);assert.equal(f.read.openThrough(250),false);
  f.read.observe(251);await f.advance(300);
  assert.equal("opened_through" in f.calls[1].body,false);assert.equal(f.read.confirmedOpenedThrough(),250);
});

test("a lost mixed receipt retries the same increment before sending newer opening and mentions",async()=>{
  const f=fixture();let release;
  f.api.hook=()=>new Promise((_,reject)=>{release=reject;});
  f.read.openThrough(200);f.read.observe(21);await f.advance(300);
  f.read.openThrough(240);f.read.observe(99);
  release(Error("network"));await settle();f.api.hook=null;
  await f.advance(1000);await f.advance(300);
  assert.deepEqual(f.calls[1],f.calls[0]);
  assert.equal(f.calls[2].body.opened_through,240);assert.deepEqual(f.calls[2].body.mention_seen_seqs,[99]);
  assert.equal(f.read.confirmedOpenedThrough(),240);assert.deepEqual(f.saved,new Set([21,99]));
});

test("a remote opening settles pending opening without settling a pending mention",async()=>{
  const f=fixture();f.read.openThrough(200);f.read.observe(21);
  f.api.hook=path=>path.endsWith("state") ? {...f.response(),revision:2,opened_through:220} : undefined;
  f.read.poll();await settle();
  assert.equal(f.read.confirmedOpenedThrough(),220);assert.equal(f.read.confirmed(21),false);assert.equal(f.read.seen(21),true);
  f.api.hook=()=>({...f.response(),revision:3,opened_through:220,mention_seen_seqs:[21]});await f.advance(300);
  assert.equal("opened_through" in f.calls[1].body,false);assert.deepEqual(f.calls[1].body.mention_seen_seqs,[21]);
  assert.equal(f.read.confirmed(21),true);
  // A higher revision still cannot move an opening position backwards.
  f.api.hook=()=>({...f.response(),revision:4,opened_through:200});f.read.poll();await settle();
  assert.equal(f.read.confirmedOpenedThrough(),220);
  assert.equal(f.read.summary().revision,3);
});

test("entry metadata keeps K when messages have already advanced, without replacing a newer shared summary",async()=>{
  const f=fixture();let release;
  f.api.hook=()=>new Promise(resolve=>{release=resolve;});
  const task=f.read.entryState();await settle();
  assert.equal(f.calls[0].body.include_new_messages,true);
  const result={...f.response(),new_message_seqs:[10,70,95,250],unseen_mention_seqs:[10,70,95],mention_count:3,unread_count:4};
  f.state.lastSeq=251;
  release(result);assert.deepEqual(await task,result);
  assert.equal(f.read.summary(),undefined,"entry K must not regress the global summary behind message refresh");
  f.api.hook=null;f.read.openThrough(result.snapshot_seq);await f.advance(300);
  assert.equal(f.calls[1].body.opened_through,250);assert.equal(f.calls[1].body.snapshot_seq,250);
  f.read.poll();await settle();assert.equal("include_new_messages" in f.calls[2].body,false);
  assert.equal("new_message_seqs" in f.read.summary(),false,"temporary entry sequences belong to the conversation");
});

test("resetting the same context cancels entry and write requests and drops both pending increments",async()=>{
  const f=fixture();const releases=[];
  f.api.hook=()=>new Promise(resolve=>{releases.push(resolve);});
  const entry=f.read.entryState();f.read.openThrough(200);f.read.observe(21);await f.advance(300);
  f.read.reset({force:true});assert.equal(await entry,null);
  for(const release of releases)release({...f.response(),revision:2,opened_through:200,mention_seen_seqs:[21],new_message_seqs:[250]});
  await settle();await f.advance(10000);
  assert.equal(f.read.openedThrough(),0);assert.equal(f.read.seen(21),false);assert.equal(f.changes,0);assert.equal(f.calls.length,2);
});

test("entry metadata rejects a cleared boundary or malformed sequence list and can retry independently",async()=>{
  const f=fixture();let release;
  f.api.hook=()=>new Promise(resolve=>{release=resolve;});
  const entry=f.read.entryState();await settle();const old=f.response();
  f.state.clearedThrough=100;
  release({...old,new_message_seqs:[10,250]});assert.equal(await entry,null);
  f.api.hook=()=>({...f.response(),new_message_seqs:[101,101]});
  assert.equal(await f.read.entryState(),null);assert.match(f.notices.at(-1),/暂未同步/);
  f.api.hook=null;await f.advance(1000);
  const valid=await f.read.entryState();assert.equal(valid.opened_through,100);assert.equal(valid.new_message_seqs[0],101);
  assert.equal(f.notices.at(-1),"");
});

test("opening stays pending through timeout and a state query can verify its lost receipt",async()=>{
  const f=fixture();let release;
  f.api.hook=()=>new Promise(resolve=>{release=resolve;});
  f.read.openThrough(200);await f.advance(5300);
  assert.equal(f.read.openedThrough(),200);assert.equal(f.read.confirmedOpenedThrough(),0);assert.match(f.notices.at(-1),/暂未同步/);
  release({...f.response(),revision:1,opened_through:200});await settle();
  assert.equal(f.read.confirmedOpenedThrough(),0,"a timeout discards late data");
  f.api.hook=()=>({...f.response(),revision:1,opened_through:200});f.read.poll();await settle();
  assert.equal(f.read.confirmedOpenedThrough(),200);assert.equal(f.notices.at(-1),"");
  await f.advance(30000);assert.equal(f.calls.filter(c=>c.path.endsWith("confirm")).length,1);
});

test("background, suspended and offline contexts retain both pending increments until polling resumes",async()=>{
  for(const pause of [f=>{f.env.document.hidden=true;},f=>{f.state.suspended=true;},f=>{f.env.window.navigator={onLine:false};}]) {
    const f=fixture();pause(f);f.read.openThrough(200);f.read.observe(21);
    await f.advance(1000);assert.equal(f.calls.length,0);assert.equal(await f.read.entryState(),null);
    f.env.document.hidden=false;f.state.suspended=false;f.env.window.navigator={onLine:true};
    f.read.poll();await settle();await f.advance(300);
    const batch=f.calls.find(call=>call.path.endsWith("confirm")).body;
    assert.equal(batch.opened_through,200);assert.deepEqual(batch.mention_seen_seqs,[21]);
    assert.equal(f.read.confirmedOpenedThrough(),200);assert.equal(f.read.confirmed(21),true);
  }
});


test("pending reading writes respect the conversation's busy/read-only gate and resume through polling",async()=>{
  let writable=false;
  const f=fixture({canConfirm:()=>writable});
  f.read.openThrough(120);f.read.observe(100);await f.advance(300);
  assert.equal(f.calls.length,0);
  writable=true;f.read.poll();await f.advance(300);
  const batch=f.calls.find(c=>c.path.endsWith("confirm"));
  assert.equal(batch.body.opened_through,120);assert.deepEqual(batch.body.mention_seen_seqs,[100]);
});
