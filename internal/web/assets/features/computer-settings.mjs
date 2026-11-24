import { randomUUID } from "../random-id.mjs";

// Connection settings read local state. Only explicit validate/save actions
// contact the Computer; credentials are never written to browser storage.
export function createComputerSettings({env, runtime}) {
  const {request, setTimeout, clearTimeout} = env;
  const {$, state} = runtime;
  let current, saving = false, generation = 0, timer = null, operation = null, unconfirmed = false;
  const feedback = message => { $("computer-feedback").textContent = message; };
  const active = () => $("settings-dialog").open && !$("computer-settings").hidden;
  const epoch = () => state.managed ? state.catalog?.service_epoch : state.info?.service_epoch;
  const pending = op => op && !["completed", "failed"].includes(op.status);
  const docker = () => $("computer-runtime-mode").value === "docker";
  const disabling = () => current?.can_disable && !$("computer-enabled").checked;
  const storageKey = () => `teamx.computer.operation.${current?.installation_id}`;
  const remember = id => { try { runtime.storage?.setItem(storageKey(), id); } catch { /* Optional storage. */ } };
  const forget = () => { try { runtime.storage?.removeItem(storageKey()); } catch { /* Optional storage. */ } };
  function clear() {
    generation++; clearTimeout(timer); timer = null;
    $("computer-credential").value = "";
  }
  function render() {
    const busy = saving || pending(operation) || unconfirmed;
    for (const id of ["computer-enabled", "computer-provider", "computer-runtime-mode", "computer-endpoint", "computer-credential", "computer-max-screens", "computer-test", "computer-save", "computer-docker-cpus", "computer-docker-memory"]) $(id).disabled = busy || !current;
    if (current?.can_disable && !saving && !unconfirmed) { $("computer-enabled").disabled=false; $("computer-save").disabled=!disabling(); }
    $("computer-manual-fields").hidden=docker();
    $("computer-docker-hint").hidden=!docker();
    $("computer-docker-limits").hidden=!docker();
    for(const id of ["computer-docker-cpus","computer-docker-memory"]) $(id).disabled ||= !docker() || !!current?.docker_created;
    for(const id of ["computer-endpoint","computer-credential"]) $(id).disabled ||= docker();
    $("computer-test").textContent=docker() ? "检查环境" : "测试连接";
    $("computer-refresh").disabled = saving;
    $("computer-credential").placeholder = current?.runtime_mode === "docker" && !docker() ? "输入手动连接的 Runtime 管理密钥" : current?.has_credential ? "已保存，留空保留原密钥" : "输入 Runtime 管理密钥";
    $("computer-status").textContent = current ? (current.message || {disabled:"已停用",ready:"连接已就绪",recovering:"正在恢复连接",applying:"正在应用设置"}[current.status] || "连接状态待确认") : "正在读取配置…";
    $("computer-counts").textContent = current ? `已启用成员 ${current.enabled_members || 0} · 最近核验运行屏幕 ${current.running_screens || 0}` : "";
    $("computer-impact").textContent = docker() ? "首次开启可能需要下载镜像。全局关闭时先等待操作结清，再停止容器；再次开启复用登录状态。" : current?.enabled ? "更换电脑或停用时，会先等待现有操作结清并停止屏幕；登录状态和文件会保留。" : "电脑缺省关闭。保存停用的连接信息无需启动电脑服务。";
    $("computer-progress").textContent = operation ? `${operation.status === "completed" ? "已完成" : operation.status === "failed" ? "未应用" : "应用中"}：${operation.message || ({runtime_check:"检查本机 Docker",runtime_image:"准备固定镜像，首次可能需要下载",runtime_volumes:"准备电脑数据",runtime_create:"创建电脑容器",runtime_connect:"准备电脑连接",runtime_start:"启动电脑容器",runtime_prepare:"准备镜像与电脑容器",runtime_ready:"等待电脑服务就绪",runtime_stop:"停止托管电脑容器",runtime_abort:"停止未通过核验的候选电脑",validate:"核验连接",settle:"等待结清",stop:"停止原电脑屏幕",members:"更新成员会话",commit:"保存配置"}[operation.stage] || "处理中")}` : "";
    $("computer-save").textContent = saving ? "保存中…" : disabling() ? "关闭电脑" : "保存并应用";
  }
  function fill() {
    $("computer-enabled").checked = !!current.enabled;
    $("computer-provider").value = current.provider;
    $("computer-endpoint").value = current.endpoint;
    $("computer-max-screens").value = String(current.max_screens);
    $("computer-runtime-mode").value=current.suggested_runtime_mode || current.runtime_mode || "manual";
    $("computer-docker-cpus").value=String(current.docker_cpus || 4);
    $("computer-docker-memory").value=String(current.docker_memory_gib || 6);
    if(current.can_disable) $("computer-enabled").checked=true;
  }
  async function load({fillForm = true} = {}) {
    const version = ++generation, serviceEpoch = epoch();
    clearTimeout(timer); timer = null;
    try {
      const data = await request("/v1/computer");
      if (version !== generation || serviceEpoch !== epoch() || data.service_epoch !== serviceEpoch) return;
      const priorVersion = current?.version;
      current = data;
      operation = data.operations.find(pending) || data.operations.at(-1) || null;
      unconfirmed = false;
      let savedID;
      try { savedID = runtime.storage?.getItem(storageKey()); } catch { /* Optional storage. */ }
      if (savedID && !data.operations.some(op => op.id === savedID)) {
        try { const receipt = await request(`/v1/computer/operations/${encodeURIComponent(savedID)}`); if (version !== generation || serviceEpoch !== epoch()) return; operation = receipt; }
        catch (error) {
          if (version !== generation || serviceEpoch !== epoch()) return;
          if (error.code === "operation_not_found") { forget(); feedback("原操作未受理或记录已过期，请核对当前配置后明确保存。"); }
          else { unconfirmed = true; feedback("原保存操作暂未确认，请刷新进度后核对配置。"); }
        }
      }
      if (operation && !pending(operation)) forget();
      if (fillForm || priorVersion && priorVersion !== data.version) fill();
      render();
      if (active() && (pending(operation) || current.status === "recovering")) timer = setTimeout(() => load({fillForm:false}), 1500);
    } catch (error) { if (version === generation) feedback(error.message); }
  }
  function input() {
    return {
      service_epoch:current.service_epoch,
      ...(current.room_id ? {room_id:current.room_id, room_instance:current.room_instance} : {}),
      installation_id:current.installation_id,
      caller:{kind:"human", id:state.operator},
      expected_version:current.version,
      enabled:$("computer-enabled").checked,
      provider:$("computer-provider").value,
      runtime_mode:$("computer-runtime-mode").value || "manual",
      docker_cpus:Number($("computer-docker-cpus").value || 4),
      docker_memory_gib:Number($("computer-docker-memory").value || 6),
      endpoint:docker() ? "" : $("computer-endpoint").value.trim(),
      max_screens:Number($("computer-max-screens").value),
      credential_action:!docker() && $("computer-credential").value ? "replace" : "keep",
      credential:docker() ? "" : $("computer-credential").value,
    };
  }
  async function submit(validate = false) {
    if (saving || !current || pending(operation) && !disabling() || unconfirmed) return;
    const body = input(), version = generation, serviceEpoch = epoch();
    if (!validate) { body.request_id = randomUUID(env.crypto).replaceAll("-", ""); remember(body.request_id); }
    saving = true; feedback(""); render();
    try {
      const result = await request(validate ? docker() ? "/v1/computer/check" : "/v1/computer/validate" : "/v1/computer/save", body);
      if (version !== generation || serviceEpoch !== epoch()) return;
      if (validate) feedback(docker() ? `Docker 环境可用${result.emulated ? "，amd64 镜像将使用架构模拟，启动后继续核验" : ""}。` : `连接核验通过，当前运行 ${result.running_screens} 个屏幕。`);
      else { operation = result; feedback("设置已受理，可刷新查询应用进度。"); }
    } catch (error) {
      if (version !== generation || serviceEpoch !== epoch()) return;
      if (!validate) {
        unconfirmed = !["conflict", "busy", "request", "identity"].includes(error.code);
        if (!unconfirmed) forget();
      }
      feedback(error.code === "conflict" ? "配置已在其他页面修改，请刷新后核对再保存。" : validate ? error.message : "保存结果待确认，请刷新进度核对原操作。" );
    } finally {
      body.credential = ""; if (!validate) $("computer-credential").value = ""; saving = false; render();
      if (!validate && version === generation && serviceEpoch === epoch()) await load({fillForm:false});
    }
  }
  function mount() {
    $("computer-form").addEventListener("submit", event => { event.preventDefault(); return submit(); });
    $("computer-test").addEventListener("click", () => submit(true));
    $("computer-refresh").addEventListener("click", () => load());
    $("computer-runtime-mode").addEventListener("change",()=>{ $("computer-credential").value="";if(!docker() && current?.runtime_mode==="docker" && $("computer-endpoint").value===current.endpoint) $("computer-endpoint").value="";render(); });
    $("computer-enabled").addEventListener("change",render);
  }
  return {mount,load,clear,get saving(){return saving;}};
}
