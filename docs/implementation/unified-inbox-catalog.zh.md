# 会话目录独立读取实施与验收

对应 [统一 Inbox 存储方案 S1](../UNIFIED-INBOX-STORAGE-PLAN.zh.md#s1--会话目录独立可读)。本次只交付目录读取隔离；S2–S6 保留原计划边界。

## 已实现的行为

- `roomManager` 继续持有目录与 runtime 写入权；启动、`saveIndex`、runtime 预留/打开/完成/失败、共享操作 busy 和退出安装目录快照。可变的时间指针、默认助理、团队成员视图与 runtime 服务信息均冻结；名称映射复用已冻结版本。
- 列表请求读取原子目录，再在 manager 锁外读取已发布的 Room 日志、配置和人类读取回执，最后在锁外编码与写出。普通锁竞争不再返回目录 409；单 Room 操作继续通过 busy 表达。
- 摘要核对 RoomIdentity；发布的 runtime 保留原 ServiceEpoch，迟到读取不安装到新实例。单条正文/配置故障以 `summary_status: unavailable` 表达，页面显示“会话摘要暂不可用”；人类统计继续独立表达 unavailable。畸形 mention 范围也按局部故障处理。
- settings 原子发布时区与版本，配置文件同步期间目录读取上次已安装投影。目录/settings 先发布后发 SSE 信号，observer 只通知；正文与人类回执沿用提交后通知。
- Store 的 `lock` 和 `uncertain` 通过独立健康锁读取，所有生产不确定提交入口统一标记；不持文件 I/O 锁获取健康。未改动 writer 的并发准入、冻结范围与关闭结清合同，这些属于 S3。

S1 不新增持久文件。目录写入仍以原持久提交成功并安装元数据为完成点，随后发布投影及通知；重启从 `rooms.json` 和各 Room 原发布根重建内存目录。正文、消费及回收权威沿用当前路径，本阶段没有新增或放宽可回收事实。

核心说明已同步至 [Server 架构](../server.zh.md#页面更新与连接)和 [Room 架构](../room-architecture.zh.md#房间生命周期)。

## 故障与服务验证

`TestRoomsCatalogDuringBlockedNotificationWork` 使用隔离真实本地 HTTP 服务、Room Actor、平台私有输入分区与 Store。GitLab 来源数据由 fixture 提供，不请求外部平台或模型。先通过真实来源确认路径把一条输入交给私有 Inbox，再分别阻塞：

1. 来源持久化的目录同步，整个保存期间保留现有通知 Gate。
2. 现有 `maintainNotificationInboxLocked` 进入私有维护时的 Room Store 锁；维护持有 manager 锁等待本地存储，释放后正常完成。

每个场景在释放阻塞点前完成 **100/100 次 HTTP 200**，另各取 100 次无阻塞样本。单 Room、一条平台输入、loopback 顺序请求；测量包含请求、读取、解码和响应结束，并非满载或远程性能承诺。

| 场景 | 样本数 | p50 | p95 | p99 | 最大值 |
| --- | ---: | ---: | ---: | ---: | ---: |
| 来源保存，无阻塞 | 100 | 0.312 ms | 2.101 ms | 3.921 ms | 6.483 ms |
| 来源保存，目录同步阻塞中 | 100 | 0.208 ms | 0.402 ms | 0.537 ms | 1.015 ms |
| 私有维护，无阻塞 | 100 | 0.284 ms | 2.633 ms | 4.578 ms | 6.586 ms |
| 私有维护，Store 锁阻塞中 | 100 | 0.193 ms | 0.343 ms | 0.455 ms | 0.482 ms |

有阻塞样本比无阻塞样本更快受缓存预热等影响；验收结论是请求在阻塞释放前独立完成。测试同时覆盖慢响应写出时仍可取得 manager 锁、settings 写盘期间读取旧投影并在完成后读取新版、目录多 reader 与发布竞争、嵌套元数据隔离、提交不确定和关闭、损坏正文/mention/替换实例/替换配置的逐 Room 故障隔离。既有创建、重命名、runtime、默认助理、人类读取和 settings 测试继续核对正常入口。

## Chromium 验证

`TestRoomsCatalogBrowserRefreshAndSwitch` 使用真实 Chromium、原页面与隔离本地服务，两个已打开的 Room 各有一条消息。持有 manager Gate 时刷新页面，目录仍渲染完整；浏览器内另外完成 100 次列表请求，全部成功。释放后双向切换两个 Room，历史和各自草稿保留，零 pageerror。

浏览器 fetch 的 100 个样本：p50 1.10 ms、p95 2.30 ms、p99 4.60 ms、最大 5.60 ms。该数字包括浏览器调度与 JSON 读取。Room 其他 API 和 SSE 准入仍可能等旧 Gate，因此完整聊天恢复及切换在释放后核验；不把本结果解释为所有接口均已隔离。

## 验证命令

```sh
go test ./...
go test -tags=integration ./...
go test -race -tags=integration ./internal/room -run 'Test(RoomsCatalog|HumanReadCatalog|SystemSettings|.*Settings|RuntimeCatalog|RoomManager|DefaultAssistant|AgentName)' -count=1
node --test internal/web/tests/*test.mjs
TEAMX_ROOMS_CATALOG_BROWSER=1 go test -tags=e2e ./internal/room -run TestRoomsCatalogBrowserRefreshAndSwitch -count=1 -v
```

验证结果：

- S1 故障测试、目录/健康/settings/默认助理/runtime 的定向 race，以及最后补充的摘要损坏、删除和退出生命周期 race 均通过。
- Chromium 验证通过，前端 736 项测试通过，架构文档链接与 `git diff --check` 通过。
- 额外的 `go test ./...` 全仓普通回归通过，Room 包耗时 501.492 秒。
- 额外的全仓集成运行达到默认 10 分钟总时限后超时。随后按测试名差集发起 467 个集成专属用例补跑；用户指出可以选择相关测试后，已停止这轮过宽补跑，不将其记为通过。

S1 的完成依据是受影响路径的定向、故障、race 与浏览器验证；全仓普通回归作为补充证据。此阶段不把无关模块的完整集成套件作为门槛。未执行真实 provider、DSH 模型或生产数据写入；这些不属于 S1 的目录读取验收。
