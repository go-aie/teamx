# 私有输入 owner 与有界维护实施记录

对应[统一 Inbox 存储方案 S2](../UNIFIED-INBOX-STORAGE-PLAN.zh.md#s2--私有输入-owner-与基本有界维护)。在基线 `c1ea70a` 及已有 S1 工作区上增量实施，保留私有和 Room 持久格式；不启用共享 Inbox，也不切换通知 owner 或 Room 日志内核。

## 实现与完成点

- Store 为每个完整 MemberIdentity 预留唯一 MemberInputActor，加载 Repository 后发布地址，service 复用同一 handle。请求分别携带 Inbox 与成员身份。普通 Go 内核通过正文、消费、执行协调和回收四组能力静态组装，保留旧加载器、分区、位置和引用。
- 正常收件、执行初始化、正式页、Session 证明确认、额度、目录曝光、关注度和历史预览经成员 owner。业务适配器仍在局部 Store 锁内只读已发布正文和证明；原业务模块保留关联与动作写入权。离线升级仅在独占目录中使用原 Repository。
- handler 登记有界 operation，task 串行执行本地变更。最多 64 个未结清请求；维护运行时的新请求合并成一个后续批次，上一批维护结束后，已排队业务可先于下一批维护执行。材料准备和管理层来源解析不占存储变更槽，正式读页另有 owner 管理的输出 lease。
- Inspect 通过已发布地址直接读取原子投影，不取 Store 文件锁、不请求通知 Gate、不扫描或触发维护。投影在回复及 SSE 信号前安装；派生视图失败局部表达 unavailable，不把已持久收件或确认改报失败。来源准入和到期维护由独立后台 task 推进。
- 来源 Gate 内先固定一个分区、最多 128 个目标候选，再捕获精确订阅命名空间的不可变释放证据。核验完整 Room、目标、SubscriptionID、冻结路由、DeliveryID 和候选位置/摘要，逐项返回 unknown、held、released 及来源修订。缺失历史、目标不符和来源 writer 不健康都保持 unknown。输入变更 task 不反向取得 Gate。
- 回收领域规划达到 10ms 或 128 条先结束，ConsumptionRelease 与 Plan/Prepare/Commit 接受有限候选。正常提交最多重写一个边界段，只删除本批原段，增量安装正文索引和字节计量，不全量重载分区。预览取得段 pin；提交前重新核对页与 pin。源侧终态已核实才压缩对应去重回执。
- 过期跳过单独持久提交，尊重服务保留天数和跳过开关，不伪造消费或业务办结。回收计划绑定实际保留策略。未结清正式页、来源 unknown/held 与未知提交继续保护正文。
- 回合保存中的 Seal 也经输入 owner；重开恢复已持久执行 generation。新执行不能越过在途输出 lease。移除使旧 owner 退役并结清，释放 Store 中的正文与索引引用；旧 handle 不能重新创建 writer 或继续返回成员预览。关闭 quiesce 后等待已受理队列和输出 lease，超时保留 runtime 与目录锁。

收件以分区根及回执持久提交为成功；消费以匹配 Session 证明驱动检查点及同页证明提交为成功。Room 的读取权威仍是 Room Actor 的原生游标和 ReadProofs，成员页在其后结清。来源释放与业务关联是分别核实的回收事实。重开依赖原发布根、容量预留、消费文件、协调器和 Session 证明，而不是日志尾或“已写出”推断。

## 故障与回归证据

| 检查 | 结果及实际范围 |
| --- | --- |
| 维护阻塞时 Inspect | 维护 task 持有 Store 锁且未释放时，100/100 次成员 Inspect 已完成；普通运行共 0.276ms，race 样本共 3.175ms，均为本地 Actor 调用总时长，不是 HTTP 延迟承诺 |
| 合并与公平性 | 8 个维护请求合为一个后续批次，已排队业务在该批前执行；达到 64 个在途请求后返回可重试 busy |
| 取消与关闭 | 取消等待不会丢弃已受理写入；关闭超时后第二个 Store 仍无法取得目录锁，释放任务/输出 lease 后可以继续结清 |
| 来源解析等待 | 阻塞正式页的来源 resolver 时，同一成员的本地维护和 Inspect 先完成，证明没有占住反向依赖的变更槽 |
| 预览与有限回收 | 300 条稀疏序号按 128/128/44 条推进；准备计划后新 pin 阻止提交，解除后完成；每批增量索引、容量与重新打开的完整加载结果一致 |
| 原策略与证明 | 保留 7 天、过期跳过及重开核实；已有 A/B/A、跨分区同序号、同键幂等/摘要冲突、图片额度、Room 原生证明与 Session 恢复测试继续通过 |
| 视图及只读边界 | 派生视图故障不反转可靠收件；目录和预览不提交消费排除；退役成员不暴露旧投影 |
| 来源过渡证据 | 固定快照不随后续确认变化；精确历史订阅缺失、目标错误和未知提交返回 unknown，未确认项 held，已提交终态 released |
| 隔离服务与通知回归 | integration 覆盖来源确认失败重开、disabled backlog、移除重加、容量、关闭等待投递、图片与撤权缓存，以及来源/维护阻塞中的目录 HTTP 请求 |
| 前端 | `node --test internal/web/tests/web_app_test.mjs`：192/192 通过 |

定向普通回归、相关 race 与上述 integration 已通过。全仓普通回归也已通过，Room 包用时约 490 秒，日志为 `.local/unified-inbox-owner-go-test-final.log`。后续依用户要求仅补必要的边界检查，未重复全仓。新增或修正的检查包括第三方来源的派生视图故障、原生 Room 一次清空超过 128 条、输出 lease、退役后的索引释放，以及其他领域安装来源准入证据后的后台状态刷新。

相关日志为 `.local/unified-inbox-owner-race.log`、`.local/unified-inbox-owner-review-race.log`、`.local/unified-inbox-owner-final-check.log`、`.local/unified-inbox-owner-projection-check.log` 和 `.local/unified-inbox-owner-integration.log`。绑定生成、文档链接和 `git diff --check` 均已核验。

## 真实 DSH 与服务重启

使用现有 `.teamx/config.toml` 的真实 `deepseek-v4.1-flash-1017`，测试目录及服务均隔离，未改生产会话或通知配置。

1. `TestInputSimplePersistentRealDSH`：真实 HTTP MCP，读取 Room 限制，再列举并逐条正式读取两条已保存的 MR 通知。两条平台页取得持久 Session 结果证明，最终 settled/received；报告保留原只读要求。最终复验模型回合约 10.99 秒，整个测试 15.03 秒。
2. `TestInputUpgradeNormalRestartRealDSH`：真实服务进程打开已有数据、核验输入合同并执行读页，经正常停止/重启后保留输入状态和原回答，继续确认后续 Room 消息。原生 ReadThrough 为 2 → 3 → 3 → 4，测试 20.13 秒。

第一项使用保存的 fixture 平台通知，真实部分是 DSH、模型、HTTP MCP 与 Session 证明；未请求真实 GitLab/Lark，也未在模型验收中输入图片。第二项真实执行服务和重启，来源仍为隔离数据。两项均记录 external_writes=0，不据此声称真实 provider 或浏览器验收。

原始日志与证据位于 `.local/unified-inbox-owner-simple*.log`、`.local/unified-inbox-owner-simple*-20261118/evidence.json`、`.local/unified-inbox-owner-restart.log` 及 `.local/unified-inbox-owner-restart-20261118/report.json`，不复制凭据或 Session 私有正文到核心文档。

## 文档与后续门槛

已更新[核心概念](../concepts.zh.md)、[Room 架构](../room-architecture.zh.md)、[Agent 收件箱](../inbox.zh.md)，新增[存储架构](../storage-architecture.zh.md)并从[文档目录](../README.zh.md)和架构页链接。正常工具说明已修正旧 turn_number、正文 next_cursor、仅写出即消费及逐条 JSON 正文说明。

S3 的不同 Room writer 并发准入、未知提交和统一关闭合同仍待实施。S4 才删除过渡 Gate、接入 NotificationActor，并按测量优化来源库存捕获、目录/状态重建、根与检查点编码等固定上限工作。共享 Inbox/Grant 和 Room 共同段内核仍分别属于 S5、S6；本次不迁移旧数据格式、不启用这些能力。
