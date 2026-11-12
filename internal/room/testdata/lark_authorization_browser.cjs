// Production UI and HTTP service; official CLI responses are isolated fixtures.
const {chromium} = require('playwright');
const assert = require('node:assert/strict');
const {writeFile, mkdir} = require('node:fs/promises');
const {join} = require('node:path');

(async () => {
  const [endpoint, fixture, id, room, platformID, viewport, goos] = process.argv.slice(2);
  const output = process.env.TEAMX_CONNECT_BROWSER_ARTIFACTS;
  if (output) await mkdir(output, {recursive:true});
  const browser = await chromium.launch({headless:true});
  const errors = [], writes = [], unexpected = [];
  try {
    const page = await browser.newPage({viewport:viewport === 'mobile' ? {width:390,height:844} : {width:1440,height:1000}});
    page.setDefaultTimeout(15000);
    page.on('pageerror', e => errors.push(e.message));
    page.on('request', r => {
      const url = new URL(r.url());
      if (url.origin !== endpoint) unexpected.push(url.origin);
      if (r.method() === 'POST' && ['/v1/tools/bind','/v1/platforms/connect/cancel','/v1/platforms/lark/cancel'].includes(url.pathname)) writes.push({path:url.pathname,body:r.postDataJSON()});
    });
    const catalog = await (await fetch(endpoint+'/v1/rooms')).json();
    const current = catalog.rooms.find(r => r.room_instance === room);
    await page.addInitScript(selection => sessionStorage.setItem('teamx.selected-room',JSON.stringify(selection)), {room_id:current.room_id,room_instance:room});
    const capture = name => output ? page.screenshot({path:join(output,`${viewport}-${name}.png`),fullPage:true,animations:'disabled'}) : undefined;
    const bindings = async () => {
      const q = new URLSearchParams({service_epoch:catalog.service_epoch,caller_id:catalog.operator,room_id:current.room_id,room_instance:room});
      return (await fetch(endpoint+'/v1/tools/bindings?'+q)).json();
    };
    await page.goto(endpoint);
    const card = page.locator(`#messages [data-interaction-id="${id}"]`);
    await card.getByRole('link', {name:'补全授权 ↗',exact:true}).waitFor();
    await card.locator('img').evaluate(img => img.complete && img.naturalWidth > 0 ? true : new Promise((resolve,reject) => {img.onload=resolve;img.onerror=reject;}));
    assert.match(await card.textContent(), /补全授权.*2 个共享连接/s);
    assert.equal((await bindings()).bindings[0].action_authorization, undefined);
    await capture('authorization');
    // Global settings only observes a conversation-owned authorization.
    await page.locator('#open-settings').click();
    await page.locator('#settings-tab-platforms').click();
    await page.locator('#platforms-list-view').waitFor({state:'visible'});
    assert.equal(await page.locator('#settings-dialog [data-interaction-id]').count(),0);
    await page.locator('#close-settings').click();
    await page.reload();
    await card.getByRole('link', {name:'补全授权 ↗',exact:true}).waitFor();
    await writeFile(join(fixture,'auth-release'),'yes');
    await card.getByText('已连接',{exact:true}).waitFor();
    assert.equal(await card.locator('img').isVisible(),false);
    assert.equal((await bindings()).bindings[0].action_authorization,undefined,'OAuth or page reads expanded the member grants');
    await capture('authorized');
    // Inspect the account-level check through the actual production endpoint.
    await page.locator('#open-settings').click();
    await page.locator('#settings-tab-platforms').click();
    await page.locator('#platforms-list .connection-row').filter({hasText:'工作飞书'}).click();
    await page.waitForFunction(()=>document.querySelector('#connection-capability-list li') && !document.querySelector('#connection-capabilities').hidden);
    assert.match(await page.locator('#connection-capability-list').textContent(), /已就绪/);
    assert.equal(await page.locator('#connection-authorize').isVisible(),false);
    if (goos === 'darwin') assert.doesNotMatch(await page.locator('#connection-capability-list').textContent(), /不支持/);
    else assert.match(await page.locator('#connection-capability-list').textContent(), /当前服务不支持/);
    assert.equal(await page.locator('#connection-capability-details').evaluate(el=>el.open),false);
    for (const old of ['connection-sending','connection-send-check','connection-authorize-sending','connection-authorize-targets']) assert.equal(await page.locator('#'+old).count(),0);
    assert.equal(await page.locator('#settings-dialog').evaluate(el => el.scrollWidth > el.clientWidth),false);
    await page.locator('#connection-capability-status').scrollIntoViewIfNeeded();
    await capture('capabilities');
    await page.locator('#close-settings').click();
    // The original fault: an enabled historical binding contains no actions.
    // Repair upgrades it in one explicit action; the switch stays enabled.
    if (!(await page.locator('#agent-tab-platforms').isVisible())) await page.getByRole('button',{name:'展开助理详情',exact:true}).click();
    await page.locator('#agent-tab-platforms').click();
    const row = page.locator(`#member-notification-platforms [data-platform-id="${platformID}"]`);
    const toggle = row.getByRole('switch',{name:'工作飞书 使用工具'});
    assert.equal(await toggle.isChecked(),true);
    if (goos === 'darwin') assert.doesNotMatch(await row.textContent(),/不支持发送/);
    await row.getByRole('button',{name:'修复能力',exact:true}).click();
    await row.getByText(/工具已启用/).waitFor();
    assert.equal(await toggle.isChecked(),true);
    const after = await bindings();
    assert.deepEqual(after.bindings[0].action_authorization.grants.map(g => g.operation),['message.reply','message.send','message.send_self']);
    assert.equal(writes.length,1);
    assert.equal(writes[0].path,'/v1/tools/bind');
    assert.equal(writes[0].body.binding.capability_evidence,undefined);
    assert.equal(writes[0].body.binding.action_authorization,undefined);
    assert.equal(await page.locator('#platforms-member-view').evaluate(el => el.scrollWidth > el.clientWidth),false);
    await capture('member-repaired');
    assert.deepEqual(unexpected,[],'browser contacted an external service');
    assert.deepEqual(errors,[]);
    console.log(`PASS ${viewport}: original-card authorization, shared impact, QR, reload, settings close, automatic check, read-only preservation, one member repair, no overflow; synthetic CLI, native writes=0`);
  } finally { await browser.close(); }
})().catch(e => {console.error(e);process.exitCode=1;});
