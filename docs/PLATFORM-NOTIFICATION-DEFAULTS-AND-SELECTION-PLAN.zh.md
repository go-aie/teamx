# 平台通知默认值与对话选择路径优化方案

状态：第 1 步「总开关语义切片」已实施，见[实施记录](implementation/platform-notification-toggle.zh.md)；第 2 步「一步入口切片」已实施，见[实施记录](implementation/platform-notification-current.zh.md)；第 3 步已实施，真实模型＋浏览器独立重复重试 4／4 通过，见[实施记录](implementation/platform-notification-selection.zh.md)；此前 HTTP 429 与行为偏差证据保留，真实提供方读取保留未运行门槛。更新日期：2026-11-19。方案代码基线：HEAD `6e56e3c`。

本方案采用**纯通知开关一步完成**：Agent 只传准确平台 ID、原 human 消息 seq 和开关，服务端读取并冻结状态，复用现有配置与回执机制。取消前版的 ref 改造，不新增引用、签名密钥、观测记录、配置草稿或持久化 schema。当前三个切片的代码与指导均已实施；第 3 步本轮真实模型＋浏览器独立重复验收通过，真实提供方读取仍未运行，具体证据及历史失败见实施记录。

## 1. 当前代码与实施边界

| 当前实现 | 本次改动 |
| --- | --- |
| 手动首次通知开关经 `onboarding.mjs`、`platform_onboarding.go` 使用默认配置；已有通知由 `notifications.mjs` 复制原配置、只改总开关 | 对话入口采用同样的首次默认／已有配置恢复语义 |
| 飞书 `platform_member_lark.go` 首次定制时关闭默认来源；已有双分支纯恢复会被防扩张检查拦截 | 分开处理纯总开关和显式来源修改 |
| GitLab `platform_member_gitlab.go` 首次启用待办符合默认范围，但 `platform_member_store.go` 拒绝通知总开关 | 支持通用总开关，保留单来源与复杂修改检查 |
| `configurePlatformMember` 入口要求完整版本、操作 ID；原回执按完整输入比较 | 新增仅供纯开关使用的服务端观测入口，原回执优先恢复，首次提交才补齐版本 |
| `platform_selection.go`、`platform-interactions.mjs` 已有账号、群、项目选择组件 | 改善 Agent 任务入口，直接复用组件 |

2026-11-18 的“飞书选择群的卡片”用了 19 次工具调用、约 176 秒：未读平台 Skill、未调用 selection，而是遍历群、遇到 502 后搜索源码，最终发文字清单。实际模型输入已有平台 Skill 导航，不能归因于新输入链路丢失指导；本轮没有调用卡片接口，也不足以证明卡片接口故障。

本次只简化纯总开关。群、人员、项目和筛选的复杂修改继续使用现有完整观测参数；选择组件继续使用现有命令。无需为了它们一并建设通用简化协议。不改外部发送授权、OAuth、输入流、调度或长期规则，不新增人员选择组件、完整通知编辑卡片与共享草稿；相关扩展仍见[对话式平台设置方案](CONVERSATION-PLATFORM-SETTINGS-PLAN.zh.md)。

## 2. Agent 命令契约

### 2.1 开关无需先 get

已有准确平台 ID 时，直接调用：

```sh
"$TEAMX_CLI" platforms member notifications \
  --platform-id ID --source-seq SEQ --enabled true
```

关闭改为 `--enabled false`。Agent 不生成操作 ID、不填写两个版本，也不先调用 member get/status 来补参数；原回执恢复由该入口完成。“一步”指配置变更只需一次业务调用，不免除读取用户消息、必要的 Skill 导航和有歧义时选择账号。

`ID` 来自已核实的准确平台；唯一有效账号直接复用，不能猜别名或自动换账号。`SEQ` 由服务端分配，Agent 从本次依据的 human 消息读取并透传；跨回合沿用原请求，一轮多条消息时不能自动取最新一条。成员身份由现有执行环境与服务端核验，不增加成员参数，也不建设自动任务上下文绑定。

