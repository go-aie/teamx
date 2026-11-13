import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import http from 'node:http';
import { collectSample, observationEndpoint, observationSummary, observe } from './server-observation.mjs';
import { hash, inventory, writeJSON, run, json } from './package-lib.mjs';

test('observer permits canonical HTTPS/loopback origins and refuses credential-bearing or insecure remote URLs', () => {
  for (const u of ['https://assistant.example', 'http://127.0.0.1:8088', 'http://[::1]:8088']) assert.equal(observationEndpoint(u), u);
  for (const u of ['http://192.168.1.2', 'https://user:token@example.com', 'https://example.com/?token=x',
    'https://example.com/#token', 'https://example.com/subpath']) assert.throws(() => observationEndpoint(u));
});

test('sampling authenticates GETs, checks installation/generation and projects diagnostics without message/config secrets', async () => {
  const calls = [];
  const values = [{ installation_id: 'instance', operator: 'owner' }, { service_epoch: 'epoch', rooms: [
    { status: 'ready', room_id: 'room', room_instance: 'identity' }] },
  { service_epoch: 'epoch', operations: [{ id: 'private', status: 'completed', message: 'private message' }], catalog: { secret: true } },
  { subscriptions: [{ subscription_id: 'private-source', provider: 'gitlab', channel: 'events', pending: 3,
    config: 'private config', metrics: { requests: 12 } }] }];
  const fetcher = async (url, options) => {
    calls.push([url, options]); return { ok: true, json: async () => values[calls.length - 1] };
  };
  const result = await collectSample({ endpoint: 'https://assistant.example', token: 'PRIVATE-TOKEN', installation: 'instance', fetcher });
  assert.equal(result.subscriptions[0].pending, 3);
  assert.equal(result.subscriptions[0].source_sha256.length, 64);
  assert.ok(!JSON.stringify(result).includes('private'));
  assert.ok(calls.every(([, o]) => o.method === 'GET' && o.redirect === 'error' && o.headers.Authorization === 'Bearer PRIVATE-TOKEN'));
  const q = new URL(calls[3][0]).searchParams;
  assert.equal(q.get('service_epoch'), 'epoch'); assert.equal(q.get('room_instance'), 'identity');
  await assert.rejects(collectSample({ endpoint: 'https://assistant.example', token: 'x', installation: 'other', fetcher:
    async () => ({ ok: true, json: async () => values[0] }) }), /installation mismatch/);
});

test('observation counts failures and frozen sampling gaps without promoting short runs', () => {
  const samples = [{ at: '2026-01-01T00:00:00Z', status: 'ok' },
    { at: '2026-01-02T00:00:00Z', status: 'unavailable' }];
  const summary = observationSummary(samples, 86400, 60);
  assert.equal(summary.duration_met, true); assert.equal(summary.sampling_continuity_met, false);
  assert.equal(summary.failed_samples, 1); assert.equal(summary.subscriptions_observed, false);
  assert.equal(observationSummary(samples.slice(0, 1), 60, 60).duration_met, false);
});

test('short installed archive observation writes private reports and remains incomplete; mismatched payload is rejected', async t => {
  const temp = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'teamx-observation-')));
  t.after(() => fs.rmSync(temp, { recursive: true, force: true }));
  const root = path.join(temp, 'install'), release = path.join(root, 'releases', 'candidate');
  fs.mkdirSync(release, { recursive: true });
  fs.symlinkSync('releases/candidate', path.join(root, 'current'));
  writeJSON(path.join(root, 'installation.json'), { server_root: root, instance_id: 'instance' });
  writeJSON(path.join(release, 'manifest.json'), { profile: 'standard', target: 'darwin-arm64',
    server_deployment: { model_delivery_sha256: hash('synthetic') } });
  writeJSON(path.join(release, 'checksums.txt'), inventory(release));
  const archive = path.join(temp, 'candidate.tar.gz');
  run('tar', ['-czf', archive, '-C', path.dirname(release), 'candidate']);
  const token = path.join(temp, 'token'); fs.writeFileSync(token, 'PRIVATE-TOKEN-0123456789', { mode: 0o600 });
  const requests = [];
  const server = http.createServer((req, res) => {
    requests.push(req.method);
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify(req.url === '/v1/access/session' ? { operator: 'owner', installation_id: 'instance' } :
      req.url === '/v1/rooms' ? { service_epoch: 'epoch', rooms: [] } : { service_epoch: 'epoch', operations: [] }));
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => server.close(resolve)));
  const output = path.join(temp, 'observation');
  const opts = { archive, root, token, output, endpoint: `http://127.0.0.1:${server.address().port}`, duration: 1, interval: 1 };
  const report = await observe(opts);
  assert.equal(report.status, 'incomplete'); assert.equal(Object.hasOwn(report, 'release_ready'), false);
  assert.ok(report.summary.successful_samples >= 2); assert.ok(requests.every(m => m === 'GET'));
  const samples = fs.readFileSync(path.join(output, 'samples.jsonl'), 'utf8').trim().split('\n').map(line => JSON.parse(line));
  assert.equal(report.started_at, samples[0].at);
  assert.equal(report.finished_at, samples.at(-1).at);
  assert.equal(report.summary.duration_seconds, (Date.parse(samples.at(-1).at) - Date.parse(samples[0].at)) / 1000);
  assert.ok(Date.parse(report.verified_at) >= Date.parse(report.finished_at));
  for (const file of ['report.json', 'samples.jsonl']) {
    assert.equal(fs.statSync(path.join(output, file)).mode & 0o777, 0o600);
    assert.ok(!fs.readFileSync(path.join(output, file), 'utf8').includes('PRIVATE-TOKEN'));
  }
  fs.writeFileSync(path.join(release, 'extra'), 'changed installed payload');
  const bad = path.join(temp, 'mismatch');
  await assert.rejects(observe({ ...opts, output: bad }), /payload differs/);
  assert.equal(json(path.join(bad, 'report.json')).status, 'failed');
});
