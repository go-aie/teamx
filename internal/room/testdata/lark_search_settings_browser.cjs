const {chromium}=require('playwright');
const assert=require('node:assert/strict');
(async()=>{
 const browser=await chromium.launch({headless:true,channel:process.env.TEAMX_BROWSER_CHANNEL || undefined});
 try{
  const page=await browser.newPage({viewport:{width:1440,height:1000}}),errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  const open=async()=>{
   await page.locator('.member-summary').first().click();await page.locator('#agent-tab-platforms').click();
   await page.getByRole('button',{name:'编辑 飞书 的通知设置',exact:true}).click();
   await page.waitForFunction(()=>!document.getElementById('lark-fields').disabled);
   await page.locator('#lark-groups-title').click();
  };
  await page.goto(process.argv[2]);await open();
  for(const id of ['lark-scope','lark-private-enabled','lark-groups-enabled','lark-groups-scope','lark-group','lark-all'])assert.equal(await page.locator('#'+id).isChecked(),true);
  assert.equal(await page.locator('#lark-save').isDisabled(),true);
  assert.equal(await page.locator('#lark-groups-chat-selection').isVisible(),false);
  await page.locator('#lark-groups-scope').uncheck();
  assert.equal(await page.locator('#lark-save').isDisabled(),true);
  await page.locator('#lark-groups-chat-picker-summary').click();
  for(const id of ['oc_group','oc_2','oc_3','oc_4','oc_5'])await page.locator(`#lark-groups-chat-options input[value="${id}"]`).check();
  await page.locator('#lark-group-mode').selectOption('all');
  assert.equal(await page.locator('#lark-group-cost-warning').isVisible(),false);
  await page.locator('#lark-groups-scope').check();
  assert.equal(await page.locator('#lark-group-cost-warning').isVisible(),true);
  assert.equal(await page.locator('#lark-save').isEnabled(),true);
  for(const width of [1440,390]){
   await page.setViewportSize({width,height:1000});
   assert.equal(await page.locator('#notification-detail').evaluate(e=>e.scrollWidth>e.clientWidth+1),false);
   await page.locator('#notification-detail').screenshot({path:`/tmp/teamx-lark-search-cost-${width}.png`});
  }
  await page.setViewportSize({width:1440,height:1000});
  await page.locator('#lark-groups-enabled').uncheck();
  assert.equal(await page.locator('#lark-group-cost-warning').isVisible(),false);
  await page.locator('#lark-groups-enabled').check();
  await page.locator('#lark-group-mode').selectOption('mentions');
  assert.equal(await page.locator('#lark-group-cost-warning').isVisible(),false);
  await page.locator('#lark-group').uncheck();await page.locator('#lark-all').uncheck();
  assert.equal(await page.locator('#lark-save').isEnabled(),true);
  await page.locator('#lark-save').click();await page.locator('#lark-feedback').filter({hasText:'设置已保存'}).waitFor();
  assert.equal(await page.locator('#lark-save').isDisabled(),true);
  await page.reload();await open();
  assert.equal(await page.locator('#lark-groups-scope').isChecked(),true);
  assert.equal(await page.locator('#lark-group').isChecked(),false);assert.equal(await page.locator('#lark-all').isChecked(),false);
  await page.locator('#lark-groups-scope').uncheck();
  assert.equal(await page.locator('#lark-groups-chat-selected button').count(),5);
  await page.locator('#lark-group-mode').selectOption('all');
  for(const width of [1440,390,632]){
   await page.setViewportSize({width,height:1000});
   assert.equal(await page.locator('#notification-detail').evaluate(e=>e.scrollWidth>e.clientWidth+1),false);
   await page.locator('#notification-detail').screenshot({path:`/tmp/teamx-lark-search-settings-${width}.png`});
  }
  await page.locator('#lark-save').click();await page.locator('#lark-feedback').filter({hasText:'设置已保存'}).waitFor();
  assert.deepEqual(errors,[]);
  console.log(JSON.stringify({fixture_only:true,server_defaults:true,all_groups:true,preserved_five_groups:true,own_context:true,cost_warning:true,persist_reload:true,mobile_overflow:false,page_errors:0}));
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
