# Agent 历史 MCP 与图文交付

实施日期：2026-11-19。范围对应[统一历史读取方案 H2](../AGENT-HISTORY-PLAN.zh.md)，基于 [H1 历史读取服务](agent-history-h1.zh.md)。

## 实现与隔离入口

- `history_mcp.go` 将 `history_list`／`history_read` 注册为只读 MCP 工具，复用现有 HTTP 身份、框架 call ID、响应大小、完整输出 framing 及 lease 生命周期。参数与 H1 一致，直接传已有 `stream_ref`；正文续页只传 cursor。
- 隔离组合使用 `newHistoryMCPHTTP(cliInbox)`，由 `serverAccess.historyMCP` 明确安装 HTTP／CLI 两种工具集合。HTTP Inbox 模式提供 Inbox 两工具和 History 两工具；CLI Inbox 模式仅提供 History 两工具，实际调用 `inbox_read` 也被拒绝。普通生产构造器仍使用原工具集合，没有新增运行配置开关或启用现有 Room。
- `history.go` 的直接服务调用和 MCP 共用 `prepareHistory`，同调用 ID 返回原观察，改参拒绝；同 cursor 重供同页，缓存失效明确要求重新查询。正文、目录、图片预算属于同一执行，独立于正式 Inbox。

隔离工具调用示例：

```json
{"name":"history_list","arguments":{"limit":10}}
{"name":"history_read","arguments":{"stream_ref":"stream_...","limit":20}}
{"name":"history_read","arguments":{"cursor":"history_..."}}
```

## 材料复用与只读边界

`history_materials.go` 只适配 H1 页的记录范围、操作预算及 MCP 投影，复用 `roomInputNotices`／`inboxImageNotices`、`findNotificationImages`、`notificationImageBudget.prepare` 和现有 `materialAttachments`。`platform_attachments.go` 的同一下载入口继续执行保存引用的归属核验、成员／执行核验、账号与绑定核验、下载生命周期、格式／字节／像素限制及晚到结果拒绝；没有新增 provider 解析器、下载器、材料服务或正式页。

内部 `attachmentRange` 携带已选中的不可变保存记录，替代正式页的范围依据。账号门禁内只重新核验保留元数据和选定范围，正文来自本次已验证的记录；下载和批量正文读取在门禁外。远端图片使用执行成员自己的所选平台连接；私有来源还核对原接收账号，共享来源不借用采集账号凭据。没有可用图片权限时正文仍能返回，图片逐张说明未供给原因。

MCP `content[0]` 是业务 JSON，其余内容块直接承载 image。每条 entry 的 `images` 保留原 position：已供给时使用对应 `content_index`，未供给时使用 `reason`，省略可推断的 status。按最新记录分配图片额度，正文从早到晚；同次读取可去重，新的历史查询不借用 Inbox 或旧 Session 的图片供给记录。

单次及累计文本沿用 H1 限额；本回合图片最多 4 张、原始字节合计 4 MiB，并受服务更小的材料上限约束。不同调用重供同 cursor 的图片也计入累计额度；同调用 ID 的有效重试不重扣。材料描述加入文本预算后，仍按完整记录保留最近连续后缀，cursor 从实际返回边界续读。响应还检查实际 MCP 序列化大小。

所有历史读取均不 reserve／prepare 正式页、不关联事项、不确认消费，不创建 Session 消费证明。存储错误对工具明确报告类别并隐藏私有文件路径。

## 撤权与关闭

- 成员操作 lease 和保留 pin 覆盖准备至 HTTP 输出结束。共享 Grant 的撤销与最终输出准入共用有界 handoff：撤权先发生则拒绝缓存／准备结果；已准入的输出结束后才能结清撤权。图片下载期间不占用该 handoff。
- 执行凭证撤销先停止准入并取消请求，再等待已接收的 HTTP 操作结束。随后释放查询、图片和材料访问缓存，但保留已用预算及过期调用标记，重新连接不能刷新额度。
- 回合结清、owner 关闭和 Store 关闭沿原生命周期释放整个历史查询状态。失败输出可以用相同调用 ID 重试；历史输出不经过 Inbox 的收到正文确认路径。

