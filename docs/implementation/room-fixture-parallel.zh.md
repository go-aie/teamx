# Room 独立夹具有界并行

对应[测试改造方案 §9.2 第一批](../TEST-SUITE-REFACTOR-PLAN.zh.md#92-最推荐的实施顺序)。本次只审核四个候选文件，复用 `parallelFixtureTest` 的四个槽位；不删除断言、下沉规则、移动标签或修改生产代码。

## 隔离审核与覆盖承接

| 原用例范围 | 隔离与清理审核 | 原断言及保留的真实边界 |
| --- | --- | --- |
| `platform_member_current_test.go` 的 7 项夹具测试 | `memberNotificationToggleFixture` → Lark 工具夹具／GitLab OAuth 夹具均拥有私有临时 Store、manager、队列和 hook；GitLab TLS server 使用随机端口，8088 仅为模拟回调地址。阻塞预留在启动 goroutine 前注册释放。子场景保持串行。 | 原测试承接全部断言：单次业务请求、精确来源／成员／账号／订阅身份、历史回执、冻结准入、真实落盘失败和重启后的未知提交防重。 |
| `platform_connect_notifications_test.go` 的 13 项夹具测试 | 团队和个人助理夹具各自创建私有 manager／Store；认证 session、通知 worker、受控 backend 和故障 hook 均属于当前夹具；`connectPending` 的 done 清理先于 manager 关闭。原生 CLI helper 使用所属 manager 的私有 Lark 队列。 | 原测试承接全部断言：完成前／中／后订阅、取消／处理身份、持久交接故障点、恢复后原序号、容量回收、自动继续／失败不重试、成员移除、准入与 Session 轮换。 |
| `platform_lark_authorization_test.go` 的 5 项夹具测试 | `roomAuthProfile` 的可执行文件和授权标记位于私有目录；配置 profile 同名不能保证队列隔离，因此显式复用 `NewClientFactory(0, 0, 0)`，同一 manager 内仍共享账号锁／队列。HTTP server 使用随机端口；关闭 manager 取消并等待授权进程退出。 | 原测试承接全部断言：共享连接精确身份、权限回执、无工具绑定 CLI、换号／取消、OAuth 期间版本变化及中断不推定成功。 |
| `model_management_test.go` 的 10 项夹具测试 | 模型 TOML、凭据、操作 journal、manager、受控 probe／backend 和持久故障点均私有。`modelTestService` 只解析 `[env]`，不调用 `applyStartupEnv`。阻塞 probe／turn 先注册释放；失败断言前释放 manager 锁。 | 原测试承接全部断言：默认／显式引用、关闭 Room／重启、当前 turn 结清、暂停优先、独立发布、失败隔离、幂等重试、未知提交、创建并发、取消和同 Room 独立凭据。 |

以上调用链没有 `t.Setenv`、`t.Chdir` 或固定环境键写入。`managerTestProfiles` 为每次调用生成随机 `TEAMX_MANAGER_TEST_KEY_<id>`，只删除自己设置的键，manager／进程清理先于凭据清理，最后才释放夹具槽位。Lark 子进程只继承正式白名单环境，不继承这些随机键；受控 DSH backend 不启动真实模型进程。共享 `fixtureScripts` 只读，未引入共享可变 Store 或扩大并发上限。

`TestPlatformMemberCurrentCLIRejectsMixedModes`、`TestPlatformMemberToggleIdentityScope`、`TestWorkspaceSnapshotComparisonExcludesOnlyModelReference` 无重型夹具，继续直接串行执行。没有删除或收缩业务分支，因此无需新增下层替代测试。所有持久同步、授权与生命周期断言仍在原层执行。

## 验证与计时

环境：2026-11-20，macOS ARM64、Go 1.27.1；HEAD `77e6fee303277d24d7f08f678c2e37e8bd7b91ff` 加当时已有工作树。先保存四文件原件并构建改前二进制，按 7／7／6／5／10 项逐组启用并行并定向验证，再进行一次四文件同条件对照。选择范围始终包含原 38 项顶层测试（含子场景 80 项），没有新增或退出用例。源码摘要、精确选择正则及逐项 wait／held 见[机器可读证据](room-fixture-parallel-evidence.json)。

| 检查 | 结果 | 命令墙钟／包执行（秒） |
| --- | --- | ---: |
| 改前核心，同范围、默认并发 | 38 项顶层／含子项 80 项通过，零跳过 | 42.870／42.483 |
| 改后核心，同范围、默认并发 | 38 项顶层／含子项 80 项通过，零跳过 | 24.367／24.282 |
| 改后核心，`-test.parallel=1` | 同范围全部通过，零跳过 | 45.417／45.216 |
| 改后 `-race -tags=integration` | 同范围全部通过，无竞态报告 | 29.404／29.253 |
| 全仓核心＋本地集成 | **未通过**；1,914 项顶层通过、1 项失败、9 项跳过；本轮 38 项全部通过 | 831.331／Room 803.035 |

切片计时顺序执行，不并行运行其他测试命令；复用匹配源码／标签／race 的测试二进制，通过 `go tool test2json -t` 执行，参数为 `-test.v=test2json -test.count=1 -test.timeout=3m -test.run=<证据中的 selection_regex>`，工作目录为 `internal/room`。二进制分别用 `go test -c` 和 `go test -c -race -tags=integration` 构建，编译不计入上表切片墙钟。单并发只额外设置 `-test.parallel=1`，不与普通并发或 race 混算收益。

同范围墙钟 **42.870 → 24.367 秒，缩短 18.503 秒（43.2%）**。35 项夹具的最大槽位等待约 6.86 秒，持槽中位数 2.13 秒，持槽合计 92.04 秒（包含清理）。持槽合计是多个重叠任务的资源占用，不能当作套件墙钟；它高于改前四文件串行顶层合计 41.78 秒，显示并发下单项成本增加，可能包含磁盘竞争与调度影响，尚未进一步剖析。因此本轮止于四文件，保持四槽。该局部结果不代表全仓核心缩短 43.2%，也未重新测量三分钟核心预算。

开发中自动继续组曾有一项因检查点准入 `ErrBusy` 失败。原 helper 在后台维护仍可能提交时直接初始化／读取／确认 Inbox；现复用同文件已有 `consumeConnectTestInbox`，只在五秒界内处理明确的忙碌拒绝，确认始终使用同一 turn／batch receipt。原完成事件、正常消费及失败 turn 不重试断言全部保留；修正后的六项切片、38 项组合、单并发及 race 均通过。终止结果与模型校验的失败断言移到锁外，三个模型阻塞点先注册幂等释放，probe 启动等待另有五秒上限。

完整集成执行一次 `go test -json -tags=integration -count=1 -timeout=15m ./...`，包含核心，正常收完且未超时，命令墙钟包含编译。21 个包通过，Room 包失败，5 个包无测试；Room 顶层 1,406 通过、1 失败、7 跳过。此前完整集成超过十分钟仍未结束，因此本次显式使用 15 分钟诊断上限，Makefile 默认上限保持原值；该上限调整不算优化。

唯一失败为 `TestLarkAuthHTTPGuardsCancellationAndShutdown`：[旧断言](../../internal/room/platform_lark_auth_integration_test.go#L92) 要求 `img-src 'self';`，当前 [Web CSP](../../internal/web/handler.go#L53) 为 `img-src 'self' blob:;`，对应 [Web 合同测试](../../internal/web/handler_test.go#L39) 已采用后者。这组不一致已存在于当前 HEAD，与本批四文件及 helper 无调用关系；本次只记录，不改动图片预览或旧断言。**完整集成仍未通过，不能将 13 分 51 秒写成健康基线，也没有完成八分钟预算验收。** 本轮 38 项在该完整运行中均通过。

本次集成中仍较慢的串行项包括跨模型轮换授权 14.35 秒、个人助理接入 7.67 秒、输入升级发布边界 7.18 秒；完整六项见机器证据。这些是当前集成负载下的局部观察，不能与旧核心计时直接同比。第一批降低了所选四文件的墙钟成本；全仓剩余串行成本仍需按原方案后续批次处理，本轮未继续扩展。全仓 race 和真实浏览器／模型／平台未运行。
