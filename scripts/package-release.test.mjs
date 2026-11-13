import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as p from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import { hash, inventory, json, run, writeJSON } from './package-lib.mjs';
import { assertReleaseSource, releaseBatch } from './package-release-source.mjs';
import { checkVerification, inspectCandidate, releasePlan, verificationReport } from './package-release.mjs';
import { machOFiles, signBundle, signingPolicy } from './package-signing.mjs';

const root = p.resolve(p.dirname(fileURLToPath(import.meta.url)), '..');
const commit = 'a'.repeat(40), version = 'v0.0.0';
const target = `${process.platform}-${process.arch === 'x64' ? 'amd64' : process.arch}`;
const smokeFiles = { smoke_sha256: 'scripts/package-smoke.mjs',
  native_check_sha256: 'scripts/check-dsh-native.mjs', fixture_sha256: 'scripts/fixtures/acp.mjs' };
const batch = { run_id: 101, run_attempt: 2 };
const native = { 'darwin-arm64': ['darwin', 'arm64', 'arm64'], 'darwin-amd64': ['darwin', 'x64', 'x86_64'],
  'linux-arm64': ['linux', 'arm64', 'aarch64'], 'linux-amd64': ['linux', 'x64', 'x86_64'] };

function fixture(t, candidateTarget = target) {
  const temp = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-candidate-test-'));
  t.after(() => fs.rmSync(temp, { recursive: true, force: true }));
  const source = p.join(temp, 'source'), directory = p.join(temp, 'artifact');
  fs.mkdirSync(directory);
  for (const file of ['packaging/dependencies.lock.json', 'packaging/dsh/package-lock.json',
    'packaging/dsh/package.json', 'tools/gitlab/glab.lock.json', ...Object.values(smokeFiles), 'scripts/package-release.mjs', 'scripts/package-ci.mjs', 'scripts/package-lib.mjs',
    'scripts/package-release-source.mjs', 'scripts/package-profiles.mjs', 'scripts/package-signing.mjs', 'scripts/package-platform.mjs', 'scripts/fixtures/models.mjs']) {
    fs.mkdirSync(p.dirname(p.join(source, file)), { recursive: true });
    fs.copyFileSync(p.join(root, file), p.join(source, file));
  }
  const name = `teamx-${version}-${candidateTarget}-core`, bundle = p.join(temp, name);
  fs.mkdirSync(bundle);
  const signing = signingPolicy(candidateTarget), security = { target: candidateTarget, profile: 'core', signing,
    notarization: { status: candidateTarget.startsWith('darwin-') ? 'not_run' : 'not_applicable' } };
  const input = { path: 'packaging/dsh/package.json', sha256: hash(fs.readFileSync(p.join(source, 'packaging/dsh/package.json'))) };
  const manifest = { schema_version: 1, version, channel: 'release', target: candidateTarget, profile: 'core',
    distribution: { schema_version: 2, kind: 'release_archive', signing, build: batch },
    source: { commit, dirty: false, build_inputs: [input], packaging_inputs: [input] },
    dependency_lock_sha256: hash(fs.readFileSync(p.join(source, 'packaging/dependencies.lock.json'))), dependencies: { node: {}, dsh: {} } };
  let smoke;
  function archive() {
    writeJSON(p.join(bundle, 'manifest.json'), manifest);
    fs.rmSync(p.join(bundle, 'checksums.txt'), { force: true });
    writeJSON(p.join(bundle, 'checksums.txt'), inventory(bundle));
    run('tar', ['-czf', p.join(directory, name + '.tar.gz'), '-C', temp, name]);
    const digest = hash(fs.readFileSync(p.join(directory, name + '.tar.gz')));
    fs.writeFileSync(p.join(directory, name + '.tar.gz.sha256'), `${digest}  ${name}.tar.gz\n`);
    const [platform, process_arch, machine] = native[candidateTarget];
    smoke = { schema_version: 1, status: 'passed', environment: { platform, process_arch, machine },
      checks: ['synthetic protocol test'], target: candidateTarget, profile: 'core', archive_sha256: digest,
      ...Object.fromEntries(Object.entries(smokeFiles).map(([key, file]) => [key, hash(fs.readFileSync(p.join(source, file)))])) };
    writeJSON(p.join(directory, name + '-report.json'), smoke);
    writeJSON(p.join(directory, name + '-security.json'), { ...security, archive_sha256: digest });
  }
  archive();
  const inspect = () => inspectCandidate(directory, candidateTarget, 'core', commit, version, source);
  return { temp, source, directory, name, bundle, manifest, security, archive, inspect, verification: () => verificationReport(inspect(), smoke, batch, source) };
}

