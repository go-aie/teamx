import assert from "node:assert/strict";
export const target = (id="a") => ({mode:"inbox",agent_id:id,membership_id:`member-${id}`});
export const key = (id,t) => JSON.stringify([id,t.membership_id || "room"]);
const copy = structuredClone;
export function fixture({empty=false}={}) {
  const members=["a","b"].map(id=>({agent_id:id,membership_id:`member-${id}`,status:"paused",runner:{profile:"p"}}));
  const room={room_id:"R",room_instance:"instance-R",name:"Review",status:"ready",service:{room_id:"R",room_instance:"instance-R",service_epoch:"runtime"}};
  const platforms=[{id:"gl",name:"公司 GitLab",provider:"gitlab",url:"https://gitlab.invalid",validation:{status:"valid",user_id:101,username:"alice"}},{id:"gl2",name:"个人 GitLab",provider:"gitlab",url:"https://gitlab.invalid",validation:{status:"valid",user_id:202,username:"bob"}},{id:"lark",name:"公司飞书",provider:"lark",url:"https://open.feishu.cn",validation:{status:"valid",username:"Alice",account:{id:{value:"ou_a"}}}}].map(p=>({...p,version:1}));
  const records=new Map(),operations=new Map();
  const tools={revision:0,bindings:[],disabled:[],catalog:[{name:"gitlab",provider:"gitlab"},{name:"lark",provider:"lark"},{name:"scion",provider:"scion"},{name:"local"}]};
  const gitlab=(id,t,interval=30)=>({platform_id:id,version:2,config:{enabled:true,platform_id:id,target:copy(t),interval_seconds:interval,backfill_seconds:0,events:{enabled:false,project:"",filter:{types:[]}},todos:{enabled:true,filter:{reasons:["assigned"],types:["work_item"]}}},events:{available:true,status:"disabled",config:{enabled:false}},todos:{available:true,status:"running",config:{enabled:true},recipient_id:id==="gl2" ? 202 : 101,instance:"https://gitlab.invalid",subscription_id:`sub-${id}-${t.membership_id || "room"}`},errors:{},pending:{}});
  if(!empty){
    for(const t of [target("a"),target("b"),{}])records.set(key("gl",t),gitlab("gl",t,t.membership_id==="member-b" ? 45 : 30));
    records.set(key("lark",target("a")),{platform_id:"lark",provider:"lark",channel:"messages",subscription_id:"sub-lark-a",subscription_version:3,status:"running",config:{enabled:true,platform_id:"lark",target:target("a"),private:{enabled:true,scope:"all",chat_ids:[]},groups:{enabled:false,chat_ids:[],message_mode:"mentions",at_me:true,at_all:false},interval_seconds:60,backfill_seconds:0},lark:{chats:[]}});
  }
  const source=(v)=>({provider:"gitlab",channel:"todos",platform_id:v.platform_id,target:copy(v.config.target),subscription_id:v.todos.subscription_id,instance:"https://gitlab.invalid",config:copy(v.config)});
  const api={calls:[],members,records,platforms,tools,operations,hook:null,epoch:"manager",runtimeEpoch:"runtime",async request(path,body){
    api.calls.push({path,body:copy(body)});
    const override=api.hook?.(path,body);if(override!==undefined)return override;
    if(path.startsWith("/v1/tools/bindings?"))return copy(tools);
    if(path==="/v1/tools/bind"){
      assert.equal(body.revision,tools.revision);
      const matches=b=>b.agent_id===body.binding.agent_id && b.membership_id===body.binding.membership_id && b.tool===body.binding.tool && b.platform_id===body.binding.platform_id;
      tools.bindings=tools.bindings.filter(b=>!matches(b));tools.disabled=tools.disabled.filter(b=>!matches(b));
      tools[body.enabled ? "bindings" : "disabled"].push(copy(body.binding));tools.revision++;return copy(tools);
    }
    if(path==="/v1/platforms/onboarding/prepare"){
      if(operations.has(body.operation_id))return copy(operations.get(body.operation_id));
      const old=records.get(key(body.platform_id,body)),known=[...tools.bindings,...tools.disabled].some(b=>b.platform_id===body.platform_id && b.membership_id===body.membership_id);
      const op={schema:2,operation_id:body.operation_id,platform_id:body.platform_id,input:copy(body),connection:{status:"waiting"},notifications:{status:body.skip_notifications || body.provider==="scion" ? "skipped" : body.preserve_existing && old ? "preserved" : "pending"},tool:{status:body.skip_tool ? "skipped" : body.preserve_existing && known ? "preserved" : "pending"}};
      operations.set(op.operation_id,op);return copy(op);
    }
    if(path==="/v1/platforms/onboarding/status")return copy(operations.get(body.operation_id));
    if(path==="/v1/platforms/onboarding/apply"){
      const op=operations.get(body.operation_id),spec=op.input,t={mode:"inbox",agent_id:spec.agent_id,membership_id:spec.membership_id};op.connection.status="connected";
      if(!["applied","preserved","skipped"].includes(op.notifications.status)){
        if(spec.provider==="gitlab"){
          const v=gitlab(spec.platform_id,t);v.version=1;v.config={...v.config,...(spec.gitlab || {}),enabled:spec.notifications!==false,target:t};records.set(key(spec.platform_id,t),v);
        }else records.set(key(spec.platform_id,t),{provider:"lark",channel:"messages",subscription_id:`sub-${spec.platform_id}-${spec.membership_id}`,subscription_version:1,status:"running",config:{enabled:spec.notifications!==false,platform_id:spec.platform_id,target:t,interval_seconds:60,private:{enabled:true,scope:"all",chat_ids:[]},groups:{enabled:false,chat_ids:[],message_mode:"mentions",at_me:true,at_all:false},...(spec.lark || {})},lark:{chats:[]}});
        op.notifications={status:"applied",version:1};
      }
      if(!["applied","preserved","skipped"].includes(op.tool.status)){
        tools[spec.tool===false ? "disabled" : "bindings"].push({agent_id:spec.agent_id,membership_id:spec.membership_id,platform_id:spec.platform_id,tool:spec.provider});tools.revision++;op.tool={status:"applied",version:tools.revision};
      }
      return copy(op);
    }
    if(path.startsWith("/v1/platforms/onboarding/targets?"))return {members:members.map(m=>({...m,room_id:room.room_id,room_instance:room.room_instance}))};
    if(path==="/v1/ui")return {managed:true};
    if(path==="/v1/rooms")return {service_epoch:api.epoch,operator:"tester",rooms:[{...room,service:{...room.service,service_epoch:api.runtimeEpoch}}],teams:[]};
    if(path.startsWith("/v1/workspace?"))return {workspace:{root:"workspaces"},workspace_version:1,members:[]};
    if(path.startsWith("/v1/platforms?"))return {platforms:copy(api.platforms)};
    if(path.startsWith("/v1/platform/notifications?")){
      const q=new URL(path,"http://fixture").searchParams;
      const all=[...records.values()],subscriptions=all.flatMap(v=>v.provider==="lark" ? [copy(v)] : v.todos.subscription_id ? [source(v)] : []);
      if(q.has("subscription_id"))return {subscriptions:subscriptions.filter(v=>v.subscription_id===q.get("subscription_id") && v.provider===q.get("provider") && v.channel===q.get("channel")),retired_subscriptions:[]};
      assert.equal(q.has("platform_id"),false,"summary reads every member and room");
      return {subscriptions,gitlab_platforms:copy(all.filter(v=>!v.provider))};
    }
    if(path.startsWith("/v1/platform/gitlab/notifications?")){
      const q=new URL(path,"http://fixture").searchParams;
      assert.ok(q.has("target_mode"));
      const t=q.get("target_mode")==="inbox" ? {mode:"inbox",agent_id:q.get("agent_id"),membership_id:q.get("membership_id")} : {};
      const saved=records.get(key(q.get("platform_id"),t));
      return copy(saved || {...gitlab(q.get("platform_id"),t),version:0,todos:{available:true,status:"disabled",config:{enabled:false}},config:{...gitlab(q.get("platform_id"),t).config,todos:{enabled:false,filter:{reasons:["assigned"],types:["work_item"]}}}});
    }
    if(path==="/v1/platform/gitlab/notifications/configure"){
      assert.ok(body.config.target.mode,"HTTP configuration requires explicit target mode");
      const k=key(body.platform_id,body.config.target),previous=records.get(k);
      assert.equal(body.version,previous?.version || 0);
      const next={...(previous || gitlab(body.platform_id,body.config.target)),version:body.version+1,config:copy(body.config)};records.set(k,next);return copy(next);
    }
    if(path==="/v1/platform/notifications/configure"){
      assert.equal(body.platform_id,undefined,"Lark uses config platform only");
      const k=key(body.config.platform_id,body.config.target),previous=records.get(k);
      assert.equal(body.subscription_id,previous?.subscription_id || "");assert.equal(body.subscription_version,previous?.subscription_version || 0);
      const next={...previous,platform_id:body.config.platform_id,provider:"lark",channel:"messages",subscription_id:previous?.subscription_id || "new-lark",subscription_version:body.subscription_version+1,status:body.config.enabled ? "running" : "disabled",config:copy(body.config),lark:{chats:[]}};records.set(k,next);return copy(next);
    }
    if(path.endsWith("notifications/sync")){assert.ok(body.target?.mode,"sync requires explicit target mode");return copy(records.get(key(body.platform_id || "lark",body.target)));}
    if(path==="/v1/member/list")return {members:copy(members),latest_seq:0};
    if(path==="/v1/member/activity")return {member:copy(members.find(m=>m.membership_id===body.membership_id))};
    if(path==="/v1/member/inbox/status")return {latest_seq:50,expired_through:0,retained_entries:50,bytes:100,limits:{bytes:1000}};
    if(path==="/v1/member/inbox"){
      const end=Math.min(body.snapshot_seq,body.after+body.limit);
      return {snapshot_seq:body.snapshot_seq,next_after:end,has_more:end<body.snapshot_seq,status:{latest_seq:50,expired_through:0,retained_entries:50,bytes:100,limits:{bytes:1000}},entries:Array.from({length:end-body.after},(_,i)=>({seq:body.after+i+1,item:{provider:"gitlab",channel:"todos",text:`${body.agent_id}通知`},notification_source:{provider:"gitlab",channel:"todos",subscription_id:`sub-gl-${body.membership_id}`,instance:"https://gitlab.invalid"},source:{status:"not_checked"}}))};
    }
    if(path==="/v1/read")return {messages:[],snapshot_seq:0,has_more:false};
    throw Error(`Unexpected ${path}`);
  }};
  return api;
}
