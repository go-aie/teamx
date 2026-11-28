import {test} from 'node:test';
import assert from 'node:assert/strict';
import {launch,settle} from './web_app_harness.mjs';
import {fixture as baseFixture} from './web_member_platform_fixture.mjs';
function fixture(kind='groups') {
 const api=baseFixture(),original=api.request.bind(api);
 api.op={operation_id:'b'.repeat(32),room_id:'R',room_instance:'instance-R',agent_id:'a',membership_id:'member-a',source_seq:1,source:{seq:1},input:kind==='account' ? {kind} : {kind,platform_id:'lark',platform_version:1,query:'产品'},draft:kind==='account' ? {name:'工作飞书'} : {},version:1,result_version:0,status:'waiting',target_available:true,interaction:{id:'b'.repeat(32),kind:'platform_'+kind},continuation:{mode:'inbox',state:'waiting_result'}};
 api.messages=[{seq:1,author:{kind:'human',id:'tester'},text:'连接飞书并关注产品讨论群',created_at:'2026-11-11T01:00:00Z'}];
 api.request=async(path,body,...rest)=>{
  if(path==='/v1/read')return {messages:api.messages.filter(m=>m.seq>(body.after||0)),snapshot_seq:api.messages.length,cleared_through:0,has_more:false};
  if(path==='/v1/platforms/interactions/list')return {interactions:[],selections:[structuredClone(api.op)]};
  if(path.startsWith('/v1/platforms/selection-interactions/')){
   api.calls.push({path,body:structuredClone(body)});
   if(api.hook){const result=api.hook(path,body);if(result!==undefined)return result;}
   if(path.endsWith('/candidates'))return kind==='account' ? [{id:'account1',name:'工作',version:7,account:{id:{value:'ou_work'}}},{id:'account2',name:'工作',version:9,account:{id:{value:'ou_other'}}}] : {chats:body.page_token ? [{chat_id:'oc_three',name:'产品讨论群',chat_mode:'topic'}] : [{chat_id:'oc_one',name:'产品讨论群',chat_mode:'group'},{chat_id:'oc_two',name:'产品讨论群',chat_mode:'group'}],has_more:!body.page_token,page_token:body.page_token ? '' : 'page2',search_warnings:[{source:'bot',code:'permission_denied'}]};
   if(body.version!==api.op.version)throw Object.assign(Error('changed'),{code:'conflict'});
   if(path.endsWith('/update')){api.op.draft=structuredClone(body.draft);api.op.version++;return structuredClone(api.op);}
   if(path.endsWith('/submit') || path.endsWith('/cancel')){api.op.submitted_version=api.op.version;api.op.version++;api.op.status=path.endsWith('/submit') ? 'completed' : 'cancelled';api.op.result_version=1;api.op.continuation.state='pending_delivery';return structuredClone(api.op);}
  }
  return original(path,body,...rest);
 };
 return api;
}
const card=ui=>ui.document.getElementById('message-1').querySelector('.message-interactions').children[0];
const candidates=el=>el.children[4];
const controls=el=>el.children[7].children;
const writes=api=>api.calls.filter(c=>/\/(update|submit|cancel)$/.test(c.path));

test('Scion account choices carry exact connection versions and lead missing accounts to settings',async()=>{
 const api=fixture('account');api.op.input={provider:'scion',kind:'account'};api.op.draft={};
 api.hook=path=>path.endsWith('/candidates') ? [{id:'account1',name:'工作',version:7,account:{id:{value:'alice'},instance:'https://one.example'}},{id:'account2',name:'工作',version:9,account:{id:{value:'alice'},instance:'https://two.example'}}] : undefined;
 const ui=await launch(api);await settle();const el=card(ui);
 assert.equal(el.children[0].textContent,'选择 Scion 账号');
 assert.equal(el.children[2].children.length,0);
 assert.equal(candidates(el).children.length,2);
 assert.match(el.children[6].textContent,/完整设置登录/);
 assert.match(candidates(el).children[1].children[1].textContent,/https:\/\/two.example/);
 const input=candidates(el).children[1].children[0];input.checked=true;await input.emit('change');
 await controls(el)[0].emit('click');await settle();
 assert.equal(api.op.draft.platform_id,'account2');assert.equal(api.op.draft.platform_version,9);
 assert.equal(api.calls.some(c=>/member\/(tool|notifications)|connect\/start/.test(c.path)),false);
});

