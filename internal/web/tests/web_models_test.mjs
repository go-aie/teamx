import { test } from "node:test";
import assert from "node:assert/strict";
import {launch, settle} from "./web_app_harness.mjs";
import {fillModelParameters, readModelParameters} from "../assets/model-form.mjs";

const textOf = el => [el.textContent, ...el.children.map(textOf)].join("\n");
const descendants = el => [el, ...el.children.flatMap(descendants)];
function fixture() {
 const info = {room_id:"A",room_instance:"instance-A",service_epoch:"runtime"};
 const identity = {room_id:"A",room_instance:"instance-A",agent_id:"assistant",membership_id:"member-A"};
 const api = {calls:[], hook:null, models:{catalog:{version:1,default:"A",entries:[{id:"A",name:"日常",model_id:"model-A",protocol:"openai-completions",revision:1,compat:{supports_developer_role:false}},{id:"B",name:"视觉",model_id:"model-B",protocol:"openai-completions",revision:1}]},members:[{...identity,selection:{mode:"default"},version:1,target_entry_id:"A",target_revision:1,actual:{entry_id:"A",revision:1,model_id:"model-A",pending:false}}],operations:[]},
 async request(path,body) {
  api.calls.push({path,body:structuredClone(body)});
  if (api.hook) {const result=api.hook(path,body);if(result!==undefined)return result;}
  if (path==="/v1/ui") return {managed:true,service_epoch:"manager"};
  if (path==="/v1/rooms") return {service_epoch:"manager",operator:"tester",rooms:[{...info,name:"A",status:"ready",runtime_status:"ready",service:info}],teams:[]};
  if (path==="/v1/member/list") return {members:[{agent_id:"assistant",membership_id:"member-A",status:"idle",runner:{profile:"runtime"}}]};
  if (path==="/v1/member/activity") return {member:{agent_id:"assistant",membership_id:"member-A",status:"idle"}};
  if (path==="/v1/read") return {messages:[],snapshot_seq:0,has_more:false};
  if (path.startsWith("/v1/workspace?")) return {workspace:{root:"workspaces"},workspace_version:1,members:[]};
  if (path.startsWith("/v1/platform/notifications?")) return {subscriptions:[]};
  if (path.startsWith("/v1/platforms?")) return {platforms:[]};
  if (path.startsWith("/v1/tools/bindings?")) return {catalog:[],bindings:[]};
  if (path==="/v1/models") return structuredClone(api.models);
  if (path==="/v1/models/change") {const op={id:body.request_id,status:"validating",members:[],completed:0,waiting:0,failed:0};api.models.operations.unshift(op);return op;}
  throw Error(`Unexpected request ${path}`);
 }};
 return api;
}
async function openSettings(ui) {await ui.el("open-settings").emit("click");await ui.el("settings-tab-models").emit("click");await settle();}
async function openMember(ui) {await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();}

test("member settings start collapsed, retain their state while polling and reset on reopening",async()=>{
 const api=fixture(),ui=await launch(api);await openMember(ui);
 assert.equal(ui.el("agent-controls").hidden,true);assert.equal(ui.el("toggle-agent-controls").getAttribute("aria-expanded"),"false");
 assert.equal(ui.el("agent-status").textContent,"等待消息");assert.equal(ui.el("agent-model-alert").hidden,true);
 await ui.el("toggle-agent-controls").emit("click");assert.equal(ui.el("agent-controls").hidden,false);
 assert.equal(ui.el("toggle-agent-controls").getAttribute("aria-expanded"),"true");
 await ui.refresh();assert.equal(ui.el("agent-controls").hidden,false);
 await ui.el("close-activity").emit("click");await openMember(ui);assert.equal(ui.el("agent-controls").hidden,true);
 assert.equal(api.calls.some(c=>c.path==="/v1/models/change"),false);
});

