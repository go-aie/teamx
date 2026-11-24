# 扩展与按需构建的基线及分支交接

日期：2026-11-20。对应[第二阶段方案 P0](../../GORDIS-PLUGIN-AND-SELECTIVE-BUILD-PLAN.zh.md#92-p0固定当前基线明确分支进入条件)。**P0 已完成；下一项可实施 P1a。** 本次交付源码、依赖和验证基线，以及平台／Agent 分别进入后续单元的条件。未新增生产接口、工厂或 Gordis 依赖，P1–P5 的实现仍未开始。

## 基线与证据范围

起点为 `6bf554bdebd8496b5d545252b3fddc856ebc9db2`（`test: streamline regression suites and browser checks`）加现有工作树，Go `1.27.1 darwin/arm64`，`GOWORK` 为空。R0、R3a、R1a、R5a、R3b、R6a.1、R5b.1 的交付记录继续有效，见[实施索引](../README.zh.md)；不把暂缓的第一阶段单元算作已完成。

开始时有 21 个 tracked 文件修改，其中 18 个为 Room 生产 Go 文件，另有未跟踪的附件生产文件和测试。它们涉及 Room 附件、材料、输入、History、Store 锁及关闭路径，属于本次基线的一部分；测试通过不能只归于 HEAD，也不表示这些独立工作已获本阶段验收。另有既有方案、运行目录和本地依赖；本次不处理它们。未读取运行凭据内容、重启日常服务、迁移数据或执行远端业务写入。

核对期间，既有 `room_attachment_materials.go` 与其测试分别于本地 16:10:55、16:13:48 发生其他工作树编辑。本次保留它们，并在证据中分别记录初始／后续哈希；主分支定向回归对应初始采样。16:14:50 后对更新后的材料边界补跑两项 integration＋race（Room 4.435 秒），并刷新构建、vet 和全仓 e2e 编译，均通过；未把之前的完整选择升级为覆盖这次并发编辑的结果。

机器可读证据（本地验收文件 `plugin-handoff-validation.json`）保存采样时间、HEAD、原始 diff 摘要／哈希、参与调用的源码与测试哈希、实际生产包依赖、飞书直接引用、资源入口、版本与逐命令结果。采样复用 `scripts/architecture-baseline.py`，原始日志在本机 `.local/plugin-handoff/`；旧 R／History／测试健康度证据没有覆盖。依赖来自 `go list`，具体方法调用另经源码核对，不将文本命中当作完整调用图。

## 分支判定与停止点

| 分支／单元 | 判定 | 下一步和边界 |
| --- | --- | --- |
| 平台 P1a | **可进入** | 拆通用目录／解析与内置汇总、Provider 专用来源绑定，继续默认完整组合；无需先迁连接、订阅或输入 owner |
| 平台 P1b | 等待 P1a | 公开最小工具描述及启动／Compose 入口，外部 module 实际发现与执行；当前 `cmd/teamx → internal/room.Run` 尚不能作为外部作者入口 |
| 飞书 P2n | 既有通知 owner／投递合同可复用；等待 P1b 和运行绑定收敛 | 把 `Source.Bind` 与 `Admit`／可选加载检查一起显式绑定，去掉通用初始化的 Provider switch；保留原 manager 门槛、每 Room 来源 owner 和收件 owner |
| 飞书 P2t | Calls 可复用；准入／应用分发仍需具体 R6a.2 切片 | 完整处理现有内置 `help/read/actions` 调用族，不能只搬 read；等待 P1b，默认接 P2n 后实施 |
| Agent P4a | 生命周期调用合同可复用，**跨包宿主合同尚未具备** | 先完成下文“DSH 宿主准备操作”，再迁工厂。不能把持有整个 `service`／`Store` 的当前适配直接公开，也不自动迁完整 MemberAgent |
| P2r／P3／P5 | 尚不具备产品裁剪／Gordis 生命周期／选择交付证据 | 以本记录剩余引用清单继续逐操作完成，不以运行时禁用、SDK 编译或 P1a 包级依赖代替产品验收 |

P0 的完成是交接判定完成，不要求两条分支同时无条件放行。近期授权边界止于本记录；上述下一单元未在本次实施。

## 正常入口、能力与依赖闭包

| 入口 | 当前实际调用与交接点 |
| --- | --- |
| 默认受管服务 | `cmd/teamx/main.go → room.Run → serve/runServerOnce → serveRoomManager/openTeamRoomManager`；`management.go` 装配 `newRoomManager`，`platform.go:initPlatform` 注入目录与 registry，加载连接／绑定／来源，再由服务运行路径启动采集。构造 manager 已会建日历等资源，不能直接拿它做纯 Compose 检查 |
| 显式单 Room／离线命令 | `newService/startService`、Store 的默认能力 helper、平台／通知／输入升级继续存在；它们会经 `platform_assembly.go:defaultPlatformCatalog` 使用 `platforms.Builtins`，P1a／后续装配不能只更新受管服务 |
| 静态能力 | `app/platforms/builtins.go` 是现有唯一全集声明：GitLab 工具＋events/todos、飞书内置工具＋messages、Scion 工具；前两者提供保存输入 codec，Scion 不声明通知。飞书当前选择的是 `SearchSource`，不是退回旧轮询采集 |
| 通知 | `defaultNotificationAdapters → Source.Bind`，由 `notification.New(Host, sources...)` 接入；`loadPlatformNotificationsLocked` 加载已保存状态，`admitPlatformNotification`／`commitPlatformNotification` 分别负责查询准入和真实收件 |
| 工具 | `tools list`／精确入口先经 R3a 解析；`tools_http.go:serveToolRun → builtinToolDispatchers["lark"] → serveBuiltinTool/runBuiltinTool`，后者接原 read／动作服务。内置 help 仍走成员／绑定及 Calls，但不读取账号凭据；不能统一成与实际 read 相同的凭据预检 |
| Agent | `recovery.go:spawnMember → executionBackend → memberBackend`；`inspectBackendRecovery` 用同一具体适配解释恢复；关闭 Room、模型应用、Workspace、activity 等则调用固定 `storedSessionBackend(store)`。P4a 必须统一两类构造来源 |
| HTTP／MCP／CLI | 现有访问／执行身份包装与原 framing 保留；默认 unified HTTP MCP 是 Inbox 两工具＋History 两工具；CLI Inbox 模式的 MCP 只有 History 两工具。History／预览不会代替正式输入消费 |

`go list -deps ./internal/app/platforms` 和 `go list -deps ./cmd/teamx` **都包含 GitLab、飞书、Scion**。原因首先是 `catalog.go/entries.go/calls.go` 与 `builtins.go` 同包，后者导入全部实现；`SourceBindings` 还直接包含 `gitlab.EventsPrepare/TodosPrepare` 和 `lark.QueryScope/MessageReader`。拆掉汇总后仍须处理这一专用参数合同。另一方面，`registry`、`tool`、通用 notification 的 Go 包依赖未反向引用 Provider，已有边界可保留。

P1a 的直接工作面是 `internal/app/platforms/{catalog,builtins,entries,calls}.go`、`internal/room/platform_assembly.go`、`platform_notifications.go`、`platform.go` 及原目录消费者。通用包应保留零 I/O 描述校验／解析，内置选择移到外层；默认目录、单 Room／Store 和离线消费者接同一显式完整组合。Call 登记不是 Provider 工厂，无需因此重做 R6a.1。完成时检查**通用包**不含具体 Provider、默认集合与来源／codec 一致、旧描述删除；Room 仍直接导入多个 Provider，须继续列明而不能称产品已裁剪。

## 飞书切片的 owner、锁与结清

以下 `D` 是 manager 的 `data_dir`，`R` 是 Room 的 Store 目录，受管模式为 `D/rooms/<roomID>`。

| 状态／操作 | 唯一 owner 与现有可用操作 | 同步、恢复及落盘 |
| --- | --- | --- |
| 连接与账号 | `registry.Manager` 维护连接文件；Room manager 的连接／授权编排调用 `Load/ResolveAccessAccount`、`BeginVerification/CompleteVerification` 等。`platform.go` 绑定飞书核验客户端 | `D/platform-connections.json`；`manager.mu` 保护准入快照和应用操作，registry 保存仍经原持久适配；PlatformID、Generation、账号、CredentialVersion／Fingerprint 分别核验 |
| 配置／授权 | `configureLarkSubscription/syncLarkSubscription`、成员配置和 `resumeLarkSubscriptionsLocked`；`larkAuthSessions`、`oauthFlights/oauthLeases` 及验证结果仍由原 manager／registry 管理 | 原配置版本、操作回执、账号刷新／撤销与应答丢失语义保留；不能在新 Provider 中复制 token lease／flight 或直接改共享 map |
| 来源状态与 worker | `notification.Manager` 管目录／worker，每 Room `NotificationActor`／source owner 串行提交；`Source/NewRunner`、`Configure/SaveFrom`、`ChangedLocked/CancelLocked/DoneLocked/Stop` 已可用 | `R/notifications/lark.messages.<subscriptionID>.json`；查询目录 `R/notification-queries/`。内部 `Manager.mu` 不跨回调／I/O；持久业务 owner 与 manager 准入门槛分开 |
| 飞书查询准入 | `platform_notification_lark.go:larkNotificationLifecycle().Admit` 在 `manager.mu` 下解析已捕获账号，核对绑定并返回 `QueryScope`、Revision、CredentialStamp；Reader 只由 `larkClients.New` 构造 | 公共准入先在锁外取 Room roster，再锁内复核 runtime／成员／配置；实际查询在锁外执行。当前飞书适配没有额外 `CheckLoaded`，不得虚构必须实现的空加载接口 |
| 收件与确认 | `commitPlatformNotification` 固定 runtime，在 `manager.mu` 下获取 `service.changes` 读租约，锁外调用 `receiveInbox`；原私有 MemberInputActor／共享 InboxActor 保存正文，来源另确认 | 私有 `R/inboxes/<membership>/<provider>/{partition.json,records/*.jsonl,consumption.json}`；共享 `R/shared-inboxes/project/`，Grant 和成员消费独立。保存成功才确认来源；DeliveryID 保证来源确认丢失后重放防重 |
| 工具准入及结果复核 | `preparePlatformCallLocked` 捕获精确成员／绑定／账号，`checkLarkReadLocked` 等在结果返回时重新核验；read、引用和动作仍使用各自原服务 | `R/tool-bindings.json`；manager 只持短门槛，远端与输出在锁外。动作账本、授权与未知结果不归 Calls 或未来 Plugin |
| 所有在途调用 | 每 manager 一个 `app/platforms.Calls[*platformCall]`；`Register/Cancel/Pending/Finish/WaitCalls`，原调用处负责在资源实际清理后 Finish | 锁顺序 `manager.mu → Calls.mu`；Cancel 只是信号，门槛外等待回执。成员移除、绑定改变、撤权和 Session 轮换共用 `beginPlatformMemberChange` 等原门槛；超时不移除登记 |

P2n 可以复用 `notification.Host` 的具体保存／准入／投递操作，由宿主适配守住现有锁；不把完整 manager 传给来源工厂。需要先接通的准备操作仅为**飞书 messages 运行绑定的构造、准入、加载与退出**，随 P2n 实施并验证；不要求把所有飞书订阅配置提前迁出。若移配置，则以 `configureLarkSubscription` 的一次完整替换为 R6b 操作：保留旧来源 retired 发布、附件调用取消、锁外等待、版本复核与后继 worker 启动，不能只拆保存调用。

P2t 需把 `tools_builtin.go` 的参数／应用调用与 HTTP 输出分开，覆盖 `help/read/actions resolve/actions execute`，继续使用同一 Calls。`readLark/readLarkTarget`、`executeReferenceAction` 的原验证与动作 owner 不迁成新的执行账本；同一外层工具调用可能还经原下层服务登记资源，不能随意删掉这些结清点。该准备就是所选 **R6a.2 调用族**，不是整条 R6 已完成。

## DSH 切片与跨包前的宿主准备

`MemberAgent` 继续拥有成员执行、暂停、恢复、回合及轮换状态，`app/agents.TurnScheduler` 拥有排队／名额，`memberStateStore` 委托原检查点与输入 owner。`ownedSession` 汇合单次关闭；后端的 cleanup pending 与已结清但保存失败是不同事实，不能因取消、超时或其他资源已停而启动替代 Session。

| 当前合同／入口 | 可复用事实与尚存依赖 |
| --- | --- |
| `Backend/Session` 与 `backend_lifecycle.go` | 执行、拥有资源的关闭、恢复分类、状态引用、模型切换已有真实消费者；`memberStateStore` 不暴露 Store mutex。合同仍有私有方法和同包值，不能直接作为外部实现 API |
| `dsh_lifecycle.go:memberBackend` | 已集中正常构造，但返回具体 `dshBackend`，含 `inputService/inboxMCP *service`、`*Store`、`*serverAccess`、模型／凭据／settings；`executionBackend/inspectBackendRecovery` 复用它。尚不是窄宿主工厂 |
| `storedSessionBackend` | 目前仍 `return dshBackend{store: store}`。调用遍布 `runtime_assembly/runtime_recovery`、`rotation_state`、`model_apply`、`default_assistant/scheduling_state`、`ui_http`、Workspace 关闭／删除检查；换正常成员构造而漏掉这里会形成固定 DSH 旁路 |
| `dsh.go:OpenSession` | 直接锁 Store 读写 `session.json`、模型快照与 `mcpOperations`；用 `serverAccess.issueExecution/issueMCPInbox` 建执行资源，并通过整个 service 取得 MCP／材料。返回拥有 component 和 revoke 的 Session，即使打开部分失败也交由原清理路径收口 |
| 输入／材料／History | `input_stream_runtime.go:newInputSession`、`mcp_access.go` 与 `dsh_materials.go` 仍在宿主；执行 generation＋turn、成员身份、图文范围／额度、正式结果证明和 revoke 等待都必须保留。不把这些算法搬入 DSH 或新建输入 owner |
| 持久与观察 | `R/agents/<membership>/agent.json` 是成员检查点；`session.json` 是适配引用，`session.json.dsh*` 下的 backend／ACP SessionID、恢复与进程文件由 DSH 组件管理。模型快照／轮换记录沿原路径；活动由后端适配投影，私有状态不公开 |

**跨包前的具体准备操作：** 在原包补强 R5b.1 的宿主边界，先保持原调用而收敛以下两个完整操作；这不是 R5b.2 的成员状态机迁移。

1. **打开及关闭一个成员 Session 的资源闭包。** 从 `memberBackend → OpenSession → dshSession/ownedSession.close` 收敛引用／模型检查与保存、Workspace 检查、执行与 MCP 授权、材料／恢复上下文及 revoke 的宿主操作。宿主继续持有 Store、access 和 inputSession；后端取得必要值及窄操作，不能接收全能 service／Store。验证部分打开失败、取消、MCP 撤销等待、真实关闭结清和持久输入证明后，才迁跨包实现。
2. **读取及切换已保存 Session 的操作。** 收敛 `storedSessionBackend` 使用的引用、代际、恢复／结清核验、固定 ModelTarget 切换与 activity 所需保存／查询适配；活跃与关闭 Room 的全部上述调用必须最终来自同一所选工厂。验证构造／状态读取零模型执行、旧代际失权、轮换未知提交重试和关闭 Room 模型应用，不能把新的动态模型解析塞进已准备好的切换。

两个准备操作均保留原唯一 writer／锁／状态机，不新增未来空接口；按当前调用确定合同和值类型。准备完成后 P4a 再公开真正必需的工厂能力，默认仍只接受 DSH；P4b 再记录 ACP 通信、DSH 扩展与 TeamX 持久接收桥接的能力归属，并做真实后端对照。ACP 的成功响应、MCP 返回、fixture 成功或本轮 SDK 编译都不能证明正式输入已持久接收，更不证明 Codex 兼容。

## 关闭、恢复与文件约束

受管关闭在 `runtime_shutdown.go:shutdown` 先取消日历／关闭更新通道，取得 shutdown gate，设置 manager closing，冻结全部服务的回合准入，取消授权／生命周期 worker、Calls 和通知，再在门槛外并发等待每 Room、通知 Stop 与授权完成。单个失败不阻止其他 Room 收到停止；失败／未结清保留 root 所有权及可重试状态，不伪造成功回执。

每 Room 的 `service.close` 依次封闭准入、等待已受理 changes、停止授予、通知成员结清并等待 management、停止／等待成员、结清日历和 Inbox、停止调度与 Room／runtime、关闭输入 owner，最后关闭 Store。`Store.Close` 等待输入和 writer 后才释放目录；**本工作树还包含附件 `attachmentLife` 等待及原生 `roomCommit` 串行**，下一阶段不得照旧 HEAD 的锁清单绕过它们。构造失败的清理仍由原 defer／返回拥有资源的 Session 承担。

恢复继续核对 RoomInstance／MembershipID、持久启停意图、来源配置／账号代际、输入结果、固定轮换目标与旧进程结清；未知提交的健康围栏必须先重开对账。没有把 Store 或 Repository 变成 Plugin，Gordis 的停止也不能代替这些业务结清。

## 剩余引用、资源与暂缓 R 单元

证据文件的 `lark_importers` 列出本次全部生产直接导入文件，`provider_references` 补充公共装配／内置分发等文本引用。P2r 应按下列操作继续隔离；该清单包括 Go 传递依赖之外的资源。

| 操作／面 | 当前落点与后续责任 |
| --- | --- |
| 连接、核验、认证与账号能力 | `platform.go`、`platform_lark_auth/authorization/capabilities/chats`、`platform_connect*`；保留同一 registry／auth／lease owner，逐个完整 R6b 操作迁适配 |
| 成员／订阅／onboarding | `platform_member_lark`、`platform_notification_lark*`、`platform_member_store`、`platform_onboarding`、`tools_lark_configuration`；包括配置版本、应答丢失、停用／恢复及 capability 投影 |
| 引用、动作、图文和历史 | `platform_lark*references/target/actions`、`platform_references`、`platform_attachments`、`notification_images`、默认 notification capabilities、`input_stream_storage`；本地 codec 与投影也与飞书实现同包，保留历史读取的独立工厂尚不存在 |
| 离线升级与旧数据 | `lark_notifications_upgrade`、成员／输入／平台升级；缺少已有分区 codec 时 `OpenRepository` 返回 `inputstream.ErrVersion`，不跳过分区或重建空历史。P5b 仍需新增**启动前**配置／分区能力检查，不把当前较晚的打开拒绝当作已交付预检 |
| Web 与诊断 | `internal/web/embed.go` 包含所有 feature 模块；`features/lark-connection.mjs` 及 connections、notifications、members、onboarding、settings、inbox 等仍有专用入口。当前后端目录不能自动生成这些 UI，P2r／P5b 分别说明支持与资源裁剪范围 |
| 工具、Skill 与打包 | `tool/skills.go` 一次嵌入所有 `skills/*/SKILL.md`；飞书工具是内部受控分发，`lark-cli` 仍供账号／通知等现有路径使用。`packaging/dependencies.lock.json` 的 core 为 node＋DSH，standard 另含 glab＋lark-cli；`bundle-files.json`、包装器、许可证、模板及 package 脚本仍按既有 profile 交付，与未来 Provider 选择不是同一事实 |
| 固定基础 | DSH launcher／Node、`input_bridge.mjs`、`probe_tool.mjs`、Agent guide／角色模板、文件存储与目录锁继续保留；本阶段不交付完全无 DSH 的产品 |

| 暂缓单元 | 本切片结论与证据／触发条件 |
| --- | --- |
| R1b 目录锁 | 现有 Store／bundle 持有与释放合同可复用。P0 无需迁锁原语；未结清保锁、关闭后重开仍是后续验收条件 |
| R2 History | 现有 History／MCP、撤销及图文合同可复用。P1a 不改查询 owner；DSH 只绑定宿主能力，不因此搬 History 算法 |
| R4 Inbox／正式输入 | 通知 `Host.Deliver → receiveInbox`、原输入 Actor／Repository／generation＋turn 证明合同可复用。先做 DSH 宿主准备；若必须直接操作输入内部状态，再选具体 R4 操作，不能复制 owner |
| R5b.2 完整成员迁移 | 暂不需要；生命周期经现有合同发生，隐藏具体类型的轮换／模型 fixture 和 AST 约束继续有效。但 DSH 宿主准备尚待实施，不能称全部跨包前置已具备 |
| R6a.2 | P2t 承接飞书整个内置调用族；其他工具、附件、原生读取族保留原适配，分别验收 |
| R6b | P2n 仅收敛运行绑定，可继续调用原订阅／连接 owner；P2r 真正裁剪时，核验、订阅替换、授权／刷新／撤销等逐操作迁移。不能把整套配置搬迁加入 P1a |
| R7 周边业务 | 原 Followup／动作账本、Models、Workspace、Calendar、Access 可通过宿主操作保留；当前无需迁 owner。DSH 准备涉及模型／access 的适配，不能直接公开这些私有服务 |
| R8 Room／装配／传输 | Room 与运行目录 owner 可保留；P1b **需要**公开启动适配接入正常 cmd，P2t 需要分开应用调用与 HTTP framing；不等待全量 R8，也不以只增外部入口而保留另一套默认装配验收 |

## Gordis／Build 的当前版本与 API

通过模块代理以 `GOWORK=off` 查询，Core `github.com/RussellLuo/gordis` 和独立模块 `github.com/RussellLuo/gordis/build` 当前 `@latest` 均解析为 **`v0.0.0-20261027071224-44c780f99320`**，Go 要求为 `1.27`。本地 Gordis HEAD 同为 `44c780f99320bb8e19accdc6bd5fae9c4beb9331`；相关 API 文件与下载的已发布模块哈希核对一致。本地未跟踪文档／脚本不构成发行依赖，Qi 仅为设计参考。

在 `.local/plugin-handoff/sdk/` 的独立临时 module 固定上述两个版本，**没有 replace、没有相邻 checkout 依赖**，编译核对 `NewHost`、`Env.MountReady`、`Host.Shutdown`、`PluginSpec.New`、`Scope.AcquireReady/OnStop/Defer/AfterReady`、`build.Build` 和 `Adapter.Generate/Validate` 的实际签名。证据保存临时 go.mod／go.sum、编译源码及模块校验和，TeamX 根 go.mod／go.sum 未更改。Custom 示例属于嵌套 module，没有打进已发布 Build module；仅作本地源码参考，不能依赖下载主 module 获得该示例目录。

`Plugin.Spec/New` 是纯元数据／零启动构造，`Activate(ctx,*Scope)` 才取得资源；Ready 后服务发布，OnStop、排空和 Defer 分别承担停止与清理。以后 P3 只选择已有真实资源服务映射这些行为，不为目录创建空 Plugin。Build 以 `Request` 选择包／模块／替换，`Adapter.Generate(Plan) → Project` 生成 main，非空 `Validate.Args` 执行应用提供的候选检查，再交付 binary 与 `.build/` 记录；**TeamX 尚无该零资源检查入口**，应由 P1b／P5a 接通。Build 编译库不进入产品运行依赖；额外资源选择与旧数据预检仍是 P5b 的应用责任。本轮未运行 TeamX 的 Gordis 装配或选择构建。

## 本轮验证与后续清单

命令、完整选择、逐项通过／跳过、退出码、日志哈希和失败摘要见机器可读证据。以下按本轮当前工作树报告；部分独立命令并发执行，计时只用于定位，不作为性能对照。

| 检查 | 本轮结果 |
| --- | --- |
| 平台定向 integration＋race | 40 项顶层通过，Room 84.472 秒；飞书来源／账号恢复、收件防重、共享来源、配置应答／旧状态保护、工具授权与动作、升级未知提交及 manager 关闭 |
| Agent 定向 integration＋race | 44 项顶层通过，Room 59.889 秒；关闭结清、自动／手动轮换、固定代际恢复、模型切换、输入升级和工具 Schema；包含隐藏具体后端类型的模型轮换 fixture |
| 精确工具入口／撤权 integration＋race | 7 项顶层通过，Room 44.957 秒；覆盖内置分发、动作回执、慢输出撤权与原生调用结清 |
| 已知 Calendar／History 用例复验 | 两项通过，Room 4.981 秒；旧 Calendar 失败和 History 总套件超时不再写作本轮单项失败 |
| 组件全量 race，包上限 3 分钟 | **命令退出 1**。app、DSH、inputstream、persistence、architecture 和其余平台组件包通过；飞书包在三分钟总超时退出，当时 `TestSearchBacklogRestartAndDelayedIndex` 运行约 82 秒，栈在 `State.Clone → JSON`。不是该用例单独运行三分钟；没有断言失败或 race 报告，不足以判断死锁／业务缺陷。中止后未执行的飞书用例不算通过 |
| CLI 构建／`--help`、`go vet ./...` | 通过；产物只写 `.local`，帮助未启动服务 |
| `go test -tags=e2e ./... -run '^$'` | 全仓 e2e 配置编译通过，执行零业务测试；不等于模型／浏览器／平台验收 |
| 发布 API 独立模块编译／依赖检查 | 通过；产品和应用目录仍有全部 Provider，未加入 Gordis 运行依赖 |

旧 R3b／R5b.1 记录中的 `TestSecondNotificationProviderReloadAndDedup` 和 `TestInputUpgradeRotatesSettledDSHContract` 本轮均在原测试上 race 通过，不再沿用旧 fixture 失败结论；原 vet 告警也已消除。完整 core／integration／全仓 race 本轮**未重跑**，不沿用旧全仓结果冒充本轮通过，也不因一个组件包超时扩大重跑或修改 gate。600 条飞书积压的完整 race 验收仍未完成，后续影响该来源时须单独有界定位／复验；P1a 的纯装配解耦可先进行，不能据此放宽 P2n 的通知恢复门槛。

未运行真实 DSH／模型、真实飞书或其他平台、浏览器、归档发行、部署及日常服务切换。已有 R5b.1 的真实 DSH 和 H3／H4 的图文证据只作历史上下文，未刷新为本轮事实。

下一单元沿本记录的实际入口选验证：P1a 核对通用依赖、默认来源／codec、零启动／按需账号读取及单 Room／升级适配；P1b 验证外部 module 的发现和执行；P2n 覆盖启动／重开／停采、账号代际、配置应答丢失、pending 投递、撤权和关闭；P2t 覆盖精确 help／read／actions、流式输出、晚到结果及关闭重试；DSH 宿主准备和 P4a／P4b 保留执行、图文、History、取消、恢复、轮换与持久证明，真实后端需隔离验证。产品依赖裁剪、资源裁剪和旧数据预检分别等 P2r／P5 验收，P0 不提前宣称这些成果。
