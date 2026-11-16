const {chromium}=require('playwright');
const assert=require('node:assert/strict');
(async()=>{
 const endpoint=process.argv[2],path=process.argv[3],browser=await chromium.launch({headless:true});
 try{
  const context=await browser.newContext(),page=await context.newPage();
  await page.goto(endpoint+'/access');
  const status=await page.evaluate(async token=>(await fetch('/v1/access/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({token})})).status,process.env.TEAMX_WEB_UPDATES_TEST_TOKEN);
  assert.equal(status,200);
  await page.evaluate(path=>{
   window.updateFrames=[];window.updateErrors=0;window.updateSource=new EventSource(path);
   for(const event of ['hello','invalidate','resync','heartbeat'])window.updateSource.addEventListener(event,e=>window.updateFrames.push({event,data:JSON.parse(e.data),id:e.lastEventId}));
   window.updateSource.onerror=()=>{window.updateErrors++;window.updateSource.close();};
  },path);
  await page.waitForFunction(()=>window.updateFrames.some(f=>f.event==='hello'));
  const result=await page.evaluate(async path=>{
   const query=new URL(path,location.origin).searchParams,session=await(await fetch('/v1/access/session')).json();
   const info={room_id:query.get('room_id'),room_instance:query.get('room_instance'),service_epoch:query.get('room_service_epoch')},caller={kind:'human',id:session.operator};
   const sent=await fetch('/v1/send',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({...info,caller,client_message_id:'browser-once',text:'HTTP-only body'})});
   return {status:sent.status,info,caller};
  },path);
  assert.equal(result.status,200);
  await page.waitForFunction(()=>window.updateFrames.some(f=>f.event==='invalidate'&&f.data.topic==='messages'));
  const frames=await page.evaluate(()=>window.updateFrames);
  assert.equal(frames[0].event,'hello');assert.ok(frames.every(f=>f.id===''));assert.ok(frames.every(f=>!JSON.stringify(f).includes('HTTP-only body')));
  const read=await page.evaluate(async({info,caller})=>(await fetch('/v1/read',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({...info,caller,mode:'all'})})).json(),result);
  assert.equal(read.messages.length,1);assert.equal(read.messages[0].text,'HTTP-only body');
  assert.equal(await page.evaluate(async()=>(await fetch('/v1/access/logout',{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'})).status),200);
  await page.waitForFunction(()=>window.updateErrors===1);
  console.log('Chromium EventSource passed: cookie authentication, hello before invalidation, HTTP state read, logout closes stream.');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
