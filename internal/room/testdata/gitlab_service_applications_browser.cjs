const {chromium}=require('playwright');
(async()=>{
 const browser=await chromium.launch({headless:true});
 try {
  const context=await browser.newContext({ignoreHTTPSErrors:true,viewport:{width:1360,height:950}}),page=await context.newPage(),errors=[];
  page.setDefaultTimeout(10000);
  page.on('pageerror',e=>errors.push(e.message));
  await context.route('**/oauth/authorize?**',async route=>{
   const authorize=new URL(route.request().url()),callback=new URL(authorize.searchParams.get('redirect_uri'));
   callback.searchParams.set('state',authorize.searchParams.get('state'));callback.searchParams.set('code','fixture-code');
   await route.fulfill({contentType:'text/html',body:`<a id="grant" href="${callback.href.replaceAll('&','&amp;')}">Authorize fixture</a>`});
  });
  await page.goto(process.argv[2]);
  await page.waitForFunction(()=>!document.getElementById('open-settings').disabled,{timeout:10000}).catch(async()=>{throw Error('bootstrap: '+await page.locator('#sync-error').textContent()+' '+errors.join('; '))});
  await page.locator('#user-menu-toggle').click();await page.locator('#open-settings').click();await page.locator('#settings-tab-platforms').click();await page.locator('#connection-new').click();
  await page.waitForFunction(()=>document.getElementById('gitlab-service-application').value.length===32);
  await page.locator('#connection-name').fill('My managed GitLab');
  for(const id of ['gitlab-client-id','gitlab-client-secret','gitlab-edit-application','gitlab-repair-application'])if(await page.locator(`#${id}`).isVisible())throw Error(`private application management visible: ${id}`);
  if(await page.locator('#gitlab-scope-write-repository').isChecked())throw Error('repository scope enabled');
  await page.locator('#onboarding-only').check();
  const opening=context.waitForEvent('page');await page.locator('#gitlab-login').click();const popup=await opening;
  await popup.locator('#grant').click();await popup.waitForURL('**/gitlab/complete');
  await page.waitForFunction(()=>document.getElementById('gitlab-account').textContent.includes('已连接')).catch(async()=>{throw Error('authorization: '+await page.locator('#gitlab-account').textContent()+'; '+await page.locator('#platforms-feedback').textContent()+'; '+errors.join('; '))});
  if(errors.length)throw Error(errors.join('\n'));
  console.log(JSON.stringify({applicationSelection:true,privateFieldsHidden:true,apiOnly:true,authorizationSaved:true,pageErrors:errors.length}));
 }finally{await browser.close();}
})().catch(error=>{console.error(error.stack||error);process.exit(1)});
