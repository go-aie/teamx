const {chromium}=require('playwright'),assert=require('node:assert/strict');
const {mkdirSync}=require('node:fs'),{join}=require('node:path');
const [endpoint,directID,groupID,brokenID]=process.argv.slice(2);
const artifacts=process.env.TEAMX_HUMAN_READ_ARTIFACTS;
if(artifacts)mkdirSync(artifacts,{recursive:true});
(async()=>{
 const browser=await chromium.launch({headless:true}),errors=[];
 try {
  const context=await browser.newContext({viewport:{width:1440,height:1000}}),desktop=await context.newPage();
  const phoneContext=await browser.newContext({viewport:{width:390,height:844},isMobile:true,hasTouch:true}),phone=await phoneContext.newPage();
  for(const page of [desktop,phone]){page.on('pageerror',e=>errors.push(e.message));page.setDefaultTimeout(12000);}
  // Independent contexts can contend on the catalog mutex, as normal clients do.
  const catalog=async()=>{
   for(let attempt=0;;attempt++){
    const response=await fetch(endpoint+'/v1/rooms'),body=await response.json();
    if(response.status===409 && body.code==='busy' && attempt<20){await new Promise(r=>setTimeout(r,50));continue;}
    assert.equal(response.status,200,JSON.stringify(body));return body;
   }
  };
  let data=await catalog();
  const room=id=>data.rooms.find(r=>r.room_id===id);
  const names={direct:room(directID).name,group:room(groupID).name,broken:room(brokenID).name};
  const choice=(page,id)=>page.locator('.room-choice').filter({has:page.locator('strong').filter({hasText:id===directID?names.direct:id===groupID?names.group:names.broken})});
  const read=async id=>{const data=await catalog();return data.rooms.find(r=>r.room_id===id).human_read;};
  const waitFor=async check=>{const end=Date.now()+12000;while(!await check()){assert.ok(Date.now()<end,'condition timed out');await new Promise(r=>setTimeout(r,100));}};
  // Stop local viewing on the desktop, then let an independent phone context
  // read a conversation the desktop has never opened.
  await desktop.goto(endpoint);await choice(desktop,groupID).waitFor();
  await desktop.locator('#settings-dialog').evaluate(el=>el.showModal());
  assert.equal(await choice(desktop,groupID).locator('.room-unread').textContent(),'99+');
  assert.equal(await choice(desktop,groupID).locator('.room-mention').textContent(),'[有人@我]');
  assert.equal(await choice(desktop,brokenID).locator('.room-read-unavailable').textContent(),'暂未同步');
  assert.equal(await choice(desktop,directID).locator('.room-mention').count(),0);
  assert.equal(await choice(desktop,directID).locator('.room-avatar .assistant-icon').count(),1);
  assert.equal(await choice(desktop,groupID).locator('.room-avatar-name').textContent(),names.group);
  const before=await read(groupID);
  await phone.goto(endpoint);
  await phone.locator('#room-list .room-choice').first().waitFor();
  if(await phone.locator('#workspace').evaluate(el=>el.classList.contains('rooms-collapsed')))await phone.locator('#mobile-back-to-rooms').click();
  await choice(phone,groupID).click();await phone.locator('#message-120').waitFor();
  await phone.bringToFront();await phone.locator('#message-120').scrollIntoViewIfNeeded();
  await waitFor(async()=> (await read(groupID)).revision>before.revision);
  const changed=await read(groupID),syncStart=Date.now();
  assert.equal(changed.unread_count,changed.mention_count,"opening clears ordinary unread while retaining unviewed mentions");
  await waitFor(async()=> (await choice(desktop,groupID).getAttribute('aria-label')).includes(`${changed.unread_count} 条未读消息`));
  const syncMs=Date.now()-syncStart;assert.ok(syncMs<3000,`catalog sync ${syncMs}ms`);
  assert.equal(await desktop.locator('#message-120').count(),1,'desktop remains in its original conversation');
  // Returning to the mobile list ends this visit; opening the same conversation
  // captures a new bookmark while the unloaded, unviewed mention survives.
  assert.ok((await read(groupID)).mention_count>0);
  await phone.locator('#mobile-back-to-rooms').click();await choice(phone,groupID).click();
  await phone.locator('#view-mention').filter({hasText:'有人 @我'}).waitFor();
  assert.equal(await phone.locator('.new-message-divider').count(),0,'mobile list discards the previous ordinary bookmark');
  assert.equal(await phone.locator('#message-1').count(),0,'reopening does not preload the old mention');
  // Open the group on desktop. Reading changes must not create a new latest
  // message, reorder rows or consume unrendered history.
  await desktop.locator('#settings-dialog').evaluate(el=>el.close());await choice(desktop,groupID).click();
  await desktop.locator('#message-120').waitFor();await desktop.locator('#view-mention').waitFor({state:'visible'});
  assert.match(await desktop.locator('#view-mention').textContent(),/有人 @我/);
  await desktop.locator('#load-history').click();await desktop.locator('#message-21').waitFor();
  assert.equal(await desktop.locator('#message-1').count(),0,'history loads only one earlier batch');
  await desktop.locator('#view-mention').waitFor({state:'visible'});
  assert.match(await desktop.locator('#view-mention').textContent(),/有人 @我/);
  // A response fetched under the old Room remains invalid even if its network
  // completion arrives only after navigation has opened a different Room.
  let releaseHistory,markHeld;
  const heldHistory=new Promise(resolve=>{markHeld=resolve;});
  const holdHistory=async route=>{
   const body=route.request().postDataJSON();
   if(body.room_id===groupID && body.snapshot_seq!==undefined){
    const response=await route.fetch();
    await new Promise(resolve=>{releaseHistory=resolve;markHeld();});
    await route.fulfill({response});return;
   }
   await route.continue();
  };
  await desktop.route('**/v1/read',holdHistory);
  await desktop.locator('#view-mention').click();await heldHistory;
  assert.equal(await desktop.locator('#load-history').isEnabled(),false);
  await choice(desktop,directID).click();await desktop.locator('#message-120').waitFor();
  releaseHistory();await desktop.waitForTimeout(200);
  assert.equal(await desktop.locator('#message-1').count(),0);
  assert.equal(await desktop.locator('#message-21').count(),0);
  assert.equal(await desktop.locator('#history-progress').isVisible(),false);
  await desktop.unroute('**/v1/read',holdHistory);
  await choice(desktop,groupID).click();await desktop.locator('#message-120').waitFor();
  // Verify retained last values under a per-row catalog failure; other rows and
  // the normal member/message/composer path continue refreshing.
  const last=await choice(desktop,groupID).locator('.room-unread').textContent();
  await desktop.route('**/v1/rooms',async route=>{
   const response=await route.fetch(),body=await response.json();
   if(!Array.isArray(body.rooms)){await route.fulfill({response});return;}
   body.rooms.find(r=>r.room_id===groupID).human_read={status:'unavailable'};
   await route.fulfill({response,json:body});
  });
  const changedCatalog=await fetch(endpoint+'/v1/send',{method:'POST',headers:{'Content-Type':'application/json',Origin:endpoint},body:JSON.stringify({...room(groupID).service,caller:{kind:'human',id:data.operator},client_message_id:'catalog-fault-notification',text:'触发目录失效通知'})});
  assert.equal(changedCatalog.status,200,await changedCatalog.text());
  await choice(desktop,groupID).locator('.room-read-unavailable').waitFor();
  assert.equal(await choice(desktop,groupID).locator('.room-unread').textContent(),last);
  await desktop.locator('#message-input').fill('目录阅读故障期间保留草稿');assert.equal(await desktop.locator('#send').isEnabled(),true);
  assert.equal(await desktop.locator('#connection').textContent(),'服务已连接');
  await desktop.unroute('**/v1/rooms');
  // Isolate layout checks from visible confirmation, then exercise both conversation
  // types with real new-message polling and a controlled activity response.
  for(const page of [desktop,phone]){
   await page.evaluate(()=>{document.hasFocus=()=>false;window.dispatchEvent(new Event('blur'));});
   await page.route('**/v1/member/list',async route=>{
    const response=await route.fetch(),body=await response.json();
   if(!Array.isArray(body.members)){await route.fulfill({response});return;}
    if(body.members[0]){body.members[0].status='running';body.members[0].execution={active:true,execution_id:'layout-check'};}
    await route.fulfill({response,json:body});
   });
  }
  for(const width of [1440,390,320]){
   const page=width===1440?desktop:phone;
   await page.setViewportSize({width,height:width===1440?1000:544});
   for(const id of [directID,groupID]){
    if(width!==1440)await page.locator('#mobile-back-to-rooms').click();
    await choice(page,id).click();await page.locator('#message-100').waitFor();
    await page.locator('#message-90').scrollIntoViewIfNeeded();
    const data=await catalog(),target=data.rooms.find(r=>r.room_id===id);
    const sent=await fetch(endpoint+'/v1/send',{method:'POST',headers:{'Content-Type':'application/json',Origin:endpoint},body:JSON.stringify({...target.service,caller:{kind:'human',id:data.operator},client_message_id:`catalog-layout-${id}-${width}`,text:'检查双按钮与执行提示避让。'})});
    assert.equal(sent.status,200);
    await page.locator('#jump-latest').waitFor({state:'visible'});await page.locator('#view-mention').waitFor({state:'visible'});
    await page.locator('#execution-hint').waitFor({state:'visible'});
    const mention=await page.locator('#view-mention').boundingBox(),latest=await page.locator('#jump-latest').boundingBox();
    const activity=await page.locator('#execution-hint').boundingBox(),input=await page.locator('#compose').boundingBox();
    assert.ok(mention.height>=44 && latest.height>=44 && mention.y+mention.height<=latest.y && latest.y+latest.height<=activity.y && activity.y+activity.height<=input.y);
    assert.match(await page.locator('#view-mention').getAttribute('aria-label'),/提及你/);
    if(artifacts)await page.screenshot({path:join(artifacts,`jumps-${id===directID?'direct':'room'}-${width}.png`)});
   }
  }
  // Exercise long labels, 99+, defaults and room mentions in the real layout.
  for(const page of [desktop,phone]){
   await page.route('**/v1/rooms',async route=>{
    const response=await route.fetch(),body=await response.json();
   if(!Array.isArray(body.rooms)){await route.fulfill({response});return;}
    for(const r of body.rooms){
     r.name=(r.room_id===brokenID?'CSGScion · ':'')+r.name+' · 很长的会话名称 · 用来检查默认助理时间摘要和角标布局';
     if(r.human_read.status==='ready')r.human_read.unread_count=123;
    }
    await route.fulfill({response,json:body});
   });
  }
  const layoutCatalog=await fetch(endpoint+'/v1/send',{method:'POST',headers:{'Content-Type':'application/json',Origin:endpoint},body:JSON.stringify({...room(groupID).service,caller:{kind:'human',id:data.operator},client_message_id:'catalog-layout-notification',text:'触发目录布局投影刷新'})});
  assert.equal(layoutCatalog.status,200,await layoutCatalog.text());
  for(const width of [1440,390,320]){
   const page=width===1440?desktop:phone;
   if(width!==1440){await page.setViewportSize({width,height:844});if(await page.locator('#workspace').evaluate(el=>el.classList.contains('rooms-collapsed')))await page.locator('#mobile-back-to-rooms').click();}
   await waitFor(async()=> (await page.locator('.room-choice strong').first().textContent()).includes('很长的会话名称'));
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
   for(const item of await page.locator('.room-choice').all()){
    const layout=await item.evaluate(el=>{
     const rect=node=>{const r=node.getBoundingClientRect();return {x:r.x,y:r.y,width:r.width,height:r.height,right:r.right,bottom:r.bottom};};
     const image=el.querySelector('.room-avatar-image'),label=el.querySelector('.room-avatar-name');
     return {radius:getComputedStyle(image).borderRadius,label:label&&rect(label),rows:getComputedStyle(el).gridTemplateRows.split(' ').length,box:rect(el),avatar:rect(el.querySelector('.room-avatar')),detail:rect(el.querySelector('.room-detail')),name:rect(el.querySelector('strong')),time:rect(el.querySelector('time')),
      mention:el.querySelector('.room-mention')&&rect(el.querySelector('.room-mention')),badge:el.querySelector('.room-unread')&&rect(el.querySelector('.room-unread'))};
    });
    assert.equal(layout.rows,2,'conversation stays on two rows');assert.ok(layout.box.height<=72);
    assert.equal(layout.radius,'50%');
    if(layout.label)assert.ok(layout.label.width<=32 && layout.label.height<=28,'name fits two lines inside the circle');
    assert.ok(layout.name.width>0 && layout.name.right<=layout.time.x);
    assert.ok(layout.detail.x>layout.avatar.right && layout.detail.bottom<=layout.box.bottom);
    if(layout.mention)assert.equal(layout.mention.y,layout.detail.y);
    if(layout.badge)assert.ok(layout.badge.y<layout.avatar.y && layout.badge.right>layout.avatar.right && layout.badge.y>=layout.box.y);
   }
   for(const row of await page.locator('.room-entry').all()){
    const count=row.locator('.room-unread'),remove=row.locator('.room-delete');
    if(await count.count() && await remove.count()){
     const a=await count.boundingBox(),b=await remove.boundingBox();
     assert.ok(!a||!b||a.y+a.height<=b.y||b.y+b.height<=a.y||a.x+a.width<=b.x||b.x+b.width<=a.x,'badge does not overlap delete');
    }
   }
   assert.match(await page.locator('.room-choice').first().getAttribute('aria-label'),/默认助理/);
   if(artifacts)await page.screenshot({path:join(artifacts,`catalog-${width}.png`)});
   if(width!==1440)await choice(page,groupID).click();
  }
  // Use Chromium touch input to exercise long presses, scrolling cancellation
  // and the native release/click sequence, rather than dispatching a fake click.
  const touch=await phoneContext.newCDPSession(phone);
  const touchStart=async locator=>{
   const b=await locator.boundingBox(),point={x:b.x+b.width/2,y:b.y+b.height/2};
   await touch.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:[point]});return point;
  };
  const touchEnd=()=>touch.send('Input.dispatchTouchEvent',{type:'touchEnd',touchPoints:[]});
  let deleteWrites=0;phone.on('request',req=>{if(new URL(req.url()).pathname==='/v1/rooms/delete')deleteWrites++;});
  await phone.bringToFront();
  for(const width of [390,320]){
   await phone.setViewportSize({width,height:844});
   if(await phone.locator('#workspace').evaluate(el=>el.classList.contains('rooms-collapsed')))await phone.locator('#mobile-back-to-rooms').tap();
   const item=choice(phone,groupID),remove=item.locator('..').locator('.room-delete');
   assert.equal(await remove.isVisible(),false);
   assert.equal(await item.evaluate(el=>getComputedStyle(el).paddingRight),'12px','hidden action reserves no space');
   if(artifacts)await phone.screenshot({path:join(artifacts,`delete-hidden-${width}.png`)});
   const point=await touchStart(item);
   await touch.send('Input.dispatchTouchEvent',{type:'touchMove',touchPoints:[{...point,y:point.y+24}]});
   await touchEnd();await new Promise(r=>setTimeout(r,600));assert.equal(await remove.isVisible(),false,'drag cancels hold');
   await choice(phone,directID).tap();
   await waitFor(async()=>await phone.locator('#workspace').evaluate(el=>el.classList.contains('rooms-collapsed')));
   await phone.locator('#mobile-back-to-rooms').tap();const originalName=await phone.locator('#room-name').textContent();
   await touchStart(item);await remove.waitFor({state:'visible'});await touchEnd();
   assert.equal(await phone.locator('#workspace').evaluate(el=>el.classList.contains('rooms-collapsed')),false,'long press release does not open conversation');
   assert.equal(await phone.locator('#room-name').textContent(),originalName);
   const action=await remove.boundingBox(),summary=await item.locator('.room-detail').boundingBox();
   assert.ok(action.width>=44 && action.height>=44 && summary.x+summary.width<=action.x,'revealed action has its own touch area');
   if(artifacts)await phone.screenshot({path:join(artifacts,`delete-revealed-${width}.png`)});
   await remove.tap();await phone.locator('#delete-dialog').waitFor({state:'visible'});
   assert.equal(deleteWrites,0);await phone.locator('#delete-dismiss').tap();assert.equal(await remove.isVisible(),false);
   await touchStart(item);await remove.waitFor({state:'visible'});await touchEnd();
   await phone.touchscreen.tap(8,350);assert.equal(await remove.isVisible(),false,'outside tap dismisses action');
   assert.equal(await choice(phone,directID).locator('..').locator('.room-delete').count(),0,'default assistant stays protected');
  }
  assert.equal(deleteWrites,0);assert.deepEqual(errors,[]);
  console.log(JSON.stringify({independentContexts:2,catalogSyncMs:syncMs,unopenedCounts:true,mobileListDropsBookmarkKeepsMention:true,fullHistoryMentionEntry:true,oneBatchHistory:true,switchDiscardsLateHistory:true,catalogFailureIsolated:true,directAndRoomJumps:true,activityAvoided:true,shortViewportSimulated:true,twoRowCatalog:true,roomNameAvatars:true,touchLongPress:true,deleteRequiresConfirmation:true,viewports:[1440,390,320]}));
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
