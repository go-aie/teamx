# Scion Issue／Review 通知接入方案

状态：**N1 按本次授权收敛为 Issue 首批，已实施并完成只读链路验收；N1b 对话配置已实施、验收结果见独立记录；Review 及 N2—N4 尚未实施**。日期：2026-11-27。

2026-11-27 实施范围调整：用户同意第一批先接 Issue。因此本次 N1 只开放 `object_types=["issue"]`，页面明确为 open Issue 新建通知；Review 列表与类型选择留下一批。下文原 Issue／Review 路线继续作为后续参考，不代表本次已经交付 Review。实现和独立验收记录见 [Scion Issue 通知首批](implementation/scion-issue-notifications.zh.md)。

本文依据当前代码、固定 `scn 0.1.13` 及本次线上只读实测编写。本次 N1b 已按用户授权实施，继续沿用 Issue 首批范围；N2—N4 仍需另行授权。本文不启用正式订阅、不修改平台数据；Issue 首批验收见上方记录，Review 及后续动态仍未验收。N1b 实现及当前验证边界见 [Scion 对话配置](implementation/scion-conversation-configuration.zh.md)。

此前的 [Scion 通知与 Review 评论轮询方案](SCION-NOTIFICATION-INTEGRATION-PLAN.zh.md) 以指定 Review 评论为主；本文首期收敛为 **open Issue／Review 的新建发现、开始时间和正式消费**，后续再扩展 open 对象动态。按当前静态平台装配、通知 owner 与统一输入流组织，不为补偿 CLI 增量能力不足改变合理架构。两份方案不应作为两套实现同时建设；后续实施以评估后的本文为准。

## 1. 首期范围与后续目标

首期在**已有 Scion 平台和工具接入上补充通知能力**，注册 `scion/workitems` 来源，编辑内容名称为 **Issue／Review 新建通知**；完成 N2 后再称为“动态”。复用同一 Scion 连接、账号和平台行，不新增第二个 Scion 平台或独立通知连接。一个订阅对应一个已验证连接、一个仓库、一个接收目标；可同时选择 Issue 和 Review，不拆成两个独立账号配置。

**首期只实施 N1**：通过现有成员通知管理页面，选择已验证连接、一个仓库、Issue／Review 和开始时间，轮询原生 open 列表，识别新建对象，投递到一个指定成员的私有 Inbox，再经现有输入工具正式读取、确认。支持暂停／恢复、立即同步、重启去重，以及截断／失败诊断。

首期不读取评论、revision 或评审结果，不做关闭／合入通知，不增加动态类型编辑、仓库替换和对话配置。本节首期边界优先于后续完整设计；§3—§10 中只有 N1 明确列出的职责进入首期。其余是后续开发参考，不能作为 N1 的隐含前置条件。

后续完整目标是在**仍为 open 的对象**范围内扩展：

1. 后台发现查询时仍为 open 的新建 Issue／Review，交付标题、正文摘要、作者、状态和原始链接。
2. 读取 open 对象的新评论、Review 新 revision 和可见评审结果；对象退出 open 集合后停止周期详情读取。
3. 投递到一个明确成员的私有 Inbox，复用现有统一输入存储和 Agent 正式读取；订阅本身不要求 Agent 开启 Scion 工具，也不要求初始化 Workspace。
4. 提供暂停／恢复、立即同步、逐分支进度和范围不完整提示；重启不重放已交付内容。

N1 是独立可交付首期，包含自身的真实只读／页面／消费和恢复验收。N2、N3、N4 是后续路线，另行评估和授权；不能把它们的工作量都归入首期，也不能只完成 N1 就宣称评论动态已经接通。

**推荐接着实施 N1b：Scion 工具／通知的对话式配置。** 它依赖 N1 的配置与投递闭环，直接复用同一配置服务，不依赖 N2 的评论／revision 动态。已有连接可在对话中选择并配置；首次登录仍由既有 Scion 登录界面承接，不新增另一套登录协议。

明确排除：共享项目 Inbox、个人 Scion `inbox`、通知 Hook、核心仓库 Webhook、提交／CI／release 通知、仓库自动枚举、远端评论或状态写入、附件和图片理解、自动 approve／合入／关闭，以及 Linux 多用户 Scion 准入。现有工具和开发流程保持独立。

## 2. 已验证能力及实际限制

### 2.1 本次线上读取

2026-11-27 19:57（Asia/Shanghai），使用隔离凭据副本、账号 `csgship`，读取 `https://scion.opencsg.com`。没有创建对象、发表评论、标记通知已读或配置 Hook。

| 检查 | 实际结果 |
| --- | --- |
| `OpenCSG/scion issue ls --state all --limit 1000` | 515 条，`truncated=false` |
| 同仓库 `review ls --state all --limit 1000` | 689 条，`truncated=false` |
| 按创建时间本地过滤今天 | 12 个 Issue、21 个 Review |
| 按创建时间本地过滤 11 月 20 日至实测时刻 | 184 个 Issue、223 个 Review |
| Issue #512 `show` | 正文、1 条评论全文、timeline 可读 |
| demo Review #1 `show/comments` | 2 个 revision、2 条评论全文可读 |
| 评论按 11 月 7 日 20:15—20:30 本地过滤 | 选中评论 #260 |
| 两类列表传入 `--since` | 均退出 2，原生 CLI 不支持该参数 |
| 两类列表先设 `--limit 200` | 均返回 `truncated=true`，增大总上限才读全 |

列表 JSON 均有 `created_at_ms/updated_at_ms`。本次全量列表的紧凑 JSON 约 1.24 MB／1.09 MB，单次直接 CLI 调用约 1.64／1.39 秒；这只是当前样本，不是生产轮询 SLA。

私有实测材料为 `.local/scion-notification-research-20261127/summary.json` 及同目录响应。关键结果已写入本文，实施和测试不依赖此目录存在，也不能将这些平台正文直接提交为测试 fixture。

2026-11-27 20:40（Asia/Shanghai）追加原生 open 筛选实测：

| 查询 | 实际结果 |
| --- | --- |
| `issue ls --state open --limit 200` | 20 条，`truncated=false`，约 0.61 秒、54 KB |
| `review ls --state open --limit 200` | 0 条，`truncated=false`，约 0.50 秒 |

结果保存在同目录 `open_summary.json`。这与先前全状态读取时间不同，不作为同一快照的性能对照。对当前 open 集合，后续一遍对象详情预计只需 20 次业务查询；N1 连这部分查询都不需要，仅轮询两类列表及必要仓库／账号核验。

### 2.2 决定设计的限制

- CLI 只公开列表总 `--limit`，内部自行分页；没有可跨进程保存的外部分页 token、`--since` 或时间区间。必须在 Scion 适配器中完成时间判断。
- Issue 列表含评论数，Review 列表**没有评论数**。Review 新评论不能只靠列表更新时间或 revision 推断。
- 固定 tag 的 Review 评论写入不会更新对象的 `updated_at_ms`。后续动态采集时，仍为 open 的旧对象在开始时间后收到评论也在范围内，不能因其创建时间早就跳过详情读取；N1 不采集评论。
- `issue show` 带完整评论列表；`review comments` 返回完整评论快照。没有评论外部分页可补读；超大响应必须报告不完整，不能截断 JSON 后当成成功。
- revision 有版本号；评论有原生 ID；评审结果和 timeline 没有统一事件 ID，不能伪造服务端事件编号。
- 个人 Inbox 按收件规则生成且排除操作者本人，不能覆盖共用账号的 Agent 动态。本期直接读取仓库对象，不以 Inbox 是否为空判断是否有动态。

