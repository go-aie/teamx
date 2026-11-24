// One SandboxManager per process/Session. ACP alone uses stdout.
import fs from 'node:fs';
import { spawn } from 'node:child_process';
import { lookup } from 'node:dns';
import { isIP } from 'node:net';
import { pathToFileURL } from 'node:url';

const config = JSON.parse(fs.readFileSync(3, 'utf8'));
fs.closeSync(3);
let manager;
let child;
let interrupted = false;
let stage = 'runtime';
for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => { interrupted = true; child?.kill(signal); });
}
try {
  if (process.platform !== 'linux' || process.getuid() === 0 || process.versions.node !== '24.22.2') {
    throw new Error('unverified SRT host or Node version');
  }
  ({ SandboxManager: manager } = await import(pathToFileURL(`${config.Runtime}/node_modules/@anthropic-ai/sandbox-runtime/dist/index.js`)));
  const routes = new Map((config.Routes ?? []).map(route => [route.host, route.address]));
  // This pinned SRT's address guard accepts a resolver on its network options.
  // The guard still checks the returned address/port before the one and only
  // dial. Internal aliases never consult DNS and cannot rebind externally.
  config.Policy.network.resolve = (host, options, callback) => {
    const address = routes.get(host);
    if (address) return callback(null, [{ address, family: isIP(address) }]);
    if (host.endsWith('.execution.invalid')) return callback(Object.assign(new Error('unknown execution endpoint'), { code: 'ENOTFOUND' }));
    return lookup(host, options, callback);
  };
  stage = 'initialize';
  await manager.initialize(config.Policy);
  const deps = manager.checkDependencies(config.Policy.ripgrep);
  if (deps.errors?.length || deps.warnings?.length) throw new Error('required SRT dependencies unavailable');
  if (interrupted) throw new Error('SRT startup interrupted');
  // Each argument survives the shell required by the pinned SRT Linux API.
  const quote = value => `'${String(value).replaceAll("'", "'\\''")}'`;
  const argv = config.Check ? ['/bin/true'] : ['/usr/bin/env', 'NO_PROXY=', 'no_proxy=', 'NODE_USE_ENV_PROXY=1', `TMPDIR=${config.Temp}`, config.Binary, ...config.Args];
  stage = 'wrap';
  const command = await manager.wrapWithSandbox(argv.map(quote).join(' '));
  if (interrupted) throw new Error('SRT startup interrupted');
  stage = 'spawn';
  child = spawn(command, { shell: '/bin/bash', stdio: 'inherit' });
  const status = await new Promise((resolve, reject) => {
    child.once('error', reject);
    child.once('exit', (code, signal) => resolve({ code, signal }));
  });
  await manager.cleanupAfterCommand();
  await manager.reset();
  // A signal or cleanup failure can never become normal resume evidence.
  process.exitCode = !interrupted && !status.signal && status.code === 0 ? 0 : 1;
} catch (error) {
  process.stderr.write(`SRT ${stage} failure: ${String(error?.message ?? error).slice(0, 1024)}\n`);
  try { await manager?.reset(); } catch {}
  process.exitCode = 1;
}
