import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as p from 'node:path';
import { fileURLToPath } from 'node:url';
import { extract, hash, inventory, json, run, writeJSON } from './package-lib.mjs';
import { acceptanceInputs, assessCandidate, checkAssessment, inspectCandidate, releasePlan } from './package-release.mjs';
import { checkQualification, qualificationFor, qualificationTemplate, residentServerTarget, residentServerFields } from './package-qualification.mjs';
import { browserNames, browserFields, modelFields, platformFields, webDeliveryTemplate } from './server-release-policy.mjs';
import { signingPolicy } from './package-signing.mjs';
import { hasDependency, profileDependencies } from './package-profiles.mjs';
import { assembleRelease, assessmentName, candidateName, checkReleaseDirectory, publishRelease, validateCandidateRun } from './package-publish.mjs';

const root = p.resolve(p.dirname(fileURLToPath(import.meta.url)), '..');
const version = 'v0.0.0';
const digest = file => hash(fs.readFileSync(file));
const native = { 'darwin-arm64': ['darwin', 'arm64', 'arm64'], 'darwin-amd64': ['darwin', 'x64', 'x86_64'],
  'linux-arm64': ['linux', 'arm64', 'aarch64'], 'linux-amd64': ['linux', 'x64', 'x86_64'] };

// Tiny synthetic archives exercise the release protocol. They contain no
// runtime and do not establish real platform, model, browser or Git acceptance.
function fixture(t, includeFull = false) {
  const temp = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-publish-test-'));
  t.after(() => fs.rmSync(temp, { recursive: true, force: true }));
  const source = p.join(temp, 'source'), candidates = p.join(temp, 'candidates'), assessments = p.join(temp, 'assessments');
  fs.mkdirSync(candidates); fs.mkdirSync(assessments);
  for (const file of new Set([...acceptanceInputs, 'scripts/package-release.mjs', 'scripts/package-qualification.mjs',
    'scripts/package-ci.mjs', 'scripts/package-release-source.mjs', 'scripts/package-signing.mjs', 'scripts/server-release-policy.mjs',
    'packaging/dependencies.lock.json', 'packaging/dsh/package.json', 'packaging/dsh/package-lock.json',
    'tools/gitlab/glab.lock.json', 'docs/installation.zh.md', 'packaging/install.sh', 'LICENSE'])) {
    fs.mkdirSync(p.dirname(p.join(source, file)), { recursive: true });
    fs.copyFileSync(p.join(root, file), p.join(source, file));
  }
  const lock = json(p.join(source, 'packaging/dependencies.lock.json'));
  if (includeFull) lock.release.profiles.push('full');
  writeJSON(p.join(source, 'packaging/dependencies.lock.json'), lock);
  const git = (...args) => run('git', args, { cwd: source, stdio: ['ignore', 'pipe', 'pipe'], env: {
    ...process.env, GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_NOSYSTEM: '1',
    GIT_AUTHOR_NAME: 'test', GIT_COMMITTER_NAME: 'test', GIT_AUTHOR_EMAIL: 'test@example.invalid', GIT_COMMITTER_EMAIL: 'test@example.invalid'
  } }).trim();
  git('init', '-q'); git('add', '.'); git('commit', '-qm', 'synthetic release source'); git('tag', version);
  const { commit, matrix: { include: matrix } } = releasePlan(source, version);
  const context = { repository: 'example/teamx', version, commit, candidate_run_id: 101, candidate_run_attempt: 2,
    publish_run_id: 202, publish_run_attempt: 1 };
  const rows = [];
  for (const row of matrix) {
    const name = `teamx-${version}-${row.target}-${row.profile}`, bundle = p.join(temp, name);
    fs.mkdirSync(p.join(bundle, 'THIRD_PARTY_NOTICES'), { recursive: true });
    fs.copyFileSync(p.join(source, 'LICENSE'), p.join(bundle, 'LICENSE'));
    fs.copyFileSync(p.join(source, 'docs/installation.zh.md'), p.join(bundle, 'README.zh.md'));
    fs.writeFileSync(p.join(bundle, 'THIRD_PARTY_NOTICES/NOTICE'), 'Synthetic dependency notice');
    if (row.profile === 'full') {
      fs.mkdirSync(p.join(bundle, 'THIRD_PARTY_NOTICES/git-source'));
      fs.writeFileSync(p.join(bundle, 'THIRD_PARTY_NOTICES/git-source/COPYING'), 'Synthetic corresponding source remains in bundle');
    }
    const signing = signingPolicy(row.target), input = { path: 'packaging/dsh/package.json', sha256: digest(p.join(source, 'packaging/dsh/package.json')) };
    const manifest = { version, channel: 'release', target: row.target, profile: row.profile,
      source: { commit, dirty: false, build_inputs: [input], packaging_inputs: [input] },
      distribution: { kind: 'release_candidate', release_ready: false, signing },
      dependency_lock_sha256: digest(p.join(source, 'packaging/dependencies.lock.json')),
      qualification_baseline: lock.targets[row.target].qualification_baseline,
      ...(residentServerTarget(row) ? { server_deployment: { model_delivery_sha256: hash('synthetic model delivery') } } : {}),
      dependencies: Object.fromEntries(profileDependencies(row.profile, lock).map(name => [name, {}])) };
    writeJSON(p.join(bundle, 'manifest.json'), manifest);
    writeJSON(p.join(bundle, 'checksums.txt'), inventory(bundle));
    const directory = p.join(candidates, candidateName(version, row, 2)); fs.mkdirSync(directory);
    const archive = name + '.tar.gz';
    run('tar', ['-czf', p.join(directory, archive), '-C', temp, name]);
    const archiveHash = digest(p.join(directory, archive));
    fs.writeFileSync(p.join(directory, archive + '.sha256'), `${archiveHash}  ${archive}\n`);
    const smoke = { status: 'passed', target: row.target, profile: row.profile, archive_sha256: archiveHash,
      ...Object.fromEntries(Object.entries({ smoke_sha256: 'scripts/package-smoke.mjs',
        native_check_sha256: 'scripts/check-dsh-native.mjs', fixture_sha256: 'scripts/fixtures/acp.mjs' }).map(([k, path]) => [k, digest(p.join(source, path))])) };
    writeJSON(p.join(directory, name + '-report.json'), smoke);
    writeJSON(p.join(directory, name + '-security.json'), { archive_sha256: archiveHash, target: row.target,
      profile: row.profile, signing, notarization: { status: row.target.startsWith('darwin-') ? 'not_run' : 'not_applicable' } });
    const receipt = inspectCandidate(directory, row.target, row.profile, commit, version, source);
    writeJSON(p.join(directory, 'candidate.json'), receipt);
    const privateDir = p.join(temp, 'private-' + row.target + '-' + row.profile); fs.mkdirSync(privateDir);
    const [platform, process_arch, machine] = native[row.target], environment = { platform, process_arch, machine };
    const evidence = { status: 'passed', archive_sha256: archiveHash, target: row.target, profile: row.profile,
      manifest_sha256: hash(JSON.stringify(manifest, null, 2) + '\n'), source: manifest.source, dependencies: manifest.dependencies,
      environment, verifier_inputs: acceptanceInputs.map(path => ({ path, sha256: digest(p.join(source, path)) })), checks: {} };
    for (const [key, file, value] of [ ['archive_smoke', 'smoke.json', smoke],
      ['real_dsh_acp', 'real-dsh/report.json', { status: 'passed', fixture: false, cancelToolStopped: true, dshProcessStopped: true,
        resumedOriginalSession: true, sessionId: 'PRIVATE-MODEL-SESSION', afterRestart: { result: { stop_reason: 'end_turn' } } }],
      ['browser', 'browser/report.json', { status: 'passed', fixture: false }] ]) {
      fs.mkdirSync(p.dirname(p.join(privateDir, file)), { recursive: true });
      writeJSON(p.join(privateDir, file), value);
      evidence.checks[key] = { status: 'passed', evidence: file, sha256: digest(p.join(privateDir, file)) };
    }
    writeJSON(p.join(privateDir, 'report.json'), evidence);
    const checks = {
      clean_machine: ['fresh_extraction', 'independent_home', 'host_go_absent', 'host_node_absent', 'host_platform_clis_absent',
        'system_dependencies_within_baseline', ...(hasDependency(row.profile, 'git') ? ['host_git_absent'] : [])],
      downloaded_system_security: row.target.startsWith('darwin-') ?
        ['quarantine_present', 'quarantine_preserved', 'launched_after_resolution', 'user_approved'] :
        ['baseline_runtime_verified', 'dynamic_libraries_within_baseline'],
      ...(hasDependency(row.profile, 'git') ? {
        git_tls_clone: ['bundled_git', 'host_git_absent', 'helper_executed', 'tls_verified', 'https_clone', 'local_commit'] } : {}),
      ...(hasDependency(row.profile, 'glab') ? {
        platform_login_read_bind: ['isolated_accounts', 'gitlab_login', 'gitlab_read', 'gitlab_bind', 'lark_login', 'lark_read', 'lark_bind']
      } : {})
    };
    if (residentServerTarget(manifest)) {
      checks.resident_server = residentServerFields(manifest);
      checks.web_delivery = [];
    }
    const manual = { archive_sha256: archiveHash, target: row.target, profile: row.profile,
      environment: { ...environment, baseline: manifest.qualification_baseline }, checks: Object.fromEntries(Object.entries(checks).map(([key, fields]) =>
        [key, { status: 'passed', fixture: false, method: 'maintainer_manual', tested_at: '2026-01-02T00:00:00Z',
          evidence_sha256: hash('synthetic evidence for ' + key), observations: { ...Object.fromEntries(fields.map(f => [f, true])),
            ...(key === 'web_delivery' ? passingWebDelivery(manifest) : {}),
            ...(key === 'downloaded_system_security' && platform === 'darwin' ? { first_launch: 'blocked' } : {}),
            ...(key === 'platform_login_read_bind' ? { remote_writes: 0 } : {}),
            ...(key === 'resident_server' ? { model_delivery_sha256: manifest.server_deployment.model_delivery_sha256,
              migration: 'not_applicable' } : {}) } }])) };
    const assessment = assessCandidate(receipt, privateDir, source, manual);
    const assessmentDir = p.join(assessments, assessmentName(row, 1)); fs.mkdirSync(assessmentDir);
    const assessmentFile = p.join(assessmentDir, 'assessment.json'); writeJSON(assessmentFile, assessment);
    rows.push({ row, directory, receipt, privateDir, manual, assessment, assessmentFile });
  }
  const output = p.join(temp, 'release');
  return { temp, source, candidates, assessments, matrix, context, rows, output,
    assemble: () => assembleRelease(candidates, assessments, context, output, source) };
}