test("running members can pause from the collapsed header and runtime failures remain visible",async()=>{
 const api=fixture(),member={agent_id:"assistant",membership_id:"member-A",status:"running",runner:{profile:"runtime"},execution:{active:true,paused:false}};
 api.hook=(path,body)=>{
  if(path==="/v1/member/list")return {members:[structuredClone(member)]};
  if(path==="/v1/member/pause"){
   assert.equal(body.agent_id,"assistant");assert.equal(body.membership_id,"member-A");
   member.execution.active=false;member.execution.paused=true;member.status="paused";return {};
  }
 };
 const ui=await launch(api);await openMember(ui);
 assert.equal(ui.el("agent-controls").hidden,true);assert.equal(ui.el("agent-quick-actions").hidden,false);
 assert.equal(ui.el("agent-quick-actions").children[0].textContent,"暂停");
 await ui.el("agent-quick-actions").children[0].emit("click");await settle();
 assert.equal(api.calls.filter(c=>c.path==="/v1/member/pause").length,1);
 assert.equal(ui.el("agent-controls").hidden,true);assert.equal(ui.el("agent-quick-actions").hidden,true);
 member.execution.error="服务暂不可用";member.status="backend_blocked";await ui.refresh();
 assert.equal(ui.el("agent-model-alert").textContent,"运行异常");assert.equal(ui.el("toggle-agent-controls").dataset.state,"error");
 await ui.el("toggle-agent-controls").emit("click");assert.equal(ui.el("agent-controls").hidden,false);
 assert.equal(ui.el("agent-error").textContent,"服务暂不可用");
});

test("member model picker names its default and distinguishes applied, pending and unstarted sessions",async()=>{
 const api=fixture(),ui=await launch(api);await openMember(ui);
 assert.equal(ui.el("member-model-selection").children[0].textContent,"跟随默认 · 日常");
 assert.equal(ui.el("member-model-status").textContent,"已生效");assert.equal(ui.el("member-model-ok").hidden,false);
 api.models.members[0].actual.pending=true;await ui.clock.advance(2000);
 assert.equal(ui.el("member-model-status").textContent,"待启动");assert.equal(ui.el("member-model-ok").hidden,true);
 delete api.models.members[0].actual;await ui.clock.advance(2000);
 assert.equal(ui.el("member-model-status").textContent,"未启动");assert.equal(ui.el("member-model-ok").hidden,true);
});

test("model drafts survive reload with original version and compatibility while excluding keys",async()=>{
 const api=fixture(),saved=new Map(),ui=await launch(api,saved);await openSettings(ui);
 await ui.el("model-list").children[0].children[0].emit("click");
 ui.el("model-entry-name").value="我的模型草稿";await ui.el("model-entry-name").emit("input");
 ui.el("catalog-api-key").value="private-model-key";
 await ui.el("catalog-toggle-key").emit("click");assert.equal(ui.el("catalog-api-key").type,"text");
 ui.el("catalog-developer-role").value="false";await ui.el("catalog-developer-role").emit("change");
 ui.el("catalog-context-window").value="128000";await ui.el("catalog-context-window").emit("input");
 await ui.window.emit("pagehide");assert.equal(ui.el("catalog-api-key").value,"");
 assert.equal(ui.el("catalog-api-key").type,"password");
 assert.doesNotMatch(JSON.stringify([...saved]),/private-model-key/);
 api.models.catalog.version=2;
 const reopened=await launch(api,saved);await openSettings(reopened);
 assert.equal(reopened.el("model-entry-name").value,"我的模型草稿");
 assert.equal(reopened.el("catalog-developer-role").value,"false");
 assert.equal(reopened.el("catalog-context-window").value,"128000");
 assert.match(reopened.el("model-feedback").textContent,/原预期版本保留/);
 await reopened.el("model-edit-form").emit("submit");await settle();
 assert.equal(api.calls.find(c=>c.path==="/v1/models/change").body.expected_version,1);
});

