# 测试健康度与核心回归提速

对应[测试改造方案](../TEST-SUITE-REFACTOR-PLAN.zh.md)。状态：**T1 修复及 T2–T4 实施与各自定向验证完成；最终全仓核心 7 分 33 秒通过、三分钟预算未达；完整集成按用户要求中止，最终全仓 race 未运行**。下文保留各阶段证据、覆盖承接及计时；不将定向结果视为完整套件通过。

## 修复与覆盖

| 用例 | 原因与处理；保留的边界 |
| --- | --- |
| `TestTodosAppHandler`、`TestTodosAppHTTP` | helper 持锁 Fatal 导致 manager 清理死锁。短临界区复制状态与元数据，解锁后断言；UI 先关闭独立接收开关，再取消最后一个来源，保留两种传输的完整业务流程。 |
| `TestTodosAppAssertionFailureClosesManager`（新增） | 用当前测试二进制隔离预期 Fatal，要求 10 秒内退出码 1、准确失败信息，并在 manager 清理后写入标记；超时或缺少标记均失败。 |
| `TestInputUpgradeRotatesSettledDSHContract` | 固定两个工具的合同已过期。升级集成确认 Inbox、History 均被采用，保留代次替换、实际执行绑定、旧参数拒绝和输入读取；完整工具集合与 Schema 仍由 `TestInputToolsMinimalContract`、`TestHistoryMCPModesSchemasAndNoConsumption` 与 `TestHistorySchemasAndProductionReadOnlyDefinitions` 承接。 |
| `TestRoomsCatalogDuringBlockedNotificationWork` | **产品缺陷**：过期输入消费释放检查使用尚无版本的维护事实，返回 `input_invalid`。在首次检查前生成事实版本；阻塞任务退出后恢复 hook，失败时也先释放并等待结清。保留真实持久边界阻塞时 100 次目录 HTTP 读取。 |
| `TestPlatformInputExpiryMaintenanceRequiresSourceRelease`（新增） | 核心层证明过期且来源已结清的输入可跳过并回收，来源仍待确认时不得回收；无需 manager 或外部进程。 |
| `TestIndependentEventsAppHandler` | 等待成员平台列表完成实际读取；先关闭独立接收再取消最后来源。保留无仓库绑定配置、替换项目、保留前代与 Workspace 不变检查。 |
| `TestIndependentEventsPublicConfigureAndStaleVerification` | 请求目标错误复用了另一 Room 的 membership，改用实际目标 Room 的成员；保留重复配置冲突与并发编辑后拒绝迟到核验，预先注册阻塞释放。 |
| `TestIndependentEventsReplacementWaitsForOldWorker` | `Stop` 永久关闭服务准入，不能在它之后继续创建。旧 worker 实际结清后恢复正常 fetcher，继续验证替换成功；保留结清前禁止替换、取消与迟到结果不落盘。 |
| `TestTodosReplacementFailureNeverEnablesBothAccounts` | 暂停的订阅允许保留回执 worker，不等于旧账号恢复查询。重启后恢复同一受控时钟并主动驱动 runner，验证旧身份准入拒绝；计数实际 fetch 准备次数，并在 worker 全部结清后断言为零，保留历史与退休/绑定变化检查。 |
| `TestTodosTCPHTTPConfigureSyncDisableAndInboxDelivery` | 取消最后来源时同时关闭接收，符合现行显式来源合同；保留真实 TCP 的保存、同步、关闭与 Inbox 单次投递。 |
| `TestLarkNotificationsUpgradeInterruptedCommitRecoveryAndConflict` | replacement 失败会冻结 writer；断言未知提交后关闭 Store、释放目录锁并重新打开再恢复，保留第三方冲突拒绝、原始备份与 journal 完成检查。 |
| `TestRuntimeBackgroundNotificationsIgnoreAnotherRoomsOperation` | 投递目的地已经是成员 Inbox，改查 owner Inbox 且另一 Room 不收到输入；两边会话不产生消息。预先注册生命周期占用结清，保留本 Room busy 拒绝。 |
| `TestPolicyManagementHTTP` | 替换团队夹具后未刷新目录投影，页面按旧团队创建失败；发布新目录，并等待 Room/成员就绪。保留规则读写、历史、撤销、并发冲突与草稿恢复，增加关键阶段诊断。 |
| `TestSecondNotificationProviderReloadAndDedup` | 重建 notification manager 丢失受控时钟，按真实日期过期清理旧样本；每次装配后恢复同一时钟。保留双 codec 落盘重载、丢失回执重放防重和跨 provider 隔离。 |

共享 stdio Node helper 在启动后注册取消、关闭输入与回收进程，失败也执行；请求携带同一取消上下文，并对退出后的管道等待设界。未删除测试、放宽身份条件、增加业务重试或调整构建标签。生产代码仅修复维护事实版本生成顺序，持久同步、未知提交冻结与关闭合同保持原样。

