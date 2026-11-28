//go:build e2e

package room

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Uses production managers, HTTP/SSE and UI. Only the model component/probe is
// controlled. Never reads or writes the developer's configured Home or accounts.
func TestSpacesBrowserSwitchAndTabs(t *testing.T) {
	if os.Getenv("TEAMX_SPACES_BROWSER") != "1" {
		t.Skip("set TEAMX_SPACES_BROWSER=1 for Chromium acceptance")
	}
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, endpoint, _ := startSpaceServerFixture(t, filepath.Join(project, ".teamx"))
	// Reproduce a legacy Room whose public project contains all Space data.
	if err := s.claims.reserve(s.initial.SpaceID, RoomIdentity{RoomID: "legacy-workspace", RoomInstance: "legacy"}, map[string]string{"member": project}); err != nil {
		t.Fatal(err)
	}
	script := `const {chromium}=require('playwright'),assert=require('node:assert/strict');
(async()=>{const browser=await chromium.launch({headless:true});try{
 const context=await browser.newContext(),page=await context.newPage(),errors=[];
 page.on('pageerror',e=>errors.push(e.message));
 const request=(path,body)=>page.evaluate(async({path,body})=>{
  const response=await fetch(path,{method:body?'POST':'GET',headers:body?{'Content-Type':'application/json'}:{},body:body?JSON.stringify(body):undefined});
  const result=await response.json();assertResponse(response.ok,result);return result;
  function assertResponse(ok,result){if(!ok)throw Error(JSON.stringify(result));}
 },{path,body});
 const directory=()=>request('/v1/spaces');
 const record=async id=>(await directory()).spaces.find(space=>space.id===id);
 const spaceControls=async target=>{
  await target.locator('#space-select').waitFor({state:'visible'});
  assert.equal(await target.locator('.space-picker').count(),1);
  assert.equal(await target.getByRole('button',{name:'空间设置',exact:true}).isVisible(),true);
 };
 const navigate=async id=>{await page.locator('#space-select').selectOption(id);await page.locator('#room-list [data-default-assistant="true"]').waitFor();await spaceControls(page);};
 await page.goto(process.argv[1]);await page.locator('#room-list [data-default-assistant="true"]').waitFor();
 await spaceControls(page);const a=(await directory()).default_space_id;
 await page.locator('#message-input').fill('A private draft');
 await page.locator('#space-select').selectOption('create');
 await page.getByRole('dialog',{name:'新建空间',exact:true}).waitFor();
 assert.equal(await page.locator('#space-create-name').inputValue(),'');
 assert.equal(await page.locator('.space-dialog[open] button:visible').count(),2);
 assert.equal(await page.locator('#space-select').inputValue(),a);
 await page.locator('#space-create-name').fill('Work');
 await page.getByRole('dialog',{name:'新建空间',exact:true}).getByRole('button',{name:'取消',exact:true}).click();
 // Old versions persisted even definite failures. A legacy request must be reconciled
 // separately, without locking an old name into the ordinary creation form.
 const legacy={request_id:'e'.repeat(32),name:'默认空间'};
 await page.evaluate(({a,legacy})=>sessionStorage.setItem('teamx.space-create:'+location.origin+':'+a,JSON.stringify(legacy)),{a,legacy});
 await page.reload();await page.locator('#room-list [data-default-assistant="true"]').waitFor();await spaceControls(page);
 await page.locator('#space-select').selectOption('create');
 const recovery=page.getByRole('dialog',{name:'继续上次创建',exact:true});
 await recovery.waitFor();
 assert.equal(await recovery.locator('input:visible').count(),0);
 assert.match(await recovery.textContent(),/默认空间/);
 // A definite rejection is readable and releases the old request and name.
 let rejectedRequest;
 await page.route('**/v1/spaces',async route=>{
  if(route.request().method()!=='POST')return route.continue();
  rejectedRequest=route.request().postDataJSON();
  await route.fulfill({status:400,contentType:'application/json',body:JSON.stringify({code:'directory_conflict',message:'directory_conflict'})});
 });
 await page.getByRole('button',{name:'核对并继续',exact:true}).click();
 await page.getByRole('dialog',{name:'新建空间',exact:true}).waitFor();
 await page.getByRole('status').filter({hasText:'新空间的数据目录与已有工作目录重叠'}).waitFor();
 assert.deepEqual(rejectedRequest,legacy);
 assert.equal(await page.evaluate(a=>sessionStorage.getItem('teamx.space-create:'+location.origin+':'+a),a),null);
 assert.equal(await page.locator('#space-create-name').isEditable(),true);
 await page.unroute('**/v1/spaces');
 await page.locator('#space-create-name').fill('Work corrected');
 // Losing an accepted response keeps the original request across close/reopen.
 let lostRequest;
 await page.route('**/v1/spaces',async route=>{
  if(route.request().method()!=='POST')return route.continue();
  lostRequest=route.request().postDataJSON();await route.fetch();await route.abort();
 });
 await page.getByRole('button',{name:'创建并进入',exact:true}).click();
 await recovery.waitFor();
 await recovery.getByRole('status').filter({hasText:'暂时无法确认创建结果'}).waitFor();
 assert.equal(await recovery.locator('input:visible').count(),0);
 await recovery.getByRole('button',{name:'取消',exact:true}).click();
 await page.locator('#space-select').selectOption('create');
 await recovery.waitFor();assert.match(await recovery.textContent(),/Work corrected/);
 await page.unroute('**/v1/spaces');
 await page.getByRole('button',{name:'核对并继续',exact:true}).click();
 await page.locator('#model-form').waitFor();await spaceControls(page);
 const b=await page.locator('#space-select').inputValue();assert.notEqual(a,b);
 assert.equal(b,lostRequest.request_id);assert.equal((await record(b)).name,'Work corrected');assert.ok(page.url().includes('/s/'+b+'/'));
 await page.locator('#base-url').fill('https://example.invalid/v1');await page.locator('#model-id').fill('controlled-b');await page.locator('#api-key').fill('synthetic-private-b-key');
 await page.locator('#save-model').click();await page.locator('#room-list [data-default-assistant="true"]').waitFor({timeout:20000});
 await spaceControls(page);assert.equal(await page.locator('#message-input').inputValue(),'');
 await page.locator('#message-input').fill('B private draft');
 const tab=await context.newPage();tab.on('pageerror',e=>errors.push(e.message));
 await tab.goto(process.argv[1]+'/s/'+a+'/');await tab.locator('#room-list [data-default-assistant="true"]').waitFor();
 await spaceControls(tab);assert.ok(tab.url().includes('/s/'+a+'/'));assert.equal(await tab.locator('#message-input').inputValue(),'');await tab.locator('#message-input').fill('A second tab draft');
 assert.ok(page.url().includes('/s/'+b+'/'));
 await navigate(a);
 assert.equal(await page.locator('#message-input').inputValue(),'A private draft');
 await navigate(b);
 assert.equal(await page.locator('#message-input').inputValue(),'B private draft');
 await page.getByRole('button',{name:'空间设置',exact:true}).click();
 const settings=page.getByRole('dialog',{name:'空间设置',exact:true});
 await settings.waitFor();
 assert.equal(await settings.locator('button:visible').count(),2);
 assert.equal(await settings.getByRole('button',{name:'创建并进入',exact:true}).count(),0);
 assert.equal(await settings.locator('#space-lifecycle-action').isVisible(),false);
 for(const width of [1280,390,320]){
  await page.setViewportSize({width,height:800});
  assert.equal(await settings.evaluate(el=>el.scrollWidth<=el.clientWidth),true);
  if(process.env.TEAMX_SPACES_SCREENSHOTS){const fs=require('node:fs');fs.mkdirSync(process.env.TEAMX_SPACES_SCREENSHOTS,{recursive:true});await settings.screenshot({path:process.env.TEAMX_SPACES_SCREENSHOTS+'/settings-'+width+'.png'});}
 }
 await page.setViewportSize({width:1280,height:800});
 await page.locator('#space-name').fill('Renamed Work');
 await settings.getByRole('button',{name:'保存',exact:true}).click();
 await settings.waitFor({state:'hidden'});
 assert.equal((await record(b)).name,'Renamed Work');
 assert.ok(tab.url().includes('/s/'+a+'/'));
 let releaseOld;const held=new Promise(resolve=>releaseOld=resolve);
 const oldRoute='**/s/'+a+'/v1/rooms';
 await page.route(oldRoute,async route=>{const response=await route.fetch();await held;try{await route.fulfill({response})}catch{}});
 const oldRequest=page.waitForRequest(request=>request.url().endsWith('/s/'+a+'/v1/rooms'));
 await page.goto(process.argv[1]+'/s/'+a+'/',{waitUntil:'domcontentloaded'});await oldRequest;
 // This page is still initializing with its Room response deliberately held.
 // Navigate directly to discard it; ordinary switches above use the picker.
 await page.goto(process.argv[1]+'/s/'+b+'/');releaseOld();
 await page.locator('#room-list [data-default-assistant="true"]').waitFor();
 assert.equal(await page.locator('#message-input').inputValue(),'B private draft');
 assert.ok(page.url().includes('/s/'+b+'/'));
 await page.unroute(oldRoute);
 assert.deepEqual(errors,[]);
 // Archiving and restoration keep the picker available on the loading page.
 await page.getByRole('button',{name:'空间设置',exact:true}).click();
 await page.getByText('更多操作',{exact:true}).click();
 await page.locator('#space-lifecycle-action').click();
 await page.getByRole('dialog',{name:'归档空间',exact:true}).waitFor();
 assert.equal(await page.locator('.space-dialog[open] button:visible').count(),2);
 await page.getByRole('button',{name:'取消',exact:true}).click();
 // Cancelling confirmation must not archive or leave the next opening pre-confirmed.
 assert.equal((await record(b)).status,'active');
 await page.getByRole('button',{name:'空间设置',exact:true}).click();
 assert.equal(await page.locator('#space-lifecycle-action').isVisible(),false);
 await page.getByText('更多操作',{exact:true}).click();
 await page.locator('#space-lifecycle-action').click();
 // Rejected writes keep the confirmation open and never navigate away.
 const archiveRoute='**/v1/spaces/'+b+'/archive';
 await page.route(archiveRoute,route=>route.fulfill({status:409,contentType:'application/json',body:JSON.stringify({code:'busy'})}));
 await page.getByRole('button',{name:'确认归档',exact:true}).click();
 await page.getByRole('dialog',{name:'归档空间',exact:true}).getByRole('status').filter({hasText:'空间正在处理其他操作'}).waitFor();
 assert.ok(page.url().includes('/s/'+b+'/'));assert.equal((await record(b)).status,'active');
 await page.unroute(archiveRoute);
 await page.getByRole('button',{name:'确认归档',exact:true}).click();
 await page.waitForURL('**/s/'+a+'/');await page.locator('#room-list [data-default-assistant="true"]').waitFor();
 assert.equal(await page.locator('#space-select').inputValue(),a);
 assert.equal(await page.locator('#message-input').inputValue(),'A private draft');
 // An explicit visit to an archived Space remains available for restoration.
 await page.locator('#space-select').selectOption(b);
 await page.getByRole('heading',{name:'会话暂不可用',exact:true}).waitFor({timeout:20000});await spaceControls(page);
 assert.match(await page.locator('#space-status').textContent(),/空间设置.*恢复.*切换其他空间/);
 assert.ok(tab.url().includes('/s/'+a+'/'));
 assert.equal(await tab.locator('#message-input').inputValue(),'A second tab draft');
 const denied=await context.request.get(process.argv[1]+'/s/'+b+'/v1/rooms');assert.equal(denied.status(),503);
 await page.getByRole('button',{name:'空间设置',exact:true}).click();
 await page.getByText('更多操作',{exact:true}).click();
 await page.locator('#space-lifecycle-action').click();
 await page.getByRole('button',{name:'恢复并进入',exact:true}).click();
 await page.locator('#room-list [data-default-assistant="true"]').waitFor({timeout:20000});
 assert.equal(await page.locator('#message-input').inputValue(),'B private draft');
 await spaceControls(page);assert.deepEqual(errors,[]);
 // Archiving the default chooses the other ready Space. With all Spaces
 // archived, the recovery page must remain usable rather than redirect-loop.
 const archiveCurrent=async()=>{
  await page.getByRole('button',{name:'空间设置',exact:true}).click();
  await page.getByText('更多操作',{exact:true}).click();
  await page.locator('#space-lifecycle-action').click();
  await page.getByRole('button',{name:'确认归档',exact:true}).click();
 };
 await navigate(a);await archiveCurrent();
 await page.waitForURL('**/s/'+b+'/');await page.locator('#room-list [data-default-assistant="true"]').waitFor();
 assert.equal(await page.locator('#space-select').inputValue(),b);
 await archiveCurrent();
 await page.getByRole('heading',{name:'会话暂不可用',exact:true}).waitFor({timeout:20000});await spaceControls(page);
 assert.ok(page.url().includes('/s/'+b+'/'));
 await page.getByRole('button',{name:'空间设置',exact:true}).click();
 await page.getByText('更多操作',{exact:true}).click();await page.locator('#space-lifecycle-action').click();
 await page.getByRole('button',{name:'恢复并进入',exact:true}).click();
 await page.locator('#room-list [data-default-assistant="true"]').waitFor({timeout:20000});
 assert.equal(await page.locator('#message-input').inputValue(),'B private draft');assert.deepEqual(errors,[]);
 console.log(JSON.stringify({spaceUI:'personal-visible',archiveRestore:'passed',archiveReturn:'default-or-ready',lastArchivedSpace:'recoverable',rejectedArchive:'stays',routes:'A-B-A-B',tabs:'independent',drafts:'isolated',setup:'independent',rename:'passed',lateResponse:'isolated',pageErrors:errors}));
 }finally{await browser.close()}})().catch(e=>{console.error(e);process.exit(1)});`
	modules, err := filepath.Abs("../../packaging/verifier/node_modules")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "node", "-e", script, endpoint)
	command.Env = append(os.Environ(), "NODE_PATH="+modules)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Chromium: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
}

