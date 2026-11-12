import { compactViewport } from "../client.mjs";
import { sameService } from "../client.mjs";

// Room/member notification subscriptions, targets and configuration views.
export function createNotifications({ env, membersUI, workspaceUI, runtime, roomsUI, settingsUI, connectionsUI, onboardingUI, layoutUI }) {
  const { document, window, Option, setTimeout, clearTimeout, request } = env;
  const { state, $, notice, setSelectOptions } = runtime;

  let notificationNavigation = 0;

  const gitlabEventStatuses = { disabled: "已关闭", initializing: "初始化中", running: "运行中", backoff: "退避中", credentials_invalid: "凭据失效", capacity: "接收容量已满", binding_changed: "绑定已变化" };

  const gitlabEventErrors = { credential_unavailable: "凭据不可用，请验证所选平台。", credential_rejected: "GitLab 拒绝访问，请检查权限和凭据。", binding_changed: "绑定已变化，请核对后保存设置。", project_identity_changed: "项目身份已变化，需要核查绑定。", capacity: "接收容量已满，进度未推进。", query_failed: "查询失败，将按退避计划重试。", room_capacity: "房间消息或投递回执容量已满，事件保留在积压中。" };

  const notificationTargetErrors = { target_unavailable: "投递目标暂不可用，通知保留在来源积压中。", target_removed: "接收成员已移除，旧积压保留原目标；同名新成员不会继承。", inbox_capacity: "成员收件箱容量已满，新通知保留在来源积压中。" };

  function notificationTarget(config) { const t = config?.target; return t?.mode === "inbox" ? { mode: "inbox", agent_id: t.agent_id, membership_id: t.membership_id } : {}; }

  function sameNotificationTarget(a, b) { return JSON.stringify(notificationTarget({target:a})) === JSON.stringify(notificationTarget({target:b})); }

  function memberNotificationTarget(member = membersUI.selectedAgent()) { return member ? {mode:"inbox",agent_id:member.agent_id,membership_id:member.membership_id} : null; }

  function activeNotificationTarget(ws = workspaceUI.workspaceView) { return ws?.notificationTarget || {}; }

  function notificationScopeKey(target, id = "") { return JSON.stringify([state.catalog?.service_epoch,state.info?.service_epoch,notificationTarget({target}), id]); }

  function notificationRequestTarget(target) { return target?.mode === "inbox" ? notificationTarget({target}) : {mode:"room"}; }

  function notificationTargetQuery(target) { return target?.mode === "inbox" ? {target_mode:"inbox",agent_id:target.agent_id,membership_id:target.membership_id} : {target_mode:"room"}; }

  function notificationWritePending(ws, key = ws?.notificationScope) { return !!ws?.notificationWrites?.has(key); }

  function beginNotificationWrite(ws, key) { (ws.notificationWrites ||= new Set()).add(key); }

  function notificationContextCurrent(ws, key, epoch) { return ws === workspaceUI.workspaceView && ws.notificationScope === key && state.catalog?.service_epoch === epoch; }

  function rememberNotificationDraft(ws) {
    if (!ws?.notificationScope) return;
    ws.notificationDrafts ||= new Map();
    const messages = ws.notificationChannel === "messages";
    if (messages && ws.messages && ws.larkDirty) ws.notificationDrafts.set(ws.notificationScope, {config:larkMessagesDraft(),directories:ws.larkChatDirectories});
    else if (!messages && ws.gitlabDirty) ws.notificationDrafts.set(ws.notificationScope, {config:structuredClone(ws.gitlabDraft),emptyTypes:ws.eventsEmptyTypes});
    else ws.notificationDrafts.delete(ws.notificationScope);
  }

  function gitlabConfig(config = {}) {
    const e = config.events || {}, t = todoConfig(config.todos || {});
    return { enabled: config.enabled ?? true, platform_id: config.platform_id || "", target: notificationTarget(config), interval_seconds: config.interval_seconds ?? 10,
      backfill_seconds: config.backfill_seconds || 0, ...(config.backfill_start ? {backfill_start: new Date(config.backfill_start).toISOString()} : {}),
      events: { enabled: !!e.enabled, project: e.project || "", etag_verified: !!e.etag_verified,
        filter: { types: [...(e.filter?.types || [])] } }, todos: t };
  }

  function gitlabDraft() { return workspaceUI.workspaceView?.gitlabDraft || gitlabConfig(); }

  function gitlabSettingsBusy(ws) { return !ws?.gitlabConnected || notificationWritePending(ws) || !!ws.gitlabSending || !!ws.notificationToggle || !!ws.gitlab?.applying || roomsUI.managementBusy(); }

  function gitlabConfigKey(config) {
    const c = gitlabConfig(config);
    return JSON.stringify([c.enabled, c.platform_id, c.target, c.interval_seconds, c.backfill_seconds, config.backfill_start === "" ? "custom" : c.backfill_start || "", gitlabEventConfigKey(c.events), todoConfigKey(c.todos)]);
  }

  function acceptGitLabView(ws, view, saved = false) {
    if(!notificationConfigured({provider:"gitlab"},view))view={...view,config:{...view.config,enabled:false}};
    ws.gitlab = view; ws.gitlabConnected = true;
    ws.events = view.events; ws.todos = view.todos; ws.eventsConnected = ws.todosConnected = true;
    if (saved || !ws.gitlabDirty || !ws.eventsEmptyTypes && gitlabConfigKey(ws.gitlabDraft) === gitlabConfigKey(view.config)) { ws.gitlabDraft = gitlabConfig(view.config); ws.gitlabDirty = false; ws.eventsEmptyTypes = false; ws.gitlabBackfillEditing = false; }
    else if (view.version && ws.gitlabDraft.platform_id===view.config.platform_id) {
      ws.gitlabDraft.enabled=view.config.enabled;
      ws.gitlabDirty=!!ws.eventsEmptyTypes || gitlabConfigKey(ws.gitlabDraft)!==gitlabConfigKey(view.config);
    }
  }

  function editGitLabConfig() {
    const ws = workspaceUI.workspaceView;
    ws.gitlabDirty = !!ws.eventsEmptyTypes || gitlabConfigKey(ws.gitlabDraft) !== gitlabConfigKey(ws.gitlab.config);
    ws.gitlabError = ""; ws.gitlabFeedback = "";
    renderGitLabEvents();
  }

  function gitlabPlatformOptions(selected, placeholder) {
    const platforms = workspaceUI.workspaceView?.platforms || [];
    const options = [new Option(placeholder, ""), ...platforms.map(item => new Option(`${item.name} · ${item.validation?.username ? `@${item.validation.username}` : "待验证"}`, item.id))];
    if (selected && !platforms.some(item => item.id === selected)) options.push(new Option("原连接暂不可用", selected));
    return options;
  }

  const gitlabEventTypeIDs = ["opened", "merged", "closed", "note", "push"];

  const gitlabEventTypeInputs = () => gitlabEventTypeIDs.map(id => $(`gitlab-events-type-${id}`));

  function gitlabEventConfigKey(config) {
    const unique = values => [...new Set(values || [])].sort(), types = unique(config.filter?.types);
    return JSON.stringify({ project: config.project || "", enabled: config.enabled, etag_verified: !!config.etag_verified,
      types: types.length === gitlabEventTypeIDs.length ? [] : types });
  }

  function gitlabEventDraftState(data) {
    const root = gitlabDraft(), config = root.events, connection = workspaceUI.workspaceView?.platforms?.find(c => c.id === root.platform_id);
    const identityChanged = !!(config.enabled && data.subscription_id && data.recipient_id > 0 && todoVerified(connection) && (data.recipient_id !== connection.validation.user_id || data.instance !== connection.url));
    const targetChanged = root.platform_id !== workspaceUI.workspaceView?.gitlab?.config.platform_id || config.project !== data.config.project || identityChanged;
    const targetInvalid = config.enabled && (!todoVerified(connection) || !config.project);
    const count = workspaceUI.workspaceView?.eventsEmptyTypes ? 0 : config.filter.types.length || gitlabEventTypeIDs.length;
    return { config, count, dirty: !!workspaceUI.workspaceView?.eventsEmptyTypes || identityChanged || gitlabEventConfigKey(config) !== gitlabEventConfigKey(workspaceUI.workspaceView?.gitlab?.config.events || config), targetChanged, targetInvalid,
      invalid: targetInvalid || config.enabled && count === 0 };
  }

  async function syncGitLabNotifications(catalog, ws) {
    if (ws !== workspaceUI.workspaceView) return;
    const revisions = {messages:ws.messagesRevision,gitlab:ws.gitlabRevision};
    const read = ws.notificationsRead = (ws.notificationsRead || 0) + 1;
    const scope = ws.notificationScope, context = ws.notificationEpoch;
    try {
      const params = new URLSearchParams({service_epoch:catalog.service_epoch,caller_id:ws.operator,...ws.room});
      const data = await request(`/v1/platform/notifications?${params}`);
      if (ws !== workspaceUI.workspaceView || state.catalog?.service_epoch !== catalog.service_epoch || read !== ws.notificationsRead || context !== ws.notificationEpoch) return;
      ws.notificationsConnected = true;
      ws.allSubscriptions = data.subscriptions || [];
      ws.retiredSources = data.retired_subscriptions || [];
      ws.larkDefaults = data.lark_defaults;
      ws.larkSources = ws.allSubscriptions.filter(v=>v.provider === "lark" && v.channel === "messages");
      ws.gitlabPlatforms = data.gitlab_platforms || [];
      if (scope !== ws.notificationScope) return;
      const target = activeNotificationTarget(ws), id = ws.notificationPlatformID;
      if (!ws.notificationToggle && !ws.messagesSending && revisions.messages === ws.messagesRevision) {
        const next=ws.larkSources.find(v=>v.config.platform_id===id && sameNotificationTarget(v.config.target,target));
        if (next) { ws.messages=next; ws.messagesConnected=true; if(ws.larkDirty && next.subscription_id)$("lark-enabled").checked=!!next.config.enabled; }
      }
      if (!ws.notificationToggle && !ws.gitlabSending && revisions.gitlab === ws.gitlabRevision) {
        const next=ws.gitlabPlatforms.find(v=>v.platform_id===id && sameNotificationTarget(v.config.target,target));
        if(next)acceptGitLabView(ws,next);
      }
    } catch {
      if (ws !== workspaceUI.workspaceView || state.catalog?.service_epoch !== catalog.service_epoch || read !== ws.notificationsRead || context !== ws.notificationEpoch) return;
      ws.notificationsConnected=false;
      if(scope===ws.notificationScope){ws.gitlabConnected=false;ws.messagesConnected=false;}
    }
  }

  function larkConfigKey(c = {}) {return JSON.stringify({enabled:!!c.enabled,platform_id:c.platform_id || "",target:notificationTarget(c),private:{enabled:!!c.private?.enabled,scope:c.private?.scope || "all",chat_ids:[...(c.private?.chat_ids || [])].sort()},groups:{enabled:!!c.groups?.enabled,scope:c.groups?.scope || "specified",chat_ids:[...(c.groups?.chat_ids || [])].sort(),message_mode:c.groups?.message_mode || "mentions",at_me:!!c.groups?.at_me,at_all:!!c.groups?.at_all},interval_seconds:c.interval_seconds || 10,backfill_seconds:c.backfill_seconds || 0,backfill_start:c.backfill_start ? new Date(c.backfill_start).toISOString() : ""});}

  function larkMessagesDraft() {
    const ws=workspaceUI.workspaceView,privateIDs=larkChatIDs("private").sort(),groups=larkChatIDs("groups").sort();
    return {enabled:$("lark-enabled").checked,platform_id:$("lark-connection").value,target:structuredClone(activeNotificationTarget(ws)),
      private:{enabled:$("lark-private-enabled").checked,scope:larkAllChats() ? "all" : "specified",chat_ids:privateIDs},
      groups:{enabled:$("lark-groups-enabled").checked,scope:$("lark-groups-scope").checked ? "all" : "specified",chat_ids:groups,message_mode:$("lark-group-mode").value || "mentions",at_me:$("lark-group").checked,at_all:$("lark-all").checked},
      interval_seconds:Number($("lark-interval").value),...backfillDraft("lark")};
  }

  function selectLarkPlatform(ws,id) {
    stopLarkChatSearch();
    ws.messages=structuredClone(ws.larkSources?.find(s=>s.config.platform_id===id && sameNotificationTarget(s.config.target,activeNotificationTarget(ws))) || {provider:"lark",channel:"messages",available:true,subscription_id:"",subscription_version:0,status:"disabled",config:{...(ws.larkDefaults || {private:{enabled:true,scope:"all",chat_ids:[]},groups:{enabled:false,scope:"specified",chat_ids:[],message_mode:"mentions",at_me:true,at_all:false},interval_seconds:10,backfill_seconds:0}),enabled:false,platform_id:id,target:structuredClone(activeNotificationTarget(ws))},lark:{chats:[]}});
    ws.larkDirty=false;ws.larkFilled=null;ws.larkSelectedChats={private:[],groups:[]};ws.messagesFeedback="";ws.messagesError="";ws.messagesRevision=(ws.messagesRevision || 0)+1;
  }

  function larkAllChats() { return $("lark-scope").checked; }

  function notificationLocalTime(value) { return settingsUI.systemTime.local(value); }

  function backfillDraft(prefix) {
    const mode=$(`${prefix}-backfill`).value;
    let start=""; try { start=settingsUI.readTimeInput(`${prefix}-backfill-start`); } catch { /* renderBackfill reports the conversion error. */ }
    return {backfill_seconds:mode==="custom" ? 0 : Number(mode || 0),
      ...(mode==="custom" ? {backfill_start:start} : {})};
  }

  function fillBackfill(prefix, config) {
    $(`${prefix}-backfill`).value=config.backfill_start ? "custom" : String(config.backfill_seconds || 0);
    settingsUI.fillTimeInput(`${prefix}-backfill-start`,config.backfill_start);
  }

  function renderBackfill(prefix, locked, baseline, busy=false) {
    const mode=$(`${prefix}-backfill`).value, start=Date.parse(backfillDraft(prefix).backfill_start || "");
    $(`${prefix}-backfill`).disabled=locked || busy;$(`${prefix}-backfill-start`).disabled=locked || busy;
    $(`${prefix}-backfill`).hidden=locked;$(`${prefix}-backfill-locked`).hidden=!locked;
    $(`${prefix}-backfill-custom`).hidden=locked || mode!=="custom";
    $(`${prefix}-backfill-start`).max=notificationLocalTime(Date.now());
    const time=Date.parse(baseline || "");
    $(`${prefix}-backfill-value`).textContent=Number.isFinite(time) ? settingsUI.formatDate(time) : "首次接收时已确定";
    let conversionError=""; try { settingsUI.readTimeInput(`${prefix}-backfill-start`); } catch(error) { conversionError=error.message; }
    const error=settingsUI.systemTime.error || (mode==="custom" && conversionError) || (mode==="custom" && !Number.isFinite(start) ? "请选择要开始接收消息的日期和时间。" : mode==="custom" && start<=0 ? "请选择 1970 年之后的有效时间。" : mode==="custom" && start>Date.now() ? "起始时间不能晚于当前时间。" : "");
    $(`${prefix}-backfill-start-label`).textContent=`起始时间（${settingsUI.systemTime.zone || "系统时区异常"}）`;
    $(`${prefix}-backfill-help`).textContent=locked ? `更早消息需新建${roomsUI.conversationType()}。` : "首次开启后固定接收起点。";
    notice(`${prefix}-backfill-error`,error);$(`${prefix}-backfill-start`).setAttribute("aria-invalid",String(!!error));
    return error;
  }

  function notificationSyncSummary(data) {
    const date=value=>settingsUI.formatDate(value);
    const parts=[data.last_check ? `最近检查 ${date(data.last_check)}` : data.subscription_id ? (data.config.enabled ? "等待首次检查" : "已暂停") : "尚未开始同步"];
    if(data.pending>0)parts.push(`待投递 ${data.pending}`);
    if(data.retry_at)parts.push(`下次重试 ${date(data.retry_at)}`);
    if(data.scan)parts.push(`历史检查中（第 ${data.scan.next_page} 页）`);
    if(data.gap)parts.push("部分历史消息可能缺失");
    return parts.join(" · ");
  }

  function larkChatIDs(kind) {
    const selected=workspaceUI.workspaceView?.larkSelectedChats;
    return [...new Set(kind ? selected?.[kind] || [] : [...(selected?.private || []),...(selected?.groups || [])])];
  }

  function larkChatType(chat) { return chat?.chat_mode==="p2p" || chat?.mode==="p2p" ? "私聊" : "群聊"; }

  function larkChatLabel(chat, id) { return chat?.name || `未命名${chat ? larkChatType(chat) : "会话"} · ${id}`; }

  function larkChatElement(kind, name) { return $(`lark-${kind}-chat-${name}`); }

  function larkChatDirectory(ws, connection, kind) {
    const key=JSON.stringify([state.catalog?.service_epoch,ws.notificationScope,connection?.id,connection?.version,connection?.validation?.account]);
    ws.larkChatDirectories ||= {};
    if(ws.larkChatDirectories[kind]?.key!==key){
      const previous=ws.larkChatDirectories[kind];
      clearTimeout(previous?.timer);previous?.controller?.abort();
      ws.larkChatDirectories[kind]={key,query:"",type:kind==="private" ? "p2p" : "group",known:new Map(),chats:new Map(),loaded:false,loading:false,hasMore:false,token:"",error:"",pages:0,warnings:[]};
      larkChatElement(kind,"search").value="";
    }
    return ws.larkChatDirectories[kind];
  }

  function selectLarkChat(kind, id, selected) {
    const ids=larkChatIDs(kind);
    if(selected && !ids.includes(id)){
      if(larkChatIDs().length>=32){notice(`lark-${kind}-chat-selection-error`,"最多选择 32 个会话，请先移除已选会话。");return;}
      ids.push(id);
    }
    if(workspaceUI.workspaceView)workspaceUI.workspaceView.larkSelectedChats[kind]=selected ? ids : ids.filter(x=>x!==id);
    for(const type of ["private","groups"])notice(`lark-${type}-chat-selection-error`,"");
    if(workspaceUI.workspaceView)workspaceUI.workspaceView.larkDirty=true;
    renderLarkMessages();
  }

  function larkChatPickerActive(kind) {
    return $("notification-detail").open && $(`lark-${kind}-settings`).open && !larkChatElement(kind,"selection").hidden && larkChatElement(kind,"picker").open;
  }

  function renderLarkChatPicker(ws, connection, kind) {
    const dir=larkChatDirectory(ws,connection,kind), ids=larkChatIDs(kind),noun=kind==="private" ? "人员" : "群聊";
    const ready=connection?.validation?.status==="valid",busy=$("lark-fields").disabled,blocked=!ready || busy;
    const needsPlatform=!$("lark-connection").value,hidden=kind==="private" ? larkAllChats() : $("lark-groups-scope").checked;
    $(kind==="private" ? "lark-scope" : "lark-groups-scope").disabled=blocked;
    larkChatElement(kind,"selection").hidden=hidden;
    larkChatElement(kind,"picker-summary").setAttribute("aria-disabled",String(blocked));
    larkChatElement(kind,"picker-hint").textContent=needsPlatform ? "请先选择平台，再选择会话。" : !connection ? "所选平台不可用，请重新选择。" : !ready ? "请先在「管理平台」中验证所选连接，再选择会话。" : "";
    larkChatElement(kind,"picker-hint").hidden=ready;
    larkChatElement(kind,"search").disabled=blocked;
    if(blocked || hidden){
      larkChatElement(kind,"picker").open=false;
      if(dir.loading || dir.timer)stopLarkChatSearch(kind);
    }
    const known=new Map((ws.messages?.config.platform_id===connection?.id ? ws.messages?.lark?.chats || [] : []).map(chat=>[chat.chat_id,chat]));
    for(const [id,chat] of dir.known)known.set(id,chat);
    larkChatElement(kind,"selected").replaceChildren(...ids.map(id=>{
      const chat=known.get(id),label=larkChatLabel(chat,id),button=document.createElement("button");
      button.type="button";button.textContent=`${label} ×`;button.title=id;button.setAttribute("aria-label",`移除${noun}：${label}`);
      button.disabled=blocked;
      button.addEventListener("click",()=>{if(ws===workspaceUI.workspaceView && !$("lark-fields").disabled && !button.disabled)selectLarkChat(kind,id,false);});return button;
    }));
    larkChatElement(kind,"picker-summary").textContent=!ready ? needsPlatform ? "请先选择平台" : "请先验证平台" : ids.length ? `已选 ${ids.length} ${kind==="private" ? "人" : "个群聊"} · 编辑` : `选择${noun}`;
    const query=dir.query,chats=[...dir.chats.values()];
    if(!query)chats.sort((a,b)=>(a.name || a.chat_id).localeCompare(b.name || b.chat_id,"zh-CN"));
    larkChatElement(kind,"options").replaceChildren(...chats.map(chat=>{
      const label=document.createElement("label"),input=document.createElement("input"),text=document.createElement("span");
      label.className="lark-chat-option";input.type="checkbox";input.value=chat.chat_id;input.checked=ids.includes(chat.chat_id);input.disabled=blocked || !input.checked && larkChatIDs().length>=32;
      text.textContent=larkChatLabel(chat,chat.chat_id);label.title=chat.chat_id;
      label.append(input,text);input.addEventListener("change",()=>{
        if(ws!==workspaceUI.workspaceView || ws.larkChatDirectories?.[kind]!==dir || $("lark-fields").disabled || input.disabled)return;
        selectLarkChat(kind,chat.chat_id,input.checked);
      });return label;
    }));
    const warningText=dir.warnings.map(w=>{
      const name={group:"群聊",user:"人员私聊",bot:"机器人私聊"}[w.source] || "部分会话";
      if(w.code==="refine_query")return `${name}结果较多，请输入更完整的名称`;
      if(w.code==="permission_denied")return `${name}搜索权限不足，请补充授权；也可清空关键词浏览会话列表`;
      return `${name}搜索暂不可用，可重试`;
    }).join("；");
    const summary=dir.loading ? (query ? "正在搜索…" : `正在加载…已有 ${chats.length} 个结果`) : !dir.loaded ? (query ? "等待搜索…" : `输入${kind==="private" ? "联系人姓名" : "群名"}可远程搜索。`) : query ? `已找到 ${chats.length} 个结果${dir.hasMore ? "，可加载更多" : ""}${!chats.length ? (dir.warnings.length ? " · 当前未返回匹配会话" : " · 无匹配会话") : ""}` : `已加载 ${chats.length} 个结果${dir.hasMore ? "，可加载更多或输入名称搜索" : ""}${!chats.length ? " · 暂无会话" : ""}`;
    larkChatElement(kind,"picker-status").textContent=!ready ? "请先选择并验证飞书连接。" : dir.error || `${summary}${warningText ? `；${warningText}` : ""}`;
    larkChatElement(kind,"more").hidden=!dir.hasMore;larkChatElement(kind,"more").disabled=blocked || dir.loading || dir.chats.size>=10000 || dir.pages>=100;
    larkChatElement(kind,"refresh").textContent=query ? "重新搜索" : "刷新列表";
    larkChatElement(kind,"refresh").disabled=blocked || dir.loading || !!dir.timer;
  }

  async function loadLarkChats(kind, reset=false) {
    const ws=workspaceUI.workspaceView,epoch=state.catalog?.service_epoch,connection=ws?.larkPlatforms?.find(x=>x.id===$("lark-connection").value);
    if(!ws || !epoch || connection?.validation?.status!=="valid" || $("lark-fields").disabled || !larkChatPickerActive(kind))return;
    let dir=larkChatDirectory(ws,connection,kind);
    if(dir.loading || !reset && dir.loaded && !dir.hasMore)return;
    if(reset)dir=resetLarkChatSearch(ws,kind,dir,dir.query);
    if([...dir.query].length>50){dir.error="搜索名称最多 50 个字符。";renderLarkMessages();return;}
    clearTimeout(dir.timer);dir.timer=null;dir.controller=new AbortController();
    dir.loading=true;dir.error="";renderLarkMessages();
    try{
      const page=await request("/v1/platform/lark/chats",{service_epoch:epoch,...ws.room,caller:{kind:"human",id:ws.operator},platform_id:connection.id,platform_version:connection.version,page_token:dir.token,query:dir.query,type:dir.type},undefined,40000,dir.controller.signal);
      if(ws!==workspaceUI.workspaceView || epoch!==state.catalog?.service_epoch || ws.larkChatDirectories?.[kind]!==dir || !larkChatPickerActive(kind))return;
      if(!Array.isArray(page.chats) || typeof page.has_more!=="boolean" || page.has_more && (!page.page_token || page.page_token===dir.token))throw Error("invalid page");
      for(const chat of page.chats){
        const mode=chat.chat_mode || chat.mode;
        if(kind==="private" ? mode!=="p2p" : !["group","topic"].includes(mode))continue;
        dir.chats.set(chat.chat_id,chat);dir.known.set(chat.chat_id,chat);
      }
      for(const warning of page.search_warnings || [])if(!dir.warnings.some(w=>w.source===warning.source && w.code===warning.code))dir.warnings.push(warning);
      dir.loaded=true;dir.hasMore=page.has_more;dir.token=page.page_token || "";dir.pages++;
      if(dir.hasMore && (dir.chats.size>=10000 || dir.pages>=100))dir.error="已达会话列表加载上限，请输入更完整的名称搜索。";
    }catch(e){
      if(ws===workspaceUI.workspaceView && ws.larkChatDirectories?.[kind]===dir && !dir.controller.signal.aborted){
        const errors={conflict:"连接已变化，请刷新通知设置并核对连接。",identity_mismatch:"飞书登录账号已变化，请重新验证连接后刷新会话列表。",permission_denied:"读取会话的权限不足，请检查授权并重新验证连接。",credential:"飞书连接未验证或登录已失效，请重新验证连接。",cursor_expired:"会话列表已变化，请刷新会话列表后继续选择。"};
        dir.error=errors[e.code] || "会话列表读取失败，已选会话保留；可刷新列表重试。";
      }
    }finally{
      dir.loading=false;
      if(ws===workspaceUI.workspaceView && ws.larkChatDirectories?.[kind]===dir)renderLarkMessages();
    }
  }

  function resetLarkChatSearch(ws,kind,dir,query) {
    clearTimeout(dir.timer);dir.controller?.abort();
    // Retain only selected labels across queries; result caches stay bounded.
    const known=new Map([...dir.known].filter(([id])=>larkChatIDs(kind).includes(id)));
    return ws.larkChatDirectories[kind]={key:dir.key,query,type:dir.type,known,chats:new Map(),loaded:false,loading:false,hasMore:false,token:"",error:"",pages:0,warnings:[]};
  }

  function queueLarkChatSearch(kind,composing=false,delay=300) {
    const ws=workspaceUI.workspaceView;if(!ws || larkChatElement(kind,"search").disabled)return;
    const connection=ws.larkPlatforms?.find(x=>x.id===$("lark-connection").value);
    let dir=larkChatDirectory(ws,connection,kind);
    const query=larkChatElement(kind,"search").value.trim();
    if(query!==dir.query)dir=resetLarkChatSearch(ws,kind,dir,query);
    clearTimeout(dir.timer);dir.timer=null;
    if([...query].length>50)dir.error="搜索名称最多 50 个字符。";
    else if(!composing && !dir.loaded && !dir.loading && larkChatPickerActive(kind)){
      dir.timer=setTimeout(()=>{dir.timer=null;if(ws===workspaceUI.workspaceView && ws.larkChatDirectories?.[kind]===dir)void loadLarkChats(kind);},query ? delay : 0);
    }
    renderLarkMessages();
  }

  function stopLarkChatSearch(kind) {
    const ws=workspaceUI.workspaceView;if(!ws)return;
    for(const type of kind ? [kind] : ["private","groups"]){
      const dir=ws.larkChatDirectories?.[type];if(!dir)continue;
      if(ws.larkChatComposing)ws.larkChatComposing[type]=false;
      clearTimeout(dir.timer);dir.controller?.abort();
      ws.larkChatDirectories[type]={...dir,loading:false,timer:null,controller:null};
    }
  }

  function renderLarkMessages() {
    const ws=workspaceUI.workspaceView,data=ws?.messages;
    $("lark-fields").disabled=!data || notificationWritePending(ws) || !!ws?.messagesSending || !!ws?.notificationToggle || roomsUI.managementBusy();
    $("lark-save").disabled=true;$("lark-sync").disabled=true;
    if(!data) return;
    const fresh=!!ws.larkRestore || ws.larkFilled !== data.config.platform_id+":"+data.subscription_id+":"+data.subscription_version && !ws.larkDirty;
    const c=ws.larkRestore || (fresh ? data.config : larkMessagesDraft());
    const platforms=ws.larkPlatforms || [];
    const options=[new Option("请选择连接", ""), ...platforms.map(x=>new Option(`${x.name} · ${x.validation?.username || "待验证"}`,x.id))];
    if(c.platform_id && !platforms.some(x=>x.id===c.platform_id))options.push(new Option("原连接暂不可用",c.platform_id));
    setSelectOptions("lark-connection", options, c.platform_id || "");$("lark-connection").hidden=true;
    const target=JSON.stringify(activeNotificationTarget(ws));
    setSelectOptions("lark-target",[new Option("固定接收者",target)],target);$("lark-target").disabled=true;
    if(fresh){
      $("lark-scope").checked=c.private?.scope==="all";$("lark-groups-scope").checked=c.groups?.scope==="all";$("lark-enabled").checked=!!c.enabled;
      $("lark-private-enabled").checked=!!c.private?.enabled;$("lark-groups-enabled").checked=!!c.groups?.enabled;
      ws.larkSelectedChats={private:[...(c.private?.chat_ids || [])],groups:[...(c.groups?.chat_ids || [])]};
      $("lark-group").checked=!!c.groups?.at_me;$("lark-group-mode").value=c.groups?.message_mode || "mentions";$("lark-all").checked=!!c.groups?.at_all;
      $("lark-interval").value=c.interval_seconds || 10;fillBackfill("lark",c);
      ws.larkFilled=data.config.platform_id+":"+data.subscription_id+":"+data.subscription_version;
      ws.larkRestore=null;
    }
    ws.larkSelectionPlatform=c.platform_id;
    $("lark-groups-scope-option").hidden=ws.larkDefaults?.groups?.scope!=="all" && !data.lark?.search;
    $("lark-group-mode-all").disabled=false;
    const groupAll=$("lark-group-mode").value==="all";
    $("lark-group-cost-warning").hidden=!$("lark-groups-enabled").checked || !$("lark-groups-scope").checked || !groupAll;
    $("lark-group").disabled=groupAll;$("lark-all").disabled=groupAll;$("lark-group-mentions").hidden=groupAll;
    $("lark-scope-help").hidden=!larkAllChats();
    const draft=larkMessagesDraft(), dirty=larkConfigKey(draft)!==larkConfigKey(data.config) || !data.subscription_id && !!draft.platform_id, connection=platforms.find(x=>x.id===draft.platform_id);
    const scopeError=draft.enabled && !draft.private.enabled && !draft.groups.enabled ? "请选择至少一种接收内容。" : draft.private.enabled && draft.private.scope==="specified" && !draft.private.chat_ids.length ? "请至少选择一个人员私聊，或关闭人员私聊接收。" : draft.groups.enabled && draft.groups.scope!=="all" && !draft.groups.chat_ids.length ? "请至少选择一个群聊，或关闭群聊接收。" : "";
    const backfillError=renderBackfill("lark",!!data.subscription_id && (!!data.lark?.baseline || data.status!=="disabled"),data.lark?.baseline || data.config.backfill_start);
    const valid=!backfillError && (!draft.enabled || connection?.validation?.status==="valid") && draft.target.mode==="inbox" && (!draft.enabled || draft.private.enabled || draft.groups.enabled) && (!draft.private.enabled || draft.private.scope==="all" || draft.private.chat_ids.length>0) && (!draft.groups.enabled || draft.groups.scope==="all" || draft.groups.chat_ids.length>0) && draft.interval_seconds>=3 && draft.interval_seconds<=3600;
    $("lark-save").disabled=notificationWritePending(ws)||!ws.messagesConnected||!!ws.messagesSending||roomsUI.managementBusy()||!valid||!dirty;
    $("lark-sync").disabled=notificationWritePending(ws)||!ws.messagesConnected||!!ws.messagesSending||roomsUI.managementBusy()||dirty||!data.config.enabled;
    $("lark-save").textContent=ws.messagesSending==="configure" ? "保存中…" : "保存设置";
    $("lark-sync").textContent=ws.messagesSending==="sync" ? "请求中…" : "立即同步";
    $("lark-state").textContent=!ws.messagesConnected ? "状态不可读" : dirty ? "待保存" : !data.config.enabled ? "通知已暂停" : data.error_code ? "需处理" : data.status==="disabled" ? "采集未启动" : "";
    notice("lark-account",connection?.validation?.status==="valid" ? `接收账号：${connection.validation.username}` : draft.platform_id ? "请先核验此平台账号。" : "请从上方选择平台详情。");
    for(const kind of ["private","groups"])renderLarkChatPicker(ws,connection,kind);
    $("lark-feedback").textContent=ws.messagesSending ? "正在处理…" : ws.messagesFeedback || (dirty ? "有未保存的更改" : "");
    const errors={...connectionsUI.connectionValidationErrors,...notificationTargetErrors,capacity:"来源容量已满，进度保留；释放容量后显式同步。",binding_changed:"连接身份已变化；旧订阅保留，请核对原账号。",identity_mismatch:"账号已变化，进度保留；请核对原账号并重新验证连接。",credential_unavailable:"登录凭据不可用，进度保留；请验证连接，原账号下已开启的通知会自动恢复。",credential_rejected:"登录已失效，进度保留；请在原 profile 完成用户授权并验证连接，原账号下已开启的通知会自动恢复。",permission_denied:"读取权限不足，进度保留；请检查用户授权范围并验证连接，原账号下已开启的通知会自动恢复。",cursor_expired:"游标已过期，将在同一时间窗重扫。",query_failed:"读取失败，将退避重试。"};
    notice("lark-error",ws.messagesError || scopeError || (!ws.messagesConnected ? "通知状态暂不可读。" : errors[data.delivery_error_code] || errors[data.error_code] || data.error_code || ""));
    const d=data.lark || {}, date=x=>settingsUI.formatDate(x);
    const diagnostics=[notificationSyncSummary(data)];
    if(d.excluded_items>0)diagnostics.push(`本人自聊历史已排除 ${d.excluded_items}`);
    if(!d.search && data.subscription_id && data.config.private?.enabled && data.config.private?.scope==="all" && !d.directory_complete)diagnostics.push(d.directory_scanning ? `会话目录加载中（${d.directory_pages || 0} 页）` : "会话目录尚未加载完整");
    if(!d.search)for(const [count,label] of [[d.failed_chats,"读取受阻"],[d.unlisted_chats,"不在最新目录"],[d.gap_chats,"积压超过 24 小时"],[d.unchecked,"待检查会话"]])if(count>0)diagnostics.push(`${label} ${count}`);
    $("lark-diagnostics").textContent=diagnostics.join(" · ");
    const branches=d.search?.branches || [];
    $("lark-branch-status").hidden=!d.search;
    $("lark-branch-status").replaceChildren(...branches.map(b=>{
      const li=document.createElement("li"),name={private:"人员私聊",groups:"群聊消息",own:"群内本人上下文"}[b.kind] || b.kind;
      const progress=b.completed ? `已检查至 ${date(b.completed*1000)}` : "等待完成首个时间窗";
      const stage=b.stage==="page" ? `当前页 ${b.page_done}/${b.page_items}` : b.stage==="search" ? "正在查询" : "等待检查";
      li.textContent=`${name} · ${progress} · ${stage}${b.start ? ` · 当前范围 ${date(b.start*1000)} 至 ${date(b.end*1000)}` : ""}${b.last_success ? ` · 最近完成 ${date(b.last_success)}` : ""}${b.error_code ? ` · 读取受阻：${errors[b.error_code] || b.error_code}` : ""}${b.retry_at ? ` · 重试 ${date(b.retry_at)}` : ""}`;return li;
    }));
    const chats=(d.chats || []).filter(x=>x.relationship==="self" || x.relationship==="pending" || x.error_code || x.scan || !x.completed);
    $("lark-chat-status").hidden=!chats.length;
    $("lark-chat-status").replaceChildren(...chats.map(x=>{const li=document.createElement("li");li.textContent=`${x.name || x.chat_id} · ${x.relationship==="self" ? "本人自聊，已排除自动接收与处理" : x.relationship==="pending" ? `会话关系待核验${x.error_code ? `（${x.error_code}）` : ""}` : x.error_code ? `读取受阻 ${x.error_code}${x.retry_at ? `，重试 ${date(x.retry_at)}` : ""}` : x.scan ? `检查中（${x.scan.pages} 页）` : "待检查"}`;return li;}));
    onboardingUI.renderMemberPlatformAddition();
  }

  async function changeLarkMessages(action) {
    const ws=workspaceUI.workspaceView;if(!ws?.messages || ws.messagesSending || $(action==="configure"?"lark-save":"lark-sync").disabled)return;
    const payload={service_epoch:state.catalog.service_epoch,caller:{kind:"human",id:ws.operator},...ws.room,provider:"lark",channel:"messages",subscription_id:ws.messages.subscription_id,subscription_version:ws.messages.subscription_version};
    const scope=ws.notificationScope;
    if(action==="configure")payload.config=larkMessagesDraft();
    else payload.target=notificationRequestTarget(activeNotificationTarget(ws));
    if(action==="configure" && ws.notificationAddition?.key===scope && await onboardingUI.captureMemberPlatformDraft(payload.config))return;
    beginNotificationWrite(ws,scope);
    ws.messagesRevision=(ws.messagesRevision||0)+1;ws.messagesSending=action;ws.messagesError="";
    const revision=ws.messagesRevision;
    const current=()=>notificationContextCurrent(ws,scope,payload.service_epoch) && ws.messagesRevision===revision;
    renderLarkMessages();
    try{
      const data=await request(`/v1/platform/notifications/${action}`,payload);
      if(!current())return;if(data.config.platform_id!==ws.notificationPlatformID || !sameNotificationTarget(data.config.target,activeNotificationTarget(ws)))throw Error("Notification scope mismatch");const replaced=ws.messages.subscription_id && ws.messages.subscription_id!==data.subscription_id;ws.messages=data;ws.larkSources=[...(ws.larkSources || []).filter(s=>s.config.platform_id!==data.config.platform_id || !sameNotificationTarget(s.config.target,data.config.target)),data];ws.messagesConnected=true;ws.larkDirty=false;ws.larkFilled=null;ws.messagesFeedback=action==="sync" ? "已发起同步，结果将自动更新。" : replaced ? "已为新连接开启订阅，旧订阅已暂停。" : "设置已保存。";
    }catch(e){if(current())ws.messagesError=e.code==="conflict"?"设置或账号已变化，请刷新后核对。":"结果未确认，请刷新状态；不会自动重发。";}
    finally{ws.notificationWrites?.delete(scope);if(current()){ws.messagesRevision++;ws.messagesSending=false;renderGitLabEvents();}else if(ws===workspaceUI.workspaceView)renderGitLabEvents();}
  }

  function gitlabProjectDirectory(ws) {
    const connection=todoPlatform(ws);
    const key=JSON.stringify([state.catalog?.service_epoch,ws.notificationScope,connection?.id,connection?.version,connection?.validation]);
    if(ws.gitlabProjectDirectory?.key!==key){
      clearTimeout(ws.gitlabProjectDirectory?.timer);
      ws.gitlabProjectDirectory={key,search:"",projects:new Map(),loaded:false,loading:false,nextPage:0,error:""};
      $("gitlab-project-search").value="";
    }
    return ws.gitlabProjectDirectory;
  }

  function renderGitLabProjectPicker(ws) {
    const dir=gitlabProjectDirectory(ws),connection=todoPlatform(ws),ready=todoVerified(connection),busy=gitlabSettingsBusy(ws);
    const selected=ws.gitlabDraft.events.project;
    const needsPlatform=!ws.gitlabDraft.platform_id;
    $("gitlab-project-summary").textContent=ready ? selected || "选择项目" : needsPlatform ? "请先选择平台" : "请先验证平台";
    $("gitlab-project-hint").textContent=needsPlatform ? "请先在上方选择平台，再选择项目。" : !connection ? "所选平台不可用，请在上方重新选择。" : !ready ? "请先在「管理平台」中验证所选连接，再选择项目。" : "";
    $("gitlab-project-hint").hidden=ready;
    if(!ready)$("gitlab-project-picker").open=false;
    $("gitlab-project-summary").setAttribute("aria-disabled",String(!ready || busy));
    $("gitlab-project-search").disabled=!ready || busy;
    $("gitlab-project-options").replaceChildren(...[...dir.projects.values()].map(project=>{
      const label=document.createElement("label"),input=document.createElement("input"),text=document.createElement("span"),name=document.createElement("span"),path=document.createElement("small");
      label.className="gitlab-project-option";input.type="radio";input.name="gitlab-project";input.value=project.path_with_namespace;input.checked=selected===input.value;input.disabled=!ready || busy;
      name.textContent=project.name;path.textContent=project.path_with_namespace;text.append(name,path);label.append(input,text);
      input.addEventListener("change",()=>{
        if(ws!==workspaceUI.workspaceView || gitlabProjectDirectory(ws)!==dir || gitlabSettingsBusy(ws))return;
        ws.gitlabDraft.events.project=project.path_with_namespace;
        $("gitlab-project-picker").open=false;editGitLabConfig();$("gitlab-project-summary").focus();
      });return label;
    }));
    $("gitlab-project-status").textContent=!ready ? "请先选择并验证 GitLab 连接。" : dir.error || (dir.loading || dir.timer ? "正在搜索项目…" : !dir.loaded ? "展开后加载可访问的项目。" : !dir.projects.size ? "没有匹配的项目，请更换关键词。" : `已加载 ${dir.projects.size} 个项目${dir.nextPage ? " · 可加载更多" : ""}`);
    $("gitlab-project-more").hidden=!dir.nextPage;
    $("gitlab-project-more").disabled=!ready || busy || dir.loading || !!dir.timer;
    $("gitlab-project-refresh").disabled=!ready || busy || dir.loading || !!dir.timer;
  }

  async function loadGitLabProjects(reset=false) {
    const ws=workspaceUI.workspaceView,epoch=state.catalog?.service_epoch,connection=ws && todoPlatform(ws);
    if(!ws?.gitlabDraft || !epoch || !todoVerified(connection) || gitlabSettingsBusy(ws) || !$("gitlab-project-picker").open)return;
    let dir=gitlabProjectDirectory(ws);
    if(reset){
      clearTimeout(dir.timer);
      dir=ws.gitlabProjectDirectory={...dir,projects:new Map(),loaded:false,loading:false,nextPage:0,error:"",timer:null};
    }
    if(dir.loading || dir.timer || dir.loaded && !dir.nextPage)return;
    const number=dir.loaded ? dir.nextPage : 1;
    dir.loading=true;dir.error="";renderGitLabProjectPicker(ws);
    try{
      const page=await request("/v1/platform/gitlab/projects",{service_epoch:epoch,...ws.room,caller:{kind:"human",id:ws.operator},platform_id:connection.id,platform_version:connection.version,search:dir.search,page:number},undefined,45000);
      if(ws!==workspaceUI.workspaceView || epoch!==state.catalog?.service_epoch || gitlabProjectDirectory(ws)!==dir)return;
      if(!Array.isArray(page.projects) || !Number.isInteger(page.next_page) || page.next_page!==0 && page.next_page<=number || page.projects.some(p=>!Number.isSafeInteger(p.id) || p.id<=0 || typeof p.name!=="string" || typeof p.path_with_namespace!=="string"))throw Error("invalid page");
      for(const project of page.projects)dir.projects.set(project.id,project);
      dir.loaded=true;dir.nextPage=page.next_page;
    }catch(e){
      if(ws===workspaceUI.workspaceView && gitlabProjectDirectory(ws)===dir){
        dir.error=e.code==="conflict" ? "连接已变化，请刷新通知设置后重试。" : e.code==="credential" ? "连接已失效，请重新验证后重试。" : "项目列表读取失败，可刷新列表重试；已选项目保留。";
      }
    }finally{
      dir.loading=false;
      if(ws===workspaceUI.workspaceView && gitlabProjectDirectory(ws)===dir)renderGitLabProjectPicker(ws);
    }
  }

  function notificationPlatforms(ws = workspaceUI.workspaceView) { return [...(ws?.platforms || []), ...(ws?.larkPlatforms || [])]; }

  function notificationView(ws, item, target = activeNotificationTarget(ws)) {
    return item.provider === "gitlab" ? ws?.gitlabPlatforms?.find(v => v.platform_id === item.id && sameNotificationTarget(v.config.target,target))
      : ws?.larkSources?.find(v => v.config.platform_id === item.id && sameNotificationTarget(v.config.target,target));
  }

  function notificationConfigured(item, view) { return item.provider === "gitlab" ? !!view?.version || !!view?.events?.subscription_id || !!view?.todos?.subscription_id : !!view?.subscription_id; }

  function notificationIssue(ws, item, view) {
    const error = view?.target_error_code || (item.provider === "gitlab" ? Object.values(view?.errors || {}).find(Boolean) || view?.events?.error_code || view?.todos?.error_code || view?.events?.delivery_error_code || view?.todos?.delivery_error_code : view?.error_code || view?.delivery_error_code);
    return !ws.notificationsConnected ? "通知状态暂不可读" : ws.notificationToggleError?.key === notificationScopeKey(view?.config.target,item.id) ? ws.notificationToggleError.message
      : error ? notificationTargetErrors[error] || gitlabEventErrors[error] || "通知需处理，请查看详情"
      : item.validation?.status !== "valid" ? "账号需要核验" : item.provider === "gitlab" && Object.values(view?.pending || {}).some(Boolean) ? "部分通知等待应用" : "";
  }

  function notificationDescription(config) {
    if (!config) return "设置接收";
    const content = config.todos || config.events ? [config.todos?.enabled ? "我的待办" : "", config.events?.enabled ? "项目互动" : ""].filter(Boolean).join("、")
      : [config.private?.enabled ? config.private.scope === "all" ? "全部人员私聊" : "指定私聊" : "", config.groups?.enabled ? `${config.groups.scope === "all" ? "全部群聊" : "指定群聊"} · ${config.groups.message_mode === "all" ? "全部消息" : [config.groups.at_me ? "@我" : "",config.groups.at_all ? "@所有人" : ""].filter(Boolean).join("、")}` : ""].filter(Boolean).join("、");
    return `${config.enabled ? "" : "已暂停 · "}${content || "未选择内容"}`;
  }

  function notificationRow(ws, item, target, editable = false) {
    const view=notificationView(ws,item,target) || {config:{}}, row=document.createElement("div"), detail=document.createElement("button");
    row.className="notification-source-row";row.dataset.platformId=item.id;row.dataset.membershipId=target.membership_id || "";
    detail.type="button";detail.className="notification-source";detail.dataset.provider=item.provider;
    detail.setAttribute("aria-controls","notification-detail");detail.setAttribute("aria-label",`${item.name} 通知详情`);
    const avatar=document.createElement("span"),label=document.createElement("span"),title=document.createElement("strong"),summary=document.createElement("small"),arrow=editable ? null : document.createElement("button");
    avatar.className="avatar integration-avatar";avatar.textContent=item.provider==="gitlab" ? "GL" : item.provider==="scion" ? "SC" : "飞";avatar.setAttribute("aria-hidden","true");
    label.className="notification-source-name";title.textContent=item.name;summary.textContent=notificationDescription(view.config);label.append(title,summary);detail.append(avatar,label);
    if(arrow){arrow.type="button";arrow.className="notification-source-arrow";arrow.textContent="›";arrow.setAttribute("aria-label",`${item.name} 通知详情`);}
    const open=opener=>target.mode==="inbox" ? membersUI.openMemberPlatform(opener,item,target) : openNotificationSource(opener,"gitlab",item.id,target);
    detail.addEventListener("click",()=>open(detail));if(arrow)arrow.addEventListener("click",()=>open(arrow));
    const toggle=document.createElement("input");toggle.type="checkbox";toggle.className="notification-switch";
    toggle.checked=ws.notificationToggle===item.id && sameNotificationTarget(ws.notificationToggleTarget,target) ? ws.notificationToggleEnabled : !!view.config.enabled;
    toggle.setAttribute("role","switch");toggle.setAttribute("aria-label",`${item.name} 接收通知`);
    toggle.disabled=notificationWritePending(ws,notificationScopeKey(target,item.id)) || !state.catalogConnected || !ws.notificationsConnected || !!ws.notificationToggle || !!ws.gitlabSending || !!ws.messagesSending || roomsUI.managementBusy();
    toggle.addEventListener("change",()=>notificationConfigured(item,notificationView(ws,item,target)) ? togglePlatformNotification(ws,item,toggle.checked,target) : onboardingUI.initializeMemberNotifications(item,target,toggle.checked));
    const issue=document.createElement("p");issue.className="notification-source-issue";issue.setAttribute("role","status");issue.textContent=notificationIssue(ws,item,view);issue.hidden=!issue.textContent;
    row.append(detail,toggle,...(arrow ? [arrow] : []),issue);return row;
  }

  function renderNotificationList(prefix, target) {
    const ws=workspaceUI.workspaceView,items=notificationPlatforms(ws).filter(item=>target.mode==="inbox" || item.provider==="gitlab");
    const configured=items.filter(item=>notificationConfigured(item,notificationView(ws,item,target)));
    $(`${prefix}platforms`).replaceChildren(...configured.map(item=>notificationRow(ws,item,target)));
    $(`${prefix}source-count`).textContent=String(configured.length);
    $(`${prefix}empty`).hidden=!!configured.length;
    $(`${prefix}empty`).textContent=ws?.platformsLoading ? "正在读取平台…" : target.mode==="inbox" ? "尚未配置来源，添加通知来源后开始接收。" : roomsUI.isDirect() ? "将平台通知发送到当前会话。" : "将平台通知发送到当前房间。";
    const unavailable=!!ws && (!ws.notificationsConnected || !!ws.repositoryPlatformsError);
    notice(`${prefix}list-error`,unavailable ? ws.repositoryPlatformsError || "通知状态暂不可读，请刷新。" : "");
    $(`${prefix}list-reload`).hidden=!unavailable;
    const query=$(`${prefix}add-search`).value.trim().toLowerCase();
    $(`${prefix}add-options`).replaceChildren(...items.filter(item=>!configured.includes(item) && [item.name,item.validation?.username,item.url].join(" ").toLowerCase().includes(query)).map(item=>{
      const button=document.createElement("button");button.type="button";button.className="notification-add-option";button.dataset.provider=item.provider;button.dataset.platformId=item.id;
      const name=document.createElement("strong"),account=document.createElement("small");name.textContent=item.name;account.textContent=`${item.validation?.username ? `@${item.validation.username} · ` : ""}设置接收 ›`;button.append(name,account);
      button.disabled=!ws.notificationsConnected || roomsUI.managementBusy();
      button.addEventListener("click",()=>{ setNotificationPicker(prefix,false);return openNotificationSource(button,item.provider==="lark" ? "messages" : "gitlab",item.id,target); });return button;
    }));
    return configured;
  }

  function renderNotificationPlatforms() {
    const ws=workspaceUI.workspaceView;
    renderNotificationList("notification-",{});
    membersUI.renderMemberPlatforms();
    const orphanViews=[...(ws?.gitlabPlatforms || []),...(ws?.larkSources || [])].filter(v=>v.config.target?.mode==="inbox" && !state.members.some(m=>sameNotificationTarget(memberNotificationTarget(m),v.config.target)));
    $("notification-orphans").hidden=!orphanViews.length;
    $("notification-orphan-items").replaceChildren(...orphanViews.map(v=>{
      const id=v.platform_id || v.config.platform_id;
      const item=notificationPlatforms(ws).find(p=>p.id===id) || {id,name:"原平台暂不可用",provider:v.provider || "gitlab"};
      const row=notificationRow(ws,item,v.config.target);
      row.children[0].children[1].children[0].textContent=`${v.config.target.agent_id} · ${item.name}`;
      row.children[0].children[1].children[1].textContent="成员已移除 · 查看诊断";
      return row;
    }));
  }

  function setNotificationPicker(prefix, open) {
    $(`${prefix}add-picker`).hidden=!open;
    $(`${prefix}add-toggle`).setAttribute("aria-expanded",String(open));
    if(prefix==="member-notification-")$("member-notification-empty").hidden=open || Number($("member-notification-source-count").textContent)>0;
    if(open)$(`${prefix}add-search`).focus({preventScroll:true});
  }

  async function togglePlatformNotification(ws, item, enabled, target=activeNotificationTarget(ws)) {
    if(ws!==workspaceUI.workspaceView || !ws.notificationsConnected || ws.notificationToggle || ws.gitlabSending || ws.messagesSending || roomsUI.managementBusy())return;
    const view=notificationView(ws,item,target);if(!notificationConfigured(item,view) || notificationWritePending(ws,notificationScopeKey(target,item.id)))return;
    const epoch=state.catalog.service_epoch,scope=ws.notificationScope,gitlab=item.provider==="gitlab",key=notificationScopeKey(target,item.id);
    beginNotificationWrite(ws,key);
    const revisionKey=gitlab ? "gitlabRevision" : "messagesRevision";ws[revisionKey]=(ws[revisionKey] || 0)+1;
    const revision=ws[revisionKey];
    const current=()=>notificationContextCurrent(ws,scope,epoch) && ws[revisionKey]===revision;
    ws.notificationToggle=item.id;ws.notificationToggleTarget=target;ws.notificationToggleEnabled=enabled;ws.notificationToggleError=null;renderGitLabEvents();
    try {
      const body={service_epoch:epoch,caller:{kind:"human",id:ws.operator},...ws.room,config:{...structuredClone(view.config),enabled,target:notificationRequestTarget(target)}};
      if(gitlab)Object.assign(body,{platform_id:item.id,version:view.version});
      else Object.assign(body,{provider:"lark",channel:"messages",subscription_id:view.subscription_id,subscription_version:view.subscription_version});
      const next=await request(gitlab ? "/v1/platform/gitlab/notifications/configure" : "/v1/platform/notifications/configure",body,undefined,50000);
      if(!current())return;
      if(!sameNotificationTarget(next.config.target,target) || next.config.platform_id!==item.id)throw Error("Notification scope mismatch");
      if(gitlab){
        ws.gitlabPlatforms=[...(ws.gitlabPlatforms || []).filter(v=>notificationScopeKey(v.config.target,v.platform_id)!==key),next];
        if(ws.gitlab?.config.platform_id===item.id && sameNotificationTarget(activeNotificationTarget(ws),target))acceptGitLabView(ws,next);
      } else {
        ws.larkSources=[...(ws.larkSources || []).filter(v=>notificationScopeKey(v.config.target,v.config.platform_id)!==key),next];
        if(ws.messages?.config.platform_id===item.id && sameNotificationTarget(activeNotificationTarget(ws),target)){ws.messages=next;$("lark-enabled").checked=!!next.config.enabled;}
      }
    }catch(error){
      if(current())ws.notificationToggleError={key,message:error.code==="request" ? "请先在详情完善接收设置" : error.code==="conflict" ? "设置已变化，请核对后重试" : "结果未确认，请刷新"};
    }finally{
      ws.notificationWrites?.delete(key);
      if(current()){ws[revisionKey]++;ws.notificationToggle=null;await syncGitLabNotifications(state.catalog,ws);renderGitLabEvents();}
      else if(ws===workspaceUI.workspaceView)renderGitLabEvents();
    }
  }

  function renderGitLabEvents() {
    renderNotificationPlatforms(); renderGitLabTodos(); renderLarkMessages(); renderNotificationSource();
    const ws = workspaceUI.workspaceView, data = ws?.events, config = ws?.gitlabDraft?.events;
    $("gitlab-events").hidden = !data || !data.available && !data.subscription_version;
    $("gitlab-events-fields").disabled = !config || gitlabSettingsBusy(ws);
    if (!data || !config) { renderGitLabSettings(); return; }
    renderGitLabProjectPicker(ws);
    $("gitlab-events-enabled").checked = config.enabled;
    for (const input of gitlabEventTypeInputs()) input.checked = !ws.eventsEmptyTypes && (!config.filter.types.length || config.filter.types.includes(input.value));
    const draft = gitlabEventDraftState(data);
    $("gitlab-events-all-types").checked = draft.count === gitlabEventTypeIDs.length;
    $("gitlab-events-all-types").indeterminate = draft.count > 0 && draft.count < gitlabEventTypeIDs.length;
    $("gitlab-events-feedback").textContent = draft.invalid ? draft.targetInvalid ? "请选择已验证连接和项目" : "请检查活动筛选" : "";
    $("gitlab-events-action-hint").textContent = draft.targetChanged && data.subscription_id ? "更换连接、账号或项目将新建订阅；旧进度与积压保留。" : "";
    notice("gitlab-events-error", gitlabApplyNotice(ws, "events") || notificationTargetErrors[data.delivery_error_code] || gitlabEventErrors[data.delivery_error_code] || gitlabEventErrors[data.error_code] || "");
    renderGitLabSettings();
  }

  function editGitLabEvents() {
    const ws = workspaceUI.workspaceView;
    if (!ws?.gitlabDraft || gitlabSettingsBusy(ws)) { renderGitLabEvents(); return; }
    const types = gitlabEventTypeInputs().filter(input => input.checked).map(input => input.value);
    ws.eventsEmptyTypes = !types.length;
    Object.assign(ws.gitlabDraft.events, { enabled: $("gitlab-events-enabled").checked,
      filter: { types: types.length === gitlabEventTypeIDs.length ? [] : types } });
    editGitLabConfig();
  }

  const gitlabTodoReasons = ["mentioned", "assigned", "review_requested", "other"];

  const gitlabTodoTypes = ["merge_request", "work_item"];

  const gitlabTodoErrors = { ...gitlabEventErrors, credential_unavailable: "个人连接凭据不可用，请在平台中重新验证。", binding_changed: "账号身份已变化，请核对后显式保存。", scan_incomplete: "补查尚未完成，将按检查周期继续；完整同步时间尚未更新。" };

  function todoConfig(config) {
    return { enabled: !!config.enabled,
      filter: { reasons: [...(config.filter?.reasons ?? gitlabTodoReasons)], types: [...(config.filter?.types ?? gitlabTodoTypes)] } };
  }

  function todoConfigKey(config) {
    const c = todoConfig(config);
    c.filter.reasons.sort(); c.filter.types.sort();
    return JSON.stringify(c);
  }

  function todoPlatform(ws) { return ws.platforms?.find(item => item.id === ws.gitlabDraft?.platform_id); }

  function todoVerified(connection) { return connection?.validation?.status === "valid" && connection.validation.user_id > 0; }

  function gitlabTodoDraftState(ws) {
    const data = ws.todos, config = ws.gitlabDraft.todos;
    const connection = todoPlatform(ws);
    const switching = data.subscription_id && todoVerified(connection) && (data.recipient_id !== connection.validation.user_id || data.instance !== connection.url);
    const dirty = todoConfigKey(config) !== todoConfigKey(ws.gitlab.config.todos) || !!switching && config.enabled;
    const unavailable = config.enabled && !todoVerified(connection);
    return { connection, switching, dirty, unavailable };
  }

  function fillGitLabTodos(config) {
    $("gitlab-todos-enabled").checked = config.enabled;
    for (const [group, values] of [["reason", gitlabTodoReasons], ["type", gitlabTodoTypes]]) {
      const selected = config.filter[group === "reason" ? "reasons" : "types"];
      for (const value of values) $(`gitlab-todos-${group}-${value}`).checked = selected.includes(value);
      const count = values.filter(value => selected.includes(value)).length;
      const all = $(`gitlab-todos-all-${group === "reason" ? "reasons" : "types"}`);
      all.checked = count === values.length;
      all.indeterminate = count > 0 && count < values.length;
    }
  }

  function renderGitLabTodos() {
    const ws = workspaceUI.workspaceView, data = ws?.todos, config = ws?.gitlabDraft?.todos;
    $("gitlab-todos-fields").disabled = !config || gitlabSettingsBusy(ws);
    if (!data || !config) { notice("gitlab-todos-error", ""); return; }
    fillGitLabTodos(config);
    const { switching, unavailable } = gitlabTodoDraftState(ws);
    $("gitlab-todos-feedback").textContent = unavailable ? "请检查接收账号的连接" : "";
    $("gitlab-todos-action-hint").textContent = switching && config.enabled ? "保存所选平台的通知设置，其他平台保留各自设置和进度。" : "";
    if (!config.filter.reasons.length || !config.filter.types.length) $("gitlab-todos-action-hint").textContent += " 当前过滤为空，不接收匹配项。";
    notice("gitlab-todos-error", gitlabApplyNotice(ws, "todos") || notificationTargetErrors[data.delivery_error_code] || gitlabTodoErrors[data.delivery_error_code] || gitlabTodoErrors[data.error_code] || "");
  }

  function editGitLabTodos() {
    const ws = workspaceUI.workspaceView;
    if (!ws?.gitlabDraft || gitlabSettingsBusy(ws)) { renderGitLabEvents(); return; }
    Object.assign(ws.gitlabDraft.todos, { enabled: $("gitlab-todos-enabled").checked,
      filter: { reasons: gitlabTodoReasons.filter(value => $(`gitlab-todos-reason-${value}`).checked), types: gitlabTodoTypes.filter(value => $(`gitlab-todos-type-${value}`).checked) } });
    editGitLabConfig();
  }

  const gitlabChannels = ["events", "todos"];

  function gitlabApplyNotice(ws, channel) {
    const code = ws?.gitlab?.errors?.[channel];
    if (code === "sync_failed") return "同步未完成，请核对连接后重试同步。";
    return code ? "设置已保存，但此类消息尚未成功应用。核对连接与设置后，点击重试应用。" : ws?.gitlab?.pending?.[channel] ? "设置等待应用，请点击重试应用。" : "";
  }

  function gitlabChannelDirty(channel) {
    const ws = workspaceUI.workspaceView;
    if (!ws?.gitlabDraft) return false;
    return channel === "events" ? gitlabEventDraftState(ws.events).dirty : gitlabTodoDraftState(ws).dirty;
  }

  function gitlabNeedsApply(ws) {
    if (!ws?.gitlabDraft) return false;
    return gitlabChannels.some(channel => ws.gitlab.pending?.[channel] || gitlabChannelDirty(channel));
  }

  function renderGitLabSettings() {
    const ws = workspaceUI.workspaceView, shared = gitlabDraft();
    const busy = gitlabSettingsBusy(ws);
    $("gitlab-shared-fields").disabled = busy;
    $("gitlab-interval").disabled = busy;
    $("gitlab-interval").value = shared.interval_seconds;
    if (!ws?.gitlabBackfillEditing) fillBackfill("gitlab",shared);
    const backfillLocked=!!ws?.gitlab?.backfill_locked || !!ws?.events?.subscription_id || !!ws?.todos?.subscription_id;
    const backfillError=renderBackfill("gitlab",backfillLocked,shared.backfill_start || ws?.events?.baseline || ws?.todos?.baseline,busy);
    if (!backfillLocked) $("gitlab-backfill-help").textContent="首次开启后固定接收起点。";
    const connectionID = shared.platform_id, options = gitlabPlatformOptions(connectionID, "请选择连接");
    setSelectOptions("gitlab-connection", options, connectionID);$("gitlab-connection").hidden=true;
    const connection = ws?.platforms?.find(item => item.id === connectionID);
    $("gitlab-connection").title = connection?.url || "";
    $("gitlab-notification-account").textContent = todoVerified(connection) ? `接收账号：@${connection.validation.username}（ID ${connection.validation.user_id}，已验证）`
      : ws?.platformsError || (connectionID ? "请先验证所选连接。" : "");
    $("gitlab-notification-account").hidden = !$("gitlab-notification-account").textContent;
    const target = JSON.stringify(activeNotificationTarget(ws));
    setSelectOptions("gitlab-target", [new Option("固定接收者",target)], target);$("gitlab-target").disabled=true;
    for (const channel of gitlabChannels) {
      $(`gitlab-${channel}-enabled`).disabled = busy;
      if (gitlabApplyNotice(ws, channel)) $(`gitlab-${channel}-settings`).open = true;
      const data = ws?.[channel], error = ws?.gitlab?.errors?.[channel] || data?.error_code || data?.delivery_error_code;
      $(`gitlab-${channel}-state`).textContent = !ws?.gitlabConnected ? "状态不可读" : ws.gitlab.pending?.[channel] ? "等待应用" : gitlabChannelDirty(channel) ? "待保存"
        : !shared[channel].enabled || !shared.enabled ? "" : error ? "需处理" : !data?.status || data.status==="disabled" ? "采集未启动" : "";
      $(`gitlab-${channel}-summary`).setAttribute("data-error", String(!!error));
      const errors = channel === "events" ? gitlabEventErrors : gitlabTodoErrors;
      const problem = gitlabApplyNotice(ws, channel) || notificationTargetErrors[data?.delivery_error_code] ||
        errors[data?.delivery_error_code] || errors[data?.error_code] || data?.delivery_error_code || data?.error_code || "";
      const status = $(`gitlab-${channel}-status`);
      status.textContent = !ws?.gitlabConnected || !data ? "状态暂不可读，请刷新。" :
        [notificationSyncSummary(data), problem].filter(Boolean).join(" · ");
      status.setAttribute("data-error", String(!!problem || !ws?.gitlabConnected));
    }
    const pending = gitlabNeedsApply(ws), dirty = !!ws?.gitlabDirty || !!shared.platform_id && !ws?.gitlab?.version && !ws?.events?.subscription_id && !ws?.todos?.subscription_id;
    const invalid = !!backfillError || !Number.isInteger(shared.interval_seconds) || shared.interval_seconds < 3 || shared.interval_seconds > 120 || !!ws?.gitlabDraft && (gitlabEventDraftState(ws.events).invalid || gitlabTodoDraftState(ws).unavailable);
    $("gitlab-save").disabled = busy || !(dirty || pending) || invalid;
    $("gitlab-save").textContent = ws?.gitlabSending === "configure" ? "保存中…" : !dirty && pending ? "重试应用" : "保存设置";
    const active = ws?.gitlab?.config.enabled !== false && (ws?.gitlab?.config.events.enabled || ws?.gitlab?.config.todos.enabled);
    $("gitlab-sync").hidden = false;
    $("gitlab-sync").disabled = busy || dirty || pending || invalid || !active;
    $("gitlab-sync").textContent = ws?.gitlabSending === "sync" ? "请求中…" : "立即同步";
    $("gitlab-feedback").textContent = ws?.gitlabError || (ws?.gitlabSending ? "正在处理 GitLab 通知设置…"
      : ws?.gitlab?.applying ? "正在应用 GitLab 通知设置…"
      : ws?.gitlab?.errors?.config === "settings_mismatch" ? "现有订阅设置不一致，请选择公共连接、投递位置和检查周期后保存。"
      : pending && !dirty ? "设置已保存，部分消息等待应用。请核对提示后重试应用。" : dirty ? invalid ? "请完善设置后保存。" : "有未保存的更改" : ws?.gitlabFeedback || "");
    onboardingUI.renderMemberPlatformAddition();
  }

  async function editGitLabShared(field) {
    const ws = workspaceUI.workspaceView;
    if (!ws?.gitlabDraft || $("gitlab-shared-fields").disabled) { renderGitLabEvents(); return; }
    const draft = ws.gitlabDraft;
    if (field === "connection") {
      const id = $("gitlab-connection").value;
      if (id && ws.gitlabPlatforms) {
        const revision = ws.gitlabRevision = (ws.gitlabRevision || 0) + 1;
        const epoch = state.catalog.service_epoch;
        ws.gitlabSending = "select"; renderGitLabEvents();
        try {
          const params = new URLSearchParams({service_epoch:epoch,caller_id:ws.operator,...ws.room,platform_id:id,...notificationTargetQuery(activeNotificationTarget(ws))});
          const view = await request(`/v1/platform/gitlab/notifications?${params}`);
          if (ws !== workspaceUI.workspaceView || state.catalog?.service_epoch !== epoch || ws.gitlabRevision !== revision) return;
          acceptGitLabView(ws, view, true); ws.gitlabError = ""; ws.gitlabFeedback = "";
        } catch {
          if (ws !== workspaceUI.workspaceView || ws.gitlabRevision !== revision) return;
          ws.gitlabError = "平台通知设置暂不可读，请重新选择。";
        } finally {
          if (ws === workspaceUI.workspaceView && ws.gitlabRevision === revision) { ws.gitlabRevision++; ws.gitlabSending = false; renderGitLabEvents(); }
        }
        return;
      }
      draft.platform_id = id;
    }
    else if (field === "interval") draft.interval_seconds = Number($("gitlab-interval").value);
    else return;
    editGitLabConfig();
    if(field==="connection" && $("gitlab-project-picker").open)void loadGitLabProjects();
  }

  function editGitLabBackfill() {
    const ws=workspaceUI.workspaceView;
    if (!ws?.gitlabDraft || $("gitlab-backfill").disabled || $("gitlab-shared-fields").disabled) return;
    ws.gitlabBackfillEditing=true;
    delete ws.gitlabDraft.backfill_start;
    Object.assign(ws.gitlabDraft,backfillDraft("gitlab"));
    editGitLabConfig();
  }

  async function changeGitLabSettings(action) {
    const ws = workspaceUI.workspaceView;
    if (!ws || $(action === "configure" ? "gitlab-save" : "gitlab-sync").disabled) return;
    const payload = { service_epoch: state.catalog.service_epoch, caller: { kind: "human", id: ws.operator }, ...ws.room, version: ws.gitlab.version };
    payload.platform_id = action === "configure" ? ws.gitlabDraft.platform_id : ws.gitlab.config.platform_id;
    const scope=ws.notificationScope;
    if(action === "sync") payload.target=notificationRequestTarget(activeNotificationTarget(ws));
    if (action === "configure") {
      payload.config = {...structuredClone(ws.gitlabDraft),target:notificationRequestTarget(activeNotificationTarget(ws))};
      if (ws.gitlab.config.platform_id !== payload.platform_id && ws.gitlabPlatforms) payload.version = ws.gitlabPlatforms.find(view => view.platform_id === payload.platform_id && sameNotificationTarget(view.config.target,activeNotificationTarget(ws)))?.version || 0;
    }
    if(action==="configure" && ws.notificationAddition?.key===scope && await onboardingUI.captureMemberPlatformDraft(payload.config))return;
    beginNotificationWrite(ws,scope);
    ws.gitlabRevision = (ws.gitlabRevision || 0) + 1; ws.gitlabSending = action; ws.gitlabError = "";
    const revision=ws.gitlabRevision;
    const current=()=>notificationContextCurrent(ws,scope,payload.service_epoch) && ws.gitlabRevision===revision;
    renderGitLabEvents();
    try {
      const view = await request(`/v1/platform/gitlab/notifications/${action}`, payload);
      if (!current()) return;
      if(view.config.platform_id!==payload.platform_id || !sameNotificationTarget(view.config.target,activeNotificationTarget(ws)))throw Error("Notification scope mismatch");
      acceptGitLabView(ws, view, true);
      ws.gitlabPlatforms = [...(ws.gitlabPlatforms || []).filter(v => v.platform_id !== view.config.platform_id || !sameNotificationTarget(v.config.target,view.config.target)), {...view, platform_id:view.config.platform_id}];
      ws.gitlabFeedback = action === "sync" ? "已发起同步，结果将自动更新。" : "设置已保存。";
    } catch (error) {
      if (!current()) return;
      ws.gitlabError = error.code === "conflict" ? action === "sync" ? "配置或账号已变化，请核对最新状态。" : "设置版本已变化，请核对最新状态后重新保存。" : error.code === "request" ? "设置无效，请检查连接、项目和筛选。" : "结果未确认，请核对最新状态后重试；不会自动重发。";
      ws.gitlabSending = false; await syncGitLabNotifications(state.catalog, ws);
    } finally {
      ws.notificationWrites?.delete(scope);
      if (current()) { ws.gitlabRevision++; ws.gitlabSending = false; renderGitLabEvents(); }
      else if(ws===workspaceUI.workspaceView)renderGitLabEvents();
    }
  }

  let notificationFocus = null;

  function renderContextViews() {
    const detail = $("activity-dialog").open || $("notification-detail").open || roomsUI.isDirect() && !layoutUI.directNotificationsOpen;
    $("close-conversation-notifications").hidden = !roomsUI.isDirect() || !layoutUI.directNotificationsOpen;
    $("member-list-view").hidden = roomsUI.isDirect() && layoutUI.directNotificationsOpen ? $("notification-detail").open : detail;
    $("member-list-view").inert = $("member-list-view").hidden || !layoutUI.membersPanelOpen();
    $("member-list-view").setAttribute("aria-hidden", String($("member-list-view").inert));
  }

  function renderNotificationSource() {
    const ws = workspaceUI.workspaceView, messages = ws?.notificationChannel === "messages";
    const id = ws?.notificationPlatformID || (messages ? ws?.messages?.config.platform_id : ws?.gitlabDraft?.platform_id);
    const item = membersUI.memberPlatformItems(ws).find(p => p.id === id), view = item && notificationView(ws, item);
    const target=activeNotificationTarget(ws),member=state.members.find(m=>sameNotificationTarget(memberNotificationTarget(m),target));
    const name=target.mode==="inbox" ? membersUI.memberRole(member || {})?.name || target.agent_id : roomsUI.isDirect() ? "会话" : "房间";
    $("notification-title").textContent=`${name} · ${item?.name || membersUI.memberPlatformItems(ws).find(p=>p.id===id)?.name || (messages ? "飞书" : "GitLab")} ${target.mode==="inbox" ? "设置" : "通知"}`;
    let instance="";try{instance=item?.provider==="gitlab" ? new URL(item.url).host : "";}catch{instance=item?.url || "";}
    $("notification-account").textContent=item ? [item.validation?.username ? `@${item.validation.username}` : "账号待核验",instance].filter(Boolean).join(" · ") : "";
    $("notification-account").title=item?.url || "";
    const coexist=target.mode==="inbox" && notificationView(ws,item || {},{})?.config.enabled || target.mode!=="inbox" && ws?.gitlabPlatforms?.some(v=>v.platform_id===id && v.config.target?.mode==="inbox" && v.config.enabled);
    $("notification-destination").textContent=(target.mode==="inbox" ? `发送到 ${name} 的收件箱${member ? "" : "（原成员已移除）"}` : roomsUI.isDirect() ? "发送到当前会话消息流" : "发送到当前房间，成员可见") + (coexist ? "。成员和房间分别订阅时，可能同时看到同一通知。" : "");
    $("notification-sources").hidden=!ws;
    const matches=!!id && (messages ? ws?.messages?.config.platform_id : ws?.gitlabDraft?.platform_id)===id && sameNotificationTarget(messages ? ws?.messages?.config.target : ws?.gitlabDraft?.target,target);
    const connected=matches && (messages ? ws?.messagesConnected : ws?.gitlabConnected) && !ws?.notificationHistory;
    $("notification-panel-messages").hidden=!messages || !connected;$("notification-panel-gitlab").hidden=messages || !connected;
    notice("notification-notice",ws?.notificationHistory || (!connected ? "通知状态暂不可读，请刷新。" : item && item.validation?.status!=="valid" ? "账号需要核验，请在管理平台中处理。" : ""));
    $("notification-reload").hidden=!!connected || !!ws?.notificationHistory;
    const configured=item && notificationConfigured(item,view),enabled=configured ? !!view.config.enabled : messages ? !!ws?.messages?.config.enabled : !!ws?.gitlabDraft?.enabled;
    const toolsOnly=ws?.notificationChannel==="tools",history=!!ws?.notificationHistory;
    $("notification-destination").hidden=toolsOnly || history;
    $("notification-destination").classList.toggle("sr-only",!coexist && (target.mode!=="inbox" || !!member));
    const status=!connected || toolsOnly || history ? "" : configured && !enabled ? "通知已暂停" : !configured ? "尚未开启通知" : "";
    notice("notification-enabled-state",status);
    notice("notification-toggle-error",ws?.notificationToggleError?.key===notificationScopeKey(target,id) ? ws.notificationToggleError.message : "");
    const backLabel=ws?.notificationReturn ? (roomsUI.isDirect() ? "返回助理详情" : "返回成员详情") : roomsUI.isDirect() ? "返回会话通知" : "返回房间通知";
    $("close-notification").setAttribute("aria-label",backLabel);$("close-notification").title=backLabel;
    if(history){$("notification-title").textContent="通知来源诊断";$("notification-account").textContent="";$("notification-destination").textContent="";}
    if(toolsOnly){$("notification-panel-messages").hidden=true;$("notification-panel-gitlab").hidden=true;notice("notification-notice","此平台暂无可调整的配置，工具启停请返回平台列表。");$("notification-reload").hidden=true;}
    $("gitlab-manage-platforms").disabled = !ws;
    onboardingUI.renderMemberPlatformAddition();renderContextViews();
  }

  async function openNotificationSource(opener, channel, platformID, target = {}) {
    const ws=workspaceUI.workspaceView;if(!ws || !platformID || channel==="messages" && target.mode!=="inbox")return;
    const navigation=++notificationNavigation;
    const epoch=state.catalog?.service_epoch,opening=ws.notificationOpen=(ws.notificationOpen || 0)+1;
    rememberNotificationDraft(ws);
    const key=notificationScopeKey(target,platformID),same=ws.notificationScope===key;
    notificationFocus=opener;
    if($("activity-dialog").open && !ws.notificationReturn)ws.notificationReturn={info:{...state.info},member:{...membersUI.activityMember},tab:membersUI.agentTab,platformScroll:$("platforms-member-view").scrollTop,activityScroll:$("activity-dialog").scrollTop,bodyScroll:$("activity-body").scrollTop,policyScroll:$("policy-member-view").scrollTop,scroll:$("inbox-member-view").scrollTop,windowScroll:{left:window.scrollX,top:window.scrollY}};
    ws.notificationTarget=structuredClone(target);ws.notificationScope=key;ws.notificationChannel=channel==="messages" ? "messages" : channel==="tools" ? "tools" : "gitlab";ws.notificationPlatformID=platformID;ws.notificationHistory="";
    if(!same){
      stopLarkChatSearch();ws.gitlabRevision=(ws.gitlabRevision || 0)+1;ws.messagesRevision=(ws.messagesRevision || 0)+1;
      ws.gitlabSending=false;ws.messagesSending=false;ws.notificationToggle=null;ws.notificationToggleError=null;
      ws.gitlab=null;ws.gitlabDraft=null;ws.events=null;ws.todos=null;ws.messages=null;ws.gitlabConnected=false;ws.messagesConnected=false;
      ws.gitlabDirty=false;ws.larkDirty=false;ws.eventsEmptyTypes=false;ws.gitlabBackfillEditing=false;ws.gitlabError="";ws.gitlabFeedback="";ws.messagesError="";ws.messagesFeedback="";ws.larkChatDirectories=null;ws.gitlabProjectDirectory=null;
    }
    $("activity-dialog").hidden=true;membersUI.activity.close();layoutUI.showNotificationDetail();
    if(!$("notification-detail").open)$("notification-detail").show();
    renderGitLabEvents();
    await workspaceUI.loadWorkspacePlatforms();
    if(!notificationContextCurrent(ws,key,epoch) || opening!==ws.notificationOpen || navigation!==notificationNavigation)return;
    if(channel==="tools"){renderNotificationSource();return;}
    if(channel==="messages"){
      if(!same){
        selectLarkPlatform(ws,platformID);
        const cached=ws.notificationDrafts?.get(key);
        if(cached){
          ws.larkRestore={...cached.config,enabled:ws.messages.config.enabled};ws.larkChatDirectories=cached.directories;ws.larkDirty=true;
          for(const kind of ["private","groups"])larkChatElement(kind,"search").value=cached.directories?.[kind]?.query || "";
        }
      }
      ws.messagesConnected=!!ws.notificationsConnected;
    }else if(!same || !ws.gitlabConnected){
      const revision=ws.gitlabRevision=(ws.gitlabRevision || 0)+1;
      ws.gitlabSending="select";renderGitLabEvents();
      try{
        const params=new URLSearchParams({service_epoch:epoch,caller_id:ws.operator,...ws.room,platform_id:platformID,...notificationTargetQuery(target)});
        const view=await request(`/v1/platform/gitlab/notifications?${params}`);
        if(!notificationContextCurrent(ws,key,epoch) || revision!==ws.gitlabRevision || navigation!==notificationNavigation)return;
        if(view.config.platform_id!==platformID || !sameNotificationTarget(view.config.target,target))throw Error("Notification scope mismatch");
        acceptGitLabView(ws,view,true);
        const cached=ws.notificationDrafts?.get(key);
        if(cached){ws.gitlabDraft=structuredClone(cached.config);ws.gitlabDraft.enabled=view.config.enabled;ws.eventsEmptyTypes=cached.emptyTypes;ws.gitlabDirty=!!cached.emptyTypes || gitlabConfigKey(ws.gitlabDraft)!==gitlabConfigKey(view.config);fillBackfill("gitlab",ws.gitlabDraft);}
      }catch{
        if(!notificationContextCurrent(ws,key,epoch) || revision!==ws.gitlabRevision || navigation!==notificationNavigation)return;
        ws.gitlabConnected=false;ws.gitlabError="平台通知设置暂不可读，请刷新。";
      }finally{if(notificationContextCurrent(ws,key,epoch) && revision===ws.gitlabRevision){ws.gitlabSending=false;ws.gitlabRevision++;}}
    }
    if(!notificationContextCurrent(ws,key,epoch) || opening!==ws.notificationOpen || navigation!==notificationNavigation)return;
    if(channel==="events" || channel==="todos")$(`gitlab-${channel}-settings`).open=true;
    renderGitLabEvents();$("notification-title").focus();
    if(compactViewport(window))$("notification-detail").scrollIntoView({block:"start"});
  }

  async function openNotificationMessage(opener, source) {
    const ws=workspaceUI.workspaceView,epoch=state.catalog?.service_epoch,navigation=++notificationNavigation;if(!ws)return;
    const showHistory=message=>{
      if(ws!==workspaceUI.workspaceView || state.catalog?.service_epoch!==epoch || navigation!==notificationNavigation)return;
      ws.notificationHistory=message;ws.notificationPlatformID=source?.platform_id || "";notificationFocus=opener;
      $("notification-detail").show();renderNotificationSource();$("notification-title").focus();
    };
    if(!source?.subscription_id || !source?.provider || !source?.channel){showHistory("此历史消息缺少完整来源身份，无法唯一定位原配置。");return;}
    try{
      const params=new URLSearchParams({service_epoch:epoch,caller_id:ws.operator,...ws.room,provider:source.provider,channel:source.channel,subscription_id:source.subscription_id,...(source.platform_id ? {platform_id:source.platform_id} : {})});
      const data=await request(`/v1/platform/notifications?${params}`);
      if(ws!==workspaceUI.workspaceView || state.catalog?.service_epoch!==epoch || navigation!==notificationNavigation)return;
      const views=[...(data.subscriptions || []),...(data.retired_subscriptions || [])];
      const exact=views.filter(v=>v.subscription_id===source.subscription_id && v.provider===source.provider && v.channel===source.channel && (!source.platform_id || v.platform_id===source.platform_id));
      if(exact.length!==1){showHistory("无法唯一定位此消息的原订阅；请检查来源诊断。");return;}
      const original=exact[0];
      if(source.instance && (original.instance || original.account?.instance)!==source.instance){showHistory("原订阅的实例身份与消息不匹配，请检查历史来源。");return;}
      if(original.retired){showHistory(`此消息来自已退役订阅 ${original.subscription_id}；原目标和历史消息保持不变。${notificationSyncSummary(original)}`);return;}
      await openNotificationSource(opener,source.channel==="messages" ? "messages" : source.channel,original.platform_id,original.target || original.config.target);
    }catch{showHistory("原订阅暂不可读或身份不匹配，未打开其他成员的设置。");}
  }

  async function reloadNotifications() {
    const ws=workspaceUI.workspaceView,id=ws?.notificationPlatformID,channel=ws?.notificationChannel,target=structuredClone(activeNotificationTarget(ws)),scope=ws?.notificationScope;
    await workspaceUI.loadWorkspacePlatforms();
    if(ws!==workspaceUI.workspaceView)return;
    await syncGitLabNotifications(state.catalog,ws);
    if(ws!==workspaceUI.workspaceView || ws.notificationScope!==scope)return;
    if(ws.notificationsConnected)ws.notificationToggleError=null;
    if($("notification-detail").open && id)await openNotificationSource(notificationFocus,channel,id,target);
    renderGitLabEvents();
  }

  function invalidateNavigation() { notificationNavigation++; }

  function currentSource() {
    const target = activeNotificationTarget();
    return { target, platformID: workspaceUI.workspaceView?.notificationPlatformID,
      memberAvailable: state.members.some(member => member.membership_id === target.membership_id) };
  }

  function mount() {
    for(const id of ["enabled","connection","target","private-enabled","groups-enabled","scope","groups-scope","group","group-mode","all","interval","backfill","backfill-start"]) $("lark-"+id).addEventListener("change",()=>{
      if(id==="connection" && workspaceUI.workspaceView)selectLarkPlatform(workspaceUI.workspaceView,$("lark-connection").value);
      else if(workspaceUI.workspaceView)workspaceUI.workspaceView.larkDirty=true;renderLarkMessages();
      if(id==="scope" && larkChatPickerActive("private"))void loadLarkChats("private");
    });

    for(const kind of ["private","groups"]){
      larkChatElement(kind,"picker-summary").addEventListener("click",event=>{if(larkChatElement(kind,"search").disabled)event.preventDefault();});
      larkChatElement(kind,"picker").addEventListener("toggle",()=>{
        if(larkChatElement(kind,"search").disabled)larkChatElement(kind,"picker").open=false;
        if(!larkChatElement(kind,"picker").open)stopLarkChatSearch(kind);
        else {queueLarkChatSearch(kind,false,0);void loadLarkChats(kind);}
      });
      $(`lark-${kind}-settings`).addEventListener("toggle",()=>{
        if(!$(`lark-${kind}-settings`).open){larkChatElement(kind,"picker").open=false;stopLarkChatSearch(kind);}
      });
      larkChatElement(kind,"search").addEventListener("compositionstart",()=>{if(workspaceUI.workspaceView)(workspaceUI.workspaceView.larkChatComposing ||= {})[kind]=true;queueLarkChatSearch(kind,true);});
      larkChatElement(kind,"search").addEventListener("compositionend",()=>{if(workspaceUI.workspaceView)(workspaceUI.workspaceView.larkChatComposing ||= {})[kind]=false;queueLarkChatSearch(kind);});
      larkChatElement(kind,"search").addEventListener("input",event=>queueLarkChatSearch(kind,event.isComposing || workspaceUI.workspaceView?.larkChatComposing?.[kind]));
      larkChatElement(kind,"more").addEventListener("click",()=>loadLarkChats(kind));
      larkChatElement(kind,"refresh").addEventListener("click",()=>loadLarkChats(kind,true));
    }

    $("lark-backfill-start").addEventListener("input",()=>{if(workspaceUI.workspaceView)workspaceUI.workspaceView.larkDirty=true;renderLarkMessages();});

    $("lark-save").addEventListener("click",()=>changeLarkMessages("configure"));

    $("lark-sync").addEventListener("click",()=>changeLarkMessages("sync"));

    $("lark-manage-platforms").addEventListener("click",async()=>{await settingsUI.openSettings("platforms");});

    $("gitlab-project-summary").addEventListener("click",event=>{if(!todoVerified(workspaceUI.workspaceView && todoPlatform(workspaceUI.workspaceView)) || gitlabSettingsBusy(workspaceUI.workspaceView))event.preventDefault();});

    $("gitlab-project-picker").addEventListener("toggle",()=>{if($("gitlab-project-picker").open && workspaceUI.workspaceView?.gitlabDraft && !gitlabProjectDirectory(workspaceUI.workspaceView).loaded)void loadGitLabProjects();});

    $("gitlab-project-search").addEventListener("input",()=>{
      const ws=workspaceUI.workspaceView;if(!ws?.gitlabDraft)return;
      const old=gitlabProjectDirectory(ws),search=$("gitlab-project-search").value.trim();
      if(search===old.search)return;
      clearTimeout(old.timer);
      const dir=ws.gitlabProjectDirectory={...old,search,projects:new Map(),loaded:false,loading:false,nextPage:0,error:""};
      dir.timer=setTimeout(()=>{dir.timer=null;if(ws===workspaceUI.workspaceView && gitlabProjectDirectory(ws)===dir)void loadGitLabProjects();},300);
      renderGitLabProjectPicker(ws);
    });

    $("gitlab-project-picker").addEventListener("keydown",event=>{if(event.key==="Escape" && $("gitlab-project-picker").open){event.preventDefault();$("gitlab-project-picker").open=false;$("gitlab-project-summary").focus();}});

    $("gitlab-project-more").addEventListener("click",()=>loadGitLabProjects());

    $("gitlab-project-refresh").addEventListener("click",()=>loadGitLabProjects(true));

    for(const prefix of ["notification-","member-notification-"]) {
      $(`${prefix}add-toggle`).addEventListener("click",()=>setNotificationPicker(prefix,$(`${prefix}add-picker`).hidden));
      $(`${prefix}add-search`).addEventListener("input",renderNotificationPlatforms);
      $(`${prefix}add-picker`).addEventListener("keydown",event=>{
        if(event.key!=="Escape")return;
        event.preventDefault();event.stopPropagation?.();setNotificationPicker(prefix,false);$(`${prefix}add-toggle`).focus();
      });
    }

    for (const id of ["gitlab-events-enabled", ...gitlabEventTypeIDs.map(id => `gitlab-events-type-${id}`)]) {
      for (const event of ["input", "change"]) $(id).addEventListener(event, editGitLabEvents);
    }

    $("gitlab-events-all-types").addEventListener("change", () => {
      for (const input of gitlabEventTypeInputs()) input.checked = $("gitlab-events-all-types").checked;
      editGitLabEvents();
    });

    for (const id of ["enabled", ...gitlabTodoReasons.map(value => `reason-${value}`), ...gitlabTodoTypes.map(value => `type-${value}`)]) {
      for (const event of ["input", "change"]) $(`gitlab-todos-${id}`).addEventListener(event, editGitLabTodos);
    }

    for (const [group, values] of [["reason", gitlabTodoReasons], ["type", gitlabTodoTypes]]) {
      const all = $(`gitlab-todos-all-${group === "reason" ? "reasons" : "types"}`);
      all.addEventListener("change", () => {
        for (const value of values) $(`gitlab-todos-${group}-${value}`).checked = all.checked;
        editGitLabTodos();
      });
    }

    for (const field of ["connection", "target", "interval"]) $(`gitlab-${field}`).addEventListener("change", () => editGitLabShared(field));

    $("gitlab-interval").addEventListener("input", () => editGitLabShared("interval"));

    $("gitlab-backfill").addEventListener("change",editGitLabBackfill);

    $("gitlab-backfill-start").addEventListener("change",editGitLabBackfill);

    $("gitlab-backfill-start").addEventListener("input",editGitLabBackfill);

    $("gitlab-save").addEventListener("click", () => changeGitLabSettings("configure"));

    $("gitlab-sync").addEventListener("click", () => changeGitLabSettings("sync"));

    $("close-notification").addEventListener("click", () => $("notification-detail").close());

    $("notification-detail").addEventListener("close",()=>{
      if($("notification-detail").open)return;
      notificationNavigation++;
      const ws=workspaceUI.workspaceView,back=ws?.notificationReturn;
      if(ws){ws.notificationOpen=(ws.notificationOpen || 0)+1;if(ws.gitlabSending==="select"){ws.gitlabRevision++;ws.gitlabSending=false;}}
      if(ws?.notificationAddition){
        ws.notificationDrafts?.delete(ws.notificationAddition.key);ws.gitlabDirty=false;ws.larkDirty=false;
        if(ws.notificationAddition.v)ws.notificationAddition.v.addition=null;
        if(ws.gitlab){ws.gitlabDraft=gitlabConfig(ws.gitlab.config);ws.gitlabBackfillEditing=false;fillBackfill("gitlab",ws.gitlab.config);}
        ws.larkFilled=null;ws.larkRestore=null;
      }else rememberNotificationDraft(ws);
      stopLarkChatSearch();
      $("activity-dialog").hidden=roomsUI.isDirect() && layoutUI.directNotificationsOpen;
      if(ws){ws.notificationReturn=null;ws.notificationAddition=null;}
      if(back && sameService(state.info,back.info) && membersUI.selectedAgent()?.membership_id===back.member.membership_id && $("activity-dialog").open){
        (notificationFocus?.isConnected ? notificationFocus : $(`agent-tab-${membersUI.agentTab}`)).focus({preventScroll:true});
        $("platforms-member-view").scrollTop=back.platformScroll;
        if(back.tab==="activity" && membersUI.agentTab==="activity")void membersUI.activity.select({...membersUI.selectedAgent(),info:state.info}).then(()=>{if($("activity-dialog").open && membersUI.selectedAgent()?.membership_id===back.member.membership_id && membersUI.agentTab==="activity")$("activity-body").scrollTop=back.bodyScroll;});
        $("activity-dialog").scrollTop=back.activityScroll;$("activity-body").scrollTop=back.bodyScroll;$("policy-member-view").scrollTop=back.policyScroll;
        $("inbox-member-view").scrollTop=back.scroll;
        window.scrollTo?.(back.windowScroll);
      }else if(!$("activity-dialog").open && !$("workspace-dialog").open)(notificationFocus?.isConnected ? notificationFocus : $("notification-add-toggle")).focus();
      notificationFocus=null;onboardingUI.renderMemberPlatformAddition();renderContextViews();
    });

    $("notification-detail").addEventListener("keydown", event => {
      if (event.key === "Escape") {
        if (event.defaultPrevented || event.target?.closest?.("select:open")) return;
        event.preventDefault(); $("notification-detail").close();
      }
    });

    $("notification-reload").addEventListener("click",reloadNotifications);

    $("notification-list-reload").addEventListener("click",reloadNotifications);

    $("member-notification-list-reload").addEventListener("click",async()=>{await reloadNotifications();if(membersUI.memberToolsView && membersUI.toolsCurrent(membersUI.memberToolsView))await membersUI.loadCLITools(membersUI.memberToolsView);});

    $("gitlab-manage-platforms").addEventListener("click", () => settingsUI.openSettings("platforms"));
  }

  return {
    mount,
    invalidateNavigation,
    currentSource,
    stopLarkChatSearch,
    syncGitLabNotifications,
    renderNotificationPlatforms,
    notificationPlatforms,
    notificationConfigured,
    notificationView,
    memberNotificationTarget,
    notificationRow,
    notificationDescription,
    notificationIssue,
    sameNotificationTarget,
    openNotificationSource,
    renderGitLabEvents,
    renderContextViews,
    openNotificationMessage,
    setNotificationPicker,
    notificationScopeKey,
    changeLarkMessages,
    changeGitLabSettings
  };
}
