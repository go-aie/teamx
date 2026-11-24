// Explicit browser/component acceptance with production markup, CSS and module.
// The HTTP settings backend below is a fixture, independent of real AIO proof.
import assert from "node:assert/strict";
import {createServer} from "node:http";
import {readFile,mkdir,writeFile} from "node:fs/promises";
import {createRequire} from "node:module";
import {resolve,extname} from "node:path";
import {fileURLToPath} from "node:url";
const {chromium}=createRequire(new URL("../../../packaging/verifier/package.json",import.meta.url))("playwright");
const assets=fileURLToPath(new URL("../assets/",import.meta.url));
const output=resolve(process.argv[2] || ".local/computer-settings-browser");await mkdir(output,{recursive:true});
const html=(await readFile(resolve(assets,"index.html"),"utf8")).replace('<script type="module" src="/ui/app.js"></script>',"");
let data={service_epoch:"browser-test",installation_id:"isolated",enabled:false,provider:"aio",endpoint:"",max_screens:2,version:1,has_credential:false,status:"disabled",operations:[],enabled_members:0,running_screens:0};
const writes=[],errors=[];
const server=createServer(async(req,res)=>{
 try {
  if(req.url.startsWith("/v1/computer")){
   res.setHeader("Content-Type","application/json");
   if(req.url==="/v1/computer"){res.end(JSON.stringify(data));return;}
   if(req.url==="/v1/computer/validate"){res.end(JSON.stringify({running_screens:0}));return;}
   if(req.url==="/v1/computer/check"){
    let raw="";for await(const chunk of req)raw+=chunk;const body=JSON.parse(raw);
    assert.equal(body.runtime_mode,"docker");assert.equal(body.endpoint,"");assert.equal(body.credential,"");assert.equal(body.credential_action,"keep");
    res.end(JSON.stringify({emulated:true,architecture:"aarch64"}));return;
   }
   let raw="";for await(const chunk of req)raw+=chunk;
   const body=JSON.parse(raw);writes.push({id:body.request_id,enabled:body.enabled});
   assert.equal(body.credential_action,"replace");assert.equal(body.expected_version,data.version);
   data={...data,enabled:body.enabled,endpoint:body.endpoint,version:data.version+1,has_credential:true,status:"ready",operations:[{id:body.request_id,status:"completed",stage:"done",message:"已保存并应用。"}]};
   // Simulate lost save receipt. The browser must query local operation state.
   res.end("{");return;
  }
  if(req.url==="/"){res.setHeader("Content-Type","text/html; charset=utf-8");res.end(html);return;}
  const path=resolve(assets,decodeURIComponent(req.url.split("?")[0]).replace(/^\/ui\//,""));
  if(!path.startsWith(assets))throw Error("outside assets");
  res.setHeader("Content-Type",extname(path)===".css"?"text/css":"text/javascript");res.end(await readFile(path));
 }catch{res.statusCode=404;res.end();}
});
await new Promise(done=>server.listen(0,"127.0.0.1",done));
const browser=await chromium.launch({headless:true});
try{
 const page=await browser.newPage({viewport:{width:1100,height:900}});
 page.on("pageerror",error=>errors.push(error.message));
 await page.goto(`http://127.0.0.1:${server.address().port}/`);
 await page.evaluate(async()=>{
  document.querySelector("main").hidden=true;
  for(const panel of document.querySelectorAll('#settings-panels > [role="tabpanel"]'))panel.hidden=panel.id!=="computer-settings";
  for(const tab of document.querySelectorAll('#settings-navigation [role="tab"]'))tab.setAttribute("aria-selected",String(tab.id==="settings-tab-computer"));
  const {createComputerSettings}=await import("/ui/features/computer-settings.mjs");
  const request=async(path,body)=>{
   const response=await fetch(path,body?{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)}:{});
   if(!response.ok)throw Error("HTTP "+response.status);return response.json();
  };
  const ui=createComputerSettings({env:{request,setTimeout,clearTimeout,crypto},runtime:{$:id=>document.getElementById(id),state:{managed:true,catalog:{service_epoch:"browser-test"},operator:"owner"},storage:localStorage}});
  document.getElementById("settings-dialog").showModal();ui.mount();await ui.load();
 });
 assert.equal(await page.locator("#computer-enabled").isChecked(),false);
 await page.locator("#computer-enabled").check();await page.locator("#computer-endpoint").fill("http://127.0.0.1:18090");
 await page.locator("#computer-credential").fill("temporary-browser-acceptance-credential");
 await page.locator("#computer-test").click();await page.getByText("连接核验通过，当前运行 0 个屏幕。").waitFor();
 assert.equal(writes.length,0);
 await page.locator("#computer-save").click();await page.getByText("已完成：已保存并应用。").waitFor();
 assert.equal(writes.length,1);assert.equal(await page.locator("#computer-credential").inputValue(),"");
 await page.locator("#settings-panels").evaluate(el=>el.scrollTop=0);
 await page.screenshot({path:resolve(output,"desktop.png"),animations:"disabled"});
 await page.setViewportSize({width:390,height:844});
 await page.locator("#computer-refresh").click();await page.locator("#settings-panels").evaluate(el=>el.scrollTop=0);
 const box=await page.locator("#settings-dialog").boundingBox();assert.ok(box.x>=0 && box.x+box.width<=390);
 assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
 await page.screenshot({path:resolve(output,"mobile.png"),animations:"disabled"});
 await page.locator("#computer-runtime-mode").selectOption("docker");
 assert.equal(await page.locator("#computer-manual-fields").isVisible(),false);
 await page.locator("#computer-test").click();await page.getByText("Docker 环境可用，amd64 镜像将使用架构模拟，启动后继续核验。").waitFor();
 assert.equal(writes.length,1);assert.equal(await page.locator("#computer-credential").inputValue(),"");
 await page.locator("#settings-panels").evaluate(el=>el.scrollTop=0);await page.screenshot({path:resolve(output,"managed-mobile.png"),animations:"disabled"});
 await page.setViewportSize({width:1100,height:900});await page.screenshot({path:resolve(output,"managed-desktop.png"),animations:"disabled"});
 assert.deepEqual(errors,[]);
 await writeFile(resolve(output,"report.json"),JSON.stringify({passed:true,backend:"HTTP fixture; no Docker",checks:["production settings component and styles","default disabled","explicit test does not save","lost save receipt recovered without replay","management key cleared","managed mode hides connection fields","read-only environment check sends no address or key","emulation condition displayed","desktop and 390px viewport fit"],writes:writes.length},null,2));
}finally{await browser.close();await new Promise(done=>server.close(done));}
