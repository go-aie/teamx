import {test} from 'node:test';
import assert from 'node:assert/strict';
import {launch,settle} from './web_app_harness.mjs';
import {fixture as baseFixture} from './web_member_platform_fixture.mjs';
function fixture(){
 const api=baseFixture(),original=api.request.bind(api);
 api.messages=[{seq:1,author:{kind:'human',id:'tester'},text:'连接飞书',created_at:'2026-11-11T01:00:00Z'}];
 api.op={operation_id:'a'.repeat(32),room_id:'R',room_instance:'instance-R',agent_id:'a',membership_id:'member-a',source_seq:1,name:'工作飞书',version:1,result_version:0,status:'waiting',stage:'application',target_available:true,application:{status:'pending'},authorization:{status:'pending'},connection:{status:'pending'},interaction:{id:'a'.repeat(32),kind:'lark_authorization',authorization_url:'https://open.feishu.cn/page/cli?temporary',qrcode_url:'/v1/platforms/lark/qrcode?opaque'}};
 api.request=async(path,body,...rest)=>{
  if(path==='/v1/read')return {messages:api.messages.filter(m=>m.seq>(body.after||0)),snapshot_seq:api.messages.length,cleared_through:0,has_more:false};
  if(path.startsWith('/v1/platforms/interactions/') || path==='/v1/platforms/gitlab/connect'){
   api.calls.push({path,body:structuredClone(body)});
   if(api.interactionHook){const result=api.interactionHook(path,body);if(result!==undefined)return result;}
   if(path.endsWith('/list'))return {interactions:body.room_instance==='instance-R' ? [structuredClone(api.op)] : []};
   if(path.endsWith('/cancel')){api.op.status='cancelled';api.op.version++;api.op.result_version=1;api.op.interaction={id:api.op.operation_id,kind:'lark_authorization'};return structuredClone(api.op);}
  }
  return original(path,body,...rest);
 };
 // The base fixture's operator is tester.
 return api;
}
const card=ui=>ui.document.getElementById('message-1').querySelector('.message-interactions').children[0];
const link=card=>card.children[5].children[0];
const cancel=card=>card.children[5].children[1];
const writes=api=>api.calls.filter(c=>c.path==='/v1/platforms/interactions/cancel');

test('subscribed completion distinguishes pending delivery, scheduler blocks, consumption and handled results',async()=>{
 const api=fixture(),ui=await launch(api);await settle();
 api.op.continuation={mode:'inbox',state:'waiting_result'};
 await ui.refresh();await settle();assert.match(card(ui).children[6].textContent,/已订阅.*Inbox/);
 api.op.status='completed';api.op.connection.status='connected';api.op.result_version=1;
 for(const [reason,text] of [['inbox_capacity','容量不足'],['member_paused','Agent 已暂停'],['inbox_admission_blocked','来源核验受阻'],['session_rotating','轮换 Session'],['member_failed','上次回合失败']]) {
  api.op.continuation={mode:'inbox',state:'delivered',blocked_reason:reason};
  await ui.refresh();await settle();assert.match(card(ui).children[1].textContent,/已连接/);assert.match(card(ui).children[6].textContent,new RegExp(`等待 Agent 接续.*${text}.*发消息`));
 }
 api.op.continuation={mode:'inbox',state:'consumed'};await ui.refresh();await settle();assert.match(card(ui).children[6].textContent,/已读取.*等待接续/);
 api.op.continuation={mode:'inbox',state:'handled',handled_version:1};await ui.refresh();await settle();assert.match(card(ui).children[6].textContent,/已核对.*需求以对话/);
 api.op.continuation.mode='none';await ui.refresh();await settle();assert.match(card(ui).children[6].textContent,/已核对.*需求以对话/);
 assert.equal(writes(api).length,0);
});