test("model recovery reads the original uncertain operation after reload without resubmitting",async()=>{
 const api=fixture(),saved=new Map();let operation,online=false;
 api.hook=(path,body)=>{
  if(path==="/v1/models/change") {operation={id:body.request_id,status:"applying",members:[],completed:0,waiting:1,failed:0};throw Error("lost receipt");}
  if(path.startsWith("/v1/models/operation?")) {if(!online)throw Error("offline");assert.equal(new URL(path,"http://fixture").searchParams.get("id"),operation.id);return operation;}
 };
 const ui=await launch(api,saved);await openSettings(ui);await ui.el("model-new").emit("click");
 ui.el("model-entry-name").value="草稿";await ui.el("model-entry-name").emit("input");
 ui.el("catalog-model-id").value="candidate";ui.el("catalog-api-key").value="do-not-save";
 await ui.el("model-edit-form").emit("submit");await settle();await ui.window.emit("pagehide");
 assert.doesNotMatch(JSON.stringify([...saved]),/do-not-save/);
 online=true;const reopened=await launch(api,saved);await openSettings(reopened);
 assert.match(textOf(reopened.el("model-operations")),/正在应用/);
 assert.equal(reopened.el("model-edit-form").hidden,true);
 assert.equal(api.calls.filter(c=>c.path==="/v1/models/change").length,1);
 assert.ok(api.calls.filter(c=>c.path.includes("operation?id=")).length>=2);
});

test("model list polls retain button focus and stop display reads while hidden",async()=>{
 const api=fixture(),ui=await launch(api);await openSettings(ui);
 const row=ui.el("model-list").children[0],edit=row.children[0];edit.focus();
 await ui.clock.advance(2000);assert.equal(ui.el("model-list").children[0],row);assert.equal(ui.document.activeElement,edit);
 ui.document.hidden=true;await ui.document.emit("visibilitychange");const calls=api.calls.length;
 await ui.clock.advance(5000);assert.equal(api.calls.length,calls);
});

test("online editor reuses credentials and shared three-state parameters without replacing an external draft",async()=>{
 const api=fixture(),ui=await launch(api);
 await openSettings(ui);const row=ui.el("model-list").children[0];await row.children[0].emit("click");
 assert.equal(ui.el("catalog-api-key").hidden,true);assert.equal(ui.el("catalog-developer-role").value,"false");
 assert.equal(ui.el("catalog-key-input").hidden,true);
 ui.el("model-entry-name").value="我的名称";
 // Another tab publishes a catalog revision; the editor retains its original
 // expected version so saving a stale draft produces a server conflict.
 api.models.catalog.version=2;
 await openSettings(ui);
 assert.equal(ui.el("model-entry-name").value,"我的名称");
 await ui.el("model-edit-form").emit("submit");await settle();
 const sent=api.calls.find(call=>call.path==="/v1/models/change").body;
 assert.equal(sent.expected_version,1);assert.equal(sent.entry.name,"我的名称");
 assert.equal(sent.entry.compat.supports_developer_role,false);assert.equal("api_key" in sent,false);assert.equal("api_key_env" in sent.entry,false);
 assert.equal(ui.el("catalog-api-key").value,"");
 assert.match(textOf(ui.el("model-operations")),/正在验证/);
});

test("compact member picker applies immediately with the captured versions and retains the choice while validating",async()=>{
 const api=fixture(),ui=await launch(api);await openMember(ui);
 const selector=ui.el("member-model-selection");assert.equal(selector.children.length,3);assert.equal(selector.children[0].value,"default");assert.equal(selector.children[1].value,"A");
 assert.match(ui.el("member-model-progress").title,/model-A/);
 selector.value="A";await selector.emit("change");
 api.models.members[0].version=2;api.models.members[0].selection={mode:"explicit",entry_id:"B"};api.models.catalog.version=2;
 await openSettings(ui);assert.equal(selector.value,"A");
 const sent=api.calls.find(call=>call.path==="/v1/models/change").body;
 assert.deepEqual(sent.selection,{mode:"explicit",entry_id:"A"});assert.equal(sent.expected_selection_version,1);assert.equal(sent.expected_version,1);assert.equal(sent.membership_id,"member-A");
 assert.equal(selector.disabled,true);assert.equal(ui.el("member-model-progress").dataset.state,"busy");
 assert.equal(ui.el("member-model-save"),undefined);
 api.models.operations[0].status="completed";api.models.members[0].selection={mode:"explicit",entry_id:"A"};
 await ui.clock.advance(800);assert.equal(selector.disabled,false);assert.equal(selector.value,"A");assert.equal(ui.el("member-model-progress").dataset.state,"ready");
});

