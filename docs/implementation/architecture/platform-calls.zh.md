# 平台在途调用登记与结清

日期：2026-11-20。对应模块边界方案 R6a.1。

## 实施单

| 项目 | 本次范围 |
| --- | --- |
| 范围 | 所有原 `platformCalls` 调用族共用的登记表、取消信号、完成回执、待结清查询和等待；包括工具、原生读取／动作、通知附件、OAuth、连接核验及 Workspace |
| 状态归属 | 登记 owner 迁至 `app/platforms.Calls`；每个 manager 只有一个零值初始化的实例。账号快照、成员／绑定核验、OAuth lease／flight、订阅及持久记录仍由原 owner 管理 |
| 跨模块合同 | `Register`、`Cancel`、`Pending`、`Finish`、`WaitCalls`；`Call` 只暴露取消与只读回执。旧 manager 持有准入元数据，以类型参数关联同一调用，不复制授权或配置状态 |
| 接入与清理 | 删除 Room 的调用 map、直接登记／删除、回执创建／关闭及锁内遍历；全部原登记点改用同一组件，控制操作与关闭路径仍经原门槛选择、取消并在门槛外等待 |
| 验证与停止点 | 验证账号／Room／成员／绑定隔离、撤权、凭据轮换、在途附件／动作、取消后晚到结果、超时保留 owner 及关闭重试；R6a.2 的准入编排和 R6b 的连接配置不在本次范围 |

实施起点为 HEAD `633a0f2` 及已有 R0／R3a／R1a／R5a／R3b 的工作树。保留其他未提交改动和运行目录；本次不迁移运行数据、不重启日常服务、不变更远端配置。

## 合同与锁顺序

[调用组件](../../../internal/app/platforms/calls.go)拥有唯一在途表和完成回执。`Register` 接收原调用上下文的 `CancelFunc`，保留原来的请求关联、超时和 deadline；组件不启动 goroutine，不建立授权缓存，也不自动判断请求已完成。

`Cancel` 只发信号并返回完成回执，父上下文取消也不会移除登记。原执行者在远端响应体、进程、临时凭据、token lease 和相关清理实际返回后，沿原有位置调用 `Finish`。`Finish` 先取消上下文，再删除登记并关闭回执；重复完成不会重复关闭。`WaitCalls` 只等待调用／通知 worker 回执，超时不丢弃登记，后续可再次等待。

锁顺序保持 `manager.mu → Calls.mu`。选择函数、取消函数均在 `Calls.mu` 之外执行；组件持锁时没有 manager 回调。`Cancel`／`Pending` 先复制表项，再运行选择函数；调用点仍持有 `manager.mu`，因此不改变原来的受控选择范围。该内部锁只保护登记表，准入、最后证据复核与绑定变更仍在原 manager 门槛内，等待仍在门槛外。没有新增先无锁核验、后任意执行的窗口。

## 正常入口与装配清单

| 调用族／控制入口 | 接入与保留职责 |
| --- | --- |
| 共用 `preparePlatformCallLocked` | 工具 run、GitLab／飞书原生读取及受管动作继续通过原绑定／账号核验；登记与完成转交组件 |
| 通知附件 | `platform_attachments.go` 在原成员／订阅／已保存正文核验后登记；订阅 key、晚到结果核验、下载大小及权限边界不变 |
| 项目／会话列表 | `platform_gitlab_projects.go`、`platform_lark_chats.go` 的所有请求登记，原超时、成员及最终账号证据复核不变 |
| 飞书绑定证据 | `platform_lark_self_send.go`、`tools_lark_configuration.go` 的自聊与能力核验登记；最终绑定写入仍由旧 manager 负责 |
| 连接复用核验 | `platform_connect.go` 的在途调用登记，按原 operation ID 取消；操作记录及恢复仍在原 owner |
| OAuth | `platform_oauth.go` 的 token 使用 lease、`platform_gitlab_auth.go` 的候选 token 撤销登记；lease／flight map、凭据代际、刷新和撤权流程不迁移 |
| Workspace | `workspace_git_managed.go`、`workspace_scion.go` 的进程调用登记；原项目核验、凭据快照及进程清理顺序不变 |
| 绑定／成员／凭据变更 | 原取消选择条件保留。GitLab／Scion 撤权及连接更新通过 `Pending` 获取同一表的回执；订阅替换通过同一表按订阅 key 取消附件 |
| Room／服务退出 | `waitPlatformCalls` 在原关闭／删除／Workspace 控制路径中取消指定 Room 或全部调用并等待；超时继续保留 root／Store 所有权及重试条件 |

