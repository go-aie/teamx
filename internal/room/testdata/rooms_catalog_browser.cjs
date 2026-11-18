const {chromium} = require('playwright');
const assert = require('node:assert/strict');
(async () => {
  const endpoint = process.argv[2], rooms = JSON.parse(process.argv[3]), sourceOwner = process.argv[4] === "true";
  const browser = await chromium.launch({headless:true});
  try {
    const page = await browser.newPage({viewport:{width:1440,height:1000}}), errors=[], catalogs=[];
    page.on('pageerror', e => errors.push(e.message));
    page.on('response', r => {if(new URL(r.url()).pathname==='/v1/rooms') catalogs.push(r.status());});
    page.setDefaultTimeout(15000);
    const selector = room => `#room-list > [data-room-key=${JSON.stringify(JSON.stringify([room.room_id,room.room_instance]))}] > button:first-child`;
    const ready = async room => {
      await page.locator('#messages').getByText('History '+room.room_id,{exact:true}).waitFor();
      await page.waitForFunction(()=>!document.querySelector('#message-input').disabled);
    };
    await page.goto(endpoint); await ready(rooms[0]);
    await page.locator('#message-input').fill('Assistant draft');
    await page.locator(selector(rooms[1])).click(); await ready(rooms[1]);
    await page.locator('#message-input').fill('Other draft');
    await page.request.post(endpoint+'/__catalog/hold');
    // Reload the actual application while the old maintenance gate is held.
    // Other Room APIs still wait on this gate in S1. The directory must render.
    const refreshed = page.waitForResponse(r=>new URL(r.url()).pathname==='/v1/rooms');
    await page.reload({waitUntil:'domcontentloaded'});
    assert.equal((await refreshed).status(),200);
    await page.locator(selector(rooms[0])).waitFor();
    await page.locator(selector(rooms[1])).waitFor();
    const samples = await page.evaluate(async () => {
      const values=[];
      for(let i=0;i<100;i++) {
        const start=performance.now();
        const r=await fetch('/v1/rooms',{signal:AbortSignal.timeout(2000)});
        if(r.status!==200) throw new Error('catalog HTTP '+r.status);
        const body=await r.json();
        if(body.rooms.length!==2) throw new Error('incomplete directory');
        values.push(performance.now()-start);
      }
      return values.sort((a,b)=>a-b);
    });
    // Source persistence leaves the other Room usable before it is released.
    if (sourceOwner) {
      await ready(rooms[1]);
      await page.locator(selector(rooms[0])).click(); await ready(rooms[0]);
      await page.locator(selector(rooms[1])).click(); await ready(rooms[1]);
    }
    await page.request.post(endpoint+'/__catalog/release');
    await ready(rooms[1]);
    assert.equal(await page.locator('#message-input').inputValue(),'Other draft');
    await page.locator(selector(rooms[0])).click(); await ready(rooms[0]);
    assert.equal(await page.locator('#message-input').inputValue(),'Assistant draft');
    await page.locator(selector(rooms[1])).click(); await ready(rooms[1]);
    assert.equal(await page.locator('#message-input').inputValue(),'Other draft');
    assert(catalogs.every(status=>status===200)); assert.deepEqual(errors,[]);
    console.log(JSON.stringify({browser:'Chromium',backend:'isolated local HTTP, real Room actors and Store',directory_refresh_while_blocked:true,blocked_boundary:sourceOwner?"notification-source-save":"manager-lock",switch_before_release:sourceOwner,requests_before_release:100,successes:100,p50_ms:samples[49],p95_ms:samples[94],p99_ms:samples[98],max_ms:samples[99],switch_after_release:true,drafts_preserved:true,page_errors:errors}));
  } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
