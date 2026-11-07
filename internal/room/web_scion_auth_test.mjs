import {test} from "node:test";
import assert from "node:assert/strict";
import {launch,settle} from "./web_app_harness.mjs";
const profile=(id="a",executable="/service/scn")=>({version:1,brand:"scion",profile:id.repeat(32),executable,cli_version:"0.1.13"});
function fixture(){
 const api={calls:[],connections:[],profiles:[{profile:"a".repeat(32),username:"alice"}],view:null,hook:null,async request(path,data){
  api.calls.push({path,data:structuredClone(data)});
  if(api.hook){const v=api.hook(path,data);if(v!==undefined)return v;}
  if(path==="/v1/ui")return {managed:true};
  if(path==="/v1/rooms")return {service_epoch:"manager",operator:"tester",rooms:[],teams:[]};
  if(path.startsWith("/v1/platform/connections?"))return {connections:structuredClone(api.connections)};
  if(path==="/v1/platform/connections/scion/discover")return {executable:data.executable||"/service/scn",profiles:structuredClone(api.profiles),unavailable:0};
  if(path==="/v1/platform/connections/scion/login")return {session_id:data.session_id,status:"starting"};
  if(path==="/v1/platform/connections/scion/status")return api.view||{status:"starting"};
  if(path==="/v1/platform/connections/scion/cancel")return {status:"cancelled"};
  if(path==="/v1/platform/connections/save"){
   const c={...structuredClone(data),version:1,validation:{status:"unverified"},references:[]};api.connections=[c];return c;
  }
  if(path==="/v1/platform/connections/verify"){
   const c=api.connections[0];c.version++;c.validation={status:"valid",username:"alice"};return structuredClone(c);
  }
  throw new Error(`unexpected ${path}`);
 }};return api;
}
async function draft(api){
 const ui=await launch(api);await ui.el("open-settings").emit("click");await ui.el("connection-new").emit("click");
 ui.el("connection-provider").value="scion";await ui.el("connection-provider").emit("change");
 ui.el("connection-name").value="我的 Scion";ui.el("connection-url").value="https://scion.example";
 await ui.el("connection-url").emit("change");return ui;
}
test("Scion auto-detects and selects a single managed account without manual profile IDs",async()=>{
 const api=fixture(),ui=await draft(api);
 assert.equal(ui.el("connection-scion-executable").value,"/service/scn");
 assert.equal(ui.el("connection-scion-profile").value,"a".repeat(32));
 assert.equal(ui.el("connection-scion-choice").hidden,true);
 assert.match(ui.el("connection-scion-account").textContent,/alice/);
 await ui.el("connection-form").emit("submit");
 const saved=api.calls.find(c=>c.path.endsWith("/save")).data;
 assert.deepEqual(saved.cli_profile,profile());assert.equal(saved.pat,undefined);
 assert.match(ui.el("connections-feedback").textContent,/账号验证成功/);
});
test("Scion requires an explicit choice for multiple accounts and never replaces a missing saved account",async()=>{
 const api=fixture();api.profiles.push({profile:"b".repeat(32),username:"bob"});
 const ui=await draft(api);
 assert.equal(ui.el("connection-scion-profile").value,"");assert.equal(ui.el("connection-scion-choice").hidden,false);
 await ui.el("connection-form").emit("submit");assert.equal(api.connections.length,0);assert.match(ui.el("connections-feedback").textContent,/连接未保存/);
 ui.el("connection-scion-profile").value="b".repeat(32);await ui.el("connection-scion-profile").emit("change");
 await ui.el("connection-form").emit("submit");assert.equal(api.connections[0].cli_profile.profile,"b".repeat(32));
 api.profiles=[api.profiles[0]];
 await ui.el("connections-list").children[0].emit("click");
 assert.equal(ui.el("connection-scion-profile").value,"");assert.equal(ui.el("connection-scion-choice").hidden,false);
});
test("Scion sends PAT once, clears the password immediately, then saves only the native profile reference",async()=>{
 const api=fixture();api.profiles=[];const ui=await draft(api);
 ui.el("connection-scion-pat").value="test-private-pat";
 await ui.el("connection-scion-pat").emit("keydown",{key:"Enter"});
 assert.equal(ui.el("connection-scion-pat").value,"");assert.equal(ui.el("connection-save").disabled,true);
 assert.equal(ui.el("connection-scion-cancel").hidden,false);
 api.view={status:"succeeded",username:"alice",cli_profile:profile("b")};
 await ui.clock.advance(1000);
 assert.equal(ui.el("connection-scion-profile").value,"b".repeat(32));
 assert.match(ui.el("connections-feedback").textContent,/登录与账号验证成功/);
 await ui.el("connection-form").emit("submit");
 assert.equal(api.calls.filter(c=>c.path.endsWith("/scion/login")).length,1);
 const save=api.calls.find(c=>c.path.endsWith("/save"));assert.equal(save.data.pat,undefined);assert.deepEqual(save.data.cli_profile,profile("b"));
 assert.equal(JSON.stringify([...ui.saved]).includes("test-private-pat"),false);
});
test("a lost Scion login response recovers with status and never resends the PAT",async()=>{
 const api=fixture(),ui=await draft(api);
 api.hook=path=>{if(path.endsWith("/scion/login"))throw new Error("lost response");};
 api.view={status:"succeeded",username:"bob",cli_profile:profile("b")};
 ui.el("connection-scion-pat").value="secret-pat";await ui.el("connection-scion-login").emit("click");
 assert.match(ui.el("connection-scion-account").textContent,/bob/);
 assert.equal(api.calls.filter(c=>c.path.endsWith("/scion/login")).length,1);
 assert.equal(api.calls.filter(c=>c.path.endsWith("/scion/status")).length,1);
});
test("closing Scion authentication cancels the owned flow and ignores a late success",async()=>{
 const api=fixture(),ui=await draft(api);
 ui.el("connection-scion-pat").value="secret-pat";await ui.el("connection-scion-login").emit("click");
 await ui.el("close-settings").emit("click");await settle();
 api.view={status:"succeeded",username:"bob",cli_profile:profile("b")};await ui.clock.advance(2000);
 assert.equal(api.calls.filter(c=>c.path.endsWith("/scion/cancel")).length,1);
 assert.equal(ui.el("connection-scion-pat").value,"");
 assert.notEqual(ui.el("connection-scion-profile").value,"b".repeat(32));
 assert.equal(api.calls.some(c=>c.path.endsWith("/save")),false);
});
test("late Scion discovery cannot populate a different platform or leak an entered PAT",async()=>{
 const api=fixture(),ui=await draft(api);let release;
 api.hook=path=>{if(path.endsWith("/scion/discover"))return new Promise(resolve=>release=resolve);};
 const pending=ui.el("connection-scion-detect").emit("click");await settle();
 ui.el("connection-scion-pat").value="unsubmitted-secret";
 ui.el("connection-provider").value="gitlab";await ui.el("connection-provider").emit("change");
 release({executable:"/late/scn",profiles:[{profile:"b".repeat(32),username:"late"}]});await pending;
 assert.equal(ui.el("connection-scion-pat").value,"");assert.equal(ui.el("connection-scion-row").hidden,true);
 assert.equal(ui.el("connection-save").disabled,false);assert.notEqual(ui.el("connection-scion-executable").value,"/late/scn");
});
test("failed Scion PAT login stays unsaved and clears secrets on return",async()=>{
 const api=fixture(),ui=await draft(api);api.view={status:"failed",error_code:"login_failed"};
 ui.el("connection-scion-pat").value="wrong-pat";await ui.el("connection-scion-login").emit("click");
 assert.match(ui.el("connections-feedback").textContent,/PAT 登录失败/);assert.equal(api.connections.length,0);
 ui.el("connection-scion-pat").value="draft-secret";await ui.el("connection-back").emit("click");
 assert.equal(ui.el("connection-scion-pat").value,"");
});

