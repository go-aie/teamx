import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import p from 'node:path';
import { preparedDependencies } from './package-cache.mjs';
import { createHash } from 'node:crypto';
import { extract, run } from './package-lib.mjs';
import { dependencyInputs } from './package-dependencies.mjs';

function fixture(t) {
  const root = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-dependency-cache-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  let builds = 0, validations = 0;
  const cache = p.join(root, 'cache'), identity = { target: 'linux-amd64', lock: 'fixed-lock', assembly: 'fixed-code' };
  const build = directory => {
    builds++;
    fs.mkdirSync(p.join(directory, 'bin'), { recursive: true });
    fs.writeFileSync(p.join(directory, 'bin/cli'), '#!/bin/sh\nexit 0\n', { mode: 0o755 });
    fs.symlinkSync('cli', p.join(directory, 'bin/alias'));
    return { version: 'pinned' };
  };
  const validate = () => { validations++; };
  return { root, cache, identity, build, validate, builds: () => builds, validations: () => validations,
    prepare: (name, extra = {}) => preparedDependencies({ cache, identity, destination: p.join(root, name), build, validate, ...extra }) };
}

test('application and compression changes reuse dependencies; locks and dependency assembly invalidate them', async t => {
  const f = fixture(t), source = new URL('../', import.meta.url);
  const lock = JSON.parse(fs.readFileSync(new URL('packaging/dependencies.lock.json', source), 'utf8'));
  for (const { path } of dependencyInputs(source.pathname, lock)) {
    fs.mkdirSync(p.dirname(p.join(f.root, path)), { recursive: true });
    fs.copyFileSync(new URL(path, source), p.join(f.root, path));
  }
  const identity = () => ({ ...f.identity, inputs: dependencyInputs(f.root, lock) });
  await f.prepare('cold', { identity: identity() });
  fs.writeFileSync(p.join(f.root, 'scripts/package.mjs'), 'changed application assembly and compression');
  fs.writeFileSync(p.join(f.root, 'scripts/package-archive.mjs'), 'changed compression');
  fs.writeFileSync(p.join(f.root, 'app.go'), 'changed application');
  await f.prepare('application', { identity: identity() });
  assert.equal(f.builds(), 1);
  for (const file of ['scripts/package-dependencies.mjs', 'packaging/dsh/package-lock.json']) {
    fs.appendFileSync(p.join(f.root, file), '\nchanged');
    await f.prepare('changed-' + f.builds(), { identity: identity() });
  }
  assert.equal(f.builds(), 3);
});

test('prepared dependencies reuse checked files, modes and relative links without sharing writable inodes', async t => {
  const f = fixture(t);
  await f.prepare('first');
  fs.writeFileSync(p.join(f.root, 'first/bin/cli'), 'caller changed its copy');
  assert.deepEqual(await f.prepare('second'), { version: 'pinned' });
  assert.equal(f.builds(), 1);assert.equal(f.validations(), 2);
  assert.equal(fs.readFileSync(p.join(f.root, 'second/bin/cli'), 'utf8'), '#!/bin/sh\nexit 0\n');
  assert.equal(fs.statSync(p.join(f.root, 'second/bin/cli')).mode & 0o777, 0o755);
  assert.equal(fs.readlinkSync(p.join(f.root, 'second/bin/alias')), 'cli');
  for (const identity of [{ ...f.identity, lock: 'new-lock' }, { ...f.identity, target: 'linux-arm64' }, { ...f.identity, assembly: 'new-code' }]) await f.prepare('variant-' + f.builds(), { identity });
  assert.equal(f.builds(), 4);
});

test('changed or extra dependency cache contents fail before native validation or rebuilding', async t => {
  for (const mode of ['changed', 'extra', 'symlink']) {
    const f = fixture(t);await f.prepare('first');
    const entry = p.join(f.cache, 'prepared-dependencies', fs.readdirSync(p.join(f.cache, 'prepared-dependencies'))[0]);
    const payload = p.join(f.root, 'tampered'), archive = p.join(entry, 'payload.tar');
    extract(archive, payload);
    if (mode === 'changed') fs.writeFileSync(p.join(payload, 'bin/cli'), 'modified');
    if (mode === 'extra') fs.writeFileSync(p.join(payload, 'extra'), 'unexpected');
    if (mode === 'symlink') { fs.unlinkSync(p.join(payload, 'bin/alias'));fs.symlinkSync('/etc/passwd', p.join(payload, 'bin/alias')); }
    run('tar', ['-cf', archive, '-C', payload, '.']);
    // Even a matching tar digest cannot hide changed files or escaping links.
    const record = JSON.parse(fs.readFileSync(p.join(entry, 'record.json'), 'utf8'));
    record.payload_sha256 = createHash('sha256').update(fs.readFileSync(archive)).digest('hex');
    fs.writeFileSync(p.join(entry, 'record.json'), JSON.stringify(record));
    await assert.rejects(f.prepare('second'), /cache mismatch|Escaping symlink/);
    assert.equal(f.builds(), 1);assert.equal(f.validations(), 1);
  }
});

test('build or native validation failure never publishes a reusable closure', async t => {
  const f = fixture(t);
  for (const extra of [{ build: () => { throw Error('install failed'); } }, { validate: () => { throw Error('native failed'); } }]) {
    await assert.rejects(f.prepare('failure-' + f.builds(), extra), /failed/);
    assert.deepEqual(fs.readdirSync(p.join(f.cache, 'prepared-dependencies')), []);
  }
  await f.prepare('good');assert.equal(f.builds(), 2);
});

test('a damaged prepared archive is refused before extraction or native execution', async t => {
  const f = fixture(t);await f.prepare('first');
  const entry = p.join(f.cache, 'prepared-dependencies', fs.readdirSync(p.join(f.cache, 'prepared-dependencies'))[0]);
  fs.appendFileSync(p.join(entry, 'payload.tar'), 'damaged');
  await assert.rejects(f.prepare('second'), /cache mismatch/);
  assert.equal(fs.existsSync(p.join(f.root, 'second')), false);assert.equal(f.validations(), 1);
});

test('interrupted and concurrent cache preparation cannot publish a partial dependency closure', async t => {
  const f = fixture(t);
  await assert.rejects(f.prepare('interrupted', { build: directory => { f.build(directory);throw Error('interrupted install'); } }), /interrupted/);
  assert.deepEqual(fs.readdirSync(p.join(f.cache, 'prepared-dependencies')), []);
  const build = async directory => { const metadata = f.build(directory);await new Promise(resolve => setTimeout(resolve, 20));return metadata; };
  await Promise.all([f.prepare('concurrent-a', { build }), f.prepare('concurrent-b', { build })]);
  await f.prepare('reused');
  assert.equal(f.builds(), 3);
  assert.equal(fs.readdirSync(p.join(f.cache, 'prepared-dependencies')).filter(name => !name.startsWith('.')).length, 1);
});
