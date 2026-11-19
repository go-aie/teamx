import { Marked, Renderer } from "./vendor/marked.mjs";
import { createImageToolIcon, createNavigationIcon } from "./icons.mjs";
import { mentionRanges, memberMentionRanges } from "./client.mjs";

function escapeHTML(text, preserveEntities = false) {
  return text.replace(preserveEntities ? /&(?!(?:#\d+|#x[\da-f]+|\w+);)/gi : /&/g, "&amp;")
    .replace(/</g, "&lt;").replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;").replace(/'/g, "&#39;");
}

// General message links are absolute web links. Authenticated GitLab attachments
// use a separate, strictly constructed same-origin URL below.
function webLink(href, label, title) {
  if (!/^https?:\/\//i.test(href) || /[\u0000-\u0020\u007f]/.test(href)) return label;
  try {
    const url = new URL(href);
    if (!url.hostname) return label;
    const tooltip = title ? ` title="${escapeHTML(title)}"` : "";
    return `<a href="${escapeHTML(url.href)}" target="_blank" rel="noopener noreferrer"${tooltip}>${label}</a>`;
  } catch { return label; }
}

function notificationMarkdown(text) {
  return text.split("\n").map(line => line.startsWith("> ")
    ? "> " + line.slice(2).replace(/\\([\\*_\[\]<>`#\-+!|.:()~])/g, "$1") : line).join("\n");
}

export function notificationOptions(info, operator, source, locator) {
  if (info && source?.provider === "lark" && source.channel === "messages" && (source.message_id?.value || source.reference?.version === 2 && source.reference?.target?.type === "message")) {
    return {
      larkImages: true,
      attachmentURL(href) {
        if (!/^lark-image:img_[A-Za-z0-9_-]{1,252}$/.test(href)) return null;
        const query = new URLSearchParams({ room_id: info.room_id, room_instance: info.room_instance, service_epoch: info.service_epoch, caller_id: operator, ...locator, path: href });
        return `/v1/platform/attachments?${query}`;
      },
    };
  }
  if (!info || source?.provider !== "gitlab" || !["events", "todos"].includes(source.channel)) return undefined;
  return {
    quotedMarkdown: true,
    attachmentURL(href) {
      if (!Number.isSafeInteger(source.project_id) || source.project_id <= 0 || !/^https:\/\//.test(source.instance) || /[\u0000-\u0020\u007f\\]/.test(href)) return null;
      try {
        const origin = new URL(source.instance);
        const url = new URL(href, origin);
        if (origin.origin !== source.instance || url.origin !== origin.origin || url.username || url.password || url.search || url.hash || href.includes("?") || href.includes("#") || href.startsWith("//")) return null;
        const path = decodeURIComponent(url.pathname);
        const prefixes = ["/uploads/", `/-/project/${source.project_id}/uploads/`];
        if (source.project) prefixes.push(`/${source.project}/-/uploads/`, `/${source.project}/uploads/`);
        const prefix = prefixes.find(prefix => path.startsWith(prefix));
        if (!prefix) return null;
        const parts = path.slice(prefix.length).split("/");
        if (parts.length !== 2 || !/^[a-f0-9]{32}$/.test(parts[0]) || !parts[1] || [".", ".."].includes(parts[1]) || /[\\\u0000-\u001f\u007f]/.test(parts[1])) return null;
        const query = new URLSearchParams({ room_id: info.room_id, room_instance: info.room_instance, service_epoch: info.service_epoch, caller_id: operator, ...locator, path: url.href });
        return `/v1/platform/attachments?${query}`;
      } catch { return null; }
    },
  };
}

function imageHTML({ href, text, title, width, height }, options) {
  const src = options.attachmentURL?.(href);
  if (!src) return webLink(href, escapeHTML(text || "图片"), title);
  const size = (width ? ` width="${width}"` : "") + (height ? ` height="${height}"` : "");
  const img = `<img class="message-image" src="${escapeHTML(src)}" alt="${escapeHTML(text || "图片")}"${title ? ` title="${escapeHTML(title)}"` : ""}${size} loading="lazy" decoding="async" referrerpolicy="no-referrer">`;
  return `<button class="attachment-preview" type="button" aria-label="放大查看图片" title="放大查看图片">${img}</button>`;
}

const imagePreviews = new WeakMap();

export function openImagePreview(doc, src, alt) {
  let preview = imagePreviews.get(doc);
  if (!preview) {
    const dialog = doc.createElement("dialog"); dialog.className = "message-image-dialog";
    dialog.setAttribute("aria-label", "图片预览");
    const toolbar = doc.createElement("div"); toolbar.className = "message-image-toolbar";
    toolbar.setAttribute("role", "toolbar"); toolbar.setAttribute("aria-label", "图片操作");
    const controls = doc.createElement("div"); controls.className = "message-image-controls";
    const tool = (action, label) => {
      const button = doc.createElement("button"); button.type = "button";
      button.dataset.imageAction = action;
      button.setAttribute("aria-label", label); button.title = label;
      if (action === "original") button.textContent = "1:1";
      else button.append(createImageToolIcon(doc, action));
      return button;
    };
    const zoomOut = tool("zoomOut", "缩小（−）"), zoomIn = tool("zoomIn", "放大（+）");
    const scale = doc.createElement("output"); scale.className = "message-image-scale";
    scale.setAttribute("aria-label", "缩放比例"); scale.setAttribute("aria-live", "polite");
    const original = tool("original", "原始大小（1）"), fit = tool("fit", "适应窗口（0）"), rotate = tool("rotate", "旋转（R）");
    const divider = doc.createElement("span"); divider.className = "message-image-divider"; divider.setAttribute("aria-hidden", "true");
    const download = doc.createElement("a"); download.className = "message-image-download";
    download.setAttribute("aria-label", "下载图片"); download.title = "下载图片";
    download.download = "image"; download.referrerPolicy = "no-referrer";
    download.append(createImageToolIcon(doc, "download"));
    const close = doc.createElement("button"); close.type = "button";
    close.setAttribute("aria-label", "关闭图片预览"); close.title = "关闭图片预览";
    close.append(createNavigationIcon(doc, "close"));
    controls.append(zoomOut, scale, zoomIn, original, fit, divider, rotate, download);
    toolbar.append(controls, close);
    const stage = doc.createElement("div"); stage.className = "message-image-stage";
    const canvas = doc.createElement("div"); canvas.className = "message-image-canvas";
    const image = doc.createElement("img"); image.referrerPolicy = "no-referrer"; image.draggable = false;
    const error = doc.createElement("p"); error.hidden = true; error.setAttribute("role", "status");
    canvas.append(image); stage.append(canvas, error); dialog.append(toolbar, stage); doc.body.append(dialog);
    let zoom = null, angle = 0, ready = false, drag = null, dragged = false;
    const buttons = [zoomOut, zoomIn, original, fit, rotate];
    function sizeDialog() {
      if (!ready) return;
      const viewportWidth = doc.documentElement.clientWidth, viewportHeight = doc.defaultView.innerHeight;
      const margin = viewportWidth <= 360 ? 8 : viewportWidth <= 640 ? 24 : 32;
      const maxWidth = Math.min(1800, viewportWidth - margin), maxHeight = viewportHeight - margin;
      const rotated = angle % 180 !== 0;
      const width = rotated ? image.naturalHeight : image.naturalWidth, height = rotated ? image.naturalWidth : image.naturalHeight;
      const chromeHeight = toolbar.offsetHeight + 34;
      const fit = Math.min(1, Math.max(1, maxWidth - 34) / width, Math.max(1, maxHeight - chromeHeight) / height);
      // Size the window from the fitted image, independently of manual zoom.
      // Give desktop previews a comfortable minimum canvas, while compact
      // screens retain smaller bounds. Small images still render at most 100%.
      const minWidth = viewportWidth <= 640 ? 440 : 720, minHeight = viewportWidth <= 640 ? 240 : 480;
      dialog.style.width = `${Math.min(maxWidth, Math.max(minWidth, width * fit + 34))}px`;
      dialog.style.height = `${Math.min(maxHeight, Math.max(minHeight, height * fit + chromeHeight))}px`;
    }
    function update() {
      for (const button of buttons) button.disabled = !ready;
      download.hidden = !ready;
      if (!ready) { scale.textContent = "—"; return; }
      const rotated = angle % 180 !== 0;
      const width = rotated ? image.naturalHeight : image.naturalWidth, height = rotated ? image.naturalWidth : image.naturalHeight;
      const availableWidth = Math.max(1, stage.clientWidth - 32), availableHeight = Math.max(1, stage.clientHeight - 32);
      const value = zoom ?? Math.min(1, availableWidth / width, availableHeight / height);
      scale.textContent = `${Math.round(value * 100)}%`;
      canvas.style.width = `${width * value}px`; canvas.style.height = `${height * value}px`;
      image.style.width = `${image.naturalWidth * value}px`;
      image.style.transform = `translate(-50%, -50%) rotate(${angle}deg)`;
      stage.classList.toggle("pannable", width * value > availableWidth || height * value > availableHeight);
      zoomOut.disabled = value <= .05; zoomIn.disabled = value >= 5;
      fit.setAttribute("aria-pressed", String(zoom === null));
      original.setAttribute("aria-pressed", String(zoom === 1));
      return value;
    }
    function resize(value) {
      if (!ready) return;
      zoom = value === null ? null : Math.max(.05, Math.min(5, value)); update();
      stage.scrollLeft = Math.max(0, (stage.scrollWidth - stage.clientWidth) / 2);
      stage.scrollTop = Math.max(0, (stage.scrollHeight - stage.clientHeight) / 2);
    }
    zoomOut.addEventListener("click", () => resize(update() / 1.25));
    zoomIn.addEventListener("click", () => resize(update() * 1.25));
    original.addEventListener("click", () => resize(1)); fit.addEventListener("click", () => resize(null));
    rotate.addEventListener("click", () => { if (ready) { angle = (angle + 90) % 360; sizeDialog(); resize(zoom); } });
    stage.addEventListener("dblclick", () => resize(zoom === 1 ? null : 1));
    dialog.addEventListener("keydown", event => {
      if (event.ctrlKey || event.metaKey || event.altKey) return;
      const action = {"+":zoomIn, "=":zoomIn, "-":zoomOut, "0":fit, "1":original, r:rotate, R:rotate}[event.key];
      if (action && ready) { event.preventDefault(); action.click(); }
    });
    stage.addEventListener("pointerdown", event => {
      if (event.pointerType !== "mouse" || event.button !== 0 || !stage.classList.contains("pannable")) return;
      event.preventDefault(); stage.setPointerCapture(event.pointerId);
      dragged = false;
      drag = {id:event.pointerId, x:event.clientX, y:event.clientY, left:stage.scrollLeft, top:stage.scrollTop};
      stage.classList.add("dragging");
    });
    stage.addEventListener("pointermove", event => {
      if (!drag || event.pointerId !== drag.id) return;
      if (Math.abs(event.clientX - drag.x) + Math.abs(event.clientY - drag.y) > 3) dragged = true;
      stage.scrollLeft = drag.left - event.clientX + drag.x; stage.scrollTop = drag.top - event.clientY + drag.y;
    });
    const stopDrag = () => { drag = null; stage.classList.remove("dragging"); };
    stage.addEventListener("pointerup", stopDrag); stage.addEventListener("pointercancel", stopDrag); stage.addEventListener("lostpointercapture", stopDrag);
    doc.defaultView?.addEventListener("resize", () => { if (dialog.open) { sizeDialog(); update(); } });
    close.addEventListener("click", () => dialog.close());
    dialog.addEventListener("cancel", event => { event.preventDefault(); dialog.close(); });
    dialog.addEventListener("click", event => {
      if (dragged) { dragged = false; return; }
      if (event.target === dialog || event.target === stage) dialog.close();
    });
    dialog.addEventListener("close", () => {
      ready = false; dragged = false; stopDrag(); image.removeAttribute("src"); image.alt = "";
      download.removeAttribute("href"); canvas.hidden = true; error.hidden = true; update();
    });
    image.addEventListener("load", () => {
      if (!dialog.open) return;
      ready = true; canvas.hidden = false; error.hidden = true; sizeDialog(); resize(zoom);
    });
    image.addEventListener("error", () => {
      if (!dialog.open) return;
      ready = false; canvas.hidden = true; error.hidden = false;
      error.textContent = "图片加载失败，请关闭后重试。"; update();
    });
    preview = {dialog, show(src, alt) {
      ready = false; zoom = null; angle = 0;
      dialog.style.width = ""; dialog.style.height = "";
      canvas.hidden = true; error.hidden = false; error.textContent = "正在加载图片…";
      image.alt = alt || "图片"; download.href = src; image.src = src; update();
      if (!dialog.open) dialog.showModal();
    }}; imagePreviews.set(doc, preview);
  }
  preview.show(src, alt);
}

function makeMarkdown(options = {}) {
  return new Marked({
    gfm: true,
    breaks: true,
    renderer: {
      html({ text }) {
        // Only attribute-free formatting tags are accepted from message HTML.
        // Links, images and all other markup still use the guarded renderers.
        return /^<\/?(?:strong|b|em|i|del|s)>$|^<br\s*\/?>$/i.test(text) ? text : escapeHTML(text);
      },
      text(token) {
        return token.tokens ? this.parser.parseInline(token.tokens) : escapeHTML(token.text, true);
      },
      link({ href, tokens, title, raw }) {
        // GFM's bare URL rule keeps Chinese sentence punctuation in the URL.
        // Explicit Markdown links and <autolinks> retain their exact destination.
        if (/^https?:\/\//i.test(raw)) {
          const suffix = raw.match(/[。，！？、；：）》】”’]+$/u)?.[0] || "";
          if (suffix) return webLink(href.slice(0, -suffix.length), escapeHTML(raw.slice(0, -suffix.length), true), title) + escapeHTML(suffix);
        }
        const literal = list => list.map(token=>token.type==="roomMention" ? {type:"text",raw:token.raw,text:options.roomMentionTokens[token.index].raw} : token.tokens ? {...token,tokens:literal(token.tokens)} : token);
        const label = this.parser.parseInline(options.roomMentionTokens ? literal(tokens) : tokens);
        if (options.openCalendar && /^#calendar\/task\/task_[a-f0-9]{32}$/.test(href)) return `<a href="${href}" class="calendar-receipt-link">${label}</a>`;
        return webLink(href, label, title);
      },
      image({ href, text, title }) {
        return imageHTML({ href, text, title }, options);
      },
      table(token) {
        return `<div class="table-wrap">${Renderer.prototype.table.call(this, token)}</div>`;
      },
    },
    extensions: [{
      name: "inlineHTMLCode", level: "inline",
      start(src) { return src.search(/<code>/i); },
      tokenizer(src) {
        const match = /^<code>([\s\S]*?)<\/code>/i.exec(src);
        if (match) return { type: "inlineHTMLCode", raw: match[0], text: match[1] };
      },
      renderer({ text }) { return `<code>${escapeHTML(text, true)}</code>`; },
    }, ...(options.roomMentionTokens ? [{
      name:"roomMention",level:"inline",start:src=>src.indexOf(options.roomMentionPrefix),
      tokenizer(src){
        if(!src.startsWith(options.roomMentionPrefix))return;
        const match=/^(\d+)\uE001/.exec(src.slice(options.roomMentionPrefix.length));
        if(match && options.roomMentionTokens[Number(match[1])])return {type:"roomMention",raw:options.roomMentionPrefix+match[0],index:Number(match[1])};
      },
      renderer(token){const item=options.roomMentionTokens[token.index];return `<mark class="${item.self ? "mention" : "mention-member"}">${escapeHTML("@"+item.label)}</mark>`;}
    }] : []), ...(options.compactLarkMentions ? [{
      name: "larkMention", level: "inline",
      start(src) { return src.indexOf("@"); },
      tokenizer(src) {
        const match = /^@([^@\r\n（）]{1,256})（((?:open_id|user_id|union_id|ID|ID类型未知): [^（）\r\n]+|身份未解析)）/.exec(src);
        if (match) return {type:"larkMention",raw:match[0],name:match[1],identity:match[2]};
      },
      renderer(token) { return `<span class="lark-mention" title="${escapeHTML(token.identity)}">${escapeHTML("@" + token.name)}</span>`; },
    }] : []), ...(options.quotedMarkdown ? [{
      name: "sizedImage", level: "inline",
      start(src) { return src.indexOf("!["); },
      tokenizer(src) {
        if (!src.startsWith("![")) return;
        const token = this.lexer.tokenizer.link(src);
        if (!token || token.type !== "image") return;
        const suffix = src.slice(token.raw.length).match(/^[ \t]*(?:\n[ \t]*)?\{((?:(?:width|height)=\d+[ \t]*){1,2})\}/);
        if (suffix) {
          const attrs = [...suffix[1].matchAll(/(width|height)=(\d+)/g)];
          if (new Set(attrs.map(attr => attr[1])).size === attrs.length && attrs.every(attr => +attr[2] > 0 && +attr[2] <= 8192)) {
            for (const [, name, value] of attrs) token[name] = +value;
            token.raw += suffix[0];
          }
        }
        return { ...token, type: "sizedImage" };
      },
      renderer(token) { return imageHTML(token, options); },
    }] : [])],
  });
}

const markdown = makeMarkdown();

export function markdownHTML(text, options) {
  if (!options) return markdown.parse(text);
  if (options.larkImages && !text.includes("](lark-image:")) text = text.replace(/^image_key: (img_[A-Za-z0-9_-]{1,252})\r?$/gm, "![飞书图片](lark-image:$1)");
  let prefix="\uE000TEAMX_MENTION_";
  while(text.includes(prefix))prefix+="x";
  const tokens=[], parts=[];let end=0;
  for(const at of options.mentions || []){
    if(!Number.isSafeInteger(at.start)||!Number.isSafeInteger(at.end)||at.start<end||at.end<=at.start+1||at.end>text.length||text[at.start]!=="@"||!at.id)continue;
    const raw=text.slice(at.start,at.end),label=options.mentionLabel?.(at) || at.agent_name || raw.slice(1);
    parts.push(text.slice(end,at.start),`${prefix}${tokens.length}\uE001`);
    tokens.push({raw,label,self:at.kind==="human" && at.id===options.operatorID});end=at.end;
  }
  parts.push(text.slice(end));
  const source=parts.join("");
  let html=makeMarkdown({...options,roomMentionPrefix:prefix,roomMentionTokens:tokens.length ? tokens : undefined}).parse(options.quotedMarkdown ? notificationMarkdown(source) : source);
  for(const [index,item] of tokens.entries()){
    const token=`${prefix}${index}\uE001`;
    html=html.replaceAll(token,escapeHTML(item.raw)).replaceAll(encodeURIComponent(token),escapeHTML(encodeURIComponent(item.raw)));
  }
  return html;
}

export function renderMessageBody(container, text, operator, options) {
  // HTML comes solely from the configured renderer, with a small formatting
  // allowlist and protocol-checked links. Mentions are added to text nodes afterwards.
  options={...options,operatorID:operator};
  container.innerHTML = markdownHTML(text, options);
  const doc = container.ownerDocument;
  if (options?.openCalendar) for (const link of container.querySelectorAll("a.calendar-receipt-link")) link.addEventListener("click", event => {
    event.preventDefault(); options.openCalendar(link.getAttribute("href").slice("#calendar/task/".length));
  });
  if (options) {
    for (const img of container.querySelectorAll("img.message-image")) {
      const preview = img.closest("button.attachment-preview");
      if (preview) preview.addEventListener("click", event => {
        event.preventDefault(); event.stopPropagation();
        // Template-cloned messages acquire the live document when inserted.
        openImagePreview(container.ownerDocument, img.src, img.alt);
      });
      img.addEventListener("error", () => {
        const fallback = container.ownerDocument.createElement("button"); fallback.type = "button";
        fallback.textContent = "图片加载失败，点击重试";
        fallback.className = "attachment-error";
        fallback.addEventListener("click", () => openImagePreview(container.ownerDocument, img.src, img.alt));
        if (preview) preview.replaceWith(fallback);
        else img.replaceWith(fallback);
      }, { once: true });
    }
  }
  const walker = doc.createTreeWalker(container, 4 /* SHOW_TEXT */);
  const nodes = [];
  while (walker.nextNode()) {
    const node = walker.currentNode;
    if (!node.parentElement.closest("a, code, pre, mark")) nodes.push(node);
  }
  for (const node of nodes) {
    const ranges = [...mentionRanges(node.data, options?.operatorNames || operator).map(at => ({...at, self:true})),
      ...memberMentionRanges(node.data, options?.memberNames || [])].sort((a,b) => a.start-b.start || b.end-a.end);
    if (!ranges.length) continue;
    const fragment = doc.createDocumentFragment();
    let end = 0;
    for (const range of ranges) {
      if (range.start < end) continue;
      fragment.append(doc.createTextNode(node.data.slice(end, range.start)));
      const mark = doc.createElement("mark");
      mark.className = range.self ? "mention" : "mention-member";
      const label = range.self ? options.mentionLabel?.({ kind: "human", id: operator }) : null;
      mark.textContent = label ? "@" + label : node.data.slice(range.start, range.end);
      fragment.append(mark);
      end = range.end;
    }
    fragment.append(doc.createTextNode(node.data.slice(end)));
    node.replaceWith(fragment);
  }
}
