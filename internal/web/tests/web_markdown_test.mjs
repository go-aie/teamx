import { test } from "node:test";
import assert from "node:assert/strict";
import { markdownHTML, notificationOptions, openImagePreview, renderMessageBody } from "../assets/markdown.mjs";

test("plain operator mentions display the current username without changing source text or literal contexts", () => {
  const owner = "owner_" + "a".repeat(32);
  const source = `🙂 @${owner}、@russellluo 请确认 @reviewer @${owner}2`;
  const textNode = (data, literal = false) => ({ data, parentElement: { closest: () => literal },
    replaceWith(fragment) { this.rendered = fragment.children; } });
  const nodes = [textNode(source), ...["a", "code", "pre", "mark"].map(() => textNode("@" + owner, true))];
  const doc = {
    createTreeWalker() { let index = 0; return { nextNode() { this.currentNode = nodes[index++]; return !!this.currentNode; } }; },
    createTextNode: data => ({ textContent: data }),
    createElement: tag => ({ tag }),
    createDocumentFragment: () => ({ children: [], append(...items) { this.children.push(...items); } }),
  };
  const container = { ownerDocument: doc, querySelectorAll: () => [] };
  let username = "russellluo";
  const options = { operatorNames: [owner, "russellluo"], memberNames: ["reviewer"],
    mentionLabel: at => { assert.deepEqual(at, { kind: "human", id: owner }); return username; } };
  renderMessageBody(container, source, owner, options);
  assert.equal(nodes[0].rendered.map(n => n.textContent).join(""), `🙂 @russellluo、@russellluo 请确认 @reviewer @${owner}2`);
  assert.deepEqual(nodes[0].rendered.filter(n => n.tag === "mark").map(n => [n.className, n.textContent]),
    [["mention", "@russellluo"], ["mention", "@russellluo"], ["mention-member", "@reviewer"]]);
  username = "自定义 <img src=x>";
  renderMessageBody(container, source, owner, options);
  assert.equal(nodes[0].rendered[1].textContent, "@自定义 <img src=x>");
  assert.equal(nodes[0].data, source);
  assert.ok(nodes.slice(1).every(n => !n.rendered));
  renderMessageBody(container, source, owner, { operatorNames: [owner] });
  assert.equal(nodes[0].rendered.map(n => n.textContent).join(""), source);
});

test("messages render Markdown structure and preserve ordinary line breaks", () => {
  const html = markdownHTML("## 方案\n\n**粗体**、*强调*、~~删除~~、`todo.py`\n下一行\n\n> 引用\n\n1. 添加\n2. 完成\n\n- [x] 已完成\n- [ ] 待处理\n\n---");
  for (const part of ["<h2>方案</h2>", "<strong>粗体</strong>", "<em>强调</em>", "<del>删除</del>", "<code>todo.py</code>", "<br>下一行", "<blockquote>", "<ol>", "<li>添加</li>", "<hr>", 'disabled=""']) {
    assert.ok(html.includes(part), part);
  }
  assert.equal(markdownHTML("a &amp; b & c"), "<p>a &amp; b &amp; c</p>\n");
});

test("HTML formatting renders alongside Markdown while code and escaped tags stay literal", () => {
  const html = markdownHTML('1. <strong>通知</strong>：已开启\n2. <strong>工具</strong>：<code>gitlab-552fe1bf</code>\n\n<b>粗体</b> <em>强调</em> <i>斜体</i> <del>删除</del> <s>删除</s><br>换行<br/>继续\n\n<code>x < y &amp; **literal** <strong>literal</strong> @ann</code>\n\n`<strong>literal</strong>`\n\n\\<strong>escaped\\</strong>');
  for (const part of ['<strong>通知</strong>', '<strong>工具</strong>', '<code>gitlab-552fe1bf</code>',
    '<b>粗体</b>', '<em>强调</em>', '<i>斜体</i>', '<del>删除</del>', '<s>删除</s>', '<br>换行', '<br/>继续',
    '<code>x &lt; y &amp; **literal** &lt;strong&gt;literal&lt;/strong&gt; @ann</code>',
    '<code>&lt;strong&gt;literal&lt;/strong&gt;</code>', '&lt;strong&gt;escaped&lt;/strong&gt;']) assert.ok(html.includes(part), part);
  const bound = markdownHTML('<strong>@ann</strong> <code>@ann</code>', {
    mentions:[{start:8,end:12,kind:"human",id:"owner"},{start:28,end:32,kind:"human",id:"owner"}],
    operatorID:"owner", mentionLabel:() => "用户名",
  });
  assert.ok(bound.includes('<strong><mark class="mention">@用户名</mark></strong>'));
  assert.ok(bound.includes('<code>@ann</code>'));
});

