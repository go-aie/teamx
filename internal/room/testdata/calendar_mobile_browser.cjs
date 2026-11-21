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
  const monthFits=async(target,label)=>{
    await target.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
    const geometry=await target.locator('#calendar-items').evaluate(el=>{
      const grid=el.querySelector('.calendar-month-grid'),bounds=el.getBoundingClientRect(),days=[...grid.querySelectorAll('.calendar-month-day')];
      const buttons=[...grid.querySelectorAll('.calendar-month-count')].filter(button=>getComputedStyle(button).display!=='none');
      return {scroll:el.scrollTop,overflow:el.scrollHeight-el.clientHeight,panelOverflow:el.parentElement.scrollHeight-el.parentElement.clientHeight,
        rows:new Set(days.map(day=>Math.round(day.getBoundingClientRect().top))).size,days:days.length,
        bottom:days.at(-1).getBoundingClientRect().bottom,bounds:bounds.toJSON(),
        clipped:buttons.some(button=>button.getBoundingClientRect().bottom>button.parentElement.getBoundingClientRect().bottom+1)};
    });
    assert.equal(geometry.scroll,0,label);assert.equal(geometry.days,42,label);assert.equal(geometry.rows,6,label);
    assert.ok(geometry.overflow<=1&&geometry.panelOverflow<=1&&!geometry.clipped&&geometry.bottom<=geometry.bounds.bottom+1,JSON.stringify({label,...geometry}));
  };
  let capturedDayDrag=false;
  const nativeDaySwipe=async(dx,{cancel=false}={})=>{
    const original=await currentDate(),scroll=await root.evaluate(el=>el.scrollTop);
    const before=await root.evaluate(el=>({axis:el.querySelector('.calendar-time-axis').getBoundingClientRect().left,corner:el.querySelector('.calendar-grid-corner').getBoundingClientRect().left}));
    let release;const held=new Promise(resolve=>{release=resolve;});
    await page.route('**/v1/calendar/list',async route=>{await held;await route.continue();});
    const session=await context.newCDPSession(page),box=await root.boundingBox(),x=box.x+box.width*(dx<0?.78:.22),y=box.y+160;
    const distance=!cancel&&Math.abs(dx)>=48?Math.sign(dx)*220:dx;
    try{
      await session.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:[{x,y}]});
      for(let step=1;step<=10;step++){
        await session.send('Input.dispatchTouchEvent',{type:'touchMove',touchPoints:[{x:x+distance*step/10,y}]});await page.waitForTimeout(20);
        const geometry=await root.evaluate(el=>{
          const bounds=el.getBoundingClientRect(),hit=document.elementFromPoint(bounds.left+60,bounds.top+12);
          return {axis:el.querySelector('.calendar-time-axis').getBoundingClientRect().left,corner:el.querySelector('.calendar-grid-corner').getBoundingClientRect().left,
            headerCoversBody:!!hit?.closest('.calendar-grid-headers'),top:el.scrollTop,transform:el.firstElementChild.style.transform};
        });
        assert.ok(Math.abs(geometry.axis-before.axis)<1&&Math.abs(geometry.corner-before.corner)<1&&Math.abs(geometry.top-scroll)<2,JSON.stringify(geometry));
        assert.equal(geometry.headerCoversBody,true);assert.equal(geometry.transform,'');
      }
      if(artifacts&&!capturedDayDrag){await shot('mobile-day-native-drag');capturedDayDrag=true;}
      await session.send('Input.dispatchTouchEvent',{type:cancel?'touchCancel':'touchEnd',touchPoints:[]});
      const expected=!cancel&&Math.abs(dx)>=48?shift(original,dx<0?1:-1):null;
      await page.waitForTimeout(250);
      await page.waitForFunction(expected=>{
        const el=document.getElementById('calendar-items'),width=el.querySelector('.calendar-day-column').getBoundingClientRect().width,day=document.getElementById('calendar-date').value;
        const column=el.querySelector(`.calendar-day-column[data-day="${day}"]`),bounds=el.getBoundingClientRect();
        return (!expected||day===expected)&&Math.abs(el.scrollLeft/width-Math.round(el.scrollLeft/width))<.02&&Math.abs(column.getBoundingClientRect().left-bounds.left-48)<2;
      },expected);
      assert.deepEqual(await visibleDays(),[await currentDate()]);assert.ok(Math.abs(await root.evaluate(el=>el.scrollTop)-scroll)<2);
    }finally{release();await page.unrouteAll({behavior:'wait'});await session.detach();}
  };
  const smoothSwipe=async(dx,{cancel=false}={})=>{
    if(await root.getAttribute('data-mode')==='day')return nativeDaySwipe(dx,{cancel});
    let release;const held=new Promise(resolve=>{release=resolve;});
    await page.route('**/v1/calendar/list',async route=>{await held;await route.continue();});
    const session=await context.newCDPSession(page),box=await root.boundingBox(),x=box.x+box.width*(dx<0?.78:.22),y=box.y+160;
    try{
      const scroll=await root.evaluate(el=>el.scrollTop);
      await session.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:[{x,y}]});
      for(let step=1;step<=10;step++){
        await session.send('Input.dispatchTouchEvent',{type:'touchMove',touchPoints:[{x:x+dx*step/10,y}]});await page.waitForTimeout(20);
      }
      const direction=!cancel&&Math.abs(dx)>=48?(dx<0?1:-1):0;
      const drag=await root.evaluate((el,{direction})=>{
        const track=el.querySelector('.calendar-swipe-track');if(!track)return null;
        window.__calendarPane=track.children[direction+1];window.__calendarMotion=[];window.__calendarRecord=true;
        const sample=()=>{
          if(!window.__calendarRecord)return;
          const pane=window.__calendarPane,rect=pane.getBoundingClientRect(),bounds=el.getBoundingClientRect(),heading=pane.querySelector('.calendar-month-headers').getBoundingClientRect();
          window.__calendarMotion.push({x:rect.left-bounds.left,y:heading.top-bounds.top,top:el.scrollTop});requestAnimationFrame(sample);
        };requestAnimationFrame(sample);
        return {x:track.children[1].getBoundingClientRect().left-el.getBoundingClientRect().left,top:el.scrollTop};
      },{direction});
      assert.ok(drag);assert.ok(Math.abs(drag.x-dx)<2&&Math.abs(drag.top-scroll)<2,JSON.stringify(drag));
      await session.send('Input.dispatchTouchEvent',{type:cancel?'touchCancel':'touchEnd',touchPoints:[]});
      await page.locator('.calendar-swipe-track').waitFor({state:'detached'});await page.waitForTimeout(35);
      const motion=await page.evaluate(()=>{window.__calendarRecord=false;return {frames:window.__calendarMotion,retained:window.__calendarPane.isConnected};});
      assert.equal(motion.retained,true);assert.ok(motion.frames.length>=3,JSON.stringify(motion));
      for(let index=1;index<motion.frames.length;index++){
        const before=motion.frames[index-1],after=motion.frames[index];
        assert.ok(Math.abs(after.x)<=Math.abs(before.x)+1&&Math.abs(after.top-scroll)<2&&Math.abs(after.y-before.y)<2,JSON.stringify(motion));
      }
      assert.ok(Math.abs(motion.frames.at(-1).x)<2,JSON.stringify(motion));
    }finally{release();await page.unrouteAll({behavior:'wait'});await session.detach();}
    if(!cancel&&Math.abs(dx)>=48)await page.waitForFunction(()=>!window.__calendarPane.isConnected);
  };
  const visibleDays=()=>root.evaluate(el=>{
    const bounds=el.getBoundingClientRect();
    return [...el.querySelectorAll('.calendar-day-column')].filter(column=>{
      const rect=column.getBoundingClientRect();return rect.left>=bounds.left+47&&rect.right<=bounds.right+1;
    }).map(column=>column.dataset.day);
  });
  const returnToday=async(day,{duringScroll=false}={})=>{
    const expected=await root.evaluate((el,duringScroll)=>{
      const hour=Number(el.querySelector('.calendar-now-line > span').textContent.split(':')[0]);
      const top=Math.min(el.scrollHeight-el.clientHeight,Math.max(0,(hour-1)*56));
      if(!duringScroll)el.scrollTop=top>200?0:600;return top;
    },duringScroll);
    let release;const held=new Promise(resolve=>{release=resolve;});
    await page.route('**/v1/calendar/list',async route=>{await held;await route.continue();});
    try{
      await page.locator('#calendar-today').click();
      await page.waitForFunction(({day,expected})=>{
        const root=document.getElementById('calendar-items'),bounds=root.getBoundingClientRect();
        const first=[...root.querySelectorAll('.calendar-day-column')].find(column=>column.getBoundingClientRect().left>=bounds.left+47);
        return document.getElementById('calendar-date').value===day&&first?.dataset.day===day&&Math.abs(root.scrollTop-expected)<2;
      },{day,expected});
      // A pending native scroll must not change the selection back afterward.
      await page.waitForTimeout(250);assert.equal(await currentDate(),day);
      assert.ok(Math.abs(await root.evaluate(el=>el.scrollTop)-expected)<2);
    }finally{release();await page.unrouteAll({behavior:'wait'});}
  };
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
      await root.evaluate((el,{step,expected})=>{
        const width=el.querySelector('.calendar-day-column').getBoundingClientRect().width;
        window.__calendarColumn=el.querySelector(`.calendar-day-column[data-day="${expected}"]`);
        el.scrollTo({left:el.scrollLeft+step*width,behavior:'instant'});
        window.__calendarRefill=[];window.__calendarRecordRefill=true;
        const sample=()=>{
          if(!window.__calendarRecordRefill)return;
          window.__calendarRefill.push({connected:window.__calendarColumn.isConnected,x:window.__calendarColumn.getBoundingClientRect().left-el.getBoundingClientRect().left,top:el.scrollTop});
          requestAnimationFrame(sample);
        };requestAnimationFrame(sample);
      },{step,expected});
      await waitDate(expected);
      await page.waitForFunction(()=>{
        const el=document.getElementById('calendar-items'),columns=el.querySelectorAll('.calendar-day-column');
        const width=columns[0].getBoundingClientRect().width,index=Math.round(el.scrollLeft/width),days=Math.round((el.clientWidth-48)/width);
        return index>=3&&index+days<=columns.length-3;
      });
      await page.evaluate(()=>new Promise(requestAnimationFrame));
      const frames=await page.evaluate(()=>{window.__calendarRecordRefill=false;return window.__calendarRefill;});
      for(const frame of frames){assert.equal(frame.connected,true,'refill must retain visible snap targets');assert.ok(Math.abs(frame.x-48)<2&&Math.abs(frame.top-600)<2,JSON.stringify(frames));}
      days-=step;
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
    await returnToday(original);await returnToday(original);
    await root.evaluate(el=>{el.scrollTop=600;});
    await smoothSwipe(-130);await waitDate(shift(original,1));
    assert.deepEqual(await visibleDays(),[shift(original,1)]);
    assert.ok(Math.abs(await root.evaluate(el=>el.scrollTop)-600)<3);
    assert.equal(await page.locator('#calendar-detail').isVisible(),false);
    await smoothSwipe(130);await waitDate(original);
    await smoothSwipe(30);await returnToday(original);
    await root.evaluate(el=>{el.scrollTop=600;});
    await smoothSwipe(-80,{cancel:true});await returnToday(original);
    await root.evaluate(el=>{el.scrollTop=600;});
    await swipeCalendar(page,8,-100);
    assert.equal(await currentDate(),original);
    assert.ok(await root.evaluate(el=>el.scrollTop)>600);
    const touch=await context.newCDPSession(page),bounds=await root.boundingBox(),x=bounds.x+220,y=bounds.y+160;
    await touch.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:[{id:0,x,y}]});
    await touch.send('Input.dispatchTouchEvent',{type:'touchMove',touchPoints:[{id:0,x:x-60,y}]});
    assert.equal(await root.evaluate(el=>el.firstElementChild.style.transform),'');
    await touch.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:[{id:0,x:x-60,y},{id:1,x:x-20,y:y+40}]});
    assert.equal(await root.evaluate(el=>el.firstElementChild.style.transform),'');
    await touch.send('Input.dispatchTouchEvent',{type:'touchCancel',touchPoints:[]});await touch.detach();
    assert.equal(await currentDate(),original);
    await chooseMode('month');await page.locator('.calendar-month-grid').waitFor();
    const windows=[];
    await page.route('**/v1/calendar/list',async route=>{
      const query=route.request().postDataJSON().calendar;
      if(!query.cursor)windows.push(query.from);
      if(!query.cursor&&windows.length===2){
        const response=await route.fetch(),data=await response.json();data.revision++;
        await route.fulfill({response,json:data});
      }else await route.continue();
    });
    const restarted=page.waitForResponse(async response=>response.url().endsWith('/v1/calendar/list')&&windows.length>=5&&response.request().postDataJSON().calendar.from===windows[4]&&!(await response.json()).next_cursor);
    await chooseDate('2026-12-31',page);
    await restarted;await page.evaluate(()=>new Promise(requestAnimationFrame));
    await page.waitForFunction(()=>document.querySelector('.calendar-month-grid')?.dataset.month==='2026-12');
    await page.unrouteAll({behavior:'wait'});
    assert.ok(windows.length>=5,JSON.stringify(windows));
    assert.equal(windows[0],windows[2]);assert.equal(windows[1],windows[3]);
    const occurrenceIDs=await root.locator('.calendar-occurrence').evaluateAll(items=>items.map(el=>el.dataset.occurrenceId));
    assert.equal(new Set(occurrenceIDs).size,occurrenceIDs.length,'neighbor reads must not duplicate arrangements');
    await smoothSwipe(-130);await page.waitForFunction(()=>document.querySelector('.calendar-month-grid')?.dataset.month==='2027-01');
    assert.equal(await currentDate(),'2027-01-31');
    await smoothSwipe(130);await page.waitForFunction(()=>document.querySelector('.calendar-month-grid')?.dataset.month==='2026-12');
    assert.equal(await currentDate(),'2026-12-31');
    await page.emulateMedia({reducedMotion:'reduce'});
    await swipeCalendar(page,-130);await waitDate('2027-01-31');
    assert.equal(await page.locator('.calendar-swipe-track').count(),0);
    await swipeCalendar(page,130);await waitDate('2026-12-31');
    await page.emulateMedia({reducedMotion:'no-preference'});
    for(const height of [844,568,420,360]){
      await page.setViewportSize({width:390,height});await monthFits(page,`mobile month ${height}`);await shot(`mobile-month-fit-${height}`);
    }
    await page.setViewportSize({width:390,height:844});
    await chooseMode('multi');await page.locator('.calendar-time-grid[data-mode="multi"]').waitFor();
    await chooseDate(original,page);
    await page.waitForFunction(day=>document.querySelector('#calendar-date').value===day&&document.querySelector(`.calendar-day-column[data-day="${day}"]`),original);
    await returnToday(original);
    await root.evaluate(el=>{const width=el.querySelector('.calendar-day-column').getBoundingClientRect().width;el.scrollTo({left:el.scrollLeft+width,behavior:'instant'});});
    await waitDate(shift(original,1));await returnToday(original);
    const scrollStart=await root.evaluate(el=>{
      const start=el.scrollLeft,width=el.querySelector('.calendar-day-column').getBoundingClientRect().width;
      el.scrollTo({left:start+5*width,behavior:'smooth'});return start;
    });
    await page.waitForFunction(start=>document.getElementById('calendar-items').scrollLeft>start+5,scrollStart);
    await returnToday(original,{duringScroll:true});assert.equal((await visibleDays())[0],original);
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
    await root.evaluate(el=>{el.scrollTop=620;});
    await chooseMode('day');await page.locator('.calendar-time-grid[data-mode="day"]').waitFor();
    await root.evaluate(el=>{el.scrollTop=350;});
    await smoothSwipe(-130);assert.ok(Math.abs(await root.evaluate(el=>el.scrollTop)-350)<2);
    await smoothSwipe(130);
    await chooseMode('multi');await page.locator('.calendar-time-grid[data-mode="multi"]').waitFor();
    assert.ok(Math.abs(await root.evaluate(el=>el.scrollTop)-620)<2);
    await chooseMode('month');await page.locator('.calendar-month-grid').waitFor();await monthFits(page,'mobile month after time scroll');
    await chooseMode('day');await page.locator('.calendar-time-grid[data-mode="day"]').waitFor();
    assert.ok(Math.abs(await root.evaluate(el=>el.scrollTop)-350)<2);
    await chooseMode('multi');await page.locator('.calendar-time-grid[data-mode="multi"]').waitFor();
    assert.ok(Math.abs(await root.evaluate(el=>el.scrollTop)-620)<2);
    await root.evaluate(el=>{el.scrollTop=600;});
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
    const desktopRoot=desktop.locator('#calendar-items');
    const desktopMode=async mode=>{
      await desktop.locator(`#calendar-mode [data-mode="${mode}"]`).click();
      await desktop.locator(mode==='month'?'.calendar-month-grid':`.calendar-time-grid[data-mode="${mode}"]`).waitFor();
    };
    const desktopDay=await desktop.locator('#calendar-date').inputValue();
    await desktopRoot.evaluate(el=>{el.scrollTop=420;});
    await desktop.locator('#calendar-next').click();
    await desktop.waitForFunction(day=>document.querySelector('#calendar-date').value===day,shift(desktopDay,7));
    assert.ok(Math.abs(await desktopRoot.evaluate(el=>el.scrollTop)-420)<2);
    await desktop.locator('#calendar-prev').click();
    await desktop.waitForFunction(day=>document.querySelector('#calendar-date').value===day,desktopDay);
    assert.ok(Math.abs(await desktopRoot.evaluate(el=>el.scrollTop)-420)<2);
    await desktopMode('day');await desktopRoot.evaluate(el=>{el.scrollTop=210;});
    await desktop.locator('#calendar-next').click();
    await desktop.waitForFunction(day=>document.querySelector('#calendar-date').value===day,shift(desktopDay,1));
    assert.ok(Math.abs(await desktopRoot.evaluate(el=>el.scrollTop)-210)<2);
    await desktop.locator('#calendar-prev').click();
    await desktop.waitForFunction(day=>document.querySelector('#calendar-date').value===day,desktopDay);
    assert.ok(Math.abs(await desktopRoot.evaluate(el=>el.scrollTop)-210)<2);
    await desktopMode('week');assert.ok(Math.abs(await desktopRoot.evaluate(el=>el.scrollTop)-420)<2);
    await desktopMode('month');
    for(const height of [1000,700,500]){
      await desktop.setViewportSize({width:1440,height});await monthFits(desktop,`desktop month ${height}`);
      if(artifacts)await desktop.screenshot({path:`${artifacts}/desktop-month-fit-${height}.png`,fullPage:true});
      const month=await desktop.locator('.calendar-month-grid').getAttribute('data-month');
      await desktop.locator('#calendar-next').click();await desktop.waitForFunction(month=>document.querySelector('.calendar-month-grid')?.dataset.month!==month,month);
      await monthFits(desktop,`desktop next month ${height}`);
      await desktop.locator('#calendar-prev').click();await desktop.waitForFunction(month=>document.querySelector('.calendar-month-grid')?.dataset.month===month,month);
    }
    const compactCount=desktop.locator('.calendar-month-count:visible').first();
    await compactCount.click();await desktop.locator('.calendar-time-grid[data-mode="day"]').waitFor();
    assert.ok(Math.abs(await desktopRoot.evaluate(el=>el.scrollTop)-210)<2);
    await desktop.setViewportSize({width:1440,height:1000});
    await desktopMode('week');assert.ok(Math.abs(await desktopRoot.evaluate(el=>el.scrollTop)-420)<2);
    await desktopMode('day');assert.ok(Math.abs(await desktopRoot.evaluate(el=>el.scrollTop)-210)<2);
    await desktopMode('month');await monthFits(desktop,'desktop month after time scroll');
    await desktop.reload();await desktop.locator('#conversation-calendar').click();await desktop.locator('.calendar-month-grid').waitFor();
    await desktop.close();
    // Unavailable storage must still allow a fresh mobile calendar to open.
    await page.addInitScript(()=>Object.defineProperty(window,'localStorage',{get(){throw new Error('storage disabled');}}));
    await page.reload();await page.locator('#conversation-calendar').click();await page.locator('.calendar-time-grid[data-mode="day"]').waitFor();
    assert.deepEqual(errors,[]);assert.deepEqual(writes,[]);
    return {mobile_default_day:true,separate_calendar_and_list_icons:true,calendar_range_sheet:true,range_sheet_backdrop_escape_swipe_and_browser_back:true,list_to_calendar_range_selection:true,day_month_touch_navigation:true,continuous_drag_and_release:true,day_native_scroll_shared_with_multi:true,day_fixed_time_axis_during_drag_and_settlement:true,day_fixed_zone_and_clock_labels:true,day_content_width_stable:true,today_repeated_without_data_change:true,today_from_buffered_dates:true,today_interrupts_native_scroll:true,today_without_waiting_for_read:true,short_drag_native_snap:true,cancelled_drag_native_snap:true,reduced_motion_navigation:true,slow_read_without_snap_back:true,neighbor_snapshot_restart:true,refill_retains_visible_columns:true,vertical_touch_without_date_change:true,cancelled_touch_without_date_change:true,multi_native_touch_snap:true,multi_fixed_time_axis:true,multi_buffer_refill:true,multi_cross_year:true,multi_responsive_columns:true,independent_view_preferences:true,independent_time_scroll_positions:true,desktop_arrows_preserve_time_scroll:true,month_fits_available_height:true,compact_month_counts_remain_accessible:true,optional_storage:true};
  }catch(error){await shot('mobile-gesture-failure');console.log('MOBILE_TRACE',JSON.stringify(trace));console.log('MOBILE_STATE',await root.evaluate(el=>({mode:el.dataset.mode,date:document.getElementById('calendar-date').value,left:el.scrollLeft,top:el.scrollTop,first:el.querySelector('.calendar-day-column')?.dataset.day,error:document.getElementById('calendar-error').textContent})));throw error;}
  finally{await context.close();}
};
