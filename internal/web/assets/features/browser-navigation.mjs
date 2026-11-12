// A transient history entry keeps browser Back inside the current conversation
// while a dialog or a phone sidebar is open. No form data enters history.
export function createBrowserNavigation({ env, layers }) {
  const { window } = env;
  let current = null, settling = false;
  function sync() {
    if (!window.history || settling) return;
    const next = layers().at(-1);
    if (next?.id === current?.id) return;
    if (current) {
      settling = true;
      window.history.back();
    } else if (next) {
      current = next;
      window.history.pushState({ teamxOverlay: next.id }, "");
    }
  }
  function mount() {
    // A reloaded transient entry must not restore a platform operation or form.
    if (window.history?.state?.teamxOverlay) window.history.replaceState(null, "");
    window.addEventListener("popstate", async () => {
      if (!current) return;
      const closing = current;
      current = null;
      if (!settling) {
        settling = true;
        await closing.close();
      }
      settling = false;
      sync();
    });
    if (env.MutationObserver) new env.MutationObserver(sync).observe(env.document.body, {
      attributes: true, subtree: true, attributeFilter: ["open", "hidden", "class"]
    });
  }
  return { mount, sync };
}
