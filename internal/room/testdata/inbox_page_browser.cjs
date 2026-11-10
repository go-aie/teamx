// Production UI + real local manager/Inbox actors. Synthetic input, no platform writes.
const {chromium}=require('playwright');
const fs=require('node:fs/promises');
const path=require('node:path');
const assert=require('node:assert/strict');
(async()=>{
 const ready=JSON.parse(await fs.readFile(process.argv[2],'utf8')),output=process.argv[3];
 const browser=await chromium.launch({headless:true}),errors=[],checks=[];
 const page=await browser.newPage({viewport:{width:1440,height:1000}});
 page.setDefaultTimeout(12000);page.on('pageerror',e=>errors.push(e.message));
 const first=ready.targets[0].members[0],second=ready.targets[0].members[1];
 const command=async input=>{const response=await page.request.post(ready.endpoint+'/__inbox_fixture',{data:{room:0,member:0,...input}});assert.equal(response.status(),200,await response.text());};
 const seqs=()=>page.locator('#inbox-entries > section').evaluateAll(rows=>rows.map(row=>Number(row.dataset.inboxSeq)));
 const waitLast=seq=>page.waitForFunction(seq=>document.querySelector('#inbox-entries > section:last-child')?.dataset.inboxSeq===String(seq),seq);
 const waitFirst=seq=>page.waitForFunction(seq=>document.querySelector('#inbox-entries > section')?.dataset.inboxSeq===String(seq),seq);
 const click=id=>page.locator('#'+id).click();
 const snapshot=()=>page.evaluate(()=>({scroll:document.querySelector('#inbox-member-view').scrollTop,open:document.querySelector('#inbox-entries details')?.open,focus:document.activeElement?.outerHTML}));
 const openRoom=async label=>{await page.getByRole('button',{name:new RegExp('^'+label+' 运行中')}).click();await page.locator('#members .member-summary').first().click();await click('agent-tab-inbox');};
 try {
  await page.goto(ready.endpoint);await openRoom('inbox-browser-A');await waitLast(60);
  assert.deepEqual(await seqs(),Array.from({length:25},(_,i)=>i+36));
  assert.equal(await page.locator('#inbox-page').textContent(),'第 1 页');
  await page.locator('#inbox-status').filter({hasText:'保留 60 条'}).waitFor();checks.push('latest_page');
  await click('inbox-next');await waitFirst(11);
  const summary=page.locator('#inbox-entries details summary').first();await summary.click();
  await page.locator('#inbox-member-view').evaluate(el=>el.scrollTop=300);await summary.focus();
  const before=await snapshot(),reads=[];
  const countRead=r=>{if(new URL(r.url()).pathname==='/v1/member/inbox')reads.push(r.url());};page.on('request',countRead);
  await command({count:3});
  await page.locator('#inbox-latest').filter({hasText:'有更新'}).waitFor();
  assert.equal(await page.locator('#inbox-page').textContent(),'第 2 页');
  assert.equal(reads.length,0);assert.deepEqual(await snapshot(),before);assert.equal((await seqs())[0],11);
  await page.locator('#inbox-status').filter({hasText:'保留 63 条'}).waitFor();checks.push('automatic_notice_history_focus_scroll');
  // Programmatic click keeps the actual keyboard focus inside the source details.
  const refreshed=page.waitForResponse(r=>new URL(r.url()).pathname==='/v1/member/inbox');
  await page.locator('#inbox-reload').evaluate(el=>el.click());await refreshed;
  await page.waitForFunction(()=>document.querySelector('#inbox-entries').getAttribute('aria-busy')==='false');
  assert.deepEqual(await snapshot(),before);assert.equal((await seqs())[0],11);assert.equal(await page.locator('#inbox-page').textContent(),'第 2 页');checks.push('manual_refresh_preserves_position');
  await click('inbox-latest');await waitLast(63);assert.equal((await seqs())[0],39);assert.equal(await page.locator('#inbox-page').textContent(),'第 1 页');checks.push('explicit_latest_new_snapshot');
  await click('inbox-next');await waitFirst(14);
  await command({through:20});await waitFirst(39);
  assert.equal(await page.locator('#inbox-page').textContent(),'第 1 页');
  await page.locator('#inbox-notice').filter({hasText:'已过期'}).waitFor();await page.locator('#inbox-status').filter({hasText:'保留 43 条'}).waitFor();checks.push('background_reclamation');
  // Cleanup between inspection and page read must invalidate and relocate.
  let race=true;
  await page.route('**/v1/member/inbox',async route=>{if(race){race=false;await command({through:45});}await route.continue();});
  await click('inbox-reload');await waitFirst(46);
  assert.equal(await page.locator('#inbox-page').textContent(),'第 1 页');await page.unroute('**/v1/member/inbox');checks.push('cleanup_during_read');
  await page.locator('#inbox-member-view').screenshot({path:path.join(output,'desktop.png')});
  // Delayed response from another membership must not replace the selected page.
  let release,held;
  await page.route('**/v1/member/inbox',async route=>{
   const body=route.request().postDataJSON();
   if(body.membership_id===first.membership_id && !held){held=true;const result=await route.fetch();await new Promise(r=>release=r);await route.fulfill({response:result});}
   else await route.continue();
  });
  await page.locator('#inbox-reload').evaluate(el=>el.click());await page.waitForFunction(()=>document.querySelector('#inbox-entries').getAttribute('aria-busy')==='true');
  while(!release)await new Promise(r=>setTimeout(r,20));
  await click('close-activity');await page.locator(`#members .member-summary[data-activity-member="${second.membership_id}"]`).click();await click('agent-tab-inbox');await waitLast(2);release();
  await page.waitForTimeout(200);assert.deepEqual(await seqs(),[1,2]);checks.push('late_membership_response');
  await page.unroute('**/v1/member/inbox');
  await click('close-activity');await page.locator(`#members .member-summary[data-activity-member="${first.membership_id}"]`).click();await click('agent-tab-inbox');await waitLast(63);
  // Status has its own context guard as well as page reads.
  let statusRelease;
  await page.route('**/v1/member/inbox/status',async route=>{
   if(!statusRelease){const result=await route.fetch();await new Promise(r=>statusRelease=r);await route.fulfill({response:result});}
   else await route.continue();
  });
  while(!statusRelease)await new Promise(r=>setTimeout(r,50));
  await click('close-activity');await openRoom('inbox-browser-B');await waitLast(2);statusRelease();
  await page.waitForTimeout(200);assert.deepEqual(await seqs(),[1,2]);checks.push('late_room_status');
  await page.unroute('**/v1/member/inbox/status');
  await page.setViewportSize({width:390,height:844});
  assert.equal(await page.locator('#inbox-member-view').evaluate(el=>el.scrollWidth>el.clientWidth),false);
  await page.locator('#inbox-member-view').screenshot({path:path.join(output,'mobile.png')});checks.push('mobile_layout');
  // Restore A and remove all retained records, including the currently visible page.
  await click('close-activity');await openRoom('inbox-browser-A');await waitLast(63);
  await command({through:63});
  await page.waitForFunction(()=>document.querySelectorAll('#inbox-entries > section').length===0);
  await page.locator('#inbox-status').filter({hasText:'保留 0 条'}).waitFor();checks.push('empty_after_cleanup');
  assert.deepEqual(errors,[]);
  await fs.writeFile(path.join(output,'report.json'),JSON.stringify({status:'passed',driver:'playwright-chromium',checks,errors,remote_writes:0,model:'not involved',backend:'real local manager and Inbox Store',input:'synthetic notifications'},null,2));
  console.log(JSON.stringify({status:'passed',checks}));
 }catch(error){await page.screenshot({path:path.join(output,'failed.png'),fullPage:true});await fs.writeFile(path.join(output,'failure.json'),JSON.stringify({message:error.message,errors,checks},null,2));throw error;}
 finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
