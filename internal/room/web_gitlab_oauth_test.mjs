import {test} from "node:test";
import assert from "node:assert/strict";
import {launch,settle} from "./web_app_harness.mjs";
function fixture(kind="oauth") {
 const item={id:"original",name:"Company GitLab",provider:"gitlab",url:"https://gitlab.invalid",version:2,credential_kind:kind,credential_configured:true,authorization_status:kind==="oauth"?"active":undefined,validation:{status:"valid",username:"tester"},references:[{room_id:"R",room_instance:"instance",name:"Review"}]};
 const api={calls:[],items:[item],prepared:{status:"ready",application_id:"app",redirect_uri:"http://127.0.0.1:8088/v1/platform/connections/gitlab/callback",applications_url:"https://gitlab.invalid/-/user_settings/applications",affected:[{id:item.id,version:2,referenced_rooms:[{room_id:"R",room_instance:"instance"}]}]},status:{session_id:"session",connection_id:item.id,status:"waiting"},async request(path,body){
  api.calls.push({path,body:structuredClone(body)});
  if(path==="/v1/ui")return {managed:true};
  if(path==="/v1/rooms")return {service_epoch:"manager",operator:"tester",rooms:[],teams:[]};
  if(path.startsWith("/v1/platform/connections?"))return {connections:structuredClone(api.items)};
  if(path.endsWith("/gitlab/prepare"))return structuredClone(body.edit_application ? api.editPrepared || api.prepared : api.prepared);
  if(path.endsWith("/gitlab/initialize"))return api.initialized || structuredClone(api.prepared);
  if(path.endsWith("/gitlab/login"))return {session_id:"session",connection_id:api.targetID || item.id,status:"waiting",authorization_url:"https://gitlab.invalid/oauth/authorize?state=fixture-state"};
  if(path.endsWith("/gitlab/logout"))return {session_id:"session",connection_id:item.id,status:"disconnecting"};
  if(path.endsWith("/gitlab/status"))return typeof api.status==="function" ? api.status() : structuredClone(api.status);
  if(path.endsWith("/gitlab/cancel"))return {status:"cancelled"};
  if(path.endsWith("/connections/save"))return {...item,...body,version:3};
  throw Error(`Unexpected ${path}`);
 }};return api;
}
async function edit(api) {const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connections-list").children[0].emit("click");return ui;}
function referenceNames(ui) {return ui.el("connection-reference-list").children.flatMap(group=>group.children.at(-1).children.map(row=>row.children[0]?.textContent));}
async function authorize(ui, action="login") {
 await ui.el(`gitlab-${action}`).emit("click");
 if (!ui.el("connection-auth-confirmation").hidden) await ui.el("connection-auth-confirm").emit("click");
}
test("GitLab OAuth login confirms references and polls confirmed save",async()=>{
 const api=fixture(),ui=await edit(api);api.confirm=()=>{throw Error("native confirmation must not open")};
 assert.match(ui.el("gitlab-account").textContent,/已连接/);
 assert.equal(ui.el("connection-references").hidden,false);assert.deepEqual(referenceNames(ui),["Review"]);
 assert.equal(ui.el("connection-references").dataset.impact,"false");
 assert.equal(ui.el("gitlab-setup").hidden,true);
 let opened, popups=0; const popup={closed:false,opener:{},location:{replace:url=>{opened=url}},close(){this.closed=true}};ui.window.open=()=>{popups++;return popup};
 await ui.el("gitlab-login").emit("click");
 assert.equal(popups,0);assert.equal(api.calls.some(c=>c.path.endsWith("/gitlab/login")),false);
 assert.equal(ui.el("connection-auth-confirmation").hidden,false);assert.deepEqual(referenceNames(ui),["Review"]);
 assert.equal(ui.el("connection-references").hidden,false);assert.equal(ui.el("connection-references").dataset.impact,"true");
 assert.equal(ui.el("connection-name").disabled,true);assert.equal(ui.el("connection-save").disabled,true);
 await ui.el("connection-auth-confirm").emit("click");assert.equal(popups,1);assert.equal(ui.el("connection-auth-confirmation").hidden,true);
 assert.equal(ui.el("connection-references").hidden,false);
 assert.equal(ui.el("connection-references").dataset.impact,"true");
 assert.equal(ui.el("gitlab-login-link").hidden,false);assert.match(opened,/^https:\/\/gitlab.invalid\/oauth\/authorize/);assert.equal(popup.opener,null);
 await ui.el("connection-auth-confirm").emit("click");assert.equal(popups,1);
 const request=api.calls.find(c=>c.path.endsWith("/gitlab/login")).body;
 assert.deepEqual(request.affected,api.prepared.affected);assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/login")).length,1);
 assert.equal(api.calls.filter(c=>c.path.endsWith("/connections/save")).length,0);
 api.status={status:"succeeded",connections:[{...api.items[0],version:3,credential_kind:"oauth",authorization_status:"active"}]};await ui.clock.advance(1500);
 assert.match(ui.el("gitlab-account").textContent,/已连接.*tester/);assert.match(ui.el("connections-feedback").textContent,/已保存.*核验成功/);
 assert.equal(ui.el("gitlab-setup").hidden,true);
 assert.equal(ui.el("connection-references").dataset.impact,"false");
 assert.equal(ui.el("gitlab-login-link").href,"");assert.doesNotMatch(JSON.stringify([...ui.saved]),/fixture-state/);
});
test("GitLab closing cancels the browser flow and ignores a late status",async()=>{
 const api=fixture(),ui=await edit(api);await authorize(ui);let finish;
 api.status=()=>new Promise(r=>{finish=r});const polling=ui.clock.advance(1500);await settle();await ui.el("close-settings").emit("click");
 finish({status:"succeeded",connections:[{...api.items[0],validation:{status:"valid",username:"late-user"}}]});await polling;
 assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/cancel")).length,1);assert.equal(ui.el("gitlab-login-link").href,"");assert.doesNotMatch(ui.el("gitlab-account").textContent,/late-user/);
});
test("GitLab shared logout displays local disconnect when remote revocation is unknown",async()=>{
 const api=fixture("oauth"),ui=await edit(api);api.status={status:"disconnected",connections:[{...api.items[0],authorization_status:"logged_out",revocation_status:"unconfirmed",credential_configured:false,validation:{status:"unverified"}}]};
 await ui.el("gitlab-logout").emit("click");assert.equal(ui.el("connection-auth-confirm").textContent,"确认退出");
 await ui.el("connection-auth-confirm").emit("click");assert.match(ui.el("gitlab-account").textContent,/本地已断开.*未确认/);assert.match(ui.el("connections-feedback").textContent,/本地已断开/);
 assert.equal(ui.el("gitlab-setup").hidden,true);assert.equal(ui.el("gitlab-logout").hidden,true);
});
test("GitLab new connection keeps imported configuration separate from account validation and saves only after login",async()=>{
 const api=fixture();api.items=[];api.prepared={...api.prepared,status:"configuration_required",affected:[]};const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");
 ui.el("connection-name").value="New GitLab";ui.el("connection-url").value="https://gitlab.invalid";
 await ui.el("connection-form").emit("submit");assert.match(ui.el("connections-feedback").textContent,/应用配置/);assert.equal(api.calls.filter(c=>c.path.endsWith("/connections/save")).length,0);
 api.prepared={...api.prepared,status:"owner_verification_required",application_id:undefined,client_import_id:"imported"};api.initialized=api.prepared;
 ui.el("gitlab-client-id").value="fixture-client";ui.el("gitlab-client-secret").value="private-import-secret";
 await ui.el("gitlab-import").emit("click");assert.equal(ui.el("gitlab-client-secret").value,"");assert.doesNotMatch(JSON.stringify([...ui.saved]),/private-import-secret/);
 await ui.el("gitlab-login").emit("click");const login=api.calls.find(c=>c.path.endsWith("/gitlab/login")).body;assert.equal(login.client_import_id,"imported");assert.equal(login.name,"New GitLab");assert.equal(login.id,undefined);
});
test("GitLab status failure preserves a cancel path and never claims success or retries login",async()=>{
 const api=fixture(),ui=await edit(api);api.status=()=>{throw Error("read failed")};await authorize(ui);
 assert.match(ui.el("connections-feedback").textContent,/状态读取失败/);assert.equal(ui.el("gitlab-cancel").hidden,false);
 await ui.el("gitlab-cancel").emit("click");assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/cancel")).length,1);assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/login")).length,1);
});
test("GitLab inline confirmation can be cancelled or left without starting authorization",async()=>{
 const api=fixture(),ui=await edit(api);let popups=0;ui.window.open=()=>{popups++;return null};
 await ui.el("gitlab-login").emit("click");await ui.el("connection-auth-dismiss").emit("click");
 assert.equal(ui.el("connection-auth-confirmation").hidden,true);assert.equal(ui.el("connection-name").disabled,false);assert.equal(ui.el("connection-save").disabled,false);
 assert.match(ui.el("connections-feedback").textContent,/已取消/);
 assert.equal(ui.el("connection-references").hidden,false);
 assert.equal(ui.el("connection-references").dataset.impact,"false");assert.deepEqual(referenceNames(ui),["Review"]);
 await ui.el("gitlab-login").emit("click");await ui.el("connection-back").emit("click");
 await ui.el("connection-auth-confirm").emit("click");
 await ui.el("connections-list").children[0].emit("click");assert.equal(ui.el("connection-auth-confirmation").hidden,true);
 await ui.el("gitlab-login").emit("click");await ui.el("close-settings").emit("click");await ui.el("connection-auth-confirm").emit("click");
 assert.equal(popups,0);assert.equal(api.calls.some(c=>/\/gitlab\/(login|logout|cancel)$/.test(c.path)),false);
});
test("GitLab shared authorization groups every affected room and preserves pending bindings",async()=>{
 const api=fixture();api.items[0].references.push({room_id:"P",room_instance:"pending",name:"Next Room",pending:true});
 const other={...api.items[0],id:"other",name:"Shared GitLab",references:Array.from({length:25},(_,i)=>({room_id:`R${i}`,room_instance:"other",name:`Room ${i}`}))};
 api.items.push(other);
 api.prepared.affected=api.items.map(item=>({id:item.id,version:item.version,referenced_rooms:item.references.map(({room_id,room_instance})=>({room_id,room_instance}))}));
 const ui=await edit(api);assert.deepEqual(referenceNames(ui),["Review","Next Room"]);
 await ui.el("gitlab-login").emit("click");assert.equal(ui.el("connection-references-title").textContent,"引用房间 · 27");
 assert.equal(ui.el("connection-reference-list").children.length,2);assert.equal(ui.el("connection-reference-list").children[1].children[0].textContent,"Shared GitLab");
 assert.equal(referenceNames(ui).length,27);assert.equal(referenceNames(ui).at(-1),"Room 24");
 assert.equal(ui.el("connection-reference-list").children[0].children.at(-1).children[1].children[1].textContent,"待完成绑定");
 await ui.el("connection-auth-dismiss").emit("click");assert.deepEqual(referenceNames(ui),["Review","Next Room"]);
});
test("GitLab connection without shared impact logs in directly and keeps a link if the popup is blocked",async()=>{
 const api=fixture();api.items[0].references=[];api.prepared.affected=[{id:"original",version:2}];const ui=await edit(api);
 await ui.el("gitlab-login").emit("click");assert.equal(ui.el("connection-auth-confirmation").hidden,true);
 assert.equal(ui.el("gitlab-login-link").hidden,false);assert.match(ui.el("connections-feedback").textContent,/授权页面/);
 assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/login")).length,1);
});
test("Git repository scope is explicitly selected during application setup",async()=>{
 const api=fixture();api.items=[];api.prepared.status="configuration_required";const ui=await launch(api);
 await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");ui.el("connection-name").value="New GitLab";ui.el("connection-url").value="https://gitlab.invalid";
 ui.el("gitlab-repository-access").checked=true;
 ui.el("gitlab-client-id").value="client";ui.el("gitlab-client-secret").value="secret";
 await ui.el("gitlab-import").emit("click");
 const imports=api.calls.filter(c=>c.path.endsWith("/gitlab/initialize"));
 assert.deepEqual(imports[0].body.client.scopes,["api","write_repository"]);
});

