import {test} from "node:test";
import assert from "node:assert/strict";
import {launch,settle} from "./web_app_harness.mjs";

function fixture(count=1) {
  const members=Array.from({length:count},(_,i)=>({agent_id:`agent-${i}`,membership_id:`member-${i}`,runner:{kind:"dsh",profile:"p"},status:"idle"}));
  const room={room_id:"R",room_instance:"instance-R",name:"Review",status:"ready",service:{room_id:"R",room_instance:"instance-R",service_epoch:"runtime"}};
  const api={calls:[],members,ops:new Map(),platforms:[],epoch:"manager",hook:null,
    prepared:{status:"ready",application_id:"app",client_id:"client-A",scopes:["api","write_repository"],affected:[],redirect_uri:"https://teamx.invalid/v1/platforms/gitlab/callback",applications_url:"https://gitlab.invalid/-/user_settings/applications"},
    auth:{status:"waiting"},async request(path,body) {
      api.calls.push({path,body:structuredClone(body)});
      const overridden=api.hook?.(path,body);if(overridden!==undefined)return overridden;
      if(path==="/v1/ui")return {managed:true};
      if(path==="/v1/rooms")return {service_epoch:api.epoch,operator:"tester",rooms:[room],teams:[]};
      if(path.startsWith("/v1/workspace?"))return {workspace:{root:"workspaces"},workspace_version:1,members:[],credential_status:"missing"};
      if(path.startsWith("/v1/platform/notifications?"))return {subscriptions:[],gitlab_platforms:[]};
      if(path.startsWith("/v1/platforms?"))return {platforms:structuredClone(api.platforms)};
      if(path==="/v1/member/list")return {members:structuredClone(api.members),latest_seq:0};
      if(path==="/v1/member/activity")return {member:members.find(m=>m.membership_id===body.membership_id)};
      if(path==="/v1/read")return {messages:[],snapshot_seq:0,has_more:false};
      if(path.startsWith("/v1/tools/bindings?"))return {revision:0,bindings:[],catalog:[]};
      if(path.startsWith("/v1/platforms/onboarding/targets?"))return {members:api.members.map(m=>({...m,room_id:room.room_id,room_instance:room.room_instance}))};
      if(path==="/v1/platforms/onboarding/prepare") {
        if(!api.ops.has(body.operation_id)) {
          const {service_epoch,caller,operation_id,...input}=body;
          api.ops.set(operation_id,{schema:1,operation_id,input,platform_id:input.platform_id || "a".repeat(32),connection:{status:"waiting"},notifications:{status:input.only_platform ? "skipped":"pending"},tool:{status:input.only_platform ? "skipped":"pending"}});
        }
        return structuredClone(api.ops.get(body.operation_id));
      }
      if(path==="/v1/platforms/onboarding/status")return structuredClone(api.ops.get(body.operation_id));
      if(path==="/v1/platforms/onboarding/apply") {
        const op=api.ops.get(body.operation_id);op.connection={status:"connected",version:2};op.notifications={status:"preserved",version:3};op.tool={status:"applied",version:4,error_code:"executable_unavailable"};return structuredClone(op);
      }
      if(path==="/v1/platforms/onboarding/cancel") {const op=api.ops.get(body.operation_id);op.cancelled=true;return structuredClone(op);}
      if(path==="/v1/platforms/gitlab/prepare")return structuredClone(api.prepared);
      if(path==="/v1/platforms/gitlab/login") {
        api.login=structuredClone(body);
        return {session_id:"session",platform_id:"a".repeat(32),onboarding_id:body.onboarding_id,status:"waiting",authorization_url:"https://gitlab.invalid/oauth/authorize?state=secret-url"};
      }
      if(path==="/v1/platforms/gitlab/status")return structuredClone(api.auth);
      if(path==="/v1/platforms/gitlab/cancel")return {status:"cancelled"};
      if(path==="/v1/platforms/lark/discover")return {executable:"/service/lark-cli",profiles:[{name:"profile",brand:"feishu",user:"Alice",tokenStatus:"valid"}]};
      if(path==="/v1/platforms/save") {
        const op=api.ops.get(body.onboarding_id);op.connection={status:"connected",version:1};op.notifications={status:"applied",version:1};op.tool={status:"applied",version:1};
        const item={...body,version:1,validation:{status:"valid",username:"Alice"},references:[],onboarding:structuredClone(op)};api.platforms.push(item);return item;
      }
      throw Error(`Unexpected ${path}`);
    }};
  return api;
}
async function add(api,saved) {
  const ui=await launch(api,saved);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");
  ui.el("connection-name").value="Company GitLab";ui.el("connection-url").value="https://gitlab.invalid";
  await ui.el("connection-url").emit("change");return ui;
}
const calls=(api,path)=>api.calls.filter(c=>c.path===path);

