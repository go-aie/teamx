// Real Chromium + TeamX HTTP and isolated synthetic Scion CLI credentials.
const {chromium}=require('playwright'),assert=require('node:assert/strict'),path=require('node:path');
(async()=>{
 const [endpoint,encoded,out]=process.argv.slice(2),input=JSON.parse(encoded),browser=await chromium.launch({headless:true});
 try{
  const context=await browser.newContext({viewport:{width:1440,height:1050}}),page=await context.newPage(),errors=[],writes=[],responses=[];
  page.setDefaultTimeout(20000);page.on('pageerror',e=>errors.push(e.message));
  page.on('request',r=>{if(/selection-interactions\/(update|submit)$/.test(r.url()))writes.push(r.postDataJSON());});
  page.on('response',r=>{if(/\/platforms\/(scion\/|save|verify)/.test(r.url()))responses.push({path:new URL(r.url()).pathname,status:r.status()});});
  await context.addInitScript(({room,space})=>sessionStorage.setItem(space ? `teamx.space:${space}:teamx.selected-room` : 'teamx.selected-room',JSON.stringify(room)),{room:{room_id:input.room_id,room_instance:input.room_instance},space:input.space_id});
  await page.goto(endpoint+(input.space_id ? `/s/${input.space_id}/` : ''));
  const card=page.locator(`#messages [data-interaction-id="${input.operation_id}"]`),login=card.getByRole('button',{name:'登录并继续',exact:true}),pat=card.locator('.scion-card-login input[type="password"]');
  await pat.waitFor();await login.waitFor();
  assert.equal(await card.locator('.platform-selection-candidates input').count(),0);
  assert.deepEqual(await card.getByRole('button').allTextContents(),['登录并继续','完整设置']);
  assert.equal(await card.getByRole('link',{name:'查看／创建 PAT ↗'}).getAttribute('href'),'https://scion.invalid/profile?tab=tokens');
  if(out)await page.screenshot({path:path.join(out,'empty-desktop.png'),fullPage:true});
  await page.setViewportSize({width:390,height:844});
  if(out)await page.screenshot({path:path.join(out,'empty-mobile.png'),fullPage:true});
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth),true);
  // A draft PAT is a live password control, never part of saved chat history.
  await pat.click();await pat.fill('unsubmitted-pat');assert.equal(await pat.inputValue(),'unsubmitted-pat');await page.reload();await pat.waitFor();
  await page.waitForFunction(()=>document.getElementById('connection')?.textContent==='服务已连接');
  assert.equal(await pat.inputValue(),'');
  assert.equal(writes.length,0);
  await page.setViewportSize({width:1440,height:1050});
  await page.waitForFunction(()=>!document.getElementById('workspace').inert && !document.getElementById('conversation').inert);
  await pat.click();await pat.fill('alice-pat');assert.equal(await pat.inputValue(),'alice-pat');
  await pat.evaluate(el=>{window.originalPATControl=el;});await login.click();
  try{await card.getByText('选择已保存',{exact:true}).waitFor();}catch(error){
   console.error(JSON.stringify({card:await card.textContent(),same_control:await pat.evaluate(el=>el===window.originalPATControl),responses,writes,errors}));throw error;
  }
  assert.equal(await pat.inputValue(),'');assert.equal(await page.locator('#settings-dialog').evaluate(el=>el.open),false);
  assert.equal(await pat.evaluate(el=>el===window.originalPATControl),true);
  assert.equal(writes.length,2);assert.equal(writes.every(w=>w.operation_id===input.operation_id),true);
  assert.equal(JSON.stringify(writes).includes('alice-pat'),false);
  await page.reload();await card.getByText('选择已保存',{exact:true}).waitFor();
  if(out)await page.screenshot({path:path.join(out,'completed-desktop.png'),fullPage:true});
  assert.deepEqual(errors,[]);console.log(JSON.stringify({status:'passed',relogin:input.relogin,page_errors:errors,selection_writes:writes.length}));
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
