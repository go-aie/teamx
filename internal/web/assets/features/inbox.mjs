import { notificationOptions } from "../markdown.mjs";

// Member Inbox snapshots, pagination and retained reading position.
export function createInbox({ env, settingsUI, notificationsUI, runtime, membersUI, policyUI }) {
  const { document, renderMessageBody } = env;
  const { state, $, rpc } = runtime;

  let inboxView = null;

  function notificationParticipant(person) {
    if (!person) return "未知发送者";
    let name = person.name || (person.is_receiver ? "我" : person.type === "app" ? "机器人" : person.id?.value ? `飞书用户 · …${person.id.value.slice(-6)}` : "未知发送者");
    if (person.is_receiver && name !== "我") name += "（我）";
    return name;
  }

  function renderInboxTaskEvent(entry) {
    const section = document.createElement("section"); section.className = "activity-section";
    section.dataset.inboxSeq = String(entry.seq);
    const heading = document.createElement("header"); heading.className = "inbox-entry-header";
    const seq = document.createElement("span"); seq.className = "inbox-entry-seq"; seq.textContent = `#${entry.seq}`;
    const kind = document.createElement("span"); kind.className = "inbox-channel"; kind.textContent = "内部事件";
    const event = entry.task_event;
    const time = document.createElement("time"), date = new Date(event?.created_at || entry.received_at);
    if (Number.isFinite(date.getTime())) {
      time.dateTime = date.toISOString();
      settingsUI.bindSystemTime(time, () => { time.textContent = settingsUI.formatDate(date, {month:"numeric",day:"numeric",hour:"2-digit",minute:"2-digit"}); time.title = settingsUI.formatDate(date); });
    }
    heading.append(seq, kind, time); section.append(heading);
    const title = document.createElement("h3");
    title.textContent = {operation_completed:"操作结果已保存", operation_failed:"操作失败", user_selection:"用户选择已保存"}[event?.event_type] || "无法识别的事件";
    section.append(title);
    const body = document.createElement("p"); body.className = "quiet";
    body.textContent = "供 Agent 接续处理；收到或消费此事件不代表原需求已完成。"; section.append(body);
    if (event) {
      const details = document.createElement("details"); details.className = "inbox-source-details";
      const summary = document.createElement("summary"); summary.textContent = "事件详情";
      const fields = document.createElement("dl"); fields.className = "inbox-source-fields";
      for (const [label, value] of [["操作", event.operation_id], ["结果版本", event.result_version], ["原请求", event.source_seq && `#${event.source_seq}`], ["结果引用", event.result_ref], ["事件", event.event_id]]) {
        const key = document.createElement("dt"), text = document.createElement("dd");
        key.textContent = label; text.textContent = value == null ? "" : String(value); fields.append(key, text);
      }
      details.append(summary, fields); section.append(details);
    }
    return section;
  }

  function inboxSourceDetails(entry) {
    const item = entry.item, source = entry.source || {};
    const details = document.createElement("details"); details.className = "inbox-source-details";
    const summary = document.createElement("summary"); summary.textContent = "来源详情";
    const metadata = document.createElement("dl"); metadata.className = "inbox-source-fields";
    const add = (label, value, namespace) => {
      if (!value) return;
      const key = document.createElement("dt"), text = document.createElement("dd");
      key.textContent = label; text.textContent = value;
      if (namespace) text.title = namespace;
      metadata.append(key,text);
      return text;
    };
    const id = (label, value) => add(label, value?.value, value?.namespace);
    const date = value => value && new Date(value).getTime() > 0 ? settingsUI.formatDate(value) : "";
    add("实例",item.instance); id("来源租户",item.tenant); id("接收账号租户",item.receiver?.tenant);
    add("项目",item.project); add("项目 ID",item.project_id && String(item.project_id));
    add("会话",item.conversation?.name); id("会话 ID",item.conversation?.id);
    if (item.sender) add("发送者",notificationParticipant(item.sender));
    if (entry.origin?.kind === "agent") add("代发",`Agent ${entry.origin.agent_id} · ${entry.origin.status}`);
    if (entry.origin?.uncertain_send) add("发送状态","该会话有结果未确定的代发动作；仅作进展上下文");
    if (item.context_only) add("用途","进展上下文，不触发新的自动回复或待办");
    if (entry.automatic_admission) {
      const admission = entry.automatic_admission;
      const reasons = {self_excluded:"本人自聊历史，已排除；需要办理时请在会话中提出",relationship_pending:"会话关系待核验，暂不作为 Agent 输入"};
      add("自动处理",reasons[admission.reason_code] || (!admission.verified ? "来源证据待核验，暂不作为 Agent 输入" : admission.decision === "excluded" ? "已排除自动处理，历史记录保留" : "可自动处理"));
    }
    id("发送者 ID",item.sender?.id); id("接收账号 ID",item.receiver?.id);
    for (const [label,value] of [["消息时间",item.created_at],["接收时间",entry.received_at]]) {
      const element=add(label,date(value));
      if(element)settingsUI.bindSystemTime(element,()=>{element.textContent=date(value);});
    }
    id("消息 ID",item.message_id); id("回复消息",item.reply_to); id("根消息",item.root_id); id("话题 ID",item.thread_id);
    if (item.mentions?.length) add("提及",item.mentions.map(at => `${at.all ? "@所有人" : "@" + notificationParticipant(at)}${at.id?.value ? `（${at.id.value}）` : ""}`).join("、"));
    if (item.attachments?.length) add("附件",item.attachments.map(a => `${a.type} · ${a.reference} · ${a.status === "not_read" ? "未读取" : a.status}`).join("\n"));
    const statuses = {not_checked:"尚未解析",resolved:"已核实",partial:"部分核实",unavailable:"暂不可读",unsupported:"暂不支持",mismatch:"来源不匹配"};
    add("来源状态",source.status && (statuses[source.status] || source.status));
    add("原因",source.reason || source.reason_code);
    add("来源引用",source.source_ref); add("来源版本",source.source_version && String(source.source_version));
    add("对象引用",source.object_ref); add("目标引用",source.target_ref);
    if (item.structured === false) add("结构化信息","历史通知，仅保留已知信息；缺失字段未补全");
    if (item.truncated) add("内容状态","正文已截断，内容可能不完整");
    if (item.metadata_truncated) add("元数据状态","部分名称、提及或附件信息已截断");
    if(entry.notification_source){
      const settings=document.createElement("button");settings.type="button";settings.className="text-button";settings.textContent="查看通知来源设置";
      settings.addEventListener("click",()=>notificationsUI.openNotificationMessage(settings,entry.notification_source));details.append(settings);
    }
    details.append(summary,metadata); return details;
  }

  function inboxCurrent(view) {
    return view && inboxView === view && state.info?.room_id === view.info.room_id && state.info?.room_instance === view.info.room_instance && state.info?.service_epoch === view.info.service_epoch && membersUI.selectedAgent()?.agent_id === view.member.agent_id && membersUI.selectedAgent()?.membership_id === view.member.membership_id && membersUI.agentTab === "inbox" && $("activity-dialog").open;
  }

  function renderInboxStatus(view) {
    if (!inboxCurrent(view)) return;
    const status = view.status;
    if (status) {
      $("inbox-status").textContent = `保留 ${status.retained_entries} 条` + (status.write_error ? ` · 写入受阻：${status.write_error}` : "");
      $("inbox-status").title = `只读预览，不推进 Agent 消费进度。\n占用 ${status.bytes} / ${status.limits.bytes} 字节 · 最新序号 #${status.latest_seq} · 已清理至 #${status.expired_through}`;
    }
    const newer = view.page && status?.latest_seq > view.page.snapshot_seq;
    $("inbox-latest").textContent = newer ? "有更新" : "最新";
    $("inbox-latest").title = newer ? "有新记录，点击跳到最新" : "跳到最新记录";
    $("inbox-latest").setAttribute("aria-label", $("inbox-latest").title);
    $("inbox-notice").textContent = view.error || view.syncError || view.notice || (status?.expired_through ? "旧通知正文已过期清理，跟进和动作记录继续保留。" : "");
    const member = membersUI.selectedAgent(), execution = member?.execution;
    const paused = execution?.paused ? "自动处理已暂停" : execution?.active ? "Agent 正在处理" : `Agent ${membersUI.statusNames[member?.status] || member?.status || ""}`;
    const through = Math.max(status?.consumed_through || 0, status?.expired_through || 0, status?.skipped_through || 0);
    const pending = status && status.latest_seq > through;
    const capacity = status && (status.retained_entries >= status.limits.entries || status.bytes >= status.limits.bytes);
    $("inbox-runtime").textContent = [paused, execution?.error && `自动处理受阻：${execution.error}`, pending && "有输入等待处理或来源核验", capacity && "容量已满，来源积压等待空间释放后自动重试"].filter(Boolean).join(" · ");
  }

  function renderInboxPagination(view) {
    const entries = view.page?.entries || [];
    const label = view.page ? entries.length ? `第 ${view.pageNumber} 页` : "暂无通知" : "—";
    for (const suffix of ["", "-bottom"]) {
      $("inbox-prev" + suffix).disabled = view.loading || !entries.length || view.end >= view.page.snapshot_seq;
      $("inbox-next" + suffix).disabled = view.loading || !entries.length || view.after <= view.status.expired_through;
      $("inbox-page" + suffix).textContent = label;
      $("inbox-page" + suffix).title = entries.length ? `消息 #${entries[0].seq}–#${entries.at(-1).seq}` : "";
    }
    $("inbox-reload").disabled = $("inbox-latest").disabled = view.loading;
    $("inbox-pagination-bottom").hidden = !entries.length;
    $("inbox-entries").setAttribute("aria-busy", String(view.loading));
  }

  async function refreshMemberInbox() {
    const view = inboxView;
    if (!inboxCurrent(view) || view.loading || view.refreshing || document.hidden || !document.hasFocus() || $("notification-detail").open) return;
    view.refreshing = true;
    try {
      const status = await rpc("/v1/member/inbox/status", view.member, view.info);
      if (!inboxCurrent(view) || view.loading) return;
      view.status = status; view.syncError = "";
      // A page touching reclaimed records cannot remain a readable old snapshot.
      if (!view.page) await loadMemberInbox("refresh", status);
      else if (view.after < status.expired_through) await loadMemberInbox("relocate", status);
      else { renderInboxStatus(view); renderInboxPagination(view); }
    } catch (error) {
      if (inboxCurrent(view) && !view.loading) { view.syncError = `同步失败：${error.message}。当前页已保留，可点击刷新重试。`; renderInboxStatus(view); }
    } finally { view.refreshing = false; }
  }

  async function loadMemberInbox(action = "latest", knownStatus) {
    const member = membersUI.selectedAgent();
    if (!member || membersUI.agentTab !== "inbox") return;
    const prior = inboxCurrent(inboxView) ? inboxView : null;
    if (prior?.loading) return;
    if (["prev", "next"].includes(action) && (!prior?.page || $("inbox-" + action).disabled)) return;
    const keepPosition = action === "refresh" || action === "relocate";
    const view = {member:{agent_id:member.agent_id,membership_id:member.membership_id}, info:{...state.info}, page:prior?.page, pageNumber:prior?.pageNumber || 1, status:prior?.status, after:prior?.after, end:prior?.end, loading:true, notice:action === "latest" ? "" : prior?.notice};
    inboxView = view;
    if (!prior) { $("inbox-entries").replaceChildren(); $("inbox-status").textContent = "正在读取…"; $("inbox-notice").textContent = $("inbox-runtime").textContent = ""; }
    renderInboxPagination(view);
    try {
      let status = knownStatus || await rpc("/v1/member/inbox/status", view.member, view.info);
      if (!inboxCurrent(view)) return;
      let snapshot = action === "latest" || !prior?.page ? status.latest_seq : prior.page.snapshot_seq;
      let pageNumber = action === "latest" || !prior?.page ? 1 : prior.pageNumber;
      if (action === "next") pageNumber++;
      if (action === "prev") pageNumber--;
      let end = snapshot;
      if (action === "next") end = prior.after;
      if (action === "prev") end = Math.min(snapshot, prior.end + 25);
      if (keepPosition && prior?.page) end = prior.end;
      let after = Math.max(status.expired_through, end - 25);
      if (keepPosition && prior?.page) after = prior.after;
      let data;
      // Cleanup may race either the inspection or any short byte-limited page.
      // Retry once with a fresh boundary; repeated races remain a visible retry.
      for (let attempt = 0; attempt < 2; attempt++) {
        if (after < status.expired_through || end <= status.expired_through && status.latest_seq > status.expired_through || snapshot < status.expired_through) {
          view.notice = "当前记录已过期，已重新定位到保留记录。";
          snapshot = status.latest_seq;
          // A new browsing session starts at the latest retained page. Page
          // numbers are local navigation state, never a count of stored pages.
          pageNumber = 1;
          end = snapshot;
          after = Math.max(status.expired_through, end - 25);
        }
        if (action === "relocate") view.notice = "当前记录已过期，已重新定位到保留记录。";
        try {
          const entries = [];
          let cursor = after;
          do {
            const page = await rpc("/v1/member/inbox", {...view.member, after:cursor, snapshot_seq:snapshot, limit:Math.max(1, end - cursor)}, view.info);
            if (!inboxCurrent(view)) return;
            entries.push(...page.entries);
            data = {...page, entries};
            if (cursor < end && page.next_after <= cursor) throw new Error("分页未推进，请重试");
            cursor = page.next_after;
          } while (cursor < end);
          // The status in the preview may be newer than the initial inspection.
          status = data.status;
          if (after < status.expired_through) throw Object.assign(new Error("通知已过期"), {code:"inbox_expired"});
          break;
        } catch (error) {
          if (error.code !== "inbox_expired" || attempt) throw error;
          status = await rpc("/v1/member/inbox/status", view.member, view.info);
          if (!inboxCurrent(view)) return;
          view.notice = "当前记录已过期，已重新定位到保留记录。";
        }
      }
      view.page = data; view.pageNumber = pageNumber; view.status = status; view.after = after; view.end = end; view.error = "";
      const scrollTop = $("inbox-member-view").scrollTop;
      const previous = new Map([...(prior?.page?.entries || [])].map((entry, i) => [entry.seq, {entry, node:$("inbox-entries").children[i]}]));
      const nodes = data.entries.map(entry => {
        const old = previous.get(entry.seq);
        if (old?.node && JSON.stringify(old.entry) === JSON.stringify(entry)) return old.node;
        if (entry.type === "task_event") {
          const section = renderInboxTaskEvent(entry);
          if (old?.node?.querySelector("details")?.open) section.querySelector("details").open = true;
          return section;
        }
        if (entry.type && entry.type !== "platform_notification") {
          const section = document.createElement("section"); section.dataset.inboxSeq = String(entry.seq);
          section.textContent = `#${entry.seq} · 无法识别的 Inbox 条目类型`; return section;
        }
        const section = document.createElement("section"); section.className = "activity-section";
        const item = entry.item;
        const source = item;
        const provider = source.provider === "lark" ? "飞书消息" : source.provider === "gitlab" ? `GitLab · ${{events:"项目活动",todos:"我的待办"}[source.channel] || source.channel}` : `${source.provider} / ${source.channel}`;
        const heading = document.createElement("header"); heading.className = "inbox-entry-header";
        const seq = document.createElement("span"); seq.className = "inbox-entry-seq"; seq.textContent = `#${entry.seq}`;
        const channel = document.createElement("span"); channel.className = "inbox-channel"; channel.textContent = provider;
        const time = document.createElement("time");
        const created = new Date(entry.item.created_at), received = new Date(entry.received_at);
        const date = created.getTime() > 0 ? created : received;
        if (Number.isFinite(date.getTime())) {
          time.dateTime = date.toISOString();
          settingsUI.bindSystemTime(time, () => {
            time.textContent = settingsUI.formatDate(date, {month:"numeric",day:"numeric",hour:"2-digit",minute:"2-digit"});
            time.title = `${created.getTime() > 0 ? "消息时间" : "接收时间"}：${settingsUI.formatDate(date)}`;
          });
        } else time.textContent = "时间未获取";
        heading.append(seq,channel,time); section.append(heading);
        const lark = source.provider === "lark", context = item.conversation || item.context;
        const identity = document.createElement("div"); identity.className = "inbox-message-heading";
        if (item.sender) {
          const sender = document.createElement("h3"); sender.className = "inbox-sender"; sender.textContent = notificationParticipant(item.sender) + (entry.origin?.kind === "agent" ? " · Agent 代发" : "") + (item.action ? ` · ${item.action}` : "");
          (lark ? identity : section).append(sender);
        }
        let chat;
        if (lark ? ["group","topic"].includes(item.conversation?.type) : context) {
          chat = document.createElement(lark ? "span" : "p"); chat.className = "inbox-chat";
          const label = context?.name || "会话名称未获取";
          if(lark)chat.setAttribute("aria-label",`群聊：${label}`);
          let url;
          try { url = new URL(context?.url); } catch { /* A source may have no original link. */ }
          if (url && ["https:","http:"].includes(url.protocol) && !url.username && !url.password) {
            const link = document.createElement("a"); link.textContent = label; link.href = url.href;
            link.target = "_blank"; link.rel = "noopener noreferrer"; chat.append(link);
          } else chat.textContent = label;
          if(lark)identity.append(chat);
        }
        if(lark && identity.children.length)section.append(identity);
        if (item.sender && item.changed) {
          const change = document.createElement("p"); change.className = "inbox-change quiet";
          change.textContent = "消息已变更 · 关联原消息，不自动重做已完成动作"; section.append(change);
        }
        const body = document.createElement("div"); body.className = "message-text";
        const options = notificationOptions(view.info, state.operator, source, { seq: entry.seq, agent_id: member.agent_id, membership_id: member.membership_id });
        renderMessageBody(body, item.text, state.operator, lark ? {...options, compactLarkMentions:true} : options);
        section.append(body);
        if(!lark && chat)section.append(chat);
        if (item.truncated || item.metadata_truncated) {
          const notice = document.createElement("p"); notice.className = "inbox-change quiet";
          notice.textContent = item.truncated ? "正文已截断，内容可能不完整" : "部分名称、提及或附件信息已截断"; section.append(notice);
        }
        section.append(inboxSourceDetails(entry));
        section.dataset.inboxSeq = String(entry.seq);
        if (old?.node?.querySelector("details")?.open) section.querySelector("details").open = true;
        return section;
      });
      const container = $("inbox-entries");
      const unchanged = nodes.length === container.children.length && nodes.every((node, i) => node === container.children[i]);
      if (!unchanged) {
        const focused = document.activeElement;
        const focusedRow = [...container.children].find(row => row.contains?.(focused));
        const focusPath = [];
        for (let child = focused; focusedRow && child !== focusedRow; child = child.parentElement) focusPath.unshift([...child.parentElement.children].indexOf(child));
        // Keep retained nodes attached: replaceChildren would blur even reused nodes.
        const retained = new Set(nodes);
        for (const node of [...container.children]) if (!retained.has(node)) node.remove();
        for (let i = 0; i < nodes.length; i++) if (container.children[i] !== nodes[i]) container.insertBefore(nodes[i], container.children[i] || null);
        if (keepPosition && focusedRow && !focused.isConnected) {
          let replacement = nodes.find(row => row.dataset.inboxSeq === focusedRow.dataset.inboxSeq);
          for (const index of focusPath) replacement = replacement?.children[index];
          (replacement || $("inbox-page")).focus({preventScroll:true});
        }
      }
      $("inbox-member-view").scrollTop = keepPosition ? scrollTop : 0;
      if (prior && !keepPosition) $("inbox-page").focus({preventScroll:true});
      if (action === "latest" && !view.notice?.includes("重新定位")) view.notice = "";
    } catch(error) {
      if (inboxCurrent(view)) view.error = `读取失败：${error.message}` + (prior?.page ? "。当前页已保留，可点击刷新或重试翻页。" : "。请点击刷新重试。");
    } finally {
      if (inboxCurrent(view)) { view.loading = false; renderInboxStatus(view); renderInboxPagination(view); notificationsUI.renderNotificationPlatforms(); }
    }
  }

  function reset() { inboxView = null; $("inbox-entries").replaceChildren(); }

  function mount() {
    $("inbox-reload").addEventListener("click", () => loadMemberInbox("refresh"));

    $("inbox-latest").addEventListener("click", () => loadMemberInbox("latest"));

    $("inbox-followups").addEventListener("click", async () => {
      await membersUI.selectAgentTab("policy");
      if (!policyUI.policyCurrent(policyUI.policyView)) return;
      $("policy-followups").open = true;
      if (!policyUI.policyView.followupsLoading) await policyUI.loadPolicyFollowups(policyUI.policyView);
    });

    for (const suffix of ["", "-bottom"]) {
      $("inbox-prev" + suffix).addEventListener("click", () => loadMemberInbox("prev"));
      $("inbox-next" + suffix).addEventListener("click", () => loadMemberInbox("next"));
    }
  }

  return {
    mount,
    reset,
    get inboxView() { return inboxView; },
    inboxCurrent,
    loadMemberInbox,
    refreshMemberInbox
  };
}
