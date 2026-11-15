# TeamX MCP Inbox 图文读取

日期：2026-11-15。对应[整体方案](../TEAMX-MCP-INBOX-PLAN.zh.md)阶段 2；阶段 1 的传输边界见 [HTTP MCP 接入记录](teamx-mcp-http.zh.md)。本文保留阶段 2 完成时的隔离接入状态；阶段 3 的默认切换与停滞恢复现已实施，见[实施记录](teamx-mcp-default.zh.md)。阶段 4 的 stdio／其他 Agent 仍待实施。

## 实现与验证

共享 `/mcp` 现注册正式 `inbox_read`。只有显式启用 MCP Inbox 的隔离 Session 凭据能够调用；旧的传输验收凭据不会获得 Inbox 权限。生产 `newMember` 保持现有 CLI 入口，没有恢复生产 Agent 或修改其配置、消费进度。

输入为必填 `turn_number`、可选 `limit`（默认 25，范围 1–25）与 `cursor`。成员、Room instance、服务 epoch、Session generation 来自认证上下文，参数不能覆盖。运行中的回合与固定范围按现有 `TurnRecord` 校验。下一页使用上一页返回的不透明游标；省略游标始终重放本轮第一页，改变 `limit` 不能把重试解释成翻页。未确认上一页时返回明确错误；跨成员、generation、回合或失效游标不能读取新页。

读取继续调用 `readReferenceInbox`，由原 Inbox actor 的 `ReadInboxBatch` 领取单个待确认页，先持久保存来源／跟进／内部事件关联。返回投影复用 `projectAgentInboxRead` 的业务字段，包含通知正文、来源与业务引用、已有跟进摘要，以及本轮范围、`page_after`／`page_through`、`has_more`／`next_cursor` 和图片覆盖。内部回执与批次 ID 不进入模型，也不重复生成 `structuredContent`。

通知按原序号排序，图片按通知和出现位置排序；同图多次出现保留全部位置。首块为业务 JSON 文字，随后每张图均为紧邻来源说明的顶层 MCP `image` 块，使用标准 MIME 和 base64。没有 URL 占位或 DSH 私有附件标识。

`notification_materials.go` 保存与 Agent 无关的图片配置、来源与下载接口，新增整轮有界预算。默认每轮扫描最多 25 条通知、最多尝试下载 4 张图、单图 2 MiB、原图累计 4 MiB、单图 16 Mi 像素、累计准备时间 30 秒。相同来源附件去重，分页和重试均不刷新预算；失败尝试也占图片数额度，避免失败下载绕过上限。MCP 编码另受 8 MiB 响应上限限制，并为 JSON-RPC envelope 保留空间。正文必须完整投影；响应过大时先去掉图片并更新覆盖，不静默裁正文。

图片逐项报告 `supplied`、`unavailable`、`omitted` 或 `unsupported` 与安全原因；重复附件标明原通知和位置。纯文本模型不下载图片，并明确说明缺图。下载复用已保存来源的附件解析、账号／成员检查、字节及像素限制。缓存重发前重新检查成员、generation、服务 epoch、订阅和连接；成功图片还校验原账号／凭据证据。内部事件仅建立本地结果关联，不交给平台解析或图片下载。

每个 Session 只保留当前回合、最多 25 页缓存；结果和 base64 不落盘。准备、HTTP 输出及确认共用单成员操作租约，并发请求返回 busy。真实 HTTP 写出在准备完成后另限 15 秒，避免停止读响应的客户端无限阻塞撤销和回合收尾。每页完整写出且 flush 成功后，沿用阶段 1 输出适配器调用 `ConfirmInboxBatch`；短写、断线、取消或投影错误不确认。已确认缓存只重发结果，不重新提交过期 receipt。确认后业务失败或取消不会回退消费进度。

确认失败保留原 receipt，在当前 running 回合内最多尝试 3 次；后续读取先重试该 receipt，仍失败则明确返回错误，不能领取下一页。`ErrCommitUnknown` 保持 Store 冻结／reopen 规则，不盲重试。DSH 回合返回时也检查未解决的确认错误，使其进入成员现有失败状态及私有诊断。没有 completed 回合补确认接口，也没有新增持久交付账本。

