const {chromium}=require('playwright');
const assert=require('node:assert/strict');

(async()=>{
 const endpoint=process.argv[2], rooms=JSON.parse(process.argv[3]);
 const browser=await chromium.launch({headless:true});
 try {
  const page=await browser.newPage({viewport:{width:1440,height:1000}}), errors=[], requests=[];
  page.on('pageerror',error=>errors.push(error.message));
  page.on('request',request=>{
   const path=new URL(request.url()).pathname;
   if(path.startsWith('/v1/')&&path!=='/v1/updates')requests.push(path);
  });
  await page.addInitScript(()=>{
   const Native=EventSource;window.updateSources=[];
   window.EventSource=class extends Native {
    constructor(url){super(url);this.closed=false;this.hello=null;this.events=[];window.updateSources.push(this);this.addEventListener('hello',e=>this.hello=JSON.parse(e.data));this.addEventListener('invalidate',e=>this.events.push(JSON.parse(e.data)));}
    close(){this.closed=true;super.close();}
   };
  });
  const selector=room=>`#room-list > [data-room-key=${JSON.stringify(JSON.stringify([room.room_id,room.room_instance]))}] > button:first-child`;
  async function ready(room){
   await page.waitForFunction(id=>window.updateSources.at(-1)?.hello&&new URL(window.updateSources.at(-1).url).searchParams.get('room_id')===id&&!document.querySelector('#message-input').disabled,room.room_id);
   await page.locator('#messages').getByText('History '+room.room_id,{exact:true}).waitFor();
  }
  await page.goto(endpoint);await ready(rooms[0]);
  // Visit both Rooms before measurement so reading confirmations have settled.
  await page.locator(selector(rooms[1])).click();await ready(rooms[1]);
  await page.locator('#message-input').fill('Draft target');
  await page.waitForTimeout(1500);
  await page.locator(selector(rooms[0])).click();await ready(rooms[0]);
  await page.locator('#message-input').fill('Draft assistant');
  await page.waitForTimeout(1500);
  // Both first visits can publish a durable opened position. Establish a new
  // subscription baseline after those real global catalog changes settle.
  await page.locator(selector(rooms[1])).click();await ready(rooms[1]);await page.waitForTimeout(1500);
  await page.locator(selector(rooms[0])).click();await ready(rooms[0]);await page.waitForTimeout(1500);
  // Real Chromium, native EventSource and isolated Go services. Delay only the
  // authoritative HTTP requests; this is controlled latency, not a WAN claim.
  await page.route('**/v1/**',async route=>{
   if(new URL(route.request().url()).pathname!=='/v1/updates')await new Promise(resolve=>setTimeout(resolve,100));
   await route.continue();
  });
  requests.length=0;
  const started=performance.now();
  await page.locator(selector(rooms[1])).click();await ready(rooms[1]);
  const readyMs=Math.round(performance.now()-started);
  await page.waitForFunction(()=>!document.querySelector('#send').disabled);
  assert.equal(await page.locator('#message-input').inputValue(),'Draft target');
  await page.waitForTimeout(150);
  const switchRequests=[...requests];
  const count=path=>requests.filter(p=>p===path).length;
  if(count('/v1/rooms'))console.error(JSON.stringify({requests,sources:await page.evaluate(()=>window.updateSources.map(s=>({hello:s.hello,events:s.events})))}));
  for(const path of ['/v1/ui','/v1/rooms','/v1/platforms'])assert.equal(count(path),0,path);
  for(const path of ['/v1/workspace','/v1/read','/v1/member/list','/v1/platforms/interactions/list'])assert.equal(count(path),1,path);
  assert.equal(count('/v1/send'),0);
  assert.equal(await page.evaluate(()=>window.updateSources.filter(s=>!s.closed).length),1);
  // Returning from the phone list starts a reading visit on the same stream.
  await page.unroute('**/v1/**');await page.setViewportSize({width:390,height:844});
  const streams=await page.evaluate(()=>window.updateSources.length);
  await page.locator('#mobile-back-to-rooms').click();
  await page.locator(selector(rooms[1])).click();
  await page.waitForFunction(()=>document.querySelector('#mobile-primary-navigation').hidden&&!document.querySelector('#message-input').disabled);
  assert.equal(await page.evaluate(()=>window.updateSources.length),streams);
  assert.equal(await page.locator('#message-input').inputValue(),'Draft target');
  assert.deepEqual(errors,[]);
  console.log(JSON.stringify({simulated_http_latency_ms:100,switch_to_history_and_composer_ms:readyMs,HTTP_requests:switchRequests,global_reads:0,chat_reads:switchRequests.filter(path=>path==='/v1/read').length,active_streams:1,mobile_return:true,drafts_preserved:true}));
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
