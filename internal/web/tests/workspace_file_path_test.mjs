import {test} from "node:test";
import assert from "node:assert/strict";
import {workspaceFilePath} from "../assets/workspace-file-path.mjs";
import {markdownHTML} from "../assets/markdown.mjs";

test('file URLs decode once and normalize relative paths within the workspace root',()=>{
 for(const [href,base,want] of [
  ['docs/示例.md','','docs/示例.md'],['./docs/a/../result.md','','docs/result.md'],
  ['docs/%E4%B8%AD%E6%96%87%20file.md','','docs/中文 file.md'],
  ['docs/%252e%252e/result.md','','docs/%2e%2e/result.md'],
  ['docs/%23part%3F.md','','docs/#part?.md'],['../images/demo.png','docs','images/demo.png'],
 ])assert.equal(workspaceFilePath(href,base),want,href);
 for(const href of ['../secret','a/../../secret','%2e%2e/secret','/absolute','%2fabsolute','//host/a','file:a',
  'https://example.org/a','custom:x','C:/file','a\\b','a%5cb','a%00b','a%0ab','%zz','doc.md?q=x','doc.md#title','#title','']){
  assert.equal(workspaceFilePath(href),null,href);
 }
});

test('native explicit and reference links opt in while quotes, code and images keep their original rules',()=>{
 const source='[交付](docs/file%20name.md) [引用][ref]\n\n[ref]: docs/引用.md\n\n> [引用文本](docs/quoted.md)\n>\n> > [嵌套](docs/nested.md)\n\n`[代码](docs/code.md)`\n\n```md\n[代码块](docs/fenced.md)\n```\n\n[外链](https://example.org) ![图片](docs/image.png)\n\n[危险](file:///etc/passwd) [越界](../secret)';
 const html=markdownHTML(source,{workspaceFiles:true});
 assert.equal((html.match(/class="workspace-file-link"/g)||[]).length,2);
 assert.ok(html.includes('data-file-path="docs/file name.md"'));
 assert.ok(html.includes('data-file-path="docs/引用.md"'));
 assert.ok(html.includes('href="https://example.org/"'));
 assert.ok(!html.includes('<img'));
 assert.ok(!markdownHTML(source).includes('workspace-file-link'));
 const escaped=markdownHTML('[x](docs/%22%3E%3Cimg%20onerror=alert(1)%3E.md)',{workspaceFiles:true});
 assert.ok(!escaped.includes('<img'));assert.ok(escaped.includes('&quot;&gt;&lt;img'));
});

test('documents share URL decoding while separating path, relative base and anchors',async()=>{
 const {workspaceFileTarget,workspaceHeadingID}=await import('../assets/workspace-file-path.mjs');
 for(const [href,want] of [
  ['../images/%E5%9B%BE.png',{path:'images/图.png',fragment:''}],
  ['guide.md#%E4%BD%BF%E7%94%A8',{path:'docs/guide.md',fragment:'使用'}],
  ['#中文-标题',{path:'docs/readme.md',fragment:'中文-标题'}],
  ['guide%23x.md#part',{path:'docs/guide#x.md',fragment:'part'}],
  ['%252e%252e/%23.md',{path:'docs/%2e%2e/#.md',fragment:''}],
 ]) assert.deepEqual(workspaceFileTarget(href,'docs','docs/readme.md'),want,href);
 for(const href of ['../../secret','../%2e%2e/secret','//host/image.png','/root.png','file:x','data:image/png,x','https://example.org/image.png','a.md?x#y','#%00','a%5cb']){
  assert.equal(workspaceFileTarget(href,'docs','docs/readme.md'),null,href);
 }
 assert.equal(workspaceHeadingID('中文 标题 + Hello!'),'中文-标题-hello');
 const html=markdownHTML('[导航](guide.md#%E4%BD%BF%E7%94%A8) [本页](#标题)\n\n> [引用内导航](../other.md)\n\n![本地](../images/a.png) ![远程](https://example.org/a.png) ![越界](../../secret) ![危险](data:image/png;base64,AAA)\n\n<iframe src="/x"></iframe><img src="/secret" onerror="alert(1)">',{workspaceDocument:{path:'docs/readme.md'}});
 assert.equal((html.match(/workspace-file-link/g)||[]).length,3);
 assert.ok(html.includes('data-file-path="docs/guide.md" data-file-fragment="使用"'));
 assert.ok(html.includes('data-file-path="images/a.png"'));
 assert.equal((html.match(/workspace-image-load/g)||[]).length,1);
 assert.ok(html.includes('href="https://example.org/a.png"'));
 assert.ok(!html.includes('<img')&&!html.includes('<iframe'));
 assert.equal((markdownHTML('[message](guide.md#part)',{workspaceFiles:true}).match(/workspace-file-link/g)||[]).length,0);
});
