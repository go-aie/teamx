// A service-side browser. A selection only updates the caller's draft.
export const directoryErrors = {
  directory_unavailable: "目录暂不可用，请检查服务账号权限或挂载后重试。",
  directory_not_allowed: "目录不在可选择范围内，或属于 TeamX 管理数据。",
  directory_not_found: "目录不存在，请检查服务端路径或挂载。",
  directory_not_directory: "该路径不是目录。",
  directory_permission_denied: "服务账号无法进入或列举该目录。",
  directory_path_changed: "目录链接已变化，请重新选择。",
  directory_selection_disabled: "服务已关闭指定目录选择；可恢复默认目录。",
  directory_home_unavailable: "服务账号主目录不可用，请配置 server.workspace_roots 后重启。",
  directory_roots_unavailable: "可选择根目录暂不可用，请检查服务端挂载和权限。",
  directory_conflict: "目录已被其他 Room 占用，或与成员目录重叠；同 Room 共用目录需要串行调度。",
};
export function createDirectoryPicker({ env, runtime }) {
  const { document, request, Option } = env, { $, notice } = runtime;
  let view = null, sequence = 0, pathRevision = 0;
  function current(v) { return view === v && $("directory-dialog").open && v.current(); }
  function restoreFocus(v) { if (v?.current()) (typeof v.opener === "function" ? v.opener() : v.opener)?.focus(); }
  function close() { const v = view; sequence++; view = null; $("directory-dialog").close(); runtime.navigationChanged(); restoreFocus(v); }
  function button(text, action) {
    const el = document.createElement("button"); el.type = "button"; el.className = "directory-entry";
    const label = document.createElement("span"); label.textContent = text;
    el.append(icon("M3 7V5a1 1 0 0 1 1-1h5l2 3h9a1 1 0 0 1 1 1v11H3z"), label, icon("m9 6 6 6-6 6"));
    el.addEventListener("click", action); return el;
  }
  function icon(path) {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg"), line = document.createElementNS("http://www.w3.org/2000/svg", "path");
    svg.setAttribute("viewBox", "0 0 24 24"); svg.setAttribute("aria-hidden", "true"); line.setAttribute("d", path); svg.append(line); return svg;
  }
  function renderEntries(v) {
    if (!current(v) || !v.data) return;
    const data = v.data, entries = data.entries.filter(entry => $("directory-show-hidden").checked || !entry.name.startsWith("."));
    $("directory-entries").replaceChildren(...entries.map(entry => button(entry.name, () => navigate(entry.path))));
    $("directory-count").textContent = data.path ? `${entries.length} 个文件夹` : "";
    $("directory-status").textContent = data.unavailable_reason ? (directoryErrors[data.unavailable_reason] || directoryErrors.directory_unavailable) + (data.truncated ? " 列表不完整，可输入绝对路径定位。" : "")
      : data.directory_source === "managed" && !data.can_select ? "默认目录的子目录仅供浏览，请返回默认目录选择。"
      : data.truncated ? "列表不完整，可输入绝对路径定位。" : !entries.length ? (data.entries.length ? "此目录只有隐藏目录，可勾选显示。" : "此目录没有可浏览的子目录。") : "";
  }
  async function navigate(path) {
    const v = view; if (!v || !current(v)) return;
    const read = ++sequence, inputRevision = pathRevision; v.data = null;
    $("directory-entries").setAttribute("aria-busy", "true");
    $("directory-select").disabled = true; $("directory-parent").disabled = true; $("directory-root").disabled = true;
    $("directory-entries").replaceChildren(); notice("directory-error", "");
    $("directory-count").textContent = ""; $("directory-current").textContent = "请选择目录"; $("directory-current").title = "";
    $("directory-status").textContent = "正在读取目录…";
    try {
      const q = new URLSearchParams(v.identity); if (path !== undefined) q.set("path", path);
      const data = await request(`/v1/workspace/directories?${q}`);
      if (!current(v) || sequence !== read) return;
      v.data = data;
      $("directory-roots").replaceChildren(...data.roots.map(root => {
        const option = new Option(`${root.path}${root.unavailable_reason ? ` · ${directoryErrors[root.unavailable_reason] || "暂不可用"}` : ""}`, root.path);
        option.disabled = !!root.unavailable_reason; return option;
      }));
      const root = data.roots.find(root => !root.unavailable_reason && (root.path === data.path || data.path?.startsWith(root.path.replace(/\/$/, "") + "/")));
      $("directory-roots").value = root?.path || "";
      $("directory-roots").disabled = !data.roots.some(root => !root.unavailable_reason);
      $("directory-roots-field").hidden = data.roots.length < 2 && !data.roots.some(root => root.unavailable_reason);
      v.root = root?.path || data.roots.find(root => !root.unavailable_reason)?.path;
      $("directory-root").title = v.root ? `返回可选根目录：${v.root}` : "返回可选根目录";
      $("directory-root").disabled = !v.root || v.root === data.path;
      v.pathDirty = pathRevision !== inputRevision;
      if (!v.pathDirty) $("directory-path").value = data.path || "";
      if (path === undefined) notice("directory-initial-warning", data.current_directory_error
        ? `${directoryErrors[data.current_directory_error] || directoryErrors.directory_unavailable} ${data.path ? "已从可选根目录开始，" : ""}原设置保留。` : "");
      $("directory-current").textContent = data.path ? data.path.split("/").filter(Boolean).at(-1) || "/" : "尚无可浏览目录";
      $("directory-current").title = data.path || "";
      renderEntries(v);
      $("directory-select").textContent = data.directory_source === "managed" ? "使用默认目录" : "使用此目录";
      $("directory-select").disabled = !data.can_select || v.pathDirty; $("directory-parent").disabled = !data.parent;
    } catch (error) {
      if (!current(v) || sequence !== read) return;
      $("directory-status").textContent = "";
      notice("directory-error", directoryErrors[error.code] || "读取失败，请重新定位或稍后重试。");
    } finally { if (current(v) && sequence === read) $("directory-entries").setAttribute("aria-busy", "false"); }
  }
  function open({ identity, current: valid, select, opener, configured, initialPath }) {
    close(); view = { identity, current:valid, select, opener };
    $("directory-roots").replaceChildren(); $("directory-path").value = "";
    $("directory-roots-field").hidden = true;
    $("directory-show-hidden").checked = false;
    $("directory-select").textContent = "使用此目录";
    const name = configured?.path?.split("/").filter(Boolean).at(-1) || configured?.path || "";
    $("directory-configured").textContent = configured?.source === "external" ? `当前使用：指定目录 · ${name}` : "当前使用：默认目录";
    $("directory-configured").title = configured?.path || "";
    notice("directory-initial-warning", "");
    $("directory-dialog").showModal(); runtime.navigationChanged(); $("directory-cancel").focus();
    return navigate(initialPath);
  }
  function reconcile() { if (view && !current(view)) close(); }
  function mount() {
    $("directory-show-hidden").addEventListener("change", () => { if (view) renderEntries(view); });
    $("directory-path").addEventListener("input", () => {
      pathRevision++;
      if (view) { view.pathDirty = true; $("directory-select").disabled = true; }
    });
    $("directory-locate").addEventListener("submit", event => { event.preventDefault(); return navigate($("directory-path").value); });
    $("directory-roots").addEventListener("change", () => navigate($("directory-roots").value));
    $("directory-parent").addEventListener("click", () => view?.data?.parent && navigate(view.data.parent));
    $("directory-root").addEventListener("click", () => view?.root && navigate(view.root));
    $("directory-select").addEventListener("click", () => {
      const v = view; if (!v || !current(v) || !v.data?.can_select || v.pathDirty) return;
      v.select(v.data.path, v.data.directory_source); close();
    });
    $("directory-cancel").addEventListener("click", close);
    $("directory-dialog").addEventListener("close", () => {
      // A native close event may arrive after the dialog has been reopened.
      if ($("directory-dialog").open) return;
      const v = view; sequence++; view = null; restoreFocus(v); runtime.navigationChanged();
    });
  }
  return { open, close, reconcile, mount };
}
