// Real browser against an isolated TeamX service, synthetic Feishu CLI only.
const {chromium}=require('playwright'),assert=require('node:assert/strict'),{writeFile,mkdir}=require('node:fs/promises'),{join}=require('node:path');
(async()=>{
 const [endpoint,fixture,id,room,other]=process.argv.slice(2),browser=await chromium.launch({headless:true});
 const artifacts=process.env.TEAMX_CONNECT_BROWSER_ARTIFACTS;
 if(artifacts)await mkdir(artifacts,{recursive:true});
 const capture=(page,name)=>artifacts ? page.screenshot({path:join(artifacts,name+'.png'),fullPage:true,animations:'disabled'}) : undefined;
 try{
  const context=await browser.newContext({viewport:{width:1440,height:1000}}),page=await context.newPage(),errors=[];
  page.on('pageerror',e=>errors.push(e.message));page.setDefaultTimeout(12000);
  await context.route('https://open.feishu.cn/**',route=>route.fulfill({contentType:'text/html',body:'<!doctype html><title>Simulated Feishu authorization</title><p>CLI fixture authorization only.</p>'}));
  const catalog=await (await fetch(endpoint+'/v1/rooms')).json(),current=catalog.rooms.find(r=>r.room_instance===room),second=catalog.rooms.find(r=>r.room_instance===other);
  // Keep both rooms live; navigation uses the actual room sidebar.
  await context.addInitScript(selection=>sessionStorage.setItem('teamx.selected-room',JSON.stringify(selection)),{room_id:current.room_id,room_instance:room});
  await page.goto(endpoint);
  const card=page.locator(`#messages [data-interaction-id="${id}"]`),link=card.locator('a'),qr=card.locator('img');
  // Room order is activity based. Select by stable room entry metadata if needed.
  if(!(await card.count())){const buttons=page.locator('#room-list .room-choice, #direct-list .room-choice');if(await buttons.count())await buttons.filter({hasText:current.name}).first().click();}
  await card.waitFor();await link.waitFor({state:'visible'});await qr.waitFor({state:'visible'});
  await qr.evaluate(img=>img.complete && img.naturalWidth>0 ? true : new Promise((resolve,reject)=>{img.onload=resolve;img.onerror=reject;}));
  assert.match(await link.getAttribute('href'),/^https:\/\/open\.feishu\.cn\/page\/cli\?/);
  await capture(page,'application-authorization');
  await link.focus();await card.evaluate(el=>{window.originalConnectCard=el;window.originalConnectLink=el.querySelector('a');});
  // An actual new Room message triggers the render path while the waiter runs.
  await page.evaluate(async({service,operator})=>{const r=await fetch('/v1/send',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({...service,caller:{kind:'human',id:operator},client_message_id:'browser-new-message',text:'我正在授权'})});if(!r.ok)throw Error(await r.text());},{service:current.service,operator:catalog.operator});
  await page.locator('#messages').getByText('我正在授权',{exact:true}).waitFor();
  assert.equal(await card.evaluate(el=>el===window.originalConnectCard && el.querySelector('a')===window.originalConnectLink && document.activeElement===window.originalConnectLink),true);
  // Close full settings: observing this operation must not cancel its process.
  await page.locator('#open-settings').click();await page.locator('#settings-tab-platforms').click();await page.locator(`#conversation-connections [data-interaction-id="${id}"]`).waitFor();await capture(page,'settings');await page.locator('#close-settings').click();
  await link.waitFor({state:'visible'});
  let popupWait=page.waitForEvent('popup');await link.click();let popup=await popupWait;await popup.waitForLoadState('domcontentloaded');await popup.close();
  const accountQR=page.waitForResponse(response=>response.url().includes('/v1/platforms/lark/qrcode?') && new URL(response.url()).searchParams.get('stage')==='authorization');
  await writeFile(join(fixture,'app-release'),'yes');
  await card.getByText('等待你完成授权飞书账号',{exact:true}).waitFor();assert.equal((await accountQR).ok(),true);
  await capture(page,'account-authorization');
  await page.reload();await link.waitFor({state:'visible'});assert.match(await link.getAttribute('href'),/feishu/);
  await page.locator('#room-list .room-choice, #direct-list .room-choice').filter({hasText:second.name}).click();
  await page.waitForFunction(id=>!document.querySelector(`#messages [data-interaction-id="${id}"]`),id);
  await page.locator('#room-list .room-choice, #direct-list .room-choice').filter({hasText:current.name}).click();await link.waitFor({state:'visible'});
  popupWait=page.waitForEvent('popup');await link.click();popup=await popupWait;await popup.waitForLoadState('domcontentloaded');await popup.close();
  await writeFile(join(fixture,'auth-release'),'yes');
  await card.getByText('已连接',{exact:true}).waitFor();assert.equal(await link.isVisible(),false);assert.equal(await qr.isVisible(),false);
  await page.reload();await card.getByText('已连接',{exact:true}).waitFor();
  await capture(page,'connected');
  await page.setViewportSize({width:390,height:844});await page.waitForFunction(()=>document.documentElement.scrollWidth<=window.innerWidth);assert.equal(await card.isVisible(),true);await capture(page,'mobile');
  assert.deepEqual(errors,[]);console.log('PASS: Chromium + TeamX HTTP/CLI + simulated authorization; waiter, QR, message focus, reload, room switch, settings close, saved result, mobile.');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
