// Local Linux artifact gate. All state is disposable; no account credentials,
// deployment connection or third-party API is used by this driver.
import fs from 'node:fs';
import os from 'node:os';
import p from 'node:path';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { fileURLToPath } from 'node:url';
import { extract, hash, json, must, run, writeJSON } from './package-lib.mjs';
import { seedOfflineModels } from './fixtures/models.mjs';
import { checkSmoke } from './package-acceptance-lib.mjs';

const root = p.resolve(p.dirname(fileURLToPath(import.meta.url)), '..');
const args = process.argv.slice(2);
const usage = 'node scripts/platform-program-acceptance.mjs ARCHIVE --previous-archive ARCHIVE --output-dir NEW_DIR [--emulated]';
if (args[0] === '--help') { console.log(usage); process.exit(0); }
const archive = p.resolve(args.shift() || ''), options = {};
while (args.length) {
  const key = args.shift();
  must(!Object.hasOwn(options, key), `Duplicate option: ${key}`);
  if (key === '--emulated') options[key] = true;
  else {
    must(['--previous-archive', '--output-dir'].includes(key) && args.length && !args[0].startsWith('--'), usage);
    options[key] = p.resolve(args.shift());
  }
}
must(options['--previous-archive'] && options['--output-dir'], usage);
must(process.platform === 'linux' && process.arch === 'x64' && process.getuid() !== 0,
  'Run on Linux amd64 as a non-root user (declare instruction emulation with --emulated)');
