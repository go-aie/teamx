# 快照 writer 与健康围栏

日期：2026-11-20。对应[模块边界方案 R1a](../../ARCHITECTURE-MODULE-BOUNDARIES-ANALYSIS.zh.md#114-r1分别抽出-writer-与目录锁)。R1a 已完成。本单元迁移通用快照设施与健康状态；R1b 的目录锁原语和其他业务 owner 留在原包。

## 实施单

| 项目 | 本次范围 |
| --- | --- |
| 范围 | 快照有界读取、同路径写入串行、原子替换／删除、目录持久化缓存、共享健康围栏和关闭结清；接入全部现有 Store 与独立 snapshotWriter 调用者 |
| 状态归属 | writer 的健康、关闭准入、在途写入、路径槽和目录持久性证据从 Room 迁至 `persistence.Writer`；Store 继续唯一持有目录锁，使用自己的 `lockMu` 同步；业务锁、正文／消费及回收状态不迁移 |
| 跨模块合同 | `Save/Remove/ReadJSON`、`Check/MarkUncertain/Close(ctx)`、`EnsureDirectory` 与窄 I/O seam；`ErrInvalid/ErrCommitUnknown/ErrClosed` 属新包，Room 以原变量转发，保持 `errors.Is` 和错误文案；新包仅依赖标准库 |
| 接入与清理 | 原 writer 算法删除；保留 `snapshotWriter` 薄适配，供 Store、Access、Model、首次访问／setup 和升级入口使用；既有 domain I/O seam 继续供正文日志／回收和故障测试使用。后续 R4／R7／R8 中相应 domain 的 I/O seam 与调用者全部切换到新模块后移除适配 |
| 验证与停止点 | 对照 R0／R3a 的未解决失败；核验提交未知、排队再准入、独立故障域、输入故障上报、来源确认和关闭所有权。目录锁、日志／Repository／旧 Inbox 提交算法、human-read 与配置恢复协议仍在原 owner |

实施起点：HEAD `633a0f2fbcd237c0e9a68f5ba13dc93ff0b68d78` 与包含 R0／R3a 的现有工作树，Go 1.27.1、darwin/arm64。未提交的方案、运行目录及既有改动保留；文档同步更新对应架构状态、实施索引和边界检查，不重写 R0／R3a 历史证据。

## 当前合同

每个原 `snapshotWriter` 实例只创建一个 `persistence.Writer`。薄适配用 `sync.Once` 安全初始化，转发已有 I/O seam，不保存第二份健康标志、路径锁或在途计数。`Store.save/removeSnapshot` 先核对目录所有权，再进入同一 writer。管理目录、各 Room、Access 等原有独立实例保持独立；不会逐文件拆开或合并成全局 writer。

- 同路径写入者先排队，取得路径槽后核对关闭／未知提交围栏；不同路径可以并行。已经准入的操作继续决定自己的提交结果，其他路径出错不改写它的结果。
- rename／remove 一经尝试，其失败或随后目录同步失败均冻结该 writer。编码、临时文件、文件 Sync／Close、目录准备等替换前失败保持确定失败。临时文件清理在在途计数结清前完成。
- 私有输入 `platformInputStorage.result`、共享 `InboxActor.dispatch` 和旧 Inbox 多文件提交／回收的 `markUncertain` 继续上报到同一个 Store writer；来源确认走同一 writer 的新写入准入。读取保持原准入差异，提交未知不冻结 Store 的已提交快照读取。
- Store 先关闭输入 owner 的准入并等待已受理操作／读 lease／runtime，允许已受理操作完成最后一次保存；随后 `Writer.Close` 关闭 writer 准入并等待 I/O；最后在 `Store.mu → lockMu` 下释放原 OS 目录锁。等待期间不持有这两个锁。writer 从不访问目录锁。
- `Writer.Close(ctx)` 可取消等待，但准入保持关闭，只有成功结清才允许外层释放所有权；再次调用可以继续等待。Store 保留原来的无超时 `Close`。上层已有超时／取消路径仍由原 owner 处理。
- `EnsureDirectory` 同时为 Room 正文日志准备目录并复用原 inode 缓存；其上层操作准入与结清继续由原 owner 保证。没有把日志提交或回收算法搬入通用 writer。
- `humanReadStore` 的用户已读恢复及 `configDocument` 的配置锁／pending reconcile 仍是原独立协议；它们只复用原 seam 与通用目录同步原语，不进入 Store 健康域。

## 验证

完整命令、退出码、日志哈希与最终源码摘要见 persistence-writer-validation.json（本地验收文件 `persistence-writer-validation.json`），本地日志在 `.local/architecture-persistence/`。所有专项均为本地组件／集成证据，故障由受控 I/O seam 或 owner 操作返回值注入；不冒充真实文件系统事故或线上恢复。

| 检查 | 本轮结果 |
| --- | --- |
| 改动前现有 Room 定向集成对照 | 通过，3.891 秒 |
| 新包全部 race 测试 | 通过，2.083 秒；排队路径再准入、关闭取消后继续结清、替换与删除故障、有界读取和独立健康域 |
| 跨模块 integration race | 通过，Room 76.716 秒；覆盖输入故障上报、来源确认、旧 Inbox 回收、owner／读 lease 关闭、目录锁释放重开、human-read／配置恢复、管理与生产依赖边界 |
| History integration race | 通过，Room 17.340 秒；沿用当前 History 合同，未修改 MCP 注册或材料／恢复实现 |
| CLI 构建与全部包 e2e 配置编译 | 通过；未执行真实模型或平台验收，产物没有部署 |
| 全仓 core／integration（failfast） | 均失败；Room 分别 56.924／53.641 秒，均重现 R0／R3a 的 Calendar 失败，其他包通过，无超时 |
| 标准 `go vet ./...` | 失败；同原有 12 处非具名结构体字面量告警，日志哈希与 R3a 完全相同，本次改动文件无新增告警 |
| 代码迁移与格式核对 | 七个 I/O 算法在符号改名并忽略注释／空白后与原实现一致；gofmt、diff 及新文件空白检查通过 |

全仓失败仍为 `TestCalendarManagedRecurringPauseRecoveryAndPublication` 在 `calendar_lifecycle_test.go:201` 报 `controlled model turn did not start`。两次全仓命令均保留原 3／8 分钟上限并加 `-failfast`，Calendar 之后未覆盖的 Room 测试不计为通过；其他基线失败没有因此被宣称修复。没有改变测试预期、超时或 CI 门槛。

R0、R3a、R1a 推荐首批已落地，专项边界验证通过；全仓门槛仍有上述基线失败。Room 大包、目录锁原语及其他业务 owner 继续保留。未重启现有服务、修改运行数据、执行真实平台写入或真实模型／浏览器验收。

## 接入清单

| 现有调用者 | 当前接入 |
| --- | --- |
| `storage.go`、`management.go` | 正常单 Room／受管根 Store；所有领域 `save/removeSnapshot` 经薄适配进入同一个 writer |
| `platforms_upgrade_cli.go`、`notifications_upgrade_cli.go`、`platform_model_upgrade.go`、`input_upgrade_legacy.go` | 原升级 Store 的 writer 构造与实例边界保留；不改升级、备份和发布协议 |
| `server_access.go/access_sessions.go` | 原 Access sessionStore 独立实例，浏览器会话保存委托新 writer |
| `first_access.go`、`server_setup.go` | 首次访问与 setup 操作记录，保留原每次保存的实例范围 |
| `model_service.go`、`model_management.go`、`model_migration.go` | 模型凭据、操作和迁移记录，保留原 writer 生命周期与配置文档恢复边界 |
| `room_input_log.go`、`inbox_storage.go`、`inbox_reclamation.go` | 原正文／旧 Inbox 提交和回收算法、I/O seam 留在 Room；目录准备委托新设施，既有未知提交仍经 `markUncertain` 围栏所属 Store |
| `input_stream_storage.go`、`shared_inbox.go` | Repository 保存用同一 Store；私有／共享非快照提交未知仍由原 owner 识别并上报同一 writer |
| `human_read.go`、`config_document.go` | 独立恢复协议和故障域保留，只委托通用读／目录原语 |

本轮没有移动 Actor，不需要重生成绑定；没有修改生产入口路径或打包／CI 清单引用的文件路径，不需要变更打包清单。新包已在生产依赖检查中要求实际存在。
