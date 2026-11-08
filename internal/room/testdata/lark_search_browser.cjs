// Full UI and HTTP path with an isolated manager and fixture CLI.
const {chromium}=require('playwright');
const assert=require('node:assert/strict');
(async()=>{
 const browser=await chromium.launch({headless:true});
 try{
  const page=await browser.newPage({viewport:{width:1440,height:1000}}),errors=[],searches=[];
  page.on('pageerror',e=>errors.push(e.message));
  page.on('request',r=>{if(r.url().endsWith('/v1/platform/lark/chats'))searches.push(r.postDataJSON());});
  await page.goto(process.argv[2]);await page.locator('#open-lark-source').click();await page.locator('#lark-enabled').check();
  await page.locator('#lark-connection').selectOption('c'.repeat(32));
  await page.locator('#lark-chat-picker-summary').click();
  await page.locator('#lark-chat-options input[value="oc_private"]').check();
  await page.locator('#lark-chat-search').fill('yanfa');
  await page.locator('#lark-chat-options input[value="oc_search_peer"]').waitFor();
  assert.equal(await page.locator('#lark-chat-options input').count(),2);
  assert.match(await page.locator('#lark-chat-picker-status').textContent(),/人员私聊结果较多.*机器人私聊搜索权限不足/);
  await page.locator('#lark-chat-options input[value="oc_search_group"]').check();
  assert.equal(await page.locator('#lark-chat-selected button').count(),2);
  assert.equal(await page.locator('#lark-chat-more').isVisible(),false);
  await page.locator('.notification-settings.lark-settings').screenshot({path:'/tmp/teamx-lark-search-desktop.png'});
  await page.locator('#lark-chat-type').selectOption('p2p');
  await page.waitForFunction(()=>document.querySelectorAll('#lark-chat-options input').length===1);
  assert.equal(await page.locator('#lark-chat-options input').getAttribute('value'),'oc_search_peer');
  await page.setViewportSize({width:390,height:844});
  await page.locator('.notification-settings.lark-settings').screenshot({path:'/tmp/teamx-lark-search-mobile.png'});
  assert.equal(await page.locator('#lark-chat-picker').evaluate(el=>el.scrollWidth>el.clientWidth+1),false);
  await page.locator('#lark-chat-search').fill('');
  await page.locator('#lark-chat-options input[value="oc_private"]').waitFor();
  assert.match(await page.locator('#lark-chat-selected').textContent(),/研发群/);
  assert.ok(searches.some(x=>x.query==='yanfa' && x.type==='all'));
  assert.ok(searches.some(x=>x.query==='yanfa' && x.type==='p2p'));
  assert.deepEqual(errors,[]);
  console.log(JSON.stringify({fixture_only:true,remote_search:true,type_filter:true,partial_permissions:true,selected_labels:true,mobile_overflow:false,page_errors:0}));
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
