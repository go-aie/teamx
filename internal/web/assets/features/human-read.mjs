// Human reading is independent of message/Agent refreshes and browser storage.
// Only confirmed server sets are shared; optimistic observations remain pending.
export function createHumanRead({ env, runtime, changed }) {
  const { state, notice, refresh } = runtime;
  const { setTimeout, clearTimeout, performance } = env;
  let context = null;
  const key = () => state.info ? JSON.stringify([state.installationID || "", state.info.room_id,
    state.info.room_instance, state.info.service_epoch, state.operator]) : "";
  const current = ctx => ctx === context && ctx.installation === (state.installationID || "") && ctx.operator === state.operator &&
    ctx.info.room_id === state.info?.room_id && ctx.info.room_instance === state.info?.room_instance && ctx.info.service_epoch === state.info?.service_epoch;
  const active = () => !state.suspended && !env.document.hidden && env.window.navigator?.onLine !== false;

  function status(ctx) {
    if (current(ctx)) notice("human-read-notice", ctx.paused ? "阅读状态暂未同步，阅读确认已暂停。服务恢复后重新打开会话可重试。"
      : ctx.readError || ctx.writeError ? "阅读状态暂未同步，正在重试；聊天可继续。" : "");
  }

  function reset() {
    if (context?.key === key()) return;
    if (context) {
      clearTimeout(context.timer);
      for (const cancel of context.requests) cancel();
    }
    context = state.info ? { key: key(), installation: state.installationID || "", info: { ...state.info }, operator: state.operator,
      confirmedSeen: new Set(), pendingSeen: new Map(), revision: -1, snapshot: 0, cleared: 0,
      requests: new Set(), timer: null, stateTask: null, confirmTask: null, batch: null,
      readFailures: 0, writeFailures: 0, nextState: 0, nextConfirm: 0, paused: false } : null;
    notice("human-read-notice", "");
  }

  function prune(ctx) {
    ctx.cleared = Math.max(ctx.cleared, state.clearedThrough);
    for (const seq of ctx.confirmedSeen) if (seq <= ctx.cleared) ctx.confirmedSeen.delete(seq);
    for (const seq of ctx.pendingSeen.keys()) if (seq <= ctx.cleared || ctx.confirmedSeen.has(seq)) ctx.pendingSeen.delete(seq);
    if (ctx.batch && !ctx.batch.seen_seqs.some(seq => ctx.pendingSeen.has(seq))) ctx.batch = null;
  }

  function accept(ctx, result) {
    if (!current(ctx) || result.room_id !== ctx.info.room_id || result.room_instance !== ctx.info.room_instance ||
      result.operator_id !== ctx.operator || !Number.isSafeInteger(result.revision) || result.revision < 0 ||
      !Number.isSafeInteger(result.snapshot_seq) || !Number.isSafeInteger(result.cleared_through) ||
      result.cleared_through < 0 || result.cleared_through > result.snapshot_seq ||
      !Array.isArray(result.seen_seqs) || result.seen_seqs.length > 10000 ||
      result.seen_seqs.some(seq => !Number.isSafeInteger(seq) || seq <= 0 || seq > result.snapshot_seq)) throw new Error("阅读状态响应无效");
    // Ignore old polls, including polls overtaken by a message clear/refresh.
    if (result.revision < ctx.revision || result.snapshot_seq < Math.max(ctx.snapshot, state.lastSeq) ||
      result.cleared_through < Math.max(ctx.cleared, state.clearedThrough)) return false;
    ctx.revision = result.revision; ctx.snapshot = result.snapshot_seq; ctx.cleared = result.cleared_through;
    ctx.summary = { ...result, status: "ready" };
    for (const seq of result.seen_seqs) ctx.confirmedSeen.add(seq);
    prune(ctx);
    if (!ctx.pendingSeen.size) { ctx.writeError = false; ctx.writeFailures = 0; ctx.nextConfirm = 0; }
    changed();
    return true;
  }

  // Bound even an unresponsive transport; cancelling/timeout discards late data.
  function request(ctx, path, data = {}) {
    return new Promise((resolve, reject) => {
      const controller = new AbortController();
      let done = false;
      const finish = (error, result) => {
        if (done) return;
        done = true; clearTimeout(timer); ctx.requests.delete(cancel);
        if (error) reject(error); else resolve(result);
      };
      const cancel = () => { controller.abort(); finish(new Error("阅读同步已取消")); };
      const timer = setTimeout(() => { controller.abort(); finish(new Error("阅读同步超时")); }, 5000);
      ctx.requests.add(cancel);
      Promise.resolve().then(() => done ? undefined : env.request(path, { ...ctx.info, caller: { kind: "human", id: ctx.operator }, ...data },
        undefined, 5000, controller.signal)).then(result => finish(null, result), error => finish(error));
    });
  }

  function failed(ctx, error, write) {
    if (!current(ctx)) return;
    if (["human_read_unavailable", "identity", "request", "invalid"].includes(error.code)) ctx.paused = true;
    // A state poll may already have verified an uncertain/lost receipt.
    if (write && !ctx.pendingSeen.size && !ctx.paused) { status(ctx); return; }
    if (write) {
      ctx.writeError = true;
      ctx.nextConfirm = performance.now() + Math.min(30000, 1000 * 2 ** Math.min(ctx.writeFailures++, 5));
    } else {
      ctx.readError = true;
      ctx.nextState = performance.now() + Math.min(30000, 1000 * 2 ** Math.min(ctx.readFailures++, 5));
    }
    status(ctx);
  }

  function schedule(ctx, delay = 300) {
    if (!current(ctx) || ctx.timer !== null || ctx.confirmTask || ctx.paused || !ctx.pendingSeen.size) return;
    ctx.timer = setTimeout(() => { ctx.timer = null; void confirm(ctx); }, Math.max(delay, ctx.nextConfirm - performance.now()));
  }

  async function confirm(ctx) {
    if (!current(ctx) || !active() || ctx.paused || ctx.confirmTask) return;
    prune(ctx);
    if (!ctx.pendingSeen.size) return;
    if (!ctx.batch) {
      const entries = [...ctx.pendingSeen].slice(0, 100);
      ctx.batch = { snapshot_seq: Math.max(...entries.map(([, snapshot]) => snapshot)), seen_seqs: entries.map(([seq]) => seq) };
    }
    const batch = ctx.batch;
    ctx.confirmTask = request(ctx, "/v1/human-read/confirm", batch);
    try {
      const result = await ctx.confirmTask;
      if (!current(ctx)) return;
      if (accept(ctx, result)) {
        ctx.writeError = false; ctx.writeFailures = 0; ctx.nextConfirm = 0;
        status(ctx);
        // Refresh the existing catalog/message path after a durable receipt.
        void refresh();
      } else {
        // Recheck an overtaken response on the next batch, without a tight loop.
        ctx.nextConfirm = performance.now() + 1000;
      }
    } catch (error) { failed(ctx, error, true); }
    finally {
      ctx.confirmTask = null;
      schedule(ctx);
    }
  }

  function poll() {
    reset();
    const ctx = context;
    if (!ctx || !active()) return;
    prune(ctx); schedule(ctx);
    if (ctx.stateTask || performance.now() < ctx.nextState) return;
    ctx.stateTask = request(ctx, "/v1/human-read/state").then(result => {
      if (!current(ctx)) return;
      if (accept(ctx, result)) { ctx.readError = false; ctx.readFailures = 0; ctx.nextState = 0; }
      status(ctx);
    }).catch(error => failed(ctx, error, false)).finally(() => { ctx.stateTask = null; });
  }

  function seen(seq) {
    const ctx = context;
    return !!ctx && current(ctx) && (seq <= Math.max(ctx.cleared, state.clearedThrough) || ctx.confirmedSeen.has(seq) || ctx.pendingSeen.has(seq));
  }

  function observe(seq) {
    const ctx = context;
    if (!ctx || !current(ctx) || seen(seq) || seq <= state.clearedThrough || seq > state.lastSeq) return false;
    ctx.pendingSeen.set(seq, state.lastSeq);
    schedule(ctx);
    return true;
  }

  return { reset, poll, seen, observe, confirmed: seq => !!context && current(context) && context.confirmedSeen.has(seq),
    summary: () => context && current(context) ? context.summary : null,
    prune: () => { if (context) prune(context); } };
}
