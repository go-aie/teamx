import {test} from "node:test";
import assert from "node:assert/strict";
import {launch,settle} from "./web_app_harness.mjs";
function fixture(){
 const members=[{agent_id:"assistant",membership_id:"member-a",runner:{profile:"p"}}];
 const connection={id:"lark-connection",name:"飞书",provider:"lark",url:"https://open.feishu.cn",version:1,validation:{status:"valid",username:"测试用户",account:{id:{value:"ou_fixture"}}}};
 const api={calls:[],fail:false,chats:[{chat_id:"oc_fixture",name:"产品讨论群",chat_mode:"group"},{chat_id:"oc_private",name:"张同学",chat_mode:"p2p"}],data:{provider:"lark",channel:"messages",available:true,subscription_id:"",subscription_version:0,status:"disabled",pending:0,config:{enabled:false,connection_id:"",chats:[],target:{},private:true,group_mentions:true,at_all:false,bots:true,interval_seconds:60,backfill_seconds:0},lark:{cli_version:"1.0.99",chats:[]}},async request(path,body){
  api.calls.push({path,body:structuredClone(body)});
  if(path==="/v1/platform/lark/chats")return {chats:structuredClone(api.chats),has_more:false,page_token:""};
  if(path==="/v1/ui")return {managed:true};
  if(path==="/v1/rooms")return {service_epoch:"manager",operator:"tester",rooms:[{room_id:"A",room_instance:"instance-A",name:"A",status:"ready",runtime_status:"ready",service:{room_id:"A",room_instance:"instance-A",service_epoch:"active-A"}}],teams:[]};
  if(path.startsWith("/v1/workspace?"))return {workspace:{root:"workspaces"},workspace_version:1,members:[],credential_status:"missing"};
  if(path.startsWith("/v1/platform/notifications?"))return {subscriptions:[structuredClone(api.data)]};
  if(path.startsWith("/v1/platform/connections?"))return {connections:api.connections};
  if(path==="/v1/platform/connections/lark/discover")return {executable:"/fixture/lark-cli",profiles:api.profiles};
  if(path==="/v1/platform/connections/lark/login" || path==="/v1/platform/connections/lark/logout")return {session_id:"auth-session",status:"starting",connections:[]};
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
 ui.el("lark-chats").value="oc_fixture";await ui.el("lark-chats").emit("change");
}
test("Lark specified-chat settings preserve drafts and submit frozen Membership",async()=>{
 const api=fixture(),ui=await launch(api);await draft(ui);
 assert.equal(ui.el("notification-panel-messages").hidden,false);assert.equal(ui.el("lark-save").disabled,false);
 await ui.refresh();assert.equal(ui.el("lark-chats").value,"oc_fixture");assert.equal(ui.el("lark-enabled").checked,true);
 await ui.el("lark-save").emit("click");const saved=api.calls.find(x=>x.path==="/v1/platform/notifications/configure").body;
 assert.equal(saved.provider,"lark");assert.equal(saved.channel,"messages");assert.equal(saved.subscription_version,0);assert.equal(saved.config.target.membership_id,"member-a");
 assert.deepEqual(saved.config.chats,["oc_fixture"]);assert.equal(saved.config.at_all,false);assert.equal(saved.config.bots,true);
 assert.equal(ui.el("lark-save").disabled,true);assert.equal(ui.el("lark-sync").disabled,false);assert.equal(ui.el("lark-backfill").disabled,true);
});
test("Lark failed saves retain draft without automatic retry",async()=>{
 const api=fixture(),ui=await launch(api);await draft(ui);api.fail=true;await ui.el("lark-save").emit("click");await ui.refresh();
 assert.equal(api.calls.filter(x=>x.path==="/v1/platform/notifications/configure").length,1);
 assert.equal(ui.el("lark-chats").value,"oc_fixture");assert.match(ui.el("lark-error").textContent,/变化.*核对/);
});
test("Lark group receiving mode survives refresh and save with private messages always included",async()=>{
 const api=fixture(),ui=await launch(api);await draft(ui);
 assert.equal(ui.el("lark-group-mode").value,"mentions");
 ui.el("lark-group-mode").value="all";await ui.el("lark-group-mode").emit("change");
 assert.equal(ui.el("lark-group").disabled,true);assert.equal(ui.el("lark-all").disabled,true);
 assert.equal(ui.el("lark-private"),undefined);assert.equal(ui.el("lark-bots"),undefined);
 await ui.refresh();assert.equal(ui.el("lark-group-mode").value,"all");
 await ui.el("lark-save").emit("click");
 const saved=api.calls.find(x=>x.path==="/v1/platform/notifications/configure").body;
 assert.equal(saved.config.group_all,true);assert.equal(saved.config.scope_mode,"specified");assert.deepEqual(saved.config.chats,["oc_fixture"]);
 assert.equal(ui.el("lark-group-mode").value,"all");assert.equal(ui.el("lark-save").disabled,true);
 ui.el("lark-group-mode").value="mentions";await ui.el("lark-group-mode").emit("change");
 assert.equal(ui.el("lark-save").disabled,false);assert.equal(ui.el("lark-group").disabled,false);assert.equal(ui.el("lark-all").disabled,false);
});
test("Lark switching to all chats clears group-all and requires explicit re-enabling on return",async()=>{
 const api=fixture(),ui=await launch(api);await draft(ui);
 ui.el("lark-group-mode").value="all";await ui.el("lark-group-mode").emit("change");
 ui.el("lark-scope").value="all";await ui.el("lark-scope").emit("change");
 assert.equal(ui.el("lark-group-mode").value,"mentions");assert.equal(ui.el("lark-group-mode-all").disabled,true);
 assert.equal(ui.el("lark-group").disabled,false);
 await ui.el("lark-save").emit("click");
 const saved=api.calls.find(x=>x.path==="/v1/platform/notifications/configure").body;
 assert.equal(saved.config.group_all,false);assert.equal(saved.config.scope_mode,"all");
 ui.el("lark-scope").value="specified";await ui.el("lark-scope").emit("change");
 assert.equal(ui.el("lark-group-mode-all").disabled,false);assert.equal(ui.el("lark-group-mode").value,"mentions");
});
async function connectionDraft(api,platform="feishu"){
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");
 ui.el("connection-provider").value=platform;await ui.el("connection-provider").emit("change");return ui;
}
test("Lark connection discovers the CLI and profile, derives origin and verifies after saving",async()=>{
 const api=fixture(),ui=await connectionDraft(api);
 assert.equal(ui.el("connection-url-row").hidden,true);assert.equal(ui.el("connection-url").required,false);
 assert.equal(ui.el("connection-profile").value,"explicit");assert.equal(ui.el("connection-profile-choice").hidden,true);
 assert.equal(ui.el("connection-executable").value,"/fixture/lark-cli");assert.equal(ui.el("connection-name").value,"飞书");
 assert.match(ui.el("connection-account").textContent,/测试用户/);
 await ui.el("connection-form").emit("submit");const saved=api.calls.find(x=>x.path==="/v1/platform/connections/save").body;
 assert.equal(saved.provider,"lark");assert.equal(saved.url,"https://open.feishu.cn");assert.equal(saved.snapshot_version,2);
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
 assert.match(ui.el("connections-feedback").textContent,/连接未保存.*选择应用配置/);
 ui.el("connection-profile").value="another";await ui.el("connection-profile").emit("change");
 await ui.el("connection-form").emit("submit");assert.equal(api.calls.find(x=>x.path==="/v1/platform/connections/save").body.cli_profile.profile,"another");
});
test("missing application configuration shows setup guidance and preserves the connection draft",async()=>{
 const api=fixture();api.profiles=[];const ui=await connectionDraft(api);ui.el("connection-name").value="我的飞书";
 assert.equal(ui.el("connection-setup").hidden,false);assert.match(ui.el("connection-setup-command").textContent,/config init --new --brand feishu/);
 assert.equal(ui.el("connection-login").hidden,true);await ui.el("connection-form").emit("submit");
 assert.equal(ui.el("connection-name").value,"我的飞书");assert.equal(ui.el("connection-form").hidden,false);
 assert.equal(ui.el("connections-feedback").dataset.kind,"error");assert.match(ui.el("connections-feedback").textContent,/连接未保存/);
 api.profiles=[{name:"new-app",brand:"feishu"}];await ui.el("connection-detect").emit("click");
 assert.equal(ui.el("connection-profile").value,"new-app");assert.equal(ui.el("connection-login").hidden,false);
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
 assert.match(ui.el("connections-feedback").textContent,/登录与身份验证成功/);
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
 let prompt="";api.confirm=text=>{prompt=text;return true;};api.authView={session_id:"auth-session",status:"succeeded"};
 const ui=await connectionDraft(api);await ui.el("connection-logout").emit("click");
 assert.match(prompt,/个人助手/);assert.match(prompt,/不等于取消应用/);
 const body=api.calls.find(x=>x.path==="/v1/platform/connections/lark/logout").body;
 assert.deepEqual(body.affected,[{id:"lark-connection",version:1,referenced_rooms:[{room_id:"A",room_instance:"instance-A"}]}]);
 assert.match(ui.el("connections-feedback").textContent,/已退出.*授权管理/);
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
 assert.equal(ui.el("lark-scope").value,"specified");
 ui.el("lark-scope").value="all";await ui.el("lark-scope").emit("change");
 assert.equal(ui.el("lark-chats").disabled,true);assert.equal(ui.el("lark-save").disabled,false);
 await ui.refresh();assert.equal(ui.el("lark-scope").value,"all");
 await ui.el("lark-save").emit("click");const saved=api.calls.find(x=>x.path==="/v1/platform/notifications/configure").body;
 assert.equal(saved.config.scope_mode,"all");assert.deepEqual(saved.config.chats,[]);
 assert.equal(saved.config.target.membership_id,"member-a");
});
test("Lark discovery and per-chat failures are visible without claiming full coverage",async()=>{
 const api=fixture();api.data={...api.data,subscription_id:"subscription",subscription_version:1,status:"running",lark:{directory_complete:false,directory_scanning:true,directory_pages:2,failed_chats:1,unlisted_chats:3,gap_chats:2,oldest_lag_seconds:90000,chats:[{chat_id:"oc_blocked",error_code:"permission_denied"}]}};
 const ui=await launch(api);await ui.el("open-lark-source").emit("click");await settle();
 assert.match(ui.el("lark-diagnostics").textContent,/目录 未完成（2 页）/);
 assert.match(ui.el("lark-diagnostics").textContent,/读取受阻 1/);
 assert.match(ui.el("lark-diagnostics").textContent,/积压超过 24 小时 2/);
 assert.match(ui.el("lark-chat-status").children[0].textContent,/读取受阻 permission_denied/);
});

async function openChatPicker(ui){
 ui.el("lark-chat-picker").open=true;await ui.el("lark-chat-picker").emit("toggle");await settle();
}
function chatChoice(ui,id){return ui.el("lark-chat-options").children.find(row=>row.children[0].value===id)?.children[0];}
test("Lark conversation dropdown selects names and types, searches and saves native IDs",async()=>{
 const api=fixture(),ui=await launch(api);await draft(ui);ui.el("lark-chats").value="";
 await openChatPicker(ui);
 const browse=api.calls.find(x=>x.path==="/v1/platform/lark/chats").body;
 assert.equal(browse.connection_id,"lark-connection");assert.equal(browse.connection_version,1);assert.equal(browse.caller.kind,"human");assert.equal(browse.room_instance,"instance-A");
 assert.match(ui.el("lark-chat-picker-status").textContent,/已加载完成/);
 ui.el("lark-chat-search").value="张";await ui.el("lark-chat-search").emit("input");
 assert.equal(ui.el("lark-chat-options").children.length,1);assert.ok(chatChoice(ui,"oc_private"));
 const privateChat=chatChoice(ui,"oc_private");privateChat.checked=true;await privateChat.emit("change");
 assert.match(ui.el("lark-chat-selected").children[0].textContent,/张同学/);
 ui.el("lark-chat-search").value="";await ui.el("lark-chat-search").emit("input");
 ui.el("lark-chat-type").value="group";await ui.el("lark-chat-type").emit("change");
 assert.equal(ui.el("lark-chat-options").children.length,1);assert.ok(chatChoice(ui,"oc_fixture"));
 const group=chatChoice(ui,"oc_fixture");group.checked=true;await group.emit("change");
 await ui.refresh();assert.equal(ui.el("lark-chat-selected").children.length,2);
 await ui.el("lark-save").emit("click");
 const saved=api.calls.find(x=>x.path==="/v1/platform/notifications/configure").body.config;
 assert.deepEqual(saved.chats,["oc_fixture","oc_private"]);assert.equal(saved.private,true);assert.equal(saved.bots,true);
 await ui.el("lark-chat-selected").children[0].emit("click");assert.equal(ui.el("lark-chat-selected").children.length,1);
});
test("Lark dropdown pagination and failures preserve selection and report incomplete search",async()=>{
 const api=fixture(),original=api.request;let fail=false;
 api.request=async(path,body)=>{
  if(path!=="/v1/platform/lark/chats")return original(path,body);
  api.calls.push({path,body});if(fail)throw Error("read failed");
  return body.page_token ? {chats:[{chat_id:"oc_next",chat_mode:"p2p",name:"下一页同事"}],has_more:false,page_token:""} : {chats:[api.chats[0]],has_more:true,page_token:"next-page"};
 };
 const ui=await launch(api);await draft(ui);await openChatPicker(ui);
 assert.match(ui.el("lark-chat-picker-status").textContent,/尚未加载完整.*搜索仅筛选已加载会话/);
 fail=true;await ui.el("lark-chat-more").emit("click");
 assert.match(ui.el("lark-chat-picker-status").textContent,/读取失败.*已选会话保留/);assert.equal(ui.el("lark-chats").value,"oc_fixture");
 fail=false;await ui.el("lark-chat-more").emit("click");
 assert.equal(api.calls.filter(x=>x.path==="/v1/platform/lark/chats").at(-1).body.page_token,"next-page");
 assert.ok(chatChoice(ui,"oc_next"));assert.equal(ui.el("lark-chat-more").hidden,true);
});
test("Lark dropdown limits selection and discards a late result after connection changes",async()=>{
 const api=fixture(),original=api.request;let finish;
 api.request=(path,body)=>path==="/v1/platform/lark/chats" ? new Promise(resolve=>{finish=resolve;}) : original(path,body);
 const ui=await launch(api);await draft(ui);
 ui.el("lark-chats").value=Array.from({length:32},(_,i)=>`oc_${i}`).join("\n");await ui.el("lark-chats").emit("change");
 ui.el("lark-chat-picker").open=true;const opening=ui.el("lark-chat-picker").emit("toggle");await settle();
 finish({chats:api.chats,has_more:false,page_token:""});await opening;
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

function localDateTime(date){
 const pad=n=>String(n).padStart(2,"0");
 return `${date.getFullYear()}-${pad(date.getMonth()+1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
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
 assert.equal(ui.el("lark-backfill").disabled,true);assert.match(ui.el("lark-backfill-help").textContent,/当前接收起点.*已锁定.*新建房间/);
 ui.el("lark-interval").value="10";await ui.el("lark-interval").emit("change");
 assert.equal(ui.el("lark-save").disabled,false);await ui.el("lark-save").emit("click");
 const saved=api.calls.find(x=>x.path==="/v1/platform/notifications/configure").body;
 assert.equal(saved.subscription_id,"existing");assert.equal(saved.config.interval_seconds,10);assert.equal(saved.config.backfill_start,start.toISOString());
 assert.equal(ui.el("lark-backfill-start").disabled,true);
});
