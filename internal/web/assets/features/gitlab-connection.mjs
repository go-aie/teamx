// GitLab application setup and OAuth authorization lifecycle.
export function createGitLabConnection({ env, connectionsUI, runtime, settingsUI, onboardingUI, interactionsUI }) {
  const { document, window, Option, setTimeout, clearTimeout, request } = env;
  const { state, $, setSelectOptions } = runtime;

  const returnKey="teamx.gitlab-authorization";
  function clearSavedAuthorization() {try{runtime.storage?.removeItem(returnKey);}catch{}}
  function saveAuthorization(auth) {
    const reference={installation_id:state.installationID || "",operator:state.operator,service_epoch:state.catalog?.service_epoch,
      session_id:auth.session_id,platform_id:auth.platform_id || "",onboarding_id:auth.onboarding_id || ""};
    try{runtime.storage?.setItem(returnKey,JSON.stringify(reference));}catch{}
  }
  async function restoreAuthorization() {
    let ref;try{ref=JSON.parse(runtime.storage?.getItem(returnKey) || "null");}catch{}
    if(!ref)return;
    if(ref.installation_id!==(state.installationID || "") || ref.operator!==state.operator || ref.service_epoch!==state.catalog?.service_epoch || typeof ref.session_id!=="string") {clearSavedAuthorization();return;}
    await settingsUI.openSettings("platforms");
    const item=connectionsUI.platforms.items.find(item=>item.id===ref.platform_id);
    await connectionsUI.editPlatform(item);
    connectionsUI.platforms.gitlabAuth={session_id:ref.session_id,platform_id:ref.platform_id,onboarding_id:ref.onboarding_id,affected:[],requestedScopes:["api"]};
    renderGitLabAccount();await pollGitLabAuth();
  }

  const gitlabScopeOptions = [
    {id:"gitlab-scope-api",scope:"api",required:true,defaultSelected:true},
    {id:"gitlab-scope-write-repository",scope:"write_repository",required:false,defaultSelected:true},
  ];

  let gitlabScopeVersion = 0;

  let gitlabEditingApplication = false;

  function renderGitLabReuse() {
    const prepared=connectionsUI.platforms.gitlabSetup, status=prepared?.status, creating=!connectionsUI.platforms.selected;
    $("gitlab-reuse").hidden=(!creating && status!=="application_incompatible") || !prepared || connectionsUI.connectionIsLark() || connectionsUI.connectionIsScion();
    const candidates=prepared?.applications || [], compatible=candidates.filter(c=>c.callback_compatible && !c.missing_scopes?.length);
    const missingScopes=[...new Set(candidates.flatMap(c=>c.missing_scopes || []))];
    const reason=[candidates.some(c=>!c.callback_compatible) ? "回调地址不匹配" : "",missingScopes.length ? `缺少权限 ${missingScopes.join(" + ")}` : ""].filter(Boolean).join("；") || "GitLab 应用配置不兼容";
    $("gitlab-repair-application").hidden=creating || status!=="application_incompatible" || !connectionsUI.platforms.selected?.authorization_status || gitlabEditingApplication;
    $("gitlab-repair-application").disabled=connectionsUI.platforms.sending || !!connectionsUI.platforms.gitlabPending || !!connectionsUI.platforms.gitlabAuth;
    $("gitlab-application-choice").hidden=!creating || status!=="application_selection_required";
    setSelectOptions("gitlab-application",[new Option("选择兼容应用",""),...compatible.map(c=>new Option(`Application ID · ${c.client_id}`,c.application_id))],connectionsUI.platforms.gitlabApplicationID || "");
    $("gitlab-reuse-summary").textContent=status==="ready" ? "复用此实例的已配置应用，新平台的账号需单独授权。" : status==="application_selection_required" ? "此实例有多个兼容应用，请明确选择一个后登录。" : status==="application_incompatible" ? `${reason}。${creating ? "请先到已有平台的「修改应用配置」中更新。" : "请修改应用配置后重新授权。"}` : "此实例尚无兼容应用，请完成下方应用配置并核验所有者。";
    $("gitlab-compatibility-details").hidden=status!=="application_incompatible" || !candidates.length;
    $("gitlab-application-issues").replaceChildren(...(status==="application_incompatible" ? candidates.map(c=>{
      const li=document.createElement("li");li.textContent=`${candidates.length>1 ? `${c.client_id}：` : ""}${[!c.callback_compatible ? `回调地址不匹配（需要 ${prepared.redirect_uri}）` : "",c.missing_scopes?.length ? `缺少权限 ${c.missing_scopes.join(" + ")}` : ""].filter(Boolean).join("；")}`;return li;
    }) : []));
  }

  function renderGitLabAccount() {
    settingsUI.renderSettingsNavigation();
    const item = connectionsUI.platforms.selected, active = !!connectionsUI.platforms.gitlabAuth;
    const pending = !!connectionsUI.platforms.gitlabPending;
    const oauth = item?.credential_kind === "oauth";
    const status = item?.authorization_status;
    const reusable=!item && connectionsUI.platforms.gitlabSetup?.status==="ready";
    const needsChoice=!item && ["application_selection_required","application_incompatible"].includes(connectionsUI.platforms.gitlabSetup?.status);
    const incompatible=connectionsUI.platforms.gitlabSetup?.status==="application_incompatible";
    const editing=(!status && !reusable) || gitlabEditingApplication;
    const origin=$("connection-url").value.trim().replace(/\/$/,"");
    const connectedAccounts=[...new Set(connectionsUI.platforms.items.filter(c=>c.provider==="gitlab" && c.url===origin).map(c=>c.validation?.username).filter(Boolean))];
    $("gitlab-account-choice").hidden=!reusable || pending || active;
    $("gitlab-account-choice-hint").textContent=connectedAccounts.length ? `已有平台账号：${connectedAccounts.map(name=>`@${name}`).join("、")}。新平台请使用另一个账号。` : "请在 GitLab 网页登录本次要连接的账号。";
    $("gitlab-switch-account").href=reusable ? `${origin}/` : "";
    $("gitlab-switch-account").textContent=connectedAccounts.length ? "前往 GitLab 切换账号 ↗" : "前往 GitLab 登录账号 ↗";
    $("gitlab-account-switch-help").hidden=!connectedAccounts.length;
    $("gitlab-app-config-row").hidden=!editing;
    $("gitlab-advanced").open=editing;
    $("gitlab-setup").hidden=!editing;
    $("gitlab-account").textContent = active ? connectionsUI.platforms.gitlabAuth.status === "waiting" ? "等待 GitLab 授权…" : "正在处理授权…" : oauth ? status === "active" ? `已连接 · @${item.validation.username}` : status === "logged_out" ? item.revocation_status === "confirmed" ? "已退出登录" : "本地已断开，远端撤销未确认" : status ? "需要重新授权" : "未连接" : "未连接";
    $("gitlab-login").textContent = status && status !== "logged_out" ? "重新授权" : reusable ? "授权新账号" : onboardingUI.onboarding.active && !$("onboarding-only").checked ? "连接并启用" : "登录 GitLab";
    $("gitlab-login").disabled = active;
    $("connection-validation").hidden = oauth;
    $("connection-notes").hidden = !item || oauth;
    $("gitlab-login").hidden = (!item && !reusable) || pending || editing || incompatible;
    $("gitlab-logout").hidden = !status || status === "logged_out" || pending || editing;
    $("gitlab-logout").disabled = active;
    $("gitlab-edit-application").hidden=!status || editing || pending || incompatible;
    $("gitlab-edit-application").disabled=active;
    $("gitlab-edit-cancel").hidden=!status || !editing || active || pending;
    $("gitlab-cancel").hidden = !active;
    const hasApp=!!status;
    $("gitlab-advanced-title").hidden=true;
    if (!hasApp) {$("gitlab-advanced").open=!reusable;$("gitlab-setup").hidden=reusable;}
    $("gitlab-advanced-title").setAttribute("aria-disabled",String(active || pending || connectionsUI.platforms.sending));
    $("gitlab-import").textContent=hasApp ? "更新并授权" : onboardingUI.onboarding.active && !$("onboarding-only").checked ? "连接并启用" : "连接 GitLab";
    $("gitlab-initial-hint").hidden=hasApp;
    if (!hasApp && !connectionsUI.platforms.gitlabSetup) $("gitlab-setup").hidden=false;
    renderGitLabReuse();onboardingUI.renderOnboarding();
    renderGitLabSave();
    connectionsUI.renderPlatformReferences();
    $("connection-verify").hidden = oauth || !item;
    for (const id of ["connection-name", "connection-url", "connection-provider", "gitlab-import"]) $(id).disabled = active || pending || (id === "connection-provider" && !!item) || (id === "connection-name" && gitlabEditingApplication);
    for (const option of gitlabScopeOptions) {
      $(option.id).disabled=option.required || active || pending;
      if (option.required) $(option.id).checked=true;
    }
    if(connectionsUI.platforms.connectOperation) {
      $("connection-name").readOnly=true;$("connection-url").readOnly=true;$("connection-provider").disabled=true;
      $("connection-title").textContent="继续对话中的 GitLab 连接";
      $("gitlab-import").textContent="核验应用并继续连接";
      $("connection-actions").hidden=true;
    }
    if (needsChoice) $("gitlab-import").disabled=true;
    $("gitlab-client-fields").hidden=needsChoice;
    if(onboardingUI.onboarding.active && onboardingUI.onboarding.pending){$("connection-provider").disabled=true;$("connection-url").readOnly=true;}
  }

  function renderGitLabSave() {
    const item=connectionsUI.platforms.selected, name=$("connection-name").value.trim();
    $("connection-save").textContent=onboardingUI.onboarding.active && item ? "连接并启用" : item ? "保存名称" : "连接并启用";
    $("connection-save").hidden=(!item || name===item.name) && !(onboardingUI.onboarding.active && item);
    const editing=$("gitlab-advanced").open && !$("gitlab-setup").hidden;
    $("connection-delete").hidden=!item || !!item.references?.length || editing;
    $("connection-actions").hidden=$("connection-save").hidden && $("connection-delete").hidden;
    $("connection-save").disabled=connectionsUI.platforms.sending || !!connectionsUI.platforms.gitlabAuth || !!connectionsUI.platforms.gitlabPending || !!item && (!name || name===item.name && !onboardingUI.onboarding.active);
  }

  function selectedGitLabScopes() {
    return gitlabScopeOptions.filter(option=>option.required || $(option.id).checked).map(option=>option.scope);
  }

  function setGitLabScopes(scopes = null) {
    for (const option of gitlabScopeOptions) $(option.id).checked=option.required || (Array.isArray(scopes) ? scopes.includes(option.scope) : option.defaultSelected);
  }

  function gitlabPreparePayload() {
    const item=connectionsUI.platforms.selected;
    // Existing accounts retain their application's scopes unless explicitly edited.
    const scopes=item?.authorization_status && !gitlabEditingApplication ? {} : {scopes:selectedGitLabScopes()};
    return {...(item ? {id:item.id,version:item.version} : {origin:$("connection-url").value.trim().replace(/\/$/,"")}),...scopes,...(!item && connectionsUI.platforms.gitlabApplicationID ? {application_id:connectionsUI.platforms.gitlabApplicationID} : {})};
  }

  function gitlabSetupFeedback(message = "", kind = "status") {
    const feedback=$("gitlab-setup-feedback");
    feedback.textContent=message;feedback.hidden=!message;feedback.dataset.kind=kind;
    feedback.setAttribute("role",kind === "error" ? "alert" : "status");
  }

  async function prepareGitLabPlatform(editApplication = false) {
    const version = connectionsUI.platforms.formVersion, lookup=++connectionsUI.platforms.gitlabSetupVersion, scopeVersion=gitlabScopeVersion;
    const wasOpen=$("gitlab-advanced").open;
    const source=gitlabPreparePayload();
    let result;
    try {result = await request("/v1/platforms/gitlab/prepare", connectionsUI.connectionPayload({...source,...(editApplication ? {edit_application:true} : {})}), undefined, 50000);}
    catch (error) {if (version !== connectionsUI.platforms.formVersion || lookup !== connectionsUI.platforms.gitlabSetupVersion) return null;throw error;}
    if (version !== connectionsUI.platforms.formVersion || lookup !== connectionsUI.platforms.gitlabSetupVersion || connectionsUI.connectionIsLark() || connectionsUI.connectionIsScion() || source.origin !== gitlabPreparePayload().origin) return null;
    if (!editApplication && connectionsUI.platforms.selected?.authorization_status && !gitlabEditingApplication && scopeVersion === gitlabScopeVersion && result.scopes) setGitLabScopes(result.scopes);
    if (!editApplication && connectionsUI.platforms.gitlabEditingImportID === null) {
      $("gitlab-setup").hidden = !!connectionsUI.platforms.selected?.authorization_status;
      if ($("gitlab-advanced").open === wasOpen) $("gitlab-advanced").open=!$("gitlab-setup").hidden;
      if (!$("gitlab-setup").hidden) {
        connectionsUI.platforms.gitlabEditingImportID=result.client_import_id || "";
        if (scopeVersion === gitlabScopeVersion) setGitLabScopes(result.scopes || source.scopes);
        if (!$("gitlab-client-id").value) $("gitlab-client-id").value=result.client_id || "";
      }
    }
    $("gitlab-redirect-uri").value=result.redirect_uri;
    $("gitlab-applications-link").href=result.applications_url;
    connectionsUI.platforms.gitlabSetup = result;
    connectionsUI.renderPlatformReferences();
    return result;
  }

  async function discoverGitLabSetup() {
    if (connectionsUI.connectionIsLark() || connectionsUI.connectionIsScion() || !$("connection-url").value.trim()) return;
    const version=connectionsUI.platforms.formVersion, origin=$("connection-url").value;
    try {if (await prepareGitLabPlatform()) {gitlabSetupFeedback();renderGitLabAccount();}}
    catch {if(version === connectionsUI.platforms.formVersion && origin === $("connection-url").value) gitlabSetupFeedback("读取失败，请核对实例地址后重新保存。", "error");}
  }

  async function requestGitLabApplicationEdit() {
    if (connectionsUI.platforms.sending || connectionsUI.platforms.gitlabAuth || connectionsUI.platforms.gitlabPending || gitlabEditingApplication) return;
    if (connectionsUI.connectionDirty()) {connectionsUI.connectionFeedback("请先保存名称，再修改应用配置。","error");return;}
    const version=connectionsUI.platforms.formVersion;
    connectionsUI.platforms.sending=true;$("connection-fields").disabled=true;
    $("connection-back").disabled=true;$("close-settings").disabled=true;
    connectionsUI.connectionFeedback();gitlabSetupFeedback();
    try {
      const prepared=await prepareGitLabPlatform(true);if (!prepared) return;
      if (version!==connectionsUI.platforms.formVersion) return;
      connectionsUI.platforms.gitlabPending={action:"configure",version,prepared,affected:prepared.affected || [],title:"修改应用配置将影响以上连接和房间。",confirmLabel:"修改配置"};
    } catch {connectionsUI.connectionFeedback("读取应用配置失败，请重试。", "error");}
    finally {connectionsUI.platforms.sending=false;$("connection-fields").disabled=false;$("connection-back").disabled=false;$("close-settings").disabled=false;renderGitLabAccount();}
  }

  function beginGitLabApplicationEdit() {
    const pending=connectionsUI.platforms.gitlabPending;
    if (pending?.action!=="configure" || pending.version!==connectionsUI.platforms.formVersion) return;
    clearGitLabConfirmation();
    if (connectionsUI.connectionDirty()) {renderGitLabAccount();connectionsUI.connectionFeedback("请先保存名称，再修改应用配置。","error");return;}
    gitlabEditingApplication=true;
    connectionsUI.platforms.gitlabSetup=pending.prepared;
    connectionsUI.platforms.gitlabEditingImportID=pending.prepared.client_import_id || "";
    setGitLabScopes(pending.prepared.scopes);
    $("gitlab-client-id").value=pending.prepared.client_id || "";$("gitlab-client-secret").value="";
    connectionsUI.connectionFeedback();gitlabSetupFeedback();renderGitLabAccount();$("gitlab-client-id").focus();
  }

  function cancelGitLabApplicationEdit() {
    if (connectionsUI.platforms.sending || connectionsUI.platforms.gitlabAuth) return;
    gitlabEditingApplication=false;
    connectionsUI.platforms.gitlabEditingImportID=null;
    $("gitlab-client-secret").value="";
    gitlabSetupFeedback();renderGitLabAccount();
  }

  function openGitLabPopup() {
    try {
      const popup=window.open?.("about:blank", "_blank");
      if (popup) popup.opener=null;
      return popup || null;
    } catch {return null;}
  }

  async function startGitLabAuth(action, candidate = null, popup) {
    if(connectionsUI.platforms.connectOperation && action==="login") {
      const op=connectionsUI.platforms.connectOperation;
      if(JSON.stringify(state.info)!==JSON.stringify(op.service)){popup?.close();connectionsUI.connectionFeedback("会话已变化，请返回原对话继续。","error");return;}
      const prepared=candidate || await prepareGitLabPlatform();
      if(!prepared || !["ready","owner_verification_required"].includes(prepared.status)){popup?.close();connectionsUI.connectionFeedback("请先选择或配置兼容应用。","error");return;}
      const current=interactionsUI.platformInteractionState.operations.find(v=>v.operation_id===op.operation_id);
      if(!current){popup?.close();return;}
      const card={op:current};
      await interactionsUI.authorizeGitLabInteraction(card,prepared.client_import_id ? {client_import_id:prepared.client_import_id} : {application_id:prepared.application_id},popup);
      connectionsUI.showPlatformList();await connectionsUI.loadPlatforms();
      return;
    }
    if (connectionsUI.platforms.sending || connectionsUI.platforms.gitlabAuth || connectionsUI.platforms.gitlabPending || connectionsUI.connectionIsLark()) return;
    const item = connectionsUI.platforms.selected, version = connectionsUI.platforms.formVersion;
    if (item && connectionsUI.connectionDirty()) {connectionsUI.connectionFeedback("请先保存名称，再操作授权。", "error");return;}
    if (!item && !$("connection-name").value.trim()) {connectionsUI.connectionInputError("connection-name", "请输入连接名称。");return;}
    if (action === "login" && popup === undefined) popup=openGitLabPopup();
    connectionsUI.platforms.sending = true; $("connection-fields").disabled = true;
    $("connection-back").disabled = true; $("close-settings").disabled = true;
    connectionsUI.connectionFeedback("正在检查 GitLab 应用配置…");
    let pending;
    try {
      const prepared = candidate || await prepareGitLabPlatform(); if (!prepared) return;
      if (version !== connectionsUI.platforms.formVersion) return;
      if (!["ready", "owner_verification_required"].includes(prepared.status)) {
        $("gitlab-setup").hidden = false;
        renderGitLabReuse();connectionsUI.connectionFeedback(prepared.status==="application_selection_required" ? "请先选择一个兼容应用。" : prepared.status==="application_incompatible" ? "请先修改应用配置，再重新授权。" : "请先完成一次应用配置，然后登录 GitLab。", "error");return;
      }
      const affected = prepared.affected || [];
      const target = item ? {id:item.id,version:item.version} : {name:$("connection-name").value.trim(),origin:$("connection-url").value.trim()};
      pending = { action, version, target, prepared, affected, popup, updatingApplication:!!candidate && !!item?.authorization_status, creatingPlatform:!item };
      if (action === "logout" && (affected.length > 1 || affected.some(c=>c.referenced_rooms?.length))) {
        pending.title="退出登录将暂停以上房间的通知。";
        pending.confirmLabel="确认退出";
        connectionsUI.platforms.gitlabPending = pending;
        connectionsUI.connectionFeedback();
      }
    } catch {
      connectionsUI.connectionFeedback("GitLab 应用配置检查失败，请稍后重试。", "error");
    } finally {
      if (!pending && popup && !popup.closed) popup.close();
      connectionsUI.platforms.sending=false;settingsUI.renderSettingsNavigation();$("connection-fields").disabled=false;$("connection-back").disabled=false;$("close-settings").disabled=false;renderGitLabAccount();
    }
    if (connectionsUI.platforms.gitlabPending) $("connection-auth-confirm").focus();
    else if (pending) await beginGitLabAuth(pending);
  }

  function clearGitLabConfirmation() {
    connectionsUI.platforms.gitlabPending = null;
    connectionsUI.renderPlatformReferences();
  }

  async function beginGitLabAuth({action, version, target, prepared, affected, popup, updatingApplication, creatingPlatform}) {
    if (connectionsUI.platforms.sending || connectionsUI.platforms.gitlabAuth || version !== connectionsUI.platforms.formVersion || connectionsUI.connectionIsLark()) return;
    clearGitLabConfirmation();
    connectionsUI.platforms.sending = true; $("connection-fields").disabled = true;
    $("connection-back").disabled = true; $("close-settings").disabled = true;
    connectionsUI.connectionFeedback(action === "logout" ? "正在退出 GitLab…" : "正在打开 GitLab 授权页面…");
    try {
      if (action === "login" && popup === undefined) popup=openGitLabPopup();
      const op=action==="login" && onboardingUI.onboarding.active ? await onboardingUI.prepareOnboarding() : null;
      const auth = await request(`/v1/platforms/gitlab/${action}`,connectionsUI.connectionPayload({...target,application_id:prepared.application_id,client_import_id:prepared.client_import_id,scopes:prepared.scopes,...(op ? {onboarding_id:op.operation_id} : {}),affected}));
      if (version !== connectionsUI.platforms.formVersion) return;
      saveAuthorization(auth);
      connectionsUI.platforms.gitlabAuth = {...auth, affected, updatingApplication, creatingPlatform, requestedScopes:prepared.scopes || ["api"], clientImportID:prepared.client_import_id || ""}; renderGitLabAccount();
      if (auth.authorization_url) {
        const targetURL=new URL(auth.authorization_url);
        const origin=target.origin || connectionsUI.platforms.selected?.url || $("connection-url").value;
        if(targetURL.origin!==new URL(origin).origin || targetURL.protocol!=="https:" || targetURL.pathname!=="/oauth/authorize" || targetURL.username || targetURL.password)throw new Error("授权地址校验失败，请核对原操作。");
        $("gitlab-login-link").href = auth.authorization_url; $("gitlab-login-link").hidden = false; if (popup && !popup.closed) popup.location.replace(auth.authorization_url); else window.location?.assign(auth.authorization_url);}
      connectionsUI.connectionFeedback(action === "logout" ? "正在退出并核对撤销结果…" : "等待 GitLab 授权…");
      await pollGitLabAuth();
    } catch (error) {
      connectionsUI.connectionFeedback(!error.code && error.message ? error.message : error.code === "identity" ? "登录需要从服务配置的规范地址打开 TeamX；请核对应用回调地址，并刷新页面。" : "授权结果未确认，请返回列表核对。", "error");
    } finally {if (popup && !connectionsUI.platforms.gitlabAuth && !popup.closed) popup.close(); connectionsUI.platforms.sending=false;settingsUI.renderSettingsNavigation();$("connection-fields").disabled=false;$("connection-back").disabled=false;$("close-settings").disabled=false;renderGitLabAccount();}
  }

  async function pollGitLabAuth() {
    if (!runtime.canPoll()) {
      clearTimeout(connectionsUI.platforms.gitlabTimer);
      connectionsUI.platforms.gitlabTimer = setTimeout(() => { void pollGitLabAuth(); }, 1500);
      return;
    }
    const auth = connectionsUI.platforms.gitlabAuth, version = connectionsUI.platforms.formVersion;
    if (!auth) return;
    try {
      const result = await request("/v1/platforms/gitlab/status",connectionsUI.connectionPayload({session_id:auth.session_id}));
      if (connectionsUI.platforms.gitlabAuth !== auth || version !== connectionsUI.platforms.formVersion) return;
      if (["waiting", "exchanging", "settling", "committing", "cleaning", "disconnecting"].includes(result.status)) {
        auth.status=result.status;renderGitLabAccount();
        connectionsUI.platforms.gitlabTimer=setTimeout(()=>{void pollGitLabAuth();},1500);return;
      }
      clearSavedAuthorization();clearGitLabAuthView();connectionsUI.platforms.gitlabAuth=null;
      for (const item of result.platforms || []) connectionsUI.updatePlatformItem(item);
      const saved=(result.platforms || []).find(c=>c.id===connectionsUI.platforms.selected?.id) || (result.platforms || []).find(c=>c.id===auth.platform_id) || result.platforms?.[0];
      if (saved) await connectionsUI.editPlatform(saved);else renderGitLabAccount();
      if (auth.onboarding_id && onboardingUI.onboarding.pending?.operation_id===auth.onboarding_id) {
        const resultVersion=connectionsUI.platforms.formVersion,resultIdentity=onboardingUI.onboardingIdentity();
        try {const op=await request("/v1/platforms/onboarding/status",connectionsUI.connectionPayload({operation_id:auth.onboarding_id}));
          if (onboardingUI.onboardingCurrent(resultVersion,resultIdentity) && onboardingUI.onboarding.pending?.operation_id===auth.onboarding_id) onboardingUI.acceptOnboarding(op);
        } catch {if(onboardingUI.onboardingCurrent(resultVersion,resultIdentity)){onboardingUI.onboarding.error="账号登录结果已返回，通知与工具的进度未确认，请读取接入进度。";onboardingUI.renderOnboarding();}}
      }
      const savedMessage=auth.updatingApplication ? "应用配置已更新。" : auth.creatingPlatform ? "已连接 GitLab。" : "授权已更新。";
      const messages={succeeded:savedMessage + (auth.affected.some(c=>c.referenced_rooms?.length) ? "通知请手动同步恢复。" : ""),logged_out:"已退出登录，远端撤销已确认。",disconnected:"本地已断开，远端撤销或调用结清未确认。",cancelled:"本次登录已取消，请核对连接状态。",expired:"本次登录已过期，可以重新登录。",failed:"授权未完成，保存或切换结果请返回列表核对。"};
      const requestedScopes=result.requested_scopes || auth.requestedScopes, missingScopes=result.missing_scopes || [];
      const scopeError=`${missingScopes.length ? `GitLab 应用缺少权限：${missingScopes.join(" + ")}。` : `GitLab 拒绝了申请的权限：${requestedScopes.join(" + ")}。`}请打开 GitLab 应用设置，核对当前 Application ID 并勾选 ${requestedScopes.join(" + ")}，保存后重新登录。${requestedScopes.includes("write_repository") ? "若不需要拉取和推送代码，可取消 write_repository 后重新授权。" : ""}`;
      const duplicateAccount=result.error_code==="account_already_connected";
      const errors={account_already_connected:`${result.account_username ? `@${result.account_username}` : "本次授权的 GitLab 账号"} 已连接其他平台，请切换账号后重试。`,invalid_client:"应用凭据无效，请检查 Application ID 和 Secret。",invalid_scope:scopeError,invalid_grant:"授权已失效，请重新登录 GitLab。",identity_mismatch:"GitLab 账号与原账号不符，请为另一个账号新建连接；原连接未切换。"};
      const cleanupMessage=result.cleanup_status === "manual_required" ? "候选授权待人工清理，未撤销现用授权。" : ["pending","unconfirmed"].includes(result.cleanup_status) ? "候选令牌或远端撤销未确认，请在 GitLab 个人应用页面核对授权。" : result.cleanup_status === "revoked" && result.status === "failed" ? "本次未采用的候选令牌已撤销。" : "";
      const message=`${errors[result.error_code] || messages[result.status] || "请核对连接状态。"}${duplicateAccount ? "" : cleanupMessage}`;
      if (result.status === "failed" && auth.clientImportID) {
        gitlabEditingApplication=!!connectionsUI.platforms.selected?.authorization_status;
        connectionsUI.platforms.gitlabEditingImportID=auth.clientImportID;
        $("gitlab-setup").hidden=false;$("gitlab-advanced").open=true;
        connectionsUI.connectionFeedback();gitlabSetupFeedback(message,"error");
        renderGitLabAccount();
      } else connectionsUI.connectionFeedback(message,["succeeded","logged_out"].includes(result.status)?"success":"error",duplicateAccount ? result.cleanup_status==="manual_required" ? "本次未使用的授权尚未自动撤销，已有平台的授权保持不变。" : cleanupMessage : "");
    } catch(error) {
      if(connectionsUI.platforms.gitlabAuth===auth && version===connectionsUI.platforms.formVersion && error.code==="identity") {
        clearSavedAuthorization();clearGitLabAuthView();connectionsUI.platforms.gitlabAuth=null;renderGitLabAccount();
        connectionsUI.connectionFeedback("原授权会话已失效或不属于当前浏览器，请核对连接列表；未重新发起授权。","error");return;
      }
      if (connectionsUI.platforms.gitlabAuth===auth && version===connectionsUI.platforms.formVersion) {connectionsUI.connectionFeedback("授权状态读取失败，正在核对原操作。", "error"); connectionsUI.platforms.gitlabTimer=setTimeout(()=>{void pollGitLabAuth();},3000);}
    }
  }

  function clearGitLabAuthView() {clearTimeout(connectionsUI.platforms.gitlabTimer);connectionsUI.platforms.gitlabTimer=null;$("gitlab-login-link").hidden=true;$("gitlab-login-link").href="";}

  async function cancelGitLabAuth(keepalive = false) {
    clearSavedAuthorization();
    clearGitLabConfirmation();
    const auth=connectionsUI.platforms.gitlabAuth;clearGitLabAuthView();connectionsUI.platforms.gitlabAuth=null;connectionsUI.platforms.formVersion++;
    if (auth) {try {const body=connectionsUI.connectionPayload({session_id:auth.session_id}); if (keepalive) await globalThis.fetch("/v1/platforms/gitlab/cancel",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body),credentials:"same-origin",keepalive:true}); else await request("/v1/platforms/gitlab/cancel",body);} catch { /* Explicit list refresh reconciles a late commit. */ }}
    if (!connectionsUI.connectionIsLark() && !connectionsUI.connectionIsScion()) renderGitLabAccount();
  }

  async function configureGitLabPlatform() {
    if (connectionsUI.platforms.sending || connectionsUI.platforms.gitlabAuth || connectionsUI.platforms.gitlabPending) return;
    if (!connectionsUI.platforms.selected && !$("connection-name").value.trim()) {connectionsUI.connectionInputError("connection-name","请输入连接名称。");return;}
    if (connectionsUI.platforms.selected && connectionsUI.connectionDirty()) {connectionsUI.connectionFeedback("请先保存名称，再更新应用配置。","error");return;}
    const version=connectionsUI.platforms.formVersion;
    const popup=openGitLabPopup();
    let candidate;
    connectionsUI.platforms.sending=true;$("connection-fields").disabled=true;$("connection-back").disabled=true;$("close-settings").disabled=true;
    connectionsUI.connectionFeedback();gitlabSetupFeedback("正在准备 GitLab 授权…");
    try {
      const prepared=await prepareGitLabPlatform(gitlabEditingApplication);if (!prepared) return;
      const source=gitlabPreparePayload();
      const replacing=connectionsUI.platforms.gitlabEditingImportID;
      const reusable=!connectionsUI.platforms.selected?.authorization_status && !$("gitlab-client-secret").value && ["ready","owner_verification_required"].includes(prepared.status) && (prepared.status==="ready" || $("gitlab-client-id").value.trim()===prepared.client_id) && source.scopes.join(" ")===(prepared.scopes || []).join(" ");
      if (["application_selection_required","application_incompatible"].includes(prepared.status)) {renderGitLabReuse();gitlabSetupFeedback("请先选择兼容应用或处理上方兼容性问题。","error");return;}
      let result=prepared;
      if (!reusable) {
        if (!$("gitlab-client-id").value.trim() || !$("gitlab-client-secret").value) {gitlabSetupFeedback(!$("gitlab-client-id").value.trim() ? "请填写 Application ID。" : "请填写 Secret。","error");return;}
        result=await request("/v1/platforms/gitlab/initialize",connectionsUI.connectionPayload({...source,...(replacing ? {client_import_id:replacing} : {}),client:{client_id:$("gitlab-client-id").value.trim(),client_secret:$("gitlab-client-secret").value,redirect_uri:prepared.redirect_uri,scopes:source.scopes}}),undefined,50000);
      }
      if (version!==connectionsUI.platforms.formVersion) return;
      connectionsUI.platforms.gitlabSetupVersion++;connectionsUI.platforms.gitlabSetup=result;
      connectionsUI.platforms.gitlabEditingImportID=connectionsUI.platforms.selected?.authorization_status ? null : result.client_import_id || "";
      if (["ready","owner_verification_required"].includes(result.status)) candidate=result;
      if (candidate) gitlabSetupFeedback();
      else gitlabSetupFeedback("应用配置未就绪，请检查填写内容。","error");
    } catch (error) {
      $("gitlab-setup").hidden=false;$("gitlab-advanced").open=true;
      if(error.code === "conflict") connectionsUI.platforms.gitlabEditingImportID=null;
      const reload=connectionsUI.platforms.selected?.authorization_status ? "请取消修改，再点击「修改应用配置」重新填写。" : "请返回列表重新打开连接，再填写。";
      gitlabSetupFeedback(error.code === "busy" ? "登录仍在处理中，请等待结束后重新保存。" : error.code === "conflict" ? `配置已变化，${reload}` : error.code === "request" ? "配置无效，请检查 Application ID、Secret 和回调地址。" : "保存结果未确认，请重新打开连接核对。", "error");
    }
    finally {if ((!candidate || version !== connectionsUI.platforms.formVersion) && popup && !popup.closed) popup.close();$("gitlab-client-secret").value="";connectionsUI.platforms.sending=false;settingsUI.renderSettingsNavigation();$("connection-fields").disabled=false;$("connection-back").disabled=false;$("close-settings").disabled=false;renderGitLabAccount();}
    if (candidate && version === connectionsUI.platforms.formVersion) await startGitLabAuth("login",candidate,popup);
  }

  function resetApplicationEditor() { gitlabEditingApplication = false; }

  function mount() {
    $("gitlab-application").addEventListener("change",async()=>{connectionsUI.platforms.gitlabApplicationID=$("gitlab-application").value;await discoverGitLabSetup();});

    $("gitlab-login").addEventListener("click",()=>startGitLabAuth("login"));

    $("gitlab-edit-application").addEventListener("click",requestGitLabApplicationEdit);

    $("gitlab-repair-application").addEventListener("click",requestGitLabApplicationEdit);

    $("gitlab-edit-cancel").addEventListener("click",cancelGitLabApplicationEdit);

    $("gitlab-logout").addEventListener("click",()=>startGitLabAuth("logout"));

    $("gitlab-cancel").addEventListener("click",async()=>{await cancelGitLabAuth();connectionsUI.connectionFeedback("已请求取消，请核对连接状态；已提交的授权不会回滚。");});

    for (const option of gitlabScopeOptions) $(option.id).addEventListener("change",async()=>{
      gitlabScopeVersion++;if (option.required) $(option.id).checked=true;
      if (!connectionsUI.platforms.selected && connectionsUI.platforms.gitlabSetup?.status==="application_incompatible") await discoverGitLabSetup();
    });

    $("gitlab-import").addEventListener("click",configureGitLabPlatform);
  }

  return {
    mount,
    restoreAuthorization,
    resetApplicationEditor,
    setGitLabScopes,
    discoverGitLabSetup,
    renderGitLabAccount,
    cancelGitLabAuth,
    beginGitLabApplicationEdit,
    beginGitLabAuth,
    clearGitLabConfirmation,
    gitlabSetupFeedback,
    startGitLabAuth,
    renderGitLabSave,
    configureGitLabPlatform
  };
}
