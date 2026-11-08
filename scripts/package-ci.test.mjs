import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as p from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import { hash, json, run, writeJSON } from './package-lib.mjs';
import { assertNative, inspectSnapshot, snapshotPlan, verifySnapshot } from './package-ci.mjs';
import { profileDependencies, releaseProfiles } from './package-profiles.mjs';

const root = p.resolve(p.dirname(fileURLToPath(import.meta.url)), '..');
const commit = 'a'.repeat(40);
const target = `${process.platform}-${process.arch === 'x64' ? 'amd64' : process.arch}`;
const verifierFiles = {
  smoke_sha256: 'scripts/package-smoke.mjs',
  native_check_sha256: 'scripts/check-dsh-native.mjs',
  fixture_sha256: 'scripts/fixtures/acp.mjs'
};

function fixture(t, profile = 'core') {
  const temp = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-ci-test-'));
  t.after(() => fs.rmSync(temp, { recursive: true, force: true }));
  const directory = p.join(temp, 'artifact'), source = p.join(temp, 'source');
  fs.mkdirSync(directory);
  for (const file of ['packaging/dependencies.lock.json', 'packaging/dsh/package-lock.json',
    'packaging/dsh/package.json', 'tools/gitlab/glab.lock.json', ...Object.values(verifierFiles)]) {
    fs.mkdirSync(p.dirname(p.join(source, file)), { recursive: true });
    fs.copyFileSync(p.join(root, file), p.join(source, file));
  }
  const name = `teamx-dev-${commit.slice(0, 12)}-${target}-${profile}`;
  const bundle = p.join(temp, name);
  fs.mkdirSync(bundle);
  const manifest = {
    version: `dev-${commit.slice(0, 12)}`, channel: 'dev', target, profile,
    dependencies: Object.fromEntries(profileDependencies(profile).map(name => [name, {}])),
    source: { commit, dirty: false, build_inputs: [], packaging_inputs: [
      { path: 'packaging/dsh/package.json', sha256: hash(fs.readFileSync(p.join(source, 'packaging/dsh/package.json'))) }
    ] },
    dependency_lock_sha256: hash(fs.readFileSync(p.join(source, 'packaging/dependencies.lock.json')))
  };
  const reportPath = p.join(directory, name + '-report.json');
  function archive() {
    writeJSON(p.join(bundle, 'manifest.json'), manifest);
    run('tar', ['-czf', p.join(directory, name + '.tar.gz'), '-C', temp, name]);
    const digest = hash(fs.readFileSync(p.join(directory, name + '.tar.gz')));
    fs.writeFileSync(p.join(directory, name + '.tar.gz.sha256'), `${digest}  ${name}.tar.gz\n`);
    writeJSON(reportPath, { status: 'passed', target, profile, archive_sha256: digest,
      ...Object.fromEntries(Object.entries(verifierFiles).map(([key, file]) => [key, hash(fs.readFileSync(p.join(source, file)))])) });
  }
  archive();
  return { directory, source, manifest, archive, reportPath,
    inspect: (expectedTarget = target, expectedProfile = profile, expectedCommit = commit) => inspectSnapshot(directory, expectedTarget, expectedProfile, expectedCommit, source) };
}

test('standard transport requires its platform CLIs, rejects bundled Git, and release scope cannot silently shrink', t => {
  const f = fixture(t, 'standard');
  assert.equal(f.inspect().manifest.profile, 'standard');
  delete f.manifest.dependencies.glab; f.archive();
  assert.throws(f.inspect, /dependencies differ/);
  f.manifest.dependencies.glab = {}; f.manifest.dependencies.git = {}; f.archive();
  assert.throws(f.inspect, /dependencies differ/);
  const lock = json(p.join(f.source, 'packaging/dependencies.lock.json'));
  assert.deepEqual(releaseProfiles(lock), ['core', 'standard']);
  assert.ok(snapshotPlan(f.source).matrix.include.every(row => row.profile === 'standard'));
  lock.release.profiles = ['core'];
  assert.throws(() => releaseProfiles(lock), /Incomplete/);
});

test('Linux readiness reports Git TLS and source blockers only for profiles bundling Git', () => {
  for (const profile of ['core', 'standard', 'full']) {
    const result = spawnSync(process.execPath, [p.join(root, 'scripts/check-packaging.mjs'), '--require-ready',
      '--target', 'linux-amd64', '--profile', profile], { encoding: 'utf8' });
    assert.equal(result.status, 2);
    assert.equal(result.stderr.includes('git_tls_runtime'), profile === 'full');
    assert.equal(result.stderr.includes('git_redistribution_materials'), profile === 'full');
  }
});

