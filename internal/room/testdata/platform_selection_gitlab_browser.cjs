const {chromium}=require('playwright'),assert=require('node:assert/strict');
(async()=>{
 const [endpoint,id,room]=process.argv.slice(2),browser=await chromium.launch({headless:true});
 try {
  const context=await browser.newContext({viewport:{width:1440,height:1000}}),page=await context.newPage(),errors=[];page.on('pageerror',e=>errors.push(e.message));page.setDefaultTimeout(15000);
  const catalog=await(await fetch(endpoint+'/v1/rooms')).json(),current=catalog.rooms.find(r=>r.room_instance===room);
  await context.addInitScript(selection=>sessionStorage.setItem('teamx.selected-room',JSON.stringify(selection)),{room_id:current.room_id,room_instance:room});await page.goto(endpoint);
  const card=page.locator(`#messages [data-interaction-id="${id}"]`);await card.waitFor();await card.getByText('Project · team/project · #7',{exact:true}).waitFor();
  await card.evaluate(el=>window.originalProjectCard=el);await card.getByRole('button',{name:'加载更多',exact:true}).click();
  const row=card.locator('.platform-selection-candidates label').filter({hasText:'other/project'});await row.locator('input').check();await card.locator('select').selectOption('note');
  await page.locator('#open-settings').click();await page.locator('#settings-tab-platforms').click();await page.locator(`#conversation-connections [data-interaction-id="${id}"]`).waitFor();await page.locator('#close-settings').click();
  assert.equal(await card.evaluate(el=>el===window.originalProjectCard),true);assert.match(await card.textContent(),/other\/project.*#8/);
  // Closing full settings refreshes candidate metadata from page one while the
  // local selection survives. Reload page two and check the exact selected ID.
  await card.getByRole('button',{name:'加载更多',exact:true}).click();assert.equal(await row.locator('input').isChecked(),true);
  await page.setViewportSize({width:390,height:844});await page.waitForFunction(()=>document.documentElement.scrollWidth<=innerWidth);
  await card.getByRole('button',{name:'提交选择',exact:true}).click();await card.getByText('选择已保存',{exact:true}).waitFor();
  await page.reload();await card.getByText('选择已保存',{exact:true}).waitFor();assert.match(await card.textContent(),/other\/project.*#8/);assert.deepEqual(errors,[]);
  console.log('PASS: project paths/IDs, pagination, single selection/filter, settings close, stable DOM, mobile, submit and reload; GitLab metadata simulated.');
 } finally {await browser.close();}
})().catch(e=>{console.error(e.message);process.exitCode=1;});
