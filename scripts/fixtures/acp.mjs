// Offline packaging fixture. This file is never included in a user archive.
import { createInterface } from 'node:readline';
import { appendFileSync } from 'node:fs';
const model = JSON.stringify(['teamx', 'packaging-fixture']);
const configOptions = [
  {
    id: 'model',
    currentValue: model,
    options: [{ group: 'teamx', options: [{ value: model }] }]
  }
];
let sessionId = `packaging-${process.pid}`;
const send = (value) =>
  process.stdout.write(JSON.stringify({ jsonrpc: '2.0', ...value }) + '\n');
for await (const line of createInterface({ input: process.stdin })) {
  const f = JSON.parse(line);
  if (!f.method) continue;
  appendFileSync(
    process.env.TEAMX_PACKAGING_FIXTURE_LOG,
    JSON.stringify({ pid: process.pid, method: f.method }) + '\n'
  );
  let result = {};
  switch (f.method) {
    case 'initialize':
      result = {
        protocolVersion: 1,
        authMethods: [],
        agentInfo: { name: 'packaging-fixture', version: '1' },
        agentCapabilities: { sessionCapabilities: { close: {}, resume: {} } }
      };
      break;
    case 'session/new':
      result = { sessionId, configOptions };
      break;
    case 'session/resume':
      sessionId = f.params.sessionId;
      result = { configOptions };
      break;
    case 'session/set_config_option':
      result = { configOptions };
      break;
    case 'session/prompt':
      result = { stopReason: 'end_turn' };
      break;
    case 'session/cancel':
      continue;
  }
  if (f.id !== undefined) send({ id: f.id, result });
}
