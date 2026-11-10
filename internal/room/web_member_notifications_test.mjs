import {test} from "node:test";
import assert from "node:assert/strict";
import {launch,settle,openMemberInbox,openNotification} from "./web_app_harness.mjs";

import {fixture,target,key} from "./web_member_platform_fixture.mjs";
const copy=structuredClone;
const list=(ui)=>ui.el("member-notification-platforms");
const writes=api=>api.calls.filter(c=>c.path.endsWith("notifications/configure"));
const reads=api=>api.calls.filter(c=>c.path==="/v1/member/inbox");
async function switchMember(ui,id){await ui.el("close-notification").emit("click");await openMemberInbox(ui,`member-${id}`);}

test("M4 member sources are scoped; the room list offers only GitLab and allows shared platforms",async()=>{
  const api=fixture(),ui=await launch(api);await openMemberInbox(ui,"member-a");
  assert.equal(list(ui).children.length,2);assert.equal(ui.el("notification-platforms").children.length,1);
  assert.equal(ui.el("member-notification-source-count").textContent,"2");
  assert.equal(ui.el("notification-add-options").children.some(b=>b.dataset.provider==="lark"),false);
  await switchMember(ui,"b");
  assert.equal(list(ui).children.length,1);
  assert.ok(ui.el("member-notification-add-options").children.some(b=>b.dataset.platformId==="lark"));
  await openNotification(ui,"gitlab","gl","member-b");
  assert.equal(ui.el("notification-title").textContent,"b · 公司 GitLab 设置");
  assert.equal(ui.el("gitlab-interval").value,45);
  assert.match(ui.el("notification-destination").textContent,/发送到 b 的收件箱.*同时看到/);
  assert.equal(writes(api).length,0);
});

test("M4 settings, toggles and readback preserve the Inbox snapshot, cursors, scroll and expansion",async()=>{
  const api=fixture(),ui=await launch(api);await openMemberInbox(ui,"member-a");await ui.el("inbox-next").emit("click");
  ui.el("inbox-member-view").scrollTop=420;const entries=ui.el("inbox-entries").children,before=reads(api).length;
  await ui.el("member-notification-add-toggle").emit("click");assert.equal(ui.el("member-notification-add-toggle").attributes["aria-expanded"],"true");
  await openNotification(ui,"gitlab","gl",true);
  ui.el("gitlab-interval").value="72";await ui.el("gitlab-interval").emit("input");
  ui.notificationToggle().checked=false;await ui.notificationToggle().emit("change");
  assert.equal(recordsValue(api,"gl","a").config.interval_seconds,30);
  assert.equal(ui.el("gitlab-interval").value,72);
  await ui.el("gitlab-save").emit("click");await ui.el("notification-reload").emit("click");await ui.el("close-notification").emit("click");
  assert.equal(ui.el("activity-dialog").open,true);assert.equal(ui.el("activity-dialog").hidden,false);
  assert.equal(ui.el("agent-tab-inbox").attributes["aria-selected"],"true");
  assert.equal(ui.el("inbox-page").textContent,"第 2 页");assert.equal(ui.el("inbox-member-view").scrollTop,420);
  assert.equal(ui.el("inbox-entries").children,entries);assert.equal(reads(api).length,before);
  assert.equal(ui.el("member-notification-add-toggle").attributes["aria-expanded"],"true");
  await ui.el("inbox-prev").emit("click");assert.equal(reads(api).at(-1).body.snapshot_seq,50);assert.equal(reads(api).at(-1).body.after,25);
  assert.equal(api.calls.some(c=>/member\/(run|enable)|inbox\/(read|confirm)/.test(c.path)),false);
});
function recordsValue(api,id,member){return api.records.get(key(id,target(member)));}

