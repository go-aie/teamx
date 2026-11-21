const {chromium}=require('playwright');
const assert=require('node:assert/strict');
const fs=require('node:fs');
(async()=>{
 const endpoint=process.argv[2],fixture=JSON.parse(process.argv[3]);
 const browser=await chromium.launch({headless:true});
 try{
  const page=await browser.newPage({viewport:{width:1600,height:1000}}),errors=[],requests=[];
  page.setDefaultTimeout(12000);
  page.on('pageerror',error=>errors.push(error.message));
  page.on('request',request=>requests.push(request.url()));
  // Observe resource lifetime without changing the production fetch or renderer.
  await page.addInitScript(()=>{
   const create=URL.createObjectURL.bind(URL),revoke=URL.revokeObjectURL.bind(URL);window.workspaceBlobs=new Set();
   URL.createObjectURL=blob=>{const url=create(blob);workspaceBlobs.add(url);return url;};
   URL.revokeObjectURL=url=>{workspaceBlobs.delete(url);revoke(url);};
  });
  await page.goto(endpoint);await page.waitForFunction(()=>!document.querySelector('#message-input').disabled);
  await page.locator(`[data-room-key*='${fixture.room.room_instance}'] .room-choice`).click();
  const link=page.locator('#message-1 button.workspace-file-link');await link.waitFor();
  await page.locator('#message-input').fill('资源阅读草稿');
  const openMessage=async()=>{await link.click();await page.locator('#files-body h1').filter({hasText:'相对资源预览'}).waitFor();};
  const waitImage=async()=>{try{await page.waitForFunction(()=>[...document.querySelectorAll('#files-body img')].some(img=>img.complete&&img.naturalWidth>0));}catch(error){console.error(JSON.stringify({text:await page.locator('#files-body').textContent(),error:await page.locator('#files-error').textContent(),requests:requests.filter(url=>url.includes('/v1/workspace/files')),errors}));throw error;}};
  const directOpen=async(path)=>{
   await page.locator('#room-menu-label').click();await page.locator('#conversation-open-files').click();
   await page.locator('#files-body .file-entry').first().waitFor();await page.locator('#files-path-edit').click();await page.locator('#files-locate-path').fill(path);await page.locator('#files-locate button[type=submit]').click();
  };
  await openMessage();await waitImage();
  await page.locator('#files-body .workspace-image-load').first().click();await page.locator('dialog.message-image-dialog[open] img').waitFor();
  await page.keyboard.press('Escape');assert.equal(await page.locator('#files-page').isVisible(),true);
  const previewReads=()=>requests.filter(url=>url.includes('/v1/workspace/files/preview?')).length;
  const before=previewReads();await page.locator('#files-body button.workspace-file-link').filter({hasText:'本页锚点'}).click();
  assert.equal(previewReads(),before);assert.ok(await page.locator('#files-body').evaluate(el=>el.scrollTop)>0);
  await page.locator('#files-back').click();await page.locator('#files-body button.workspace-file-link').filter({hasText:'文档导航'}).click();
  await page.locator('#files-body h1').filter({hasText:'使用指南'}).waitFor();await waitImage();
  assert.equal(await page.locator('#files-error').isVisible(),false);
  for(const url of requests.filter(url=>url.includes('/v1/workspace/files')&&!url.includes('/sources'))){
   const q=new URL(url).searchParams;assert.equal(q.get('message_seq'),'1');assert.equal(q.has('membership_id'),false);
  }
  await page.locator('#files-back').click();await page.locator('#files-body h1').filter({hasText:'相对资源预览'}).waitFor();
  // Scroll every placeholder into view. Only the first twenty may load automatically.
  const images=page.locator('#files-body button.workspace-image-load');assert.equal(await images.count(),21);
  for(let i=0;i<21;i++)await images.nth(i).scrollIntoViewIfNeeded();
  await page.waitForFunction(()=>document.querySelectorAll('#files-body img').length===20);
  assert.equal(await images.nth(20).locator('img').count(),0);
  await images.nth(20).click();await page.waitForFunction(()=>document.querySelectorAll('#files-body img').length===21);
  assert.ok(requests.every(url=>!url.includes('invalid.example')));
  assert.equal(await page.locator('#files-body script, #files-body iframe').count(),0);
  assert.equal(await page.evaluate(()=>window.workspaceScriptRan),undefined);
  assert.equal(await page.locator('#files-body button.workspace-file-link').filter({hasText:'根外链接'}).count(),0);
  const downloadPromise=page.waitForEvent('download');await page.locator('#files-download').click();const download=await downloadPromise;
  assert.equal(download.suggestedFilename(),'说明.md');assert.equal(await download.failure(),null);
  await page.locator('#files-close').click();assert.equal(await page.evaluate(()=>workspaceBlobs.size),0);
  // Direct browsing shares resources and type rules, but uses the direct selector.
  await directOpen('images/图 片.png');await waitImage();assert.equal(await page.locator('#files-source-toggle').isVisible(),false);
  await page.locator('#files-close').click();await directOpen('docs/仅下载.html');
  await page.locator('#files-body').filter({hasText:'不支持预览'}).waitFor();assert.equal(await page.locator('#files-body script, #files-body iframe').count(),0);
  const htmlDownload=page.waitForEvent('download');await page.locator('#files-download').click();assert.equal((await htmlDownload).suggestedFilename(),'仅下载.html');
  await page.locator('#files-close').click();
  for(const width of [1000,390]){
   await page.setViewportSize({width,height:850});await openMessage();await waitImage();
   await page.locator('#files-body button.workspace-file-link').filter({hasText:'文档导航'}).click();await page.locator('#files-body h1').filter({hasText:'使用指南'}).waitFor();await waitImage();
   const box=await page.locator('#files-page').boundingBox();assert.ok(box.x>=0&&box.x+box.width<=width+1);
   if(process.env.TEAMX_FILES_SCREENSHOTS&&fixture.name==='direct-false'){
    fs.mkdirSync(process.env.TEAMX_FILES_SCREENSHOTS,{recursive:true});await page.screenshot({path:`${process.env.TEAMX_FILES_SCREENSHOTS}/resources-${width}.png`,animations:'disabled'});
   }
   if(width===390)await page.goBack();else await page.keyboard.press('Escape');
   await page.locator('#files-body h1').filter({hasText:'相对资源预览'}).waitFor();
   if(width===390)await page.goBack();else await page.keyboard.press('Escape');
   assert.equal(await page.locator('#files-page').isVisible(),false);assert.equal(await page.evaluate(()=>workspaceBlobs.size),0);
  }
  // A content response arriving after close must never install a Blob URL.
  await page.setViewportSize({width:1600,height:1000});
  let release;const gate=new Promise(resolve=>release=resolve);let ready;const started=new Promise(resolve=>ready=resolve);
  await page.route('**/v1/workspace/files/content?**',async route=>{const response=await route.fetch();ready();await gate;try{await route.fulfill({response});}catch{}});
  await openMessage();await started;await page.locator('#files-close').click();release();await page.waitForTimeout(150);assert.equal(await page.evaluate(()=>workspaceBlobs.size),0);
  await page.unroute('**/v1/workspace/files/content?**');
  await openMessage();await waitImage();
  const imageURL=requests.filter(url=>url.includes('/v1/workspace/files/content?')&&new URL(url).searchParams.has('message_seq')).at(-1);
  const catalog=await page.request.get(endpoint+'/v1/rooms').then(response=>response.json());
  const workspaceQuery=new URLSearchParams({...fixture.room,service_epoch:catalog.service_epoch,caller_id:catalog.operator});
  const switchDirectory=async external=>{
   const workspace=await page.request.get(endpoint+'/v1/workspace?'+workspaceQuery).then(response=>response.json());
   const target={...workspace.workspace};if(external)target.member_directories={[fixture.member]:fixture.external};else delete target.member_directories;
   const op=await page.request.post(endpoint+'/v1/workspace/apply',{headers:{Origin:endpoint},data:{...fixture.room,service_epoch:catalog.service_epoch,caller:{kind:'human',id:catalog.operator},operation_id:require('node:crypto').randomBytes(16).toString('hex'),expected_version:workspace.workspace_version,target}});
   assert.equal(op.status(),200,await op.text());
   await page.waitForFunction(async query=>{const data=await fetch('/v1/workspace?'+query).then(response=>response.json());return data.operation?.status==='completed';},workspaceQuery.toString());
   await page.waitForFunction(()=>!document.querySelector('#message-input').disabled);
  };
  await switchDirectory(true);
  await page.waitForFunction(()=>workspaceBlobs.size===0);
  if(await page.locator('#files-page').isVisible())await page.locator('#files-close').click();
  await page.reload();await page.waitForFunction(()=>!document.querySelector('#message-input').disabled);
  await page.locator(`[data-room-key*='${fixture.room.room_instance}'] .room-choice`).click();
  await page.locator('#message-input').fill('资源阅读草稿');
  await link.click();await page.locator('#files-error').filter({hasText:'来源已变化'}).waitFor();
  assert.equal((await page.request.get(imageURL)).status(),409);
  await page.locator('#files-close').click();await switchDirectory(false);
  // Directory application replaces the runtime epoch. Reopen the page after
  // the operation settles so this next action uses the new service identity.
  await page.reload();await page.waitForFunction(()=>!document.querySelector('#message-input').disabled);
  await page.locator(`[data-room-key*='${fixture.room.room_instance}'] .room-choice`).click();
  await page.locator('#message-input').fill('资源阅读草稿');await openMessage();await waitImage();
  const currentCatalog=await page.request.get(endpoint+'/v1/rooms').then(response=>response.json());
  const room=currentCatalog.rooms.find(room=>room.room_instance===fixture.room.room_instance);
  const cleared=await page.request.post(endpoint+'/v1/messages/clear',{headers:{Origin:endpoint},data:{...room.service,caller:{kind:'human',id:catalog.operator},snapshot_seq:1}});
  assert.equal(cleared.status(),200,await cleared.text());await page.locator('#files-error').filter({hasText:'原消息已清空'}).waitFor();
  assert.equal(await page.evaluate(()=>workspaceBlobs.size),0);assert.equal(await page.locator('#files-body img').count(),0);
  const old=await page.request.get(imageURL);assert.equal(old.status(),404);assert.equal((await old.json()).code,'reference_unavailable');
  assert.equal(await page.locator('#message-input').inputValue(),'资源阅读草稿');assert.deepEqual(errors,[]);
  console.log(JSON.stringify({case:fixture.name,status:'passed',browser:browser.version(),realHTTP:true,controlledExecution:true,messageFixture:"persisted Agent source",realModel:false,sizes:[1600,1000,390],images:true,documentNavigation:true,anchors:true,automaticImageBudget:20,explicitImage:true,downloads:true,blobCleanup:true,lateContent:true,sourceChange:true,messageClear:true,remoteImageRequests:0}));
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
