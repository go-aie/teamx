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
  if(path.endsWith("/gitlab/prepare"))return structuredClone(api.prepared);
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
test("GitLab OAuth login confirms references and polls confirmed save",async()=>{
 const api=fixture(),ui=await edit(api);const confirmations=[];api.confirm=t=>{confirmations.push(t);return true};
 assert.match(ui.el("gitlab-account").textContent,/已连接/);
 assert.equal(ui.el("gitlab-setup").hidden,true);
 let opened; const popup={closed:false,opener:{},location:{replace:url=>{opened=url}},close(){this.closed=true}};ui.window.open=()=>popup;
 await ui.el("gitlab-login").emit("click");assert.equal(ui.el("gitlab-login-link").hidden,false);assert.match(opened,/^https:\/\/gitlab.invalid\/oauth\/authorize/);assert.equal(popup.opener,null);
 const request=api.calls.find(c=>c.path.endsWith("/gitlab/login")).body;
 assert.deepEqual(request.affected,api.prepared.affected);assert.match(confirmations[0],/Company GitLab.*Review/);
 assert.equal(api.calls.filter(c=>c.path.endsWith("/connections/save")).length,0);
 api.status={status:"succeeded",connections:[{...api.items[0],version:3,credential_kind:"oauth",authorization_status:"active"}]};await ui.clock.advance(1500);
 assert.match(ui.el("gitlab-account").textContent,/已连接.*tester/);assert.match(ui.el("connections-feedback").textContent,/已保存.*核验成功/);
 assert.equal(ui.el("gitlab-setup").hidden,true);
 assert.equal(ui.el("gitlab-login-link").href,"");assert.doesNotMatch(JSON.stringify([...ui.saved]),/fixture-state/);
});
test("GitLab closing cancels the browser flow and ignores a late status",async()=>{
 const api=fixture(),ui=await edit(api);await ui.el("gitlab-login").emit("click");let finish;
 api.status=()=>new Promise(r=>{finish=r});const polling=ui.clock.advance(1500);await settle();await ui.el("close-settings").emit("click");
 finish({status:"succeeded",connections:[{...api.items[0],validation:{status:"valid",username:"late-user"}}]});await polling;
 assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/cancel")).length,1);assert.equal(ui.el("gitlab-login-link").href,"");assert.doesNotMatch(ui.el("gitlab-account").textContent,/late-user/);
});
test("GitLab shared logout displays local disconnect when remote revocation is unknown",async()=>{
 const api=fixture("oauth"),ui=await edit(api);api.status={status:"disconnected",connections:[{...api.items[0],authorization_status:"logged_out",revocation_status:"unconfirmed",credential_configured:false,validation:{status:"unverified"}}]};
 await ui.el("gitlab-logout").emit("click");assert.match(ui.el("gitlab-account").textContent,/本地已断开.*未确认/);assert.match(ui.el("connections-feedback").textContent,/本地已断开/);
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
 const api=fixture(),ui=await edit(api);api.status=()=>{throw Error("read failed")};await ui.el("gitlab-login").emit("click");
 assert.match(ui.el("connections-feedback").textContent,/状态读取失败/);assert.equal(ui.el("gitlab-cancel").hidden,false);
 await ui.el("gitlab-cancel").emit("click");assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/cancel")).length,1);assert.equal(api.calls.filter(c=>c.path.endsWith("/gitlab/login")).length,1);
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
