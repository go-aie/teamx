import { randomUUID } from "./random-id.mjs";
import { fillModelParameters, readModelParameters, syncModelProtocol, modelDraftFields, snapshotModelFields, restoreModelFields, setModelKeyVisible } from "./model-form.mjs";
const $ = id => document.getElementById(id);
function returnToApp() {
  const hash=location.hash || "";
  location.replace("/" + (hash.startsWith("#platform-connection=") || hash==="#platform-settings" ? hash : ""));
}
let version = 0, requestID, polling = false, configuredKey = false, active = true;
let setupRead = 0, submitting = false;
let operator, draftKey, draftReady=false, loginEnabled=true;
let lastSetupError;
const setupFields = {protocol:"protocol",base_url:"base-url",model_id:"model-id",api_key:"api-key",input_modalities:"input-modalities",context_window:"context-window",max_tokens:"max-tokens",reasoning_effort:"reasoning-effort","compat.supports_developer_role":"developer-role"};
const advancedFields = new Set(["input-modalities", "context-window", "max-tokens", "reasoning-effort", "developer-role"]);
let storage; try { storage=sessionStorage; } catch {}
function saveDraft() {
  if (!draftKey || !draftReady) return;
  try { storage?.setItem(draftKey,JSON.stringify({version,request_id:requestID,fields:snapshotModelFields($)})); } catch {}
}
function restoreDraft(versionKnown = true) {
  let draft; try { draft=JSON.parse(storage?.getItem(draftKey)); } catch { return; }
  if (!draft || !Number.isSafeInteger(draft.version)) return;
  if (versionKnown && draft.version !== version) { $("reload-model-draft").hidden=false; feedback("配置已在其他页面更改，草稿与原预期版本保留，请核对后再保存。"); }
  version=draft.version; restoreModelFields($,draft.fields);
  if (typeof draft.request_id === "string") requestID=draft.request_id;
}
async function request(path, data) {
  const response = await fetch(path, {method: data ? "POST" : "GET", cache: "no-store", credentials: "same-origin", headers: data ? {"Content-Type":"application/json"} : {}, body: data ? JSON.stringify(data) : undefined});
  const result = await response.json();
  if (!response.ok) {const error = new Error(response.status === 401 ? "访问令牌无效或登录已到期，请重新输入。" : response.status === 429 ? "尝试过于频繁，请稍后重试。" : result.message || "请求未完成，请稍后重试。"); error.status = response.status; error.field = result.field; error.code = result.code; throw error; }
  return result;
}
function feedback(message, kind = "status") {
  $("feedback").setAttribute("data-state", kind);
  $("feedback").setAttribute("role", kind === "error" ? "alert" : "status");
  $("feedback").textContent = message;
}
function clearSetupErrors() {
  lastSetupError = undefined;
  for (const id of Object.values(setupFields)) {
    $(id).removeAttribute("aria-invalid");
    $(`${id}-error`).hidden = true; $(`${id}-error`).textContent = "";
  }
}
function showSetupError(error) {
  showSetupForm();
  const signature = JSON.stringify([error.field, error.message]);
  const reveal = signature !== lastSetupError;
  clearSetupErrors(); lastSetupError = signature;
  feedback(error.message || "模型验证失败，请检查设置后重试。", "error");
  const id = setupFields[error.field];
  if (id) {
    if (advancedFields.has(id)) $("model-advanced").open = true;
    if (id === "api-key" && $("key-input").hidden) { $("replace-key").checked = true; keyState(); }
    $(id).setAttribute("aria-invalid", "true");
    $(`${id}-error`).textContent = error.message; $(`${id}-error`).hidden = false;
  }
  if (reveal) {
    const target = id ? $(id) : $("feedback");
    target.focus({preventScroll:true}); target.scrollIntoView({block:"center"});
  }
}
function keyState() {
  const reuse = configuredKey && !$("replace-key").checked;
  $("api-key").hidden = reuse; $("api-key").required = !reuse;
  $("key-input").hidden = reuse; setModelKeyVisible($, false);
  if (reuse) $("api-key").value = "";
  $("key-state").textContent = reuse ? "密钥已配置，本次验证将复用。" : "密钥保存在运行 TeamX 的设备上。";
}
function protocolState() { syncModelProtocol($); }
function busy(value) {
  for (const el of $("model-form").elements) el.disabled = value;
  $("model-form").setAttribute("aria-busy", String(value));
  $("reload-model-draft").disabled = value;
  $("save-model").textContent = value ? "正在提交…" : "验证并开始使用";
}
function showSetupForm() {
  $("setup-editor").hidden = false; $("setup-progress").hidden = true;
  $("check-setup-operation").hidden = true;
}
function showSetupProgress(phase) {
  if (navigator.onLine === false) phase = "unknown";
  const entering = $("setup-progress").hidden;
  busy(true); clearSetupErrors(); feedback("");
  $("setup-editor").hidden = true; $("setup-progress").hidden = false;
  $("setup-progress").setAttribute("data-state", phase);
  $("setup-summary-model").textContent = $("model-id").value || "所选模型";
  let address = "服务商默认地址";
  if ($("base-url").value) {
    try { const url = new URL($("base-url").value); address = /^https?:$/.test(url.protocol) ? url.origin + url.pathname : "接口地址待确认"; }
    catch { address = "接口地址待确认"; }
  }
  $("setup-summary-address").textContent = address;
  const [title, hint] = phase === "preparing"
    ? ["正在准备助理…", "模型已保存，即将进入对话。"]
    : phase === "unknown"
      ? ["暂时无法确认验证结果", "正在自动检查，连接恢复后会继续更新。"]
      : ["正在验证模型…", "可能需要约 90 秒，完成后将自动进入对话。"];
  if ($("setup-progress-title").textContent !== title) $("setup-progress-title").textContent = title;
  if ($("setup-progress-hint").textContent !== hint) $("setup-progress-hint").textContent = hint;
  $("check-setup-operation").hidden = phase !== "unknown";
  if (entering) { $("setup-progress").focus({preventScroll:true}); $("model").scrollIntoView({block:"start"}); }
}
function reconcileSetup(state) {
  if (!state.setup_required) { returnToApp(); return true; }
  const operation = state.operation;
  const pending = operation && ["validating", "saved"].includes(operation.status);
  const wasUnconfirmed = !!requestID;
  // Only an authenticated read can settle an uncertain submission. The setup
  // endpoint holds one operation and rejects another while it is running.
  requestID = pending ? operation.operation_id || requestID : undefined;
  saveDraft();
  if (pending) { showSetupProgress(operation.status === "saved" ? "preparing" : "validating"); return false; }
  busy(false); showSetupForm();
  if (operation?.status === "failed") showSetupError(operation);
  else if (operation) { clearSetupErrors(); feedback(operation.message || "正在验证模型，稍后将自动进入对话。"); }
  else if (!lastSetupError) feedback(wasUnconfirmed ? "上次提交未被受理，请重新填写密钥后重试。" : "");
  return !pending;
}
async function showSession() {
  if (submitting) return;
  const generation = ++setupRead;
  try {
    const session=await request("/v1/access/session");
    if (generation !== setupRead) return;
    operator=session.operator;
    loginEnabled=session.login_enabled !== false;
    const nextKey=JSON.stringify(["teamx.setup-draft",session.installation_id || location.origin,operator]);
    if (draftKey !== nextKey) { requestID=undefined; draftReady=false; }
    draftKey=nextKey;
  } catch (error) {
    if (generation !== setupRead) return;
    if (error.status !== 401 && requestID) { showSetupProgress("unknown"); void poll(); return; }
    document.title = "TeamX · 登录";
    $("login").hidden=false; $("logout").hidden=true; $("model").hidden=true; $("api-key").value=""; return;
  }
  $("login").hidden = true; $("logout").hidden = !loginEnabled;
  let state;
  try { state = await request("/v1/setup"); }
  catch (error) {
    if (generation !== setupRead) return;
    restoreDraft(false); keyState();
    if (requestID) {
      $("model").hidden = false; document.title = "TeamX · 设置模型"; draftReady = true;
      showSetupProgress("unknown"); void poll(); return;
    }
    throw error;
  }
  if (generation !== setupRead) return;
  if (!state.setup_required) { returnToApp(); return; }
  if (state.migration) {$("model").hidden=true;$("retry-migration").hidden=false;$("retry-migration").disabled=state.migration.running;feedback(`${state.message} 已处理 ${state.migration.completed} / ${state.migration.total}。`);return;}
  $("model").hidden = false;
  document.title = "TeamX · 设置模型";
  version = state.version;
  const m = state.model || {};
  fillModelParameters($, m);
  configuredKey = state.credential_configured; $("replace-key-label").hidden = !configuredKey;
  restoreDraft(); draftReady=true; keyState(); protocolState();
  if (state.writable === false) {showSetupForm();busy(true); $("save-model").textContent = "需要迁移或部署机修复"; feedback(state.message); return;}
  const draftFeedback = $("feedback").textContent;
  if (!reconcileSetup(state)) void poll();
  else if (!state.operation && !$("reload-model-draft").hidden) feedback(draftFeedback);
}
async function poll() {
  if (polling) return;
  polling = true;
  try {
    while (active) {
      await new Promise(resolve => setTimeout(resolve, 700));
      if (!active) break;
      if (document.hidden || navigator.onLine === false) continue;
      const generation = setupRead;
      try {
        const state = await request("/v1/setup");
        if (generation !== setupRead) continue;
        if (reconcileSetup(state)) return;
      } catch (error) {
        if (generation !== setupRead) continue;
        if (error.status === 401) {location.replace("/access"); return;}
        showSetupProgress("unknown");
      }
    }
  } finally { polling = false; }
}
for (const id of modelDraftFields) { $(id).addEventListener("input",saveDraft); $(id).addEventListener("change",saveDraft); }
for (const id of [...modelDraftFields, "api-key", "replace-key"]) for (const event of ["input", "change"]) $(id).addEventListener(event, () => { if (lastSetupError) {clearSetupErrors(); feedback("");} });
$("reload-model-draft").addEventListener("click",async()=>{
  if (requestID || $("reload-model-draft").disabled) return;
  try {storage?.removeItem(draftKey);} catch {}
  requestID=undefined;$("reload-model-draft").hidden=true;$("api-key").value="";
  feedback("已放弃旧草稿，请按当前配置重新填写。");await showSession();
});
$("check-setup-operation").addEventListener("click",async()=>{
  if (submitting || $("check-setup-operation").disabled) return;
  $("check-setup-operation").disabled = true;
  try { await showSession(); } catch { showSetupProgress("unknown"); }
  finally { $("check-setup-operation").disabled = false; }
});
$("replace-key").addEventListener("change", keyState);
$("toggle-key").addEventListener("click", () => setModelKeyVisible($, $("api-key").type !== "text"));
$("retry-migration").addEventListener("click", async()=>{ $("retry-migration").disabled=true;try{await request("/v1/models/migration/retry",{caller:{kind:"human",id:operator}});feedback("迁移正在继续，完成后可刷新进入对话。");}catch(error){feedback(error.message);}finally{$("retry-migration").disabled=false;} });
$("protocol").addEventListener("change", protocolState);
$("model-id").addEventListener("input", protocolState);
function hideToken() {
  $("token").type = "password";
  $("toggle-token").setAttribute("aria-pressed", "false");
  $("toggle-token").setAttribute("aria-label", "显示访问令牌");
  $("toggle-token").title = "显示访问令牌";
}
$("toggle-token").addEventListener("click", () => {
  if ($("token").type === "text") hideToken();
  else {
    $("token").type = "text";
    $("toggle-token").setAttribute("aria-pressed", "true");
    $("toggle-token").setAttribute("aria-label", "隐藏访问令牌");
    $("toggle-token").title = "隐藏访问令牌";
  }
});
$("token").addEventListener("input", () => {
  $("token").removeAttribute("aria-invalid"); $("login-feedback").hidden = true;
});
$("login-form").addEventListener("submit", async event => {
  event.preventDefault(); const button = $("login-submit");
  if (button.disabled) return;
  button.disabled = true; button.textContent = "正在登录…";
  $("login-form").setAttribute("aria-busy", "true");
  $("login-feedback").hidden = true; $("token").removeAttribute("aria-invalid");
  try {
    const token = $("token").value.trim(); $("token").value = ""; hideToken();
    await request("/v1/access/login", {token}); feedback(""); await showSession();
  } catch (error) {
    if ($("login").hidden) feedback(error.message);
    else {
      $("login-feedback").textContent = error.status ? error.message : "暂时无法连接，请稍后重试。";
      $("login-feedback").hidden = false;
      if (error.status === 401) $("token").setAttribute("aria-invalid", "true");
      $("token").focus();
    }
  } finally {
    button.disabled = false; button.textContent = "登录";
    $("login-form").setAttribute("aria-busy", "false");
  }
});
$("model-form").addEventListener("submit", async event => {
  event.preventDefault();
  if (requestID || $("save-model").disabled) return;
  ++setupRead; submitting=true; $("check-setup-operation").disabled=true;
  clearSetupErrors();
  const data = {request_id:requestID ||= randomUUID(), expected_version:version, ...readModelParameters($)};
  saveDraft();
  if (!configuredKey || $("replace-key").checked) data.api_key = $("api-key").value;
  $("api-key").value = ""; setModelKeyVisible($, false); busy(true);
  feedback(""); $("check-setup-operation").hidden = true;
  try {
    const operation = await request("/v1/setup/model", data);
    if (!reconcileSetup({setup_required:true, operation})) void poll();
  } catch (error) {
    // Authored 400 diagnostics reject the input before an operation is admitted.
    // Show them immediately even if a later status read would be offline.
    if (error.status === 400 && error.code === "request") {
      requestID = undefined; saveDraft(); busy(false); $("check-setup-operation").hidden = true;
      showSetupError(error); return;
    }
    // A lost HTTP response may still have admitted the operation. Reconcile
    // before accepting a fresh submission, without retaining the key.
    try { const state = await request("/v1/setup"); if (reconcileSetup(state)) {if (state.setup_required) showSetupError(error); return;} }
    catch { showSetupProgress("unknown"); }
    void poll();
  } finally { data.api_key = ""; submitting=false; $("check-setup-operation").disabled=false; }
});
$("logout").addEventListener("click", async () => {$("api-key").value = ""; try {await request("/v1/access/logout", {}); location.replace("/access");} catch(error) {feedback(error.message);} });
addEventListener("pagehide", () => {saveDraft();active = false; $("api-key").value = ""; setModelKeyVisible($, false); $("token").value = ""; hideToken();});
function resume() { if (!document.hidden && navigator.onLine !== false) {active=true;void showSession().catch(error=>feedback(error.message));} }
addEventListener("pageshow",resume); addEventListener("online",resume);
addEventListener("offline",()=>{ if (requestID) showSetupProgress("unknown"); });
document.addEventListener("visibilitychange",() => { if(document.hidden) saveDraft(); else resume(); });
showSession().catch(error => feedback(error.message));
