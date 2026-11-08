import * as fs from 'node:fs';
import * as p from 'node:path';
import { randomBytes } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { files, hash, json, must, run, writeJSON } from './package-lib.mjs';

// Credential-bearing commands must never expose argv or subprocess output.
function secretRun(command, args) {
  try { return run(command, args, { stdio: ['ignore', 'pipe', 'pipe'] }); }
  catch { throw Error(`Signing credential operation failed: ${p.basename(command)}`); }
}

export function signingPolicy(target, mode = 'unsigned') {
  must(['unsigned', 'developer-id'].includes(mode), 'Unknown macOS signing mode');
  if (!target.startsWith('darwin-')) return { mode: 'not_applicable', status: 'not_applicable' };
  return { mode, status: mode === 'unsigned' ? 'not_run' : 'pending' };
}

// Include executables, native addons, dylibs and universal Mach-O files. Shell
// launchers are not Mach-O code. Skip symlinks, which retain their relative targets.
export function machOFiles(bundle) {
  return files(bundle).filter(name => {
    const file = p.join(bundle, name);
    if (!fs.lstatSync(file).isFile()) return false;
    const fd = fs.openSync(file, 'r'), header = Buffer.alloc(4);
    try { fs.readSync(fd, header, 0, 4, 0); } finally { fs.closeSync(fd); }
    return ['cffaedfe', 'cefaedfe', 'cafebabe', 'bebafeca', 'cafebabf', 'bfbafeca'].includes(header.toString('hex'));
  });
}

export function signBundle(bundle, root, target, mode, env = process.env) {
  const policy = signingPolicy(target, mode);
  if (policy.status !== 'pending') return policy;
  must(process.platform === 'darwin', 'Developer ID signing requires macOS');
  must(/^Developer ID Application: [^\r\n]+ \([A-Z0-9]{10}\)$/.test(env.TEAMX_SIGN_IDENTITY || ''),
    'A Developer ID Application identity is required');
  must(env.TEAMX_SIGN_KEYCHAIN && env.TEAMX_NOTARY_PROFILE, 'Signing keychain/notary profile required');
  const paths = machOFiles(bundle);
  must(paths.includes('runtime/node/bin/node') && paths.includes('bin/teamx'), 'Missing code to sign');
  for (const name of paths) {
    const file = p.join(bundle, name);
    run('/usr/bin/codesign', ['--force', '--sign', env.TEAMX_SIGN_IDENTITY,
      '--keychain', env.TEAMX_SIGN_KEYCHAIN, '--timestamp', '--options', 'runtime',
      ...(name === 'runtime/node/bin/node' ? ['--entitlements', p.join(root, 'packaging/node-entitlements.plist')] : []), file]);
    run('/usr/bin/codesign', ['--verify', '--strict', '--verbose=2', file]);
  }
  return { mode, status: 'passed', identity: env.TEAMX_SIGN_IDENTITY,
    files: paths.map(path => ({ path, sha256: hash(fs.readFileSync(p.join(bundle, path))) })) };
}

export function notarizeBundle(bundle, stage, signing, env = process.env) {
  if (signing.mode !== 'developer-id') return { status: signing.status === 'not_applicable' ? 'not_applicable' : 'not_run' };
  must(signing.status === 'passed', 'Notarization requires successful signing');
  const zip = p.join(stage, 'notarization.zip');
  run('/usr/bin/ditto', ['-c', '-k', '--keepParent', bundle, zip]);
  const receipt = JSON.parse(secretRun('/usr/bin/xcrun', ['notarytool', 'submit', zip,
    '--keychain-profile', env.TEAMX_NOTARY_PROFILE, '--keychain', env.TEAMX_SIGN_KEYCHAIN,
    '--wait', '--timeout', '30m', '--output-format', 'json']));
  must(receipt.status === 'Accepted' && /^[a-f0-9-]{36}$/i.test(receipt.id || ''), 'Apple notarization did not accept the candidate');
  // ZIP and bare command-line executables cannot be stapled. The ticket is
  // available online; clean downloaded-machine Gatekeeper acceptance is separate.
  return { status: 'passed', submission_id: receipt.id, submitted_zip_sha256: hash(fs.readFileSync(zip)),
    ticket: 'online', stapling: 'not_supported_for_bare_cli_tar_archive' };
}

