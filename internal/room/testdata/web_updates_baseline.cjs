const {toggleConversationDetails}=require('./conversation_details.cjs');
const {chromium}=require('playwright');
const assert=require('node:assert/strict');
const fs=require('node:fs/promises');
const os=require('node:os');

(async()=>{
 const endpoint=process.argv[2],browser=await chromium.launch({headless:true});
 try {
  const context=await browser.newContext({viewport:{width:1440,height:1000}}),page=await context.newPage(),errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.goto(endpoint);
  await page.locator('#room-list .room-choice[aria-current="true"]').waitFor();
  await page.waitForFunction(()=>!document.querySelector('#message-input').disabled);
  await page.waitForTimeout(2000);
  const catalog=await(await fetch(endpoint+'/v1/rooms')).json(),info=catalog.rooms.find(r=>r.service).service;
  const caller={kind:'human',id:catalog.operator};
	const roster=await(await fetch(endpoint+'/v1/member/list',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({...info,caller})})).json();
	for(const member of roster.members){const paused=await fetch(endpoint+'/v1/member/pause',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({...info,caller,agent_id:member.agent_id,membership_id:member.membership_id})});assert.equal(paused.status,200);}
	if(await page.locator('#activity-dialog').evaluate(el=>el.open))await toggleConversationDetails(page);
	await page.waitForTimeout(2000);
  const output={recorded_at:new Date().toISOString(),platform:process.platform,arch:process.arch,node:process.version,browser:browser.version(),cpu:os.cpus()[0].model,fixture:{rooms:1,members:1,backend:'controlled, paused',viewport:[1440,1000]},window_ms:12000,scenarios:[]};
  let active=null;
  const starts=new Map();
  page.on('request',r=>{if(active && new URL(r.url()).pathname.startsWith('/v1/'))starts.set(r,{at:Date.now(),active});});
  page.on('requestfinished',async r=>{
   const sample=starts.get(r);if(!sample)return;starts.delete(r);
   const response=await r.response(),bytes=await response.body().then(b=>b.length).catch(()=>0);
   sample.active.requests.push({path:new URL(r.url()).pathname,elapsed_ms:Date.now()-sample.at,bytes});
  });
  for(const scenario of ['idle','continuous-messages','activity']){
   if(scenario==='activity'){
    await toggleConversationDetails(page);
    await page.locator('#agent-tab-activity').click();
    await page.waitForTimeout(2000);
   }
   await page.evaluate(()=>{
    window.baselineSeen={};
    window.baselineObserver?.disconnect();
    window.baselineObserver=new MutationObserver(()=>{
     for(const el of document.querySelectorAll('#messages [id^="message-"]'))if(!(el.id in window.baselineSeen))window.baselineSeen[el.id]=Date.now();
    });
    window.baselineObserver.observe(document.querySelector('#messages'),{childList:true,subtree:true});
   });
   const sample={scenario,requests:[],commits:[]};active=sample;
   const end=Date.now()+output.window_ms;
   while(Date.now()<end){
    if(scenario==='continuous-messages'){
     const sent=await fetch(endpoint+'/v1/send',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({...info,caller,client_message_id:'baseline-'+Date.now(),text:'baseline message '+Date.now()})});
     assert.equal(sent.status,200);const receipt=await sent.json();sample.commits.push({seq:receipt.seq,at:Date.now()});
    }
    await page.waitForTimeout(scenario==='continuous-messages'?300:Math.min(1000,end-Date.now()));
   }
   active=null;
   const seen=await page.evaluate(()=>window.baselineSeen);
   const latencies=sample.commits.filter(c=>seen['message-'+c.seq]).map(c=>Math.max(0,seen['message-'+c.seq]-c.at)).sort((a,b)=>a-b);
   const counts={},http=sample.requests.map(r=>r.elapsed_ms).sort((a,b)=>a-b);
   for(const r of sample.requests)counts[r.path]=(counts[r.path]||0)+1;
   const percentile=(a,p)=>a.length?a[Math.min(a.length-1,Math.ceil(a.length*p)-1)]:null;
   output.scenarios.push({scenario,counts,total:sample.requests.length,bytes:sample.requests.reduce((n,r)=>n+r.bytes,0),http_ms:{p50:percentile(http,.5),p95:percentile(http,.95),max:http.at(-1)},message_apply_ms:{samples:latencies.length,commits:sample.commits.length,p50:percentile(latencies,.5),p95:percentile(latencies,.95),max:latencies.at(-1)}});
  }
  assert.deepEqual(errors,[]);
  if(process.env.TEAMX_WEB_UPDATES_BASELINE_OUTPUT)await fs.writeFile(process.env.TEAMX_WEB_UPDATES_BASELINE_OUTPUT,JSON.stringify(output,null,2)+'\n');
  console.log(JSON.stringify(output,null,2));
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
