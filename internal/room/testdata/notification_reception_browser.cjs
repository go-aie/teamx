// Local manager and saved fixture account only; no remote platform requests.
const {chromium}=require('playwright');
const assert=require('node:assert/strict');
(async()=>{
 const browser=await chromium.launch({headless:true});
 try {
  const page=await browser.newPage({viewport:{width:1440,height:1100}}),errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.goto(process.argv[2]);
  await page.locator('#open-gitlab-source').click();
  const connection=await page.locator('#gitlab-connection option').evaluateAll(options=>options.find(o=>o.value)?.value);
  await page.locator('#gitlab-connection').selectOption(connection);
  await page.locator('#gitlab-todos-enabled').check();
  await page.locator('#gitlab-backfill').selectOption('custom');
  assert.equal(await page.locator('#gitlab-save').isDisabled(),true);
  await page.locator('#gitlab-backfill-start').fill('2099-01-01T00:00');
  await page.locator('#gitlab-backfill-error').filter({hasText:'不能晚于'}).waitFor();
  assert.equal(await page.locator('#gitlab-save').isDisabled(),true);
  await page.locator('#gitlab-backfill-start').fill('2026-10-01T12:34:56');
  await page.locator('#notification-panel-gitlab').screenshot({path:'/tmp/teamx-reception-wide.png'});
  const response=page.waitForResponse(r=>r.url().endsWith('/v1/platform/gitlab/notifications/configure'));
  await page.locator('#gitlab-save').click();
  const saved=await (await response).json();
  assert.equal(saved.backfill_locked,true);
  assert.equal(Date.parse(saved.config.backfill_start),new Date('2026-10-01T12:34:56').getTime());
  assert.equal(saved.todos.baseline,saved.config.backfill_start);
  await page.locator('#gitlab-backfill-locked').waitFor({state:'visible'});
  assert.equal(await page.locator('#gitlab-backfill').isDisabled(),true);
  assert.equal(await page.locator('#gitlab-save').isDisabled(),true);
  await page.reload();await page.locator('#open-gitlab-source').click();
  await page.locator('#gitlab-backfill-locked').waitFor({state:'visible'});
  await page.setViewportSize({width:860,height:1000});
  await page.locator('#notification-panel-gitlab').screenshot({path:'/tmp/teamx-reception-narrow.png'});
  await page.locator('#notification-tab-messages').click();
  await page.locator('#lark-enabled').check();
  assert.deepEqual(await page.locator('#gitlab-backfill option').allTextContents(),await page.locator('#lark-backfill option').allTextContents());
  assert.deepEqual(errors,[]);
  process.stdout.write('Reception presets, custom validation, shared start, lock, reload and responsive layout passed.\n');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
