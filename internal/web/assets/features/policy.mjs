// Natural-language policies, followups and their source details.
export function createPolicy({ env, membersUI, runtime, settingsUI, conversationUI }) {
  const { document, renderMessageBody } = env;
  const { state, $, rpc } = runtime;

  let policyView = null, policyEditor = null;

  const policyStates = {suggested:"建议 · 未生效", active:"有效", revoked:"已撤销"};

  function policyCurrent(view) {
    return policyView === view && membersUI.agentTab === "policy" && $("activity-dialog").open && membersUI.selectedAgent()?.membership_id === view.member.membership_id && state.info?.room_id === view.info.room_id && state.info?.room_instance === view.info.room_instance && state.info?.service_epoch === view.info.service_epoch;
  }

  function editPolicy(record = null) {
    const view = policyView;
    if (!view || !policyCurrent(view) || view.saving || view.loading || !view.data) return;
    view.editVersion=(view.editVersion || 0)+1;
    policyEditor = {view, record, id:record?.id || `p-${Date.now()}-${Math.random().toString(36).slice(2,10)}`};
    renderPolicyEmptyHint();
    $("policy-form").hidden = false;
    $("policy-editor-title").textContent = record ? "修改规则" : "新增规则";
    $("policy-text").value = record?.text || "";
    settingsUI.fillTimeInput("policy-expires",record?.expires_at);
    $("policy-text").focus();
  }

  function policyDateText(value) {
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? value : settingsUI.formatDate(date, {year:"numeric",month:"2-digit",day:"2-digit",hour:"2-digit",minute:"2-digit"});
  }

  function policyMetadata(record) {
    const meta = document.createElement("div"); meta.className = "policy-entry-meta";
    if (record.expires_at) {
      const expires = document.createElement("p"); settingsUI.bindSystemTime(expires, () => { expires.textContent = `有效期至：${policyDateText(record.expires_at)}`; }); meta.append(expires);
    }
    return meta;
  }

  function policyCard(view, r, history) {
    const section = document.createElement("section"); section.className = "activity-section policy-entry";section.dataset.policyId=r.id;
    const header = document.createElement("div"); header.className = "policy-entry-header";
    const title = document.createElement("h4"), status = document.createElement("span");
    const expired = r.expires_at && new Date(r.expires_at) <= new Date();
    status.className = "policy-entry-state"; status.dataset.status = expired ? "expired" : r.status;
    status.textContent = expired ? `${policyStates[r.status]} · 已过期` : policyStates[r.status]; title.append(status);
    const body = document.createElement("p"); body.className = "inbox-text policy-entry-body"; body.textContent = r.text;
    const source = document.createElement("details"), summary = document.createElement("summary"); source.className = "policy-history";
    summary.textContent = "来源与历史";
    summary.title = `当前版本 v${r.version}`;
    const identity = document.createElement("p"); identity.textContent = `规则 ID：${r.id} · 当前版本 v${r.version}`;
    source.append(summary, identity);
    for (const v of history.filter(v => v.id === r.id)) {
      const revision = document.createElement("article"); revision.className = "policy-revision";
      const heading = document.createElement("h5"); heading.textContent = `v${v.version} · ${policyStates[v.status]}`;
      const metadata = document.createElement("p"); metadata.className = "policy-revision-meta";
      const time = document.createElement("time"); time.dateTime = v.updated_at; settingsUI.bindSystemTime(time, () => { time.title = settingsUI.formatDate(v.updated_at); time.textContent = policyDateText(v.updated_at); });
      const origin = document.createElement("span");
      origin.textContent = ` · 操作者 ${v.changed_by.id}\n${v.source.seq ? "会话消息 #"+v.source.seq : "成员设置"} · 来源用户 ${v.source.user}`;
      metadata.append(time, origin);
      const content = document.createElement("p"); content.className = "inbox-text policy-revision-body"; content.textContent = v.text;
      const raw = document.createElement("details"), rawTitle = document.createElement("summary"); raw.className = "policy-source"; rawTitle.textContent = "原始来源";
      const original = document.createElement("p"); original.className = "inbox-text";
      original.textContent = v.source.text;
      raw.append(rawTitle, original);
      revision.append(heading, metadata, content, policyMetadata(v), raw);
      if (v.conversation) {
        const basis = document.createElement("p"); basis.className = "inbox-text";
        basis.textContent = `Agent 理解：${v.conversation.basis}`; revision.append(basis);
      }
      source.append(revision);
    }
    const actions = document.createElement("div"); actions.className = "member-actions";
    const edit = document.createElement("button"); edit.type = "button"; edit.className = "text-button button-compact"; edit.textContent = r.status === "suggested" ? "审核并编辑" : "编辑";
    edit.setAttribute("aria-label", `${r.status === "suggested" ? "审核并编辑" : "编辑"}规则：${r.text}`);
    edit.addEventListener("click",()=> { if (policyCurrent(view)) editPolicy(r); }); actions.append(edit);
    if (r.status !== "revoked") {
      const revoke = document.createElement("button"); revoke.type = "button"; revoke.className = "text-button button-compact danger-button"; revoke.textContent = "撤销";
      revoke.title = "撤销后不再生效，历史记录仍会保留";
      revoke.setAttribute("aria-label", `撤销规则：${r.text}`);
      revoke.addEventListener("click",()=> { if (policyCurrent(view)) void savePolicyChange(view,{...policyChange(r),status:"revoked",source_text:`撤销规则 ${r.id} v${r.version}：${r.text}`}); }); actions.append(revoke);
    }
    header.append(title, actions); section.append(header, body, policyMetadata(r), source); return section;
  }

  function followupResultURL(raw) { try { const url=new URL(raw);return ["https:","http:"].includes(url.protocol) && !url.username && !url.password ? url.href : ""; } catch { return ""; } }

  function followupParagraph(parent,text) { const p=document.createElement("p");p.textContent=text;parent.append(p); }

  const followupStates = {pending:"待处理",in_progress:"办理中",waiting_user:"待你答复",waiting_followup:"等待后续",completed:"已完成",archived:"已归档",unknown:"结果待核实"};

  const sourceStates = {resolved:"已关联到此对象",partial:"部分信息已核实",unavailable:"暂无法核实",unsupported:"暂不支持解析",mismatch:"身份不符",permission_denied:"无权核实"};

  function followupsCurrent(view, request = view.followupRequest) {
    return policyCurrent(view) && $("policy-followups").open && view.followupRequest === request;
  }

  function followupDisclosure(parent, title, className = "") {
    const detail=document.createElement("details"), summary=document.createElement("summary");
    detail.className=className; summary.textContent=title; detail.append(summary); parent.append(detail); return detail;
  }

  async function loadFollowupDetail(view,item,section) {
    const request=view.followupRequest;
    let panel=section.followupPanel;
    if(!panel) { panel=document.createElement("div");panel.className="followup-detail";section.followupPanel=panel;section.append(panel); }
    panel.replaceChildren(); section.followupLoading=true;
    try {
      if(item.kind === "unresolved_source") {
        const event=await rpc("/v1/matters/source",{...view.member,followup:{seq:item.seq,source_ref:item.ref}},view.info);
        if(!followupsCurrent(view,request)) return;
        renderFollowupSource(panel,event);
        section.followupLoaded=true;
        return;
      }
      const data=await rpc("/v1/matters/read",{...view.member,followup:{object_ref:item.object_ref,unit_ref:item.ref}},view.info);
      if(!followupsCurrent(view,request)) return;
      const unit=data.unit,target=data.target.target;
      const progress=document.createElement("section");progress.className="followup-progress";
      const heading=document.createElement("h4");heading.textContent="处理进展";progress.append(heading);panel.append(progress);
      followupParagraph(progress,`办理状态：${followupStates[unit.status] || unit.status}`);
      if(unit.summary) followupParagraph(progress,unit.summary);
      if(unit.next) followupParagraph(progress,`下一步：${unit.next}`);
      if(!unit.summary && !unit.next) followupParagraph(progress,"暂无进展摘要或下一步记录。");
      if(unit.evidence) followupParagraph(progress,`进展依据：${unit.evidence}`);
      for(const question of unit.questions || []) {
        followupParagraph(progress,`${question.answers?.length ? "已答问题" : "待你答复"}：${question.text}`);
        for(const answer of question.answers || []) followupParagraph(progress,`用户 ${answer.message.author.id} #${answer.message.seq}：${answer.message.text}`);
        if(question.room_seq && !question.answers?.length) { const reply=document.createElement("button");reply.type="button";reply.className="text-button";reply.textContent="在会话中回复";reply.addEventListener("click",()=>{ if(!followupsCurrent(view,request))return; const message=state.messages.get(question.room_seq); if(message) { conversationUI.beginReply(message); $("activity-dialog").close(); } else { followupParagraph(progress,`请在会话中加载消息 #${question.room_seq} 并就地回复。`); } });progress.append(reply); }
      }
      const instructionStates={active:"有效",fulfilled:"已落实",revoked:"已撤销",expired:"已过期"};
      for(const instruction of unit.instructions || []) followupParagraph(progress,`单次指令 · ${instructionStates[instruction.status] || instruction.status} · ${instruction.intent.body || instruction.intent.constraints} · 用户 #${instruction.source.seq}：${instruction.source.text}`);
      for(const action of unit.actions || []) {
        const actions={"object.comment":"发表评论","discussion.reply":"回复讨论","message.reply":"回复消息","message.send":"发送消息"}, results={succeeded:"已成功",not_applied:"未执行",unknown:"结果待核实"};
        followupParagraph(progress,`${actions[action.request.operation] || action.request.operation} · ${results[action.status] || action.status} · ${action.attempts} 次尝试${action.possible_duplicate ? " · 可能重复" : ""}`);
        followupParagraph(progress,`账号 ${action.inspection.account_label || action.access.account.id.value} · ${action.request.input.body} · ${action.result?.message || "结果待核实"}`);
        if(followupResultURL(action.result?.url)) { const link=document.createElement("a");link.textContent="查看远端回执";link.href=followupResultURL(action.result.url);link.target="_blank";link.rel="noopener noreferrer";progress.append(link); }
      }
      const sources=followupDisclosure(panel,"相关通知","followup-sources");
      const body=document.createElement("div");sources.append(body);
      sources.addEventListener("toggle",async()=>{
        if(!sources.open || sources.loaded || sources.loading || !followupsCurrent(view,request))return;
        sources.loading=true;body.replaceChildren();
        try { await loadFollowupSources(view,item.object_ref,body,0,request);if(followupsCurrent(view,request))sources.loaded=true; }
        catch(error) { if(followupsCurrent(view,request))followupParagraph(body,`读取失败：${error.message}。收起后展开可重试。`); }
        finally { sources.loading=false; }
      });
      const technical=followupDisclosure(panel,"技术信息","followup-technical");
      followupParagraph(technical,`${target.provider} · ${target.instance} · ${target.object.type} 原生 ID ${target.object.id.value} · 跟进版本 ${unit.revision}`);
      for(const action of unit.actions || []) followupParagraph(technical,`动作 ID：${action.request.id}`);
      section.followupLoaded=true;
    } catch(error) { if(followupsCurrent(view,request)) followupParagraph(panel,`读取失败：${error.message}。收起后展开可重试。`); }
    finally { section.followupLoading=false; }
  }

  function renderFollowupSource(panel,event) {
    const resolution=event.last_attempt?.resolution || event.resolution;
    if(resolution.reason) followupParagraph(panel,resolution.reason);
    const body=document.createElement("div");body.className="inbox-text";
    renderMessageBody(body,event.entry_status === "expired" ? "原通知正文已过期，跟进与动作记录继续保留。需要当前内容时可按需读取平台。" : event.entry?.item?.text || "没有通知正文。");panel.append(body);
    if(followupResultURL(event.source?.url)) { const link=document.createElement("a");link.textContent="打开来源";link.href=followupResultURL(event.source.url);link.target="_blank";link.rel="noopener noreferrer";panel.append(link); }
    const technical=followupDisclosure(panel,"技术信息","followup-technical");
    followupParagraph(technical,`投递 ID：${event.delivery_id} · 解析状态：${resolution.status}`);
  }

  async function loadFollowupSources(view,objectRef,panel,after,request) {
    const page=await rpc("/v1/matters/sources",{...view.member,followup:{object_ref:objectRef},after,limit:5},view.info);
    if(!followupsCurrent(view,request))return;
    if(!page.items.length && !after)followupParagraph(panel,"暂无已关联通知。");
    for(const source of page.items) {
      const detail=followupDisclosure(panel,`收件箱 #${source.seq} · ${sourceStates[source.status] || source.status}`,"followup-source");
      const body=document.createElement("div");detail.append(body);
      detail.addEventListener("toggle",async()=>{
        if(!detail.open || detail.loaded || detail.loading || !followupsCurrent(view,request))return;
        detail.loading=true;body.replaceChildren();
        try { const event=await rpc("/v1/matters/source",{...view.member,followup:{seq:source.seq,source_ref:source.source_ref}},view.info);if(followupsCurrent(view,request)){renderFollowupSource(body,event);detail.loaded=true;} }
        catch(error) { if(followupsCurrent(view,request))followupParagraph(body,`读取失败：${error.message}。收起后展开可重试。`); }
        finally {detail.loading=false;}
      });
    }
    if(!page.complete) { const more=document.createElement("button");more.type="button";more.className="text-button";more.textContent="更多通知";more.addEventListener("click",async()=>{more.disabled=true;try{await loadFollowupSources(view,objectRef,panel,page.next_after,request);more.hidden=true;}catch(error){more.disabled=false;if(followupsCurrent(view,request))followupParagraph(panel,error.message);}});panel.append(more); }
  }

  function policyChange(record) {
    const {id,version,status,text,expires_at} = record;
    return {id,version,status,text,...(expires_at ? {expires_at} : {}),revision:policyView.data.revision};
  }

  function renderPolicy(view) {
    if (!policyCurrent(view)) return;
    $("policy-entries").replaceChildren(...view.data.current.map(r=>policyCard(view,r,view.data.history)));
    renderPolicyEmptyHint();
    $("policy-status").textContent=`${view.data.effective.length} 条有效`;
  }

  function renderPolicyEmptyHint() {
    $("policy-empty").hidden = !policyView?.data || !policyCurrent(policyView) || policyView.data.current.length > 0 || !!policyEditor;
  }

  function renderPolicyFollowups(view) {
    if(!followupsCurrent(view))return;
    $("policy-matters").replaceChildren(...view.matters.items.map(item => {
      const section=document.createElement("details"); section.className="followup-record";
      const title=document.createElement("summary"), label=document.createElement("span"), status=document.createElement("span");
      label.textContent=`${item.kind === "object" ? "对象跟进" : item.kind === "discussion" ? "讨论跟进" : "通知待核实"}${item.target_label ? " · "+item.target_label : item.kind === "unresolved_source" ? " · 收件箱 #"+item.seq : ""}`;
      status.className="followup-state";
      status.textContent=item.kind === "unresolved_source" ? sourceStates[item.status] || item.status : item.all_completed === false && ["completed","archived"].includes(item.status) ? "有未完成跟进" : followupStates[item.status] || item.status;
      status.dataset.attention=String(item.status === "waiting_user" || item.status === "unknown" || !!item.actions?.length);
      title.append(label,status);section.append(title);
      if(item.summary)followupParagraph(section,item.summary);
      if(item.next)followupParagraph(section,`${item.kind === "unresolved_source" ? "核实说明" : "下一步"}：${item.next}`);
      section.addEventListener("toggle",()=>{if(section.open && !section.followupLoaded && !section.followupLoading && followupsCurrent(view))void loadFollowupDetail(view,item,section);});
      if(item.object_ref) { const related=document.createElement("button");related.type="button";related.className="text-button";related.textContent="查看同对象记录";related.addEventListener("click",()=>{if(followupsCurrent(view))void loadPolicyFollowups(view,"",item.object_ref);});section.append(related); }
      for(const q of item.questions || []) { const p=document.createElement("p");p.className="followup-attention";p.textContent=`待你答复：${q.text}`;section.append(p); }
      for(const action of item.actions || []) { const p=document.createElement("p");p.className="followup-attention";p.textContent=`执行结果待核实 · ${action.attempts} 次尝试`;section.append(p); }
      return section;
    }));
    if(!view.matters.items.length)followupParagraph($("policy-matters"),"暂无跟进记录。");
    $("policy-next").hidden=view.matters.complete;
    $("followups-all").hidden=!view.objectRef;
    $("followups-status").textContent=`${view.objectRef ? "当前对象 · " : ""}${view.matters.total} 条${$("followups-history").checked ? "" : "活跃"}记录`;
  }

  async function loadPolicyFollowups(view, cursor = "", objectRef = view.objectRef) {
    if(!followupsCurrent(view))return;
    const request=++view.followupRequest;
    view.followupsLoading=true;view.followupsLoaded=false;view.objectRef=objectRef;
    $("policy-matters").replaceChildren();$("policy-next").hidden=true;$("followups-status").textContent="正在读取跟进记录…";
    for(const id of ["followups-reload","followups-all","policy-next","followups-history"])$(id).disabled=true;
    try {
      const matters=await rpc("/v1/matters/list",{...view.member,followup:{cursor,object_ref:objectRef,include_completed:$("followups-history").checked},limit:5},view.info);
      if(!followupsCurrent(view,request))return;
      view.matters=matters;view.followupsLoaded=true;renderPolicyFollowups(view);
    } catch(error) { if(followupsCurrent(view,request))$("followups-status").textContent=`读取失败：${error.message}。可刷新记录重试。`; }
    finally { if(followupsCurrent(view,request)){view.followupsLoading=false;for(const id of ["followups-reload","followups-all","policy-next","followups-history"])$(id).disabled=false;} }
  }

  async function loadMemberPolicy(resetFollowups = false) {
    const member=membersUI.selectedAgent(); if (!member || membersUI.agentTab!=="policy" || (policyView?.saving && policyCurrent(policyView))) return;
    const keepOpen=!resetFollowups && policyView && policyCurrent(policyView) && $("policy-followups").open;
    const view={member:{agent_id:member.agent_id,membership_id:member.membership_id},info:{...state.info},objectRef:keepOpen ? policyView.objectRef : "",followupRequest:0,followupsLoaded:false,loading:true};
    $("policy-followups").open=!!keepOpen;
    if(!keepOpen)$("followups-history").checked=false;
    policyView=view; policyEditor=null; for(const id of ["policy-save","policy-suggest","policy-new","policy-retry","policy-next"]) $(id).disabled=false; $("policy-form").hidden=true; $("policy-entries").replaceChildren(); $("policy-matters").replaceChildren(); $("policy-next").hidden=true; $("policy-status").textContent="正在读取…";
    $("policy-retry").hidden=true;$("policy-retry").disabled=true;$("policy-new").disabled=true;
    renderPolicyEmptyHint();
    $("followups-status").textContent="";
    try {
      const data=await rpc("/v1/policy/read",view.member,view.info);
      if (!policyCurrent(view)) return;
      view.data=data;renderPolicy(view);
      if($("policy-followups").open && !view.followupsLoaded && !view.followupsLoading)await loadPolicyFollowups(view);
    } catch(error) { if(policyCurrent(view)){view.readFailed=true;$("policy-retry").hidden=false;$("policy-status").textContent=`读取失败：${error.message}`;} }
    finally {view.loading=false;if(policyCurrent(view)){$("policy-retry").disabled=false;$("policy-new").disabled=!view.data;}}
  }

  function canRefreshPolicy(view) {
    return view && policyCurrent(view) && !view.loading && !view.saving && !view.needsRefresh && !policyEditor && !document.hidden && document.hasFocus() && !$("notification-detail").open && !document.querySelector("dialog:modal");
  }

  async function refreshMemberPolicy() {
    const view=policyView;if(!canRefreshPolicy(view) || view.refreshing)return;
    view.refreshing=true;const editVersion=view.editVersion || 0;
    try {
      const data=await rpc("/v1/policy/read",view.member,view.info);
      if(!canRefreshPolicy(view) || editVersion!==(view.editVersion || 0))return;
      const changed=JSON.stringify(view.data)!==JSON.stringify(data);
      const scrollTop=$("policy-member-view").scrollTop;
      const openHistory=new Set([...$("policy-entries").children].filter(row=>row.children[3]?.open).map(row=>row.dataset.policyId));
      view.data=data;view.readFailed=false;$("policy-retry").hidden=true;$("policy-new").disabled=false;
      if(changed){renderPolicy(view);for(const row of $("policy-entries").children)if(openHistory.has(row.dataset.policyId))row.children[3].open=true;$("policy-member-view").scrollTop=scrollTop;}
      else $("policy-status").textContent=`${data.effective.length} 条有效`;
    } catch(error) {
      if(canRefreshPolicy(view) && editVersion===(view.editVersion || 0)){view.readFailed=true;$("policy-retry").hidden=false;$("policy-status").textContent=`读取失败：${error.message}`;}
    } finally {view.refreshing=false;}
  }

  async function savePolicyChange(view,change) {
    if(!policyCurrent(view) || view.saving || view.needsRefresh || !view.data) return;
    view.saving=true; for(const id of ["policy-save","policy-suggest","policy-new","policy-retry","policy-next"]) $(id).disabled=true;
    $("policy-retry").hidden=true;$("policy-status").textContent="正在保存…";
    try {
      await rpc("/v1/policy/update",{...view.member,policy:change},view.info);
      if(!policyCurrent(view)) return;
      view.saving=false; await loadMemberPolicy();
    } catch(error) { view.needsRefresh=true; if(policyCurrent(view)){$("policy-retry").hidden=false;$("policy-status").textContent=`保存失败或结果待核实：${error.message}。请重试读取，核对当前版本后再修改。`;} }
    finally { view.saving=false; if(policyView===view) for(const id of ["policy-save","policy-suggest","policy-new","policy-retry","policy-next"]) $(id).disabled=(policyView===view && view.needsRefresh && ["policy-save","policy-suggest"].includes(id)) || false; }
  }

  async function savePolicyForm(status) {
    const editor=policyEditor; if(!editor || !policyCurrent(editor.view)) return;
    const c={id:editor.id,version:editor.record?.version || 0,revision:editor.view.data.revision,status,text:$("policy-text").value};
    try { const expires=settingsUI.readTimeInput("policy-expires"); if(expires)c.expires_at=expires; }
    catch(error) { $("policy-status").textContent=error.message; return; }
    c.source_text=`成员设置：${status === "active" ? "保存并生效" : "保存为建议"} · 规则\n${c.text}` + (c.expires_at ? `\n有效期至：${c.expires_at}` : "");
    await savePolicyChange(editor.view,c);
  }

  function mount() {
    $("policy-new").addEventListener("click",()=>editPolicy());

    $("policy-cancel").addEventListener("click",()=>{$("policy-form").hidden=true;policyEditor=null;renderPolicyEmptyHint();void refreshMemberPolicy();});

    $("policy-retry").addEventListener("click",()=>loadMemberPolicy());

    $("policy-followups").addEventListener("toggle",()=>{
      const view=policyView;if(!view || !policyCurrent(view))return;
      if(!$("policy-followups").open){view.followupRequest++;view.followupsLoading=false;view.followupsLoaded=false;$("policy-matters").replaceChildren();$("followups-status").textContent="";$("policy-next").hidden=true;return;}
      if(!view.followupsLoaded && !view.followupsLoading)void loadPolicyFollowups(view);
    });

    $("followups-reload").addEventListener("click",()=>{if(policyView)void loadPolicyFollowups(policyView);});

    $("followups-history").addEventListener("change",()=>{if(policyView)void loadPolicyFollowups(policyView);});

    $("followups-all").addEventListener("click",()=>{if(policyView)void loadPolicyFollowups(policyView,"","");});

    $("policy-next").addEventListener("click",()=>{if(policyView)void loadPolicyFollowups(policyView,policyView.matters?.next || "");});

    $("policy-form").addEventListener("submit",event=>{event.preventDefault();return savePolicyForm("active");});

    $("policy-suggest").addEventListener("click",()=>savePolicyForm("suggested"));
  }

  return {
    mount,
    get policyEditor() { return policyEditor; },
    loadMemberPolicy,
    policyCurrent,
    get policyView() { return policyView; },
    loadPolicyFollowups,
    refreshMemberPolicy
  };
}
