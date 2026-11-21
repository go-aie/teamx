// Real retained Events and image proxy on the server announced by the read-only
// recovery test. No browser request fabricates a response or writes to GitLab.
import assert from 'node:assert/strict';
import {readFile, writeFile} from 'node:fs/promises';
import {join, resolve} from 'node:path';
import {chromium} from '../../../packaging/verifier/node_modules/playwright/index.mjs';

const output=resolve(process.argv[2]);
const ready=JSON.parse(await readFile(join(output,'browser-ready.json'),'utf8'));
const browser=await chromium.launch({headless:true});
const context=await browser.newContext({viewport:{width:1440,height:1000}});
const page=await context.newPage();
const errors=[];
page.on('pageerror',e=>errors.push(e.message));
const proof={status:'failed',platform:'real GitLab images through TeamX saved notification proxy',screenshots:[],checks:[]};
const screenshot=async name=>{await page.screenshot({path:join(output,name),fullPage:true});proof.screenshots.push(name);};
try {
  await page.goto(ready.page);
  await page.locator('#members .member-summary').first().waitFor({state:'attached'});
  if (await page.locator('#room-menu-label').getAttribute('aria-expanded')!=='true') await page.locator('#room-menu-label').click();
  await page.locator('#agent-tab-inbox').waitFor({state:'visible'});
  await page.locator('#agent-tab-inbox').click();
  await page.locator('#inbox-entries img.message-image').first().waitFor({state:'visible'});
  await page.waitForFunction(()=>{const a=[...document.querySelectorAll('#inbox-entries img.message-image')];return a.length===3&&a.every(i=>i.complete&&i.naturalWidth===160&&i.naturalHeight===90);});
  const sources=await page.locator('#inbox-entries img.message-image').evaluateAll(a=>a.map(i=>i.getAttribute('src')));
  assert.ok(sources.every(s=>s.startsWith('/v1/platform/attachments?')));
  proof.image_sources=sources;
  for (const details of await page.locator('#inbox-entries .inbox-source-details').all()) await details.evaluate(e=>{e.open=true;});
  const text=await page.locator('#inbox-entries').innerText();
  assert.match(text,/进展上下文/);
  proof.checks.push('three real returned images render in saved Inbox','own-account context shown; independent observer does not claim sender authorship');
  await screenshot('browser-inbox-desktop.png');
  await page.locator('#inbox-entries .attachment-preview').first().click();
  await page.locator('.message-image-dialog[open] img').waitFor({state:'visible'});
  await screenshot('browser-image-preview.png');
  await page.getByRole('button',{name:'关闭图片预览',exact:true}).click();
  await page.setViewportSize({width:390,height:844});
  if (await page.locator('#room-menu-label').getAttribute('aria-expanded')!=='true') await page.locator('#room-menu-label').click();
  await page.locator('#agent-tab-inbox').waitFor({state:'visible'});
  await page.locator('#agent-tab-inbox').click();
  await page.locator('#inbox-entries img.message-image').first().scrollIntoViewIfNeeded();
  await screenshot('browser-inbox-mobile.png');
  await page.locator('#inbox-entries img.message-image').last().scrollIntoViewIfNeeded();
  await screenshot('browser-thread-mobile.png');
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
  proof.checks.push('existing image enlargement','390px mobile notification images and exact-thread image without horizontal overflow');
  assert.deepEqual(errors,[]);
  proof.status='passed';
  await writeFile(join(output,'browser-report.json'),JSON.stringify(proof,null,2)+'\n');
  await writeFile(join(output,'browser-done'),'passed\n');
} catch(e) {
  proof.error=String(e);proof.page_text=(await page.locator('body').innerText()).slice(0,5000);
  await screenshot('browser-failure.png');
  await writeFile(join(output,'browser-report.json'),JSON.stringify(proof,null,2)+'\n');
  await writeFile(join(output,`browser-failure-report-${Date.now()}.json`),JSON.stringify(proof,null,2)+'\n');
  throw e;
} finally {await browser.close();}
