import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as p from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import { extract, hash, inventory, json, run, writeJSON } from './package-lib.mjs';
import { inspectCandidate, releasePlan, verificationReport } from './package-release.mjs';
import { signingPolicy } from './package-signing.mjs';
import { profileDependencies } from './package-profiles.mjs';
import { assembleRelease, verificationName, candidateName, checkReleaseDirectory, publishRelease, releaseContext, githubRequest } from './package-publish.mjs';

const root = p.resolve(p.dirname(fileURLToPath(import.meta.url)), '..');
const version = 'v0.0.0';
const digest = file => hash(fs.readFileSync(file));
// Tiny synthetic archives exercise the release protocol. They contain no
// runtime and do not establish real platform, model, browser or Git acceptance.
function fixture(t) {
  const temp = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-publish-test-'));
  t.after(() => fs.rmSync(temp, { recursive: true, force: true }));
  const source = p.join(temp, 'source'), candidates = p.join(temp, 'candidates'), verifications = p.join(temp, 'verifications');
  fs.mkdirSync(candidates); fs.mkdirSync(verifications);
  for (const file of new Set(['scripts/package-release.mjs', 'scripts/package-ci.mjs', 'scripts/package-lib.mjs',
    'scripts/package-publish.mjs', 'scripts/package-release-source.mjs', 'scripts/package-profiles.mjs', 'scripts/package-signing.mjs', 'scripts/package-platform.mjs', 'scripts/fixtures/models.mjs',
    'scripts/package-smoke.mjs', 'scripts/check-dsh-native.mjs', 'scripts/fixtures/acp.mjs',
    'packaging/dependencies.lock.json', 'packaging/dsh/package.json', 'packaging/dsh/package-lock.json',
    'tools/gitlab/glab.lock.json', 'docs/installation.zh.md', 'packaging/install.sh', 'LICENSE'])) {
    fs.mkdirSync(p.dirname(p.join(source, file)), { recursive: true });
    fs.copyFileSync(p.join(root, file), p.join(source, file));
  }
  const lock = json(p.join(source, 'packaging/dependencies.lock.json'));
  writeJSON(p.join(source, 'packaging/dependencies.lock.json'), lock);
  const git = (...args) => run('git', args, { cwd: source, stdio: ['ignore', 'pipe', 'pipe'], env: {
    ...process.env, GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_NOSYSTEM: '1',
    GIT_AUTHOR_NAME: 'test', GIT_COMMITTER_NAME: 'test', GIT_AUTHOR_EMAIL: 'test@example.invalid', GIT_COMMITTER_EMAIL: 'test@example.invalid'
  } }).trim();
  git('init', '-q'); git('add', '.'); git('commit', '-qm', 'synthetic release source'); git('tag', version);
  const { commit, matrix: { include: matrix } } = releasePlan(source, version);
  const context = { repository: 'example/teamx', version, commit, workflow: '.github/workflows/release.yml', run_id: 101, run_attempt: 2 };
  const rows = [];
  for (const row of matrix) {
    const name = `teamx-${version}-${row.target}-${row.profile}`, bundle = p.join(temp, name);
    fs.mkdirSync(p.join(bundle, 'THIRD_PARTY_NOTICES'), { recursive: true });
    fs.copyFileSync(p.join(source, 'LICENSE'), p.join(bundle, 'LICENSE'));
    fs.copyFileSync(p.join(source, 'docs/installation.zh.md'), p.join(bundle, 'README.zh.md'));
    fs.writeFileSync(p.join(bundle, 'THIRD_PARTY_NOTICES/NOTICE'), 'Synthetic dependency notice');
    const signing = signingPolicy(row.target), input = { path: 'packaging/dsh/package.json', sha256: digest(p.join(source, 'packaging/dsh/package.json')) };
    const manifest = { version, channel: 'release', target: row.target, profile: row.profile,
      source: { commit, dirty: false, build_inputs: [input], packaging_inputs: [input] },
      distribution: { schema_version: 2, kind: 'release_archive', signing, build: { run_id: 101, run_attempt: 2 } },
      dependency_lock_sha256: digest(p.join(source, 'packaging/dependencies.lock.json')),
      qualification_baseline: lock.targets[row.target].qualification_baseline,
      dependencies: Object.fromEntries(profileDependencies(row.profile, lock).map(name => [name, {}])) };
    writeJSON(p.join(bundle, 'manifest.json'), manifest);
    writeJSON(p.join(bundle, 'checksums.txt'), inventory(bundle));
    const directory = p.join(candidates, candidateName(version, row, 2)); fs.mkdirSync(directory);
    const archive = name + '.tar.gz';
    run('tar', ['-czf', p.join(directory, archive), '-C', temp, name]);
    const archiveHash = digest(p.join(directory, archive));
    fs.writeFileSync(p.join(directory, archive + '.sha256'), `${archiveHash}  ${archive}\n`);
    const native = { 'darwin-arm64': ['darwin', 'arm64', 'arm64'], 'darwin-amd64': ['darwin', 'x64', 'x86_64'],
      'linux-arm64': ['linux', 'arm64', 'aarch64'], 'linux-amd64': ['linux', 'x64', 'x86_64'] }[row.target];
    const smoke = { schema_version: 1, status: 'passed',
      environment: { platform: native[0], process_arch: native[1], machine: native[2] }, checks: ['synthetic protocol test'], target: row.target, profile: row.profile, archive_sha256: archiveHash,
      ...Object.fromEntries(Object.entries({ smoke_sha256: 'scripts/package-smoke.mjs',
        native_check_sha256: 'scripts/check-dsh-native.mjs', fixture_sha256: 'scripts/fixtures/acp.mjs' }).map(([k, path]) => [k, digest(p.join(source, path))])) };
    writeJSON(p.join(directory, name + '-report.json'), smoke);
    writeJSON(p.join(directory, name + '-security.json'), { archive_sha256: archiveHash, target: row.target,
      profile: row.profile, signing, notarization: { status: row.target.startsWith('darwin-') ? 'not_run' : 'not_applicable' } });
    const receipt = inspectCandidate(directory, row.target, row.profile, commit, version, source);
    writeJSON(p.join(directory, 'candidate.json'), receipt);
    const verification = verificationReport(receipt, smoke, { run_id: 101, run_attempt: 2 }, source);
    const verificationDir = p.join(verifications, verificationName(version, row, 2)); fs.mkdirSync(verificationDir);
    const verificationFile = p.join(verificationDir, 'verification.json'); writeJSON(verificationFile, verification);
    rows.push({ row, directory, receipt, verification, verificationDir, verificationFile });
  }
  const output = p.join(temp, 'release');
  return { temp, source, candidates, verifications, matrix, context, rows, output, git,
    assemble: () => assembleRelease(candidates, verifications, context, output, source) };
}

