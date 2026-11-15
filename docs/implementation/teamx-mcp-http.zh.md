# TeamX HTTP MCP 接入与输出确认

日期：2026-11-15。对应[整体方案](../TEAMX-MCP-INBOX-PLAN.zh.md)阶段 1；本文保留阶段 1 完成时的实现与验收，后续阶段 2 见[正式 Inbox 图文读取](teamx-mcp-inbox.zh.md)，阶段 3–4 仍待实施。

## 实现与验证

依赖固定为 `github.com/mark3labs/mcp-go v1.5.0`，该依赖要求 Go 1.25.5，仓库继续使用 Go 1.27。`serverAccess` 在现有监听服务挂载共用 `/mcp`，使用无传输会话状态的 JSON 响应，关闭 GET 流、主动通知与 SSE 升级；请求上限 128 KiB，完整响应上限 8 MiB。生产服务当前不注册任何工具，`output_probe` 只存在于 `_test.go` 的隔离服务器。

专用 Bearer 凭据与普通 CLI 执行凭据分开保存，只保留内存 token 摘要，绑定 Room instance、成员、服务 epoch 和 Session generation。每次请求检查成员和当前 generation；撤销取消在途请求，重启后的新访问层没有旧凭据。MCP 请求不经过 `/v1/` identity envelope，可信身份从 context 取得。现有 Host / Origin 校验继续执行，管理 token、浏览器 cookie、普通执行凭据和匿名访问均不能访问 MCP；MCP token 也不能访问 CLI 路由。

`internal/agent/dsh` 新增与业务无关的 `MCPHTTPServer` 配置，检查 ACP `mcpCapabilities.http`，在 `session/new` 和 `session/resume` 中传递标准 `mcpServers`。端点、名称和头名称纳入恢复指纹，临时头值排除；没有 MCP 配置时保留旧指纹。头值不写入 TeamX 后端配置，也不进入工具环境；成员凭据和截断后的凭据片段纳入输出、活动与 stderr 脱敏。生产成员自动签发、注入和撤销整合按方案留在阶段 2，目前仅隔离验收使用这些能力。

输出适配器使用 SDK 成功 hook 关联当前 `tools/call` 的结果与请求 ID，并计算最终 JSON 编码的长度和内存摘要，设置明确的 `Content-Length`，避免依赖 handler 返回后才发送的 chunk 终止帧。真正输出时逐次核对写入字节数、错误、状态和响应类型；只有完整匹配且 `ResponseController.Flush` 成功、请求未取消，才调用确认。hook、handler 返回或 HTTP 200 本身均不触发确认。短写、写错误、flush 错误、无法序列化、超限、工具错误和可观察的断线均不确认。没有新增持久交付记录，也没有改变 `ConfirmInboxBatch` 的 running-turn 校验。

确认使用有超时的独立 context，防止输出完成后的取消中断确认。确认回调仍负责记录自身失败；HTTP 响应已发出后不能改写错误。正式的失败状态、原回执重试和分页限制留在阶段 2。

`Store` 增加仅在内存中的在途操作屏障。接纳读取时核对明确的 turn number；成员结束回合时先关闭接纳，再等待已有读／输出／确认释放，最后写入终态。成功、失败和取消均经过此边界，等待期间不持有 Store 文件锁。使用真实 Inbox actor 的隔离工具证明：确认发生时 checkpoint 仍为 running，确认后的取消不回退 consumption，旧回合请求不能进入新回合。另有实际 `MemberAgent` 正常结束和取消的检查点测试。

