//go:build e2e

package room

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/pelletier/go-toml/v2"
)

// A fresh isolated Home only. The optional source supplies read-only model
// parameters/credential; no original business state or remote writes are used.
func TestSpaceResetBrowser(t *testing.T) {
	if os.Getenv("TEAMX_SPACE_RESET_BROWSER") != "1" {
		t.Skip("explicit Chromium reset acceptance")
	}
	home := t.TempDir()
	seed := resetSeedForTest(t)
	source := os.Getenv("TEAMX_SPACE_RESET_REAL_CONFIG")
	output := os.Getenv("TEAMX_SPACE_RESET_OUTPUT")
	if output != "" {
		if !filepath.IsAbs(output) {
			t.Fatal("absolute fresh output required")
		}
		if err := os.Mkdir(output, 0700); err != nil {
			t.Fatal(err)
		}
		home = filepath.Join(output, "home")
		if err := os.Mkdir(home, 0700); err != nil {
			t.Fatal(err)
		}
	}
	launcher := testTeamDefinition(t).Runtime.DSH
	parameters := modelTestEntry("browser-reset").ModelParameters
	key := "synthetic-key"
	var probe func(context.Context, dsh.Options) (dsh.ProbeResult, error) = successfulModelProbe
	selected := personalDeployment(controlledActivationFactory(nil, nil))
	if source != "" {
		original, err := loadStartupConfig(source, true)
		if err != nil || original.Models == nil || original.Runtime == nil {
			t.Fatal("managed model source required", err)
		}
		entry, err := original.Models.resolve(modelSelection{Mode: "default"})
		if err != nil {
			t.Fatal(err)
		}
		credential, err := (&modelCredentials{dir: filepath.Join(filepath.Dir(source), "model-credentials")}).get(entry.CredentialRef)
		if err != nil {
			t.Fatal(err)
		}
		launcher, parameters, key = original.Runtime.DSH, entry.ModelParameters.clone(), credential.Value
		probe = nil
		selected = personalDeployment(nil)
		binary := productionFixtureCLI(t)
		selected.factories.service = func(dir, id string, config serviceConfig) (*service, error) {
			config.cliPath = binary
			return newConfiguredService(dir, id, config)
		}
	}
	raw, err := toml.Marshal(map[string]any{"runtime": map[string]any{"dsh": launcher}, "models": modelCatalog{SchemaVersion: 1}})
	if err != nil {
		t.Fatal(err)
	}
	mustWriteResetFile(t, filepath.Join(home, "config.toml"), string(raw))
	startup := freshAccessConfig(t, home)
	models, err := newModelService(startup, newSystemSettings(startup), home)
	if err != nil {
		t.Fatal(err)
	}
	if probe != nil {
		models.probe = probe
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	if _, err := models.save(ctx, modelSave{Entry: modelEntry{ID: "daily", Name: "Reset acceptance", ModelParameters: parameters}, APIKey: key, ExpectedVersion: 0, MakeDefault: true}); err != nil {
		t.Fatal(err)
	}
	if err := initializeTeamTemplates(seed, teamDefaultsPath(filepath.Join(home, "config.toml"))); err != nil {
		t.Fatal(err)
	}
	info, stop := startResetServerForTest(t, []string{"--home", home}, serverOptions{deployment: &selected, platformCatalog: defaultPlatformCatalog(), probe: probe})
	defer stop()
	s := selected.spaces
	id := s.directory.DefaultSpaceID
	before := waitSpaceState(t, s, id, "ready")
	oldService := waitSpaceAssistant(t, before)
	oldRoom := oldService.info.RoomIdentity
	input, err := json.Marshal(setupModelInput{ModelParameters: parameters, APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	script := `
 const {chromium}=require('playwright'),assert=require('node:assert/strict');
 (async()=>{const browser=await chromium.launch({headless:true});try{
 const context=await browser.newContext(),page=await context.newPage(),errors=[];
 page.on('pageerror',e=>errors.push(e.message));
 const base=process.argv[1],real=process.env.TEAMX_RESET_REAL==='1',model=JSON.parse(process.env.TEAMX_RESET_MODEL);
 const request=async(path,body)=>{const response=await context.request.fetch(base+path,{method:body?'POST':'GET',data:body});const result=await response.json();assert.ok(response.ok(),JSON.stringify(result));return result;};
 await page.goto(base);await page.locator('#room-list [data-default-assistant="true"]').waitFor();
 const dir=await request('/v1/spaces'),id=dir.default_space_id,prefix='teamx.space:'+id+':';
 const reply=async marker=>{
   await page.locator('#message-input').fill('这是空间重置验收。请通过 send 在当前会话仅回复 '+marker+'。不要调用平台、联网、读写文件或执行其他操作。');await page.locator('#send').click();
   await page.waitForFunction(marker=>[...document.querySelectorAll('.message:not(.own) .message-text')].some(el=>el.textContent.trim()===marker),marker,{timeout:120000});
 };
 if(real)await reply('TEAMX_RESET_BEFORE_OK');
 const tab=await context.newPage();tab.on('pageerror',e=>errors.push(e.message));await tab.goto(page.url());await tab.locator('#message-input').waitFor();
 await tab.locator('#message-input').fill('stale second tab');
 await page.locator('#message-input').fill('old draft');
 await page.evaluate(({prefix})=>{localStorage.setItem(prefix+'old-private','old');localStorage.setItem('teamx.space:other:keep','keep');sessionStorage.setItem(prefix+'old-private','old');},{prefix});
 await page.locator('#user-menu-toggle').click();await page.locator('#open-settings').click();await page.getByRole('button',{name:'重置此空间',exact:true}).click();
 if(process.env.TEAMX_RESET_OUTPUT)await page.screenshot({path:process.env.TEAMX_RESET_OUTPUT+'/confirmation.png',fullPage:true});
 await page.getByRole('dialog',{name:'重置此空间',exact:true}).getByRole('button',{name:'确认重置',exact:true}).click();
 await page.locator('#model-form').waitFor({state:'visible',timeout:45000});
 await tab.locator('#model-form').waitFor({state:'visible',timeout:45000});
 if(process.env.TEAMX_RESET_OUTPUT)await page.screenshot({path:process.env.TEAMX_RESET_OUTPUT+'/setup-after-reset.png',fullPage:true});
 assert.equal(await page.evaluate(prefix=>localStorage.getItem(prefix+'old-private'),prefix),null);
 assert.equal(await page.evaluate(()=>localStorage.getItem('teamx.space:other:keep')),'keep');
 assert.equal(await tab.evaluate(prefix=>sessionStorage.getItem(prefix+'old-private'),prefix),null);
 const completed=(await request('/v1/spaces')).spaces.find(s=>s.id===id);assert.equal(completed.reset.phase,'completed');
 await page.locator('#protocol').selectOption(model.protocol);
 if(model.base_url)await page.locator('#base-url').fill(model.base_url);
 await page.locator('#model-id').fill(model.model_id);await page.locator('#api-key').fill(model.api_key);
 await page.locator('#model-advanced').evaluate(el=>el.open=true);
 if(model.context_window)await page.locator('#context-window').fill(String(model.context_window));
 if(model.max_tokens)await page.locator('#max-tokens').fill(String(model.max_tokens));
 if(model.input_modalities)await page.locator('#input-modalities').selectOption(model.input_modalities.join(','));
 if(model.reasoning_effort)await page.locator('#reasoning-effort').selectOption(model.reasoning_effort);
 if(model.compat?.supports_developer_role!==undefined)await page.locator('#developer-role').selectOption(String(model.compat.supports_developer_role));
 await page.locator('#save-model').click();await page.locator('#room-list [data-default-assistant="true"]').waitFor({timeout:120000});
 assert.equal(await page.locator('#message-input').inputValue(),'');
 if(real)await reply('TEAMX_RESET_AFTER_OK');
 const resetID=completed.reset.request_id;
 await page.locator('#message-input').fill('keep through ordinary reload');await page.reload();await page.locator('#message-input').waitFor();
 assert.equal(await page.locator('#message-input').inputValue(),'keep through ordinary reload');
 assert.equal((await request('/v1/spaces')).spaces.find(s=>s.id===id).reset.request_id,resetID);
 if(process.env.TEAMX_RESET_OUTPUT)await page.screenshot({path:process.env.TEAMX_RESET_OUTPUT+'/after-reset.png',fullPage:true});
 assert.deepEqual(errors,[]);console.log(JSON.stringify({browser:'passed',real_model:real,tabs:2,reset_id:resetID,cache_isolation:'passed',page_errors:errors}));
 }finally{await browser.close()}})().catch(e=>{console.error(e);process.exit(1)});`
	modules, err := filepath.Abs("../../packaging/verifier/node_modules")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "node", "-e", script, info.Endpoint)
	real := "0"
	if source != "" {
		real = "1"
	}
	cmd.Env = append(os.Environ(), "NODE_PATH="+modules, "TEAMX_RESET_MODEL="+string(input), "TEAMX_RESET_REAL="+real, "TEAMX_RESET_OUTPUT="+output)
	result, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Chromium reset: %v\n%s", err, result)
	}
	t.Log(string(result))
	after := waitSpaceState(t, s, id, "ready")
	newService := waitSpaceAssistant(t, after)
	if newService.info.RoomIdentity == oldRoom || before.RuntimeEpoch == after.RuntimeEpoch {
		t.Fatal("old business identity reused")
	}
	if output != "" {
		if err := os.WriteFile(filepath.Join(output, "report.json"), result, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
