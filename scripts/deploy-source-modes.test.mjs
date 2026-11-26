import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import p from 'node:path';
import { execFileSync } from 'node:child_process';
import { checkoutDeploymentSource, saveDeployment } from './deploy-linux-remote.mjs';

test('private deployment jobs check out readable sources and restore the operator umask', t => {
  const root = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-deploy-source-modes-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const repo = p.join(root, 'repo');
  fs.mkdirSync(repo);
  const git = (...args) => execFileSync('git', ['-C', repo, ...args], { encoding: 'utf8' }).trim();
  git('init', '-q');
  fs.writeFileSync(p.join(repo, 'README.md'), 'public source', { mode: 0o600 });
  fs.writeFileSync(p.join(repo, 'launcher'), '#!/bin/sh\nexit 0\n', { mode: 0o755 });
  git('add', '.');
  git('-c', 'user.name=Fixture', '-c', 'user.email=fixture@localhost', '-c', 'commit.gpgsign=false', '-c', 'core.hooksPath=/dev/null', 'commit', '-qm', 'fixture');
  const commit = git('rev-parse', 'HEAD'), bundle = p.join(root, 'source.bundle');
  git('bundle', 'create', bundle, 'HEAD');
  const source = p.join(root, 'source'), previous = process.umask(0o077);
  try {
    checkoutDeploymentSource(source, bundle, commit);
    assert.equal(process.umask(), 0o077);
    assert.equal(fs.statSync(p.join(source, 'README.md')).mode & 0o777, 0o644);
    assert.equal(fs.statSync(p.join(source, 'launcher')).mode & 0o777, 0o755);
    assert.equal(fs.readFileSync(p.join(source, 'README.md'), 'utf8'), 'public source');
    saveDeployment(p.join(root, 'receipt.json'), { status: 'private' });
    assert.equal(fs.statSync(p.join(root, 'receipt.json')).mode & 0o777, 0o600);
    assert.throws(() => checkoutDeploymentSource(source, bundle, commit), /EEXIST/);
    assert.equal(process.umask(), 0o077);
  } finally { process.umask(previous); }
});
