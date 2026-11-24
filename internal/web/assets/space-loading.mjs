import { mountSpacePicker, pageSpaceID } from "./space-context.mjs";
void mountSpacePicker();
let active = true;
window.addEventListener("pagehide", () => { active = false; });
document.getElementById("space-reload").addEventListener("click", () => location.reload());
async function poll() {
  try {
    const response = await fetch("/v1/spaces", {cache:"no-store"});
    if (response.status === 401) { location.replace(`/s/${pageSpaceID}/access`); return; }
    if (response.ok) {
      const directory = await response.json(), space = directory.spaces.find(space => space.id === pageSpaceID);
      if (!active) return;
      if (["ready", "setup_required", "migration_required"].includes(space?.runtime_state)) { location.reload(); return; }
      if (space?.status === "suspended") {
        document.querySelector("h1").textContent = "空间已归档";
        document.getElementById("space-status").textContent = "此空间的后台任务已停止，会话与工作文件已保留。可在空间设置中恢复，或切换其他空间。";
        if (document.getElementById("space-lifecycle-action")?.dataset.action !== "restore") location.reload();
        return;
      }
      if (space?.status === "archiving") {
        document.querySelector("h1").textContent = "正在归档空间";
        document.getElementById("space-status").textContent = space.lifecycle_error || "正在停止此空间并等待已受理操作结清，其他空间可继续使用。";
        if (!space.lifecycle_busy) { if (document.getElementById("space-lifecycle-action")?.disabled) location.reload(); return; }
      }
      if (space?.runtime_state === "failed") { document.getElementById("space-status").textContent = "此空间启动失败。可以切换其他空间；请在部署机核对配置并重启后重试。"; return; }
    }
  } catch { /* The same Space remains selected during a service restart. */ }
  if (active) setTimeout(poll, 750);
}
void poll();
