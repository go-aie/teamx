# 模块边界重构基线

日期：2026-11-19。对应[架构方案 R0](../../ARCHITECTURE-MODULE-BOUNDARIES-ANALYSIS.zh.md#113-r0固定实施基线)。R0 已完成基线采集、调用入口与 owner 清单、依赖约束及验证记录；R1–R8 尚未实施。

## 实施单与快照

| 项目 | 本次范围 |
| --- | --- |
| 范围 | 受管／显式单 Room 启动、正常与保留工具入口、History H1–H4、后续切片的源码／测试和恢复不变量 |
| 状态归属 | 仅记录和验证，不转移 owner、字段或持久记录；不运行数据升级 |
| 跨模块合同 | 固定当前合同与第 10.3 节的 import 方向；复用既有平台及 Workspace 边界测试 |
| 接入与清理 | 增加只读采集脚本及测试包，正常调用不切换；没有新服务、alias 或业务接口 |
| 停止点 | 后续阶段可据此定位源码和对照结果；失败单独记录，不通过改业务逻辑或放宽检查修复基线 |

实施起点 HEAD：`633a0f2fbcd237c0e9a68f5ba13dc93ff0b68d78`（`feat: add unified agent history`），并非首次架构分析的 `a6560c7`。环境为 Go 1.27.1、darwin/arm64；完整环境、工作区状态、源码 SHA-256、build tags、测试名称、生产 import 图及分组引用见 baseline.json（本地验收文件 `baseline.json`）。

起点已有 tracked diff 仅 `docs/implementation/README.zh.md`，增加 50 行。架构方案、多份方案／实施记录、三个 Room 测试／测试资源及运行目录已有 untracked 内容；JSON 保存名称和源码摘要，不读取运行目录内容。现有未跟踪测试也属于这次工作树测试基线，不可把结果称为“纯 HEAD 检出结果”。本次增量是本记录、采集脚本、架构测试、平台边界测试补缺、证据及两处文档状态／索引更新，原有改动保留。最终机器快照在 R0 约束与文档更新后采集，`production_go_diff` 应为空；它的 tracked diff 包含本次测试改动，不冒充起点原始 diff。

从仓库根目录采集新的对照文件：

```sh
python3 scripts/architecture-baseline.py /absolute/fresh-baseline.json
go test ./internal/architecture ./internal/platform ./internal/workspace -count=1
```

输出必须是新文件。`files` 固定参与采样的源码与测试内容，`production_packages` 是当前构建平台的实际图，`contract_searches/references` 提供下文每条工作线的逐文件／逐行检索入口，包含声明、真实调用及生成绑定。引用是词法搜索，**不是经过类型解析的调用图**：同名 receiver、生成转发和测试 helper 须结合源码区分。移植前先按摘要检查漂移，再根据所选操作补充其实际符号；不能将宽泛类型搜索命中直接视为迁移 allowlist。

## 正常入口与保留入口

| 入口 | 当前路径与语义 |
| --- | --- |
| 进程／CLI | `cmd/teamx/main.go → room.Run → serve/runServer/runServerOnce` 或 `runCLIWithInput`；`service` 进入服务管理，`init/doctor/bundle` 有自己的分支 |
| 默认受管服务 | `runServerOnce` 未显式指定 `--room/--state-dir` 时走 `serveRoomManager → openTeamRoomManager → roomManager`；目录和团队配置来自 startup config，正常构造启用 unified inputs |
| home／安装根 | `serve --home`／`--server-root` 经布局、home 锁、访问／模型准备后进入同一受管服务；不能与显式 config／单 Room flags 混用 |
| 显式单 Room | 指定 `--room` 或 `--state-dir`，`create` 默认是 `newService`；默认 state-dir 仍按现有代码取 `<data_dir>/design-review`。`--dsh-config` 仅该模式支持 |
| HTTP | `serverAccess.wrap` 挂接操作者／执行身份，manager 或 service 分发；原生 Room 读取／确认、管理查询、业务动作、用户阅读旁路各保留自己的合同 |
| 正常 Agent 输入 | `dshBackend/dshSession → newInputSession → issueInputBinding`，以及 `issueMCP/serveMCP` 的 unified 分支；两条凭证路径均按 `inboxMode` 选择 `inputMCPFor` |
| HTTP Inbox 模式 MCP | `newHistoryMCPHTTP(false)`：`inbox_list`、`inbox_read`、`history_list`、`history_read` |
| CLI Inbox 模式 MCP | `newHistoryMCPHTTP(true)`：`history_list`、`history_read`；正式输入由 `TEAMX_INPUT_CONTRACT` 选择统一 Inbox CLI |
| 保留兼容传输 | `newMCPHTTP/newInputMCPHTTP(false)/addInboxTool` 及非 unified `mcpExecution` 分支仍存在，主要供旧合同和显式 fixture 路径使用。不能按名称将其一个 `inbox_read` 工具集当作默认生产工具集；删除前须审计支持范围 |
| 已删除入口 | `teamx read` 已返回 unknown command，无 `--all` 兼容分支，也无 History CLI；原生 Room HTTP／恢复预览仍在 |

工具名称、参数 Schema 和 framing 以 `input_stream_transport.go`、`history.go`、`history_mcp.go`、`mcp_transport.go`、`mcp_access.go` 为权威；本次源码摘要固定这些文件。工具发现不是模型已收到新工具集的证据。

## History 合同与证据层级

| 合同 | 固定行为与源码／测试 |
| --- | --- |
| 保留可见性 | Room、私有和共享本地已保存内容；包含已消费／跳过／未消费，不远端补采，不创建事项或来源关联。`history.go`、`inputstream/history.go`；`TestHistoryRoomVisibilityFrozenPagesAndNoWrites`、`TestHistoryConsumedSkippedPendingAndNoMatters` |
| 身份与权限 | 复用正常流引用；Room 按成员可见性，共享按当前 Grant 及冻结接收集合核验，重加不继承旧记录。`TestHistoryDirectorySnapshotIdentityAndPagination`、`TestHistorySharedGrantRevocationRejoinAndFrozenRecipients` |
| 快照与续页 | 先选最近窗口、页内按时间正序，仅 cursor 向更早内容续读；新追加不插旧页，清空／回收使旧快照失效。`TestHistoryExpiryClearRestartAndStableStreamReference`、`TestHistoryReclamationInvalidatesReadAndDirectorySnapshots` |
| 预算与缓存 | 目录 64 KiB／页、正文 256 KiB／页、执行回合累计文字 512 KiB；独立图片 4 张／4 MiB；最多 4 个快照、空闲 5 分钟。同调用 ID 重放不重复计费，改参拒绝；传输重连不能刷新额度 |
| 图文与账号 | `historyReadMaterials`、`attachmentRange` 接原材料下载器和成员账号核验，维持 `content_index`／省略原因、材料范围及图片缓存。`TestHistoryMaterialsUseExistingDownloaderAndMemberAccount`、`TestHistoryMCPImagePositionsDedupAndIndependentBudget` |
| 输出与撤销 | pin／成员读租约覆盖输出；最终核验与 handoff 继续和撤权串行。`issueInputBinding` 撤销先删除准入、取消、等待 active，再 `discardHistoryResults`，不退回已用额度；`finishMCPTurn/closeInputOwners` 才清整个回合实例。`TestHistoryMCPRevokeAndCloseWaitForOutput`、`TestHistoryMCPCancelDownloadReleasesPinsAndCache` |
| 恢复 | 有界 Room 预览带 `room_stream_ref`，已消费也可预览；失败首次回合可重供，成功后不反复给。History／预览不消费，不把重复事实当新任务；unknown 动作查原账本。`recovery.go`、`dsh.go`、`TestHistoryRecoveryPreviewAndFormalDelivery`、`TestHistoryGuidanceAndBackendModes` |

功能实施的历史事实保留在 [H1](../agent-history-h1.zh.md)、[H2](../agent-history-h2.zh.md)、[H3](../agent-history-h3.zh.md)、[H4](../agent-history-h4.zh.md)。H1/H2 的“未注册”、H3 的隔离开关是当时事实；当前正常注册以 H4 和源码为准。H3 的真实平台图文、H4 的真实 DSH 默认服务路径来自已有记录及 H3 摘要（本地验收文件 `agent-history-h3-evidence.json`）／H4 摘要（本地验收文件 `agent-history-h4-evidence.json`），本次没有重跑真实模型、平台或浏览器。

运行态只读观察：本次观察到 PID 28389，启动于本地时间 2026-11-19 23:09:50，命令 `./bin/teamx serve --config .teamx/config.toml`；磁盘 `bin/teamx` 的 `go version -m` 为同一 `633a0f2…`、`vcs.modified=true`。这比旧 H4 的“尚未切换”记录更新，但磁盘元数据和进程命令不能证明进程实际加载字节、更不能证明每个现有 Session 的工具发现／采用。本次未请求其受保护接口，未重启、替换二进制或轮换 Session；目标服务的 tools/list、图文、消费与恢复仍需另行验证，不能据 R0 宣称部署验收完成。

## 状态 owner、持久布局与健康域

以下 `D` 是 manager 的 `data_dir`，`R` 是某个 Room 的 Store 目录（受管模式 `D/rooms/<roomID>`），`H` 是配置文件所在目录；MembershipID／RoomInstance／Grant 均不能用展示名称替代。

| 状态 | 唯一 owner／同步 | 现有持久位置或瞬态边界 |
| --- | --- | --- |
| Room 目录、运行与管理进度 | `roomManager.mu`；`runtimes/index/busy/operationDone`，只读 catalog 原子发布 | `D/rooms.json`、`R/room-config.json`；运行观测不写 rooms.json |
| 公共正文／成员／原生消费 | `Room` Actor 提交，Store 保存；`Store.mu` 保护本地发布与索引 | `R/room.json` 唯一发布根，`R/room-input/records/*.jsonl`；索引与 pin 不成为第二发布根 |
| 私有正文、正式结果、消费 | `MemberInputActor → platformInputStorage.repository` 串行写；`inputOwners` 管登记与关闭，读 lease 不能绕过 | `R/inboxes/<membership>/<provider>/{partition.json,records/*.jsonl,consumption.json}`；`R/agents/<membership>/{input-state.json,input-results/*.json,capacity.json}` |
| 共享正文与 Grant | `InboxActor/sharedInputStorage`；已提交 view 原子发布；成员消费仍由其 MemberInputActor 管 | `R/shared-inboxes/project/partition.json` 与 records；`R/agents/<membership>/shared-input/project.json`；正文只存一次 |
| 旧 Inbox | `AgentInbox` 的旧状态／回收流程；不是新 Repository 的第二 writer | `R/inboxes/<membership>/inbox.json` 等旧布局；保留的升级／对照入口不得混作正常新正文 |
| History 查询 | `platformInputStorage.history/historyMu/historyHandoff`，供同一 execution 的 inputSession 复用 | 仅内存查询、cursor、重放、图文预算和短期 pin；R2 才改变归属 |
| 成员／调度／Session | `MemberAgent` 拥有执行状态；`turnScheduler` 拥有排队与 grant；`dshBackend/dshSession` 适配具体后端 | `R/agents/<membership>/agent.json`、`session.json`、`session.json.dsh-*/`、轮换／回合记录；DSH 的 backend.json／ACP SessionID 归组件 |
| 通知来源 | `notification.Manager` 装配，每 Room `NotificationActor` 串行提交；目标持久收件与来源确认分两次 | `R/notifications/`；`notification-queries/` 是查询工作目录；DeliveryID 幂等承接确认丢失 |
| 平台连接、绑定、OAuth、调用 | `registry.Manager` 保存连接；应用操作仍在 manager 门槛下；`platformCalls/oauthFlights/oauthLeases/*AuthSessions` 不复制 | `D/platform-connections.json`、`R/tool-bindings.json`；在途调用取消与等待、token lease 仍由原 manager 结清 |
| Followup／Policy／Action | Store 受控业务命令和既有锁保存，各记录独立于输入消费 | `R/agents/<membership>/followups/`、`policy.json`、`actions/records/*.json`；unknown 不能换 ID 重放 |
| 用户已读／提及 | `humanReadStore` 独立同步与 writer | `R/human-read.json`；其故障不冻结 Room 业务写入，不合并 Agent 游标 |
| Calendar | `CalendarService` 装配的每 Room `CalendarActor`，调度／交付由原服务协作 | `R/calendar/state.json`；交付回执与幂等须跟随完整 owner 生命周期 |
| Model／Workspace／Access | `modelService/modelController`、manager 的 workspace 操作、`serverAccess` 分别拥有；请求鉴权与业务服务分开 | `H/model-credentials/`、`model-validations/`、`model-operations/`、迁移 journal／backup；workspace 配置／进度在原 Room config／目录索引；`H/auth/browser-sessions.json` 等访问记录 |
| 文件健康与目录锁 | 每个 Store 内嵌 `snapshotWriter`；`healthMu` 还保护 Store.lock；`closeMu` 串行关闭 | `.lock` 只解锁不删除；home `.bundle.lock` 是另一个句柄。不同 Store、human-read、配置／访问 writer 继续保持原有实例／健康域 |

R1a 需同时保留 `save/removeSnapshot/admitSnapshotWrite/check/markUncertain` 及 `healthMu/uncertain/writerClosing/writes/writesDone` 的作用。排队者拿到路径槽后再次检查健康；rename／remove 后目录同步失败是提交未知；非快照 Repository 与旧 Inbox 的未知提交必须继续冻结同域 Store，读准入不能被一律改成写准入。原 snapshotWriter 构造点还在 `management.go`、`server_access.go`、`first_access.go`、`server_setup.go`、`model_service.go`、`model_management.go`、`model_migration.go`、`platform_model_upgrade.go`、`input_upgrade_legacy.go`、`platforms_upgrade_cli.go`、`notifications_upgrade_cli.go`；迁移不能只替换 Store 内嵌类型而遗漏独立实例。

## 关闭与结清顺序

1. `runServerOnce/serveRoomManager` 保持 HTTP／监听器所有权，先进入业务关闭，再 `server.Shutdown`；不是先等待 HTTP 再阻止调度。根 `roomManager.close` 还会关闭 updates、停止 modelController，再调用带超时的 shutdown；正常 serve 的显式 shutdown 与 defer close 共用原 owner。
2. manager 的 `shutdown` 先取消 calendar、关闭 updates、取得 shutdown gate，`mu` 下置 closing、freeze 全部 service、发管理／授权／OAuth／平台调用及来源取消信号；等待均在 manager 锁外。各 Room 关闭、调用等待、来源等待及 lifecycle worker 并发结清；某个 Room 失败不能阻止其他 Room 收到 stop。
3. `service.close` freeze，等待已准入 changes，quiesce calendar 与调度 grant，发成员 settle，再等待 management、成员 Stop/Wait、calendar retire、旧 Inbox、scheduler、Room、Actor runtime 和 input owners。最后 cancel service 并 `Store.Close`。锁等待／超时仍保留 owner，不写成功回执冒充已关闭。
4. `Store.Close` 先 `closeInputOwners`（含读 lease、查询清理和 owner/runtime 结清），再设 writerClosing、等待 writesDone，最后解锁目录。结清前不能重开。manager 汇总关闭回执，失败保留根所有权；成功后关闭共享 CalendarService 和根 Store。

R6 保持 `manager.mu` 下核验、注册、绑定变更与最终证据检查的同步关系；取消只发信号、`notification.Wait/waitPlatformCalls` 在锁外。History 图片下载不占输出 handoff，输出保护不持有 Store 锁；这些关系不能为了抽包改变。

## 后续职责、全部调用点的检索入口与现有测试

表中列出正常入口及交叉生命周期调用族，逐行引用和测试函数全集见 JSON；`*.gen.go` 只是绑定，移动 Actor 时须重新生成。阶段不是文件 allowlist；同一文件可同时含多种职责，尤其 `service.go/management.go/storage.go`。

| 切片／JSON 引用组 | 源码与调用族 | 对照测试／恢复门槛 |
| --- | --- | --- |
| R1a/b `persistence` | `storage.go/storage_writer.go/storage_lock_*.go`；所有 Store 保存／删除、inputPersistence、未知提交上报、上列独立 writer、Store／service／manager／bundle 关闭 | `storage*_test.go`、`input_stream_storage_test.go`、`human_read*_test.go`；`TestSnapshotWriterQueuedPathRechecksHealth`、`TestSnapshotWriterCloseDrainsAllAdmittedWrites`、`TestNotificationConfirmationRechecksSharedWriterHealth`、`TestPlatformInputUnknownCommitFreezesOwningStore`、`TestDirectoryOwnership` |
| R2 `history` | `history.go/history_materials.go/history_mcp.go/input_stream_history.go`；正常 MCP 两凭证路径、直接 historyCall、mcp_turn／member_input／mcp_access 清理、恢复指引 | `history*_test.go`、`inputstream/history_test.go`；上文固定合同、输出撤权／取消／预算／pin、其他成员继续追加消费回收 |
| R3a `platform_entries` | `tool_entries.go`；`tools.go/tools_http.go/tools_builtin.go`、`platform_attachments.go`、GitLab／Lark 原生引用／动作、followups、通知来源账号关联；纯解析与 registry I/O／准入必须分清 | `tool_entries_integration_test.go`、`tools_target_concurrency_test.go`、平台附件／原生引用／动作测试；精确 entry、同 provider 多账号、歧义、凭据代际和来源已退休 |
| R3b `platform_assembly` | `platform.go` 的 provider／工具／通知分开注册；`platform_references.go`、`inputStorageCodecs`、tool catalog／可用性／装配验证 | 平台／Workspace 既有 import 测试、`TestInputStorageUsesStaticSourceAssembly`、`TestThirdNativeSourceAndAdditionalChannelUseSameStorage`；无 codec、工具缺省、额外 channel |
| R4 `input_owners/input_execution` | `member_input.go/shared_inbox.go/shared_input_reader.go`、`input_stream_*.go`、`mcp_turn.go`、`inbox_*` 兼容路径；通知持久投递、Grant／消费、正式页／框架证明、回收、退出／关闭 | `member_input_test.go/shared_inbox*_test.go/input_stream*_test.go`；准入背压、读 lease、共享撤权、回执恢复、结清前不换 owner；bridge 的收到证明不能改成 flush |
| R5a `scheduler` | `turn_scheduler.go/scheduling.go`；`member.go` 绑定／request／release／exit，`service.go` spawn／quiesce／wait，`runtimes.go` stopRoomGrants，重建／关闭 | `turn_scheduler_test.go/parallel_scheduling_test.go`；精确 grant、失败重供、取消后持槽、成员退出、各 Room 隔离 |
| R5b `members` | `member.go/session.go/dsh.go/rotation.go/rotation_state.go/preparation.go` 与恢复／模型／activity／状态查询；后端私有状态解释、open/resume/cancel/close/settle 均纳入 | `member*_test.go/rotation*_test.go/automatic*_test.go/dsh*_test.go` 和 DSH lifecycle/recovery；模型／预算策略不变；真实 DSH 退出／恢复另按切片验收 |
| R6a `platform_calls` | `platform_call_lifecycle.go`；tools run、附件、GitLab／Lark 原生读取／动作，以及 projects/chats/capabilities、自发消息配置、OAuth、connect、Workspace Git／Scion 等直接 platformCalls 登记族；`runtime_shutdown`／成员移除／绑定撤销统一取消结清 | `tools_integration_test.go`、`tool_entries_integration_test.go`、附件并发与 provider 原生测试、`runtime_shutdown_test.go`；不能只迁 preparePlatformCallLocked 而漏掉直接登记 |
| R6b `platform_configuration` | `platform*.go/tools_lark_configuration.go`；HTTP／CLI／onboarding／成员订阅、通知配置及恢复，OAuth flight／lease、auth session／撤销／关闭共用现有 owner | `platform_connect*_test.go/platform_oauth*_test.go/platform_*auth*_test.go/platform_member*_test.go/platform_notification*_test.go`；每次指定 provider＋操作，丢失应答不能重做授权或复制配置 map |
| R7 `followups` | `followups*.go/policy*.go/actions*.go/action_execute_service.go`；业务查询、进度／策略更新、受管动作、输入来源关联、恢复账本 | 对应同前缀测试、`agent_contract_integration_test.go`；消费不等于办结，来源／账号／目标及 unknown 不重试 |
| R7 `models/workspaces` | `model*.go/workspace*.go`；HTTP／CLI／设置、模型验证／应用／恢复及 backend 选择；仓库准备／校验／绑定／操作恢复 | 同前缀测试、`models_test.go`、`workspace/boundaries_test.go`；凭据隔离、运行中切换、目录保护、部分失败与结清 |
| R7 `calendars/access` | `calendar*.go/access*.go/server_access.go/mcp_access.go`；定时调度／Room 发布、权限发放／使用／撤销／输出等待，独立持久和关闭 | `calendar*_test.go/server_access*_test.go/mcp*_test.go`；交付幂等、停机恢复、旧 token、在途输出；请求 filter 与应用访问状态分开 |
| R8 `conversation/assembly_transport` | `room.go/state.go/protocol.go/message_mentions.go/room_input*.go/human_read*.go`；`management.go/runtimes.go/runtime_*.go/service.go/serve.go`；各 HTTP/MCP/CLI handler／Schema／help 与生成绑定 | Room 原生时间线／读证明、human_read 故障隔离、`runtime_shutdown_test.go`、`service_deployment_test.go`、server／CLI／MCP 测试；按完整运行操作和调用族接入，最终才删旧转发 |

这些是切片进入前的导航与风险边界，不代表已决定迁移整个前缀。具体 provider 操作、所有可变字段／锁／关闭责任与新旧调用切换，仍按方案第 11.2 节写在各切片实施单中；R0 不替未来 owner 迁移预建接口。

## 依赖检查与验证结果

既有 `TestCapabilityImportBoundaries` 和 `TestRepositoryImportBoundaries` 继续保护平台公共合同／provider／工具进程及 Workspace；本次为前者补齐 GitLab 不得反向依赖 Lark 的对称约束。新增 `internal/architecture/boundaries_test.go` 检查**生产传递依赖**，允许测试使用 frozen fixture，但禁止实际产品包依赖 `inputstreamtest/frozen/testutil`，保护底层不能反向 import room／应用／入口；已声明的 app 子服务禁止依赖 app 根装配与入口，并保护 History 不依赖 inputs、Inbox 不依赖 agents。规则覆盖当前真实底层包及后续实际出现的包，不创建占位生产模块。

本次命令、退出码、日志摘要与摘要哈希见 validation.json（本地验收文件 `validation.json`）。完整本地日志在 `.local/architecture-baseline/`；全量基线和针对性验证分别报告，后者通过不抵消前者失败。

| 检查 | 本轮结果 |
| --- | --- |
| 架构／平台／Workspace 边界 | 通过；最终约束另在 owners-entry race 组合中通过 |
| History／writer／grant／关闭／正式证明 race | 通过，Room 约 55 秒；inputstream 通过，DSH 包仅编译且无匹配测试；bridge 本地合同另由 Node 测试覆盖 |
| 来源装配／精确 entry／成员 owner／调度／human-read race | 通过，Room 约 59 秒；包含非快照输入未知提交冻结同 Store、用户阅读故障隔离 |
| 生产 CLI 构建、全部包 e2e 编译 | 通过，产物仅 `.local/architecture-baseline/teamx`；`-run '^$'` 不执行真实验收 |
| Web＋输入 bridge | 772/772 通过，本地 JS 组件证据 |
| 打包 inventory | 通过：4 targets、4 teams、724 locked npm packages |
| `make test` | 失败：Calendar 断言及 Room 默认 3 分钟总超时；其余包通过，部分命中缓存 |
| `make test-integration` | 失败：Calendar 与四项 DSH／升级断言及 Room 默认 8 分钟总超时；其余包通过 |
| `node --test scripts/*.test.mjs` | 88/89 通过，安装指南导出后的仓库相对链接失败 |
| `go vet ./...`、integration vet | 均失败，已有 12 项非具名外部结构体字面量告警；补充的 `-composites=false` 通过，不能替代标准 gate |

未解决失败固定如下，均未修改生产逻辑、测试预期、超时或 CI gate 来掩盖：

1. `TestCalendarManagedRecurringPauseRecoveryAndPublication`：`calendar_lifecycle_test.go:201`，`controlled model turn did not start`。默认／integration 均出现，独立复跑仍失败；该复跑 7.205 秒内正常结束，没有包超时。
2. `TestUnifiedDSHPreservesReceiptFailureDiagnostic`、`TestUnifiedDSHDirectoryRecoverySupplyAndContract`、`TestUnifiedDSHDirectoryCannotBypassDurableDiscoveryBudget`：`input_stream_session_test.go:46/90/226`，`snapshot identity mismatch`。四项 DSH／升级专项独立复跑中全部重现。
3. `TestInputUpgradeRotatesSettledDSHContract`：`input_upgrade_dsh_integration_test.go:152`，轮换后的实际 tools/list 包含 Inbox＋History 四工具，而测试旧断言拒绝该集合。这是需要后续校准的已有测试合同，不能据其失败回退默认 History 注册。独立复跑与上三项共 2.437 秒，没有包超时。
4. Room 全量包的 3／8 分钟总时限均触发；超时时分别正在 `TestHumanReadInvalidLegacyFileIsNeverConverted/future` 和 `TestPlatformMemberCurrentConcurrentReservationAndFrozenAdmission/gitlab/member`。这是累积包时限的观察，不能仅凭栈把当前测试诊断成死锁；前者独立复跑及整组 human-read race 均完成。
5. `scripts/package-publish.test.mjs:153` 的安装指南独立导出检查失败：未改动的 `docs/installation.zh.md:152` 仍引用 `workspace.zh.md#选择已有目录`，导出后无法独立导航。
6. 标准 vet 的 12 项告警位于已有 `input_stream_fixture_test.go/input_stream_identity_test.go/input_stream_snapshot_test.go/ui_updates_http_test.go/ui_updates_test.go`，完整位置见 JSON。

全量 core／integration 初跑互相重叠，可能影响耗时；已将断言失败独立复跑，仍保留首次超时事实，不把定向通过升级成全量通过。R0 的完成条件允许记录未解决失败；后续切片须与这些具体失败逐项对照，新增失败仍是回归，修复这些基线问题则应明确另计范围。

R0 完成不等于推荐首批完成：下一单元是 R3a，之后 R1a；Room 大包、现有具体 DSH／Repository／provider 装配均仍在。真实服务采用、模型／浏览器与外部写入不是本次本地基线成果。
