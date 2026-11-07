import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import * as client from "./web/client.mjs";
import { notificationOptions } from "./web/markdown.mjs";

// Exercise the actual entry point and its events without a browser dependency.
// This DOM implements events and content only; it is not visual acceptance.
const html = await readFile(new URL("./web/index.html", import.meta.url), "utf8");
const app = (await readFile(new URL("./web/app.js", import.meta.url), "utf8")).replace(/^import .*;\n/gm, "");
class Element {
  constructor(tag = "div") {
    this.tag = tag; this.children = []; this.listeners = {}; this.dataset = {}; this.attributes = {};
    this.hidden = false; this.disabled = false; this.open = false; this.value = ""; this.textContent = "";
    this.scrollTop = 0; this.scrollHeight = 100; this.clientHeight = 100;
    this.clientWidth = 300;
    const styles = new Map();
    this.style = { setProperty: (name, value) => styles.set(name, value), getPropertyValue: name => styles.get(name) || "" };
    this.capturedPointers = new Set();
    const classes = new Set();
    this.classList = { add: name => classes.add(name), remove: name => classes.delete(name), contains: name => classes.has(name),
      toggle: (name, on) => on ? classes.add(name) : classes.delete(name) };
    this.parts = new Map();
  }
  get options() { return this.children; }
  append(...children) { this.children.push(...children.flatMap(child => child.tag === "fragment" ? child.children : [child])); }
  replaceChildren(...children) { this.children = []; this.append(...children); if (this.tag === "select") this.value = this.children[0]?.value || ""; }
  setAttribute(key, value) { this.attributes[key] = value; }
  querySelector(selector) {
    if (!this.parts.has(selector)) this.parts.set(selector, new Element());
    return this.parts.get(selector);
  }
  querySelectorAll(selector) {
    assert.equal(selector, "details[open]");
    return this.children.flatMap(child => [
      ...(child.tag === "details" && child.open ? [child] : []), ...child.querySelectorAll(selector),
    ]);
  }
  addEventListener(event, handler) { (this.listeners[event] ||= []).push(handler); }
  async emit(event, data = {}) {
    if (event === "click" && this.disabled) return;
    await Promise.all((this.listeners[event] || []).map(handler => handler({ preventDefault() {}, target: this, ...data })));
  }
  focus() {}
  requestSubmit() { return this.emit("submit"); }
  setRangeText(text, start, end) {
    this.value = this.value.slice(0, start) + text + this.value.slice(end);
    this.selectionStart = this.selectionEnd = start + text.length;
  }
  getBoundingClientRect() { return this.rect || { top: 0, bottom: this.clientHeight, left: 0, right: this.clientWidth, width: this.clientWidth, height: this.clientHeight }; }
  setPointerCapture(id) { this.capturedPointers.add(id); }
  hasPointerCapture(id) { return this.capturedPointers.has(id); }
  releasePointerCapture(id) { this.capturedPointers.delete(id); }
  scrollIntoView() { this.scrollIntoViewCount = (this.scrollIntoViewCount || 0) + 1; }
  cloneNode() { return new Element(this.tag); }
  show() { this.open = true; this.modal = false; }
  showModal() { this.open = true; this.modal = true; }
  close() { if (this.open) { this.open = false; void this.emit("close"); } }
}
export const settle = async () => { for (let i=0; i<15; i++) await new Promise(resolve => setImmediate(resolve)); };
export async function launch(api, saved = new Map()) {
  const elements = new Map([...html.matchAll(/id="([^"]+)"/g)].map(match => [match[1], new Element()]));
  for (const id of ["lark-scope", "lark-group-mode", "lark-chat-type", "lark-connection", "lark-target", "lark-backfill", "connection-provider", "connection-profile", "team", "profile", "activity-member", "workspace-connection", "gitlab-connection", "gitlab-target"]) elements.get(id).tag = "select";
  for (const id of ["message-template", "member-template"]) elements.get(id).content = { firstElementChild: new Element() };
  for (const [, id, value] of html.matchAll(/<input id="(gitlab-(?:events-type|todos-reason|todos-type)-[^"]+)" type="checkbox" value="([^"]+)"/g)) {
    elements.get(id).tag = "input"; elements.get(id).value = value; elements.get(id).checked = false;
  }
  const workspace = elements.get("workspace"); workspace.clientWidth = 1440;
  elements.get("room-rail").clientWidth = 220;
  const window = new Element("window"); window.innerWidth = 1440; window.innerHeight = 900;
  window.confirm = text => api.confirm ? api.confirm(text) : true;
  window.open = () => null; // Default to a blocked popup; UI retains its explicit link.
  const document = Object.assign(new Element("document"), { hidden: false, focused: true, title: "", hasFocus() { return this.focused; },
    getElementById(id) {
      if (elements.has(id)) return elements.get(id);
      const find = el => el.id === id ? el : el.children.map(find).find(Boolean);
      const dynamic = [...elements.values()].map(find).find(Boolean);
      assert.ok(dynamic, `unknown HTML id ${id}`); return dynamic;
    },
    createElement: tag => new Element(tag), createDocumentFragment: () => new Element("fragment"),
    querySelector: selector => selector === "dialog:modal" ? [...elements.values()].find(el => el.open && el.modal) || null : workspace,
    querySelectorAll: () => [] });
  const storage = { getItem: key => saved.get(key) || null, setItem: (key, value) => saved.set(key, value), removeItem: key => saved.delete(key) };
  let now = 0, timerID = 0;
  const timers = new Map();
  const clock = { async advance(ms) {
    const end = now + ms;
    while (true) {
      const next = [...timers].filter(([, timer]) => timer.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
      if (!next) break;
      now = next[1].at; timers.delete(next[0]); next[1].fn(); await settle();
    }
    now = end; await settle();
  } };
  const names = ["document", "window", "sessionStorage", "Option", "setInterval", "requestAnimationFrame", "renderMessageBody", "notificationOptions", "performance", "setTimeout", "clearTimeout", ...Object.keys(client).map(name => name === "readPages" ? "collectPages" : name)];
  const values = [document, window, storage,
    function(text, value) { const el = new Element("option"); el.textContent = text; el.value = value; return el; },
    () => {}, fn => fn(), (el, text, operator, options) => { el.textContent = text; el.markdownOptions = options; }, notificationOptions, { now: () => now },
    (fn, delay) => { const id = ++timerID; timers.set(id, { fn, at: now + delay }); return id; },
    id => timers.delete(id), ...Object.values(client)];
  values[names.indexOf("request")] = api.request;
  const AsyncFunction = Object.getPrototypeOf(async function() {}).constructor;
  const entry = new AsyncFunction(...names, app + "\nreturn { refresh };\n");
  const entrypoint = await entry(...values);
  assert.ok(!elements.get("sync-error").textContent, elements.get("sync-error").textContent);
  return { el: id => elements.get(id), saved, document, window, clock, refresh: entrypoint.refresh,
    async draft(text) { elements.get("message-input").value = text; await elements.get("message-input").emit("input"); },
    async select(name) {
      const choice = elements.get("room-list").children.map(row => row.children[0]).find(button => button.children[0].textContent === name);
      assert.ok(choice, `room ${name} is listed`); await choice.emit("click"); await settle();
    },
    async delete(name) {
      const row = elements.get("room-list").children.find(row => row.children[0].children[0].textContent === name);
      assert.ok(row, `room ${name} is listed`); await row.children[1].emit("click"); await settle();
    },
    async create(name) {
      await elements.get("new-room").emit("click"); elements.get("new-room-name").value = name;
      await elements.get("create-form").emit("submit"); await settle();
    } };
}
