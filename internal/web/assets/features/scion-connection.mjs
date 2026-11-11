// Scion account discovery, PAT login and logout lifecycle.
export function createScionConnection({ env, runtime, connectionsUI, settingsUI, onboardingUI }) {
  const { Option, setTimeout, clearTimeout, request } = env;
  const { $, setSelectOptions } = runtime;

  function renderScionPATLink() {
    const link = $("connection-scion-pat-link"), origin = $("connection-url").value.trim();
    let href = "";
    try {
      const url = new URL(origin);
      if (connectionsUI.connectionIsScion() && url.protocol === "https:" && !url.username && !url.password &&
          url.pathname === "/" && !origin.includes("?") && !origin.includes("#") && !/[\s\\]/.test(origin)) {
        href = new URL("/profile?tab=tokens", url.origin).href;
      }
    } catch { /* Wait for a valid instance address. */ }
    link.href = href;
    link.hidden = !href;
  }

  function scionProfileDraft() { return {version:1, executable:$("connection-scion-executable").value.trim(), profile:$("connection-scion-profile").value.trim(), brand:"scion", cli_version:"0.1.13"}; }

  function renderScionAccount() {
    settingsUI.renderSettingsNavigation();
    $("connection-save").textContent=onboardingUI.onboarding.active ? $("onboarding-only").checked ? "连接并核验" : "连接并启用" : "保存连接";
    const active = !!connectionsUI.platforms.scionAuth || !!connectionsUI.platforms.scionLogout, pending = !!connectionsUI.platforms.scionPending;
    for (const id of ["connection-scion-executable", "connection-scion-profile", "connection-scion-detect", "connection-scion-login-open", "connection-scion-login", "connection-scion-login-dismiss", "connection-scion-pat", "connection-scion-logout"]) $(id).disabled = active || pending || connectionsUI.platforms.detecting || connectionsUI.platforms.sending;
    $("connection-scion-login-open").setAttribute("aria-expanded",String(!$("connection-scion-login-panel").hidden));
    $("connection-scion-login-dismiss").hidden = active;
    $("connection-url").readOnly = !!connectionsUI.platforms.selected || active || pending || onboardingUI.onboarding.active && !!onboardingUI.onboarding.pending;
    $("connection-provider").disabled = !!connectionsUI.platforms.selected || active || pending || onboardingUI.onboarding.active && !!onboardingUI.onboarding.pending;
    $("connection-name").disabled = pending || connectionsUI.platforms.sending;
    $("connection-scion-cancel").hidden = !connectionsUI.platforms.scionAuth;
    $("connection-save").disabled = active || pending || connectionsUI.platforms.detecting || connectionsUI.platforms.sending;
    $("connection-verify").disabled = active || pending || connectionsUI.platforms.detecting || connectionsUI.platforms.sending;
    $("connection-delete").disabled = active || pending || connectionsUI.platforms.sending || !!connectionsUI.platforms.selected?.references?.length;
    const selected = connectionsUI.platforms.scionDiscovery?.profiles.find(p => p.profile === $("connection-scion-profile").value);
    $("connection-scion-login-open").textContent = selected ? "重新登录" : "登录";
    $("connection-scion-logout").hidden = !selected;
    $("connection-scion-account").title = "";
    const status = $("connection-scion-status");
    status.hidden = true; status.textContent = ""; status.title = ""; status.dataset.kind = "status";
    connectionsUI.renderPlatformReferences();
    if (active) { $("connection-scion-account").textContent = connectionsUI.platforms.scionLogout ? "正在退出登录…" : "正在登录并核验账号…"; return; }
    if (connectionsUI.platforms.detecting) { $("connection-scion-account").textContent = "正在检测账号…"; return; }
    $("connection-scion-account").textContent = selected ? `已识别账号 · ${selected.username}` : !$("connection-url").value.trim() ? "填写实例地址后自动检测账号。" : connectionsUI.platforms.scionDiscovery ? connectionsUI.platforms.scionDiscovery.profiles.length ? "请选择账号，或点击登录。" : "尚未登录，请点击登录。" : "请刷新账号以完成检测。";
    if (!selected) return;
    const item = connectionsUI.platforms.selected, validation = item?.validation;
    const same = item?.provider === "scion" && JSON.stringify(scionProfileDraft()) === JSON.stringify(item.cli_profile);
    status.hidden = false;
    if (!item) status.textContent = "保存连接后自动验证账号。";
    else if (!same) status.textContent = "账号配置已更改，保存后自动验证。";
    else if (validation?.status === "valid" && selected.username !== validation.username) {
      status.textContent = "账号信息已变化，请重新验证。"; status.dataset.kind = "error";
    } else if (validation?.status === "valid") {
      $("connection-scion-account").textContent = `已连接 · ${selected.username}`;
      $("connection-scion-account").title = $("connection-validation").title;
      status.hidden = true;
    } else if (validation?.status === "failed") {
      status.textContent = connectionsUI.connectionValidationErrors[validation.error_code] || "验证失败，请重新验证连接。"; status.dataset.kind = "error";
    } else status.textContent = "尚未验证连接。";
  }

  function scionProfileOptions(data, selected) {
    const users = new Map();
    for (const p of data.profiles) users.set(p.username, (users.get(p.username) || 0) + 1);
    const options = data.profiles.map(p => new Option(users.get(p.username) > 1 ? `${p.username} · ${p.profile.slice(-6)}` : p.username, p.profile));
    if (data.profiles.length !== 1 || selected && !data.profiles.some(p => p.profile === selected)) options.unshift(new Option("选择账号", ""));
    setSelectOptions("connection-scion-profile", options, data.profiles.some(p => p.profile === selected) ? selected : !selected && data.profiles.length === 1 ? data.profiles[0].profile : "");
    $("connection-scion-choice").hidden = !data.profiles.length || data.profiles.length === 1 && (!selected || data.profiles.some(p => p.profile === selected));
  }

  async function detectScionPlatform() {
    if (!connectionsUI.connectionIsScion() || connectionsUI.platforms.scionAuth) return;
    clearScionConfirmation();
    const version = ++connectionsUI.platforms.formVersion, url = $("connection-url").value.trim();
    const selected = $("connection-scion-profile").value;
    connectionsUI.platforms.scionDiscovery = null;
    if (!url) { renderScionAccount(); return; }
    connectionsUI.platforms.detecting = true; renderScionAccount();
    try {
      const data = await request("/v1/platforms/scion/discover", connectionsUI.connectionPayload({url, executable:$("connection-scion-executable").value.trim()}), undefined, 70000);
      if (version !== connectionsUI.platforms.formVersion) return;
      connectionsUI.platforms.scionDiscovery = {...data, origin:url};
      $("connection-scion-executable").value = data.executable;
      scionProfileOptions(data, selected);
      connectionsUI.connectionFeedback(data.unavailable ? "部分账号不可用，请刷新或使用 PAT 登录。" : "");
    } catch (error) {
      if (version !== connectionsUI.platforms.formVersion) return;
      if (["cli_unavailable", "cli_version_mismatch"].includes(error.code)) $("connection-scion-advanced").open = true;
      connectionsUI.connectionFeedback(connectionsUI.connectionValidationErrors[error.code] || (error.code === "request" ? "请填写有效的 HTTPS 实例地址和 CLI 路径。" : "账号检测失败，请核对程序或网络后刷新。"), "error");
    } finally {
      if (version === connectionsUI.platforms.formVersion) { connectionsUI.platforms.detecting = false; renderScionAccount(); }
    }
  }

  async function startScionLogin() {
    if (!connectionsUI.connectionIsScion() || connectionsUI.platforms.scionAuth || connectionsUI.platforms.scionPending || connectionsUI.platforms.sending || connectionsUI.platforms.detecting) return;
    const pat = $("connection-scion-pat").value.trim();
    $("connection-scion-pat").value = "";
    if (!pat || !$("connection-url").value.trim()) { connectionsUI.connectionFeedback("请填写实例地址和 PAT。", "error"); return; }
    try {if(onboardingUI.onboarding.active)await onboardingUI.prepareOnboarding();}
    catch(error){connectionsUI.connectionFeedback(error.code ? "接入目标未确认，请读取接入进度。" : error.message,"error");return;}
    const auth = {session_id:crypto.randomUUID().replaceAll("-", ""), version:connectionsUI.platforms.formVersion, origin:$("connection-url").value.trim()};
    connectionsUI.platforms.scionAuth = auth; renderScionAccount();
    connectionsUI.connectionFeedback("正在通过 Scion CLI 登录…");
    try {
      await request("/v1/platforms/scion/login", connectionsUI.connectionPayload({session_id:auth.session_id,url:$("connection-url").value.trim(),executable:$("connection-scion-executable").value.trim(),pat}), undefined, 15000);
    } catch (error) {
      if (connectionsUI.platforms.scionAuth !== auth) return;
      const rejected = {request:"请填写有效的 HTTPS 实例地址和 PAT。", identity:"服务身份已变化，请刷新页面。", capacity:"登录操作已达上限，请稍后刷新账号。", busy:"服务正忙，请稍后重试登录。"};
      if (rejected[error.code]) { connectionsUI.platforms.scionAuth=null; renderScionAccount(); connectionsUI.connectionFeedback(rejected[error.code],"error"); return; }
      // A lost start response is recovered by ID through status, never by sending
      // the PAT or login again. The operator still owns cancellation on close.
    }
    if (connectionsUI.platforms.scionAuth === auth) await pollScionAuth();
  }

  async function pollScionAuth() {
    const auth = connectionsUI.platforms.scionAuth;
    if (!auth) return;
    try {
      const result = await request("/v1/platforms/scion/status", connectionsUI.connectionPayload({session_id:auth.session_id}));
      if (connectionsUI.platforms.scionAuth !== auth || connectionsUI.platforms.formVersion !== auth.version) return;
      if (result.status === "starting") { connectionsUI.platforms.scionTimer = setTimeout(() => { void pollScionAuth(); }, 1000); return; }
      connectionsUI.platforms.scionAuth = null; clearTimeout(connectionsUI.platforms.scionTimer);
      if (result.status === "succeeded" && result.cli_profile) {
        const profile = result.cli_profile;
        const data = {origin:auth.origin, executable:profile.executable, profiles:(connectionsUI.platforms.scionDiscovery?.profiles || []).filter(p => p.profile !== profile.profile), unavailable:0};
        data.profiles.push({profile:profile.profile,username:result.username});
        connectionsUI.platforms.scionDiscovery = data;
        $("connection-scion-executable").value = profile.executable;
        scionProfileOptions(data, profile.profile);
        $("connection-scion-login-panel").hidden = true;
        renderScionAccount(); connectionsUI.connectionFeedback(`账号已验证，请保存连接。`, "success");
      } else {
        renderScionAccount(); connectionsUI.connectionFeedback(result.status === "cancelled" ? "本次登录已取消。" : connectionsUI.connectionValidationErrors[result.error_code] || "PAT 登录失败，请检查实例、令牌及权限。", "error");
      }
    } catch {
      if (connectionsUI.platforms.scionAuth !== auth) return;
      await cancelScionAuth();
      connectionsUI.connectionFeedback("登录结果未确认，请刷新账号核对。", "error");
    }
  }

  async function cancelScionAuth() {
    const auth = connectionsUI.platforms.scionAuth;
    clearScionConfirmation();
    $("connection-scion-pat").value = "";
    clearTimeout(connectionsUI.platforms.scionTimer);
    if (!auth) return;
    connectionsUI.platforms.scionAuth = null;
    try { await request("/v1/platforms/scion/cancel", connectionsUI.connectionPayload({session_id:auth.session_id}), undefined, 12000); }
    catch { connectionsUI.connectionFeedback("登录取消结果未确认，请稍后刷新账号核对。", "error"); }
    if (connectionsUI.connectionIsScion()) renderScionAccount();
  }

  function clearScionConfirmation() {
    const pending = connectionsUI.platforms.scionPending;
    connectionsUI.platforms.scionPending = null;
    if (pending) renderScionAccount();
  }

  function confirmScionLogout() {
    if (!connectionsUI.connectionIsScion() || connectionsUI.platforms.scionAuth || connectionsUI.platforms.scionPending || connectionsUI.platforms.sending || connectionsUI.platforms.detecting) return;
    const profile = scionProfileDraft(), discovery = connectionsUI.platforms.scionDiscovery;
    const selected = discovery?.profiles.find(p => p.profile === profile.profile);
    if (!selected || discovery.executable !== profile.executable || discovery.origin !== $("connection-url").value.trim()) return;
    const affected = connectionsUI.platforms.items.filter(c => c.provider === "scion" && c.cli_profile?.profile === profile.profile);
    connectionsUI.platforms.scionPending = {version:connectionsUI.platforms.formVersion, profile, discovery, action:"logout",
      title:`退出「${selected.username}」的登录？将停止以上房间中使用此账号的操作。`,
      description:"将清除本地登录凭据，保留连接配置。平台上的 PAT 仍然有效，可在令牌管理页面撤销。", confirmLabel:"确认退出",
      affected:affected.map(c => ({id:c.id, version:c.version, referenced_rooms:(c.references || []).map(({room_id,room_instance}) => ({room_id,room_instance}))}))};
    connectionsUI.connectionFeedback(); renderScionAccount(); $("connection-auth-confirm").focus();
  }

  async function logoutScion() {
    const pending = connectionsUI.platforms.scionPending;
    if (!pending || pending.version !== connectionsUI.platforms.formVersion || !connectionsUI.connectionIsScion() || connectionsUI.platforms.scionAuth || connectionsUI.platforms.sending || connectionsUI.platforms.detecting) return;
    const {version, profile, discovery, affected} = pending;
    connectionsUI.platforms.scionLogout = pending;
    clearScionConfirmation();
    connectionsUI.platforms.sending = true; $("connection-fields").disabled = true;
    $("connection-back").disabled = true; $("close-settings").disabled = true;
    $("connection-scion-pat").value = "";
    renderScionAccount(); connectionsUI.connectionFeedback("正在退出登录…");
    try {
      const result = await request("/v1/platforms/scion/logout", connectionsUI.connectionPayload({url:discovery.origin, cli_profile:profile, affected}), undefined, 15000);
      if (version !== connectionsUI.platforms.formVersion) return;
      if (result.status !== "succeeded" || !Array.isArray(result.platforms)) throw new Error("unconfirmed logout");
      for (const item of result.platforms) connectionsUI.updatePlatformItem(item);
      connectionsUI.platforms.scionDiscovery = {...discovery, profiles:discovery.profiles.filter(p => p.profile !== profile.profile)};
      scionProfileOptions(connectionsUI.platforms.scionDiscovery, profile.profile);
      $("connection-scion-login-panel").hidden = true;
      connectionsUI.connectionFeedback("已退出登录，平台 PAT 仍有效。", "success");
    } catch (error) {
      if (version !== connectionsUI.platforms.formVersion) return;
      connectionsUI.platforms.scionDiscovery = null;
      connectionsUI.connectionFeedback(error.code === "conflict" ? "账号或引用已变化，请返回列表重新加载后重试。" : error.code === "busy" ? "操作尚未结束，请刷新账号后重试。" : "退出结果未确认，请刷新账号核对。", "error");
    } finally {
      connectionsUI.platforms.scionLogout = null;
      connectionsUI.platforms.sending = false; settingsUI.renderSettingsNavigation(); $("connection-fields").disabled = false;
      $("connection-back").disabled = false; $("close-settings").disabled = false;
      if (version === connectionsUI.platforms.formVersion) {
        renderScionAccount();
        if (connectionsUI.platforms.scionDiscovery) $("connection-scion-login-open").focus();
      }
    }
  }

  function mount() {
    $("connection-scion-executable").addEventListener("change", detectScionPlatform);

    $("connection-url").addEventListener("input", renderScionPATLink);

    $("connection-scion-detect").addEventListener("click", detectScionPlatform);

    $("connection-scion-profile").addEventListener("change", () => {clearScionConfirmation();connectionsUI.connectionFeedback();renderScionAccount();});

    $("connection-scion-login").addEventListener("click", startScionLogin);

    $("connection-scion-login-open").addEventListener("click", () => {
      if (connectionsUI.platforms.scionAuth || connectionsUI.platforms.scionPending || connectionsUI.platforms.sending || connectionsUI.platforms.detecting) return;
      $("connection-scion-login-panel").hidden=false;
      renderScionAccount();$("connection-scion-pat").focus();
    });

    $("connection-scion-login-dismiss").addEventListener("click", () => {
      if (connectionsUI.platforms.scionAuth || connectionsUI.platforms.sending) return;
      $("connection-scion-pat").value="";$("connection-scion-login-panel").hidden=true;
      renderScionAccount();$("connection-scion-login-open").focus();
    });

    $("connection-scion-logout").addEventListener("click", confirmScionLogout);

    $("connection-scion-pat").addEventListener("keydown", event => { if(event.key==="Enter" && !event.isComposing) { event.preventDefault(); if(!event.repeat) return startScionLogin(); } });

    $("connection-scion-cancel").addEventListener("click", async () => {await cancelScionAuth();await detectScionPlatform();});
  }

  return {
    mount,
    detectScionPlatform,
    renderScionPATLink,
    cancelScionAuth,
    logoutScion,
    clearScionConfirmation,
    scionProfileDraft
  };
}
