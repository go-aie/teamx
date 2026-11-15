// Human reading is independent of message/Agent refreshes and browser storage.
// Opened positions and viewed mentions merge independently; observations remain pending.
export function createHumanRead({ env, runtime, changed, canConfirm = () => true }) {
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

  function reset({ force = false } = {}) {
    if (!force && context?.key === key()) return;
    if (context) {
      clearTimeout(context.timer);
      for (const cancel of context.requests) cancel();
    }
    context = state.info ? { key: key(), installation: state.installationID || "", info: { ...state.info }, operator: state.operator,
      confirmedSeen: new Set(), pendingSeen: new Map(), confirmedOpened: 0, pendingOpened: null, revision: -1, snapshot: 0, cleared: 0,
      requests: new Set(), timer: null, stateTask: null, confirmTask: null, batch: null,
      readFailures: 0, writeFailures: 0, nextState: 0, nextConfirm: 0, paused: false } : null;
    notice("human-read-notice", "");
  }

  const hasPending = ctx => !!ctx.pendingOpened || ctx.pendingSeen.size > 0;

  function prune(ctx) {
    ctx.cleared = Math.max(ctx.cleared, state.clearedThrough);
    ctx.confirmedOpened = Math.max(ctx.confirmedOpened, ctx.cleared);
    if (ctx.pendingOpened?.through <= ctx.confirmedOpened) ctx.pendingOpened = null;
    for (const seq of ctx.confirmedSeen) if (seq <= ctx.cleared) ctx.confirmedSeen.delete(seq);
    for (const seq of ctx.pendingSeen.keys()) if (seq <= ctx.cleared || ctx.confirmedSeen.has(seq)) ctx.pendingSeen.delete(seq);
    if (ctx.batch && !(ctx.batch.opened_through > ctx.confirmedOpened) &&
      !(ctx.batch.mention_seen_seqs || []).some(seq => ctx.pendingSeen.has(seq))) ctx.batch = null;
  }

  function validate(ctx, result, includeNew = false) {
    const integer = n => Number.isSafeInteger(n) && n >= 0;
    const sequences = (seqs, floor = result.cleared_through) => Array.isArray(seqs) && seqs.length <= 10000 &&
      seqs.every((seq, i) => integer(seq) && seq > floor && seq <= result.snapshot_seq && (!i || seq > seqs[i - 1]));
    if (!current(ctx) || result.version !== 2 || result.room_id !== ctx.info.room_id || result.room_instance !== ctx.info.room_instance ||
      result.operator_id !== ctx.operator || !integer(result.revision) || !integer(result.snapshot_seq) ||
      !integer(result.cleared_through) || result.cleared_through > result.snapshot_seq ||
      !integer(result.opened_through) || result.opened_through < result.cleared_through || result.opened_through > result.snapshot_seq ||
      !sequences(result.mention_seen_seqs) || !sequences(result.unseen_mention_seqs) ||
      !integer(result.unread_count) || result.unread_count > 10000 || result.unread_count < result.mention_count ||
      result.mention_count !== result.unseen_mention_seqs.length ||
      (includeNew && !sequences(result.new_message_seqs, result.opened_through))) throw new Error("阅读状态响应无效");
    const seen = new Set(result.mention_seen_seqs);
    if (result.unseen_mention_seqs.some(seq => seen.has(seq))) throw new Error("阅读状态响应无效");
  }

  function accept(ctx, result) {
    validate(ctx, result);
    // Shared summaries may not regress behind message refreshes or newer receipts.
    if (result.revision < ctx.revision || result.snapshot_seq < Math.max(ctx.snapshot, state.lastSeq) ||
      result.cleared_through < Math.max(ctx.cleared, state.clearedThrough) || result.opened_through < ctx.confirmedOpened) return false;
    ctx.revision = result.revision; ctx.snapshot = result.snapshot_seq; ctx.cleared = result.cleared_through;
    ctx.confirmedOpened = Math.max(ctx.confirmedOpened, result.opened_through);
    const { new_message_seqs, ...summary } = result;
    ctx.summary = { ...summary, status: "ready" };
    for (const seq of result.mention_seen_seqs) ctx.confirmedSeen.add(seq);
    prune(ctx);
    if (!hasPending(ctx)) { ctx.writeError = false; ctx.writeFailures = 0; ctx.nextConfirm = 0; }
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
    if (write && !hasPending(ctx) && !ctx.paused) { status(ctx); return; }
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
    if (!current(ctx) || ctx.timer !== null || ctx.confirmTask || ctx.paused || !hasPending(ctx)) return;
    ctx.timer = setTimeout(() => { ctx.timer = null; void confirm(ctx); }, Math.max(delay, ctx.nextConfirm - performance.now()));
  }

  async function confirm(ctx) {
    if (!current(ctx) || !active() || !canConfirm() || ctx.paused || ctx.confirmTask) return;
    prune(ctx);
    if (!hasPending(ctx)) return;
    if (!ctx.batch) {
      const entries = [...ctx.pendingSeen].slice(0, 100);
      ctx.batch = { snapshot_seq: Math.max(ctx.pendingOpened?.snapshot || 0, ...entries.map(([, snapshot]) => snapshot)) };
      if (entries.length) ctx.batch.mention_seen_seqs = entries.map(([seq]) => seq);
      if (ctx.pendingOpened) ctx.batch.opened_through = ctx.pendingOpened.through;
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

  // Entry metadata keeps its own fixed K even when messages have reached K+1.
  // The conversation owns entry generations and the temporary review set.
  async function entryState() {
    reset();
    const ctx = context;
    if (!ctx || !active() || performance.now() < ctx.nextState) return null;
    try {
      const result = await request(ctx, "/v1/human-read/state", { include_new_messages: true });
      if (!current(ctx)) return null;
      validate(ctx, result, true);
      if (result.cleared_through < Math.max(ctx.cleared, state.clearedThrough)) return null;
      accept(ctx, result);
      ctx.readError = false; ctx.readFailures = 0; ctx.nextState = 0; status(ctx);
      return result;
    } catch (error) { failed(ctx, error, false); return null; }
  }

  // These observations concern mentions only. Ordinary review is page-local.
  function seen(seq) {
    const ctx = context;
    return !!ctx && current(ctx) && (seq <= Math.max(ctx.cleared, state.clearedThrough) || ctx.confirmedSeen.has(seq) || ctx.pendingSeen.has(seq));
  }

  function observe(seq) {
    const ctx = context;
    if (!ctx || !current(ctx) || !Number.isSafeInteger(seq) || seen(seq) || seq <= state.clearedThrough || seq > state.lastSeq) return false;
    ctx.pendingSeen.set(seq, state.lastSeq);
    schedule(ctx);
    return true;
  }

  function openThrough(snapshot) {
    const ctx = context;
    if (!ctx || !current(ctx) || !Number.isSafeInteger(snapshot) || snapshot <= Math.max(ctx.confirmedOpened, ctx.pendingOpened?.through || 0, state.clearedThrough) ||
      snapshot > state.lastSeq) return false;
    ctx.pendingOpened = { through: snapshot, snapshot };
    schedule(ctx);
    return true;
  }

  return { reset, poll, entryState, openThrough, seen, observe,
    openedThrough: () => context && current(context) ? Math.max(context.confirmedOpened, context.pendingOpened?.through || 0, state.clearedThrough) : 0,
    confirmedOpenedThrough: () => context && current(context) ? Math.max(context.confirmedOpened, state.clearedThrough) : 0,
    confirmed: seq => !!context && current(context) && context.confirmedSeen.has(seq),
    summary: () => context && current(context) ? context.summary : null,
    prune: () => { if (context) prune(context); } };
}
