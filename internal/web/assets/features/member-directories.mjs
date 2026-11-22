import { resolveAgentName } from "../agent-names.mjs";
import { createDirectoryPicker, directoryErrors } from "./directory-picker.mjs";

export function createMemberDirectories({ env, runtime, membersUI, workspaceView, isDirect, busy, submit, changed = () => {} }) {
  const { $, state } = runtime;
  const picker = createDirectoryPicker({ env, runtime });
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
  function describe(workspace, member) {
    const path = workspace.member_directories?.[member.membership_id];
    const shared = path ? workspaceView().data.members.filter(m => m.membership_id !== member.membership_id && workspace.member_directories?.[m.membership_id] === path) : [];
    return `${path ? "指定目录" : "默认目录"} · ${member.effective_directory || path || member.default_directory || member.workspace}${shared.length ? `\n与 ${shared.map(name).join("、")} 共用。共用目录共用文件、分支和 Git 暂存区，提交可能包含其他成员的修改。` : ""}`;
  }
  function affected(d) {
    return workspaceView().data.members.filter(m => (d.target.member_directories?.[m.membership_id] || "") !== (workspaceView().data.workspace.member_directories?.[m.membership_id] || ""));
  }
  function feedback(d) {
    const ws = workspaceView(), op = ws?.data?.operation;
    if (conflict(d)) return "配置版本已变化，草稿已保留。请刷新设置后重新选择。";
    if (ws?.error) return ws.error;
    if (op?.status === "running") return "正在应用工作目录，关闭页面不会取消已受理操作。";
    if (op?.status === "failed") return "工作目录设置未完成，请核查后重试原操作。";
    const changed = d && affected(d);
    const error = ws?.data?.directory_error ? (directoryErrors[ws.data.directory_error] || directoryErrors.directory_unavailable) + " " : "";
    return changed?.length ? error + `待应用：${changed.map(name).join("、")}。应用后会结清当前执行并重置受影响成员的私有上下文，工作文件保留。`
      : error || (op?.status === "completed" ? "设置已生效。" : "选择 TeamX 服务所在机器的目录。");
  }
  function browse(d, member, opener, options = {}) {
    const ws = workspaceView(), epoch = state.catalog?.service_epoch;
    const current = () => ws === workspaceView() && epoch === state.catalog?.service_epoch &&
      ws.data?.members.some(m => m.agent_id === member.agent_id && m.membership_id === member.membership_id) &&
      $("workspace-selector").open;
    return picker.open({ identity:{...ws.room, service_epoch:epoch, caller_id:ws.operator, agent_id:member.agent_id, membership_id:member.membership_id}, current,
      configured:{ source:ws.data.workspace.member_directories?.[member.membership_id] ? "external" : "managed", path:ws.data.workspace.member_directories?.[member.membership_id] || member.default_directory || member.workspace },
      select:(path, source) => {
        if (conflict(d) || disabled(d)) return;
        chooseComposer(source === "managed" ? "" : path);
      }, opener, ...options });
  }

  const disabled = d => !d || !workspaceView()?.connected || busy() || conflict(d);
  function renderMember() {
    const selected = membersUI.selectedAgent(), ws = workspaceView();
    const member = ws?.data?.members.find(m => m.agent_id === selected?.agent_id && m.membership_id === selected?.membership_id);
    $("member-directory").hidden = !state.managed || !member;
    if (!member) return;
    const workspace = ws.data.workspace;
    const [fullPath, ...sharing] = describe(workspace, member).split("\n");
    const path = member.effective_directory || workspace.member_directories?.[member.membership_id] || member.default_directory || member.workspace;
    const parts = path.split("/").filter(Boolean);
    const short = parts.slice(-2).map(part => {
      if (/^[a-f0-9]{32}$/i.test(part)) return part.slice(0, 8) + "…";
      const chars = [...part];
      return chars.length > 32 ? chars.slice(0, 16).join("") + "…" + chars.slice(-8).join("") : part;
    }).join("/");
    $("member-directory-summary").textContent = `${workspace.member_directories?.[member.membership_id] ? "指定目录" : "默认目录"} · ${parts.length > 2 ? "…/" : ""}${short}`;
    $("member-directory-summary").title = path;
    const detail = $("member-directory-details"), key = `${ws.key}/${member.membership_id}`;
    if (detail.dataset.member !== key) { detail.open = false; detail.dataset.member = key; }
    $("member-directory-path").textContent = fullPath;
    $("member-directory-sharing").textContent = sharing.join("\n");
    $("member-directory-sharing").hidden = !sharing.length;
    $("member-directory-feedback").textContent = ws.data.directory_error ? directoryErrors[ws.data.directory_error] || directoryErrors.directory_unavailable : "";
  }
  function render() { picker.reconcile(); renderMember(); }
  function refreshDraft(key) { workspaceView()?.directoryDrafts?.delete(key); render(); }
  async function apply(key) {
    const d = draft(key); if (disabled(d) || !affected(d).length || composerState().parallel && Object.keys(d.target.member_directories || {}).length) return;
    await submit({ target:structuredClone(d.target), version:d.version, draftKey:key }); render();
  }
  function composerState() {
    const ws = workspaceView(), d = draft("composer"), members = ws?.data?.members || [];
    return {group:!isDirect(), workspace:ws?.data?.workspace, target:d?.target, members, disabled:disabled(d) || !members.length,
      changed:!!d && !!affected(d).length, conflict:!!conflict(d), feedback:feedback(d),
      parallel:ws?.data?.scheduling === "parallel" && members.length > 1,
      failed:ws?.data?.operation?.status === "failed", pending:!!ws?.pending, missing:!!ws?.missing,
      sending:!!ws?.sending,
      retryDisabled:!!ws?.sending || !ws?.connected || !!ws?.pending || !!state.catalog?.busy};
  }
  function chooseComposer(path) {
    const d = draft("composer"); if (disabled(d)) return;
    const members = workspaceView().data.members;
    if (path && composerState().parallel) return;
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
    $("workspace-selector").addEventListener("close", picker.reconcile);
  }

  return { mount, render, picker, composer:{state:composerState, choose:chooseComposer,
    browse(options = {}) { const d = draft("composer"), member = workspaceView()?.data?.members[0];
      if (!member || disabled(d) || composerState().parallel) return;
      return browse(d, member, $("workspace-select-browse"), options);
    }, apply:() => apply("composer"), refresh:() => { refreshDraft("composer"); changed(); }} };
}
