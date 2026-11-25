import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as p from 'node:path';
import { fileURLToPath } from 'node:url';
import { assembleSRT, checkSRTLock, checkSRTArchive, checkSRTHosts, includesSRT } from './package-srt.mjs';
import { hash, inventory, json, writeJSON } from './package-lib.mjs';
import { preparedDependencies } from './package-cache.mjs';

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

test('SRT dependency cache builds and reuses its contract before public asset assembly', async t => {
  const directory = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx SRT dependency assembly '));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const cache = p.join(directory, 'cache'), buildHome = p.join(directory, 'build-home');
  fs.mkdirSync(buildHome);
  let builds = 0;
  const identity = { target: 'linux-amd64', profile: 'standard', test: 'SRT assembly before public assets' };
  const prepare = name => {
    const bundle = p.join(directory, name);
    return preparedDependencies({ cache, identity, destination: bundle,
      build: () => {
        builds++;
        const node = p.join(bundle, 'runtime/node/bin/node');
        fs.mkdirSync(p.dirname(node), { recursive: true });
        // Substitute only npm execution. The real SRT assembly, lock checks,
        // output writing, inventory and prepared-cache round trip all run.
        fs.writeFileSync(node, '#!/bin/sh\nset -eu\ncase "$1" in\n */npm-cli.js) test "$2" = ci;;\n *) exit 2;;\nesac\nmkdir -p node_modules\nprintf synthetic > node_modules/fixture\nprintf "npm-ci\\n" >> "$HOME/npm-invocations"\n', { mode: 0o755 });
        assert.equal(fs.existsSync(p.join(bundle, 'share')), false);
        assert.equal(fs.existsSync(p.join(bundle, 'THIRD_PARTY_NOTICES')), false);
        return { srt: assembleSRT({ root: source, bundle, target: 'linux-amd64', profile: 'standard', cache, buildHome, offline: true }) };
      },
      validate: (bundle, metadata) => {
        assert.deepEqual(json(p.join(bundle, metadata.srt.contract)), checkSRTLock(source));
        assert.equal(metadata.srt.runtime_sha256, hash(JSON.stringify(inventory(p.join(bundle, 'runtime/srt')))));
        assert.ok(json(p.join(bundle, 'THIRD_PARTY_NOTICES/srt-dependencies.json')).packages.length > 0);
      }
    });
  };
  const cold = await prepare('cold');
  fs.writeFileSync(p.join(directory, 'cold/share/teamx/public-asset'), 'separate assembly step');
  assert.deepEqual(await prepare('warm'), cold);
  assert.equal(builds, 1);
  assert.equal(fs.readFileSync(p.join(buildHome, 'npm-invocations'), 'utf8'), 'npm-ci\n');
  assert.equal(fs.existsSync(p.join(directory, 'warm/share/teamx/public-asset')), false);
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
