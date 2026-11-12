// Shared platform connection forms, validation and account selection.
export function createConnections({ env, settingsUI, runtime, scionUI, gitlabUI, larkUI, onboardingUI, workspaceUI, notificationsUI }) {
  const { document, window, Option, setTimeout, clearTimeout, request } = env;
  const { state, $, setSelectOptions, confirmAction } = runtime;

  // Application secrets live only in memory and are never retried automatically.
  const platforms = { items: [], selected: null, sending: false, generation: 0, formVersion: 0, discovery: null, detecting: false, auth: null, authTimer: null, larkPending:null, larkStarting:null, gitlabAuth: null, gitlabTimer: null, gitlabPending: null, gitlabSetup:null, gitlabSetupVersion:0, gitlabEditingImportID:null, scionDiscovery:null, scionAuth:null, scionTimer:null, scionPending:null, scionLogout:null };

  const connectionValidationErrors = { login_failed:"PAT 登录失败，请核对实例、令牌及权限。", timeout:"操作超时，请刷新账号核对结果；不会自动重新登录。", cancelled:"操作已取消，请刷新账号核对状态。", credential_unavailable: "登录凭据不可用，请核对账号授权或服务凭据配置。", credential_rejected: "认证失败，请核对当前账号登录和权限。", identity_mismatch: "profile 账号或应用已变化，请核对后重新验证。", permission_denied: "当前用户权限不足，请检查授权。", application_scopes_unchecked:"应用权限暂未确认，请检查网络后重新检测。", identity_or_version_changed:"账号或连接版本已变化，请核对原账号后重试。", cli_version_mismatch: "CLI 版本不匹配，请使用对应工具已验收的固定版本。", cli_unavailable: "CLI 无法执行，请核对路径与后台运行权限。", query_failed: "连接失败，请检查地址或网络后重试验证。", invalid_response: "实例返回了无效的身份信息。" };

  let connectionFeedbackTimer=null;

  function connectionFeedback(message = "", kind = "status", detail = "") {
    clearTimeout(connectionFeedbackTimer);connectionFeedbackTimer=null;
    settingsUI.renderSettingsNavigation();
    const feedback = $("platforms-feedback");
    if (typeof message === "function") settingsUI.bindSystemTime(feedback, () => { feedback.textContent = message(); });
    else { feedback.refreshSystemTime = null; feedback.textContent = message; }
    feedback.dataset.kind = kind;
    feedback.setAttribute("role", kind === "error" ? "alert" : "status");
    const details = $("platforms-feedback-details"), explanation = $("platforms-feedback-detail");
    details.hidden = !detail; details.open = false;
    if (typeof detail === "function") settingsUI.bindSystemTime(explanation, () => { explanation.textContent = detail(); });
    else { explanation.refreshSystemTime = null; explanation.textContent = detail; }
    if(message && kind==="success" && !detail)connectionFeedbackTimer=setTimeout(()=>connectionFeedback(),5000);
  }

  const connectionInputs = ["connection-name", "connection-url", "connection-executable", "connection-profile"];

  function clearPlatformErrors() {
    for (const id of connectionInputs) $(id).setAttribute("aria-invalid", "false");
  }

  function connectionInputError(id, message) {
    $(id).setAttribute("aria-invalid", "true");
    connectionFeedback(message, "error");
    $(id).focus();
  }

  function connectionAuthFields() {
    const isLark = connectionIsLark(), isScion = connectionIsScion();
    $("connection-provider-field").hidden = isLark && !!platforms.selected;
    $("connection-name-field").classList.toggle("connection-field-wide",isLark && !!platforms.selected);
    for (const id of ["connection-executable", "connection-profile"]) $(id).setAttribute("aria-required", String(isLark));
    clearPlatformErrors();
    connectionFeedback();
    $("connection-validation").hidden=false;
    $("connection-notes").hidden = isScion || !platforms.selected;
    if (isLark || isScion) $("connection-save").textContent = "保存连接";
    if (isLark || isScion) {$("connection-save").hidden=false;$("connection-actions").hidden=false;}
    $("connection-scion-row").hidden = !isScion;
    $("connection-profile-row").hidden = !isLark;
    $("connection-url-row").hidden = isLark;
    $("connection-url").required = !isLark;
    $("gitlab-account-row").hidden = isLark || isScion;
    $("gitlab-app-config-row").hidden = isLark || isScion;
    $("connection-name").placeholder = isScion ? "公司 Scion" : isLark ? "我的飞书" : "公司 GitLab";
    $("connection-url").placeholder = isScion ? "https://scion.example.com" : "https://gitlab.example.com";
    scionUI.renderScionPATLink();
    if (!isScion) $("connection-scion-pat").value = "";
    if (isLark || isScion) $("gitlab-client-secret").value = "";
    else gitlabUI.renderGitLabAccount();
  }

  function connectionIsLark() { return ["feishu", "lark"].includes($("connection-provider").value); }

  function connectionIsScion() { return $("connection-provider").value === "scion"; }

  function renderPlatformReferences() {
    const item = platforms.selected;
    const pending = connectionIsLark() ? platforms.larkPending : connectionIsScion() ? platforms.scionPending : platforms.gitlabPending;
    const active = connectionIsLark() ? platforms.larkStarting || platforms.auth : connectionIsScion() ? platforms.scionLogout : platforms.gitlabAuth;
    const gitlab=!connectionIsLark() && !connectionIsScion();
    const flow = pending || active, affected = flow?.affected || (gitlab ? platforms.gitlabSetup?.affected : undefined);
    const groups = affected ? affected.map(a => {
      const connection = platforms.items.find(c => c.id === a.id);
      return { name: connection?.name || "其他共享连接", references: (a.referenced_rooms || []).map(ref => ({
        ...ref, ...connection?.references?.find(r => r.room_id === ref.room_id && r.room_instance === ref.room_instance),
      })) };
    }) : item ? [{ name: item.name, references: item.references || [] }] : [];
    if (flow && !groups.length) groups.push({name:"",references:[]});
    $("connection-auth-confirmation").hidden = !pending;
    $("connection-auth-confirmation-title").textContent = pending?.title || "";
    $("connection-auth-confirmation-description").textContent = pending?.description || "";
    $("connection-auth-confirmation-description").hidden = !pending?.description;
    $("connection-auth-confirm").textContent = pending?.confirmLabel || "继续登录";
    $("connection-auth-confirm").classList.toggle("danger-button",pending?.action === "logout");
    for (const id of ["connection-auth-confirm","connection-auth-dismiss"]) $(id).disabled = platforms.sending;
    const count = new Set(groups.flatMap(group => group.references.map(ref => JSON.stringify([ref.room_id, ref.room_instance])))).size;
    $("connection-references").hidden = !groups.length || connectionIsLark() && !flow && count===0;
    $("connection-references").dataset.impact = String(!!flow);
    $("connection-references-title").textContent = `使用此连接的会话 · ${count}`;
    $("connection-reference-list").replaceChildren(...groups.map(group => {
      const section = document.createElement("div"); section.className = "connection-reference-group";
      if (groups.length > 1) { const title = document.createElement("h5"); title.textContent = group.name; section.append(title); }
      const list = document.createElement("ul");
      for (const ref of group.references) {
        const row = document.createElement("li"), name = document.createElement("span");
        name.textContent = ref.name || "房间信息待刷新"; row.append(name);
        if (ref.pending) { const status = document.createElement("small"); status.textContent = "待完成绑定"; row.append(status); }
        list.append(row);
      }
      if (!group.references.length) { const empty = document.createElement("li"); empty.className = "quiet"; empty.textContent = "暂无房间引用"; list.append(empty); }
      section.append(list); return section;
    }));
  }

  async function cancelPlatformAuth() {await scionUI.cancelScionAuth();await gitlabUI.cancelGitLabAuth();await larkUI.cancelLarkAuth();}

  function updatePlatformItem(item) {
    platforms.generation++; // A confirmed mutation supersedes earlier list reads.
    const index = platforms.items.findIndex(c => c.id === item.id);
    if (index < 0) platforms.items.push(item); else platforms.items[index] = item;
    if (platforms.selected?.id === item.id) platforms.selected = item;
  }

  function connectionPayload(extra) {
    return { service_epoch: state.catalog.service_epoch, caller: { kind: "human", id: state.operator }, ...extra };
  }

  function renderPlatforms() {
    $("platforms-empty").hidden = platforms.items.length > 0;
    $("platforms-list").replaceChildren(...platforms.items.map(item => {
      const button = document.createElement("button"); button.type = "button"; button.className = "connection-row";button.dataset.platformId=item.id;
      const label = document.createElement("span"), name = document.createElement("strong"), detail = document.createElement("small"), status = document.createElement("span");
      name.textContent = item.name; detail.textContent = item.provider === "lark" ? `${item.cli_profile?.brand === "lark" ? "Lark" : "飞书"}${item.validation.username ? ` · ${item.validation.username}` : ""}` : `${item.provider === "scion" ? "Scion" : "GitLab"} · ${item.url}`;
      status.textContent = item.credential_kind === "oauth" && item.authorization_status !== "active" ? (item.authorization_status === "logged_out" ? "已退出 ›" : "需要重新授权 ›") : item.validation.status === "valid" ? "已验证 ›" : item.validation.status === "failed" ? "验证失败 ›" : "未验证 ›";
      label.append(name, detail); button.append(label, status);
      button.addEventListener("click", () => editPlatform(item)); return button;
    }));
  }

  async function loadPlatforms() {
    const generation = ++platforms.generation;
    // Keep the current content while reading; only a failed read needs an action.
    $("platforms-list-view").setAttribute("aria-busy", "true");
    $("platforms-retry").disabled = true;
    $("platforms-retry").textContent = "重试中…";
    try {
      const query = new URLSearchParams({ service_epoch: state.catalog.service_epoch, caller_id: state.operator });
      const data = await request(`/v1/platforms?${query}`);
      if (generation !== platforms.generation) return;
      platforms.items = data.platforms; renderPlatforms();
      $("platforms-load-error").hidden = true;
      if (!$("platforms-list-view").hidden) connectionFeedback();
    } catch {
      if (generation === platforms.generation) {
        $("platforms-empty").hidden = true;
        $("platforms-load-error").hidden = false;
      }
    } finally {
      if (generation === platforms.generation) {
        $("platforms-list-view").setAttribute("aria-busy", "false");
        $("platforms-retry").disabled = false;
        $("platforms-retry").textContent = "重试";
      }
    }
  }

  function showPlatformList() {
    platforms.connectOperation=null;$("connection-name").readOnly=false;
    larkUI.clearLarkConfirmation();
    scionUI.clearScionConfirmation();
    $("connection-scion-pat").value="";
    gitlabUI.clearGitLabConfirmation();
    platforms.formVersion++; platforms.discovery = null; platforms.detecting = false; larkUI.clearLarkAuthView(); larkUI.resetLarkSetup(); larkUI.closeLarkControls(); platforms.larkDiscoveryError = null;
    onboardingUI.onboarding.active=false;
    platforms.selected = null; platforms.gitlabSetup=null;platforms.gitlabEditingImportID=null;$("gitlab-client-secret").value = "";
    settingsUI.showPlatforms();
    $("connection-form").hidden = true; $("platforms-list-view").hidden = !state.managed;
    $("platforms-load-error").hidden = true;
    connectionFeedback();onboardingUI.renderOnboarding();
  }

  function editPlatform(item = null) {
    platforms.connectOperation=null;$("connection-name").readOnly=false;
    if(!$("platforms-list-view").hidden){platforms.listReturn={scroll:$("settings-panels").scrollTop,id:item?.id};$("settings-panels").scrollTop=0;}
    settingsUI.showPlatforms();
    larkUI.clearLarkConfirmation();
    scionUI.clearScionConfirmation();
    $("connection-scion-pat").value=""; platforms.scionDiscovery=null;
    $("connection-scion-login-panel").hidden=true;
    $("connection-scion-advanced").open=false;
    gitlabUI.clearGitLabConfirmation();
    platforms.formVersion++; platforms.discovery = null; platforms.detecting = false; larkUI.clearLarkAuthView(); larkUI.resetLarkSetup(); larkUI.closeLarkControls(); platforms.larkDiscoveryError = null;
    clearPlatformErrors();
    platforms.selected = item;onboardingUI.onboarding.active=false;onboardingUI.onboarding.fixed=null;platforms.gitlabApplicationID="";
    onboardingUI.renderOnboarding();
    gitlabUI.resetApplicationEditor();
    gitlabUI.gitlabSetupFeedback();
    platforms.gitlabSetup=null;platforms.gitlabEditingImportID=null;
    $("gitlab-compatibility-details").open=false;
    $("gitlab-advanced").open=!item?.authorization_status;
    gitlabUI.setGitLabScopes(); $("gitlab-setup").hidden = true; $("gitlab-client-id").value = ""; $("gitlab-client-secret").value = "";
    $("connection-form").hidden = false; $("platforms-list-view").hidden = true;
    $("connection-title").textContent = item?.provider === "lark" ? `${item.cli_profile?.brand === "lark" ? "Lark" : "飞书"}设置` : item ? "平台详情" : "添加平台";
    $("connection-name").value = item?.name || "";
    setSelectOptions("connection-provider",[new Option("GitLab","gitlab"),new Option("Scion","scion"),new Option("飞书","feishu"),new Option("Lark","lark")],item?.provider === "lark" ? item.cli_profile?.brand || "feishu" : item?.provider || "gitlab");
    $("connection-provider").disabled = !!item;
    $("connection-url").value = item?.url || ""; $("connection-url").readOnly = !!item;
    setSelectOptions("connection-profile",[new Option("", item?.cli_profile?.profile || "")],item?.cli_profile?.profile || "");
    $("connection-executable").value = item?.cli_profile?.executable || "";
    $("connection-scion-executable").value = item?.provider === "scion" ? item.cli_profile.executable : "";
    setSelectOptions("connection-scion-profile",[new Option("", item?.provider === "scion" ? item.cli_profile.profile : "")],item?.provider === "scion" ? item.cli_profile.profile : "");
    $("connection-advanced").open = false;
    $("connection-capabilities").hidden = true;
    $("connection-capability-details").open = false;
    $("connection-setup-new").checked = true;
    $("connection-setup-existing").checked = false;
    $("connection-setup-copy").textContent = "复制命令";
    $("connection-setup-copy-status").textContent = "";
    $("connection-notes").hidden = !item;
    const v = item?.validation;
    $("connection-validation").textContent = v?.status === "valid" ? `已验证 · @${v.username}`
      : v?.status === "failed" ? connectionValidationErrors[v.error_code] || "验证失败。" : "尚未验证连接。";
    settingsUI.bindSystemTime($("connection-validation"), () => { $("connection-validation").title = [v?.user_id ? `账号 ID ${v.user_id}` : "", v?.checked_at ? `验证于 ${settingsUI.formatDate(v.checked_at)}` : ""].filter(Boolean).join(" · "); });
    renderPlatformReferences();
    $("connection-delete").disabled = !!item?.references?.length;
    for (const id of ["connection-delete", "connection-verify"]) $(id).hidden = !item;
    connectionFeedback(); connectionAuthFields();
    $("connection-fields").disabled = false; $("connection-name").focus();
    for (const id of ["connection-save", "connection-verify"]) $(id).disabled = false;
    onboardingUI.renderOnboarding();
    if (connectionIsLark()) return larkUI.detectLarkPlatform();
    if (connectionIsScion()) return scionUI.detectScionPlatform();
    gitlabUI.renderGitLabAccount();
    return gitlabUI.discoverGitLabSetup();
  }

  function connectionDirty() {
   const item=platforms.selected;
   return !item || $("connection-name").value.trim()!==item.name || (item.provider==="lark" && JSON.stringify(larkUI.larkProfileDraft())!==JSON.stringify(item.cli_profile)) || (item?.provider==="scion" && JSON.stringify(scionUI.scionProfileDraft())!==JSON.stringify(item.cli_profile));
  }

  async function connectionAction(action) {
    if (platforms.sending || platforms.auth || platforms.larkPending || platforms.gitlabAuth || platforms.gitlabPending || platforms.scionAuth || platforms.scionPending || platforms.detecting || !state.catalog) return;
    const item = platforms.selected;
    if (action==="save" && item && onboardingUI.onboarding.active) {
      onboardingUI.onboarding.busy=true;platforms.sending=true;$("connection-fields").disabled=true;$("close-settings").disabled=true;$("connection-back").disabled=true;onboardingUI.renderOnboarding();
      try {
        const version=platforms.formVersion,identity=onboardingUI.onboardingIdentity(),op=await onboardingUI.prepareOnboarding();
        const applied=await request("/v1/platforms/onboarding/apply",connectionPayload({operation_id:op.operation_id}),undefined,50000);
        if(onboardingUI.onboardingCurrent(version,identity)){onboardingUI.acceptOnboarding(applied);await loadPlatforms();if(workspaceUI.workspaceView)await notificationsUI.syncGitLabNotifications(state.catalog,workspaceUI.workspaceView);}
      }
      catch(error) {onboardingUI.onboarding.error=error.code ? "启用结果未确认，请读取进度。" : error.message;onboardingUI.renderOnboarding();}
      finally {onboardingUI.onboarding.busy=false;platforms.sending=false;$("connection-fields").disabled=false;$("close-settings").disabled=false;$("connection-back").disabled=false;onboardingUI.renderOnboarding();}return;
    }
    if (connectionIsLark() && platforms.larkSetupResult && platforms.larkSetupResult.status !== "succeeded") {connectionFeedback("请先核对应用并完成授权，再保存连接。", "error");return;}
    if (action === "save" && item?.provider === "gitlab" && !connectionDirty()) return;
    if (action === "save" && !connectionIsLark() && !connectionIsScion() && !item) return gitlabUI.startGitLabAuth("login");
    if (action !== "save" && !item) return;
    if (action === "verify" && connectionDirty()) { connectionFeedback("请先保存更改，再验证连接。", "error"); return; }
    const version = platforms.formVersion, epoch = state.catalog.service_epoch, operator = state.operator;
    const stillCurrent = () => platforms.formVersion === version && state.catalog?.service_epoch === epoch && state.operator === operator && platforms.selected?.id === item?.id && platforms.selected?.version === item?.version && !platforms.sending;
    if (action === "delete") {
      if (!await confirmAction({title:`删除「${item.name}」连接？`,message:"删除后将无法通过此连接访问平台。",confirmLabel:"删除连接"}) || !stillCurrent()) return;
    }
    let payload = { id: item?.id || crypto.randomUUID().replaceAll("-", ""), version: item?.version || 0 };
    if (action === "save") {
      clearPlatformErrors();
      payload = { ...payload, name: $("connection-name").value.trim(), provider: connectionIsLark() ? "lark" : connectionIsScion() ? "scion" : "gitlab", url: connectionIsLark() ? larkUI.connectionOrigin() : $("connection-url").value.trim() };
      if (payload.provider === "scion") {
        if ($("connection-scion-pat").value) { connectionFeedback("尚有未提交的 PAT，请先点击「登录」，再保存连接。", "error"); return; }
        const profile = scionUI.scionProfileDraft();
        if (!platforms.scionDiscovery?.profiles.some(p=>p.profile===profile.profile) || platforms.scionDiscovery.executable!==profile.executable || platforms.scionDiscovery.origin!==payload.url) { connectionFeedback("连接未保存：请刷新账号并选择可用账号，或先使用 PAT 登录。", "error"); return; }
        if (!item || JSON.stringify(profile) !== JSON.stringify(item.cli_profile)) payload.cli_profile = profile;
      }
      if (payload.provider === "lark") {
        const profileName = $("connection-profile").value.trim();
        if (!profileName || !platforms.discovery?.profiles.some(p => p.name === profileName)) { connectionFeedback("连接未保存：请先选择或连接应用。", "error"); return; }
        if (!/^[A-Za-z0-9_.-]{1,128}$/.test(profileName) || profileName === "." || profileName === "..") {
          connectionInputError("connection-profile", "连接未保存：应用配置名称无效，请点击「重新检测」。", "error"); return;
        }
        const executable = $("connection-executable").value.trim();
        if (!executable.startsWith("/") || /[\x00\r\n]/.test(executable)) {
          connectionInputError("connection-executable", "连接未保存：请填写 lark-cli 可执行文件的绝对路径，例如 /opt/homebrew/bin/lark-cli。"); return;
        }
        const profile = larkUI.larkProfileDraft();
        if (!item || JSON.stringify(profile) !== JSON.stringify(item.cli_profile)) payload.cli_profile = profile;
      }
    }
    if (payload.cli_profile && item?.references?.length) {
      if (!await confirmAction({title:`替换「${item.name}」账号配置？`,message:`影响会话：${item.references.map(ref=>ref.name).join("、")}。\n将等待正在执行的工具结束，替换后需重新验证连接并核对绑定。`,confirmLabel:"替换配置"}) || !stillCurrent()) return;
      payload.referenced_rooms=item.references.map(({room_id,room_instance})=>({room_id,room_instance}));
    }
    platforms.sending = true; $("connection-fields").disabled = true;
    $("connection-back").disabled = true; $("close-settings").disabled = true;
    connectionFeedback(action === "verify" ? "正在验证…" : action === "delete" ? "正在删除…" : "正在保存…");
    let savedResult = null;
    try {
      const op=action==="save" && onboardingUI.onboarding.active ? await onboardingUI.prepareOnboarding() : null;
      if (op) payload={...payload,id:op.platform_id,url:op.input.origin,onboarding_id:op.operation_id};
      let result = await request(`/v1/platforms/${action}`, connectionPayload(payload), undefined, 40000);
      if (result.onboarding) onboardingUI.acceptOnboarding(result.onboarding);
      if (action === "delete") { showPlatformList(); await loadPlatforms(); connectionFeedback("连接已删除。", "success"); }
      else if (action === "save") {
        savedResult = result;
        updatePlatformItem(result);
        if (["lark", "scion"].includes(result.provider) && result.validation.status !== "valid") {
          connectionFeedback("连接已保存，正在验证账号…");
          result = await request("/v1/platforms/verify", connectionPayload({ id: result.id, version: result.version }), undefined, 40000);
          savedResult = result; updatePlatformItem(result);
          if (result.validation.status !== "valid") {
            await editPlatform(result);
            connectionFeedback(`连接已保存，但验证失败：${connectionValidationErrors[result.validation.error_code] || "请核对账号授权后重试。"}`, "error");
            return;
          }
        }
        // Use the confirmed save response immediately; an earlier list read must
        // not replace it with an older version after returning to the list.
        platforms.generation++;
        let index = platforms.items.findIndex(item => item.id === result.id);
        if (index < 0) { index = platforms.items.length; platforms.items.push(result); }
        else platforms.items[index] = result;
        showPlatformList(); renderPlatforms();
        $("platforms-list-view").setAttribute("aria-busy", "false");
        connectionFeedback(["lark", "scion"].includes(result.provider) ? "连接已保存，账号验证成功。" : "名称已保存。", "success");
        $("platforms-list").children[index].focus();
      } else { updatePlatformItem(result); await editPlatform(result); connectionFeedback(result.validation.status === "valid" ? "连接验证成功。" : connectionValidationErrors[result.validation.error_code] || "验证失败。", result.validation.status === "valid" ? "success" : "error"); }
    } catch (error) {
      // Reconcile only by explicit list refresh. Never resend a secret request.
      const errors = { conflict: "连接已变化，请返回列表重新加载后再编辑。", request: payload.provider === "lark" ? "连接未保存：请检查连接名称及自动检测的 CLI 配置。" : "连接未保存：请检查连接名称、HTTPS 实例地址及认证来源。", identity: "服务身份已变化，请刷新页面后重试。", capacity: "平台数量已达上限。", busy: "操作尚未结束，请稍后返回列表核对。" };
      if (savedResult) { await editPlatform(savedResult); connectionFeedback("连接已保存，验证结果未确认。请重新验证。", "error"); }
      else connectionFeedback(errors[error.code] || "操作结果未确认，请返回列表核对。", "error");
    } finally {

      platforms.sending = false; settingsUI.renderSettingsNavigation(); if (!connectionIsLark() && !connectionIsScion()) gitlabUI.renderGitLabSave(); $("connection-fields").disabled = false;
      $("connection-back").disabled = false; $("close-settings").disabled = false;
    }
  }

  function mount() {
    $("connection-auth-confirm").addEventListener("click",()=>{
      if (connectionIsScion()) return scionUI.logoutScion();
      if (connectionIsLark()) { if (platforms.larkPending) return larkUI.beginLarkAuth(platforms.larkPending); }
      else if (platforms.gitlabPending?.action === "configure") return gitlabUI.beginGitLabApplicationEdit();
      else if (platforms.gitlabPending) return gitlabUI.beginGitLabAuth(platforms.gitlabPending);
    });

    $("connection-auth-dismiss").addEventListener("click",()=>{
      if (connectionIsScion()) {scionUI.clearScionConfirmation();$("connection-scion-logout").focus();}
      else if (connectionIsLark()) {const action=platforms.larkPending?.action;larkUI.clearLarkConfirmation();$(larkUI.larkAuthButton(action)).focus();}
      else {const action=platforms.gitlabPending?.action;gitlabUI.clearGitLabConfirmation();gitlabUI.renderGitLabAccount();$(action === "configure" ? "gitlab-edit-application" : action === "logout" ? "gitlab-logout" : "gitlab-login").focus();}
      connectionFeedback("已取消本次操作。");
    });

    $("connection-new").addEventListener("click",async()=>{
      if (onboardingUI.onboarding.pending && !onboardingUI.onboardingDone()) {onboardingUI.onboarding.error="请先继续上次接入，或停止剩余步骤。";onboardingUI.renderOnboarding();return;}
      onboardingUI.clearOnboarding();await editPlatform();await onboardingUI.startOnboardingOptions();
    });

    $("connection-name").addEventListener("input",()=>{if (connectionIsLark()) larkUI.renderLarkAccount();else if (!connectionIsScion()) gitlabUI.renderGitLabSave();});

    $("connection-back").addEventListener("click", async () => { if (platforms.auth || platforms.gitlabAuth || platforms.scionAuth) await cancelPlatformAuth(); if (onboardingUI.onboarding.pending) await onboardingUI.onboardingAction("status",{discardUnconnected:true}); showPlatformList(); await loadPlatforms();
      if(platforms.listReturn && $("settings-dialog").open && settingsUI.settingsTab==="platforms" && $("connection-form").hidden){const back=platforms.listReturn;([...$("platforms-list").children].find(row=>row.dataset.platformId===back.id) || $("connection-new")).focus({preventScroll:true});$("settings-panels").scrollTop=back.scroll;}
    });

    $("platforms-retry").addEventListener("click", loadPlatforms);

    $("connection-provider").addEventListener("change", async () => {
      larkUI.resetLarkSetup(); larkUI.closeLarkControls(); platforms.larkDiscoveryError = null;
      platforms.gitlabApplicationID="";platforms.gitlabSetup=null;platforms.gitlabEditingImportID=null;$("gitlab-setup").hidden=true;
      larkUI.clearLarkConfirmation();
      scionUI.clearScionConfirmation();
      if (platforms.scionAuth) await scionUI.cancelScionAuth();
      $("connection-scion-pat").value=""; platforms.scionDiscovery=null;
      setSelectOptions("connection-scion-profile",[],"");
      $("connection-scion-login-panel").hidden=true;
      $("connection-scion-advanced").open=false;
      platforms.formVersion++; platforms.discovery = null; platforms.detecting = false;
      setSelectOptions("connection-profile",[],"");
      $("connection-setup-new").checked = true;
      $("connection-setup-existing").checked = false;
      if (!platforms.selected) {
        $("connection-url").value = connectionIsLark() ? larkUI.connectionOrigin() : "";
        if (!$("connection-name").value.trim() || ["飞书", "Lark"].includes($("connection-name").value)) $("connection-name").value = connectionIsLark() ? larkUI.connectionBrand() === "lark" ? "Lark" : "飞书" : "";
      }
      connectionAuthFields();
      onboardingUI.renderOnboarding();
      if (connectionIsLark()) return larkUI.detectLarkPlatform();
      if (connectionIsScion()) return scionUI.detectScionPlatform();
      gitlabUI.renderGitLabAccount();
    });

    $("connection-url").addEventListener("change", () => { platforms.gitlabApplicationID="";scionUI.renderScionPATLink(); if(connectionIsScion()) return scionUI.detectScionPlatform(); return gitlabUI.discoverGitLabSetup(); });

    window.addEventListener("pagehide", () => { $("gitlab-client-secret").value = ""; void gitlabUI.cancelGitLabAuth(true); void larkUI.cancelLarkAuth(true); });

    $("connection-form").addEventListener("submit", event => {
      event.preventDefault();
      if (!connectionIsLark() && !connectionIsScion() && (!platforms.selected || !connectionDirty() && $("gitlab-advanced").open && $("gitlab-client-secret").value)) return gitlabUI.configureGitLabPlatform();
      return connectionAction("save");
    });

    $("connection-verify").addEventListener("click", () => connectionAction("verify"));

    $("connection-delete").addEventListener("click", () => connectionAction("delete"));
  }

  return {
    mount,
    platforms,
    connectionValidationErrors,
    editPlatform,
    connectionIsLark,
    connectionIsScion,
    connectionPayload,
    loadPlatforms,
    connectionFeedback,
    connectionAuthFields,
    renderPlatformReferences,
    updatePlatformItem,
    connectionDirty,
    showPlatformList,
    connectionInputError,
    cancelPlatformAuth,
    connectionAction
  };
}
