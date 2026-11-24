import { spaceURL } from "./space-context.mjs";
// HTTP observation scheduling only. Transport/lifecycle ownership is installed
// separately; creating a scheduler does not open a stream or start a poll.
export const updateIntervals = Object.freeze({
  calendar: 1000, messages: 500, members: 1000, human_read: 1000, catalog: 1500,
  activity: 1500, inbox: 1500, notifications: 2000, models: 1000,
  workspace: 1000, interactions: 1000, authorization: 1000,
  settings: 1000, platforms: 1000, tools: 1000, policy: 1000,
});

export function createUpdateScheduler(env) {
  const { setTimeout, clearTimeout } = env;
  const resources = new Set();
  const now = () => env.performance.now();
  let generation = 0, paused = false;

  function resource(topic, { scope = () => "", enabled = () => true, read,
    apply = () => {}, onError = () => {}, needed = () => true, interval = updateIntervals[topic] } = {}) {
    if (!Number.isFinite(interval) || interval < 0) throw new Error(`Unknown update topic: ${topic}`);
    let identity, dirty = 0, clean = 0, write = 0, pending, timer = null;
    let runningContext, runningRevision, runningWrite;
    let lastStart = -Infinity, retryAt = 0, failures = 0, urgent = false, value;
    let disposed = false, blocked = false;
    const cancelTimer = () => { if (timer !== null) clearTimeout(timer); timer = null; };
    function context() {
      const next = JSON.stringify([generation, scope()]);
      if (identity !== next) {
        identity = next; dirty = clean = 0; write++; urgent = false;
        value = undefined; blocked = false; lastStart = -Infinity; retryAt = failures = 0; cancelTimer();
      }
      return next;
    }
    function allowed() { return !disposed && !paused && enabled(); }
    function schedule() {
      // A consumer may defer diagnostic-only dirty state while showing a
      // summary. Explicit entry/recovery still owns its pending serial read.
      if (!allowed() || (!urgent && !needed()) || blocked || pending || dirty === clean || timer !== null) return;
      const delay = urgent ? 0 : Math.max(lastStart + interval, retryAt) - now();
      if (delay <= 0) { void start().catch(() => {}); return; }
      timer = setTimeout(() => {
        timer = null;
        context();
        schedule();
      }, delay);
    }
    function start() {
      const captured = context(), revision = dirty, writeVersion = write;
      if (!allowed()) return Promise.resolve();
      urgent = false; cancelTimer();
      runningContext = captured; runningRevision = revision; runningWrite = writeVersion;
      const current = () => allowed() && captured === JSON.stringify([generation, scope()]) && writeVersion === write;
      // Install pending before invoking read, including synchronous readers.
      pending = Promise.resolve().then(() => {
        if (!current()) return;
        lastStart = now();
        return read({ current, onFailure: error => { throw error; } });
      }).then(result => {
        if (!current()) return;
        apply(result); value = result; clean = revision; failures = 0; retryAt = 0;
        return result;
      }).catch(error => {
        if (current()) {
          retryAt = now() + Math.min(30000, 1000 * 2 ** Math.min(failures++, 5));
          // Identity/permission failures stop this observation until explicit
          // entry/recovery. Other resources keep working.
          if (["identity", "forbidden", "unauthorized", "request", "invalid"].includes(error.code)) blocked = true;
          onError(error);
        }
        throw error;
      }).finally(() => {
        pending = null;
        context();
        schedule();
      });
      return pending;
    }
    const api = {
      topic,
      // All ordinary notifications, including terminal/error notifications,
      // use this path. They never gain the explicit-read exception.
      invalidate() { context(); dirty++; schedule(); },
      refresh() {
        context();
        if (!allowed()) { dirty++; return Promise.resolve(); }
        blocked = false;
        if (pending) {
          if (runningContext === identity && dirty === runningRevision && runningWrite === write) return pending;
          // A new context or a post-write observation must wait for the old
          // transport to settle. Repeated explicit requests share that round.
          if (dirty === clean) dirty++;
          urgent = true;
          return pending.catch(() => {}).then(() => pending || value);
        }
        dirty++; urgent = true;
        return start();
      },
      // Apply an authoritative command receipt and fence reads started before
      // the write. Uncertain writes call supersede() then explicitly refresh
      // their original operation; the scheduler never executes commands.
      supersede(result) {
        context(); write++; cancelTimer(); urgent = false;
        clean = dirty;
        if (result !== undefined) { apply(result); value = result; }
        else value = undefined;
      },
      wake() { context(); schedule(); },
      get value() { context(); return value; },
      dispose() { disposed = true; cancelTimer(); resources.delete(api); },
      reset() { identity = undefined; context(); },
    };
    resources.add(api);
    return api;
  }
  return {
    resource,
    // An owner calls reset when replacing a stream/view. Old callbacks must
    // carry the returned token; rejecting one never dirties the new view.
    reset() { generation++; for (const item of resources) item.reset(); return generation; },
    get generation() { return generation; },
    invalidate(topic, token = generation) {
      if (token !== generation) return;
      for (const item of resources) if (item.topic === topic) item.invalidate();
    },
    pause() { paused = true; generation++; for (const item of resources) item.reset(); },
    resume() { paused = false; for (const item of resources) item.wake(); },
    dispose() { for (const item of [...resources]) item.dispose(); },
  };
}

