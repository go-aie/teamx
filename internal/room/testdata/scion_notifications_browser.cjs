const {chromium}=require('playwright'),assert=require('node:assert/strict'),path=require('node:path');
(async()=>{
 const [endpoint,encoded,out]=process.argv.slice(2),input=JSON.parse(encoded),browser=await chromium.launch({headless:true});
 try {
  const context=await browser.newContext({viewport:{width:1440,height:1050}}),page=await context.newPage(),errors=[];
  page.on('pageerror',e=>errors.push(e.message));page.setDefaultTimeout(20000);
  await context.addInitScript(room=>sessionStorage.setItem('teamx.selected-room',JSON.stringify(room)),{room_id:input.room_id,room_instance:input.room_instance});
  await page.goto(endpoint);await page.locator('.member-summary').first().click();await page.locator('#agent-tab-platforms').click();
  if(input.mode==='initial'){
   await page.locator('#member-notification-add-toggle').click();
   await page.locator(`#member-notification-add-options [data-platform-id="${input.platform_id}"]`).hover();
   await page.locator(`#member-notification-add-options [data-platform-id="${input.platform_id}"] [data-action="settings"]`).click();
   await page.locator('#scion-repository').fill('OpenCSG/scion');await page.locator('#scion-backfill').selectOption('86400');
   const verify=page.waitForResponse(r=>r.url().endsWith('/v1/platform/notifications/verify'));await page.locator('#scion-verify').click();assert.equal((await verify).status(),200);
   const save=page.waitForResponse(r=>r.url().endsWith('/v1/platform/notifications/configure'));await page.locator('#scion-save').click();const result=await save;assert.equal(result.status(),200,await result.text());
  }else await page.getByRole('button',{name:'编辑 Scion 的通知设置',exact:true}).click();
  await page.locator('#scion-backfill-locked').waitFor({state:'visible'});
  assert.equal(await page.locator('#scion-repository').isDisabled(),true);assert.equal(await page.locator('#scion-repository').inputValue(),'OpenCSG/scion');
  assert.match(await page.locator('#notification-panel-workitems').textContent(),/不含评论、Review/);
  assert.equal(await page.locator('#notification-panel-gitlab').isVisible(),false);
  await page.screenshot({path:path.join(out,input.mode+'-desktop.png'),fullPage:true});
  await page.setViewportSize({width:390,height:844});
  await page.locator('#room-menu-label').click();await page.locator('#conversation-open-details').click();
  await page.locator('.member-summary').first().click();await page.locator('#agent-tab-platforms').click();
  await page.getByRole('button',{name:'编辑 Scion 的通知设置',exact:true}).click();
  await page.locator('#notification-panel-workitems').waitFor({state:'visible'});assert.equal(await page.locator('#notification-detail').evaluate(e=>e.scrollWidth>e.clientWidth+1),false);
  await page.screenshot({path:path.join(out,input.mode+'-mobile.png'),fullPage:true});assert.deepEqual(errors,[]);
  console.log(JSON.stringify({browser:'Chromium',mode:input.mode,desktop:true,mobile:true,real_api:true,page_errors:errors}));
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
