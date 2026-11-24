import { randomUUID } from "../random-id.mjs";

export function createMemberComputer({env, runtime, current}) {
  const {request, window} = env;
  const {$, state} = runtime;
  let view, value, saving, reading, error = "";
  function render() {
    if (!$("member-computer")) return;
    $("member-computer").hidden = !view || !value;
    if (!view || !value) return;
    $("member-computer-enabled").checked = saving === view ? !!view.computerRequested : !!value.computer_enabled;
    $("member-computer-enabled").disabled = saving === view || !!value.operation_id && value.status !== "completed";
    $("member-computer-status").textContent = error || (value.status !== "completed" ? value.message || "正在应用电脑设置…" : value.computer_enabled && !value.global_enabled ? "等待全局电脑连接启用" : value.computer_enabled ? "已启用，在右侧“电脑”中观看或接管独立屏幕" : "关闭后保留登录状态和文件");
  }
  function open(v) {view=v;value=undefined;error="";render();void refresh();}
  async function refresh() {
    const v=view;if (!v || !current(v) || reading === v) return;
    reading=v;
    try {
      const q = new URLSearchParams({room_id:v.room_id,room_instance:v.room_instance,agent_id:v.agent_id,membership_id:v.membership_id});
      const data=await request(`/v1/members/computer?${q}`);
      if (view===v && current(v)) {
        value=data;
        if (data.operation_id === v.computerOperation || data.computer_settings_version > v.computerExpected && data.computer_enabled === v.computerRequested) error="";
        render();
      }
    } catch { /* Optional capability on older servers. */ }
    finally {if (reading===v) reading=undefined;}
  }
  function mount() {
    $("member-computer-enabled")?.addEventListener("change",async () => {
      const v=view;if (!v || !value || saving === v || !current(v)) return;
      const enabled=$("member-computer-enabled").checked;
      const operation_id=randomUUID(window.crypto || globalThis.crypto).replaceAll("-","");
      const input={room_id:v.room_id,room_instance:v.room_instance,agent_id:v.agent_id,membership_id:v.membership_id,service_epoch:v.epoch || state.info?.service_epoch,caller:{kind:"human",id:state.operator},expected_version:value.computer_settings_version,operation_id,enabled};
      v.computerOperation=operation_id;v.computerExpected=value.computer_settings_version;v.computerRequested=enabled;
      saving=v;error="";render();
      try {await request("/v1/members/computer",input);}
      catch(e) {if (view===v && current(v)) error=e.code==="conflict" ? "设置已变化，已刷新当前状态。" : `保存未确认：${e.message}。正在核对原操作。`;}
      finally {if (saving===v) saving=undefined;await refresh();render();}
    });
  }
  return {mount,open,refresh};
}