原 `platformCall` 只保留 manager 所需的准入元数据，并嵌入组件 handle；不再拥有取消字段或可写回执。`cancelPlatformCallsLocked`、`finishPlatformCallLocked`／`finishPlatformCall` 是供上述旧调用者使用的薄适配；`waitPlatformCalls` 和成员变更适配负责原门槛及 worker 联合结清。后续 R6a.2 逐族迁移准入及控制入口后，再按实际调用者删除这些适配。本次不移动 Actor，无需重生成绑定，也没有新增打包资源或 Provider。

## 验证与限制

命令、退出码、日志摘要及源码哈希见 platform-calls-validation.json（本地验收文件 `platform-calls-validation.json`）。原始日志和改前源码快照位于 `.local/architecture-platform-calls/`，不覆盖此前阶段证据。

| 检查 | 结果 |
| --- | --- |
| 登记组件与生产依赖边界 race | 通过；覆盖取消与结清分离、账号／调用族筛选、超时保留、异主实例不能完成回执、重复完成、并发登记／取消／完成，以及选择／取消函数重入时不持登记锁 |
| 关键结清用例改前／改后对照 | 同一组 13 个顶层用例、含子用例 47 项均通过 race，Room 分别 24.758／24.511 秒；覆盖工具进程、附件、成员变更、凭据轮换、原生读取撤权、OAuth、Room 隔离和关闭超时后重试 |
| 跨模块 integration race | 全部选择项通过：5 个包、150 个顶层用例、含子用例 364 项；Room 260.583 秒，无数据竞争报告。包含现有工具／绑定、受管动作、连接权限、OAuth 和 Workspace 用例，无跳过失败用例的 `-skip` |
| CLI 构建／e2e 配置编译 | 均通过；产物仅写入 `.local/architecture-platform-calls/`，e2e 只编译 |
| 标准 vet | 仍为已有 12 处非具名字面量告警；与 R3b 最终日志按路径及告警正文比较一致，没有新增告警 |
| 全仓 core／integration | 使用仓库 3／8 分钟上限及 `-failfast`，均重现 Calendar 基线失败；其他包通过。Room 后续未执行用例不计通过 |

改前宽范围 integration 在 8 分钟总上限退出，不能记为通过。随后使用 Go overlay 将 40 个实际改动文件恢复为本轮改前状态，对照相同的关键结清集合；其中五个仅变更等待委托及 import 的适配文件按这一明确差异反向恢复。overlay 没有改测试预期，原 map、登记／完成／取消实现及直接 map fixture 均被恢复；新增组件仍在磁盘，但恢复后的 Room 不使用它。该对照与最终跨模块 race 分别记录，未把宽范围超时改计为通过。

完整回归在 `TestCalendarManagedRecurringPauseRecoveryAndPublication` 的 `calendar_lifecycle_test.go:201` 报 `controlled model turn did not start`。改前源码 overlay 单独运行同一测试，6.462 秒内复现相同失败；本次没有调整 Calendar fixture、预期或 CI 门槛。

本轮只进行本地组件与受控集成验证；未执行真实平台写入、DSH／模型或浏览器验收。

## 完成边界

R6a.1 交付所有原调用族共用的登记 owner、取消、回执和等待。账号解析、绑定准入、最终结果复核、持久化配置、OAuth lease／flight 以及连接／订阅恢复仍在旧 manager。R6a.2 和 R6b 尚未实施，后续逐族／逐操作接入时继续使用同一登记实例。
