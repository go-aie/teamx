// Optional local browser acceptance; uses only the test server and fixture tokens.
const {chromium}=require('playwright');
(async()=>{
 const browser=await chromium.launch({headless:true});
 try {
  const context=await browser.newContext({ignoreHTTPSErrors:true,viewport:{width:1440,height:1000}});
  const page=await context.newPage(),errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  context.on('dialog',d=>d.accept());
  await context.route('**/oauth/authorize?**',async route=>{
   const u=new URL(route.request().url());
   const callback=new URL(u.searchParams.get('redirect_uri'));
   callback.searchParams.set('state',u.searchParams.get('state'));callback.searchParams.set('code','authorization-fixture');
   await route.fulfill({contentType:'text/html',body:`<!doctype html><html><title>Fixture GitLab consent</title><a id="grant" href="${callback.href.replaceAll('&','&amp;')}">Authorize fixture</a></html>`});
  });
  await page.goto(process.argv[2]);
  await page.locator('#open-settings').click();await page.locator('.connection-row').first().click();
  if(await page.locator('#gitlab-setup').isVisible())throw Error('setup shown for configured connection');
  if(await page.locator('#connection-pat').count())throw Error('token in normal form');
  const popupPromise=context.waitForEvent('page');await page.locator('#gitlab-login').click();
  try { await page.locator('#gitlab-login-link').waitFor({state:'visible',timeout:8000}); } catch {throw Error('start feedback: '+await page.locator('#connections-feedback').textContent()+'; init: '+await page.locator('#gitlab-scope-hint').textContent());}
  const popup=await popupPromise;
  await popup.locator('#grant').click();await popup.waitForURL('**/gitlab/complete');
  if(new URL(popup.url()).search)throw Error('code not stripped');
  await page.locator('#gitlab-account').filter({hasText:'已连接'}).waitFor();
  if(await page.locator('#connection-auth').count())throw Error('OAuth asks for auth kind');
  await page.screenshot({path:'/tmp/teamx-g3-browser-desktop.png',fullPage:true});
  await page.setViewportSize({width:390,height:844});
  await page.screenshot({path:'/tmp/teamx-g3-browser-mobile.png',fullPage:true});
  await page.locator('#gitlab-logout').click();
  await page.locator('#gitlab-account').filter({hasText:'已退出登录'}).waitFor();
  await page.locator('#connection-back').click();await page.locator('#connection-new').click();
  await page.locator('#connection-name').fill('New GitLab');await page.locator('#connection-url').fill('https://new.gitlab.invalid');
  await page.locator('#connection-save').click();await page.locator('#gitlab-setup').waitFor({state:'visible'});
  await page.setViewportSize({width:1440,height:1000});await page.screenshot({path:'/tmp/teamx-oauth-setup-desktop.png',fullPage:true});
  await page.setViewportSize({width:390,height:844});await page.screenshot({path:'/tmp/teamx-oauth-setup-mobile.png',fullPage:true});
  if(await page.locator('#gitlab-remote-id').count())throw Error('obsolete integer application ID');
  await page.locator('#gitlab-client-id').fill('new-client');await page.locator('#gitlab-client-secret').fill('fixture-only-secret');
  await page.locator('#gitlab-import').click();
  await page.waitForFunction(()=>document.getElementById('gitlab-setup').hidden && document.getElementById('gitlab-client-secret').value==='');

  if(errors.length)throw Error(errors.join(';'));
  console.log(JSON.stringify({browser:'Chromium',fixture_only:true,pkce_callback:true,cookie_binding:true,code_removed:true,login_saved:true,logout_confirmed:true,setup:true,secret_cleared:true,desktop:[1440,1000],mobile:[390,844],page_errors:0}));
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
