# 每 Room 通知 owner 与来源释放实施记录

对应[统一 Inbox 存储方案 S4](../UNIFIED-INBOX-STORAGE-PLAN.zh.md#s4--每-room-通知-owner-与可靠释放协议)。在现有 S1–S3 工作区上增量实施，沿用私有 Inbox 与 Room 的原持久路径、格式和位置；共享 Inbox/Grant、Room 共用段内核分别保留为 S5、S6。

## 实现与完成语义

- `notification.Manager` 预留每个完整 RoomIdentity 的唯一 `NotificationActor`。来源可变 State 只在该 owner 的受控任务中修改；每个 owner 一个持久任务、最多 64 个排队请求。任务携带 operation ID 与 activation，等待取消不撤销已受理保存；状态从原子安装的冻结投影读取。
- 删除生产 `Host.Gate` 和 S2 的全量来源释放快照。来源保存、目标收件、Actor 花名册查询和成员维护在 manager 锁外等待，业务准入仍检查当前目录、连接、配置和成员变更。共用 writer 继续实行 S3 的健康、并发结果和关闭合同。
- `StepHost` 固定配置版本、数据修订及连接 revision/credential stamp，提交冲突要求重查。来源持久提交成功后才推进 runner 缓存。凭据核验恢复通过 owner 在已核验配置版本下对最新进度构造候选，保留期间发生的来源确认。
- 每订阅同时最多一次投递，固定原项、路由和 DeliveryID。目标发布回执返回后，来源 owner 核对原项、基于最新 State 保存确认，保留并发查询新增项。首次投递建立目标背压，之后查询与投递可重叠；关闭会加入两者的结清。目标成功、来源确认失败时仍保留原 pending，重试返回同一目标回执。
- 成员在变更槽外请求有限候选的 `CheckSourceRelease`，不再克隆全部历史订阅。结果绑定精确订阅、目标、路由版本、DeliveryID、位置、正文摘要、数据修订和 activation。outstanding 为 held；持久观察、过期或 provider 的单调准入下界才能证明 released；历史缺失、身份不符、未知提交及不可核实返回 unknown。disabled/retired 保留其原命名空间。
- 本地回收前重验来源 activation/健康、候选摘要、消费、关联、正式页及预览保护。来源 released 只解除来源保护。来源确认及成员周期任务进入同一个成员维护操作，由成员 owner 合并和串行执行；Inspect 不触发维护。
- 同来源修订和策略的空闲检查缓存最近到期时间，安装新状态、到期或策略变化时重查。正文继续使用 S2 已验证的有限候选、增量索引、单边界段重写及重开校验，没有再加一套索引或逐条墓碑。
- 停机冻结新操作、取消来源查询、等在途投递/确认和已受理保存，然后 quiesce actor，最后释放 Store。删除在文件移除前结清来源 owner；超时保留所有权。Room runtime 暂停或关闭不删除来源 State，目标未就绪保留 pending。

唯一来源 writer 是 NotificationActor；可靠收件以目标正文发布根及去重回执落盘为准；来源确认以原订阅快照提交为准；消费继续以成员检查点及匹配的 Session 结果证明为准。重开分别从这些原权威核查，不从投递应答、来源缺项或日志尾推断成功。

## 故障、竞争和回归

| 验证 | 结果与边界 |
| --- | --- |
| A 来源保存阻塞，B 独立提交 | 通知内核 race 下 A 的 100 次已提交状态读取均完成，B 保存先于 A 释放完成；未发布的 A 状态不可见 |
| 收件等待与查询提交 | 阻塞目标回执时查询可以保存新增项；随后来源确认基于最新状态合并，旧查询与旧配置快照被拒绝，不恢复已确认 ID |
| 来源关闭中保存未结束 | 超时保留已受理保存和 owner；释放后原调用取得自己的结果，重复关闭完成，关闭后新保存拒绝 |
| 来源释放与重开 | Events、Todos、Lark 均覆盖持久观察/过期下界及解码重开；Lark 覆盖观察压缩后的会话下界；通用协议覆盖历史缺失、错误目标、disabled/retired、activation 失效和来源不健康 |
| 稳定 ID 确认失败恢复 | 子进程在目标发布后、来源确认写入前直接退出，父进程取得新目录锁并恢复两份权威，重投后仍是一份正文及原位置，来源确认成功；另覆盖确认失败关闭重开、保留期跳过和 Session 证明 |
| 两 Room 完整隔离 | 分别阻塞 A 的来源目录同步及目标本地写入；释放前 B 的来源保存、目标投递及来源确认均完成，目录和两个 Room 通知状态共 400/400 HTTP 200 |
| 持续输入与维护 | 原 MemberInput 的合并、公平性、有限回收和输出 lease 回归通过；来源容量、到期恢复、disabled backlog、成员移除及 worker 结清集成通过 |
| 前端 | 原页面回归 192/192 通过 |

隔离 HTTP race 样本每轮包含四次请求：来源保存阻塞时 p50/p95/p99 为 4.92/6.10/6.72ms；目标收件阻塞时为 4.98/6.83/30.27ms。它们是本机测试样本，不是生产延迟保证。日志为 `.local/unified-inbox-notification-isolation-race.log`。

1000 条 pending 的缓存检查：100 次空闲维护只扫描一次库存、不保存来源快照；时间推进到期后只提交一次过期状态。race 样本相对创建前多 1 个 goroutine，GC 后 heap 差值约 14KiB；这是该样本过期后的净差，不是满载占用或峰值承诺。日志为 `.local/unified-inbox-notification-owner-final.log`。

相关主要普通回归通过：Room 的通知、输入、MemberInput、Inbox 与目录测试用时 165.33 秒；inputstream/notification/GitLab/Lark 全包普通回归分别约 16.72/1.14/1.58/10.32 秒。定向通知内核、provider 释放/重开及 Room 集成 race 通过。凭据恢复竞争修正后的单项 race 重复 3 次通过。最终准入 race 日志为 `.local/unified-inbox-notification-admission-final-race.log`。

进程中断检查 `TestNotificationOwnerReceiptCrashRecovery` 在 race 下通过，日志为 `.local/unified-inbox-notification-crash-race.log`。

原始日志另包括 `.local/unified-inbox-notification-regression.log`、`-engine-regression.log`、`-release-final-race.log`、`-room-final-race.log`、`-verification-race.log` 和 `-web.log`。依用户要求按相关和重要检查收尾，未运行全仓 Go 或完整 e2e 套件。一次全 Lark race 持续占用 CPU 超过数分钟后停止；其普通全包回归及本次涉及的释放、过期、来源和升级定向 race 通过，不把被停止的运行记为通过。

## 真实平台、模型、浏览器和进程

以下均使用隔离目录，没有修改生产会话或通知设置；外部业务写入为零。

- **GitLab 正常配置与来源**：使用已有有效授权，传输层只允许选定 origin 的 HTTPS GET，拒绝刷新、撤销及业务写入。Events 真实查询 2 次，本次时间窗口没有新增命中，因此不据此声明真实 Events 正文交付；Todos 查询 4 次，接收 2 条并完成成员可靠收件和来源确认。首次限制为单页的样本出现 `scan_incomplete`，调整为明确的四页预算后复验通过。证据在 `.local/unified-inbox-notification-gitlab-20261118-2/report.json`。
- **Lark Search 正常 HTTP 配置与收件**：选定现有样本内一个群的两分钟窗口，真实搜索 1 页、详情 1 批、核验 2 次，2 条通知完成成员收件。没有宣称覆盖完整历史或所有会话类型。证据在 `.local/unified-inbox-notification-lark-20261118/report.json`，日志为 `-lark.log`。
- **真实 DSH / HTTP MCP**：`TestInputSimplePersistentRealDSH` 使用真实配置模型，正式读取两个保存的 fixture 通知并取得 Session 结果证明；13.70 秒通过。真实部分为模型、DSH、HTTP MCP 和消费确认，来源正文仍为 fixture。证据在 `.local/unified-inbox-notification-dsh-20261118/evidence.json`。
- **正常进程重启 / DSH**：`TestInputUpgradeNormalRestartRealDSH` 启动隔离真实服务、读页、正常停止和重启，核查旧回答、输入与原生游标并继续确认；17.46 秒通过。来源是隔离数据。证据在 `.local/unified-inbox-notification-restart-20261118/report.json`。
- **Chromium**：阻塞真实来源目录同步时刷新实际页面，100/100 次目录请求成功；释放前完成双向 Room 切换，释放后历史与各自草稿保留，零 pageerror。浏览器 fetch p50/p95/p99/max 为 1.90/5.00/9.40/12.70ms。模型与来源内容为 fixture，后端是实际 Room actor、writer 与本地 HTTP。日志为 `.local/unified-inbox-notification-browser.log`。

上述证据分别覆盖各链路，未宣称用真实 GitLab/Lark 消息完成同一回合的真实模型确认，也未进行生产部署。稳定 ID 失败窗口还在独立子进程中以 `os.Exit` 直接退出验证；没有模拟操作系统断电。

核心说明已更新[平台架构](../platform-architecture.zh.md)、[Room 架构](../room-architecture.zh.md)、[存储架构](../storage-architecture.zh.md)、[Server 架构](../server.zh.md)和 [Inbox](../inbox.zh.md)。共享正文、Grant 与 Room 共用段内核仍分别按 S5、S6 推进。