源码基线：上游 `v0.1.13`，提交 `3820ffd1d3a00b82c4c798e1ecc377753b05b499`；重点文件为 `client/cli/src/cmd/{issue,review,inbox}.rs`、`hub/store/src/{issues,reviews}.rs` 和 `docs/design/notifications.md`。不要用本地上游较新的 HEAD 替代固定发布能力。

## 3. 当前代码边界与实施落点

| 层 | 当前入口 | Scion 的职责／修改边界 |
| --- | --- | --- |
| 纯平台装配 | `internal/app/platforms/catalog.go`；`internal/app/builtinplatforms/{gitlab,lark,scion}/definition.go` | 在 Scion Definition 声明 channel、Source、Input codec、展示及只读来源能力；构造时无 I/O、无 worker、无账号查询 |
| 平台运行绑定 | `builtinplatforms/lark/sources.go`、`gitlab/sources.go`；`notification/binding.go` | Scion 新增 `sources.go`，实现 `BindRuntime`／注入 Reader；仅获取窄账号快照，不将 manager 或 Store 交给平台算法 |
| 原生平台实现 | `internal/platform/scion/{profile,arguments,repository}.go` | 新增原生读取 DTO、状态机、去重、时间筛选、输入 codec、内容投影；不导入 `internal/room` |
| 通用通知核心 | `internal/platform/notification/{source,owner,store,delivery,runtime,release}.go` | 复用 owner、Step、持久提交、退避、冻结 Route、投递／来源释放；不增加 Scion 专属字段或判断 |
| 服务装配与生命周期 | `internal/room/platform_notification_bindings.go`、`platform_notifications.go`、`platform_call_lifecycle.go` | 注入已有凭据／程序执行依赖，保留 Room、目标、连接版本及在途调用取消结清 |
| 配置与视图 | `platform_notifications_http.go`、`platform_notification_lark.go`、`platform_member*.go` | 新增薄 Scion 配置／诊断入口；接入现有管理配置和成员配置回执，不复制轮询或存储实现 |
| 输入和引用 | `notification/input_stream.go`、`internal/inputstream`、`room/input_stream_*`、`platform_references.go` | 用已注册 codec 写入当前成员输入存储和读取路径；原生身份解释留在 Scion；首期不进入固定 GitLab 的共享路径 |
| 页面 | `internal/web/assets/features/notifications.mjs`、`inbox.mjs` 及平台设置入口 | 新增 `scion-notifications.mjs` 管理 Scion 草稿和编辑器，复用现有通知位置、目标选择、读取调度和保存提示 |

以下是完整扩展路线的文件组织，沿用飞书命名；N1 只建立实际承载首期逻辑的文件，来源解析、成员对话配置及后续预算文件不预建。已有 `auth.go/profile.go/arguments.go/repository.go` 保留并复用：

```text
internal/platform/scion/
  client.go                       # 有界原生读取、DTO；共享账号预算留 N2
  state.go                        # Config/Binding/State、分支起点、Configure/Retire
  source.go                       # Source 注册、解码、原生事实和去重
  runner.go                       # Step、发现与动态任务的公平轮转
  expiry.go                       # 过期及 SourceTerminal 证据
  notification_capability.go      # Capabilities 及纯投影
  notification_reference.go       # 已保存原生来源的只读解析
  input_stream.go                 # 原生 payload、按对象分流、往返验证
internal/app/builtinplatforms/scion/sources.go
internal/room/platform_notification_scion.go
internal/room/platform_notification_scion_view.go
internal/room/platform_member_scion.go  # N1b：对话入口的薄配置／校验接线
internal/web/assets/features/scion-notifications.mjs
```

文件可在开发中合并，但边界不变；正文构造或请求预算确实变大后，再按飞书已有命名拆出 `notification_content.go`、`budget.go/clients.go`，不预先为每个 helper 建文件。不要把全部逻辑放进 Room，也不要借本次接入重构 GitLab／飞书。当前 GitLab 留有 Room 运行绑定，这是历史接缝；Scion 应优先沿用飞书的显式 `BindRuntime` 形式。

复用以现有接口和机制为单位：沿用 `Source/State/Runner`、`StepHost`、`RuntimeState`、`Route/DeliveryID`、输入 codec、配置版本／回执及 Scion 凭据执行。Scion 仅实现原生 DTO、快照进度、事件身份和账号请求预算，不新增调度器、writer、投递队列或配置操作日志，也不依赖 `gitlab`／`lark` 包来获得这些能力。

**不能只注册 Source。** 当前 HTTP 来源枚举、成员配置、首次启用和 onboarding 仍有 GitLab／Lark 显式分支；它们不是自动支持第三个平台的通用实现，须逐项补齐。

**共享路径有独立限制。** `room/shared_inbox.go` 的接收只接受 `gitlab/events`，并固定 GitLab codec、`Snapshot("gitlab")` 和 `AppendOnce("gitlab", ...)`；`shared_inbox_http.go`、`shared_inbox_maintenance.go` 也有固定 provider。`inputstream.OpenSharedRepository` 当前是一份 provider 分区的发布根，不能直接把多平台记录塞进现有 GitLab 分区。因此首期按飞书当前成员投递路径接入，服务端拒绝 Scion 的 `shared_inbox` 目标，不仅是在页面隐藏它。未来共享扩展须单独设计多平台 codec／分区选择、消费定位、发布、回收和旧根读取；不属于本方案首期。

## 4. 配置和时间语义

### 4.1 首期配置

```json
{
  "enabled": true,
  "platform_id": "已验证连接ID",
  "target": {"mode": "inbox", "agent_id": "接收成员ID", "membership_id": "准确成员身份ID"},
  "repository": "OpenCSG/scion",
  "object_types": ["issue", "review"],
  "interval_seconds": 60,
  "backfill_seconds": 0,
  "backfill_start": "2026-11-27T12:00:00Z"
}
```

- `repository` 接受规范名称或同实例链接，后台调用 `repo info` 核实后保存规范名称。Workspace 仅提供预填，不作为权限、仓库存在或绑定证据。
- 首期一个连接／目标只有一个活动的 `workitems` 订阅、一个仓库。类型非空、去重排序，默认 Issue＋Review。多仓库和多套重叠范围以后扩展。
- 状态范围固定为 `open`，由 Scion 查询构造器明确传入 `--state open`；不增加通用状态筛选框架或首期状态选择项。
- 默认从首次启用时开始；可选“最近一天”或自定义开始时间。复用 `NormalizeBackfill/BackfillBaseline`，相对选项只解析一次并冻结为 UTC 秒，不能每次启动重新算“最近一天”。
- 展示使用已有用户时区，提交显式时区／UTC；不得将浏览器无时区字符串直接存为服务器本地时间。不支持未来起点。
- 轮询间隔默认 60 秒，建议范围 30—3600 秒。它表示发现任务的目标间隔，不是每个对象所有动态的保证延迟。
- N1 支持初次选择类型、开关和间隔；仓库、类型和开始时间在首次保存后固定，范围修改返回明确“不支持本期修改”的结果，不清空进度或偷偷重建。后续按 §4.3、§8.2 增加类型原地编辑和仓库替换，不引入新的用户退役流程。
- 首次接收起点建立后锁定，沿用 GitLab／飞书现有规则；本期不另增重置起点入口。账号身份变化阻止旧源继续运行；同账号已验证凭据恢复仍复用原进度，不能因简化首期而放宽身份门槛。
- 目标使用现有 `mode=inbox + AgentID + MembershipID`。一次订阅只投递该成员，不复制到 Room。多名成员分别订阅时有各自的进度和正文，不宣称共享一份采集／正文；同原生账号的请求预算跨连接／profile／Room 共用。

