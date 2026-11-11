import {test} from "node:test";
import assert from "node:assert/strict";
import {launch,settle,withOnboarding} from "./web_app_harness.mjs";
function fixture(kind="oauth") {
 const item={id:"original",name:"Company GitLab",provider:"gitlab",url:"https://gitlab.invalid",version:2,credential_kind:kind,credential_configured:true,authorization_status:kind==="oauth"?"active":undefined,validation:{status:"valid",username:"tester"},references:[{room_id:"R",room_instance:"instance",name:"Review"}]};
 const api={calls:[],items:[item],prepared:{status:"ready",application_id:"app",redirect_uri:"http://127.0.0.1:8088/v1/platforms/gitlab/callback",applications_url:"https://gitlab.invalid/-/user_settings/applications",affected:[{id:item.id,version:2,referenced_rooms:[{room_id:"R",room_instance:"instance"}]}]},status:{session_id:"session",platform_id:item.id,status:"waiting"},async request(path,body){
  api.calls.push({path,body:structuredClone(body)});
  if(path==="/v1/ui")return {managed:true};
  if(path==="/v1/rooms")return {service_epoch:"manager",operator:"tester",rooms:[],teams:[]};
  if(path.startsWith("/v1/platforms?"))return {platforms:structuredClone(api.items)};
  if(path.endsWith("/gitlab/prepare"))return structuredClone(body.edit_application ? api.editPrepared || api.prepared : api.prepared);
  if(path.endsWith("/gitlab/initialize"))return structuredClone({...api.prepared,...api.initialized,status:api.initialized?.status || "owner_verification_required",application_id:undefined,client_import_id:api.initialized?.client_import_id || "imported",client_id:body.client.client_id,scopes:body.client.scopes});
  if(path.endsWith("/gitlab/login"))return {session_id:"session",platform_id:api.targetID || item.id,status:"waiting",authorization_url:"https://gitlab.invalid/oauth/authorize?state=fixture-state"};
  if(path.endsWith("/gitlab/logout"))return {session_id:"session",platform_id:item.id,status:"disconnecting"};
  if(path.endsWith("/gitlab/status"))return typeof api.status==="function" ? api.status() : structuredClone(api.status);
  if(path.endsWith("/gitlab/cancel"))return {status:"cancelled"};
  if(path.endsWith("/platforms/save"))return {...item,...body,version:3};
  throw Error(`Unexpected ${path}`);
 }};return withOnboarding(api);
}
async function edit(api) {const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("platforms-list").children[0].emit("click");return ui;}
async function openApplication(ui) {
 await ui.el(ui.el("gitlab-repair-application").hidden ? "gitlab-edit-application" : "gitlab-repair-application").emit("click");
 if (!ui.el("connection-auth-confirmation").hidden) await ui.el("connection-auth-confirm").emit("click");
}
function referenceNames(ui,id="connection-reference-list") {return ui.el(id).children.flatMap(group=>group.children.at(-1).children.map(row=>row.children[0]?.textContent));}
async function authorize(ui, action="login") {
 await ui.el(`gitlab-${action}`).emit("click");
 if (!ui.el("connection-auth-confirmation").hidden) await ui.el("connection-auth-confirm").emit("click");
}
test("GitLab reauthorization keeps saved scopes and displays incompatible callback details",async()=>{
 const api=fixture();api.prepared={...api.prepared,status:"application_incompatible",application_id:undefined,scopes:["api"],applications:[{client_id:"saved-client",scopes:["api"],callback_compatible:false,missing_scopes:[]}]};
 const ui=await edit(api);
 assert.equal(api.calls.find(c=>c.path.endsWith("/gitlab/prepare")).body.scopes,undefined);
 assert.equal(ui.el("gitlab-scope-write-repository").checked,false);
 assert.equal(ui.el("gitlab-reuse").hidden,false);
 assert.equal(ui.el("gitlab-repair-application").hidden,false);
 assert.equal(ui.el("gitlab-repair-application").disabled,false);
 assert.equal(ui.el("gitlab-edit-application").hidden,true);
 assert.equal(ui.el("gitlab-login").hidden,true);
 assert.equal(ui.el("gitlab-reuse-summary").textContent,"回调地址不匹配。请修改应用配置后重新授权。");
 assert.equal(ui.el("gitlab-compatibility-details").hidden,false);
 assert.equal(ui.el("gitlab-compatibility-details").open,false);
 assert.doesNotMatch(ui.el("gitlab-application-issues").children[0].textContent,/saved-client/);
 assert.match(ui.el("gitlab-application-issues").children[0].textContent,/回调地址不匹配/);
 assert.ok(ui.el("gitlab-application-issues").children[0].textContent.includes(api.prepared.redirect_uri));
 await ui.el("gitlab-login").emit("click");
 assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/prepare")).at(-1).body.scopes,undefined);
 assert.equal(api.calls.some(c=>c.path.endsWith("/gitlab/login")),false);
 ui.el("gitlab-compatibility-details").open=true;
 await ui.el("connection-back").emit("click");await ui.el("platforms-list").children[0].emit("click");
 assert.equal(ui.el("gitlab-compatibility-details").open,false);
 api.editPrepared={...api.prepared,status:"configuration_required",client_id:"saved-client"};
 await ui.el("gitlab-repair-application").emit("click");
 assert.equal(ui.el("connection-auth-confirmation").hidden,false);
 assert.equal(api.calls.some(c=>c.path.endsWith("/gitlab/initialize")),false);
 await ui.el("connection-auth-confirm").emit("click");
 assert.equal(ui.el("gitlab-reuse").hidden,true);
 assert.equal(ui.el("gitlab-setup").hidden,false);
});

test("GitLab compatibility summary names only actual issues and keeps multiple application IDs in details",async()=>{
 const api=fixture();api.prepared={...api.prepared,status:"application_incompatible",applications:[{client_id:"first-client",callback_compatible:true,missing_scopes:["write_repository"]},{client_id:"second-client",callback_compatible:true,missing_scopes:["write_repository"]}]};
 const ui=await edit(api);
 assert.equal(ui.el("gitlab-reuse-summary").textContent,"缺少权限 write_repository。请修改应用配置后重新授权。");
 assert.match(ui.el("gitlab-application-issues").children[0].textContent,/first-client/);
 assert.match(ui.el("gitlab-application-issues").children[1].textContent,/second-client/);
});

