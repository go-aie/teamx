import { pageSpaceID, spaceCapabilities, observeSpaceReset, freezeSpaceCache } from "./space-context.mjs";

// A control-plane surface also usable in setup or while the manager is closed.
// Keep the exact accepted/uncertain request in tab storage across navigation.
export function mountSpaceReset() {
  if (spaceCapabilities().space_reset !== true || document.getElementById("space-reset-control")) return;
  const host=document.getElementById("setup-reset-host") || document.getElementById("general-settings") || document.querySelector("main");
  if (!host) return;
  const section=document.createElement("section");section.id="space-reset-control";section.className="space-reset-control";
  const button=document.createElement("button");button.type="button";button.textContent="重置此空间";button.className="text-button danger";
  const status=document.createElement("p");status.setAttribute("role","status");status.setAttribute("aria-live","polite");
  section.append(button,status);host.append(section);
  const dialog=document.createElement("dialog");dialog.className="space-reset-dialog";dialog.setAttribute("aria-label","重置此空间");
  const title=document.createElement("h2");title.textContent="重置此空间";
  const detail=document.createElement("p");detail.textContent="将清空此空间的聊天记录、Agent 上下文、附件、通知与待办、定时任务、规则、模型配置、平台连接和默认工作目录。指定的已有目录文件、Computer、系统登录及其他空间保留。此操作无法撤销。归档空间将重新进入首次使用。";
  if (spaceCapabilities().native_lark_login_preserved === true) detail.textContent += "系统保存的飞书登录会保留。";
  const confirm=document.createElement("button");confirm.type="button";confirm.textContent="确认重置";confirm.className="danger";
  const cancel=document.createElement("button");cancel.type="button";cancel.textContent="取消";
  dialog.append(title,detail,cancel,confirm);document.body.append(dialog);
  let current, pending, sending=false, active=true;
  const key=()=>`teamx.space-reset-pending:${current?.id || pageSpaceID}`;
  const remember=()=>{try{sessionStorage.setItem(key(),JSON.stringify(pending));}catch{}};
  const phases={stopping:"正在停止任务",cleaning:"正在清理",initializing:"正在初始化"};
  async function poll() {
    try {
      const response=await fetch("/v1/spaces",{cache:"no-store",signal:AbortSignal.timeout(5000)});
      if (!response.ok) throw Error("暂时无法核对空间状态，请重新检查。");
      const directory=await response.json();if(!active)return;
      const id=pageSpaceID || directory.default_space_id;
      current=directory.spaces?.find(s=>s.id===id);if(!current)return;
      if (!observeSpaceReset(current)) return;
      try { pending ||= JSON.parse(sessionStorage.getItem(key()) || "null"); } catch {}
      const op=current.reset;
      if (op && op.phase!=="completed") {
        pending={request_id:op.request_id,expected_version:op.expected_version};remember();
        status.textContent=`${phases[op.phase] || "重置需要核对"}${op.message ? `：${op.message}` : "，请稍候。"}`;
        button.textContent=current.lifecycle_busy ? "正在重置…" : "重试重置";
        button.disabled=current.lifecycle_busy;
      } else {
        const completed=op && pending?.request_id===op.request_id;
        let message=current.lifecycle_error || "";
        if (op?.phase==="completed" && (completed || ["setup_required","failed","suspended"].includes(current.runtime_state))) {
          if (current.status==="suspended") message=op.message || "重置已完成，空间已挂起。";
          else if (current.runtime_state==="failed") message="重置已完成，空间启动失败。可重试启动。";
          else if (current.runtime_state==="setup_required") message=current.capabilities?.reset_default_model ? "重置已完成，默认模型尚未就绪，请查看模型设置。" : "重置已完成，请重新设置模型。";
          else message="重置已完成。";
        }
        status.textContent=message;
        button.textContent=pending && !completed ? "核对并重试重置" : op?.phase==="completed"&&current.runtime_state==="failed" ? "重试启动" : "重置此空间";
        button.disabled=current.status==="archiving" || current.lifecycle_busy;
        if(completed && current.runtime_state!=="failed") {pending=undefined;try{sessionStorage.removeItem(key());}catch{}}
      }
    } catch(error) { if(active) status.textContent=error.message; }
    if(active)setTimeout(poll,1500);
  }
  async function submit() {
    if(sending || !current)return;
    sending=true;button.disabled=confirm.disabled=true;dialog.close();
    pending ||= {request_id:crypto.randomUUID().replaceAll("-",""),expected_version:current.version};remember();
    try {
      const response=await fetch(`/v1/spaces/${current.id}/reset`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(pending),signal:AbortSignal.timeout(15000)});
      const result=await response.json();
      if(!response.ok)throw Error(result.message || "未能受理重置，请核对后重试。");
      freezeSpaceCache();location.replace(`/s/${current.id}/`);
    } catch(error) {status.textContent=`${error.message} 将保留原请求，重试不会创建第二次重置。`;}
    finally {sending=false;confirm.disabled=button.disabled=false;}
  }
  button.addEventListener("click",()=>{
    if(pending || current?.reset && current.reset.phase!=="completed")void submit();
    else if(current?.reset?.phase==="completed" && current.runtime_state==="failed") {pending={request_id:current.reset.request_id,expected_version:current.reset.expected_version};void submit();}
    else dialog.showModal();
  });
  confirm.addEventListener("click",submit);cancel.addEventListener("click",()=>dialog.close());
  window.addEventListener("pagehide",()=>{active=false;});
  void poll();
}