test("one eligible member is prefilled and a compatible GitLab application logs in with one durable operation",async()=>{
  const api=fixture(),ui=await add(api);
  assert.equal(ui.el("onboarding-room").value,"instance-R");assert.equal(ui.el("onboarding-member").value,"member-0");
  assert.match(ui.el("onboarding-summary").textContent,/我的待办.*Inbox.*启用/);
  assert.equal(ui.el("gitlab-setup").hidden,true);assert.equal(ui.el("gitlab-login").hidden,false);assert.match(ui.el("gitlab-reuse-summary").textContent,/复用.*账号需单独授权/);
  await ui.el("gitlab-login").emit("click");
  const body=calls(api,"/v1/platforms/onboarding/prepare")[0].body;
  assert.equal(body.membership_id,"member-0");assert.equal(body.room_instance,"instance-R");assert.equal(body.agent_id,"agent-0");assert.equal(body.provider,"gitlab");
  assert.equal(body.notifications,undefined);assert.equal(body.tool,undefined);
  assert.equal(api.login.onboarding_id,body.operation_id);assert.deepEqual(api.login.scopes,["api","write_repository"]);
  assert.equal(calls(api,"/v1/platforms/gitlab/initialize").length,0);assert.equal(calls(api,"/v1/tools/bind").length,0);assert.equal(calls(api,"/v1/platform/notifications/configure").length,0);
  assert.doesNotMatch(JSON.stringify([...ui.saved]),/secret-url|authorization_url|client_secret/);
  assert.equal(ui.el("onboarding-member").disabled,true);
});

test("multiple members require one explicit choice and the member entry fixes complete identity",async()=>{
  const api=fixture(2),ui=await add(api);
  assert.equal(ui.el("onboarding-member").value,"");await ui.el("gitlab-login").emit("click");
  assert.equal(calls(api,"/v1/platforms/gitlab/login").length,0);assert.match(ui.el("onboarding-target-error").textContent,/选择/);
  ui.el("onboarding-member").value="member-1";await ui.el("onboarding-member").emit("change");await ui.el("gitlab-login").emit("click");
  assert.equal(calls(api,"/v1/platforms/onboarding/prepare")[0].body.membership_id,"member-1");
  await ui.el("close-settings").emit("click");await ui.el("open-settings").emit("click");await ui.el("onboarding-cancel").emit("click");
  await ui.el("close-settings").emit("click");
  await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();await ui.el("agent-tab-tools").emit("click");
  await ui.el("cli-member-add-platform").emit("click");
  assert.equal(ui.el("onboarding-member").value,"member-0");assert.equal(ui.el("onboarding-member").disabled,true);assert.equal(ui.el("onboarding-room").disabled,true);
});