test("GitLab reauthorization uses existing application scopes without requesting repository access",async()=>{
 const api=fixture();api.prepared={...api.prepared,scopes:["api"]};
 const ui=await edit(api);await authorize(ui);
 assert.ok(api.calls.filter(c=>c.path.endsWith("/gitlab/prepare")).every(c=>c.body.scopes===undefined));
 assert.deepEqual(api.calls.find(c=>c.path.endsWith("/gitlab/login")).body.scopes,["api"]);
});

test("GitLab application reuse exposes account switching and explains duplicate account rejection before retry",async()=>{
 const api=fixture();api.prepared={...api.prepared,scopes:["api"],affected:[]};
 const original=structuredClone(api.items[0]);
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");
 ui.el("connection-name").value="Other account";ui.el("connection-url").value="https://gitlab.invalid";
 await ui.el("connection-url").emit("change");
 assert.equal(ui.el("gitlab-account-choice").hidden,false);
 assert.match(ui.el("gitlab-account-choice-hint").textContent,/@tester.*另一个账号/);
 assert.equal(ui.el("gitlab-switch-account").href,"https://gitlab.invalid/");
 assert.match(ui.el("gitlab-switch-account").textContent,/切换账号/);
 assert.equal(ui.el("gitlab-login").textContent,"授权新账号");
 api.status={status:"failed",error_code:"account_already_connected",account_username:"tester",cleanup_status:"manual_required"};
 await ui.el("gitlab-login").emit("click");
 const login=api.calls.find(c=>c.path.endsWith("/gitlab/login")).body;
 assert.equal(login.id,undefined);assert.equal(login.application_id,"app");assert.equal(login.name,"Other account");
 assert.equal(ui.el("platforms-feedback").textContent,"@tester 已连接其他平台，请切换账号后重试。");
 assert.doesNotMatch(ui.el("platforms-feedback").textContent,/候选授权|返回列表核对/);
 assert.match(ui.el("platforms-feedback-detail").textContent,/已有平台的授权保持不变/);
 assert.equal(ui.el("platforms-feedback-details").hidden,false);
 assert.equal(ui.el("platforms-feedback-details").open,false);
 assert.equal(ui.el("gitlab-account-choice").hidden,false);
 assert.equal(ui.el("gitlab-login").disabled,false);
 assert.deepEqual(api.items[0],original);
 assert.equal(api.calls.some(c=>/\/gitlab\/initialize$|\/platforms\/save$/.test(c.path)),false);
 api.status={status:"succeeded",platforms:[{...original,id:"second-account",name:"Other account",validation:{status:"valid",username:"other"}}]};
 await ui.el("gitlab-login").emit("click");
 assert.match(ui.el("gitlab-account").textContent,/@other/);
 assert.equal(ui.el("gitlab-account-choice").hidden,true);
 assert.deepEqual(api.items[0],original);
});

test("GitLab application updates repair an incompatible saved callback and preserve explicit scope changes",async()=>{
 const api=fixture();api.prepared={...api.prepared,status:"application_incompatible",application_id:undefined,scopes:["api"],applications:[{client_id:"saved-client",callback_compatible:false,missing_scopes:[]}]};
 api.editPrepared={...api.prepared,status:"configuration_required",client_id:"saved-client"};
 const ui=await edit(api);await openApplication(ui);
 assert.equal(ui.el("gitlab-scope-write-repository").checked,false);
 await ui.el("gitlab-import").emit("click");
 assert.match(ui.el("gitlab-setup-feedback").textContent,/请填写 Secret/);
 assert.equal(api.calls.some(c=>c.path.endsWith("/gitlab/initialize")),false);
 ui.el("gitlab-scope-write-repository").checked=true;await ui.el("gitlab-scope-write-repository").emit("change");
 ui.el("gitlab-client-secret").value="updated-secret";
 await ui.el("gitlab-import").emit("click");
 const prepared=api.calls.filter(c=>c.path.endsWith("/gitlab/prepare")).at(-1).body;
 assert.equal(prepared.edit_application,true);
 const imported=api.calls.find(c=>c.path.endsWith("/gitlab/initialize")).body;
 assert.equal(imported.id,"original");assert.equal(imported.client.redirect_uri,api.prepared.redirect_uri);
 assert.deepEqual(imported.client.scopes,["api","write_repository"]);
 assert.equal(api.calls.find(c=>c.path.endsWith("/gitlab/login")).body.client_import_id,"imported");
 assert.equal(ui.el("gitlab-client-secret").value,"");
});

