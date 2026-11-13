// Read-only deployment observation. No login, model validation, message sends,
// platform actions or retries of writes. Private reports require human review.
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { extract, hash, inventory, json, must, writeJSON } from './package-lib.mjs';

export function observationEndpoint(value) {
  const u = new URL(value);
  must(!u.username && !u.password && !u.search && !u.hash && u.pathname === '/' &&
    (u.protocol === 'https:' || u.protocol === 'http:' && ['127.0.0.1', '[::1]', 'localhost'].includes(u.hostname)),
  'Use a canonical HTTPS origin or loopback HTTP origin');
  return u.origin;
}

export async function collectSample({ endpoint, token, installation, fetcher = fetch, signal }) {
  const read = async route => {
    const r = await fetcher(endpoint + route, { method: 'GET', redirect: 'error',
      headers: { Authorization: `Bearer ${token}` }, signal: signal ?? AbortSignal.timeout(10000) });
    must(r.ok, `Observation GET ${route.split('?')[0]}: HTTP ${r.status}`);
    return r.json();
  };
  const session = await read('/v1/access/session');
  must(session.installation_id === installation && typeof session.operator === 'string', 'Observation installation mismatch');
  const catalog = await read('/v1/rooms');
  must(typeof catalog.service_epoch === 'string' && Array.isArray(catalog.rooms), 'Observation catalog unavailable');
  const models = await read('/v1/models');
  must(models.service_epoch === catalog.service_epoch && Array.isArray(models.operations), 'Observation generation changed during sample');
  const subscriptions = [];
  for (const room of catalog.rooms.filter(r => r.status === 'ready')) {
    const query = new URLSearchParams({ service_epoch: catalog.service_epoch, caller_id: session.operator,
      room_id: room.room_id, room_instance: room.room_instance });
    const view = await read('/v1/platform/notifications?' + query);
    must(Array.isArray(view.subscriptions), 'Observation subscriptions unavailable');
    for (const s of view.subscriptions) subscriptions.push({
      source_sha256: hash(JSON.stringify([room.room_id, room.room_instance, s.subscription_id, s.provider, s.channel])),
      provider: s.provider, channel: s.channel, status: s.status, pending: s.pending,
      last_check: s.last_check, retry_at: s.retry_at, error_code: s.error_code,
      delivery_error_code: s.delivery_error_code, metrics: s.metrics });
  }
  return { service_epoch: catalog.service_epoch, rooms: catalog.rooms.length,
    model_operations: models.operations.map(o => ({ status: o.status, waiting: o.waiting, failed: o.failed, completed: o.completed })),
    subscriptions };
}

export function observationSummary(samples, elapsedSeconds, intervalSeconds) {
  const gaps = samples.slice(1).map((s, i) => (Date.parse(s.at) - Date.parse(samples[i].at)) / 1000);
  return { duration_seconds: elapsedSeconds, samples: samples.length,
    successful_samples: samples.filter(s => s.status === 'ok').length,
    failed_samples: samples.filter(s => s.status !== 'ok').length,
    max_gap_seconds: gaps.length ? Math.max(...gaps) : 0,
    duration_met: elapsedSeconds >= 86400,
    sampling_continuity_met: samples.length > 1 && gaps.every(g => g > 0 && g <= Math.max(180, intervalSeconds * 3)),
    subscriptions_observed: samples.some(s => s.subscriptions?.length > 0) };
}

