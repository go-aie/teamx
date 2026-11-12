import { fillModelParameters, readModelParameters, syncModelProtocol } from "./model-form.mjs";
const $ = id => document.getElementById(id);
let version = 0, requestID, polling = false, configuredKey = false, active = true;
let operator;
async function request(path, data) {
  const response = await fetch(path, {method: data ? "POST" : "GET", cache: "no-store", credentials: "same-origin", headers: data ? {"Content-Type":"application/json"} : {}, body: data ? JSON.stringify(data) : undefined});
  const result = await response.json();
  if (!response.ok) {const error = new Error(response.status === 401 ? "管理 token 无效或登录已到期，请重新输入。" : response.status === 429 ? "尝试过于频繁，请稍后重试。" : result.message || "请求未完成，请检查配置后重试。"); error.status = response.status; throw error; }
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
  $("save-model").textContent = value ? "正在验证…" : "验证并开始使用";
}
async function showSession() {
  try { operator = (await request("/v1/access/session")).operator; } catch { return; }
  $("login").hidden = true; $("logout").hidden = false;
  const state = await request("/v1/setup");
  if (!state.setup_required) { location.replace("/"); return; }
  if (state.migration) {$("model").hidden=true;$("retry-migration").hidden=false;$("retry-migration").disabled=state.migration.running;feedback(`${state.message} 已处理 ${state.migration.completed} / ${state.migration.total}。`);return;}
  $("model").hidden = false;
  version = state.version;
  const m = state.model || {};
  fillModelParameters($, m);
  configuredKey = state.credential_configured; $("replace-key-label").hidden = !configuredKey;
  keyState(); protocolState();
  if (state.writable === false) {busy(true); $("save-model").textContent = "需要迁移或部署机修复"; feedback(state.message); return;}
  if (state.operation) {
    feedback(state.operation.message || "正在验证模型，稍后将自动进入对话。验证最多约 90 秒。");
    if (["validating", "saved"].includes(state.operation.status)) {busy(true); poll();}
  }
}
async function poll() {
  if (polling) return;
  polling = true;
  while (active) {
    await new Promise(resolve => setTimeout(resolve, 700));
    try {
      const state = await request("/v1/setup");
      if (!state.setup_required) { location.replace("/"); return; }
      if (state.operation) {
        feedback(state.operation.message || "正在验证模型…");
        if (state.operation.status === "failed") {requestID = undefined; busy(false); polling = false; return;}
      }
    } catch (error) {
      if (error.status === 401) {location.replace("/access"); return;}
      feedback("正在重新连接；服务会保留验证任务。刷新页面可查看结果。");
    }
  }
}
$("replace-key").addEventListener("change", keyState);
$("retry-migration").addEventListener("click", async()=>{ $("retry-migration").disabled=true;try{await request("/v1/models/migration/retry",{caller:{kind:"human",id:operator}});feedback("迁移正在继续，完成后可刷新进入对话。");}catch(error){feedback(error.message);}finally{$("retry-migration").disabled=false;} });
$("protocol").addEventListener("change", protocolState);
$("login-form").addEventListener("submit", async event => {
  event.preventDefault(); const button = event.submitter; button.disabled = true;
  try { const token = $("token").value; $("token").value = ""; await request("/v1/access/login", {token}); feedback(""); await showSession(); }
  catch (error) { feedback(error.message); } finally { button.disabled = false; }
});
$("model-form").addEventListener("submit", async event => {
  event.preventDefault();
  const data = {request_id:requestID ||= crypto.randomUUID(), expected_version:version, ...readModelParameters($)};
  if (!configuredKey || $("replace-key").checked) data.api_key = $("api-key").value;
  $("api-key").value = ""; busy(true);
  try {
    const operation = await request("/v1/setup/model", data);
    feedback(operation.message || "正在验证文本和工具往返，最多约 90 秒。关闭页面后任务仍会继续。");
    poll();
  } catch (error) {
    requestID = undefined;
    // A lost HTTP response may still have admitted the operation. Reconcile
    // before accepting a fresh submission, without retaining the key.
    try { const state = await request("/v1/setup"); if (state.operation?.status === "validating") {poll(); return;} } catch {}
    feedback(error.message); busy(false);
  } finally { data.api_key = ""; }
});
$("logout").addEventListener("click", async () => {$("api-key").value = ""; try {await request("/v1/access/logout", {}); location.replace("/access");} catch(error) {feedback(error.message);} });
addEventListener("pagehide", () => {active = false; $("api-key").value = ""; $("token").value = "";});
showSession().catch(error => feedback(error.message));
