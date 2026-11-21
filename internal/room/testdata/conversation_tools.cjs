const assert = require('node:assert/strict');

async function openCalendar(page) {
  if (await page.locator('#conversation-calendar').isVisible()) return page.locator('#conversation-calendar').click();
  await page.locator('#room-menu-label').click();
  await page.locator('#conversation-open-calendar').click();
}

async function verifyConversationTools(page, shot) {
  const ids = ['conversation-files','conversation-calendar','conversation-details'];
  assert.deepEqual(await page.locator('[data-conversation-shortcut]').evaluateAll(nodes=>nodes.map(node=>node.id)),ids);
  for (const id of ids) assert.equal(await page.locator('#'+id).isVisible(),true);
  const geometry = await page.locator('[data-conversation-shortcut]').evaluateAll(nodes=>nodes.map(node=>node.getBoundingClientRect().toJSON()));
  for (let i=1;i<geometry.length;i++) assert.ok(geometry[i-1].right<=geometry[i].left);
  assert.equal(await page.locator('#conversation-details').getAttribute('title'),'设置');
  assert.equal(await page.locator('#conversation-details path').getAttribute('d'),await page.locator('#open-settings path').getAttribute('d'));
  assert.equal(await page.locator('#conversation-files').getAttribute('title'),'文件');
  assert.equal(await page.locator('#activity-title').innerText(),'设置');
  assert.equal(await page.locator('#agent-context-name').isVisible(),true);
  await shot('desktop-settings');
  await page.locator('#conversation-files').click();await page.locator('#files-page').waitFor();
  assert.equal(await page.locator('#files-title').innerText(),'文件');await page.locator('#files-close').click();
  await page.locator('#room-menu-label').click();
  assert.deepEqual(await page.locator('.conversation-menu-action > span').allTextContents(),['文件','日历','设置']);
  await shot('desktop-conversation-tools-menu');
  for (const kind of ['files','calendar','settings']) {
    await page.locator('#conversation-pin-'+kind).click();
    assert.equal(await page.locator('#conversation-menu').isVisible(),true);
    assert.equal(await page.locator('#conversation-pin-'+kind).getAttribute('aria-checked'),'false');
    assert.equal(await page.locator('#conversation-tool-'+kind).isVisible(),true);
  }
  for (const id of ids) assert.equal(await page.locator('#'+id).isVisible(),false);
  assert.equal(await page.locator('#calendar-page').isVisible(),false);
  assert.equal(await page.locator('#files-page').isVisible(),false);
  await page.keyboard.press('Escape');await page.reload();await page.locator('#room-menu-label:not(:disabled)').waitFor();
  for (const id of ids) assert.equal(await page.locator('#'+id).isVisible(),false);
  await openCalendar(page);await page.locator('.calendar-time-grid').waitFor();
  await page.locator('#calendar-close').click();
  assert.equal(await page.locator('#room-menu-label').evaluate(node=>node===document.activeElement),true);
  await page.locator('#room-menu-label').click();
  for (const kind of ['files','calendar','settings']) await page.locator('#conversation-pin-'+kind).click();
  await page.keyboard.press('Escape');
  await page.locator('#room-menu-label').focus();await page.keyboard.press('ArrowDown');
  assert.equal(await page.locator('#conversation-open-files').evaluate(node=>node===document.activeElement),true);
  await page.keyboard.press('ArrowDown');await page.keyboard.press('ArrowRight');await page.keyboard.press('Enter');
  assert.equal(await page.locator('#conversation-pin-calendar').getAttribute('aria-checked'),'false');
  assert.equal(await page.locator('#conversation-menu').isVisible(),true);
  await page.keyboard.press('Space');assert.equal(await page.locator('#conversation-pin-calendar').getAttribute('aria-checked'),'true');
  await page.keyboard.press('Escape');
  for (const width of [390,320]) {
    await page.setViewportSize({width,height:844});
    for (const id of ids) assert.equal(await page.locator('#'+id).isVisible(),false);
    await page.locator('#room-menu-label').click();
    for (const kind of ['files','calendar','settings']) {
      assert.equal(await page.locator('#conversation-tool-'+kind).isVisible(),true);
      assert.equal(await page.locator('#conversation-pin-'+kind).isVisible(),false);
    }
    await shot('mobile-conversation-tools-'+width);
    await page.locator('#conversation-open-calendar').click();await page.locator('#calendar-page').waitFor();
    await page.locator('#calendar-close').click();
    const bounds = await page.locator('#room-header').evaluate(header=>{
      const title=header.querySelector('.room-title').getBoundingClientRect(),frame=header.getBoundingClientRect();
      return {offset:(title.left+title.right-frame.left-frame.right)/2,overflow:document.documentElement.scrollWidth>innerWidth};
    });
    assert.ok(Math.abs(bounds.offset)<2);assert.equal(bounds.overflow,false);
    await page.locator('#room-menu-label').click();await page.locator('#conversation-open-details').click();
    await page.locator('#activity-dialog').waitFor();
    assert.equal(await page.locator('#activity-title').innerText(),'设置');
    assert.equal(await page.locator('#agent-context-name').isVisible(),true);
    await shot('mobile-settings-'+width);await page.locator('#close-activity').click();
    await page.waitForFunction(()=>!document.querySelector('#conversation').inert);
  }
  await page.setViewportSize({width:1440,height:1000});
  for (const id of ids) await page.locator('#'+id).waitFor({state:'visible'});
}

module.exports = {openCalendar, verifyConversationTools};
