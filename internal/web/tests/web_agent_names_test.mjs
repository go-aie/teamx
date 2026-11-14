import {test} from "node:test";
import assert from "node:assert/strict";
import {launch,settle} from "./web_app_harness.mjs";
import {fixture} from "./web_member_platform_fixture.mjs";
import {markdownHTML} from "../assets/markdown.mjs";
import {messageDisplayText, resolveAgentName} from "../assets/agent-names.mjs";

function namesFixture({direct=false,duplicate=false}={}){
 const api=fixture();const name=duplicate ? "同名" : "编程助手";
 api.members[0].agent_name=name;api.members[1].agent_name=duplicate ? name : "架构师";
 let roomName=direct ? name : "协作房间";
 const messages=[{seq:1,text:"收到",author:{kind:"agent",id:"a"},author_membership_id:"member-a",created_at:"2026-11-14T01:00:00Z"},
 {seq:2,text:`@${name} 请确认`,author:{kind:"human",id:"tester"},mentions:[{start:0,end:name.length+1,kind:"agent",id:"a",membership_id:"member-a",agent_name:name}],created_at:"2026-11-14T01:00:01Z"}];
 api.hook=(path,body)=>{
  if(path==="/v1/rooms")return {service_epoch:"manager",operator:"tester",teams:[],rooms:[{room_id:"R",room_instance:"instance-R",name:roomName,is_direct:direct,status:"ready",runtime_status:"ready",service:{room_id:"R",room_instance:"instance-R",service_epoch:"runtime"},members:api.members.map(m=>({id:m.agent_id,name:"角色说明",agent_name:m.agent_name}))}]};
  if(path==="/v1/read")return {messages:structuredClone(messages.filter(m=>m.seq>(body.after || 0))),snapshot_seq:messages.at(-1).seq,has_more:false};
  if(path==="/v1/member/list")return {members:structuredClone(direct ? [api.members[0]] : api.members)};
  if(path==="/v1/models")return {catalog:{version:1,entries:[]},members:[],operations:[]};
  if(path==="/v1/member/name"){
   const m=api.members.find(m=>m.agent_id===body.agent_id && m.membership_id===body.membership_id);assert.ok(m);assert.equal(body.expected_name,m.agent_name);assert.equal(body.service_epoch,"manager");
   m.agent_name=body.agent_name;if(direct)roomName=body.agent_name;return {agent_name:m.agent_name};
  }
  if(path==="/v1/send"){messages.push({seq:messages.length+1,author:body.caller,text:body.text,mentions:body.mentions,created_at:"2026-11-14T01:00:02Z"});return {seq:messages.length};}
 };
 return api;
}
async function mention(ui,text){await ui.draft(text);const input=ui.el("message-input");input.selectionStart=input.selectionEnd=text.length;await input.emit("input");return ui.el("mention-options").children;}

for(const direct of [false,true])test(`Agent names stay coherent after ${direct ? "direct" : "group"} rename`,async()=>{
 const api=namesFixture({direct}),ui=await launch(api);await settle();
 assert.equal(ui.document.getElementById("message-1").querySelector(".author").textContent,"编程助手");
 await ui.el("members").children[0].querySelector(".member-summary").emit("click");await settle();
 await ui.el("toggle-agent-controls").emit("click");
 assert.equal(ui.el("agent-display-name").value,"编程助手");
 ui.el("agent-display-name").value="新的 称呼";await ui.el("agent-name-form").emit("submit");await settle();
 assert.equal(api.calls.filter(c=>c.path==="/v1/member/name").length,1,ui.el("agent-name-feedback").textContent);
 assert.equal(api.members[0].agent_name,"新的 称呼",ui.el("agent-name-feedback").textContent);
 assert.equal(ui.el("activity-title").textContent,"新的 称呼");
 assert.equal(ui.document.getElementById("message-1").querySelector(".author").textContent,"新的 称呼");
 const options=ui.document.getElementById("message-2").querySelector(".message-text").markdownOptions;
 assert.equal(options.mentionLabel({kind:"agent",id:"a",membership_id:"member-a"}),"新的 称呼");
 assert.equal(ui.el("room-name").textContent,direct ? "新的 称呼" : "协作房间");
 assert.equal(api.members[0].agent_id,"a");assert.equal(api.members[0].membership_id,"member-a");
 assert.equal(ui.el("members").children[0].querySelector(".member-profile").textContent,"角色说明");
 const choices=await mention(ui,"@新的 称");assert.equal(choices[0].textContent,"@新的 称呼");
});

test("same-name mention choices preserve exact targets through edits, draft reload and renaming",async()=>{
 const api=namesFixture({duplicate:true}),ui=await launch(api);
 const choices=await mention(ui,"🙂 @同");
 assert.deepEqual(choices.map(c=>c.textContent),["@同名 · a","@同名 · b"]);
 await choices[1].emit("click");
 assert.equal(ui.el("message-input").value,"🙂 @同名 ");
 await ui.draft(ui.el("message-input").value+"请处理");
 api.members[1].agent_name="另一称呼";await ui.refresh();
 const reloaded=await launch(api,ui.saved);assert.equal(reloaded.el("message-input").value,"🙂 @同名 请处理");
 await reloaded.el("compose").emit("submit");
 const sent=api.calls.find(c=>c.path==="/v1/send").body;
 assert.deepEqual(sent.mentions,[{start:3,end:6,kind:"agent",id:"b",membership_id:"member-b"}]);
 assert.equal(sent.text,"🙂 @同名 请处理");
});

test("bound mentions render renamed labels safely while code, links and unbound history retain source text",()=>{
 const text="@旧名 `@旧名` [@旧名](https://example.com)";
 const starts=[0,text.indexOf("@旧名",1),text.lastIndexOf("@旧名")];
 const mentions=starts.map(start=>({start,end:start+3,kind:"agent",id:"a",membership_id:"old-member"}));
 const html=markdownHTML(text,{mentions,mentionLabel:()=>"新名称 <img src=x>"});
 assert.ok(html.includes('<mark class="mention-member">@新名称 &lt;img src=x&gt;</mark>'));
 assert.ok(html.includes("<code>@旧名</code>"));
 assert.ok(html.includes('rel="noopener noreferrer">@旧名</a>'));
 assert.equal(html.includes("<img"),false);
 assert.equal(html.includes("TEAMX_MENTION_"),false);
 assert.ok(markdownHTML("@旧名").includes("@旧名"));
});

test("plain previews retain mention identity after another member reuses the Agent ID",()=>{
 const members=[{agent_id:"a",membership_id:"new-member",agent_name:"后来成员"}];
 const label=at=>resolveAgentName(members,at.id,at.membership_id,at.agent_name || at.id);
 const message={text:"🙂 @旧称呼 请确认",mentions:[{start:3,end:7,kind:"agent",id:"a",membership_id:"old-member",agent_name:"原成员"}]};
 assert.equal(messageDisplayText(message,label),"🙂 @原成员 请确认");
 assert.equal(resolveAgentName(members,"a","new-member"),"后来成员");
});

test("historical sender details never open a replacement with the same Agent ID",async()=>{
 const api=namesFixture(),ui=await launch(api);
 api.members[0].membership_id="replacement";api.members[0].agent_name="后来成员";
 await ui.refresh();
 await ui.document.getElementById("message-1").querySelector(".author").emit("click");
 assert.equal(ui.el("activity-dialog").open,false);
});
