# Lark 夹具的 provider 与成员最小装配

对应[测试改造方案 H1](../TEST-SUITE-REFACTOR-PLAN.zh.md#103-h1先消除共用夹具里的额外-provider-与成员)。首组 Lark 自发消息、通知、成员与工具配置调用方已迁移，真实提交、未知结果、防重、撤权、取消结清与重启断言保留。**装配工作减少，但整族首轮对照没有证明提速，也未达到数十秒收益目标；按方案停止向其他 provider 族扩散。** 本记录截至 H1；后续 H2 见[核心测试夹具记录](core-test-fixtures.zh.md)，H3–H5 未实施。

## 改动与覆盖承接

`toolRoomFixture → managerCreateWithMembers` 只创建私有 manager、目标数量成员的 Room 和真实 service，不创建账号或假工具。`newLarkHarnessWithMembers` 在此基础上显式安装 Lark，继续调用生产 `verifyPlatform` 和真实 CLI 子进程。模型配置保留 Room 创建所需的一份测试配置，不开启真实模型。

原 `managerCreate`、`cliToolFixture`、`newLarkHarness`、`selfSendHarness`、`newLarkSearchHarness`、`platformMemberHarness` 及 `larkToolConfigurationHarness` 的默认多成员合同保留；新调用方显式选择人数。需要原 GitLab／多 provider 组合的其他调用方继续使用原夹具。成员工具的单账号夹具取消了“先创建 GitLab，再从 registry 删除”的绕路。已无调用方的旧 `larkOnlyToolConfigurationHarness` 合入带人数的配置夹具。

| 原测试／断言 | 当前承接与装配 | 保留的真实边界 |
| --- | --- | --- |
| `platform_lark_self_send_integration_test.go`、恢复文件中的进程重建、ledger 四种写入失败、绑定证据更新、五种撤权竞态 | 单成员 Lark 夹具；业务断言原位保留 | 真实本地 CLI、POST 次数、原 ID 重放、未知结果禁止新 ID 重发、跨进程重启、精确账号与凭据版本、取消结清 |
| `TestLarkSelfSendConcurrentRoomsAndMembers` | 第一 Room 两成员，第二 Room 一成员，共三名实际参与者；原两个三成员 Room 退出该场景 | 共享账号队列、九次并发提交仅三次 POST、三个 membership 独立 ledger |
| 通知收集、提及、回填、搜索、ack 崩溃、多 Lark 账号及后台轮询 | 单成员 Lark；第二账号只由多账号场景显式创建 | 原生身份核验、通知持久状态、账号隔离、投递回执、防重、后台生命周期与重启 |
| `TestLarkNotificationsInboxReferencesAndRoomQuestion`、`TestLarkNotificationsRestartImmutableTargetAndDeliveryCapacity` | 两成员 Lark 夹具 | 另一成员不能借用读取权限、既有订阅不能换接收者、真实 Inbox 容量及重启 |
| 成员 group/private 配置的 CLI、撤权、未知提交与重启 | 普通流程一成员；两个 CLI sibling binding 场景、订阅 patch 和 private CLI sibling 隔离共四项保留两成员 | 真实 CLI 参数／回执、原始 baseline、兄弟成员绑定／订阅不变、核验期间账号及成员变更拒绝、落盘与重启 |
| `tools_lark_configuration_test.go` 的 HTTP/member/onboarding、原生证据和授权复核；`memberToolFixture` 的所有调用方 | 显式一成员、仅 Lark；另有账号的 picker 用例仍显式创建第二账号 | 三入口、实际 scope 核验、保存前复核、操作回执与未知结果、账号及成员身份 |

没有删除业务测试或缩减子场景矩阵，也没有新增同义断言。共用 fixture 的成员数量由调用方选择；原需要两个成员的七项检查仍使用真实不同 membership。Workspace、其他账号、通知订阅由原测试流程按需创建，没有共享 Store 或已打开 manager。四槽上限、正式同步、授权检查、定时参数和 build tags 保持原值。

## 整族对照与分段成本

同一 54 项顶层选择，`integration`、`-count=1`、默认 Go 并发与现有四槽，顺序运行改前和改后。两边均为 **53 项顶层通过＋1 项 subprocess helper 跳过；含子项 95 通过＋1 跳过**。跳过的是 `TestLarkSelfSendRecoveryProcess` 的普通入口，其实际进程路径仍由恢复测试启动。

| 指标 | 改前 | 改后 |
| --- | ---: | ---: |
| 命令墙钟，含构建（秒） | 106.200 | 112.820 |
| Room 包执行（秒） | 93.927 | 102.613 |
| 全部 58 条夹具记录累计 held（槽秒） | 307.404 | 298.650 |
| 自发消息恢复族九条记录累计 held（槽秒） | 130.429 | 135.628 |
| GitLab 账号创建／核验及无关 fake-tool 文件 | 各 82 次 | 0 |
| Lark 账号创建／生产核验 | 85 次 | 85 次 |
| Lark CLI 启动次数 | 5,763 | 5,763 |
| 初始成员持久材料 | 258 个 | 93 个 |
| Room 创建 | 86 次 | 86 次 |

成员数量按实际夹具实例及各调用方人数核对：改前 85 个三成员 Lark Room，加一个三成员跨 Room；改后 78 个单成员、七个双成员 Lark Room，加一个单成员跨 Room。该数字表示初始 membership 材料，不能解释成少启动了 165 个 DSH／模型进程。

临时计时先加入旧 helper，再用于改后相同族；计时和 native trace 已从最终源码移除。累计分段如下，各段及 held **不能相加当墙钟**：

| 分段 | 改前次数／累计秒 | 改后次数／累计秒 |
| --- | ---: | ---: |
| manager 初始化 | 95／3.024 | 95／2.995 |
| Room 创建 | 86／13.582 | 86／10.485 |
| 无关 GitLab 账号保存及核验 | 82／3.083 | 0／0 |
| 必需 Lark 账号保存及原生核验 | 85／51.634 | 85／48.164 |
| 目标流程至 fixture cleanup 前 | 85／244.765 | 85／246.925 |
| 注册的 manager cleanup | 95／1.129 | 95／1.113 |

Lark 核验分段包含真实进程成本；native trace 只统计一次实际 Python fixture 启动，不用 Python 函数体耗时冒充完整进程墙钟。显式关闭／重建及测试子进程清理仍可能计入目标流程。service 的临时计时改前覆盖 82 个旧 CLI 夹具、改后覆盖全部 85 个，旧三个纯 Lark 入口未单独计时，因此不把 service 合计当成配对提速证据。

这次对照不能证明整族速度提升。无关 GitLab 核验本轮只占约三槽秒，成员数减少只让 Room 创建累计少约三秒；主要成本仍在必需 CLI 核验及目标流程。工作区另有并行开发和外部测试进程，未建立严格受控的外部负载环境，也未证明某个后台进程、磁盘或 `fsync` 是波动原因。不会通过删减必需身份核验、扩大四槽、降低持久性或改写未知结果断言追求数字，也不将局部 held 下降外推为全仓收益。

## 验证与剩余边界

| 检查 | 结果 |
| --- | --- |
| 正常四槽整族对照 | 改前／改后均为 53 顶层通过、1 helper 跳过；含子项 95 通过、1 跳过，所有 96 个测试节点的结果一致 |
| 同一选择 `-parallel=1` | 53 顶层／含子项 95 通过、1 helper 跳过；命令墙钟 369.022 秒，包执行 357.844 秒 |
| 同一选择 `-race -tags=integration` | 53 顶层／含子项 95 通过、1 helper 跳过，无竞态报告；命令墙钟 131.482 秒，包执行 109.979 秒 |
| 默认、integration、e2e 全仓 `-run '^$'` | 三种编译均通过，墙钟分别 10.183／4.874／13.390 秒；没有执行真实 e2e |
| 旧 GitLab CLI 多成员 helper 的实际调用方 | `TestExplicitToolBindingPreservesRolesAndReadsSkillWithoutCredentials` 通过；2.397 秒墙钟 |
| 共享工作树的旧自发消息 helper，unified input 分支 | `TestLarkImagesDispatchGuards/log=true/input` 通过；9.338 秒墙钟。保留同文件现有的 `selfSendHarnessWithInputLog`，其参数继续传给 Room 配置 |
| 原断言审查 | 对 69 个既有测试函数体排除夹具调用／人数参数后比较，零业务体变化、零测试删除 |
| `go vet ./...` | **未通过**：现有未跟踪文件 `internal/room/room_action_images.go:21` 的 `ServiceInfo`、`MemberIdentity` 重复嵌入 `room_id`／`room_instance` JSON tag；本阶段没有修改该生产文件 |

临时计时已移除；最终源码通过单并发与 race 对应流程及上述编译检查。精确选择、逐项结果、临时埋点、分段与源码摘要见[机器可读证据](provider-fixtures-evidence.json)。

本阶段不重复完整核心／integration／全仓 race，也未运行真实模型、平台或浏览器。已有完整健康基线按方案复用；核心三分钟和完整本地集成八分钟预算均不能由本次结果宣布达成。后续应依据 H2／H3 检查实际重复工作与长持槽业务组合，不能把 H1 的初始化删除数量当作分钟级提速成果。
