import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import p from 'node:path';
import { spawnSync } from 'node:child_process';

test('packaging uses public artifact modes under a private operator umask', t => {
  const dir = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-package-modes-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const entry = new URL('./package.mjs', import.meta.url);
  const source = `
    import fs from 'node:fs';
    process.umask(0o077);
    process.on('exit', () => {
      const root = process.env.TEAMX_MODE_PROBE;
      fs.writeFileSync(root + '/manifest.json', '{}');
      fs.writeFileSync(root + '/executable', 'public executable', { mode: 0o755 });
      fs.writeFileSync(root + '/private-evidence', 'private evidence', { mode: 0o600 });
    });
    process.argv = [process.execPath, ${JSON.stringify(entry.pathname)}, '--help'];
    await import(${JSON.stringify(entry.href)});
  `;
  const result = spawnSync(process.execPath, ['--input-type=module', '-e', source], {
    env: { ...process.env, TEAMX_MODE_PROBE: dir }, encoding: 'utf8'
  });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /package\.sh/);
  assert.equal(fs.statSync(p.join(dir, 'manifest.json')).mode & 0o777, 0o644);
  assert.equal(fs.statSync(p.join(dir, 'executable')).mode & 0o777, 0o755);
  assert.equal(fs.statSync(p.join(dir, 'private-evidence')).mode & 0o777, 0o600);
});
