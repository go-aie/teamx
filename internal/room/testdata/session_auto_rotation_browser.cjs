// Optional real Chromium acceptance; requires Playwright and its Chromium installation.
const {chromium}=require('playwright');
const fs=require('fs');const assert=require('node:assert/strict');
(async()=>{
 const output=process.argv[2];
 if (!output || !require('path').isAbsolute(output)) throw new Error('usage: node session_auto_rotation_browser.cjs ABS_ACCEPTANCE_OUTPUT');
 const ready=JSON.parse(fs.readFileSync(output+'/browser-ready.json'));
 const browser=await chromium.launch({headless:true});
 try{
  const page=await browser.newPage({viewport:{width:1440,height:1000}});
  const mutations=[],errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  page.on('request',r=>{if(r.method()==='POST'&&/\/v1\/member\/(run|pause|session\/rotate)/.test(r.url()))mutations.push(new URL(r.url()).pathname)});
  await page.goto(ready.endpoint+'/');
  await page.locator('.member-details').filter({hasText:'本会话 2 / 2 · 累计 6'}).waitFor();
  await page.locator('.member-summary').click();
  await page.locator('#activity-budget').filter({hasText:'2 / 2 · 累计 6'}).waitFor();
  assert.equal(await page.locator('#agent-enable-hint').isVisible(),false);
  assert.equal(await page.locator('#agent-actions button').first().innerText(),'暂停');
  await page.screenshot({path:output+'/browser-threshold.png',fullPage:true});
  await page.locator('#message-input').fill('First run exactly "$TEAMX_CLI" read --unread --json in bash to consume this message. Answer privately S3_TURN_7. Do not send to the Room or use any other tools.');
  await page.locator('#message-input').press('Enter');
  await page.locator('#activity-budget').filter({hasText:'1 / 2 · 累计 7'}).waitFor({timeout:180000});
  await page.locator('#agent-status').filter({hasText:'等待消息'}).waitFor({timeout:180000});
  await page.locator('#activity-budget').filter({hasText:'1 / 2 · 累计 7'}).waitFor();
  await page.locator('#activity-rotation').filter({hasText:'达到会话回合阈值 · completed'}).waitFor();
  assert.deepEqual(mutations,[]);
  await page.screenshot({path:output+'/browser-after-rotation.png',fullPage:true});
  await page.getByRole('button',{name:'暂停 reviewer',exact:true}).click();
  await page.locator('#agent-status').filter({hasText:'已暂停'}).waitFor();
  await page.locator('#message-input').fill('Remain paused: this local acceptance message must stay unread.');
  await page.locator('#message-input').press('Enter');
  await page.locator('#activity-progress').filter({hasText:'1 条未读'}).waitFor();
  await page.waitForTimeout(1000);
  assert.match(await page.locator('#activity-budget').innerText(),/1 \/ 2 · 累计 7/);
  assert.equal(await page.locator('#agent-status').innerText(),'已暂停');
  assert.deepEqual(mutations,['/v1/member/pause']);assert.deepEqual(errors,[]);
  await page.screenshot({path:output+'/browser-paused.png',fullPage:true});
  fs.writeFileSync(output+'/browser-done.json',JSON.stringify({status:'passed',mutations,errors,threshold:'2/2 total6',after:'1/2 total7',pauseHeld:true}));
  console.log('Browser passed: threshold continued without enable/reset; pause held; counters and rotation reason visible.');
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
