import * as fs from 'node:fs';
import * as p from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  hash,
  json,
  writeJSON,
  run,
  must,
  files,
  inventory,
  extract
} from './package-lib.mjs';
import { assemblePlatformDependencies, checkBinaries } from './package-platform.mjs';
import { hasDependency } from './package-profiles.mjs';
import { assertReleaseSource } from './package-release-source.mjs';
import { signingPolicy, signBundle, notarizeBundle } from './package-signing.mjs';
const root = p.resolve(p.dirname(fileURLToPath(import.meta.url)), '..');
const opts = {
  channel: 'dev',
  profile: 'standard',
  target: `${process.platform}-${process.arch === 'x64' ? 'amd64' : process.arch}`,
  offline: false
};
let stage, pending, outputLock;
try {
  const args = process.argv.slice(2);
  while (args.length) {
    const arg = args.shift();
    if (arg === '--offline') opts.offline = true;
    else if (arg === '--candidate') opts.candidate = true;
    else if (
      [
        '--channel',
        '--profile',
        '--target',
        '--version',
        '--macos-signing',
        '--cache-dir',
        '--output-dir'
      ].includes(arg)
    ) {
      must(args.length && !args[0].startsWith('--'), `Missing ${arg}`);
      opts[arg.slice(2)] = args.shift();
    } else if (arg === '--help') {
      console.log(
        'package.sh [--channel dev|release] [--candidate] [--profile core|standard|full] [--target OS-ARCH] [--offline] [--cache-dir DIR] [--output-dir DIR] [--version vX.Y.Z] [--macos-signing unsigned|developer-id]'
      );
      process.exit(0);
    } else throw Error(`Unknown argument: ${arg}`);
  }
  const lock = json(p.join(root, 'packaging/dependencies.lock.json')),
    assets = json(p.join(root, 'packaging/bundle-files.json'));
  const packagingInputs = [...new Set([
    'go.mod', 'go.sum', 'packaging/dependencies.lock.json',
    'docs/implementation/model-deletion-migration-delivery.zh.md',
    'packaging/bundle-files.json', 'packaging/git-launcher', 'packaging/glab-launcher',
    'packaging/dsh/package.json', 'packaging/dsh/package-lock.json',
    lock.dependencies.glab.contract,
    ...assets.files.map(a => a.source),
    ...['package.mjs', 'package-lib.mjs', 'package-platform.mjs', 'package-profiles.mjs',
      'package-smoke.mjs', 'check-packaging.mjs', 'check-dsh-native.mjs',
      'package-release-source.mjs', 'package-signing.mjs',
      'fixtures/acp.mjs', 'fixtures/models.mjs'].map(f => `scripts/${f}`),
    'packaging/node-entitlements.plist'
  ])].sort().map(path => ({ path, sha256: hash(fs.readFileSync(p.join(root, path))) }));
  must(['dev', 'release'].includes(opts.channel), 'Unknown channel');
  must(!opts.candidate || opts.channel === 'release', '--candidate requires --channel release');
  must(!opts['macos-signing'] || opts.channel === 'release', '--macos-signing requires --channel release');
  signingPolicy(opts.target, opts['macos-signing']);
  must(Object.hasOwn(lock.profiles, opts.profile), 'Unknown profile');
  must(
    lock.targets[opts.target]?.delivery === 'included',
    'Unknown/excluded target'
  );
  must(
    opts.target ===
      `${process.platform}-${process.arch === 'x64' ? 'amd64' : process.arch}`,
    'Use a native target environment; cross-target DSH deployment is forbidden'
  );
  run(process.execPath, [
    p.join(root, 'scripts/check-packaging.mjs'),
    '--target',
    opts.target,
    '--profile',
    opts.profile
  ]);
  if (hasDependency(opts.profile, 'git', lock)) {
    must(!lock.targets[opts.target].blockers.includes('git_tls_runtime'),
      `full blocked for ${opts.target}: git_tls_runtime; ${lock.blockers.git_tls_runtime}`);
    must(
      lock.dependencies.git.redistribution.sources?.length >= 2,
      'full blocked: pinned Git corresponding source/build materials missing'
    );
  }
  const git = (...args) => run('git', args, { cwd: root }).trim();
  const commit = git('rev-parse', 'HEAD'),
    status = git('status', '--porcelain', '--untracked-files=all');
  const dirty = Boolean(status),
    diff = run('git', ['diff', '--binary', 'HEAD'], { cwd: root });
  if (opts.channel === 'release') {
    assertReleaseSource(root, opts.version, commit);
    // Candidates need archive-specific acceptance after construction. This flag
    // never makes them publishable; normal releases retain the readiness gate.
    if (!opts.candidate) run(process.execPath, [
      p.join(root, 'scripts/check-packaging.mjs'),
      '--require-ready',
      '--target',
      opts.target,
      '--profile',
      opts.profile
    ]);
  }
  const buildEnv = {
    ...process.env,
    CGO_ENABLED: '0',
    GOOS: process.platform,
    GOARCH: process.arch === 'x64' ? 'amd64' : process.arch,
    GOFLAGS: '-mod=readonly',
    GOWORK: 'off',
    GOTOOLCHAIN: 'local',
    ...(opts.offline ? { GOPROXY: 'off', GOSUMDB: 'off' } : {})
  };
  const localPackages = run(
    'go',
    [
      'list',
      '-deps',
      '-json',
      './cmd/teamx',
      './tools/gitlab',
      './tools/scion'
    ],
    { cwd: root, env: buildEnv }
  )
    .trim()
    .split(/\n}(?=\n\{)/)
    .map((s, i, a) => JSON.parse(i < a.length - 1 ? s + '\n}' : s));
  const sourceInputs = [];
  for (const pkg of localPackages) {
    if (!pkg.Dir || p.relative(root, pkg.Dir).startsWith('..')) continue;
    for (const file of [...(pkg.GoFiles || []), ...(pkg.EmbedFiles || [])]) {
      const path = p.join(pkg.Dir, file);
      sourceInputs.push({
        path: p.relative(root, path),
        sha256: hash(fs.readFileSync(path))
      });
    }
  }
  sourceInputs.sort((a, b) => a.path.localeCompare(b.path));
  const go = run('go', ['version'], { env: buildEnv }).trim();
  must(
    go.includes(`go${lock.build_tools.go.version} `),
    `Build requires Go ${lock.build_tools.go.version}`
  );
  const cache = p.resolve(
      opts['cache-dir'] || p.join(root, '.packaging/cache')
    ),
    out = p.resolve(opts['output-dir'] || p.join(root, 'dist'));
  fs.mkdirSync(cache, { recursive: true });
  fs.mkdirSync(out, { recursive: true });
  fs.mkdirSync(p.join(root, '.packaging'), { recursive: true });
  const version =
    opts.channel === 'release'
      ? opts.version
      : `dev-${commit.slice(0, 12)}${dirty ? '-dirty' : ''}`;
  const name = `teamx-${version}-${opts.target}-${opts.profile}`;
  const lockPath = p.join(out, `.${name}.build-lock`);
  try {
    fs.mkdirSync(lockPath);
  } catch (error) {
    throw Error(`Cannot acquire output lock ${lockPath}: ${error.code}`);
  }
  outputLock = lockPath;
  stage = fs.mkdtempSync(p.join(root, '.packaging/build-'));
  const bundle = p.join(stage, name);
  fs.mkdirSync(bundle);
  const candidate = p.join(out, `.${name}.pending.tar.gz`);
  must(
    !fs.existsSync(candidate),
    'An unaccepted archive already exists; inspect it before retrying'
  );
  pending = candidate;
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
    must(!opts.offline, `Offline cache missing: ${entry.archive}`);
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
  const nodeEntry = dep.node.platforms[opts.target];
  extract(
    await download(nodeEntry),
    p.join(bundle, nodeEntry.layout.destination),
    nodeEntry.layout.strip_components
  );
  const node = p.join(bundle, 'runtime/node/bin/node'),
    npm = p.join(bundle, 'runtime/node/lib/node_modules/npm/bin/npm-cli.js');
  must(
    run(node, ['--version']).trim() === dep.node.expected_version,
    'Node version mismatch'
  );
  must(
    run(node, [npm, '--version']).trim() === lock.build_tools.npm,
    'npm version mismatch'
  );
  const dsh = p.join(bundle, dep.dsh.layout.destination);
  fs.mkdirSync(dsh, { recursive: true });
  for (const file of ['package.json', 'package-lock.json'])
    fs.copyFileSync(p.join(root, 'packaging/dsh', file), p.join(dsh, file));
  const buildHome = p.join(stage, 'build-home');
  fs.mkdirSync(buildHome);
  const npmEnv = {
    ...process.env,
    HOME: buildHome,
    PATH: `${p.dirname(node)}:/usr/bin:/bin`,
    NPM_CONFIG_USERCONFIG: p.join(buildHome, 'npmrc'),
    NPM_CONFIG_GLOBALCONFIG: p.join(buildHome, 'global-npmrc'),
    NPM_CONFIG_CACHE: p.join(
      cache,
      `npm-${opts.target}-${dep.dsh.deployment_lock.sha256}`
    ),
    NPM_CONFIG_UPDATE_NOTIFIER: 'false'
  };
  // Native binaries ship in integrity-locked optional packages/prebuilds. Do not
  // allow lifecycle scripts to fetch mutable, unverified GitHub assets.
  console.log(
    'Deploying pinned DSH production closure (lifecycle scripts disabled)'
  );
  run(
    node,
    [
      npm,
      'ci',
      '--omit=dev',
      '--ignore-scripts',
      '--no-audit',
      '--no-fund',
      ...(opts.offline ? ['--offline'] : [])
    ],
    { cwd: dsh, env: npmEnv, stdio: 'inherit' }
  );
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
  for (const asset of assets.files) {
    const dest = p.join(bundle, asset.destination);
    fs.mkdirSync(p.dirname(dest), { recursive: true });
    fs.copyFileSync(p.join(root, asset.source), dest);
  }
  for (const dest of ['start', 'bin/dsh'])
    fs.chmodSync(p.join(bundle, dest), 0o755);
  await assemblePlatformDependencies({
    root, bundle, target: opts.target, profile: opts.profile, lock,
    glabLock: json(p.join(root, dep.glab.contract)), download
  });
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
  writeJSON(p.join(bundle, 'THIRD_PARTY_NOTICES/dsh-dependencies.json'), {
    packages: installed,
    notices
  });
  // Load the actual native addons without any model/platform network request.
  run(node, [p.join(root, 'scripts/check-dsh-native.mjs'), dsh], {
    cwd: buildHome,
    env: npmEnv
  });
  for (const build of assets.builds) {
    console.log(`Building ${build.destination}`);
    run(
      'go',
      [
        'build',
        ...(opts.channel === 'release' ? ['-trimpath'] : []),
        ...(build.destination === 'bin/teamx'
          ? [
              '-ldflags',
              `-X github.com/go-aie/teamx/internal/room.distributionVersion=${version} -X github.com/go-aie/teamx/internal/room.distributionCommit=${commit} -X github.com/go-aie/teamx/internal/room.distributionDirty=${dirty}`
            ]
          : []),
        '-o',
        p.join(bundle, build.destination),
        build.package
      ],
      { cwd: root, env: buildEnv }
    );
  }
  for (const input of [...sourceInputs, ...packagingInputs])
    must(
      hash(fs.readFileSync(p.join(root, input.path))) === input.sha256,
      `Source changed during build: ${input.path}`
    );
  const signing = opts.channel === 'release'
    ? signBundle(bundle, root, opts.target, opts['macos-signing'] || 'unsigned') : undefined;
  const dependencyRecords = {};
  for (const key of lock.profiles[opts.profile].required_dependencies) {
    const runtime = key === 'lark-cli' ? 'lark' : key;
    dependencyRecords[key] = {
      ...dep[key],
      ...(key === 'glab' ? { artifact: json(p.join(root, dep.glab.contract)).platforms[opts.target.replace('-', '_')] } : {}),
      runtime_sha256: hash(
        JSON.stringify(inventory(p.join(bundle, `runtime/${runtime}`)))
      )
    };
  }
  const assetHashes = assets.files.map((a) => ({
    path: a.destination,
    sha256: hash(fs.readFileSync(p.join(bundle, a.destination)))
  }));
  const manifest = {
    schema_version: 1,
    version,
    channel: opts.channel,
    ...(opts.candidate ? { distribution: { kind: 'release_candidate', release_ready: false, signing } } : {}),
    profile: opts.profile,
    target: opts.target,
    source: {
      commit,
      dirty,
      tracked_diff_sha256: hash(diff),
      build_inputs: sourceInputs,
      packaging_inputs: packagingInputs,
      allowlisted_files: assetHashes
    },
    go,
    dependencies: dependencyRecords,
    dependency_lock_sha256: hash(
      fs.readFileSync(p.join(root, 'packaging/dependencies.lock.json'))
    ),
    teams: assets.teams,
    system_requirements: lock.system_requirements,
    qualification_baseline: lock.targets[opts.target].qualification_baseline,
    ...(opts.profile === 'standard' && ['linux-amd64', 'darwin-arm64'].includes(opts.target) ? {
      server_deployment: { contract: 1,
        model_delivery_sha256: hash(fs.readFileSync(p.join(root, 'docs/implementation/model-deletion-migration-delivery.zh.md'))),
        acceptance: 'not_run' }
    } : {}),
    acceptance: {
      archive_smoke: 'see external report',
      real_dsh_acp: 'not_run',
      real_platforms: 'not_run',
      browser: 'not_run'
    },
    scion: 'deferred'
  };
  writeJSON(p.join(bundle, 'manifest.json'), manifest);
  checkBinaries(bundle, opts.target, opts.profile);
  const immutable = inventory(bundle);
  writeJSON(p.join(bundle, 'checksums.txt'), immutable);
  const notarization = signing ? notarizeBundle(bundle, stage, signing) : undefined;
  run('tar', ['-czf', pending, '-C', stage, name], {
    env: { ...process.env, COPYFILE_DISABLE: '1' }
  });
  const report = p.join(stage, 'smoke-report.json');
  run(
    process.execPath,
    [p.join(root, 'scripts/package-smoke.mjs'), pending, '--report', report],
    { stdio: 'inherit' }
  );
  for (const input of [...sourceInputs, ...packagingInputs])
    must(hash(fs.readFileSync(p.join(root, input.path))) === input.sha256,
      `Source changed during archive verification: ${input.path}`);
  if (opts.channel === 'release') assertReleaseSource(root, opts.version, commit);
  const archive = p.join(out, name + '.tar.gz');
  // Never overwrite a final versioned candidate with newly rebuilt bytes.
  if (opts.channel === 'release') must(
    ![archive, archive + '.sha256', p.join(out, name + '-report.json'), p.join(out, name + '-security.json')].some(f => fs.existsSync(f)),
    'Versioned output already exists; choose a fresh output directory');
  fs.renameSync(pending, archive);
  pending = undefined;
  const archiveHash = hash(fs.readFileSync(archive));
  fs.writeFileSync(
    archive + '.sha256',
    `${archiveHash}  ${p.basename(archive)}\n`
  );
  fs.copyFileSync(report, p.join(out, name + '-report.json'));
  if (signing) writeJSON(p.join(out, name + '-security.json'), {
    schema_version: 1, archive_sha256: archiveHash, target: opts.target, profile: opts.profile,
    signing, notarization, downloaded_machine_gatekeeper: 'not_run'
  });
  console.log(`Verified archive: ${archive}\nSHA256: ${archiveHash}`);
} catch (error) {
  console.error(`Package failed: ${error.message}`);
  if (pending && fs.existsSync(pending)) {
    const failed = p.join(root, '.packaging/last-failed.tar.gz');
    fs.renameSync(pending, failed);
    pending = undefined;
    console.error(`Unaccepted candidate retained for diagnostics: ${failed}`);
  }
  process.exitCode = 1;
} finally {
  if (pending) fs.rmSync(pending, { force: true });
  if (stage) fs.rmSync(stage, { recursive: true, force: true });
  if (outputLock) fs.rmdirSync(outputLock);
}
