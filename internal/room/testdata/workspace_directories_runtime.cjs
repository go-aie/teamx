const {chromium} = require('playwright');
const assert = require('node:assert/strict');

// The caller owns the isolated serve process, model turns and disk assertions.
// This driver applies settings exclusively through the production webpage.
(async () => {
  const [endpoint, raw] = process.argv.slice(2), f = JSON.parse(raw);
  const browser = await chromium.launch({headless: true});
  const page = await browser.newPage({viewport: {width: 1440, height: 1000}});
  const errors = [], requests = [];
  page.on('pageerror', e => errors.push(e.message));
  page.on('request', r => {
    if (new URL(r.url()).pathname === '/v1/workspace/apply') requests.push(r.postDataJSON());
  });
  page.setDefaultTimeout(30000);
  try {
    await page.goto(endpoint);
    await page.locator('#room-list .room-choice').filter({hasText: 'Workspace directories 验收'}).click();
    if (f.mode === 'repair') {
      // The room has no live member service; its catalog-level Workspace UI
      // must remain usable to repair all saved invalid overrides.
      await page.waitForFunction(() => document.querySelector('#room-name').textContent === 'Workspace directories 验收' && document.querySelector('#connection').textContent.includes('可查看历史并修复'));
      await page.locator('#message-1').waitFor({state: 'visible'});
      assert.equal(await page.locator('#send').isDisabled(), true);
      await page.screenshot({animations: 'disabled', path: `${f.screenshots}/repair-history.png`});
      await page.locator('#compose-more').click();
      await page.locator('#open-workspace').click();
      await page.locator('#workspace-directories-default').click();
      await page.locator('#workspace-directories-apply').click();
    } else {
      await page.waitForFunction(() => !document.querySelector('#message-input').disabled);
      await page.locator('#members .member-summary').nth(f.index).click();
      if (await page.locator('#toggle-agent-controls').getAttribute('aria-expanded') !== 'true') {
        await page.locator('#toggle-agent-controls').click();
      }
      if (f.mode === 'default') {
        await page.locator('#member-directory-default').click();
      } else {
        await page.locator('#member-directory-choose').click();
        // Navigate to the allowed root, then enter the test folder by button.
        await page.locator('#directory-path').fill(f.root);
        await page.locator('#directory-path').press('Enter');
        await page.waitForFunction(path => document.querySelector('#directory-current').title === path, f.root);
        await page.locator('#directory-entries button').getByText(f.path.split('/').pop(), {exact: true}).click();
        await page.waitForFunction(path => document.querySelector('#directory-current').title === path, f.path);
        await page.screenshot({animations: 'disabled', path: `${f.screenshots}/choose-${f.index}-${requests.length}-${f.path.split('/').pop()}.png`});
        await page.locator('#directory-select').click();
      }
      assert.equal(requests.length, 0, 'draft must not apply until clicked');
      await page.locator('#member-directory-apply').click();
    }
    assert.equal(requests.length, 1);
    const query = new URLSearchParams({service_epoch: f.epoch, caller_id: f.caller, operation_id: requests[0].operation_id, ...f.room});
    await page.waitForFunction(async ({query}) => {
      const response = await fetch('/v1/workspace?' + query);
      if (!response.ok) return false;
      const data = await response.json();
      if (data.operation?.status === 'failed') throw new Error(data.operation.error_code);
      return data.operation?.status === 'completed';
    }, {query: query.toString()});
    assert.equal(requests.length, 1);
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({browser: 'Chromium', model: 'real DSH; owned by Go driver', mode: f.mode, member_index: f.index, selected_path: f.path, operation_id: requests[0].operation_id, page_errors: errors}));
  } catch (error) {
    console.error(await page.locator('body').innerText());
    await page.screenshot({animations: 'disabled', path: `${f.screenshots}/browser-failure.png`});
    throw error;
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
