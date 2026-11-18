import { test } from "node:test";
import assert from "node:assert/strict";
import { launch, settle, openNotification } from "./web_app_harness.mjs";
import { fixture } from "./web_member_platform_fixture.mjs";
import { captureReturnView } from "../assets/view-return.mjs";
import { createBrowserNavigation } from "../assets/features/browser-navigation.mjs";

function messages(api, author = {kind:"agent",id:"a"}, source) {
  api.hook = path => path === "/v1/read" ? {messages:[{seq:1,author,text:"查看详情",created_at:new Date().toISOString(),source}],snapshot_seq:1,has_more:false} : undefined;
}
const avatar = ui => ui.el("messages").children.find(row => row.id === "message-1").querySelector(".avatar");
const openMember = async ui => {await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();};

test("phone roots preserve the selected conversation, filters, scroll and drafts through profile settings", async () => {
  const api=fixture(),request=api.request.bind(api);
  api.hook=path=>path==="/v1/models" ? {service_epoch:"manager",catalog:{version:1,entries:[]},members:[],operations:[]} : undefined;
  api.request=async(path,...args)=>{const data=await request(path,...args);if(path==="/v1/rooms")for(const room of data.rooms)room.runtime_status="ready";return data;};
  const ui=await launch(api);
  ui.window.innerWidth=390;await ui.window.emit("resize");
  await ui.draft("返回后继续编辑");ui.el("timeline").scrollTop=37;
  await ui.el("mobile-back-to-rooms").emit("click");
  await ui.el("conversation-filter-room").emit("click");
  ui.el("room-nav").scrollTop=80;
  assert.equal(ui.el("conversation-filter").dataset.filtered,"true");
  await ui.el("mobile-tab-profile").emit("click");
  assert.equal(ui.el("mobile-profile").hidden,false);
  assert.equal(ui.el("room-nav").hidden,true);
  assert.equal(ui.el("room-nav").inert,true);
  assert.equal(ui.el("conversation").inert,true);
  assert.equal(ui.el("mobile-tab-profile").getAttribute("aria-current"),"page");
  ui.el("mobile-profile").scrollTop=44;
  for(const tab of ["general","models","platforms","security"]) {
    await ui.el(`profile-${tab}`).emit("click");await settle();
    assert.equal(ui.el("settings-dialog").open,true);
    assert.equal(ui.el(`settings-tab-${tab}`).getAttribute("aria-selected"),"true");
    await ui.el("close-settings").emit("click");await settle();
    assert.equal(ui.el("mobile-profile").hidden,false);
    assert.equal(ui.el("settings-dialog").open,false,`${tab} returns to profile`);
    assert.equal(ui.document.activeElement,ui.el(`profile-${tab}`),`${tab} restores its opener`);
    assert.equal(ui.el("mobile-profile").scrollTop,44);
  }
  await ui.el("mobile-tab-messages").emit("click");
  assert.equal(ui.el("room-nav").hidden,false);
  assert.equal(ui.el("room-nav").scrollTop,80);
  assert.equal(ui.el("conversation-filter-label").textContent,"房间");
  await ui.select("Review");
  assert.equal(ui.el("mobile-primary-navigation").hidden,true);
  assert.equal(ui.el("message-input").value,"返回后继续编辑");
  assert.equal(ui.el("timeline").scrollTop,37);
  assert.equal(api.calls.some(call=>/\/send$|\/save$|\/configure$/.test(call.path)),false);
});

