import * as fs from 'node:fs';
import * as os from 'node:os';
import * as p from 'node:path';
import { fileURLToPath } from 'node:url';
import { extract, hash, inventory, json, must, run, writeJSON } from './package-lib.mjs';
import { assertNative, snapshotPlan } from './package-ci.mjs';
import { assertReleaseSource, releaseBatch } from './package-release-source.mjs';
import { machOFiles } from './package-signing.mjs';
import { checkManifestProfile, profileNames, releaseProfiles } from './package-profiles.mjs';

const root = p.resolve(p.dirname(fileURLToPath(import.meta.url)), '..');
const smokeInputs = { smoke_sha256: 'scripts/package-smoke.mjs',
  native_check_sha256: 'scripts/check-dsh-native.mjs', fixture_sha256: 'scripts/fixtures/acp.mjs' };
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const digest = file => hash(fs.readFileSync(file));

export function releasePlan(sourceRoot, version) {
  const commit = assertReleaseSource(sourceRoot, version);
  const lock = json(p.join(sourceRoot, 'packaging/dependencies.lock.json'));
  const profiles = releaseProfiles(lock);
  must(same([...profiles].sort(), ['core', 'standard']), 'Official release requires core/standard only');
  const plans = profiles.map(profile => snapshotPlan(sourceRoot, profile));
  const targets = ['darwin-amd64', 'darwin-arm64', 'linux-amd64', 'linux-arm64'];
  must(plans.every(plan => same(plan.matrix.include.map(row => row.target).sort(), targets)),
    'Official release requires all four native targets');
  return { ...plans[0], matrix: { include: plans.flatMap(plan => plan.matrix.include) }, version, commit };
}

function checkSmoke(report, receipt, sourceRoot) {
  must(report?.status === 'passed' && report.archive_sha256 === receipt.archive_sha256 &&
    report.target === receipt.manifest.target && report.profile === receipt.manifest.profile, 'Candidate smoke report mismatch');
  const native = { 'darwin-arm64': ['darwin', 'arm64', 'arm64'], 'darwin-amd64': ['darwin', 'x64', 'x86_64'],
    'linux-arm64': ['linux', 'arm64', 'aarch64'], 'linux-amd64': ['linux', 'x64', 'x86_64'] }[receipt.manifest.target];
  must(report.schema_version === 1 && same([report.environment?.platform, report.environment?.process_arch, report.environment?.machine], native) &&
    Array.isArray(report.checks) && report.checks.length > 0 && report.checks.every(check => typeof check === 'string'),
    'Missing native smoke environment or completed checks');
  for (const [key, file] of Object.entries(smokeInputs))
    must(report[key] === digest(p.join(sourceRoot, file)), `Candidate verifier differs: ${file}`);
}

// No code from the archive is executed until its identity has been checked.
export function inspectCandidate(directory, target, profile, commit, version, sourceRoot = root) {
  must(/^v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)$/.test(version), 'Invalid candidate version');
  must(/^[a-f0-9]{40}$/.test(commit), 'Expected a full candidate source commit');
  must(/^(darwin|linux)-(arm64|amd64)$/.test(target) && profileNames.includes(profile), 'Invalid candidate target/profile');
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
    checkManifestProfile(manifest, json(p.join(sourceRoot, 'packaging/dependencies.lock.json')));
    must(manifest.distribution?.schema_version === 2 && manifest.distribution.kind === 'release_archive' &&
      Object.keys(manifest.distribution).every(key => ['schema_version', 'kind', 'signing', 'build'].includes(key)),
      'Archive is not a release archive');
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
    } else must(security.signing.mode === 'not_applicable' && security.signing.status === 'not_applicable' && security.notarization.status === 'not_applicable', 'Linux signing report mismatch');
    const receipt = { schema_version: 2, kind: 'release_archive', archive,
      archive_sha256: archiveHash, build_report_sha256: digest(p.join(directory, name + '-report.json')),
      security_report_sha256: digest(p.join(directory, name + '-security.json')), security, manifest };
    checkSmoke(json(p.join(directory, name + '-report.json')), receipt, sourceRoot);
    return receipt;
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
}

export function verifyCandidate(directory, target, profile, commit, version, output) {
  const batch = releaseBatch();
  must(batch, 'CI build batch is required for download verification');
  assertNative(target);
  assertReleaseSource(root, version, commit);
  must(!fs.existsSync(output), 'Candidate verification report already exists');
  const receipt = inspectCandidate(directory, target, profile, commit, version);
  must(same(json(p.join(directory, 'candidate.json')), receipt), 'Candidate receipt differs');
  must(same(receipt.manifest.distribution.build, batch), 'Archive build batch differs');
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
    writeJSON(output, verificationReport(receipt, smoke, batch));
  } finally { fs.rmSync(temp, { recursive: true, force: true }); }
}

// Reports describe only checks actually completed by the native CI jobs.
export function verificationReport(receipt, smoke, batch, sourceRoot = root) {
  must(batch && /^[1-9]\d*$/.test(String(batch.run_id)) && Number.isSafeInteger(batch.run_id) &&
    /^[1-9]\d*$/.test(String(batch.run_attempt)) && Number.isSafeInteger(batch.run_attempt), 'Invalid verification batch');
  checkSmoke(smoke, receipt, sourceRoot);
  must(same(receipt.manifest.distribution.build, batch), 'Archive build batch differs');
  const report = { schema_version: 2, kind: 'archive_verification', status: 'passed',
    batch, archive: receipt.archive, archive_sha256: receipt.archive_sha256,
    version: receipt.manifest.version, commit: receipt.manifest.source.commit,
    target: receipt.manifest.target, profile: receipt.manifest.profile,
    manifest_sha256: hash(JSON.stringify(receipt.manifest, null, 2) + '\n'),
    dependency_lock_sha256: receipt.manifest.dependency_lock_sha256,
    build_report_sha256: receipt.build_report_sha256, security_report_sha256: receipt.security_report_sha256,
    verifier_inputs: ['scripts/package-release.mjs', 'scripts/package-ci.mjs', 'scripts/package-lib.mjs',
      'scripts/package-release-source.mjs', 'scripts/package-profiles.mjs', 'scripts/package-signing.mjs',
      'scripts/package-platform.mjs', 'scripts/fixtures/models.mjs',
      ...Object.values(smokeInputs)].map(path => ({ path, sha256: digest(p.join(sourceRoot, path)) })),
    checks: { archive_identity: 'passed', build_smoke: 'passed', download_smoke: 'passed' },
    download_smoke: Object.fromEntries(['schema_version', 'status', 'archive_sha256', 'target', 'profile', 'environment', 'checks', ...Object.keys(smokeInputs)]
      .map(key => [key, smoke[key]])) };
  return report;
}

export function checkVerification(receipt, report, batch, sourceRoot = root) {
  const expected = verificationReport(receipt, report?.download_smoke, batch, sourceRoot);
  must(same(report, expected), 'Incomplete or mismatched archive verification');
  return expected;
}

if (process.argv[1] && fs.existsSync(process.argv[1]) && fs.realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
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
    else throw Error('Usage: package-release.mjs plan VERSION | record DIR TARGET PROFILE COMMIT VERSION | verify DIR TARGET PROFILE COMMIT VERSION REPORT');
  } catch (error) { console.error(`Candidate failed: ${error.message}`); process.exitCode = 1; }
}
