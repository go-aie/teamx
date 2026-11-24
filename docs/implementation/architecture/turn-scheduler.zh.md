# 回合调度器

日期：2026-11-20。对应[模块边界方案 R5a](../../ARCHITECTURE-MODULE-BOUNDARIES-ANALYSIS.zh.md#118-r5先迁调度器再收敛和迁移成员生命周期)。R5a 已完成；成员状态机、后端、检查点及恢复／轮换仍在 Room，R5b 尚未实施。

## 实施单

| 项目 | 本次范围 |
| --- | --- |
| 范围 | serial／parallel 回合授予、FIFO、取消、精确释放、失败投递重试、成员完整退出结清、停止授予；接入单 Room、受管 Room、恢复装配、成员创建与服务关闭入口 |
| 状态归属 | 非持久调度 owner 从 `room.turnScheduler` 迁到 `app/agents.TurnScheduler`；members、queue、holders、closing 及维护任务只存在于该 Actor。服务继续拥有冻结门槛与 Runtime；不新增持久记录或第二套调度器 |
| 跨模块合同 | `RequestTurn/TurnGranted/CancelTurnRequest/ReleaseTurn/StopTurnGrants` 与本地 Actor 地址；`InspectTurnScheduler` 在消息边界返回 holders／queue 副本，供跨模块结清检查；共享身份和 mismatch 错误归 `identity`，调度模式、32 成员上限及容量错误归 `app/agents` |
| 接入与清理 | 删除 Room 调度算法及旧绑定；成员直接使用新消息，服务直接创建新 owner。`spawnScheduledMember`／`bindTurnScheduler` 留在 `member_assembly.go`，负责识别和绑定原 `MemberAgent`；配置 JSON 与 ErrRequest 映射留为薄适配 |
| 验证与停止点 | 改前／改后定向集成、调度器和跨模块 race、授予失败、取消与完整退出、服务冻结、容量、模式、配置快照与恢复；后端／Session、输入就绪、模型门槛、预算、检查点、恢复／轮换和 Store 所有权继续沿用原实现 |

实施起点：HEAD `633a0f2fbcd237c0e9a68f5ba13dc93ff0b68d78` 与包含 R0／R3a／R1a 的现有工作树，Go 1.27.1、darwin/arm64。既有未提交代码、方案和运行目录保留；R0 与前序验证快照不覆盖。本轮不迁移运行数据、不重启或部署服务。

## 合同与接入

[调度器](../../../internal/app/agents/turn_scheduler.go)只依赖 Actorx、[持久身份](../../../internal/identity/identity.go)和标准库；新包不导入 Room、DSH、输入服务或传输。身份只包含原有 RoomID／RoomInstance／AgentID／MembershipID，字段及 JSON 标签不变。`room.RoomIdentity/MemberIdentity` 与 `platform.RoomIdentity` 使用 alias；Room 的 ErrIdentity、ErrCapacity 转发原语义的共享 sentinel，保留 `errors.Is`。Session generation 与 ServiceEpoch 不迁入身份包。

新包拥有模式的缺省值与合法值判定。`room.schedulingMode` 只转发判定并保留既有 JSON 解码、null 拒绝、ErrRequest、空值和省略字段语义；配置、初始化回执及持久快照无格式变化。身份迁出后，只将新产生跨包 vet 告警的身份和调度消息字面量改为具名字段；相关 Calendar、Models、升级与 Workspace 文件没有业务分支改动。

| 调用点 | 当前接入与保留责任 |
| --- | --- |
| `service.go:startService` | 单 Room 与受管激活共同创建唯一 `agents.NewTurnScheduler`，传入冻结模式和服务原有 atomic 门槛 |
| `recovery.go:spawnMember`、`member_assembly.go` | 沿原成员恢复／创建入口 spawn，再绑定新调度器地址；具体 `*MemberAgent` 识别留在旧装配侧 |
| `member.go` | 直接申请、接受、取消与释放新消息；资格复核、单成员排他、释放失败重供、预算及 Session 清理保持原实现 |
| `service.close`、`runtimes.stopRoomGrants`、`runtime_shutdown.go` | 沿原入口冻结并停止授予，再结清管理／成员／输入 owner；最后 Stop/Wait 调度器、Room 和 Runtime，才允许释放 Store |
| `teams.go`、`management.go`、`runtime_assembly.go`、`model_references.go`、`workspace_directories.go` | 沿原配置适配读取新模式判定，保持创建冻结、恢复和目录并发约束 |
| Room 调度／关闭集成测试 | 经 `InspectTurnScheduler` 查询副本，不跨包读写调度内部字段；纯调度器测试迁至新包，不再创建 Room／Store |

身份 alias 的剩余调用者是现有 Room 与 Platform 业务入口，待相应领域归位后再按需移除；JSON 配置适配随 R7／R8 的模板和冻结配置归属迁移处理。成员绑定 helper 随 R5b／R8 装配迁移处理，不能为了删除它而把成员状态机带入本单元。没有保留第二份队列或新旧并行发放。

## 保持的不变量

- 授予之前登记 holder。成员邮箱满时保留责任并定时重投；并发模式下一个失败投递不阻止其他成员收到授予。
- 请求按完整身份及单调编号去重；取消／完成编号在该 Actor 退出前保持退休。旧编号、旧身份或不同目标不能覆盖活动 holder。
- 取消只撤队列，不能证明执行结清；只有匹配的释放或已完成的 Actor Wait 才回收名额。Stopped／后端清理仍在执行时继续持有。
- 原 atomic 服务冻结门槛同时限制新申请与维护重投；Quiesce 清空队列并保留未结清 holder。服务关闭后不因释放、退出或维护再次授予。
- serial 每 Room 至多一个 holder；parallel 每 membership 至多一个。同一成员的 Room／Inbox 继续共用执行限制，不同 Room 继续独立。

## 验证

命令、退出码、日志哈希、源码摘要和算法对照见 turn-scheduler-validation.json（本地验收文件 `turn-scheduler-validation.json`），本地日志在 `.local/architecture-scheduler/`。本轮均为本地组件或受控 Backend 集成证据；未执行真实模型、平台写入或浏览器验收。

| 检查 | 结果 |
| --- | --- |
| 改前／改后调度与关闭定向 integration | 均通过，Room 分别 16.532／15.317 秒；覆盖模式快照、受管 Workspace／讨论重启、服务冻结和输入共用限制 |
| 新包调度器 race | 通过，2.317 秒；保留原三组核心用例，补充冻结／维护、容量退休／模式／身份校验及 Stopped 清理未结清场景 |
| 跨模块 integration race | 全部选择项通过；Room 145.308 秒，含成员取消、失败／panic 清理、退出、恢复／轮换、Store 保锁、身份／引用隔离与生产依赖约束 |
| CLI 构建及全仓 e2e 配置编译 | 通过；仅本地构建和编译，产物未部署 |
| 全仓 core／integration（failfast） | 均失败；Room 分别 73.809／76.031 秒，重现基线 Calendar 失败；其他包通过，无超时 |
| 标准 `go vet ./...` | 剩原有 12 处非具名字面量告警，输出与 R1a 基线逐字节相同；新包及本轮迁移无新增告警 |
| 算法、生成与格式 | 八个原调度方法在类型／符号改名并忽略注释空白后完全相同；Actor 绑定重新生成，原处理器不变，仅增加只读 Inspect；gofmt 与 diff 检查通过 |

全仓失败仍为 `TestCalendarManagedRecurringPauseRecoveryAndPublication` 在 `calendar_lifecycle_test.go:201` 报 `controlled model turn did not start`。两次全仓命令保留仓库 3／8 分钟上限并加 `-failfast`，Calendar 之后未执行的 Room 用例不计为通过；未修改测试预期、超时或 CI 门槛。其他 R0 失败也不因本次专项通过而视为修复。

生产依赖测试要求 `app/agents` 和 `identity` 实际进入 CLI 依赖图，禁止新服务反向依赖 Room／入口装配，并约束 identity 不依赖 TeamX 其他内部包。架构采样脚本同步当前调度符号及新 owner 路径；源码快照验证同时找到新 owner 与旧装配调用，不覆盖原基线。现有打包／CI 没有引用迁出的源码路径，无需改清单。

本单元完成后可以停留。Room 大包仍存在；R5b 的成员状态机、Backend／Session 合同、输入／模型适配、检查点与恢复／轮换收敛另行实施，也不由此宣称已具备完整插件接入条件。
