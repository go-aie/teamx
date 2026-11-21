# 飞书保存目标读取入口收敛

2026-11-21，基于 HEAD `ce5c4e3` 及已有工作区改动，实施[上下文简化分析](../AGENT-CONTEXT-SIMPLIFICATION-ANALYSIS.zh.md)的 **S1c-C1**。

## 结论与用法

删除根 `teamx lark read` 别名、Room 中的独立参数解析文件及根帮助推荐。保存目标的远端读取统一从原账号的准确 ENTRY 办理：

```sh
"$TEAMX_CLI" tools run ENTRY read --object OBJECT_REF --target TARGET_REF
"$TEAMX_CLI" tools run ENTRY read --object OBJECT_REF --target TARGET_REF --scope context
"$TEAMX_CLI" tools run ENTRY read --object OBJECT_REF --target TARGET_REF --scope context --cursor NEXT
```

复用已知 ENTRY；未知时从 `tools list` 取得，不使用 provider 别名或在失败后换账号。默认 scope 为 content，保留旧调用可选的 `--json`。上下文仍为前后各 5 分钟、每页最多 25 条；存在 `next` 才续页，并保持同一 ENTRY、object、target 和 scope。不完整但无 `next` 表示限制或失败，不能无限重读同页；附件仍未读取。

## 删除条件与实现边界

原根入口调用 `/v1/platform/lark/target` → `readLark` → `readLarkTarget`；保留入口通过 `/v1/tools/run` → 所选 `platformtools.Runtime` → `Host.Read` 进入同一目标加载、账号复核和 provider 读取。准确 ENTRY 额外约束原连接，不能覆盖保存目标的来源账号。

删除前，在仍含根入口的工作树运行同一隔离夹具：正文、上下文 25＋5 续页、消息不可用及正文截断的两条 CLI 返回逐项一致，原账号重新核验后仍可读，旧访问版本游标被拒绝；`TestLarkSavedTargetExactEntryRead` 通过（11.631 秒）。条件成立，清理收益是少维护一套 Room 参数解析、根分发及可见入口，不把 A2 已完成的推荐路径统一重复计为收益，也不据此宣称模型调用数或 token 下降。

平台解析/帮助继续归 [lark/tools.go](../../internal/app/builtinplatforms/lark/tools.go)，分页包装继续归 [lark/read.go](../../internal/app/builtinplatforms/lark/read.go)。[原 Room Host](../../internal/room/tools_builtin.go)、保存目标/来源账号恢复、原 Calls 的取消和末帧后结清均保留；三个 bounded read HTTP 接口也保留。未建立新读取或动作账本，未改变持久格式。

同步迁移通知/跟进中的恢复提示、当前通知手册、集成测试调用，以及真实通知/DSH 验收夹具的准确入口读取。Skill 原本已使用 ENTRY 替换机制，本次补齐准确续页条件；历史实施/方案中的旧入口描述保留为历史。底层错误字符串 `lark read: CODE` 是诊断标签，仍保留原错误分类，不是可执行入口。

## 验证

删除后的[保存目标集成测试](../../internal/room/platform_lark_target_entry_integration_test.go)对照保留 HTTP 接口与生产 CLI/Runtime/Host 链路，并检查：

- 既有通知目标无需重新 resolve；默认正文、显式 JSON、上下文续页、消息不存在和正文截断。
- 无绑定、其他成员、旧 Membership、人类调用、provider 别名和错误 ENTRY 均拒绝；即使两个连接为同一原生账号，也不能越过原连接。
- 原 profile 凭据版本失效时拒绝读取；同账号重新核验后原绑定/目标可恢复，旧游标失效。
- 原生账号切换返回 `identity_mismatch` 且不泄露候选正文；manager/Store 重开后仍可读取原目标。
- 错误 scope/游标被拒绝，成功和失败读取没有遗留在途 Call。

定向 `integration -race` 的 8 个顶层用例通过（25.192 秒），涵盖上述读取及既有多账号、成员边界、凭据暂停恢复、历史账号恢复、所选 Runtime、慢输出撤销结清。随后补强实际凭据版本失效/恢复，并与根帮助、旧命令拒绝一起运行，3 个顶层 race 用例通过（19.894 秒）。根帮助在选中 Lark 的人类及成员视图中都不再列出旧别名；旧命令及其 help 均在服务请求前拒绝。

完整 `app/builtinplatforms/lark`、`platform/lark`、`platform/tool`、`app/platforms` 组件测试通过；`app/platformtools` 编译通过，无独立测试。Room 的帮助/Skill/指导定向回归通过。`go test ./cmd/teamx ./boot ./builtin/platforms/...` 通过，`go test -tags=e2e ./internal/room -run '^$'` 编译通过（仅编译，未运行真实验收）。

核心复验命令：

```sh
go test ./internal/app/builtinplatforms/lark ./internal/platform/lark ./internal/platform/tool ./internal/app/platformtools ./internal/app/platforms -count=1 -timeout=3m
go test -tags=integration -race ./internal/room -run '^Test(LarkSavedTargetExactEntryRead|LarkNotificationsInboxReferencesAndRoomQuestion|PlatformEntriesLarkIndependentProfilesAndBuiltinDispatch|PlatformEntryBuiltinSlowReaderRevocationSettles|PlatformEntryUsesSelectedRuntimeAndHelpPreservesLazyAccess|LarkPlatformAndMembershipReadBoundary|LarkVerificationResumesCredentialPausedSubscription|LarkHistoricalConversationUsesCurrentCredentialForSameAccount|RetiredLarkCLIRejectsWithoutServiceAccess|RootHelpAudienceAndSelection)$' -count=1 -timeout=4m
```

本单元没有启动真实 DSH/模型、访问真实飞书、执行外部写入或重跑完整 Room 测试包；上述结果是生产 CLI/HTTP 和隔离平台进程夹具的行为证据。C2 嵌套 actions、后端组合及其专项验收不在本次范围。

## 采用

构建后旧根命令不再可用，外部脚本需按上面的 ENTRY 形式迁移。没有改日常服务或现有 Session；Skill 资源随构建生效，旧 Session 若保留旧命令记忆，沿既有结清/配置检查与受控轮换流程采用，不手改生成指令或指纹。未提交、未 push。
