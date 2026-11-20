import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as p from 'node:path';
import { createHash } from 'node:crypto';
import { spawnSync, execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const source = fs.readFileSync(fileURLToPath(new URL('../packaging/install.sh', import.meta.url)), 'utf8');
const sha256 = file => createHash('sha256').update(fs.readFileSync(file)).digest('hex');
const quote = value => `'${value.replaceAll("'", "'\\''")}'`;

// Only downloads, host identity and the system manager are simulated. Real bash,
// tar, hashes, files, symlinks and shell startup code exercise the installer.
function fixture(t, platform = 'Darwin', architecture = 'arm64') {
  const temp = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-install-test-'));
  t.after(() => fs.rmSync(temp, { recursive: true, force: true }));
  const home = p.join(temp, 'user home'), mocks = p.join(temp, 'mocks'), assets = p.join(temp, 'assets');
  for (const dir of [home, mocks, assets]) fs.mkdirSync(dir);
  const record = p.join(temp, 'record.jsonl'), program = p.join(temp, 'bootstrap.mjs');
  const root = p.join(temp, 'custom server'), data = p.join(temp, 'private data'), bin = p.join(temp, 'command bin');
  const shell = (name, body) => fs.writeFileSync(p.join(mocks, name), '#!/bin/sh\nset -eu\n' + body, { mode: 0o755 });
  shell('uname', `case "$1" in -s) echo ${platform} ;; -m) echo ${architecture} ;; *) exit 1 ;; esac\n`);
  shell('id', 'if [ "$1" = -u ]; then echo "${INSTALL_TEST_UID:-501}"; else test -f "$INSTALL_TEST_ACCOUNT"; fi\n');
  shell('systemctl', 'exit 0\n');
  shell('useradd', 'printf "useradd\\n" >> "$INSTALL_TEST_TOOLS"\ntouch "$INSTALL_TEST_ACCOUNT"\n');
  shell('sudo', 'printf "sudo %s\\n" "$*" >> "$INSTALL_TEST_TOOLS"\nif [ "$1" != -v ]; then exec "$@"; fi\n');
  shell('curl', `url='' output=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    https://*) url=$1; shift ;;
    -o) output=$2; shift 2 ;;
    --proto|--proto-redir|--tlsv1.2|--retry|--connect-timeout) shift 2 ;;
    *) shift ;;
  esac
done
printf '%s\\n' "$url" >> "$INSTALL_TEST_DOWNLOADS"
if [ "\${INSTALL_TEST_DOWNLOAD_FAIL:-}" = true ]; then printf partial > "$output"; exit 22; fi
case "$url" in https://github.com/go-aie/teamx/releases/download/v1.2.3/*) ;; *) exit 9 ;; esac
if [ -n "$output" ]; then cp "$INSTALL_TEST_ASSETS/\${url##*/}" "$output"; else cat "$INSTALL_TEST_ASSETS/\${url##*/}"; fi
`);
  fs.writeFileSync(program, `import fs from 'node:fs'; import p from 'node:path';
const args = process.argv.slice(2), env = process.env;
if (args[0] === '--version') { console.log('teamx v1.0.0 commit=fixture'); process.exit(0); }
if (args[0] === 'service' && args[1] === 'inspect') {
  if (env.INSTALL_TEST_BAD_REGISTRATION === 'true') process.exit(13);
  JSON.parse(fs.readFileSync(p.join(args[args.indexOf('--server-root') + 1], 'service.json'), 'utf8'));
  console.log('{}'); process.exit(0);
}
if (args[0] === 'service' && args[1] === 'upgrade' && args[2] === '--help') {
  console.log(env.INSTALL_TEST_LEGACY === 'true' ? '--archive FILE --backup-dir DIR' : '--version vX.Y.Z');
  process.exit(0);
}
fs.appendFileSync(env.INSTALL_TEST_RECORD, JSON.stringify(args) + '\\n');
if (process.umask() !== 0o022) process.exit(11);
if (env.INSTALL_TEST_DEPLOY_FAIL === 'true') { console.error('fixture deployment failed'); process.exit(7); }
if (args[0] !== 'service' || !['install', 'upgrade'].includes(args[1])) process.exit(8);
const get = name => args[args.indexOf(name) + 1];
if (!fs.existsSync(get('--archive'))) process.exit(9);
const root = get('--server-root'), bin = p.join(root, 'releases', 'fixture', 'bin');
fs.mkdirSync(bin, {recursive:true});
fs.copyFileSync(process.argv[1].replace('bootstrap.mjs', 'teamx-fixture'), p.join(bin, 'teamx'));
try { fs.symlinkSync('releases/fixture', p.join(root, 'current')); } catch (e) { if (e.code !== 'EEXIST') throw e; }
fs.writeFileSync(p.join(root, 'service.json'), '{}');
console.error(args[1] === 'upgrade' ? 'TeamX 已完成升级并启动。' : 'TeamX 已安装并启动。\\n1. 打开：http://127.0.0.1:8088');
`);
  const executable = p.join(temp, 'teamx-fixture');
  fs.writeFileSync(executable, `#!/bin/sh\nexec ${quote(process.execPath)} "$INSTALL_TEST_PROGRAM" "$@"\n`, { mode: 0o755 });
  const target = `${platform === 'Darwin' ? 'darwin' : 'linux'}-${architecture === 'x86_64' ? 'amd64' : 'arm64'}`;
  const name = `teamx-v1.2.3-${target}-standard`, bundle = p.join(temp, name);
  fs.mkdirSync(p.join(bundle, 'bin'), { recursive: true });
  fs.copyFileSync(executable, p.join(bundle, 'bin/teamx'));
  const archive = p.join(assets, name + '.tar.gz');
  execFileSync('tar', ['-czf', archive, '-C', temp, name], { env: { ...process.env, COPYFILE_DISABLE: '1' } });
  fs.writeFileSync(archive + '.sha256', `${sha256(archive)}  ${p.basename(archive)}\n`);
  fs.writeFileSync(p.join(assets, 'SHA256SUMS'), fs.readFileSync(archive + '.sha256'));
  fs.writeFileSync(p.join(assets, 'install.sh'), source.replace('@TEAMX_VERSION@', 'v1.2.3').replace('@TEAMX_REPOSITORY@', 'go-aie/teamx'));
  const env = { ...process.env, HOME: home, SHELL: '/bin/zsh', PATH: `${mocks}:${process.env.PATH}`,
    INSTALL_TEST_RECORD: record, INSTALL_TEST_PROGRAM: program, INSTALL_TEST_ASSETS: assets,
    INSTALL_TEST_ACCOUNT: p.join(temp, 'account'), INSTALL_TEST_TOOLS: p.join(temp, 'tools'),
    INSTALL_TEST_DOWNLOADS: p.join(temp, 'downloads') };
  const runDefault = (args = [], overrides = {}, script = source) => spawnSync('/bin/bash', ['-s', '--', ...args],
    { input: script, env: { ...env, ...overrides }, encoding: 'utf8' });
  const run = (args = [], overrides = {}, script = source) => runDefault(['--server-root', root,
    '--home', data, '--bin-dir', bin, ...args], overrides, script);
  const calls = () => fs.existsSync(record) ? fs.readFileSync(record, 'utf8').trim().split('\n').map(JSON.parse) : [];
  return { temp, home, root, data, bin, archive, env, run, runDefault, calls, assets, target };
}

test('one command with no directory flags installs macOS defaults under the current user', t => {
  const f = fixture(t), result = f.runDefault(['--archive', f.archive]);
  assert.equal(result.status, 0, result.stderr);
  const [args] = f.calls(), root = p.join(f.home, 'Library/Application Support/TeamX/server');
  assert.equal(args[args.indexOf('--server-root') + 1], root);
  assert.equal(args.includes('--home'), false, 'TeamX must resolve new defaults and existing registrations');
  assert.equal(fs.readlinkSync(p.join(f.home, '.local/bin/teamx')), p.join(root, 'current/bin/teamx'));
});

test('a piped release installer selects the native standard, pins downloads and deploys once', t => {
  for (const [platform, architecture] of [['Darwin', 'arm64'], ['Darwin', 'x86_64'], ['Linux', 'aarch64'], ['Linux', 'x86_64']]) {
    const f = fixture(t, platform, architecture);
    const script = source.replace('@TEAMX_VERSION@', 'v1.2.3').replace('@TEAMX_REPOSITORY@', 'go-aie/teamx');
    const result = f.run([], {}, script);
    assert.equal(result.status, 0, result.stderr);
    assert.equal((result.stdout + result.stderr).match(/TeamX 已安装并启动/g)?.length, 1);
    assert.ok(result.stdout.includes(`查看服务状态：${platform === 'Linux' ? 'sudo ' : ''}teamx service status`));
    assert.ok(result.stdout.includes(`命令位置：${p.join(f.bin, 'teamx')}`));
    assert.match(result.stdout + result.stderr, /http:\/\/127.0.0.1:8088/);
    assert.equal(fs.statSync(f.bin).mode & 0o777, 0o755);
    const [args] = f.calls();
    assert.equal(f.calls().length, 1);
    assert.deepEqual(args.slice(0, 2), ['service', 'install']);
    assert.equal(args[args.indexOf('--sha256') + 1], sha256(f.archive));
    assert.equal(args[args.indexOf('--home') + 1], f.data);
    assert.equal(fs.readlinkSync(p.join(f.bin, 'teamx')), p.join(f.root, 'current/bin/teamx'));
    assert.equal(fs.realpathSync(p.join(f.bin, 'teamx')), fs.realpathSync(p.join(f.root, 'releases/fixture/bin/teamx')));
    assert.equal(fs.existsSync(p.join(f.temp, '.teamx')), false);
    assert.match(fs.readFileSync(f.env.INSTALL_TEST_DOWNLOADS, 'utf8'), new RegExp(`/v1.2.3/teamx-v1.2.3-${f.target}-standard.tar.gz`));
    if (platform === 'Linux') {
      assert.equal(args[args.indexOf('--user') + 1], 'teamx');
      const tools = fs.readFileSync(f.env.INSTALL_TEST_TOOLS, 'utf8');
      assert.match(tools, /sudo -v/); assert.match(tools, /useradd/);
      assert.equal(fs.existsSync(p.join(f.home, '.zshrc')), false);
    }
  }
});

test('offline installation needs no network or init and safely reuses its command and PATH entry', t => {
  const f = fixture(t);
  const args = ['--archive', f.archive];
  for (let i = 0; i < 2; i++) {
    const result = f.run(args);
    assert.equal(result.status, 0, result.stderr);
  }
  assert.equal(f.calls().length, 1);
  assert.equal(fs.existsSync(f.env.INSTALL_TEST_DOWNLOADS), false);
  const rc = fs.readFileSync(p.join(f.home, '.zshrc'), 'utf8');
  assert.equal(rc.match(/# TeamX command/g).length, 1);
  const path = spawnSync('/bin/sh', ['-c', '. "$HOME/.zshrc"; command -v teamx'], { env: f.env, encoding: 'utf8' });
  assert.equal(path.status, 0, path.stderr);
  assert.equal(path.stdout.trim(), p.join(f.bin, 'teamx'));
});

test('existing registration stops before download, including without the command link', t => {
  const f = fixture(t);
  assert.equal(f.run(['--archive', f.archive]).status, 0);
  fs.unlinkSync(p.join(f.bin, 'teamx'));
  const result = f.run(['--version', 'v1.2.3']);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /teamx v1.0.0/);
  assert.match(result.stdout, /service upgrade --server-root/);
  assert.match(result.stdout, /本次未下载应用包或更改服务/);
  assert.equal(fs.existsSync(f.env.INSTALL_TEST_DOWNLOADS), false);
  assert.equal(f.calls().length, 1);
});

test('stable command link discovers a custom root before downloading, explicit roots still win', t => {
  const f = fixture(t);
  assert.equal(f.run(['--archive', f.archive]).status, 0);
  const result = f.runDefault(['--bin-dir', f.bin, '--version', 'v1.2.3']);
  assert.equal(result.status, 0, result.stderr);
  assert.ok(result.stdout.includes(f.root));
  assert.equal(fs.existsSync(f.env.INSTALL_TEST_DOWNLOADS), false);
  const other = p.join(f.temp, 'other installation');
  const conflict = f.runDefault(['--server-root', other, '--bin-dir', f.bin, '--version', 'v1.2.3']);
  assert.notEqual(conflict.status, 0);
  assert.match(conflict.stderr, /已存在且不属于此次安装/);
  assert.equal(fs.existsSync(other), false);
});

test('legacy CLI gets an explicit upgrade bootstrap, which preserves root and backup options', t => {
  for (const platform of ['Darwin', 'Linux']) {
    const f = fixture(t, platform, 'x86_64');
    assert.equal(f.run(['--archive', f.archive]).status, 0);
    const guidance = f.run(['--version', 'v1.2.3'], { INSTALL_TEST_LEGACY: 'true' });
    assert.equal(guidance.status, 0, guidance.stderr);
    assert.match(guidance.stdout, /当前 CLI 尚不支持联网升级/);
    assert.ok(guidance.stdout.includes('releases/download/v1.2.3/install.sh | bash -s -- --upgrade'));
    assert.equal(fs.existsSync(f.env.INSTALL_TEST_DOWNLOADS), false);
    const backup = p.join(f.temp, 'private backup');
    const command = guidance.stdout.split('\n').find(line => line.trim().startsWith('curl -fsSL '));
    const upgraded = spawnSync('/bin/bash', ['-c', command + ' --backup-dir ' + quote(backup)], { env: f.env, encoding: 'utf8' });
    assert.equal(upgraded.status, 0, upgraded.stderr);
    assert.equal(f.calls().length, 2);
    const args = f.calls()[1];
    assert.deepEqual(args.slice(0, 2), ['service', 'upgrade']);
    assert.equal(args[args.indexOf('--server-root') + 1], f.root);
    assert.equal(args[args.indexOf('--backup-dir') + 1], backup);
    assert.equal(args.includes('--user'), false);
    assert.equal(args.includes('--home'), false);
  }
});

test('damaged and incomplete registrations fail before downloads, repair is explicit', t => {
  const f = fixture(t);
  assert.equal(f.run(['--archive', f.archive]).status, 0);
  const damaged = f.run(['--version', 'v1.2.3'], { INSTALL_TEST_BAD_REGISTRATION: 'true' });
  assert.notEqual(damaged.status, 0);
  assert.match(damaged.stderr, /安装登记验证失败/);
  assert.equal(fs.existsSync(f.env.INSTALL_TEST_DOWNLOADS), false);
  fs.unlinkSync(p.join(f.root, 'service.json'));
  assert.match(f.run(['--version', 'v1.2.3']).stderr, /未完成的安装登记/);
  const repaired = f.run(['--repair', '--archive', f.archive]);
  assert.equal(repaired.status, 0, repaired.stderr);
  assert.equal(f.calls().length, 2);
});

test('macOS quarantined input remains unchanged and the deployed executable inherits no download attributes',
  { skip: process.platform !== 'darwin' }, t => {
    const f = fixture(t);
    execFileSync('/usr/bin/xattr', ['-w', 'com.apple.quarantine', '0081;00000000;TeamXTest;', f.archive]);
    const result = f.run(['--archive', f.archive]);
    assert.equal(result.status, 0, result.stderr);
    assert.match(execFileSync('/usr/bin/xattr', [f.archive], { encoding: 'utf8' }), /com.apple.quarantine/);
    const deployed = fs.realpathSync(p.join(f.bin, 'teamx'));
    assert.doesNotMatch(execFileSync('/usr/bin/xattr', [deployed], { encoding: 'utf8' }), /com.apple.quarantine/);
  });

test('PATH shell code preserves literal quotes, dollars, backticks and spaces', t => {
  const f = fixture(t);
  const bin = p.join(f.temp, "literal ' $VALUE `echo INJECTED` bin");
  const result = f.run(['--archive', f.archive, '--bin-dir', bin]);
  assert.equal(result.status, 0, result.stderr);
  const path = spawnSync('/bin/sh', ['-c', '. "$HOME/.zshrc"; command -v teamx'], { env: f.env, encoding: 'utf8' });
  assert.equal(path.status, 0, path.stderr);
  assert.equal(path.stdout.trim(), p.join(bin, 'teamx'));
});

test('invalid checksum, missing metadata and failed downloads execute no bundle code', t => {
  for (const mode of ['checksum', 'sidecar', 'duplicate', 'download', 'empty-bootstrap']) {
    const f = fixture(t);
    let args = ['--archive', f.archive];
    if (mode === 'checksum') fs.appendFileSync(f.archive, 'tampered');
    if (mode === 'sidecar') fs.unlinkSync(f.archive + '.sha256');
    if (mode === 'duplicate') fs.appendFileSync(p.join(f.assets, 'SHA256SUMS'), fs.readFileSync(f.archive + '.sha256'));
    if (mode === 'empty-bootstrap') {
      const name = p.basename(f.archive, '.tar.gz');
      fs.writeFileSync(p.join(f.temp, name, 'bin/teamx'), '');
      execFileSync('tar', ['-czf', f.archive, '-C', f.temp, name], { env: { ...process.env, COPYFILE_DISABLE: '1' } });
      args.push('--sha256', sha256(f.archive));
    }
    if (['duplicate', 'download'].includes(mode)) args = ['--version', 'v1.2.3'];
    const result = f.run(args, mode === 'download' ? { INSTALL_TEST_DOWNLOAD_FAIL: 'true' } : {});
    assert.notEqual(result.status, 0, mode);
    assert.equal(f.calls().length, 0);
    assert.equal(fs.existsSync(p.join(f.bin, 'teamx')), false);
  }
});

test('an unrelated command is preserved, and deployment failure publishes no CLI or success', t => {
  const f = fixture(t);
  fs.mkdirSync(f.bin); fs.writeFileSync(p.join(f.bin, 'teamx'), 'unrelated');
  const conflict = f.run(['--archive', f.archive]);
  assert.notEqual(conflict.status, 0); assert.match(conflict.stderr, /已存在/);
  assert.equal(fs.readFileSync(p.join(f.bin, 'teamx'), 'utf8'), 'unrelated');
  assert.equal(f.calls().length, 0);
  fs.unlinkSync(p.join(f.bin, 'teamx'));
  const failed = f.run(['--archive', f.archive], { INSTALL_TEST_DEPLOY_FAIL: 'true' });
  assert.equal(failed.status, 7);
  assert.doesNotMatch(failed.stdout, /已安装并启动/);
  assert.equal(fs.existsSync(p.join(f.bin, 'teamx')), false);
  assert.equal(fs.existsSync(p.join(f.home, '.zshrc')), false);
  const savedLog = failed.stderr.match(/日志已保留：([^\n]+)/)?.[1];
  assert.ok(savedLog);
  t.after(() => fs.rmSync(savedLog, { force: true }));
  assert.match(fs.readFileSync(savedLog, 'utf8'), /fixture deployment failed/);
  assert.equal(fs.statSync(savedLog).mode & 0o777, 0o600);
});

test('Linux reuses an existing service account and root never invokes sudo', t => {
  const f = fixture(t, 'Linux', 'x86_64');
  fs.writeFileSync(f.env.INSTALL_TEST_ACCOUNT, 'existing');
  const result = f.run(['--archive', f.archive, '--user', 'assistant'], { INSTALL_TEST_UID: '0' });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(fs.existsSync(f.env.INSTALL_TEST_TOOLS), false);
  const [args] = f.calls();
  assert.equal(args[args.indexOf('--user') + 1], 'assistant');
});

test('unsupported hosts, root macOS and unpinned source downloads stop before deployment', t => {
  for (const [platform, arch, env] of [['Darwin', 'arm64', { INSTALL_TEST_UID: '0' }], ['Linux', 'i386', {}], ['Other', 'arm64', {}]]) {
    const f = fixture(t, platform, arch), result = f.run(['--archive', f.archive], env);
    assert.notEqual(result.status, 0); assert.equal(f.calls().length, 0);
  }
  const f = fixture(t), result = f.run();
  assert.notEqual(result.status, 0); assert.match(result.stderr, /--version/);
  assert.equal(fs.existsSync(f.env.INSTALL_TEST_DOWNLOADS), false);
});