N1b 只增加与上述 N1 设置等价的对话入口：已有连接选择、初次仓库／类型／时间配置、间隔设置、通知暂停／恢复，以及工具启用／关闭。不提前开放类型编辑、起点重置、仓库替换或额外业务写动作。

### 4.2 时间究竟筛选什么

首次选择类型的开始时间 `T` 是**动态发生时间的下界**，不是“只跟踪 T 之后创建的对象”。后来新增类型使用 §4.3 冻结的分支起点。

| 内容 | 判断时间 | 首轮处理 |
| --- | --- | --- |
| 新建 Issue／Review（N1） | 对象 `created_at_ms` | 查询时为 open 且 `>= T` 才生成新建记录；摘要说明是历史补入或新发现 |
| 新评论 | 评论 `created_at_ms` | 含旧对象的新评论；不能用对象创建时间过滤掉 |
| Review revision | revision `created_at_ms` | 使用原生 revision 号和发布时间 |
| 评审结果 | verdict `created_at_ms` | 保存当次可见结果及 reviewer／revision，不宣称还原已被覆盖的全部历史 |
| open 范围变化（后续） | 本地观测时间 | 仅用于维护活跃对象集合，不根据列表消失生成关闭／合入通知 |

`updated_at_ms` 仅是对象快照字段。页面不把“最近更新对象”和“这段时间的全部动态”当成同一能力。首期订阅不提供结束时间；指定封闭时间段的历史浏览可后续独立扩展。

时间条件使用毫秒时间戳比较，起点包含；同毫秒的多条记录按原生身份分别处理，不用“最大时间戳”去重。不把服务器时钟较新的记录直接视为已成功覆盖，异常时间应诊断并在后续重读。

**沿用全局过期策略。** 当前 `notification.ExpiryPolicy` 默认跳过超过 3 天的输入；自定义起点早于保留范围不代表这些历史一定会交付。配置页展示有效交付下界和过期跳过数，采集实现按 `ExpireBefore` 处理，不暗中放宽全局策略，也不把本次 7 天查询结果冒充 7 天 Inbox 验收。

### 4.3 类型启停保留进度（N3 后续）

参照飞书分支启停和 GitLab 配置修订的原则，将类型选择作为 Scion 的业务配置，保留同一订阅的进度；不直接复用它们的具体状态结构。

| 配置变化 | 行为 |
| --- | --- |
| 首次配置已选 Issue／Review | 所选分支的首次起点均为 `T` |
| 首次增加此前从未启用的类型 | 在成功配置提交中冻结该分支起点为本次配置时间；默认不追溯至老分支的历史起点，页面提前说明 |
| 暂停一个类型 | 不再发起该类型查询；保留其分支起点、目录、去重、扫描位置、状态基线和已接受积压；另一类型继续 |
| 恢复曾启用的类型 | 从保留进度补读停用期间仍可见的事实，遵守过期策略；不重新取 now、不重发旧记录 |
| 整体暂停／恢复 | 所有分支停止／恢复查询，均保留原进度 |

已接受记录保持原 Route，不因随后取消类型勾选而撤销或改投。新类型的分支起点是 State 中的独立字段，不修改原 `Baseline`；`object_types` 非空，整体停止使用总开关。起点与配置版本一起持久保存；重复原操作回执不重新解析起点。

配置变更沿用现有修订围栏：取消旧 Runner、锁外等待后再启动新修订，迟到候选拒绝提交。重配只清理已失效的任务计划，不清空其他分支的已提交进度；被中断查询按保留状态重读。

## 5. 原生身份、内容和去重

### 5.1 绑定及对象身份

账号绑定复用当前 `PlatformID/Generation/AccountIdentity/CredentialVersion/Fingerprint`，账号仍为 `scion:subject`。通知状态不保存 PAT、凭据文件内容、临时路径或完整 Process。

仓库 API 没有公开不可变 UUID；使用已核实的 `canonical_name + created_at_ms` 形成明确命名的**组合仓库身份**，并保留原始证据。配置读取不需要验证分支，不能直接复用要求非空分支的 `VerifyRepository`；在 Scion 包中抽取／新增仓库身份读取，保留原 Workspace 分支验证。

对象身份为 `instance + repository_identity + issue/review + number`，Issue #1 与 Review #1 不相同。每轮完整发现前重新核实仓库身份；同名重建、重命名或身份无法核实不沿用旧进度，暂停并提示重新选择。远端调用和最终提交间的身份变化仍按现有版本围栏拒绝；平台本身不提供快照隔离的竞态窗口须明确保留，不能承诺事务级仓库身份。

### 5.2 事件键

对结构化元组做稳定编码和 hash，不拼接无分隔字符串，也不把账号显示名、标题或观察时间当原生 ID。

| 动态 | 适配器去重依据 |
| --- | --- |
| 对象创建 | 对象身份＋`created` |
| 评论创建 | 对象身份＋原生 comment ID |
| revision | Review 身份＋revision 号；head 不同则报语义冲突 |
| 可见评审结果 | Review 身份＋reviewer＋revision＋verdict＋原生时间；这是组合观测键，不称为原生事件 ID |

首期只生成对象创建键。后续仅以 open 集合维护周期采集范围，不要求还原 `open→closed→open` 中间状态；列表缺项不能证明关闭／合入。若评审结果中有不可区分的重复项，诊断为证据不足，不合成多条确定事件。

`Observed`、待投递 `Inbox`、终态证明与扫描进度在同一候选状态持久提交。只有 Commit 成功才能安装运行缓存、前移扫描位置；投递失败保留原条目和 `DeliveryID`。使用现有 `notification.DeliveryID + Route.DeliveryID`，不另做发送队列。

同一事实只能由一种任务负责生成：发现任务产生 created；后续详情任务产生 comment、revision、verdict，不再生成同一对象的第二条 created。对象重开后再次出现在 open 列表，仍按同一创建键去重，不当成新建对象重发。

### 5.3 内容与来源

- `SourceRef` 保存 `provider=scion/channel=workitems/subscription_id/item_key`、实例、仓库名称、原链接，以及带命名空间的对象／评论证据。
- `NotificationReference` 使用已有对象、ProjectID、RecipientID、NoteID、ParentID 等能表达的字段。revision、verdict 及额外时间保存在 **Scion 自己的 payload** 中；不得塞进 Git SHA 字段或增加通用平台字段并集。
- `notification.Content` 呈现仓库、Issue／Review 编号和标题、动态类型、作者、准确时间和正文；展示截断必须设置 `Truncated` 并说明读取／原链接入口。采集是否完整与展示是否截断分别处理。
- 账号与作者相同不排除事件，不套用飞书本人私聊或 Inbox 的收件规则；也不能从同账号推断是哪名 Agent 写的。
- 原链接由 Scion 路由规则生成并核实；评论深链格式未核实时只指向对象页，不猜测 anchor。

