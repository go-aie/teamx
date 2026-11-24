# 平台配置规则减装配

2026-11-24，实施测试改造方案 §11.3 的 F1 首组：平台成员配置、选择、连接及直接夹具调用方。只改测试与记录；未改生产规则、构建标签、持久化或并发策略，未进入 F2–F5。

## 覆盖承接

| 原昂贵分支 | 实际承接与减少的装配 | 上层保留边界 |
| --- | --- | --- |
| `TestPlatformMemberNotificationPatchPreservesRangeAndStart` | 保留原测试与生产 `memberNotificationConfig`；固定成员身份、只注册 SearchSource 的未启动通知管理器及受控时间，无 Room／Store／来源消息；基线断言加强为准确 UTC 秒 | `TestPlatformMemberNotificationToggleDefaultsAndPreservedReceipts` 继续证明正式默认配置接线、真实保存和原始接收起点 |
| `TestPlatformMemberLarkRangeAndConditionChanges`、`TestPlatformMemberLarkConflictingIntent` | 保留全部补丁、范围、条件、冲突和输入不变断言；已有配置直接调用原函数，无 manager／service | `TestPlatformMemberLarkCLIConfiguresRangesAndConditions`、私聊人员配置集成继续证明 CLI、原生目标核验、持久回执与成员隔离 |
| `TestGitLabMemberPatchPreservesOtherSourcesFiltersAndStart`、`TestGitLabMemberSourceAndFilterPatches`、`TestGitLabMemberCustomizationAndTotalSwitch` | 构造最小身份／配置，直接调用原生产函数；去掉 OAuth、账号保存、Room 和 HTTP get，省去与规则无关的锁 | 个人 Todos、准确项目配置、通知开关与 unknown 重启测试继续经过真实配置服务 |
| 成员伪造输入、飞书 CLI 歧义与会话类型拒绝 | 三项测试仅装配真实单成员 Room／来源，不安装账号、不运行原生 CLI 核验；CLI 仍走原解析与 HTTP，凡进入 HTTP 必须得到 400，防止身份或凭据错误冒充参数拒绝 | Agent 来源拒绝、浏览器边界、无操作文件及实际成员准入仍检查；成功 CLI／原生权限获取由原有代表流程承接 |
| `connectFixture`／`connectFixtureWithSource` 及其调用方 | 默认三成员改为一个参与成员；没有共享运行中的 Store、manager、日志或缓存 | 保存／重开、账号版本、成员替换、晚到成功、取消结清、unknown 防重等原断言保留；GitLab 选择浏览器的同房间隔离显式创建两个成员 |

六项纯规则测试不再创建真实 manager／Room／service。三项请求拒绝测试取消无关账号与原生权限准备。测试没有删除或移到可选标签；已有规则矩阵与组件证明共同纳入对照，未新增同义规则矩阵。

## 相关族健康度

首轮发现 `TestPlatformSelectionConversationAndMemberConfiguration` 和 `TestDefaultAssistantPlatformOnboarding` 已在人工提交处返回 403：`browserTestServer` 安装了真实访问控制，原 `selectionCall` 却通过另一套组件 Space 上下文进入 manager。该拒绝符合当前生产合同。

测试改为经过同一访问中间件的人工提交，携带实际服务 Origin；独立 DM 先建立，再通过绑定该成员的执行凭据读取和检查回执隔离。保留真实 TeamX CLI 进程、HTTP 服务、原生飞书协议夹具以及等待／Inbox／人工继续三条路径；没有放宽生产授权。独立 DM 的回执拒绝加强为准确 403。

## 验证与计时

环境：macOS ARM64，Go 1.27.1，HEAD `479e79f35157cf15b8f60c793a7840b59d07647c` 加原有工作树。范围由当前连接／选择／成员配置文件族及 `tools_configuration_decisions_test.go`、`ui_update_sources_test.go`、`input_stream_event_delivery_test.go` 自动提取，共 23 份文件、133 项顶层测试；包含已有低层规则、保留核心组件和 integration 流程。

计时使用独立编译的测试二进制，从 `internal/room` 执行 `go tool test2json -t`，指定同一完整名字选择、`-test.count=1 -test.timeout=6m`，前后顺序执行，计入执行、一次生产 CLI 构建和清理。Room 测试二进制编译单列，不算执行收益。改前使用 Go overlay 恢复本轮优化前的八份测试文件，双方共用上述 Space 夹具健康修复；未改动现有工作树来回切换。最早因工作目录不正确而失败的开发命令不进入计时对照。

| 检查 | 结果 | 执行及清理墙钟 |
| --- | --- | ---: |
| 改前，保留全部原装配并共用健康修复 | 133 项顶层、含子项 324 项通过，无失败／跳过 | 112.722 秒 |
| 改后，包含所有规则及保留集成 | 同样 133／324 项通过，无失败／跳过 | 106.854 秒 |
| 同范围 race | 同样 133／324 项通过，无失败／跳过，无竞态报告 | 141.861 秒 |
| 默认／integration／e2e 的 Room 编译 | 通过；没有执行浏览器 | 未计入执行对照 |
| `go vet ./...`、`git diff --check` | 通过 | 未计时 |

本组观察改善 **5.868 秒（5.2%）**，其中首个顶层 run 到首个顶层 cont 的串行跨度为 85.107 → 80.444 秒，首个 cont 到包结束为 26.863 → 25.544 秒。没有增加并发槽、移动测试层级或退役测试，因而没有另计“测试删除／移出日常入口”的收益；并行父项含排队，不能累加为墙钟。

**数十秒的首组投入目标未达，分钟级与全仓预算也未证明。** 六项纯规则原先合计仅约 1.32 秒，真实装配的删除是确定的，整组 5.9 秒则只是单对样本，不能排除普通波动，也不能全归因于本轮改动。改后主要慢项仍为原生权限／目标核验、保存回执和取消结清的真实流程；例如私聊人员 CLI、工具开关并发编辑、OAuth 修复。继续删规则断言不足以消除这部分成本。本阶段停止向 `rotation_state_test.go` 扩展，下一步应按 F2 首族检查进程成本，不在 F1 引入编译 CLI 或继续收缩恢复矩阵。

原始本机日志在 `/tmp/teamx-core-rules/`，选择范围、逐项顶层结果及源码摘要见机器证据（本地验收文件 `platform-configuration-rules-evidence.json`）。该文件是本轮快照，不是永久手工测试名单；临时目录可清理。

未运行完整核心、全仓 integration、全仓 race 或真实平台／模型／浏览器验收；核心三分钟与完整集成八分钟预算留待 F5 统一评价。
