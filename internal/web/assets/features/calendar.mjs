import { randomUUID } from "../random-id.mjs";
import { compactViewport } from "../client.mjs";
import { captureReturnView } from "../view-return.mjs";
import { calendarRange, calendarOffsetLabels, localDay, shiftDay, weekDates, monthDates, minuteOfDay, pointGroups } from "./calendar-dates.mjs";

// Reads use the Actor's committed projection. Opening or previewing never starts
// an Agent; mutations preserve the original operation and resource version.
export function createCalendar({ env, runtime, roomsUI, conversationUI, settingsUI }) {
  const { document, window, setTimeout, clearTimeout } = env, { $, state, rpc, currentView, notice } = runtime;
  let visible=false, value=null, pending=null, retryRequest=null, readID=0, detailID=0;
  let date="", mode=window.innerWidth<=640?"day":"week", compact=window.innerWidth<=640;
  let view=compact?"list":"calendar";
  let calendarScroll=0,calendarGridScroll=0,listScroll=0;
  let selected=null, detail=null, history=[], historyCursor="", editing=null, initialForm=null, formScope="", initialScroll=true;
  let lastOpener=null, detailReturn=null, renderedRange="", pickerMonth="", formFrequency=null;
  let selectedRun=null, runReadID=0;
  let detailOccurrence=null, nowTimer=null, workspaceScroll=0, workspaceGridScroll=0;
  let chatPosition=null;
  const hourHeight=56;
  const messageLinks=new Map();
  const labels={needs_info:"待完善",active:"已安排",paused:"已暂停",cancelled:"已取消",completed:"已结束",planned:"计划",queued:"等待 Agent",running:"执行中",publishing:"报告发送中",succeeded:"已完成",blocked:"受阻",failed:"失败",unknown:"结果未核实",skipped:"已错过"};
  const reasons={work_calendar_uncovered:"工作日历尚未覆盖，暂不执行",skipped_late:"超过补跑期限",skipped_overlap:"上一周期仍在执行",previous_result_unverified:"之前的执行结果尚未核实",user_skipped:"用户已跳过这一次",result_not_submitted:"Agent 未提交执行报告",definition_replaced:"后续安排已修改",delivery_pending_verification:"投递回执待核实",executor_unavailable:"执行者不可用",capacity:"存储容量不足"};
  const node=(tag,text,className)=>{const e=document.createElement(tag);if(text!==undefined)e.textContent=text;if(className)e.className=className;return e;};
  const button=(text,fn,className="text-button")=>{const e=node("button",text,className);e.type="button";e.addEventListener("click",fn);return e;};
  const formatters=new Map();
  function formatAt(at,options,tz){const key=JSON.stringify([options,tz]);if(!formatters.has(key))formatters.set(key,new Intl.DateTimeFormat("zh-CN",{...options,timeZone:tz,hourCycle:"h23"}));return formatters.get(key).format(new Date(at));}
  const zone=()=>value?.time_zone||settingsUI.systemTime?.zone||state.info?.time_zone||"Asia/Shanghai";
  const today=()=>localDay(Date.now(),zone());
  const available=()=>!!state.info&&roomsUI.isDirect();
  const fmt=(at,options={year:"numeric",month:"numeric",day:"numeric",hour:"2-digit",minute:"2-digit"},tz=zone())=>at?formatAt(at,options,tz):"待确定";
  const localHour=at=>Number(new Intl.DateTimeFormat("en-GB",{timeZone:zone(),hour:"2-digit",hourCycle:"h23"}).format(new Date(at)));
  const dayLabel=d=>`${Number(d.slice(5,7))}月${Number(d.slice(8))}日`;
  const reason=r=>reasons[r]||r;
  function ruleLabel(rule,includeZone=true) {
    const repeat=rule.frequency==="daily"?"每天":rule.frequency==="weekly"?`每周 ${(rule.weekdays||[]).map(d=>["","一","二","三","四","五","六","日"][d]).join("、")}`:"一次性";
    const filter={all_days:"含周末和节假日",weekdays:"周一至周五，含工作周内的法定假日",business_days:"中国大陆法定工作日，含调休补班"}[rule.day_filter||"all_days"];
    return `${repeat} ${rule.local_time||"时刻待补齐"}${rule.frequency!=="once"||rule.day_filter!=="all_days"?` · ${filter}`:""}${includeZone?` · ${rule.time_zone||"时区待确定"}`:""}${rule.end_date?` · 至 ${rule.end_date}（含当天）`:""}`;
  }
  function showError(message,retry=false){notice("calendar-error",message);notice("calendar-detail-error",message);$("calendar-retry").hidden=!retry;}
  function updateCalendarIcon(){
    const day=today(),label=`日历 · 今天 ${dayLabel(day)}`;
    $("calendar-icon-day").textContent=String(Number(day.slice(8)));
    $("conversation-calendar").setAttribute("aria-label",label);$("conversation-calendar").title=label;
  }
  function syncCreateButton(){
    const icon=compactViewport(window),create=$("calendar-new");
    create.classList.toggle("add-button",icon);create.classList.toggle("primary",!icon);create.classList.toggle("button-compact",!icon);
    $("calendar-new-label").hidden=icon;
    if(icon)$("calendar-new-icon").removeAttribute("hidden");else $("calendar-new-icon").setAttribute("hidden","");
  }
  function syncDetailNavigation(){
    const label=compactViewport(window)?"返回日历":"关闭任务详情";
    $("calendar-detail-close").setAttribute("aria-label",label);$("calendar-detail-close").title=label;
  }
  function sync(){ $("conversation-calendar").hidden=!available();updateCalendarIcon();syncCreateButton();if(!available())setVisible(false);else scheduleClock(); }
  function setOptionsOpen(open,focus=false){
    $("calendar-options-menu").hidden=!open;$("calendar-options-toggle").setAttribute("aria-expanded",String(open));
    if(focus)$("calendar-options-toggle").focus({preventScroll:true});
  }
  function syncView(){
    const calendar=view==="calendar";
    $("calendar-panel").dataset.view=view;
    $("calendar-navigation").hidden=!calendar;$("calendar-mode").hidden=!calendar;
    $("calendar-items").hidden=!calendar;$("calendar-tasks").hidden=calendar;$("calendar-list-controls").hidden=calendar;
    $("calendar-show-skipped").closest("label").hidden=!calendar;
    $("calendar-view").dataset.view=view;
    for(const b of $("calendar-view").querySelectorAll("button"))b.setAttribute("aria-pressed",String(b.dataset.view===view));
  }
  function switchView(next){
    if(next===view)return;
    if(view==="calendar"){calendarScroll=$("calendar-panel").scrollTop;calendarGridScroll=$("calendar-items").scrollTop;}else listScroll=$("calendar-panel").scrollTop;
    view=next;$("calendar-date-picker").open=false;syncView();
    if(view==="calendar")$("calendar-items").scrollTop=calendarGridScroll;
    $("calendar-panel").scrollTop=view==="calendar"?calendarScroll:listScroll;
    void refresh().catch(()=>{});
  }
  function setVisible(next) {
    const was=visible;visible=next&&available();
    if(visible&&!was)chatPosition=conversationUI.capturePosition();
    $("calendar-panel").hidden=!visible;$("timeline-wrap").hidden=visible;$("compose").hidden=visible;
    $("conversation-chat").hidden=!visible;$("conversation").classList.toggle("calendar-open",visible);
    $("room-heading").hidden=visible;$("calendar-heading").hidden=!visible;
    $("conversation-actions").hidden=visible;$("calendar-header-actions").hidden=!visible;
    if(!visible)setOptionsOpen(false);
    $("conversation-calendar").setAttribute("aria-expanded",String(visible));
    if(visible){conversationUI.saveDraft();if(!date)date=today();syncView();if(value)render();runtime.wake("calendar");void runtime.observe("calendar");}
    else {if(was){closeDetail(false);if(chatPosition)conversationUI.restorePosition(chatPosition);chatPosition=null;}conversationUI.scheduleMessageViewing();}
    scheduleClock();
  }
  function reset(){
    chatPosition=null;
    workspaceScroll=0;workspaceGridScroll=0;calendarScroll=0;calendarGridScroll=0;listScroll=0;renderedRange="";messageLinks.clear();readID++;detailID++;pending=null;retryRequest=null;value=null;date="";selected=null;detail=null;editing=null;history=[];initialScroll=true;
    $("calendar-date-picker").open=false;closeDetail(false);resetForm();sync();setVisible(false);$("calendar-items").replaceChildren();$("calendar-task-items").replaceChildren();$("calendar-task-count").textContent="0";showError("");
  }
  async function refresh({current=()=>true,displayZone=zone()}={}) {
    sync();if(!available())return;
    const version=runtime.viewVersion,id=++readID;
    if(!date)date=today();
    const range=calendarRange(date,mode,displayZone),query=view==="calendar"?{from:range.from,to:range.to}:{};
    try {
      let data,occurrences=[],cursor="",attempt=0;
      // Retry a complete read after a changed snapshot; never combine pages from
      // different revisions or show a silently truncated week.
      for(;;){
        try {
          do {
            data=await rpc("/v1/calendar/list",{calendar:{...query,...(cursor?{cursor}: {})}});
            if(!currentView(version)||!current()||id!==readID)return;
            // Every management page contains the complete task set and links.
            // The cursor only paginates occurrences, which the task view omits.
            if(view==="calendar")occurrences.push(...data.occurrences);
            cursor=view==="calendar"?data.next_cursor||"":"";
          }while(cursor);
          break;
        }catch(error){if(error.field!=="cursor"||attempt++>=1)throw error;occurrences=[];cursor="";}
      }
      // A system-zone change can race the range read. Rebase the local dates
      // before presenting the new zone, so boundary occurrences are not lost.
      if(data.time_zone&&data.time_zone!==displayZone)return refresh({current,displayZone:data.time_zone});
      const changed=value?.revision!==data.revision,rangeKey=JSON.stringify([view,view==="calendar"?date:"",view==="calendar"?mode:"",data.time_zone]);
      value={...data,occurrences};renderLinks();if(visible&&(changed||renderedRange!==rangeKey)){render();renderedRange=rangeKey;}showError("");
      if(visible&&selected&&!editing&&changed)await loadDetail(selected, false);
    }catch(error){if(currentView(version)&&id===readID)showError(error.message,true);throw error;}
  }
  function renderLinks(){
    // Links are scoped to retained, committed source/report receipts in this DM.
    if(!value)return;
    const grouped=new Map();
    for(const link of value?.message_links||[]){if(!grouped.has(link.message_seq))grouped.set(link.message_seq,[]);grouped.get(link.message_seq).push(link);}
    for(const [seq,old] of messageLinks)if(!grouped.has(seq)){old.row.remove();messageLinks.delete(seq);}
    for(const [seq,links] of grouped){
      const message=document.getElementById(`message-${seq}`);if(!message)continue;
      const key=JSON.stringify(links.map(link=>[link,value.tasks.find(t=>t.task_id===link.task_id)?.definition.title]));
      const old=messageLinks.get(seq);if(old?.key===key&&old.row.parentElement===(message.querySelector(".message-main")||message))continue;
      old?.row.remove();const row=node("div",undefined,"calendar-message-links");messageLinks.set(seq,{key,row});
      for(const link of links){const task=value.tasks.find(t=>t.task_id===link.task_id);if(!task)continue;
        row.append(button(`${link.run_id?"查看执行安排":"查看日历"} · ${task.definition.title}`,()=>{setVisible(true);void openDetail(link.task_id,link.run_id);}));}
      (message.querySelector(".message-main")||message).append(row);
    }
  }
  function status(o){return o.reason==="user_skipped"?"已跳过":labels[o.status]||o.status;}
  function taskButton(task,text){const b=button(text,()=>void openDetail(task.task_id),"calendar-task-link");b.dataset.taskId=task.task_id;b.disabled=!!pending;return b;}
  function occurrenceButton(o){
    const task=value.tasks.find(t=>t.task_id===o.task_id),d=o.run?.task.definition||o.missed_range?.task.definition||task.definition;
    const b=button("",()=>void openDetail(o.task_id,o.run?.run_id||"",o),"calendar-occurrence");b.dataset.occurrenceId=o.occurrence_id;b.dataset.taskId=o.task_id;b.dataset.status=o.status;b.disabled=!!pending;
    const time=node("time",fmt(o.due_at,{hour:"2-digit",minute:"2-digit"}));time.dateTime=o.due_at;
    b.append(time,node("strong",d.title),node("span",`${d.schedule.frequency!=="once"?"↻ · ":""}${status(o)}`,"calendar-status"));
    b.title=`${d.title} · ${status(o)}${o.reason?` · ${reason(o.reason)}`:""}`;
    return b;
  }
  function dayKind(d){return value?.days?.find(day=>day.date===d)?.kind||"";}
  function dayMarker(d){return {holiday:"休",makeup:"补",unknown:"未覆盖"}[dayKind(d)]||"";}
  function dayDescription(d){return {holiday:"节假日",makeup:"补班",weekend:"周末",unknown:"工作日历未覆盖"}[dayKind(d)]||"";}
  function entries(){return (value?.occurrences||[]).filter(o=>$("calendar-show-skipped").checked||!["skipped","cancelled"].includes(o.status));}
  function render(){
    if(!value)return;
    if($("calendar-create").open){syncForm();$("calendar-submit").disabled=!!pending;$("calendar-save-draft").disabled=!!pending;return;}
    const boundary=value.work_calendars?.at(-1)?.to;
    $("calendar-coverage").classList.toggle("calendar-coverage-warning",value.coverage_incomplete||!boundary);
    notice("calendar-coverage",value.coverage_incomplete?"部分日期的工作日历尚未覆盖；工作日任务暂不执行。":boundary?`中国大陆工作日历覆盖至 ${boundary}${new Date(boundary)-Date.now()<90*86400000?"，请留意更新":""}。`:"工作日历尚未覆盖；每天和周一至周五安排仍可执行。");
    syncView();const range=calendarRange(date,mode,zone());
    $("calendar-range-label").textContent=mode==="day"?`${compact?"":`${date.slice(0,4)}年 `}${dayLabel(date)}`:`${range.dates[0].slice(0,4)}年 ${dayLabel(range.dates[0])} — ${dayLabel(range.dates.at(-1))}`;
    $("calendar-range").title=`${date.slice(0,4)}年 ${dayLabel(date)}`;
    $("calendar-date").value=date;
    $("calendar-mode").dataset.mode=mode;
    for(const b of $("calendar-mode").querySelectorAll("button"))b.setAttribute("aria-pressed",String(b.dataset.mode===mode));
    $("calendar-submit").disabled=!!pending;$("calendar-save-draft").disabled=!!pending;
    const root=$("calendar-items"),scroll=root.scrollTop;
    const occurrences=entries();
    if(view==="calendar"){root.replaceChildren();renderGrid(root,range.dates,occurrences);root.scrollTop=scroll;}
    const shelf=$("calendar-task-items");shelf.replaceChildren();
    const filter=$("calendar-task-filter").value||"open";
    const tasks=value.tasks.filter(t=>filter==="all"||filter==="open"&&["active","paused","needs_info"].includes(t.state)||t.state===filter);
    for(const task of tasks){
      const row=node("div",undefined,"calendar-task-row");row.dataset.taskId=task.task_id;
      row.append(taskButton(task,task.definition.title),node("span",task.blocked_reason?reason(task.blocked_reason):labels[task.state],"calendar-status"));
      const rule=task.definition.schedule;
      row.append(node("small",`${rule.frequency==="once"?`${rule.start_date||"日期待补齐"} · `:""}${ruleLabel(rule)}`));
      if(task.state==="active"&&task.next_at)row.append(node("small",`下次 ${fmt(task.next_at)}${rule.time_zone&&rule.time_zone!==zone()?` · ${zone()}`:""}`));shelf.append(row);
    }
    if(!tasks.length)shelf.append(node("p","暂无此类任务。","calendar-empty"));
    $("calendar-task-count").textContent=String(tasks.length);
    if(initialScroll&&view==="calendar"){
      const first=occurrences.find(o=>localDay(o.due_at,zone())===today())||occurrences[0];
      const hour=range.dates.includes(today())?localHour(Date.now()):first?localHour(first.due_at):9;
      root.scrollTop=Math.max(0,(hour-1)*hourHeight);initialScroll=false;
    }
  }
  function renderGrid(root,dates,occurrences){
    const grid=node("div",undefined,"calendar-time-grid"),headers=node("div",undefined,"calendar-grid-headers");
    grid.style.setProperty("--day-count",String(dates.length));grid.dataset.mode=mode;
    const corner=node("div",undefined,"calendar-grid-corner");corner.title=zone();corner.setAttribute("aria-label",`显示时区 ${zone()}`);
    for(const offset of calendarOffsetLabels(date,mode,zone()))corner.append(node("span",offset));
    headers.append(corner);
    for(const d of dates){
      const head=button("",()=>navigate(d,"day"),"calendar-day-heading");head.dataset.today=String(d===today());head.dataset.kind=dayKind(d);
      head.setAttribute("aria-label",`${d}${d===today()?"，今天":""}${dayDescription(d)?`，${dayDescription(d)}`:""}`);
      head.title=dayDescription(d);
      const dayNumber=node("div",undefined,"calendar-heading-date");dayNumber.append(node("strong",String(Number(d.slice(8)))));
      if(dayMarker(d))dayNumber.append(node("small",dayMarker(d)));
      head.append(node("span",`周${["一","二","三","四","五","六","日"][weekDates(d).indexOf(d)]}`),dayNumber);headers.append(head);
    }
    const body=node("div",undefined,"calendar-time-body"),axis=node("div",undefined,"calendar-time-axis");
    for(let hour=0;hour<24;hour++){const tick=node("div",undefined,"calendar-hour");tick.append(node("span",`${String(hour).padStart(2,"0")}:00`));axis.append(tick);}
    body.append(axis);
    for(const d of dates){
      const column=node("div",undefined,"calendar-day-column");column.dataset.day=d;
      const points=occurrences.filter(o=>localDay(o.due_at,zone())===d);
      for(const group of pointGroups(points,zone(),hourHeight)){
        let item;
        if(group.items.length<=2){
          for(const [index,point] of group.items.entries()){
            const b=occurrenceButton(point);b.style.setProperty("--point-top",`${minuteOfDay(point.due_at,zone())*hourHeight/60}px`);
            if(group.items.length===2){b.style.setProperty("left",`calc(${index*50}% + 3px)`);b.style.setProperty("width","calc(50% - 6px)");}
            column.append(b);
          }
          continue;
        } else {
          item=node("details",undefined,"calendar-cluster");
          const summary=node("summary",`${fmt(group.items[0].due_at,{hour:"2-digit",minute:"2-digit"})} · ${group.items.length} 项安排`),list=node("div",undefined,"calendar-cluster-list");
          for(const o of group.items)list.append(occurrenceButton(o));item.append(summary,list);
        }
        item.style.setProperty("--point-top",`${group.top}px`);column.append(item);
      }
      body.append(column);
    }
    grid.append(headers,body);root.append(grid);updateNow();scheduleClock();
  }
  function scheduleClock(){
    clearTimeout(nowTimer);
    if(available())nowTimer=setTimeout(()=>{
      if(!document.hidden){updateCalendarIcon();if(visible&&view==="calendar"&&!$("calendar-create").open)updateNow();}
      scheduleClock();
    },Math.min(30000,60000-Date.now()%60000));
  }
  function updateNow(){
    const body=$("calendar-items").querySelector(".calendar-time-body");if(!body)return;
    body.querySelector(".calendar-now-line")?.remove();
    const dates=calendarRange(date,mode,zone()).dates,index=dates.indexOf(today());if(index<0)return;
    const line=node("div",undefined,"calendar-now-line"),label=node("span",fmt(Date.now(),{hour:"2-digit",minute:"2-digit"}));
    line.style.setProperty("--now-top",`${minuteOfDay(Date.now(),zone())*hourHeight/60}px`);
    const dot=node("i");dot.style.setProperty("--today-column",String(index));line.append(label,dot);line.setAttribute("aria-hidden","true");body.append(line);
  }
  function renderPicker(offset=0){
    const month=monthDates(pickerMonth||date||today(),offset);pickerMonth=month.month;
    $("calendar-month-label").textContent=`${pickerMonth.slice(0,4)}年 ${Number(pickerMonth.slice(5,7))}月`;
    const root=$("calendar-month-days");root.replaceChildren();
    for(const label of ["一","二","三","四","五","六","日"])root.append(node("span",label));
    for(const day of month.dates){const b=button(String(Number(day.slice(8))),()=>{navigate(day);$("calendar-date-picker").open=false;$("calendar-range").focus();},"calendar-picker-day");
      b.dataset.date=day;b.dataset.currentMonth=String(day.slice(0,7)===pickerMonth.slice(0,7));b.setAttribute("aria-label",`${day}${day===today()?"，今天":""}`);b.setAttribute("aria-pressed",String(day===date));if(day===today())b.setAttribute("aria-current","date");root.append(b);}
  }
  function navigate(next,nextMode=mode){$("calendar-date-picker").open=false;date=next;mode=nextMode;initialScroll=true;void refresh().catch(()=>{});}
  function closeDetail(focus=true){
    const taskID=selected,occurrenceID=lastOpener?.dataset?.occurrenceId,back=detailReturn;detailReturn=null;
    detailID++;runReadID++;$("calendar-detail").close();$("calendar-detail").hidden=true;selected=null;detail=null;detailOccurrence=null;selectedRun=null;history=[];
    if(focus){
      const candidate=(occurrenceID&&$("calendar-items").querySelector(`[data-occurrence-id="${occurrenceID}"]`))||$("calendar-task-items").querySelector(`[data-task-id="${taskID}"]`);
      const fallback=candidate?.getClientRects?.().length?candidate:$("calendar-view").querySelector(`[data-view="${view}"]`);
      if(!back?.restore(fallback))fallback.focus({preventScroll:true});
    }
  }
  async function openDetail(taskID,runID="",occurrence=null){
    showError("");detailOccurrence=occurrence;selectedRun=null;runReadID++;lastOpener=document.activeElement;selected=taskID;
    detailReturn=captureReturnView(env,runtime,{valid:()=>visible,scroll:["calendar-panel","calendar-items"]});
    $("calendar-detail").hidden=false;if(!$("calendar-detail").open)$("calendar-detail").showModal();$("calendar-detail-content").replaceChildren(node("p","正在读取任务…"));
    $("calendar-detail").scrollTop=0;
    $("calendar-detail-close").focus();
    await loadDetail(taskID,true,runID,occurrence);
  }
  async function loadDetail(taskID,reportError=true,runID="",occurrence=null){
    const id=++detailID,version=runtime.viewVersion;
    try {
      const result=await rpc("/v1/calendar/get",{calendar:{task_id:taskID,history:true}});
      if(!currentView(version)||id!==detailID||selected!==taskID)return;
      detail=result;history=result.history||[];historyCursor=result.next_cursor||"";renderDetail(occurrence||detailOccurrence);
      const resultID=runID||selectedRun?.run_id||history.find(run=>run.run_id)?.run_id;
      if(resultID)await showRun(resultID,false);
    }catch(error){if(currentView(version)&&id===detailID&&reportError)showError(error.message);}
  }
  function fact(root,label,text){const row=node("div",undefined,"calendar-fact");row.append(node("dt",label),node("dd",text));root.append(row);}
  function renderDetail(occurrence=detailOccurrence){
    if(!detail)return;const task=detail,d=task.definition,root=$("calendar-detail-content");
    const settingsOpen=root.querySelector(".calendar-execution-settings")?.open||false;
    const historyOpen=root.querySelector(".calendar-history-section")?.open;
    const resultSection=root.querySelector(".calendar-run-detail")||node("section",undefined,"calendar-run-detail");
    root.replaceChildren();
    const stateLabel=node("p",labels[task.state],"calendar-status");stateLabel.dataset.state=task.state;root.append(stateLabel,node("h2",d.title));
    const when=occurrence?.due_at||(task.state==="active"?task.next_at:null)||(d.schedule.frequency==="once"?task.first_at:null);
    root.append(node("p",when?`${!occurrence&&d.schedule.frequency!=="once"?"下次 ":""}${fmt(when)} · ${d.schedule.frequency==="once"?"一次性":d.schedule.frequency==="daily"?"每天":"每周"}`:task.state==="needs_info"?"时间待完善":ruleLabel(d.schedule,false),"calendar-detail-when"));
    if(when&&d.schedule.frequency!=="once")root.append(node("p",ruleLabel(d.schedule,false),"calendar-hint"));
    root.append(node("p",`${d.schedule.time_zone||zone()}${d.schedule.time_zone&&d.schedule.time_zone!==zone()?` · 当前显示时区 ${zone()}`:""}`,"calendar-hint"));
    root.append(node("p",d.instruction,"calendar-instruction"));
    if(task.blocked_reason)root.append(node("p",reason(task.blocked_reason),"notice"));
    if(occurrence&&occurrence.status!=="planned"&&!occurrence.run)root.append(node("p",`本次：${status(occurrence)}${occurrence.reason?` · ${reason(occurrence.reason)}`:""}`,"calendar-hint"));
    const actions=node("div",undefined,"calendar-actions"),more=node("details",undefined,"calendar-more"),menu=node("div",undefined,"calendar-more-menu");more.append(node("summary","更多"),menu);
    if(!["cancelled","completed"].includes(task.state)){const edit=button(d.schedule.frequency==="once"?"编辑":"编辑后续安排",()=>startEdit(task),"secondary-button button-compact");edit.disabled=!!pending;actions.append(edit);}
    if(["active","paused"].includes(task.state)){const b=button(task.state==="paused"?"恢复":"暂停",()=>void change(task,task.state==="paused"?"resume":"pause"));b.disabled=!!pending;actions.append(b);}
    if(!["cancelled","completed"].includes(task.state)){
      if(task.state==="active"&&task.next_at&&new Date(task.next_at)>new Date()){const b=button(d.schedule.frequency==="once"?"跳过这一次":"跳过下一次",()=>void change(task,"skip_once"));b.disabled=!!pending;menu.append(b);}
      const cancel=button("取消任务",()=>void change(task,"cancel"),"text-button danger-button");cancel.disabled=!!pending;menu.append(cancel);actions.append(more);
    }
    root.append(actions);
    root.append(resultSection);renderRun();
    const settings=node("details",undefined,"calendar-disclosure calendar-execution-settings");settings.open=settingsOpen;settings.append(node("summary","执行设置"));
    const facts=node("dl",undefined,"calendar-facts");fact(facts,"执行规则",ruleLabel(d.schedule));
    if(d.schedule.frequency!=="once"){fact(facts,"开始日期",d.schedule.start_date||"待完善");fact(facts,"首次执行",firstAt(task));}
    if(occurrence&&task.next_at&&occurrence.due_at!==task.next_at)fact(facts,"下次执行",fmt(task.next_at));
    fact(facts,"错过后处理",d.misfire==="skip"?"错过即跳过":"一小时内补最近一次");
    if(d.scope)fact(facts,"执行范围",scopeText(d.scope));
    if(d.schedule.excluded_dates?.length)fact(facts,"已跳过",d.schedule.excluded_dates.join("、"));
    settings.append(facts);
    if(d.schedule.day_filter==="business_days"){
      settings.append(node("h3","工作日历来源"));
      for(const c of value?.work_calendars||[]){const p=node("p",`${c.from} 至 ${c.to} · ${c.version} · 发布于 ${c.published} · `),a=node("a","官方来源");a.href=c.source;a.target="_blank";a.rel="noreferrer";p.append(a);settings.append(p);}
    }
    settings.append(node("p","服务需保持运行。暂停执行中的任务会请求停止对应回合，已完成的动作无法撤回。","calendar-hint"));root.append(settings);
    if(task.creation_source?.message_seq)root.append(button("查看创建消息",()=>void jumpReport(task.creation_source.message_seq),"text-button button-compact calendar-source"));
    const historySection=node("details",undefined,"calendar-disclosure calendar-history-section");historySection.open=historyOpen??false;
    historySection.append(node("summary",history.length?"执行历史": "执行历史 · 尚未执行"));
    if(history.length)historySection.append(node("p","最近 90 天，最多 5,000 条；更早明细可能已清理。","calendar-hint"));
    const list=node("div",undefined,"calendar-history");
    for(const run of history){const row=node("article",undefined,"calendar-history-row");row.dataset.runId=run.run_id;
      row.append(node("strong",`${fmt(run.due_at)} · ${labels[run.status]||run.status}`));
      if(run.kind==="missed_range")row.append(node("p",`至 ${fmt(run.until_at)}，共 ${run.count} 次漏跑`));
      if(run.definition.title!==d.title)row.append(node("p",run.definition.title));if(run.reason)row.append(node("p",reason(run.reason)));
      if(run.work_calendar_version)row.append(node("small",`工作日历版本 ${run.work_calendar_version}`));
      if(run.run_id)row.append(button("查看本次详情",()=>void showRun(run.run_id)));
      if(run.report_seq)row.append(button("查看私信报告",()=>void jumpReport(run.report_seq)));list.append(row);
    }
    historySection.append(list);if(historyCursor)historySection.append(button("加载更早执行",()=>void moreHistory()));root.append(historySection);
  }
  function firstAt(task){return task.first_at?`${fmt(task.first_at)}（本次生效后）`:"尚未确定";}
  function scopeText(scope){if(!scope)return "未单独指定；执行时需核实必要范围";return typeof scope.context==="string"&&Object.keys(scope).length===1?scope.context:JSON.stringify(scope,null,2);}
  async function moreHistory(){
    const id=detailID,taskID=selected,version=runtime.viewVersion;
    try{const data=await rpc("/v1/calendar/get",{calendar:{task_id:taskID,history:true,cursor:historyCursor}});
      if(!currentView(version)||id!==detailID||selected!==taskID)return;history.push(...data.history);historyCursor=data.next_cursor||"";renderDetail();
    }catch(error){if(currentView(version)&&id===detailID)showError(`历史读取失败，请重新打开任务核对：${error.message}`);}
  }
  function renderRun(){
    const root=$("calendar-detail-content").querySelector(".calendar-run-detail"),run=selectedRun,latest=history.find(item=>item.run_id);
    const isExpanded=selector=>root.dataset.runId===run?.run_id&&!!root.querySelector(selector)?.open;
    const reportOpen=isExpanded(".calendar-report-section"),snapshotOpen=isExpanded(".calendar-run-snapshot"),attemptsOpen=isExpanded(".calendar-run-attempts");
    root.dataset.runId=run?.run_id||"";
    root.replaceChildren();root.hidden=!run&&!latest;if(root.hidden)return;
    if(!run){
      root.append(node("h3","最近执行"),node("p",`${fmt(latest.due_at)} · ${labels[latest.status]||latest.status}`),button("查看结果",()=>void showRun(latest.run_id)));
      if(latest.report_seq)root.append(button("查看私信报告",()=>void jumpReport(latest.report_seq)));return;
    }
    root.append(node("h3","本次执行"),node("p",`${fmt(run.due_at)} · ${run.phase==="publishing"?labels.publishing:labels[run.status]||run.status}`));
    if(run.reason)root.append(node("p",reason(run.reason),"notice"));
    if(run.report){
      const report=run.report.trim(),brief=report.replace(/\s+/g," "),expanded=brief.length>80||report.split("\n").length>3;
      root.append(node("p",expanded?`${brief.slice(0,80)}${brief.length>80?"…":""}`:report,"calendar-report calendar-report-preview"));
      if(expanded){const full=node("details",undefined,"calendar-disclosure calendar-report-section");full.open=reportOpen;full.append(node("summary","查看完整报告"),node("p",report,"calendar-report"));root.append(full);}
    }
    if(run.publication?.status==="published")root.append(button("查看私信报告",()=>void jumpReport(run.publication.seq)));
    const snapshot=node("details",undefined,"calendar-disclosure calendar-run-snapshot"),definition=run.task.definition;
    snapshot.open=snapshotOpen;
    snapshot.append(node("summary","执行时任务内容"),node("h4",definition.title),node("p",ruleLabel(definition.schedule)),node("p",definition.instruction),node("pre",scopeText(definition.scope)));root.append(snapshot);
    if(run.previous_attempts?.length){
      const attempts=node("details",undefined,"calendar-disclosure calendar-run-attempts");attempts.open=attemptsOpen;attempts.append(node("summary",`此前尝试 · ${run.previous_attempts.length} 次`));
      for(const attempt of run.previous_attempts)attempts.append(node("p",`第 ${attempt.number} 次 · ${labels[attempt.status]||attempt.status}${attempt.reason?` · ${reason(attempt.reason)}`:""}`),node("p",attempt.report||"未保存报告","calendar-report"));root.append(attempts);
    }
  }
  async function showRun(runID,scroll=true){
    const id=detailID,request=++runReadID,version=runtime.viewVersion;
    try{const run=await rpc("/v1/calendar/get",{calendar:{run_id:runID}});if(!currentView(version)||id!==detailID)return;
      if(request!==runReadID)return;selectedRun=run;renderRun();
      if(scroll)$("calendar-detail-content").querySelector(".calendar-run-detail").scrollIntoView({block:"nearest"});
    }catch(error){if(currentView(version)&&id===detailID&&request===runReadID)showError(error.message);}
  }
  async function jumpReport(seq){setVisible(false);await conversationUI.locateMessage(seq);}
  async function submit(request){
    if(pending)return false;const version=runtime.viewVersion,key=JSON.stringify({...request,operation_id:undefined});
    if(retryRequest?.key===key)request=retryRequest.request;else retryRequest={key,request};
    pending=request;$("calendar-submit").disabled=true;if(visible)render();if(detail)renderDetail();
    try{await rpc("/v1/calendar/update",{calendar:request});if(!currentView(version))return false;retryRequest=null;pending=null;
      try{await refresh();}catch(error){if(currentView(version))showError(`任务已保存，刷新暂未成功：${error.message}`,true);}return true;
    }catch(error){if(currentView(version))showError(error.code==="version_conflict"||error.code==="conflict"?`安排已被修改。请保留当前草稿，重新打开任务核对后再编辑。${error.message}`:`未确认保存结果；重试将复用原请求。${error.message}`);return false;}
    finally{if(currentView(version)){pending=null;$("calendar-submit").disabled=false;if(visible)render();if(detail)renderDetail();}}
  }
  async function change(task,action){
    const request={action,operation_id:randomUUID(),expected_version:task.version,task_id:task.task_id,human_source:{}};
    if(action==="skip_once")request.local_date=localDay(task.next_at,task.definition.schedule.time_zone);
    await submit(request);
  }
  function formDefinition(){
    const schedule={frequency:$("calendar-frequency").value,local_time:$("calendar-local-time").value,day_filter:$("calendar-frequency").value==="once"?$("calendar-once-filter").value:$("calendar-day-filter").value};
    for(const [key,id] of [["start_date","calendar-start-date"],["end_date","calendar-end-date"],["time_zone","calendar-time-zone"]])if($(id).value)schedule[key]=$(id).value;
    if(schedule.frequency==="weekly")schedule.weekdays=Array.from($("calendar-weekdays").querySelectorAll("input:checked"),e=>Number(e.value));
    if(schedule.day_filter==="business_days")schedule.work_calendar="CN";
    if(editing?.definition.schedule.excluded_dates)schedule.excluded_dates=editing.definition.schedule.excluded_dates;
    const d={title:$("calendar-title").value,instruction:$("calendar-instruction").value,schedule,misfire:$("calendar-misfire").value};
    const scope=$("calendar-scope").value.trim();
    // Preserve verified structured scope exactly unless the user changes it.
    if(editing&&scope===formScope)d.scope=editing.definition.scope??null;
    else if(scope){try{d.scope=JSON.parse(scope);if(!d.scope||Array.isArray(d.scope)||typeof d.scope!=="object")throw Error();}catch{d.scope={context:scope};}}
    else d.scope=null;
    return d;
  }
  function startEdit(task){
    closeDetail(false);editing=task;retryRequest=null;const d=task.definition;
    for(const [key,id] of [["title","calendar-title"],["instruction","calendar-instruction"],["misfire","calendar-misfire"]])$(id).value=d[key]|| (key==="misfire"?"latest_within_one_hour":"");
    for(const [key,id] of [["frequency","calendar-frequency"],["day_filter","calendar-day-filter"],["start_date","calendar-start-date"],["end_date","calendar-end-date"],["local_time","calendar-local-time"],["time_zone","calendar-time-zone"]])$(id).value=d.schedule[key]||"";
    for(const box of $("calendar-weekdays").querySelectorAll("input"))box.checked=d.schedule.weekdays?.includes(Number(box.value))||false;
    formScope=scopeText(d.scope);if(!d.scope)formScope="";$("calendar-scope").value=formScope;
    $("calendar-once-filter").value=d.schedule.day_filter||"all_days";formFrequency=null;
    $("calendar-form-heading").textContent=d.schedule.frequency==="once"?"编辑任务":"编辑后续安排";$("calendar-submit").textContent="保存修改";$("calendar-form-cancel").hidden=false;$("calendar-form-cancel").textContent="放弃修改";
    initialForm=formDefinition();syncForm();$("calendar-advanced").open=false;$("calendar-create").open=true;$("calendar-create").scrollIntoView({block:"start"});$("calendar-title").focus();
  }
  function resetForm(){editing=null;initialForm=null;formScope="";formFrequency=null;$("calendar-form").reset();$("calendar-form-heading").textContent="新建定时任务";$("calendar-submit").textContent="保存任务";$("calendar-form-cancel").textContent="取消";$("calendar-create").open=false;$("calendar-advanced").open=false;syncForm();}
  function syncForm(){
    const frequency=$("calendar-frequency").value,once=frequency==="once";
    if(formFrequency&&formFrequency!==frequency){if(once)$("calendar-once-filter").value=$("calendar-day-filter").value;else if(formFrequency==="once")$("calendar-day-filter").value=$("calendar-once-filter").value;}
    formFrequency=frequency;
    $("calendar-weekdays").hidden=frequency!=="weekly";$("calendar-filter-field").hidden=once;$("calendar-end-field").hidden=once;$("calendar-once-filter-field").hidden=!once;
    $("calendar-start-label").textContent=once?"执行日期":"开始日期";$("calendar-start-hint").hidden=once;
    $("calendar-start-date").required=once;$("calendar-local-time").required=true;
    $("calendar-save-draft").hidden=!!editing&&editing.state!=="needs_info";
    const filter=once?$("calendar-once-filter").value:$("calendar-day-filter").value;
    $("calendar-filter-hint").textContent=filter==="weekdays"?"工作周内的法定假日仍执行。":filter==="business_days"?"包含调休补班，覆盖不足时暂不执行。":"";
    $("calendar-form-summary").textContent=`按 ${$("calendar-time-zone").value||zone()} 执行；${$("calendar-misfire").value==="skip"?"错过即跳过":"错过后一小时内补最近一次"}。服务需保持运行。`;
  }
  async function saveForm(event,draft=false){
    event.preventDefault();if(pending)return;
    const d=formDefinition(),request={action:editing?"edit":"create",operation_id:randomUUID(),human_source:{}};
    if(editing){request.task_id=editing.task_id;request.expected_version=editing.version;request.changes={};
      for(const key of ["title","instruction","scope","schedule","misfire"])if(JSON.stringify(d[key])!==JSON.stringify(initialForm[key]))request.changes[key]=d[key];
      if(editing.state==="needs_info"&&!draft)request.state="active";
      if(!Object.keys(request.changes).length){if(request.state)request.changes.title=d.title;else{resetForm();return;}}
    }else{if(!d.scope)delete d.scope;request.definition=d;if(draft)request.state="needs_info";}
    if(await submit(request))resetForm();
  }
  function mount(){
    syncCreateButton();
    syncDetailNavigation();runtime.dismissOnBackdrop($("calendar-detail"),()=>closeDetail());
    settingsUI.bindSystemTime($("calendar-panel"),()=>{
      if(value&&settingsUI.systemTime.zone)value.time_zone=settingsUI.systemTime.zone;
      updateCalendarIcon();
      if(available()){initialScroll=true;runtime.wake("calendar");void runtime.observe("calendar");}
    });
    document.addEventListener("visibilitychange",()=>{if(!document.hidden)updateCalendarIcon();});
    $("calendar-create").addEventListener("toggle",()=>{
      const open=$("calendar-create").open;
      $("calendar-new").disabled=open;
      if(open){workspaceScroll=$("calendar-panel").scrollTop;workspaceGridScroll=$("calendar-items").scrollTop;}
      $("calendar-panel").classList.toggle("calendar-form-open",open);
      if(open)$("calendar-panel").scrollTop=0;
      else {render();$("calendar-items").scrollTop=workspaceGridScroll;$("calendar-panel").scrollTop=workspaceScroll;}
      scheduleClock();
    });
    $("calendar-form").addEventListener("submit",event=>void saveForm(event));
    $("calendar-frequency").addEventListener("change",syncForm);
    for(const id of ["calendar-day-filter","calendar-once-filter","calendar-time-zone","calendar-misfire"])$(id).addEventListener("change",syncForm);
    $("calendar-time-zone").addEventListener("input",syncForm);
    $("calendar-save-draft").addEventListener("click",event=>{if($("calendar-title").reportValidity()&&$("calendar-instruction").reportValidity())void saveForm(event,true);});syncForm();
    $("conversation-chat").addEventListener("click",()=>{setVisible(false);$("conversation-calendar").focus({preventScroll:true});});
    $("conversation-calendar").addEventListener("click",()=>{setVisible(!visible);if(visible)$("conversation-chat").focus({preventScroll:true});});
    $("calendar-options-toggle").addEventListener("click",()=>setOptionsOpen($("calendar-options-menu").hidden));
    $("calendar-refresh").addEventListener("click",()=>{setOptionsOpen(false,true);void refresh().catch(()=>{});});
    $("calendar-retry").addEventListener("click",()=>void refresh().catch(()=>{}));
    $("calendar-prev").addEventListener("click",()=>navigate(shiftDay(date,mode==="day"?-1:-7)));
    $("calendar-next").addEventListener("click",()=>navigate(shiftDay(date,mode==="day"?1:7)));
    $("calendar-today").addEventListener("click",()=>navigate(today()));
    $("calendar-date").addEventListener("change",()=>{if($("calendar-date").value)navigate($("calendar-date").value);});
    $("calendar-date-picker").addEventListener("toggle",()=>{if($("calendar-date-picker").open){pickerMonth=date;renderPicker();}});
    $("calendar-month-prev").addEventListener("click",()=>renderPicker(-1));$("calendar-month-next").addEventListener("click",()=>renderPicker(1));
    $("calendar-picker-today").addEventListener("click",()=>navigate(today()));
    document.addEventListener("click",event=>{if(!event.target.closest?.("#calendar-date-picker"))$("calendar-date-picker").open=false;if(!event.target.closest?.("#calendar-options"))setOptionsOpen(false);});
    $("calendar-date-picker").addEventListener("keydown",event=>{if(event.key==="Escape"){$("calendar-date-picker").open=false;$("calendar-range").focus();}});
    $("calendar-options").addEventListener("keydown",event=>{if(event.key==="Escape")setOptionsOpen(false,true);});
    $("calendar-mode").addEventListener("click",event=>{const next=event.target.closest("[data-mode]")?.dataset.mode;if(next)navigate(date,next);});
    $("calendar-view").addEventListener("click",event=>{const next=event.target.closest("[data-view]")?.dataset.view;if(next)switchView(next);});
    $("calendar-show-skipped").addEventListener("change",render);$("calendar-task-filter").addEventListener("change",render);
    $("calendar-detail-close").addEventListener("click",()=>closeDetail());
    $("calendar-new").addEventListener("click",()=>{if(!editing&&!$("calendar-start-date").value)$("calendar-start-date").value=view==="calendar"?date||today():today();syncForm();$("calendar-create").open=true;$("calendar-create").scrollIntoView({block:"start"});$("calendar-title").focus();});
    $("calendar-form-cancel").addEventListener("click",resetForm);
    $("calendar-detail").addEventListener("cancel",event=>{event.preventDefault();closeDetail();});
    window.addEventListener("resize",()=>{syncCreateButton();syncDetailNavigation();const next=window.innerWidth<=640;if(next!==compact){compact=next;if(visible)render();}});
  }
  return {mount,refresh,sync,reset,renderLinks,openTask:taskID=>{setVisible(true);return openDetail(taskID);},visible:()=>visible};
}
