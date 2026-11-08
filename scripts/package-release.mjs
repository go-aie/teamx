import * as fs from 'node:fs';
import * as os from 'node:os';
import * as p from 'node:path';
import { fileURLToPath } from 'node:url';
import { extract, hash, inventory, json, must, run, writeJSON } from './package-lib.mjs';
import { assertNative, snapshotPlan } from './package-ci.mjs';
import { assertReleaseSource } from './package-release-source.mjs';
import { checkRealDSH } from './package-acceptance-lib.mjs';
import { machOFiles } from './package-signing.mjs';

const root = p.resolve(p.dirname(fileURLToPath(import.meta.url)), '..');
const smokeInputs = { smoke_sha256: 'scripts/package-smoke.mjs',
  native_check_sha256: 'scripts/check-dsh-native.mjs', fixture_sha256: 'scripts/fixtures/acp.mjs' };
const acceptanceInputs = ['scripts/package-acceptance.mjs', 'scripts/package-acceptance-lib.mjs',
  'scripts/package-browser.mjs', 'scripts/package-lib.mjs', 'scripts/package-platform.mjs',
  ...Object.values(smokeInputs), 'internal/room/bundle_real_e2e_test.go',
  'internal/room/dsh_real_test.go', 'internal/room/demo_test.go'];
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const digest = file => hash(fs.readFileSync(file));

export function releasePlan(sourceRoot, version) {
  const commit = assertReleaseSource(sourceRoot, version);
  const core = snapshotPlan(sourceRoot, 'core'), full = snapshotPlan(sourceRoot, 'full');
  return { ...core, matrix: { include: [...core.matrix.include, ...full.matrix.include] }, version, commit };
}

function checkSmoke(report, receipt, sourceRoot) {
  must(report.status === 'passed' && report.archive_sha256 === receipt.archive_sha256 &&
    report.target === receipt.manifest.target && report.profile === receipt.manifest.profile, 'Candidate smoke report mismatch');
  for (const [key, file] of Object.entries(smokeInputs))
    must(report[key] === digest(p.join(sourceRoot, file)), `Candidate verifier differs: ${file}`);
}

// No code from the archive is executed until its identity has been checked.
export function inspectCandidate(directory, target, profile, commit, version, sourceRoot = root) {
  must(/^v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)$/.test(version), 'Invalid candidate version');
  must(/^[a-f0-9]{40}$/.test(commit), 'Expected a full candidate source commit');
  must(/^(darwin|linux)-(arm64|amd64)$/.test(target) && ['core', 'full'].includes(profile), 'Invalid candidate target/profile');
  const name = `teamx-${version}-${target}-${profile}`;
  const required = [name + '.tar.gz', name + '.tar.gz.sha256', name + '-report.json', name + '-security.json'];
  const names = fs.readdirSync(directory).sort();
  must(required.every(n => names.includes(n)) && names.every(n => [...required, 'candidate.json'].includes(n)),
    'Candidate contains missing or unexpected files');
  for (const name of names) must(fs.lstatSync(p.join(directory, name)).isFile(), 'Candidate asset is not a regular file');
  const archive = required[0], archiveHash = digest(p.join(directory, archive));
  must(fs.readFileSync(p.join(directory, archive + '.sha256'), 'utf8') === `${archiveHash}  ${archive}\n`, 'Candidate checksum mismatch');
  const temp = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-candidate-'));
  try {
    extract(p.join(directory, archive), temp);
    must(same(fs.readdirSync(temp), [name]), 'Candidate root/name mismatch');
    const bundle = p.join(temp, name), manifest = json(p.join(bundle, 'manifest.json'));
    must(same(inventory(bundle).filter(f => f.path !== 'checksums.txt'), json(p.join(bundle, 'checksums.txt'))),
      'Candidate immutable contents differ');
    must(manifest.channel === 'release' && manifest.version === version && manifest.target === target &&
      manifest.profile === profile && manifest.source.commit === commit && manifest.source.dirty === false,
      'Candidate source/tag/target/profile/channel mismatch');
    must(manifest.distribution?.kind === 'release_candidate' && manifest.distribution.release_ready === false,
      'Archive is not an unqualified release candidate');
    must(manifest.dependency_lock_sha256 === digest(p.join(sourceRoot, 'packaging/dependencies.lock.json')),
      'Candidate dependency lock differs');
    for (const key of ['build_inputs', 'packaging_inputs']) {
      must(Array.isArray(manifest.source[key]) && manifest.source[key].length > 0, `Missing candidate ${key}`);
      const seen = new Set();
      for (const input of manifest.source[key]) {
        must(typeof input.path === 'string' && !p.isAbsolute(input.path) &&
          !input.path.split('/').some(part => ['', '..'].includes(part)) && !seen.has(input.path), 'Unsafe/duplicate candidate source input');
        seen.add(input.path);
        must(digest(p.join(sourceRoot, input.path)) === input.sha256, `Candidate source input differs: ${input.path}`);
      }
    }
    const security = json(p.join(directory, name + '-security.json'));
    must(security.archive_sha256 === archiveHash && security.target === target && security.profile === profile &&
      same(security.signing, manifest.distribution.signing), 'Candidate security report mismatch');
    if (target.startsWith('darwin-')) {
      must(['unsigned', 'developer-id'].includes(security.signing.mode), 'Unknown candidate signing mode');
      if (security.signing.mode === 'developer-id') {
        must(security.signing.status === 'passed' && security.notarization.status === 'passed' &&
          /^[a-f0-9-]{36}$/i.test(security.notarization.submission_id || ''), 'Missing candidate signing/notarization');
        must(same(security.signing.files.map(item => item.path), machOFiles(bundle)), 'Incomplete signed-code inventory');
        for (const item of security.signing.files) must(digest(p.join(bundle, item.path)) === item.sha256, 'Signed code changed');
      } else must(security.signing.status === 'not_run' && security.notarization.status === 'not_run', 'Unsigned candidate claims signing');
    } else must(security.signing.status === 'not_applicable' && security.notarization.status === 'not_applicable', 'Linux signing report mismatch');
    const receipt = { schema_version: 1, kind: 'release_candidate', release_ready: false, archive,
      archive_sha256: archiveHash, build_report_sha256: digest(p.join(directory, name + '-report.json')),
      security_report_sha256: digest(p.join(directory, name + '-security.json')), security, manifest };
    checkSmoke(json(p.join(directory, name + '-report.json')), receipt, sourceRoot);
    return receipt;
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
}

