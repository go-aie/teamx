// Disposable Linux prerequisite probe, with no credentials or production roots.
// A successful prerequisite probe is deliberately NOT an isolation acceptance.
import { createHash } from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';

const sha = data => createHash('sha256').update(data).digest('hex');
const fileHash = name => sha(fs.readFileSync(name));
const json = name => JSON.parse(fs.readFileSync(name, 'utf8'));
const cfg = process.argv[2] ? json(process.argv[2]) : {
  lab: '/lab', srt: '/opt/srt', dsh: '/opt/dsh',
  bwrap: '/usr/bin/bwrap', socat: '/usr/bin/socat', rg: '/usr/bin/rg',
};
const srtRoot = `${cfg.srt}/node_modules/@anthropic-ai/sandbox-runtime`;
const dshRoot = `${cfg.dsh}/node_modules/@deepseek-ai/dsh`;
const lab = cfg.lab;
const own = part => `${lab}/a/${part}`;
const write = (name, value) => fs.writeFileSync(path.join(lab, name), JSON.stringify(value, null, 2) + '\n', { mode: 0o600 });
const env = {
  PATH: `${path.dirname(process.execPath)}:${path.dirname(cfg.bwrap)}:/usr/bin:/bin`,
  HOME: own('home'), TMPDIR: own('tmp'),
  LANG: 'C.UTF-8', LC_ALL: 'C.UTF-8',
  DSH_PERMISSION_MODE: 'workspace-write',
};
function run(binary, args, input = '') {
  const result = spawnSync(binary, args, {
    cwd: own('workspace'), env, input, encoding: 'utf8',
    timeout: 20000, killSignal: 'SIGKILL', maxBuffer: 1024 * 1024,
  });
  return { exit_code: result.status, signal: result.signal,
    error: result.error?.code ?? null, stdout: result.stdout ?? '', stderr: result.stderr ?? '' };
}
const report = {
  status: 'failed', stage: 'host_prerequisites', production_changes: false,
  real_model: false, real_dsh_started: false, srt_route_accepted: false,
  acceptance: Object.fromEntries(['dsh_file_shell_child_links', 'authenticated_mcp_cli',
    'model_platform_network', 'dns_redirect_ipv6_unix_bypass', 'nested_dsh_sandbox',
    'same_session_resume', 'cancel_descendants_proxies', 'faults_and_parallel_b',
    'real_model_round'].map(name => [name, 'not_run'])),
};
try {
  if (os.platform() !== 'linux' || process.getuid() === 0 || (!process.argv[2] && !fs.existsSync('/.dockerenv'))) {
    throw new Error('requires non-root disposable Linux experiment');
  }
  if (fs.readdirSync(lab).length !== 0) throw new Error('requires empty /lab; refuse overwriting evidence');
  for (const area of ['a', 'b', 'c', 'control', 'service', 'host-home']) {
    fs.mkdirSync(`${lab}/${area}/private`, { recursive: true, mode: 0o700 });
    fs.writeFileSync(`${lab}/${area}/private/secret`, `SYNTHETIC_${area.toUpperCase()}`, { mode: 0o600 });
  }
  report.synthetic_domains = { a: 'user-1/space-a', b: 'user-1/space-b', c: 'user-2/space-c',
    control: 'core_private', service: 'own_space_service_credentials', 'host-home': 'host_credentials' };
  report.unsandboxed_same_uid_read_control = Object.keys(report.synthetic_domains)
    .every(area => fs.readFileSync(`${lab}/${area}/private/secret`, 'utf8').startsWith('SYNTHETIC_'));
  for (const part of ['workspace', 'state', 'home', 'tmp']) {
    fs.mkdirSync(own(part), { mode: 0o700 });
  }
  fs.symlinkSync(`${lab}/b/private`, own('workspace/escape'));
  const policy = {
    network: { allowedDomains: [], deniedDomains: [], strictAllowlist: true,
      allowAllUnixSockets: false, allowLocalBinding: false },
    filesystem: { denyRead: ['/'],
      allowRead: ['/usr', '/bin', '/sbin', '/lib', '/lib64', '/dev', '/proc',
        '/etc/ssl', '/etc/alternatives', '/etc/ld.so.cache', '/etc/hosts', '/etc/resolv.conf',
        cfg.srt, cfg.dsh, path.dirname(path.dirname(process.execPath)), path.dirname(path.dirname(cfg.bwrap)),
        own('workspace'), own('state'), own('home'), own('tmp')],
      allowWrite: [own('workspace'), own('state'), own('home'), own('tmp')], denyWrite: [] },
    bwrapPath: cfg.bwrap, socatPath: cfg.socat, ripgrep: { command: cfg.rg },
    enableWeakerNestedSandbox: false, enableWeakerNetworkIsolation: false,
    allowNestedUserNamespaces: false,
  };
  write('policy.json', policy);
  report.policy_sha256 = fileHash(`${lab}/policy.json`);
  report.policy_enforcement = 'not_verified';
  report.environment_names = Object.keys(env).sort();
  report.host = { kernel: os.release(), architecture: os.arch(), uid: process.getuid(),
    groups: process.getgroups(), status: fs.readFileSync('/proc/self/status', 'utf8')
      .split('\n').filter(line => /^(CapEff|CapBnd|NoNewPrivs|Seccomp):/.test(line)) };
  report.runtime = { node: process.versions.node,
    srt: json(`${srtRoot}/package.json`).version, dsh: json(`${dshRoot}/package.json`).version };
  if (report.runtime.node !== '24.22.2' || report.runtime.srt !== '0.0.83' || report.runtime.dsh !== '0.2.2-rc.1') {
    throw new Error('runtime differs from locked experiment');
  }
  report.versions = {
    bwrap: run(cfg.bwrap, ['--version']),
    socat: run(cfg.socat, ['-V']),
    srt: run(process.execPath, [`${srtRoot}/dist/cli.js`, '--version']),
    dsh: run(process.execPath, [`${dshRoot}/lib/bin.js`, '--version']),
  };
  const packages = run('/usr/bin/dpkg-query', ['-W', '-f=${Package}\t${Version}\t${Architecture}\n']);
  if (packages.exit_code !== 0) throw new Error('cannot inventory Linux dependency versions');
  fs.writeFileSync(`${lab}/linux-packages.tsv`, packages.stdout, { mode: 0o600 });
  report.linux_packages_sha256 = fileHash(`${lab}/linux-packages.tsv`);
  report.files_sha256 = {};
  for (const file of [process.execPath, cfg.bwrap, cfg.socat,
    `${cfg.srt}/package-lock.json`, `${cfg.dsh}/package-lock.json`,
    `${srtRoot}/dist/cli.js`, `${srtRoot}/dist/sandbox/linux-sandbox-utils.js`,
    `${dshRoot}/lib/bin.js`]) report.files_sha256[file] = fileHash(file);
  const seccomp = `${srtRoot}/vendor/seccomp/${os.arch() === 'arm64' ? 'arm64' : 'x64'}/apply-seccomp`;
  if (fs.existsSync(seccomp)) report.files_sha256[seccomp] = fileHash(seccomp);
  const { SandboxManager } = await import(`${srtRoot}/dist/index.js`);
  for (const key of Object.keys(process.env)) delete process.env[key];
  Object.assign(process.env, env);
  report.srt_dependency_check = SandboxManager.checkDependencies();
  // The upstream existence check is not proof that the host permits namespaces.
  report.namespace = run(cfg.bwrap, ['--unshare-user', '--unshare-pid', '--unshare-net',
    '--ro-bind', '/', '/', '--proc', '/proc', '--dev', '/dev', '/bin/true']);
  const deps = report.srt_dependency_check;
  if (deps.errors?.length || deps.warnings?.length) {
    report.status = 'prerequisite_failed';
    report.failure_class = 'required_dependency_error_or_warning';
    report.srt_execution = report.srt_dsh_acp = { status: 'not_run', reason: report.failure_class };
    write('report.json', report);
    process.exitCode = 2;
  } else {
    const prefix = [`${srtRoot}/dist/cli.js`, '--settings', `${lab}/policy.json`, '--'];
    const marker = own('workspace/started');
    report.srt_execution = run(process.execPath, [...prefix, process.execPath, '-e',
      `require('node:fs').writeFileSync(${JSON.stringify(marker)}, 'started')`]);
    report.payload_started = fs.existsSync(marker);
    // Exercise the exact full-DSH launch shape even when the primitive refuses.
    // No model endpoint or key is configured, so this cannot make a model call.
    report.srt_dsh_acp = run(process.execPath, [...prefix, process.execPath,
      `${dshRoot}/lib/bin.js`, '--profile', 'acp'],
      JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'initialize',
        params: { protocolVersion: 1, clientCapabilities: {} } }) + '\n');
    report.real_dsh_started = report.srt_dsh_acp.stdout.split('\n').some(line => {
      try { return Boolean(JSON.parse(line).result?.agentInfo); } catch { return false; }
    });
    const reason = report.namespace.exit_code !== 0 ? 'linux_namespaces_unavailable'
      : report.srt_execution.exit_code !== 0 || !report.payload_started ? 'srt_policy_or_launcher_failed'
      : null;
    report.status = reason ? 'prerequisite_failed' : 'prerequisites_only';
    report.failure_class = reason;
    report.acceptance_required = true;
    write('report.json', report);
    process.exitCode = reason ? 2 : 3; // Never return success for an unrun acceptance.
  }
} catch (error) {
  report.failure_class = 'experiment_error';
  report.error = error.message;
  write('report.json', report);
  process.exitCode = 1;
}
