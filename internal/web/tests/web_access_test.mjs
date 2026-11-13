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
    value:"",hidden:false,disabled:false,listeners:{},attributes:{},
    addEventListener(name,fn){(this.listeners[name] ||= []).push(fn);},
    setAttribute(name,value){this.attributes[name]=value;},removeAttribute(name){delete this.attributes[name];},focus(){this.focused=true;},scrollIntoView(){this.scrolled=true;},
    async emit(name){for(const fn of this.listeners[name] || []) await fn({preventDefault(){}});}
  }]));
  const el = id=>elements.get(id);
  for (const [,id,options] of html.matchAll(/<select\b[^>]*id="([^"]+)"[^>]*>([\s\S]*?)<\/select>/g)) el(id).options=[...options.matchAll(/<option value="([^"]*)"/g)].map(([,value])=>({value,disabled:false}));
  el("reload-model-draft").hidden=true;
  el("setup-progress").hidden=true;
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

test("accepted first-use validation replaces the form with one summary and follows confirmed stages to the app",async()=>{
  let accept,operation,complete=false;const writes=[];
  const ui=await launch(async(path,body)=>{
    if(path==="/v1/access/session")return {status:200,data:{operator:"tester",installation_id:"instance"}};
    if(path==="/v1/setup/model"){writes.push(body);return new Promise(resolve=>{accept=()=>{operation={operation_id:body.request_id,status:"validating",message:"technical validation details"};resolve({status:202,data:operation});};});}
    return {status:200,data:{...initial,setup_required:!complete,operation}};
  });
  ui.el("model-id").value="my-model";ui.el("base-url").value="https://model.invalid/v1";ui.el("api-key").value="private-key";
  ui.el("model-advanced").open=true;
  const submitting=ui.el("model-form").emit("submit");await settle();
  assert.equal(ui.el("setup-editor").hidden,false);assert.equal(ui.el("save-model").textContent,"正在提交…");assert.equal(ui.el("check-setup-operation").hidden,true);
  accept();await submitting;
  assert.equal(ui.el("setup-editor").hidden,true);assert.equal(ui.el("setup-progress").hidden,false);
  assert.equal(ui.el("setup-summary-model").textContent,"my-model");assert.equal(ui.el("setup-summary-address").textContent,"https://model.invalid/v1");
  assert.equal(ui.el("setup-progress-title").textContent,"正在验证模型…");assert.equal(ui.el("feedback").textContent,"");assert.equal(ui.el("check-setup-operation").hidden,true);
  operation.status="saved";await ui.tick();assert.equal(ui.el("setup-progress-title").textContent,"正在准备助理…");assert.equal(ui.el("check-setup-operation").hidden,true);
  complete=true;await ui.tick();assert.deepEqual(ui.navigation,["/"]);assert.equal(writes.length,1);
});

test("only uncertain progress exposes a state check and a confirmed failure restores the original fields",async()=>{
  let operation,offline=false;const writes=[];
  const ui=await launch(async(path,body)=>{
    if(path==="/v1/access/session")return {status:200,data:{operator:"tester",installation_id:"instance"}};
    if(path==="/v1/setup/model"){writes.push(body);operation={operation_id:body.request_id,status:"validating"};return {status:202,data:operation};}
    if(offline)throw Error("network unavailable");
    return {status:200,data:{...initial,operation}};
  });
  ui.el("model-id").value="my-model";ui.el("context-window").value="128000";ui.el("api-key").value="private-key";
  await ui.el("model-form").emit("submit");offline=true;await ui.tick();
  assert.equal(ui.el("setup-editor").hidden,true);assert.equal(ui.el("setup-progress").attributes["data-state"],"unknown");assert.equal(ui.el("check-setup-operation").hidden,false);
  await ui.el("check-setup-operation").emit("click");assert.equal(writes.length,1);assert.equal(ui.el("save-model").disabled,true);
  offline=false;operation={...operation,status:"failed",field:"reasoning_effort",message:"请调整推理强度后重试。"};await ui.tick();
  assert.equal(ui.el("setup-editor").hidden,false);assert.equal(ui.el("setup-progress").hidden,true);assert.equal(ui.el("check-setup-operation").hidden,true);
  assert.equal(ui.el("model-id").value,"my-model");assert.equal(ui.el("context-window").value,"128000");assert.equal(ui.el("api-key").value,"");
  assert.equal(ui.el("model-advanced").open,true);assert.equal(ui.el("reasoning-effort").focused,true);
});

