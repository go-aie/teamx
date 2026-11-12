// Real Chromium/production UI. Provider mode is recorded by the Go harness.
const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');

(async () => {
  const [endpoint, inputPath, output] = process.argv.slice(2);
  const input = JSON.parse(await fs.readFile(inputPath, 'utf8'));
  const expected = input.state.config;
  const browser = await chromium.launch({ headless: true });
  try {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1100 } });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    page.setDefaultTimeout(15000);
    const catalog = await (await fetch(endpoint + '/v1/rooms')).json();
    const room = catalog.rooms.find(room => room.room_instance === input.room);
    assert.ok(room, 'exact acceptance Room exists');
    await context.addInitScript(selection => sessionStorage.setItem('teamx.selected-room', JSON.stringify(selection)), {
      room_id: room.room_id, room_instance: room.room_instance,
    });
    await page.goto(endpoint);
    // The acceptance member is added after the fixture team's members.
    await page.locator('.member-summary').filter({ hasText: 'assistant' }).click();
    await page.locator('#agent-tab-platforms').click();
    await page.getByRole('button', { name: `编辑 ${input.platform_name || '飞书'} 的通知设置`, exact: true }).click();
    await page.waitForFunction(() => !document.getElementById('lark-fields').disabled);
    await page.locator('#lark-groups-settings').evaluate(element => { element.open = true; });
    await page.locator('#lark-private-settings').evaluate(element => { element.open = true; });
    for (const [selector, value] of [
      ['#lark-enabled', expected.enabled],
      ['#lark-private-enabled', expected.private.enabled],
      ['#lark-scope', expected.private.scope === 'all'],
      ['#lark-groups-enabled', expected.groups.enabled],
      ['#lark-groups-scope', expected.groups.scope === 'all'],
      ['#lark-group', expected.groups.at_me],
      ['#lark-all', expected.groups.at_all],
    ]) assert.equal(await page.locator(selector).isChecked(), !!value, selector);
    assert.equal(await page.locator('#lark-group-mode').inputValue(), expected.groups.message_mode);
    for (const [kind, ids] of [['groups', expected.groups.chat_ids || []], ['private', expected.private.chat_ids || []]]) {
      await page.locator(`#lark-${kind}-chat-picker`).evaluate(element => { element.open = true; });
      const selected = page.locator(`#lark-${kind}-chat-selected button`);
      assert.equal(await selected.count(), ids.length, `${kind}: complete selected list`);
      for (const id of ids) {
        assert.ok((await selected.evaluateAll(buttons => buttons.map(button => button.outerHTML))).some(html => html.includes(id)), `${kind}: selected ${id}`);
      }
    }
    await page.screenshot({ path: path.join(output, 'settings-desktop.png'), fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    assert.equal(await page.locator('#notification-detail').evaluate(element => element.scrollWidth > element.clientWidth + 1), false);
    await page.screenshot({ path: path.join(output, 'settings-mobile.png'), fullPage: true });
    await page.setViewportSize({ width: 1440, height: 1100 });
    await page.locator('#lark-all').setChecked(!expected.groups.at_all);
    const saved = page.waitForResponse(response => response.url().endsWith('/v1/platform/notifications/configure') && response.request().method() === 'POST');
    await page.locator('#lark-save').click();
    const response = await saved;
    assert.equal(response.status(), 200);
    const result = await response.json();
    assert.equal(result.subscription_version, input.state.subscription_version + 1);
    assert.equal(result.config.groups.at_all, !expected.groups.at_all);
    // The human form serializes an absent selection as []; both null and []
    // represent the same empty set. Nonempty personnel IDs must remain exact.
    assert.deepEqual(
      { ...result.config.private, chat_ids: result.config.private.chat_ids || [] },
      { ...expected.private, chat_ids: expected.private.chat_ids || [] },
    );
    assert.equal(result.config.backfill_start, expected.backfill_start);
    assert.deepEqual(errors, []);
    await fs.writeFile(path.join(output, 'browser-report.json'), JSON.stringify({
      status: 'passed', version_before: input.state.subscription_version,
      version_after: result.subscription_version, desktop: true, mobile: true,
      independent_save: true, page_errors: errors,
    }, null, 2), { mode: 0o600 });
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
