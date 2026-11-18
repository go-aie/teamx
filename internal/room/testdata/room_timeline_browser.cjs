const {chromium}=require('playwright'),assert=require('node:assert/strict'),path=require('node:path');
(async()=>{
 const [endpoint,encoded,mode,out]=process.argv.slice(2),room=JSON.parse(encoded),browser=await chromium.launch({headless:true});
 try{
  const page=await browser.newPage({viewport:{width:1440,height:1000}}),errors=[];page.on('pageerror',e=>errors.push(e.message));page.setDefaultTimeout(15000);
  await page.goto(endpoint);
  await page.locator(`#room-list > [data-room-key=${JSON.stringify(JSON.stringify([room.room_id,room.room_instance]))}] > button:first-child`).click();
  await page.waitForFunction(()=>!document.querySelector('#message-input').disabled);
  if(await page.locator('#activity-dialog').evaluate(e=>e.open))await page.locator('#close-activity').click();
  async function send(text){await page.locator('#message-input').fill(text);const response=page.waitForResponse(r=>new URL(r.url()).pathname==='/v1/send'&&r.request().method()==='POST');await page.locator('#send').click();const result=await response;assert.equal(result.status(),200);const receipt=await result.json();await page.locator('#message-'+receipt.seq).waitFor();return receipt;}
  if(mode==='initial'){
   assert.equal((await send('ROOM_BROWSER_ONE')).seq,1);
   await page.locator('#message-1 .reply-button').click();
   assert.equal((await send('ROOM_BROWSER_REPLY')).seq,2);
   await page.locator('#message-2 .message-reference').waitFor({state:'visible'});
   const response=await page.evaluate(async()=>{
    const catalog=await(await fetch('/v1/rooms')).json(),service=catalog.rooms.find(r=>r.service).service;
    const r=await fetch('/v1/messages/clear',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({...service,caller:{kind:'human',id:catalog.operator},snapshot_seq:1})});return {status:r.status,body:await r.json()};
   });assert.equal(response.status,200,JSON.stringify(response));assert.equal(response.body.cleared_through,1);
   await page.locator('#message-1').waitFor({state:'detached'});await page.locator('#message-2').waitFor();
   await page.reload();await page.locator('#message-2').waitFor();assert.equal(await page.locator('#message-1').count(),0);
  }else{
   await page.locator('#message-2').waitFor();assert.equal(await page.locator('#message-1').count(),0);
   assert.match(await page.locator('#message-2 .message-reference').textContent(),/已清空/);
   assert.equal((await send('ROOM_BROWSER_AFTER_RESTART')).seq,3);
  }
  await page.screenshot({path:path.join(out,mode+'-desktop.png'),fullPage:true});
  await page.setViewportSize({width:390,height:844});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  await page.screenshot({path:path.join(out,mode+'-mobile.png'),fullPage:true});assert.deepEqual(errors,[]);
  console.log(JSON.stringify({browser:'Chromium',mode,ui_send:true,reply:true,clear_via_http:true,desktop:true,mobile:true,page_errors:errors}));
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
