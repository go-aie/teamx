import { mountSpacePicker, pageSpaceID } from "./space-context.mjs";
import { mountSpaceReset } from "./space-reset.mjs";
void mountSpacePicker().then(mountSpaceReset);
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
      if (space?.reset && space.reset.phase !== "completed") {
        document.querySelector("h1").textContent = ({stopping:"正在停止任务",cleaning:"正在清理",initializing:"正在初始化"})[space.reset.phase] || "重置需要核对";
        document.getElementById("space-status").textContent = space.reset.message || "正在重置此空间，请稍候。";
      } else if (space?.status === "suspended") {
        document.querySelector("h1").textContent = "会话暂不可用";
        document.getElementById("space-status").textContent = space.capabilities?.space_management === true
          ? "后台任务已停止，会话与工作文件已保留。可在管理空间中恢复，或切换其他空间。"
          : "后台任务已停止，会话与工作文件已保留。请联系部署管理员恢复后重试。";
        return;
      }
      if ((!space?.reset || space.reset.phase === "completed") && space?.status === "archiving") {
        document.querySelector("h1").textContent = "正在停止后台任务";
        document.getElementById("space-status").textContent = space.lifecycle_error || "正在等待已受理操作完成，请稍候。";
        if (!space.lifecycle_busy) return;
      }
      if (space?.runtime_state === "failed") { document.getElementById("space-status").textContent = "启动失败，请在部署机核对配置并重启后重试。"; return; }
    }
  } catch { /* The same Space remains selected during a service restart. */ }
  if (active) setTimeout(poll, 750);
}
void poll();