test("only adding a platform omits member identity; explicit choices and history use P5 config",async()=>{
  const api=fixture(),ui=await add(api);
  ui.el("onboarding-only").checked=true;await ui.el("onboarding-only").emit("change");await ui.el("gitlab-login").emit("click");
  const body=calls(api,"/v1/platforms/onboarding/prepare")[0].body;assert.equal(body.only_platform,true);assert.equal(body.membership_id,undefined);assert.equal(body.room_id,undefined);
  const api2=fixture(),ui2=await add(api2);
  ui2.el("onboarding-tool").checked=false;await ui2.el("onboarding-tool").emit("change");ui2.el("onboarding-backfill").value="3600";await ui2.el("onboarding-backfill").emit("change");await ui2.el("gitlab-login").emit("click");
  const custom=calls(api2,"/v1/platforms/onboarding/prepare")[0].body;
  assert.equal(custom.tool,false);assert.equal(custom.gitlab.backfill_seconds,3600);assert.equal(custom.gitlab.todos.enabled,true);assert.equal(custom.gitlab.events.enabled,false);
});

test("GitLab multiple applications are selected explicitly and incompatible applications never import or log in",async()=>{
  const api=fixture();api.prepared={...api.prepared,status:"application_selection_required",application_id:undefined,applications:["A","B"].map(id=>({application_id:id,client_id:`client-${id}`,callback_compatible:true,missing_scopes:[]}))};
  const ui=await add(api);assert.equal(ui.el("gitlab-application-choice").hidden,false);assert.equal(ui.el("gitlab-application").value,"");assert.equal(ui.el("gitlab-import").disabled,true);
  api.hook=(path,body)=>path==="/v1/platforms/gitlab/prepare" && body.application_id==="B" ? {...api.prepared,status:"ready",application_id:"B",client_id:"client-B"}:undefined;
  ui.el("gitlab-application").value="B";await ui.el("gitlab-application").emit("change");await ui.el("gitlab-login").emit("click");assert.equal(api.login.application_id,"B");
  const api2=fixture();api2.prepared={...api2.prepared,status:"application_incompatible",application_id:undefined,applications:[{client_id:"old-client",callback_compatible:false,missing_scopes:["write_repository"]}]};
  const ui2=await add(api2);assert.match(ui2.el("gitlab-application-issues").children[0].textContent,/回调地址不匹配.*缺少权限 write_repository/);
  await ui2.el("gitlab-import").emit("click");assert.equal(calls(api2,"/v1/platforms/gitlab/login").length,0);assert.equal(calls(api2,"/v1/platforms/gitlab/initialize").length,0);
});

test("opting out of repository access automatically rereads compatibility without a separate check action",async()=>{
  const api=fixture();api.prepared={...api.prepared,status:"application_incompatible",application_id:undefined,applications:[{client_id:"api-only-client",callback_compatible:true,missing_scopes:["write_repository"]}]};
  api.hook=(path,body)=>path==="/v1/platforms/gitlab/prepare" && !body.scopes.includes("write_repository") ? {...api.prepared,status:"ready",application_id:"api-only-app",scopes:["api"]} : undefined;
  const ui=await add(api);
  assert.equal(ui.el("gitlab-import").disabled,true);
  ui.el("gitlab-scope-write-repository").checked=false;await ui.el("gitlab-scope-write-repository").emit("change");
  assert.equal(ui.el("gitlab-compatibility-details").hidden,true);
  assert.equal(ui.el("gitlab-login").hidden,false);
  await ui.el("gitlab-login").emit("click");
  assert.equal(api.login.application_id,"api-only-app");assert.deepEqual(api.login.scopes,["api"]);
});

test("duplicate GitLab accounts keep the result panel hidden and reuse the operation when another account connects",async()=>{
  const api=fixture(),ui=await add(api,undefined,true);
  api.auth={status:"failed",error_code:"account_already_connected",account_username:"Alice",cleanup_status:"manual_required"};
  await ui.el("gitlab-login").emit("click");
  const operationID=api.login.onboarding_id;
  assert.equal(ui.el("onboarding-result").hidden,true);
  assert.equal(ui.el("gitlab-account-choice").hidden,false);
  assert.equal(ui.el("platforms-feedback").textContent,"@Alice 已连接其他平台，请切换账号后重试。");
  assert.equal(ui.el("platforms-feedback-details").open,false);
  await ui.refresh();assert.equal(ui.el("onboarding-result").hidden,true);
  const op=api.ops.get(operationID);op.connection={status:"connected",version:1};
  const account={id:op.platform_id,name:"Company GitLab",provider:"gitlab",url:"https://gitlab.invalid",version:1,credential_kind:"oauth",authorization_status:"active",validation:{status:"valid",username:"Bob"},references:[]};
  api.platforms=[account];api.auth={status:"succeeded",platforms:[account]};
  await ui.el("gitlab-login").emit("click");
  assert.equal(api.login.onboarding_id,operationID);
  assert.equal(calls(api,"/v1/platforms/onboarding/prepare").length,1);
  assert.equal(ui.el("onboarding-result").hidden,false);
  assert.match(ui.el("onboarding-result-account").textContent,/@Bob/);
});