## 6. 采集状态机与预算

### 6.1 首期发现与后续动态任务

**N1 只实现发现任务**：定时读取所选类型 `ls --state open`，完成仓库身份核验和列表校验后，按对象创建时间及原生身份接受新建记录。列表已经包含标题、正文、作者和时间，不为每个对象补读详情；不维护历史对象的动态任务目录。

**N2 才增加动态任务**：以完整 open 列表为活跃集合，公平轮转读取其中 Issue `show`、Review `show`／`comments`，生成评论、revision、评审结果。创建时间早于起点但仍为 open 的对象也可有起点后的动态。

open 范围使用以下规则，无需补全量 all 扫描：

- 仅在成功、未截断的 open 列表中确认活跃集合；查询失败或截断不凭缺项停用对象。
- 已知对象不再出现在完整列表时，移出周期详情任务，保留已接收记录及必要去重证据；不额外排队确认关闭原因，不猜测 closed／landed／删除。
- 详情响应若已为非 open，不生成该响应的新增动态并移出活跃任务，等下一轮发现重新校准。查询开始和事件发生无法做到事务隔离，不声称精确判断某条历史评论发生时的对象状态。
- 重新进入 open 列表时恢复该对象的去重和采集进度；只处理仍可见且满足起点的事实，不保证还原停用期间每一次状态变化。
- 在两次轮询之间创建后又关闭、且从未在 open 查询中出现的对象，不属于本期覆盖承诺；关闭后才出现的新评论／评审变化不再主动追踪。此前已经接受的通知仍正常交付，不因对象后来关闭而撤回。

N1 仅按已选类型轮转列表任务。N2 的发现任务与动态任务交替让出执行机会，不因详情队列阻塞新对象发现，也不让不断出现的新对象永久饿死旧 Review。每个活跃对象分别记录最近成功、下次尝试和错误；单对象不可读不将其他对象伪装为已同步。

N2 中 Review 评论必须独立重读，不以对象更新时间不变、无新 revision、无 Inbox 通知作为跳过条件。Issue 评论数只可用于非强制轮次的提示／优先级优化；该阶段周期性重读活跃对象详情，不依赖“计数没变就无变化”建立完整性保证。N1 不执行详情任务。

### 6.2 列表截断与首次初始化

1. 第一次以 200 为总 `--limit`，截断则依次增大为 400、800、1600、上限 2000；后续可从上次已读全的大小起步。
2. 每次从最新前缀重新读取，持久化下一次请求上限和已确认对象目录；**这不是分页续传**，也不能把不同请求的结果宣称为同一个冻结快照。
3. 完整响应中的新建对象可独立接受、按 ID 去重；列表仍截断时不宣布所选类型 open 范围初始化或本轮发现完成，不依据缺项推断退出或删除。
4. `truncated=false` 只表示此次列表完整，不表示所有对象详情都同步过。发现与动态各自保存成功时间／完成标记。
5. 达到数量或输出预算仍不完整时保留原进度，显示 `discovery_incomplete` 及原因；不推进完整扫描水位，不静默取前 2000 条继续声称 open 范围完整。CLI 能力或规模上限按来源诊断处理，不为补偿它改造核心调度／持久化架构。

首次开启时先持久化准确起点 `T`，再采集。N1 按对象创建时间判断；后续每个分支按其冻结起点判断评论／revision／verdict。初始化失败后继续同一个起点和原 ID，不重新取 now；新增／恢复类型按 §4.3 处理，不重置已有分支。

### 6.3 Runner 和持久状态

实现 `notification.Source/State/Runner`，沿用 `StepHost.Read/Commit`。每次 Step 至多发起一个有界业务 CLI 查询，返回后让出；一轮完整同步由多个 Step 组成，不能在一个 Step 内 for 循环扫完整个仓库。

原生 CLI 内部会分页，凭据准备还会执行 `whoami`，所以“一次源查询”不等于一次 HTTP／RPC。所有前置核验和内部读取受同一个最长 30 秒的 `QueryContext` 约束；Scion 诊断分别统计业务 CLI 和核验 CLI 次数，不把它们冒充真实网络请求次数。

N1 的 State 只需：

```text
RoomIdentity / SubscriptionID / SubscriptionVersion / Retired
Config / Binding / RepositoryEvidence / Baseline / RuntimeState
Discovery[issue|review] = limit、下一次时间、完整时间、截断／错误
NextType / Observed(created keys) / Inbox / 来源释放所需终态证明
```

N2／N3 随对应功能加入以下进度，不在 N1 预建：

```text
RoomIdentity / SubscriptionID / SubscriptionVersion / Retired
Config / Binding / RepositoryEvidence / Baseline
RuntimeState
Branches[issue|review] = 首次接收起点、已初始化、limit、下一次时间、完整时间、截断／错误
Objects[typed number] = open 活跃标记、必要摘要、每类任务进度
NextTask / NextObject / 待核实的有界任务
Observed / Inbox / 与来源释放有关的终态证明
```

只存必要身份、revision／结果键、评论 ID 和有界展示证据，不每轮复制全仓库原始正文。State codec 严格校验 schema、枚举、重复 ID、时间、仓库／对象匹配、冻结 Route；原生响应允许不影响语义的新增字段，但缺少必需字段或未知关键状态须失败，不能默认成 open／空列表。

任务处理顺序为：读取已提交 State／Scope → 克隆候选 → 选择一个到期任务 → 在锁外查询 → 校验响应和当前修订 → 在候选中一起写入事实、Observed、Inbox 和下一任务位置 → Commit → 仅在成功后采用候选。失败不把暂存响应标成已消费；重启根据持久任务位置重新查询，原生 ID 保证重读不会重复投递。

建议初始预算，实施时保留为集中常量并通过实测校准：

| 项目 | 起始值／行为 |
| --- | --- |
| 每种类型 open 列表 | 2000；不完整则显式阻挡该范围成功声明 |
| 单次 stdout／stderr | 8 MiB／64 KiB；超限取消进程，不解析残缺响应 |
| 业务查询 | N1 复用订阅 Runner 的串行 Step 和已有在途调用取消；N2 详情采集时再增加同账号共享客户端请求预算 |
| 连续查询 | N1 按配置周期与共用 slice 预算执行；N2 同账号 FIFO 准入、最小间隔约 1 秒，等待不占 manager／owner 提交槽 |
| 待投递／去重／状态大小 | 复用 `StepHost.Limits`；当前默认 Inbox 10000、Observed 100000、状态 64 MiB |
| 重试 | 复用 RuntimeState 退避；一个对象的暂时错误不重置其他对象进度 |

不能直接使用 Scion 身份探测的 64 KiB 输出收集器承载业务列表，当前真实列表已超过 1 MB。`scion.Prepare` 的小核验输出上限保留，业务读取单独有界。