简化模式表达“切换当前平台通知”，以服务端本次受理时的状态为准，不承诺检测受理前已发生的配置变化；它不是针对 Agent 先前读取版本的条件写入。如果任务依赖先前观测的准确账号或配置版本，仍使用完整模式。服务端冻结后发生的变化则必须拒绝应用，两者不能混淆。

查询只是可选读取，平台选择形式保持一致：

```sh
"$TEAMX_CLI" platforms member get --platform-id ID
```

`get` 默认返回当前已核验平台及配置的版本，供解释现状或后续复杂修改使用；显式 `--platform-version V` 仍表示准确版本读取，过期须冲突。不要为纯开关强制插入查询。

### 2.2 新旧输入明确区分

在 `platform_member_cli.go` 利用 `flag.Visit` 判断参数是否实际出现，不能只看数值是否为 0：

- 仅 `platform-id + source-seq + enabled` 是简化纯开关；`enabled` 必须显式传入，`false` 不能被当成缺省。
- `platform-version`、`notification-version`、`operation-id` 均未出现时才进入简化模式；任意出现就按完整模式要求全部必需字段。显式 `notification-version 0` 仍是“已观察到无订阅”，不能解释为自动观测。
- 缺少版本／ID且携带任意来源、范围、筛选、绑定字段的请求直接拒绝，不能静默扩大简化入口。未知字段、空 patch 同样不得混入。
- `get` 只有省略平台版本才启用当前状态读取；显式版本为 0 拒绝。`chats/projects/tool/status` 不扩展为自动观测模式。

HTTP 仍使用 `/v1/platforms/member/notifications` 与 `/v1/platforms/member/get`。建议在 `platformMemberRequest` 增加**仅用于请求**的 `ObserveCurrent bool`（JSON `observe_current`），由 CLI 在上述短形式下设置；HTTP 端独立校验形状，不能信任 CLI。它不是 Agent 新参数，不进入 `platformMemberInput`、操作回执或持久化 schema。

`observe_current=true` 的通知请求只允许 `SourceSeq`、`Input.PlatformID`、`Input.Enabled` 及既有绑定身份；客户端操作 ID、平台版本、通知版本必须缺省，其他业务字段均须缺省。原完整模式与内部持久化校验继续要求非零平台版本和完整内部请求，不能直接放宽 `validPlatformMemberInput` 来接受所有不完整输入。

## 3. 服务端开发设计

### 3.1 稳定操作 ID 与原回执优先

服务端对简化开关使用固定域标识，如 `platform-member-notification-toggle/v1`，将以下字段按稳定结构序列化后取 SHA-256 的前 16 字节，输出现有要求的 32 位十六进制操作 ID：

`RoomID + RoomInstance + AgentID + MembershipID + PlatformID + Kind=notifications + SourceSeq`。

不要拼接无分隔字符串；不要把 Enabled、当前配置版本、turn、服务 epoch 或时间放入 ID。这样同一来源改成相反开关会冲突，重启／换回合／配置已增长后仍定位同一记录。不同成员、平台或新的 human seq 则独立。

在 `configurePlatformMember` 的完整参数校验之前分派简化请求，建议使用一个小的 prepare helper；不要在 HTTP 外壳中“补最新参数再递归调用完整入口”。原入口比较的是完整输入，成功后的重试若重新补新版本，必然与原输入冲突。

简化请求的原回执匹配只比较准确成员、平台、类别、原 seq、纯开关形状和 Enabled；版本来自已保存 `op.Input`，不是客户端补写或重读。恢复时仍校验当前调用者拥有该成员身份，但不要求旧账号当前仍可用，也不以当前版本变化否定历史回执。返回历史完成结果不等于声称当前配置仍然开启。

同来源已存在旧完整模式的不同 ID，或已有更新来源操作，继续由 `checkPlatformMemberSourceLocked` 拒绝；错误指向原操作供 status 恢复，不能创建平行记录。不要未经匹配就把某个同来源复杂配置回执视为本次纯开关成功。