test("GitLab failed first login can correct application credentials and use the replacement after reopening",async()=>{
 const api=fixture();api.items=[];
 api.prepared={...api.prepared,status:"owner_verification_required",application_id:undefined,client_import_id:"wrong-import",affected:[]};
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");
 ui.el("connection-name").value="New GitLab";ui.el("connection-url").value="https://gitlab.invalid";
 api.status={status:"failed"};
 await ui.el("connection-form").emit("submit");
 assert.equal(ui.el("gitlab-advanced").open,true);assert.equal(ui.el("gitlab-advanced-title").hidden,true);
 assert.equal(ui.el("gitlab-setup").hidden,false);
 ui.el("gitlab-client-id").value="correct-client";ui.el("gitlab-client-secret").value="correct-secret";
 api.initialized={...api.prepared,client_import_id:"correct-import"};
 await ui.el("gitlab-import").emit("click");
 const replacement=api.calls.find(c=>c.path.endsWith("/gitlab/initialize")).body;
 assert.equal(replacement.client_import_id,"wrong-import");assert.equal(replacement.client.client_id,"correct-client");
 assert.equal(replacement.client.client_secret,"correct-secret");assert.equal(ui.el("gitlab-client-secret").value,"");
 assert.doesNotMatch(JSON.stringify([...ui.saved]),/correct-secret/);
 api.prepared=api.initialized;
 await ui.el("connection-back").emit("click");await ui.el("connection-new").emit("click");
 ui.el("connection-name").value="New GitLab";ui.el("connection-url").value="https://gitlab.invalid";
 api.status={status:"waiting"};await ui.el("connection-form").emit("submit");
 assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/login")).at(-1).body.client_import_id,"correct-import");
 assert.equal(ui.el("gitlab-advanced-title").attributes["aria-disabled"],"true");
});

