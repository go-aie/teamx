import { randomUUID } from "../random-id.mjs";
import { compactViewport } from "../client.mjs";
import { captureReturnView } from "../view-return.mjs";
import { calendarRange, calendarOffsetLabels, localDay, shiftDay, shiftMonth, weekDates, monthDates, minuteOfDay, pointGroups } from "./calendar-dates.mjs";

// Reads use the Actor's committed projection. Opening or previewing never starts
// an Agent; mutations preserve the original operation and resource version.
export function createCalendar({ env, runtime, roomsUI, conversationUI, settingsUI, dateTimeUI }) {
  const { document, window, setTimeout, clearTimeout } = env, { $, state, rpc, currentView, notice } = runtime;
  let visible=false, value=null, pending=null, retryRequest=null, readID=0, detailID=0;
  let compact=window.innerWidth<=640, date="", mode=compact?"day":"week", view="calendar";
  let preferenceLayout="", storage;
  try { storage=env.localStorage; } catch { /* Preferences also work in memory. */ }
  const preferences=new Map();
  const multiDays=()=>window.innerWidth>=480?4:3;
  const rangeAt=(displayZone=zone(),padding=0)=>calendarRange(date,mode,displayZone,{days:multiDays(),padding});
  let multiStart="", multiWidth=0, multiTimer=null, multiScrolling=false, multiRenderPending=false, multiTouch=false;
  let swipe=null, suppressClickUntil=0;
  let calendarScroll=0,calendarGridScroll=0,listScroll=0;
  let selected=null, detail=null, history=[], historyCursor="", editing=null, initialForm=null, formScope="", initialScroll=true;
  let lastOpener=null, detailReturn=null, renderedRange="", formFrequency=null;
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
  const hasDifferentZone=timeZone=>!!timeZone&&timeZone!==zone();
  const today=()=>localDay(Date.now(),zone());
  const available=()=>!!state.info&&roomsUI.isDirect();
  const fmt=(at,options={year:"numeric",month:"numeric",day:"numeric",hour:"2-digit",minute:"2-digit"},tz=zone())=>at?formatAt(at,options,tz):"待确定";
  const localHour=at=>Number(new Intl.DateTimeFormat("en-GB",{timeZone:zone(),hour:"2-digit",hourCycle:"h23"}).format(new Date(at)));
  const dayLabel=d=>`${Number(d.slice(5,7))}月${Number(d.slice(8))}日`;
  const reason=r=>reasons[r]||r;
  function restorePreference(){
    const layout=compact?"mobile":"desktop";
    if(layout===preferenceLayout)return;
    preferenceLayout=layout;
    let saved=preferences.get(layout);
    try { saved??=JSON.parse(storage?.getItem(`teamx.calendar-view.${layout}`)||"null"); } catch { /* Ignore unavailable or malformed storage. */ }
    const modes=compact?["day","multi","month"]:["day","week","month"];
    mode=modes.includes(saved?.mode)?saved.mode:compact?"day":"week";
    view=saved?.view==="list"?"list":"calendar";
  }
  function savePreference(){
    const saved={mode,view};preferences.set(preferenceLayout,saved);
    try { storage?.setItem(`teamx.calendar-view.${preferenceLayout}`,JSON.stringify(saved)); } catch { /* Storage is optional. */ }
  }
  function syncRange(){
    const dates=rangeAt().dates;
    const end=dates.at(-1),shortRange=dates[0].slice(0,7)===end.slice(0,7)?`${Number(date.slice(5,7))}月${Number(date.slice(8))}–${Number(end.slice(8))}日`:`${dayLabel(date)}–${dayLabel(end)}`;
    $("calendar-range-label").textContent=mode==="month"?`${date.slice(0,4)}年 ${Number(date.slice(5,7))}月`:mode==="day"?`${compact?"":`${date.slice(0,4)}年 `}${dayLabel(date)}`:compact?shortRange:`${dates[0].slice(0,4)}年 ${dayLabel(dates[0])} — ${dayLabel(end)}`;
    $("calendar-range").title=`${date.slice(0,4)}年 ${dayLabel(date)}`;
    $("calendar-date").value=date;
    $("calendar-today").dataset.away=String(!dates.includes(today()));
  }
  function ruleLabel(rule,includeZone=true) {
    const repeat=rule.frequency==="daily"?"每天":rule.frequency==="weekly"?`每周 ${(rule.weekdays||[]).map(d=>["","一","二","三","四","五","六","日"][d]).join("、")}`:"一次性";
    const filter={all_days:"含周末和节假日",weekdays:"周一至周五，含工作周内的法定假日",business_days:"中国大陆法定工作日，含调休补班"}[rule.day_filter||"all_days"];
    return `${repeat} ${rule.local_time||"时刻待补齐"}${rule.frequency!=="once"||rule.day_filter!=="all_days"?` · ${filter}`:""}${includeZone&&hasDifferentZone(rule.time_zone)?` · ${rule.time_zone}`:""}${rule.end_date?` · 至 ${rule.end_date}（含当天）`:""}`;
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
    if(!open)$("calendar-options-menu").querySelector("details").open=false;
    if(focus)$("calendar-options-toggle").focus({preventScroll:true});
  }
  function syncView(){
    const calendar=view==="calendar";
    $("calendar-panel").dataset.view=view;
    $("calendar-navigation").hidden=!calendar;$("calendar-mode").hidden=!calendar;
    $("calendar-items").hidden=!calendar;$("calendar-tasks").hidden=calendar;$("calendar-list-controls").hidden=calendar;
    $("calendar-show-skipped").closest("label").hidden=!calendar;
    $("calendar-view").dataset.view=view;
    const modeLabel={day:"日",week:"周",multi:"多日",month:"月"}[mode];
    $("calendar-mode-label").textContent=modeLabel;$("calendar-mode-open").dataset.mode=mode;
    $("calendar-mode-open").setAttribute("aria-label",`选择日历视图，当前${modeLabel}`);
    for(const b of $("calendar-view").querySelectorAll("[data-view]"))b.setAttribute("aria-pressed",String(b.dataset.view===view));
    for(const b of $("calendar-mode-sheet").querySelectorAll("[data-calendar-mode]"))b.setAttribute("aria-pressed",String(b.dataset.calendarMode===mode));
  }
  function closeModeSheet(focus=true){
    $("calendar-mode-sheet").close();$("calendar-mode-sheet").style.removeProperty("transform");
    $("calendar-mode-open").setAttribute("aria-expanded","false");
    if(focus)(compact?$("calendar-mode-open"):$("calendar-view").querySelector('[data-view="calendar"]')).focus({preventScroll:true});
    runtime.navigationChanged();
  }
  function openModeSheet(){
    if(!compact)return;
    setOptionsOpen(false);dateTimeUI.close();syncView();
    $("calendar-mode-sheet").showModal();$("calendar-mode-open").setAttribute("aria-expanded","true");
    $("calendar-mode-sheet").querySelector(`[data-calendar-mode="${mode}"]`)?.focus({preventScroll:true});
    runtime.navigationChanged();
  }
  function switchView(next){
    if(next===view)return;
    if(view==="calendar"){calendarScroll=$("calendar-panel").scrollTop;calendarGridScroll=$("calendar-items").scrollTop;}else listScroll=$("calendar-panel").scrollTop;
    view=next;savePreference();dateTimeUI.close();syncView();
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
    if(!visible){setOptionsOpen(false);if($("calendar-mode-sheet").open)closeModeSheet(false);}
    $("conversation-calendar").setAttribute("aria-expanded",String(visible));
    if(visible){restorePreference();conversationUI.saveDraft();if(!date)date=today();syncView();if(value)render();runtime.wake("calendar");void runtime.observe("calendar");}
    else {if(was){closeDetail(false);if(chatPosition)conversationUI.restorePosition(chatPosition);chatPosition=null;}conversationUI.scheduleMessageViewing();}
    scheduleClock();
  }
  function reset(){
    clearTimeout(multiTimer);multiScrolling=false;multiRenderPending=false;multiTouch=false;swipe=null;multiStart="";
    chatPosition=null;
    workspaceScroll=0;workspaceGridScroll=0;calendarScroll=0;calendarGridScroll=0;listScroll=0;renderedRange="";messageLinks.clear();readID++;detailID++;pending=null;retryRequest=null;value=null;date="";selected=null;detail=null;editing=null;history=[];initialScroll=true;
    dateTimeUI.close();closeDetail(false);resetForm();sync();setVisible(false);$("calendar-items").replaceChildren();$("calendar-task-items").replaceChildren();$("calendar-task-count").textContent="0";showError("");
  }
  async function refresh({current=()=>true,displayZone=zone()}={}) {
    sync();if(!available())return;
    const version=runtime.viewVersion,id=++readID;
    if(!date)date=today();
    const range=rangeAt(displayZone,mode==="multi"?7:0),query=view==="calendar"?{from:range.from,to:range.to}:{};
    try {
      let data,occurrences=[],cursor="",attempt=0;
      // Retry a complete read after a changed snapshot; never combine pages from
      // different revisions or show a silently truncated calendar range.
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
      if(mode==="multi"&&(date<range.dates[0]||shiftDay(date,multiDays()-1)>range.dates.at(-1)))return refresh({current,displayZone});
      const changed=value?.revision!==data.revision,rangeKey=JSON.stringify([view,view==="calendar"?date:"",view==="calendar"?mode:"",data.time_zone]);
      value={...data,occurrences,range};renderLinks();if(visible&&(changed||renderedRange!==rangeKey)){render();renderedRange=rangeKey;}showError("");
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
  function coverageLabel(){
    const calendars=value?.work_calendars||[],first=calendars.at(0)?.from,last=calendars.at(-1)?.to;
    return last?`中国大陆工作日历覆盖${first?`范围：${first} 至 `:"至 "}${last}。`:"中国大陆工作日历尚未覆盖；工作日任务暂不执行。";
  }
  function taskRuleLabel(rule){
    const differentZone=hasDifferentZone(rule.time_zone);
    let text=ruleLabel(rule,false);
    if(rule.frequency==="once"){
      text=`${rule.start_date||"日期待补齐"} ${rule.local_time||"时刻待补齐"} · 一次性`;
      const filter={weekdays:"仅周一至周五",business_days:"仅中国大陆法定工作日"}[rule.day_filter];
      if(filter)text+=` · ${filter}`;
    }
    return `${text}${differentZone?` · ${rule.time_zone}`:""}`;
  }
  function occurrenceButton(o){
    const task=value.tasks.find(t=>t.task_id===o.task_id),d=o.run?.task.definition||o.missed_range?.task.definition||task.definition;
    const b=button("",()=>void openDetail(o.task_id,o.run?.run_id||"",o),"calendar-occurrence");b.dataset.occurrenceId=o.occurrence_id;b.dataset.taskId=o.task_id;b.dataset.status=o.status;b.disabled=!!pending;
    const time=node("time",fmt(o.due_at,{hour:"2-digit",minute:"2-digit"}));time.dateTime=o.due_at;
    const title=node("strong",d.title),attention=["failed","blocked","unknown"].includes(o.status);
    if(attention)title.prepend(node("span",`${status(o)} · `,"calendar-occurrence-alert"));
    b.append(time,title);
    if(!attention)b.append(node("span",`${d.schedule.frequency!=="once"?"↻ · ":""}${status(o)}`,"calendar-status"));
    b.title=`${d.title} · ${status(o)}${o.reason?` · ${reason(o.reason)}`:""}`;
    return b;
  }
  function dayKind(d){return value?.days?.find(day=>day.date===d)?.kind||"";}
  function dayMarker(d){return {holiday:"休",makeup:"补",unknown:"未覆盖"}[dayKind(d)]||"";}
  function dayDescription(d){return {holiday:"节假日",makeup:"补班",weekend:"周末",unknown:"工作日历未覆盖"}[dayKind(d)]||"";}
  function entries(){return (value?.occurrences||[]).filter(o=>$("calendar-show-skipped").checked||!["skipped","cancelled"].includes(o.status));}
  function render(){
    if(!value)return;
    if(multiScrolling||multiTouch){multiRenderPending=true;return;}
    if($("calendar-create").open){syncForm();$("calendar-submit").disabled=!!pending;$("calendar-save-draft").disabled=!!pending;return;}
    $("calendar-work-coverage").textContent=coverageLabel();
    const uncovered=value.tasks.filter(task=>["active","paused","needs_info"].includes(task.state)&&(task.blocked_reason==="work_calendar_uncovered"||(view==="calendar"&&value.occurrences.some(o=>o.task_id===task.task_id&&o.reason==="work_calendar_uncovered"))));
    $("calendar-coverage").classList.toggle("calendar-coverage-warning",uncovered.length>0);
    notice("calendar-coverage",uncovered.length?`工作日历未覆盖部分执行日期，受影响的安排暂不执行：${uncovered.slice(0,3).map(task=>task.definition.title).join("、")}${uncovered.length>3?`等 ${uncovered.length} 项任务`:""}。`:"");
    syncView();const range=mode==="multi"?value.range:rangeAt();syncRange();
    $("calendar-mode").dataset.mode=mode;
    for(const b of $("calendar-mode").querySelectorAll("button"))b.setAttribute("aria-pressed",String(b.dataset.mode===mode));
    $("calendar-submit").disabled=!!pending;$("calendar-save-draft").disabled=!!pending;
    const root=$("calendar-items"),scroll=root.scrollTop;
    root.dataset.mode=mode;
    const occurrences=entries();
    if(view==="calendar"){
      clearTimeout(multiTimer);root.replaceChildren();root.scrollLeft=0;
      if(mode==="month")renderMonth(root,range.dates,occurrences);else renderGrid(root,range.dates,occurrences);
      root.scrollTop=scroll;
      if(mode==="multi"){
        multiStart=range.dates[0];multiWidth=(root.clientWidth-48)/multiDays();
        root.firstElementChild.style.setProperty("--day-width",`${multiWidth}px`);
        multiWidth=root.querySelector(".calendar-day-column").getBoundingClientRect().width;
        const index=range.dates.indexOf(date);root.scrollLeft=Math.max(0,index)*multiWidth;
      }
    }
    const shelf=$("calendar-task-items");shelf.replaceChildren();
    const filter=$("calendar-task-filter").value||"open";
    const tasks=value.tasks.filter(t=>filter==="all"||filter==="open"&&["active","paused","needs_info"].includes(t.state)||t.state===filter);
    for(const task of tasks){
      const row=node("div",undefined,"calendar-task-row");row.dataset.taskId=task.task_id;
      const link=taskButton(task,""),arrow=node("span","›","calendar-task-chevron");
      link.setAttribute("aria-label",task.definition.title);link.setAttribute("aria-haspopup","dialog");link.setAttribute("aria-controls","calendar-detail");arrow.setAttribute("aria-hidden","true");
      const stateLabel=node("span",task.blocked_reason?reason(task.blocked_reason):labels[task.state],"calendar-status");stateLabel.id=`calendar-task-status-${task.task_id}`;
      link.append(node("span",task.definition.title,"calendar-task-title"),stateLabel,arrow);
      const rule=task.definition.schedule;
      const scheduleLabel=node("small",taskRuleLabel(rule));scheduleLabel.id=`calendar-task-rule-${task.task_id}`;link.append(scheduleLabel);
      link.setAttribute("aria-describedby",`${stateLabel.id} ${scheduleLabel.id}`);
      if(task.state==="active"&&task.next_at&&rule.frequency!=="once")link.append(node("small",`下次 ${fmt(task.next_at)}${rule.time_zone&&rule.time_zone!==zone()?` · ${zone()}`:""}`));row.append(link);shelf.append(row);
    }
    if(!tasks.length)shelf.append(node("p","暂无此类任务。","calendar-empty"));
    $("calendar-task-count").textContent=String(tasks.length);
    if(initialScroll&&view==="calendar"){
      if(mode==="month")root.scrollTop=0;
      else {
        const first=occurrences.find(o=>localDay(o.due_at,zone())===today())||occurrences[0];
        const hour=range.dates.includes(today())?localHour(Date.now()):first?localHour(first.due_at):9;
        root.scrollTop=Math.max(0,(hour-1)*hourHeight);
      }
      initialScroll=false;
    }
  }
  function renderMonth(root,dates,occurrences){
    const grid=node("div",undefined,"calendar-month-grid"),headers=node("div",undefined,"calendar-month-headers"),body=node("div",undefined,"calendar-month-body");
    grid.dataset.month=date.slice(0,7);
    grid.setAttribute("aria-label",`${date.slice(0,4)}年 ${Number(date.slice(5,7))}月，${zone()}`);
    for(const label of ["一","二","三","四","五","六","日"])headers.append(node("span",`周${label}`));
    const grouped=new Map();
    for(const o of occurrences){const d=localDay(o.due_at,zone());if(!grouped.has(d))grouped.set(d,[]);grouped.get(d).push(o);}
    for(const d of dates){
      const cell=node("section",undefined,"calendar-month-day");cell.dataset.day=d;cell.dataset.currentMonth=String(d.slice(0,7)===date.slice(0,7));cell.dataset.today=String(d===today());cell.dataset.kind=dayKind(d);
      const heading=button("",()=>navigate(d,"day"),"calendar-month-date");
      heading.setAttribute("aria-label",`${d}${d===today()?"，今天":""}${dayDescription(d)?`，${dayDescription(d)}`:""}，查看当天安排`);heading.title=dayDescription(d)||"查看当天安排";
      if(d===today())heading.setAttribute("aria-current","date");
      heading.append(node("strong",String(Number(d.slice(8)))));
      if(dayMarker(d))heading.append(node("small",dayMarker(d)));
      cell.append(heading);
      const points=(grouped.get(d)||[]).sort((a,b)=>Date.parse(a.due_at)-Date.parse(b.due_at)||a.occurrence_id.localeCompare(b.occurrence_id));
      for(const o of points.slice(0,3))cell.append(occurrenceButton(o));
      if(points.length){
        const more=button("",()=>navigate(d,"day"),"calendar-month-count");more.dataset.overflow=String(points.length>3);
        more.setAttribute("aria-label",`${d}，查看全部 ${points.length} 项安排`);
        if(points.length>3)more.append(node("span",`还有 ${points.length-3} 项`,"calendar-month-count-wide"));
        more.append(node("span",`${points.length} 项`,"calendar-month-count-compact"));cell.append(more);
      }
      body.append(cell);
    }
    grid.append(headers,body);root.append(grid);
  }
  function renderGrid(root,dates,occurrences){
    const grid=node("div",undefined,"calendar-time-grid"),headers=node("div",undefined,"calendar-grid-headers");
    grid.style.setProperty("--day-count",String(dates.length));grid.dataset.mode=mode;
    if(mode==="multi")grid.style.setProperty("--day-width",`${(root.clientWidth-48)/multiDays()}px`);
    const corner=node("div",undefined,"calendar-grid-corner");corner.title=zone();corner.setAttribute("aria-label",`显示时区 ${zone()}`);
    for(const offset of calendarOffsetLabels(date,mode,zone(),{days:multiDays()}))corner.append(node("span",offset));
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
    const ticks=body.querySelectorAll(".calendar-hour > span");
    for(const tick of ticks)tick.removeAttribute("data-now-overlap");
    const dates=[...$("calendar-items").querySelectorAll(".calendar-day-column")].map(column=>column.dataset.day),index=dates.indexOf(today());if(index<0)return;
    const line=node("div",undefined,"calendar-now-line"),label=node("span",fmt(Date.now(),{hour:"2-digit",minute:"2-digit"}));
    line.style.setProperty("--now-top",`${minuteOfDay(Date.now(),zone())*hourHeight/60}px`);
    const dot=node("i");dot.style.setProperty("--today-column",String(index));line.append(label,dot);line.setAttribute("aria-hidden","true");body.append(line);
    const bounds=label.getBoundingClientRect();
    for(const tick of ticks){
      const rect=tick.getBoundingClientRect();
      if(rect.bottom>bounds.top-1&&rect.top<bounds.bottom+1)tick.dataset.nowOverlap="true";
    }
  }
  function navigate(next,nextMode=mode,{preserveTime=false}={}){
    clearTimeout(multiTimer);multiScrolling=false;multiRenderPending=false;
    dateTimeUI.close();date=next;mode=nextMode;savePreference();initialScroll=!preserveTime;void refresh().catch(()=>{});
  }
  function settleMulti(){
    clearTimeout(multiTimer);if(multiTouch)return;multiScrolling=false;
    if(!visible||view!=="calendar"||mode!=="multi"||!multiStart||!multiWidth)return;
    const root=$("calendar-items"),count=root.querySelectorAll(".calendar-day-column").length;
    const index=Math.max(0,Math.min(count-multiDays(),Math.round(root.scrollLeft/multiWidth)));
    if(Math.abs(root.scrollLeft-index*multiWidth)>1){
      multiScrolling=true;root.scrollTo({left:index*multiWidth,behavior:window.matchMedia("(prefers-reduced-motion: reduce)").matches?"auto":"smooth"});return;
    }
    date=shiftDay(multiStart,index);syncRange();
    root.querySelector(".calendar-grid-corner")?.replaceChildren(...calendarOffsetLabels(date,mode,zone(),{days:multiDays()}).map(offset=>node("span",offset)));
    // Refill either edge without moving the visible dates or time position.
    if(index<3||index+multiDays()>count-3){multiRenderPending=false;void refresh().catch(()=>{});}
    else if(multiRenderPending){multiRenderPending=false;render();}
  }
  function mountGestures(){
    const root=$("calendar-items");
    root.tabIndex=0;root.setAttribute("aria-label","日历安排，左右方向键浏览日期");
    root.addEventListener("keydown",event=>{
      if(event.target!==root||!["ArrowLeft","ArrowRight"].includes(event.key))return;
      event.preventDefault();const direction=event.key==="ArrowLeft"?-1:1;
      navigate(mode==="month"?shiftMonth(date,direction):shiftDay(date,direction*(mode==="week"?7:1)),mode,{preserveTime:true});
    });
    root.addEventListener("scroll",()=>{
      if(!compact||mode!=="multi"||!visible)return;
      multiScrolling=true;clearTimeout(multiTimer);multiTimer=setTimeout(settleMulti,180);
    },{passive:true});
    root.addEventListener("scrollend",settleMulti);
    root.addEventListener("touchstart",event=>{
      cancelSwipe();multiTouch=compact&&mode==="multi"&&event.touches.length===1;
      if(!compact||view!=="calendar"||mode==="multi"||event.touches.length!==1||event.target.closest("input,select,textarea,a,details[open]"))return;
      const point=event.touches[0];swipe={x:point.clientX,y:point.clientY,dx:0,axis:null};
    },{passive:true});
    root.addEventListener("touchmove",event=>{
      if(!swipe)return;
      if(event.touches.length!==1){cancelSwipe();return;}
      const point=event.touches[0],dx=point.clientX-swipe.x,dy=point.clientY-swipe.y;
      if(!swipe.axis&&Math.max(Math.abs(dx),Math.abs(dy))>10)swipe.axis=Math.abs(dx)>Math.abs(dy)*1.35?"x":"y";
      if(swipe.axis!=="x")return;
      event.preventDefault();swipe.dx=dx;
      root.firstElementChild?.style.setProperty("transform",`translateX(${Math.max(-80,Math.min(80,dx*.3))}px)`);
    },{passive:false});
    function cancelSwipe(){root.firstElementChild?.style.removeProperty("transform");swipe=null;}
    root.addEventListener("touchcancel",()=>{multiTouch=false;cancelSwipe();if(mode==="multi")multiTimer=setTimeout(settleMulti,180);},{passive:true});
    root.addEventListener("touchend",()=>{
      if(multiTouch){multiTouch=false;multiTimer=setTimeout(settleMulti,180);}
      const gesture=swipe;cancelSwipe();if(gesture?.axis!=="x")return;
      suppressClickUntil=Date.now()+400;
      if(Math.abs(gesture.dx)<48)return;
      const direction=gesture.dx<0?1:-1;
      navigate(mode==="month"?shiftMonth(date,direction):shiftDay(date,direction),mode,{preserveTime:true});
    },{passive:true});
    root.addEventListener("click",event=>{if(Date.now()<suppressClickUntil){event.preventDefault();event.stopImmediatePropagation();}},true);
  }
  function closeDetail(focus=true){
    const taskID=selected,occurrenceID=lastOpener?.dataset?.occurrenceId,back=detailReturn;detailReturn=null;
    detailID++;runReadID++;$("calendar-detail").close();$("calendar-detail").hidden=true;selected=null;detail=null;detailOccurrence=null;selectedRun=null;history=[];
    if(focus){
      const candidate=(occurrenceID&&$("calendar-items").querySelector(`[data-occurrence-id="${occurrenceID}"]`))||$("calendar-task-items").querySelector(`button[data-task-id="${taskID}"]`);
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
    if(hasDifferentZone(d.schedule.time_zone))root.append(node("p",`执行时区 ${d.schedule.time_zone} · 显示时区 ${zone()}`,"calendar-hint calendar-zone-difference"));
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
    const facts=node("dl",undefined,"calendar-facts");fact(facts,"执行规则",ruleLabel(d.schedule,false));
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
    initialForm=formDefinition();syncForm();$("calendar-advanced").open=false;showForm();
  }
  function showForm(){setOptionsOpen(false);$("calendar-create").hidden=false;$("calendar-create").open=true;$("calendar-create").scrollIntoView({block:"start"});$("calendar-title").focus();}
  function resetForm(){editing=null;initialForm=null;formScope="";formFrequency=null;$("calendar-form").reset();$("calendar-form-heading").textContent="新建任务";$("calendar-submit").textContent="保存任务";$("calendar-form-cancel").textContent="取消";$("calendar-create").open=false;$("calendar-create").hidden=true;$("calendar-advanced").open=false;syncForm();}
  function syncForm(){
    const frequency=$("calendar-frequency").value,once=frequency==="once";
    if(formFrequency&&formFrequency!==frequency){if(once)$("calendar-once-filter").value=$("calendar-day-filter").value;else if(formFrequency==="once")$("calendar-day-filter").value=$("calendar-once-filter").value;}
    formFrequency=frequency;
    $("calendar-weekdays").hidden=frequency!=="weekly";$("calendar-filter-field").hidden=once;$("calendar-end-field").hidden=once;$("calendar-once-filter-field").hidden=!once;
    $("calendar-start-label").textContent=once?"执行日期":"开始日期";$("calendar-start-hint").hidden=once;
    $("calendar-start-date").required=once;$("calendar-local-time").required=true;
    for(const id of ["calendar-start-date","calendar-end-date","calendar-local-time"])dateTimeUI.validate($(id));
    $("calendar-save-draft").hidden=!!editing&&editing.state!=="needs_info";
    const filter=once?$("calendar-once-filter").value:$("calendar-day-filter").value;
    const filterHint=filter==="weekdays"?"工作周内的法定假日仍执行。":filter==="business_days"?`包含调休补班。${coverageLabel()}覆盖不足时暂不执行。`:"";
    $("calendar-filter-hint").textContent=once?"":filterHint;$("calendar-once-filter-hint").textContent=once?filterHint:"";
    const taskZone=$("calendar-time-zone").value||zone();
    $("calendar-form-summary").textContent=`${hasDifferentZone(taskZone)?`按 ${taskZone} 执行；`:""}${$("calendar-misfire").value==="skip"?"错过即跳过":"错过后一小时内补最近一次"}。服务需保持运行。`;
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
    restorePreference();mountGestures();
    const sheet=$("calendar-mode-sheet"),sheetHeader=sheet.querySelector(".calendar-sheet-header");let sheetTouch=null;
    runtime.dismissOnBackdrop(sheet,()=>closeModeSheet());
    $("calendar-mode-open").addEventListener("click",openModeSheet);
    $("calendar-mode-close").addEventListener("click",()=>closeModeSheet());
    sheet.addEventListener("cancel",event=>{event.preventDefault();closeModeSheet();});
    sheet.addEventListener("click",event=>{
      const next=event.target.closest("[data-calendar-mode]")?.dataset.calendarMode;if(!next)return;
      closeModeSheet();const preserveTime=next===mode;mode=next;
      if(view==="list"){initialScroll=!preserveTime;switchView("calendar");}
      else navigate(date||today(),next,{preserveTime});
      syncView();
    });
    sheetHeader.addEventListener("touchstart",event=>{const point=event.touches[0];sheetTouch=event.touches.length===1?{x:point.clientX,y:point.clientY,dy:0}:null;},{passive:true});
    sheetHeader.addEventListener("touchmove",event=>{
      if(!sheetTouch)return;if(event.touches.length!==1){sheetTouch=null;sheet.style.removeProperty("transform");return;}
      const point=event.touches[0],dy=point.clientY-sheetTouch.y;
      if(dy>10&&dy>Math.abs(point.clientX-sheetTouch.x)*1.35){event.preventDefault();sheetTouch.dy=dy;sheet.style.transform=`translateY(${Math.min(dy,160)}px)`;}
    },{passive:false});
    sheetHeader.addEventListener("touchend",()=>{const dy=sheetTouch?.dy||0;sheetTouch=null;sheet.style.removeProperty("transform");if(dy>60)closeModeSheet();},{passive:true});
    sheetHeader.addEventListener("touchcancel",()=>{sheetTouch=null;sheet.style.removeProperty("transform");},{passive:true});
    syncCreateButton();
    syncDetailNavigation();runtime.dismissOnBackdrop($("calendar-detail"),()=>closeDetail());
    settingsUI.bindSystemTime($("calendar-panel"),()=>{
      if(value&&settingsUI.systemTime.zone)value.time_zone=settingsUI.systemTime.zone;
      updateCalendarIcon();
      if($("calendar-create").open)syncForm();
      if(detail)renderDetail();
      if(available()){initialScroll=true;runtime.wake("calendar");void runtime.observe("calendar");}
    });
    document.addEventListener("visibilitychange",()=>{if(!document.hidden)updateCalendarIcon();});
    $("calendar-create").addEventListener("toggle",()=>{
      const open=$("calendar-create").open;
      $("calendar-create").hidden=!open;
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
    $("calendar-prev").addEventListener("click",()=>navigate(mode==="month"?shiftMonth(date,-1):shiftDay(date,mode==="week"?-7:-1)));
    $("calendar-next").addEventListener("click",()=>navigate(mode==="month"?shiftMonth(date,1):shiftDay(date,mode==="week"?7:1)));
    $("calendar-today").addEventListener("click",()=>navigate(today()));
    dateTimeUI.register($("calendar-date"), {kind:"date", trigger:$("calendar-range"), zone, label:"选择日期", navigation:true, range:day=>mode==="week"?weekDates(day):mode==="multi"?calendarRange(day,mode,zone(),{days:multiDays()}).dates:[]});
    $("calendar-date").addEventListener("change",()=>{if($("calendar-date").value&&!dateTimeUI.validate($("calendar-date")))navigate($("calendar-date").value);});
    for(const [id,kind,label] of [["calendar-start-date","date","选择执行日期"],["calendar-end-date","date","选择结束日期"],["calendar-local-time","time","选择执行时刻"]]) {
      dateTimeUI.register($(id), {kind, trigger:$(id+"-toggle"), zone:()=>$("calendar-time-zone").value||zone(), label, showZone:()=>kind==="time"&&hasDifferentZone($("calendar-time-zone").value)});
    }
    document.addEventListener("click",event=>{if(!event.target.closest?.("#calendar-options"))setOptionsOpen(false);});
    $("calendar-options").addEventListener("keydown",event=>{if(event.key==="Escape")setOptionsOpen(false,true);});
    $("calendar-mode").addEventListener("click",event=>{const next=event.target.closest("[data-mode]")?.dataset.mode;if(next)navigate(date,next);});
    $("calendar-view").addEventListener("click",event=>{const next=event.target.closest("[data-view]")?.dataset.view;if(next)switchView(next);});
    $("calendar-show-skipped").addEventListener("change",render);$("calendar-task-filter").addEventListener("change",render);
    $("calendar-detail-close").addEventListener("click",()=>closeDetail());
    $("calendar-new").addEventListener("click",()=>{if(!editing&&!$("calendar-start-date").value)$("calendar-start-date").value=view==="calendar"?date||today():today();syncForm();showForm();});
    $("calendar-form-cancel").addEventListener("click",resetForm);
    $("calendar-detail").addEventListener("cancel",event=>{event.preventDefault();closeDetail();});
    window.addEventListener("resize",()=>{
      syncCreateButton();syncDetailNavigation();const next=window.innerWidth<=640;
      if(!next&&sheet.open)closeModeSheet(false);
      if(next!==compact){compact=next;restorePreference();renderedRange="";if(visible){syncView();void refresh().catch(()=>{});}}
      else if(visible&&mode==="multi")render();
    });
  }
  return {mount,refresh,sync,reset,renderLinks,openTask:taskID=>{setVisible(true);return openDetail(taskID);},visible:()=>visible};
}
