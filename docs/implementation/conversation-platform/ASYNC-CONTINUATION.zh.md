# 对话式平台接入：异步自动接续实施记录

日期：2026-11-11。对应方案 C5；不包含 C6 的账号／群选择表单、字段编辑及完整组合验收。本阶段接入真实连接操作服务的结果生产者；本地验证使用模拟官方飞书 CLI、受控模型组件和实际 Room／Inbox／成员调度，不将这些证据称为真实飞书授权或真实模型验收。

## 已交付

- `connect start ... --notify inbox|none` 可在发起时设置结果通知；`connect notify --operation-id ID --mode inbox|none` 可在原操作上切换。固定业务入参与通知策略独立；同一 ID 重试或更改通知策略不重新授权、保存连接或更改连接结果。
- 授权通常跨回合，接入 Skill 默认订阅后结束回合；短等待最多尝试一次有界 wait。同步返回、用户主动继续与 Inbox 新回合都先查同一操作、最新用户意图及成员配置步骤回执，不引入第三种回合输入。
- `connect acknowledge --operation-id ID --result-version V` 保存精确结果版本的处理回执。它表示 Agent 已核对该操作结果，不表示 Inbox 已消费或原需求已完成。Skill 在核对并处理明确要求的后续步骤后、一次最终反馈前确认；晚到事件发现 `handled_version` 已覆盖时只按普通 Inbox 规则消费，不重复配置或完成反馈。
- status、wait 及人类交互视图投影 `continuation`：通知模式、交接状态、已处理版本、Inbox 序号及阻塞原因。界面区分“连接已保存”和“等待 Agent 接续”，反馈暂停、来源准入阻塞、容量不足、失败恢复及 Session 轮换；始终保留用户发言主动继续的路径。观察不会投递、远程核验来源、推进消费或重新授权。

## 保存、交付与恢复

连接操作仍保存在 `platform-connect/<operation-id>.json`，只有操作服务写结果。通知偏好、不可变待投递事件、Inbox 交付回执和结果处理版本单独保存在 `platform-connect-notifications/<operation-id>.json`，绑定完整 Room／Agent／Membership。无通知的历史操作无需迁移。

1. 操作服务先保存终态与结果版本，再唤醒通知 worker。终态保存失败时不会从内存中的未确认结果生成事件。服务重启先按原规则核对连接，已保存结果保留；临时授权不会重新执行，未确认结果生成对应失败信号。
2. 订阅已经结束的操作时，将偏好与对应事件一次保存；操作稍后结束时，worker 根据已保存结果补齐待投递记录。事件 ID 由操作 ID／结果版本确定，时间取已保存终态时间，重试不改载荷。重复的后台结束回调不改写终态及结果版本。
3. 管理器持有一个可取消、退出时加入等待的本地 worker，唤醒后或每秒有界检查。投递使用 C4 的 `receiveTaskEvent`，保留管理器、成员变更和 Inbox 身份边界，以持久化 `InboxReceipt` 确认交付。收到成功回执后才保存交接确认；容量、busy 或目标不可用时保留同一事件重试。
4. Inbox 正文已写、交付回执／检查点未写，或根通知确认未写时，重开沿用原事件和 DeliveryID 核对。即使条目已消费、正文已回收，也能从原持久化回执返回相同序号，不能再分配一条通知。
5. `none` 停止未投递部分，但保留原事件和历史回执，不撤回已入队条目。已处理版本可以抑制尚未入队的多余通知；已入队的晚到事件仍按正常批次消费。已处理、交付、消费与长期业务完成分别记录。

通知错误不会把已保存的连接改成失败；存储遇到 `commit_unknown` 时仍遵守既有 Store 写入冻结和重开核对规则，不绕过冻结报告成功。处理回执和聊天反馈不构成跨文件事务：确认后反馈前若回合中断，恢复时必须核对聊天历史和步骤回执，补齐缺失反馈，不重放业务步骤。代码保障连接和配置操作幂等；自然语言反馈是否重复仍需要真实 Agent 验收。

## 调度边界

worker 只交付内部信号，不启用成员、补充预算、重试失败回合或调用模型。成员沿用普通 Inbox 准入、共享批次、暂停、预算、公平调度及 Session 轮换。来源核验阻塞不会因为内部事件已到达而放宽，也不会越过前方条目推进游标。成员退出后，原待投递记录及结果保留；同名新成员不能查询、确认或接收旧成员结果。运行时尚未恢复或全员暂停时保持待投递，后续可恢复或由用户主动继续。

## 验证

```sh
go test ./... -count=1 -timeout=8m
go test -race -tags=integration ./internal/room \
  -run '^Test(PlatformConnect|Inbox(MixedPayload|TaskEvent|PayloadLegacy|Consumption|Scheduling|Admission|Lifecycle)|AutomaticRotation)' \
  -count=1 -timeout=4m
go test -race ./internal/room \
  -run '^TestPlatformConnectNotification(StartPolicy|CrashRecovery|ContinuesAcrossSessionRotation)' \
  -count=1 -timeout=2m
go test -race ./internal/room -run '^TestPlatformConnectNotification' -count=1 -timeout=2m
go test -tags=integration ./internal/room \
  -run '^TestPlatformConnectNativeSetupAndCLIProcessLifetime$' -count=1 -timeout=2m
node --test --test-reporter=dot internal/web/tests/web_*test.mjs
git diff --check
```

已通过：连接／混合 Inbox／Session 自动轮换的 integration＋race 回归、后补的启动通知策略／恢复 worker／跨 Session 接续定向 race，以及真实 CLI 进程＋模拟官方 CLI 的授权订阅交付。前端全量模拟 DOM 通过，随后补充的取消订阅后已处理状态提示也通过定向回归。最终 C5 全部通知专项再次通过 race，耗时 12.292 秒，覆盖显式 none 状态及最新阻塞原因投影。

首次全仓回归在并行运行其他重负载测试时，既有 `TestInboxLifecycleActiveTurnBatchAndSweepBound` 超过其内部 10 秒期限；该项单独复测及上述专项 race 均通过。随后无并行重负载的全仓重跑全部通过，Room 包耗时 123.174 秒。

本地专项覆盖：订阅前／中／后完成，重复订阅及结束回调，wait 返回与晚到事件，取消订阅及再次订阅，精确版本处理确认，成员身份隔离，失败／过期／取消／服务重启信号；待投递、Inbox 正文、根交付确认写入中断，消费及回收后重开去重；容量不足与释放后原事件重投，外部准入阻塞与成员退出；没有新聊天时由连接结果触发 Inbox 回合，消费后回合失败不自动重试，以及跨 Session 轮换的接续。前端模拟 DOM 覆盖订阅、已交付、已消费、已处理及各类阻塞提示。

本阶段结束时，真实飞书＋真实模型 Agent 的无新消息自动接续、竞争时自然语言反馈去重及真实浏览器新增提示尚未验收；C3 的既有真实连接证据不能代替这些验收。后续已完成 C6，并于 2026-11-11 通过同账号真实 OAuth 组合链路：连接保存事件自动进入下一回合，群选择事件再次自动进入配置回合，human 消息始终只有一条，两个事件均消费并确认处理。真实 Chromium 的回执、设置、刷新及窄屏检查也通过。详见 [完整对话接入记录](SELECTIONS.zh.md)末尾“同账号真实 OAuth 组合验收”；真实竞争时自然语言反馈去重仍未实测。
