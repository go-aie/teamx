import * as fs from 'node:fs';
import * as p from 'node:path';
import * as os from 'node:os';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { fileURLToPath } from 'node:url';
import {
  hash,
  json,
  writeJSON,
  run,
  must,
  inventory,
  extract
} from './package-lib.mjs';
import { checkBinaries } from './package-platform.mjs';
import { checkManifestProfile, hasDependency } from './package-profiles.mjs';
const scriptRoot = p.dirname(fileURLToPath(import.meta.url));
const args = process.argv.slice(2),
  archive = p.resolve(args.shift() || '');
let reportPath;
if (args.length) {
  must(
    args.shift() === '--report' && args.length === 1,
    'Usage: package-smoke.sh ARCHIVE [--report FILE]'
  );
  reportPath = p.resolve(args.shift());
}
const temp = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx archive smoke ')),
  children = new Set();
const checks = [];
const pass = (name) => {
  checks.push(name);
  console.log(`Smoke: ${name}`);
};
const pause = () => new Promise((resolve) => setTimeout(resolve, 100));
async function until(fn, description) {
  const deadline = Date.now() + 30000;
  let last;
  while (Date.now() < deadline) {
    try {
      const value = await fn();
      if (value) return value;
    } catch (e) {
      last = e;
    }
    await pause();
  }
  throw Error(`Timed out: ${description}${last ? ': ' + last.message : ''}`);
}
try {
  const archiveHash = hash(fs.readFileSync(archive));
  const extracted = p.join(temp, 'extracted');
  extract(archive, extracted);
  const entries = fs.readdirSync(extracted);
  must(entries.length === 1, 'Archive must have a single root');
  const bundle = fs.realpathSync(p.join(extracted, entries[0])),
    manifest = json(p.join(bundle, 'manifest.json'));
  must(
    manifest.target ===
      `${process.platform}-${process.arch === 'x64' ? 'amd64' : process.arch}`,
    'Archive architecture differs from native smoke host'
  );
  must(
    !fs.existsSync(p.join(bundle, '.teamx')),
    'Private runtime home in archive'
  );
  const expected = json(p.join(bundle, 'checksums.txt')),
    actual = inventory(bundle).filter((f) => f.path !== 'checksums.txt');
  must(
    JSON.stringify(actual) === JSON.stringify(expected),
    'Immutable contents/permissions/checksums differ'
  );
  checkManifestProfile(manifest);
  checkBinaries(bundle, manifest.target, manifest.profile);
  for (const item of actual) {
    must(
      !/(?:^|\/)(?:\.env(?:\..*)?|\.git|\.local|\.teamx)(?:\/|$)/.test(
        item.path
      ),
      `Private file: ${item.path}`
    );
  }
  for (const name of [
    'start',
    'bin/teamx',
    'bin/teamx-gitlab',
    'bin/teamx-scion',
    'bin/dsh',
    'runtime/node/bin/node'
  ])
    must(
      (fs.statSync(p.join(bundle, name)).mode & 0o111) !== 0,
      `Not executable: ${name}`
    );
  pass('archive contents, hashes, modes and internal symlinks');
  const system = p.join(temp, 'system-tools'),
    home = p.join(temp, 'isolated-home'),
    cwd = p.join(temp, 'unrelated cwd');
  for (const dir of [system, home, cwd]) fs.mkdirSync(dir);
  for (const name of ['dirname', 'sh', 'basename', 'uname', 'sed'])
    fs.symlinkSync(name === 'sh' ? '/bin/sh' : `/usr/bin/${name}`, p.join(system, name));
  const env = {
    HOME: home,
    PATH: system,
    LANG: 'en_US.UTF-8',
    TMPDIR: temp,
    OPENAI_API_KEY: 'packaging-fixture-key',
    TEAMX_PACKAGING_FIXTURE_LOG: p.join(temp, 'fixture.jsonl')
  };
  const exec = (name, argv = [], options = {}) =>
    run(p.join(bundle, name), argv, { cwd, env, timeout: 30000, ...options });
  must(
    exec('bin/teamx', ['--version']).startsWith(
      `teamx ${manifest.version} commit=${manifest.source.commit} dirty=${manifest.source.dirty}`
    ),
    'TeamX build identity mismatch'
  );
  must(
    exec('runtime/node/bin/node', ['--version']).trim() ===
      manifest.dependencies.node.expected_version,
    'Node version mismatch'
  );
  must(
    exec('bin/dsh', ['--version']).trim() ===
      manifest.dependencies.dsh.expected_version,
    'DSH version mismatch'
  );
  for (const [name, argv] of [
    ['bin/teamx', ['--help']],
    ['bin/teamx-gitlab', ['work-items', 'view', '--help']],
    ['bin/dsh', ['--help']]
  ])
    exec(name, argv);
  {
    for (const [name, dependency] of [
      ...(!hasDependency(manifest.profile, 'glab') ? [['bin/teamx-gitlab', 'glab']] : []),
      ['bin/teamx-scion', 'scn']
    ]) {
      let absent = false;
      try {
        exec(name, ['--help'], { stdio: ['ignore', 'pipe', 'pipe'] });
      } catch (e) {
        absent = e.status === 127 && String(e.stderr).includes(dependency);
      }
      must(absent, `Expected missing optional dependency guidance: ${name}`);
    }
  }
  for (const [name, argv, key] of [
    ['bin/glab', ['version'], 'glab'], ['bin/lark-cli', ['--version'], 'lark-cli'], ['bin/git', ['--version'], 'git']
  ]) {
    if (hasDependency(manifest.profile, key)) {
      const value = exec(name, argv).trim(), dep = manifest.dependencies[key];
      must(dep && (dep.expected_version ? value === dep.expected_version : value.startsWith(dep.expected_version_prefix + ' ')), `Bundled runtime version mismatch: ${key}`);
    }
  }
  if (hasDependency(manifest.profile, 'glab')) {
    exec('bin/glab', ['--help']); exec('bin/teamx-gitlab', ['--help']);
  }
  if (hasDependency(manifest.profile, 'lark-cli')) exec('bin/lark-cli', ['--help']);
  if (hasDependency(manifest.profile, 'git')) {
    exec('bin/git', ['help', '-a']); exec('bin/git', ['lfs', 'version']);
    const gitRoot = p.join(bundle, 'runtime/git');
    let httpsHelperLoaded = false;
    try { exec('runtime/git/libexec/git-core/git-remote-https', [], { stdio: ['ignore', 'pipe', 'pipe'] }); }
    catch (error) { httpsHelperLoaded = error.status === 1 && /usage/i.test(String(error.stderr)); }
    must(httpsHelperLoaded, 'Git HTTPS helper cannot load its TLS/system libraries');
    if (fs.existsSync(p.join(gitRoot, 'libexec/git-core/git-credential-manager')))
      must(exec('bin/git', ['credential-manager', '--version']).trim().startsWith(manifest.dependencies.git.bundled_components['git-credential-manager']), 'GCM version mismatch');
    must(exec('bin/git', ['--exec-path']).trim() === p.join(gitRoot, 'libexec/git-core'), 'Git exec-path did not relocate');
    const repo = p.join(temp, 'offline git repository');
    exec('bin/git', ['init', '--quiet', repo]);
    must(fs.existsSync(p.join(repo, '.git/hooks/pre-commit.sample')), 'Git templates did not relocate');
    must(exec('bin/git', ['-C', repo, 'hash-object', '--stdin']).trim() === 'e69de29bb2d1d6434b8b29ae775ad8c2e48c5391', 'Git object helper failed');
    const material = json(p.join(bundle, 'THIRD_PARTY_NOTICES/git-materials.json'));
    must(material.source_tree_sha256 === hash(JSON.stringify(inventory(p.join(bundle, 'THIRD_PARTY_NOTICES/git-source')))), 'Git source material digest mismatch');
    pass('Git helpers/templates and corresponding source without host Git');
  }
  pass('declared CLI versions/help without host tools');
  exec('runtime/node/bin/node', [
    p.join(scriptRoot, 'check-dsh-native.mjs'),
    p.join(bundle, 'runtime/dsh')
  ]);
  pass(
    'bundled runtime versions and CLI help without host Go/Node/platform CLIs'
  );
  const start = p.join(bundle, 'start');
  exec('start', ['init', '--non-interactive']);
  const portable = p.join(bundle, '.teamx'),
    config = p.join(portable, 'config.toml');
  const initialized = fs.readFileSync(config, 'utf8');
  let failed = false;
  try {
    exec('start', [], { stdio: ['ignore', 'pipe', 'pipe'] });
  } catch (e) {
    failed = true;
    const output = String(e.stdout) + String(e.stderr);
    for (const field of ['runtime.model.base_url', 'runtime.model.model_id'])
      must(output.includes(field), `Missing exact diagnostic ${field}`);
  }
  must(failed, 'Unconfigured start must fail');
  exec('start', ['init', '--non-interactive']);
  must(fs.readFileSync(config, 'utf8') === initialized, 'init replaced config');
  // Validate all distributed teams, and run ordinary manager HTTP against the real DSH
  // configuration without ever opening a DSH session or sending a prompt.
  const configure = (file, binary) => {
    let s = fs
      .readFileSync(file, 'utf8')
      .replace("base_url = ''", "base_url = 'http://127.0.0.1:1/v1'")
      .replace("model_id = ''", "model_id = 'packaging-fixture'");
    s = s.replace(/listen = [^\n]+/, "listen = '127.0.0.1:0'");
    if (binary)
      s = s.replace(
        /binary_path = [^\n]+/,
        `binary_path = ${JSON.stringify(binary)}`
      );
    fs.writeFileSync(file, s, { mode: 0o600 });
  };
  configure(config);
  const doctor = JSON.parse(exec('start', ['doctor']));
  must(doctor.ready, 'Configured bundle must be ready');
  must(
    doctor.checks.find((c) => c.name === 'git').status === (hasDependency(manifest.profile, 'git') ? 'ok' : 'missing'),
    'Git capability differs from archive profile'
  );
  for (const name of ['glab', 'lark-cli', 'git']) {
    const check = doctor.checks.find(c => c.name === name);
    must(check.required === hasDependency(manifest.profile, name) &&
      check.status === (hasDependency(manifest.profile, name) ? 'ok' : 'missing'), `Doctor capability differs: ${name}`);
  }
  const template = p.join(portable, 'teams/bookstore-night/prompts/alan.md');
  fs.appendFileSync(template, '\nuser change\n');
  const changed = fs.readFileSync(template, 'utf8');
  exec('start', ['init', '--non-interactive']);
  must(fs.readFileSync(template, 'utf8') === changed, 'init replaced template');
  pass('portable init, exact model diagnostics, optional Git and preservation');
  async function launch(homeArg = []) {
    const child = spawn(start, homeArg, {
      cwd,
      env,
      stdio: ['ignore', 'pipe', 'pipe']
    });
    children.add(child);
    let output = '',
      errors = '';
    child.stdout.on('data', (b) => (output += b));
    child.stderr.on('data', (b) => (errors += b));
    const exited = once(child, 'exit');
    const info = await until(() => {
      for (const line of output.split('\n')) {
        try {
          const obj = JSON.parse(line);
          if (obj.endpoint) return obj;
        } catch {}
      }
      if (child.exitCode !== null) throw Error(`Service exited: ${errors}\n${output}`);
    }, 'service readiness');
    return { child, info, exited, errors: () => errors };
  }
  async function stop(service, signal = 'SIGTERM') {
    service.child.kill(signal);
    const result = await Promise.race([
      service.exited,
      new Promise((_, reject) => {
        const timer = setTimeout(
          () => reject(Error('Service shutdown timeout')),
          35000
        );
        timer.unref();
      })
    ]);
    children.delete(service.child);
    must(result[0] === 0, `Shutdown failed: ${service.errors()}`);
    await until(async () => {
      try {
        await fetch(service.info.endpoint + '/v1/rooms', {
          signal: AbortSignal.timeout(300)
        });
        return false;
      } catch {
        return true;
      }
    }, 'port release');
  }
  const request = async (service, path, body) => {
    const response = await fetch(service.info.endpoint + path, {
      method: body ? 'POST' : 'GET',
      ...(body
        ? {
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body)
          }
        : {}),
      signal: AbortSignal.timeout(3000)
    });
    const text = await response.text();
    must(response.ok, `HTTP ${path}: ${response.status}: ${text}`);
    return path === '/' ? text : JSON.parse(text);
  };
  let service = await launch();
  const catalog = await request(service, '/v1/rooms');
  must(
    catalog.teams
      .map((t) => t.id)
      .sort()
      .join() === manifest.teams.slice().sort().join(),
    'Not all templates load'
  );
  must((await request(service, '/')).includes('<html'), 'Homepage unavailable');
  // Same home cannot serve through a second launcher, even before port binding.
  let locked = false;
  try {
    exec('start', [], { stdio: ['ignore', 'pipe', 'pipe'], timeout: 30000 });
  } catch (e) {
    locked = String(e.stderr).includes('state directory already in use');
  }
  must(locked, 'Second home owner was not refused');
  const room = await request(service, '/v1/rooms/create', {
    service_epoch: catalog.service_epoch,
    caller: { kind: 'human', id: 'packaging-smoke' },
    team_id: 'takeout-dinner',
    create_id: 'packaging-smoke'
  });
  await stop(service, 'SIGINT');
  service = await launch();
  must(
    (await request(service, '/v1/rooms')).rooms.some(
      (r) => r.room_id === room.room_id
    ),
    'Room lost after restart'
  );
  await stop(service);
  pass(
    'HTTP homepage, all teams, Room creation, home lock, SIGINT/SIGTERM and persistence'
  );
  // A separate explicit home exercises ACP with a fixed fixture. The real bundled
  // DSH already passed version/help checks above; this is not model acceptance.
  const external = p.join(temp, 'external home');
  exec('start', ['init', '--non-interactive', '--home', external]);
  const fixture = p.join(temp, 'fixture');
  fs.mkdirSync(fixture);
  fs.copyFileSync(
    p.join(scriptRoot, 'fixtures/acp.mjs'),
    p.join(fixture, 'acp.mjs')
  );
  const quote = (s) => "'" + s.replaceAll("'", "'\\''") + "'";
  const fixtureBin = p.join(fixture, 'dsh');
  fs.writeFileSync(
    fixtureBin,
    `#!/bin/sh\nif [ "\${1:-}" = --version ]; then echo ${manifest.dependencies.dsh.version}; exit 0; fi\nexec ${quote(p.join(bundle, 'runtime/node/bin/node'))} ${quote(p.join(fixture, 'acp.mjs'))} "$@"\n`,
    { mode: 0o755 }
  );
  configure(p.join(external, 'config.toml'), fixtureBin);
  // Explicit home must run with a read-only bundle, including first init.
  fs.rmSync(external, { recursive: true });
  const modes = inventory(bundle)
    .filter((f) => f.kind === 'file')
    .map((f) => [p.join(bundle, f.path), f.mode]);
  const directories = (dir) => {
    const result = [dir];
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
      if (entry.isDirectory())
        result.push(...directories(p.join(dir, entry.name)));
    }
    return result;
  };
  for (const dir of directories(bundle))
    modes.push([dir, fs.statSync(dir).mode & 0o777]);
  for (const [file, mode] of modes) fs.chmodSync(file, mode & ~0o222);
  try {
    exec('start', ['init', '--non-interactive', '--home', external]);
    configure(p.join(external, 'config.toml'), fixtureBin);
    const externalConfig = p.join(external, 'config.toml');
    fs.writeFileSync(
      externalConfig,
      fs
        .readFileSync(externalConfig, 'utf8')
        .replace("data_dir = './data'", "data_dir = '.'")
    );
    service = await launch(['--home', external]);
    const cat = await request(service, '/v1/rooms');
    const created = await request(service, '/v1/rooms/create', {
      service_epoch: cat.service_epoch,
      caller: { kind: 'human', id: 'packaging-smoke' },
      team_id: 'takeout-dinner',
      create_id: 'fixture'
    });
    await request(service, '/v1/rooms/open', {
      service_epoch: cat.service_epoch,
      caller: { kind: 'human', id: 'packaging-smoke' },
      room_id: created.room_id,
      room_instance: created.room_instance,
      open_id: 'fixture'
    });
    await until(async () => {
      const rooms = await request(service, '/v1/rooms');
      return rooms.rooms.some(
        (r) => r.room_id === created.room_id && r.service
      );
    }, 'fixture Room open');
    exec('bin/teamx', [
      'send',
      created.room_id,
      '--endpoint',
      service.info.endpoint,
      '--id',
      'packaging-fixture-turn',
      '--text',
      'Offline packaging fixture turn'
    ]);
    await until(
      () =>
        fs.existsSync(env.TEAMX_PACKAGING_FIXTURE_LOG) &&
        fs
          .readFileSync(env.TEAMX_PACKAGING_FIXTURE_LOG, 'utf8')
          .includes('session/prompt'),
      'ACP fixture prompt'
    );
    await stop(service);
    const events = fs
      .readFileSync(env.TEAMX_PACKAGING_FIXTURE_LOG, 'utf8')
      .trim()
      .split('\n')
      .map(JSON.parse);
    must(
      events.some((e) => e.method === 'session/close'),
      'ACP close not observed'
    );
    for (const pid of new Set(events.map((e) => e.pid)))
      must(
        (() => {
          try {
            process.kill(pid, 0);
            return false;
          } catch {
            return true;
          }
        })(),
        'Fixture process leaked'
      );
    service = await launch(['--home', external]);
    await stop(service);
  } finally {
    for (const [file, mode] of modes) fs.chmodSync(file, mode);
  }
  pass(
    'explicit home, read-only bundle, fixed ACP fixture prompt/close and process cleanup'
  );
  const report = {
    schema_version: 1,
    status: 'passed',
    archive_sha256: archiveHash,
    target: manifest.target,
    profile: manifest.profile,
    environment: { os_release: os.release(), process_arch: process.arch, machine: os.machine(), platform: process.platform },
    checks,
    real_dsh_acp: 'not_run',
    real_platforms: 'not_run',
    browser: 'not_run',
    smoke_sha256: hash(fs.readFileSync(fileURLToPath(import.meta.url))),
    native_check_sha256: hash(
      fs.readFileSync(p.join(scriptRoot, 'check-dsh-native.mjs'))
    ),
    fixture: 'scripts/fixtures/acp.mjs',
    fixture_sha256: hash(
      fs.readFileSync(p.join(scriptRoot, 'fixtures/acp.mjs'))
    )
  };
  if (reportPath) writeJSON(reportPath, report);
} catch (error) {
  console.error(`Smoke failed: ${error.message}`);
  process.exitCode = 1;
} finally {
  for (const child of children) {
    const exited =
      child.exitCode === null && child.signalCode === null
        ? once(child, 'exit')
        : Promise.resolve();
    child.kill('SIGKILL');
    await exited;
  }
  fs.rmSync(temp, { recursive: true, force: true });
}
