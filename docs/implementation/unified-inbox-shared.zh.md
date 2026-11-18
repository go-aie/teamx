# 共享项目 Inbox 与双成员独立消费

对应[统一 Inbox 存储方案 S5](../UNIFIED-INBOX-STORAGE-PLAN.zh.md#s5--共享-inbox-的双成员完整闭环)。交付每 Room 一个 `project` Inbox、一条启用的 GitLab 项目事件订阅和多成员独立消费。个人 Todo、飞书及私有路径保持原入口；Room 接入共同段内核和相关升级仍由 S6 交付。验证以用户指定的相关主要测试为准。

## 正常入口与所有权

房间详情的「共享项目收件箱」选择并保存接收成员，随后复用 GitLab 连接、项目搜索及项目活动设置。页面提供保留正文预览、成员未读／消费证据不可用、未结清页、保留期、来源释放与容量原因。成员自己的收件箱合并私有及共享历史，标明自己的 pending、confirmed 或 skipped；预览不创建来源关联、不消费。

- `InboxActor` 是该 Inbox 的唯一正文、Grant、页保护和回收 writer。受控任务串行持久变更，队列上限 64；只读业务适配器使用已安装的不可变正文视图。
- `MemberInputActor` 拥有本成员的共享消费文件、执行、准备意图、材料、结果及 Session 证明。共享和私有平台输入共用原每回合 25 条额度，目录曝光和关注排序沿用成员偏好。
- `NotificationActor` 继续拥有来源 State。共享收件只返回一次持久回执，来源确认不随接收成员数增加。冻结来源订阅、目标和原项，正文发布时冻结实际 Grant 集合；相同 ID 重投不增加新成员。
- `GET /v1/rooms/shared-inbox` 读取管理预览；`POST` 使用观测的 `version`、human Caller、service_epoch、完整 RoomIdentity 及完整成员身份更新接收集合，初次为版本 0。来源设置继续经 `/v1/platform/gitlab/notifications/configure`，目标为 `{"mode":"shared_inbox","inbox_id":"project"}`。

共享内核复用 `internal/inputstream` 的段、校验、发布边界和连续前缀回收。共享分区身份显式绑定 Inbox 命名空间，不与同名 Membership 的私有分区混淆。单分区容量从发布根核实，与正文、回执及 Grant 同次生效，不另写共享容量预留账本。布局及协议集中在[存储架构](../storage-architecture.zh.md#共享正文成员交付与释放)。

## 持久合同与恢复

`SharedPageRequest` 固定 Inbox、分区、成员、Grant、物理流、执行、page、起点、上界、排除位置及有效额度；成员协调器先保存 `SharedReadIntent`。`SharedPage` 保存请求摘要、固定快照、扫描／可推进边界、实际位置、剩余量、页摘要及保护凭证。同页同参返回原页，改起点、Grant、流、执行、范围或额度返回冲突；撤权和已回收页返回明确错误。

共享 owner 先保存保护，成员再保存 reserved 页和准备结果。共享 handoff 授权与撤权串行，成员随后保存 awaiting_confirmation 并交付。匹配的持久 Session 结果证明到达后，成员原子保存检查点及同页证明，结清成员页，再解除共享保护。重开能接续意图、补结清确认和重复解除；无匹配证明保持保护。安全的交付前失败先持久终止成员页，再解除未 handoff 的保护；意图也阻挡其他页及 Session 换代。

Grant 加入使用 joining → 成员初始消费文件 → active，避免中断后把缺失的活动消费文件重置为零。撤权先关闭准入，未结清页留在 closing；原 Session 证明仍可结清，所有页结清后记录 closed。重新加入产生新 ID 和新起点。移除成员先完成 Grant 结清，其他成员及来源可继续。

`MemberReleaseCandidate` / `MemberRelease` 逐项绑定精确候选、记录摘要及持久修订，返回 unknown、held、released。未读且没有页保护仍为 held；缺失文件和不匹配证据为 unknown。已确认需核验关联；到期跳过先由成员 owner 保存，即使该成员没有准备过页。未加载 owner 时受控读取校验后的消费和关联文件。闭合 Grant 从共享根核验，不从离线或名单缺项推断释放。

维护由共享 owner 的受控周期 task 和来源变化唤醒，一个运行实例加 dirty 合并请求，每批最多 128 条；无有效接收者也能按保留期回收已结清旧正文。跨 owner 等待在共享变更槽之外，回收提交前重新覆盖本地 Grant 和页保护。旧执行证明按成员先压缩、共享 owner 后核实的顺序压缩；仍被记录或保护引用的 Grant 保留。

容量错误同时映射到来源侧的 inbox_capacity 背压，暂停新增查询并保留原 ID；无有效接收者只阻挡新查询，已发布记录仍可重投原回执并结清来源。共享周期维护也覆盖 disabled／retired 来源的到期排除。

本次还修正来源释放中投递 ID 的核验：真实 provider 在来源 ID 外再按冻结 Route 派生目标 ID。释放比较现在核验完整目标 ID；私有既有 ID 算法不变。遗漏这一层会让真实记录一直返回 unknown，阻挡默认到期跳过和回收。

## 故障、并发与主要回归

| 检查 | 结果 |
| --- | --- |
| 同页重放与冲突 | 同参、追加后重放、确认后应答丢失可复用原页；改起点、Grant、流、执行或额度冲突；已结清重放不新建保护 |
| 一份正文、独立消费 | A/B/A 交错流、甲确认乙未读、共享与私有合用额度、同名私有分区隔离；没有成员正文副本 |
| 加入与撤权 | 加入后旧 ID 重投不补发；handoff 与撤权排序，旧引用拒绝，新 Grant 不继承旧记录，joining 中断可接续 |
| 持久边界 | 追加预留、正文同步、发布、应答以及共享保护中断后重开；准备意图、成员页与原生证明均保留恢复依据；共享授权已落盘但成员 handoff 尚未落盘时可持久中止，迟到 handoff 被拒绝 |
| 真正进程退出 | 子进程在共享正文发布后、来源确认前退出；新进程恢复原两 Grant、一个正文及原回执，再完成来源确认 |
| 正常关闭重开 | 甲已确认、乙未读；重开保持两份进度，甲撤权后再重开乙仍可读取 |
| 来源背压与无接收者 | 正文容量失败不确认来源，保存 inbox_capacity 后原 ID 可重试；无接收者不执行新查询，但仍能重放已发布回执并确认来源 |
| 无页到期与离线成员 | 默认策略为两名未读成员持久跳过再回收；关闭所有 Grant 后周期任务仍推进；关闭过期跳过时乙未读保持正文，甲 owner 卸载后可从权威文件重建已消费释放 |
| 相关 race | inputstream、来源释放、共享／成员 owner、额度与原生 Room 读取通过；上述共享重启、进程退出及过期／离线成员定向集成 race 通过 |
| 前端 | 原页面回归 192/192 通过；Chromium 使用实际 HTTP/Room/共享和成员 owner，验证预览、保存接收集合、撤权后新 Grant、复用 GitLab 编辑器，桌面和 390px 移动视口无横向溢出、零 pageerror |

主要日志：`.local/unified-inbox-shared-engine-final.log`、`-engine-contract-final.log`、`-room-final.log`、`-protocol-final.log`、`-race-final.log`、`-restart-race-final.log`、`-web-final.log`、`-browser-final.log`。已生成桌面及移动截图并人工检查。没有运行完整 e2e 或集成套件。普通完整 Room 回归持续约七分钟后按用户要求停止，不计为通过；其他平台和输入内核全包普通测试通过。

## 真实平台、模型与满载实测

真实平台仅使用既有授权的选定项目 `product/agentichub/teamx-demo`，在隔离目录复制授权证据。传输只允许选定 origin 的 HTTPS GET，拒绝刷新、撤销和业务写入；生产 Room 配置及正文未修改。

- **组合链路**：真实 GitLab 查询 2 次、接收 2 条项目活动；一份共享正文、两个 Grant。两名真实 DSH 经 HTTP MCP 各自正式读取这两条真实正文，分别取得持久 Session 结果证明；甲结束后乙仍未消费。两个模型回合约 4.06／4.23 秒，整个测试约 14.67 秒。成员未配置独立远端读取绑定时，解析明确为 binding_unavailable，已授权保存快照仍交付，不冒充采集账号。报告为 `.local/unified-inbox-shared-combined-20261118/report.json`。
- **独立模型样本**：两名真实 DSH 各读完同一份 fixture 的两个标记并确认，验证正文没有复制到成员分区。约 16.17 秒通过，来源为 fixture；证据在 `.local/unified-inbox-shared-dsh-20261118/report.json`。
- **满载**：10000 条短 native payload、100 个流、两名成员；正文 8,984,674 字节，根／回执 3,780,546 字节，两名成员首屏协调、证明及结果共 20,914 字节。GC 后堆增量约 16.8MB；重开 0.238 秒，两个成员各取一页合计 0.400 秒，接近满载的新追加约 65ms。正常入口拒绝溢出、保留原正文，原 ID 可回放；释放 128 条连续前缀后，恢复原容量阻塞 ID 并取得下一序号。报告为 `.local/unified-inbox-shared-capacity.json`。

满载样本为一次构建的完整已发布 fixture，溢出、重投、正式页、回收和恢复追加使用正常内核与文件同步。它衡量该数据集的占用、首页和重开，不代表 GitLab 大正文、长时间吞吐或全部成员结果缓存的最坏值。此前尝试逐条同步追加 10000 条的运行在六分钟超时，原日志保留为 `.local/unified-inbox-shared-capacity-serial-timeout.log`，不将它计为吞吐验收通过。

生产部署、扩展多个共享 Inbox，以及 Room 接入共同内核和格式升级均未在本次执行。
