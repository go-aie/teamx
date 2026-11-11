# Room 架构

Room 保存共享事实，成员保存私有执行状态。生产执行链为 `Room → MemberAgent → Backend / Session → DSH`，Actorx 提供 Actor、请求及生命周期基础；`cmd/teamx` 是 CLI 入口。

## 模块与身份

| 层 | 主要职责 | 代码入口 |
| --- | --- | --- |
| 管理目录 | 团队创建、房间索引、独立实例的打开、恢复、删除及配置操作 | [management.go](../internal/room/management.go)、[runtimes.go](../internal/room/runtimes.go) |
| Room Actor | 消息序号、成员关系、读取页、游标及通知元数据 | [room.go](../internal/room/room.go) |
| 成员 Actor | 合并通知、申请名额、调用 Session、记录累计次数与会话基线 | [member.go](../internal/room/member.go) |
| 调度 Actor | 单持有者 FIFO、申请去重、撤销与结清后交接 | [turn_scheduler.go](../internal/room/turn_scheduler.go) |
| HTTP / CLI | 校验调用身份，提供用户与 Agent 的读写入口 | [service.go](../internal/room/service.go)、[cli.go](../internal/room/cli.go) |

持久身份是 RoomID + RoomInstance、AgentID + MembershipID；ServiceEpoch 在重新组装服务时变化。操作者预览不消费成员未读；Agent 工具绑定完整成员身份，不能自行切房或管理成员。这是可信本机协议，不是多租户认证系统。

## 私信与默认实例

`is_direct` 是创建时固定的会话属性，贯通创建选择、冻结配置、快照和目录，不加入 Room／成员身份。普通 Room 通过 `team_id` 创建，私信通过内置 `agent_role_id` 创建；二者互斥，运行时不从成员数量推断类型。旧文件缺字段按普通 Room 读取，不扫描迁移。

私信必须恰好一个 Agent，不能添加第二个、移除或替换唯一成员；加载与恢复同时核对目录、配置、快照类型及冻结成员身份，矛盾或零成员状态明确报错。角色、模型、Membership 和工作目录在创建时冻结，相同创建 ID 的重试保留原选择及身份。

默认助理的创建预留与 `rooms.json` 中的 `default_assistant` 记录在同一次提交关联，再复用普通私信初始化。首次启用意图先于 ready 保存；完成后的恢复遵守原暂停、失败与 Session 规则，不以名字查找、接管同名私信或重复创建替代实例。

