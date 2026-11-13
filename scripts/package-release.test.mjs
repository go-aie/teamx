import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as p from 'node:path';
import { fileURLToPath } from 'node:url';
import { hash, inventory, json, run, writeJSON } from './package-lib.mjs';
import { assertReleaseSource } from './package-release-source.mjs';
import { assessCandidate, candidatePlan, inspectCandidate, releasePlan } from './package-release.mjs';
import { machOFiles, signBundle, signingPolicy } from './package-signing.mjs';

const root = p.resolve(p.dirname(fileURLToPath(import.meta.url)), '..');
const commit = 'a'.repeat(40), version = 'v0.0.0';
const target = `${process.platform}-${process.arch === 'x64' ? 'amd64' : process.arch}`;
const smokeFiles = { smoke_sha256: 'scripts/package-smoke.mjs',
  native_check_sha256: 'scripts/check-dsh-native.mjs', fixture_sha256: 'scripts/fixtures/acp.mjs' };
const acceptanceFiles = ['scripts/package-acceptance.mjs', 'scripts/package-acceptance-lib.mjs',
  'scripts/package-browser.mjs', 'scripts/package-lib.mjs', 'scripts/package-platform.mjs', ...Object.values(smokeFiles), 'scripts/fixtures/models.mjs',
  'internal/room/bundle_real_e2e_test.go', 'internal/room/dsh_real_test.go', 'internal/room/demo_test.go',
  'packaging/verifier/package.json', 'packaging/verifier/package-lock.json', 'scripts/package-profiles.mjs'];

