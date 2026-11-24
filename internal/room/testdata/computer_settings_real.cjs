const {chromium}=require('playwright');
const assert=require('node:assert/strict'),path=require('node:path');
(async()=>{
 const endpoint=process.argv[2],enabled=process.argv[3]==='true';
 const browser=await chromium.launch({headless:true});
 try{
  const page=await browser.newPage({viewport:{width:1100,height:900}});
  page.setDefaultTimeout(30000);const writes=[];
  page.on('request',r=>{if(new URL(r.url()).pathname==='/v1/computer/save')writes.push(r.postDataJSON())});
  await page.goto(endpoint);await page.locator('#user-menu-toggle').click();await page.locator('#open-settings').click();await page.locator('#settings-tab-computer').click();
  await page.waitForFunction(()=>!document.querySelector('#computer-runtime-mode').disabled);
  await page.locator('#computer-runtime-mode').selectOption('docker');
  await page.locator('#computer-enabled').setChecked(enabled);
  const saved=page.waitForResponse(r=>new URL(r.url()).pathname==='/v1/computer/save' && r.request().method()==='POST');
  await page.locator('#computer-save').click();const receipt=await saved;assert.equal(receipt.status(),202,await receipt.text());
  await page.waitForFunction(value=>{
   const status=document.querySelector('#computer-status').textContent;
   return document.querySelector('#computer-enabled').checked===value && !document.querySelector('#computer-save').disabled && document.querySelector('#computer-progress').textContent.startsWith('已完成') && status.includes(value?'连接已就绪':'已停用');
  },enabled,{timeout:240000});
  assert.equal(writes.length,1);assert.equal(writes[0].runtime_mode,'docker');assert.equal(writes[0].enabled,enabled);assert.equal(writes[0].endpoint,'');assert.equal(writes[0].credential,'');
  if(process.env.TEAMX_COMPUTER_VIEW_EVIDENCE)await page.screenshot({path:path.join(process.env.TEAMX_COMPUTER_VIEW_EVIDENCE,`settings-${enabled?'enabled':'disabled'}.png`)});
  console.log(JSON.stringify({real_settings_ui:true,enabled,applied:true,writes:1}));
 }finally{await browser.close()}
})().catch(e=>{console.error(e.stack);process.exit(1)});