默认实例按完整 RoomIdentity 禁止删除。旧版本的 deleting／deleted 记录仍可读取，未结清删除仅显式继续；恢复已删除实例时先核对原引用并保存 pending 意图，再创建全新独立身份。提交不确定时遵守 Store 写入锁定，不能换请求或身份绕过。操作入口见[会话与默认助理](conversations.zh.md#管理接口)。

## 消息与读取

消息按单调 Seq 提交，发送以完整发送身份和 ClientMessageID 去重。相同 ID、相同内容返回原结果，内容不同则冲突。发送响应丢失后应核对历史，保留原 ID、身份和内容。

读取分为消费与预览：

- `read --unread` 生成固定快照的一页，CLI 成功交付后确认游标；每成员同时仅一张有效未确认页，30 秒过期。
- 每页最多 100 条、1 MiB 正文。stdout 或确认失败不能跳过未交付页；提交过的游标不回退。
- `--latest`、`--all`、`--unread --peek` 只预览。读取期间新增消息留待后续消费。
- 自己的发言不增加自己的未读，也不清除此前未读；新成员不把加入前历史算作未读。

Room 通过有界通知批次发送未读数量、版本等元数据，失败可重试；消息提交成功与通知成功分别处理。成员校验身份和版本并合并通知，不累加重复数量。同批未读不会在成员沉默后反复触发回合。

## 回合、名额与轮换

调度器按有效申请到达顺序授予一个执行名额，不按角色固定顺序。等待者继续处理通知和控制消息，`waiting=true`、`active=false`，不增加受理次数；获名额后再次核对成员资格和最新输入。当前持有者结清后，有新输入则重新排队。

名额覆盖懒打开 Session、整个模型回合、工具和取消结清。暂停、移除、轮换会撤销排队申请；取消未结清时仍保持 active 并持有名额。服务退出先停止授予，再结清成员和调度器。队列不持久化。

DSH 默认每会话受理 16 回合，由 `server.session_max_turns`／`--session-max-turns` 设置，受管房间保存创建时阈值。Room／Inbox 共用累计 Used，失败和取消计数，任务未受理不计数；会话次数由 Used 与 SessionStartUsed 的差值推导。达到阈值后只在下一批正常输入前请求服务协调器换代，不申请模型执行名额，不要求人工续额；无输入不轮换，用户暂停始终有效。

自动与手动轮换共用生命周期准入及三阶段持久协议，实际换代同步更新会话基线，累计次数不清零。旧配置与调度快照按版本可靠转换；未知旧基线保持为空，在新输入前先轮换，损坏状态阻断执行。失败、取消、未结清和提交不确定仍受原异常恢复约束。金额、上下文容量及公共发言条数不由会话回合阈值控制。

## 房间生命周期

创建先持久保存选择和创建身份，再写入冻结的团队、角色、资源、runtime 与 Workspace 配置。相同创建 ID 的重试不能改变选择；`ready` 表示目录创建完成，打开实例是单独步骤，页面在创建确认后衔接打开。

管理器以完整 `RoomIdentity` 管理独立实例及操作槽。打开目标房间不停止其他房间，普通请求按 `RoomID + RoomInstance + ServiceEpoch` 路由，找不到实例不会隐式打开或改写身份。不同房间的长操作不持有全局目录锁，同房间的打开、Workspace 应用、重启讨论和删除串行。共享连接变更仍结清实际引用该连接的所有调用。

页面选择保存在各标签页的 `sessionStorage`，不写服务级选择；草稿、引用、未确认请求和私有活动按房间隔离。后台目录更新及迟到响应不能覆盖当前视图。`/v1/rooms/open` 以 `open_id` 去重，提供独立查询回执；旧 `/activate` 已移除。

启动逐房间核查归属、操作、协作意图和 Session 证据；已启用房间无需被页面查看即可恢复，暂停且已结清的房间按需打开。中断和证据不足的回合不会自动重放，一个房间失败只阻断自身。退出先冻结所有实例的新执行，再用统一期限并行结清服务、工具、通知查询及生命周期 worker；未结清保留归属与根目录锁。

重新开始讨论先暂停并结清全队，逐个轮换私有 Session，成功后清空公共历史；保留文件、成员和累计次数。跨成员无原子回滚，批次进度写入 `discussion-restart.json`；失败或中断后阻止自动恢复，须核查后显式重试。

删除先保存 `deleting`，取消通知与工具并验证当前及历史 Session 全部结清，再持久进入 `removing` 并删除房间目录。操作由服务持有，不随 HTTP 断开取消；房间从目录消失才表示完成。失败和重启不自动续删。重命名仅更新索引显示名，使用原名称检查冲突，不改变房间身份、创建收据或 Session。

## 存储与失败边界

```text
data/                                # 配置文件旁的数据目录
  rooms.json                         # 目录、运行归属、打开与退出回执、管理操作
  platform-connections.json          # 全局连接及私有凭据记录，0600
  credentials/                       # Workspace 的房间归属凭据
  rooms/<room-id>/
    room-config.json                 # 冻结配置和生效 Workspace
    room.json                        # 公共历史、成员、游标与投递回执
    tool-bindings.json               # 成员工具与连接引用
    discussion-restart.json          # 重新开始批次
    agents/<membership-id>/          # agent/session 状态、代际引用及 collaboration.json
    resources/                       # 创建时冻结资源
    workspaces/<membership-id>/      # 默认工作目录
    notifications/                   # 来源状态与收件箱
```

各快照使用同目录临时文件、文件同步、原子替换和目录同步；目录 0700、文件 0600。跨文件没有事务，管理操作必须记录进度。替换结果不确定时冻结 Store 写入，结清并重新打开后核对磁盘，不能同进程绕过。

启动独占目录并严格校验版本、字段和身份；损坏数据不替换为空房间，孤立文件不复活已移除成员。受管房间要求完整 runtime、Workspace 和正版本；旧运行时及 Workspace 迁移命令已移除。

默认上限为 32 成员、10000 消息、单条 16 KiB、历史正文合计 64 MiB；满载拒绝新增，保留历史。正常退出和私有会话恢复见 [Agent 运行时](agent-runtime.zh.md)，管理接口与日常操作见[运行手册](../internal/room/README.zh.md)。
