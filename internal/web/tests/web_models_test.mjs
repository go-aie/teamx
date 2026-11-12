import { test } from "node:test";
import assert from "node:assert/strict";
import {launch, settle} from "./web_app_harness.mjs";
import {fillModelParameters, readModelParameters} from "../assets/model-form.mjs";

const textOf = el => [el.textContent, ...el.children.map(textOf)].join("\n");
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

test("online editor reuses credentials and shared three-state parameters without replacing an external draft",async()=>{
 const api=fixture(),ui=await launch(api);
 await openSettings(ui);const row=ui.el("model-list").children[0];await row.children.find(el=>el.textContent==="编辑").emit("click");
 assert.equal(ui.el("catalog-api-key").hidden,true);assert.equal(ui.el("catalog-developer-role").value,"false");
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
 await ui.el("member-model-progress").emit("click");await settle();assert.equal(ui.el("model-settings").hidden,false);
});

test("shared model form preserves compat omission, false and true and clears it on protocol change",()=>{
 const fields=new Map();const $=id=>{if(!fields.has(id))fields.set(id,{value:"",hidden:false});return fields.get(id);};
 for(const value of [undefined,false,true]){fillModelParameters($,{protocol:"openai-completions",model_id:"A",compat:value===undefined?{}:{supports_developer_role:value},reasoning_effort:"low"});assert.equal(readModelParameters($).compat?.supports_developer_role,value);assert.equal(readModelParameters($).reasoning_effort,"low");}
 fillModelParameters($,{protocol:"deepseek-messages",model_id:"B",compat:{supports_developer_role:false}});assert.equal(readModelParameters($).compat,undefined);assert.equal($("developer-role").value,"");
});

test("deletion previews default and fixed references, keeps the expected version, and excludes pending targets",async()=>{
 const api=fixture(),ui=await launch(api);await openSettings(ui);
 const row=ui.el("model-list").children[0];await row.children.find(el=>el.textContent==="删除").emit("click");
 const review=row.children.find(el=>el.id==="model-delete-confirm");assert.match(textOf(review),/使用默认者保持默认/);assert.match(textOf(review),/含未打开/);
 const selector=review.children.find(el=>el.id==="model-delete-replacement");assert.equal(selector.children.length,1);assert.equal(selector.value,"B");
 api.models.catalog.version=2;
 await review.children.find(el=>el.textContent==="验证并删除、替换引用").emit("click");await settle();
 const sent=api.calls.find(c=>c.path==="/v1/models/change").body;
 assert.equal(sent.kind,"delete");assert.equal(sent.entry_id,"A");assert.equal(sent.replacement_id,"B");assert.equal(sent.expected_version,1);
 api.models.catalog.entries[0].pending_deletion=true;await openSettings(ui);
 assert.match(textOf(ui.el("model-list").children[0]),/待删除/);assert(!ui.el("model-list").children[0].children.some(el=>el.textContent==="编辑"));
 await openMember(ui);assert(!ui.el("member-model-selection").children.some(el=>el.value==="A"));
});

test("unverified missing-key imports request only the key and template references block deletion",async()=>{
 const api=fixture();api.models.catalog.entries[0].unverified=true;api.models.entry_states={A:{validated:false,credential_configured:false,templates:["team / worker"]}};
 const ui=await launch(api);await openSettings(ui);let row=ui.el("model-list").children[0];assert.match(textOf(row),/待验证/);assert.match(textOf(row),/请补填 API Key/);
 await row.children.find(el=>el.textContent==="编辑").emit("click");assert.equal(ui.el("catalog-api-key").hidden,false);assert.equal(ui.el("catalog-replace-key").checked,true);
 await row.children.find(el=>el.textContent==="删除").emit("click");const review=row.children.find(el=>el.id==="model-delete-confirm");assert.match(textOf(review),/请先修正模板引用/);assert(!review.children.some(el=>el.textContent==="验证并删除、替换引用"));
});

test("model results show only the latest operation and unchanged polls retain its DOM",async()=>{
 const api=fixture();
 const completed={id:"latest",status:"completed",committed:true,message:"模型配置已保存，相关成员已完成应用。",members:[],completed:2,waiting:0,failed:0};
 api.models.operations=[completed,{id:"old",status:"failed",committed:false,message:"unsupported_installation",members:[],completed:0,waiting:0,failed:0}];
 const ui=await launch(api);await openSettings(ui);
 const list=ui.el("model-operations"),row=list.children[0];
 assert.equal(list.children.length,1);assert.match(textOf(row),/已完成/);assert.doesNotMatch(textOf(list),/unsupported_installation|需处理|历史记录/);
 await ui.clock.advance(2000);assert.equal(list.children[0],row);
 api.models.operations.unshift({id:"new",status:"failed",committed:true,message:"部分成员未完成",members:[{agent_id:"assistant",status:"failed",error:"应用失败"}],completed:0,waiting:0,failed:1});
 await ui.clock.advance(2000);
 assert.equal(list.children.length,1);assert.match(textOf(list),/部分成员未完成/);assert(list.children[0].children.some(el=>el.textContent==="重试未完成成员"));
});

test("submission feedback clears when validation finishes and a new failure stays visible",async()=>{
 const api=fixture(),ui=await launch(api);await openSettings(ui);
 await ui.el("model-list").children[0].children.find(el=>el.textContent==="编辑").emit("click");
 await ui.el("model-edit-form").emit("submit");await settle();
 assert.match(ui.el("model-feedback").textContent,/已受理/);
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
 await openMember(ui);await ui.el("member-model-progress").emit("click");await settle();
 assert.equal(ui.el("model-settings").hidden,false);assert.equal(ui.el("settings-tab-models").getAttribute("aria-selected"),"true");
});