// Components share the exact same in-flight HTTP result. A confirmed result
// can also be reused by a later consumer until invalidated. There is no timer;
// automatic retries/frequency belong to the resource scheduler above.
export function createSharedRead({ scope, read }) {
  let pending, pendingRevision, confirmed, identity, revision = 0;
  function context() {
    const next = JSON.stringify(scope());
    if (next !== identity) { identity = next; revision++; pending = confirmed = undefined; }
    return next;
  }
  const api = {
    read({ cached = false } = {}) {
      const captured = context(), version = revision;
      if (pending) {
        if (pendingRevision === version) return pending;
        return pending.catch(() => {}).then(() => api.read({ cached }));
      }
      if (cached && confirmed !== undefined) return Promise.resolve(confirmed);
      let response;
      try { response = read(); } catch (error) { response = Promise.reject(error); }
      const task = Promise.resolve(response).then(result => {
        if (captured !== JSON.stringify(scope()) || version !== revision) {
          throw Object.assign(new Error("读取上下文已变化"), { code: "stale_read" });
        }
        confirmed = result;
        return result;
      }).catch(error => {
        if (captured === JSON.stringify(scope()) && version === revision) confirmed = undefined;
        throw error;
      }).finally(() => { if (pending === task) pending = undefined; });
      pending = task; pendingRevision = version;
      return task;
    },
    invalidate() { context(); revision++; confirmed = undefined; },
    get value() { context(); return confirmed; },
  };
  return api;
}

// One owner for the page's native EventSource. A new hello always reconciles
// HTTP state; neither transport errors nor recovery execute business commands.
export function createUpdateConnection({ env, identity, reconcile, invalidate, status, fence = () => {}, authenticate = () => Promise.resolve() }) {
  const { setTimeout, clearTimeout } = env;
  let source, key, generation = 0, stopped = true, timer, watchdog, failures = 0, connectionID;
  const clear = () => { clearTimeout(timer); clearTimeout(watchdog); timer = watchdog = undefined; };
  const snapshot = () => JSON.stringify(identity());
  function close() {
    generation++; source?.close(); source = undefined; connectionID = undefined; clear(); fence();
  }
  function retry() {
    if (stopped || timer !== undefined) return;
    const delay = Math.min(30000, Math.max(1000, 1000 * 2 ** Math.min(failures++, 5) * (0.8 + (env.random?.() ?? Math.random()) * 0.4)));
    timer = setTimeout(() => { timer = undefined; void recover(); }, delay);
  }
  async function recover() {
    const token = generation;
    try { await reconcile({ current: () => !stopped && token === generation }); } catch { /* Scheduler retains failed resources. */ }
    if (stopped || token !== generation) return;
    open();
  }
  function replace() { close(); status(false); retry(); }
  function heartbeat(token) {
    clearTimeout(watchdog);
    watchdog = setTimeout(() => { if (token === generation) replace(); }, 45000);
  }
  function open({ roomOnly = false } = {}) {
    if (stopped) return;
    const info = identity();
    if (!info?.service_epoch) { retry(); return; }
    close(); key = snapshot();
    const token = generation;
    const query = new URLSearchParams({ service_epoch: info.service_epoch });
    if (info.room_id) for (const field of ["room_id", "room_instance", "room_service_epoch"]) query.set(field, info[field]);
    try { source = new env.EventSource(spaceURL(`/v1/updates?${query}`)); }
    catch { replace(); return; }
    const owned = source;
    const valid = () => !stopped && token === generation && source === owned;
    heartbeat(token);
    for (const event of ["hello", "invalidate", "resync", "heartbeat"]) owned.addEventListener(event, message => {
      if (!valid()) return;
      let data; try { data = JSON.parse(message.data); } catch { replace(); return; }
      if (event === "hello") {
        if (data.protocol !== 1 || !data.connection_id || data.service_epoch !== info.service_epoch ||
            info.installation_id && data.installation_id !== info.installation_id) { replace(); return; }
        connectionID = data.connection_id; failures = 0; heartbeat(token);
        status(false);
        // Register-before-hello closes the bootstrap race. Notifications received
        // during reconciliation dirty the very same serialized HTTP readers.
        fence();
        const entering = roomOnly;
        roomOnly = false; // Native reconnects always perform full recovery.
        void Promise.resolve(reconcile({ roomOnly: entering, globalVersions: data.global_versions, current: valid })).then(() => {
          if (!valid()) return;
          if (key !== snapshot()) { open(); return; }
          status(true);
        }).catch(() => { if (valid()) replace(); });
        return;
      }
      if (!connectionID || data.connection_id !== connectionID) return;
      heartbeat(token);
      if (event === "resync") {
        fence(); status(false);
        void Promise.resolve(reconcile({ current: valid })).then(() => {
          if (!valid()) return;
          if (key !== snapshot()) open(); else status(true);
        }).catch(() => { if (valid()) replace(); });
      } else if (event === "invalidate") invalidate(data);
    });
    owned.onerror = () => {
      if (!valid()) return;
      status(false); fence(); connectionID = undefined;
      // EventSource owns ordinary reconnects. Authentication is checked through
      // the existing session endpoint; only its real 401 navigates to login.
      void Promise.resolve(authenticate()).catch(() => {});
      if (owned.readyState === 2) replace();
    };
  }
  return {
    start() { if (!stopped && source && key === snapshot()) return; stopped = false; open(); },
    ensure(options) {
      if (!stopped && key !== snapshot()) { open(options); return true; }
      return false;
    },
    reconnect() { if (!stopped) replace(); },
    stop() { stopped = true; close(); },
    get connected() { return !!connectionID; },
  };
}
