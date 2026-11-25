const {toggleConversationDetails}=require('./conversation_details.cjs');
const {chromium}=require('playwright'),assert=require('node:assert/strict');
(async()=>{
 const endpoint=process.argv[2],browser=await chromium.launch({headless:true});
 try {
  const context=await browser.newContext({viewport:{width:1440,height:1000}}),page=await context.newPage(),errors=[];
  // This fixture hosts one manager directly. Normalize the default Space URL
  // alias to that manager; all requests still use its real access middleware.
  const managerURL=value=>{const url=new URL(value);url.pathname=url.pathname.replace(/^\/s\/[a-f0-9]{32}/,'');return url.href;};
  await context.route(/\/s\/[a-f0-9]{32}\//,route=>route.continue({url:managerURL(route.request().url())}));
  page.on('pageerror',e=>errors.push(e.message));page.setDefaultTimeout(15000);
  await page.addInitScript(()=>{
   window.directMemberListFrames=0;
   const sample=()=>{const workspace=document.querySelector('#workspace'),rail=document.querySelector('#member-rail'),members=document.querySelector('#members');
    if(workspace?.classList.contains('direct-conversation') && rail && !rail.inert && members?.getClientRects().length && getComputedStyle(members).visibility!=='hidden')window.directMemberListFrames++;
    requestAnimationFrame(sample);
   };requestAnimationFrame(sample);
  });
  await page.goto(endpoint);
  const openMemberDetails=async()=>{
   if(await page.locator('#member-sidebar').getAttribute('aria-hidden')==='true')await toggleConversationDetails(page);
   await page.locator('#agent-controls').waitFor();
  };
  if(process.argv[3]==='restore'){
   const before=await(await fetch(endpoint+'/v1/rooms')).json();assert.equal(before.default_assistant.status,'deleted');
   await page.getByRole('button',{name:'恢复默认助理（新建会话）',exact:true}).waitFor();
   assert.match(await page.locator('#default-assistant-notice').textContent(),/原消息、规则和订阅不会恢复/);
   await page.reload();assert.equal(await page.locator('#room-list [data-direct="true"] .room-choice').count(),0);
   await page.getByRole('button',{name:'恢复默认助理（新建会话）',exact:true}).click();
   await page.locator('#room-list [data-default-assistant="true"] .room-choice[aria-current="true"]').waitFor();
   await page.locator('#agent-controls').waitFor();
   const after=await(await fetch(endpoint+'/v1/rooms')).json();assert.equal(after.default_assistant.status,'completed');assert.notEqual(after.default_assistant.room_instance,before.default_assistant.room_instance);
   assert.equal(after.rooms.length,1);assert.equal(await page.locator('#messages .message').count(),0);
   assert.equal(await page.locator('#room-list [data-default-assistant="true"] .room-delete').count(),0);
   await page.reload();await page.locator('#room-list [data-default-assistant="true"]').waitFor();
   const final=await(await fetch(endpoint+'/v1/rooms')).json();assert.equal(final.default_assistant.room_instance,after.default_assistant.room_instance);
   assert.deepEqual(errors,[]);console.log('Chromium passed: legacy deletion stays absent until explicit restore, fresh default selected once, no fake task or delete affordance.');return;
  }
  assert.equal(await page.locator('#direct-notifications').count(),0);
  await page.locator('#room-list [data-direct="true"] .room-choice[aria-current="true"]').waitFor();
  assert.equal(await page.locator('#room-list [data-direct="true"]').count(),1);
  await openMemberDetails();
  // A cold page holds member readiness while the private settings stay open.
  const cold=await context.newPage();let releaseMembers;
  const membersReady=new Promise(resolve=>{releaseMembers=resolve;});
  await cold.route('**/v1/member/list',async route=>{await membersReady;await route.fallback();});
  await cold.goto(endpoint,{waitUntil:'commit'});
  if(await cold.locator('#member-sidebar').getAttribute('aria-hidden')==='true')await toggleConversationDetails(cold);
  await cold.locator('#direct-settings-loading').waitFor();
  assert.equal(await cold.locator('#members').isVisible(),false);
  assert.equal(await cold.locator('#activity-dialog').evaluate(el=>el.open),false);
  releaseMembers();await cold.locator('#agent-controls').waitFor();
  assert.equal(await cold.locator('#direct-settings-loading').isVisible(),false);
  assert.equal(await cold.locator('#activity-title').innerText(),'设置');
  await cold.close();
  // Escape dismisses a nested picker before its parent settings dialog.
  await page.locator('#user-menu-toggle').click();await page.locator('#open-settings').click();await page.locator('#settings-tab-general').click();
  await page.locator('#timezone-picker-toggle').click();await page.keyboard.press('Escape');
  assert.equal(await page.locator('#settings-dialog').evaluate(el=>el.open),true);
  assert.equal(await page.locator('#timezone-options').isVisible(),false);
  await page.keyboard.press('Escape');
  assert.equal(await page.locator('#settings-dialog').evaluate(el=>el.open),false);
  assert.equal(await page.locator('#close-activity').isVisible(),true);
  assert.equal(await page.locator('#member-list-view').isVisible(),false);
  assert.equal(await page.locator('#agent-tab-profile').getAttribute('aria-selected'),'true');
  assert.deepEqual(await page.locator('#agent-tabs button').allTextContents(),['档案','平台','通知','规则','活动']);
  assert.equal(await page.locator('#activity-title').innerText(),'设置');
  assert.equal(await page.locator('#activity-dialog > header #activity-avatar').count(),0);
  assert.equal(await page.locator('#agent-controls #activity-avatar').isVisible(),true);
  await page.locator('#agent-actions').getByRole('button',{name:/暂停/}).waitFor();
  await page.locator('#agent-actions').getByRole('button',{name:/暂停/}).click();
  await page.locator('#agent-actions').getByRole('button',{name:/启用/}).waitFor();
  const layouts=[];
  for(const viewport of [{width:1440,height:1000},{width:390,height:844},{width:320,height:568},{width:844,height:390}]) {
   await page.setViewportSize(viewport);
   await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
   if(!await page.locator('#member-rail').isVisible())await toggleConversationDetails(page);
   if(!await page.locator('#activity-dialog').evaluate(el=>el.open))await openMemberDetails();
   await page.locator('#agent-tab-profile').click();
   const before=await page.locator('#agent-tabs').boundingBox();
   await page.locator('#agent-controls').evaluate(el=>{el.scrollTop=el.scrollHeight;});
   const metrics=await page.evaluate(()=>{
    const header=document.querySelector('#activity-dialog .activity-header').getBoundingClientRect(),tabs=document.querySelector('#agent-tabs').getBoundingClientRect();
    return {headerBottom:header.bottom,tabTop:tabs.top,tabBottom:tabs.bottom,overflow:document.documentElement.scrollWidth-innerWidth,
     allTabsVisible:[...document.querySelectorAll('#agent-tabs button')].every(button=>{const r=button.getBoundingClientRect();return r.left>=0 && r.right<=innerWidth && r.bottom<=innerHeight;})};
   });
   assert(Math.abs(metrics.tabTop-before.y)<1,JSON.stringify(metrics));
   assert(metrics.tabTop-metrics.headerBottom<2 && metrics.allTabsVisible && metrics.overflow<=1,JSON.stringify({viewport,metrics}));
   assert.equal(await page.locator('#agent-actions').isVisible(),true);
   if(process.env.TEAMX_DM_BROWSER_ARTIFACTS){const fs=require('node:fs/promises'),path=require('node:path');await fs.mkdir(process.env.TEAMX_DM_BROWSER_ARTIFACTS,{recursive:true});await page.locator('#member-rail').screenshot({path:path.join(process.env.TEAMX_DM_BROWSER_ARTIFACTS,`profile-${viewport.width}x${viewport.height}.png`)});}
   await page.locator('#agent-tab-platforms').click();assert.equal(await page.locator('#agent-controls').isVisible(),false);
   assert.equal(await page.locator('#activity-avatar').isVisible(),false);assert.equal(await page.locator('#agent-actions').isVisible(),false);
   await page.locator('#agent-tab-platforms').press('End');assert.equal(await page.locator('#agent-tab-activity').getAttribute('aria-selected'),'true');
   await page.locator('#agent-tab-activity').press('Home');assert.equal(await page.locator('#agent-tab-profile').getAttribute('aria-selected'),'true');
   layouts.push({viewport,...metrics});
  }
  await page.setViewportSize({width:1440,height:1000});
  await page.locator('#message-input').fill('DM draft');
  // Polling preserves selected tab, mounted node and focus.
  await page.locator('#agent-tab-inbox').click();await page.locator('#message-input').focus();
  await page.evaluate(()=>window.savedInbox=document.querySelector('#inbox-member-view'));
  await page.waitForTimeout(2300);
  assert.equal(await page.locator('#agent-tab-inbox').getAttribute('aria-selected'),'true');
  assert.equal(await page.evaluate(()=>document.activeElement===document.querySelector('#message-input')&&window.savedInbox===document.querySelector('#inbox-member-view')),true);
  await page.locator('#room-list [data-direct="false"] .room-choice').first().click();
  await page.waitForFunction(()=>document.querySelector('#message-input').placeholder==='发送给 '+document.querySelector('#room-name').textContent.trim());
  await page.locator('#member-list-view').waitFor();
  assert.match(await page.locator('#message-input').getAttribute('placeholder'),/^发送给 /);
  assert.equal(await page.locator('#notification-sources').count(),0);
  await page.locator('#message-input').fill('Room draft');await page.reload();
  await page.locator('#room-list .room-choice[aria-current="true"]').waitFor();
  assert.equal(await page.locator('#message-input').inputValue(),'Room draft');
  await page.locator('#room-list [data-direct="true"] .room-choice').first().click();
  await openMemberDetails();
  assert.equal(await page.locator('#message-input').inputValue(),'DM draft');
  // Narrow view starts with chat; the conversation menu opens full-height details.
  await page.setViewportSize({width:390,height:844});
  await page.locator('#member-rail').waitFor({state:'hidden'});
  assert.equal(await page.locator('#member-rail').isVisible(),false);
  assert.equal(await page.locator('#compose').evaluate(el=>el.getBoundingClientRect().bottom<=innerHeight),true);
  await toggleConversationDetails(page);
  await openMemberDetails();
  await page.locator('#close-activity').click();
  await page.locator('#member-rail').waitFor({state:'hidden'});
  await page.locator('#room-menu-label').focus();await page.keyboard.press('Enter');await page.locator('#conversation-open-details').click();
  await openMemberDetails();await page.keyboard.press('Escape');
  await page.locator('#member-rail').waitFor({state:'hidden'});
  await page.waitForFunction(()=>document.activeElement.id==='room-menu-label');
  assert.equal(await page.evaluate(()=>document.activeElement.id),'room-menu-label');
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
  await page.setViewportSize({width:1440,height:1000});
  // Explicit DM creation has no team picker and does not post a fake task.
  await page.locator('#new-room').click();await page.getByRole('menuitem',{name:'新建私信',exact:true}).click();
  assert.equal(await page.locator('#team').isVisible(),false);
  await page.locator('#new-room-name').fill('另一位助理');await page.locator('#create-room').click();
  await page.locator('#room-name').getByText('另一位助理',{exact:true}).waitFor();
  assert.equal(await page.locator('#messages .message').count(),0);
  assert.equal(await page.locator('#message-input').inputValue(),'');
  assert.equal(await page.locator('#empty > p').textContent(),'梳理待办、总结信息、起草内容；接入飞书或 GitLab，还能跟进消息和项目进展。');
  assert.deepEqual(await page.locator('#direct-suggestions button').allTextContents(),['你能帮我做什么？','整理待办']);
  assert.deepEqual(await page.locator('#platform-suggestions button').allTextContents(),['接入飞书','接入 GitLab']);
  await page.locator('#direct-suggestions button').first().click();
  assert.equal(await page.locator('#message-input').inputValue(),'介绍一下你能帮我做什么');
  assert.equal(await page.locator('#messages .message').count(),0);
  await page.locator('#direct-suggestions button').last().click();
  assert.match(await page.locator('#message-input').inputValue(),/待办/);
  assert.equal(await page.locator('#messages .message').count(),0);
  // A shared account does not imply this new assistant has notifications/tools.
  let larkValidation='valid';
  await page.route('**/v1/platforms?*',async route=>{
   const response=await route.fetch({url:managerURL(route.request().url())}),data=await response.json();
   data.platforms.push({id:'suggestion-lark',provider:'lark',name:'工作飞书',validation:{status:larkValidation}});
   try {
    await route.fulfill({response,json:data});
   } catch(error) {
    if(!String(error).includes('Route is already handled'))throw error;
   }
  });
  await page.reload();
  await page.locator('#platform-suggestions').waitFor();
  assert.deepEqual(await page.locator('#direct-suggestions button').allTextContents(),['你能帮我做什么？','整理待办']);
  assert.deepEqual(await page.locator('#platform-suggestions button').allTextContents(),['接入飞书','接入 GitLab']);
  assert.equal(await page.locator('#message-input').inputValue(),'帮我整理一下待办事项');
  await page.locator('#onboard-lark').click();
  assert.equal(await page.locator('#message-input').inputValue(),'帮我整理一下待办事项');
  assert.match(await page.locator('#send-error').textContent(),/未发送/);
  assert.equal(await page.locator('#messages .message').count(),0);
  larkValidation='failed';
  await page.reload();
  await page.locator('#platform-suggestions').waitFor();
  assert.equal(await page.locator('#message-input').inputValue(),'帮我整理一下待办事项');
  await page.unrouteAll({behavior:'wait'});
  await page.locator('#message-input').fill('');
  for(const viewport of [{width:1440,height:1000},{width:390,height:844}]){
   await page.setViewportSize(viewport);
   assert.equal(await page.locator('#platform-suggestions').isVisible(),true);
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
   if(process.env.TEAMX_DM_BROWSER_ARTIFACTS){const fs=require('node:fs/promises'),path=require('node:path');await fs.mkdir(process.env.TEAMX_DM_BROWSER_ARTIFACTS,{recursive:true});await page.screenshot({path:path.join(process.env.TEAMX_DM_BROWSER_ARTIFACTS,`welcome-${viewport.width}.png`),fullPage:true});}
  }
  await page.locator('#onboard-gitlab').click();
  await page.waitForFunction(()=>document.querySelector('#platform-suggestions').hidden);
  assert.match(await page.locator('#messages').textContent(),/帮我接入GitLab/);
  assert.equal(await page.locator('#settings-dialog').isVisible(),false);
  await page.setViewportSize({width:1440,height:1000});
  // A second tab sees the same single default, while this tab keeps its choice.
  const second=await context.newPage();await second.goto(endpoint);
  await second.locator('#room-list [data-default-assistant="true"]').waitFor();
  assert.equal(await second.locator('#room-list [data-default-assistant="true"]').count(),1);
  await page.reload();await page.locator('#room-name').getByText('另一位助理',{exact:true}).waitFor();
  // The default has no delete affordance and the server also rejects bypasses.
  assert.equal(await second.locator('#room-list [data-default-assistant="true"] .room-delete').count(),0);
  const forbidden=await second.evaluate(async()=>{
   const catalog=await(await fetch('/v1/rooms')).json(),ref=catalog.default_assistant;
   const response=await fetch('/v1/rooms/delete',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({service_epoch:catalog.service_epoch,caller:{kind:'human',id:catalog.operator},room_id:ref.room_id,room_instance:ref.room_instance})});return response.status;
  });assert.equal(forbidden,409);
  // A manually created DM remains deletable from another tab.
  const manual=second.locator('#room-list [data-default-assistant="false"]').filter({hasText:'另一位助理'});
  await manual.locator('.room-delete').click();
  assert.equal(await second.evaluate(()=>document.activeElement.id),'delete-dismiss');
  await second.keyboard.press('Escape');assert.equal(await manual.isVisible(),true);
  await manual.locator('.room-delete').click();await second.locator('#delete-submit').click();await manual.waitFor({state:'detached'});
  await second.reload();await second.locator('#room-list [data-default-assistant="true"]').waitFor();
  assert.equal(await second.locator('#room-list [data-default-assistant="true"]').count(),1);
  if(process.env.TEAMX_DM_BROWSER_ARTIFACTS){const fs=require('node:fs/promises'),path=require('node:path');await fs.mkdir(process.env.TEAMX_DM_BROWSER_ARTIFACTS,{recursive:true});await page.screenshot({path:path.join(process.env.TEAMX_DM_BROWSER_ARTIFACTS,'desktop.png'),fullPage:true});await page.setViewportSize({width:390,height:844});await page.screenshot({path:path.join(process.env.TEAMX_DM_BROWSER_ARTIFACTS,'mobile.png'),fullPage:true});}
  assert.deepEqual(errors,[]);
  assert.equal(await page.evaluate(()=>window.directMemberListFrames),0);
  console.log(JSON.stringify({layouts,direct_member_list_frames:0,cold_settings_loading:true}));
  console.log('Chromium passed: unified conversations, protected default and HTTP rejection, manual DM deletion, direct inspector, polling focus/tab, member platform entry, mobile and second tab.');
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
