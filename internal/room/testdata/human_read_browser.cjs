const {chromium}=require('playwright'),assert=require('node:assert/strict');
const {mkdirSync}=require('node:fs'),{join}=require('node:path');
const endpoint=process.argv[2],mode=process.argv[3],history=mode.startsWith('history'),large=mode==='history-large';
const artifacts=process.env.TEAMX_HUMAN_READ_ARTIFACTS;
if(artifacts)mkdirSync(artifacts,{recursive:true});
(async()=>{
 const browser=await chromium.launch({headless:true}),errors=[];
 try {
  const context=await browser.newContext({viewport:{width:1440,height:1000}}),page=await context.newPage();
  page.on('pageerror',e=>errors.push(e.message));page.setDefaultTimeout(15000);
  const readState=async()=>{
   const info=await(await fetch(endpoint+'/v1/ui')).json();
   const response=await fetch(endpoint+'/v1/human-read/state',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({room_id:info.room_id,room_instance:info.room_instance,service_epoch:info.service_epoch,caller:{kind:'human',id:info.operator}})});
   assert.equal(response.status,200);return response.json();
  };
  const waitFor=async(check)=>{const deadline=Date.now()+10000;while(!await check()){if(Date.now()>deadline)throw Error('condition timed out');await new Promise(r=>setTimeout(r,100));}};
  if(history){
   // Load the complete real retained history through the normal fixed-snapshot
   // continuation path, bypassing only the initial 50-message selection.
   if(!large)await page.route('**/v1/read',async route=>{
    const body=route.request().postDataJSON();
    if(body.mode==='latest'){body.mode='range';body.after=0;delete body.latest;}
    await route.continue({postData:JSON.stringify(body)});
   });
   await page.addInitScript(()=>{
    const frame=window.requestAnimationFrame.bind(window);window.readingFrames=[];
    window.requestAnimationFrame=fn=>frame(time=>{const start=performance.now();fn(time);if(window.measureReading)window.readingFrames.push(performance.now()-start);});
   });
  }
  if(mode==='history-mentions')await page.route('**/features/conversation.mjs',async route=>{
   const response=await route.fetch(),source=await response.text();
   const target='if (!humanRead.seen(message.seq)) unreadCandidates.set(message.seq, el);';assert.ok(source.includes(target));
   await route.fulfill({response,body:source.replace(target,'if (mentioned && !humanRead.seen(message.seq)) unreadCandidates.set(message.seq, el);')});
  });
  await page.goto(endpoint);await page.locator('#message-'+(history?10000:120)).waitFor();
  if(history){
   assert.equal(await page.locator('#messages .message').count(),large?50:10000);
   await page.evaluate(()=>{window.measureReading=true;});
   for(let i=0;i<80;i++){
    await page.locator('#timeline').evaluate((el,i)=>{el.scrollTop=el.scrollHeight*(i/80);el.dispatchEvent(new Event('scroll'));},i);
    await page.waitForTimeout(20);
   }
   const timing=await page.evaluate(()=>{window.measureReading=false;const a=window.readingFrames.sort((a,b)=>a-b);return{samples:a.length,p50:a[Math.floor(a.length*.5)],p95:a[Math.floor(a.length*.95)],max:a.at(-1)};});
   assert.ok(timing.samples>=40);assert.ok(timing.p95<50,JSON.stringify(timing));
   console.log(JSON.stringify({mode,messages:10000,loadedMessages:large?50:10000,textBytes:large?6600:42,viewport:[1440,1000],readingFrameMs:timing}));
  }else{
   assert.ok(!(await readState()).seen_seqs.includes(120),'the first page has not read the target');
   let holdRemote=true;const held=[];
   await page.route('**/v1/human-read/state',async route=>{
    if(holdRemote)await new Promise(resolve=>held.push(resolve));
    await route.continue();
   });
   // Separate headless windows can both report focus. Cover the desktop with
   // a real modal so only the phone can observe the target during this step.
   await page.locator('#settings-dialog').evaluate(el=>el.showModal());
   const second=await browser.newContext({viewport:{width:390,height:844}}),phone=await second.newPage();
   phone.on('pageerror',e=>errors.push(e.message));await phone.goto(endpoint);await phone.locator('#message-120').waitFor();
   // Both pages initially load only recent history. Reading the newest message
   // must leave the earliest retained message unread.
   await phone.bringToFront();await phone.locator('#message-120').scrollIntoViewIfNeeded();
   await waitFor(async()=> (await readState()).seen_seqs.includes(120));
   const before=await readState();assert.ok(!before.seen_seqs.includes(1));
   await page.bringToFront();
   await page.locator('#timeline').evaluate(el=>{el.scrollTop=0;});
   await page.locator('#settings-dialog').evaluate(el=>el.close());
   const top=await page.locator('#timeline').evaluate(el=>el.scrollTop);
   const node=await page.locator('#message-120').elementHandle();
   assert.equal(await node.evaluate(el=>el.classList.contains('mentioned')),true,'the unfocused page still awaits the remote reading result');
   const syncStart=Date.now();holdRemote=false;for(const release of held)release();
   await page.waitForFunction(()=>!document.querySelector('#message-120').classList.contains('mentioned'));
   const syncMs=Date.now()-syncStart;assert.ok(syncMs<3000,`reading sync took ${syncMs}ms`);
   assert.equal(await page.locator('#timeline').evaluate(el=>el.scrollTop),top);
   assert.equal(await node.evaluate(el=>el===document.querySelector('#message-120')),true);
   assert.ok(await page.locator('#message-120 .message-text .mention').count()>0,'@ text remains colored');
   assert.equal(await page.locator('#jump-latest').isVisible(),false);
   if(artifacts)await page.screenshot({path:join(artifacts,'reading-desktop.png')});
   await phone.locator('#message-90').scrollIntoViewIfNeeded();
   const info=await(await fetch(endpoint+'/v1/ui')).json();
   const sent=await fetch(endpoint+'/v1/send',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({room_id:info.room_id,room_instance:info.room_instance,service_epoch:info.service_epoch,caller:{kind:'human',id:info.operator},client_message_id:'browser-jump-layout',text:'检查新消息与提及按钮共存。'})});
   assert.equal(sent.status,200);
   await phone.locator('#jump-latest').waitFor({state:'visible'});
   for(const width of [390,320]){
    await phone.setViewportSize({width,height:844});await phone.bringToFront();
    await phone.locator('#view-mention').waitFor({state:'visible'});
    const button=await phone.locator('#view-mention').boundingBox();assert.ok(button.height>=44);
    const latest=await phone.locator('#jump-latest').boundingBox(),input=await phone.locator('#compose').boundingBox();
    assert.ok(latest.height>=44 && button.y+button.height<=latest.y && latest.y+latest.height<=input.y,'both jumps stack above the composer');
    assert.match(await phone.locator('#view-mention').getAttribute('aria-label'),/提及你.*assistant/);
    assert.equal(await phone.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
    if(artifacts)await phone.screenshot({path:join(artifacts,`reading-mobile-${width}.png`)});
   }
   // Inject a stalled state transport; message polls and composer stay usable.
   await page.route('**/v1/human-read/state',route=>route.abort('timedout'));
   await page.locator('#human-read-notice').waitFor({state:'visible'});
   await page.locator('#message-input').fill('reading failures leave this draft usable');
   assert.equal(await page.locator('#send').isEnabled(),true);
   assert.equal(await page.locator('#connection').textContent(),'服务已连接');
   await second.close();console.log(JSON.stringify({mode,independentContexts:2,viewports:[[1440,1000],[390,844],[320,844]],syncMs,gapPreserved:true,readingFailureIsolated:true}));
  }
  assert.deepEqual(errors,[]);
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
