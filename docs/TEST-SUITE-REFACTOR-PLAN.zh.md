# 测试分层与执行效率改造方案

状态：**T1 修复及 T2–T4 实施与各自定向验证完成；§9.2 前四批已实施，第四批同范围切片墙钟中位数约缩短 24.2%；最新完整核心＋本地集成 15 分 49 秒（命令墙钟）通过、八分钟预算未达；最近一次独立全仓核心 7 分 33 秒通过、三分钟预算未达；全仓 race 未运行**。T2 使用标准库受控时间及现有四槽夹具提速；T3 完成 Todos／工具配置规则下沉、CLI/UI 组合收缩及日历集成分层；T4 完成浏览器功能／长窗口／满容量入口拆分、e2e 标签统一和执行文档，真实 Chromium 功能及专项均定向通过。T1–T4 覆盖映射、验证和同条件计时见[实施与耗时记录](implementation/test-suite-health.zh.md)，后续第一批见[并行记录](implementation/room-fixture-parallel.zh.md)，第二批见[授权与工具矩阵记录](implementation/authorization-tool-matrix.zh.md)，第三批见[Lark 协议矩阵记录](implementation/lark-protocol-matrix.zh.md)，第四批见[定时等待与装配记录](implementation/timer-assembly.zh.md)。T4 最终核心墙钟较 T1 缩短约 18.7%；本轮未重测独立全仓核心，不外推局部提速；完整集成健康门槛已补齐，性能预算仍未达。后续优化顺序与慢项全表见 §9 和附录 B，前四批已实施，其余仍待实施。下文的原始测量和分阶段要求作为后续对照保留。

## 1. 目标与范围

将业务分支放在依赖最少的层级验证，集成测试负责真实组件之间的连接与生命周期，e2e 负责浏览器、外部工具、平台或模型特有的风险。保持现有功能和关键失败边界的覆盖，同时缩短日常反馈时间。

本次推荐按 T1 → T2 → T3 → T4 实施，每阶段可以独立评审和交付：

| 阶段 | 主要成果 | 收益与投入 |
| --- | --- | --- |
| T1：恢复测试健康度 | 修复清理死锁、排查已复现失败，形成能够正常结束的基线 | 最高优先级，先消除失败后等待整包超时的成本；按问题族分小提交 |
| T2：缩短默认核心回归 | 受控时间、明确事件屏障、隔离夹具有界并行 | 针对已测得的慢项，主要影响默认层及所有包含它的入口 |
| T3：业务规则下沉、集成矩阵收缩 | Todos 轻量轮询测试、工具配置决策测试、薄 CLI/UI 组合检查 | 中等投入，减少每个业务分支反复装配服务、落盘和启动进程 |
| T4：浏览器分层与执行约定收敛 | 快速浏览器功能集、独立专项测量、明确的运行入口和文档 | 控制按需验收成本，并避免日常重复全跑 |

不以测试数量、文件数量或覆盖率百分比下降作为成果。每项删除或收缩都必须说明：原来保护什么、由哪项测试承接、仍有哪些独有边界需要保留。

本次不进行 Room 全面拆包、引入通用测试框架、建立测试选择平台，也不改变正式存储耐久性、授权策略或关闭语义。真实平台写入、运行实例重启和部署不在本方案的默认实施范围内。

## 2. 已核实的基线

测量日期：2026-11-20；源码 HEAD：`b136f0edf5d87103423ba77b830a700aab0dc5ed`，包含当时工作树。环境为 macOS ARM64、Go 1.27.1。两组主测量顺序执行，保留构建缓存，以 `-count=1` 排除测试结果缓存。

| 范围 | 实际结果 | 解读 |
| --- | --- | --- |
| `go test -json -count=1 -timeout=8m ./...` | 墙钟 494.957 秒，通过；Room 包 481.101 秒 | 默认层的正常执行本身已经偏重 |
| 仅 `integration` 新增的 543 个顶层测试，排除核心测试名 | 墙钟 490.772 秒；515 通过、15 跳过、11 失败、2 项未结束；Room 达到 8 分钟包级超时 | 存在清理死锁，不能视为健康集成套件的性能基线，也不是完整 `make test-integration` 的结果 |
| 11 个失败项以 `-parallel=1` 复跑 | 全部仍失败，墙钟 101.504 秒 | 不能仅归因于本轮并发负载；仍须逐项诊断 |
| 单独运行 `TestTodosAppHandler`，包上限 15 秒 | 再次超时，栈停在清理阶段等待 manager 锁 | 已独立复现测试清理死锁 |

补充观察：

- Room 默认层的顶层测试从 11 月 8 日的 269 个增至 907 个；integration 从 322 个增至 493 个。数量增长说明工作量变化，不直接说明测试多余。
- 默认层只有 12 个顶层测试显式使用并行；串行顶层测试报告耗时合计约 466 秒。`tools_target_concurrency_test.go` 与 `tools_lark_configuration_test.go` 两组约占 70 秒。
- `TestUIUpdatesHTTPHeartbeat` 单项约 15.1 秒；自动恢复相关测试存在多次超过一秒的固定等待。
- 失败项复跑中，`TestPolicyManagementHTTP` 约 60 秒后被杀；`TestRuntimeBackgroundNotificationsIgnoreAnotherRoomsOperation` 约 30 秒后失败。失败等待也占用显著时间，其业务原因尚未全部确定。
- 浏览器代码中，更新功能的单 Room／manager 两种模式各静置 60 秒，通知范围检查另静置 60 秒。正常执行这些场景，仅这三次静置就需至少 180 秒。此次未执行浏览器或真实模型／平台 e2e。

原始本机证据位于 `/tmp/teamx-test-audit.LkxgSw/`，汇总为 `audit-summary.json`。该目录可能被清理，本文已保留实施所需的关键结论和失败清单；实施时需核对当前源码并生成新的证据，不能依赖这个临时目录作为长期 gate。

### 2.1 T1 实测后的后续执行约束（2026-11-20）

本轮全仓核心通过，但墙钟为 **557.748 秒（9 分 18 秒）**，其中 Room 543.508 秒；完整本地集成及差集补跑均在收口时中止，不能称为健康的完整集成基线。具体结果与慢项见[实施与耗时记录](implementation/test-suite-health.zh.md)。这说明修复失败后，正常测试的执行成本仍是主要问题，后续不能再用反复全跑来推进提速工作。

后续阶段按以下约束执行；本节替代 §4.3、§8 中原先要求先完成两轮全量基线的执行顺序，不取消最终覆盖门槛：

- **先提速，再补全量。** T2 可基于已通过的核心与定向证据启动，不以重新跑完 T1 完整集成为前置条件。优先处理两组串行工具配置测试（本轮合计 76.90 秒）、15.08 秒的心跳检查和自动恢复等待；T3 再承接仍需完整服务装配的慢组合。
- **开发反馈保持局部。** 每个切片只跑受影响检查及必要的定向 race；耗时比较限于改造组，已有证据足够且代码未变时不追加同范围复跑。重复定位同一问题时，源码、构建标签与 race 配置未变可复用现有测试二进制；源码改变后仍须重新构建，不引入另一套运行框架。
- **完整入口避免重复。** 阶段性全量以一次 `make test-integration` 覆盖核心和本地集成，不再默认先完整跑核心。核心入口的独立计时只在需要评价其预算时安排；全仓 race 留给最终交付，阶段中仅在影响范围确有必要时扩大。
- **长跑前交代成本。** 运行前说明范围、基于现有测量的预计耗时及本次要补充的证据；超过预期时查看当前用例、失败或清理栈，不能仅反复提高 timeout。提速前再次完整核心需按约 9 分钟量级估计，不能仍按 Makefile 的 3 分钟默认上限预期。
- **保留未完成项。** T1 完整集成仍待验收，完成相关提速后用一次完整集成补齐；中止、差集补跑或复用核心结果均不能冒充合并入口的完整通过和独立耗时。不得用删除正确断言、增加跳过或降低持久性换取速度。

## 3. 目标分层与覆盖边界

| 层级 | 应承担的验证 | 依赖与执行方式 |
| --- | --- | --- |
| 单元测试 | 解析、过滤、权限决策、版本／身份匹配、状态转换、分页／预算／退避算法 | 优先位于实际所属包；使用最小状态、受控时钟和轻量 host，必要时使用临时文件 |
| 核心组件测试 | Actor 消息、Store／writer 提交边界、组件内取消／结清和故障隔离 | 默认层保留；允许真实临时目录及有界组件装配，避免无关 manager／CLI／浏览器 |
| 本地集成测试 | CLI／HTTP 接线、真实进程协议、服务重建、持久恢复、跨组件准入与结清 | `integration || e2e`；保留能证明组件协作的代表流程和独有故障点 |
| 浏览器功能回归 | 实际 DOM、原生 EventSource、焦点／滚动／触摸、页面恢复与后台生命周期 | `e2e` 并显式启用；覆盖正常路径与关键异常，控制业务矩阵和样本规模 |
| 专项验收／测量 | 长时间静置、满容量、性能对照、真实模型／平台／已安装工具 | 独立显式选择，不混入日常核心或普通浏览器功能集 |

单元测试是默认核心层的一部分，但默认核心层不要求全部变成纯函数测试。文件名或是否使用 `httptest`、临时目录，不能单独决定分层。

下沉遵循以下约束：

1. **降低依赖，而非仅移动构建标签。** 原来启动完整服务的测试，移入普通 `_test.go` 后仍启动完整服务，不算完成下沉。
2. 同一分支优先在最低有效层穷举，上层验证接线和该层独有的行为。跨维度组合只有在维度之间存在实际相互作用时保留完整矩阵。
3. 最小注入点只服务于已有职责：优先复用时钟、Fetcher、StepHost、受控 backend 等能力；不得给正式 CLI 增加模拟开关，或建立覆盖全部业务的通用 mock 层。
4. 保留真实落盘测试中的文件同步、目录同步、未知提交、目录锁和重启证据。不得用关闭 `fsync` 或共享可变 Store 来换取提速。
5. 保留精确账号／成员／目标／版本校验、丢失应答后的防重、撤权后复核、取消与关闭结清、故障域隔离。纯状态模拟不能代替这些行为的真实组件证明。

每次收缩在评审说明中记录简短映射：`原测试／断言 → 下层承接测试 → 上层保留边界`。仅维护本次改动的映射，不额外建立永久的全仓用例登记系统。

## 4. T1：修复失败与清理路径，恢复健康基线

### 4.1 先修复确定的清理死锁

入口：[web_todos_test.go](../internal/room/web_todos_test.go) 的 `checkTodosAppResult`，调用者为 [web_todos_integration_test.go](../internal/room/web_todos_integration_test.go) 中两个 UI 测试。

