const screen = document.getElementById("screen"), status = document.getElementById("status");
const keyboard = document.getElementById("keyboard"), keyboardButton = document.getElementById("keyboard-toggle");
let RFB, rfb, active = true, mode = "", interactive = false;
const base = new URL("./", location.href);
window.addEventListener("pagehide", () => { active = false; rfb?.disconnect(); });
function report(state, text) {
  status.textContent = text; status.hidden = state === "connected";
  parent.postMessage({type:"teamx-environment", state, interactive}, location.origin);
}
function connect(control = null) {
  if (!active || !RFB) return;
  const nextMode = control ? control.control_id : "view";
  if (mode === nextMode) return;
  mode = nextMode; const previous = rfb; rfb = null; previous?.disconnect(); screen.replaceChildren();
  const socket = new URL("websockify", base); socket.protocol = location.protocol === "https:" ? "wss:" : "ws:";
  if (control) { socket.searchParams.set("control_id", control.control_id); socket.searchParams.set("client_id", control.client_id); }
  interactive = !!control; keyboardButton.hidden = !interactive; keyboard.value = "";
  const current = new RFB(screen, socket.href, {shared:true}); rfb = current;
  current.viewOnly = !interactive; current.scaleViewport = true; current.resizeSession = false;
  current.addEventListener("connect", () => { if (rfb === current) report("connected", ""); });
  current.addEventListener("disconnect", () => { if (active && rfb === current) { interactive = false; keyboardButton.hidden = true; report("disconnected", "连接已断开，请点击面板中的重新连接。"); } });
  current.addEventListener("credentialsrequired", () => { report("unavailable", "电脑认证暂不可用。"); current.disconnect(); });
  current.addEventListener("securityfailure", () => report("unavailable", "电脑认证暂不可用。"));
}
window.addEventListener("message", event => {
  if (event.source !== parent || event.origin !== location.origin || event.data?.type !== "teamx-environment-mode") return;
  const control = event.data;
  if (control.interactive && /^[a-f0-9]{32}$/.test(control.control_id) && /^[a-f0-9]{32}$/.test(control.client_id)) connect(control);
  else connect();
});
function sendText(text) {
  if (!interactive || !rfb) return;
  for (const char of text) { const point = char.codePointAt(0); rfb.sendKey(point <= 255 ? point : 0x01000000 | point); }
}
keyboardButton.addEventListener("click", () => keyboard.focus());
keyboard.addEventListener("input", event => { if (!event.isComposing) { sendText(keyboard.value); keyboard.value = ""; } });
keyboard.addEventListener("compositionend", () => { sendText(keyboard.value); keyboard.value = ""; });
keyboard.addEventListener("keydown", event => {
  const key = {Backspace:0xff08, Enter:0xff0d, Tab:0xff09}[event.key];
  if (key && interactive) { event.preventDefault(); rfb.sendKey(key); }
});
keyboard.addEventListener("beforeinput", event => { if (event.inputType === "deleteContentBackward" && interactive) { event.preventDefault(); rfb.sendKey(0xff08); } });
try { ({default:RFB} = await import(new URL("vnc/core/rfb.js", base).href)); connect(); }
catch { report("unavailable", "电脑暂不可用，请点击面板中的重新连接。"); }
