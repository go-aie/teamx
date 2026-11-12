// Explicit real-provider/browser acceptance. All writes use a new output home.
// Reuses a selected, already managed credential without printing its value.
import fs from 'node:fs';
import path from 'node:path';
import { spawn, spawnSync } from 'node:child_process';
import { createRequire } from 'node:module';
import assert from 'node:assert/strict';
const { chromium } = createRequire(import.meta.url)('playwright');
const [outputArg, sourceArg, runtimeArg, mode] = process.argv.slice(2);
const prefilled = mode === "prefill";
assert(outputArg && sourceArg && runtimeArg, 'output, managed source home and runtime release required');
const output = path.resolve(outputArg), source = path.resolve(sourceArg), runtime = path.resolve(runtimeArg);
const bundle = mode === 'archive' ? runtime : path.join(output, 'bundle'), home = path.join(output, 'home');
assert(fs.existsSync(path.join(bundle, 'bin/teamx')), 'build the current binary in output/bundle/bin/teamx first');
assert(!fs.existsSync(home), 'acceptance requires a new home');
const config = fs.readFileSync(path.join(source, 'config.toml'), 'utf8');
const get = key => { const m=config.match(new RegExp(`^${key} = ['"]([^'"\\n]*)['"]`, 'm')); assert(m,`source ${key} missing`); return m[1]; };
const key = fs.readFileSync(path.join(source,'model-credentials',get('credential_ref')), 'utf8');
// Credential store serializes the exact key as raw file bytes.
if (mode !== 'archive') {
fs.copyFileSync('packaging/start', path.join(bundle,'start'));
fs.chmodSync(path.join(bundle,'start'), 0o755);
fs.copyFileSync(path.join(runtime,'bin/dsh'), path.join(bundle,'bin/dsh'));
fs.chmodSync(path.join(bundle,'bin/dsh'), 0o755);
if (!fs.existsSync(path.join(bundle,'runtime'))) fs.symlinkSync(path.join(runtime,'runtime'), path.join(bundle,'runtime'), 'dir');
fs.mkdirSync(path.join(bundle,'share/teamx'), {recursive:true});
fs.cpSync('.teamx/teams', path.join(bundle,'share/teamx/teams'), {recursive:true});
}
const env = {...process.env};
for (const name of Object.keys(env)) if (/KEY|TOKEN|SECRET|DSH|TEAMX/.test(name)) delete env[name];
// Empty inherited names must also never override the explicit bound key.
env.OPENAI_API_KEY = '';
if (prefilled) {
 fs.mkdirSync(home,{mode:0o700});
 const modelBlock=config.slice(config.indexOf('protocol ='),config.indexOf('[models.entries.compat]')).trim();
 fs.writeFileSync(path.join(home,'config.toml'),`# Preserve this prefilled configuration comment\n[runtime.dsh]\nbinary_path = ${JSON.stringify(path.join(bundle,'bin/dsh'))}\n[runtime.model]\n${modelBlock}\napi_key_env = 'IMPORTED_PREFILL_KEY'\n[runtime.model.compat]\nsupports_developer_role = false\n[env]\nIMPORTED_PREFILL_KEY = ${JSON.stringify(key)}\n`,{mode:0o600});
}

const report={status:'failed',scope:'fresh start, browser validation/save/default assistant/real dialogue/restart',checks:[],remote_platform_writes:0};
let service, browser;
async function until(fn, label, timeout=120000) {
 const deadline=Date.now()+timeout;
 while(Date.now()<deadline){const value=await fn();if(value)return value;await new Promise(r=>setTimeout(r,200));}
 throw Error(`Timed out: ${label}`);
}
async function start(){
 let stdout='',stderr='';
 const child=spawn('/bin/sh',[path.join(bundle,'start'),'--home',home,'--listen','127.0.0.1:0'],{env,stdio:['ignore','pipe','pipe']});
 const exit=new Promise(resolve=>child.once('exit',(code,signal)=>resolve({code,signal})));
 service={child,exit,logs:()=>({stdout,stderr})};
 child.stdout.on('data',b=>{stdout+=b;});child.stderr.on('data',b=>{stderr+=b;});
 service.endpoint=await until(()=>{assert(child.exitCode===null,`service exited: ${stderr}`);for(const line of stdout.split('\n')){try{const item=JSON.parse(line);if(item.endpoint)return item.endpoint;}catch{}}},'startup',30000);
 service.logs=()=>({stdout,stderr});return service.endpoint;
}
async function stop(){if(!service)return;service.child.kill('SIGTERM');const result=await service.exit;fs.writeFileSync(path.join(output,'server-last.log'),JSON.stringify(service.logs()),{mode:0o600});service=undefined;if(report.status==='passed'||result.code===0)assert.equal(result.code,0,'server shutdown');}
function checked(name){report.checks.push(name);process.stdout.write(name+'\n');}
async function waitAssistantTurnCompleted() {
 return until(()=>{
  const index=JSON.parse(fs.readFileSync(path.join(home,'data/rooms.json'),'utf8'));
  const room=index.rooms.find(r=>r.is_direct&&r.status==='ready');if(!room)return false;
  const root=path.join(home,'data/rooms',room.room_id);
  const saved=JSON.parse(fs.readFileSync(path.join(root,'room-config.json'),'utf8'));
  const file=path.join(root,'agents',saved.members[0].membership_id,'agent.json');
  if(!fs.existsSync(file))return false;
  const agent=JSON.parse(fs.readFileSync(file,'utf8'));return agent.turn?.status==='completed';
 },'DSH turn terminal completion');
}