## 验证

环境：2026-11-20，macOS ARM64、Go 1.27.1、Node 26.7.0；源码 `b136f0edf5d87103423ba77b830a700aab0dc5ed` 加本次工作树。主测量顺序执行，保留构建缓存并使用 `-count=1`。为容纳现有慢项，本次诊断显式使用 15 分钟包上限；未修改 Makefile 默认上限，不将提高上限计为优化。

| 检查 | 实际结果 |
| --- | --- |
| 全仓核心：`go test -json -count=1 -timeout=15m ./...` | 通过；墙钟 **557.748 秒（9 分 18 秒）**；Room 543.508 秒。顶层 1,347 通过、10 跳过、0 失败；含子用例 3,202 通过、11 跳过。Room 顶层 900 通过、8 跳过。 |
| 上述 15 项相关顶层测试，`-race -tags=integration -count=1 -timeout=3m ./internal/room` | 全部通过；Room 19.214 秒。覆盖附录 A 全部 11 项、两个 Todos UI 路径，以及两个新增回归。 |
| 最后补强的恢复 runner 与工具 Schema 合同，定向 race | 2 项顶层测试及其子用例通过；Room 2.691 秒。 |
| `go vet ./...` | 通过。 |
| `node --test internal/web/tests/web*_test.mjs` | 767 通过、0 失败、0 跳过。 |
| `go test -tags=e2e ./... -run '^$'` | 编译通过；不代表真实 e2e 验收。 |
| 完整核心＋本地集成的合并运行 | **主动中止，未验收**；没有可用的完整耗时结论。 |
| 复用核心结果、仅补跑 integration 新增项 | 构建清单核对出 544 项新增顶层测试：Room 494、Lark 39、Scion provider 4、Scion tool 7；没有丢失核心测试。补跑随后按用户要求停止，不能当作完整通过。 |

完整 integration 会重复执行刚通过的核心部分。用户反馈本轮等待过长后，先改为差集补跑，随后按进一步收口要求停下全部长跑；该差集只用于本轮诊断，没有新增永久入口或测试名清单。跳过项仍沿用原有显式启用条件，没有为本次修复增加跳过。

机器可读结果与慢项见 [test-suite-health-evidence.json](test-suite-health-evidence.json)。原始本机诊断位于 `/tmp/teamx-t1/`，不是长期 gate。已复现失败均有上述处理与通过的定向证据，但完整集成中其他用例是否存在未发现失败，仍需后续确认。

## T1 测量后的提速入口

T1 只恢复健康度，**没有实现提速**。当时核心全仓耗时仍超出现有 3 分钟默认上限；放宽诊断上限不是优化结果。下表保留 T1 的原始观察，T2 结果见下一节。

| T2 优先入口 | 本轮观测 | 下一步 |
| --- | --- | --- |
| `tools_target_concurrency_test.go` | 12 个串行顶层用例，报告耗时合计 40.29 秒 | 审核独立 Store、环境与 hook 后复用四槽夹具有界并行。 |
| `tools_lark_configuration_test.go` | 10 个串行顶层用例，合计 36.61 秒 | 同上；仍需重建完整服务的组合留给 T3 下沉。 |
| `ui_updates_http_test.go` | 串行组 22.83 秒；心跳单项 15.08 秒、阻塞 writer 5.99 秒 | 受控 tick／输出屏障，保留真实 HTTP 帧、取消和撤权复核。 |
| `automatic_recovery_test.go` | 10 个串行顶层用例，合计 19.60 秒 | 受控时间结合 Actor 事件屏障，保留暂停、结清与永久错误边界。 |
| `TestPlatformAuthorizationSurvivesModelRotationAndPause` | 单项 21.73 秒 | 先拆明轮换、暂停与等待的成本，再决定 T2 时间控制或 T3 组合缩减；保留跨轮换授权证明。 |

表中的组耗时仅汇总**非并行顶层**测试，不叠加子用例，也不作为包墙钟。Room 核心仍只有 12 个顶层测试使用并行。

后续先做目标慢组与必要 race，不再连续重跑完整核心和包含核心的完整 integration。完成提速后，以一次完整 `make test-integration` 补齐本轮未完成门槛；若需要专门比较核心入口，再单独安排其计时。不要因耗时而弱化身份、持久化、未知提交或关闭断言。

上述 T1 工作没有启动 Chromium、真实 DSH/模型/平台，也未操作运行实例、重启或部署。T2 提速见下文；规则下沉、集成矩阵和浏览器分层仍留给 T3–T4。

## T2：受控时间与有界并行

T2 只修改测试与夹具，生产时间参数、授权、持久同步及关闭语义保持原合同。没有删除测试、增加跳过或移动构建标签。