test('connection is available immediately, progress and new messages retain component and focus, reload restores the same reference',async()=>{
 const api=fixture(),saved=new Map(),ui=await launch(api,saved);await settle();
 const first=card(ui),authorize=link(first);assert.equal(first.dataset.interactionId,api.op.operation_id);assert.equal(authorize.hidden,false);assert.equal(first.children[4].hidden,false);
 authorize.focus();api.messages.push({seq:2,author:{kind:'agent',id:'a'},text:'请完成授权',created_at:'2026-11-11T01:01:00Z'});api.op.stage='authorization';api.op.version++;api.op.application.status='applied';api.op.interaction.authorization_url='https://accounts.feishu.cn/oauth?second';
 await ui.refresh();await settle();assert.equal(card(ui),first);assert.equal(link(card(ui)),authorize);assert.equal(ui.document.activeElement,authorize);assert.match(authorize.href,/second/);assert.match(first.children[4].src,/stage=authorization$/);assert.match(first.children[2].textContent,/应用已完成.*连接待完成/);
 const reloaded=await launch(api,saved);await settle();assert.equal(card(reloaded).dataset.interactionId,api.op.operation_id);assert.match(link(card(reloaded)).href,/second/);
 api.op.status='completed';api.op.connection.status='connected';api.op.authorization.status='applied';api.op.result_version=1;api.op.version++;api.op.account_id='ou_verified';
 await reloaded.refresh();await settle();assert.equal(link(card(reloaded)).hidden,true);assert.equal(card(reloaded).children[4].hidden,true);assert.match(card(reloaded).children[1].textContent,/已连接/);assert.match(card(reloaded).children[3].textContent,/ou_verified/);assert.equal(writes(api).length,0);
 assert.equal([...saved.values()].some(v=>v.includes('temporary')||v.includes('second')),false);
});

test('full settings shares the operation and closing settings or the page does not cancel it',async()=>{
 const api=fixture(),ui=await launch(api);await settle();
 await ui.el('open-settings').emit('click');await ui.el('settings-tab-platforms').emit('click');
 assert.equal(ui.el('conversation-connections-section').hidden,false);assert.equal(ui.el('conversation-connections').children[0].dataset.interactionId,card(ui).dataset.interactionId);
 await ui.el('close-settings').emit('click');await ui.window.emit('pagehide');assert.equal(writes(api).length,0);assert.equal(api.op.status,'waiting');
 await cancel(card(ui)).emit('click');await settle();assert.equal(writes(api).length,1);assert.equal(writes(api)[0].body.room_instance,'instance-R');assert.equal(writes(api)[0].body.version,1);assert.equal(cancel(card(ui)).hidden,true);
});

test('duplicate clicks share one request; an unknown cancel is read back without mutation replay',async()=>{
 const api=fixture(),ui=await launch(api);await settle();let reject;
 api.interactionHook=path=>path.endsWith('/cancel') ? new Promise((_,r)=>reject=r) : undefined;
 const first=cancel(card(ui)),pending=first.emit('click');await settle();await first.emit('click');assert.equal(writes(api).length,1);
 api.op.status='cancelled';api.op.version++;api.op.result_version=1;reject(Error('lost receipt'));await pending;await settle();assert.equal(cancel(card(ui)).hidden,true);assert.equal(writes(api).length,1);assert.match(card(ui).children[1].textContent,/已取消/);
});

test('failed status observations disable old authorization material and retry read-only',async()=>{
 const api=fixture(),ui=await launch(api);await settle();api.interactionHook=path=>{if(path.endsWith('/list'))throw Error('offline');};
 await ui.refresh();await settle();assert.equal(link(card(ui)).hidden,true);assert.equal(card(ui).children[4].hidden,true);assert.match(ui.el('platform-interaction-error').textContent,/无法读取/);
 api.interactionHook=null;await ui.refresh();await settle();assert.equal(link(card(ui)).hidden,false);assert.equal(writes(api).length,0);
 api.op.interaction.authorization_url='javascript:alert(1)';api.op.interaction.qrcode_url='https://foreign.invalid/pixel';await ui.refresh();await settle();assert.equal(link(card(ui)).hidden,true);assert.equal(card(ui).children[4].hidden,true);
});

