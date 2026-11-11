// One shared, cancellable confirmation surface for destructive operations.
export function createConfirmation(document) {
  const $ = id => document.getElementById(id), dialog = $("confirmation-dialog");
  let pending = null;
  function finish(accepted) {
    if (!pending) return;
    const { resolve, opener } = pending;
    pending = null;
    dialog.close();
    if (opener?.isConnected !== false) opener?.focus();
    resolve(accepted);
  }
  $("confirmation-accept").addEventListener("click", () => finish(true));
  $("confirmation-cancel").addEventListener("click", () => finish(false));
  $("confirmation-close").addEventListener("click", () => finish(false));
  dialog.addEventListener("cancel", event => { event.preventDefault(); finish(false); });
  dialog.addEventListener("close", () => { if (!dialog.open) finish(false); });
  return ({ title, message, confirmLabel = "确认", danger = true }) => {
    if (pending) return Promise.resolve(false);
    $("confirmation-title").textContent = title;
    $("confirmation-message").textContent = message;
    $("confirmation-accept").textContent = confirmLabel;
    $("confirmation-accept").classList.toggle("danger-button", danger);
    return new Promise(resolve => {
      pending = { resolve, opener: document.activeElement };
      dialog.showModal();
      $("confirmation-cancel").focus();
    });
  };
}