test("member picker restores the published choice and exposes validation failures through its status icon",async()=>{
 const api=fixture(),ui=await launch(api);await openMember(ui);
 const selector=ui.el("member-model-selection");selector.value="B";await selector.emit("change");
 api.models.operations[0].status="failed";api.models.operations[0].message="模型验证失败";
 await ui.clock.advance(800);
 assert.equal(selector.value,"default");assert.equal(selector.disabled,false);
 assert.equal(ui.el("member-model-progress").dataset.state,"error");assert.match(ui.el("member-model-progress").title,/模型验证失败/);assert.equal(ui.el("member-model-error-icon").hidden,false);
 await ui.el("member-model-progress").emit("click");assert.equal(ui.el("member-model-details").hidden,false);
 assert.match(ui.el("member-model-feedback").textContent,/模型验证失败/);
 assert.match(ui.el("member-model-actual").textContent,/model-A/);
 await ui.el("member-model-settings").emit("click");await settle();assert.equal(ui.el("model-settings").hidden,false);
});

test("shared model form preserves compat omission, false and true and clears it on protocol change",()=>{
 const fields=new Map();const $=id=>{if(!fields.has(id))fields.set(id,{value:"",hidden:false});return fields.get(id);};
 for(const value of [undefined,false,true]){fillModelParameters($,{protocol:"openai-completions",model_id:"A",compat:value===undefined?{}:{supports_developer_role:value},reasoning_effort:"low"});assert.equal(readModelParameters($).compat?.supports_developer_role,value);assert.equal(readModelParameters($).reasoning_effort,"low");}
 fillModelParameters($,{protocol:"deepseek-messages",model_id:"B",compat:{supports_developer_role:false}});assert.equal(readModelParameters($).compat,undefined);assert.equal($("developer-role").value,"");
});

test("deletion previews default and fixed references, keeps the expected version, and excludes pending targets",async()=>{
 const api=fixture(),ui=await launch(api);await openSettings(ui);
 const row=ui.el("model-list").children[0];await row.children[0].emit("click");await ui.el("model-delete").emit("click");
 const review=ui.el("model-action-review").children.find(el=>el.id==="model-delete-confirm");assert.match(textOf(review),/使用默认者保持默认/);assert.match(textOf(review),/含未打开/);
 assert.equal(ui.el("model-save-actions").hidden,true);
 await ui.el("model-edit-form").emit("submit");assert.equal(api.calls.filter(c=>c.path==="/v1/models/change").length,0);
 const selector=review.children.find(el=>el.id==="model-delete-replacement");assert.equal(selector.children.length,1);assert.equal(selector.value,"B");
 api.models.catalog.version=2;
 await review.children.find(el=>el.textContent==="验证并删除、替换引用").emit("click");await settle();
 const sent=api.calls.find(c=>c.path==="/v1/models/change").body;
 assert.equal(sent.kind,"delete");assert.equal(sent.entry_id,"A");assert.equal(sent.replacement_id,"B");assert.equal(sent.expected_version,1);
 api.models.catalog.entries[0].pending_deletion=true;await openSettings(ui);
 assert.match(textOf(ui.el("model-list").children[0]),/待删除/);assert.equal(ui.el("model-list").children[0].children[0].disabled,true);
 await openMember(ui);assert(!ui.el("member-model-selection").children.some(el=>el.value==="A"));
});

test("unverified missing-key imports request only the key and template references block deletion",async()=>{
 const api=fixture();api.models.catalog.entries[0].unverified=true;api.models.entry_states={A:{validated:false,credential_configured:false,templates:["team / worker"]}};
 const ui=await launch(api);await openSettings(ui);let row=ui.el("model-list").children[0];assert.match(textOf(row),/请补填 API Key/);
 await row.children[0].emit("click");assert.equal(ui.el("catalog-api-key").hidden,false);assert.equal(ui.el("catalog-replace-key").checked,true);
 await ui.el("model-delete").emit("click");const review=ui.el("model-action-review").children.find(el=>el.id==="model-delete-confirm");assert.match(textOf(review),/请先修正模板引用/);assert(!review.children.some(el=>el.textContent==="验证并删除、替换引用"));
});

