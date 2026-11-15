# 通知观察保护与规则记录级并发

2026-11-15。对应[配置变更意图与并发协调方案](../CONFIGURATION-INTENT-AND-CONCURRENCY-PLAN.zh.md)阶段二。阶段一工具合同继续保留；阶段三的真实 DSH、桌面／手机浏览器及真实平台组合验收尚未执行。

## 实现与接口

规则的 Agent 和 human 写入都在原 `Store.mu` 下读取最新 history，只检查准确记录的 `version`；文件 `revision` 仍随追加递增，但不能再作为写入输入，包含显式 0／null 的旧字段也拒绝。CLI JSON、HTTP、设置页、帮助、Agent guide 和当前通知手册已同步。human 保存响应保留原管理视图，增加本次准确 `record` 和 `policy_token`；Agent 继续返回原提交记录及当前完整有效策略。动作的完整 policy token、建议的原版本／正文／公开问题回执／human 答复检查均保留。

新对话证据使用 `digest_version: 2`，请求摘要去掉文件 revision 和运行回合，规范化有效期时区，保留记录版本、真实 human 来源、正文、状态、basis 和建议关联。旧证据不修改、不重新计算持久摘要；复用时按原 history 位置重建旧请求，只有摘要完全匹配才返回历史记录。相同来源无法证明等价时，冲突响应提供原 `record` 和核对提示，不追加相似规则。原请求匹配先于目标版本检查，因此撤回后的原保存／确认重试不会重新激活规则。

通知继续使用原订阅／desired version、原权威保存函数和采集器。所有 Agent 类型化补丁都必须显式携带 `--notification-version N`／`input.notification_version`，0 是“观察时未配置”；缺少输入和 null 均拒绝。完整 human 表单也要求准确版本：飞书为 `subscription_version`，GitLab 为 `version`。这些前提不能在准备后自动更新。

成员配置的共用准入在远程准备后复查准确账号、generation、凭据指纹／版本、CLI profile、成员及订阅版本；飞书额外冻结订阅 ID，避免退休后新订阅恰好版本相同的 ABA。普通采集进度不进入配置版本检查，保存仍从最新状态保留进度；不同接收成员或平台独立。准确版本冲突返回 field、expected／actual 和恢复指引，保存尝试前能够证明无效果的拒绝报告 `outcome=not_applied`。规则保存尝试后继续遵守 Store 原不确定写入保护。

GitLab 已确认保存 desired configuration 后，即使账号变化或采集器结算失败，也保留本次实际接受的版本和配置。部分来源未应用时，成员回执仍使用既有 `unknown` 防重放，并保留 `result.version`、`source_apply_pending`；通过 `member get.gitlab_notifications` 查看当前 `Applying/Pending/Errors`。原 ID、status 和重复请求都不触发修复。修复使用完整设置已有的“重试应用”，以已保存配置及其观察版本应用缺少的来源，不重提成员原意图；无配置变化时 desired version 不递增。内部通知 context 只推进到本次确实接受的版本。

示例：

```sh
# N 必须来自准确成员／平台的 member get，未配置为显式 0。
teamx platforms member notifications --platform-id ID --platform-version V \
  --notification-version N --operation-id STEP_ID --source-seq SEQ \
  --group-scope specified --group-chat-id CHAT_A

# 两条独立规则可基于同一次读取中的各自版本分别更新。
# 不再提交文件 revision；同规则旧 version 必须核对，不能自动刷新。
teamx policy update --change '{"type":"save","id":"reply-style","version":1,"source_seq":24,"turn_number":2,"basis":"真实 human 的新增长期要求","body":{"text":"简短中文，并保留必要限制"}}'
```

## 验证与边界

本地验收使用隔离 Room、生产 CLI／HTTP／前端以及本地模拟提供方，未修改日常运行服务或真实平台配置。新增行为测试覆盖：

- Agent 与 human 从同一次观察出发保存／并发更新独立规则；同规则修改／撤回仍拒绝旧版本；旧 policy token 不能派发动作。
- 历史 digest 在其他规则更新、撤回、清空公开消息和重载后的原请求复用；不等价请求返回原证据且不追加。新摘要覆盖业务差异、跨回合及等价时区。
- CLI／Agent HTTP／human HTTP 缺少通知版本或 null 时拒绝，显式 0 可用；旧规则 revision 写合同拒绝。
- 远程群核验窗口中，human 增加 B 后，Agent 的旧 A 范围替换和另一份旧完整表单都拒绝；换订阅 ID 而版本相同也拒绝；普通采集进度与另一接收成员保存允许继续。
- GitLab 部分应用保留已接受版本，原回执／查询不修复；完整设置显式重试只应用来源，不增加 desired version；账号在 desired 保存后变化时不写入旧授权下的采集器。
- 设置页通过真实 HTTP 保存：另一规则更新不阻塞本地编辑，同规则冲突保留草稿；Node 前端回归覆盖通知草稿在后台刷新和切换成员／平台后仍使用原观察版本，拒绝旧范围覆盖且不自动重发。冲突时草稿保留，用户显式点击刷新才读取最新配置并重新编辑。原观察完全匹配的本地总开关操作可携带确认后的版本继续编辑；若开关使用的状态已有他人修改，不能借开关推进旧草稿的前提。

主要复验命令：

```sh
go test ./...
node --test internal/web/tests/*_test.mjs
go test -tags integration ./internal/room \
  -run 'TestNotification|TestGitLabPartialApplication|TestPolicy(Independent|Legacy|Intent|Write)|TestPolicyManagementHTTP|TestGitLabMemberAccountChangeAfterDesiredSave' -count=1
go test -tags integration ./internal/room \
  -run 'TestPolicyManagementHTTP|TestPlatformMember|TestGitLabMember|TestGitLabUnified|TestGitLabPlatforms|TestLarkMultiplePlatforms|TestTool|TestLarkToolConfiguration|TestOnboarding' -count=1
go test -race -tags integration ./internal/room \
  -run 'TestPolicy(Independent|Legacy|Intent|Write)|TestNotification|TestGitLabPartialApplication|TestGitLabMemberAccountChangeAfterDesiredSave|TestReferenceConversationPolicy|TestReferenceActionRuleLifecycleAdmission|TestTool|TestLarkToolConfiguration|TestLarkPermissionGap|TestOnboardingTool' -count=1
go test -tags integration ./internal/room \
  -run 'TestMemberNotification(GitLabMembersAndRoomHTTPIndependentDeliveryAndRestart|LarkMembersHTTPIndependentProgressAndSyncIdentity)|TestTodosAdditionalAccountKeepsIndependentProgress' -count=1
go test -tags e2e ./internal/room -run '^$'
git diff --check
```

上述 Go／integration／race 回归及前端 617 项测试通过；e2e 标签只验证编译，没有执行真实后端。设置页 HTTP 用例通过 Node DOM harness 驱动生产页面处理函数，不能替代真实浏览器验收。阶段三仍须完成跨领域真实 DSH、桌面／手机浏览器、隔离真实平台及部署检查，不能据本阶段结果宣布完整发布。
