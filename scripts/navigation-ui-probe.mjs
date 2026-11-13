// Chromium against synthetic APIs; this does not exercise real providers.
import http from "node:http";
import fs from "node:fs/promises";
import path from "node:path";
import assert from "node:assert/strict";
import {createRequire} from "node:module";
import {fixture} from "../internal/web/tests/web_member_platform_fixture.mjs";

const {chromium}=createRequire(path.resolve("packaging/verifier/package.json"))("playwright");
const out=path.resolve(process.argv[2] || ".local/navigation-ui-check");
await fs.mkdir(out,{recursive:true});
const api=fixture(),errors=[],checks=[];
let directMode=false;
const messages=Array.from({length:30},(_,i)=>({seq:i+1,author:{kind:"agent",id:"a"},text:`消息 ${i+1}：检查返回后继续阅读的位置。`,created_at:new Date().toISOString()}));
messages.push(...[31,32].map(seq=>({seq,author:{kind:"integration",id:"gitlab"},text:"平台通知来源",created_at:new Date().toISOString(),
  source:{provider:"gitlab",channel:"todos",...(seq===32 ? {platform_id:"gl",subscription_id:"sub-gl-room",instance:"https://gitlab.invalid"} : {})}})));
const server=http.createServer(async(req,res)=>{
  try {
    const url=new URL(req.url,"http://fixture");
    if(url.pathname.startsWith("/v1/")) {
      let raw="";for await(const chunk of req)raw+=chunk;
      const body=raw ? JSON.parse(raw) : undefined;
      let data;
      if(url.pathname==="/v1/access/session")data={login_enabled:false,operator:"tester"};
      else if(url.pathname==="/v1/ui")data={managed:true,installation_id:"navigation-probe",service_epoch:"manager",time_zone:"Asia/Shanghai",settings_version:1};
      else if(url.pathname==="/v1/rooms") {
        data=await api.request(url.pathname,body);for(const room of data.rooms){room.runtime_status="ready";room.is_direct=directMode;if(directMode)room.members=[{id:"a",name:"助理"}];}
        Object.assign(data,{time_zone:"Asia/Shanghai",settings_version:1});
      } else if(url.pathname==="/v1/member/list" && directMode)data={members:[api.members[0]]};
      else if(url.pathname==="/v1/read")data={messages:messages.filter(m=>m.seq>(body.after || 0)),snapshot_seq:32,has_more:false};
      else if(url.pathname==="/v1/platforms/interactions/list")data={interactions:[],selections:[]};
      else if(url.pathname==="/v1/models")data={service_epoch:"manager",catalog:{version:1,default:"daily",entries:[{id:"daily",name:"日常",model_id:"model-a",revision:1}]},members:[],operations:[]};
      else if(url.pathname==="/v1/platforms/gitlab/setup")data={applications:[]};
      else data=await api.request(url.pathname+url.search,body);
      res.writeHead(200,{"content-type":"application/json","cache-control":"no-store"});res.end(JSON.stringify(data));return;
    }
    const file=url.pathname==="/" ? "index.html" : url.pathname.startsWith("/ui/") ? url.pathname.slice(4) : "";
    if(!file || file.includes("..")){res.writeHead(404);res.end();return;}
    res.writeHead(200,{"content-type":file.endsWith(".css") ? "text/css" : /\.m?js$/.test(file) ? "text/javascript" : file.endsWith(".svg") ? "image/svg+xml" : "text/html"});
    res.end(await fs.readFile(path.resolve("internal/web/assets",file)));
  }catch(error){errors.push(error.message);res.writeHead(500);res.end("{}");}
});
const check=(name,ok)=>{assert.ok(ok,name);checks.push(name);console.log(name);};
let browser;
try {
  await new Promise(resolve=>server.listen(0,"127.0.0.1",resolve));browser=await chromium.launch({headless:true});
  const origin=`http://127.0.0.1:${server.address().port}`;
  for(const [name,width,height] of [["desktop",1440,900],["phone",390,844],["small-phone",320,640]]) {
    const context=await browser.newContext({viewport:{width,height},isMobile:width<500,hasTouch:width<500});
    const page=await context.newPage();page.setDefaultTimeout(10000);page.on("pageerror",e=>errors.push(e.message));
    await page.goto(origin);await page.locator("#message-32").waitFor();
    if(await page.locator("#toggle-members").getAttribute("aria-expanded")==="true")await page.locator("#toggle-members").click();
    await page.locator("#message-input").fill("返回后继续编辑");
    for(const method of ["button","browser","escape"]) {
      await page.locator("#message-30 .avatar").click();
      await page.waitForFunction(()=>document.querySelector("#activity-dialog").open);
      if(method==="button")await page.locator("#close-activity").click();
      else if(method==="escape")await page.keyboard.press("Escape");
      else await page.evaluate(()=>history.back());
      await page.waitForFunction(()=>!document.querySelector("#activity-dialog").open && document.querySelector("#member-rail").inert);
      check(`${name}: avatar ${method} returns in one step`,!(await page.locator("#conversation").evaluate(el=>el.inert)));
      check(`${name}: draft survives ${method}`,await page.locator("#message-input").inputValue()==="返回后继续编辑");
    }
    await page.locator("#toggle-members").click();await page.locator("#members .member-summary").first().click();
    await page.locator("#close-activity").click();await page.waitForFunction(()=>!document.querySelector("#activity-dialog").open && !document.querySelector("#member-list-view").hidden);
    check(`${name}: list entry returns to the list`,!(await page.locator("#member-rail").evaluate(el=>el.inert)));
    if(width<500)await page.locator("#close-mobile-members").click();else await page.locator("#toggle-members").click();
    await page.locator("#message-31 .avatar").click();await page.locator("#notification-title").filter({hasText:"通知来源诊断"}).waitFor();
    await page.locator("#close-notification").click();await page.waitForFunction(()=>document.querySelector("#member-rail").inert);
    check(`${name}: history diagnostic returns to conversation`,true);
    await page.locator("#message-32 .avatar").click();await page.locator("#gitlab-sync-status > summary").click();
    await page.locator("#gitlab-manage-platforms").click();await page.locator("#connection-new").click();
    await page.locator("#connection-name").fill("未保存的平台");
    await page.evaluate(()=>history.back());await page.waitForFunction(()=>document.querySelector("#connection-form").hidden);
    check(`${name}: browser Back returns connection editor to settings list`,await page.locator("#settings-dialog").evaluate(el=>el.open));
    await page.evaluate(()=>history.back());await page.waitForFunction(()=>!document.querySelector("#settings-dialog").open);
    check(`${name}: settings returns to notification source`,await page.locator("#notification-detail").evaluate(el=>el.open));
    await page.locator("#gitlab-manage-platforms").click();await page.locator("#settings-tab-models").click();await page.locator("#model-list .model-row-main").click();
    await page.locator("#catalog-replace-key").check();await page.locator("#catalog-api-key").fill("do-not-save-this-key");await page.keyboard.press("Escape");
    await page.waitForFunction(()=>document.querySelector("#model-edit-form").hidden);
    check(`${name}: Escape returns model editor and clears key`,await page.locator("#catalog-api-key").inputValue()==="" && await page.locator("#settings-dialog").evaluate(el=>el.open));
    await page.locator("#close-settings").click();await page.locator("#close-notification").click();
    await page.waitForFunction(()=>document.querySelector("#member-rail").inert);
    await page.screenshot({path:path.join(out,`${name}-conversation.png`)});
    check(`${name}: conversation retains its draft after nested returns`,await page.locator("#message-input").inputValue()==="返回后继续编辑");
    await context.close();
  }
  directMode=true;
  for(const width of [1440,390,320]) {
    const context=await browser.newContext({viewport:{width,height:844},isMobile:width<500,hasTouch:width<500});
    const page=await context.newPage();page.setDefaultTimeout(10000);page.on("pageerror",e=>errors.push(e.message));
    await page.goto(origin);await page.locator("#message-32").waitFor();
    if(!(await page.locator("#member-rail").evaluate(el=>el.inert))){await page.locator("#room-menu-label").click();await page.locator("#direct-details").click();}
    for(const method of ["button","browser","escape"]) {
      await page.locator("#room-menu-label").click();await page.locator("#direct-notifications").click();
      await page.locator("#close-conversation-notifications").waitFor();
      if(method==="button")await page.locator("#close-conversation-notifications").click();
      else if(method==="escape")await page.keyboard.press("Escape");
      else await page.evaluate(()=>history.back());
      await page.waitForFunction(()=>document.querySelector("#member-rail").inert);
      check(`direct ${width}px: notifications ${method} returns to conversation`,!(await page.locator("#activity-dialog").evaluate(el=>el.open)));
    }
    await context.close();
  }
  check("returning never submits a write",!api.calls.some(c=>/\/send|\/save$|\/configure$|\/change$|\/login$/.test(c.path)));
  assert.deepEqual(errors,[]);
  await fs.writeFile(path.join(out,"report.json"),JSON.stringify({browser:browser.version(),synthetic_apis:true,checks,errors},null,2));
}finally{await browser?.close();await new Promise(resolve=>server.close(resolve));}
