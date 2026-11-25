import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as p from 'node:path';
import { fileURLToPath } from 'node:url';
import { checkSRTLock, checkSRTArchive, checkSRTHosts, includesSRT } from './package-srt.mjs';
import { hash, inventory, json, writeJSON } from './package-lib.mjs';

const source = p.resolve(p.dirname(fileURLToPath(import.meta.url)), '..');

test('SRT is limited to Linux amd64 standard and shares the exact execution lock', () => {
  assert.equal(includesSRT('linux-amd64', 'standard'), true);
  for (const [target, profile] of [['linux-arm64', 'standard'], ['darwin-arm64', 'standard'], ['linux-amd64', 'core'], ['linux-amd64', 'full']])
    assert.equal(includesSRT(target, profile), false);
  assert.equal(checkSRTLock(source).version, '0.0.83');
});

test('each supported host has a complete independent pinned tool set', () => {
  const lock = checkSRTLock(source);
  assert.equal(lock.system.version_id, '24.04');
  assert.equal(lock.additional_hosts[0].system.version_id, '22.04');
  for (const mutate of [
    c => c.additional_hosts[0].host_tools.pop(),
    c => c.additional_hosts[0].host_tools[0].binary_sha256 = '',
    c => c.additional_hosts[0].host_tools[0].path = '/usr/bin/rg',
    c => c.additional_hosts.push(c.additional_hosts[0])
  ]) {
    const copy = structuredClone(lock); mutate(copy);
    assert.throws(() => checkSRTHosts(copy), /SRT host/);
  }
});

test('archive rejects missing, mismatched or modified SRT resources before launch', t => {
  const bundle = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-srt-archive-'));
  t.after(() => fs.rmSync(bundle, { recursive: true, force: true }));
  const runtime = p.join(bundle, 'runtime/srt');
  fs.mkdirSync(runtime, { recursive: true });
  fs.mkdirSync(p.join(bundle, 'share/teamx'), { recursive: true });
  const lock = json(p.join(source, 'packaging/srt/runtime.lock.json'));
  fs.copyFileSync(p.join(source, lock.npm_lock), p.join(runtime, 'package-lock.json'));
  writeJSON(p.join(bundle, 'share/teamx/srt-runtime.json'), lock);
  const manifest = { target: 'linux-amd64', profile: 'standard' };
  assert.throws(() => checkSRTArchive(bundle, manifest), /Missing SRT/);
  manifest.execution_runtimes = { srt: { version: lock.version, npm_lock_sha256: lock.npm_lock_sha256,
    contract: 'share/teamx/srt-runtime.json', default_execution: 'native', runtime_sha256: hash(JSON.stringify(inventory(runtime))) } };
  fs.writeFileSync(p.join(runtime, 'extra'), 'unreviewed');
  assert.throws(() => checkSRTArchive(bundle, manifest), /digest mismatch/);
  fs.rmSync(p.join(runtime, 'extra'));
  fs.writeFileSync(p.join(runtime, 'package-lock.json'), '{}');
  assert.throws(() => checkSRTArchive(bundle, manifest), /digest mismatch/);
  assert.throws(() => checkSRTArchive(bundle, { ...manifest, target: 'darwin-arm64' }), /Unexpected SRT/);
});
