// Framework-only execution binding and durable delivery receipts. No protocol
// identifiers or confirmation controls enter model tool arguments.
import { AsyncLocalStorage } from 'node:async_hooks';
import { randomUUID, createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
export const name = 'teamx-input-bridge';
const retryableTools = new Set(['inbox_list', 'inbox_read', 'history_list', 'history_read']);
export const inject = ['tools', 'shellEnv', 'sessions'];

function canonical(value) {
  if (Array.isArray(value)) return '[' + value.map(canonical).join(',') + ']';
  if (value !== null && typeof value === 'object') return '{' + Object.keys(value).sort().map(key => canonical(key) + ':' + canonical(value[key])).join(',') + '}';
  return JSON.stringify(value).replace(/\u2028/g, '\\u2028').replace(/\u2029/g, '\\u2029');
}
export function bodyDigest(body) { return createHash('sha256').update(canonical(body)).digest('hex'); }
const MAX_CLI_OUTPUT = 16 << 20;
function businessBodies(texts) {
  const bodies = [];
  for (const text of texts) {
    let candidates = [text];
    try { JSON.parse(text); } catch {
      // DSH coalesces adjacent MCP text blocks before an image. Our complete
      // compact JSON body remains one line, followed by material provenance.
      candidates = text.split('\n');
    }
    for (const candidate of candidates) {
    try {
      const value = JSON.parse(candidate);
      if (value && Array.isArray(value.entries) && Number.isInteger(value.remaining_entries)) {
        // CLI carries transport material blocks beside the same business body.
        const body = { ...value }; delete body.content; bodies.push(body);
      }
    } catch { /* Not a complete business result; it cannot acknowledge a page. */ }
    }
  }
  return bodies;
}
export function apply(ctx, config) {
  const scope = new AsyncLocalStorage();
  const agents = new WeakMap();
  const pending = new WeakMap();
  const executions = new Map();
  let shellRestore;
  const fetchOriginal = globalThis.fetch;
  async function retryFetch(request) {
    for (let i = 0; i < 3; i++) {
      try {
        const response = await fetchOriginal(request.clone());
        if ((response.status < 500 && response.status !== 409) || i === 2) return response;
        await response.body?.cancel();
      } catch (error) { if (request.signal.aborted || i === 2) throw error; }
      await new Promise(resolve => setTimeout(resolve, 50 * (i + 1)));
    }
  }
  globalThis.fetch = async (input, init) => {
    const execution = scope.getStore();
    if (!execution) return fetchOriginal(input, init);
    const request = new Request(input, init);
    if (request.url !== execution.endpoint + '/mcp' || request.method !== 'POST') return fetchOriginal(request);
    const body = await request.clone().json();
    if (body.method !== 'tools/call') return fetchOriginal(request);
    request.headers.set('Authorization', 'Bearer ' + execution.token);
    request.headers.set('X-Teamx-Call', execution.call);
    if (body.params?.name === 'inbox_read') execution.read = true;
    return retryableTools.has(body.params?.name) ? retryFetch(request) : fetchOriginal(request);
  };
  ctx.on('dispose', () => { globalThis.fetch = fetchOriginal; shellRestore?.(); executions.clear(); });
  ctx.on('agent/request', async (event, next) => {
    const binding = JSON.parse(readFileSync(config.bindingPath, 'utf8'));
    if (!binding.token || !binding.endpoint) throw new Error('Input execution unavailable');
    const receipts = pending.get(event.agent) ?? [];
    if (receipts.length) {
      // Tool results have reached their final policy/rendered form and have
      // been appended to Session history before the next model request.
      if (!await ctx.sessions.flush(event.agent.session)) throw new Error('Input result durability unavailable');
      while (receipts.length) {
        const execution = receipts[0];
        const response = await retryFetch(new Request(execution.endpoint + '/mcp', {
          method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + execution.token, 'X-Teamx-Call': execution.call },
          body: JSON.stringify({ method: 'teamx/received', received_digests: execution.digests }), signal: AbortSignal.timeout(15000),
        }));
        await response.body?.cancel();
        // A superseded Run can no longer acknowledge its calls. Its saved
        // page remains available for replay through the current binding.
        const superseded = execution.token !== binding.token && (response.status === 401 || response.status === 403);
        if (!response.ok && !superseded) throw new Error('Input result could not be verified; read the same stream again');
        receipts.shift();
      }
      pending.delete(event.agent);
    }
    agents.set(event.agent, binding);
    return next();
  });
  ctx.shellEnv.register({
    name: 'teamx-input-execution',
    variables: {
      DSH_TEAMX_INPUT_BINDING: { description: 'Private execution binding managed by the TeamX runtime.' },
      ...(config.executionProxy ? { DSH_TEAMX_EXECUTION_PROXY: { description: 'Private local execution transport.' } } : {}),
    },
    resolve: () => {
      const e = scope.getStore();
      return e ? {
        DSH_TEAMX_INPUT_BINDING: JSON.stringify({ token: e.token, call: e.call }),
        ...(config.executionProxy && process.env.HTTP_PROXY ? { DSH_TEAMX_EXECUTION_PROXY: process.env.HTTP_PROXY } : {}),
      } : {};
    },
  });
  const listEnvironment = ctx.shellEnv.list.bind(ctx.shellEnv);
  ctx.shellEnv.list = () => listEnvironment().filter(v => !['DSH_TEAMX_INPUT_BINDING', 'DSH_TEAMX_EXECUTION_PROXY'].includes(v.key));
  ctx.on('tools/execute', async (exec, next) => {
    const binding = agents.get(exec.agent);
    if (!binding || exec.token === undefined) throw new Error('Input execution correlation unavailable');
    const execution = { ...binding, endpoint: binding.endpoint.replace(/\/$/, ''), call: randomUUID(), read: false, shell: exec.name === 'bash' };
    executions.set(exec.token, execution);
    // This is a trusted executor field, never an extra model argument. The
    // ordinary Bash tail cap is too small for a formal page with materials.
    const shell = execution.shell && ctx.get?.('shell');
    if (shell && !shellRestore) {
      const resolve = shell.resolve;
      shell.resolve = function(request) {
        return resolve.call(this, scope.getStore()?.shell ? { ...request, stdoutMaxBytes: MAX_CLI_OUTPUT } : request);
      };
      shellRestore = () => { shell.resolve = resolve; };
    }
    return scope.run(execution, async () => {
      const result = await next();
      const value = result.value;
      if (!execution.shell || result.isError || value?.kind !== 'foreground' || value.exitCode !== 0 || value.timedOut || value.aborted || value.stdout?.truncated) return result;
      let page;
      try { page = JSON.parse(value.stdout?.text ?? ''); } catch { return result; }
      if (!page || !Array.isArray(page.entries) || !Number.isInteger(page.remaining_entries) || !Array.isArray(page.content)) return result;
      const body = { ...page }; delete body.content;
      const blocks = page.content;
      if (blocks.some(block => block.type !== 'text' && block.type !== 'image')) throw new Error('Unsupported input material');
      const images = blocks.filter(block => block.type === 'image');
      let refs = [];
      if (images.length) {
        const attachments = ctx.get?.('attachments');
        const llm = ctx.get?.('llm');
        const routed = exec.agent?.session.requestHeader?.()?.config;
        const provider = routed?.provider ?? exec.agent?.options?.provider;
        const model = routed?.model ?? exec.agent?.options?.model;
        if (!attachments || !llm || !provider || !model) throw new Error('Input image admission unavailable');
        const info = await llm.resolveModelInfo(provider, model, exec.signal);
        if (!info.inputModalities?.includes('image') || exec.signal?.aborted) throw new Error('Input image admission unavailable');
        const decoded = images.map(block => {
          if (!['image/png','image/jpeg','image/webp','image/gif'].includes(block.mimeType) || typeof block.data !== 'string') throw new Error('Invalid input image');
          const data = Buffer.from(block.data, 'base64');
          if (data.toString('base64') !== block.data) throw new Error('Invalid input image encoding');
          return { data, mediaType: block.mimeType };
        });
        refs = await attachments.saveImages(decoded);
      }
      let image = 0;
      execution.cliProjection = [{type:'text',text:JSON.stringify(body)}, ...blocks.map(block => block.type === 'image' ? {type:'image',attachment:refs[image++]} : {type:'text',text:block.text})];
      return result;
    });
  });
  ctx.on('tools/post-execute', async (exec, result, next) => {
    const decision = await next();
    const execution = executions.get(exec.token);
    if (!execution?.cliProjection || result.isError || decision.kind !== 'accept' || Object.hasOwn(decision, 'value')) return decision;
    // Respect policy replacements/blocks. Only replace an accepted, unchanged
    // Bash rendering with the corresponding durable business/image content.
    if (decision.content !== undefined && JSON.stringify(decision.content) !== JSON.stringify(result.content)) return decision;
    execution.projectedCLI = true;
    return { ...decision, content: execution.cliProjection };
  });
  // Observe the FINAL immutable result, after policies and rich-content
  // finalization. A command exit, partial stdout or intermediate body is not
  // evidence that the model received the business result.
  ctx.on('tools/result', (exec, result) => {
    const execution = executions.get(exec.token); executions.delete(exec.token);
    if (!execution || result.isError || (!execution.read && !execution.shell)) return;
    const rendered = result.content.filter(block => block.type === 'text').map(block => block.text);
    let bodies;
    if (execution.shell) {
      const value = result.value;
      if (value?.kind !== 'foreground' || value.exitCode !== 0 || value.timedOut || value.aborted || value.stdout?.truncated) return;
      const stdout = value.stdout?.text ?? '';
      // Do not acknowledge redirected/discarded or policy-replaced output.
      bodies = execution.projectedCLI ? businessBodies(rendered) : rendered.some(text => text.includes(stdout.trim())) ? businessBodies(stdout.split('\n')) : [];
    } else {
      bodies = businessBodies(rendered);
    }
    const images = result.content.filter(block => block.type === 'image').length;
    const supplied = bodies.reduce((n, body) => n + (body.images ?? []).filter(image => image.status === 'supplied' && !image.shared_source_ref && !image.shared_room_seq).length, 0);
    if (images < supplied) bodies = [];
    // Withheld or incomplete output has no proof to send. The service
    // retains its prepared page and terminal barrier for an ordinary reread.
    if (!bodies.length) return;
    execution.digests = bodies.map(bodyDigest);
    const receipts = pending.get(exec.agent) ?? []; receipts.push(execution); pending.set(exec.agent, receipts);
  });
  ctx.provide('teamxInputBridgeReady', true);
}
