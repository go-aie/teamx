import { randomUUID } from "./random-id.mjs";

const validID = id => /^img_[a-f0-9]{32}$/.test(id);
const fields = ["id", "name", "size", "mime_type", "width", "height", "expires_at", "uploaded"];
// Only identities and display metadata cross a page reload. Never persist bytes or URLs.
export function savedAttachments(items) {
  return (Array.isArray(items) ? items : []).filter(item => item && validID(item.id)).slice(0, 4)
    .map(item => Object.fromEntries(fields.filter(key => item[key] !== undefined).map(key => [key, item[key]])));
}
export function attachmentURL(info, operator, id, seq) {
  const query = new URLSearchParams({ room_id: info.room_id, room_instance: info.room_instance,
    service_epoch: info.service_epoch, caller_id: operator, attachment_id: id });
  if (seq) query.set("seq", seq);
  return `/v1/attachments/content?${query}`;
}
export function clipboardImages(data) {
  const items = Array.from(data?.items || []).filter(item => item.kind === "file").map(item => item.getAsFile()).filter(file => file?.type.startsWith("image/"));
  // Chromium may create separate File wrappers with different lastModified
  // values for the same clipboard entry. Match mirrored representations by
  // stable metadata and count, preserving distinct entries within either list.
  const mirrored = new Map();
  const key = file => JSON.stringify([file.name, file.size, file.type]);
  for (const file of items) mirrored.set(key(file), (mirrored.get(key(file)) || 0) + 1);
  const extra = Array.from(data?.files || []).filter(file => {
    if (!file.type.startsWith("image/")) return false;
    const count = mirrored.get(key(file)) || 0;
    if (!count) return true;
    mirrored.set(key(file), count - 1);
    return false;
  });
  return [...items, ...extra];
}
export const imageSummary = count => count === 1 ? "[图片]" : `[图片 ×${count}]`;

export async function uploadImage(fetcher, info, operator, item, file, signal) {
  const query = new URLSearchParams({ room_id: info.room_id, room_instance: info.room_instance,
    service_epoch: info.service_epoch, caller_id: operator, attachment_id: item.id });
  const form = new FormData(); form.append("file", file, file.name);
  const response = await fetcher(`/v1/attachments/upload?${query}`, {method:"POST", body:form, signal, cache:"no-store"});
  const result = await response.json();
  if (!response.ok) throw new Error(result.message || result.error || `图片上传失败 (${response.status})`);
  return result;
}