test("GitLab OAuth login starts once without a second confirmation and automatically saves the result",async()=>{
 const api=fixture(),ui=await edit(api);api.confirm=()=>{throw Error("native confirmation must not open")};
 assert.match(ui.el("gitlab-account").textContent,/已连接/);
 assert.equal(ui.el("connection-references").hidden,false);assert.deepEqual(referenceNames(ui),["Review"]);
 assert.equal(ui.el("connection-references").dataset.impact,"false");
 assert.equal(ui.el("gitlab-setup").hidden,true);
 let opened, popups=0; const popup={closed:false,opener:{},location:{replace:url=>{opened=url}},close(){this.closed=true}};ui.window.open=()=>{popups++;return popup};
 await ui.el("gitlab-login").emit("click");
 assert.equal(popups,1);assert.equal(api.calls.some(c=>c.path.endsWith("/gitlab/login")),true);
 assert.equal(ui.el("connection-auth-confirmation").hidden,true);assert.deepEqual(referenceNames(ui),["Review"]);
 assert.equal(ui.el("connection-references").hidden,false);assert.equal(ui.el("connection-references").dataset.impact,"true");
 assert.equal(ui.el("connection-name").disabled,true);assert.equal(ui.el("connection-save").disabled,true);
 await ui.el("gitlab-login").emit("click");assert.equal(popups,1);assert.equal(ui.el("connection-auth-confirmation").hidden,true);
 assert.equal(ui.el("connection-references").hidden,false);
 assert.equal(ui.el("connection-references").dataset.impact,"true");
 assert.equal(ui.el("gitlab-login-link").hidden,false);assert.match(opened,/^https:\/\/gitlab.invalid\/oauth\/authorize/);assert.equal(popup.opener,null);
 await ui.el("gitlab-login").emit("click");assert.equal(popups,1);
 const request=api.calls.find(c=>c.path.endsWith("/gitlab/login")).body;
 assert.deepEqual(request.affected,api.prepared.affected);assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/login")).length,1);
 assert.equal(api.calls.filter(c=>c.path.endsWith("/platforms/save")).length,0);
 api.status={status:"succeeded",platforms:[{...api.items[0],version:3,credential_kind:"oauth",authorization_status:"active"}]};await ui.clock.advance(1500);
 assert.match(ui.el("gitlab-account").textContent,/已连接.*tester/);assert.match(ui.el("platforms-feedback").textContent,/授权已更新/);
 assert.equal(ui.el("gitlab-setup").hidden,true);
 assert.equal(ui.el("connection-references").dataset.impact,"false");
 assert.equal(ui.el("gitlab-login-link").href,"");assert.doesNotMatch(JSON.stringify([...ui.saved]),/fixture-state/);
});
test("GitLab closing cancels the browser flow and ignores a late status",async()=>{
 const api=fixture(),ui=await edit(api);await authorize(ui);let finish;
 api.status=()=>new Promise(r=>{finish=r});const polling=ui.clock.advance(1500);await settle();await ui.el("close-settings").emit("click");
 finish({status:"succeeded",platforms:[{...api.items[0],validation:{status:"valid",username:"late-user"}}]});await polling;
 assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/cancel")).length,1);assert.equal(ui.el("gitlab-login-link").href,"");assert.doesNotMatch(ui.el("gitlab-account").textContent,/late-user/);
});
test("GitLab shared logout displays local disconnect when remote revocation is unknown",async()=>{
 const api=fixture("oauth"),ui=await edit(api);api.status={status:"disconnected",platforms:[{...api.items[0],authorization_status:"logged_out",revocation_status:"unconfirmed",credential_configured:false,validation:{status:"unverified"}}]};
 await ui.el("gitlab-logout").emit("click");assert.equal(ui.el("connection-auth-confirm").textContent,"确认退出");
 await ui.el("connection-auth-confirm").emit("click");assert.match(ui.el("gitlab-account").textContent,/本地已断开.*未确认/);assert.match(ui.el("platforms-feedback").textContent,/本地已断开/);
 assert.equal(ui.el("gitlab-setup").hidden,true);assert.equal(ui.el("gitlab-logout").hidden,true);
});
test("GitLab first connection imports and authorizes once, then saves without another button",async()=>{
 const api=fixture();api.items=[];api.prepared={...api.prepared,status:"configuration_required",application_id:undefined,affected:[]};
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");
 ui.el("connection-name").value="New GitLab";ui.el("connection-url").value="https://gitlab.invalid";
 ui.el("gitlab-client-id").value="fixture-client";ui.el("gitlab-client-secret").value="fixture-secret";
 await ui.el("gitlab-import").emit("click");
 assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/initialize")).length,1);
 const logins=api.calls.filter(c=>c.path.endsWith("/gitlab/login"));
 assert.equal(logins.length,1);assert.equal(logins[0].body.client_import_id,"imported");assert.equal(logins[0].body.name,"New GitLab");
 assert.equal(ui.el("connection-save").hidden,true);assert.equal(ui.el("connection-actions").hidden,true);
 assert.equal(ui.el("gitlab-client-secret").value,"");assert.match(ui.el("gitlab-account").textContent,/等待 GitLab 授权/);
 api.status={status:"succeeded",platforms:[{...fixture().items[0],name:"New GitLab",references:[],version:3}]};
 await ui.clock.advance(1500);
 assert.match(ui.el("platforms-feedback").textContent,/已连接 GitLab/);
 assert.equal(api.calls.some(c=>c.path.endsWith("/platforms/save")),false);
 assert.equal(ui.el("connection-save").hidden,true);
});
test("GitLab first connection requires a name and can reuse saved credentials without another import",async()=>{
 const api=fixture();api.items=[];api.prepared={...api.prepared,client_id:"saved-client",scopes:["api"],affected:[]};
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");
 ui.el("connection-url").value="https://gitlab.invalid";
 await ui.el("gitlab-import").emit("click");
 assert.equal(ui.el("connection-name").attributes["aria-invalid"],"true");assert.equal(api.calls.some(c=>c.path.endsWith("/gitlab/login")),false);
 ui.el("connection-name").value="New GitLab";
 await ui.el("connection-form").emit("submit");
 assert.equal(ui.el("gitlab-scope-write-repository").checked,false);
 assert.equal(api.calls.some(c=>c.path.endsWith("/gitlab/initialize")),false);
 assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/login")).length,1);
 assert.equal(api.calls.find(c=>c.path.endsWith("/gitlab/login")).body.application_id,"app");
});

test("GitLab changing a saved application's requested scopes requires credentials before authorization",async()=>{
 const api=fixture();api.items=[];api.prepared={...api.prepared,client_id:"saved-client",scopes:["api"],affected:[]};
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");
 ui.el("connection-name").value="New GitLab";ui.el("connection-url").value="https://gitlab.invalid";
 await ui.el("connection-url").emit("change");
 ui.el("gitlab-scope-write-repository").checked=true;await ui.el("gitlab-scope-write-repository").emit("change");
 await ui.el("gitlab-import").emit("click");
 assert.match(ui.el("gitlab-setup-feedback").textContent,/请填写 Secret/);
 assert.equal(api.calls.some(c=>c.path.endsWith("/gitlab/login")),false);
 assert.equal(ui.el("gitlab-scope-write-repository").checked,true);
});