test("GitLab configuration save conflict keeps correction form open and clears the Secret",async()=>{
 const api=fixture();api.items=[];api.prepared={...api.prepared,status:"owner_verification_required",application_id:undefined,client_import_id:"pending",affected:[]};
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");
 ui.el("connection-url").value="https://gitlab.invalid";await ui.el("connection-url").emit("change");
 await ui.el("gitlab-import").emit("click");assert.equal(ui.el("gitlab-setup").hidden,false);
 assert.match(ui.el("gitlab-setup-feedback").textContent,/请填写/);
 ui.el("gitlab-client-id").value="client";ui.el("gitlab-client-secret").value="secret";
 const request=api.request;api.request=async(path,body)=>{if(path.endsWith("/gitlab/initialize"))throw {code:"conflict"};return request(path,body);};
 await ui.el("gitlab-import").emit("click");
 assert.equal(ui.el("gitlab-setup").hidden,false);assert.equal(ui.el("gitlab-client-secret").value,"");
 assert.equal(ui.el("gitlab-advanced-title").attributes["aria-disabled"],"false");assert.match(ui.el("gitlab-setup-feedback").textContent,/已变化.*返回列表重新打开连接/);
 assert.equal(ui.el("gitlab-setup-feedback").attributes.role,"alert");assert.equal(ui.el("connections-feedback").textContent,"");
});

