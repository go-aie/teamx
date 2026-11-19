# Calendar 按需 CLI 入口

Calendar 使用现有 `teamx calendar list/get/update/claim/finish`，不再注册五个 Calendar MCP 工具。统一输入 MCP 只保留 `inbox_list` 和 `inbox_read`；Room 消息和事件仍通过统一输入消费。

常驻 Agent／助手指引只保留 Calendar 用途、CLI 入口和按需帮助提示。字段、重复规则、工作日口径、鉴权、幂等重放和报告发布规则集中到 `teamx calendar --help`，无需连接或工具绑定即可读取；各子命令的 `--help` 也可读取同一说明。原两份提示词中的 Calendar 段落各由 317 个英文单词缩减为 47 个，完整帮助不再预加载到真实模型验收指令中。

到期 Room 事件和每轮 Pending Calendar 上下文均指向 CLI 帮助。Agent 先读取最新人类意图，再直接 claim 指定 run_id，完成后用 finish 提交完整报告。修改操作继续携带显式 `--turn current_turn`，由运行时提供私有执行凭据，服务端校验会话、成员、执行代际与回合。CalendarActor、调度器、持久化、版本冲突、幂等和可靠发布继续复用原实现。

原 MCP 合同及真实验收记录保留为历史证据；当前入口以本文和 CLI 帮助为准。本次仅调整 Calendar，其他 CLI 命令职责没有变更。

验证覆盖：CLI 创建及重复规则、已保存默认值的幂等重放、版本冲突、取消后重新领取的拒绝、完整报告与公开发布去重、非法字段和缺失版本的拒绝、缺失／错误回合或执行凭据及终态执行的拒绝，以及无连接读取帮助。原真实 DSH 验收已改为观测 Calendar CLI 的 HTTP 请求，仅记录业务参数和结果，不记录凭据。

专项及 race 回归通过；带 e2e 标签的 Room 测试编译通过。真实已安装 DSH／默认模型在隔离私信会话中完成创建、定时领取、报告发布，并等待执行回合正常结束后检查去重：update／claim／finish 各一次、可选 list 一次、get 零次，公开报告仅一份。真实模型指令未预加载完整帮助，创建回合实际通过 Bash 读取 `calendar --help`。没有平台读取或外部业务写入；既有服务未重启。

真实复验证据位于本地 `.local/acceptance/calendar-cli-on-demand-20261119-2/evidence.json`，验收测试为 `TestCalendarRealDSH`，通过耗时 104.12 秒。日常专项命令：

```sh
go test -race ./internal/calendar ./internal/room \
  -run 'Test(Calendar|InputToolsMinimalContract|UnifiedMCPAuthentication|UnifiedDSHDirectoryRecoverySupplyAndContract)' \
  -count=1 -timeout=3m
```

全仓 `go test ./... -count=1 -timeout=10m` 通过，日志位于 `.local/acceptance/calendar-cli-go-test.log`。