function repository(t) {
  const dir = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-tag-test-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const git = (...args) => run('git', args, { cwd: dir, stdio: ['ignore', 'pipe', 'pipe'],
    env: { ...process.env, GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_NOSYSTEM: '1',
      GIT_AUTHOR_NAME: 'test', GIT_AUTHOR_EMAIL: 'test@example.invalid', GIT_COMMITTER_NAME: 'test', GIT_COMMITTER_EMAIL: 'test@example.invalid' } }).trim();
  git('init', '-q'); fs.writeFileSync(p.join(dir, 'input'), 'source'); git('add', '.'); git('commit', '-qm', 'fixture');
  return { dir, git };
}

test('release source requires an exact clean tag, including annotated tags; branches cannot substitute', t => {
  const { dir, git } = repository(t);
  for (const invalid of ['v01.0.0', 'v1.2.3-rc.1', 'v1.2.3+build.1', '1.2.3', 'v1.2', 'v1.2.3\n'])
    assert.throws(() => assertReleaseSource(dir, invalid), /requires --version/);
  git('branch', version);
  assert.throws(() => assertReleaseSource(dir, version));
  git('tag', '-am', 'fixture', version);
  const expected = git('rev-parse', 'HEAD');
  assert.equal(assertReleaseSource(dir, version, expected), expected);
  assert.throws(() => assertReleaseSource(dir, version, commit), /commit mismatch/);
  fs.writeFileSync(p.join(dir, 'untracked'), 'data');
  assert.throws(() => assertReleaseSource(dir, version), /clean worktree/);
  fs.unlinkSync(p.join(dir, 'untracked'));
  fs.appendFileSync(p.join(dir, 'input'), 'changed'); git('add', 'input');
  assert.throws(() => assertReleaseSource(dir, version), /clean worktree/);
  git('commit', '-qm', 'different HEAD');
  assert.throws(() => assertReleaseSource(dir, version), /Tag does not match/);
});

test('release plan requires four targets and core/standard', t => {
  const f = fixture(t), r = repository(t);
  fs.cpSync(f.source, r.dir, { recursive: true });
  r.git('add', '.'); r.git('commit', '-qm', 'source'); r.git('tag', version);
  const plan = releasePlan(r.dir, version);
  assert.equal(plan.matrix.include.length, 8);
  assert.deepEqual(new Set(plan.matrix.include.map(row => row.profile)), new Set(['core', 'standard']));
  assert.deepEqual(new Set(plan.matrix.include.map(row => row.target)), new Set(['darwin-arm64', 'darwin-amd64', 'linux-amd64', 'linux-arm64']));
  assert.equal(plan.version, version);
  const lockFile = p.join(r.dir, 'packaging/dependencies.lock.json'), lock = json(lockFile);
  lock.release.profiles.push('full'); writeJSON(lockFile, lock);
  r.git('add', '.'); r.git('commit', '-qm', 'full release scope'); r.git('tag', '-f', version);
  assert.throws(() => releasePlan(r.dir, version), /core\/standard only/);
  lock.release.profiles.pop(); lock.targets['linux-arm64'].delivery = 'excluded'; writeJSON(lockFile, lock);
  r.git('add', '.'); r.git('commit', '-qm', 'missing release target'); r.git('tag', '-f', version);
  assert.throws(() => releasePlan(r.dir, version), /all four native targets/);
});

test('release archive identity binds immutable bytes, signing and verifier', t => {
  const f = fixture(t), receipt = f.inspect();
  assert.equal(receipt.kind, 'release_archive');
  assert.equal(receipt.schema_version, 2);
  assert.equal(Object.hasOwn(receipt, 'release_ready'), false);
  const report = f.verification();
  assert.equal(checkVerification(receipt, report, batch, f.source).status, 'passed');
  assert.deepEqual(report.checks, { archive_identity: 'passed', build_smoke: 'passed', download_smoke: 'passed' });
  f.manifest.source.dirty = true; f.archive();
  assert.throws(f.inspect, /source\/tag\/target\/profile\/channel mismatch/);
});

