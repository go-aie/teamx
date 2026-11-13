import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as p from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { hash, run, writeJSON } from './package-lib.mjs';
import { packageOffline } from './package-offline.mjs';

const root = p.resolve(p.dirname(fileURLToPath(import.meta.url)), '..');

function fixture(t, profile = 'standard') {
  const temp = fs.realpathSync(fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-offline-test-')));
  t.after(() => fs.rmSync(temp, { recursive: true, force: true }));
  const name = `teamx-v1.2.3-linux-amd64-${profile}`, bundle = p.join(temp, name);
  fs.mkdirSync(p.join(bundle, 'bin'), { recursive: true });
  const manifest = { schema_version: 1, version: 'v1.2.3', target: 'linux-amd64', profile };
  writeJSON(p.join(bundle, 'manifest.json'), manifest);
  // Record wrapper forwarding without running a real deployment or networking.
  const installer = '#!/bin/bash\nset -eu\nprintf "%s\\n" "$@" > "$OFFLINE_TEST_RECORD"\n';
  fs.writeFileSync(p.join(bundle, 'install.sh'), installer, { mode: 0o755 });
  fs.writeFileSync(p.join(bundle, 'bin/teamx'), 'do not execute this fixture', { mode: 0o755 });
  const archive = p.join(temp, name + '.tar.gz'), output = p.join(temp, 'delivery', name + '-offline.zip');
  const digest = () => hash(fs.readFileSync(archive));
  const repack = () => {
    run('tar', ['-czf', archive, '-C', temp, name], { env: { ...process.env, COPYFILE_DISABLE: '1' } });
    fs.writeFileSync(archive + '.sha256', `${digest()}  ${p.basename(archive)}\n`);
  };
  repack();
  return { temp, name, bundle, archive, output, digest, repack, manifest, installer,
    build: options => packageOffline(archive, { output, ...options }) };
}

test('offline ZIP preserves the original archive and carries only the fixed installation files', t => {
  const f = fixture(t), before = fs.readFileSync(f.archive), receipt = f.build();
  assert.equal(receipt.archive_sha256, hash(before));
  assert.equal(receipt.sha256, hash(fs.readFileSync(f.output)));
  assert.equal(receipt.target, 'linux-amd64');
  assert.deepEqual(fs.readFileSync(f.archive), before);
  const folder = f.name + '-offline';
  assert.deepEqual(run('unzip', ['-Z1', f.output]).trim().split('\n').sort(),
    ['install-offline.sh', 'install.sh', p.basename(f.archive), p.basename(f.archive) + '.sha256', 'README.zh.md']
      .map(name => `${folder}/${name}`).sort());
  const extracted = p.join(f.temp, "recipient's $data folder");
  run('unzip', ['-q', f.output, '-d', extracted]);
  const directory = p.join(extracted, folder);
  assert.deepEqual(fs.readFileSync(p.join(directory, p.basename(f.archive))), before);
  assert.equal(fs.readFileSync(p.join(directory, 'install.sh'), 'utf8'), f.installer);
  assert.equal(fs.readFileSync(p.join(directory, p.basename(f.archive) + '.sha256'), 'utf8'),
    `${hash(before)}  ${p.basename(f.archive)}\n`);
  const record = p.join(f.temp, 'arguments');
  const result = spawnSync('/bin/bash', [p.join(directory, 'install-offline.sh'), '--home', '/private path'], {
    cwd: os.tmpdir(), env: { ...process.env, OFFLINE_TEST_RECORD: record }, encoding: 'utf8'
  });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(fs.readFileSync(record, 'utf8').trimEnd().split('\n'),
    ['--archive', p.join(directory, p.basename(f.archive)), '--home', '/private path']);
});

test('bad checksum, foreign sidecar, core archive or missing installer cannot produce an offline ZIP', t => {
  for (const mode of ['digest', 'sidecar', 'core', 'installer', 'identity']) {
    const f = fixture(t, mode === 'core' ? 'core' : 'standard');
    if (mode === 'digest') fs.appendFileSync(f.archive, 'tampered');
    if (mode === 'sidecar') fs.writeFileSync(f.archive + '.sha256', `${f.digest()}  other.tar.gz\n`);
    if (mode === 'installer') { fs.unlinkSync(p.join(f.bundle, 'install.sh')); f.repack(); }
    if (mode === 'identity') {
      f.manifest.target = 'darwin-arm64'; writeJSON(p.join(f.bundle, 'manifest.json'), f.manifest); f.repack();
    }
    assert.throws(f.build, /checksum|SHA-256|Sidecar|standard|install.sh|identity/);
    assert.equal(fs.existsSync(f.output), false);
  }
});

test('explicit digest supports standalone input and existing output is preserved', t => {
  const f = fixture(t), digest = f.digest();
  fs.unlinkSync(f.archive + '.sha256');
  f.build({ sha256: digest.toUpperCase() });
  const before = fs.readFileSync(f.output);
  assert.throws(() => f.build({ sha256: digest }), /already exists/);
  assert.deepEqual(fs.readFileSync(f.output), before);
  assert.ok(!fs.readdirSync(p.dirname(f.output)).some(name => name.startsWith('.teamx-offline-')));
});

test('CLI default output is isolated from source archives and Make accepts paths containing spaces', t => {
  const f = fixture(t);
  const cli = spawnSync(process.execPath, [p.join(root, 'scripts/package-offline.mjs'), '--archive', f.archive],
    { cwd: f.temp, encoding: 'utf8' });
  assert.equal(cli.status, 0, cli.stderr);
  const receipt = JSON.parse(cli.stdout);
  assert.equal(receipt.output, p.join(f.temp, 'dist', 'offline', f.name + '-offline.zip'));
  const archiveDirectory = p.join(f.temp, 'source archives with spaces');
  fs.mkdirSync(archiveDirectory);
  const archive = p.join(archiveDirectory, p.basename(f.archive));
  fs.copyFileSync(f.archive, archive); fs.copyFileSync(f.archive + '.sha256', archive + '.sha256');
  const output = p.join(f.temp, 'offline delivery.zip');
  const make = spawnSync('make', ['package-offline', `ARCHIVE=${archive}`, `OUTPUT=${output}`],
    { cwd: root, encoding: 'utf8' });
  assert.equal(make.status, 0, make.stderr);
  assert.ok(fs.existsSync(output));
});
