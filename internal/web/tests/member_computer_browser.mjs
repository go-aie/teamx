// Explicit member-settings browser acceptance; HTTP backend is controlled.
import assert from "node:assert/strict";
import {createServer} from "node:http";
import {readFile,mkdir,writeFile} from "node:fs/promises";
import {createRequire} from "node:module";
import {resolve,extname} from "node:path";
import {fileURLToPath} from "node:url";
const {chromium}=createRequire(new URL("../../../packaging/verifier/package.json",import.meta.url))("playwright");
const assets=fileURLToPath(new URL("../assets/",import.meta.url));
const output=resolve(process.argv[2] || ".local/member-computer-browser");await mkdir(output,{recursive:true});
const html=(await readFile(resolve(assets,"index.html"),"utf8")).replace('<script type="module" src="/ui/app.js"></script>',"");
let data={computer_enabled:false,computer_settings_version:0,global_enabled:false,status:"completed"};
const writes=[],errors=[];
const server=createServer(async(req,res)=>{
 try{
  if(req.url.startsWith("/v1/members/computer")){
   res.setHeader("Content-Type","application/json");
   if(req.method==="GET"){res.end(JSON.stringify(data));return;}
   let raw="";for await(const c of req)raw+=c;const body=JSON.parse(raw);
   assert.equal(body.membership_id,"member-A");assert.equal(body.service_epoch,"member-browser");assert.equal(body.expected_version,0);assert.match(body.operation_id,/^[a-f0-9]{32}$/);writes.push(body);
   data={...data,computer_enabled:true,computer_settings_version:1,status:"applying",operation_id:body.operation_id};
   res.end("{");return;
  }
  if(req.url==="/"){res.setHeader("Content-Type","text/html; charset=utf-8");res.end(html);return;}
  const path=resolve(assets,decodeURIComponent(req.url.split("?")[0]).replace(/^\/ui\//,""));if(!path.startsWith(assets))throw Error("outside assets");
  res.setHeader("Content-Type",extname(path)===".css"?"text/css":"text/javascript");res.end(await readFile(path));
 }catch{res.statusCode=404;res.end();}
});
await new Promise(done=>server.listen(0,"127.0.0.1",done));
const browser=await chromium.launch({headless:true});
try{
 const page=await browser.newPage({viewport:{width:1100,height:900}});page.on("pageerror",e=>errors.push(e.message));
 await page.goto(`http://127.0.0.1:${server.address().port}/`);
 await page.evaluate(async()=>{
  const {createMemberComputer}=await import("/ui/features/member-computer.mjs");
  const request=async(path,body)=>{const res=await fetch(path,body?{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)}:{});if(!res.ok)throw Error("HTTP "+res.status);return res.json();};
  const $=id=>document.getElementById(id);$("activity-dialog").showModal();$("agent-controls").hidden=false;
  const name=$("agent-name");if(name)name.textContent="测试助理";
  const identity={room_id:"room-A",room_instance:"instance-A",agent_id:"A",membership_id:"member-A",epoch:"member-browser"};
  window.memberComputerUI=createMemberComputer({env:{request,window},runtime:{$,state:{operator:"owner"}},current:()=>true});
  window.memberComputerUI.mount();window.memberComputerUI.open(identity);
 });
 await page.locator("#member-computer").waitFor({state:"visible"});
 assert.equal(await page.locator("#member-computer-enabled").isChecked(),false);
 await page.locator("#member-computer-enabled").check();
 await page.getByText("正在应用电脑设置…").waitFor();assert.equal(writes.length,1);
 assert.equal(await page.locator("#member-computer-enabled").isDisabled(),true);
 data={...data,status:"completed",operation_id:undefined};await page.evaluate(()=>window.memberComputerUI.refresh());
 await page.getByText("等待全局电脑连接启用").waitFor();
 assert.equal(await page.locator("#member-computer-enabled").isChecked(),true);assert.equal(await page.locator("#member-computer-enabled").isDisabled(),false);
 await page.screenshot({path:resolve(output,"desktop.png"),animations:"disabled"});
 await page.setViewportSize({width:390,height:844});
 assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
 await page.screenshot({path:resolve(output,"mobile.png"),animations:"disabled"});assert.deepEqual(errors,[]);
 await writeFile(resolve(output,"report.json"),JSON.stringify({passed:true,production_markup:true,real_chromium:true,backend:"controlled HTTP",viewport:["1100x900","390x844"],lost_receipt_reconciled:true,preference_preserved_when_global_disabled:true,writes:writes.length},null,2));
}finally{await browser.close();await new Promise(done=>server.close(done));}
