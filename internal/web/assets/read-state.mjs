// Reads stay quiet briefly; only settled failures need recovery actions.
// A newer read owns the indicator, even if an older request finishes later.
export function beginRead(env, owner, key, render) {
  // Native browser timers must not receive the environment object as `this`.
  const { setTimeout, clearTimeout } = env;
  clearTimeout(owner[key]?.timer);
  const state = owner[key] = { pending: true, slow: false, failed: !!owner[key]?.failed };
  state.timer = setTimeout(() => {
    if (owner[key] !== state) return;
    state.slow = true;
    render();
  }, 350);
  render();
  return (failed = false) => {
    clearTimeout(state.timer);
    if (owner[key] !== state) return;
    state.pending = false; state.slow = false; state.failed = failed;
    render();
  };
}

export function readState(...reads) {
  return {
    pending: reads.some(read => !read || read.pending),
    slow: reads.some(read => read?.pending && read.slow),
    failed: reads.some(read => read?.failed),
  };
}