test("HTML formatting never accepts attributes or enables other HTML elements", () => {
  const html = markdownHTML([
    '<strong onclick="alert(1)">通知</strong>', '<b style="color:red">text</b>',
    '<code onmouseover="alert(1)">tool</code>', '<br onmouseover="alert(1)">',
    '<a href="javascript:alert(1)">run</a>', '<svg onload="alert(1)"></svg>',
    '<strong><img src=x onerror=alert(1)></strong>',
    '<strong><script>alert(1)</script></strong>',
  ].join('\n\n'));
  assert.ok(!/<(?:strong|b|code|br)\s+[^>]/i.test(html));
  assert.ok(!/<(?:a|svg|img|script)\b/i.test(html));
  for (const tag of ['strong onclick=', 'code onmouseover=', 'br onmouseover=', 'a href=', 'svg onload=', 'img src=']) assert.ok(html.includes('&lt;' + tag), tag);
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
  assert.ok(html.includes('<button class="attachment-preview" type="button" aria-label="放大查看图片"'));
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

test("Lark images and legacy keys render same-origin previews bound to the exact Inbox entry", () => {
  const source = {provider:"lark",channel:"messages",reference:{version:2,target:{type:"message"}}};
  const options = notificationOptions(imageInfo,"operator",source,{seq:9,agent_id:"assistant",membership_id:"member-a"});
  for (const text of ["![飞书图片](lark-image:img_v3_fixture)\n请分析图片", "image_key: img_v3_fixture\n请分析图片"]) {
    const html = markdownHTML(text,options);
    assert.ok(html.includes('<img class="message-image"'));
    assert.ok(html.includes('class="attachment-preview"'));
    assert.ok(html.includes('<button class="attachment-preview" type="button"'));
    assert.ok(html.includes('aria-label="放大查看图片"'));
    assert.ok(!html.includes('target="_blank"'));
    assert.ok(html.includes("请分析图片"));
    const src = html.match(/<img[^>]+src="([^"]+)"/)[1].replaceAll("&amp;","&");
    const query = new URL(src,"http://fixture").searchParams;
    for (const [key,value] of Object.entries({path:"lark-image:img_v3_fixture",seq:"9",agent_id:"assistant",membership_id:"member-a",room_id:"room-a",service_epoch:"epoch-a"})) assert.equal(query.get(key),value);
  }
  for (const path of ["lark-image:img_x?token=secret","lark-image:../img_x","https://evil.invalid/a.png","data:image/png,test"]) assert.equal(options.attachmentURL(path),null);
  assert.ok(!markdownHTML("![image](lark-image:img_x)").includes("<img"));
  assert.ok(!markdownHTML("`![image](lark-image:img_x)`",options).includes("<img"));
  assert.equal(notificationOptions(imageInfo,"operator",{...source,channel:"other"},{seq:1}),undefined);
});

for (const provider of ["lark", "gitlab"]) test(`${provider} image preview follows adopted messages, reuses a modal, and clears it on every close path`, () => {
  class Element {
    constructor(tag) {
      this.tag = tag; this.children = []; this.attributes = {}; this.listeners = {}; this.open = false;
      this.dataset = {}; this.style = {}; this.clientWidth = 832; this.clientHeight = 632; this.offsetHeight = 56;
      this.scrollWidth = 832; this.scrollHeight = 632;
      const classes = new Set();
      this.classList = {add:name=>classes.add(name),remove:name=>classes.delete(name),contains:name=>classes.has(name),
        toggle:(name,on)=>on ? classes.add(name) : classes.delete(name)};
    }
    append(...children) { this.children.push(...children); }
    setAttribute(key, value) { this.attributes[key] = value; }
    removeAttribute(key) { delete this[key]; }
    addEventListener(name, fn) { (this.listeners[name] ||= []).push(fn); }
    emit(name, data = {}) { for (const fn of this.listeners[name] || []) fn({target:this,preventDefault() {},stopPropagation() {},...data}); }
    click() { if (!this.disabled) this.emit("click"); }
    showModal() { this.open = true; this.modal = true; }
    close() { this.open = false; this.emit("close"); }
  }
  const doc = {body:new Element("body"),documentElement:{clientWidth:864},defaultView:{innerHeight:724,addEventListener() {}},
    createElement:tag => new Element(tag),createElementNS:(_,tag) => new Element(tag),createTreeWalker:() => ({nextNode:() => false})};
  const options = notificationOptions(imageInfo, "operator", provider === "lark"
    ? {provider:"lark",channel:"messages",reference:{version:2,target:{type:"message"}}} : imageSource,
    {seq:35,agent_id:"assistant",membership_id:"member-a"});
  const href = provider === "lark" ? "lark-image:img_saved" : upload;
  const src = options.attachmentURL(href);
  const button = new Element("button"), thumbnail = new Element("img");
  thumbnail.src = src; thumbnail.alt = "截图 <img src=x>";
  thumbnail.closest = selector => selector === "button.attachment-preview" ? button : null;
  const templateDoc = {...doc,body:null};
  const container = {ownerDocument:templateDoc,querySelectorAll:() => [thumbnail]};
  const text = `![截图](${href})`;
  renderMessageBody(container, provider === "gitlab" ? notificationQuote(text) : text, "operator", options);
  assert.ok(container.innerHTML.includes('<button class="attachment-preview"'));
  container.ownerDocument = doc;
  button.emit("click");
  const dialog = doc.body.children[0], [toolbar, stage] = dialog.children, [controls, close] = toolbar.children, [canvas, error] = stage.children;
  const image = canvas.children[0], [zoomOut, scale, zoomIn, original, fit, divider, rotate, download] = controls.children;
  assert.equal(dialog.open, true); assert.equal(dialog.modal, true);
  assert.equal(image.src, src); assert.equal(image.referrerPolicy, "no-referrer");
  assert.equal(image.alt, "截图 <img src=x>");
  assert.equal(zoomIn.disabled, true); assert.equal(canvas.hidden, true);
  image.naturalWidth = 1600; image.naturalHeight = 900; image.emit("load");
  assert.equal(canvas.hidden, false); assert.equal(error.hidden, true);
  assert.equal(scale.textContent, "50%"); assert.equal(canvas.style.width, "800px");
  assert.equal(download.href, src); assert.equal(download.hidden, false);
  assert.equal(zoomIn.disabled, false);
  const largeSize = {width:dialog.style.width,height:dialog.style.height};
  original.click(); assert.equal(scale.textContent, "100%");
  assert.deepEqual({width:dialog.style.width,height:dialog.style.height},largeSize,"manual zoom keeps the window stable");
  assert.equal(image.style.width, "1600px"); assert.equal(stage.classList.contains("pannable"), true);
  zoomIn.click(); assert.equal(scale.textContent, "125%");
  zoomOut.click(); assert.equal(scale.textContent, "100%");
  fit.click(); assert.equal(scale.textContent, "50%");
  rotate.click(); assert.equal(image.style.transform, "translate(-50%, -50%) rotate(90deg)");
  assert.equal(scale.textContent, "38%"); assert.equal(canvas.style.height, "600px");
  dialog.emit("keydown", {key:"1"}); assert.equal(scale.textContent, "100%");
  dialog.emit("keydown", {key:"0"}); assert.equal(scale.textContent, "38%");
  dialog.emit("click", {target:image}); assert.equal(dialog.open, true);
  image.emit("error"); assert.equal(error.hidden, false);
  assert.equal(canvas.hidden, true); assert.equal(zoomIn.disabled, true); assert.equal(download.hidden, true);
  close.emit("click");
  assert.equal(dialog.open, false); assert.equal(image.src, undefined); assert.equal(error.hidden, true);
  for (const action of ["cancel", "click"]) {
    openImagePreview(doc, src, "截图");
    assert.equal(doc.body.children.length, 1);
    image.emit("load"); assert.equal(error.hidden, true);
    assert.equal(scale.textContent, "50%", "each opening resets rotation and scale");
    dialog.emit(action);
    assert.equal(dialog.open, false); assert.equal(image.src, undefined);
  }
  button.replaceWith = fallback => { button.replacement = fallback; };
  thumbnail.emit("error");
  assert.equal(button.replacement.textContent, "图片加载失败，点击重试");
  button.replacement.emit("click");
  assert.equal(dialog.open, true); assert.equal(image.src, src);
  assert.equal(doc.body.children.length, 1);
  close.emit("click");
  openImagePreview(doc, src, "小图");
  image.naturalWidth = 480; image.naturalHeight = 120; image.emit("load");
  assert.ok(parseFloat(dialog.style.width) < parseFloat(largeSize.width));
  assert.ok(parseFloat(dialog.style.height) < parseFloat(largeSize.height));
  assert.equal(scale.textContent,"100%","small images retain their original size");
  close.emit("click");
});

test("compact Lark mentions show names and keep identity in tooltips without rewriting code or link destinations", () => {
  const mention = "@罗彭（open_id: ou_reader；当前接收账号）";
  const options = {compactLarkMentions:true};
  const html = markdownHTML(`${mention} 请确认，@张三（user_id: user_peer） 收到\n\n\`${mention}\`\n\n\`\`\`text\n${mention}\n\`\`\`\n\n[原消息](https://example.com/?user=ou_reader)`, options);
  assert.ok(html.includes('title="open_id: ou_reader；当前接收账号">@罗彭</span>'));
  assert.ok(html.includes('title="user_id: user_peer">@张三</span>'));
  assert.ok(html.includes(`<code>${mention}</code>`));
  assert.ok(html.includes(`<pre><code class="language-text">${mention}\n</code></pre>`));
  assert.ok(html.includes('href="https://example.com/?user=ou_reader"'));
  assert.ok(markdownHTML(mention).includes(mention), "ordinary messages and Agent text are not compacted");
  assert.ok(markdownHTML('@未知（身份未解析）',options).includes('title="身份未解析">@未知</span>'));
  assert.ok(markdownHTML('@同事（负责测试）',options).includes('@同事（负责测试）'));
  const escaped = markdownHTML('@<img src=x>（open_id: ou_peer" onmouseover="x）', options);
  assert.ok(!escaped.includes('<img'));
  assert.ok(!/"\s+onmouseover=/.test(escaped));
});

test("calendar receipt links require an explicit local handler and an exact task reference", () => {
  const id="task_"+"a".repeat(32), source=`[查看日历](#calendar/task/${id})`;
  assert.match(markdownHTML(source,{openCalendar:()=>{}}),/class="calendar-receipt-link"/);
  assert.doesNotMatch(markdownHTML(source),/<a/);
  for(const href of ["#calendar/task/task_other",`#calendar/task/${id}?room=other`,`#calendar/task/${id}/extra`,"javascript:alert(1)"]){
    assert.doesNotMatch(markdownHTML(`[安排](${href})`,{openCalendar:()=>{}}),/calendar-receipt-link|<a/);
  }
  let click,opened,prevented=false;
  const link={getAttribute:()=>`#calendar/task/${id}`,addEventListener:(event,handler)=>{assert.equal(event,"click");click=handler;}};
  const container={ownerDocument:{createTreeWalker:()=>({nextNode:()=>false})},querySelectorAll:selector=>selector==="a.calendar-receipt-link"?[link]:[]};
  renderMessageBody(container,source,"owner",{openCalendar:taskID=>opened=taskID});
  click({preventDefault(){prevented=true;}});assert.equal(opened,id);assert.equal(prevented,true);
});
