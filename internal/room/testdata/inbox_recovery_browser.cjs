const {toggleConversationDetails}=require('./conversation_details.cjs');
const {chromium}=require('playwright'),assert=require('node:assert/strict'),fs=require('node:fs/promises'),path=require('node:path');
(async()=>{
 const [endpoint,out,membership]=process.argv.slice(2),browser=await chromium.launch({headless:true}),errors=[],checks=[];
 const context=await browser.newContext({viewport:{width:1440,height:1000}}),page=await context.newPage();
 page.setDefaultTimeout(15000);page.on('pageerror',e=>errors.push(e.message));
 const command=async data=>{const r=await page.request.post(endpoint+'/__inbox_recovery',{data});assert.equal(r.status(),200,await r.text());};
 const waitState=async predicate=>{let state;for(let i=0;i<150;i++){state=await(await page.request.get(endpoint+'/__inbox_state')).json();if(predicate(state))return state;await new Promise(r=>setTimeout(r,100));}throw new Error('State deadline: '+JSON.stringify(state));};
 try{
  await page.goto(endpoint);await page.locator('#room-list .room-choice').first().click();
  await page.locator(`#members .member-summary[data-activity-member="${membership}"]`).click();
  await page.locator('#agent-error').filter({hasText:'等待恢复 1/3'}).waitFor();
  assert.match(await page.locator('#agent-error').textContent(),/#1–#25.*积压 61/);
  await page.locator('#activity-dialog').screenshot({path:path.join(out,'desktop-waiting.png')});checks.push('desktop_range_backlog_waiting_reason');
  for(const [seconds,attempt] of [[15,1],[60,2],[300,3]]){
   await command({seconds});
   await waitState(state=>!state.active && state.inbox_recovery?.attempts===attempt && (attempt===3 || Date.parse(state.inbox_recovery.next_at)>0));
  }
  // Refresh the actual current Room through its visible controls.
  await page.locator('#close-activity').click();await page.locator('#room-list .room-choice').first().click();
  await page.locator(`#members .member-summary[data-activity-member="${membership}"]`).click();
  await page.locator('#agent-error').filter({hasText:'已停止自动恢复（3/3）'}).waitFor();
  await page.locator('#agent-quick-actions button').filter({hasText:'继续读取收件箱'}).waitFor();
  await page.locator('#activity-dialog').screenshot({path:path.join(out,'desktop-exhausted.png')});checks.push('three_attempts_stop_with_continue');
  await page.setViewportSize({width:390,height:844});
  await toggleConversationDetails(page);
  await page.locator(`#members .member-summary[data-activity-member="${membership}"]`).click();
  await page.locator('#activity-dialog').screenshot({path:path.join(out,'mobile-exhausted.png')});
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),true);
  const button=page.locator('#agent-quick-actions button').filter({hasText:'继续读取收件箱'});
  await button.waitFor({state:'visible'});
  const color=await button.evaluate(el=>getComputedStyle(el).borderColor);assert.ok(color);
  checks.push('mobile_visible_continue_no_horizontal_overflow');
  await command({read:true});await button.click();
  await waitState(state=>state.inbox?.consumed_through===61);
  checks.push('explicit_continue_drains_61_without_chat');assert.deepEqual(errors,[]);
  await fs.writeFile(path.join(out,'report.json'),JSON.stringify({status:'passed',driver:'playwright-chromium',checks,errors,remote_writes:0,model:'synthetic component',backend:'real local scheduler, Inbox, manager, HTTP'},null,2));
  console.log(JSON.stringify({status:'passed',checks}));
 }catch(e){await page.screenshot({path:path.join(out,'failed.png'),fullPage:true});throw e}finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
