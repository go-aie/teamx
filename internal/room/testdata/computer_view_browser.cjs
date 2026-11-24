const {chromium}=require('playwright');
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
(async()=>{
 const endpoint=process.argv[2],fixture=JSON.parse(process.argv[3]);
 const browser=await chromium.launch({headless:true});
 const dir=process.env.TEAMX_COMPUTER_VIEW_EVIDENCE;
 const errors=[];let contextForDebug;
 try{
  const context=await browser.newContext({viewport:{width:1600,height:1000}}),page=await context.newPage();contextForDebug=context;
  page.on('pageerror',e=>errors.push(e.message));page.on('console',m=>{if(m.type()==='error')console.error('browser console:',m.text())});page.on('response',r=>{if(r.status()>=400)console.error('HTTP',r.status(),new URL(r.url()).pathname)});page.setDefaultTimeout(20000);
  const selectRoom=async(p,room)=>{await p.locator(`[data-room-key*='${room.room_instance}'] .room-choice`).click();await p.waitForFunction(()=>!document.querySelector('#message-input').disabled);};
  await page.goto(endpoint);await selectRoom(page,fixture.room);
  const pixels=async p=>{
   const viewer=p.frames().find(f=>f.url().endsWith('/viewer'));assert.ok(viewer,'viewer mounted');
   await viewer.waitForFunction(()=>{const c=document.querySelector('canvas');if(!c||c.width<100)return false;const d=c.getContext('2d').getImageData(0,0,c.width,c.height).data,colors=new Set();for(let i=0;i<d.length;i+=400)colors.add(`${d[i]},${d[i+1]},${d[i+2]}`);return colors.size>5;});
  };
  const open=async p=>{await p.locator('#room-menu-label').click();await p.locator('#conversation-open-environment').click();if(fixture.group){await p.locator(`#environment-source option[value='${fixture.first_screen_id}']`).waitFor({state:'attached'});await p.locator('#environment-source').selectOption(fixture.first_screen_id)}await p.locator('#environment-status').filter({hasText:/已连接|其他页面正在接管|已交还 Agent/}).waitFor();await pixels(p);};
  await page.locator('#message-input').fill('电脑面板切换保留的草稿');await open(page);
  await page.locator('#environment-expand').click();assert.equal(await page.locator('#conversation').evaluate(e=>e.inert),true);
  await page.locator('#environment-expand').click();
  await page.locator('#conversation-files').click();assert.equal(await page.locator('#environment-screen iframe').count(),0);await open(page);
  if(!fixture.group){await page.locator('#conversation-calendar').click();assert.equal(await page.locator('#environment-screen iframe').count(),0);await open(page);}
  await page.locator('#environment-refresh').click();await page.locator('#environment-status').filter({hasText:'已连接'}).waitFor();await pixels(page);
  await page.locator('#environment-control').click();await page.locator('#environment-status').filter({hasText:'可直接输入'}).waitFor();
  const other=await context.newPage();await other.goto(endpoint);await selectRoom(other,fixture.room);await open(other);
  assert.equal(await other.locator('#environment-control').isDisabled(),true);await other.close();await page.bringToFront();
  const canvas=page.frameLocator('#environment-screen iframe').locator('canvas');await canvas.click({position:{x:100,y:100}});
  const status=async()=>{const r=await fetch(fixture.fixture_status);if(!r.ok)throw Error(`fixture status HTTP ${r.status}`);return r.json()};
  const waitStatus=async(check,message)=>{const deadline=Date.now()+60000;let lastError;while(Date.now()<deadline){try{const s=await status();if(check(s))return s}catch(e){lastError=e}await page.waitForTimeout(150)}throw Error(message+(lastError ? ` (${lastError.message})` : ''))};
  const generation=(await waitStatus(s=>Number.isInteger(s.form_loads),'fixture status did not become available')).form_loads;
  await page.keyboard.press('Control+l');await page.waitForTimeout(300);await page.keyboard.type('http://127.0.0.1:18765/form',{delay:60});await page.keyboard.press('Enter');
  await waitStatus(s=>s.form_loads>generation && s.generation===s.form_loads && s.focus==='username','form did not load and focus username through VNC');
  await page.keyboard.type('demo-user',{delay:100});await waitStatus(s=>s.username_ok,'VNC username input not observed');await page.keyboard.press('Tab');
  await waitStatus(s=>s.focus==='password','VNC Tab did not focus password');
  if(dir)await page.screenshot({path:path.join(dir,'desktop.png')});
  await page.setViewportSize({width:390,height:844});await page.waitForTimeout(300);
  assert.equal(await page.locator('#conversation').evaluate(e=>e.inert),true);
  const viewer=page.frameLocator('#environment-screen iframe');await viewer.locator('#keyboard-toggle').click();
  await viewer.locator('#keyboard').fill('demo-pass-4x');await waitStatus(s=>s.password_length===12,'VNC temporary keyboard input not observed');
  await viewer.locator('#keyboard').press('Backspace');await waitStatus(s=>s.password_length===11,'VNC Backspace not observed');await viewer.locator('#keyboard').fill('2');
  await waitStatus(s=>s.password_ok,'VNC corrected password not observed');
  assert.equal(await viewer.locator('#keyboard').inputValue(),'');await viewer.locator('#keyboard').press('Enter');
  await waitStatus(s=>s.logins===1,'fictional login did not complete through VNC keyboard');
  await page.waitForTimeout(400);if(dir)await page.screenshot({path:path.join(dir,'mobile.png')});
  if(fixture.combined){for(let i=0;i<180;i++){const s=await(await fetch(fixture.held_probe)).json();if(s.completed>=2)break;if(i===179)throw Error('B real DSH turn did not complete during A takeover');await page.waitForTimeout(1000)}}
  await page.locator('#environment-control').click();await page.locator('#environment-status').filter({hasText:'已交还 Agent'}).waitFor();
  await page.locator('#environment-close').click();assert.equal(await page.locator('#message-input').inputValue(),'电脑面板切换保留的草稿');
  await page.setViewportSize({width:1600,height:1000});await open(page);
  if(fixture.group){
   await page.locator('#environment-source').selectOption(fixture.second_screen_id);
   await page.waitForFunction(id=>document.querySelector('#environment-screen iframe')?.src.includes(id),fixture.second_screen_id);
   await page.locator('#environment-status').filter({hasText:'已连接'}).waitFor();await pixels(page);if(dir)await page.screenshot({path:path.join(dir,'member-selection.png')});
  }
  await selectRoom(page,fixture.other_room);assert.equal(await page.locator('#environment-screen iframe').count(),0);
  if(fixture.group){await page.locator('#room-menu-label').click();await page.locator('#conversation-open-environment').click();await page.locator('#environment-status').filter({hasText:'暂未配置'}).waitFor();assert.equal(await page.locator('#environment-screen iframe').count(),0)}else{await open(page)}
  assert.deepEqual(errors,[]);
  const report={passed:true,real_chromium:true,real_managed_runtime:true,viewports:['1600x1000','390x844'],checks:['pixel frames',fixture.group?'sidebar expand and files switching':'sidebar expand and files/calendar switching','draft preserved','page lease exclusivity','mouse focus and keyboard input','mobile temporary keyboard clears input','fictional login through VNC','handoff returns prior scheduling',fixture.group?'same-Room member picker and empty private Room':'member screen switching'],errors};
  if(dir)fs.writeFileSync(path.join(dir,'browser.json'),JSON.stringify(report,null,2));console.log(JSON.stringify(report));
 }catch(e){for(const p of contextForDebug?.pages?.()||[]){console.error("panel",await p.locator("#environment-status").textContent().catch(()=>"unavailable"));for(const f of p.frames()){console.error("frame",new URL(f.url()).pathname,await f.locator("#status").textContent().catch(()=>"no status"))}if(dir)await p.screenshot({path:path.join(dir,"failure.png")}).catch(()=>{})}throw e;}finally{await browser.close();}
})().catch(e=>{console.error(e.stack);process.exit(1);});
