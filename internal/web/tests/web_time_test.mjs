import {test} from "node:test";
import assert from "node:assert/strict";
import {createSystemTime, createTimeInput} from "../assets/time.mjs";
import {launch, settle} from "./web_app_harness.mjs";

const snapshot = (time_zone="Asia/Shanghai", settings_version=1, service_epoch="manager") => ({time_zone,settings_version,service_epoch});
test("system zone controls midnight grouping, explicit offsets and inputs independently of host TZ", () => {
  const clock=createSystemTime();clock.update(snapshot());
  const instant="2026-11-08T17:04:13.09Z";
  assert.equal(clock.day(instant),"2026-11-09");
  assert.match(clock.format(instant),/2026\/11\/09 01:04:13/);
  assert.equal(clock.local(instant),"2026-11-09T01:04:13");
  assert.equal(clock.local("2026-11-09T01:04:13+08:00"),"2026-11-09T01:04:13");
  assert.equal(clock.parse("2026-11-09T01:04:13"),"2026-11-08T17:04:13.000Z");
  clock.update(snapshot("UTC",2));assert.equal(clock.day(instant),"2026-11-08");
  assert.equal(clock.local(instant),"2026-11-08T17:04:13");
  assert.equal(clock.update(snapshot("Asia/Shanghai",1)),false);
  assert.equal(clock.zone,"UTC");
  clock.update(snapshot("America/New_York",1,"restart"));
  assert.equal(clock.version,1);assert.equal(clock.local(instant),"2026-11-08T12:04:13");
});

test("DST gaps and folds are rejected; saved instants keep their exact offset and precision", () => {
  const clock=createSystemTime();clock.update(snapshot("America/New_York"));
  for(const value of ["2026-03-08T02:30", "2026-11-01T01:30", "2026-02-30T12:00"]) assert.throws(()=>clock.parse(value),/有效时间/);
  assert.equal(clock.parse("2026-03-08T03:30"),"2026-03-08T07:30:00.000Z");
  const input=createTimeInput(clock), instant="2026-11-01T06:30:00.123Z";
  assert.equal(input.fill(instant),"2026-11-01T01:30:00");
  assert.equal(input.read("2026-11-01T01:30:00"),instant);
  assert.equal(input.read("2026-11-01T01:30"),instant);
  clock.update(snapshot("UTC",2));assert.equal(input.rebase(),"2026-11-01T06:30:00");
  assert.equal(input.read("2026-11-01T06:30:00"),instant);
});

test("unresolved time drafts require confirmation after zone changes and missing zones never use host TZ", () => {
  const clock=createSystemTime();clock.update(snapshot("America/New_York"));
  const input=createTimeInput(clock);input.edit("2026-11-01T01:30");
  clock.update(snapshot("UTC",2));assert.equal(input.rebase(),"2026-11-01T01:30");
  assert.throws(()=>input.read("2026-11-01T01:30"),/重新确认/);
  input.edit("2026-11-01T01:30");assert.equal(input.read("2026-11-01T01:30"),"2026-11-01T01:30:00.000Z");
  input.edit("",true);assert.throws(()=>input.read(""),/完整有效/);
  clock.update(snapshot("Asia/Shanghai",3));input.rebase();assert.throws(()=>input.read(""),/重新确认/);
  input.edit("");assert.equal(input.read(""),"");
  for(const zone of [null,"Invalid/Zone","Local"]) {
    clock.update(snapshot(zone,clock.version+1));assert.ok(clock.error);
    assert.equal(clock.format(Date.now()),"系统时区异常");assert.equal(clock.local(Date.now()),"");
    assert.throws(()=>clock.parse("2026-11-01T01:30"),/系统时区/);
  }
});

