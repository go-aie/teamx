import fs from 'node:fs';
import p from 'node:path';
import { createHash } from 'node:crypto';
import { hash, inventory, extract, json, must, run, writeJSON } from './package-lib.mjs';

async function fileHash(file) {
  const value = createHash('sha256');
  for await (const block of fs.createReadStream(file)) value.update(block);
  return value.digest('hex');
}

// An uncompressed tar avoids copying a large npm tree file by file into the
// cache. Each consumer extracts independent files; no mutable hardlinks exist.
export async function preparedDependencies({ cache, identity, destination, build, validate = () => {}, timing }) {
  const step = (label, fn) => timing ? timing.step('dependency cache: ' + label, fn) : fn();
  const timed = (label, fn) => timing ? timing.run('dependency cache: ' + label, fn) : fn();
  const key = hash(JSON.stringify(identity));
  const parent = p.join(cache, 'prepared-dependencies'), entry = p.join(parent, key);
  fs.mkdirSync(parent, { recursive: true, mode: 0o700 });
  async function verify() {
    const record = json(p.join(entry, 'record.json'));
    must(JSON.stringify(record.identity) === JSON.stringify(identity) &&
      record.payload_sha256 === await timed('verify archive digest', () => fileHash(p.join(entry, 'payload.tar'))),
    `Prepared dependency cache mismatch: ${entry}; preserve it for inspection and remove this entry before retrying`);
    return record;
  }
  if (fs.existsSync(entry)) {
    const record = await verify();
    console.log(`Dependencies: extracting verified ${key}`);
    const actual = extract(p.join(entry, 'payload.tar'), destination, 0, { step });
    must(JSON.stringify(actual) === JSON.stringify(record.inventory), 'Copied dependency cache mismatch');
    await validate(destination, record.metadata);
    console.log(`Dependencies: reused verified ${key}`);
    return record.metadata;
  }
  console.log(`Dependencies: preparing ${key}`);
  const metadata = await timed('assemble dependencies', () => build(destination));
  await validate(destination, metadata);
  const stage = fs.mkdtempSync(p.join(parent, '.pending-'));
  try {
    console.log('Dependencies: recording inventory and packing prepared cache');
    const items = step('scan prepared files', () => inventory(destination)), archive = p.join(stage, 'payload.tar');
    step('write prepared archive', () => run('tar', ['-cf', archive, '-C', destination, '.'], { env: { ...process.env, COPYFILE_DISABLE: '1' } }));
    const payload_sha256 = await timed('hash prepared archive', () => fileHash(archive));
    console.log(`Dependencies: ${items.length} entries; prepared archive ${fs.statSync(archive).size} bytes`);
    writeJSON(p.join(stage, 'record.json'), { identity, inventory: items, metadata, payload_sha256 });
    try { fs.renameSync(stage, entry); }
    catch (error) { if (!['EEXIST', 'ENOTEMPTY'].includes(error.code)) throw error; await verify(); }
  } finally { fs.rmSync(stage, { recursive: true, force: true }); }
  return metadata;
}