| 原测试／断言 | 承接方式 | 保留的真实边界 |
| --- | --- | --- |
| `TestUIUpdatesHTTPHeartbeat` 的 15 秒等待 | `testing/synctest` 驱动生产 ticker；通过 `net/http` 与 `net.Pipe` 读取实际 SSE 帧，验证到期前不发心跳、连接身份一致、下一次心跳复核撤权并关闭连接 | 同时保留普通 server 一秒写超时，并跨过完整生产心跳间隔，证明 SSE 覆盖写超时；原 TCP HTTP、重连与服务关闭测试继续保留 |
| `TestUIUpdatesBlockedWriterCancellation` 的五秒 deadline 和到期等待 | 每个场景独立时间 bubble；`synctest.Wait` 确认 writer 和取消观察者到达阻塞点，完整走过时间窗，检查到期前仍阻塞、到期后中断 | 请求取消、登出、设置变更、shutdown、session expiry 与写 deadline 六种原因，继续经过真实 `ResponseController` |
| 七项自动恢复测试中的一秒退避与固定负向等待 | 真实 Actor 与维护 timer 全部在各自 bubble 内运行；`MemberAgent.now` 的默认 `time.Now` 同步受控，推进后等待所有到期任务处理，再检查没有额外恢复 | 有限次数、重启后额度、暂停优先、旧 Session 未结清不替换、永久错误不重试；`TestAutomaticRecoveryRestoresEnabledMember` 继续使用真实时间验证定时接线 |
| `TestDSHRecoveryPreviewTimeoutAndIdentity` 的两秒 timeout | 每个场景独立 bubble，使用原生产超时；身份与数据错误分别精确检查 `ErrIdentity`、`ErrInvalid` | 实际 Actor 调用超时降级、已清理预览降级，身份／数据错误仍向上传播；真实 Store 和只读恢复记录检查保留 |
| 两个工具配置文件的 21 项夹具测试 | 复用 `parallelFixtureTest`，共用最多四个槽位；纯参数校验继续直接执行，不并行共享父夹具的子用例 | 原 Store、manager、账号队列、远端准备、ABA、撤权、未知提交及重启断言全部保留 |

时间 bubble 使用 Go 1.27 已有标准库，不新增生产模拟开关或通用 mock 层。其磁盘读写、同步与目录锁仍实际执行。工具夹具均拥有私有目录、manager、账号队列和 hook；`managerTestProfiles` 仅设置随机独立凭据键，未发现 `t.Setenv`、`t.Chdir` 或共享可变配置。CLI 的一次构建和 Lark 零间隔测试队列保持现有实现。

四槽 helper 增加 verbose 诊断：`fixture slot: wait=... held=...`。wait 从测试获准继续到取得槽位，held 包括夹具执行和清理；Go 自身的 `-parallel` 排队发生在此之前，未计入该 wait。并行用例的 `Elapsed` 不能相加当作套件耗时。

### T2 计时与验证

环境仍为 macOS ARM64、Go 1.27.1。主对照均执行同一组 51 个顶层测试、相同 `-count=1` 和四分钟有界上限，保留构建缓存；两次主测量顺序执行。命令与完整选择保存在机器可读证据的 `t2` 中，不新增永久测试入口或清单。

| 同条件范围 | 改造前 | 改造后 |
| --- | --- | --- |
| 51 项选择的命令墙钟，包含编译／启动 | 141.820 秒 | 60.795 秒，下降约 57% |
| 同一选择的 Room 包执行时间 | 139.575 秒 | 52.493 秒 |
| 两组工具夹具从首次执行／继续至全部结束的区间 | 79.475 秒 | 26.234 秒 |
| `TestUIUpdatesHTTPHeartbeat` | 15.10 秒 | 0.11 秒 |
| `TestUIUpdatesBlockedWriterCancellation` | 5.86 秒 | 0.74 秒 |
| 自动恢复文件，串行顶层耗时合计 | 18.67 秒 | 5.92 秒 |
| `TestDSHRecoveryPreviewTimeoutAndIdentity` | 2.55 秒 | 0.43 秒 |

两次选择均为 51 项顶层通过、零失败、零跳过。改造后 21 个工具夹具的槽位等待中位数 5.57 秒、最大 8.21 秒；持有槽位中位数 3.18 秒、最大 9.38 秒。持有时间合计因并行资源竞争高于原串行执行，收益以执行区间和包墙钟判断。主对照后进一步补强了 heartbeat 的普通写超时接线，最终源码由后续单并发及 race 检查验证。

