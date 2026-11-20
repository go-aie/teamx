# 配置组合验收与发布检查

2026-11-15。对应[配置变更意图与并发协调方案](../CONFIGURATION-INTENT-AND-CONCURRENCY-PLAN.zh.md)阶段三。新增组合验收与发布说明，没有新增配置功能、执行器或结果状态。既有工作区改动保持原样，没有提交、部署或重启日常服务。

## 组合验收

本地模拟提供方、浏览器和真实模型分别记录。所有测试使用新建数据目录、Room 和成员；模拟飞书 CLI 仅接受核验／读取，GitLab 使用本地测试账号。未启动真实通知采集器，未发送平台消息或评论。

| 证据 | 实际覆盖 |
| --- | --- |
| 生产 CLI＋模拟提供方 | `TestToolConfigurationNativeCLIIndependentPlatforms` 覆盖飞书先、GitLab 先及准备重叠；两个独立零值观察均成功，准确核对两次工具保存及实际回执投影。准备窗口中的同目标／ABA／账号／成员／权限变化继续运行原专项测试。 |
| 跨领域恢复 | 新增 `TestConfigurationReceiptsAndObservationsSurviveRestart`：同一成员经原生 Agent CLI、human HTTP 保存双工具、通知和两条规则，模拟工具最终回执写入失败。重建管理器／Room 服务后，三个领域的持久快照保持相同；已完成回执不变，unknown 的查询和同 ID 请求均不重放，恢复期间管理器写入次数为 0。此处是组件重建，不称为 OS 进程崩溃验收。 |
| 中断与部分应用 | 回归 CLI HTTP 响应丢失、目标保存尝试失败、工具／通知最终回执失败及重建用例；GitLab 来源应用失败保留实际接受的 desired version 和 Pending／Errors，原 ID 查询／重复请求不修复。断开点和保存次数由既有故障注入测试核对，没有用配置内容相似推断成功。 |
| Chromium＋模拟提供方 | 新增 `TestConfigurationObservationBrowser`，桌面 1440×1100 与手机视口 390×844 各自独立执行：两工具均从准确 0 观察开启；真实竞争写入新增群 B 后，旧 A 表单获得 HTTP 409，草稿保留；修改另一规则不阻塞本地规则保存，同规则修改获得 409、保留正文并禁止继续保存。核对请求次数，无自动重发、常规审批弹窗或横向溢出。另回归 `TestLarkAuthorizationAndMemberRepairBrowser` 的两端准确工具冲突、输入保留及显式恢复。 |
| 真实 DSH＋模拟提供方 | 新增显式 opt-in 的 `TestConfigurationIntentRealDSH`，使用当前配置的默认模型 `deepseek-v4.1-flash-1017`、安装的真实 DSH 和生产 CLI／HTTP／Store。在隔离成员上完成四个真实 human 回合：双工具分别保存、两条既有规则分别更新、使用准确订阅观察修改通知，以及通知保存后最终回执失败的 unknown 场景。独立检查实际绑定、记录版本／human 来源、通知范围／起点、回执、HTTP 调用次数及两次工具／两次规则保存；unknown 保留真实保存效果、不升级为 completed，模型不重新提交或换 ID。没有 `/v1/actions/execute` 或 `/v1/tools/run` 请求。 |

通知远程核验时 human 新增 B、普通采集进度与兄弟成员写入、订阅 ID 替换等继续由 `TestNotificationReplacementRechecksExactObservationAfterRemotePreparation` 覆盖。规则历史请求复用、撤回后不激活、旧 policy token 拒绝和建议原问题／正文检查继续回归，没有缩小动作授权边界。

本次浏览器证据位于 `.local/acceptance/configuration-combination/browser/` 和 `binding-browser/`，含桌面／手机截图、浏览器请求记录及 Go 独立读回的持久内容。真实模型报告在 `dsh-verified/report.json`，含四个回合的 ACP 审计、真实 human 消息、配置回执和保存次数。它们属于本机验收产物，不入库、不包含模型 Key；临时 Room 和模型会话在测试结束后关闭。

## 复验方法

普通回归与无外部依赖的集成用例：

```sh
go test ./...
node --test internal/web/tests/*_test.mjs
go test -tags integration ./internal/room -count=1 -timeout=8m \
  -run 'TestConfigurationReceipts|TestTool|TestBinding|TestLarkToolConfiguration|TestLarkPermissionGap|TestOnboarding|TestPlatformMember|TestGitLabMember|TestNotification|TestGitLabPartialApplication|TestPolicy|TestGitLabUnified|TestGitLabPlatforms|TestLarkMultiplePlatforms|TestReferenceConversationPolicy|TestReferenceActionRuleLifecycleAdmission|TestMemberNotification|TestTodosAdditionalAccount'
go test -race -tags integration ./internal/room -count=1 -timeout=8m \
  -run 'TestConfigurationReceipts|TestToolIndependent|TestToolPrepared|TestToolSaveAttempt|TestToolConfiguration|TestToolLegacyUnknown|TestLarkToolConfiguration|TestLarkPermissionGap|TestOnboardingTool|TestOnboardingFrozenSettings|TestPolicy(Independent|Legacy|Intent|Write)|TestNotification|TestGitLabPartialApplication|TestGitLabMemberAccountChangeAfterDesiredSave|TestReferenceConversationPolicy|TestReferenceActionRuleLifecycleAdmission'
go test -tags e2e ./internal/room -run '^$'
git diff --check
```

