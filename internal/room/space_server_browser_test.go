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
 await page.goto(process.argv[1]);await page.locator('#room-list [data-default-assistant="true"]').waitFor();
 await page.locator('#space-select').waitFor();const a=await page.locator('#space-select').inputValue();
 await page.locator('#message-input').fill('A private draft');
 await page.getByRole('button',{name:'空间设置',exact:true}).click();
 await page.locator('#space-name').fill('Work');await page.getByRole('button',{name:'创建空间',exact:true}).click();
 await page.locator('#model-form').waitFor();const b=await page.locator('#space-select').inputValue();assert.notEqual(a,b);
 await page.locator('#base-url').fill('https://example.invalid/v1');await page.locator('#model-id').fill('controlled-b');await page.locator('#api-key').fill('synthetic-private-b-key');
 await page.locator('#save-model').click();await page.locator('#room-list [data-default-assistant="true"]').waitFor({timeout:20000});
 await page.locator('#space-select').waitFor();assert.equal(await page.locator('#message-input').inputValue(),'');
 await page.locator('#message-input').fill('B private draft');
 const tab=await context.newPage();tab.on('pageerror',e=>errors.push(e.message));
 await tab.goto(process.argv[1]+'/s/'+a+'/');await tab.locator('#room-list [data-default-assistant="true"]').waitFor();
 assert.equal(await tab.locator('#space-select').inputValue(),a);assert.equal(await tab.locator('#message-input').inputValue(),'');await tab.locator('#message-input').fill('A second tab draft');
 assert.equal(await page.locator('#space-select').inputValue(),b);
 await page.locator('#space-select').selectOption(a);await page.locator('#room-list [data-default-assistant="true"]').waitFor();
 assert.equal(await page.locator('#message-input').inputValue(),'A private draft');
 await page.locator('#space-select').selectOption(b);await page.locator('#room-list [data-default-assistant="true"]').waitFor();
 assert.equal(await page.locator('#message-input').inputValue(),'B private draft');
 await page.getByRole('button',{name:'空间设置',exact:true}).click();await page.locator('#space-name').fill('Renamed Work');await page.getByRole('button',{name:'保存名称',exact:true}).click();await page.locator('.space-dialog').waitFor({state:'hidden'});
 assert.equal(await page.locator('#space-select option:checked').textContent(),'Renamed Work');
 assert.equal(await tab.locator('#space-select').inputValue(),a);
 let releaseOld;const held=new Promise(resolve=>releaseOld=resolve);
 const oldRoute='**/s/'+a+'/v1/rooms';
 await page.route(oldRoute,async route=>{const response=await route.fetch();await held;try{await route.fulfill({response})}catch{}});
 const oldRequest=page.waitForRequest(request=>request.url().endsWith('/s/'+a+'/v1/rooms'));
 await page.locator('#space-select').selectOption(a);await oldRequest;
 await page.locator('#space-select').selectOption(b);releaseOld();
 await page.locator('#room-list [data-default-assistant="true"]').waitFor();
 assert.equal(await page.locator('#message-input').inputValue(),'B private draft');
 assert.equal(await page.locator('#space-select').inputValue(),b);
 await page.unroute(oldRoute);
 assert.deepEqual(errors,[]);
  // Archive B, preserve A's open tab, then restore with new UI instance.
 await page.getByRole('button',{name:'空间设置',exact:true}).click();
 await page.getByRole('button',{name:'归档空间',exact:true}).click();
 await page.getByRole('button',{name:'确认归档',exact:true}).click();
 await page.getByRole('heading',{name:'空间已归档',exact:true}).waitFor({timeout:20000});
 assert.equal(await tab.locator('#space-select').inputValue(),a);
 const denied=await context.request.get(process.argv[1]+'/s/'+b+'/v1/rooms');assert.equal(denied.status(),503);
 await page.getByRole('button',{name:'空间设置',exact:true}).click();
 await page.getByRole('button',{name:'恢复空间',exact:true}).click();
 await page.locator('#room-list [data-default-assistant="true"]').waitFor({timeout:20000});
 assert.equal(await page.locator('#message-input').inputValue(),'B private draft');
 assert.deepEqual(errors,[]);
 console.log(JSON.stringify({archiveRestore:'passed',switch:'A-B-A-B',tabs:'independent',drafts:'isolated',setup:'independent',rename:'passed',lateResponse:'isolated',pageErrors:errors}));
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