for (const exit of ["close-settings","connection-back","native-close"]) {
  test(`leaving an unfinished account login via ${exit} clears its setup record and allows a fresh connection`,async()=>{
    const api=fixture(),ui=await add(api,undefined,true);
    await ui.el("gitlab-login").emit("click");
    const id=api.login.onboarding_id;
    if(exit==="native-close") {ui.el("settings-dialog").close();await settle();}
    else await ui.el(exit).emit("click");
    if(exit!=="connection-back") await ui.el("open-settings").emit("click");
    assert.equal(api.ops.get(id).cancelled,true);
    assert.equal(ui.el("onboarding-result").hidden,true);
    assert.equal(ui.saved.has("teamx.platform-onboarding:tester"),false);
    assert.equal(calls(api,"/v1/platforms/gitlab/cancel").length,1);
    await ui.el("connection-new").emit("click");
    assert.equal(ui.el("connection-form").hidden,false);
    ui.el("connection-name").value="Another GitLab";ui.el("connection-url").value="https://gitlab.invalid";
    await ui.el("connection-url").emit("change");await ui.el("gitlab-login").emit("click");
    assert.notEqual(api.login.onboarding_id,id);
  });
}

test("reopening settings discards an old waiting record saved by an earlier page",async()=>{
  const api=fixture(),ui=await add(api);await ui.el("gitlab-login").emit("click");
  const id=api.login.onboarding_id,next=await launch(api,ui.saved);
  await next.el("open-settings").emit("click");
  assert.equal(api.ops.get(id).cancelled,true);
  assert.equal(next.el("onboarding-result").hidden,true);
  assert.equal(next.saved.has("teamx.platform-onboarding:tester"),false);
  assert.equal(calls(api,"/v1/platforms/gitlab/login").length,1);
});

test("leaving reconciles a late connected account and retains its unfinished notification receipt",async()=>{
  const api=fixture(),ui=await add(api);await ui.el("gitlab-login").emit("click");
  const id=api.login.onboarding_id,op=api.ops.get(id);
  op.connection={status:"connected",version:2};op.notifications={status:"failed",error_code:"settings_changed"};
  await ui.el("close-settings").emit("click");await ui.el("open-settings").emit("click");
  assert.equal(calls(api,"/v1/platforms/onboarding/cancel").length,0);
  assert.equal(ui.el("onboarding-result").hidden,false);
  assert.equal(ui.el("onboarding-apply").hidden,false);
  assert.equal(JSON.parse(ui.saved.get("teamx.platform-onboarding:tester")).operation_id,id);
});

for (const failure of ["status","cancel"]) {
  test(`an unconfirmed ${failure} keeps the saved operation until exit can be reconciled`,async()=>{
    const api=fixture(),ui=await add(api);await ui.el("gitlab-login").emit("click");
    const id=api.login.onboarding_id;
    api.hook=path=>path===`/v1/platforms/onboarding/${failure}` ? Promise.reject(Error("lost response")) : undefined;
    await ui.el("close-settings").emit("click");await ui.el("open-settings").emit("click");
    assert.equal(JSON.parse(ui.saved.get("teamx.platform-onboarding:tester")).operation_id,id);
    assert.equal(ui.el("onboarding-result").hidden,false);
    assert.match(ui.el("onboarding-result-error").textContent,/未确认/);
    api.hook=null;await ui.el("close-settings").emit("click");await ui.el("open-settings").emit("click");
    assert.equal(ui.el("onboarding-result").hidden,true);
    assert.equal(ui.saved.has("teamx.platform-onboarding:tester"),false);
  });
}

