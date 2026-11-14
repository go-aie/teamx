# Room 运行说明

正式成员由 `MemberAgent` 调度，只支持 DSH Backend。Room 保存共享历史、成员身份与读取进度；成员拥有私有 Session。模型通过同一个 TeamX CLI 读写 Room，普通输出不会自动发言。测试中的观察成员、虚拟 Session 和演示驱动均位于 `*_test.go`，不进入正式二进制。

新用户安装、网页首启及常驻维护见[安装与运行](../../docs/installation.zh.md)。本文面向源码开发与配置排查。

## 从源码运行

需要 Go **1.27+** 和完整 DSH 安装，DSH 的 Node 与依赖须保留，准备方法见[Agent 运行时](../../docs/agent-runtime.zh.md#准备-dsh-与配置)。在仓库根目录执行：

```sh
go build -o bin/teamx ./cmd/teamx
cp .teamx/config.example.toml .teamx/config.toml
```

已有配置时跳过复制，避免覆盖。将 `.teamx/config.toml` 的 `runtime.dsh.binary_path` 填为本机 DSH 的绝对路径（可用 `command -v dsh` 查找），然后运行：

```sh
./bin/teamx serve --home .teamx
```

Web 界面已嵌入程序，无需单独启动前端。打开 `http://127.0.0.1:8088`，在网页配置并验证模型，无需 export API Key；默认数据在 `.teamx/data/`。`server.dsh_turn_timeout` 默认为 `"15m"`，大型任务可设 `"30m"`。Ctrl-C 正常停止并等待清理完成，同一配置再次启动可继续已有房间。

## 归档调试运行

普通用户通过安装器部署常驻服务，见[安装指南](../../docs/installation.zh.md)。开发者需要在终端观察服务、验证 core 包或隔离 home 时，可在解压目录运行：

```sh
/bin/sh ./start --home /absolute/private-debug-home --listen 127.0.0.1:8090
```

该命令准备缺失基础文件并启动 Server，需保持终端打开；Ctrl-C 后等待清理完成。省略 `--home` 时使用包内 `.teamx/`。模型仍在网页配置，无需先 init。`/bin/sh ./start init` 只准备文件和访问身份后退出；`/bin/sh ./start doctor` 诊断依赖，不发送模型请求。相同 home 只允许一个服务实例。

macOS 手工解压后通过 `start` 调试时，脚本会检查下载隔离标记，只有终端明确输入 `yes` 才解除该包的隔离；取消不执行包内程序，非交互不自动同意。这是开发入口的行为，不是安装器的前置步骤。

## 服务配置

首次运行直接使用 `teamx serve --home .teamx`，会准备固定操作者；全新安装默认仅本机访问，无需登录。`--home` 缺少模型或模型无效时，可在浏览器设置并启动；`--config` 保留已配置开发环境的直接启动方式。不指定 `--config` 时只读取 `~/.teamx/config.toml`，不自动查找当前目录或项目祖先目录。显式指定的文件缺失或 TOML 结构错误时会阻止启动；两份配置不会合并。

数据目录与团队目录通过 `server.data_dir`、`server.teams_dir` 设置，默认是配置文件旁的 `data/`、`teams/`。项目配置对应 `.teamx/data/` 和 `.teamx/teams/`，用户配置对应 `~/.teamx/data/` 和 `~/.teamx/teams/`。已有实例缺少管理凭据时拒绝启动，需在部署机用 `access init` 显式修复。

在「设置 → 安全」可开启登录验证、生成或自定义访问令牌。已有安装保持原认证状态。开启验证后，浏览器使用管理 token 换取 HttpOnly Cookie；会话默认 24 小时，最多 16 个，仅保存在服务内存，退出、到期和 Server 重启后失效。本机免登录仍校验请求来源与固定 human 身份；Agent 始终需要独立执行凭据。启用登录后，全部私有 API 均要求管理 Bearer、浏览器会话或绑定成员的执行凭据。人工 CLI 使用 `--token-file FILE` 或 `TEAMX_ADMIN_TOKEN_FILE`，不用在命令行中传 token 值。管理 token 文件必须由部署账号拥有、权限为 0600 的普通文件；初始化不覆盖已有文件。网页轮换后旧 token 和浏览器会话立即失效；活跃文件由 `access.admin_token_file` 指定。也可在停服后替换该文件并重启，固定 `access.operator_id` 保持不变。

模型和密钥在「设置 → 模型」验证并维护，无需重启。成员按默认／固定条目选择；执行配置变化等待当前回合结束后轮换，暂停意图保留。删除模型可选择替代条目并查看逐成员进度。旧模型配置在业务恢复前一次迁移，私有备份与固定进度保留；导入条目不伪造验证，旧 Session 使用原快照恢复。迁移冲突时只开放认证后的进度查询与重试。

在「设置 → 安全」选择仅本机、局域网或 HTTPS 代理，对应 `server.access_mode = "local" | "lan" | "proxy"`。局域网模式要求登录、全接口监听，通过面板列出的本机私有 IP 和端口访问，HTTP 未加密。HTTPS 代理模式要求登录、唯一的 `server.public_base_url = "https://assistant.example.com"` 和明确的 `server.trusted_proxies` IP 列表；受管服务保持 loopback 监听。代理保留规范 Host、向 loopback 转发单值 `X-Forwarded-Proto: https`，不支持子路径、别名 origin 或转发 Host。访问模式、端口和入口地址重启后生效；内部 Agent 始终使用实际 loopback endpoint。常驻安装始终要求登录，目录和生命周期见 [Server 架构](../../docs/server.zh.md)，安装、证书与系统服务配置见[安装与运行](../../docs/installation.zh.md)。

加载规则：

- 配置文件中的相对目录、DSH 入口和两个工具路径变量，以配置文件所在目录为基准；命令行文件路径以当前工作目录为基准。不展开 `~`、变量或 shell 命令，其他环境变量值按字面量传递。
- `[env]` 仅补充未设置的服务环境变量；外部已设置的空值也优先。服务不自动加载 `.env`。
- 监听地址、回合预算、通知合并窗口和 DSH 单回合超时按“显式命令行参数 → 配置文件 → 默认值”取值；数据目录与团队目录只通过配置文件指定。
- 非在线设置在启动时加载，手工修改需重启。模型列表与时区通过设置页保存后立即发布；Session 保存自己的实际执行快照。

示例及字段见 [config.example.toml](../../.teamx/config.example.toml)和[团队配置](../../.teamx/teams/README.md)。仓库提交配置示例和团队模板，忽略真实的 `.teamx/config.toml` 与 `.teamx/data/`；凭据不写入团队 JSON。

`server.time_zone` 默认 `Asia/Shanghai`。可在「设置 → 通用」搜索 IANA 时区、查看当前生效值并保存，无需重启；全部 Room 共用。浏览器时区只在点击对应按钮后填入草稿，保存后才生效。页面标签、日期分组、悬浮详情和时间输入采用系统时区；正文与存储的绝对时刻保持原样。接收起点和规则有效期按系统时区输入，夏令时中不存在或有歧义的时间需要纠正。切换时区会重新显示已确定的绝对时刻，未完成的时间草稿需重新确认或修改。手工编辑配置仍需重启；运行期间编辑应遵守配置文件锁，或先停止服务。Agent 每轮收到当前本地时间，首次和时区变更时提供时间约定；变更从下一正常回合生效，原始时间数据保持不变。

`server.dsh_turn_timeout = "15m"` 设置每次 DSH 回合的总时长，包含模型与工具执行，默认 15 分钟。大型设计任务可设为 `"30m"`，也可通过 `--dsh-turn-timeout 30m` 覆盖；必须为正的时长。它属于服务策略，重启后适用于新旧房间，无需轮换 Session。超时仍会取消当前回合并暂停成员；手动取消和资源结清沿用各自的期限。

### 使用用户目录

从源码目录首次准备用户配置和团队模板：

```sh
mkdir -p "$HOME/.teamx"
cp .teamx/config.example.toml "$HOME/.teamx/config.toml"
cp -R .teamx/teams "$HOME/.teamx/"
```

填写 `~/.teamx/config.toml` 中的 DSH 路径，再运行 `teamx serve --home "$HOME/.teamx"`，在网页配置模型。

## 多房间服务

不传 `--room` / `--state-dir` 时进入多房间管理模式，启动输出中的 `data_dir`、`teams_dir` 是实际使用的绝对路径。从项目目录或其他工作目录选择配置：

```sh
teamx serve --config .teamx/config.toml
# 从其他工作目录恢复：
teamx serve --config /绝对路径/config.toml
```

默认管理入口不接受 `--dsh-config`。左侧「＋」可新建私信，或选择团队及可选名称新建房间；团队成员初始化为默认模型，也可用 `model_selection` 固定引用全局条目；成员声明职责与可选工具环境。创建时保存 DSH、选择方式、展开后的角色指令及资源副本，密钥使用私有托管引用。已有房间恢复不读取团队模板；修改模板后重启服务只影响后续新建。外部产物、凭据、项目依赖和原有恢复证据仍须可用。

新房间公共消息为空，成员、Session、工作区、游标和预算独立，开场消息仅填入草稿。已有房间切回不补填或覆盖草稿。受管房间必须保存完整 runtime 和 Workspace 配置；旧运行时和 Workspace 离线迁移命令已移除。

[Workspace](../../docs/workspace.zh.md)通过输入框下方入口配置普通目录、可选 HTTPS 仓库和 GitLab 认证。新房间只准备普通成员目录，模板建议不自动生效。应用先结清旧执行，再准备目标、提交配置并更新已有 Session 代际；保留文件和公共历史，不自动重放旧工作。GitLab 只引用全局 OAuth 连接，短期凭据由服务管理，不再接收或保存房间 PAT。已有仓库和项目只读，回访只做本地核对。

应用使用稳定操作 ID 和配置版本，提供 `GET /v1/workspace`、`POST /v1/workspace/apply`、`POST /v1/workspace/retry`。超时先查询，失败显式重试原目标；部分提交和不确定写入阻止执行。管理身份、请求结构与页面凭据处理见 [Workspace 接口](../../docs/workspace.zh.md#管理接口)。旧运行时与 Workspace 迁移命令已移除，受管房间必须具备完整 runtime 和 Workspace 快照。

每个标签页独立保存查看对象，切换或关闭页面不停止后台 Agent、工具和通知投递。不同房间可以并行，每个房间仍只有一个执行名额；重复查看健康实例不重建 Session、不改变成员状态或预算。列表按房间显示目录状态、运行状态、操作进度和错误，同房间冲突操作串行，其他房间仍可查看及执行。草稿、引用、提醒进度及未确认发送分别保存在当前标签页各房间中。未确认发送保留完整原身份和消息 ID，身份过期后须先核对历史，再放弃重试。迟到响应不会修改新房间页面。创建和打开使用持久化去重 ID，响应丢失后可核对列表并显式重试，刷新不会自动续跑；创建成功但打开失败的房间仍留在列表。

房间列表的「删除」经确认后停止目标房间，验证当前及历史私有会话已结清，再永久移除整个房间目录：公共消息、成员状态、所有 Agent 私有会话、诊断、资源副本和工作区文件一并删除。团队模板和其他房间保留；删除当前房间不会自动选择另一个房间。操作者接口 `POST /v1/rooms/delete` 要求目录 `service_epoch`、human `caller` 以及精确的 `room_id` / `room_instance`。后台操作由服务持有，不随 HTTP 断开取消；通过 `GET /v1/rooms` 的房间 `busy`、`status=deleting`、`deletion_phase`（`closing` / `removing`）和 `error` 核对进度，房间从列表消失才表示完成。失败或服务中断后不会自动继续删除或恢复该房间，须核查并明确重试。进入 `removing` 前先持久记录执行已结清，因此即使数据只删掉一部分也能重新打开目录并明确重试。

点击顶部房间名或旁边的编辑图标可就地修改名称，Enter 保存、Esc 取消，也可点击旁边的保存/取消按钮；保存后同步更新标题和左侧列表。`POST /v1/rooms/rename` 携带目录 `service_epoch`、human `caller`、精确 `room_id` / `room_instance`、`name` 和原名称 `expected_name`；名称去除首尾空白后须非空且不超过 256 字节。原名称不匹配时返回冲突，避免覆盖其他页面的修改。当前显示名仅原子更新 `rooms.json`，创建收据和冻结配置保留原名称；消息、草稿、成员、工作区和 Session 身份不变，不重启执行。响应丢失后核对目录，不自动重发。

管理接口：`GET /v1/rooms` 返回每项的 `status`（目录）、`runtime_status`（`closed / opening / ready / failed`）、`busy`、`runtime_error`、可用 `service` 和安全成员名称。顶层 `busy` 仅表示共享资源操作；会话查看选择保存在浏览器本地，无有效历史选择时优先进入默认助理。

`POST /v1/rooms/open` 要求管理器 `service_epoch`、human `caller`、`room_id`、`room_instance` 和稳定 `open_id`，返回 `running / completed / failed`，完成时附 `service`。`GET /v1/rooms/open?service_epoch=…&caller_id=…&open_id=…` 只查询原回执。相同 ID 不重放，失败后检查并用新 ID 显式打开。旧 `/v1/rooms/activate` 已移除。普通 Room API 携带实例的完整身份；`GET /v1/info` 也须带 `room_id`、`room_instance`、实例 `service_epoch` 和 `caller_id`。

`rooms.json` 仅支持版本 3，以完整身份的 `owned_rooms` 记录运行归属，并保存 `opens` 和逐房间 `shutdowns`；旧全局选择、单房间归属及激活回执不再读取或迁移。正常启动逐房间恢复既有启用意图；异常回合不自动重放，暂停且已结清的房间按需打开。退出冻结全部房间后并行结清，任何资源未结清均保留归属和根目录锁。

本人消息使用浅绿色气泡，他人和 Agent 消息使用浅灰色气泡。未查看的操作者 @ 提及以消息左侧绿色强调线标识。页面处于前台且获得焦点时，消息至少一半进入可视区并连续停留 1 秒后，自动标记该条已查看；超过视口高度的长消息以半个视口为可见门槛。快速划过或后台页面不计入查看。消息区右下角的提及提醒跳转到最早的未查看提及，仍按可见停留规则消除提醒；新消息入口位于其下方，点击回到最新消息。已查看消息移除强调线，气泡底色保持不变，计数逐条减少，不会连带清除更早未查看的消息。@ 用户名本身始终保留深绿底白字标签，表示这里存在提及，与是否已查看无关。查看进度在切换房间和标签页刷新后保留，不推进 Agent 未读游标。

### 单 Agent 会话 API

私信使用 `is_direct=true` 和内置 `agent_role_id` 创建，复用既有 Room 的打开、身份与恢复机制。默认个人助理作为固定入口不可删除，手动新建私信仍可删除。创建请求示例、初始化重试及历史已删除默认实例的显式恢复见[会话与默认助理](../../docs/conversations.zh.md#管理接口)。

直接对助理说明连接飞书、关注群和持续提醒的需求即可；只连接不附带启用通知或工具。对话授权、账号／群选择、接续策略和完整设置入口见[飞书使用说明](../../docs/lark.zh.md#通过对话接入)。

以下协作流程使用显式参数保留的单房间模式。

## 启动与协作

需要 Go 1.27+、完整安装的 DSH、回环监听、进程观察和可用模型网络/凭据。按 [Agent 运行时](../../docs/agent-runtime.zh.md) 准备环境。显式单房间模式需自行准备包含 `reviewer`、`observer` 两个命名 profile 的 JSON；每个 profile 配置 `binary_path`、`workspace`、`instructions` 和 `model`，字段见 [dshProfile](dsh.go)。路径须为本机绝对路径，工作区须预先创建。凭据由服务环境注入，服务不自动加载 `.env`。

```sh
go build -o /tmp/teamx ./cmd/teamx
/tmp/teamx serve --room design-review --state-dir ./.teamx/data/dsh-team \
  --listen 127.0.0.1:8088 --dsh-config /absolute/dsh-team.json --session-max-turns 16
```

在浏览器打开 **http://127.0.0.1:8088/** 使用内置 Room 页面。服务也会在 stderr 打印 `Room UI:` 地址；自动分配端口时使用打印的地址。无需前端构建或独立进程。

页面提供公共消息流、发送与引用、较早历史加载、配置角色选择与成员添加、启用/暂停协作及取消当前回合，展示成员未读、游标、会话次数和错误。消息与成员约每 1.5 秒同步；切回页面立即同步，连接失败保留现有消息并自动重连。新成员先添加并启用，再发送任务；加入前历史不产生未读。普通输出仍然私有，成员选择沉默也属于正常行为。

处理提示旁的停止图标仅取消对应 Agent 的当前回合，结清后显示「已停止」。Agent 保持启用，不自动重试被取消的回合；发送纠正消息后可继续处理。侧栏「暂停」停止后续调度，当前回合继续执行。

团队选择位于新建房间窗口，右侧展示保存的成员及协作状态。

页面始终以操作者身份预览，不消费 Agent 未读。单条消息上限沿用 16 KiB；发送未确认时锁定原内容与消息 ID，显式重试不会重复追加，草稿和未确认请求按房间及操作者保存在当前标签页的 sessionStorage。服务正常重启后刷新身份；已有未确认发送保留原完整身份，不改写为新的服务代次。页面不提供 Session 轮换与成员删除，仍可用下方 CLI 执行。

公共历史清空 API `POST /v1/messages/clear` 仍要求操作者携带 `snapshot_seq`；页面将「重新开始」放在消息数旁的「···」菜单，经确认执行下述完整重置流程。

「重新开始讨论」经确认后暂停所有成员并取消当前回合，等待旧执行结清、轮换所有私有 Session，再清空公共历史并恢复协作。操作期间阻止新发言、清空和成员变更，页面仍可刷新和查看进度。成员、角色、工作区文件和累计次数保留，会话基线随换代更新；过程中新增的用户暂停不会被覆盖。新 Session 按需创建，不重放旧回合，也不会删除旧 Session 目录。工作区中已有文件仍可能被新会话读取。

批量操作由服务后台持有，关闭页面或 HTTP 超时不会取消。轮换失败时保留公共历史，成员保持暂停；Session 引用逐个提交，部分成员可能已重置，不承诺跨成员原子回滚。`discussion-restart.json` 记录进度，未完成或失败的批次在服务重启后阻止全队自动恢复，需核查后显式重试。操作者接口 `POST /v1/discussion/restart` 携带 32 位小写十六进制 `restart_id` 和最近一次批次 ID `previous_restart_id`（首次为空）；重复当前 ID 返回已有结果，过期请求报告冲突。`/v1/member/list` 的 `discussion_restart` 返回阶段、结果、错误与未恢复成员。

消息输入框按 Enter 发送，Shift + Enter、Ctrl + Enter（Mac 为 ⌘ + Enter）换行；中文输入法确认选词不触发发送。

房间头部只保留团队成员图标，控制同一个右侧栏展开或收起。点击成员信息区、消息中的成员头像／名称，或消息区底部处理／等待提示，会将侧栏从成员列表切换为 Agent 详情。顶部显示成员、运行状态及启用／暂停操作。默认打开「活动记录」；受管房间可切换至「工具设置」，为当前成员启用工具并选择连接，展示实际账号、实例及沿用账号的有效权限；不再提供旧房间工具的逐操作授权和项目范围配置。切换至工具页签停止读取私有活动；点击「返回成员」或按 Escape 返回列表，多成员时可在详情内切换。两个页面共用同一宽度。桌面端可拖动侧栏左边缘调整宽度，聚焦分隔线后也支持左右方向键及 Home/End；宽度受消息区剩余空间约束，随窗口大小调整，并保存在当前标签页。收起侧栏停止读取活动，再次展开显示成员列表。详情展示：当前状态、读取进度、本会话次数、累计统计与轮换原因。运行中约每 1.5 秒读取当前回合的内存快照，按通知到达顺序穿插显示思考、工具活动与内部回复，每一段可独立折叠。连续的同类文本合为一段；同一工具的连续更新合为一行，若中间出现其他活动，后续工具更新保留在其到达位置。内部回复默认展开，思考和工具默认收起；刷新与回合结束保留展开状态。默认自动跟随最新活动；向上翻看时暂停，滚回底部或点击「回到最新」恢复跟随。工具段展示名称、状态、参数（最多 2 KiB）与文本结果（最多 4 KiB）。活动流最多保留最近 256 段，编码总量不超过 256 KiB，每段文本最多 16 KiB，截断明确标记。结束后显示最近完成回合的活动流、结束原因及错误；活动只接受有序 `events`，缺少该字段的诊断明确显示格式无效，不按摘要推断顺序。当前 DSH 的 ACP 通知按已提交的模型输出和工具事件到达，不保证逐 token 更新，也不展示模型未提供的思考。抽屉关闭后不继续读取，观察不触发执行、不消费未读，也不写入公共历史。运行中快照不落盘，服务重启后仅保留最近完成回合的诊断。

HTTP 的 `/`、`/ui/*` 提供嵌入静态资源，`GET /v1/ui` 在管理模式返回 `managed=true`，页面再从 `GET /v1/rooms` 读取目录、活动身份、操作者、团队展示信息及活动房间保存的成员名称；单房间模式仍返回原身份与元数据，不暴露模型配置或凭据。管理模式拒绝直接增删成员。`POST /v1/member/activity` 仅接受操作者调用，并校验 Room/服务代际及当前成员代际，优先读取当前回合的有界内存快照，空闲时按需读取该成员有界的 `dsh-turn.json`，返回 `Cache-Control: no-store`；成员工具不能访问，成员列表和公共历史不携带私有回复。配置内容、模型凭据及 DSH 全量会话文件不通过此入口暴露。页面同源访问，沿用可信本机使用边界。

操作者终端也可以继续使用 CLI，无需设置成员身份：

```sh
export TEAMX_ENDPOINT=http://127.0.0.1:8088
export TEAMX_ADMIN_TOKEN_FILE="$PWD/.teamx/auth/admin-token"
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
/tmp/teamx member session status --agent reviewer --json
# 失败后继续原操作，ID 和来源目录从 status 取得；--cancel/--run/--reason 须保持原值
/tmp/teamx member session rotate --agent reviewer --operation-id OP_ID --from-generation FROM_DIR --cancel --continue
/tmp/teamx member remove --agent reviewer
```

每个 Room 同时只有一个成员持有执行名额，覆盖按需打开 Session、完整回合及其工具结清。FIFO 按调度 Actor 收到有效申请的顺序排队，不固定角色顺序；等待者显示 `waiting`（页面“等待执行”），`active=false`，不增加回合编号或消耗预算。取得名额后重新核对 Room 的成员资格与最新未读；仍有新输入的原持有者结清后排到队尾。不同 Room 的名额独立，排队不阻止消息读写、用户输入和成员管理。

一个成员同时最多一个回合。忙碌时通知合并，结清后处理尚未提示的新输入；不阅读或保持沉默也能正常结束，同一批未读不会反复自激。`run --retry` 明确再次提示现有未读，不重放旧工具调用。取消请求本身不表示执行已停止；未结清时仍为 `active=true` 并持有名额，成员状态保留等待原因。暂停、移除和轮换撤销等待申请，服务关闭先停止新授予。队列不持久化，重启仍受原 Session 恢复规则约束。

执行期间用户追加消息继续合并，不自动打断当前回合。串行执行不限制发言总数，也不保证讨论收敛或减少 token；发送检查和每轮公开发言预算尚未实施。

`serve --session-max-turns N`／`server.session_max_turns` 必须为正数，默认 16。DSH 按会话累计已受理执行尝试，Room／Inbox 共用；失败和取消计数，任务未受理不计数。第 N 回合结束后继续等待，第 N+1 次正常输入前后台轮换，不需要点击继续或追加额度；无输入不换代、不启动模型。`execution.session_turns` 显示本会话 used / limit 及累计 total，累计次数在轮换、讨论重启和正常服务重启后保留。受管房间保存创建时阈值，修改服务默认值只影响新房间；单房间使用启动配置。旧 `max_turns`／`--max-turns` 明确报错，不作为别名。

旧受管配置加载时将原阈值一次转换为新格式，协作快照保留累计次数与暂停意图。没有可信基线的旧 Session 在下次正常输入前先换代，不能从旧执行编号推算会话用量；没有明确启用证据的旧成员保持暂停，由用户启用一次。新格式缺字段或计数冲突会阻断恢复。非 DSH 后端按自身能力保留原累计约束。

自动轮换保留最新启用／暂停意图、已提示输入和消费进度，不重试失败回合、不绕过异常恢复要求。页面详情显示本会话次数、累计统计和最近轮换原因；正常阈值轮换不弹确认、不发 Room 提示。暂停在等待或清理期间都立即生效。

轮换先暂停新回合，继续合并通知，等待当前回合结束（或用 `--cancel` 取消并等待），关闭并等待旧 Session、进程和 worker，成功后才持久切换目录。新 Session 按需创建，成员身份、历史、游标和累计次数保持。默认完成后暂停；`--run` 明确恢复调度，不自动重试已提示的输入。pause 可以撤销待完成轮换的 `--run`。

**轮换重置私有上下文，无自动摘要或私有记忆迁移。** 新建或正常恢复 Session 的首个业务回合获得角色、当前工具身份与规则，并额外提供最近最多 20 条 Room 消息、最多 25 项未完成事项摘要、已保存未知动作的数量及查询入口。恢复材料最多 32 KiB，预算不足会明确省略；预览不改变已读、Inbox 消费、回答关联或业务进度。成员按需用 `read --all/--latest`、`matters sources/source/read`、`actions read/list` 查回详情，已回答问题不重问、已确认动作不重发、未知结果先核实。重要进度及时写入现有事项或 Workspace 文件。旧发言与外部副作用保留，不能自动重放；旧私有目录保留。`dsh-turn.json` 是最近回合的诊断，不是交接摘要。

轮换命令最多等待约 5 秒，超时报告 incomplete / outcome unknown；服务继续持有操作与旧资源的清理责任。`member session status --agent ID --json` 或 `execution.rotation` 返回操作 ID、固定 from/to、累计起始值、最后确认阶段及错误。阶段为 `prepared → old_closed → completed`；waiting / closing 表示本进程正在处理，failed 保留最后确认阶段。完成只表示引用与基线已提交，新后端仍按需打开。

CLI 自动生成操作 ID 并读取来源目录，也可显式传入 `--operation-id`、`--from-generation`；错误输出保留这两个值。相同 ID 和相同参数查询原结果；失败后用 `--continue` 继续原操作，取消、继续调度及原因参数必须保持原值。活动操作拒绝其他 ID，过期来源目录报告冲突。HTTP `POST /v1/member/session/rotate` 必须携带 `operation_id`、`from_generation`；可选 `rotation_reason`、`cancel`、`resume`、`continue_rotation`。`POST /v1/member/session/status` 只查询。未知提交结果须先结清、重开存储并核对，不能用普通启用盲目重试。

旧执行的进程、工具、协议读取和 worker 全部退出后，显式轮换可以丢弃失败会话。无法确认结清或持久提交不确定时，仍禁止替代执行。轮换期间的新暂停立即保存；完成回调不会覆盖它。

用户的启用／暂停意图独立于故障暂停保存。Ctrl-C 正常停机后，原先启用且可恢复的成员自动恢复调度，原 Session 按需恢复，不重放被中断的回合；手动暂停的成员保持暂停。已启用成员遇到可恢复的后端故障时，服务在旧进程、工具和 worker 全部结清且没有已保存的未知动作后，自动轮换并用正常 Room／Inbox 输入继续工作。新 Session 清除私有上下文并补充共享恢复材料，不重放旧调用栈。连续故障最多自动恢复一次，次数跨重启保留，成功回合或手动「启用协作」后才重新允许恢复。清理未结清、未知动作、鉴权／配置错误及模型限流保留暂停或阻断；处理原因后仍可点击「启用协作」。按钮附近说明是否会重置会话。可识别的限流只展示固定 HTTP 429 提示，不公开上游错误正文或凭据。

当前 DSH 回合默认最长 15 分钟（`dsh_turn_timeout`），普通输出保留最多 64 KiB；输出 token 上限使用模型配置，模型请求重试为 0。固定会话回合阈值不能代表上下文容量；没有货币费用上限、精确 token 记账或自动摘要迁移。

## CLI 工具与读取语义

适配器向真实 Session 绑定六项环境：`TEAMX_ENDPOINT`、`TEAMX_ROOM_ID`、`TEAMX_ROOM_INSTANCE`、`TEAMX_SERVICE_EPOCH`、`TEAMX_AGENT_ID`、`TEAMX_MEMBERSHIP_ID`；另提供绝对路径 `TEAMX_CLI`。工具不得自行切换身份，也无成员管理权限。受管模式下操作者须指定端点及已打开的 `--room ROOM_ID`（或命令的 ROOM 位置参数），CLI 从目录取得完整实例身份，查询不会隐式打开；认证后的固定 human 与正文／查询身份须一致。Agent 另带随 DSH 会话签发／撤销的临时执行凭据，不能冒充 human；DSH 子工具通过临时 `TEAMX_EXECUTION_AUTH` 传递同一凭据，避免 DSH 对 `TOKEN` 名称的默认过滤。凭据不进入冻结 ToolEnv 或恢复材料。

Agent 用 `read --unread --limit 20 --json` 按序消费，成功交付一页后 CLI 确认游标，再继续下一页。每页最多 100 条消息及 1 MiB 正文。一个成员同时只有一张有效未确认页，30 秒过期；并发消费返回 busy，历史预览不受影响。stdout 失败、确认失败或结果不确定不会越过未交付页；已提交游标不回退。重启使旧收据失效。

`--latest N`、`--all`、`--unread --peek` 都是预览，不推进游标。固定快照读取期间新增消息留待下一次读取。自己的发言不计为自己的未读，也不清空先前未读。`send --id ID` 按发送身份与 ID 去重；相同请求重试返回原序号，不同内容冲突。结果不确定时使用原 ID 与原内容。

Room 同时最多一个通知批次，每个目标最多等待 100 ms；失败约每秒重试，每 5 秒核对状态。成员校验完整身份及 Revision，不累加重复通知。任务名额和通知状态有界；Room 消息提交后即使通知失败仍保留。

## 正常恢复与退出

同 `--room`、同 `--state-dir` 重启。启动先独占并校验状态、恢复调度检查点和重新绑定 Actor，再发布就绪。RoomInstance、MembershipID、序号、版本和游标保留，ServiceEpoch 更新；旧工具请求被拒绝。孤立文件不复活已移除成员。

DSH 只有正常关闭证明、同产物/工作区/角色/模型配置才允许原 Session 按需恢复；正常完成或已正常结清的 interrupted 成员按保存的启用意图恢复调度。遗留 running 提交为 interrupted，不自动重放。原 Session 无法恢复但旧执行已结清时，符合上文条件的故障可自动轮换一次；缺 profile 或无可靠停止证明只阻止该成员，Room 记录和其他成员保留可用。损坏或身份冲突的权威数据会阻止就绪。

重启先按持久轮换阶段核对引用与协作基线，再恢复调度；保留最新协作意图，手动轮换默认暂停。符合正常恢复条件的完成／中断回合及已完成轮换的新代际可按启用意图继续；中断回合保留历史，不重放旧调用栈。已运行的新 Session 正常关闭后沿用原会话恢复。异常退出、旧引用或未结清执行不能靠轮换绕过核查；不提供宿主崩溃接管。

Ctrl-C / SIGTERM 先停止新接纳，结清管理操作与成员任务/Session，再关闭 Room、Runtime 和 Store。移除先提交关系删除，拒绝旧工具，再取消并等待资源。清理超时明确报告并保留目录所有权；不能把发出取消或进程退出请求视作结清。

服务关闭使用 30 秒的等待预算；DSH 的取消、协议关闭和进程树自然退出分别使用取消等待预算（默认各 5 秒）。正常取消并完成关闭的 interrupted 成员保留原会话恢复资格，重启后按保存的启用／暂停意图恢复调度。Session 若因取消未结清、协议或进程观察失败而禁用，关闭也返回具体原因；强制回收或进程非零退出同样明确报错且不写入正常停止凭据。已有记录缺失的凭据不会自动补写。

## 存储与恢复边界

版本 1：`room.json` 保存身份、完整历史、成员 runner/profile 和游标；`agents/<MembershipID>/agent.json` 保存调度水位与回合；`session.json` 保存 DSH 目录引用；DSH 的 backend.json、home、会话历史位于引用目录。`settled.json` 只在旧执行全部退出后记录，供显式重置核查；它不赋予原会话恢复资格，并在再次启动前消费。旧版本已失败且缺少退出证明的目录不会自动补写该记录。轮换支持唯一目录代际与可选 pending，启动前消费新建许可。`rotation.json` 持久保存当前／最近操作的固定目标与三阶段；DSH 成员加载时将 `collaboration.json` 转换为版本 2，同一快照保存 Enabled、累计 Used、SessionGeneration 和 SessionStartUsed。既有房间的 `room-config.json` 按版本升级并保留原阈值；旧会话无可信基线时明确保存空代际，在新输入前先轮换。首轮恢复材料只使用内存供给标记，不增加持久 ready 状态。

当前 runner 仅为 DSH，字段严格解码。用户选择重建后已移除旧事项／决定／动作、索引迁移和旧 DSH 目录推断；请用新目录创建 Room，旧数据由用户处置，服务不自动删除。当前正常关闭的 DSH Session 仍按准确引用与结清证据恢复。

macOS/Linux 使用非阻塞文件锁，其他平台明确不支持。快照同目录临时写入、文件同步、原子替换及目录同步；文件 0600、目录 0700。跨文件无事务。替换或目录同步结果不确定会冻结 Store 写入，必须结清、关闭并重新加载核查。损坏、未知字段、版本与身份冲突不创建空 Room。

默认最多 32 成员、10,000 消息、单条正文 16 KiB、历史正文共 64 MiB；满载拒绝新写，保留历史。编码上限 Room 512 MiB、各私有文件 1 MiB。任意外部工具不保证恰好执行一次。

## 验证入口

```sh
make test
make test-integration
make test-race
go vet ./...
node --test internal/web/tests/web*_test.mjs
make test-e2e-check
```

以上命令从仓库根目录执行，按改动范围选择，日常无需把全仓 integration 与 race 都跑一遍。默认核心测试、常用本地 integration 与少用的真实 e2e 验收分开运行，分层标准与按模块命令见[测试说明](../../docs/testing.zh.md)。

修改 Actor handler 后运行 `go generate ./internal/room`。本地检查需要 Go 和 Node；最后一条命令只检查验收测试能否编译，不执行真实模型或平台操作。普通测试通过不代表真实链路已验证。

真实验收需显式启用，运行方式见 [DSH](../../docs/agent-runtime.zh.md#真实验收)、[Workspace](../../docs/workspace.zh.md#真实验收)、[通知](../../docs/notifications.zh.md#接口与验证)和 [GitLab 工具](../../tools/gitlab/README.zh.md#真实验收)。验收目录应与日常数据隔离；写入结果不确定时先核对状态，再决定后续操作。

消息、调度、身份与持久化设计见 [Room 架构](../../docs/room-architecture.zh.md)。

## 当前待验项

- Room / Web：组件、HTTP、前端和真实 DSH 双房间并行／恢复／退出已有通过记录；真实浏览器 A/B/A 切房、双标签页独立选择、刷新／关闭标签页后后台执行及视觉检查仍待验。成员工具设置与生命周期组合也需分别核实。复跑入口及安装、模型和全新输出目录要求见[运行时真实验收](../../docs/agent-runtime.zh.md#真实验收)。
- 团队协作：串行调度在真实书店任务中的效果。

平台工具和 Git 工作区的具体缺口分别见 [GitLab 工具验收](../../tools/gitlab/README.zh.md#真实验收)与 [Workspace 验收](../../docs/workspace.zh.md#真实验收)。这些是现有链路的验证缺口；旧工具或旧协议的通过结果不能沿用到当前实现。
