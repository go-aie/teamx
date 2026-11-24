import {test} from "node:test";
import assert from "node:assert/strict";
import {createSystemTime, createTimeInput} from "../assets/time.mjs";
import {launch, settle} from "./web_app_harness.mjs";

const snapshot = (time_zone="Asia/Shanghai", settings_version=1, service_epoch="manager") => ({time_zone,settings_version,service_epoch});
test("recent timestamps use system-zone calendar days across midnight, years and DST", () => {
  const clock=createSystemTime();clock.update(snapshot());
  const now="2027-01-01T00:30:00+08:00";
  for (const [value,label] of [
    ["2026-12-31T16:05:00Z","00:05"],
    ["2026-12-31T15:59:00Z","昨天"],
    ["2026-12-30T15:59:00Z","2026年12月30日"],
    ["2027-01-03T00:00:00Z","1月3日"],
  ]) assert.equal(clock.recent(value,now),label);
  assert.equal(clock.recent("2026-11-08T00:00:00Z","2026-11-11T00:00:00Z"),"11月8日");
  clock.update(snapshot("America/New_York",2));
  // Yesterday is a calendar date, even when the elapsed interval exceeds 24 hours.
  assert.equal(clock.recent("2026-11-01T04:30:00Z","2026-11-02T05:15:00Z"),"昨天");
  assert.equal(clock.recent("2026-03-08T05:30:00Z","2026-03-09T04:15:00Z"),"昨天");
  assert.equal(clock.recent("invalid",now),"时间未获取");
  clock.update(snapshot(null,3));assert.equal(clock.recent(now,now),"系统时区异常");
});
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
      if(path==="/v1/computer")return {...api.settings,installation_id:"install",enabled:false,provider:"aio",endpoint:"",max_screens:2,version:1,status:"disabled",operations:[]};
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

for (const managed of [true, false]) test(`general settings show the server's actual storage paths (managed=${managed})`, async () => {
  const api=fixture(managed);
  const storage={home:"/srv/TeamX <private>",data_dir:"/mnt/business data",teams_dir:"/mnt/team templates"};
  api.settings.storage=storage;
  const ui=await launch(api);
  await ui.el("open-settings").emit("click");
  assert.equal(ui.el("storage-settings").hidden,false);
  for (const [id,key] of [["storage-home","home"],["storage-data","data_dir"],["storage-teams","teams_dir"]]) assert.equal(ui.el(id).textContent,storage[key]);
  assert.equal(ui.el("storage-external").hidden,false);
  assert.equal(ui.el("storage-data-row").hidden,false);
  assert.equal(ui.el("storage-teams-row").hidden,false);
  await saveZone(ui,"UTC");
  assert.equal(ui.el("storage-data").textContent,storage.data_dir);
});