当前路径为：`m.mu.Lock → 断言 t.Fatal → 未执行 Unlock → testManager 清理调用 manager.close → shutdown 等待 m.mu`。

改造要求：

- 在短临界区读取并复制断言所需的状态，解锁后再断言；复制内容不能保留会被后台任务修改的引用。
- 必须在锁内使用的短 helper 以函数级 `defer` 保证解锁，不能为了补一个 `defer` 而把锁扩大到后续服务读取、等待或关闭流程。
- 检查本次涉及的同类断言 helper、阻塞 hook、Node 子进程和 HTTP 服务的清理路径。阻塞测试先注册释放措施，再启动工作；失败和取消时也能结清。
- 增加一个有界的失败路径回归，证明断言失败会返回非零且清理能完成。可用现有测试二进制的 helper 子进程隔离预期失败，不以“等整包 timeout”作为成功条件。

修复解锁后，原业务断言仍须排查；“不再挂死”与“功能测试通过”分开验收。

### 4.2 逐项处理已复现失败

完整清单见附录 A。每项按“独立复现 → 对照当前生产合同 → 确定原因 → 最小修复 → 定向复验”处理：

- 已确认的合同过期示例：`verifyMigratedSessionTools` 仍要求 tools/list 恰好只有两个 Inbox 工具，实际已有 History 工具。应对照当前注册逻辑校准合同，将完整工具集合／Schema 分支放在合同测试；升级集成继续验证新合同被采用、执行绑定有效、迁移后的输入可用。
- Policy HTTP 的 60 秒退出和后台通知的 30 秒失败，需分别定位子进程、请求或生命周期等待在哪一步停住，补充关键阶段诊断；不能直接统一缩短等待后宣布解决。
- 账号替换、来源撤换、通知升级及目录维护等失败，尚不能统一判断为夹具问题。若暴露产品缺陷，独立修复该缺陷，保留对应回归，不放宽正确的身份或结清条件。
- 不通过删除、无条件跳过、接受任意错误或增加重试来消除失败。确已退出支持范围的旧场景，先核对当前合同和待验事项，再按覆盖映射移除。

### 4.3 验收与交付

- 两个 Todos UI 测试均能正常结束；故意触发断言失败时不会阻塞清理。
- 附录 A 每项均有明确处理结果，全部在保留覆盖的前提下通过；仍有未解释失败时，T1 不标记完成。
- 原计划在定向验证后分别测量核心与完整本地集成。本轮核心已通过，完整集成按用户要求中止；按 §2.1 先推进 T2 提速，再补一次完整集成验收，不将该待验门槛作为启动提速的前置长跑。确需诊断时显式使用有界上限并记录；不把提高上限算作优化。
- 本阶段不同时重排大批测试或收缩矩阵，便于判断失败修复与后续提速的关系。

## 5. T2：缩短默认核心测试

### 5.1 用受控时间与屏障替代真实等待

优先处理：

| 入口 | 改造方向 | 必须保留的验证 |
| --- | --- | --- |
| `ui_updates_http_test.go` 的心跳与阻塞 writer 检查 | 内部注入可驱动的 tick／定时依赖，保留默认生产间隔；验证实际 HTTP 帧和取消 | heartbeat 身份、撤权复核、阻塞输出中断、连接关闭 |
| `automatic_recovery_test.go` | 优先复用 `MemberAgent.now`，结合明确的 Actor 状态／事件屏障驱动恢复 | 恢复次数有限、暂停优先、旧 Session 未结清不得替换、永久错误不自动重试 |
| `dsh_recovery_context_test.go` 等超时用例 | 对测试目标使用有界、可控制的超时依赖；成功路径等待明确事件 | 超时降级与身份／数据错误的不同处理 |

仅替换 `Now()` 不一定能驱动 Actor 定时调度；测试必须明确“时间已推进”和“对应调度已处理”两个条件。优先复用已有机制；确需新增依赖时，限制在所属组件内部，保持正式时间参数及默认行为。

负向断言不能改成“此刻没有发生”。应证明相关任务已被处理／拒绝、下一次到期已被推进，或受控时间窗已经完整走过。仍保留少量真实定时器接线验证，避免只有模拟时间证明。

### 5.2 对安全隔离的夹具有界并行

优先审核 `tools_target_concurrency_test.go`、`tools_lark_configuration_test.go`，随后按健康基线的慢项选择下一组。复用现有 `parallelFixtureTest`，保持最多四个夹具槽位。

逐项确认私有 Store、manager、账号队列和 hook；检查所有辅助函数是否修改环境、当前目录或共享状态。有 `t.Setenv`／`t.Chdir` 或共享配置依赖的测试保持串行，不对共享父夹具的子用例批量增加 `t.Parallel()`。

继续复用已有测试二进制、`productionFixtureCLI` 的一次构建，以及独立的 Lark 零间隔测试队列。生产限流和测试中的队列／取消／容量保证保持原样，不重新建设这些已有优化。

### 5.3 验收与交付

- 受控时间用例稳定通过，目标分支无需真实等待生产的 1／5／15 秒间隔；真实定时器接线仍有覆盖。
- 改为并行的组分别在 `-parallel=1` 和正常并发下通过，相关并发、撤权、关闭测试通过定向 race。
- 对改造组做同条件前后计时，必要时采样三次；报告夹具等待与执行的差别，不累加并行用例的 `Elapsed` 作为套件耗时。
- 核心中仍需完整 manager 重建或跨服务组合的慢项列入 T3 拆分，不单纯把它们移到标签后以缩短默认测试。

## 6. T3：下沉规则，收缩本地集成矩阵

### 6.1 Todos 轮询规则作为首个完整切片

参照 [events_poll_test.go](../internal/platform/gitlab/events_poll_test.go) 的 `eventPollHarness`，在 `internal/platform/gitlab` 建立最小 Todos StepHost／Fetcher 夹具，使用内存快照与受控时间，无需 Room、Workspace、凭据存储或目录 manager。

从 [platform_notification_todos_integration_test.go](../internal/room/platform_notification_todos_integration_test.go) 拆出以下规则：

| 现有用例或行为 | 下层承接范围 | 集成层保留范围 |
| --- | --- | --- |
| `TestTodosScansPendingDoneAndNeverUsesAnIDCursor` | pending／done 扫描、低 ID／已知 ID 不错误提前结束 | 查询范围与真实通知来源接入 |
| `TestTodosScanBudgetReportsIncompleteAndKeepsCheckpoint` | 预算耗尽、yield 与 checkpoint 延续 | 提交后的实际恢复 |
| `TestTodosCapacityRejectsWholePage`、`TestTodosSkippedObjectsAreCountedWithoutDelivery` | 整页容量拒绝、过滤与统计 | 页面拒绝时不产生部分持久投递 |
| `TestTodosCurrentIntervalAndFixedInactiveInterval`、`TestTodosQuickPollsBoundRequestsAndFullReconciliationFindsDeeperItems` | 活跃／非活跃间隔、quick／full 请求边界、完整对账 | Room 活跃状态传递与一次实际 sync 接线 |
| `TestTodosRetryAfterAndAuthenticationPause` | 401／403／429／500 的分类、退避、服务端最早重试时间 | 凭据状态与人工 sync 的真实准入 |
| 分页失败、重开、并发 ack、提交错误相关混合用例 | 算法与候选快照规则 | 真实保存失败、未知提交冻结、丢失回执、防重和重启 |

先补齐下层承接并验证，再删除上层重复断言。混合测试按规则与持久行为拆分，不整段搬移或整段删除。轻量 host 应遵守真实提交／冲突／取消合同，避免因“立即成功”的假实现绕过实际顺序。

### 6.2 工具配置与权限矩阵

针对 T2 中仍偏重的工具配置组，将已有职责中的权限组合、目标匹配、意图规范化和版本决策放在最小有效层测试。纯入口／绑定解析优先复用 `internal/app/platforms`；provider 规则放所属 provider 包；应用准入和 owner 生命周期保留在原层。

各 HTTP／member／onboarding 入口保留一次正常接线及入口独有的身份拒绝。对同一通用决策，不再让每个入口都穷举完整远端 CLI 场景；provider 差异、准备后账号／绑定变化、ABA、权限撤回、未知提交和其他成员并发仍保留对应边界证明。

不为测试提速推进完整配置服务迁移，也不引入第二套授权逻辑。确需提取决策函数时，由生产路径实际调用；不能只测试一套独立的测试模型。

### 6.3 CLI 输入与 UI 组合

- [cli_json_input_test.go](../internal/room/cli_json_input_test.go) 已验证 inline／file／stdin 的请求一致性、无效输入和不自动重试。集成层的 `TestAgentJSONInputManagedActionReplayAndLimits` 收缩重复输入矩阵；业务尺寸限制在实际校验层穷举，集成保留成功执行、重放只执行一次、超限无副作用，以及 object／discussion 各自独有的分派。
- `TestTodosAppHandler` 与 `TestTodosAppHTTP` 当前运行同一 `web_todos_http.mjs` 完整流程。优先保留真实 TCP HTTP 的完整业务检查；stdio 路径保留最小桥接合同，不再次维护同样的业务矩阵。先确认普通桥接测试能承接编码、响应和错误传播，不能直接删掉传输边界覆盖。
- 日历重复规则、DST 和日期判断继续由 `internal/calendar` 穷举；manager 重建、后台恢复、跨会话隔离与唯一发布保留代表性集成流程。`calendar_lifecycle_test.go` 这类完整组合可以在完成下层承接后归入 integration，复用 helper 与顶层测试分开，避免破坏默认层编译。
- `input_upgrade_test.go` 的不同持久发布／恢复边界不是普通业务分支矩阵；保留真实中断点，不把多个提交阶段合并成只验证最终成功的一例。

### 6.4 验收与交付

- 每个切片都有具体断言承接映射，下层新增检查直接覆盖生产规则；上层独有边界仍可独立复验。
- 新增单元用例无需启动无关进程或完整 manager，相关包的默认测试可独立通过。
- 定向组件／集成检查与必要 race 通过；每个切片比较改造组的总成本，不能只报告移到另一层后的时间下降。
- 将核心与 integration 合起来检查，确认相关顶层测试和 helper 在默认、integration、e2e 标签下均可编译；迁移后没有因选择正则遗漏新测试。

## 7. T4：浏览器功能回归与专项测量分开

### 7.1 功能流程保留代表性，真实长窗口独立选择

