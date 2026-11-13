// Archive + real Chromium lifecycle experiment. Model execution uses a gated
// ACP fixture; no platform credentials, network model, or external writes.
import * as fs from 'node:fs';
import * as p from 'node:path';
import * as os from 'node:os';
import { createRequire } from 'node:module';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { parseArgs } from 'node:util';
import { fileURLToPath } from 'node:url';
import { extract, hash, inventory, json, must, run } from './package-lib.mjs';

const { values } = parseArgs({ options: {
  archive: { type: 'string' }, output: { type: 'string' }, verifier: { type: 'string' },
  bundle: { type: 'string' }, channel: { type: 'string', default: 'chrome' }
} });
must(values.archive && values.output && values.verifier,
  'server-background-probe.mjs --archive FILE --output NEW_DIR --verifier PLAYWRIGHT_PACKAGE_DIR');
const archive = p.resolve(values.archive), output = p.resolve(values.output);
fs.mkdirSync(output, { recursive: false, mode: 0o700 });
const report = { schema_version: 1, status: 'failed', checks: [],
  recorded_at: new Date().toISOString(), archive_bytes: fs.statSync(archive).size,
  driver_sha256: hash(fs.readFileSync(fileURLToPath(import.meta.url))),
  host: { platform: process.platform, arch: process.arch, release: os.release() },
  archive_sha256: hash(fs.readFileSync(archive)),
  model: 'gated ACP fixture; not real DSH/model acceptance',
  platforms: 'not_run', mobile: 'not_run', remote_https: 'not_run' };
