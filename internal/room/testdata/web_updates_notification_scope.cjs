const {toggleConversationDetails}=require('./conversation_details.cjs');
const {chromium}=require('playwright'),assert=require('node:assert/strict');
const {writeFileSync}=require('node:fs');
(async()=>{
 const mode=process.argv[3],longWindow=mode==='long';
 assert.ok(['functional','long'].includes(mode),'explicit browser scenario required');
 const endpoint=process.argv[2],baseline=process.env.TEAMX_WEB_UPDATES_SCOPE_BASELINE==='1';
 assert.ok(!baseline||longWindow,'polling baseline requires the long-window scenario');
 const browser=await chromium.launch({headless:true}),errors=[];
 try{
  const contexts=await Promise.all([browser.newContext({viewport:{width:1440,height:1000}}),browser.newContext({viewport:{width:390,height:844},isMobile:true,hasTouch:true})]);
  const pages=await Promise.all(contexts.map(c=>c.newPage())),reads=[[],[]],frames=[[],[]];
  async function rpc(page,path,body){const r=await page.request[body?'post':'get'](endpoint+path,{...(body?{data:body}:{}),headers:{Origin:endpoint}});assert.equal(r.status(),200,await r.text());return r.json();}
  for(const [i,p]of pages.entries()){
   p.on('pageerror',e=>errors.push(e.message));
   p.on('request',r=>{const path=new URL(r.url()).pathname;if(path.startsWith('/v1/')&&path!=='/v1/updates')reads[i].push({path,time:Date.now(),bytes:0});});
   p.on('response',async r=>{const path=new URL(r.url()).pathname;if(path.startsWith('/v1/')&&path!=='/v1/updates'){try{const body=await r.body(),entry=reads[i].findLast(x=>x.path===path&&!x.bytes);if(entry){entry.bytes=body.length;entry.status=r.status();}}catch{}}});
   await p.addInitScript(()=>{const Native=EventSource;window.scopeFrames=[];window.EventSource=class extends Native{constructor(url){super(url);for(const type of ['hello','invalidate','heartbeat'])this.addEventListener(type,e=>window.scopeFrames.push({type,data:JSON.parse(e.data)}));}};document.hasFocus=()=>false;});
   await p.goto(endpoint+'/access');
   await rpc(p,'/v1/access/login',{token:process.env.TEAMX_WEB_UPDATES_TEST_TOKEN});
   const selection=await rpc(p,'/__scope/info');
   await p.addInitScript(room=>sessionStorage.setItem('teamx.selected-room',JSON.stringify(room)),selection.current);
   await p.goto(endpoint);
   if(i===1)await p.locator(`#room-list .room-entry[data-room-key='${JSON.stringify([selection.current.room_id,selection.current.room_instance])}'] .room-choice`).click();
   await p.waitForFunction(()=>document.querySelector('#connection').textContent==='服务已连接');
  }
  const info=await rpc(pages[0],'/__scope/info');
  for(const p of pages){
   if(!await p.locator(`#members [data-activity-member="${info.member.membership_id}"]`).isVisible())await toggleConversationDetails(p);
   if(await p.locator('#toggle-member-list').getAttribute('aria-expanded')==='false')await p.locator('#toggle-member-list').click();
   for(let attempt=0;attempt<20;attempt++) {
    await p.waitForFunction(()=>document.querySelector('#connection').textContent==='服务已连接');
    await p.locator(`#members [data-activity-member="${info.member.membership_id}"]`).click();
    await p.waitForTimeout(100);
    if(await p.locator('#activity-dialog').evaluate(el=>el.open))break;
   }
   try { await p.locator(`#member-notification-platforms [data-platform-id="${info.platform}"]`).waitFor({timeout:10000}); }
   catch(e){console.error(JSON.stringify({url:p.url(),reads:reads[pages.indexOf(p)],body:(await p.locator('body').innerText()).slice(-6000)}));throw e;}
  }
  await pages[0].locator('#close-activity').click();
  // One ordinary chat and one member summary stay active. Multiple real empty source queries
  // and persisted metric/watermark updates continue throughout this window.
  await pages[0].waitForTimeout(3000);
  const from=reads.map(a=>a.length),framesFrom=await Promise.all(pages.map(p=>p.evaluate(()=>window.scopeFrames.length))),started=Date.now(),before=await rpc(pages[0],'/__scope/info');
  if(longWindow)await pages[0].waitForTimeout(60000);
  else for(const [i,p]of pages.entries())await p.waitForFunction(from=>window.scopeFrames.slice(from).some(f=>f.type==='invalidate'&&f.data.topic==='notifications'&&f.data.kind==='diagnostics'),framesFrom[i]);
  let after=await rpc(pages[0],'/__scope/info');
  const stepDeadline=Date.now()+5000;
  while(!longWindow&&after.steps<=before.steps&&Date.now()<stepDeadline){await pages[0].waitForTimeout(25);after=await rpc(pages[0],'/__scope/info');}
  const duration=Date.now()-started;
  assert.ok(after.steps>before.steps,'actual source rounds continued');
  if(longWindow){
   assert.ok(after.steps-before.steps>=55,'actual source rounds continued for sixty seconds');
   for(const provider of ['gitlab','lark'])assert.ok(after.calls[provider]-before.calls[provider]>=5,'actual '+provider+' queries continued');
  }
  const idle=reads.map((a,i)=>a.slice(from[i]));
  const unrelated=new Set(['/v1/rooms','/v1/member/list','/v1/member/inbox/status','/v1/member/inbox','/v1/platform/notifications']);
  if(!baseline)for(const rows of idle)assert.deepEqual(rows.filter(r=>unrelated.has(r.path)),[]);
  const idleFrames=await Promise.all(pages.map((p,i)=>p.evaluate(from=>window.scopeFrames.slice(from).filter(f=>f.type==='invalidate').map(f=>f.data),framesFrom[i])));
  if(longWindow&&!baseline)for(const fs of idleFrames)assert.ok(fs.filter(f=>f.topic==='notifications'&&f.kind==='diagnostics').length>=20);
  if(longWindow){
   // Keep the original sixty-second measurement and message probe. Functional
   // transitions run separately; reconstructed polling remains measurement-only.
   const msg=await rpc(pages[0],'/__scope/message?id=baseline-probe&text=baseline%20probe',{yes:true});
   await pages[0].locator('#message-'+msg.seq).waitFor();
   const report={mode,baseline,duration_ms:duration,collector_rounds:after.steps-before.steps,queries:Object.fromEntries(['gitlab','lark'].map(p=>[p,after.calls[p]-before.calls[p]])),idle:idle.map(rows=>({http:rows.length,bytes:rows.reduce((a,r)=>a+r.bytes,0),catalog_requests:rows.filter(r=>r.path==='/v1/rooms').length,catalog_computations:rows.filter(r=>r.path==='/v1/rooms'&&r.status===200).length,paths:Object.fromEntries([...new Set(rows.map(r=>r.path))].map(path=>[path,rows.filter(r=>r.path===path).length]))})),idle_frames:idleFrames.map(fs=>Object.fromEntries([...new Set(fs.map(f=>f.topic+':'+(f.kind||'summary')))].map(k=>[k,fs.filter(f=>f.topic+':'+(f.kind||'summary')===k).length]))),message_to_dom_ms:Date.now()-msg.installed};
   if(process.env.TEAMX_WEB_UPDATES_SCOPE_OUTPUT)writeFileSync(process.env.TEAMX_WEB_UPDATES_SCOPE_OUTPUT,JSON.stringify(report,null,2)+'\n');
   console.log(JSON.stringify(report));return;
  }
  const probe=await rpc(pages[0],'/__scope/message?id=baseline-probe&text=baseline%20probe',{yes:true});
  await pages[0].locator('#message-'+probe.seq).waitFor();const probeLatency=Date.now()-probe.installed;
  async function open(p){if(!await p.locator('#activity-dialog').evaluate(el=>el.open))await p.locator(`#members [data-activity-member="${info.member.membership_id}"]`).click();await p.locator(`#member-notification-platforms [data-platform-id="${info.platform}"] .notification-source`).click();await p.locator('#notification-detail').waitFor();}
  async function close(p){await p.locator('#close-notification').click();}
  // Only the page showing diagnostics reads collection metrics. Exchange the
  // roles and verify the other independent queue/cache behaves identically.
  const details=[];
  for(const index of [0,1]){
   await open(pages[index]);await pages[index].waitForTimeout(2500);
   const start=reads.map(a=>a.length);await pages[index].waitForTimeout(6500);
   const rows=reads.map((a,i)=>a.slice(start[i]));
   if(!baseline){
    assert.ok(rows[index].filter(r=>r.path==='/v1/platform/notifications').length>=2);
    assert.equal(rows[1-index].filter(r=>r.path==='/v1/platform/notifications').length,0);
    for(const rs of rows)assert.equal(rs.filter(r=>['/v1/rooms','/v1/member/list','/v1/member/inbox/status'].includes(r.path)).length,0);
   }
   details.push(rows);await close(pages[index]);
  }
  // A real installed error updates both source summaries. A later diagnostic
  // checkpoint cannot consume either page's summary invalidation.
  const error=await rpc(pages[0],'/__scope/error',{yes:true});
  for(const p of pages)await p.waitForFunction(()=>[...document.querySelectorAll('.notification-source-issue')].some(n=>n.textContent.includes('投递目标暂不可用')));
  const errorLatency=Date.now()-error.installed;
  await rpc(pages[0],'/__scope/recover',{yes:true});
  for(const p of pages)await p.waitForFunction(()=>![...document.querySelectorAll('.notification-source-issue')].some(n=>n.textContent.includes('投递目标暂不可用')));
  // Different Room subscriptions keep private reads separate while the
  // shared catalog still updates on both pages.
  await pages[1].locator('#close-activity').click();await pages[1].waitForTimeout(500);
  if(await pages[1].locator('#close-mobile-members').isVisible())await pages[1].locator('#close-mobile-members').click();
  await pages[1].waitForFunction(()=>document.querySelector('#member-rail').getAttribute('aria-hidden')==='true');
  await pages[1].locator('#mobile-back-to-rooms').click();
  const choose=(p,room)=>p.locator(`#room-list .room-entry[data-room-key='${JSON.stringify([room.room_id,room.room_instance])}'] .room-choice`).click();
  await choose(pages[1],info.other);await pages[1].waitForFunction(()=>document.querySelector('#connection').textContent==='服务已连接');
  const split=await rpc(pages[0],'/__scope/message?id=split-message&text=split%20room%20message',{yes:true});
  await pages[0].locator('#message-'+split.seq).waitFor();
  await pages[1].waitForFunction(()=>document.querySelector('#room-list').textContent.includes('split room message'));
  assert.equal(await pages[1].locator('#messages').textContent().then(s=>s.includes('split room message')),false);
  await pages[1].locator('#mobile-back-to-rooms').click();await choose(pages[1],info.current);
  await pages[1].locator('#message-'+split.seq).waitFor();
  // Independent authenticated devices merge ordinary read progress and
  // different mention confirmations without resetting each page's entry view.
  const operator=(await rpc(pages[0],'/v1/access/session')).operator;
  const mentionA=await rpc(pages[0],'/__scope/message?id=mention-a&text='+encodeURIComponent('@'+operator+' first mention'),{yes:true});
  const mentionB=await rpc(pages[0],'/__scope/message?id=mention-b&text='+encodeURIComponent('@'+operator+' second mention'),{yes:true});
  for(const p of pages)await p.locator('#message-'+mentionB.seq).waitFor();
  const base={...info.current,caller:{kind:'human',id:operator}},reading=await rpc(pages[0],'/v1/human-read/state',base);
  assert.ok(reading.unseen_mention_seqs.includes(mentionA.seq)&&reading.unseen_mention_seqs.includes(mentionB.seq));
  const positions=await Promise.all(pages.map(p=>p.evaluate(()=>{window.scopeDivider=document.querySelector('.new-message-divider');return document.querySelector('#timeline').scrollTop;})));
  await Promise.all(pages.map((p,i)=>rpc(p,'/v1/human-read/confirm',{...base,snapshot_seq:mentionB.seq,opened_through:i?mentionB.seq:split.seq,mention_seen_seqs:[i?mentionB.seq:mentionA.seq]})));
  const merged=await rpc(pages[0],'/v1/human-read/state',base);
  assert.equal(merged.opened_through,mentionB.seq);assert.ok(merged.mention_seen_seqs.includes(mentionA.seq)&&merged.mention_seen_seqs.includes(mentionB.seq));
  await pages[0].waitForTimeout(1500);
  for(const [i,p]of pages.entries())assert.deepEqual(await p.evaluate(()=>({position:document.querySelector('#timeline').scrollTop,same:window.scopeDivider===document.querySelector('.new-message-divider')})),{position:positions[i],same:true});
  // Page-local drafts and recovery: one page is hidden while the other keeps
  // collecting and receives the message; foreground catches up once.
  for(const p of pages){if(await p.locator('#activity-dialog').evaluate(el=>el.open)){await p.locator('#close-activity').click();await p.waitForTimeout(500);}if(await p.locator('#close-mobile-members').isVisible())await p.locator('#close-mobile-members').click();}
  await pages[0].locator('#message-input').fill('desktop draft');await pages[1].locator('#message-input').fill('phone draft');
  await pages[1].evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:true});document.dispatchEvent(new Event('visibilitychange'));});
  const msg=await rpc(pages[0],'/__scope/message?id=scope-message&text=scope%20business%20message',{yes:true});
  await pages[0].locator('#message-'+msg.seq).waitFor();const messageLatency=Date.now()-msg.installed;
  await pages[1].evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:false});document.dispatchEvent(new Event('visibilitychange'));});
  await pages[1].locator('#message-'+msg.seq).waitFor();
  assert.equal(await pages[0].locator('#message-input').inputValue(),'desktop draft');assert.equal(await pages[1].locator('#message-input').inputValue(),'phone draft');
  // Session revocation applies only to one authenticated context.
  await rpc(pages[1],'/v1/access/logout',{yes:true});
  const next=await rpc(pages[0],'/__scope/message?id=scope-survivor&text=surviving%20session',{yes:true});
  await pages[0].locator('#message-'+next.seq).waitFor();
  assert.equal(reads.flat().filter(r=>['/v1/send','/v1/models/change','/v1/platform/lark/login','/v1/platform/gitlab/login'].includes(r.path)).length,0);
  for(let i=0;i<pages.length;i++)frames[i]=await pages[i].evaluate(()=>window.scopeFrames).catch(()=>[]);
  assert.deepEqual(errors,[]);
  const report={mode,baseline,duration_ms:duration,collector_rounds:after.steps-before.steps,queries:Object.fromEntries(['gitlab','lark'].map(p=>[p,after.calls[p]-before.calls[p]])),idle:idle.map(rows=>({http:rows.length,bytes:rows.reduce((a,r)=>a+r.bytes,0),catalog_requests:rows.filter(r=>r.path==='/v1/rooms').length,catalog_computations:rows.filter(r=>r.path==='/v1/rooms'&&r.status===200).length,paths:Object.fromEntries([...new Set(rows.map(r=>r.path))].map(path=>[path,rows.filter(r=>r.path===path).length]))})),idle_frames:idleFrames.map(fs=>Object.fromEntries([...new Set(fs.map(f=>f.topic+':'+(f.kind||'summary')))].map(k=>[k,fs.filter(f=>f.topic+':'+(f.kind||'summary')===k).length]))),details:details.map(pair=>pair.map(rows=>({http:rows.length,notifications:rows.filter(r=>r.path==='/v1/platform/notifications').length,bytes:rows.reduce((a,r)=>a+r.bytes,0)}))),error_to_both_dom_ms:errorLatency,message_probe_to_dom_ms:probeLatency,message_to_dom_ms:messageLatency,independent_sessions:true,different_rooms:true,concurrent_shared_read:true,local_reading_view:true,hidden_recovery:true,logout_isolation:true,no_command_replay:true};
  if(process.env.TEAMX_WEB_UPDATES_SCOPE_OUTPUT)writeFileSync(process.env.TEAMX_WEB_UPDATES_SCOPE_OUTPUT,JSON.stringify(report,null,2)+'\n');
  console.log(JSON.stringify(report));
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
