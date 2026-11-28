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
  await target.locator('#space-trigger').waitFor({state:'visible'});
  assert.equal(await target.locator('.space-picker').count(),1);
  assert.equal(await target.locator('#space-settings').isVisible(),false);
 };
 const chooseSpace=async id=>{await page.locator('#space-trigger').click();await page.locator('#space-menu [data-space-id="'+id+'"]').click();};
 const createSpace=async()=>{await page.locator('#space-trigger').click();await page.locator('#space-create').click();};
 const management=page.getByRole('dialog',{name:'管理空间',exact:true});
 const row=id=>management.locator('[data-space-id="'+id+'"]');
 const settledManager=async()=>{await management.waitFor();await page.waitForFunction(()=>!document.getElementById('space-management-refresh').disabled);};
 const openManagement=async()=>{if(!await management.isVisible()){if(!await page.locator('#space-menu').evaluate(el=>el.matches(':popover-open')))await page.locator('#space-trigger').click();await page.locator('#space-settings').click();}await settledManager();};
 const closeManagement=async()=>{await management.getByRole('button',{name:'关闭',exact:true}).click();await management.waitFor({state:'hidden'});};
 const openMore=async id=>{await row(id).locator('[data-action="more"]').click();};
 const openRename=async id=>{await openManagement();await openMore(id);await row(id).locator('[data-action="rename"]').click();await page.getByRole('dialog',{name:'修改空间名称',exact:true}).waitFor();};
 const openArchive=async id=>{await openManagement();await openMore(id);await row(id).locator('[data-action="archive"]').click();await page.getByRole('dialog',{name:'归档空间',exact:true}).waitFor();};
 const navigate=async id=>{await chooseSpace(id);await page.waitForURL('**/s/'+id+'/');await page.locator('#room-list [data-default-assistant="true"]').waitFor();await spaceControls(page);};
 await page.goto(process.argv[1]);await page.locator('#room-list [data-default-assistant="true"]').waitFor();
 await spaceControls(page);const a=(await directory()).default_space_id;
 assert.equal(await page.locator('.space-trigger-name').textContent(),'TeamX');
 // Space navigation shares the brand's single header row and supports keyboard
 // navigation, dismissal, narrow screens and the collapsed desktop rail.
 const viewport=async width=>{
  await page.setViewportSize({width,height:800});
  // Let responsive rail visibility and popover geometry observe the resize.
  await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
 };
 const checkHeader=async()=>{
  await page.locator('#space-trigger').waitFor({state:'visible'});
  const header=await page.locator('.room-rail-header').boundingBox(),trigger=await page.locator('#space-trigger').boundingBox();
  assert.ok(trigger.y>=header.y&&trigger.y+trigger.height<=header.y+header.height);
  assert.equal(await page.locator('.room-rail-header').evaluate(el=>el.scrollWidth<=el.clientWidth),true);
 };
 const screenshot=async name=>{if(process.env.TEAMX_SPACES_SCREENSHOTS){const fs=require('node:fs');fs.mkdirSync(process.env.TEAMX_SPACES_SCREENSHOTS,{recursive:true});await page.screenshot({animations:'disabled',path:process.env.TEAMX_SPACES_SCREENSHOTS+'/'+name+'.png'});}};
 await checkHeader();await screenshot('header-desktop');
 await page.locator('#space-trigger').focus();await page.keyboard.press('ArrowDown');
 assert.equal(await page.locator('#space-menu [aria-checked="true"]').evaluate(el=>el===document.activeElement),true);
 await page.keyboard.press('End');assert.equal(await page.evaluate(()=>document.activeElement.id),'space-create');
 await page.keyboard.press('Home');assert.equal(await page.locator('#space-menu [aria-checked="true"]').evaluate(el=>el===document.activeElement),true);
 await page.keyboard.press('Escape');assert.equal(await page.locator('#space-trigger').getAttribute('aria-expanded'),'false');
 assert.equal(await page.evaluate(()=>document.activeElement.id),'space-trigger');
 await page.locator('#space-trigger').click();await page.locator('#space-menu').waitFor({state:'visible'});
 assert.equal(await page.locator('#space-settings svg[aria-hidden="true"]').count(),1);
 assert.equal(await page.locator('#space-create svg[aria-hidden="true"]').count(),1);
 assert.equal(await page.locator('#space-create').textContent(),'新建空间');
 assert.equal(await page.locator('#space-settings').textContent(),'管理空间');
 assert.equal(await page.locator('#space-menu [data-space-id="'+a+'"] .space-badge').textContent(),'默认');
 await page.locator('#space-trigger').click();await page.locator('#space-menu').waitFor({state:'hidden'});
 await page.locator('#space-trigger').click();await page.locator('#message-input').click();
 await page.waitForFunction(()=>document.getElementById('space-trigger').getAttribute('aria-expanded')==='false');
 await page.locator('#toggle-rooms').click();await page.locator('#space-trigger').waitFor({state:'hidden'});
 await page.locator('#toggle-rooms').click();await spaceControls(page);
 for(const width of [390,320]){
  await viewport(width);
  if(!await page.locator('#space-trigger').isVisible())await page.locator('#mobile-back-to-rooms').click();
  await checkHeader();await page.locator('#space-trigger').click();
  const menu=await page.locator('#space-menu').boundingBox();assert.ok(menu.x>=0&&menu.x+menu.width<=width);
  await screenshot('menu-'+width);await page.keyboard.press('Escape');
 }
 await viewport(1280);
 await page.goto(process.argv[1]+'/s/'+a+'/');await page.locator('#room-list [data-default-assistant="true"]').waitFor();await spaceControls(page);
 await page.locator('#message-input').fill('A private draft');
 // Creation shares close, Escape and guarded backdrop dismissal with settings.
 for(const method of ['cross','escape','backdrop']){
  await createSpace();const surface=page.getByRole('dialog',{name:'新建空间',exact:true});await surface.waitFor();
  assert.equal(await page.evaluate(()=>document.activeElement.id),'space-create-name');
  if(method==='cross')await surface.getByRole('button',{name:'关闭',exact:true}).click();
  if(method==='escape')await page.keyboard.press('Escape');
  if(method==='backdrop'){
   const rect=await surface.boundingBox();
   await page.mouse.move(rect.x+rect.width/2,rect.y+12);await page.mouse.down();await page.mouse.move(2,2);await page.mouse.up();
   assert.equal(await surface.isVisible(),true);
   await page.mouse.click(2,2);
  }
  await surface.waitFor({state:'hidden'});
 }
 await createSpace();
 await page.getByRole('dialog',{name:'新建空间',exact:true}).waitFor();
 const creation=page.getByRole('dialog',{name:'新建空间',exact:true});
 assert.equal(await creation.locator('.space-hint:not(.space-note)').textContent(),'优先沿用当前空间的默认模型，平台连接需单独配置。');
 assert.equal(await creation.locator('.space-note').textContent(),'飞书仅默认空间可用。');
 for(const width of [1280,390,320]){
  await viewport(width);
  assert.equal(await creation.evaluate(el=>el.scrollWidth<=el.clientWidth),true);
  await screenshot('create-'+width);
 }
 await viewport(1280);
 assert.equal(await page.locator('#space-create-name').inputValue(),'');
 assert.equal(await page.locator('.space-dialog[open] .space-actions button:visible').count(),2);
 assert.equal(await page.locator('#space-trigger').getAttribute('data-space-id'),a);
 await page.locator('#space-create-name').fill('Work');
 await page.getByRole('dialog',{name:'新建空间',exact:true}).getByRole('button',{name:'取消',exact:true}).click();
 // Old versions persisted even definite failures. A legacy request must be reconciled
 // separately, without locking an old name into the ordinary creation form.
 const legacy={request_id:'e'.repeat(32),name:'默认空间'};
 await page.evaluate(({a,legacy})=>sessionStorage.setItem('teamx.space-create:'+location.origin+':'+a,JSON.stringify(legacy)),{a,legacy});
 await page.reload();await page.locator('#room-list [data-default-assistant="true"]').waitFor();await spaceControls(page);
 await createSpace();
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
 let lostRequest,releaseCreation;const heldCreation=new Promise(resolve=>releaseCreation=resolve);
 await page.route('**/v1/spaces',async route=>{
  if(route.request().method()!=='POST')return route.continue();
  lostRequest=route.request().postDataJSON();await heldCreation;await route.fetch();await route.abort();
 });
 await page.getByRole('button',{name:'创建并进入',exact:true}).click();
 await page.waitForFunction(()=>document.querySelector('[aria-labelledby="space-create-name-title"] [data-dialog-close]').disabled);
 await page.keyboard.press('Escape');await page.mouse.click(2,2);
 assert.equal(await creation.isVisible(),true);
 releaseCreation();
 await recovery.waitFor();
 await recovery.getByRole('status').filter({hasText:'暂时无法确认创建结果'}).waitFor();
 assert.equal(await recovery.locator('input:visible').count(),0);
 await recovery.getByRole('button',{name:'取消',exact:true}).click();
 await createSpace();
 await recovery.waitFor();assert.match(await recovery.textContent(),/Work corrected/);
 await page.unroute('**/v1/spaces');
 await page.getByRole('button',{name:'核对并继续',exact:true}).click();
 await page.locator('#model-form').waitFor();await spaceControls(page);
 const b=await page.locator('#space-trigger').getAttribute('data-space-id');assert.notEqual(a,b);
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
 // Management keeps maintenance out of the switcher and identifies the
 // actual default independently of its editable name.
 await openManagement();
 assert.equal(await management.locator('.space-actions').count(),0);
 await page.mouse.click(2,2);await management.waitFor({state:'hidden'});await openManagement();
 await page.keyboard.press('Escape');await management.waitFor({state:'hidden'});await openManagement();
 await management.getByRole('tab',{name:/使用中/}).focus();await page.keyboard.press('ArrowRight');
 assert.equal(await management.getByRole('tab',{name:/已归档/}).getAttribute('aria-selected'),'true');
 await page.keyboard.press('ArrowLeft');
 assert.equal(await row(a).locator('.space-badge').textContent(),'默认');
 for(const width of [1280,390,320]){
  await viewport(width);
  assert.equal(await management.evaluate(el=>el.scrollWidth<=el.clientWidth),true);
  await screenshot('management-'+width);
 }
 await viewport(1280);
 await row(a).locator('[data-action="more"]').focus();await page.keyboard.press('ArrowDown');
 assert.equal(await row(a).locator('[data-action="rename"]').evaluate(el=>el===document.activeElement),true);
 await page.keyboard.press('Escape');assert.equal(await management.isVisible(),true);
 await openMore(a);await row(a).locator('[data-action="rename"]').click();
 const settings=page.getByRole('dialog',{name:'修改空间名称',exact:true});
 assert.equal(await page.evaluate(()=>document.activeElement.id),'space-name');
 await page.locator('#space-name').fill('Home renamed');await settings.getByRole('button',{name:'保存',exact:true}).click();await settledManager();
 assert.equal((await record(a)).name,'Home renamed');
 assert.equal(await row(a).locator('.space-badge').textContent(),'默认');
 assert.ok(page.url().includes('/s/'+b+'/'));
 await closeManagement();
 await page.locator('#space-trigger').click();
 assert.equal(await page.locator('#space-menu [data-space-id="'+a+'"] .space-badge').textContent(),'默认');
 await page.keyboard.press('Escape');
 await page.locator('#space-menu').waitFor({state:'hidden'});
 await openRename(b);
 assert.equal(await settings.locator('.space-actions button:visible').count(),2);
 await page.locator('#space-name').fill('Renamed Work');
 await settings.getByRole('button',{name:'保存',exact:true}).click();await settledManager();await closeManagement();
 assert.equal((await record(b)).name,'Renamed Work');
 assert.equal(await page.locator('.space-trigger-name').textContent(),'Renamed Work');
 await page.locator('#space-trigger').click();
 assert.equal(await page.locator('#space-menu [aria-checked="true"] .space-menu-name').textContent(),'Renamed Work');
 await screenshot('menu-desktop');await page.keyboard.press('Escape');
 const longName='这是一个很长的空间名称用于确认侧栏标题保持单行并且菜单仍能显示完整名称';
 await openRename(b);await page.locator('#space-name').fill(longName);await settings.getByRole('button',{name:'保存',exact:true}).click();await settledManager();await closeManagement();
 for(const width of [1280,320]){
  await viewport(width);
  if(!await page.locator('#space-trigger').isVisible())await page.locator('#mobile-back-to-rooms').click();
  await checkHeader();await page.locator('#space-trigger').click();
  assert.equal(await page.locator('#space-menu [aria-checked="true"] .space-menu-name').textContent(),longName);
  assert.equal(await page.locator('#space-menu').evaluate(el=>el.scrollWidth<=el.clientWidth),true);
  await screenshot('long-name-'+width);await page.keyboard.press('Escape');
  await openManagement();assert.equal(await management.evaluate(el=>el.scrollWidth<=el.clientWidth),true);
  await screenshot('management-long-'+width);await closeManagement();
 }
 await viewport(1280);
 await openRename(b);await page.locator('#space-name').fill('Renamed Work');await settings.getByRole('button',{name:'保存',exact:true}).click();await settledManager();await closeManagement();
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
 // Cancelled and rejected confirmations never archive a Space.
 await openArchive(b);
 assert.equal(await page.locator('.space-dialog[open] .space-actions button:visible').count(),2);
 await page.mouse.click(2,2);await settledManager();
 assert.equal((await record(b)).status,'active');
 await openMore(b);await row(b).locator('[data-action="archive"]').click();
 const archiveRoute='**/v1/spaces/'+b+'/archive';
 let releaseArchive;const heldArchive=new Promise(resolve=>releaseArchive=resolve);
 await page.route(archiveRoute,async route=>{await heldArchive;await route.fulfill({status:409,contentType:'application/json',body:JSON.stringify({code:'busy'})});});
 await page.getByRole('button',{name:'确认归档',exact:true}).click();
 const archiveDialog=page.getByRole('dialog',{name:'归档空间',exact:true});
 assert.equal(await archiveDialog.getByRole('button',{name:'关闭',exact:true}).isDisabled(),true);
 await page.keyboard.press('Escape');await page.mouse.click(2,2);assert.equal(await archiveDialog.isVisible(),true);
 releaseArchive();
 await page.getByRole('dialog',{name:'归档空间',exact:true}).getByRole('status').filter({hasText:'空间正在处理其他操作'}).waitFor();
 assert.ok(page.url().includes('/s/'+b+'/'));assert.equal((await record(b)).status,'active');
 await page.unroute(archiveRoute);
 await page.getByRole('button',{name:'确认归档',exact:true}).click();
 await page.waitForURL('**/s/'+a+'/');await page.locator('#room-list [data-default-assistant="true"]').waitFor();
 assert.equal(await page.locator('#message-input').inputValue(),'A private draft');
 await page.locator('#space-trigger').click();
 assert.equal(await page.locator('#space-menu [data-space-id="'+b+'"]').count(),0);
 await page.keyboard.press('Escape');
 await openManagement();await management.getByRole('tab',{name:/已归档/}).click();
 await row(b).locator('[data-action="restore"]').waitFor();
 assert.equal(await row(b).locator('[data-action="enter"]').count(),0);
 await screenshot('management-archived');
 // Restore from a different Space, without navigating or contacting the
 // archived Space's conversation APIs. Entry remains an explicit action.
 const beforeRestore=page.url();
 let archivedRoomReads=0;const watch=request=>{if(request.url().includes('/s/'+b+'/v1/rooms'))archivedRoomReads++;};page.on('request',watch);
 await row(b).locator('[data-action="restore"]').click();
 await page.getByRole('button',{name:'取消',exact:true}).click();await settledManager();
 assert.equal((await record(b)).status,'suspended');assert.equal(page.url(),beforeRestore);
 await row(b).locator('[data-action="restore"]').click();
 const restoreRoute='**/v1/spaces/'+b+'/restore';
 await page.route(restoreRoute,route=>route.fulfill({status:409,contentType:'application/json',body:JSON.stringify({code:'busy'})}));
 await page.getByRole('button',{name:'确认恢复',exact:true}).click();
 await page.getByRole('dialog',{name:'恢复空间',exact:true}).getByRole('status').filter({hasText:'空间正在处理其他操作'}).waitFor();
 assert.equal((await record(b)).status,'suspended');assert.equal(page.url(),beforeRestore);
 await page.unroute(restoreRoute);
 await page.getByRole('button',{name:'确认恢复',exact:true}).click();await settledManager();
 assert.equal(page.url(),beforeRestore);assert.equal(archivedRoomReads,0);page.off('request',watch);
 assert.equal((await record(b)).status,'active');
 await row(b).locator('[data-action="enter"]').click();
 await page.waitForURL('**/s/'+b+'/');await page.locator('#room-list [data-default-assistant="true"]').waitFor({timeout:20000});
 assert.equal(await page.locator('#message-input').inputValue(),'B private draft');
 assert.ok(tab.url().includes('/s/'+a+'/'));assert.equal(await tab.locator('#message-input').inputValue(),'A second tab draft');
 // Non-current maintenance keeps the current document and its draft intact.
 await openArchive(a);await page.getByRole('button',{name:'确认归档',exact:true}).click();await settledManager();
 for(let attempt=0;attempt<30&&await row(a).locator('[data-action="restore"]').count()===0;attempt++){
  await page.waitForTimeout(100);if(await management.locator('#space-management-refresh').isEnabled())await management.locator('#space-management-refresh').click();await settledManager();
 }
 assert.equal((await record(a)).status,'suspended');assert.ok(page.url().includes('/s/'+b+'/'));
 assert.equal(await page.locator('#message-input').inputValue(),'B private draft');
 await closeManagement();
 // With every Space archived, the loading document retains global management
 // and an empty switcher still focuses a usable action.
 await openArchive(b);await page.getByRole('button',{name:'确认归档',exact:true}).click();
 await page.getByRole('heading',{name:'会话暂不可用',exact:true}).waitFor({timeout:20000});await spaceControls(page);
 assert.match(await page.locator('#space-status').textContent(),/管理空间.*恢复.*切换其他空间/);
 await page.locator('#space-trigger').focus();await page.keyboard.press('ArrowDown');
 assert.equal(await page.locator('#space-menu [data-space-id]').count(),0);
 assert.equal(await page.evaluate(()=>document.activeElement.id),'space-settings');
 await page.keyboard.press('Escape');await openManagement();
 await management.getByRole('tab',{name:/使用中/}).click();
 assert.equal(await management.getByText('暂无使用中的空间。',{exact:true}).count(),1);
 await management.getByRole('tab',{name:/已归档/}).click();
 await screenshot('management-all-archived');
 await row(b).locator('[data-action="restore"]').click();await page.getByRole('button',{name:'确认恢复',exact:true}).click();await settledManager();
 await row(b).locator('[data-action="enter"]').click();await page.locator('#room-list [data-default-assistant="true"]').waitFor({timeout:20000});
 assert.equal(await page.locator('#message-input').inputValue(),'B private draft');assert.deepEqual(errors,[]);
 console.log(JSON.stringify({spaceUI:'personal-visible',defaultBadge:'identity-based',archivedSwitcher:'excluded',management:'cross-space',restoreEntry:'explicit',archiveRestore:'passed',archiveReturn:'default-or-ready',lastArchivedSpace:'recoverable',rejectedArchive:'stays',routes:'A-B-A-B',tabs:'independent',drafts:'isolated',setup:'independent',rename:'passed',lateResponse:'isolated',pageErrors:errors}));
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
  const createSpace=async()=>{await page.locator('#space-trigger').click();await page.locator('#space-create').click();};
  const source=await page.locator('#space-trigger').getAttribute('data-space-id');
  await createSpace();
  await page.locator('#space-create-name').fill('Automatic model');
  const creationRequest=page.waitForRequest(r=>r.method()==='POST'&&r.url().endsWith('/v1/spaces'));
  await page.getByRole('button',{name:'创建并进入',exact:true}).click();
  const input=(await creationRequest).postDataJSON();assert.equal(input.model_source_space_id,source);
  await page.waitForURL('**/s/'+input.request_id+'/**');
  await page.locator('#room-list [data-default-assistant="true"]').waitFor({timeout:20000});
  assert.equal(await page.locator('#space-trigger').getAttribute('data-space-id'),input.request_id);
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