test("GitLab status failure preserves a cancel path and never claims success or retries login",async()=>{
 const api=fixture(),ui=await edit(api);api.status=()=>{throw Error("read failed")};await authorize(ui);
 assert.match(ui.el("platforms-feedback").textContent,/状态读取失败/);assert.equal(ui.el("gitlab-cancel").hidden,false);
 await ui.el("gitlab-cancel").emit("click");assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/cancel")).length,1);assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/login")).length,1);
});
test("GitLab logout confirmation can be cancelled or left without starting a remote operation",async()=>{
 const api=fixture(),ui=await edit(api);let popups=0;ui.window.open=()=>{popups++;return null};
 await ui.el("gitlab-logout").emit("click");await ui.el("connection-auth-dismiss").emit("click");
 assert.equal(ui.el("connection-auth-confirmation").hidden,true);assert.equal(ui.el("connection-name").disabled,false);assert.equal(ui.el("connection-save").disabled,true);
 assert.match(ui.el("platforms-feedback").textContent,/已取消/);
 assert.equal(ui.el("connection-references").hidden,false);
 assert.equal(ui.el("connection-references").dataset.impact,"false");assert.deepEqual(referenceNames(ui),["Review"]);
 await ui.el("gitlab-logout").emit("click");await ui.el("connection-back").emit("click");
 await ui.el("connection-auth-confirm").emit("click");
 await ui.el("platforms-list").children[0].emit("click");assert.equal(ui.el("connection-auth-confirmation").hidden,true);
 await ui.el("gitlab-logout").emit("click");await ui.el("close-settings").emit("click");await ui.el("connection-auth-confirm").emit("click");
 assert.equal(popups,0);assert.equal(api.calls.some(c=>/\/gitlab\/(login|logout|cancel)$/.test(c.path)),false);
});
test("GitLab shared authorization groups every affected room and preserves pending bindings",async()=>{
 const api=fixture();api.items[0].references.push({room_id:"P",room_instance:"pending",name:"Next Room",pending:true});
 const other={...api.items[0],id:"other",name:"Shared GitLab",references:Array.from({length:25},(_,i)=>({room_id:`R${i}`,room_instance:"other",name:`Room ${i}`}))};
 api.items.push(other);
 api.prepared.affected=api.items.map(item=>({id:item.id,version:item.version,referenced_rooms:item.references.map(({room_id,room_instance})=>({room_id,room_instance}))}));
 const ui=await edit(api);assert.equal(referenceNames(ui).length,27);
 await ui.el("gitlab-login").emit("click");assert.equal(ui.el("connection-references-title").textContent,"引用房间 · 27");
 assert.equal(ui.el("connection-reference-list").children.length,2);assert.equal(ui.el("connection-reference-list").children[1].children[0].textContent,"Shared GitLab");
 assert.equal(referenceNames(ui).length,27);assert.equal(referenceNames(ui).at(-1),"Room 24");
 assert.equal(ui.el("connection-reference-list").children[0].children.at(-1).children[1].children[1].textContent,"待完成绑定");
 await ui.el("gitlab-cancel").emit("click");assert.equal(referenceNames(ui).length,27);
});
test("GitLab connection without shared impact logs in directly and keeps a link if the popup is blocked",async()=>{
 for (const open of [()=>null,()=>{throw Error("popup denied")}]) {
  const api=fixture();api.items[0].references=[];api.prepared.affected=[{id:"original",version:2}];const ui=await edit(api);ui.window.open=open;
  await ui.el("gitlab-login").emit("click");assert.equal(ui.el("connection-auth-confirmation").hidden,true);
  assert.equal(ui.el("gitlab-login-link").hidden,false);assert.match(ui.el("platforms-feedback").textContent,/等待 GitLab 授权/);
  assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/login")).length,1);
 }
});
test("GitLab application scopes default to api and write_repository and allow opting out of repository access",async()=>{
 for (const repositoryAccess of [true,false]) {
  const api=fixture();api.items=[];api.prepared.status="configuration_required";const ui=await launch(api);
  await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");ui.el("connection-name").value="New GitLab";
  assert.equal(ui.el("gitlab-scope-api").checked,true);assert.equal(ui.el("gitlab-scope-api").disabled,true);
  assert.equal(ui.el("gitlab-scope-write-repository").checked,true);assert.equal(ui.el("gitlab-scope-write-repository").disabled,false);
  ui.el("connection-name").value="New GitLab";ui.el("connection-url").value="https://gitlab.invalid";
  ui.el("gitlab-scope-write-repository").checked=repositoryAccess;await ui.el("gitlab-scope-write-repository").emit("change");
  ui.el("gitlab-client-id").value="client";ui.el("gitlab-client-secret").value="secret";
  await ui.el("gitlab-import").emit("click");
  const requested=repositoryAccess ? ["api","write_repository"] : ["api"];
  const prepared=api.calls.find(c=>c.path.endsWith("/gitlab/prepare")).body;
  const imported=api.calls.find(c=>c.path.endsWith("/gitlab/initialize")).body;
  assert.deepEqual(prepared.scopes,requested);assert.deepEqual(imported.scopes,requested);assert.deepEqual(imported.client.scopes,requested);
  assert.equal("repository_access" in imported,false);
 }
});

test("GitLab late discovery preserves a changed scope selection and saved api-only settings restore without expansion",async()=>{
 const api=fixture();api.items=[];api.prepared={...api.prepared,status:"configuration_required",scopes:["api","write_repository"]};
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");ui.el("connection-name").value="New GitLab";
 ui.el("connection-url").value="https://gitlab.invalid";
 const request=api.request;let finish;
 api.request=async(path,body)=>path.endsWith("/gitlab/prepare") ? new Promise(resolve=>{finish=resolve}) : request(path,body);
 const reading=ui.el("connection-url").emit("change");await settle();
 ui.el("gitlab-scope-write-repository").checked=false;await ui.el("gitlab-scope-write-repository").emit("change");
 finish(api.prepared);await reading;
 assert.equal(ui.el("gitlab-scope-write-repository").checked,false);
 api.request=request;api.items=fixture().items;api.editPrepared={...api.prepared,status:"configuration_required",scopes:["api"],client_id:"api-only-client"};
 await ui.el("connection-back").emit("click");await ui.el("platforms-list").children[0].emit("click");await openApplication(ui);
 assert.equal(ui.el("gitlab-scope-api").checked,true);assert.equal(ui.el("gitlab-scope-api").disabled,true);
 assert.equal(ui.el("gitlab-scope-write-repository").checked,false);
 assert.equal(api.calls.some(c=>c.path.endsWith("/gitlab/initialize")),false);
});