// Synthetic attestations test release policy only; never real acceptance.
function passingWebDelivery(manifest) {
  const o = webDeliveryTemplate(manifest);
  Object.assign(o.models, Object.fromEntries(modelFields.map(f => [f, true])), {
    upstream_scope: 'same_upstream_multiple_entries', dsh_identity_sha256: hash('synthetic DSH') });
  for (const name of browserNames) Object.assign(o.browsers[name], { status: 'passed', physical_device: true,
    browser_version: 'test', os_version: 'test', evidence_sha256: hash(name),
    observations: Object.fromEntries(browserFields.map(f => [f, true])) });
  Object.assign(o.platforms, Object.fromEntries(platformFields.map(f => [f, true])), {
    authorization_sha256: hash('synthetic authorization'), evidence_sha256: hash('synthetic platform') });
  Object.assign(o.observation, { started_at: '2026-01-01T00:00:00Z', finished_at: '2026-01-02T00:00:00Z',
    duration_seconds: 86400, samples: 1441, max_gap_seconds: 60, evidence_sha256: hash('synthetic observation'),
    same_archive_and_installation: true, collection_backlog_reviewed: true,
    rate_limit_recovery_reviewed: true, failures_reviewed: true });
  o.download_upgrade_restore_network_verified = o.authentication_context_reset_and_limits_documented = true;
  return o;
}

