const {toggleConversationDetails}=require('./conversation_details.cjs');
// Actual Chromium and production HTTP. Competing writes use the human endpoints.
const {chromium} = require('playwright');
const assert = require('node:assert/strict');
const {mkdir, writeFile} = require('node:fs/promises');
const {join} = require('node:path');

(async () => {
  const [endpoint, instance, larkID, gitlabID, agent, viewport] = process.argv.slice(2);
  const output = process.env.TEAMX_CONFIGURATION_ARTIFACTS;
  if (output) await mkdir(output, {recursive:true});
  const browser = await chromium.launch({headless:true});
  const errors = [], writes = [], external = [], dialogs = [];
  try {
    const page = await browser.newPage({viewport:viewport === 'mobile' ? {width:390,height:844} : {width:1440,height:1100}});
    page.setDefaultTimeout(15000);
    page.on('pageerror', e => errors.push(e.message));
    page.on('dialog', async d => {dialogs.push(d.message());await d.dismiss();});
    page.on('request', r => {
      const url = new URL(r.url());
      if (url.origin !== endpoint) external.push(url.origin);
      if (r.method() === 'POST' && ['/v1/tools/bind','/v1/platform/notifications/configure','/v1/policy/update'].includes(url.pathname)) writes.push({path:url.pathname, body:r.postDataJSON()});
    });
    const call = async (path, body) => {
      const response = await fetch(endpoint+path, body && {method:'POST',headers:{'Content-Type':'application/json',Origin:endpoint},body:JSON.stringify(body)});
      const data = await response.json();
      assert.equal(response.status,200,JSON.stringify(data));
      return data;
    };
    const catalog = await call('/v1/rooms');
    const room = catalog.rooms.find(r => r.room_instance === instance);
    assert.ok(room?.service);
    const base = {...room.service,caller:{kind:'human',id:catalog.operator}};
    const members = await call('/v1/member/list',base);
    const member = members.members.find(m => m.agent_id === agent);
    const context = {...base,agent_id:agent,membership_id:member.membership_id};
    const capture = name => output ? page.screenshot({path:join(output,`${viewport}-${name}.png`),fullPage:true,animations:'disabled'}) : undefined;
    await page.addInitScript(selection => sessionStorage.setItem('teamx.selected-room',JSON.stringify(selection)),{room_id:room.room_id,room_instance:instance});
    await page.goto(endpoint);
    if(viewport === 'mobile')await page.locator('#room-list .room-choice').filter({hasText:room.name}).first().click();
    if(viewport === 'mobile') {
      await toggleConversationDetails(page);
      if(await page.locator('#toggle-member-list').getAttribute('aria-expanded') === 'false')await page.locator('#toggle-member-list').click();
    }
    await page.locator('.member-summary').filter({hasText:agent}).first().click();
    await page.locator('#agent-tab-platforms').click();
    // Both targets were observed absent. An unrelated tool save must not block.
    for(const [id,name] of [[larkID,'飞书'],[gitlabID,'GitLab']]) {
      const row = page.locator(`#member-notification-platforms [data-platform-id="${id}"]`);
      const toggle = row.getByRole('switch',{name:new RegExp('使用工具$')});
      assert.equal(await toggle.isChecked(),false,name);
      const saved = page.waitForResponse(r=>r.url().endsWith('/v1/tools/bind') && r.request().method()==='POST');
      await toggle.click();
      assert.equal((await saved).status(),200);
      await row.getByText(/工具已启用/).waitFor();
    }
    assert.equal(writes.length,2);
    assert.deepEqual(writes.map(w=>w.body.expected_binding_revision),[0,0]);
    for(const w of writes)assert.equal('revision' in w.body,false);
    await capture('tools-enabled');
    // A full notification draft based on A cannot erase B added after reading.
    await page.getByRole('button',{name:'编辑 飞书 的通知设置',exact:true}).click();
    await page.waitForFunction(()=>!document.getElementById('lark-fields').disabled);
    await page.locator('#lark-groups-settings').evaluate(e=>e.open=true);
    assert.equal(await page.locator('#lark-group-mode').inputValue(),'mentions');
    await page.locator('#lark-group-mode').selectOption('all');
    await page.route('**/v1/platform/notifications/configure',async route=>{
      const original = route.request().postDataJSON();
      assert.equal(original.subscription_version,1);
      assert.deepEqual(original.config.groups.chat_ids,['oc_group_one']);
      const config = structuredClone(original.config);
      config.groups.chat_ids.push('oc_group_two');
      config.groups.message_mode='mentions';
      await call('/v1/platform/notifications/configure',{...original,config});
      await route.continue();
    },{times:1});
    const conflict = page.waitForResponse(r=>r.url().endsWith('/v1/platform/notifications/configure') && r.request().method()==='POST');
    await page.locator('#lark-save').click();
    assert.equal((await conflict).status(),409);
    await page.locator('#lark-error').filter({hasText:'草稿已保留'}).waitFor();
    assert.equal(await page.locator('#lark-group-mode').inputValue(),'all');
    assert.equal(await page.locator('#notification-detail').evaluate(e=>e.scrollWidth>e.clientWidth+1),false);
    await capture('notification-conflict');
    await page.locator('#notification-detail').evaluate(e=>e.close());
    await page.locator('#agent-tab-policy').click();
    const style = page.locator('[data-policy-id="style"]');
    await style.getByRole('button',{name:/编辑规则/}).click();
    await page.locator('#policy-text').fill('中文简明，并保留限制');
    await call('/v1/policy/update',{...context,policy:{id:'order',version:1,status:'active',text:'结论后给理由',source_text:'补充独立规则'}});
    let saved = page.waitForResponse(r=>r.url().endsWith('/v1/policy/update') && r.request().method()==='POST');
    await page.locator('#policy-save').click();
    assert.equal((await saved).status(),200,'unrelated rule blocked the local draft');
    await style.locator('.policy-entry-body').filter({hasText:'中文简明，并保留限制'}).waitFor();
    await style.getByRole('button',{name:/编辑规则/}).click();
    await page.locator('#policy-text').fill('尚未保存的本地规则草稿');
    await call('/v1/policy/update',{...context,policy:{id:'style',version:2,status:'active',text:'另一入口保存的最新规则',source_text:'同一规则的新修改'}});
    saved = page.waitForResponse(r=>r.url().endsWith('/v1/policy/update') && r.request().method()==='POST');
    await page.locator('#policy-save').click();
    assert.equal((await saved).status(),409);
    await page.locator('#policy-form-status').filter({hasText:'保存失败'}).waitFor();
    assert.equal(await page.locator('#policy-text').inputValue(),'尚未保存的本地规则草稿');
    assert.equal(await page.locator('#policy-save').isDisabled(),true);
    await capture('rule-conflict');
    const feedback = await page.locator('#policy-form-feedback').boundingBox();
    const saveButton = await page.locator('#policy-save').boundingBox();
    assert.ok(feedback && saveButton && feedback.y+feedback.height <= saveButton.y+1 && saveButton.y-feedback.y-feedback.height < 40,'save error is not beside the save actions');
    assert.ok(feedback.y >= 0 && feedback.y+feedback.height <= page.viewportSize().height,`save error was left outside the viewport: ${JSON.stringify(feedback)}`);
    assert.ok(saveButton.y+saveButton.height <= page.viewportSize().height,'save actions were left outside the viewport');
    assert.equal(await page.locator('#policy-form-retry').isVisible(),true);
    assert.equal(await page.locator('#policy-retry').isVisible(),false);
    assert.equal(await page.locator('#activity-dialog').evaluate(e=>e.scrollWidth>e.clientWidth+1),false);
    await page.locator('#policy-form-retry').click();
    await page.locator('#policy-form-status').filter({hasText:'已重新读取当前版本'}).waitFor();
    assert.equal(await page.locator('#policy-text').inputValue(),'尚未保存的本地规则草稿');
    await style.locator('.policy-entry-body').filter({hasText:'另一入口保存的最新规则'}).waitFor();
    assert.equal(await page.locator('#policy-save').isDisabled(),false);
    assert.equal(writes.filter(w=>w.path==='/v1/platform/notifications/configure').length,1);
    assert.equal(writes.filter(w=>w.path==='/v1/policy/update').length,2);
    for(const w of writes.filter(w=>w.path==='/v1/policy/update'))assert.equal('revision' in w.body.policy,false);
    assert.deepEqual(external,[]);
    assert.deepEqual(errors,[]);
    assert.deepEqual(dialogs,[],'unexpected routine approval');
    if(output)await writeFile(join(output,`${viewport}-browser.json`),JSON.stringify({status:'passed',provider:'isolated fixtures',writes,errors,external,dialogs},null,2),{mode:0o600});
    console.log(`PASS ${viewport}: dual tools, stale notification rejection, independent rule save, same-rule conflict, drafts retained, no replay or approval, no overflow`);
  } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
