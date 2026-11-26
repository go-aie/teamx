import * as fs from 'node:fs';
import * as p from 'node:path';
import { hash, json, writeJSON, run, must, files, extract } from './package-lib.mjs';
import { assemblePlatformDependencies } from './package-platform.mjs';
import { assembleSRT } from './package-srt.mjs';
import { preparedDependencies } from './package-cache.mjs';

export function dependencyInputs(root, lock) {
  // Include assembly code as well as locks. Application-only commits reuse the
  // closure; any dependency/assembly change selects a different entry.
  return [
    'packaging/dependencies.lock.json', 'packaging/dsh/package.json', 'packaging/dsh/package-lock.json',
    'packaging/srt/package.json', 'packaging/srt/package-lock.json', 'packaging/srt/runtime.lock.json',
    lock.dependencies.glab.contract, 'packaging/glab-launcher', 'packaging/git-launcher',
    'scripts/package-dependencies.mjs', 'scripts/package-lib.mjs', 'scripts/package-cache.mjs',
    'scripts/package-platform.mjs', 'scripts/package-profiles.mjs', 'scripts/package-srt.mjs',
    'scripts/check-dsh-native.mjs'
  ].map(path => ({ path, sha256: hash(fs.readFileSync(p.join(root, path))) }));
}

export async function preparePackageDependencies({ root, bundle, cache, target, profile, lock, buildHome, offline, timing }) {
  const dep = lock.dependencies;
  async function download(entry) {
    const path = p.join(cache, entry.sha256 + '-' + entry.archive);
    if (fs.existsSync(path)) {
      must(
        hash(fs.readFileSync(path)) === entry.sha256,
        `Corrupt cache: ${entry.archive}`
      );
      return path;
    }
    must(!offline, `Offline cache missing: ${entry.archive}`);
    console.log(`Downloading pinned ${entry.archive}`);
    const tmp = path + `.${process.pid}.tmp`;
    try {
      run('curl', [
        '--fail',
        '--location',
        '--silent',
        '--show-error',
        '--http1.1',
        '--proto',
        '=https',
        '--proto-redir',
        '=https',
        '--retry',
        '2',
        '--connect-timeout',
        '15',
        '--max-time',
        '300',
        '--output',
        tmp,
        entry.url
      ]);
      must(
        hash(fs.readFileSync(tmp)) === entry.sha256,
        `Checksum mismatch: ${entry.archive}`
      );
      fs.renameSync(tmp, path);
      return path;
    } finally {
      fs.rmSync(tmp, { force: true });
    }
  }
  const dependencyBundle = bundle;
  const inputs = dependencyInputs(root, lock);
  return preparedDependencies({
    cache, destination: dependencyBundle, timing,
    identity: { version: 1, target, profile,
      libc: process.report?.getReport().header.glibcVersionRuntime || null, inputs },
    validate: directory => timing.step('native dependency check', () => {
      const node = p.join(directory, 'runtime/node/bin/node');
      run(node, [p.join(root, 'scripts/check-dsh-native.mjs'), p.join(directory, dep.dsh.layout.destination)], {
        cwd: buildHome, env: { ...process.env, HOME: buildHome, PATH: `${p.dirname(node)}:/usr/bin:/bin` }
      });
    }),
    build: async () => {
      for (const name of ['bin', 'THIRD_PARTY_NOTICES']) fs.mkdirSync(p.join(dependencyBundle, name), { recursive: true });
      const nodeEntry = dep.node.platforms[target];
      extract(
        await download(nodeEntry),
        p.join(dependencyBundle, nodeEntry.layout.destination),
        nodeEntry.layout.strip_components
      );
      const node = p.join(dependencyBundle, 'runtime/node/bin/node'),
        npm = p.join(dependencyBundle, 'runtime/node/lib/node_modules/npm/bin/npm-cli.js');
      must(
        run(node, ['--version']).trim() === dep.node.expected_version,
        'Node version mismatch'
      );
      must(
        run(node, [npm, '--version']).trim() === lock.build_tools.npm,
        'npm version mismatch'
      );
      const dsh = p.join(dependencyBundle, dep.dsh.layout.destination);
      fs.mkdirSync(dsh, { recursive: true });
      for (const file of ['package.json', 'package-lock.json'])
        fs.copyFileSync(p.join(root, 'packaging/dsh', file), p.join(dsh, file));
      const npmEnv = {
        ...process.env,
        HOME: buildHome,
        PATH: `${p.dirname(node)}:/usr/bin:/bin`,
        NPM_CONFIG_USERCONFIG: p.join(buildHome, 'npmrc'),
        NPM_CONFIG_GLOBALCONFIG: p.join(buildHome, 'global-npmrc'),
        NPM_CONFIG_CACHE: p.join(
          cache,
          `npm-${target}-${dep.dsh.deployment_lock.sha256}`
        ),
        NPM_CONFIG_UPDATE_NOTIFIER: 'false'
      };
      // Native binaries ship in integrity-locked optional packages/prebuilds. Do not
      // allow lifecycle scripts to fetch mutable, unverified GitHub assets.
      console.log(
        'Deploying pinned DSH production closure (lifecycle scripts disabled)'
      );
      timing.step('install DSH dependencies', () => run(
        node,
        [
          npm,
          'ci',
          '--omit=dev',
          '--ignore-scripts',
          '--no-audit',
          '--no-fund',
          ...(offline ? ['--offline'] : [])
        ],
        { cwd: dsh, env: npmEnv, stdio: 'inherit' }
      ));
      must(
        hash(fs.readFileSync(p.join(dsh, 'package-lock.json'))) ===
          dep.dsh.deployment_lock.sha256,
        'npm changed deployment lock'
      );
      // node-pty normally installs these permissions in a lifecycle script.
      for (const file of files(dsh).filter((f) =>
        /node-pty\/prebuilds\/.*\/spawn-helper$/.test(f)
      ))
        fs.chmodSync(p.join(dsh, file), 0o755);
      await assemblePlatformDependencies({
        root, bundle: dependencyBundle, target: target, profile: profile, lock,
        glabLock: json(p.join(root, dep.glab.contract)), download
      });
      const srt = assembleSRT({ root, bundle: dependencyBundle, target: target, profile: profile, cache, buildHome, offline: offline });
      const notices = [];
      for (const file of files(p.join(dsh, 'node_modules')).filter((f) =>
        /(?:^|\/)(?:licen[cs]e|copying|notice|copyright)(?:[^/]*?)$/i.test(f)
      )) {
        const src = p.join(dsh, 'node_modules', file);
        if (fs.lstatSync(src).isFile())
          notices.push({
            path: `runtime/dsh/node_modules/${file}`,
            sha256: hash(fs.readFileSync(src))
          });
      }
      const installed = [];
      for (const file of files(p.join(dsh, 'node_modules')).filter((f) =>
        /(?:^|\/)package.json$/.test(f)
      )) {
        const pkg = json(p.join(dsh, 'node_modules', file));
        if (pkg.name && pkg.version)
          installed.push({
            name: pkg.name,
            version: pkg.version,
            license: pkg.license || 'see package notices',
            path: `runtime/dsh/node_modules/${file}`
          });
      }
      writeJSON(p.join(dependencyBundle, 'THIRD_PARTY_NOTICES/dsh-dependencies.json'), {
        packages: installed,
        notices
      });
      return { srt };
    }
  });
}
