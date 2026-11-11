import { memberMentionRanges } from "./client.mjs";

// Mentions remain ordinary Room text; choosing a member never changes delivery.
export function createMentionPicker({ document, input, list, mirror, members, operator, enabled, changed }) {
  let context = null, choices = [], selected = 0, composing = false;

  function syncScroll() {
    mirror.style.setProperty("width", `${input.clientWidth}px`);
    mirror.scrollTop = input.scrollTop; mirror.scrollLeft = input.scrollLeft;
  }

  function renderText() {
    const text = input.value;
    const ranges = memberMentionRanges(text, [...members().map(m => m.agent_id), operator()]);
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
    const match = /(^|[^A-Za-z0-9_@.+-])@([^\s@，。！？：；、（）【】「」“”‘’,!?;:()\[\]{}<>]*)$/u.exec(input.value.slice(0, end));
    if (!match || /[\p{L}\p{N}_@.+-]/u.test(input.value.charAt(end))) return null;
    return { start: end - match[2].length - 1, end, query: match[2] };
  }

  function choose(member) {
    const at = current();
    if (!at || !context || at.start !== context.start || at.end !== context.end || at.query !== context.query ||
      !members().some(m => m.agent_id === member.agent_id && m.membership_id === member.membership_id)) { close(); return; }
    const separator = /^\s/u.test(input.value.slice(at.end)) ? "" : " ";
    input.setRangeText(`@${member.agent_id}${separator}`, at.start, at.end, "end");
    close(); input.focus(); changed();
  }

  function highlight() {
    for (let i = 0; i < list.children.length; i++) list.children[i].setAttribute("aria-selected", String(i === selected));
    const option = list.children[selected];
    input.setAttribute("aria-activedescendant", option.id);
    option.scrollIntoView({ block: "nearest" });
  }

  function update() {
    const at = current();
    if (!at) { close(); return; }
    const previous = context?.start === at.start && context.query === at.query ? choices[selected] : null;
    context = at;
    choices = members().filter(m => m.agent_id.toLocaleLowerCase().includes(at.query.toLocaleLowerCase()));
    if (!choices.length) { close(); return; }
    selected = Math.max(0, choices.findIndex(m => m.membership_id === previous?.membership_id && m.agent_id === previous?.agent_id));
    list.replaceChildren(...choices.map((member, index) => {
      const option = document.createElement("button");
      option.type = "button"; option.tabIndex = -1; option.id = `mention-option-${index}`;
      option.className = "mention-option"; option.setAttribute("role", "option");
      option.textContent = `@${member.agent_id}`;
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
      const range = memberMentionRanges(input.value, members().map(m => m.agent_id)).find(at => event.key === "Delete" ? caret === at.start :
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
  return { close, mount, keydown, renderText, refresh: () => { renderText(); if (context) update(); } };
}