| 场景 | 普通浏览器功能集 | 专项保留 |
| --- | --- | --- |
| SSE／页面刷新 | 原生 EventSource 接线、更新后补读、断开恢复、草稿与焦点、后台切换、独立会话隔离 | 真实 60 秒无多余读取、持续采集期间诊断帧／请求数、长窗口对照 |
| 会话阅读与提及 | 足以跨页及触发补载的样本，检查提及确认、取消／失败恢复、迟到响应失效和滚动位置 | 10,000 条、接近 64 MiB 正文、Long Tasks／内存／批次耗时 |
| 桌面与移动布局 | 桌面和代表性移动端各保留完整关键流程；320／390 等其他必要尺寸检查布局、可见裁剪与触摸 | 实体手机键盘、系统后台恢复及部署代理行为仍按既有独立验收保留 |
| 真实 DSH／平台 | 按变更能力选择代表闭环，验证真实依赖特有行为 | 模型重复采样、历史方案配对、更多账号／故障矩阵按需执行 |

复用 `internal/web/tests/web_updates_test.mjs` 已有的受控时钟检查，承接刷新调度、限频和静置规则。浏览器仍验证原生连接和实际页面行为；不能用前端虚拟时间声称已经观察了后端真实心跳或长期采集。

长窗口从普通功能用例分出独立测试名或子场景，并有单独显式启用条件；运行普通功能集时不等待这些窗口。专项保留原有时间／容量与判定口径，不能仅缩短到几秒或缩小数据后沿用旧的性能结论。无需新增一套测试运行框架。

历史 frozen／FIFO／四工具／两工具等对照只在对应选型或验收仍有价值时维护。移出常规门槛前核对当前实施记录和未完成验收；不把尚待验证的能力默认为完成，不删除原始失败证据、远端回执或防重记录。

### 7.2 执行入口与 CI

本期保持 Makefile 的既有含义，避免引入第三套构建标签或依赖手工测试名清单：

| 使用场景 | 推荐执行范围 |
| --- | --- |
| 局部修改反馈 | 所属包默认测试，加受影响的本地集成／前端检查；必要时扩大到调用方 |
| 阶段性完整本地回归 | `make test-integration` 已包含核心，不再习惯性先完整跑一次 `make test` |
| 并发／生命周期改动 | 先运行相关包／行为的定向 race；最终交付做一次全仓 race，阶段中按实际影响范围决定是否扩大 |
| 标签及 helper 调整 | `make test-e2e-check`，只作为编译检查 |
| 浏览器／真实依赖变更 | 显式选择相关功能场景；长窗口、满容量、模型重复采样分别选择 |

实施时可沿用以下定向入口，正则随最终测试名调整；这些示例不表示当前失败已经修复，也不要求每次修改都依次执行：

```sh
go test ./internal/platform/gitlab -run '^TestTodos' -count=1
make test-integration TEST_PACKAGES=./internal/room TEST_RUN='Todos|GitLabUnified'
make test-race TEST_PACKAGES=./internal/room TEST_RUN='TestUIUpdates|TestAutomaticRecovery'
make test-e2e-check
```

日常入口继续允许测试结果缓存；计时与偶发问题诊断才加 `-count=1`。当前 `integration || e2e` 是累加关系，带 e2e 标签的全量运行不是“仅跑 e2e”。分析时可用实际构建文件与测试名自动生成差集，执行前检查遗漏、重名和空选择；不把本次临时差集固化为永久 gate。

统一构建标签与启用方式：实际 Chromium／安装环境验收及满容量性能顶层测试归入 e2e 并显式启用；复用 helper 依据调用方保留在可编译的位置。纠正目前少量仅靠环境变量跳过的默认浏览器／性能测试，但不把标签移动本身计作运行提速。

现有 package／release CI 继续保留核心、标准 vet、Web 和打包检查，不加入真实模型、平台或浏览器作为发布依赖。同步更新 [testing.zh.md](testing.zh.md) 的分层、命令和过期耗时记录；日常文档不承担阶段用例清单，实施结果集中维护一份简短记录即可。

### 7.3 验收与交付

- 普通浏览器功能集可独立选择，不触发 60 秒长窗口或满容量场景；专项入口仍可独立复跑，断言和证据口径明确。
- 保留桌面／移动、原生 EventSource、焦点／滚动／触摸、跨上下文及关键失败恢复覆盖；模拟视口仍不视为实体手机验收。
- Makefile、测试标签、场景开关和使用文档一致，原有 CI 检查不被删除或降级。
- 长期真实验收和外部写入按对应能力的既有目标与授权执行，不因本测试改造方案自动启动。

## 8. 性能验收与完成条件

本轮以 T1 已通过的核心测量和定向验证作为 T2 起点；完整集成健康基线仍待补齐，按 §2.1 先处理耗时，不在每个后续阶段开头重新测全量。比较时使用同一机器、相同 Go／Node 版本、相同并发设置和相近负载；浏览器测量另固定 Playwright／Chromium 版本。保留构建缓存，计时使用 `-count=1`，主测量不互相重叠。

记录包级时间、命令墙钟、通过／失败／跳过数、慢组及等待原因。失败或超时单列；不能把提前失败、减少选择范围、增加跳过或迁移标签后的时间下降当作整体收益。

初始优化预算为：默认核心全仓实际执行争取回到 **3 分钟以内**，健康的完整本地集成争取落入现有 **8 分钟**上限。它们是规划目标，不是已验证承诺；浏览器功能集按拆分后的真实流程建立预算，专项保留自己的测量窗口。目标未达时记录剩余慢项与下一步范围，不能削弱覆盖或反复放大默认 timeout 来宣称完成。

验证采用有界节奏：每个切片运行受影响检查和必要的定向 race；代表性慢组只有波动需要解释时才取三次样本。相关提速完成后，安排一次完整本地集成，补齐 T1 待验门槛并测量合并入口；同一份通过结果可同时承接当时的阶段验收与最终交付，后续源码变化时再按影响范围补验。评价核心 3 分钟预算时，另行安排一次核心计时，不在每轮全量集成前串跑。最终全仓 race、标准 vet、Web 检查和 e2e 编译按最终改动补齐，已有且仍适用的证据无需机械重跑；只有新改动、失败、接近预算或波动需要解释时才补跑。全仓单次超时使用显式、有界参数并记录，稳定后再依据健康数据校准默认值。

整体完成要求：

1. T1 失败清单及清理死锁已处理，完整本地套件能正常结束且通过。
2. 关键规则下沉到实际所属层，上层重复矩阵收缩，断言承接可审查；完整核心＋集成的成本确实下降。
3. 核心时间等待和夹具并发有明确约束，相关 race 通过，生产时间、耐久性和授权行为保持原合同。
4. 浏览器普通功能与专项测量能独立运行，真实依赖验收边界未被削弱。
5. 执行入口与文档一致，记录实际耗时、达成／未达预算及仍需单独完成的真实验收；不把编译、组件或模拟证据升级为真实平台证明。

## 9. T1–T4 之后的慢项复盘与后续优化（前四批已实施，其余待实施）

本节在提交 `6bf554b` 后补充，最初只整理已有证据和后续方案。用例明细见附录 B；T1–T4 实施与机器可读证据见 [test-suite-health.zh.md](implementation/test-suite-health.zh.md) 和 [test-suite-health-evidence.json](implementation/test-suite-health-evidence.json)。§9.2 第一批现已实施，四文件共 35 项夹具复用四槽并行；同范围 38 项顶层测试、单并发和定向 race 均通过，切片墙钟 42.870 → 24.367 秒（约缩短 43.2%）。一次完整集成墙钟 831.331 秒，1,914 项顶层通过、1 项既有 CSP 断言失败、9 项跳过；第一批 38 项均通过，但当时完整集成尚未验收通过。结果及隔离审核见[第一批实施记录](implementation/room-fixture-parallel.zh.md)。第二批现已实施：动作规则及工具矩阵下沉、夹具初始化收缩，同范围墙钟 22.108 → 19.192 秒（约缩短 13.2%），15 项顶层及其承接检查、单并发和扩大调用方的 94 项定向 race 通过。校准上述过期 CSP 断言后，一次完整集成 768.749 秒通过，1,924 项顶层通过、9 项跳过，八分钟预算未达；见[第二批实施记录](implementation/authorization-tool-matrix.zh.md)。第三批现已实施：Lark 原生协议矩阵下沉，同范围整个包墙钟 74.142 → 59.599 秒（约缩短 19.6%），新增八项轻量检查；单并发、Lark 全包及 Room 调用方定向 race 通过。本次完整集成约 12 分 48 秒（Go JSON 事件跨度）通过，1,932 项顶层通过、9 项跳过，22 包通过、八分钟预算未达；见[第三批实施记录](implementation/lark-protocol-matrix.zh.md)。第四批现已实施：Inbox／连接通知剩余负向窗口使用受控时间及调度结清屏障，纯规则去掉 50 次无关种子写入，日历发布改用真实 CalendarActor／Room／Store 组件闭环。同范围含新增规则的三样本墙钟中位数 12.439 → 9.427 秒（约缩短 24.2%），单并发及 37 项定向 race 通过；本次完整集成 1,933 项顶层通过、9 项跳过，22 包通过；墙钟 948.975 秒（15 分 49 秒），八分钟预算未达。见[第四批实施记录](implementation/timer-assembly.zh.md)。第五批及以后仍待实施；本节不改变前述安全和验收要求。

### 9.1 最新结论与优先级依据