function fixture(managed=true) {
  const info={room_id:"A",room_instance:"instance-A",service_epoch:"room-A"};
  const member={agent_id:"assistant",membership_id:"member-A",status:"idle",runner:{profile:"runtime"}};
  const api={calls:[],settings:snapshot("Asia/Shanghai",1,managed ? "manager" : "room-A"),hook:null,
    messages:[{seq:1,text:"原始正文 17:04",author:{kind:"human",id:"tester"},created_at:"2026-11-08T15:59:00Z"},{seq:2,text:"原始 UTC 不改写",author:{kind:"human",id:"tester"},created_at:"2026-11-08T17:04:13.09Z"}],
    async request(path,body) {
      api.calls.push({path,body:structuredClone(body)});
      if(api.hook){const value=api.hook(path,body);if(value!==undefined)return value;}
      if(path==="/v1/ui")return {...api.settings,...(managed ? {managed:true} : {...info,operator:"tester",profiles:[]})};
      if(path==="/v1/rooms")return {...api.settings,operator:"tester",rooms:[{...info,name:"A",status:"ready",runtime_status:"ready",service:info,created_at:api.messages[1].created_at}],teams:[]};
      if(path.startsWith("/v1/workspace?"))return {workspace:{root:"workspaces"},workspace_version:1,members:[]};
      if(path.startsWith("/v1/platform/notifications?"))return {subscriptions:[]};
      if(path.startsWith("/v1/platforms?"))return {platforms:[]};
      if(path==="/v1/member/list")return {members:[member]};
      if(path==="/v1/member/activity")return {member};
      if(path==="/v1/read")return {messages:api.messages.filter(m=>m.seq>(body.after || 0)),snapshot_seq:2,has_more:false};
      if(path==="/v1/settings")return {...api.settings,...(!managed ? info : {})};
      if(path==="/v1/settings/save") {api.settings={...api.settings,time_zone:body.time_zone,settings_version:api.settings.settings_version+1};return {time_zone:api.settings.time_zone,settings_version:api.settings.settings_version};}
      if(path==="/v1/member/inbox/status")return {latest_seq:1,expired_through:0,retained_entries:1,bytes:0,limits:{bytes:1000}};
      if(path==="/v1/member/inbox")return {next_after:1,entries:[{seq:1,received_at:"2026-11-08T17:05:00Z",item:{provider:"gitlab",channel:"todos",created_at:api.messages[1].created_at,text:"通知正文 17:04"}}],snapshot_seq:1,has_more:false,status:{latest_seq:1,expired_through:0,retained_entries:1,bytes:0,limits:{bytes:1000}}};
      if(path==="/v1/policy/read")return {revision:0,current:[],effective:[],history:[]};
      if(path==="/v1/policy/update")return {};
      throw new Error(`Unexpected request ${path}`);
    }};
  return api;
}
const textOf = element => [element.textContent,...element.children.map(textOf)].join("\n");
const times = ui => ui.el("messages").children.filter(el=>el.tag!=="div" || !el.className?.includes("day-divider")).map(el=>el.querySelector("time"));
async function saveZone(ui,zone) {ui.el("system-timezone").value=zone;await ui.el("system-timezone").emit("input");await ui.el("timezone-form").emit("submit");}
async function openAgent(ui,tab) {await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();await ui.el(`agent-tab-${tab}`).emit("click");await settle();}

test("saving changes old message labels, grouping and Room list immediately; other tabs poll and ignore stale settings", async () => {
  const api=fixture(),ui=await launch(api),other=await launch(api);
  const original=structuredClone(api.messages);
  assert.equal(ui.el("messages").children.length,4);
  assert.equal(ui.el("messages").children[0].textContent,"11月8日 周日");
  assert.equal(ui.el("messages").children[2].textContent,"11月9日 周一");
  assert.equal(times(ui)[1].textContent,"01:04");assert.match(times(ui)[1].title,/11\/09 01:04/);
  await ui.el("open-settings").emit("click");
  assert.equal(ui.el("timezone-save").disabled,true);
  await ui.el("timezone-form").emit("submit");assert.equal(api.calls.some(c=>c.path==="/v1/settings/save"),false);
  for(const [value,disabled] of [["",true],["UTC",false],[" Asia/Shanghai ",true]]) {
    ui.el("system-timezone").value=value;await ui.el("system-timezone").emit("input");assert.equal(ui.el("timezone-save").disabled,disabled);
  }
  assert.equal(ui.el("timezone-feedback").textContent,"");
  await saveZone(ui,"UTC");assert.equal(ui.el("timezone-save").disabled,true);
  const save=api.calls.find(c=>c.path==="/v1/settings/save").body;
  assert.deepEqual(save,{service_epoch:"manager",caller:{kind:"human",id:"tester"},expected_version:1,time_zone:"UTC"});
  assert.equal(ui.el("messages").children.length,3);assert.equal(times(ui)[1].textContent,"17:04");
  assert.match(textOf(ui.el("room-list")),/11\/08 17:04/);
  assert.match(ui.el("timezone-feedback").textContent,/当前生效：UTC.*下一回合/);
  assert.deepEqual(api.messages,original);
  other.el("system-timezone").value="UTC";await other.el("system-timezone").emit("input");
  assert.equal(other.el("timezone-save").disabled,false);
  await other.refresh();assert.equal(times(other)[1].textContent,"17:04");assert.equal(other.el("timezone-save").disabled,true);
  assert.equal(other.el("timezone-feedback").textContent,"");
  api.settings=snapshot("Asia/Shanghai",1);await ui.refresh();assert.equal(times(ui)[1].textContent,"17:04");
  assert.equal(ui.el("system-timezone").value,"UTC");
});

