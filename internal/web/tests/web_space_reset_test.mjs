import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import vm from "node:vm";
import { addDialogClose } from "../assets/dialogs.mjs";

const source = (await readFile(new URL("../assets/space-reset.mjs", import.meta.url), "utf8"))
  .replace(/^import .*;\n/gm, "").replace("export function mountSpaceReset", "function mountSpaceReset");

function surface(capabilities, record) {
  class Element {
    children=[]; events={}; textContent=""; dataset={};
    append(...items) { this.children.push(...items); }
    setAttribute() {}
    addEventListener(name,fn) { this.events[name]=fn; }
    close() { this.open=false; }
    showModal() { this.open=true; }
  }
  const main=new Element(),body=new Element(),values=new Map(),writes=[],navigation=[];
  const id="a".repeat(32),timers=[];
  let frozen=0;
  const context={
    pageSpaceID:id,spaceCapabilities:()=>capabilities,observeSpaceReset:()=>true,addDialogClose,
    freezeSpaceCache:()=>frozen++,AbortSignal,crypto:{randomUUID:()=>"b".repeat(32)},
    document:{body,getElementById:name=>name==="general-settings"?main:null,querySelector:()=>main,createElement:()=>new Element(),createElementNS:()=>new Element()},
    window:{addEventListener(){}},location:{replace:path=>navigation.push(path)},
    sessionStorage:{getItem:key=>values.get(key),setItem:(key,value)=>values.set(key,value),removeItem:key=>values.delete(key)},
    setTimeout:fn=>timers.push(fn),
    fetch:async(path,options)=>{
      if(options?.method==="POST") {
        writes.push(JSON.parse(options.body));
        return {ok:writes.length>1,json:async()=>({message:"连接中断"})};
      }
      return {ok:true,json:async()=>({spaces:[{id,version:7,status:"active",runtime_state:"ready",capabilities,...record}]})};
    },
  };
  vm.runInNewContext(source+"\nmountSpaceReset();",context);
  return {main,body,writes,navigation,values,frozen:()=>frozen};
}
const settle=()=>new Promise(resolve=>setImmediate(resolve));

test("hosted reset preserves the original request after uncertainty and describes private login clearing",async()=>{
  const ui=surface({space_reset:true,reset_default_model:true,native_lark_login_preserved:false});
  await settle();
  const [button,status]=ui.main.children[0].children;
  const dialog=ui.body.children[0],detail=dialog.children[1],confirm=dialog.children[3];
  assert.match(detail.textContent,/平台连接/);
  assert.doesNotMatch(detail.textContent,/系统保存的飞书登录会保留/);
  button.events.click();assert.equal(dialog.open,true);
  await confirm.events.click();
  assert.match(status.textContent,/保留原请求/);
  assert.equal(ui.writes.length,1);assert.equal(ui.frozen(),0);
  button.events.click();await settle();
  assert.equal(ui.writes.length,2);
  assert.deepEqual(ui.writes[1],ui.writes[0]);
  assert.equal(ui.frozen(),1);assert.deepEqual(ui.navigation,[`/s/${"a".repeat(32)}/`]);
});

test("hosted model failure reports completed reset and leaves the default model feedback in setup",async()=>{
  const ui=surface({space_reset:true,reset_default_model:true},{runtime_state:"setup_required",reset:{phase:"completed",request_id:"c".repeat(32)}});
  await settle();
  const status=ui.main.children[0].children[1];
  assert.match(status.textContent,/重置已完成，默认模型尚未就绪/);
  assert.equal(ui.writes.length,0);
});

test("personal macOS reset explicitly preserves native Feishu login",async()=>{
  const ui=surface({space_reset:true,native_lark_login_preserved:true});
  await settle();
  assert.match(ui.body.children[0].children[1].textContent,/系统保存的飞书登录会保留/);
});