test('a late cancel or list response cannot mount the old Room interaction in a new Room',async()=>{
 const api=fixture(),original=api.request.bind(api),other={room_id:'B',room_instance:'instance-B',name:'Other',status:'ready',service:{room_id:'B',room_instance:'instance-B',service_epoch:'runtime-B'}};
 api.request=async(path,body,...args)=>{
  if(path==='/v1/rooms'){const catalog=await original(path,body,...args);return {...catalog,rooms:[...catalog.rooms,other].map(r=>({...r,runtime_status:"ready"}))};}
  if(path==='/v1/read' && body.room_instance==='instance-B')return {messages:[],snapshot_seq:0,has_more:false,cleared_through:0};
  return original(path,body,...args);
 };
 const ui=await launch(api);await settle();let resolve;
 api.interactionHook=path=>path.endsWith('/cancel') ? new Promise(r=>resolve=r) : undefined;
 const pending=cancel(card(ui)).emit('click');await settle();await ui.select('Other');await ui.refresh();await settle();
 assert.equal(ui.el('conversation-connections-section').hidden,true);resolve({...structuredClone(api.op),status:'cancelled',version:2,result_version:1});await pending;await settle();
 assert.equal(ui.el('conversation-connections').children.length,0);assert.equal(ui.el('messages').children.length,0);assert.equal(writes(api).length,1);assert.equal(writes(api)[0].body.room_instance,'instance-R');
 await ui.select('Review');await ui.refresh();await settle();assert.equal(card(ui).dataset.interactionId,api.op.operation_id);
 const pendingReads=[];api.interactionHook=(path,body)=>path.endsWith('/list') ? new Promise(resolve=>pendingReads.push({resolve,body})) : undefined;
 await ui.refresh();await settle();await ui.select('Other');await ui.refresh();await settle();
 pendingReads.find(r=>r.body.room_instance==='instance-R').resolve({interactions:[structuredClone(api.op)]});await settle();
 assert.equal(ui.el('conversation-connections-section').hidden,true);assert.equal(ui.el('messages').children.length,0);
 pendingReads.find(r=>r.body.room_instance==='instance-B').resolve({interactions:[]});await settle();
});

test('GitLab fixed authorization bridges the original browser, retains nodes and hides the button on completion',async()=>{
 const api=fixture();api.op.interaction={id:api.op.operation_id,kind:'gitlab_authorization'};api.op.provider='gitlab';api.op.origin='https://gitlab.example';api.op.account_username='tester';api.op.stage='authorization';api.op.application.status='reused';api.op.continuation={mode:'inbox',state:'waiting_result'};
 const ui=await launch(api);await settle();const first=card(ui),authorize=link(first);assert.equal(authorize.hidden,false);assert.match(first.children[0].textContent,/GitLab/);assert.match(first.children[6].textContent,/gitlab.example.*tester.*Inbox/);assert.equal(first.children[4].hidden,true);
 const navigated=[];ui.window.open=()=>({opener:{},location:{replace:url=>navigated.push(url)},close(){}});
 let resolve;api.interactionHook=path=>path.endsWith('/gitlab/connect') ? new Promise(r=>resolve=r) : undefined;
 await authorize.emit('click');await settle();await authorize.emit('click');await settle();
 const writes=api.calls.filter(c=>c.path.endsWith('/gitlab/connect'));assert.equal(writes.length,1);assert.equal(writes[0].body.version,1);assert.equal(writes[0].body.caller.kind,'human');
 resolve({authorization_url:'https://gitlab.example/oauth/authorize?state=temporary'});await settle();assert.equal(navigated.length,1);assert.equal(card(ui),first);
 api.op.status='completed';api.op.result_version=1;api.op.version++;api.op.connection.status='connected';api.op.account_id='13';api.op.continuation={mode:'inbox',state:'delivered',blocked_reason:'member_paused'};
 await ui.refresh();await settle();assert.equal(link(card(ui)).hidden,true);assert.match(first.children[6].textContent,/已连接|连接已保存/);assert.match(first.children[6].textContent,/暂停/);
});

test('GitLab authorization rejects other origins and never falls back to a Feishu selection form',async()=>{
 const api=fixture();api.op.interaction={id:api.op.operation_id,kind:'gitlab_authorization'};api.op.origin='https://gitlab.example';api.op.stage='authorization';
 const ui=await launch(api);await settle();const navigated=[];let closed=0;ui.window.open=()=>({location:{replace:url=>navigated.push(url)},close(){closed++;}});
 api.interactionHook=path=>path.endsWith('/gitlab/connect') ? {authorization_url:'https://attacker.invalid/oauth/authorize'} : undefined;
 await link(card(ui)).emit('click');await settle();assert.equal(navigated.length,0);assert.equal(closed,1);assert.match(card(ui).children[7].textContent,/授权入口暂不可用/);
 await ui.el('open-settings').emit('click');await ui.el('settings-tab-platforms').emit('click');await ui.el('close-settings').emit('click');assert.equal(api.op.status,'waiting');
});

