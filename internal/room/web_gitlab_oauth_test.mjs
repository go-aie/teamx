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
