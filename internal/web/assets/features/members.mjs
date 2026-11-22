import {imageUploadStatus} from "./lark-capabilities.mjs";
import { agentName } from "../agent-names.mjs";
import { compactViewport } from "../client.mjs";
import { createActivityReader, sameService } from "../client.mjs";
import { renderAvatar } from "../avatars.mjs";
import { createNavigationIcon } from "../icons.mjs";
import { captureReturnView } from "../view-return.mjs";
import { beginRead, readState } from "../read-state.mjs";
import { isSessionRecovering as recovering, memberExecutionFailed as runtimeFailed, memberExecutionMessage } from "../member-execution.mjs";

// Member inspection, private activity, CLI tools and platform summaries.
export function createMembers({ env, runtime, notificationsUI, workspaceUI, onboardingUI, inboxUI, roomsUI, layoutUI, policyUI, conversationUI, connectionsUI, settingsUI, userMenuUI }) {
  const { document, window, Option, ResizeObserver, request } = env;
  const { state, $, notice, rpc, currentView, refresh, discussionBusy, storage } = runtime;

  const executionMessage = member => memberExecutionMessage(member, settingsUI.formatDate);
  const memberBusy = new Set();
  const stopRequests = new Map();
  const memberListKey = "teamx.member-list-expanded";
  let memberListExpanded = false;
  try { memberListExpanded = JSON.parse(storage?.getItem(memberListKey)) === true; } catch { /* Start with compact avatars. */ }

  function renderMemberListToggle() {
    const button = $("toggle-member-list"), label = `${memberListExpanded ? "收起" : "展开"}成员列表`;
    button.setAttribute("aria-expanded", String(memberListExpanded));
    button.setAttribute("aria-label", label); button.title = label;
    $("members").classList.toggle("members-compact", !roomsUI.isDirect() && !memberListExpanded);
  }

  let nameEditor=null,nameSaving=false,nameEditing=false;
  let memberToolsView = null;
  let memberToolsGeneration = 0;
  let memberModelStatus = {};

  function renderDiagnostics(prefix, detail) {
    $(prefix + "-diagnostics").hidden = !detail;
    $(prefix + "-diagnostic-error").textContent = detail || "";
  }

  function renderMemberProfile() {
    const member = selectedAgent();
    const modelError = memberModelStatus.state === "error", pending = memberModelStatus.state === "busy";
    const alert = $("agent-model-alert");
    alert.textContent = modelError ? "模型异常" : runtimeFailed(member) ? "运行异常" : pending ? "切换中" : "";
    alert.hidden = !alert.textContent;
    alert.dataset.state = modelError || runtimeFailed(member) ? "error" : pending ? "busy" : "ready";
    alert.title = memberModelStatus.detail || "";
    const actions = $("agent-actions");
    actions.hidden = !member?.execution;
    if (!actions.hidden) renderMemberActions(member, actions, "primary");
  }

  function renderMemberModelStatus(status) {
    memberModelStatus = status; renderMemberProfile();
  }

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

  async function openMemberTools(member, { automatic = false } = {}) {
    if (!state.managed || !state.info || !state.catalogConnected) {
      notificationsUI.renderNotificationPlatforms();return;
    }
    memberToolsView = { identity: toolsIdentity(), runtimeEpoch:state.info.service_epoch, generation:++memberToolsGeneration, member, cli: null, cliConnected:false };
    if (automatic) await runtime.observe("configuration", { cached: true });
    else await connectionsUI.refreshPlatformState();
  }

  // CLI bindings have a separate revision and never inherit legacy grants.
  async function loadCLITools(v, { background = false, current = () => true, onFailure = () => {} } = {}) {
    // A poll cannot observe an intermediate local write or erase its feedback.
    if (background && (workspaceUI.workspaceView?.memberToolBusy || onboardingUI.onboarding.busy)) return;
    const read = v.cliRead = (v.cliRead || 0) + 1;
    const finish = beginRead(env, v, "cliReadState", () => { if (toolsCurrent(v) && read === v.cliRead) renderMemberTools(v); });
    let failed = false;
    try {
      const q = new URLSearchParams({ service_epoch: v.identity.service_epoch, caller_id: v.identity.caller.id,
        room_id: v.identity.room_id, room_instance: v.identity.room_instance });
      const data = await request(`/v1/tools/bindings?${q}`);
      if (!current() || !toolsCurrent(v) || read !== v.cliRead) return;
      v.cli = data;v.cliConnected=true;
      if (!v.attentionBusy) {
        try { const attention=await request("/v1/member/input-attention",{...v.identity,agent_id:v.member.agent_id,membership_id:v.member.membership_id,expected_version:0});if(current()&&toolsCurrent(v)&&read===v.cliRead)v.attention=attention; } catch { /* Older runtimes do not expose input preferences. */ }
      }
      if (!background) v.cliResult = null;
    } catch (error) {
      if (!current() || !toolsCurrent(v) || read !== v.cliRead) return;
      failed = true; v.cliConnected=false; onFailure(error);
    } finally { finish(failed); }
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

  function accountRecovery(item, view, selection, gaps, result) {
    const errors = [view?.error_code, ...Object.values(view?.errors || {}), view?.events?.error_code, view?.todos?.error_code];
    const next = action => gaps.some(c => c.next_action === action);
    if (errors.includes("identity_mismatch")) return {reason:"账号与连接不一致",action:"核验账号"};
    if (["expired","revoked"].includes(item.authorization_status)) return {reason:"授权已失效",action:"重新授权"};
    if (errors.includes("credential_rejected")) return {reason:"通知因认证失败暂停",action:"检查账号"};
    if (errors.includes("permission_denied")) return {reason:"缺少所需权限",action:"查看权限"};
    if (next("configure_application")) return {reason:"应用配置待完成",action:"配置应用"};
    if (next("authorize_user") || result?.tool === item.id && /补全/.test(result.message)
      || item.authorization_status && item.authorization_status !== "active") return {reason:"用户授权未完成",action:"完成授权"};
    if (errors.includes("credential_unavailable")) return item.provider === "gitlab"
      ? {reason:"通知因凭据问题暂停",action:"检查账号"} : {reason:"登录凭据不可用",action:"重新授权"};
    if (item.validation?.status !== "valid") return {reason:item.validation?.status === "failed" ? "账号核验未通过" : "账号待核验",action:"核验账号"};
    if (selection?.unavailable_reason === "account_unavailable" || next("revalidate_account")) return {reason:"账号需重新核验",action:"核验账号"};
    // A missing or expired Lark snapshot is ordinary read-time freshness, not
    // evidence that the connected account needs attention. Actual check errors
    // and permission gaps retain their recovery actions.
    if (gaps.some(c => c.next_action === "retry_check" && !(item.provider === "lark" && c.state === "unchecked" &&
        ["check_expired", "check_missing"].includes(c.reason_code)))) return {reason:"权限状态待刷新",action:"检查权限",kind:"status"};
    return null;
  }

  function toolSwitch(v,item) {
    const label=document.createElement("label"),input=document.createElement("input"),text=document.createElement("span");
    const d=typeof item==="string" ? v?.cli?.catalog?.find(d=>d.name===item) : v?.cli?.catalog?.find(d=>d.provider===item.provider);
    const key=typeof item==="string" ? item : item.id,b=toolBinding(v,item);
    label.className="member-platform-tool workspace-check";input.type="checkbox";input.className="notification-switch";input.setAttribute("role","switch");
    input.setAttribute("aria-label",`${typeof item==="string" ? item : item.name} 使用工具`);
    input.checked=v?.cliPending?.tool===key ? v.cliPending.enabled : v?.cliConflict?.tool===key ? v.cliConflict.enabled : !!b;
    input.disabled=!v?.cli || !v.cliConnected || workspaceUI.workspaceView?.platformsConnected===false || !d || !!workspaceUI.workspaceView?.memberToolBusy || onboardingUI.onboarding.busy || !state.connected || !state.catalogConnected || discussionBusy() || (!b && typeof item!=="string" && item.validation?.status!=="valid");
    text.textContent="工具";label.append(input,text);
    input.addEventListener("change",()=>changeMemberTool(v,item,input.checked));return label;
  }

  async function changeMemberTool(v,item,enabled,repair=false) {
    const ws=workspaceUI.workspaceView;if(!toolsCurrent(v) || !v.cliConnected || ws.memberToolBusy || onboardingUI.onboarding.busy)return;
    const d=typeof item==="string" ? v.cli.catalog.find(d=>d.name===item) : v.cli.catalog.find(d=>d.provider===item.provider);
    if(!d || enabled && typeof item!=="string" && item.validation?.status!=="valid")return;
    const key=typeof item==="string" ? item : item.id,b=toolBinding(v,item);
    const binding={agent_id:v.member.agent_id,membership_id:v.member.membership_id,tool:d.name,...(typeof item!=="string" ? {platform_id:item.id} : {})};
    const bindingKey=JSON.stringify([binding.agent_id,binding.membership_id,binding.tool,binding.platform_id || ""]).replace(/[<>&\u2028\u2029]/g,c=>"\\u"+c.charCodeAt(0).toString(16).padStart(4,"0"));
    if(enabled && (d.provider!=="lark" || repair) && b?.action_authorization)binding.action_authorization=structuredClone(b.action_authorization);
    if(v.cliConflict?.tool===key)v.cliConflict=null;
    ws.memberToolBusy=true;v.cliPending={tool:key,enabled};v.cliResult=null;v.cliRead=(v.cliRead || 0)+1;renderMemberTools(v);
    let message="";
    try {
      const next=await request("/v1/tools/bind",{...v.identity,expected_binding_revision:v.cli.target_revisions?.[bindingKey] ?? 0,binding,enabled,platform_version:enabled && typeof item!=="string" ? item.version : 0},undefined,enabled && d.provider==="lark" ? 70000 : 10000);
      if(toolsCurrent(v))v.cli={...v.cli,...next};
      message=`${typeof item==="string" ? item : item.name} 工具${enabled ? "已启用" : "已停用"}。`;
      if(enabled && next.capabilities?.capabilities?.some(c=>c.configured && c.state!=="ready"))message+=" 部分动作暂不可用，请核对平台权限或本人投递核验状态。";
    } catch(error) {
      if(error.code==="conflict")v.cliConflict={tool:key,enabled};
      message=error.code==="conflict" ? "该成员的工具设置已变化，本次未保存。请核对当前设置后重试。" : error.code==="user_authorization_required" ? "尚未保存，请先补全当前飞书连接的用户授权。" : ["conflict","identity","request","capacity"].includes(error.code) ? "保存失败，请核对当前成员、平台与权限后重试。" : "保存结果未确认，请核对读回状态；不会自动重发。";
    } finally {
      ws.memberToolBusy=false;v.cliPending=null;
      // A read resolves unknown local writes; no bind request is replayed.
      if(toolsCurrent(v)){await connectionsUI.refreshPlatformState();if(!v.cliConnected)message+=" 当前状态暂不可读，请刷新。";v.cliResult={tool:key,message,error:!v.cliConnected || /失败|未确认|尚未保存|未保存/.test(message)};renderMemberTools(v);}
      else if(memberToolsView?.cli && toolsCurrent(memberToolsView))await connectionsUI.refreshPlatformState();
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

  function inputAttentionControl(item,target) {
    const v=memberToolsView;
    if(!v || !toolsCurrent(v) || target?.membership_id!==v.member.membership_id || item?.supports_notifications!==true)return null;
    const preference=v.attention?.available && v.attention.sources.find(p=>p.platform_id===item.id);
    if(!preference)return null;
    const levels=[["low","较少","较少关注"],["normal","普通","普通关注"],["high","重点","重点关注"]];
    const field=document.createElement("div"),label=document.createElement("span"),value=document.createElement("div"),stars=document.createElement("div"),text=document.createElement("span");
    field.className="member-input-attention";label.textContent="关注级别";value.className="member-attention-value";
    stars.className="member-attention-stars";stars.setAttribute("role","radiogroup");stars.setAttribute("aria-label",`${item.name} 关注级别`);stars.setAttribute("aria-describedby","notification-attention-help");
    text.className="member-attention-label";
    const choices=levels.map(([level,,description],index)=>{
      const choice=document.createElement("label"),input=document.createElement("input"),icon=document.createElementNS("http://www.w3.org/2000/svg","svg"),path=document.createElementNS("http://www.w3.org/2000/svg","path");
      choice.className="member-attention-star";choice.title=`${description}（${index+1} 颗星）`;
      input.type="radio";input.name=`member-attention-${target.membership_id}-${item.id}`;input.value=level;input.disabled=!!v.attentionBusy;
      input.setAttribute("aria-label",choice.title);
      icon.setAttribute("viewBox","0 0 24 24");icon.setAttribute("aria-hidden","true");icon.setAttribute("focusable","false");
      path.setAttribute("d","m12 3 2.8 5.7 6.3.9-4.6 4.5 1.1 6.3-5.6-3-5.6 3 1.1-6.3L2.9 9.6l6.3-.9Z");icon.append(path);
      choice.append(input,icon);stars.append(choice);
      input.addEventListener("change",()=>input.checked && saveAttention(level,input));
      return {choice,input};
    });
    function showLevel(level){
      const rank=levels.findIndex(([key])=>key===level);
      text.textContent=levels[rank][1];
      choices.forEach(({choice,input},index)=>{choice.dataset.filled=String(index<=rank);input.checked=input.value===level;});
    }
    async function saveAttention(next,input){
      if(v.attentionBusy || !toolsCurrent(v) || next===preference.attention)return;
      const hadFocus=document.activeElement===input;
      v.attentionBusy=true;v.attentionPlatformID=item.id;v.attentionError="";v.attentionFeedback="正在保存…";
      showLevel(next);choices.forEach(({input})=>{input.disabled=true;});stars.setAttribute("aria-busy","true");
      for(const child of [...$("notification-attention").children])if(child.className==="form-error")child.remove();
      $("notification-attention-status").textContent=v.attentionFeedback;$("notification-attention-status").hidden=false;
      try {
        const updated=await request("/v1/member/input-attention",{...v.identity,agent_id:target.agent_id,membership_id:target.membership_id,platform_id:item.id,source_version:preference.source_version,attention:next,expected_version:preference.version});
        if(toolsCurrent(v)){v.attention=updated;v.attentionFeedback="";}
      }catch(error){
        if(toolsCurrent(v)){
          v.attentionFeedback="";v.attentionError=error.message || "保存失败，请刷新后重试。";
          try {
            const latest=await request("/v1/member/input-attention",{...v.identity,agent_id:target.agent_id,membership_id:target.membership_id,expected_version:0});
            if(toolsCurrent(v))v.attention=latest;
          }catch{ /* Keep the last observed value when readback is unavailable. */ }
        }
      }finally{
        v.attentionBusy=false;
        if(toolsCurrent(v)){
          const focused=hadFocus && (document.activeElement===input || document.activeElement===document.body) && workspaceUI.workspaceView?.notificationPlatformID===item.id && $("notification-detail").open;
          renderMemberPlatforms();notificationsUI.renderNotificationSource();
          if(focused && !$("notification-attention").hidden)$("notification-attention").querySelector("input:checked")?.focus();
        }
      }
    }
    showLevel(preference.attention);value.append(stars,text);field.append(label,value);return field;
  }

  function renderInputAttention(item,target,container) {
    const control=inputAttentionControl(item,target),v=memberToolsView;
    container.replaceChildren(...(control ? [control] : []));container.hidden=!control;
    if(!control)return;
    const help=document.createElement("p"),feedback=document.createElement("p");
    help.id="notification-attention-help";help.className="quiet";help.textContent="影响助理浏览此来源的偏好，不改变接收范围。修改后自动保存。";
    feedback.id="notification-attention-status";feedback.className="quiet";feedback.setAttribute("role","status");
    feedback.textContent=v.attentionPlatformID===item.id ? v.attentionFeedback || "" : "";feedback.hidden=!feedback.textContent;
    container.append(help,feedback);
    if(v.attentionPlatformID===item.id && v.attentionError){
      const error=document.createElement("p");error.className="form-error";error.setAttribute("role","alert");error.textContent=v.attentionError;container.append(error);
    }
  }

  function renderMemberPlatforms() {
    const ws=workspaceUI.workspaceView,target=notificationsUI.memberNotificationTarget(),v=memberToolsView;
    $("member-notification-sources").hidden=!state.managed || !ws || !target;
    if(!ws || !target)return;
    const items=memberPlatformItems(ws,v),configured=items.filter(item=>memberHasPlatform(item,target,v));
    const reading=readState(ws.platformsReadState,ws.notificationsReadState,v?.cliReadState);
    const loaded=!!ws.allPlatforms && !!ws.allSubscriptions && !!v?.cli;
    $("member-notification-platforms").setAttribute("aria-busy",String(reading.pending));
    $("member-platform-count").hidden=!loaded;
    const inlineAccountIssues = new Set();
    $("member-notification-platforms").replaceChildren(...configured.map(item=>{
      const view=notificationsUI.notificationView(ws,item,target),hasNotifications=item.supports_notifications===true;
      const row=notificationsUI.notificationRow(ws,item,target,true);
      row.className+=" member-platform-row";
      row.children[0].setAttribute("aria-label",hasNotifications ? `编辑 ${item.name} 的通知设置` : `${item.name} 平台详情`);
      const heading=row.children[0].children[1].children[0],name=document.createElement("span"),edit=document.createElementNS("http://www.w3.org/2000/svg","svg"),path=document.createElementNS("http://www.w3.org/2000/svg","path");
      name.className="member-platform-name";name.textContent=item.name;
      edit.setAttribute("class","edit-icon platform-edit-icon");edit.setAttribute("viewBox","0 0 24 24");edit.setAttribute("aria-hidden","true");edit.setAttribute("focusable","false");
      path.setAttribute("d","m15 5 4 4M4 20l4-1L20 7a2.8 2.8 0 0 0-4-4L4 15Z");edit.append(path);heading.replaceChildren(name,edit);
      const attention=v?.attention?.available && v.attention.sources.find(p=>p.platform_id===item.id);
      const attentionLabel={high:"重点",low:"较少"}[attention?.attention];
      if(attentionLabel){const hint=document.createElement("span");hint.className="member-platform-attention";hint.textContent=attentionLabel;heading.append(hint);}
      row.children[0].children[1].children[1].textContent=[hasNotifications ? notificationsUI.notificationConfigured(item,view) ? notificationsUI.notificationDescription(view.config) : "尚未开启通知" : "无通知能力",item.validation?.username ? `@${item.validation.username}` : "账号待核验"].join(" · ");
      row.children[0].title=`${item.name} · ${row.children[0].children[1].children[1].textContent}`;
      const input=row.children[1],label=document.createElement("label");input.id=`member-platform-notify-${item.id}`;label.htmlFor=input.id;label.className="member-platform-notify-label";label.textContent=hasNotifications ? "通知" : "无通知能力";
      input.hidden=!hasNotifications;if(!notificationsUI.notificationConfigured(item,view) && onboardingUI.onboarding.busy)input.disabled=true;label.hidden=!hasNotifications;
      row.children[2].textContent=[notificationsUI.notificationConfigured(item,view) ? notificationsUI.notificationIssue(ws,item,view) : "",toolIssue(v,item)].filter(Boolean).join(" ");row.children[2].hidden=!row.children[2].textContent;row.children[2].dataset.kind=v?.cliResult?.tool===item.id && !v.cliResult.error ? "success" : "error";
      const info=row.children[0],issue=row.children[2],controls=document.createElement("div");controls.className="member-platform-controls";
      controls.append(label,input,toolSwitch(v,item));
      const accountActions=document.createElement("div");accountActions.className="member-platform-account-actions";
      const selection=v?.cli?.selections?.find(s=>s.agent_id===target.agent_id && s.membership_id===target.membership_id && s.platform_id===item.id), gaps=selection?.availability?.capabilities?.filter(c=>c.configured!==false && c.state!=="ready") || [];
      const recovery = accountRecovery(item,view,selection,gaps.filter(c=>c.name!=="image.upload"),v?.cliResult);
      if(recovery) {
        inlineAccountIssues.add(item.id);
        const reason=document.createElement("span");reason.className="member-platform-account-reason";reason.textContent=recovery.reason;reason.dataset.kind=recovery.kind || "error";
        const settings=document.createElement("button");settings.type="button";settings.className="text-button button-compact";settings.textContent=recovery.action;
        settings.setAttribute("aria-label",`${recovery.action}：${item.name}`);
        settings.addEventListener("click",async()=>{await settingsUI.openSettings("platforms",settings);const current=connectionsUI.platforms.items.find(p=>p.id===item.id);if(current)await connectionsUI.editPlatform(current,{fromSource:true});});
        accountActions.append(reason,settings);
        issue.hidden = !(v?.cliResult?.tool===item.id && v.cliResult.message);
      }
      if(item.provider === "lark" && toolBinding(v,item)) {
        const configured=selection?.action_authorization?.grants?.length;
        // Account settings owns OAuth diagnostics; member grants and self
        // evidence still require their existing member-scoped repair action.
        if(!recovery && (!configured || gaps.some(c=>["verify_self","enable_member_tool"].includes(c.next_action)))) {
          const repair=document.createElement("button");repair.type="button";repair.className="text-button button-compact";repair.textContent="修复能力";
          repair.disabled=!!ws.memberToolBusy || !v?.cliConnected;
          repair.addEventListener("click",()=>changeMemberTool(v,item,true,true));accountActions.append(repair);
        }
      }
      const imageUpload=selection?.availability?.capabilities?.find(c=>c.name==="image.upload");
      if(item.provider==="lark" && imageUpload) {
        const hint=document.createElement("span");hint.className="member-platform-account-reason";
        const imageActions=selection.availability.image_actions || [];
        const imageTargets=[["message.send_self","本人"],["message.send","指定群"]].filter(([operation])=>imageActions.includes(operation)).map(([,label])=>label);
        hint.textContent=`${imageUploadStatus(imageUpload)}；${imageTargets.length ? `图文范围：${imageTargets.join("、")}` : "图文发送尚未开放"}`;
        hint.dataset.kind="status";accountActions.append(hint);
        if(imageUpload.next_action && imageUpload.state!=="ready" && imageUpload.state!=="unsupported") {
          const repair=document.createElement("button");repair.type="button";repair.className="text-button button-compact";
          repair.textContent="查看图片权限";repair.addEventListener("click",async()=>{await settingsUI.openSettings("platforms",repair);const current=connectionsUI.platforms.items.find(p=>p.id===item.id);if(current)await connectionsUI.editPlatform(current,{fromSource:true});});
          accountActions.append(repair);
        }
      }
      accountActions.hidden=!accountActions.children.length;
      row.replaceChildren(info,controls,issue,accountActions);return row;
    }));
    layoutMemberPlatforms();
    $("member-notification-source-count").textContent=String(configured.length);
    $("member-notification-empty").hidden=!!configured.length || !loaded && reading.pending || reading.failed || !$("member-notification-add-picker").hidden;
    $("member-notification-empty").textContent="添加平台，接收通知或使用工具。";
    const issues=configured.filter(item=>{
      const view=notificationsUI.notificationView(ws,item,target);
      const availability=v?.cli?.selections?.find(s=>s.agent_id===target.agent_id && s.membership_id===target.membership_id && s.platform_id===item.id);
      return notificationsUI.notificationConfigured(item,view) && notificationsUI.notificationIssue(ws,item,view) || !!availability?.unavailable_reason || v?.cliResult?.tool===item.id && v.cliResult.error;
    });
    $("member-platform-issues").replaceChildren(...issues.filter(item=>!inlineAccountIssues.has(item.id)).map(item=>{
      const button=document.createElement("button");button.type="button";button.className="text-button";button.textContent=`${item.name}：查看异常`;button.addEventListener("click",()=>openMemberPlatform(button,item,target));return button;
    }));
    $("member-notification-list-error").classList.toggle("form-error",!!reading.failed);
    $("member-notification-list-error").classList.toggle("quiet",!reading.failed);
    notice("member-notification-list-error",reading.failed ? "部分设置暂不可读，请刷新。" : reading.slow ? "正在读取平台设置…" : "");$("member-notification-list-reload").hidden=!reading.failed;
    $("member-notification-list-reload").disabled=reading.pending;
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
    const sources=readState(ws.platformsReadState,ws.notificationsReadState),sourcesLoaded=!!ws.allPlatforms && !!ws.allSubscriptions;
    const hint=inboxUI.inboxView?.status?.expired_through ? "当前没有保留的通知，旧通知已过期清理。" : sources.failed ? "通知状态暂不可读，请查看平台设置。" : !sourcesLoaded ? sources.slow ? "正在读取通知来源…" : "" : !subscriptions.length ? "添加平台，开始接收通知。" : issues.length ? "尚未收到通知，请查看平台异常提示。" : subscriptions.some(item=>notificationsUI.notificationView(ws,item,target).config.enabled) ? "暂未收到通知。" : "通知已暂停。";
    $("inbox-empty-hint").hidden=!hint || !inboxUI.inboxView?.page || !!inboxUI.inboxView.page.entries.length;
    $("inbox-empty-hint").textContent=hint;
    $("inbox-add-platform").hidden=!!subscriptions.length || !sourcesLoaded || sources.failed;
    const inboxIssues = configured.filter(item => notificationsUI.notificationConfigured(item,notificationsUI.notificationView(ws,item,target)) && notificationsUI.notificationIssue(ws,item,notificationsUI.notificationView(ws,item,target)));
    $("inbox-platform-issues").replaceChildren(...inboxIssues.map(item=>{const b=document.createElement("button");b.type="button";b.className="text-button";b.textContent=`${item.name}：${notificationsUI.notificationIssue(ws,item,notificationsUI.notificationView(ws,item,target))} 查看设置`;b.addEventListener("click",()=>openMemberPlatform(b,item,target));return b;}));
    const pending = [...(ws.allSubscriptions || []),...(ws.retiredSources || [])].filter(source=>notificationsUI.sameNotificationTarget(source.target || source.config?.target,target)).reduce((n,source)=>n+(source.pending || 0),0);
    $("inbox-backlog").textContent = sources.failed ? "来源状态暂不可读，可到平台设置重试读取。" : pending ? `来源积压 ${pending} 条等待投递；容量释放后会自动重试，凭据和绑定异常需在平台设置处理。` : "";
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
  let activityReturn = null, returningActivity = null;
  let activityNavigation = 0;

  let activityMember = null;

  let agentTab = "profile";

  const activity = createActivityReader((member) => rpc("/v1/member/activity", {
    agent_id: member.agent_id, membership_id: member.membership_id,
  }, member.info), renderActivity, () => runtime.observe("activity"));

  const statusNames = { inbox_waiting: "等待读取恢复", inbox_stalled: "收件箱读取已停止", paused: "已暂停", idle: "等待消息", waiting: "等待执行", running: "运行中", completed: "已完成",
    failed: "执行失败", interrupted: "已中断", unavailable: "暂不可用", detached: "未连接",
    budget_exhausted: "预算用尽", checkpoint_failed: "检查点失败", backend_blocked: "暂时无法恢复", recovering: "正在恢复",
    rotating: "轮换中", closing: "清理中", merging: "合并通知" };

  function memberRole(member) {
    return roomsUI.selectedEntry()?.members?.find(role => role.id === member.agent_id);
  }

  function openMemberActivity(member, opener, tab = "profile", { readingView = null, fromConversation = false, automatic = false } = {}) {
    layoutUI.showMemberContent?.();
    notice("notification-next-step", "");
    notificationsUI.invalidateNavigation();
    if (!state.connected || !state.info) return;
    const current = state.members.find(item => item.agent_id === member.agent_id && item.membership_id === member.membership_id);
    if (!current) return;
    if (fromConversation && $("activity-dialog").open && !$("activity-dialog").hidden &&
      activityMember?.membership_id === current.membership_id && agentTab === tab) return Promise.resolve();
    activityNavigation++;
    if (fromConversation || !$("activity-dialog").open || activityMember?.membership_id !== current.membership_id) {
      const previous = $("activity-dialog").open && !$("activity-dialog").hidden && selectedAgent();
      activityReturn = { view: captureReturnView(env, runtime, { opener }), panel: layoutUI.captureMemberPanel(), fromConversation,
        previous: previous ? { member: previous, tab: agentTab, back: activityReturn, inbox: inboxUI.captureReadingView() } : null };
    }
    activityFocus = { element: opener, membership: current.membership_id };
    $("notification-detail").close();
    $("activity-dialog").hidden=false;
    activityMember = { agent_id: current.agent_id, membership_id: current.membership_id };
    memberModelStatus = {}; renderMemberProfile();
    layoutUI.expandMembers();
    layoutUI.savePanels(); layoutUI.renderPanels();
    if (!$("activity-dialog").open) $("activity-dialog").show();
    memberToolsView = null;
    renderActivityControls();
    notificationsUI.renderNotificationPlatforms();
    if(state.managed){void settingsUI.openMemberModel?.(current);void onboardingUI.restoreOnboarding()?.then(()=>renderMemberPlatforms());}
    readingView?.();
    const selected = selectAgentTab(tab);
    if(state.managed)void openMemberTools(current, { automatic });
    if (compactViewport(window)) {
      $("activity-dialog").scrollIntoView({ block: "start" });
      $("close-activity").focus({preventScroll:true});
    }
    return selected;
  }

  function renderActivityNavigation() {
    const button = $("close-activity");
    const root = roomsUI.isDirect() && !activityReturn?.previous;
    const direction = root && !compactViewport(window) ? "close" : "left";
    const label = root ? compactViewport(window) ? "返回对话" : "关闭设置" : "返回设置";
    button.hidden = false;
    button.setAttribute("aria-label", label); button.title = label;
    button.classList.toggle("agent-detail-close", direction === "close");
    if (button.dataset.direction !== direction) {
      button.replaceChildren(createNavigationIcon(document, direction));
      button.dataset.direction = direction;
    }
  }

  function renderActivityControls() {
    workspaceUI.renderMemberDirectory?.();
    const panel = $("activity-dialog");
    if (panel.open && !state.members.some(member => member.membership_id === activityMember?.membership_id)) panel.close();
    notificationsUI.renderContextViews();
    renderActivityNavigation();
    const selected=selectedAgent();
    $("activity-title").textContent = !roomsUI.isDirect() && selected ? `${agentName(selected)} · 设置` : "设置";
    $("agent-profile-name").textContent = selected ? agentName(selected) : "";
    if (selected) renderAvatar(document, $("activity-avatar"), {kind:"agent",id:selected.agent_id,name:agentName(selected),direct:roomsUI.isDirect(),defaultAssistant:roomsUI.isDefaultAssistant()});
    $("agent-tabs").hidden = false;
    $("agent-tab-platforms").hidden = !state.managed;
    const member = selectedAgent();
    if (!member || !panel.open) { $("agent-name-row").hidden=true;$("agent-name-form").hidden=true;return; }
    const nameKey=JSON.stringify([state.info,member.agent_id,member.membership_id]);
    if(!nameEditor || nameEditor.key!==nameKey){nameEditor={key:nameKey,expected:agentName(member)};nameEditing=false;$("agent-display-name").value=agentName(member);notice("agent-name-feedback","");}
    else if(!nameSaving && $("agent-display-name").value===nameEditor.expected){nameEditor.expected=agentName(member);$("agent-display-name").value=agentName(member);}
    $("agent-name-row").hidden=!state.managed || nameEditing;
    $("agent-name-form").hidden=!state.managed || !nameEditing;
    $("agent-profile-name").hidden = state.managed;
    $("agent-name-label").textContent=roomsUI.isDirect() ? "私信名称" : "成员名称";
    $("agent-name-summary").textContent=agentName(member);
    const editNameLabel=`${agentName(member)}，修改${roomsUI.isDirect() ? "私信" : "成员"}名称`;
    $("edit-agent-name").setAttribute("aria-label",editNameLabel);
    $("edit-agent-name").title=editNameLabel;
    $("edit-agent-name").setAttribute("aria-expanded",String(nameEditing));
    $("edit-agent-name").disabled=nameSaving;
    $("agent-name-save").disabled=nameSaving || !nameEditing;
    $("agent-name-cancel").disabled=nameSaving;
    $("agent-display-name").disabled=nameSaving;
    $("agent-status").textContent = member.execution?.active ? "运行中" :
      ["idle", "completed"].includes(member.status) ? member.execution?.paused ? "已暂停" : "空闲" : statusNames[member.status] || member.status;
    $("agent-status").classList.toggle("running", !!member.execution?.active || recovering(member));
    $("agent-status").classList.toggle("failed", /failed|blocked|unavailable|interrupted/.test(member.status));
    renderMemberActions(member, $("agent-recovery-actions"), "recovery");
    $("agent-recovery-actions").hidden = !$("agent-recovery-actions").children.length;
    renderMemberProfile();
    const execution = member.execution;
    const reset = !recovering(member) && (execution?.session_reset_required || member.status === "backend_blocked");
    const budget = execution?.turn_budget;
    const exhausted = !execution?.session_turns && budget && budget.used >= budget.limit;
    const effects = [];
    if (reset) effects.push("重置会话，清除私有上下文");
    if (exhausted) effects.push(`追加 ${budget.grant || 16} 回合`);
    notice("agent-enable-hint", effects.length ? `启用将${effects.join("，并")}。` : "");
    notice("agent-error", executionMessage(member));
    $("agent-error").classList.toggle("form-error", !recovering(member) && !(execution?.inbox_recovery || execution?.input_recovery));
    renderDiagnostics("agent", recovering(member) ? "" : execution?.error);
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
    agentTab = tab === "profile" ? "profile" : tab === "platforms" && state.managed ? "platforms" : tab === "policy" ? "policy" : tab === "inbox" ? "inbox" : "activity";
    for (const key of ["profile", "platforms", "inbox", "policy", "activity"]) {
      const button = $(`agent-tab-${key}`);
      button.setAttribute("aria-selected", String(agentTab === key));
      button.setAttribute("tabindex", agentTab === key ? "0" : "-1");
    }
    $("agent-controls").hidden = agentTab !== "profile";
    $("activity-body").hidden = $("activity-footer").hidden = agentTab !== "activity";
    $("platforms-member-view").hidden = agentTab !== "platforms";
    $("inbox-member-view").hidden = agentTab !== "inbox";
    $("policy-member-view").hidden = agentTab !== "policy";
    if (agentTab === "activity") await activity.select({ ...member, info: state.info });
    else if (agentTab === "inbox") {
      activity.close();notificationsUI.renderNotificationPlatforms();
      if(inboxUI.inboxView?.page && !inboxUI.inboxView.loading && sameService(inboxUI.inboxView.info,state.info) && inboxUI.inboxView.member.agent_id===member.agent_id && inboxUI.inboxView.member.membership_id===member.membership_id)$("inbox-member-view").scrollTop=inboxUI.inboxView.scrollTop || 0;
      else await inboxUI.loadMemberInbox();
    }
    else if (agentTab === "platforms") { activity.close();notificationsUI.renderNotificationPlatforms(); }
    else if (agentTab === "policy") { activity.close(); await policyUI.loadMemberPolicy(true); }
    else activity.close();
  }

  function renderMemberActions(member, actions, mode) {
    const execution = member.execution;
    const signature = JSON.stringify([member.agent_id, agentName(member), member.membership_id, member.status, execution?.paused, execution?.active,
      execution?.session_reset_required, execution?.inbox_recovery, execution?.input_recovery, execution?.inbox?.recovery_blocked, state.connected, discussionBusy(), memberBusy.has(member.membership_id), member.unread_count, execution?.agent?.turn?.status, mode]);
    if (actions.dataset.signature === signature) return;
    actions.dataset.signature = signature;
    actions.replaceChildren();
    if (!execution) return;
    const enable = !recovering(member) && (execution.paused || execution.session_reset_required || member.status === "backend_blocked" || member.status === "budget_exhausted");
    const add = (label, action) => {
      const button = document.createElement("button"); button.type = "button"; button.className = "secondary-button button-compact"; button.textContent = label;
      button.setAttribute("aria-label", `${label} ${agentName(member)}`);
      button.disabled = memberBusy.has(member.membership_id) || !state.connected || discussionBusy() || member.status === "checkpoint_failed" || member.status === "rotating" && action !== "pause";
      button.addEventListener("click", () => controlMember(member, action)); actions.append(button);
    };
    if (mode === "primary") add(enable ? "启用" : "暂停", enable ? "run" : "pause");
    if (mode === "recovery" && (execution.inbox_recovery || execution.input_recovery) && !execution.active && !execution.paused && !execution.inbox?.recovery_blocked && !execution.input_recovery?.blocked && ["completed", "failed", "interrupted"].includes(execution.agent?.turn?.status) && ["inbox_waiting", "inbox_stalled"].includes(member.status)) add("继续读取收件箱", "inbox/continue");
  }

  function renderExecutionHint() {
    const available = state.connected && state.info && !discussionBusy();
    for (const [id, request] of stopRequests) {
      const member = state.members.find(member => member.membership_id === id);
      if (!member || member.execution?.active && member.execution.execution_id !== request.executionID) {
        stopRequests.delete(id);
      } else if (available && member.execution?.stopping) {
        request.phase = "settling";
      } else if (available && request.phase === "settling" && !member.execution?.active) {
        request.phase = member.execution?.error || /failed|blocked|unavailable/.test(member.status) ? "error" : "stopped";
      }
    }
    const running = available ? state.members.filter(member => member.execution?.active || member.status === "running") : [];
    const waiting = available ? state.members.filter(member => member.status === "waiting" && !member.execution?.active && !member.execution?.paused) : [];
    const restoring = available ? state.members.filter(recovering) : [];
    const names = members => members.map(agentName).join("、");
    // Active covers Session opening, model work and tools; it does not identify a thinking phase.
    const feedback = available ? [...stopRequests].filter(([id, request]) =>
      ["stopped", "error"].includes(request.phase) && ![...running, ...waiting].some(member => member.membership_id === id)) : [];
    const feedbackText = feedback.some(([, request]) => request.phase === "error") ? "停止未确认" : feedback.length ? "已停止" : "";
    const text = running.length ? `${names(running)} 正在处理…` : waiting.length ? `${names(waiting)} 正在等待执行…` : restoring.length ? `${names(restoring)} 正在恢复…` : feedbackText;
    const queue = running.length && waiting.length ? `${names(waiting)} 等待执行` : "";
    const hint = $("execution-hint");
    const primary = running.length ? running : waiting.length ? waiting : restoring;
    const phaseText = running.length ? "正在处理…" : waiting.length ? "正在等待执行…" : "正在恢复…";
    const signature = JSON.stringify([runtime.viewVersion, text, queue, primary.map(member => [member.membership_id, member.execution?.execution_id,
      member.execution?.stopping]), [...stopRequests]]);
    if (hint.dataset.signature === signature) return;
    const follow = conversationUI.nearBottom();
    hint.dataset.signature = signature;
    hint.hidden = !text;
    hint.dataset.phase = running.length ? "running" : waiting.length ? "waiting" : restoring.length ? "recovering" : "stopped";
    const content = $("execution-text");
    content.replaceChildren();
    const mixedPhases = primary.some(member => member.execution?.stopping ||
      ["requesting", "settling"].includes(stopRequests.get(member.membership_id)?.phase));
    for (const [index, member] of primary.entries()) {
      if (index) {
        const separator = document.createElement("span"); separator.textContent = "、"; content.append(separator);
      }
      const button = document.createElement("button");
      const name = agentName(member);
      const request = stopRequests.get(member.membership_id);
      const stopping = member.execution?.stopping || ["requesting", "settling"].includes(request?.phase);
      button.type = "button"; button.className = "execution-member";
      button.textContent = stopping ? `${name} 正在停止…` : primary.length === 1 || mixedPhases ? `${name} ${phaseText}` : name;
      button.append(createNavigationIcon(document));
      button.dataset.activityMember = member.membership_id;
      button.setAttribute("aria-label", `查看详情 ${name}`);
      button.title = `查看 ${name} 的详情`;
      button.addEventListener("click", () => openMemberActivity(member, button, "activity", {fromConversation:true}));
      content.append(button);
      if (member.execution?.active && member.execution.execution_id) {
        const stop = document.createElement("button");
        stop.type = "button"; stop.className = "execution-stop";
        stop.dataset.stopMember = member.membership_id;
        stop.setAttribute("aria-label", `停止 ${name}`); stop.title = `停止 ${name} 当前回合`;
        stop.disabled = stopping;
        const icon = document.createElement("span"); icon.setAttribute("aria-hidden", "true"); stop.append(icon);
        const version = runtime.viewVersion;
        stop.addEventListener("click", () => { if (currentView(version)) return stopMember(member); });
        content.append(stop);
      }
      if (request?.phase === "error") {
        const error = document.createElement("span"); error.className = "execution-feedback";
        error.textContent = "停止未确认"; error.title = request.error || member.execution?.error || "请查看成员状态";
        content.append(error);
      }
    }
    if (primary.length > 1 && !mixedPhases) {
      const phase = document.createElement("span"); phase.textContent = ` ${phaseText}`; content.append(phase);
    }
    for (const [id, request] of feedback) {
      const status = document.createElement("span"); status.className = "execution-feedback";
      const member = state.members.find(member => member.membership_id === id);
      const name = agentName(member);
      status.textContent = `${primary.length || feedback.length > 1 ? `${name} ` : ""}${request.phase === "stopped" ? "已停止" : "停止未确认"}`;
      status.title = request.error || member.execution?.error || "";
      content.append(status);
    }
    $("execution-queue").textContent = queue;
    $("execution-queue").hidden = !queue;
    hint.title = [text, queue].filter(Boolean).join(" · ");
    $("timeline-wrap").classList.toggle("has-activity", !!text);
    if (follow) $("timeline").scrollTop = $("timeline").scrollHeight;
  }

  function syncDirectDetail() {
    if (!roomsUI.isDirect() || !state.connected || !layoutUI.membersPanelOpen() || $("notification-detail").open || state.members.length !== 1) return;
    if ($("activity-dialog").open && selectedAgent()?.membership_id === state.members[0].membership_id) return;
    void openMemberActivity(state.members[0], $("room-menu-label"), "profile", {automatic:true});
  }

  const showHumanMember = () => !!state.info && !roomsUI.isDirect();
  const participantCount = () => state.members.length + (showHumanMember() ? 1 : 0);

  function renderMembers() {
    renderMemberListToggle();
    syncDirectDetail();
    $("room-details").hidden = roomsUI.isDirect();
    $("room-members-heading").hidden = roomsUI.isDirect();
    renderActivityControls();
    renderExecutionHint();
    $("member-count").textContent = participantCount();
    $("room-title-count").textContent = `(${participantCount()})`;
    $("member-empty").hidden = state.members.length > 0;
    const human = showHumanMember() ? {id:state.operator, name:userMenuUI.displayName()} : null;
    // Keep focus and controls stable when polling reports no visible changes.
    const signature = JSON.stringify([state.members, human]) + [...memberBusy].join(",") + state.connected + discussionBusy();
    if ($("members").dataset.signature === signature) return;
    $("members").dataset.signature = signature;
    const fragment = document.createDocumentFragment();
    for (const member of state.members) {
      const el = $("member-template").content.firstElementChild.cloneNode(true);
      const execution = member.execution;
      const running = execution?.active;
      const label = running ? "运行中" : statusNames[member.status] || member.status;
      const role = memberRole(member);
      el.querySelector(".member-name").textContent = agentName(member);
      const summary = el.querySelector(".member-summary");
      summary.dataset.activityMember = member.membership_id;
      summary.setAttribute("aria-label", `查看 ${agentName(member)} 的详情，${label}${runtimeFailed(member) ? "，运行异常" : ""}`);
      summary.title = `${agentName(member)} · ${label}`;
      summary.dataset.state = runtimeFailed(member) || /failed|blocked|unavailable|interrupted/.test(member.status) ? "error" : running || recovering(member) ? "running" : "idle";
      summary.disabled = !state.connected;
      summary.addEventListener("click", () => openMemberActivity(member, summary));
      renderAvatar(document, el.querySelector(".avatar"), {kind:"agent", id:member.agent_id, name:agentName(member), direct:roomsUI.isDirect(), defaultAssistant:roomsUI.isDefaultAssistant()});
      const profile = el.querySelector(".member-profile"), description = role?.name || member.runner?.profile;
      profile.textContent = description || "";
      profile.hidden = roomsUI.isDirect() || !description || description === agentName(member) || description === member.agent_id;
      const status = el.querySelector(".member-status");
      status.textContent = label;
      status.classList.toggle("running", !!running);
      status.classList.toggle("failed", /failed|blocked|unavailable|interrupted/.test(member.status));
      const budget = execution?.turn_budget;
      el.querySelector(".member-details").textContent = `${member.unread_count} 条未读 · 已读至 #${member.read_through}` +
        (execution?.session_turns ? `\n${execution.session_turns.baseline_known ? `本会话 ${execution.session_turns.used} / ${execution.session_turns.limit}` : "待接入轮换"} · 累计 ${execution.session_turns.total}` : budget ? `\n回合 ${budget.used} / ${budget.limit}` : "");
      if (executionMessage(member)) {
        const error = el.querySelector(".member-error");
        error.hidden = false;
        error.textContent = executionMessage(member);
        error.classList.toggle("recovering", recovering(member));
      }
      fragment.append(el);
    }
    if (human) {
      const el = $("human-member-template").content.firstElementChild.cloneNode(true);
      el.querySelector(".member-summary").title = `${human.name} (你)`;
      el.querySelector(".member-summary").setAttribute("aria-label", `${human.name}，你`);
      el.querySelector(".member-name").textContent = human.name;
      renderAvatar(document, el.querySelector(".avatar"), {kind:"human", id:human.id});
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
    notice("profile-hint", profiles.length ? "新成员默认暂停，添加后点击「启用」。" : "还没有可用角色。请在启动服务时通过 --dsh-config 指定配置文件。");
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
    const stopping = !!execution?.stopping;
    $("activity-status").textContent = stopping ? "正在停止" : execution?.active ? "运行中" : statusNames[member.status] || member.status;
    $("activity-progress").textContent = `已读至 #${member.read_through} · ${member.unread_count} 条未读`;
    const budget = execution?.turn_budget;
    const session = execution?.session_turns;
    $("activity-budget").textContent = session ? `${session.baseline_known ? `${session.used} / ${session.limit}` : "待接入轮换"} · 累计 ${session.total}` : budget ? `${budget.used} / ${budget.limit}` : "暂不可用";
    const rotation = execution?.rotation;
    const rotationReasons = { session_max_turns: "达到会话回合阈值", manual: "手动轮换", model_change: "模型切换", backend_recovery: "自动恢复会话" };
    const phases = {prepared:"等待旧执行结束",old_closed:"正在准备新会话",completed:"已完成"};
    notice("activity-rotation", rotation ? `${rotationReasons[rotation.reason] || "会话切换"} · ${rotation.status === "failed" ? "暂未完成" : rotation.reason === "model_change" ? ({prepared:"等待当前回合结束",old_closed:"正在安装新模型",completed:"已完成"}[rotation.phase] || "等待处理") : phases[rotation.phase] || "处理中"}` : "");
    const turn = execution?.agent?.turn;
    $("activity-turn").textContent = execution?.agent ? `${execution.agent.turn_number}` +
      (turn ? ` · ${statusNames[turn.status] || turn.status}` : " · 尚未运行") : "暂不可用";
    notice("activity-execution-error", executionMessage(member));
    $("activity-execution-error").classList.toggle("form-error", !recovering(member) && !(execution?.inbox_recovery || execution?.input_recovery));
    const live = !!data.current_output, output = data.current_output || data.last_output;
    renderDiagnostics("activity", recovering(member) ? "" : [...new Set([execution?.error, output?.error, rotation?.error].filter(Boolean))].join("\n"));
    if (!output) {
      $("activity-output").hidden = true;
      $("activity-jump-latest").hidden = true;
      notice("activity-notice", data.output_error || (stopping ? "等待当前执行结束…" : execution?.active ? "回合已启动，等待模型或工具活动…" : "暂无私有活动。"));
      return;
    }
    notice("activity-notice", "");
    $("activity-output").hidden = false;
    const result = output.result;
    // The scheduler clears its error only after a requested cancellation has
    // settled cleanly. The saved diagnostic still retains the raw error.
    const stopped = !live && !execution?.active && turn?.status === "interrupted" &&
      !execution?.error && !execution?.session_reset_required && !/failed|blocked|unavailable/.test(member.status);
    const outputError = stopped || recovering(member) ? "" : output.error || "";
    const reasons = { end_turn: "已完成", cancelled: "已停止", max_tokens: "达到 token 上限", refusal: "拒绝执行" };
    const failed = !!(runtimeFailed(member) || outputError || turn?.status === "failed");
    const outcome = recovering(member) ? "正在恢复" : stopped ? "已停止" : failed ? "执行失败" : reasons[result.stopReason] || "已结束";
    $("activity-output-title").textContent = live ? `当前回合 · ${stopping ? "正在停止" : "运行中"}` : `最近回合 · ${outcome}`;
    const settling = live && !stopping && !!result.stopReason;
    $("activity-stop").textContent = settling ? "正在结清" : "";
    $("activity-stop").hidden = !settling;
    if (stopped) $("activity-turn").textContent = `${execution.agent.turn_number} · 已停止`;
    notice("activity-output-error", outputError ? "该回合未能完成，可查看诊断详情。" : "");
    if (!Array.isArray(result.events)) {
      $("activity-output").hidden = true;
      $("activity-jump-latest").hidden = true;
      notice("activity-notice", "活动记录格式无效，无法显示。");
      return;
    }
    renderActivityTimeline(result, JSON.stringify([selected.info, selected.membership_id, execution?.agent?.turn_number]));
    $("activity-timeline").hidden = false;
    notice("activity-timeline-empty", result.events.length ? "" : live ? stopping ? "等待当前执行结束…" : "等待模型或工具活动…" : "该回合没有活动记录。");
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
    returningActivity = activityReturn;
    $("activity-dialog").close();
  }

  async function controlMember(member, action) {
    const info = state.info, version = runtime.viewVersion;
    memberBusy.add(member.membership_id); renderMembers();
    try {
      const identity = { agent_id: member.agent_id, membership_id: member.membership_id };
      const failedUnread = member.unread_count > 0 && ["failed", "interrupted"].includes(member.execution?.agent?.turn?.status);
      await rpc(`/v1/member/${action}`, { ...identity, retry: action === "run" && failedUnread }, info);
      await refresh();
    } catch (error) { if (currentView(version)) notice("sync-error", `${agentName(member)} 操作未确认，请查看成员状态。${error.message}`); }
    finally { memberBusy.delete(member.membership_id); renderMembers(); }
  }

  async function stopMember(member) {
    const current = state.members.find(item => item.agent_id === member.agent_id && item.membership_id === member.membership_id);
    const previous = stopRequests.get(member.membership_id);
    if (!state.connected || discussionBusy() || !current?.execution?.active || !member.execution?.execution_id ||
      current.execution.execution_id !== member.execution.execution_id || current.execution.stopping ||
      ["requesting", "settling"].includes(previous?.phase)) return;
    const info = state.info, version = runtime.viewVersion;
    const request = { executionID: member.execution.execution_id, phase: "requesting" };
    stopRequests.set(member.membership_id, request); renderExecutionHint();
    try {
      await rpc("/v1/member/cancel", { agent_id: member.agent_id, membership_id: member.membership_id,
        execution_id: request.executionID }, info);
      request.phase = "settling";
    } catch (error) {
      request.phase = "error"; request.error = error.message;
    }
    if (currentView(version)) { await refresh(); renderExecutionHint(); }
  }

  function resetTools() { memberToolsView = null; }

  function resetActivity() {
    stopRequests.clear();
    activity.close(); $("activity-dialog").close();
    activitySignature = ""; activityMember = null;
    memberModelStatus = {}; renderMemberProfile();
  }

  function mount() {
    renderMemberListToggle();
    $("toggle-member-list").addEventListener("click", () => {
      memberListExpanded = !memberListExpanded;
      try { storage?.setItem(memberListKey, JSON.stringify(memberListExpanded)); } catch { /* Keep this page's choice in memory. */ }
      renderMemberListToggle();
    });
    $("edit-agent-name").addEventListener("click",()=>{
      const member=selectedAgent();if(!state.managed || !member || nameSaving)return;
      nameEditor={key:JSON.stringify([state.info,member.agent_id,member.membership_id]),expected:agentName(member)};
      nameEditing=true;$("agent-display-name").value=agentName(member);notice("agent-name-feedback","");
      renderActivityControls();$("agent-display-name").focus();
    });
    const cancelNameEdit=()=>{
      if(nameSaving)return;
      nameEditing=false;nameEditor=null;renderActivityControls();$("edit-agent-name").focus();
    };
    $("agent-name-cancel").addEventListener("click",cancelNameEdit);
    $("agent-name-form").addEventListener("keydown",event=>{
      if(event.key!=="Escape")return;
      event.stopPropagation?.();if(event.isComposing)return;
      event.preventDefault();cancelNameEdit();
    });
    $("agent-name-form").addEventListener("submit",async event=>{
      event.preventDefault();const member=selectedAgent();if(!member || nameSaving || !nameEditor || !nameEditing)return;
      const editor=nameEditor,info={...state.info},name=$("agent-display-name").value.trim();
      if(!name || /[\r\n\t]/.test(name) || new TextEncoder().encode(name).length>256){notice("agent-name-feedback","名称不能为空，且不能超过 256 字节或包含换行。");return;}
      nameSaving=true;renderActivityControls();notice("agent-name-feedback","");
      try{
        await request("/v1/member/name",{service_epoch:state.catalog.service_epoch,room_id:info.room_id,room_instance:info.room_instance,agent_id:member.agent_id,membership_id:member.membership_id,agent_name:name,expected_name:editor.expected,caller:{kind:"human",id:state.operator}});
        if(nameEditor===editor){editor.expected=name;nameEditing=false;notice("agent-name-feedback","");}
      }catch(error){if(nameEditor===editor)notice("agent-name-feedback",error.code==="conflict" ? "名称已在其他页面修改，请关闭详情后重新打开。" : `保存未确认，请核对当前名称后再修改。${error.message}`);}
      finally{nameSaving=false;await refresh();renderActivityControls();if(nameEditor===editor && !nameEditing && $("activity-dialog").open)$("edit-agent-name").focus();}
    });
    if(typeof ResizeObserver!=="undefined")new ResizeObserver(layoutMemberPlatforms).observe($("member-notification-platforms"));

    document.fonts?.ready.then(layoutMemberPlatforms);

    for (const key of ["profile", "platforms", "inbox", "policy", "activity"]) {
      const button = $(`agent-tab-${key}`);
      button.addEventListener("click", () => selectAgentTab(key));
      button.addEventListener("keydown", event => {
        if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
        event.preventDefault();
        const keys = state.managed ? ["profile", "platforms", "inbox", "policy", "activity"] : ["profile", "inbox", "policy", "activity"];
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
      if(!nameSaving)nameEditor=null;
      if ($("activity-dialog").open) return;
      const back = returningActivity; returningActivity = null; activityReturn = null;
      activityNavigation++;
      activity.close(); memberToolsView = null; activityMember = null; agentTab = "profile"; activitySignature = "";
      inboxUI.reset();
      activityScrollMember = ""; activityFollowing = true; activityScrollTop = 0; $("activity-jump-latest").hidden = true;
      activityTimelineContext = ""; activityTimelineRows.clear(); $("activity-timeline").replaceChildren();
      renderActivityControls();
      if (back?.view.current()) {
        if (roomsUI.isDirect() && !back.previous) { layoutUI.collapseMembers(); layoutUI.savePanels(); layoutUI.renderPanels(); }
        else back.panel();
        if (back.previous && state.members.some(member => member.membership_id === back.previous.member.membership_id)) {
          const selected = openMemberActivity(back.previous.member, activityFocus?.element, back.previous.tab, {readingView:back.previous.inbox});
          activityReturn = back.previous.back;
          renderActivityControls();
          const navigation = activityNavigation;
          void selected?.then(() => {
            if (navigation === activityNavigation && $("activity-dialog").open && selectedAgent()?.membership_id === back.previous.member.membership_id && agentTab === back.previous.tab) back.view.restore();
          });
        }
        back.view.restore($("room-menu-label"));
        activityFocus = null;
        return;
      }
      const opener = !layoutUI.membersExpanded ? null : activityFocus?.element?.isConnected ? activityFocus.element
        : [...document.querySelectorAll("[data-activity-member]")].find(button => button.dataset.activityMember === activityFocus?.membership);
      if (!$("notification-detail").open) (opener || $("room-menu-label")).focus();
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
    renderInputAttention,
    renderMemberModelStatus,
    memberPlatformItems,
    memberRole,
    get activityMember() { return activityMember; },
    get agentTab() { return agentTab; },
    activity,
    toolsCurrent,
    loadCLITools,
    renderMembers,
    participantCount,
    openMemberActivity,
    syncDirectDetail,
    statusNames,
    selectAgentTab,
    renderExecutionHint,
    renderProfiles
  };
}