test('dev, old qualification format or retargeted archives cannot substitute release archives', t => {
  const f = fixture(t);
  for (const [key, value] of [['channel', 'dev'], ['version', 'v0.0.1'], ['profile', 'full'], ['target', 'linux-unknown']]) {
    const previous = f.manifest[key]; f.manifest[key] = value; f.archive();
    assert.throws(f.inspect, /mismatch/); f.manifest[key] = previous;
  }
  f.manifest.distribution.kind = 'release_candidate'; f.archive();
  assert.throws(f.inspect, /not a release archive/);
});

test('legacy qualification flags and incomplete signing cannot enter a release archive', t => {
  const f = fixture(t);
  f.manifest.distribution.release_ready = true; f.archive();
  assert.throws(f.inspect, /not a release archive/);
  delete f.manifest.distribution.release_ready;
  f.security.signing = { mode: 'developer-id', status: 'pending', files: [] };
  f.manifest.distribution.signing = f.security.signing; f.archive();
  assert.throws(f.inspect, /signing/);
});

test('release batch is optional locally and strictly bound when CI identifiers are present', () => {
  assert.equal(releaseBatch({}), undefined);
  assert.deepEqual(releaseBatch({ GITHUB_RUN_ID: '101', GITHUB_RUN_ATTEMPT: '2' }), batch);
  for (const env of [{ GITHUB_RUN_ID: '101' }, { GITHUB_RUN_ATTEMPT: '2' },
    { GITHUB_RUN_ID: '0', GITHUB_RUN_ATTEMPT: '1' }, { GITHUB_RUN_ID: '1e3', GITHUB_RUN_ATTEMPT: '2' }])
    assert.throws(() => releaseBatch(env), /Invalid CI build batch/);
});

test('candidate rejects corrupt transport, extra files, altered security, changed sources and changed verifiers', t => {
  const f = fixture(t);
  fs.writeFileSync(p.join(f.directory, 'private.env'), 'secret');
  assert.throws(f.inspect, /unexpected/); fs.unlinkSync(p.join(f.directory, 'private.env'));
  fs.appendFileSync(p.join(f.directory, f.name + '.tar.gz'), 'corrupt');
  assert.throws(f.inspect, /checksum/); f.archive();
  const securityPath = p.join(f.directory, f.name + '-security.json');
  writeJSON(securityPath, { ...json(securityPath), archive_sha256: '0'.repeat(64) });
  assert.throws(f.inspect, /security report mismatch/); f.archive();
  fs.appendFileSync(p.join(f.source, 'packaging/dsh/package.json'), '\n');
  assert.throws(f.inspect, /source input differs/);
  fs.copyFileSync(p.join(root, 'packaging/dsh/package.json'), p.join(f.source, 'packaging/dsh/package.json'));
  fs.appendFileSync(p.join(f.source, 'scripts/fixtures/acp.mjs'), '\n');
  assert.throws(f.inspect, /verifier differs/);
});

test('CI verification rejects different batches, partial smoke, changed sources and invented checks', t => {
  const f = fixture(t), receipt = f.inspect(), report = f.verification();
  for (const change of [{ archive_sha256: '0'.repeat(64) }, { commit: 'b'.repeat(40) },
    { batch: { ...batch, run_attempt: 3 } }, { checks: { archive_identity: 'passed' } },
    { kind: 'candidate_assessment' }, { private: 'must not be published' }])
    assert.throws(() => checkVerification(receipt, { ...report, ...change }, batch, f.source), /mismatched archive verification/);
  assert.throws(() => checkVerification(receipt, report, { ...batch, run_id: 202 }, f.source), /build batch differs/);
  assert.throws(() => checkVerification(receipt, report, { run_id: '101', run_attempt: 2 }, f.source), /Invalid verification batch/);
  assert.throws(() => checkVerification(receipt, { ...report, download_smoke: { ...report.download_smoke, status: 'not_run' } }, batch, f.source), /smoke report mismatch/);
  for (const smoke of [{ ...report.download_smoke, checks: [] },
    { ...report.download_smoke, environment: { ...report.download_smoke.environment, process_arch: 'foreign' } }])
    assert.throws(() => checkVerification(receipt, { ...report, download_smoke: smoke }, batch, f.source), /native smoke/);
  fs.appendFileSync(p.join(f.source, 'scripts/package-release.mjs'), '\n');
  assert.throws(() => checkVerification(receipt, report, batch, f.source), /mismatched archive verification/);
});