test("GitLab first and saved unverified applications show the same form directly",async()=>{
 const api=fixture();api.items=[];api.prepared={...api.prepared,status:"configuration_required",application_id:undefined,affected:[]};
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");
 assert.equal(ui.el("gitlab-advanced-title").hidden,true);assert.equal(ui.el("gitlab-advanced").open,true);
 assert.equal(ui.el("gitlab-setup").hidden,false);
 ui.el("connection-url").value="https://gitlab.invalid";await ui.el("connection-url").emit("change");
 assert.equal(ui.el("gitlab-setup").hidden,false);
 await ui.el("connection-back").emit("click");
 api.items=[{...fixture().items[0],credential_configured:false,authorization_status:undefined,validation:{status:"unverified"}}];
 api.prepared={...api.prepared,status:"owner_verification_required",client_import_id:"pending"};
 await ui.el("open-settings").emit("click");await ui.el("connections-list").children[0].emit("click");
 assert.equal(ui.el("gitlab-advanced").open,true);
 assert.equal(ui.el("gitlab-setup").hidden,false);
 assert.equal(ui.el("gitlab-advanced-title").hidden,true);
});

for (const status of ["succeeded","failed"]) test(`GitLab verified application replacement stays in advanced settings and ${status} preserves the expected connection`,async()=>{
 const api=fixture();api.editPrepared={...api.prepared,status:"configuration_required",application_id:undefined,scopes:["api","write_repository"]};
 const ui=await edit(api);
 assert.equal(ui.el("gitlab-advanced-title").hidden,false);assert.equal(ui.el("gitlab-advanced").open,false);
 assert.equal(ui.el("gitlab-setup").hidden,true);
 await ui.el("gitlab-advanced-title").emit("click");
 assert.equal(ui.el("gitlab-setup").hidden,false);assert.equal(ui.el("gitlab-client-secret").value,"");
 assert.equal(ui.el("gitlab-repository-access").checked,true);assert.equal(ui.el("gitlab-import").textContent,"验证并更新");
 ui.el("gitlab-client-id").value="replacement-client";ui.el("gitlab-client-secret").value="replacement-secret";
 api.initialized={...api.editPrepared,status:"owner_verification_required",client_import_id:"replacement"};
 await ui.el("gitlab-import").emit("click");
 assert.equal(ui.el("gitlab-client-secret").value,"");assert.match(ui.el("gitlab-account").textContent,/已连接/);
 assert.equal(ui.el("connection-auth-confirmation").hidden,false);assert.match(ui.el("connection-auth-confirmation-title").textContent,/更换应用配置.*共享连接/);
 assert.equal(api.calls.some(c=>c.path.endsWith("/gitlab/login")),false);
 await ui.el("connection-auth-confirm").emit("click");
 const login=api.calls.find(c=>c.path.endsWith("/gitlab/login")).body;
 assert.equal(login.client_import_id,"replacement");assert.equal(login.application_id,undefined);assert.deepEqual(login.affected,api.prepared.affected);
 api.status={status,...(status === "succeeded" ? {connections:[{...api.items[0],version:3}]} : {})};
 await ui.clock.advance(1500);
 assert.match(ui.el("gitlab-account").textContent,/已连接.*tester/);
 if(status === "succeeded") {assert.equal(ui.el("gitlab-advanced").open,false);assert.equal(ui.el("gitlab-setup").hidden,true);}
 else {assert.match(ui.el("gitlab-setup-feedback").textContent,/授权未完成/);assert.equal(ui.el("connections-feedback").textContent,"");}
 assert.doesNotMatch(JSON.stringify([...ui.saved]),/replacement-secret/);
});

