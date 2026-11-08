// Offline P0 inventory validation. This does not construct or accept a bundle.
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { readFileSync, realpathSync, lstatSync } from 'node:fs';
import { dirname, isAbsolute, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const read = (path) => readFileSync(resolve(root, path));
const json = (path) => JSON.parse(read(path));
const digest = (path) => createHash('sha256').update(read(path)).digest('hex');
const check = (condition, message) => assert(condition, message);
const same = (actual, expected, message) => assert.deepEqual(actual, expected, message);
const sha = (value) => check(/^[a-f0-9]{64}$/.test(value), `Invalid SHA-256: ${value}`);
const localFile = (path) => {
  check(typeof path === 'string' && path.length > 0 && !isAbsolute(path), `Not a relative file: ${path}`);
  check(!path.split('/').some((part) => part === '..' || part === ''), `Unsafe path: ${path}`);
  const absolute = resolve(root, path);
  check(lstatSync(absolute).isFile(), `Not a regular file: ${path}`);
  check(!relative(root, realpathSync(absolute)).startsWith('..'), `File escapes repository: ${path}`);
};
const https = (value) => {
  const url = new URL(value);
  check(url.protocol === 'https:' && !url.username && !url.password && !url.search, `Unsafe dependency URL: ${value}`);
  check(!/\/(latest|main|master)(\/|$)/.test(url.pathname), `Floating dependency URL: ${value}`);
};

try {
  let target;
  let profile = 'full';
  let requireReady = false;
  const args = process.argv.slice(2);
  while (args.length) {
    const arg = args.shift();
    if (arg === '--target') target = args.shift();
    else if (arg === '--profile') profile = args.shift();
    else if (arg === '--require-ready') requireReady = true;
    else throw new Error(`Unknown argument: ${arg}`);
    if ((arg === '--target' && !target) || (arg === '--profile' && !profile)) throw new Error(`Missing value: ${arg}`);
  }
  const lock = json('packaging/dependencies.lock.json');
  const assets = json('packaging/bundle-files.json');
  const glab = json('tools/gitlab/glab.lock.json');
  const scn = json('tools/scion/scn.lock.json');
  same(lock.schema_version, 1);
  same(assets.schema_version, 1);
  check(Object.hasOwn(lock.profiles, profile), `Unknown profile: ${profile}`);
  const targets = Object.keys(lock.targets);
  same([...targets].sort(), ['darwin-amd64', 'darwin-arm64', 'linux-amd64', 'linux-arm64']);
  if (target) check(Object.hasOwn(lock.targets, target), `Unknown target: ${target}`);
  for (const required of ['darwin-arm64', 'linux-amd64']) {
    check(lock.targets[required].required_by_plan && lock.targets[required].delivery === 'included', `Required target removed: ${required}`);
  }

  const dependencies = lock.dependencies;
  same(dependencies.glab.version, glab.version, 'glab lock version drift');
  same(dependencies.glab.commit, glab.source_commit, 'glab source commit drift');
  check(glab.source_commit.startsWith(glab.commit), 'glab short commit drift');
  check(read('tools/gitlab/main.go').toString().includes(`const glabVersion = "${glab.version}"`), 'glab runtime contract drift');
  check(read('internal/platform/lark/client.go').toString().includes(`const CLIVersion = "${dependencies['lark-cli'].version}"`), 'lark CLI runtime contract drift');
  same(dependencies.scn.version, scn.version, 'scn wrapper contract drift');
  same(dependencies.scn.status, 'deferred');
  same(dependencies.scn.included_profiles, []);
  same(dependencies.dsh.version, '0.2.2-rc.1');
  same(dependencies.dsh.tag, 'dsh-v0.2.2-rc.1');
  same(dependencies.dsh.prerelease, true);
  check(dependencies.node.version.split('.').map(Number)[0] >= 24, 'Node does not satisfy DSH minimum');
  const [major, minor] = dependencies.node.version.split('.').map(Number);
  check(major > 24 || minor >= 9, 'DSH requires Node >=24.9');
  same(read('go.mod').toString().match(/^go (\S+)$/m)?.[1], lock.build_tools.go.version.split('.').slice(0, 2).join('.'), 'Go module contract drift');

  for (const [name, dependency] of Object.entries(dependencies)) {
    check(/^\d+\.\d+\.\d+(?:-[\w.]+)?$/.test(dependency.version), `Unpinned version: ${name}`);
    if (dependency.status === 'deferred') continue;
    if (dependency.commit) check(/^[a-f0-9]{40}$/.test(dependency.commit), `Incomplete commit: ${name}`);
    localFile(dependency.license.file);
    sha(dependency.license.sha256);
    same(digest(dependency.license.file), dependency.license.sha256, `License digest drift: ${name}`);
    https(dependency.license.source);
    for (const blocker of dependency.blockers) check(Object.hasOwn(lock.blockers, blocker), `Unknown blocker: ${blocker}`);
    if (name === 'dsh') continue;
    for (const t of targets) {
      const entry = name === 'glab' ? glab.platforms[t.replace('-', '_')] : dependency.platforms[t];
      check(entry, `Missing ${name} artifact for ${t}`);
      sha(entry.sha256);
      https(entry.url);
      check(entry.url.endsWith('/' + entry.archive), `Archive URL mismatch: ${name}/${t}`);
      check(entry.archive.includes(dependency.version), `Archive version mismatch: ${name}/${t}`);
    }
  }

  const dsh = dependencies.dsh;
  const gitSources = dependencies.git.redistribution.sources;
  check(Array.isArray(gitSources) && gitSources.length === 2, 'Git source/build inputs missing');
  same(gitSources.map(s => s.destination), ['dugite', 'dugite/git']);
  for (const [i, entry] of gitSources.entries()) {
    https(entry.url);
    sha(entry.sha256);
    const commit = i === 0 ? dependencies.git.commit : dependencies.git.source_git_commit;
    check(entry.url.endsWith('/' + commit) && entry.archive.includes(commit), 'Git source commit drift');
  }
  for (const notice of dependencies.git.redistribution.component_notices) {
    localFile(notice.file);
    sha(notice.sha256);
    same(digest(notice.file), notice.sha256, `Git component notice drift: ${notice.component}`);
    https(notice.source);
    check(assets.files.some(f => f.source === notice.file), `Git component notice missing from allowlist: ${notice.component}`);
  }
  https(dsh.artifact.url);
  sha(dsh.artifact.sha256);
  sha(dsh.upstream_lock.sha256);
  const deployment = dsh.deployment_lock;
  for (const [path, expected] of [[deployment.file, deployment.sha256], [deployment.package_json, deployment.package_json_sha256]]) {
    localFile(path);
    same(digest(path), expected, `DSH deployment input digest drift: ${path}`);
  }
  const npmLock = json(deployment.file);
  same(npmLock.lockfileVersion, 3);
  same(json(deployment.package_json).dependencies, { '@deepseek-ai/dsh': dsh.version });
  same(npmLock.packages[''].dependencies, { '@deepseek-ai/dsh': dsh.version });
  same(npmLock.packages['node_modules/@deepseek-ai/dsh'].integrity, dsh.artifact.integrity);
  same(Object.keys(npmLock.packages).length - 1, deployment.package_count);
  for (const [path, pkg] of Object.entries(npmLock.packages)) {
    if (!path) continue;
    check(!pkg.link && /^https:\/\/registry\.npmjs\.org\//.test(pkg.resolved), `Non-registry npm dependency: ${path}`);
    check(/^sha512-[A-Za-z0-9+/]+=*$/.test(pkg.integrity), `Missing strong npm integrity: ${path}`);
    check(/^\d+\.\d+\.\d+(?:[-+][\w.+-]+)?$/.test(pkg.version), `Unpinned npm version: ${path}`);
  }
  for (const t of targets) {
    const native = dsh.platforms[t];
    same(npmLock.packages['node_modules/' + native.native_system_package]?.version, native.native_system_version, `Missing native system package: ${t}`);
    for (const prefix of ['@img/sharp-', '@img/sharp-libvips-', '@vscode/ripgrep-', '@koromix/koffi-']) {
      check(npmLock.packages['node_modules/' + prefix + t.replace('amd64', 'x64')], `Missing native package: ${prefix}${t}`);
    }
    check(lock.targets[t].runner && lock.targets[t].qualification_baseline, `Missing native environment: ${t}`);
    for (const blocker of lock.targets[t].blockers) check(Object.hasOwn(lock.blockers, blocker), `Unknown target blocker: ${blocker}`);
  }

  const destinations = new Set();
  const sources = new Set();
  for (const file of assets.files) {
    localFile(file.source);
    check(!isAbsolute(file.destination) && !file.destination.split('/').some((part) => part === '..' || part === ''), `Unsafe destination: ${file.destination}`);
    check(!destinations.has(file.destination), `Duplicate destination: ${file.destination}`);
    check(!sources.has(file.source), `Duplicate source: ${file.source}`);
    check(!/^(?:\.teamx\/(?:config\.toml|data(?:\/|$))|\.env(?:\.|$)|bin\/|\.local\/|dist\/|\.git\/)/.test(file.source), `Private/uncontrolled asset: ${file.source}`);
    check(!file.destination.startsWith('.teamx/'), `Runtime home must not be shipped: ${file.destination}`);
    destinations.add(file.destination);
    sources.add(file.source);
  }
  const tracked = execFileSync('git', ['ls-files', '-z', '--', '.teamx/teams'], { cwd: root, encoding: 'utf8' }).split('\0').filter(Boolean).sort();
  same(assets.files.map((file) => file.source).filter((path) => path.startsWith('.teamx/teams/')).sort(), tracked, 'Team resources missing or unreviewed; update explicit allowlist');
  same(assets.teams, tracked.filter((path) => path.endsWith('/team.json')).map((path) => path.split('/')[2]).sort());
  for (const build of assets.builds) {
    localFile(build.package + '/main.go');
    check(!destinations.has(build.destination), `Duplicate executable: ${build.destination}`);
    destinations.add(build.destination);
  }
  for (const path of assets.embedded_resources) localFile(path);
  for (const notice of lock.go_module_notices) {
    localFile(notice.license_file);
    same(digest(notice.license_file), notice.license_sha256, `Go license digest drift: ${notice.module}`);
    check(sources.has(notice.license_file), `Go license missing from asset list: ${notice.module}`);
    check(read('go.sum').toString().split('\n').includes(`${notice.module} ${notice.version} ${notice.sum}`), `Go module sum drift: ${notice.module}`);
  }
  for (const policy of Object.values(lock.profiles)) {
    check(!policy.required_dependencies.includes('scn'), 'Scion must remain deferred');
    for (const name of policy.required_dependencies) check(dependencies[name], `Unknown profile dependency: ${name}`);
  }
  console.log(`P0 inventory valid: ${targets.length} targets, ${assets.teams.length} teams, ${deployment.package_count} locked npm packages.`);
  if (requireReady) {
    const blockers = new Set();
    for (const name of lock.profiles[profile].required_dependencies) for (const b of dependencies[name].blockers) blockers.add(b);
    for (const t of target ? [target] : targets) {
      for (const b of lock.targets[t].blockers) {
        if (!['git_redistribution_materials', 'git_tls_runtime'].includes(b) || profile === 'full') blockers.add(b);
      }
      if (lock.targets[t].support !== 'accepted') blockers.add('native_runtime_acceptance');
    }
    if (blockers.size) {
      console.error(`NOT READY (${target || 'all targets'}/${profile}):\n` + [...blockers].map((b) => `- ${b}: ${lock.blockers[b]}`).join('\n'));
      process.exitCode = 2;
    }
  }
} catch (error) {
  console.error(`Invalid packaging inventory: ${error.message}`);
  process.exitCode = 1;
}