| 已观察事实 | 对下一轮的含义 |
| --- | --- |
| 第四批完整集成 1,933 项顶层通过、9 项跳过，22 包通过；墙钟 948.975 秒（15 分 49 秒），八分钟预算未达 | 本批只改测试；局部中位数约缩短 24.2%，没有独立重测全仓核心或全仓 race。保留完整健康门槛，后续只推进已授权批次 |
| 第三批后的完整集成 22 包全部通过，1,932 项顶层通过、9 项跳过；Go JSON 事件跨度 768.477 秒，八分钟预算未达 | Lark 包同条件墙钟约缩短 19.6%，但 Room 仍是全量主要成本；完整集成事件跨度与此前 Python 墙钟口径不同，不据此宣称全仓提速 |
| 第二批后的完整集成健康通过，墙钟 768.749 秒、Room 737.881 秒；1,924 项顶层通过、9 项跳过，所选 15 项全部通过 | 健康门槛已补齐，但八分钟预算仍未达。下表核心数字仍是此前独立测量，不用失败的上次全量或本批切片推算全仓收益 |
| 最终完整核心墙钟 453.328 秒，Room 包 440.540 秒；T1 核心墙钟 557.748 秒 | 当前全仓观察缩短约 18.7%，三分钟目标未达。Room 是首要执行路径；不能把 T2 的局部 57% 改善当作全仓收益 |
| 最终核心 Room 串行顶层报告耗时合计约 **404.40 秒**；31 项并行夹具从首次 cont 到最后完成约 **34.12 秒** | 最推荐先处理仍串行的独立 Room 夹具及其重复装配。上述两个区间不是精确剖析分账，不机械相加；其他包与 Room 同时运行 |
| 工具／动作并行用例顶层可达 10–19 秒，但部分大多在等槽位 | 先看 `wait` 与 `held`，再决定优化哪项；保持四槽，不直接按 `Elapsed` 排名，也不靠无限并行或重排测试名获取收益 |
| `TestToolIndependentTargetsMergeAndProjectActualCommit`：18.90 秒，其中 wait 6.41／held 12.50；`TestReferenceActionRefusals`：15.71 秒，其中 wait 3.60／held 12.11 | 两项确有较高执行／清理成本，是第二批矩阵下沉和夹具收缩中优先处理的对象 |
| `TestReferenceActionIntentPersistenceAndProcessing`：10.46 秒，其中 wait 9.80／held 0.66；`TestReferenceActionKeepsOtherUnansweredQuestion`：8.78 秒，其中 wait 7.99／held 0.79 | 暂不为这类个体新增抽象；先减少前面长持槽任务，避免优化错对象 |
| 其他包最终执行：`internal/inputstream` 44.054 秒、`internal/agent/dsh` 36.856 秒、`internal/platform/lark` 核心 12.692 秒 | 这些包当前被 Room 的长执行遮蔽。输入持久化与 DSH 的优化放在后批，不能把其节省秒数直接累加成全仓收益 |
| T3 完整集成在 630.662 秒中止；收口集成在 80.223 秒按用户要求中止 | 附录中的集成单项都是“已结束项的观察”，不是完整集成排名或健康基线；未结束项不补猜耗时 |
| 同一日历组合在 T3 定向约 3.54 秒、负载较高的完整集成中 19.50 秒；旧 deadline 用例失败后在最终核心 2.30 秒通过 | 先区分重复装配、实际 I/O、排队、环境负载与失败等待。既有服务升级／独立编译重叠是负载观察，不是已证明的根因 |

当前代码已具备四槽隔离夹具、测试二进制／生产 CLI 一次构建、Lark 独立零间隔队列和 Python `-S`。后续应在这些能力上减少重复工作，不重复建设或将已有收益重新计入。

### 9.2 最推荐的实施顺序

| 顺序 | 改造范围与具体做法 | 保留边界／本批验收 | 投入与预期作用 |
| --- | --- | --- | --- |
| **第一批：Room 串行夹具有界并行** | 首先审核 `platform_member_current_test.go`（串行合计 16.68 秒）、`platform_connect_notifications_test.go`（13.55 秒），再审 `platform_lark_authorization_test.go`（9.22 秒）和 `model_management_test.go`（9.14 秒）。逐顶层核对私有 Store、manager、账号队列、端口、hook 及传递调用的环境修改；每次只选 5–10 个符合条件的顶层复用 `parallelFixtureTest` | 仍共用现有四槽。`t.Setenv`／`t.Chdir`、固定环境键、共享父夹具及共享进程状态的调用保持串行，不能只检查顶层函数。随机独立凭据键仍需审核清理和进程继承。候选组在单并发、正常并发及必要 race 下验证；比较整组墙钟和持槽时间，若磁盘竞争抵消收益则停止扩展 | **最高推荐，投入较低**；针对约 404 秒串行主成本。首批四文件约 48.59 秒是观察范围，绝不是可直接节省的承诺 |
| **第二批：Room 授权／工具矩阵与大组合收缩** | 先拆 `TestReferenceActionRefusals` 的 14 个拒绝分支：请求形状／版本冲突等优先直接覆盖现有 `referenceActionRequestError`、`referenceActionUnitConflict` 等生产规则；依赖 Store 的指令／规则判断使用最小合法状态。再处理 `TestReferenceActionThreeAuthorities`、工具入口一致性及独立目标合并。最后收缩 `TestPlatformAuthorizationSurvivesModelRotationAndPause` 的 provider × 状态 × 完成时机组合 | 规则测试直接调用实际生产决策；必要提取由原路径调用，不建立第二套授权模型。真实 owner 继续覆盖账号／成员／目标／版本复核、准备后撤权与 ABA、并发合并、保存／未知提交、重启防重。跨模型轮换仍保留原 operation、Inbox 投递及暂停；provider 差异、轮内结果与轮换后结果不能在无证据时合并 | **高推荐，投入中等**；先减少真实长持槽任务和重复 manager／会话装配，连带降低短任务排队。不能把一次成功流程替代全部提交或并发边界 |
| **第三批：Lark 原生协议矩阵下沉** | 优先 `TestReadFailuresDiscardCandidate`、`TestMessageSearchRejectsIncompleteAndUnverifiedPages`、`TestMessageBatchFailuresDiscardCandidates`，再处理 sender、membership、image、reply/send。将 `classify`／`decode` 和各业务实际候选校验中的纯错误分类、格式、重复／范围判断放到轻量单元层；原生 CLI 保留 argv、stdout／退出码、前后身份核验及流程独有拒绝 | 先给 `parallelClientTest` 增加与 Room 同等的 wait／held 诊断，保持四槽；不将 15–44 秒顶层时间全归于子进程。每类 wire DTO、单页不隐式扩展、bot／账号切换、取消、单次 POST 和失败不泄露候选仍有实际进程证明；不能缓存或跳过生产身份核验 | **高推荐，投入中等**；主要改善本地集成和编入 e2e 的本地部分。Lark 核心已较短，不宣称这批直接显著缩短当前核心主路径 |
| **第四批：剩余定时等待与非必要装配** | 查 `inbox_recovery_test.go` 的退避／80–100ms 负向等待、`platform_connect_notifications_test.go` 的两处 150ms 窗口及日历发布组合。只对确属时间规则的分支复用受控时间；成功路径等待明确处理／结清屏障。`TestCalendarRecurringPublicationKeepsTaskActive` 的递归状态规则由 `internal/calendar` 穷举，实际发布和不终止任务保留组件闭环 | T2 已改造的心跳／阻塞 writer／恢复 bubble 不再机械重写。时间推进后证明相关调度确已处理，负向断言覆盖完整窗口；保留少量真实 timer／HTTP／取消接线。若主要成本是落盘而非 Sleep，回到夹具分析，不加无效时钟抽象 | **中等推荐，投入较低至中等**；优先处理仍串行的项，已有小等待的收益不夸大 |
| **第五批：持久边界与 DSH 进程夹具** | 输入升级先复用不可变输入材料、减少与目标发布点无关的初始化；所有文件发布／未知提交／恢复中断点继续真实执行。`TestConsumptionAbortHandoffRaceAndBusy` 目前循环 12 次，评估以明确 Abort／Handoff 两种提交次序的屏障承接分支，重复竞争另保留专项。DSH `TestResumeFailsClosedWithoutReplacement` 等将能在加载／身份校验层拒绝的模式与真正需要 Open／Resume 子进程的模式分开 | 不关闭 `fsync`，不共享可变 Store，不合并多种未知提交。输入升级的并行试验曾未证明稳定收益，不能整文件并行。DSH 的 `fixtureOptions` 目前 `t.Setenv("TEAMX_TEST_KEY", ...)`，未经改造不能并行；即使改为私有注入，也保留真实信号、回收、未结清禁止替换及失败恢复合同 | **后批，投入较高**；这些覆盖价值高、当前对核心墙钟的直接收益有限，优先减少夹具启动成本，正式存储算法优化需另有证据 |
| **第六批：执行入口与专项边界收口** | 核对仍仅靠环境变量跳过的 `TestSharedInboxFullCapacityMeasurements`、pinned glab／PTY 与生产 askpass 验证，按实际依赖分到 e2e 或本地集成，helper 留在调用方可编译的位置。保留普通浏览器、长窗口、满容量及真实平台的独立选择 | 这些默认跳过项本轮未测容量或安装环境耗时；标签修正只算分层完善，不算提速。浏览器 60 秒窗口、10,000 条／近 64 MiB、真实手机和代理边界继续独立保留；不增加真实平台写入授权或发布门槛 | **低成本收尾**；优先减少误运行和重复全跑。T4 普通浏览器目前最长单组约 35 秒，没有必要再优先压缩已很短的页面流程 |

第一批完成后先判断串行主成本是否下降，再决定第二批的扩展范围。不要为了“一次做完全部批次”跨入 Room 全面拆包、通用 mock 框架、测试选择平台或并发上限扩容。新增正式函数与长期测试按职责命名，不按本表批次命名。

### 9.3 每批应交付的最小方案与验证

1. **先写本切片承接映射。** 对每项拟删除或收缩的分支，列出原断言、实际生产规则所在位置、下层直接测试、保留的真实组件／进程证明；无法给出承接的分支不删除。纯规则可用最小内存状态，存储与结清边界继续使用私有真实目录。
2. **先拆成本，再改代码。** 直接利用已有 JSON、wait／held 记录定位。确需新诊断，只在本次已有 helper 记录 fixture 初始化、原生 CLI 次数、Actor／提交／结清等待和清理的阶段耗时；不为此建设全仓永久登记系统。装配成本高的组合先缩小不相关初始化，再考虑并行。
3. **仅做一次同条件切片对照。** 开发中只跑受影响层；准备评估收益时，对同一业务范围顺序测量改前／改后，包含新增下层检查和保留集成，使用相同标签、并发和 `-count=1`。已有 before 可复用，三次采样只用于解释波动。并行调整另验证单并发与必要 race，包含清理；不能相加各 `Elapsed` 或把纯标签移动算作收益。
4. **按代码影响补齐边界。** 规则下沉若修改生产函数，补其调用方接线及相关并发／授权 race；仅改文档不跑测试，标签／helper 修改做对应编译。不得因阶段命名或正则变化漏掉新增检查。原本已通过且源码未变的验证不机械重跑。
5. **最后安排一次阶段性全量。** 各切片完成、源码稳定且无明显独立编译／升级负载时，才运行一次完整 `test-integration`；它已包含核心，前面不串跑完整核心。只有用户单独评价核心预算时再做一次核心计时；最终全仓 race 按交付范围安排。中途失败先看首个失败与清理栈，保留部分结果，不将每次 timeout 增大后重跑作为推进方式。

后续仍以核心三分钟、健康本地集成八分钟为目标，但当前没有证据保证上述批次足以达到。每批报告同范围实际变化及剩余主成本；必要时依据稳定健康数据另行校准默认诊断上限，不能把放宽上限写成优化成果。

### 9.4 已明显改善或应保留成本的项

