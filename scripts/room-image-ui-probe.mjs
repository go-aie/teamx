// Real Chromium + synthetic HTTP APIs. No TeamX storage, model or phone OS is exercised.
import http from "node:http";
import fs from "node:fs/promises";
import path from "node:path";
import assert from "node:assert/strict";
import {createRequire} from "node:module";
import {fixture} from "../internal/web/tests/web_member_platform_fixture.mjs";
const {chromium} = createRequire(path.resolve("packaging/verifier/package.json"))("playwright");
const out = path.resolve(process.argv[2] || ".local/room-image-ui");
await fs.mkdir(out,{recursive:true});
const csp = (await fs.readFile("internal/web/handler.go","utf8")).match(/Set\("Content-Security-Policy", "([^"]+)"\)/)[1];
const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAKAAAABkCAIAAACO1KzYAAABGElEQVR4nO3RQQlCAQBEQSsZxQxiHuPYyIMnIxjiI66PgRdglzk93y+FO/18gb4a4HiA4wGOBzge4HiA4wGOBzge4HiA4wGOBzge4HiA4wGOBzge4HiA4x0CPl8vgx15dLs/BgMMGDBgwIABAx4IMGDAgJcCDBgwYMCAAQMeCDBgwICXAgwYMGDAgAEDHggwYMCAlwIMGDBgwIABAx4IMGDAgJcCDBgwYMCAAQMeCDBgwICXAgwYMGDAgAEDHggwYMCAlwIMGDBgwIABAx4IMGDAgJcCDBgwYMCAAQMeCDBgwICXAgwYMGDAgPV/AY4HOB7geIDjAY4HOB7geIDjAY4HOB7geIDjAY4HOB7geIDjAY4HOB7geB93iHuIlQoDBQAAAABJRU5ErkJggg==","base64");
const limits={max_images:4,image_bytes:2097152,total_bytes:4194304,max_pixels:16777216,mime_types:["image/png","image/jpeg","image/webp"]};
const rooms=[{room_id:"R",room_instance:"instance-R",service_epoch:"runtime"},{room_id:"B",room_instance:"instance-B",service_epoch:"runtime"}];
const api=fixture(), objects=new Map(), messages=new Map(), sends=[], uploads=[], states=[], errors=[], checks=[];
const streams=new Set(); let loseReceipt=false, failUpload=false, holdUploads=false, held=[];
const json=(res,value,status=200)=>{res.writeHead(status,{"content-type":"application/json","cache-control":"no-store"});res.end(JSON.stringify(value));};
const server=http.createServer(async(req,res)=>{
  try {
    const url=new URL(req.url,"http://fixture");
    if(url.pathname==="/v1/updates") {
      res.writeHead(200,{"content-type":"text/event-stream","cache-control":"no-store"});
      res.write(`event: hello\ndata: ${JSON.stringify({protocol:1,connection_id:"test",installation_id:"image-ui",service_epoch:"manager",global_versions:{}})}\n\n`);
      streams.add(res); req.on("close",()=>streams.delete(res));return;
    }
    if(url.pathname.startsWith("/v1/")) {
      if(url.pathname==="/v1/attachments/upload") {
        const chunks=[]; for await(const chunk of req)chunks.push(chunk);
        const form=await new Request("http://fixture",{method:"POST",headers:{"content-type":req.headers["content-type"]},body:Buffer.concat(chunks)}).formData();
        const file=form.get("file"), id=url.searchParams.get("attachment_id"), room=url.searchParams.get("room_instance");
        uploads.push({id,room,name:file.name});
        const finish=()=>{
          if(failUpload){json(res,{message:"测试网络中断"},503);return;}
          const attachment={id,kind:"image",name:file.name,size:file.size,mime_type:file.type,width:160,height:100};
          objects.set(id,{attachment,room});json(res,{attachment,expires_at:new Date(Date.now()+86400000).toISOString()});
        };
        if(holdUploads)held.push(finish);else finish();return;
      }
      if(url.pathname==="/v1/attachments/content") {
        const object=objects.get(url.searchParams.get("attachment_id"));
        if(!object || object.room!==url.searchParams.get("room_instance")){res.writeHead(404);res.end();return;}
        res.writeHead(200,{"content-type":"image/png","cache-control":"no-store","x-content-type-options":"nosniff"});res.end(png);return;
      }
      let raw="";for await(const chunk of req)raw+=chunk;
      const body=raw?JSON.parse(raw):undefined;
      let data;
      if(url.pathname==="/v1/access/session")data={login_enabled:false,operator:"tester"};
      else if(url.pathname==="/v1/ui")data={managed:true,installation_id:"image-ui",service_epoch:"manager",operator_display_name:"测试用户",time_zone:"Asia/Shanghai",settings_version:1};
      else if(url.pathname==="/v1/platforms/interactions/list")data={interactions:[],selections:[]};
      else if(url.pathname==="/v1/human-read/state" || url.pathname==="/v1/human-read/confirm")data={version:2,...rooms.find(room=>room.room_instance===body.room_instance),operator_id:body.caller.id,revision:1,snapshot_seq:(messages.get(body.room_instance)||[]).length,cleared_through:0,opened_through:body.opened_through||0,mention_seen_seqs:[],unread_count:0,mention_count:0,new_message_seqs:[],unseen_mention_seqs:[]};
      else if(url.pathname==="/v1/rooms"){
        data=await api.request("/v1/rooms");
        data.rooms=[0,1].map(i=>({...data.rooms[0],...rooms[i],name:i?"Other":"Review",service:rooms[i],runtime_status:"ready"}));
        Object.assign(data,{time_zone:"Asia/Shanghai",settings_version:1});
      } else if(url.pathname==="/v1/info")data={...rooms.find(room=>room.room_instance===url.searchParams.get("room_instance")),attachments:limits};
      else if(url.pathname==="/v1/attachments/state"){
        states.push(body);data={attachments:body.ids.map(id=>{const object=objects.get(id);return object?{id,status:"available",attachment:object.attachment}:{id,status:"missing"};})};
      } else if(url.pathname==="/v1/send"){
        sends.push(body);
        const list=messages.get(body.room_instance)||[];
        let message=list.find(message=>message.client_message_id===body.client_message_id);
        if(!message){message={...body,seq:list.length+1,author:body.caller,attachments:(body.attachments||[]).map(item=>objects.get(item.id).attachment),created_at:new Date().toISOString()};list.push(message);messages.set(body.room_instance,list);}
        if(loseReceipt){req.socket.destroy();return;} data={seq:message.seq};
      } else if(url.pathname==="/v1/read"){
        const list=messages.get(body.room_instance)||[];data={messages:list.filter(message=>message.seq>(body.after||0)),snapshot_seq:list.length,has_more:false};
      } else data=await api.request(url.pathname+url.search,body);
      json(res,data);return;
    }
    const file=url.pathname==="/"?"index.html":url.pathname.startsWith("/ui/")?url.pathname.slice(4):"";
    if(!file || file.includes("..")){res.writeHead(404);res.end();return;}
    res.writeHead(200,{"content-security-policy":csp,"content-type":file.endsWith(".css")?"text/css":/\.m?js$/.test(file)?"text/javascript":file.endsWith(".svg")?"image/svg+xml":"text/html"});
    res.end(await fs.readFile(path.resolve("internal/web/assets",file)));
  }catch(error){errors.push(error.stack);json(res,{message:error.message},500);}
});
const check=(label,value)=>{assert.ok(value,label);checks.push(label);console.log(label);};
let browser;
try {
  await new Promise(resolve=>server.listen(0,"127.0.0.1",resolve));
  browser=await chromium.launch({headless:true});
  const origin=`http://127.0.0.1:${server.address().port}`;
  for(const width of [1440,390,320]) {
    objects.clear();messages.clear();sends.length=uploads.length=states.length=0;
    const context=await browser.newContext({viewport:{width,height:width===320?640:900},isMobile:width<600,hasTouch:width<600});
    const page=await context.newPage();page.setDefaultTimeout(10000);page.on("pageerror",error=>{errors.push(error.stack);console.error(error.stack);});
    const enter = async () => {
      if (width<600 && await page.locator("#conversation").evaluate(el=>el.inert)) await page.getByRole("button",{name:/Review/}).first().click();
      await page.locator("#message-input:not([disabled])").waitFor();
    };
    await page.goto(origin);await page.locator("#compose-more:enabled").waitFor();await enter();
    check(`${width}: plus, input and send follow the same left-to-right order`,await page.evaluate(()=>{
      const plus=document.querySelector("#compose-more").getBoundingClientRect(), input=document.querySelector("#message-input").getBoundingClientRect(), send=document.querySelector("#send").getBoundingClientRect();
      return plus.right<=input.left && input.right<=send.left && Math.abs(plus.bottom-send.bottom)<2 && plus.width>=44 && send.width>=44;
    }));
    check(`${width}: default directory does not add a composer row`,!await page.locator("#workspace-trigger").isVisible());
    await page.locator("#message-input").fill("第一行\n第二行\n第三行");
    check(`${width}: multiline input grows while controls stay at its bottom`,await page.evaluate(()=>{
      const input=document.querySelector("#message-input").getBoundingClientRect(), plus=document.querySelector("#compose-more").getBoundingClientRect();
      return input.height>plus.height && Math.abs(input.bottom-plus.bottom)<2;
    }));
    await page.locator("#message-input").fill("整理本周项目进展");
    await page.screenshot({animations:"disabled",path:path.join(out,`${width}-composer.png`)});
    await page.locator("#compose-more").click(); await page.locator("#add-images:enabled").waitFor();
    const surface=page.locator("#compose-menu");
    await surface.evaluate(el=>Promise.all(el.getAnimations().map(animation=>animation.finished)));
    check(`${width}: expanded action surface stays inside viewport`,await surface.evaluate(el=>{
      const r=el.getBoundingClientRect();return r.left>=0 && r.right<=innerWidth && r.top>=0 && r.bottom<=innerHeight;
    }));
    await page.screenshot({animations:"disabled",path:path.join(out,`${width}-menu.png`)});
    if(width<600){
      check(`${width}: expanded panel raises the input and keeps its controls accessible`,await surface.evaluate(el=>{
        const input=document.querySelector("#message-input"), trigger=document.querySelector("#compose-more"), send=document.querySelector("#send"), box=el.getBoundingClientRect();
        return !el.matches(":modal") && !input.closest("[inert]") && input.getBoundingClientRect().top>=0 && input.getBoundingClientRect().bottom<=box.top && trigger.getBoundingClientRect().bottom<=box.top && send.getBoundingClientRect().bottom<=box.top;
      }));
      check(`${width}: expanded panel has no extra cancel action`,await page.locator("#compose-menu-close").count()===0);
      await page.locator("#compose-more").click();await surface.waitFor({state:"hidden"});
      check(`${width}: plus toggles the expanded panel closed`,true);
      await page.locator("#compose-more").click();await page.locator("#message-input").click();await surface.waitFor({state:"hidden"});
      check(`${width}: returning to editing closes the panel and retains the draft`,await page.locator("#message-input").inputValue()==="整理本周项目进展" && await page.locator("#message-input").evaluate(el=>el===document.activeElement));
      await page.locator("#compose-more").click();await page.mouse.click(10,80);await surface.waitFor({state:"hidden"});
      check(`${width}: tapping the message area closes the panel`,true);
      await page.locator("#compose-more").click();
      const grip=await page.locator("#compose-menu-grip").boundingBox();
      await page.mouse.move(grip.x+grip.width/2,grip.y+8);await page.mouse.down();
      await page.mouse.move(grip.x+grip.width/2,grip.y+68,{steps:5});await page.mouse.up();
      await surface.waitFor({state:"hidden"});check(`${width}: dragging the sheet handle down dismisses it`,true);
      await page.locator("#compose-more").click();
      await page.waitForFunction(()=>history.state?.teamxOverlay==="compose-menu");
      await page.goBack();await surface.waitFor({state:"hidden"});check(`${width}: browser Back closes the sheet within the conversation`,await page.locator("#message-input").isVisible());
      if(width===390){
        await page.setViewportSize({width:844,height:390});await page.locator("#compose-more").click();
        await surface.evaluate(el=>Promise.all(el.getAnimations().map(animation=>animation.finished)));
        await page.screenshot({animations:"disabled",path:path.join(out,"844-landscape-menu.png")});
        check("touch landscape keeps input above the expanded panel",await surface.evaluate(el=>!el.matches(":modal") && el.classList.contains("compose-sheet") && document.querySelector("#message-input").getBoundingClientRect().top>=0 && document.querySelector("#message-input").getBoundingClientRect().bottom<=el.getBoundingClientRect().top && el.getBoundingClientRect().bottom<=innerHeight));
        await page.locator("#compose-more").click();await page.setViewportSize({width,height:900});
      }
    }else{
      check("desktop menu opens above the bottom composer",await surface.evaluate(el=>el.getBoundingClientRect().bottom<=document.querySelector("#compose-more").getBoundingClientRect().top));
      await page.keyboard.press("Escape");
      await page.locator("#compose-more").evaluate(el=>{el.style.position="fixed";el.style.top="80px";});
      await page.locator("#compose-more").click();
      check("desktop menu opens downward when there is room",await surface.evaluate(el=>el.getBoundingClientRect().top>=document.querySelector("#compose-more").getBoundingClientRect().bottom));
      await page.keyboard.press("Escape");await page.locator("#compose-more").evaluate(el=>{el.style.removeProperty("position");el.style.removeProperty("top");});
    }
    await page.locator("#message-input").fill("");
    const add=async(names)=>{
      await page.locator("#compose-more").click();await page.locator("#add-images:enabled").waitFor();
      const chooser=page.waitForEvent("filechooser");await page.locator("#add-images").click();
      await (await chooser).setFiles(names.map(name=>({name,mimeType:"image/png",buffer:png})));
    };
    await add(["first.png"]);await page.locator('.attachment-draft[data-status="ready"]').waitFor();
    check(`${width}: blob thumbnail passes actual CSP`,await page.locator(".attachment-draft img").evaluate(img=>img.complete&&img.naturalWidth===160&&img.src.startsWith("blob:")));
    await page.locator("#message-input").fill("文字与多图");await add(["second.png"]);
    await page.waitForFunction(()=>document.querySelectorAll('.attachment-draft[data-status="ready"]').length===2);
    check(`${width}: plus stays available with text and images`,await page.locator("#compose-more").isVisible());
    await page.screenshot({animations:"disabled",path:path.join(out,`${width}-draft.png`)});
    check(`${width}: composer controls remain in viewport`,await page.locator("#send").evaluate(el=>{const r=el.getBoundingClientRect();return r.bottom<=innerHeight&&r.right<=innerWidth&&r.left>=0;}));
    await page.locator("#send").click();await page.locator("#message-1 .room-message-attachments img").first().waitFor();
    assert.equal(sends.length,1);assert.equal(sends[0].attachments.length,2);assert.equal(sends[0].text,"文字与多图");
    await page.locator("#message-1 .attachment-preview").first().click();await page.locator(".message-image-dialog").waitFor();
    check(`${width}: shared preview exposes zoom rotate and download`,await page.locator('.message-image-dialog [data-image-action="rotate"]').count()>0&&await page.locator(".message-image-download").count()===1);
    await page.keyboard.press("Escape");
    await add(["only-image.png"]);await page.locator('.attachment-draft[data-status="ready"]').waitFor();
    loseReceipt=true;await page.locator("#send").click();await page.locator("#discard-send").waitFor();
    const firstRequest=structuredClone(sends.at(-1)), uploadCount=uploads.length;
    await page.reload();await page.locator("#message-input:not([disabled])").waitFor();await enter();await page.locator('#send[aria-label="重试发送"]').waitFor();
    loseReceipt=false;await page.locator("#send").click();await page.locator("#message-2 .attachment-preview").waitFor();
    assert.deepEqual(sends.at(-1),firstRequest);assert.equal(uploads.length,uploadCount);assert.equal(messages.get("instance-R").length,2);
    check(`${width}: uncertain pure-image retry survives reload without uploading or duplicating`,true);
    await page.locator("#message-2 .reply-button").click();
    check(`${width}: image-only quote uses image summary`,(await page.locator("#reply-label").textContent())==="[图片]");
    await page.locator("#cancel-reply").click();
    check(`${width}: image-only selection includes copy summary`,await page.locator("#message-2 .message-text").evaluate(el=>{
      const range=document.createRange();range.selectNodeContents(el);const selection=getSelection();selection.removeAllRanges();selection.addRange(range);
      const copied=selection.toString();selection.removeAllRanges();return copied.includes("[图片]");
    }));
    await page.locator("#message-input").evaluate(input=>{
      input.focus();const data=new DataTransfer();data.items.add(new File([new Uint8Array([1,2,3])],"paste.png",{type:"image/png"}));data.setData("text/plain","粘贴文字");
      input.dispatchEvent(new ClipboardEvent("paste",{clipboardData:data,bubbles:true,cancelable:true}));
    });
    await page.locator('.attachment-draft[data-status="ready"]').waitFor();
    check(`${width}: paste uses upload path and retains accompanying text`,await page.locator("#message-input").inputValue()==="粘贴文字");
    await page.reload();await page.locator("#message-input:not([disabled])").waitFor();await enter();await page.locator('.attachment-draft[data-status="ready"]').waitFor();
    check(`${width}: reload restores uploaded draft using state API`,states.length>0&&await page.locator(".attachment-draft img").evaluate(img=>img.complete&&img.naturalWidth===160&&!img.src.startsWith("blob:")));
    // A file picker cancellation leaves the draft unchanged.
    const before=await page.locator(".attachment-draft").count();
    await page.locator("#attachment-input").dispatchEvent("cancel");check(`${width}: cancelled selection keeps the draft`,await page.locator(".attachment-draft").count()===before);
    await page.locator(".attachment-remove").click();await page.locator("#message-input").fill("");
    failUpload=true;await add(["failed.png"]);await page.locator('.attachment-draft[data-status="failed"]').waitFor();
    check(`${width}: failed image blocks sending`,await page.locator("#send").isDisabled());
    const failedID=uploads.at(-1).id;failUpload=false;await page.getByRole("button",{name:"重试",exact:true}).click();
    await page.locator('.attachment-draft[data-status="ready"]').waitFor();assert.equal(uploads.at(-1).id,failedID);
    await page.locator(".attachment-remove").click();
    // Reverse upload completion and switch away before it arrives.
    holdUploads=true;await add(["slow-first.png","fast-second.png"]);
    await page.waitForFunction(()=>document.querySelectorAll('.attachment-draft[data-status="uploading"]').length===2);
    if(width<600)await page.locator("#mobile-back-to-rooms").click();
    await page.getByRole("button",{name:/Other/}).first().click();
    held.pop()();held.pop()();held=[];holdUploads=false;
    await page.waitForTimeout(100);
    check(`${width}: late uploads never enter another Room`,await page.locator(".attachment-draft").count()===0);
    if(width<600)await page.locator("#mobile-back-to-rooms").click();
    await page.getByRole("button",{name:/Review/}).first().click();
    await page.waitForFunction(()=>document.querySelectorAll('.attachment-draft[data-status="ready"]').length===2);
    assert.deepEqual(await page.locator(".attachment-draft-label").allTextContents(),["slow-first.png","fast-second.png"]);
    check(`${width}: returning retains upload selection order`,true);
    await page.waitForFunction(()=>!document.querySelector("#add-images").disabled);
    await page.locator("#compose-more").focus();await page.keyboard.press("ArrowDown");
    assert.equal(await page.locator("#add-images").evaluate(el=>el===document.activeElement),true);
    await page.keyboard.press("Escape");await page.waitForFunction(()=>document.querySelector("#compose-more")===document.activeElement);assert.equal(await page.locator("#compose-more").evaluate(el=>el===document.activeElement),true);
    check(`${width}: menu keyboard navigation and Escape return focus`,true);
    await page.locator("#compose-more").click();await page.locator("#open-workspace").click();await page.locator("#close-workspace").click();
    await page.waitForFunction(()=>document.querySelector("#compose-more")===document.activeElement);
    check(`${width}: workspace returns focus to the shared trigger`,await page.locator("#compose-more").evaluate(el=>el===document.activeElement));
    check(`${width}: no horizontal overflow`,await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
    await page.locator("#message-input").fill("展开面板时直接发送");await page.locator("#compose-more").click();
    await page.locator("#send").click();await page.locator("#message-3 .attachment-preview").first().waitFor();
    check(`${width}: send stays usable while the action panel is open`,sends.at(-1).text==="展开面板时直接发送" && await page.locator("#message-input").inputValue()==="" && !await page.locator("#compose-menu").isVisible());
    await page.screenshot({animations:"disabled",path:path.join(out,`${width}-messages.png`)});
    await context.close();
  }
  assert.deepEqual(errors,[]);
  await fs.writeFile(path.join(out,"evidence.json"),JSON.stringify({backend:"synthetic HTTP API",browser:browser.version(),widths:[1440,390,320],checks,errors,notVerified:["real TeamX storage","real iOS Safari and Android Chrome file pickers and clipboard","real DSH/model"]},null,2)+"\n");
}catch(error) { console.error("Fixture errors:",errors); throw error; }finally {
  await browser?.close();for(const stream of streams)stream.destroy();server.closeAllConnections();await new Promise(resolve=>server.close(resolve));
}
