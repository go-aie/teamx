// Shared human avatar for the user menu and conversation messages.
export function createUserIcon(document) {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  for (const [name, value] of Object.entries({ viewBox: "0 0 24 24", class: "user-icon", "aria-hidden": "true", focusable: "false" })) svg.setAttribute(name, value);
  const head = document.createElementNS("http://www.w3.org/2000/svg", "circle");
  for (const [name, value] of Object.entries({ cx: "12", cy: "8", r: "4" })) head.setAttribute(name, value);
  const body = document.createElementNS("http://www.w3.org/2000/svg", "path");
  body.setAttribute("d", "M5 21v-2a7 7 0 0 1 14 0v2");
  svg.append(head, body);
  return svg;
}

// Shared assistant mark for navigation, messages and member details.
export function createAssistantIcon(document) {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("class", "assistant-icon");
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("focusable", "false");
  const head = document.createElementNS("http://www.w3.org/2000/svg", "rect");
  for (const [name, value] of Object.entries({x:5, y:6, width:14, height:13, rx:3})) head.setAttribute(name, String(value));
  const details = document.createElementNS("http://www.w3.org/2000/svg", "path");
  details.setAttribute("d", "M12 3v3M3 10v5m18-5v5M9 10v1m6-1v1m-6 4h6");
  svg.append(head, details);
  return svg;
}

// Shared geometry for directional navigation and closing a surface.
export function createNavigationIcon(document, direction = "right") {
  const paths = {left:"m15 6-6 6 6 6", right:"m9 6 6 6-6 6", down:"m6 9 6 6 6-6", close:"m6 6 12 12M6 18 18 6"};
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  for (const [key, value] of Object.entries({viewBox:"0 0 24 24", class:"navigation-icon", "aria-hidden":"true", focusable:"false"})) svg.setAttribute(key, value);
  const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
  path.setAttribute("d", paths[direction]); svg.append(path);
  return svg;
}