test("GitLab failed first login can correct application credentials and use the replacement after reopening",async()=>{
 const api=fixture();api.items=[];
 api.prepared={...api.prepared,status:"owner_verification_required",application_id:undefined,client_import_id:"wrong-import",affected:[]};
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");ui.el("connection-name").value="New GitLab";
 ui.el("connection-name").value="New GitLab";ui.el("connection-url").value="https://gitlab.invalid";
 api.status={status:"failed"};
 await ui.el("connection-form").emit("submit");
 assert.equal(ui.el("gitlab-advanced").open,true);assert.equal(ui.el("gitlab-advanced-title").hidden,true);
 assert.equal(ui.el("gitlab-setup").hidden,false);
 ui.el("gitlab-client-id").value="correct-client";ui.el("gitlab-client-secret").value="correct-secret";
 api.initialized={...api.prepared,client_import_id:"correct-import",client_id:"correct-client",scopes:["api","write_repository"]};
 await ui.el("gitlab-import").emit("click");
 const replacement=api.calls.find(c=>c.path.endsWith("/gitlab/initialize")).body;
 assert.equal(replacement.client_import_id,"wrong-import");assert.equal(replacement.client.client_id,"correct-client");
 assert.equal(replacement.client.client_secret,"correct-secret");assert.equal(ui.el("gitlab-client-secret").value,"");
 assert.doesNotMatch(JSON.stringify([...ui.saved]),/correct-secret/);
 api.prepared=api.initialized;
 await ui.el("connection-back").emit("click");await ui.el("connection-new").emit("click");ui.el("connection-name").value="New GitLab";
 ui.el("connection-name").value="New GitLab";ui.el("connection-url").value="https://gitlab.invalid";
 api.status={status:"waiting"};await ui.el("connection-form").emit("submit");
 assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/login")).at(-1).body.client_import_id,"correct-import");
 assert.equal(ui.el("gitlab-advanced-title").attributes["aria-disabled"],"true");
});

test("GitLab configuration save conflict keeps correction form open and clears the Secret",async()=>{
 const api=fixture();api.items=[];api.prepared={...api.prepared,status:"owner_verification_required",application_id:undefined,client_import_id:"pending",affected:[]};
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");ui.el("connection-name").value="New GitLab";
 ui.el("connection-url").value="https://gitlab.invalid";await ui.el("connection-url").emit("change");
 await ui.el("gitlab-import").emit("click");assert.equal(ui.el("gitlab-setup").hidden,false);
 assert.match(ui.el("gitlab-setup-feedback").textContent,/请填写/);
 ui.el("gitlab-client-id").value="client";ui.el("gitlab-client-secret").value="secret";
 const request=api.request;api.request=async(path,body)=>{if(path.endsWith("/gitlab/initialize"))throw {code:"conflict"};return request(path,body);};
 await ui.el("gitlab-import").emit("click");
 assert.equal(ui.el("gitlab-setup").hidden,false);assert.equal(ui.el("gitlab-client-secret").value,"");
 assert.equal(ui.el("gitlab-advanced-title").attributes["aria-disabled"],"false");assert.match(ui.el("gitlab-setup-feedback").textContent,/已变化.*返回列表重新打开连接/);
 assert.equal(ui.el("gitlab-setup-feedback").attributes.role,"alert");assert.equal(ui.el("platforms-feedback").textContent,"");
});

test("GitLab first and saved unverified applications show the same form directly",async()=>{
 const api=fixture();api.items=[];api.prepared={...api.prepared,status:"configuration_required",application_id:undefined,affected:[]};
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");ui.el("connection-name").value="New GitLab";
 assert.equal(ui.el("gitlab-advanced-title").hidden,true);assert.equal(ui.el("gitlab-advanced").open,true);
 assert.equal(ui.el("gitlab-setup").hidden,false);
 ui.el("connection-url").value="https://gitlab.invalid";await ui.el("connection-url").emit("change");
 assert.equal(ui.el("gitlab-setup").hidden,false);
 await ui.el("connection-back").emit("click");
 api.items=[{...fixture().items[0],credential_configured:false,authorization_status:undefined,validation:{status:"unverified"}}];
 api.prepared={...api.prepared,status:"owner_verification_required",client_import_id:"pending"};
 await ui.el("open-settings").emit("click");await ui.el("platforms-list").children[0].emit("click");
 assert.equal(ui.el("gitlab-advanced").open,true);
 assert.equal(ui.el("gitlab-setup").hidden,false);
 assert.equal(ui.el("gitlab-advanced-title").hidden,true);
});

for (const status of ["succeeded","failed"]) test(`GitLab verified application replacement stays in advanced settings and ${status} preserves the expected connection`,async()=>{
 const api=fixture();api.editPrepared={...api.prepared,status:"configuration_required",application_id:undefined,scopes:["api","write_repository"]};
 const ui=await edit(api);
 assert.equal(ui.el("gitlab-edit-application").hidden,false);assert.equal(ui.el("gitlab-advanced").open,false);
 assert.equal(ui.el("gitlab-setup").hidden,true);
 await openApplication(ui);
 assert.equal(ui.el("gitlab-setup").hidden,false);assert.equal(ui.el("gitlab-client-secret").value,"");
 assert.equal(ui.el("gitlab-scope-write-repository").checked,true);assert.equal(ui.el("gitlab-import").textContent,"更新并授权");
 ui.el("gitlab-client-id").value="replacement-client";ui.el("gitlab-client-secret").value="replacement-secret";
 api.initialized={...api.editPrepared,status:"owner_verification_required",client_import_id:"replacement"};
 await ui.el("gitlab-import").emit("click");
 assert.equal(ui.el("gitlab-client-secret").value,"");assert.match(ui.el("gitlab-account").textContent,/等待 GitLab 授权/);
 assert.equal(ui.el("connection-auth-confirmation").hidden,true);
 assert.equal(api.calls.some(c=>c.path.endsWith("/gitlab/login")),true);
 assert.equal(ui.el("connection-save").disabled,true);
 const login=api.calls.find(c=>c.path.endsWith("/gitlab/login")).body;
 assert.equal(login.client_import_id,"replacement");assert.equal(login.application_id,undefined);assert.deepEqual(login.affected,api.prepared.affected);
 api.status={status,...(status === "succeeded" ? {platforms:[{...api.items[0],version:3}]} : {})};
 await ui.clock.advance(1500);
 assert.match(ui.el("gitlab-account").textContent,/已连接.*tester/);
 if(status === "succeeded") {assert.equal(ui.el("gitlab-advanced").open,false);assert.equal(ui.el("gitlab-setup").hidden,true);assert.match(ui.el("platforms-feedback").textContent,/应用配置已更新/);assert.equal(ui.el("connection-save").disabled,true);assert.equal(api.calls.some(c=>c.path.endsWith("/platforms/save")),false);}
 else {assert.match(ui.el("gitlab-setup-feedback").textContent,/授权未完成/);assert.equal(ui.el("platforms-feedback").textContent,"");}
 assert.doesNotMatch(JSON.stringify([...ui.saved]),/replacement-secret/);
});

