# Room 调度能力实施与验证

日期：2026-11-13。范围：[Room 调度与 Scion 并行开发方案](../ROOM-SCHEDULING-AND-SCION-PARALLEL-DEVELOPMENT-PLAN.zh.md)中的阶段 1。

团队 `team.json` 顶层支持 `scheduling`：`serial` 保持 Room FIFO、最多一个成员执行；`parallel` 允许不同成员同时执行，每个 membership 最多一个回合。缺省和空字符串为串行，非法字符串、大小写变体、非字符串及 `null` 拒绝。

创建 Room 时保存明确的有效模式，恢复时沿用创建快照。旧快照没有该字段时按串行处理，读取不会重写旧文件或初始化回执，无需存储版本升级。团队模板变化或删除、Workspace 应用、讨论重启、模型选择更新均保留模式；运行中不提供模式切换。README 已补充配置用法，现有团队模板保持原值。

调度器使用完整 `MemberIdentity` 索引持有者，保留单个 FIFO 和维护任务。请求在授予前登记；投递失败保留执行归属并重发，一个成员邮箱满不阻挡其他并发成员。匹配身份与申请编号的释放或确认 Actor 完整退出后才回收；取消和 Quiesce 保留未结清持有者。现有 MemberAgent 的资格复核、预算与 Session 清理协议沿用。

并发发送与已读确认会竞争 Room 提交门禁。HTTP 的 `/v1/send` 与 `/v1/read/confirm` 针对明确 `ErrBusy` 每 10ms 等待重试，约 2 秒后仍忙则返回错误，调用方取消会停止等待。每次使用相同消息 ID、正文、目标或已读回执；其他错误和结果未知不会自动重试。Room 持久提交协议保持原有顺序。

验证已覆盖：

- 三个受控 Backend 在任何一个结束前均进入执行；动态新增第四成员无需调整容量，同期串行房间继续 FIFO。
- 重复、迟到的请求与授予不重复打开 Session；旧释放不影响新回合，持有期间的新申请编号不覆盖记录；满邮箱重投、Actor 退出与 Quiesce 精确处理归属。
- 已授予、尚在资格复核的成员暂停、删除、轮换或完成已读后不打开 Session、不消耗预算；其他执行者保持运行。
- 活跃成员暂停、删除、轮换、失败，以及 panic／不确定后端／退出清理期间，其他成员仍可继续执行；未结清成员继续持有执行归属。
- 三个执行者关房时均收到取消，最后一个未结清时 Store 仍被锁定，结清后不启动新回合。
- Room 与 Inbox 对同一成员保持单回合，新输入留待后续回合；不同成员可以重叠。
- 真实本地 HTTP／CLI 路径的同时发言、确认已读与中途新增输入保持消息唯一和准确游标；明确 busy 可恢复，等待有界，取消及不确定结果不重试。
- 团队缺省、显式配置、非法值、模板修改／删除与重启恢复；旧初始化回执不变；Workspace 与讨论重启集成路径和模型选择重写保留模式；多个成员正在执行时，Workspace 应用等待 Session 清理后才准备目录，讨论重启等待最后一个 Session 结清后才清空历史。

验证命令：

```sh
go test ./internal/room -run '^(TestTurnScheduler|TestRoomTurns|TestRoomSlot|TestServiceShutdown|TestTeam|TestRotation)' -count=1
go test -race ./internal/room -run '^(TestTurnScheduler|TestRoomTurns|TestRoomSlot|TestServiceShutdown|TestTeam|TestRotation)' -count=1
go test -tags=integration ./internal/room -run '^TestManagedTeam' -count=1
go test -race -tags=integration ./internal/room -run '^TestManagedTeam' -count=1
```

以上均通过；追加的模型重写、Quiesce、等待上限和多成员 Workspace／讨论重启用例也已通过对应 race 验证。`go test ./...` 全仓回归通过（Room 包约 306 秒）；`git diff --check` 通过。

本记录的证据范围是组件、race 和使用受控 DSH 组件的本地配置／生命周期集成测试；没有浏览器验收，也没有真实平台写入。后续阶段 2 的团队、成员绑定和真实环境准备已完成，见[环境准备记录](scion-parallel-environment.zh.md)。阶段 3 的真实 DSH／Scion 并发编码、远端发布、交接与集成尚未实施。
