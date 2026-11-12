// Real Chromium with synthetic APIs. This checks browser layout and lifetime;
// it does not stand in for a physical phone, platform OAuth or a real backend.
import http from "node:http";
import fs from "node:fs/promises";
import path from "node:path";
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { fixture } from "../internal/web/tests/web_member_platform_fixture.mjs";

const { chromium } = createRequire(path.resolve("packaging/verifier/package.json"))("playwright");
const out = path.resolve(process.argv[2] || ".local/server-web-experience-check");
await fs.mkdir(out, {recursive:true});
const api = fixture(), errors = [], checks = [], messages = [], sends = new Map();
let loseResponse = false, epoch = "manager", reads = 0, logged = true, setup = false, setupVersion = 1, setupOperation;
const setupSubmissions=[], modelOperations=[];
const modelCatalog={version:1,default:"daily",entries:[{id:"daily",name:"原默认模型",model_id:"upstream-a",protocol:"openai-completions",revision:1},{id:"model-b",name:"备用模型",model_id:"upstream-b",protocol:"openai-completions",revision:1}]};
const modelMembers=["R","DM"].map(room_id=>({room_id,room_instance:`instance-${room_id}`,agent_id:"a",membership_id:"member-a",version:1,selection:{mode:"default"},target_entry_id:"daily",actual:{model_id:"upstream-a",pending:false}}));
const settings = {time_zone:"Asia/Shanghai",settings_version:1};
const server = http.createServer(async (req, res) => {
  try {
    const url = new URL(req.url, "http://fixture");
    if (url.pathname.startsWith("/v1/")) {
      let raw = ""; for await (const chunk of req) raw += chunk;
      const body = raw ? JSON.parse(raw) : undefined;
      let data;
      if (url.pathname === "/v1/access/session") {res.writeHead(logged?200:401,{"content-type":"application/json"});res.end(JSON.stringify({operator:"tester",installation_id:"browser-fixture"}));return;}
      if (url.pathname === "/v1/access/login") {logged=true;data={operator:"tester"};}
      else if (url.pathname === "/v1/setup") data={setup_required:setup,version:setupVersion,writable:true,model:{protocol:"openai-completions",model_id:"initial"},operation:setupOperation};
      else if (url.pathname === "/v1/setup/model") {setupSubmissions.push(body);setupOperation={id:body.request_id,status:"validating",message:"正在验证模型"};data=setupOperation;}
      else
      if (url.pathname === "/v1/ui") { reads++; data = {managed:true,installation_id:"browser-fixture",service_epoch:epoch,...settings}; }
      else if (url.pathname === "/v1/rooms") {
        data = await api.request(url.pathname, body); data.service_epoch=epoch;
        data.rooms.push({...data.rooms[0],room_id:"DM",room_instance:"instance-DM",name:"个人助理",is_direct:true,
          service:{room_id:"DM",room_instance:"instance-DM",service_epoch:"dm-runtime"}});
        for (const room of data.rooms) {room.runtime_status="ready";room.members=[{id:"a",name:"开发助理"}];if(room.room_id==="R")room.name="软件开发";}
        Object.assign(data,settings);
      } else if (url.pathname === "/v1/member/list" && body.room_id === "DM") data={members:[api.members[0]]};
      else if (url.pathname === "/v1/models") data={service_epoch:epoch,catalog:modelCatalog,members:modelMembers,operations:modelOperations};
      else if (url.pathname === "/v1/models/change") {
        const operation={id:body.request_id,status:"applying",members:[{agent_id:body.agent_id,status:"waiting"}],completed:0,waiting:1,failed:0};modelOperations.unshift(operation);
        const member=modelMembers.find(m=>m.room_id===body.room_id);if(member){member.operation_id=operation.id;member.selection=body.selection;}
        data=operation;
      }
      else if (url.pathname === "/v1/models/operation") data=modelOperations.find(op=>op.id===url.searchParams.get("id"));
      else if (url.pathname === "/v1/platforms/gitlab/prepare") data={status:"ready",application_id:"app",redirect_uri:"http://127.0.0.1:8088/v1/platforms/gitlab/callback",applications_url:"https://gitlab.invalid/-/user_settings/applications",affected:[]};
      else if (url.pathname === "/v1/read") data={messages:messages.filter(m=>m.seq>(body.after||0)),snapshot_seq:messages.length,has_more:false};
      else if (url.pathname === "/v1/send") {
        if (!sends.has(body.client_message_id)) {
          messages.push({seq:messages.length+1,text:body.text,created_at:new Date().toISOString(),author:{kind:"human",id:"tester"}});
          sends.set(body.client_message_id,{seq:messages.length});
        }
        data=sends.get(body.client_message_id);
        if (loseResponse) { loseResponse=false; res.writeHead(504,{"content-type":"application/json"}); res.end(JSON.stringify({error:"receipt unavailable"})); return; }
      } else if (url.pathname === "/v1/platforms/interactions/list") data={interactions:[],selections:[]};
      else data=await api.request(url.pathname+url.search,body);
      res.writeHead(200,{"content-type":"application/json","cache-control":"no-store"}); res.end(JSON.stringify(data)); return;
    }
    const name=url.pathname === "/" ? "index.html" : url.pathname === "/access" ? "access.html" : url.pathname.startsWith("/ui/") ? url.pathname.slice(4) : "";
    if (!name || name.includes("..")) { res.writeHead(404); res.end(); return; }
    res.writeHead(200,{"content-type":name.endsWith(".svg") ? "image/svg+xml" : name.endsWith(".css") ? "text/css" : /\.m?js$/.test(name) ? "text/javascript" : "text/html"});
    res.end(await fs.readFile(path.resolve("internal/web/assets",name)));
  } catch (error) { errors.push(error.message); res.writeHead(500); res.end("{}"); }
});
let browser;
const check = (label, value) => { assert.ok(value,label); checks.push(label); };
try {
  await new Promise((resolve,reject)=>{server.once("error",reject);server.listen(0,"127.0.0.1",resolve);});
  browser=await chromium.launch({headless:true});
  const origin=`http://127.0.0.1:${server.address().port}`;
  for (const [name,width,height,mobile] of [["desktop",1440,900,false],["phone",390,844,true],["small-phone",320,640,true],["landscape",844,390,true]]) {
    const context=await browser.newContext({viewport:{width,height},isMobile:mobile,hasTouch:mobile});
    const page=await context.newPage(); page.on("pageerror",error=>errors.push(error.message));
    await page.goto(origin); await page.waitForFunction(()=>document.querySelector("#connection").textContent.includes("已连接"));
    const fits = () => page.evaluate(() => {
      const send=document.querySelector("#send").getBoundingClientRect();
      return document.documentElement.scrollWidth<=innerWidth && send.bottom<=innerHeight+1 && send.top>=0;
    });
    check(`${name}: composer and page fit`,await fits());
    await page.locator("#message-input").fill("中文草稿");
    await page.locator("#message-input").dispatchEvent("compositionstart");
    const before=sends.size; await page.locator("#message-input").press("Enter");
    check(`${name}: composition does not submit`,sends.size===before);
    await page.locator("#message-input").dispatchEvent("compositionend");
    await page.locator("#message-input").fill("中文草稿");
    await page.reload(); await page.locator("#send:not([disabled])").waitFor();
    check(`${name}: reload retains draft`,await page.locator("#message-input").inputValue()==="中文草稿");
    await page.locator("#send").click(); await page.waitForFunction(()=>document.querySelector("#message-input").value==="");
    check(`${name}: explicit send accepted`,sends.size===before+1);
    if (mobile) {
      await page.locator("#toggle-rooms").click();
      await page.getByRole("button",{name:/^私信：个人助理/}).click();
      await page.waitForFunction(()=>document.querySelector("#room-name").textContent==="个人助理");
      await page.locator("#room-menu-label").click(); await page.locator("#direct-details").click(); await page.locator("#agent-tab-inbox").click();
      await page.locator("#inbox-entries .activity-section").first().waitFor();
      check(`${name}: assistant Inbox reachable`,await page.locator("#inbox-entries").isVisible());
      await page.locator("#close-mobile-members").click();
      check(`${name}: returns to composer`,await fits());
    }
    await page.locator("#user-menu-toggle").click();
    await page.locator("#open-settings").click();
    await page.locator("#settings-tab-platforms").click(); await page.locator("#connection-new").click();
    await page.locator("#connection-name").fill("手机连接表单");
    await page.locator("#connection-url").fill("https://gitlab.invalid");
    check(`${name}: platform form stays within screen`,await page.locator("#connection-url").evaluate(el=>{
      const r=el.getBoundingClientRect();return r.left>=0 && r.right<=innerWidth;
    }));
    await page.goBack(); await page.locator("#settings-dialog").waitFor({state:"hidden"});
    check(`${name}: browser Back returns to conversation`,await page.locator("#compose").isVisible());
    await page.screenshot({path:path.join(out,`${name}.png`)});
    await page.locator("#user-menu-toggle").click(); await page.locator("#open-settings").click(); await page.locator("#settings-tab-models").click();
    await page.locator(".model-row-main").first().waitFor();
    check(`${name}: models use the platform heading and add control`,await page.evaluate(()=>{
      const model=document.querySelector("#model-list-view .platforms-heading h3"),platform=document.querySelector("#platforms-list-view .platforms-heading h3");
      return getComputedStyle(model).fontSize===getComputedStyle(platform).fontSize && document.querySelector("#model-new").classList.contains("add-button");
    }));
    check(`${name}: model list omits revisions and routine validation copy`,!(await page.locator("#model-list-view").innerText()).match(/revision|已验证|私有上下文/));
    await page.screenshot({animations:"disabled",path:path.join(out,`${name}-model-list.png`)});
    const modelRow=page.locator(".model-row").first();
    check(`${name}: model names use the model ID once`,await modelRow.locator("strong").textContent()==="upstream-a" && await modelRow.locator(".model-id").count()===0);
    check(`${name}: list has one edit target without action menus`,await modelRow.getByRole("button").count()===1 && await modelRow.locator(".model-menu").count()===0);
    check(`${name}: edit target spans the row without old layout padding`,await modelRow.evaluate(el=>{
      const main=el.querySelector(".model-row-main");
      return getComputedStyle(el).paddingTop==="0px" && getComputedStyle(main).marginTop==="0px" && Math.abs(el.getBoundingClientRect().width-main.getBoundingClientRect().width)<1;
    }));
    await modelRow.hover();await modelRow.evaluate(el=>el.getAnimations().forEach(a=>a.finish()));
    check(`${name}: hovering highlights the whole row`,await modelRow.evaluate(el=>getComputedStyle(el).backgroundColor!=="rgba(0, 0, 0, 0)"));
    await modelRow.locator(".model-users > summary").click();
    check(`${name}: expanding usage stays in list and shows readable names`,!(await page.locator("#model-edit-form").isVisible()) && (await modelRow.locator(".model-users").innerText()).includes("软件开发 / 开发助理"));
    await page.screenshot({animations:"disabled",path:path.join(out,`${name}-model-users.png`)});

    await page.locator(".model-row-main").first().click();
    check(`${name}: editing replaces the list`,await page.locator("#model-edit-form").isVisible() && !(await page.locator("#model-list-view").isVisible()));
    check(`${name}: current default and deletion live in the editor`,await page.locator("#model-default-state").isVisible() && !(await page.locator("#model-set-default").isVisible()) && await page.locator("#model-delete").isVisible());
    await page.screenshot({animations:"disabled",path:path.join(out,`${name}-model-edit-existing.png`)});

    await page.locator("#model-edit-back").click();
    await page.locator(".model-row-main").nth(1).click();await page.locator("#model-set-default").click();
    check(`${name}: default action confirms inside editor`,await page.locator("#model-default-confirm").isVisible() && !(await page.locator("#model-list-view").isVisible()));
    await page.locator("#model-default-confirm").getByRole("button",{name:"取消",exact:true}).click();
    await page.locator("#model-edit-back").click();
    if(name==="desktop") {
      modelOperations.unshift({id:"completed-history",status:"completed",committed:true,members:[],completed:2,waiting:0,failed:0,message:"历史成功记录"});
      await page.waitForTimeout(2100);
      check("completed model history does not occupy the list",await page.locator("#model-operations").innerText()==="");
      modelOperations.unshift({id:"failed-change",status:"failed",committed:true,members:[{agent_id:"a",status:"failed",error:"应用失败"}],completed:0,waiting:0,failed:1,message:"部分 Agent 未完成应用"});
      await page.getByRole("button",{name:"重试未完成成员"}).waitFor();
      check("model failures retain an actionable retry",await page.getByRole("button",{name:"重试未完成成员"}).isVisible());
      await page.screenshot({animations:"disabled",path:path.join(out,"desktop-model-failure.png")});
      modelOperations.length=0;
      await page.waitForFunction(()=>document.querySelector("#model-operations").textContent==="");
    }
    await page.locator("#model-new").click();
    await page.screenshot({animations:"disabled",path:path.join(out,`${name}-model-editor.png`)});
    await page.locator("#model-entry-name").fill("非秘密模型草稿");
    await page.locator("#catalog-model-id").fill("edited-model");await page.locator("#catalog-api-key").fill("never-store-model-key");
    await page.locator("#model-edit-form summary").click();await page.locator("#catalog-developer-role").selectOption("false");
    await page.locator("#catalog-context-window").fill("128000");
    await page.reload();await page.locator("#user-menu-toggle").click();await page.locator("#open-settings").click();await page.locator("#settings-tab-models").click();
    await page.locator("#model-edit-form").waitFor();
    check(`${name}: model draft and tri-state restored without Key`,await page.locator("#catalog-model-id").inputValue()==="edited-model" && await page.locator("#catalog-developer-role").inputValue()==="false" && await page.locator("#catalog-api-key").inputValue()==="");
    check(`${name}: model Key absent from browser storage`,await page.evaluate(()=>!JSON.stringify({...sessionStorage,...localStorage}).includes("never-store-model-key")));
    await page.locator("#model-edit-form summary").click();
    check(`${name}: advanced model field fits`,await page.locator("#catalog-context-window").evaluate(el=>{const r=el.getBoundingClientRect();return r.left>=0 && r.right<=innerWidth;}));
    await page.locator("#catalog-developer-role").scrollIntoViewIfNeeded();await page.screenshot({path:path.join(out,`${name}-models.png`)});
    await page.locator("#model-edit-cancel").click();
    await page.locator(".model-row-main").first().click();
    await page.locator("#model-delete").click();
    await page.locator("#model-delete-replacement").selectOption("model-b");
    check(`${name}: deletion replacement remains reachable`,await page.locator("#model-delete-replacement").evaluate(el=>{const r=el.getBoundingClientRect();return r.left>=0 && r.right<=innerWidth;}));
    await page.locator("#model-delete-confirm").getByRole("button",{name:"取消",exact:true}).click();
    check(`${name}: cancelling deletion returns to editor`,await page.locator("#model-edit-form").isVisible() && !(await page.locator("#model-list-view").isVisible()));
    await page.locator("#close-settings").click();
    if(name === "phone") {
      await page.locator("#room-menu-label").click(); await page.locator("#direct-details").click();await page.locator("#member-model-selection").waitFor();
      await page.locator("#member-model-selection").selectOption("model-b");
      await page.waitForFunction(()=>document.querySelector("#member-model-feedback").textContent.includes("等待当前回合结束"));
      check("member selection shows waiting and actual Session model",(await page.locator("#member-model-progress").getAttribute("title")).includes("upstream-a"));
      await page.locator("#close-mobile-members").click();
      modelOperations.length=0;for(const member of modelMembers)delete member.operation_id;
    }
    if (name === "phone") {
      await page.locator("#message-input").fill("旋转后继续输入");
      await page.setViewportSize({width:844,height:390});
      await page.waitForFunction(() => {
        const send=document.querySelector("#send").getBoundingClientRect();
        return send.bottom<=innerHeight+1 && send.top>=0;
      });
      check("rotation retains input and reachable send",await fits() && await page.locator("#message-input").inputValue()==="旋转后继续输入");
      await page.setViewportSize({width:390,height:844});
      await page.locator("#message-input").fill("未知响应只核对一次"); loseResponse=true;
      await page.locator("#send").click(); await page.locator("#discard-send").waitFor();
      const accepted=sends.size;
      await context.setOffline(true); await page.waitForFunction(()=>document.querySelector("#connection").textContent.includes("网络已断开"));
      await context.setOffline(false); await page.waitForFunction(()=>document.querySelector("#connection").textContent.includes("已连接"));
      check("online does not replay unknown send",sends.size===accepted);
      await page.reload(); await page.locator("#discard-send").waitFor();
      check("reload keeps unknown send pending",sends.size===accepted);
      await page.locator("#send").click(); await page.waitForFunction(()=>document.querySelector("#message-input").value==="");
      check("explicit retry keeps message ID",sends.size===accepted);
      await page.evaluate(()=>window.dispatchEvent(new PageTransitionEvent("pagehide",{persisted:true})));
      const paused=reads; await page.waitForTimeout(3200); check("pagehide pauses display reads",reads===paused);
      epoch="manager-restarted";
      await page.evaluate(()=>window.dispatchEvent(new PageTransitionEvent("pageshow",{persisted:true})));
      await page.locator("#service-change").waitFor(); check("return rereads service generation",reads>paused);
    }
    await context.close();
  }
  setup=true;logged=false;
  const setupContext=await browser.newContext({viewport:{width:390,height:844},isMobile:true,hasTouch:true});
  const firstUse=await setupContext.newPage();firstUse.on("pageerror",error=>errors.push(error.message));
  await firstUse.goto(`${origin}/access`);await firstUse.locator("#token").fill("fixture-access");await firstUse.locator("#login-submit").click();
  await firstUse.locator("#model-form").waitFor();await firstUse.locator("#model-id").fill("first-use-draft");
  await firstUse.locator("#api-key").fill("never-store-setup-key");await firstUse.locator("summary").click();await firstUse.locator("#developer-role").selectOption("false");
  setupVersion=2;await firstUse.reload();await firstUse.locator("#model-form").waitFor();
  check("first-use reload retains tri-state, original version, and excludes Key",await firstUse.locator("#model-id").inputValue()==="first-use-draft" && await firstUse.locator("#api-key").inputValue()==="");
  check("first-use conflict requires explicit fresh form",await firstUse.locator("#reload-model-draft").isVisible());
  await firstUse.locator("#reload-model-draft").click();await firstUse.locator("#api-key").fill("never-store-setup-key");await firstUse.locator("#save-model").click();
  await firstUse.waitForFunction(()=>document.querySelector("#save-model").disabled);
  await firstUse.reload();await firstUse.locator("#model-form").waitFor();
  check("first-use resumes original operation without a new POST",setupSubmissions.length===1 && setupSubmissions[0].expected_version===2);
  check("first-use Key absent from browser storage",await firstUse.evaluate(()=>!JSON.stringify({...sessionStorage,...localStorage}).includes("never-store-setup-key")));
  logged=false;await firstUse.reload();await firstUse.locator("#login").waitFor();
  await firstUse.locator("#token").fill("fixture-access");await firstUse.locator("#login-submit").click();await firstUse.locator("#model-form").waitFor();
  check("relogin queries existing setup operation without resubmitting",setupSubmissions.length===1 && await firstUse.locator("#save-model").isDisabled());
  await setupContext.close();
  checks.push("no browser or fixture errors");
  await fs.writeFile(path.join(out,"report.json"),JSON.stringify({browser:browser.version(),synthetic_apis:true,physical_device:false,checks,errors},null,2));
  assert.deepEqual(errors,[],"browser or fixture errors");
  console.log(JSON.stringify({checks:checks.length,report:path.join(out,"report.json")}));
} finally { await browser?.close(); await new Promise(resolve=>server.close(resolve)); }