test("storage shows one home and only lists directories outside it", async () => {
  const api=fixture();
  api.settings.storage={home:"/srv/teamx",data_dir:"/srv/teamx/data",teams_dir:"/srv/teamx/teams"};
  const ui=await launch(api);
  assert.equal(ui.el("storage-home").textContent,"/srv/teamx");
  assert.equal(ui.el("storage-external").hidden,true);
  assert.equal(ui.el("storage-data-row").hidden,true);
  assert.equal(ui.el("storage-teams-row").hidden,true);
  const copied=[];
  ui.window.navigator={clipboard:{writeText:async path=>copied.push(path)}};
  await ui.el("storage-copy").emit("click");
  assert.deepEqual(copied,["/srv/teamx"]);
  assert.equal(ui.el("storage-copy").dataset.copied,"true");
  await ui.clock.advance(1500);
  assert.equal(ui.el("storage-copy").dataset.copied,"false");
  ui.window.navigator.clipboard.writeText=async()=>{throw new Error("unavailable");};
  await ui.el("storage-copy").emit("click");
  assert.equal(ui.el("storage-feedback").hidden,false);
  assert.match(ui.el("storage-feedback").textContent,/手动复制/);
  api.settings={...api.settings,settings_version:2,storage:{...api.settings.storage,data_dir:"/srv/teamx-other/data"}};
  await ui.refresh();
  assert.equal(ui.el("storage-external").hidden,false);
  assert.equal(ui.el("storage-data-row").hidden,false);
  assert.equal(ui.el("storage-data").textContent,"/srv/teamx-other/data");
  assert.equal(ui.el("storage-teams-row").hidden,true);
});

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
  assert.match(ui.el("room-list").children[0].children[0].children[1].title,/11月8日.*17:04/);
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
  api.settings=snapshot("UTC",2);await ui.refresh();assert.equal(ui.el("policy-expires").value,"2026-11-08 17:04:13");
  assert.equal(ui.el("policy-text").value,"保留文本");await ui.el("policy-form").emit("submit");
  assert.equal(api.calls.find(c=>c.path==="/v1/policy/update").body.policy.expires_at,"2026-11-08T17:04:13.000Z");
  api.settings=snapshot("America/New_York",3);await ui.refresh();await ui.el("policy-new").emit("click");
  ui.el("policy-expires").value="2026-11-01T01:30";await ui.el("policy-expires").emit("input");await ui.el("policy-form").emit("submit");
  assert.match(ui.el("policy-form-status").textContent,/夏令时/);
  api.settings=snapshot("UTC",4);await ui.refresh();await ui.el("policy-form").emit("submit");
  assert.match(ui.el("policy-form-status").textContent,/重新确认/);
  assert.equal(api.calls.filter(c=>c.path==="/v1/policy/update").length,1);
  await ui.el("policy-expires-toggle").emit("click");await ui.el("date-time-apply").emit("click");
  await ui.el("policy-form").emit("submit");
  assert.equal(api.calls.filter(c=>c.path==="/v1/policy/update").length,2);
  assert.equal(api.calls.filter(c=>c.path==="/v1/policy/update").at(-1).body.policy.expires_at,"2026-11-01T01:30:00.000Z");
});

test("confirming an unchanged expiry keeps its saved milliseconds and DST offset", async () => {
  const api=fixture();api.settings=snapshot("America/New_York");
  const record={id:"precise",version:1,status:"active",text:"保留有效期",expires_at:"2026-11-01T06:30:00.123Z",source:{user:"tester",text:"保留有效期"},changed_by:{kind:"human",id:"tester"},updated_at:"2026-10-01T00:00:00Z"};
  api.hook=path=>path==="/v1/policy/read"?{revision:1,current:[record],effective:[record],history:[record]}:undefined;
  const ui=await launch(api);await openAgent(ui,"policy");
  await ui.el("policy-entries").children[0].children[0].children[1].children[0].emit("click");
  assert.equal(ui.el("policy-expires").value,"2026-11-01 01:30:00");
  await ui.el("policy-expires-toggle").emit("click");await ui.el("date-time-apply").emit("click");
  await ui.el("policy-form").emit("submit");
  assert.equal(api.calls.find(call=>call.path==="/v1/policy/update").body.policy.expires_at,record.expires_at);
});

