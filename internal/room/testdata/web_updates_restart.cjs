const {chromium}=require('playwright'),assert=require('node:assert/strict');
(async()=>{
 const endpoint=process.argv[2],browser=await chromium.launch({headless:true});
 try{
  const page=await browser.newPage({viewport:{width:1440,height:1000}}),peer=await browser.newPage({viewport:{width:390,height:844}}),errors=[],writes=[];
  peer.on('pageerror',e=>errors.push(e.message));
  peer.on('request',r=>{if(r.method()==='POST'&&['/v1/send','/v1/models/change','/v1/platforms/connect/start'].includes(new URL(r.url()).pathname))writes.push(r.url());});
  page.on('pageerror',e=>errors.push(e.message));
  page.on('request',r=>{if(r.method()==='POST'&&['/v1/send','/v1/models/change','/v1/platforms/connect/start'].includes(new URL(r.url()).pathname))writes.push(r.url());});
  await peer.goto(endpoint);
  if(await peer.locator('#room-list .room-choice').first().isVisible())await peer.locator('#room-list .room-choice').first().click();
  await peer.waitForFunction(()=>!document.querySelector('#message-input').disabled);
  await peer.locator('#message-input').fill('另一端独立草稿');
  await page.goto(endpoint);await page.waitForFunction(()=>!document.querySelector('#message-input').disabled);
  async function identity(){const response=await fetch(endpoint+'/v1/rooms');assert.equal(response.status,200);const data=await response.json();return{...data.rooms.find(r=>r.service).service,caller:{kind:'human',id:data.operator},manager_epoch:data.service_epoch};}
  const before=await identity();
  async function send(info,id){const {manager_epoch,...base}=info;const response=await fetch(endpoint+'/v1/send',{method:'POST',headers:{'Content-Type':'application/json',Origin:endpoint},body:JSON.stringify({...base,client_message_id:id,text:id})});assert.equal(response.status,200);return response.json();}
  const first=await send(before,'restart-first');await page.locator('#message-'+first.seq).waitFor();
  if(await page.locator('#activity-dialog').evaluate(el=>el.open))await page.locator('#close-activity').click();
  await page.locator('#message-input').fill('重启后仍待发送的草稿');
  assert.equal(await page.evaluate(()=>document.activeElement.id),'message-input');
  const response=await fetch(endpoint+'/__test/restart',{method:'POST',headers:{Origin:endpoint}});assert.equal(response.status,200,await response.text());
  const after=await identity();assert.notEqual(before.manager_epoch,after.manager_epoch);assert.notEqual(before.service_epoch,after.service_epoch);
  const second=await send(after,'restart-second');
  await page.locator('#message-'+second.seq).waitFor();await page.waitForFunction(()=>!document.querySelector('#send').disabled);
  assert.equal(await page.locator('#message-input').inputValue(),'重启后仍待发送的草稿');
  assert.equal(await page.evaluate(()=>document.activeElement.id),'message-input');
  await peer.locator('#message-'+second.seq).waitFor();await peer.waitForFunction(()=>!document.querySelector('#send').disabled);
  assert.equal(await peer.locator('#message-input').inputValue(),'另一端独立草稿');
  assert.equal(await peer.locator('#message-'+first.seq).count(),1);
  assert.equal(await page.locator('#message-'+first.seq).count(),1);assert.deepEqual(writes,[]);assert.deepEqual(errors,[]);
  console.log('PASS: two independent browser contexts, manager and runtime reconstructed from persisted state, automatic identity reconciliation, message catchup, draft/focus preserved, no command replay.');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
