import { test } from "node:test";
import assert from "node:assert/strict";
import { launch, settle } from "./web_app_harness.mjs";
import { fixture } from "./web_member_platform_fixture.mjs";
import { createBrowserNavigation } from "../assets/features/browser-navigation.mjs";
import { createRoomDrafts } from "../assets/client.mjs";

test("installation changes isolate drafts, including late receipts from the previous instance", () => {
  const saved = new Map(), storage = {getItem:key=>saved.get(key),setItem:(key,value)=>saved.set(key,value)};
  let installation = "first";
  const drafts = createRoomDrafts(storage,()=>installation), info={room_instance:"same-room"};
  const first = drafts.get(info,"human"); first.text="first device draft"; drafts.save(info,"human",first);
  installation="second";
  const second = drafts.get(info,"human"); assert.equal(second.text,"");
  second.text="second instance draft"; drafts.save(info,"human",second);
  first.text="late old receipt"; drafts.save(info,"human",first);
  assert.equal(drafts.get(info,"human").text,"second instance draft");
  installation="first"; assert.equal(drafts.get(info,"human").text,"late old receipt");
});

test("hidden, offline and bfcache pages retain drafts, suspend reads and reconcile before sending", async () => {
  const api = fixture();
  let poll;
  const ui = await launch(api, new Map(), { setInterval: fn => { poll = fn; } });
  await ui.draft("手机上的未发送草稿");
  ui.document.hidden = true;
  await ui.document.emit("visibilitychange");
  const before = api.calls.length;
  poll(); await ui.clock.advance(5000); await ui.refresh();
  assert.equal(api.calls.length, before);
  assert.equal(ui.el("send").disabled, true);
  ui.document.hidden = false;
  await ui.document.emit("visibilitychange"); await settle();
  assert.equal(ui.el("message-input").value, "手机上的未发送草稿");
  assert.equal(ui.el("send").disabled, false);
  ui.window.navigator = { onLine:false };
  await ui.window.emit("offline");
  assert.match(ui.el("connection").textContent, /网络已断开/);
  assert.equal(ui.el("sync-error").hidden,false);
  assert.match(ui.el("sync-error").textContent,/网络已断开/);
  const offline = api.calls.length;
  poll(); await settle(); assert.equal(api.calls.length, offline);
  ui.window.navigator.onLine = true;
  let release;
  api.hook = path => path === "/v1/ui" ? new Promise(resolve => { release = () => resolve({managed:true}); }) : undefined;
  await ui.window.emit("online"); await settle();
  assert.equal(ui.el("send").disabled, true);
  release(); api.hook = null; await settle();
  assert.equal(ui.el("send").disabled, false);
  assert.equal(ui.el("sync-error").hidden,true);
  await ui.window.emit("pagehide");
  const hidden = api.calls.length; poll(); await settle(); assert.equal(api.calls.length, hidden);
  await ui.window.emit("pageshow"); await settle();
  assert.ok(api.calls.length > hidden);
  assert.equal(api.calls.filter(c => /\/send|\/cancel/.test(c.path)).length, 0);
});

test("unknown sends survive refresh and recovery without any automatic replay", async () => {
  const api = fixture(), saved = new Map();
  api.hook = path => { if (path === "/v1/send") throw Error("response lost"); };
  const ui = await launch(api, saved);
  await ui.draft("只发送一次"); await ui.el("compose").emit("submit");
  const pending = api.calls.find(c => c.path === "/v1/send").body;
  await ui.window.emit("pagehide"); await ui.window.emit("pageshow"); await settle();
  const reopened = await launch(api, saved);
  assert.match(reopened.el("send").textContent, /重试发送/);
  assert.equal(api.calls.filter(c => c.path === "/v1/send").length, 1);
  await reopened.el("compose").emit("submit");
  assert.deepEqual(api.calls.filter(c => c.path === "/v1/send").at(-1).body, pending);
});