function remote(f) {
  const state = { release: undefined, otherReleases: [], assets: [], calls: [], uploads: [], downloads: [], bytes: new Map() };
  const api = async (repo, endpoint, options = {}) => {
    assert.equal(repo, f.context.repository);
    state.calls.push({ repo, endpoint, ...options });
    if (endpoint.startsWith('git/ref/tags/')) return { object: { type: 'commit', sha: f.context.commit } };
    // The tag endpoint exposes published releases; drafts require a listing.
    if (endpoint.startsWith('releases/tags/')) return state.release?.draft === false ? state.release : undefined;
    if (endpoint.startsWith('releases?')) {
      const query = new URLSearchParams(endpoint.split('?')[1]);
      const page = Number(query.get('page')), size = Number(query.get('per_page'));
      assert.equal(size, 100); assert.ok(page >= 1);
      const releases = [...state.otherReleases, ...(state.release ? [state.release] : [])];
      return releases.slice((page - 1) * size, page * size);
    }
    if (endpoint === 'releases' && options.method === 'POST') return state.release = { id: 42, ...options.body };
    if (endpoint.startsWith('releases/42/assets?')) return state.assets;
    if (endpoint === 'releases/42' && options.method === 'PATCH') return state.release = { ...state.release, ...options.body };
    throw Error('Unexpected fake API call');
  };
  const upload = async (tag, paths, repository) => {
    assert.equal(tag, f.context.version);
    assert.equal(repository, f.context.repository);
    state.uploads.push(paths.map(path => p.basename(path)));
    for (const path of paths) {
      const id = state.assets.length + 1, bytes = fs.readFileSync(path);
      state.bytes.set(id, bytes);
      state.assets.push({ id, name: p.basename(path), state: 'uploaded', size: bytes.length, digest: 'sha256:' + hash(bytes) });
    }
  };
  const downloadHash = async (repository, asset) => {
    assert.equal(repository, f.context.repository);
    state.downloads.push(asset.name);
    return hash(state.bytes.get(asset.id));
  };
  return { state, options: { sourceRoot: f.source, api, upload, downloadHash } };
}