test("browser detection only fills a draft; failed saves and conflicts preserve it without replay", async () => {
  const api=fixture();api.settings=snapshot("Pacific/Honolulu");const ui=await launch(api);
  await ui.el("open-settings").emit("click");
  assert.equal(ui.el("timezone-browser").hidden,false);await ui.el("timezone-browser").emit("click");
  assert.equal(api.calls.some(c=>c.path==="/v1/settings/save"),false);
  api.hook=path=>{if(path==="/v1/settings/save")throw Object.assign(new Error("version changed"),{code:"conflict"});};
  api.settings=snapshot("UTC",2);await saveZone(ui,"America/New_York");
  assert.equal(ui.el("system-timezone").value,"America/New_York");assert.match(ui.el("timezone-current").textContent,/UTC/);
  assert.match(ui.el("timezone-feedback").textContent,/草稿已保留/);
  await ui.refresh();assert.equal(api.calls.filter(c=>c.path==="/v1/settings/save").length,1);
  api.hook=path=>{if(path==="/v1/settings/save")throw new Error("invalid time_zone");};
  await saveZone(ui,"Bad/Zone");assert.equal(ui.el("system-timezone").value,"Bad/Zone");
  assert.match(ui.el("timezone-feedback").textContent,/invalid time_zone/);
});

test("open Inbox headers and source details repaint without rereading or changing page/body", async () => {
  const api=fixture(),ui=await launch(api);await openAgent(ui,"inbox");
  const entry=ui.el("inbox-entries").children[0],time=entry.children[0].children[2],details=entry.children.at(-1);
  details.open=true;assert.match(time.title,/11\/09 01:04/);
  const calls=api.calls.filter(c=>c.path==="/v1/member/inbox").length;
  api.settings=snapshot("UTC",2);await ui.refresh();
  assert.match(time.title,/11\/08 17:04/);assert.match(textOf(details),/11\/08 17:04/);
  assert.equal(details.open,true);assert.match(textOf(entry),/通知正文 17:04/);
  assert.equal(api.calls.filter(c=>c.path==="/v1/member/inbox").length,calls);
});

test("policy time inputs submit system-zone instants, preserve resolved drafts on switches and block DST ambiguity", async () => {
  const api=fixture(),ui=await launch(api);await openAgent(ui,"policy");await ui.el("policy-new").emit("click");
  ui.el("policy-text").value="保留文本";ui.el("policy-expires").value="2026-11-09T01:04:13";await ui.el("policy-expires").emit("input");
  api.settings=snapshot("UTC",2);await ui.refresh();assert.equal(ui.el("policy-expires").value,"2026-11-08T17:04:13");
  assert.equal(ui.el("policy-text").value,"保留文本");await ui.el("policy-form").emit("submit");
  assert.equal(api.calls.find(c=>c.path==="/v1/policy/update").body.policy.expires_at,"2026-11-08T17:04:13.000Z");
  api.settings=snapshot("America/New_York",3);await ui.refresh();await ui.el("policy-new").emit("click");
  ui.el("policy-expires").value="2026-11-01T01:30";await ui.el("policy-expires").emit("input");await ui.el("policy-form").emit("submit");
  assert.match(ui.el("policy-status").textContent,/夏令时/);
  api.settings=snapshot("UTC",4);await ui.refresh();await ui.el("policy-form").emit("submit");
  assert.match(ui.el("policy-status").textContent,/重新确认/);
  assert.equal(api.calls.filter(c=>c.path==="/v1/policy/update").length,1);
});

test("single-Room settings carry full identity and unavailable zones show an explicit error", async () => {
  const api=fixture(false),ui=await launch(api);await ui.el("open-general-settings").emit("click");
  assert.equal(ui.el("general-settings").hidden,false);assert.equal(ui.el("platforms-list-view").hidden,true);
  await saveZone(ui,"UTC");const save=api.calls.find(c=>c.path==="/v1/settings/save").body;
  assert.equal(save.room_id,"A");assert.equal(save.room_instance,"instance-A");assert.equal(save.service_epoch,"room-A");
  api.settings={...api.settings,time_zone:null,settings_version:3};await ui.refresh();
  assert.equal(ui.el("timezone-error").hidden,false);assert.match(times(ui)[1].textContent,/系统时区异常/);
});

test("a late save response cannot overwrite a restarted service's settings", async () => {
  const api=fixture(),ui=await launch(api);let finish;
  api.hook=path=>path==="/v1/settings/save" ? new Promise(resolve=>{finish=()=>resolve({time_zone:"UTC",settings_version:2});}) : undefined;
  const pending=saveZone(ui,"UTC");await settle();
  assert.equal(ui.el("timezone-save").disabled,true);
  await ui.el("timezone-form").emit("submit");assert.equal(api.calls.filter(c=>c.path==="/v1/settings/save").length,1);
  api.settings=snapshot("America/New_York",1,"restarted");await ui.refresh();
  finish();await pending;
  assert.match(ui.el("timezone-current").textContent,/America\/New_York/);
  assert.equal(times(ui)[1].textContent,"12:04");assert.doesNotMatch(ui.el("timezone-feedback").textContent,/已保存/);
});