显式浏览器验收需要本地 Playwright／Chromium。`NODE_PATH` 指向已经安装 Playwright 的目录；截图输出目录可替换：

```sh
TEAMX_CONFIGURATION_BROWSER=1 \
TEAMX_CONFIGURATION_ARTIFACTS="$PWD/.local/acceptance/configuration-combination/browser" \
NODE_PATH="$PWD/packaging/verifier/node_modules" \
  go test -tags=e2e ./internal/room -run '^TestConfigurationObservationBrowser$' -count=1 -v

TEAMX_CONNECT_BROWSER=1 \
TEAMX_CONNECT_BROWSER_ARTIFACTS="$PWD/.local/acceptance/configuration-combination/binding-browser" \
NODE_PATH="$PWD/packaging/verifier/node_modules" \
  go test -tags=e2e ./internal/room -run '^TestLarkAuthorizationAndMemberRepairBrowser$' -count=1 -v
```

真实 DSH 用例只在 `TEAMX_CONFIGURATION_DSH=fixture` 时执行；其他值跳过，不隐式进入真实平台。读取指定配置中的默认模型及已有凭据，兼容旧 runtime.model；不会修改该配置或凭据存储。输出必须是尚不存在的绝对目录，父目录需存在，每次使用新名字：

```sh
TEAMX_CONFIGURATION_DSH=fixture \
TEAMX_CONFIGURATION_CONFIG="$PWD/.teamx/config.toml" \
TEAMX_DSH_OUTPUT="$PWD/.local/acceptance/configuration-combination/dsh-new-run" \
  go test -tags e2e ./internal/room -run '^TestConfigurationIntentRealDSH$' -count=1 -v -timeout=15m
```

本次普通 Go、前端 617 项、上述集成与 race、两组真实浏览器以及真实 DSH 用例通过。e2e 标签编译通过不等于执行全部真实外部平台测试。

## 兼容与发布边界

核对了当前 CLI 帮助、平台 Skill、Agent 指引、成员配置示例和通知手册：工具绝对意图不带全局版本，已读绑定编辑用准确 `binding_revision`；human 工具表单必须携带 `expected_binding_revision`；通知必须显式携带准确版本，包括合法的 0；规则仅携带记录版本，文件 revision 只读。旧工具／规则全局版本写合同包含显式 0／null 均拒绝；冲突提示不授予自动换前提或换 ID 重发的权限。

兼容回归保留 schema 1 工具文件的只读加载及首次实际变更升级，工具 schema 2、成员工具回执 schema 3、onboarding schema 4；通知回执和规则文件未为统一外观升级。历史成员／onboarding 回执继续按原证据读取，unknown 防重放；旧待处理工具步骤缺少准确前提时要求核对，不自动执行。schema、容量、溢出及原 human 来源限制沿用前两阶段测试。

正式部署时沿用[安装与运行](../installation.zh.md#备份与升级)的一致备份／升级流程：

1. 停止旧服务和所有旧写进程、结算活动调用并确认取得数据独占权；保存整个 TeamX home／data 的一致备份和旧发布闭包，包含工具、规则、通知进度、成员操作及 onboarding 记录。开发目录运行也须整体备份，不能只复制 `tool-bindings.json`。
2. 二进制、嵌入前端、帮助和 Skill 一起更新。新 schema 写入后使用支持它的版本；不得单独还原工具文件、删 unknown 记录或启动旧进程“修复”回执。
3. 启动后完成认证及准确 Room／成员读回检查，核对工具绑定、原操作状态、通知 desired 与 Pending／Errors、规则记录及有效策略。部分来源未应用使用既有显式重试应用入口；未知结果只查询并核对原请求结算，不重新提交意图。
4. 若需恢复，使用同一安装／账号／绝对路径的整体备份，保留新旧记录用于核对；不得把旧内容相等或全局 before+1 当作成功凭据。

这些是发布检查和操作约束；本次没有实际停服、升级、备份恢复或制作／发布发行包。

**尚未完成的门槛：**真实飞书／GitLab 配置验收仅限另行明确授权的隔离平台／账号／成员目标；本次没有这些目标，因此未执行。真实模型＋模拟提供方和本地浏览器＋模拟提供方均不能替代它。手机证据是 Chromium 手机视口，不是实体手机。阶段三验收代码和以上组合验证已完成，整体方案仍不能标记为全量发布验收通过；实际部署时再执行上述核对步骤。
