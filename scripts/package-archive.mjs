import * as fs from 'node:fs';
import { spawn } from 'node:child_process';
import { createGzip } from 'node:zlib';
import { pipeline } from 'node:stream/promises';

// Stream tar through fast gzip without an intermediate tar or a new host tool.
// Await both processes: a tar failure must never publish a valid-looking gzip.
export async function compressArchive(archive, directory, name, { level = 1 } = {}) {
  const tar = spawn('tar', ['-cf', '-', '-C', directory, name], {
    stdio: ['ignore', 'pipe', 'inherit'], env: { ...process.env, COPYFILE_DISABLE: '1' }
  });
  const completed = new Promise((resolve, reject) => {
    tar.once('error', reject);
    tar.once('close', code => code === 0 ? resolve() : reject(Error(`tar exited ${code}`)));
  });
  const packed = pipeline(tar.stdout, createGzip({ level }), fs.createWriteStream(archive));
  packed.catch(() => tar.kill());
  const results = await Promise.allSettled([completed, packed]);
  for (const result of results) if (result.status === 'rejected') throw result.reason;
}
