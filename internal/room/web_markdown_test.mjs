import { test } from "node:test";
import assert from "node:assert/strict";
import { markdownHTML, notificationOptions } from "./web/markdown.mjs";

test("messages render Markdown structure and preserve ordinary line breaks", () => {
  const html = markdownHTML("## 方案\n\n**粗体**、*强调*、~~删除~~、`todo.py`\n下一行\n\n> 引用\n\n1. 添加\n2. 完成\n\n- [x] 已完成\n- [ ] 待处理\n\n---");
  for (const part of ["<h2>方案</h2>", "<strong>粗体</strong>", "<em>强调</em>", "<del>删除</del>", "<code>todo.py</code>", "<br>下一行", "<blockquote>", "<ol>", "<li>添加</li>", "<hr>", 'disabled=""']) {
    assert.ok(html.includes(part), part);
  }
  assert.equal(markdownHTML("a &amp; b & c"), "<p>a &amp; b &amp; c</p>\n");
});

test("bare and Markdown web links open safely without swallowing sentence punctuation", () => {
  const html = markdownHTML("https://example.com/a?q=1&b=2。 http://example.org/test。\n[合并请求](https://example.com/mr/2 \"查看 MR\")\n<https://example.com/explicit>");
  for (const url of ["https://example.com/a?q=1&amp;b=2", "http://example.org/test", "https://example.com/mr/2", "https://example.com/explicit"]) {
    assert.ok(html.includes(`href="${url}" target="_blank" rel="noopener noreferrer"`), url);
  }
  assert.ok(html.includes("</a>。"));
  assert.ok(html.includes('title="查看 MR"'));
  assert.ok(!html.includes("%E3%80%82"));
  assert.ok(markdownHTML("https://example.com/a_(b).").includes('href="https://example.com/a_(b)"'));
});

test("code keeps URLs and markup literal; tables scroll within the message", () => {
  const html = markdownHTML("`https://example.com @ann`\n\n```html\n<script>alert(1)</script>\nhttps://example.com\n```\n\n| 名称 | 状态 |\n| :--- | ---: |\n| **任务** | 完成 |");
  assert.ok(!html.includes("<a "));
  assert.ok(html.includes("&lt;script&gt;alert(1)&lt;/script&gt;"));
  assert.ok(html.includes('<pre><code class="language-html">'));
  assert.ok(html.includes('<div class="table-wrap"><table>'));
  assert.ok(html.includes('<th align="right">状态</th>'));
  assert.ok(html.includes("<strong>任务</strong>"));
});