test('full matrix assembles original bytes, installer, notices and CI reports without private acceptance', t => {
  const f = fixture(t), seal = f.assemble();
  assert.equal(seal.bundles, 8);
  assert.deepEqual(fs.readdirSync(f.output).sort(), ['install.sh', 'README.zh.md', 'LICENSES.tar.gz',
    'release-notes.md', 'release.json', 'SHA256SUMS', ...f.rows.flatMap(({ receipt }) =>
      [receipt.archive, receipt.archive + '.sha256', receipt.archive.replace('.tar.gz', '-verification.json')])].sort());
  const installer = fs.readFileSync(p.join(f.output, 'install.sh'), 'utf8');
  assert.ok(installer.includes("release_version='v0.0.0'"));
  assert.ok(installer.includes("release_repository='example/teamx'"));
  const index = json(p.join(f.output, 'release.json'));
  assert.equal(index.kind, 'github_release');
  assert.equal(index.schema_version, 2);
  assert.equal(index.installer.sha256, digest(p.join(f.output, 'install.sh')));
  checkReleaseDirectory(f.output, f.context, seal.checksums_sha256, f.source);
  for (const item of f.rows) {
    assert.equal(digest(p.join(f.output, item.receipt.archive)), item.receipt.archive_sha256);
    const report = json(p.join(f.output, item.receipt.archive.replace('.tar.gz', '-verification.json')));
    assert.equal(report.kind, 'archive_verification');
    assert.equal(report.status, 'passed');
    assert.deepEqual(report.batch, { run_id: f.context.run_id, run_attempt: f.context.run_attempt });
    assert.deepEqual(Object.keys(report.checks), ['archive_identity', 'build_smoke', 'download_smoke']);
    assert.ok(!/release_ready|qualified_release|manual_qualification/.test(JSON.stringify(report)));
  }
  assert.ok(!/release_ready|qualified_release/.test(JSON.stringify(index)));
  const notes = fs.readFileSync(p.join(f.output, 'release-notes.md'), 'utf8');
  assert.ok(notes.includes('*-verification.json'));
  assert.ok(!notes.includes('真实 DSH/ACP、浏览器及各自用户环境验收'));
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

test('missing package, report, wrong attempt or failed smoke prevents aggregation', t => {
  const f = fixture(t), row = f.rows.find(r => r.row.target === 'linux-arm64' && r.row.profile === 'standard');
  fs.renameSync(row.directory, row.directory + '-other-attempt');
  assert.throws(f.assemble, /Candidate matrix/); assert.equal(fs.existsSync(f.output), false);
  fs.renameSync(row.directory + '-other-attempt', row.directory);
  for (const change of [{ status: 'incomplete' }, { batch: { run_id: 101, run_attempt: 1 } },
    { archive_sha256: '0'.repeat(64) }, { checks: { archive_identity: 'passed' } },
    { version: 'v0.0.1' }, { commit: 'b'.repeat(40) }, { target: 'darwin-arm64' }, { profile: 'core' },
    { build_report_sha256: '0'.repeat(64) }, { security_report_sha256: '0'.repeat(64) }, { verifier_inputs: [] }]) {
    writeJSON(row.verificationFile, { ...row.verification, ...change });
    assert.throws(f.assemble, /mismatched archive verification/);
    assert.equal(fs.existsSync(f.output), false);
  }
  writeJSON(row.verificationFile, row.verification);
  fs.renameSync(row.verificationDir, row.verificationDir + '-other-attempt');
  assert.throws(f.assemble, /Verification matrix/);
  fs.renameSync(row.verificationDir + '-other-attempt', row.verificationDir);
  fs.unlinkSync(row.verificationFile);
  assert.throws(f.assemble, /CI verification files/);
  assert.equal(fs.existsSync(f.output), false);
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
  assert.deepEqual(r.state.downloads.slice().sort(), fs.readdirSync(f.output).sort());
  for (const asset of r.state.assets) assert.deepEqual(r.state.bytes.get(asset.id), fs.readFileSync(p.join(f.output, asset.name)));
  const writes = r.state.calls.filter(call => call.method).length, uploads = r.state.uploads.length;
  await assert.rejects(() => publishRelease(f.output, f.context, seal.checksums_sha256, r.options), /published or belongs/);
  assert.equal(r.state.calls.filter(call => call.method).length, writes);
  assert.equal(r.state.uploads.length, uploads);
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

test('draft recovery finds the exact tag beyond the first page without creating another release', async t => {
  const f = fixture(t), seal = f.assemble(), r = await interrupted(f, seal);
  r.state.otherReleases = Array.from({ length: 100 }, (_, index) => ({
    id: 1000 + index, tag_name: `${version}-rc.${index}`, draft: index % 2 === 0, body: 'another release'
  }));
  const calls = r.state.calls.length;
  await publishRelease(f.output, f.context, seal.checksums_sha256, r.options);
  assert.deepEqual(r.state.calls.slice(calls).filter(call => call.endpoint.startsWith('releases?')).map(call => call.endpoint),
    ['releases?per_page=100&page=1', 'releases?per_page=100&page=2']);
  assert.equal(r.state.calls.filter(call => call.method === 'POST').length, 1);
  assert.equal(r.state.release.id, 42);
  assert.equal(r.state.release.draft, false);
  assert.ok(!r.state.uploads[1].includes(r.state.uploads[0][0]));
});

test('multiple releases for the same tag block recovery before any remote write', async t => {
  const f = fixture(t), seal = f.assemble();
  for (const draft of [true, false]) {
    const r = await interrupted(f, seal);
    r.state.otherReleases = [{ ...r.state.release, id: 43, draft }];
    const writes = r.state.calls.filter(call => call.method).length;
    await assert.rejects(() => publishRelease(f.output, f.context, seal.checksums_sha256, r.options), /Multiple releases/);
    assert.equal(r.state.calls.filter(call => call.method).length, writes);
    assert.equal(r.state.uploads.length, 1);
    assert.equal(r.state.release.draft, true);
  }
});

test('release listing failures and malformed pages never count as an absent release', async t => {
  const f = fixture(t), seal = f.assemble();
  for (const failure of [401, 403, 404, 429, 500, 'malformed', 'later-page']) {
    const r = remote(f), baseAPI = r.options.api;
    r.options.api = async (repo, endpoint, options) => {
      if (endpoint.startsWith('releases?')) {
        if (failure === 'malformed') return { releases: [] };
        if (failure === 'later-page' && endpoint.endsWith('page=1'))
          return Array.from({ length: 100 }, (_, id) => ({ id, tag_name: 'another-version', draft: false }));
        throw Error(`HTTP ${failure === 'later-page' ? 403 : failure}`);
      }
      return baseAPI(repo, endpoint, options);
    };
    await assert.rejects(() => publishRelease(f.output, f.context, seal.checksums_sha256, r.options),
      failure === 'malformed' ? /Invalid GitHub listing/ : /HTTP/);
    assert.ok(!r.state.calls.some(call => call.method));
    assert.equal(r.state.uploads.length, 0);
    assert.equal(r.state.release, undefined);
  }
});

test('remote corruption, moved tag and read errors leave drafts unpublished', async t => {
  const f = fixture(t), seal = f.assemble();
  for (const failure of ['corrupt', 'tag', 'permission']) {
    const r = remote(f), baseAPI = r.options.api;
    if (failure === 'corrupt') r.options.downloadHash = async () => '0'.repeat(64);
    r.options.api = async (repo, endpoint, opts) => {
      if (failure === 'permission' && endpoint.startsWith('releases?')) throw Error('HTTP 403');
      if (failure === 'tag' && endpoint.startsWith('git/ref/') && r.state.assets.length)
        return { object: { type: 'commit', sha: '0'.repeat(40) } };
      return baseAPI(repo, endpoint, opts);
    };
    await assert.rejects(() => publishRelease(f.output, f.context, seal.checksums_sha256, r.options));
    assert.ok(!r.state.calls.some(c => c.method === 'PATCH'));
    assert.ok(!r.state.release || r.state.release.draft);
  }
});

test('release context comes from the current tag run without completed-run API queries', t => {
  const f = fixture(t), env = { GITHUB_REPOSITORY: f.context.repository, GITHUB_EVENT_NAME: 'push',
    GITHUB_REF: `refs/tags/${version}`, GITHUB_SHA: f.context.commit,
    GITHUB_WORKFLOW_REF: `${f.context.repository}/.github/workflows/release.yml@refs/tags/${version}`,
    GITHUB_RUN_ID: '101', GITHUB_RUN_ATTEMPT: '2' };
  assert.deepEqual(releaseContext(f.source, env).context, f.context);
  for (const change of [{ GITHUB_EVENT_NAME: 'workflow_dispatch' }, { GITHUB_REF: `refs/heads/${version}` },
    { GITHUB_SHA: '0'.repeat(40) }, { GITHUB_RUN_ID: '' }, { GITHUB_RUN_ATTEMPT: '0' },
    { GITHUB_WORKFLOW_REF: `${f.context.repository}/.github/workflows/other.yml@refs/tags/${version}` }])
    assert.throws(() => releaseContext(f.source, { ...env, ...change }));
});

async function interrupted(f, seal, count = 1) {
  const r = remote(f), normalUpload = r.options.upload;
  r.options.upload = async (tag, paths, repository) => {
    await normalUpload(tag, paths.slice(0, count), repository);
    throw Error('upload response lost');
  };
  await assert.rejects(() => publishRelease(f.output, f.context, seal.checksums_sha256, r.options), /upload response lost/);
  r.options.upload = normalUpload;
  return r;
}

test('publish retry uses downloaded sealed assets and original context despite a new run attempt', async t => {
  const f = fixture(t), seal = f.assemble(), r = await interrupted(f, seal);
  const artifact = p.join(f.temp, 'sealed-artifact'), downloaded = p.join(f.temp, 'retry-download');
  fs.cpSync(f.output, artifact, { recursive: true });
  const contextFile = p.join(f.temp, 'release-source.json'); writeJSON(contextFile, f.context);
  // A publish retry has no build/verification directories and cannot reassemble.
  for (const directory of [f.output, f.candidates, f.verifications]) fs.rmSync(directory, { recursive: true });
  fs.cpSync(artifact, downloaded, { recursive: true });
  const savedAttempt = process.env.GITHUB_RUN_ATTEMPT;
  process.env.GITHUB_RUN_ATTEMPT = String(f.context.run_attempt + 1);
  try {
    const result = await publishRelease(downloaded, json(contextFile), seal.checksums_sha256, r.options);
    assert.equal(result.status, 'published');
  } finally {
    if (savedAttempt === undefined) delete process.env.GITHUB_RUN_ATTEMPT;
    else process.env.GITHUB_RUN_ATTEMPT = savedAttempt;
  }
  assert.deepEqual(json(p.join(downloaded, 'release.json')).context, f.context);
  assert.equal(r.state.calls.filter(call => call.method === 'POST').length, 1);
  assert.ok(!r.state.uploads[1].includes(r.state.uploads[0][0]));
  for (const asset of r.state.assets) assert.deepEqual(r.state.bytes.get(asset.id), fs.readFileSync(p.join(artifact, asset.name)));
});

test('lost response after uploading every asset resumes without uploading any asset twice', async t => {
  const f = fixture(t), seal = f.assemble(), r = await interrupted(f, seal, Infinity);
  assert.equal(r.state.assets.length, fs.readdirSync(f.output).length);
  await publishRelease(f.output, f.context, seal.checksums_sha256, r.options);
  assert.equal(r.state.uploads.length, 1);
  assert.equal(r.state.calls.filter(call => call.method === 'POST').length, 1);
  assert.equal(r.state.release.draft, false);
});

test('foreign batch contexts and modified local assets stop before any remote request', async t => {
  const f = fixture(t), seal = f.assemble();
  for (const change of [{ run_id: 202 }, { run_attempt: 3 }, { repository: 'foreign/teamx' }, { commit: 'b'.repeat(40) }]) {
    const r = remote(f);
    await assert.rejects(() => publishRelease(f.output, { ...f.context, ...change }, seal.checksums_sha256, r.options));
    assert.equal(r.state.calls.length, 0);
    assert.equal(r.state.uploads.length, 0);
  }
  const r = remote(f);
  fs.appendFileSync(p.join(f.output, 'install.sh'), '\nchanged');
  await assert.rejects(() => publishRelease(f.output, f.context, seal.checksums_sha256, r.options), /asset changed/);
  assert.equal(r.state.calls.length, 0);
});

test('a draft from a different seal is never edited, replaced or uploaded into', async t => {
  const f = fixture(t), seal = f.assemble(), r = await interrupted(f, seal);
  r.state.release.body += 'different sealed batch';
  const writes = r.state.calls.filter(call => call.method).length;
  await assert.rejects(() => publishRelease(f.output, f.context, seal.checksums_sha256, r.options), /different accepted assets/);
  assert.equal(r.state.calls.filter(call => call.method).length, writes);
  assert.equal(r.state.uploads.length, 1);
});

test('corrupt, duplicate, unexpected and incomplete remote assets block draft recovery', async t => {
  const f = fixture(t), seal = f.assemble();
  for (const mode of ['bytes', 'digest', 'size', 'state', 'duplicate', 'unexpected', 'download']) {
    await t.test(mode, async () => {
      const r = await interrupted(f, seal), asset = r.state.assets[0];
      if (mode === 'bytes') r.state.bytes.set(asset.id, Buffer.from('different remote bytes'));
      if (mode === 'digest') asset.digest = 'sha256:' + '0'.repeat(64);
      if (mode === 'size') asset.size++;
      if (mode === 'state') asset.state = 'new';
      if (mode === 'duplicate') r.state.assets.push({ ...asset, id: 999 });
      if (mode === 'unexpected') r.state.assets.push({ ...asset, id: 999, name: 'unexpected.env' });
      if (mode === 'download') r.options.downloadHash = async () => { throw Error('HTTP 403'); };
      await assert.rejects(() => publishRelease(f.output, f.context, seal.checksums_sha256, r.options));
      assert.equal(r.state.uploads.length, 1);
      assert.ok(!r.state.calls.some(call => call.method === 'PATCH'));
      assert.equal(r.state.release.draft, true);
    });
  }
});

test('an incomplete successful upload and local changes during upload leave the release draft', async t => {
  const f = fixture(t), seal = f.assemble();
  for (const mode of ['missing', 'local-change']) {
    const r = remote(f), normalUpload = r.options.upload;
    r.options.upload = async (tag, paths, repository) => {
      await normalUpload(tag, mode === 'missing' ? paths.slice(1) : paths, repository);
      if (mode === 'local-change') fs.appendFileSync(p.join(f.output, 'README.zh.md'), 'changed during upload');
    };
    await assert.rejects(() => publishRelease(f.output, f.context, seal.checksums_sha256, r.options),
      mode === 'missing' ? /matrix\/assets incomplete/ : /asset changed/);
    assert.ok(!r.state.calls.some(call => call.method === 'PATCH'));
    assert.equal(r.state.release.draft, true);
  }
});

test('remote annotated tags resolve to the exact commit before creation and again before publication', async t => {
  const f = fixture(t), seal = f.assemble(), r = remote(f), baseAPI = r.options.api;
  const tag = 'c'.repeat(40), nested = 'd'.repeat(40);
  r.options.api = async (repository, endpoint, options) => {
    if (endpoint.startsWith('git/ref/tags/')) return { object: { type: 'tag', sha: tag } };
    if (endpoint === `git/tags/${tag}`) return { object: { type: 'tag', sha: nested } };
    if (endpoint === `git/tags/${nested}`) return { object: { type: 'commit', sha: f.context.commit } };
    return baseAPI(repository, endpoint, options);
  };
  await publishRelease(f.output, f.context, seal.checksums_sha256, r.options);
  assert.equal(r.state.release.draft, false);
});

test('a moved, cyclic or non-commit remote tag never creates a draft', async t => {
  const f = fixture(t), seal = f.assemble();
  for (const mode of ['moved', 'cycle', 'blob']) {
    const r = remote(f), baseAPI = r.options.api;
    r.options.api = async (repository, endpoint, options) => {
      if (endpoint.startsWith('git/ref/') || endpoint.startsWith('git/tags/'))
        return { object: mode === 'cycle' ? { type: 'tag', sha: 'c'.repeat(40) } :
          { type: mode === 'blob' ? 'blob' : 'commit', sha: '0'.repeat(40) } };
      return baseAPI(repository, endpoint, options);
    };
    await assert.rejects(() => publishRelease(f.output, f.context, seal.checksums_sha256, r.options), /Remote tag moved/);
    assert.ok(!r.state.calls.some(call => call.method));
    assert.equal(r.state.uploads.length, 0);
  }
});

test('a lost final publication response cannot turn a published version into a resumable draft', async t => {
  const f = fixture(t), seal = f.assemble(), r = remote(f), baseAPI = r.options.api;
  r.options.api = async (repository, endpoint, options) => {
    const result = await baseAPI(repository, endpoint, options);
    if (options?.method === 'PATCH') throw Error('publication response lost');
    return result;
  };
  await assert.rejects(() => publishRelease(f.output, f.context, seal.checksums_sha256, r.options), /publication response lost/);
  assert.equal(r.state.release.draft, false);
  const writes = r.state.calls.filter(call => call.method).length;
  await assert.rejects(() => publishRelease(f.output, f.context, seal.checksums_sha256, r.options), /published or belongs/);
  assert.equal(r.state.calls.filter(call => call.method).length, writes);
  assert.equal(r.state.uploads.length, 1);
});

test('every package and CI report is mandatory and additional artifact directories are refused', t => {
  const f = fixture(t);
  for (const row of f.rows) {
    for (const directory of [row.directory, row.verificationDir]) {
      const moved = p.join(f.temp, 'temporarily-absent');
      fs.renameSync(directory, moved);
      assert.throws(f.assemble, /matrix incomplete or unexpected/);
      assert.equal(fs.existsSync(f.output), false);
      fs.renameSync(moved, directory);
    }
  }
  for (const parent of [f.candidates, f.verifications]) {
    const extra = p.join(parent, 'different-build-batch'); fs.mkdirSync(extra);
    assert.throws(f.assemble, /matrix incomplete or unexpected/);
    assert.equal(fs.existsSync(f.output), false);
    fs.rmdirSync(extra);
  }
});

test('the standalone plan CLI exports the exact annotated-tag source and batch for workflow outputs', t => {
  const f = fixture(t), output = p.join(f.temp, 'context.json'), githubOutput = p.join(f.temp, 'github-output');
  f.git('tag', '-d', version); f.git('tag', '-am', 'annotated source fixture', version);
  const result = spawnSync(process.execPath, [p.join(f.source, 'scripts/package-publish.mjs'), 'plan', output], {
    cwd: f.source, encoding: 'utf8', env: { ...process.env,
      GITHUB_REPOSITORY: f.context.repository, GITHUB_EVENT_NAME: 'push', GITHUB_REF: `refs/tags/${version}`,
      GITHUB_SHA: f.context.commit, GITHUB_WORKFLOW_REF: `${f.context.repository}/.github/workflows/release.yml@refs/tags/${version}`,
      GITHUB_RUN_ID: String(f.context.run_id), GITHUB_RUN_ATTEMPT: String(f.context.run_attempt), GITHUB_OUTPUT: githubOutput
    }
  });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(json(output), f.context);
  const values = Object.fromEntries(fs.readFileSync(githubOutput, 'utf8').trimEnd().split('\n').map(line => {
    const equals = line.indexOf('='); return [line.slice(0, equals), line.slice(equals + 1)];
  }));
  assert.equal(values.version, version);
  assert.equal(values.commit, f.context.commit);
  assert.deepEqual(JSON.parse(values.context), f.context);
  assert.deepEqual(JSON.parse(values.matrix).include, f.matrix);
  assert.ok(values.node_version && values.go_version && values.dependency_key);
});

test('GitHub HTTP failures never count as a missing release except an explicitly allowed 404', async t => {
  const previous = process.env.GH_TOKEN;
  process.env.GH_TOKEN = 'fixture-only';
  t.after(() => { if (previous === undefined) delete process.env.GH_TOKEN; else process.env.GH_TOKEN = previous; });
  let status = 404;
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    assert.equal(url, 'https://api.github.com/repos/example/teamx/releases/tags/v0.0.0');
    assert.equal(options.method, 'GET');
    return new Response(JSON.stringify({ message: 'private transport detail' }), { status });
  });
  assert.equal(await githubRequest('example/teamx', 'releases/tags/v0.0.0', { missing: true }), undefined);
  await assert.rejects(() => githubRequest('example/teamx', 'releases/tags/v0.0.0'), /HTTP 404/);
  for (status of [401, 403, 429, 500]) {
    await assert.rejects(() => githubRequest('example/teamx', 'releases/tags/v0.0.0', { missing: true }), error => {
      assert.ok(error.message.includes(`HTTP ${status}`));
      assert.ok(!error.message.includes('private transport detail'));
      return true;
    });
  }
});