function fixture(t) {
  const temp = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-candidate-test-'));
  t.after(() => fs.rmSync(temp, { recursive: true, force: true }));
  const source = p.join(temp, 'source'), directory = p.join(temp, 'artifact');
  fs.mkdirSync(directory);
  for (const file of ['packaging/dependencies.lock.json', 'packaging/dsh/package-lock.json',
    'packaging/dsh/package.json', 'tools/gitlab/glab.lock.json', ...acceptanceFiles,
    'scripts/package-release.mjs', 'scripts/package-qualification.mjs', 'scripts/server-release-policy.mjs']) {
    fs.mkdirSync(p.dirname(p.join(source, file)), { recursive: true });
    fs.copyFileSync(p.join(root, file), p.join(source, file));
  }
  const name = `teamx-${version}-${target}-core`, bundle = p.join(temp, name);
  fs.mkdirSync(bundle);
  const signing = signingPolicy(target), security = { target, profile: 'core', signing,
    notarization: { status: target.startsWith('darwin-') ? 'not_run' : 'not_applicable' } };
  const input = { path: 'packaging/dsh/package.json', sha256: hash(fs.readFileSync(p.join(source, 'packaging/dsh/package.json'))) };
  const manifest = { schema_version: 1, version, channel: 'release', target, profile: 'core',
    distribution: { kind: 'release_candidate', release_ready: false, signing },
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
    smoke = { status: 'passed', target, profile: 'core', archive_sha256: digest,
      ...Object.fromEntries(Object.entries(smokeFiles).map(([key, file]) => [key, hash(fs.readFileSync(p.join(source, file)))])) };
    writeJSON(p.join(directory, name + '-report.json'), smoke);
    writeJSON(p.join(directory, name + '-security.json'), { ...security, archive_sha256: digest });
  }
  archive();
  function evidence() {
    const receipt = inspect(), dir = p.join(temp, 'private');
    fs.mkdirSync(dir, { recursive: true });
    const report = { status: 'passed', archive_sha256: receipt.archive_sha256, target, profile: 'core',
      manifest_sha256: hash(JSON.stringify(manifest, null, 2) + '\n'), source: manifest.source, dependencies: manifest.dependencies,
      environment: { platform: process.platform, process_arch: process.arch, machine: os.machine() },
      verifier_inputs: acceptanceFiles.map(path => ({ path, sha256: hash(fs.readFileSync(p.join(source, path))) })), checks: {} };
    for (const [key, file, data] of [
      ['archive_smoke', 'smoke.json', smoke],
      ['real_dsh_acp', 'real-dsh/report.json', { status: 'passed', fixture: false, cancelToolStopped: true,
        dshProcessStopped: true, resumedOriginalSession: true, sessionId: 'private-session-do-not-publish',
        afterRestart: { result: { stop_reason: 'end_turn' } } }],
      ['browser', 'browser/report.json', { status: 'passed', fixture: false, browser: 'fixture-test-only' }]
    ]) {
      fs.mkdirSync(p.dirname(p.join(dir, file)), { recursive: true });
      writeJSON(p.join(dir, file), data);
      report.checks[key] = { status: 'passed', evidence: file, sha256: hash(fs.readFileSync(p.join(dir, file))) };
    }
    writeJSON(p.join(dir, 'report.json'), report);
    return { dir, report, save: () => writeJSON(p.join(dir, 'report.json'), report) };
  }
  const inspect = () => inspectCandidate(directory, target, 'core', commit, version, source);
  return { temp, source, directory, name, manifest, security, archive, inspect, evidence };
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
  assert.throws(() => assertReleaseSource(dir, 'v01.0.0'), /requires --version/);
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

test('release plan requires four targets and core/standard while full candidates stay explicit', t => {
  const f = fixture(t), r = repository(t);
  fs.cpSync(f.source, r.dir, { recursive: true });
  r.git('add', '.'); r.git('commit', '-qm', 'source'); r.git('tag', version);
  const plan = releasePlan(r.dir, version);
  assert.equal(plan.matrix.include.length, 8);
  assert.deepEqual(new Set(plan.matrix.include.map(row => row.profile)), new Set(['core', 'standard']));
  assert.equal(candidatePlan(r.dir, version, 'full').matrix.include.length, 4);
  assert.ok(candidatePlan(r.dir, version, 'full').matrix.include.every(row => row.profile === 'full'));
  assert.deepEqual(new Set(plan.matrix.include.map(row => row.target)), new Set(['darwin-arm64', 'darwin-amd64', 'linux-amd64', 'linux-arm64']));
  assert.equal(plan.version, version);
});

test('candidate identity binds immutable release bytes, security and verifier without promoting readiness', t => {
  const f = fixture(t), receipt = f.inspect();
  assert.equal(receipt.release_ready, false);
  assert.equal(receipt.kind, 'release_candidate');
  const assessment = assessCandidate(receipt);
  assert.equal(assessment.status, 'incomplete');
  assert.ok(assessment.remaining.includes('real_dsh_acp'));
  f.manifest.source.dirty = true; f.archive();
  assert.throws(f.inspect, /source\/tag\/target\/profile\/channel mismatch/);
});

test('dev, qualified or retargeted archives cannot be substituted for candidates', t => {
  const f = fixture(t);
  for (const [key, value] of [['channel', 'dev'], ['version', 'v0.0.1'], ['profile', 'full'], ['target', 'linux-unknown']]) {
    const previous = f.manifest[key]; f.manifest[key] = value; f.archive();
    assert.throws(f.inspect, /mismatch/); f.manifest[key] = previous;
  }
  f.manifest.distribution.release_ready = true; f.archive();
  assert.throws(f.inspect, /unqualified release candidate/);
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

test('old P3 archive and changed verifier evidence are rejected; current limited evidence cannot qualify release', t => {
  const f = fixture(t), receipt = f.inspect(), e = f.evidence();
  const assessment = assessCandidate(receipt, e.dir, f.source);
  assert.equal(assessment.checks.real_dsh_acp, 'passed');
  assert.equal(assessment.release_ready, false);
  assert.ok(assessment.remaining.includes('clean_machine'));
  assert.ok(!JSON.stringify(assessment).includes('private-session-do-not-publish'));
  e.report.archive_sha256 = '0'.repeat(64); e.save();
  assert.throws(() => assessCandidate(receipt, e.dir, f.source), /another candidate/);
  e.report.archive_sha256 = receipt.archive_sha256;
  e.report.verifier_inputs.pop(); e.save();
  assert.throws(() => assessCandidate(receipt, e.dir, f.source), /verifier fingerprints/);
});

test('partial, fixture and edited child acceptance cannot count as real candidate evidence', t => {
  const f = fixture(t), receipt = f.inspect(), e = f.evidence();
  e.report.checks.real_dsh_acp.status = 'not_run'; e.save();
  assert.throws(() => assessCandidate(receipt, e.dir, f.source), /Missing\/mismatched/);
  e.report.checks.real_dsh_acp.status = 'passed'; e.save();
  const file = p.join(e.dir, 'real-dsh/report.json'), real = json(file);
  real.fixture = true; writeJSON(file, real);
  assert.throws(() => assessCandidate(receipt, e.dir, f.source), /Missing\/mismatched/);
  e.report.checks.real_dsh_acp.sha256 = hash(fs.readFileSync(file)); e.save();
  assert.throws(() => assessCandidate(receipt, e.dir, f.source), /Real DSH acceptance/);
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
