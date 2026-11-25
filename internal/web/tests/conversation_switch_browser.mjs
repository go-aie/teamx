// Real Chromium, production markup and CSS, controlled HTTP/SSE APIs.
// Samples every animation frame during cold entry and cached revalidation.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile, mkdir, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import { resolve, extname } from "node:path";
import { fixture } from "./web_member_platform_fixture.mjs";

const { chromium } = createRequire(new URL("../../../packaging/verifier/package.json", import.meta.url))("playwright");
const assets = fileURLToPath(new URL("../assets/", import.meta.url));
const output = resolve(process.argv[2] || ".local/conversation-switch-browser");
await mkdir(output, { recursive: true });
const html = (await readFile(resolve(assets, "index.html"), "utf8")).replace('<script type="module" src="/ui/app.js"></script>', "");
const api = fixture(), globalVersions = { settings:"1", catalog:"1", platforms:"1" };
const rooms = ["R", "S", "DM"].map(id => ({ room_id:id, room_instance:`instance-${id}`, name:id, status:"ready", runtime_status:"ready",
  is_direct:id === "DM", service:{room_id:id, room_instance:`instance-${id}`, service_epoch:`runtime-${id}`} }));
let delayedRoom, delayedReadingRoom, seenMentions = true, connections = 0;
const releases = [], readingReleases = [], errors = [], reports = [];
const messages = room => Array.from({length:16}, (_, i) => ({seq:i+1, text:`${room} message ${i+1}\n\n${i < 2 ? "@tester " : ""}A stable conversation body with enough content to preserve a reading position.`,
  author:{kind:"agent",id:"a"}, created_at:"2026-11-25T00:00:00Z"}));
