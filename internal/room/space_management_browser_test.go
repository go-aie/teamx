//go:build e2e

package room

import (
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Real HTTPS transport, browser UI and Space managers with two authenticated
// fixture users. Model/platform stubs keep all writes inside this temporary Home.
func TestHostedSpaceManagementBrowser(t *testing.T) {
	if os.Getenv("TEAMX_SPACES_BROWSER") != "1" {
		t.Skip("set TEAMX_SPACES_BROWSER=1 for Chromium acceptance")
	}
	s := hostedResetFixture(t)
	bin := t.TempDir()
	cli := filepath.Join(bin, "lark-cli")
	mustWriteResetFile(t, cli, "#!/bin/sh\necho 'lark-cli version 1.0.99'\n")
	if err := os.Chmod(cli, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	s.assembly.config.hostTools = hostToolPolicy{restricted: true}
	s.assembly.config.executionResources = newExecutionResources(executionResourceLimits{SpacesPerUser: 2})
	var users []map[string]string
	for _, account := range []string{"alice", "bob"} {
		cookie, subject, id := hostedLogin(t, s, account)
		waitSpaceAssistant(t, waitSpaceState(t, s, id, "ready"))
		users = append(users, map[string]string{"account": account, "owner": subject.UserID, "space": id, "cookie_name": cookie.Name, "cookie_value": cookie.Value})
	}
	server := httptest.NewUnstartedServer(s)
	server.Config.ErrorLog = log.New(io.Discard, "", 0) // Chromium retries the fixture's self-signed certificate.
	server.StartTLS()
	t.Cleanup(func() {
		if err := s.closeWithTimeout(); err != nil {
			t.Error(err)
		}
		server.Close()
	})
	s.access.public = server.URL
	payload, err := json.Marshal(users)
	if err != nil {
		t.Fatal(err)
	}
	script := `const {chromium}=require('playwright'),assert=require('node:assert/strict');
(async()=>{const browser=await chromium.launch({headless:true});try{
 const base=process.argv[1],users=JSON.parse(process.env.TEAMX_SPACE_BROWSER_USERS),errors=[],views=[];
 const ready=page=>page.locator('#room-list [data-default-assistant="true"]').waitFor({timeout:20000});
 for(const user of users){
  const context=await browser.newContext({ignoreHTTPSErrors:true});
  await context.addCookies([{name:user.cookie_name,value:user.cookie_value,url:base,secure:true,httpOnly:true,sameSite:'Lax'}]);
  const page=await context.newPage();page.on('pageerror',e=>errors.push(e.message));
  const request=async(path,body)=>{
   const response=await context.request.fetch(base+path,{method:body?'POST':'GET',headers:body?{Origin:base}:undefined,data:body});
   return {status:response.status(),data:await response.json()};
  };
  const directory=async()=>{const r=await request('/v1/spaces');assert.equal(r.status,200);return r.data;};
  const management=page.getByRole('dialog',{name:'管理空间',exact:true});
  const settled=async()=>{await management.waitFor();await page.waitForFunction(()=>!document.getElementById('space-management-refresh').disabled);};
  const open=async()=>{await page.locator('#space-trigger').click();await page.locator('#space-settings').click();await settled();};
  const close=async()=>{await management.getByRole('button',{name:'关闭',exact:true}).click();};
  const row=id=>management.locator('[data-space-id="'+id+'"]');
  const create=async()=>{await page.locator('#space-trigger').click();await page.locator('#space-create').click();};
  const choose=async id=>{await page.locator('#space-trigger').click();await page.locator('#space-menu [data-space-id="'+id+'"]').click();await ready(page);};
  const archive=async id=>{
   await open();await row(id).locator('[data-action="more"]').click();await row(id).locator('[data-action="archive"]').click();
   await page.getByRole('button',{name:'确认归档',exact:true}).click();
  };
  const restore=async id=>{
   await management.getByRole('tab',{name:/已归档/}).click();await row(id).locator('[data-action="restore"]').click();
   await page.getByRole('button',{name:'确认恢复',exact:true}).click();await settled();
   await management.getByRole('tab',{name:/使用中/}).click();
   await row(id).locator('[data-action="enter"]').click();await ready(page);
  };
  await page.goto(base);await ready(page);
  assert.equal(await page.locator('#space-trigger').getAttribute('data-space-id'),user.space);
  assert.equal((await directory()).spaces.length,1);
  await create();
  const creation=page.getByRole('dialog',{name:'新建空间',exact:true});
  assert.equal(await creation.locator('.space-hint:not(.space-note)').textContent(),'将自动配置默认模型，平台连接需单独配置。');
  assert.equal(await creation.locator('.space-note').isVisible(),false);
  for(const width of [1280,390,320]){await page.setViewportSize({width,height:800});assert.equal(await creation.evaluate(el=>el.scrollWidth<=el.clientWidth),true);}
  await page.setViewportSize({width:1280,height:800});await page.locator('#space-create-name').fill(user.account+' work');
  let original;
  if(user.account==='alice')await page.route('**/v1/spaces',async route=>{
   if(route.request().method()!=='POST')return route.continue();
   original=route.request().postDataJSON();await route.fetch();await route.abort();
  });
  const accepted=page.waitForRequest(r=>r.method()==='POST'&&r.url().endsWith('/v1/spaces'));
  await page.getByRole('button',{name:'创建并进入',exact:true}).click();
  const input=(await accepted).postDataJSON();assert.equal('model_source_space_id' in input,false);
  if(user.account==='alice'){
   await page.getByRole('dialog',{name:'继续上次创建',exact:true}).waitFor();await page.unroute('**/v1/spaces');
   const retry=page.waitForRequest(r=>r.method()==='POST'&&r.url().endsWith('/v1/spaces'));
   await page.getByRole('button',{name:'核对并继续',exact:true}).click();
   assert.deepEqual((await retry).postDataJSON(),original);
  }
  await page.waitForURL('**/s/'+input.request_id+'/**');await ready(page);
  const child=input.request_id;let d=await directory();
  assert.equal(d.default_space_id,user.space);assert.equal(d.spaces.length,2);
  for(const space of d.spaces){
   assert.equal(space.owner_user_id,user.owner);assert.equal(space.capabilities.space_management,true);
   assert.equal(space.capabilities.space_model_source,false);assert.equal(space.capabilities.lark,true);
   for(const feature of ['scion','computer','managed_repositories'])assert.equal(space.capabilities[feature],false);
  }
  await open();await row(child).locator('[data-action="more"]').click();await row(child).locator('[data-action="rename"]').click();
  await page.locator('#space-name').fill(user.account+' renamed');await page.getByRole('dialog',{name:'修改空间名称',exact:true}).getByRole('button',{name:'保存',exact:true}).click();
  await settled();assert.match(await row(child).textContent(),new RegExp(user.account+' renamed'));await close();
  await page.locator('#message-input').fill(user.account+' child draft');await choose(user.space);
  assert.equal(await page.locator('#message-input').inputValue(),'');await choose(child);
  assert.equal(await page.locator('#message-input').inputValue(),user.account+' child draft');
  if(user.account==='alice'){
   await create();await page.locator('#space-create-name').fill('over capacity');await page.getByRole('button',{name:'创建并进入',exact:true}).click();
   await creation.locator('.space-error').filter({hasText:'空间正在处理其他操作'}).waitFor();
   assert.equal((await directory()).spaces.length,2);await creation.getByRole('button',{name:'取消',exact:true}).click();
   await archive(child);await page.waitForURL('**/s/'+user.space+'/');await ready(page);
   await open();await restore(child);assert.equal(await page.locator('#message-input').inputValue(),user.account+' child draft');
   // A stopped default retains global management on a fresh root navigation.
   await archive(user.space);await settled();await close();await page.goto(base);
   await page.locator('#space-status').filter({hasText:'管理空间中恢复'}).waitFor();
   await open();await restore(user.space);
  }
  views.push({user,context,page,request,directory,child});
 }
 const [a,b]=views;
 for(const view of views){
  const foreign=view===a?b:a;
  assert.equal((await view.directory()).spaces.some(s=>s.owner_user_id!==view.user.owner),false);
  assert.equal((await view.request('/s/'+foreign.child+'/v1/ui')).status,403);
  for(const action of ['', '/archive','/restore','/reset']){
   const body=action==='/reset'?{request_id:'e'.repeat(32),expected_version:1}:action?{expected_version:1}:{name:'foreign',expected_version:1};
   assert.equal((await view.request('/v1/spaces/'+foreign.child+action,body)).status,403,action || 'rename');
  }
 }
 assert.deepEqual(errors,[]);
 console.log(JSON.stringify({spaceManagement:'hosted-visible',users:2,spacesPerUser:2,creation:'automatic',uncertainRetry:'same-body',rename:'passed',switchDrafts:'isolated',archiveRestore:'passed',archivedDefault:'recoverable',capacity:'enforced',crossOwner:'rejected',pageErrors:errors}));
}finally{await browser.close()}})().catch(e=>{console.error(e);process.exit(1)});`
	modules, err := filepath.Abs("../../packaging/verifier/node_modules")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "node", "-e", script, server.URL)
	command.Env = append(os.Environ(), "NODE_PATH="+modules, "TEAMX_SPACE_BROWSER_USERS="+string(payload))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Chromium hosted Space management: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
	s.mu.Lock()
	records := append([]SpaceRecord{}, s.directory.Spaces...)
	s.mu.Unlock()
	for _, record := range records {
		r := waitSpaceState(t, s, record.ID, "ready")
		entry, err := r.manager.config.models.catalog().resolve(modelSelection{Mode: "default"})
		if err != nil {
			t.Fatal(err)
		}
		key, err := r.manager.config.models.Credentials.get(entry.CredentialRef)
		owner, _ := s.access.users.user(record.OwnerUserID)
		if err != nil || key.Value != "gk_quota-for-"+owner.Identity.Subject {
			t.Fatal("browser-created Space did not use its owner's model quota", err)
		}
	}
}
