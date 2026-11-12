import {test} from "node:test";
import assert from "node:assert/strict";
import {readFile} from "node:fs/promises";
import {createAccessSettings} from "../assets/features/access-settings.mjs";

const html = await readFile(new URL("../assets/index.html",import.meta.url),"utf8");
async function launch(api) {
  const fields=new Map([...html.matchAll(/id="([^"]+)"/g)].map(([,id])=>[id,{value:"",checked:false,hidden:false,type:"password",open:false,dataset:{},attributes:{},setAttribute(name,value){this.attributes[name]=value;},listeners:{},addEventListener(name,fn){this.listeners[name]=fn;},async emit(name){await this.listeners[name]?.({preventDefault(){}});},focus(){},select(){},showModal(){this.open=true;},close(){this.open=false;this.listeners.close?.();}}]));
  const events={},writes=[],copied=[],navigation=[],timers=new Map();
  const ui=createAccessSettings({env:{setTimeout:(fn)=>{timers.set(fn,fn);return fn;},clearTimeout:id=>timers.delete(id),document:{activeElement:fields.get("access-settings-save")},request:async(path,body)=>{if(body)writes.push({path,body:structuredClone(body)});return api(path,body);},window:{navigator:{clipboard:{writeText:async value=>copied.push(value)}},location:{assign:path=>navigation.push(path)},addEventListener:(name,fn)=>events[name]=fn}},runtime:{$:id=>fields.get(id),state:{operator:"owner"}}});
  ui.mount();await ui.load();
  return {ui,el:id=>fields.get(id),events,writes,copied,navigation};
}
const initial={version:1,login_enabled:false,remote_enabled:false,public_base_url:"",remote_active:false,writable:true};
const active={...initial,login_enabled:true};
const settle=async()=>{for(let i=0;i<5;i++)await new Promise(resolve=>setImmediate(resolve));};

test("first-use generates a copyable candidate and saves without an old-token confirmation",async()=>{
  const token="random-token-with-enough-length";
  const view=await launch(async(path,body)=>path.endsWith("generate")?{token}:body?{...active,version:2}:initial);
  assert.equal(view.el("access-settings-save").disabled,true);
  view.el("access-login-enabled").checked=true;await view.el("access-login-enabled").emit("change");await settle();
  assert.deepEqual(view.writes.map(v=>v.path),["/v1/access/settings/generate"]);
  assert.equal(view.el("access-new-token").value,token);
  await view.el("access-token-copy").emit("click");assert.deepEqual(view.copied,[token]);
  await view.el("access-settings-form").emit("submit");
  assert.equal(view.writes[1].body.token,token);assert.equal(view.writes[1].body.expected_version,1);
  assert.equal(view.writes[1].body.current_token,undefined);
  assert.equal(view.el("access-confirm-dialog").open,false);
  assert.equal(view.el("access-new-token").value,"");assert.equal(view.el("access-current-token").value,"");
  assert.equal(view.el("access-token-summary").hidden,false);assert.equal(view.el("access-token-fields").hidden,true);
  assert.equal(view.el("access-draft-hint").hidden,true);
});

test("unchecking login only marks a draft; save opens a cancellable verification without a write",async()=>{
  const view=await launch(async()=>active);
  assert.equal(view.el("access-token-summary").hidden,false);assert.equal(view.el("access-token-fields").hidden,true);
  assert.equal(view.el("access-confirm-dialog").open,false);
  view.el("access-login-enabled").checked=false;await view.el("access-login-enabled").emit("change");
  assert.equal(view.el("access-draft-hint").hidden,false);assert.equal(view.el("access-token-fields").hidden,true);
  assert.equal(view.writes.length,0);
  await view.el("access-settings-form").emit("submit");
  assert.equal(view.el("access-confirm-dialog").open,true);assert.equal(view.el("access-confirm-title").textContent,"关闭登录验证");
  assert.equal(view.writes.length,0);
  view.el("access-current-token").value="private-proof";await view.el("access-confirm-cancel").emit("click");
  assert.equal(view.el("access-confirm-dialog").open,false);assert.equal(view.el("access-current-token").value,"");
  assert.equal(view.el("access-draft-hint").hidden,false);assert.equal(view.writes.length,0);
});

test("failed proof stays inside confirmation and a corrected proof applies the original draft",async()=>{
  const view=await launch(async(path,body)=>{
    if(!body)return active;
    if(body.current_token!=="correct-current-token"){const e=new Error("wrong proof");e.code="current_token_required";throw e;}
    return {...initial,version:2};
  });
  view.el("access-login-enabled").checked=false;await view.el("access-login-enabled").emit("change");
  await view.el("access-settings-form").emit("submit");
  view.el("access-current-token").value="wrong";await view.el("access-confirm-form").emit("submit");
  assert.equal(view.writes[0].body.current_token,"wrong");assert.equal(view.writes[0].body.login_enabled,false);
  assert.match(view.el("access-confirm-feedback").textContent,/当前令牌不正确/);
  assert.equal(view.el("access-confirm-dialog").open,true);assert.equal(view.el("access-current-token").value,"");
  view.el("access-current-token").value="correct-current-token";await view.el("access-confirm-form").emit("submit");
  assert.equal(view.el("access-confirm-dialog").open,false);assert.equal(view.el("access-current-token").value,"");
  assert.equal(view.el("access-draft-hint").hidden,true);assert.match(view.el("access-settings-feedback").textContent,/免登录/);
});

test("replacing a token prepares a candidate with the session and verifies once at final save",async()=>{
  const candidate="new-memorable-access-token";
  const view=await launch(async(path,body)=>path.endsWith("generate")?{token:candidate}:body?{...active,version:2}:active);
  await view.el("access-token-change").emit("click");await settle();
  assert.equal(view.writes.length,1);assert.equal(view.writes[0].body.current_token,undefined);
  assert.equal(view.el("access-token-summary").hidden,true);assert.equal(view.el("access-new-token").value,candidate);
  await view.el("access-settings-form").emit("submit");
  assert.equal(view.writes.length,1);assert.equal(view.el("access-confirm-title").textContent,"更换访问令牌");
  await view.el("access-confirm-dialog").emit("cancel");
  assert.equal(view.el("access-confirm-dialog").open,false);assert.equal(view.el("access-new-token").value,candidate);
  await view.el("access-settings-form").emit("submit");
  view.el("access-current-token").value="original-current-token";await view.el("access-confirm-form").emit("submit");
  assert.equal(view.writes[1].body.token,candidate);assert.equal(view.writes[1].body.current_token,"original-current-token");
  assert.equal(view.el("access-token-summary").hidden,false);assert.equal(view.el("access-new-token").value,"");
});

test("remote access forces login and its initial save does not request a nonexistent old token",async()=>{
  const view=await launch(async(path,body)=>path.endsWith("generate")?{token:"sufficiently-long-random-token"}:body?{...active,version:2,remote_enabled:true,public_base_url:body.public_base_url,restart_required:true}:initial);
  view.el("access-remote-enabled").checked=true;await view.el("access-remote-enabled").emit("change");await settle();
  assert.equal(view.el("access-login-enabled").checked,true);assert.equal(view.el("access-login-enabled").disabled,true);
  view.el("access-public-url").value="https://assistant.example.com";
  await view.el("access-settings-form").emit("submit");
  assert.equal(view.writes.at(-1).body.public_base_url,"https://assistant.example.com");assert.equal(view.el("access-confirm-dialog").open,false);
  assert.match(view.el("access-settings-feedback").textContent,/重启/);
});

test("cancelling a token edit removes its candidate and returns to the configured-token summary",async()=>{
  const view=await launch(async(path)=>path.endsWith("generate")?{token:"private-candidate-token"}:active);
  await view.el("access-token-change").emit("click");await settle();
  await view.el("access-token-edit-cancel").emit("click");
  assert.equal(view.el("access-token-fields").hidden,true);assert.equal(view.el("access-token-summary").hidden,false);
  assert.equal(view.el("access-new-token").value,"");assert.equal(view.el("access-settings-save").disabled,true);
});

test("closing while generation is pending prevents a late token from repopulating the form",async()=>{
  let release;
  const view=await launch(async(path)=>path.endsWith("generate")?new Promise(resolve=>release=resolve):initial);
  const pending=view.el("access-token-generate").emit("click");view.ui.clear();release({token:"late-private-credential"});await pending;
  assert.equal(view.el("access-new-token").value,"");
});

test("an in-flight confirmation cannot submit twice and pagehide clears both credential inputs",async()=>{
  let release;
  const view=await launch(async(path,body)=>body?new Promise(resolve=>release=resolve):active);
  view.el("access-login-enabled").checked=false;await view.el("access-login-enabled").emit("change");
  await view.el("access-settings-form").emit("submit");view.el("access-current-token").value="current-proof-token";
  const first=view.el("access-confirm-form").emit("submit");await settle();
  await view.el("access-confirm-form").emit("submit");assert.equal(view.writes.length,1);
  view.events.pagehide();assert.equal(view.el("access-confirm-dialog").open,false);
  assert.equal(view.el("access-current-token").value,"");assert.equal(view.el("access-new-token").value,"");
  release({...initial,version:2});await first;
  assert.equal(view.el("access-settings-feedback").textContent,"");
});
