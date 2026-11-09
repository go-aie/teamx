import {test} from "node:test";
import assert from "node:assert/strict";
import {launch,settle} from "./web_app_harness.mjs";
function fixture(){
 const members=[{agent_id:"assistant",membership_id:"member-a",runner:{profile:"p"}}];
 const connection={id:"lark-connection",name:"飞书",provider:"lark",url:"https://open.feishu.cn",version:1,validation:{status:"valid",username:"测试用户",account:{id:{value:"ou_fixture"}}}};
 const api={calls:[],fail:false,chats:[{chat_id:"oc_fixture",name:"产品讨论群",chat_mode:"group"},{chat_id:"oc_private",name:"张同学",chat_mode:"p2p"}],data:{provider:"lark",channel:"messages",available:true,subscription_id:"",subscription_version:0,status:"disabled",pending:0,config:{enabled:false,connection_id:"",chats:[],target:{},private:true,group_mentions:true,at_all:false,bots:true,interval_seconds:60,backfill_seconds:0},lark:{cli_version:"1.0.99",chats:[]}},async request(path,body){
  api.calls.push({path,body:structuredClone(body)});
  if(path==="/v1/platform/lark/chats")return {chats:structuredClone(api.chats.filter(c=>!body.query || c.name.includes(body.query))),has_more:false,page_token:""};
  if(path==="/v1/ui")return {managed:true};
  if(path==="/v1/rooms")return {service_epoch:"manager",operator:"tester",rooms:[{room_id:"A",room_instance:"instance-A",name:"A",status:"ready",runtime_status:"ready",service:{room_id:"A",room_instance:"instance-A",service_epoch:"active-A"}}],teams:[]};
  if(path.startsWith("/v1/workspace?"))return {workspace:{root:"workspaces"},workspace_version:1,members:[],credential_status:"missing"};
  if(path.startsWith("/v1/platform/notifications?"))return {subscriptions:[structuredClone(api.data)]};
  if(path.startsWith("/v1/platform/connections?"))return {connections:api.connections};
  if(path==="/v1/platform/connections/lark/discover")return {executable:"/fixture/lark-cli",profiles:api.profiles};
  if(path==="/v1/platform/connections/lark/login" || path==="/v1/platform/connections/lark/logout" || path==="/v1/platform/connections/lark/authorize-targets" || path==="/v1/platform/connections/lark/authorize-sending")return {session_id:"auth-session",status:"starting",connections:[]};
  if(path==="/v1/platform/connections/lark/status")return api.authView;
  if(path==="/v1/platform/connections/lark/cancel")return {session_id:"auth-session",status:"cancelled"};
  if(path==="/v1/platform/connections/save"){
   const saved={id:body.id,...body,version:1,validation:{status:"unverified"}};api.connections.push(saved);return saved;
  }
  if(path==="/v1/platform/connections/verify"){
   const saved=api.connections.find(c=>c.id===body.id),result={...saved,version:saved.version+1,validation:api.verifyError?{status:"failed",error_code:api.verifyError}:{status:"valid",username:"测试用户"}};
   api.connections[api.connections.indexOf(saved)]=result;return result;
  }
  if(path==="/v1/platform/notifications/configure"){
   if(api.fail)throw Object.assign(Error("conflict"),{code:"conflict"});
   api.data={...api.data,config:body.config,subscription_id:api.data.subscription_id || "subscription",subscription_version:api.data.subscription_version+1,status:"running"};return structuredClone(api.data);
  }
  if(path==="/v1/member/list")return {members,latest_seq:0};
  if(path==="/v1/read")return {messages:[],snapshot_seq:0,has_more:false};
  throw Error(`Unexpected ${path}`);
 }};api.connections=[connection];api.profiles=[{name:"explicit",brand:"feishu",user:"测试用户",tokenStatus:"valid"}];api.authView={session_id:"auth-session",status:"waiting",verification_url:"https://accounts.feishu.cn/oauth?code=fixture",qrcode_url:"/v1/platform/connections/lark/qrcode?session_id=auth-session"};return api;
}
async function draft(ui){
 await ui.el("open-lark-source").emit("click");await settle();
 ui.el("lark-enabled").checked=true;ui.el("lark-connection").value="lark-connection";
 ui.el("lark-target").value=JSON.stringify({mode:"inbox",agent_id:"assistant",membership_id:"member-a"});
 await ui.el("lark-connection").emit("change");await openChatPicker(ui);
 const choice=chatChoice(ui,"oc_fixture");choice.checked=true;await choice.emit("change");
}
test("Lark specified-chat settings preserve drafts and submit frozen Membership",async()=>{
 const api=fixture(),ui=await launch(api);await draft(ui);
 assert.equal(ui.el("notification-panel-messages").hidden,false);assert.equal(ui.el("lark-save").disabled,false);
 await ui.refresh();assert.match(ui.el("lark-chat-selected").children[0].textContent,/产品讨论群/);assert.equal(ui.el("lark-enabled").checked,true);
 await ui.el("lark-save").emit("click");const saved=api.calls.find(x=>x.path==="/v1/platform/notifications/configure").body;
 assert.equal(saved.provider,"lark");assert.equal(saved.channel,"messages");assert.equal(saved.subscription_version,0);assert.equal(saved.config.target.membership_id,"member-a");
 assert.deepEqual(saved.config.chats,["oc_fixture"]);assert.equal(saved.config.at_all,false);assert.equal(saved.config.bots,true);
 assert.equal(ui.el("lark-save").disabled,true);assert.equal(ui.el("lark-sync").disabled,false);assert.equal(ui.el("lark-backfill").disabled,true);
});
test("Lark failed saves retain draft without automatic retry",async()=>{
 const api=fixture(),ui=await launch(api);await draft(ui);api.fail=true;await ui.el("lark-save").emit("click");await ui.refresh();
 assert.equal(api.calls.filter(x=>x.path==="/v1/platform/notifications/configure").length,1);
 assert.match(ui.el("lark-chat-selected").children[0].textContent,/产品讨论群/);assert.match(ui.el("lark-error").textContent,/变化.*核对/);
});
test("Lark settings distinguish an unsaved switch from saved state and sync feedback",async()=>{
 const api=fixture(),original=api.request;
 api.request=async(path,body)=>{
  if(path!=="/v1/platform/notifications/sync")return original(path,body);
  api.calls.push({path,body:structuredClone(body)});return structuredClone(api.data);
 };
 const ui=await launch(api);await draft(ui);
 assert.equal(ui.el("lark-state").textContent,"待保存");assert.equal(ui.el("lark-sync").disabled,true);
 await ui.el("lark-save").emit("click");
 assert.equal(ui.el("lark-state").textContent,"运行中");assert.equal(ui.el("lark-feedback").textContent,"设置已保存。");
 await ui.el("lark-sync").emit("click");
 assert.equal(api.calls.filter(call=>call.path==="/v1/platform/notifications/sync").length,1);
 assert.match(ui.el("lark-feedback").textContent,/已发起同步/);
});
test("Lark enable reveals configuration and disabling preserves the draft",async()=>{
 const api=fixture(),ui=await launch(api);await draft(ui);
 await ui.el("lark-enabled").emit("change");assert.equal(ui.el("lark-settings").open,true);
 ui.el("lark-interval").value="90";await ui.el("lark-interval").emit("change");
 ui.el("lark-enabled").checked=false;await ui.el("lark-enabled").emit("change");assert.equal(ui.el("lark-settings").open,false);
 ui.el("lark-enabled").checked=true;await ui.el("lark-enabled").emit("change");assert.equal(ui.el("lark-settings").open,true);
 assert.equal(ui.el("lark-interval").value,"90");assert.equal(ui.el("lark-chat-selected").children.length,1);
 assert.equal(api.calls.some(call=>call.path==="/v1/platform/notifications/configure"),false);
});
test("Lark group receiving mode survives refresh and save with private messages always included",async()=>{
 const api=fixture(),ui=await launch(api);await draft(ui);
 assert.equal(ui.el("lark-group-mode").value,"mentions");
 ui.el("lark-group-mode").value="all";await ui.el("lark-group-mode").emit("change");
 assert.equal(ui.el("lark-group-mentions").hidden,true);
 assert.equal(ui.el("lark-private"),undefined);assert.equal(ui.el("lark-bots"),undefined);
 await ui.refresh();assert.equal(ui.el("lark-group-mode").value,"all");
 await ui.el("lark-save").emit("click");
 const saved=api.calls.find(x=>x.path==="/v1/platform/notifications/configure").body;
 assert.equal(saved.config.group_all,true);assert.equal(saved.config.scope_mode,"specified");assert.deepEqual(saved.config.chats,["oc_fixture"]);
 assert.equal(ui.el("lark-group-mode").value,"all");assert.equal(ui.el("lark-save").disabled,true);
 ui.el("lark-group-mode").value="mentions";await ui.el("lark-group-mode").emit("change");
 assert.equal(ui.el("lark-save").disabled,false);assert.equal(ui.el("lark-group-mentions").hidden,false);
});
test("Lark switching to all chats clears group-all and requires explicit re-enabling on return",async()=>{
 const api=fixture(),ui=await launch(api);await draft(ui);
 ui.el("lark-group-mode").value="all";await ui.el("lark-group-mode").emit("change");
 ui.el("lark-scope").checked=true;await ui.el("lark-scope").emit("change");
 assert.equal(ui.el("lark-group-mode").value,"mentions");assert.equal(ui.el("lark-group-mode-all").disabled,true);
 await ui.el("lark-save").emit("click");
 const saved=api.calls.find(x=>x.path==="/v1/platform/notifications/configure").body;
 assert.equal(saved.config.group_all,false);assert.equal(saved.config.scope_mode,"all");
 ui.el("lark-scope").checked=false;await ui.el("lark-scope").emit("change");
 assert.equal(ui.el("lark-group-mode-all").disabled,false);assert.equal(ui.el("lark-group-mode").value,"mentions");
});
async function connectionDraft(api,platform="feishu"){
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");
 ui.el("connection-provider").value=platform;await ui.el("connection-provider").emit("change");return ui;
}
test("Lark connection discovers the CLI and profile, derives origin and verifies after saving",async()=>{
 const api=fixture(),ui=await connectionDraft(api);
 assert.equal(ui.el("connection-url-row").hidden,true);assert.equal(ui.el("connection-url").required,false);
 assert.equal(ui.el("connection-profile").value,"explicit");assert.equal(ui.el("connection-application-picker").hidden,true);
 assert.equal(ui.el("connection-executable").value,"/fixture/lark-cli");assert.equal(ui.el("connection-name").value,"飞书");
 assert.match(ui.el("connection-account").textContent,/测试用户/);
 await ui.el("connection-form").emit("submit");const saved=api.calls.find(x=>x.path==="/v1/platform/connections/save").body;
 assert.equal(saved.provider,"lark");assert.equal(saved.url,"https://open.feishu.cn");assert.equal(saved.snapshot_version,undefined);
 assert.equal(saved.cli_profile.cli_version,"1.0.99");assert.equal(saved.cli_profile.profile,"explicit");assert.equal(saved.cli_profile.brand,"feishu");
 assert.equal(saved.pat,undefined);assert.equal(saved.token_env,undefined);
 assert.equal(api.calls.filter(x=>x.path==="/v1/platform/connections/verify").length,1);
 assert.equal(ui.el("connection-form").hidden,true);assert.match(ui.el("connections-feedback").textContent,/连接已保存.*验证成功/);
});
test("Lark platform has its own default origin and brand without a duplicate brand selector",async()=>{
 const api=fixture();api.profiles[0].brand="lark";const ui=await connectionDraft(api,"lark");
 await ui.el("connection-form").emit("submit");const saved=api.calls.find(x=>x.path==="/v1/platform/connections/save").body;
 assert.equal(saved.url,"https://open.larksuite.com");assert.equal(saved.cli_profile.brand,"lark");assert.equal(saved.name,"Lark");
 assert.equal(ui.el("connection-brand"),undefined);
});
test("multiple configurations require selection and never ask the user to type a profile",async()=>{
 const api=fixture();api.profiles.push({name:"another",brand:"feishu",user:"另一账号"});const ui=await connectionDraft(api);
 assert.equal(ui.el("connection-profile-choice").hidden,false);assert.equal(ui.el("connection-profile").value,"");
 await ui.el("connection-form").emit("submit");assert.equal(api.calls.filter(x=>x.path==="/v1/platform/connections/save").length,0);
 assert.match(ui.el("connections-feedback").textContent,/连接未保存.*选择或连接应用/);
 ui.el("connection-profile").value="another";await ui.el("connection-profile").emit("change");
 await ui.el("connection-form").emit("submit");assert.equal(api.calls.find(x=>x.path==="/v1/platform/connections/save").body.cli_profile.profile,"another");
});
test("missing application configuration shows setup guidance and preserves the connection draft",async()=>{
 for(const brand of ["feishu","lark"]){
 const api=fixture();api.profiles=[];const ui=await connectionDraft(api,brand);ui.el("connection-name").value="我的连接";
 assert.equal(ui.el("connection-setup").hidden,false);assert.match(ui.el("connection-setup-command").textContent,new RegExp(`config init --new --brand ${brand} --name teamx-${brand}`));
 assert.equal(ui.el("connection-sending").hidden,true);assert.equal(ui.el("connection-send-status").hidden,true);
 assert.equal(ui.el("connection-save").hidden,true);assert.equal(ui.el("connection-actions").hidden,true);
 let copied;
 ui.window.navigator={clipboard:{async writeText(text){copied=text;}}};
 await ui.el("connection-setup-copy").emit("click");assert.equal(copied,ui.el("connection-setup-command").textContent);assert.equal(ui.el("connection-setup-copy").textContent,"已复制");
 ui.el("connection-setup-existing").checked=true;ui.el("connection-setup-new").checked=false;await ui.el("connection-setup-existing").emit("change");
 assert.doesNotMatch(ui.el("connection-setup-command").textContent,/--new/);
 assert.match(ui.el("connection-setup-command").textContent,new RegExp(`config init --brand ${brand} --name teamx-${brand}`));
 assert.match(ui.el("connection-setup-hint").textContent,/App ID.*App Secret/);assert.equal(ui.el("connection-setup-copy").textContent,"复制命令");
 ui.window.navigator={};await ui.el("connection-setup-copy").emit("click");assert.match(ui.el("connection-setup-copy-status").textContent,/请选中上方命令复制/);
 assert.equal(ui.el("connection-login").hidden,true);await ui.el("connection-form").emit("submit");
 assert.equal(ui.el("connection-name").value,"我的连接");assert.equal(ui.el("connection-form").hidden,false);
 assert.equal(ui.el("connections-feedback").dataset.kind,"error");assert.match(ui.el("connections-feedback").textContent,/连接未保存/);
 api.profiles=[{name:"new-app",brand}];await ui.el("connection-detect").emit("click");
 assert.equal(ui.el("connection-profile").value,"new-app");assert.equal(ui.el("connection-login").hidden,false);
 assert.equal(ui.el("connection-advanced").open,false);assert.equal(ui.el("connection-application-name").textContent,"new-app");assert.equal(ui.el("connection-account").textContent,"尚未登录");
 assert.equal(ui.el("connection-save").hidden,true);assert.equal(ui.el("connection-sending").hidden,true);
 api.profiles[0].user="测试用户";await ui.el("connection-detect").emit("click");
 assert.equal(ui.el("connection-save").hidden,false);assert.equal(ui.el("connection-actions").hidden,false);
 assert.equal(ui.el("connection-sending").hidden,false);assert.equal(ui.el("connection-sending").open,false);
 assert.equal(ui.el("connection-account").textContent,"测试用户");assert.match(ui.el("connection-account-state").textContent,/已登录/);
 }
});
test("relative executable is rejected before saving an automatically discovered account",async()=>{
 const api=fixture(),ui=await connectionDraft(api);ui.el("connection-executable").value="lark-cli";
 await ui.el("connection-form").emit("submit");assert.equal(ui.el("connection-executable").attributes["aria-invalid"],"true");
 assert.match(ui.el("connections-feedback").textContent,/连接未保存.*绝对路径/);assert.equal(api.calls.filter(x=>x.path==="/v1/platform/connections/save").length,0);
});
test("server rejection is visibly an error and never retries a connection save",async()=>{
 const api=fixture(),original=api.request;api.request=async(path,body)=>{if(path==="/v1/platform/connections/save"){api.calls.push({path,body});throw Object.assign(Error("invalid request"),{code:"request"});}return original(path,body);};
 const ui=await connectionDraft(api);await ui.el("connection-form").emit("submit");await ui.refresh();
 assert.equal(ui.el("connection-form").hidden,false);assert.equal(ui.el("connection-profile").value,"explicit");
 assert.equal(ui.el("connections-feedback").dataset.kind,"error");assert.match(ui.el("connections-feedback").textContent,/连接未保存/);
 assert.equal(api.calls.filter(x=>x.path==="/v1/platform/connections/save").length,1);
});
test("a saved connection with failed verification stays editable and is not reported as connected",async()=>{
 const api=fixture();api.verifyError="permission_denied";const ui=await connectionDraft(api);await ui.el("connection-form").emit("submit");
 assert.equal(ui.el("connection-form").hidden,false);assert.match(ui.el("connections-feedback").textContent,/连接已保存.*验证失败/);
 assert.equal(ui.el("connections-feedback").dataset.kind,"error");assert.equal(api.calls.filter(x=>x.path==="/v1/platform/connections/save").length,1);
});
test("login shows the link and QR, polls read-only status, and does not persist the flow",async()=>{
 const api=fixture(),ui=await connectionDraft(api);await ui.el("connection-login").emit("click");
 assert.equal(ui.el("connection-login-panel").hidden,false);assert.match(ui.el("connection-login-link").href,/^https:\/\/accounts.feishu.cn/);
 assert.match(ui.el("connection-login-qr").src,/^\/v1\/platform\/connections\/lark\/qrcode\?/);
 assert.equal(ui.el("connection-save").disabled,true);assert.equal(ui.el("connection-login-cancel").hidden,false);
 assert.equal(api.calls.filter(x=>x.path==="/v1/platform/connections/lark/login").length,1);
 api.authView={session_id:"auth-session",status:"succeeded",username:"测试用户"};await ui.clock.advance(1500);
 assert.equal(ui.el("connection-login-panel").hidden,true);assert.equal(ui.el("connection-login-link").href,"");assert.equal(ui.el("connection-save").disabled,false);
 assert.match(ui.el("connections-feedback").textContent,/账号已验证/);
 assert.equal(JSON.stringify([...ui.saved]).includes("auth-session"),false);assert.equal(JSON.stringify([...ui.saved]).includes("accounts.feishu.cn"),false);
});
test("closing during login cancels the server flow and ignores a late status response",async()=>{
 const api=fixture(),ui=await connectionDraft(api);await ui.el("connection-login").emit("click");
 await ui.el("close-settings").emit("click");assert.equal(ui.el("settings-dialog").open,false);
 assert.equal(api.calls.filter(x=>x.path==="/v1/platform/connections/lark/cancel").length,1);
 const reads=api.calls.filter(x=>x.path==="/v1/platform/connections/lark/status").length;
 await ui.clock.advance(3000);assert.equal(api.calls.filter(x=>x.path==="/v1/platform/connections/lark/status").length,reads);
 assert.equal(ui.el("connection-login-qr").src,"");
});
test("logout explicitly acknowledges all connections sharing the profile and its affected Rooms",async()=>{
 const api=fixture();api.connections[0].cli_profile={version:1,executable:"/different/lark-cli",profile:"explicit",brand:"feishu",cli_version:"1.0.99"};
 api.connections[0].references=[{name:"个人助手",room_id:"A",room_instance:"instance-A"}];
 api.confirm=()=>{throw Error("native confirmation must not open");};api.authView={session_id:"auth-session",status:"succeeded"};
 const ui=await connectionDraft(api);await ui.el("connection-logout").emit("click");
 assert.equal(ui.el("connection-auth-confirmation").hidden,false);
 assert.match(ui.el("connection-auth-confirmation-description").textContent,/不等于取消应用/);
 assert.equal(ui.el("connection-references-title").textContent,"引用房间 · 1");
 assert.equal(ui.el("connection-reference-list").children[0].children.at(-1).children[0].children[0].textContent,"个人助手");
 assert.equal(api.calls.some(c=>c.path.endsWith("/lark/logout")),false);
 await ui.el("connection-auth-confirm").emit("click");
 const body=api.calls.find(x=>x.path==="/v1/platform/connections/lark/logout").body;
 assert.deepEqual(body.affected,[{id:"lark-connection",version:1,referenced_rooms:[{room_id:"A",room_instance:"instance-A"}]}]);
 assert.match(ui.el("connections-feedback").textContent,/已退出登录/);assert.match(ui.el("connections-feedback-detail").textContent,/授权管理/);
});
test("Lark shares the grouped impact panel through confirmation and active authorization",async()=>{
 const api=fixture();api.confirm=()=>{throw Error("native confirmation must not open");};
 api.connections[0].cli_profile={version:1,executable:"/fixture/lark-cli",profile:"explicit",brand:"feishu",cli_version:"1.0.99"};
 api.connections[0].references=[{room_id:"A",room_instance:"instance-A",name:"个人助手",pending:true}];
 api.connections.push({...api.connections[0],id:"shared",name:"共享应用",references:[{room_id:"A",room_instance:"instance-A",name:"个人助手"},{room_id:"B",room_instance:"instance-B",name:"评审房间"}]});
 const ui=await connectionDraft(api);
 await ui.el("connection-login").emit("click");
 assert.equal(ui.el("connection-auth-confirmation").hidden,false);
 assert.equal(ui.el("connection-references").dataset.impact,"true");
 assert.equal(ui.el("connection-references-title").textContent,"引用房间 · 2");
 assert.equal(ui.el("connection-reference-list").children.length,2);
 assert.equal(ui.el("connection-reference-list").children[1].children[0].textContent,"共享应用");
 assert.equal(ui.el("connection-reference-list").children[0].children.at(-1).children[0].children[1].textContent,"待完成绑定");
 assert.equal(ui.el("connection-save").disabled,true);
 await ui.el("connection-form").emit("submit");assert.equal(api.calls.some(c=>c.path.endsWith("/save")),false);
 await ui.el("connection-auth-confirm").emit("click");
 assert.equal(ui.el("connection-auth-confirmation").hidden,true);
 assert.equal(ui.el("connection-references").dataset.impact,"true");
 assert.equal(ui.el("connection-references-title").textContent,"引用房间 · 2");
 assert.equal(ui.el("connection-login-panel").hidden,false);
 assert.equal(ui.el("connection-login-cancel").hidden,false);
 assert.equal(ui.el("connection-login").disabled,true);
 await ui.el("connection-login-cancel").emit("click");
 assert.equal(ui.el("connection-references").dataset.impact,"false");
 assert.equal(ui.el("connection-login-panel").hidden,true);
 assert.equal(api.calls.filter(c=>c.path.endsWith("/lark/login")).length,1);
});
test("Lark shows an empty impact panel while processing and clears stale confirmations",async()=>{
 for(const action of ["cancel","back","close","profile"]){
  const api=fixture();api.connections[0].cli_profile={version:1,executable:"/fixture/lark-cli",profile:"explicit",brand:"feishu",cli_version:"1.0.99"};
  api.connections[0].references=[];api.authView={session_id:"auth-session",status:"starting"};
  const ui=await connectionDraft(api);await ui.el("connection-login").emit("click");
  assert.equal(ui.el("connection-references-title").textContent,"引用房间 · 0");
  assert.equal(ui.el("connection-reference-list").children[0].children.at(-1).children[0].textContent,"暂无房间引用");
  if(action==="cancel"){
   await ui.el("connection-auth-dismiss").emit("click");
   assert.equal(ui.el("connection-name").disabled,false);
   await ui.el("connection-login").emit("click");await ui.el("connection-auth-confirm").emit("click");
   assert.equal(ui.el("connection-references").dataset.impact,"true");
   assert.match(ui.el("connection-account").textContent,/正在准备账号授权/);
   await ui.el("connection-login-cancel").emit("click");
   assert.equal(ui.el("connection-references").dataset.impact,"false");
  }else{
   if(action==="back")await ui.el("connection-back").emit("click");
   if(action==="close")await ui.el("close-settings").emit("click");
   if(action==="profile"){ui.el("connection-profile").value="";await ui.el("connection-profile").emit("change");}
   assert.equal(ui.el("connection-auth-confirmation").hidden,true);
   assert.equal(ui.el("connection-name").disabled,false);
   await ui.el("connection-auth-confirm").emit("click");
   assert.equal(api.calls.some(c=>c.path.endsWith("/lark/login")),false);
  }
 }
});
test("a late detection cannot disable or change a newly selected GitLab form",async()=>{
 const api=fixture(),original=api.request;let finish;
 api.request=(path,body)=>path==="/v1/platform/connections/lark/discover" ? new Promise(resolve=>{finish=resolve;}) : original(path,body);
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");
 ui.el("connection-provider").value="feishu";const detecting=ui.el("connection-provider").emit("change");
 ui.el("connection-provider").value="gitlab";await ui.el("connection-provider").emit("change");
 finish({executable:"/late/lark-cli",profiles:api.profiles});await detecting;
 assert.equal(ui.el("connection-profile-row").hidden,true);assert.equal(ui.el("connection-url-row").hidden,false);assert.equal(ui.el("connection-save").disabled,false);
 assert.notEqual(ui.el("connection-executable").value,"/late/lark-cli");
});
test("a removed saved profile never silently changes to another account",async()=>{
 const api=fixture();api.connections[0].cli_profile={version:1,executable:"/fixture/lark-cli",profile:"removed",brand:"feishu",cli_version:"1.0.99"};
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connections-list").children[0].emit("click");
 assert.equal(ui.el("connection-profile").value,"");assert.equal(ui.el("connection-profile-choice").hidden,false);
 await ui.el("connection-form").emit("submit");assert.equal(api.calls.filter(x=>x.path==="/v1/platform/connections/save").length,0);
});
test("an in-flight auth status cannot restore the QR after the settings close",async()=>{
 const api=fixture(),original=api.request;let finish,reads=0;
 api.request=(path,body)=>path==="/v1/platform/connections/lark/status" && ++reads>1 ? new Promise(resolve=>{finish=resolve;}) : original(path,body);
 const ui=await connectionDraft(api);await ui.el("connection-login").emit("click");await ui.clock.advance(1500);
 await ui.el("close-settings").emit("click");finish({session_id:"auth-session",status:"succeeded",username:"晚到账号"});await settle();
 assert.equal(ui.el("settings-dialog").open,false);assert.equal(ui.el("connection-login-panel").hidden,true);assert.equal(ui.el("connection-login-qr").src,"");
 assert.doesNotMatch(ui.el("connections-feedback").textContent,/晚到账号/);
});
test("Lark all-chat mode is explicit, preserves drafts and sends no stale specified IDs",async()=>{
 const api=fixture(),ui=await launch(api);await draft(ui);
 assert.equal(ui.el("lark-scope").checked,false);
 ui.el("lark-scope").checked=true;await ui.el("lark-scope").emit("change");
 assert.equal(ui.el("lark-chat-selection").hidden,true);assert.equal(ui.el("lark-save").disabled,false);
 await ui.refresh();assert.equal(ui.el("lark-scope").checked,true);
 await ui.el("lark-save").emit("click");const saved=api.calls.find(x=>x.path==="/v1/platform/notifications/configure").body;
 assert.equal(saved.config.scope_mode,"all");assert.deepEqual(saved.config.chats,[]);
 assert.equal(saved.config.target.membership_id,"member-a");
 assert.equal(ui.el("lark-scope-help").hidden,false);assert.equal(ui.el("lark-chat-picker").open,false);
 ui.el("lark-scope").checked=false;await ui.el("lark-scope").emit("change");
 assert.equal(ui.el("lark-chat-selection").hidden,false);assert.equal(ui.el("lark-scope-help").hidden,true);
 assert.equal(ui.el("lark-chat-selected").children.length,1);assert.match(ui.el("lark-chat-picker-summary").textContent,/已选 1/);
 await ui.el("lark-save").emit("click");
 const restored=api.calls.filter(x=>x.path==="/v1/platform/notifications/configure").at(-1).body.config;
 assert.equal(restored.scope_mode,"specified");assert.deepEqual(restored.chats,["oc_fixture"]);
});
test("Lark discovery and per-chat failures are visible without claiming full coverage",async()=>{
 const api=fixture();api.data={...api.data,subscription_id:"subscription",subscription_version:1,status:"running",config:{...api.data.config,scope_mode:"all"},lark:{directory_complete:false,directory_scanning:true,directory_pages:2,failed_chats:1,unlisted_chats:3,gap_chats:2,oldest_lag_seconds:90000,chats:[{chat_id:"oc_blocked",error_code:"permission_denied"}]}};
 const ui=await launch(api);await ui.el("open-lark-source").emit("click");await settle();
 assert.match(ui.el("lark-diagnostics").textContent,/会话目录加载中（2 页）/);
 assert.match(ui.el("lark-diagnostics").textContent,/读取受阻 1/);
 assert.match(ui.el("lark-diagnostics").textContent,/积压超过 24 小时 2/);
 assert.match(ui.el("lark-chat-status").children[0].textContent,/读取受阻 permission_denied/);
});
test("Lark compact sync status omits empty diagnostics and successful chat rows",async()=>{
 const api=fixture(),ui=await launch(api);await ui.el("open-lark-source").emit("click");await settle();
 assert.equal(ui.el("lark-diagnostics").textContent,"尚未开始同步");assert.equal(ui.el("lark-chat-status").hidden,true);
 const checked="2026-11-07T01:02:03Z",retry="2026-11-07T01:03:03Z";
 api.data={...api.data,subscription_id:"existing",subscription_version:1,status:"backoff",last_check:checked,pending:2,retry_at:retry,
  config:{...api.data.config,enabled:true},lark:{...api.data.lark,chats:[{chat_id:"oc_ok",completed:true},{chat_id:"oc_blocked",completed:true,error_code:"permission_denied"}]}};
 const active=await launch(api);await active.el("open-lark-source").emit("click");await settle();
 assert.match(active.el("lark-diagnostics").textContent,/最近检查.*待投递 2.*下次重试/);
 assert.doesNotMatch(active.el("lark-diagnostics").textContent,/目录|尚无|CLI|读取受阻 0|待检查会话 0/);
 assert.equal(active.el("lark-chat-status").hidden,false);assert.equal(active.el("lark-chat-status").children.length,1);
 assert.match(active.el("lark-chat-status").children[0].textContent,/oc_blocked.*permission_denied/);
});