test("completed model operations stay out of the list and failures remain actionable",async()=>{
 const api=fixture();
 const completed={id:"latest",status:"completed",committed:true,message:"模型配置已保存，相关成员已完成应用。",members:[],completed:2,waiting:0,failed:0};
 api.models.operations=[completed,{id:"old",status:"failed",committed:false,message:"unsupported_installation",members:[],completed:0,waiting:0,failed:0}];
 const ui=await launch(api);await openSettings(ui);
 const list=ui.el("model-operations"),row=list.children[0];
 assert.equal(list.children.length,0);assert.doesNotMatch(textOf(list),/已完成|unsupported_installation|需处理|历史记录/);
 await ui.clock.advance(2000);assert.equal(list.children[0],row);
 api.models.operations.unshift({id:"new",status:"failed",committed:true,message:"部分成员未完成",members:[{agent_id:"assistant",status:"failed",error:"应用失败"}],completed:0,waiting:0,failed:1});
 await ui.clock.advance(2000);
 assert.equal(list.children.length,1);assert.match(textOf(list),/部分成员未完成/);assert(list.children[0].children.some(el=>el.textContent==="重试未完成成员"));
 const failed=list.children[0];await ui.clock.advance(2000);assert.equal(list.children[0],failed);
});

test("submission feedback clears when validation finishes and a new failure stays visible",async()=>{
 const api=fixture(),ui=await launch(api);await openSettings(ui);
 await ui.el("model-list").children[0].children[0].emit("click");
 await ui.el("model-edit-form").emit("submit");await settle();
 assert.equal(ui.el("model-feedback").textContent,"");assert.match(textOf(ui.el("model-operations")),/正在验证/);
 api.models.operations[0].status="failed";api.models.operations[0].message="验证失败，请检查模型地址。";
 await ui.clock.advance(800);
 assert.equal(ui.el("model-feedback").textContent,"");assert.match(textOf(ui.el("model-operations")),/验证失败，请检查模型地址/);
 assert.doesNotMatch(textOf(ui.el("model-operations")),/完成 0 \/ 等待 0 \/ 失败 0/);
});

test("models have an independent settings tab that remains hidden during background polling",async()=>{
 const api=fixture(),ui=await launch(api);
 await ui.el("open-settings").emit("click");await settle();
 assert.equal(ui.el("general-settings").hidden,false);assert.equal(ui.el("model-settings").hidden,true);
 await ui.el("settings-tab-general").emit("keydown",{key:"ArrowDown"});await settle();
 assert.equal(ui.el("settings-tab-models").getAttribute("aria-selected"),"true");assert.equal(ui.el("model-settings").hidden,false);assert.equal(ui.el("general-settings").hidden,true);
 await ui.el("settings-tab-models").emit("keydown",{key:"ArrowDown"});await settle();
 assert.equal(ui.el("platform-settings").hidden,false);assert.equal(ui.el("model-settings").hidden,true);
 await ui.clock.advance(2000);assert.equal(ui.el("model-settings").hidden,true);
 await ui.el("settings-tab-platforms").emit("keydown",{key:"Home"});assert.equal(ui.el("general-settings").hidden,false);
 await openMember(ui);await ui.el("member-model-progress").emit("click");await ui.el("member-model-settings").emit("click");await settle();
 assert.equal(ui.el("model-settings").hidden,false);assert.equal(ui.el("settings-tab-models").getAttribute("aria-selected"),"true");
});

