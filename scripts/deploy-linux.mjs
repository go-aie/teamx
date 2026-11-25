import fs from 'node:fs';
import os from 'node:os';
import p from 'node:path';
import { randomBytes } from 'node:crypto';
import { execFileSync, spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { progress } from './package-progress.mjs';

const root = p.resolve(p.dirname(fileURLToPath(import.meta.url)), '..');
const check = (ok, message) => { if (!ok) throw Error(message); };
const git = (repo, ...args) => execFileSync('git', ['-C', repo, ...args], { encoding: 'utf8' }).trim();
export const shellQuote = value => `'${value.replaceAll("'", "'\\''")}'`;

export function deploymentConfig(input) {
  const defaults = { work_dir: '/var/tmp/teamx-deploy', server_root: '/opt/teamx', home: '/var/lib/teamx', bin_dir: '/usr/local/bin', service_user: 'teamx', node_binary: 'node', execution: 'native', srt_endpoints: [], build_path: [], keep_releases: 2, keep_backups: 2 };
  const keys = new Set([...Object.keys(defaults), 'cache_dir', 'go_mod_cache', 'go_build_cache', 'backup_root', 'deployment_config']);
  for (const key of ['host', 'port']) check(!Object.hasOwn(input, key), `Specify SSH ${key} with --${key}, not in deployment JSON`);
  for (const key of Object.keys(input)) check(keys.has(key), `Unknown deployment setting: ${key}`);
  const c = { ...defaults, ...input };
  c.cache_dir ??= p.posix.join(c.work_dir, 'cache');
  c.go_mod_cache ??= p.posix.join(c.work_dir, 'go-mod');
  c.go_build_cache ??= p.posix.join(c.work_dir, 'go-cache');
  c.backup_root ??= p.posix.join(p.posix.dirname(c.home), 'teamx-backups');
  const absolute = value => typeof value === 'string' && p.posix.isAbsolute(value) && p.posix.normalize(value) === value && value !== '/' && !/[\r\n\0]/.test(value);
  for (const key of ['work_dir', 'server_root', 'home', 'bin_dir', 'cache_dir', 'go_mod_cache', 'go_build_cache', 'backup_root']) check(absolute(c[key]), `Expected a canonical absolute ${key}`);
  if (c.deployment_config !== undefined) check(absolute(c.deployment_config), 'Expected an absolute remote deployment_config');
  check(c.node_binary === 'node' || absolute(c.node_binary), 'Invalid node_binary');
  check(Array.isArray(c.build_path) && c.build_path.every(x => absolute(x) && !x.includes(':')), 'Invalid build_path');
  check(/^[a-z_][a-z0-9_-]*$/.test(c.service_user), 'Invalid service_user');
  check(['native', 'srt'].includes(c.execution), 'execution must be native or srt');
  check(Number.isSafeInteger(c.keep_releases) && c.keep_releases >= 2, 'keep_releases must be an integer >= 2');
  check(Number.isSafeInteger(c.keep_backups) && c.keep_backups >= 1, 'keep_backups must be an integer >= 1');
  check(Array.isArray(c.srt_endpoints) && c.srt_endpoints.every(x => {
    try { const u = new URL(x); return ['http:', 'https:'].includes(u.protocol) && !u.username && !u.password && !u.search && !u.hash && (u.pathname === '/' || u.pathname === '') && !/[\r\n\0*]/.test(x); } catch { return false; }
  }), 'srt_endpoints must be exact origins');
  const within = (a, b) => b === a || b.startsWith(a + '/');
  for (const [a, b] of [[c.server_root,c.home], ...['work_dir','cache_dir','go_mod_cache','go_build_cache','backup_root'].flatMap(key => [[c[key],c.server_root],[c[key],c.home]])]) {
    check(!within(a,b) && !within(b,a), 'Build/cache/backup directories must not overlap the installed program or private home');
  }
  return c;
}

export function sourceCommit(repo) {
  const commit = git(repo, 'rev-parse', '--verify', 'HEAD^{commit}');
  check(/^[0-9a-f]{40}$/.test(commit), 'Expected a committed SHA-1 HEAD');
  const entries = execFileSync('git', ['-C', repo, 'ls-tree', '-rz', commit], { encoding: 'utf8' }).split('\0').filter(Boolean);
  for (const entry of entries) {
    const [header, name] = entry.split('\t');
    check(!header.startsWith('160000 '), 'Git submodules are not supported by this deployment script');
    check(!/^\.env(?:\.|$)/.test(name) && !/^(?:\.local|\.packaging|dist|bin)(?:\/|$)/.test(name) &&
      (!name.startsWith('.teamx/') || name === '.teamx/config.example.toml' || name.startsWith('.teamx/teams/')), `Private/generated path is committed: ${name}`);
  }
  return commit;
}

export function makeSourceBundle(repo, file, commit) {
  execFileSync('git', ['-C', repo, 'bundle', 'create', file, 'HEAD'], { stdio: 'inherit' });
  check(git(repo,'bundle','list-heads',file).split(/\s/)[0]===commit,'HEAD changed while creating source bundle; retry');
}

function child(command, args, options = {}) {
  const subprocess = spawn(command, args, options);
  const done = new Promise((resolve, reject) => {
    subprocess.once('error', reject);
    subprocess.once('close', code => code === 0 ? resolve() : reject(Error(`${command} exited ${code}`)));
  });
  return { process:subprocess, done };
}

export async function deployLocal(repo, config, { host, port, dryRun = false, buildOnly = false, offline = false } = {}) {
  check(typeof host === 'string' && /^(?:[a-zA-Z0-9_][a-zA-Z0-9_.-]*@)?(?:[a-zA-Z0-9][a-zA-Z0-9_.-]*|\[[0-9a-fA-F:]+\])$/.test(host), 'Specify --host [user@]host (an SSH config alias is also accepted)');
  if (port !== undefined) check(Number.isInteger(port) && port > 0 && port < 65536, 'Invalid SSH --port; expected 1..65535');
  const c = deploymentConfig(config), commit = sourceCommit(repo);
  const id = `${commit.slice(0,12)}-${new Date().toISOString().replace(/\D/g,'')}-${randomBytes(3).toString('hex')}`;
  const job = p.posix.join(c.work_dir, 'jobs', id);
  console.log(`Source: ${commit} (HEAD only; uncommitted/untracked changes are excluded)`);
  console.log(`Target: ${host}; job: ${job}; action: ${buildOnly ? 'build only' : 'install or upgrade'}`);
  if (dryRun) return { commit, job, build_only: buildOnly, dry_run: true };
  const timing = progress('local'), started = performance.now();
  const temp = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-deploy-'));
  fs.chmodSync(temp, 0o700);
  const sshArgs = ['-o', 'BatchMode=yes', '-o', 'ConnectTimeout=15', '-o', 'ServerAliveInterval=30', '-o', 'ServerAliveCountMax=6', '-o', 'ControlMaster=no', '-o', 'ControlPath=none', '-o', 'ClearAllForwardings=yes'];
  if (port) sshArgs.push('-p', String(port));
  try {
    timing.step('create committed Git bundle', () => makeSourceBundle(repo, p.join(temp, 'source.bundle'), commit));
    fs.copyFileSync(p.join(root, 'scripts/deploy-linux-remote.mjs'), p.join(temp, 'runner.mjs'));
    fs.copyFileSync(p.join(root, 'scripts/package-progress.mjs'), p.join(temp, 'package-progress.mjs'));
    fs.writeFileSync(p.join(temp, 'request.json'), JSON.stringify({ config:c, commit, job, build_only:buildOnly, offline }), { mode:0o600 });
    await timing.run('SSH upload', async () => {
      const upload = child('ssh', [...sshArgs,host,`umask 077; mkdir -p -- ${shellQuote(job)} && tar -xf - -C ${shellQuote(job)}`], { stdio:['pipe','inherit','inherit'] });
      const archive = child('tar', ['-cf','-','-C',temp,'source.bundle','runner.mjs','package-progress.mjs','request.json'], { stdio:['ignore','pipe','inherit'], env:{...process.env,COPYFILE_DISABLE:'1'} });
      archive.process.stdout.pipe(upload.process.stdin);
      upload.done.catch(()=>archive.process.kill());
      archive.done.catch(()=>upload.process.kill());
      upload.process.stdin.on('error', error => { if (error.code !== 'EPIPE') upload.process.kill(); });
      const uploaded = await Promise.allSettled([archive.done,upload.done]);
      for (const result of uploaded) if (result.status === 'rejected') throw result.reason;
    });
    await timing.run('remote build or reuse and deployment', () => child('ssh', [...sshArgs,host,`${shellQuote(c.node_binary)} ${shellQuote(job+'/runner.mjs')} ${shellQuote(job+'/request.json')}`], { stdio:'inherit' }).done);
    console.log(`Completed. Remote artifacts and receipt: ${job}`);
    return { commit, job, build_only:buildOnly };
  } finally {
    fs.rmSync(temp, {recursive:true,force:true});
    console.log(`[local] total: ${((performance.now() - started) / 1000).toFixed(2)}s`);
  }
}

async function main() {
  let file, host, port, dryRun=false, buildOnly=false, offline=false;
  const args=process.argv.slice(2);
  while (args.length) {
    const arg=args.shift();
    if (arg==='--config') { file=args.shift(); check(file,'--config requires a JSON file'); }
    else if (arg==='--host') { host=args.shift(); check(host,'--host requires [user@]host'); }
    else if (arg==='--port') { const value=args.shift(); check(value && /^\d+$/.test(value),'--port requires a numeric SSH port'); port=Number(value); }
    else if (arg==='--dry-run') dryRun=true;
    else if (arg==='--build-only') buildOnly=true;
    else if (arg==='--offline') offline=true;
    else if (arg==='--help' || arg==='-h') { console.log('Usage: scripts/deploy-linux.sh --host [user@]host --config FILE.json [--port PORT] [--dry-run] [--build-only] [--offline]\nDeploy committed HEAD via SSH; build Linux amd64 standard remotely and use its installer.\nSSH host and port are command arguments only; keep environment settings in an ignored local JSON file.'); return; }
    else throw Error(`Unknown argument: ${arg}`);
  }
  check(file,'Specify --config FILE.json; see packaging/deploy-linux.example.json');
  await deployLocal(root,JSON.parse(fs.readFileSync(file,'utf8')),{host,port,dryRun,buildOnly,offline});
}
if (process.argv[1] && p.resolve(process.argv[1])===fileURLToPath(import.meta.url)) main().catch(error=>{console.error(`Deploy: ${error.message}`);process.exitCode=1;});