test('signing is explicit, scans native modules and helpers, skips scripts and links, never fakes Developer ID', t => {
  const dir = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-signing-test-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  for (const name of ['node', 'addon.node', 'helper']) fs.writeFileSync(p.join(dir, name), Buffer.from('cffaedfe', 'hex'));
  fs.writeFileSync(p.join(dir, 'start'), '#!/bin/sh'); fs.symlinkSync('node', p.join(dir, 'link'));
  assert.deepEqual(machOFiles(dir), ['addon.node', 'helper', 'node']);
  assert.equal(signBundle(dir, root, 'darwin-arm64', 'unsigned').status, 'not_run');
  assert.equal(signingPolicy('linux-arm64', 'developer-id').status, 'not_applicable');
  assert.throws(() => signingPolicy('darwin-arm64', 'adhoc'), /Unknown macOS signing/);
  assert.throws(() => signBundle(dir, root, 'darwin-arm64', 'developer-id', {}), /requires macOS|Developer ID Application/);
});

test('Developer ID archives require matching code inventory, code hashes and notarization', t => {
  // Synthetic reports exercise validation on any host, without signing code.
  const f = fixture(t, 'darwin-arm64'), file = p.join(f.bundle, 'native.node');
  fs.writeFileSync(file, Buffer.from('cffaedfe', 'hex'));
  const signed = { path: 'native.node', sha256: hash(fs.readFileSync(file)) };
  Object.assign(f.security.signing, { mode: 'developer-id', status: 'passed', files: [signed] });
  Object.assign(f.security.notarization, { status: 'passed', submission_id: '12345678-1234-1234-1234-123456789abc' });
  f.archive();
  assert.equal(f.inspect().security.signing.mode, 'developer-id');
  f.security.notarization.status = 'not_run'; f.archive();
  assert.throws(f.inspect, /signing\/notarization/);
  f.security.notarization.status = 'passed'; f.security.signing.files = []; f.archive();
  assert.throws(f.inspect, /signed-code inventory/);
  f.security.signing.files = [signed]; fs.appendFileSync(file, 'changed'); f.archive();
  assert.throws(f.inspect, /Signed code changed/);
});

test('unsigned macOS archives cannot claim successful signing or notarization', t => {
  const f = fixture(t, 'darwin-arm64');
  assert.equal(f.inspect().security.signing.status, 'not_run');
  f.security.signing.status = 'passed'; f.archive();
  assert.throws(f.inspect, /Unsigned candidate claims signing/);
  f.security.signing.status = 'not_run'; f.security.notarization.status = 'passed'; f.archive();
  assert.throws(f.inspect, /Unsigned candidate claims signing/);
});

test('removed qualification commands and bypass flags fail at the CLI without creating reports', t => {
  const temporary = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-obsolete-cli-'));
  t.after(() => fs.rmSync(temporary, { recursive: true, force: true }));
  const output = p.join(temporary, 'must-not-exist.json');
  for (const [script, args, message] of [
    ['package.mjs', ['--candidate'], /Unknown argument/],
    ['check-packaging.mjs', ['--require-ready'], /Unknown argument/],
    ['package-release.mjs', ['assess', output], /Usage:/],
    ['package-release.mjs', ['qualification-template', output], /Usage:/],
    ['package-publish.mjs', ['plan', 'v0.0.0', '101', '2', output], /Usage:/]
  ]) {
    const result = spawnSync(process.execPath, [p.join(root, 'scripts', script), ...args], { encoding: 'utf8' });
    assert.equal(result.status, 1, script);
    assert.match(result.stderr, message);
    assert.equal(fs.existsSync(output), false);
  }
});