N2 的账号请求预算参考 `lark/budget.go`、`lark/clients.go` 的共享队列模式：键为规范实例＋带命名空间的原生账号 ID，不包含 PlatformID、profile、Room 或订阅 ID。一个服务中的 Scion 客户端工厂持有共享预算；从工厂创建或复制 Reader 必须保留该预算，不能每次 New 都创建独立队列。原生分页仍由单次 CLI 内部执行，不承诺控制每一条底层 RPC 的间隔。N1 的低频列表采集不新增跨订阅队列或提前建设该工厂。

一次采集查询的账号核验和业务命令使用同一份凭据快照，并共同占用该次准入直到进程结清；排队取消不消耗查询进度，超时／错误／保存失败均释放占用。使用已验证绑定确定预算键，`whoami` 与绑定不符时停止，不能换键继续。测试注入独立预算实例。该机制是 Scion 客户端的执行协调，不是新的通知调度器；本期不将两平台私有预算强行抽成通用框架，也不扩展到 Scion 开发／Workspace 的全部工具调用。

**容量与时效取舍必须可见。** 此前按全状态 515 个 Issue／689 个 Review 估算，一轮详情需要约 1893 次业务查询；那是全历史对象范围，已不作为本方案目标。20:40 的实际 open 集合为 20／0，后续详情量约为 `20 + 0×2 = 20` 次；数据随平台变化，该数量不是固定性能保证。N1 无逐对象详情查询，正常周期仅按选择读取一至两种 open 列表及必要账号／仓库核验。

状态筛选缩小的是明确业务范围，调度、持久化和输入架构不变。CLI 仍缺少增量时间／事件流能力；若 open 对象也很多，展示截断与实际延迟，保留有界轮询，不新增并行扫描框架或改写通用内核补偿上游限制。

## 7. 凭据、取消、恢复与来源释放

### 7.1 原生 CLI 传输

Go 负责调度、状态和去重，**网络传输走所选原生 `scn`**；不直连未公开 Hub HTTP、不引入第二套 gRPC SDK，也不让 Agent 执行常驻 shell。

Room 注入窄的 `NativeExecute/Reader` 能力，复用 `scionProgram`、`scion.Snapshot/Prepare`、`tool.Process` 和 `platformCalls`：

1. 锁内核实当前连接、账号、凭据引用、订阅／目标及程序快照，登记准确在途调用；通知读取不借用某个 Agent 的工具授权。
2. 锁外复制该 origin 的凭据、固定版本及 `whoami` 验证，执行固定参数构造器生成的只读命令。配置不能注入任意 argv。
3. `SCION_ENDPOINT/SCION_HUB` 固定所选实例；保持临时 HOME、私有配置、禁 daemon、过滤 `SCION_TOKEN` 及非必要环境。
4. 输出有界，查询结束或取消均等待进程组退出、删除临时凭据；结清后再完成在途回执。
5. 返回候选前重查账号／凭据／订阅修订，后续 Commit 再受通用 Scope 围栏检查；旧调用迟到结果不能写进新配置。

`RuntimeHost.Account/States` 仅能在 Admit／CheckLoaded 的既有门槛内使用，不能被 Runner 缓存回调后在任意时间调用。平台 Runner 只持有具体 QueryScope 和注入 Reader。

### 7.2 生命周期

- 暂停保留起点、对象目录、去重和积压；恢复补读暂停期间仍可见的事实。只读查询可重试，不触发任何远端业务写入。
- 同账号重新认证仅更新经过验证的凭据绑定，保留进度；不同账号、连接 generation、实例或仓库生命周期须隔离旧源，不能清错后接着跑。
- 连接停用／退出登录、Room 关闭、目标移除、配置替换与服务退出均接入既有通知取消及 `platformCalls` 结清；新查询在撤权后被阻止。
- 私有成员移除接入既有准确 Membership 取消和冻结 Route；一个成员的订阅不授予其他成员读取权。采集配置不授予成员远端工具访问权，已接收正文按既有本地访问及保留规则处理。
- 不把 native exit 5 一律解释成 PAT 过期：它也可能是对象权限不足。只在身份核验或可信分类确定凭据失效时进入 `credentials_invalid`；业务查询权限／不存在错误保留为该仓库或对象诊断。
- JSON 损坏、缺字段、重复 ID、对象号不匹配、进程超时、输出超限均不推进该任务成功位置。NotFound 不等于空评论，更不等于已删除。

### 7.3 与统一输入存储闭环

Scion State 除基本接口外实现 `Expire(cutoff)` 与 `SourceTerminal(item)`，参照当前 `lark/expiry.go`，让过期跳过、已确认来源释放、正文回收及容量恢复可核验。

去重和终态证据的保留不能只按“已投递就删除”；仍可重读、仍有来源回执／页保护的记录必须保留。只压缩已有持久过期下界或准确终态证明覆盖的事实；证据不足返回 unknown，不能为释放空间伪造成功。

## 8. 当前输入、引用和配置入口的完整接线

### 8.1 静态声明与输入流

在 Scion `Definition()` 中声明 `NotificationChannels=["workitems"]`、Source、Bind 和 `NotificationCapabilities()`。工厂和 codec 构造无副作用，空／受限 Catalog 不出现 Scion 来源，也不能从 standard composition 偷补。

Input codec 采用独立版本化 Scion payload，校验原始 SourceRef、Route、事件证据和 DeliveryDigest 往返一致。流分组为 `实例＋采集账号＋订阅＋组合仓库身份＋对象类型＋编号`，同一对象的创建和动态进入同一流，不按评论分成几千个流，也不合并不同仓库的同号对象。

同一 codec 支持当前私有正文存储、History／Agent 输入读取、成员消费与输入列表展示；不恢复旧 AgentInbox 存储、旧 CLI 消费协议或直接向模型塞通知正文。共享存储留待 §3 所述独立扩展。

N1 保存准确 SourceRef、对象身份和原链接，完成 codec 往返、输入正文及来源展示即可；不以完整 followup／远端重读能力为前置条件。N3 再补只读来源解析与现有 followup resolver：使用已保存原生 envelope，不宣称核实了当前远端状态；成员没有独立工具／账号绑定可以继续读已授权快照，远端重读明确 `binding_unavailable`。不注册任何写动作、不将采集凭据冒充成员凭据。

### 8.2 管理配置与 API

复用 `/v1/platform/notifications` 的读取，以及 `/configure`、`/sync` 的管理入口：显式 `provider=scion/channel=workitems/subscription_id/subscription_version`，配置带准确 target。新建必须显式观察到版本 0；已有配置按当前版本冲突，不自动刷新版本覆盖。

当前 POST 路由只接受 `lark/messages`，读取枚举也只列 GitLab／Lark；在原路由增添 Scion codec／配置分派和诊断视图。新增业务校验放 `platform_notification_scion.go`，不能为了接入重新造一套 Scion HTTP 调度服务。

`notificationView` 增添可选 Scion 诊断：仓库、发现和动态各自状态、已知对象数、每类／逐对象最近成功、最旧待同步时间、列表上限／是否截断和原因。公共 RuntimeState 的 status 枚举不加 Scion 值：例如数量／输出预算阻挡使用既有 capacity，具体原因放 error code／Scion 诊断。