### 3.2 首次观测、冻结和保存必须在同一锁段

建议控制流程如下，函数名可按仓库习惯调整：

```text
校验简化请求形状，派生稳定 ID
lock manager.mu
  checkToolMemberLocked：准确绑定身份与生命周期
  readPlatformMemberLocked(ID)
  若已有：匹配纯开关意图后返回原回执，不写配置、不重新观测
  非 NotExist 的读取错误：停止，不把损坏／不可读当成不存在
  checkPlatformMemberSourceLocked；读取并核验原 human 消息
  按准确 PlatformID 找当前已核验平台，取得 PlatformVersion
  platformMemberViewLocked：取得配置、NotificationVersion、订阅身份与账号快照
  构造完整内部 req.Input、platformMemberAdmission 和规范化目标配置
  无实际变化：保存 completed/preserved 原回执并返回
  有变化：保存现有 unknown/submission_unconfirmed 回执，包含冻结输入和配置
unlock
用冻结 admission、版本和配置进入现有通知应用流程
完成后更新同一回执；异常保留现有 unknown／partial 语义
```

纯开关不需要核验新增群、人员、项目或工具证据，因此应避免当前通用路径为这些查询预留的“先解锁、再重新入锁准备配置”间隙。**从读取当前配置到持久化原回执，均保持同一 manager 锁段**；不要在两者之间释放锁，再用更新的状态重新准备。

只在锁内做现有本地状态读取、规范化和原子回执保存。远端调用、采集器结清与等待放在锁外，不能为了冻结快照长期占有全局锁。当前 `platformMemberAccountLocked` 会校验输入平台版本；当前观测模式应先从已保存平台记录取得版本，再调用原账号核验和 `platformMemberViewLocked`，不能简单取消原版本校验或相信客户端账号字段。`get` 的当前读取可复用这一小段逻辑，但不创建操作或通知。

无回执时的参数、权限和来源验证失败属于未提交，不创建成功结果；有效目标配置还未形成时也不为了防重伪造回执。冻结保证从成功保存原回执开始成立。此前失败不能承诺服务端记住一份不存在的观测；返回实际错误，调用层不得自动循环重试冲突。只有确认不存在提交、核对当前意图后才处理新的请求，不能宣称任何失败都自动恢复。

### 3.3 复用应用与防并发检查

`platform_member.go` 已有通知应用分支：GitLab 调用 `configureGitLabNotifications`，飞书调用 `configureLarkSubscription`，随后保存结果。若需要复用，将**通知部分**抽成公共 apply helper，接收冻结的完整请求、配置回执和 admission；简化与完整入口均调用它。不复制另一套 GitLab 分来源应用或飞书生命周期处理，不顺带改工具配置流程。

继续使用 `checkPlatformMemberContextLocked` 的账号／凭据快照、通知版本、飞书 SubscriptionID 检查，以及配置服务内部的锁与目标校验。配置、账号、成员或订阅在保存原回执后变化，应用应停止并保留实际回执状态，不刷新版本继续执行。GitLab 的 desired、Events／Todos 实际应用及 pending/errors 区分保持原样。

并发同请求：第一个调用在锁内保存 unknown 后解锁；第二个只返回该原回执，不再次 apply。响应丢失、完成回执保存失败或重启后的 unknown 同理；status 和重复调用都不能执行 unknown。先保存回执失败则不写配置，读取异常及不确定存储结果按现有 Store 保护处理。

操作仍持久化完整 `platformMemberInput` 和规范化配置，沿用现有 schema、`unknown/completed/failed` 和 `applied/preserved`。不增加后台自动恢复队列。保存前拒绝的输入、保存后可能已有部分效果的错误，必须按现有边界分别报告，不能一律改成 failed 后允许重试。

## 4. 两平台配置语义与实现落点

### 4.1 总开关与默认值

“纯总开关”是业务字段只有 Enabled，与是否使用简化参数无关；完整参数模式的纯开关也应得到相同业务结果，区别只是版本由谁观测。

