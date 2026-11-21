const {chromium}=require('playwright');
const assert=require('node:assert/strict');
(async()=>{
 const [url,id,mode,out,roomID,roomInstance]=process.argv.slice(2),errors=[];
 const browser=await chromium.launch({headless:true,channel:process.env.TEAMX_BROWSER_CHANNEL || undefined});
 try{
  const page=await browser.newPage({viewport:{width:1440,height:1000}});
  page.on('pageerror',e=>errors.push(e.message));
  await page.goto(url);
  await page.locator(`#room-list > [data-room-key=${JSON.stringify(JSON.stringify([roomID,roomInstance]))}] > button:first-child`).click();
  await page.locator('#message-input:enabled').waitFor();
  if(await page.locator('#activity-dialog').evaluate(e=>e.open))await page.locator('#close-activity').click();
  const images=page.locator('.room-message-attachments img.message-image');
  await images.first().waitFor();
  assert.ok(await images.count()>=2);
  for(let i=0;i<await images.count();i++){
   await images.nth(i).scrollIntoViewIfNeeded();
   await page.waitForFunction(i=>{const image=document.querySelectorAll('.room-message-attachments img.message-image')[i];return image?.complete && image.naturalWidth>0},i);
  }
  for(const width of [1440,390]){
   await page.setViewportSize({width,height:1000});
   await images.first().scrollIntoViewIfNeeded();
   await page.locator('#messages').screenshot({path:`${out}/room-images-${width}.png`});
  }
  await page.setViewportSize({width:1440,height:1000});
  await page.locator('.member-summary').first().click();await page.locator('#agent-tab-platforms').click();
  const row=page.locator(`#member-notification-platforms [data-platform-id="${id}"]`);
  await row.getByText('图文范围：本人、指定群',{exact:false}).waitFor();
  assert.equal(await row.getByRole('switch',{name:'飞书 使用工具',exact:true}).isChecked(),true);
  await page.locator('#close-activity').click();
  await page.locator('#user-menu-toggle').click();await page.locator('#open-settings').click();await page.locator('#settings-tab-platforms').click();
  await page.locator(`#platforms-list [data-platform-id="${id}"]`).click();
  const upload=page.locator('#connection-capability-list li').filter({hasText:'图片上传权限'});
  const expected=mode==='ready' ? '图片上传权限已就绪' : '图片上传需管理员开通 im:resource';
  await page.waitForFunction(text=>document.getElementById('connection-capability-list').textContent.includes(text),expected);
  await page.locator('#connection-capability-details summary').click();await upload.waitFor();
  assert.match(await upload.textContent(),/发送图文还需成员工具许可和目标权限/);
  if(mode!=='ready'){
   assert.equal(await page.locator('#connection-application-permissions').getAttribute('href'),'https://open.feishu.cn/app/cli_fixture/auth');
   assert.equal(await page.locator('#connection-application-permissions').isVisible(),true);
  }
  for(const width of [1440,390]){
   await page.setViewportSize({width,height:1000});
   assert.equal(await page.locator('#connection-form').evaluate(e=>e.scrollWidth>e.clientWidth+1),false);
   await page.locator('#connection-capabilities').scrollIntoViewIfNeeded();
   await page.locator('#connection-capabilities').screenshot({path:`${out}/image-permission-${width}.png`});
  }
  assert.deepEqual(errors,[]);
  console.log(JSON.stringify({fixture_only:true,mode,production_image_targets:["self","group"],room_images_loaded:true,text_tool_enabled:true,official_repair:mode!=='ready',mobile_overflow:false,page_errors:0}));
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