async function openChatPicker(ui){
 ui.el("lark-chat-picker").open=true;await ui.el("lark-chat-picker").emit("toggle");await settle();
}
function chatChoice(ui,id){return ui.el("lark-chat-options").children.find(row=>row.children[0].value===id)?.children[0];}
test("Lark chat picker explains prerequisites and blocks browsing until the connection is verified",async()=>{
 const api=fixture(),ui=await launch(api);await ui.el("open-lark-source").emit("click");await settle();
 assert.equal(ui.el("lark-chat-picker-summary").textContent,"请先选择平台连接");
 assert.equal(ui.el("lark-chat-picker-hint").hidden,false);assert.match(ui.el("lark-chat-picker-hint").textContent,/在上方选择平台连接/);
 assert.equal(ui.el("lark-chat-search").disabled,true);assert.equal(ui.el("lark-chat-type").disabled,true);
 assert.equal(ui.el("lark-scope").disabled,true);
 await openChatPicker(ui);await ui.el("lark-chat-refresh").emit("click");
 assert.equal(ui.el("lark-chat-picker").open,false);assert.equal(api.calls.some(x=>x.path==="/v1/platform/lark/chats"),false);
 api.connections[0].validation.status="unverified";
 ui.el("lark-connection").value="lark-connection";await ui.el("lark-connection").emit("change");
 assert.equal(ui.el("lark-chat-picker-summary").textContent,"请先验证平台连接");assert.match(ui.el("lark-chat-picker-hint").textContent,/管理连接/);
 await openChatPicker(ui);assert.equal(api.calls.some(x=>x.path==="/v1/platform/lark/chats"),false);
 api.connections[0].validation.status="valid";await ui.el("lark-connection").emit("change");await openChatPicker(ui);
 assert.equal(ui.el("lark-chat-picker-hint").hidden,true);assert.equal(ui.el("lark-chat-search").disabled,false);assert.equal(ui.el("lark-chat-type").disabled,false);
 assert.equal(ui.el("lark-scope").disabled,false);
 assert.equal(api.calls.filter(x=>x.path==="/v1/platform/lark/chats").length,1);
 api.connections[0].validation.status="failed";await ui.el("lark-connection").emit("change");
 assert.equal(ui.el("lark-chat-picker").open,false);assert.equal(ui.el("lark-chat-picker-summary").attributes["aria-disabled"],"true");
 const choice=chatChoice(ui,"oc_fixture");assert.equal(choice.disabled,true);choice.checked=true;await choice.emit("change");
 assert.equal(ui.el("lark-chat-selected").children.length,0);
 await ui.el("lark-chat-refresh").emit("click");assert.equal(api.calls.filter(x=>x.path==="/v1/platform/lark/chats").length,1);
});
test("Lark conversation dropdown selects names and types, searches and saves native IDs",async()=>{
 const api=fixture(),ui=await launch(api);await draft(ui);await ui.el("lark-chat-selected").children[0].emit("click");
 await openChatPicker(ui);
 const browse=api.calls.find(x=>x.path==="/v1/platform/lark/chats").body;
 assert.equal(browse.connection_id,"lark-connection");assert.equal(browse.connection_version,1);assert.equal(browse.caller.kind,"human");assert.equal(browse.room_instance,"instance-A");
 assert.match(ui.el("lark-chat-picker-status").textContent,/已加载 2 个会话/);
 ui.el("lark-chat-search").value="张";await ui.el("lark-chat-search").emit("input");
 await ui.clock.advance(300);
 assert.equal(ui.el("lark-chat-options").children.length,1);assert.ok(chatChoice(ui,"oc_private"));
 const privateChat=chatChoice(ui,"oc_private");privateChat.checked=true;await privateChat.emit("change");
 assert.match(ui.el("lark-chat-selected").children[0].textContent,/张同学/);
 ui.el("lark-chat-search").value="";await ui.el("lark-chat-search").emit("input");
 await ui.clock.advance(0);
 ui.el("lark-chat-type").value="group";await ui.el("lark-chat-type").emit("change");
 assert.equal(ui.el("lark-chat-options").children.length,1);assert.ok(chatChoice(ui,"oc_fixture"));
 const group=chatChoice(ui,"oc_fixture");group.checked=true;await group.emit("change");
 await ui.refresh();assert.equal(ui.el("lark-chat-selected").children.length,2);
 await ui.el("lark-save").emit("click");
 const saved=api.calls.find(x=>x.path==="/v1/platform/notifications/configure").body.config;
 assert.deepEqual(saved.chats,["oc_fixture","oc_private"]);assert.equal(saved.private,true);assert.equal(saved.bots,true);
 await ui.el("lark-chat-selected").children[0].emit("click");assert.equal(ui.el("lark-chat-selected").children.length,1);
});
test("Lark dropdown pagination and failures preserve selection and offer remote search",async()=>{
 const api=fixture(),original=api.request;let fail=false;
 api.request=async(path,body)=>{
  if(path!=="/v1/platform/lark/chats")return original(path,body);
  api.calls.push({path,body});if(fail)throw Error("read failed");
  return body.page_token ? {chats:[{chat_id:"oc_next",chat_mode:"p2p",name:"下一页同事"}],has_more:false,page_token:""} : {chats:[api.chats[0]],has_more:true,page_token:"next-page"};
 };
 const ui=await launch(api);await draft(ui);
 assert.match(ui.el("lark-chat-picker-status").textContent,/可加载更多或输入名称搜索/);
 fail=true;await ui.el("lark-chat-more").emit("click");
 assert.match(ui.el("lark-chat-picker-status").textContent,/读取失败.*已选会话保留/);assert.match(ui.el("lark-chat-selected").children[0].textContent,/产品讨论群/);
 fail=false;await ui.el("lark-chat-more").emit("click");
 assert.equal(api.calls.filter(x=>x.path==="/v1/platform/lark/chats").at(-1).body.page_token,"next-page");
 assert.ok(chatChoice(ui,"oc_next"));assert.equal(ui.el("lark-chat-more").hidden,true);
});
test("Lark dropdown limits selection and discards a late result after connection changes",async()=>{
 const api=fixture(),original=api.request;let finish,delay=false;
 api.request=(path,body)=>delay && path==="/v1/platform/lark/chats" ? new Promise(resolve=>{finish=resolve;}) : original(path,body);
 api.chats.push(...Array.from({length:32},(_,i)=>({chat_id:`oc_${i}`,name:`会话 ${i}`,chat_mode:"group"})));
 const ui=await launch(api);await draft(ui);await ui.el("lark-chat-selected").children[0].emit("click");
 for(let i=0;i<32;i++){const choice=chatChoice(ui,`oc_${i}`);choice.checked=true;await choice.emit("change");}
 delay=true;
 assert.equal(chatChoice(ui,"oc_fixture").disabled,true);
 await ui.el("lark-chat-selected").children[0].emit("click");assert.equal(chatChoice(ui,"oc_fixture").disabled,false);
 const refreshing=ui.el("lark-chat-refresh").emit("click");await settle();
 ui.el("lark-connection").value="";await ui.el("lark-connection").emit("change");
 finish({chats:api.chats,has_more:false,page_token:""});await refreshing;
 assert.equal(ui.el("lark-chat-options").children.length,0);assert.equal(ui.el("lark-chat-selected").children.length,0);
 assert.match(ui.el("lark-chat-picker-status").textContent,/先选择并验证/);
});
test("Lark all-only group filtering is saved independently of private reception",async()=>{
 const api=fixture(),ui=await launch(api);await draft(ui);
 ui.el("lark-group").checked=false;await ui.el("lark-group").emit("change");
 ui.el("lark-all").checked=true;await ui.el("lark-all").emit("change");
 await ui.el("lark-save").emit("click");
 const saved=api.calls.find(x=>x.path==="/v1/platform/notifications/configure").body.config;
 assert.equal(saved.private,true);assert.equal(saved.bots,true);assert.equal(saved.group_all,false);assert.equal(saved.group_mentions,false);assert.equal(saved.at_all,true);
});
for(const [mode,groupMentions,atAll] of [["mentions",true,false],["at-all",false,true],["mentions-all",true,true],["none",false,false]]){
 test(`Lark ${mode} group rule round-trips and survives switching all messages and scope`,async()=>{
  const api=fixture(),ui=await launch(api);await draft(ui);
  ui.el("lark-group").checked=groupMentions;await ui.el("lark-group").emit("change");
  ui.el("lark-all").checked=atAll;await ui.el("lark-all").emit("change");
  ui.el("lark-group-mode").value="all";await ui.el("lark-group-mode").emit("change");
  await ui.el("lark-save").emit("click");
  assert.equal(api.data.config.group_all,true);assert.equal(api.data.config.group_mentions,groupMentions);assert.equal(api.data.config.at_all,atAll);
  const reloaded=await launch(api);await reloaded.el("open-lark-source").emit("click");await settle();
  assert.equal(reloaded.el("lark-group-mode").value,"all");assert.equal(reloaded.el("lark-save").disabled,true);
  reloaded.el("lark-scope").checked=true;await reloaded.el("lark-scope").emit("change");
  assert.equal(reloaded.el("lark-group-mode").value,"mentions");
  assert.equal(reloaded.el("lark-group").checked,groupMentions);assert.equal(reloaded.el("lark-all").checked,atAll);
  await reloaded.el("lark-save").emit("click");
  assert.equal(api.data.config.group_all,false);assert.equal(api.data.config.group_mentions,groupMentions);assert.equal(api.data.config.at_all,atAll);
  assert.equal(api.data.config.private,true);assert.equal(api.data.config.bots,true);
  const saved=await launch(api);await saved.el("open-lark-source").emit("click");await settle();
  assert.equal(saved.el("lark-group-mode").value,"mentions");assert.equal(saved.el("lark-save").disabled,true);
  assert.equal(saved.el("lark-group").checked,groupMentions);assert.equal(saved.el("lark-all").checked,atAll);
 });
}
test("Lark saved chats remain removable when the directory cannot load",async()=>{
 const api=fixture(),original=api.request;
 api.data={...api.data,subscription_id:"existing",subscription_version:1,config:{...api.data.config,enabled:true,connection_id:"lark-connection",chats:["oc_missing"],target:{mode:"inbox",agent_id:"assistant",membership_id:"member-a"}}};
 api.request=(path,body)=>path==="/v1/platform/lark/chats" ? Promise.reject(Error("unavailable")) : original(path,body);
 const ui=await launch(api);await ui.el("open-lark-source").emit("click");await settle();await openChatPicker(ui);
 assert.equal(ui.el("lark-chats"),undefined);assert.match(ui.el("lark-chat-selected").children[0].textContent,/oc_missing/);
 await ui.el("lark-chat-selected").children[0].emit("click");
 assert.equal(ui.el("lark-chat-selected").children.length,0);assert.equal(ui.el("lark-save").disabled,true);
});

