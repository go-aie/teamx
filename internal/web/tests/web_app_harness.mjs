import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createApp } from "../assets/application.mjs";

// Exercise the actual entry point and its events without a browser dependency.
// This DOM implements events and content only; it is not visual acceptance.
const html = await readFile(new URL("../assets/index.html", import.meta.url), "utf8");
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
  append(...children) { for(const child of children.flatMap(child => child.tag === "fragment" ? [...child.children] : [child])) { child.remove();this.children.push(child);child.parentElement=this; } }
  insertBefore(child, before) { child.remove(); const i=this.children.indexOf(before); this.children.splice(i<0?this.children.length:i,0,child); child.parentElement=this; }
  remove() { if(this.parentElement){const list=this.parentElement.children;list.splice(list.indexOf(this),1);this.parentElement=null;} }
  replaceChildren(...children) { for(const child of [...this.children])child.remove();this.children = []; this.append(...children); if (this.tag === "select") this.value = this.children[0]?.value || ""; }
  setAttribute(key, value) { this.attributes[key] = value; }
  getAttribute(key) { return this.attributes[key] ?? this[key] ?? null; }
  removeAttribute(key) { delete this.attributes[key];delete this[key]; }
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
export async function launch(api, saved = new Map(), environment = {}) {
  const elements = new Map([...html.matchAll(/id="([^"]+)"/g)].map(match => [match[1], new Element()]));
  for (const id of ["lark-group-mode", "lark-connection", "lark-target", "lark-backfill", "connection-provider", "connection-profile", "connection-scion-profile", "team", "profile", "workspace-connection", "gitlab-connection", "gitlab-target", "onboarding-room", "onboarding-member", "onboarding-backfill", "gitlab-application"]) elements.get(id).tag = "select";
  for (const id of ["message-template", "member-template"]) elements.get(id).content = { firstElementChild: new Element() };
  for (const [, id, value] of html.matchAll(/<input id="(gitlab-(?:events-type|todos-reason|todos-type)-[^"]+)" type="checkbox" value="([^"]+)"/g)) {
    elements.get(id).tag = "input"; elements.get(id).value = value; elements.get(id).checked = false;
  }
  for(const id of ["notification-add-picker","member-notification-add-picker"]) elements.get(id).hidden=true;
  elements.get("lark-scope").tag = "input";
  const workspace = elements.get("workspace"); workspace.clientWidth = 1440;
  elements.get("room-rail").clientWidth = 240;
  for (const [rail, side, fallback] of [["room-rail", "rooms", 240], ["member-rail", "members", 340]]) {
    elements.get(rail).getBoundingClientRect = () => ({ width: Number.parseFloat(workspace.style.getPropertyValue(`--${side}-open-width`)) || fallback });
  }
  const window = new Element("window"); window.innerWidth = 1440; window.innerHeight = 900;
  window.getComputedStyle = () => ({ getPropertyValue: name => name === "--rooms-default-width" ? "240px" : "340px" });
  window.confirm = text => api.confirm ? api.confirm(text) : true;
  window.open = () => null; // Default to a blocked popup; UI retains its explicit link.
  const document = Object.assign(new Element("document"), { hidden: false, focused: true, title: "", hasFocus() { return this.focused; },
    getElementById(id) {
      if (elements.has(id)) return elements.get(id);
      const find = el => el.id === id ? el : el.children.map(find).find(Boolean);
      const dynamic = [...elements.values()].map(find).find(Boolean);
      assert.ok(dynamic, `unknown HTML id ${id}`); return dynamic;
    },
    createElement: tag => new Element(tag), createElementNS: (_, tag) => new Element(tag), createDocumentFragment: () => new Element("fragment"),
    querySelector: selector => selector === "dialog:modal" ? [...elements.values()].find(el => el.open && el.modal) || null : workspace,
    querySelectorAll: selector => {
      if (selector !== "[data-system-time]") return [];
      const visit = el => [...(el.refreshSystemTime ? [el] : []), ...el.children.flatMap(visit), ...[...el.parts.values()].flatMap(visit)];
      return [...elements.values()].flatMap(visit);
    } });
  for(const el of elements.values())el.focus=()=>{document.activeElement=el;};
  const createElement=document.createElement;
  document.createElement=tag=>{const el=createElement(tag);el.focus=()=>{document.activeElement=el;};return el;};
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
  const appEnvironment = {
    document, window, sessionStorage: storage,
    confirmAction: ({title,message}) => window.confirm(`${title}\n${message}`),
    Option: function(text, value) { const el = new Element("option"); el.textContent = text; el.value = value; return el; },
    setInterval: () => {}, requestAnimationFrame: fn => fn(),
    renderMessageBody: (el, text, operator, options) => { el.textContent = text; el.markdownOptions = options; },
    performance: { now: () => now },
    setTimeout: (fn, delay) => { const id = ++timerID; timers.set(id, { fn, at: now + delay }); return id; },
    clearTimeout: id => timers.delete(id),
    request: async (path, body, ...args) => {
      const data = await api.request(path, body, ...args);
      // Existing fixtures predate service-wide settings. Explicit null values
      // still exercise invalid/missing settings without a fallback.
      if (path === "/v1/ui" || path === "/v1/rooms") return {time_zone:"Asia/Shanghai",settings_version:1,...(data.managed ? {service_epoch:"manager"} : {}),...data};
      return data;
    },
  };
  Object.defineProperties(appEnvironment, Object.getOwnPropertyDescriptors(environment));
  const entrypoint = await createApp(appEnvironment);
  const notificationToggle = () => {
    const {target, platformID, memberAvailable} = entrypoint.notificationSource();
    const list = target.mode !== "inbox" ? elements.get("notification-platforms") : memberAvailable ? elements.get("member-notification-platforms") : elements.get("notification-orphan-items");
    const row = [...list.children].find(row => row.dataset.platformId === platformID);
    return row?.className.includes("member-platform-row") ? row.children[1].children[1] : row?.children[1];
  };
  assert.ok(!elements.get("sync-error").textContent, elements.get("sync-error").textContent);
  return { el: id => elements.get(id), saved, document, window, clock, refresh: entrypoint.refresh, openNotificationSource:entrypoint.openNotificationSource, selectedAgent:entrypoint.selectedAgent, notificationToggle,
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
      await elements.get("new-room").emit("click"); await elements.get("new-team-room").emit("click"); elements.get("new-room-name").value = name;
      await elements.get("create-form").emit("submit"); await settle();
    } };
}

