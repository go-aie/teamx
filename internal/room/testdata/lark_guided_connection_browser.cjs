// Simulated-platform browser acceptance. Requires playwright and lark-cli 1.0.99.
// Uses the production UI, fixture APIs and locally generated QR images; no real OAuth or application creation.
// node internal/room/testdata/lark_guided_connection_browser.cjs [OUTPUT_DIR]
const http = require('node:http');
const fs = require('node:fs/promises');
const path = require('node:path');
const assert = require('node:assert/strict');
const {execFileSync} = require('node:child_process');
const {chromium} = require('playwright');
const web = path.resolve(__dirname, '../web');
const output = path.resolve(process.argv[2] || '.local/lark-guided-connection-s3-browser');
const profile = {version:1,executable:'/fixture/lark-cli',profile:'teamx-feishu-browser',brand:'feishu',cli_version:'1.0.99'};
const links = {application:'https://open.feishu.cn/page/cli?user_code=fixture&lpv=1.0.99&ocv=1.0.99&from=cli',authorization:'https://accounts.feishu.cn/oauth?code=fixture'};
let profiles=[], connections=[], view, flow=0;
const calls=[], errors=[];
const result = (status, stage, extra={}) => ({session_id:`setup-${flow}`,status,stage,...extra});
const configured = (status, stage, extra={}) => result(status,stage,{configuration_found:true,cli_profile:profile,app_id:'cli_browser',app_name:'浏览器测试应用',...extra});
const waiting = stage => ({verification_url:links[stage],qrcode_url:`/v1/platform/connections/lark/qrcode?session_id=setup-${flow}`});
function api(url, body) {
 if(url.pathname==='/v1/ui') return {managed:true,time_zone:'Asia/Shanghai',settings_version:1,service_epoch:'manager'};
 if(url.pathname==='/v1/rooms') return {service_epoch:'manager',operator:'tester',time_zone:'Asia/Shanghai',settings_version:1,rooms:[],teams:[]};
 if(url.pathname==='/v1/platform/connections') return {connections};
 if(url.pathname==='/v1/platform/connections/lark/discover') return {executable:profile.executable,profiles:profiles.filter(p=>p.brand===body.brand)};
 if(url.pathname==='/v1/platform/connections/lark/setup') {flow++;view=result('waiting','application',waiting('application'));return result('starting','application');}
 if(url.pathname==='/v1/platform/connections/lark/login') {view=configured('waiting','authorization',waiting('authorization'));return result('starting','authorization');}
 if(url.pathname==='/v1/platform/connections/lark/status') return view;
 if(url.pathname==='/v1/platform/connections/lark/cancel') {view={...view,status:'cancelled',verification_url:undefined,qrcode_url:undefined};return view;}
 if(url.pathname==='/v1/platform/connections/save') {const saved={...body,version:1,validation:{status:'unverified'}};connections.push(saved);return saved;}
 if(url.pathname==='/v1/platform/connections/verify') {const saved=connections.find(c=>c.id===body.id);saved.version++;saved.validation={status:'valid',username:'浏览器测试用户'};saved.references=[{name:'个人助手',room_id:'fixture',room_instance:'fixture-instance'}];return saved;}
 throw Error(`Unexpected API ${url.pathname}`);
}
const server=http.createServer(async(req,res)=>{
 try {
  const url=new URL(req.url,'http://localhost');
  if(url.pathname==='/v1/platform/connections/lark/qrcode') {
   assert.equal(view.status,'waiting');
   const png=await fs.readFile(path.join(output,`${view.stage}-qr.png`));
   res.writeHead(200,{'Content-Type':'image/png','Cache-Control':'no-store'});res.end(png);return;
  }
  if(url.pathname.startsWith('/v1/')) {
   let raw='';for await(const c of req)raw+=c;
   const body=raw?JSON.parse(raw):undefined;calls.push({path:url.pathname,body});
   res.writeHead(200,{'Content-Type':'application/json','Cache-Control':'no-store'});res.end(JSON.stringify(api(url,body)));return;
  }
  const name=url.pathname==='/'?'index.html':url.pathname.startsWith('/ui/')?url.pathname.slice(4):'';
  if(!name || name.includes('..')) {res.writeHead(404);res.end();return;}
  const data=await fs.readFile(path.join(web,name));
  res.writeHead(200,{'Content-Type':name.endsWith('.css')?'text/css':/\.m?js$/.test(name)?'text/javascript':'text/html'});res.end(data);
 } catch(e) {errors.push(e.message);res.writeHead(500);res.end('fixture error');}
});
(async()=>{
 await fs.mkdir(output,{recursive:true});
 for(const stage of ['application','authorization']) execFileSync('lark-cli',['auth','qrcode',links[stage],'--output',path.join(output,`${stage}-qr.png`)],{stdio:'pipe'});
 await new Promise(r=>server.listen(0,'127.0.0.1',r));
 const browser=await chromium.launch({headless:true});
 try {
  for(const [label,width,height] of [['desktop',1360,1000],['mobile',390,844]]) {
   profiles=[];connections=[];
   const page=await browser.newPage({viewport:{width,height},isMobile:label==='mobile',hasTouch:label==='mobile'});
   page.setDefaultTimeout(10000);page.on('pageerror',e=>errors.push(e.message));
   // External navigation is a labelled stub, so this check never calls the platform.
   await page.context().route('https://**/*',route=>route.fulfill({contentType:'text/html',body:'<p>模拟官方页面，仅用于链接打开检查</p>'}));
   await page.goto(`http://127.0.0.1:${server.address().port}`);
   await page.locator('#open-settings').click();await page.locator('#settings-tab-connections').click();await page.locator('#connection-new').click();
   await page.locator('#connection-provider').selectOption('feishu');
   await page.locator('#connection-setup-start').waitFor({state:'visible'});
   await page.locator('#connection-name').fill('我的飞书连接');
   assert.equal(await page.locator('#connection-advanced').isVisible(),false);
   assert.equal(await page.locator('#connection-setup-command').isVisible(),false);
   await page.locator('#settings-dialog').screenshot({path:path.join(output,`${label}-initial.png`)});
   await page.locator('#connection-setup-start').click();
   await page.locator('#connection-login-link').filter({hasText:'打开配置页面'}).waitFor();
   assert.equal(await page.locator('#connection-login-link').getAttribute('href'),links.application);
   const popupReady=page.waitForEvent('popup');await page.locator('#connection-login-link').click();const popup=await popupReady;
   await popup.waitForLoadState();assert.equal(popup.url(),links.application);await popup.close();
   await page.waitForFunction(()=>document.querySelector('#connection-login-qr').naturalWidth>0);
   assert.equal(await page.locator('#connection-save').isVisible(),false);
   await page.locator('#connection-login-qr').scrollIntoViewIfNeeded();
   await page.locator('#settings-dialog').screenshot({path:path.join(output,`${label}-application.png`)});
   profiles=[{name:'unrelated',brand:'feishu',user:'其他用户'},{name:profile.profile,brand:'feishu',appId:'cli_browser'}];
   view=configured('waiting','authorization',waiting('authorization'));
   await page.locator('#connection-login-link').filter({hasText:'打开授权页面'}).waitFor();
   await page.waitForFunction(()=>document.querySelector('#connection-login-qr').complete && document.querySelector('#connection-login-qr').naturalWidth>0);
   assert.equal(await page.locator('#connection-application-name').textContent(),'浏览器测试应用');
   await page.locator('#settings-dialog').screenshot({path:path.join(output,`${label}-authorization.png`)});
   view=configured('failed','authorization',{error_code:'permission_denied'});
   await page.locator('#connection-login').filter({hasText:'继续账号授权'}).waitFor();
   assert.match(await page.locator('#connections-feedback').textContent(),/应用已配置.*继续授权/);
   assert.equal(await page.locator('#connection-profile').inputValue(),profile.profile);
   const setups=calls.filter(c=>c.path.endsWith('/lark/setup')).length;
   await page.locator('#connection-login').click();
   await page.locator('#connection-login-panel').waitFor({state:'visible'});
   assert.equal(calls.filter(c=>c.path.endsWith('/lark/setup')).length,setups);
   profiles[1].user='浏览器测试用户';view=configured('succeeded','complete',{username:'浏览器测试用户',renewal_unavailable:true,expires_at:'2027-01-01T00:00:00Z'});
   await page.locator('#connection-save').waitFor({state:'visible'});
   assert.equal(await page.locator('#connection-name').inputValue(),'我的飞书连接');
   assert.equal(await page.locator('#connection-sending').getAttribute('open'),null);
   assert.equal(await page.locator('#connection-account-state').textContent(),'已验证');
   assert.equal(await page.locator('#connection-login').isVisible(),false);
   assert.equal(await page.locator('#connection-reauthorize').isVisible(),false);
   await page.locator('#connection-save').scrollIntoViewIfNeeded();
   assert.equal(await page.locator('#settings-dialog').evaluate(el=>el.scrollWidth>el.clientWidth),false);
   assert.equal(await page.locator('#platform-settings').evaluate(el=>el.scrollWidth>el.clientWidth),false);
   await page.locator('#settings-dialog').screenshot({path:path.join(output,`${label}-verified.png`)});
   assert.match(await page.locator('#connections-feedback').textContent(),/无法自动续期/);
   assert.equal(await page.locator('#connections-feedback-details').getAttribute('open'),null);
   await page.locator('#connections-feedback-details > summary').click();
   assert.match(await page.locator('#connections-feedback-detail').textContent(),/刷新 user_access_token/);
   await page.locator('#connections-feedback-detail').scrollIntoViewIfNeeded();
   await page.locator('#settings-dialog').screenshot({path:path.join(output,`${label}-renewal.png`)});
   await page.locator('#connection-save').click();await page.locator('#connections-feedback').filter({hasText:'连接已保存，账号验证成功'}).waitFor();
   assert.equal(connections[0].cli_profile.profile,profile.profile);
   assert.equal(await page.locator('#connections-feedback-details').isVisible(),false);
   await page.locator('#connections-list .connection-row').first().click();
   await page.locator('#connection-account-state').filter({hasText:'已验证'}).waitFor();
   assert.equal(await page.locator('#connection-account').textContent(),'浏览器测试用户');
   assert.equal(await page.locator('#connection-notes').isVisible(),false);
   assert.equal(await page.locator('#connection-profile').isVisible(),false);
   assert.equal(await page.locator('#connection-advanced').isVisible(),false);
   await page.locator('#connection-application').scrollIntoViewIfNeeded();
   await page.locator('#settings-dialog').screenshot({path:path.join(output,`${label}-connected.png`)});
   await page.locator('#connection-account-manage > summary').click();
   await page.locator('#connection-reauthorize').waitFor({state:'visible'});
   await page.locator('#settings-dialog').screenshot({path:path.join(output,`${label}-manage.png`)});
   await page.locator('#connection-account-manage > summary').press('Escape');
   assert.equal(await page.locator('#connection-reauthorize').isVisible(),false);
   assert.equal(await page.locator('#settings-dialog').isVisible(),true);
   await page.locator('#connection-account-manage > summary').click();
   await page.locator('#connection-name').click();assert.equal(await page.locator('#connection-reauthorize').isVisible(),false);
   await page.locator('#connection-account-manage > summary').click();
   const logins=calls.filter(c=>c.path.endsWith('/lark/login')).length;
   await page.locator('#connection-reauthorize').click();
   await page.locator('#connection-auth-confirmation').waitFor({state:'visible'});
   assert.equal(calls.filter(c=>c.path.endsWith('/lark/login')).length,logins);
   await page.locator('#connection-auth-dismiss').click();
   await page.locator('#connection-application-change').click();
   await page.locator('#connection-profile').waitFor({state:'visible'});
   assert.equal(await page.locator('#connection-setup-other').isVisible(),true);
   await page.locator('#settings-dialog').screenshot({path:path.join(output,`${label}-application-picker.png`)});
   await page.locator('#connection-profile').press('Escape');
   assert.equal(await page.locator('#connection-profile').isVisible(),false);
   await page.locator('#connection-back').click();
   // Cancel and reopen with a configuration written before cancellation.
   await page.locator('#connection-new').click();await page.locator('#connection-provider').selectOption('feishu');
   await page.locator('#connection-application-change').click();await page.locator('#connection-setup-other').click();await page.locator('#connection-login-panel').waitFor({state:'visible'});
   await page.locator('#connection-login-cancel').click();await page.locator('#connection-recheck').waitFor({state:'visible'});
   assert.equal(await page.locator('#connection-login-panel').isVisible(),false);
   await page.locator('#close-settings').click();
   await page.locator('#open-settings').click();await page.locator('#connection-new').click();await page.locator('#connection-provider').selectOption('feishu');
   await page.locator('#connection-application-change').click();
   await page.locator('#connection-profile-choice').waitFor({state:'visible'});
   assert.equal(await page.locator('#connection-profile').inputValue(),'');
   assert.equal(await page.locator('#connection-save').isVisible(),false);
   assert.equal(await page.locator('#connection-advanced').isVisible(),false);
   await page.locator('#connection-provider').selectOption('lark');
   assert.equal(await page.locator('#connection-setup-start').isVisible(),false);
   assert.match(await page.locator('#connection-guided-hint').textContent(),/尚未验证/);
   await page.locator('#connection-advanced > summary').click();
   await page.locator('#connection-setup > summary').click();
   assert.equal(await page.locator('#connection-setup-command').isVisible(),true);
   await page.locator('#settings-dialog').screenshot({path:path.join(output,`${label}-fallback.png`)});
   await page.close();
  }
  assert.deepEqual(errors,[]);
  assert.equal(calls.some(c=>/notifications\/configure|authorize-sending|authorize-targets/.test(c.path)),false);
  await fs.writeFile(path.join(output,'result.json'),JSON.stringify({result:'passed',viewports:['1360x1000','390x844'],setupRequests:calls.filter(c=>c.path.endsWith('/lark/setup')).length,saveRequests:calls.filter(c=>c.path.endsWith('/connections/save')).length,errors,platform:'fixture; no real OAuth or creation'},null,2));
  console.log(`Browser acceptance passed: ${output}`);
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;}).finally(()=>server.close());
