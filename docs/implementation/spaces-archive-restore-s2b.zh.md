# 空间归档、结清与恢复（S2b）

日期：2026-11-24。状态：S2b 已实施，完成下列范围验收；结合 [S2a](spaces-create-switch-s2a.zh.md)，个人多空间 S2 完成。范围依据[主方案第 11.4 节](../WORKSPACE-AND-MULTI-USER-ANALYSIS-PLAN.zh.md#114-第二批实施单独立可用的双空间)。平台支持矩阵沿用 S2a，新空间的飞书／Scion 限制继续生效；不开放多账号、强执行隔离、永久删除、跨空间移动或 Gordis P3。

## 使用

在当前空间的“空间设置”中选择“归档空间”，阅读停止范围后点击“确认归档”。归档停止该空间的 Agent、通知采集和定时执行；空间选择器继续列出它，并标明“归档中”或“已归档”。归档页提供状态反馈，其他空间可继续使用。

恢复时选择已归档空间，在“空间设置”中点击“恢复空间”。会话、成员、工作文件、模型、连接和 Computer 身份继续保留；恢复使用新的运行对象和 epoch。尚未设置模型的空间恢复到独立 setup。普通切换仍不停止后台工作。

若结清超时，页面显示失败反馈，可点击“重试归档”；关闭仍作用于原实例，完成后才开放恢复。登记提交结果不确定时，保守关闭目标空间准入，保留持久证据，要求重启核对；不自动复制空间或重放操作。

## 实现与责任

`spaces.json` 是空间状态权威：`active → archiving → suspended`；恢复持久发布 `active` 后才装配新实例。新增接口为 `POST /v1/spaces/<id>/archive` 和 `POST /v1/spaces/<id>/restore`，正文仅接受 `{expected_version}`，校验正式主体、owner、状态及版本；执行令牌无控制面权限。`GET /v1/spaces` 返回运行状态、归档工作状态和可读失败反馈。

- `spaceServer` 只持有目录、原运行实例引用和归档等待任务。原 `SpaceRuntime` 直接拥有 `roomManager`；通知、日历、Room、平台操作及 Computer 的冻结／取消／等待仍由原关闭协议负责，没有第二套业务 manager、writer 或结清协议。
- 先持久登记归档意图，关闭路由准入，撤销本空间 SSE／执行凭据并取消运行上下文。setup、普通 HTTP、回调及 Computer viewer 不能绕过归档状态。关闭原实例的 entry 上下文，也阻止 setup 完成后自行重开业务。
- 原工厂先等待已受理 HTTP／setup worker 和业务资源。30 秒等待超时保留相同 entry、runtime、操作记录及未释放目录锁，恢复被拒；重试继续等待并调用同一 runtime 的 Close。部署关闭也等待已准入的归档任务，全部持久写入结清后才释放控制面锁。
- 只有原 Serve owner 已返回且 Close 成功，才登记 `suspended`。恢复重新构造 `roomManager`、通知 Manager、模型／Computer controller 及 runtime epoch，不在 stopped 对象上 Start。原 Space、Room、Membership、profile 和绝对路径不变；默认空间保留原无空间 endpoint，避免改变现有 Session 指纹。
- 重启不启动归档空间；中断于 `archiving` 的登记继续原状态操作，不通过打开业务 manager 重放历史。原进程的 Go 实例已不存在，恢复时后端／Computer 对持久的 Session、执行与 unknown 证据继续沿原合同核验，不把未知外部结果改写为成功。目录及 Computer 实例的持久占用不因归档释放，外部工作文件不删除。
- 共享登录与空间业务准入分离。默认空间归档甚至全部空间归档后，控制面登录、会话查询、空间列表及恢复仍可用；其他空间的浏览器凭据与流不被撤销。恢复后的旧运行 epoch、执行令牌、服务身份及 viewer 标识不能操作新实例。

## 验证

所有写入均使用临时目录或新建 `.local/spaces-archive-*` 验收目录；未改动日常 Home、平台连接或 Runtime。脱敏结果见证据文件（本地验收文件 `spaces-archive-restore-s2b-evidence.json`）。

- 实际 manager／通知 Manager：归档停止、setup 归档、全部归档后重启、显式恢复、新 manager／通知对象／epoch、原 Room／成员身份、旧令牌及回调拒绝、共享登录保留、SSE 撤销、目录占用及文件保留。
- 实际受控长任务：归档等待 30 秒超时后保留原 owner 和数据锁，拒绝提前恢复；另一空间继续使用，释放任务后原对象重试结清，再构造新实例。
- 归档最终回执准备失败时，保持归档中并拒绝恢复；重试只补写回执，仍引用同一原实例。
- 注入登记 rename 后故障：结果不确定时不报告归档成功，关闭目标空间准入，其他空间继续服务；重启核对并完成同一归档，无业务 worker 被偷偷启动。
- 通知 pending：两空间各有已写入 Inbox、来源确认失败的通知，归档后停服、重启并显式恢复；来源继续原 pending，每个 Inbox 仍只有一项，无重复投递或跨空间交付。
- Chromium：创建／setup／改名、A→B→A、双标签、迟到响应、归档与恢复、另一个标签继续使用、恢复后的草稿保留；页面异常为空。
- 安装版 DSH／真实模型：两空间分别对话、归档、停服重启并显式恢复，再分别对话；原两份 SessionID、成员身份及 SpaceID 保留，runtime epoch 更新。通过路径 `.local/spaces-archive-real-20261124-03`，57.85 秒，模型 `deepseek-v4.1-flash-1017`。首次探测的工具往返失败不计通过；后续发现的默认空间 endpoint 指纹变化已修正，最终运行保持原 Session。
- 真实 Computer：macOS 个人部署启动两个全新 Linux AIO Runtime；归档 B 后旧 WebSocket 关闭、实例占用不转移，恢复使用新的 controller、原 ComputerID／Runtime 实例／容器／卷及 screen/profile 身份，旧 viewer epoch 被拒。通过路径 `.local/spaces-archive-computer-real-20261124-02`，64.32 秒；等待原异步准入恢复后验证真实 screen，验收容器和卷由清理函数移除。未把 screen 身份核验扩写为网站 Cookie 内容或未知外部操作成功验收。
- 相关 Go 回归与定向 race、Web 包、DSH／通知包、`go vet ./internal/room ./internal/web`、877 项前端测试及 `git diff --check` 通过。遵循主方案优先相关测试的约束，未重跑整个 Room 包或全仓测试。

真实外部 GitLab／飞书授权、发送及平台写入未在本轮重复；已有支持范围和 unknown／重试合同继续沿用，不将本地受控来源冒充真实平台验收。

## 停止点

S2a／S2b 已完成，可交付个人多空间的创建、改名、切换、独立使用、归档与恢复。Gordis P3 继续独立验收，不作为空间交付条件；下一产品阶段为 S3 的执行隔离路线验证与正式接入，仍不声明多用户可用。