try{
 const endpoint=await start();checked('direct start without init/access init');
 const token=fs.readFileSync(path.join(home,'auth/admin-token'),'utf8').trim();
 const accessBefore=fs.readFileSync(path.join(home,'first-access.json'),'utf8');
 browser=await chromium.launch({headless:true});report.browser=browser.version();
 const page=await browser.newPage({viewport:{width:1280,height:960}});
 const errors=[];page.on('pageerror',error=>errors.push(error.message));
 await page.goto(endpoint);await page.locator('#token').fill(token);await page.locator('#login-form button').click();
 await page.locator('#model-form').waitFor({state:'visible'});
 if (!prefilled) {
 await page.locator('#base-url').fill('http://127.0.0.1:1/v1');await page.locator('#model-id').fill(get('model_id'));await page.locator('#api-key').fill(key);
 await page.locator('#save-model').click();await page.locator('#save-model:not([disabled])').waitFor({timeout:120000});
 assert(!fs.readFileSync(path.join(home,'config.toml'),'utf8').includes('credential_ref'));checked('failed validation does not publish');
 }
 await page.locator('#base-url').fill(get('base_url'));
 if (!prefilled) await page.locator('#api-key').fill(key);
 else { assert(await page.locator('#api-key').isHidden()); assert((await page.locator('#key-state').innerText()).includes('密钥已配置')); checked('prefilled credential is reused without input'); }
 await page.locator('summary').click();await page.locator('#developer-role').selectOption('false');
 await page.locator('#input-modalities').selectOption('text,image');await page.locator('#context-window').fill('1048576');await page.locator('#max-tokens').fill('32768');await page.locator('#reasoning-effort').selectOption('low');
 await page.screenshot({path:path.join(output,'setup.png'),fullPage:true});
 await page.locator('#save-model').click();await page.reload();
 await page.locator('#message-input').waitFor({state:'visible',timeout:150000});
 checked('reload retains accepted validation and same login across setup transition');
 const text='请在房间中回复 TEAMX_FIRST_USE_OK。这是模型首次启动验收，无需访问任何外部平台。';
 await page.locator('#message-input').fill(text);await page.locator('#send').click();

 await until(async()=>{const body=await page.locator('#messages').innerText();return body.includes('TEAMX_FIRST_USE_OK')&&body.split('TEAMX_FIRST_USE_OK').length>=3;},'real assistant reply');
 await waitAssistantTurnCompleted();checked('real first dialogue through the managed default model, turn completed');
 await page.screenshot({path:path.join(output,'dialogue.png'),fullPage:true});
 const published=fs.readFileSync(path.join(home,'config.toml'),'utf8');assert(!published.includes(key));assert(!published.includes('api_key_env'));assert(published.includes('supports_developer_role = false'));
 const catalog=await page.evaluate(async()=>{const r=await fetch('/v1/rooms');return r.json();});
 const assistant=catalog.rooms.find(r=>r.is_direct);assert(assistant);report.room_id=assistant.room_id;
 const saved=JSON.parse(fs.readFileSync(path.join(home,'data/rooms',assistant.room_id,'room-config.json'),'utf8'));
 assert.equal(saved.members[0].model_selection.mode,'default');assert.equal(saved.runtime.model,undefined);checked('default reference, compat false and private credential persisted');
 await stop();const restarted=await start();await page.goto(restarted);await page.locator('#token').fill(token);await page.locator('#login-form button').click();
 await page.locator('#message-input').waitFor({state:'visible',timeout:30000});assert.equal(fs.readFileSync(path.join(home,'first-access.json'),'utf8'),accessBefore);assert.equal(fs.readFileSync(path.join(home,'auth/admin-token'),'utf8').trim(),token);checked('restart keeps authentication and skips setup');
 await page.locator('#message-input').fill('请在房间中回复 TEAMX_FIRST_USE_RESTART_OK，无需访问任何外部平台。');await page.locator('#send').click();
 await until(async()=>{const body=await page.locator('#messages').innerText();return body.split('TEAMX_FIRST_USE_RESTART_OK').length>=3;},'real reply after restart');await waitAssistantTurnCompleted();checked('real reply after restart without exported key, turn completed');
 assert.deepEqual(errors,[]);report.status='passed';
} catch(error) {report.error=error.message;throw error;} finally {
 if(browser)await browser.close();await stop();
 fs.writeFileSync(path.join(output,'report.json'),JSON.stringify(report,null,2),{mode:0o600});
}
