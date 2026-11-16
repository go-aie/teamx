const {chromium}=require('playwright'),assert=require('node:assert/strict');
const {mkdirSync}=require('node:fs'),{join}=require('node:path');
const endpoint=process.argv[2],mode=process.argv[3],history=mode.startsWith('history'),large=mode==='history-large';
const combined=mode.startsWith('combined-'),width=combined?Number(mode.split('-')[1]):1440;
const artifacts=process.env.TEAMX_HUMAN_READ_ARTIFACTS;
if(artifacts)mkdirSync(artifacts,{recursive:true});
(async()=>{
 const browser=await chromium.launch({headless:true}),errors=[],monitors=[];
 try {
  const context=await browser.newContext({viewport:{width,height:width===1440?1000:844},isMobile:width<1440,hasTouch:width<1440}),page=await context.newPage();
  page.on('pageerror',e=>errors.push(e.message));page.setDefaultTimeout(15000);
  const readState=async()=>{
   const info=await(await fetch(endpoint+'/v1/ui')).json();
   const response=await fetch(endpoint+'/v1/human-read/state',{method:'POST',headers:{'Content-Type':'application/json',Origin:endpoint},body:JSON.stringify({room_id:info.room_id,room_instance:info.room_instance,service_epoch:info.service_epoch,caller:{kind:'human',id:info.operator}})});
   assert.equal(response.status,200,await response.clone().text());return response.json();
  };
  const waitFor=async(check)=>{const deadline=Date.now()+10000;while(!await check()){if(Date.now()>deadline)throw Error('condition timed out');await new Promise(r=>setTimeout(r,100));}};
  if(history){
   await page.addInitScript(()=>{
    const frame=window.requestAnimationFrame.bind(window);window.readingFrames=[];window.batchBuilds=[];window.longTasks=[];window.anchorErrors=[];
    window.requestAnimationFrame=fn=>frame(time=>{const start=performance.now();fn(time);if(window.measureReading)window.readingFrames.push(performance.now()-start);});
    new PerformanceObserver(list=>{if(window.measureReading)window.longTasks.push(...list.getEntries().map(e=>e.duration));}).observe({type:'longtask',buffered:false});
    const json=Response.prototype.json;
    Response.prototype.json=async function(){const data=await json.call(this);if(data.new_message_seqs)window.entryMetadataBytes=new TextEncoder().encode(JSON.stringify(data)).length;if(this.url.endsWith('/v1/read') && data.messages?.length){window.batchReady=performance.now();}return data;};
    addEventListener('DOMContentLoaded',()=>{
     new MutationObserver(records=>{
      if(records.some(r=>[...r.addedNodes].some(n=>n.classList?.contains('message'))) && window.measureReading && window.batchReady){
       window.batchBuilds.push(performance.now()-window.batchReady);window.batchReady=0;
      }
     }).observe(document.querySelector('#messages'),{childList:true});
    });
   });
  }
  if(!history && mode!=='locating')await page.addInitScript(()=>{document.hasFocus=()=>false;});
  await page.goto(endpoint);await page.locator('#message-'+(history?10000:combined?100:120)).waitFor();
  if(combined){
   const before=await readState();
   assert.equal(await page.locator('#messages .message').count(),50);
   assert.equal(await page.locator('#message-10').count(),0);
   assert.equal(before.unread_count,97,'own messages excluded; new mentions counted once');
   assert.deepEqual(before.unseen_mention_seqs,[10,70,95]);
   assert.equal(before.opened_through,0,'unfocused preload cannot open');
   // Keep the first loaded ordinary message visible when enabling observation,
   // so all three mentions initially remain outside the visible window.
   await page.locator('#message-51').scrollIntoViewIfNeeded();
   await page.evaluate(()=>{document.hasFocus=()=>true;window.dispatchEvent(new Event('focus'));});
   const observed=new Set(),own=new Set([5,55,96]);
   const observeVisible=async()=>{
    const seqs=await page.evaluate(async()=>{
     await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
     const root=document.querySelector('#timeline').getBoundingClientRect(),v=visualViewport;
     const top=Math.max(root.top,v.offsetTop),bottom=Math.min(root.bottom,v.offsetTop+v.height);
     const left=Math.max(root.left,v.offsetLeft),right=Math.min(root.right,v.offsetLeft+v.width);
     return [...document.querySelectorAll('#messages .message')].filter(el=>{
      const r=el.getBoundingClientRect();
      return Math.min(bottom,r.bottom)-Math.max(top,r.top)>=Math.min(r.height,root.height,v.height)/2 &&
       Math.min(right,r.right)-Math.max(left,r.left)>=Math.min(r.width,root.width,v.width)/2;
     }).map(el=>Number(el.id.slice(8)));
    });
    for(const seq of seqs)if(!own.has(seq))observed.add(seq);
   };
   await observeVisible();await waitFor(async()=> (await readState()).opened_through===100);
   const opened=await readState();assert.equal(opened.unread_count,3);assert.equal(opened.mention_count,3);
   assert.match(await page.locator('#view-mention').textContent(),/有人 @我 · 3 条/);
   await page.locator('#view-mention').click();await page.locator('#message-10').waitFor();
   await page.locator('#history-progress').waitFor({state:'hidden'});await observeVisible();
   await waitFor(async()=> (await readState()).mention_seen_seqs.includes(10));
   assert.equal(await page.locator('#messages .message').count(),100,'one click loads continuous history');
   assert.deepEqual((await readState()).unseen_mention_seqs,[70,95]);
   const divider=await page.locator('.new-message-divider').elementHandle();
   assert.equal(await divider.evaluate(el=>el.nextElementSibling.id),'message-1');
   await page.locator('#message-70').scrollIntoViewIfNeeded();await observeVisible();
   await waitFor(async()=> (await readState()).mention_seen_seqs.includes(70));
   assert.deepEqual((await readState()).unseen_mention_seqs,[95],'natural scroll only confirms visible mentions');
   // A fresh independent page has no ordinary bookmark, but keeps the remaining
   // mention across refresh. Its preloading must not affect the first visit.
   const second=await browser.newContext({viewport:{width:390,height:844}}),other=await second.newPage();
   other.on('pageerror',e=>errors.push(e.message));
   await other.addInitScript(()=>{document.hasFocus=()=>false;});
   await other.goto(endpoint);await other.locator('#message-100').waitFor();
   await other.reload();await other.locator('#view-mention').filter({hasText:'有人 @我 · 1 条'}).waitFor();
   assert.equal(await other.locator('.new-message-divider').count(),0);
   assert.deepEqual((await readState()).unseen_mention_seqs,[95]);
   await second.close();await page.bringToFront();
   // Lost durable receipt: preserve the optimistic count while ordinary state
   // polling independently recovers the actual server-side confirmation.
   let lostReceipt=false;
   const recovered=page.waitForResponse(async response=>response.url().endsWith('/v1/human-read/state') &&
    lostReceipt && response.status()===200 && (await response.json()).mention_seen_seqs.includes(95));
   await page.route('**/v1/human-read/confirm',async route=>{
    if(!lostReceipt && route.request().postDataJSON().mention_seen_seqs?.includes(95)){
     const response=await route.fetch();assert.equal(response.status(),200);
     lostReceipt=true;await route.abort('failed');return;
    }
    await route.continue();
   });
   await page.locator('#message-95').scrollIntoViewIfNeeded();await observeVisible();
   await waitFor(async()=> lostReceipt && (await readState()).mention_count===0);
   await recovered;
   const remaining=97-observed.size;
   assert.ok(remaining>0);assert.match(await page.locator('#view-mention').textContent(),new RegExp(`\\b${remaining} 条新消息`));
   assert.equal((await readState()).unread_count,0,'local review is independent of persistent unread');
   assert.equal(await divider.evaluate(el=>el===document.querySelector('.new-message-divider')),true);
   assert.equal(await divider.evaluate(el=>el.nextElementSibling.id),'message-1','viewing never moves the divider');
   await page.locator('#human-read-notice').waitFor({state:'hidden'});
   assert.match(await page.locator('#view-mention').textContent(),new RegExp(`\\b${remaining} 条新消息`));
   if(artifacts)await page.screenshot({path:join(artifacts,`combined-${width}.png`)});
   await page.reload();await page.locator('#message-100').waitFor();
   await page.locator('#mention-notice').waitFor({state:'hidden'});
   assert.equal(await page.locator('.new-message-divider').count(),0);
   assert.deepEqual((await readState()).mention_seen_seqs,[10,70,95]);
   console.log(JSON.stringify({mode,viewport:width,defaultWindow:50,mentions:[10,70,95],ownMessages:3,ordinaryRemaining:remaining,
    automaticHistory:true,naturalScroll:true,independentReloadPreservesMention:true,lostReceiptRecovered:true,exactLocalCount:true,fixedDivider:true,exitDropsBookmark:true}));
  }else if(history){
   assert.equal(await page.locator('#messages .message').count(),50,'performance starts at the normal latest window');
   await page.bringToFront();
   const cdp=await context.newCDPSession(page);await cdp.send('Performance.enable');
   let peakHeap=0,peakNodes=0,peakBacking=0,peakEmbedder=0;
   const sample=async()=>{const h=await cdp.send('Runtime.getHeapUsage'),d=await cdp.send('Memory.getDOMCounters');peakHeap=Math.max(peakHeap,h.usedSize);peakBacking=Math.max(peakBacking,h.backingStorageSize||0);peakEmbedder=Math.max(peakEmbedder,h.embedderHeapUsedSize||0);peakNodes=Math.max(peakNodes,d.nodes);};
   await sample();const initialHeap=peakHeap;
   let monitor=setInterval(()=>sample().catch(()=>{}),100);
   const inputTimes=[];let probing=false;
   const inputMonitor=setInterval(async()=>{
    if(probing)return;probing=true;const start=Date.now();
    try{await page.locator('#message-input').fill('补载时仍可编辑 '+start);inputTimes.push(Date.now()-start);}catch(error){if(!page.isClosed())errors.push(error.message);}finally{probing=false;}
   },150);
   monitors.push(monitor,inputMonitor);
   await page.evaluate(()=>{window.measureReading=true;});
   // A failed batch retains the committed prefix. Retry never duplicates nodes.
   let failAt=3,historyRequests=0,failed=false;const ranges=[];
   await page.route('**/v1/read',async route=>{
    const body=route.request().postDataJSON();
    if(body.snapshot_seq!==undefined){
     historyRequests++;ranges.push([body.after,body.snapshot_seq]);
     if(historyRequests===failAt){failed=true;await route.abort('failed');return;}
    }
    await route.continue();
   });
   await page.locator('#view-mention').click();
   await page.locator('#review-notice').filter({hasText:'历史加载失败'}).waitFor();
   assert.ok(failed);const failedCount=await page.locator('#messages .message').count();assert.equal(failedCount,150);
   assert.ok(!(await readState()).mention_seen_seqs.includes(1));
   failAt=-1;
   // Keep an existing visible node and its viewport offset through prepends.
   await page.locator('#message-9970').scrollIntoViewIfNeeded();
   const anchor=await page.locator('#message-9970').elementHandle(),anchorTop=await anchor.evaluate(el=>el.getBoundingClientRect().top);
   await page.locator('#view-mention').click();
   await page.waitForFunction(()=>document.querySelectorAll('#messages .message').length>=300);
   const cancelStart=Date.now();await page.locator('#cancel-history').click();const cancelMs=Date.now()-cancelStart;
   await page.locator('#history-progress').waitFor({state:'hidden'});
   const cancelledCount=await page.locator('#messages .message').count();await page.waitForTimeout(200);
   assert.equal(await page.locator('#messages .message').count(),cancelledCount);
   assert.ok(!(await readState()).mention_seen_seqs.includes(1),'cancelling never confirms the unloaded target');
   assert.ok(Math.abs(await anchor.evaluate(el=>el.getBoundingClientRect().top)-anchorTop)<2,'prepends preserve the visible anchor');
   const start=Date.now();await page.locator('#view-mention').click();
   await page.locator('#message-1').waitFor({timeout:90000});
   await page.locator('#history-progress').waitFor({state:'hidden',timeout:90000});
   await waitFor(async()=> (await readState()).mention_seen_seqs.includes(1));
   const elapsedMs=Date.now()-start;
   assert.equal(await page.locator('#messages .message').count(),10000);
   assert.equal(await anchor.evaluate(el=>el===document.querySelector('#message-9970')),true);
   assert.equal(await page.locator('#messages .day-divider').count(),1);
   assert.equal(await page.locator('#messages .new-message-divider').count(),1);
   assert.equal(await page.evaluate(()=>new Set([...document.querySelectorAll('#messages .message')].map(n=>n.id)).size),10000);
   assert.ok(ranges.every(([a,b])=>b-a<=50),'every snapshot covers at most 50 messages');
   // Main polling after loading must append without rebuilding retained nodes.
   const info=await(await fetch(endpoint+'/v1/ui')).json();
   // The real backend retains at most 10,000 messages. Free one slot only
   // after measuring the complete automatic load, then measure a real append.
   const identity={room_id:info.room_id,room_instance:info.room_instance,service_epoch:info.service_epoch,caller:{kind:'human',id:info.operator}};
   const cleared=await fetch(endpoint+'/v1/messages/clear',{method:'POST',headers:{'Content-Type':'application/json',Origin:endpoint},body:JSON.stringify({...identity,snapshot_seq:1})});
   assert.equal(cleared.status,200,await cleared.text());await page.locator('#message-1').waitFor({state:'detached'});
   const refreshStart=Date.now();
   const sent=await fetch(endpoint+'/v1/send',{method:'POST',headers:{'Content-Type':'application/json',Origin:endpoint},body:JSON.stringify({room_id:info.room_id,room_instance:info.room_instance,service_epoch:info.service_epoch,caller:{kind:'human',id:info.operator},client_message_id:'history-live-append',text:'补载完成后正常刷新。'})});assert.equal(sent.status,200,await sent.text());
   await page.locator('#message-10001').waitFor();const refreshMs=Date.now()-refreshStart;
   assert.equal(await anchor.evaluate(el=>el===document.querySelector('#message-9970')),true);
   const refreshPosition=await page.locator('#timeline').evaluate(el=>el.scrollTop);
   await page.waitForTimeout(1700);assert.equal(await page.locator('#timeline').evaluate(el=>el.scrollTop),refreshPosition);
   await page.evaluate(()=>{window.readingFrames=[];});
   for(let i=0;i<80;i++){
    await page.locator('#timeline').evaluate((el,i)=>{el.scrollTop=el.scrollHeight*(i/80);el.dispatchEvent(new Event('scroll'));},i);
    await page.waitForTimeout(20);
   }
   clearInterval(monitor);clearInterval(inputMonitor);await sample();
   const stats=await page.evaluate(()=>{
    window.measureReading=false;
    const summary=a=>{a.sort((a,b)=>a-b);return{samples:a.length,p50:a[Math.floor(a.length*.5)]||0,p95:a[Math.floor(a.length*.95)]||0,max:a.at(-1)||0};};
    return{entryMetadataBytes:window.entryMetadataBytes,readingFrameMs:summary(window.readingFrames),batchBuildAndCommitMs:summary(window.batchBuilds),longTasksMs:summary(window.longTasks)};
   });
   assert.ok(stats.readingFrameMs.samples>=40);assert.ok(stats.readingFrameMs.p95<50,JSON.stringify(stats));
   assert.ok(stats.batchBuildAndCommitMs.max<500,JSON.stringify(stats));
   assert.ok(cancelMs<500,`cancel took ${cancelMs}ms`);
   assert.ok(Math.max(...inputTimes)<500,`input took ${Math.max(...inputTimes)}ms`);
   const reading=await readState();const metadataBytes=Buffer.byteLength(JSON.stringify(reading));
   inputTimes.sort((a,b)=>a-b);
   console.log(JSON.stringify({mode,messages:10000,loadedMessages:10000,textBytes:large?6600:42,viewport:[1440,1000],elapsedMs,historyRequests,
    failedCount,cancelledCount,cancelMs,inputMs:{samples:inputTimes.length,p95:inputTimes[Math.floor(inputTimes.length*.95)],max:Math.max(...inputTimes)},refreshMs,metadataBytes,
    initialHeap,peakHeap,peakBacking,peakEmbedder,peakNodes,clearOneForCapacity:true,...stats}));
  }else if(mode==='locating'){
   await page.bringToFront();
   let release,hold=true,fail=false;
   await page.route('**/v1/read',async route=>{
    const body=route.request().postDataJSON();
    if(body.snapshot_seq!==undefined){
     if(fail){fail=false;await route.abort('failed');return;}
     const response=await route.fetch();
     if(hold)await new Promise(resolve=>{release=resolve;});
     await route.fulfill({response});return;
    }
    await route.continue();
   });
   await page.locator('#view-mention').click();await page.waitForFunction(()=>!document.querySelector('#history-progress').hidden);
   assert.equal(await page.locator('#load-history').isEnabled(),false);
   await page.locator('#cancel-history').click();hold=false;release();await page.waitForTimeout(150);
   assert.equal(await page.locator('#message-1').count(),0);assert.ok(!(await readState()).mention_seen_seqs.includes(1));
   fail=true;await page.locator('#view-mention').click();await page.locator('#review-notice').filter({hasText:'历史加载失败'}).waitFor();
   await page.locator('#view-mention').click();await page.locator('#message-1').waitFor();
   await waitFor(async()=> (await readState()).mention_seen_seqs.includes(1));
   assert.equal(await page.locator('#messages .message').count(),120);
   assert.ok(!(await readState()).mention_seen_seqs.includes(21),'intermediate targets passed by instant positioning stay unviewed');
   await page.reload();await page.locator('#message-120').waitFor();hold=true;
   await page.locator('#view-mention').click();await page.locator('#history-progress').waitFor({state:'visible'});
   const info=await(await fetch(endpoint+'/v1/ui')).json();
   const cleared=await fetch(endpoint+'/v1/messages/clear',{method:'POST',headers:{'Content-Type':'application/json',Origin:endpoint},body:JSON.stringify({room_id:info.room_id,room_instance:info.room_instance,service_epoch:info.service_epoch,caller:{kind:'human',id:info.operator},snapshot_seq:70})});assert.equal(cleared.status,200);
   await page.locator('#history-progress').waitFor({state:'hidden'});hold=false;release();await page.waitForTimeout(200);
   assert.equal(await page.locator('#message-21').count(),0,'clear discards a previously valid late history response');
   assert.ok(!(await readState()).mention_seen_seqs.includes(21));
   console.log(JSON.stringify({mode,defaultWindow:50,automaticUnloadedTarget:true,manualGate:true,cancelDiscardsLateResponse:true,retryContinues:true,instantPositionDoesNotConfirmPassedMentions:true,clearDiscardsLateResponse:true}));
  }else{
   assert.ok(!(await readState()).mention_seen_seqs.includes(120),'unfocused preload cannot confirm');
   assert.equal((await readState()).opened_through,0,'unfocused preload cannot open');
   let holdRemote=true;const held=[];
   await page.route('**/v1/human-read/state',async route=>{
    if(holdRemote)await new Promise(resolve=>held.push(resolve));
    await route.continue();
   });
   await page.locator('#settings-dialog').evaluate(el=>el.showModal());
   const second=await browser.newContext({viewport:{width:390,height:844}}),phone=await second.newPage();
   phone.on('pageerror',e=>errors.push(e.message));await phone.goto(endpoint);await phone.locator('#message-120').waitFor();
   await phone.bringToFront();await phone.locator('#message-120').scrollIntoViewIfNeeded();
   await waitFor(async()=> (await readState()).mention_seen_seqs.includes(120));
   const before=await readState();assert.equal(before.opened_through,120);
   assert.ok(before.unseen_mention_seqs.includes(100),'unviewed loaded mention persists independently');
   assert.equal(before.unread_count,before.mention_count,'ordinary unread clears on open');
   await page.bringToFront();
   await page.locator('#timeline').evaluate(el=>{el.scrollTop=0;});
   await page.locator('#settings-dialog').evaluate(el=>el.close());
   const top=await page.locator('#timeline').evaluate(el=>el.scrollTop);
   const node=await page.locator('#message-120').elementHandle();
   assert.equal(await node.evaluate(el=>el.classList.contains('mentioned')),true,'desktop awaits remote receipt');
   const syncStart=Date.now();holdRemote=false;for(const release of held)release();
   await page.waitForFunction(()=>!document.querySelector('#message-120').classList.contains('mentioned'));
   const syncMs=Date.now()-syncStart;assert.ok(syncMs<3000,`reading sync took ${syncMs}ms`);
   assert.equal(await page.locator('#timeline').evaluate(el=>el.scrollTop),top);
   assert.equal(await node.evaluate(el=>el===document.querySelector('#message-120')),true);
   assert.ok(await page.locator('#message-120 .message-text .mention').count()>0,'@ text remains colored');
   assert.equal(await page.locator('#jump-latest').isVisible(),false);
   if(artifacts)await page.screenshot({path:join(artifacts,'reading-desktop.png')});
   await phone.bringToFront();
   await phone.locator('#view-mention').click();
   await waitFor(async()=> (await readState()).mention_seen_seqs.includes(100));
   assert.equal(await phone.locator('#message-100').evaluate(el=>el.classList.contains('mentioned')),false);
   await phone.locator('#message-90').scrollIntoViewIfNeeded();
   const info=await(await fetch(endpoint+'/v1/ui')).json();
   const sent=await fetch(endpoint+'/v1/send',{method:'POST',headers:{'Content-Type':'application/json',Origin:endpoint},body:JSON.stringify({room_id:info.room_id,room_instance:info.room_instance,service_epoch:info.service_epoch,caller:{kind:'human',id:info.operator},client_message_id:'browser-jump-layout',text:'检查回看与回到最新消息入口。'})});
   assert.equal(sent.status,200);
   await phone.locator('#jump-latest').waitFor({state:'visible'});
   for(const width of [390,320]){
    await phone.setViewportSize({width,height:844});await phone.bringToFront();
    await phone.locator('#view-mention').waitFor({state:'visible'});
    const button=await phone.locator('#view-mention').boundingBox();assert.ok(button.height>=44);
    const latest=await phone.locator('#jump-latest').boundingBox(),input=await phone.locator('#compose').boundingBox();
    assert.ok(latest.height>=44 && button.y+button.height<=latest.y && latest.y+latest.height<=input.y);
    assert.match(await phone.locator('#jump-latest').textContent(),/回到最新消息/);
    assert.equal(await phone.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
    if(artifacts)await phone.screenshot({path:join(artifacts,`reading-mobile-${width}.png`)});
   }
   // A fresh page has no ordinary bookmark, while the shared receipts survive.
   await phone.reload();await phone.locator('#message-121').waitFor();
   await phone.locator('#mention-notice').waitFor({state:'hidden'});
   assert.equal((await readState()).mention_count,0);
   await page.route('**/v1/human-read/state',route=>route.abort('timedout'));
   await page.locator('#human-read-notice').waitFor({state:'visible'});
   await page.bringToFront();
   await page.locator('#message-input').fill('reading failures still allow sending');
   assert.equal(await page.locator('#send').isEnabled(),true);
   await page.locator('#send').click();
   await page.getByText('reading failures still allow sending',{exact:true}).waitFor();
   assert.equal(await page.locator('#connection').textContent(),'服务已连接');
   await second.close();console.log(JSON.stringify({mode,independentContexts:2,viewports:[[1440,1000],[390,844],[320,844]],syncMs,
     openedClearsOrdinary:true,unviewedMentionPreserved:true,loadedClickConfirms:true,readingOnlyKeepsScrollAndNodes:true,reloadDropsOrdinaryBookmark:true,readingFailureAllowsSend:true}));
  }
  assert.deepEqual(errors,[]);
 }finally{for(const timer of monitors)clearInterval(timer);await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
