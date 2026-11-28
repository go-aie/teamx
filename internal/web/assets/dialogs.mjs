import { createNavigationIcon } from "./icons.mjs";

// Only a primary press and click that both land on the backdrop dismiss a
// dialog. Dragging or selecting from inside the dialog must not close it.
export function dismissOnBackdrop(dialog, close) {
  let pressedOutside = false;
  const outside = event => {
    const rect = dialog.getBoundingClientRect();
    return event.target === dialog && (event.clientX < rect.left || event.clientX >= rect.right ||
      event.clientY < rect.top || event.clientY >= rect.bottom);
  };
  dialog.addEventListener("pointerdown", event => {
    pressedOutside = event.button === 0 && event.isPrimary !== false && outside(event);
  });
  dialog.addEventListener("pointercancel", () => { pressedOutside = false; });
  dialog.addEventListener("close", () => { pressedOutside = false; });
  dialog.addEventListener("click", event => {
    const dismiss = pressedOutside && outside(event);
    pressedOutside = false;
    if (dialog.open && dismiss) return close();
  });
}

export function addDialogClose({ document, dialog, header, canClose = () => true, onClose = () => dialog.close() }) {
  const button = document.createElement("button"); button.type = "button"; button.className = "icon-button";
  button.dataset.dialogClose = "true"; button.setAttribute("aria-label", "关闭"); button.title = "关闭";
  button.append(createNavigationIcon(document, "close")); header.append(button);
  const close = () => { if (canClose()) onClose(); };
  button.addEventListener("click", close);
  dialog.addEventListener("cancel", event => { event.preventDefault(); close(); });
  dismissOnBackdrop(dialog, close);
  return {button, close};
}
