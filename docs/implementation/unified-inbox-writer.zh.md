# 共用持久 writer 实施记录

对应[统一 Inbox 存储方案 S3](../UNIFIED-INBOX-STORAGE-PLAN.zh.md#s3--共用持久-writer-并发基础)，在现有 S1/S2 工作区上增量实施。正常持久合同见[存储架构](../storage-architecture.zh.md#持久-writer-的准入与路径所有权)。文件格式及路径保持兼容，通知 Gate 仍由 S4 切换。

## 实现边界

- `Store.save` 在任何目录或文件 I/O 前自行进行健康准入，管理和通知适配器共用这一入口。短健康锁保护目录锁状态、未知提交、writer 关闭及在途计数；每个已准入任务独立保存结果，直至临时文件清理完成才退出在途集合。
- 业务 owner 继续拥有状态的读取、修改及安装。同路径另有文件级串行保护，排队项取得路径后重新准入；不同路径的文件 I/O 可并发。路径登记在最后一个调用退出时释放，避免按历史文件永久累积路径锁。
- 目录缓存只在索引登记时持共用锁，实际 inode 检查及持久准备按目录串行。保留父目录和目录身份核对、失败重试，以及每次保存的文件同步和替换后目录同步。
- `Store.Close` 串行执行：先结清成员 owner 的完整已受理操作，再冻结 writer 新准入；在健康锁与局部 Store 锁之外等待所有已准入保存，最后结清局部 Store 操作并释放 OS 目录锁。正常 service 关闭在此之前已停止业务准入和结清 owner。目录锁释放状态改由健康 accessor 读取。
- Workspace 快照删除也经过 writer 准入、在途计数和未知提交判定。原有其他业务存储保留自己的协议，不扩大或缩小原 Store 冻结范围。
- 抽出普通 Go `snapshotWriter` 供 Store 组合，原访问会话、首次设置、模型管理和迁移的独立文件 writer 直接使用该类型。它们保留已有外部所有权和自身健康边界；真正的 Store 在共用 writer 准入前另行检查 OS 目录锁，不能以换地址绕过冻结或关闭。

成功仍以同目录临时文件、文件同步、关闭、原子替换及目录同步全部完成为准。替换前失败明确未提交，尝试替换后的模糊错误冻结该 Store。已准入的其他保存不重新检查全局冻结；其后新建的来源确认必须重新准入。

## 故障、竞争及恢复验证

| 场景 | 已验证结果 |
| --- | --- |
| A 的目录同步结果未知，B 已成功 | A 返回 `ErrCommitUnknown`；B 保留成功，Store 拒绝新保存 |
| A 未知时 B 停在不同边界 | 覆盖 B 的目录准备、文件同步、替换前、替换后及目录同步；释放 B 后按自身结果结清 |
| B 在替换前自身失败 | 保留明确未提交，不被 A 的未知结果改报为未知；重开无 B 文件 |
| 来源保存成功后共用 Store 冻结 | 通过真实 `notifications.SaveLocked` 入口拒绝下一次确认；内存及重开的来源状态均保留 pending |
| 同路径排队时前项未知 | 排队项未进入在途保存，取得路径后被健康准入拒绝，不覆盖原磁盘证据 |
| 一个 Room 的准备或文件同步阻塞 | 在释放该阻塞点前，另一个 Room 不同目录的保存已完成 |
| 同目录 32 路首次保存 | 只准备一次目录，全部保存成功；已有目录及父目录被替换、初始化失败重试测试继续通过 |
| 关闭中 A 未知而 B 仍在文件同步 | 拒绝新准入，等待 B 成功；B 未释放前第二个 Store 仍不能取得目录锁，全部结清后可重开 |
| 删除快照的模糊错误 | 删除和目录同步两处故障均冻结后续保存及删除；后续删除在 I/O 前被拒绝 |
| 独立业务 writer 与 Room Store 的边界 | 独立 writer 正常保存且未知后冻结自身；没有目录锁的 Room Store 仍拒绝保存 |
| 已有数据与重开 | 重新取得独占目录后加载完整快照；确认失败重投不重复私有收件，原成员、Session 与工具身份恢复规则继续通过 |

新增行为检查位于 `storage_writer_test.go`、`storage_writer_notification_test.go`。故障钩子与 channel 固定持久边界，独立完成断言都在释放阻塞点前执行。多边界检查在 race 下重复 5 次通过，日志为 `.local/unified-inbox-writer-boundaries-race.log`。

已有 snapshot、提交故障、目录所有权及成员关闭相关 race 通过，日志为 `.local/unified-inbox-writer-race.log`；首次新增来源确认及 writer race 日志为 `.local/unified-inbox-writer-confirmation-race.log`。抽出共用 writer 后，包含独立 writer、访问会话及来源确认的最终 race 通过，日志为 `.local/unified-inbox-writer-final-race.log`。

最终主要普通回归通过，覆盖全部 Snapshot、提交故障、目录所有权、MemberInput 及新增来源确认检查，日志为 `.local/unified-inbox-writer-main-tests.log`（3.444 秒）。

定向 integration 通过，抽出共用 writer 后复验也通过；最终日志为 `.local/unified-inbox-writer-integration-final.log`，包括：

- `TestRealCLIRestartRestoresMembersSessionsAndToolIdentity`：构建 fixture CLI、启动真实隔离服务进程、正常停止及重启，核对原成员、Session 和旧工具身份失效。
- `TestMemberNotificationGitLabMembersHTTPIndependentDeliveryAndRestart`：成员独立订阅经 HTTP 配置、投递及重开。
- `TestInboxLifecycleSourceAckFailureCrossesExpiryAndRestart`：来源确认失败后重开，保持原 DeliveryID、收件和过期策略。
- `TestGitLabEventsPageCommitFailureIsAtomicAndUncertainWriteLatches`、`TestGitLabEventsShutdownRetainsLockUntilQuerySettlement`：原通知来源持久故障与关闭等待。
- `TestWorkspaceSharedPlatformRestartAndUncertainStore`、`TestTodosReceiptsDeduplicateAfterRestart`：共用平台健康与来源去重恢复。

首次全仓回归发现既有独立文件写入借用无目录锁的 Store，新增准入因此误拒绝。这一问题已通过上述共用 writer 与目录所有权分离修复；访问、首次使用、模型管理/迁移和升级等定向兼容回归通过，日志为 `.local/unified-inbox-writer-compatibility.log`。首次失败日志 `.local/unified-inbox-writer-go-test.log` 保留用于追溯。

修复后再次执行全仓 `go test ./...`，Room 包仍在运行时，依用户“全仓很慢时跑相关主要测试即可”的后续指示停止了本次测试进程树；日志为 `.local/unified-inbox-writer-go-test-final.log`。本阶段不声称全仓回归通过，也不执行完整 e2e/integration 套件，以已通过的相关主要测试、race 和定向重启集成为完成依据。文档本地链接、Go 格式及差异空白检查通过。

本阶段未修改前端或工具合同，不扩展浏览器、真实模型或 provider 验收；上述真实部分是服务进程与本地文件系统，来源和 Session 使用 fixture。

## 后续门槛

S3 交付可供并发使用的持久 writer。当前生产通知仍经过管理 Gate，本阶段的不同 Room 并发故障检查在共用 writer 层进行，不据此宣称来源已经并行运行。S4 还要交付 NotificationActor、候选修订核对、异步可靠投递与来源确认、正式释放证据、Gate 移除及真实来源链路验证。共享 Inbox/Grant 与 Room 共用段内核分别保留为 S5、S6。