function localDateTime(date) {
 const parts=Object.fromEntries(new Intl.DateTimeFormat("en-CA",{timeZone:"Asia/Shanghai",year:"numeric",month:"2-digit",day:"2-digit",hour:"2-digit",minute:"2-digit",second:"2-digit",hourCycle:"h23"}).formatToParts(date).map(p=>[p.type,p.value]));
 return `${parts.year}-${parts.month}-${parts.day}T${parts.hour}:${parts.minute}:${parts.second}`;
}
test("Lark custom historical start uses local time and survives refresh and saved reload",async()=>{
 const api=fixture(),ui=await launch(api);await draft(ui);
 assert.equal(ui.el("lark-backfill").disabled,false);assert.equal(ui.el("lark-backfill-start").disabled,false);
 assert.equal(ui.el("lark-backfill").value,"0");assert.equal(ui.el("lark-backfill-custom").hidden,true);
 ui.el("lark-backfill").value="custom";await ui.el("lark-backfill").emit("change");
 assert.equal(ui.el("lark-backfill-custom").hidden,false);assert.equal(ui.el("lark-save").disabled,true);
 assert.match(ui.el("lark-backfill-error").textContent,/请选择/);
 const start=new Date(Math.floor((Date.now()-7*86400000)/1000)*1000),local=localDateTime(start);
 ui.el("lark-backfill-start").value=local;await ui.el("lark-backfill-start").emit("input");
 assert.equal(ui.el("lark-save").disabled,false);await ui.refresh();assert.equal(ui.el("lark-backfill-start").value,local);
 await ui.el("lark-save").emit("click");
 const saved=api.calls.find(x=>x.path==="/v1/platform/notifications/configure").body.config;
 assert.equal(saved.backfill_seconds,0);assert.equal(saved.backfill_start,start.toISOString());
 assert.equal(ui.el("lark-backfill").value,"custom");assert.equal(ui.el("lark-backfill-start").value,local);
 assert.equal(ui.el("lark-backfill").disabled,true);assert.equal(ui.el("lark-backfill-start").disabled,true);assert.equal(ui.el("lark-save").disabled,true);
 const reloaded=await launch(api);await reloaded.el("open-lark-source").emit("click");await settle();
 assert.equal(reloaded.el("lark-backfill").value,"custom");assert.equal(reloaded.el("lark-backfill-start").value,local);
 assert.equal(reloaded.el("lark-backfill").disabled,true);assert.equal(reloaded.el("lark-backfill-start").disabled,true);
 assert.match(reloaded.el("lark-backfill-help").textContent,/已锁定.*新建房间/);
});
test("Lark rejects future backfill dates and switching back to default clears custom submission",async()=>{
 const api=fixture(),ui=await launch(api);await draft(ui);
 ui.el("lark-backfill").value="custom";await ui.el("lark-backfill").emit("change");
 ui.el("lark-backfill-start").value=localDateTime(new Date(Date.now()+86400000));await ui.el("lark-backfill-start").emit("input");
 assert.equal(ui.el("lark-save").disabled,true);assert.match(ui.el("lark-backfill-error").textContent,/不能晚于/);
 await ui.el("lark-save").emit("click");assert.equal(api.calls.filter(x=>x.path==="/v1/platform/notifications/configure").length,0);
 ui.el("lark-backfill").value="0";await ui.el("lark-backfill").emit("change");
 assert.equal(ui.el("lark-backfill-custom").hidden,true);assert.equal(ui.el("lark-backfill-error").hidden,true);
 await ui.el("lark-save").emit("click");
 const saved=api.calls.find(x=>x.path==="/v1/platform/notifications/configure").body.config;
 assert.equal(saved.backfill_seconds,0);assert.equal(saved.backfill_start,undefined);
});
test("Lark locked history start does not block other settings or rewrite the saved timestamp",async()=>{
 const api=fixture(),start=new Date(Math.floor((Date.now()-86400000)/1000)*1000);
 api.data={...api.data,subscription_id:"existing",subscription_version:3,status:"running",config:{...api.data.config,enabled:true,connection_id:"lark-connection",chats:["oc_fixture"],backfill_start:start.toISOString(),target:{mode:"inbox",agent_id:"assistant",membership_id:"member-a"}},lark:{...api.data.lark,baseline:start.toISOString()}};
 const ui=await launch(api);await ui.el("open-lark-source").emit("click");await settle();
 assert.equal(ui.el("lark-backfill").disabled,true);assert.match(ui.el("lark-backfill-help").textContent,/接收起点.*已锁定.*新建房间/);
 ui.el("lark-interval").value="10";await ui.el("lark-interval").emit("change");
 assert.equal(ui.el("lark-save").disabled,false);await ui.el("lark-save").emit("click");
 const saved=api.calls.find(x=>x.path==="/v1/platform/notifications/configure").body;
 assert.equal(saved.subscription_id,"existing");assert.equal(saved.config.interval_seconds,10);assert.equal(saved.config.backfill_start,start.toISOString());
 assert.equal(ui.el("lark-backfill-start").disabled,true);
});