test("GitLab existing platforms only enable saving for a changed nonempty name",async()=>{
 const api=fixture(),ui=await edit(api);
 assert.equal(ui.el("connection-save").textContent,"保存名称");assert.equal(ui.el("connection-save").disabled,true);
 await ui.el("connection-form").emit("submit");
 assert.equal(api.calls.some(c=>c.path.endsWith("/platforms/save")),false);
 ui.el("connection-name").value="Renamed GitLab";await ui.el("connection-name").emit("input");
 assert.equal(ui.el("connection-save").disabled,false);
 ui.el("connection-name").value=` ${api.items[0].name} `;await ui.el("connection-name").emit("input");
 assert.equal(ui.el("connection-save").disabled,true);
 ui.el("connection-name").value=" ";await ui.el("connection-name").emit("input");
 assert.equal(ui.el("connection-save").disabled,true);
 ui.el("connection-name").value="Renamed GitLab";await ui.el("connection-name").emit("input");
 await ui.el("connection-form").emit("submit");
 const saves=api.calls.filter(c=>c.path.endsWith("/platforms/save"));
 assert.equal(saves.length,1);assert.equal(saves[0].body.name,"Renamed GitLab");
 assert.match(ui.el("platforms-feedback").textContent,/名称已保存/);
 assert.equal(api.calls.some(c=>c.path.endsWith("/gitlab/login")),false);
 await ui.el("platforms-list").children[0].emit("click");
 assert.equal(ui.el("connection-save").textContent,"保存名称");assert.equal(ui.el("connection-save").disabled,true);
});

test("GitLab application update shows shared impact before the action and opens one popup before importing",async()=>{
 const api=fixture();
 const other={...api.items[0],id:"other",name:"Shared GitLab",references:[{room_id:"Other",room_instance:"other-instance",name:"Other room"}]};
 api.items.push(other);
 api.editPrepared={...api.prepared,status:"configuration_required",application_id:undefined,scopes:["api"],client_id:"saved-client",affected:api.items.map(item=>({id:item.id,version:item.version,referenced_rooms:item.references.map(({room_id,room_instance})=>({room_id,room_instance}))}))};
 const ui=await edit(api);await openApplication(ui);
 assert.equal(ui.el("connection-references").hidden,false);
 assert.match(ui.el("connection-references-title").textContent,/引用房间 · 2/);
 assert.deepEqual(referenceNames(ui),["Review","Other room"]);
 assert.equal(ui.el("connection-reference-list").children[1].children[0].textContent,"Shared GitLab");
 assert.equal(api.calls.some(c=>c.path.endsWith("/gitlab/initialize")),false);
 const popup={closed:false,opener:{},location:{replace(url){this.url=url}},close(){this.closed=true}};
 let popups=0;ui.window.open=()=>{popups++;return popup};
 let finish;const request=api.request;
 api.request=async(path,body)=>path.endsWith("/gitlab/initialize") ? (api.calls.push({path,body:structuredClone(body)}),new Promise(resolve=>{finish=resolve})) : request(path,body);
 ui.el("gitlab-client-secret").value="fixture-secret";
 const updating=ui.el("gitlab-import").emit("click");
 assert.equal(popups,1);assert.equal(popup.opener,null);await settle();
 await ui.el("gitlab-import").emit("click");
 assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/initialize")).length,1);
 finish({...api.editPrepared,status:"owner_verification_required",client_import_id:"replacement"});await updating;
 assert.equal(popups,1);assert.equal(ui.el("connection-auth-confirmation").hidden,true);
 assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/login")).length,1);
 assert.deepEqual(api.calls.find(c=>c.path.endsWith("/gitlab/login")).body.affected,api.editPrepared.affected);
 assert.match(popup.location.url,/gitlab.invalid\/oauth\/authorize/);
 assert.match(ui.el("gitlab-account").textContent,/等待 GitLab 授权/);
 assert.equal(ui.el("connection-save").disabled,true);
});

test("GitLab application preparation failures close the reserved popup and a name draft prevents importing",async()=>{
 const api=fixture();api.editPrepared={...api.prepared,status:"configuration_required",application_id:undefined,scopes:["api"],client_id:"saved-client"};
 const ui=await edit(api);await openApplication(ui);
 let opened=0,closed=0;ui.window.open=()=>{opened++;return {closed:false,close(){this.closed=true;closed++}}};
 ui.el("gitlab-client-secret").value="fixture-secret";
 ui.el("connection-name").value="Unsaved name";await ui.el("connection-name").emit("input");
 await ui.el("gitlab-import").emit("click");
 assert.equal(opened,0);assert.equal(api.calls.some(c=>c.path.endsWith("/gitlab/initialize")),false);
 assert.match(ui.el("platforms-feedback").textContent,/先保存名称/);
 ui.el("connection-name").value=api.items[0].name;await ui.el("connection-name").emit("input");
 const request=api.request;api.request=async(path,body)=>{if(path.endsWith("/gitlab/initialize"))throw Error("unconfirmed import");return request(path,body)};
 await ui.el("gitlab-import").emit("click");
 assert.equal(opened,1);assert.equal(closed,1);
 assert.equal(api.calls.some(c=>c.path.endsWith("/gitlab/login")),false);
 assert.equal(ui.el("connection-save").disabled,true);
 assert.match(ui.el("gitlab-setup-feedback").textContent,/保存结果未确认/);
});

