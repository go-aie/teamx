const {chromium}=require('playwright'),assert=require('node:assert/strict'),path=require('node:path');
(async()=>{
 const [endpoint,encoded,out]=process.argv.slice(2),input=JSON.parse(encoded),browser=await chromium.launch({headless:true});
 try{
  const context=await browser.newContext({viewport:{width:1440,height:1050}}),page=await context.newPage(),errors=[];
  page.on('pageerror',e=>errors.push(e.message));page.setDefaultTimeout(20000);
  await context.addInitScript(room=>sessionStorage.setItem('teamx.selected-room',JSON.stringify(room)),{room_id:input.room_id,room_instance:input.room_instance});
  await page.goto(endpoint);
  if(input.mode==='login'){
   await page.locator('#user-menu-toggle').click();await page.locator('#open-settings').click();await page.locator('#settings-tab-platforms').click();await page.locator('#connection-new').click();
   await page.locator('#connection-provider').selectOption('scion');await page.locator('#connection-url').fill('https://scion.opencsg.com');await page.locator('#connection-url').blur();
   await page.locator('#connection-scion-login-open').click();await page.locator('#connection-scion-pat').waitFor({state:'visible'});
   assert.equal(await page.locator('#connection-scion-pat').getAttribute('type'),'password');
  }else if(input.mode==='select'){
   const card=page.locator(`#messages [data-interaction-id="${input.operation_id}"]`);
   await card.getByText('选择 Scion 账号',{exact:true}).waitFor();
   await card.locator(`.platform-selection-candidates input[value="${input.platform_id}"]`).waitFor();
   assert.equal(await card.locator('.platform-selection-fields input:visible').count(),0);
   await page.screenshot({path:path.join(out,'account-before.png'),fullPage:true});
   await page.setViewportSize({width:390,height:844});await page.screenshot({path:path.join(out,'account-mobile.png'),fullPage:true});await page.setViewportSize({width:1440,height:1050});
   await card.locator(`.platform-selection-candidates input[value="${input.platform_id}"]`).check();
   await card.getByRole('button',{name:'提交选择',exact:true}).click();await card.getByText('选择已保存',{exact:true}).waitFor();
  }else{
   await page.locator('#agent-tab-platforms').click();
   await page.getByRole('button',{name:'编辑 Scion 的通知设置',exact:true}).click();
   await page.locator('#scion-backfill-locked').waitFor({state:'visible'});
   assert.equal(await page.locator('#scion-repository').inputValue(),'OpenCSG/scion');assert.equal(await page.locator('#scion-repository').isDisabled(),true);
  }
  await page.screenshot({path:path.join(out,input.mode+'-desktop.png'),fullPage:true});assert.deepEqual(errors,[]);
  console.log(JSON.stringify({status:'passed',mode:input.mode,page_errors:errors}));
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
