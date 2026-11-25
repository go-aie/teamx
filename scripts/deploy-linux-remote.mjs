import fs from 'node:fs';
import os from 'node:os';
import p from 'node:path';
import { createHash } from 'node:crypto';
import { execFileSync, spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { progress } from './package-progress.mjs';

const check = (ok, message) => { if (!ok) throw Error(message); };
const digest = data => createHash('sha256').update(data).digest('hex');
const output = (cmd, args, options = {}) => execFileSync(cmd, args, { encoding: 'utf8', maxBuffer: 64 * 1024 * 1024, ...options }).trim();
const json = file => JSON.parse(fs.readFileSync(file, 'utf8'));
const verifierFiles = {
  smoke_sha256: 'scripts/package-smoke.mjs', srt_check_sha256: 'scripts/package-srt.mjs',
  native_check_sha256: 'scripts/check-dsh-native.mjs', fixture_sha256: 'scripts/fixtures/acp.mjs'
};
const run = (cmd, args, options = {}) => new Promise((resolve, reject) => {
  const { logFile, ...spawnOptions } = options;
  const log = logFile ? fs.createWriteStream(logFile, { mode: 0o600 }) : undefined;
  const child = spawn(cmd, args, { stdio: log ? ['inherit', 'pipe', 'pipe'] : 'inherit', ...spawnOptions });
  log?.once('error', error => { child.kill(); reject(error); });
  if (log) {
    child.stdout.pipe(process.stdout, { end: false });child.stderr.pipe(process.stderr, { end: false });
    child.stdout.pipe(log, { end: false });child.stderr.pipe(log, { end: false });
  }
  child.once('error', error => { log?.end();reject(error); });
  child.once('close', code => {
    const finish = () => code === 0 ? resolve() : reject(Error(`${cmd} exited ${code}`));
    if (log) log.end(finish); else finish();
  });
});
async function sha256(file) {
  const hash = createHash('sha256');
  for await (const block of fs.createReadStream(file)) hash.update(block);
  return hash.digest('hex');
}

export async function deployRemote(request, runtime = { platform: process.platform, arch: process.arch, machine: os.machine(), uid: process.getuid?.() }) {
  check(runtime.platform === 'linux' && runtime.arch === 'x64' && runtime.machine === 'x86_64', 'Native Linux amd64 is required');
  const { config: c, commit, job } = request;
  check(/^[a-f0-9]{40}$/.test(commit), 'Invalid source commit');
  check(p.isAbsolute(job) && fs.realpathSync(job) === job, 'Job must be a physical absolute directory');
  const timing = progress('deploy'), started = performance.now();
  const receipt = { source_commit: commit, server_root: c.server_root, status: 'preparing', build_only: !!request.build_only, timings: timing.records };
  const save = () => fs.writeFileSync(p.join(job, 'deployment.json'), JSON.stringify(receipt, null, 2) + '\n', { mode: 0o600 });
  const source = p.join(job, 'source'), dist = p.join(job, 'dist');
  try {
    timing.step('checkout committed source', () => {
      fs.mkdirSync(source);
      output('git', ['init', '-q', source]);
      output('git', ['-C', source, 'fetch', '--quiet', p.join(job, 'source.bundle'), commit]);
      output('git', ['-C', source, 'checkout', '--quiet', '--detach', 'FETCH_HEAD']);
      check(output('git', ['-C', source, 'rev-parse', 'HEAD']) === commit, 'Remote source differs from requested HEAD');
    });
    const env = { ...process.env, PATH: [...c.build_path, process.env.PATH].join(':'), GOMODCACHE: c.go_mod_cache, GOCACHE: c.go_build_cache, GOTOOLCHAIN: 'local' };
    const identity = {
      version: 1, commit, target: 'linux-amd64', profile: 'standard',
      runner: digest(fs.readFileSync(fileURLToPath(import.meta.url))),
      progress: digest(fs.readFileSync(fileURLToPath(new URL('./package-progress.mjs', import.meta.url)))),
      environment: { platform: runtime.platform, arch: runtime.arch, machine: runtime.machine, os_release: os.release(), libc: process.report?.getReport().header.glibcVersionRuntime || null }
    };
    const key = digest(JSON.stringify(identity));
    const artifacts = p.join(c.work_dir, 'artifacts'), cached = p.join(artifacts, key);
    const name = `teamx-dev-${commit.slice(0, 12)}-linux-amd64-standard`, archiveName = name + '.tar.gz';
    const archive = p.join(dist, archiveName), reportName = name + '-report.json';
    const files = [archiveName, archiveName + '.sha256', reportName];
    const reused = fs.existsSync(cached);
    receipt.artifact_cache = { key, path: cached, reused };
    let cacheRecord;
    if (reused) {
      await timing.run('reuse verified archive', async () => {
        cacheRecord = json(p.join(cached, 'record.json'));
        check(JSON.stringify(cacheRecord.identity) === JSON.stringify(identity), 'Cached artifact identity mismatch');
        fs.mkdirSync(dist);
        for (const file of files) fs.copyFileSync(p.join(cached, file), p.join(dist, file), fs.constants.COPYFILE_FICLONE);
        console.log(`Reusing archive for ${commit}; original full smoke is retained, not rerun`);
      });
    } else {
      for (const dir of [c.cache_dir, c.go_mod_cache, c.go_build_cache]) fs.mkdirSync(dir, { recursive: true });
      const args = ['scripts/package.mjs', '--channel', 'dev', '--target', 'linux-amd64', '--profile', 'standard', '--cache-dir', c.cache_dir, '--output-dir', dist];
      if (request.offline) args.push('--offline');
      console.log(`Building ${commit} in ${source}`);
      await timing.run('build and full smoke', () => run(process.execPath, args, { cwd: source, env, logFile: p.join(job, 'build.log') }));
    }
    const archiveDigest = await timing.run('verify archive, report and source', async () => {
      check(fs.readdirSync(dist).filter(x => x.endsWith('.tar.gz')).length === 1, 'Expected one successful standard archive');
      const value = await sha256(archive);
      check(fs.readFileSync(archive + '.sha256', 'utf8') === `${value}  ${archiveName}\n`, 'Archive checksum mismatch');
      const report = json(p.join(dist, reportName));
      check(report.status === 'passed' && report.archive_sha256 === value && report.target === 'linux-amd64' && report.profile === 'standard', 'Build report mismatch');
      for (const [field, file] of Object.entries(verifierFiles)) {
        check(report[field] === digest(fs.readFileSync(p.join(source, file))), `Smoke verifier mismatch: ${field}`);
      }
      if (cacheRecord) check(cacheRecord.archive_sha256 === value && cacheRecord.report_sha256 === digest(fs.readFileSync(p.join(dist, reportName))), 'Cached artifact checksum mismatch');
      const manifest = JSON.parse(output('tar', ['-xOf', archive, name + '/manifest.json']));
      check(manifest.source?.commit === commit && manifest.source?.dirty === false && manifest.channel === 'dev' && manifest.target === 'linux-amd64' && manifest.profile === 'standard', 'Packaged source identity mismatch');
      return value;
    });
    receipt.archive = archive;
    receipt.archive_sha256 = archiveDigest;
    if (!reused) timing.step('publish verified artifact cache', () => {
      fs.mkdirSync(artifacts, { recursive: true, mode: 0o700 });
      const stage = fs.mkdtempSync(p.join(artifacts, '.pending-'));
      try {
        for (const file of files) fs.copyFileSync(p.join(dist, file), p.join(stage, file), fs.constants.COPYFILE_FICLONE);
        fs.writeFileSync(p.join(stage, 'record.json'), JSON.stringify({ identity, archive_sha256: archiveDigest, report_sha256: digest(fs.readFileSync(p.join(dist, reportName))) }) + '\n', { mode: 0o600 });
        try { fs.renameSync(stage, cached); }
        catch (error) {
          if (!['EEXIST', 'ENOTEMPTY'].includes(error.code)) throw error;
          check(JSON.stringify(json(p.join(cached, 'record.json')).identity) === JSON.stringify(identity), 'Concurrent cache identity mismatch');
        }
      } finally { fs.rmSync(stage, { recursive: true, force: true }); }
    });
    receipt.status = 'built'; save();
    if (request.build_only) {
      console.log(`Build only; installed service untouched. Archive: ${archive}`);
      return receipt;
    }
    const installer = p.join(job, 'install.sh');
    timing.step('extract verified installer', () => fs.writeFileSync(installer, output('tar', ['-xOf', archive, name + '/install.sh']) + '\n', { mode: 0o700 }));
    const upgrade = fs.existsSync(p.join(c.server_root, 'service.json')), privileged = runtime.uid !== 0;
    const previousRelease = upgrade ? fs.realpathSync(p.join(c.server_root, 'current')) : undefined;
    const installArgs = [installer, '--archive', archive, '--sha256', archiveDigest, '--server-root', c.server_root, '--bin-dir', c.bin_dir];
    if (upgrade) {
      if (privileged) await run('sudo', ['-n', 'install', '-d', '-m', '700', c.backup_root], { env });
      else fs.mkdirSync(c.backup_root, { recursive: true, mode: 0o700 });
      receipt.backup = p.join(c.backup_root, 'before-' + p.basename(job));
      installArgs.push('--upgrade', '--backup-dir', receipt.backup);
    } else {
      installArgs.push('--home', c.home, '--user', c.service_user, '--execution', c.execution);
      if (c.deployment_config) installArgs.push('--deployment-config', c.deployment_config);
      if (c.execution === 'srt') for (const endpoint of c.srt_endpoints) installArgs.push('--srt-endpoint', endpoint);
    }
    receipt.action = upgrade ? 'upgrade' : 'install'; receipt.status = 'deploying'; save();
    console.log(`${receipt.action}: ${c.server_root}${upgrade ? `; backup: ${receipt.backup}` : ''}`);
    await timing.run('installer (validation, backup and service switch)', () => run(privileged ? 'sudo' : 'bash', privileged ? ['-n', 'bash', ...installArgs] : installArgs, { env, logFile: p.join(job, 'installer.log') }));
    timing.step('installed version and authenticated status', () => {
      const cli = p.join(c.bin_dir, 'teamx');
      const version = output(privileged ? 'sudo' : cli, privileged ? ['-n', cli, '--version'] : ['--version'], { env });
      check(version.includes(`commit=${commit}`) && version.includes('dirty=false'), 'Installed command does not match requested commit');
      const status = jsonOutput(privileged ? 'sudo' : cli, privileged ? ['-n', cli, 'service', 'status', '--server-root', c.server_root] : ['service', 'status', '--server-root', c.server_root], env);
      check(status.authenticated === true, 'Deployment did not authenticate to the installed service');
      check(status.ready === true, 'Installed service is not ready; retention cleanup was skipped');
      receipt.service = status;
    });
    // Reapplying the already selected archive is a successful no-op: the
    // installer creates no backup, so do not enroll its reserved backup path.
    if (upgrade && fs.realpathSync(p.join(c.server_root, 'current')) === previousRelease) delete receipt.backup;
    receipt.status = 'deployed';
    save();
    // Cleanup is a separate post-success operation. Installation/identity
    // failures never enroll backups or delete recovery materials.
    try {
      await timing.run('retain successful releases and backups', async () => {
        const cli = p.join(c.bin_dir, 'teamx');
        const args = ['service', 'prune', '--server-root', c.server_root, '--keep-releases', String(c.keep_releases ?? 2), '--keep-backups', String(c.keep_backups ?? 2)];
        if (receipt.backup) args.push('--from', receipt.backup);
        // A full backup verification may take time; run asynchronously so the
        // progress heartbeat remains visible, and keep stdout as JSON.
        const result = p.join(job, 'retention.json');
        await run(privileged ? 'sudo' : cli, privileged ? ['-n', cli, ...args] : args, { env, logFile: result });
        receipt.retention = json(result);
      });
    } catch (error) {
      receipt.retention = { error: error.message };
      console.error(`Deployment succeeded; retention cleanup needs attention: ${error.message}. See ${job}/retention.json; service remains on the deployed version.`);
    }
    return receipt;
  } catch (error) { receipt.status = 'failed'; receipt.error = error.message; throw error; }
  finally {
    receipt.duration_ms = Math.round(performance.now() - started);
    save();
    console.log(`[deploy] total: ${(receipt.duration_ms / 1000).toFixed(2)}s; status: ${receipt.status}; receipt: ${job}/deployment.json`);
  }
}
function jsonOutput(cmd, args, env) { return JSON.parse(output(cmd, args, { env })); }
if (process.argv[1] && p.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  deployRemote(json(process.argv[2])).catch(error => {
    console.error(`Remote deploy: ${error.message}\nKeep the job/backup and inspect service status/logs; no automatic rollback.`);
    process.exitCode = 1;
  });
}
