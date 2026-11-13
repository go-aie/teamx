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

for (const width of [390,1440]) {
  for (const expanded of [false,true]) test(`avatar return restores the original sidebar and draft at ${width}px, expanded=${expanded}`, async () => {
    const api=fixture();messages(api);const ui=await launch(api);
    ui.window.innerWidth=width;await ui.window.emit("resize");
    const isOpen=()=>ui.el("toggle-members").getAttribute("aria-expanded")==="true";
    if(isOpen()!==expanded)await ui.el("toggle-members").emit("click");
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
    if(ui.el("member-rail").inert)await ui.el("toggle-members").emit("click");
    ui.el("member-sidebar").scrollTop=92;await openMember(ui);
    await ui.el("close-activity").emit("click");await settle();
    assert.equal(ui.el("member-rail").inert,false);assert.equal(ui.el("member-list-view").hidden,false);
    assert.equal(ui.el("member-sidebar").scrollTop,92);
  });

  test(`notification message diagnostics return directly to the conversation at ${width}px`, async () => {
    const api=fixture();messages(api,{kind:"integration",id:"gitlab"},{provider:"gitlab",channel:"events"});
    const ui=await launch(api);ui.window.innerWidth=width;await ui.window.emit("resize");
    if(!ui.el("member-rail").inert)await ui.el("toggle-members").emit("click");
    await avatar(ui).emit("click");await settle();assert.equal(ui.el("notification-detail").open,true);
    assert.equal(ui.el("member-rail").inert,false);
    await ui.el("close-notification").emit("click");await settle();
    assert.equal(ui.el("member-rail").inert,true);assert.equal(ui.el("conversation").inert,false);
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

for (const width of [390,1440]) test(`conversation notifications return to the conversation when details were closed at ${width}px`, async () => {
  const api=fixture(),request=api.request.bind(api);
  api.request=async(path,...args)=>{
    const data=await request(path,...args);
    if(path==="/v1/rooms")for(const room of data.rooms){room.is_direct=true;room.runtime_status="ready";}
    if(path==="/v1/member/list")data.members=data.members.slice(0,1);
    return data;
  };
  const ui=await launch(api);ui.window.innerWidth=width;await ui.window.emit("resize");
  if(!ui.el("member-rail").inert)await ui.el("direct-details").emit("click");
  await ui.el("direct-notifications").emit("click");assert.equal(ui.el("member-rail").inert,false);
  await ui.el("close-conversation-notifications").emit("click");await settle();
  assert.equal(ui.el("member-rail").inert,true);assert.equal(ui.el("conversation").inert,false);
  assert.equal(ui.el("activity-dialog").open,false);
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

test("returning from historical notification diagnostics resumes the original activity reader", async () => {
  const api=fixture();messages(api,{kind:"integration",id:"gitlab"},{provider:"gitlab",channel:"events"});
  const ui=await launch(api);await openMember(ui);await ui.el("agent-tab-activity").emit("click");await settle();
  const reads=()=>api.calls.filter(call=>call.path==="/v1/member/activity").length;
  const before=reads();await avatar(ui).emit("click");await settle();
  await ui.el("close-notification").emit("click");await settle();
  assert.equal(ui.el("activity-dialog").open,true);assert.equal(ui.el("activity-dialog").hidden,false);
  assert.equal(ui.el("agent-tab-activity").getAttribute("aria-selected"),"true");assert.ok(reads()>before);
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
