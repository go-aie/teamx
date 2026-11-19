// Use the exact card created by the real model in this test run.
const {chromium}=require('playwright');
const assert=require('node:assert/strict');
const {join}=require('node:path');
(async()=>{
 const [endpoint,id,room,provider,out]=process.argv.slice(2);
 const browser=await chromium.launch({headless:true});
 try {
  const context=await browser.newContext({viewport:{width:1440,height:1000}});
  const page=await context.newPage(),errors=[];page.on('pageerror',e=>errors.push(e.message));
  page.setDefaultTimeout(15000);
  const catalog=await(await fetch(endpoint+'/v1/rooms')).json();
  const current=catalog.rooms.find(r=>r.room_instance===room);assert.ok(current.is_direct);
  await context.addInitScript(selection=>sessionStorage.setItem('teamx.selected-room',JSON.stringify(selection)),{room_id:current.room_id,room_instance:room});
  const start=Date.now();await page.goto(endpoint);
  const card=page.locator(`#messages [data-interaction-id="${id}"]`);
  try {await card.waitFor();} catch(error) {
   await page.screenshot({path:join(out,'card-failure.png'),fullPage:true});
   console.error(JSON.stringify({operator:catalog.operator,current,body:await page.locator('body').innerText(),errors}));throw error;
  }
  const visibleAtMS=Date.now(),visibleMs=visibleAtMS-start;
  const query=card.locator('.platform-selection-fields input');
  await query.fill(provider==='lark'?'产品':'Project');
  const searched=page.waitForResponse(r=>r.url().endsWith('/v1/platforms/selection-interactions/candidates')&&r.request().postDataJSON().query===(provider==='lark'?'产品':'Project'));
  await card.getByRole('button',{name:'搜索',exact:true}).click();assert.equal((await searched).status(),200);
  await card.getByRole('button',{name:'加载更多',exact:true}).click();
  const selector=provider==='lark'?'input[value="oc_group_three"]':'input[value="8"]';
  const chosen=card.locator('.platform-selection-candidates').locator(selector);
  await chosen.check();await page.screenshot({path:join(out,'card-desktop.png'),fullPage:true});
  await page.setViewportSize({width:390,height:844});
  await page.waitForFunction(()=>document.documentElement.scrollWidth<=innerWidth);
  assert.ok(await chosen.isChecked());
  await page.screenshot({path:join(out,'card-mobile.png'),fullPage:true});
  const submitted=page.waitForResponse(r=>r.url().endsWith('/v1/platforms/selection-interactions/submit'));
  await card.getByRole('button',{name:'提交选择',exact:true}).click();
  const response=await submitted;assert.equal(response.status(),200);const receipt=await response.json();
  assert.equal(receipt.operation_id,id);assert.equal(receipt.status,'completed');
  if(provider==='lark')assert.deepEqual(receipt.draft.chat_ids,['oc_group_three']);
  else {assert.equal(receipt.project.id,8);assert.equal(receipt.project.path_with_namespace||receipt.project.path,'other/project');}
  await card.getByText('选择已保存',{exact:true}).waitFor();
  await page.reload();await card.getByText('选择已保存',{exact:true}).waitFor();
  assert.deepEqual(errors,[]);
  console.log(JSON.stringify({status:'passed',component_visible_ms:visibleMs,component_visible_at_ms:visibleAtMS,operation_id:id,result_version:receipt.result_version,search:true,pagination:true,mobile:true,reload:true}));
 } finally {await browser.close();}
})().catch(e=>{console.error(e.stack);process.exitCode=1;});
