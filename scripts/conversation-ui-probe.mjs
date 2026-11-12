// Chromium against synthetic APIs: mention rendering, retry geometry and the
// compact inspector. This does not verify a real provider or running backend.
import http from "node:http";
import fs from "node:fs/promises";
import path from "node:path";
import assert from "node:assert/strict";
import {createRequire} from "node:module";
import {fixture} from "../internal/web/tests/web_member_platform_fixture.mjs";

const {chromium}=createRequire(path.resolve("packaging/verifier/package.json"))("playwright");
const out=path.resolve(process.argv[2] || ".local/conversation-ui-check");
await fs.mkdir(out,{recursive:true});
const api=fixture(), owner="owner_"+"a".repeat(32), errors=[], checks=[];
let busy=false, busyReads=0, roomReads=0;
const member={room_id:"R",room_instance:"instance-R",agent_id:"a",membership_id:"member-a",version:1,selection:{mode:"default"},target_entry_id:"daily",actual:{model_id:"model-a",pending:false}};
const messages=[{seq:1,author:{kind:"agent",id:"a"},created_at:new Date().toISOString(),text:"@russellluo 请确认。\n\n`@russellluo` [链接 @russellluo](https://example.com)"}];
const server=http.createServer(async(req,res)=>{
  try {
    const url=new URL(req.url,"http://fixture");
    if(url.pathname.startsWith("/v1/")) {
      let raw="";for await(const chunk of req)raw+=chunk;
      const body=raw ? JSON.parse(raw) : undefined;
      let data;
      if(url.pathname==="/v1/access/session")data={login_enabled:false,operator:owner};
      else if(url.pathname==="/v1/ui")data={managed:true,installation_id:"ui-probe",service_epoch:"manager",operator_display_name:"russellluo",time_zone:"Asia/Shanghai",settings_version:1};
      else if(url.pathname==="/v1/rooms") {
        roomReads++;
        if(busy){busyReads++;res.writeHead(409,{"content-type":"application/json"});res.end(JSON.stringify({code:"busy",message:"room commit or unread page already pending"}));return;}
        data=await api.request(url.pathname,body);data.operator=owner;
        data.rooms[0].name="个人助理";data.rooms[0].is_direct=true;
        data.rooms[0].members=[{id:"a",name:"个人助理"}];
        Object.assign(data,{time_zone:"Asia/Shanghai",settings_version:1});
      }
      else if(url.pathname==="/v1/member/list")data={members:[api.members[0]]};
      else if(url.pathname==="/v1/read")data={messages:messages.filter(m=>m.seq>(body.after || 0)),snapshot_seq:1,has_more:false};
      else if(url.pathname==="/v1/platforms/interactions/list")data={interactions:[],selections:[]};
      else if(url.pathname==="/v1/models")data={service_epoch:"manager",catalog:{version:1,default:"daily",entries:[{id:"daily",name:"日常",model_id:"model-a",revision:1},{id:"other",name:"另一个名称很长的模型",model_id:"model-b",revision:1}]},members:[member],operations:[]};
      else data=await api.request(url.pathname+url.search,body);
      res.writeHead(200,{"content-type":"application/json","cache-control":"no-store"});res.end(JSON.stringify(data));return;
    }
    const file=url.pathname==="/" ? "index.html" : url.pathname.startsWith("/ui/") ? url.pathname.slice(4) : "";
    if(!file || file.includes("..")){res.writeHead(404);res.end();return;}
    res.writeHead(200,{"content-type":file.endsWith(".css") ? "text/css" : /\.m?js$/.test(file) ? "text/javascript" : file.endsWith(".svg") ? "image/svg+xml" : "text/html"});
    res.end(await fs.readFile(path.resolve("internal/web/assets",file)));
  }catch(error){errors.push(error.message);res.writeHead(500);res.end("{}");}
});
const check=(label,value)=>{assert.ok(value,label);checks.push(label);console.log(label);};
let browser;
try {
  await new Promise(resolve=>server.listen(0,"127.0.0.1",resolve));
  browser=await chromium.launch({headless:true});
  const origin=`http://127.0.0.1:${server.address().port}`;
  for(const [name,width,height,mobile] of [["desktop",1440,900,false],["phone",390,844,true],["small-phone",320,640,true]]) {
    const context=await browser.newContext({viewport:{width,height},isMobile:mobile,hasTouch:mobile});
    const page=await context.newPage();page.setDefaultTimeout(10000);page.on("pageerror",e=>errors.push(e.message));
    member.actual={model_id:"model-a",pending:false};
    await page.goto(origin);await page.locator("#messages .mention").waitFor();
    await page.bringToFront();
    check(`${name}: only ordinary mention text is yellow`,await page.locator("#messages .mention").count()===1);
    await page.waitForFunction(()=>!document.querySelector("#message-1").classList.contains("mentioned"));
    check(`${name}: viewed mention text stays yellow`,await page.locator("#messages .mention").count()===1);
    await page.locator("#message-input").fill("保留草稿");
    check(`${name}: internal owner is absent from composer`,!(await page.locator("#compose").textContent()).includes("owner_"));
    check(`${name}: no separate connection footer`,await page.locator(".room-footer").count()===0);
    await page.locator("#room-menu-label").click();
    await page.screenshot({path:path.join(out,`${name}-conversation.png`)});
    check(`${name}: connection status available in menu`,await page.locator("#connection").isVisible());
    await page.locator("#room-menu-label").click();
    const geometry=()=>page.locator("#timeline").evaluate(el=>({top:el.getBoundingClientRect().top,height:el.clientHeight,scroll:el.scrollTop}));
    const before=await geometry(), beforeBusy=busyReads;
    busy=true;
    await page.waitForFunction(()=>fetch("/v1/ui").then(()=>true));
    for(let attempt=0;busyReads===beforeBusy && attempt<60;attempt++)await page.waitForTimeout(100);
    assert.ok(busyReads>beforeBusy,"poll reaches busy endpoint");
    check(`${name}: brief busy does not open an error`,!await page.locator("#sync-error").isVisible());
    check(`${name}: brief busy preserves send`,await page.locator("#send").isEnabled());
    assert.deepEqual(await geometry(),before);
    await page.locator("#sync-error").waitFor();
    check(`${name}: persistent busy has readable feedback`,(await page.locator("#sync-error").textContent()).includes("服务繁忙"));
    await page.screenshot({path:path.join(out,`${name}-sync.png`)});
    assert.deepEqual(await geometry(),before);
    check(`${name}: sync warning does not move timeline`,true);
    busy=false;const readsBefore=roomReads;
    for(let attempt=0;roomReads===readsBefore && attempt<60;attempt++)await page.waitForTimeout(100);
    assert.ok(roomReads>readsBefore,"poll recovers after busy");
    await page.locator("#send:not([disabled])").waitFor();
    check(`${name}: recovery keeps draft`,await page.locator("#message-input").inputValue()==="保留草稿");
    await context.setOffline(true);await page.locator("#sync-error").waitFor();
    check(`${name}: real browser offline event shows a visible warning`,(await page.locator("#sync-error").textContent()).includes("网络已断开"));
    assert.deepEqual(await geometry(),before);
    await context.setOffline(false);await page.locator("#send:not([disabled])").waitFor();
    check(`${name}: online recovery clears the warning`,!await page.locator("#sync-error").isVisible());
    check(`${name}: no horizontal overflow`,await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
    check(`${name}: direct header has no detail icon or repeated member status`,!await page.locator("#toggle-members").isVisible() && await page.locator("#direct-status").count()===0);
    if(mobile){await page.locator("#room-menu-label").click();await page.locator("#direct-details").click();}
    await page.locator("#member-model-selection").waitFor();
    check(`${name}: default model has a readable label`,(await page.locator("#member-model-selection option").first().textContent())==="跟随默认 · 日常");
    check(`${name}: model uses shared select appearance`,await page.locator("#member-model-selection").evaluate(el=>el.classList.contains("field-control") && getComputedStyle(el).backgroundImage!=="none"));
    check(`${name}: model is applied`,await page.locator("#member-model-status").textContent()==="已生效");
    check(`${name}: healthy platform rows have no account action`,await page.locator(".member-platform-account-actions:not([hidden])").count()===0);
    member.actual.pending=true;await page.getByText("待启动",{exact:true}).waitFor();
    check(`${name}: pending model has no success tick`,!await page.locator("#member-model-ok").isVisible());
    check(`${name}: model toolbar fits inspector`,await page.locator("#member-model").evaluate(el=>{const r=el.getBoundingClientRect();return r.left>=0 && r.right<=innerWidth && el.scrollWidth<=el.clientWidth;}));
    await page.screenshot({path:path.join(out,`${name}.png`)});
    await context.close();
  }
  assert.deepEqual(errors,[]);
  await fs.writeFile(path.join(out,"report.json"),JSON.stringify({browser:browser.version(),synthetic_apis:true,checks,errors},null,2));
  console.log(`${checks.length} browser checks passed; artifacts: ${out}`);
}finally{await browser?.close();await new Promise(resolve=>server.close(resolve));}