| 入口／范围 | 已有证据及后续处理 |
| --- | --- |
| `TestUIUpdatesHTTPHeartbeat` | T2 同条件约 15.10 → 0.11 秒；当前真实 HTTP 帧／撤权／超时覆盖保留，无需重复优化已消除的 15 秒等待 |
| `TestUIUpdatesBlockedWriterCancellation` | T2 约 5.86 → 0.74 秒；保留 ResponseController 与取消原因矩阵 |
| `TestDSHRecoveryPreviewTimeoutAndIdentity` | T2 约 2.55 → 0.43 秒。T3 负载较高时仍观察到 6.13 秒，包含实际夹具／落盘，不能再把它全部归因于预览 timeout |
| `automatic_recovery_test.go` | T1 串行合计 19.60 秒；T2 对照 18.67 → 5.92 秒；最终核心合计 10.42 秒。`TestAutomaticRecoveryPermanentErrorsStayPaused` 的旧 4.98 秒真实等待已处理，剩余以当前具体成本判断 |
| `TestReferenceActionUsesLaterVerifiedAlias` | T1 旧观察 6.37 秒，最终核心顶层约 0.91 秒、持槽约 0.91 秒。并行排队条件不同，不将差值当作独立提速证明；目前不优先改动 |
| 两组工具配置夹具 | T1 串行合计 76.90 秒；T2 同条件执行区间 79.475 → 26.234 秒。当前全核心与动作组共用四槽，不能拿个别顶层变长当作同条件回退 |
| `TestLarkToolConfigurationUserScopeGapsRespectPlatformSupport` | 旧观察 6.30 秒；T3 的规则由 provider 测试与 `TestLarkToolConfigurationUserAndApplicationScopeAdmission` 代表接线承接，旧名已退出，不能按旧名继续漏选或重复建矩阵 |
| Todos／CLI／UI／日历 T3 切片 | 含新增下层检查的合并墙钟 47.492 → 40.117 秒。`TestTodosAppHandler` 完整业务由 TCP `TestTodosAppHTTP` 承接，stdio 只保留桥接合同；日历 manager 组合归入集成，不能把移层时间算成总体节省。CLI 单独观察有 1.659 → 2.692 秒，未证明单切片提速 |
| 输入升级并行试验 | 包执行 19.268 → 15.470／16.845 秒，但发布中断单项受 I/O 竞争影响，未采用；优先复用不可变材料，不恢复整组并行试验作为既定方案 |
| 原失败后的 30／60 秒等待及 Todos 清理死锁 | T1 已处理，具体旧用例保留附录 A。只有复现新的清理故障时才回到 T1；不能把旧 timeout 和最终通过样本混算平均性能 |
| `TestDSHRecoveryPrioritizesOnlySavedMappingsBeforePageLimit` | T3 12.65 秒后失败，尚未到分页优先级断言；独立 3.54 秒、最终核心 2.30 秒通过，原十秒上下文保留。若重现，先分段观察来源写入、关联与读取、剩余 deadline，不仅提高上限 |
| 编译／命令墙钟异常 | T3 最后 CLI 断言补验墙钟 236.591 秒，Room 执行约 4.04 秒；主要是当时重新编译成本，不是该 CLI 用例执行变成四分钟。复用匹配源码／标签／race 的现有二进制，源码变更后仍重建 |

## 附录 A：T1 需要核对的失败清单

以下 11 项均在低并发复跑中仍失败。除已明确的 tools/list 断言过期外，表中只记录观测症状，不预判都是测试缺陷或产品缺陷。

| 测试 | 本轮观测／排查入口 |
| --- | --- |
| `TestInputUpgradeRotatesSettledDSHContract` | `input_upgrade_dsh_integration_test.go`：tools/list 固定两个工具，实际包含 History |
| `TestRoomsCatalogDuringBlockedNotificationWork` | `rooms_catalog_integration_test.go`：private-maintenance 解除阻塞后 `input_invalid` |
| `TestIndependentEventsAppHandler` | `platform_notification_events_independent_integration_test.go`：页面夹具找不到成员平台 GitLab 入口 |
| `TestIndependentEventsPublicConfigureAndStaleVerification` | 同上：配置请求 403 |
| `TestIndependentEventsReplacementWaitsForOldWorker` | 同上：结清后恢复返回 notification service closing |
| `TestTodosReplacementFailureNeverEnablesBothAccounts` | `platform_notification_todos_integration_test.go`：retire 场景重启后状态／worker 断言失败 |
| `TestTodosTCPHTTPConfigureSyncDisableAndInboxDelivery` | 同上：409，通知启用缺少显式来源 |
| `TestLarkNotificationsUpgradeInterruptedCommitRecoveryAndConflict` | `lark_notifications_upgrade_test.go`：通知升级中断恢复／冲突检查失败，独立保留日志定位 |
| `TestRuntimeBackgroundNotificationsIgnoreAnotherRoomsOperation` | `runtime_execution_test.go`：投递 owner 断言及生命周期清理超时，低并发约 30 秒 |
| `TestPolicyManagementHTTP` | `policy_management_e2e_test.go`／`web_policy_http.mjs`：子进程约 60 秒后被杀，需定位具体等待步骤 |
| `TestSecondNotificationProviderReloadAndDedup` | `capability_independence_integration_test.go`：多 provider 重载／去重检查失败，独立保留日志定位 |

另外两个未结束项为 `TestTodosAppHandler`、`TestTodosAppHTTP`，共享 §4.1 所述清理死锁；其业务断言失败仍须在解锁修复后继续处理。


## 附录 B：本轮已观察慢项清单（后续处理用）

本表来自已保存结果，**没有为填表重跑任何测试**。收录四份现有日志中：最终完整核心顶层 ≥3 秒，T3／收口两次中止集成中已结束顶层 ≥5 秒，或 T3 改造后定向顶层 ≥5 秒的并集，共 **123 个不同包／测试名**；另列前文已处理项和浏览器专项。阈值仅用于本轮清单，低于阈值但累计偏重的文件见 B.1；不是新增性能 gate 或全仓永久登记系统。

时间均为秒，父项包含其子场景；同名不同包分别记录。`并` 表示至少一次观察为并行顶层，报告时间可包含四槽等待；Room 最终 `wait/held` 另列，held 包含清理，不包含此前 Go `-parallel` 调度暂停。`串` 表示所列观察未出现顶层 pause。`—` 表示该份记录没有已结束结果／不在该层，**不等于零秒**。T3 集成失败或中止记录与收口的短暂集成观察不具备同条件对照关系，数值不可平均或相加。表内按已知最大观测排序便于查找，不代表优化优先级，实施顺序以 §9.2 为准。逐项数字从 `/tmp/teamx-t2/`、`/tmp/teamx-t3/`、`/tmp/teamx-final/` 的既有 JSON 提取并固化于本文，后续不依赖临时目录继续存在。

### B.1 核心串行聚合成本

下列为最终核心各文件的串行顶层耗时之和（只加顶层，不重复加子项），是定位范围而非独立运行墙钟。不同包可并行执行；不能跨包相加推算全仓时间。

| 包／源码 | 串行合计 | 后续处理 |
| --- | ---: | --- |
| [internal/inputstream/consumption_test.go](../internal/inputstream/consumption_test.go) | 20.28 | 第五批：持久边界与重复竞争；当前非最长路径 |
| [internal/room/input_upgrade_test.go](../internal/room/input_upgrade_test.go) | 18.36 | 第五批：保留发布点，优化无关准备 |
| [internal/room/platform_member_current_test.go](../internal/room/platform_member_current_test.go) | 16.68 | 第一批：隔离审核与四槽并行 |
| [internal/room/platform_connect_model_test.go](../internal/room/platform_connect_model_test.go) | 13.85 | 第二批：跨轮换组合收缩 |
| [internal/room/platform_connect_notifications_test.go](../internal/room/platform_connect_notifications_test.go) | 13.55 | 第一批：隔离审核与四槽并行 |
| [internal/agent/dsh/session_test.go](../internal/agent/dsh/session_test.go) | 10.95 | 第五批：进程／配置校验拆分；固定环境键保持串行 |
| [internal/room/automatic_recovery_test.go](../internal/room/automatic_recovery_test.go) | 10.42 | 第四批：先拆装配／调度成本 |
| [internal/agent/dsh/recovery_test.go](../internal/agent/dsh/recovery_test.go) | 10.38 | 第五批：进程／配置校验拆分；固定环境键保持串行 |
| [internal/room/inbox_recovery_test.go](../internal/room/inbox_recovery_test.go) | 10.30 | 第四批：先拆装配／调度成本 |
| [internal/inputstream/storage_test.go](../internal/inputstream/storage_test.go) | 10.16 | 第五批：持久边界与重复竞争；当前非最长路径 |
| [internal/room/platform_lark_authorization_test.go](../internal/room/platform_lark_authorization_test.go) | 9.22 | 第一批：隔离审核与四槽并行 |
| [internal/room/model_management_test.go](../internal/room/model_management_test.go) | 9.14 | 第一批：隔离审核与四槽并行 |
| [internal/room/inbox_task_events_test.go](../internal/room/inbox_task_events_test.go) | 7.93 | 第一批后扩展候选；先审核传递环境与共享状态 |
| [internal/room/platform_connect_gitlab_test.go](../internal/room/platform_connect_gitlab_test.go) | 7.76 | 第一批后扩展候选；先审核传递环境与共享状态 |
| [internal/room/default_assistant_test.go](../internal/room/default_assistant_test.go) | 6.86 | 第一批后扩展候选；先审核传递环境与共享状态 |
| [internal/room/input_stream_tools_test.go](../internal/room/input_stream_tools_test.go) | 6.82 | 第一批后扩展候选；先审核传递环境与共享状态 |
| [internal/room/calendar_recurrence_test.go](../internal/room/calendar_recurrence_test.go) | 6.65 | 第四批：先拆装配／调度成本 |
| [internal/room/history_test.go](../internal/room/history_test.go) | 6.49 | 第一批后扩展候选；先审核传递环境与共享状态 |
| [internal/room/platform_member_test.go](../internal/room/platform_member_test.go) | 6.35 | 第一批后扩展候选；先审核传递环境与共享状态 |
| [internal/room/ui_update_sources_test.go](../internal/room/ui_update_sources_test.go) | 6.13 | 第一批后扩展候选；先审核传递环境与共享状态 |
| [internal/room/input_stream_reader_test.go](../internal/room/input_stream_reader_test.go) | 6.06 | 第一批后扩展候选；先审核传递环境与共享状态 |
| [internal/room/inbox_consumption_test.go](../internal/room/inbox_consumption_test.go) | 5.93 | 第一批后扩展候选；先审核传递环境与共享状态 |
| [internal/room/agent_read_views_test.go](../internal/room/agent_read_views_test.go) | 5.92 | 第一批后扩展候选；先审核传递环境与共享状态 |
| [internal/room/model_delete_test.go](../internal/room/model_delete_test.go) | 5.87 | 第一批后扩展候选；先审核传递环境与共享状态 |
| [internal/room/platform_member_gitlab_test.go](../internal/room/platform_member_gitlab_test.go) | 5.86 | 第一批后扩展候选；先审核传递环境与共享状态 |
| [internal/agent/dsh/lifecycle_test.go](../internal/agent/dsh/lifecycle_test.go) | 5.85 | 第五批：进程／配置校验拆分；固定环境键保持串行 |
| [internal/room/platform_connect_gitlab_setup_test.go](../internal/room/platform_connect_gitlab_setup_test.go) | 5.81 | 第一批后扩展候选；先审核传递环境与共享状态 |
| [internal/room/mcp_transport_test.go](../internal/room/mcp_transport_test.go) | 5.59 | 第一批后扩展候选；先审核传递环境与共享状态 |
| [internal/room/inbox_reclamation_test.go](../internal/room/inbox_reclamation_test.go) | 5.45 | 第一批后扩展候选；先审核传递环境与共享状态 |
| [internal/room/agent_contract_test.go](../internal/room/agent_contract_test.go) | 5.32 | 第一批后扩展候选；先审核传递环境与共享状态 |
| [internal/room/platform_selection_gitlab_test.go](../internal/room/platform_selection_gitlab_test.go) | 5.16 | 第一批后扩展候选；先审核传递环境与共享状态 |
| [internal/room/platform_connect_test.go](../internal/room/platform_connect_test.go) | 5.12 | 第一批后扩展候选；先审核传递环境与共享状态 |
| [internal/room/mcp_inbox_test.go](../internal/room/mcp_inbox_test.go) | 5.11 | 第一批后扩展候选；先审核传递环境与共享状态 |
| [internal/room/inbox_lifecycle_test.go](../internal/room/inbox_lifecycle_test.go) | 5.08 | 第一批后扩展候选；先审核传递环境与共享状态 |