test("untrusted messages cannot inject HTML, executable links or fetched images", () => {
  const html = markdownHTML([
    '<script>alert("x")</script>',
    '<img src=x onerror=alert(1)>',
    '<iframe src="https://example.com"></iframe>',
    '[run](javascript:alert%281%29)',
    '[entity](java&#x73;cript:alert%281%29)',
    '[data](data:text/html,test)',
    '[local](file:///etc/passwd)',
    '[relative](/v1/send)',
    '[mail](mailto:a@example.com)',
    '![photo](https://example.com/private.png)',
    '![bad](data:image/svg+xml,test)',
    '[attribute](https://example.com/\"onclick=\"alert%281%29)',
    '[title](https://example.com \"x\\\" onmouseover=\\\"alert(1)\")',
  ].join("\n\n"));
  assert.ok(!/<(?:script|img|iframe|style)\b/i.test(html));
  assert.ok(!/href="(?:javascript|data|file|mailto):/i.test(html));
  assert.ok(!html.includes('href="/v1/send"'));
  assert.ok(html.includes("&lt;script&gt;"));
  assert.ok(html.includes('href="https://example.com/private.png"'));
  assert.ok(!/"\s+on(?:click|mouseover|error)=/i.test(html));
});

test("notification quotes keep external markup literal and retain the object reference link", () => {
  const html = markdownHTML(String.raw`alice · 发表了评论

> ＠developer
> \# fake heading
> \!\[image\]\(https\://evil\.invalid\)

[group/project · 工作项 #2 · 登录失败](https://gitlab.example.com/group/project/-/issues/2#note_15)`);
  assert.equal((html.match(/<blockquote>/g) || []).length, 1);
  assert.equal((html.match(/<a /g) || []).length, 1);
  assert.ok(!/<(?:img|h1|h2)\b/.test(html));
  assert.ok(html.includes("![image](https://evil.invalid)"));
  assert.ok(html.includes('href="https://gitlab.example.com/group/project/-/issues/2#note_15" target="_blank" rel="noopener noreferrer"'));
});

const upload = "/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/image.png";
const imageSource = { provider: "gitlab", channel: "events", instance: "https://gitlab.example.com", project_id: 312, project: "group/project" };
const imageInfo = { room_id: "room-a", room_instance: "instance-a", service_epoch: "epoch-a" };
const imageOptions = locator => notificationOptions(imageInfo, "operator", imageSource, locator);
// Mirror the persisted notification.Quote envelope, including literal escaping
// and neutralized mentions. Both old entries and new deliveries use this format.
function notificationQuote(body) {
  return body.split("\n").map(line => "> " + line.replace(/@/g, "＠").replace(/[\\*_\[\]<>`#\-+!|.:()~]/g, "\\$&")).join("\n");
}

test("saved GitLab comments restore Markdown and lazy same-origin images with dimensions", () => {
  const text = notificationQuote(`**检查图片**\n![image](${upload}){width=900 height=536}\n@reviewer\n\n- [x] 已检查\n\n\`\`\`js\nconst count = 1;\n\`\`\``);
  const html = markdownHTML(text, imageOptions({ seq: 8, agent_id: "assistant", membership_id: "member-a" }));
  assert.ok(html.includes("<strong>检查图片</strong>"));
  assert.ok(html.includes('<img class="message-image" src="/v1/platform/attachments?'));
  for (const part of ['width="900" height="536"', 'loading="lazy"', 'decoding="async"', 'referrerpolicy="no-referrer"', 'type="checkbox"', '<pre><code class="language-js">', "＠reviewer"]) assert.ok(html.includes(part), part);
  assert.ok(!html.includes("{width="));
  const src = html.match(/<img[^>]+src="([^"]+)"/)[1].replaceAll("&amp;", "&");
  const query = new URL(src, "http://teamx.invalid").searchParams;
  assert.equal(query.get("path"), imageSource.instance + upload);
  assert.equal(query.get("seq"), "8");
  assert.equal(query.get("room_id"), "room-a");
  assert.equal(query.get("room_instance"), "instance-a");
  assert.equal(query.get("service_epoch"), "epoch-a");
  assert.equal(query.get("membership_id"), "member-a");
  assert.ok([...query.keys()].every(key => !/token|^pat$/i.test(key)));
});

test("GitLab attachment conversion accepts bound project URLs and rejects other origins and projects", () => {
  const options = imageOptions({ seq: 2 });
  for (const href of [upload, imageSource.instance + upload, imageSource.instance + "/-/project/312" + upload, "/group/project/-" + upload, "/group/project" + upload]) {
    assert.ok(options.attachmentURL(href), href);
  }
  for (const href of ["https://evil.invalid" + upload, "//gitlab.example.com" + upload, "/-/project/313" + upload, upload + "?token=x", upload + "#x", "javascript:alert(1)", "data:image/png,x", upload.replace("image.png", "%5cimage.png"), upload.replace("image.png", "%00.png")]) {
    assert.equal(options.attachmentURL(href), null, href);
    assert.ok(!markdownHTML(notificationQuote(`![image](${href})`), options).includes("<img"));
  }
  assert.equal(notificationOptions(imageInfo, "operator", { ...imageSource, provider: "other" }, { seq: 1 }), undefined);
  assert.equal(notificationOptions(imageInfo, "operator", { ...imageSource, channel: "unknown" }, { seq: 1 }), undefined);
  assert.ok(!markdownHTML(`![image](${upload})`).includes("<img"));
});

test("restored GitLab Markdown still escapes HTML and keeps code/literal escapes literal", () => {
  const text = notificationQuote(`<img src=x onerror=alert(1)>\n<script>alert(1)</script>\n\n[run](javascript:alert%281%29)\n\n\\*literal\\*\n\n\`![image](${upload})\`\n\n\`\`\`md\n![image](${upload})\n\`\`\``);
  const html = markdownHTML(text, imageOptions({ seq: 1 }));
  assert.ok(!/<(?:script|img|iframe)\b/i.test(html));
  assert.ok(!html.includes('href="javascript:'));
  assert.ok(html.includes("&lt;img"));
  assert.ok(html.includes("*literal*"));
  assert.ok(!html.includes("<em>literal</em>"));
});

test("image size syntax supports a following line and ignores invalid attributes", () => {
  const options = imageOptions({ seq: 1 });
  assert.ok(markdownHTML(notificationQuote(`![image](${upload})\n{height=536 width=900}`), options).includes('width="900" height="536"'));
  for (const size of ["{width=0}", "{width=99999999}", '{width="900" onerror="x"}', "{width=900 width=20}"]) {
    const html = markdownHTML(notificationQuote(`![image](${upload})${size}`), options);
    assert.ok(!/<img[^>]+width=/.test(html), size);
  }
});
