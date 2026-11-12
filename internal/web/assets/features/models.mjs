import { fillModelParameters, readModelParameters, syncModelProtocol } from "../model-form.mjs";

export function createModels({env, runtime, openSettings}) {
  const {document, window, request, Option, setTimeout, clearTimeout} = env;
  const crypto = window.crypto || globalThis.crypto;
  const {state, $} = runtime;
  const field = id => $(`catalog-${id}`);
  let data, editor, saving = false, read = 0, timer, memberView, memberSaving = false, defaultReview;
  let lastOperation, operationRenderKey;
  const active = () => state.managed && !!state.catalog?.service_epoch;
  const envelope = () => ({service_epoch:state.catalog.service_epoch, caller:{kind:"human", id:state.operator}});
  function text(tag, value, className) { const node = document.createElement(tag); node.textContent = value; if (className) node.className = className; return node; }
  function button(label, action) { const node = text("button", label, "text-button"); node.type = "button"; node.addEventListener("click", action); return node; }
  function feedback(message) { $("model-feedback").textContent = message; }
  function keyState() {
    const reuse = editor?.entry && data?.entry_states?.[editor.entry.id]?.credential_configured !== false && !field("replace-key").checked;
    field("api-key").hidden = !!reuse; field("api-key").required = !reuse;
    if (reuse) field("api-key").value = "";
    field("key-state").textContent = reuse ? "密钥已配置，本次验证将复用。" : "请输入 API Key，将保存到部署机私有目录。";
  }
  function closeEditor() { field("api-key").value = ""; editor = undefined; $("model-edit-form").hidden = true; }
  function openEditor(entry) {
    if (saving || !data) return;
    lastOperation = undefined; feedback("");
    editor = {entry, version:data.catalog.version, id:entry?.id || `model-${crypto.randomUUID().replaceAll("-", "").slice(0,16)}`};
    $("model-edit-title").textContent = entry ? "编辑模型" : "新增模型";
    $("model-entry-name").value = entry?.name || "";
    fillModelParameters(field, entry); field("api-key").value = ""; field("replace-key").checked = data.entry_states?.[entry?.id]?.credential_configured === false;
    field("replace-key-label").hidden = !entry;
    const users = data.members.filter(m => m.target_entry_id === entry?.id).length;
    $("model-impact").textContent = entry ? `执行配置变化将影响 ${users} 个 Agent，各自在当前回合结束后切换；私有上下文将重置。仅改名不会轮换。` : "新增后可设为默认，或为指定 Agent 选择。";
    $("model-edit-form").hidden = false; keyState(); $("model-entry-name").focus();
  }
  async function load() {
    if (!active()) return;
    const generation = ++read, epoch = state.catalog.service_epoch;
    try {
      const next = await request("/v1/models");
      if (generation !== read || state.catalog?.service_epoch !== epoch || next.service_epoch && next.service_epoch !== epoch) return;
      data = next; render(); renderMember();
      if (lastOperation?.epoch === epoch) {
        const op = data.operations.find(op => op.id === lastOperation.id);
        if (op && ["completed", "failed", "cancelled"].includes(op.status)) {
          feedback(""); lastOperation = undefined;
        }
      }
      if (data.operations.some(op => ["validating","prepared","applying"].includes(op.status))) schedule();
      else if ($("settings-dialog").open || memberView && currentMemberView(memberView)) schedule(2000);
    } catch (error) { if (generation === read) {feedback(error.message); if (memberView && currentMemberView(memberView)) {memberView.error=error.message;renderMember();}} }
  }
  function schedule(delay = 800) { if (timer) clearTimeout(timer); timer = setTimeout(() => {timer = undefined; void load();}, delay); }
  function render() {
    if (defaultReview?.parentElement) return;
    $("model-list").replaceChildren(...data.catalog.entries.map(entry => {
      const row = document.createElement("section"); row.className = "model-row";
      const status = data.entry_states?.[entry.id];
      row.append(text("strong", `${entry.name}${entry.id === data.catalog.default ? " · 默认" : ""}${entry.pending_deletion ? " · 待删除" : ""}`), text("p", `${entry.model_id} · revision ${entry.revision} · ${status?.validated === false || status?.validated !== true && entry.unverified ? "待验证" : "已验证"}${status?.credential_configured === false ? " · 请补填 API Key" : ""}`, "quiet"));
      const users = data.members.filter(m => m.target_entry_id === entry.id || m.selection.mode === "explicit" && m.selection.entry_id === entry.id);
      const details = document.createElement("details"); details.append(text("summary", `${users.length} 个使用者`));
      for (const user of users) details.append(text("p", `${user.room_id} / ${user.agent_id} · ${user.selection.mode === "default" ? "使用默认" : "固定使用"}${user.operation_id ? " · 等待应用" : ""}`, "quiet"));
      for (const template of status?.templates || []) details.append(text("p", `模板：${template}（删除前须修正）`, "quiet"));
      row.append(details);
      if (entry.pending_deletion) {row.append(text("p", "引用替换尚未完成，请查看下方进度并重试。", "quiet"));return row;}
      row.append(button("编辑", () => openEditor(entry)), button("删除", () => reviewDeletion(entry, row)));
      if (entry.id !== data.catalog.default) row.append(button("设为默认", () => {
        if (saving) return;
        const followers = data.members.filter(m => m.selection.mode === "default").length, expectedVersion = data.catalog.version;
        closeEditor(); defaultReview?.remove();
        const review = document.createElement("div"); review.id = "model-default-confirm"; defaultReview = review;
        review.append(text("p", `将 ${entry.name} 设为默认，影响 ${followers} 个使用默认的 Agent；实际配置变化时将重置私有上下文。`), button("验证并设为默认", () => {review.remove(); void submit({kind:"default",entry_id:entry.id,expected_version:expectedVersion});}), button("取消", () => review.remove()));
        row.append(review);
      }));
      return row;
    }));
    renderOperations();
  }
  function renderOperations() {
    // The API orders operations by creation time, newest first.
    const op = data.operations[0];
    const key = JSON.stringify([state.catalog.service_epoch, op]);
    if (key === operationRenderKey) return;
    operationRenderKey = key;
    const labels = {validating:"正在验证",prepared:"准备应用",applying:"正在应用",completed:"已完成",failed:"需处理",cancelled:"已取消"};
    if (op) {
      const section = document.createElement("div"); section.className = "model-operation";
      const label = labels[op.status] || op.status;
      const progress = op.members.length ? ` · 完成 ${op.completed} / 等待 ${op.waiting} / 失败 ${op.failed}` : "";
      section.append(text("p", label + progress, "quiet"));
      if (op.message) section.append(text("p", op.message, "quiet"));
      for (const member of op.members.filter(m => m.status !== "completed")) section.append(text("p", `${member.agent_id} · ${member.status === "failed" ? member.error : "等待当前回合结束"}`, "quiet"));
      if (op.status === "failed" && op.committed) section.append(button("重试未完成成员", () => action(op.id,"retry")));
      if (op.status === "validating") section.append(button("取消", () => action(op.id,"cancel")));
      $("model-operations").replaceChildren(section);
    } else $("model-operations").replaceChildren();
  }
  function reviewDeletion(entry, row) {
    if (saving) return;
    closeEditor(); defaultReview?.remove();
    const review = document.createElement("div"); review.id = "model-delete-confirm"; defaultReview = review;
    const templates = data.entry_states?.[entry.id]?.templates || [];
    const users = data.members.filter(m => m.target_entry_id === entry.id || m.selection.mode === "explicit" && m.selection.entry_id === entry.id);
    const used = entry.id === data.catalog.default || users.length > 0;
    const replacements = data.catalog.entries.filter(e => e.id !== entry.id && !e.pending_deletion);
    const version = data.catalog.version;
    review.append(text("p", `删除 ${entry.name}，影响 ${users.length} 个 Agent（含未打开的会话）。使用默认者保持默认；固定引用者改为固定使用替代模型。实际模型变化会重置私有上下文。`));
    for (const user of users) review.append(text("p", `${user.room_id} / ${user.agent_id}${user.operation_id ? " · 有未完成操作，请先处理" : ""}`, "quiet"));
    if (templates.length) review.append(text("p", `请先修正模板引用：${templates.join("、")}`));
    else if (used && !replacements.length) review.append(text("p", "请先新增替代模型，不能删除唯一默认模型。"));
    else {
      const select = document.createElement("select"); select.id = "model-delete-replacement";
      if (used) {select.append(...replacements.map(e => new Option(e.name,e.id)));select.value=replacements.find(e=>e.id===data.catalog.default)?.id || replacements[0].id;review.append(text("p","替代模型："),select);}
      review.append(button(used ? "验证并删除、替换引用" : "确认删除", () => {const replacement_id=used?select.value:"";review.remove();void submit({kind:"delete",entry_id:entry.id,replacement_id,expected_version:version});}));
    }
    review.append(button("取消", () => review.remove()));row.append(review);
  }
  async function action(id, action) {
    try {await request("/v1/models/operation", {...envelope(),id,action}); lastOperation = {id, epoch:state.catalog.service_epoch}; await load();}
    catch(error) {lastOperation = undefined; feedback(error.message);}
  }
  async function submit(input) {
    if (saving) return;
    saving = true;
    lastOperation = undefined;
    const epoch = state.catalog.service_epoch, requestID = crypto.randomUUID();
    let accepted;
    $("model-save").disabled = true; $("model-new").disabled = true;
    try {
      await request("/v1/models/change", {...envelope(),request_id:requestID,...input});
      if (state.catalog?.service_epoch !== epoch) return;
      lastOperation = {id:requestID, epoch};
      accepted = {id:requestID, epoch};
      closeEditor(); feedback("已受理；验证通过后保存并自动应用。关闭页面后仍会继续，请查看应用进度。");
    } catch(error) {
      if (state.catalog?.service_epoch !== epoch) return;
      try {await request(`/v1/models/operation?id=${encodeURIComponent(requestID)}`); lastOperation = {id:requestID, epoch}; accepted = {id:requestID, epoch}; closeEditor();feedback("已找到原操作，正在读取进度。");}
      catch {feedback(`提交未确认：${error.message}。请核对进度后再保存；其他草稿已保留。`);}
    } finally {
      input.api_key = ""; field("api-key").value = ""; saving = false;
      $("model-save").disabled = false; $("model-new").disabled = false;
      await load();
    }
    return accepted;
  }
  function currentMemberView(v) { return v === memberView && $("activity-dialog").open && state.info?.room_id === v.room_id && state.info?.room_instance === v.room_instance && state.catalog?.service_epoch === v.epoch; }
  async function openMember(member) {
    memberView = {room_id:state.info.room_id,room_instance:state.info.room_instance,agent_id:member.agent_id,membership_id:member.membership_id,epoch:state.catalog?.service_epoch};
    $("member-model-feedback").textContent = ""; $("member-model").hidden = true;
    await load();
  }
  function renderMember() {
    const v = memberView;
    if (!v || !currentMemberView(v) || !data) return;
    const member = data.members.find(m => m.room_id === v.room_id && m.room_instance === v.room_instance && m.agent_id === v.agent_id && m.membership_id === v.membership_id);
    if (!member) {$("member-model").hidden = true; return;}
    v.member = member;
    v.expectedVersion = member.version; v.catalogVersion = data.catalog.version;
    $("member-model").hidden = false;
    const select = $("member-model-selection");
    const entries = data.catalog.entries.filter(e=>!e.pending_deletion);
    const optionsKey = JSON.stringify([data.catalog.default, entries.map(e=>[e.id,e.name])]);
    if (select.dataset.optionsKey !== optionsKey) {
      select.replaceChildren(new Option("默认","default"), ...entries.map(e => new Option(e.name,e.id)));
      select.dataset.optionsKey = optionsKey;
    }
    const operation = data.operations.find(op=>op.id === (member.operation_id || v.operationID));
    const pending = memberSaving || !!member.operation_id || operation && ["validating","prepared","applying"].includes(operation.status);
    if (!pending) v.pendingSelection = undefined;
    select.value = v.pendingSelection ?? (member.selection.mode === "default" ? "default" : member.selection.entry_id);
    select.dataset.mode = select.value === "default" ? "default" : "explicit";
    select.title = `${select.value === "default" ? `默认模型：${entries.find(e=>e.id===data.catalog.default)?.name || ""}。` : ""}切换模型；当前回合结束后应用，私有上下文将重置。`;
    select.disabled = !!pending || saving;
    const actual = member.actual;
    const error = v.error || (operation?.status === "failed" ? operation.message || "模型应用失败" : "");
    const status = error ? `模型切换失败：${error}` : pending ? operation?.status === "validating" ? "正在验证模型" : "正在应用模型，等待当前回合结束" : actual?.pending ? "模型已应用，待下一次工作启动" : actual ? "模型已生效" : "模型尚未启动";
    const detail = `${status}${actual ? `；当前 Session：${actual.model_id}` : ""}。点击查看模型设置。`;
    const progress = $("member-model-progress"); progress.dataset.state = error ? "error" : pending ? "busy" : "ready"; progress.title = detail; progress.setAttribute("aria-label", detail);
    $("member-model-ok").hidden = !!error || !!pending; $("member-model-busy").hidden = !!error || !pending; $("member-model-error-icon").hidden = !error;
    $("member-model-feedback").textContent = status;
    if (pending) schedule();
  }
  function mount() {
    $("model-new").addEventListener("click", () => openEditor());
    $("model-edit-cancel").addEventListener("click", closeEditor);
    field("replace-key").addEventListener("change",keyState);
    field("protocol").addEventListener("change",() => syncModelProtocol(field));
    $("model-edit-form").addEventListener("submit",event => {
      event.preventDefault();if (!editor || saving) return;
      const entry = {id:editor.id,name:$("model-entry-name").value.trim(),...readModelParameters(field)};
      const input = {kind:"save",entry,expected_version:editor.version};
      if (!editor.entry || field("replace-key").checked) input.api_key = field("api-key").value;
      field("api-key").value = ""; void submit(input);
    });
    $("member-model-progress").addEventListener("click",() => openSettings());
    $("member-model-selection").addEventListener("change",async () => {
      const v = memberView;if (!v || !currentMemberView(v) || !v.member || memberSaving || saving || v.member.operation_id) return;
      const value = $("member-model-selection").value;
      const selection = value === "default" ? {mode:"default"} : {mode:"explicit",entry_id:value};
      const identity = {room_id:v.room_id,room_instance:v.room_instance,agent_id:v.agent_id,membership_id:v.membership_id};
      if (selection.mode === v.member.selection.mode && selection.entry_id === v.member.selection.entry_id) return;
      const expectedVersion = v.catalogVersion, expectedSelectionVersion = v.expectedVersion;
      memberSaving = true; v.pendingSelection=value; v.error=""; renderMember();
      const accepted = await submit({kind:"selection",...identity,selection,expected_version:expectedVersion,expected_selection_version:expectedSelectionVersion});
      memberSaving=false;
      if (currentMemberView(v)) {v.operationID=accepted?.id;if (!accepted) v.error=$("model-feedback").textContent || "提交未确认，请查看模型设置。";renderMember();}
    });
    window.addEventListener("pagehide",() => {field("api-key").value="";});
  }
  return {mount,load,openMember,clearKey:() => {field("api-key").value="";defaultReview?.remove();defaultReview=undefined;}};
}