### B.2 `internal/room`：80 项

工具／动作长持槽项按第二批处理，只有等待长的项随上游成本下降观察；其他串行项目按 B.1 的组优先级处理。

| 测试（源码） | 最终核心 | T3 集成 | 收口集成 | 补充证据／并发属性 |
| --- | ---: | ---: | ---: | --- |
| [`TestLocalGitFetchPushAndExactSHA`](../internal/room/capability_independence_integration_test.go) | — | 35.45 | 1.48 | 串 |
| [`TestCalendarManagedRecurringPauseRecoveryAndPublication`](../internal/room/calendar_lifecycle_integration_test.go) | — | 19.50 | 1.48 | 串 |
| [`TestRoomUnreadPeekPaginatesWithoutConsumption`](../internal/room/cli_test.go) | 1.62 | 19.21 | 1.32 | 串 |
| [`TestToolIndependentTargetsMergeAndProjectActualCommit`](../internal/room/tools_target_concurrency_test.go) | 18.90 | — | — | 并；最终 wait/held 6.41/12.50；T1 9.79；T3 定向 11.06 |
| [`TestReferenceActionRefusals`](../internal/room/followups_actions_test.go) | 15.71 | — | — | 并；最终 wait/held 3.60/12.11；T1 13.29 |
| [`TestToolBindingsLegacyUpgradeCapacityAndOverflow`](../internal/room/tools_target_concurrency_test.go) | 14.69 | — | — | 并；最终 wait/held 10.22/4.48；T3 定向 5.92 |
| [`TestCalendarManagedOfflineRecoveryOnlyLatestWhileAgentsPaused`](../internal/room/calendar_lifecycle_integration_test.go) | — | 14.41 | 1.71 | 串 |
| [`TestPlatformAuthorizationSurvivesModelRotationAndPause`](../internal/room/platform_connect_model_test.go) | 13.85 | — | — | 串；T1 21.73 |
| [`TestLarkToolConfigurationUserAndApplicationScopeAdmission`](../internal/room/tools_lark_configuration_test.go) | 13.77 | — | — | 并；最终 wait/held 9.30/4.47；T3 定向 9.10 |
| [`TestToolLegacyUnknownReceiptsRemainReadOnlyAfterRestart`](../internal/room/tools_target_concurrency_test.go) | 13.70 | — | — | 并；最终 wait/held 9.90/3.80；T3 定向 6.33 |
| [`TestReferenceActionThreeAuthorities`](../internal/room/followups_actions_test.go) | 13.39 | — | — | 并；最终 wait/held 8.24/5.15；T1 10.24 |
| [`TestDSHRecoveryPrioritizesOnlySavedMappingsBeforePageLimit`](../internal/room/dsh_recovery_context_test.go) | 2.30 | 12.65 失败 | — | 串 |
| [`TestConfigurationReceiptsAndObservationsSurviveRestart`](../internal/room/configuration_recovery_integration_test.go) | — | 12.45 | 5.69 | 串 |
| [`TestOnboardingToolInterruptedSettlementKeepsCommittedProjection`](../internal/room/tools_target_concurrency_test.go) | 12.43 | — | — | 并；最终 wait/held 9.46/2.97；T3 定向 7.07 |
| [`TestDiscussionRestartSettlesWholeTeamBeforeClearing`](../internal/room/discussion_test.go) | 0.52 | 12.32 | — | 串 |
| [`TestDirectConversationRejectsInconsistentPersistence`](../internal/room/direct_conversation_test.go) | 0.76 | 12.05 | — | 串 |
| [`TestLarkToolConfigurationExplicitActionsAndPreservedIntent`](../internal/room/tools_lark_configuration_test.go) | 12.02 | — | — | 并；最终 wait/held 8.54/3.48；T1 5.40；T3 定向 7.27 |
| [`TestToolPreparedBindingRejectsABAAndAllowsOtherMembers`](../internal/room/tools_target_concurrency_test.go) | 11.98 | — | — | 并；最终 wait/held 6.43/5.55；T3 定向 9.81 |
| [`TestOnboardingToolExactObservationAndLegacyPending`](../internal/room/tools_target_concurrency_test.go) | 11.72 | — | — | 并；最终 wait/held 7.65/4.07；T3 定向 7.31 |
| [`TestReferenceActionRuleLifecycleAdmission`](../internal/room/followups_actions_test.go) | 11.20 | — | — | 并；最终 wait/held 6.33/4.87；T1 11.92 |
| [`TestAgentInboxCLIOutputAndConfirmation`](../internal/room/agent_read_views_test.go) | 4.48 | 10.62 | 1.54 | 串 |
| [`TestDiscussionRestartRecoversSettledRateLimitFailures`](../internal/room/discussion_test.go) | 0.67 | 10.61 | — | 串 |
| [`TestReferenceActionIntentPersistenceAndProcessing`](../internal/room/followups_actions_test.go) | 10.46 | — | — | 并；最终 wait/held 9.80/0.66 |
| [`TestLarkToolConfigurationEntrypointsAgreeAndSelfProofIsRequired`](../internal/room/tools_lark_configuration_test.go) | 10.34 | — | — | 并；最终 wait/held 0.91/9.42；T1 6.73；T3 定向 7.94 |
| [`TestToolSaveAttemptBoundaryAndOriginalOperationFence`](../internal/room/tools_target_concurrency_test.go) | 10.21 | — | — | 并；最终 wait/held 2.63/7.58；T1 5.86；T3 定向 8.88 |
| [`TestLarkToolConfigurationCLIAddsOnlyRequestedActions`](../internal/room/tools_lark_configuration_test.go) | 9.52 | — | — | 并；最终 wait/held 7.92/1.60；T3 定向 10.11 |
| [`TestReferenceActionRechecksAuthorityBeforeRetry`](../internal/room/followups_actions_test.go) | 10.04 | — | — | 并；最终 wait/held 4.24/5.80；T1 9.69 |
| [`TestLarkToolConfigurationRechecksAfterRemotePreparation`](../internal/room/tools_lark_configuration_test.go) | 9.90 | — | — | 并；最终 wait/held 1.80/8.10；T1 6.23；T3 定向 8.53 |
| [`TestDefaultAssistantLegacyDeletionTombstoneAndManualReplacement`](../internal/room/default_assistant_test.go) | 0.58 | 9.82 | 0.67 | 串 |
| [`TestReferenceActionPersistenceFailures`](../internal/room/followups_actions_test.go) | 9.69 | — | — | 并；最终 wait/held 4.35/5.35；T1 6.49 |
| [`TestToolPreparationRejectsExpiredCapabilityObservation`](../internal/room/tools_target_concurrency_test.go) | 9.46 | — | — | 并；最终 wait/held 6.45/3.01；T3 定向 6.54 |
| [`TestReferenceActionUnknownRetryAndRecovery`](../internal/room/followups_actions_test.go) | 9.43 | — | — | 并；最终 wait/held 3.89/5.53；T1 4.97 |
| [`TestLarkToolConfigurationOnboardingPreservesChoicesAndUnknownSubmission`](../internal/room/tools_lark_configuration_test.go) | 8.93 | — | — | 并；最终 wait/held 6.08/2.85；T3 定向 5.67 |
| [`TestLarkToolConfigurationCanonicalizesExistingGrants`](../internal/room/tools_lark_configuration_test.go) | 8.59 | — | — | 并；最终 wait/held 5.95/2.64；T3 定向 8.86 |
| [`TestReferenceActionKeepsOtherUnansweredQuestion`](../internal/room/followups_actions_test.go) | 8.78 | — | — | 并；最终 wait/held 7.99/0.79 |
| [`TestLarkPermissionGapRechecksExactTargetAfterRemoteRead`](../internal/room/tools_target_concurrency_test.go) | 8.69 | — | — | 并；最终 wait/held 6.21/2.48；T3 定向 6.70 |
| [`TestDiscussionRestartClearFailureCanBeRetried`](../internal/room/discussion_test.go) | 0.55 | 8.42 | — | 串 |
| [`TestDSHRestorePausesWithoutReplacingBackend`](../internal/room/dsh_test.go) | 0.40 | 8.24 | — | 串 |
| [`TestAgentPolicyAndActionDiagnosticRecovery`](../internal/room/cli_diagnostics_test.go) | 0.66 | 8.18 | 0.56 | 串 |
| [`TestInputUpgradeResumesEveryPublicationBoundary`](../internal/room/input_upgrade_test.go) | 8.15 | — | — | 串；T1 8.56 |
| [`TestReferenceActionRuleUnknownSnapshotRecovery`](../internal/room/followups_actions_test.go) | 8.14 | — | — | 并；最终 wait/held 6.90/1.24；T1 6.24 |
| [`TestActionsLedgerFailureNeverReplays`](../internal/room/actions_test.go) | 2.89 | 8.11 | 1.03 | 串 |
| [`TestAutomaticRecoveryAfterFailedSessionOpen`](../internal/room/automatic_recovery_test.go) | 2.06 | 8.05 | 0.96 | 串 |
| [`TestAutomaticRecoveryRestoresOnlyEnabledSettledSessions`](../internal/room/automatic_recovery_test.go) | 2.30 | 7.85 | 1.01 | 串 |
| [`TestToolContractsRejectRetiredVersionsAndRequireHumanObservation`](../internal/room/tools_target_concurrency_test.go) | 7.67 | — | — | 并；最终 wait/held 6.77/0.90 |
| [`TestManagedActivationFailureReceiptsAndInterruptedRecovery`](../internal/room/activation_integration_test.go) | — | 7.58 | 0.70 | 串 |
| [`TestDefaultAssistantCreationCommitRecovery`](../internal/room/default_assistant_test.go) | 0.44 | 7.53 | 0.47 | 串 |
| [`TestTodosAppHTTP`](../internal/room/web_todos_integration_test.go) | — | — | — | 并；T3 定向 7.46 |
| [`TestDefaultAssistantOnceForNewAndExistingCatalogs`](../internal/room/default_assistant_test.go) | 0.59 | 7.37 | 0.68 | 串 |
| [`TestReferenceActionUnknownSurvivesLaterStop`](../internal/room/followups_actions_test.go) | 7.34 | — | — | 并；最终 wait/held 6.64/0.70 |
| [`TestDefaultAssistantInitialEnableAndPauseSurviveRestart`](../internal/room/default_assistant_test.go) | 0.47 | 7.30 | 0.43 | 串 |
| [`TestDSHRecoveryAdmitsOnlyStoppedOriginalSession`](../internal/room/dsh_collaboration_test.go) | 0.61 | 7.18 | — | 串 |
| [`TestToolIntentIdentityIncludesObservationAndNormalizesActions`](../internal/room/tools_target_concurrency_test.go) | 2.63 | — | — | 并；最终 wait/held 0.00/2.63；T3 定向 7.16 |
| [`TestDSHMembersQueueMergeAndIsolateFailure`](../internal/room/dsh_collaboration_test.go) | 0.43 | 6.94 | — | 串 |
| [`TestEnableResetsFailedSessionAndReplenishesTogether`](../internal/room/enable_test.go) | 0.53 | 6.93 | — | 串 |
| [`TestLarkToolConfigurationNativeSelfEvidenceRefreshAndForgery`](../internal/room/tools_lark_configuration_test.go) | 6.45 | — | — | 并；最终 wait/held 0.00/6.45；T3 定向 6.03 |
| [`TestDefaultAssistantBlockedConfigurationAndRetry`](../internal/room/default_assistant_test.go) | 0.40 | 6.25 | 0.38 | 串 |
| [`TestDSHRecoveryPreviewTimeoutAndIdentity`](../internal/room/dsh_recovery_context_test.go) | 0.43 | 6.13 | — | 串 |
| [`TestCalendarRecurringPublicationKeepsTaskActive`](../internal/room/calendar_recurrence_test.go) | 6.13 | 3.50 | 0.24 | 串 |
| [`TestTodosReplacementFailureNeverEnablesBothAccounts`](../internal/room/platform_notification_todos_integration_test.go) | — | — | — | 并；T3 定向 6.06 |
| [`TestDSHRecoveryReadOnlyAnsweredQuestionAndConsumedSource`](../internal/room/dsh_recovery_context_test.go) | 0.40 | 5.71 | — | 串 |
| [`TestDiscussionRestartUpdatesSessionBaselineAndPreservesTotal`](../internal/room/discussion_test.go) | 0.36 | 5.58 | — | 串 |
| [`TestTodosWorkspaceChangesDoNotChangeAccountScope`](../internal/room/platform_notification_todos_integration_test.go) | — | — | — | 并；T3 定向 5.54 |
| [`TestTodosAllProjectsDoesNotDependOnWorkspaceRecovery`](../internal/room/platform_notification_todos_integration_test.go) | — | — | — | 并；T3 定向 5.44 |
| [`TestCalendarAgentCLIDirectClaimAndFinish`](../internal/room/calendar_agent_test.go) | 0.38 | 5.32 | 0.33 | 串 |
| [`TestTodosReceiptsDeduplicateAfterRestart`](../internal/room/platform_notification_todos_integration_test.go) | — | — | — | 并；T3 定向 5.30 |
| [`TestTodosCapacityRejectsWholePage`](../internal/room/platform_notification_todos_integration_test.go) | — | — | — | 并；T3 定向 5.25 |
| [`TestPlatformMemberCurrentConcurrentReservationAndFrozenAdmission`](../internal/room/platform_member_current_test.go) | 5.24 | — | — | 串；T1 5.18 |
| [`TestDiscussionRestartFailureKeepsHistoryAndTeamPaused`](../internal/room/discussion_test.go) | 0.35 | 5.11 | — | 串 |
| [`TestCalendarPersistenceDeliveryAndReportReceiptAfterClear`](../internal/room/calendar_test.go) | 1.36 | 5.06 | 1.31 | 串 |
| [`TestTodosCredentialRotationPausesUntilSameAccountExplicitSync`](../internal/room/platform_notification_todos_integration_test.go) | — | — | — | 并；T3 定向 5.02 |
| [`TestPlatformMemberCurrentStorageFailuresAndRestart`](../internal/room/platform_member_current_test.go) | 4.05 | — | — | 串；T1 5.07 |
| [`TestGitLabCredentialRecoveryKeepsUnrelatedPausesAndIdentities`](../internal/room/platform_gitlab_recovery_test.go) | 3.95 | — | — | 串 |
| [`TestInboxLifecycleActiveTurnBatchAndSweepBound`](../internal/room/inbox_lifecycle_test.go) | 3.90 | — | — | 串 |
| [`TestInboxRecoveryHistoricalStallDrainsPagesWithoutChat`](../internal/room/inbox_recovery_test.go) | 3.82 | — | — | 串 |
| [`TestPlatformMemberToolReceiptsIdentityAndIndependentVersions`](../internal/room/platform_member_test.go) | 3.64 | — | — | 串 |
| [`TestInboxSchedulingIndependentPauseBudgetAndFairness`](../internal/room/inbox_consumption_test.go) | 3.58 | — | — | 串 |
| [`TestGitLabConnectBrowserAndAccountBoundaries`](../internal/room/platform_connect_gitlab_test.go) | 3.29 | — | — | 串；T1 5.44 |
| [`TestLarkAuthorizationRejectsAccountSwitchAndCancellation`](../internal/room/platform_lark_authorization_test.go) | 3.04 | — | — | 串 |
| [`TestInputUpgradeFailsClosedOnInvalidAuthority`](../internal/room/input_upgrade_test.go) | 3.00 | — | — | 串 |

