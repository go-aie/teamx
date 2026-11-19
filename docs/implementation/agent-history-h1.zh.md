# Agent 历史读取视图与续页

实施日期：2026-11-19。范围对应 [统一历史读取方案 H1](../AGENT-HISTORY-PLAN.zh.md)。

## 已完成

- `inputSession.historyCall(ctx, callID, tool, args)` 提供 `history_list`／`history_read` 的只读服务契约；固定 schema、说明及只读工具定义可供后续隔离接入，本次未注册到生产 MCP。
- 目录包含仍保留且成员可见的 Room、GitLab／飞书私有流和共享流。沿用正常 Inbox 的流引用，按最近本地保存时间倒序、引用稳定排序；已消费／跳过／无事项的流仍可发现。
- 已知引用可直接读取。Room 使用 `roomVisible`，保留自身发言、公共事件及本人私有事件，排除他人私有事件；共享输入同时核验有效 Grant 和冻结接收集合，重新加入不继承旧记录。
- 目录固定元数据；正文固定首次已发布范围，先过滤可见性再取最近窗口，页内从早到晚，cursor 向更早记录续读。新增不插旧页，同 cursor 重供同页；重新传引用创建最近观察。
- 复用既有业务正文投影、保存通知解码及来源证据，保留作者、回复、事件 `result_ref`、真实截断和附件状态。不执行远端解析、不查询事项进度、不创建来源关联。
- 文本预算按完整记录保留最近连续后缀，cursor 从实际供给边界继续。目录单页 64 KiB、正文单页 256 KiB，每执行回合累计 512 KiB；不足时明确 `history_budget`，最新候选无法放入单页时明确 `too_large`。
- 所有传输复用成员 owner 上的运行时计数。有效同调用 ID 重放不重扣预算，改参拒绝；目录和正文共用最多 4 个快照、5 分钟空闲有效期。失效／被淘汰的调用不会悄悄重新观察。
- 只在实际读取期间持有保留 pin；查询缓存不阻止回收。清空／回收改变保留边界时旧查询明确失效，确知此前清空／回收时提供事实提示。回合结清、成员退出和 Store 关闭释放查询缓存。

## 代码与直接调用

- `internal/inputstream/history.go`：从已验证索引读取保留流元数据，不按消费状态过滤，不为目录读取整批通知正文。
- `internal/room/history.go`：契约、查询缓存、选择／分页、预算、权限和只读正文投影。
- `storage.go`／`room_input_log.go`：随有效原生发布根维护可重建的可见性元数据，目录选取本身不读 Room 正文；提取原生日志的只读解码 helper。正文读取在 Store 锁内捕获发布根／缓存及短期 pin，在锁外校验和读取日志，不增加正文副本或新的 Room 发布权威。现有统一身份／回合门禁仍执行原生 Room 校验，日志损坏会在门禁拒绝整个调用，H3 性能验收需计入这部分既有校验成本。
- `mcp_turn.go`／`member_input.go`：沿原生命周期释放短期查询状态。

服务测试调用同一 `inputSession`；`callID` 由执行框架传入，不是 Agent 参数：

```go
session.historyCall(ctx, "framework-call-1", "history_list", []byte(`{"limit":10}`))
session.historyCall(ctx, "framework-call-2", "history_read", []byte(`{"stream_ref":"stream_...","limit":20}`))
session.historyCall(ctx, "framework-call-3", "history_read", []byte(`{"cursor":"..."}`))
```

正文和目录 cursor 不混用；续页只能传 cursor。调用／快照缓存失效后应重新开始对应查询，流引用本身仍可在正常重启或新 Session 代际重新核验后使用。

## 验证范围

`history_test.go` 的直接服务测试覆盖：

- Room 已消费与未消费、自身发言、公有／私有事件可见性；读取前后逐文件摘要对比，核对无消费或业务写入。
- 已消费、合法跳过、未消费、无事项的 GitLab／飞书正文及附件／截断证据；与正常 Inbox 直接复用流引用。
- 同名跨账号、私有／共享及跨成员隔离；共享撤权后拒绝缓存重放、旧 cursor，重新授权不继承原接收集合。
- 固定目录分页、正文续页、并发追加、调用重试、完整后缀预算裁减和实际续读边界。
- 清空／实际回收使旧正文和目录快照失效；重启／新 Session 代际复用引用，cursor 失效；空闲过期、缓存上限、取消、关闭、回合结清及 pin 释放。
- 已发布正文损坏明确报错；schema 校验及两个工具的只读声明，确认生产工具集合未增加 History。

`internal/inputstream/history_test.go` 另验证元数据索引重建、跳过不影响历史全集、目录不读取正文及正文缺失时读操作失败。

已执行检查：

```sh
go test -race -tags=integration -timeout=3m ./internal/inputstream ./internal/room \
  -run 'TestHistory|TestRetainedMetadata|TestUnifiedRoom|TestSharedInbox|TestInputToolsMinimalContract|TestInputBusinessProjection|TestInputReceiptCanonical|TestMCP.*Turn|TestMemberInput|TestAutomaticRotation|TestDSHRecovery' -count=1
go vet -composites=false ./...
git diff --check
```

上述针对性服务／回归／race、其余 vet 项及 diff 检查通过。`go vet ./...` 仍报告原有测试文件中的非具名外部结构体字面量告警，本次未修改这些文件。全量 `go test -tags=integration -timeout=8m ./...` 两轮均在 Room 包触及 8 分钟总时限，分别刚进入既有平台连接及飞书设置用例；未报告断言失败，但不能算全量通过。其余包均通过。

## 后续边界

本记录属于服务层与本地组件证据。H2 才接入 HTTP MCP、现有材料流程、真实 image 内容块及覆盖输出结束的 lease／共享撤权 handoff；H3 才做恢复 guide、真实 DSH／ACP、真实平台材料和容量性能验收；H4 才移除 `teamx read` 并正式启用。现有正式页、消费证明、Agent guide、恢复供给及 CLI 入口未切换。
