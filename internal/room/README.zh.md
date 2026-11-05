# Room 运行说明

正式成员由 `MemberAgent` 调度，只支持 DSH Backend。Room 保存共享历史、成员身份与读取进度；成员拥有私有 Session。模型通过同一个 TeamX CLI 读写 Room，普通输出不会自动发言。测试中的观察成员、虚拟 Session 和演示驱动均位于 `*_test.go`，不进入正式二进制。

## 服务配置

项目内运行使用 `teamx serve --config .teamx/config.toml`。不指定 `--config` 时只读取 `~/.teamx/config.toml`，不自动查找当前目录或项目祖先目录。显式指定的文件缺失、或已有配置内容错误时会阻止启动；两份配置不会合并。

数据目录与团队目录通过 `server.data_dir`、`server.teams_dir` 设置，默认是配置文件旁的 `data/`、`teams/`。项目配置对应 `.teamx/data/` 和 `.teamx/teams/`，用户配置对应 `~/.teamx/data/` 和 `~/.teamx/teams/`。缺少默认配置时仍保持用户目录范围，可在配置的团队目录中使用自带完整 runtime 的团队。

加载规则：

- 配置文件中的相对目录、DSH 入口和两个工具路径变量，以配置文件所在目录为基准；命令行文件路径以当前工作目录为基准。不展开 `~`、变量或 shell 命令，其他环境变量值按字面量传递。
- `[env]` 仅补充未设置的服务环境变量；外部已设置的空值也优先。服务不自动加载 `.env`。
- 监听地址、回合预算、通知合并窗口和 DSH 单回合超时按“显式命令行参数 → 配置文件 → 默认值”取值；数据目录与团队目录只通过配置文件指定。
- 配置在启动时加载，修改后需重启。新房间保存解析后的 runtime 和角色；已有房间继续使用原快照。

示例及字段见 [config.example.toml](../../.teamx/config.example.toml)和[团队配置](../../.teamx/teams/README.md)。仓库提交配置示例和团队模板，忽略真实的 `.teamx/config.toml` 与 `.teamx/data/`；凭据不写入团队 JSON。

`server.dsh_turn_timeout = "15m"` 设置每次 DSH 回合的总时长，包含模型与工具执行，默认 15 分钟。大型设计任务可设为 `"30m"`，也可通过 `--dsh-turn-timeout 30m` 覆盖；必须为正的时长。它属于服务策略，重启后适用于新旧房间，无需轮换 Session。超时仍会取消当前回合并暂停成员；手动取消和资源结清沿用各自的期限。

### 使用用户目录

从源码目录首次准备用户配置和团队模板：

```sh
mkdir -p "$HOME/.teamx"
cp .teamx/config.example.toml "$HOME/.teamx/config.toml"
cp -R .teamx/teams "$HOME/.teamx/"
```

编辑 `~/.teamx/config.toml` 中的 DSH 路径、模型和密钥。TeamX 可执行程序在 PATH 中时直接运行 `teamx serve`；从源码运行也可使用 `./bin/teamx serve`。

## 多房间服务

不传 `--room` / `--state-dir` 时进入多房间管理模式，启动输出中的 `data_dir`、`teams_dir` 是实际使用的绝对路径。从项目目录或其他工作目录选择配置：

```sh
teamx serve --config .teamx/config.toml
# 从其他工作目录恢复：
teamx serve --config /绝对路径/config.toml
```

默认管理入口不接受 `--dsh-config`。左侧「＋」仅选择团队及可选房间名；团队默认采用 `config.toml` 的 runtime，也可完整覆盖模型和 DSH 参数，成员声明职责与可选工具环境。创建时保存 runtime、展开后的角色指令及资源副本，仅记录密钥环境变量名称。已有房间恢复不读取团队模板；修改模板后重启服务只影响后续新建。外部产物、凭据、项目依赖和原有恢复证据仍须可用。

新房间公共消息为空，成员、Session、工作区、游标和预算独立，开场消息仅填入草稿。已有房间切回不补填或覆盖草稿。受管房间必须保存完整 runtime 和 Workspace 配置；旧运行时和 Workspace 离线迁移命令已移除。

