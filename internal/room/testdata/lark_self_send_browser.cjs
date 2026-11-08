// Optional real-browser check: node internal/room/testdata/lark_self_send_browser.cjs OUTPUT_DIR
// Serves the production UI with an isolated API fixture; no platform calls.
const http = require('node:http');
const fs = require('node:fs/promises');
const path = require('node:path');
const assert = require('node:assert/strict');
const {chromium} = require('playwright');
const web = path.resolve(__dirname, '../web');
const output = path.resolve(process.argv[2] || '.local/lark-self-send-s3-browser');
const members = ['a', 'b'].map(id => ({agent_id:`agent-${id}`,membership_id:`member-${id}`,status:'idle',runner:{profile:'p'},unread_count:0,read_through:0}));
const account = id => ({provider:'lark',instance:'https://open.feishu.cn',tenant:{namespace:'lark:tenant_key',value:'fixture'},id:{namespace:'lark:open_id:fixture',value:`ou_${id}_abcdefghijklmnopqrstuvwxyz0123456789`}});
let connections = ['one','two'].map(id => ({id,name:`飞书测试账号 ${id}`,provider:'lark',url:'https://open.feishu.cn',version:1,validation:{status:'valid',username:id,account:account(id)}}));
const data = {revision:0,bindings:[],catalog:[{name:'lark',provider:'lark',version:'1.0.99',actions:[{operation:'message.reply',target_kind:'message',object_types:['message']},{operation:'message.send',target_kind:'object',object_types:['chat']},{binding_evidence:'self_delivery',operation:'message.send_self',target_kind:'object',object_types:['user']}]}]};
const calls = [], errors = [];
let rejectBinding = false, loseBindingResponse = false;
function api(url, body) {
  if(url.pathname==='/v1/ui') return {managed:true};
  if(url.pathname==='/v1/rooms') return {service_epoch:'manager',operator:'tester',rooms:[{room_id:'A',room_instance:'instance-A',name:'本人投递验收',status:'ready',runtime_status:'ready',service:{room_id:'A',room_instance:'instance-A',service_epoch:'active-A'}}],teams:[]};
  if(url.pathname==='/v1/workspace') return {workspace:{root:'workspaces'},workspace_version:1,members:[],credential_status:'missing'};
  if(url.pathname==='/v1/platform/notifications') return {subscriptions:[]};
  if(url.pathname==='/v1/platform/connections') return {connections};
  if(url.pathname==='/v1/tools/bindings') return data;
  if(url.pathname==='/v1/tools/bind') {
    if(rejectBinding) return {code:'conflict',error:'本人会话待核验'};
    const b = body.binding;
    assert.equal(b.capability_evidence,undefined);
    data.bindings = data.bindings.filter(old => old.membership_id!==b.membership_id || old.tool!==b.tool);
    if(body.enabled) {
      const copy = structuredClone(b);
      if(copy.action_authorization?.grants.some(g=>g.operation==='message.send_self')) copy.capability_evidence=[{kind:'self_delivery',account:account(copy.connection_id),target:{containers:[{id:{value:'oc_self_abcdefghijklmnopqrstuvwxyz0123456789'}}]}}];
      data.bindings.push(copy);
    }
    data.revision++;
    return data;
  }
  if(url.pathname==='/v1/member/list') return {members,latest_seq:0};
  if(url.pathname==='/v1/member/activity') return {member:members.find(m=>m.membership_id===body.membership_id)};
  if(url.pathname==='/v1/read') return {messages:[],snapshot_seq:0,has_more:false};
  throw Error(`unexpected API ${url.pathname}`);
}
const server = http.createServer(async(req,res)=>{
  try {
    const url = new URL(req.url,'http://localhost');
    if(url.pathname.startsWith('/v1/')) {
      let raw=''; for await(const c of req) raw+=c;
      const body=raw?JSON.parse(raw):undefined;
      calls.push({path:url.pathname,body});
      const result=api(url,body);
      if(url.pathname==='/v1/tools/bind' && loseBindingResponse) {res.destroy();return;}
      res.writeHead(result.code?409:200,{'Content-Type':'application/json'});res.end(JSON.stringify(result));return;
    }
    const name=url.pathname==='/'?'index.html':url.pathname.startsWith('/ui/')?url.pathname.slice(4):'';
    if(!name || name.includes('..')) {res.writeHead(404);res.end();return;}
    const content=await fs.readFile(path.join(web,name));
    res.writeHead(200,{'Content-Type':name.endsWith('.css')?'text/css':/\.m?js$/.test(name)?'text/javascript':'text/html'});res.end(content);
  } catch(e) {errors.push(e.message);res.writeHead(500);res.end('fixture failed');}
});
(async()=>{
  await fs.mkdir(output,{recursive:true});
  await new Promise(r=>server.listen(0,'127.0.0.1',r));
  const browser=await chromium.launch({headless:true});
  try {
    const page=await browser.newPage({viewport:{width:1360,height:1000}});
    page.on('pageerror',e=>errors.push(e.message));
    await page.goto(`http://127.0.0.1:${server.address().port}`);
    await page.locator('#members .member-summary').first().click();
    await page.locator('#agent-tab-tools').click();
    const select=page.getByLabel('lark 连接账号与实例');
    const checkbox=page.getByRole('checkbox',{name:'lark 发送到本人自聊'});
    const apply=page.locator('.cli-tool-row button');
    await checkbox.waitFor();
    assert.equal(await checkbox.isDisabled(),true);
    await page.getByText('请先选择已验证的飞书用户连接', {exact:false}).waitFor();
    await select.selectOption('one');
    assert.equal(await checkbox.isChecked(),false);
    await apply.click();
    await page.getByText('已应用', {exact:false}).waitFor();
    assert.equal(data.bindings[0].action_authorization.grants.some(g=>g.operation==='message.send_self'),false);
    await checkbox.check();await apply.click();
    await page.getByText('本人目标：', {exact:false}).waitFor();
    assert.equal(await checkbox.isChecked(),true);
    await page.locator('#tools-member-view').screenshot({path:path.join(output,'desktop.png')});
    await page.setViewportSize({width:390,height:900});
    const panel=page.locator('#tools-member-view');
    assert.equal(await panel.evaluate(el=>el.scrollWidth>el.clientWidth),false);
    const geometry=await checkbox.evaluate(el=>({checkbox:el.getBoundingClientRect().toJSON(),label:el.closest('label').getBoundingClientRect().toJSON(),text:el.nextElementSibling.getBoundingClientRect().toJSON()}));
    assert.ok(geometry.text.x>geometry.checkbox.x && geometry.text.y<geometry.checkbox.bottom);
    await panel.screenshot({path:path.join(output,'mobile.png')});
    await select.selectOption('two');
    assert.equal(await checkbox.isChecked(),false);
    assert.doesNotMatch(await panel.textContent(),/oc_self/);
    rejectBinding=true;await checkbox.check();await apply.click();
    await page.locator('#cli-member-feedback').filter({hasText:'应用失败'}).waitFor();
    rejectBinding=false;await page.locator('#cli-member-refresh').click();
    assert.equal(await select.inputValue(),'one');
    loseBindingResponse=true;await checkbox.uncheck();await apply.click();
    await page.locator('#cli-member-feedback').filter({hasText:'不会自动重发'}).waitFor();
    const count=calls.filter(c=>c.path==='/v1/tools/bind').length;
    loseBindingResponse=false;await page.locator('#cli-member-refresh').click();
    await checkbox.waitFor();assert.equal(await checkbox.isChecked(),false);
    assert.equal(calls.filter(c=>c.path==='/v1/tools/bind').length,count);
    // An invalidated connection cannot display an old target as current.
    connections=connections.map(c=>({...c,validation:{status:'unverified'}}));
    data.bindings[0].capability_evidence=undefined;
    await page.locator('#cli-member-refresh').click();
    await page.getByText('请先选择已验证的飞书用户连接',{exact:false}).waitFor();
    assert.equal(await checkbox.isDisabled(),true);
    assert.doesNotMatch(await panel.textContent(),/oc_self/);
    await panel.screenshot({path:path.join(output,'unavailable.png')});
    assert.deepEqual(errors,[]);
    await fs.writeFile(path.join(output,'report.json'),JSON.stringify({status:'passed',surface:'production HTML CSS JS, real Chromium, isolated API fixture',platform_writes:0,narrow_overflow:false,checkbox_geometry:geometry,page_errors:errors,binding_posts:count},null,2));
    console.log(`Browser checks passed: ${output}`);
  } finally {await browser.close();server.close();}
})().catch(e=>{console.error(e);server.close();process.exitCode=1;});