test("task forms and pickers show only a differing zone and follow global changes without rewriting the task zone", async () => {
  const api=fixture(),ui=await launch(api);
  await ui.el("calendar-new").emit("click");
  assert.doesNotMatch(ui.el("calendar-form-summary").textContent,/Asia\/Shanghai|时区|按 .*执行/);
  await ui.el("calendar-local-time-toggle").emit("click");
  assert.equal(ui.el("date-time-zone").hidden,true);
  await ui.el("date-time-cancel").emit("click");
  ui.el("calendar-time-zone").value="UTC";await ui.el("calendar-time-zone").emit("input");
  assert.match(ui.el("calendar-form-summary").textContent,/按 UTC 执行/);
  await ui.el("calendar-local-time-toggle").emit("click");
  assert.equal(ui.el("date-time-zone").textContent,"时区：UTC");
  assert.equal(ui.el("date-time-zone").hidden,false);
  api.settings=snapshot("UTC",2);await ui.refresh();
  assert.equal(ui.el("date-time-picker").hidden,true);
  assert.equal(ui.el("calendar-time-zone").value,"UTC");
  assert.doesNotMatch(ui.el("calendar-form-summary").textContent,/UTC|按 .*执行/);
  await ui.el("calendar-local-time-toggle").emit("click");assert.equal(ui.el("date-time-zone").hidden,true);
  api.settings=snapshot("Asia/Shanghai",3);await ui.refresh();
  assert.equal(ui.el("calendar-time-zone").value,"UTC");
  assert.match(ui.el("calendar-form-summary").textContent,/按 UTC 执行/);
  assert.equal(api.calls.some(call=>call.path==="/v1/calendar/update"),false);
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
  assert.equal(ui.el("general-settings").hidden,false);assert.equal(ui.el("platform-settings").hidden,true);assert.equal(ui.el("access-settings").hidden,true);
  ui.el("system-timezone").value="America/New_York";await ui.el("system-timezone").emit("input");
  await ui.el("settings-tab-general").emit("keydown",{key:"ArrowDown"});
  assert.equal(ui.el("settings-tab-models").attributes["aria-selected"],"true");
  await ui.el("settings-tab-models").emit("keydown",{key:"ArrowDown"});
  assert.equal(ui.el("settings-tab-platforms").attributes["aria-selected"],"true");
  assert.equal(ui.el("general-settings").hidden,true);assert.equal(ui.el("platform-settings").hidden,false);
  await ui.el("settings-tab-platforms").emit("keydown",{key:"ArrowDown"});
  assert.equal(ui.el("settings-tab-computer").attributes["aria-selected"],"true");
  assert.equal(ui.el("computer-settings").hidden,false);
  await ui.el("settings-tab-computer").emit("keydown",{key:"ArrowDown"});
  assert.equal(ui.el("settings-tab-security").attributes["aria-selected"],"true");
  assert.equal(ui.el("access-settings").hidden,false);assert.equal(ui.el("general-settings").hidden,true);
  await ui.el("settings-tab-platforms").emit("click");
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
  await single.el("settings-tab-general").emit("keydown",{key:"End"});
  assert.equal(single.el("access-settings").hidden,false);assert.equal(single.el("settings-tab-security").attributes["aria-selected"],"true");
});

test("Escape dismisses the time-zone picker from its toggle before closing settings", async () => {
  const api=fixture(),ui=await launch(api);
  await ui.el("open-settings").emit("click");
  await ui.el("timezone-picker-toggle").emit("click");
  let prevented=false,stopped=false;
  await ui.el("timezone-picker").emit("keydown",{key:"Escape",preventDefault(){prevented=true;},stopPropagation(){stopped=true;}});
  assert.equal(prevented,true);assert.equal(stopped,true);
  assert.equal(ui.el("timezone-options").hidden,true);assert.equal(ui.el("settings-dialog").open,true);
  assert.equal(ui.document.activeElement,ui.el("timezone-picker-toggle"));
  await ui.el("timezone-picker-toggle").emit("click");
  prevented=false;
  await ui.el("settings-dialog").emit("cancel",{preventDefault(){prevented=true;}});
  assert.equal(prevented,true);assert.equal(ui.el("timezone-options").hidden,true);
  assert.equal(api.calls.some(c=>c.path==="/v1/settings/save"),false);
});

