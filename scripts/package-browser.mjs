import * as fs from 'node:fs';
import * as p from 'node:path';
import { createRequire } from 'node:module';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { hash, must, run } from './package-lib.mjs';

async function until(fn, name) {
  const deadline = Date.now() + 30000;
  while (Date.now() < deadline) {
    const result = await fn();
    if (result) return result;
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  throw Error(`Browser acceptance timed out: ${name}`);
}

export async function acceptBrowser({ bundle, output, env }) {
  // Playwright is a verifier dependency, never a runtime or bundled asset.
  const { chromium } = createRequire(import.meta.url)('playwright');
  const home = p.join(output, 'user home'), cwd = p.join(output, 'other cwd');
  fs.mkdirSync(cwd);
  const start = p.join(bundle, 'start');
  const init = () => run(start, ['init', '--home', home], { env, cwd, timeout: 30000 });
  init();
  const config = p.join(home, 'config.toml');
  fs.writeFileSync(config, fs.readFileSync(config, 'utf8')
    .replace(/listen = [^\n]+/, "listen = '127.0.0.1:0'")
    .replace("base_url = ''", "base_url = 'http://127.0.0.1:1/v1'")
    .replace("model_id = ''", "model_id = 'browser-no-prompt'"), { mode: 0o600 });
  const template = p.join(home, 'teams/bookstore-night/prompts/alan.md');
  fs.appendFileSync(template, '\nP3 user customization\n');
  const before = [config, template].map(file => hash(fs.readFileSync(file)));
  init();
  must([config, template].every((file, i) => hash(fs.readFileSync(file)) === before[i]),
    'Browser home init overwrote user settings');
  let service, browser;
  const errors = [];
  let catalogBusyRetries = 0;
  const readCatalog = endpoint => until(async () => {
    const response = await fetch(endpoint + '/v1/rooms', { signal: AbortSignal.timeout(3000) });
    const catalog = await response.json();
    if (response.status === 409 && catalog.code === 'busy') {
      catalogBusyRetries++;
      return false;
    }
    must(response.ok && Array.isArray(catalog.rooms),
      `Browser Room catalog failed: HTTP ${response.status}, ${catalog.code || 'invalid response'}`);
    return catalog;
  }, 'Room catalog readiness');
  const launch = async () => {
    const child = spawn(start, ['--home', home], { cwd, env, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '', stderr = '';
    const exited = once(child, 'exit');
    // Attach the owner before readiness; failed launches still get cleanup.
    service = { child, exited, endpoint: undefined };
    child.stdout.on('data', b => { stdout = (stdout + b).slice(-65536); });
    child.stderr.on('data', b => { stderr = (stderr + b).slice(-65536); });
    child.on('error', () => {});
    const endpoint = await until(() => {
      must(child.exitCode === null && child.signalCode === null, 'Browser service exited before readiness');
      for (const line of stdout.split('\n')) {
        try { const item = JSON.parse(line); if (item.endpoint) return item.endpoint; } catch {}
      }
    }, 'service readiness');
    service.endpoint = endpoint;
    return endpoint;
  };
  const stop = async signal => {
    if (!service) return;
    const current = service;
    current.child.kill(signal);
    let timer;
    try {
      const [code] = await Promise.race([current.exited,
        new Promise((_, reject) => { timer = setTimeout(() => reject(Error('Browser service cleanup unconfirmed')), 35000); })]);
      must(code === 0, 'Browser service failed shutdown');
      if (current.endpoint) await until(async () => {
        try { await fetch(current.endpoint + '/v1/rooms', { signal: AbortSignal.timeout(300) }); return false; }
        catch { return true; }
      }, 'port release');
      service = undefined;
    } finally { clearTimeout(timer); }
  };
  try {
    const endpoint = await launch();
    browser = await chromium.launch({ headless: true });
    const page = await browser.newPage({ viewport: { width: 1360, height: 960 } });
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(endpoint);
    await page.locator('#new-room').waitFor({ state: 'visible' });
    await page.locator('#new-room').click();
    await page.locator('#team').selectOption('personal-assistant');
    await page.locator('#new-room-name').fill('P3 归档浏览器验收');
    await page.locator('#create-room').click();
    await page.locator('#room-name').filter({ hasText: 'P3 归档浏览器验收' }).waitFor();
    await page.locator('#message-input').waitFor({ state: 'visible' });
    await page.screenshot({ path: p.join(output, 'desktop.png'), fullPage: true });
    const catalog = await readCatalog(endpoint);
    const room = catalog.rooms.find(r => r.name === 'P3 归档浏览器验收');
    must(room?.room_id && room?.room_instance, 'Browser-created Room not persisted by production API');
    await page.setViewportSize({ width: 390, height: 900 });
    await page.screenshot({ path: p.join(output, 'mobile.png'), fullPage: true });
    must(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), 'Mobile page overflows');
    await page.close();
    await stop('SIGINT');
    const restarted = await launch();
    const after = await readCatalog(restarted);
    must(after.rooms.some(r => r.room_id === room.room_id && r.room_instance === room.room_instance), 'Browser Room lost across restart');
    const reopened = await browser.newPage();
    reopened.on('pageerror', error => errors.push(error.message));
    await reopened.goto(restarted);
    await reopened.locator('.room-choice').filter({ hasText: 'P3 归档浏览器验收' }).waitFor();
    await reopened.close();
    await stop('SIGTERM');
    init();
    must([config, template].every((file, i) => hash(fs.readFileSync(file)) === before[i]), 'Restart/init overwrote configuration or template');
    must(errors.length === 0, 'Browser JavaScript errors; see private evidence');
    return { status: 'passed', fixture: false, browser: 'Chromium ' + browser.version(),
      surface: 'final archive production UI and backend', room_id: room.room_id,
      checks: ['homepage', 'create/open Room', 'desktop/mobile', 'independent home',
        'configuration/template preservation', 'SIGINT/SIGTERM port release', 'restart persistence'],
      model_prompts: 0, remote_platform_writes: 0, page_errors: errors, catalog_busy_retries: catalogBusyRetries };
  } finally {
    if (browser) await browser.close();
    await stop('SIGTERM');
  }
}