// Legacy account-editor fixtures opt into the P5 operation protocol. Dedicated
// onboarding tests provide their own API and exercise failure/recovery states.
export function withOnboarding(api) {
  const original=api.request.bind(api), ops=new Map();
  api.request=async(path,body,...args)=>{
    if(path.startsWith("/v1/platforms/onboarding/targets?")) {
      const q=new URL(path,"http://fixture").searchParams;
      const data=await original("/v1/member/list",{});
      return {members:data.members.map(m=>({room_id:q.get("room_id"),room_instance:q.get("room_instance"),agent_id:m.agent_id,membership_id:m.membership_id}))};
    }
    if(path.startsWith("/v1/platforms/onboarding/")) {
      api.calls.push({path,body:structuredClone(body),data:structuredClone(body)});
      if(path.endsWith("/prepare")) {
        const {service_epoch,caller,operation_id,...input}=body;
        ops.set(operation_id,{schema:2,operation_id,input,platform_id:input.platform_id || operation_id,connection:{status:"waiting"},notifications:{status:input.only_platform ? "skipped":"pending"},tool:{status:input.only_platform ? "skipped":"pending"}});
      }
      const op=ops.get(body.operation_id);if(!op)throw Object.assign(Error("missing"),{code:"identity"});
      if(path.endsWith("/cancel")) op.cancelled=true;
      if(path.endsWith("/apply")) {op.connection={status:"connected"};if(!op.input.only_platform){op.notifications={status:"applied",version:1};op.tool={status:"applied",version:1};}}
      return structuredClone(op);
    }
    const result=await original(path,body,...args);
    if(body?.onboarding_id && path.endsWith("/gitlab/login")) return {...result,onboarding_id:body.onboarding_id};
    return result;
  };
  return api;
}

export async function openMemberInbox(ui, membershipID = "") {
  if(membershipID && ui.selectedAgent()?.membership_id!==membershipID && ui.el("activity-dialog").open){await ui.el("close-activity").emit("click");await settle();}
  if(!ui.el("activity-dialog").open) {
    const row=ui.el("members").children.find(row=>!membershipID || row.querySelector(".member-summary").dataset.activityMember===membershipID) || ui.el("members").children[0];
    assert.ok(row,"member is listed");await row.querySelector(".member-summary").emit("click");await settle();
  }
  if(ui.el("agent-tab-inbox").attributes["aria-selected"]!=="true"){await ui.el("agent-tab-inbox").emit("click");await settle();}
}
export async function openNotification(ui, provider, platformID = "", member = provider === "lark") {
  if(member)await openMemberInbox(ui,typeof member==="string" ? member : "");
  const prefix=member ? "member-notification-" : "notification-";
  const rows=ui.el(prefix+"platforms").children;
  const button=rows.filter(row=>!platformID || row.dataset.platformId===platformID).map(row=>row.children[0]).find(b=>b.dataset.provider===provider)
    || ui.el(prefix+"add-options").children.find(b=>b.dataset.provider===provider && (!platformID || b.dataset.platformId===platformID));
  assert.ok(button,`${provider} platform is available from ${member ? "member inbox" : "room notifications"}`);
  if(member && !rows.some(row=>row.children[0]===button)){
    const m=ui.selectedAgent();
    await ui.openNotificationSource(button,provider==="lark" ? "messages" : "gitlab",platformID || button.dataset.platformId,{mode:"inbox",agent_id:m.agent_id,membership_id:m.membership_id});
  }else await button.emit("click");await settle();
}
