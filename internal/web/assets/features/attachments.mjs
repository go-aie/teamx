import { createAttachmentDraft, attachmentURL, clipboardImages, uploadImage, formatBytes, imageSummary } from "../attachment-drafts.mjs";
import { openImagePreview } from "../markdown.mjs";

export function createAttachments({ env, runtime, changed, persist }) {
  const { document, window } = env, { state, $, rpc } = runtime;
  const models = new WeakMap(), capabilities = new Map();
  let active = null, pickerOwner = null, rendered = "";
  const identityKey = info => JSON.stringify([state.installationID, info, state.operator]);
  const editable = () => !!state.info && state.connected && !state.draft.pending && !runtime.discussionBusy();
  const capability = () => active && capabilities.get(active.key);
  function closeMenu(focus = false) {
    const wasOpen = !$("attachment-menu").hidden;
    $("attachment-menu").hidden = true; $("compose-more").setAttribute("aria-expanded", "false");
    if (focus) $("compose-more").focus();
    if (wasOpen) runtime.navigationChanged();
  }
  function model(draft, info, operator) {
    if (!models.has(draft)) models.set(draft, createAttachmentDraft({draft, info, operator,
      upload:(...args) => uploadImage(env.fetch, ...args),
      states:(info, operator, ids) => rpc("/v1/attachments/state", {ids, caller:{kind:"human", id:operator}}, info),
      changed:() => { persist(info, operator, draft); if (state.draft === draft) { render(); changed(); } },
      urls:window.URL || URL, timer:env.setTimeout, clearTimer:env.clearTimeout }));
    return models.get(draft);
  }
  async function loadCapabilities(context, force = false) {
    if (!force && capabilities.has(context.key)) return;
    const entry = {loading:true}; capabilities.set(context.key, entry); render();
    try {
      const query = new URLSearchParams({...context.info, caller_id:context.operator});
      const result = await env.request(`/v1/info?${query}`);
      if (result.room_instance !== context.info.room_instance || result.service_epoch !== context.info.service_epoch || !result.attachments) throw new Error("图片能力不可用");
      entry.limits = result.attachments;
    } catch { entry.error = "图片功能暂不可用，点击添加图片重试。"; }
    finally { entry.loading = false; if (active === context) { render(); changed(); } }
  }
  function activate() {
    closeMenu(); rendered = ""; runtime.notice("attachment-error", "");
    active = state.info ? {draft:state.draft, info:{...state.info}, operator:state.operator, key:identityKey(state.info)} : null;
    if (active) {
      active.model = model(active.draft, active.info, active.operator);
      active.model.activate(active.info);
      void loadCapabilities(active);
    }
    render();
  }
  const blockReason = () => active?.model.blockReason(capability()?.limits) || "";
  function render() {
    $("compose-more").disabled = !editable();
    if (!editable()) closeMenu();
    const cap = capability();
    $("add-images").disabled = !editable() || !!cap?.loading;
    $("attachment-hint").textContent = cap?.limits
      ? `PNG、JPEG、WebP · 最多 ${cap.limits.max_images} 张 · 单张 ${formatBytes(cap.limits.image_bytes)} · 合计 ${formatBytes(cap.limits.total_bytes)}`
      : cap?.error || "正在加载图片限制…";
    const list = active?.model.items() || [];
    $("attachment-status").textContent = state.draft.pending && blockReason()
      ? "该次发送的图片不可修改。请先核对历史，使用原请求重试或放弃重试。" : blockReason();
    $("attachment-status").hidden = !$("attachment-status").textContent;
    $("attachment-drafts").hidden = !list.length;
    const key = JSON.stringify([active?.key, !!state.draft.pending, list.map(item => [item.id,item.status,item.url,item.error,item.name])]);
    if (key === rendered) return;
    rendered = key;
    const owner = active;
    $("attachment-drafts").replaceChildren(...list.map(item => {
      const node = document.createElement("div"); node.className = "attachment-draft"; node.dataset.status = item.status;
      const image = document.createElement("img"); image.alt = item.name;
      if (item.url || item.uploaded && item.status === "ready") image.src = item.url || attachmentURL(owner.info, owner.operator, item.id);
      else image.hidden = true;
      image.addEventListener("error", () => { image.hidden = true; });
      const label = document.createElement("span"); label.className = "attachment-draft-label"; label.textContent = item.name;
      const status = document.createElement("span"); status.className = "attachment-draft-status";
      status.textContent = item.error || ({uploading:"上传中…", ready:"已就绪", checking:"正在确认…", reselect:"请重新选择图片"}[item.status] || "等待上传");
      node.append(image, label, status);
      if (["failed", "reselect"].includes(item.status) && !state.draft.pending) {
        const retry = document.createElement("button"); retry.type = "button"; retry.textContent = item.file || item.uploaded ? "重试" : "重新选择";
        retry.addEventListener("click", () => {
          if (active !== owner || !editable()) return;
          if (item.file || item.uploaded) void owner.model.retry(item.id);
          else choose(item.id);
        }); node.append(retry);
      }
      const remove = document.createElement("button"); remove.type = "button"; remove.className = "attachment-remove";
      remove.textContent = "×"; remove.setAttribute("aria-label", `移除图片 ${item.name}`); remove.disabled = !!state.draft.pending;
      remove.addEventListener("click", () => { if (active === owner && editable()) { owner.model.remove(item.id); $("message-input").focus(); } });
      node.append(remove); return node;
    }));
  }
  function choose(replaceID = null) {
    if (!editable()) return;
    if (!capability()?.limits) { void loadCapabilities(active, true); return; }
    pickerOwner = {context:active, replaceID};
    $("attachment-input").value = "";
    closeMenu();
    // Keep this click synchronous with the user's gesture, including mobile Safari.
    $("attachment-input").click();
  }
  function addFiles(files, context = active) {
    const limits = context && capabilities.get(context.key)?.limits;
    if (!files.length || !context || context.draft.pending || !limits) return;
    const errors = context.model.addFiles(files, limits);
    if (state.draft === context.draft) { runtime.notice("attachment-error", errors.join(" ")); render(); changed(); }
  }
  function mount() {
    $("compose-more").addEventListener("click", () => {
      const open = $("attachment-menu").hidden;
      $("attachment-menu").hidden = !open; $("compose-more").setAttribute("aria-expanded", String(open));
      if (open) $("add-images").focus();
      runtime.navigationChanged();
    });
    $("compose-more").addEventListener("keydown", event => {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault(); $("attachment-menu").hidden = false; $("compose-more").setAttribute("aria-expanded", "true"); $("add-images").focus(); runtime.navigationChanged();
      } else if (event.key === "Escape") { event.preventDefault(); closeMenu(true); }
    });
    $("attachment-menu").addEventListener("keydown", event => {
      if (event.key === "Escape") { event.preventDefault(); closeMenu(true); }
      else if (["ArrowDown","ArrowUp","Home","End"].includes(event.key)) { event.preventDefault(); $("add-images").focus(); }
    });
    document.addEventListener("click", event => {
      if (!$("compose-more").contains(event.target) && !$("attachment-menu").contains(event.target)) closeMenu();
    });
    $("attachment-menu").addEventListener("focusout", event => {
      if (event.relatedTarget && !$("attachment-menu").contains(event.relatedTarget) && event.relatedTarget !== $("compose-more")) closeMenu();
    });
    $("add-images").addEventListener("click", () => choose());
    $("attachment-input").addEventListener("change", () => {
      const picked = pickerOwner; pickerOwner = null;
      const files = Array.from($("attachment-input").files || []); $("attachment-input").value = "";
      if (!picked || !files.length || picked.context.draft.pending) return;
      if (picked.replaceID) picked.context.model.remove(picked.replaceID);
      // The OS selector may hide the page; its change event can precede network
      // reconciliation on return. Retain the File in its original draft anyway.
      addFiles(files, picked.context);
      if (state.draft === picked.context.draft) $("compose-more").focus();
    });
    $("attachment-input").addEventListener("cancel", () => { pickerOwner = null; $("compose-more").focus(); });
    $("message-input").addEventListener("paste", event => {
      if (!editable() || !capability()?.limits) return;
      const files = clipboardImages(event.clipboardData);
      if (!files.length) return;
      event.preventDefault();
      const text = event.clipboardData.getData("text/plain");
      if (text) {
        const input = $("message-input"); input.setRangeText(text, input.selectionStart, input.selectionEnd, "end");
        input.dispatchEvent(new window.Event("input", {bubbles:true}));
      }
      addFiles(files);
    });
  }
  function renderMessage(container, message) {
    if (!message.attachments?.length) return;
    const group = document.createElement("div"); group.className = "room-message-attachments"; container.append(group);
    const summary = document.createElement("span"); summary.className = "sr-only";
    summary.textContent = imageSummary(message.attachments.length); group.append(summary);
    for (const attachment of message.attachments) {
      const src = attachmentURL(state.info, state.operator, attachment.id, message.seq);
      const button = document.createElement("button"); button.type = "button"; button.className = "attachment-preview";
      button.setAttribute("aria-label", `放大查看图片 ${attachment.name}`);
      const img = document.createElement("img"); img.className = "message-image"; img.alt = attachment.name;
      img.loading = "lazy"; img.decoding = "async"; img.referrerPolicy = "no-referrer"; img.src = src;
      button.append(img); group.append(button);
      button.addEventListener("click", () => openImagePreview(container.ownerDocument, src, attachment.name));
      img.addEventListener("error", () => { button.replaceChildren(); button.className = "attachment-error"; button.textContent = "图片加载失败，点击重试"; }, {once:true});
    }
  }
  return {mount, activate, render, renderMessage, blockReason, closeMenu,
    clear:draft => models.get(draft)?.clear(), release:draft => models.get(draft)?.dispose()};
}
