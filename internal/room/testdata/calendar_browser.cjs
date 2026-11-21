const {chromium}=require('playwright'),assert=require('node:assert/strict'),fs=require('node:fs');
(async()=>{
 const browser=await chromium.launch({headless:true});const context=await browser.newContext({viewport:{width:1440,height:1000}}),page=await context.newPage();
 const errors=[];page.on('pageerror',error=>errors.push(error.message));
 const artifacts=process.env.TEAMX_CALENDAR_ARTIFACTS;
 if(artifacts)fs.mkdirSync(artifacts,{recursive:true});
 const shot=async name=>{if(artifacts)await page.screenshot({path:`${artifacts}/${name}.png`,fullPage:true,animations:"disabled"});};
 const swipeCalendar=async(target,dx,dy=0,selector='#calendar-items',onStep=async()=>{})=>{
   const session=await target.context().newCDPSession(target);await session.send('Emulation.setTouchEmulationEnabled',{enabled:true});
   const box=await target.locator(selector).boundingBox(),x=box.x+box.width*(dx<0?.78:.22),y=box.y+Math.min(180,box.height/2);
   await session.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:[{x,y}]});
   for(let step=1;step<=10;step++){await session.send('Input.dispatchTouchEvent',{type:'touchMove',touchPoints:[{x:x+dx*step/10,y:y+dy*step/10}]});await target.waitForTimeout(20);await onStep();}
   await session.send('Input.dispatchTouchEvent',{type:'touchEnd',touchPoints:[]});await session.detach();
 };
 const chooseCalendarMode=async(target,mode)=>{
   await target.evaluate(()=>new Promise(requestAnimationFrame));
   await target.locator('#calendar-mode-open').click();
   await target.locator(`#calendar-mode-sheet [data-calendar-mode="${mode}"]`).click();
   await target.locator('#calendar-mode-sheet').waitFor({state:'hidden'});
 };
 const chooseDate=async (value,target=page)=>{
   const [year,month]=value.split('-');
   await target.locator('#calendar-range').click();await target.locator('#date-time-year').click();
   await target.locator(`.date-time-choice[data-date-value="${Number(year)}"]`).click();await target.locator('#date-time-month').click();
   await target.locator(`.date-time-choice[data-date-value="${Number(month)}"]`).click();await target.locator(`.date-time-day[data-date-value="${value}"]`).click();
 };
 const task=title=>page.locator('.calendar-task-row').filter({has:page.getByRole('button',{name:title,exact:true,includeHidden:true})});
 const showView=async view=>{
   await page.evaluate(()=>new Promise(requestAnimationFrame));
   const b=page.locator(`#calendar-view [data-view="${view}"]`);
   if(await b.getAttribute('aria-pressed')!=='true'){
     const response=page.waitForResponse(r=>r.url().endsWith('/v1/calendar/list')&&!!r.request().postDataJSON()?.calendar?.from===(view==='calendar'));
     await b.click();await (await response).finished();await page.evaluate(()=>new Promise(requestAnimationFrame));
   }
 };
 const openTask=async title=>{await showView('list');await task(title).locator('button').click();await page.locator('#calendar-detail-content h2').filter({hasText:title,exact:true}).waitFor();};
 const close=async()=>page.locator('#calendar-detail-close').click();
 const refreshCalendar=async()=>{await page.locator('#calendar-options-toggle').click();await page.locator('#calendar-refresh').click();};
 const checkCalendarToolbar=async()=>{
   await page.locator('#calendar-panel').evaluate(el=>{el.scrollTop=0;});
   await page.locator('#calendar-new').evaluate(el=>Promise.all(el.getAnimations().map(animation=>animation.finished.catch(()=>{}))));
   const geometry=await page.evaluate(()=>{
     const rect=selector=>{const r=document.querySelector(selector).getBoundingClientRect();return {left:r.left,right:r.right,top:r.top,bottom:r.bottom,height:r.height};};
     return {header:rect('#room-header'),title:rect('#calendar-heading'),back:rect('#conversation-chat'),navigation:rect('.calendar-navigation'),mode:rect('#calendar-mode'),view:rect('#calendar-view'),create:rect('#calendar-new'),more:rect('#calendar-options-toggle'),menu:rect('#calendar-options-menu'),filter:rect('.calendar-skip-toggle'),refresh:rect('#calendar-refresh')};
   });
   const center=r=>(r.top+r.bottom)/2;
   assert.ok(Math.abs(center(geometry.create)-center(geometry.more))<2,JSON.stringify(geometry));
   assert.ok(geometry.create.top>=geometry.header.top&&geometry.create.bottom<=geometry.header.bottom);
   assert.ok(geometry.back.right<=geometry.title.left&&geometry.title.right<=geometry.create.left);
   if(await page.locator('#calendar-mode').isVisible()){
     assert.ok(geometry.navigation.top>=geometry.header.bottom);
     assert.ok(geometry.mode.top>=geometry.header.bottom);
     assert.ok(geometry.mode.right<=geometry.view.left);
     if(page.viewportSize().width>640)assert.ok(Math.abs(center(geometry.mode)-center(geometry.navigation))<2);
   }else if(page.viewportSize().width>640||await page.locator('#calendar-panel').getAttribute('data-view')==='list')assert.equal(await page.locator('#calendar-navigation').isVisible(),false);
   assert.ok(geometry.view.right<=page.viewportSize().width);
   assert.ok(geometry.create.right<=geometry.more.left);
   assert.equal(await page.locator('#room-heading').isVisible(),false);
   assert.equal(await page.locator('#conversation-actions').isVisible(),false);
   assert.equal(await page.locator('#calendar-heading').innerText(),'日历');
   if(page.viewportSize().width<=640){
     assert.equal(await page.locator('#calendar-new-label').isVisible(),false);
     assert.equal(await page.locator('#calendar-new-icon').isVisible(),true);
     const create=await page.locator('#calendar-new').evaluate(el=>{const style=getComputedStyle(el),hit=getComputedStyle(el,'::after');return {background:style.backgroundColor,radius:style.borderRadius,hitWidth:parseFloat(hit.minWidth),hitHeight:parseFloat(hit.minHeight)};});
     assert.equal(create.background,'rgba(0, 0, 0, 0)');assert.equal(create.radius,'50%');assert.ok(create.hitWidth>=44&&create.hitHeight>=44);
     const centered=await page.locator('#calendar-heading').evaluate(el=>{const range=document.createRange();range.selectNodeContents(el);const text=range.getBoundingClientRect(),header=document.querySelector('#room-header').getBoundingClientRect();return Math.abs((text.left+text.right-header.left-header.right)/2)<2;});
     assert.equal(centered,true);
   }else{
     assert.equal(await page.locator('#calendar-new-label').isVisible(),true);
     assert.equal(await page.locator('#calendar-new-icon').isVisible(),false);
   }
   if(await page.locator('#calendar-options-menu').isVisible()){
     assert.ok(geometry.menu.top>=geometry.more.bottom);
     assert.ok(Math.abs(geometry.menu.right-geometry.more.right)<2);
     if(await page.locator('.calendar-skip-toggle').isVisible()){assert.ok(Math.abs(geometry.filter.height-geometry.refresh.height)<2);assert.ok(Math.abs(geometry.filter.left-geometry.refresh.left)<2);}
   }
 };
 const action=label=>({click:async()=>{const b=label==='编辑后续安排'?page.locator('#calendar-detail-content .calendar-actions').getByRole('button',{name:/^编辑/}):page.locator('#calendar-detail-content .calendar-actions button',{hasText:label,exact:true});if(!await b.isVisible())await page.locator('.calendar-more > summary').click();await b.click();}});
 const advanced=async()=>{if(!await page.locator('#calendar-advanced').evaluate(el=>el.open))await page.locator('#calendar-advanced > summary').click();};
 const waitState=label=>page.waitForFunction(label=>document.querySelector('#calendar-detail-content > .calendar-status')?.textContent===label,label);
 try {
  await page.goto(process.argv[2]);await page.locator('#conversation-calendar').waitFor({state:'visible'});
  const checkChatHeader=async()=>{
    assert.equal(await page.locator('#room-heading').isVisible(),true);
    assert.equal(await page.locator('#calendar-heading').isVisible(),false);
    assert.equal(await page.locator('#calendar-header-actions').isVisible(),false);
    assert.equal(await page.locator('#calendar-icon-day').textContent(),await page.evaluate(()=>new Intl.DateTimeFormat('en-US',{timeZone:'Asia/Shanghai',day:'numeric'}).format(Date.now())));
    assert.equal(await page.getByRole('tab',{name:'聊天',exact:true}).count(),0);
    const bounds=await page.evaluate(()=>{
      const rect=id=>{const r=document.getElementById(id).getBoundingClientRect();return {left:r.left,right:r.right,top:r.top,bottom:r.bottom};};
      return {header:rect('room-header'),title:document.querySelector('.room-title').getBoundingClientRect().toJSON(),calendar:rect('conversation-calendar'),details:rect('room-menu-label'),timeline:rect('timeline-wrap')};
    });
    assert.ok(bounds.title.right<=bounds.calendar.left);
    assert.ok(bounds.calendar.right<=bounds.details.left);
    assert.ok(bounds.details.right<=bounds.header.right);
    assert.ok(Math.abs(bounds.timeline.top-bounds.header.bottom)<2);
    if(page.viewportSize().width<=640)assert.ok(Math.abs((bounds.title.left+bounds.title.right-bounds.header.left-bounds.header.right)/2)<2);
    assert.equal(await page.locator('#conversation-calendar').getAttribute('aria-expanded'),'false');
  };
  await checkChatHeader();await shot('desktop-chat');
  for(const width of [390,320]){await page.setViewportSize({width,height:844});await checkChatHeader();await shot(`mobile-chat-${width}`);}
  await page.setViewportSize({width:1440,height:1000});
  await page.locator('#message-input').fill('保留聊天草稿');
  await page.locator('#timeline').evaluate(el=>{el.scrollTop=el.scrollHeight/3;});
  const chatPosition=await page.locator('#timeline').evaluate(el=>{
    const top=el.getBoundingClientRect().top,message=[...el.querySelectorAll('#messages > [id]')].find(e=>e.getBoundingClientRect().bottom>top);
    return {scroll:el.scrollTop,id:message.id,offset:message.getBoundingClientRect().top-top};
  });
  await page.locator('#conversation-calendar').click();
  await page.getByRole('button',{name:'返回聊天',exact:true}).click();
  const returnedPosition=await page.locator('#timeline').evaluate((el,id)=>({scroll:el.scrollTop,offset:document.getElementById(id).getBoundingClientRect().top-el.getBoundingClientRect().top}),chatPosition.id);
  assert.ok(Math.abs(returnedPosition.offset-chatPosition.offset)<3,JSON.stringify({chatPosition,returnedPosition}));
  assert.equal(await page.locator('#message-input').inputValue(),'保留聊天草稿');
  assert.equal(await page.locator('#conversation-calendar').evaluate(el=>document.activeElement===el),true);
  await page.locator('#conversation-calendar').click();await task('一次性检查').waitFor({state:'attached'});
  assert.equal(await page.locator('#conversation-calendar').getAttribute('aria-expanded'),'true');
  assert.equal(await page.locator('#conversation-calendar').evaluate(el=>getComputedStyle(el).backgroundColor),'rgba(0, 0, 0, 0)');
  assert.equal(await page.locator('#calendar-mode').getAttribute('data-mode'),'week');
  assert.equal(await page.locator('.calendar-grid-corner').innerText(),'GMT+8');
  assert.equal(await page.locator('.calendar-grid-corner').getAttribute('title'),'Asia/Shanghai');
  const layout=await page.evaluate(()=>{
    const rect=el=>el.getBoundingClientRect();
    const back=rect(document.querySelector('#conversation-chat')),header=rect(document.querySelector('#room-header')),body=rect(document.querySelector('.calendar-time-body'));
    const headings=[...document.querySelectorAll('.calendar-day-heading')].map(el=>({weekday:rect(el.querySelector('span')).toJSON(),date:rect(el.querySelector('strong')).toJSON()}));
    const ticks=[...document.querySelectorAll('.calendar-hour > span')];
    return {back:back.toJSON(),header:header.toJSON(),headings,ticks:[1,12,23].map(hour=>({hour,center:(rect(ticks[hour]).top+rect(ticks[hour]).bottom)/2-body.top}))};
  });
  assert.ok(layout.back.top>=layout.header.top&&layout.back.bottom<=layout.header.bottom);
  for(const head of layout.headings){assert.ok(head.weekday.bottom<=head.date.top);assert.ok(Math.abs(head.weekday.left+head.weekday.width/2-head.date.left-head.date.width/2)<1);}
  for(const tick of layout.ticks)assert.ok(Math.abs(tick.center-tick.hour*56)<1);
  assert.notEqual(await page.locator('.calendar-day-heading[data-today="true"] strong').evaluate(el=>getComputedStyle(el).backgroundColor),'rgba(0, 0, 0, 0)');
  assert.equal(await page.locator('.calendar-day-heading[data-kind="weekend"] small').count(),0);
  await checkCalendarToolbar();await page.locator('#calendar-options-toggle').click();await page.locator('#calendar-show-skipped').uncheck();await checkCalendarToolbar();await shot('desktop-calendar-menu');
  await page.keyboard.press('Escape');assert.equal(await page.locator('#calendar-options-toggle').getAttribute('aria-expanded'),'false');
  await page.locator('#calendar-options-toggle').click();assert.equal(await page.locator('#calendar-show-skipped').isChecked(),false);await page.locator('#calendar-show-skipped').check();await page.keyboard.press('Escape');
  assert.equal(await page.locator('.calendar-day-heading').count(),7);
  assert.ok(await page.locator('#calendar-items').evaluate(el=>el.scrollTop)>0);
  assert.ok(await page.locator('.calendar-cluster').count()>0);
  await shot('desktop-week');
  const timeScroll=await page.locator('#calendar-items').evaluate(el=>el.scrollTop);await page.locator('#calendar-items').evaluate(el=>{el.scrollTop=0;});await shot('desktop-week-start');await page.locator('#calendar-items').evaluate((el,top)=>{el.scrollTop=top;},timeScroll);
  assert.equal(await page.locator('.calendar-day-heading').first().evaluate(e=>e.offsetHeight),64);
  assert.equal(await page.locator('#calendar-coverage').isVisible(),false);
  assert.equal(await page.locator('#calendar-create').isVisible(),false);
  // The calendar fills the actual remaining panel height, including tall
  // windows that previously stopped growing at the 780px time-axis limit.
  for(const [width,height] of [[1440,1200],[1440,700]]){
    await page.setViewportSize({width,height});await page.evaluate(()=>new Promise(requestAnimationFrame));
    const bounds=await page.locator('#calendar-items').evaluate(el=>{
      const panel=el.parentElement,style=getComputedStyle(panel);
      return {bottom:el.getBoundingClientRect().bottom,availableBottom:panel.getBoundingClientRect().bottom-parseFloat(style.paddingBottom),height:el.clientHeight,panelScroll:panel.scrollHeight-panel.clientHeight};
    });
    assert.ok(Math.abs(bounds.bottom-bounds.availableBottom)<2,JSON.stringify(bounds));
    assert.ok(bounds.panelScroll<2,JSON.stringify(bounds));
    if(height===1200)assert.ok(bounds.height>780,JSON.stringify(bounds));
    await shot(`desktop-calendar-fill-${height}`);
  }
  await page.setViewportSize({width:1440,height:1000});
  await page.locator('#calendar-options-toggle').click();await page.locator('.calendar-work-calendar > summary').click();
  assert.match(await page.locator('#calendar-work-coverage').innerText(),/中国大陆工作日历覆盖范围/);
  await shot('desktop-work-calendar');await page.keyboard.press('Escape');
  await page.locator('#calendar-range').click();await page.locator('.date-time-day').first().waitFor();
  assert.equal(await page.locator('.date-time-day').count(),42);
  assert.equal(await page.locator('#date-time-today').innerText(),'今天');await shot('desktop-date-picker');
  assert.equal(await page.locator('#date-time-today').evaluate(el=>{const button=el.getBoundingClientRect(),footer=el.parentElement.getBoundingClientRect();return Math.abs(button.left+button.right-footer.left-footer.right)<2;}),true);
  assert.equal(await page.locator('#date-time-picker input:visible').count(),0);
  assert.equal(await page.locator('#date-time-cancel').isVisible(),false);assert.equal(await page.locator('#date-time-apply').isVisible(),false);
  assert.equal(await page.locator('#date-time-year').getAttribute('aria-expanded'),'false');assert.equal(await page.locator('#date-time-month').getAttribute('aria-expanded'),'false');
  const pickerSize=await page.locator('#date-time-picker').boundingBox();
  await page.locator('#date-time-month').click();assert.equal(await page.locator('.date-time-choice').count(),12);await shot('desktop-month-picker');
  assert.equal(await page.locator('#date-time-month').getAttribute('aria-expanded'),'true');
  assert.equal(await page.locator('#date-time-next .date-time-double').isVisible(),true);
  assert.equal(await page.locator('#date-time-next .date-time-single').isVisible(),false);
  const browsingDay=await page.locator('#calendar-date').inputValue();
  await page.locator('#date-time-next').click();assert.equal(await page.locator('#date-time-year').innerText(),'2027年');assert.equal(await page.locator('#date-time-month').innerText(),'11月');
  assert.equal(await page.locator('#date-time-grid').getAttribute('data-level'),'months');assert.equal(await page.locator('#calendar-date').inputValue(),browsingDay);
  await page.locator('#date-time-prev').click();assert.equal(await page.locator('#date-time-year').innerText(),'2026年');
  assert.equal(await page.locator('#date-time-month').evaluate(el=>new DOMMatrixReadOnly(getComputedStyle(el,'::after').transform).a<0),true);
  await page.locator('.date-time-choice[data-date-value="5"]').click();
  await page.locator('#date-time-year').click();await shot('desktop-year-picker');
  assert.equal(await page.locator('#date-time-year').getAttribute('aria-expanded'),'true');
  assert.equal(await page.locator('#date-time-year').evaluate(el=>new DOMMatrixReadOnly(getComputedStyle(el,'::after').transform).a<0),true);
  const yearSize=await page.locator('#date-time-picker').boundingBox();assert.equal(yearSize.height,pickerSize.height);assert.equal(yearSize.width,pickerSize.width);
  await page.locator('.date-time-choice[data-date-value="2027"]').click();assert.equal(await page.locator('#date-time-month').innerText(),'5月');
  await page.locator('#date-time-year').click();await page.locator('.date-time-choice[data-date-value="2026"]').click();
  await page.locator('#date-time-month').click();await page.locator('.date-time-choice[data-date-value="11"]').click();
  assert.equal(await page.locator('.date-time-day[data-in-range="true"]').count(),7);

  await page.locator('.date-time-day[aria-pressed="true"]').click();
  const nowOffset=await page.locator('.calendar-now-line').evaluate(e=>parseFloat(e.style.getPropertyValue('--now-top')));
  assert.ok(nowOffset>=0&&nowOffset<24*56);
  const nowColors=await page.locator('.calendar-now-line').evaluate(el=>({line:getComputedStyle(el).color,background:getComputedStyle(el).backgroundColor,dot:getComputedStyle(el.querySelector('i')).backgroundColor,label:getComputedStyle(document.querySelector('.calendar-now-label')).color}));
  assert.equal(nowColors.line,'rgb(214, 62, 69)');assert.equal(nowColors.dot,nowColors.line);assert.equal(nowColors.label,nowColors.line);assert.equal(nowColors.background,nowColors.line);

  // Calendar shows occurrences; the task list has one row per definition.
  const range=mode=>({click:async()=>{if(page.viewportSize().width<=640)await chooseCalendarMode(page,mode==='week'?'multi':mode);else await page.locator(`#calendar-mode [data-mode="${mode}"]`).click();}});
  const presentation=view=>page.locator(`#calendar-view [data-view="${view}"]`);
  const occurrenceIDs=()=>page.locator('#calendar-items .calendar-occurrence').evaluateAll(items=>items.map(item=>item.dataset.occurrenceId).sort());
  const taskIDs=()=>page.locator('.calendar-task-row').evaluateAll(items=>items.map(item=>item.dataset.taskId).sort());
  const selectedDay=await page.locator('#calendar-date').inputValue(),weekLabel=await page.locator('#calendar-range-label').innerText(),weekIDs=await occurrenceIDs();
  const savedGridScroll=await page.locator('#calendar-items').evaluate(el=>el.scrollTop);
  const taskQueries=[];const recordQuery=req=>{if(req.url().endsWith('/v1/calendar/list'))taskQueries.push(req.postDataJSON().calendar);};
  page.on('request',recordQuery);await showView('list');page.off('request',recordQuery);
  assert.ok(taskQueries.length>0);assert.ok(taskQueries.every(q=>!q.from&&!q.to&&!q.cursor));
  assert.equal(await page.locator('#calendar-mode').isVisible(),false);
  assert.equal(await page.locator('#calendar-navigation').isVisible(),false);
  assert.equal(await page.locator('#calendar-items').isVisible(),false);
  assert.equal(await page.locator('#calendar-tasks > summary').count(),0);
  assert.equal(await page.locator('.calendar-task-row').count(),32);
  assert.equal(await page.locator('.calendar-task-row').evaluateAll(items=>new Set(items.map(item=>item.dataset.taskId)).size),32);
  assert.match(await task('日常检查 01').innerText(),/每天/);
  assert.match(await task('日常检查 01').innerText(),/下次/);
  assert.doesNotMatch(await task('实习生面试提醒').innerText(),/Asia\/Shanghai|下次/);
  assert.match(await task('实习生面试提醒').innerText(),/14:30 · 一次性/);
  assert.match(await task('一次性检查').innerText(),/UTC/);
  assert.equal(await task('实习生面试提醒').locator('.calendar-task-chevron').isVisible(),true);
  // The metadata and trailing space share the native button's click and keyboard target.
  await task('实习生面试提醒').locator('small').click();
  await page.locator('#calendar-detail-content h2').getByText('实习生面试提醒',{exact:true}).waitFor();
  await page.locator('#calendar-detail').evaluate(el=>Promise.all(el.getAnimations().map(animation=>animation.finished.catch(()=>{}))));
  const drawer=await page.locator('#calendar-detail').boundingBox();assert.ok(Math.abs(drawer.width-480)<1);assert.ok(Math.abs(drawer.x+drawer.width-1440)<1);
  await close();await task('实习生面试提醒').locator('button').press('Enter');
  await page.locator('#calendar-detail-content h2').getByText('实习生面试提醒',{exact:true}).waitFor();await close();
  await task('实习生面试提醒').locator('.calendar-task-chevron').click();await page.locator('#calendar-detail-content h2').getByText('实习生面试提醒',{exact:true}).waitFor();await close();
  const allTasks=await taskIDs();await checkCalendarToolbar();await shot('desktop-task-list');
  await page.locator('#calendar-panel').evaluate(el=>{el.scrollTop=250;});
  const savedListScroll=await page.locator('#calendar-panel').evaluate(el=>el.scrollTop);
  await showView('calendar');
  assert.equal(await page.locator('#calendar-date').inputValue(),selectedDay);
  assert.equal(await page.locator('#calendar-range-label').innerText(),weekLabel);
  assert.ok(Math.abs(await page.locator('#calendar-items').evaluate(el=>el.scrollTop)-savedGridScroll)<2);
  assert.deepEqual(await occurrenceIDs(),weekIDs);
  await range('day').click();await page.waitForFunction(()=>document.querySelector('#calendar-mode').dataset.mode==='day');
  assert.equal(await page.locator('.calendar-day-column').count(),1);
  assert.equal(await page.locator('.calendar-day-column').getAttribute('data-day'),selectedDay);
  const weekday=await page.evaluate(day=>new Intl.DateTimeFormat('zh-CN',{weekday:'short',timeZone:'Asia/Shanghai'}).format(new Date(`${day}T12:00:00+08:00`)),selectedDay);
  assert.equal(await page.locator('.calendar-day-heading > span').innerText(),weekday);
  assert.equal(await page.locator('#calendar-items').evaluate(el=>el.scrollWidth<=el.clientWidth),true);
  const dotPosition=await page.evaluate(()=>({dot:document.querySelector('.calendar-now-line > i').getBoundingClientRect().left+3,column:document.querySelector('.calendar-day-column').getBoundingClientRect().left}));
  assert.ok(Math.abs(dotPosition.dot-dotPosition.column)<2);await shot('desktop-day');
  await page.locator('#calendar-next').click();await page.waitForFunction(day=>document.querySelector('#calendar-date').value!==day,selectedDay);
  assert.equal(Date.parse(await page.locator('#calendar-date').inputValue())-Date.parse(selectedDay),86400000);
  await showView('list');assert.deepEqual(await taskIDs(),allTasks);
  assert.ok(Math.abs(await page.locator('#calendar-panel').evaluate(el=>el.scrollTop)-savedListScroll)<2);
  await showView('calendar');
  await page.locator('#calendar-prev').click();await page.waitForFunction(day=>document.querySelector('#calendar-date').value===day,selectedDay);
  await range('week').click();await page.waitForFunction(()=>document.querySelector('#calendar-mode').dataset.mode==='week');
  await page.locator('#calendar-next').click();await page.waitForFunction(day=>document.querySelector('#calendar-date').value!==day,selectedDay);
  assert.equal(Date.parse(await page.locator('#calendar-date').inputValue())-Date.parse(selectedDay),7*86400000);
  await page.locator('#calendar-prev').click();await page.waitForFunction(day=>document.querySelector('#calendar-date').value===day,selectedDay);
  assert.deepEqual(await occurrenceIDs(),weekIDs);
  // Month reads every occurrence page before presenting a Monday-first grid.
  let monthPages=0;const countMonth=req=>{if(req.url().endsWith('/v1/calendar/list'))monthPages++;};
  page.on('request',countMonth);await range('month').click();await page.locator('.calendar-month-day').first().waitFor();page.off('request',countMonth);
  assert.ok(monthPages>1);assert.equal(await page.locator('.calendar-month-day').count(),42);
  assert.deepEqual(await page.locator('.calendar-month-headers > span').allTextContents(),['周一','周二','周三','周四','周五','周六','周日']);
  assert.equal(await page.locator('#calendar-range-label').innerText(),`${selectedDay.slice(0,4)}年 ${Number(selectedDay.slice(5,7))}月`);
  assert.equal(await page.locator('.calendar-time-axis').count(),0);assert.equal(await page.locator('.calendar-now-line').count(),0);
  assert.ok(await page.locator('.calendar-month-day[data-current-month="false"]').count()>0);
  assert.equal(await page.locator('.calendar-month-date[aria-current="date"]').count(),1);
  assert.equal(await page.locator('#calendar-items').evaluate(el=>el.scrollWidth<=el.clientWidth),true);
  await shot('desktop-month');
  const monthItem=page.locator('.calendar-month-day > .calendar-occurrence').first(),monthTitle=await monthItem.locator('strong').innerText();
  await monthItem.click();await page.locator('#calendar-detail-content h2').getByText(monthTitle,{exact:true}).waitFor();await close();
  assert.equal(await page.locator('#calendar-mode').getAttribute('data-mode'),'month');assert.equal(await page.locator('#calendar-date').inputValue(),selectedDay);
  const monthOverflow=page.locator('.calendar-month-count[data-overflow="true"]').first();
  const overflowingDay=await monthOverflow.evaluate(el=>el.parentElement.dataset.day),dailyCount=Number((await monthOverflow.getAttribute('aria-label')).match(/全部 (\d+) 项/)[1]);
  await monthOverflow.click();await page.waitForFunction(day=>document.querySelector('#calendar-mode').dataset.mode==='day'&&document.querySelector('.calendar-day-column')?.dataset.day===day,overflowingDay);
  assert.equal(await page.locator('#calendar-items .calendar-occurrence').count(),dailyCount);
  await range('month').click();await page.locator('.calendar-month-grid').waitFor();
  const savedMonth=await page.locator('.calendar-month-grid').getAttribute('data-month');await showView('list');await showView('calendar');assert.equal(await page.locator('.calendar-month-grid').getAttribute('data-month'),savedMonth);
  await page.locator('#calendar-next').click();await page.waitForFunction(month=>document.querySelector('.calendar-month-grid')?.dataset.month!==month,savedMonth);
  await page.locator('#calendar-prev').click();await page.waitForFunction(month=>document.querySelector('.calendar-month-grid')?.dataset.month===month,savedMonth);
  await chooseDate('2026-12-31');await page.waitForFunction(()=>document.querySelector('.calendar-month-grid')?.dataset.month==='2026-12');
  await page.locator('#calendar-next').click();await page.waitForFunction(()=>document.querySelector('.calendar-month-grid')?.dataset.month==='2027-01');assert.equal(await page.locator('#calendar-date').inputValue(),'2027-01-31');
  await page.locator('#calendar-prev').click();await page.waitForFunction(()=>document.querySelector('.calendar-month-grid')?.dataset.month==='2026-12');
  await range('week').click();await page.locator('.calendar-time-grid').waitFor();
  await chooseDate('2026-01-01');
  await page.waitForFunction(()=>document.querySelector('#calendar-date').value==='2026-01-01'&&document.querySelector('.calendar-day-heading[data-kind="makeup"]'));
  assert.equal(await page.locator('.calendar-day-heading[data-kind="holiday"]').first().getAttribute('title'),'节假日');
  assert.equal(await page.locator('.calendar-day-heading[data-kind="holiday"] small').first().innerText(),'休');
  assert.equal(await page.locator('.calendar-day-heading[data-kind="makeup"] small').innerText(),'补');
  assert.equal(await page.locator('.calendar-day-heading[data-kind="makeup"]').getAttribute('title'),'补班');await shot('desktop-holiday-markers');
  await range('month').click();await page.locator('.calendar-month-grid').waitFor();
  assert.equal(await page.locator('.calendar-month-day[data-day="2026-01-01"] small').innerText(),'休');
  assert.ok(await page.locator('.calendar-month-day[data-kind="makeup"]').count()>0);await shot('desktop-month-holidays');
  await range('week').click();await page.locator('.calendar-time-grid').waitFor();
  const holidayScroll=await page.locator('#calendar-items').evaluate(el=>el.scrollTop);
  await showView('list');assert.deepEqual(await taskIDs(),allTasks);
  await page.locator('#calendar-new').click();
  assert.equal(await page.locator('#calendar-start-date').inputValue(),selectedDay);
  await page.locator('#calendar-form-cancel').click();await page.waitForFunction(()=>!document.querySelector('#calendar-create').open);
  await showView('calendar');
  assert.equal(await page.locator('#calendar-date').inputValue(),'2026-01-01');
  assert.ok(Math.abs(await page.locator('#calendar-items').evaluate(el=>el.scrollTop)-holidayScroll)<2);
  await page.locator('#calendar-today').click();await page.waitForFunction(day=>document.querySelector('#calendar-date').value===day,selectedDay);

  await showView('list');
  const reminderButton=task('实习生面试提醒').locator('button');await reminderButton.scrollIntoViewIfNeeded();
  const reminderPosition=await page.evaluate(()=>({panel:document.querySelector('#calendar-panel').scrollTop,grid:document.querySelector('#calendar-items').scrollTop,date:document.querySelector('#calendar-date').value}));
  await openTask('实习生面试提醒');
  assert.equal(await page.locator('.calendar-zone-difference').count(),0);
  assert.doesNotMatch(await page.locator('#calendar-detail-content').innerText(),/Asia\/Shanghai/);
  assert.equal(await page.locator('#calendar-detail-close').getAttribute('aria-label'),'关闭任务详情');
  assert.equal(await page.locator('.calendar-detail-close-icon').isVisible(),true);assert.equal(await page.locator('.calendar-detail-back-icon').isVisible(),false);
  assert.equal(await page.locator('.calendar-execution-settings').evaluate(e=>e.open),false);
  assert.equal(await page.locator('.calendar-history-section').evaluate(e=>e.open),false);
  assert.match(await page.locator('.calendar-detail-when').innerText(),/14:30/);
  assert.equal(await page.locator('.calendar-facts').isVisible(),false);
  await shot('desktop-reminder-detail');
  const reminderBounds=await page.locator('#calendar-detail').boundingBox();await page.mouse.click(reminderBounds.x+4,reminderBounds.y+150);assert.equal(await page.locator('#calendar-detail').isVisible(),true);
  await page.mouse.click(reminderBounds.x-10,150);await page.locator('#calendar-detail').waitFor({state:'hidden'});
  assert.equal(await reminderButton.evaluate(el=>document.activeElement===el),true);
  const reminderReturn=await page.evaluate(()=>({panel:document.querySelector('#calendar-panel').scrollTop,grid:document.querySelector('#calendar-items').scrollTop,date:document.querySelector('#calendar-date').value}));assert.deepEqual(reminderReturn,reminderPosition);
  await openTask('实习生面试提醒');await page.keyboard.press('Escape');await page.locator('#calendar-detail').waitFor({state:'hidden'});
  await showView('calendar');
  const workspaceGridScroll=await page.locator('#calendar-items').evaluate(e=>e.scrollTop);
  await page.locator('#calendar-new').click();await page.locator('#calendar-title').fill('实习生面试提醒');await page.locator('#calendar-instruction').fill('提醒我参加实习生面试，结果回复本私信。');await page.locator('#calendar-local-time').fill('14:30');
  assert.equal(await page.locator('#calendar-advanced').evaluate(e=>e.open),false);assert.equal(await page.locator('#calendar-day-filter').isVisible(),false);
  assert.doesNotMatch(await page.locator('#calendar-form-summary').innerText(),/Asia\/Shanghai|按 .*执行/);
  assert.ok(await page.locator('#calendar-form').evaluate(e=>e.clientWidth)<=720);
  await shot('desktop-simple-form');await page.locator('#calendar-form-cancel').click();await page.waitForFunction(()=>!document.querySelector('#calendar-panel').classList.contains('calendar-form-open'));assert.ok(Math.abs(await page.locator('#calendar-items').evaluate(e=>e.scrollTop)-workspaceGridScroll)<2);
  await page.locator('#calendar-new').click();await page.locator('#calendar-title').fill('实习生面试提醒');await page.locator('#calendar-instruction').fill('提醒我参加实习生面试，结果回复本私信。');await page.locator('#calendar-local-time').fill('14:30');await page.setViewportSize({width:390,height:844});await shot('mobile-simple-form');
  assert.equal(await page.locator('#calendar-panel').evaluate(e=>e.scrollWidth<=e.clientWidth),true);
  await page.locator('#calendar-start-date-toggle').click();await shot('mobile-task-date-picker');
  const mobilePicker=await page.locator('#date-time-picker').boundingBox();assert.ok(mobilePicker.x>=0&&mobilePicker.x+mobilePicker.width<=390&&mobilePicker.y>=0&&mobilePicker.y+mobilePicker.height<=844);
  await page.keyboard.press('Escape');assert.equal(await page.locator('#calendar-create').evaluate(e=>e.open),true);
  await page.locator('#calendar-start-date-toggle').click();await page.waitForFunction(()=>history.state?.teamxOverlay==='date-time-picker');await page.evaluate(()=>history.back());await page.locator('#date-time-picker').waitFor({state:'hidden'});assert.equal(await page.locator('#calendar-create').evaluate(e=>e.open),true);
  await page.locator('#calendar-local-time-toggle').click();assert.equal(await page.locator('#date-time-second-field').isVisible(),false);assert.equal(await page.locator('#date-time-zone').isVisible(),false);await shot('mobile-task-time-picker');
  await page.locator('#date-time-hour').fill('15');await page.locator('#date-time-minute').fill('45');
  assert.equal(await page.locator('#calendar-local-time').inputValue(),'14:30');await page.locator('#date-time-apply').click();assert.equal(await page.locator('#calendar-local-time').inputValue(),'15:45');
  await page.locator('#calendar-form-cancel').click();await page.setViewportSize({width:1440,height:1000});
  await page.locator('#calendar-today').click();
  // Fetch all pages of a real 210+ occurrence next week, without truncation.
  let cursors=0;page.on('request',req=>{if(req.url().endsWith('/v1/calendar/list')&&req.postDataJSON()?.calendar?.cursor)cursors++;});
  let rejectCursor=true;
  await page.route('**/v1/calendar/list',route=>{if(route.request().postDataJSON().calendar.cursor&&rejectCursor){rejectCursor=false;return route.fulfill({status:409,contentType:'application/json',body:JSON.stringify({code:'version_conflict',field:'cursor',error:'snapshot changed'})});}return route.continue();});
  await page.locator('#calendar-next').click();
  await page.waitForFunction(()=>document.querySelectorAll('.calendar-occurrence').length>=210);
  assert.ok(cursors>=2);assert.equal(rejectCursor,false);
  assert.equal(await page.locator('.calendar-occurrence').evaluateAll(els=>new Set(els.map(e=>e.dataset.occurrenceId)).size),await page.locator('.calendar-occurrence').count());
  await page.unroute('**/v1/calendar/list');await page.locator('#calendar-today').click();
  await openTask('一次性检查');
  await page.locator('.calendar-report-preview').filter({hasText:'C3_HISTORY_REPORT：本地受控执行结果'}).waitFor();
  assert.equal(await page.locator('.calendar-run-snapshot').evaluate(el=>el.open),false);
  assert.equal(await page.locator('.calendar-run-snapshot h4').isVisible(),false);
  assert.ok(await page.locator('#calendar-detail').evaluate(el=>el.scrollTop)<2);
  assert.match(await page.locator('#calendar-detail-content').innerText(),/UTC/);
  await shot('desktop-simple-detail');await page.locator('.calendar-history-section > summary').click();assert.equal(await page.locator('.calendar-history-row').count(),50);
  await page.locator('#calendar-detail-content button',{hasText:'加载更早执行'}).click();
  await page.waitForFunction(()=>document.querySelectorAll('.calendar-history-row').length===55);
  await page.locator('.calendar-history-row').nth(1).getByRole('button',{name:'查看本次详情'}).click();
  await page.waitForFunction(()=>document.querySelector('.calendar-run-detail').dataset.runId==='browser-history-01');
  assert.equal(await page.locator('.calendar-report-section').evaluate(el=>el.open),false);
  assert.ok((await page.locator('.calendar-report-preview').innerText()).length<=81);
  await page.locator('.calendar-report-section > summary').click();assert.match(await page.locator('.calendar-report-section .calendar-report').innerText(),/完整报告验证/);
  await page.locator('.calendar-run-snapshot > summary').click();assert.equal(await page.locator('.calendar-run-snapshot h4').innerText(),'一次性检查');
  await page.locator('#calendar-detail').evaluate(el=>{el.scrollTop=el.scrollHeight;});const stickyClose=await page.locator('#calendar-detail-close').boundingBox();assert.ok(stickyClose.y>=0&&stickyClose.y+stickyClose.height<=page.viewportSize().height);
  await page.locator('.calendar-history-row').first().getByRole('button',{name:'查看本次详情'}).click();
  await page.locator('.calendar-run-detail').getByText('C3_HISTORY_REPORT：本地受控执行结果',{exact:true}).waitFor();
  const resultHeading=await page.locator('.calendar-run-detail > h3').boundingBox(),detailHeader=await page.locator('.calendar-detail-top').boundingBox();assert.ok(resultHeading.y>=detailHeader.y+detailHeader.height);
  await shot('desktop-detail');
  await page.locator('.calendar-run-detail').getByRole('button',{name:'查看私信报告'}).click();
  await page.locator('#timeline-wrap').waitFor({state:'visible'});
  assert.equal(await page.locator('#message-input').inputValue(),'保留聊天草稿');
  await page.locator('.calendar-receipt-link').click();
  await page.locator('#calendar-detail-content h2').waitFor();await close();await page.locator('#conversation-chat').click();
  await page.locator('.calendar-message-links button',{hasText:'查看执行安排'}).click();
  await page.locator('#calendar-detail-content h2').waitFor();
  await action('暂停').click();await waitState('已暂停');
  await close();await openTask('一次性检查');await action('恢复').click();await waitState('已安排');
  // Editing only title must preserve the verified scope and fixed rule exactly.
  await action('编辑后续安排').click();
  assert.equal(await page.locator('#calendar-time-zone').inputValue(),'UTC');
  assert.match(await page.locator('#calendar-form-summary').innerText(),/按 UTC 执行/);
  await page.locator('#calendar-local-time-toggle').click();assert.equal(await page.locator('#date-time-zone').innerText(),'时区：UTC');await page.locator('#date-time-cancel').click();
  await page.locator('#calendar-title').fill('编辑后的检查');await page.locator('#calendar-submit').click();
  await task('编辑后的检查').waitFor();await openTask('编辑后的检查');
  await page.locator('.calendar-execution-settings > summary').click();assert.match(await page.locator('#calendar-detail-content').innerText(),/verified/);
  await page.locator('.calendar-run-snapshot > summary').click();assert.equal(await page.locator('.calendar-run-snapshot h4').innerText(),'一次性检查');
  await close();
  // A real settings save changes the display range, preserving the task zone.
  const changeZone=async zone=>page.evaluate(async zone=>{
    const {settings_version,...identity}=await (await fetch('/v1/settings')).json();
    const {operator}=await (await fetch('/v1/access/session')).json();
    const response=await fetch('/v1/settings/save',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({...identity,caller:{kind:'human',id:operator},expected_version:settings_version,time_zone:zone})});
    if(!response.ok)throw new Error(await response.text());
  },zone);
  await showView('calendar');await changeZone('America/New_York');await refreshCalendar();
  await page.waitForFunction(()=>document.querySelector('.calendar-grid-corner')?.title==='America/New_York');
  await openTask('编辑后的检查');assert.match(await page.locator('.calendar-zone-difference').innerText(),/执行时区 UTC · 显示时区 America\/New_York/);
  await changeZone('UTC');await page.waitForFunction(()=>!document.querySelector('.calendar-zone-difference'));
  assert.doesNotMatch(await page.locator('#calendar-detail-content').innerText(),/UTC/);
  await action('编辑后续安排').click();assert.equal(await page.locator('#calendar-time-zone').inputValue(),'UTC');assert.doesNotMatch(await page.locator('#calendar-form-summary').innerText(),/UTC/);
  await page.locator('#calendar-form-cancel').click();
  await showView('calendar');await changeZone('Asia/Shanghai');await refreshCalendar();
  await page.waitForFunction(()=>document.querySelector('.calendar-grid-corner')?.title==='Asia/Shanghai');
  // Another real tab observes committed changes through the existing SSE path.
  const other=await context.newPage();await other.goto(process.argv[2]);await other.locator('#conversation-calendar').click();await other.locator('#calendar-view [data-view="list"]').click();
  await other.getByRole('button',{name:'编辑后的检查',exact:true}).waitFor();
  await page.bringToFront();await openTask('编辑后的检查');
  await page.route('**/v1/calendar/update',async route=>{await other.bringToFront();await route.continue();});
  await action('暂停').click();
  await other.waitForFunction(()=>[...document.querySelectorAll('.calendar-task-row')].find(e=>e.textContent.includes('编辑后的检查'))?.textContent.includes('已暂停'));
  await page.unroute('**/v1/calendar/update');await other.close();await page.bringToFront();await waitState('已暂停');await close();
  // Phone is a real Chromium rendering with a simulated viewport, not a device.
  await page.setViewportSize({width:390,height:844});
  await showView('list');
  assert.equal(await page.locator('#calendar-navigation').isVisible(),false);
  assert.equal(await page.locator('#calendar-panel').evaluate(el=>el.scrollWidth<=el.clientWidth),true);
  await page.locator('#calendar-options-toggle').click();await checkCalendarToolbar();await shot('mobile-calendar-menu');await page.keyboard.press('Escape');
  await page.setViewportSize({width:320,height:844});assert.equal(await page.locator('#calendar-panel').evaluate(el=>el.scrollWidth<=el.clientWidth),true);await checkCalendarToolbar();await shot('mobile-calendar-320');await page.setViewportSize({width:390,height:844});
  assert.equal(await presentation('list').getAttribute('aria-pressed'),'true');
  await showView('calendar');await range('month').click();await page.locator('.calendar-month-grid').waitFor();
  for(const width of [390,320]){
    await page.setViewportSize({width,height:844});
    assert.equal(await page.locator('#calendar-panel').evaluate(el=>el.scrollWidth<=el.clientWidth),true);
    assert.equal(await page.locator('#calendar-items').evaluate(el=>el.scrollWidth<=el.clientWidth),true);
    assert.equal(await page.locator('.calendar-month-day').count(),42);await shot(`mobile-month-${width}`);
  }
  const mobileCount=page.locator('.calendar-month-count').first(),mobileMonthDay=await mobileCount.evaluate(el=>el.parentElement.dataset.day);
  await mobileCount.click();await page.waitForFunction(day=>document.querySelector('#calendar-date').value===day&&document.querySelector(`.calendar-day-column[data-day="${day}"]`),mobileMonthDay);
  await page.locator('#calendar-today').click();await page.waitForFunction(day=>document.querySelector('#calendar-date').value===day,selectedDay);
  await page.setViewportSize({width:390,height:844});
  const mobileDay=await page.locator('#calendar-date').inputValue();
  await showView('calendar');await range('day').click();await page.waitForFunction(()=>document.querySelector('#calendar-mode').dataset.mode==='day');
  const visibleDayColumns=await page.locator('#calendar-items').evaluate(el=>{
    const bounds=el.getBoundingClientRect();return [...el.querySelectorAll('.calendar-day-column')].filter(column=>{const rect=column.getBoundingClientRect();return rect.left>=bounds.left+47&&rect.right<=bounds.right+1;}).map(column=>column.dataset.day);
  });
  assert.deepEqual(visibleDayColumns,[mobileDay]);
  assert.equal(await page.locator('#calendar-date').inputValue(),mobileDay);await shot('mobile-day-calendar');
  const beforeDate=await page.locator('#calendar-date').inputValue();
  await swipeCalendar(page,-220);
  await page.waitForFunction(before=>document.querySelector('#calendar-date').value!==before,beforeDate);
  const selectedDate=await page.locator('#calendar-date').inputValue();
  await showView('list');await task('编辑后的检查').scrollIntoViewIfNeeded();
  const scroll=await page.locator('#calendar-panel').evaluate(el=>el.scrollTop);
  await openTask('编辑后的检查');
  assert.equal(await page.locator('#calendar-detail-close').getAttribute('aria-label'),'返回日历');
  assert.equal(await page.locator('.calendar-detail-back-icon').isVisible(),true);assert.equal(await page.locator('.calendar-detail-close-icon').isVisible(),false);
  await page.locator('#calendar-detail').evaluate(el=>Promise.all(el.getAnimations().map(animation=>animation.finished.catch(()=>{}))));
  const mobileDetailHeader=await page.evaluate(()=>{const title=document.querySelector('#calendar-detail-title').getBoundingClientRect(),back=document.querySelector('#calendar-detail-close').getBoundingClientRect();return {titleCenter:(title.left+title.right)/2,backRight:back.right,titleLeft:title.left};});
  assert.ok(Math.abs(mobileDetailHeader.titleCenter-195)<2&&mobileDetailHeader.backRight<=mobileDetailHeader.titleLeft);
  await shot('mobile-detail');
  await page.waitForFunction(()=>history.state?.teamxOverlay==='calendar-detail');await page.goBack();await page.locator('#calendar-detail').waitFor({state:'hidden'});
  assert.equal(await page.locator('#calendar-date').inputValue(),selectedDate);
  await openTask('编辑后的检查');await action('恢复').click();await waitState('已安排');await close();
  assert.equal(await task('编辑后的检查').locator('button').evaluate(el=>document.activeElement===el),true);
  assert.equal(await page.locator('#calendar-date').inputValue(),selectedDate);
  assert.ok(Math.abs(await page.locator('#calendar-panel').evaluate(el=>el.scrollTop)-scroll)<3);
  await page.locator('#conversation-chat').click();assert.equal(await page.locator('#message-input').inputValue(),'保留聊天草稿');
  await checkChatHeader();
  await page.locator('#conversation-calendar').click();assert.equal(await page.locator('#calendar-date').inputValue(),selectedDate);
  await showView('calendar');await page.locator('#calendar-today').click();await page.locator('.calendar-day-heading[data-today="true"]').waitFor();await shot('mobile-day');await showView('list');await shot('mobile-task-list');
  await page.locator('#calendar-new').click();
  await page.locator('#calendar-title').fill('每周工作日提醒');await page.locator('#calendar-instruction').fill('核对本地已确认范围并报告');
  await advanced();await page.locator('#calendar-scope').fill('本私信已确认的本地资料');await page.locator('#calendar-frequency').selectOption('weekly');
  await page.locator('#calendar-weekdays input[value="1"]').check();await page.locator('#calendar-weekdays input[value="3"]').check();
  await page.locator('#calendar-day-filter').selectOption('business_days');
  assert.match(await page.locator('#calendar-filter-hint').innerText(),/工作日历覆盖范围/);
  await page.locator('#calendar-local-time').fill('09:00');await page.locator('#calendar-time-zone').fill('Asia/Shanghai');
  await page.locator('#calendar-submit').click();await openTask('每周工作日提醒');
  await page.locator('.calendar-execution-settings > summary').click();assert.match(await page.locator('#calendar-detail-content').innerText(),/每周 一、三/);assert.match(await page.locator('#calendar-detail-content').innerText(),/含调休补班/);
  await action('跳过下一次').click();await page.getByText('已跳过',{exact:true}).waitFor();await close();
  // Draft completion uses edit + activation, retaining the same task identity.
  await page.locator('#calendar-new').click();await page.locator('#calendar-title').fill('待完善提醒');await page.locator('#calendar-instruction').fill('等待时间信息');await page.locator('#calendar-save-draft').click();
  await openTask('待完善提醒');await waitState('待完善');await action('编辑后续安排').click();
  await page.locator('#calendar-frequency').selectOption('daily');await page.locator('#calendar-local-time').fill('09:00');await page.locator('#calendar-submit').click();await page.waitForFunction(()=>document.querySelector('#calendar-create').open===false);await openTask('待完善提醒');await waitState('已安排');await close();
  // Unknown mutation receipt reuses the original operation ID.
  await page.locator('#calendar-new').click();await page.locator('#calendar-title').fill('保存回执恢复');await page.locator('#calendar-instruction').fill('本地提醒');await page.locator('#calendar-frequency').selectOption('daily');await page.locator('#calendar-local-time').fill('09:00');
  const operations=[];let lose=true;
  await page.route('**/v1/calendar/update',async route=>{operations.push(route.request().postDataJSON().calendar.operation_id);if(lose){lose=false;await route.fetch();await route.abort();}else await route.continue();});
  await page.locator('#calendar-submit').click();await page.waitForFunction(()=>document.querySelector('#calendar-submit').disabled===false);
  assert.equal(await page.locator('#calendar-create').getAttribute('open')!==null,true);await page.locator('#calendar-submit').click();await page.waitForFunction(()=>document.querySelector('#calendar-create').open===false);
  await page.unroute('**/v1/calendar/update');assert.equal(operations.length,2);assert.equal(operations[0],operations[1]);assert.equal(await task('保存回执恢复').count(),1);
  await page.locator('#calendar-new').click();await page.locator('#calendar-title').fill('保存后刷新失败');await page.locator('#calendar-instruction').fill('本地提醒');await page.locator('#calendar-frequency').selectOption('daily');await page.locator('#calendar-local-time').fill('09:00');
  await page.route('**/v1/calendar/list',route=>route.abort());await page.locator('#calendar-submit').click();await page.waitForFunction(()=>document.querySelector('#calendar-create').open===false);
  await page.locator('#calendar-retry').waitFor();await page.unroute('**/v1/calendar/list');await page.locator('#calendar-retry').click();await task('保存后刷新失败').waitFor();
  // Stale edits keep the user's form; they cannot silently refresh and overwrite.
  await openTask('保存回执恢复');await action('编辑后续安排').click();await page.locator('#calendar-title').fill('冲突草稿保留');
  await page.route('**/v1/calendar/update',route=>route.fulfill({status:409,contentType:'application/json',body:JSON.stringify({code:'version_conflict',message:'stale version',error:'stale version',actual_version:2})}));
  await page.locator('#calendar-submit').click();await page.locator('#calendar-error').filter({hasText:'安排已被修改'}).waitFor();assert.equal(await page.locator('#calendar-title').inputValue(),'冲突草稿保留');await page.unroute('**/v1/calendar/update');await page.locator('#calendar-form-cancel').click();
  // Coverage unknown and cancelled tasks remain discoverable in management.
  await page.locator('#calendar-new').click();await page.locator('#calendar-title').fill('工作日历覆盖外');await page.locator('#calendar-instruction').fill('覆盖外不执行');await page.locator('#calendar-frequency').selectOption('daily');await page.locator('#calendar-day-filter').selectOption('business_days');await page.locator('#calendar-start-date').fill('2028-01-01');await page.locator('#calendar-local-time').fill('09:00');await page.locator('#calendar-submit').click();await task('工作日历覆盖外').waitFor();assert.match(await page.locator('#calendar-coverage').innerText(),/工作日历覆盖外/);await openTask('工作日历覆盖外');assert.match(await page.locator('#calendar-detail-content').innerText(),/工作日历尚未覆盖/);await action('取消任务').click();await waitState('已取消');await close();await page.locator('#calendar-task-filter').selectOption('cancelled');await task('工作日历覆盖外').waitFor();
  assert.equal(await page.locator('#calendar-panel').evaluate(el=>el.scrollWidth<=el.clientWidth),true);
  const empty=await context.newPage();await empty.clock.install({time:new Date('2026-11-19T15:59:00Z')});await empty.goto(process.argv[3]);await empty.locator('#conversation-calendar').click();await empty.locator('#calendar-view [data-view="list"]').click();await empty.getByText('暂无此类任务。',{exact:true}).waitFor();
  await empty.setViewportSize({width:390,height:844});await empty.evaluate(()=>new Promise(requestAnimationFrame));await empty.locator('#calendar-view [data-view="list"]').click();await empty.getByText('暂无此类任务。',{exact:true}).waitFor();
  if(artifacts)await empty.screenshot({path:`${artifacts}/mobile-empty.png`,fullPage:true,animations:"disabled"});
  await empty.locator('#conversation-chat').click();await empty.clock.pauseAt(new Date('2026-11-19T15:59:59Z'));
  assert.equal(await empty.locator('#calendar-icon-day').textContent(),'19');
  await empty.clock.runFor(1000);assert.equal(await empty.locator('#calendar-icon-day').textContent(),'20');
  assert.match(await empty.locator('#conversation-calendar').getAttribute('aria-label'),/11月20日/);
  await empty.locator('#conversation-calendar').click();assert.equal(await empty.locator('#calendar-date').inputValue(),'2026-11-19');
  await empty.clock.resume();await empty.close();await page.bringToFront();
  // Controlled read responses exercise every execution state without starting
  // Agents. Past queued/running entries must remain as clear as planned ones.
  const appearanceStatuses=['planned','queued','running','publishing','failed','blocked','unknown','succeeded','skipped','cancelled'];
  const appearance=await context.newPage();await appearance.setViewportSize({width:1440,height:1000});appearance.setDefaultTimeout(5000);
  appearance.on('pageerror',error=>errors.push(error.message));
  await appearance.route('**/v1/calendar/list',async route=>{
    const query=route.request().postDataJSON().calendar;if(!query.from)return route.continue();
    const response=await route.fetch({timeout:5000}),data=await response.json(),day=(Date.parse(query.to)-Date.parse(query.from))<2*86400000;
    data.occurrences=appearanceStatuses.map((status,index)=>({occurrence_id:`appearance-${status}`,task_id:data.tasks[0].task_id,status,due_at:new Date(Date.parse(query.from)+(day?index*2+1:Math.floor(index/2)*24+(index%2)*2+1)*3600000).toISOString()}));
    data.next_cursor='';data.complete=true;await route.fulfill({status:response.status(),json:data});
  });
  await appearance.goto(process.argv[2]);await appearance.locator('#conversation-calendar').click();
  await appearance.locator('#calendar-view [data-view="calendar"]').click();
  for(const mode of ['week','day','month']){
    await appearance.locator(`#calendar-mode [data-mode="${mode}"]`).click();await appearance.waitForFunction(mode=>document.querySelector('#calendar-mode').dataset.mode===mode,mode,{timeout:5000});
    await appearance.locator('[data-occurrence-id="appearance-planned"]').waitFor({state:'attached'});
    const colors=await appearance.locator('#calendar-items .calendar-occurrence').evaluateAll(items=>Object.fromEntries(items.map(el=>[el.dataset.status,{text:getComputedStyle(el).color,mark:getComputedStyle(el,'::before').backgroundColor,disabled:el.disabled}])));
    for(const status of ['succeeded','skipped','cancelled']){assert.equal(colors[status].text,colors.succeeded.text);assert.notEqual(colors[status].text,colors.planned.text);assert.equal(colors[status].disabled,false);}
    for(const status of ['queued','running','publishing','failed','blocked','unknown'])assert.equal(colors[status].text,colors.planned.text);
    assert.equal(await appearance.locator('#calendar-items .calendar-occurrence-alert').count(),3);
    if(mode==='month')for(const status of ['succeeded','skipped','cancelled'])assert.notEqual(colors[status].mark,colors.planned.mark);
    if(artifacts)await appearance.screenshot({path:`${artifacts}/execution-states-${mode}.png`,fullPage:true,animations:'disabled'});
  }
  await appearance.close();
  // Measure actual text bounds near both hour boundaries, then let the clock
  // move away to verify labels recover without reopening the calendar.
  const timeContext=await browser.newContext({viewport:{width:1440,height:1000}}),timePage=await timeContext.newPage();
  timePage.on('pageerror',error=>errors.push(error.message));
  await timePage.clock.install({time:new Date('2026-11-19T09:11:00Z')});await timePage.goto(process.argv[3]);await timePage.locator('#conversation-calendar').click();
  await timePage.locator('.calendar-now-line').waitFor();await timePage.clock.pauseAt(new Date('2026-11-19T09:11:30Z'));
  const hourVisibility=hour=>timePage.locator('.calendar-hour > span').nth(hour).evaluate(el=>getComputedStyle(el).visibility);
  assert.equal(await timePage.locator('.calendar-now-label').innerText(),'17:11');
  assert.equal(await hourVisibility(17),'hidden');assert.equal(await hourVisibility(16),'visible');assert.equal(await hourVisibility(18),'visible');
  await timePage.locator('#calendar-items').evaluate(el=>{el.scrollTop=16*56;});
  if(artifacts)await timePage.screenshot({path:`${artifacts}/current-time-hour-overlap.png`,fullPage:true,animations:'disabled'});
  await timePage.clock.fastForward(7*60*1000);
  assert.equal(await timePage.locator('.calendar-now-label').innerText(),'17:18');assert.equal(await hourVisibility(17),'visible');
  const labelGap=await timePage.evaluate(()=>document.querySelector('.calendar-now-label').getBoundingClientRect().top-document.querySelectorAll('.calendar-hour > span')[17].getBoundingClientRect().bottom);
  assert.ok(labelGap>=1);
  if(artifacts)await timePage.screenshot({path:`${artifacts}/current-time-hour-restored.png`,fullPage:true,animations:'disabled'});
  await timePage.clock.fastForward(12*60*1000);
  assert.equal(await timePage.locator('.calendar-now-label').innerText(),'17:30');assert.equal(await hourVisibility(17),'visible');assert.equal(await hourVisibility(18),'visible');
  await timePage.clock.fastForward(12*60*1000);
  assert.equal(await timePage.locator('.calendar-now-label').innerText(),'17:42');assert.equal(await hourVisibility(18),'visible');
  await timePage.clock.fastForward(7*60*1000);
  assert.equal(await timePage.locator('.calendar-now-label').innerText(),'17:49');assert.equal(await hourVisibility(18),'hidden');assert.equal(await hourVisibility(17),'visible');
  await timePage.clock.resume();await timePage.setViewportSize({width:390,height:844});await chooseCalendarMode(timePage,'day');await timePage.locator('.calendar-time-grid[data-mode="day"]').waitFor();
  assert.equal(await hourVisibility(18),'hidden');assert.equal(await hourVisibility(17),'visible');
  if(artifacts)await timePage.screenshot({path:`${artifacts}/mobile-current-time-hour-overlap.png`,fullPage:true,animations:'disabled'});
  await timeContext.close();
  const mobileEvidence=await require('./calendar_mobile_browser.cjs')({browser,endpoint:process.argv[2],artifacts,swipeCalendar,chooseDate,chooseCalendarMode});
  assert.deepEqual(errors,[]);
  const evidence={status:'passed',desktop:'1440x1000 Chromium',mobile:'390x844 and 320x844 Chromium viewport simulation; no real device',header_calendar_entry:true,chat_without_tab_row:true,centered_mobile_title:true,chat_scroll_return:true,week:true,month:true,red_current_time_line:true,current_time_hour_overlap:true,hour_labels_recover_with_clock:true,execution_state_rendering:'controlled read responses in day/week/month',finished_occurrences_dimmed:true,pending_and_exception_occurrences_emphasized:true,month_all_pages:true,month_overflow_to_day:true,month_task_detail:true,month_cross_year:true,month_holiday_markers:true,mobile_month:true,compact_toolbar:true,calendar_and_task_views:true,day_time_axis:true,deduplicated_task_list:true,task_list_without_date_range:true,calendar_and_list_scroll_return:true,holiday_makeup_markers:true,month_picker:true,year_picker:true,month_preserved_across_years:true,picker_stable_height:true,picker_paging_units:true,shared_task_pickers:true,minute_time_axis:true,layered_form:true,form_return_position:true,draft_secondary_action:true,compact_reminder_detail:true,work_calendar_in_menu:true,contextual_coverage_hint:true,whole_task_row_clickable:true,task_row_keyboard:true,single_create_entry:true,compact_task_time:true,range_pagination:true,snapshot_restart:true,saved_then_read_failure:true,coincident_tasks:true,history_pagination:55,frozen_result:true,mutual_links:true,older_report_history_loading:true,empty_view_no_state_file:true,edit_scope_preserved:true,display_zone_change_rule_fixed:true,default_timezone_implicit:true,timezone_difference_only:true,live_timezone_hint_refresh:true,version_conflict_draft:true,sse_other_tab:true,pause_resume:true,chat_draft:true,date_scroll_return:true,mobile_browser_back:true,receipt_markdown_link:true,weekly_form:true,skip_once:true,draft_completion:true,unknown_receipt_replay:true,coverage_unknown:true,cancellation:true,console_errors:errors};
  Object.assign(evidence,mobileEvidence);
  if(artifacts)fs.writeFileSync(`${artifacts}/browser-evidence.json`,JSON.stringify(evidence,null,2)+'\n');console.log(JSON.stringify(evidence));
 }catch(error){await shot('failure');console.log('FAILURE_STATE',await page.evaluate(()=>({error:document.getElementById('calendar-error')?.textContent,detail:document.getElementById('calendar-detail-content')?.innerText})));throw error;}finally{await browser.close();}
})().catch(error=>{console.error(error);process.exit(1);});
