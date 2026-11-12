// This plugin has no file, process, network, Room or platform capabilities.
// Agent-local registrations survive an empty global allow list.
export const name = 'teamx-model-probe';
export const inject = ['tools', 'agents'];
export function apply(ctx, config) {
  let calls = 0;
  let steps = 0;
  ctx.on('agent/created', ({ agent }) => {
    agent.ctx.tools.restrict({ allow: [] });
    agent.ctx.tools.register({
      name: 'teamx_probe',
      description: 'Return a fixed connectivity receipt. No side effects.',
      parameters: { type: 'object', properties: {}, additionalProperties: false },
      output: {
        schema: { type: 'string' },
        render: (_args, value) => [{ type: 'text', text: value }],
      },
      execute: async () => {
        if (++calls > 1) throw new Error('probe tool budget exceeded');
        return config.receipt;
      },
    });
    const names = [...agent.ctx.tools.view(agent).visible.keys()];
    if (names.length !== 1 || names[0] !== 'teamx_probe') throw new Error('probe tool isolation failed');
  });
  ctx.on('agent/request', (_event, next) => {
    if (++steps > 4) throw new Error('probe request budget exceeded');
    return next();
  });
  ctx.provide('teamxProbeReady', true);
}