// A controller is owned by one installation/Room/operator draft. Async work only
// mutates that owner; selecting another conversation does not cancel its upload.
export function createAttachmentDraft({ draft, info, operator, upload, states, changed,
  urls = URL, timer = setTimeout, clearTimer = clearTimeout, now = () => Date.now() }) {
  draft.attachments = savedAttachments(draft.attachments);
  const local = new Map(draft.attachments.map(item => [item.id, {status:item.uploaded ? "checking" : "reselect"}]));
  let identity = {...info}, checking = null, disposed = false, expiryTimer = null;
  const items = () => draft.attachments.map(item => ({...item, ...local.get(item.id)}));
  function notify() {
    if (disposed) return;
    clearTimer(expiryTimer);
    const expires = draft.attachments.filter(item => local.get(item.id)?.status === "ready" && item.expires_at)
      .map(item => Date.parse(item.expires_at)).filter(value => Number.isFinite(value) && value > now());
    if (expires.length) expiryTimer = timer(() => { expiryTimer = null; void restore(); }, Math.max(1, Math.min(...expires) - now()));
    changed();
  }
  function release(id) {
    const data = local.get(id);
    data?.controller?.abort();
    if (data?.url) urls.revokeObjectURL(data.url);
    local.delete(id);
  }
  function remove(id) {
    if (draft.pending) return;
    release(id); draft.attachments = draft.attachments.filter(item => item.id !== id); notify();
  }
  async function retry(id) {
    if (disposed || draft.pending) return;
    const item = draft.attachments.find(item => item.id === id), data = local.get(id);
    if (!item || !data || data.status === "uploading") return;
    if (!data.file) return restore(true);
    const controller = data.controller = new AbortController();
    data.status = "uploading"; data.error = ""; notify();
    const timeout = timer(() => controller.abort(), 60000);
    try {
      const result = await upload({...identity}, operator, item, data.file, controller.signal);
      if (disposed || local.get(id) !== data) return;
      if (result.attachment?.id !== id) throw new Error("图片上传回执不匹配，请重试。");
      Object.assign(item, savedAttachments([{...result.attachment, uploaded:true, expires_at:result.expires_at}])[0]);
      data.status = "ready";
    } catch (error) {
      if (disposed || local.get(id) !== data) return;
      data.status = "failed";
      data.error = error.name === "AbortError" ? "上传中断，请重试" : `上传失败：${error.message}`;
    } finally {
      clearTimer(timeout);
      if (local.get(id) === data) { data.controller = null; notify(); }
    }
  }
  function addFiles(files, limits) {
    if (disposed || draft.pending) return [];
    const errors = [];
    for (const file of files) {
      if (draft.attachments.length >= limits.max_images) { errors.push(`每条消息最多 ${limits.max_images} 张图片。`); break; }
      if (!limits.mime_types.includes(file.type) && !(file.type === "" && /\.(png|jpe?g|webp)$/i.test(file.name))) { errors.push("请选择 PNG、JPEG 或 WebP 图片；HEIC／HEIF 等格式请先另存为支持的格式。"); continue; }
      if (!file.size || file.size > limits.image_bytes) { errors.push(`单张图片须大于 0 且不超过 ${formatBytes(limits.image_bytes)}。`); continue; }
      if (draft.attachments.reduce((sum, item) => sum + item.size, 0) + file.size > limits.total_bytes) {
        errors.push(`每条消息的图片总大小不能超过 ${formatBytes(limits.total_bytes)}。`); continue;
      }
      const item = {id:`img_${randomUUID().replaceAll("-", "")}`, name:file.name, size:file.size, mime_type:file.type, uploaded:false};
      draft.attachments.push(item);
      local.set(item.id, {file, url:urls.createObjectURL(file), status:"new"});
      void retry(item.id);
    }
    notify(); return errors;
  }
  async function restore(force = false) {
    if (disposed || checking) return checking;
    const pending = draft.attachments.filter(item => force || local.get(item.id)?.status === "checking" ||
      local.get(item.id)?.status === "ready" && item.expires_at && Date.parse(item.expires_at) <= now());
    if (!pending.length) return;
    const currentIdentity = identity;
    for (const item of pending) if (local.get(item.id)?.status !== "uploading") local.get(item.id).status = "checking";
    notify();
    checking = (async () => {
      try {
        const result = await states(currentIdentity, operator, pending.map(item => item.id));
        if (disposed || currentIdentity !== identity) return;
        for (const item of pending) {
          const data = local.get(item.id);
          if (!data || data.status !== "checking") continue;
          const found = result.attachments.find(value => value.id === item.id);
          if (found?.status === "available" && found.attachment?.id === item.id) {
            delete item.expires_at;
            Object.assign(item, savedAttachments([{...found.attachment, uploaded:true, expires_at:found.expires_at}])[0]);
            data.status = "ready"; data.error = "";
          } else {
            data.status = data.file ? "failed" : "reselect";
            data.error = found?.status === "expired" ? "图片已过期，请移除或重新选择" : "图片缺失或不可用，请移除或重新选择";
          }
        }
      } catch (error) {
        for (const item of pending) {
          const data = local.get(item.id);
          if (currentIdentity === identity && data?.status === "checking") { data.status = "failed"; data.error = `图片状态未确认：${error.message}`; }
        }
      } finally { checking = null; notify(); if (!disposed && currentIdentity !== identity) void restore(); }
    })();
    return checking;
  }
  function activate(next) {
    if (next.service_epoch !== identity.service_epoch) {
      identity = {...next};
      for (const data of local.values()) if (data.status === "ready") data.status = "checking";
    }
    void restore();
  }
  function clear() {
    for (const id of local.keys()) release(id);
    draft.attachments = []; notify();
  }
  function blockReason(limits) {
    const list = items();
    if (list.some(item => item.status === "uploading")) return "图片正在上传，完成后可发送。";
    if (list.some(item => item.status === "checking")) return "正在确认图片状态，请稍候。";
    if (list.some(item => item.status !== "ready")) return "请重试、移除失败图片，或重新选择图片后再发送。";
    if (limits && (list.length > limits.max_images || list.some(item => item.size > limits.image_bytes) ||
      list.reduce((sum, item) => sum + item.size, 0) > limits.total_bytes)) return "草稿图片超过当前限制，请移除部分图片。";
    return "";
  }
  return {items, addFiles, remove, retry, restore, activate, clear, blockReason,
    dispose() { disposed = true; clearTimer(expiryTimer); for (const id of local.keys()) release(id); }};
}
export const formatBytes = value => `${Number((value / 1048576).toFixed(2))} MiB`;
