// Real Chromium + TeamX HTTP, synthetic official Feishu CLI in the Go harness.
const {chromium}=require('playwright'),assert=require('node:assert/strict'),{mkdir}=require('node:fs/promises'),{join}=require('node:path');
(async()=>{
 const [endpoint,accountID,groupID,platformID,room]=process.argv.slice(2),browser=await chromium.launch({headless:true});
 const artifacts=process.env.TEAMX_SELECTION_BROWSER_ARTIFACTS;
 if(artifacts)await mkdir(artifacts,{recursive:true});
 try {
  const context=await browser.newContext({viewport:{width:1440,height:1100}}),page=await context.newPage(),errors=[];
  page.on('pageerror',e=>errors.push(e.message));page.setDefaultTimeout(12000);
  const catalog=await (await fetch(endpoint+'/v1/rooms')).json(),current=catalog.rooms.find(r=>r.room_instance===room);
  await context.addInitScript(selection=>sessionStorage.setItem('teamx.selected-room',JSON.stringify(selection)),{room_id:current.room_id,room_instance:room});
  await page.goto(endpoint);
  if(current.is_direct){
   await page.locator('#direct-list .room-choice[aria-current="true"]').waitFor();
   assert.equal(catalog.default_assistant.room_instance,room);
   assert.equal(await page.locator('#member-list-view').isVisible(),false);
   await page.locator('#agent-tab-platforms').waitFor();
  }
  const account=page.locator(`#messages [data-interaction-id="${accountID}"]`),group=page.locator(`#messages [data-interaction-id="${groupID}"]`);
  await account.waitFor();await account.locator('.platform-selection-candidates label').first().waitFor();
  if(artifacts)await page.screenshot({path:join(artifacts,'forms.png'),fullPage:true,animations:'disabled'});
  assert.equal(await account.locator('.platform-selection-candidates label').count(),2);
  await account.locator('.platform-selection-candidates input').first().check();await account.getByRole('button',{name:'提交选择',exact:true}).click();
  await account.getByText('选择已保存',{exact:true}).waitFor();
  await group.locator('.platform-selection-candidates label').filter({hasText:'oc_group_two'}).waitFor();
  await group.getByRole('button',{name:'加载更多',exact:true}).click();await group.locator('.platform-selection-candidates label').filter({hasText:'oc_group_three'}).waitFor();
  // Dirty input/focus/node identity survives a new real Room message and polling.
  const query=group.locator('.platform-selection-fields input');await query.fill('本地输入');await query.focus();
  await group.evaluate(el=>{window.originalSelectionCard=el;window.originalSelectionInput=el.querySelector('.platform-selection-fields input');});
  await page.evaluate(async({service,operator})=>{const response=await fetch('/v1/send',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({...service,caller:{kind:'human',id:operator},client_message_id:'selection-browser-new-message',text:'正在选择群聊'})});if(!response.ok)throw Error(await response.text());},{service:current.service,operator:catalog.operator});
  await page.locator('#messages').getByText('正在选择群聊',{exact:true}).waitFor();
  assert.equal(await group.evaluate(el=>el===window.originalSelectionCard && el.querySelector('.platform-selection-fields input')===window.originalSelectionInput && document.activeElement===window.originalSelectionInput),true);
  assert.equal(await query.inputValue(),'本地输入');
  await group.locator('.platform-selection-candidates label').filter({hasText:'oc_group_one'}).locator('input').check();
  // Global settings show shared platforms and leave the conversation selection intact.
  await group.getByRole('button',{name:'完整设置',exact:true}).click();await page.locator("#platforms-list-view").waitFor({state:"visible"});assert.equal(await page.locator("#settings-dialog [data-interaction-id]").count(),0);await page.locator('#close-settings').click();
  if(current.is_direct){
   assert.equal(await page.locator('#member-list-view').isVisible(),false);
   assert.equal(await page.locator('#agent-tab-platforms').getAttribute('aria-selected'),'true');
  }
  const second=await context.newPage();await second.goto(endpoint);
  const other=second.locator(`#messages [data-interaction-id="${groupID}"]`);
  await other.locator('.platform-selection-candidates label').filter({hasText:'oc_group_two'}).locator('input').check();
  await other.getByRole('button',{name:'提交选择',exact:true}).click();await other.getByText('选择已保存',{exact:true}).waitFor();
  // A stale tab cannot replace the immutable submitted target.
  const stale=await page.evaluate(async({service,operator,id})=>{const r=await fetch('/v1/platforms/selection-interactions/submit',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({...service,caller:{kind:'human',id:operator},operation_id:id,version:1})});return r.status;},{service:current.service,operator:catalog.operator,id:groupID});assert.equal(stale,409);
  await page.reload();await group.getByText('选择已保存',{exact:true}).waitFor();
  assert.match(await group.textContent(),/已选 1 个群/);
  if(artifacts)await page.screenshot({path:join(artifacts,'selected.png'),fullPage:true,animations:'disabled'});
  await page.setViewportSize({width:390,height:844});
  if(artifacts)await page.screenshot({path:join(artifacts,'mobile.png'),fullPage:true,animations:'disabled'});
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth),true);
  assert.deepEqual(errors,[]);console.log('Chromium: account/group choices, pagination, stable input/focus, settings return, stale tab, refresh and mobile passed.');
 } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