### B.3 `internal/platform/lark`：26 项

第三批已实施；下表保留改造前观察。helper 现有四槽 wait／held 输出，新对照及覆盖承接见 [Lark 协议矩阵记录](implementation/lark-protocol-matrix.zh.md)，不能将历史并行顶层的全部耗时归于 CLI 执行。

| 测试（源码） | 最终核心 | T3 集成 | 收口集成 | 补充证据／并发属性 |
| --- | ---: | ---: | ---: | --- |
| [`TestReadFailuresDiscardCandidate`](../internal/platform/lark/client_integration_test.go) | — | 44.24 | 15.10 | 并 |
| [`TestMessageSearchRejectsIncompleteAndUnverifiedPages`](../internal/platform/lark/message_search_integration_test.go) | — | 42.57 | — | 并 |
| [`TestMessageBatchFailuresDiscardCandidates`](../internal/platform/lark/message_search_integration_test.go) | — | 37.51 | 23.95 | 并 |
| [`TestPinnedReplyTransportSinglePOSTAndCancellation`](../internal/platform/lark/reply_test.go) | — | 28.08 | 10.61 | 并 |
| [`TestSenderNameLookupBoundedAndOptional`](../internal/platform/lark/client_integration_test.go) | — | 28.02 | 13.88 | 并 |
| [`TestMessageBatchNativeDetailsAndOmissions`](../internal/platform/lark/message_search_integration_test.go) | — | 27.05 | 13.56 | 并 |
| [`TestPinnedSendTransportSinglePOSTAndCancellation`](../internal/platform/lark/send_transport_test.go) | — | 26.89 | 10.10 | 并 |
| [`TestImageDownloadIdentityFormatLimitsAndCleanup`](../internal/platform/lark/images_integration_test.go) | — | 23.85 | 14.73 | 并 |
| [`TestPinnedSendTargetNativeEvidence`](../internal/platform/lark/send_targets_test.go) | — | 22.99 | 7.53 | 并 |
| [`TestSearchJoinedGroupMembershipPages`](../internal/platform/lark/search_membership_integration_test.go) | — | 21.03 | 12.67 | 并 |
| [`TestNativeConversationRelationship`](../internal/platform/lark/conversation_integration_test.go) | — | 19.64 | 15.57 | 并 |
| [`TestSelfSendPinnedQueueCancellationAndFinalGuard`](../internal/platform/lark/pinned_integration_test.go) | — | 19.04 | 9.87 | 并 |
| [`TestSenderLookupCannotEscapeAccountGuard`](../internal/platform/lark/client_integration_test.go) | — | 16.38 | 1.44 | 并 |
| [`TestMessageSearchReadsCancelAndShareProfileGate`](../internal/platform/lark/message_search_integration_test.go) | — | 16.18 | 11.09 | 并 |
| [`TestMessageSearchSinglePageContract`](../internal/platform/lark/message_search_integration_test.go) | — | 15.74 | 6.34 | 并 |
| [`TestApplicationSetupRejectsUnconfirmedAndChangedResults`](../internal/platform/lark/setup_integration_test.go) | — | 14.62 | 5.48 | 串 |
| [`TestPermissionCheckReadOnlyAndIndependentDependencies`](../internal/platform/lark/permissions_integration_test.go) | — | 13.40 | 7.57 | 串 |
| [`TestChatSearchNativeContracts`](../internal/platform/lark/chat_search_integration_test.go) | — | 11.60 | 5.18 | 并 |
| [`TestDirectoryCLIPageContractAndIdentityChange`](../internal/platform/lark/directory_integration_test.go) | — | 10.06 | 10.84 | 并 |
| [`TestMessageBatchUsesSingleBoundedBatch`](../internal/platform/lark/message_search_integration_test.go) | — | 9.20 | 2.05 | 并 |
| [`TestImageDownloadCancellationRemovesFiles`](../internal/platform/lark/images_integration_test.go) | — | 7.02 | 8.44 | 并 |
| [`TestChatSearchRejectsInvalidInputBeforeCLI`](../internal/platform/lark/chat_search_integration_test.go) | — | 8.09 | 5.18 | 并 |
| [`TestLoginRequestsSupportedCompleteScopeSet`](../internal/platform/lark/auth_integration_test.go) | — | 6.55 | 3.23 | 串 |
| [`TestCancellationAndProfileGate`](../internal/platform/lark/client_integration_test.go) | — | 6.03 | 0.68 | 并 |
| [`TestSearchBacklogRestartAndDelayedIndex`](../internal/platform/lark/search_backlog_test.go) | 4.84 | 5.86 | 4.79 | 串 |
| [`TestSenderLookupSplitsAtNativeBatchLimit`](../internal/platform/lark/client_integration_test.go) | — | 4.86 | 5.29 | 并 |