| 当前状态／输入 | 行为 |
| --- | --- |
| 无真实配置，纯开启 | 使用手动首次开启的同一平台默认，按原机制建立首次接收起点 |
| 有真实配置，纯开启／关闭 | 只改总开关；分支、范围、筛选、间隔、已锁定起点和采集进度不变 |
| 无真实配置，纯关闭 | 保存 `completed/preserved` 操作回执，不创建通知配置、订阅或接收起点 |
| 已处于请求状态且已正确应用 | 返回 preserved，不增加配置版本或重启采集器 |
| 已有配置没有有效接收来源 | 开启时报告需要选择来源，不套首次默认、不宣告接收成功 |
| GitLab applying/pending/errors 或原 unknown | 保留原恢复边界，不能用开关重放部分应用 |

飞书生产默认复用 [`DefaultSearchConfig`](../internal/platform/lark/search_state.go)：全部可访问人员私聊，加全部群中 @我／@所有人的消息；不扩展机器人、自聊或所有群的全部消息。旧 `DefaultConfig` 群默认不同，不能拿它替代生产默认或回填存量数据。room 实现继续调用 `m.defaultLarkConfig(in.PlatformID)` 按注册 source schema 选择默认，避免把生产默认硬编码进旧适配器测试或存量配置。

GitLab 默认复用 [`DefaultNotificationConfig`](../internal/platform/gitlab/notification_config.go)：个人待办开启，沿用现有默认原因与对象类型，项目活动关闭；待办覆盖账号可访问项目，不要求 Workspace 或项目选择。

“真实配置”沿当前权威视图判断：飞书有当前订阅；GitLab有 saved desired version、实际 Events／Todos 订阅或历史锁定事实。默认草稿不算配置，version 为 0 的旧采集器不能当成全新；已保存的关闭配置不能被默认值覆盖。无配置读取或关闭不锁定起点；尚未启动的配置首次启用按现有机制确定起点，已锁定起点不变。

### 4.2 显式定制保持原边界

首次指定一个群不额外开启私聊，首次指定项目活动不额外开启待办；已有配置只修改请求字段。“只接收这个群”需要关闭已有私聊时，Agent 明确传对应分支修改，服务端不从群参数猜测。

完整模式中 Enabled 与来源字段组合，先按定制路径构造，再以显式总开关决定最终启停，false 优先；不进入首次默认分支。仅对纯总开关恢复放行“会同时恢复多个已保存来源”的情况，单来源操作继续保留防扩张检查。空指定列表、提及条件、完整范围替换、人员关系和准确项目核验继续有效。

### 4.3 逐文件指导

| 文件／位置 | 具体调整 |
| --- | --- |
| `platform_member_cli.go` | 用参数出现事实区分短／完整形式；纯开关免填版本与操作 ID；get 允许省略平台版本；保留其他命令约束 |
| `platform_member_store.go` | 增加请求级 ObserveCurrent，不进持久化 input；允许 GitLab 通用 Enabled 及合法完整组合，保留跨 provider 字段拒绝与原记录校验 |
| `platform_member.go` | 简化分派、稳定 ID、原回执投影匹配、锁内冻结／预留；复用通知 apply；补齐 GitLab 无配置关闭与无变化回执；get 当前读取严格限域 |
| `platform_member_lark.go` | 用小的纯开关判断区分默认初始化与原定制初始化；已有纯恢复只改 Enabled；仅此路径放行双分支恢复，其他冲突不放宽 |
| `platform_member_gitlab.go` | 纯开关从真实原配置或 DefaultNotificationConfig 起步；定制继续从原 opt-in 初始化；显式 Enabled 最终覆盖，原项目核验与 pending/errors 保护不变 |
| `platform_onboarding.go`、手动通知路径 | 继续共享原默认函数、权威配置服务；对齐无有效来源的启用判断，不改身份归属或让 Agent 使用 human 接口 |
| `platform_connect_cli.go`、子命令帮助 | 优先展示短开关与可选 get；完整模式说明保留在定制帮助，错误指出缺失／混用字段，不返回不相关的全量连接说明 |

