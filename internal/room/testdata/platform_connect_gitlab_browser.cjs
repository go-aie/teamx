// Real Chromium + TeamX HTTP/CLI; simulated remote authorization only.
const {chromium}=require('playwright'),assert=require('node:assert/strict'),{mkdir}=require('node:fs/promises'),{join}=require('node:path');
(async()=>{
 const [endpoint,id,room,origin,firstApplication,samePage]=process.argv.slice(2),browser=await chromium.launch({headless:true}),artifacts=process.env.TEAMX_CONNECT_BROWSER_ARTIFACTS;
 if(artifacts)await mkdir(artifacts,{recursive:true});
 try {
  const context=await browser.newContext({viewport:{width:1440,height:1000}}),page=await context.newPage(),errors=[];
  page.on('pageerror',error=>errors.push(error.message));page.setDefaultTimeout(15000);
  await context.route(origin+'/oauth/authorize?**',route=>{
   const u=new URL(route.request().url()),callback=new URL(endpoint+'/v1/platforms/gitlab/callback');callback.searchParams.set('state',u.searchParams.get('state'));callback.searchParams.set('code','browser-fixture');
   return route.fulfill({contentType:'text/html',body:`<!doctype html><title>Simulated GitLab OAuth</title><a href="${callback.href.replaceAll('&','&amp;')}">Authorize fixture</a>`});
  });
  const catalog=await(await fetch(endpoint+'/v1/rooms')).json(),current=catalog.rooms.find(r=>r.room_instance===room);
  await context.addInitScript(selection=>sessionStorage.setItem('teamx.selected-room',JSON.stringify(selection)),{room_id:current.room_id,room_instance:room});
  await page.goto(endpoint);
  const card=page.locator(`#messages [data-interaction-id="${id}"]`),link=card.locator('a').first();
  await card.waitFor();await link.waitFor({state:'visible'});assert.match(await card.textContent(),/GitLab/);assert.equal(await card.locator('img').isVisible(),false);
  await card.evaluate(el=>{window.originalGitlabCard=el;});
  await page.locator('#user-menu-toggle').click();await page.locator('#open-settings').click();await page.locator('#settings-tab-platforms').click();await page.locator("#platforms-list-view").waitFor({state:"visible"});assert.equal(await page.locator("#settings-dialog [data-interaction-id]").count(),0);await page.locator('#close-settings').click();
  assert.equal(await card.evaluate(el=>el===window.originalGitlabCard),true);
  if(firstApplication==='true') {
   assert.match(await card.textContent(),/所需 scopes：api/);
   const applicationLink=card.getByText('前往 GitLab 管理应用 ↗',{exact:true});assert.equal(await applicationLink.getAttribute('href'),origin+'/-/user_settings/applications');
   await link.click();await page.locator('#connection-form').waitFor({state:'visible'});
   assert.equal(await page.locator('#connection-url').inputValue(),origin);assert.equal(await page.locator('#connection-url').evaluate(el=>el.readOnly),true);
   await page.locator('#close-settings').click();await page.reload();await link.waitFor({state:'visible'});
   await link.click();await page.locator('#gitlab-client-id').fill('private-client-id');await page.locator('#gitlab-client-secret').fill('private-client-secret');
   assert.equal(await page.locator('#gitlab-scope-write-repository').isChecked(),false);
   const popupWait=page.waitForEvent('popup');await page.locator('#gitlab-import').click();const popup=await popupWait;await popup.getByText('Authorize fixture',{exact:true}).waitFor();
   await page.locator('#platforms-list-view').waitFor({state:'visible'});assert.equal(await page.locator('#gitlab-client-secret').inputValue(),'');await page.locator('#close-settings').click();
   await popup.getByText('Authorize fixture',{exact:true}).click();await popup.waitForURL(/\/gitlab\/complete(?:\?.*)?$/);await popup.close();
  } else if(samePage==='true') {
   await page.setViewportSize({width:390,height:844});
   await page.evaluate(()=>{window.open=()=>null;});
   await link.click();await page.getByText('Authorize fixture',{exact:true}).waitFor();
   await page.getByText('Authorize fixture',{exact:true}).click();await page.waitForURL(/\/gitlab\/complete(?:\?.*)?$/);
   assert.equal(await page.locator('meta[name=viewport]').count(),1);
   await page.getByText('返回 TeamX 查看结果',{exact:true}).click();
   await card.getByText('已连接',{exact:true}).waitFor();
   assert.equal(new URL(page.url()).hash,'');
   assert.equal(await page.evaluate(()=>sessionStorage.getItem('teamx.platform-return')),null);
  } else {
  const popupWait=page.waitForEvent('popup');await link.click();const popup=await popupWait;await popup.getByText('Authorize fixture',{exact:true}).waitFor();
  await page.reload();await link.waitFor({state:'visible'});
  // Multiple tabs in this context share the original HttpOnly binding.
  const secondWait=page.waitForEvent('popup');await link.click();const second=await secondWait;await second.getByText('Authorize fixture',{exact:true}).waitFor();await second.close();
  await popup.getByText('Authorize fixture',{exact:true}).click();await popup.waitForURL(/\/gitlab\/complete(?:\?.*)?$/);await popup.close();
  }
  await card.getByText('已连接',{exact:true}).waitFor();assert.equal(await link.isVisible(),false);
  await page.reload();await card.getByText('已连接',{exact:true}).waitFor();
  if(artifacts)await page.screenshot({path:join(artifacts,firstApplication==='true' ? 'first-application-connected.png' : 'gitlab-connected.png'),fullPage:true});
  await page.setViewportSize({width:390,height:844});await page.waitForFunction(()=>document.documentElement.scrollWidth<=innerWidth);assert.equal(await card.isVisible(),true);
  if(artifacts)await page.screenshot({path:join(artifacts,firstApplication==='true' ? 'first-application-mobile.png' : 'gitlab-mobile.png'),fullPage:true});
  assert.deepEqual(errors,[]);console.log(samePage==='true' ? 'PASS: GitLab blocked popup, same-page OAuth, cookie callback, fixed completion link, original operation return, reference cleanup and reload at 390px. Remote OAuth simulated.' : firstApplication==='true' ? 'PASS: GitLab first application settings return, close/reopen, secure import, owner verification, original callback save, reload and mobile. Remote OAuth simulated.' : 'PASS: GitLab browser bridge, repeated click/tab, settings close, refresh, callback save, result and mobile. Remote OAuth simulated.');
 } finally {await browser.close();}
})().catch(error=>{console.error(error.message);process.exitCode=1;});