export function verifyCandidate(directory, target, profile, commit, version, output) {
  assertNative(target);
  assertReleaseSource(root, version, commit);
  must(!fs.existsSync(output), 'Candidate verification report already exists');
  const receipt = inspectCandidate(directory, target, profile, commit, version);
  must(same(json(p.join(directory, 'candidate.json')), receipt), 'Candidate receipt differs');
  const temp = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-candidate-smoke-'));
  try {
    const report = p.join(temp, 'smoke.json');
    run(process.execPath, [p.join(root, 'scripts/package-smoke.mjs'), p.join(directory, receipt.archive), '--report', report], { stdio: 'inherit' });
    const smoke = json(report);
    checkSmoke(smoke, receipt, root);
    must(same(inspectCandidate(directory, target, profile, commit, version), receipt), 'Candidate changed during download verification');
    must(same(json(p.join(directory, 'candidate.json')), receipt), 'Candidate receipt changed during download verification');
    assertReleaseSource(root, version, commit);
    fs.mkdirSync(p.dirname(output), { recursive: true });
    writeJSON(output, { ...receipt, status: 'passed', scope: 'candidate transport identity and isolated archive smoke', download_smoke: smoke });
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
}

// Read P3 evidence privately; publish only hashes/status, never model answers,
// account identifiers, Session data, local paths or screenshots.
export function assessCandidate(receipt, evidenceDirectory, sourceRoot = root) {
  const checks = { archive_smoke: 'passed', real_dsh_acp: 'not_run', browser: 'not_run',
    clean_machine: 'not_run', downloaded_system_security: 'not_run',
    ...(receipt.manifest.target.startsWith('darwin-') ? {
      code_signing: receipt.security.signing.status, notarization: receipt.security.notarization.status
    } : {}),
    ...(receipt.manifest.profile === 'full' ? { git_tls_clone: 'not_run', platform_login_read_bind: 'not_run' } : {}) };
  let acceptance;
  if (evidenceDirectory) {
    const file = p.join(evidenceDirectory, 'report.json'), report = json(file), manifest = receipt.manifest;
    must(report.status === 'passed' && report.archive_sha256 === receipt.archive_sha256 &&
      report.target === manifest.target && report.profile === manifest.profile &&
      report.manifest_sha256 === hash(JSON.stringify(manifest, null, 2) + '\n') &&
      same(report.source, manifest.source) && same(report.dependencies, manifest.dependencies), 'Acceptance belongs to another candidate or did not pass');
    const native = { 'darwin-arm64': ['darwin', 'arm64', 'arm64'], 'darwin-amd64': ['darwin', 'x64', 'x86_64'],
      'linux-arm64': ['linux', 'arm64', 'aarch64'], 'linux-amd64': ['linux', 'x64', 'x86_64'] }[manifest.target];
    must(same([report.environment?.platform, report.environment?.process_arch, report.environment?.machine], native), 'Acceptance was not on the native target');
    must(Array.isArray(report.verifier_inputs) && same(report.verifier_inputs.map(i => i.path).sort(), [...acceptanceInputs].sort()),
      'Incomplete acceptance verifier fingerprints');
    for (const input of report.verifier_inputs)
      must(input.sha256 === digest(p.join(sourceRoot, input.path)), `Acceptance verifier changed: ${input.path}`);
    for (const key of ['archive_smoke', 'real_dsh_acp', 'browser']) {
      const item = report.checks[key];
      const expected = { archive_smoke: 'smoke.json', real_dsh_acp: 'real-dsh/report.json', browser: 'browser/report.json' }[key];
      must(item?.status === 'passed' && item.evidence === expected && item.sha256 === digest(p.join(evidenceDirectory, expected)),
        `Missing/mismatched acceptance evidence: ${key}`);
    }
    checkSmoke(json(p.join(evidenceDirectory, 'smoke.json')), receipt, sourceRoot);
    checkRealDSH(json(p.join(evidenceDirectory, 'real-dsh/report.json')));
    const browser = json(p.join(evidenceDirectory, 'browser/report.json'));
    must(browser.status === 'passed' && browser.fixture === false, 'Actual browser evidence missing');
    checks.real_dsh_acp = checks.browser = 'passed';
    acceptance = { report_sha256: digest(file), verifier_inputs: report.verifier_inputs };
  }
  return { schema_version: 1, kind: 'candidate_assessment', archive: receipt.archive,
    archive_sha256: receipt.archive_sha256, release_ready: false, status: 'incomplete', checks,
    ...(acceptance ? { acceptance } : {}),
    remaining: Object.entries(checks).filter(([, status]) => status !== 'passed').map(([key]) => key) };
}

if (process.argv[1] && p.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [command, ...args] = process.argv.slice(2);
    if (command === 'plan' && args.length === 1) {
      const plan = releasePlan(root, args[0]);
      if (process.env.GITHUB_OUTPUT) fs.appendFileSync(process.env.GITHUB_OUTPUT,
        Object.entries(plan).map(([key, value]) => `${key}=${typeof value === 'string' ? value : JSON.stringify(value)}\n`).join(''));
      console.log(JSON.stringify(plan, null, 2));
    } else if (command === 'record' && args.length === 5) {
      assertReleaseSource(root, args[4], args[3]);
      must(!fs.existsSync(p.join(args[0], 'candidate.json')), 'Candidate receipt already exists');
      writeJSON(p.join(args[0], 'candidate.json'), inspectCandidate(...args));
    } else if (command === 'verify' && args.length === 6) verifyCandidate(...args);
    else if (command === 'assess' && [6, 7].includes(args.length)) {
      const [directory, target, profile, commit, version, output, evidence] = args;
      must(!fs.existsSync(output), 'Candidate assessment already exists');
      const receipt = inspectCandidate(directory, target, profile, commit, version);
      must(same(json(p.join(directory, 'candidate.json')), receipt), 'Candidate receipt differs');
      const assessment = assessCandidate(receipt, evidence);
      writeJSON(output, assessment);
      console.log(`Candidate assessment: ${assessment.status}; remaining: ${assessment.remaining.join(', ')}`);
      process.exitCode = 2;
    } else throw Error('Usage: package-release.mjs plan VERSION | record DIR TARGET PROFILE COMMIT VERSION | verify DIR TARGET PROFILE COMMIT VERSION REPORT | assess DIR TARGET PROFILE COMMIT VERSION REPORT [PRIVATE_EVIDENCE_DIR]');
  } catch (error) { console.error(`Candidate failed: ${error.message}`); process.exitCode = 1; }
}