| 最终源码检查 | 结果 |
| --- | --- |
| 同一 51 项选择，`-parallel=1 -count=1` | 全部通过；含子用例 127 项，零失败、零跳过；Room 98.596 秒、墙钟 109.176 秒 |
| 同一选择加全部 `TestUIUpdates.*`，`-race -tags=integration -count=1` | 59 项顶层、含子用例 138 项全部通过，零失败、零跳过；Room 61.753 秒、墙钟 84.313 秒 |
| `go vet ./...` | 通过 |
| `make test-e2e-check` | 全仓编译通过；不代表真实 e2e 验收 |
| `git diff --check` | 通过 |

单并发和 race 的耗时用于记录检查成本，不与主对照混作性能样本。主对照、单并发、race 的选择和测试数量均经 JSON 结果核对。

### 剩余成本与交付边界

输入升级组另做了两种隔离并行试验：包执行从 19.268 秒变为 15.470／16.845 秒，重编译使命令墙钟从 21.114 秒变为 24.116／24.848 秒；后者不能视为实际执行回退。发布中断矩阵的单项耗时因真实落盘竞争明显变长，试验不足以证明稳定的套件收益，本轮未推广，最终保留原串行夹具和全部真实发布／恢复边界。这些探索样本不计入上述主对照。

仍偏重的完整组合留给 T3 分析：工具配置仍逐分支装配 manager、执行原生 CLI；`TestPlatformAuthorizationSurvivesModelRotationAndPause` 本轮分别为 12.26／13.57 秒，覆盖跨轮换授权、暂停及原操作投递，需要保留这些边界后再降低装配成本。真实输入升级的各持久发布点不能合并成单个最终成功测试。

本轮没有重跑全仓核心、完整 `make test-integration`、全仓 race、浏览器或真实模型／平台验收。**核心三分钟预算尚未重新测量，T1 完整集成门槛仍未补齐**；上述约 57% 仅适用于所选慢组。后续完成相关分层后，按方案安排一次完整集成及最终全仓检查，不把局部结果升级为完整套件验收。

## T3：业务规则下沉与组合检查收缩

生产变更仅提取现有决策，由原路径直接调用：`larkToolGrantPlan`／`strictLarkGrantReduction`、`normalizePlatformMemberInput` 与 provider 的 `lark.ToolAuthorizationRequired`。账号准入、owner 生命周期、远端准备后的复核、授权与持久化顺序继续由原 owner 执行；没有引入模拟 CLI 开关或另一套授权实现。

### 断言承接