for (const managed of [true,false]) test(`computer settings validate/save online and recover a lost receipt (managed=${managed})`, async () => {
  const api=fixture(managed),ui=await launch(api);
  let data={...api.settings,...(!managed?{room_id:"A",room_instance:"instance-A"}:{}),installation_id:"install",enabled:false,provider:"aio",endpoint:"",max_screens:2,version:1,has_credential:false,status:"disabled",operations:[]};
  api.hook=(path,body)=>{
    if(path==="/v1/computer")return structuredClone(data);
    if(path==="/v1/computer/validate")return {running_screens:0,max_screens:4};
    if(path==="/v1/computer/save"){
      assert.equal(body.credential_action,"replace");assert.equal(body.service_epoch,data.service_epoch);assert.equal(body.installation_id,"install");
      assert.equal(body.room_id,managed?undefined:"A");assert.match(body.request_id,/^[a-f0-9]{32}$/);
      data={...data,endpoint:body.endpoint,enabled:true,version:2,has_credential:true,status:"ready",operations:[{id:body.request_id,status:"completed",stage:"done",message:"已保存并应用。"}]};
      throw Error("response lost after acceptance");
    }
  };
  await ui.el("open-settings").emit("click");await ui.el("settings-tab-computer").emit("click");await settle();
  assert.equal(ui.el("computer-enabled").checked,false);assert.equal(ui.el("computer-settings").hidden,false);
  const key="test-runtime-private-management-credential";
  ui.el("computer-enabled").checked=true;ui.el("computer-endpoint").value="http://127.0.0.1:18090";ui.el("computer-credential").value=key;
  await ui.el("computer-test").emit("click");
  assert.match(ui.el("computer-feedback").textContent,/核验通过/);assert.equal(ui.el("computer-credential").value,key);
  assert.equal(api.calls.filter(c=>c.path==="/v1/computer/save").length,0);
  await ui.el("computer-form").emit("submit");await settle();
  assert.equal(ui.el("computer-credential").value,"");assert.match(ui.el("computer-progress").textContent,/已完成/);
  assert.equal(api.calls.filter(c=>c.path==="/v1/computer/save").length,1);
  await ui.el("computer-refresh").emit("click");await settle();
  assert.equal(ui.el("computer-enabled").checked,true);assert.equal(ui.el("computer-credential").value,"");
  assert.match(ui.el("computer-credential").placeholder,/留空保留/);
  await ui.el("settings-tab-general").emit("click");assert.equal(ui.el("computer-credential").value,"");
});

test("computer settings keep one pending operation and never replay a failed connection test", async () => {
  const api=fixture(),ui=await launch(api);
  let data={...api.settings,installation_id:"install",enabled:false,provider:"aio",endpoint:"http://127.0.0.1:18090",max_screens:2,version:1,has_credential:true,status:"disabled",operations:[]};
  api.hook=(path)=>{
    if(path==="/v1/computer")return structuredClone(data);
    if(path==="/v1/computer/validate")throw Error("连接不可用");
  };
  await ui.el("open-settings").emit("click");await ui.el("settings-tab-computer").emit("click");await settle();
  await ui.el("computer-test").emit("click");assert.match(ui.el("computer-feedback").textContent,/连接不可用/);
  assert.equal(api.calls.filter(c=>c.path==="/v1/computer/validate").length,1);
  data.operations=[{id:"a".repeat(32),status:"waiting",stage:"settle",message:"等待原电脑结清"}];data.status="applying";
  await ui.el("computer-refresh").emit("click");await settle();
  assert.equal(ui.el("computer-save").disabled,true);assert.match(ui.el("computer-progress").textContent,/等待原电脑结清/);
  await ui.el("settings-tab-general").emit("click");
  assert.equal(ui.el("general-settings").hidden,false);
});

test("computer application observed from another page rebases the form together with its version", async () => {
  const api=fixture(),ui=await launch(api);
  let data={...api.settings,installation_id:"install",enabled:false,provider:"aio",endpoint:"",max_screens:2,version:1,status:"applying",operations:[{id:"b".repeat(32),status:"waiting",stage:"commit",message:"应用中"}]};
  api.hook=path=>path==="/v1/computer"?structuredClone(data):undefined;
  await ui.el("open-settings").emit("click");await ui.el("settings-tab-computer").emit("click");await settle();
  assert.equal(ui.el("computer-save").disabled,true);assert.equal(ui.el("computer-enabled").checked,false);
  data={...data,enabled:true,endpoint:"https://computer.example.test",max_screens:3,version:2,status:"ready",has_credential:true,operations:[{id:"b".repeat(32),status:"completed",stage:"done",message:"已保存并应用。"}]};
  await ui.clock.advance(1500);await settle();
  assert.equal(ui.el("computer-enabled").checked,true);assert.equal(ui.el("computer-endpoint").value,data.endpoint);
  assert.equal(ui.el("computer-max-screens").value,"3");assert.equal(ui.el("computer-save").disabled,false);
  assert.equal(api.calls.some(c=>c.path==="/v1/computer/save"),false);
});

