import { Marked, Renderer } from "./vendor/marked.mjs";
import { mentionRanges } from "./client.mjs";

function escapeHTML(text, preserveEntities = false) {
  return text.replace(preserveEntities ? /&(?!(?:#\d+|#x[\da-f]+|\w+);)/gi : /&/g, "&amp;")
    .replace(/</g, "&lt;").replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;").replace(/'/g, "&#39;");
}

// Only absolute web links are navigable. Never interpret message HTML or fetch
// embedded images; image syntax becomes an explicit link instead.
function webLink(href, label, title) {
  if (!/^https?:\/\//i.test(href) || /[\u0000-\u0020\u007f]/.test(href)) return label;
  try {
    const url = new URL(href);
    if (!url.hostname) return label;
    const tooltip = title ? ` title="${escapeHTML(title)}"` : "";
    return `<a href="${escapeHTML(url.href)}" target="_blank" rel="noopener noreferrer"${tooltip}>${label}</a>`;
  } catch { return label; }
}

const markdown = new Marked({
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
      return webLink(href, escapeHTML(text || "图片"), title);
    },
    table(token) {
      return `<div class="table-wrap">${Renderer.prototype.table.call(this, token)}</div>`;
    },
  },
});

export function markdownHTML(text) {
  return markdown.parse(text);
}

export function renderMessageBody(container, text, operator) {
  // HTML comes solely from the configured renderer, with escaped message HTML
  // and protocol-checked links. Mentions are added to text nodes afterwards.
  container.innerHTML = markdownHTML(text);
  const doc = container.ownerDocument;
  const walker = doc.createTreeWalker(container, 4 /* SHOW_TEXT */);
  const nodes = [];
  while (walker.nextNode()) {
    const node = walker.currentNode;
    if (!node.parentElement.closest("a, code, pre")) nodes.push(node);
  }
  for (const node of nodes) {
    const ranges = mentionRanges(node.data, operator);
    if (!ranges.length) continue;
    const fragment = doc.createDocumentFragment();
    let end = 0;
    for (const range of ranges) {
      fragment.append(doc.createTextNode(node.data.slice(end, range.start)));
      const mark = doc.createElement("mark");
      mark.className = "mention";
      mark.textContent = node.data.slice(range.start, range.end);
      fragment.append(mark);
      end = range.end;
    }
    fragment.append(doc.createTextNode(node.data.slice(end)));
    node.replaceWith(fragment);
  }
}
