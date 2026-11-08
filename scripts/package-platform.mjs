import * as fs from 'node:fs';
import * as p from 'node:path';
import { hash, inventory, extract, must, writeJSON } from './package-lib.mjs';
import { hasDependency } from './package-profiles.mjs';

// Inspect headers rather than trusting an archive filename or a successful
// invocation under transparent CPU emulation.
export function binaryTarget(path) {
  const fd = fs.openSync(path, 'r'), header = Buffer.alloc(64);
  try { fs.readSync(fd, header, 0, header.length, 0); }
  finally { fs.closeSync(fd); }
  if (header.subarray(0, 4).equals(Buffer.from([0x7f, 0x45, 0x4c, 0x46]))) {
    must(header[4] === 2 && header[5] === 1, `Unsupported ELF: ${path}`);
    const arch = { 62: 'amd64', 183: 'arm64' }[header.readUInt16LE(18)];
    must(arch, `Unknown ELF architecture: ${path}`);
    return `linux-${arch}`;
  }
  if (header.readUInt32LE(0) === 0xfeedfacf) {
    const arch = { 0x01000007: 'amd64', 0x0100000c: 'arm64' }[header.readUInt32LE(4)];
    must(arch, `Unknown Mach-O architecture: ${path}`);
    return `darwin-${arch}`;
  }
  throw Error(`Expected a target binary: ${path}`);
}

export function checkBinaries(bundle, target, profile) {
  const paths = ['bin/teamx', 'bin/teamx-gitlab', 'bin/teamx-scion', 'runtime/node/bin/node'];
  if (hasDependency(profile, 'glab')) paths.push('runtime/glab/bin/glab');
  if (hasDependency(profile, 'lark-cli')) paths.push('bin/lark-cli');
  if (hasDependency(profile, 'git')) paths.push('runtime/git/bin/git', 'runtime/git/libexec/git-core/git-remote-https');
  for (const path of paths) must(binaryTarget(p.join(bundle, path)) === target, `Binary target mismatch: ${path}`);
}

export async function assemblePlatformDependencies({ root, bundle, target, profile, lock, glabLock, download }) {
  const dep = lock.dependencies;
  for (const [name, entry, executable] of [
    ...(hasDependency(profile, 'glab', lock) ? [['glab', glabLock.platforms[target.replace('-', '_')], 'bin/glab']] : []),
    ...(hasDependency(profile, 'lark-cli', lock) ? [['lark', dep['lark-cli'].platforms[target], 'lark-cli']] : [])
  ]) {
    // Preserve upstream documentation and notices along with its executable.
    const dest = p.join(bundle, 'runtime', name);
    extract(await download(entry), dest);
    if (name === 'glab') {
      fs.copyFileSync(p.join(root, 'packaging/glab-launcher'), p.join(bundle, 'bin/glab'));
      fs.chmodSync(p.join(bundle, 'bin/glab'), 0o755);
    } else fs.copyFileSync(p.join(dest, executable), p.join(bundle, 'bin/lark-cli'));
  }
  if (!hasDependency(profile, 'git', lock)) return;
  const git = p.join(bundle, 'runtime/git');
  extract(await download(dep.git.platforms[target]), git);
  for (const name of ['bin/git', 'libexec/git-core/git-remote-https', 'share/git-core/templates', 'etc/gitconfig'])
    must(fs.existsSync(p.join(git, name)), `Incomplete Git runtime: ${name}`);
  if (target.startsWith('linux-')) must(fs.existsSync(p.join(git, 'ssl/cacert.pem')), 'Git CA bundle missing');
  fs.copyFileSync(p.join(root, 'packaging/git-launcher'), p.join(bundle, 'bin/git'));
  fs.chmodSync(p.join(bundle, 'bin/git'), 0o755);

  // Ship the matching source, including the otherwise empty Git submodule,
  // and the scripts/workflows/dependency inputs used by dugite-native.
  const source = p.join(bundle, 'THIRD_PARTY_NOTICES/git-source');
  for (const entry of dep.git.redistribution.sources) {
    const dest = p.join(source, entry.destination);
    extract(await download(entry), dest, 1);
  }
  for (const name of ['dugite/script/build-macos.sh', 'dugite/script/build-ubuntu.sh', 'dugite/dependencies.json', 'dugite/git/COPYING', 'dugite/git/Makefile'])
    must(fs.existsSync(p.join(source, name)), `Git source/build material missing: ${name}`);
  const runtime = inventory(git);
  const notices = runtime.filter(f => /(?:^|\/)(?:licen[cs]e|copying|notice|copyright|third.?party)[^/]*$/i.test(f.path));
  // GCM bundles .NET and its dependencies; a top-level Git GPL text cannot
  // replace these third-party notices.
  if (fs.existsSync(p.join(git, 'libexec/git-core/git-credential-manager')))
    must(notices.some(f => /(?:third.?party|\/NOTICE$)/i.test(f.path)), 'GCM third-party notices missing');
  writeJSON(p.join(bundle, 'THIRD_PARTY_NOTICES/git-materials.json'), {
    source_commit: dep.git.source_git_commit,
    build_commit: dep.git.commit,
    sources: dep.git.redistribution.sources,
    source_tree_sha256: hash(JSON.stringify(inventory(source))),
    runtime_notices: notices,
    system_libraries: lock.targets[target].qualification_baseline.system_libraries
  });
}