test("managed computer settings prepare without manual connection fields and allow disable while preparing", async()=>{
  const api=fixture(),ui=await launch(api);
  let data={...api.settings,installation_id:"install",enabled:false,provider:"aio",endpoint:"",max_screens:2,version:1,runtime_mode:"manual",suggested_runtime_mode:"docker",docker_cpus:4,docker_memory_gib:6,status:"disabled",operations:[]};
  api.hook=(path,body)=>{
    if(path==="/v1/computer")return structuredClone(data);
    if(path==="/v1/computer/check")return {emulated:true,architecture:"aarch64"};
    if(path==="/v1/computer/save"){
      assert.equal(body.runtime_mode,"docker");assert.equal(body.endpoint,"");assert.equal(body.credential,"");assert.equal(body.credential_action,"keep");
      if(body.enabled){data={...data,can_disable:true,status:"applying",operations:[{id:body.request_id,status:"applying",stage:"runtime_prepare"}]};return data.operations[0];}
      data={...data,version:2,can_disable:false,runtime_mode:"docker",suggested_runtime_mode:"docker",status:"disabled",operations:[{id:body.request_id,status:"completed",stage:"done"}]};return data.operations[0];
    }
  };
  await ui.el("open-settings").emit("click");await ui.el("settings-tab-computer").emit("click");await settle();
  assert.equal(ui.el("computer-runtime-mode").value,"docker");assert.equal(ui.el("computer-manual-fields").hidden,true);assert.equal(ui.el("computer-enabled").checked,false);
  await ui.el("computer-test").emit("click");assert.match(ui.el("computer-feedback").textContent,/架构模拟/);
  assert.equal(api.calls.filter(c=>c.path==="/v1/computer/save").length,0);
  ui.el("computer-enabled").checked=true;await ui.el("computer-form").emit("submit");await settle();
  assert.equal(ui.el("computer-enabled").disabled,false);assert.equal(ui.el("computer-runtime-mode").disabled,true);assert.equal(ui.el("computer-save").disabled,true);
  ui.el("computer-enabled").checked=false;await ui.el("computer-enabled").emit("change");assert.equal(ui.el("computer-save").disabled,false);
  await ui.el("computer-form").emit("submit");await settle();
  assert.equal(ui.el("computer-enabled").checked,false);assert.equal(api.calls.filter(c=>c.path==="/v1/computer/save").length,2);
});

test("managed computer deployment limits stay fixed after creation and switching mode clears a draft key", async()=>{
  const api=fixture(),ui=await launch(api);
  const data={...api.settings,installation_id:"install",enabled:false,provider:"aio",endpoint:"",max_screens:2,version:3,runtime_mode:"docker",docker_cpus:4,docker_memory_gib:6,docker_created:true,status:"disabled",operations:[]};
  api.hook=path=>path==="/v1/computer"?structuredClone(data):undefined;
  await ui.el("open-settings").emit("click");await ui.el("settings-tab-computer").emit("click");await settle();
  assert.equal(ui.el("computer-docker-cpus").disabled,true);assert.equal(ui.el("computer-docker-memory").disabled,true);
  ui.el("computer-runtime-mode").value="manual";await ui.el("computer-runtime-mode").emit("change");assert.equal(ui.el("computer-manual-fields").hidden,false);
  ui.el("computer-credential").value="draft-private-key";ui.el("computer-runtime-mode").value="docker";await ui.el("computer-runtime-mode").emit("change");
  assert.equal(ui.el("computer-credential").value,"");assert.equal(ui.el("computer-endpoint").disabled,true);
});
