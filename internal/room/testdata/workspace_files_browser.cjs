const {openCalendar}=require('./conversation_tools.cjs');
const {chromium}=require('playwright');
const assert=require('node:assert/strict');
const fs=require('node:fs');
(async()=>{
 const endpoint=process.argv[2],fixture=JSON.parse(process.argv[3]);
 const browser=await chromium.launch({headless:true});
 try{
  const page=await browser.newPage({viewport:{width:1600,height:1000}}),errors=[],requests=[];
  page.on('pageerror',err=>{errors.push(err.message);console.error('PAGE ERROR',err.message);});page.on('request',req=>requests.push(req.url()));
  page.setDefaultTimeout(12000);
  await page.goto(endpoint);await page.waitForFunction(()=>!document.querySelector('#message-input').disabled);await page.locator(`[data-room-key*='${fixture.room.room_instance}'] .room-choice`).click();await page.waitForFunction(()=>!document.querySelector('#message-input').disabled);
  const open=async()=>{await page.locator('#room-menu-label').click();await page.locator('#conversation-open-files').click();await page.locator('#files-body .file-entry').first().waitFor();};
  const locate=async value=>{await page.locator('#files-path-edit').click();await page.locator('#files-locate-path').fill(value);await page.locator('#files-locate-path').press('Enter');};
  const snapshot=async label=>{if(process.env.TEAMX_FILES_SCREENSHOTS&&fixture.name==='room-external'){fs.mkdirSync(process.env.TEAMX_FILES_SCREENSHOTS,{recursive:true});await page.screenshot({path:process.env.TEAMX_FILES_SCREENSHOTS+'/'+label+'.png',animations:'disabled'});}};
  const assertHeaderAlignment=async()=>{
   const geometry=await page.evaluate(()=>{
    const box=selector=>{const rect=document.querySelector(selector).getBoundingClientRect();return {top:rect.top,bottom:rect.bottom,height:rect.height};};
    return {files:box('.files-header'),rooms:box('.room-rail-header'),conversation:box('#room-header'),expected:parseFloat(getComputedStyle(document.querySelector('#workspace')).getPropertyValue('--workspace-header-height')),desktop:innerWidth>640};
   });
   assert.ok(Math.abs(geometry.files.height-geometry.expected)<1,JSON.stringify(geometry));
   if(geometry.desktop)for(const peer of [geometry.rooms,geometry.conversation]){
    assert.ok(Math.abs(geometry.files.top-peer.top)<1,JSON.stringify(geometry));
    assert.ok(Math.abs(geometry.files.bottom-peer.bottom)<1,JSON.stringify(geometry));
   }
  };
  await page.locator('#message-input').fill('未发送的文件浏览草稿');await page.waitForTimeout(300);
  const original=await page.locator('#member-rail').evaluate(el=>({inert:el.inert,width:el.getBoundingClientRect().width}));
  await open();assert.equal(await page.locator('#member-list-view').isVisible(),false);assert.equal(await page.locator('#activity-dialog').isVisible(),false);
  await assertHeaderAlignment();assert.equal(await page.locator('#files-title').innerText(),'文件');await snapshot('desktop-root');
  assert.equal(await page.locator('#files-back').isVisible(),false);assert.equal(await page.locator('#files-up').isDisabled(),true);
  assert.equal(await page.locator('#files-locate').isVisible(),false);
  assert.equal(await page.locator('#files-source-field').isVisible(),!fixture.direct);
  assert.equal(await page.locator('#files-body .file-entry').filter({hasText:'.hidden'}).count(),0);
  await page.locator('#files-options-toggle').click();await page.locator('#files-hidden').check();await page.locator('#files-body .file-entry').filter({hasText:'.hidden'}).waitFor();
  assert.equal(await page.locator('#files-body .file-entry').filter({hasText:'.teamx'}).count(),0);
  await page.locator('#files-body .file-entry').getByText('docs',{exact:true}).click();
  await page.locator('#files-body .file-entry').getByText('示例.md',{exact:true}).waitFor();
  assert.equal(await page.locator('#files-up').isEnabled(),true);
  const fileNames=()=>page.locator('#files-body .file-entry[data-kind=file] .file-entry-name').allTextContents();
  let names=await fileNames();assert.ok(names.indexOf('file2.txt')<names.indexOf('file10.txt'));
  assert.equal(await page.locator('#files-body .file-entry').first().getAttribute('data-kind'),'directory');
  await page.locator('#files-sort').selectOption('modified');
  names=await fileNames();assert.equal(names[0],'file10.txt');
  assert.equal(await page.locator('#files-body .file-entry').first().getAttribute('data-kind'),'directory');
  await page.locator('#files-sort').selectOption('name');await snapshot('desktop-directory');
  await page.locator('#files-expand').click();assert.equal(await page.locator('.files-list-heading .file-entry-modified').isVisible(),true);
  await assertHeaderAlignment();
  await snapshot('desktop-directory-expanded');await page.locator('#files-expand').click();
  const firstFolder=page.locator('#files-body .file-entry').first();await firstFolder.focus();await page.keyboard.press('ArrowRight');
  await page.locator('#files-body .file-entry').getByText('deep',{exact:true}).waitFor();
  await locate('docs/architecture/deep/subfolder/');await page.locator('#files-body .file-entry').getByText('guide.md',{exact:true}).waitFor();
  await page.locator('.files-crumb-overflow > summary').click();await page.locator('.files-crumb-menu button').getByText('docs',{exact:true}).click();
  await page.locator('#files-body .file-entry').getByText('示例.md',{exact:true}).waitFor();
  assert.equal(await page.locator('#files-body .file-entry[data-path="docs/architecture"]').evaluate(el=>el===document.activeElement),true);
  await page.locator('#files-filter').fill('示例');assert.equal(await page.locator('#files-body .file-entry').count(),1);
  if(fixture.direct){
   await openCalendar(page);await page.waitForFunction(()=>document.querySelector('.calendar-time-grid')||document.querySelector('#calendar-error').textContent);assert.equal(await page.locator('#calendar-error').textContent(),'');await page.locator('.calendar-time-grid').waitFor();
   assert.equal(await page.locator('#files-page').isVisible(),false);
   assert.equal(await page.locator('#message-input').isEditable(),true);
   await page.locator('#member-resizer').focus();await page.keyboard.press('Home');
   await page.locator('#calendar-mode-open').waitFor();
   assert.equal(await page.locator('#calendar-mode').getAttribute('data-mode'),'week');
   await page.locator('#calendar-mode-open').click();await page.locator('#calendar-mode-sheet [data-calendar-mode="month"]').click();
   await page.locator('.calendar-month-grid').waitFor();const date=await page.locator('#calendar-date').inputValue();
   const calendarWidth=await page.locator('#member-rail').evaluate(el=>el.getBoundingClientRect().width);
   assert.ok(Math.abs(calendarWidth-400)<2);
   await open();assert.equal(await page.locator('#files-filter').inputValue(),'示例');
   assert.equal(await page.locator('#files-body .file-entry').count(),1);
   await openCalendar(page);await page.locator('.calendar-month-grid').waitFor();
   assert.equal(await page.locator('#calendar-date').inputValue(),date);
   await page.locator('#room-menu-label').click();await page.locator('#conversation-open-details').click();
   assert.equal(await page.locator('#calendar-page').isVisible(),false);
   assert.equal(await page.locator('#activity-dialog').isVisible(),true);
   await open();
  }
  await page.locator('#files-body .file-entry').getByText('示例.md',{exact:true}).click();
  await page.locator('#files-body h1').waitFor();assert.equal(await page.locator('#files-body h1').textContent(),'工作区预览');
  await assertHeaderAlignment();
  assert.equal(await page.locator('#files-path').textContent().then(text=>text.includes('示例.md')),false);
  assert.equal(await page.locator('#files-directory').textContent(),'docs');
  assert.equal(await page.locator('#files-preview-mode').getAttribute('aria-pressed'),'true');
  assert.equal(await page.locator('#files-source-toggle').getAttribute('aria-pressed'),'false');
  assert.equal(await page.locator('#files-list-tools').isVisible(),false);assert.equal(await page.locator('#files-body img, #files-body script').count(),0);
  assert.ok(requests.every(url=>!url.includes('invalid.example')));
  let widths=await page.evaluate(()=>({file:document.querySelector('#member-rail').getBoundingClientRect().width,chat:document.querySelector('#conversation').getBoundingClientRect().width}));
  assert.ok(widths.file>=600,JSON.stringify(widths));assert.ok(widths.chat>=480,JSON.stringify(widths));
  // Drag width is independent of member settings.
  const handle=await page.locator('#member-resizer').boundingBox();await page.mouse.move(handle.x+2,handle.y+100);await page.mouse.down();await page.mouse.move(handle.x-35,handle.y+100);await page.mouse.up();
  await snapshot('desktop-preview');
  await page.locator('#files-source-toggle').click();await page.locator('#files-body pre.files-text').waitFor();assert.match(await page.locator('#files-body').textContent(),/# 工作区预览/);
  await page.locator('#files-source-toggle').click();assert.equal(await page.locator('#files-body pre.files-text').isVisible(),true);
  await page.locator('#files-preview-mode').click();await page.locator('#files-body h1').waitFor();await page.locator('#files-source-toggle').click();
  await page.locator('#files-refresh').click();await page.waitForFunction(()=>document.querySelector('#files-body').getAttribute('aria-busy')==='false');assert.equal(await page.locator('#files-source-toggle').getAttribute('aria-pressed'),'true');
  await page.locator('#files-expand').click();assert.equal(await page.locator('#conversation').evaluate(el=>el.inert),true);
  await assertHeaderAlignment();
  const fullWidth=await page.locator('#member-rail').evaluate(el=>el.getBoundingClientRect().width);assert.ok(fullWidth>widths.file);
  await page.keyboard.press('Escape');assert.equal(await page.locator('#files-expand').textContent(),'展开');
  await page.locator('#files-back').click();assert.equal(await page.locator('#files-filter').inputValue(),'示例');assert.equal(await page.locator('#files-body .file-entry').count(),1);
  await locate('docs/示例.md');await page.locator('#files-body h1').waitFor();
  await page.locator('#files-close').click();assert.equal(await page.locator('#message-input').inputValue(),'未发送的文件浏览草稿');
  const restored=await page.locator('#member-rail').evaluate(el=>({inert:el.inert,width:el.getBoundingClientRect().width}));assert.equal(restored.inert,original.inert);
  // Wait for sidebar transitions before checking restored geometry.
  await page.waitForTimeout(300);assert.ok(Math.abs(await page.locator('#member-rail').evaluate(el=>el.getBoundingClientRect().width)-original.width)<2);
  await page.locator('#room-menu-label').click();await page.locator('#conversation-open-details').click();
  assert.equal(await page.locator('#member-rail').evaluate(el=>el.inert),true);
  await open();await page.locator('#files-close').click();assert.equal(await page.locator('#member-rail').evaluate(el=>el.inert),true);
  await page.locator('#room-menu-label').click();await page.locator('#conversation-open-details').click();
  for(const [width,label] of [[1000,'narrow-preview'],[390,'mobile-preview']]){
   await page.setViewportSize({width,height:850});await open();
   await assertHeaderAlignment();
   await page.locator('#files-body .file-entry').getByText('docs',{exact:true}).click();await page.locator('#files-body .file-entry').getByText('示例.md',{exact:true}).waitFor();
   const geometry=await page.locator('#files-page').evaluate(el=>({width:el.clientWidth,scroll:el.scrollWidth,header:document.querySelector('#files-body').getBoundingClientRect().top-el.getBoundingClientRect().top}));
   assert.ok(geometry.scroll<=geometry.width,JSON.stringify(geometry));assert.ok(geometry.header<240,JSON.stringify(geometry));
   if(width===390)assert.ok((await page.locator('#files-body .file-entry').first().boundingBox()).height>=44);
   await snapshot(width===390?'mobile-directory':'narrow-directory');
   await page.locator('#files-path-edit').click();await page.keyboard.press('Escape');assert.equal(await page.locator('#files-page').isVisible(),true);assert.equal(await page.locator('#files-locate').isVisible(),false);
   await locate('docs/architecture/deep/subfolder/');await page.locator('#files-body .file-entry').getByText('guide.md',{exact:true}).waitFor();
   assert.equal(await page.locator('.files-crumb-overflow > summary').isVisible(),true);
   await snapshot(width===390?'mobile-deep-path':'narrow-deep-path');
   await locate('docs/示例.md');await page.locator('#files-body h1').waitFor();
   await assertHeaderAlignment();
   const previewGeometry=await page.locator('#files-page').evaluate(el=>({width:el.clientWidth,scroll:el.scrollWidth,header:document.querySelector('#files-body').getBoundingClientRect().top-el.getBoundingClientRect().top}));
   assert.ok(previewGeometry.scroll<=previewGeometry.width,JSON.stringify(previewGeometry));assert.ok(previewGeometry.header<=124,JSON.stringify(previewGeometry));
   assert.equal(await page.locator('#files-back').getAttribute('aria-label'),'返回列表');
   assert.equal(await page.locator('#conversation').evaluate(el=>el.inert),true);
   const box=await page.locator('#files-page').boundingBox();assert.ok(box.x>=0&&box.x+box.width<=width+1,JSON.stringify(box));
   await snapshot(label);
   if(width===390){
    await page.locator('#files-directory').click();await page.locator('#files-body .file-entry').getByText('示例.md',{exact:true}).waitFor();
    const longName='这是一份名称很长的工作区文件用于检查窄屏省略显示.md';
    await page.locator('#files-body .file-entry').getByText(longName,{exact:true}).click();await page.locator('#files-body h1').filter({hasText:'Long name'}).waitFor();
    assert.equal(await page.locator('#files-title').getAttribute('title'),'docs/'+longName);
    assert.equal(await page.locator('#files-title').evaluate(el=>el.scrollWidth>el.clientWidth),true);
    assert.equal(await page.locator('#files-view-mode').isVisible(),true);assert.equal(await page.locator('#files-download').isVisible(),true);
    await snapshot('mobile-long-filename');
   }
   if(width===390){await page.goBack();await page.locator('#files-list-tools').waitFor();assert.equal(await page.locator('#files-page').isVisible(),true);await page.locator('#files-close').click();}
   else{await page.keyboard.press('Escape');await page.locator('#files-list-tools').waitFor();await page.locator('#files-close').click();}
   assert.equal(await page.locator('#message-input').inputValue(),'未发送的文件浏览草稿');
  }
  await page.setViewportSize({width:1600,height:1000});await open();
  // A closed panel cannot install a delayed response from the real backend.
  let release;const gate=new Promise(resolve=>release=resolve);
  await page.route('**/v1/workspace/files/preview?**',async route=>{const response=await route.fetch();await gate;try{await route.fulfill({response});}catch{}});
  await locate('docs/示例.md');await page.waitForFunction(()=>document.querySelector('#files-status').textContent==='正在读取…');await page.locator('#files-close').click();release();await page.waitForTimeout(200);assert.equal(await page.locator('#files-page').isVisible(),false);
  await page.unroute('**/v1/workspace/files/preview?**');
  await open();
  if(!fixture.direct){
    const options=await page.locator('#files-source option').evaluateAll(options=>options.filter(o=>o.value&&!o.disabled).map(o=>o.value));
    await page.locator('#files-source').selectOption(options[1]);await page.locator('#files-body .file-entry').getByText('other.md',{exact:true}).waitFor();
    await page.locator('#files-source').selectOption(fixture.member);await page.locator('#files-body .file-entry').getByText('docs',{exact:true}).waitFor();
  }
  let releaseRoom;const roomGate=new Promise(resolve=>releaseRoom=resolve);
  await page.route('**/v1/workspace/files/preview?**',async route=>{const response=await route.fetch();await roomGate;try{await route.fulfill({response});}catch{}});
  await locate('docs/示例.md');await page.waitForFunction(()=>document.querySelector('#files-status').textContent==='正在读取…');
  await page.locator(`[data-room-key*='${fixture.other_room.room_instance}'] .room-choice`).click();await page.waitForFunction(()=>!document.querySelector('#message-input').disabled);
  releaseRoom();await page.waitForTimeout(200);assert.equal(await page.locator('#files-page').isVisible(),false);assert.equal(await page.locator('#files-body').textContent(),'');
  await page.unroute('**/v1/workspace/files/preview?**');
  await open();await locate('docs/示例.md');await page.locator('#files-body h1').waitFor();assert.equal(await page.locator('#files-body h1').textContent(),'Other Room');
  if(fixture.name==='room-external'||fixture.name==='direct-external'){
   // A touch context also exercises mobile font sizing and the landscape breakpoint.
   const phone=await browser.newPage({viewport:{width:390,height:850},isMobile:true,hasTouch:true,deviceScaleFactor:2});
   phone.on('pageerror',err=>errors.push(err.message));phone.setDefaultTimeout(12000);
   await phone.goto(endpoint);await phone.waitForFunction(()=>!document.querySelector('#message-input').disabled);
   const choice=phone.locator(`[data-room-key*='${fixture.room.room_instance}'] .room-choice`);
   if(!await choice.isVisible())await phone.locator('#mobile-back-to-rooms').tap();
   await choice.tap();await phone.waitForFunction(()=>!document.querySelector('#message-input').disabled);
   const shot=async name=>{if(process.env.TEAMX_FILES_SCREENSHOTS)await phone.screenshot({path:process.env.TEAMX_FILES_SCREENSHOTS+'/'+(fixture.direct?'direct-':'')+name+'.png',animations:'disabled'});};
   for(const [width,height] of [[320,850],[390,850],[430,850],[844,390]]){
    await phone.setViewportSize({width,height});await phone.locator('#room-menu-label').tap();await phone.locator('#conversation-open-files').tap();
    await phone.locator('#files-body .file-entry').first().waitFor();
    assert.equal(await phone.locator('#files-sort').isVisible(),false);assert.equal(await phone.locator('#files-expand').isVisible(),false);
    const geometry=await phone.locator('#files-page').evaluate(el=>{
     const filter=document.querySelector('#files-filter'),rect=filter.getBoundingClientRect();
     return {width:innerWidth,scroll:el.scrollWidth,panel:el.clientWidth,filterWidth:rect.width,filterHeight:rect.height,font:getComputedStyle(filter).fontSize,placeholder:getComputedStyle(filter,'::placeholder').fontSize,title:getComputedStyle(document.querySelector('#files-title')).fontSize,header:document.querySelector('#files-body').getBoundingClientRect().top-el.getBoundingClientRect().top};
    });
    assert.equal(geometry.width,width);assert.ok(geometry.scroll<=geometry.panel,JSON.stringify(geometry));
    assert.equal(geometry.font,'16px');assert.equal(geometry.placeholder,'16px');assert.equal(geometry.title,'17px');
    assert.ok(geometry.filterWidth>=180,JSON.stringify(geometry));assert.equal(geometry.filterHeight,44);assert.ok(geometry.header<=144,JSON.stringify(geometry));
    await shot('touch-root-'+width);
    await phone.locator('#files-options-toggle').tap();await phone.locator('#files-sort-mobile').waitFor();
    await shot('touch-options-'+width);await phone.locator('#files-sort-mobile').selectOption('modified');
    assert.equal(await phone.locator('#files-options').isVisible(),false);
    await phone.locator('#files-body .file-entry').getByText('docs',{exact:true}).tap();await phone.locator('#files-body .file-entry').getByText('示例.md',{exact:true}).waitFor();
    assert.equal(await phone.locator('#files-body .file-entry[data-kind=file] .file-entry-name').first().textContent(),'file10.txt');
    await shot('touch-directory-'+width);
    await phone.locator('#files-filter').fill('示例');await phone.locator('#files-body .file-entry').getByText('示例.md',{exact:true}).tap();await phone.locator('#files-body h1').waitFor();
    await shot('touch-preview-'+width);await phone.locator('#files-source-toggle').tap();await phone.locator('#files-body pre').waitFor();
    await phone.locator('#files-preview-mode').tap();await phone.locator('#files-body h1').waitFor();
    await phone.locator('#files-back').tap();assert.equal(await phone.locator('#files-filter').inputValue(),'示例');
    await phone.locator('#files-close').tap();
   }
   await phone.close();
  }
  assert.deepEqual(errors,[]);
  console.log(JSON.stringify({case:fixture.name,status:'passed',browser:browser.version(),node:process.version,platform:process.platform,arch:process.arch,sizes:[1600,1000,390],markdown:true,sourceToggle:true,filter:true,locate:true,sidebarRestore:true,expanded:true,mobileBack:true,lateResponse:true,crossRoomIsolation:true,remoteImages:0}));
 }finally{await browser.close();}
})().catch(err=>{console.error(err);process.exitCode=1;});
