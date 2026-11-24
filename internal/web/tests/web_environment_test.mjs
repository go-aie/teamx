import {test} from "node:test";
import assert from "node:assert/strict";
import {launch, settle} from "./web_app_harness.mjs";
import {fixture} from "./web_member_platform_fixture.mjs";

const environment = (id, label) => ({id:id.repeat(64), label, viewer_url:`/v1/environment/${id.repeat(64)}/viewer`});
function environmentFixture() {
  const api = fixture();
  api.environments = [environment("a", "助理 A"), environment("b", "助理 B")];
  api.control = null;
  api.hook = (path,body) => {
    if (path.startsWith("/v1/environments?")) return {environments:structuredClone(api.environments)};
    if (path.includes("/control?")) return {control:structuredClone(api.control),paused:!!api.control};
    if (path.endsWith("/control")) {
      if (body.action === "takeover") api.control = {id:body.control_id,phase:"waiting",owned:true,resumed:false};
      if (body.action === "finish") api.control = {...api.control,phase:"completed",resumed:true};
      if (body.action === "abandon") api.control = {...api.control,phase:"abandoned",resumed:false};
      return {control:structuredClone(api.control),paused:body.action!=="finish"};
    }
  };
  return api;
}
async function open(ui) { await ui.el("conversation-environment").emit("click"); await settle(); }
async function connect(ui, interactive = false) {
  const frame = ui.el("environment-screen").children[0];
  ui.window.location = {origin:"http://teamx.invalid"};
  if (!frame.contentWindow) frame.contentWindow = {messages:[],postMessage(data){this.messages.push(data);}};
  await ui.window.emit("message", {origin:"http://teamx.invalid",source:frame.contentWindow,data:{type:"teamx-environment",state:"connected",interactive}});
  return frame;
}

test("environment shares the auxiliary rail, selects sources and releases viewers on switching", async () => {
  const api = environmentFixture(), ui = await launch(api);
  await ui.draft("观看时继续编辑"); await open(ui);
  assert.equal(ui.el("environment-page").hidden, false);
  assert.equal(ui.el("environment-source-field").hidden, false);
  assert.equal(ui.el("conversation").inert, false);
  await ui.el("member-resizer").emit("keydown", {key:"ArrowRight"});
  const environmentWidth = JSON.parse(ui.saved.get("teamx.room-panels")).environmentWidth;
  assert.ok(environmentWidth > 0);
  let frame = ui.el("environment-screen").children[0];
  assert.equal(frame.src, api.environments[0].viewer_url);
  ui.el("environment-source").value = api.environments[1].id;
  await ui.el("environment-source").emit("change");
  assert.equal(frame.parentElement, null);
  frame = ui.el("environment-screen").children[0];
  assert.equal(frame.src, api.environments[1].viewer_url);
  await ui.el("environment-expand").emit("click");
  assert.equal(ui.el("conversation").inert, true);
  await ui.el("environment-page").emit("keydown", {key:"Escape"});
  assert.equal(ui.el("environment-page").hidden, false);
  assert.equal(ui.el("conversation").inert, false);
  await ui.el("conversation-files").emit("click"); await settle();
  assert.equal(ui.el("environment-page").hidden, true);
  assert.equal(frame.parentElement, null);
  await open(ui);
  assert.equal(ui.el("files-page").hidden, true);
  assert.equal(ui.el("workspace").style.getPropertyValue("--members-open-width"), `${environmentWidth}px`);
  assert.equal(ui.el("environment-source").value, api.environments[1].id);
  await ui.el("environment-close").emit("click");
  assert.equal(ui.el("environment-screen").children.length, 0);
  assert.equal(ui.el("message-input").value, "观看时继续编辑");
});

test("empty, failed and stale environment reads never mount an old viewer", async () => {
  const api = environmentFixture(), ui = await launch(api);
  api.environments = []; await open(ui);
  assert.match(ui.el("environment-status").textContent, /暂未配置/);
  let release;
  api.hook = path => path.startsWith("/v1/environments?") ? new Promise(resolve => { release = resolve; }) : undefined;
  await ui.el("environment-refresh").emit("click"); await settle();
  await ui.el("environment-close").emit("click");
  release({environments:[environment("a", "旧环境")]}); await settle();
  assert.equal(ui.el("environment-screen").children.length, 0);
  api.hook = path => { if (path.startsWith("/v1/environments?")) throw Error("offline"); };
  await open(ui); assert.match(ui.el("environment-status").textContent, /暂不可用/);
  api.hook = path => path.startsWith("/v1/environments?") ? {environments:[environment("b", "新环境")]} : undefined;
  await ui.el("environment-refresh").emit("click"); await settle();
  assert.equal(ui.el("environment-screen").children.length, 1);
  api.epoch = "new-manager"; await ui.refresh();
  assert.equal(ui.el("environment-page").hidden, true);
  assert.equal(ui.el("environment-screen").children.length, 0);
});