隔离 `dshBackend.inboxMCP` 自动签发 `/mcp` 专用凭据，在 ACP new／resume 注入配置，恢复时换新凭据；失败打开和关闭均撤销并结清操作。稳定工具合同、HTTP 地址和入口指引进入 Session 指纹，动态 token 排除。隔离 MCP Session 仅保留 Room 图文预览，Inbox 内容从正式工具取得；生产原预览路径保留，等待阶段 3 受控切换。同轮固定 CLI／MCP 模式，撤销凭据也不会允许该轮改走 CLI。

确定性回归覆盖普通／内部事件、同条多图和重复图、真实业务关联、固定范围和后来通知隔离、游标重放与翻页、跨轮／重启、短写、并发、确认失败与不确定提交、已确认后业务失败、成员移除／generation／epoch／凭据撤销、CLI 混用、纯文本、缺图及整轮图片数／字节预算。GitLab 和飞书的组件集成使用实际平台附件服务；缓存命中不重复下载，来源撤销后不重发缓存。

通过的检查：

```sh
go test ./... -count=1 -timeout=15m
go test -tags=integration ./internal/room -run '^TestMCP' -count=1
go test -race -tags=integration ./internal/room -run '^Test(MCP|DSHMaterials|LarkInboxImagePreview)' -count=2
go test -race -tags=integration ./internal/room -run '^TestMCP' -count=2
go test -tags=integration ./internal/room -run '^Test(AgentInboxCLIOutputAndConfirmation|InboxConsumptionCLIOutputAndIdentity|InboxMixedCLIOutputMustCompleteBeforeConfirmation|InboxEmptySnapshotDoesNotCreateConsumption)$' -count=1
go vet -tags=integration ./...
git diff --check
```

全量运行一次完成，Room 包用时约 360 秒。增加最终页范围字段、入口文字、HTTP 写入时限和剩余故障用例后补跑相应专项检查。日志保存在 `.local/acceptance/mcp-inbox-*.log`，不纳入常规 CI 的真实模型／平台测试另列如下。

## 真实验收

真实 DSH 为 `0.2.1-alpha.1`，模型为 `qwen3.8-flash`，协议继续固定 MCP `2025-11-25`、依赖 `mcp-go v1.5.0`。`TestRealDSHMCPInboxMultiImage` 使用实际 `dshBackend` 自动注入 MCP；普通通知、两张图及同图重复引用、内部事件在同一页正式读取。随机码仅存在于图片像素中，图片文件不放入 Agent Workspace，正文和指引不含答案。新建与正常恢复各只调用一次 MCP，每次返回两张图；恢复阶段更换像素随机码，避免旧答案冒充恢复识图。真实模型输入也观察到标准图片块，正式消费随输出推进。此测试用本地附件 seam，单独证明 DSH／ACP／MCP／模型理解，不能代替真实平台下载。

平台验收通过 `TestRealMCPInboxSavedPlatformImages` 单独执行，没有调用模型：

- **GitLab**：已有通知的真实私有图片通过标准下载客户端获取，作为顶层 MCP 图片返回并确认到序号 1。使用显式 GET-only 测试凭据；本项没有验证 managed OAuth 注册／刷新生命周期，也没有复制或刷新生产 OAuth 授权。生产平台管理与权限路径由组件集成回归验证。
- **飞书**：复用已登录 CLI profile，在隔离安装注册连接和群订阅，通过实际平台附件服务下载已有群通知图片，再经 MCP 返回并确认到序号 1。先前使用的自会话样本被既有 `self_excluded` 准入规则正确排除；最终使用已有群通知，没有放宽产品准入规则。

私有报告：

- `.local/acceptance/mcp-inbox-pixels-20261115-final/report.json`
- `.local/acceptance/mcp-inbox-gitlab-20261115-1/report.json`
- `.local/acceptance/mcp-inbox-lark-20261115-4/report.json`

所有验收均未发送远端评论、修改 Todo 或更改生产 Room。仍接受服务端写出／flush 成功不等于客户端持久保存或模型感知的传输限制；崩溃后未确认输入可在新合法回合重复交付。未验收 stdio 和其他 Agent，阶段 3／4 边界保持不变。
