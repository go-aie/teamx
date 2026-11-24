//go:build e2e

package room

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestPersonalSpaceIdentitySetupBrowser(t *testing.T) {
	if os.Getenv("TEAMX_SPACE_IDENTITY_BROWSER") != "1" {
		t.Skip("optional Chromium acceptance of automatic User/Space registration")
	}
	endpoint, _ := startPersonalSetupServer(t, t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	script := `
 const {chromium}=require('playwright'),assert=require('node:assert/strict');
 (async()=>{const browser=await chromium.launch({headless:true});try{
  const page=await browser.newPage(),errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.goto(process.argv[1]);await page.locator('#model-form').waitFor({state:'visible'});
  assert.equal(await page.locator('#login').isVisible(),false);
  const session=await page.evaluate(()=>fetch('/v1/access/session').then(r=>r.json()));
  assert.match(session.user_id,/^owner_[a-f0-9]{32}$/);assert.match(session.space_id,/^[a-f0-9]{32}$/);
  assert.equal(session.operator,session.user_id);assert.equal(session.login_enabled,false);
  const scoped=await page.evaluate(id=>fetch('/v1/spaces/'+id+'/setup').then(r=>r.status),session.space_id);
  assert.equal(scoped,200);
  const foreign=await page.evaluate(()=>fetch('/v1/spaces/other/setup').then(r=>r.status));assert.equal(foreign,403);
  await page.reload();await page.locator('#model-form').waitFor({state:'visible'});
  assert.deepEqual(await page.evaluate(()=>fetch('/v1/access/session').then(r=>r.json())),session);
  assert.deepEqual(errors,[]);
 }finally{await browser.close()}})().catch(e=>{console.error(e);process.exit(1)});`
	command := exec.CommandContext(ctx, "node", "-e", script, endpoint)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Chromium registration/setup: %v\n%s", err, output)
	}
}