| 原检查／断言 | 下层承接 | 上层保留边界 |
| --- | --- | --- |
| `TestTodosScansPendingDoneAndNeverUsesAnIDCursor` | `internal/platform/gitlab/todos_poll_test.go` 同名测试：pending/done、低 ID、已知 ID、基线及不重导入 | `TestTodosTCPHTTPConfigureSyncDisableAndInboxDelivery` 与个人账号／Workspace 独立性检查继续证明真实来源、查询与投递接线 |
| `TestTodosScanBudgetReportsIncompleteAndKeepsCheckpoint` | provider 同名测试穷举页数、字节、时间预算，检查 yield、不早查、继续 page 2 与最终不漏页 | `TestTodosBudgetCheckpointPersistsAndSyncReconciles` 关闭并重开真实 Store，检查已提交 checkpoint；人工 sync 从 pending page 1 重扫、去重及 Room 激活立即查询 |
| `TestTodosCapacityRejectsWholePage`、`TestTodosSkippedObjectsAreCountedWithoutDelivery` | provider 同名测试承接容量拒绝、过滤前历史、跳过原因与重复扫描不重复计数 | 容量检查保留真实整页不部分落盘；跳过对象保留实际 notification view 的统计投影 |
| `TestTodosCurrentIntervalAndFixedInactiveInterval`、`TestTodosQuickPollsBoundRequestsAndFullReconciliationFindsDeeperItems` | provider 同名测试使用内存快照和受控时间验证完整时间窗、quick 两页上限、full 对账与深页低 ID | 上述 checkpoint 集成检查承接激活和人工 sync；TCP 集成继续承接一次实际 sync 与 Inbox 不重复投递 |
| `TestTodosRetryAfterAndAuthenticationPause` 的 401/403/429/500 矩阵 | provider 同名测试：分类、准确 RetryAt／ServerNotBefore、到期前不查、到期后重试或保持暂停 | Room 保留 401 代表流程：人工 sync 恢复查询准入，同时不能绕过服务端 Retry-After；凭据轮换、身份变化仍有独立集成 |
| 分页失败、ack、候选冲突、取消及提交失败的规则 | `TestTodosPageFailureRescansMovingOffsets`、`TestTodosCandidatesHonorAcknowledgementsConflictsAndCancellation` | 原真实分页失败重启、并发 ack／元数据变化、Save 失败、未知提交冻结、丢失回执和重启防重检查全部保留 |
| 工具默认／显式动作、保留较宽意图、严格减权、排序与版本匹配 | `TestLarkToolGrantPlan`、`TestToolBindingRevisionMatchesExactTarget`；入口与账号解析继续由 `internal/app/platforms` 原测试承接 | HTTP/member/onboarding 正常接线、自聊 proof、实际扩权／保留结果与版本、CLI、provider 差异、准备后撤权、ABA、其他成员并发、未知提交及重启检查继续保留 |
| 工具权限缺口的 read/send 矩阵、reply 不依赖 target scopes | `lark.TestToolAuthorizationRequiredByIntentAndPlatformSupport` 与原 `TestPermissionCapabilitiesDegradeOnlyAffectedBranches` | `TestLarkToolConfigurationUserAndApplicationScopeAdmission` 保留 read 缺口的 member/HTTP 拒绝、无落盘／无 operation 占位及 app 缺口部分意图保存 |
| 独立 `TestToolDefaultActionIntentSurvivesOmittedJSONFields` | `TestPlatformMemberIntentNormalizationRetainsObservation`：动作排序去重、nil/空数组 JSON 回读一致、显式零观察与省略不同 | 合并到 `TestLarkToolConfigurationEntrypointsAgreeAndSelfProofIsRequired/member`：空数组配置后，省略 actions 仍读回同一真实持久回执；另一意图测试继续拒绝 operation ID 下删除版本观察 |
| `TestLarkToolConfigurationExplicitActionsAndPreservedIntent` 的重复默认扩权／重复保留 | grant plan 与默认动作原有轻量测试承接组合；不重复运行两次原生 CLI 核验 | 保留历史 read-only、显式单动作、实际扩权与子集请求不减权的真实保存流程；规范化旧 grants 的版本变更集成继续保留 |
| `TestAgentJSONInputManagedActionReplayAndLimits` 的 inline/file/stdin 乘积 | 原 `cli_json_input_test.go` 保留所有输入模式一致、错误及不自动重试；新增 `TestReferenceActionRequestSizeLimits`、`TestAgentActionEnvelopeSizeLimits` 与 `gitlab.TestReferenceActionInputSizeBoundaries` 覆盖生产校验的临界值、UTF-8 字节与序列化预算 | object/discussion 各保留真实成功、相同请求重放只 POST 一次、业务超限与 HTTP envelope 超限均无副作用；只用 stdin 承接接线 |
| `TestTodosAppHandler` 与 `TestTodosAppHTTP` 重复完整 UI 业务 | `TestAppHandlerBridge` + `web_handler_bridge.mjs`：无需 manager，检查 GET/POST、Unicode/查询与正文编码、响应 ID 关联、错误 code/status/message | `TestTodosAppHTTP` 继续运行完整 `web_todos_http.mjs` TCP 流程；原故意断言失败后的 manager 清理回归保留 |
| 日历完整 manager 组合 | 既有 `internal/calendar` recurrence/default/DST/日期及离线规则测试继续穷举 | 两个 `TestCalendarManaged*` 原样归入 `calendar_lifecycle_integration_test.go`，保留真实重建、暂停、后台恢复、跨会话隔离和唯一发布；helper 留在默认层供原浏览器检查编译 |

Todos 轻量 host 每次读取克隆快照，提交时检查上下文、状态／scope revision、订阅身份、schema 和整页容量；只在提交成功后安装候选。测试包含 fetch 期间 ack、最后读取后 ack 造成的候选冲突、scope 变化、取消和保存拒绝，不将“立即成功”的假提交当作原子性证明。真实磁盘同步、未知提交与回执证据仍由上层负责。未修改 `input_upgrade_test.go` 的持久发布中断点。

### T3 定向计时与检查

环境同前；T3 源码 HEAD 为 `3c1532541d8e07e311bd4af7fd39430ceba206d5` 加当前工作树。计时期间另一项安装器提交使 HEAD 从 `b136f0e` 前进到 `3c15325`，只涉及 `packaging/install.sh` 和 `scripts/package-install.test.mjs`，与本次后端选择无关。保留构建缓存；主对照顺序运行，均使用 `-count=1 -tags=integration -timeout=4m`。改造前选择四个切片的 55 项 Room 顶层测试，改造后覆盖保留的 Room 检查和新增下层检查，共 66 项；选择由源码函数名生成，实际执行的包／顶层名已逐项记录到机器可读证据。两侧都包含真实日历组合，不能把移到 integration 的时间当作收益。

| 相同业务范围的总成本 | 改造前 | 改造后 |
| --- | --- | --- |
| 命令墙钟，含构建／启动与所有新增下层检查 | 47.492 秒 | 40.117 秒，下降约 16% |
| Room 包执行时间 | 35.963 秒 | 29.024 秒，下降约 19% |
| 新增 provider 检查所在包的执行时间 | 无新增项 | GitLab 0.378 秒、Lark 1.024 秒，已纳入上行墙钟 |
| 顶层结果／含子用例结果 | 55／114 通过 | 66／187 通过；两侧均零失败、零跳过 |

