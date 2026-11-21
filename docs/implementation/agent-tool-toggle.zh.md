# 现有工具绑定总开关短路径

2026-11-21，基于 HEAD `c25cb6f` 及已有工作区改动，实施[上下文简化分析](../AGENT-CONTEXT-SIMPLIFICATION-ANALYSIS.zh.md)的 **S3a**。范围是当前成员的飞书／GitLab 现有工具绑定总开关。

## 用法与范围

明确的人类请求已给出准确账号时，直接提交：

```sh
"$TEAMX_CLI" platforms member tool --platform-id ID --source-seq ORIGINAL_SEQ --enabled true
"$TEAMX_CLI" platforms member tool --platform-id ID --source-seq ORIGINAL_SEQ --enabled false
```

无需先 get/status、填写版本或生成 operation ID。账号不明确时仍从 `platforms list` 消歧；只操作当前 Room/Agent/Membership。工具配置不授予业务写入授权，也不修改通知订阅、来源、过滤器或 Workspace。

短路径只恢复保存的准确绑定与 grants，**不采用默认 action，不随当前能力集合扩大授权**。首次绑定、缺少旧授权或旧绑定无法确认账号代际时拒绝短路径，并指向完整设置／修复流程。飞书用户 OAuth 缺口沿原合同返回 `not_applied / user_authorization_required`，不写绑定、不预约配置回执；修复仍针对同一连接。失败恢复不授权补入默认能力或新增 action。

初次设置、明确新增 action 或基于已观察版本的编辑仍使用完整命令：

```sh
"$TEAMX_CLI" platforms member tool --platform-id ID --platform-version V --operation-id STABLE_ID --source-seq ORIGINAL_SEQ --enabled true --binding-revision N --action message.reply
```

CLI 中显式出现 platform-version、operation-id、binding-revision 或 action 就选择完整模式，必须同时提供 platform-version 和 operation-id；原 binding-revision 冲突语义保留。完整飞书 enable 省略 action 仍采用原默认授权合同，不能把它当作短路径恢复失败后的自动替代。

## 实现与恢复

- CLI 只发送 platform_id/enabled、原 source_seq 和 `observe_current=true`。HTTP 严格检查字段是否出现，包括显式零、空数组和 null；携带版本、action、订阅等字段的混合短请求被拒绝。
- 原 `configurePlatformMember` 准入与锁内读取真实人类来源、核验当前成员，观察准确账号及目标绑定 revision；从启用／停用绑定中恢复匹配成员、平台类型、连接和账号代际的保存意图。飞书要求保存的 action authorization 存在。
- 稳定 ID 由 Room/Agent/Membership、准确连接、tool 种类及原 source_seq 派生，开关值与观察版本不参与。同来源相同意图恢复原回执，反向开关冲突；后续真实请求用其新 seq。通知操作 ID 的既有派生结果保留。
- 飞书 enable 将保存的授权显式交给原 grant plan、权限检查和能力准备，避免省略 action 时加入默认能力。远端读取后仍复核账号、凭据、成员及绑定 revision；同目标并发编辑冲突，其他目标可以独立提交。
- 短路径 disable 在原停用绑定集合保留账号代际和授权意图，清除能力证明；这些休眠 grants 不参与工具可用性。重新启用通过原能力准备链检查，证明不能从停用状态自动继承。完整模式的原行为保留，不迁移历史停用记录。
- 复用原 schema 3 配置回执、原 tool-bindings 存储、原 Calls 取消和等待。写入前保存 unknown 屏障；最终回执写入失败时，原来源的重试／status／服务重启均只读原 unknown，不重放操作。

回执描述该次操作，不代表当前设置。正常 completed 无需额外 get/status/acknowledge；中断或 unknown 用原 `member status --source-seq ORIGINAL_SEQ` 和准确成员设置核对。超时加一次未找到不能证明未提交。冲突不能自动刷新观察值，也不能换来源、ID、成员或账号绕过。后续人类纠正／撤销始终优先。

## 验证

[工具开关行为测试](../../internal/room/platform_member_tool_toggle_test.go)覆盖：

- 飞书／GitLab 的保留、关闭、重复关闭、恢复和重复启用；CLI 每次恰好一次 `/v1/platforms/member/tool` 业务请求，短请求不带版本／operation ID。原完整命令至少 5 个业务参数降为 3 个，并去掉为了填参而执行的 get；身份解析不计作业务配置调用。
- 飞书原先只有 message.reply 时，关闭／恢复仍只有该授权，不增加 send/self；操作不创建通知订阅。
- 同来源重试回执不变、反向开关冲突；旧未提交来源被较新意图挡住，历史回执在后续变更后仍可读取。
- 首次绑定、缺授权／错误账号代际、用户 OAuth 缺口、混合 CLI／HTTP 字段被正确拒绝或返回明确修复结果，不提前写配置。
- 不同成员、相同原生账号下的不同连接、Agent 文本伪造人类来源和被替换 Membership 的边界。
- 同目标／账号在远端准备期间变更被拒绝，其他成员绑定的并发编辑不被覆盖；撤权先取消在途调用，并在真实结清后返回。
- 最终回执保存失败后重开 manager/Store，原短命令与 status 均返回 unknown，权威绑定不再变化。

[字段与 ID 检查](../../internal/room/platform_connection_rules_test.go)补充通知旧 ID 固定样本兼容和工具／通知 ID 分域检查。7 个新增工具行为用例及字段、ID、指导回归的定向 race 检查通过（14.949 秒）。随后包含原成员通知／工具集成、完整编辑／历史 unknown／并发与帮助指导的定向 `integration -race` 回归通过（63.514 秒）；工具与平台组件测试、命令入口编译检查通过。

最终回归命令：

```sh
go test -tags=integration -race ./internal/room -run '^Test(PlatformMember|LarkToolConfiguration|ToolIndependent|ToolPrepared|ToolBindingsLegacy|ToolContracts|ToolSaveAttempt|LarkPermissionGap|OnboardingTool|ToolIntent|ToolPreparation|ToolLegacy|ExplicitToolBinding|ScopedHelp|AgentCommandHelp|TaskGuidance|PlatformSkillTopic|SharedGuidance)' -count=1 -timeout=4m
go test ./internal/platform/tool ./internal/app/platforms ./cmd/teamx -count=1
```

`git diff --check` 通过；通知 ID 的固定样本检查保护已有回执的恢复兼容性。

这些是生产 CLI/HTTP 链路和隔离平台进程夹具证据；没有启动真实模型、访问真实飞书／GitLab 或执行外部写入，不声称真实模型 token／耗时下降。S3b、policy 组合、MCP 及生命周期重构不在本单元范围。

## 采用

服务端与 CLI／嵌入 Skill 随新构建一起采用；未改运行中的服务或既有 Session。完整平台 Skill 和 member/tool 帮助已说明两种模式及恢复边界，不新增 topic 或常驻流程。未提交、未 push。