test("M4 each member keeps its own platform draft and quick switches use confirmed configuration",async()=>{
  const api=fixture(),ui=await launch(api);await openNotification(ui,"gitlab","gl","member-a");
  ui.el("gitlab-interval").value="72";await ui.el("gitlab-interval").emit("input");
  await switchMember(ui,"b");await openNotification(ui,"gitlab","gl",true);
  assert.equal(ui.el("gitlab-interval").value,45);
  ui.notificationToggle().checked=false;await ui.notificationToggle().emit("change");
  assert.equal(recordsValue(api,"gl","b").config.enabled,false);assert.equal(recordsValue(api,"gl","a").config.enabled,true);
  assert.equal(api.records.get(key("gl",{})).config.enabled,true);
  await switchMember(ui,"a");await openNotification(ui,"gitlab","gl",true);
  assert.equal(ui.el("gitlab-interval").value,72);assert.equal(ui.notificationToggle().checked,true);
});

test("M4 failed quick switches roll back and unknown results read the exact target without replay",async()=>{
  const api=fixture(),ui=await launch(api);await openMemberInbox(ui,"member-b");let finish;
  api.hook=(path,body)=>path.endsWith("notifications/configure") ? new Promise((resolve,reject)=>finish={resolve,reject,body}) : undefined;
  const toggle=list(ui).children[0].children[1].children[1];toggle.checked=false;const pending=toggle.emit("change");await settle();
  assert.equal(list(ui).children[0].children[1].children[1].disabled,true);await toggle.emit("change");assert.equal(writes(api).length,1);
  finish.reject(Object.assign(Error("bad config"),{code:"request"}));await pending;
  assert.equal(list(ui).children[0].children[1].children[1].checked,true);
  const next=list(ui).children[0].children[1].children[1];next.checked=false;const unknown=next.emit("change");await settle();
  recordsValue(api,"gl","b").config.enabled=false;recordsValue(api,"gl","b").version++;finish.reject(Error("lost reply"));await unknown;
  assert.equal(list(ui).children[0].children[1].children[1].checked,false);assert.equal(writes(api).length,2);assert.equal(recordsValue(api,"gl","a").config.enabled,true);
});

test("M4 late member save and single-config read never replace another member's form",async()=>{
  const api=fixture(),ui=await launch(api);await openNotification(ui,"gitlab","gl","member-a");let finish;
  api.hook=(path)=>path.endsWith("notifications/configure") ? new Promise(resolve=>finish=resolve) : undefined;
  ui.el("gitlab-interval").value="72";await ui.el("gitlab-interval").emit("input");const save=ui.el("gitlab-save").emit("click");await settle();
  await switchMember(ui,"b");await openNotification(ui,"gitlab","gl",true);
  finish(copy(recordsValue(api,"gl","a")));await save;
  assert.equal(ui.el("notification-title").textContent,"b · 公司 GitLab 设置");assert.equal(ui.el("gitlab-interval").value,45);assert.notEqual(ui.el("gitlab-save").textContent,"保存中…");
  await switchMember(ui,"a");
  api.hook=path=>path.startsWith("/v1/platform/gitlab/notifications?") ? new Promise(resolve=>finish=resolve) : undefined;
  const opening=openNotification(ui,"gitlab","gl",true);await settle();
  await switchMember(ui,"b");api.hook=null;await openNotification(ui,"gitlab","gl",true);
  finish(copy(recordsValue(api,"gl","a")));await opening;
  assert.equal(ui.el("gitlab-interval").value,45);assert.equal(ui.el("notification-panel-gitlab").hidden,false);
});

test("M4 wrong-target read hides every old form until an exact retry succeeds",async()=>{
  const api=fixture(),ui=await launch(api);await openNotification(ui,"gitlab","gl","member-a");
  await switchMember(ui,"b");api.hook=path=>path.startsWith("/v1/platform/gitlab/notifications?") ? copy(recordsValue(api,"gl","a")) : undefined;
  await openNotification(ui,"gitlab","gl",true);
  assert.equal(ui.el("notification-panel-gitlab").hidden,true);assert.equal(ui.el("gitlab-save").disabled,true);
  assert.equal(ui.el("notification-title").textContent,"b · 公司 GitLab 设置");
  api.hook=null;await ui.el("notification-reload").emit("click");assert.equal(ui.el("gitlab-interval").value,45);
});

