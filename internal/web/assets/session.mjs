// One controller per document. It only retries authentication reads/renewals;
// business writes are never replayed after an authentication failure.
export const temporaryAuthentication = code => ["authentication_temporarily_unavailable", "session_storage_unavailable"].includes(code);
export const authenticationMessage = "暂时无法验证登录，正在重试";
export function createSessionController({ read, renew, document = globalThis.document, location = globalThis.location,
  storage, now = Date.now, setTimer = setTimeout, clearTimer = clearTimeout, changed = () => {}, beforeLogin = () => {} } = {}) {
  let metadata, pending, timer, stopped = true, unavailable = false, navigating = false, failures = 0;
  const recoveryKey = "teamx.login-recovery";
  const visible = () => document?.visibilityState !== "hidden" && !document?.hidden;
  function schedule(delay) {
    clearTimer(timer); timer = undefined;
    if (stopped || !visible() || navigating || !metadata?.renewal_supported && !unavailable) return;
    const when = Date.parse(metadata?.renew_after);
    const until = Date.parse(metadata?.usable_until || metadata?.verified_until);
    // Recheck at the authorization boundary even if Retry-After runs past it.
    const next = Math.min(when, Number.isFinite(until) ? until : Infinity);
    timer = setTimer(() => { timer = undefined; void check().catch(() => {}); }, delay ?? Math.max(1000, next - now()));
  }
  function setUnavailable(value) { if (unavailable !== value) { unavailable = value; changed(value); } }
  function accept(value) {
    if (!value?.operator || !value?.installation_id) return;
    metadata = value; failures = 0; setUnavailable(false); schedule();
  }
  function login() {
    if (navigating || location?.pathname?.endsWith("/access")) return;
    navigating = true; clearTimer(timer); beforeLogin();
    // Store only a bounded local application route and confirmed identity.
    const path = location?.pathname || "/";
    if (metadata && /^\/(?:s\/[a-f0-9]{32}\/)?$/.test(path)) try {
      storage?.setItem(recoveryKey, JSON.stringify({ installation: metadata.installation_id, operator: metadata.operator, path,
        hash: location.hash === "#platform-settings" ? location.hash : "" }));
    } catch {}
    location?.assign?.("/access");
  }
  function restore(value) {
    let saved;
    try { saved = JSON.parse(storage?.getItem(recoveryKey)); storage?.removeItem(recoveryKey); } catch { return false; }
    if (saved?.installation !== value?.installation_id || saved?.operator !== value?.operator ||
      typeof saved?.path !== "string" || !/^\/s\/[a-f0-9]{32}\/$/.test(saved.path)) return false;
    location?.replace?.(saved.path + (saved.hash === "#platform-settings" ? saved.hash : "")); return true;
  }
  const sessionPath = path => /\/v1\/access\/session(?:\/renew)?$/.test(path);
  function failed(error, sessionCheck) {
    // A failed background check does not invalidate an authorization the host
    // already granted. Business rejections and storage failures still fence UI.
    const deadline = Math.min(Date.parse(metadata?.usable_until || metadata?.verified_until),
      metadata?.absolute_expires_at ? Date.parse(metadata.absolute_expires_at) : Infinity);
    const transient = !error.status || error.status === 503 && error.code === "authentication_temporarily_unavailable";
    const usable = sessionCheck && transient && now() < deadline;
    if (!usable) setUnavailable(true);
    let delay = [1000, 5000, 15000, 30000][Math.min(failures++, 3)];
    if (usable) delay = Math.min(delay, deadline - now());
    schedule(delay);
  }
  function observe(status, result, path) {
    if (status === 401) { login(); return; }
    if (status === 503 && temporaryAuthentication(result?.code || result?.error)) {
      // check() owns its error handling; do not count the same failure twice.
      if (!pending || !sessionPath(path)) failed({status, code:result?.code || result?.error}, sessionPath(path));
      return;
    }
    if (status >= 200 && status < 300 && sessionPath(path)) accept(result);
  }
  function check() {
    if (pending) return pending;
    // Install pending before calling a reader, including synchronous fixtures.
    pending = Promise.resolve().then(() => metadata?.renewal_supported ? renew() : read()).then(value => { accept(value); return value; }, error => {
      if (error.status === 401) login();
      else failed(error, true);
      throw error;
    }).finally(() => { pending = undefined; });
    return pending;
  }
  function visibility() {
    if (!visible()) { clearTimer(timer); timer = undefined; }
    else if (!stopped) void check().catch(() => {});
  }
  return {
    observe, accept, check, login, restore,
    get unavailable() { return unavailable; },
    beforeRequest(path, payload) {
      if (payload && unavailable && !/\/v1\/access\/(?:session\/renew|logout|login|sso\/login)$/.test(path)) {
        const error = new Error(authenticationMessage); error.status = 503; error.code = "authentication_temporarily_unavailable"; throw error;
      }
    },
    start(checkNow = true) { if (!stopped) return; stopped = false; document?.addEventListener?.("visibilitychange", visibility); if (checkNow) void check().catch(() => {}); else schedule(); },
    stop() { stopped = true; clearTimer(timer); document?.removeEventListener?.("visibilitychange", visibility); },
  };
}
let controller;
export function installSessionController(options) { controller?.stop(); controller = createSessionController(options); controller.start(options.readOnStart !== false); return controller; }
export function observeAuthentication(status, result, path) { controller?.observe(status, result, path); }
export function beforeAuthenticatedRequest(path, payload) { controller?.beforeRequest(path, payload); }
export function requireLogin() {
  if (controller) controller.login();
  else if (!globalThis.location?.pathname?.endsWith("/access")) globalThis.location?.assign?.("/access");
}
