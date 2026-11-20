const assert=require('node:assert/strict');

// Uses Chromium touch input and native scrolling against the persisted Calendar
// API. These are viewport simulations, not physical iOS/Android device tests.
module.exports=async({browser,endpoint,artifacts,swipeCalendar,chooseDate,chooseCalendarMode})=>{
  const context=await browser.newContext({viewport:{width:390,height:844},hasTouch:true,isMobile:true});
  const page=await context.newPage(),errors=[],writes=[],trace=[];
  page.setDefaultTimeout(10000);
  page.on('pageerror',error=>errors.push(error.message));
  page.on('request',request=>{if(request.url().endsWith('/v1/calendar/update'))writes.push(request.url());});
  const root=page.locator('#calendar-items'),modeButton=page.locator('#calendar-mode-open');
  const chooseMode=mode=>chooseCalendarMode(page,mode);
  const currentDate=()=>page.locator('#calendar-date').inputValue();
  const shift=(day,days)=>new Date(Date.parse(day+'T12:00:00Z')+days*86400000).toISOString().slice(0,10);
  const waitDate=day=>page.waitForFunction(day=>document.querySelector('#calendar-date').value===day,day);
  const shot=async name=>{if(artifacts)await page.screenshot({path:`${artifacts}/${name}.png`,fullPage:true,animations:'disabled'});};
  const visibleDays=()=>root.evaluate(el=>{
    const bounds=el.getBoundingClientRect();
    return [...el.querySelectorAll('.calendar-day-column')].filter(column=>{
      const rect=column.getBoundingClientRect();return rect.left>=bounds.left+47&&rect.right<=bounds.right+1;
    }).map(column=>column.dataset.day);
  });
  const align=async days=>{
    const destination=shift(await currentDate(),days);
    while(days){
      const step=await root.evaluate((el,days)=>{
        const columns=el.querySelectorAll('.calendar-day-column'),width=columns[0].getBoundingClientRect().width;
        const index=Math.round(el.scrollLeft/width),visible=Math.round((el.clientWidth-48)/width);
        return Math.max(-index,Math.min(columns.length-visible-index,days));
      },days);
      assert.notEqual(step,0,'buffer must refill before the next drag');
      const expected=shift(await currentDate(),step);trace.push({step,expected});
      await root.evaluate((el,step)=>{
        const width=el.querySelector('.calendar-day-column').getBoundingClientRect().width;
        el.scrollTo({left:el.scrollLeft+step*width,behavior:'instant'});
      },step);
      await waitDate(expected);
      await page.waitForFunction(()=>{
        const el=document.getElementById('calendar-items'),columns=el.querySelectorAll('.calendar-day-column');
        const width=columns[0].getBoundingClientRect().width,index=Math.round(el.scrollLeft/width),days=Math.round((el.clientWidth-48)/width);
        return index>=3&&index+days<=columns.length-3;
      });days-=step;
    }
    return destination;
  };
  try{
    await page.goto(endpoint);await page.locator('#conversation-calendar').click();
    await page.locator('.calendar-time-grid[data-mode="day"]').waitFor();
    assert.equal(await modeButton.getAttribute('data-mode'),'day');
    for(const id of ['calendar-mode','calendar-prev','calendar-next'])assert.equal(await page.locator('#'+id).isVisible(),false);
    const gridButton=page.locator('#calendar-view [data-view="calendar"]'),listButton=page.locator('#calendar-view [data-view="list"]'),sheet=page.locator('#calendar-mode-sheet');
    assert.equal(await gridButton.isVisible(),true);assert.equal(await listButton.isVisible(),true);
    await modeButton.click();await sheet.waitFor();
    await sheet.evaluate(el=>Promise.all(el.getAnimations().map(animation=>animation.finished.catch(()=>{}))));
    assert.equal(await sheet.locator('[data-calendar-mode]').count(),3);
    assert.equal(await sheet.locator('[data-calendar-mode="day"]').getAttribute('aria-pressed'),'true');
    const sheetBounds=await sheet.boundingBox();assert.ok(Math.abs(sheetBounds.y+sheetBounds.height-844)<2&&sheetBounds.width>=380,JSON.stringify(sheetBounds));
    await shot('mobile-range-sheet');
    await page.keyboard.press('Escape');await sheet.waitFor({state:'hidden'});
    await page.waitForFunction(()=>history.state?.teamxOverlay!=='calendar-mode-sheet');
    assert.equal(await modeButton.evaluate(el=>document.activeElement===el),true);
    await modeButton.click();await page.waitForFunction(()=>history.state?.teamxOverlay==='calendar-mode-sheet');
    await page.goBack();await sheet.waitFor({state:'hidden'});
    await page.waitForFunction(()=>history.state?.teamxOverlay!=='calendar-mode-sheet');
    await modeButton.click();await page.mouse.click(4,4);await sheet.waitFor({state:'hidden'});
    await page.waitForFunction(()=>history.state?.teamxOverlay!=='calendar-mode-sheet');
    await modeButton.click();await swipeCalendar(page,0,100,'.calendar-sheet-header');await sheet.waitFor({state:'hidden'});
    assert.equal(await modeButton.getAttribute('data-mode'),'day');
    await shot('mobile-default-day');
    const original=await currentDate();
    await root.evaluate(el=>{el.scrollTop=600;});
    await swipeCalendar(page,-130);await waitDate(shift(original,1));
    await page.waitForFunction(day=>document.querySelector('.calendar-day-column')?.dataset.day===day,shift(original,1));
    assert.ok(Math.abs(await root.evaluate(el=>el.scrollTop)-600)<3);
    assert.equal(await page.locator('#calendar-detail').isVisible(),false);
    await swipeCalendar(page,130);await waitDate(original);
    await swipeCalendar(page,8,-100);
    assert.equal(await currentDate(),original);
    assert.ok(await root.evaluate(el=>el.scrollTop)>600);
    const touch=await context.newCDPSession(page),bounds=await root.boundingBox(),x=bounds.x+220,y=bounds.y+160;
    await touch.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:[{id:0,x,y}]});
    await touch.send('Input.dispatchTouchEvent',{type:'touchMove',touchPoints:[{id:0,x:x-60,y}]});
    assert.notEqual(await root.evaluate(el=>el.firstElementChild.style.transform),'');
    await touch.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:[{id:0,x:x-60,y},{id:1,x:x-20,y:y+40}]});
    assert.equal(await root.evaluate(el=>el.firstElementChild.style.transform),'');
    await touch.send('Input.dispatchTouchEvent',{type:'touchCancel',touchPoints:[]});await touch.detach();
    assert.equal(await currentDate(),original);
    await chooseMode('month');await page.locator('.calendar-month-grid').waitFor();
    await chooseDate('2026-12-31',page);
    await page.waitForFunction(()=>document.querySelector('.calendar-month-grid')?.dataset.month==='2026-12');
    await swipeCalendar(page,-130);await page.waitForFunction(()=>document.querySelector('.calendar-month-grid')?.dataset.month==='2027-01');
    assert.equal(await currentDate(),'2027-01-31');
    await swipeCalendar(page,130);await page.waitForFunction(()=>document.querySelector('.calendar-month-grid')?.dataset.month==='2026-12');
    assert.equal(await currentDate(),'2026-12-31');
    await chooseMode('multi');await page.locator('.calendar-time-grid[data-mode="multi"]').waitFor();
    await chooseDate('2026-12-30',page);
    await page.waitForFunction(()=>document.querySelector('.calendar-day-column')?.dataset.day==='2026-12-23');
    await page.waitForTimeout(350);
    assert.deepEqual(await visibleDays(),['2026-12-30','2026-12-31','2027-01-01']);
    await root.evaluate(el=>{el.scrollTop=600;});
    const axisBefore=await page.locator('.calendar-time-axis').boundingBox();
    // A small, slow native drag must settle on one complete date column.
    await swipeCalendar(page,-105);
    await page.waitForFunction(()=>document.querySelector('#calendar-date').value!=='2026-12-30');
    await page.waitForTimeout(400);
    const afterTouch=await currentDate(),axisAfter=await page.locator('.calendar-time-axis').boundingBox();
    assert.ok(afterTouch>'2026-12-30');assert.ok(Math.abs(axisBefore.x-axisAfter.x)<1);
    assert.ok(Math.abs(await root.evaluate(el=>el.scrollTop)-600)<3);
    assert.equal(await page.locator('#calendar-detail').isVisible(),false);
    const width=await page.locator('.calendar-day-column').first().evaluate(el=>el.getBoundingClientRect().width);
    assert.ok(Math.abs(await root.evaluate(el=>el.scrollLeft)/width-Math.round(await root.evaluate(el=>el.scrollLeft)/width))<.02);
    assert.equal((await visibleDays())[0],afterTouch);
    const next=await align(1);assert.equal((await visibleDays())[0],next);
    // Traverse both buffer edges; reloading preserves the date and time window.
    for(const days of [5,5,-5,-5,-5]){
      const expected=await align(days);
      await page.waitForTimeout(400);
      assert.equal((await visibleDays())[0],expected);
      assert.ok(Math.abs(await root.evaluate(el=>el.scrollTop)-600)<3);
    }
    for(const width of [320,390,540]){
      await page.setViewportSize({width,height:844});await page.waitForTimeout(350);
      assert.equal((await visibleDays()).length,width>=480?4:3);
      const geometry=await page.evaluate(()=>{
        const rect=id=>document.getElementById(id).getBoundingClientRect().toJSON();
        const panel=document.getElementById('calendar-panel');
        return {date:rect('calendar-range'),today:rect('calendar-today'),view:rect('calendar-view'),overflow:panel.scrollWidth>panel.clientWidth};
      });
      assert.equal(geometry.overflow,false);
      assert.ok(geometry.date.right<=geometry.today.left+1&&geometry.today.right<=geometry.view.left+1,JSON.stringify(geometry));
      assert.ok(Math.abs(geometry.date.top-geometry.view.top)<2,JSON.stringify(geometry));
      const height=await root.evaluate(el=>{
        const panel=el.parentElement;
        return {bottom:el.getBoundingClientRect().bottom,availableBottom:panel.getBoundingClientRect().bottom-parseFloat(getComputedStyle(panel).paddingBottom),overflow:panel.scrollHeight-panel.clientHeight};
      });
      assert.ok(Math.abs(height.bottom-height.availableBottom)<2&&height.overflow<2,JSON.stringify(height));
      await shot(`mobile-multi-${width}`);
      if(width===320){
        await modeButton.click();await sheet.waitFor();
        assert.equal(await sheet.locator('[data-calendar-mode] > span').evaluateAll(items=>items.every(el=>el.getBoundingClientRect().height<28)),true);
        await shot('mobile-range-sheet-320');await page.locator('#calendar-mode-close').click();await sheet.waitFor({state:'hidden'});
      }
    }
    await page.setViewportSize({width:390,height:844});
    await page.reload();await page.locator('#conversation-calendar').click();
    await page.locator('.calendar-time-grid[data-mode="multi"]').waitFor();
    assert.equal(await modeButton.getAttribute('data-mode'),'multi');
    await page.locator('#calendar-view [data-view="list"]').click();await page.locator('#calendar-tasks').waitFor();
    assert.equal(await page.locator('#calendar-navigation').isVisible(),false);
    assert.equal(await page.locator('#calendar-task-filter').isVisible(),true);await shot('mobile-unified-task-list');
    assert.equal(await listButton.getAttribute('aria-pressed'),'true');
    await modeButton.click();await page.keyboard.press('Escape');assert.equal(await listButton.getAttribute('aria-pressed'),'true');
    await gridButton.click();await page.locator('.calendar-time-grid[data-mode="multi"]').waitFor();
    assert.equal(await sheet.isVisible(),false);assert.equal(await modeButton.getAttribute('data-mode'),'multi');
    await listButton.click();await chooseMode('day');await page.locator('.calendar-time-grid[data-mode="day"]').waitFor();
    assert.equal(await gridButton.getAttribute('aria-pressed'),'true');
    await chooseMode('multi');await listButton.click();
    await page.reload();await page.locator('#conversation-calendar').click();await page.locator('#calendar-tasks').waitFor();
    assert.equal(await page.locator('#calendar-view [data-view="list"]').getAttribute('aria-pressed'),'true');
    const desktop=await context.newPage();await desktop.setViewportSize({width:1440,height:1000});
    await desktop.goto(endpoint);await desktop.locator('#conversation-calendar').click();
    await desktop.locator('.calendar-time-grid[data-mode="week"]').waitFor();
    assert.equal(await desktop.locator('#calendar-mode-open').isVisible(),false);
    await desktop.locator('#calendar-mode [data-mode="month"]').click();await desktop.locator('.calendar-month-grid').waitFor();
    await desktop.reload();await desktop.locator('#conversation-calendar').click();await desktop.locator('.calendar-month-grid').waitFor();
    await desktop.close();
    // Unavailable storage must still allow a fresh mobile calendar to open.
    await page.addInitScript(()=>Object.defineProperty(window,'localStorage',{get(){throw new Error('storage disabled');}}));
    await page.reload();await page.locator('#conversation-calendar').click();await page.locator('.calendar-time-grid[data-mode="day"]').waitFor();
    assert.deepEqual(errors,[]);assert.deepEqual(writes,[]);
    return {mobile_default_day:true,separate_calendar_and_list_icons:true,calendar_range_sheet:true,range_sheet_backdrop_escape_swipe_and_browser_back:true,list_to_calendar_range_selection:true,day_month_touch_navigation:true,vertical_touch_without_date_change:true,cancelled_touch_without_date_change:true,multi_native_touch_snap:true,multi_fixed_time_axis:true,multi_buffer_refill:true,multi_cross_year:true,multi_responsive_columns:true,independent_view_preferences:true,optional_storage:true};
  }catch(error){await shot('mobile-gesture-failure');console.log('MOBILE_TRACE',JSON.stringify(trace));console.log('MOBILE_STATE',await root.evaluate(el=>({mode:el.dataset.mode,date:document.getElementById('calendar-date').value,left:el.scrollLeft,top:el.scrollTop,first:el.querySelector('.calendar-day-column')?.dataset.day,error:document.getElementById('calendar-error').textContent})));throw error;}
  finally{await context.close();}
};