const output = options['--output-dir'];
fs.mkdirSync(output, { mode: 0o700 });
process.umask(0o077);
const report = {
  schema_version: 1, status: 'incomplete', recorded_at: new Date().toISOString(),
  environment: { platform: process.platform, arch: process.arch, machine: os.machine(),
    uid: process.getuid(), gid: process.getgid(), release: os.release(),
    glibc: process.report.getReport().header.glibcVersionRuntime,
    emulated: !!options['--emulated'], systemd: 'not_run' },
  verifier_inputs: ['scripts/platform-program-acceptance.mjs', 'internal/platform/lark/storage_native_integration_test.go',
    'internal/room/testdata/lark_configuration_cli/main.go'].map(path => ({ path, sha256: hash(fs.readFileSync(p.join(root, path))) })),
  checks: {},
  excluded: ['target deployment', 'real OAuth/refresh/DPoP', 'third-party writes',
    'host systemd', 'real model/browser acceptance', 'incompatible CLI storage migration']
};
const save = () => writeJSON(p.join(output, 'report.json'), report);
async function child(name, command, argv, env = process.env) {
  const fd = fs.openSync(p.join(output, name + '.log'), 'wx', 0o600);
  try {
    const proc = spawn(command, argv, { cwd: root, env, stdio: ['ignore', fd, fd] });
    const [code] = await once(proc, 'exit');
    must(code === 0, `${name} failed (${code}); inspect ${name}.log`);
  } finally { fs.closeSync(fd); }
}
async function check(name, work) {
  console.log(`Platform acceptance: ${name}`);
  try { report.checks[name] = { status: 'passed', ...await work() }; }
  catch (error) { report.checks[name] = { status: 'failed', error: error.message }; throw error; }
  finally { save(); }
}
const delay = () => new Promise(resolve => setTimeout(resolve, 100));
async function until(fn, label, timeout = 90000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) { const value = await fn(); if (value) return value; await delay(); }
  throw Error(`Timed out: ${label}`);
}
const quote = value => "'" + value.replaceAll("'", "'\\''") + "'";
const children = new Set();
try {
  const bundles = [], manifests = [];
  const serverRoot = p.join(output, 'installation');
  fs.mkdirSync(p.join(serverRoot, 'releases'), { recursive: true });
  for (const [label, file] of [['a', options['--previous-archive']], ['b', archive]]) {
    const unpacked = p.join(output, 'release-' + label);
    extract(file, unpacked);
    const entries = fs.readdirSync(unpacked);
    must(entries.length === 1, 'Archive must have a single root');
    const bundle = p.join(serverRoot, 'releases', entries[0]);
    fs.renameSync(p.join(unpacked, entries[0]), bundle);
    const manifest = json(p.join(bundle, 'manifest.json'));
    must(manifest.target === 'linux-amd64' && manifest.profile === 'standard' && !manifest.source.dirty,
      'Use clean Linux amd64 standard source snapshots');
    report['release_' + label] = { archive: file, sha256: hash(fs.readFileSync(file)),
      version: manifest.version, source_commit: manifest.source.commit };
    bundles.push(bundle);
    manifests.push(manifest);
    await check('archive_' + label, async () => {
      const receipt = file.replace(/\.tar\.gz$/, '-report.json');
      const evidence = p.join(output, 'smoke-' + label + '.json');
      const reused = fs.existsSync(receipt) && json(receipt).smoke_level === 'full';
      if (reused) fs.copyFileSync(receipt, evidence);
      else await child('smoke-' + label, process.execPath, [p.join(root, 'scripts/package-smoke.mjs'), file, '--report', evidence]);
      checkSmoke(json(evidence), report['release_' + label].sha256, manifest);
      return { evidence: p.basename(evidence), fixture: true, reused_full_smoke: reused };
    });
  }
  const [a, b] = bundles;
  must(report.release_a.source_commit !== report.release_b.source_commit, 'Use distinct previous and candidate source snapshots');
  // Validator-only fixes need not rebuild already verified archive bytes.
  // Every bundled program and packaging input must still match the candidate.
  const inputs = [...manifests[1].source.build_inputs, ...manifests[1].source.packaging_inputs];
  must(inputs.length > 0, 'Candidate lacks source input evidence');
  for (const input of inputs)
    must(hash(fs.readFileSync(p.join(root, input.path))) === input.sha256, `Candidate input differs: ${input.path}`);
  report.verifier_source_commit = run('git', ['rev-parse', 'HEAD'], { cwd: root }).trim();
  const changes = run('git', ['diff', '--name-only', report.release_b.source_commit, 'HEAD'], { cwd: root }).trim();
  for (const path of changes ? changes.split('\n') : [])
    must(report.verifier_inputs.some(input => input.path === path), `Non-verifier source differs: ${path}`);
  report.candidate_inputs_match = true;
  must(!run('git', ['status', '--porcelain', '--untracked-files=all'], { cwd: root }).trim(),
    'Use clean acceptance source');
  // The real CLI/keychain check uses only synthetic app secrets and blocks its
  // post-save remote probe. Prefer running this entire gate with --network none.
  await check('native_private_storage', async () => {
    await child('native-storage', 'make', ['test-integration', 'TEST_PACKAGES=./internal/platform/lark',
      'TEST_RUN=^TestLinuxNativeLarkCredentialStorage$', 'TEST_FLAGS=-count=1 -v'],
    { ...process.env, TEAMX_LARK_STORAGE_TEST_BIN: p.join(b, 'bin/lark-cli'),
      TEAMX_LARK_STORAGE_PREVIOUS_BIN: p.join(a, 'bin/lark-cli'),
      TEAMX_LARK_STORAGE_TEST_EMULATED: options['--emulated'] ? '1' : '' });
    return { synthetic_credentials: true, fixture: false, evidence: 'native-storage.log' };
  });
  await check('linux_platform_composition', async () => {
    await child('composition', 'make', ['test-integration',
      'TEST_PACKAGES=./internal/room ./internal/platform/tool ./internal/platform ./tools/gitlab ./tools/scion',
      'TEST_RUN=Test(ManagedLark|GitlabProgramPair|ScionProgram|SharedHostTools|PlatformProgram|NativeProgram|DependencySelection|BrokenSibling|IndependentGitLabHelp|CapabilityImportBoundaries)',
      'TEST_FLAGS=-count=1 -v'],
    { ...process.env, TEAMX_TEST_GITLAB_BIN: p.join(b, 'bin/teamx-gitlab'), TEAMX_GLAB_BIN: p.join(b, 'bin/glab') });
    return { fixture: true, evidence: 'composition.log' };
  });
  await check('installed_service_upgrade', async () => {
    const home = p.join(output, 'home'), fixture = p.join(output, 'protocol');
    fs.mkdirSync(fixture);
    const native = p.join(fixture, 'native-cli');
    run('go', ['build', '-o', native, './internal/room/testdata/lark_configuration_cli'], { cwd: root });
    const state = { Profile: 'fixture', AppID: 'cli_fixture', Brand: 'feishu', Version: '1.0.99',
      OpenID: 'ou_reader', Tenant: 'account_tenant' };
    writeJSON(p.join(fixture, 'native-fixture.json'), state);
    // Read the production scope constants, rather than maintaining another
    // permission list in the artifact driver.
    const scopes = [...new Set([...fs.readFileSync(p.join(root, 'internal/platform/lark/permissions.go'), 'utf8')
      .matchAll(/^\s*\w+Scopes\s*= "([^"]+)"/gm)].flatMap(m => m[1].split(' ')))].sort();
    must(scopes.includes('offline_access') && scopes.includes('docx:document:readonly'), 'Scope fixture is incomplete');
    writeJSON(p.join(fixture, 'application-scopes.json'), scopes);
    writeJSON(p.join(fixture, 'missing-scopes.json'), []);
    writeJSON(p.join(fixture, 'messages.json'), []);
    writeJSON(p.join(fixture, 'directory.json'), { items: [], has_more: false });
    const cli = label => p.join(fixture, 'lark-' + label);
    for (const label of ['a', 'b']) fs.writeFileSync(cli(label),
      `#!/bin/sh\nexec ${quote(native)} ${quote(fixture)} "$@"\n`, { mode: 0o700 });
    const dsh = p.join(fixture, 'dsh');
    fs.writeFileSync(dsh, `#!/bin/sh\nif [ "\${1:-}" = --version ]; then echo 0.2.2-rc.1; exit 0; fi\nexec ${quote(p.join(b, 'runtime/node/bin/node'))} ${quote(p.join(root, 'scripts/fixtures/acp.mjs'))} "$@"\n`, { mode: 0o700 });
    const env = label => ({ HOME: home, PATH: '/usr/bin:/bin', LANG: 'C.UTF-8', TMPDIR: output,
      TEAMX_LARK_BIN: cli(label), TEAMX_PACKAGING_FIXTURE_LOG: p.join(output, 'acp.ndjson') });
    const current = p.join(serverRoot, 'current');
    fs.symlinkSync(p.join('releases', p.basename(a)), current);
    run(p.join(a, 'start'), ['init', '--non-interactive', '--server-root', serverRoot, '--home', home], { env: env('a') });
    seedOfflineModels(home, dsh, '0.2.2-rc.1');
    async function launch(bundle, label) {
      const proc = spawn(p.join(bundle, 'bin/teamx'), ['serve', '--server-root', serverRoot, '--home', home, '--listen', '127.0.0.1:0'],
        { env: env(label), stdio: ['ignore', 'pipe', 'pipe'] });
      children.add(proc);
      const exited = once(proc, 'exit');
      let stdout = '', stderr = '';
      proc.stdout.on('data', data => stdout += data);
      const log = fs.createWriteStream(p.join(output, 'service-' + label + '.log'), { flags: 'wx', mode: 0o600 });
      proc.stderr.on('data', data => { stderr += data; log.write(data); });
      proc.once('exit', () => log.end());
      const info = await until(() => {
        must(proc.exitCode === null, `Service ${label} exited: ${stderr}`);
        for (const line of stdout.split('\n')) { try { const v = JSON.parse(line); if (v.endpoint) return v; } catch {} }
      }, 'service readiness');
      const tokenFile = p.join(home, 'auth/admin-token');
      const headers = fs.existsSync(tokenFile) ? { Authorization: `Bearer ${fs.readFileSync(tokenFile, 'utf8').trim()}` } : { Origin: info.endpoint };
      const session = await fetch(info.endpoint + '/v1/access/session', { headers });
      must(session.ok, 'Operator session unavailable');
      return { proc, exited, info, headers, caller: { kind: 'human', id: (await session.json()).operator } };
    }
    async function stop(s) {
      s.proc.kill('SIGTERM');
      const result = await Promise.race([s.exited, new Promise((_, reject) => {
        const timer = setTimeout(() => reject(Error('Service shutdown timeout')), 35000); timer.unref();
      })]);
      children.delete(s.proc);
      must(result[0] === 0, 'Service did not settle cleanly');
    }
    async function request(s, path, body) {
      const deadline = Date.now() + 90000;
      for (;;) {
        const response = await fetch(s.info.endpoint + path, { method: body ? 'POST' : 'GET',
          headers: { ...s.headers, 'Content-Type': 'application/json' },
          ...(body ? { body: JSON.stringify(body) } : {}), signal: AbortSignal.timeout(60000) });
        const data = await response.json();
        // Retry only explicit, unaccepted catalog operations with the same ID.
        // Transport failures and ambiguous submissions are never replayed.
        if (response.status === 409 && data.code === 'busy' && Date.now() < deadline &&
            (!body || path === '/v1/rooms/create' && body.create_id || path === '/v1/rooms/open' && body.open_id)) {
          await delay(); continue;
        }
        must(response.ok, `${path}: HTTP ${response.status}, ${JSON.stringify(data)}`);
        return data;
      }
    }
    let service = await launch(a, 'a');
    const catalog = await request(service, '/v1/rooms');
    const basic = s => ({ service_epoch: s.info.service_epoch, caller: s.caller });
    const created = await request(service, '/v1/rooms/create', { ...basic(service), team_id: 'takeout-dinner', create_id: 'platform-upgrade' });
    const identity = { room_id: created.room_id, room_instance: created.room_instance };
    await request(service, '/v1/rooms/open', { ...basic(service), ...identity, open_id: 'platform-upgrade' });
    const member = await until(async () => {
      const targets = await request(service, '/v1/platforms/onboarding/targets?' + new URLSearchParams({ ...identity, service_epoch: catalog.service_epoch, caller_id: service.caller.id }));
      return targets.members?.find(m => m.room_id === identity.room_id);
    }, 'member target');
    const profile = { version: 1, executable: cli('a'), profile: 'fixture', brand: 'feishu', cli_version: '1.0.99' };
    let connection = await request(service, '/v1/platforms/save', { ...basic(service), id: 'c'.repeat(32), version: 0,
      name: 'Synthetic Feishu', provider: 'lark', url: 'https://open.feishu.cn', cli_profile: profile });
    connection = await request(service, '/v1/platforms/verify', { ...basic(service), id: connection.id, version: connection.version });
    const operationID = 'd'.repeat(32);
    await request(service, '/v1/platforms/onboarding/prepare', { ...basic(service), ...identity,
      agent_id: member.agent_id, membership_id: member.membership_id, platform_id: connection.id,
      provider: 'lark', origin: connection.url, operation_id: operationID });
    fs.unlinkSync(cli('a'));
    const failed = await request(service, '/v1/platforms/onboarding/apply', { ...basic(service), operation_id: operationID });
    must(failed.notifications.status === 'applied' && failed.tool.status === 'failed' && ['cli_unavailable', 'apply_interrupted'].includes(failed.tool.error_code),
      'Expected saved notifications and unavailable tool');
    const query = s => '/v1/platform/notifications?' + new URLSearchParams({ ...identity, service_epoch: s.info.service_epoch, caller_id: s.caller.id });
    const subscriptions = s => request(s, query(s));
    const before = (await subscriptions(service)).subscriptions.find(s => s.platform_id === connection.id);
    must(before?.subscription_id, 'Saved subscription missing');
    await stop(service);
    // Preserve the exact stored credential evidence, rather than comparing a
    // verification response whose timestamps can legitimately change.
    function locate(dir, name) {
      for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
        const file = p.join(dir, entry.name);
        if (entry.isFile() && entry.name === name) return file;
        if (entry.isDirectory()) { const found = locate(file, name); if (found) return found; }
      }
    }
    const connectionFile = locate(home, 'platform-connections.json');
    must(connectionFile, 'Connection snapshot missing');
    const evidence = fs.readFileSync(connectionFile);
    fs.unlinkSync(current);
    fs.symlinkSync(p.join('releases', p.basename(b)), current);
    service = await launch(b, 'b');
    const saved = await request(service, '/v1/platforms/onboarding/status', { ...basic(service), operation_id: operationID });
    must(saved.tool.status === 'failed' && saved.operation_id === operationID, 'Original operation was not recovered');
    // HTTP readiness precedes asynchronous Room recovery. This read uses the
    // same member/recovery gate as apply, without resubmitting any operation.
    await request(service, '/v1/platforms/onboarding/targets?' + new URLSearchParams({
      ...identity, service_epoch: service.info.service_epoch, caller_id: service.caller.id }));
    const resumed = await request(service, '/v1/platforms/onboarding/apply', { ...basic(service), operation_id: operationID });
    must(resumed.operation_id === operationID && resumed.notifications.status === 'applied' && resumed.tool.status === 'applied',
      `Original tool step did not resume: ${resumed.tool.status}/${resumed.tool.error_code || ''}`);
    const after = await until(async () => {
      const current = (await subscriptions(service)).subscriptions.find(s => s.subscription_id === before.subscription_id);
      return current && current.last_full && !current.error_code && Date.parse(current.last_full) > (Date.parse(before.last_full) || 0) && current;
    }, 'original notification query after upgrade', 120000);
    for (const key of ['subscription_id', 'subscription_version', 'baseline', 'config', 'account'])
      must(JSON.stringify(before[key]) === JSON.stringify(after[key]), `Upgrade changed ${key}`);
    must(evidence.equals(fs.readFileSync(connectionFile)), 'Program upgrade rewrote connection evidence');
    const calls = fs.readFileSync(p.join(fixture, 'native-calls.ndjson'), 'utf8').trim().split('\n').map(JSON.parse);
    must(calls.some(c => c.args.includes('/open-apis/im/v1/messages/search')), 'No successful read-only collection call');
    await stop(service);
    must(evidence.equals(fs.readFileSync(connectionFile)), 'Shutdown rewrote connection evidence');
    return { fixture: true, evidence: ['service-a.log', 'service-b.log'], operation_id: operationID,
      subscription_id: before.subscription_id, connection_sha256: hash(evidence),
      checks: ['real archived service A to B', 'removed historical CLI', 'original operation resumes tool step',
        'original notification successfully queries', 'connection evidence and subscription identity preserved', 'clean process settlement'] };
  });
  report.status = 'passed';
} catch (error) {
  report.status = 'failed'; report.error = error.message;
  console.error(error.message); process.exitCode = 1;
} finally {
  for (const proc of children) {
    if (proc.exitCode === null && proc.signalCode === null) {
      proc.kill('SIGTERM');
      await Promise.race([once(proc, 'exit'), new Promise(resolve => { const timer = setTimeout(resolve, 35000); timer.unref(); })]);
      if (proc.exitCode === null && proc.signalCode === null) {
        report.status = 'failed'; report.cleanup_error = `Service PID ${proc.pid} did not settle`;
        process.exitCode = 1;
      }
    }
  }
  save();
}
