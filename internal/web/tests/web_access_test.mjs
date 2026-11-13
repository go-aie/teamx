import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import vm from "node:vm";
import { webcrypto } from "node:crypto";
import { randomUUID } from "../assets/random-id.mjs";
import * as modelForm from "../assets/model-form.mjs";
import { settle } from "./web_app_harness.mjs";

const source = (await readFile(new URL("../assets/access.mjs", import.meta.url), "utf8")).replace(/^import .*;\n/gm, "");
const html = await readFile(new URL("../assets/access.html", import.meta.url), "utf8");

test("password-manager fields cannot fall back to putting credentials in the URL",async()=>{
  assert.match(html,/<form id="login-form"[^>]*method="post"[^>]*action="\/v1\/access\/login"/);
  assert.match(html,/<input id="token"[^>]*autocomplete="current-password"/);
  const app = await readFile(new URL("../assets/index.html",import.meta.url),"utf8");
  for (const id of ["access-settings-form","access-confirm-form"]) {
    assert.match(app,new RegExp(`<form id="${id}"[^>]*method="post"[^>]*action="/v1/access/settings"`));
  }
});

async function launch(api, saved = new Map(), hash = "") {
  const elements = new Map([...html.matchAll(/id="([^"]+)"/g)].map(([,id])=>[id, {
    value:"",hidden:false,disabled:false,listeners:{},
    addEventListener(name,fn){(this.listeners[name] ||= []).push(fn);},
    setAttribute(){},removeAttribute(){},focus(){},
    async emit(name){for(const fn of this.listeners[name] || []) await fn({preventDefault(){}});}
  }]));
  const el = id=>elements.get(id);
  el("reload-model-draft").hidden=true;
  el("model-form").elements=[...html.matchAll(/<(?:input|select|button) id="([^"]+)"/g)].map(([,id])=>el(id)).filter(field=>![el("login-submit"),el("logout"),el("retry-migration"),el("check-setup-operation"),el("reload-model-draft"),el("toggle-token"),el("token")].includes(field));
  const timers=[],navigation=[],documentEvents=new Map();
  vm.runInNewContext(source, {...modelForm,randomUUID,URL,crypto:webcrypto,
    document:{hidden:false,getElementById:el,addEventListener:(name,fn)=>documentEvents.set(name,fn)},navigator:{onLine:true},
    location:{origin:"http://fixture",hash,replace:path=>navigation.push(path)},addEventListener(){},
    sessionStorage:{getItem:key=>saved.get(key),setItem:(key,value)=>saved.set(key,value),removeItem:key=>saved.delete(key)},
    setTimeout:fn=>timers.push(fn),
    fetch:async(path,options)=>{
      const result=await api(path,options.body ? JSON.parse(options.body) : undefined);
      return {ok:result.status<400,status:result.status,json:async()=>result.data};
    }
  });
  await settle();
  return {el,saved,navigation,async foreground(){documentEvents.get("visibilitychange")();await settle();},async tick(){timers.shift()?.();await settle();}};
}
const initial = {setup_required:true,writable:true,version:0,model:{protocol:"openai-completions",model_id:"fixture-model"}};

test("first-use unknown submission survives failed reconciliation and reload without a second write",async()=>{
  const writes=[];let offline=false,operation;
  const api=async(path,body)=>{
    if(path==="/v1/access/session") return {status:200,data:{operator:"tester",installation_id:"instance"}};
    if(path==="/v1/setup/model") {writes.push(body);offline=true;operation={operation_id:body.request_id,status:"validating"};throw Error("lost receipt");}
    if(offline) throw Error("offline");
    return {status:200,data:{...initial,operation}};
  };
  const ui=await launch(api);
  ui.el("api-key").value="private-fixture-key";await ui.el("model-form").emit("submit");
  assert.equal(ui.el("save-model").disabled,true);assert.equal(ui.el("reload-model-draft").disabled,true);
  await ui.el("model-form").emit("submit");await ui.el("reload-model-draft").emit("click");await ui.tick();
  assert.equal(writes.length,1);assert.doesNotMatch(JSON.stringify([...ui.saved]),/private-fixture-key/);
  assert.match(JSON.stringify([...ui.saved]),new RegExp(writes[0].request_id));
  offline=false;
  const reopened=await launch(api,ui.saved);
  assert.equal(reopened.el("save-model").disabled,true);assert.equal(writes.length,1);
  operation={...operation,status:"saved"};await reopened.tick();assert.equal(reopened.el("save-model").disabled,true);
  operation={...operation,status:"failed",message:"服务重启中断，请重新验证"};await reopened.tick();
  assert.equal(reopened.el("save-model").disabled,false);assert.match(reopened.el("feedback").textContent,/服务重启/);
  assert.doesNotMatch(JSON.stringify([...ui.saved]),new RegExp(writes[0].request_id));
});

test("first-use authoritative absence releases the draft only for a new explicit submission",async()=>{
  const writes=[];let offline=false;
  const api=async(path,body)=>{
    if(path==="/v1/access/session")return {status:200,data:{operator:"tester",installation_id:"instance"}};
    if(path==="/v1/setup/model"){writes.push(body);offline=true;throw Error("connection lost before admission");}
    if(offline)throw Error("offline");
    return {status:200,data:initial};
  };
  const ui=await launch(api);ui.el("api-key").value="key";await ui.el("model-form").emit("submit");
  offline=false;await ui.tick();
  assert.equal(ui.el("save-model").disabled,false);assert.equal(writes.length,1);
  ui.el("api-key").value="key";await ui.el("model-form").emit("submit");
  assert.equal(writes.length,2);assert.notEqual(writes[0].request_id,writes[1].request_id);
});

test("a setup read started before submission cannot settle the newer uncertain write",async()=>{
  let delayRead=false,offline=false,release;const writes=[];
  const api=async(path,body)=>{
    if(path==="/v1/access/session")return {status:200,data:{operator:"tester",installation_id:"instance"}};
    if(path==="/v1/setup/model"){writes.push(body);offline=true;throw Error("unknown receipt");}
    if(offline)throw Error("offline");
    if(delayRead)return new Promise(resolve=>{release=()=>resolve({status:200,data:initial});});
    return {status:200,data:initial};
  };
  const ui=await launch(api);delayRead=true;await ui.foreground();assert.equal(typeof release,"function");
  ui.el("api-key").value="key";await ui.el("model-form").emit("submit");
  release();await settle();
  assert.equal(ui.el("save-model").disabled,true);
  assert.match(JSON.stringify([...ui.saved]),new RegExp(writes[0].request_id));
  await ui.el("model-form").emit("submit");assert.equal(writes.length,1);
});


test("login preserves only fixed platform return fragments and performs no authorization write",async()=>{
  for(const hash of ["#platform-connection="+encodeURIComponent(JSON.stringify({operation_id:"original"})),"#platform-settings","#https://attacker.invalid"]){
    let logged=false;const writes=[];
    const ui=await launch(async(path,body)=>{
      if(body)writes.push({path,body});
      if(path==="/v1/access/login"){logged=true;return {status:200,data:{}};}
      if(path==="/v1/access/session")return {status:logged?200:401,data:{operator:"tester",installation_id:"instance"}};
      if(path==="/v1/setup")return {status:200,data:{setup_required:false}};
      throw Error(path);
    },new Map(),hash);
    ui.el("token").value="management-token";await ui.el("login-form").emit("submit");await settle();
    assert.deepEqual(ui.navigation,["/"+(hash.startsWith("#platform-")?hash:"")]);
    assert.deepEqual(writes.map(v=>v.path),["/v1/access/login"]);
  }
});