四槽夹具共享同一次批处理的排队，不能相加并行 `Elapsed` 当作墙钟。按各 Room 切片第一次实际 run/cont 至最后通过的观察窗口，Todos 为 19.750 → 23.415 秒，工具为 31.947 → 24.585 秒，CLI 为 1.659 → 2.692 秒，UI 为 20.558 → 9.875 秒，日历为 3.541 → 3.587 秒。这些窗口相互重叠、包含槽位等待及其他切片的资源竞争；provider 新增成本已包含在合并命令内。它们用于呈现各组实际观察，不作为独立基准或可累加收益；本轮只对合并范围报告约 16% 的墙钟收益，不宣称 Todos/CLI 或单纯标签迁移各自提速。

| 检查 | 结果 |
| --- | --- |
| 改造后定向组件／集成 | 66 项顶层、187 项含子用例通过；墙钟 40.117 秒 |
| 同一选择 + Todos 生命周期／渠道隔离 + `internal/app/platforms` 入口解析，`-race -tags=integration` | 75 项顶层、233 项含子用例全部通过，零失败、零跳过；墙钟 73.428 秒 |
| `internal/platform/gitlab`、`internal/platform/lark`、`internal/app/platforms`、`internal/calendar` 默认测试 | 全部通过；命令墙钟 13.130 秒，新单元检查无需 Room/Workspace/CLI 凭据装配 |
| 全仓默认、integration、e2e 编译 | 全部通过；e2e 仅编译，不代表真实依赖验收 |
| `go vet ./...` | 通过 |
| 最后补强的断言，定向 race | Todos 初始订阅身份对照通过，GitLab 1.569 秒；CLI 明确检查两类超限诊断且 stdout 为空，Room 4.038 秒。补强发生在完整集成构建后，生产代码未变；CLI 补验墙钟 236.591 秒，主要消耗于当时负载下的重新编译，不作为性能样本 |

### 完整本地集成尝试与剩余门槛

本轮只尝试一次 `go test -json -tags=integration -count=1 -timeout=12m ./...`，墙钟 **630.662 秒（10 分 31 秒）后主动中止**。Room 包已执行约 605 秒；中止前顶层 740 通过、7 跳过、1 失败，含子用例 2,150 通过、8 跳过、1 失败。仍有未结束或未执行项，不能当作完整通过，也不能把已执行项的耗时当作健康完整基线。

失败为 `TestDSHRecoveryPrioritizesOnlySavedMappingsBeforePageLimit`：`dsh_recovery_context_test.go:145` 的 `readReferenceInbox(h.ctx, ...)` 返回 `context deadline exceeded`，该用例使用原 `newInboxHarness` 的十秒上下文。本轮没有修改此用例或其夹具，也尚未走到要验证的分页优先级断言；没有放宽 deadline、删除断言或增加重试。它仍需单独诊断，不能仅因本次改造组通过而归为无关噪声。

全量期间观察到本机另有独立的 `teamx service upgrade` 在运行，可用磁盘从约 5.7 GiB 降至 4.6 GiB，原日历／本地 Git 等用例显著慢于定向测量；这些是负载观察，**没有证明其就是失败原因**。确认已有失败且进度明显偏慢后，仅中止本轮 full-suite 进程树，没有操作独立升级或运行服务。未再次重建整包复跑该失败，也未提高上限重跑全量。

12 分钟是本轮显式诊断上限，没有修改 Makefile，不将它算作提速。**T1 完整集成门槛仍未补齐**。T3 的实施、定向组件／集成、race 与标签编译已完成；全仓健康通过仍待上述失败诊断后验收。未另跑全仓核心，三分钟核心预算仍无本轮独立测量；T4 浏览器分层和最终全仓 race 留待后续。未启动真实平台／模型或 Chromium 验收，未进行部署。


## T4：浏览器功能与专项测量分层

改动只涉及测试、标签、执行注释和文档。Makefile 继续保留原来的核心／integration／race／e2e 编译含义、结果缓存和超时；package／release CI 未改动。没有新增构建标签、测试选择框架或永久测试名清单，也未修改生产时间、授权、存储和关闭行为。

### 断言承接与选择

