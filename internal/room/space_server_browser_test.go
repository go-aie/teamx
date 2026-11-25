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
	_, endpoint, _ := startSpaceServerFixture(t, t.TempDir())
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
 const noSpaceControls=async target=>{
  assert.equal(await target.locator('.space-picker,.space-dialog,#space-select').count(),0);
  assert.equal(await target.getByRole('button',{name:'空间设置',exact:true}).count(),0);
 };
 const navigate=async id=>{await page.goto(process.argv[1]+'/s/'+id+'/');await page.locator('#room-list [data-default-assistant="true"]').waitFor();await noSpaceControls(page);};
 await page.goto(process.argv[1]);await page.locator('#room-list [data-default-assistant="true"]').waitFor();
 await noSpaceControls(page);const a=(await directory()).default_space_id;
 await page.locator('#message-input').fill('A private draft');
 const b=require('node:crypto').randomUUID().replaceAll('-','');
 const created=await request('/v1/spaces',{request_id:b,name:'Work'});assert.equal(created.id,b);assert.notEqual(a,b);
 await page.goto(process.argv[1]+'/s/'+b+'/');await page.locator('#model-form').waitFor();await noSpaceControls(page);
 await page.locator('#base-url').fill('https://example.invalid/v1');await page.locator('#model-id').fill('controlled-b');await page.locator('#api-key').fill('synthetic-private-b-key');
 await page.locator('#save-model').click();await page.locator('#room-list [data-default-assistant="true"]').waitFor({timeout:20000});
 await noSpaceControls(page);assert.equal(await page.locator('#message-input').inputValue(),'');
 await page.locator('#message-input').fill('B private draft');
 const tab=await context.newPage();tab.on('pageerror',e=>errors.push(e.message));
 await tab.goto(process.argv[1]+'/s/'+a+'/');await tab.locator('#room-list [data-default-assistant="true"]').waitFor();
 await noSpaceControls(tab);assert.ok(tab.url().includes('/s/'+a+'/'));assert.equal(await tab.locator('#message-input').inputValue(),'');await tab.locator('#message-input').fill('A second tab draft');
 assert.ok(page.url().includes('/s/'+b+'/'));
 await navigate(a);
 assert.equal(await page.locator('#message-input').inputValue(),'A private draft');
 await navigate(b);
 assert.equal(await page.locator('#message-input').inputValue(),'B private draft');
 await request('/v1/spaces/'+b,{name:'Renamed Work',expected_version:(await record(b)).version});
 assert.equal((await record(b)).name,'Renamed Work');
 assert.ok(tab.url().includes('/s/'+a+'/'));
 let releaseOld;const held=new Promise(resolve=>releaseOld=resolve);
 const oldRoute='**/s/'+a+'/v1/rooms';
 await page.route(oldRoute,async route=>{const response=await route.fetch();await held;try{await route.fulfill({response})}catch{}});
 const oldRequest=page.waitForRequest(request=>request.url().endsWith('/s/'+a+'/v1/rooms'));
 await page.goto(process.argv[1]+'/s/'+a+'/',{waitUntil:'domcontentloaded'});await oldRequest;
 await navigate(b);releaseOld();
 await page.locator('#room-list [data-default-assistant="true"]').waitFor();
 assert.equal(await page.locator('#message-input').inputValue(),'B private draft');
 assert.ok(page.url().includes('/s/'+b+'/'));
 await page.unroute(oldRoute);
 assert.deepEqual(errors,[]);
  // The backend still manages lifecycle while the UI exposes no controls.
 await request('/v1/spaces/'+b+'/archive',{expected_version:(await record(b)).version});
 await page.goto(process.argv[1]+'/s/'+b+'/');
 await page.getByRole('heading',{name:'会话暂不可用',exact:true}).waitFor({timeout:20000});await noSpaceControls(page);
 assert.ok(tab.url().includes('/s/'+a+'/'));
 assert.equal(await tab.locator('#message-input').inputValue(),'A second tab draft');
 const denied=await context.request.get(process.argv[1]+'/s/'+b+'/v1/rooms');assert.equal(denied.status(),503);
 await request('/v1/spaces/'+b+'/restore',{expected_version:(await record(b)).version});
 await page.locator('#space-reload').click();
 await page.locator('#room-list [data-default-assistant="true"]').waitFor({timeout:20000});
 assert.equal(await page.locator('#message-input').inputValue(),'B private draft');
 await noSpaceControls(page);assert.deepEqual(errors,[]);
 console.log(JSON.stringify({spaceUI:'hidden',archiveRestore:'passed',routes:'A-B-A-B',tabs:'independent',drafts:'isolated',setup:'independent',rename:'passed',lateResponse:'isolated',pageErrors:errors}));
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
