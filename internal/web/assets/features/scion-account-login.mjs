import { randomUUID } from "../random-id.mjs";
import { DEFAULT_SCION_ORIGIN, createScionInstanceOptions } from "./scion-instance.mjs";

export function scionPATURL(value) {
  try {
    const url=new URL(value);
    if(url.protocol==="https:" && !url.username && !url.password && url.pathname==="/" && !url.search && !url.hash && !/[\s\\?#]/.test(value))return `${url.origin}/profile?tab=tokens`;
  } catch {}
  return "";
}

// The password belongs only to the live control and the one login request.
// Auth/session/profile references never enter the conversation selection draft.
export function createScionAccountLogin({env,runtime,connectionsUI,current,onSaved,renderCard}) {
  const {document,request}=env,{state}=runtime,editors=new Set();
  const managerIdentity=()=>({service_epoch:state.catalog.service_epoch,caller:{kind:"human",id:state.operator}});
  const observing=flow=>["login","login_unknown","save_unknown","verify_unknown"].includes(flow?.stage);
  function attach(card) {
    const panel=document.createElement("div");panel.className="scion-card-login";panel.hidden=true;
    const originLabel=document.createElement("label"),originText=document.createElement("span"),origin=document.createElement("input");
    const origins=createScionInstanceOptions(document,`scion-origins-${card.op.operation_id}`);
    originText.textContent="Scion 实例地址";origin.type="url";origin.className="field-control";origin.placeholder=DEFAULT_SCION_ORIGIN;origin.setAttribute("aria-label","Scion 实例地址");origin.setAttribute("list",origins.id);originLabel.append(originText,origin,origins);
    const heading=document.createElement("div"),label=document.createElement("label"),pat=document.createElement("input"),link=document.createElement("a"),impact=document.createElement("p");
    heading.className="connection-pat-heading";label.textContent="访问令牌（PAT）";pat.id=`scion-pat-${card.op.operation_id}`;label.htmlFor=pat.id;
    pat.type="password";pat.autocomplete="new-password";pat.spellcheck=false;pat.className="field-control";pat.placeholder="填写 Scion PAT";pat.setAttribute("aria-label","Scion 访问令牌（PAT）");
    link.textContent="查看／创建 PAT ↗";link.target="_blank";link.rel="noopener noreferrer";link.referrerPolicy="no-referrer";
    impact.className="quiet";heading.append(label,link);panel.append(originLabel,heading,pat,impact);card.fields.append(panel);
    card.scionLogin={panel,originLabel,origin,pat,link,impact,open:false,connection:null,flow:null};editors.add(card);
    origin.addEventListener("input",()=>render(card));
    pat.addEventListener("keydown",event=>{if(event.key==="Enter" && !event.isComposing){event.preventDefault();if(!event.repeat)void start(card);}});
  }
  function open(card,connection=null,{focus=true}={}) {
    const e=card.scionLogin;if(!e || e.flow)return;
    e.pat.value="";e.error="";e.connection=connection;e.open=true;e.origin.value=connection?.url || card.op.input.origin || DEFAULT_SCION_ORIGIN;
    renderCard(card);
    if(focus)(e.origin.value ? e.pat : e.origin).focus();
  }
  function render(card) {
    const e=card.scionLogin;if(!e)return;
    const active=card.op.status==="waiting" && card.op.target_available;
    if(!active){e.pat.value="";e.open=false;if(card.op.status!=="waiting"){e.flow=null;e.error="";}}
    const flow=e.flow,busy=!!flow && !flow.stage.endsWith("unknown"),pending=observing(flow);
    e.panel.hidden=!active || !e.open;card.fields.hidden=e.panel.hidden;
    e.originLabel.hidden=!!e.connection || !!card.op.input.origin;e.origin.disabled=!!flow;
    e.pat.disabled=!!flow;e.pat.hidden=!!flow;
    e.link.href=scionPATURL(e.origin.value.trim());e.link.hidden=!e.link.href;
    const count=new Set((e.connection?.references || []).map(r=>`${r.room_id}:${r.room_instance}`)).size;
    e.impact.hidden=!e.originLabel.hidden;
    e.impact.textContent=`${e.connection ? `${e.connection.name} · ` : ""}${e.origin.value}${count ? `。重新登录将更新此连接，影响正在使用它的 ${count} 个会话。` : ""}`;
    if(e.open) {
      card.selected.hidden=true;
      card.login.disabled=busy;
      card.login.textContent=pending && flow.stage.endsWith("unknown") ? "核对连接结果" : busy ? ({starting:"正在登录…",login:"正在登录…",save:"正在保存…",verify:"正在核验…",selection:"正在继续接入…"}[flow.stage] || "正在核对…") : "登录并继续";
      card.hint.textContent="登录成功后自动保存连接并继续本次接入。PAT 不会写入聊天记录。";
    } else card.selected.hidden=false;
    card.loginChoices.hidden ||= !!flow;
  }
  function busy(card) {return !!card.scionLogin?.flow;}
  function pending() {return [...editors].some(card=>observing(card.scionLogin?.flow));}
  function isCurrent(card,flow) {return card.scionLogin?.flow===flow && current(flow.generation);}
  function fail(card,message,{retain=false}={}) {
    card.scionLogin.pat.value="";
    if(!retain)card.scionLogin.flow=null;
    card.scionLogin.error=message;renderCard(card);
  }
  async function start(card) {
    const e=card.scionLogin;if(!e?.open || card.op.status!=="waiting" || !card.op.target_available)return;
    if(e.flow){if(observing(e.flow))await runtime.observe("authorization:scion-card");return;}
    let pat=e.pat.value.trim();e.pat.value="";
    const url=e.origin.value.trim(),patURL=scionPATURL(url);
    if(!pat || !patURL){pat="";fail(card,"请填写有效的 HTTPS 实例地址和 PAT。");return;}
    const flow={stage:"starting",generation:current(),identity:managerIdentity(),session_id:randomUUID().replaceAll("-",""),connection:e.connection,
      id:e.connection?.id || randomUUID().replaceAll("-",""),version:e.connection?.version || 0,name:e.connection?.name || "Scion",url:new URL(patURL).origin,
      selectionVersion:card.editVersion,selectionDraft:JSON.stringify(card.draft)};
    e.flow=flow;e.error="";renderCard(card);
    try {
      if(flow.connection) {
        const data=await runtime.platformRead.read();if(!isCurrent(card,flow))return;
        const latest=data.platforms.find(c=>c.id===flow.id);
        if(!latest || latest.version!==flow.version){pat="";fail(card,"连接已变化，请刷新账号后重试。");return;}
      }
      try {
        await request("/v1/platforms/scion/login",{...flow.identity,session_id:flow.session_id,url:flow.url,pat,...(flow.connection && !card.scionManaged ? {executable:flow.connection.cli_profile.executable} : {})},undefined,15000);
      } catch(error) {
        if(!isCurrent(card,flow))return;
        if(["request","identity","forbidden","capacity","busy","cli_unavailable","cli_version_mismatch"].includes(error.code)) {
          fail(card,connectionsUI.connectionValidationErrors[error.code] || "登录未开始，请核对实例和 PAT 后重试。");return;
        }
        // Read the owned session after an uncertain start. Never replay the PAT.
      } finally {pat="";}
      if(isCurrent(card,flow)){flow.stage="login";await runtime.observe("authorization:scion-card");}
    } catch(error) {
      pat="";if(isCurrent(card,flow))fail(card,"账号暂时无法核对，请重试或打开完整设置。");
    }
  }
  function matching(c,flow) {return c?.id===flow.id && c.provider==="scion" && c.url===flow.url && c.version>flow.version && ["version","brand","profile","executable","cli_version"].every(key=>c.cli_profile?.[key]===flow.profile?.[key]);}
  async function finish(card,flow,saved) {
    if(!isCurrent(card,flow))return;
    card.scionLogin.error="";card.scionLogin.connection=saved;
    flow.stage="selection";renderCard(card);
    connectionsUI.updatePlatformItem(saved);
    await onSaved(card,saved,flow);
    if(!isCurrent(card,flow))return;
    card.scionLogin.flow=null;card.scionLogin.open=false;renderCard(card);
  }
  async function verify(card,flow,saved) {
    if(!isCurrent(card,flow))return;
    if(saved.validation?.status==="valid"){await finish(card,flow,saved);return;}
    flow.stage="verify";renderCard(card);
    try {
      const checked=await request("/v1/platforms/verify",{...flow.identity,id:saved.id,version:saved.version},undefined,40000);
      if(!isCurrent(card,flow))return;
      connectionsUI.updatePlatformItem(checked);card.scionLogin.connection=checked;
      if(checked.validation?.status!=="valid"){fail(card,"连接已保存，但账号核验失败。请在完整设置中核对后继续。");return;}
      await finish(card,flow,checked);
    } catch(error) {
      if(!isCurrent(card,flow))return;
      flow.stage="verify_unknown";fail(card,"连接已保存，核验结果未确认，正在核对原连接。",{retain:true});
      runtime.changed("authorization:scion-card");
    }
  }
  async function save(card,flow,profile) {
    if(!isCurrent(card,flow))return;
    flow.profile=profile;flow.stage="save";renderCard(card);
    const referenced_rooms=(flow.connection?.references || []).map(({room_id,room_instance})=>({room_id,room_instance}));
    try {
      const saved=await request("/v1/platforms/save",{...flow.identity,id:flow.id,version:flow.version,name:flow.name,provider:"scion",url:flow.url,cli_profile:profile,...(flow.connection ? {referenced_rooms} : {})},undefined,40000);
      if(!isCurrent(card,flow))return;
      if(!matching(saved,flow))throw Error("unexpected connection");
      connectionsUI.updatePlatformItem(saved);card.scionLogin.connection=saved;await verify(card,flow,saved);
    } catch(error) {
      if(!isCurrent(card,flow))return;
      if(["request","conflict","identity","forbidden","capacity"].includes(error.code)){fail(card,"登录已完成，但连接未保存；请在完整设置中核对原连接。");return;}
      flow.stage="save_unknown";fail(card,"登录已完成，保存结果未确认，正在核对原连接。",{retain:true});
      runtime.changed("authorization:scion-card");
    }
  }
  async function read({current:observationCurrent=()=>true,onFailure=()=>{}}={}) {
    await Promise.all([...editors].map(async card=>{
      const flow=card.scionLogin?.flow;if(!observing(flow) || flow.reading || !isCurrent(card,flow))return;
      flow.reading=true;
      try {
        if(flow.stage.startsWith("login")) {
          const result=await request("/v1/platforms/scion/status",{...flow.identity,session_id:flow.session_id});
          if(!isCurrent(card,flow) || !observationCurrent())return;
          if(result.status==="starting")return;
          if(result.status!=="succeeded" || !result.cli_profile){fail(card,connectionsUI.connectionValidationErrors[result.error_code] || "PAT 登录失败，请核对令牌后重试。");return;}
          await save(card,flow,result.cli_profile);
        } else {
          const data=await runtime.platformRead.read();
          if(!isCurrent(card,flow) || !observationCurrent())return;
          const saved=data.platforms.find(c=>c.id===flow.id);
          if(matching(saved,flow)) {
            if(saved.validation?.status==="valid")await finish(card,flow,saved);
            else if(flow.stage==="save_unknown")await verify(card,flow,saved);
          }
        }
      } catch(error) {
        if(isCurrent(card,flow) && observationCurrent()){
          if(flow.stage==="login")flow.stage="login_unknown";
          fail(card,"登录或连接结果暂不可读，请核对原操作；不会重复提交 PAT。",{retain:true});onFailure(error);
        }
      } finally {flow.reading=false;}
    }));
  }
  function reset() {for(const card of editors){card.scionLogin.pat.value="";card.scionLogin.flow=null;}editors.clear();}
  function detach(card) {if(!card.scionLogin)return;card.scionLogin.pat.value="";if(!card.scionLogin.flow)editors.delete(card);}
  function clearPasswords() {for(const card of editors)card.scionLogin.pat.value="";}
  return {attach,open,render,start,busy,pending,read,reset,detach,clearPasswords};
}