test("newer completed operations cannot hide older failed or applying changes and retries keep the original ID",async()=>{
 const api=fixture();
 const failed={id:"old-failed",status:"failed",committed:true,message:"旧成员切换失败",members:[{agent_id:"assistant",status:"failed",error:"应用失败"}],completed:0,waiting:0,failed:1};
 api.models.operations=[{id:"latest",status:"completed",members:[]},failed,{id:"waiting",status:"applying",committed:true,members:[],completed:0,waiting:1,failed:0}];
 api.hook=(path,body)=>path==="/v1/models/operation" ? (assert.equal(body.id,failed.id),{...failed,status:"applying"}) : undefined;
 const ui=await launch(api);await openSettings(ui);
 const list=ui.el("model-operations"),row=list.children[0],retry=row.children.find(el=>el.textContent==="重试未完成成员");
 assert.equal(list.children.length,2);assert.match(textOf(row),/旧成员切换失败/);
 retry.focus();api.models.operations[2].waiting=2;await ui.clock.advance(800);
 assert.equal(list.children[0],row);assert.equal(ui.document.activeElement,retry);
 await retry.emit("click");await settle();
 assert.equal(api.calls.filter(c=>c.path==="/v1/models/change").length,0);
 assert.deepEqual(api.calls.find(c=>c.path==="/v1/models/operation").body.action,"retry");
});

test("touch member details expose target, actual model and a retry bound to the failed operation",async()=>{
 const api=fixture(),member=api.models.members[0];
 member.target_entry_id="B";member.selection={mode:"explicit",entry_id:"B"};member.operation_id="failed-selection";
 api.models.operations=[{id:"failed-selection",status:"failed",committed:true,message:"切换未完成，请重试",members:[{...member,status:"failed",error:"应用失败"}],completed:0,waiting:0,failed:1}];
 api.hook=(path,body)=>path==="/v1/models/operation" ? (assert.equal(body.id,"failed-selection"),api.models.operations[0]) : undefined;
 const ui=await launch(api);await openMember(ui);
 assert.equal(ui.el("agent-controls").hidden,true);assert.equal(ui.el("agent-model-alert").textContent,"模型异常");
 assert.equal(ui.el("member-model-details").hidden,true);
 await ui.el("toggle-agent-controls").emit("click");assert.equal(ui.el("agent-controls").hidden,false);
 assert.equal(ui.el("member-model-progress").getAttribute("aria-expanded"),"true");
 assert.match(ui.el("member-model-target").textContent,/model-B/);assert.match(ui.el("member-model-actual").textContent,/model-A/);
 assert.match(ui.el("member-model-feedback").textContent,/切换未完成/);assert.equal(ui.el("member-model-retry").hidden,false);
 await ui.el("member-model-retry").emit("click");assert.equal(api.calls.find(c=>c.path==="/v1/models/operation").body.action,"retry");
 await ui.el("member-model-progress").emit("click");assert.equal(ui.el("member-model-details").hidden,true);
 await ui.el("toggle-agent-controls").emit("click");assert.equal(ui.el("agent-controls").hidden,true);
 assert.equal(ui.el("toggle-agent-controls").getAttribute("aria-expanded"),"false");
 assert.equal(api.calls.filter(c=>c.path==="/v1/models/change").length,0);
});

test("model list opens one editor and returning discards its secret without submitting",async()=>{
 const api=fixture(),ui=await launch(api);await openSettings(ui);
 const row=ui.el("model-list").children[0];
 assert.doesNotMatch(textOf(row),/revision|已验证/);
 await row.children[0].emit("click");
 assert.equal(ui.el("model-list-view").hidden,true);assert.equal(ui.el("model-edit-form").hidden,false);
 ui.el("catalog-api-key").value="discard-this-key";
 await ui.el("model-edit-back").emit("click");
 assert.equal(ui.el("catalog-api-key").value,"");assert.equal(ui.el("model-list-view").hidden,false);
 assert.equal(ui.el("model-edit-form").hidden,true);assert.equal(ui.document.activeElement,ui.el("model-new"));
 assert.equal(api.calls.filter(c=>c.path==="/v1/models/change").length,0);
 await ui.el("model-new").emit("click");
 assert.equal(ui.el("model-list-view").hidden,true);assert.equal(ui.el("model-entry-name").value,"");
 assert.equal(ui.el("model-impact").textContent,"");
});