test("GitLab application hints follow their actions and permissions follow the code access option",async()=>{
 const api=fixture();api.items=[];const ui=await launch(api);
 await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");
 assert.equal(ui.el("gitlab-setup-hint").textContent,"保存后登录 GitLab 完成验证");
 ui.el("gitlab-repository-access").checked=false;await ui.el("gitlab-repository-access").emit("change");
 assert.equal(ui.el("gitlab-scope-hint").textContent,"GitLab 应用权限：api");
 ui.el("gitlab-repository-access").checked=true;await ui.el("gitlab-repository-access").emit("change");
 assert.equal(ui.el("gitlab-scope-hint").textContent,"GitLab 应用权限：api + write_repository");
 await ui.el("connection-back").emit("click");api.items=fixture().items;
 await ui.el("open-settings").emit("click");await ui.el("connections-list").children[0].emit("click");
 assert.equal(ui.el("gitlab-setup-hint").textContent,"验证成功后生效，失败保留原连接");
});

test("GitLab reopening application settings restores saved public fields and leaves Secret empty",async()=>{
 const api=fixture();api.editPrepared={...api.prepared,status:"configuration_required",application_id:undefined,client_id:"saved-client",scopes:["api","write_repository"]};
 const ui=await edit(api);await ui.el("gitlab-advanced-title").emit("click");
 assert.equal(ui.el("gitlab-client-id").value,"saved-client");assert.equal(ui.el("gitlab-client-secret").value,"");
 assert.equal(ui.el("gitlab-redirect-uri").value,api.prepared.redirect_uri);assert.equal(ui.el("gitlab-repository-access").checked,true);
 ui.el("gitlab-client-id").value="draft-client";ui.el("gitlab-client-secret").value="draft-secret";
 await ui.el("gitlab-advanced-title").emit("click");await ui.el("gitlab-advanced-title").emit("click");
 assert.equal(ui.el("gitlab-client-id").value,"draft-client");assert.equal(ui.el("gitlab-client-secret").value,"draft-secret");
 await ui.el("connection-back").emit("click");await ui.el("connections-list").children[0].emit("click");await ui.el("gitlab-advanced-title").emit("click");
 assert.equal(ui.el("gitlab-client-id").value,"saved-client");assert.equal(ui.el("gitlab-client-secret").value,"");
 assert.equal(ui.el("gitlab-repository-access").checked,true);assert.equal(ui.el("gitlab-setup-feedback").hidden,true);
});