test("foreground recovery settles and ignores a read begun before the page was hidden", async () => {
  const api = fixture(), ui = await launch(api);
  let release, delay = true;
  api.hook = path => {
    if (path === "/v1/read" && delay) return new Promise(resolve => {
      release = () => resolve({messages:[{seq:99,text:"stale",author:{kind:"human",id:"tester"},created_at:new Date().toISOString()}],snapshot_seq:99});
    });
  };
  const reading = ui.refresh(); await settle();
  ui.document.hidden = true; await ui.document.emit("visibilitychange");
  ui.document.hidden = false; await ui.document.emit("visibilitychange");
  assert.equal(ui.el("send").disabled, true);
  delay = false; release(); await reading; await settle();
  assert.equal(ui.el("messages").children.length, 0);
  assert.match(ui.el("connection").textContent, /已连接/);
});

test("phone Room sidebars overlay independently of desktop preferences and retain the composer", async () => {
  const ui = await launch(fixture());
  await ui.draft("旋转后继续编辑");
  ui.window.innerWidth = 390; ui.window.innerHeight = 720;
  ui.window.visualViewport = { height:380, offsetTop:0, scale:1 };
  await ui.window.emit("resize");
  assert.equal(ui.el("toggle-rooms").getAttribute("aria-expanded"), "false");
  assert.equal(ui.el("toggle-members").getAttribute("aria-expanded"), "false");
  assert.equal(ui.el("workspace").style.getPropertyValue("--app-height"), "380px");
  await ui.el("toggle-rooms").emit("click"); assert.equal(ui.el("conversation").inert, true);
  await ui.el("toggle-members").emit("click");
  assert.equal(ui.el("toggle-rooms").getAttribute("aria-expanded"), "false");
  await ui.el("close-mobile-members").emit("click");
  assert.equal(ui.el("conversation").inert, false);
  ui.window.innerWidth = 1440; await ui.window.emit("resize");
  assert.equal(ui.el("toggle-members").getAttribute("aria-expanded"), "true");
  assert.equal(ui.el("message-input").value, "旋转后继续编辑");
});

test("IME confirmation including compositionend before Enter never sends", async () => {
  const api = fixture(), ui = await launch(api);
  api.hook = path => path === "/v1/send" ? {seq:1} : undefined;
  await ui.draft("中文消息");
  await ui.el("message-input").emit("compositionstart");
  await ui.el("compose").emit("submit");
  await ui.el("message-input").emit("keydown", {key:"Enter"});
  await ui.el("message-input").emit("compositionend");
  await ui.el("message-input").emit("keydown", {key:"Enter"});
  assert.equal(api.calls.filter(c => c.path === "/v1/send").length, 0);
  await ui.clock.advance(60);
  await ui.el("message-input").emit("keydown", {key:"Enter"}); await settle();
  assert.equal(api.calls.filter(c => c.path === "/v1/send").length, 1);
});

test("browser Back closes a transient surface; Forward never recreates a write or form", async () => {
  let layer = null, pop, state = null, backs = 0, pushes = 0;
  const window = { history:{ get state(){return state;}, replaceState(value){state=value;},
    pushState(value){state=value; pushes++;}, back(){ backs++; queueMicrotask(() => pop()); } },
    addEventListener(name, fn){if(name === "popstate") pop=fn;} };
  const nav = createBrowserNavigation({env:{window}, layers:() => layer ? [layer] : []});
  nav.mount(); layer = {id:"settings", close:() => {layer=null;}}; nav.sync();
  assert.equal(pushes, 1); assert.deepEqual(state, {teamxOverlay:"settings"});
  await pop(); assert.equal(layer, null);
  await pop(); assert.equal(layer, null);
  layer = {id:"members", close:() => {layer=null;}}; nav.sync();
  layer = null; nav.sync(); await settle(); assert.equal(backs, 1);
});
