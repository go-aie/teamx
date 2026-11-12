import { assessPassword } from "../password-policy.mjs";

// Service-wide access controls. Credentials live only in forms, never storage.
export function createAccessSettings({env, runtime}) {
  const {window, document, request, setTimeout, clearTimeout} = env;
  const {$, state} = runtime;
  let current, saving = false, generation = 0, editingToken = false, pending = null;
  let localLoginEnabled = false;
  let copyTimer = null;
  const feedback = message => { $("access-settings-feedback").textContent = message; };
  const busy = () => saving || !!pending;

  function showToken(show) {
    $("access-new-token").type = show ? "text" : "password";
    const label = show ? "隐藏令牌" : "显示令牌";
    $("access-token-toggle").setAttribute("aria-pressed", String(show));
    $("access-token-toggle").setAttribute("aria-label", label);
    $("access-token-toggle").title = label;
  }

  function resetCopied() {
    if (copyTimer !== null) clearTimeout(copyTimer);
    copyTimer = null;
    $("access-token-copy").dataset.copied = "false";
    $("access-token-copy").setAttribute("aria-label", "复制令牌");
    $("access-token-copy").title = "复制令牌";
  }

  const modes = ["local", "lan", "proxy"];
  const modeFrom = data => data?.access_mode || (data?.remote_enabled ? "proxy" : "local");
  const selectedMode = () => modes.find(mode => $("access-mode-" + mode).checked) || "local";
  function selectMode(mode) {
    for (const candidate of modes) $("access-mode-" + candidate).checked = candidate === mode;
  }

  function changed() {
    return !!current && ($("access-login-enabled").checked !== current.login_enabled ||
      selectedMode() !== modeFrom(current) ||
      (selectedMode() === "proxy" && $("access-public-url").value.trim() !== current.public_base_url) ||
      (selectedMode() === "lan" && $("access-port").value !== (current.port || "8088")) ||
      ($("access-login-enabled").checked && !!$("access-new-token").value));
  }

  function candidateAssessment() {
    return assessPassword($("access-new-token").value, current?.password_policy);
  }

  function renderPasswordHint() {
    const token = $("access-new-token"), visible = !$("access-token-fields").hidden;
    const assessment = visible && token.value ? candidateAssessment() : null;
    $("access-token-hint").textContent = assessment ? assessment.message : "至少 12 个字符，修改后需重新登录。";
    $("access-token-hint").dataset.strength = assessment?.level || "";
    token.setAttribute("aria-invalid", String(!!assessment?.issue));
    token.setCustomValidity?.(assessment?.issue ? assessment.message : "");
    return assessment;
  }

  function render() {
    const enabled = $("access-login-enabled").checked, mode = selectedMode();
    $("access-settings-form").hidden = !current?.writable;
    $("access-login-enabled").disabled = busy() || current?.login_required || mode !== "local" || current?.remote_active;
    for (const candidate of modes) $("access-mode-" + candidate).disabled = busy();
    $("access-login-hint").textContent = current?.login_required ? "后台服务需要登录验证。" : mode !== "local" ? "此访问方式需要登录验证。" : current?.remote_active ? "切换为仅本机并重启后，才能关闭登录验证。" : "打开 TeamX 时需要输入令牌。";
    $("access-connection-fields").hidden = mode === "local";
    $("access-lan-fields").hidden = mode !== "lan";
    $("access-proxy-fields").hidden = mode !== "proxy";
    $("access-port").disabled = busy() || mode !== "lan";
    $("access-port").required = mode === "lan";
    const port = $("access-port").value;
    const validPort = /^\d+$/.test(port) && Number(port) >= 1 && Number(port) <= 65535;
    const addresses = validPort ? (current?.lan_hosts || []).filter(host => !host.includes(":")).map(host => `http://${host}:${Number(port)}`) : [];
    $("access-lan-addresses").value = addresses.join("\n");
    $("access-lan-addresses").rows = Math.max(1, Math.min(4, addresses.length));
    $("access-lan-copy").disabled = busy() || !addresses.length;
    const lanReady = current?.active_mode === "lan" && !current?.restart_required && modeFrom(current) === "lan" && port === current?.port;
    $("access-lan-address-label").textContent = lanReady ? "访问地址" : "访问地址（待生效）";
    $("access-lan-status").textContent = !validPort ? "请输入有效端口。" : !addresses.length ? "未检测到局域网 IP，请连接局域网后重启 TeamX。" : lanReady ? "同一局域网内的设备可使用此地址登录。" : "保存并重启 TeamX 后可访问。";
    $("access-token-summary").hidden = !enabled || !current?.login_enabled || editingToken;
    $("access-token-fields").hidden = !enabled || (!!current?.login_enabled && !editingToken);
    $("access-token-edit-cancel").hidden = !current?.login_enabled;
    $("access-draft-hint").hidden = !changed();
    for (const id of ["access-public-url", "access-new-token", "access-token-change", "access-token-edit-cancel", "access-token-generate", "access-token-toggle", "access-token-copy"]) $(id).disabled = busy();
    $("access-new-token").disabled = busy() || $("access-token-fields").hidden;
    $("access-new-token").required = !$("access-token-fields").hidden;
    $("access-public-url").disabled = busy() || mode !== "proxy";
    $("access-public-url").required = mode === "proxy";
    const assessment = renderPasswordHint();
    $("access-settings-save").disabled = busy() || !current?.writable || !changed() || !!assessment?.issue;
    $("access-settings-save").textContent = saving ? "保存中…" : "保存更改";
    for (const id of ["access-current-token", "access-confirm-submit", "access-confirm-cancel", "access-confirm-close"]) $(id).disabled = saving;
  }

  function closeConfirmation(restoreFocus = true) {
    const action = pending;
    pending = null;
    $("access-current-token").value = "";
    $("access-confirm-feedback").textContent = "";
    $("access-confirm-dialog").close();
    if (action) { action.body.token = ""; action.body.current_token = ""; }
    if (restoreFocus && action?.opener?.isConnected !== false) action?.opener?.focus();
    runtime.navigationChanged?.();
  }

  function clear() {
    generation++;
    closeConfirmation(false);
    editingToken = false;
    $("access-new-token").value = "";
    showToken(false); resetCopied();
  }

  async function load() {
    clear(); const version = generation;
    try {
      const data = await request("/v1/access/settings");
      if (version !== generation) return;
      current = data;
      localLoginEnabled = !!data.login_enabled;
      $("access-login-enabled").checked = !!data.login_enabled;
      selectMode(modeFrom(data));
      $("access-port").value = data.port || "8088";
      $("access-public-url").value = data.public_base_url || "";
      $("access-settings-login").hidden = true;
      feedback(data.restart_required ? "已保存，重启 TeamX 后生效。" : "");
      render();
    } catch { if(version === generation) feedback("读取失败，请重新打开设置。"); }
  }

  async function generateToken() {
    if (busy()) return;
    const version = generation;
    resetCopied();
    saving = true; render();
    try {
      const result = await request("/v1/access/settings/generate", {caller:{kind:"human",id:state.operator}});
      if (version !== generation) return;
      $("access-new-token").value = result.token;
      feedback("已生成新令牌，请复制后保存。");
    } catch(error) { if(version === generation) feedback(error.message); }
    finally { saving = false; render(); }
  }

  function confirmSave(body) {
    const closing = !body.login_enabled, replacing = !!body.token;
    $("access-confirm-title").textContent = closing ? "关闭登录验证" : replacing ? "更换访问令牌" : "保存访问设置";
    $("access-confirm-message").textContent = closing ? "关闭后，本机打开 TeamX 将无需登录。请输入当前令牌确认。" : replacing ? "更换后，所有浏览器需重新登录。请输入当前令牌确认。" : "请输入当前令牌，确认本次访问设置更改。";
    $("access-confirm-submit").textContent = closing ? "确认关闭" : replacing ? "确认更换" : "确认保存";
    $("access-confirm-feedback").textContent = "";
    pending = {body, opener:document?.activeElement};
    render(); $("access-confirm-dialog").showModal(); $("access-current-token").focus();
    runtime.navigationChanged?.();
  }

  async function save(body) {
    const version = generation;
    saving = true; render();
    try {
      const data = await request("/v1/access/settings", body);
      if(version !== generation) return;
      current = data; clear();
      localLoginEnabled = !!data.login_enabled;
      $("access-login-enabled").checked = !!data.login_enabled;
      selectMode(modeFrom(data));
      $("access-port").value = data.port || "8088";
      $("access-public-url").value = data.public_base_url || "";
      feedback(`${data.login_enabled ? "已保存，请重新登录。" : "已保存，本机访问免登录。"}${data.restart_required ? "访问方式和连接设置重启后生效。" : ""}`);
      $("access-settings-login").hidden = !data.login_enabled;
      $("access-logout").hidden = !data.login_enabled;
      $("user-menu-divider").hidden = !data.login_enabled;
    } catch(error) {
      if(version !== generation) return;
      const message = error.code === "current_token_required" ? "当前令牌不正确，请重新输入。" : error.code === "conflict" ? "设置已更改，请关闭后重新打开核对。" : error.message;
      if (pending) $("access-confirm-feedback").textContent = message;
      else feedback(message);
    } finally {
      body.current_token = "";
      $("access-current-token").value = "";
      // A failed confirmation retains only the candidate, never the proof.
      if (!pending) body.token = "";
      saving = false; render();
      if (pending) $("access-current-token").focus();
    }
  }

  function mount() {
    $("access-login-enabled").addEventListener("change", () => {
      if (selectedMode() === "local") localLoginEnabled = $("access-login-enabled").checked;
      feedback(""); render();
      if ($("access-login-enabled").checked && !current?.login_enabled && !$("access-new-token").value) void generateToken();
    });
    for (const mode of modes) $("access-mode-" + mode).addEventListener("change", () => {
      selectMode(mode);
      $("access-login-enabled").checked = mode !== "local" || localLoginEnabled || !!current?.login_required || !!current?.remote_active;
      feedback(""); render();
      if ($("access-login-enabled").checked && !current?.login_enabled && !$("access-new-token").value) void generateToken();
    });
    for (const id of ["access-new-token", "access-public-url", "access-port"]) {
      const update = () => {if (id === "access-new-token") resetCopied(); feedback(""); render();};
      for (const event of ["input", "change"]) $(id).addEventListener(event, update);
    }
    $("access-token-change").addEventListener("click", () => {editingToken = true; render(); void generateToken();});
    $("access-token-edit-cancel").addEventListener("click", () => {editingToken = false; $("access-new-token").value = ""; showToken(false); resetCopied(); feedback(""); render();});
    $("access-token-generate").addEventListener("click", generateToken);
    $("access-token-toggle").addEventListener("click", () => showToken($("access-new-token").type !== "text"));
    $("access-token-copy").addEventListener("click", async () => {
      const token = $("access-new-token").value, version = generation;
      resetCopied();
      try {
        if (!token) { feedback("请先生成或填写新令牌。"); return; }
        await window.navigator.clipboard.writeText(token);
        if (version !== generation || token !== $("access-new-token").value) return;
        $("access-token-copy").dataset.copied = "true";
        $("access-token-copy").setAttribute("aria-label", "令牌已复制");
        $("access-token-copy").title = "已复制";
        copyTimer = setTimeout(resetCopied, 1500);
        feedback("令牌已复制。");
      } catch {
        if (version !== generation || token !== $("access-new-token").value) return;
        showToken(true); $("access-new-token").focus(); $("access-new-token").select?.(); feedback("请手动复制选中的令牌。");
      }
    });
    $("access-lan-copy").addEventListener("click", async () => {
      const addresses = $("access-lan-addresses").value;
      if (!addresses) return;
      try { await window.navigator.clipboard.writeText(addresses); feedback("访问地址已复制。"); }
      catch { $("access-lan-addresses").focus(); $("access-lan-addresses").select(); feedback("请手动复制选中的访问地址。"); }
    });
    $("access-settings-form").addEventListener("submit", async event => {
      event.preventDefault(); if(busy() || !current || !changed()) return;
      if ($("access-login-enabled").checked && $("access-new-token").value && candidateAssessment().issue) {
        renderPasswordHint(); $("access-new-token").focus(); return;
      }
      const body = {caller:{kind:"human",id:state.operator}, expected_version:current.version, login_enabled:$("access-login-enabled").checked,
        access_mode:selectedMode(), remote_enabled:selectedMode() !== "local", port:selectedMode() === "lan" ? $("access-port").value : "", public_base_url:selectedMode() === "proxy" ? $("access-public-url").value.trim() : "", token:$("access-login-enabled").checked ? $("access-new-token").value : ""};
      if (current.login_enabled) confirmSave(body);
      else await save(body);
    });
    $("access-confirm-form").addEventListener("submit", async event => {
      event.preventDefault(); if(saving || !pending) return;
      const token = $("access-current-token").value;
      if (!token) { $("access-confirm-feedback").textContent = "请输入当前令牌。"; $("access-current-token").focus(); return; }
      pending.body.current_token = token;
      $("access-current-token").value = "";
      await save(pending.body);
    });
    const cancel = () => {if (!saving) {closeConfirmation(); render();}};
    $("access-confirm-cancel").addEventListener("click", cancel);
    $("access-confirm-close").addEventListener("click", cancel);
    $("access-confirm-dialog").addEventListener("cancel", event => {event.preventDefault(); cancel();});
    $("access-confirm-dialog").addEventListener("close", () => {if (!$("access-confirm-dialog").open && pending && !saving) cancel();});
    $("access-settings-login").addEventListener("click", () => {clear();window.location.assign("/access");});
    window.addEventListener("pagehide", clear);
  }
  return {mount, load, clear, get saving(){return busy();}};
}