test("GitLab application configuration has one authorization action and no competing account actions",async()=>{
 const api=fixture(),ui=await edit(api);
 assert.equal(ui.el("connection-save").hidden,true);
 await openApplication(ui);
 assert.equal(ui.el("gitlab-import").textContent,"更新并授权");
 assert.equal(ui.el("gitlab-login").hidden,true);assert.equal(ui.el("gitlab-logout").hidden,true);
 assert.equal(ui.el("gitlab-initial-hint").hidden,true);
 await ui.el("gitlab-edit-cancel").emit("click");
 assert.equal(ui.el("gitlab-login").hidden,false);assert.equal(ui.el("gitlab-logout").hidden,false);
 await ui.el("connection-back").emit("click");await ui.el("connection-new").emit("click");
 assert.equal(ui.el("gitlab-import").textContent,"连接 GitLab");assert.equal(ui.el("gitlab-initial-hint").hidden,false);
 assert.equal(ui.el("connection-save").hidden,true);
});

test("GitLab reopening application settings restores saved public fields and leaves Secret empty",async()=>{
 const api=fixture();api.editPrepared={...api.prepared,status:"configuration_required",application_id:undefined,client_id:"saved-client",scopes:["api","write_repository"]};
 const ui=await edit(api);await openApplication(ui);
 assert.equal(ui.el("gitlab-client-id").value,"saved-client");assert.equal(ui.el("gitlab-client-secret").value,"");
 assert.equal(ui.el("gitlab-redirect-uri").value,api.prepared.redirect_uri);assert.equal(ui.el("gitlab-scope-write-repository").checked,true);
 ui.el("gitlab-client-id").value="draft-client";ui.el("gitlab-client-secret").value="draft-secret";
 assert.equal(ui.el("gitlab-client-id").value,"draft-client");assert.equal(ui.el("gitlab-client-secret").value,"draft-secret");
 await ui.el("gitlab-edit-cancel").emit("click");assert.equal(ui.el("gitlab-client-secret").value,"");await openApplication(ui);
 assert.equal(ui.el("gitlab-client-id").value,"saved-client");
 await ui.el("connection-back").emit("click");await ui.el("platforms-list").children[0].emit("click");await openApplication(ui);
 assert.equal(ui.el("gitlab-client-id").value,"saved-client");assert.equal(ui.el("gitlab-client-secret").value,"");
 assert.equal(ui.el("gitlab-scope-write-repository").checked,true);assert.equal(ui.el("gitlab-setup-feedback").hidden,true);
});

test("GitLab invalid application feedback stays beside verification and corrections reuse the pending candidate",async()=>{
 const api=fixture();api.editPrepared={...api.prepared,status:"configuration_required",application_id:undefined,client_id:"saved-client"};
 const ui=await edit(api);await openApplication(ui);
 ui.el("gitlab-client-secret").value="wrong-secret";
 api.initialized={...api.editPrepared,status:"owner_verification_required",client_import_id:"candidate"};
 api.status={status:"failed",error_code:"invalid_client"};
 await ui.el("gitlab-import").emit("click");
 assert.match(ui.el("gitlab-setup-feedback").textContent,/应用凭据无效.*Application ID 和 Secret/);
 assert.equal(ui.el("gitlab-setup-feedback").hidden,false);assert.equal(ui.el("gitlab-setup-feedback").attributes.role,"alert");
 assert.equal(ui.el("platforms-feedback").textContent,"");assert.equal(ui.el("gitlab-advanced").open,true);
 assert.equal(ui.el("gitlab-client-id").value,"saved-client");assert.equal(ui.el("gitlab-client-secret").value,"");
 ui.el("gitlab-client-secret").value="correct-secret";await ui.el("gitlab-import").emit("click");
 const correction=api.calls.filter(c=>c.path.endsWith("/gitlab/initialize")).at(-1).body;
 assert.equal(correction.client_import_id,"candidate");assert.equal(correction.client.client_secret,"correct-secret");
});

test("GitLab scope failures explain missing or requested permissions beside application settings",async()=>{
 for (const knownMissing of [true,false]) {
  const api=fixture();api.editPrepared={...api.prepared,status:"configuration_required",application_id:undefined,client_id:"saved-client",scopes:["api","write_repository"]};
  const ui=await edit(api);await openApplication(ui);
  ui.el("gitlab-client-secret").value="fixture-secret";
  api.initialized={...api.editPrepared,status:"owner_verification_required",client_import_id:"candidate"};
  api.status={status:"failed",error_code:"invalid_scope",...(knownMissing ? {requested_scopes:["api","write_repository"],missing_scopes:["write_repository"]} : {})};
  await ui.el("gitlab-import").emit("click");
  const feedback=ui.el("gitlab-setup-feedback");
  assert.match(feedback.textContent,knownMissing ? /缺少权限：write_repository/ : /拒绝了申请的权限：api \+ write_repository/);
  assert.match(feedback.textContent,/GitLab 应用设置.*Application ID.*勾选 api \+ write_repository.*重新登录/);
  assert.match(feedback.textContent,/取消 write_repository 后重新授权/);
  assert.equal(feedback.hidden,false);assert.equal(feedback.attributes.role,"alert");
  assert.equal(ui.el("gitlab-advanced").open,true);assert.equal(ui.el("gitlab-setup").hidden,false);
  assert.equal(ui.el("gitlab-client-secret").value,"");
  assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/login")).length,1);
 }
});

test("GitLab ordinary login scope failures name api without suggesting unnecessary repository access",async()=>{
 const api=fixture(),ui=await edit(api);api.status={status:"failed",error_code:"invalid_scope",requested_scopes:["api"]};
 await authorize(ui);
 assert.match(ui.el("platforms-feedback").textContent,/拒绝了申请的权限：api.*GitLab 应用设置.*Application ID.*重新登录/);
 assert.doesNotMatch(ui.el("platforms-feedback").textContent,/write_repository|本次申请权限/);
});

