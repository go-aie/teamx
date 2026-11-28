import {test} from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import p from 'node:path';
import {prepareTeamDefaults} from './prepare-team-defaults.mjs';

test('development defaults use the package inventory and leave user teams untouched', t => {
  const root = fs.realpathSync(fs.mkdtempSync(p.join(os.tmpdir(), 'team-defaults-')));
  t.after(() => fs.rmSync(root, {recursive:true, force:true}));
  const home = p.join(root, 'home'), source = p.join(root, 'source');
  fs.mkdirSync(p.join(root, 'packaging'));
  fs.mkdirSync(source);
  fs.writeFileSync(p.join(source, 'team.json'), '{"name":"initial"}');
  fs.writeFileSync(p.join(source, 'unlisted-secret'), 'private');
  fs.writeFileSync(p.join(root, 'packaging/bundle-files.json'), JSON.stringify({files:[
    {source:'source/team.json',destination:'share/teamx/teams/example/team.json'}
  ]}));
  fs.mkdirSync(p.join(home, 'teams'), {recursive:true});
  fs.writeFileSync(p.join(home, 'teams/custom'), 'user customization');
  const config = p.join(home, 'custom.toml'), destination = prepareTeamDefaults(config, root);
  assert.equal(destination, p.join(home, 'defaults/teams'));
  assert.equal(fs.readFileSync(p.join(destination, 'example/team.json'), 'utf8'), '{"name":"initial"}');
  assert.equal(fs.existsSync(p.join(destination, 'unlisted-secret')), false);
  fs.writeFileSync(p.join(destination, 'obsolete'), 'old version');
  fs.writeFileSync(p.join(source, 'team.json'), '{"name":"new version"}');
  prepareTeamDefaults(config, root);
  assert.equal(fs.existsSync(p.join(destination, 'obsolete')), false);
  assert.equal(fs.readFileSync(p.join(destination, 'example/team.json'), 'utf8'), '{"name":"new version"}');
  assert.equal(fs.readFileSync(p.join(home, 'teams/custom'), 'utf8'), 'user customization');
  fs.writeFileSync(p.join(source, 'team.json'), 'invalid JSON');
  assert.throws(() => prepareTeamDefaults(config, root), SyntaxError);
  fs.unlinkSync(p.join(source, 'team.json'));
  assert.throws(() => prepareTeamDefaults(config, root));
  assert.equal(fs.readFileSync(p.join(destination, 'example/team.json'), 'utf8'), '{"name":"new version"}');
  fs.rmSync(destination, {recursive:true});
  fs.symlinkSync(source, destination);
  assert.throws(() => prepareTeamDefaults(config, root), /symlink/);
  assert.equal(fs.readFileSync(p.join(source, 'unlisted-secret'), 'utf8'), 'private');
});
