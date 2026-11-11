// Durable platform onboarding and member platform additions.
export function createOnboarding({ env, runtime, connectionsUI, larkUI, settingsUI, roomsUI, workspaceUI, notificationsUI, membersUI, gitlabUI, scionUI }) {
  const { document, Option, request } = env;
  const { state, $, notice, refresh, setSelectOptions, storage } = runtime;

  // Authentication and settings use the same durable P5 operation. Only its ID
  // and non-secret prepare request are kept in this tab, never OAuth URLs/tokens.
  const onboarding = {active:false, operation:null, pending:null, members:[], fixed:null, busy:false, error:"", notices:new Set(),owner:null};

  function onboardingStorageKey() { return `teamx.platform-onboarding:${state.operator}`; }

  function onboardingDone(op = onboarding.operation) {
    return !!op && (op.cancelled || op.connection.status === "connected" && [op.notifications,op.tool].every(s => ["applied","preserved","skipped"].includes(s.status)));
  }

  function saveOnboarding() {
    try { if (onboarding.pending) storage?.setItem(onboardingStorageKey(),JSON.stringify(onboarding.pending)); else storage?.removeItem(onboardingStorageKey()); } catch { /* The current tab still retains the operation. */ }
  }

  function acceptOnboarding(op) {
    if(op.operation_id!==onboarding.operation?.operation_id)$("onboarding-result-details").open=false;
    onboarding.owner=state.operator;onboarding.operation=op; onboarding.error="";
    onboarding.pending={operation_id:op.operation_id}; saveOnboarding(); renderOnboarding();
  }

  function onboardingIdentity() { return {epoch:state.catalog?.service_epoch,operator:state.operator}; }

  function onboardingCurrent(version, identity) { return version===connectionsUI.platforms.formVersion && identity.epoch===state.catalog?.service_epoch && identity.operator===state.operator; }

  function onboardingRoom() { return state.catalog?.rooms.find(r=>r.room_instance===$("onboarding-room").value); }

  function onboardingMember() { return onboarding.members.find(m=>m.membership_id===$("onboarding-member").value); }

  function onboardingProvider() { return connectionsUI.connectionIsLark() ? "lark" : connectionsUI.connectionIsScion() ? "scion" : "gitlab"; }

  function onboardingOrigin() {
    if(connectionsUI.connectionIsLark())return larkUI.connectionOrigin();
    const value=$("connection-url").value.trim();
    try {
      const url=new URL(value);
      if(url.protocol!=="https:" || url.username || url.password || url.pathname!=="/" || value.includes("?") || value.includes("#") || /[\s\\]/.test(value))throw Error("invalid origin");
      return value.replace(url.host,url.host.toLowerCase()).replace(/\/$/,"");
    } catch {throw Error("请输入不含账号、密码、路径或查询参数的 HTTPS 实例地址。");}
  }

  function renderOnboarding() {
    const active=onboarding.active, fixed=!!onboarding.pending, only=$("onboarding-only").checked, provider=onboardingProvider();
    $("onboarding-options").hidden=!active || !onboarding.fixed || fixed && !!onboarding.operation?.input.only_platform;
    $("onboarding-targets").hidden=only || !!onboarding.fixed?.membership_id || onboarding.members.length===1;
    $("onboarding-room").parentElement?.classList.toggle("onboarding-room-fixed", !!onboarding.fixed);
    $("onboarding-member-field").hidden=!!onboarding.fixed?.membership_id || onboarding.members.length===1;
    $("onboarding-only-option").hidden=!onboarding.fixed;
    $("onboarding-enable-options").hidden=only;
    $("onboarding-notification-details").hidden=provider==="scion" || !$("onboarding-notifications").checked;
    $("onboarding-notifications").disabled=provider==="scion" || fixed;
    for (const id of ["onboarding-room","onboarding-member","onboarding-only","onboarding-tool","onboarding-backfill","onboarding-backfill-start"]) $(id).disabled=fixed || !!onboarding.fixed && (id==="onboarding-room" || id==="onboarding-member" && !!onboarding.fixed.membership_id);
    $("onboarding-backfill-custom").hidden=$("onboarding-backfill").value!=="custom";
    $("onboarding-backfill-start-label").textContent=`起始时间（${settingsUI.systemTime.zone || "系统时区异常"}）`;
    const member=onboardingMember(), room=onboardingRoom();
    const content=provider==="gitlab" ? "我的待办（项目互动未开启）" : "全部人员私聊（群聊未开启）";
    $("onboarding-summary").textContent=only ? "仅连接并核验账号；不创建通知或成员工具设置。" : `为「${member?.agent_id || "所选成员"}」接入后，${provider==="scion" ? "此平台没有通知能力，" : $("onboarding-notifications").checked ? `接收${content} → ${room?.name || "所选 Room"} 的成员 Inbox；` : "通知关闭；"}${$("onboarding-tool").checked ? "启用对应平台工具。" : "工具关闭。"}已有设置保留，范围可在通知详情中调整。`;
    if(active && fixed){$("connection-provider").disabled=true;$("connection-url").readOnly=true;}
    const op=onboarding.operation;
    const done=onboardingDone(op);
    const waitingForAccount=active && op?.connection.status==="waiting" && !op.cancelled && !onboarding.error && [op.notifications,op.tool].every(step=>["pending","skipped"].includes(step.status));
    const samePlatform=$("connection-form").hidden || (connectionsUI.platforms.selected ? connectionsUI.platforms.selected.id===op?.platform_id : active);
    $("onboarding-result").hidden=(!onboarding.pending && !op) || done && !onboarding.error || waitingForAccount || !samePlatform;
    $("onboarding-result-target").textContent=op ? op.input.only_platform ? "仅添加平台" : `${state.catalog?.rooms.find(r=>r.room_instance===op.input.room_instance)?.name || op.input.room_id} · ${op.input.agent_id}` : "正在恢复上次接入记录…";
    const item=connectionsUI.platforms.items.find(p=>p.id===op?.platform_id);
    const providerName={gitlab:"GitLab",lark:"飞书",scion:"Scion"}[op?.input.provider || onboarding.pending?.prepare?.provider] || "平台";
    const name=item?.name || providerName;
    const unfinished=op ? [["通知设置",op.notifications],["工具设置",op.tool]].filter(([,step])=>!["applied","preserved","skipped"].includes(step.status)).map(([label])=>label) : [];
    $("onboarding-result-summary").textContent=onboarding.busy ? `正在确认 ${name} 接入状态…` : onboarding.error || !op ? `${name} 接入结果待确认。` : op.connection.status!=="connected" ? `${name} 接入尚未完成。` : `${name} 已连接，${unfinished.join("、")}尚未完成。`;
    $("onboarding-result-account").textContent=op ? `${item?.name || "平台"}${item?.validation?.username ? ` · @${item.validation.username}` : ""}` : "";
    const status={waiting:"等待账号核验",connected:"账号已连接",pending:"等待应用",applied:"已保存",preserved:"保留原设置",skipped:"本次不修改",failed:"未完成"};
    const errors={account_unavailable:"请先完成登录或核验账号，再读取进度。",account_changed:"账号已变化，请停止本次接入并核对平台。",target_changed:"原 Room 或成员已变化，请重新选择目标发起接入。",settings_changed:"设置已被修改；请核对当前选择，再发起新的接入。",executable_unavailable:"程序缺失，启用意图已保存；安装后会重新检查。",legacy_tool_choice_unknown:"原工具选择未确认，已保留。请在成员工具设置中明确启用。",cancelled:"剩余步骤已停止，已保存的设置保留。",busy:"正在处理，请稍后读取进度。",binding_changed:"绑定已变化，请核对当前平台与成员。"};
    $("onboarding-steps").replaceChildren(...(op ? [["账号",op.connection],["通知",op.notifications],["工具",op.tool]].map(([title,step])=>{
      const row=document.createElement("div"),dt=document.createElement("dt"),dd=document.createElement("dd");dt.textContent=title;
      dd.textContent=`${status[step.status] || step.status}${step.error_code ? ` · ${errors[step.error_code] || "暂不可用，请核对平台、权限及当前设置后重试。"}` : ""}`;row.append(dt,dd);return row;
    }) : []));
    notice("onboarding-result-error",onboarding.error);
    $("onboarding-resume").hidden=done && !onboarding.error || !!op && !onboarding.error;
    $("onboarding-prepare-retry").hidden=!onboarding.pending?.prepare || !!op;
    $("onboarding-continue-login").hidden=!op || done || !!onboarding.error || op.connection.status==="connected";
    $("onboarding-apply").hidden=!op || done || !!onboarding.error || op.connection.status!=="connected";
    $("onboarding-cancel").hidden=!onboarding.pending || done;
    $("onboarding-notification-settings").hidden=!op || done || op.connection.status!=="connected" || op.input.only_platform || op.schema!==2 || !["gitlab","lark"].includes(op.input.provider);
    for (const id of ["onboarding-prepare-retry","onboarding-resume","onboarding-continue-login","onboarding-apply","onboarding-cancel","onboarding-notification-settings"]) $(id).disabled=onboarding.busy || connectionsUI.platforms.sending || !!connectionsUI.platforms.gitlabAuth || !!connectionsUI.platforms.auth || !!connectionsUI.platforms.scionAuth;
  }

  async function loadOnboardingMembers(preferred = "") {
    const version=connectionsUI.platforms.formVersion, identity=onboardingIdentity(), room=onboardingRoom();
    onboarding.members=[];setSelectOptions("onboarding-member",[new Option("正在读取成员…","")],"");
    notice("onboarding-target-error","");$("onboarding-target-retry").hidden=true;
    if (!room) { setSelectOptions("onboarding-member",[new Option("先选择 Room","")],"");renderOnboarding();return; }
    try {
      const q=new URLSearchParams({service_epoch:identity.epoch,caller_id:identity.operator,room_id:room.room_id,room_instance:room.room_instance});
      const data=await request(`/v1/platforms/onboarding/targets?${q}`);
      if (!onboardingCurrent(version,identity) || room.room_instance!==onboardingRoom()?.room_instance) return;
      onboarding.members=data.members;
      const options=data.members.map(m=>new Option(room.members?.find(r=>r.id===m.agent_id)?.name || m.agent_id,m.membership_id));
      const selected=data.members.some(m=>m.membership_id===preferred) ? preferred : data.members.length===1 && !preferred ? data.members[0].membership_id : "";
      if (!selected) options.unshift(new Option(data.members.length ? "选择一次使用成员" : "此 Room 没有适用的 Agent",""));
      setSelectOptions("onboarding-member",options,selected);
      if (preferred && !selected) notice("onboarding-target-error","原成员已变化，请重新打开成员详情。");
    } catch { if (!onboardingCurrent(version,identity) || room.room_instance!==onboardingRoom()?.room_instance) return;
      notice("onboarding-target-error","成员读取失败，请重试。也可在详情选择仅添加平台。");$("onboarding-target-retry").hidden=false;
    }
    renderOnboarding();
  }

  async function startOnboardingOptions(fixed = null) {
    onboarding.active=true;onboarding.fixed=fixed;onboarding.notices=new Set();
    const rooms=(state.catalog?.rooms || []).filter(r=>r.status==="ready" && !r.busy);
    const preferred=fixed?.room_instance || roomsUI.selectedRoom?.room_instance;
    const selected=rooms.find(r=>r.room_instance===preferred)?.room_instance || (!fixed && rooms.length===1 ? rooms[0].room_instance : "");
    const options=rooms.map(r=>new Option(r.name,r.room_instance));if (!selected) options.unshift(new Option("选择使用的 Room",""));
    setSelectOptions("onboarding-room",options,selected);
    $("onboarding-only").checked=!fixed;$("onboarding-details").open=false;
    $("onboarding-notifications").checked=true;$("onboarding-tool").checked=true;$("onboarding-backfill").value="0";settingsUI.fillTimeInput("onboarding-backfill-start","");
    renderOnboarding();
    if (fixed) await loadOnboardingMembers(fixed.membership_id || "");
  }

  async function prepareOnboarding() {
    if (!onboarding.active) return null;
    if (onboarding.pending) {
      if (onboarding.operation) {if(onboarding.operation.cancelled)throw Error("此接入已停止，请新建接入。");return onboarding.operation;}
      if (!onboarding.pending.prepare) throw Error("请先读取接入进度。");
    }
    const only=$("onboarding-only").checked, member=onboardingMember(), room=onboardingRoom();
    if (!only && (!member || !room || onboarding.fixed && (onboarding.fixed.membership_id && member.membership_id!==onboarding.fixed.membership_id || room.room_instance!==onboarding.fixed.room_instance))) {
      notice("onboarding-target-error","请选择有效的 Room 和使用成员，再连接并启用。");throw Error("请选择使用成员。");
    }
    let spec={provider:onboardingProvider(),origin:onboardingOrigin(),...(connectionsUI.platforms.selected ? {platform_id:connectionsUI.platforms.selected.id} : {}),...(only ? {only_platform:true} : {room_id:room.room_id,room_instance:room.room_instance,agent_id:member.agent_id,membership_id:member.membership_id})};
    if (!only) {
      spec.preserve_existing=true;
      for (const name of ["notifications","tool"]) if (onboarding.notices.has(name) || !$("onboarding-"+name).checked) spec[name]=$("onboarding-"+name).checked;
      if (onboarding.notices.has("backfill") && spec.provider!=="scion") {
        const mode=$("onboarding-backfill").value;
        const backfill={backfill_seconds:mode==="custom" ? 0 : Number(mode),...(mode==="custom" ? {backfill_start:settingsUI.readTimeInput("onboarding-backfill-start")} : {})};
        if (mode==="custom" && !backfill.backfill_start) throw Error("请选择有效的开始接收时间。");
        spec.backfill={seconds:backfill.backfill_seconds,...(backfill.backfill_start ? {start:backfill.backfill_start} : {})};
      }
    }
    const pending=onboarding.pending || {operation_id:crypto.randomUUID().replaceAll("-",""),prepare:spec};
    onboarding.owner=state.operator;onboarding.pending=pending;saveOnboarding();renderOnboarding();
    const version=connectionsUI.platforms.formVersion,identity=onboardingIdentity();
    let op;
    try {op=await request("/v1/platforms/onboarding/prepare",connectionsUI.connectionPayload({operation_id:pending.operation_id,...pending.prepare}));}
    catch(error) {if(onboardingCurrent(version,identity) && ["request","identity"].includes(error.code)) {onboarding.pending=null;saveOnboarding();renderOnboarding();}throw error;}
    if (!onboardingCurrent(version,identity)) throw Error("页面目标已变化，请读取接入进度。");
    acceptOnboarding(op);return op;
  }

  async function onboardingAction(action, {discardUnconnected = false} = {}) {
    if (!onboarding.pending || onboarding.busy || connectionsUI.platforms.sending || connectionsUI.platforms.gitlabAuth || connectionsUI.platforms.auth || connectionsUI.platforms.scionAuth) return;
    const version=connectionsUI.platforms.formVersion,identity=onboardingIdentity(),id=onboarding.pending.operation_id;
    onboarding.busy=true;onboarding.error="";renderOnboarding();
    try {
      const op=await request(`/v1/platforms/onboarding/${action}`,connectionsUI.connectionPayload({operation_id:id}),undefined,50000);
      if (!onboardingCurrent(version,identity) || id!==onboarding.pending?.operation_id) return;
      acceptOnboarding(op);
      // Leaving an account login also abandons its unused setup record. Read the
      // server receipt first so a late login or partial apply remains recoverable.
      if (discardUnconnected && !op.cancelled && op.connection.status==="waiting" &&
          !connectionsUI.platforms.items.some(item=>item.id===op.platform_id) &&
          [op.notifications,op.tool].every(step=>["pending","skipped"].includes(step.status))) {
        const cancelled=await request("/v1/platforms/onboarding/cancel",connectionsUI.connectionPayload({operation_id:id}));
        if (!onboardingCurrent(version,identity) || id!==onboarding.pending?.operation_id) return;
        acceptOnboarding(cancelled);
        if (cancelled.cancelled && cancelled.connection.status==="waiting") clearOnboarding();
      }
      if (action==="apply") {await connectionsUI.loadPlatforms();if(workspaceUI.workspaceView)await notificationsUI.syncGitLabNotifications(state.catalog,workspaceUI.workspaceView);}
      if(action!=="status" && onboardingDone(op))connectionsUI.connectionFeedback(op.cancelled ? "已停止接入。" : op.input.only_platform ? "已连接。" : "已完成接入。","success");
    } catch(error) { if (onboardingCurrent(version,identity)) onboarding.error=error.code==="identity" ? "接入记录或操作人不匹配，请核对当前服务与账号。" : "接入结果未确认；请读取进度后再继续，不会重新登录或重放远端动作。"; }
    finally {onboarding.busy=false;renderOnboarding();}
  }

  function restoreOnboarding() {
    if(onboarding.owner!==state.operator){onboarding.pending=null;onboarding.operation=null;onboarding.active=false;onboarding.owner=state.operator;}
    if (!onboarding.pending) {try {const p=JSON.parse(storage?.getItem(onboardingStorageKey()));if (/^[a-f0-9]{32}$/.test(p?.operation_id)) onboarding.pending=p;}catch { /* Ignore an invalid tab record. */ }}
    renderOnboarding();if(onboarding.pending) return onboardingAction("status",{discardUnconnected:true});
  }

  function clearOnboarding() {onboarding.pending=null;onboarding.operation=null;onboarding.active=false;onboarding.error="";$("onboarding-result-details").open=false;saveOnboarding();renderOnboarding();}

  async function addPlatformForMember(fixed = null) {
    await settingsUI.openSettings("platforms");
    if (onboarding.pending && !onboardingDone()) {onboarding.error="请先继续上次接入，或停止其剩余步骤后重新添加。";renderOnboarding();return;}
    clearOnboarding();await connectionsUI.editPlatform();await startOnboardingOptions(fixed);
  }

  function memberPlatformAddition(v,item) {
    return {item,notifications:["gitlab","lark"].includes(item.provider),tool:!!v?.cli?.catalog?.some(d=>d.provider===item.provider)};
  }

  function memberPlatformAdditionBusy(v,item) {
    return !v || !membersUI.toolsCurrent(v) || !v.cliConnected || !workspaceUI.workspaceView?.notificationsConnected || onboarding.busy || !!workspaceUI.workspaceView?.memberToolBusy || item.validation?.status!=="valid" || !!onboarding.pending && !onboardingDone();
  }

  async function openMemberPlatformAddition(opener,item) {
    const ws=workspaceUI.workspaceView,v=membersUI.memberToolsView;if(memberPlatformAdditionBusy(v,item))return;
    const a=v.addition=memberPlatformAddition(v,item),key=notificationsUI.notificationScopeKey(notificationsUI.memberNotificationTarget(v.member),item.id);
    $("member-platform-add-notifications").checked=a.notifications;$("member-platform-add-tool").checked=a.tool;
    ws.notificationAddition={key,v};
    if(a.notifications)$(`${item.provider}-advanced-settings`).open=true;
    await membersUI.openMemberPlatform(opener,item,notificationsUI.memberNotificationTarget(v.member));
    if(ws!==workspaceUI.workspaceView || !membersUI.toolsCurrent(v) || a!==v.addition || ws.notificationAddition?.key!==key || !$("notification-detail").open)return;
    if(item.provider==="gitlab" && ws.gitlabConnected && !ws.gitlab?.version){
      ws.gitlabDraft.enabled=true;ws.gitlabDraft.todos.enabled=true;ws.gitlabDirty=true;
    }else if(item.provider==="lark" && ws.messagesConnected && !ws.messages?.subscription_id){
      $("lark-enabled").checked=true;ws.larkDirty=true;
    }
    notificationsUI.renderGitLabEvents();
  }

  function renderMemberPlatformAddition() {
    const ws=workspaceUI.workspaceView,v=membersUI.memberToolsView,op=onboarding.operation;
    const a=ws?.notificationAddition?.v===v && ws.notificationAddition.key===ws.notificationScope ? v?.addition : null;
    const own=op && op.input.room_instance===ws?.room.room_instance && op.input.agent_id===v?.member.agent_id && op.input.membership_id===v?.member.membership_id;
    $("member-platform-add-selection").hidden=!a;$("member-platform-add-actions").hidden=!a;
    for(const provider of ["gitlab","lark"]){
      $(`${provider}-save`).hidden=!!a;
      $(`${provider}-sync-status`).hidden=!!a;
      $(`${provider}-feedback`).hidden=!!a;
    }
    if(a){
      const notifications=["gitlab","lark"].includes(a.item.provider),tool=!!v.cli?.catalog?.some(d=>d.provider===a.item.provider);
      $("notification-title").textContent=`添加 ${a.item.name}`;
      $("notification-enabled-state").hidden=true;
      if(!notifications)$("notification-notice").hidden=true;
      $("member-platform-add-notifications").disabled=!notifications || onboarding.busy;
      $("member-platform-add-tool").disabled=!tool || onboarding.busy;
      $("member-platform-add-cancel").disabled=onboarding.busy;
      if(a.error)notice("notification-notice",a.error);
      if(!a.notifications){$("notification-panel-messages").hidden=true;$("notification-panel-gitlab").hidden=true;}
      const invalid=a.notifications && (a.item.provider==="lark" ? $("lark-save").disabled : $("gitlab-save").disabled);
      $("member-platform-add-submit").disabled=memberPlatformAdditionBusy(v,a.item) || !!invalid;
    }
    const status={applied:"已保存",preserved:"保留原设置",skipped:"本次不修改",pending:"等待应用",failed:"未完成"};
    $("member-platform-add-result").textContent=own ? `通知：${status[op.notifications.status]}；工具：${status[op.tool.status]}。${onboarding.error || (op.notifications.status==="failed" || op.tool.status==="failed" ? "已成功部分保留，请读取进度后继续未完成步骤。" : "")}` : onboarding.pending && !onboardingDone() ? `有未完成的接入，请读取进度后继续。${onboarding.error}` : onboarding.error;
    $("member-platform-add-resume").hidden=!onboarding.pending || onboardingDone();
    const canApply=own && (op.connection.status==="connected" || op.connection.status==="waiting" && membersUI.memberPlatformItems().some(p=>p.id===op.platform_id && p.validation?.status==="valid"));
    $("member-platform-add-retry").hidden=!onboarding.pending || onboardingDone() || !!op && !canApply;
    $("member-platform-add-resume").disabled=$("member-platform-add-retry").disabled=onboarding.busy;
  }

  async function runMemberOnboarding(v,spec) {
    if(!membersUI.toolsCurrent(v) || !v.cliConnected || workspaceUI.workspaceView?.memberToolBusy || onboarding.busy)return;
    if(onboarding.pending && !onboardingDone()){onboarding.error="请先读取并完成上次接入，或在全局设置停止剩余步骤。";renderMemberPlatformAddition();return;}
    clearOnboarding();
    const identity=onboardingIdentity(),ws=workspaceUI.workspaceView;
    onboarding.pending={operation_id:crypto.randomUUID().replaceAll("-",""),prepare:spec};onboarding.owner=state.operator;saveOnboarding();onboarding.busy=true;membersUI.renderMemberPlatforms();
    const id=onboarding.pending.operation_id;let preparing=true;
    const current=()=>identity.epoch===state.catalog?.service_epoch && identity.operator===state.operator && onboarding.pending?.operation_id===id;
    try {
      let op=await request("/v1/platforms/onboarding/prepare",{service_epoch:identity.epoch,caller:{kind:"human",id:identity.operator},operation_id:id,...spec});
      if(!current())return;acceptOnboarding(op);preparing=false;
      op=await request("/v1/platforms/onboarding/apply",{service_epoch:identity.epoch,caller:{kind:"human",id:identity.operator},operation_id:id},undefined,50000);
      if(!current())return;acceptOnboarding(op);
      if(op.notifications.status==="applied"){
        const key=notificationsUI.notificationScopeKey({mode:"inbox",agent_id:spec.agent_id,membership_id:spec.membership_id},spec.platform_id);
        ws.notificationDrafts?.delete(key);
        if(ws.notificationScope===key){ws.gitlabDirty=false;ws.larkDirty=false;ws.larkFilled=null;}
      }
      if(onboardingDone(op) && membersUI.toolsCurrent(v))v.addition=null;
    }catch(error){if(current()){
      if(preparing && ["request","identity"].includes(error.code)){onboarding.pending=null;saveOnboarding();onboarding.error="接入未准备，请核对成员、平台与通知范围后重试。";}
      else onboarding.error="接入结果未确认，请读取进度；不会自动重发。";
    }}
    finally{
      onboarding.busy=false;
      if(ws===workspaceUI.workspaceView && identity.epoch===state.catalog?.service_epoch){await notificationsUI.syncGitLabNotifications(state.catalog,ws);if(membersUI.memberToolsView && membersUI.toolsCurrent(membersUI.memberToolsView))await membersUI.loadCLITools(membersUI.memberToolsView);notificationsUI.renderGitLabEvents();}
    }
  }

  async function initializeMemberNotifications(item,target,enabled=true) {
    const v=membersUI.memberToolsView;
    if(!v || !membersUI.toolsCurrent(v) || !notificationsUI.sameNotificationTarget(notificationsUI.memberNotificationTarget(v.member),target))return;
    return runMemberOnboarding(v,{...workspaceUI.workspaceView.room,agent_id:target.agent_id,membership_id:target.membership_id,platform_id:item.id,provider:item.provider,origin:item.url,notifications:enabled,skip_tool:true});
  }

  async function addMemberPlatform(v,a) {
    if(!a || memberPlatformAdditionBusy(v,a.item))return;
    const ws=workspaceUI.workspaceView,addition=ws.notificationAddition,custom=addition?.v===v;
    const spec={...ws.room,agent_id:v.member.agent_id,membership_id:v.member.membership_id,platform_id:a.item.id,provider:a.item.provider,origin:a.item.url,preserve_existing:true,notifications:a.notifications,tool:a.tool};
    if(a.config && a.notifications)spec[a.item.provider]=a.config;
    a.error="";
    await runMemberOnboarding(v,spec);
    if(custom && ws===workspaceUI.workspaceView && membersUI.toolsCurrent(v) && ws.notificationAddition===addition){
      if(onboarding.pending)$("notification-detail").close();
      else {a.error=onboarding.error;notificationsUI.renderGitLabEvents();}
    }
    if(onboardingDone() && membersUI.toolsCurrent(v))notificationsUI.setNotificationPicker("member-notification-",false);
  }

  async function captureMemberPlatformDraft(config) {
    const ws=workspaceUI.workspaceView,a=ws?.notificationAddition,v=membersUI.memberToolsView;
    if(!a || a.key!==ws.notificationScope || a.v!==v || !membersUI.toolsCurrent(v) || !v.addition || v.addition.item.id!==config.platform_id)return false;
    const addition=v.addition;addition.config=structuredClone(config);
    await addMemberPlatform(v,addition);return true;
  }

  function mount() {
    $("notification-add-platform").addEventListener("click",()=> { notificationsUI.setNotificationPicker("member-notification-",false); const member=membersUI.selectedAgent();if(!member || !workspaceUI.workspaceView)return; return addPlatformForMember({...workspaceUI.workspaceView.room,agent_id:member.agent_id,membership_id:member.membership_id}); });

    $("onboarding-room").addEventListener("change",()=>loadOnboardingMembers());

    $("onboarding-target-retry").addEventListener("click",()=>loadOnboardingMembers(onboarding.fixed?.membership_id || ""));

    $("onboarding-member").addEventListener("change",renderOnboarding);

    for (const name of ["only","notifications","tool","backfill","backfill-start"]) $("onboarding-"+name).addEventListener("change",()=>{onboarding.notices.add(name.startsWith("backfill") ? "backfill" : name);renderOnboarding();if(onboardingProvider()==="gitlab")gitlabUI.renderGitLabAccount();else if(connectionsUI.connectionIsLark())larkUI.renderLarkAccount();});

    for (const [id,action] of [["onboarding-resume","status"],["onboarding-apply","apply"],["onboarding-cancel","cancel"]]) $(id).addEventListener("click",()=>onboardingAction(action));

    $("onboarding-continue-login").addEventListener("click",async()=>{
      const op=onboarding.operation;if(!op || op.cancelled)return;
      const item=connectionsUI.platforms.items.find(p=>p.id===op.platform_id);
      await connectionsUI.editPlatform(item || null);
      if(onboarding.operation!==op)return;
      if(!item){$("connection-provider").value=op.input.provider==="lark" ? op.input.origin.includes("larksuite") ? "lark" : "feishu" : op.input.provider;$("connection-url").value=op.input.origin;connectionsUI.connectionAuthFields();}
      onboarding.active=true;onboarding.fixed=op.input;
      setSelectOptions("onboarding-room",[new Option(state.catalog?.rooms.find(r=>r.room_instance===op.input.room_instance)?.name || op.input.room_id || "仅添加平台",op.input.room_instance || "")],op.input.room_instance || "");
      onboarding.members=op.input.only_platform ? [] : [op.input];setSelectOptions("onboarding-member",[new Option(op.input.agent_id || "",op.input.membership_id || "")],op.input.membership_id || "");
      $("onboarding-only").checked=!!op.input.only_platform;$("onboarding-notifications").checked=op.input.notifications!==false;$("onboarding-tool").checked=op.input.tool!==false;
      renderOnboarding();
      if(connectionsUI.connectionIsLark())await larkUI.detectLarkPlatform();else if(connectionsUI.connectionIsScion())await scionUI.detectScionPlatform();else await gitlabUI.discoverGitLabSetup();
    });

    $("onboarding-prepare-retry").addEventListener("click",async()=>{
      if(onboarding.busy || !onboarding.pending?.prepare)return;
      onboarding.busy=true;onboarding.active=true;renderOnboarding();
      try {await prepareOnboarding();}catch(error){onboarding.error=error.code ? "准备结果未确认，请读取进度后再继续。" : error.message;}
      finally{onboarding.busy=false;renderOnboarding();}
    });

    $("onboarding-notification-settings").addEventListener("click",async()=>{
      const op=onboarding.operation,room=state.catalog?.rooms.find(r=>r.room_id===op?.input.room_id && r.room_instance===op?.input.room_instance);
      if(!room || op.schema!==2 || op.input.only_platform){onboarding.error="接入结果缺少有效的成员定位，请检查原 Room 和成员。";renderOnboarding();return;}
      await settingsUI.closeSettings();
      if(state.info?.room_id!==room.room_id || state.info?.room_instance!==room.room_instance){roomsUI.selectRoom(room);await refresh();}
      if(state.info?.room_instance!==room.room_instance)return;
      const member=state.members.find(m=>m.agent_id===op.input.agent_id && m.membership_id===op.input.membership_id);
      if(!member){await settingsUI.openSettings("platforms");onboarding.error="原接收成员已移除或替换，账号连接已保留。";renderOnboarding();return;}
      if(!$("activity-dialog").open || membersUI.selectedAgent()?.membership_id!==member.membership_id)membersUI.openMemberActivity(member,$("onboarding-notification-settings"));
      await notificationsUI.openNotificationSource($("member-notification-add-toggle"),op.input.provider==="lark" ? "messages" : "gitlab",op.platform_id,notificationsUI.memberNotificationTarget(member));
    });

    for(const [id,key] of [["member-platform-add-notifications","notifications"],["member-platform-add-tool","tool"]])$(id).addEventListener("change",()=>{
      if(membersUI.memberToolsView?.addition)membersUI.memberToolsView.addition[key]=$(id).checked;notificationsUI.renderGitLabEvents();
    });

    $("member-platform-add-cancel").addEventListener("click",()=>$("notification-detail").close());

    $("member-platform-add-submit").addEventListener("click",()=>{
      const v=membersUI.memberToolsView,a=v?.addition;if(!a || $("member-platform-add-submit").disabled)return;
      if(a.notifications)return a.item.provider==="lark" ? notificationsUI.changeLarkMessages("configure") : notificationsUI.changeGitLabSettings("configure");
      return addMemberPlatform(v,a);
    });

    $("member-platform-add-resume").addEventListener("click",async()=>{await onboardingAction("status");renderMemberPlatformAddition();});

    $("member-platform-add-retry").addEventListener("click",async()=>{
      if(!onboarding.operation && onboarding.pending?.prepare){
        if(onboarding.busy)return;
        const pending=onboarding.pending,identity=onboardingIdentity();onboarding.busy=true;renderMemberPlatformAddition();
        const current=()=>onboarding.pending===pending && state.catalog?.service_epoch===identity.epoch && state.operator===identity.operator;
        try{const op=await request("/v1/platforms/onboarding/prepare",connectionsUI.connectionPayload({...pending.prepare,operation_id:pending.operation_id}));if(current())acceptOnboarding(op);}catch{if(current())onboarding.error="准备结果未确认，请读取进度。";}finally{onboarding.busy=false;renderMemberPlatformAddition();}return;
      }
      await onboardingAction("apply");if(membersUI.memberToolsView && membersUI.toolsCurrent(membersUI.memberToolsView))await membersUI.loadCLITools(membersUI.memberToolsView);if(workspaceUI.workspaceView)await workspaceUI.loadWorkspacePlatforms();renderMemberPlatformAddition();
    });

    $("inbox-add-platform").addEventListener("click",async()=>{
      const ws=workspaceUI.workspaceView,target=notificationsUI.memberNotificationTarget();if(!ws || !target)return;
      await membersUI.selectAgentTab("platforms");
      if(ws!==workspaceUI.workspaceView || !notificationsUI.sameNotificationTarget(target,notificationsUI.memberNotificationTarget()))return;
      membersUI.renderMemberPlatforms();notificationsUI.setNotificationPicker("member-notification-",true);
    });
  }

  return {
    mount,
    onboarding,
    memberPlatformAdditionBusy,
    openMemberPlatformAddition,
    addMemberPlatform,
    memberPlatformAddition,
    renderMemberPlatformAddition,
    captureMemberPlatformDraft,
    initializeMemberNotifications,
    restoreOnboarding,
    prepareOnboarding,
    renderOnboarding,
    onboardingIdentity,
    onboardingCurrent,
    acceptOnboarding,
    onboardingAction,
    onboardingDone,
    clearOnboarding,
    startOnboardingOptions
  };
}
