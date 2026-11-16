const {chromium}=require('playwright'),assert=require('node:assert/strict');
(async()=>{
 const endpoint=process.argv[2],browser=await chromium.launch({headless:true});
 try{
  const page=await browser.newPage(),requests=[],errors=[];
  page.on('pageerror',e=>errors.push(e.message));page.on('request',r=>requests.push(new URL(r.url()).pathname));
  await page.goto(endpoint+'/access');
  assert.equal(await page.evaluate(async token=>(await fetch('/v1/access/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({token})})).status,process.env.TEAMX_WEB_UPDATES_TEST_TOKEN),200);
  await page.goto(endpoint);
  await page.waitForFunction(()=>document.querySelector('#connection').textContent==='服务已连接');
  await page.locator('#message-input').fill('认证撤销后保留的草稿');
  assert.equal(await page.evaluate(async()=>(await fetch('/v1/access/logout',{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'})).status),200);
  await page.waitForURL('**/access');
  assert.ok(await page.evaluate(()=>Object.values(sessionStorage).some(value=>value.includes('认证撤销后保留的草稿'))));
  assert.equal(requests.filter(path=>path==='/v1/send'||path==='/v1/models/change'||/\/platforms\/[^/]+\/login$/.test(path)).length,0);
  assert.deepEqual(errors,[]);console.log('PASS: production page observes stream revocation, confirms session 401, returns to login and keeps the draft without any business write.');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
