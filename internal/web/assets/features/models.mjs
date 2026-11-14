import { randomUUID } from "../random-id.mjs";
import { createNavigationIcon } from "../icons.mjs";
import { captureReturnView } from "../view-return.mjs";
import { fillModelParameters, readModelParameters, syncModelProtocol, modelDraftFields, snapshotModelFields, restoreModelFields, setModelKeyVisible } from "../model-form.mjs";

export function createModels({env, runtime, openSettings, onMemberStatus}) {
  const {document, window, request, Option, setTimeout, clearTimeout} = env;
  const crypto = window.crypto || globalThis.crypto;
  const {state, $, setSelectOptions} = runtime;
  const field = id => $(`catalog-${id}`);
  let data, editor, saving = false, read = 0, timer, memberView, memberSaving = false, defaultReview;
  let lastOperation, operationRenderKey, listRenderKey, restoredKey;
  let editorReturn;
  const operationRows = new Map();
  const draftKey = () => JSON.stringify(["teamx.model-draft",state.installationID || "",state.operator]);
  function persist() {
    const draft = editor ? {id:editor.id,entry_id:editor.entry?.id,expected_version:editor.version,
      name:$("model-entry-name").value,fields:snapshotModelFields(field),replace_key:!!field("replace-key").checked} : undefined;
    try {
      runtime.storage?.setItem(restoredKey || draftKey(), JSON.stringify({draft,operation:lastOperation?.id}));
    } catch { /* Optional tab storage; secrets are never part of this record. */ }
  }
  function restore() {
    if (restoredKey === draftKey()) return;
    restoredKey = draftKey(); editor=undefined; lastOperation=undefined; listRenderKey=undefined; $("model-edit-form").hidden=true; $("model-list-view").hidden=false;
    let saved; try { saved = JSON.parse(runtime.storage?.getItem(restoredKey)); } catch { return; }
    if (saved?.draft && Number.isSafeInteger(saved.draft.expected_version) && typeof saved.draft.id === "string") {
      const entry = data.catalog.entries.find(entry=>entry.id===saved.draft.entry_id);
      if (!saved.draft.entry_id || entry) openEditor(entry,saved.draft);
      else feedback("草稿对应的模型已移除，请核对模型列表。");
    }
    if (typeof saved?.operation === "string") lastOperation={id:saved.operation,epoch:state.catalog.service_epoch,unconfirmed:true};
    persist();
  }
  const active = () => state.managed && !!state.catalog?.service_epoch;
  const envelope = () => ({service_epoch:state.catalog.service_epoch, caller:{kind:"human", id:state.operator}});
  function text(tag, value, className) { const node = document.createElement(tag); node.textContent = value; if (className) node.className = className; return node; }
  function button(label, action) { const node = text("button", label, "text-button"); node.type = "button"; node.addEventListener("click", action); return node; }
  function feedback(message) { $("model-feedback").textContent = message; }
  function modelName(entry) {
    const imported = entry.id === "daily" && ["原默认模型", "默认模型"].includes(entry.name) || /^imported-\d+$/.test(entry.id) && /^导入模型 \d+$/.test(entry.name);
    return !entry.name || imported ? entry.model_id : entry.name;
  }
  function userName(user) {
    const room = state.catalog?.rooms?.find(room => room.room_id === user.room_id && room.room_instance === user.room_instance);
    const member = room?.members?.find(member => member.id === user.agent_id);
    return `${room?.name || "未命名会话"} / ${member?.name || user.agent_id}`;
  }
  function clearReview() { defaultReview?.remove(); defaultReview=undefined; }
  function renderEditorActions() {
    const entry=editor?.entry, isDefault=entry?.id===data?.catalog.default;
    $("model-management-actions").hidden=!entry;
    $("model-default-state").hidden=!entry || !isDefault;
    $("model-set-default").hidden=!entry || isDefault;
    $("model-save-actions").hidden=!!defaultReview?.parentElement;
    for (const id of ["model-set-default", "model-delete"]) $(id).disabled=saving || !!entry?.pending_deletion || !!defaultReview?.parentElement || !!lastOperation?.unconfirmed;
  }
  function keyState() {
    const reuse = editor?.entry && data?.entry_states?.[editor.entry.id]?.credential_configured !== false && !field("replace-key").checked;
    field("api-key").hidden = !!reuse; field("api-key").required = !reuse;
    field("key-input").hidden = !!reuse; setModelKeyVisible(field, false);
    if (reuse) field("api-key").value = "";
    field("key-state").textContent = reuse ? "密钥已配置，本次验证将复用。" : "密钥保存在运行 TeamX 的设备上。";
  }
  function closeEditor() { clearReview(); field("api-key").value = ""; setModelKeyVisible(field, false); editor = undefined; $("model-edit-form").hidden = true; $("model-list-view").hidden = false; persist(); }
  function returnFromEditor() {
    if (saving) return;
    const back = editorReturn; editorReturn = undefined;
    closeEditor();
    if (!back?.restore($("model-new"))) $("model-new").focus({preventScroll:true});
    runtime.navigationChanged();
  }
  function openEditor(entry, draft) {
    if (saving || !data) return;
    if (lastOperation?.unconfirmed && !draft) {feedback("请先读取原操作进度，再决定是否开始新的编辑。");return;}
    if (!editor) editorReturn = captureReturnView(env, runtime);
    lastOperation = undefined; clearReview(); feedback("");
    editor = {entry, version:draft?.expected_version ?? data.catalog.version, id:draft?.id || entry?.id || `model-${randomUUID(crypto).replaceAll("-", "").slice(0,16)}`};
    $("model-edit-title").textContent = entry ? "编辑模型" : "添加模型";
    $("model-entry-name").value = entry && modelName(entry) !== entry.model_id ? modelName(entry) : "";
    fillModelParameters(field, entry); field("api-key").value = ""; field("replace-key").checked = data.entry_states?.[entry?.id]?.credential_configured === false;
    field("replace-key-label").hidden = !entry;
    const users = data.members.filter(m => m.target_entry_id === entry?.id).length;
    $("model-impact").textContent = entry && users ? `更改配置将影响 ${users} 个 Agent，于当前回合结束后应用并重置私有上下文。仅改名不受影响。` : "";
    if (draft) {
      $("model-entry-name").value = draft.name || ""; restoreModelFields(field,draft.fields); field("replace-key").checked=!!draft.replace_key;
      feedback(editor.version === data.catalog.version ? "已恢复未保存的表单，请重新填写所需密钥。" : "其他页面或设备已修改模型，草稿与原预期版本保留，请核对后再编辑。");
    }
    $("model-entry-name").placeholder=field("model-id").value || "留空使用模型 ID";
    $("model-list-view").hidden = true; $("model-edit-form").hidden = false; keyState(); renderEditorActions(); persist(); $("model-entry-name").focus({preventScroll:true}); $("settings-panels").scrollTop=0;
  }
  async function load() {
    if (!active()) return;
    if (!runtime.canPoll()) { schedule(2000); return; }
    const generation = ++read, epoch = state.catalog.service_epoch;
    try {
      const next = await request("/v1/models");
      if (generation !== read || state.catalog?.service_epoch !== epoch || next.service_epoch && next.service_epoch !== epoch) return;
      data = next; restore();
      if (lastOperation?.id && !data.operations.some(op=>op.id===lastOperation.id)) {
        const originalID=lastOperation.id;
        try {
          const original = await request(`/v1/models/operation?id=${encodeURIComponent(originalID)}`);
          if (generation !== read || state.catalog?.service_epoch !== epoch || lastOperation?.id !== originalID) return;
          data.operations.unshift(original); lastOperation.unconfirmed=false;
        } catch(error) {
          if (generation !== read || state.catalog?.service_epoch !== epoch || lastOperation?.id !== originalID) return;
          if (error.code === "request") {lastOperation=undefined;persist();feedback("原操作未受理，请核对草稿后明确保存。");}
          else {lastOperation.unconfirmed=true;feedback("原模型操作暂未确认，请读取原操作进度；不会自动重新提交。");}
        }
      }
      if (lastOperation && data.operations.some(op=>op.id===lastOperation.id)) {
        lastOperation.unconfirmed=false;
        if (editor) closeEditor();
      }
      render(); renderMember();
      if (lastOperation?.epoch === epoch) {
        const op = data.operations.find(op => op.id === lastOperation.id);
        if (op && ["completed", "failed", "cancelled"].includes(op.status)) {
          feedback(op.status === "completed" ? "模型已保存" : op.status === "cancelled" ? "操作已取消" : ""); lastOperation = undefined; persist();
        }
      }
      if (data.operations.some(op => ["validating","prepared","applying"].includes(op.status))) schedule();
      else if ($("settings-dialog").open || memberView && currentMemberView(memberView)) schedule(2000);
    } catch (error) { if (generation === read) {feedback(error.message); if (memberView && currentMemberView(memberView)) {memberView.error=error.message;renderMember();}} }
  }
  function schedule(delay = 800) { if (timer) clearTimeout(timer); timer = setTimeout(() => {timer = undefined; void load();}, delay); }
  function render() {
    renderEditorActions();
    if (defaultReview?.parentElement) return;
    const rooms = state.catalog?.rooms?.map(room=>[room.room_id,room.room_instance,room.name,room.members]);
    const listKey=JSON.stringify([data.catalog,data.entry_states,rooms,data.members.map(m=>[m.room_id,m.room_instance,m.agent_id,m.target_entry_id,m.selection,m.operation_id])]);
    if (listKey !== listRenderKey) $("model-list").replaceChildren(...data.catalog.entries.map(entry => {
      const row = document.createElement("section"); row.className = "model-row";
      const status = data.entry_states?.[entry.id], name=modelName(entry);
      const main = button("", () => openEditor(entry)); main.className = "model-row-main"; main.setAttribute("aria-label", `编辑模型 ${name}`);
      const content=text("span", "", "model-row-content"), title=text("span", "", "model-row-title");
      title.append(text("strong", name));
      if (entry.id === data.catalog.default) title.append(text("span", "默认", "model-default-badge"));
      if (entry.pending_deletion) title.append(text("span", "待删除", "model-state"));
      content.append(title);
      if (name !== entry.model_id) content.append(text("span", entry.model_id, "model-id"));
      const chevron = createNavigationIcon(document); chevron.classList.add("model-row-chevron");
      main.append(content, chevron); main.disabled=!!entry.pending_deletion;
      row.append(main);
      if (status?.credential_configured === false || status?.validated === false || status?.validated !== true && entry.unverified) row.append(text("p", status?.credential_configured === false ? "请补填 API Key" : "待验证", "model-state"));
      const users = data.members.filter(m => m.target_entry_id === entry.id || m.selection.mode === "explicit" && m.selection.entry_id === entry.id);
      const details = document.createElement("details"); details.className = "model-users";
      details.append(text("summary", users.length ? `${users.length} 个 Agent 使用` : "模板引用"));
      details.addEventListener("click", event=>event.stopPropagation?.());
      for (const user of users) details.append(text("p", `${userName(user)} · ${user.selection.mode === "default" ? "跟随默认" : "固定使用"}${user.operation_id ? " · 等待应用" : ""}`, "quiet"));
      for (const template of status?.templates || []) details.append(text("p", `模板：${template}（删除前须修正）`, "quiet"));
      if (users.length || status?.templates?.length) row.append(details);
      if (entry.pending_deletion) row.append(text("p", "引用替换尚未完成，请查看下方进度并重试。", "quiet"));
      return row;
    }));
    listRenderKey=listKey;
    $("models-empty").hidden = data.catalog.entries.length > 0;
    renderOperations();
    $("model-check-operation").hidden=!lastOperation?.unconfirmed;
  }
  function renderOperations() {
    // Keep the latest feedback concise, but never hide work that still needs
    // attention just because another device or member finished a newer change.
    const operations = data.operations.filter((op, index) => !["completed", "cancelled"].includes(op.status) &&
      (index === 0 || op.id === lastOperation?.id || op.committed || ["validating", "prepared", "applying"].includes(op.status)));
    const key = JSON.stringify([state.catalog.service_epoch, operations]);
    if (key === operationRenderKey) return;
    operationRenderKey = key;
    const labels = {validating:"正在验证",prepared:"准备应用",applying:"正在应用",completed:"已完成",failed:"需处理",cancelled:"已取消"};
    const rows = operations.map(op => {
      const signature = JSON.stringify([state.catalog.service_epoch, op]);
      const existing = operationRows.get(op.id);
      if (existing?.signature === signature) return existing.section;
      const section = document.createElement("div"); section.className = "model-operation"; section.dataset.state = op.status;
      const label = labels[op.status] || op.status;
      const progress = op.waiting ? ` · ${op.waiting} 个 Agent 待应用` : op.failed ? ` · ${op.failed} 个 Agent` : "";
      section.append(text("p", label + progress, "quiet"));
      if (op.message) section.append(text("p", op.message, "quiet"));
      const pending = op.members.filter(m => m.status !== "completed");
      if (pending.length) {
        const details = document.createElement("details"); details.append(text("summary", "查看 Agent 进度"));
        for (const member of pending) details.append(text("p", `${userName(member)} · ${member.status === "failed" ? member.error : "等待当前回合结束"}`, "quiet"));
        section.append(details);
      }
      if (op.status === "failed" && op.committed) section.append(button("重试未完成成员", () => action(op.id,"retry")));
      if (op.status === "validating") section.append(button("取消", () => action(op.id,"cancel")));
      operationRows.set(op.id, {signature, section});
      return section;
    });
    const list = $("model-operations");
    for (const [id, row] of operationRows) if (!rows.includes(row.section)) { row.section.remove(); operationRows.delete(id); }
    // Reuse unchanged rows so another operation's progress cannot steal focus
    // or close an expanded member list.
    for (let index = 0; index < rows.length; index++) if (list.children[index] !== rows[index]) list.insertBefore(rows[index], list.children[index] || null);
    for (const child of [...list.children]) if (!rows.includes(child)) child.remove();
  }
  function reviewDefault() {
    if (!editor?.entry || saving || defaultReview?.parentElement) return;
    const entry=editor.entry, expectedVersion=editor.version;
    const followers=data.members.filter(member=>member.selection.mode==="default").length;
    const review=document.createElement("div"); review.id="model-default-confirm"; defaultReview=review;
    review.append(text("p", `将已保存的 ${modelName(entry)} 设为默认，影响 ${followers} 个 Agent；配置变化时将重置私有上下文。`),
      button("验证并设为默认",()=>{clearReview();void submit({kind:"default",entry_id:entry.id,expected_version:expectedVersion});}),
      button("取消",()=>{clearReview();renderEditorActions();$("model-set-default").focus();}));
    $("model-action-review").append(review); renderEditorActions();
    review.scrollIntoView({block:"nearest"});
  }
  function reviewDeletion() {
    if (!editor?.entry || saving || defaultReview?.parentElement) return;
    const entry=editor.entry, version=editor.version;
    const review=document.createElement("div"); review.id="model-delete-confirm"; defaultReview=review;
    const templates=data.entry_states?.[entry.id]?.templates || [];
    const users=data.members.filter(m=>m.target_entry_id===entry.id || m.selection.mode==="explicit" && m.selection.entry_id===entry.id);
    const used=entry.id===data.catalog.default || users.length>0;
    const replacements=data.catalog.entries.filter(e=>e.id!==entry.id && !e.pending_deletion);
    review.append(text("p", `删除 ${modelName(entry)}，影响 ${users.length} 个 Agent（含未打开的会话）。使用默认者保持默认；固定引用者改为固定使用替代模型。实际模型变化会重置私有上下文。`));
    for (const user of users) review.append(text("p", `${userName(user)}${user.operation_id ? " · 有未完成操作，请先处理" : ""}`, "quiet"));
    if (templates.length) review.append(text("p", `请先修正模板引用：${templates.join("、")}`));
    else if (used && !replacements.length) review.append(text("p", "请先新增替代模型，不能删除唯一默认模型。"));
    else {
      const select=document.createElement("select"); select.id="model-delete-replacement"; select.className="field-control"; select.setAttribute("aria-label","替代模型");
      if (used) {select.append(...replacements.map(e=>new Option(modelName(e),e.id)));select.value=replacements.find(e=>e.id===data.catalog.default)?.id || replacements[0].id;review.append(text("p","替代模型："),select);}
      review.append(button(used ? "验证并删除、替换引用" : "确认删除",()=>{const replacement_id=used?select.value:"";clearReview();void submit({kind:"delete",entry_id:entry.id,replacement_id,expected_version:version});}));
    }
    review.append(button("取消",()=>{clearReview();renderEditorActions();$("model-delete").focus();}));
    $("model-action-review").append(review); renderEditorActions(); review.scrollIntoView({block:"nearest"});
  }
  async function action(id, action) {
    try {await request("/v1/models/operation", {...envelope(),id,action}); lastOperation = {id, epoch:state.catalog.service_epoch}; persist(); await load();}
    catch(error) {lastOperation = undefined; feedback(error.message);}
  }
  async function submit(input) {
    if (saving) return;
    if (lastOperation?.unconfirmed) { feedback("请先读取原操作进度，再决定是否保存。"); return; }
    saving = true;
    lastOperation = undefined;
    const epoch = state.catalog.service_epoch, requestID = randomUUID(crypto);
    lastOperation={id:requestID,epoch,unconfirmed:true}; persist();
    let accepted;
    $("model-save").disabled = true; $("model-new").disabled = true;
    try {
      await request("/v1/models/change", {...envelope(),request_id:requestID,...input});
      if (state.catalog?.service_epoch !== epoch) return;
      lastOperation = {id:requestID, epoch};
      accepted = {id:requestID, epoch};
      closeEditor(); feedback("");
    } catch(error) {
      if (state.catalog?.service_epoch !== epoch) return;
      try {await request(`/v1/models/operation?id=${encodeURIComponent(requestID)}`); lastOperation = {id:requestID, epoch}; accepted = {id:requestID, epoch}; closeEditor();feedback("已找到原操作，正在读取进度。");}
      catch {if (["request","conflict","identity"].includes(error.code)) lastOperation=undefined; persist(); feedback(`${error.code === "conflict" ? "其他页面或设备已修改模型，草稿与原预期版本保留。" : "提交未确认："}${error.message}。请核对进度后再保存。`);}
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
    $("member-model-details").hidden = true; $("member-model-progress").setAttribute("aria-expanded", "false");
    onMemberStatus({});
    await load();
  }
  function renderMember() {
    const v = memberView;
    if (!v || !currentMemberView(v) || !data) return;
    const member = data.members.find(m => m.room_id === v.room_id && m.room_instance === v.room_instance && m.agent_id === v.agent_id && m.membership_id === v.membership_id);
    if (!member) {$("member-model").hidden = true; onMemberStatus({}); return;}
    v.member = member;
    v.expectedVersion = member.version; v.catalogVersion = data.catalog.version;
    $("member-model").hidden = false;
    const select = $("member-model-selection");
    const entries = data.catalog.entries.filter(e=>!e.pending_deletion);
    const defaultEntry = entries.find(e=>e.id===data.catalog.default);
    const operation = data.operations.find(op=>op.id === (member.operation_id || v.operationID));
    const pending = memberSaving || !!member.operation_id || operation && ["validating","prepared","applying"].includes(operation.status);
    if (!pending) v.pendingSelection = undefined;
    setSelectOptions("member-model-selection", [new Option(`跟随默认 · ${defaultEntry ? modelName(defaultEntry) : "未设置"}`,"default"), ...entries.map(e => new Option(modelName(e),e.id))],
      v.pendingSelection ?? (member.selection.mode === "default" ? "default" : member.selection.entry_id));
    select.dataset.mode = select.value === "default" ? "default" : "explicit";
    select.title = `${select.value === "default" ? `默认模型：${modelName(entries.find(e=>e.id===data.catalog.default) || {model_id:""})}。` : ""}切换模型；当前回合结束后应用，私有上下文将重置。`;
    select.disabled = !!pending || saving;
    const actual = member.actual;
    const error = v.error || (operation?.status === "failed" ? operation.message || "模型应用失败" : "");
    const status = error ? `模型切换失败：${error}` : pending ? operation?.status === "validating" ? "正在验证模型" : "正在应用模型，等待当前回合结束" : actual?.pending ? "模型已应用，待下一次工作启动" : actual ? "模型已生效" : "模型尚未启动";
    const detail = `${status}${actual ? `；当前 Session：${actual.model_id}` : ""}。点击查看模型详情。`;
    const progress = $("member-model-progress"); progress.dataset.state = error ? "error" : pending ? "busy" : actual && !actual.pending ? "ready" : "idle"; progress.title = detail; progress.setAttribute("aria-label", detail);
    onMemberStatus({state:progress.dataset.state,detail});
    $("member-model-status").textContent = error ? "切换失败" : pending ? operation?.status === "validating" ? "验证中" : "应用中" : actual?.pending ? "待启动" : actual ? "已生效" : "未启动";
    $("member-model-ok").hidden = !!error || !!pending || !actual || !!actual.pending; $("member-model-busy").hidden = !!error || !pending; $("member-model-error-icon").hidden = !error;
    $("member-model-feedback").textContent = status;
    const targetID = v.pendingSelection && v.pendingSelection !== "default" ? v.pendingSelection : v.pendingSelection === "default" ? data.catalog.default : member.target_entry_id;
    const target = data.catalog.entries.find(entry => entry.id === targetID);
    $("member-model-target").textContent = `目标模型：${target ? `${modelName(target)}${modelName(target) !== target.model_id ? `（${target.model_id}）` : ""}` : targetID || "尚未选择"}`;
    $("member-model-actual").textContent = actual ? `当前 Session：${actual.model_id}${actual.pending ? "（待启动）" : ""}` : "当前 Session：尚未启动";
    $("member-model-retry").hidden = !(operation?.status === "failed" && operation.committed);
    v.operation = operation;
    if (pending) schedule();
  }
  function showMemberDetails() {
    $("member-model-details").hidden = false;
    $("member-model-progress").setAttribute("aria-expanded", "true");
  }

  function mount() {
    for (const id of modelDraftFields) {
      field(id).addEventListener("input",persist); field(id).addEventListener("change",persist);
    }
    $("model-entry-name").addEventListener("input",persist);
    field("replace-key").addEventListener("change",persist);
    $("model-check-operation").addEventListener("click",load);
    $("model-new").addEventListener("click", () => openEditor());
    $("model-delete").addEventListener("click",reviewDeletion);
    $("model-set-default").addEventListener("click",reviewDefault);
    field("model-id").addEventListener("input",()=>{$("model-entry-name").placeholder=field("model-id").value || "留空使用模型 ID";syncModelProtocol(field);});
    for (const id of ["model-edit-cancel", "model-edit-back"]) $(id).addEventListener("click", returnFromEditor);
    field("replace-key").addEventListener("change",keyState);
    field("toggle-key").addEventListener("click",() => setModelKeyVisible(field, field("api-key").type !== "text"));
    field("protocol").addEventListener("change",() => syncModelProtocol(field));
    $("model-edit-form").addEventListener("submit",event => {
      event.preventDefault();if (!editor || saving || defaultReview?.parentElement) return;
      const parameters=readModelParameters(field);
      const entry = {id:editor.id,name:$("model-entry-name").value.trim() || parameters.model_id,...parameters};
      const input = {kind:"save",entry,expected_version:editor.version};
      if (!editor.entry || field("replace-key").checked) input.api_key = field("api-key").value;
      field("api-key").value = ""; setModelKeyVisible(field, false); void submit(input);
    });
    $("member-model-progress").addEventListener("click",() => {
      const details = $("member-model-details"); details.hidden = !details.hidden;
      $("member-model-progress").setAttribute("aria-expanded", String(!details.hidden));
    });
    $("member-model-settings").addEventListener("click",() => openSettings());
    $("member-model-retry").addEventListener("click",async () => {
      const v = memberView;
      if (!v || !currentMemberView(v) || v.operation?.status !== "failed" || !v.operation.committed || $("member-model-retry").disabled) return;
      $("member-model-retry").disabled = true;
      try { await action(v.operation.id,"retry"); }
      finally { $("member-model-retry").disabled = false; }
    });
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
    window.addEventListener("pagehide",() => {persist();field("api-key").value="";setModelKeyVisible(field,false);});
  }
  return {mount,load,openMember,showMemberDetails,returnFromEditor,clearKey:() => {field("api-key").value="";setModelKeyVisible(field,false);clearReview();}};
}
