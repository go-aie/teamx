# Agent 来源 CLI 合同可达性

日期：2026-11-21。对应[上下文简化方案 S0](../AGENT-CONTEXT-SIMPLIFICATION-ANALYSIS.zh.md#92-可独立交付的工作单元)，已完成。基准为 `9a7ed3de5732a4a53eafd58c3894f117ff8e886d` 加既有工作树；原有测试与文档调整保留。

`matters sources` 现在接受 `--cursor`，将返回的 `next_cursor` 直接交给既有服务端查询；`matters ensure` 现在接受 `--source`，将实际 `source_ref` 交给既有讨论事项创建/复用路径。服务端请求、来源存储、解析、消费及身份校验算法未改动。

## 使用与兼容

```sh
teamx matters sources --limit 25
teamx matters sources --cursor RETURNED_CURSOR --limit 25
teamx matters source --source SRC
teamx matters resolve --source SRC --turn TURN
teamx matters ensure --source SRC --target TARGET_REF --turn TURN
```

使用实际返回的引用、cursor 和当前回合；带 `--object` 的分页在续页时保持相同条件。`source` 只读保存证据，`resolve` 才显式重试解析；`ensure` 仅在需要独立讨论办理时创建，返回 `requires_read` 后用 `matters read` 恢复业务视图。

`sources/source/resolve/ensure` 的帮助已同步；`matters read` 帮助明确新 `sources.next_cursor` 与历史 `source_page.next_after` 的区别。历史来源继续使用 `sources --after`、`source/resolve --seq SEQ --source SRC`、`ensure --seq SEQ --target REF --turn TURN`。新输入仍拒绝非零 `--after`，无需猜造旧序号。

## 验证

新增 [followups_sources_cli_test.go](../../internal/room/followups_sources_cli_test.go) 经真实 CLI 参数解析、HTTP 服务准入和原 Store 业务路径验证：

- 27 条正式交付的新输入来源按默认页大小 25 完整续页，无遗漏或重复；带 object 条件的 cursor 可续读，改变或丢弃条件则拒绝。
- 实际 `source_ref` 能读取原文、显式重试解析、创建并复用讨论事项；再次读取仍为准确讨论目标，无对象评论回退。
- 非法 cursor/source、跨成员的 cursor/source/resolve/ensure、不匹配 target、过期回合与新输入的旧 offset 均拒绝。
- 来源操作前后正式输入执行状态完全一致；另一个流保留未读。两成员夹具显式选择仅 GitLab 的 catalog，并传入生产 Store/CLI 构造，不补入飞书等默认能力。
- 历史来源通过 CLI 使用实际 `next_after` 完整续页，按旧 seq 读取、解析和创建讨论仍可用。

相关 integration＋race 回归 **31 个顶层用例、含子项 91 项通过，零跳过、零竞态报告**。覆盖新增场景、原 ReferenceEntry/ReferenceFollowups/PlatformRead、帮助及所选/空 catalog 检查：

```sh
go test -race -tags=integration -timeout=3m ./internal/room \
  -run '^(TestMattersCLI|TestReferenceEntry|TestReferenceFollowups|TestPlatformRead|TestInputTools|TestAgentCommandHelp|TestAgentPolicyHelp|TestSelectedCatalog|TestEmptyCatalog)' \
  -count=1
```

`go build -o /tmp/teamx-context-s0 ./cmd/teamx` 通过；所构建二进制的 `matters sources/ensure --help` 输出正确；差异空白检查通过。

S0 到此停止。帮助视图随后由 [S1a-A1](agent-help-views.zh.md) 单独交付；公共指南、Skill 分主题加载、别名清理等仍待实施。本次 S0 未运行全仓测试、真实模型、浏览器或外部平台写入，也未替换日常服务或现有 Session。