test("profile uses the current display name and the existing authenticated sign-out flow", async () => {
  const api=fixture();let fail=true;
  api.hook=path=>{if(path==="/v1/access/logout"){if(fail)throw Error("offline");return {};}};
  const ui=await launch(api,new Map(),{fetch:async()=>({ok:true,json:async()=>({login_enabled:true})})});
  assert.equal(ui.el("profile-logout").hidden,false);
  ui.el("user-display-name").value="新的名字";
  await ui.el("user-name-form").emit("submit");
  assert.equal(ui.el("mobile-profile-name").textContent,"新的名字");
  await ui.el("profile-logout").emit("click");
  assert.equal(ui.el("profile-feedback").hidden,false);
  assert.equal(ui.el("profile-logout").disabled,false);
  fail=false;let destination="";ui.window.location={assign:value=>destination=value};
  await ui.el("profile-logout").emit("click");
  assert.equal(destination,"/access");
});

for (const width of [390,1440]) {
  test(`member layout choice persists without rebuilding entries at ${width}px`, async () => {
    const api=fixture(),ui=await launch(api);
    ui.window.innerWidth=width;await ui.window.emit("resize");
    if(ui.el("member-rail").inert)await ui.el("room-menu-label").emit("click");
    const toggle=ui.el("toggle-member-list"),entry=ui.el("members").children[0];
    assert.equal(toggle.getAttribute("aria-expanded"),"false");
    assert.equal(ui.el("members").classList.contains("members-compact"),true);
    const calls=api.calls.length;
    await toggle.emit("click");
    assert.equal(toggle.getAttribute("aria-expanded"),"true");
    assert.equal(ui.el("members").classList.contains("members-compact"),false);
    assert.equal(ui.el("members").children[0],entry);assert.equal(api.calls.length,calls);
    await ui.refresh();assert.equal(ui.el("members").children[0],entry);
    const reloaded=await launch(api,ui.saved);
    assert.equal(reloaded.el("toggle-member-list").getAttribute("aria-expanded"),"true");
    await reloaded.el("toggle-member-list").emit("click");
    const compact=await launch(api,reloaded.saved);
    assert.equal(compact.el("members").classList.contains("members-compact"),true);
    if(compact.el("member-rail").inert)await compact.el("room-menu-label").emit("click");
    await openMember(compact);await compact.el("close-activity").emit("click");await settle();
    assert.equal(compact.el("members").classList.contains("members-compact"),true);
    assert.equal(compact.el("member-list-view").hidden,false);
  });

  for (const expanded of [false,true]) test(`avatar return restores the original sidebar and draft at ${width}px, expanded=${expanded}`, async () => {
    const api=fixture();messages(api);const ui=await launch(api);
    ui.window.innerWidth=width;await ui.window.emit("resize");
    const isOpen=()=>ui.el("room-menu-label").getAttribute("aria-expanded")==="true";
    if(isOpen()!==expanded)await ui.el("room-menu-label").emit("click");
    await ui.draft("未发送草稿");ui.el("timeline").scrollTop=37;
    await avatar(ui).emit("click");assert.equal(ui.el("activity-dialog").open,true);
    ui.el("timeline").scrollTop=0;
    await ui.el("close-activity").emit("click");await settle();
    assert.equal(ui.el("activity-dialog").open,false);assert.equal(isOpen(),expanded);
    assert.equal(ui.el("timeline").scrollTop,37);assert.equal(ui.el("message-input").value,"未发送草稿");
    assert.equal(api.calls.filter(call=>/\/send|\/configure|\/change|\/login$/.test(call.path)).length,0);
  });

  test(`member list return retains the list and its scroll position at ${width}px`, async () => {
    const ui=await launch(fixture());ui.window.innerWidth=width;await ui.window.emit("resize");
    if(ui.el("member-rail").inert)await ui.el("room-menu-label").emit("click");
    ui.el("member-sidebar").scrollTop=92;await openMember(ui);
    await ui.el("close-activity").emit("click");await settle();
    assert.equal(ui.el("member-rail").inert,false);assert.equal(ui.el("member-list-view").hidden,false);
    assert.equal(ui.el("member-sidebar").scrollTop,92);
  });

  test(`historical platform messages have no settings action at ${width}px`, async () => {
    const api=fixture();messages(api,{kind:"integration",id:"gitlab"},{provider:"gitlab",channel:"events"});
    const ui=await launch(api);ui.window.innerWidth=width;await ui.window.emit("resize");
    await avatar(ui).emit("click");
    assert.equal(ui.el("notification-detail").open,false);
  });
}