N1 只接管理读取、configure、sync 及暂停／恢复，展示起点、每种 open 列表的完整时间／截断、pending 和原生错误；不提前增加动态任务诊断。已保存仓库或类型修改直接拒绝且不改进度。下面的身份替换流程留到 N3，N1 不为它增加内部阶段状态。

仓库／账号身份替换沿用 `platform_notification_config.go:configureEventsSubscription` 和 `platform_notification_lark.go:configureLarkSubscription` 的生命周期模式，全部由现有 configure 入口协调：

1. 按当前 subscription ID／观察版本核实旧源，锁外验证所选账号和新仓库；返回后重新核对账号、目标及配置版本。
2. 在配置流程内部持久退役旧源，保留原积压／Route；取消旧 Runner 和在途查询，锁外等待结清。
3. 重新核对旧源退役修订、当前账号／仓库证据、接收目标，并确认没有并发建立的活动源后，保存新 ID 的替代源，再启动 Runner。普通类型／间隔／开关修改按 §4.3 更新原源，不走替换。
4. 替代源保留本次冻结的全局及逐类型接收起点，重新建立该仓库的目录／去重；不能用全局旧起点覆盖后来启用类型的起点，也不得将旧仓库进度用于新仓库，或在等待后重新把起点改为 now。成员操作回执和诊断保存返回的新 subscription ID／version。

这是有中间状态的持久流程，不能承诺跨两份快照原子替换。若退役后失败，保留旧源已退役和新源未建立的实际结果；页面显示需处理的配置状态。恢复先读取原操作回执及当前订阅，沿用既有冲突／unknown 规则，不自动重发、不复活旧采集，也不绕过成员配置的冻结上下文检查。读 get／status 不执行恢复写入；旧 ID 的迟到候选不得提交至替代源。

成员配置的内部应用上下文需准确辨认“本次操作已持久退役旧源”的阶段和修订，避免将自己的中间提交当成其他请求改了配置；只接受与原冻结输入相符的旧／退役源身份，不能通过重读最新版本放宽检查。该阶段沿用已有成员操作回执及应用上下文表达，不增设另一份操作日志；重启后原 unknown 仍只读核查，不自动续写。

不新增 `/retire` API、独立重订阅操作日志或要求用户“先结束旧关注再创建新关注”的流程。内部 Retire 和范围替换保护仍保留，由同一个配置服务承担；配置成功后页面回填实际订阅身份，结果不确定时按原回执／准确来源查询。

### 8.3 成员对话配置与首次启用（N1b）

N1 复用现有页面管理 API，不新增 Agent 对话配置命令、完整 onboarding 通知步骤或成员配置回执字段。现有 Scion 连接／工具流程保持可用；在 N1b 完成前，相关入口明确尚未开放 Scion 对话配置，避免误入 Lark 默认分支或宣称已经启用。这些是必要入口保护，不是另一套配置服务。

N1b 复用既有 `platforms member get/tool/notifications/status` 及账号选择交互，增加 Scion 的明确分派。工具操作仍进入当前工具绑定服务，通知操作仍进入 N1 的 `configureScionSubscription` 等配置实现；不得由对话入口另写订阅文件、启动采集循环或复制一套配置逻辑。

本阶段的接入范围：

| 用户意图 | 行为 |
| --- | --- |
| “给你启用／关闭 Scion 工具” | 定位当前成员及准确已有连接；启用、恢复或建立工具绑定走既有配置流程，关闭走原取消／结清路径 |
| “关注这个仓库从今天开始的新 Issue／Review” | 复用 N1 的仓库验证、初次类型选择、开始时间和成员目标；保存后通过同一个列表查询／输入链路交付 |
| “暂停／恢复 Scion 通知” | 使用现有简化总开关模式，保留起点、范围、进度和准确原回执；工具状态保持 |
| 多个可用 Scion 连接 | 扩展现有账号选择卡片；选择结果携带准确连接 ID／版本和成员范围，不让模型猜账号或创建重复连接 |
| 没有可用连接／账号需重新登录 | 引导到现有 Scion 连接／PAT 登录界面；账号未核实前不启用工具或通知。用户登录后继续对话，重读真实连接及版本再配置，不新增后台自动续配流程 |

已有工具可用不等于这些入口已经支持：当前 `platform_member_store.go` 的 provider 校验、`platform_selection.go` 的选择分派及 `platform_connect.go` 的对话连接入口仍主要限定 GitLab／Lark。N1b 补已有连接的只读枚举／选择和成员配置；**不将 Scion 加入完整对话 connect 登录状态机作为本阶段前置条件**。首次登录只复用现有 UI，PAT 不进入 Room、模型参数、选择回执或成员配置 JSON。

当前 `platform_member.go`、`platform_member_current.go`、`platform_member_store.go` 仍有两平台假设，需要补齐：

- 添加 Scion 的明确输入分支和 Config／回执字段；通知配置使用独立的 Scion 结构，校验不同平台字段互斥，不能落入 Lark 默认分支。工具配置只接受当前工具绑定需要的字段，不借用飞书动作授权字段。
- `member get` 返回 Scion 准确订阅／版本、冻结起点和配置；纯开关恢复既有配置，保留范围、进度和原操作回执。
- 对话配置与 N1 页面受同一修改边界约束：初次保存后锁定仓库／类型／起点，N1b 不提前增加逐类型起点或替换阶段。N3 开放这些能力时再让两入口同步升级。
- 首次纯开启没有仓库时返回“请选择仓库”，不隐式读取 Workspace 后启用；显式仓库配置与开关可合在同一次配置操作中。
- 复杂配置保留 human SourceSeq、稳定 OperationID、冻结输入和准确版本；`unknown` 回执重复请求只返回原结果，不自动再 apply。
- 旧 Scion 工具连接加载后仍不自动启用通知；保存连接、使用工具和接收通知是独立选择。完整平台 onboarding／首次登录后的自动配置联动留 N3，不阻挡本阶段已有连接的对话配置。
- 同步更新 CLI 参数／help、平台配置指引及原生工具提示，避免模型自行运行 `while scn ...`。Scion 参数使用独立的结构，不复用 GitLab ProjectID 或飞书 chat_ids。

回执涉及扩展 schema 时显式新增版本并保留已有合法回执读取，不把旧记录缺少 Scion 字段视作需要自动开启。第一批源码检查应列出所有 provider 分支，不能只改 HTTP 和可见表单。

工具和通知同时被用户请求时，可以沿用同一原始 human SourceSeq，分别执行现有 tool／notifications 操作，各自记录结果；一项成功另一项失败时如实报告，不添加跨两类配置的原子事务或统一回滚。不确定结果按原操作 ID／SourceSeq 查询，不在新消息或新 ID 下盲目重放。

建议接线位置：`platform_member.go`、`platform_member_current.go`、`platform_member_store.go`、`platform_member_cli.go` 和新增的薄 `platform_member_scion.go`；账号选择扩展 `platform_selection*.go` 与 `platform-interactions.mjs` 的既有卡片分派，补全准确 Scion 类型而不是按非 GitLab 回落飞书。同步更新仓库内 `internal/platform/tool/skills/platforms/SKILL.md`、`notifications.md`、选择主题及 CLI help。这些是产品内 Agent 指引，不另建 Scion 对话编排框架。

## 9. 页面体验

### 9.1 复用现有 Scion 平台行和通知编辑器

