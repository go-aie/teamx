import { test } from "node:test";
import assert from "node:assert/strict";
import { mountSpacePicker } from "../assets/space-context.mjs";
import { launch, withOnboarding } from "./web_app_harness.mjs";

async function capabilities(value) {
  const previousFetch = globalThis.fetch, previousDocument = globalThis.document;
  try {
    globalThis.fetch = async () => ({ok:true,json:async()=>({default_space_id:"a".repeat(32),spaces:[{id:"a".repeat(32),capabilities:value}]})});
    globalThis.document = {querySelectorAll:()=>[],getElementById:()=>null};
    await mountSpacePicker();
  } finally {
    globalThis.fetch = previousFetch;
    if (previousDocument === undefined) delete globalThis.document; else globalThis.document = previousDocument;
  }
}

function fixture() {
  const api = {calls:[],async request(path,body) {
    api.calls.push({path,body:structuredClone(body)});
    if (path === "/v1/ui") return {managed:true};
    if (path === "/v1/rooms") return {service_epoch:"manager",operator:"tester",rooms:[],teams:[]};
    if (path.startsWith("/v1/platforms?")) return {platforms:[]};
    if (path === "/v1/platforms/lark/discover") return {executable:"lark-cli",profiles:[]};
    if (path.startsWith("/v1/platforms/gitlab/applications?")) return {managed_application:true,applications:[{application_id:"service-app",origin:"https://gitlab.invalid"}]};
    if (path === "/v1/platforms/gitlab/prepare") return {managed_application:true,status:"ready",application_id:"service-app",scopes:["api"],redirect_uri:"https://teamx.invalid/v1/platforms/gitlab/callback",affected:[]};
    throw Error(`Unexpected ${path}`);
  }};
  return withOnboarding(api);
}

async function add(api) {
  const ui = await launch(api);
  await ui.el("open-settings").emit("click");
  await ui.el("connection-new").emit("click");
  return ui;
}

test("adding a platform selects an available type without requesting disabled GitLab", async t => {
  await capabilities({gitlab:false,scion:false,lark:true,gitlab_application_management:false});
  t.after(()=>capabilities({}));
  const api = fixture(), ui = await add(api);
  assert.deepEqual(ui.el("connection-provider").options.map(o=>o.value),["feishu","lark"]);
  assert.equal(ui.el("connection-provider").value,"feishu");
  assert.equal(ui.el("gitlab-account-row").hidden,true);
  assert.equal(ui.el("gitlab-service-application-row").hidden,true);
  assert.equal(ui.el("connection-url-row").hidden,true);
  assert.equal(api.calls.some(c=>c.path.includes("/gitlab/")),false);
  assert.equal(api.calls.filter(c=>c.path === "/v1/platforms/lark/discover").length,1);
  assert.equal(ui.el("platforms-feedback").textContent,"");
});

test("adding a platform explains when no platform type is available", async t => {
  await capabilities({gitlab:false,scion:false,lark:false,gitlab_application_management:false});
  t.after(()=>capabilities({}));
  const api = fixture(), ui = await add(api);
  assert.equal(ui.el("connection-fields").hidden,true);
  assert.match(ui.el("platforms-feedback").textContent,/暂未开放.*平台/);
  assert.equal(api.calls.some(c=>/\/platforms\/(gitlab|lark|scion)\//.test(c.path)),false);
});

test("selecting managed GitLab discovers its application without asking for client credentials", async t => {
  await capabilities({gitlab:true,scion:false,lark:true,gitlab_application_management:false,managed_repositories:false});
  t.after(()=>capabilities({}));
  const api = fixture(), ui = await add(api);
  assert.equal(ui.el("connection-provider").value,"gitlab");
  assert.equal(ui.el("connection-url").value,"https://gitlab.invalid");
  assert.equal(ui.el("gitlab-service-application-row").hidden,true);
  assert.equal(ui.el("connection-url-row").hidden,false);
  assert.equal(ui.el("connection-url").readOnly,true);
  assert.equal(ui.el("gitlab-client-fields").hidden,true);
  assert.equal(ui.el("gitlab-app-config-row").hidden,true);
  assert.equal(ui.el("gitlab-login").hidden,false);
  assert.equal(ui.el("gitlab-scope-write-repository").checked,false);
  ui.el("connection-provider").value="feishu";
  await ui.el("connection-provider").emit("change");
  const before = api.calls.filter(c=>c.path.includes("/gitlab/applications?")).length;
  ui.el("connection-provider").value="gitlab";
  await ui.el("connection-provider").emit("change");
  assert.equal(api.calls.filter(c=>c.path.includes("/gitlab/applications?")).length,before+1);
  assert.equal(ui.el("connection-url").value,"https://gitlab.invalid");
  assert.equal(ui.el("gitlab-login").hidden,false);
  assert.equal(ui.el("gitlab-client-fields").hidden,true);
  const prepared = api.calls.filter(c=>c.path === "/v1/platforms/gitlab/prepare");
  assert.ok(prepared.every(c=>c.body.application_id === "service-app" && !c.body.client));
});