优先使用几个 room 内部小 helper；不引入新的 Provider 接口、配置层或持久化迁移。旧操作使用原 ID 和完整冻结输入恢复，不按新默认重新计算。GitLab pending/errors 不能被“输入看起来没变化”的判断跳过。

## 5. 卡片与指导收敛

对“飞书选择群的卡片”：先读平台 Skill，按原 seq 恢复既有选择步骤，核对准确账号，直接 `selection start --kind groups`；GitLab 项目用既有 `--kind projects`。候选搜索、分页、保留选择及错误重试交给已有组件。Agent 不先遍历候选、不检查业务发送授权、不搜索源码，不把文字群清单或 chat_id 粘贴指引当作卡片。

核对创建回执及结果订阅后简短反馈并结束本轮，不循环等待点选。仅展示卡片不授权修改通知；原请求包含“选群来订阅”等配置意图时，提交后核对原意图、最新请求和原回执再应用复杂范围。继续使用原 selection、配置回执、acknowledge 及统一 Room operation-result event，迟到事件不重复保存。全部群转指定仍需完整替换意图；取消勾选不等于取消原订阅。

指导以替换、合并为主：

- 公共 guide 的 Skill 导航覆盖平台连接、通知／工具设置及账号／群／项目选择界面；默认助理模板、usage Skill 同步。
- 平台 Skill 的公共生命周期区分入口：纯开关直接一步执行并由服务端恢复原回执；复杂修改才要求先 get／观测版本；真实选择才创建组件。不能保留“所有设置先 get/status”的统一前置步骤。
- Skill 只说明“首次默认／已有恢复”，不复制平台默认布尔值；删除 GitLab 不支持总开关等旧例外，保留复杂修改及 unknown 恢复的必要约束。
- 卡片的原命令和续接机制不额外简化，不新增强制已读 Skill 门禁、关键词路由或工具次数硬限制。

关键导航放公共 guide，覆盖保存旧模板或自定义角色的助理，不覆盖用户角色文件。检查 [`componentOptions`](../internal/room/dsh.go) 与 [`unifiedAgentGuide`](../internal/room/input_stream_guide.go) 拼接后的实际 Instructions。Instructions 已纳入 [`configDigest`](../internal/agent/dsh/state.go)，存量 Session 沿既有受控更新／轮换机制生效，不手改指纹或后台历史，不要求重建整个会话。

## 6. 验证与交付顺序

### 6.1 配置、协议与恢复测试

复用现有 `platform_member_test.go`、`platform_member_lark_test.go`、`platform_member_gitlab_test.go`、成员集成测试、onboarding／生产默认测试与 Web 通知／选择回归。新增测试按长期行为命名，重点为：

| 场景 | 必须验证 |
| --- | --- |
| CLI 短开关、false、get 当前读取 | 一次正确请求；无需预先 get/status；完整模式仍严格；显式零与省略不混淆 |
| HTTP 绕过 CLI／错误形状 | ObserveCurrent 不能用于复杂字段、工具、其他读取命令；身份与真实 human seq 校验继续有效 |
| 两平台首次纯开启 | 与同平台手动默认的规范化结果一致；准确成员、工具绑定不变、首次起点正确 |
| 已有指定范围／筛选后关再开 | 飞书双分支、GitLab 待办＋活动均保留范围／分支／筛选／间隔／起点／进度 |
| 纯恢复与定制 | 双分支纯恢复成功；单来源顺带唤醒其他休眠来源仍冲突；首次定制不带入额外默认 |
| 无配置关闭／同状态／无有效来源 | preserved 不创建通知或增加版本；无有效来源不开启；GitLab 草稿、旧采集器与部分应用分类正确 |
| 稳定 ID 与重复输入 | 同成员／平台／原 seq 不因 bool、版本、turn 或 epoch 改 ID；不同 bool 冲突，完整旧模式同来源不另起操作 |
| 受理前已有配置变化 | 短开关采用本次当前范围，不覆盖成历史范围；完整模式依旧拒绝过期观测 |
| 保存后配置／账号／成员／订阅变化 | 用冻结 admission 和版本拒绝应用；原回执保留，不自动刷新；同版本不同订阅也拒绝 |
| 重试时当前版本已增长／账号失效／有更新操作 | 原 ID 的结果仍可查回，不二次 apply；不把历史回执宣称为当前状态 |
| 并发、响应丢失、unknown、完成回执保存失败和重启 | 同请求只有一次执行；新调用返回原冻结记录；未保存预留时不得写配置；非 NotExist 不视为无记录 |

