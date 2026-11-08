import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import * as fs from 'node:fs';
import * as p from 'node:path';
export const hash = (data) => createHash('sha256').update(data).digest('hex');
export const json = (path) => JSON.parse(fs.readFileSync(path, 'utf8'));
export const writeJSON = (path, value) =>
  fs.writeFileSync(path, JSON.stringify(value, null, 2) + '\n');
export function run(cmd, args = [], options = {}) {
  return execFileSync(cmd, args, {
    encoding: 'utf8',
    maxBuffer: 64 * 1024 * 1024,
    stdio: ['ignore', 'pipe', 'inherit'],
    ...options
  });
}
export function must(ok, message) {
  if (!ok) throw Error(message);
}
export function files(root) {
  const result = [];
  function walk(dir) {
    for (const name of fs.readdirSync(dir).sort()) {
      const path = p.join(dir, name);
      if (fs.lstatSync(path).isDirectory()) walk(path);
      else result.push(p.relative(root, path));
    }
  }
  walk(root);
  return result;
}
export function inventory(root) {
  root = fs.realpathSync(root);
  return files(root).map((name) => {
    must(!/[\n\r\\]/.test(name), `Unsupported filename: ${name}`);
    const path = p.join(root, name),
      st = fs.lstatSync(path);
    if (st.isSymbolicLink()) {
      const target = fs.readlinkSync(path),
        resolved = fs.realpathSync(path);
      must(
        !p.isAbsolute(target) && !p.relative(root, resolved).startsWith('..'),
        `Escaping symlink: ${name}`
      );
      return {
        path: name,
        kind: 'symlink',
        target,
        // Link permission bits are not portable: a Linux builder on a macOS
        // bind mount can observe 0755, while extraction onto ext4 yields 0777.
        // Validate the link target/containment and the target file's own mode.
        sha256: hash(target)
      };
    }
    must(st.isFile(), `Unsupported asset: ${name}`);
    return {
      path: name,
      kind: 'file',
      sha256: hash(fs.readFileSync(path)),
      mode: st.mode & 0o777
    };
  });
}
export function extract(archive, dest, strip = 0) {
  const names = run('tar', ['-tf', archive]).split('\n').filter(Boolean);
  must(
    names.every((n) => !p.isAbsolute(n) && !n.split('/').includes('..')),
    'Unsafe archive path'
  );
  fs.mkdirSync(dest, { recursive: true });
  // An operator may use umask 077 for private acceptance evidence. Preserve
  // reviewed archive modes rather than silently stripping read/execute bits.
  run('tar', ['-xpf', archive, '-C', dest, `--strip-components=${strip}`], {
    env: { ...process.env, COPYFILE_DISABLE: '1' }
  });
  inventory(dest);
}
