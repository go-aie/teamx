# 后端生命周期依赖收敛

日期：2026-11-20。对应[模块边界方案 R5b.1](../../ARCHITECTURE-MODULE-BOUNDARIES-ANALYSIS.zh.md#118-r5先迁调度器再收敛和迁移成员生命周期)。R5b.1 已完成。成员状态机与适配继续在原 Room 包；R5b.2 和第二后端接入另行实施。

## 实施单

| 项目 | 本次范围 |
| --- | --- |
| 范围 | 执行、拥有资源的关闭、结清判定、自动／手动轮换、正常／故障恢复、活跃及关闭 Room 模型应用；接通检查点、输入代际校验、活动投影、Workspace 与关闭／删除检查 |
| 状态归属 | `MemberAgent` 继续拥有原状态机和 `agent.json`；原 Store／输入 owner 保留检查点和正式消费权威；后端适配管理 `session.json`，DSH 组件仍独占私有目录、ACP SessionID 和进程 |
| 实际合同 | `Backend/Session` 保持执行合同；`ownedSessionCloser`、`backendRunCheck`、`rotatingBackend`、`backendRecoveryPolicy`、`modelChangingBackend` 表达现有能力；`storedBackend` 为启动与关闭 Room 提供同一持久事实接口；`memberStateStore` 限定成员的检查点及输入回合操作 |
| 接入与清理 | 消除成员关闭、轮换、恢复、检查点、模型应用及 HTTP activity 对 `dshBackend/dshSession` 的识别；DSH 解释集中到后端与模型适配，正常入口实际调用新合同 |
| 保持边界 | 只允许 DSH 的配置、模型字段、JSON／TOML、代际名称、MCP 工具及输入证明协议不变；不迁 MemberAgent、Store 或输入 owner，不建立后端注册框架，不接入 Codex |

实施起点为 HEAD `51e2a43` 和已有架构单元的工作树。保留原有未提交修改；隔离验收只创建本轮目录，不重启或部署日常服务，不执行真实平台业务写入。

## 合同与正常入口

[生命周期合同](../../../internal/room/backend_lifecycle.go)只描述当前调用者实际需要的操作；仅支持执行的 Backend 仍可用于原有有界成员。可轮换后端必须同时提供自己的目录引用，成员初始化检查点时不再构造一个 DSH 适配器代读。`MemberAgent.Store` 使用 `memberStateStore`，没有 Store mutex／目录／repository 的直接访问；输入继续操作由原 Store 委托原输入 owner，`finishMCPTurn` 仍只清理回合资源，不代替正式读取证明。

[DSH 生命周期适配](../../../internal/room/dsh_lifecycle.go)集中目录代际命名、轮换实现、持久结清／恢复检查、故障候选分类、启动恢复探测、运行装配、历史错误识别和活动投影。`storedSessionBackend` 明确选择当前唯一持久后端，不推断或声称支持其他格式。

| 入口 | 收敛后调用及保留责任 |
| --- | --- |
| `ownedSession.close`、`MemberAgent.Stopped` | 经 `ownedSessionCloser` 等待实际关闭，重复／discard 关闭仍由同一个 `sync.Once` 汇合；普通 fixture 保留原有有界 Close |
| 成员运行与取消 | 通过后端故障候选和取消分类合同判断；checkpoint、暂停／启用、调度释放和未知动作检查仍由原成员及应用服务负责 |
| `rotation.go` | 通过可轮换合同读取／核验代际，模型切换使用独立能力；仍先结清平台调用和旧 Session，再持久保存 `old_closed`，最后切换固定目标 |
| `rotation_state.go` | 启动及关闭 Room 通过同一持久后端合同对账；`switchPreparedSession` 使用已冻结的 ModelTarget，不重新解析当前目录或请求模型 |
| `recovery.go`、`runtime_recovery.go` | 后端探测返回原 Session 是否可恢复、故障恢复候选及诊断；原应用层继续检查轮换状态、持久启停意图、一次恢复额度、未完成回合及业务结果 |
| `model_apply.go` | 运行时身份检查由现有模型适配解释；活跃及关闭 Room 经同一代际／轮换合同应用。关闭 Room 不启动 Actor／模型回合 |
| `scheduling_state.go`、`default_assistant.go` | 检查点读取后端实际引用，保留原初始基线、累计计数、阈值和默认助理启用规则 |
| 输入执行／正式读取 | `turn_execution.go`、`input_stream_runtime.go` 读取同一适配引用，保留 generation＋turn、旧代际失权、正式结果证明与原生确认 |
| HTTP activity | 服务先验证活跃成员身份，再向持久后端查询活动投影；通过 `activeSessionOutput` 读取实时输出，不识别具体 Session 或组件 |
| Workspace、关闭和删除 | 经同一后端引用、代际与结清检查，保留原目录锁、固定操作 ID、ModelTarget、历史代际和已移除成员检查 |

`ClosedError` 表示进程已经结清但恢复／保存仍可失败；适配转换为通用结清事实，保留原错误文本及 `errors.Is/As` 链。cleanup pending 优先于任何已结清回执，不能以固定超时、取消信号或另一个资源的结清推定成功。未结清时 Actor／调度名额／Store 所有权继续保留，失败不启动替代 Session。

## 保留的具体适配

| 归属 | 当前仍具体识别的内容与原因 |
| --- | --- |
| `dsh.go`、`dsh_lifecycle.go` | DSH 进程、ACP、错误、私有文件、代际格式及关闭循环；这些是实际后端适配 |
| `model_session.go`、模型参数／凭据／验证服务 | 当前 DSH 的 profile、RuntimeIdentity、ModelConfig、探测回执及持久模型快照；本次不虚构第二后端通用配置 |
| `service_config.go`、`service.go` 的成员配置校验、`serve.go`、`teams.go`、受管 runtime 装配 | 当前 DSH launcher／profile 配置和只允许 DSH 的核验；装配与外部持久字段保留 |
| `dsh_materials.go`、`dsh_recovery_context.go`、`mcp_access.go` | 现有图文及 ACP MCP 声明映射；输入／History owner 和证明协议不改 |
| `backendDiagnostic` | 继续序列化现有 DSH TurnResult，活动接口 JSON 不变；没有引入第二套输出格式 |
| `input_upgrade_legacy.go`、模型迁移 | 旧格式升级必须识别原 DSH 文件；本次不重做已交付的数据迁移 |

Codex 接入仍需独立确认后端选择／配置、ACP 适配、取消及结清、原 Session 恢复、模型／工具／图片能力与输入持久结果证明。当前 DSH 框架桥接没有被泛化或删减；能够通信本身不构成正式输入确认。

## 验证

完整命令、结果、日志摘要和源码哈希记录在 backend-lifecycle-validation.json（本地验收文件 `backend-lifecycle-validation.json`）。本地原始日志与隔离真实运行保存在 `.local/architecture-backend/`。

| 检查 | 结果 |
| --- | --- |
| 改前／改后同一生命周期 integration 集合 | 均通过；Room 分别 56.314／67.277 秒。改前使用完整 Room 源码／测试 overlay，子进程构建继承同一 overlay |
| 生命周期及默认助理 integration race | 通过，Room 149.401 秒；覆盖关闭／discard／panic、运行中取消、自动／手动轮换、固定代际恢复、模型应用、Store 保锁与默认助理恢复 |
| 最终合同 integration race | 通过，Room 28.544 秒；恢复与模型切换 fixture 隐藏具体 Backend／Session 类型，仍通过实际成员入口；架构 AST 约束通过 |
| DSH／调度器／架构组件全量 race | 均通过；DSH 46.112 秒，含取消、进程回收、恢复身份、图文及框架持久证明桥接 |
| 真实 DSH 自动轮换与正常重启 | 通过，6 个真实回合、两个自动轮换周期；空闲不换代，正常重启保留原 SessionID、代际、计数与消费检查点 |
| 真实 DSH 手动轮换与取消 | 通过；保留 Room／业务记录、固定目标重试幂等、旧进程及运行中 shell 退出、取消后新输入、正常关闭与原 Session 恢复 |
| CLI 构建、标准 vet、全仓 e2e 编译 | 均通过；没有部署或日常服务切换，e2e 编译不等于全部真实验收 |
| 函数体与格式 | 11 个核心函数经 AST 解析、仅统一符号名及默认代际 helper 后函数体一致；JSON／目录／模型／证明协议不变；gofmt 与 diff 检查通过 |
| 全仓 core／integration／宽范围 race | 未全绿，具体限制如下；不以专项通过替代全仓结果 |

全仓 core 使用仓库原三分钟上限，在总超时退出时正在执行 `TestHistoryMCPServiceImageLimitAppliesAcrossQueries`（该用例仅运行约一秒）；该用例改前／改后单独执行均通过，不能将此总超时归为该用例失败。没有延长 core 上限或宣称完整 core 通过。

全仓 integration 的 Room 在 414.186 秒退出，记录两项失败：`TestInputUpgradeRotatesSettledDSHContract` 的旧断言拒绝当前 Inbox＋History 四工具，及 `TestSecondNotificationProviderReloadAndDedup` 报同订阅 ID 的第二 provider 改变 GitLab 状态。两项在改前 overlay 和改后单独运行均重现。宽范围 race 在 368.645 秒结束，只有前一项断言失败；其余所选项及其他包通过，没有 race 报告。本轮不修改这些预期或 CI 门槛，也不把尚未执行的全仓用例计为通过。

真实验收使用当前安装的 DSH `0.2.1-alpha.1`、配置中的 `deepseek-v4.1-flash-1017` 和现有凭据，全部运行在新隔离目录，没有真实平台业务写入。自动验收旧驱动在重启时更换随机监听地址，而 MCP 地址属于恢复配置身份；改前／改后均因此触发已结清换代。驱动现复用原监听地址，生产兼容检查不变。手动验收的原 HTTP fixture 补齐私有管理员 token 和服务返回的操作者身份；没有跳过鉴权或放宽进程唯一性、PID／启动时间及退出检查。

未进行浏览器、真实平台业务写入或日常服务切换。R5b.2 的完整成员状态机迁移、第二阶段公开扩展合同及 Codex 接入不在本单元。