func TestPersonalSpaceInitialModelBrowser(t *testing.T) {
	if os.Getenv("TEAMX_SPACES_BROWSER") != "1" {
		t.Skip("set TEAMX_SPACES_BROWSER=1 for Chromium acceptance")
	}
	_, endpoint, _ := startArchivableSpaceServerFixture(t, t.TempDir())
	script := `const {chromium}=require('playwright'),assert=require('node:assert/strict');
 (async()=>{const browser=await chromium.launch({headless:true});try{
  const page=await browser.newPage(),errors=[],manualModelWrites=[];
  page.on('pageerror',e=>errors.push(e.message));
  page.on('request',r=>{if(r.method()==='POST'&&r.url().endsWith('/v1/setup/model'))manualModelWrites.push(r.url());});
  await page.goto(process.argv[1]);await page.locator('#room-list [data-default-assistant="true"]').waitFor();
  const source=await page.locator('#space-select').inputValue();
  await page.locator('#space-select').selectOption('create');
  await page.locator('#space-create-name').fill('Automatic model');
  const creationRequest=page.waitForRequest(r=>r.method()==='POST'&&r.url().endsWith('/v1/spaces'));
  await page.getByRole('button',{name:'创建并进入',exact:true}).click();
  const input=(await creationRequest).postDataJSON();assert.equal(input.model_source_space_id,source);
  await page.waitForURL('**/s/'+input.request_id+'/**');
  await page.locator('#room-list [data-default-assistant="true"]').waitFor({timeout:20000});
  assert.equal(await page.locator('#space-select').inputValue(),input.request_id);
  assert.deepEqual(manualModelWrites,[]);assert.deepEqual(errors,[]);
  console.log(JSON.stringify({automaticModelSetup:'passed',manualModelWrites:0,pageErrors:errors}));
 }finally{await browser.close()}})().catch(e=>{console.error(e);process.exit(1)});`
	modules, err := filepath.Abs("../../packaging/verifier/node_modules")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "node", "-e", script, endpoint)
	command.Env = append(os.Environ(), "NODE_PATH="+modules)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Chromium automatic model setup: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
}
