// Optional shared-component acceptance, using production markup/styles/module.
// No application backend, model, credentials or external service is involved.
import assert from "node:assert/strict";
import {createServer} from "node:http";
import {readFile, mkdir, writeFile} from "node:fs/promises";
import {createRequire} from "node:module";
import {resolve, extname} from "node:path";
import {fileURLToPath} from "node:url";

const {chromium} = createRequire(new URL("../../../packaging/verifier/package.json", import.meta.url))("playwright");
const assets = fileURLToPath(new URL("../assets/", import.meta.url));
const output = resolve(process.argv[2] || "/tmp/teamx-date-time-picker");
await mkdir(output, {recursive:true});
const html = (await readFile(resolve(assets, "index.html"), "utf8")).replace('<script type="module" src="/ui/app.js"></script>', "");
const server = createServer(async (request, response) => {
  try {
    if (request.url === "/") { response.setHeader("Content-Type", "text/html; charset=utf-8"); response.end(html); return; }
    const name = decodeURIComponent(new URL(request.url, "http://localhost").pathname).replace(/^\/ui\//, "");
    const path = resolve(assets, name);
    if (!path.startsWith(assets)) throw Error("outside assets");
    response.setHeader("Content-Type", extname(path) === ".css" ? "text/css" : extname(path) === ".svg" ? "image/svg+xml" : "text/javascript");
    response.end(await readFile(path));
  } catch { response.statusCode = 404; response.end(); }
});
await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
const browser = await chromium.launch({headless:true});
const errors = [], checks = [];
try {
  const page = await browser.newPage({viewport:{width:1000,height:900}});
  page.on("pageerror", error => errors.push(error.message));
  await page.goto(`http://127.0.0.1:${server.address().port}/`);
  await page.evaluate(async () => {
    const {createDateTimePicker} = await import("/ui/date-time-picker.mjs");
    document.querySelector("main").hidden = true;
    const surface = document.createElement("dialog"); surface.id = "picker-test-surface"; surface.className = "dialog-surface";
    surface.style.cssText = "width:min(460px,calc(100vw - 24px));padding:24px;overflow:visible";
    const title = document.createElement("h2"); title.textContent = "日期与时间"; title.style.marginTop = "0"; surface.append(title);
    const picker = createDateTimePicker({env:{document, window, MutationObserver}}); picker.mount();
    const fields = [["calendar-start-date","date",false,"执行日期"], ["calendar-local-time","time",false,"执行时刻"], ["gitlab-backfill-start","datetime",true,"通知起始时间"], ["policy-expires","datetime",true,"规则有效期"]];
    for (const [id,kind,seconds,label] of fields) {
      const input = document.getElementById(id), wrapper = input.closest(".date-time-field"), toggle = document.getElementById(id+"-toggle");
      const owner = document.createElement("label"); owner.style.cssText = "display:block;margin-bottom:16px";
      const caption = document.createElement("span"); caption.textContent = label; owner.append(caption, wrapper); surface.append(owner);
      input.disabled = false;
      input.value = kind === "date" ? "2028-02-29" : kind === "time" ? "09:30" : "2026-11-19 09:30:27";
      if (id === "gitlab-backfill-start") input.max = "2026-11-19 09:30:27";
      picker.register(input, {kind, seconds, trigger:toggle, zone:()=>kind==="datetime"?"Asia/Shanghai":document.getElementById("calendar-time-zone").value||"Asia/Shanghai", label, showZone:()=>kind==="time"&&!!document.getElementById("calendar-time-zone").value&&document.getElementById("calendar-time-zone").value!=="Asia/Shanghai"});
    }
    document.body.append(surface); surface.showModal();
  });
  const panel = page.locator("#date-time-picker");
  const shot = name => page.screenshot({path:resolve(output, name+".png"), animations:"disabled"});
  const choice = value => page.locator(`.date-time-choice[data-date-value="${value}"]`);
  const visible = async () => {
    const box = await panel.boundingBox(), viewport = page.viewportSize();
    assert.ok(box && box.x >= 0 && box.y >= 0 && box.x+box.width <= viewport.width && box.y+box.height <= viewport.height, JSON.stringify({box,viewport}));
  };
  await page.locator("#policy-expires-toggle").click(); await visible(); await shot("desktop-date-time");
  assert.equal(await page.locator("#date-time-zone").isVisible(), false);
  assert.equal(await page.locator('#date-time-next .date-time-single').isVisible(), true);
  assert.equal(await page.locator('#date-time-next .date-time-double').isVisible(), false);
  const daySize = await panel.boundingBox();
  await page.locator("#date-time-month").click(); await shot("desktop-months");
  assert.equal((await panel.boundingBox()).height, daySize.height);
  assert.equal(await page.locator('#date-time-next .date-time-double').isVisible(), true);
  assert.equal(await page.locator('#date-time-next .date-time-single').isVisible(), false);
  await page.locator('#date-time-next').click();
  assert.equal(await page.locator('#date-time-year').innerText(), '2027年');
  assert.equal(await page.locator('#date-time-month').innerText(), '11月');
  assert.equal(await page.locator('#date-time-grid').getAttribute('data-level'), 'months');
  assert.equal(await panel.isVisible(), true);
  await page.locator('#date-time-prev').click(); assert.equal(await page.locator('#date-time-year').innerText(), '2026年');
  await choice("5").click(); await page.locator("#date-time-year").click(); await shot("desktop-years");
  assert.equal((await panel.boundingBox()).height, daySize.height);
  const firstYear = Number(await page.locator('.date-time-choice').first().getAttribute('data-date-value'));
  await page.locator('#date-time-next').click();
  assert.equal(Number(await page.locator('.date-time-choice').first().getAttribute('data-date-value')), firstYear+12);
  await page.locator('#date-time-prev').click();
  assert.equal(Number(await page.locator('.date-time-choice').first().getAttribute('data-date-value')), firstYear);
  await choice("2027").click(); assert.equal(await page.locator("#date-time-month").innerText(), "5月");
  assert.equal(await page.locator("#policy-expires").inputValue(), "2026-11-19 09:30:27");
  await page.locator('.date-time-day[data-date-value="2027-05-17"]').click();
  for (const [part,value] of [["hour","14"],["minute","25"],["second","37"]]) await page.locator("#date-time-"+part).fill(value);
  await page.locator("#date-time-apply").click();
  assert.equal(await page.locator("#policy-expires").inputValue(), "2027-05-17 14:25:37");
  checks.push("modal interaction, month/year navigation, stable height, single/double arrows, year paging retains month, seconds committed only on confirmation");

  await page.locator("#gitlab-backfill-start-toggle").click();
  assert.equal(await page.locator("#date-time-zone").isVisible(), false);
  assert.equal(await page.locator('#date-time-next').isDisabled(), true);
  await page.locator('#date-time-month').click(); assert.equal(await page.locator('#date-time-next').isDisabled(), true);
  await page.locator('#date-time-year').click(); assert.equal(await page.locator('#date-time-next').isDisabled(), true);
  await page.locator('#date-time-year').click();
  assert.equal(await page.locator('.date-time-day[data-date-value="2026-11-20"]').isDisabled(), true);
  await page.locator("#date-time-second").fill("28"); assert.equal(await page.locator("#date-time-apply").isDisabled(), true);
  await page.locator("#date-time-second").fill("27"); assert.equal(await page.locator("#date-time-apply").isDisabled(), false);
  await page.locator("#date-time-second").fill("");
  await page.locator("#date-time-prev").click(); assert.equal(await page.locator("#date-time-second").inputValue(), "");
  assert.equal(await page.locator("#date-time-apply").isDisabled(), true);
  await page.keyboard.press("Escape"); assert.equal(await panel.isVisible(), false);
  assert.equal(await page.locator("#picker-test-surface").evaluate(element=>element.open), true);
  assert.equal(await page.locator("#gitlab-backfill-start-toggle").evaluate(element=>document.activeElement===element), true);
  checks.push("notification future limit includes seconds; incomplete drafts and Escape preserve parent dialog");

  await page.locator("#calendar-start-date-toggle").click();
  await page.keyboard.press("ArrowRight"); await page.keyboard.press("Enter");
  assert.equal(await page.locator("#calendar-start-date").inputValue(), "2028-03-01");
  await page.locator("#calendar-local-time-toggle").click();
  assert.equal(await page.locator("#date-time-zone").isVisible(), false);
  await page.locator('#date-time-cancel').click();
  await page.locator('#calendar-time-zone').evaluate(input=>{input.value='UTC';});
  await page.locator('#calendar-local-time-toggle').click();
  assert.equal(await page.locator('#date-time-zone').innerText(), '时区：UTC');
  assert.equal(await page.locator("#date-time-second-field").isVisible(), false);
  await page.locator("#date-time-hour").fill("14"); await page.locator("#date-time-minute").fill("5"); await page.keyboard.press("Enter");
  assert.equal(await page.locator("#calendar-local-time").inputValue(), "14:05");
  checks.push("keyboard date navigation, leap day, minute-only task clock");

  for (const width of [390, 320]) {
    await page.setViewportSize({width,height:844});
    await page.locator("#policy-expires-toggle").click(); await visible(); await shot(`mobile-${width}-date-time`);
    await page.locator("#date-time-year").click(); await visible(); await shot(`mobile-${width}-years`);
    await choice("2027").click(); await page.locator("#date-time-month").click(); await visible(); await shot(`mobile-${width}-months`);
    assert.equal(await page.locator('#date-time-next .date-time-double').isVisible(), true);
    await page.locator('#date-time-next').click(); assert.equal(await page.locator('#date-time-year').innerText(), '2028年');
    assert.equal(await page.locator('#date-time-month').innerText(), '5月');
    await page.locator('#date-time-prev').click(); assert.equal(await page.locator('#date-time-year').innerText(), '2027年');
    await page.locator("#date-time-cancel").click();
    assert.equal(await page.locator("#policy-expires").inputValue(), "2027-05-17 14:25:37");
  }
  checks.push("390px and 320px viewports, date/month/year layout, cancellation");
  await page.locator("#policy-expires").focus(); await page.keyboard.press("Alt+ArrowDown");
  await page.locator("#date-time-clear").click(); assert.equal(await page.locator("#policy-expires").inputValue(), "");
  await page.locator("#calendar-start-date-toggle").click();
  await page.locator("#calendar-start-date").evaluate(input=>{input.disabled=true;});
  await panel.waitFor({state:"hidden"}); assert.equal(await page.locator("#calendar-start-date-toggle").isDisabled(), true);
  checks.push("Alt+Down, optional clearing and disabled-field lifecycle");
  assert.deepEqual(errors, []);
  const result = {status:"passed",kind:"production component in Chromium; simulated mobile viewport",checks,errors};
  await writeFile(resolve(output, "evidence.json"), JSON.stringify(result,null,2)+"\n");
  console.log(JSON.stringify(result));
} catch (error) {
  for (const page of browser.contexts().flatMap(context=>context.pages())) await page.screenshot({path:resolve(output,"failure.png")});
  throw error;
} finally {
  await browser.close(); await new Promise(resolve=>server.close(resolve));
}
