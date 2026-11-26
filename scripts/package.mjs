import * as fs from 'node:fs';
import * as p from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  hash,
  json,
  writeJSON,
  run,
  must,
  inventory,
  inventoryFile,
  inventorySubset
} from './package-lib.mjs';
import { checkBinaries } from './package-platform.mjs';
import { hasDependency } from './package-profiles.mjs';
import { assertReleaseSource, releaseBatch } from './package-release-source.mjs';
import { signingPolicy, signBundle, notarizeBundle } from './package-signing.mjs';
import { compressArchive } from './package-archive.mjs';
import { preparePackageDependencies } from './package-dependencies.mjs';
import { progress } from './package-progress.mjs';
const root = p.resolve(p.dirname(fileURLToPath(import.meta.url)), '..');
// Public distribution files must be readable/executable by the service account,
// even when the operator protects deployment evidence with umask 077.
process.umask(0o022);
const opts = {
  channel: 'dev',
  profile: 'standard',
  target: `${process.platform}-${process.arch === 'x64' ? 'amd64' : process.arch}`,
  offline: false
};
let stage, pending, outputLock, timingFile;
const timing = progress('package');
const totalStarted = performance.now();
let finishStage = timing.start('preflight and source inputs');
try {
  const args = process.argv.slice(2);
  while (args.length) {
    const arg = args.shift();
    if (arg === '--offline') opts.offline = true;
    else if (
      [
        '--channel',
        '--profile',
        '--target',
        '--version',
        '--macos-signing',
        '--smoke',
        '--cache-dir',
        '--output-dir'
      ].includes(arg)
    ) {
      must(args.length && !args[0].startsWith('--'), `Missing ${arg}`);
      opts[arg.slice(2)] = args.shift();
    } else if (arg === '--help') {
      console.log(
        'package.sh [--channel dev|release] [--smoke quick|full] [--profile core|standard|full] [--target OS-ARCH] [--offline] [--cache-dir DIR] [--output-dir DIR] [--version vX.Y.Z] [--macos-signing unsigned|developer-id]'
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
    'packaging/srt/package.json', 'packaging/srt/package-lock.json', 'packaging/srt/runtime.lock.json',
    lock.dependencies.glab.contract,
    ...assets.files.map(a => a.source),
    ...['package.mjs', 'package-lib.mjs', 'package-platform.mjs', 'package-profiles.mjs',
      'package-dependencies.mjs', 'package-archive.mjs', 'package-smoke.mjs', 'package-quick.mjs', 'package-cache.mjs', 'package-progress.mjs', 'check-packaging.mjs', 'check-dsh-native.mjs',
      'package-release-source.mjs', 'package-signing.mjs', 'package-srt.mjs',
      'fixtures/acp.mjs', 'fixtures/models.mjs'].map(f => `scripts/${f}`),
    'packaging/node-entitlements.plist'
  ])].sort().map(path => ({ path, sha256: hash(fs.readFileSync(p.join(root, path))) }));
  must(['dev', 'release'].includes(opts.channel), 'Unknown channel');
  opts.smoke ??= opts.channel === 'release' ? 'full' : 'quick';
  must(['quick','full'].includes(opts.smoke), '--smoke must be quick or full');
  must(opts.channel !== 'release' || opts.smoke === 'full', 'Official releases require full smoke');
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
  const batch = opts.channel === 'release' ? releaseBatch() : undefined;
  if (opts.channel === 'release') assertReleaseSource(root, opts.version, commit);
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
  finishStage();
  finishStage = undefined;
  const lockPath = p.join(out, `.${name}.build-lock`);
  try {
    fs.mkdirSync(lockPath);
  } catch (error) {
    throw Error(`Cannot acquire output lock ${lockPath}: ${error.code}`);
  }
  outputLock = lockPath;
  timingFile = p.join(out, name + '-timing.json');
  stage = fs.mkdtempSync(p.join(root, '.packaging/build-'));
  const bundle = p.join(stage, name);
  fs.mkdirSync(bundle);
  const candidate = p.join(out, `.${name}.pending.tar.gz`);
  must(
    !fs.existsSync(candidate),
    'An unaccepted archive already exists; inspect it before retrying'
  );
  pending = candidate;
  const buildHome = p.join(stage, 'build-home');
  fs.mkdirSync(buildHome);
  const dep = lock.dependencies;
  const { srt } = await timing.run('prepare dependencies', () => preparePackageDependencies({
    root, bundle, cache, target: opts.target, profile: opts.profile, lock,
    buildHome, offline: opts.offline, timing
  }));
  timing.step('copy public assets', () => {
    for (const asset of assets.files) {
      const dest = p.join(bundle, asset.destination);
      fs.mkdirSync(p.dirname(dest), { recursive: true });
      fs.copyFileSync(p.join(root, asset.source), dest);
    }
    for (const dest of ['start', 'install.sh', 'bin/dsh'])
      fs.chmodSync(p.join(bundle, dest), 0o755);
  });
  for (const build of assets.builds) {
    console.log(`Building ${build.destination}`);
    timing.step(`compile ${build.destination}`, () => run(
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
    ));
  }
  for (const input of [...sourceInputs, ...packagingInputs])
    must(
      hash(fs.readFileSync(p.join(root, input.path))) === input.sha256,
      `Source changed during build: ${input.path}`
    );
  const signing = opts.channel === 'release'
    ? signBundle(bundle, root, opts.target, opts['macos-signing'] || 'unsigned') : undefined;
  finishStage = timing.start('inventory and manifest');
  const bundleInventory = timing.step('scan bundle contents', () => inventory(bundle));
  const dependencyRecords = {};
  for (const key of lock.profiles[opts.profile].required_dependencies) {
    const runtime = key === 'lark-cli' ? 'lark' : key;
    dependencyRecords[key] = {
      ...dep[key],
      ...(key === 'glab' ? { artifact: json(p.join(root, dep.glab.contract)).platforms[opts.target.replace('-', '_')] } : {}),
      runtime_sha256: hash(
        JSON.stringify(inventorySubset(bundle, bundleInventory, `runtime/${runtime}`))
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
    ...(opts.channel === 'release' ? { distribution: { schema_version: 2, kind: 'release_archive', signing,
      ...(batch ? { build: batch } : {}) } } : {}),
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
    ...(srt ? { execution_runtimes: { srt } } : {}),
    dependency_lock_sha256: hash(
      fs.readFileSync(p.join(root, 'packaging/dependencies.lock.json'))
    ),
    teams: assets.teams,
    system_requirements: lock.system_requirements,
    qualification_baseline: lock.targets[opts.target].qualification_baseline,
    ...(opts.profile === 'standard' && ['linux-amd64', 'darwin-arm64'].includes(opts.target) ? {
      server_deployment: { contract: 1,
        model_delivery_sha256: hash(fs.readFileSync(p.join(root, 'docs/implementation/model-deletion-migration-delivery.zh.md'))),
      }
    } : {}),
    scion: 'deferred'
  };
  writeJSON(p.join(bundle, 'manifest.json'), manifest);
  checkBinaries(bundle, opts.target, opts.profile);
  // Preserve directory traversal order without reading dependency bytes again.
  const immutable = [...bundleInventory, inventoryFile(bundle, 'manifest.json')].sort((a, b) => {
    const x = a.path.replaceAll('/', '\0'), y = b.path.replaceAll('/', '\0');
    return x < y ? -1 : x > y ? 1 : 0;
  });
  writeJSON(p.join(bundle, 'checksums.txt'), immutable);
  finishStage();
  finishStage = undefined;
  const notarization = signing ? notarizeBundle(bundle, stage, signing) : undefined;
  await timing.run('compress archive', () => compressArchive(pending, stage, name, { level: opts.channel === 'release' ? 6 : 1 }));
  const report = p.join(stage, 'smoke-report.json');
  await timing.run(opts.smoke === 'full' ? 'full archive smoke' : 'application check', () => run(
    process.execPath,
    opts.smoke === 'full' ? [p.join(root, 'scripts/package-smoke.mjs'), pending, '--report', report]
      : [p.join(root, 'scripts/package-quick.mjs'), bundle, report],
    { stdio: 'inherit' }
  ));
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
  const checked = json(report);
  checked.smoke_level = opts.smoke;
  checked.archive_sha256 = archiveHash;
  if(opts.smoke === 'quick') {
    for(const [field,file] of Object.entries({smoke_sha256:'package-smoke.mjs',quick_sha256:'package-quick.mjs',srt_check_sha256:'package-srt.mjs',native_check_sha256:'check-dsh-native.mjs',fixture_sha256:'fixtures/acp.mjs'}))
      checked[field]=hash(fs.readFileSync(p.join(root,'scripts',file)));
  }
  writeJSON(p.join(out, name + '-report.json'), checked);
  if (signing) writeJSON(p.join(out, name + '-security.json'), {
    schema_version: 1, archive_sha256: archiveHash, target: opts.target, profile: opts.profile,
    signing, notarization, downloaded_machine_gatekeeper: 'not_run'
  });
  console.log(`Verified archive: ${archive}\nSHA256: ${archiveHash}`);
} catch (error) {
  finishStage?.(error);
  finishStage = undefined;
  console.error(`Package failed: ${error.message}`);
  if (pending && fs.existsSync(pending)) {
    const failed = p.join(root, '.packaging/last-failed.tar.gz');
    fs.renameSync(pending, failed);
    pending = undefined;
    console.error(`Failed archive retained for diagnostics: ${failed}`);
  }
  process.exitCode = 1;
} finally {
  try {
    timing.step('cleanup build workspace', () => {
      if (pending) fs.rmSync(pending, { force: true });
      if (stage) fs.rmSync(stage, { recursive: true, force: true });
      if (outputLock) fs.rmdirSync(outputLock);
    });
  } finally {
    const elapsed = Math.round(performance.now() - totalStarted);
    console.log(`[package] total: ${(elapsed / 1000).toFixed(2)}s`);
    if (timingFile) writeJSON(timingFile, { total_ms: elapsed, stages: timing.records });
  }
}
