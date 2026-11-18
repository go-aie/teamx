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
const reading=new Map();
let directMode=false;
const messages=Array.from({length:30},(_,i)=>({seq:i+1,author:{kind:"agent",id:"a"},text:`消息 ${i+1}：检查返回后继续阅读的位置。`,created_at:new Date().toISOString()}));
messages.push(...[31,32].map(seq=>({seq,author:{kind:"integration",id:"gitlab"},text:"平台通知来源",created_at:new Date().toISOString(),
  source:{provider:"gitlab",channel:"todos"}})));
const server=http.createServer(async(req,res)=>{
  try {
    const url=new URL(req.url,"http://fixture");
    if(url.pathname==="/v1/updates") {
      res.writeHead(200,{"content-type":"text/event-stream","cache-control":"no-store"});
      const connection_id="navigation-probe";
      res.write(`event: hello\ndata: ${JSON.stringify({protocol:1,connection_id,service_epoch:"manager",installation_id:"navigation-probe"})}\n\n`);
      const interval=setInterval(()=>res.write(`event: heartbeat\ndata: ${JSON.stringify({connection_id})}\n\n`),10000);
      res.on("close",()=>clearInterval(interval));
      return;
    }
    if(url.pathname.startsWith("/v1/")) {
      let raw="";for await(const chunk of req)raw+=chunk;
      const body=raw ? JSON.parse(raw) : undefined;
      let data;
      if(url.pathname==="/v1/access/session")data={login_enabled:true,operator:"tester"};
      else if(url.pathname==="/v1/ui")data={managed:true,installation_id:"navigation-probe",service_epoch:"manager",time_zone:"Asia/Shanghai",settings_version:1};
      else if(url.pathname==="/v1/rooms") {
        data=await api.request(url.pathname,body);for(const room of data.rooms){room.runtime_status="ready";room.is_direct=directMode;room.created_at=new Date().toISOString();room.last_message_at=room.created_at;room.last_message_preview={author:{kind:"agent",id:"a"},agent_name:"助理",text:"已整理好讨论结果，请查看。"};if(directMode)room.members=[{id:"a",name:"助理"}];}
        const sample=data.rooms[0];
        data.rooms.push(...Array.from({length:12},(_,i)=>({...sample,room_id:`sample-${i}`,room_instance:`instance-${i}`,name:["产品讨论","研发协作","代码审查","每周计划"][i%4]+(i>3 ? ` ${i+1}` : ""),created_at:new Date(Date.now()-(i+1)*3600000).toISOString(),last_message_at:new Date(Date.now()-(i+1)*3600000).toISOString()})));
        for(const [i,room] of data.rooms.entries()) {
          const saved=reading.get(JSON.stringify([room.room_id,room.room_instance,data.operator]));
          room.human_read={status:"ready",revision:saved?.revision || 0,snapshot_seq:32,cleared_through:0,unread_count:i ? 9 : 32-(saved?.opened || 0),mention_count:0};
        }
        Object.assign(data,{time_zone:"Asia/Shanghai",settings_version:1});
      } else if(url.pathname==="/v1/member/list" && directMode)data={members:[api.members[0]]};
      else if(url.pathname==="/v1/read")data={messages:messages.filter(m=>m.seq>(body.after || 0)),snapshot_seq:32,has_more:false};
      else if(url.pathname.startsWith("/v1/human-read/")) {
        const key=JSON.stringify([body.room_id,body.room_instance,body.caller.id]);
        const saved=reading.get(key) || {revision:0,opened:0};
        if(url.pathname.endsWith("/confirm")){saved.opened=Math.max(saved.opened,Math.min(32,body.opened_through || 0));saved.revision++;reading.set(key,saved);}
        data={version:2,room_id:body.room_id,room_instance:body.room_instance,operator_id:body.caller.id,revision:saved.revision,snapshot_seq:32,cleared_through:0,opened_through:saved.opened,mention_seen_seqs:[],unseen_mention_seqs:[],unread_count:32-saved.opened,mention_count:0};
        if(body.include_new_messages)data.new_message_seqs=Array.from({length:32-saved.opened},(_,i)=>saved.opened+i+1);
      }
      else if(url.pathname==="/v1/member/input-attention")data={version:0,platforms:[]};
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
  for(const [name,width,height] of [["desktop",1440,900],["phone",390,844],["small-phone",320,640],["large-phone",430,932],["landscape",844,390]]) {
    const mobile=width<500 || height<500;
    const context=await browser.newContext({viewport:{width,height},isMobile:mobile,hasTouch:mobile});
    const page=await context.newPage();page.setDefaultTimeout(10000);page.on("pageerror",e=>errors.push(e.message));
    await page.goto(origin);await page.locator("#message-32").waitFor();
    check(`${name}: only desktop shows the global unread count at the top`,await page.locator("#conversation-unread-total").isVisible()===!mobile && await page.locator("#conversation-unread-total").textContent()==="99+");
    check(`${name}: default entry reflects its navigation role`,await page.locator("#conversation-filter-label").textContent()===(mobile ? "全部" : "消息"));
    await page.locator("#conversation-filter").click();
    check(`${name}: filter menu contains only conversation types`,(await page.locator("#conversation-filter-menu button").allTextContents()).join(",")==="全部,私信,房间");
    await page.screenshot({path:path.join(out,`${name}-message-menu.png`),animations:"disabled"});
    await page.locator("#conversation-filter").click();
    if(mobile) {
      await page.locator("#mobile-primary-navigation").waitFor();
      check(`${name}: opens the message list with brand and bottom navigation`,await page.locator("#room-brand").isVisible());
      check(`${name}: default filter states its scope with a chevron`,await page.locator("#conversation-filter-label").isVisible() && await page.locator(".conversation-filter-chevron").isVisible());
      check(`${name}: only one header precedes the list`,!await page.locator(".room-nav-heading").isVisible());
      await page.screenshot({path:path.join(out,`${name}-messages.png`),animations:"disabled"});
      await page.locator("#conversation-filter").click();await page.locator("#conversation-filter-direct").click();
      check(`${name}: selected filter remains explicit`,await page.locator("#conversation-filter-label").isVisible() && await page.locator("#conversation-filter-label").textContent()==="私信");
      check(`${name}: filtering retains only the bottom global unread badge`,!await page.locator("#conversation-unread-total").isVisible() && await page.locator("#mobile-messages-unread").isVisible() && await page.locator("#mobile-messages-unread").textContent()==="99+");
      await page.screenshot({path:path.join(out,`${name}-private-filter.png`),animations:"disabled"});
      await page.locator("#rooms-empty").waitFor();
      await page.locator("#new-room").click();await page.locator("#new-direct").click();
      check(`${name}: plus still opens direct creation`,await page.locator("#create-dialog").evaluate(el=>el.open));
      await page.locator("#close-create").click();
      await page.locator("#conversation-filter").click();await page.locator("#conversation-filter-all").click();
      await page.locator("#room-nav").evaluate(el=>el.scrollTop=120);
      const listScroll=await page.locator("#room-nav").evaluate(el=>el.scrollTop);
      await page.locator("#mobile-tab-profile").click();
      check(`${name}: profile is a page without the brand header`,await page.locator("#mobile-profile").isVisible() && !await page.locator("#room-brand").isVisible());
      check(`${name}: profile retains the messages unread reminder`,await page.locator("#mobile-messages-unread").isVisible() && await page.locator("#mobile-messages-unread").textContent()==="99+");
      await page.screenshot({path:path.join(out,`${name}-profile.png`),animations:"disabled"});
      if(name==="phone") {
        await page.setViewportSize({width:1440,height:900});
        check("rotation to desktop restores sidebar controls",await page.locator("#user-menu-toggle").isVisible() && await page.locator("#conversation-filter").evaluate(el=>el.parentElement.id==="conversation-filter-container"));
        check("rotation to desktop restores the messages label and global count",await page.locator("#conversation-filter-label").textContent()==="消息" && await page.locator("#conversation-unread-total").isVisible());
        await page.setViewportSize({width,height});
        check("rotation back to phone retains the profile page",await page.locator("#mobile-profile").isVisible() && !await page.locator("#room-brand").isVisible());
        check("rotation back to phone restores the filter label without a top badge",await page.locator("#conversation-filter-label").textContent()==="全部" && !await page.locator("#conversation-unread-total").isVisible());
      }
      await page.locator("#profile-platforms").click();await page.locator("#settings-dialog").waitFor();
      await page.locator("#settings-dialog").evaluate(async el=>Promise.all(el.getAnimations().map(animation=>animation.finished.catch(()=>{}))));
      check(`${name}: profile opens the requested settings category`,await page.locator("#settings-tab-platforms").getAttribute("aria-selected")==="true");
      check(`${name}: settings fill the viewport`,await page.locator("#settings-dialog").evaluate(el=>Math.abs(el.getBoundingClientRect().width-innerWidth)<1));
      check(`${name}: settings content stays within the viewport`,await page.locator("#settings-dialog").evaluate(el=>el.scrollWidth<=el.clientWidth && el.getBoundingClientRect().bottom<=innerHeight+1));
      await page.screenshot({path:path.join(out,`${name}-settings.png`),animations:"disabled"});
      await page.evaluate(()=>history.back());await page.waitForFunction(()=>!document.querySelector("#settings-dialog").open);
      check(`${name}: settings Back restores profile and focus`,await page.locator("#profile-platforms").evaluate(el=>el===document.activeElement));
      await page.evaluate(()=>history.back());await page.waitForFunction(()=>document.querySelector("#mobile-profile").hidden);
      check(`${name}: profile Back restores messages`,await page.locator("#room-brand").isVisible());
      check(`${name}: profile settings retain list scroll`,await page.locator("#room-nav").evaluate(el=>el.scrollTop)===listScroll);
      await page.locator("#room-list .room-choice").first().click();
      await page.locator("#mobile-primary-navigation").waitFor({state:"hidden"});
      check(`${name}: conversation hides root navigation`,!await page.locator("#mobile-primary-navigation").isVisible() && !await page.locator("#room-brand").isVisible());
    } else {
      check(`${name}: desktop retains its sidebar and user menu`,await page.locator("#user-menu-toggle").isVisible() && !await page.locator("#mobile-primary-navigation").isVisible());
      for(const [filter,label,visible] of [["direct","私信",false],["room","房间",true],["all","消息",true]]) {
        await page.locator("#conversation-filter").click();await page.locator(`#conversation-filter-${filter}`).click();
        check(`${name}: ${label} pairs its label with the selected scope's unread count`,await page.locator("#conversation-filter-label").textContent()===label && await page.locator("#conversation-unread-total").isVisible()===visible);
        if(filter==="room")await page.screenshot({path:path.join(out,"desktop-room-filter.png"),animations:"disabled"});
      }
    }
    if(await page.locator("#room-menu-label").getAttribute("aria-expanded")==="true")await page.locator("#room-menu-label").click();
    await page.locator("#message-input").fill("返回后继续编辑");
    if(mobile) {
      await page.locator("#mobile-back-to-rooms").click();await page.locator("#mobile-tab-profile").click();await page.locator("#mobile-tab-messages").click();
      await page.locator("#room-list .room-choice").first().click();
      check(`${name}: switching root tabs retains the draft`,await page.locator("#message-input").inputValue()==="返回后继续编辑");
    }
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
    await page.locator("#room-menu-label").click();await page.locator("#members .member-summary").first().click();
    await page.locator("#close-activity").click();await page.waitForFunction(()=>!document.querySelector("#activity-dialog").open && !document.querySelector("#member-list-view").hidden);
    check(`${name}: list entry returns to the list`,!(await page.locator("#member-rail").evaluate(el=>el.inert)));
    if(mobile)await page.locator("#close-mobile-members").click();else await page.locator("#room-menu-label").click();
    await page.locator("#message-31 .avatar").click();
    check(`${name}: historical platform message is read-only`,!(await page.locator("#notification-detail").evaluate(el=>el.open)));
    await page.locator("#message-30 .avatar").click();
    await page.locator("#agent-tab-inbox").click();
    await page.locator("#inbox-entries summary").filter({hasText:"来源详情"}).first().click();
    await page.getByRole("button",{name:"查看通知来源设置"}).first().click();
    await page.locator("#gitlab-sync-status > summary").click();
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
    await page.locator("#close-activity").click();
    await page.waitForFunction(()=>document.querySelector("#member-rail").inert);
    await page.screenshot({path:path.join(out,`${name}-conversation.png`),animations:"disabled"});
    check(`${name}: conversation retains its draft after nested returns`,await page.locator("#message-input").inputValue()==="返回后继续编辑");
    await context.close();
  }
  directMode=true;
  for(const width of [1440,390,320]) {
    const context=await browser.newContext({viewport:{width,height:844},isMobile:width<500,hasTouch:width<500});
    const page=await context.newPage();page.setDefaultTimeout(10000);page.on("pageerror",e=>errors.push(e.message));
    await page.goto(origin);await page.locator("#message-32").waitFor();
    if(width<500)await page.locator("#room-list .room-choice").first().click();
    if(!(await page.locator("#member-rail").evaluate(el=>el.inert))){await page.locator("#room-menu-label").click();}
    await page.locator("#room-menu-label").click();
    check(`direct ${width}px: assistant platform settings are visible`,await page.locator("#platforms-member-view").isVisible());
    await page.locator("#close-activity").click();
    await page.waitForFunction(()=>document.querySelector("#member-rail").inert);
    await context.close();
  }
  check("returning never submits a write",!api.calls.some(c=>/\/send|\/save$|\/configure$|\/change$|\/login$/.test(c.path)));
  assert.deepEqual(errors,[]);
  await fs.writeFile(path.join(out,"report.json"),JSON.stringify({browser:browser.version(),synthetic_apis:true,checks,errors},null,2));
}finally{await browser?.close();await new Promise(resolve=>server.close(resolve));}
