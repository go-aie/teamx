# 飞书图文发送：原图准备与保留保护

日期：2026-11-20。对应 [接入方案](../LARK-IMAGE-SEND-PLAN.zh.md) 阶段 1。

阶段 1 已实现并通过本地核心、隔离托管装配与 race 验证。能力仅供后续含图动作装配：正式 self／群／回复 schema 未扩展，没有图片上传或消息发送，没有修改日常配置、重启服务或轮换 Session。阶段 2～4 及真实飞书验收仍待实施。

## 实现

- `internal/room/room_retained_read.go`：将原生已发布 root 捕获、不可变日志缓存和保留 pin 下沉为 Store 私有能力。正文在 Store.mu 外通过既有读取器校验；History 保留原有签名、bound 和 snapshot_expired 映射。
- `storage.go`／`input_stream_runtime.go`：两种存储均检查原生保留 pin；InputLog 的其他正式页保护仍按原条件执行。租约期间越过保留边界的清空返回原 ErrBusy，释放后恢复原清空语义。
- `followups_actions_service.go`：新动作通过既有证据校验后附加私有调用上下文，包含服务、成员、运行回合与动作 ID；已有回执快路径不变。
- `room_action_images.go`：一次捕获原生快照，校验完整有序来源集合，只接受当前 Room 中仍保留、可见、可投递的原生人类消息图片；核对附件完整描述、上传者、对象状态与 Room 身份，读取并校验原始字节、SHA256、MIME、尺寸和单图／总量限制。最多 4 张，单图 2 MiB、总量 4 MiB、单图 16 Mi 像素。
- 来源租约持有一个消息保留 pin 和全部图片对象 pin，Check 组合平台调用检查、服务状态与安全固定路径上的 agent／Session 元数据复核；不重新扫描 Room 历史。所有准备失败统一释放，Close 幂等。未新增 inputSession、读取凭据、图片预算、状态文件或长期字节缓存。

没有修改方案第 4.2 节列出的 inbox／history 生产协议与材料链路，也没有修改共享动作账本、授权结构或飞书传输实现。

## 验证证据

新增测试覆盖：两种存储的原图顺序及同字节不同附件、单个原生快照 pin、清空 busy、并发追加／读取、无关过期对象回收；多图准备部分失败与重复关闭后的 pin 释放；错误序号／附件、草稿、缺失／损坏字节、错误归属／描述／Room、active／unconfirmed／恢复不确定对象、MIME／尺寸／像素及跨消息总字节上限；服务、成员、回合、Session 代次与取消检查。

HTTP inbox_read、实际 CLI inbox read 和 history_read 返回的原生 seq／附件 ID 可直接用于来源准备。正式消费后仍可取保留原图；text-only 或模型图片数额度不足不影响准备。准备前后完整 Store 文件摘要及 History 查询／预算检查通过；删除原对象后，旧 History 页仍可重放，新准备拒绝缓存兜底。

隔离托管测试使用真实 HTTP 附件上传／消息提交、CLI actions execute、正式 preparePlatformCallLocked 和 Lark merge 上下文链路。仅在隔离 manager 的 Inspect 中准备原图后停止，验证准确字节、session.Close 释放、没有动作落账或平台消息 POST，正式含图输入继续拒绝。平台身份核验使用本地 CLI 子进程夹具，无真实飞书访问。

通过的命令：

```sh
go test ./internal/room -run '^(TestRoomActionImages|TestRoomAttachments|TestRoomAttachmentMaterials|TestHistory|TestReferenceAction|TestLarkSelfSend)' -count=1

go test -tags=integration ./internal/room -run '^(TestRoomActionImagesManagedActionContextAndCleanup|TestRoomAttachmentMaterialsServiceAssembly|TestRoomAttachmentMaterialsSelectedRangeAndLifecycle|TestHistoryMCPImagePositionsDedupAndIndependentBudget|TestHistoryRoomVisibilityFrozenPagesAndNoWrites)$' -count=1

go test -race ./internal/room -run '^TestRoomActionImages' -count=1

go test -race -tags=integration ./internal/room -run '^TestRoomActionImages(ManagedActionContextAndCleanup|CapturesPersistedSessionGeneration|CheckExecutionWithoutRoomScan)$' -count=1

git diff --check
```

核心回归覆盖既有 History schema、MCP 工具注册与无消费、固定快照／pin 生命周期、图片读取／预算／重放和动作执行／回执行为。未运行全仓测试、真实 DSH／Agent 或真实飞书发送验收；这些属于后续阶段，不能由本阶段的 Store 与隔离托管证据替代。
