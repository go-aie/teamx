const {chromium}=require('playwright'),assert=require('node:assert/strict');
const {mkdirSync}=require('node:fs'),{join}=require('node:path');
const [endpoint,directID,groupID,brokenID]=process.argv.slice(2);
const artifacts=process.env.TEAMX_HUMAN_READ_ARTIFACTS;
if(artifacts)mkdirSync(artifacts,{recursive:true});
(async()=>{
 const browser=await chromium.launch({headless:true}),errors=[];
 try {
  const context=await browser.newContext({viewport:{width:1440,height:1000}}),desktop=await context.newPage();
  const phoneContext=await browser.newContext({viewport:{width:390,height:844}}),phone=await phoneContext.newPage();
  for(const page of [desktop,phone]){page.on('pageerror',e=>errors.push(e.message));page.setDefaultTimeout(12000);}
  const catalog=async()=>{const response=await fetch(endpoint+'/v1/rooms');assert.equal(response.status,200);return response.json();};
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
  assert.equal(await choice(desktop,groupID).locator('.room-mention').textContent(),'有人 @你');
  assert.equal(await choice(desktop,brokenID).locator('.room-read-unavailable').textContent(),'暂未同步');
  assert.equal(await choice(desktop,directID).locator('.room-mention').count(),0);
  const before=await read(groupID);
  await phone.goto(endpoint);await phone.locator('#mobile-back-to-rooms').click();
  await choice(phone,groupID).click();await phone.locator('#message-120').waitFor();
  await phone.bringToFront();await phone.locator('#message-120').scrollIntoViewIfNeeded();
  await waitFor(async()=> (await read(groupID)).revision>before.revision);
  const changed=await read(groupID),syncStart=Date.now();
  await waitFor(async()=> (await choice(desktop,groupID).getAttribute('aria-label')).includes(`${changed.unread_count} 条未读消息`));
  const syncMs=Date.now()-syncStart;assert.ok(syncMs<3000,`catalog sync ${syncMs}ms`);
  assert.equal(await desktop.locator('#message-120').count(),1,'desktop remains in its original conversation');
  // Open the group on desktop. Reading changes must not create a new latest
  // message, reorder rows or consume unrendered history.
  await desktop.locator('#settings-dialog').evaluate(el=>el.close());await choice(desktop,groupID).click();
  await desktop.locator('#message-120').waitFor();await desktop.locator('#earlier-mentions').waitFor({state:'visible'});
  assert.equal(await desktop.locator('#earlier-mentions').textContent(),'更早还有 2 条提及');
  await desktop.locator('#load-history').click();await desktop.locator('#message-21').waitFor();
  assert.equal(await desktop.locator('#message-1').count(),0,'history loads only one earlier batch');
  await desktop.locator('#earlier-mentions').waitFor({state:'visible'});
  assert.equal(await desktop.locator('#earlier-mentions').textContent(),'更早还有 1 条提及');
  // Verify retained last values under a per-row catalog failure; other rows and
  // the normal member/message/composer path continue refreshing.
  const last=await choice(desktop,groupID).locator('.room-unread').textContent();
  await desktop.route('**/v1/rooms',async route=>{
   const response=await route.fetch(),body=await response.json();
   body.rooms.find(r=>r.room_id===groupID).human_read={status:'unavailable'};
   await route.fulfill({response,json:body});
  });
  await choice(desktop,groupID).locator('.room-read-unavailable').waitFor();
  assert.equal(await choice(desktop,groupID).locator('.room-unread').textContent(),last);
  await desktop.locator('#message-input').fill('目录阅读故障期间保留草稿');assert.equal(await desktop.locator('#send').isEnabled(),true);
  assert.equal(await desktop.locator('#connection').textContent(),'服务已连接');
  await desktop.unroute('**/v1/rooms');
  // Isolate layout checks from the dwell timer, then exercise both conversation
  // types with real new-message polling and a controlled activity response.
  for(const page of [desktop,phone]){
   await page.evaluate(()=>{document.hasFocus=()=>false;window.dispatchEvent(new Event('blur'));});
   await page.route('**/v1/member/list',async route=>{
    const response=await route.fetch(),body=await response.json();
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
    const sent=await fetch(endpoint+'/v1/send',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({...target.service,caller:{kind:'human',id:data.operator},client_message_id:`catalog-layout-${id}-${width}`,text:'检查双按钮与执行提示避让。'})});
    assert.equal(sent.status,200);
    await page.locator('#jump-latest').waitFor({state:'visible'});await page.locator('#view-mention').waitFor({state:'visible'});
    await page.locator('#execution-hint').waitFor({state:'visible'});
    const mention=await page.locator('#view-mention').boundingBox(),latest=await page.locator('#jump-latest').boundingBox();
    const activity=await page.locator('#execution-hint').boundingBox(),input=await page.locator('#compose').boundingBox();
    assert.ok(mention.height>=44 && latest.height>=44 && mention.y+mention.height<=latest.y && latest.y+latest.height<=activity.y && activity.y+activity.height<=input.y);
    assert.match(await page.locator('#view-mention').getAttribute('aria-label'),/提及你.*reviewer/);
    if(artifacts)await page.screenshot({path:join(artifacts,`jumps-${id===directID?'direct':'room'}-${width}.png`)});
   }
  }
  // Exercise long labels, 99+, defaults and room mentions in the real layout.
  for(const page of [desktop,phone]){
   await page.route('**/v1/rooms',async route=>{
    const response=await route.fetch(),body=await response.json();
    for(const r of body.rooms){r.name='很长的会话名称 · '+r.name+' · 用来检查默认助理时间摘要和角标布局';if(r.human_read.status==='ready')r.human_read.unread_count=123;}
    await route.fulfill({response,json:body});
   });
  }
  for(const width of [1440,390,320]){
   const page=width===1440?desktop:phone;
   if(width!==1440){await page.setViewportSize({width,height:844});await page.locator('#mobile-back-to-rooms').click();}
   await waitFor(async()=> (await page.locator('.room-choice strong').first().textContent()).startsWith('很长'));
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
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
  assert.deepEqual(errors,[]);
  console.log(JSON.stringify({independentContexts:2,catalogSyncMs:syncMs,unopenedCounts:true,earlierMentions:true,oneBatchHistory:true,catalogFailureIsolated:true,directAndRoomJumps:true,activityAvoided:true,shortViewportSimulated:true,viewports:[1440,390,320]}));
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
