import fs from 'node:fs';
import p from 'node:path';
import {fileURLToPath} from 'node:url';

// Use the same public file inventory as packaging, never a running Space's teams.
export function prepareTeamDefaults(configPath, root = p.resolve(p.dirname(fileURLToPath(import.meta.url)), '..')) {
  const realDirectory = path => {
    if (fs.existsSync(path)) return fs.realpathSync(path);
    return p.join(realDirectory(p.dirname(path)), p.basename(path));
  };
  root = fs.realpathSync(root);
  const home = realDirectory(p.dirname(p.resolve(configPath)));
  const destination = p.join(home, 'defaults/teams');
  const files = JSON.parse(fs.readFileSync(p.join(root, 'packaging/bundle-files.json'), 'utf8')).files
    .filter(file => file.destination.startsWith('share/teamx/teams/'));
  if (!files.some(file => file.destination.endsWith('/team.json'))) throw Error('No initial team templates');
  const regularPath = path => {
    for (let at = path; ; at = p.dirname(at)) {
      const info = fs.lstatSync(at, {throwIfNoEntry: false});
      if (info) {
        if (info.isSymbolicLink()) throw Error(`Template path is a symlink: ${at}`);
      }
      if (at === p.dirname(at)) break;
    }
  };
  regularPath(destination);
  const content = files.map(file => {
    const source = p.join(root, file.source), relative = p.relative('share/teamx/teams', file.destination);
    if (!relative || relative.startsWith('..') || p.isAbsolute(relative)) throw Error('Invalid template inventory');
    regularPath(source);
    if (!fs.lstatSync(source).isFile()) throw Error(`Invalid template: ${source}`);
    const data = fs.readFileSync(source);
    if (p.basename(relative) === 'team.json') JSON.parse(data.toString('utf8'));
    return {relative, data};
  });
  const parent = p.dirname(destination);
  fs.mkdirSync(parent, {recursive: true, mode: 0o700});
  const stage = fs.mkdtempSync(p.join(parent, '.teams-'));
  const previous = p.join(parent, '.teams-previous');
  try {
    for (const file of content) {
      const target = p.join(stage, file.relative);
      fs.mkdirSync(p.dirname(target), {recursive: true, mode: 0o700});
      fs.writeFileSync(target, file.data, {mode: 0o600});
    }
    fs.rmSync(previous, {recursive: true, force: true});
    if (fs.existsSync(destination)) fs.renameSync(destination, previous);
    try { fs.renameSync(stage, destination); }
    catch (error) {
      if (fs.existsSync(previous)) fs.renameSync(previous, destination);
      throw error;
    }
    fs.rmSync(previous, {recursive: true, force: true});
    return destination;
  } finally { fs.rmSync(stage, {recursive: true, force: true}); }
}

if (process.argv[1] && p.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  if (process.argv.length > 3) throw Error('Usage: node scripts/prepare-team-defaults.mjs [config.toml]');
  console.log(prepareTeamDefaults(process.argv[2] || '.teamx/config.toml'));
}