function remote(f) {
  const state = { release: undefined, assets: [], calls: [], uploads: [] };
  const api = async (repo, endpoint, options = {}) => {
    state.calls.push({ repo, endpoint, ...options });
    if (endpoint.startsWith('git/ref/tags/')) return { object: { type: 'commit', sha: f.context.commit } };
    if (endpoint.startsWith('releases/tags/')) return state.release;
    if (endpoint === 'releases' && options.method === 'POST') return state.release = { id: 42, ...options.body };
    if (endpoint.startsWith('releases/42/assets?')) return state.assets;
    if (endpoint === 'releases/42' && options.method === 'PATCH') return state.release = { ...state.release, ...options.body };
    throw Error('Unexpected fake API call');
  };
  const upload = async (_version, paths) => {
    state.uploads.push(paths.map(path => p.basename(path)));
    for (const path of paths) state.assets.push({ id: state.assets.length + 1, name: p.basename(path),
      state: 'uploaded', size: fs.statSync(path).size, digest: 'sha256:' + digest(path), sha: digest(path) });
  };
  return { state, options: { sourceRoot: f.source, api, upload, downloadHash: async (_repo, asset) => asset.sha } };
}

test('full matrix assembles original bytes, notice archive and sanitized archive-specific qualifications', t => {
  const f = fixture(t);
  f.rows[0].assessment.private_extra = 'PRIVATE-ACCOUNT'; writeJSON(f.rows[0].assessmentFile, f.rows[0].assessment);
  const seal = f.assemble();
  assert.equal(seal.bundles, 8);
  const installer = fs.readFileSync(p.join(f.output, 'install.sh'), 'utf8');
  assert.ok(installer.includes("release_version='v0.0.0'"));
  assert.ok(installer.includes("release_repository='example/teamx'"));
  assert.equal(json(p.join(f.output, 'release.json')).installer.sha256, digest(p.join(f.output, 'install.sh')));
  checkReleaseDirectory(f.output, f.context, seal.checksums_sha256, f.source);
  for (const item of f.rows) {
    assert.equal(digest(p.join(f.output, item.receipt.archive)), item.receipt.archive_sha256);
    assert.equal(item.receipt.manifest.distribution.release_ready, false);
  }
  const report = json(p.join(f.output, f.rows[0].receipt.archive.replace('.tar.gz', '-assessment.json')));
  assert.equal(report.release_ready, true);
  assert.equal(report.checks.code_signing, 'not_required');
  assert.ok(!JSON.stringify(report).includes('PRIVATE-'));
  const notices = run('tar', ['-tf', p.join(f.output, 'LICENSES.tar.gz')]);
  assert.ok(notices.includes('linux-arm64-standard/THIRD_PARTY_NOTICES/NOTICE'));
  assert.ok(!notices.includes('git-source'));
});

