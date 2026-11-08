// Runs with the bundled Node against the installed closure, without credentials.
import { createRequire } from 'node:module';
import { execFileSync } from 'node:child_process';
import { resolve, join } from 'node:path';
import { mkdtempSync, openSync, closeSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
const require = createRequire(resolve(process.argv[2], 'package.json'));
const sharp = require('sharp');
const pty = require('node-pty');
require('koffi');
const dir = mkdtempSync(join(tmpdir(), 'teamx-native-'));
const fd = openSync(join(dir, 'lock'), 'w');
try {
  await require('@deepseek-ai/node-addon-system/flock').tryLockExclusive(fd);
  await sharp({
    create: { width: 1, height: 1, channels: 3, background: '#ffffff' }
  })
    .png()
    .toBuffer();
  execFileSync(require('@vscode/ripgrep').rgPath, ['--version']);
  await new Promise((resolve, reject) => {
    const child = pty.spawn('/bin/sh', ['-c', 'exit 0'], {
      env: { PATH: '/usr/bin:/bin' }
    });
    const timer = setTimeout(() => {
      child.kill();
      reject(Error('node-pty did not settle'));
    }, 5000);
    child.onExit(({ exitCode }) => {
      clearTimeout(timer);
      exitCode === 0 ? resolve() : reject(Error('node-pty failed'));
    });
  });
} finally {
  closeSync(fd);
  rmSync(dir, { recursive: true, force: true });
}