test("L4-A sending checks report both permission layers and keep writes disabled",async()=>{
 const api=fixture(),original=api.request;
 api.request=(path,body)=>path==="/v1/platform/connections/lark/sending-status"?Promise.resolve({account_fixed:true,application_scopes:"missing",user_scopes:"missing",adapter_implemented:false}):original(path,body);
 const ui=await connectionDraft(api);
 assert.equal(ui.el("connection-send-check").hidden,false);
 await ui.el("connection-send-check").emit("click");
 assert.match(ui.el("connection-send-status").textContent,/发送条件未齐备.*开通应用发送权限.*用户发送授权/);
 assert.equal(api.calls.some(c=>c.path==="/v1/platform/connections/lark/login"),false);
});

test("Lark sending status reports accepted modes and keeps direct user sends distinct",async()=>{
 const api=fixture(),original=api.request;
 api.request=(path,body)=>path==="/v1/platform/connections/lark/sending-status"?Promise.resolve({account_fixed:true,application_scopes:"granted",user_scopes:"granted",adapter_implemented:true,reply_enabled:true,group_send_enabled:true,self_send_enabled:true,user_send_enabled:false}):original(path,body);
 const ui=await connectionDraft(api);
 await ui.el("connection-send-check").emit("click");
 assert.match(ui.el("connection-send-status").textContent,/可用：普通回复、群消息、本人自聊.*成员工具设置.*主动私信暂未开放/);
 assert.equal(api.calls.some(c=>c.path.includes("/actions/execute")),false);
});

