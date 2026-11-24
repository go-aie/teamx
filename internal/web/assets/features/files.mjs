import { spaceURL } from "../space-context.mjs";
import { markdownHTML, openImagePreview } from "../markdown.mjs";
import { workspaceHeadingID } from "../workspace-file-path.mjs";
import { captureReturnView } from "../view-return.mjs";
import { compactViewport } from "../client.mjs";

const errors = {
  workspace_source_unavailable: "工作目录暂不可用，请核对目录设置后刷新。",
  workspace_source_changed: "工作目录来源已变化，请重新选择来源。",
  file_not_found: "文件已不存在，可返回所在目录查看。",
  file_not_allowed: "无法读取此路径：仅开放当前工作区内获准的普通文件和目录。",
  file_changed: "文件在读取时发生变化，请刷新重试。",
  file_too_large: "文件超过预览上限，可在下载限制内下载查看。",
  image_limit_exceeded: "图片超过大小或像素上限，无法预览。",
  preview_type_unavailable: "此类型不支持预览，可下载后查看。",
  reference_unavailable: "原消息已清空或未记录文件来源，请在文件面板查找。",
  identity: "会话或服务身份已变化，请重新打开文件面板。",
};

const fileNames = new Intl.Collator(undefined, {numeric:true, sensitivity:"base"});
const fileDate = new Intl.DateTimeFormat(undefined, {month:"2-digit", day:"2-digit", hour:"2-digit", minute:"2-digit", hour12:false});
function fileSize(bytes) {
  if (bytes < 1024) return `${bytes} B`;
  const unit = bytes < 1024 ** 2 ? 1024 : 1024 ** 2;
  return `${Number((bytes / unit).toFixed(1))} ${unit === 1024 ? "KB" : "MB"}`;
}
function modifiedDate(value) {
  const date = new Date(value);
  return Number.isFinite(date.getTime()) ? fileDate.format(date) : "—";
}

