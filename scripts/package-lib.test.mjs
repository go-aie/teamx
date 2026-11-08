import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as p from 'node:path';
import { fileURLToPath } from 'node:url';
import { hash, run, inventory, extract, writeJSON } from './package-lib.mjs';
import { binaryTarget, checkBinaries } from './package-platform.mjs';
const scripts = p.dirname(fileURLToPath(import.meta.url));

test('private evidence umask preserves archived executable and readable modes', (t) => {
  const dir = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-private-mode-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const source = p.join(dir, 'source');
  fs.mkdirSync(source);
  fs.writeFileSync(p.join(source, 'start'), '#!/bin/sh\n', { mode: 0o755 });
  fs.writeFileSync(p.join(source, 'readme'), 'data', { mode: 0o644 });
  const archive = p.join(dir, 'bundle.tar.gz');
  run('tar', ['-czf', archive, '-C', dir, 'source']);
  const previous = process.umask(0o077);
  try { extract(archive, p.join(dir, 'out')); } finally { process.umask(previous); }
  assert.deepEqual(inventory(p.join(dir, 'out/source')), inventory(source));
});

test('link permissions may change across filesystems while target and regular file modes remain verifiable', (t) => {
  const dir = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-link-mode-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  fs.writeFileSync(p.join(dir, 'file'), 'data', { mode: 0o755 });
  const link = p.join(dir, 'link');
  fs.symlinkSync('file', link);
  const before = inventory(dir);
  if (process.platform === 'darwin') fs.lchmodSync(link, 0o700);
  assert.deepEqual(inventory(dir), before);
  assert.equal(Object.hasOwn(before.find(f => f.path === 'link'), 'mode'), false);
  fs.chmodSync(p.join(dir, 'file'), 0o644);
  assert.notDeepEqual(inventory(dir), before);
  fs.unlinkSync(link);
  fs.symlinkSync('other', link);
  fs.writeFileSync(p.join(dir, 'other'), 'data');
  assert.notDeepEqual(inventory(dir).find(f => f.path === 'link'), before.find(f => f.path === 'link'));
});

test('binary target checks reject a mismatched full helper even when the host could emulate it', (t) => {
  const dir = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-target-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const elf = (cpu) => {
    const b = Buffer.alloc(64);
    b.set([0x7f, 0x45, 0x4c, 0x46, 2, 1]);
    b.writeUInt16LE(cpu, 18);
    return b;
  };
  const macho = (cpu) => {
    const b = Buffer.alloc(64);
    b.writeUInt32LE(0xfeedfacf, 0);
    b.writeUInt32LE(cpu, 4);
    return b;
  };
  for (const [target, data] of [
    ['linux-amd64', elf(62)], ['linux-arm64', elf(183)],
    ['darwin-amd64', macho(0x01000007)], ['darwin-arm64', macho(0x0100000c)]
  ]) {
    const path = p.join(dir, target);
    fs.writeFileSync(path, data);
    assert.equal(binaryTarget(path), target);
  }
  for (const name of ['bin/teamx', 'bin/teamx-gitlab', 'bin/teamx-scion', 'runtime/node/bin/node', 'runtime/glab/bin/glab', 'bin/lark-cli', 'runtime/git/bin/git', 'runtime/git/libexec/git-core/git-remote-https']) {
    const path = p.join(dir, name);
    fs.mkdirSync(p.dirname(path), { recursive: true });
    fs.writeFileSync(path, elf(name.endsWith('git-remote-https') ? 183 : 62));
  }
  assert.doesNotThrow(() => checkBinaries(dir, 'linux-amd64', 'core'));
  assert.throws(() => checkBinaries(dir, 'linux-amd64', 'full'), /Binary target mismatch.*git-remote-https/);
});

test('inventory and extraction reject links outside a distribution', (t) => {
  const dir = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-package-negative-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const root = p.join(dir, 'bundle');
  fs.mkdirSync(root);
  fs.symlinkSync('/etc/passwd', p.join(root, 'escape'));
  assert.throws(() => inventory(root), /Escaping symlink/);
  const tar = p.join(dir, 'bad.tar.gz');
  run('tar', ['-czf', tar, '-C', dir, 'bundle']);
  assert.throws(() => extract(tar, p.join(dir, 'out')), /Escaping symlink/);
});

test('archive smoke rejects modified, extra and incorrectly permissioned files before launch', (t) => {
  const dir = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-package-negative-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  for (const variant of ['modified', 'extra', 'permissions']) {
    const root = p.join(dir, variant);
    fs.mkdirSync(root);
    writeJSON(p.join(root, 'manifest.json'), {
      target: `${process.platform}-${process.arch === 'x64' ? 'amd64' : process.arch}`
    });
    fs.writeFileSync(p.join(root, 'start'), '#!/bin/sh\nexit 0\n', {
      mode: 0o755
    });
    writeJSON(p.join(root, 'checksums.txt'), inventory(root));
    if (variant === 'modified')
      fs.appendFileSync(p.join(root, 'start'), '# changed\n');
    if (variant === 'extra') fs.writeFileSync(p.join(root, '.env'), 'private');
    if (variant === 'permissions') fs.chmodSync(p.join(root, 'start'), 0o644);
    const archive = p.join(dir, variant + '.tar.gz');
    run('tar', ['-czf', archive, '-C', dir, variant]);
    const report = p.join(dir, variant + '.json');
    assert.throws(
      () =>
        run(
          process.execPath,
          [p.join(scripts, 'package-smoke.mjs'), archive, '--report', report],
          { stdio: ['ignore', 'pipe', 'pipe'] }
        ),
      (error) =>
        String(error.stderr).includes(
          'Immutable contents/permissions/checksums differ'
        )
    );
    assert.equal(fs.existsSync(report), false);
  }
});

test('internal links remain valid when the extraction parent has a filesystem alias', (t) => {
  const dir = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-package-alias-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const real = p.join(dir, 'real'),
    alias = p.join(dir, 'alias');
  fs.mkdirSync(real);
  fs.writeFileSync(p.join(real, 'file'), 'data');
  fs.symlinkSync('file', p.join(real, 'link'));
  fs.symlinkSync(real, alias);
  assert.deepEqual(inventory(alias), inventory(real));
});