test("an unsubmitted Scion PAT cannot silently save the previously selected account",async()=>{
 const api=fixture(),ui=await draft(api);
 ui.el("connection-scion-pat").value="draft-new-account-pat";
 await ui.el("connection-form").emit("submit");
 assert.equal(api.calls.some(c=>c.path.endsWith("/save")),false);
 assert.equal(api.calls.some(c=>c.path.endsWith("/scion/login")),false);
 assert.match(ui.el("connections-feedback").textContent,/未提交的 PAT/);
});

test("a rejected Scion login reports input errors without polling or replaying the secret",async()=>{
 const api=fixture(),ui=await draft(api);
 api.hook=path=>{if(path.endsWith("/scion/login"))throw Object.assign(new Error("invalid request"),{code:"request"});};
 ui.el("connection-scion-pat").value="private-pat";
 await ui.el("connection-scion-login").emit("click");
 assert.match(ui.el("connections-feedback").textContent,/HTTPS 实例地址和 PAT/);
 assert.equal(ui.el("connection-scion-pat").value,"");
 assert.equal(api.calls.filter(c=>c.path.endsWith("/scion/login")).length,1);
 assert.equal(api.calls.some(c=>c.path.endsWith("/scion/status")||c.path.endsWith("/scion/cancel")),false);
});
