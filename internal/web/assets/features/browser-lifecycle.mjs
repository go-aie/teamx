// Browser lifetime controls display reads only. Server work and uncertain writes
// remain owned by their existing operation IDs; recovery never submits them.
export function createBrowserLifecycle({ env, suspend, resume, save }) {
  const { document, window, setInterval } = env;
  let away = false;
  const active = () => !away && !document.hidden && window.navigator?.onLine !== false;
  function update() {
    if (active()) void resume();
    else { save(); suspend(); }
  }
  return {
    active,
    mount() {
      setInterval(() => { if (active()) void resume(false); }, 1500);
      document.addEventListener("visibilitychange", update);
      window.addEventListener("offline", update);
      window.addEventListener("online", update);
      window.addEventListener("pagehide", () => { away = true; update(); });
      window.addEventListener("pageshow", () => { away = false; update(); });
    }
  };
}
