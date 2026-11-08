import * as fs from 'node:fs';
import * as p from 'node:path';
import * as os from 'node:os';
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { hash, json, writeJSON, must, run, extract, inventory } from './package-lib.mjs';
import { checkBinaries } from './package-platform.mjs';
import { checkSmoke, checkRealDSH, resultForScope } from './package-acceptance-lib.mjs';
import { acceptBrowser } from './package-browser.mjs';

const root = p.resolve(p.dirname(fileURLToPath(import.meta.url)), '..');
const usage = 'package-acceptance.sh ARCHIVE --output-dir NEW_PRIVATE_DIR [--model-config TOML] [--env-file FILE]';
const args = process.argv.slice(2);
if (args[0] === '--help') { console.log(usage); process.exit(0); }
const options = {}, archive = p.resolve(args.shift() || '');
while (args.length) {
  const key = args.shift();
  must(['--output-dir', '--model-config', '--env-file'].includes(key) && args.length && !args[0].startsWith('--'), usage);
  must(!options[key], `Duplicate ${key}`);
  options[key] = p.resolve(args.shift());
}
must(options['--output-dir'], usage);
const output = options['--output-dir'];
// Evidence includes private model answers, Session files and service logs.
// Exclusive creation also prevents accidental replay into an earlier run.
fs.mkdirSync(output, { mode: 0o700 });
process.umask(0o077);
const report = {
  schema_version: 1, status: 'incomplete', recorded_at: new Date().toISOString(),
  scope: 'local archive smoke + real DSH/ACP + production browser; Git/Scion/platform accounts excluded by user',
  release_ready: false,
  deferred: ['Git/TLS/clone', 'Scion', 'GitLab/Lark account login/read/bind',
    'other delivery targets', 'download quarantine/signing and clean-machine qualification'],
  environment: { platform: process.platform, process_arch: process.arch, machine: os.machine(), os_release: os.release() },
  checks: {},
  verifier_inputs: ['scripts/package-acceptance.mjs', 'scripts/package-acceptance-lib.mjs',
    'scripts/package-browser.mjs', 'scripts/package-lib.mjs', 'scripts/package-platform.mjs',
    'scripts/package-smoke.mjs', 'scripts/check-dsh-native.mjs', 'scripts/fixtures/acp.mjs',
    'internal/room/bundle_real_e2e_test.go', 'internal/room/dsh_real_test.go',
    'internal/room/demo_test.go', 'packaging/verifier/package.json',
    'packaging/verifier/package-lock.json', 'scripts/package-profiles.mjs'].map(path => ({ path, sha256: hash(fs.readFileSync(p.join(root, path))) }))
};
const save = () => writeJSON(p.join(output, 'report.json'), report);
async function step(name, work) {
  console.log(`Acceptance: ${name}`);
  try { report.checks[name] = await work(); }
  catch (error) { report.checks[name] = { status: 'failed', error: error.message }; }
  save();
  console.log(`Acceptance: ${name}: ${report.checks[name].status}`);
}
async function child(command, argv, name, env = process.env, timeout = 20 * 60000) {
  const log = fs.openSync(p.join(output, name + '.log'), 'wx', 0o600);
  try {
    await new Promise((resolve, reject) => {
      const proc = spawn(command, argv, { cwd: root, env, stdio: ['ignore', log, log] });
      // Go's acceptance deadline and owner cleanup run before this outer limit.
      // Do not kill a cleanup owner and then report successful process cleanup.
      const timer = setTimeout(() => reject(Error(`${name} exceeded outer deadline; inspect retained owner PID ${proc.pid}`)), timeout);
      proc.once('error', error => { clearTimeout(timer); reject(error); });
      proc.once('exit', code => {
        clearTimeout(timer);
        code === 0 ? resolve() : reject(Error(`${name} failed (exit ${code}); inspect ${name}.log`));
      });
    });
  } finally { fs.closeSync(log); }
}
try {
  report.archive_sha256 = hash(fs.readFileSync(archive));
  const extracted = p.join(output, 'extracted archive');
  extract(archive, extracted);
  const entries = fs.readdirSync(extracted);
  must(entries.length === 1, 'Archive must have one root');
  const bundle = p.join(extracted, entries[0]), manifest = json(p.join(bundle, 'manifest.json'));
  const expectedTarget = `${process.platform}-${process.arch === 'x64' ? 'amd64' : process.arch}`;
  must(manifest.target === expectedTarget, 'Archive target differs from acceptance process');
  must(({ arm64: process.platform === 'linux' ? 'aarch64' : 'arm64', x64: 'x86_64' })[process.arch] === os.machine(),
    'Use a native machine; emulated process cannot establish platform acceptance');
  const expected = json(p.join(bundle, 'checksums.txt'));
  const unchanged = () => must(JSON.stringify(inventory(bundle).filter(f => f.path !== 'checksums.txt')) === JSON.stringify(expected),
    'Archive immutable contents changed');
  unchanged();
  checkBinaries(bundle, manifest.target, manifest.profile);
  report.target = manifest.target;
  report.profile = manifest.profile;
  report.manifest_sha256 = hash(fs.readFileSync(p.join(bundle, 'manifest.json')));
  report.dependencies = manifest.dependencies;
  report.source = manifest.source;
  save();
  await step('archive_smoke', async () => {
    const file = p.join(output, 'smoke.json');
    await child(process.execPath, [p.join(root, 'scripts/package-smoke.mjs'), archive, '--report', file], 'smoke');
    const smoke = json(file);
    checkSmoke(smoke, report.archive_sha256, manifest);
    return { status: 'passed', evidence: 'smoke.json', sha256: hash(fs.readFileSync(file)), fixture: smoke.fixture };
  });
  must(report.checks.archive_smoke.status === 'passed', 'Fix archive smoke before real acceptance');
  const system = p.join(output, 'system tools'), home = p.join(output, 'process home');
  fs.mkdirSync(system); fs.mkdirSync(home);
  for (const [name, source] of Object.entries({ sh: '/bin/sh', bash: '/bin/bash', dirname: '/usr/bin/dirname',
    basename: '/usr/bin/basename', uname: '/usr/bin/uname', sed: '/usr/bin/sed', sleep: '/bin/sleep',
    cat: '/bin/cat', ps: '/bin/ps' })) fs.symlinkSync(source, p.join(system, name));
  // Keep the real workspace-write sandbox. This OS tool is a declared macOS
  // prerequisite, not a host development runtime or a permission escalation.
  if (process.platform === 'darwin') {
    must(fs.existsSync('/usr/bin/sandbox-exec'), 'macOS DSH tools require /usr/bin/sandbox-exec');
    fs.symlinkSync('/usr/bin/sandbox-exec', p.join(system, 'sandbox-exec'));
  } else if (process.platform === 'linux' && fs.existsSync('/usr/bin/bwrap')) {
    // Linux selects bubblewrap when available, with bundled Landlock as its
    // fallback. Keep the declared OS sandbox tool on the restricted PATH.
    fs.symlinkSync('/usr/bin/bwrap', p.join(system, 'bwrap'));
    // DSH's functional bubblewrap probe invokes `true` through PATH.
    fs.symlinkSync('/usr/bin/true', p.join(system, 'true'));
  }
  const cleanPath = [p.join(bundle, 'bin'), p.join(bundle, 'runtime/node/bin'), system].join(p.delimiter);
  await step('real_dsh_acp', async () => {
    if (!options['--model-config']) return { status: 'not_run', reason: 'Explicit --model-config required' };
    const go = run('go', ['env', 'GOROOT']).trim();
    await child(p.join(go, 'bin/go'), ['test', '-tags=e2e', './internal/room', '-run', '^TestBundleRealDSH$', '-count=1', '-timeout=20m', '-v'], 'real-dsh', {
      ...process.env, TEAMX_BUNDLE_REAL: '1', TEAMX_BUNDLE_ROOT: bundle,
      TEAMX_BUNDLE_MODEL_CONFIG: options['--model-config'], TEAMX_BUNDLE_TEST_PATH: cleanPath,
      TEAMX_DSH_OUTPUT: p.join(output, 'real-dsh'), TEAMX_DSH_ENV_FILE: options['--env-file'] || ''
    }, 21 * 60000);
    const file = p.join(output, 'real-dsh/report.json');
    const real = json(file);
    checkRealDSH(real);
    return { status: 'passed', evidence: 'real-dsh/report.json', sha256: hash(fs.readFileSync(file)), fixture: false,
      checks: ['real model turns', 'CLI tools', 'running-tool cancellation', 'normal shutdown/process/lock cleanup', 'new prompt on original Session after service restart'] };
  });
  await step('browser', async () => {
    const browserOutput = p.join(output, 'browser');
    fs.mkdirSync(browserOutput, { mode: 0o700 });
    const browserReport = await acceptBrowser({ bundle, output: browserOutput, env: {
      HOME: home, XDG_CONFIG_HOME: p.join(home, '.config'), PATH: cleanPath,
      LANG: 'en_US.UTF-8', TMPDIR: output, OPENAI_API_KEY: 'browser-no-model-call'
    } });
    writeJSON(p.join(browserOutput, 'report.json'), browserReport);
    return { ...browserReport, evidence: 'browser/report.json', sha256: hash(fs.readFileSync(p.join(browserOutput, 'report.json'))) };
  });
  unchanged();
  must(hash(fs.readFileSync(archive)) === report.archive_sha256, 'Archive changed during acceptance');
  must(report.verifier_inputs.every(input => hash(fs.readFileSync(p.join(root, input.path))) === input.sha256),
    'Acceptance verifier changed during the run; repeat with stable inputs');
  report.status = resultForScope(report.checks);
} catch (error) {
  report.error = error.message;
} finally {
  save();
  console.log(`Acceptance ${report.status}; private evidence: ${p.join(output, 'report.json')}`);
  process.exitCode = report.status === 'passed' ? 0 : 2;
}