test("notification source and global settings return through the actual parent with its tab and scroll", async () => {
  const ui=await launch(fixture());await openMember(ui);await ui.el("agent-tab-inbox").emit("click");await settle();
  ui.el("inbox-member-view").scrollTop=88;await openNotification(ui,"gitlab","gl",true);
  ui.el("notification-detail").scrollTop=55;
  ui.el("gitlab-manage-platforms").focus();await ui.el("gitlab-manage-platforms").emit("click");
  await ui.el("close-settings").emit("click");await settle();
  assert.equal(ui.el("notification-detail").open,true);assert.equal(ui.el("notification-detail").scrollTop,55);
  assert.equal(ui.document.activeElement,ui.el("gitlab-manage-platforms"));
  await ui.el("close-notification").emit("click");await settle();
  assert.equal(ui.el("activity-dialog").open,true);assert.equal(ui.el("activity-dialog").hidden,false);
  assert.equal(ui.el("agent-tab-inbox").getAttribute("aria-selected"),"true");assert.equal(ui.el("inbox-member-view").scrollTop,88);
});


test("an avatar opened over another member detail restores that member and its tab", async () => {
  const api=fixture();messages(api);const ui=await launch(api);
  await ui.el("members").children[1].querySelector(".member-summary").emit("click");
  await ui.el("agent-tab-inbox").emit("click");await settle();await ui.el("inbox-next").emit("click");await settle();
  ui.el("inbox-member-view").scrollTop=63;
  const page=ui.el("inbox-page").textContent,first=ui.el("inbox-entries").children[0];
  await avatar(ui).emit("click");assert.equal(ui.selectedAgent().agent_id,"a");
  await ui.el("close-activity").emit("click");await settle();
  assert.equal(ui.selectedAgent().agent_id,"b");assert.equal(ui.el("agent-tab-inbox").getAttribute("aria-selected"),"true");
  assert.equal(ui.el("inbox-member-view").scrollTop,63);
  assert.equal(ui.el("inbox-page").textContent,page);assert.equal(ui.el("inbox-entries").children[0],first);
  await ui.el("close-activity").emit("click");await settle();assert.equal(ui.el("member-list-view").hidden,false);
});


test("opening the same member from a conversation restores its previous tab on return", async () => {
  const api=fixture();messages(api);const ui=await launch(api);await openMember(ui);
  await ui.el("agent-tab-inbox").emit("click");await settle();ui.el("inbox-member-view").scrollTop=42;
  await avatar(ui).emit("click");assert.equal(ui.el("agent-tab-platforms").getAttribute("aria-selected"),"true");
  await avatar(ui).emit("click");
  await ui.el("close-activity").emit("click");await settle();
  assert.equal(ui.el("activity-dialog").open,true);assert.equal(ui.el("agent-tab-inbox").getAttribute("aria-selected"),"true");
  assert.equal(ui.el("inbox-member-view").scrollTop,42);
});

test("a platform connection opened from a member returns directly to that member", async () => {
  const ui=await launch(fixture());await openMember(ui);ui.el("platforms-member-view").scrollTop=71;
  await ui.el("notification-add-platform").emit("click");await settle();
  assert.equal(ui.el("settings-dialog").open,true);assert.equal(ui.el("connection-form").hidden,false);
  await ui.el("connection-back").emit("click");await settle();
  assert.equal(ui.el("settings-dialog").open,false);assert.equal(ui.el("activity-dialog").open,true);
  assert.equal(ui.el("platforms-member-view").scrollTop,71);
});

