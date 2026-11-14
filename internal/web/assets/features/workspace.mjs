import { resolveAgentName } from "../agent-names.mjs";
import { randomUUID } from "../random-id.mjs";
import { captureReturnView } from "../view-return.mjs";
// Workspace configuration, platform choices and durable operation recovery.
export function createWorkspace({ env, runtime, notificationsUI, membersUI, roomsUI, conversationUI, settingsUI }) {
  const { document, window, Option, request, requestAnimationFrame } = env;
  const { state, $, notice, refresh, storage } = runtime;

  // Scoped to a Room instance and operator. Durable operation IDs can survive
  // service epochs; notification drafts are invalidated when the epoch changes.
  let workspaceView = null;
  let workspaceReturn = null;

  const workspaceKey = (room, operator) => JSON.stringify(["teamx.workspace", room.room_instance, operator]);

  const workspaceStages = { waiting: "正在结清旧执行", preparing: "正在初始化目录与绑定", committing: "正在保存配置", sessions: "正在更新成员 Session", completed: "设置已生效" };

  const workspaceErrors = { interrupted: "服务曾中断，请核查后重试。", cleanup_failed: "旧执行未能结清，请先检查服务状态。", preparation_failed: "初始化失败，原配置和目录现场已保留，请检查仓库、认证或目标目录。", commit_unknown: "配置提交结果待核查，请重新打开服务后查看状态。", session_failed: "成员 Session 更新未完成，请核查后重试。", credential_missing: "凭据缺失，请检查服务端凭据或指定的环境变量。" };

  function workspaceBusy() {
    return !!workspaceView && (workspaceView.sending || !!workspaceView.pending ||
      !!workspaceView.data?.operation && workspaceView.data.operation.status !== "completed");
  }

  function workspaceCanRevise() {
    return workspaceView?.data?.operation?.status === "failed" && workspaceView.data.can_revise === true;
  }

  function workspaceFormBusy() {
    return workspaceView?.sending || !!workspaceView?.pending || workspaceBusy() && !workspaceCanRevise();
  }

  function saveWorkspacePending(ws) {
    try {
      if (ws.pending) storage?.setItem(ws.key, JSON.stringify(ws.pending));
      else storage?.removeItem(ws.key);
    } catch { /* The server still retains accepted operation IDs. */ }
  }

  function selectWorkspace(room, operator) {
    const key = room && workspaceKey(room, operator);
    const notificationEpoch=JSON.stringify([state.catalog?.service_epoch,room?.service?.service_epoch]);
    if (key === workspaceView?.key) {
      if(!workspaceView)return;
      if(workspaceView.notificationEpoch!==notificationEpoch){
        notificationsUI.invalidateNavigation();notificationsUI.stopLarkChatSearch();$("notification-detail").close();
        // Workspace operation recovery keeps its durable key; notification forms
        // and requests must start afresh across manager or Room service epochs.
        for(const field of Object.keys(workspaceView))if(/^(notification|gitlab|lark|events|todos|messages|memberSource|memberPlatform|memberTool)/.test(field))delete workspaceView[field];
        delete workspaceView.allSubscriptions;delete workspaceView.retiredSources;
        delete workspaceView.allPlatforms;membersUI.resetTools();
        workspaceView.notificationEpoch=notificationEpoch;
        if($("activity-dialog").open && membersUI.selectedAgent())void membersUI.openMemberTools(membersUI.selectedAgent());
      }
      return;
    }

    closeWorkspacePicker();
    membersUI.resetTools();
    $("notification-detail").close();
    $("workspace-dialog").close();
    workspaceView = room ? { room: { room_id: room.room_id, room_instance: room.room_instance }, operator, key,
      notificationEpoch, data: null, sending: false, pending: null, closeOnComplete: null, error: "", formVersion: null, missing: false, connected: false } : null;
    if (!workspaceView) return;
    try {
      const pending = JSON.parse(storage?.getItem(key));
      if (/^[a-f0-9]{32}$/.test(pending?.operation_id)) {
        workspaceView.pending = { operation_id: pending.operation_id };
        workspaceView.closeOnComplete = pending.operation_id;
      }
    } catch { /* Never replay requests from storage. */ }
  }

  async function syncWorkspace(catalog) {
    const room = roomsUI.selectedEntry(catalog);
    selectWorkspace(room, catalog.operator);
    const ws = workspaceView;
    if (!ws) { renderWorkspace(); return; }
    let completed = false;
    const query = (operationID = "") => {
      const params = new URLSearchParams({ service_epoch: catalog.service_epoch, caller_id: ws.operator, ...ws.room });
      if (operationID) params.set("operation_id", operationID);
      return request(`/v1/workspace?${params}`);
    };
    try {
      let data = await query();
      if (ws !== workspaceView) return;
      if (ws.pending && data.operation?.operation_id !== ws.pending.operation_id) {
        try { data = await query(ws.pending.operation_id); }
        catch (error) {
          if (error.code !== "operation_not_found") throw error;
          ws.missing = true;
          ws.error = "未查到原操作收据，无法确认凭据是否曾保存。不会自动重发；可重新选择连接并显式申请新操作。";
        }
      }
      if (ws !== workspaceView) return;
      const operation = data.operation, previous = ws.data?.operation;
      completed = operation?.status === "completed" && (ws.closeOnComplete === operation.operation_id ||
        previous?.operation_id === operation.operation_id && previous.status === "running");
      ws.data = data; ws.connected = true;
      if (ws.queryFailed && !ws.missing) { ws.error = ""; ws.queryFailed = false; }
      if (ws.pending && ws.pending.operation_id === data.operation?.operation_id) {
        ws.pending = null; ws.missing = false; ws.error = ""; saveWorkspacePending(ws);
      }
    } catch {
      if (ws !== workspaceView) return;
      ws.queryFailed = true; ws.connected = false; ws.error = "暂时无法查询工作目录，正在重连；不会自动重发操作。";
    }
    await notificationsUI.loadNotificationSubscriptions(catalog, ws);
    if (ws !== workspaceView) return;
    if (!ws.platforms || roomsUI.isDirect() && state.messages.size === 0) await loadWorkspacePlatforms();
    if (ws !== workspaceView) return;
    renderWorkspace();
    if (completed) {
      ws.closeOnComplete = null;
      $("workspace-dialog").close();
    }
  }

  function renderWorkspacePlatforms() {
    const ws = workspaceView;
    if (!ws) return;
    const options = [new Option("请选择已有连接", "")];
    if (ws.data?.workspace?.binding && !ws.data.workspace.binding.platform_id) options.push(new Option("保留当前旧认证（尚未迁移）", "keep"));
    const provider = $("workspace-kind").value === "scion" ? "scion" : "gitlab";
    for (const item of ws.repositoryPlatforms || ws.platforms || []) {
      if (item.provider !== provider || provider === "scion" && item.validation?.status !== "valid") continue;
      options.push(new Option(`${item.name} · ${item.validation.username || item.validation.account?.id?.value || ""} · ${item.url}`, item.id));
    }
    $("workspace-connection").replaceChildren(...options);
    $("workspace-connection").value = ws.selectedPlatform || "";
  }

  async function loadWorkspacePlatforms() {
    const ws = workspaceView, epoch = state.catalog?.service_epoch;
    if (!ws || !epoch) return;
    const read = ws.platformsRead = (ws.platformsRead || 0) + 1;
    ws.platformsLoading = true; ws.platformsError = "正在加载平台…"; renderWorkspace();
    try {
      const query = new URLSearchParams({ service_epoch: epoch, caller_id: ws.operator });
      const data = await request(`/v1/platforms?${query}`);
      if (ws !== workspaceView || state.catalog?.service_epoch !== epoch || read !== ws.platformsRead) return;
      ws.allPlatforms = data.platforms;
      ws.repositoryPlatforms = data.platforms.filter(item => ["gitlab", "scion"].includes(item.provider));
      ws.repositoryPlatformsError = "";
      ws.platforms = data.platforms.filter(item => item.provider === "gitlab");
      ws.larkPlatforms = data.platforms.filter(item => item.provider === "lark");
      ws.platformsError = ws.platforms.length ? "" : "尚无平台，请先管理平台。";
      renderWorkspacePlatforms();
    } catch { if (ws === workspaceView && state.catalog?.service_epoch === epoch && read === ws.platformsRead) { ws.platformsError = "平台加载失败，请重新打开通知源重试。"; ws.repositoryPlatformsError = "平台加载失败，请重新打开工作目录设置重试。"; } }
    if (ws === workspaceView) { ws.platformsLoading = false; renderWorkspace(); }
  }

  function fillWorkspaceForm() {
    const ws = workspaceView;
    const operation = ws?.data?.operation;
    const unfinished = operation && ["running", "failed"].includes(operation.status);
    const formVersion = ws?.data && JSON.stringify([ws.data.workspace_version, unfinished ? operation.operation_id : ""]);
    if (!ws?.data || ws.formVersion === formVersion) return;
    const current = ws.data.workspace || {}, suggestion = ws.data.suggestions || {};
    const target = unfinished ? operation.target || current : current;
    ws.formVersion = formVersion;
    $("workspace-root").value = current.root || "workspaces";
    // Suggestions only prefill text. Their checkboxes remain off until selected.
    $("workspace-repository").checked = !!target.repository;
    $("workspace-kind").value = target.repository?.kind || suggestion.repository?.kind || "git";
    $("workspace-repo-url").value = target.repository?.url || suggestion.repository?.url || "";
    $("workspace-branch").value = target.repository ? target.repository.branch || "" : suggestion.repository?.branch || "main";
    $("workspace-gitlab").checked = !!target.binding;
    ws.selectedPlatform = target.binding?.platform_id || (current.binding ? "keep" : "");
    renderWorkspacePlatforms();

  }

  function renderWorkspace() {
    notificationsUI.renderGitLabEvents();
    const ws = workspaceView, data = ws?.data, current = data?.workspace, op = data?.operation;
    $("workspace-control").hidden = !state.managed || !ws;
    $("compose-more").disabled = !state.managed || !ws;
    if ($("compose-more").disabled) closeWorkspacePicker();
    $("open-workspace").disabled = !ws;
    const label = current?.repository ? current.repository.url.split("/").pop().replace(/\.git$/, "")
      : !current || current.root === "workspaces" ? "默认目录" : current.root;
    $("workspace-label").textContent = label;
    $("workspace-trigger").title = `工作目录：${label}`;
    $("workspace-selected-label").textContent = label;
    $("workspace-selected-description").textContent = current?.repository ? "各成员使用独立的仓库目录"
      : current && current.root !== "workspaces" ? `房间内目录 · ${current.root}` : "沿用各成员自己的工作目录";
    let status = ws?.pending ? "提交结果待确认" : op?.status === "running" ? workspaceStages[op.phase] || "正在应用"
      : op?.status === "failed" ? workspaceCanRevise() ? "初始化失败 · 可修改后重新应用" : "设置未完成 · 请核查后重试"
      : op?.status === "completed" ? "设置已生效" : "";
    $("workspace-summary").textContent = status;
    $("workspace-summary").hidden = !status || !state.managed || !ws;
    if (!$("workspace-dialog").open) return;
    fillWorkspaceForm();
    $("workspace-current").textContent = current ? `当前目录：${current.root} · 配置版本 ${data.workspace_version}` : "沿用原成员目录";
    $("workspace-paths").replaceChildren(...(data?.members || []).map(member => {
      const item = document.createElement("li"); item.textContent = `${resolveAgentName(state.members, member.agent_id, member.membership_id)} · ${member.workspace}`; return item;
    }));
    const repo = !!current?.repository, binding = !!current?.binding;
    const scion = $("workspace-kind").value === "scion";
    if (scion) $("workspace-gitlab").checked = true;
    const git = $("workspace-repository").checked, gitlab = git && $("workspace-gitlab").checked;
    $("workspace-kind").disabled = repo;
    $("workspace-binding-label").textContent = scion ? "绑定 Scion 连接（必需）" : "绑定 GitLab";
    $("workspace-fields").disabled = !data || !ws.connected || workspaceFormBusy() || roomsUI.managementBusy();
    $("workspace-root").readOnly = repo || git;
    $("workspace-root-hint").textContent = repo ? "仓库目录已固定；各成员使用独立 clone。" : git
      ? "首次关联会在新的受管仓库目录中分别 clone；旧目录文件保留。" : "切换目录不搬迁文件，原目录保留。";
    $("workspace-repository").disabled = repo;
    $("workspace-repo-url").readOnly = repo;
    $("workspace-branch").readOnly = repo;
    $("workspace-gitlab").disabled = binding || scion;
    $("workspace-git-fields").hidden = !git;
    $("workspace-gitlab-fields").hidden = !gitlab;
    $("workspace-git-auth-hint").hidden = gitlab;
    $("workspace-repo-url").required = git;
    $("workspace-repo-url").disabled = !git;
    $("workspace-connection").disabled = !gitlab;
    $("workspace-connection-feedback").textContent = ws.repositoryPlatformsError || ($("workspace-connection").children.length <= 1 ? `尚无可选的 ${scion ? "Scion 已验证" : "GitLab"} 连接，请先管理平台。` : "选择同类型连接，应用时核实仓库访问权限；Scion 会核实分支存在。");
    $("workspace-credential-status").hidden = !binding;
    $("workspace-credential-status").textContent = binding ? data.credential_status === "configured" ? current.binding.platform_id ? current.binding.provider === "scion" ? `Scion 连接已配置 · ${current.binding.project}` : `共享连接已配置 · 项目 ID ${current.binding.project_id}（绑定时已核实）` : "旧凭据已配置（远端权限未验证）" : "凭据缺失，请检查平台或旧认证。" : "";
    $("workspace-status").textContent = status;
    notice("workspace-error", ws.error || (op?.status === "failed" ? workspaceErrors[op.error_code] || "操作未完成，请检查服务状态。" : ""));
    $("workspace-operation-detail").hidden = !op && !ws.pending;
    $("workspace-operation-target").textContent = op ? JSON.stringify({ operation_id: op.operation_id, status: op.status, phase: op.phase, target: op.target }, null, 2) : ws.pending?.operation_id || "";
    $("workspace-apply").disabled = !data || !ws.connected || workspaceFormBusy() || roomsUI.managementBusy() ||
      state.draft.sending || !!state.draft.pending || state.draft.restartStarting || !!state.draft.restartRequest || state.restart?.status === "running";
    $("workspace-apply").textContent = ws.sending ? "正在提交…" : workspaceCanRevise() ? "修改并重新应用" : git ? repo ? "应用绑定设置" : "绑定并初始化" : "应用";
    $("workspace-retry").hidden = op?.status !== "failed" || !!ws.pending;
    $("workspace-retry").disabled = ws.sending || !ws.connected || !!state.catalog?.busy;
    $("workspace-new").hidden = !ws.missing;
    $("workspace-refresh").disabled = ws.sending;
  }

  function platformProjectFromRepository(value, provider = "gitlab") {
    // Match the server's HTTPS origin and project-path rules. Keep explicit ports
    // (including :443) so the derived origin exactly matches the repository host.
    const raw = value.trim(), parts = /^https:\/\/([^/?#]+)(\/[^?#]*)?$/.exec(raw);
    const invalid = () => new Error("请填写有效的仓库 HTTPS URL，以解析平台仓库；地址不能含用户名、密码、Token 或查询参数。");
    if (!parts || raw.length > 4096 || /[\s\\]/.test(raw)) throw invalid();
    let url;
    try { url = new URL(raw); } catch { throw invalid(); }
    if (!url.hostname || url.username || url.password) throw invalid();
    const path = (parts[2] || "").replace(/^\//, "").replace(/\/$/, "");
    const project = provider === "scion" ? path : path.replace(/\.git$/, "");
    if (path.length > 2048 || !project.includes("/") || project.split("/").some(part =>
      !/^[A-Za-z0-9_.-]+$/.test(part) || part === "." || part === "..")) throw invalid();
    return { provider, url: `https://${parts[1].toLowerCase()}`, project };
  }

  function workspaceTarget() {
    const old = workspaceView.data.workspace;
    const git = $("workspace-repository").checked;
    const target = { root: old?.repository || !git ? $("workspace-root").value.trim() : old?.root || "" };
    if (git) {
      const kind = $("workspace-kind").value || "git";
      target.repository = old?.repository ? { ...old.repository } : { url: $("workspace-repo-url").value.trim() };
      if (!old?.repository) target.repository.kind = kind;
      if (!old?.repository && $("workspace-branch").value.trim()) target.repository.branch = $("workspace-branch").value.trim();
      if ($("workspace-gitlab").checked) {
        target.binding = platformProjectFromRepository(target.repository.url, kind === "scion" ? "scion" : "gitlab");
        if (!old?.repository) {
          target.repository.url = `${target.binding.url}/${target.binding.project}${kind === "scion" ? "" : ".git"}`;
          $("workspace-repo-url").value = target.repository.url;
        }
        const selected = $("workspace-connection").value;
        if (selected === "keep") {
          if (!old?.binding || old.binding.platform_id) throw new Error("请选择平台。");
          target.binding = { ...old.binding };
        } else {
          const connection = (workspaceView.repositoryPlatforms || workspaceView.platforms)?.find(item => item.id === selected);
          if (!connection || kind === "scion" && connection.validation?.status !== "valid") throw new Error("请选择已验证的平台，或前往管理平台。");
          if (connection.provider !== target.binding.provider || connection.url !== target.binding.url) throw new Error("所选连接与仓库实例不一致。");
          target.binding.platform_id = connection.id;
        }
      }
    }
    // Reject embedded credentials before any non-sensitive request is retained.
    for (const value of [target.repository?.url, target.binding?.url].filter(Boolean)) {
      let url;
      try { url = new URL(value); } catch { throw new Error("请填写有效的 HTTPS 地址。"); }
      if (url.protocol !== "https:" || url.username || url.password || url.search || url.hash) throw new Error("请使用无用户名、密码、Token 或查询参数的 HTTPS 地址。");
    }
    return target;
  }

  async function submitWorkspace(retry = false) {
    const ws = workspaceView;
    if (!ws || ws.sending || !ws.connected || (retry ? $("workspace-retry").disabled : $("workspace-apply").disabled)) return;
    let payload;
    try {
      payload = { ...ws.room, service_epoch: state.catalog.service_epoch, caller: { kind: "human", id: ws.operator },
        operation_id: retry ? ws.data.operation.operation_id : randomUUID().replaceAll("-", "") };
      if (!retry) {
        payload.expected_version = ws.data.workspace_version; payload.target = workspaceTarget();
        if (workspaceCanRevise()) payload.replaces_operation_id = ws.data.operation.operation_id;
        if (payload.target.binding?.platform_id) payload.platform_version = (ws.repositoryPlatforms || ws.platforms).find(item => item.id === payload.target.binding.platform_id).version;
      }
    } catch (error) { ws.error = error.message; renderWorkspace(); return; }
    ws.pending = { operation_id: payload.operation_id }; ws.missing = false; saveWorkspacePending(ws);
    ws.closeOnComplete = payload.operation_id;
    ws.sending = true; ws.error = "";

    renderWorkspace(); conversationUI.composerState(); membersUI.renderMembers(); roomsUI.renderRooms();
    try {
      const op = await request(retry ? "/v1/workspace/retry" : "/v1/workspace/apply", payload);
      ws.data.operation = op; ws.pending = null; saveWorkspacePending(ws);
    } catch (error) {
      if (["request", "identity", "conflict", "capacity", "busy"].includes(error.code)) {
        ws.closeOnComplete = null;
        ws.pending = null; saveWorkspacePending(ws);
        ws.error = error.code === "conflict" ? "配置已变化，请查看最新状态后重新应用。" : "请求被拒绝，请核对配置、当前房间和服务状态；共享连接变化后请重新打开设置加载连接。";
      } else ws.error = "提交结果尚未确认，正在查询原操作；不会自动重发。";
    } finally {
   ws.sending = false;
      await refresh(); renderWorkspace(); conversationUI.composerState(); roomsUI.renderRooms();
    }
  }

  function closeWorkspacePicker() {
    $("workspace-picker").open = false;
    syncWorkspacePicker();
  }

  function syncWorkspacePicker() {
    const open = $("workspace-picker").open;
    $("workspace-trigger").setAttribute("aria-expanded", String(open));
    $("compose-more").setAttribute("aria-expanded", String(open));
    $("compose-more").setAttribute("aria-label", open ? "收起工作目录" : "展开工作目录");
    runtime.navigationChanged();
  }

  function focusWorkspaceTrigger() {
    $($("compose").classList.contains("has-message") ? "message-input" : "compose-more").focus();
  }

  async function openWorkspace() {
    if (!workspaceView) return;
    closeWorkspacePicker();
    workspaceReturn = captureReturnView(env, runtime, { opener: $($("compose").classList.contains("has-message") ? "message-input" : "compose-more") });
    $("workspace-dialog").showModal(); renderWorkspace(); $("close-workspace").focus(); await loadWorkspacePlatforms();
  }

  function mount() {
    $("compose-more").addEventListener("click", () => {
      const atBottom = conversationUI.nearBottom();
      $("workspace-picker").open = !$("workspace-picker").open;
      syncWorkspacePicker();
      if (atBottom) requestAnimationFrame(conversationUI.bottom);
    });
    $("workspace-picker").addEventListener("toggle", () => {
      syncWorkspacePicker();
    });

    const dismissPicker = event => {
      if (event.key === "Escape" && $("workspace-picker").open) {
        event.preventDefault(); closeWorkspacePicker(); focusWorkspaceTrigger();
      }
    };
    $("workspace-picker").addEventListener("keydown", dismissPicker);
    $("compose-more").addEventListener("keydown", dismissPicker);

    document.addEventListener("click", event => {
      if ($("workspace-picker").open && event.target !== $("compose-more") && !$("compose-more").contains(event.target) && !$("workspace-picker").contains(event.target)) closeWorkspacePicker();
    });

    $("workspace-picker").addEventListener("focusout", event => {
      if (event.relatedTarget && event.relatedTarget !== $("compose-more") && !$("workspace-picker").contains(event.relatedTarget)) closeWorkspacePicker();
    });

    $("open-workspace").addEventListener("click", openWorkspace);

    $("close-workspace").addEventListener("click", () => $("workspace-dialog").close());

    $("workspace-dialog").addEventListener("close", () => {
      if ($("notification-detail").open) $("gitlab-project-summary").focus();
      else focusWorkspaceTrigger();
      const back = workspaceReturn; workspaceReturn = null;
      back?.restore();
    });

    $("workspace-kind").addEventListener("change", () => { if (workspaceView) workspaceView.selectedPlatform = ""; renderWorkspacePlatforms(); renderWorkspace(); });

    for (const id of ["workspace-repository", "workspace-gitlab"]) $(id).addEventListener("change", renderWorkspace);

    $("workspace-connection").addEventListener("change", () => { if (workspaceView) workspaceView.selectedPlatform = $("workspace-connection").value; });

    $("workspace-manage-platforms").addEventListener("click", async () => {
      await settingsUI.openSettings("platforms", $("workspace-manage-platforms"));
    });

    $("workspace-form").addEventListener("submit", event => { event.preventDefault(); return submitWorkspace(); });

    $("workspace-retry").addEventListener("click", () => submitWorkspace(true));

    $("workspace-refresh").addEventListener("click", async () => { await refresh(); await loadWorkspacePlatforms(); });

    $("workspace-new").addEventListener("click", () => {
      const ws = workspaceView;
      if (!ws?.missing || ws.sending) return;
      ws.pending = null; ws.missing = false; ws.error = "请核对目标和平台，再点击应用；原请求不会自动重发。";
       saveWorkspacePending(ws); renderWorkspace(); conversationUI.composerState(); roomsUI.renderRooms();
    });
  }

  return {
    mount,
    closeWorkspacePicker,
    renderWorkspace,
    selectWorkspace,
    get workspaceView() { return workspaceView; },
    loadWorkspacePlatforms,
    workspaceBusy,
    workspaceKey,
    workspaceStages,
    syncWorkspace
  };
}
