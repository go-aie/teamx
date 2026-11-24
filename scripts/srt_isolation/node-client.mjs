import fs from 'node:fs';
if (process.argv[2] === 'read') {
  try { fs.readFileSync(process.argv[3]); console.log('READ_ALLOWED'); }
  catch { console.log('READ_DENIED'); process.exitCode = 1; }
} else {
  try {
    const r = await fetch(process.argv[3], {
      headers: { Authorization: `Bearer ${process.env.SRT_TEST_TOKEN}` },
      signal: AbortSignal.timeout(3000),
    });
    console.log(JSON.stringify({ status: r.status, body: (await r.text()).slice(0, 1024) }));
  } catch { console.log(JSON.stringify({ connection_failed: true })); }
}
