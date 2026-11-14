import { agentName } from "./agent-names.mjs";
import { memberMentionRanges } from "./client.mjs";

// Mentions remain ordinary Room text; choosing a member never changes delivery.
export function createMentionPicker({ document, input, list, mirror, members, operator, enabled, changed, draft = () => null }) {
  let context = null, choices = [], selected = 0, composing = false;
  let previousText = input.value, previousDraft = draft();
  function reconcile() {
    const value = input.value, currentDraft = draft();
    if (!currentDraft) return;
    if (currentDraft !== previousDraft) { previousDraft = currentDraft; previousText = value; return; }
    if (value === previousText) return;
    let start=0, oldEnd=previousText.length, newEnd=value.length;
    while(start<oldEnd && start<newEnd && previousText[start]===value[start])start++;
    while(oldEnd>start && newEnd>start && previousText[oldEnd-1]===value[newEnd-1]){oldEnd--;newEnd--;}
    const delta=newEnd-oldEnd;
    currentDraft.mentions=(currentDraft.mentions || []).flatMap(at=>at.end<=start ? [at] : at.start>=oldEnd ? [{...at,start:at.start+delta,end:at.end+delta}] : []);
    previousText=value;
  }
  const names=()=>[...members().flatMap(m=>[agentName(m),m.agent_id]),operator()];
  function boundMentions(){
    reconcile();const bound=(draft()?.mentions || []).map(at=>({...at}));
    for(const at of memberMentionRanges(input.value,names())){
      if(bound.some(b=>at.start<b.end && at.end>b.start))continue;
      const choices=members().filter(m=>m.agent_id===at.name || agentName(m)===at.name);
      if(choices.length===1)bound.push({start:at.start,end:at.end,kind:"agent",id:choices[0].agent_id,membership_id:choices[0].membership_id});
    }
    return bound.sort((a,b)=>a.start-b.start);
  }

  function syncScroll() {
    mirror.style.setProperty("width", `${input.clientWidth}px`);
    mirror.scrollTop = input.scrollTop; mirror.scrollLeft = input.scrollLeft;
  }

  function renderText() {
    reconcile();
    const text = input.value;
    const ranges = memberMentionRanges(text, names());
    input.classList.toggle("has-mentions", ranges.length > 0);
    mirror.hidden = !ranges.length;
    const parts = [];
    let end = 0;
    for (const at of ranges) {
      const plain = document.createElement("span"); plain.textContent = text.slice(end, at.start); parts.push(plain);
      const mark = document.createElement("mark"); mark.className = "mention-member";
      mark.textContent = text.slice(at.start, at.end); parts.push(mark); end = at.end;
    }
    const rest = document.createElement("span"); rest.textContent = text.slice(end) + (text.endsWith("\n") ? "\n" : ""); parts.push(rest);
    mirror.replaceChildren(...parts); syncScroll();
  }

  function close() {
    context = null; choices = []; selected = 0;
    list.hidden = true; list.replaceChildren();
    input.setAttribute("aria-expanded", "false");
    input.removeAttribute("aria-activedescendant");
  }

  function current() {
    if (!enabled() || input.disabled || input.readOnly || composing || input.selectionStart !== input.selectionEnd) return null;
    const end = input.selectionStart;
    if (!Number.isInteger(end)) return null;
    const match = /(^|[^A-Za-z0-9_@.+-])@([^\n@，。！？：；、（）【】「」“”‘’,!?;:()\[\]{}<>]*)$/u.exec(input.value.slice(0, end));
    if (!match || /[\p{L}\p{N}_@.+-]/u.test(input.value.charAt(end))) return null;
    return { start: end - match[2].length - 1, end, query: match[2] };
  }

  function choose(member) {
    const at = current();
    if (!at || !context || at.start !== context.start || at.end !== context.end || at.query !== context.query ||
      !members().some(m => m.agent_id === member.agent_id && m.membership_id === member.membership_id)) { close(); return; }
    const separator = /^\s/u.test(input.value.slice(at.end)) ? "" : " ";
    const label=`@${agentName(member)}`;
    input.setRangeText(`${label}${separator}`, at.start, at.end, "end");
    reconcile();
    if(draft())draft().mentions=[...(draft().mentions || []),{start:at.start,end:at.start+label.length,kind:"agent",id:member.agent_id,membership_id:member.membership_id}].sort((a,b)=>a.start-b.start);
    close(); input.focus(); changed();
  }

  function highlight() {
    for (let i = 0; i < list.children.length; i++) list.children[i].setAttribute("aria-selected", String(i === selected));
    const option = list.children[selected];
    input.setAttribute("aria-activedescendant", option.id);
    option.scrollIntoView({ block: "nearest" });
  }

  function update() {
    reconcile();
    const at = current();
    if (!at) { close(); return; }
    const previous = context?.start === at.start && context.query === at.query ? choices[selected] : null;
    context = at;
    choices = members().filter(m => [m.agent_id,agentName(m)].some(name=>name.toLocaleLowerCase().includes(at.query.toLocaleLowerCase())));
    if (!choices.length) { close(); return; }
    selected = Math.max(0, choices.findIndex(m => m.membership_id === previous?.membership_id && m.agent_id === previous?.agent_id));
    list.replaceChildren(...choices.map((member, index) => {
      const option = document.createElement("button");
      option.type = "button"; option.tabIndex = -1; option.id = `mention-option-${index}`;
      option.className = "mention-option"; option.setAttribute("role", "option");
      option.textContent = `@${agentName(member)}${members().filter(m=>agentName(m)===agentName(member)).length>1 ? ` · ${member.agent_id}` : ""}`;
      option.addEventListener("pointerdown", event => { event.preventDefault(); });
      option.addEventListener("click", () => choose(member));
      return option;
    }));
    list.hidden = false; input.setAttribute("aria-expanded", "true"); highlight();
  }

  function keydown(event) {
    if (event.isComposing || event.keyCode === 229 || composing) return true;
    if (event.ctrlKey || event.metaKey || event.altKey || event.shiftKey) return false;
    if ((event.key === "Backspace" || event.key === "Delete") && enabled() && !input.disabled && !input.readOnly && input.selectionStart === input.selectionEnd) {
      const caret = input.selectionStart;
      const range = memberMentionRanges(input.value, names()).find(at => event.key === "Delete" ? caret === at.start :
        caret === at.end || caret === at.end + 1 && input.value[at.end] === " ");
      if (range) {
        event.preventDefault();
        const end = range.end + (input.value[range.end] === " " ? 1 : 0);
        input.setRangeText("", range.start, end, "end");
        close(); changed(); return true;
      }
    }
    if (!context) return false;
    if (event.key === "Escape") { event.preventDefault(); close(); return true; }
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault(); selected = (selected + (event.key === "ArrowDown" ? 1 : -1) + choices.length) % choices.length;
      highlight(); return true;
    }
    if (!event.shiftKey && (event.key === "Enter" || event.key === "Tab")) {
      event.preventDefault(); if (!event.repeat) choose(choices[selected]); return true;
    }
    return false;
  }

  function mount() {
    close(); renderText();
    input.addEventListener("input", update);
    input.addEventListener("click", update);
    input.addEventListener("keyup", event => { if (["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) update(); });
    input.addEventListener("blur", close);
    input.addEventListener("scroll", syncScroll);
    input.addEventListener("compositionstart", () => { composing = true; close(); });
    input.addEventListener("compositionend", () => { composing = false; update(); });
  }
  return { boundMentions, close, mount, keydown, renderText, refresh: () => { renderText(); if (context) update(); } };
}