test("L4-C target authorization is explicit and separate from sending and directory search",async()=>{
 const api=fixture(),ui=await connectionDraft(api);
 assert.equal(ui.el("connection-target-authorize").hidden,false);
 await ui.el("connection-target-authorize").emit("click");
 assert.equal(api.calls.some(c=>c.path.endsWith("/authorize-targets")),false);
 await ui.el("connection-auth-confirm").emit("click");
 const call=api.calls.find(c=>c.path==="/v1/platform/connections/lark/authorize-targets");
 assert.ok(call);assert.equal(call.body.cli_profile.profile,"explicit");
 assert.equal(api.calls.some(c=>c.path==="/v1/platform/connections/lark/authorize-sending"),false);
});

test("Lark searches remote matches with debounce and preserves selected labels across queries",async()=>{
 const api=fixture(),original=api.request;
 api.request=async(path,body,...rest)=>{
  if(path!=="/v1/platform/lark/chats" || !body.query)return original(path,body,...rest);
  api.calls.push({path,body:structuredClone(body)});
  return body.page_token ? {chats:[{chat_id:"oc_more",name:"更多群",chat_mode:"topic"}],has_more:false,page_token:""} : {chats:[{chat_id:"oc_remote",name:"研发群",chat_mode:"group"}],has_more:true,page_token:"next-search"};
 };
 const ui=await launch(api);await draft(ui);
 ui.el("lark-chat-search").value="yan";await ui.el("lark-chat-search").emit("input");
 await ui.clock.advance(200);
 ui.el("lark-chat-search").value="yanfa";await ui.el("lark-chat-search").emit("input");
 await ui.clock.advance(299);assert.equal(api.calls.filter(c=>c.body?.query).length,0);
 await ui.clock.advance(1);
 const call=api.calls.filter(c=>c.body?.query)[0];assert.equal(call.body.query,"yanfa");assert.equal(call.body.type,"all");
 assert.ok(chatChoice(ui,"oc_remote")); // Remote pinyin matches must not be locally filtered out.
 const choice=chatChoice(ui,"oc_remote");choice.checked=true;await choice.emit("change");
 await ui.el("lark-chat-more").emit("click");assert.equal(api.calls.at(-1).body.page_token,"next-search");
 assert.ok(chatChoice(ui,"oc_more"));
 ui.el("lark-chat-search").value="other";await ui.el("lark-chat-search").emit("input");await ui.clock.advance(300);
 assert.equal(api.calls.at(-1).body.page_token,"");assert.match(ui.el("lark-chat-selected").children[1].textContent,/研发群/);
 ui.el("lark-chat-search").value="";await ui.el("lark-chat-search").emit("input");await ui.clock.advance(0);
 assert.ok(chatChoice(ui,"oc_private"));assert.match(ui.el("lark-chat-selected").children[1].textContent,/研发群/);
});

