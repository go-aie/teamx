import * as fs from 'node:fs';
import * as p from 'node:path';
import { hash, inventory, json, must, run, writeJSON } from './package-lib.mjs';

export const includesSRT = (target, profile) => target === 'linux-amd64' && profile === 'standard';

export function checkSRTHosts(lock) {
  const seen = new Set();
  for (const host of [lock, ...(lock.additional_hosts || [])]) {
    const { system, host_tools: tools } = host;
    must(system?.id && system.version_id && /^glibc \d+\.\d+$/.test(system.glibc), 'Invalid SRT host baseline');
    const key = `${system.id}/${system.version_id}/${system.glibc}`;
    must(!seen.has(key), 'Duplicate SRT host baseline');
    seen.add(key);
    must(Array.isArray(tools) && tools.length === 3 &&
      ['/usr/bin/bwrap', '/usr/bin/socat', '/usr/bin/rg'].every(path => tools.filter(t => t.path === path).length === 1),
    'Incomplete SRT host helpers');
    for (const tool of tools) must(/^https:\/\//.test(tool.url) && tool.version && tool.version_contains &&
      /^[a-f0-9]{64}$/.test(tool.sha256) && /^[a-f0-9]{64}$/.test(tool.binary_sha256),
    'Unpinned SRT host helper');
  }
}

export function checkSRTLock(root) {
  const lock = json(p.join(root, 'packaging/srt/runtime.lock.json'));
  checkSRTHosts(lock);
  const npm = fs.readFileSync(p.join(root, lock.npm_lock));
  must(hash(npm) === lock.npm_lock_sha256 &&
    npm.equals(fs.readFileSync(p.join(root, 'internal/agent/srt/package-lock.json'))),
  'SRT deployment lock differs from execution contract');
  const packages = JSON.parse(npm).packages;
  must(packages['node_modules/@anthropic-ai/sandbox-runtime'].version === lock.version,
    'SRT runtime version differs');
  for (const [name, entry] of Object.entries(packages)) if (name)
    must(/^https:\/\/registry.npmjs.org\//.test(entry.resolved) && /^sha512-/.test(entry.integrity),
      `SRT dependency lacks pinned integrity: ${name}`);
  return lock;
}

// SRT is an available resource in this archive, never the default execution.
// Native helpers are fixed host prerequisites, not copied from a build host.
export function assembleSRT({ root, bundle, target, profile, cache, buildHome, offline }) {
  if (!includesSRT(target, profile)) return undefined;
  const lock = checkSRTLock(root), runtime = p.join(bundle, 'runtime/srt');
  fs.mkdirSync(runtime, { recursive: true });
  for (const name of ['package.json', 'package-lock.json'])
    fs.copyFileSync(p.join(root, 'packaging/srt', name), p.join(runtime, name));
  const node = p.join(bundle, 'runtime/node/bin/node');
  run(node, [p.join(bundle, 'runtime/node/lib/node_modules/npm/bin/npm-cli.js'),
    'ci', '--omit=dev', '--ignore-scripts', '--no-audit', '--no-fund', ...(offline ? ['--offline'] : [])], {
    cwd: runtime, stdio: 'inherit', env: {
      HOME: buildHome, PATH: `${p.dirname(node)}:/usr/bin:/bin`,
      NPM_CONFIG_USERCONFIG: p.join(buildHome, 'npmrc'),
      NPM_CONFIG_GLOBALCONFIG: p.join(buildHome, 'global-npmrc'),
      NPM_CONFIG_CACHE: p.join(cache, `npm-srt-${target}-${lock.npm_lock_sha256}`),
      NPM_CONFIG_UPDATE_NOTIFIER: 'false'
    }
  });
  must(hash(fs.readFileSync(p.join(runtime, 'package-lock.json'))) === lock.npm_lock_sha256,
    'npm changed SRT lock');
  // Dependency preparation runs before public assets are copied, including
  // when building a fresh prepared-dependency cache. Own our output parents.
  for (const directory of ['share/teamx', 'THIRD_PARTY_NOTICES'])
    fs.mkdirSync(p.join(bundle, directory), { recursive: true });
  writeJSON(p.join(bundle, 'share/teamx/srt-runtime.json'), lock);
  const items = inventory(runtime);
  writeJSON(p.join(bundle, 'THIRD_PARTY_NOTICES/srt-dependencies.json'), {
    packages: Object.entries(json(p.join(runtime, 'package-lock.json')).packages)
      .filter(([name]) => name).map(([path, entry]) => ({ path, ...entry })),
    notices: items.filter(item => /(?:^|\/)(?:licen[cs]e|copying|notice)[^/]*$/i.test(item.path))
  });
  return { version: lock.version, npm_lock_sha256: lock.npm_lock_sha256,
    runtime_sha256: hash(JSON.stringify(items)), contract: 'share/teamx/srt-runtime.json',
    default_execution: 'native', release_acceptance: 'pending' };
}

export function checkSRTArchive(bundle, manifest) {
  if (!includesSRT(manifest.target, manifest.profile)) {
    must(!manifest.execution_runtimes?.srt, 'Unexpected SRT target/profile');
    return;
  }
  const record = manifest.execution_runtimes?.srt;
  must(record?.default_execution === 'native' && record.contract === 'share/teamx/srt-runtime.json',
    'Missing SRT resource contract');
  const lock = json(p.join(bundle, record.contract));
  checkSRTHosts(lock);
  must(lock.target === manifest.target && lock.profile === manifest.profile && lock.version === record.version &&
    lock.npm_lock_sha256 === record.npm_lock_sha256, 'SRT contract identity mismatch');
  must(hash(fs.readFileSync(p.join(bundle, 'runtime/srt/package-lock.json'))) === record.npm_lock_sha256 &&
    hash(JSON.stringify(inventory(p.join(bundle, 'runtime/srt')))) === record.runtime_sha256,
    'SRT resource digest mismatch');
  run(p.join(bundle, 'runtime/node/bin/node'), ['--input-type=module', '-e',
    'import {SandboxManager} from "@anthropic-ai/sandbox-runtime"; if (!SandboxManager) process.exit(1)'],
  { cwd: p.join(bundle, 'runtime/srt'), env: { PATH: '/usr/bin:/bin' } });
}
