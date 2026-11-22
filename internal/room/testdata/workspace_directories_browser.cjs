const {toggleConversationDetails}=require('./conversation_details.cjs');
const {chromium} = require('playwright');
const assert = require('node:assert/strict');
(async()=>{
  const endpoint=process.argv[2], fixture=JSON.parse(process.argv[3]);
  const browser=await chromium.launch({headless:true});
  let page;
  try {
    page=await browser.newPage({viewport:{width:1440,height:1000}});const errors=[],applies=[];
    page.on('pageerror',e=>errors.push(e.message));
    page.on('response',async r=>{if(r.url().includes('/workspace/directories') && r.status()!==200)console.error('DIRECTORY RESPONSE',r.url(),await r.text());});
    page.on('request',r=>{if(new URL(r.url()).pathname==='/v1/workspace/apply')applies.push(r.postDataJSON());});
    page.setDefaultTimeout(15000);
    await page.goto(endpoint);
    await page.waitForFunction(()=>!document.querySelector('#message-input').disabled);
    const inspect=async()=>{
      if(await page.locator('#conversation-open-details').getAttribute('aria-expanded')!=='true')await toggleConversationDetails(page);
      await page.waitForFunction(()=>document.querySelector('#connection').textContent==='服务已连接' && !document.querySelector('#members .member-summary')?.disabled);
      if(!await page.locator('#activity-dialog').evaluate(el=>el.open))await page.locator('#members .member-summary').first().click();
      await page.locator('#activity-dialog').waitFor({state:'visible'});
      await page.locator('#agent-tab-profile').click();
      await page.locator('#member-directory-summary').waitFor({state:'visible'});
      assert.equal(await page.locator('#activity-title').innerText(),(await page.locator('#agent-name-summary').innerText())+' · 设置');
      assert.equal(await page.locator('#member-directory button').count(),0);
    };
    const closeSettings=async()=>{
      if(await page.locator('#activity-dialog').isVisible()) {
        await page.locator('#close-activity').click();
        await page.waitForFunction(()=>document.querySelector('#member-directory').hidden);
      }
      if(await page.locator('#close-mobile-members').isVisible())await page.locator('#close-mobile-members').click();
      else if(await page.locator('#close-room-details').isVisible())await page.locator('#close-room-details').click();
      await page.waitForFunction(()=>!document.querySelector('#conversation').inert);
    };
    const openPicker=async()=>{
      await closeSettings();
      if(!await page.locator('#workspace-selector').evaluate(el=>el.open))await page.locator('#workspace-trigger').click();
      await page.locator('#workspace-select-browse').click();
      await page.waitForFunction(()=>document.querySelector('#directory-entries').getAttribute('aria-busy')==='false');
    };
    const captureMember=async label=>{
      if(!process.env.TEAMX_WORKSPACE_SCREENSHOTS)return;
      await page.locator('#member-directory').scrollIntoViewIfNeeded();
      await page.locator('#activity-dialog').screenshot({animations:'disabled',path:process.env.TEAMX_WORKSPACE_SCREENSHOTS+`/member-directory-${label}.png`});
    };
    await inspect();await captureMember('default');
    assert.equal(await page.locator('#member-directory-path').isVisible(),false);
    await page.locator('#member-directory-summary').click();
    assert.equal(await page.locator('#member-directory-path').isVisible(),true);
    await page.locator('#member-directory-summary').click();
    await openPicker();
    await page.waitForFunction(()=>!document.querySelector('#directory-select').disabled);
    assert.equal(await page.locator('#directory-roots-field').isVisible(),false);
    assert.equal(await page.locator('#directory-locate').isVisible(),true);
    assert.equal(await page.locator('#directory-path').inputValue(),fixture.default);
    assert.equal(await page.locator('#directory-select').textContent(),'使用默认目录');
    assert.equal(await page.locator('#directory-parent').isDisabled(),true);
    if(process.env.TEAMX_WORKSPACE_SCREENSHOTS)await page.screenshot({animations:'disabled',path:process.env.TEAMX_WORKSPACE_SCREENSHOTS+'/directory-default.png'});
    await page.locator('#directory-entries button').getByText('notes',{exact:true}).click();
    await page.waitForFunction(path=>document.querySelector('#directory-current').title===path,fixture.default+'/notes');
    assert.equal(await page.locator('#directory-select').isDisabled(),true);
    await page.locator('#directory-parent').click();
    await page.waitForFunction(path=>document.querySelector('#directory-current').title===path,fixture.default);
    await page.locator('#directory-root').click();
    await page.waitForFunction(path=>document.querySelector('#directory-current').title===path,fixture.root);
    assert.equal(await page.locator('#directory-entries button').getByText('.hidden-folder',{exact:true}).count(),0);
    await page.locator('#directory-show-hidden').check();
    await page.locator('#directory-entries button').getByText('.hidden-folder',{exact:true}).waitFor();
    await page.locator('#directory-show-hidden').uncheck();
    if(process.env.TEAMX_WORKSPACE_SCREENSHOTS)await page.screenshot({animations:'disabled',path:process.env.TEAMX_WORKSPACE_SCREENSHOTS+'/directory-root.png'});
    await page.locator('#directory-entries button').getByText(fixture.child.split('/').pop(),{exact:true}).click();
    await page.waitForFunction(path=>document.querySelector('#directory-current').title===path,fixture.child);
    await page.locator('#directory-parent').click();
    await page.waitForFunction(path=>document.querySelector('#directory-current').title===path,fixture.root);
    await page.locator('#directory-path').fill(fixture.child);await page.locator('#directory-path').press('Enter');
    await page.waitForFunction(path=>document.querySelector('#directory-current').title===path,fixture.child);
    await page.keyboard.press('Escape');await page.locator('#directory-dialog').waitFor({state:'hidden'});
    assert.equal(applies.length,0);
    assert.equal(await page.evaluate(()=>document.activeElement.id),'workspace-select-browse');
    await openPicker();
    await page.locator('#directory-path').fill(fixture.child);await page.locator('#directory-path').press('Enter');
    await page.waitForFunction(path=>document.querySelector('#directory-current').title===path,fixture.child);
    await page.waitForFunction(()=>!document.querySelector('#directory-select').disabled);
    await page.locator('#directory-select').focus();await page.keyboard.press('Enter');
    await page.locator('#workspace-select-apply').waitFor({state:'visible'});
    assert.match(await page.locator('#workspace-select-feedback').textContent(),/所有成员共用/);
    assert.equal(applies.length,0);
    if(process.env.TEAMX_WORKSPACE_SCREENSHOTS)await page.locator('#workspace-selector').screenshot({animations:'disabled',path:process.env.TEAMX_WORKSPACE_SCREENSHOTS+'/group-directory-selector.png'});
    await page.locator('#workspace-select-apply').click();
    await page.waitForFunction(()=>!document.querySelector('#message-input').disabled);
    const query=new URLSearchParams({service_epoch:fixture.epoch,caller_id:fixture.caller,...fixture.room});
    await page.waitForFunction(async({query,path,id})=>{
      const r=await fetch('/v1/workspace?'+query);const d=await r.json();
      return d.operation?.status==='completed' && d.members.every(member=>d.workspace.member_directories?.[member.membership_id]===path);
    },{query:query.toString(),path:fixture.child,id:fixture.member.membership_id});
    assert.equal(applies.length,1);
    assert(Object.values(applies[0].target.member_directories).length>1);
    assert(Object.values(applies[0].target.member_directories).every(path=>path===fixture.child));
    const layouts=[];
    for(const viewport of [{width:1440,height:1000},{width:390,height:844},{width:320,height:568},{width:844,height:390}]) {
      await page.setViewportSize(viewport);
      await page.reload();
      await page.waitForFunction(()=>!document.querySelector('#message-input').disabled);
      if(viewport.width<=640)await page.locator('#room-list .room-choice').first().click();
      if(await page.locator('#conversation-open-details').getAttribute('aria-expanded')!=='true')await toggleConversationDetails(page);
      await inspect();
      await captureMember(`${viewport.width}x${viewport.height}`);
      await openPicker();
      await page.waitForFunction(()=>!document.querySelector('#directory-select').disabled);
      assert.equal(await page.locator('#directory-path').inputValue(),fixture.child);
      assert.equal(await page.locator('#directory-configured').getAttribute('title'),fixture.child);
      await page.locator('#directory-dialog').evaluate(el=>Promise.all(el.getAnimations().map(a=>a.finished)));
      const metrics=await page.evaluate(()=>{
        const dialog=document.querySelector('#directory-dialog'),select=document.querySelector('#directory-select'),cancel=document.querySelector('#directory-cancel');
        const d=dialog.getBoundingClientRect(),s=select.getBoundingClientRect(),c=cancel.getBoundingClientRect();
        return {left:d.left,right:d.right,top:d.top,bottom:d.bottom,overflow:dialog.scrollWidth-dialog.clientWidth,selectVisible:s.top>=0 && s.bottom<=innerHeight,cancelVisible:c.top>=0 && c.bottom<=innerHeight};
      });
      assert(metrics.left>=0 && metrics.right<=viewport.width+1 && metrics.top>=0 && metrics.bottom<=viewport.height+1,JSON.stringify({viewport,metrics}));
      assert(metrics.overflow<=1 && metrics.selectVisible && metrics.cancelVisible,JSON.stringify({viewport,metrics}));
      for(let i=0;i<12;i++) {
        await page.keyboard.press('Tab');
        assert(await page.evaluate(()=>document.activeElement===document.body || document.querySelector('#directory-dialog').contains(document.activeElement)));
      }
      if(process.env.TEAMX_WORKSPACE_SCREENSHOTS)await page.screenshot({animations:'disabled',path:process.env.TEAMX_WORKSPACE_SCREENSHOTS+`/directory-${viewport.width}x${viewport.height}.png`});
      layouts.push({viewport,...metrics});await page.locator('#directory-cancel').click();
    }
    // Selecting the managed root restores default semantics through the same UI.
    await page.setViewportSize({width:1440,height:1000});
    await inspect();await openPicker();
    await page.locator('#directory-path').fill(fixture.default);await page.locator('#directory-path').press('Enter');
    await page.waitForFunction(path=>document.querySelector('#directory-current').title===path,fixture.default);
    await page.locator('#directory-select').click();await page.locator('#workspace-select-apply').click();
    await page.waitForFunction(async query=>{const d=await(await fetch('/v1/workspace?'+query)).json();return d.operation?.status==='completed' && !d.workspace.member_directories;},query.toString());
    assert.equal(applies.length,2);assert.equal(applies[1].target.member_directories,undefined);
    // Reassign, then restore all defaults from the same composer entry.
    await inspect();await openPicker();
    await page.locator('#directory-root').click();
    await page.waitForFunction(path=>document.querySelector('#directory-current').title===path,fixture.root);
    await page.locator('#directory-entries button').getByText(fixture.child.split('/').pop(),{exact:true}).click();
    await page.waitForFunction(path=>document.querySelector('#directory-current').title===path,fixture.child);
    await page.locator('#directory-select').click();await page.locator('#workspace-select-apply').click();
    await page.waitForFunction(async({query,id,path})=>{const d=await(await fetch('/v1/workspace?'+query)).json();return d.operation?.status==='completed' && d.members.every(member=>d.workspace.member_directories?.[member.membership_id]===path);},{query:query.toString(),id:fixture.member.membership_id,path:fixture.child});
    await inspect();
    await closeSettings();
    await page.locator('#workspace-trigger').click();
    await page.locator('#workspace-select-options button').filter({hasText:'各自默认'}).click();
    await page.locator('#workspace-select-apply').click();
    await page.waitForFunction(async query=>{const d=await(await fetch('/v1/workspace?'+query)).json();return d.operation?.status==='completed' && !d.workspace.member_directories;},query.toString());
    assert.equal(applies.length,4);assert.deepEqual(errors,[]);
    console.log(JSON.stringify({browser:'Chromium',backend:'isolated production HTTP and Store; controlled model component',button_navigation:true,path_location:true,cancel_without_write:true,keyboard:true,composer_apply:true,shared_directory:true,bulk_restore:true,member_read_only:true,layouts,page_errors:errors}));
  } catch(error) {
    if(page) {
      console.error(await page.evaluate(()=>({viewport:[innerWidth,innerHeight],elements:Object.fromEntries(['activity-dialog','agent-tab-profile','member-directory','workspace-select-apply','workspace-select-feedback','workspace-summary','directory-dialog','directory-status','directory-path','directory-error','sync-error'].map(id=>{const e=document.getElementById(id);return [id,{open:e.open,hidden:e.hidden,disabled:e.disabled,text:e.textContent.slice(0,500),value:e.value}];}))})));
      if(process.env.TEAMX_WORKSPACE_SCREENSHOTS)await page.screenshot({animations:'disabled',path:process.env.TEAMX_WORKSPACE_SCREENSHOTS+'/failure.png'});
    }
    throw error;
  } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