### B.4 `internal/inputstream`：8 项

第五批处理；当前包运行被 Room 长执行遮蔽，优先保留持久／进程真实边界。

| 测试（源码） | 最终核心 | T3 集成 | 收口集成 | 补充证据／并发属性 |
| --- | ---: | ---: | ---: | --- |
| [`TestConsumptionAbortHandoffRaceAndBusy`](../internal/inputstream/consumption_test.go) | 5.34 | 19.01 | 4.32 | 串 |
| [`TestConsumptionStreamsBoundsQuotaAndReopen`](../internal/inputstream/consumption_test.go) | 3.67 | 17.30 | 1.96 | 串 |
| [`TestConsumptionReceiptAfterReclaimedCheckpoint`](../internal/inputstream/consumption_reclaim_test.go) | 2.11 | 16.60 | 2.87 | 串 |
| [`TestReclaimIndependentPrefixesAndCrashRecovery`](../internal/inputstream/storage_test.go) | 3.62 | 13.10 | 2.41 | 串 |
| [`TestReadableDirectoryAdmissionSnapshotAndFairExposure`](../internal/inputstream/directory_test.go) | 3.80 | 11.24 | 2.54 | 串 |
| [`TestConsumptionFailureBoundaries`](../internal/inputstream/consumption_test.go) | 2.49 | 10.12 | 1.79 | 串 |
| [`TestFrameworkCallReplayReopenAndReceiptLoss`](../internal/inputstream/calls_test.go) | 0.54 | 6.23 | 0.37 | 串 |
| [`TestLogAppendCrashRecovery`](../internal/inputstream/storage_test.go) | 1.55 | 5.00 | 0.81 | 串 |

### B.5 `internal/agent/dsh`：6 项

第五批处理；当前包运行被 Room 长执行遮蔽，优先保留持久／进程真实边界。

| 测试（源码） | 最终核心 | T3 集成 | 收口集成 | 补充证据／并发属性 |
| --- | ---: | ---: | ---: | --- |
| [`TestResumeFailsClosedWithoutReplacement`](../internal/agent/dsh/recovery_test.go) | 5.42 | 12.59 | 2.29 | 串 |
| [`TestPromptValidationDoesNotDispatchOrDisableSession`](../internal/agent/dsh/prompt_test.go) | 2.71 | 10.51 | 2.53 | 串 |
| [`TestStopReasonsAndFaults`](../internal/agent/dsh/session_test.go) | 2.45 | 7.38 | 1.61 | 串 |
| [`TestBoundCredentialOverridesAmbientAndRetainsRecoveryVersion`](../internal/agent/dsh/credential_test.go) | 0.92 | 7.18 | 0.77 | 串 |
| [`TestConfiguredReasoningEffortMaterializedAndRecoveryBound`](../internal/agent/dsh/reasoning_test.go) | 2.12 | 5.66 | 1.30 | 串 |
| [`TestPermissionPolicyAndUnknownRequests`](../internal/agent/dsh/session_test.go) | 1.45 | 5.51 | 1.25 | 串 |

### B.6 `internal/platform/gitlab`：1 项

主要来自 T3 高负载完整集成的局部观察，先诊断再定优化；不因单项历史慢而修改正式 Git／认证合同。

| 测试（源码） | 最终核心 | T3 集成 | 收口集成 | 补充证据／并发属性 |
| --- | ---: | ---: | ---: | --- |
| [`TestGetProjectUsesNativeIDAndRejectsChangedIdentity`](../internal/platform/gitlab/projects_test.go) | 0.00 | 7.62 | 0.00 | 串 |

### B.7 `internal/platform/scion`：1 项

主要来自 T3 高负载完整集成的局部观察，先诊断再定优化；不因单项历史慢而修改正式 Git／认证合同。

| 测试（源码） | 最终核心 | T3 集成 | 收口集成 | 补充证据／并发属性 |
| --- | ---: | ---: | ---: | --- |
| [`TestScionPATFailuresAndCancelledUnpublishedProfile`](../internal/platform/scion/auth_integration_test.go) | — | 5.87 | 2.25 | 串 |

### B.8 `tools/scion`：1 项

主要来自 T3 高负载完整集成的局部观察，先诊断再定优化；不因单项历史慢而修改正式 Git／认证合同。

| 测试（源码） | 最终核心 | T3 集成 | 收口集成 | 补充证据／并发属性 |
| --- | ---: | ---: | ---: | --- |
| [`TestNativeArgumentsStreamsAndExitCodes`](../tools/scion/main_integration_test.go) | — | 8.62 | 4.10 | 串 |

### B.9 浏览器功能、长窗口与满容量

以下为 T4 独立场景的真实 Chromium 结果，不是全量 e2e 的耗时；普通功能最终证据来自首次选择与必要的定向复验，没有一次干净组合墙钟。整体 e2e 还编入核心／本地集成，真实模型／平台未全跑，不能把下面数值累加成“全仓 e2e”。

| 场景 | 观测秒数 | 推荐处理 |
| --- | ---: | --- |
| `TestHumanReadBrowser` | 16.27；其中 `history-functional` 4.21 | 保持 600 条跨页功能样本；两个 combined 尺寸保留，320px 布局与触摸由目录／reading 承接 |
| `TestHumanReadCatalogBrowser` | 13.65 | 保留独立上下文、迟到响应、目录故障、滚动与原生触摸；已用目标 Room 实际读取屏障修正等待 |
| `TestWebUpdatesBrowserSubscription` | 1.25 | 保留原生连接／认证／HTTP 补读，不优先压缩 |
| `TestWebUpdatesApplicationBrowser` | 15.12 | 保留两种服务、草稿／焦点／后台恢复；60 秒静置已独立 |
| `TestWebUpdatesModelOutcomeBrowser` | 1.54 | 保留模型回执丢失与单次写入证明；受控 probe 不等于真实模型 |
| `TestWebUpdatesAuthenticationBrowser` | 0.64 | 保留会话撤销、草稿保存与无重放 |
| `TestWebUpdatesRestartBrowser` | 6.33 | 保留真实临时 manager 重建、身份更新、双端补读和焦点；不缩成模拟状态变更 |
| `TestWebUpdatesNotificationScopeBrowser` | 34.64 | 当前最慢普通浏览器组；仍含两端各 6.5 秒诊断限频观察等真实页面行为，优先复用已有受控调度证据，需新增实际失败时才进一步收缩 |
| `TestPlatformSelectionConversationBrowser` | 10.34 | 含一次生产 CLI 构建的影响，与下一项共享构建；不当作独立稳定执行预算 |
| `TestDefaultAssistantPlatformOnboardingBrowser` | 5.97 | 两项合并命令 16.465 秒；保留原始账号／群选择、后置 CLI 保存和独立 DM 身份 |
| `TestWebUpdatesIdleBrowser` | 127.62；两服务各 63.68／63.94 | 原 60 秒窗口和生产心跳不可缩短；只在专项显式选择，普通功能不重复观察 |
| `TestWebUpdatesNotificationScopeLongWindowBrowser` | 66.62 | 其中实际观察 60.010 秒、60 轮采集、两端各 70 个诊断帧；保留请求／字节／DOM 延迟口径 |
| `TestHumanReadCapacityBrowser/history` | 31.11 | 实际 10,000 条；保留全部节点、批次／输入／取消／帧／内存判定 |
| `TestHumanReadCapacityBrowser/history-large` | 51.28 | 接近 64 MiB；CDP embedder heap 峰值约 1.97GB，不是 RSS 或低内存承诺。性能或渲染算法修改需另做同条件专项，不能缩容量过门槛 |

三项专项顶层合并墙钟 276.709 秒，包含 82.39 秒容量父项和约三分钟真实观察。尚未在这轮计时的共享满容量、History 容量、旧轮询／frozen／FIFO／工具数量配对、安装环境及真实 DSH／平台重复采样只保留按需入口，不捏造耗时或新性能结论。


### B.10 改造前及 race 的补充观察

下列八个名字未重复列入 B.2–B.8 的四份日志并集，但也曾在这几轮报告较长耗时。race、串行对照与普通并发不能混作同条件样本；并行顶层均可能包含排队。两个已收缩旧名应按覆盖承接检查，不能为了按旧名复跑而恢复重复测试。

| 测试 | 已有观测秒数 | 推荐处理 |
| --- | --- | --- |
| `TestToolDefaultActionIntentSurvivesOmittedJSONFields` | T3 改造前 7.68；T2 race 10.51 | T3 已并入 member 正常接线的空数组／省略字段幂等回执，规范化由轻量测试承接；不恢复独立完整夹具 |
| `TestLarkToolConfigurationReplyDoesNotRequireTargetScopes` | T3 改造前 8.32；T2 race 8.46 | T3 已由 provider 的 intent／scope 规则与代表入口承接；旧名退出，不再重复完整远端 CLI 矩阵 |
| `TestTodosPageSaveFailureIsAtomicAndCommitUnknownLatches` | T3 改造前 5.40 | 第五批：整页原子性／未知提交不能改成内存模型，只减少无关初始化 |
| `TestTodosInactiveBacklogDeliversOnlyAfterItsRoomIsActive` | T3 改造前 5.05；T3 race 5.31 | 第一批后审核私有夹具；活跃状态接线及不提前投递保留 |
| `TestTodosDeletionAndShutdownWaitForQueryOwnership` | T3 race 5.29 | 保留查询持有权、取消与关闭结清；优先看屏障和清理，不减短合法等待换结果 |
| `TestTodosCredentialRotationWaitsBeforeSavingReplacement` | T3 race 7.91 | 保留旧查询先结清、再替换凭据及精确账号复核，按第二／五批边界分析 |
| `TestTodosFailureRestartAndMovingPagesRescanBothStates` | T3 race 5.52 | 分页算法已下沉，真实失败重启及 pending／done 接线继续保留，核对有无剩余重复装配 |
| `TestTodosInitializationFailurePreservesBaselineAndFilterHistory` | T3 race 7.58 | 保留真实初始化失败、基线／过滤历史不变，减少与本故障点无关的准备 |