test('a page-only selection result does not promise automatic continuation',async()=>{
 const api=fixture();api.op.status='expired';api.op.result_version=1;api.op.continuation={mode:'inbox',state:'page_only'};
 const ui=await launch(api);await settle();
 assert.match(card(ui).children[8].textContent,/仅在页面提示.*不会自动接续/);
 assert.equal(writes(api).length,0);
});
test('a selection receipt immediately refreshes its Room event without an Agent update',async()=>{
 const api=fixture('account'),original=api.request.bind(api);
 api.request=async(path,body,...rest)=>{
  const result=await original(path,body,...rest);
  if(path.endsWith('/selection-interactions/submit')) {
   api.messages.push({seq:2,type:'event',author:{kind:'human',id:'tester'},text:'选择结果已更新',created_at:'2026-11-11T01:01:00Z'});
   result.continuation={mode:'inbox',state:'delivered',room_seq:2};
  }
  return result;
 };
 const ui=await launch(api);await settle();
 await controls(card(ui))[0].emit('click');await settle();
 const event=ui.document.getElementById('message-2');
 assert.equal(event?.className,'room-event');assert.equal(event.textContent,'选择结果已更新');
 assert.equal(event.children.length,0);
});
test('groups use exact choices, pagination and fixed fields; repeated click applies once',async()=>{
 const api=fixture(),ui=await launch(api);await settle();const el=card(ui);
 assert.equal(candidates(el).children.length,2);assert.match(el.children[9].textContent,/权限不足/);
 const input=candidates(el).children[1].children[0];input.checked=true;await input.emit('change');
 await el.children[5].emit('click');await settle();assert.equal(candidates(el).children.length,3);assert.equal(input.checked,true);
 const mode=el.children[2].children[1].children[1];mode.value='mentions';await mode.emit('change');
 await Promise.all([controls(el)[0].emit('click'),controls(el)[0].emit('click')]);await settle();
 assert.equal(writes(api).filter(c=>c.path.endsWith('/submit')).length,1);assert.deepEqual(api.op.draft.chat_ids,['oc_two']);assert.equal(api.op.draft.group_message_mode,'mentions');assert.match(el.children[1].textContent,/已保存/);assert.match(el.children[8].textContent,/等待 Agent 接续/);
});
test('group summaries show names and retain selected labels across searches; IDs remain in tooltips',async()=>{
 const api=fixture(),ui=await launch(api);await settle();const el=card(ui);
 const choice=candidates(el).children[0];
 assert.equal(choice.children[1].textContent,'产品讨论群');assert.equal(choice.title,'oc_one');
 choice.children[0].checked=true;await choice.children[0].emit('change');
 assert.equal(el.children[6].textContent,'已选 1 个群：产品讨论群');assert.equal(el.children[6].title,'oc_one');
 api.hook=path=>path.endsWith('/candidates') ? {chats:[],has_more:false} : undefined;
 await el.children[3].emit('click');await settle();
 assert.equal(candidates(el).children.length,0);assert.equal(el.children[6].textContent,'已选 1 个群：产品讨论群');
 api.op.status='completed';api.op.version++;api.op.draft={chat_ids:['oc_one']};api.op.chats=[{chat_id:'oc_one',name:'产品讨论群'}];
 const reloaded=await launch(api);await settle();
 assert.equal(card(reloaded).children[6].textContent,'已选 1 个群：产品讨论群');
});
test('local input and focus survive polling, new messages and natural-language draft conflict',async()=>{
 const api=fixture('account'),ui=await launch(api);await settle();const el=card(ui),name=el.children[2].children[0].children[1];name.value='手动名称';name.focus();await name.emit('input');
 api.messages.push({seq:2,author:{kind:'human',id:'tester'},text:'新消息',created_at:'2026-11-11T01:01:00Z'});api.op.version++;api.op.draft.name='自然语言名称';await ui.refresh();await settle();
 assert.equal(card(ui),el);assert.equal(name.value,'手动名称');assert.equal(ui.document.activeElement,name);assert.match(el.children[9].textContent,/输入仍保留/);
 await controls(el)[0].emit('click');await settle();assert.equal(api.op.status,'waiting');assert.equal(writes(api).filter(c=>c.path.endsWith('/submit')).length,0);assert.match(el.children[9].textContent,/草稿/);
 await controls(el)[2].emit('click');assert.equal(name.value,'自然语言名称');
});
test('multiple account candidates distinguish stable account IDs and selected version',async()=>{
 const api=fixture('account'),ui=await launch(api);await settle();const el=card(ui);
 assert.match(candidates(el).children[0].children[1].textContent,/ou_work/);assert.match(candidates(el).children[1].children[1].textContent,/ou_other/);
 const control=candidates(el).children[1].children[0];control.checked=true;await control.emit('change');await controls(el)[0].emit('click');await settle();
 assert.equal(api.op.draft.platform_id,'account2');assert.equal(api.op.draft.platform_version,9);assert.equal(api.op.draft.name,'工作');
});
test('global settings leave the selection and loaded candidates in the conversation',async()=>{
 const api=fixture(),ui=await launch(api);await settle();const el=card(ui);
 await el.children[5].emit('click');await settle();
 const control=candidates(el).children[2].children[0];control.checked=true;await control.emit('change');
 const reads=api.calls.filter(c=>c.path.endsWith('/candidates')).length;
 await controls(el)[3].emit('click');await settle();
 assert.equal(ui.el('settings-dialog').open,true);assert.equal(ui.el('platforms-list-view').hidden,false);
 assert.equal(ui.el('conversation-connections'),undefined);assert.equal(card(ui),el);
 await ui.el('close-settings').emit('click');await settle();assert.equal(api.op.status,'waiting');assert.equal(writes(api).length,0);assert.equal(control.checked,true);
 assert.equal(candidates(el).children.length,3);assert.equal(api.calls.filter(c=>c.path.endsWith('/candidates')).length,reads);
 await controls(el)[0].emit('click');await settle();assert.equal(api.op.status,'completed');assert.deepEqual(api.op.draft.chat_ids,['oc_three']);assert.match(el.children[1].textContent,/已保存/);
});
test('unknown submit response recovers the receipt without resubmitting',async()=>{
 const api=fixture('account'),ui=await launch(api);await settle();const el=card(ui);
 api.hook=(path,body)=>{if(path.endsWith('/submit')){api.op.version++;api.op.result_version=1;api.op.status='completed';throw Error('lost response');}};
 await controls(el)[0].emit('click');await settle();assert.equal(api.op.status,'completed');assert.equal(writes(api).length,1);assert.match(el.children[1].textContent,/已保存/);assert.equal(controls(el)[0].hidden,true);
});
test('expired and replaced forms disable input and retain result meaning',async()=>{
 const api=fixture(),ui=await launch(api);await settle();const el=card(ui);
 api.op.target_available=false;await ui.refresh();await settle();assert.equal(controls(el)[0].disabled,true);assert.match(el.children[8].textContent,/不会转交/);
 api.op.target_available=true;api.op.status='expired';api.op.version++;api.op.result_version=1;await ui.refresh();await settle();assert.equal(controls(el)[0].hidden,true);assert.match(el.children[1].textContent,/过期/);assert.equal(writes(api).length,0);
});