test("auth completion reports each part, and reload retries only the saved P5 operation with a fresh epoch",async()=>{
  const api=fixture(),ui=await add(api);await ui.el("gitlab-login").emit("click");
  const op=api.ops.get(api.login.onboarding_id);op.connection={status:"connected",version:2};op.notifications={status:"failed",error_code:"settings_changed"};op.tool={status:"applied",version:3,error_code:"executable_unavailable"};
  api.platforms=[{id:op.platform_id,name:"Company GitLab",provider:"gitlab",url:"https://gitlab.invalid",version:2,credential_kind:"oauth",authorization_status:"active",validation:{status:"valid",username:"Bob"},references:[]}];api.auth={status:"succeeded",platforms:api.platforms};
  await ui.clock.advance(1500);
  assert.match(ui.el("onboarding-steps").children[1].children[1].textContent,/设置已被修改/);assert.match(ui.el("onboarding-steps").children[2].children[1].textContent,/程序缺失.*已保存/);assert.match(ui.el("onboarding-result-account").textContent,/@Bob/);
  api.epoch="restarted";const next=await launch(api,ui.saved);await next.el("open-settings").emit("click");assert.equal(next.el("onboarding-apply").hidden,false);
  const logins=calls(api,"/v1/platforms/gitlab/login").length;await next.el("onboarding-apply").emit("click");
  const applied=calls(api,"/v1/platforms/onboarding/apply").at(-1).body;assert.deepEqual(applied,{service_epoch:"restarted",caller:{kind:"human",id:"tester"},operation_id:op.operation_id});
  assert.equal(calls(api,"/v1/platforms/gitlab/login").length,logins);assert.match(next.el("onboarding-steps").children[1].children[1].textContent,/保留原设置/);
});

test("Lark profile save hands the fixed operation to the server and does not open a second settings workflow",async()=>{
  const api=fixture(),ui=await add(api);ui.el("connection-provider").value="feishu";await ui.el("connection-provider").emit("change");
  await ui.el("connection-form").emit("submit");
  const saved=calls(api,"/v1/platforms/save")[0].body,prepared=calls(api,"/v1/platforms/onboarding/prepare")[0].body;
  assert.equal(saved.onboarding_id,prepared.operation_id);assert.equal(saved.id,api.ops.get(prepared.operation_id).platform_id);assert.equal(prepared.provider,"lark");assert.equal(prepared.origin,"https://open.feishu.cn");
  assert.equal(calls(api,"/v1/platforms/verify").length,0);assert.equal(calls(api,"/v1/tools/bind").length,0);assert.equal(calls(api,"/v1/platform/notifications/configure").length,0);
  assert.equal(ui.el("onboarding-steps").children[1].children[1].textContent,"已保存 · v1");
});

test("late member discovery cannot select a member in a different Room",async()=>{
  const api=fixture();let finish;api.hook=path=>path.startsWith("/v1/platforms/onboarding/targets?") ? new Promise(resolve=>{finish=resolve}):undefined;
  const ui=await launch(api);await ui.el("open-settings").emit("click");const adding=ui.el("connection-new").emit("click");await settle();
  ui.el("onboarding-room").value="";await ui.el("onboarding-room").emit("change");finish({members:api.members});await adding;
  assert.equal(ui.el("onboarding-member").value,"");assert.equal(ui.el("onboarding-member").options.length,1);
});

