// Scion's first delivery exposes only open Issue creation, using the shared editor.
export function createScionNotifications({env,runtime,workspaceUI,settingsUI,roomsUI,render,scopeKey,sameTarget,beginWrite}) {
  const {state,$,notice}=runtime, {request}=env;
  const active=()=>workspaceUI.workspaceView;
  const configKey=c=>JSON.stringify(c);
  function select(ws,id,target) {
    const key=scopeKey(target,id), saved=ws.scionSources?.find(v=>v.platform_id===id && sameTarget(v.config.target,target));
    const old=ws.scionDrafts?.get(key);
    if(old){ws.scion=old;return;}
    const binding=ws.data?.workspace?.binding;
    const config=saved?.config || {enabled:true,platform_id:id,target:structuredClone(target),repository:binding?.provider==="scion" && binding.platform_id===id ? binding.project : "",object_types:["issue"],interval_seconds:60,backfill_seconds:0};
    ws.scion={key,view:saved || {subscription_id:"",subscription_version:0,config,status:"disabled"},draft:structuredClone(config),dirty:false,error:"",feedback:""};
    (ws.scionDrafts ||= new Map()).set(key,ws.scion);
  }
  function accept(ws) {
    const s=ws.scion;if(!s || s.dirty || s.busy)return;
    const next=ws.scionSources?.find(v=>v.platform_id===s.draft.platform_id && sameTarget(v.config.target,s.draft.target));
    if(next){s.view=next;s.draft=structuredClone(next.config);}
  }
  function fill() {
    const ws=active(),s=ws?.scion;if(!s)return;
    if(s.filled===s.draft)return;
    $("scion-repository").value=s.draft.repository;$("scion-interval").value=s.draft.interval_seconds;$("scion-enabled").checked=s.draft.enabled;
    $("scion-backfill").value=s.draft.backfill_start ? "custom" : String(s.draft.backfill_seconds || 0);
    settingsUI.fillTimeInput("scion-backfill-start",s.draft.backfill_start);s.filled=s.draft;
  }
  function edit() {
    const s=active()?.scion;if(!s || s.busy)return;
    try {
      const mode=$("scion-backfill").value;
      s.draft={...s.draft,enabled:$("scion-enabled").checked,repository:$("scion-repository").value.trim(),interval_seconds:Number($("scion-interval").value),backfill_seconds:mode==="custom" ? 0 : Number(mode)};
      delete s.draft.backfill_start;if(mode==="custom")s.draft.backfill_start=settingsUI.readTimeInput("scion-backfill-start");
      s.error="";s.feedback="";s.dirty=configKey(s.draft)!==configKey(s.view.config);s.filled=s.draft;
    }catch(error){s.error=error.message;s.dirty=true;}
    render();
  }
  function valid(c){return c.repository && c.interval_seconds>=30 && c.interval_seconds<=3600 && (!Object.hasOwn(c,"backfill_start") || Number.isFinite(Date.parse(c.backfill_start)) && Date.parse(c.backfill_start)<=Date.now());}
  function show() {
    const ws=active(),s=ws?.scion,panel=$("notification-panel-workitems"),visible=ws?.notificationChannel==="workitems" && !!s && s.key===ws.notificationScope && !ws.notificationHistory;
    panel.hidden=!visible;if(!visible)return;
    fill();const locked=!!s.view.subscription_id,busy=!!s.busy || !ws.notificationsConnected || roomsUI.managementBusy();
    $("scion-repository").disabled=locked || busy;$("scion-backfill").disabled=locked || busy;$("scion-backfill-start").disabled=locked || busy;$("scion-interval").disabled=busy;$("scion-enabled").disabled=busy;
    $("scion-backfill-custom").hidden=locked || $("scion-backfill").value!=="custom";
    $("scion-backfill-start").max=settingsUI.systemTime.local(Date.now());
    $("scion-backfill-locked").hidden=!locked;$("scion-backfill-locked").textContent=locked ? s.view.baseline ? `接收起点：${settingsUI.formatDate(s.view.baseline)}（已固定）` : "首次启用时确定接收起点" : "";
    $("scion-verify").disabled=busy || !s.draft.repository;$("scion-verify").hidden=locked;
    $("scion-save").disabled=busy || !valid(s.draft) || (locked && !s.dirty);
    $("scion-reload").disabled=busy;
    $("scion-sync").disabled=busy || !locked || s.dirty || !s.view.config.enabled;
    $("scion-unsaved").textContent=s.dirty ? "有未保存的更改" : "";
    const d=s.view.scion,parts=[];
    if(s.busy)parts.push(s.busy==="verify" ? "正在验证仓库…" : "正在处理…");
    else if(locked)parts.push(!s.view.config.enabled ? "已暂停" : d?.truncated ? "范围不完整" : s.view.status==="backoff" ? "重试中" : d?.last_full ? "open 列表同步正常" : "初始化中");
    if(d?.last_full)parts.push(`最近完整读取 ${settingsUI.formatDate(d.last_full)}`);
    if(s.view.pending)parts.push(`待投递 ${s.view.pending}`);
    $("scion-status").textContent=parts.join(" · ");
    $("scion-expiry").textContent=`${s.view.effective_after ? `当前有效接收下界：${settingsUI.formatDate(s.view.effective_after)}。` : "沿用全局过期策略。"}已跳过 ${(s.view.metrics?.expired_skipped || 0)+(d?.expired_skipped || 0)} 条。`;
    const errors={discovery_incomplete:"open 列表未读全，当前范围不完整。",repository_identity_changed:"仓库身份变化，采集已停止，请核查。",capacity:"接收容量已满，进度已保留。",output_limit:"原生响应超过读取上限，当前范围未完成。",query_failed:"查询失败，将重试。",credential_unavailable:"凭据不可用，请重新验证原连接。",binding_changed:"账号身份变化，旧进度未转交新账号。"};
    notice("scion-error",s.error || errors[s.view.error_code] || (s.view.error_code ? `采集需处理：${s.view.error_code}` : ""));
    $("scion-feedback").textContent=s.feedback || "";
  }
  async function change(action,{view,enabled,target}={}) {
    const ws=active(),s=ws?.scion,source=view || s?.view;if(!ws || !source || s?.busy)return;
    const cfg=view ? {...structuredClone(view.config),enabled,target} : structuredClone(s.draft),key=scopeKey(cfg.target,cfg.platform_id),epoch=state.catalog?.service_epoch;
    if(ws.notificationWrites?.has(key))return;
    beginWrite(ws,key);ws.scionRevision=(ws.scionRevision || 0)+1;
    const revision=ws.scionRevision,original=s;
    if(!view){s.busy=action;s.error="";s.feedback="";}render();
    const current=()=>active()===ws && state.catalog?.service_epoch===epoch && ws.scionRevision===revision;
    try {
      const body={service_epoch:epoch,...ws.room,caller:{kind:"human",id:ws.operator},provider:"scion",channel:"workitems",subscription_id:source.subscription_id || "",subscription_version:source.subscription_version || 0};
      if(action==="sync")body.target=source.config.target;else body.config=cfg;
      const next=await request(`/v1/platform/notifications/${action}`,body,undefined,50000);
      if(!current())return;
      if(action==="verify"){if(original===ws.scion && original.key===ws.notificationScope)original.feedback=`仓库已验证：${next.repository.name}`;return;}
      if(next.config.platform_id!==cfg.platform_id || !sameTarget(next.config.target,cfg.target))throw Error("通知目标不匹配");
      ws.scionSources=[...(ws.scionSources || []).filter(v=>!(v.platform_id===cfg.platform_id && sameTarget(v.config.target,cfg.target))),next];
      const draft=ws.scionDrafts?.get(key);
      if(draft && (!view || !draft.dirty)){draft.view=next;draft.draft=structuredClone(next.config);draft.dirty=false;draft.feedback=action==="sync" ? "已请求立即同步" : "已保存";}
    }catch(error){if(current() && original===ws.scion && original?.key===ws.notificationScope)original.error=error.code==="conflict" ? "配置已变化，请重新读取后核对；未覆盖原版本。" : error.message || "结果未确认，请重新读取状态。";}
    finally{ws.notificationWrites?.delete(key);if(original)original.busy="";if(current()){ws.scionRevision++;render();}}
  }
  async function reload(){const ws=active(),s=ws?.scion,epoch=state.catalog?.service_epoch;if(!s || s.busy)return;s.busy="reload";render();try{await runtime.observe("notifications");if(active()!==ws || state.catalog?.service_epoch!==epoch || ws.scion!==s)return;if(!ws.notificationsConnected)throw Error("状态暂不可读，请稍后重试。");const next=ws.scionSources?.find(v=>v.platform_id===s.draft.platform_id && sameTarget(v.config.target,s.draft.target));if(next){s.view=next;s.draft=structuredClone(next.config);}s.error="";s.dirty=false;s.feedback="已重新读取保存状态";}catch(error){s.error=error.message;}finally{s.busy="";render();}}
  function mount(){$("scion-reload").addEventListener("click",reload);for(const id of ["scion-repository","scion-interval","scion-enabled","scion-backfill","scion-backfill-start"])$(id).addEventListener("change",edit);for(const id of ["scion-repository","scion-interval","scion-backfill-start"])$(id).addEventListener("input",edit);$("scion-save").addEventListener("click",()=>change("configure"));$("scion-sync").addEventListener("click",()=>change("sync"));$("scion-verify").addEventListener("click",()=>change("verify"));}
  return {select,accept,show,change,mount};
}