| 原检查／断言 | 普通功能承接 | 独立专项／保留边界 |
| --- | --- | --- |
| `TestWebUpdatesApplicationBrowser` 两种服务各静置 60 秒 | 原名继续验证原生 SSE、消息 HTTP 补读、后台恢复、设置切换复用连接、草稿／焦点和不重放命令 | `TestWebUpdatesIdleBrowser` + `TEAMX_WEB_UPDATES_IDLE=1`；原 60 秒、零额外读取、单连接与至少三次真实生产心跳全部保留 |
| `TestWebUpdatesNotificationScopeBrowser` 的 60 秒采集窗口及双端矩阵 | 原名等待实际诊断帧和采集轮次，保留两端交换诊断面板、限频读取、错误／恢复、同／不同会话、共享阅读合并、草稿、后台恢复与单端退出 | `TestWebUpdatesNotificationScopeLongWindowBrowser` + `TEAMX_WEB_UPDATES_SCOPE_LONG=1`；原 60 秒、至少 55 轮、各 provider 至少五次查询、每端至少 20 个诊断帧及读取／字节／DOM 延迟保留。旧轮询 scope baseline 只允许搭配此入口 |
| `TestHumanReadBrowser/history*` 的满容量加载与性能 | 新 `history-functional` 使用 600 条，保留正常 50 条起点、第三批失败保留 150 条前缀、真实读取屏障后的取消、迟到响应失效、接续、锚点、去重、清理一条后实时追加及原节点保持 | `TestHumanReadCapacityBrowser` + `TEAMX_HUMAN_READ_CAPACITY=1`；原 `history`／`history-large` 的 10,000 条、6,600 字节普通正文、Long Tasks／CDP 内存／输入／取消／批次／阅读帧判定不缩小 |
| `combined-320` 再次运行完整提及业务 | 1440／390 各保留完整业务；`reading` 与 `TestHumanReadCatalogBrowser` 继续覆盖 320／390 可见裁剪、短视口和 Chromium 原生触摸、长按与取消 | 不把小屏或 CDP 触摸当作实体手机键盘、系统后台或部署代理验收 |
| 默认层或 integration 中仅靠环境变量跳过的实际 Chromium／容量测试 | 本地 HTTP／CLI 与共享 helper 继续按原层编译；选择流程不再查看浏览器环境变量 | 10 个已有浏览器／容量文件改为 `e2e`，新增 e2e-only 选择入口。`TestPlatformSelectionConversationBrowser`／`TestDefaultAssistantPlatformOnboardingBrowser` 复用原 CLI 后置检查，普通集成继续走 CLI 选择 |

前端受控时钟检查继续承接调度、限频和静置规则；本机真实窗口才作为心跳／持续采集证据。历史 frozen／FIFO／四工具／两工具、旧轮询配对测量、真实依赖入口及既有失败、回执、防重记录均未删除，也未将待验能力标为完成。

首次 Chromium 功能选择暴露三项脚本等待问题，原失败日志保留在机器可读证据中：目录布局切换可能命中上一会话的同序号节点，改为等待目标 Room 的实际 `/v1/read`；重启前关闭详情尚未完成焦点恢复，改为等待侧栏关闭；短采集窗口可能先收到诊断帧、后完成轮次计数，改为有界等待实际计数。没有放宽 DOM、焦点或采集断言，三项随后定向复验。


### T4 定向验证与预算

环境为 macOS ARM64、Go 1.27.1、Node v26.7.0、Playwright 1.59.1、Chromium 147.0.7727.15。Node 按已有父目录 `node_modules` 解析 Playwright，先于 `NODE_PATH`；本轮未安装或修改依赖。先构建 e2e 测试二进制，再通过 `go tool test2json` 复用，子场景仍使用当前磁盘上的 Node 脚本；源码未变的通过项没有反复重建、重跑。

普通功能开关下的八个顶层场景均已有最终通过证据：阅读功能 16.27 秒、目录与触摸 13.65 秒、原生订阅 1.25 秒、双服务页面 15.12 秒、模型回执 1.54 秒、认证恢复 0.64 秒、重启恢复 6.33 秒、活动采集隔离 34.64 秒。普通阅读日志只有两个 combined 尺寸、reading、locating、600 条 history-functional；没有 capacity 场景。采集普通观察窗为 811ms、一轮实际采集，两种 provider 各新增一次查询；这只是功能证据，不能代表 60 秒负载结论。

以上来自首次选择加三项修正后的定向复验，**不是一次干净完整功能集的墙钟，也不相加宣称套件提速比例**。后续普通阅读组、SSE／恢复组及活动采集组各以一分钟为初始反馈预算，组合入口以三分钟为规划上限；需同条件完整测量后再判断组合预算达成。原三段 60 秒窗口与两组满容量场景已经独立选择，未通过缩短它们的窗口或容量换取功能集结果。

默认、integration 全仓编译、`make test-e2e-check`、标准 `go vet ./...` 和 Web 767 项检查均通过；构建清单确认迁移文件在默认／integration 中排除、在 e2e 中编入。e2e 编译只证明标签和 helper 接线，不代替 Chromium 或真实依赖验收。


专项以独立开关单独运行，三个顶层及全部子场景通过、零失败、零跳过，命令墙钟 276.709 秒，包含原约三分钟真实等待与容量加载：

