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

export function createImageToolIcon(document, action) {
  const paths = {
    zoomOut:"M10 17a7 7 0 1 0 0-14 7 7 0 0 0 0 14Zm5-2 6 6M7 10h6",
    zoomIn:"M10 17a7 7 0 1 0 0-14 7 7 0 0 0 0 14Zm5-2 6 6M7 10h6m-3-3v6",
    fit:"M8 3H3v5m13-5h5v5M3 16v5h5m13-5v5h-5M8 8h8v8H8Z",
    rotate:"M5 8a8 8 0 1 1-1 8M5 3v5h5",
    download:"M12 3v12m-5-5 5 5 5-5M4 17v4h16v-4",
  };
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  for (const [key, value] of Object.entries({viewBox:"0 0 24 24", "aria-hidden":"true", focusable:"false"})) svg.setAttribute(key, value);
  const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
  path.setAttribute("d", paths[action]); svg.append(path);
  return svg;
}

// Shared settings geometry for global settings and conversation controls.
export function createSettingsIcon(document) {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24"); svg.setAttribute("aria-hidden", "true");
  const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
  path.setAttribute("d", "M9.998 3.099Q10.168 2.576 10.718 2.576L13.282 2.576Q13.832 2.576 14.002 3.099L14.481 4.569Q14.652 5.092 15.128 5.367L16.181 5.974Q16.657 6.249 17.195 6.135L18.707 5.816Q19.245 5.702 19.520 6.178L20.802 8.398Q21.077 8.875 20.709 9.284L19.676 10.433Q19.309 10.842 19.309 11.392L19.309 12.608Q19.309 13.158 19.676 13.567L20.709 14.716Q21.077 15.125 20.802 15.602L19.520 17.822Q19.245 18.298 18.707 18.184L17.195 17.865Q16.657 17.751 16.181 18.026L15.128 18.633Q14.652 18.908 14.481 19.431L14.002 20.901Q13.832 21.424 13.282 21.424L10.718 21.424Q10.168 21.424 9.998 20.901L9.519 19.431Q9.348 18.908 8.872 18.633L7.819 18.026Q7.343 17.751 6.805 17.865L5.293 18.184Q4.755 18.298 4.480 17.822L3.198 15.602Q2.923 15.125 3.291 14.716L4.324 13.567Q4.691 13.158 4.691 12.608L4.691 11.392Q4.691 10.842 4.324 10.433L3.291 9.284Q2.923 8.875 3.198 8.398L4.480 6.178Q4.755 5.702 5.293 5.816L6.805 6.135Q7.343 6.249 7.819 5.974L8.872 5.367Q9.348 5.092 9.519 4.569Z");
  const center = document.createElementNS("http://www.w3.org/2000/svg", "circle");
  for (const [key, value] of Object.entries({cx:12,cy:12,r:3.25})) center.setAttribute(key,String(value));
  svg.append(path,center); return svg;
}

export function createFolderIcon(document) {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24"); svg.setAttribute("aria-hidden", "true");
  const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
  path.setAttribute("d", "M3 8V6a2 2 0 0 1 2-2h4l2 3h8a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8Zm0 0h18");
  svg.append(path); return svg;
}
