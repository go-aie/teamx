import { test } from "node:test";
import assert from "node:assert/strict";
import { markdownHTML } from "./web/markdown.mjs";

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
