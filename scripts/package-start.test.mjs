import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as p from 'node:path';
import { spawn, spawnSync, execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const launcher = fileURLToPath(new URL('../packaging/start', import.meta.url));
const macOS = { skip: process.platform !== 'darwin' };
const quarantine = 'com.apple.quarantine';

function fixture(t) {
  const temp = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-start-'));
  t.after(() => fs.rmSync(temp, { recursive: true, force: true }));
  const root = p.join(temp, 'bundle with spaces');
  fs.mkdirSync(p.join(root, 'bin'), { recursive: true });
  fs.mkdirSync(p.join(root, 'runtime/native'), { recursive: true });
  fs.copyFileSync(launcher, p.join(root, 'start'));
  const record = p.join(temp, 'launched');
  fs.writeFileSync(p.join(root, 'bin/teamx'), '#!/bin/sh\nprintf "%s\\n" "$@" > "$TEAMX_LAUNCH_RECORD"\n', { mode: 0o755 });
  const native = p.join(root, 'runtime/native/addon.node');
  fs.writeFileSync(native, 'fixture');
  const env = { ...process.env, TEAMX_LAUNCH_RECORD: record };
  const args = ['/bin/sh', p.join(root, 'start'), 'init', '--home', p.join(temp, 'private home')];
  return { temp, root, record, native, env, args };
}

function setAttribute(file, name = quarantine) {
  execFileSync('/usr/bin/xattr', ['-w', name, '0081;00000000;TeamXTest;', file]);
}

function hasQuarantine(file) {
  return execFileSync('/usr/bin/xattr', ['-s', file], { encoding: 'utf8' }).split('\n').includes(quarantine);
}

// A shell pipe gives macOS script(1) a regular pipe rather than Node's socket.
// script supplies the real terminal required by the approval prompt.
function terminal(f, answer, beforeAnswer = () => {}) {
  return new Promise((resolve, reject) => {
    const child = spawn('/bin/sh', ['-c', 'cat | /usr/bin/script -q /dev/null "$@"', 'pty-test', ...f.args], { env: f.env });
    let output = '', answered = false;
    const timer = setTimeout(() => { child.kill(); reject(Error(`Launcher timed out: ${output}`)); }, 10000);
    child.on('error', reject);
    child.stdout.on('data', data => {
      output += data;
      if (!answered && output.includes('[默认取消] ')) {
        answered = true;
        try { beforeAnswer(); } catch (error) { child.stdin.end('no\n'); reject(error); return; }
        child.stdin.end(answer);
      }
    });
    child.stderr.on('data', data => { output += data; });
    child.on('close', status => { clearTimeout(timer); resolve({ status, output }); });
    if (f.args.includes('--non-interactive')) child.stdin.end();
  });
}

test('an unquarantined bundle launches without interaction and preserves arguments', t => {
  const f = fixture(t);
  const result = spawnSync(f.args[0], f.args.slice(1), { env: f.env, encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stderr, '');
  assert.deepEqual(fs.readFileSync(f.record, 'utf8').trimEnd().split('\n'),
    ['bundle', '--bundle-root', fs.realpathSync(f.root), ...f.args.slice(2)]);
});

test('quarantine on a nested runtime blocks piped approval and unattended initialization', macOS, async t => {
  const f = fixture(t);
  setAttribute(f.native);
  const result = spawnSync(f.args[0], f.args.slice(1), { env: f.env, input: 'yes\n', encoding: 'utf8' });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /在终端执行/);
  f.args.push('--non-interactive');
  const unattended = await terminal(f, 'yes\n');
  assert.equal(unattended.status, 1, unattended.output);
  assert.doesNotMatch(unattended.output, /\[默认取消\]/);
  assert.equal(fs.existsSync(f.record), false);
  assert.equal(hasQuarantine(f.native), true);
});

test('blank, negative and EOF answers leave quarantined code unexecuted', macOS, async t => {
  for (const answer of ['\n', 'no\n', '']) {
    const f = fixture(t);
    setAttribute(f.native);
    const result = await terminal(f, answer);
    assert.equal(result.status, 1, result.output);
    assert.match(result.output, /已取消/);
    assert.equal(fs.existsSync(f.record), false);
    assert.equal(hasQuarantine(f.native), true);
  }
});

test('inspection and removal errors stop before launching bundle code', macOS, async t => {
  const f = fixture(t);
  setAttribute(f.native);
  const directory = p.dirname(f.native);
  fs.chmodSync(directory, 0o000);
  try {
    const unreadable = spawnSync(f.args[0], f.args.slice(1), { env: f.env, encoding: 'utf8' });
    assert.equal(unreadable.status, 1, unreadable.stderr);
    assert.match(unreadable.stderr, /无法检查/);
    assert.equal(fs.existsSync(f.record), false);
  } finally { fs.chmodSync(directory, 0o755); }
  fs.chmodSync(f.native, 0o444);
  try {
    const readonly = await terminal(f, 'yes\n');
    assert.equal(readonly.status, 1, readonly.output);
    assert.match(readonly.output, /解除下载隔离失败/);
    assert.equal(fs.existsSync(f.record), false);
    assert.equal(hasQuarantine(f.native), true);
  } finally { fs.chmodSync(f.native, 0o644); }
});

test('one approval clears the bundle, preserves other attributes and never follows external symlinks', macOS, async t => {
  const f = fixture(t);
  const outside = p.join(f.temp, 'outside');
  fs.writeFileSync(outside, 'outside');
  fs.symlinkSync(outside, p.join(f.root, 'external-link'));
  for (const file of [f.root, p.join(f.root, 'start'), p.join(f.root, 'bin/teamx'), f.native, outside]) setAttribute(file);
  setAttribute(f.native, 'org.teamx.test');
  const result = await terminal(f, 'yes\n', () => {
    assert.equal(fs.existsSync(f.record), false);
    assert.equal(hasQuarantine(f.native), true);
  });
  assert.equal(result.status, 0, result.output);
  assert.equal(fs.existsSync(f.record), true);
  for (const file of [f.root, p.join(f.root, 'start'), p.join(f.root, 'bin/teamx'), f.native]) assert.equal(hasQuarantine(file), false);
  assert.equal(hasQuarantine(outside), true);
  assert.match(execFileSync('/usr/bin/xattr', [f.native], { encoding: 'utf8' }), /org.teamx.test/);
  fs.unlinkSync(f.record);
  const again = spawnSync(f.args[0], f.args.slice(1), { env: f.env, encoding: 'utf8' });
  assert.equal(again.status, 0, again.stderr);
  assert.equal(again.stderr, '');
  assert.equal(fs.existsSync(f.record), true);
  // A new quarantined file must require approval again, without an approval marker.
  setAttribute(f.native);
  fs.unlinkSync(f.record);
  const updated = spawnSync(f.args[0], f.args.slice(1), { env: f.env, encoding: 'utf8' });
  assert.equal(updated.status, 1);
  assert.equal(fs.existsSync(f.record), false);
});
