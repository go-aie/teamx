const {chromium}=require('playwright'),assert=require('node:assert/strict');
(async()=>{
 const endpoint=process.argv[2],browser=await chromium.launch({headless:true});
 try{
  const page=await browser.newPage(),errors=[],writes=[];
  page.on('pageerror',e=>errors.push(e.message));
  page.on('request',r=>{if(new URL(r.url()).pathname==='/v1/models/change')writes.push(r.postDataJSON());});
  await page.addInitScript(()=>{
   const Native=EventSource;window.sources=[];
   window.EventSource=class extends Native{constructor(url){super(url);window.sources.push(this);this.addEventListener('hello',e=>this.connectionID=JSON.parse(e.data).connection_id);}};
  });
  await page.goto(endpoint);
  await page.waitForFunction(()=>window.sources.at(-1)?.connectionID);
  await page.locator('#user-menu-toggle').click();await page.locator('#open-settings').click();await page.locator('#settings-tab-models').click();
  await page.locator('#model-list .model-row-main').first().click();
  await page.locator('#model-entry-name').fill('保存回执丢失后核对');
  await page.route('**/v1/models/change',async route=>{const response=await route.fetch();assert.equal(response.status(),202,await response.text());await route.abort('failed');},{times:1});
  await page.locator('#model-save').click();
  await page.waitForFunction(()=>document.querySelector('#model-edit-form').hidden);
  await page.locator('#model-list').getByText('保存回执丢失后核对',{exact:true}).waitFor();
  assert.equal(writes.length,1);const original=writes[0].request_id;
  await page.evaluate(()=>{const s=window.sources.at(-1);s.dispatchEvent(new MessageEvent('resync',{data:JSON.stringify({connection_id:s.connectionID})}));});
  await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:true});document.dispatchEvent(new Event('visibilitychange'));});
  await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:false});document.dispatchEvent(new Event('visibilitychange'));});
  await page.waitForFunction(()=>window.sources.at(-1)?.connectionID);
  await page.reload();await page.waitForFunction(()=>window.sources.at(-1)?.connectionID);
  assert.equal(writes.length,1);
  const op=await(await fetch(endpoint+'/v1/models/operation?id='+encodeURIComponent(original))).json();
  assert.equal(op.id,original);assert.equal(op.status,'completed');assert.deepEqual(errors,[]);
  console.log('PASS: actual model change accepted once, lost HTTP receipt, original operation reconciled through resync, visibility recovery and reload; model probe is controlled.');
  await page.close();
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