test("M4 Lark settings and sync send exact member, subscription and version",async()=>{
  const api=fixture(),ui=await launch(api);await openNotification(ui,"lark","lark","member-a");
  ui.el("lark-interval").value="90";await ui.el("lark-interval").emit("change");
  ui.notificationToggle().checked=false;await ui.notificationToggle().emit("change");
  const saved=writes(api).at(-1).body;assert.deepEqual(saved.config.target,target("a"));assert.equal(saved.config.interval_seconds,60);assert.equal(saved.subscription_id,"sub-lark-a");assert.equal(saved.subscription_version,3);
  assert.equal(Number(ui.el("lark-interval").value),90);await ui.el("lark-save").emit("click");assert.equal(writes(api).at(-1).body.config.interval_seconds,90);
  ui.notificationToggle().checked=true;await ui.notificationToggle().emit("change");await ui.el("lark-sync").emit("click");
  assert.deepEqual(api.calls.find(c=>c.path.endsWith("notifications/sync")).body.target,target("a"));
});

test("M4 source picker searches accounts and initializes B without granting tools or copying A",async()=>{
  const api=fixture(),ui=await launch(api);await openMemberInbox(ui,"member-b");
  ui.el("member-notification-add-search").value="Alice";await ui.el("member-notification-add-search").emit("input");
  assert.deepEqual(ui.el("member-notification-add-options").children.map(b=>b.dataset.platformId),["lark"]);
  await openNotification(ui,"lark","lark",true);assert.match(ui.el("notification-destination").textContent,/b/);
  assert.equal(ui.el("lark-save").disabled,false);await ui.el("lark-save").emit("click");
  assert.equal(writes(api).at(-1).body.subscription_version,0);assert.deepEqual(writes(api).at(-1).body.config.target,target("b"));
  assert.equal(recordsValue(api,"lark","a").subscription_version,3);assert.equal(api.calls.some(c=>c.path==="/v1/tools/bind"),false);
});

test("M4 empty states distinguish no sources, paused sources and source failure",async()=>{
  const api=fixture({empty:true});api.hook=path=>path==="/v1/member/inbox" ? {snapshot_seq:0,entries:[],has_more:false,status:{latest_seq:0,limits:{bytes:1000},bytes:0}} : undefined;
  const ui=await launch(api);await openMemberInbox(ui,"member-a");assert.match(ui.el("inbox-empty-hint").textContent,/添加平台/);
  const populated=fixture();populated.hook=api.hook;for(const v of populated.records.values())v.config.enabled=false;
  const paused=await launch(populated);await openMemberInbox(paused,"member-a");assert.match(paused.el("inbox-empty-hint").textContent,/已暂停/);
  recordsValue(populated,"gl","a").target_error_code="target_removed";await paused.refresh();assert.match(paused.el("inbox-empty-hint").textContent,/异常/);
});

test("M4 Inbox source navigation resolves SubscriptionID and legacy sources explain missing identity",async()=>{
  const api=fixture(),ui=await launch(api);await openMemberInbox(ui,"member-b");
  const details=ui.el("inbox-entries").children[0].children.at(-1),button=details.children.find(c=>c.tag==="button");await button.emit("click");
  assert.equal(ui.el("notification-title").textContent,"b · 公司 GitLab 设置");assert.equal(ui.el("gitlab-interval").value,45);
  assert.ok(api.calls.some(c=>c.path.includes("subscription_id=sub-gl-member-b")));
  await ui.el("close-notification").emit("click");
  api.hook=path=>path.startsWith("/v1/platform/notifications?") && path.includes("subscription_id") ? {subscriptions:[],retired_subscriptions:[]} : undefined;
  await button.emit("click");assert.equal(ui.el("notification-panel-gitlab").hidden,true);assert.match(ui.el("notification-notice").textContent,/唯一定位/);
});