export function prepareSigning(directory, env = process.env) {
  must(process.platform === 'darwin', 'Signing credentials require macOS');
  for (const key of ['TEAMX_MACOS_P12_BASE64', 'TEAMX_MACOS_P12_PASSWORD', 'TEAMX_APPLE_API_KEY_BASE64',
    'TEAMX_APPLE_API_KEY_ID', 'TEAMX_APPLE_API_ISSUER', 'TEAMX_SIGN_IDENTITY'])
    must(env[key] && !/[\r\n]/.test(env[key]), `Missing/invalid signing secret: ${key}`);
  fs.mkdirSync(directory, { mode: 0o700 });
  const keychain = p.join(directory, 'signing.keychain-db'), password = randomBytes(32).toString('hex');
  const p12 = p.join(directory, 'identity.p12'), key = p.join(directory, 'AuthKey.p8');
  // Retain only paths for unconditional cleanup, including partial setup failure.
  writeJSON(p.join(directory, 'state.json'), { keychain });
  try {
    fs.writeFileSync(p12, Buffer.from(env.TEAMX_MACOS_P12_BASE64, 'base64'), { mode: 0o600 });
    fs.writeFileSync(key, Buffer.from(env.TEAMX_APPLE_API_KEY_BASE64, 'base64'), { mode: 0o600 });
    secretRun('/usr/bin/security', ['create-keychain', '-p', password, keychain]);
    secretRun('/usr/bin/security', ['set-keychain-settings', '-lut', '7200', keychain]);
    secretRun('/usr/bin/security', ['unlock-keychain', '-p', password, keychain]);
    secretRun('/usr/bin/security', ['import', p12, '-k', keychain, '-P', env.TEAMX_MACOS_P12_PASSWORD,
      '-T', '/usr/bin/codesign', '-T', '/usr/bin/security']);
    secretRun('/usr/bin/security', ['set-key-partition-list', '-S', 'apple-tool:,apple:,codesign:',
      '-s', '-k', password, keychain]);
    secretRun('/usr/bin/xcrun', ['notarytool', 'store-credentials', 'teamx-candidate', '--key', key,
      '--key-id', env.TEAMX_APPLE_API_KEY_ID, '--issuer', env.TEAMX_APPLE_API_ISSUER, '--keychain', keychain]);
    must(env.GITHUB_ENV, 'GITHUB_ENV required for signing preparation');
    fs.appendFileSync(env.GITHUB_ENV, `TEAMX_SIGN_KEYCHAIN=${keychain}\nTEAMX_NOTARY_PROFILE=teamx-candidate\n`);
  } finally {
    fs.rmSync(p12, { force: true }); fs.rmSync(key, { force: true });
  }
}

export function cleanupSigning(directory) {
  if (!fs.existsSync(directory)) return;
  const state = json(p.join(directory, 'state.json'));
  must(state.keychain === p.join(directory, 'signing.keychain-db'), 'Unexpected signing keychain path');
  if (fs.existsSync(state.keychain)) secretRun('/usr/bin/security', ['delete-keychain', state.keychain]);
  fs.rmSync(directory, { recursive: true });
}

if (process.argv[1] && p.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [command, directory, ...extra] = process.argv.slice(2);
    must(directory && !extra.length, 'Usage: package-signing.mjs prepare|cleanup DIRECTORY');
    if (command === 'prepare') prepareSigning(p.resolve(directory));
    else if (command === 'cleanup') cleanupSigning(p.resolve(directory));
    else throw Error('Unknown signing command');
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