协议验收固定为 **MCP `2025-11-25`**。首次真实 DSH 验收发现默认协商的新协议 `2026-07-28` 会在成功 hook 之后装饰响应，导致预期编码与实际编码不同；适配器正确拒绝确认。本阶段通过 SDK 协议协商配置仅宣告已验证版本，真实 DSH 随后使用 `2025-11-25` 完成调用。扩展新协议必须重新验证最终响应边界，不能删除字节核对或提前确认。相关 SDK 源码见 [Streamable HTTP](https://github.com/mark3labs/mcp-go/blob/v1.5.0/server/streamable_http.go)、[响应装饰](https://github.com/mark3labs/mcp-go/blob/v1.5.0/server/response.go)。

确定性验证包含：标准 MCP client initialize / tools/list / tools/call、顶层 text / image 内容块、短写和实际 TCP 断线、写入／flush 取消、确认后取消、序列化失败、响应超限、身份失效、凭据隔离和旧 CLI 完整输出后确认回归。实际 TCP 用例先等待 net/http 观察到断线；尚不可观察的断线不能由服务端成功写入推断。

通过的主要检查：

```sh
go test ./internal/agent/dsh -count=1
go test -race ./internal/room -run '^TestMCP' -count=5
go test -tags=integration ./internal/room -run 'Test(AgentInboxCLIOutputAndConfirmation|InboxConsumptionCLIOutputAndIdentity|InboxMixedCLIOutputMustCompleteBeforeConfirmation|InboxEmptySnapshotDoesNotCreateConsumption|MCP)' -count=1
go vet ./...
git diff --check
```

补充明确 HTTP 长度后，同一 MCP 专项 race 再运行 3 次通过；真实 DSH 新建／恢复闭环也再次通过。

全量回归按用例补齐：`go test -json ./... -count=1 -timeout=5m` 触及 Room 包的 5 分钟总时限，停止前 601 个顶层用例通过；随后依据测试列表补跑未完成／跳过项，余项运行成功。合计 Room 的 642 个顶层用例中 638 个通过、4 个按原条件跳过，没有测试断言失败或未覆盖项，其他有测试的包均通过。此次尚未取得单次全量运行完成的记录，不能把分段结果写成单次全量通过。逐项日志为 `.local/acceptance/mcp-http-regression.jsonl` 和 `mcp-http-regression-remaining.jsonl`，汇总为 `.local/acceptance/mcp-http-verification.json`。

## 真实 DSH 验收

`TestRealDSHMCPHTTPDiscovery` 使用当前安装的 DSH `0.2.1-alpha.1`，经真实 ACP 子进程注入 HTTP 声明，发现 `mcp__teamx__output_probe` 并调用。新建和正常恢复各读取一页；恢复前更换临时凭据。共 4 次模型网关请求、2 次输出后确认，隔离 Inbox 消费到序号 2，Session 正常关闭。没有使用生产 Room，也没有远端业务写入。

模型端为本地确定性 Completions 网关，用于检查真实 DSH 向模型公布工具、调用 MCP 并把返回正文投影到后续模型输入。这是实际 DSH／ACP／MCP 客户端证据，**不是真实模型理解或平台图片验收**。独立多图协议用例只证明内容块与完整输出边界。

最终私有报告：`.local/acceptance/mcp-http-20261115-framed/report.json`；运行日志：`.local/acceptance/mcp-http-dsh-framed.log`。可复跑命令：

```sh
TEAMX_MCP_DSH_REAL=1 \
TEAMX_DSH_BINARY_PATH=/absolute/path/to/node \
TEAMX_DSH_BINARY_ARGS='["/absolute/path/to/dsh/apps/cli/lib/bin.js"]' \
TEAMX_DSH_OUTPUT=/absolute/path/to/new-evidence-directory \
go test -tags=e2e ./internal/room -run '^TestRealDSHMCPHTTPDiscovery$' -count=1 -v
```

## 后续边界

阶段 2 才增加正式 `inbox_read`、页游标、图片预算、缓存、确认失败恢复以及生产 Session 身份注入；阶段 3 才切换默认入口、移除 Inbox 读前供给并补有限恢复；阶段 4 保留 stdio 与其他 Agent 兼容验收。当前保留生产 CLI、Inbox 预览和 Room 图片路径。

服务端写入及 flush 成功不证明客户端持久保存或模型感知。进程崩溃时仍可能重复交付未确认输入；本阶段没有引入 completed 回合补确认或业务回合事务。
