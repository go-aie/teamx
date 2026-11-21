const {toggleConversationDetails}=require('./conversation_details.cjs');
// Paired real-browser measurements. All writes target the isolated Go fixture.
const {chromium}=require('playwright');
const assert=require('node:assert/strict');
const fs=require('node:fs/promises');
const os=require('node:os');
const path=require('node:path');
const {createHash}=require('node:crypto');
const summary=a=>{a=[...a].sort((a,b)=>a-b);const at=p=>a.length?a[Math.ceil(a.length*p)-1]:null;return{samples:a.length,p50:at(.5),p95:at(.95),max:a.at(-1)??null};};
(async()=>{
 const [endpoint,dataset,mode,ref]=process.argv.slice(2),browser=await chromium.launch({headless:true});
 try{
  const context=await browser.newContext({viewport:{width:1440,height:1000}}),page=await context.newPage(),errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.addInitScript(()=>{
   window.perfSeen={};window.perfActivity={};window.perfMembers={};window.perfLong=[];window.perfSources=[];
   new PerformanceObserver(list=>{for(const e of list.getEntries())window.perfLong.push({at:performance.timeOrigin+e.startTime,ms:e.duration});}).observe({type:'longtask',buffered:true});
   const Native=EventSource;
   window.EventSource=class extends Native{constructor(url){super(url);this.closed=false;window.perfSources.push(this);}close(){this.closed=true;super.close();}};
   addEventListener('DOMContentLoaded',()=>{
    new MutationObserver(records=>{
     for(const record of records)for(const node of record.addedNodes){
      if(node.nodeType!==1)continue;
      for(const el of [node,...node.querySelectorAll('[id^="message-"]')])if(/^message-\d+$/.test(el.id))window.perfSeen[el.id]??=Date.now();
     }
     for(const el of document.querySelectorAll('.member-name'))if(/^member-perf-/.test(el.textContent))window.perfMembers[el.textContent]??=Date.now();
     const text=document.querySelector('#activity-timeline .activity-text')?.textContent||'';
     const marker=text.match(/^perf-\d+/)?.[0];if(marker)window.perfActivity[marker]??=Date.now();
    }).observe(document.body,{childList:true,subtree:true,characterData:true});
   });
  });
  await page.goto(endpoint);await page.waitForFunction(()=>!document.querySelector('#message-input').disabled);
  let bootstrapBusy=0;
  async function catalogRead(){for(let attempt=0;attempt<30;attempt++){const response=await fetch(endpoint+'/v1/rooms');if(response.status===409){bootstrapBusy++;await page.waitForTimeout(100);continue;}assert.equal(response.status,200,await response.clone().text());return response.json();}throw Error('catalog bootstrap remained busy');}
  const catalog=await catalogRead(),info=catalog.rooms.find(r=>r.service).service,caller={kind:'human',id:catalog.operator};
  const rpc=async(p,body)=>{const response=await fetch(endpoint+p,{method:'POST',headers:{'Content-Type':'application/json',Origin:endpoint},body:JSON.stringify({...info,caller,...body})});assert.equal(response.status,200,await response.clone().text());return response.json();};
  const member=(await rpc('/v1/member/list',{})).members[0];
  await rpc('/v1/member/pause',{agent_id:member.agent_id,membership_id:member.membership_id});
  await page.waitForTimeout(3000);
  if(await page.locator('#activity-dialog').evaluate(el=>el.open))await page.locator('#close-activity').click();
  const application=await(await fetch(endpoint+'/ui/application.mjs')).text();
  const report={binary_sha256:process.env.TEAMX_WEB_UPDATES_BINARY_SHA256,go_version:process.env.TEAMX_WEB_UPDATES_GO_VERSION,application_sha256:createHash('sha256').update(application).digest('hex'),recorded_at:new Date().toISOString(),cpu:os.cpus()[0].model,platform:process.platform,arch:process.arch,node:process.version,browser:browser.version(),dataset,mode,bootstrap_busy_responses:bootstrapBusy,polling_ref:ref,window_ms:12000,
   fixture:{rooms:dataset==='loaded'?101:1,active_messages:dataset==='loaded'?10000:0,inactive_messages_per_room:dataset==='loaded'?100:0,activity_bytes:dataset==='loaded'?720*1024:0,viewport:[1440,1000]},scenarios:[]};
  let expectedName=member.agent_name;
  let active=null;const pending=new Set(),requests=new Map();
  page.on('request',request=>{
   const name=new URL(request.url()).pathname;
   if(!active||!name.startsWith('/v1/')||name==='/v1/updates')return;
   const body=request.method()==='POST'?request.postDataJSON():null;
   const entry={path:name,at:Date.now(),history:name==='/v1/read'&&body?.snapshot_seq!==undefined};active.requests.push(entry);requests.set(request,entry);
  });
  async function finish(request,failed=false){
   const entry=requests.get(request);if(!entry)return;requests.delete(request);
   const response=failed?null:await request.response();entry.status=response?.status();entry.bytes=response?await response.body().then(b=>b.length).catch(()=>0):0;entry.elapsed=Date.now()-entry.at;
  }
  for(const event of ['requestfinished','requestfailed'])page.on(event,r=>{const promise=finish(r,event==='requestfailed');pending.add(promise);promise.finally(()=>pending.delete(promise));});
  async function send(prefix){const receipt=await rpc('/v1/send',{client_message_id:prefix+'-'+Date.now(),text:prefix+' '+Date.now()});const {at}=await(await fetch(endpoint+'/__measure/installed')).json();return{seq:receipt.seq,at};}
  for(const scenario of ['idle','continuous-messages','activity',...(dataset==='loaded'?['combined']:[])]){
   if(scenario==='continuous-messages'&&dataset==='loaded'){
    await rpc('/v1/messages/clear',{snapshot_seq:512});await page.waitForTimeout(2500);
   }
   if(scenario==='activity'){
    if(dataset==='loaded'){const response=await fetch(endpoint+'/__measure/activity?marker=static');assert.equal(response.status,200);}
    await toggleConversationDetails(page);await page.locator('#agent-tab-activity').click();
    if(dataset==='loaded')await page.waitForFunction(()=>document.querySelector('#activity-timeline .activity-text')?.textContent.length>=240*1024);
    await page.waitForTimeout(2500);
   }
   await page.locator('#message-input').fill('负载期间保留草稿');
   const sample={scenario,requests:[],commits:[],activities:[],members:[],injector_name_busy:0},start=Date.now();
   await fetch(endpoint+'/__measure/start');active=sample;
   if(scenario==='combined'){
    // Independent paged HTTP reader shares the service with visible Activity.
    // Separate history DOM/render performance is measured by history-large.
    await page.evaluate(({info,caller})=>{
     window.historyBusy=true;window.historyReads=0;window.historyFailure=null;
     window.historyTask=(async()=>{let after=512;while(window.historyBusy&&window.historyReads<200){const response=await fetch('/v1/read',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({...info,caller,mode:'range',after,snapshot_seq:Math.min(10000,after+50)})});if(!response.ok)throw Error(await response.text());const data=await response.json();if(data.messages.length>50)throw Error('history page exceeded 50');window.historyReads++;after=data.messages.at(-1)?.seq||after;if(after>=10000)after=512;await new Promise(r=>setTimeout(r,25));}})().catch(e=>window.historyFailure=e.message);
    },{info,caller});
   }

   if(scenario==='combined'){
    const first=await send('quiet-first');await page.locator('#message-'+first.seq).waitFor();sample.quiet_first_ms=await page.evaluate(seq=>window.perfSeen['message-'+seq],first.seq)-first.at;
   }
   const end=start+report.window_ms;let serial=0;
   while(Date.now()<end){
    if(scenario==='continuous-messages'||scenario==='combined')sample.commits.push(await send('load-message'));
    if(scenario==='combined'){
     const marker='perf-'+(++serial),{at}=await(await fetch(endpoint+'/__measure/activity?marker='+marker)).json();sample.activities.push({marker,at});
     if(serial%5===1){
      const name='member-perf-'+serial;
      for(let attempt=0;;attempt++){
      const response=await fetch(endpoint+'/v1/member/name',{method:'POST',headers:{'Content-Type':'application/json',Origin:endpoint},body:JSON.stringify({service_epoch:catalog.service_epoch,room_id:info.room_id,room_instance:info.room_instance,caller,agent_id:member.agent_id,membership_id:member.membership_id,expected_name:expectedName,agent_name:name})});
      if(response.status===409){const error=await response.json();assert.equal(error.code,'busy');assert.ok(attempt<30);sample.injector_name_busy++;await page.waitForTimeout(100);continue;}
      assert.equal(response.status,200,await response.text());break;}expectedName=name;
      const installed=await(await fetch(endpoint+'/__measure/installed')).json();sample.members.push({name,at:installed.member_at});
     }
    }
    await page.waitForTimeout(scenario==='idle'||scenario==='activity'?Math.min(1000,end-Date.now()):300);
   }
   active=null;const elapsed=Date.now()-start;
   const backend=await(await fetch(endpoint+'/__measure/end')).json();
   if(scenario==='combined')await page.evaluate(async()=>{window.historyBusy=false;await window.historyTask;if(window.historyFailure)throw Error(window.historyFailure);if(window.historyReads!==200)throw Error('fixed 200-page workload did not finish inside the measurement window: '+window.historyReads);});
   await Promise.all([...pending]);
   // Drain only for application-latency observations; requests below are exactly
   // those started in the 12-second window, including unfinished boundary reads.
   await page.waitForTimeout(2200);
   const seen=await page.evaluate(()=>({messages:window.perfSeen,activity:window.perfActivity,members:window.perfMembers,long:window.perfLong}));
   const counts={},starts={};for(const r of sample.requests){counts[r.path]=(counts[r.path]||0)+1;if(!r.history)(starts[r.path]??=[]).push(r.at);}
   const gaps={};for(const [p,times] of Object.entries(starts))gaps[p]=times.length>1?Math.min(...times.slice(1).map((t,i)=>t-times[i])):null;
   const latency=sample.commits.filter(c=>seen.messages['message-'+c.seq]).map(c=>seen.messages['message-'+c.seq]-c.at);
   const memberLatency=sample.members.filter(c=>seen.members[c.name]).map(c=>seen.members[c.name]-c.at);
   const activityLatency=sample.activities.filter(c=>seen.activity[c.marker]).map(c=>seen.activity[c.marker]-c.at);
   const item={scenario,elapsed_ms:elapsed,injector_name_busy:sample.injector_name_busy,counts,total:sample.requests.length,bytes:sample.requests.reduce((n,r)=>n+(r.bytes||0),0),http_ms:summary(sample.requests.map(r=>r.elapsed)),http_failures:sample.requests.filter(r=>r.status!==200),
    logical_rounds:{messages:sample.requests.filter(r=>r.path==='/v1/read'&&!r.history).length,history_http_pages:sample.requests.filter(r=>r.history).length,catalog:counts['/v1/rooms']||0,activity:counts['/v1/member/activity']||0},minimum_start_gap_ms:gaps,
    message_install_to_dom_ms:{commits:sample.commits.length,...summary(latency)},members_install_to_dom_ms:{installs:sample.members.length,...summary(memberLatency)},activity_install_to_dom_ms:{installs:sample.activities.length,...summary(activityLatency)},quiet_first_ms:sample.quiet_first_ms,
    long_tasks_ms:summary(seen.long.filter(e=>e.at>=start&&e.at<end).map(e=>e.ms)),backend};
   report.scenarios.push(item);
   item.catalog_projection_calls=sample.requests.filter(r=>r.path==='/v1/rooms'&&r.status===200).length;
   item.catalog_projection_rows=item.catalog_projection_calls*report.fixture.rooms;
   if(mode==='sse'){
    assert.equal(item.http_failures.length,0,JSON.stringify(item.http_failures));
    if(scenario==='idle'||scenario==='activity')assert.equal(item.total,0,'unchanging views stop reading');
    if(scenario==='continuous-messages'||scenario==='combined'){
     for(const p of ['/v1/ui','/v1/workspace','/v1/platforms','/v1/platform/notifications','/v1/models','/v1/tools/bindings'])assert.equal(counts[p]||0,0,`unrelated ${p}`);
     assert.ok((gaps['/v1/rooms']??1500)>=1480,JSON.stringify(gaps));
     assert.ok((gaps['/v1/member/activity']??1500)>=1480,JSON.stringify(gaps));
     assert.equal(latency.length,sample.commits.length,'every committed message reaches DOM');
     assert.ok(item.message_install_to_dom_ms.p95<=1000,JSON.stringify(item.message_install_to_dom_ms));
     if(scenario==='combined'){assert.equal(memberLatency.length,sample.members.length);assert.ok(activityLatency.length>=5);}
     if(memberLatency.length)assert.ok(item.members_install_to_dom_ms.p95<=1500,JSON.stringify(item.members_install_to_dom_ms));
     if(activityLatency.length)assert.ok(item.activity_install_to_dom_ms.p95<=2000,JSON.stringify(item.activity_install_to_dom_ms));
    }
   }
  }
  if(mode==='sse'){
   await page.locator('#close-activity').click();
   const peers=[await context.newPage(),await(await browser.newContext()).newPage()];
   for(const peer of peers){await peer.goto(endpoint);await peer.waitForFunction(()=>!document.querySelector('#message-input').disabled);}
   await page.waitForTimeout(3000);await fetch(endpoint+'/__measure/start');
   await page.waitForTimeout(5000);const idle=await(await fetch(endpoint+'/__measure/end')).json();
   assert.deepEqual(idle.http_counts_including_injector,{},'three pages remain idle');
   const message=await send('multi-page');for(const p of [page,...peers])await p.locator('#message-'+message.seq).waitFor();
   assert.equal(await page.locator('#message-input').inputValue(),'负载期间保留草稿');
   assert.equal((await(await fetch(endpoint+'/__measure/connections')).json()).connections,3);
   report.multiple_pages={pages:3,browser_contexts:2,idle_seconds:5,idle_http:idle.http_counts_including_injector,message_received_by_all:true};
   for(const p of peers)await p.close();
  }
  assert.deepEqual(errors,[]);
  if(process.env.TEAMX_WEB_UPDATES_PERF_OUTPUT){await fs.mkdir(process.env.TEAMX_WEB_UPDATES_PERF_OUTPUT,{recursive:true});await fs.writeFile(path.join(process.env.TEAMX_WEB_UPDATES_PERF_OUTPUT,`${dataset}-${mode}.json`),JSON.stringify(report,null,2)+'\n');}
  console.log(JSON.stringify(report));
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
