// Service-wide access controls. Credentials live only in forms, never storage.
export function createAccessSettings({env, runtime}) {
  const {window, document, request, setTimeout, clearTimeout} = env;
  const {$, state} = runtime;
  let current, saving = false, generation = 0, editingToken = false, pending = null;
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

  function changed() {
    return !!current && ($("access-login-enabled").checked !== current.login_enabled ||
      $("access-remote-enabled").checked !== current.remote_enabled ||
      ($("access-remote-enabled").checked && $("access-public-url").value.trim() !== current.public_base_url) ||
      ($("access-login-enabled").checked && !!$("access-new-token").value));
  }

  function render() {
    const enabled = $("access-login-enabled").checked;
    $("access-settings-form").hidden = !current?.writable;
    $("access-login-enabled").disabled = busy() || $("access-remote-enabled").checked || current?.remote_active;
    $("access-remote-enabled").disabled = busy();
    $("access-login-hint").textContent = current?.remote_active ? "关闭远程访问并重启后，才能关闭登录验证。" : "打开 TeamX 时需要输入令牌。";
    $("access-remote-fields").hidden = !$("access-remote-enabled").checked;
    $("access-token-summary").hidden = !enabled || !current?.login_enabled || editingToken;
    $("access-token-fields").hidden = !enabled || (!!current?.login_enabled && !editingToken);
    $("access-token-edit-cancel").hidden = !current?.login_enabled;
    $("access-draft-hint").hidden = !changed();
    for (const id of ["access-public-url", "access-new-token", "access-token-change", "access-token-edit-cancel", "access-token-generate", "access-token-toggle", "access-token-copy"]) $(id).disabled = busy();
    $("access-new-token").disabled = busy() || $("access-token-fields").hidden;
    $("access-new-token").required = !$("access-token-fields").hidden;
    $("access-public-url").disabled = busy() || !$("access-remote-enabled").checked;
    $("access-settings-save").disabled = busy() || !current?.writable || !changed();
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
      $("access-login-enabled").checked = !!data.login_enabled;
      $("access-remote-enabled").checked = !!data.remote_enabled;
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
      $("access-login-enabled").checked = !!data.login_enabled;
      $("access-remote-enabled").checked = !!data.remote_enabled;
      $("access-public-url").value = data.public_base_url || "";
      feedback(`${data.login_enabled ? "已保存，请重新登录。" : "已保存，本机访问免登录。"}${data.restart_required ? "远程设置重启后生效。" : ""}`);
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
      feedback(""); render();
      if ($("access-login-enabled").checked && !current?.login_enabled && !$("access-new-token").value) void generateToken();
    });
    $("access-remote-enabled").addEventListener("change", () => {
      if ($("access-remote-enabled").checked) $("access-login-enabled").checked = true;
      feedback(""); render();
      if ($("access-login-enabled").checked && !current?.login_enabled && !$("access-new-token").value) void generateToken();
    });
    for (const id of ["access-new-token", "access-public-url"]) $(id).addEventListener("input", () => {if (id === "access-new-token") resetCopied(); feedback(""); render();});
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
    $("access-settings-form").addEventListener("submit", async event => {
      event.preventDefault(); if(busy() || !current || !changed()) return;
      const body = {caller:{kind:"human",id:state.operator}, expected_version:current.version, login_enabled:$("access-login-enabled").checked,
        remote_enabled:$("access-remote-enabled").checked, public_base_url:$("access-public-url").value.trim(), token:$("access-login-enabled").checked ? $("access-new-token").value : ""};
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