test("model rows use readable conversation names and keep usage separate from editing",async()=>{
 const api=fixture();
 api.hook=path=>path==="/v1/rooms" ? {service_epoch:"manager",operator:"tester",rooms:[{room_id:"A",room_instance:"instance-A",name:"软件开发",members:[{id:"assistant",name:"角色说明",agent_name:"开发助理"}],status:"ready",runtime_status:"ready",service:{room_id:"A",room_instance:"instance-A",service_epoch:"runtime"}}],teams:[]} : undefined;
 api.models.members.push({...api.models.members[0],room_instance:"previous-instance"});
 const ui=await launch(api);await openSettings(ui);
 const row=ui.el("model-list").children[0],usage=row.children.find(el=>el.className==="model-users");
 assert.equal(descendants(row).filter(el=>el.tag==="button").length,1);
 assert.match(textOf(usage),/软件开发 \/ 开发助理 · 跟随默认/);
 assert.match(textOf(usage),/未命名会话 \/ assistant/);
 assert.doesNotMatch(textOf(usage),/instance-A|previous-instance/);
 await usage.emit("click");assert.equal(ui.el("model-edit-form").hidden,true);
 await row.children[0].emit("click");
 assert.equal(ui.el("model-management-actions").hidden,false);
 assert.equal(ui.el("model-default-state").hidden,false);assert.equal(ui.el("model-set-default").hidden,true);
 await ui.el("model-edit-back").emit("click");await ui.el("model-new").emit("click");
 assert.equal(ui.el("model-management-actions").hidden,true);
});

test("automatic model names use the model ID without duplicating it or changing stored aliases",async()=>{
 const api=fixture(),entry=api.models.catalog.entries[0];
 entry.id="daily";entry.name="原默认模型";entry.model_id="deepseek-flash";
 api.models.catalog.default="daily";api.models.members[0].target_entry_id="daily";
 const ui=await launch(api);await openSettings(ui);
 let row=ui.el("model-list").children[0];
 assert.equal(textOf(row).match(/deepseek-flash/g).length,1);assert.doesNotMatch(textOf(row),/原默认模型/);
 assert.equal(entry.name,"原默认模型");
 await row.children[0].emit("click");assert.equal(ui.el("model-entry-name").value,"");
 assert.equal(ui.el("model-entry-name").placeholder,"deepseek-flash");
 await ui.el("model-edit-back").emit("click");entry.name="快速模型";await ui.clock.advance(2000);
 row=ui.el("model-list").children[0];assert.match(textOf(row),/快速模型/);assert.match(textOf(row),/deepseek-flash/);
});

test("blank model display names save the model ID and custom names remain editable",async()=>{
 const api=fixture(),ui=await launch(api);await openSettings(ui);await ui.el("model-new").emit("click");
 ui.el("catalog-model-id").value="deepseek-flash";await ui.el("catalog-model-id").emit("input");
 assert.equal(ui.el("model-entry-name").placeholder,"deepseek-flash");
 ui.el("catalog-api-key").value="private-key";await ui.el("model-edit-form").emit("submit");await settle();
 assert.equal(api.calls.find(c=>c.path==="/v1/models/change").body.entry.name,"deepseek-flash");
});

test("default model changes stay in the editor and keep its captured catalog version",async()=>{
 const api=fixture(),ui=await launch(api);await openSettings(ui);await ui.el("model-list").children[1].children[0].emit("click");
 assert.equal(ui.el("model-default-state").hidden,true);assert.equal(ui.el("model-set-default").hidden,false);
 api.models.catalog.version=2;await ui.clock.advance(2000);
 await ui.el("model-set-default").emit("click");
 const review=ui.el("model-action-review").children.find(el=>el.id==="model-default-confirm");
 assert.equal(ui.el("model-list-view").hidden,true);
 await review.children.find(el=>el.textContent==="验证并设为默认").emit("click");await settle();
 const sent=api.calls.find(c=>c.path==="/v1/models/change").body;
 assert.equal(sent.kind,"default");assert.equal(sent.entry_id,"B");assert.equal(sent.expected_version,1);assert.equal("api_key" in sent,false);
});