## 验证证据

`history_mcp_test.go` 覆盖实际 MCP JSON 输出与工具发现、两种 Inbox 模式、参数拒绝、固定快照／cursor／调用重放、GitLab／飞书图像位置及去重、缺失原因、跨查询与传输预算、服务更小上限、共享准备中撤权／输出等待、取消下载、短写重试、回合结束和路径隐藏。`history_materials_integration_test.go` 通过现有 manager／下载器／HTTP transport 验证已消费 Room 图像、无本人绑定时拒绝借用采集凭据，绑定移除后的缓存拒绝，以及共享记录在无成员连接时拒绝借用采集凭据、选择另一账号后实际使用该成员账号的 token 下载。权威存储逐文件摘要、消费状态及正式页记录均有前后对照。

真实 DSH／ACP 使用当前配置的 `deepseek-v4.1-flash-1017`，HTTP 和 CLI Inbox 模式各一次。两次均从 MCP 的一次 `history_read` 获得保存文字和随机验证码图片，答案命中文字与完整像素验证码；网关观察到原生 image 输入，保存正文和提示中未提供完整图片答案。读取前后权威存储一致，未发生外部业务写入。

可复核摘要见 [H2 图文证据](agent-history-h2-evidence.json)，完整私有 trace 位于 `.local/history-mcp-real-20261119-02/report.json`。**真实的是 DSH／ACP／框架桥接／模型和 MCP 交付；保存通知、图片字节及图片下载是本地 fixture。** 这不代表真实 GitLab／飞书平台材料、完整恢复、容量性能或正式启用已验收。

已通过的检查：

```sh
go test -race -tags=integration -timeout=3m ./internal/inputstream ./internal/agent/dsh ./internal/room \
  -run 'TestHistory|TestRetainedMetadata|TestInputBridge|TestUnifiedRoom|TestSharedInbox|TestInputToolsMinimalContract|TestInputBusinessProjection|TestInputReceiptCanonical|TestMCP|TestMemberInput|TestAutomaticRotation|TestDSHRecovery|TestUnifiedMCP|TestInputMCP|TestModelAttachment|TestPlatformAttachment|TestUnifiedCLI' -count=1

go test -race -tags=integration ./internal/room -run '^TestHistory' -count=1 -timeout=3m
go test -race -tags=integration ./internal/room -run '^TestHistoryMaterials' -count=1 -timeout=1m
go vet -tags=integration -composites=false ./...
```

最终存储错误脱敏另通过专项 History race 检查。`go test ./... -timeout=8m` 的其他包均通过，Room 包触及 8 分钟总时限；超时时正进入既有 `TestOnboardingToolInterruptedSettlementKeepsCommittedProjection` 的飞书 fixture 初始化，未报告断言失败。该全量运行不能算通过，本次没有扩大时限重跑或修改无关测试。标准 vet 的外部结构体非具名字面量告警沿用 H1 的已知边界。

真实小样本可使用现有模型目录和只读凭据复跑，输出必须为新目录：

```sh
TEAMX_HISTORY_DSH_REAL=1 \
TEAMX_HISTORY_CONFIG=/absolute/path/to/config.toml \
TEAMX_DSH_OUTPUT=/absolute/path/to/fresh-evidence \
go test -tags=e2e ./internal/room -run '^TestHistoryRealDSHTextAndImage$' -v -count=1 -timeout=7m
```

## 后续边界

H3 负责 Agent／恢复 guide、真实平台保存材料、扩展 DSH 场景与性能验收；H4 才移除 `teamx read` 并在目标服务和安全窗口正式切换。当前未重启正式服务、未轮换现有 Session，也未更改正常消费证明或订阅配置。
