# 通知来源的运行绑定与外部作者合同

日期：2026-11-21。对应[第二阶段 P2n](../../GORDIS-PLUGIN-AND-SELECTIVE-BUILD-PLAN.zh.md#94-p2np2t补通知和工具的完整运行边界)，承接 [P1b](extension-assembly.zh.md)。飞书 messages 继续使用 SearchSource／schema 4，新增显式运行绑定与可实际投递的公开通知作者合同；默认完整组合保留。

工作基准为 HEAD `fc9ae9d0a8ad09599cc9fd851c448fec2e84e431` 加 P1a／P1b 及既有工作树。本次未提交、发布或切换日常服务。没有新增依赖、迁移存储格式、运行真实模型或进行远端业务写入。逐项命令、日志摘要、源码哈希、依赖与限制见[验证证据](notification-runtime-validation.json)。

## 实现和原 owner

| 位置 | 本次责任 |
| --- | --- |
| `platforms.Source.Bind` | 所选描述显式绑定 Source、Admit、CheckLoaded；Compose 只验证纯声明 |
| `internal/app/builtinplatforms/lark.BindRuntime` | 从 Room 删除原飞书准入实现，改在实现侧核对账号／代际／凭据，生成同一 QueryScope、Revision 与 CredentialStamp；本地检查加载状态 |
| `notification.RuntimeHost` | 只读账号、当前来源订阅快照；不接受 manager／Store，账号快照不含 token，CLIProfile 脱离原记录 |
| `notificationRuntimeHost` | 在原 manager 准入锁内适配原唯一连接／订阅 owner，按所选 Provider／Source 限定查询 |
| `platform_notification_bindings.go` | 单独显式提供尚由 Room 持有的执行依赖。飞书 Reader 继续复用同一 `m.larkClients`，不另建预算或授权 owner；GitLab 保留原生命周期实现 |
| `platform_notifications.go` | 不再导入 Provider 或按 Provider 分支初始化来源；使用所选 Bind 和可选执行依赖适配。离线 codec 选择仅构造静态 Source，不绑定运行宿主 |
| `platform/notification`、`Extension.Notifications` | 公开实际所需的来源／Runner／宿主、通知与身份值、本地 input codec 合同；转换后进入唯一 Catalog |
| `testdata/external-notification` | 独立 module 通过公开 Compose／boot 加载、采集并持久投递到隔离 Inbox，不导入 TeamX internal |

RuntimeHost 的快照方法只允许在 Admit／CheckLoaded 中同步调用，不能从 Runner 使用；这些回调不等待远端工作。Bind 仅连接依赖，生产资源仍在正常启动后的查询侧获取。外部来源无需修改通用初始化或添加专用 Host 字段。

Room 状态、成员／目标准入、连接授权与代际、订阅配置编排仍由原 manager／registry／来源 Actor 管理，本次不需要另做 R6b 操作。原 Read／Commit 检查数据及连接 revision；投递在原运行实例保留 changes lease，由 Inbox／输入 owner 持久收件，成功后来源才确认。正式内容读取仍用本地 codec，取消与关闭仍等待原查询／投递／Store 结清；没有第二个可变缓存、writer、Actor 或业务 registry。

保存目录、JSON／JSONL、订阅 ID、Baseline、冻结路由、pending、凭据恢复与消费证明均未改。Room 附件仍是独立本地材料来源，pin、回收和关闭继续由原宿主负责。

## 外部 module 验收

fixture 使用 `GOWORK=off` 和隔离副本中的本地 replace，不改根 go.mod／go.sum。来源只发出一条确定性本地通知，提供现有持久收件必需的 Input codec；不创建平台连接、工具绑定或远端 Workspace，不启动模型。

第一次隔离服务创建 Room／成员并关闭；随后在停机状态写入来源拥有的显式 opt-in 快照。正常启动调用选中来源的加载检查和 Runner，保存 pending 后，由正式 Inbox 写入 JSONL 并确认。验证人类预览不消费、重新启动不重复、保存内容不依赖远端在线；再以缺 codec 的选择重开，明确得到 `input_version_unsupported`，原 JSONL 哈希保持不变。四次服务关闭均退出 0。

停机 opt-in 仅是 fixture 的来源配置方式，本次没有新增通用外部订阅配置 API。已有飞书配置 UI／授权继续由原 owner 提供；新平台的产品化账号、配置和授权仍须另行实现。公开作者指南见[扩展说明](../../extensions.zh.md)。

## 验证范围

组件与架构 race、通知内核 race、飞书搜索／来源相关组件 race、独立工具与通知 module 验收均通过。正常 `cmd/teamx` 构建、帮助、静态装配检查和全仓 e2e 配置编译通过。主 Room 定向 integration＋race 通过 56 项顶层用例；另有 1 项要求外部停机备份的演练未配置而跳过。图文、本人消息与本地恢复的补充 integration＋race 回归另有 12 项顶层通过，无跳过；两组共 68 项不同用例通过。

新增故障测试验证：停止采集的应答超时后，停用状态和新配置版本已经保存；旧版本重试被拒绝，晚到查询不覆盖保存进度。关闭超时取消查询并保留根 Store 锁；实际查询退出后可以重试关闭、重开原来源，晚到结果不进入保存状态。

原回归同时覆盖正常接收、账号／代际切换、凭据恢复、撤权、多个账号独立配置、来源确认丢失后的重开去重、搜索／回填、原配置操作回执、图片权限、context_only 与本地附件生命周期。相关离线恢复夹具补上了 P1b 要求的显式完整目录选择，保留原备份、故障注入、重开和数据一致性断言。首次扩大检查暴露这些夹具仍依赖旧隐式默认；修复及最终复验见证据，历史失败日志保留。

组件 vet 通过；全仓 vet 仍有原 `room_action_images.go` 的两条重复 JSON tag 告警，该文件及对应 identity 与 HEAD 相同。全仓业务测试／全仓 race、真实飞书／DSH、浏览器、外部停机备份和发行裁剪未作为本次通过项。

## 后续边界与收益

| 问题 | 可复核结果 |
| --- | --- |
| 新来源是否少改公共代码 | 外部 module 只写声明、状态、Runner、Bind 和本地 codec，无通用 Provider 分支或参数结构修改 |
| 实现是否可选择 | 所选目录决定来源／投影／codec；通用目录的生产依赖不含具体 Provider。实际产品仍包含飞书，不宣称已裁剪 |
| 状态责任是否单一 | 原连接、配置、订阅、目标、输入和附件 owner 均保留；增加的 Host 只返回快照 |
| 验证和维护成本 | 局部绑定测试、两个真实隔离外部 module、原 lifecycle／恢复／图文回归；无第二套状态机 |
| 适配是否有实际用途 | 删除 Room 的飞书准入实现及通用初始化分支；剩余适配只提供快照与同一客户端工厂 |

当前 Room 仍有 35 个生产文件直接引用飞书实现，完整清单在验证证据中。主要落点为：`management.go` 的共享客户端工厂；连接／授权／权限／setup；通知订阅配置、成员操作、视图与离线升级；help／read／actions 工具族；引用／followup、图文和附件；Agent 提示与 UI 更新。执行依赖适配也仍在 Room。它们分别是 P2t／P2r 的输入，不把文件数量当作产品依赖隔离证明。

下一项为 P2t；P2r、P3、P5 仍待完成。此次没有 Gordis 服务、资源裁剪、Codex 或新平台产品接入。