[Workspace](../../docs/workspace.zh.md)通过输入框下方入口配置普通目录、可选 HTTPS 仓库和 GitLab 认证。新房间只准备普通成员目录，模板建议不自动生效。应用先结清旧执行，再准备目标、提交配置并更新已有 Session 代际；保留文件和公共历史，不自动重放旧工作。PAT 仅经 write-only 请求保存为私有凭据引用。已有仓库和项目只读，回访只做本地核对。

应用使用稳定操作 ID 和配置版本，提供 `GET /v1/workspace`、`POST /v1/workspace/apply`、`POST /v1/workspace/retry`。超时先查询，失败显式重试原目标；部分提交和不确定写入阻止执行。管理身份、请求结构与页面凭据处理见 [Workspace 接口](../../docs/workspace.zh.md#管理接口)。旧运行时与 Workspace 迁移命令已移除，受管房间必须具备完整 runtime 和 Workspace 快照。

每个标签页独立保存查看对象，切换或关闭页面不停止后台 Agent、工具和通知投递。不同房间可以并行，每个房间仍只有一个执行名额；重复查看健康实例不重建 Session、不改变成员状态或预算。列表按房间显示目录状态、运行状态、操作进度和错误，同房间冲突操作串行，其他房间仍可查看及执行。草稿、引用、提醒进度及未确认发送分别保存在当前标签页各房间中。未确认发送保留完整原身份和消息 ID，身份过期后须先核对历史，再放弃重试。迟到响应不会修改新房间页面。创建和打开使用持久化去重 ID，响应丢失后可核对列表并显式重试，刷新不会自动续跑；创建成功但打开失败的房间仍留在列表。

房间列表的「删除」经确认后停止目标房间，验证当前及历史私有会话已结清，再永久移除整个房间目录：公共消息、成员状态、所有 Agent 私有会话、诊断、资源副本和工作区文件一并删除。团队模板和其他房间保留；删除当前房间不会自动选择另一个房间。操作者接口 `POST /v1/rooms/delete` 要求目录 `service_epoch`、human `caller` 以及精确的 `room_id` / `room_instance`。后台操作由服务持有，不随 HTTP 断开取消；通过 `GET /v1/rooms` 的房间 `busy`、`status=deleting`、`deletion_phase`（`closing` / `removing`）和 `error` 核对进度，房间从列表消失才表示完成。失败或服务中断后不会自动继续删除或恢复该房间，须核查并明确重试。进入 `removing` 前先持久记录执行已结清，因此即使数据只删掉一部分也能重新打开目录并明确重试。

点击顶部房间名或旁边的编辑图标可就地修改名称，Enter 保存、Esc 取消，也可点击旁边的保存/取消按钮；保存后同步更新标题和左侧列表。`POST /v1/rooms/rename` 携带目录 `service_epoch`、human `caller`、精确 `room_id` / `room_instance`、`name` 和原名称 `expected_name`；名称去除首尾空白后须非空且不超过 256 字节。原名称不匹配时返回冲突，避免覆盖其他页面的修改。当前显示名仅原子更新 `rooms.json`，创建收据和冻结配置保留原名称；消息、草稿、成员、工作区和 Session 身份不变，不重启执行。响应丢失后核对目录，不自动重发。

管理接口：`GET /v1/rooms` 返回每项的 `status`（目录）、`runtime_status`（`closed / opening / ready / failed`）、`busy`、`runtime_error`、可用 `service` 和安全成员名称。顶层 `busy` 仅表示共享资源操作；`default_room_id` 只兼容旧索引的初始查看建议，不表示当前选择。

`POST /v1/rooms/open` 要求管理器 `service_epoch`、human `caller`、`room_id`、`room_instance` 和稳定 `open_id`，返回 `running / completed / failed`，完成时附 `service`。`GET /v1/rooms/open?service_epoch=…&caller_id=…&open_id=…` 只查询原回执。相同 ID 不重放，失败后检查并用新 ID 显式打开。旧 `/v1/rooms/activate` 已移除。普通 Room API 携带实例的完整身份；`GET /v1/info` 也须带 `room_id`、`room_instance`、实例 `service_epoch` 和 `caller_id`。

`rooms.json` 版本 2 以完整身份的 `owned_rooms` 记录运行归属，并保存 `opens` 和逐房间 `shutdowns`。旧归属迁移后移除，历史激活回执及中断证据保留用于恢复检查。正常启动逐房间恢复既有启用意图；异常回合不自动重放，暂停且已结清的房间按需打开。退出冻结全部房间后并行结清，任何资源未结清均保留归属和根目录锁。

未查看的操作者 @ 提及会将整条消息背景标黄。页面处于前台且获得焦点时，消息至少一半进入可视区并连续停留 1 秒后，自动标记该条已查看；超过视口高度的长消息以半个视口为可见门槛。快速划过或后台页面不计入查看。顶部提醒跳转到最早的未查看提及，仍按可见停留规则消除提醒。已查看消息背景恢复常规颜色，未查看的消息继续标黄，计数逐条减少，不会连带清除更早未查看的消息。@ 用户名本身始终保留黄色标记，表示这里存在提及，与是否已查看无关。查看进度在切换房间和标签页刷新后保留，不推进 Agent 未读游标。

以下协作流程使用显式参数保留的单房间模式。

## 启动与协作

需要 Go 1.27+、完整安装的 DSH、回环监听、进程观察和可用模型网络/凭据。按 [Agent 运行时](../../docs/agent-runtime.zh.md) 准备环境。显式单房间模式需自行准备包含 `reviewer`、`observer` 两个命名 profile 的 JSON；每个 profile 配置 `binary_path`、`workspace`、`instructions` 和 `model`，字段见 [dshProfile](dsh.go)。路径须为本机绝对路径，工作区须预先创建。凭据由服务环境注入，服务不自动加载 `.env`。

```sh
go build -o /tmp/teamx ./cmd/teamx
/tmp/teamx serve --room design-review --state-dir ./.teamx/data/dsh-team \
  --listen 127.0.0.1:8088 --dsh-config /absolute/dsh-team.json --max-turns 16
```

在浏览器打开 **http://127.0.0.1:8088/** 使用内置 Room 页面。服务也会在 stderr 打印 `Room UI:` 地址；自动分配端口时使用打印的地址。无需前端构建或独立进程。

页面提供公共消息流、发送与引用、较早历史加载、配置角色选择与成员添加、启用/暂停协作及取消当前回合，展示成员未读、游标、预算和错误。消息与成员约每 1.5 秒同步；切回页面立即同步，连接失败保留现有消息并自动重连。新成员先添加并启用，再发送任务；加入前历史不产生未读。普通输出仍然私有，成员选择沉默也属于正常行为。

团队选择位于新建房间窗口，右侧展示保存的成员及协作状态。

页面始终以操作者身份预览，不消费 Agent 未读。单条消息上限沿用 16 KiB；发送未确认时锁定原内容与消息 ID，显式重试不会重复追加，草稿和未确认请求按房间及操作者保存在当前标签页的 sessionStorage。服务正常重启后刷新身份；已有未确认发送保留原完整身份，不改写为新的服务代次。页面不提供 Session 轮换与成员删除，仍可用下方 CLI 执行。

公共历史清空 API `POST /v1/messages/clear` 仍要求操作者携带 `snapshot_seq`；页面将「重新开始」放在消息数旁的「···」菜单，经确认执行下述完整重置流程。

「重新开始讨论」经确认后暂停所有成员并取消当前回合，等待旧执行结清、轮换所有私有 Session，再清空公共历史并恢复协作。操作期间阻止新发言、清空和成员变更，页面仍可刷新和查看进度。成员、角色、工作区文件、累计回合和预算保留；预算已用尽的成员保持暂停。新 Session 按需创建，不重放旧回合，也不会删除旧 Session 目录。工作区中已有文件仍可能被新会话读取。

批量操作由服务后台持有，关闭页面或 HTTP 超时不会取消。轮换失败时保留公共历史，成员保持暂停；Session 引用逐个提交，部分成员可能已重置，不承诺跨成员原子回滚。`discussion-restart.json` 记录进度，未完成或失败的批次在服务重启后阻止全队自动恢复，需核查后显式重试。操作者接口 `POST /v1/discussion/restart` 携带 32 位小写十六进制 `restart_id` 和最近一次批次 ID `previous_restart_id`（首次为空）；重复当前 ID 返回已有结果，过期请求报告冲突。`/v1/member/list` 的 `discussion_restart` 返回阶段、结果、错误与预算耗尽成员。

消息输入框按 Enter 发送，Shift + Enter、Ctrl + Enter（Mac 为 ⌘ + Enter）换行；中文输入法确认选词不触发发送。

房间头部只保留团队成员图标，控制同一个右侧栏展开或收起。点击成员信息区、消息中的成员头像／名称，或消息区底部处理／等待提示，会将侧栏从成员列表切换为 Agent 详情。顶部显示成员、运行状态及启用／暂停、取消回合等操作。默认打开「活动记录」；受管房间可切换至「工具设置」，为当前成员启用工具并选择连接，展示实际账号、实例及沿用账号的有效权限；不再提供旧房间工具的逐操作授权和项目范围配置。切换至工具页签停止读取私有活动；点击「返回成员」或按 Escape 返回列表，多成员时可在详情内切换。两个页面共用同一宽度。桌面端可拖动侧栏左边缘调整宽度，聚焦分隔线后也支持左右方向键及 Home/End；宽度受消息区剩余空间约束，随窗口大小调整，并保存在当前标签页。收起侧栏停止读取活动，再次展开显示成员列表。详情展示：当前状态、读取进度、预算与累计回合。运行中约每 1.5 秒读取当前回合的内存快照，按通知到达顺序穿插显示思考、工具活动与内部回复，每一段可独立折叠。连续的同类文本合为一段；同一工具的连续更新合为一行，若中间出现其他活动，后续工具更新保留在其到达位置。内部回复默认展开，思考和工具默认收起；刷新与回合结束保留展开状态。默认自动跟随最新活动；向上翻看时暂停，滚回底部或点击「回到最新」恢复跟随。工具段展示名称、状态、参数（最多 2 KiB）与文本结果（最多 4 KiB）。活动流最多保留最近 256 段，编码总量不超过 256 KiB，每段文本最多 16 KiB，截断明确标记。结束后显示最近完成回合的活动流、结束原因及错误；活动只接受有序 `events`，缺少该字段的诊断明确显示格式无效，不按摘要推断顺序。当前 DSH 的 ACP 通知按已提交的模型输出和工具事件到达，不保证逐 token 更新，也不展示模型未提供的思考。抽屉关闭后不继续读取，观察不触发执行、不消费未读，也不写入公共历史。运行中快照不落盘，服务重启后仅保留最近完成回合的诊断。

HTTP 的 `/`、`/ui/*` 提供嵌入静态资源，`GET /v1/ui` 在管理模式返回 `managed=true`，页面再从 `GET /v1/rooms` 读取目录、活动身份、操作者、团队展示信息及活动房间保存的成员名称；单房间模式仍返回原身份与元数据，不暴露模型配置或凭据。管理模式拒绝直接增删成员。`POST /v1/member/activity` 仅接受操作者调用，并校验 Room/服务代际及当前成员代际，优先读取当前回合的有界内存快照，空闲时按需读取该成员有界的 `dsh-turn.json`，返回 `Cache-Control: no-store`；成员工具不能访问，成员列表和公共历史不携带私有回复。配置内容、模型凭据及 DSH 全量会话文件不通过此入口暴露。页面同源访问，沿用可信本机使用边界。

操作者终端也可以继续使用 CLI，无需设置成员身份：

```sh
export TEAMX_ENDPOINT=http://127.0.0.1:8088
/tmp/teamx member add --agent reviewer --profile reviewer
/tmp/teamx member add --agent observer --profile observer
/tmp/teamx send --text '请检查退出顺序' --id task-1
/tmp/teamx member run --agent reviewer
/tmp/teamx member run --agent observer
/tmp/teamx member list --json
/tmp/teamx read --latest 10 --json
/tmp/teamx read --all --json
```

`member add` 默认 `--runner dsh`，必须指定服务已配置的 `--profile`。新成员从加入时的历史末尾开始，不把旧消息变成未读；首次实际回合才打开 Session。成员之间角色、工作区、进程、私有状态和预算独立。操作者可直接发消息和预览历史，无需建立模拟成员。

`member list` 中的 `unread_count` 来自 Room；`notification` 是成员已处理的最新元数据，短暂延迟正常。通知不含正文；`execution` 给出 paused / active、回合、错误、预算与轮换状态。服务 stdout 输出一行就绪 JSON，日志写 stderr；`--listen 127.0.0.1:0` 自动分配端口。

## 回合、预算与轮换

```sh
/tmp/teamx member pause --agent reviewer
/tmp/teamx member pause --agent reviewer --cancel
/tmp/teamx member run --agent reviewer
/tmp/teamx member run --agent reviewer --retry
/tmp/teamx member session rotate --agent reviewer --cancel
/tmp/teamx member remove --agent reviewer
```

每个 Room 同时只有一个成员持有执行名额，覆盖按需打开 Session、完整回合及其工具结清。FIFO 按调度 Actor 收到有效申请的顺序排队，不固定角色顺序；等待者显示 `waiting`（页面“等待执行”），`active=false`，不增加回合编号或消耗预算。取得名额后重新核对 Room 的成员资格与最新未读；仍有新输入的原持有者结清后排到队尾。不同 Room 的名额独立，排队不阻止消息读写、用户输入和成员管理。

一个成员同时最多一个回合。忙碌时通知合并，结清后处理尚未提示的新输入；不阅读或保持沉默也能正常结束，同一批未读不会反复自激。`run --retry` 明确再次提示现有未读，不重放旧工具调用。取消请求本身不表示执行已停止；未结清时仍为 `active=true` 并持有名额，成员状态保留等待原因。暂停、移除和轮换撤销等待申请，服务关闭先停止新授予。队列不持久化，重启仍受原 Session 恢复规则约束。

执行期间用户追加消息继续合并，不自动打断当前回合。串行执行不限制发言总数，也不保证讨论收敛或减少 token；发送检查和每轮公开发言预算尚未实施。

`serve --max-turns N` 必须为正数，默认每成员每次服务受理 16 回合。失败和取消计数，任务名额不足未受理不扣；耗尽后 `budget_exhausted` 暂停，历史和未读保留。`execution.turn_budget` 显示 limit / used。「启用协作」（CLI `member run`）在额度耗尽时追加一批回合，批量为房间创建时的 `max-turns`，保留已用次数与累计回合；未耗尽时不追加。pause、单独轮换、后台恢复和重新开始讨论不续额。显式单房间模式按服务启动计数；受管房间持久保存使用量和追加后的上限，切房、重新开始及重启均不续额。新成员代际独立计数，持久累计回合编号另行保存。

轮换先暂停新回合，继续合并通知，等待当前回合结束（或用 `--cancel` 取消并等待），关闭并等待旧 Session、进程和 worker，成功后才持久切换目录。新 Session 按需创建，成员身份、历史、游标和预算保持。默认完成后暂停；`--run` 明确恢复调度，不自动重试已提示的输入。pause 可以撤销待完成轮换的 `--run`。

**轮换重置私有上下文，无自动摘要或私有记忆迁移。** 新 Session 获得角色、模型、当前工具身份和历史入口 `read --all/--latest`，预览不改变未读。旧发言与外部副作用保留，不能自动重放；旧私有目录保留。`dsh-turn.json` 是最近回合的诊断，不是交接摘要。

轮换命令最多等待约 5 秒，超时报告 incomplete / outcome unknown；成员仍负责清理。通过 `execution.rotation.status` 查看 waiting / closing / completed / failed，先观察再决定是否重试；重复 rotate 不幂等。原会话不能恢复与旧执行未结清是两种状态：进程、工具、协议读取和 worker 全部退出后，显式轮换可以丢弃失败会话并创建新代际。无法确认结清或提交不确定时，仍禁止替代执行。

成员的继续操作统一为「启用协作」，按钮附近说明是否会重置会话（清除私有上下文）及追加回合。会话受阻时先通过原有结清检查并完成轮换，再恢复调度；如果额度也耗尽，则在轮换成功后追加一批回合。失败或中断留下的未读会再次提示，不重放旧调用栈。重置失败时保留暂停，不追加额度。可识别的模型限流只展示固定的 HTTP 429 提示，不公开上游错误正文或凭据；应等待提供方额度恢复后再重试，没有自动重试循环。

当前 DSH 回合默认最长 5 分钟，普通输出保留最多 64 KiB；OpenAI 配置为 4096 最大输出 token、模型请求重试为 0。没有货币费用上限、精确 token 记账、自动压缩或自动轮换，服务预算不能代替提供方账单。

## CLI 工具与读取语义

适配器向真实 Session 绑定六项环境：`TEAMX_ENDPOINT`、`TEAMX_ROOM_ID`、`TEAMX_ROOM_INSTANCE`、`TEAMX_SERVICE_EPOCH`、`TEAMX_AGENT_ID`、`TEAMX_MEMBERSHIP_ID`；另提供绝对路径 `TEAMX_CLI`。工具不得自行切换身份，也无成员管理权限。受管模式下操作者须指定端点及已打开的 `--room ROOM_ID`（或命令的 ROOM 位置参数），CLI 从目录取得完整实例身份，查询不会隐式打开；这是可信本机协议，不是多用户认证系统。

Agent 用 `read --unread --limit 20 --json` 按序消费，成功交付一页后 CLI 确认游标，再继续下一页。每页最多 100 条消息及 1 MiB 正文。一个成员同时只有一张有效未确认页，30 秒过期；并发消费返回 busy，历史预览不受影响。stdout 失败、确认失败或结果不确定不会越过未交付页；已提交游标不回退。重启使旧收据失效。

`--latest N`、`--all`、`--unread --peek` 都是预览，不推进游标。固定快照读取期间新增消息留待下一次读取。自己的发言不计为自己的未读，也不清空先前未读。`send --id ID` 按发送身份与 ID 去重；相同请求重试返回原序号，不同内容冲突。结果不确定时使用原 ID 与原内容。

Room 同时最多一个通知批次，每个目标最多等待 100 ms；失败约每秒重试，每 5 秒核对状态。成员校验完整身份及 Revision，不累加重复通知。任务名额和通知状态有界；Room 消息提交后即使通知失败仍保留。

## 正常恢复与退出

同 `--room`、同 `--state-dir` 重启。启动先独占并校验状态、恢复调度检查点和重新绑定 Actor，再发布就绪。RoomInstance、MembershipID、序号、版本和游标保留，ServiceEpoch 更新；旧工具请求被拒绝。孤立文件不复活已移除成员。

DSH 只有正常关闭证明、同产物/工作区/角色/模型配置才允许原 Session 按需恢复；completed 成员恢复调度，failed/interrupted 成员暂停。遗留 running 提交为 interrupted，不自动重放。恢复失败不退回新会话；缺 profile 或无可靠停止证明只阻止该成员，Room 记录和其他成员保留可用。损坏或身份冲突的权威数据会阻止就绪。

已提交但尚未启动的普通轮换代际在重启后暂停，可显式继续；已完成的 Workspace 管理操作仅对其精确记录的新代际恢复健康调度意图，待处理或中断工作仍需显式继续。已运行的新 Session 正常关闭后沿用原会话恢复。异常退出、旧引用或未结清执行不能靠轮换绕过核查；不提供宿主崩溃接管。

Ctrl-C / SIGTERM 先停止新接纳，结清管理操作与成员任务/Session，再关闭 Room、Runtime 和 Store。移除先提交关系删除，拒绝旧工具，再取消并等待资源。清理超时明确报告并保留目录所有权；不能把发出取消或进程退出请求视作结清。

服务关闭使用 30 秒的等待预算；DSH 的取消、协议关闭和进程树自然退出分别使用取消等待预算（默认各 5 秒）。正常取消并完成关闭的 interrupted 成员保留原会话恢复资格，但重启后仍暂停，需显式继续。Session 若因取消未结清、协议或进程观察失败而禁用，关闭也返回具体原因；强制回收或进程非零退出同样明确报错且不写入正常停止凭据。已有记录缺失的凭据不会自动补写。

## 存储与兼容边界

版本 1：`room.json` 保存身份、完整历史、成员 runner/profile 和游标；`agents/<MembershipID>/agent.json` 保存调度水位与回合；`session.json` 保存 DSH 目录引用；DSH 的 backend.json、home、会话历史位于引用目录。`settled.json` 只在旧执行全部退出后记录，供显式重置核查；它不赋予原会话恢复资格，并在再次启动前消费。旧版本已失败且缺少退出证明的目录不会自动补写该记录。轮换支持唯一目录代际与可选 pending，启动前消费新建许可。轮换观察状态是进程内数据，持久引用决定重启后的代际。

旧 manual/scripted runner 或含旧 script 字段的快照明确拒绝，不转换为 DSH、不覆盖或清空。旧模拟实验请保留原目录，真实 DSH 使用新目录；原 DSH 快照继续支持。

macOS/Linux 使用非阻塞文件锁，其他平台明确不支持。快照同目录临时写入、文件同步、原子替换及目录同步；文件 0600、目录 0700。跨文件无事务。替换或目录同步结果不确定会冻结 Store 写入，必须结清、关闭并重新加载核查。损坏、未知字段、版本与身份冲突不创建空 Room。

默认最多 32 成员、10,000 消息、单条正文 16 KiB、历史正文共 64 MiB；满载拒绝新写，保留历史。编码上限 Room 512 MiB、各私有文件 1 MiB。任意外部工具不保证恰好执行一次。

## 验证入口

```sh
go generate ./internal/room
go test -race -count=1 -timeout 5m ./...
go vet ./...
node --test internal/room/web*_test.mjs
go test -tags=e2e ./... -run '^$'
go test ./internal/room -run '^TestFixtureDemoAndReport$' -count=1 -v
```

修改 Actor handler 后才需运行 `go generate`；Actorx 版本固定在 `go.mod`，由 Go 下载。普通测试需要 Node；`e2e` 命令只检查编译，不执行真实服务验收。

默认测试使用测试观察者、受控 Backend、屏障及假进程，不读取模型凭据。独立 harness 注入虚拟 Session，复用正式 CLI、HTTP、Room 和 MemberAgent；`TestFixtureDemoAndReport` 检查双成员报告、历史、服务日志及进程清理，临时证据目录由 Go 测试管理。正式二进制不识别测试 profile 行为或 harness 环境开关，测试端口不作为产品功能提供。

本地生产二进制检查可设置 `TEAMX_TEST_CLI_BIN`（TeamX）和 `TEAMX_TEST_GITLAB_BIN`（GitLab 工具）的绝对路径；`TEAMX_GLAB_BIN` 指定固定版本 glab。这些检查默认跳过。

受限环境无法监听时，应在允许监听的环境补跑完整测试，不能记为全量通过。DOM/handler、真实 TCP、浏览器观察与真实平台/模型验证分别记录。

真实验收必须显式启用，入口见 [DSH](../../docs/agent-runtime.zh.md#真实验收)、[Workspace](../../docs/workspace.zh.md#真实验收)、[通知](../../docs/notifications.zh.md#真实验收)和 [GitLab 工具](../../tools/gitlab/README.zh.md#真实验收)。每次使用新的私有绝对输出路径，与日常 `data/` 隔离；完整运行目录可能包含凭据和私有历史，不整体公开。失败或中断先保留现场，核对远端及持久状态，结果未知的写入不能直接重发。

消息、调度、身份与持久化设计见 [Room 架构](../../docs/room-architecture.zh.md)。

## 当前待验项

- Room / Web：多房间组件、HTTP、前端、定向竞态及真实 DSH 验证见[多房间验收记录](../../docs/MULTI-ROOM-RUNTIME-ACCEPTANCE.zh.md)；真实浏览器切房／双标签页仍受工具来源权限门禁阻断，成员工具设置、生命周期组合和视觉检查尚未通过。
- 团队协作：串行调度在真实书店任务中的效果。

平台工具和 Git 工作区的具体缺口分别见 [GitLab 工具验收](../../tools/gitlab/README.zh.md#真实验收)与 [Workspace 验收](../../docs/workspace.zh.md#真实验收)。这些是现有链路的验证缺口；旧工具或旧协议的通过结果不能沿用到当前实现。
