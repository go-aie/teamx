# 平台精确工具入口解析

日期：2026-11-20。对应[模块边界方案 R3a](../../ARCHITECTURE-MODULE-BOUNDARIES-ANALYSIS.zh.md#116-r3先抽入口解析再整合静态能力装配)。R3a 已完成；R3b 与 R6 尚未实施，本单元结束时 R1a 尚未实施，其后续进展见[快照 writer 与健康围栏](persistence-writer.zh.md)。

## 实施单

| 项目 | 本次交付 |
| --- | --- |
| 范围 | 根据已核验成员的 active bindings、现有工具定义和按需读取的连接快照，解析唯一工具入口；接入工具列表、公开 run／skill 和私有原生读取／附件／动作的现有共用准入 |
| 状态归属 | 纯逻辑迁移；新包不保存 registry、绑定、授权、在途调用或查询缓存。原 manager／registry／Store 继续拥有锁、记录与生命周期 |
| 跨模块合同 | `app/platforms` 接受 `tool.Binding`、`tool.Definition`、`registry.Snapshot` 和局部查询 DTO；返回 `ToolEntry` 或带 code 的 `EntryError`，不接受 Room、manager、HTTP 或 MCP 类型 |
| 接入与清理 | 旧 `resolveToolBindingLocked`、`checkToolEntryLocked` 成为快照读取／错误映射适配；原匹配分支删除。列表和执行账号准入共用新包的绑定／连接身份匹配，无第二套注册表 |
| 验证与停止点 | 多账号、精确入口、generation、缺失／退休绑定、歧义、按需读取与错误顺序；保留原可用性、远端核验、授权、结清、通知来源关联和静态装配 |

实施起点仍是 HEAD `633a0f2fbcd237c0e9a68f5ba13dc93ff0b68d78` 与现有工作树，Go 1.27.1、darwin/arm64。R0 基线快照、其他方案、运行目录及已有改动保持原状；本次另更新架构方案与实施索引，依赖测试增加对新生产包的实际存在检查。

## 实际边界与调用点

[entries.go](../../../internal/app/platforms/entries.go) 只导入 `platform/registry`、`platform/tool` 和标准库 `iter`：

- `BoundTools` 按原绑定顺序选取指定 AgentID／MembershipID 的已知工具；禁用历史记录不进入输入集合。
- `ToolEntry.MatchesConnection` 统一核对 PlatformID、Provider、Generation。列表和执行账号准入使用同一规则。
- `CheckPublicToolEntry` 拒绝公开 provider alias；即使只绑定一个账号，也必须使用列表给出的精确入口。
- `ResolveToolEntry` 解析公开入口或带平台证据的私有实现类型，保留无匹配与多匹配的区别。

原 `room` 包继续负责：

| 调用点 | 当前作用 |
| --- | --- |
| `tool_entries.go:resolveToolBindingLocked` | 在原 manager 锁内调用纯解析；确实需要连接时才执行一次 `platforms.Load`，并在同一锁内重算 |
| `tool_entries.go:checkToolEntryLocked` | 调用公共入口检查并映射原请求诊断 |
| `tools.go:selectionsLocked` | 通过 `BoundTools` 和 `MatchesConnection` 选入口，再做原可用性、账号、动作与成员能力投影 |
| `platform_call_lifecycle.go:preparePlatformCallLocked` | 复用绑定解析和连接匹配；账号解析、最终准入、调用登记与结清仍在原同步边界 |
| `tools_http.go`／`withToolEntry`／`prepareToolLocked` | 原公开列表、run、skill 与内置工具分发路径继续使用以上适配 |
| 附件、GitLab／飞书原生读取和受管动作 | 经原共用准入使用新解析；通知来源账号关联、来源退休判断及授权复核保持原实现 |

这些薄适配仍承接现有调用者；只有后续服务同时接管快照准入、错误映射及相应调用族后才移除。`platform_notification_capabilities.go`、工具／来源／引用工厂、OAuth flight／lease、成员配置操作及进程依赖检查均未迁移，分别留给 R3b、R6 等后续单元。

## 保留的读取与错误行为

解析器收到 nil 连接快照时，只有遍历到“请求工具与绑定的实现类型不同、且绑定有平台”的位置，才返回 `entry_connections_required`。外层加载后重算；非 nil 空快照表示已经读取但没有连接。两次纯计算共用原锁内的绑定和工具定义，不移动准入或最终证据核验。

因此，私有 type 调用和纯本地工具在原本不读取 registry 的情况下仍可工作；其他成员、未知定义及不符合指定平台的绑定不会触发读取。保留顺序也意味着：若本地匹配后仍需检查后续平台绑定，该读取失败仍优先返回；若歧义发生在读取之前，仍先返回歧义。结构化解析错误在旧适配映射回原 `ErrRequest`／`ErrConflict`、field、message 与 hint；registry 读取错误原样返回。公开 skill 路径原有的最终错误处理也保留。

私有实现类型解析不直接核验连接 generation，也不授予权限；正式调用仍由原账号准入拒绝过期 generation，私有 help 继续可以只看绑定。正常公开入口与列表均匹配精确 generation，不会改选同 Provider 的其他账号。

## 验证

完整命令、退出码及日志摘要哈希见 [platform-entries-validation.json](platform-entries-validation.json)，本地日志位于 `.local/architecture-platform-entries/`。本轮全仓回归加 `-failfast`，保留原 3／8 分钟上限；遇到已有失败后未覆盖的后续 Room 测试不计为通过。

| 检查 | 结果 |
| --- | --- |
| 改动前相关 Room 集成对照 | 通过，28.581 秒；多账号入口、附件、原生引用、绑定并发与无账号工具 |
| 新解析与旧适配定向测试 | 通过；覆盖精确账号、generation／Provider、成员隔离、移除／禁用绑定、未知定义、歧义、按需读取及错误优先级 |
| 跨模块 integration race | 通过，Room 30.150 秒；含多账号路由、公开 skill／run、撤权／取消结清、附件、原生引用、OAuth 账号隔离及原有依赖边界 |
| generation 变化的准入 race | 通过，1.847 秒；列表显示不可用、公开入口拒绝、私有正式执行拒绝，私有 help 仍可用，禁用历史绑定不复活 |
| 新包生产依赖检查 | 通过；新包已进入 CLI 的生产依赖图，无反向导入旧 Room／装配／传输 |
| CLI 构建与全部包 e2e 配置编译 | 通过；仅编译，不执行真实模型或平台验收 |
| 全仓 core／integration 回归（failfast） | 均失败，分别 61.662／51.963 秒；同 R0 的 `TestCalendarManagedRecurringPauseRecoveryAndPublication` 在 `calendar_lifecycle_test.go:201` 报 `controlled model turn did not start`，未发生超时；其余包通过，Room 后续测试未覆盖 |
| 标准 `go vet ./...` | 失败；重现 R0 已记录的 12 处既有非具名结构体字面量告警，本次文件无新增告警 |

本次仅抽取纯解析，没有重启运行服务、修改运行数据、执行真实平台写入或进行真实模型／浏览器验收。既有全仓失败不由专项通过抵消，测试预期、超时及检查门槛未修改。R3a 的包边界已经实际使用，Room 大包与后续 owner 迁移仍待后续单元完成。
