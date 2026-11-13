import { randomUUID } from "./random-id.mjs";
import { fillModelParameters, readModelParameters, syncModelProtocol, modelDraftFields, snapshotModelFields, restoreModelFields } from "./model-form.mjs";
const $ = id => document.getElementById(id);
function returnToApp() {
  const hash=location.hash || "";
  location.replace("/" + (hash.startsWith("#platform-connection=") || hash==="#platform-settings" ? hash : ""));
}
let version = 0, requestID, polling = false, configuredKey = false, active = true;
let setupRead = 0, submitting = false;
let operator, draftKey, draftReady=false, loginEnabled=true;
let storage; try { storage=sessionStorage; } catch {}
function saveDraft() {
  if (!draftKey || !draftReady) return;
  try { storage?.setItem(draftKey,JSON.stringify({version,request_id:requestID,fields:snapshotModelFields($)})); } catch {}
}
function restoreDraft() {
  let draft; try { draft=JSON.parse(storage?.getItem(draftKey)); } catch { return; }
  if (!draft || !Number.isSafeInteger(draft.version)) return;
  if (draft.version !== version) { $("reload-model-draft").hidden=false; feedback("配置已在其他页面更改，草稿与原预期版本保留，请核对后再保存。"); }
  version=draft.version; restoreModelFields($,draft.fields);
  if (typeof draft.request_id === "string") requestID=draft.request_id;
}
async function request(path, data) {
  const response = await fetch(path, {method: data ? "POST" : "GET", cache: "no-store", credentials: "same-origin", headers: data ? {"Content-Type":"application/json"} : {}, body: data ? JSON.stringify(data) : undefined});
  const result = await response.json();
  if (!response.ok) {const error = new Error(response.status === 401 ? "访问令牌无效或登录已到期，请重新输入。" : response.status === 429 ? "尝试过于频繁，请稍后重试。" : result.message || "请求未完成，请稍后重试。"); error.status = response.status; throw error; }
  return result;
}
function feedback(message) { $("feedback").textContent = message; }
function keyState() {
  const reuse = configuredKey && !$("replace-key").checked;
  $("api-key").hidden = reuse; $("api-key").required = !reuse;
  if (reuse) $("api-key").value = "";
  $("key-state").textContent = reuse ? "密钥已配置，本次验证将复用。" : "请输入 API Key，密钥将保存在部署机的私有目录。";
}
function protocolState() { syncModelProtocol($); }
function busy(value) {
  for (const el of $("model-form").elements) el.disabled = value;
  $("reload-model-draft").disabled = value;
  $("save-model").textContent = value ? "正在验证…" : "验证并开始使用";
}
function reconcileSetup(state) {
  if (!state.setup_required) { returnToApp(); return true; }
  const operation = state.operation;
  const pending = operation && ["validating", "saved"].includes(operation.status);
  // Only an authenticated read can settle an uncertain submission. The setup
  // endpoint holds one operation and rejects another while it is running.
  requestID = pending ? operation.operation_id || requestID : undefined;
  saveDraft(); busy(!!pending); $("check-setup-operation").hidden = !pending;
  if (operation) feedback(operation.message || "正在验证模型，稍后将自动进入对话。");
  else feedback("尚未保存模型，请核对表单后明确提交。");
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
  } catch {
    if (generation !== setupRead) return;
    $("login").hidden=false; $("logout").hidden=true; $("model").hidden=true; $("api-key").value=""; return;
  }
  $("login").hidden = true; $("logout").hidden = !loginEnabled;
  const state = await request("/v1/setup");
  if (generation !== setupRead) return;
  if (!state.setup_required) { returnToApp(); return; }
  if (state.migration) {$("model").hidden=true;$("retry-migration").hidden=false;$("retry-migration").disabled=state.migration.running;feedback(`${state.message} 已处理 ${state.migration.completed} / ${state.migration.total}。`);return;}
  $("model").hidden = false;
  version = state.version;
  const m = state.model || {};
  fillModelParameters($, m);
  configuredKey = state.credential_configured; $("replace-key-label").hidden = !configuredKey;
  restoreDraft(); draftReady=true; keyState(); protocolState();
  if (state.writable === false) {busy(true); $("save-model").textContent = "需要迁移或部署机修复"; feedback(state.message); return;}
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
        feedback("提交结果尚未确认，正在查询原操作；不会重新提交。也可点击读取进度。");
      }
    }
  } finally { polling = false; }
}
for (const id of modelDraftFields) { $(id).addEventListener("input",saveDraft); $(id).addEventListener("change",saveDraft); }
$("reload-model-draft").addEventListener("click",async()=>{
  if (requestID || $("reload-model-draft").disabled) return;
  try {storage?.removeItem(draftKey);} catch {}
  requestID=undefined;$("reload-model-draft").hidden=true;$("api-key").value="";
  feedback("已放弃旧草稿，请按当前配置重新填写。");await showSession();
});
$("check-setup-operation").addEventListener("click",()=>showSession().catch(()=>feedback("暂时无法读取原操作，恢复连接后将继续查询。")));
$("replace-key").addEventListener("change", keyState);
$("retry-migration").addEventListener("click", async()=>{ $("retry-migration").disabled=true;try{await request("/v1/models/migration/retry",{caller:{kind:"human",id:operator}});feedback("迁移正在继续，完成后可刷新进入对话。");}catch(error){feedback(error.message);}finally{$("retry-migration").disabled=false;} });
$("protocol").addEventListener("change", protocolState);
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
  const data = {request_id:requestID ||= randomUUID(), expected_version:version, ...readModelParameters($)};
  saveDraft();
  if (!configuredKey || $("replace-key").checked) data.api_key = $("api-key").value;
  $("api-key").value = ""; busy(true);
  $("check-setup-operation").hidden = false;
  try {
    const operation = await request("/v1/setup/model", data);
    feedback(operation.message || "正在验证文本和工具往返，最多约 90 秒。关闭页面后任务仍会继续。");
    poll();
  } catch (error) {
    // A lost HTTP response may still have admitted the operation. Reconcile
    // before accepting a fresh submission, without retaining the key.
    try { if (reconcileSetup(await request("/v1/setup"))) {feedback(error.message); return;} }
    catch { feedback("提交结果尚未确认，正在查询原操作；不会重新提交。也可点击读取进度。"); }
    void poll();
  } finally { data.api_key = ""; submitting=false; $("check-setup-operation").disabled=false; }
});
$("logout").addEventListener("click", async () => {$("api-key").value = ""; try {await request("/v1/access/logout", {}); location.replace("/access");} catch(error) {feedback(error.message);} });
addEventListener("pagehide", () => {saveDraft();active = false; $("api-key").value = ""; $("token").value = ""; hideToken();});
function resume() { if (!document.hidden && navigator.onLine !== false) {active=true;void showSession().catch(error=>feedback(error.message));} }
addEventListener("pageshow",resume); addEventListener("online",resume);
document.addEventListener("visibilitychange",() => { if(document.hidden) saveDraft(); else resume(); });
showSession().catch(error => feedback(error.message));