| 专项 | 本轮结果 |
| --- | --- |
| `TestHumanReadCapacityBrowser` | 82.39 秒；两组分别 31.11／51.28 秒，均实际构建 10,000 条 DOM。取消 90／101ms、最大输入响应 231／321ms、最大批次构建提交 19.7／172.9ms、阅读帧 p95 4.3／4.2ms，原阈值全部通过 |
| `TestWebUpdatesIdleBrowser` | 127.62 秒；单 Room／manager 各真实 60 秒零额外读取、一个原生连接、至少三次生产心跳 |
| `TestWebUpdatesNotificationScopeLongWindowBrowser` | 66.62 秒；观察窗 60,010ms、实际 60 轮、GitLab／Lark 查询增量 60／10，两端各 70 个 diagnostics 帧、均零显示 HTTP 读取，消息安装至 DOM 190ms |

大正文样本的 CDP embedder heap 峰值约 1.97GB，估算值并非进程 RSS，也不能和其他 heap 数相加；专项通过不能改述为低内存承诺。完整数值、命令、最终通过项与首轮失败信息见机器可读证据的 `t4`，本机原始日志位于 `/tmp/teamx-t4/`，临时目录不作为永久 gate。本轮没有重跑旧轮询／frozen 等历史配对或真实模型重复采样，不生成新的配对性能结论。


新增两个 Chromium 选择入口通过，合计 16.465 秒（含本地夹具使用的生产 CLI 一次构建）；原 CLI 选择／默认助理 wait、inbox、human 接线的定向 integration race 通过，两个顶层、含子用例五项，包执行 14.088 秒。该 race 命令故意保留 `TEAMX_SELECTION_BROWSER=1`，integration 路径仍只执行 CLI，不再因环境变量隐式启动 Chromium。这两组用于接线验收，不作性能配对样本。

**T4 的分层、入口、文档及定向验收完成，整体测试改造仍未完成全仓门槛。** 本轮未重新执行完整核心／本地集成或全仓 race；T3 记录的 deadline 失败、完整集成健康度、核心三分钟预算及最终全仓 race 仍待补齐。未运行真实 DSH／平台、实体手机或部署代理验收，也未重启运行实例或部署；这些继续按各能力的既有目标和授权独立执行。


## 收口时的全仓测量与提交边界

2026-11-20，用户授权补齐测量后，独立复跑 T3 的 `TestDSHRecoveryPrioritizesOnlySavedMappingsBeforePageLimit`，原十秒上下文及所有断言未变，测试本体 3.54 秒通过。随后它在完整核心中以 2.30 秒通过。本轮未复现旧 deadline，不能据此断言旧失败的原因或删除原始失败记录，也没有放宽超时或增加重试。

| 本轮入口 | 结果与实际成本 |
| --- | --- |
| `go test -json -count=1 -timeout=12m ./...` | **全仓通过，墙钟 453.328 秒（7 分 33 秒）**；Room 440.540 秒。顶层 1,365 通过、5 跳过；含子用例 3,294 通过、6 跳过，零失败。跳过均沿既有显式环境／容量测量／子进程 helper 条件 |
| `go test -json -tags=integration -count=1 -timeout=15m ./...` | **按用户明确要求中止**，墙钟 80.223 秒，不是完整通过或健康耗时；SIGINT 仅发送给本次进程组 |
| 最终全仓 race | 用户要求停止长跑并先提交，未启动；既有定向 race 证据继续保留 |

相对 T1 修复后完整核心的 557.748 秒，当前观察墙钟减少 **104.420 秒，约 18.7%**，仍未达到三分钟目标；不能把 T2 局部 57% 的收益外推至全仓。两次都保留构建缓存、使用 `-count=1`，本机及 Go／Node 版本相同。当前套件还包含并行进行的服务启动改动，计时中观察到独立 Service 编译／测试重叠，因此这属于实际工作负载下的全仓观察，不是严格隔离的因果性能实验。测量前后已有源码内容未变；并行安装／服务工作之后独立提交为 `9ae6bca`，不纳入本次测试改造提交。

核心仍以 Room 为主要成本。已完成慢项包括跨模型轮换授权约 13.85 秒、输入升级的所有发布中断点约 8.15 秒；四槽工具夹具的顶层时间包含排队，不能相加作为墙钟。没有为追求预算削弱授权、真实落盘、未知提交、重启或生命周期断言。

用户随后明确要求不要反复运行、先提交 T1–T4，故到此停止测试。完整集成健康耗时、八分钟预算及最终全仓 race 仍待验；e2e 继续区分已通过的编译／本地浏览器专项与未执行的真实依赖，不将按需场景计时冒充全量 e2e。完整命令、计数、跳过及慢项见机器可读证据的 `final_acceptance`。