test('GitLab identity failures distinguish a browser binding from stale page identity',async()=>{
 const api=fixture();api.op.interaction={id:api.op.operation_id,kind:'gitlab_authorization'};api.op.origin='https://gitlab.example';api.op.stage='authorization';
 const ui=await launch(api);await settle();
 for(const [field,text] of [['service_epoch',/页面或请求身份.*刷新/],['browser_session',/最初发起授权的浏览器/]]){
  api.interactionHook=path=>{if(path.endsWith('/gitlab/connect'))throw Object.assign(new Error('identity failed'),{code:'identity',field});};
  await link(card(ui)).emit('click');await settle();assert.match(card(ui).children[7].textContent,text);
 }
});

test('GitLab first application settings return to the original operation and closing never cancels its browser flow',async()=>{
 const api=fixture();Object.assign(api.op,{provider:'gitlab',origin:'https://gitlab.example',account_username:'tester',name:'Work GitLab',setup:{origin:'https://gitlab.example',applications_url:'https://gitlab.example/-/user_settings/applications',redirect_uri:'http://localhost:8088/v1/platforms/gitlab/callback',scopes:['api'],applications:[]},interaction:{id:api.op.operation_id,kind:'gitlab_authorization'}});
 const request=api.request.bind(api);
 api.request=async(path,body,...rest)=>{
  if(path.endsWith('/gitlab/prepare')){api.calls.push({path,body:structuredClone(body)});return {status:'configuration_required',redirect_uri:api.op.setup.redirect_uri,scopes:['api'],applications_url:api.op.setup.applications_url};}
  if(path.endsWith('/gitlab/initialize')){api.calls.push({path,body:structuredClone(body)});return {status:'owner_verification_required',client_import_id:'b'.repeat(32),client_id:body.client.client_id,scopes:['api']};}
  return request(path,body,...rest);
 };
 const ui=await launch(api);await settle();
 const original=card(ui);assert.match(link(original).textContent,/配置 GitLab 应用/);assert.match(original.children[6].textContent,/localhost:8088.*api/);assert.equal(original.children[8].href,api.op.setup.applications_url);
 await link(original).emit('click');await settle();
 assert.equal(ui.el('connection-form').hidden,false);assert.equal(ui.el('connection-url').value,api.op.origin);assert.equal(ui.el('connection-url').readOnly,true);assert.equal(ui.el('connection-name').value,'Work GitLab');assert.equal(ui.el('connection-provider').disabled,true);
 assert.equal(ui.el('gitlab-scope-write-repository').checked,false);
 await ui.el('close-settings').emit('click');await settle();assert.equal(writes(api).length,0);assert.equal(api.op.status,'waiting');
 await link(original).emit('click');await settle();
 ui.el('gitlab-client-id').value='private-id';ui.el('gitlab-client-secret').value='private-secret';
 const navigated=[];ui.window.open=()=>({location:{replace:url=>navigated.push(url)},close(){}});
 api.interactionHook=path=>path.endsWith('/gitlab/connect') ? {authorization_url:'https://gitlab.example/oauth/authorize?state=fixture'} : undefined;
 await ui.el('gitlab-import').emit('click');await settle();
 const bind=api.calls.filter(c=>c.path.endsWith('/gitlab/connect'));assert.equal(bind.length,1);assert.equal(bind[0].body.operation_id,api.op.operation_id);assert.equal(bind[0].body.version,1);assert.equal(bind[0].body.client_import_id,'b'.repeat(32));assert.equal(navigated.length,1);
 assert.equal(ui.el('gitlab-client-secret').value,'');assert.equal(card(ui),original);
 await ui.el('close-settings').emit('click');await settle();
 assert.equal(api.calls.some(c=>c.path.endsWith('/gitlab/login') || c.path.endsWith('/gitlab/cancel') || c.path.endsWith('/onboarding/prepare')),false);
});