test("Lark search ignores stale responses, aborts superseded requests and waits for Chinese composition",async()=>{
 const api=fixture(),original=api.request;let finish,oldSignal;
 api.request=(path,body,fetcher,timeout,signal)=>{
  if(path==="/v1/platform/lark/chats" && body.query==="old")return new Promise(resolve=>{finish=resolve;oldSignal=signal;});
  return original(path,body);
 };
 const ui=await launch(api);await draft(ui);
 ui.el("lark-chat-search").value="old";await ui.el("lark-chat-search").emit("input");await ui.clock.advance(300);
 await ui.el("lark-chat-search").emit("compositionstart");
 ui.el("lark-chat-search").value="zhang";await ui.el("lark-chat-search").emit("input",{isComposing:true});
 assert.equal(oldSignal.aborted,true);await ui.clock.advance(1000);
 assert.equal(api.calls.some(c=>c.body?.query==="zhang"),false);
 ui.el("lark-chat-search").value="张";await ui.el("lark-chat-search").emit("compositionend");await ui.clock.advance(300);
 assert.ok(chatChoice(ui,"oc_private"));
 finish({chats:[{chat_id:"oc_stale",name:"旧结果",chat_mode:"group"}],has_more:false,page_token:""});await settle();
 assert.equal(chatChoice(ui,"oc_stale"),undefined);assert.ok(chatChoice(ui,"oc_private"));
 ui.el("lark-chat-type").value="p2p";await ui.el("lark-chat-type").emit("change");await ui.clock.advance(0);
 assert.equal(api.calls.at(-1).body.type,"p2p");assert.equal(api.calls.at(-1).body.page_token,"");
 const count=api.calls.length;
 ui.el("lark-chat-search").value="字".repeat(51);await ui.el("lark-chat-search").emit("input");await ui.clock.advance(300);
 assert.equal(api.calls.length,count);assert.match(ui.el("lark-chat-picker-status").textContent,/最多 50/);
});

test("Lark search reports partial permissions and refinement without hiding usable results",async()=>{
 const api=fixture(),original=api.request;
 api.request=(path,body)=>path==="/v1/platform/lark/chats" && body.query ? Promise.resolve({chats:[{chat_id:"oc_found",name:"匹配群",chat_mode:"group"}],has_more:false,page_token:"",search_warnings:[{source:"user",code:"refine_query"},{source:"bot",code:"permission_denied"}]}) : original(path,body);
 const ui=await launch(api);await draft(ui);
 ui.el("lark-chat-search").value="匹配";await ui.el("lark-chat-search").emit("input");await ui.clock.advance(300);
 assert.ok(chatChoice(ui,"oc_found"));assert.equal(ui.el("lark-chat-more").hidden,true);
 assert.match(ui.el("lark-chat-picker-status").textContent,/人员私聊结果较多.*完整的名称.*机器人私聊搜索权限不足/);
 assert.match(ui.el("lark-chat-selected").children[0].textContent,/产品讨论群/);
});

test("closing Lark search cancels pending work and late results cannot replace a reopened picker",async()=>{
 const api=fixture(),original=api.request;let finish,signal;
 api.request=(path,body,fetcher,timeout,s)=>path==="/v1/platform/lark/chats" && body.query ? new Promise(resolve=>{finish=resolve;signal=s;}) : original(path,body);
 const ui=await launch(api);await draft(ui);
 ui.el("lark-chat-search").value="late";await ui.el("lark-chat-search").emit("input");await ui.clock.advance(300);
 ui.el("lark-chat-picker").open=false;await ui.el("lark-chat-picker").emit("toggle");assert.equal(signal.aborted,true);
 ui.el("lark-chat-search").value="";await ui.el("lark-chat-search").emit("input");await openChatPicker(ui);
 finish({chats:[{chat_id:"oc_late",name:"旧查询",chat_mode:"group"}],has_more:false,page_token:""});await settle();
 assert.equal(chatChoice(ui,"oc_late"),undefined);assert.ok(chatChoice(ui,"oc_fixture"));
});