test('bundle and Release export one installation guide with navigation valid outside the repository', t => {
  const f = fixture(t);
  f.assemble();
  const guide = fs.readFileSync(p.join(root, 'docs/installation.zh.md'), 'utf8');
  const mapping = json(p.join(root, 'packaging/bundle-files.json')).files;
  const readme = mapping.find(file => file.destination === 'README.zh.md');
  assert.equal(readme.source, 'docs/installation.zh.md');
  assert.equal(fs.readFileSync(p.join(f.output, 'README.zh.md'), 'utf8'), guide);
  const extracted = p.join(f.temp, 'guide-extracted');
  extract(p.join(f.output, f.rows[0].receipt.archive), extracted);
  const bundle = p.join(extracted, f.rows[0].receipt.archive.slice(0, -7));
  assert.equal(fs.readFileSync(p.join(bundle, 'README.zh.md'), 'utf8'), guide);
  const slug = heading => heading.toLowerCase().replace(/[^\p{L}\p{N}_ -]/gu, '').replace(/ /g, '-');
  const headings = new Set([...guide.matchAll(/^#{1,6} (.+)$/gm)].map(match => slug(match[1])));
  for (const [, target] of guide.matchAll(/\[[^\]]+\]\(([^)]+)\)/g)) {
    if (/^https?:\/\//.test(target)) continue;
    assert.ok(target.startsWith('#'), `Guide depends on a repository file: ${target}`);
    assert.ok(headings.has(decodeURIComponent(target.slice(1))), `Missing guide section: ${target}`);
  }
});

test('missing or extra target/profile/attempt and incomplete standard platform acceptance prevent release', t => {
  const f = fixture(t), missing = f.rows.find(r => r.row.target === 'linux-arm64' && r.row.profile === 'standard');
  fs.renameSync(missing.directory, missing.directory + '-other-attempt');
  assert.throws(f.assemble, /Candidate matrix/); assert.equal(fs.existsSync(f.output), false);
  fs.renameSync(missing.directory + '-other-attempt', missing.directory);
  missing.assessment.checks.platform_login_read_bind = 'not_run'; writeJSON(missing.assessmentFile, missing.assessment);
  assert.throws(f.assemble, /missing or unexpected checks/); assert.equal(fs.existsSync(f.output), false);
  writeJSON(missing.assessmentFile, assessCandidate(missing.receipt, missing.privateDir, f.source, missing.manual));
  fs.unlinkSync(missing.assessmentFile);
  assert.throws(f.assemble); assert.equal(fs.existsSync(f.output), false);
});

test('known Linux full TLS blocker cannot be erased by synthetic passing acceptance', t => {
  const f = fixture(t, true);
  assert.throws(f.assemble, /TLS runtime remains blocked/);
  assert.equal(fs.existsSync(f.output), false);
});

test('standard requires platform login/read/bind evidence while omitting Git-only gates', t => {
  const f = fixture(t), r = f.rows.find(r => r.row.target === 'linux-arm64' && r.row.profile === 'standard');
  assert.ok(json(p.join(f.source, 'packaging/dependencies.lock.json')).targets['linux-arm64'].blockers.includes('git_tls_runtime'));
  assert.equal(r.assessment.release_ready, true);
  assert.equal(r.assessment.checks.platform_login_read_bind, 'passed');
  assert.equal(Object.hasOwn(r.assessment.checks, 'git_tls_clone'), false);
  delete r.manual.checks.platform_login_read_bind;
  const partial = assessCandidate(r.receipt, r.privateDir, f.source, r.manual);
  assert.equal(partial.release_ready, false);
  assert.deepEqual(partial.remaining, ['platform_login_read_bind']);
});

test('resident Server qualification binds model delivery and refuses incomplete deployment or Linux sending evidence', t => {
  const f = fixture(t);
  for (const r of f.rows.filter(r => residentServerTarget(r.row))) {
    assert.equal(r.assessment.checks.resident_server, 'passed');
    const missing = structuredClone(r.manual);
    delete missing.checks.resident_server;
    assert.ok(assessCandidate(r.receipt, r.privateDir, f.source, missing).remaining.includes('resident_server'));
    for (const mutate of [
      o => o.consistent_backup_restore = false,
      o => o.no_completed_member_repeated = false,
      o => o.model_delivery_sha256 = '0'.repeat(64),
      o => o.migration = 'not_run',
      ...(r.row.target === 'linux-amd64' ? [o => o.lark_account_pinned_send = false] : [])
    ]) {
      const invalid = structuredClone(r.manual);
      mutate(invalid.checks.resident_server.observations);
      assert.throws(() => checkQualification(r.receipt, invalid), /Resident|migration/);
    }
  }
});

test('Web delivery requires real device/model/action and 24 hour evidence for the same candidate', t => {
  const f = fixture(t), r = f.rows.find(r => residentServerTarget(r.row));
  const missing = structuredClone(r.manual); delete missing.checks.web_delivery;
  assert.deepEqual(assessCandidate(r.receipt, r.privateDir, f.source, missing).remaining, ['web_delivery']);
  assert.equal(qualificationTemplate(r.receipt).checks.web_delivery.status, 'not_run');
  for (const mutate of [
    o => o.model_delivery_sha256 = '0'.repeat(64),
    o => o.models.key_rotation = false,
    o => o.models.request_and_execution_snapshot_verified = false,
    o => o.platforms.authorization_sha256 = null,
    o => o.browsers.ios_safari.physical_device = false,
    o => o.browsers.android_chrome.observations.model_settings = false,
    o => o.observation.duration_seconds = 60,
    o => o.observation.finished_at = '2026-01-01T00:01:00Z',
    o => o.observation.samples = 2,
    o => o.observation.max_gap_seconds = 3600,
    o => o.observation.collection_backlog_reviewed = false
  ]) {
    const invalid = structuredClone(r.manual); mutate(invalid.checks.web_delivery.observations);
    assert.throws(() => checkQualification(r.receipt, invalid), /Web delivery|Physical browser/);
  }
  const limited = structuredClone(r.manual);
  for (const name of ['ios_safari', 'android_chrome']) limited.checks.web_delivery.observations.browsers[name] = { status: 'not_run' };
  limited.checks.web_delivery.observations.private_notes = 'PRIVATE-ACCOUNT';
  const assessment = assessCandidate(r.receipt, r.privateDir, f.source, limited);
  assert.equal(assessment.release_ready, true);
  const result = checkAssessment(r.receipt, assessment, f.source).manual_qualification.checks.web_delivery.observations;
  assert.deepEqual(result.supported_browsers, ['desktop_chromium', 'desktop_safari']);
  assert.deepEqual(result.pending_browsers, ['ios_safari', 'android_chrome']);
  assert.ok(!JSON.stringify(result).includes('PRIVATE-ACCOUNT'));
  limited.checks.web_delivery.tested_at = '2026-01-01T00:00:00Z';
  assert.throws(() => checkQualification(r.receipt, limited), /predates observation/);
});

test('manual checks require native baseline, exact bytes and detailed observations; unsigned needs no certificate', t => {
  const f = fixture(t), r = f.rows[0];
  assert.throws(() => checkQualification(r.receipt, qualificationTemplate(r.receipt)), /maintainer evidence/);
  assert.equal(checkAssessment(r.receipt, r.assessment, f.source).release_ready, true);
  const document = { schema_version: 1, version, commit: f.context.commit, records: [r.manual] };
  assert.equal(qualificationFor(document, r.receipt), r.manual);
  assert.throws(() => qualificationFor({ ...document, records: [r.manual, r.manual] }, r.receipt), /Duplicate/);
  assert.throws(() => qualificationFor({ ...document, commit: '0'.repeat(40) }, r.receipt), /source\/version/);
  for (const mutate of [
    q => q.archive_sha256 = '0'.repeat(64), q => q.environment.machine = 'emulated',
    q => delete q.environment.baseline, q => q.checks.clean_machine.fixture = true,
    q => q.checks.clean_machine.observations.host_node_absent = false,
    q => q.checks.downloaded_system_security.observations.user_approved = false
  ]) {
    const copy = structuredClone(r.manual); mutate(copy);
    assert.throws(() => checkQualification(r.receipt, copy));
  }
  const limited = assessCandidate(r.receipt, r.privateDir, f.source);
  assert.equal(limited.checks.code_signing, 'not_required');
  assert.equal(limited.release_ready, false);
  assert.deepEqual(limited.remaining, ['clean_machine', 'downloaded_system_security']);
});

test('corrupt archives, stale verifier and fabricated ready flags are rejected at aggregation', t => {
  const f = fixture(t), r = f.rows[0], original = fs.readFileSync(p.join(r.directory, r.receipt.archive));
  fs.appendFileSync(p.join(r.directory, r.receipt.archive), 'changed');
  assert.throws(f.assemble, /checksum/);
  fs.writeFileSync(p.join(r.directory, r.receipt.archive), original);
  const assessment = structuredClone(r.assessment); assessment.assessment_verifiers[0].sha256 = '0'.repeat(64);
  assert.throws(() => checkAssessment(r.receipt, assessment, f.source), /policy\/verifier/);
  const partial = assessCandidate(r.receipt, undefined, f.source, r.manual);
  Object.assign(partial, { status: 'passed', release_ready: true, remaining: [] });
  assert.throws(() => checkAssessment(r.receipt, partial, f.source), /real acceptance/);
});

test('sealed release rejects changed assets, changed seal and extra private files', t => {
  const f = fixture(t), seal = f.assemble();
  assert.throws(() => checkReleaseDirectory(f.output, f.context, '0'.repeat(64), f.source), /seal differs/);
  fs.writeFileSync(p.join(f.output, 'private.env'), 'PRIVATE');
  assert.throws(() => checkReleaseDirectory(f.output, f.context, seal.checksums_sha256, f.source), /unexpected/);
  fs.unlinkSync(p.join(f.output, 'private.env'));
  fs.appendFileSync(p.join(f.output, 'README.zh.md'), 'changed');
  assert.throws(() => checkReleaseDirectory(f.output, f.context, seal.checksums_sha256, f.source), /asset changed/);
});

test('publication creates a draft, uploads unchanged accepted bytes, verifies downloads then publishes', async t => {
  const f = fixture(t), seal = f.assemble(), r = remote(f);
  const result = await publishRelease(f.output, f.context, seal.checksums_sha256, r.options);
  assert.equal(result.status, 'published');
  assert.equal(r.state.calls.find(c => c.method === 'POST').body.draft, true);
  assert.equal(r.state.calls.at(-1).method, 'PATCH');
  assert.equal(r.state.release.draft, false);
  assert.equal(r.state.assets.length, fs.readdirSync(f.output).length);
  await assert.rejects(() => publishRelease(f.output, f.context, seal.checksums_sha256, r.options), /published or belongs/);
});

test('interrupted draft resumes only identical assets and never overwrites an existing remote file', async t => {
  const f = fixture(t), seal = f.assemble(), r = remote(f);
  const normalUpload = r.options.upload;
  r.options.upload = async (v, paths, repo) => { await normalUpload(v, paths.slice(0, 1), repo); throw Error('transport lost'); };
  await assert.rejects(() => publishRelease(f.output, f.context, seal.checksums_sha256, r.options), /transport lost/);
  assert.equal(r.state.release.draft, true);
  r.options.upload = normalUpload;
  await publishRelease(f.output, f.context, seal.checksums_sha256, r.options);
  assert.ok(!r.state.uploads[1].includes(r.state.uploads[0][0]));
  assert.equal(r.state.calls.filter(c => c.method === 'POST').length, 1);
});

test('remote corruption, moved tag and read errors leave drafts unpublished', async t => {
  const f = fixture(t), seal = f.assemble();
  for (const failure of ['corrupt', 'tag', 'permission']) {
    const r = remote(f), baseAPI = r.options.api;
    if (failure === 'corrupt') r.options.downloadHash = async () => '0'.repeat(64);
    r.options.api = async (repo, endpoint, opts) => {
      if (failure === 'permission' && endpoint.startsWith('releases/tags/')) throw Error('HTTP 403');
      if (failure === 'tag' && endpoint.startsWith('git/ref/') && r.state.assets.length)
        return { object: { type: 'commit', sha: '0'.repeat(40) } };
      return baseAPI(repo, endpoint, opts);
    };
    await assert.rejects(() => publishRelease(f.output, f.context, seal.checksums_sha256, r.options));
    assert.ok(!r.state.calls.some(c => c.method === 'PATCH'));
    assert.ok(!r.state.release || r.state.release.draft);
  }
});

test('candidate provenance rejects foreign workflows, stale attempts and incomplete artifact matrix', t => {
  const f = fixture(t), info = { id: 101, run_attempt: 2, repository: { full_name: f.context.repository },
    head_repository: { full_name: f.context.repository }, event: 'push', path: '.github/workflows/release.yml',
    head_sha: f.context.commit, head_branch: version, status: 'completed', conclusion: 'success' };
  const artifacts = f.matrix.flatMap(row => [candidateName(version, row, 2), `candidate-verification-${version}-${row.target}-${row.profile}-2`])
    .map((name, index) => ({ id: index + 1, name, expired: false }));
  validateCandidateRun(info, artifacts, f.context, f.matrix);
  for (const change of [{ event: 'pull_request' }, { run_attempt: 3 }, { conclusion: 'failure' },
    { path: '.github/workflows/other.yml' }, { head_sha: '0'.repeat(40) }, { head_repository: { full_name: 'foreign/repo' } }])
    assert.throws(() => validateCandidateRun({ ...info, ...change }, artifacts, f.context, f.matrix), /successful tag workflow/);
  assert.throws(() => validateCandidateRun(info, artifacts.slice(1), f.context, f.matrix), /Missing/);
  assert.throws(() => validateCandidateRun(info, [...artifacts, artifacts[0]], f.context, f.matrix), /ambiguous/);
});

test('standalone qualification CLI distinguishes ready, incomplete and invalid evidence', t => {
  const f = fixture(t), r = f.rows[0], script = p.join(f.source, 'scripts/package-release.mjs');
  const qualifications = p.join(f.temp, 'qualifications.json');
  const document = { schema_version: 1, version, commit: f.context.commit, records: [r.manual] };
  writeJSON(qualifications, document);
  const args = [r.directory, r.row.target, r.row.profile, f.context.commit, version];
  const call = (command, suffix) => run(process.execPath, [script, command, ...args, ...suffix], { stdio: ['ignore', 'pipe', 'pipe'] });
  const ready = p.join(f.temp, 'ready.json');
  call('assess', [ready, r.privateDir, qualifications]);
  assert.equal(json(ready).release_ready, true);
  const incomplete = p.join(f.temp, 'incomplete.json');
  assert.throws(() => call('assess', [incomplete, '-', qualifications]), error => error.status === 2);
  assert.equal(json(incomplete).release_ready, false);
  const template = p.join(f.temp, 'template.json');
  call('qualification-template', [template]);
  assert.equal(json(template).records[0].checks.clean_machine.status, 'not_run');
  const invalid = p.join(f.temp, 'invalid.json');
  assert.throws(() => call('assess', [invalid, r.privateDir, template]), error => error.status === 1);
  assert.equal(fs.existsSync(invalid), false);
});