export async function observe(options) {
  const endpoint = observationEndpoint(options.endpoint);
  const duration = Number(options.duration ?? 86400), interval = Number(options.interval ?? 60);
  must(Number.isInteger(duration) && duration > 0 && Number.isInteger(interval) && interval > 0 && interval <= 60,
    'Duration must be positive seconds; interval must be 1..60 seconds');
  const tokenInfo = fs.lstatSync(options.token);
  must(tokenInfo.isFile() && (tokenInfo.mode & 0o777) === 0o600 && tokenInfo.uid === process.getuid(),
    'Token must be a private 0600 regular file owned by this user');
  const token = fs.readFileSync(options.token, 'utf8').trim();
  must(/^[\x21-\x7e]{16,256}$/.test(token), 'Invalid management token file');
  process.umask(0o077);
  fs.mkdirSync(options.output, { mode: 0o700 });
  const reportFile = path.join(options.output, 'report.json'), samples = [];
  const report = { schema_version: 1, status: 'incomplete',
    scope: 'authenticated read-only resident observation; review collection/backlog/rate-limit/recovery separately',
    archive_sha256: hash(fs.readFileSync(options.archive)), environment: { platform: process.platform,
      arch: process.arch, machine: os.machine(), release: os.release() },
    verifier_sha256: hash(fs.readFileSync(fileURLToPath(import.meta.url))), remote_writes: 0 };
  let interrupted = false;
  const controller = new AbortController();
  const stop = () => { interrupted = true; controller.abort(); };
  process.once('SIGINT', stop); process.once('SIGTERM', stop);
  let current, expected, started;
  const save = () => writeJSON(reportFile, report);
  try {
    const registration = json(path.join(options.root, 'installation.json'));
    must(registration.server_root === fs.realpathSync(options.root) && registration.instance_id, 'Invalid installation registration');
    current = fs.realpathSync(path.join(options.root, 'current'));
    must(current.startsWith(fs.realpathSync(path.join(options.root, 'releases')) + path.sep), 'Current release escapes installation');
    const temp = path.join(options.output, 'extracted');
    try {
      extract(options.archive, temp);
      const roots = fs.readdirSync(temp); must(roots.length === 1, 'Expected one archive root');
      const bundle = path.join(temp, roots[0]), manifest = json(path.join(bundle, 'manifest.json'));
      must(manifest.profile === 'standard' && manifest.server_deployment, 'Expected resident standard archive');
      expected = json(path.join(bundle, 'checksums.txt'));
      must(JSON.stringify(inventory(bundle).filter(f => f.path !== 'checksums.txt')) === JSON.stringify(expected), 'Archive inventory mismatch');
      report.target = manifest.target; report.source = manifest.source; report.dependencies = manifest.dependencies;
      report.manifest_sha256 = hash(fs.readFileSync(path.join(bundle, 'manifest.json')));
      report.model_delivery_sha256 = manifest.server_deployment.model_delivery_sha256;
    } finally { fs.rmSync(temp, { recursive: true, force: true }); }
    const unchanged = () => must(fs.realpathSync(path.join(options.root, 'current')) === current &&
      JSON.stringify(inventory(current).filter(f => f.path !== 'checksums.txt')) === JSON.stringify(expected) &&
      hash(fs.readFileSync(path.join(current, 'manifest.json'))) === report.manifest_sha256, 'Installed payload differs from observed archive');
    unchanged();
    report.installation_sha256 = hash(registration.instance_id);
    started = performance.now(); report.started_at = new Date().toISOString(); save();
    while (!interrupted) {
      const elapsedAtSample = (performance.now() - started) / 1000;
      const sample = { at: new Date().toISOString(), status: 'ok' };
      try {
        must(fs.realpathSync(path.join(options.root, 'current')) === current, 'Selected archive changed');
        Object.assign(sample, await collectSample({ endpoint, token, installation: registration.instance_id,
          signal: AbortSignal.any([controller.signal, AbortSignal.timeout(45000)]) }));
      } catch (error) {
        // Never serialize fetch errors, request headers or response bodies.
        sample.status = 'unavailable';
      }
      samples.push(sample);
      fs.appendFileSync(path.join(options.output, 'samples.jsonl'), JSON.stringify(sample) + '\n', { mode: 0o600 });
      report.summary = observationSummary(samples, (performance.now() - started) / 1000, interval); save();
      // The last sampling point, rather than request/verification overhead,
      // must reach the requested observation window.
      if (elapsedAtSample >= duration) break;
      await delay(Math.min(interval * 1000, Math.max(1, duration * 1000 - (performance.now() - started))), undefined,
        { signal: controller.signal }).catch(() => {});
    }
    unchanged();
    report.started_at = samples[0].at;
    report.finished_at = samples.at(-1).at;
    report.verified_at = new Date().toISOString();
    report.summary = observationSummary(samples, (Date.parse(report.finished_at) - Date.parse(report.started_at)) / 1000, interval);
    report.samples_sha256 = hash(fs.readFileSync(path.join(options.output, 'samples.jsonl')));
    report.status = interrupted ? 'interrupted' : report.summary.duration_met && report.summary.sampling_continuity_met ?
      'completed_pending_review' : 'incomplete';
  } catch (error) { report.status = 'failed'; report.error = 'Observation setup or installed payload verification failed'; throw error; }
  finally { process.removeListener('SIGINT', stop); process.removeListener('SIGTERM', stop); save(); }
  return report;
}

if (process.argv[1] && fs.realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const usage = 'server-observation.mjs --archive FILE --server-root DIR --endpoint ORIGIN --token-file FILE --output-dir NEW_DIR [--duration-seconds 86400] [--interval-seconds 60]';
  try {
    const args = process.argv.slice(2), opts = {}, names = { '--archive': 'archive', '--server-root': 'root',
      '--endpoint': 'endpoint', '--token-file': 'token', '--output-dir': 'output', '--duration-seconds': 'duration', '--interval-seconds': 'interval' };
    if (args.length === 1 && args[0] === '--help') console.log(usage);
    else {
      while (args.length) { const key = args.shift(); must(names[key] && args.length && !opts[names[key]], usage); opts[names[key]] = args.shift(); }
      must(['archive', 'root', 'endpoint', 'token', 'output'].every(k => opts[k]), usage);
      const report = await observe(opts);
      console.log(`Observation: ${report.status}`);
      process.exitCode = report.status === 'completed_pending_review' ? 0 : 2;
    }
  } catch { console.error('Observation failed; inspect private report and command arguments.'); process.exitCode = 1; }
}