let service, exited, browser, stdout = '', stderr = '';
const check = (name, condition) => {
  must(condition, name); report.checks.push(name);
};
async function until(fn, label) {
  const deadline = Date.now() + 30000;
  while (Date.now() < deadline) {
    if (await fn()) return;
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  throw Error(`Timed out: ${label}`);
}
async function stop() {
  if (!service) return;
  service.kill('SIGTERM');
  let timer;
  try {
    const [code] = await Promise.race([exited, new Promise((_, reject) => {
      timer = setTimeout(() => reject(Error('Server shutdown timed out')), 35000);
    })]);
    service = undefined;
    must(code === 0, 'Server did not shut down normally');
  } finally { clearTimeout(timer); }
}
try {
  const { chromium } = createRequire(p.join(p.resolve(values.verifier), 'package.json'))('playwright');
  let bundle;
  if (values.bundle) {
    bundle = fs.realpathSync(p.resolve(values.bundle));
    must(JSON.stringify(JSON.parse(run('tar', ['-xOf', archive, `${p.basename(bundle)}/checksums.txt`]))) ===
      JSON.stringify(json(p.join(bundle, 'checksums.txt'))), 'Reused bundle differs from archive');
  }
  else {
    const extracted = p.join(output, 'releases');
    extract(archive, extracted);
    const entries = fs.readdirSync(extracted);
    must(entries.length === 1, 'Archive must contain one root');
    bundle = fs.realpathSync(p.join(extracted, entries[0]));
  }
  const manifest = json(p.join(bundle, 'manifest.json'));
  must(manifest.profile === 'standard', 'Probe requires standard');
  must(manifest.target === `${process.platform}-${process.arch === 'x64' ? 'amd64' : process.arch}`,
    'Use a native archive');
  check('archive contents and modes match checksums',
    JSON.stringify(inventory(bundle).filter(f => f.path !== 'checksums.txt')) ===
    JSON.stringify(json(p.join(bundle, 'checksums.txt'))));
  report.source = manifest.source;
  report.dependencies = Object.fromEntries(Object.entries(manifest.dependencies).map(([k, v]) => [k, v.version]));
  report.version_path = bundle;
  report.version_file_bytes = inventory(bundle).reduce((sum, f) =>
    sum + (f.kind === 'file' ? fs.statSync(p.join(bundle, f.path)).size : 0), 0);
  const home = p.join(output, '用户数据 home'), cwd = p.join(output, 'other cwd');
  fs.mkdirSync(cwd);
  const log = p.join(output, 'acp.jsonl'), release = p.join(output, 'release-prompt');
  const env = { PATH: `${bundle}/bin:${bundle}/runtime/node/bin:/usr/bin:/bin`,
    GLAB_CONFIG_DIR: p.join(output, 'glab-empty'),
    LANG: 'en_US.UTF-8', OPENAI_API_KEY: 'background-fixture-only',
    TEAMX_BACKGROUND_LOG: log, TEAMX_BACKGROUND_RELEASE: release };
  run(p.join(bundle, 'start'), ['init', '--non-interactive', '--home', home], { cwd, env });
  const fixture = fileURLToPath(new URL('./fixtures/background-acp.mjs', import.meta.url));
  report.fixture_sha256 = hash(fs.readFileSync(fixture));
  const wrapper = p.join(output, 'fixture-dsh');
  const quote = value => "'" + value.replaceAll("'", "'\\''") + "'";
  fs.writeFileSync(wrapper, `#!/bin/sh\nif [ "\${1:-}" = --version ]; then echo 0.2.2-rc.1; exit 0; fi\nexec ${quote(p.join(bundle, 'runtime/node/bin/node'))} ${quote(fixture)} "$@"\n`, { mode: 0o700 });
  const config = p.join(home, 'config.toml');
  fs.writeFileSync(config, fs.readFileSync(config, 'utf8')
    .replace(/listen = [^\n]+/, "listen = '127.0.0.1:0'")
    .replace(/binary_path = [^\n]+/, `binary_path = ${JSON.stringify(wrapper)}`)
    .replace("base_url = ''", "base_url = 'http://127.0.0.1:1/v1'")
    .replace("model_id = ''", "model_id = 'background-fixture'"), { mode: 0o600 });
  run(p.join(bundle, 'bin/teamx'), ['access', 'init', '--config', config], {cwd, env});
  const adminToken = fs.readFileSync(p.join(home, 'auth/admin-token'), 'utf8').trim();
  const started = Date.now();
  service = spawn(p.join(bundle, 'start'), ['--home', home], {
    cwd, env, detached: true, stdio: ['ignore', 'pipe', 'pipe'] });
  exited = once(service, 'exit');
  service.stdout.on('data', b => { stdout = (stdout + b).slice(-65536); });
  service.stderr.on('data', b => { stderr = (stderr + b).slice(-65536); });
  let ready;
  await until(() => {
    must(service.exitCode === null && service.signalCode === null, 'Server exited before ready');
    for (const line of stdout.split('\n')) {
      try { const value = JSON.parse(line); if (value.endpoint) ready = value; } catch {}
    }
    return ready;
  }, 'readiness');
  report.ready_ms = Date.now() - started;
  browser = await chromium.launch({ headless: true, channel: values.channel });
  report.browser_channel = values.channel;
  report.browser = browser.version();
  const page = await browser.newPage();
  await page.goto(ready.endpoint);
  await page.locator('#token').fill(adminToken);
  await page.locator('#login-form button').click();
  await page.waitForURL(ready.endpoint + '/');
  // The normal first-run path creates and opens the default assistant.
  await page.locator('#message-input').waitFor({ state: 'visible' });
  await page.locator('#message-input').fill('Local lifecycle fixture only');
  await page.locator('#send').click();
  const events = () => fs.existsSync(log) ? fs.readFileSync(log, 'utf8').trim().split('\n').map(JSON.parse) : [];
  await until(() => events().some(e => e.method === 'session/prompt'), 'prompt started');
  await page.screenshot({ path: p.join(output, 'before-close.png') });
  await browser.close(); browser = undefined;
  report.browser_closed_at = Date.now();
  check('prompt is still gated when all browser windows close', !events().some(e => e.method === 'prompt_completed'));
  fs.writeFileSync(release, 'release after browser closed\n');
  // No UI polling or further HTTP requests occur before fixture completion.
  await until(() => events().some(e => e.method === 'prompt_completed'), 'background completion');
  check('execution completes after browser exit with stdin closed', events().some(e =>
    e.method === 'prompt_completed' && e.at >= report.browser_closed_at) && service.exitCode === null);
  const roomsDir = p.join(home, 'data/rooms');
  await until(() => fs.readdirSync(roomsDir, { recursive: true })
    .filter(name => name.endsWith('/agent.json'))
    .some(name => json(p.join(roomsDir, name)).turn?.status === 'completed'), 'durable completed turn');
  check('Server persists completed turn without browser polling', true);
  report.server_rss_kib = Number(run('/bin/ps', ['-o', 'rss=', '-p', String(service.pid)]).trim());
  await stop();
  check('normal stop closes ACP session', events().some(e => e.method === 'session/close'));
  check('owned ACP processes exit', [...new Set(events().map(e => e.pid))].every(pid => {
    try { process.kill(pid, 0); return false; } catch (error) { return error.code === 'ESRCH'; }
  }));
  fs.writeFileSync(p.join(output, 'server.stderr.log'), stderr);
  report.status = 'passed';
} catch (error) {
  report.error = error.message;
  throw error;
} finally {
  try { if (browser) await browser.close(); await stop(); }
  finally {
    if (service) { try { process.kill(-service.pid, 'SIGKILL'); } catch {} }
    fs.writeFileSync(p.join(output, 'report.json'), JSON.stringify(report, null, 2) + '\n');
    fs.writeFileSync(p.join(output, 'server.stderr.log'), stderr);
    fs.writeFileSync(p.join(output, 'server.stdout.log'), stdout);
  }
}
console.log(JSON.stringify({ status: report.status, checks: report.checks,
  archive_sha256: report.archive_sha256, browser: report.browser,
  ready_ms: report.ready_ms, server_rss_kib: report.server_rss_kib,
  report: p.join(output, 'report.json') }, null, 2));
