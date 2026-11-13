import * as fs from 'node:fs';
import * as p from 'node:path';
import { createHash } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { must, run } from './package-lib.mjs';

const targets = ['darwin-arm64', 'darwin-amd64', 'linux-arm64', 'linux-amd64'];
function fileDigest(file) {
  const descriptor = fs.openSync(file, 'r'), digest = createHash('sha256'), buffer = Buffer.alloc(1024 * 1024);
  try {
    let count;
    while ((count = fs.readSync(descriptor, buffer)) > 0) digest.update(buffer.subarray(0, count));
    return digest.digest('hex');
  } finally { fs.closeSync(descriptor); }
}

// Wrap an existing, checked archive without rebuilding it or executing any of
// its programs. Always take the installer from that archive, never the checkout.
export function packageOffline(archive, { output, sha256 } = {}) {
  must(typeof archive === 'string' && archive.length > 0, 'Supply --archive FILE, or make package-offline ARCHIVE=FILE');
  archive = p.resolve(archive);
  const name = p.basename(archive);
  must(/^teamx-[A-Za-z0-9._-]+\.tar\.gz$/.test(name), 'Expected a TeamX .tar.gz archive');
  must(fs.lstatSync(archive).isFile(), 'Archive must be a regular file');
  const digest = fileDigest(archive);
  if (!sha256) {
    const fields = fs.readFileSync(archive + '.sha256', 'utf8').trim().split(/\s+/);
    must(fields.length === 2 && fields[1] === name, 'Sidecar must identify exactly this archive');
    sha256 = fields[0];
  }
  must(typeof sha256 === 'string' && /^[a-fA-F0-9]{64}$/.test(sha256) && sha256.toLowerCase() === digest,
    'Archive SHA-256 mismatch');

  const stem = name.slice(0, -7);
  const entries = run('tar', ['-tzf', archive]).split('\n').filter(Boolean).map(name => name.replace(/\/$/, ''));
  must(entries.length > 0 && new Set(entries).size === entries.length && entries.every(name =>
    !p.isAbsolute(name) && !/[\r\\]/.test(name) && name.split('/').every(part => part && part !== '.' && part !== '..') &&
    (name === stem || name.startsWith(stem + '/'))), 'Archive paths must be unique and stay inside its named root');
  for (const file of ['manifest.json', 'install.sh', 'bin/teamx'])
    must(entries.includes(`${stem}/${file}`), `Archive is missing ${file}`);
  const manifest = JSON.parse(run('tar', ['-xOzf', archive, `${stem}/manifest.json`]));
  must(manifest.schema_version === 1 && manifest.profile === 'standard', 'Offline installation requires a standard archive');
  must(targets.includes(manifest.target) && typeof manifest.version === 'string' &&
    /^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(manifest.version) &&
    stem === `teamx-${manifest.version}-${manifest.target}-standard`, 'Archive name and manifest identity differ');
  const installer = run('tar', ['-xOzf', archive, `${stem}/install.sh`]);
  must(installer.startsWith('#!') && !installer.includes('\0'), 'Archive installer must be a nonempty shell script');

  output = p.resolve(output || p.join('dist', 'offline', stem + '-offline.zip'));
  must(output.endsWith('.zip'), 'Output must be a .zip file');
  must(!fs.existsSync(output), 'Output already exists; choose a new path');
  fs.mkdirSync(p.dirname(output), { recursive: true });
  // Stage beside the output so link(2) can publish a complete ZIP without ever
  // replacing a destination created by another process.
  const temporary = fs.mkdtempSync(p.join(p.dirname(output), '.teamx-offline-'));
  const folder = stem + '-offline', directory = p.join(temporary, folder);
  const names = ['install-offline.sh', 'install.sh', name, name + '.sha256', 'README.zh.md'];
  try {
    fs.mkdirSync(directory, { mode: 0o755 });
    fs.copyFileSync(archive, p.join(directory, name));
    must(fileDigest(p.join(directory, name)) === digest, 'Archive changed while creating the offline package');
    fs.writeFileSync(p.join(directory, name + '.sha256'), `${digest}  ${name}\n`);
    fs.writeFileSync(p.join(directory, 'install.sh'), installer);
    fs.writeFileSync(p.join(directory, 'install-offline.sh'), `#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
exec /bin/bash "$ROOT/install.sh" --archive "$ROOT/${name}" "$@"
`);
    fs.writeFileSync(p.join(directory, 'README.zh.md'), `# TeamX 本地安装

适用版本：${manifest.version}；服务机器平台：${manifest.target}。

解开此 ZIP 后，在本目录打开终端执行：

\x60\x60\x60sh
bash ./install-offline.sh
\x60\x60\x60

脚本校验本目录的原始 standard 归档，然后安装并启动常驻服务。无需访问 GitHub、另装 Go／Node、再次解压内层归档或执行 init。请保留此目录中的全部文件。

macOS 使用已登录普通用户，不加 sudo；Linux 需要 systemd，安装器按需使用 sudo 并准备专用普通账号。按安装输出打开网页、读取本机管理 token 并登录，在网页配置模型。模型服务和平台连接仍需相应网络。

后续使用 \x60teamx service status/stop/start/restart/uninstall\x60；Linux 管理命令前加 sudo。自定义目录等参数可直接追加，例如 \x60bash ./install-offline.sh --bin-dir /absolute/bin\x60；完整选项用 \x60bash ./install-offline.sh --help\x60 查看。

本包保留原始应用归档及其 SHA-256，不重新构建或修改归档。已有实例升级须使用备份与升级流程，不用安装脚本替代升级。
`);
    fs.chmodSync(directory, 0o755);
    for (const file of names) fs.chmodSync(p.join(directory, file), file.endsWith('.sh') ? 0o755 : 0o644);
    const zip = p.join(temporary, 'offline.zip');
    // The application archive is already compressed. Store the five explicit
    // files; never include a working directory, private home or extra siblings.
    run('zip', ['-0', '-X', '-q', zip, ...names.map(name => `${folder}/${name}`)], { cwd: temporary });
    const zipDigest = fileDigest(zip);
    fs.linkSync(zip, output);
    return { output, sha256: zipDigest, archive_sha256: digest, version: manifest.version,
      target: manifest.target, profile: manifest.profile };
  } finally {
    fs.rmSync(temporary, { recursive: true, force: true });
  }
}

if (process.argv[1] && p.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const options = {};
    let archive = process.env.TEAMX_OFFLINE_ARCHIVE;
    if (process.env.TEAMX_OFFLINE_OUTPUT) options.output = process.env.TEAMX_OFFLINE_OUTPUT;
    const args = process.argv.slice(2);
    while (args.length) {
      const flag = args.shift();
      if (flag === '--help' || flag === '-h') {
        console.log('Usage: node scripts/package-offline.mjs --archive FILE [--sha256 HEX] [--output FILE.zip]\n       make package-offline ARCHIVE=FILE [OUTPUT=FILE.zip]\nRequires Node, tar and zip; the default digest is read from FILE.sha256.');
        process.exit(0);
      }
      must(['--archive', '--output', '--sha256'].includes(flag) && args.length > 0, 'Unknown option or missing value');
      const value = args.shift();
      if (flag === '--archive') archive = value;
      else options[flag.slice(2)] = value;
    }
    console.log(JSON.stringify(packageOffline(archive, options), null, 2));
  } catch (error) {
    console.error(`Offline package failed: ${error.message}`);
    process.exitCode = 1;
  }
}