test("environment scales on phone and ignores unrelated frame messages", async () => {
  const api = environmentFixture(), ui = await launch(api);
  await open(ui);
  const initial = ui.el("environment-status").textContent;
  const frame = ui.el("environment-screen").children[0];
  frame.contentWindow = {}; ui.window.location = {origin:"http://teamx.invalid"};
  await ui.window.emit("message", {origin:"http://evil.invalid", source:{}, data:{type:"teamx-environment",state:"connected"}});
  assert.equal(ui.el("environment-status").textContent, initial);
  await ui.window.emit("message", {origin:"http://teamx.invalid", source:frame.contentWindow, data:{type:"teamx-environment",state:"connected"}});
  assert.match(ui.el("environment-status").textContent, /已连接/);
  ui.window.innerWidth = 390; await ui.window.emit("resize");
  assert.equal(ui.el("conversation").inert, true);
  await ui.el("environment-close").emit("click");
  assert.equal(ui.el("conversation").inert, false);
  assert.equal(ui.document.activeElement, ui.el("room-menu-label"));
});

test("handoff waits for the member, enables only owned input and completes through one operation", async () => {
  const api = environmentFixture(), ui = await launch(api);
  await open(ui); const frame = await connect(ui);
  assert.equal(ui.el("environment-control").disabled,false);
  await ui.el("environment-control").emit("click"); await settle();
  assert.equal(ui.el("environment-control").textContent,"取消接管");
  assert.match(ui.el("environment-status").textContent,/等待当前回合/);
  assert.equal(frame.contentWindow.messages.at(-1).interactive,false);
  api.control.phase = "ready"; await ui.clock.advance(2000);
  assert.equal(frame.contentWindow.messages.at(-1).interactive,true);
  assert.equal(ui.el("environment-control").textContent,"完成接管");
  await connect(ui,true);
  assert.match(ui.el("environment-status").textContent,/可直接输入/);
  await ui.el("environment-control").emit("click"); await settle();
  assert.equal(frame.contentWindow.messages.at(-1).interactive,false);
  assert.match(ui.el("environment-status").textContent,/已交还/);
  const operations = api.calls.filter(call=>call.path.endsWith("/control"));
  assert.deepEqual(operations.map(call=>call.body.action),["takeover","finish"]);
  assert.equal(operations[0].body.control_id,operations[1].body.control_id);
});

test("another page stays read-only and closing owned control abandons without resuming", async () => {
  const api = environmentFixture(), ui = await launch(api);
  api.control = {id:"a".repeat(32),owned:false,phase:"ready"};
  await open(ui); let frame = await connect(ui);
  assert.equal(ui.el("environment-control").disabled,true);
  assert.equal(frame.contentWindow.messages.at(-1).interactive,false);
  api.control.owned = true; await ui.clock.advance(2000);
  await ui.el("environment-close").emit("click"); await settle();
  assert.equal(api.control.phase,"abandoned");
  assert.equal(api.control.resumed,false);
  assert.equal(frame.parentElement,null);
});

test("uncertain finish retries the same handoff and a failed heartbeat drops input", async () => {
  const api = environmentFixture(), hook = api.hook, ui = await launch(api);
  await open(ui); const frame = await connect(ui);
  await ui.el("environment-control").emit("click"); await settle();
  api.control.phase = "ready"; await ui.clock.advance(2000); await connect(ui,true);
  let dropped = false;
  api.hook = (path,body) => {
    if (path.endsWith("/control") && body.action === "finish" && !dropped) {
      dropped=true;api.control.phase="finishing";throw Error("lost response");
    }
    return hook(path,body);
  };
  await ui.el("environment-control").emit("click"); await settle();
  assert.equal(ui.el("environment-control").textContent,"重试完成");
  assert.equal(frame.contentWindow.messages.at(-1).interactive,false);
  await ui.el("environment-control").emit("click"); await settle();
  const finishes=api.calls.filter(call=>call.path.endsWith("/control") && call.body.action==="finish");
  assert.equal(finishes.length,2);assert.equal(finishes[0].body.control_id,finishes[1].body.control_id);
  await ui.el("environment-control").emit("click"); await settle();
  api.control.phase="ready";await ui.clock.advance(2000);await connect(ui,true);
  api.hook = (path,body) => {if(path.includes("/control?"))throw Error("offline");return hook(path,body);};
  await ui.clock.advance(2000);
  assert.equal(frame.contentWindow.messages.at(-1).interactive,false);
  assert.match(ui.el("environment-status").textContent,/停止输入/);
});

test("unknown outcome review and failed restoration keep an explicit pause message", async () => {
  const api = environmentFixture(), ui = await launch(api);
  api.control = {id:"a".repeat(32),owned:true,phase:"ready",review:true,resumed:false};
  await open(ui); const frame = await connect(ui,true);
  assert.match(ui.el("environment-status").textContent,/仅供人工核验/);
  assert.match(ui.el("environment-status").textContent,/交还后 Agent 保持暂停/);
  api.control = {...api.control,phase:"completed",resume_failed:true};
  await ui.clock.advance(2000);
  assert.match(ui.el("environment-status").textContent,/恢复 Agent 失败/);
  assert.equal(frame.contentWindow.messages.at(-1).interactive,false);
});
