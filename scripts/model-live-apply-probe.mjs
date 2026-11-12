// Real DSH/provider/browser acceptance. A new home only; the selected source
// supplies a managed model and key without printing it or copying business data.
import fs from 'node:fs';
import path from 'node:path';
import http from 'node:http';
import {spawn} from 'node:child_process';
import {createRequire} from 'node:module';
import {createHash} from 'node:crypto';
import {Readable} from 'node:stream';
import assert from 'node:assert/strict';
const {chromium}=createRequire(import.meta.url)('playwright');
const [outputArg,sourceArg,mode]=process.argv.slice(2),output=path.resolve(outputArg),source=path.resolve(sourceArg),home=path.join(output,'home');
assert(fs.existsSync(path.join(output,'teamx')),'build current binary at output/teamx');assert(!fs.existsSync(home),'requires a fresh output/home');
const config=fs.readFileSync(path.join(source,'config.toml'),'utf8');
const value=k=>{const match=config.match(new RegExp(`^${k} = ['"]([^'"\\n]*)['"]`,'m'));assert(match,`source ${k} unavailable`);return match[1];};
fs.mkdirSync(home,{mode:0o700});
fs.cpSync(path.join(source,'model-credentials'),path.join(home,'model-credentials'),{recursive:true});
fs.cpSync(path.join(source,'model-validations'),path.join(home,'model-validations'),{recursive:true});
for(const dir of ['model-credentials','model-validations']){fs.chmodSync(path.join(home,dir),0o700);for(const file of fs.readdirSync(path.join(home,dir)))fs.chmodSync(path.join(home,dir,file),0o600);}
fs.writeFileSync(path.join(home,'config.toml'),`[runtime.dsh]\nbinary_path = ${JSON.stringify(value('binary_path'))}\n[server]\ndata_dir = 'data'\nteams_dir = 'teams'\ndsh_turn_timeout = '3m'\n${config.slice(config.indexOf('[models]'))}`,{mode:0o600});
const env={...process.env,OPENAI_API_KEY:''};for(const k of Object.keys(env))if(/KEY|TOKEN|SECRET|DSH|TEAMX/.test(k)&&k!=='OPENAI_API_KEY')delete env[k];
const report={status:'failed',scope:'online model entries; same real upstream model with changed route and max_tokens; browser/default/fixed/restart',checks:[],requests:[]};
const key=fs.readFileSync(path.join(source,'model-credentials',value('credential_ref')),'utf8');
let server,browser,proxy,page;
async function until(fn,label,timeout=150000){const end=Date.now()+timeout;while(Date.now()<end){const result=await fn();if(result)return result;await new Promise(r=>setTimeout(r,150));}throw Error(`Timed out: ${label}`);}
function checked(label){report.checks.push(label);process.stdout.write(label+'\n');}
async function start(){let logs='';const child=spawn(path.join(output,'teamx'),['serve','--home',home,'--listen','127.0.0.1:0'],{env,stdio:['ignore','pipe','pipe']});const exit=new Promise(resolve=>child.on('exit',(code,signal)=>resolve({code,signal})));server={child,exit};child.stdout.on('data',b=>logs+=b);child.stderr.on('data',b=>logs+=b);server.logs=()=>logs;return until(()=>{assert.equal(child.exitCode,null,logs);for(const line of logs.split('\n')){try{const data=JSON.parse(line);if(data.endpoint)return data.endpoint;}catch{}}},'startup',30000);}
async function stop(){if(!server)return;server.child.kill('SIGTERM');const result=await server.exit;fs.writeFileSync(path.join(output,'server.log'),server.logs(),{mode:0o600});server=undefined;assert.equal(result.code,0,'server clean shutdown');}
function memberRoot(){const index=JSON.parse(fs.readFileSync(path.join(home,'data/rooms.json'),'utf8'));const room=index.rooms.find(r=>r.is_direct);const dir=path.join(home,'data/rooms',room.room_id);const saved=JSON.parse(fs.readFileSync(path.join(dir,'room-config.json'),'utf8'));return path.join(dir,'agents',saved.members[0].membership_id);}
function turnNumber(){const file=path.join(memberRoot(),'agent.json');return fs.existsSync(file)?JSON.parse(fs.readFileSync(file,'utf8')).turn_number:0;}
async function waitTurn(previous=0){await until(()=>{const file=path.join(memberRoot(),'agent.json');if(!fs.existsSync(file))return false;const state=JSON.parse(fs.readFileSync(file,'utf8'));return state.turn_number>previous&&state.turn?.status==='completed';},'turn completion');}
try{
 proxy=http.createServer(async(req,res)=>{try{const chunks=[];for await(const chunk of req)chunks.push(chunk);const body=Buffer.concat(chunks);let payload={};try{payload=JSON.parse(body);}catch{}
 report.requests.push({model:payload.model,max_tokens:payload.max_tokens??payload.max_completion_tokens,token_parameters:Object.fromEntries(Object.entries(payload).filter(([k])=>/token/i.test(k))),developer:payload.messages?.some(m=>m.role==='developer'),credential_sha256:createHash('sha256').update(req.headers.authorization||'').digest('hex')});
 const upstream=await fetch(value('base_url').replace(/\/$/,'')+req.url.replace(/^\/v1/,''),{method:req.method,headers:{'content-type':'application/json',authorization:req.headers.authorization},body:req.method==='GET'?undefined:body});
 res.writeHead(upstream.status,{'content-type':upstream.headers.get('content-type')||'application/json'});if(upstream.body)Readable.fromWeb(upstream.body).pipe(res);else res.end();
 }catch{res.writeHead(502);res.end('{"error":{"message":"acceptance proxy unavailable"}}');}});
 await new Promise(resolve=>proxy.listen(0,'127.0.0.1',resolve));const base=`http://127.0.0.1:${proxy.address().port}/v1`;
 const endpoint=await start();browser=await chromium.launch({headless:true});report.browser=browser.version();
 page=await browser.newPage({viewport:{width:1400,height:1100}});const errors=[];report.browser_errors=errors;page.on('pageerror',e=>errors.push(e.message));
 await page.goto(endpoint);await page.locator('#token').fill(fs.readFileSync(path.join(home,'auth/admin-token'),'utf8').trim());await page.locator('#login-form button').click();await page.locator('#message-input').waitFor();
 const read=()=>page.evaluate(async()=>{const r=await fetch('/v1/models');assertResponse(r);return r.json();function assertResponse(r){if(!r.ok)throw Error('models read failed');}});
 await page.locator('#message-input').fill('请通过 send 在会话中仅回复 TEAMX_LIVE_A_OK，无需外部平台操作。');await page.locator('#send').click();await waitTurn();checked('original A Session completes a real dialogue before replacement');
 await page.locator('#open-settings').click();await page.locator('#settings-tab-models').click();await page.locator('#model-new').waitFor();await page.locator('#model-new').click();
 await page.locator('#model-entry-name').fill('在线模型 B');await page.locator('#catalog-base-url').fill(base);await page.locator('#catalog-model-id').fill(value('model_id'));await page.locator('#catalog-api-key').fill(key);
 await page.locator('#model-edit-form summary').click();await page.locator('#catalog-developer-role').selectOption('false');await page.locator('#catalog-max-tokens').fill('8192');
 await page.locator('#model-save').click();await page.reload();await page.locator('#message-input').waitFor();
 await until(async()=>{const d=await read();return d.catalog.entries.some(e=>e.name==='在线模型 B')&&d.operations.every(o=>o.status==='completed');},'add B verified');checked('browser adds and validates B; refresh retains asynchronous operation');
 await page.locator('#open-settings').click();await page.locator('#settings-tab-models').click();await page.locator('#model-list').getByRole('button',{name:'设为默认',exact:true}).click();await page.getByRole('button',{name:'验证并设为默认',exact:true}).click();
 const switched=await until(async()=>{const d=await read();return d.catalog.default!=='daily'&&d.operations.every(o=>o.status==='completed')&&d;},'default propagation');
 const bID=switched.catalog.default;assert.equal(switched.members[0].selection.mode,'default');assert.equal(switched.members[0].actual.entry_id,bID);assert.equal(switched.members[0].actual.pending,true);
 await page.screenshot({path:path.join(output,'models.png'),fullPage:true});checked('default propagation commits target generation without an unsolicited turn');
 await page.locator('#close-settings').click();
 const beforeBTurn=turnNumber();await page.locator('#message-input').fill('请使用 send 在会话中仅回复 TEAMX_LIVE_B_OK，这是模型配置验收，不需要外部平台操作。');await page.locator('#send').click();await waitTurn(beforeBTurn);await until(async()=>{const text=await page.locator('#messages').innerText();return text.split('TEAMX_LIVE_B_OK').length>=3;},'published B reply');
 assert(report.requests.some(r=>r.model===value('model_id')&&!r.developer));const targetRef=JSON.parse(fs.readFileSync(path.join(memberRoot(),'session.json'),'utf8'));const targetSnapshot=JSON.parse(fs.readFileSync(path.join(memberRoot(),targetRef.directory+'.model.json'),'utf8'));assert.equal(targetSnapshot.profile.model.max_tokens,8192);checked('real subsequent Session reaches B route with system compatibility; generation binds output limit 8192');
 // Pin the same current model: reference changes without another generation.
 const before=JSON.parse(fs.readFileSync(path.join(memberRoot(),'session.json'),'utf8')).directory;
 // Direct conversations expose member detail through the assistant avatar.
 const detailButton=page.locator('[data-activity-member]').first();await detailButton.click();await page.locator('#member-model-selection').waitFor();await page.locator('#member-model-selection').selectOption(bID);
 await until(async()=>{const d=await read();return d.members[0].selection.mode==='explicit'&&d.operations.every(o=>o.status==='completed');},'pin B');
 assert.equal(JSON.parse(fs.readFileSync(path.join(memberRoot(),'session.json'),'utf8')).directory,before);checked('member pins current B without rotating');
 await page.locator('#open-settings').click();await page.locator('#settings-tab-models').click();await page.locator('#model-list').getByRole('button',{name:'设为默认',exact:true}).click();await page.getByRole('button',{name:'验证并设为默认',exact:true}).click();await until(async()=>{const d=await read();return d.catalog.default==='daily'&&d.operations.every(o=>o.status==='completed');},'default A keeps explicit B');
 assert.equal(JSON.parse(fs.readFileSync(path.join(memberRoot(),'session.json'),'utf8')).directory,before);checked('default returns to A while explicit B retains its Session');
 await page.locator('#close-settings').click();await page.screenshot({path:path.join(output,'member.png'),fullPage:true});
 await stop();const endpoint2=await start();await page.goto(endpoint2);await page.locator('#token').fill(fs.readFileSync(path.join(home,'auth/admin-token'),'utf8').trim());await page.locator('#login-form button').click();await page.locator('#message-input').waitFor();
 const beforeRequests=report.requests.length, previousTurn=turnNumber();await page.locator('#message-input').fill('请使用 send 在会话中仅回复 TEAMX_LIVE_RESTART_OK。');await page.locator('#send').click();await waitTurn(previousTurn);await until(()=>report.requests.length>beforeRequests,'real B after restart');checked('restart keeps fixed B and its managed key despite empty host key');
 if(mode==='delivery'){
  report.scope+='; delete default with default/fixed references, real post-delete and restart dialogue';
  await page.locator('[data-activity-member]').first().click();await page.locator('#member-model-selection').waitFor();await page.locator('#member-model-selection').selectOption('daily');
  await until(async()=>{const d=await read();return d.members[0].selection.entry_id==='daily'&&d.operations.every(o=>o.status==='completed');},'fixed A before delete');
  await page.evaluate(async()=>{const catalog=await(await fetch('/v1/rooms')).json();const session=await(await fetch('/v1/access/session')).json();const response=await fetch('/v1/rooms/create',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({service_epoch:catalog.service_epoch,caller:{kind:'human',id:session.operator},is_direct:true,agent_role_id:'assistant',create_id:'deletion-follower'})});if(!response.ok)throw Error('create default follower failed: '+await response.text());});
  const oldGeneration=JSON.parse(fs.readFileSync(path.join(memberRoot(),'session.json'),'utf8')).directory;
  await page.locator('#open-settings').click();await page.locator('#settings-tab-models').click();await page.locator('#model-list .model-row').filter({hasText:' · 默认'}).getByRole('button',{name:'删除',exact:true}).click();await page.locator('#model-delete-replacement').selectOption(bID);await page.getByRole('button',{name:'验证并删除、替换引用',exact:true}).click();
  const deleted=await until(async()=>{const d=await read();return !d.catalog.entries.some(e=>e.id==='daily')&&d.operations.every(o=>o.status==='completed')&&d;},'delete A and replace every reference');
  assert.equal(deleted.catalog.default,bID);assert(deleted.members.some(m=>m.selection.mode==='default'));assert(deleted.members.some(m=>m.selection.mode==='explicit'&&m.selection.entry_id===bID));assert(deleted.members.every(m=>m.target_entry_id===bID));assert(fs.existsSync(path.join(memberRoot(),oldGeneration+'.model.json')));
  await until(async()=>{const names=await page.locator('#model-list .model-row strong').allTextContents();return names.length===1&&names[0].includes('在线模型 B')&&names[0].includes('默认');},'rendered final deletion status');
  await page.screenshot({path:path.join(output,'deleted.png'),fullPage:true});await page.locator('#close-settings').click();
  // The sidebar member panel may still cover the composer; close it explicitly.
  const closeMember=page.locator('#close-activity');if(await closeMember.isVisible())await closeMember.click();
  const deletionTurn=turnNumber();await page.locator('#message-input').fill('请使用 send 在会话中仅回复 TEAMX_DELETION_OK。');await page.locator('#send').click();await waitTurn(deletionTurn);checked('browser deletes default A; default/fixed references reach B and a subsequent real turn completes');
  const generation=JSON.parse(fs.readFileSync(path.join(memberRoot(),'session.json'),'utf8')).directory;
  await stop();await page.goto(await start());await page.locator('#token').fill(fs.readFileSync(path.join(home,'auth/admin-token'),'utf8').trim());await page.locator('#login-form button').click();await page.locator('#message-input').waitFor();
  const restartTurn=turnNumber();await page.locator('#message-input').fill('请使用 send 在会话中仅回复 TEAMX_DELETION_RESTART_OK。');await page.locator('#send').click();await waitTurn(restartTurn);assert.equal(JSON.parse(fs.readFileSync(path.join(memberRoot(),'session.json'),'utf8')).directory,generation);checked('deletion restart preserves completed generation and continues real execution');
 }
 assert.equal(errors.length,0,errors.join('\n'));report.status='passed';report.remote_platform_writes=0;
} catch(error){report.error=error.stack;if(page){report.page_url=page.url();report.page_text=(await page.locator('body').innerText()).slice(0,5000);await page.screenshot({path:path.join(output,'failure.png'),fullPage:true});}throw error;} finally {if(browser)await browser.close();try{await stop();}catch(error){report.cleanup_error=error.message;}if(proxy)await new Promise(resolve=>proxy.close(resolve));fs.writeFileSync(path.join(output,'report.json'),JSON.stringify(report,null,2),{mode:0o600});}
