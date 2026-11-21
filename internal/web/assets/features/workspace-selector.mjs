import { compactViewport } from "../client.mjs";

const basename = path => path?.split("/").filter(Boolean).at(-1) || path || "默认目录";
export function workspaceDirectoryLabel(workspace) {
  const paths = [...new Set(Object.values(workspace?.member_directories || {}).filter(Boolean))];
  if (paths.length) return {label:paths.length === 1 ? basename(paths[0]) : "成员指定目录", path:paths.join("\n")};
  if (workspace?.repository) return {label:basename(workspace.repository.url).replace(/\.git$/, ""), path:workspace.root || workspace.repository.url};
  if (workspace?.root && workspace.root !== "workspaces") return {label:basename(workspace.root), path:workspace.root};
  return {label:"默认目录", path:"沿用各成员自己的工作目录"};
}

// The compact entry edits a directory draft. Applying uses the existing durable
// Workspace operation, including its identity, version and recovery checks.
export function createWorkspaceSelector({env, runtime, directories, closeMenu, openSettings}) {
  const {document, window} = env, {$} = runtime;
  const panel = $("workspace-selector"), trigger = $("workspace-trigger");
  let signature = "", mobile = false;
  function position() {
    if (!panel.open) return;
    const viewport = window.visualViewport;
    const left = viewport?.offsetLeft || 0, top = viewport?.offsetTop || 0;
    const width = viewport?.width || window.innerWidth, height = viewport?.height || window.innerHeight;
    panel.style.maxHeight = `${Math.max(100,mobile ? Math.min(height * .85,height - 16) : height - 16)}px`;
    if (mobile) { panel.style.bottom = `${Math.max(0,window.innerHeight - top - height)}px`; return; }
    const anchor = trigger.getBoundingClientRect(), box = panel.getBoundingClientRect();
    const above = anchor.top - box.height - 8;
    panel.style.left = `${Math.max(left + 8,Math.min(anchor.left,left + width - box.width - 8))}px`;
    panel.style.top = `${Math.max(top + 8,Math.min(above >= top + 8 ? above : anchor.bottom + 8,top + height - box.height - 8))}px`;
  }
  function close(focus = true) {
    if (!panel.open) return;
    panel.close(); trigger.setAttribute("aria-expanded","false");
    if (focus) trigger.focus({preventScroll:true});
    runtime.navigationChanged();
  }
  function render() {
    const data = directories.state();
    if (!panel.open) return;
    const query = $("workspace-select-search").value.trim().toLocaleLowerCase();
    const selected = data.target?.member_directories || {};
    const paths = [...new Set([...Object.values(data.workspace?.member_directories || {}),...Object.values(selected)].filter(Boolean))];
    const options = [{path:"",label:"默认目录",description:"沿用各成员自己的工作目录"},
      ...paths.map(path=>({path,label:basename(path),description:path}))]
      .filter(item=>!query || `${item.label}\n${item.description}`.toLocaleLowerCase().includes(query));
    const key = JSON.stringify([options,selected,data.disabled]);
    if (key !== signature) {
      signature = key;
      $("workspace-select-options").replaceChildren(...options.map(item=>{
        const button = document.createElement("button"); button.type = "button"; button.className = "workspace-choice";
        button.dataset.path = item.path;
        const checked = item.path ? data.members.length > 0 && data.members.every(member=>selected[member.membership_id] === item.path) : !Object.keys(selected).length;
        button.setAttribute("aria-pressed",String(checked)); button.disabled = data.disabled;
        const icon = document.createElementNS("http://www.w3.org/2000/svg","svg"), path = document.createElementNS("http://www.w3.org/2000/svg","path");
        icon.setAttribute("viewBox","0 0 24 24"); icon.setAttribute("aria-hidden","true");
        path.setAttribute("d",item.path ? "M3 7V5h6l2 2h10v13H3ZM3 10h18" : "m3 10 9-7 9 7M5 9v11h14V9M9 20v-7h6v7"); icon.append(path);
        const text = document.createElement("span"), label = document.createElement("strong"), detail = document.createElement("small");
        label.textContent = item.label; detail.textContent = item.description; text.append(label,detail);
        const check = document.createElement("span"); check.className = "workspace-choice-check"; check.textContent = "✓"; check.hidden = !checked; check.setAttribute("aria-hidden","true");
        button.append(icon,text,check); button.addEventListener("click",()=>{
          directories.choose(item.path);
          Array.from($("workspace-select-options").children).find(el=>el.dataset.path === item.path)?.focus({preventScroll:true});
        });
        return button;
      }));
    }
    $("workspace-select-empty").hidden = !!options.length;
    $("workspace-select-browse").disabled = data.disabled;
    $("workspace-select-browse-label").textContent = $("workspace-select-search").value.trim().startsWith("/") ? "定位此路径…" : "选择其他目录…";
    $("workspace-select-feedback").textContent = data.feedback + (data.changed && data.members.length > 1 ? " 会话成员将共用同一目录和文件。" : "");
    $("workspace-select-apply").hidden = !data.changed; $("workspace-select-apply").disabled = data.disabled || !data.changed;
    $("workspace-select-refresh").hidden = !data.conflict;
    position();
  }
  function open() {
    if (trigger.disabled) return;
    if (panel.open) { close(); return; }
    closeMenu(); signature = ""; $("workspace-select-search").value = "";
    mobile = compactViewport(window); panel.classList.toggle("workspace-selector-sheet",mobile);
    panel.style.left = ""; panel.style.top = ""; panel.style.bottom = ""; panel.style.maxHeight = "";
    if (mobile) panel.showModal(); else panel.show();
    trigger.setAttribute("aria-expanded","true"); render();
    // Avoid opening a phone keyboard before the user chooses to search.
    (mobile ? $("workspace-select-close") : $("workspace-select-search")).focus({preventScroll:true});
    runtime.navigationChanged();
  }
  function mount() {
    trigger.addEventListener("click",open);
    $("workspace-select-close").addEventListener("click",()=>close());
    $("workspace-select-search").addEventListener("input",render);
    $("workspace-select-search").addEventListener("keydown",event=>{
      if (event.key === "Enter") { event.preventDefault(); if ($("workspace-select-search").value.trim().startsWith("/")) void browse(); }
    });
    async function browse() {
      const value = $("workspace-select-search").value.trim();
      await directories.browse({initialPath:value.startsWith("/") ? value : undefined});
      render();
    }
    $("workspace-select-browse").addEventListener("click",browse);
    $("workspace-select-apply").addEventListener("click",()=>directories.apply());
    $("workspace-select-refresh").addEventListener("click",()=>directories.refresh());
    $("workspace-select-settings").addEventListener("click",()=>{close(false);return openSettings();});
    panel.addEventListener("cancel",event=>{event.preventDefault();close();});
    panel.addEventListener("keydown",event=>{if(event.key === "Escape" && !$("directory-dialog").open){event.preventDefault();close();}});
    panel.addEventListener("click",event=>{
      if (event.target !== panel) return;
      const box = panel.getBoundingClientRect();
      if (event.clientX < box.left || event.clientX > box.right || event.clientY < box.top || event.clientY > box.bottom) close();
    });
    panel.addEventListener("close",()=>{if (!panel.open) {trigger.setAttribute("aria-expanded","false");runtime.navigationChanged();}});
    document.addEventListener("click",event=>{
      const inside = event.composedPath?.().includes(panel) || panel.contains(event.target);
      if (panel.open && !mobile && !inside && !trigger.contains(event.target) && !$("directory-dialog").contains(event.target) && !$("directory-dialog").open) close(false);
    });
    const resize = () => {if (!panel.open) return; if (mobile !== compactViewport(window)) {close(false);open();} else position();};
    window.addEventListener("resize",resize);
    window.visualViewport?.addEventListener("resize",position);
    window.visualViewport?.addEventListener("scroll",position);
  }
  return {mount,render,close};
}
