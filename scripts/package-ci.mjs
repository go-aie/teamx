import * as fs from 'node:fs';
import * as os from 'node:os';
import * as p from 'node:path';
import { fileURLToPath } from 'node:url';
import { extract, hash, json, must, run, writeJSON } from './package-lib.mjs';
import { checkManifestProfile, profileNames } from './package-profiles.mjs';

const root = p.resolve(p.dirname(fileURLToPath(import.meta.url)), '..');
const lockPath = 'packaging/dependencies.lock.json';
const smokeInputs = {
  smoke_sha256: 'scripts/package-smoke.mjs',
  native_check_sha256: 'scripts/check-dsh-native.mjs',
  fixture_sha256: 'scripts/fixtures/acp.mjs'
};

export function snapshotPlan(sourceRoot, profile = 'standard') {
  const lock = json(p.join(sourceRoot, lockPath));
  must(profileNames.includes(profile) && lock.profiles[profile], 'Unknown snapshot profile');
  const targets = Object.entries(lock.targets).filter(([, entry]) => entry.delivery === 'included');
  for (const target of ['darwin-arm64', 'linux-amd64'])
    must(targets.some(([name]) => name === target), `Required snapshot target missing: ${target}`);
  const include = targets.map(([target, entry]) => {
    must(/^(darwin|linux)-(arm64|amd64)$/.test(target), `Unsupported snapshot target: ${target}`);
    must(typeof entry.runner === 'string' && /^[a-z0-9][a-z0-9.-]*$/.test(entry.runner), `Missing/invalid runner: ${target}`);
    return { target, runner: entry.runner, profile };
  });
  const cacheInputs = [lockPath, 'packaging/dsh/package-lock.json', 'packaging/dsh/package.json',
    lock.dependencies.glab.contract];
  return {
    matrix: { include },
    go_version: lock.build_tools.go.version,
    node_version: lock.build_tools.node,
    // The workflow also includes runner OS/arch, target and profile in the key.
    dependency_key: hash(JSON.stringify(cacheInputs.map(file => [file, hash(fs.readFileSync(p.join(sourceRoot, file)))])))
  };
}

export function assertNative(target) {
  const actual = `${process.platform}-${process.arch === 'x64' ? 'amd64' : process.arch}`;
  const machine = { 'darwin-arm64': 'arm64', 'darwin-amd64': 'x86_64',
    'linux-arm64': 'aarch64', 'linux-amd64': 'x86_64' }[target];
  must(target === actual && machine === os.machine(),
    `Native runner required: expected ${target}, observed ${actual}/${os.machine()}`);
  // Rosetta can translate uname's result as well as the executable.
  if (process.platform === 'darwin' && process.arch === 'x64')
    must(run('/usr/sbin/sysctl', ['-in', 'sysctl.proc_translated']).trim() !== '1',
      'Native runner required: Rosetta translation detected');
}

