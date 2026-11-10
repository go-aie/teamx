// Run from cua_repl after its browser documentation has been read:
// const {verifyMemberNotificationsBrowser}=await import(ABS_PATH);
// await verifyMemberNotificationsBrowser({browser,tab,output:ABS_OUTPUT});
// Uses the real backend announced by TestMemberNotificationsReal. All writes go
// through visible controls; the driver never calls management APIs directly.
import assert from 'node:assert/strict';
import {readFile,writeFile} from 'node:fs/promises';
import {isAbsolute,join} from 'node:path';

export async function verifyMemberNotificationsBrowser({browser,tab,output,driver='cua_repl',errors=[]}) {
  assert.ok(isAbsolute(output));
  const ready=JSON.parse(await readFile(join(output,'browser-ready.json'),'utf8'));
  assert.equal(ready.phase,'final','wait for collectors to settle before UI mutations');
  assert.ok(ready.gitlab_platform,'this browser scenario requires real GitLab and Lark');
  const p=tab.playwright;
  const checks=[];
  const locator=id=>p.locator(`#${id}`);
  const select=async label=>{
    const member=ready.members.find(m=>m.agent_id===label);
    await locator('activity-member').selectOption(member.membership_id);
    await p.getByRole('button',{name:`启用协作 ${label}`,exact:true}).waitFor({state:'visible',timeoutMs:15000});
  };
  const checked=l=>l.evaluate(e=>e.checked);
  const row=id=>p.locator(`#member-notification-platforms [data-platform-id="${id}"]`);
  const screenshot=name=>tab.screenshot({fullPage:true}).then(data=>writeFile(join(output,name),data));
  const settled=async id=>{
    await p.locator(`#member-notification-platforms [data-platform-id="${id}"] input:not(:disabled)`).first().waitFor({state:'visible',timeoutMs:15000});
    await p.locator(`#member-notification-platforms [data-platform-id="${id}"] input:disabled`).waitFor({state:'hidden',timeoutMs:15000});
  };
  await tab.goto(ready.endpoint+'/');
  await p.getByRole('button',{name:/^M5 通知验收 运行中/}).click();
  await p.getByRole('button',{name:'查看 assistant 的详情',exact:true}).click();
  assert.equal(JSON.stringify(await p.locator('#agent-tabs [role="tab"]').allTextContents({})),JSON.stringify(['平台','长期规则','收件箱','活动记录']));
  assert.equal(await locator('agent-tab-platforms').getAttribute('aria-selected'),'true');
  assert.equal(await locator('agent-tab-tools').count(),0);
  checks.push('member_platforms');

  await locator('agent-tab-inbox').click();
  assert.equal(await locator('member-notification-sources').isVisible(),false);
  await locator('inbox-next').click();
  await locator('inbox-page').filter({hasText:'第 2 页'}).waitFor({state:'visible',timeoutMs:15000});
  await locator('inbox-prev').press('PageDown');
  await p.waitForTimeout(350); // Let native smooth scrolling settle before comparing positions.
  assert.equal(await locator('inbox-page').innerText(),'第 2 页');
  const entries=await locator('inbox-entries').innerText();
  const scroll=await locator('inbox-member-view').evaluate(e=>e.scrollTop);
  assert.ok(scroll>0);
  await screenshot('browser-inbox-page2.png');
  await locator('agent-tab-platforms').click();
  await row(ready.gitlab_platform).locator('.notification-source').click();
  await locator('notification-panel-gitlab').waitFor({state:'visible',timeoutMs:15000});
  assert.match(await locator('notification-title').innerText(),/^assistant · /);
  assert.equal(await locator('gitlab-target').isVisible(),false);
  await locator('close-notification').click();
  await locator('agent-tab-inbox').click();
  assert.equal(await locator('inbox-page').innerText(),'第 2 页');
  assert.equal(await locator('inbox-entries').innerText(),entries);
  assert.equal(await locator('inbox-member-view').evaluate(e=>e.scrollTop),scroll);
  checks.push('detail_return');

  await locator('agent-tab-platforms').click();
  await select('reviewer');
  await row(ready.gitlab_platform).locator('.notification-source').click();
  await locator('notification-panel-gitlab').waitFor({state:'visible',timeoutMs:15000});
  assert.match(await locator('notification-title').innerText(),/^reviewer · /);
  await p.locator('#notification-panel-gitlab .notification-advanced > summary').click();
  const interval=await locator('gitlab-interval').evaluate(e=>e.value);
  await locator('gitlab-interval').fill('90');
  await locator('notification-enabled').click();
  await p.locator('#notification-enabled:not(:disabled)').waitFor({state:'visible',timeoutMs:15000});
  assert.equal(await checked(locator('notification-enabled')),false);
  assert.equal(await locator('gitlab-interval').evaluate(e=>e.value),'90');
  await locator('notification-enabled').click();
  await p.locator('#notification-enabled:not(:disabled)').waitFor({state:'visible',timeoutMs:15000});
  await locator('close-notification').click();
  checks.push('draft_not_submitted');

  // Lark's existing binding begins with both enabled. Exercise all four combinations.
  const switches=()=>row(ready.lark_platform).locator('input[role="switch"]');
  const combination=async()=>[await checked(switches().nth(0)),await checked(switches().nth(1))];
  assert.deepEqual(await combination(),[true,true]);
  for(const [index,expected] of [[0,[false,true]],[1,[false,false]],[0,[true,false]],[1,[true,true]]]) {
    await switches().nth(index).click();
    await settled(ready.lark_platform);
    assert.deepEqual(await combination(),expected);
  }
  await select('assistant');
  assert.equal(await checked(row(ready.gitlab_platform).locator('input[role="switch"]').first()),true);
  await select('reviewer');
  assert.equal(await checked(row(ready.gitlab_platform).locator('input[role="switch"]').first()),true);
  checks.push('independent_switches','member_isolation');

  await locator('agent-tab-platforms').press('ArrowRight');
  assert.equal(await locator('agent-tab-policy').getAttribute('aria-selected'),'true');
  await locator('agent-tab-policy').press('End');
  assert.equal(await locator('agent-tab-activity').getAttribute('aria-selected'),'true');
  await locator('agent-tab-activity').press('Home');
  assert.equal(await locator('agent-tab-platforms').getAttribute('aria-selected'),'true');
  await locator('member-notification-collapse').press('Enter');
  assert.equal(await locator('member-notification-collapse').getAttribute('aria-expanded'),'false');
  await locator('member-notification-collapse').press('Enter');
  checks.push('keyboard');
  await screenshot('browser-platforms-desktop.png');

  await select('defaults');
  await locator('agent-tab-inbox').click();
  assert.equal(await locator('inbox-add-platform').isVisible(),true);
  await locator('inbox-add-platform').click();
  await locator('member-notification-add-search').fill('M5');
  const option=p.locator(`#member-notification-add-options [data-platform-id="${ready.lark_platform}"]`);
  await option.waitFor({state:'visible',timeoutMs:15000});
  await option.hover();
  await option.getByRole('button',{name:/添加并启用/}).click({timeoutMs:15000});
  await row(ready.lark_platform).waitFor({state:'visible',timeoutMs:30000});
  await settled(ready.lark_platform);
  assert.equal(await checked(row(ready.lark_platform).locator('input[role="switch"]').nth(0)),true);
  assert.equal(await checked(row(ready.lark_platform).locator('input[role="switch"]').nth(1)),true);
  await locator('agent-tab-inbox').click();
  assert.equal(await locator('inbox-add-platform').isVisible(),false);
  checks.push('add_existing_defaults','inbox_empty_states');

  const viewport=await browser.capabilities.get('viewport');
  await viewport.set({width:390,height:900});
  try {
    await locator('agent-tab-platforms').click();
    assert.equal(await p.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
    await row(ready.lark_platform).locator('.notification-source').click();
    await locator('notification-panel-messages').waitFor({state:'visible',timeoutMs:15000});
    assert.match(await locator('notification-title').innerText(),/^defaults · M5 飞书/);
    await screenshot('browser-detail-390.png');
    await locator('close-notification').click();
    await locator('activity-dialog').waitFor({state:'visible',timeoutMs:15000});
    await locator('platforms-member-view').waitFor({state:'visible',timeoutMs:15000});
    await screenshot('browser-platforms-390.png');
    checks.push('narrow');
  } finally {await viewport.reset();}
  // Separate Room target; the member selector is absent and only GitLab is offered.
  await locator('close-activity').click();
  await p.locator('#notification-platforms .notification-source').first().click();
  await locator('notification-panel-gitlab').waitFor({state:'visible',timeoutMs:15000});
  assert.match(await locator('notification-title').innerText(),/房间/);
  assert.equal(await locator('gitlab-target').isVisible(),false);
  await locator('close-notification').click();
  checks.push('room_target');
  assert.equal(errors.length,0,'browser JavaScript errors invalidate acceptance');
  const report={status:'passed',driver,room:ready.room,members:ready.members,checks,draft:{agent:'reviewer',platform:ready.gitlab_platform,saved_interval:Number(interval),unsaved_interval:90},unexecuted:['new account OAuth/CLI login','two accounts of the same provider','live partial failure recovery','non-platform/no-notification tool catalog'],screenshots:['browser-inbox-page2.png','browser-platforms-desktop.png','browser-detail-390.png','browser-platforms-390.png']};
  await writeFile(join(output,'browser-done.json'),JSON.stringify(report,null,2),{mode:0o600});
  return report;
}