test("an unconfirmed prepare retries its exact saved request and cancellation leaves confirmed steps visible",async()=>{
 const api=fixture(),ui=await add(api);let lost=true;
 api.hook=(path,body)=>{if(path==="/v1/platforms/onboarding/prepare" && lost){lost=false;throw Error("lost prepare response");}};
 await ui.el("gitlab-login").emit("click");assert.equal(api.login,undefined);assert.equal(ui.el("onboarding-prepare-retry").hidden,false);
 const original=calls(api,"/v1/platforms/onboarding/prepare")[0].body;
 await ui.el("onboarding-prepare-retry").emit("click");assert.deepEqual(calls(api,"/v1/platforms/onboarding/prepare")[1].body,original);
 const op=api.ops.get(original.operation_id);op.connection={status:"connected",version:2};op.notifications={status:"applied",version:1};
 await ui.el("onboarding-resume").emit("click");await ui.el("onboarding-cancel").emit("click");
 assert.match(ui.el("onboarding-steps").children[1].children[1].textContent,/已保存/);assert.equal(ui.el("onboarding-continue-login").hidden,true);
 await ui.el("onboarding-dismiss").emit("click");assert.equal(ui.el("connection-form").hidden,true);assert.equal(ui.el("onboarding-result").hidden,true);
});

test("platform notification summaries toggle only the chosen platform and preserve its full saved configuration",async()=>{
 const api=fixture();
 api.platforms=["one","two"].map(id=>({id,provider:"gitlab",name:id,url:"https://gitlab.invalid",version:1,validation:{status:"valid",username:id}}));
 const views=api.platforms.map(p=>({platform_id:p.id,version:2,config:{platform_id:p.id,enabled:true,target:{mode:"inbox",agent_id:"agent-0",membership_id:"member-0"},interval_seconds:30,backfill_start:"2026-11-01T00:00:00Z",events:{enabled:false,project:"",filter:{types:[]}},todos:{enabled:true,filter:{reasons:["assigned"]}}},events:{config:{enabled:false},status:"disabled"},todos:{config:{enabled:true},status:"running"},pending:{},errors:{}}));
 api.hook=(path,body)=>{
  if(path.startsWith("/v1/platform/notifications?"))return {subscriptions:[],gitlab_platforms:structuredClone(views),gitlab:structuredClone(views[0])};
  if(path==="/v1/platform/gitlab/notifications/configure") {const v=views.find(v=>v.platform_id===body.platform_id);v.config=structuredClone(body.config);v.version++;return structuredClone(v);}
 };
 const ui=await launch(api);await ui.el("open-gitlab-source").emit("click");await settle();
 assert.equal(ui.el("gitlab-connection").hidden,true);assert.equal(ui.el("notification-platforms").children.length,2);
 const toggle=ui.el("notification-platforms").children[0].children[0].children[1].children[0];
 assert.equal(toggle.checked,true);assert.match(ui.el("notification-platforms").children[0].children[1].textContent,/我的待办.*agent-0 Inbox.*@one/);
 const before=structuredClone(views[0].config);toggle.checked=false;await toggle.emit("change");
 const sent=calls(api,"/v1/platform/gitlab/notifications/configure")[0].body;
 assert.equal(sent.platform_id,"one");assert.deepEqual(sent.config,{...before,enabled:false});assert.equal(views[1].config.enabled,true);
 assert.equal(ui.el("notification-platforms").children[0].children[0].children[1].children[0].checked,false);
});

test("invalid account-bearing origins are rejected before persisting a prepare request or sending PAT",async()=>{
 const api=fixture(),ui=await add(api);ui.el("connection-provider").value="scion";await ui.el("connection-provider").emit("change");
 ui.el("connection-url").value="https://user:private-url-password@scion.invalid";ui.el("connection-scion-pat").value="private-pat";
 await ui.el("connection-scion-login").emit("click");
 assert.equal(ui.el("connection-scion-pat").value,"");assert.equal(calls(api,"/v1/platforms/onboarding/prepare").length,0);assert.equal(calls(api,"/v1/platforms/scion/login").length,0);
 assert.doesNotMatch(JSON.stringify([...ui.saved]),/private-url-password|private-pat/);assert.match(ui.el("platforms-feedback").textContent,/HTTPS 实例地址/);
});