// One owner for directory/preview state, navigation and cancellable reads.
export function createFiles({ env, runtime, layoutUI, membersUI, conversationToolsUI }) {
  const { document, window, request, fetch: fetchFile, setTimeout: delay, clearTimeout: clearDelay } = env, { $, state } = runtime;
  let opened = false, source = null, sources = [], limits = null;
  let directory = "", listing = null, preview = null, filter = "", sourceMode = false;
  let listScroll = 0, generation = 0, controller = null, identity = null;
  let returnView = null, workspaceSignature = null, hiddenScroll = null;
  let loading = false, error = "", selectionRequired = false;
  let messageSeq = null, legacyMessage = false;
  let messageMembership = null;
  let pathEditing = false, sortOrder = "name", focusedPath = "";
  const directoryViews = new Map();
  let history = [], resources = new Map(), imageQueue = [], imageActive = 0, imageObserver = null, imageClosers = [];
  const blobURL = window.URL || URL;
  function clearResources() {
    imageObserver?.disconnect(); imageObserver = null; imageQueue = [];
    for (const close of imageClosers) close(); imageClosers = [];
    for (const item of resources.values()) if (item.url) blobURL.revokeObjectURL(item.url);
    resources.clear();
  }
  const context = () => JSON.stringify([state.installationID, state.operator, state.catalog?.service_epoch,
    state.info?.room_id, state.info?.room_instance, state.info?.service_epoch]);
  const selected = () => JSON.stringify([messageSeq,source?.membership_id,source?.root_id]);
  function cancel() { generation++; controller?.abort(); controller = null; loading = false; clearResources(); }
  function suspend() { if (opened && (loading || imageActive || imageQueue.length)) { cancel(); error = "读取已暂停，请刷新重试。"; render(); } }
  function begin() {
    cancel(); controller = new AbortController();
    const read = generation, key = context(), membership = selected(), signal = controller.signal;
    return { signal, current: () => opened && !signal.aborted && read === generation && key === context() && membership === selected() };
  }
  function query(path, direct = true) {
    const info = state.info;
    const q = new URLSearchParams({room_id:info.room_id, room_instance:info.room_instance,
      service_epoch:state.catalog.service_epoch, caller_id:state.operator});
    if (direct) {
      if (messageSeq !== null) q.set("message_seq",String(messageSeq));
      else { q.set("membership_id",source.membership_id); q.set("root_id",source.root_id); }
      q.set("path",path);
    }
    return q;
  }
  function groups() {
    const result = [];
    for (const item of sources) {
      const group = item.available && result.find(group => group[0].available && group[0].shared_key === item.shared_key);
      if (group) group.push(item); else result.push([item]);
    }
    return result;
  }
  function renderSources() {
    const select = $("files-source"), grouped = groups(); select.replaceChildren();
    const placeholder = document.createElement("option"); placeholder.value = ""; placeholder.textContent = "请选择工作目录来源";
    select.append(placeholder);
    for (const group of grouped) {
      const item = group.find(item => item.membership_id === source?.membership_id) || group[0];
      const option = document.createElement("option"); option.value = item.membership_id;
      option.textContent = group.map(item => item.label).join("、") + (group.length > 1 ? "（共用目录）" : "") + (!item.available ? "（不可用）" : "");
      option.disabled = !item.available; select.append(option);
    }
    select.value = source?.membership_id || "";
    $("files-source-field").hidden = !!preview || grouped.length < 2 && !selectionRequired;
    select.disabled = loading;
    select.title = select.options[select.selectedIndex]?.textContent || source?.label || "选择工作目录来源";
  }
  function icon(path, className = "file-entry-icon") {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg"), shape = document.createElementNS("http://www.w3.org/2000/svg", "path");
    svg.setAttribute("viewBox", "0 0 24 24"); svg.setAttribute("aria-hidden", "true"); svg.setAttribute("class", className);
    shape.setAttribute("d",path); svg.append(shape); return svg;
  }
  function saveDirectory() {
    if (!listing || preview) return;
    directoryViews.set(directory,{filter,scroll:$("files-body").scrollTop,focusedPath});
    if (directoryViews.size > 100) directoryViews.delete(directoryViews.keys().next().value);
  }
  function focusEntry(path) {
    const entry = [...$("files-body").children].find(item => item.dataset.path === path);
    (entry || $("files-body")).focus({preventScroll:true});
  }
  function closeOptions() {
    $("files-options").hidden = true; $("files-options-toggle").setAttribute("aria-expanded","false");
  }
  function editPath(editing) {
    pathEditing = editing; closeOptions();
    $("files-locate").hidden = !editing; $("files-breadcrumb-row").hidden = editing;
    if (editing) { $("files-locate-path").value = preview?.path || directory; $("files-locate-path").focus(); $("files-locate-path").select?.(); }
    else $("files-path-edit").focus({preventScroll:true});
    runtime.navigationChanged();
  }
  function navigateDirectory(path) {
    closeOptions(); pathEditing = false;
    return messageSeq !== null ? containingDirectory(path) : readDirectory(path,{focus:true});
  }
  function up() { if (directory && source && !preview) return navigateDirectory(directory.split("/").slice(0,-1).join("/")); }
  function renderPath() {
    const nav = $("files-path-segments"), fullPath = preview ? preview.path.split("/").slice(0,-1).join("/") : directory;
    nav.replaceChildren(); $("files-path").title = fullPath || "工作目录";
    $("files-path").setAttribute("aria-label",preview ? "所在目录" : "文件路径");
    $("files-locate").hidden = !pathEditing;
    $("files-breadcrumb-row").hidden = pathEditing;
    $("files-path-edit").hidden = !!preview;
    $("files-path-edit").disabled = !source;
    const directoryButton = $("files-directory"); directoryButton.hidden = !preview; directoryButton.disabled = loading;
    directoryButton.textContent = source ? fullPath.split("/").at(-1) || source.label : "文件";
    directoryButton.title = source ? `打开所在目录：${fullPath || "根目录"}` : "选择当前工作目录来源";
    directoryButton.setAttribute("aria-label",directoryButton.title);
    const root = document.createElement("button"); root.type = "button"; root.className = "files-crumb files-crumb-root";
    root.title = `${source?.label || "工作目录"} · 根目录`; root.setAttribute("aria-label",`返回根目录：${source?.label || "工作目录"}`);
    if (!$("files-source-field").hidden) root.append(icon("m3 10 9-7 9 7M5 9v11h14V9M9 20v-7h6v7",""));
    else root.textContent = source?.label || "工作目录";
    root.disabled = !source || !fullPath || loading;
    if (!fullPath && !preview) root.setAttribute("aria-current","page");
    root.addEventListener("click",()=>navigateDirectory("")); if (fullPath || !preview) nav.append(root);
    const segments = fullPath.split("/").filter(Boolean);
    const separator = () => { const span = document.createElement("span"); span.className = "files-crumb-separator"; span.textContent = "/"; span.setAttribute("aria-hidden","true"); nav.append(span); };
    const crumb = (index, current = false) => {
      const item = document.createElement(current ? "span" : "button"); item.className = "files-crumb"; item.textContent = segments[index];
      const path = segments.slice(0,index+1).join("/"); item.title = path;
      if (current) item.setAttribute("aria-current","page");
      else { item.type = "button"; item.disabled = !source || loading; item.addEventListener("click",()=>navigateDirectory(path)); }
      return item;
    };
    if (segments.length > 2) {
      separator(); const overflow = document.createElement("details"), summary = document.createElement("summary"), menu = document.createElement("div");
      overflow.className = "files-crumb-overflow"; summary.textContent = "…"; summary.title = "展开上级路径"; summary.setAttribute("aria-label","展开上级路径");
      menu.className = "files-crumb-menu menu-surface";
      for (let i=0;i<segments.length-2;i++) menu.append(crumb(i));
      overflow.append(summary,menu); nav.append(overflow);
    }
    for (let i=Math.max(0,segments.length-2);i<segments.length;i++) {
      separator(); if (!preview || i < segments.length-1) nav.append(crumb(i,i === segments.length-1));
    }
  }
  function renderListing(body) {
    const visible = listing.entries.filter(item => item.name.toLocaleLowerCase().includes(filter.toLocaleLowerCase())).sort((a,b) =>
      Number(b.kind === "directory") - Number(a.kind === "directory") ||
      (sortOrder === "modified" ? (Date.parse(b.mtime) || 0) - (Date.parse(a.mtime) || 0) : 0) || fileNames.compare(a.name,b.name));
    for (const entry of visible) {
      const button = document.createElement("button"); button.type = "button"; button.className = "file-entry";
      const folder = entry.kind === "directory";
      const shape = folder ? "M3 7V5h6l2 2h10v13H3ZM3 10h18" : /\.(png|jpe?g|webp|gif|svg)$/i.test(entry.name) ? "M14 3H5v18h14V8Zm0 0v5h5M7 17l3-4 3 3 2-2 2 3M8 9h.01" : "M14 3H5v18h14V8Zm0 0v5h5M8 12h8M8 16h6";
      const name = document.createElement("span"), modified = document.createElement("span"), size = document.createElement("span");
      name.className = "file-entry-name"; name.textContent = entry.name;
      modified.className = "file-entry-modified"; modified.textContent = modifiedDate(entry.mtime);
      size.className = "file-entry-size"; size.textContent = folder ? "—" : fileSize(entry.size || 0);
      button.append(icon(shape),name,modified,size); button.dataset.path = entry.path; button.dataset.kind = entry.kind;
      button.title = `${entry.name}\n${folder ? "文件夹" : fileSize(entry.size || 0)} · ${modifiedDate(entry.mtime)}`;
      button.setAttribute("aria-label",`${folder ? "文件夹" : "文件"}：${entry.name}`);
      button.addEventListener("focus",()=>{focusedPath = entry.path;});
      button.addEventListener("click", () => { focusedPath = entry.path; return folder ? navigateDirectory(entry.path) : readPreview(entry.path); }); body.append(button);
    }
    const folders = visible.filter(item=>item.kind === "directory").length;
    $("files-count").textContent = filter ? `${visible.length} / ${listing.entries.length} 项${listing.truncated ? "（列表不完整）" : ""}` : `${folders} 个文件夹 · ${visible.length-folders} 个文件${listing.truncated ? "（列表不完整）" : ""}`;
    if (!visible.length) { const empty = document.createElement("p"); empty.className = "files-empty"; empty.textContent = filter ? listing.truncated ? "已返回的条目中没有匹配项，可输入相对路径定位。" : "当前目录没有匹配项，试试其他名称。" : "此目录没有可展示的文件。"; body.append(empty); }
  }
  function render() {
    $("files-page").hidden = !opened;
    if (!opened) return;
    $("files-page").classList.toggle("files-preview",!!preview);
    renderSources();
    $("files-title").textContent = preview ? preview.name : "文件";
    $("files-title").title = preview?.path || "文件";
    $("files-back").hidden = !preview;
    const backLabel = history.length ? "返回上个文档" : messageSeq !== null ? "返回会话" : "返回列表";
    $("files-back").setAttribute("aria-label",backLabel); $("files-back").title = backLabel;
    renderPath();
    $("files-list-tools").hidden = !!preview;
    $("files-up").disabled = !directory || !source || loading;
    $("files-list-heading").hidden = !!preview || !listing;
    $("files-count").hidden = !!preview || !listing;
    $("files-sort").value = sortOrder;
    $("files-sort-mobile").value = sortOrder;
    const markdown = preview?.preview_kind === "markdown";
    $("files-view-mode").hidden = !markdown;
    $("files-source-toggle").hidden = !markdown;
    $("files-preview-mode").setAttribute("aria-pressed",String(!sourceMode));
    const download = $("files-download");
    download.hidden = !source || !preview?.download_available;
    if (!download.hidden) { const q = query(preview.path); q.set("download","true"); download.href = spaceURL("/v1/workspace/files/content?"+q); download.download = preview.name; }
    else download.removeAttribute("href");
    $("files-source-toggle").setAttribute("aria-pressed", String(sourceMode));
    $("files-refresh").disabled = !state.managed || !state.info;
    $("files-error").textContent = error; $("files-error").hidden = !error;
    const hasMetadata = preview && !["pending","failed"].includes(preview.preview_kind);
    $("files-status").textContent = loading ? "正在读取…" : hasMetadata ? `${fileSize(preview.size)} · ${modifiedDate(preview.mtime)}` : "";
    $("files-status").title = hasMetadata ? `当前磁盘文件内容 · ${preview.size.toLocaleString()} 字节 · ${new Date(preview.mtime).toLocaleString()}` : "";
    $("files-meta").hidden = !loading && !hasMetadata;
    $("files-truncated").hidden = !listing?.truncated || !!preview;
    const body = $("files-body"), position = {top:body.scrollTop,left:body.scrollLeft}; body.replaceChildren();
    body.setAttribute("aria-busy",String(loading)); body.setAttribute("aria-label",preview ? `文件预览：${preview.name}` : "目录内容");
    if (preview) {
      if (preview.preview_kind === "markdown" && !sourceMode) {
        const content = document.createElement("div"); content.className = "message-text files-markdown";
        content.innerHTML = markdownHTML(preview.text || "", {workspaceDocument:{path:preview.path}}); body.append(content);
        bindDocument(content);
      } else if (["text","markdown"].includes(preview.preview_kind)) {
        const pre = document.createElement("pre"); pre.className = "files-text"; pre.textContent = preview.text || ""; body.append(pre);
      } else if (preview.preview_kind === "image") {
        const image = document.createElement("button"); image.type = "button"; image.className = "workspace-image-load";
        image.dataset.filePath = preview.path; image.dataset.imageAlt = preview.name; image.textContent = "加载图片";
        body.append(image); bindImage(image,true);
      } else if (!["pending","failed"].includes(preview.preview_kind)) {
        const message = document.createElement("p"); message.className = "quiet";
        message.textContent = preview.reason === "file_too_large" && limits?.text_bytes ? `文件超过文本预览上限（${limits.text_bytes.toLocaleString()} 字节）。` : errors[preview.reason] || "此类型不支持预览。"; body.append(message);
        if (!preview.download_available && limits?.download_bytes && preview.size > limits.download_bytes) {
          const hint = document.createElement("p"); hint.className = "quiet"; hint.textContent = `文件超过下载上限（${limits.download_bytes.toLocaleString()} 字节）。`; body.append(hint);
        }
      }
    } else if (listing) {
      renderListing(body);
    }
    body.scrollTop = position.top; body.scrollLeft = position.left;
    runtime.navigationChanged();
  }
  function remember() {
    if (preview) { history.push({path:preview.path,scroll:$("files-body").scrollTop,sourceMode}); if (history.length > 100) history.shift(); }
  }
  function scrollToFragment(fragment) {
    if (!fragment) { $("files-body").scrollTop = 0; return; }
    const target = [...$("files-body").querySelectorAll("[data-heading-id]")].find(el => el.dataset.headingId === fragment || el.dataset.headingId === workspaceHeadingID(fragment));
    if (target) target.scrollIntoView({block:"start"});
    else { error = "未找到文档中的锚点。"; $("files-error").textContent = error; $("files-error").hidden = false; }
  }
  function bindDocument(content) {
    const seen = new Map();
    for (const heading of content.querySelectorAll("h1,h2,h3,h4,h5,h6")) {
      const id = workspaceHeadingID(heading.textContent), count = seen.get(id) || 0;
      seen.set(id,count+1); heading.dataset.headingId = id + (count ? `-${count}` : "");
    }
    for (const link of content.querySelectorAll("button.workspace-file-link")) link.addEventListener("click",() => {
      remember();
      if (link.dataset.filePath === preview.path) scrollToFragment(link.dataset.fileFragment);
      else void readPreview(link.dataset.filePath,{fragment:link.dataset.fileFragment});
      runtime.navigationChanged();
    });
    imageObserver?.disconnect();
    if (window.IntersectionObserver) imageObserver = new window.IntersectionObserver(entries => {
      for (const entry of entries) if (entry.isIntersecting) { imageObserver.unobserve(entry.target); loadImage(entry.target.dataset.filePath); }
    },{root:$("files-body"),rootMargin:"200px"});
    let count = 0;
    for (const button of content.querySelectorAll("button.workspace-image-load")) bindImage(button, count++ < (limits?.auto_images || 0));
  }
  function showResource(button,item) {
    button.replaceChildren();
    if (item.url) {
      const image = document.createElement("img"); image.src = item.url; image.alt = button.dataset.imageAlt; image.className = "message-image";
      image.loading = "lazy"; button.classList.add("attachment-preview"); button.append(image);
      button.setAttribute("aria-label","放大查看图片");
    } else button.textContent = item.error || (item.loading ? "正在加载图片…" : `加载图片：${button.dataset.imageAlt}`);
  }
  function bindImage(button,automatic) {
    const path = button.dataset.filePath, item = resources.get(path);
    if (item) showResource(button,item);
    button.addEventListener("click",() => {
      const resource = resources.get(path);
      if (resource?.url) imageClosers.push(openImagePreview(document,resource.url,button.dataset.imageAlt));
      else loadImage(path,true);
    });
    if (automatic && !item && !loading) {
      if (imageObserver) imageObserver.observe(button); else loadImage(path);
    }
  }
  function loadImage(path,retry = false) {
    if (!controller || controller.signal.aborted || state.suspended || document.hidden) return;
    const existing = resources.get(path);
    if (existing && (!retry || existing.loading || existing.url)) return;
    const read = generation, key = context(), signal = controller.signal;
    const current = () => opened && !signal.aborted && read === generation && key === context();
    const item = {loading:true}; resources.set(path,item);
    const update = () => { if (current()) for (const button of $("files-body").querySelectorAll("button.workspace-image-load")) if (button.dataset.filePath === path) showResource(button,item); };
    update();
    imageQueue.push(async () => {
      if (!current()) return;
      const local = new AbortController(), timer = delay(() => local.abort(),10000);
      const abort = () => local.abort(); signal.addEventListener("abort",abort,{once:true});
      try {
        const response = await fetchFile("/v1/workspace/files/content?"+query(path),{signal:local.signal,cache:"no-store"});
        if (!response.ok) { const body = await response.json(); throw Object.assign(Error(body.error),{code:body.code}); }
        const blob = await response.blob();
        if (!current()) return;
        if (!/^image\/(png|jpeg|webp)$/.test(blob.type) || blob.size > limits.image_bytes) throw Error("图片类型或大小不符合预览限制。");
        item.url = blobURL.createObjectURL(blob);
      } catch (err) {
        if (!current()) return;
        if (["workspace_source_changed","workspace_source_unavailable","reference_unavailable","identity"].includes(err.code)) {
          await failure(err,{current});
          if (current()) { cancel(); preview = preview ? {path:preview.path,name:preview.name,preview_kind:"failed"} : null; render(); }
          return;
        }
        item.error = errors[err.code] || "图片加载失败，点击重试。";
      } finally { clearDelay(timer); signal.removeEventListener("abort",abort); item.loading = false; update(); }
    });
    pumpImages();
  }
  function pumpImages() {
    while (imageQueue.length && imageActive < (limits?.read_concurrency || 1)) {
      imageActive++; const task = imageQueue.shift();
      void task().finally(() => { imageActive--; pumpImages(); });
    }
  }
  async function failure(err, read) {
    if (!read.current()) return;
    error = errors[err.code] || err.message || "读取失败，请刷新重试。";
    if (messageSeq !== null) {
      if (["workspace_source_changed","workspace_source_unavailable","reference_unavailable"].includes(err.code)) source = null;
      cancel(); history = [];
      if (preview) preview = {...preview,preview_kind:"failed",text:undefined,download_available:false};
      loading = false; render(); return;
    }
    if (err.code === "workspace_source_changed" || err.code === "workspace_source_unavailable") {
      cancel(); history = []; directoryViews.clear(); pathEditing = false; source = null; listing = preview = null; selectionRequired = true;
      await readSources({ choose:false, keepError:true });
    }
  }
  async function readSources({choose = true, keepError = false} = {}) {
    if (!opened || state.suspended || document.hidden) return;
    const read = begin(); loading = true; if (!keepError) error = ""; render();
    try {
      const data = await request("/v1/workspace/files/sources?"+query("",false), undefined, undefined, 10000, read.signal);
      if (!read.current()) return;
      sources = data.sources || []; limits = data.limits;
      if (limits?.path_bytes) $("files-locate-path").maxLength = limits.path_bytes;
      const previous = source;
      if (previous && !sources.some(item => item.membership_id === previous.membership_id && item.root_id === previous.root_id && item.available)) {
        source = null; listing = preview = null; directoryViews.clear(); pathEditing = false; selectionRequired = true; error = errors.workspace_source_changed;
      } else if (previous) source = sources.find(item => item.membership_id === previous.membership_id);
      if (!source && choose && !selectionRequired) {
        const preferred = membersUI.selectedAgent()?.membership_id;
        source = sources.find(item => item.membership_id === preferred && item.available) || sources.find(item => item.available);
      }
      if (!source && !error) error = errors.workspace_source_unavailable;
      loading = false; render();
      if (choose && source && !listing && !preview) await readDirectory("");
    } catch (err) { if (read.current()) { loading = false; error = errors[err.code] || err.message; render(); } }
  }
  async function readDirectory(path, {focus = false, focusPath = null} = {}) {
    if (!source || state.suspended || document.hidden) return;
    saveDirectory();
    const saved = directoryViews.get(path);
    history = []; const read = begin(); preview = null; listing = null; directory = path; loading = true; error = "";
    listScroll = saved?.scroll || 0; filter = saved?.filter || ""; focusedPath = focusPath ?? saved?.focusedPath ?? "";
    $("files-filter").value = filter;
    $("files-locate-path").value = path; render();
    try {
      const q = query(path); q.set("show_hidden",String($("files-hidden").checked));
      const data = await request("/v1/workspace/files?"+q, undefined, undefined, 10000, read.signal);
      if (!read.current()) return;
      listing = data; directory = data.path || ""; loading = false; render(); $("files-body").scrollTop = listScroll;
      if (focus) focusEntry(focusedPath);
    } catch (err) { await failure(err,read); if (read.current()) { loading = false; render(); } }
  }
  async function readPreview(path, {refresh = false, fragment = "", restore = null} = {}) {
    if ((!source && messageSeq === null) || state.suspended || document.hidden) return;
    if (legacyMessage) { error = "旧消息未记录文件来源，请在文件面板查找。"; render(); return; }
    if (!preview) { saveDirectory(); listScroll = $("files-body").scrollTop; }
    closeOptions(); pathEditing = false;
    const read = begin(); loading = true; error = "";
    if (!refresh) sourceMode = false;
    preview = {path,name:path.split("/").at(-1),preview_kind:"pending",size:0,mtime:new Date().toISOString()}; render();
    try {
      const data = await request("/v1/workspace/files/preview?"+query(path), undefined, undefined, 10000, read.signal);
      if (!read.current()) return;
      if (messageSeq !== null && data.source) source = {...data.source};
      limits = data.limits || limits;
      preview = data; loading = false;
      if (restore) sourceMode = restore.sourceMode;
      render(); $("files-body").scrollTop = restore?.scroll || 0;
      if (fragment) scrollToFragment(fragment);
    } catch (err) { await failure(err,read); if (read.current()) { if (preview) preview.preview_kind = "failed"; loading = false; render(); } }
  }
  function returnFromPreview() {
    if (history.length) {
      const previous = history.pop();
      if (preview?.path === previous.path && !["pending","failed"].includes(preview.preview_kind)) {
        sourceMode = previous.sourceMode; render(); $("files-body").scrollTop = previous.scroll;
      } else void readPreview(previous.path,{restore:previous});
      runtime.navigationChanged(); return;
    }
    if (messageSeq !== null) { close(); return; }
    cancel();
    if (preview) {
      const path = preview.path; preview = null; error = ""; render(); $("files-body").scrollTop = listScroll;
      if (!listing) void readDirectory(directory,{focus:true,focusPath:path});
      else focusEntry(focusedPath);
    }
  }
  function back() {
    if (pathEditing) { editPath(false); return; }
    if (!$("files-options").hidden) { closeOptions(); $("files-options-toggle").focus(); return; }
    const overflow = $("files-path").querySelectorAll("details[open]")[0];
    if (overflow) { overflow.open = false; overflow.firstChild.focus(); return; }
    if (layoutUI.auxiliaryExpanded) { layoutUI.toggleAuxiliaryExpanded(); return; }
    if (preview) return returnFromPreview();
    if (directory) void up();
    else close();
  }
  function hide() {
    if (!opened) return;
    hiddenScroll = {top:$("files-body").scrollTop,left:$("files-body").scrollLeft};
    suspend(); closeOptions(); opened = false; $("files-page").hidden = true;
  }
  function close({focus = true} = {}) {
    if (!opened && !identity) return;
    cancel(); closeOptions(); directoryViews.clear(); history = []; pathEditing = false; opened = false; identity = null; hiddenScroll = null; source = null; sources = []; limits = null; listing = preview = null;
    messageSeq = null; legacyMessage = false;
    messageMembership = null;
    $("files-body").replaceChildren(); $("files-page").hidden = true;
    layoutUI.closeAuxiliary("files");
    if (focus) returnView?.restore($("room-menu-label")); returnView = null;
    runtime.navigationChanged();
  }
  function enter(opener) {
    conversationToolsUI.close(); cancel(); closeOptions(); history = []; directoryViews.clear(); pathEditing = false; focusedPath = ""; sortOrder = "name";
    if (!opened) returnView = captureReturnView(env,runtime,{opener,restoreScroll:false});
    opened = true; hiddenScroll = null; source = null; listing = preview = null; directory = filter = error = ""; selectionRequired = false;
    messageSeq = null; legacyMessage = false; sources = []; limits = null;
    messageMembership = null;
    $("files-filter").value = ""; $("files-locate-path").value = ""; identity = context(); workspaceSignature = null;
    layoutUI.openAuxiliary("files"); render(); $("files-close").focus({preventScroll:true});
  }
  async function open(opener = $("room-menu-label")) {
    if (opened) return;
    if (identity === context()) {
      conversationToolsUI.close(); returnView = captureReturnView(env,runtime,{opener,restoreScroll:false}); opened = true; layoutUI.openAuxiliary("files"); render();
      if (hiddenScroll) { $("files-body").scrollTop = hiddenScroll.top; $("files-body").scrollLeft = hiddenScroll.left; }
      $("files-close").focus({preventScroll:true});
      if (messageSeq === null && state.managed && state.info) await readSources({choose:!source && !selectionRequired});
      if (!opened) return;
      if (messageSeq !== null && preview && source && !legacyMessage) await readPreview(preview.path,{refresh:true,restore:{sourceMode,scroll:hiddenScroll?.top || 0}});
      else if (preview?.preview_kind === "pending") await readPreview(preview.path);
      else if (!listing && !preview && source) await readDirectory(directory);
      else if (!sources.length && !messageSeq) await readSources();
      return;
    }
    enter(opener);
    if (!state.managed || !state.info) { error = "此会话缺少可信工作目录配置，暂不支持文件浏览。"; render(); return; }
    await readSources();
  }
  async function openMessage(message,path,opener) {
    enter(opener);
    messageSeq = message.seq; legacyMessage = !/^wr1_[a-f0-9]{64}$/.test(message.workspace_root_id || "");
    messageMembership = message.author_membership_id;
    directory = path.split("/").slice(0,-1).join("/");
    if (!legacyMessage) source = {membership_id:message.author_membership_id,root_id:message.workspace_root_id,label:message.agent_name || message.author.id};
    preview = {path,name:path.split("/").at(-1),preview_kind:"failed"};
    if (!state.managed || !state.info) { error = "此会话缺少可信工作目录配置，暂不支持文件浏览。"; render(); return; }
    await readPreview(path);
  }
  async function containingDirectory(target) {
    const path = preview?.path || "", previous = source;
    cancel(); history = []; messageSeq = null; legacyMessage = false; messageMembership = null; workspaceSignature = null; listing = preview = null;
    directory = typeof target === "string" ? target : path.split("/").slice(0,-1).join("/"); error = "";
    // This explicit action switches to current-workspace browsing. An invalid
    // reference cannot silently choose a replacement source for the old link.
    const task = readSources({choose:!previous}), read = generation;
    await task;
    if (previous && opened && read === generation && source) await readDirectory(directory,{focus:true});
  }
  function observeMessages() {
    if (!identity || messageSeq === null || messageSeq > state.clearedThrough) return;
    cancel(); history = []; source = null;
    if (preview) preview = {path:preview.path,name:preview.name,preview_kind:"failed"};
    error = errors.reference_unavailable; render();
  }

  function syncIdentity() { if (identity && identity !== context()) close({focus:false}); }
  function observeWorkspace(data) {
    syncIdentity(); if (!identity) return;
    const members = messageSeq !== null ? data.members?.filter(item=>item.membership_id === messageMembership) : data.members;
    const signature = JSON.stringify([members?.map(item => [item.membership_id,item.effective_directory]),messageSeq === null ? data.operation?.status : null]);
    if (signature === workspaceSignature) return;
    const changed = workspaceSignature !== null; workspaceSignature = signature;
    if (changed && !opened) {
      cancel(); history = []; directoryViews.clear(); source = null; listing = preview = null; selectionRequired = true;
      sources = []; error = errors.workspace_source_changed;
    } else if (changed && !state.suspended) {
      if (messageSeq !== null) {
        cancel(); history = []; source = null;
        if (preview) preview = {path:preview.path,name:preview.name,preview_kind:"failed"};
        error = errors.workspace_source_changed; render();
      } else void readSources({choose:false});
    }
  }
  function mount() {
    document.addEventListener("pointerdown", event => {
      if (!$("files-options").contains(event.target) && !$("files-options-toggle").contains(event.target)) closeOptions();
      for (const details of $("files-path").querySelectorAll("details[open]")) if (!details.contains(event.target)) details.open = false;
    });
    document.addEventListener("keydown", event => {
      if (event.key !== "Escape" || event.defaultPrevented || document.querySelector("dialog:modal")) return;
      if (!$("conversation-menu").hidden) return;
      if (opened) { event.preventDefault(); back(); }
    });
    $("files-close").addEventListener("click",() => close()); $("files-back").addEventListener("click",returnFromPreview);
    $("files-directory").addEventListener("click",()=>navigateDirectory(preview?.path.split("/").slice(0,-1).join("/") || ""));
    $("files-up").addEventListener("click",up);
    $("files-path-edit").addEventListener("click",()=>editPath(true));
    $("files-path-cancel").addEventListener("click",()=>editPath(false));
    $("files-options-toggle").addEventListener("click",()=>{
      const options = $("files-options"); options.hidden = !options.hidden;
      $("files-options-toggle").setAttribute("aria-expanded",String(!options.hidden));
      if (!options.hidden) $(compactViewport(window) ? "files-sort-mobile" : "files-hidden").focus();
    });
    for (const id of ["files-sort","files-sort-mobile"]) $(id).addEventListener("change",()=>{
      sortOrder = $(id).value;
      if (id === "files-sort-mobile") { closeOptions(); $("files-options-toggle").focus(); }
      render();
    });
    $("files-body").addEventListener("keydown",event=>{
      if (preview || loading) return;
      if (event.key === "ArrowLeft" || event.key === "ArrowUp" && event.altKey) { event.preventDefault(); void up(); return; }
      const entries = [...$("files-body").querySelectorAll("button.file-entry")], index = entries.indexOf(document.activeElement);
      const next = {ArrowDown:Math.min(index+1,entries.length-1), ArrowUp:Math.max(index-1,0), Home:0, End:entries.length-1}[event.key];
      if (next !== undefined && entries[next]) { event.preventDefault(); focusedPath = entries[next].dataset.path; entries[next].focus(); entries[next].scrollIntoView({block:"nearest"}); }
      else if (event.key === "ArrowRight" && entries[index]?.dataset.kind === "directory") { event.preventDefault(); focusedPath = entries[index].dataset.path; void navigateDirectory(focusedPath); }
    });
    $("files-expand").addEventListener("click",layoutUI.toggleAuxiliaryExpanded);
    $("files-refresh").addEventListener("click",() => messageSeq !== null && preview ? readPreview(preview.path,{refresh:true}) : source ? preview ? readPreview(preview.path,{refresh:true}) : readDirectory(directory) : readSources({choose:false}));
    const showSource = value => { if (preview?.preview_kind === "markdown" && sourceMode !== value) { begin(); sourceMode = value; render(); } };
    $("files-source-toggle").addEventListener("click",() => showSource(true));
    $("files-preview-mode").addEventListener("click",() => showSource(false));
    $("files-filter").addEventListener("input",() => { filter = $("files-filter").value; render(); });
    $("files-hidden").addEventListener("change",() => { closeOptions(); $("files-options-toggle").focus(); return readDirectory(directory); });
    $("files-source").addEventListener("change",() => {
      cancel(); closeOptions(); directoryViews.clear(); focusedPath = ""; source = sources.find(item => item.membership_id === $("files-source").value && item.available) || null;
      selectionRequired = false; listing = preview = null; directory = ""; if (source) void readDirectory(""); else render();
    });
    $("files-locate").addEventListener("submit",async event => {
      event.preventDefault(); const input = $("files-locate-path").value, path = input.replace(/\/+$/, "");
      if (!source || messageSeq !== null) return;
      // Keep failed input editable; only a successful navigation closes it.
      saveDirectory(); const read = begin(); loading = true; error = ""; render();
      try {
        const q = query(path); q.set("show_hidden",String($("files-hidden").checked));
        const data = await request("/v1/workspace/files?"+q,undefined,undefined,10000,read.signal);
        if (!read.current()) return;
        directory = data.path || ""; listing = data; preview = null; history = []; pathEditing = false;
        const saved = directoryViews.get(directory); filter = saved?.filter || ""; listScroll = saved?.scroll || 0; focusedPath = saved?.focusedPath || "";
        $("files-filter").value = filter; loading = false; render(); $("files-body").scrollTop = listScroll; focusEntry(focusedPath);
      } catch (err) {
        if (!read.current()) return;
        if (err.code === "file_not_allowed" && path && !input.endsWith("/")) {
          directory = path.split("/").slice(0,-1).join("/"); listing = null; history = []; focusedPath = path; await readPreview(path);
        } else { await failure(err,read); if (read.current()) { loading = false; render(); } }
      }
    });
  }
  return {mount,open,openMessage,close,hide,back,suspend,syncIdentity,observeWorkspace,observeMessages,
    get isOpen(){return opened;},
    get navigationID(){return `files-${history.length}-${layoutUI.auxiliaryExpanded ? "expanded" : preview ? "preview" : directory ? "directory" : "root"}`;}};
}