当前 `members.mjs` 已渲染同一平台行的名称／账号、通知入口及工具开关；Scion 目前缺少通知 channel，显示“无通知能力”。N1 沿用这条平台行和右侧普通宽度的通知编辑器，视觉和操作层级与 GitLab／飞书一致：

- 在原 Scion 行补上“通知”开关和编辑入口；未配置显示“尚未开启通知”，配置后显示仓库／类型摘要。保留原名称、图标、账号状态、工具开关和管理连接入口。
- 从该行打开时，自动带入准确 PlatformID、已验证账号和接收成员。连接作为已选上下文展示，不要求再次选择相同连接、登录、输入 PAT 或填写 CLI 路径。
- 第一次打开通知开关但尚无仓库配置时，进入同一个通知编辑器完成配置；不自动选仓库、不调用新增登录流程，也不误入既有 Lark 默认分支。后续开关直接暂停／恢复准确订阅。
- 保存、加载、错误、未保存草稿、立即同步和返回行为沿用现有通知 UI 的样式与交互；Scion 模块仅提供自己的字段、摘要、校验和请求分派，不另建设置页、弹窗系统或一套外观。
- 工具与通知保持独立：开启通知不重新绑定／开启工具，关闭通知不关闭工具；已有工具授权、账号及 Workspace 保留。连接尚未建立时才进入既有 Scion 连接流程。

必要接线集中在 `members.mjs` 的平台详情跳转，以及 `notifications.mjs` 的来源选择、`notificationView/notificationDescription/togglePlatformNotification/openNotificationSource` 等现有分派点。为 Scion 增加明确分支和 `workitems` 编辑器上下文，不能让当前“非 GitLab 则按 Lark 处理”或“其他平台跳工具详情”的兜底继续生效；不为此重构整个前端为通用插件框架。

共享项目 Inbox 的来源选择不提供 Scion，直接 API 同样拒绝该目标。

### 9.2 首期字段与反馈

首次配置只需填写：**仓库、Issue／Review、开始时间**；连接和接收成员来自所打开的平台行。间隔放高级设置。仓库先提供同实例 Workspace 的可编辑预填和“验证仓库”；首期无需建设全实例仓库目录。如果从非成员上下文进入，复用现有连接／目标选择，不重复已有选择。

N1 保存后锁定仓库、类型和起点，仅保留暂停／恢复、间隔和立即同步；展示初始化中、open 列表同步正常、暂停、重试中和范围不完整。清楚标注“仅接收 open Issue／Review 的新建通知，不含评论和评审动态”。“立即同步”仅唤醒既有 Runner，不开第二个扫描器或绕过凭据／容量门槛。

N2 再显示动态追赶状态；N3 才开放类型原地编辑／分支起点和同入口仓库替换。页面文案随已实现能力升级，不提前展示后续能力承诺。

N1b 的账号选择复用已有对话卡片样式，复用同一 Scion 平台行／设置页定位；不因从对话进入而再创建通知专用账号或登录表单。对话配置保存后，原平台行与通知编辑器立即反映准确账号、工具开关、通知开关和保存结果。

草稿与请求响应按 Room instance、PlatformID、目标、subscription ID／version 和本次读取序号隔离。切换连接、目标或服务 epoch 后，迟到响应不得回填别人的草稿。后台 SSE 更新只刷新已保存状态，不覆盖用户正在编辑的表单。

N1 的 Inbox 显示如“Scion · OpenCSG/scion · 新建 Issue #516”；后续再增加新评论等动态类型。不从 open 列表缺项生成关闭／合入提示。技术 ID、去重键和扫描 limit 留在诊断，不进入主要业务正文。外部内容按现有 Markdown／链接渲染规则展示。

## 10. 测试与验收

文档调研不新增实现测试。以下是完整路线测试矩阵，N1 只做 §11 列出的首期范围；N1b 单独验收对话配置，评论／revision、动态类型修改和仓库替换随对应后续阶段增加。使用最小代表夹具，不复制整套 GitLab／飞书测试矩阵或线上千条正文。

| 层次 | 必须覆盖 |
| --- | --- |
| Scion 原生适配器 | schema／字段、原生 open 筛选、两类同号、毫秒边界、重复快照、截断扩大及上限、NotFound／exit 5 分类；N2 再测旧 open 对象新评论、同毫秒不同评论、revision 冲突、评审组合身份、超大评论 |
| Source 状态机 | 每 Step 有界、两类型轮转、暂停恢复／重启、首次固定起点、分支错误隔离、保存失败不前移、过期与 SourceTerminal、容量阻挡保留原 ID；后续再测 open 集合退出／重新进入、详情非 open 拒绝、截断不能判退出、两队列公平及类型编辑 |
| 装配和生命周期 | Catalog 声明一致、无 I/O 构造、空／受限目录、取消／超时进程组结清、临时凭据清理、换号／同账号换凭据、迟到响应围栏、只开通知不授工具；同账号跨连接／profile／Room 共用请求预算、FIFO 公平／排队取消、不同账号隔离及错误释放 |
| 配置入口 | 管理与成员配置结果一致、精确版本、纯开关／首次缺仓库、原 unknown 回执、onboarding、旧工具连接不自动启用；类型修改保留 ID／积压；仓库替换内部退役／结清、退役后失败与并发替换、返回新 ID／version |
| 输入闭环 | codec 往返／摘要、同对象分流、不同成员各自订阅／消费、私有隔离、正式读取和持久确认、应答丢失／重启不重复、来源释放与到期回收；共享目标明确拒绝 |
| N1b 对话配置 | 已有单／多连接选择、初次工具启用／关闭及保存绑定恢复、初次通知配置／总开关、工具通知独立、UI／对话结果一致、原 human 依据／成员隔离、旧版本冲突、超时／unknown 原回执防重、缺仓库不乱启用、登录界面引导／无凭据泄露、双配置部分成功如实反馈 |
| 页面 | 原 Scion 平台行增加通知开关／同风格编辑入口、自动带入既有账号／成员、不重复登录／绑定工具、通知与工具开关独立；N1 类型初选／锁定、时区、过期边界、未读全提示、草稿和迟到响应隔离、成员切换、共享页不提供 Scion、桌面／手机布局；后续再测类型编辑／新分支起点和仓库替换 |

建议阶段命令，以届时实际测试名缩小范围：

```sh
make test TEST_PACKAGES='./internal/platform/scion ./internal/app/builtinplatforms/scion ./internal/app/platforms'
make test-race TEST_PACKAGES='./internal/platform/scion ./internal/app/builtinplatforms/scion ./internal/room' TEST_RUN='TestScionNotification|TestScionWorkitems'
node --test internal/web/tests/web_scion_notifications_test.mjs
```

输入存储／通用核心发生修改时再跑其相关包；完成阶段后运行受影响的集成检查。`test-e2e-check` 仅证明编译，不能替代真实后端、模型或浏览器验收。

真实验收分开记录：

