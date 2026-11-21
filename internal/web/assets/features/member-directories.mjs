import { resolveAgentName } from "../agent-names.mjs";
import { createDirectoryPicker, directoryErrors } from "./directory-picker.mjs";

export function createMemberDirectories({ env, runtime, membersUI, workspaceView, busy, submit, changed = () => {} }) {
  const { document } = env, { $, state } = runtime;
  const picker = createDirectoryPicker({ env, runtime });
  let bulkSignature = "";
  const name = member => resolveAgentName(state.members, member.agent_id, member.membership_id);
  function draft(key) {
    const ws = workspaceView(); if (!ws?.data) return null;
    ws.directoryDrafts ||= new Map();
    let d = ws.directoryDrafts.get(key);
    if (!d || !d.dirty && d.version !== ws.data.workspace_version) {
      d = { version:ws.data.workspace_version, target:structuredClone(ws.data.workspace), dirty:false };
      ws.directoryDrafts.set(key, d);
    }
    return d;
  }
  const conflict = d => d && d.version !== workspaceView()?.data?.workspace_version;
  function set(d, member, path) {
    d.target.member_directories ||= {};
    if (path) d.target.member_directories[member.membership_id] = path;
    else delete d.target.member_directories[member.membership_id];
    if (!Object.keys(d.target.member_directories).length) delete d.target.member_directories;
    d.dirty = true; render();
  }
  function describe(d, member) {
    const path = d.target.member_directories?.[member.membership_id];
    const shared = path ? workspaceView().data.members.filter(m => m.membership_id !== member.membership_id && d.target.member_directories?.[m.membership_id] === path) : [];
    return `${path ? "指定目录" : "默认目录"} · ${path || member.default_directory || member.workspace}${shared.length ? `\n与 ${shared.map(name).join("、")} 共用。共用目录共用文件、分支和 Git 暂存区，提交可能包含其他成员的修改。` : ""}`;
  }
  function affected(d) {
    return workspaceView().data.members.filter(m => (d.target.member_directories?.[m.membership_id] || "") !== (workspaceView().data.workspace.member_directories?.[m.membership_id] || ""));
  }
  function feedback(d) {
    const ws = workspaceView(), op = ws?.data?.operation;
    if (conflict(d)) return "配置版本已变化，草稿已保留。请刷新设置后重新选择。";
    if (ws?.error) return ws.error;
    if (op?.status === "running") return "正在应用工作目录，关闭页面不会取消已受理操作。";
    if (op?.status === "failed") return "工作目录设置未完成，请在房间工作目录中查看并重试原操作。";
    const changed = d && affected(d);
    const error = ws?.data?.directory_error ? (directoryErrors[ws.data.directory_error] || directoryErrors.directory_unavailable) + " " : "";
    return changed?.length ? error + `待应用：${changed.map(name).join("、")}。应用后会结清当前执行并重置受影响成员的私有上下文，工作文件保留。`
      : error || (op?.status === "completed" ? "设置已生效。" : "选择 TeamX 服务所在机器的目录。");
  }
  function browse(d, member, opener, key, options = {}) {
    const ws = workspaceView(), epoch = state.catalog?.service_epoch;
    const current = () => ws === workspaceView() && epoch === state.catalog?.service_epoch &&
      ws.data?.members.some(m => m.agent_id === member.agent_id && m.membership_id === member.membership_id) &&
      (key === "composer" ? $("workspace-selector").open : key === "room" ? $("workspace-dialog").open : $("activity-dialog").open && membersUI.selectedAgent()?.agent_id === member.agent_id && membersUI.selectedAgent()?.membership_id === member.membership_id);
    return picker.open({ identity:{...ws.room, service_epoch:epoch, caller_id:ws.operator, agent_id:member.agent_id, membership_id:member.membership_id}, current,
      configured:{ source:ws.data.workspace.member_directories?.[member.membership_id] ? "external" : "managed", path:ws.data.workspace.member_directories?.[member.membership_id] || member.default_directory || member.workspace },
      select:(path, source) => {
        if (conflict(d) || disabled(d)) return;
        if (key === "composer") chooseComposer(source === "managed" ? "" : path);
        else set(d, member, source === "managed" ? "" : path);
      }, opener:key === "room" ? () => document.getElementById(`workspace-directory-choose-${member.membership_id}`) : opener, ...options });
  }
  const disabled = d => !d || !workspaceView()?.connected || busy() || conflict(d);
  function renderMember() {
    const selected = membersUI.selectedAgent(), ws = workspaceView();
    const member = ws?.data?.members.find(m => m.agent_id === selected?.agent_id && m.membership_id === selected?.membership_id);
    $("member-directory").hidden = !state.managed || !member;
    if (!member) return;
    const d = draft(member.membership_id);
    const [fullPath, ...sharing] = describe(d, member).split("\n");
    const path = d.target.member_directories?.[member.membership_id] || member.default_directory || member.workspace;
    const parts = path.split("/").filter(Boolean);
    const short = parts.slice(-2).map(part => {
      if (/^[a-f0-9]{32}$/i.test(part)) return part.slice(0, 8) + "…";
      const chars = [...part];
      return chars.length > 32 ? chars.slice(0, 16).join("") + "…" + chars.slice(-8).join("") : part;
    }).join("/");
    $("member-directory-summary").textContent = `${d.target.member_directories?.[member.membership_id] ? "指定目录" : "默认目录"} · ${parts.length > 2 ? "…/" : ""}${short}`;
    $("member-directory-summary").title = path;
    const detail = $("member-directory-details"), key = `${ws.key}/${member.membership_id}`;
    if (detail.dataset.member !== key) { detail.open = false; detail.dataset.member = key; }
    $("member-directory-path").textContent = fullPath;
    $("member-directory-sharing").textContent = sharing.join("\n");
    $("member-directory-sharing").hidden = !sharing.length;
    $("member-directory-feedback").textContent = feedback(d);
    $("member-directory-choose").disabled = disabled(d);
    $("member-directory-default").disabled = disabled(d) || !d.target.member_directories?.[member.membership_id];
    $("member-directory-apply").disabled = disabled(d) || !affected(d).length;
    $("member-directory-default").hidden = !d.target.member_directories?.[member.membership_id];
    $("member-directory-apply").hidden = !affected(d).length;
    $("member-directory-actions").hidden = $("member-directory-default").hidden && $("member-directory-apply").hidden;
    $("member-directory-refresh").hidden = !conflict(d);
    $("member-directory-overview").hidden = !ws.data.directory_error && ws.data.operation?.status !== "failed";
  }
  function renderRoom() {
    const ws = workspaceView(); if (!ws?.data || !$("workspace-dialog").open) return;
    const d = draft("room");
    const signature = JSON.stringify([ws.key, d.version, ws.data.workspace_version, d.target.member_directories, ws.data.members, disabled(d), state.members.map(m => [m.membership_id, m.name])]);
    if (signature !== bulkSignature) {
      bulkSignature = signature;
      $("workspace-directories").replaceChildren(...ws.data.members.map(member => {
        const row = document.createElement("div"), title = document.createElement("strong"), path = document.createElement("p"), def = document.createElement("p"), actions = document.createElement("div");
        row.className = "directory-member-row"; title.textContent = name(member); path.className = "directory-path"; path.textContent = describe(d, member);
        def.className = "quiet directory-path"; def.textContent = `默认目录：${member.default_directory || member.workspace}`;
        actions.className = "directory-actions";
        for (const [label, action] of [["选择目录", el => browse(d, member, el, "room")], ["恢复默认", () => set(d, member, "")]]) {
          const el = document.createElement("button"); el.type = "button"; el.className = "text-button"; el.textContent = label;
          if (label === "选择目录") el.id = `workspace-directory-choose-${member.membership_id}`;
          el.setAttribute("aria-label", `${name(member)} ${label}`); el.disabled = disabled(d) || label === "恢复默认" && !d.target.member_directories?.[member.membership_id];
          el.addEventListener("click", () => action(el)); actions.append(el);
        }
        row.append(title, path, def, actions); return row;
      }));
    }
    $("workspace-directories-feedback").textContent = feedback(d);
    $("workspace-directories-default").disabled = disabled(d) || !Object.keys(d.target.member_directories || {}).length;
    $("workspace-directories-apply").disabled = disabled(d) || !affected(d).length;
    $("workspace-directories-refresh").hidden = !conflict(d);
  }
  function render() { picker.reconcile(); renderMember(); renderRoom(); }
  function refreshDraft(key) { workspaceView()?.directoryDrafts?.delete(key); render(); }
  async function apply(key) {
    const d = draft(key); if (disabled(d) || !affected(d).length) return;
    await submit({ target:structuredClone(d.target), version:d.version, draftKey:key }); render();
  }
  function composerState() {
    const ws = workspaceView(), d = draft("composer"), members = ws?.data?.members || [];
    return {workspace:ws?.data?.workspace, target:d?.target, members, disabled:disabled(d) || !members.length,
      changed:!!d && !!affected(d).length, conflict:!!conflict(d), feedback:feedback(d)};
  }
  function chooseComposer(path) {
    const d = draft("composer"); if (disabled(d)) return;
    const members = workspaceView().data.members;
    for (const member of members) {
      d.target.member_directories ||= {};
      if (path) d.target.member_directories[member.membership_id] = path;
      else delete d.target.member_directories[member.membership_id];
    }
    if (!Object.keys(d.target.member_directories || {}).length) delete d.target.member_directories;
    d.dirty = true; render(); changed();
  }
  function mount() {
    picker.mount();
    $("member-directory-choose").addEventListener("click", () => {
      const selected = membersUI.selectedAgent(), m = workspaceView()?.data?.members.find(m => m.membership_id === selected?.membership_id && m.agent_id === selected?.agent_id);
      if (m) return browse(draft(m.membership_id), m, $("member-directory-choose"), m.membership_id);
    });
    $("member-directory-default").addEventListener("click", () => { const m = membersUI.selectedAgent(); if (m) set(draft(m.membership_id), m, ""); });
    $("member-directory-apply").addEventListener("click", () => apply(membersUI.selectedAgent()?.membership_id));
    $("member-directory-refresh").addEventListener("click", () => refreshDraft(membersUI.selectedAgent()?.membership_id));
    $("workspace-directories-refresh").addEventListener("click", () => refreshDraft("room"));
    $("workspace-directories-default").addEventListener("click", () => { const d = draft("room"); delete d.target.member_directories; d.dirty = true; render(); });
    $("workspace-directories-apply").addEventListener("click", () => apply("room"));
    for (const id of ["activity-dialog", "workspace-dialog", "workspace-selector"]) $(id).addEventListener("close", picker.reconcile);
  }
  return { mount, render, picker, composer:{state:composerState, choose:chooseComposer,
    browse(options = {}) { const d = draft("composer"), member = workspaceView()?.data?.members[0];
      if (!member || disabled(d)) return;
      return browse(d, member, $("workspace-select-browse"), "composer", options);
    }, apply:() => apply("composer"), refresh:() => { refreshDraft("composer"); changed(); }} };
}
