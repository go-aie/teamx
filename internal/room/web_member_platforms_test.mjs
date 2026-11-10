import {test} from "node:test";
import assert from "node:assert/strict";
import {readFile} from "node:fs/promises";
import {launch,settle,openMemberInbox} from "./web_app_harness.mjs";
import {fixture,target,key} from "./web_member_platform_fixture.mjs";
const choice=(ui,id,action="settings")=>ui.el("member-notification-add-options").children.find(b=>b.dataset.platformId===id).children[1].children.find(b=>b.dataset.action===action);
const choose=async(ui,id)=>{await choice(ui,id).emit("click");};
const calls=(api,path)=>api.calls.filter(c=>c.path===path);
const row=(ui,id)=>ui.el("member-notification-platforms").children.find(r=>r.dataset.platformId===id);
const toolToggle=(ui,id)=>row(ui,id).children[1].children[2].children[0];

test("M4.5 platforms are the default Tab followed by policy, Inbox and activity",async()=>{
 const html=await readFile(new URL("./web/index.html",import.meta.url),"utf8");
 const tabs=html.slice(html.indexOf('id="agent-tabs"'),html.indexOf('id="platforms-member-view"'));
 assert.deepEqual([...tabs.matchAll(/id="agent-tab-([^"]+)"/g)].map(m=>m[1]),["platforms","policy","inbox","activity"]);
 assert.ok(html.indexOf('id="platforms-member-view"')<html.indexOf('id="member-notification-sources"'));
 assert.doesNotMatch(html,/id="agent-tab-tools"|id="tools-member-view"/);
 const inbox=html.slice(html.indexOf('id="inbox-member-view"'),html.indexOf('id="policy-member-view"'));
 assert.doesNotMatch(inbox,/id="member-notification-|role="switch"/);
 const api=fixture(),ui=await launch(api);await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();
 assert.equal(ui.el("agent-tab-platforms").attributes["aria-selected"],"true");assert.equal(ui.el("platforms-member-view").hidden,false);assert.equal(ui.el("activity-body").hidden,true);assert.equal(calls(api,"/v1/member/activity").length,0);
 assert.equal(ui.el("member-notification-source-count").textContent,"2");assert.equal(api.tools.bindings.length,0);assert.equal(ui.el("platforms-member-view").hidden,false);
 await ui.el("member-notification-add-toggle").emit("click");assert.equal(ui.el("member-notification-add-picker").hidden,false);assert.equal(ui.el("member-notification-add-toggle").attributes["aria-expanded"],"true");
 await ui.el("agent-tab-platforms").emit("keydown",{key:"ArrowRight"});await settle();assert.equal(ui.el("agent-tab-policy").attributes["aria-selected"],"true");assert.equal(ui.el("platforms-member-view").hidden,true);
 await ui.el("agent-tab-policy").emit("keydown",{key:"End"});await settle();assert.equal(ui.el("agent-tab-activity").attributes["aria-selected"],"true");
 await ui.el("agent-tab-activity").emit("keydown",{key:"ArrowRight"});await settle();assert.equal(ui.el("agent-tab-platforms").attributes["aria-selected"],"true");
});
for(const notifications of [false,true])for(const tool of [false,true])test(`M4.5 add existing platform uses defaults with independent choices (${notifications}/${tool})`,async()=>{
 const api=fixture({empty:true}),ui=await launch(api);await openMemberInbox(ui,"member-a");
 await choose(ui,"gl");assert.equal(ui.el("member-platform-add-notifications").checked,true);assert.equal(ui.el("member-platform-add-tool").checked,true);
 assert.equal(ui.el("gitlab-todos-enabled").checked,true);assert.equal(ui.el("notification-detail").open,true);
 for(const [id,value] of [["notifications",notifications],["tool",tool]]){ui.el(`member-platform-add-${id}`).checked=value;await ui.el(`member-platform-add-${id}`).emit("change");}
 await ui.el("member-platform-add-submit").emit("click");
 const sent=calls(api,"/v1/platforms/onboarding/prepare")[0].body;
 assert.equal(sent.preserve_existing,true);assert.equal(sent.notifications,notifications);assert.equal(sent.tool,tool);assert.equal(sent.gitlab?.todos.enabled,notifications ? true : undefined);assert.equal(sent.lark,undefined);
 assert.equal(sent.agent_id,"a");assert.equal(sent.membership_id,"member-a");assert.equal(sent.room_instance,"instance-R");assert.equal(sent.platform_id,"gl");
 assert.equal(api.records.get(key("gl",target())).config.enabled,notifications);assert.equal(toolToggle(ui,"gl").checked,tool);assert.equal(row(ui,"gl").children[1].children[1].checked,notifications);
 assert.equal(calls(api,"/v1/tools/bind").length,0);assert.equal(calls(api,"/v1/platform/gitlab/notifications/configure").length,0);
 assert.equal(calls(api,"/v1/platforms/onboarding/apply").length,1);assert.match(ui.el("member-platform-add-result").textContent,/通知：已保存.*工具：已保存/);
});
test("M4.5 first custom notification draft waits for Add and keeps the Inbox position",async()=>{
 const api=fixture({empty:true}),ui=await launch(api);await openMemberInbox(ui,"member-b");await ui.el("inbox-next").emit("click");
 ui.el("inbox-member-view").scrollTop=220;const entries=ui.el("inbox-entries").children,reads=calls(api,"/v1/member/inbox").length;
 await choose(ui,"gl2");assert.equal(ui.el("notification-title").textContent,"添加 个人 GitLab");
 ui.el("gitlab-interval").value="80";await ui.el("gitlab-interval").emit("input");
 ui.el("gitlab-backfill").value="3600";await ui.el("gitlab-backfill").emit("change");
 assert.equal(api.records.size,0);assert.equal(calls(api,"/v1/platforms/onboarding/prepare").length,0);
 assert.equal(ui.el("inbox-page").textContent,"第 2 页");assert.equal(ui.el("inbox-member-view").scrollTop,220);assert.equal(ui.el("inbox-entries").children,entries);
 await ui.el("member-platform-add-submit").emit("click");const config=calls(api,"/v1/platforms/onboarding/prepare")[0].body.gitlab;
 assert.equal(ui.el("notification-detail").open,false);
 assert.equal(config.interval_seconds,80);assert.equal(config.backfill_seconds,3600);assert.deepEqual(config.target,target("b"));assert.equal(calls(api,"/v1/member/inbox").length,reads);
});
test("M4.5 a tool-only platform initializes notifications without changing its saved tool choice",async()=>{
 const api=fixture();api.tools.disabled.push({agent_id:"b",membership_id:"member-b",tool:"gitlab",platform_id:"gl2"});
 const ui=await launch(api);await openMemberInbox(ui,"member-b");const input=row(ui,"gl2").children[1].children[1];assert.equal(input.checked,false);input.checked=true;await input.emit("change");
 const spec=calls(api,"/v1/platforms/onboarding/prepare")[0].body;
 assert.equal(spec.skip_tool,true);assert.equal(spec.notifications,true);assert.equal(spec.gitlab,undefined);assert.equal(toolToggle(ui,"gl2").checked,false);assert.equal(api.tools.revision,0);
 assert.equal(api.records.get(key("gl2",target("b"))).config.todos.enabled,true);
});
test("M4.5 partial add results resume only unfinished steps",async()=>{
 const api=fixture({empty:true}),ui=await launch(api);await openMemberInbox(ui,"member-a");await choose(ui,"lark");
 api.hook=(path,body)=>{if(path==="/v1/platforms/onboarding/apply"){
  const op=api.operations.get(body.operation_id);op.connection.status="connected";op.notifications={status:"failed",error_code:"busy"};op.tool={status:"applied",version:1};api.tools.bindings.push({agent_id:"a",membership_id:"member-a",tool:"lark",platform_id:"lark"});api.tools.revision=1;return structuredClone(op);
 }};
 await ui.el("member-platform-add-submit").emit("click");assert.match(ui.el("member-platform-add-result").textContent,/通知：未完成.*工具：已保存/);
 assert.equal(toolToggle(ui,"lark").checked,true);assert.equal(ui.el("member-platform-add-retry").hidden,false);
 api.hook=null;await ui.el("member-platform-add-resume").emit("click");assert.equal(calls(api,"/v1/platforms/onboarding/apply").length,1);
 await ui.el("member-platform-add-retry").emit("click");assert.equal(api.tools.revision,1);assert.equal(api.tools.bindings.length,1);assert.equal(calls(api,"/v1/platforms/onboarding/prepare").length,1);assert.equal(calls(api,"/v1/platforms/onboarding/apply").length,2);
 assert.equal(api.records.get(key("lark",target())).config.private.scope,"all");assert.match(ui.el("member-platform-add-result").textContent,/通知：已保存/);
});
test("M4.5 unknown add keeps its durable operation and status reads do not replay",async()=>{
 const api=fixture({empty:true}),ui=await launch(api);await openMemberInbox(ui,"member-a");await choose(ui,"gl");
 api.hook=(path,body)=>{if(path==="/v1/platforms/onboarding/apply"){const op=api.operations.get(body.operation_id);op.connection.status="connected";op.notifications={status:"failed"};return Promise.reject(Error("lost"));}};
 await ui.el("member-platform-add-submit").emit("click");assert.match(ui.el("member-platform-add-result").textContent,/未确认.*不会自动重发/);
 assert.ok(ui.saved.get("teamx.platform-onboarding:tester"));await ui.el("member-platform-add-resume").emit("click");assert.equal(calls(api,"/v1/platforms/onboarding/apply").length,1);
});
test("M4.5 a late add receipt cannot populate another member's platform controls",async()=>{
 const api=fixture({empty:true}),ui=await launch(api);await openMemberInbox(ui,"member-a");await choose(ui,"gl");let release;
 api.hook=(path,body)=>path==="/v1/platforms/onboarding/apply" ? new Promise(r=>release=()=>r({...api.operations.get(body.operation_id),connection:{status:"connected"},notifications:{status:"applied"},tool:{status:"applied"}})) : undefined;
 const adding=ui.el("member-platform-add-submit").emit("click");await settle();
 await openMemberInbox(ui,"member-b");release();await adding;assert.equal(ui.selectedAgent()?.membership_id,"member-b");assert.equal(ui.el("member-notification-platforms").children.length,0);assert.equal(ui.el("member-platform-add-selection").hidden,true);
});
test("M4.5 connecting a new platform fixes the member and exposes independent choices",async()=>{
 const api=fixture({empty:true}),ui=await launch(api);await openMemberInbox(ui,"member-b");
 await ui.el("notification-add-platform").emit("click");assert.equal(ui.el("settings-dialog").open,true);assert.equal(ui.el("onboarding-member").value,"member-b");assert.equal(ui.el("onboarding-member-field").hidden,true);
 assert.equal(ui.el("onboarding-notifications").checked,true);assert.equal(ui.el("onboarding-tool").checked,true);assert.equal(ui.el("onboarding-enable-options").hidden,false);
 await ui.el("close-settings").emit("click");assert.equal(ui.selectedAgent()?.membership_id,"member-b");assert.equal(ui.el("agent-tab-inbox").attributes["aria-selected"],"true");
});

test("M4.5 an unknown prepare is explicitly retried with the same durable ID",async()=>{
 const api=fixture({empty:true}),ui=await launch(api);await openMemberInbox(ui,"member-a");await choose(ui,"gl");
 api.hook=path=>path==="/v1/platforms/onboarding/prepare"?Promise.reject(Error("lost")):undefined;
 await ui.el("member-platform-add-submit").emit("click");assert.equal(calls(api,"/v1/platforms/onboarding/prepare").length,1);assert.equal(calls(api,"/v1/platforms/onboarding/apply").length,0);
 const original=calls(api,"/v1/platforms/onboarding/prepare")[0].body;api.hook=null;
 await ui.el("member-platform-add-retry").emit("click");assert.deepEqual(calls(api,"/v1/platforms/onboarding/prepare")[1].body,original);assert.equal(calls(api,"/v1/platforms/onboarding/apply").length,0);
 await ui.el("member-platform-add-retry").emit("click");
 assert.equal(calls(api,"/v1/platforms/onboarding/apply").length,1);
 // The explicit continuation verifies the saved account and applies its steps.
 assert.equal(calls(api,"/v1/platforms/onboarding/prepare").length,2);
});

test("M4.5 restarting the page restores partial add receipts before permitting another add",async()=>{
 const api=fixture({empty:true}),ui=await launch(api);await openMemberInbox(ui,"member-a");await choose(ui,"gl");
 api.hook=(path,body)=>{if(path==="/v1/platforms/onboarding/apply"){
  const op=api.operations.get(body.operation_id);op.connection.status="connected";op.notifications={status:"failed"};op.tool={status:"applied",version:1};api.tools.bindings.push({agent_id:"a",membership_id:"member-a",platform_id:"gl",tool:"gitlab"});api.tools.revision=1;return structuredClone(op);
 }};
 await ui.el("member-platform-add-submit").emit("click");api.hook=null;
 const next=await launch(api,new Map(ui.saved));await openMemberInbox(next,"member-b");assert.match(next.el("member-platform-add-result").textContent,/有未完成的接入/);
 await openMemberInbox(next,"member-a");assert.match(next.el("member-platform-add-result").textContent,/通知：未完成.*工具：已保存/);assert.equal(calls(api,"/v1/platforms/onboarding/apply").length,1);
 await next.el("member-platform-add-retry").emit("click");assert.equal(api.tools.revision,1);assert.equal(api.tools.bindings.length,1);assert.equal(calls(api,"/v1/platforms/onboarding/prepare").length,1);
});

test("M4.5 a rejected prepare retains the first draft and allows correction",async()=>{
 const api=fixture({empty:true}),ui=await launch(api);await openMemberInbox(ui,"member-a");await choose(ui,"gl");
 ui.el("gitlab-interval").value="80";await ui.el("gitlab-interval").emit("input");
 ui.el("gitlab-backfill").value="3600";await ui.el("gitlab-backfill").emit("change");
 api.hook=path=>path==="/v1/platforms/onboarding/prepare"?Promise.reject(Object.assign(Error("invalid"),{code:"request"})):undefined;
 await ui.el("member-platform-add-submit").emit("click");assert.match(ui.el("member-platform-add-result").textContent,/核对成员、平台与通知范围/);assert.equal(ui.el("member-platform-add-submit").disabled,false);
 assert.equal(ui.saved.has("teamx.platform-onboarding:tester"),false);assert.equal(ui.el("notification-detail").open,true);
 assert.equal(ui.el("gitlab-interval").value,80);assert.equal(ui.el("gitlab-backfill").value,"3600");assert.match(ui.el("notification-notice").textContent,/核对成员、平台与通知范围/);
 api.hook=null;await ui.el("member-platform-add-submit").emit("click");assert.equal(calls(api,"/v1/platforms/onboarding/apply").length,1);
 assert.equal(calls(api,"/v1/platforms/onboarding/prepare").at(-1).body.gitlab.backfill_seconds,3600);
});

test("M4.5 returning to Activity preserves its tab and scroll and resumes read-only updates",async()=>{
 const api=fixture(),ui=await launch(api);await openMemberInbox(ui,"member-a");await ui.el("agent-tab-activity").emit("click");
 ui.el("activity-body").scrollTop=80;ui.el("platforms-member-view").scrollTop=40;
 await row(ui,"gl").children[0].emit("click");const reads=calls(api,"/v1/member/activity").length;
 await ui.refresh();assert.equal(calls(api,"/v1/member/activity").length,reads);
 await ui.el("close-notification").emit("click");await settle();assert.equal(ui.el("agent-tab-activity").attributes["aria-selected"],"true");assert.equal(ui.el("activity-body").scrollTop,80);assert.equal(ui.el("platforms-member-view").scrollTop,40);
 await ui.refresh();assert.ok(calls(api,"/v1/member/activity").length>reads);assert.equal(api.calls.some(c=>/member\/(run|enable)|inbox\/(read|confirm)/.test(c.path)),false);
});

test("platform Tab switches and manual refresh preserve Inbox snapshot, page, DOM and scroll",async()=>{
 const api=fixture(),ui=await launch(api);await openMemberInbox(ui,"member-a");await ui.el("inbox-next").emit("click");
 ui.el("inbox-member-view").scrollTop=180;const entries=ui.el("inbox-entries").children,reads=calls(api,"/v1/member/inbox").length;
 await ui.el("agent-tab-platforms").emit("click");assert.equal(ui.el("inbox-member-view").hidden,true);assert.equal(ui.el("platforms-member-view").hidden,false);
 await row(ui,"gl").children[0].emit("click");await ui.el("close-notification").emit("click");assert.equal(ui.el("agent-tab-platforms").attributes["aria-selected"],"true");
 await ui.el("agent-tab-inbox").emit("click");assert.equal(ui.el("inbox-page").textContent,"第 2 页");assert.equal(ui.el("inbox-entries").children,entries);assert.equal(ui.el("inbox-member-view").scrollTop,180);assert.equal(calls(api,"/v1/member/inbox").length,reads);
 await ui.el("inbox-reload").emit("click");assert.equal(ui.el("inbox-page").textContent,"第 2 页");assert.equal(calls(api,"/v1/member/inbox").length,reads+1);
});

test("Inbox empty-state Add opens the platforms Tab without consuming or refreshing messages",async()=>{
 const api=fixture({empty:true}),ui=await launch(api);await openMemberInbox(ui,"member-b");const reads=calls(api,"/v1/member/inbox").length;
 await ui.el("inbox-add-platform").emit("click");assert.equal(ui.el("agent-tab-platforms").attributes["aria-selected"],"true");assert.equal(ui.el("member-notification-add-picker").hidden,false);
 await ui.el("agent-tab-inbox").emit("click");assert.equal(calls(api,"/v1/member/inbox").length,reads);assert.equal(api.calls.some(c=>/member\/(run|enable)|inbox\/(read|confirm)/.test(c.path)),false);
});


test("platform quick add applies defaults in one click without opening settings",async()=>{
 const api=fixture({empty:true}),ui=await launch(api);await openMemberInbox(ui,"member-a");
 const option=ui.el("member-notification-add-options").children.find(r=>r.dataset.platformId==="gl");
 await option.emit("click");assert.equal(api.operations.size,0);
 const reads=api.calls.filter(c=>c.path.startsWith("/v1/platform/gitlab/notifications?")).length;
 await choice(ui,"gl","add").emit("click");
 const sent=calls(api,"/v1/platforms/onboarding/prepare")[0].body;
 assert.equal(sent.notifications,true);assert.equal(sent.tool,true);assert.equal(sent.gitlab,undefined);
 assert.equal(ui.el("notification-detail").open,false);assert.equal(ui.el("member-notification-add-picker").hidden,true);
 assert.equal(api.calls.filter(c=>c.path.startsWith("/v1/platform/gitlab/notifications?")).length,reads);
 assert.equal(api.records.get(key("gl",target())).config.todos.enabled,true);assert.equal(toolToggle(ui,"gl").checked,true);
});

test("cancelling first settings writes nothing and quick add ignores the cancelled history draft",async()=>{
 const api=fixture({empty:true}),ui=await launch(api);await openMemberInbox(ui,"member-a");
 await choose(ui,"gl");assert.equal(api.operations.size,0);
 ui.el("gitlab-backfill").value="3600";await ui.el("gitlab-backfill").emit("change");
 await ui.el("member-platform-add-cancel").emit("click");assert.equal(api.records.size,0);assert.equal(api.operations.size,0);
 await choose(ui,"gl");assert.equal(ui.el("gitlab-backfill").value,"0");await ui.el("member-platform-add-cancel").emit("click");
 await choice(ui,"gl","add").emit("click");const sent=calls(api,"/v1/platforms/onboarding/prepare")[0].body;
 assert.equal(sent.gitlab,undefined);assert.equal(api.records.get(key("gl",target())).config.backfill_seconds,0);
});

test("custom add validates history before writing, while tool-only add needs no notification form",async()=>{
 const api=fixture({empty:true}),ui=await launch(api);await openMemberInbox(ui,"member-a");await choose(ui,"gl");
 ui.el("gitlab-backfill").value="custom";await ui.el("gitlab-backfill").emit("change");
 assert.equal(ui.el("member-platform-add-submit").disabled,true);await ui.el("member-platform-add-submit").emit("click");assert.equal(api.operations.size,0);
 ui.el("member-platform-add-notifications").checked=false;await ui.el("member-platform-add-notifications").emit("change");
 assert.equal(ui.el("notification-panel-gitlab").hidden,true);assert.equal(ui.el("member-platform-add-submit").disabled,false);
 await ui.el("member-platform-add-submit").emit("click");const sent=calls(api,"/v1/platforms/onboarding/prepare")[0].body;
 assert.equal(sent.notifications,false);assert.equal(sent.tool,true);assert.equal(sent.gitlab,undefined);
});

for(const provider of ["lark","scion"])test(`first ${provider} settings enable only supported capabilities`,async()=>{
 const api=fixture({empty:true});if(provider==="scion")api.platforms.push({id:"scion",provider:"scion",name:"Scion",url:"https://scion.invalid",validation:{status:"valid",username:"alice"}});
 const ui=await launch(api);await openMemberInbox(ui,"member-a");await choose(ui,provider);
 assert.equal(ui.el("member-platform-add-notifications").checked,provider==="lark");assert.equal(ui.el("member-platform-add-notifications").disabled,provider==="scion");
 assert.equal(ui.el("member-platform-add-tool").checked,true);assert.equal(ui.el("member-platform-add-submit").disabled,false);
 await ui.el("member-platform-add-submit").emit("click");const sent=calls(api,"/v1/platforms/onboarding/prepare")[0].body;
 assert.equal(sent.notifications,provider==="lark");assert.equal(sent.tool,true);
 if(provider==="lark"){assert.equal(sent.lark.enabled,true);assert.equal(sent.lark.private.scope,"all");assert.equal(sent.lark.groups.enabled,false);}
});
