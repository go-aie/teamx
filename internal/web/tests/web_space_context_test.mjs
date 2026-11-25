import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import vm from "node:vm";

test("Space document routes and storage stay fixed across other tabs and location changes", async () => {
  const source = await readFile(new URL("../assets/space-context.mjs", import.meta.url), "utf8");
  const previous = globalThis.location, previousFetch = globalThis.fetch;
  try {
    const a = "a".repeat(32), b = "b".repeat(32);
    globalThis.location = {pathname:`/s/${a}/`};
    const A = await import(`data:text/javascript;base64,${Buffer.from(source).toString("base64")}#a`);
    globalThis.location = {pathname:`/s/${b}/`};
    const B = await import(`data:text/javascript;base64,${Buffer.from(source).toString("base64")}#b`);
    assert.equal(A.spaceURL("/v1/rooms"), `/s/${a}/v1/rooms`);
    assert.equal(B.spaceURL("/v1/updates?service_epoch=e"), `/s/${b}/v1/updates?service_epoch=e`);
    assert.equal(A.spaceURL("/access"), `/s/${a}/access`);
    for (const path of ["/ui/app.js", "https://example.com/v1/rooms", "//example.com/image", `/s/${a}/v1/rooms`]) assert.equal(A.spaceURL(path), path);
    const values = new Map(), storage = {getItem:key=>values.get(key),setItem:(key,value)=>values.set(key,value),removeItem:key=>values.delete(key)};
    const storageA = A.spaceStorage(storage), storageB = B.spaceStorage(storage);
    storageA.setItem("draft", "A"); storageB.setItem("draft", "B");
    assert.equal(storageA.getItem("draft"), "A"); assert.equal(storageB.getItem("draft"), "B");
    storageA.removeItem("draft"); assert.equal(storageB.getItem("draft"), "B");
    storage.setItem("legacy", "default draft");
    globalThis.fetch = async () => ({ok:true,json:async()=>({can_create:false,default_space_id:a})});
    await A.mountSpacePicker(); await B.mountSpacePicker();
    assert.equal(storageA.getItem("legacy"), "default draft");
    assert.equal(storageB.getItem("legacy"), undefined);
    storageA.removeItem("legacy"); assert.equal(storageA.getItem("legacy"), undefined);
  } finally { globalThis.fetch = previousFetch; if (previous === undefined) delete globalThis.location; else globalThis.location = previous; }
});

test("Space management stays absent while scoped capabilities and brand navigation initialize", async () => {
  const source = await readFile(new URL("../assets/space-context.mjs", import.meta.url), "utf8");
  const a = "a".repeat(32), b = "b".repeat(32);
  for (const canCreate of [true, false]) {
    const brand = {href:"/"}, computer = {disabled:false}, requests = [];
    const capabilities = {computer:false,lark:false,gitlab:true,gitlab_application_management:false};
    const context = {
      location:{pathname:`/s/${b}/`},
      document:{
        querySelectorAll:()=>[brand], getElementById:()=>computer,
        createElement(){ assert.fail("Hidden Space controls must not be created"); },
      },
      fetch:async (url, options) => {
        requests.push([url,options.method || "GET"]);
        return {ok:true,json:async()=>({can_create:canCreate,default_space_id:a,spaces:[{id:a,name:"默认空间",capabilities:{}},{id:b,name:"工作",capabilities}]})};
      },
    };
    vm.runInNewContext(source.replace(/^export /gm, "") + "\nglobalThis.api = {mountSpacePicker,spaceCapabilities,spaceURL};", context);
    await context.api.mountSpacePicker();
    assert.deepEqual(requests,[["/v1/spaces","GET"]]);
    assert.deepEqual(context.api.spaceCapabilities(),capabilities);
    assert.equal(context.api.spaceURL("/v1/rooms"),`/s/${b}/v1/rooms`);
    assert.equal(brand.href,`/s/${b}/`);
    assert.equal(computer.disabled,true);
    assert.doesNotMatch(computer.title,/空间/);
  }
});

test("Loading an archived Space needs no management controls and never reloads itself", async () => {
  const source = await readFile(new URL("../assets/space-loading.mjs", import.meta.url), "utf8");
  const heading = {}, status = {}, reloadButton = {};
  let reloads = 0;
  const context = {
    pageSpaceID:"a".repeat(32), mountSpacePicker:()=>{},
    window:{addEventListener(){}}, location:{reload(){ reloads++; }},
    document:{
      querySelector:()=>heading,
      getElementById:id=>({"space-status":status,"space-reload":reloadButton})[id],
    },
    fetch:async()=>({ok:true,json:async()=>({spaces:[{id:"a".repeat(32),status:"suspended"}]})}),
    setTimeout(){ assert.fail("An archived Space should wait for explicit retry"); },
  };
  reloadButton.addEventListener = (event,callback) => { reloadButton[event] = callback; };
  vm.runInNewContext(source.replace(/^import .*;\n/gm,""),context);
  await new Promise(resolve=>setImmediate(resolve));
  assert.equal(heading.textContent,"会话暂不可用");
  assert.match(status.textContent,/会话与工作文件已保留/);
  assert.doesNotMatch(status.textContent,/空间设置|切换其他空间/);
  assert.equal(reloads,0);
  reloadButton.click(); assert.equal(reloads,1);
});