test('snapshot matrix includes every locked target and does not hide full blockers', (t) => {
  const f = fixture(t);
  const lock = json(p.join(f.source, 'packaging/dependencies.lock.json'));
  for (const profile of ['core', 'standard', 'full']) {
    const plan = snapshotPlan(f.source, profile);
    assert.deepEqual(plan.matrix.include.map(row => row.target).sort(), Object.keys(lock.targets).sort());
    for (const row of plan.matrix.include) {
      assert.equal(row.runner, lock.targets[row.target].runner);
      assert.equal(row.profile, profile);
    }
    assert.equal(plan.node_version, lock.build_tools.node);
    assert.equal(plan.go_version, lock.build_tools.go.version);
  }
  assert.throws(() => snapshotPlan(f.source, 'release'), /Unknown snapshot profile/);
  lock.targets['linux-amd64'].delivery = 'excluded';
  writeJSON(p.join(f.source, 'packaging/dependencies.lock.json'), lock);
  assert.throws(() => snapshotPlan(f.source), /Required snapshot target missing/);
});

test('snapshot cache fingerprint changes with any locked dependency input', (t) => {
  const f = fixture(t);
  const before = snapshotPlan(f.source).dependency_key;
  fs.appendFileSync(p.join(f.source, 'packaging/dsh/package-lock.json'), '\n');
  assert.notEqual(snapshotPlan(f.source).dependency_key, before);
});

test('transport inspection binds source, capability, archive and smoke without claiming release readiness', (t) => {
  const f = fixture(t), receipt = f.inspect();
  assert.equal(receipt.release_ready, false);
  assert.equal(receipt.manifest.source.commit, commit);
  assert.equal(receipt.kind, 'development_snapshot');
  assert.throws(() => f.inspect('linux-invalid'), /report does not match/);
  assert.throws(() => f.inspect(target, 'full'), /report does not match/);
  assert.throws(() => f.inspect(target, 'core', 'b'.repeat(40)), /source\/target\/profile\/channel mismatch/);
});

test('transport inspection rejects corrupt archive, missing report and extra assets', (t) => {
  const f = fixture(t);
  fs.writeFileSync(p.join(f.directory, 'unexpected.env'), 'private');
  assert.throws(() => f.inspect(), /unexpected files/);
  fs.unlinkSync(p.join(f.directory, 'unexpected.env'));
  fs.unlinkSync(f.reportPath);
  assert.throws(() => f.inspect(), /missing or unexpected/);
  f.archive();
  const archive = fs.readdirSync(f.directory).find(name => name.endsWith('.tar.gz'));
  fs.appendFileSync(p.join(f.directory, archive), 'corruption');
  assert.throws(() => f.inspect(), /checksum mismatch/);
});

test('a passing report for other bytes or another verifier is rejected', (t) => {
  const f = fixture(t), report = json(f.reportPath);
  writeJSON(f.reportPath, { ...report, archive_sha256: '0'.repeat(64) });
  assert.throws(() => f.inspect(), /report does not match/);
  writeJSON(f.reportPath, { ...report, fixture_sha256: '0'.repeat(64) });
  assert.throws(() => f.inspect(), /verifier differs/);
});

test('transport inspection rejects changed source/lock and release channel', (t) => {
  const f = fixture(t);
  fs.appendFileSync(p.join(f.source, 'packaging/dsh/package.json'), '\n');
  assert.throws(() => f.inspect(), /source input differs/);
  fs.copyFileSync(p.join(root, 'packaging/dsh/package.json'), p.join(f.source, 'packaging/dsh/package.json'));
  f.manifest.dependency_lock_sha256 = '0'.repeat(64);
  f.archive();
  assert.throws(() => f.inspect(), /dependency lock differs/);
  f.manifest.channel = 'release';
  f.archive();
  assert.throws(() => f.inspect(), /channel mismatch/);
});

test('native runner check accepts this host and rejects a different architecture', () => {
  assertNative(target);
  assert.throws(() => assertNative(`${process.platform}-${process.arch === 'arm64' ? 'amd64' : 'arm64'}`), /Native runner required/);
});

test('download verification rejects an altered receipt and never reuses a success report', (t) => {
  const f = fixture(t), receipt = f.inspect();
  const output = p.join(f.directory, '..', 'download-report.json');
  writeJSON(p.join(f.directory, 'snapshot.json'), { ...receipt, release_ready: true });
  assert.throws(() => verifySnapshot(f.directory, target, 'core', commit, output), /receipt differs/);
  assert.equal(fs.existsSync(output), false);
  writeJSON(output, { status: 'passed' });
  assert.throws(() => verifySnapshot(f.directory, target, 'core', commit, output), /report already exists/);
});
