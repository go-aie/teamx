import { createSessionController } from "./session.mjs";

export function mountAuthenticationRecovery({ document = globalThis.document, location = globalThis.location,
  fetcher = globalThis.fetch, ...options } = {}) {
  const controller = createSessionController({ ...options, document, location,
    read: async () => {
      const response = await fetcher("/v1/access/session", { cache:"no-store", signal:AbortSignal.timeout(45000) });
      const result = await response.json();
      if (!response.ok) {
        const error = new Error(); error.status = response.status; error.code = result.code || result.error; throw error;
      }
      location.reload();
      return result;
    } });
  document.getElementById("authentication-retry").addEventListener("click", () => { void controller.check().catch(() => {}); });
  controller.start();
  return controller;
}

if (globalThis.document?.getElementById("authentication-retry")) {
  const controller = mountAuthenticationRecovery();
  addEventListener("pagehide", () => controller.stop(), { once:true });
  addEventListener("pageshow", () => controller.start());
}
