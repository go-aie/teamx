const {openCalendar}=require('./conversation_tools.cjs');
const {chromium} = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');

(async () => {
  const endpoint = process.argv[2], rooms = JSON.parse(process.argv[3]);
  const browser = await chromium.launch({headless:true});
  const page = await browser.newPage({viewport:{width:1440,height:1000}});
  const errors = [], mutations = [], artifacts = process.env.TEAMX_CALENDAR_ARTIFACTS;
  page.setDefaultTimeout(15000);
  page.on('pageerror', error => errors.push(error.message));
  page.on('request', request => {
    if (new URL(request.url()).pathname === '/v1/calendar/update') mutations.push(request.postDataJSON());
  });
  const titles = ['私信甲重复提醒', '私信乙重复提醒'];
  const row = i => page.locator('.calendar-task-row').filter({has:page.getByRole('button',{name:titles[i],exact:true})});
  const selector = i => `#room-list > [data-room-key=${JSON.stringify(JSON.stringify([rooms[i].room_id,rooms[i].room_instance]))}] > button:first-child`;
  const showCalendar = async i => {
    await openCalendar(page);
    if(await page.locator('#calendar-view [data-view="list"]').getAttribute('aria-pressed')!=='true')await page.locator('#calendar-view [data-view="list"]').click();
    await row(i).waitFor();
    assert.equal(await row(1-i).count(),0);
  };
  const choose = async i => {
    await page.locator(selector(i)).click();
    await page.waitForFunction(()=>!document.querySelector('#message-input').disabled);
  };
  const open = async i => {
    await row(i).getByRole('button',{name:titles[i],exact:true}).click();
    await page.locator('#calendar-detail-content h2').getByText(titles[i],{exact:true}).waitFor();
  };
  const action = async (label,state) => {
    await page.locator('#calendar-detail-content .calendar-actions').getByRole('button',{name:label,exact:true}).click();
    await page.waitForFunction(state=>document.querySelector('#calendar-detail-content > .calendar-status')?.textContent===state,state);
  };
  const close = async () => page.locator('#calendar-detail-close').click();
  const driver = async action => {
    const response = await page.request.post(endpoint+'/__test/calendar/'+action,{headers:{Origin:endpoint}});
    assert.equal(response.status(),200,await response.text());
    return response.json();
  };
  const restartAndReconcile = async () => {
    const before = (await (await page.request.get(endpoint+'/v1/ui')).json()).service_epoch;
    const reconciled = page.waitForResponse(async response => new URL(response.url()).pathname==='/v1/rooms' &&
      response.ok() && (await response.json()).service_epoch!==before);
    const result = await driver('restart');
    assert.equal(result.previous_epoch,before);
    assert.notEqual(result.previous_epoch,result.service_epoch);
    await reconciled;
    await page.waitForFunction(()=>!document.querySelector('#message-input').disabled);
    return result;
  };
  const shot = async name => {
    if (artifacts) {
      fs.mkdirSync(artifacts,{recursive:true});
      await page.screenshot({path:`${artifacts}/${name}.png`,fullPage:true,animations:'disabled'});
    }
  };
  try {
    await page.goto(endpoint);
    await page.waitForFunction(()=>!document.querySelector('#message-input').disabled);
    await page.locator('#message-input').fill('甲私信独立草稿');
    await showCalendar(0); await open(0); await action('暂停','已暂停'); await close();
    // Release A's delayed range response only after B is displayed. Committed
    // data from the previous selection must not replace B's calendar.
    let heldResolve, releaseRead;
    const held = new Promise(resolve=>{heldResolve=resolve;});
    const released = new Promise(resolve=>{releaseRead=resolve;});
    let holdOnce = true;
    await page.route('**/v1/calendar/list',async route=>{
      if (holdOnce && route.request().postDataJSON().room_id===rooms[0].room_id) {
        holdOnce=false; heldResolve(); await released;
      }
      await route.continue();
    });
    await page.locator('#calendar-options-toggle').click();await page.locator('#calendar-refresh').click(); await held;
    await choose(1); await page.locator('#message-input').fill('乙私信独立草稿');
    await showCalendar(1);
    const staleResponse = page.waitForResponse(response=>new URL(response.url()).pathname==='/v1/calendar/list' && response.request().postDataJSON().room_id===rooms[0].room_id);
    releaseRead(); await (await staleResponse).finished();
    await page.evaluate(()=>new Promise(requestAnimationFrame));
    await page.unroute('**/v1/calendar/list');
    assert.equal(await row(0).count(),0);
    await open(1); await action('暂停','已暂停'); await action('恢复','已安排'); await close();
    await choose(0);
    assert.equal(await page.locator('#message-input').inputValue(),'甲私信独立草稿');
    await showCalendar(0);
    // The selected conversation is A. B still runs and publishes through the
    // real manager/runtime, calendar Actor and durable Room report path.
    const report = await driver('complete-other');
    assert.ok(report.report_seq>0); assert.ok(report.binding.generation); assert.ok(report.binding.turn>0);
    assert.equal(await row(1).count(),0);
    const writesBeforeRestart = mutations.length;
    // The production identity reconciliation may return to chat. Reopen the
    // calendar after the new manager catalog has been installed.
    await restartAndReconcile();
    await showCalendar(0); await open(0);
    await page.waitForFunction(()=>document.querySelector('#calendar-detail-content > .calendar-status')?.textContent==='已暂停');
    assert.equal(mutations.length,writesBeforeRestart);
    await action('恢复','已安排'); await close();
    await shot('desktop-recovered');
    await choose(1);
    assert.equal(await page.locator('#message-input').inputValue(),'乙私信独立草稿');
    await showCalendar(1); await open(1);
    assert.equal(await page.locator('.calendar-history-row').count(),1);
    await page.locator('.calendar-history-section > summary').click();
    await page.locator('.calendar-history-row').getByRole('button',{name:'查看本次详情'}).click();
    await page.locator('.calendar-run-detail').getByText(report.report,{exact:true}).waitFor();
    await page.locator('.calendar-run-detail').getByRole('button',{name:'查看私信报告'}).click();
    await page.locator('#message-'+report.report_seq).getByText(report.report,{exact:true}).waitFor();
    assert.equal(await page.locator('#messages').getByText(report.report,{exact:true}).count(),1);
    await page.setViewportSize({width:390,height:844});
    await showCalendar(1); await open(1);
    const writesBeforeSecondRestart = mutations.length;
    await restartAndReconcile();
    await showCalendar(1); await open(1);
    assert.equal(await page.locator('.calendar-history-row').count(),1);
    assert.equal(mutations.length,writesBeforeSecondRestart);
    await shot('mobile-recovered-history'); await close();
    assert.equal(await page.locator('#calendar-panel').evaluate(el=>el.scrollWidth<=el.clientWidth),true);
    await page.locator('#calendar-close').click();
    assert.equal(await page.locator('#message-input').inputValue(),'乙私信独立草稿');
    assert.equal(await page.locator('#messages').getByText(report.report,{exact:true}).count(),1);
    assert.equal(mutations.length,4);
    assert.deepEqual(errors,[]);
    const evidence = {status:'passed',backend:'isolated real managed services, CalendarActor and durable Room paths; controlled time/model report',desktop:'1440x1000 Chromium',mobile:'390x844 Chromium viewport simulation; no real device',same_agent_role_two_conversations:true,conversation_projection_isolation:true,late_previous_conversation_read_discarded:true,independent_chat_drafts:true,task_pause_resume:true,unselected_conversation_publication:true,manager_runtime_reconstructions:2,paused_task_preserved:true,automatic_epoch_reconciliation:true,no_mutation_replay:true,recurring_task_remains_active:true,history_runs:1,unique_report:true,report,console_errors:errors};
    if (artifacts) fs.writeFileSync(`${artifacts}/combination-evidence.json`,JSON.stringify(evidence,null,2)+'\n');
    console.log(JSON.stringify(evidence));
  } catch (error) {
    await shot('combination-failure');
    console.error('page:',await page.locator('body').innerText());
    throw error;
  } finally { await browser.close(); }
})().catch(error=>{console.error(error);process.exitCode=1;});
