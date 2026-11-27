const {chromium}=require('playwright');
const assert=require('node:assert/strict');
(async()=>{
 assert.ok(['functional','idle'].includes(process.argv[3]),'explicit browser scenario required');
 const endpoint=process.argv[2], mode=process.argv[3], browser=await chromium.launch({headless:true});
 try {
  const page=await browser.newPage({viewport:{width:1440,height:1000}}), errors=[], reads=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.addInitScript(()=>{
   const Native=EventSource; window.updateSources=[];
   window.EventSource=class extends Native {
    constructor(url){super(url);this.frames=[];this.closed=false;window.updateSources.push(this);
     for(const event of ['hello','invalidate','resync','heartbeat'])this.addEventListener(event,e=>{const data=JSON.parse(e.data);this.frames.push({event,data});if(event==='hello')this.connectionID=data.connection_id;});
    }
    close(){this.closed=true;super.close();}
   };
  });
  page.on('request',r=>{const path=new URL(r.url()).pathname.replace(/^\/s\/[a-f0-9]{32}(?=\/)/,'');if(path.startsWith('/v1/')&&path!=='/v1/updates')reads.push(path);});
  await page.goto(endpoint);
  try { await page.waitForFunction(()=>window.updateSources.some(s=>s.frames.some(f=>f.event==='hello'))&&!document.querySelector('#message-input').disabled); }
  catch(error){console.error(JSON.stringify({errors,reads,state:await page.evaluate(()=>({connection:document.querySelector('#connection').textContent,sync:document.querySelector('#sync-error').textContent,sources:window.updateSources?.map(s=>({url:s.url,state:s.readyState,frames:s.frames}))}))}));throw error;}
  const config=await(await fetch(endpoint+'/v1/ui')).json();
  const catalog=config.managed?await(await fetch(endpoint+'/v1/rooms')).json():null;
  const info=catalog?catalog.rooms.find(r=>r.service).service:{room_id:config.room_id,room_instance:config.room_instance,service_epoch:config.service_epoch},caller={kind:'human',id:catalog?.operator||config.operator};
  async function rpc(path,body){const r=await fetch(endpoint+path,{method:'POST',headers:{'Content-Type':'application/json',Origin:endpoint},body:JSON.stringify({...info,caller,...body})});assert.equal(r.status,200,await r.clone().text());return r.json();}
  for(const member of (await rpc('/v1/member/list',{})).members)await rpc('/v1/member/pause',{agent_id:member.agent_id,membership_id:member.membership_id});
  await page.waitForTimeout(3000);
  await page.locator('#message-input').fill('草稿与焦点保留');
  const baseline=reads.length, sources=await page.evaluate(()=>window.updateSources.length);
  if(mode==='idle'){
  await page.waitForTimeout(60000);
  assert.deepEqual(reads.slice(baseline),[],'sixty idle seconds have no display or command HTTP reads');
  assert.equal(await page.evaluate(()=>window.updateSources.filter(s=>!s.closed).length),1);
  assert.equal(await page.evaluate(()=>window.updateSources.length),sources);
  assert.ok(await page.evaluate(()=>window.updateSources.at(-1).frames.filter(f=>f.event==='heartbeat').length>=3));
  assert.deepEqual(errors,[]);
  console.log(JSON.stringify({mode,managed:!!config.managed,idle_seconds:60,idle_http_reads:0,active_streams:1,heartbeats:'>=3'}));
  return;
  }
  const sent=await rpc('/v1/send',{client_message_id:'sse-page-once',text:'实时消息通过 HTTP 补读'});
  await page.locator('#message-'+sent.seq).waitFor();
  assert.equal(await page.locator('#message-input').inputValue(),'草稿与焦点保留');
  const active=await page.evaluate(()=>document.activeElement.id);assert.equal(active,'message-input');
  if(config.managed){
   await page.locator('#user-menu-toggle').click();
   await page.locator('#open-settings').click();
   await page.locator('#settings-tab-models').click();
   await page.waitForTimeout(2000);
   const modelIdle=reads.length;
   await page.waitForTimeout(5000);
   assert.deepEqual(reads.slice(modelIdle),[],'unchanged model settings do not poll');
   await page.locator('#close-settings').click();
   assert.equal(await page.evaluate(()=>window.updateSources.length),sources,'panel switches reuse the same stream');
  }
  // Browser events exercise the production lifecycle; this is not a claim of
  // physical-device background or system keyboard acceptance.
  await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:true});document.dispatchEvent(new Event('visibilitychange'));});
  assert.equal(await page.evaluate(()=>window.updateSources.filter(s=>!s.closed).length),0);
  const away=await rpc('/v1/send',{client_message_id:'sse-away-once',text:'后台期间提交'});
  await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:false});document.dispatchEvent(new Event('visibilitychange'));});
  await page.locator('#message-'+away.seq).waitFor();
  await page.waitForFunction(()=>!document.querySelector('#send').disabled);
  assert.equal(await page.evaluate(()=>window.updateSources.filter(s=>!s.closed).length),1);
  assert.equal(await page.locator('#message-input').inputValue(),'草稿与焦点保留');
  assert.equal(reads.filter(p=>p==='/v1/send').length,0,'recovery never submits the retained draft');
  assert.deepEqual(errors,[]);
  console.log(JSON.stringify({mode,managed:!!config.managed,active_streams:1,http_message_catchup:true,lifecycle_catchup:true,draft_and_focus:true}));
  await page.close();
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
