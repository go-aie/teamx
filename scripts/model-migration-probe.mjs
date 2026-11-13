// Explicit real-provider acceptance; every write uses a new isolated home.
// The initial explicit-config run creates a legacy Session with today's adapter,
// then normal startup migrates it. No deployed home or platform data is changed.
import fs from 'node:fs';
import path from 'node:path';
import http from 'node:http';
import {spawn,execFileSync} from 'node:child_process';
import {Readable} from 'node:stream';
import {createRequire} from 'node:module';
import assert from 'node:assert/strict';
const {chromium}=createRequire(import.meta.url)('playwright');
const [outArg,sourceArg,binaryArg,dshArg]=process.argv.slice(2);
const output=path.resolve(outArg),source=path.resolve(sourceArg),binary=path.resolve(binaryArg),home=path.join(output,'home');
fs.mkdirSync(output,{mode:0o700});fs.mkdirSync(home,{mode:0o700});
const sourceConfig=fs.readFileSync(path.join(source,'config.toml'),'utf8');
const get=k=>{const m=sourceConfig.match(new RegExp(`^${k} = ['"]([^'"\\n]*)['"]`,'m'));assert(m,`source ${k} missing`);return m[1];};
const key=fs.readFileSync(path.join(source,'model-credentials',get('credential_ref')),'utf8');
const env={...process.env};for(const k of Object.keys(env))if(/KEY|TOKEN|SECRET|DSH|TEAMX/.test(k))delete env[k];env.OPENAI_API_KEY='';
const report={status:'failed',scope:'real legacy Session migration/resume/repair/restart; current adapter legacy format',checks:[],requests:[],remote_platform_writes:0};
let server,browser,page,proxy;
async function until(fn,label,timeout=150000){const end=Date.now()+timeout;while(Date.now()<end){const v=await fn();if(v)return v;await new Promise(r=>setTimeout(r,150));}throw Error(`Timed out: ${label}`);}
const checked=label=>{report.checks.push(label);process.stdout.write(label+'\n');};
function files(){const index=JSON.parse(fs.readFileSync(path.join(home,'data/rooms.json'),'utf8'));const room=index.rooms.find(r=>r.is_direct);const root=path.join(home,'data/rooms',room.room_id);const saved=JSON.parse(fs.readFileSync(path.join(root,'room-config.json'),'utf8'));return {root,member:path.join(root,'agents',saved.members[0].membership_id),saved};}
function turn(){const f=path.join(files().member,'agent.json');return fs.existsSync(f)?JSON.parse(fs.readFileSync(f,'utf8')):{turn_number:0};}
async function start(){let logs='';const child=spawn(binary,['serve','--config',path.join(home,'config.toml'),'--listen','127.0.0.1:0'],{env,stdio:['ignore','pipe','pipe']});const exit=new Promise(resolve=>child.once('exit',(code,signal)=>resolve({code,signal})));server={child,exit,logs:()=>logs};child.stdout.on('data',b=>logs+=b);child.stderr.on('data',b=>logs+=b);return until(()=>{assert.equal(child.exitCode,null,logs);for(const line of logs.split('\n'))try{const item=JSON.parse(line);if(item.endpoint)return item.endpoint;}catch{}},'startup',30000);}
async function stop(){if(!server)return;server.child.kill('SIGTERM');const result=await server.exit;fs.writeFileSync(path.join(output,'server.log'),server.logs(),{mode:0o600});server=undefined;assert.equal(result.code,0,'clean shutdown');}
async function login(endpoint){await page.goto(endpoint);await page.locator('#token').fill(fs.readFileSync(path.join(home,'auth/admin-token'),'utf8').trim());await page.locator('#login-submit').click();await page.locator('#message-input').waitFor();}
async function dialogue(marker){const before=turn().turn_number;await page.locator('#message-input').fill(`请通过 send 在会话中仅回复 ${marker}，无需其他工具或外部平台操作。`);await page.locator('#send').click();await until(()=>turn().turn_number>before&&turn().turn?.status==='completed','completed real dialogue');await until(async()=>(await page.locator('#messages').innerText()).split(marker).length>=3,'published reply');}
async function openSettings(){if(!await page.locator('#open-settings').isVisible())await page.locator('#user-menu-toggle').click();await page.locator('#open-settings').click();}
try{
 proxy=http.createServer(async(req,res)=>{try{const chunks=[];for await(const b of req)chunks.push(b);const body=Buffer.concat(chunks);const payload=JSON.parse(body);report.requests.push({model:payload.model,developer:payload.messages?.some(m=>m.role==='developer'),max_tokens:payload.max_tokens??payload.max_completion_tokens,credential_matches:req.headers.authorization===`Bearer ${key}`});const upstream=await fetch(get('base_url').replace(/\/$/,'')+req.url.replace(/^\/v1/,''),{method:req.method,headers:{'content-type':'application/json',authorization:req.headers.authorization},body});res.writeHead(upstream.status,{'content-type':upstream.headers.get('content-type')||'application/json'});if(upstream.body)Readable.fromWeb(upstream.body).pipe(res);else res.end();}catch{res.writeHead(502);res.end();}});
 await new Promise(r=>proxy.listen(0,'127.0.0.1',r));const base=`http://127.0.0.1:${proxy.address().port}/v1`;
 fs.writeFileSync(path.join(home,'config.toml'),`# migration acceptance: preserve comment\n[server]\ndata_dir='data'\nteams_dir='teams'\ndsh_turn_timeout='3m'\n[access]\nlogin_enabled=true\n[runtime.dsh]\nbinary_path=${JSON.stringify(dshArg?path.resolve(dshArg):get('binary_path'))}\n[runtime.model]\nprotocol='openai-completions'\nbase_url=${JSON.stringify(base)}\nmodel_id=${JSON.stringify(get('model_id'))}\napi_key_env='LEGACY_MIGRATION_KEY'\n[runtime.model.compat]\nsupports_developer_role=false\n[env]\nLEGACY_MIGRATION_KEY=${JSON.stringify(key)}\n`,{mode:0o600});
 execFileSync(binary,['access','init','--config',path.join(home,'config.toml')],{env,stdio:'pipe'});
 const endpoint=await start();browser=await chromium.launch({headless:true});page=await browser.newPage({viewport:{width:1400,height:1050}});const errors=[];page.on('pageerror',e=>errors.push(e.message));
 await login(endpoint);await dialogue('TEAMX_LEGACY_BEFORE_OK');checked('legacy custom-key Session completes real dialogue');
 const originalRef=JSON.parse(fs.readFileSync(path.join(files().member,'session.json'),'utf8'));const backendPath=path.join(files().member,originalRef.directory,'backend.json');
 await stop();const originalBackend=JSON.parse(fs.readFileSync(backendPath,'utf8'));
 await login(await start());const migratedConfig=fs.readFileSync(path.join(home,'config.toml'),'utf8');assert(!migratedConfig.includes(key));assert(!migratedConfig.includes('api_key_env'));assert(!migratedConfig.includes('[runtime.model]'));
 assert.equal(files().saved.members[0].model_selection.mode,'default');const frozen=JSON.parse(fs.readFileSync(path.join(files().member,originalRef.directory+'.model.json'),'utf8'));assert(frozen.legacy);assert.equal(frozen.profile.model.api_key_env,'LEGACY_MIGRATION_KEY');assert.equal(frozen.profile.model.compat.supports_developer_role,false);
 const migratedBackend=JSON.parse(fs.readFileSync(backendPath,'utf8'));assert.equal(migratedBackend.configSHA256,originalBackend.configSHA256);assert.equal(migratedBackend.sessionId,originalBackend.sessionId);
 await dialogue('TEAMX_LEGACY_RESUMED_OK');checked('migration preserves original Session ID/fingerprint and resumes from private credentials without legacy export');
 await openSettings();await page.locator('#settings-tab-models').click();await until(async()=>(await page.locator('#model-list').innerText()).includes('待验证'),'honest migration status');await page.screenshot({path:path.join(output,'imported.png'),fullPage:true});
 await page.locator('#model-list .model-row-main').first().click();assert(await page.locator('#catalog-api-key').isHidden());await page.locator('#model-edit-form summary').click();await page.locator('#catalog-max-tokens').fill('8192');await page.locator('#model-save').click();
 await until(async()=>page.evaluate(async()=>{const r=await fetch('/v1/models');const d=await r.json();return d.operations?.some(o=>o.status==='completed')&&d.catalog.entries[0].revision===2;}),'verified managed revision');
 const nextRef=JSON.parse(fs.readFileSync(path.join(files().member,'session.json'),'utf8'));assert.notEqual(nextRef.directory,originalRef.directory);const target=JSON.parse(fs.readFileSync(path.join(files().member,nextRef.directory+'.model.json'),'utf8'));assert(!target.legacy);assert.equal(target.profile.model.api_key_env,'OPENAI_API_KEY');assert.equal(target.profile.model.max_tokens,8192);
 await page.locator('#close-settings').click();await dialogue('TEAMX_MIGRATED_NEW_SESSION_OK');checked('browser repairs imported model and subsequent real execution uses the new managed generation');
 await stop();env.LEGACY_MIGRATION_KEY='';await login(await start());await dialogue('TEAMX_MIGRATED_RESTART_OK');checked('repaired model and private key remain usable after another restart despite empty host exports');
 assert(report.requests.length>0&&report.requests.every(r=>r.credential_matches&&!r.developer));assert.equal(errors.length,0,errors.join('\n'));report.browser=browser.version();report.status='passed';
}catch(error){report.error=error.stack;if(page)await page.screenshot({path:path.join(output,'failure.png'),fullPage:true});throw error;}
finally{if(browser)await browser.close();try{await stop();}catch(error){report.cleanup_error=error.message;}if(proxy)await new Promise(r=>proxy.close(r));fs.writeFileSync(path.join(output,'report.json'),JSON.stringify(report,null,2),{mode:0o600});}