const setupProfile = {version:1,executable:"/fixture/lark-cli",profile:"teamx-feishu-guided",brand:"feishu",cli_version:"1.0.99"};
function guidedFixture() {
 const api=fixture(),original=api.request;api.profiles=[];
 api.authView={session_id:"setup-session",stage:"application",status:"waiting",verification_url:"https://open.feishu.cn/page/cli?user_code=fixture",qrcode_url:"/v1/platform/connections/lark/qrcode?session_id=setup-session"};
 api.request=async(path,body,...args)=>{
  if(path.endsWith("/lark/setup")){api.calls.push({path,body:structuredClone(body)});return {session_id:"setup-session",stage:"application",status:"starting"};}
  if(path.endsWith("/lark/cancel")){api.calls.push({path,body:structuredClone(body)});return api.cancelView || {session_id:body.session_id,stage:"application",status:"cancelled"};}
  return original(path,body,...args);
 };
 return api;
}
function configuredView(status="waiting",stage="authorization",extra={}) {
 return {session_id:"setup-session",stage,status,configuration_found:true,cli_profile:setupProfile,app_id:"cli_fixture",app_name:"测试应用",...extra};
}
test("guided connection is primary for Feishu and Lark keeps its explicit fallback",async()=>{
 for(const brand of ["feishu","lark"]){
  const api=guidedFixture(),ui=await connectionDraft(api,brand);
  assert.equal(ui.el("connection-guided").hidden,false);assert.equal(ui.el("connection-advanced").open,false);
  assert.equal(ui.el("connection-setup-start").hidden,brand==="lark");
  assert.match(ui.el("connection-guided-hint").textContent,brand==="lark"?/尚未验证/:/官方页面.*已有应用.*无需手动填写/);
  assert.equal(api.calls.some(c=>c.path.endsWith("/lark/setup")),false);
 }
 const api=fixture(),ui=await connectionDraft(api);
 assert.equal(ui.el("connection-guided").hidden,true);assert.equal(ui.el("connection-setup-other").hidden,false);
 assert.equal(ui.el("connection-profile").value,"explicit");
});
test("guided setup changes stages, clears old QR, pins the returned profile and saves only on submit",async()=>{
 const api=guidedFixture(),ui=await connectionDraft(api);ui.el("connection-name").value="我的飞书草稿";
 await ui.el("connection-setup-start").emit("click");
 const start=api.calls.find(c=>c.path.endsWith("/lark/setup")).body;
 assert.deepEqual(start,{service_epoch:"manager",caller:{kind:"human",id:"tester"},executable:"/fixture/lark-cli",brand:"feishu"});
 assert.equal(ui.el("connection-login-link").textContent,"打开配置页面 ↗");assert.match(ui.el("connection-account").textContent,/等待你完成应用配置/);
 assert.equal(ui.el("connection-save").hidden,true);assert.equal(ui.el("connection-login-cancel").hidden,false);
 assert.equal(ui.el("connection-sending").hidden,true);await ui.el("connection-setup-start").emit("click");
 assert.equal(api.calls.filter(c=>c.path.endsWith("/lark/setup")).length,1);
 api.profiles=[{name:"other",brand:"feishu",user:"其他账号"},{name:setupProfile.profile,brand:"feishu"}];
 api.authView=configuredView("starting");await ui.clock.advance(1500);
 assert.equal(ui.el("connection-login-panel").hidden,true);assert.equal(ui.el("connection-login-qr").src,"");assert.equal(ui.el("connection-login-link").href,"");
 assert.equal(ui.el("connection-application-name").textContent,"测试应用");
 api.authView=configuredView("waiting","authorization",{verification_url:"https://accounts.feishu.cn/oauth?code=fixture",qrcode_url:"/v1/platform/connections/lark/qrcode?session_id=setup-session"});
 await ui.clock.advance(1500);assert.equal(ui.el("connection-login-panel").hidden,false);assert.equal(ui.el("connection-login-link").textContent,"打开授权页面 ↗");
 api.profiles[1].user="本次用户";api.authView=configuredView("succeeded","complete",{username:"本次用户"});await ui.clock.advance(1500);
 assert.equal(ui.el("connection-profile").value,setupProfile.profile);assert.equal(ui.el("connection-profile-choice").hidden,false);
 assert.equal(ui.el("connection-name").value,"我的飞书草稿");assert.equal(ui.el("connection-save").hidden,false);assert.equal(ui.el("connection-save").disabled,false);
 assert.equal(ui.el("connection-guided").hidden,true);assert.equal(ui.el("connection-advanced").open,false);assert.equal(ui.el("connection-sending").open,false);
 assert.equal(api.calls.some(c=>c.path.endsWith("/connections/save")),false);
 assert.match(ui.el("connections-feedback").textContent,/账号已验证.*请保存连接/);
 assert.doesNotMatch(JSON.stringify([...ui.saved]),/setup-session|user_code|accounts.feishu.cn/);
 await ui.el("connection-form").emit("submit");
 assert.equal(api.calls.find(c=>c.path.endsWith("/connections/save")).body.cli_profile.profile,setupProfile.profile);
 assert.equal(api.calls.some(c=>/notifications\/configure|authorize-sending|authorize-targets/.test(c.path)),false);
});
test("OAuth failure keeps the configured app and resumes login without another registration",async()=>{
 const api=guidedFixture(),ui=await connectionDraft(api);await ui.el("connection-setup-start").emit("click");
 api.profiles=[{name:setupProfile.profile,brand:"feishu"}];api.authView=configuredView("failed","authorization",{error_code:"permission_denied"});
 await ui.clock.advance(1500);
 assert.match(ui.el("connections-feedback").textContent,/应用已配置.*读取权限不足.*继续授权/);
 assert.equal(ui.el("connection-login").textContent,"继续账号授权");assert.equal(ui.el("connection-login").hidden,false);assert.equal(ui.el("connection-save").hidden,true);
 await ui.el("connection-form").emit("submit");assert.equal(api.calls.some(c=>c.path.endsWith("/connections/save")),false);
 api.authView={session_id:"auth-session",status:"waiting",verification_url:"https://accounts.feishu.cn/oauth",qrcode_url:"/v1/platform/connections/lark/qrcode?session_id=auth-session"};
 await ui.el("connection-login").emit("click");
 assert.equal(api.calls.find(c=>c.path.endsWith("/lark/login")).body.cli_profile.profile,setupProfile.profile);
 api.profiles[0].user="恢复用户";api.authView={session_id:"auth-session",status:"succeeded",username:"恢复用户"};await ui.clock.advance(1500);
 assert.equal(api.calls.filter(c=>c.path.endsWith("/lark/setup")).length,1);
 assert.equal(ui.el("connection-save").hidden,false);assert.match(ui.el("connection-account").textContent,/恢复用户/);
});
test("failed application results and missing returned profiles never select another account",async()=>{
 for(const view of [configuredView("failed","application",{error_code:"identity_mismatch"}),configuredView("succeeded","complete",{username:"本次用户"})]){
  const api=guidedFixture(),ui=await connectionDraft(api);await ui.el("connection-setup-start").emit("click");
  api.profiles=[{name:"unrelated",brand:"feishu",user:"其他用户"}];api.authView=view;await ui.clock.advance(1500);
  assert.equal(ui.el("connection-profile").value,"");assert.equal(ui.el("connection-profile-choice").hidden,false);assert.equal(ui.el("connection-save").hidden,true);
  assert.equal(ui.el("connections-feedback").dataset.kind,"error");assert.equal(api.calls.some(c=>c.path.endsWith("/lark/login")),false);
  await ui.el("connection-recheck").emit("click");assert.equal(ui.el("connection-profile").value,"");
 }
});
test("mismatched application remains blocked until explicit detection or selection",async()=>{
 const api=guidedFixture(),ui=await connectionDraft(api);await ui.el("connection-setup-start").emit("click");
 api.profiles=[{name:setupProfile.profile,brand:"feishu",user:"已有用户"}];api.authView=configuredView("failed","application",{error_code:"configuration_unconfirmed"});await ui.clock.advance(1500);
 assert.equal(ui.el("connection-login").hidden,true);assert.equal(ui.el("connection-save").hidden,true);assert.equal(ui.el("connection-sending").hidden,true);
 assert.match(ui.el("connections-feedback").textContent,/已发现本机配置.*结果未确认.*重新检测/);
 await ui.el("connection-recheck").emit("click");assert.equal(ui.el("connection-account-manage").hidden,false);
 assert.equal(api.calls.some(c=>c.path.endsWith("/lark/login")),false);
});
test("expired and cancelled setup explicitly report found configuration and preserve the name",async()=>{
 for(const status of ["expired","cancelled"]){
  const api=guidedFixture(),ui=await connectionDraft(api);ui.el("connection-name").value="保留草稿";await ui.el("connection-setup-start").emit("click");
  api.authView={session_id:"setup-session",stage:"application",status,error_code:status==="expired"?"timeout":"cancelled"};await ui.clock.advance(1500);
  assert.match(ui.el("connections-feedback").textContent,status==="expired"?/应用配置已过期.*尚未确认/:/应用配置已取消.*尚未确认/);
  assert.equal(ui.el("connection-name").value,"保留草稿");assert.equal(ui.el("connection-login-qr").src,"");
  assert.equal(ui.el("connection-recheck").hidden,false);assert.equal(api.calls.filter(c=>c.path.endsWith("/lark/setup")).length,1);
 }
 const api=guidedFixture(),ui=await connectionDraft(api);await ui.el("connection-setup-start").emit("click");
 api.profiles=[{name:setupProfile.profile,brand:"feishu"}];api.cancelView=configuredView("cancelled","authorization",{error_code:"cancelled"});
 await ui.el("connection-login-cancel").emit("click");assert.equal(ui.el("connection-login").textContent,"继续账号授权");
 assert.equal(ui.el("connection-profile").value,setupProfile.profile);assert.equal(ui.el("connection-login-qr").src,"");
 assert.equal(api.calls.filter(c=>c.path.endsWith("/lark/cancel")).length,1);
});
test("setup request failure requires detection before retry and fixed-version errors stay visible",async()=>{
 for(const code of ["cli_version_mismatch","busy",undefined]){
  const api=guidedFixture(),original=api.request;
  api.request=async(path,body,...args)=>{if(path.endsWith("/lark/setup")){api.calls.push({path,body});throw Object.assign(Error("failed"),{code});}return original(path,body,...args);};
  const ui=await connectionDraft(api);await ui.el("connection-setup-start").emit("click");await ui.el("connection-setup-start").emit("click");
  assert.equal(api.calls.filter(c=>c.path.endsWith("/lark/setup")).length,1);assert.equal(ui.el("connection-recheck").hidden,false);
  assert.match(ui.el("connections-feedback").textContent,code==="cli_version_mismatch"?/版本不匹配/:code==="busy"?/正在处理/:/结果未确认/);
  await ui.el("connection-recheck").emit("click");assert.equal(ui.el("connection-setup-start").disabled,false);
 }
});
test("setup rejects duplicate starts while the initial response is pending",async()=>{
 const api=guidedFixture(),original=api.request;let release;
 api.request=async(path,body,...args)=>{if(path.endsWith("/lark/setup")){const result=await original(path,body,...args);await new Promise(r=>release=r);return result;}return original(path,body,...args);};
 const ui=await connectionDraft(api),starting=ui.el("connection-setup-start").emit("click");await settle();
 await ui.el("connection-setup-start").emit("click");assert.equal(api.calls.filter(c=>c.path.endsWith("/lark/setup")).length,1);
 release();await starting;assert.equal(ui.el("connection-login-panel").hidden,false);
});
test("closing setup cancels waiting, ignores an outstanding status and rediscovers when reopened",async()=>{
 const api=guidedFixture(),original=api.request,ui=await connectionDraft(api);await ui.el("connection-setup-start").emit("click");let release;
 api.request=async(path,body,...args)=>path.endsWith("/lark/status")?await new Promise(r=>release=r):original(path,body,...args);
 const reading=ui.clock.advance(1500);await settle();await ui.el("close-settings").emit("click");
 release(configuredView("succeeded","complete",{username:"晚到用户"}));await reading;
 assert.equal(ui.el("connection-login-panel").hidden,true);assert.equal(ui.el("connection-login-link").href,"");
 assert.equal(api.calls.filter(c=>c.path.endsWith("/lark/cancel")).length,1);
 api.profiles=[{name:setupProfile.profile,brand:"feishu"}];api.request=original;
 await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");ui.el("connection-provider").value="feishu";await ui.el("connection-provider").emit("change");
 assert.equal(ui.el("connection-profile").value,setupProfile.profile);assert.equal(ui.el("connection-login").hidden,false);
 assert.equal(api.calls.filter(c=>c.path.endsWith("/lark/setup")).length,1);
});
test("pagehide uses keepalive cancellation and a late start response is cancelled with its owner",async()=>{
 const api=guidedFixture(),ui=await connectionDraft(api);await ui.el("connection-setup-start").emit("click");
 const originalFetch=globalThis.fetch,requests=[];
 try {
  globalThis.fetch=async(path,options)=>{requests.push({path,...options});return {};};await ui.window.emit("pagehide");
 } finally {globalThis.fetch=originalFetch;}
 assert.equal(requests.length,1);assert.equal(requests[0].keepalive,true);assert.equal(requests[0].credentials,"same-origin");
 assert.equal(JSON.parse(requests[0].body).session_id,"setup-session");assert.equal(ui.el("connection-login-qr").src,"");
 const late=guidedFixture(),original=late.request;let release;
 late.request=async(path,body,...args)=>{if(path.endsWith("/lark/setup")){const result=await original(path,body,...args);await new Promise(r=>release=r);return result;}return original(path,body,...args);};
 const reopened=await connectionDraft(late),starting=reopened.el("connection-setup-start").emit("click");await settle();
 await reopened.window.emit("pagehide");release();await starting;await settle();
 assert.equal(late.calls.filter(c=>c.path.endsWith("/lark/cancel")).length,1);assert.equal(reopened.el("connection-login-panel").hidden,true);
});

