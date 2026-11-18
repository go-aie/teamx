const {chromium}=require('playwright'),assert=require('node:assert/strict');
(async()=>{
 const [endpoint,id,room,membership]=process.argv.slice(2),browser=await chromium.launch({headless:true});
 try {
  const context=await browser.newContext({viewport:{width:1440,height:1000}}),page=await context.newPage(),errors=[];page.on('pageerror',e=>errors.push(e.message));page.setDefaultTimeout(15000);
  const catalog=await(await fetch(endpoint+'/v1/rooms')).json(),current=catalog.rooms.find(r=>r.room_instance===room);
  await context.addInitScript(selection=>sessionStorage.setItem('teamx.selected-room',JSON.stringify(selection)),{room_id:current.room_id,room_instance:room});await page.goto(endpoint);
  const card=page.locator(`#messages [data-interaction-id="${id}"]`);await card.waitFor();await card.getByText('Project · team/project · #7',{exact:true}).waitFor();
  await card.evaluate(el=>window.originalProjectCard=el);await card.getByRole('button',{name:'加载更多',exact:true}).click();
  const row=card.locator('.platform-selection-candidates label').filter({hasText:'other/project'});await row.locator('input').check();await card.locator('select').selectOption('note');
  await page.locator('#user-menu-toggle').click();await page.locator('#open-settings').click();await page.locator('#settings-tab-platforms').click();await page.locator("#platforms-list-view").waitFor({state:"visible"});assert.equal(await page.locator("#settings-dialog [data-interaction-id]").count(),0);await page.locator('#close-settings').click();
  assert.equal(await card.evaluate(el=>el===window.originalProjectCard),true);assert.match(await card.textContent(),/other\/project.*#8/);
  // Global settings leave loaded pages and the local selection in the conversation.
  assert.equal(await card.locator(".platform-selection-candidates label").count(),2);assert.equal(await row.locator("input").isChecked(),true);
  await page.setViewportSize({width:390,height:844});await page.waitForFunction(()=>document.documentElement.scrollWidth<=innerWidth);
  const submitted=page.waitForResponse(r=>r.url().endsWith('/v1/platforms/selection-interactions/submit'));
  await card.getByRole('button',{name:'提交选择',exact:true}).click();await card.getByText('选择已保存',{exact:true}).waitFor();
  const receipt=await(await submitted).json();assert.ok(receipt.continuation.room_seq>receipt.source_seq);
  const event=page.locator(`#messages > #message-${receipt.continuation.room_seq}.room-event`);
  await event.waitFor();assert.equal(await event.textContent(),'选择结果已更新');
  assert.deepEqual(await event.evaluate(el=>({align:getComputedStyle(el).textAlign,bubble:el.classList.contains('message'),avatar:!!el.querySelector('.avatar')})),{align:'center',bubble:false,avatar:false});
  await page.reload();await card.getByText('选择已保存',{exact:true}).waitFor();assert.match(await card.textContent(),/other\/project.*#8/);
  await event.waitFor();
  await card.getByText(/选择结果已保存，等待 Agent 接续/).waitFor();
  await page.setViewportSize({width:1440,height:1000});
  const openMember=()=>page.locator(`#members .member-summary[data-activity-member="${membership}"]`).click();
  const attention=page.locator('#member-notification-platforms select[aria-label$=" 关注级别"]');
  await openMember();await attention.waitFor();assert.equal(await attention.inputValue(),'normal');
  const save=page.waitForResponse(r=>r.url().endsWith('/v1/member/input-attention') && r.request().postDataJSON().attention==='high');
  await attention.selectOption('high');assert.equal((await save).status(),200);
  await page.reload();await card.waitFor();await openMember();
  await page.waitForFunction(()=>document.querySelector('#member-notification-platforms select')?.value==='high');
  await page.locator('#agent-tab-inbox').click();
  const entries=page.locator('#inbox-entries > section[data-inbox-seq="1"]');
  await entries.filter({hasText:'GitLab partition evidence'}).waitFor();
  await entries.filter({hasText:'Feishu partition evidence'}).waitFor();
  assert.equal(await entries.count(),2);assert.deepEqual(errors,[]);
  console.log('PASS: selection, centered Room event before Agent processing, attention persisted on reload, and separate platform entries with the same sequence; platform data simulated.');
 } finally {await browser.close();}
})().catch(e=>{console.error(e.message);process.exitCode=1;});