test("GitLab automatic application discovery ignores superseded reads and failures after leaving the form",async()=>{
 const api=fixture();api.items=[];api.prepared={...api.prepared,status:"configuration_required",application_id:undefined,affected:[]};
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");ui.el("connection-name").value="New GitLab";
 ui.el("connection-url").value="https://gitlab.invalid";
 const request=api.request;let finish;
 api.request=async(path,body)=>path.endsWith("/gitlab/prepare") ? new Promise(resolve=>{finish=resolve}) : request(path,body);
 const slow=ui.el("connection-url").emit("change");await settle();
 api.request=request;await ui.el("connection-url").emit("change");
 finish({...api.prepared,status:"owner_verification_required",client_import_id:"stale"});await slow;
 assert.equal(ui.el("gitlab-setup").hidden,false);assert.equal(ui.el("gitlab-advanced-title").hidden,true);
 api.request=async(path,body)=>path.endsWith("/gitlab/prepare") ? new Promise((_,reject)=>{finish=reject}) : request(path,body);
 const failed=ui.el("connection-url").emit("change");await settle();
 await ui.el("connection-back").emit("click");finish(Error("late failure"));await failed;
 assert.equal(ui.el("platforms-feedback").textContent,"");
});

test("GitLab modifying application configuration confirms the same affected rooms as logout before opening",async()=>{
 const api=fixture(),ui=await edit(api);
 assert.equal(ui.el("gitlab-app-config-row").hidden,true);
 await ui.el("gitlab-edit-application").emit("click");
 assert.equal(ui.el("gitlab-app-config-row").hidden,true);assert.equal(ui.el("connection-auth-confirmation").hidden,false);
 assert.match(ui.el("connection-auth-confirmation-title").textContent,/修改应用配置将影响以上连接和房间/);
 assert.equal(ui.el("connection-auth-confirm").textContent,"修改配置");assert.deepEqual(referenceNames(ui),["Review"]);
 assert.equal(ui.el("connection-references").dataset.impact,"true");
 assert.equal(api.calls.some(c=>c.path.endsWith("/gitlab/initialize") || c.path.endsWith("/gitlab/login")),false);
 await ui.el("connection-auth-dismiss").emit("click");
 assert.equal(ui.el("gitlab-app-config-row").hidden,true);assert.equal(ui.el("gitlab-edit-application").hidden,false);
 await openApplication(ui);
 assert.equal(ui.el("gitlab-app-config-row").hidden,false);assert.equal(ui.el("connection-auth-confirmation").hidden,true);
 assert.equal(ui.el("connection-name").disabled,true);
 ui.el("gitlab-client-secret").value="unsaved-secret";
 await ui.el("gitlab-edit-cancel").emit("click");
 assert.equal(ui.el("gitlab-app-config-row").hidden,true);assert.equal(ui.el("gitlab-client-secret").value,"");
 assert.equal(ui.el("gitlab-login").hidden,false);assert.equal(ui.el("gitlab-logout").hidden,false);
 assert.equal(ui.el("connection-name").disabled,false);
 assert.equal(api.calls.some(c=>c.path.endsWith("/gitlab/initialize") || c.path.endsWith("/gitlab/login")),false);
});

test("GitLab loading an application edit only opens a confirmation and a stale confirm cannot reopen the editor",async()=>{
 const api=fixture(),ui=await edit(api);const request=api.request;let finish;
 api.request=async(path,body)=>path.endsWith("/gitlab/prepare") ? new Promise(resolve=>{finish=resolve}) : request(path,body);
 const opening=ui.el("gitlab-edit-application").emit("click");await settle();
 assert.equal(ui.el("gitlab-app-config-row").hidden,true);assert.equal(ui.el("connection-auth-confirmation").hidden,true);
 finish({...api.prepared,status:"configuration_required",application_id:undefined});await opening;
 assert.equal(ui.el("gitlab-app-config-row").hidden,true);assert.equal(ui.el("connection-auth-confirmation").hidden,false);
 api.request=request;
 await ui.el("connection-back").emit("click");await ui.el("platforms-list").children[0].emit("click");
 await ui.el("connection-auth-confirm").emit("click");
 assert.equal(ui.el("gitlab-app-config-row").hidden,true);
 assert.equal(api.calls.some(c=>c.path.endsWith("/gitlab/initialize") || c.path.endsWith("/gitlab/login")),false);
});

test("GitLab first authorization failure can correct and authorize the same pending import",async()=>{
 const api=fixture();api.items=[];api.status={status:"failed"};api.prepared={...api.prepared,status:"configuration_required",application_id:undefined,affected:[]};
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");ui.el("connection-name").value="New GitLab";
 ui.el("connection-url").value="https://gitlab.invalid";await ui.el("connection-url").emit("change");
 api.initialized={...api.prepared,status:"owner_verification_required",client_import_id:"first-import"};
 ui.el("gitlab-client-id").value="first-client";ui.el("gitlab-client-secret").value="first-secret";
 await ui.el("gitlab-import").emit("click");
 assert.equal(ui.el("gitlab-advanced-title").hidden,true);assert.equal(ui.el("gitlab-advanced").open,true);
 assert.equal(ui.el("gitlab-setup").hidden,false);assert.equal(ui.el("gitlab-client-secret").value,"");
 assert.equal(ui.el("connection-name").disabled,false);
 ui.el("gitlab-client-id").value="correct-client";ui.el("gitlab-client-secret").value="correct-secret";
 api.prepared=api.initialized;api.initialized={...api.prepared,client_import_id:"correct-import",client_id:"correct-client",scopes:["api","write_repository"]};
 await ui.el("gitlab-import").emit("click");
 const correction=api.calls.filter(c=>c.path.endsWith("/gitlab/initialize")).at(-1).body;
 assert.equal(correction.client_import_id,"first-import");assert.equal(correction.client.client_id,"correct-client");
 assert.equal(ui.el("gitlab-setup").hidden,false);
});
