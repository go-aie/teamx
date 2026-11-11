const {chromium}=require('playwright'),assert=require('node:assert/strict');
(async()=>{
 const endpoint=process.argv[2],browser=await chromium.launch({headless:true});
 try {
  const context=await browser.newContext({viewport:{width:1440,height:1000}}),page=await context.newPage(),errors=[];
  page.on('pageerror',e=>errors.push(e.message));page.setDefaultTimeout(15000);
  await page.goto(endpoint);
  if(process.argv[3]==='restore'){
   const before=await(await fetch(endpoint+'/v1/rooms')).json();assert.equal(before.default_assistant.status,'deleted');
   await page.getByRole('button',{name:'恢复默认助理（新建会话）',exact:true}).waitFor();
   assert.match(await page.locator('#default-assistant-notice').textContent(),/原消息、规则和订阅不会恢复/);
   await page.reload();assert.equal(await page.locator('#direct-list .room-choice').count(),0);
   await page.getByRole('button',{name:'恢复默认助理（新建会话）',exact:true}).click();
   await page.locator('#direct-list [data-default-assistant="true"] .room-choice[aria-current="true"]').waitFor();
   await page.locator('#activity-dialog[open]').waitFor();
   const after=await(await fetch(endpoint+'/v1/rooms')).json();assert.equal(after.default_assistant.status,'completed');assert.notEqual(after.default_assistant.room_instance,before.default_assistant.room_instance);
   assert.equal(after.rooms.length,1);assert.equal(await page.locator('#messages .message').count(),0);
   assert.equal(await page.locator('#direct-list [data-default-assistant="true"] .room-delete').count(),0);
   await page.reload();await page.locator('#direct-list [data-default-assistant="true"]').waitFor();
   const final=await(await fetch(endpoint+'/v1/rooms')).json();assert.equal(final.default_assistant.room_instance,after.default_assistant.room_instance);
   assert.deepEqual(errors,[]);console.log('Chromium passed: legacy deletion stays absent until explicit restore, fresh default selected once, no fake task or delete affordance.');return;
  }
  await page.locator('#direct-list .room-choice[aria-current="true"]').waitFor();
  assert.match(await page.locator('#direct-group-title').textContent(),/私信 · 1/);
  await page.locator('#activity-dialog[open]').waitFor();
  assert.equal(await page.locator('#close-activity').isVisible(),false);
  assert.equal(await page.locator('#member-list-view').isVisible(),false);
  assert.equal(await page.locator('#agent-tab-platforms').getAttribute('aria-selected'),'true');
  await page.locator('#agent-actions').getByRole('button',{name:/暂停/}).waitFor();
  await page.locator('#agent-actions').getByRole('button',{name:/暂停/}).click();
  await page.locator('#agent-actions').getByRole('button',{name:/启用协作/}).waitFor();
  await page.locator('#message-input').fill('DM draft');
  // Polling preserves selected tab, mounted node and focus.
  await page.locator('#agent-tab-inbox').click();await page.locator('#message-input').focus();
  await page.evaluate(()=>window.savedInbox=document.querySelector('#inbox-member-view'));
  await page.waitForTimeout(2300);
  assert.equal(await page.locator('#agent-tab-inbox').getAttribute('aria-selected'),'true');
  assert.equal(await page.evaluate(()=>document.activeElement===document.querySelector('#message-input')&&window.savedInbox===document.querySelector('#inbox-member-view')),true);
  await page.locator('#room-menu-label').click();await page.locator('#direct-notifications').click();
  await page.locator('#close-conversation-notifications').waitFor();
  assert.equal(await page.locator('#notification-sources-label').textContent(),'会话通知');
  assert.equal(await page.locator('#members').isVisible(),false);
  await page.locator('#close-conversation-notifications').click();
  assert.equal(await page.locator('#agent-tab-inbox').getAttribute('aria-selected'),'true');
  await page.locator('#room-list .room-choice').first().click();
  await page.waitForFunction(()=>document.querySelector('#message-input').placeholder.includes('发送到房间'));
  await page.locator('#member-list-view').waitFor();
  assert.match(await page.locator('#message-input').getAttribute('placeholder'),/发送到房间/);
  await page.locator('#message-input').fill('Room draft');await page.reload();
  await page.locator('#room-list .room-choice[aria-current="true"]').waitFor();
  assert.equal(await page.locator('#message-input').inputValue(),'Room draft');
  await page.locator('#direct-list .room-choice').first().click();
  await page.locator('#activity-dialog[open]').waitFor();
  assert.equal(await page.locator('#message-input').inputValue(),'DM draft');
  // Narrow view starts with chat; a single toggle opens full-height details.
  await page.setViewportSize({width:390,height:844});
  await page.locator('#member-rail').waitFor({state:'hidden'});
  assert.equal(await page.locator('#member-rail').isVisible(),false);
  assert.equal(await page.locator('#compose').evaluate(el=>el.getBoundingClientRect().bottom<=innerHeight),true);
  await page.getByRole('button',{name:'展开助理详情',exact:true}).click();
  await page.getByRole('button',{name:'返回私信',exact:true}).click();
  assert.equal(await page.locator('#member-rail').isVisible(),false);
  await page.getByRole('button',{name:'展开助理详情',exact:true}).focus();await page.keyboard.press('Enter');
  assert.equal(await page.evaluate(()=>document.activeElement.id),'close-activity');await page.keyboard.press('Escape');
  assert.equal(await page.evaluate(()=>document.activeElement.id),'toggle-members');
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
  await page.setViewportSize({width:1440,height:1000});
  // Explicit DM creation has no team picker and does not post a fake task.
  await page.locator('#new-room').click();await page.getByRole('button',{name:'新建私信',exact:true}).click();
  assert.equal(await page.locator('#team').isVisible(),false);
  await page.locator('#new-room-name').fill('另一位助理');await page.locator('#create-room').click();
  await page.locator('#room-name').getByText('另一位助理',{exact:true}).waitFor();
  assert.equal(await page.locator('#messages .message').count(),0);
  assert.equal(await page.locator('#message-input').inputValue(),'');
  await page.locator('#direct-suggestions button').getByText('连接飞书',{exact:true}).click();
  assert.equal(await page.locator('#message-input').inputValue(),'帮我连接飞书');
  assert.equal(await page.locator('#messages .message').count(),0);
  await page.locator('#direct-suggestions button').first().click();
  assert.match(await page.locator('#message-input').inputValue(),/今天/);
  assert.equal(await page.locator('#messages .message').count(),0);
  // A second tab sees the same single default, while this tab keeps its choice.
  const second=await context.newPage();await second.goto(endpoint);
  await second.locator('#direct-list [data-default-assistant="true"]').waitFor();
  assert.equal(await second.locator('#direct-list [data-default-assistant="true"]').count(),1);
  await page.reload();await page.locator('#room-name').getByText('另一位助理',{exact:true}).waitFor();
  // The default has no delete affordance and the server also rejects bypasses.
  assert.equal(await second.locator('#direct-list [data-default-assistant="true"] .room-delete').count(),0);
  const forbidden=await second.evaluate(async()=>{
   const catalog=await(await fetch('/v1/rooms')).json(),ref=catalog.default_assistant;
   const response=await fetch('/v1/rooms/delete',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({service_epoch:catalog.service_epoch,caller:{kind:'human',id:catalog.operator},room_id:ref.room_id,room_instance:ref.room_instance})});return response.status;
  });assert.equal(forbidden,409);
  // A manually created DM remains deletable from another tab.
  second.on('dialog',dialog=>dialog.accept());
  const manual=second.locator('#direct-list [data-default-assistant="false"]').filter({hasText:'另一位助理'});
  await manual.locator('.room-delete').click();await manual.waitFor({state:'detached'});
  await second.reload();await second.locator('#direct-list [data-default-assistant="true"]').waitFor();
  assert.equal(await second.locator('#direct-list [data-default-assistant="true"]').count(),1);
  if(process.env.TEAMX_DM_BROWSER_ARTIFACTS){const fs=require('node:fs/promises'),path=require('node:path');await fs.mkdir(process.env.TEAMX_DM_BROWSER_ARTIFACTS,{recursive:true});await page.screenshot({path:path.join(process.env.TEAMX_DM_BROWSER_ARTIFACTS,'desktop.png'),fullPage:true});await page.setViewportSize({width:390,height:844});await page.screenshot({path:path.join(process.env.TEAMX_DM_BROWSER_ARTIFACTS,'mobile.png'),fullPage:true});}
  assert.deepEqual(errors,[]);
  console.log('Chromium passed: DM/Room groups, protected default and HTTP rejection, manual DM deletion, direct inspector, polling focus/tab, notifications return, mobile and second tab.');
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
