// Full UI and HTTP path with an isolated manager and fixture CLI.
const {chromium}=require('playwright');
const assert=require('node:assert/strict');
(async()=>{
 const browser=await chromium.launch({headless:true,channel:process.env.TEAMX_BROWSER_CHANNEL || undefined});
 try{
  const page=await browser.newPage({viewport:{width:1440,height:1000}}),errors=[],searches=[];
  page.on('pageerror',e=>errors.push(e.message));
  page.on('request',r=>{if(r.url().endsWith('/v1/platform/lark/chats'))searches.push(r.postDataJSON());});
  await page.goto(process.argv[2]);
  await page.locator('.member-summary').first().click();await page.locator('#agent-tab-platforms').click();
  await page.getByRole('button',{name:'编辑 飞书 的通知设置',exact:true}).click();
  await page.waitForFunction(()=>document.getElementById('lark-private-enabled').checked && !document.getElementById('lark-fields').disabled);
  await page.locator('#lark-private-title').click();await page.locator('#lark-scope').uncheck();
  await page.locator('#lark-private-chat-picker-summary').click();
  await page.locator('#lark-private-chat-options input[value="oc_private"]').check();
  await page.locator('#lark-private-chat-search').fill('yanfa');
  await page.locator('#lark-private-chat-options input[value="oc_search_peer"]').waitFor();
  assert.equal(await page.locator('#lark-private-chat-options input').count(),1);
  assert.match(await page.locator('#lark-private-chat-picker-status').textContent(),/人员私聊结果较多.*机器人私聊搜索权限不足/);
  await page.locator('#lark-groups-title').click();
  await page.locator('#lark-groups-chat-picker-summary').click();
  await page.locator('#lark-groups-chat-search').fill('yanfa');
  await page.locator('#lark-groups-chat-options input[value="oc_search_group"]').check();
  assert.equal(await page.locator('#lark-groups-chat-options input').count(),1);
  assert.equal(await page.locator('#lark-groups-chat-selected button').count(),1);
  assert.equal(await page.locator('#lark-private-chat-selected button').count(),1);
  assert.equal(await page.locator('#lark-groups-chat-more').isVisible(),false);
  await page.locator('#notification-detail').screenshot({path:'/tmp/teamx-lark-search-desktop.png'});
  await page.locator('#lark-private-chat-search').fill('');
  await page.locator('#lark-private-chat-options input[value="oc_private"]').waitFor();
  assert.equal(await page.locator('#lark-groups-chat-search').inputValue(),'yanfa');
  assert.match(await page.locator('#lark-groups-chat-selected').textContent(),/研发群/);
  for(const width of [390,632]){
   await page.setViewportSize({width,height:900});
   assert.equal(await page.locator('#notification-detail').evaluate(el=>el.scrollWidth>el.clientWidth+1),false);
   for(const kind of ['private','groups'])assert.equal(await page.locator(`#lark-${kind}-chat-picker`).evaluate(el=>el.scrollWidth>el.clientWidth+1),false);
  }
  await page.locator('#notification-detail').screenshot({path:'/tmp/teamx-lark-search-mobile.png'});
  assert.ok(searches.some(x=>x.query==='yanfa' && x.type==='group'));
  assert.ok(searches.some(x=>x.query==='yanfa' && x.type==='p2p'));
  assert.deepEqual(errors,[]);
  console.log(JSON.stringify({fixture_only:true,separate_searches:true,remote_search:true,partial_permissions:true,selected_labels:true,mobile_overflow:false,page_errors:0}));
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