api.hook = (path, body) => {
  if (path === "/v1/ui") return {managed:true, installation_id:"switch-browser", service_epoch:api.epoch, time_zone:"Asia/Shanghai", settings_version:1};
  if (path === "/v1/rooms") return {service_epoch:api.epoch, operator:"tester", rooms, teams:[], time_zone:"Asia/Shanghai", settings_version:1};
  if (path === "/v1/platforms/interactions/list") return {interactions:[]};
  if (path === "/v1/read") return {messages:messages(body.room_id).filter(m => m.seq > (body.after || 0)),snapshot_seq:16,cleared_through:0,has_more:false};
  if (path === "/v1/member/list" && body.room_id === "DM") return {members:[api.members[0]]};
  if (path === "/v1/human-read/state") return {version:2,room_id:body.room_id,room_instance:body.room_instance,operator_id:"tester",revision:0,
    snapshot_seq:16,cleared_through:0,opened_through:16,mention_seen_seqs:seenMentions ? [1,2] : [],unseen_mention_seqs:seenMentions ? [] : [1,2],unread_count:seenMentions ? 0 : 2,mention_count:seenMentions ? 0 : 2,
    ...(body.include_new_messages ? {new_message_seqs:[]} : {})};
};
const server = createServer(async (req, res) => {
  try {
    const url = new URL(req.url, "http://fixture");
    if (url.pathname === "/v1/updates") {
      res.writeHead(200, {"Content-Type":"text/event-stream", "Cache-Control":"no-store"});
      res.write(`event: hello\ndata: ${JSON.stringify({protocol:1,installation_id:"switch-browser",service_epoch:api.epoch,connection_id:`connection-${++connections}`,global_versions:globalVersions})}\n\n`);
      return;
    }
    if (url.pathname.startsWith("/v1/")) {
      let raw = ""; for await (const chunk of req) raw += chunk;
      const body = raw ? JSON.parse(raw) : undefined;
      if (body?.room_id === delayedRoom && ["/v1/read", "/v1/member/list"].includes(url.pathname)) await new Promise(done => releases.push(done));
      if (body?.room_id === delayedReadingRoom && url.pathname === "/v1/human-read/state") await new Promise(done => readingReleases.push(done));
      let data;
      if (url.pathname === "/v1/access/session") data = {login_enabled:false};
      else try { data = await api.request(url.pathname + url.search, body); }
      catch (error) { if (!error.message.startsWith("Unexpected ")) throw error; res.writeHead(404); res.end("{}"); return; }
      res.writeHead(200, {"Content-Type":"application/json", "Cache-Control":"no-store"}); res.end(JSON.stringify(data)); return;
    }
    if (url.pathname === "/") { res.writeHead(200, {"Content-Type":"text/html; charset=utf-8"}); res.end(html); return; }
    const path = resolve(assets, url.pathname.replace(/^\/ui\//, ""));
    assert.ok(path.startsWith(assets));
    res.writeHead(200, {"Content-Type":extname(path) === ".css" ? "text/css" : extname(path) === ".svg" ? "image/svg+xml" : "text/javascript"});
    res.end(await readFile(path));
  } catch (error) { errors.push(error.message); res.writeHead(500); res.end("{}"); }
});
await new Promise(done => server.listen(0, "127.0.0.1", done));
const browser = await chromium.launch({headless:true});
const release = () => { delayedRoom = undefined; for (const done of releases.splice(0)) done(); };
const releaseReading = () => { delayedReadingRoom = undefined; for (const done of readingReleases.splice(0)) done(); };
try {
  for (const [name, width, height] of [["desktop",1440,900], ["phone",390,844]]) {
    seenMentions = true;
    const context = await browser.newContext({viewport:{width,height}}), page = await context.newPage();
    page.on("pageerror", error => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.address().port}/`);
    await page.evaluate(async () => { const {createApp} = await import("/ui/application.mjs"); window.switchApp = await createApp(); });
    const ready = () => page.waitForFunction(() => document.querySelector("#connection").textContent === "服务已连接");
    const select = async room => {
      if (width < 768 && !await page.locator("#room-nav").isVisible()) await page.locator("#mobile-back-to-rooms").click();
      await page.locator(".room-choice").filter({has:page.locator("strong", {hasText:new RegExp(`^${room}$`)})}).click();
    };
    await ready();
    await select("R"); await ready();
    // Cold entry: no welcome/empty-members frame while HTTP is pending.
    delayedRoom = "S";
    await select("S");
    await page.waitForFunction(() => !document.querySelector("#conversation-loading").hidden);
    await page.evaluate(() => {
      window.framesSeen = {empty:0, membersEmpty:0, blank:0, replacement:0, samples:0};
      window.sampling = true;
      function sample() {
        if (!window.sampling) return;
        const $ = id => document.getElementById(id), frames = window.framesSeen;
        frames.samples++;
        if (!$("empty").hidden) frames.empty++;
        if (!$("member-empty").hidden) frames.membersEmpty++;
        if ($("conversation-loading").hidden && !$("messages").children.length) frames.blank++;
        requestAnimationFrame(sample);
      }
      requestAnimationFrame(sample);
    });
    await page.screenshot({path:resolve(output, `${name}-loading.png`)});
    await page.waitForTimeout(200); release(); await ready();
    const cold = await page.evaluate(() => { window.sampling = false; return window.framesSeen; });
    assert.ok(cold.samples > 2); assert.equal(cold.empty,0); assert.equal(cold.membersEmpty,0); assert.equal(cold.blank,0);
    // Return to cached R with fresh data held back, retaining a non-bottom anchor.
    await select("R"); await ready();
    await page.locator("#timeline").evaluate(el => el.scrollTop = Math.min(300, el.scrollHeight-el.clientHeight-150));
    const savedTop = await page.locator("#timeline").evaluate(el => el.scrollTop);
    await select("S"); await ready(); delayedRoom = "R";
    await select("R");
    await page.waitForFunction(() => document.querySelector("#message-1 .message-text")?.textContent.includes("R message"));
    await page.evaluate(() => {
      window.retainedMessage = document.getElementById("message-1");
      window.retainedBody = window.retainedMessage.querySelector(".message-text");
      window.framesSeen = {empty:0, replacement:0, animation:0, samples:0}; window.sampling = true;
      function sample() {
        if (!window.sampling) return;
        const frames = window.framesSeen; frames.samples++;
        if (!document.getElementById("empty").hidden) frames.empty++;
        if (document.getElementById("message-1") !== window.retainedMessage) frames.replacement++;
        if ([...document.querySelectorAll(".message")].some(node => node.getAnimations().length)) frames.animation++;
        requestAnimationFrame(sample);
      }
      requestAnimationFrame(sample);
    });
    assert.ok(Math.abs(await page.locator("#timeline").evaluate(el => el.scrollTop)-savedTop) < 2);
    await page.waitForTimeout(200); release(); await ready(); await page.waitForTimeout(100);
    const warm = await page.evaluate(() => { window.sampling = false; return {...window.framesSeen,bodyPreserved:document.querySelector("#message-1 .message-text") === window.retainedBody}; });
    assert.ok(warm.samples > 2); assert.equal(warm.empty,0); assert.equal(warm.replacement,0); assert.equal(warm.animation,0); assert.equal(warm.bodyPreserved,true);
    assert.ok(Math.abs(await page.locator("#timeline").evaluate(el => el.scrollTop)-savedTop) < 2);
    await page.screenshot({path:resolve(output, `${name}-cached.png`)});
    // The same cache/revalidation path also applies to private conversations.
    await select("DM"); await ready(); await select("R"); await ready(); delayedRoom = "DM";
    await select("DM");
    assert.equal(await page.locator("#conversation-loading").isVisible(),false);
    const direct = await page.locator("#message-1").elementHandle();
    release(); await ready();
    assert.equal(await direct.evaluate(node => node === document.getElementById("message-1")),true);
    const reading = [];
    for (const seen of [true,false]) {
      seenMentions = true;
      await select("R"); await ready();
      await page.locator("#timeline").evaluate(el => el.scrollTop = el.scrollHeight);
      await select("S"); await ready(); delayedReadingRoom = "R"; seenMentions = seen;
      await page.evaluate(() => {
        window.mentionFrames = {samples:0,highlighted:0}; window.sampling = true;
        function sample() {
          if (!window.sampling) return;
          if (document.getElementById("room-name").textContent === "R") {
            window.mentionFrames.samples++;
            if (document.querySelector(".message.mentioned")) window.mentionFrames.highlighted++;
          }
          requestAnimationFrame(sample);
        }
        requestAnimationFrame(sample);
      });
      await select("R"); await ready(); await page.waitForTimeout(200);
      const frames = await page.evaluate(() => { window.sampling = false; return window.mentionFrames; });
      assert.ok(frames.samples > 2); assert.equal(frames.highlighted,0);
      releaseReading();
      await page.waitForFunction(expected => document.querySelectorAll(".message.mentioned").length === expected, seen ? 0 : 2);
      reading.push({seen,unknownStateFrames:frames.samples,incorrectHighlightFrames:frames.highlighted,verifiedHighlights:seen ? 0 : 2});
    }
    reports.push({viewport:name,cold,warm,directMessagePreserved:true,reading});
    await context.close();
  }
  assert.deepEqual(errors, []);
  await writeFile(resolve(output,"report.json"), JSON.stringify({passed:true,backend:"controlled HTTP/SSE",reports},null,2));
  console.log(JSON.stringify({passed:true,reports}));
} finally {
  release(); releaseReading(); await browser.close(); server.closeAllConnections(); await new Promise(done => server.close(done));
}