test("M4 service epoch discards cached drafts and late responses",async()=>{
  const api=fixture(),ui=await launch(api);await openNotification(ui,"gitlab","gl","member-a");
  ui.el("gitlab-interval").value="72";await ui.el("gitlab-interval").emit("input");
  api.epoch="next-manager";await ui.refresh();await openNotification(ui,"gitlab","gl","member-a");assert.equal(ui.el("gitlab-interval").value,30);
});

for(const provider of ["gitlab","lark"])test(`M4 ${provider} returning to a saving scope blocks duplicate writes`,async()=>{
  const api=fixture(),ui=await launch(api),id=provider==="lark" ? "lark" : "gl";await openNotification(ui,provider,id,"member-a");let finish;
  api.hook=path=>path.endsWith("notifications/configure") ? new Promise(resolve=>finish=resolve) : undefined;
  ui.notificationToggle().checked=false;const pending=ui.notificationToggle().emit("change");await settle();
  await switchMember(ui,"b");await openNotification(ui,"gitlab","gl",true);
  await switchMember(ui,"a");await openNotification(ui,provider,id,true);
  assert.equal(ui.notificationToggle().disabled,true);await ui.notificationToggle().emit("change");assert.equal(writes(api).length,1);
  finish(copy(recordsValue(api,id,"a")));await pending;
  assert.equal(ui.notificationToggle().disabled,false);
});

for(const removed of [false,true])test(`M4 unfinished onboarding details locate original B independently of current A (removed=${removed})`,async()=>{
  const api=fixture(),operation={schema:2,operation_id:"f".repeat(32),platform_id:"gl",input:{provider:"gitlab",room_id:"R",room_instance:"instance-R",agent_id:"b",membership_id:"member-b"},connection:{status:"connected"},notifications:{status:"failed",error_code:"settings_changed"},tool:{status:"applied",version:1}};
  api.hook=path=>path==="/v1/platforms/onboarding/status" ? copy(operation) : undefined;
  const saved=new Map([["teamx.platform-onboarding:tester",JSON.stringify({operation_id:operation.operation_id})]]),ui=await launch(api,saved);
  await openMemberInbox(ui,"member-a");await ui.el("open-settings").emit("click");
  assert.equal(ui.el("onboarding-notification-settings").hidden,false);
  if(removed){api.members.splice(1,1,{agent_id:"b",membership_id:"replacement-b",runner:{profile:"p"}});await ui.refresh();}
  await ui.el("onboarding-notification-settings").emit("click");await settle();
  if(removed){assert.equal(ui.el("settings-dialog").open,true);assert.match(ui.el("onboarding-result-error").textContent,/移除|替换/);assert.equal(ui.el("notification-detail").open,false);}
  else {assert.equal(ui.el("notification-title").textContent,"b · 公司 GitLab 设置");assert.equal(ui.selectedAgent()?.membership_id,"member-b");await ui.el("close-notification").emit("click");assert.equal(ui.el("agent-tab-platforms").attributes["aria-selected"],"true");}
  assert.equal(writes(api).length,0);
});

test("M4 removed-member diagnostics retain the old target and can pause it",async()=>{
  const api=fixture();api.members.splice(1,1);const ui=await launch(api);
  assert.equal(ui.el("notification-orphans").hidden,false);assert.equal(ui.el("notification-orphan-items").children.length,1);
  await ui.el("notification-orphan-items").children[0].children[0].emit("click");assert.match(ui.el("notification-destination").textContent,/原成员已移除/);
  ui.notificationToggle().checked=false;await ui.notificationToggle().emit("change");
  assert.deepEqual(writes(api)[0].body.config.target,target("b"));assert.equal(recordsValue(api,"gl","a").config.enabled,true);
});


