// Scion's first delivery exposes only open Issue creation, using the shared editor.
export function createScionNotifications({env,runtime,workspaceUI,settingsUI,roomsUI,render,scopeKey,sameTarget,beginWrite}) {
  const {state,$,notice}=runtime, {request,document,setTimeout:delay,clearTimeout:clearDelay}=env;
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
  function directory(ws,s) {
    const connection=ws.allPlatforms?.find(p=>p.id===s.draft.platform_id);
    const key=JSON.stringify([state.catalog?.service_epoch,connection?.id,connection?.version,connection?.validation]);
    if(s.directory?.key!==key){clearDelay(s.directory?.timer);s.directory?.controller?.abort();s.directory={key,connection,query:"",repositories:new Map(),loaded:false,loading:false,next:"",error:""};}
    return s.directory;
  }
  function showRepositories(ws,s,busy,locked) {
    const dir=directory(ws,s),ready=dir.connection?.validation?.status==="valid" && ws.notificationsConnected;
    $("scion-repository").hidden=!locked;
    $("scion-repository-picker").hidden=locked;
    $("scion-repository-summary").textContent=s.draft.repository || "选择仓库";
    $("scion-repository-summary").setAttribute("aria-disabled",String(busy || !ready));
    if(locked || !ready)$("scion-repository-picker").open=false;
    if($("scion-repository-search").value!==dir.query)$("scion-repository-search").value=dir.query;
    $("scion-repository-search").disabled=busy || !ready;
    $("scion-repository-options").replaceChildren(...[...dir.repositories.values()].map(repo=>{
      const label=document.createElement("label"),input=document.createElement("input"),text=document.createElement("span"),name=document.createElement("span"),description=document.createElement("small");
      label.className="gitlab-project-option";input.type="radio";input.name="scion-repository";input.value=repo.name;input.checked=s.draft.repository===repo.name;input.disabled=busy || !ready;
      name.textContent=repo.name;description.textContent=repo.description;text.append(name,description);label.append(input,text);
      input.addEventListener("change",()=>{
        if(active()!==ws || ws.scion!==s || directory(ws,s)!==dir || s.busy || s.view.subscription_id)return;
        $("scion-repository").value=repo.name;$("scion-repository-picker").open=false;edit();$("scion-repository-summary").focus();
      });return label;
    }));
    $("scion-repository-status").textContent=!ready ? "请先验证 Scion 连接。" : dir.error || (dir.loading || dir.timer ? "正在读取仓库…" : !dir.loaded ? "展开后加载可访问的仓库。" : `${dir.repositories.size ? `已加载 ${dir.repositories.size} 个匹配仓库。` : "没有匹配的仓库。"}${dir.truncated ? `列表尚未读全，${dir.next ? "可加载更多或" : "请"}输入组织/仓库前缀缩小范围。` : ""}`);
    $("scion-repository-more").hidden=!dir.next;$("scion-repository-more").disabled=busy || !ready || dir.loading || !!dir.timer;
    $("scion-repository-refresh").disabled=busy || !ready || dir.loading || !!dir.timer;
  }
  async function loadRepositories(reset=false) {
    const ws=active(),s=ws?.scion;if(!s || s.busy || roomsUI.managementBusy() || s.view.subscription_id || !ws.notificationsConnected || !$("scion-repository-picker").open)return;
    let dir=directory(ws,s);if(dir.connection?.validation?.status!=="valid")return;
    if(reset)dir=s.directory={...dir,repositories:new Map(),loaded:false,loading:false,next:"",error:""};
    if(dir.loading || dir.timer || dir.loaded && !dir.next)return;
    const token=dir.loaded ? dir.next : dir.readToken || "",epoch=state.catalog?.service_epoch,current=()=>active()===ws && ws.scion===s && directory(ws,s)===dir && state.catalog?.service_epoch===epoch && !s.view.subscription_id;
    const controller=dir.controller=new AbortController();
    dir.loading=true;dir.error="";showRepositories(ws,s,false,false);
    try {
      const page=await request("/v1/platform/scion/repositories",{service_epoch:epoch,...ws.room,caller:{kind:"human",id:ws.operator},platform_id:dir.connection.id,platform_version:dir.connection.version,target:s.draft.target,query:dir.query,page_token:token},undefined,35000,controller.signal);
      if(!current())return;
      if(!Array.isArray(page.repositories) || typeof page.truncated!=="boolean" || page.repositories.some(r=>typeof r.name!=="string" || typeof r.description!=="string"))throw Error("invalid repositories");
      for(const repo of page.repositories)dir.repositories.set(repo.name,repo);
      dir.loaded=true;dir.readToken=token;dir.next=page.next_page_token || "";dir.truncated=page.truncated;
    }catch(error){if(current())dir.error=error.code==="conflict" ? "连接已变化，请重新读取设置后重试。" : "仓库列表读取失败，可刷新重试；已选仓库保留。";}
    finally{dir.loading=false;dir.controller=null;if(current())show();}
  }
  function show() {
    const ws=active(),s=ws?.scion,panel=$("notification-panel-workitems"),visible=ws?.notificationChannel==="workitems" && !!s && s.key===ws.notificationScope && !ws.notificationHistory;
    panel.hidden=!visible;if(!visible)return;
    fill();const locked=!!s.view.subscription_id,busy=!!s.busy || !ws.notificationsConnected || roomsUI.managementBusy();
    showRepositories(ws,s,busy,locked);
    $("scion-repository").disabled=locked || busy;$("scion-backfill").disabled=locked || busy;$("scion-backfill-start").disabled=locked || busy;$("scion-interval").disabled=busy;$("scion-enabled").disabled=busy;
    $("scion-backfill-custom").hidden=locked || $("scion-backfill").value!=="custom";
    $("scion-backfill-start").max=settingsUI.systemTime.local(Date.now());
    $("scion-backfill").hidden=locked;
    $("scion-backfill-locked").hidden=!locked;$("scion-backfill-value").textContent=locked ? s.view.baseline ? settingsUI.formatDate(s.view.baseline) : "首次启用时确定" : "";
    $("scion-verify").disabled=busy || !s.draft.repository;$("scion-verify").hidden=locked;
    $("scion-save").disabled=busy || !valid(s.draft) || (locked && !s.dirty);
    $("scion-reload").disabled=busy;
    $("scion-sync").disabled=busy || !locked || s.dirty || !s.view.config.enabled;
    $("scion-unsaved").textContent=s.dirty ? "有未保存的更改" : "";
    const d=s.view.scion,parts=[];
    if(s.busy)parts.push(s.busy==="verify" ? "正在验证仓库…" : "正在处理…");
    else if(locked)parts.push(!s.view.config.enabled ? "已暂停" : d?.truncated ? "范围不完整" : s.view.status==="backoff" ? "重试中" : d?.last_full ? "同步正常" : "初始化中");
    if(d?.last_full)parts.push(`最近完整读取 ${settingsUI.formatDate(d.last_full)}`);
    if(s.view.pending)parts.push(`待投递 ${s.view.pending}`);
    $("scion-status").textContent=parts.join(" · ");
    $("scion-expiry").textContent=`${s.view.effective_after ? `当前接收 ${settingsUI.formatDate(s.view.effective_after)} 之后的通知。` : "沿用全局过期策略。"}已跳过 ${(s.view.metrics?.expired_skipped || 0)+(d?.expired_skipped || 0)} 条过期通知。`;
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
  function mount(){
    $("scion-repository-summary").addEventListener("click",event=>{const ws=active(),s=ws?.scion;if(!s || s.busy || roomsUI.managementBusy() || !ws.notificationsConnected || directory(ws,s).connection?.validation?.status!=="valid")event.preventDefault();});
    $("scion-repository-picker").addEventListener("toggle",()=>{const ws=active(),s=ws?.scion;if(s && $("scion-repository-picker").open && !directory(ws,s).loaded)void loadRepositories();});
    $("scion-repository-search").addEventListener("input",()=>{
      const ws=active(),s=ws?.scion;if(!s || s.busy || s.view.subscription_id)return;
      const old=directory(ws,s);clearDelay(old.timer);old.controller?.abort();
      const dir=s.directory={...old,query:$("scion-repository-search").value,repositories:new Map(),loaded:false,loading:false,next:"",error:"",timer:null,controller:null};
      dir.timer=delay(()=>{dir.timer=null;if(active()===ws && ws.scion===s && directory(ws,s)===dir)void loadRepositories();},250);
      show();
    });
    $("scion-repository-refresh").addEventListener("click",()=>loadRepositories(true));
    $("scion-repository-more").addEventListener("click",()=>loadRepositories());
    $("scion-repository-picker").addEventListener("keydown",event=>{if(event.key==="Escape"){$("scion-repository-picker").open=false;$("scion-repository-summary").focus();}});
    $("scion-reload").addEventListener("click",reload);for(const id of ["scion-repository","scion-interval","scion-enabled","scion-backfill","scion-backfill-start"])$(id).addEventListener("change",edit);for(const id of ["scion-repository","scion-interval","scion-backfill-start"])$(id).addEventListener("input",edit);$("scion-save").addEventListener("click",()=>change("configure"));$("scion-sync").addEventListener("click",()=>change("sync"));$("scion-verify").addEventListener("click",()=>change("verify"));
  }
  return {select,accept,show,change,mount};
}
