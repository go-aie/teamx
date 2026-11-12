import { Marked, Renderer } from "./vendor/marked.mjs";
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
  return options.larkImages ? `<button class="attachment-preview" type="button" aria-label="放大查看图片" title="放大查看图片">${img}</button>` : img;
}

const imagePreviews = new WeakMap();

export function openImagePreview(doc, src, alt) {
  let preview = imagePreviews.get(doc);
  if (!preview) {
    const dialog = doc.createElement("dialog"); dialog.className = "message-image-dialog";
    dialog.setAttribute("aria-label", "图片预览");
    const toolbar = doc.createElement("div"); toolbar.className = "message-image-toolbar";
    const title = doc.createElement("span");
    const close = doc.createElement("button"); close.type = "button"; close.textContent = "关闭";
    close.setAttribute("aria-label", "关闭图片预览");
    toolbar.append(title, close);
    const stage = doc.createElement("div"); stage.className = "message-image-stage";
    const image = doc.createElement("img"); image.referrerPolicy = "no-referrer";
    const error = doc.createElement("p"); error.hidden = true; error.setAttribute("role", "status");
    error.textContent = "图片加载失败，请关闭后重试。";
    stage.append(image, error); dialog.append(toolbar, stage); doc.body.append(dialog);
    close.addEventListener("click", () => dialog.close());
    dialog.addEventListener("cancel", event => { event.preventDefault(); dialog.close(); });
    dialog.addEventListener("click", event => { if (event.target === dialog) dialog.close(); });
    dialog.addEventListener("close", () => { image.removeAttribute("src"); image.alt = ""; error.hidden = true; });
    image.addEventListener("error", () => { if (dialog.open) error.hidden = false; });
    preview = {dialog, image, title, error}; imagePreviews.set(doc, preview);
  }
  preview.title.textContent = alt || "图片预览";
  preview.image.alt = alt || "图片"; preview.error.hidden = true;
  preview.image.src = src;
  if (!preview.dialog.open) preview.dialog.showModal();
}

function makeMarkdown(options = {}) {
  return new Marked({
    gfm: true,
    breaks: true,
    renderer: {
      html({ text }) { return escapeHTML(text); },
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
        return webLink(href, this.parser.parseInline(tokens), title);
      },
      image({ href, text, title }) {
        return imageHTML({ href, text, title }, options);
      },
      table(token) {
        return `<div class="table-wrap">${Renderer.prototype.table.call(this, token)}</div>`;
      },
    },
    extensions: [...(options.compactLarkMentions ? [{
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
  return makeMarkdown(options).parse(options.quotedMarkdown ? notificationMarkdown(text) : text);
}

export function renderMessageBody(container, text, operator, options) {
  // HTML comes solely from the configured renderer, with escaped message HTML
  // and protocol-checked links. Mentions are added to text nodes afterwards.
  container.innerHTML = markdownHTML(text, options);
  const doc = container.ownerDocument;
  if (options) {
    for (const img of container.querySelectorAll("img.message-image")) {
      const preview = img.closest("button.attachment-preview");
      if (preview) preview.addEventListener("click", event => {
        event.preventDefault(); event.stopPropagation();
        openImagePreview(doc, img.src, img.alt);
      });
      img.addEventListener("error", () => {
        const fallback = doc.createElement("button"); fallback.type = "button";
        fallback.textContent = "图片加载失败，点击重试";
        fallback.className = "attachment-error";
        fallback.addEventListener("click", () => openImagePreview(doc, img.src, img.alt));
        if (preview) preview.replaceWith(fallback);
        else img.replaceWith(fallback);
      }, { once: true });
    }
  }
  const walker = doc.createTreeWalker(container, 4 /* SHOW_TEXT */);
  const nodes = [];
  while (walker.nextNode()) {
    const node = walker.currentNode;
    if (!node.parentElement.closest("a, code, pre")) nodes.push(node);
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
      mark.textContent = node.data.slice(range.start, range.end);
      fragment.append(mark);
      end = range.end;
    }
    fragment.append(doc.createTextNode(node.data.slice(end)));
    node.replaceWith(fragment);
  }
}
