import {test} from "node:test";
import assert from "node:assert/strict";
import {launch,settle} from "./web_app_harness.mjs";
function fixture(){
 const members=[{agent_id:"assistant",membership_id:"member-a",runner:{profile:"p"}}];
 const connection={id:"lark-connection",name:"飞书",provider:"lark",url:"https://open.feishu.cn",version:1,validation:{status:"valid",username:"测试用户",account:{id:{value:"ou_fixture"}}}};
 const api={calls:[],fail:false,data:{provider:"lark",channel:"messages",available:true,subscription_id:"",subscription_version:0,status:"disabled",pending:0,config:{enabled:false,connection_id:"",chats:[],target:{},private:true,group_mentions:true,at_all:false,bots:false,interval_seconds:60,backfill_seconds:0},lark:{cli_version:"1.0.99",chats:[]}},async request(path,body){
  api.calls.push({path,body:structuredClone(body)});
  if(path==="/v1/ui")return {managed:true};
  if(path==="/v1/rooms")return {service_epoch:"manager",operator:"tester",rooms:[{room_id:"A",room_instance:"instance-A",name:"A",status:"ready",runtime_status:"ready",service:{room_id:"A",room_instance:"instance-A",service_epoch:"active-A"}}],teams:[]};
  if(path.startsWith("/v1/workspace?"))return {workspace:{root:"workspaces"},workspace_version:1,members:[],credential_status:"missing"};
  if(path.startsWith("/v1/platform/notifications?"))return {subscriptions:[structuredClone(api.data)]};
  if(path.startsWith("/v1/platform/connections?"))return {connections:[connection]};
  if(path==="/v1/platform/connections/save")return {id:body.id,...body,version:1,validation:{status:"unverified"}};
  if(path==="/v1/platform/notifications/configure"){
   if(api.fail)throw Object.assign(Error("conflict"),{code:"conflict"});
   api.data={...api.data,config:body.config,subscription_id:"subscription",subscription_version:1,status:"running"};return structuredClone(api.data);
  }
  if(path==="/v1/member/list")return {members,latest_seq:0};
  if(path==="/v1/read")return {messages:[],snapshot_seq:0,has_more:false};
  throw Error(`Unexpected ${path}`);
 }};return api;
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
 assert.deepEqual(saved.config.chats,["oc_fixture"]);assert.equal(saved.config.at_all,false);assert.equal(saved.config.bots,false);
 assert.equal(ui.el("lark-save").disabled,true);assert.equal(ui.el("lark-sync").disabled,false);assert.equal(ui.el("lark-backfill").disabled,true);
});
test("Lark failed saves retain draft without automatic retry",async()=>{
 const api=fixture(),ui=await launch(api);await draft(ui);api.fail=true;await ui.el("lark-save").emit("click");await ui.refresh();
 assert.equal(api.calls.filter(x=>x.path==="/v1/platform/notifications/configure").length,1);
 assert.equal(ui.el("lark-chats").value,"oc_fixture");assert.match(ui.el("lark-error").textContent,/变化.*核对/);
});
test("Lark profile connection uses CLI reference with explicit format upgrade",async()=>{
 const api=fixture(),ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");
 ui.el("connection-provider").value="lark";await ui.el("connection-provider").emit("change");
 ui.el("connection-name").value="测试飞书";ui.el("connection-profile").value="explicit";ui.el("connection-executable").value="/fixture/lark-cli";
 ui.el("connection-brand").value="feishu";
 await ui.el("connection-form").emit("submit");const saved=api.calls.find(x=>x.path==="/v1/platform/connections/save").body;
 assert.equal(saved.provider,"lark");assert.equal(saved.snapshot_version,2);assert.equal(saved.cli_profile.cli_version,"1.0.99");assert.equal(saved.cli_profile.profile,"explicit");assert.equal(saved.pat,undefined);assert.equal(saved.token_env,undefined);
 assert.equal(ui.el("connection-form").hidden,true);assert.equal(ui.el("connections-list-view").hidden,false);
 assert.equal(ui.el("connections-feedback").dataset.kind,"success");assert.match(ui.el("connections-feedback").textContent,/连接已保存.*尚未验证/);
});
test("Lark missing profile identifies the unsaved field without submitting or clearing the draft",async()=>{
 const api=fixture(),ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");
 ui.el("connection-provider").value="lark";await ui.el("connection-provider").emit("change");
 ui.el("connection-name").value="我的飞书";ui.el("connection-executable").value="/fixture/lark-cli";
 let focused=false;ui.el("connection-profile").focus=()=>{focused=true;};
 await ui.el("connection-form").emit("submit");
 assert.equal(api.calls.filter(x=>x.path==="/v1/platform/connections/save").length,0);
 assert.equal(ui.el("connection-form").hidden,false);assert.equal(ui.el("connection-name").value,"我的飞书");assert.equal(ui.el("connection-executable").value,"/fixture/lark-cli");
 assert.equal(ui.el("connection-profile").attributes["aria-invalid"],"true");assert.equal(ui.el("connection-profile").attributes["aria-required"],"true");assert.equal(focused,true);
 assert.equal(ui.el("connections-feedback").dataset.kind,"error");assert.equal(ui.el("connections-feedback").attributes.role,"alert");
 assert.match(ui.el("connections-feedback").textContent,/连接未保存.*profile.*whoami/);
 ui.el("connection-profile").value="explicit";await ui.el("connection-form").emit("submit");
 assert.equal(ui.el("connection-profile").attributes["aria-invalid"],"false");assert.equal(ui.el("connection-form").hidden,true);
 assert.equal(ui.el("connections-feedback").dataset.kind,"success");
});
test("Lark invalid profile and relative executable are rejected locally",async()=>{
 const api=fixture(),ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");
 ui.el("connection-provider").value="lark";await ui.el("connection-provider").emit("change");ui.el("connection-name").value="飞书";
 ui.el("connection-profile").value="显示姓名";ui.el("connection-executable").value="/fixture/lark-cli";
 await ui.el("connection-form").emit("submit");assert.equal(ui.el("connection-profile").attributes["aria-invalid"],"true");
 ui.el("connection-profile").value="explicit";ui.el("connection-executable").value="lark-cli";
 await ui.el("connection-form").emit("submit");assert.equal(ui.el("connection-executable").attributes["aria-invalid"],"true");
 assert.match(ui.el("connections-feedback").textContent,/连接未保存.*绝对路径/);
 assert.equal(api.calls.filter(x=>x.path==="/v1/platform/connections/save").length,0);
});
test("Lark server rejection is visibly an error and never reports or retries success",async()=>{
 const api=fixture(),original=api.request;api.request=async(path,body)=>{if(path==="/v1/platform/connections/save"){api.calls.push({path,body});throw Object.assign(Error("invalid request"),{code:"request"});}return original(path,body);};
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");
 ui.el("connection-provider").value="lark";await ui.el("connection-provider").emit("change");
 ui.el("connection-name").value="飞书";ui.el("connection-profile").value="explicit";ui.el("connection-executable").value="/fixture/lark-cli";
 await ui.el("connection-form").emit("submit");await ui.refresh();
 assert.equal(ui.el("connection-form").hidden,false);assert.equal(ui.el("connection-profile").value,"explicit");
 assert.equal(ui.el("connections-feedback").dataset.kind,"error");assert.equal(ui.el("connections-feedback").attributes.role,"alert");
 assert.match(ui.el("connections-feedback").textContent,/连接未保存.*profile/);assert.doesNotMatch(ui.el("connections-feedback").textContent,/已保存/);
 assert.equal(api.calls.filter(x=>x.path==="/v1/platform/connections/save").length,1);
});
test("a late connection list read cannot erase a missing-profile error",async()=>{
 const api=fixture(),original=api.request;let finishRead;
 api.request=(path,body)=>path.startsWith("/v1/platform/connections?") ? new Promise(resolve=>{finishRead=resolve;}) : original(path,body);
 const ui=await launch(api),opening=ui.el("open-settings").emit("click");
 await ui.el("connection-new").emit("click");ui.el("connection-provider").value="lark";await ui.el("connection-provider").emit("change");
 ui.el("connection-name").value="飞书";ui.el("connection-executable").value="/fixture/lark-cli";
 await ui.el("connection-form").emit("submit");finishRead({connections:[]});await opening;
 assert.equal(ui.el("connections-feedback").dataset.kind,"error");assert.match(ui.el("connections-feedback").textContent,/连接未保存.*profile/);
 assert.equal(ui.el("connection-form").hidden,false);
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
