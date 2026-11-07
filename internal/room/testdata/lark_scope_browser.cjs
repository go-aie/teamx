// Exercises the real UI with a local manager and fixture CLI only.
const {chromium}=require('playwright');
const assert=require('node:assert/strict');
(async()=>{
 const browser=await chromium.launch({headless:true});
 try{
  const page=await browser.newPage({viewport:{width:1440,height:1000}}),errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.goto(process.argv[2]);await page.locator('#open-lark-source').click();await page.locator('#lark-enabled').check();
  assert.equal(await page.locator('#lark-scope').getAttribute('type'),'checkbox');
  assert.equal(await page.locator('#lark-conversation-label').textContent(),'会话');
  assert.equal(await page.locator('#lark-scope').isDisabled(),true);
  await page.locator('#lark-connection').selectOption('c'.repeat(32));
  await page.locator('#lark-target').selectOption({index:1});
  await page.locator('#lark-chat-picker-summary').click();
  await page.locator('#lark-chat-options input[value="oc_private"]').check();
  assert.equal(await page.locator('#lark-chat-selected button').count(),1);
  await page.locator('.notification-settings.lark-settings').screenshot({path:'/tmp/teamx-lark-scope-specified-desktop.png'});
  await page.locator('#lark-scope').check();
  assert.equal(await page.locator('#lark-chat-selection').isVisible(),false);
  assert.equal(await page.locator('#lark-scope-help').isVisible(),true);
  await page.locator('#lark-save').click();await page.locator('#lark-feedback').filter({hasText:'设置已保存'}).waitFor();
  await page.locator('.notification-settings.lark-settings').screenshot({path:'/tmp/teamx-lark-scope-all-desktop.png'});
  await page.setViewportSize({width:390,height:844});
  await page.locator('.notification-settings.lark-settings').screenshot({path:'/tmp/teamx-lark-scope-all-mobile.png'});
  await page.locator('#lark-scope').uncheck();
  assert.equal(await page.locator('#lark-chat-selection').isVisible(),true);
  assert.equal(await page.locator('#lark-chat-selected button').count(),1);
  await page.locator('.notification-settings.lark-settings').screenshot({path:'/tmp/teamx-lark-scope-specified-mobile.png'});
  await page.locator('#lark-save').click();await page.locator('#lark-feedback').filter({hasText:'设置已保存'}).waitFor();
  assert.equal(await page.locator('#lark-chat-selected button').count(),1);
  assert.deepEqual(errors,[]);
  console.log(JSON.stringify({fixture_only:true,checkbox_scope:true,restore_after_save:true,desktop:[1440,1000],mobile:[390,844],page_errors:0}));
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