1. **只读平台基线**：固定 CLI／隔离凭据，N1 重复两类 open 列表读取，对照创建时间筛选、截断、输出大小和耗时；N2 再验证非空详情／评论，不要求人为制造新事件。
2. **隔离产品链路**：在临时服务根通过正常配置入口建立 Scion 订阅，采集真实可读历史，进入成员输入并经正式读取确认；验证重开、去重、成员隔离和配置页。共享目标拒绝属于本期负向门槛。
   N1b 另做真实模型对话：选择已有连接、启用工具、配置通知并核实 UI 保存结果，再实际调用只读 Scion 工具、正式读取已采集输入；既有工具集刷新／Session 生命周期按当前机制验证，不仅检查配置回执。首次账号登录引导和准确选择分别保留页面证据，不用模拟卡片替代浏览器验收。
3. **真实新增事件**：使用另行明确授权的测试仓库／测试对象写入 Issue、Review、评论、revision 和评审结果，再验证发现及读取；共享账号场景至少包含操作者与采集账号相同的事件。写入前列出具体对象和操作，未获授权时该门槛保留为未验，不用旧历史样本替代。
4. **代表规模和时效**：分别记录新建发现延迟和动态追赶延迟；在目标仓库规模下检查原生进程／核验次数、响应字节、State 大小、最旧等待及对其他来源的影响。不从单次 CLI 1 秒推导全仓每分钟同步。

真实 DSH、真实浏览器和跨进程恢复各保留独立证据；组件测试通过不代表这些门槛通过。本方案不授权对正式服务启用采集。

## 11. 开发分期与完成条件

推荐顺序：**N1 → N1b → N2 → N3 → N4**。N1b 只依赖 N1，可在尚无评论动态时独立交付；N2 的采集器不依赖 N1b，不得反向要求先改对话体系。N1 保持首期最小实施单元，后续每个阶段单独授权和验收，不自动并入 N1。

### N1：首期最小完整交付

仅实现以下链路：

```text
成员通知管理页／既有管理 API
  → 已验证 Scion 连接＋一个仓库＋初次类型选择＋开始时间
  → 原生 issue/review ls --state open
  → 创建时间过滤＋原生创建键去重
  → 共用 notification 持久化／投递
  → 指定成员的现有输入存储、正式读取和确认
```

所需新增：两类列表 DTO、仓库身份核验、精简 State／Source／Runner、静态注册、原生 Input codec、有界内容及薄管理 API／页面接线。页面直接扩展已有 Scion 平台行和共用通知编辑器，复用账号／成员上下文及原工具开关。复用已有 Scion 凭据／程序执行、RuntimeState、Route／DeliveryID、过期和 SourceTerminal、取消结清。正常周期不调用对象 show／comments。

首期配置只有初次创建、暂停／恢复、间隔和立即同步；锁定仓库、类型与起点。不同账号不能继承原源，同账号凭据恢复不丢进度。对话配置明确留 N1b；动态类型编辑、分支新增起点、仓库替换、跨订阅账号 FIFO 工厂、完整 onboarding 通知和 followup 扩展均留后续阶段，不预建空壳接口／进度字段。

必须完成的验证：正常页面配置和 open 过滤、时间边界、重复列表／重开对象不重发创建、列表失败／截断、保存失败不前移、暂停／重启、账号变化／撤权／退出取消、输入正式读取确认、应答丢失防重、过期／来源释放及容量保护。真实只读历史经隔离服务投递并被正式读取，页面和重启各有独立证据；不需要创建真实 Issue／Review 来完成首期只读交付。完整新增事件端到端写入验收留待明确授权。

完成条件是上述路径独立可用。只标注“open Issue／Review 新建通知”，不声称支持评论／评审动态或通知对话配置。N1 验收完成即可评估交付，不等 N1b 或 N2—N4 完成。

### N1b：工具／通知的对话式配置

依赖 N1 已有的 Scion 管理配置和投递闭环，按 §8.3 增加已有账号选择、工具启用／关闭、N1 通知初次配置、间隔设置和暂停／恢复；采用现有成员配置服务、准确版本、human SourceSeq 和操作回执，不改采集、输入存储或 Session 协议。

典型验收请求为“给你启用 Scion 工具，并关注这个仓库从今天开始的新 Issue”。工具与通知各自有准确结果；多账号时使用既有选择卡片，缺少连接时引导已有 Scion 登录界面，用户完成后继续配置。该阶段不建设全程对话式首次登录、PAT 对话输入、账号自动登录后后台续配、仓库替换或动态类型编辑。

完成条件：UI 和对话操作同一连接／成员时读到一致配置；真实对话能启用工具并完成只读调用、建立通知并经正式输入读取；工具／通知相互独立，错误与部分成功如实反馈；身份／版本冲突、退出撤权和 unknown／应答丢失不引起串号或重复配置。更新 Agent 指引、CLI help 和选择卡片文案，并保留真实模型及页面证据。

N1b 可单独实施和验收，不以 N2 评论动态、N3 全套高级配置或完整平台登录状态机为前置条件。

### N2：对象后续动态的完整切片

只对当前 open 集合增加公平详情任务、新评论、revision 和可见评审结果，以及按原生账号共用的请求预算；覆盖旧 open 对象新评论、退出集合后停止、重新进入防重、共享账号、超大响应、单对象错误、迟到结果和保存失败。补对应页面进度、输入正文和 Agent 指引，将能力描述升级为 open Issue／Review 动态。

完成条件是各种已声明动态均有可验证来源、时间语义及重启去重；不能用 Review 更新时间替代评论采集。

### N3：跨入口、成员隔离与恢复收口

扩展类型原地编辑／逐类型起点、同配置入口仓库替换、来源引用和完整 onboarding 通知联动；让 N1 页面与 N1b 对话配置同步支持已声明的高级设置。补跨入口、私有目标／共享目标拒绝、连接换号／撤权／退出、跨连接同账号预算、容量和回收的组合测试；回归 GitLab／飞书及空 Catalog。

N1／N1b／N2 各阶段已更新自己的用户说明、CLI help 和 Agent 指引；本阶段只补高级能力及组合边界，不把基础文档更新或 N1b 已完成的工具／通知对话配置再次列作新增功能。独立的完整对话式首次账号登录不默认纳入，确有需求时另行评估。

完成条件是同一配置服务覆盖所有已声明入口，权限和生命周期没有依赖偶然的工具绑定或 Workspace。

### N4：真实规模验收与交付决策

按 §10 验证真实只读平台、隔离服务／模型／页面、重启及代表规模。真实新增事件写入和正式部署分别以明确授权为前提。

若 open 对象规模下的动态时效仍不满足期望，报告准确范围、延迟和上游能力限制；是否推动上游增量接口另行评估，不为此改变合理架构，也不偷偷扩成 all 或缩小已选 open 范围。各阶段报告逐项列出通过、未验及部署边界。

## 12. 评估要点

首期是一个窄的 Scion 来源扩展：open 列表发现、创建时间过滤、原生 ID 去重，再复用既有通知／输入链路。它仍需必要的状态 codec、配置／页面接线和可靠消费验证，不能简化为几条 CLI 命令，但不需要改造通用通知内核、输入协议、Session 调度或持久化框架。

N1b 用现有对话配置机制表达 N1 同样的工具／通知设置，不建设第二套状态机。后续动态采集仅覆盖 open 对象，查询量取决于活跃集合而非全历史仓库。CLI 缺少增量事件能力属于上游限制，保留有界轮询和准确诊断，不以复杂架构补偿。N1 先独立验收，N1b 可随后独立交付，其余路线按实际需要再启动。