test("M4 Room runtime epoch clears old member drafts independently of workspace recovery",async()=>{
  const api=fixture(),ui=await launch(api);await openNotification(ui,"gitlab","gl","member-a");
  ui.el("gitlab-interval").value="72";await ui.el("gitlab-interval").emit("input");
  api.runtimeEpoch="next-runtime";await ui.refresh();await openNotification(ui,"gitlab","gl","member-a");assert.equal(ui.el("gitlab-interval").value,30);
});

test("M4 a late source navigation cannot open over another member's Inbox",async()=>{
  const api=fixture(),ui=await launch(api);await openMemberInbox(ui,"member-a");let finish;
  api.hook=path=>path.includes("subscription_id=") ? new Promise(resolve=>finish=resolve) : undefined;
  const details=ui.el("inbox-entries").children[0].children.at(-1),button=details.children.find(c=>c.tag==="button");
  const opening=button.emit("click");await settle();await openMemberInbox(ui,"member-b");
  finish({subscriptions:[{provider:"gitlab",channel:"todos",subscription_id:"sub-gl-member-a",platform_id:"gl",instance:"https://gitlab.invalid",target:target("a")} ]});await opening;
  assert.equal(ui.el("notification-detail").open,false);assert.equal(ui.selectedAgent()?.membership_id,"member-b");
});

test("M4 legacy and retired source locations show diagnostics without opening a replacement",async()=>{
  const api=fixture(),ui=await launch(api);await openMemberInbox(ui,"member-a");
  const details=ui.el("inbox-entries").children[0].children.at(-1),button=details.children.find(c=>c.tag==="button");
  api.hook=path=>path.includes("subscription_id=") ? {subscriptions:[],retired_subscriptions:[{provider:"gitlab",channel:"todos",subscription_id:"sub-gl-member-a",platform_id:"gl",instance:"https://gitlab.invalid",retired:true,config:{enabled:false,target:target("a")}}]} : undefined;
  await button.emit("click");assert.equal(ui.el("notification-title").textContent,"通知来源诊断");assert.match(ui.el("notification-notice").textContent,/退役.*sub-gl-member-a/);assert.equal(ui.el("notification-panel-gitlab").hidden,true);
  await ui.el("close-notification").emit("click");
  api.hook=path=>path==="/v1/member/inbox" ? {snapshot_seq:1,has_more:false,status:{latest_seq:1,bytes:10,limits:{bytes:1000}},entries:[{seq:1,item:{provider:"gitlab",channel:"todos",text:"历史通知"},notification_source:{provider:"gitlab",channel:"todos"},source:{status:"not_checked"}}]} : undefined;
  await ui.el("inbox-reload").emit("click");
  await ui.el("inbox-entries").children[0].children.at(-1).children.find(c=>c.tag==="button").emit("click");
  assert.match(ui.el("notification-notice").textContent,/缺少完整来源身份/);assert.equal(ui.el("notification-panel-gitlab").hidden,true);
});


test("M4 room notification toggle, save and sync explicitly select mode=room",async()=>{
  const api=fixture(),ui=await launch(api);await openNotification(ui,"gitlab","gl");
  ui.notificationToggle().checked=false;await ui.notificationToggle().emit("change");
  assert.deepEqual(writes(api).at(-1).body.config.target,{mode:"room"});
  ui.el("gitlab-interval").value="70";await ui.el("gitlab-interval").emit("input");await ui.el("gitlab-save").emit("click");
  assert.deepEqual(writes(api).at(-1).body.config.target,{mode:"room"});
  ui.notificationToggle().checked=true;await ui.notificationToggle().emit("change");await ui.el("gitlab-sync").emit("click");
  assert.deepEqual(api.calls.find(c=>c.path.endsWith("notifications/sync")).body.target,{mode:"room"});
  assert.equal(recordsValue(api,"gl","a").config.enabled,true);assert.equal(recordsValue(api,"gl","b").config.interval_seconds,45);
});


