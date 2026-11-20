# 定时等待与日历发布夹具收缩

对应[测试改造方案 §9.2 第四批](../TEST-SUITE-REFACTOR-PLAN.zh.md#92-最推荐的实施顺序)。本批仅修改测试与实施文档；生产时间参数、持久同步、授权、构建标签和四槽上限均未改变。既有心跳、阻塞 writer、自动恢复 bubble 不重写。

## 断言承接映射

| 原检查 | 下层／受控时间承接 | 保留的真实边界 |
| --- | --- | --- |
| Inbox 三档退避、后到输入不能提前恢复、耗尽后不再重试 | `TestInboxRecoveryBackoffExhaustionAndExplicitContinue` 复用 `MemberAgent.now`，在 `synctest` 内推进原 80／100ms 窗口并 `Wait`；每档检查到期前 1ns 不运行、到期后运行；核对后到输入已被实际读取、完整 recovery 记录未变 | 三次真实 Actor turn、原范围、持久额度、启动恢复、Session generation 保存及人工继续审计保持原检查 |
| 暂停／运行中／checkpoint／预算／未知结果拒绝、失败回合须显式继续、过期不重置额度、正式消费进展清除恢复 | 原 `TestInboxRecoveryPauseCancelFaultAndBudgetGates` 直接调用 `reconcileInboxRecovery`／`inboxReady`／`continueInbox`，改用最小内存 MemberAgent 和固定时钟 | 上行退避／恢复组合继续验证持久保存；本项原先没有运行成员，删除的只是无关 Room／Inbox 装配与 25 次种子写入 |
| 确认消费后业务失败不重新陷入恢复 | 原 `TestInboxRecoveryConfirmedReadThenBusinessFailureDoesNotStall` 直接驱动已消费状态，不装配 Actor 或种子通知 | 历史分页用例继续经过正式 MCP 确认与真实 Store；未知动作拒绝及自动 Session 换代原组合保留 |
| 取消后不自动重试、人工继续可重新读取 | `TestInboxRecoveryCancellationNeedsExplicitContinue` 在 bubble 内推进原 60ms 窗口并 `Wait`，业务时钟仍前进一小时 | 实际运行、精确 execution 取消、backend 收到取消、interrupted 结清与人工继续仍经过 Actor／Store；历史分页和自动 Session 换代继续保留真实 timer 接线 |
| 连接结果消费后失败不重试、处理后跨 Session 不重投；原两处 150ms 窗口 | 两入口共用的 continuation helper 及 Session 轮换用例在各自 bubble 内推进 **1 秒**，覆盖原窗口与多次 250ms Inbox 轮询；`Wait` 后检查无新 turn | 原 manager／HTTP／真实保存、事件原 ID、Inbox 正常确认、失败状态和 Session 换代仍保留；`parallelFixtureTest` 在 bubble 外取得四槽，清理在 bubble 内完成 |
| 接受重复任务报告与回合结清不能改写后续计划 | 新 `internal/calendar.TestRecurringFinishPreservesSchedule` 直接覆盖 daily／weekly × succeeded／blocked／failed：实际 `Claim`／`Finish`／`TurnFinished` 后保持 active、NextAt、水位及资源版本，报告仍待发布 | 已有重复规则、DST、暂停／恢复、unknown／overlap 和有限日期规则不删除；不将内存结果当作报告已发布 |
| 重复任务真实发布后保持 active | 原 `TestCalendarRecurringPublicationKeepsTaskActive` 使用实际 CalendarActor、Room 和私有 Store，去掉完整 service、成员、输入准备和日历后台调度器 | 仍经正式 `calendarCall` 创建、真实评估／claim／finish、正式发布 worker 和持久提交；增加 settled／succeeded、非零发布序号、落盘回执与 Room 内精确作者／正文检查。实际“不终止任务”仍在组件层验证 |

两个纯规则用例合计去掉 50 次与目标断言无关的通知种子写入。其他 Inbox 持久恢复、manager 重建、CLI 和日历受管组合原样保留，没有增加跳过或将测试移到另一标签。

## 同条件计时

2026-11-20，macOS ARM64、Go 1.27.1，HEAD `e098afd` 加已有工作树。使用 `-tags=integration` 编译的测试二进制，经 `go tool test2json -t` 顺序执行，参数为 `-test.v=test2json -test.count=1 -test.timeout=3m -test.run=<证据中的 selection_regex>`；工作目录分别为 `internal/room` 和 `internal/calendar`。编译不计入切片墙钟，两个包按顺序运行，计时不与其他测试或构建重叠。

选择包含整个 Inbox 恢复组、三个受影响的连接通知顶层、日历发布组件及 `TestRecurring*` 规则。改前 18 项顶层，改后 19 项，包含新增六分支规则检查；全部通过、零跳过。改前源码由本次工作树快照通过 Go overlay 编译，没有回滚工作区或用 HEAD 替代前几批改动。

| 样本 | 改前合并墙钟（秒） | 改后合并墙钟（秒） |
| --- | ---: | ---: |
| 1 | 18.906 | 11.831 |
| 2 | 12.439 | 9.067 |
| 3 | 11.940 | 9.427 |
| 中位数 | **12.439** | **9.427** |

中位数约缩短 **24.2%**。改前日历完整夹具的样本为 6.16／0.23／0.24 秒，改后为 0.11／0.11／0.11 秒；两个纯规则用例改前各约 0.75–0.91 秒，改后 Go 报告各 0.00 秒。其余持久 I/O 和并行夹具仍有波动，不将异常长样本、各项 Elapsed 或重叠持槽时间相加作为收益，也不外推为全仓提速。

最初测量后，链接器出现 `no space left on device`，磁盘仅余约 332 MiB。确认未被进程占用后，删除前两批可重建的测试二进制及本批改前二进制，并核验 inode／mtime／大小后删除 19 个超过一天的旧 TeamX Room Go 缓存 archive（约 1.51 GiB）。源码、工作区、日志与其余缓存保留。上述正式三样本均在释放空间后执行；释放前的样本不作为最终对照。因实际 I/O 与旧日历夹具波动补到三次，没有重复全量基线。

## 验证

| 检查 | 结果 |
| --- | --- |
| 正常四槽、含新增规则的三次同范围切片 | 每次 19 项顶层通过，零跳过 |
| 同范围 `-test.parallel=1` | 19 项顶层通过、零跳过；两包顺序墙钟合计 10.632 秒 |
| 定向 `-race -tags=integration` | 37 项顶层／含子项 64 项通过，零跳过、无竞态报告；命令墙钟 46.776 秒，含构建 |
| 默认标签全仓编译，`-run '^$'` | 通过，12.668 秒 |
| e2e 标签全仓编译，`-run '^$'` | 通过，12.478 秒；未运行真实 e2e |
| `go vet ./...` | 通过，2.654 秒 |
| 完整核心＋本地集成 | 1,933 项顶层通过、9 项跳过，22 包通过；墙钟 948.975 秒（15 分 49 秒），八分钟预算未达；本批 19 项全部通过 |

race 选择包含全部 Inbox recovery、连接通知、默认助理连接、Room 日历递归、受管日历、日历持久报告／取消／未知提交及 `internal/calendar.TestRecurring*`。精确命令、选择、逐项结果、样本和源码摘要见[机器可读证据](timer-assembly-evidence.json)。源码已核对为相同版本；单并发不是与正常并发的性能对照。

完整集成使用一次 `go test -json -count=1 -tags=integration -timeout=16m ./... -run '.'`，包含核心；沿用上一批显式诊断上限，没有修改 Makefile 的三分钟／八分钟预算。未单独重测全仓核心、运行全仓 race 或执行真实模型／平台／浏览器；第五批及以后未实施。

完整集成本轮只运行上述一次，命令墙钟包含构建；1,933 项顶层通过、9 项跳过，22 包通过；墙钟 948.975 秒（15 分 49 秒），八分钟预算未达。未追加独立全仓核心或重复全量。此前第三批采用 Go JSON 事件跨度，本轮采用 Python 单调时钟，不以两者之差宣称全仓提速。完整健康门槛保持通过；剩余耗时仍需按第五批及以后独立分析。

本轮 Room 包执行 **940.585 秒**，首个顶层 run 到首个顶层并行 cont 的事件跨度约 **707.523 秒**；这是执行阶段观察，不是 CPU／I/O 精确分账。剩余串行慢项包括跨模型授权 17.06 秒、输入升级逐发布边界 14.80 秒、默认助理接入 10.64 秒。局部提速未改变全仓主成本，本轮完整入口没有证明提速，不能将 24.2% 写成全仓收益；不为本批扩展重构或提高并发／超时。