test("first-use field rejection opens advanced settings, keeps the reason visible and permits a corrected explicit retry",async()=>{
  const writes=[];let operation;
  const message="DeepSeek 模型不支持 minimal 推理强度，请选择默认或 low。";
  const ui=await launch(async(path,body)=>{
    if(path==="/v1/access/session") return {status:200,data:{operator:"tester",installation_id:"instance"}};
    if(path==="/v1/setup/model") {
      writes.push(body);
      if(writes.length===1) return {status:400,data:{code:"request",field:"reasoning_effort",message}};
      operation={operation_id:body.request_id,status:"validating"};return {status:202,data:operation};
    }
    return {status:200,data:{...initial,operation}};
  });
  ui.el("model-id").value="deepseek-v4.1-flash-1017";await ui.el("model-id").emit("input");
  assert.equal(ui.el("reasoning-effort").options.find(o=>o.value==="minimal").disabled,true);
  // A restored older draft keeps its incompatible choice visible for correction.
  ui.el("reasoning-effort").value="minimal";ui.el("api-key").value="private-rejected-key";
  await ui.el("model-form").emit("submit");
  assert.equal(ui.el("model-advanced").open,true);assert.equal(ui.el("reasoning-effort").attributes["aria-invalid"],"true");
  assert.equal(ui.el("reasoning-effort-error").textContent,message);assert.equal(ui.el("reasoning-effort-error").hidden,false);
  assert.equal(ui.el("reasoning-effort").focused,true);assert.equal(ui.el("reasoning-effort").scrolled,true);
  assert.equal(ui.el("feedback").attributes["data-state"],"error");assert.equal(ui.el("save-model").disabled,false);
  await ui.foreground();assert.equal(ui.el("feedback").textContent,message);
  assert.doesNotMatch(JSON.stringify([...ui.saved]),/private-rejected-key/);
  ui.el("reasoning-effort").value="low";await ui.el("reasoning-effort").emit("change");
  assert.equal(ui.el("reasoning-effort-error").hidden,true);assert.equal(ui.el("reasoning-effort").attributes["aria-invalid"],undefined);
  ui.el("api-key").value="private-retry-key";await ui.el("model-form").emit("submit");
  assert.equal(writes.length,2);assert.notEqual(writes[0].request_id,writes[1].request_id);assert.equal(writes[1].reasoning_effort,"low");
});

test("first-use asynchronous authentication failure exposes key replacement and visible field guidance",async()=>{
  const ui=await launch(async path=>({status:200,data:path==="/v1/access/session" ? {operator:"tester",installation_id:"instance"} : {...initial,credential_configured:true,operation:{operation_id:"old-operation",status:"failed",field:"api_key",message:"模型服务拒绝了身份验证，请检查 API Key。"}}}));
  assert.equal(ui.el("key-input").hidden,false);assert.equal(ui.el("api-key").required,true);
  assert.equal(ui.el("api-key").attributes["aria-invalid"],"true");assert.equal(ui.el("api-key-error").hidden,false);
  assert.equal(ui.el("feedback").attributes.role,"alert");assert.match(ui.el("feedback").textContent,/API Key/);
});

test("an explicit field rejection stays visible even when subsequent setup reads are offline",async()=>{
  let offline=false;
  const ui=await launch(async(path)=>{
    if(path==="/v1/access/session")return {status:200,data:{operator:"tester",installation_id:"instance"}};
    if(path==="/v1/setup/model"){offline=true;return {status:400,data:{code:"request",field:"base_url",message:"请填写完整的接口地址。"}};}
    if(offline)throw Error("offline after rejection");
    return {status:200,data:initial};
  });
  ui.el("api-key").value="private-key";await ui.el("model-form").emit("submit");
  assert.equal(ui.el("save-model").disabled,false);assert.equal(ui.el("check-setup-operation").hidden,true);
  assert.equal(ui.el("base-url-error").hidden,false);assert.match(ui.el("feedback").textContent,/接口地址/);
  assert.equal(ui.el("feedback").attributes["data-state"],"error");
});

test("first-use unknown submission survives failed reconciliation and reload without a second write",async()=>{
  const writes=[];let offline=false,operation;
  const api=async(path,body)=>{
    if(path==="/v1/access/session") return {status:200,data:{operator:"tester",installation_id:"instance"}};
    if(path==="/v1/setup/model") {writes.push(body);offline=true;operation={operation_id:body.request_id,status:"validating"};throw Error("lost receipt");}
    if(offline) throw Error("offline");
    return {status:200,data:{...initial,operation}};
  };
  const ui=await launch(api);
  assert.equal(ui.el("feedback").textContent,"");
  ui.el("api-key").value="private-fixture-key";await ui.el("toggle-key").emit("click");
  assert.equal(ui.el("api-key").type,"text");assert.equal(ui.el("api-key").value,"private-fixture-key");
  await ui.el("model-form").emit("submit");
  assert.equal(ui.el("api-key").type,"password");assert.equal(ui.el("api-key").value,"");
  assert.equal(ui.el("save-model").disabled,true);assert.equal(ui.el("reload-model-draft").disabled,true);
  assert.equal(ui.el("setup-editor").hidden,true);assert.equal(ui.el("check-setup-operation").hidden,false);
  await ui.el("model-form").emit("submit");await ui.el("reload-model-draft").emit("click");await ui.tick();
  assert.equal(writes.length,1);assert.doesNotMatch(JSON.stringify([...ui.saved]),/private-fixture-key/);
  assert.match(JSON.stringify([...ui.saved]),new RegExp(writes[0].request_id));
  const disconnected=await launch(api,ui.saved);
  assert.equal(disconnected.el("setup-editor").hidden,true);assert.equal(disconnected.el("setup-progress").attributes["data-state"],"unknown");assert.equal(disconnected.el("save-model").disabled,true);
  assert.equal(writes.length,1);
  offline=false;
  const reopened=await launch(api,ui.saved);
  assert.equal(reopened.el("save-model").disabled,true);assert.equal(writes.length,1);
  operation={...operation,status:"saved"};await reopened.tick();assert.equal(reopened.el("save-model").disabled,true);
  assert.equal(reopened.el("setup-progress-title").textContent,"正在准备助理…");assert.equal(reopened.el("check-setup-operation").hidden,true);
  operation={...operation,status:"failed",message:"服务重启中断，请重新验证"};await reopened.tick();
  assert.equal(reopened.el("save-model").disabled,false);assert.match(reopened.el("feedback").textContent,/服务重启/);
  assert.equal(reopened.el("setup-editor").hidden,false);assert.equal(reopened.el("setup-progress").hidden,true);
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
