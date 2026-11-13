// Return state stays in memory: no form values or operations enter history.
export function captureReturnView({ document, window }, runtime, { opener = document.activeElement, valid = () => true, scroll = [] } = {}) {
  const context = () => JSON.stringify([runtime.state.installationID, runtime.state.operator,
    runtime.state.catalog?.service_epoch, runtime.state.info?.service_epoch,
    runtime.state.info?.room_id, runtime.state.info?.room_instance]);
  const identity = context();
  const positions = [...new Set(["timeline", "room-nav", "member-sidebar", "activity-dialog", "activity-body",
    "platforms-member-view", "policy-member-view", "inbox-member-view", "settings-panels",
    "notification-detail", "workspace-dialog", "settings-dialog", "member-rail", ...scroll])]
    .map(id => document.getElementById(id)).filter(Boolean)
    .map(element => ({ element, top: element.scrollTop, left: element.scrollLeft }));
  const page = { left: window.scrollX || 0, top: window.scrollY || 0 };
  return {
    current: () => identity === context() && valid(),
    restore(fallback) {
      if (identity !== context() || !valid()) return false;
      const available = element => element && element.isConnected !== false && !element.disabled &&
        !element.hidden && !element.closest?.('[hidden], [inert], [aria-hidden="true"], dialog:not([open])');
      (available(opener) ? opener : available(fallback) ? fallback : null)?.focus({ preventScroll: true });
      for (const { element, top, left } of positions) {
        if (element.isConnected === false) continue;
        element.scrollTop = top; element.scrollLeft = left;
      }
      window.scrollTo?.(page);
      return true;
    },
  };
}