function gitlabFixture(kind='projects') {
 const api=fixture(kind),original=api.request.bind(api);
 api.op.input=kind==='account' ? {kind,provider:'gitlab',origin:'https://gitlab.example'} : {kind,provider:'gitlab',platform_id:'gitlab-account',platform_version:7,query:'Project'};
 api.op.draft={};api.messages[0].text='连接 GitLab 并关注项目';
 api.request=async(path,body,...rest)=>{
  if(path.endsWith('/selection-interactions/candidates')) {
   api.calls.push({path,body:structuredClone(body)});
   if(api.projectError)throw Object.assign(Error('denied'),{code:'permission_denied'});
   return kind==='account' ? [{id:'account1',name:'工作',version:7,username:'user1',account:{id:{value:'101'}}},{id:'account2',name:'工作',version:9,username:'user2',account:{id:{value:'102'}}}] : {projects:body.page_token ? [{id:8,name:'Project',path_with_namespace:'other/project'}] : [{id:7,name:'Project',path_with_namespace:'team/project'}],next_page:body.page_token ? 0 : 2};
  }
  return original(path,body,...rest);
 };
 return api;
}
test('GitLab account choices keep native identities and versions; no implicit new account',async()=>{
 const api=gitlabFixture('account'),ui=await launch(api);await settle();const el=card(ui);
 assert.match(el.children[0].textContent,/GitLab/);assert.equal(candidates(el).children.length,2);
 assert.match(candidates(el).children[1].textContent || candidates(el).children[1].children[1].textContent,/user2.*102/);
 const control=candidates(el).children[1].children[0];control.checked=true;await control.emit('change');await controls(el)[0].emit('click');await settle();
 assert.equal(api.op.draft.platform_id,'account2');assert.equal(api.op.draft.platform_version,9);assert.equal(api.op.draft.name,'工作');
});
test('GitLab projects distinguish full path and native ID across pages; settings preserve the conversation filter',async()=>{
 const api=gitlabFixture(),ui=await launch(api);await settle();const el=card(ui);
 assert.match(el.children[0].textContent,/项目/);assert.match(candidates(el).children[0].children[1].textContent,/team\/project.*#7/);
 await el.children[5].emit('click');await settle();assert.equal(candidates(el).children.length,2);assert.match(candidates(el).children[1].children[1].textContent,/other\/project.*#8/);
 assert.equal(api.calls.filter(c=>c.path.endsWith('/candidates')).at(-1).body.page_token,'2');
 const input=candidates(el).children[1].children[0];assert.equal(input.type,'radio');input.checked=true;await input.emit('change');
 const mode=el.children[2].children[1].children[1];mode.value='note';await mode.emit('change');
 api.messages.push({seq:2,author:{kind:'human',id:'tester'},text:'新消息',created_at:'2026-11-11T01:01:00Z'});await ui.refresh();await settle();assert.equal(card(ui),el);assert.equal(input.checked,true);
 await controls(el)[3].emit('click');await settle();await ui.el('close-settings').emit('click');await settle();assert.equal(api.op.status,'waiting');
 await Promise.all([controls(el)[0].emit('click'),controls(el)[0].emit('click')]);await settle();
 assert.equal(api.op.draft.project_id,8);assert.equal(api.op.draft.project_path,'other/project');assert.deepEqual(api.op.draft.events_filter,{types:['note']});assert.equal(api.op.draft.chat_ids,undefined);
 assert.equal(writes(api).filter(c=>c.path.endsWith('/submit')).length,1);assert.match(el.children[6].textContent,/other\/project.*#8/);
});
test('GitLab project permission errors preserve selected target and allow retry',async()=>{
 const api=gitlabFixture(),ui=await launch(api);await settle();const el=card(ui),input=candidates(el).children[0].children[0];input.checked=true;await input.emit('change');
 api.projectError=true;await el.children[3].emit('click');await settle();assert.match(el.children[9].textContent,/权限不足/);assert.match(el.children[6].textContent,/team\/project.*#7/);assert.equal(input.checked,true);assert.equal(writes(api).length,0);
 api.projectError=false;await el.children[3].emit('click');await settle();assert.equal(candidates(el).children[0].children[0].checked,true);
});
test('GitLab project local input survives natural-language conflict and explicit reload reconciles exact path',async()=>{
 const api=gitlabFixture(),ui=await launch(api);await settle();const el=card(ui),input=candidates(el).children[0].children[0];input.checked=true;await input.emit('change');
 api.op.draft={project_id:8,project_path:'other/project',events_filter:{types:['push','note']}};api.op.version++;await ui.refresh();await settle();
 assert.match(el.children[6].textContent,/team\/project/);await controls(el)[0].emit('click');await settle();assert.equal(api.op.status,'waiting');assert.equal(writes(api).filter(c=>c.path.endsWith('/submit')).length,0);
 await controls(el)[2].emit('click');await settle();assert.match(el.children[6].textContent,/other\/project/);assert.match(el.children[6].textContent,/push.*note/);
 await controls(el)[0].emit('click');await settle();assert.deepEqual(api.op.draft.events_filter,{types:['push','note']});
});

test('selection refresh keeps search and pagination disabled until candidate reads settle',async()=>{
 const api=fixture(),ui=await launch(api);await settle();const el=card(ui);
 let release;
 api.hook=path=>path.endsWith('/candidates') ? new Promise(resolve=>{release=resolve;}) : undefined;
 const loading=el.children[3].emit('click');await settle();
 assert.equal(el.children[3].disabled,true);assert.equal(el.children[5].disabled,true);
 await ui.refresh();await settle();
 try {
  assert.equal(el.children[3].disabled,true);
  assert.equal(el.children[5].disabled,true);
 } finally {release({chats:[{chat_id:'oc_one',name:'产品讨论群',chat_mode:'group'}],has_more:true,page_token:'page2'});await loading;await settle();}
 assert.equal(el.children[3].disabled,false);assert.equal(el.children[5].disabled,false);
});

test('Scion repository cards share selection receipts and preserve exact repository identity across bounded reads',async()=>{
 const api=fixture('repositories');api.op.input={kind:'repositories',provider:'scion',platform_id:'scion',platform_version:3};api.op.draft={};
 const one={name:'Owner/repo',created_at_ms:100,description:'Issue tracker'},two={name:'Owner/other',created_at_ms:200,description:''};
 api.hook=(path,body)=>path.endsWith('/candidates') ? {repositories:body.page_token ? [one,two] : [one],truncated:!body.page_token,next_page_token:body.page_token ? '' : '200'} : undefined;
 const ui=await launch(api);await settle();const el=card(ui);
 assert.equal(el.children[0].textContent,'选择 Scion 仓库');assert.equal(el.children[2].children.length,1);assert.match(el.children[9].textContent,/尚未读全/);
 const choice=candidates(el).children[0].children[0];assert.equal(choice.type,'radio');choice.checked=true;await choice.emit('change');
 await el.children[5].emit('click');await settle();assert.equal(candidates(el).children.length,2);assert.equal(choice.checked,true);
 assert.equal(api.calls.filter(c=>c.path.endsWith('/candidates')).at(-1).body.page_token,'200');
 api.hook=path=>path.endsWith('/candidates') ? {repositories:[],truncated:false} : undefined;
 const query=el.children[2].children[0].children[1];query.value='missing';await el.children[3].emit('click');await settle();
 assert.equal(candidates(el).children.length,0);assert.equal(el.children[6].textContent,'已选仓库：Owner/repo');
 assert.equal(api.calls.filter(c=>c.path.endsWith('/candidates')).at(-1).body.page_token,'200');
 await controls(el)[0].emit('click');await settle();
 assert.deepEqual(api.op.draft,{repository:{name:'Owner/repo',created_at_ms:100}});assert.equal(api.op.status,'completed');
 assert.equal(el.children[9].textContent,'');
 assert.equal(api.calls.some(c=>/member\/(tool|notifications)|connect\/start/.test(c.path)),false);
 const restored=await launch(api);await settle();assert.equal(card(restored).children[6].textContent,'已选仓库：Owner/repo');
});