`TestPlatformMemberLarkConflictingIntent` 的“双分支纯开启冲突”和 GitLab 总开关拒绝断言改为新契约，其余来源隔离、版本、空范围保护保留。应有可控并发测试停在“原回执已保存、配置尚未应用”处，用人工配置变化验证真正的写入前检查；不要仅测试 helper 输出或模拟函数返回成功。

### 6.2 真实模型与浏览器

使用生产公共指导、默认角色和旧助理上下文，输入原话：“开启飞书通知”“开启 GitLab 通知”“关闭后再打开”“只接收这个群”“飞书选择群的卡片”“GitLab 项目选择卡片”。不能只用额外提示“必须读 Skill／调用 selection”的专用验收角色证明默认助理会正确路由。

复用 `TestConversationSettingsRealDSH`、`TestConversationGitLabSettingsRealDSH` 及已有选择／浏览器入口，补齐短开关和原话卡片场景。核对真实请求、原 seq、返回操作 ID、冻结版本、最终权威配置，以及浏览器组件搜索／分页／提交后的接续。默认待办开启不能额外出项目选择题；纯开关不能强制 get；卡片创建前不能枚举全部候选。

关键原话在新 Session 和存量助理受控轮换后做多次独立运行，报告成功次数、工具调用轨迹、提供方读取次数、首个组件可见时间及总耗时，保留失败轨迹。19 次／约 176 秒仅作错误路径参考，不把一次成功当作稳定性证明，也不靠生产调用次数硬截断提前结束。

普通接口／配置、Web 组件、真实模型＋模拟提供方、真实浏览器、真实提供方读取分别报告。真实平台验收复用明确指定账号，在隔离 Store／成员中保存配置且不启动采集器，不改原安装订阅、不发平台消息。实际通知到达及提醒送达属于其他验收边界。

### 6.3 实施顺序与检查命令

1. **总开关语义切片**：两平台配置处理、无变化回执和手动一致性；同步帮助／Skill，完成配置与来源隔离回归。
2. **一步入口切片**：请求模式、稳定 ID、原回执匹配、锁内冻结／预留和 get 当前读取；完成 CLI／HTTP／并发／重启测试。
3. **卡片与真实验收切片**：公共导航、精简 Skill、聚焦帮助、Session 生效说明；真实模型与同轮浏览器验证，并保留未运行的真实提供方门槛。

实施时按变更先做针对性检查，再运行：

```sh
go test ./internal/room ./internal/platform/lark ./internal/platform/gitlab
go test -tags=integration ./internal/room -run 'Test(PlatformMember|GitLabMember|Onboarding|LarkSearch|PlatformSelection)'
node --test internal/web/tests/web_platform_selection_test.mjs internal/web/tests/web_member_notifications_test.mjs internal/web/tests/web_lark_notifications_test.mjs internal/web/tests/web_platform_onboarding_test.mjs
go test -tags=e2e ./internal/room -run '^$'
```

最后一项仅检查编译。真实 DSH／模型／Chromium 需按既有可选入口实际运行，skip、编译和历史报告不算本次验收。真实模型与浏览器不必加入 Release CI。

只通过本地配置测试不能宣称个人助理路径问题已解决。整体完成要求包含一步纯开关、默认／恢复语义、实际可交互卡片及真实模型／浏览器证据；尚未运行的门槛明确列出。维护时只同步相关 CLI／Skill／通知参考，不翻修历史方案，不把测试轨迹和参数协议放入面向用户的 README。