test("Workspace platform management restores the existing Workspace form", async () => {
  const ui=await launch(fixture());await ui.el("open-workspace").emit("click");
  ui.el("workspace-repository").value="https://gitlab.invalid/group/project.git";
  await ui.el("workspace-manage-platforms").emit("click");
  assert.equal(ui.el("workspace-dialog").open,true);assert.equal(ui.el("settings-dialog").open,true);
  await ui.el("close-settings").emit("click");await settle();
  assert.equal(ui.el("workspace-dialog").open,true);assert.equal(ui.el("workspace-repository").value,"https://gitlab.invalid/group/project.git");
  assert.equal(ui.document.activeElement,ui.el("workspace-manage-platforms"));
});

test("return snapshots reject another Room, operator, installation or service epoch", () => {
  let focused=0;const element={scrollTop:37,focus(){focused++;}},state={operator:"a",installationID:"one",catalog:{service_epoch:"manager"},info:{room_id:"R",room_instance:"r",service_epoch:"runtime"}};
  const env={document:{activeElement:element,getElementById:id=>id==="timeline" ? element : null},window:{}};
  for(const [object,key,value] of [[state.info,"room_instance","replacement"],[state,"operator","b"],[state,"installationID","two"],[state.catalog,"service_epoch","restarted"]]){
    const back=captureReturnView(env,{state}),old=object[key];object[key]=value;element.scrollTop=0;
    assert.equal(back.restore(),false);assert.equal(element.scrollTop,0);object[key]=old;
  }
  assert.equal(focused,0);
});

test("return snapshots leave hidden root scroll positions intact when those roots become visible", () => {
  for(const hiddenByCSS of [false,true]) {
    const list={scrollTop:0,hidden:!hiddenByCSS,getClientRects:()=>hiddenByCSS ? [] : [{}]};
    const timeline={scrollTop:37,getClientRects:()=>[{}]};
    const env={document:{getElementById:id=>id==="room-nav" ? list : id==="timeline" ? timeline : null},window:{}};
    const back=captureReturnView(env,{state:{}});
    list.hidden=false;list.getClientRects=()=>[{}];list.scrollTop=120;timeline.scrollTop=90;
    assert.equal(back.restore(),true);
    assert.equal(list.scrollTop,120);assert.equal(timeline.scrollTop,37);
  }
});

test("browser Back awaits child dismissal, restores each parent and respects a blocked return", async () => {
  let pop,state=null,pushes=0,backs=0,blocked=true;
  const layers=[],window={history:{get state(){return state;},replaceState(value){state=value;},pushState(value){state=value;pushes++;},back(){backs++;queueMicrotask(()=>pop());}},addEventListener(name,fn){if(name==="popstate")pop=fn;}};
  const navigation=createBrowserNavigation({env:{window},layers:()=>layers});navigation.mount();
  layers.push({id:"settings",close:()=>layers.pop()});navigation.sync();
  layers.push({id:"editor",close:async()=>{await Promise.resolve();if(!blocked)layers.pop();}});navigation.sync();
  assert.equal(backs,0);assert.equal(pushes,1);assert.equal(state.teamxOverlay,"editor");
  await pop();assert.equal(layers.length,2);assert.equal(state.teamxOverlay,"editor");
  blocked=false;await pop();assert.equal(layers.length,1);assert.equal(state.teamxOverlay,"settings");
  await pop();assert.equal(layers.length,0);await pop();assert.equal(layers.length,0);
});

test("startup navigation waits for the initial phone homepage before changing browser history", () => {
  let pushes=0,backs=0;
  let layers=[{id:"mobile-conversation",close(){}}];
  const window={history:{pushState(){pushes++;},back(){backs++;}},addEventListener(){}};
  const navigation=createBrowserNavigation({env:{window},layers:()=>layers});
  navigation.sync();layers=[];navigation.sync();navigation.mount();
  assert.equal(pushes,0);assert.equal(backs,0);
  layers=[{id:"mobile-profile",close(){}}];navigation.sync();
  assert.equal(pushes,1);
});