// Verify transport identity before executing anything from the downloaded bundle.
// The original build report and receipt are preserved; a download check writes a
// separate report and never changes the archive or promotes its qualification.
export function inspectSnapshot(directory, target, profile, commit, sourceRoot = root) {
  must(/^[a-f0-9]{40}$/.test(commit), 'Expected a full source commit');
  must(profileNames.includes(profile), 'Unknown snapshot profile');
  const names = fs.readdirSync(directory).sort();
  const archives = names.filter(name => name.endsWith('.tar.gz'));
  must(archives.length === 1, 'Expected exactly one snapshot archive');
  const archiveName = archives[0];
  must(/^teamx-dev-[a-f0-9]{12}(?:-dirty)?-(darwin|linux)-(arm64|amd64)-(core|standard|full)\.tar\.gz$/.test(archiveName),
    'Invalid snapshot archive name');
  const reportName = archiveName.replace(/\.tar\.gz$/, '-report.json');
  const required = [archiveName, archiveName + '.sha256', reportName];
  must(names.every(name => [...required, 'snapshot.json'].includes(name)) && required.every(name => names.includes(name)),
    'Snapshot contains missing or unexpected files');
  for (const name of names)
    must(fs.lstatSync(p.join(directory, name)).isFile(), `Snapshot asset is not a regular file: ${name}`);
  const archive = p.join(directory, archiveName);
  const digest = hash(fs.readFileSync(archive));
  must(fs.readFileSync(archive + '.sha256', 'utf8') === `${digest}  ${archiveName}\n`, 'Archive checksum mismatch');
  const report = json(p.join(directory, reportName));
  must(report.status === 'passed' && report.archive_sha256 === digest && report.target === target && report.profile === profile,
    'Build smoke report does not match the snapshot');
  for (const [key, file] of Object.entries(smokeInputs))
    must(report[key] === hash(fs.readFileSync(p.join(sourceRoot, file))), `Build smoke verifier differs: ${file}`);
  const temp = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-snapshot-'));
  try {
    extract(archive, temp);
    const entries = fs.readdirSync(temp);
    must(entries.length === 1 && entries[0] === archiveName.slice(0, -7), 'Snapshot root/name mismatch');
    const bundle = p.join(temp, entries[0]);
    const manifest = json(p.join(bundle, 'manifest.json'));
    checkManifestProfile(manifest, json(p.join(sourceRoot, lockPath)));
    must(manifest.channel === 'dev' && manifest.target === target && manifest.profile === profile && manifest.source.commit === commit,
      'Snapshot source/target/profile/channel mismatch');
    must(typeof manifest.source.dirty === 'boolean', 'Snapshot dirty state missing');
    must(manifest.version === `dev-${commit.slice(0, 12)}${manifest.source.dirty ? '-dirty' : ''}` &&
      entries[0] === `teamx-${manifest.version}-${target}-${profile}`, 'Snapshot version/name mismatch');
    must(manifest.dependency_lock_sha256 === hash(fs.readFileSync(p.join(sourceRoot, lockPath))), 'Snapshot dependency lock differs');
    // Check the exact source inputs, including platform-specific Go files.
    for (const input of [...manifest.source.build_inputs, ...manifest.source.packaging_inputs]) {
      must(!p.isAbsolute(input.path) && !input.path.split('/').includes('..'), 'Unsafe source input');
      must(hash(fs.readFileSync(p.join(sourceRoot, input.path))) === input.sha256, `Snapshot source input differs: ${input.path}`);
    }
    return {
      schema_version: 1,
      kind: 'development_snapshot',
      release_ready: false,
      archive: archiveName,
      archive_sha256: digest,
      build_report_sha256: hash(fs.readFileSync(p.join(directory, reportName))),
      manifest
    };
  } finally {
    fs.rmSync(temp, { recursive: true, force: true });
  }
}

export function verifySnapshot(directory, target, profile, commit, reportPath) {
  assertNative(target);
  must(!fs.existsSync(reportPath), 'Download report already exists');
  const receipt = inspectSnapshot(directory, target, profile, commit);
  must(JSON.stringify(json(p.join(directory, 'snapshot.json'))) === JSON.stringify(receipt), 'Snapshot receipt differs');
  fs.mkdirSync(p.dirname(reportPath), { recursive: true });
  const temp = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-download-smoke-'));
  try {
    const smokeReport = p.join(temp, 'smoke.json');
    run(process.execPath, [p.join(root, 'scripts/package-smoke.mjs'), p.join(directory, receipt.archive), '--report', smokeReport],
      { stdio: 'inherit' });
    must(JSON.stringify(inspectSnapshot(directory, target, profile, commit)) === JSON.stringify(receipt), 'Snapshot changed during download smoke');
    const smoke = json(smokeReport);
    must(smoke.status === 'passed' && smoke.archive_sha256 === receipt.archive_sha256 && smoke.target === target && smoke.profile === profile,
      'Download smoke report mismatch');
    writeJSON(reportPath, { ...receipt, status: 'passed', download_smoke: smoke });
  } finally {
    fs.rmSync(temp, { recursive: true, force: true });
  }
}

if (process.argv[1] && p.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [command, ...args] = process.argv.slice(2);
    if (command === 'plan' && args.length <= 1) {
      const plan = snapshotPlan(root, args[0]);
      if (process.env.GITHUB_OUTPUT)
        fs.appendFileSync(process.env.GITHUB_OUTPUT, Object.entries(plan).map(([key, value]) =>
          `${key}=${typeof value === 'string' ? value : JSON.stringify(value)}\n`).join(''));
      console.log(JSON.stringify(plan, null, 2));
    } else if (command === 'host' && args.length === 1) assertNative(args[0]);
    else if (command === 'record' && args.length === 4) {
      must(!fs.existsSync(p.join(args[0], 'snapshot.json')), 'Snapshot receipt already exists');
      writeJSON(p.join(args[0], 'snapshot.json'), inspectSnapshot(...args));
    } else if (command === 'verify' && args.length === 5) verifySnapshot(...args);
    else throw Error('Usage: package-ci.mjs plan [core|standard|full] | host TARGET | record DIR TARGET PROFILE COMMIT | verify DIR TARGET PROFILE COMMIT REPORT');
  } catch (error) {
    console.error(`Snapshot failed: ${error.message}`);
    process.exitCode = 1;
  }
}
