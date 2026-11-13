// Durable connection and selection cards rendered inside conversations.
export function createPlatformInteractions({ env, runtime, conversationUI, settingsUI, connectionsUI, gitlabUI, roomsUI }) {
  const { document, window, request } = env;
  const { state, $, notice } = runtime;

  // Connection interactions are projections of durable operations, never generated
  // HTML or temporary OAuth URLs saved in chat history. Each view owns stable nodes.
  const platformInteractionState = {views:new Map(), operations:[], busy:new Set(), errors:new Map(), loading:false, generation:0};

  const returnKey="teamx.platform-return";
  const identityFields=["room_id","room_instance","agent_id","membership_id","operation_id","source_seq"];
  let returning=null, returnSelected=false;
  function rememberAuthorization(op) {
    const ref={...Object.fromEntries(identityFields.map(key=>[key,op[key]])),installation_id:state.installationID || "",operator:state.operator};
    returning=null;returnSelected=true;
    try{runtime.storage?.setItem(returnKey,JSON.stringify(ref));}catch{}
    conversationUI.saveDraft();
  }
  function restoreAuthorizationSelection(catalog) {
    if(returnSelected)return;
    if(!returning) {
      try {
        const hash=window.location?.hash || "";
        returning=JSON.parse(hash.startsWith("#platform-connection=") ? decodeURIComponent(hash.slice(21)) : runtime.storage?.getItem(returnKey) || "null");
      }catch{returning=null;}
    }
    if(!returning)return;
    if(identityFields.some(key=>!returning[key]) || returning.installation_id!==(state.installationID || "") || returning.operator!==catalog.operator) {clearAuthorizationReturn();return;}
    const room=catalog.rooms.find(r=>r.room_id===returning.room_id && r.room_instance===returning.room_instance);
    if(!room){clearAuthorizationReturn();return;}
    returnSelected=true;roomsUI.selectRoom(room);
  }
  function clearAuthorizationReturn() {
    returning=null;returnSelected=true;
    try{runtime.storage?.removeItem(returnKey);}catch{}
    if(window.location?.hash?.startsWith("#platform-connection="))window.history?.replaceState(null,"",window.location.pathname+window.location.search);
  }
  async function revealAuthorizationReturn(generation) {
    if(!returning || !returnSelected)return;
    const ref=returning;
    const op=platformInteractionState.operations.find(op=>identityFields.every(key=>op[key]===ref[key]));
    if(!op){clearAuthorizationReturn();notice("platform-interaction-error","原授权操作或成员已变化，请核对原会话；未重新发起连接。");return;}
    if(!state.messages.has(op.source_seq)) {
      // Keep history contiguous: adding just the old source would hide the
      // gap from the normal "load earlier" cursor.
      const before=Math.max(op.source_seq,(state.oldest || op.source_seq+1)-1);
      const read=await conversationUI.readPages({mode:"range",after:op.source_seq-1,snapshot_seq:before},state.info);
      if(generation!==platformInteractionState.generation || returning!==ref)return;
      for(const message of read.messages)state.messages.set(message.seq,message);
      conversationUI.renderMessages();renderPlatformInteractions();
    }
    if(generation!==platformInteractionState.generation || returning!==ref)return;
    const card=platformInteractionState.views.get(op.operation_id);
    card?.el.scrollIntoView?.({block:"center"});card?.status?.focus?.();
    clearAuthorizationReturn();
  }

  function resetPlatformInteractions() {
    const s=platformInteractionState;s.generation++;s.loading=false;s.operations=[];s.views.clear();s.busy.clear();s.errors.clear();
    notice("platform-interaction-error", "");conversationUI.resetMessageNodes();
  }

  function connectionInteractionCard(op) {
    if(!["lark_authorization","gitlab_authorization"].includes(op.interaction.kind))return selectionInteractionCard(op);
    const el=document.createElement("section");el.className="platform-connect-interaction";el.dataset.interactionId=op.interaction.id;
    el.setAttribute("aria-label", op.interaction.kind==="gitlab_authorization" ? "GitLab 连接" : "飞书连接");
    const heading=document.createElement("strong"), status=document.createElement("p"), steps=document.createElement("p"), account=document.createElement("p"), hint=document.createElement("p"), feedback=document.createElement("p");
    status.setAttribute("role","status");status.tabIndex=-1;steps.className="quiet";account.className="quiet";hint.className="quiet";feedback.setAttribute("role","status");
    const actions=document.createElement("div");actions.className="platform-connect-actions";
    const link=document.createElement("a");link.className="primary platform-connect-authorize";link.target="_blank";link.rel="noopener noreferrer";link.referrerPolicy="no-referrer";
    const qr=document.createElement("img");qr.className="platform-connect-qr";qr.alt="飞书授权二维码";
    qr.addEventListener("error",()=>{qr.hidden=true;});
    const cancel=document.createElement("button");cancel.type="button";cancel.className="text-button";cancel.textContent="取消连接";
    const settings=document.createElement("button");settings.type="button";settings.className="text-button";settings.textContent="完整设置";
    settings.addEventListener("click",()=>card.op.setup ? openGitLabInteractionSettings(card) : settingsUI.openSettings("platforms"));
    const applicationLink=document.createElement("a");
    if(applicationLink){applicationLink.target="_blank";applicationLink.rel="noopener noreferrer";applicationLink.referrerPolicy="no-referrer";applicationLink.textContent=op.interaction.kind==="gitlab_authorization" ? "前往 GitLab 管理应用 ↗" : "前往飞书应用权限管理 ↗";applicationLink.hidden=true;}
    actions.append(link,cancel,settings);el.append(heading,status,steps,account,qr,actions,hint,feedback);if(applicationLink)el.append(applicationLink);
    const card={el,op,heading,status,steps,account,hint,feedback,link,qr,cancel,applicationLink};
    cancel.addEventListener("click",()=>cancelPlatformInteraction(card));
    link.addEventListener("click",event=>{
      event.preventDefault();
      if(card.op.interaction.kind==="gitlab_authorization")void (card.op.setup ? openGitLabInteractionSettings(card) : authorizeGitLabInteraction(card));
      else openLarkAuthorization(card);
    });
    return card;
  }

  function updatePlatformInteraction(card, op) {
    if(!["lark_authorization","gitlab_authorization"].includes(op.interaction.kind)){updateSelectionInteraction(card,op);return;}
    card.op=op;
    const gitlab=op.interaction.kind==="gitlab_authorization";
    const terminal=["completed","cancelled","expired","failed","unknown"].includes(op.status);
    const success=op.status==="completed" && op.connection.status==="connected";
    const stages={application:gitlab ? "检查 GitLab 应用" : "配置飞书应用",authorization:gitlab ? "授权 GitLab 账号" : "授权飞书账号",connection:"核验并保存连接",complete:"连接已保存"};
    const statuses={running:"正在",waiting:"等待你完成",cancelling:"正在取消",completed:"已连接",cancelled:"已取消",expired:"授权已过期",failed:"连接失败",unknown:"结果未确认"};
    card.heading.textContent=`${op.name} · ${gitlab ? "GitLab" : "飞书"}${op.intent === "authorize" ? " · 补全授权" : ""}`;
    card.status.textContent=terminal || op.status==="cancelling" ? statuses[op.status] : `${statuses[op.status] || "正在"}${stages[op.stage] || "处理连接"}`;
    card.el.dataset.status=success ? "completed" : op.status;
    const stepName=step=>({pending:"待完成",applied:"已完成",reused:"已复用",connected:"已保存",failed:"失败",unknown:"未确认"}[step.status] || "未确认");
    card.steps.textContent=`应用${stepName(op.application)} · 账号授权${stepName(op.authorization)} · 连接${stepName(op.connection)}`;
    card.account.hidden=!op.account_id;card.account.textContent=op.account_id ? `已核验账号：${op.account_id}` : "";
    card.hint.textContent=!op.target_available ? "原 Agent 已移除或替换。可取消剩余操作，或通过完整设置查看连接。" : success ? "连接已保存。可发消息请 Agent 继续。" : terminal ? (op.error_code==="service_restarted" ? "服务已重启，请在完整设置中核对已发生的配置和授权。" : "已完成的应用配置和授权会保留，可请 Agent 核对进度，或进入完整设置处理。") : "请完成应用配置和账号授权；可能需要分两次操作。完成后可发消息请 Agent 继续。";
    const continuation=op.continuation;
    if(op.target_available && continuation && (continuation.mode==="inbox" || continuation.state==="handled")) {
      const reasons={member_paused:"Agent 已暂停",member_failed:"上次回合失败，需恢复处理",inbox_admission_blocked:"前方通知的来源核验受阻",budget_exhausted:"Agent 执行预算已用完",backend_blocked:"Agent Session 需要恢复",checkpoint_failed:"Agent 检查点需要恢复",session_rotating:"Agent 正在轮换 Session",target_unavailable:"Agent 暂不可用",target_changed:"原 Agent 已移除或替换",inbox_capacity:"Inbox 容量不足",inbox_busy:"Inbox 正忙",inbox_delivery_unconfirmed:"交付结果待核实"};
      if(continuation.state==="handled") card.hint.textContent="Agent 已核对此操作结果；后续需求以对话中的处理结果为准。";
      else if(terminal) card.hint.textContent=`${success ? "连接已保存" : "操作结果已保存"}，${continuation.state==="consumed" ? "Agent 已读取，等待接续处理" : "等待 Agent 接续"}。${continuation.blocked_reason ? `${reasons[continuation.blocked_reason] || "接续暂受阻"}。` : ""}可发消息请 Agent 继续。`;
      else card.hint.textContent="请完成应用配置和账号授权；可能需要分两次操作。结果已订阅，完成后将经 Inbox 自动接续。";
    }
    if(gitlab && activeGitLabHint(op))card.hint.textContent=`请在 ${op.origin} 授权${op.account_username ? `账号 ${op.account_username}` : "指定账号"}。${continuation?.mode==="inbox" ? "结果已订阅，将经 Inbox 等待 Agent 接续。" : "完成后可发消息请 Agent 继续。"}`;
    if(card.applicationLink){card.applicationLink.hidden=true;card.applicationLink.removeAttribute("href");}
    if(op.setup && activeGitLabHint(op)) {
      const setup=op.setup;
      try{const u=new URL(setup.applications_url);if(u.protocol==="https:" && u.origin===new URL(op.origin).origin && u.pathname==="/-/user_settings/applications"){card.applicationLink.href=u.href;card.applicationLink.hidden=false;}}catch{}
      card.hint.textContent=`实例：${setup.origin}。官方应用管理：${setup.applications_url}。回调地址：${setup.redirect_uri || "请先配置服务地址"}。所需 scopes：${setup.scopes.join("、")}。请在完整设置填写应用凭据并授权；关闭设置后仍可继续原操作。${setup.applications.some(a=>!a.compatible) ? "已有应用不兼容，请在完整设置核对回调和权限。" : ""}${continuation?.mode==="inbox" ? "结果已订阅，将经 Inbox 接续。" : "完成后可主动继续。"}`;
    }
    if (!gitlab && op.intent === "authorize") {
      const rooms=new Set((op.affected || []).flatMap(a=>(a.referenced_rooms || []).map(r=>`${r.room_id}:${r.room_instance}`)));
      card.hint.textContent += ` 本次影响 ${(op.affected || []).length} 个共享连接、${rooms.size} 个会话；只恢复原账号，不修改成员许可。`;
      card.link.textContent="补全授权 ↗";
    }
    if (!gitlab && op.permissions) {
      const labels={"chat.read":"会话读取","message.read":"消息读取","notification.private":"私聊通知","notification.group":"群通知","token.renew":"自动续期","sender.name":"发送者姓名","people.search":"人员搜索","message.reply":"普通回复","message.send":"指定群发送","message.send_self":"本人投递"};
      const gaps=op.permissions.capabilities.filter(c=>c.state!=="ready");
      card.hint.textContent += gaps.length ? ` 本次检查尚未就绪：${gaps.map(c=>`${labels[c.name] || c.name}（${c.state==="unsupported" ? "当前服务不支持" : c.next_action==="configure_application" ? "需管理员补权" : c.next_action==="authorize_user" ? "需补全授权" : "暂未确认"}）`).join("、")}。` : " 本次账号条件已核验；成员动作与本人证据仍按各自设置检查。";
      const namespace=op.permissions.account?.id?.namespace || "",appID=namespace.startsWith("lark:open_id:") ? namespace.slice("lark:open_id:".length) : "";
      if(gaps.some(c=>c.next_action==="configure_application") && /^cli_[a-zA-Z0-9_]+$/.test(appID)){card.applicationLink.href=`https://open.feishu.cn/app/${encodeURIComponent(appID)}/auth`;card.applicationLink.hidden=false;}
    }
    // Only fixed official authorization links and this service's image proxy.
    let url="";try{const u=new URL(op.interaction.authorization_url);if(u.protocol==="https:" && ["open.feishu.cn","accounts.feishu.cn","passport.feishu.cn"].includes(u.hostname))url=u.href;}catch{}
    const active=!terminal && op.status!=="cancelling" && op.target_available;
    card.link.hidden=!active || (!gitlab && !url);card.link.textContent=gitlab ? op.setup ? "配置 GitLab 应用" : "授权 GitLab 账号 ↗" : op.stage==="application" ? "配置飞书应用 ↗" : op.intent === "authorize" ? "补全授权 ↗" : "授权飞书账号 ↗";
    if(active && gitlab)card.link.href="#";else if(active && url) {if(card.link.href!==url)card.link.href=url;}else card.link.removeAttribute("href");
    card.link.setAttribute("aria-disabled",platformInteractionState.busy.has(op.operation_id) ? "true" : "false");
    const qr=op.interaction.qrcode_url || "";
    const showQR=active && qr.startsWith("/v1/platforms/lark/qrcode?");
    // One session spans app setup and user authorization. A different image URL
    // forces a fresh read when its QR changes, even if polling skipped the gap.
    const imageURL=`${qr}&stage=${encodeURIComponent(op.stage)}`;
    if(showQR){if(card.qr.getAttribute("src")!==imageURL){card.qr.src=imageURL;card.qr.hidden=false;}}else{card.qr.hidden=true;card.qr.removeAttribute("src");}
    card.cancel.hidden=terminal;
    card.cancel.disabled=platformInteractionState.busy.has(op.operation_id) || op.status==="cancelling";
    card.feedback.textContent=platformInteractionState.errors.get(op.operation_id) || "";
  }

  function openLarkAuthorization(card) {
    if(platformInteractionState.busy.has(card.op.operation_id) || card.link.hidden)return;
    rememberAuthorization(card.op);
    let popup;
    try {
      popup=window.open?.("about:blank","_blank");
      if(popup && !popup.closed){popup.opener=null;popup.location.replace(card.link.href);return;}
    }catch{popup?.close();}
    window.location?.assign(card.link.href);
  }

  function activeGitLabHint(op) {return op.target_available && ["waiting","running"].includes(op.status);}

  async function authorizeGitLabInteraction(card, application={}, preparedPopup) {
    const s=platformInteractionState,op=card.op,id=op.operation_id;
    if(s.busy.has(id) || !activeGitLabHint(op))return;
    const generation=s.generation,info=state.info,operator=state.operator;
    let popup=preparedPopup;try{if(popup===undefined)popup=window.open?.("about:blank","_blank");}catch{popup=null;}if(popup)popup.opener=null;
    rememberAuthorization(op);
    s.busy.add(id);s.errors.delete(id);renderPlatformInteractions();
    try {
      const receipt=await request("/v1/platforms/gitlab/connect",{...info,caller:{kind:"human",id:operator},operation_id:id,version:op.version,...application},undefined,5000);
      if(generation!==s.generation){popup?.close();return;}
      const target=new URL(receipt.authorization_url);
      if(target.origin!==new URL(op.origin).origin || target.pathname!=="/oauth/authorize" || target.protocol!=="https:" || target.username || target.password)throw new Error("invalid authorization target");
      if(popup && !popup.closed)popup.location.replace(target.href);
      else window.location?.assign(target.href);
    } catch(error) {
      popup?.close();
      if(generation===s.generation)s.errors.set(id,error.code==="identity" ? error.field==="browser_session" ? "请使用最初发起授权的浏览器继续，或取消后从新请求重新连接。" : "页面或请求身份校验未通过，请刷新当前服务页面后重试；仍失败时通过完整设置核对原操作。" : "授权入口暂不可用，请核对最新进度或进入完整设置。");
    } finally {if(generation===s.generation){s.busy.delete(id);renderPlatformInteractions();void refreshPlatformInteractions();}}
  }

  async function openGitLabInteractionSettings(card) {
    const s=platformInteractionState,generation=s.generation,op=card.op,id=op.operation_id;
    if(!op.setup || !activeGitLabHint(op) || s.busy.has(id))return;
    s.busy.add(id);
    try {
      await settingsUI.openSettings("platforms");
      if(generation!==s.generation)return;
      await connectionsUI.editPlatform(null,{fromSource:true});
      if(generation!==s.generation)return;
      connectionsUI.platforms.connectOperation={...op,service:structuredClone(state.info)};
      $("connection-name").value=op.name;$("connection-url").value=op.origin;
      gitlabUI.setGitLabScopes(["api"]);
      await gitlabUI.discoverGitLabSetup();
      gitlabUI.renderGitLabAccount();
    } finally {if(generation===s.generation){s.busy.delete(id);renderPlatformInteractions();}}
  }

  function renderPlatformInteractions() {
    const s=platformInteractionState, visible=new Set();
    for(const op of s.operations) {
      const message=state.messages.get(op.source_seq), host=conversationUI.interactionHost(op.source_seq);
      if(host && message?.author.kind==="human" && message.author.id===state.operator) {
        let card=s.views.get(op.operation_id);if(!card){card=connectionInteractionCard(op);s.views.set(op.operation_id,card);}
        updatePlatformInteraction(card,op);if(card.el.parentElement!==host)host.append(card.el);visible.add(op.operation_id);
      }
    }
    for(const [id,card] of s.views) if(!visible.has(id)){card.el.remove();s.views.delete(id);}
  }

  async function refreshPlatformInteractions() {
    const s=platformInteractionState;if(!state.managed || !state.info || s.loading)return;
    const generation=s.generation,info=state.info,operator=state.operator;
    s.loading=true;
    try{
      const data=await request("/v1/platforms/interactions/list",{...info,caller:{kind:"human",id:operator}},undefined,5000);
      if(generation!==s.generation)return;
      // Do not let a list read begun before cancellation replace its newer receipt.
      s.operations=[...data.interactions,...(data.selections || [])].map(op=>{const old=s.operations.find(old=>old.operation_id===op.operation_id);return old && old.version>op.version ? old : op;});
      notice("platform-interaction-error","");renderPlatformInteractions();
      await revealAuthorizationReturn(generation);
    }catch{
      if(generation!==s.generation)return;
      // Invalidate live links on a failed observation; retain the last receipt.
      for(const op of s.operations)op.interaction={id:op.interaction.id,kind:op.interaction.kind};
      renderPlatformInteractions();notice("platform-interaction-error","连接进度暂时无法读取，正在重试。可发消息请 Agent 查询原操作。");
    }finally{if(generation===s.generation)s.loading=false;}
  }

  async function cancelPlatformInteraction(card) {
    const s=platformInteractionState,op=card.op,id=op.operation_id;
    if(s.busy.has(id) || card.cancel.disabled)return;
    const generation=s.generation,payload={...state.info,caller:{kind:"human",id:state.operator},operation_id:id,version:op.version};
    s.busy.add(id);s.errors.delete(id);renderPlatformInteractions();
    try{
      const receipt=await request("/v1/platforms/interactions/cancel",payload,undefined,5000);
      if(generation!==s.generation)return;
      s.operations=s.operations.map(old=>old.operation_id===id && old.version<=receipt.version ? receipt : old);
    }catch(error){if(generation===s.generation)s.errors.set(id,error.code==="conflict" ? "进度已变化，请核对最新状态后再操作。" : "取消结果未确认，正在读取原操作；已发生的配置与授权不会撤销。");}
    finally{if(generation===s.generation){s.busy.delete(id);renderPlatformInteractions();void refreshPlatformInteractions();}}
  }

  // Fixed account/group forms share the durable selection draft. Local dirty
  // input retains its base version until submission or an explicit conflict reload.
  function selectionInteractionCard(op) {
    const el=document.createElement("section");el.className="platform-connect-interaction platform-selection";el.dataset.interactionId=op.operation_id;
    const heading=document.createElement("strong"),status=document.createElement("p"),fields=document.createElement("div"),candidates=document.createElement("div"),feedback=document.createElement("p"),actions=document.createElement("div"),hint=document.createElement("p");
    fields.className="platform-selection-fields";candidates.className="platform-selection-candidates";actions.className="platform-connect-actions";status.setAttribute("role","status");feedback.setAttribute("role","status");hint.className="quiet";
    const input=(label,type="text")=>{const host=document.createElement("label"),title=document.createElement("span"),field=document.createElement("input");title.textContent=label;field.type=type;field.className="field-control";host.append(title,field);fields.append(host);return field;};
    const account=op.input.kind==="account",gitlab=op.input.provider==="gitlab",project=op.input.kind==="projects",name=account && !gitlab ? input("新连接名称") : null,query=account ? null : input(project ? "搜索项目" : "搜索群聊");
    const search=document.createElement("button");search.type="button";search.textContent="搜索";search.className="text-button";search.hidden=account;
    const more=document.createElement("button");more.type="button";more.textContent="加载更多";more.className="text-button";more.hidden=true;
    const mode=document.createElement("select");mode.className="field-control";mode.setAttribute("aria-label",project ? "项目活动筛选" : "群消息范围");
    for(const [value,label] of (project ? [["","保留现有筛选"],["all","全部活动"],["note","评论"],["merge_request.opened","合并请求创建"],["merge_request.merged","合并请求合并"],["merge_request.closed","合并请求关闭"],["push","推送"]] : [["","保留现有消息范围"],["all","全部群消息"],["mentions","仅提及我的消息"]])){const item=document.createElement("option");item.value=value;item.textContent=label;mode.append(item);}
    if(!account){const label=document.createElement("label"),text=document.createElement("span");text.textContent=project ? "项目活动筛选" : "群消息范围";label.append(text,mode);fields.append(label);}
    const selected=document.createElement("p");selected.className="quiet";
    const submit=document.createElement("button");submit.type="button";submit.textContent="提交选择";submit.className="primary";
    const cancel=document.createElement("button");cancel.type="button";cancel.className="text-button";cancel.textContent="取消选择";
    const reload=document.createElement("button");reload.type="button";reload.className="text-button";reload.textContent="载入最新草稿";reload.hidden=true;
    const settings=document.createElement("button");settings.type="button";settings.className="text-button";settings.textContent="完整设置";settings.addEventListener("click",()=>settingsUI.openSettings("platforms"));
    actions.append(submit,cancel,reload,settings);el.append(heading,status,fields,search,candidates,more,selected,actions,hint,feedback);
    const card={el,op,heading,status,fields,candidates,feedback,actions,hint,name,query,search,more,mode,selected,submit,cancel,reload,dirty:false,editVersion:0,draft:null,rows:new Map(),searchGeneration:0,searchBusy:false,queryValue:"",pageToken:"",loaded:false};
    const dirty=()=>{card.dirty=true;};
    name?.addEventListener("input",()=>{card.draft.name=name.value;card.draft.platform_id="";card.draft.platform_version=0;dirty();updateSelectionInteraction(card,card.op);});
    mode.addEventListener("change",()=>{if(project){if(mode.value)card.draft.events_filter={types:mode.value==="all" ? [] : [mode.value]};else delete card.draft.events_filter;}else card.draft.group_message_mode=mode.value;dirty();});
    search.addEventListener("click",()=>loadSelectionCandidates(card,false));more.addEventListener("click",()=>loadSelectionCandidates(card,true));
    query?.addEventListener("keydown",event=>{if(event.key==="Enter"){event.preventDefault();void loadSelectionCandidates(card,false);}});
    reload.addEventListener("click",()=>{card.dirty=false;card.editVersion=0;updateSelectionInteraction(card,card.op);void loadSelectionCandidates(card,false);});
    submit.addEventListener("click",()=>submitSelectionInteraction(card));cancel.addEventListener("click",()=>submitSelectionInteraction(card,true));
    return card;
  }

  function updateSelectionInteraction(card,op) {
    card.op=op;const waiting=op.status==="waiting",account=op.input.kind==="account",busy=platformInteractionState.busy.has(op.operation_id);
    if(!card.editVersion || (!card.dirty && card.editVersion!==op.version)) {
      card.draft=structuredClone(op.draft);card.editVersion=op.version;
      if(card.name && card.name.value!==(op.draft.name || ""))card.name.value=op.draft.name || "";
      if(!card.loaded && card.query)card.query.value=op.input.query || "";
      if(op.input.kind==="projects") {
        const filter=op.draft.events_filter,types=filter?.types || [];
        // Keep a multi-type natural-language draft intact unless explicitly edited.
        card.mode.value=filter ? (types.length===0 ? "all" : types.length===1 ? types[0] : "") : "";
      } else card.mode.value=op.draft.group_message_mode || "";
    }
    const project=op.input.kind==="projects",gitlab=op.input.provider==="gitlab";
    card.heading.textContent=account ? (gitlab ? "选择 GitLab 账号" : "选择飞书账号") : project ? "选择关注的项目" : "选择关注的群聊";
    card.status.textContent=({waiting:"请选择后提交",completed:"选择已保存",cancelled:"选择已取消",expired:"表单已过期"})[op.status] || "结果待核实";
    card.el.dataset.status=op.status;
    card.selected.textContent=account ? (card.draft.platform_id ? `已选连接：${card.draft.name || card.draft.platform_id}` : gitlab ? `请选择 ${op.input.origin} 的已核验账号` : "使用新连接，请填写名称") : project ? (card.draft.project_id ? `已选项目：${card.draft.project_path} · #${card.draft.project_id}${card.draft.events_filter ? `；筛选：${card.draft.events_filter.types?.join("、") || "全部活动"}` : ""}` : "请选择一个项目") : `已选 ${card.draft.chat_ids?.length || 0} 个群：${(card.draft.chat_ids || []).map(id=>`${op.chats?.find(c=>c.chat_id===id)?.name || card.rows.get(id)?.chatName || "群聊"} (${id})`).join("、")}`;
    const active=waiting && op.target_available;
    for(const field of [card.name,card.query,card.mode,card.search,card.more,card.submit,card.cancel])if(field)field.disabled=!active || busy;
    for(const [id,row] of card.rows){row.control.disabled=!active || busy;row.control.checked=account ? card.draft.platform_id===id : project ? card.draft.project_id===id : (card.draft.chat_ids || []).includes(id);}
    card.fields.hidden=!waiting;card.candidates.hidden=!waiting;card.search.hidden=account || !waiting;card.more.hidden=!waiting || !card.pageToken;
    card.submit.hidden=!waiting;card.cancel.hidden=!waiting;card.reload.hidden=!active || !(card.dirty && card.editVersion!==op.version);
    const continuation=op.continuation,reasons={member_paused:"Agent 已暂停",member_failed:"上次回合失败，需恢复处理",inbox_admission_blocked:"前方通知来源核验受阻",budget_exhausted:"执行预算已用完",backend_blocked:"Session 需要恢复",checkpoint_failed:"检查点需要恢复",session_rotating:"Session 正在轮换",target_unavailable:"Agent 暂不可用",target_changed:"原 Agent 已替换",inbox_capacity:"Inbox 容量不足",inbox_busy:"Inbox 正忙",inbox_delivery_unconfirmed:"交付结果待核实"};
    card.hint.textContent=!op.target_available ? "原 Agent 已移除或替换，选择不会转交新成员。" : waiting ? "提交后由 Agent 核验并继续处理。可打开完整设置；关闭设置不会取消此选择。" : continuation?.state==="handled" ? "Agent 已核对选择；实际连接和关注结果以对话反馈为准。" : `选择结果已保存，${continuation?.mode==="inbox" ? "等待 Agent 接续" : "可发消息请 Agent 继续"}。${continuation?.blocked_reason ? `${reasons[continuation.blocked_reason] || "接续暂受阻"}。可发消息主动继续。` : ""}`;
    card.feedback.textContent=platformInteractionState.errors.get(op.operation_id) || (card.dirty && card.editVersion!==op.version ? "草稿已在其他位置更新。你的输入仍保留，请核对后载入最新草稿。" : card.candidateFeedback || "");
    if(!card.loaded && active && !busy){card.loaded=true;void loadSelectionCandidates(card,false);}
  }

  async function loadSelectionCandidates(card,append) {
    if(card.searchBusy)return;
    const s=platformInteractionState,generation=s.generation,requestVersion=++card.searchGeneration,query=append ? card.queryValue : (card.query?.value || "");
    card.searchBusy=true;card.search.disabled=true;card.more.disabled=true;
    try {
      const result=await request("/v1/platforms/selection-interactions/candidates",{...state.info,caller:{kind:"human",id:state.operator},operation_id:card.op.operation_id,query,page_token:append ? card.pageToken : ""},undefined,35000);
      if(generation!==s.generation || requestVersion!==card.searchGeneration)return;
      card.queryValue=query;card.pageToken=result.next_page ? String(result.next_page) : result.has_more ? result.page_token : "";
      if(!append){card.rows.clear();card.candidates.replaceChildren();}
      const account=card.op.input.kind==="account",project=card.op.input.kind==="projects",gitlab=card.op.input.provider==="gitlab",rows=account ? (gitlab ? result : [...result,{id:"",name:"连接另一个飞书账号",version:0}]) : project ? result.projects : result.chats;
      if(project && (!Array.isArray(rows) || rows.some(p=>!Number.isSafeInteger(p.id) || p.id<=0 || typeof p.path_with_namespace!=="string")))throw Error("invalid projects");
      for(const item of rows){const id=account || project ? item.id : item.chat_id;if(card.rows.has(id))continue;
        const row=document.createElement("label"),control=document.createElement("input"),text=document.createElement("span");control.type=account || project ? "radio" : "checkbox";control.name=`selection-${card.op.operation_id}`;
        text.textContent=account ? `${item.name}${item.username ? ` · ${item.username}` : ""}${item.id ? ` · ${item.account?.id?.value || item.id}` : ""}` : project ? `${item.name} · ${item.path_with_namespace} · #${item.id}` : `${item.name || id} · ${id}${item.chat_mode==="topic" ? " · 话题群" : ""}`;
        row.append(control,text);card.candidates.append(row);card.rows.set(id,{control,chatName:item.name});
        control.addEventListener("change",()=>{
          card.dirty=true;
          if(account){card.draft.platform_id=id;card.draft.platform_version=item.version;card.draft.name=id ? item.name : (card.name?.value || "");if(id && card.name)card.name.value=item.name;}
          else if(project){card.draft.project_id=id;card.draft.project_path=item.path_with_namespace;}
          else {const selected=new Set(card.draft.chat_ids || []);if(control.checked)selected.add(id);else selected.delete(id);card.draft.chat_ids=[...selected];}
          updateSelectionInteraction(card,card.op);
        });
      }
      card.candidateFeedback=(result.search_warnings || []).map(w=>w.code==="permission_denied" ? "部分搜索权限不足，可补充授权或清空关键词浏览" : w.code==="refine_query" ? "结果较多，请输入更完整的群名" : "部分搜索暂不可用，请重试").join("；") || (!rows.length ? (project ? "暂无候选；可调整搜索，结果只代表当前账号可见项目。" : account ? "没有已核验账号，请先连接指定账号。" : "暂无候选；可调整搜索，结果不代表所有群。") : "");
    }catch(error){if(generation===s.generation){card.candidateFeedback=error.code==="conflict" ? "账号或表单已变化，请 Agent 查询原选择，或在完整设置中核对。" : error.code==="permission_denied" || error.code==="forbidden" ? "当前账号权限不足，请核对账号授权或进入完整设置；未替换选择。" : "候选读取失败，请重试搜索或进入完整设置。";}}
    finally{if(generation===s.generation){card.searchBusy=false;updateSelectionInteraction(card,card.op);}}
  }

  async function submitSelectionInteraction(card,cancel=false) {
    const s=platformInteractionState,id=card.op.operation_id;if(s.busy.has(id))return;
    const generation=s.generation,identity={...state.info,caller:{kind:"human",id:state.operator},operation_id:id};
    let version=card.editVersion;
    s.busy.add(id);s.errors.delete(id);renderPlatformInteractions();
    try {
      if(!cancel && card.dirty){const updated=await request("/v1/platforms/selection-interactions/update",{...identity,version,draft:structuredClone(card.draft)},undefined,5000);version=updated.version;card.editVersion=version;card.dirty=false;}
      const receipt=await request(`/v1/platforms/selection-interactions/${cancel ? "cancel" : "submit"}`,{...identity,version},undefined,40000);
      if(generation!==s.generation)return;
      card.dirty=false;s.operations=s.operations.map(op=>op.operation_id===id && op.version<=receipt.version ? receipt : op);
    }catch(error){if(generation===s.generation)s.errors.set(id,error.code==="conflict" ? "草稿、账号或成员已变化；请核对最新草稿或请 Agent 查询原选择。" : "提交结果未确认，正在查询原选择。请核对结果后继续。");}
    finally{if(generation===s.generation){s.busy.delete(id);renderPlatformInteractions();void refreshPlatformInteractions();}}
  }

  function mount() {

  }

  return {
    mount,
    restoreAuthorizationSelection,
    renderPlatformInteractions,
    resetPlatformInteractions,
    refreshPlatformInteractions,
    platformInteractionState,
    authorizeGitLabInteraction
  };
}