test("settings categories isolate panels and preserve both time-zone and connection drafts", async () => {
  const api=fixture(),ui=await launch(api);await ui.el("open-settings").emit("click");
  assert.equal(ui.el("general-settings").hidden,false);assert.equal(ui.el("platform-settings").hidden,true);
  ui.el("system-timezone").value="America/New_York";await ui.el("system-timezone").emit("input");
  await ui.el("settings-tab-general").emit("keydown",{key:"ArrowDown"});
  assert.equal(ui.el("settings-tab-platforms").attributes["aria-selected"],"true");
  assert.equal(ui.el("general-settings").hidden,true);assert.equal(ui.el("platform-settings").hidden,false);
  await ui.el("connection-new").emit("click");ui.el("connection-name").value="未保存的连接";
  await ui.el("settings-tab-general").emit("click");assert.equal(ui.el("system-timezone").value,"America/New_York");
  await ui.el("settings-tab-platforms").emit("click");assert.equal(ui.el("connection-form").hidden,false);
  assert.equal(ui.el("connection-name").value,"未保存的连接");
  assert.equal(api.calls.some(c=>/settings\/save|platforms\/save/.test(c.path)),false);
  await ui.el("settings-tab-general").emit("click");await ui.el("close-settings").emit("click");
  await ui.el("lark-manage-platforms").emit("click");
  assert.equal(ui.el("settings-tab-platforms").attributes["aria-selected"],"true");
  assert.equal(ui.el("connection-form").hidden,true);assert.equal(ui.el("platforms-list-view").hidden,false);
});

test("styled time-zone picker searches, selects with mouse/keyboard and never saves implicitly", async () => {
  const api=fixture(),ui=await launch(api);await ui.el("open-settings").emit("click");
  await ui.el("system-timezone").emit("click");assert.equal(ui.el("timezone-options").hidden,false);
  await ui.el("system-timezone").emit("click");assert.equal(ui.el("timezone-options").hidden,false);
  await ui.el("timezone-picker-toggle").emit("click");assert.equal(ui.el("timezone-options").hidden,true);
  await ui.el("system-timezone").emit("click");assert.equal(ui.el("timezone-options").hidden,false);
  assert.ok(ui.el("timezone-options").children.length>400);
  ui.el("system-timezone").value="new_york";await ui.el("system-timezone").emit("input");
  assert.equal(ui.el("timezone-options").children.length,1);
  await ui.el("timezone-options").children[0].emit("click");
  assert.equal(ui.el("system-timezone").value,"America/New_York");assert.equal(ui.el("timezone-options").hidden,true);
  ui.el("system-timezone").value="utc";await ui.el("system-timezone").emit("input");
  await ui.el("system-timezone").emit("keydown",{key:"ArrowDown"});await ui.el("system-timezone").emit("keydown",{key:"Enter"});
  assert.equal(ui.el("system-timezone").value,"UTC");assert.equal(ui.el("timezone-options").hidden,true);
  ui.el("system-timezone").value="No/Match";await ui.el("system-timezone").emit("input");
  assert.match(ui.el("timezone-options").children[0].textContent,/未找到/);
  await ui.el("system-timezone").emit("keydown",{key:"Escape"});
  assert.equal(ui.el("timezone-options").hidden,true);assert.equal(ui.el("settings-dialog").open,true);
  assert.equal(api.calls.some(c=>c.path==="/v1/settings/save"),false);
});

test("settings navigation blocks changes during a save and adapts to narrow or single-Room pages", async () => {
  const api=fixture(),ui=await launch(api);let finish;
  api.hook=path=>path==="/v1/settings/save" ? new Promise(resolve=>{finish=()=>resolve({time_zone:"UTC",settings_version:2});}) : undefined;
  await ui.el("open-settings").emit("click");const pending=saveZone(ui,"UTC");await settle();
  assert.equal(ui.el("settings-tab-platforms").disabled,true);await ui.el("settings-tab-platforms").emit("click");
  assert.equal(ui.el("general-settings").hidden,false);finish();await pending;
  assert.equal(ui.el("settings-tab-platforms").disabled,false);await ui.el("settings-tab-platforms").emit("click");
  ui.window.innerWidth=390;await ui.window.emit("resize");assert.equal(ui.el("settings-navigation").attributes["aria-orientation"],"horizontal");
  assert.equal(ui.el("platform-settings").hidden,false);
  const single=await launch(fixture(false));await single.el("open-general-settings").emit("click");
  assert.equal(single.el("settings-tab-platforms").hidden,true);assert.equal(single.el("general-settings").hidden,false);
});