test("GitLab invalid application feedback stays beside verification and corrections reuse the pending candidate",async()=>{
 const api=fixture();api.editPrepared={...api.prepared,status:"configuration_required",application_id:undefined,client_id:"saved-client"};
 const ui=await edit(api);await ui.el("gitlab-advanced-title").emit("click");
 ui.el("gitlab-client-secret").value="wrong-secret";
 api.initialized={...api.editPrepared,status:"owner_verification_required",client_import_id:"candidate"};
 api.status={status:"failed",error_code:"invalid_client"};
 await ui.el("gitlab-import").emit("click");await ui.el("connection-auth-confirm").emit("click");
 assert.match(ui.el("gitlab-setup-feedback").textContent,/应用凭据无效.*Application ID 和 Secret/);
 assert.equal(ui.el("gitlab-setup-feedback").hidden,false);assert.equal(ui.el("gitlab-setup-feedback").attributes.role,"alert");
 assert.equal(ui.el("connections-feedback").textContent,"");assert.equal(ui.el("gitlab-advanced").open,true);
 assert.equal(ui.el("gitlab-client-id").value,"saved-client");assert.equal(ui.el("gitlab-client-secret").value,"");
 ui.el("gitlab-client-secret").value="correct-secret";await ui.el("gitlab-import").emit("click");
 const correction=api.calls.filter(c=>c.path.endsWith("/gitlab/initialize")).at(-1).body;
 assert.equal(correction.client_import_id,"candidate");assert.equal(correction.client.client_secret,"correct-secret");
});

test("GitLab automatic application discovery ignores superseded reads and failures after leaving the form",async()=>{
 const api=fixture();api.items=[];api.prepared={...api.prepared,status:"configuration_required",application_id:undefined,affected:[]};
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");
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
 assert.equal(ui.el("connections-feedback").textContent,"");
});

test("GitLab single application disclosure collapses and reopens without clearing a credential draft",async()=>{
 const api=fixture();api.editPrepared={...api.prepared,status:"configuration_required",application_id:undefined};
 const ui=await edit(api);await ui.el("gitlab-advanced-title").emit("click");
 ui.el("gitlab-client-id").value="draft-client";ui.el("gitlab-client-secret").value="draft-secret";
 const reads=api.calls.filter(c=>c.path.endsWith("/gitlab/prepare")).length;
 await ui.el("gitlab-advanced-title").emit("click");assert.equal(ui.el("gitlab-advanced").open,false);
 await ui.el("gitlab-advanced-title").emit("click");assert.equal(ui.el("gitlab-advanced").open,true);
 assert.equal(ui.el("gitlab-client-id").value,"draft-client");assert.equal(ui.el("gitlab-client-secret").value,"draft-secret");
 assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/prepare")).length,reads);
 assert.equal(api.calls.some(c=>c.path.endsWith("/gitlab/initialize")),false);
});

test("GitLab collapsing while application configuration loads is not undone by its response",async()=>{
 const api=fixture(),ui=await edit(api);const request=api.request;let finish;
 api.request=async(path,body)=>path.endsWith("/gitlab/prepare") ? new Promise(resolve=>{finish=resolve}) : request(path,body);
 const opening=ui.el("gitlab-advanced-title").emit("click");await settle();
 await ui.el("gitlab-advanced-title").emit("click");assert.equal(ui.el("gitlab-advanced").open,false);
 finish({...api.prepared,status:"configuration_required",application_id:undefined});await opening;
 assert.equal(ui.el("gitlab-advanced").open,false);
 await ui.el("gitlab-advanced-title").emit("click");assert.equal(ui.el("gitlab-advanced").open,true);
});

test("GitLab first application remains directly editable after saving and corrects the same pending import",async()=>{
 const api=fixture();api.items=[];api.prepared={...api.prepared,status:"configuration_required",application_id:undefined,affected:[]};
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");
 ui.el("connection-url").value="https://gitlab.invalid";await ui.el("connection-url").emit("change");
 api.initialized={...api.prepared,status:"owner_verification_required",client_import_id:"first-import"};
 ui.el("gitlab-client-id").value="first-client";ui.el("gitlab-client-secret").value="first-secret";
 await ui.el("gitlab-import").emit("click");
 assert.equal(ui.el("gitlab-advanced-title").hidden,true);assert.equal(ui.el("gitlab-advanced").open,true);
 assert.equal(ui.el("gitlab-setup").hidden,false);assert.equal(ui.el("gitlab-client-secret").value,"");
 ui.el("gitlab-client-id").value="correct-client";ui.el("gitlab-client-secret").value="correct-secret";
 api.prepared=api.initialized;api.initialized={...api.prepared,client_import_id:"correct-import"};
 await ui.el("gitlab-import").emit("click");
 const correction=api.calls.filter(c=>c.path.endsWith("/gitlab/initialize")).at(-1).body;
 assert.equal(correction.client_import_id,"first-import");assert.equal(correction.client.client_id,"correct-client");
 assert.equal(ui.el("gitlab-setup").hidden,false);
});
