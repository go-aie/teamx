// Deterministic lifecycle probe, never included in a distribution archive.
import { createInterface } from 'node:readline';
import { appendFileSync, existsSync } from 'node:fs';
const log = method => appendFileSync(process.env.TEAMX_BACKGROUND_LOG,
  JSON.stringify({ pid: process.pid, method, at: Date.now() }) + '\n');
const model = JSON.stringify(['teamx', 'background-fixture']);
const configOptions = [{ id: 'model', currentValue: model,
  options: [{ group: 'teamx', options: [{ value: model }] }] }];
let sessionId = `background-${process.pid}`;
for await (const line of createInterface({ input: process.stdin })) {
  const frame = JSON.parse(line);
  if (!frame.method) continue;
  log(frame.method);
  let result = {};
  switch (frame.method) {
    case 'initialize':
      result = { protocolVersion: 1, authMethods: [],
        agentInfo: { name: 'background-fixture', version: '1' },
        agentCapabilities: { sessionCapabilities: { close: {}, resume: {} } } };
      break;
    case 'session/new': result = { sessionId, configOptions }; break;
    case 'session/resume': sessionId = frame.params.sessionId; result = { configOptions }; break;
    case 'session/set_config_option': result = { configOptions }; break;
    case 'session/prompt': {
      const deadline = Date.now() + 30000;
      while (!existsSync(process.env.TEAMX_BACKGROUND_RELEASE)) {
        if (Date.now() > deadline) throw Error('Background probe release timed out');
        await new Promise(resolve => setTimeout(resolve, 50));
      }
      log('prompt_completed');
      result = { stopReason: 'end_turn' };
      break;
    }
    case 'session/cancel': continue;
  }
  if (frame.id !== undefined)
    process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: frame.id, result }) + '\n');
}
