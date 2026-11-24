# 平台静态能力装配

日期：2026-11-20。对应模块边界方案 R3b。

## 实施单

| 项目 | 本次范围 |
| --- | --- |
| 范围 | 已有 GitLab／飞书／Scion 的平台类型、连接配置合同、工具描述、通知来源工厂、可选输入 codec 与引用能力；受管服务、单 Room、离线平台升级及列表查询接入同一静态来源 |
| 状态归属 | 纯静态描述与装配迁移。账号、成员权限、来源进度、在途调用、订阅和 Store owner 不变；不迁移数据或新增写入者 |
| 跨模块合同 | `app/platforms` 汇集显式工厂并验证；公共 `platform.Catalog` 仅持有类型值快照，注入 registry；运行回调由 Room 外层绑定，不把 manager 传进新包 |
| 接入与清理 | 删除公共包固定 provider 清单和工具清单；通知来源、codec 与引用描述从已校验目录取得；保留 Room 专用准入、配置与动作结清适配 |
| 验证与停止点 | 通知单能力、工具单能力、重复／缺失声明、无 codec 来源及已有磁盘分区缺 codec 的保护；原权限／身份／生命周期和协议保持；R6 流程、R1b 和第二阶段扩展另行实施 |

实施起点为 HEAD `633a0f2` 及包含 R0／R3a／R1a／R5a 的现有工作树，保留其他未提交工作和运行目录。改前定向集成日志位于 `.local/architecture-platforms/before.log`。R3b 已完成；未重启日常服务、刷新 Session 或迁移运行数据。


## 合同、接入与保留责任

[静态目录](../../../internal/app/platforms/catalog.go)接收显式无 I/O 工厂，校验 provider、工具类型、通知通道／来源工厂及可选能力的完整性；[内置选择](../../../internal/app/platforms/builtins.go)是已有 GitLab／飞书／Scion 的唯一声明来源。构造不读取凭据或环境、不探测可执行文件、不启动任务。来源工厂只接收 GitLab Events／Todos 的查询准备和飞书 Reader 合同，没有 `roomManager` 参数。

`platform.Catalog` 只持有平台类型值快照。其构造与查询复制通道集合；应用目录同样复制工具环境／依赖、通知描述和引用动作的 map／Schema，普通列表查询不能修改其他消费者的注册。codec 实现沿用已有不可变值；不缓存权限、账号、可用性或执行结果。目录不需要关闭，运行 owner 和锁顺序保持原状。

| 正常入口 | 接入方式与保留责任 |
| --- | --- |
| 受管服务 `initPlatform` | 从同一目录生成连接配置适配，向 `registry.New` 显式注入类型快照；飞书客户端和 Scion 服务目录的验证回调留在外层 |
| 平台工具列表／绑定／执行 | `toolDefinitions` 从该目录生成原工具描述；可执行路径和环境 override 由外层在原查询时机绑定。工具名称、版本、Skill、环境映射与依赖不变；R3a 精确入口与原执行准入继续使用 |
| 通知初始化、重开与独立单 Room | 从目录来源工厂绑定实际 Source，外层 `gitlabNotificationLifecycle`／`larkNotificationLifecycle` 只绑定准入与加载核验；不再各自创建来源清单 |
| 私有／共享 Inbox、History 与输入存储 | 默认通知能力来自同一目录；受管激活通过 `serviceConfig.inputCapabilities` 注入同一查询到 Store。`inputStorageCodecs` 继续投影已注册来源，不建立第二份 codec registry |
| 引用描述与动作目录 | `initPlatformReferences` 从目录生成静态 capability 工厂；账号／证据核验、resolver、preparer、在途调用和结清仍由原 Room 适配绑定 |
| 离线 `platforms upgrade` | 使用同一类型快照、连接 normalizer、历史身份和 profile 校验；不再另列 provider 能力，不改变升级协议或磁盘格式 |

已删除公共包的 `platform.Types/LookupType` 固定列表、`tool.Catalog` 工具清单、Room 的通知能力 switch 和 `gitlabReferenceCapabilities` 重复工厂。原纯解析包仍承接 R3a；公共平台、registry、tool、notification 不反向导入应用装配包，既有生产依赖约束覆盖当前入口。架构采样脚本已索引新目录与 registry 的实际调用。

Room 中的默认目录 helper 仅缓存不可变的已校验快照，不是全局可变注册或 `init()` 注册。默认工具／通知能力薄适配供独立 Store、旧投影入口和测试使用；这些调用随 R4／R6／R8 迁移后再收敛。运行 connection／reference／source map 是由描述生成的既有适配句柄，原生命周期回调和测试注入保留；没有新增账号 owner、订阅 owner、权限缓存或并行写入。

## 验证与限制

可复现命令、退出码、日志摘要和本轮源码哈希见 platform-assembly-validation.json（本地验收文件 `platform-assembly-validation.json`）。原始日志与隔离对照位于 `.local/architecture-platforms/`，R0／R3a／R1a／R5a 证据不覆盖。本轮为本地组件与受控集成验证，没有执行真实模型、远端写入、浏览器验收或部署。

| 检查 | 结果 |
| --- | --- |
| 改前／改后平台定向 integration | 均通过；Room 分别 19.017／18.100 秒，覆盖已有工具、引用、入口和输入存储合同 |
| 目录与存储边界 | 通过：仅通知／仅工具、缺失／重复／不一致注册、无 codec 来源、返回集合隔离、正常来源／codec 一致，以及 registry 只使用注入目录。已有 GitLab 磁盘分区缺少 codec 时仍返回 `inputstream.ErrVersion`；分区文件不变，恢复 codec 后正文仍可重开 |
| 最终跨模块 integration race | 除已独立复现的基线 fixture 失败外，全部选择项通过，Room 42.593 秒。覆盖 onboarding、权限绑定、离线升级、原生引用读取、撤权结清和输入隔离；命令的 `-skip` 明确只跳过下述单个已记录失败，没有修改测试或 CI 门槛 |
| CLI 构建／e2e 配置编译 | 均通过；编译产物只写入 `.local/architecture-platforms/`，e2e 仅编译 |
| 标准 vet | 仍为已有 12 处非具名字面量告警；忽略迁移造成的行号变化后，与 R5a 日志逐项一致，无新增告警 |
| 全仓 core／integration | 使用仓库 3／8 分钟上限和 `-failfast`；均重现下述 Calendar 基线失败，不能计为全套通过 |

完整回归仍在 `TestCalendarManagedRecurringPauseRecoveryAndPublication` 的 `calendar_lifecycle_test.go:201` 报 `controlled model turn did not start`；其他包通过，Calendar 之后未执行的 Room 用例不计为通过。

本次扩大 race 范围还暴露 `TestSecondNotificationProviderReloadAndDedup` 的既有 fixture 问题：重建 notification Manager 后没有重绑 2026-11-03 的固定时钟，加载时改用当前时间，原 GitLab 记录触发过期，导致整体状态比较失败。当前工作树和隔离 HEAD `633a0f2` 均连续三次同样失败，没有数据竞争报告；临时 Go overlay 只补回 fixture Clock 后，该用例 race 通过。overlay 仅用于诊断，没有修改仓库测试预期，也没有将该结果当作原测试通过。

## 完成边界

R3b 交付已有静态能力的单一来源、工厂和正常消费者接入，可以在此停留。账号、OAuth、来源配置、订阅恢复、动作准入／执行和在途结清仍属 R6；Room 成员／输入／持久业务 owner 仍未迁完。R1b、R2、R4、R5b 和第二阶段 Gordis／外部扩展／可裁剪构建不包含在本次交付中。
