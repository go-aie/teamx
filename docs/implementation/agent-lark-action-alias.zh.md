# 飞书嵌套动作别名评估

2026-11-21，基于 HEAD `ce5c4e3` 及当前工作区，按[上下文简化分析](../AGENT-CONTEXT-SIMPLIFICATION-ANALYSIS.zh.md)的 **S1c-C2** 比较根 actions 与飞书嵌套 actions。

## 结论

**本次按方案的保留分支完成评估，保留嵌套别名。** 两条入口使用同一动作合同、业务执行器及持久回执，但最终输出期间的在途 Call 所有权不同，尚不满足文档要求的“原 Calls、取消、末帧后关闭……不退化”。为删除一项可选兼容别名而扩展根动作输出生命周期，当前投入超出这项清理的收益。

普通指导继续推荐已经在 A2 采用的根入口，准确 ENTRY 必须匹配原账号：

```sh
"$TEAMX_CLI" actions resolve --tool ENTRY --request JSON
"$TEAMX_CLI" actions execute --tool ENTRY --request-file -
"$TEAMX_CLI" actions read --id ORIGINAL_ID
```

原 `tools run ENTRY actions resolve/execute`、嵌套帮助、CLI 的请求文件转换以及已有调用方均保留；不改推荐路径、原生 CLI 限制、公共 Host/Runtime、图片 lease、最终派发检查或动作账本。C1 已移除的根 `lark read` 不恢复。

## 对照结果

根 `/v1/actions/execute` 经 `Store.agentAction` → `executeReferenceAction` → 原业务执行器返回。飞书嵌套入口经 `/v1/tools/run` → 所选 Runtime → `builtinToolHost.Execute` 进入同一路径；两者都检查准确 ENTRY 与保存动作的账号，使用相同 DTO、投影和错误分类。

差异在外层输送：[原动作执行器](../../internal/room/followups_actions_service.go)的 `defer session.Close()` 在业务执行返回时结束平台会话；[根 HTTP handler](../../internal/room/action_execute_service.go)随后编码 JSON。[嵌套传输](../../internal/room/tools_builtin.go)另有 `platformtools.Prepare` 准入的外层 Call，其 `defer prepared.Close()` 在 stdout 与 exit 帧写入结束后执行，期间保留 `watchToolIO` 的取消/输出协作。

新增 [TestLarkActionReceiptOutputOwnership](../../internal/room/actions_output_integration_test.go)通过受控 writer 阻塞最终输出，不依赖 socket 缓冲或短暂等待窗口：

| 观察点 | 根 actions | 嵌套 actions |
| --- | --- | --- |
| 最终输出写入仍阻塞时的 `platformCalls.Len()` | 0 | 1 |
| 此时原 ID 的持久回执 | succeeded，Attempts=1，原账号 | 同一回执 |
| 放行最终输出后 | 正常结束，Call=0 | exit 帧结束，Call=0 |
| 两入口提交同一 ID 后的夹具发送总次数 | 两入口合计 1 次，完整回执相同 | 两入口合计 1 次，完整回执相同 |

这证明业务执行和回执恢复可共用，不能据此把两种输出生命周期视为等价。这里的缺口是删除别名的前置条件，不表示已有根 actions 会丢失持久回执或必然重复发送。测试继续约束嵌套末帧后结清和双入口幂等，没有把根入口当前的 Call=0 固化为必须维持的行为。

## 验证与范围

`integration -race` 首组 3 个顶层用例通过（36.869 秒）：新增最终输出/回执测试、`TestLarkActionAgentContract`、`TestPlatformEntryBuiltinActionExecutionRetainsReceiptAndAccount`。覆盖根/嵌套错误字段一致、stdin JSON、准确入口发现、同名群消歧及候选复核、完整群图文发送、原 ID 恢复、其他 ENTRY 拒绝，以及最终输出所有权差异。

第二组 5 个顶层 `integration -race` 用例通过（143.863 秒）：回复的四类授权、unknown/重启恢复、self/group 图文的 18 个成功/上传失败/unknown/回读失败场景、两种输入合同下的 10 个并发/取消/摘要/正文/策略派发检查，以及慢输出期间撤销绑定的结清。图片临时文件及 lease 的释放仍由原检查验证。两组合计 8 个顶层用例通过；这不等于两条入口所有删除门槛均已通过。

本次没有启动真实模型、访问真实飞书、执行外部写入或修改日常服务/Session；平台发送仅发生在隔离进程夹具。未重跑完整 Room 包或真实平台矩阵。

复验关键差异：

```sh
go test -tags=integration -race ./internal/room \
  -run '^Test(LarkActionReceiptOutputOwnership|LarkActionAgentContract|PlatformEntryBuiltinActionExecutionRetainsReceiptAndAccount)$' \
  -count=1 -timeout=3m -v
go test -tags=integration -race ./internal/room \
  -run '^Test(LarkReplyCommonCLIThreeAuthorities|LarkReplyUnknownMixedTargetsAndRestart|LarkImagesManagedCLI|LarkImagesDispatchGuards|PlatformEntryBuiltinSlowReaderRevocationSettles)$' \
  -count=1 -timeout=4m -v
```

## 以后何时重新评估

若后续在原所有者下统一根动作的输出期间 Call 所有权，应先验证慢输出、取消及关闭的结清边界，同时保持凭据不可用、能力撤销和 Session 恢复时的原 ID 只读查询。随后按原 C2 条件复核 self/group/reply、图文、unknown、同账号恢复、图片 lease/最终派发及全部调用方，才能删除嵌套解析、帮助和专属适配；不以复制新账本或重设 action API 换取表面等价。

本次仅新增行为测试与评估记录，未改生产实现，未提交、未 push。