test("a changed user after guided verification cannot be saved as the verified account",async()=>{
 const api=guidedFixture(),ui=await connectionDraft(api);await ui.el("connection-setup-start").emit("click");
 api.profiles=[{name:setupProfile.profile,brand:"feishu",user:"其他用户"}];api.authView=configuredView("succeeded","complete",{username:"本次用户"});await ui.clock.advance(1500);
 assert.equal(ui.el("connection-save").hidden,true);assert.equal(ui.el("connection-login").hidden,true);
 assert.match(ui.el("connections-feedback").textContent,/账号与核验结果不符.*连接未保存/);
 await ui.el("connection-form").emit("submit");assert.equal(api.calls.some(c=>c.path.endsWith("/connections/save")),false);
});
test("status read failure cancels setup before recovery and never retries creation",async()=>{
 const api=guidedFixture(),ui=await connectionDraft(api),original=api.request;await ui.el("connection-setup-start").emit("click");
 api.request=async(path,body,...args)=>{if(path.endsWith("/lark/status"))throw Error("network");return original(path,body,...args);};
 api.cancelView=configuredView("cancelled","authorization",{error_code:"cancelled"});api.profiles=[{name:setupProfile.profile,brand:"feishu"}];
 await ui.clock.advance(1500);
 assert.equal(api.calls.filter(c=>c.path.endsWith("/lark/cancel")).length,1);assert.equal(ui.el("connection-login-qr").src,"");
 assert.match(ui.el("connections-feedback").textContent,/结果未确认.*重新检测/);
 await ui.el("connection-recheck").emit("click");assert.equal(ui.el("connection-profile").value,setupProfile.profile);
 assert.equal(api.calls.filter(c=>c.path.endsWith("/lark/setup")).length,1);
});

test("cancellation that settles a completed setup reports success and later logout clears its state",async()=>{
 const api=guidedFixture(),ui=await connectionDraft(api);await ui.el("connection-setup-start").emit("click");
 api.profiles=[{name:setupProfile.profile,brand:"feishu",user:"本次用户"}];api.cancelView=configuredView("succeeded","complete",{username:"本次用户"});
 await ui.el("connection-login-cancel").emit("click");assert.match(ui.el("connections-feedback").textContent,/取消前授权已完成.*保存连接/);
 assert.equal(ui.el("connection-account-state").textContent,"已验证");assert.equal(ui.el("connection-save").disabled,false);
 await ui.el("connection-logout").emit("click");api.profiles[0].user=undefined;api.authView={session_id:"auth-session",status:"succeeded"};await ui.el("connection-auth-confirm").emit("click");
 assert.match(ui.el("connections-feedback").textContent,/已退出/);assert.equal(ui.el("connection-save").hidden,true);
});

test("connected Lark form separates application and account and hides routine configuration",async()=>{
 const api=fixture();api.profiles[0].appId="cli_distinct_app";
 api.connections[0].cli_profile={version:1,executable:"/fixture/lark-cli",profile:"explicit",brand:"feishu",cli_version:"1.0.99"};
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connections-list").children[0].emit("click");
 assert.equal(ui.el("connection-application-name").textContent,"cli_distinct_app");assert.equal(ui.el("connection-account").textContent,"测试用户");
 assert.equal(ui.el("connection-account-state").textContent,"已验证");assert.equal(ui.el("connection-notes").hidden,true);
 assert.equal(ui.el("connection-guided").hidden,true);assert.equal(ui.el("connection-advanced").hidden,true);assert.equal(ui.el("connection-verify").hidden,true);
 assert.equal(ui.el("connection-account-manage").hidden,false);assert.equal(ui.el("connection-account-manage").open,false);
 assert.equal(ui.el("connection-login").hidden,true);assert.equal(ui.el("connection-save").disabled,true);
 ui.el("connection-name").value="改名";await ui.el("connection-name").emit("input");assert.equal(ui.el("connection-save").disabled,false);
 ui.el("connection-account-manage").open=true;await ui.el("connection-reauthorize").emit("click");
 assert.equal(ui.el("connection-auth-confirmation").hidden,false);assert.equal(ui.el("connection-account-manage").open,false);
 assert.equal(api.calls.some(c=>c.path.endsWith("/lark/login")),false);
});
test("application chooser opens deliberately, preserves the name and applies only to the draft",async()=>{
 const api=fixture();api.profiles[0].appId="cli_first";api.profiles.push({name:"second",brand:"feishu",appId:"cli_second",user:"第二用户"});
 const ui=await connectionDraft(api);ui.el("connection-name").value="名称草稿";
 assert.equal(ui.el("connection-application-picker").hidden,true);assert.equal(ui.el("connection-profile").value,"");
 await ui.el("connection-application-change").emit("click");assert.equal(ui.el("connection-application-picker").hidden,false);
 assert.equal(ui.el("connection-profile").options[1].textContent,"cli_first");
 ui.el("connection-profile").value="second";await ui.el("connection-profile").emit("change");
 assert.equal(ui.el("connection-application-picker").hidden,true);assert.equal(ui.el("connection-application-name").textContent,"cli_second");
 assert.equal(ui.el("connection-account").textContent,"第二用户");assert.equal(ui.el("connection-name").value,"名称草稿");
 assert.equal(api.calls.some(c=>/connections\/save|lark\/login|lark\/setup/.test(c.path)),false);
 await ui.el("connection-application-change").emit("click");await ui.el("connection-application-dismiss").emit("click");
 assert.equal(ui.el("connection-profile").value,"second");
});
test("expired credentials show an immediate authorization action while tool failures reveal repair",async()=>{
 const api=fixture();api.profiles[0].tokenStatus="expired";const ui=await connectionDraft(api);
 assert.equal(ui.el("connection-login").hidden,false);assert.equal(ui.el("connection-login").textContent,"重新授权");
 assert.equal(ui.el("connection-account-state").textContent,"需要重新授权");assert.equal(ui.el("connection-advanced").hidden,false);
 api.profiles[0].tokenStatus="no_token";await ui.el("connection-detect").emit("click");assert.equal(ui.el("connection-login").hidden,false);assert.equal(ui.el("connection-account-state").textContent,"需要重新授权");
 api.profiles[0].tokenStatus="valid";await ui.el("connection-detect").emit("click");assert.equal(ui.el("connection-advanced").hidden,true);
 const failed=fixture(),original=failed.request;failed.request=async(path,body,...args)=>{if(path.endsWith("/lark/discover"))throw Object.assign(Error("version"),{code:"cli_version_mismatch"});return original(path,body,...args);};
 const broken=await connectionDraft(failed);assert.equal(broken.el("connection-advanced").hidden,false);assert.equal(broken.el("connection-advanced").open,true);
 assert.match(broken.el("connections-feedback").textContent,/版本不匹配/);assert.equal(broken.el("connection-guided").hidden,true);
});

test("saved-profile authorization only mentions other connections when they exist",async()=>{
 for(const shared of [false,true]){
  const api=fixture(),profile={version:1,executable:"/fixture/lark-cli",profile:"explicit",brand:"feishu",cli_version:"1.0.99"};
  api.connections[0].cli_profile=profile;
  if(shared)api.connections.push({...api.connections[0],id:"shared-connection",name:"另一个连接"});
  const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connections-list").children[0].emit("click");
  await ui.el("connection-reauthorize").emit("click");api.authView={session_id:"auth-session",status:"succeeded",username:"测试用户"};await ui.el("connection-auth-confirm").emit("click");
  assert.equal(ui.el("connections-feedback").textContent,shared?"账号已验证。另有 1 个共享连接需重新验证。":"账号已验证。");
  assert.equal(ui.el("connections-feedback-details").hidden,true);assert.equal(ui.el("connection-account-state").textContent,"已验证");
 }
});
test("renewal failure stays visible while its instructions are expandable and cleared by the next operation",async()=>{
 const api=fixture(),ui=await connectionDraft(api);await ui.el("connection-reauthorize").emit("click");
 api.authView={session_id:"auth-session",status:"succeeded",username:"测试用户",renewal_unavailable:true,expires_at:"2027-01-01T00:00:00Z"};await ui.clock.advance(1500);
 assert.match(ui.el("connections-feedback").textContent,/账号已验证.*请保存连接.*无法自动续期.*管理员/);
 assert.doesNotMatch(ui.el("connections-feedback").textContent,/user_access_token|发布新版本/);
 assert.equal(ui.el("connections-feedback-details").hidden,false);assert.equal(ui.el("connections-feedback-details").open,false);
 assert.match(ui.el("connections-feedback-detail").textContent,/到期.*刷新 user_access_token.*发布新版本.*重新授权/);
 ui.el("connections-feedback-details").open=true;await ui.el("connection-form").emit("submit");
 assert.match(ui.el("connections-feedback").textContent,/连接已保存/);
 assert.equal(ui.el("connections-feedback-details").hidden,true);assert.equal(ui.el("connections-feedback-details").open,false);
 assert.equal(ui.el("connections-feedback-detail").textContent,"");
});
