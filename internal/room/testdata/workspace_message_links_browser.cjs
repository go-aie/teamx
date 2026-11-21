const {chromium}=require('playwright');
const assert=require('node:assert/strict');
const fs=require('node:fs');
(async()=>{
 const endpoint=process.argv[2],fixture=JSON.parse(process.argv[3]);
 const browser=await chromium.launch({headless:true});
 try{
  const page=await browser.newPage({viewport:{width:1600,height:1000}}),errors=[],requests=[];
  page.on('pageerror',error=>errors.push(error.message));
  page.on('request',request=>{if(request.url().includes('/v1/workspace/files'))requests.push(request.url());});
  page.setDefaultTimeout(12000);
  await page.goto(endpoint);await page.waitForFunction(()=>!document.querySelector('#message-input').disabled);
  await page.locator(`[data-room-key*='${fixture.room.room_instance}'] .room-choice`).click();
  const links=page.locator(`#message-${fixture.seq} button.workspace-file-link`);
  await links.first().waitFor();assert.equal(await links.count(),2); // quote/code never borrow the source
  await page.locator('#message-input').fill('文件链接预览草稿');
  const open=async(index=0)=>{const before=requests.length;await links.nth(index).click();await page.locator('#files-body h1').waitFor();
   assert.equal(await page.locator('#files-body h1').textContent(),'消息直达预览');
   const reads=requests.slice(before);assert.equal(reads.length,1,JSON.stringify(reads));
   const q=new URL(reads[0]).searchParams;assert.equal(q.get('message_seq'),String(fixture.seq));assert.equal(q.get('path'),'docs/中文 空格.md');assert.equal(q.has('membership_id'),false);
  };
  await open();assert.equal(await page.locator('#files-directory').getAttribute('aria-label'),'打开所在目录：docs');
  await page.locator('#files-source-toggle').click();await page.locator('#files-body pre').waitFor();
  await page.locator('#files-refresh').click();await page.waitForFunction(()=>document.querySelector('#files-body').getAttribute('aria-busy')==='false');
  assert.equal(await page.locator('#files-source-toggle').getAttribute('aria-pressed'),'true');
  await page.locator('#files-expand').click();await page.keyboard.press('Escape');assert.equal(await page.locator('#files-page').isVisible(),true);
  await page.keyboard.press('Escape');assert.equal(await page.locator('#files-page').isVisible(),false);
  assert.equal(await links.first().evaluate(el=>document.activeElement===el),true);
  await open(1);await page.locator('#files-directory').click();await page.locator('#files-body .file-entry').getByText('中文 空格.md',{exact:true}).waitFor();
  assert.equal(await page.locator('#files-path [aria-current=page]').textContent(),'docs');
  const directoryRead=requests.filter(url=>new URL(url).pathname==='/v1/workspace/files').at(-1);
  assert.equal(new URL(directoryRead).searchParams.has('message_seq'),false);
  await page.locator('#files-close').click();
  for(const width of [1000,390]){
   await page.setViewportSize({width,height:850});await open();assert.equal(await page.locator('#conversation').evaluate(el=>el.inert),true);
   if(process.env.TEAMX_FILES_SCREENSHOTS&&fixture.name==='room'){
    fs.mkdirSync(process.env.TEAMX_FILES_SCREENSHOTS,{recursive:true});await page.screenshot({path:`${process.env.TEAMX_FILES_SCREENSHOTS}/message-${width}.png`,animations:'disabled'});
   }
   if(width===390)await page.goBack();else await page.keyboard.press('Escape');
   assert.equal(await page.locator('#files-page').isVisible(),false);assert.equal(await page.locator('#message-input').inputValue(),'文件链接预览草稿');
  }
  await page.setViewportSize({width:1600,height:1000});
  const before=requests.length;await page.locator(`#message-${fixture.legacy} button.workspace-file-link`).click();
  await page.locator('#files-error').filter({hasText:'旧消息未记录文件来源'}).waitFor();assert.equal(requests.length,before);
  await page.locator('#files-directory').click();await page.locator('#files-body .file-entry').first().waitFor();await page.locator('#files-close').click();
  // Clear the original reference while its real preview response is delayed.
  let release;const gate=new Promise(resolve=>release=resolve);
  await page.route('**/v1/workspace/files/preview?**',async route=>{const response=await route.fetch();await gate;try{await route.fulfill({response});}catch{}});
  await links.first().click();await page.waitForFunction(()=>document.querySelector('#files-status').textContent==='正在读取…');
  const cleared=await page.evaluate(()=>fetch('/v1/rooms').then(r=>r.json()));
  const catalogRoom=cleared.rooms.find(room=>room.room_instance===fixture.room.room_instance);
  const response=await page.request.post(endpoint+'/v1/messages/clear',{headers:{Origin:endpoint},data:{...catalogRoom.service,caller:{kind:'human',id:cleared.operator},snapshot_seq:fixture.seq}});
  assert.equal(response.status(),200,await response.text());
  await page.locator('#files-error').filter({hasText:'原消息已清空'}).waitFor();release();await page.waitForTimeout(150);
  assert.equal(await page.locator('#files-body h1').count(),0);
  await page.unroute('**/v1/workspace/files/preview?**');
  await page.locator('#files-refresh').click();await page.locator('#files-error').filter({hasText:'原消息已清空'}).waitFor();
  assert.deepEqual(errors,[]);
  console.log(JSON.stringify({case:fixture.name,status:'passed',browser:browser.version(),node:process.version,platform:process.platform,realHTTP:true,controlledExecution:true,sizes:[1600,1000,390],singlePreviewRequest:true,referenceStyle:true,legacyFeedback:true,messageClear:true,focusRestore:true}));
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
