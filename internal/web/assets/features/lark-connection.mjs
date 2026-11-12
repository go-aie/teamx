// Lark application setup, account discovery and authorization lifecycle.
export function createLarkConnection({ env, runtime, connectionsUI, settingsUI, onboardingUI }) {
  const { document, window, Option, setTimeout, clearTimeout, request } = env;
  const { $, setSelectOptions } = runtime;

  function connectionBrand() { return $("connection-provider").value === "lark" ? "lark" : "feishu"; }

  function connectionOrigin() { return connectionBrand() === "lark" ? "https://open.larksuite.com" : "https://open.feishu.cn"; }

  function clearLarkAuthView() {
    clearTimeout(connectionsUI.platforms.authTimer); connectionsUI.platforms.authTimer = null;
    $("connection-login-panel").hidden = true;
    $("connection-login-link").href = ""; $("connection-login-qr").src = "";
    $("connection-login-cancel").hidden = true;
  }

  // Setup results belong to this form only. A discovered application is not a verified user.
  function resetLarkSetup() { connectionsUI.platforms.larkSetupResult = null; connectionsUI.platforms.larkSetupUncertain = false; connectionsUI.platforms.larkAccountVerified = null; }

  function rememberLarkSetup(result) {
    connectionsUI.platforms.larkSetupResult = result;
    connectionsUI.platforms.larkSetupUncertain = true;
  }

  function larkSetupFailure(result) {
    const authorization = result.stage === "authorization";
    const outcome = result.status === "cancelled" ? "已取消" : result.status === "expired" ? "已过期" : "失败";
    if (authorization && result.configuration_found) {
      const reason = result.error_code === "permission_denied" ? "读取权限不足，请检查后继续授权。" : `授权${outcome}，请继续授权。`;
      return `应用已配置。${reason}`;
    }
    const reason = {configuration_unconfirmed:"结果未确认，请重新检测。",configuration_changed:"本机配置已变化，请核对后重新检测。",identity_mismatch:"应用或品牌不符，请核对后重新检测。",setup_brand_unverified:"Lark 网页配置尚未验证，请使用「修复连接」。"}[result.error_code];
    if (reason) return `${result.configuration_found ? "已发现本机配置。" : ""}${reason}`;
    return `${authorization ? "账号授权" : "应用配置"}${outcome}。${result.configuration_found ? "已发现本机配置" : "尚未确认本机配置"}，请重新检测。`;
  }

  function renderLarkSetupCommand() {
    const existing = $("connection-setup-existing").checked;
    const executable = $("connection-executable").value || "lark-cli";
    const profile = connectionsUI.platforms.discovery?.setup_profile || `teamx-${connectionBrand()}`;
    const command = `'${executable.replaceAll("'", "'\\''")}' config init${existing ? "" : " --new"} --brand ${connectionBrand()} --name ${profile}`;
    $("connection-setup-hint").textContent = existing
      ? "准备好已有开放平台应用的 App ID 和 App Secret，执行命令后按提示完成配置。"
      : "执行命令后，按提示在浏览器中创建并配置应用。";
    if ($("connection-setup-command").textContent !== command) {
      $("connection-setup-command").textContent = command;
      $("connection-setup-copy").textContent = "复制命令";
      $("connection-setup-copy-status").textContent = "";
    }
  }

  function closeLarkControls() {
    $("connection-application-picker").hidden = true;
    $("connection-application-change").setAttribute("aria-expanded", "false");
    $("connection-account-manage").open = false;
  }

  function larkApplicationLabel(option) {
    const setup = connectionsUI.platforms.larkSetupResult;
    if (option && setup?.cli_profile?.profile === option.name) return setup.app_name || setup.app_id || option.name;
    return option?.appId || option?.name || "尚未选择应用";
  }

  function renderLarkAccount() {
    settingsUI.renderSettingsNavigation();
    const option = connectionsUI.platforms.discovery?.profiles.find(p => p.name === $("connection-profile").value);
    const active = !!connectionsUI.platforms.auth || !!connectionsUI.platforms.larkStarting, pending = !!connectionsUI.platforms.larkPending;
    const setup = connectionsUI.platforms.larkSetupResult, hasProfiles = !!connectionsUI.platforms.discovery?.profiles.length;
    const blocked = !!setup && setup.stage !== "authorization" && setup.status !== "succeeded";
    const incomplete = !!setup && setup.status !== "succeeded";
    const application = (connectionsUI.platforms.auth || connectionsUI.platforms.larkStarting)?.stage === "application";
    const v = connectionsUI.platforms.selected?.validation;
    const same = !!connectionsUI.platforms.selected && JSON.stringify(larkProfileDraft()) === JSON.stringify(connectionsUI.platforms.selected.cli_profile);
    const expired = !!option?.user && ["expired", "error", "no_token"].includes(option.tokenStatus);
    const changedUser = same && v?.status === "valid" && !!option?.user && option.user !== v.username;
    const flowVerified = setup?.status === "succeeded" && setup.username === option?.user;
    const loginVerified = connectionsUI.platforms.larkAccountVerified?.username === option?.user && JSON.stringify(connectionsUI.platforms.larkAccountVerified?.profile) === JSON.stringify(larkProfileDraft());
    const verified = !expired && !changedUser && (same && v?.status === "valid" || flowVerified || loginVerified);
    const needsAuth = !option?.user || expired || setup?.stage === "authorization" && incomplete;
    const needsRepair = !!connectionsUI.platforms.larkDiscoveryError || !!connectionsUI.platforms.larkSetupUncertain || expired || same && v?.status === "failed" || connectionBrand() === "lark" && !hasProfiles && !!connectionsUI.platforms.discovery;
    if (active || pending || connectionsUI.platforms.detecting) closeLarkControls();

    $("connection-application").hidden = !hasProfiles && !setup?.configuration_found && !active;
    $("connection-application-name").textContent = application ? "正在配置…" : setup?.configuration_found && active ? setup.app_name || setup.app_id : option ? larkApplicationLabel(option) : setup?.app_name || setup?.app_id || (connectionsUI.platforms.selected ? "原应用不可用，请重新选择" : "尚未选择应用");
    $("connection-application-change").textContent = option ? "更换…" : "选择…";
    $("connection-application-change").disabled = active || pending || connectionsUI.platforms.detecting;
    $("connection-profile-choice").hidden = !hasProfiles;
    $("connection-setup-other").hidden = connectionBrand() !== "feishu";
    $("connection-setup-other").disabled = active || pending || connectionsUI.platforms.detecting || connectionsUI.platforms.larkSetupUncertain;
    $("connection-account-section").hidden = !option && !active && !connectionsUI.platforms.detecting && !connectionsUI.platforms.larkDiscoveryError;
    $("connection-account-manage").hidden = !option?.user || active || pending || connectionsUI.platforms.detecting || blocked;
    $("connection-reauthorize").hidden = needsAuth;
    $("connection-account-reverify").hidden = !connectionsUI.platforms.selected || !same;
    $("connection-account-reverify").disabled = active || pending || connectionsUI.platforms.detecting;
    $("connection-logout").hidden = !option?.user || active || blocked || connectionsUI.platforms.detecting;
    $("connection-login").hidden = !option || !needsAuth || active || blocked || connectionsUI.platforms.detecting;
    $("connection-login").textContent = setup?.stage === "authorization" && incomplete ? "继续账号授权" : expired ? "重新授权" : `登录${connectionBrand() === "lark" ? "Lark" : "飞书"}`;
    for (const id of ["connection-profile", "connection-executable", "connection-detect", "connection-login", "connection-logout", "connection-reauthorize"]) $(id).disabled = active || pending || connectionsUI.platforms.detecting;
    $("connection-name").disabled = active || pending;
    $("connection-provider").disabled = active || pending || !!connectionsUI.platforms.selected || onboardingUI.onboarding.active && !!onboardingUI.onboarding.pending;
    $("connection-save").disabled = active || pending || incomplete || connectionsUI.platforms.detecting || !!connectionsUI.platforms.selected && !connectionsUI.connectionDirty() && !onboardingUI.onboarding.active;
    $("connection-save").textContent = onboardingUI.onboarding.active ? $("onboarding-only").checked ? "连接并核验" : "连接并启用" : connectionsUI.platforms.selected ? "保存更改" : "保存连接";
    $("connection-save").hidden = !connectionsUI.platforms.selected && (!option?.user || active || incomplete);
    $("connection-actions").hidden = !connectionsUI.platforms.selected && $("connection-save").hidden;
    $("connection-verify").hidden = true;
    $("connection-notes").hidden = true;
    $("connection-validation").hidden = true;
    $("connection-delete").disabled = active || pending || !!connectionsUI.platforms.selected?.references?.length;
    $("connection-login-cancel").hidden = !connectionsUI.platforms.auth;
    $("connection-login-cancel").textContent = connectionsUI.platforms.auth?.action === "setup" ? "取消本次连接" : "取消本次登录";
    $("connection-guided").hidden = active || pending || connectionsUI.platforms.detecting || !connectionsUI.platforms.discovery || hasProfiles || !!setup;
    $("connection-guided-hint").textContent = connectionBrand() === "lark" ? "Lark 网页配置尚未验证，请展开「修复连接」配置本机应用。" : "通过官方页面创建新应用或连接已有应用，无需手动填写凭据。";
    $("connection-setup-start").hidden = connectionBrand() !== "feishu";
    $("connection-setup-start").textContent = "配置并连接";
    $("connection-setup-start").disabled = active || pending || connectionsUI.platforms.detecting || connectionsUI.platforms.larkSetupUncertain;
    $("connection-recheck").hidden = active || pending || !connectionsUI.platforms.larkSetupUncertain;
    $("connection-recheck").disabled = connectionsUI.platforms.detecting;
    $("connection-advanced").hidden = active || pending || connectionsUI.platforms.detecting || !needsRepair;
    if (!needsRepair) $("connection-advanced").open = false;
    $("connection-setup").hidden = hasProfiles && !blocked;
    $("connection-detect").textContent = connectionsUI.platforms.detecting ? "正在检测…" : "重新检测";
    renderLarkSetupCommand();

    renderLarkCapabilities(!!option?.user && !expired && !changedUser && !incomplete && !active && !pending && !connectionsUI.platforms.detecting);
    connectionsUI.renderPlatformReferences();

    const account = $("connection-account"), status = $("connection-account-state");
    status.hidden = true; status.textContent = ""; status.dataset.kind = "status";
    if (active) {
      account.textContent = application ? connectionsUI.platforms.auth?.qrURL ? "等待你完成应用配置…" : "正在准备应用配置页面…" : (connectionsUI.platforms.auth || connectionsUI.platforms.larkStarting).action === "logout" ? "正在退出登录…" : connectionsUI.platforms.auth?.qrURL ? "等待你完成账号授权…" : "正在准备账号授权…";
    } else if (connectionsUI.platforms.detecting) account.textContent = "正在检测…";
    else if (!connectionsUI.platforms.discovery) account.textContent = "检测失败，请展开「修复连接」检查本机工具。";
    else if (blocked) account.textContent = "应用配置待核对";
    else if (setup?.stage === "authorization" && incomplete) account.textContent = "账号授权未完成";
    else {
      account.textContent = option?.user || "尚未登录";
      if (option?.user) {
        status.hidden = false;
        status.textContent = expired ? "需要重新授权" : changedUser ? "账号已变化" : verified ? "已验证" : same && v?.status === "failed" ? "验证失败" : "已登录，待验证";
        status.dataset.kind = verified ? "success" : expired || changedUser || v?.status === "failed" ? "error" : "status";
      }
    }
  }

  const capabilityNames = {"chat.read":"会话读取","message.read":"消息读取","notification.private":"私聊通知","notification.group":"群通知","token.renew":"自动续期","sender.name":"发送者姓名","people.search":"人员搜索","message.reply":"普通回复","message.send":"指定群发送","message.send_self":"本人投递"};
  const capabilityReasons = {application_scopes_missing:"需管理员补权",user_scopes_missing:"需补全授权",account_pinning_unavailable:"需重新核验账号",refresh_token_missing:"无法自动续期",refresh_token_expired:"续期已过期",check_missing:"待核验",check_expired:"检查已过期"};
  let capabilityView = null;

  function capabilityKey() {return JSON.stringify([connectionsUI.platforms.formVersion,connectionsUI.platforms.selected?.id,connectionsUI.platforms.selected?.version,larkProfileDraft()]);}

  function renderLarkCapabilities(available) {
    $("connection-capabilities").hidden = !available;
    if (!available) return;
    const key = capabilityKey();
    if (capabilityView?.key !== key) {
      capabilityView = {key,loading:true};
      void checkLarkCapabilities(key);
    }
    const view = capabilityView, check = view.check;
    const capabilities = check?.capabilities || [], gaps = capabilities.filter(c=>c.state!=="ready");
    const blocked = gaps.filter(c=>c.state==="blocked"), unsupported = gaps.some(c=>c.state==="unsupported");
    const status = $("connection-capability-status");
    status.textContent = view.loading ? "正在检查账号权限…" : !capabilities.length ? "账号权限暂未确认" : !gaps.length ? "账号权限已就绪" : blocked.length ? `${blocked.length} 项权限待处理` : unsupported ? "部分能力当前不可用" : "部分权限待核验";
    status.dataset.kind = !view.loading && capabilities.length && !gaps.length ? "success" : !view.loading && blocked.length ? "error" : "status";
    $("connection-capability-list").replaceChildren(...capabilities.map(c=>{
      const row=document.createElement("li"),name=document.createElement("span"),value=document.createElement("span");
      name.textContent=capabilityNames[c.name] || c.name;
      value.textContent=c.state==="ready" ? "已就绪" : c.state==="unsupported" ? "当前服务不支持" : capabilityReasons[c.reason_code] || "待核验";
      value.dataset.kind=c.state==="ready" ? "success" : c.state==="blocked" ? "error" : "status";
      row.append(name,value);return row;
    }));
    $("connection-capability-details").hidden = view.loading || !capabilities.length;
    $("connection-authorize").hidden = !check?.capabilities?.some(c=>c.next_action === "authorize_user");
    $("connection-capability-check").hidden = view.loading;
    const app = $("connection-application-permissions"), namespace = check?.account?.id?.namespace || "";
    const appID = namespace.startsWith("lark:open_id:") ? namespace.slice("lark:open_id:".length) : "";
    app.hidden = !check?.capabilities?.some(c=>c.next_action === "configure_application") || !/^cli_[a-zA-Z0-9_]+$/.test(appID);
    if (!app.hidden) app.href = `${connectionOrigin()}/app/${encodeURIComponent(appID)}/auth`;
    else app.removeAttribute("href");
  }

  async function checkLarkCapabilities(key = capabilityKey()) {
    const view = capabilityView = {key,loading:true};
    try {
      const check = await request("/v1/platforms/lark/permissions",connectionsUI.connectionPayload({cli_profile:larkProfileDraft()}),undefined,65000);
      if (capabilityView !== view || key !== capabilityKey()) return;
      view.check = check;
      setTimeout(()=>{if(capabilityView === view && key === capabilityKey()){view.check=null;renderLarkAccount();}},60000);
    } catch { if (capabilityView !== view || key !== capabilityKey()) return; view.check = null; }
    finally { if (capabilityView === view && key === capabilityKey()) { view.loading=false;renderLarkAccount(); } }
  }

  async function detectLarkPlatform({profile, exact = false, preserveSetup = false} = {}) {
    if (!connectionsUI.connectionIsLark() || connectionsUI.platforms.auth || connectionsUI.platforms.larkStarting) return;
    clearLarkConfirmation();
    const version = ++connectionsUI.platforms.formVersion, brand = connectionBrand();
    const selected = profile ?? connectionsUI.platforms.larkSetupResult?.cli_profile?.profile ?? $("connection-profile").value;
    exact ||= !!connectionsUI.platforms.larkSetupUncertain;
    connectionsUI.platforms.detecting = true; connectionsUI.platforms.larkDiscoveryError = null; connectionsUI.platforms.discovery = null; renderLarkAccount();
    try {
      const data = await request("/v1/platforms/lark/discover", connectionsUI.connectionPayload({ executable: $("connection-executable").value.trim(), brand }), undefined, 40000);
      if (version !== connectionsUI.platforms.formVersion) return;
      connectionsUI.platforms.discovery = data;
      $("connection-executable").value = data.executable;
      const labels = data.profiles.map(p => larkApplicationLabel(p));
      const options = data.profiles.map((p, i) => new Option(labels.filter(label => label === labels[i]).length > 1 ? `${labels[i]} · 配置 ${p.name}` : labels[i], p.name));
      if (exact || data.profiles.length !== 1 || selected && !data.profiles.some(p => p.name === selected)) options.unshift(new Option("选择应用配置", ""));
      setSelectOptions("connection-profile", options, data.profiles.some(p => p.name === selected) ? selected : selected || exact ? "" : data.profiles.length === 1 ? data.profiles[0].name : "");
      $("connection-profile-choice").hidden = !data.profiles.length;
      if (!preserveSetup) resetLarkSetup();
      connectionsUI.connectionFeedback();
    } catch (error) {
      if (version !== connectionsUI.platforms.formVersion) return;
      connectionsUI.platforms.larkDiscoveryError = error.code || "query_failed";
      $("connection-advanced").open = true;
      connectionsUI.connectionFeedback(connectionsUI.connectionValidationErrors[error.code] || "账号检测失败，请核对 CLI 配置或网络后重试。", "error");
    } finally {
      if (version === connectionsUI.platforms.formVersion) { connectionsUI.platforms.detecting = false; renderLarkAccount(); }
    }
  }

  async function cancelLarkAuth(keepalive = false) {
    clearLarkConfirmation();
    const auth = connectionsUI.platforms.auth;
    clearLarkAuthView(); connectionsUI.platforms.formVersion++;
    const version = connectionsUI.platforms.formVersion;
    if (!auth) return;
    // Stop local polling immediately. Cancellation is never a logout/revocation.
    connectionsUI.platforms.auth = null;
    if (auth.action === "setup") connectionsUI.platforms.larkSetupUncertain = true;
    let result;
    try {
      const body = {...auth.owner, session_id: auth.session_id};
      if (keepalive) await globalThis.fetch("/v1/platforms/lark/cancel", {method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body),credentials:"same-origin",keepalive:true});
      else result = await request("/v1/platforms/lark/cancel", body);
      if (version !== connectionsUI.platforms.formVersion) return;
      if (result && auth.action === "setup") rememberLarkSetup(result);
    }
    catch { if (version === connectionsUI.platforms.formVersion) connectionsUI.connectionFeedback("取消结果未确认，请重新检测。", "error"); }
    if (version === connectionsUI.platforms.formVersion) renderLarkAccount();
    return result;
  }

  function larkAuthButton(action) {return action === "logout" ? "connection-logout" : "connection-login";}

  function clearLarkConfirmation() {
    const pending = connectionsUI.platforms.larkPending;
    connectionsUI.platforms.larkPending = null;
    if (pending) renderLarkAccount();
  }

  async function startLarkAuth(action) {
    if (connectionsUI.platforms.sending || connectionsUI.platforms.auth || connectionsUI.platforms.larkPending || connectionsUI.platforms.larkStarting || connectionsUI.platforms.detecting || !connectionsUI.platforms.discovery) return;
    const profile = larkProfileDraft();
    if (!profile.profile) return;
    const affected = connectionsUI.platforms.items.filter(c => c.provider === "lark" && c.cli_profile?.profile === profile.profile);
    const pending = {action,profile,version:connectionsUI.platforms.formVersion,
      affected:affected.map(c => ({id:c.id,version:c.version,referenced_rooms:(c.references || []).map(({room_id,room_instance}) => ({room_id,room_instance}))})),
      title:action === "logout" ? "退出登录将暂停以上房间的通知。" : "授权会暂停相关通知，原账号验证后自动恢复。",
      confirmLabel:action === "logout" ? "确认退出" : "继续授权",
      description:`申请当前功能所需的完整权限，具体发送仍需成员许可与指令。共用此应用配置的 CLI 和其他工具也会受影响。${action === "logout" ? "退出登录不等于取消应用的全部授权。" : ""}`};
    if (action === "logout" || affected.length) {
      connectionsUI.platforms.larkPending = pending;
      connectionsUI.connectionFeedback();renderLarkAccount();$("connection-auth-confirm").focus();
      return;
    }
    await beginLarkAuth(pending);
  }

  async function startLarkSetup() {
    if (!connectionsUI.connectionIsLark() || connectionBrand() !== "feishu" || connectionsUI.platforms.sending || connectionsUI.platforms.auth || connectionsUI.platforms.larkStarting || connectionsUI.platforms.larkPending || connectionsUI.platforms.detecting || !connectionsUI.platforms.discovery || connectionsUI.platforms.larkSetupUncertain) return;
    if (!$("connection-name").value.trim()) {connectionsUI.connectionInputError("connection-name", "请输入连接名称。");return;}
    resetLarkSetup();
    await beginLarkAuth({action:"setup",stage:"application",version:connectionsUI.platforms.formVersion});
  }

  async function beginLarkAuth(pending) {
    const {action,profile,version,affected} = pending;
    if (!connectionsUI.connectionIsLark() || connectionsUI.platforms.sending || connectionsUI.platforms.auth || connectionsUI.platforms.larkStarting || version !== connectionsUI.platforms.formVersion) return;
    connectionsUI.platforms.larkStarting = pending;
    clearLarkConfirmation();
    connectionsUI.platforms.sending = true; $("connection-fields").disabled = true; $("connection-back").disabled = true; $("close-settings").disabled = true;
    renderLarkAccount();
    connectionsUI.connectionFeedback(action === "setup" ? "正在准备配置页面…" : action === "logout" ? "正在退出登录…" : "正在发起授权…");
    const owner = connectionsUI.connectionPayload({});
    try {
      if(onboardingUI.onboarding.active && ["login","setup"].includes(action))await onboardingUI.prepareOnboarding();
      const payload = action === "setup" ? {executable:$("connection-executable").value.trim(),brand:connectionBrand()} : {cli_profile:profile,affected};
      const result = await request(`/v1/platforms/lark/${action}`, {...owner,...payload}, undefined, 40000);
      if (version !== connectionsUI.platforms.formVersion) {
        void request("/v1/platforms/lark/cancel", {...owner,session_id:result.session_id}).catch(() => {});
        return;
      }
      for (const c of result.platforms || []) connectionsUI.updatePlatformItem(c);
      connectionsUI.platforms.auth = {session_id:result.session_id,action,profile,affected,owner,stage:result.stage};
      connectionsUI.platforms.larkStarting = null;
      $("connection-account").textContent = action === "logout" ? "正在退出登录…" : "正在准备授权页面…";
      await pollLarkAuth();
    } catch (error) {
      if (version !== connectionsUI.platforms.formVersion) return;
      if (action === "setup") {
        connectionsUI.platforms.larkSetupUncertain = true;
        connectionsUI.connectionFeedback(connectionsUI.connectionValidationErrors[error.code] || (error.code === "busy" ? "已有操作正在处理，请稍后重新检测。" : error.code === "setup_brand_unverified" ? "此品牌的网页配置尚未验证，请展开「修复连接」检查本机应用。" : "配置结果未确认，请先重新检测。"), "error");
        return;
      }
      connectionsUI.connectionFeedback(error.code === "conflict" ? "连接或引用范围已变化，请返回列表重新加载。" : error.code === "busy" ? "操作尚未结束，请稍后重新检测。" : "授权结果未确认，请重新检测。", "error");
    } finally {
      connectionsUI.platforms.larkStarting = null;
      connectionsUI.platforms.sending = false; settingsUI.renderSettingsNavigation(); $("connection-fields").disabled = false; $("connection-back").disabled = false; $("close-settings").disabled = false;
      renderLarkAccount();
    }
  }

  async function pollLarkAuth() {
    const auth = connectionsUI.platforms.auth;
    if (!auth) return;
    let finished = false, version = connectionsUI.platforms.formVersion;
    try {
      const result = await request("/v1/platforms/lark/status", {...auth.owner,session_id:auth.session_id});
      if (connectionsUI.platforms.auth !== auth || version !== connectionsUI.platforms.formVersion) return;
      if (auth.action === "setup" && (result.configuration_found || !["starting", "waiting"].includes(result.status))) rememberLarkSetup(result);
      if (result.status === "starting" || result.status === "waiting") {
        const application = result.stage === "application";
        // Remove the preceding stage's link and QR, including during OAuth startup.
        if (auth.stage !== result.stage || !result.verification_url || !result.qrcode_url) {clearLarkAuthView();auth.qrURL = null;}
        auth.stage = result.stage;
        $("connection-login-hint").textContent = application ? "创建或选择应用，完成后自动继续授权。" : "打开页面或扫码授权账号。";
        $("connection-login-link").textContent = application ? "打开配置页面 ↗" : "打开授权页面 ↗";
        $("connection-login-qr").alt = application ? "飞书应用配置二维码" : "飞书 / Lark 账号授权二维码";
        if (result.verification_url && result.qrcode_url) {
          $("connection-login-link").href = result.verification_url;
          if (auth.qrURL !== result.qrcode_url) { $("connection-login-qr").src = result.qrcode_url; auth.qrURL = result.qrcode_url; }
          $("connection-login-panel").hidden = false;
        }
        renderLarkAccount();
        connectionsUI.connectionFeedback();
        connectionsUI.platforms.authTimer = setTimeout(() => { void pollLarkAuth(); }, 1500);
        return;
      }
      clearLarkAuthView(); connectionsUI.platforms.auth = null; finished = true;
      for (const item of result.platforms || []) connectionsUI.updatePlatformItem(item);
      const setup = auth.action === "setup";
      const detecting = detectLarkPlatform(setup ? {profile:result.cli_profile?.profile || "",exact:true,preserveSetup:true} : {preserveSetup:!!connectionsUI.platforms.larkSetupResult}); version = connectionsUI.platforms.formVersion;
      await detecting;
      if (version !== connectionsUI.platforms.formVersion) return;
      if (result.status === "succeeded") {
        if (auth.action === "logout") {resetLarkSetup();renderLarkAccount();}
        if (setup && (!result.cli_profile || $("connection-profile").value !== result.cli_profile.profile || !connectionsUI.platforms.discovery)) {
          connectionsUI.connectionFeedback("应用配置未确认，连接未保存。请重新检测。", "error");return;
        }
        if ((setup || connectionsUI.platforms.larkSetupResult) && connectionsUI.platforms.discovery?.profiles.find(p => p.name === $("connection-profile").value)?.user !== result.username) {
          rememberLarkSetup({...connectionsUI.platforms.larkSetupResult,status:"failed",stage:"application",error_code:"identity_mismatch"});renderLarkAccount();
          connectionsUI.connectionFeedback("账号与核验结果不符，连接未保存。请重新检测。", "error");return;
        }
        if (setup) {connectionsUI.platforms.larkSetupUncertain = false;$("connection-advanced").open = false;renderLarkAccount();}
        else if (auth.action === "login" && connectionsUI.platforms.larkSetupResult) {connectionsUI.platforms.larkSetupResult = {...connectionsUI.platforms.larkSetupResult,status:"succeeded",username:result.username};connectionsUI.platforms.larkSetupUncertain = false;renderLarkAccount();}
        if (auth.action === "login" && connectionsUI.platforms.discovery?.profiles.find(p => p.name === auth.profile.profile)?.user === result.username) {
          connectionsUI.platforms.larkAccountVerified = {profile:auth.profile,username:result.username};renderLarkAccount();
        }
        const savedProfile = connectionsUI.platforms.selected && JSON.stringify(auth.profile) === JSON.stringify(connectionsUI.platforms.selected.cli_profile);
        const sharedCount = (auth.affected || []).filter(c => c.id !== connectionsUI.platforms.selected?.id).length;
        const message = auth.action === "logout" ? "已退出登录。" : savedProfile ? "账号已验证。" : "账号已验证，请保存连接。";
        const shared = auth.action !== "logout" && sharedCount ? `已核对 ${sharedCount} 个共享连接；本人投递需在对应成员中重新核验。` : "";
        const renewal = result.renewal_unavailable ? "登录无法自动续期，请联系应用管理员。" : "";
        const detail = auth.action === "logout" ? "退出登录不等于取消应用的全部授权。如需完全取消，请前往飞书 / Lark 授权管理页。" : result.renewal_unavailable ? () => `登录将于 ${settingsUI.formatDate(result.expires_at)} 到期。请应用管理员在开发者后台开启「刷新 user_access_token」并发布新版本，再重新授权。` : "";
        connectionsUI.connectionFeedback(message + shared + renewal, result.renewal_unavailable ? "status" : "success", detail);
      } else connectionsUI.connectionFeedback(setup ? larkSetupFailure(result) : result.status === "cancelled" ? "登录已取消，请重新检测。" : connectionsUI.connectionValidationErrors[result.error_code] || "认证失败或已过期，请重新发起。", "error");
    } catch (error) {
      if (finished) { if (version === connectionsUI.platforms.formVersion) connectionsUI.connectionFeedback("授权已完成，验证结果未确认。请重新验证。", "error"); return; }
      if (connectionsUI.platforms.auth !== auth) return;
      // Ensure an unobserved flow cannot keep changing credentials later.
      const cancelVersion = connectionsUI.platforms.formVersion + 1;
      await cancelLarkAuth();
      if (cancelVersion !== connectionsUI.platforms.formVersion) return;
      connectionsUI.connectionFeedback(auth.action === "setup" ? "配置或授权结果未确认，请重新检测。" : "授权结果未确认，请重新检测。", "error");
    }
  }

  function larkProfileDraft() {return {version:1, executable:$("connection-executable").value.trim(), profile:$("connection-profile").value.trim(), brand:connectionBrand(), cli_version:"1.0.99"};}

  function mount() {
    $("connection-profile").addEventListener("change", () => {clearLarkConfirmation();resetLarkSetup();closeLarkControls();connectionsUI.connectionFeedback();renderLarkAccount();$("connection-application-change").focus();});

    $("connection-detect").addEventListener("click", detectLarkPlatform);

    $("connection-recheck").addEventListener("click", detectLarkPlatform);

    $("connection-setup-start").addEventListener("click", startLarkSetup);

    $("connection-setup-other").addEventListener("click", startLarkSetup);

    $("connection-application-change").addEventListener("click", () => {
      if (connectionsUI.platforms.auth || connectionsUI.platforms.larkStarting || connectionsUI.platforms.larkPending || connectionsUI.platforms.detecting || connectionsUI.platforms.sending) return;
      const open = $("connection-application-picker").hidden;
      $("connection-application-picker").hidden = !open;
      $("connection-application-change").setAttribute("aria-expanded", String(open));
      $("connection-account-manage").open = false;
      if (open) $("connection-profile").focus();
    });

    $("connection-application-dismiss").addEventListener("click", () => {closeLarkControls();$("connection-application-change").focus();});

    $("connection-application-picker").addEventListener("keydown", event => {if(event.key === "Escape") {event.preventDefault();event.stopPropagation();closeLarkControls();$("connection-application-change").focus();}});

    $("connection-account-manage").addEventListener("toggle", () => {if ($("connection-account-manage").open) {$("connection-application-picker").hidden = true;$("connection-application-change").setAttribute("aria-expanded", "false");}});

    $("connection-account-manage").addEventListener("keydown", event => {if (event.key === "Escape") {event.preventDefault();event.stopPropagation();$("connection-account-manage").open = false;$("connection-account-manage").querySelector("summary").focus();}});

    document.addEventListener("pointerdown", event => {if ($("connection-account-manage").open && !event.target.closest?.("#connection-account-manage")) $("connection-account-manage").open = false;});

    $("connection-reauthorize").addEventListener("click", () => startLarkAuth("login"));

    $("connection-account-reverify").addEventListener("click", () => {closeLarkControls();return connectionsUI.connectionAction("verify");});

    for (const id of ["connection-setup-new", "connection-setup-existing"]) $(id).addEventListener("change", renderLarkSetupCommand);

    $("connection-setup-copy").addEventListener("click", async () => {
      const command = $("connection-setup-command").textContent, version = connectionsUI.platforms.formVersion;
      try {
        if (!window.navigator?.clipboard?.writeText) throw new Error("Clipboard unavailable");
        await window.navigator.clipboard.writeText(command);
        if (version !== connectionsUI.platforms.formVersion || command !== $("connection-setup-command").textContent) return;
        $("connection-setup-copy").textContent = "已复制";
        $("connection-setup-copy-status").textContent = "";
      } catch {
        if (version !== connectionsUI.platforms.formVersion || command !== $("connection-setup-command").textContent) return;
        $("connection-setup-copy-status").textContent = "无法自动复制，请选中上方命令复制。";
      }
    });

    $("connection-executable").addEventListener("change", detectLarkPlatform);

    $("connection-login").addEventListener("click", () => startLarkAuth("login"));

    $("connection-authorize").addEventListener("click",()=>startLarkAuth("login"));
    $("connection-capability-check").addEventListener("click",()=>{void checkLarkCapabilities();renderLarkAccount();});

    $("connection-logout").addEventListener("click", () => startLarkAuth("logout"));

    $("connection-login-cancel").addEventListener("click", async () => {
      const setup = connectionsUI.platforms.auth?.action === "setup";
      const cancelVersion = connectionsUI.platforms.formVersion + 1;
      const result = await cancelLarkAuth();
      if (cancelVersion !== connectionsUI.platforms.formVersion) return;
      const detecting = detectLarkPlatform(setup ? {profile:result?.cli_profile?.profile || "",exact:true,preserveSetup:true} : {}), version = connectionsUI.platforms.formVersion;
      await detecting;
      if (version !== connectionsUI.platforms.formVersion) return;
      if (setup && result?.status === "succeeded") {
        const option = connectionsUI.platforms.discovery?.profiles.find(p => p.name === $("connection-profile").value);
        if (!result.cli_profile || option?.name !== result.cli_profile.profile || option?.user !== result.username) {
          rememberLarkSetup({...result,status:"failed",stage:"application",error_code:"identity_mismatch"});renderLarkAccount();
          connectionsUI.connectionFeedback("流程已结束，核验结果未确认。请重新检测。", "error");return;
        }
        connectionsUI.platforms.larkSetupUncertain = false;$("connection-advanced").open = false;renderLarkAccount();
        connectionsUI.connectionFeedback("取消前授权已完成，请核对账号并保存连接。", "success");return;
      }
      connectionsUI.connectionFeedback(setup ? result ? larkSetupFailure(result) : "取消结果未确认，请重新检测。" : "已请求取消，请重新检测。", setup ? "error" : "status", "取消只停止等待，不撤销已完成的应用配置或账号授权。");
    });
  }

  return {
    mount,
    connectionOrigin,
    renderLarkAccount,
    detectLarkPlatform,
    cancelLarkAuth,
    beginLarkAuth,
    clearLarkConfirmation,
    larkAuthButton,
    clearLarkAuthView,
    resetLarkSetup,
    closeLarkControls,
    larkProfileDraft,
    connectionBrand
  };
}
