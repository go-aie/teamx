import { createActivityReader, sameService } from "../client.mjs";
import { createAssistantIcon } from "../icons.mjs";

// Member inspection, private activity, CLI tools and platform summaries.
export function createMembers({ env, runtime, notificationsUI, workspaceUI, onboardingUI, inboxUI, roomsUI, layoutUI, policyUI, conversationUI }) {
  const { document, window, Option, ResizeObserver, request } = env;
  const { state, $, notice, rpc, currentView, refresh, discussionBusy } = runtime;

  const memberBusy = new Set();

  let memberToolsView = null;

  // Tool views are scoped to their Room/operator/epoch and member generation.
  // Late reads and mutations cannot populate another Room or Agent's settings.
  function toolsCurrent(v) {
    return v === memberToolsView &&
      (!v.member || $("activity-dialog").open && activityMember?.membership_id === v.member.membership_id &&
        state.members.some(m => m.agent_id === v.member.agent_id && m.membership_id === v.member.membership_id)) &&
      state.catalog?.service_epoch === v.identity.service_epoch && v.runtimeEpoch===state.info?.service_epoch &&
      state.operator === v.identity.caller.id && state.info?.room_id===v.identity.room_id && state.info?.room_instance === v.identity.room_instance;
  }

  function toolsIdentity() {
    return { service_epoch: state.catalog.service_epoch, room_id: state.info.room_id,
      room_instance: state.info.room_instance, caller: { kind: "human", id: state.operator } };
  }

  async function openMemberTools(member) {
    if (!state.managed || !state.info || !state.catalogConnected) {
      notificationsUI.renderNotificationPlatforms();return;
    }
    const v = memberToolsView = { identity: toolsIdentity(), runtimeEpoch:state.info.service_epoch, member, cli: null, cliConnected:false };
    await loadCLITools(v);
  }

  // CLI bindings have a separate revision and never inherit legacy grants.
  async function loadCLITools(v) {
    const read = v.cliRead = (v.cliRead || 0) + 1;
    try {
      const q = new URLSearchParams({ service_epoch: v.identity.service_epoch, caller_id: v.identity.caller.id,
        room_id: v.identity.room_id, room_instance: v.identity.room_instance });
      const cq = new URLSearchParams({ service_epoch: v.identity.service_epoch, caller_id: v.identity.caller.id });
      const [data, platforms] = await Promise.all([request(`/v1/tools/bindings?${q}`), request(`/v1/platforms?${cq}`)]);
      if (!toolsCurrent(v) || read !== v.cliRead) return;
      v.cli = data;v.cliConnected=true; v.cliPlatforms = platforms.platforms; if(workspaceUI.workspaceView)workspaceUI.workspaceView.allPlatforms=platforms.platforms; v.cliResult = null;
      renderMemberTools(v);
    } catch {
      if (!toolsCurrent(v) || read !== v.cliRead) return;
      v.cliConnected=false;
      renderMemberTools(v);
    }
  }

  function toolBinding(v, item) {
    const name=typeof item==="string" ? item : v?.cli?.catalog?.find(d=>d.provider===item.provider)?.name;
    return v?.cli?.bindings?.find(b=>b.agent_id===v.member.agent_id && b.membership_id===v.member.membership_id && b.tool===name && (typeof item==="string" ? !b.platform_id : b.platform_id===item.id));
  }

  function toolIssue(v,item) {
    const selection=v?.cli?.selections?.find(s=>s.agent_id===v.member.agent_id && s.membership_id===v.member.membership_id && s.platform_id===item.id);
    const unavailable=toolBinding(v,item) && selection?.unavailable_reason ? ({executable_unavailable:"程序缺失，已保留启用设置；修复后会重新检查。",account_unavailable:"账号暂不可用，已保留启用设置，请重新核验。",platform_changed:"平台已变化，需重新启用。"}[selection.unavailable_reason] || "工具暂不可用，已保留启用设置。") : "";
    return [v?.cliResult?.tool===item.id ? v.cliResult.message : "",unavailable].filter(Boolean).join(" ");
  }

  function toolSwitch(v,item) {
    const label=document.createElement("label"),input=document.createElement("input"),text=document.createElement("span");
    const d=typeof item==="string" ? v?.cli?.catalog?.find(d=>d.name===item) : v?.cli?.catalog?.find(d=>d.provider===item.provider);
    const key=typeof item==="string" ? item : item.id,b=toolBinding(v,item);
    label.className="member-platform-tool workspace-check";input.type="checkbox";input.className="notification-switch";input.setAttribute("role","switch");
    input.setAttribute("aria-label",`${typeof item==="string" ? item : item.name} 使用工具`);
    input.checked=v?.cliPending?.tool===key ? v.cliPending.enabled : !!b;
    input.disabled=!v?.cli || !v.cliConnected || !d || !!workspaceUI.workspaceView?.memberToolBusy || onboardingUI.onboarding.busy || !state.connected || !state.catalogConnected || discussionBusy() || (!b && typeof item!=="string" && item.validation?.status!=="valid");
    text.textContent="工具";label.append(input,text);
    input.addEventListener("change",()=>changeMemberTool(v,item,input.checked));return label;
  }

  async function changeMemberTool(v,item,enabled) {
    const ws=workspaceUI.workspaceView;if(!toolsCurrent(v) || !v.cliConnected || ws.memberToolBusy || onboardingUI.onboarding.busy)return;
    const d=typeof item==="string" ? v.cli.catalog.find(d=>d.name===item) : v.cli.catalog.find(d=>d.provider===item.provider);
    if(!d || enabled && typeof item!=="string" && item.validation?.status!=="valid")return;
    const key=typeof item==="string" ? item : item.id,b=toolBinding(v,item);
    const binding={agent_id:v.member.agent_id,membership_id:v.member.membership_id,tool:d.name,...(typeof item!=="string" ? {platform_id:item.id} : {})};
    if(enabled && d.provider==="lark")binding.action_authorization={version:1,revision:b?.action_authorization?.revision || 1,grants:(d.actions || []).filter(s=>!s.disabled_reason).flatMap(s=>(s.object_types || []).map(object_type=>({operation:s.operation,target_kind:s.target_kind,object_type})))};
    else if(enabled && b?.action_authorization)binding.action_authorization=structuredClone(b.action_authorization);
    ws.memberToolBusy=true;v.cliPending={tool:key,enabled};v.cliResult=null;v.cliRead=(v.cliRead || 0)+1;renderMemberTools(v);
    let message="";
    try {
      const next=await request("/v1/tools/bind",{...v.identity,revision:v.cli.revision,binding,enabled,platform_version:enabled && typeof item!=="string" ? item.version : 0},undefined,binding.action_authorization?.grants.some(g=>d.actions?.some(s=>s.operation===g.operation && s.binding_evidence)) ? 40000 : 10000);
      if(toolsCurrent(v))v.cli={...v.cli,...next};
      message=`${typeof item==="string" ? item : item.name} 工具${enabled ? "已启用" : "已停用"}。`;
    } catch(error) {
      message=["conflict","identity","request","capacity"].includes(error.code) ? "保存失败，请核对当前成员、平台与权限后重试。" : "保存结果未确认，请核对读回状态；不会自动重发。";
    } finally {
      ws.memberToolBusy=false;v.cliPending=null;
      // A read resolves unknown local writes; no bind request is replayed.
      if(toolsCurrent(v)){await loadCLITools(v);if(!v.cliConnected)message+=" 当前状态暂不可读，请重新读取。";v.cliResult={tool:key,message,error:!v.cliConnected || /失败|未确认/.test(message)};renderMemberTools(v);}
      else if(memberToolsView?.cli && toolsCurrent(memberToolsView))await loadCLITools(memberToolsView);
    }
  }

  function renderMemberTools(v) {
    if(!toolsCurrent(v))return;
    notificationsUI.renderNotificationPlatforms();
  }

  function memberPlatformItems(ws=workspaceUI.workspaceView,v=memberToolsView) {
    const items=new Map((ws?.allPlatforms || notificationsUI.notificationPlatforms(ws)).map(p=>[p.id,p]));
    for(const view of [...(ws?.gitlabPlatforms || []),...(ws?.larkSources || [])]){
      const id=view.platform_id || view.config.platform_id;
      if(id && !items.has(id))items.set(id,{id,name:"原平台暂不可用",provider:view.provider || "gitlab",validation:{status:"unavailable"}});
    }
    // Keep unavailable and explicitly disabled bindings inspectable.
    for(const b of [...(v?.cli?.bindings || []),...(v?.cli?.disabled || [])]) {
      if(b.agent_id!==v.member.agent_id || b.membership_id!==v.member.membership_id || !b.platform_id || items.has(b.platform_id))continue;
      const d=v.cli.catalog.find(d=>d.name===b.tool);items.set(b.platform_id,{id:b.platform_id,name:"原平台暂不可用",provider:d?.provider,validation:{status:"unavailable"}});
    }
    return [...items.values()];
  }

  function memberHasPlatform(item,target,v=memberToolsView) {
    return notificationsUI.notificationConfigured(item,notificationsUI.notificationView(workspaceUI.workspaceView,item,target)) || [...(v?.cli?.bindings || []),...(v?.cli?.disabled || [])].some(b=>b.agent_id===target.agent_id && b.membership_id===target.membership_id && b.platform_id===item.id);
  }

  function layoutMemberPlatforms() {
    const list=$("member-notification-platforms"),width=list.getBoundingClientRect().width;
    if(!width)return; // The inspector or this tab may be hidden until it is opened.
    list.classList.add("member-platform-measuring");
    const required=Math.max(0,...Array.from(list.children,row=>
      row.children[0].getBoundingClientRect().width + row.children[1].getBoundingClientRect().width +
      (Number.parseFloat(window.getComputedStyle(row).columnGap) || 0)
    ));
    list.classList.remove("member-platform-measuring");
    list.dataset.stacked=String(required>width);
  }

  function renderMemberPlatforms() {
    const ws=workspaceUI.workspaceView,target=notificationsUI.memberNotificationTarget(),v=memberToolsView;
    $("member-notification-sources").hidden=!state.managed || !ws || !target;
    if(!ws || !target)return;
    const items=memberPlatformItems(ws,v),configured=items.filter(item=>memberHasPlatform(item,target,v));
    $("member-notification-platforms").replaceChildren(...configured.map(item=>{
      const view=notificationsUI.notificationView(ws,item,target),hasNotifications=["gitlab","lark"].includes(item.provider);
      const row=notificationsUI.notificationRow(ws,item,target,true);
      row.className+=" member-platform-row";
      row.children[0].setAttribute("aria-label",hasNotifications ? `编辑 ${item.name} 的通知设置` : `${item.name} 平台详情`);
      const heading=row.children[0].children[1].children[0],name=document.createElement("span"),edit=document.createElementNS("http://www.w3.org/2000/svg","svg"),path=document.createElementNS("http://www.w3.org/2000/svg","path");
      name.className="member-platform-name";name.textContent=item.name;
      edit.setAttribute("class","platform-edit-icon");edit.setAttribute("viewBox","0 0 24 24");edit.setAttribute("aria-hidden","true");edit.setAttribute("focusable","false");
      path.setAttribute("d","m15 5 4 4M4 20l4-1L20 7a2.8 2.8 0 0 0-4-4L4 15Z");edit.append(path);heading.replaceChildren(name,edit);
      row.children[0].children[1].children[1].textContent=[hasNotifications ? notificationsUI.notificationConfigured(item,view) ? notificationsUI.notificationDescription(view.config) : "尚未开启通知" : "无通知能力",item.validation?.username ? `@${item.validation.username}` : "账号待核验"].join(" · ");
      row.children[0].title=`${item.name} · ${row.children[0].children[1].children[1].textContent}`;
      const input=row.children[1],label=document.createElement("label");input.id=`member-platform-notify-${item.id}`;label.htmlFor=input.id;label.className="member-platform-notify-label";label.textContent=hasNotifications ? "通知" : "无通知能力";
      input.hidden=!hasNotifications;if(!notificationsUI.notificationConfigured(item,view) && onboardingUI.onboarding.busy)input.disabled=true;label.hidden=!hasNotifications;
      row.children[2].textContent=[notificationsUI.notificationConfigured(item,view) ? notificationsUI.notificationIssue(ws,item,view) : "",toolIssue(v,item)].filter(Boolean).join(" ");row.children[2].hidden=!row.children[2].textContent;row.children[2].dataset.kind=v?.cliResult?.tool===item.id && !v.cliResult.error ? "success" : "error";
      const info=row.children[0],issue=row.children[2],controls=document.createElement("div");controls.className="member-platform-controls";
      controls.append(label,input,toolSwitch(v,item));row.replaceChildren(info,controls,issue);return row;
    }));
    layoutMemberPlatforms();
    $("member-notification-source-count").textContent=String(configured.length);
    $("member-notification-empty").hidden=!!configured.length || !$("member-notification-add-picker").hidden;
    $("member-notification-empty").textContent="添加平台，接收通知或使用工具。";
    const issues=configured.filter(item=>{
      const view=notificationsUI.notificationView(ws,item,target);
      const availability=v?.cli?.selections?.find(s=>s.agent_id===target.agent_id && s.membership_id===target.membership_id && s.platform_id===item.id);
      return notificationsUI.notificationConfigured(item,view) && notificationsUI.notificationIssue(ws,item,view) || !!availability?.unavailable_reason || v?.cliResult?.tool===item.id && v.cliResult.error;
    });
    $("member-platform-issues").replaceChildren(...issues.map(item=>{
      const button=document.createElement("button");button.type="button";button.className="text-button";button.textContent=`${item.name}：查看异常`;button.addEventListener("click",()=>openMemberPlatform(button,item,target));return button;
    }));
    notice("member-notification-list-error",!ws.notificationsConnected || !v?.cliConnected ? "部分设置暂不可读，请重新读取。" : "");$("member-notification-list-reload").hidden=!!ws.notificationsConnected && !!v?.cliConnected;
    const q=$("member-notification-add-search").value.trim().toLowerCase();
    $("member-notification-add-options").replaceChildren(...items.filter(item=>!configured.includes(item) && [item.name,item.validation?.username,item.url].join(" ").toLowerCase().includes(q)).map(item=>{
      const row=document.createElement("div"),info=document.createElement("div"),title=document.createElement("strong"),account=document.createElement("small"),actions=document.createElement("div");
      row.className="notification-add-option member-platform-add-option";row.dataset.platformId=item.id;row.dataset.provider=item.provider;
      info.className="member-platform-add-info";title.textContent=item.name;account.textContent=item.validation?.username ? `@${item.validation.username}` : "账号待核验";info.append(title,account);
      actions.className="member-platform-add-buttons";
      for(const [action,label] of [["settings","设置"],["add","添加并启用"]]){
        const button=document.createElement("button");button.type="button";button.className=action==="add" ? "primary" : "text-button";button.textContent=label;button.dataset.action=action;
        button.setAttribute("aria-label",`${label} ${item.name}${item.validation?.username ? ` @${item.validation.username}` : ""}`);
        button.disabled=onboardingUI.memberPlatformAdditionBusy(v,item);
        button.addEventListener("click",()=>action==="settings" ? onboardingUI.openMemberPlatformAddition(button,item) : onboardingUI.addMemberPlatform(v,onboardingUI.memberPlatformAddition(v,item)));
        actions.append(button);
      }
      row.append(info,actions);return row;
    }));
    onboardingUI.renderMemberPlatformAddition();
    const subscriptions=configured.filter(item=>notificationsUI.notificationConfigured(item,notificationsUI.notificationView(ws,item,target)));
    $("inbox-empty-hint").hidden=!inboxUI.inboxView?.page || !!inboxUI.inboxView.page.entries.length;
    $("inbox-empty-hint").textContent=inboxUI.inboxView?.status?.expired_through && !inboxUI.inboxView?.page?.entries.length ? "当前没有保留的通知，旧通知已过期清理。" : !ws.notificationsConnected ? "通知状态暂不可读，请查看平台设置。" : !subscriptions.length ? "添加平台，开始接收通知。" : issues.length ? "尚未收到通知，请查看平台异常提示。" : subscriptions.some(item=>notificationsUI.notificationView(ws,item,target).config.enabled) ? "暂未收到通知。" : "通知已暂停。";
    $("inbox-add-platform").hidden=!!subscriptions.length;
    const inboxIssues = configured.filter(item => notificationsUI.notificationConfigured(item,notificationsUI.notificationView(ws,item,target)) && notificationsUI.notificationIssue(ws,item,notificationsUI.notificationView(ws,item,target)));
    $("inbox-platform-issues").replaceChildren(...inboxIssues.map(item=>{const b=document.createElement("button");b.type="button";b.className="text-button";b.textContent=`${item.name}：${notificationsUI.notificationIssue(ws,item,notificationsUI.notificationView(ws,item,target))} 查看设置`;b.addEventListener("click",()=>openMemberPlatform(b,item,target));return b;}));
    const pending = [...(ws.allSubscriptions || []),...(ws.retiredSources || [])].filter(source=>notificationsUI.sameNotificationTarget(source.target || source.config?.target,target)).reduce((n,source)=>n+(source.pending || 0),0);
    $("inbox-backlog").textContent = !ws.notificationsConnected ? "来源状态暂不可读，可到平台设置重试读取。" : pending ? `来源积压 ${pending} 条等待投递；容量释放后会自动重试，凭据和绑定异常需在平台设置处理。` : "";
  }

  async function openMemberPlatform(opener,item,target=notificationsUI.memberNotificationTarget()) {
    return notificationsUI.openNotificationSource(opener,item.provider==="lark" ? "messages" : item.provider==="gitlab" ? "gitlab" : "tools",item.id,target);
  }

  let activitySignature = "";

  let activityTimelineContext = "";

  let activityScrollMember = "";

  let activityFollowing = true, activityScrollTop = 0;

  const activityTimelineRows = new Map();

  let activityFocus = null;

  let activityMember = null;

  let agentTab = "activity";

  const activity = createActivityReader((member) => rpc("/v1/member/activity", {
    agent_id: member.agent_id, membership_id: member.membership_id,
  }, member.info), renderActivity);

  const statusNames = { paused: "已暂停", idle: "等待消息", waiting: "等待执行", running: "运行中", completed: "已完成",
    failed: "执行失败", interrupted: "已中断", unavailable: "暂不可用", detached: "未连接",
    budget_exhausted: "预算用尽", checkpoint_failed: "检查点失败", backend_blocked: "需重置会话",
    rotating: "轮换中", closing: "清理中", merging: "合并通知" };

  function memberRole(member) {
    return roomsUI.selectedEntry()?.members?.find(role => role.id === member.agent_id);
  }

  function openMemberActivity(member, opener, tab = state.managed ? "platforms" : "activity") {
    notice("notification-next-step", "");
    notificationsUI.invalidateNavigation();
    if (!state.connected || !state.info) return;
    const current = state.members.find(item => item.agent_id === member.agent_id && item.membership_id === member.membership_id);
    if (!current) return;
    activityFocus = { element: opener, membership: current.membership_id };
    $("notification-detail").close();
    $("activity-dialog").hidden=false;
    activityMember = { agent_id: current.agent_id, membership_id: current.membership_id };
    layoutUI.expandMembers();
    layoutUI.hideConversationNotifications(); layoutUI.savePanels(); layoutUI.renderPanels();
    if (!$("activity-dialog").open) $("activity-dialog").show();
    memberToolsView = null;
    renderActivityControls();
    notificationsUI.renderNotificationPlatforms();
    if(state.managed){void openMemberTools(current);void workspaceUI.loadWorkspacePlatforms();void onboardingUI.restoreOnboarding()?.then(()=>renderMemberPlatforms());}
    void selectAgentTab(tab);
    if (window.innerWidth <= 640) {
      $("activity-dialog").scrollIntoView({ block: "start" });
      $("close-activity").focus({preventScroll:true});
    }
  }

  function renderActivityControls() {
    const panel = $("activity-dialog");
    if (panel.open && !state.members.some(member => member.membership_id === activityMember?.membership_id)) panel.close();
    notificationsUI.renderContextViews();
    $("close-activity").hidden = roomsUI.isDirect() && window.innerWidth > 640;
    $("close-activity").setAttribute("aria-label", roomsUI.isDirect() ? "返回私信" : "返回团队成员");
    $("close-activity").title = roomsUI.isDirect() ? "返回私信" : "返回团队成员";
    const selected=selectedAgent();
    $("activity-title").textContent=selected ? memberRole(selected)?.name || selected.agent_id : "Agent 详情";
    $("agent-tabs").hidden = false;
    $("agent-tab-platforms").hidden = !state.managed;
    const member = selectedAgent();
    if (!member || !panel.open) return;
    $("agent-status").textContent = member.execution?.active ? "运行中" : statusNames[member.status] || member.status;
    $("agent-status").classList.toggle("running", !!member.execution?.active);
    $("agent-status").classList.toggle("failed", /failed|blocked|unavailable|interrupted/.test(member.status));
    renderMemberActions(member, $("agent-actions"));
    const execution = member.execution;
    const reset = execution?.session_reset_required || member.status === "backend_blocked";
    const budget = execution?.turn_budget;
    const exhausted = !execution?.session_turns && budget && budget.used >= budget.limit;
    const effects = [];
    if (reset) effects.push("重置会话，清除私有上下文");
    if (exhausted) effects.push(`追加 ${budget.grant || 16} 回合`);
    notice("agent-enable-hint", effects.length ? `启用协作将${effects.join("，并")}。` : "");
    notice("agent-error", member.execution?.error || "");
    if (memberToolsView?.cli) {
      const signature = JSON.stringify([state.connected, state.catalogConnected, discussionBusy()]);
      if (memberToolsView.availability !== signature) {
        memberToolsView.availability = signature; renderMemberTools(memberToolsView);
      }
    }
  }

  function selectedAgent() {
    return state.members.find(m => m.agent_id === activityMember?.agent_id && m.membership_id === activityMember?.membership_id);
  }

  async function selectAgentTab(tab) {
    notificationsUI.invalidateNavigation();
    const member = selectedAgent();
    if (!$("activity-dialog").open || !member) return;
    if(agentTab==="inbox" && inboxUI.inboxCurrent(inboxUI.inboxView))inboxUI.inboxView.scrollTop=$("inbox-member-view").scrollTop;
    agentTab = tab === "platforms" && state.managed ? "platforms" : tab === "policy" ? "policy" : tab === "inbox" ? "inbox" : "activity";
    for (const key of ["platforms", "policy", "inbox", "activity"]) {
      const button = $(`agent-tab-${key}`);
      button.setAttribute("aria-selected", String(agentTab === key));
      button.setAttribute("tabindex", agentTab === key ? "0" : "-1");
    }
    $("activity-body").hidden = $("activity-footer").hidden = agentTab !== "activity";
    $("platforms-member-view").hidden = agentTab !== "platforms";
    $("inbox-member-view").hidden = agentTab !== "inbox";
    $("policy-member-view").hidden = agentTab !== "policy";
    if (agentTab === "activity") await activity.select({ ...member, info: state.info });
    else if (agentTab === "inbox") {
      activity.close();notificationsUI.renderNotificationPlatforms();void workspaceUI.loadWorkspacePlatforms();
      if(inboxUI.inboxView?.page && !inboxUI.inboxView.loading && sameService(inboxUI.inboxView.info,state.info) && inboxUI.inboxView.member.agent_id===member.agent_id && inboxUI.inboxView.member.membership_id===member.membership_id)$("inbox-member-view").scrollTop=inboxUI.inboxView.scrollTop || 0;
      else await inboxUI.loadMemberInbox();
    }
    else if (agentTab === "platforms") { activity.close();notificationsUI.renderNotificationPlatforms(); }
    else if (agentTab === "policy") { activity.close(); await policyUI.loadMemberPolicy(true); }
  }

  function renderMemberActions(member, actions) {
    const execution = member.execution;
    const signature = JSON.stringify([member.agent_id, member.membership_id, member.status, execution?.paused, execution?.active,
      execution?.session_reset_required, state.connected, discussionBusy(), memberBusy.has(member.membership_id), member.unread_count, execution?.agent?.turn?.status]);
    if (actions.dataset.signature === signature) return;
    actions.dataset.signature = signature;
    actions.replaceChildren();
    if (!execution) return;
    const enable = execution.paused || execution.session_reset_required || member.status === "backend_blocked" || member.status === "budget_exhausted";
    const add = (label, action, cancel) => {
      const button = document.createElement("button"); button.type = "button"; button.textContent = label;
      button.setAttribute("aria-label", `${label} ${member.agent_id}`);
      button.disabled = memberBusy.has(member.membership_id) || !state.connected || discussionBusy() || member.status === "checkpoint_failed" || member.status === "rotating" && action !== "pause";
      button.addEventListener("click", () => controlMember(member, action, cancel)); actions.append(button);
    };
    add(enable ? "启用协作" : "暂停", enable ? "run" : "pause", false);
    if (execution.active) add("取消回合", "pause", true);
  }

  function renderExecutionHint() {
    const available = state.connected && state.info && !discussionBusy();
    const running = available ? state.members.filter(member => member.execution?.active || member.status === "running") : [];
    const waiting = available ? state.members.filter(member => member.status === "waiting" && !member.execution?.active && !member.execution?.paused) : [];
    const names = members => members.map(member => memberRole(member)?.name || member.agent_id).join("、");
    // Active covers Session opening, model work and tools; it does not identify a thinking phase.
    const text = running.length ? `${names(running)} 正在处理…` : waiting.length ? `${names(waiting)} 正在等待执行…` : "";
    const queue = running.length && waiting.length ? `${names(waiting)} 等待执行` : "";
    const hint = $("execution-hint");
    const primary = running.length ? running : waiting;
    const phaseText = running.length ? "正在处理…" : "正在等待执行…";
    const signature = JSON.stringify([text, queue, primary.map(member => member.membership_id)]);
    if (hint.dataset.signature === signature) return;
    const follow = conversationUI.nearBottom();
    hint.dataset.signature = signature;
    hint.hidden = !text;
    hint.dataset.phase = running.length ? "running" : "waiting";
    const content = $("execution-text");
    content.replaceChildren();
    for (const [index, member] of primary.entries()) {
      if (index) {
        const separator = document.createElement("span"); separator.textContent = "、"; content.append(separator);
      }
      const button = document.createElement("button");
      const name = memberRole(member)?.name || member.agent_id;
      button.type = "button"; button.className = "execution-member";
      button.textContent = primary.length === 1 ? `${name} ${phaseText}` : name;
      button.dataset.activityMember = member.membership_id;
      button.setAttribute("aria-label", `查看详情 ${name}`);
      button.title = `查看 ${name} 的详情`;
      button.addEventListener("click", () => openMemberActivity(member, button, "activity"));
      content.append(button);
    }
    if (primary.length > 1) {
      const phase = document.createElement("span"); phase.textContent = ` ${phaseText}`; content.append(phase);
    }
    $("execution-queue").textContent = queue;
    $("execution-queue").hidden = !queue;
    hint.title = [text, queue].filter(Boolean).join(" · ");
    $("timeline-wrap").classList.toggle("has-activity", !!text);
    if (follow) $("timeline").scrollTop = $("timeline").scrollHeight;
  }

  function syncDirectDetail() {
    if (!roomsUI.isDirect() || !state.connected || !layoutUI.membersPanelOpen() || layoutUI.directNotificationsOpen || $("notification-detail").open || state.members.length !== 1) return;
    if ($("activity-dialog").open && selectedAgent()?.membership_id === state.members[0].membership_id) return;
    openMemberActivity(state.members[0], $("toggle-members"));
  }

  function renderMembers() {
    syncDirectDetail();
    renderActivityControls();
    renderExecutionHint();
    $("member-count").textContent = state.members.length;
    $("member-empty").hidden = state.members.length > 0;
    // Keep focus and controls stable when polling reports no visible changes.
    const signature = JSON.stringify(state.members) + [...memberBusy].join(",") + state.connected + discussionBusy();
    if ($("members").dataset.signature === signature) return;
    $("members").dataset.signature = signature;
    const fragment = document.createDocumentFragment();
    for (const member of state.members) {
      const el = $("member-template").content.firstElementChild.cloneNode(true);
      const execution = member.execution;
      const running = execution?.active;
      const label = running ? "运行中" : statusNames[member.status] || member.status;
      const role = memberRole(member);
      el.querySelector(".member-name").textContent = role?.name || member.agent_id;
      const summary = el.querySelector(".member-summary");
      summary.dataset.activityMember = member.membership_id;
      summary.setAttribute("aria-label", `查看 ${role?.name || member.agent_id} 的详情`);
      summary.disabled = !state.connected;
      summary.addEventListener("click", () => openMemberActivity(member, summary));
      el.querySelector(".avatar").textContent = member.agent_id.slice(0, 2).toUpperCase();
      if (roomsUI.isDirect()) el.querySelector(".avatar").replaceChildren(createAssistantIcon(document));
      el.querySelector(".member-profile").textContent = role ? member.agent_id : member.runner.profile;
      const status = el.querySelector(".member-status");
      status.textContent = label;
      status.classList.toggle("running", !!running);
      status.classList.toggle("failed", /failed|blocked|unavailable|interrupted/.test(member.status));
      const budget = execution?.turn_budget;
      el.querySelector(".member-details").textContent = `${member.unread_count} 条未读 · 已读至 #${member.read_through}` +
        (execution?.session_turns ? `\n${execution.session_turns.baseline_known ? `本会话 ${execution.session_turns.used} / ${execution.session_turns.limit}` : "待接入轮换"} · 累计 ${execution.session_turns.total}` : budget ? `\n回合 ${budget.used} / ${budget.limit}` : "");
      if (execution?.error) {
        const error = el.querySelector(".member-error");
        error.hidden = false;
        error.textContent = execution.error;
      }
      fragment.append(el);
    }
    $("members").replaceChildren(fragment);
  }

  function renderProfiles(profiles) {
    if (JSON.stringify(profiles) === JSON.stringify(state.profiles) && $("profile").options.length) return;
    state.profiles = profiles;
    const selected = $("profile").value;
    $("profile").replaceChildren(...profiles.map((name) => new Option(name, name)));
    if (profiles.includes(selected)) $("profile").value = selected;
    $("add-member").disabled = profiles.length === 0 || discussionBusy();
    notice("profile-hint", profiles.length ? "新成员默认暂停，添加后点击「启用协作」。" : "还没有可用角色。请在启动服务时通过 --dsh-config 指定配置文件。");
  }

  function renderActivity({ loading, data, error }, selected) {
    const signature = JSON.stringify({ loading, data, error: error?.message, selected: selected.membership_id });
    if (signature === activitySignature) return;
    activitySignature = signature;
    const scrollMember = JSON.stringify([selected.info, selected.membership_id]);
    if (activityScrollMember !== scrollMember) {
      activityFollowing = true; activityScrollTop = 0;
    } else if (!$("activity-output").hidden) {
      activityFollowing = activityNearBottom(); activityScrollTop = $("activity-body").scrollTop;
    }
    const follow = activityFollowing, scrollTop = activityScrollTop;
    activityScrollMember = scrollMember;
    $("activity-body").setAttribute("aria-busy", String(!!loading));
    $("activity-state").hidden = !data;
    $("activity-timeline-empty").hidden = true;
    $("activity-timeline-truncated").hidden = true;
    if (loading || error) {
      $("activity-output").hidden = true;
      $("activity-jump-latest").hidden = true;
      notice("activity-notice", loading ? "正在读取私有活动…" : `私有活动读取失败，将自动重试。${error.message}`);
      return;
    }
    const member = data.member, execution = member.execution;
    $("activity-status").textContent = execution?.active ? "运行中" : statusNames[member.status] || member.status;
    $("activity-progress").textContent = `已读至 #${member.read_through} · ${member.unread_count} 条未读`;
    const budget = execution?.turn_budget;
    const session = execution?.session_turns;
    $("activity-budget").textContent = session ? `${session.baseline_known ? `${session.used} / ${session.limit}` : "待接入轮换"} · 累计 ${session.total}` : budget ? `${budget.used} / ${budget.limit}` : "暂不可用";
    const rotation = execution?.rotation;
    const rotationReasons = { session_max_turns: "达到会话回合阈值", manual: "手动轮换" };
    notice("activity-rotation", rotation ? `${rotationReasons[rotation.reason] || rotation.reason} · ${rotation.phase}${rotation.error ? ` · ${rotation.error}` : ""}` : "");
    const turn = execution?.agent?.turn;
    $("activity-turn").textContent = execution?.agent ? `${execution.agent.turn_number}` +
      (turn ? ` · ${statusNames[turn.status] || turn.status}` : " · 尚未运行") : "暂不可用";
    notice("activity-execution-error", execution?.error || "");
    const live = !!data.current_output, output = data.current_output || data.last_output;
    if (!output) {
      $("activity-output").hidden = true;
      $("activity-jump-latest").hidden = true;
      notice("activity-notice", data.output_error || (execution?.active ? "回合已启动，等待模型或工具活动…" : "暂无私有活动。"));
      return;
    }
    notice("activity-notice", "");
    $("activity-output").hidden = false;
    const result = output.result;
    $("activity-output-title").textContent = live ? "当前回合 · 运行中" : "最近完成的回合";
    const reasons = { end_turn: "正常结束", cancelled: "已取消", max_tokens: "达到 token 上限", refusal: "拒绝执行" };
    $("activity-stop").textContent = live ? (result.stopReason ? "正在结清" : "运行中") : reasons[result.stopReason] || result.stopReason || "结束原因未记录";
    notice("activity-output-error", output.error || "");
    if (!Array.isArray(result.events)) {
      $("activity-output").hidden = true;
      $("activity-jump-latest").hidden = true;
      notice("activity-notice", "活动记录格式无效，无法显示。");
      return;
    }
    renderActivityTimeline(result, JSON.stringify([selected.info, selected.membership_id, execution?.agent?.turn_number]));
    $("activity-timeline").hidden = false;
    notice("activity-timeline-empty", result.events.length ? "" : live ? "等待模型或工具活动…" : "该回合没有活动记录。");
    $("activity-timeline-truncated").hidden = !result.eventsTruncated;
    restoreActivityScroll(follow, scrollTop);
  }

  function activityNearBottom() {
    const body = $("activity-body");
    return body.scrollHeight - body.scrollTop - body.clientHeight < 90;
  }

  function restoreActivityScroll(follow, top) {
    const body = $("activity-body");
    body.scrollTop = follow ? body.scrollHeight : top;
    activityScrollTop = body.scrollTop; activityFollowing = follow || activityNearBottom();
    $("activity-jump-latest").hidden = activityFollowing;
  }

  function renderActivityTimeline(result, context) {
    const timeline = $("activity-timeline");
    if (activityTimelineContext !== context) {
      activityTimelineContext = context;
      activityTimelineRows.clear(); timeline.replaceChildren();
    }
    const retained = new Set(result.events.map(event => event.seq));
    // Only retention eviction removes existing nodes. Ordinary refreshes update
    // content in place, preserving focus, scroll and both open and closed rows.
    if ([...activityTimelineRows.keys()].some(seq => !retained.has(seq))) {
      for (const seq of activityTimelineRows.keys()) if (!retained.has(seq)) activityTimelineRows.delete(seq);
      timeline.replaceChildren(...[...activityTimelineRows.values()].map(row => row.details));
    }
    const statuses = { pending: "等待中", in_progress: "执行中", completed: "已完成", failed: "失败" };
    for (const event of result.events) {
      let row = activityTimelineRows.get(event.seq);
      if (!row) {
        const details = document.createElement("details"), summary = document.createElement("summary");
        const title = document.createElement("span"), status = document.createElement("span"), body = document.createElement("div");
        details.className = "activity-group"; details.dataset.seq = String(event.seq); details.open = event.kind === "text";
        status.className = "activity-event-status"; body.className = "activity-group-content";
        summary.append(title, status); details.append(summary, body);
        const text = document.createElement("div"), inputLabel = document.createElement("p"), input = document.createElement("pre");
        const outputLabel = document.createElement("p"), output = document.createElement("pre"), truncated = document.createElement("p");
        text.className = "activity-text"; inputLabel.className = outputLabel.className = truncated.className = "activity-caption";
        inputLabel.textContent = "参数"; outputLabel.textContent = "结果";
        body.append(text, inputLabel, input, outputLabel, output, truncated);
        row = { details, title, status, text, inputLabel, input, outputLabel, output, truncated };
        activityTimelineRows.set(event.seq, row); timeline.append(details);
      }
      const tool = event.tool;
      row.title.textContent = tool ? `工具 · ${tool.title || tool.id || "未知调用"}` : event.kind === "thought" ? "思考" : "内部回复";
      row.status.textContent = tool ? statuses[tool.status] || tool.status || "状态未记录" : "";
      row.status.hidden = !tool;
      row.text.textContent = event.text || ""; row.text.hidden = !!tool;
      row.inputLabel.hidden = row.input.hidden = !tool?.input;
      row.outputLabel.hidden = row.output.hidden = !tool?.output;
      row.input.textContent = tool?.input || ""; row.output.textContent = tool?.output || "";
      row.truncated.textContent = tool ? "调用内容已截断" : "内容超出保留上限，文本已截断。";
      row.truncated.hidden = !(event.textTruncated || tool?.detailsTruncated);
    }
  }

  function closeAgentDetail() {
    if (roomsUI.isDirect()) {
      layoutUI.collapseMembers();
    }
    $("activity-dialog").close(); layoutUI.renderPanels();
  }

  async function controlMember(member, action, cancel) {
    const info = state.info, version = runtime.viewVersion;
    memberBusy.add(member.membership_id); renderMembers();
    try {
      const identity = { agent_id: member.agent_id, membership_id: member.membership_id };
      const failedUnread = member.unread_count > 0 && ["failed", "interrupted"].includes(member.execution?.agent?.turn?.status);
      await rpc(`/v1/member/${action}`, { ...identity, cancel, retry: action === "run" && failedUnread }, info);
      await refresh();
    } catch (error) { if (currentView(version)) notice("sync-error", `${member.agent_id} 操作未确认，请查看成员状态。${error.message}`); }
    finally { memberBusy.delete(member.membership_id); renderMembers(); }
  }

  function resetTools() { memberToolsView = null; }

  function resetActivity() {
    activity.close(); $("activity-dialog").close();
    activitySignature = ""; activityMember = null;
  }

  function mount() {
    if(typeof ResizeObserver!=="undefined")new ResizeObserver(layoutMemberPlatforms).observe($("member-notification-platforms"));

    document.fonts?.ready.then(layoutMemberPlatforms);

    for (const key of ["platforms", "policy", "inbox", "activity"]) {
      const button = $(`agent-tab-${key}`);
      button.addEventListener("click", () => selectAgentTab(key));
      button.addEventListener("keydown", event => {
        if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
        event.preventDefault();
        const keys = state.managed ? ["platforms", "policy", "inbox", "activity"] : ["policy", "inbox", "activity"];
        const current = keys.indexOf(key);
        const next = event.key === "Home" ? keys[0] : event.key === "End" ? keys.at(-1) : keys[(current + (event.key === "ArrowLeft" ? keys.length - 1 : 1)) % keys.length];
        $(`agent-tab-${next}`).focus(); void selectAgentTab(next);
      });
    }

    $("activity-body").addEventListener("scroll", () => {
      // A failed read temporarily hides output and clamps the browser's scrollTop.
      // Keep the reading position until a successful snapshot restores it.
      if ($("activity-output").hidden) return;
      activityScrollTop = $("activity-body").scrollTop; activityFollowing = activityNearBottom();
      $("activity-jump-latest").hidden = activityFollowing;
    });

    $("activity-jump-latest").addEventListener("click", () => restoreActivityScroll(true, 0));

    $("close-activity").addEventListener("click", closeAgentDetail);

    $("activity-dialog").addEventListener("close", () => {
      if ($("activity-dialog").open) return;
      activity.close(); memberToolsView = null; activityMember = null; agentTab = "activity"; activitySignature = "";
      inboxUI.reset();
      activityScrollMember = ""; activityFollowing = true; activityScrollTop = 0; $("activity-jump-latest").hidden = true;
      activityTimelineContext = ""; activityTimelineRows.clear(); $("activity-timeline").replaceChildren();
      renderActivityControls();
      const opener = !layoutUI.membersExpanded ? null : activityFocus?.element?.isConnected ? activityFocus.element
        : [...document.querySelectorAll("[data-activity-member]")].find(button => button.dataset.activityMember === activityFocus?.membership);
      if (!$("notification-detail").open) (opener || $("toggle-members")).focus();
      activityFocus = null;
    });

    $("activity-dialog").addEventListener("keydown", (event) => {
      if (event.key === "Escape") { event.preventDefault(); closeAgentDetail(); }
    });

    $("toggle-add").addEventListener("click", () => {
      $("add-form").hidden = !$("add-form").hidden;
      $("toggle-add").setAttribute("aria-expanded", String(!$("add-form").hidden));
      if (!$("add-form").hidden) $("agent-name").focus();
    });

    $("add-form").addEventListener("submit", async (event) => {
      event.preventDefault();
      const agent = $("agent-name").value.trim(), profile = $("profile").value;
      if (!agent || !profile || $("add-member").disabled) return;
      const info = state.info, version = runtime.viewVersion;
      $("add-member").disabled = true; notice("add-error", "");
      try {
        await rpc("/v1/member/add", { agent_id: agent, runner: { kind: "dsh", profile } }, info);
        if (!currentView(version)) return;
        $("agent-name").value = ""; $("add-form").hidden = true;
        $("toggle-add").setAttribute("aria-expanded", "false");
        await refresh();
      } catch (error) { if (currentView(version)) notice("add-error", `添加未确认，请先查看成员列表；相同名称可以重试。${error.message}`); }
      finally { conversationUI.composerState(); }
    });
  }

  return {
    mount,
    resetTools,
    resetActivity,
    get memberToolsView() { return memberToolsView; },
    selectedAgent,
    openMemberTools,
    openMemberPlatform,
    renderMemberPlatforms,
    memberPlatformItems,
    memberRole,
    get activityMember() { return activityMember; },
    get agentTab() { return agentTab; },
    activity,
    toolsCurrent,
    loadCLITools,
    renderMembers,
    syncDirectDetail,
    openMemberActivity,
    statusNames,
    selectAgentTab,
    renderExecutionHint,
    renderProfiles
  };
}
