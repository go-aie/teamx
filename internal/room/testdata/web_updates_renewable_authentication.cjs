const {chromium} = require('playwright');
const assert = require('node:assert/strict');
(async () => {
 const chunks = [];
 for await (const chunk of process.stdin) chunks.push(chunk);
 const {endpoint, path, cookie} = JSON.parse(Buffer.concat(chunks));
 const browser = await chromium.launch({headless:true});
 try {
  // The certificate and browser session belong only to this isolated Go fixture.
  const context = await browser.newContext({ignoreHTTPSErrors:true});
  await context.addCookies([cookie]);
  const page = await context.newPage(), errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.goto(endpoint + '/ui/brand-mark.svg');
  await page.evaluate(path => {
   window.updateFrames = []; window.streamErrors = 0;
   window.source = new EventSource(path);
   for (const event of ['hello', 'heartbeat']) window.source.addEventListener(event, message => {
    window.updateFrames.push({event, data:JSON.parse(message.data)});
   });
   window.source.onerror = () => { window.streamErrors++; };
  }, path);
  await page.waitForFunction(() => window.updateFrames.some(frame => frame.event === 'hello'));
  // A production heartbeat arrives after 15 real seconds, beyond the old
  // five-second read deadline and the native browser's reconnect interval.
  await page.waitForFunction(() => window.updateFrames.some(frame => frame.event === 'heartbeat'), null, {timeout:25000});
  const result = await page.evaluate(() => ({
   hellos:window.updateFrames.filter(frame => frame.event === 'hello').length,
   heartbeats:window.updateFrames.filter(frame => frame.event === 'heartbeat').length,
   connectionIDs:[...new Set(window.updateFrames.map(frame => frame.data.connection_id))],
   streamErrors:window.streamErrors, readyState:window.source.readyState,
  }));
  assert.equal(result.hellos, 1);
  assert.equal(result.connectionIDs.length, 1);
  assert.equal(result.streamErrors, 0);
  assert.equal(result.readyState, 1);
  assert.deepEqual(errors, []);
  await page.evaluate(() => window.source.close());
  console.log(JSON.stringify({renewable_session:true, native_event_source:true, transport:'HTTP/1.1',
   hellos:result.hellos, heartbeats:result.heartbeats, stream_errors:result.streamErrors}));
 } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
