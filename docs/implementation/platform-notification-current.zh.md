# 平台通知一步入口

日期：2026-11-19。对应[平台通知默认值与对话选择方案](../PLATFORM-NOTIFICATION-DEFAULTS-AND-SELECTION-PLAN.zh.md)第 2 步。复用第 1 步的默认／恢复配置语义，完成请求协议、冻结和回执恢复；第 3 步的公共导航、卡片与真实验收仍待实施。

## 当前行为

```sh
"$TEAMX_CLI" platforms member notifications --platform-id ID --source-seq SEQ --enabled true
"$TEAMX_CLI" platforms member get --platform-id ID
```

- 纯开关只需准确平台 ID、原 human seq 和显式 true／false。CLI 按参数是否出现区分短／完整模式；任一版本或操作 ID 出现即要求完整三项，显式通知版本 0 仍表示已观测无配置。复杂来源、范围、筛选和绑定字段不能进入短模式。
- HTTP 沿用原接口，短模式使用请求级 `observe_current`。服务端独立按字段出现事实拒绝版本 0、空字段、null、空 patch 和混合输入；未知字段仍严格拒绝。此标记不进入业务 input 或持久化回执。
- 服务端按固定域、Room／instance、Agent／membership、平台、通知类别和原 seq 派生 32 位十六进制操作 ID，不纳入开关、版本、epoch 或时间。重复请求先校验当前成员身份，再匹配原回执；相反开关冲突，旧完整操作和更新来源仍有原来源栅栏。
- 首次受理在同一 manager 锁段内核验 human 消息、读取准确平台当前版本和权威配置，冻结完整输入、账号／凭据与订阅身份，并持久保存 preserved 或 unknown 回执。完整模式的纯开关也不经过为远端来源核验预留的解锁间隙。
- 两种入口共用通知配置准备与 apply 流程。远端查询、采集器结清和等待仍在原配置服务中执行；写入前沿用冻结 admission 校验。冻结后配置、账号、成员或同版本订阅身份变化，均停止应用并保留实际 unknown／部分效果，不更新版本继续执行。
- 并发重复调用、响应丢失、完成回执保存失败和重启只恢复已有记录，不再次 apply。旧回执不要求平台账号仍可用，也不声称配置当前仍开启；失效成员不能恢复旧回执。读取损坏回执停止，初始保存失败不写配置，沿用 Store 的不确定状态保护。
- `get` 省略平台版本时读取当前已核验状态，不建立订阅、操作或接收起点；显式非零版本仍严格检查。chats／projects／tool／status 不获得自动观测能力。

CLI 帮助、平台 Skill 的总开关说明和[通知技术参考](../reference/notifications.zh.md)已同步。未扩展选择命令、公共 Agent guide、角色模板或 Session 机制，没有新增持久化 schema 或自动重试队列。

## 验证

新增 `platform_member_current_test.go` 覆盖两平台的短命令单业务请求、显式 false、当前读取、模式混用、HTTP 字段存在性、human 来源、稳定 ID、原完整操作来源冲突、历史回执、账号移除及成员失效、冻结前当前范围和冻结后并发变更、存储失败与重启。

并发测试通过真实回执保存边界及 Context 停在「已保存原回执、尚未进入配置服务」处：另一请求只读到 unknown；人工配置变化、账号／成员变化和飞书同版本替换订阅后，原调用进入实际配置服务并被写入保护拒绝，不模拟 apply 成功。

- `go test ./internal/room ./internal/platform/lark ./internal/platform/gitlab -timeout=15m` 通过；room 包用时 490.434 秒。此前第 1 步实施记录中的全包未通过是历史结果，本轮完整回归已通过。
- 成员配置针对性回归通过；新增协议／并发／恢复测试的 `-race` 回归通过（25.587 秒）。
- 方案指定的成员／onboarding／Lark search／selection 集成回归通过；最终复跑用时 101.897 秒。
- Web 通知、onboarding 和 selection 共 144 项通过；平台 Skill 所在 `internal/platform/tool` 包通过。
- `go test -tags=e2e ./internal/room -run '^$'` 通过，仅表示编译成功。
- `git diff --check` 通过。

本次不运行真实 DSH、模型、Chromium 或真实提供方验收；实际通知到达及提醒送达不在本切片内。本地协议／配置测试不代表第 3 步个人助理卡片路径已完成。