test("a completed global-only connection has no permanent receipt or member writes",async()=>{
  const api=fixture(),operation={schema:2,operation_id:"e".repeat(32),platform_id:"gl",input:{provider:"gitlab",only_platform:true},connection:{status:"connected"},notifications:{status:"skipped"},tool:{status:"skipped"}};
  api.hook=path=>path==="/v1/platforms/onboarding/status" ? copy(operation) : undefined;
  const ui=await launch(api,new Map([["teamx.platform-onboarding:tester",JSON.stringify({operation_id:operation.operation_id})]]));
  await openMemberInbox(ui,"member-a");await ui.el("open-settings").emit("click");
  assert.equal(ui.el("onboarding-result").hidden,true);assert.equal(ui.el("onboarding-notification-settings").hidden,true);
  assert.equal(writes(api).length,0);assert.equal(api.calls.some(c=>/onboarding\/(apply|cancel)/.test(c.path)),false);
});


for(const provider of ["gitlab","lark"])test(`detail configuration has no duplicate switches and a new ${provider} source stays paused`,async()=>{
  const api=fixture({empty:true}),ui=await launch(api);
  await openNotification(ui,provider,provider==="lark" ? "lark" : "gl","member-a");
  assert.equal(ui.el("notification-enabled"),undefined);
  assert.equal(ui.el("member-platform-tool-control"),undefined);
  assert.equal(ui.el(`${provider==="lark" ? "lark" : "gitlab"}-save`).disabled,false);
  await ui.el(`${provider==="lark" ? "lark" : "gitlab"}-save`).emit("click");
  assert.equal(writes(api).at(-1).body.config.enabled,false);
  assert.match(ui.el("notification-enabled-state").textContent,/通知已暂停/);
  assert.equal(api.calls.some(c=>c.path==="/v1/tools/bind"),false);
});

test("selected notification content explains an inactive collector without contradicting the checkbox",async()=>{
  const api=fixture();recordsValue(api,"gl","a").todos.status="disabled";
  const ui=await launch(api);await openNotification(ui,"gitlab","gl","member-a");
  assert.equal(ui.el("gitlab-todos-enabled").checked,true);
  assert.equal(ui.el("gitlab-todos-state").textContent,"采集未启动");
  assert.equal(ui.el("gitlab-events-enabled").checked,false);
  assert.equal(ui.el("gitlab-events-state").textContent,"");
});

test("Inbox shows source capacity/backlog separately from retained input and uses existing followups",async()=>{
  const api=fixture();
  const record=api.records.get(key("gl",target("a")));record.todos.delivery_error_code="inbox_capacity";
  api.hook=(path)=>{
    if(path.startsWith("/v1/platform/notifications?"))return {subscriptions:[{provider:"gitlab",channel:"todos",target:target("a"),subscription_id:"fixture",pending:4}],gitlab_platforms:[copy(record)]};
    if(path==="/v1/policy/read")return {revision:1,current:[],history:[],effective:[]};
    if(path==="/v1/matters/list")return {items:[{kind:"object",ref:"fixture",status:"waiting_user",questions:[{text:"请确认范围"}]}],total:1,complete:true};
  };
  const ui=await launch(api);await openMemberInbox(ui,"member-a");
  assert.match(ui.el("inbox-platform-issues").children[0].textContent,/容量已满.*积压/);
  assert.match(ui.el("inbox-backlog").textContent,/来源积压 4 条/);
  assert.match(ui.el("inbox-status").textContent,/保留 50 条/);
  await ui.el("inbox-followups").emit("click");
  assert.equal(ui.el("policy-followups").open,true);
  assert.equal(ui.el("policy-matters").children[0].children.at(-1).textContent,"待你答复：请确认范围");
  assert.equal(api.calls.some(c=>/member\/run|inbox\/confirm|actions\/execute/.test(c.path)),false);
});
