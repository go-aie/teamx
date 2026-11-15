# 工具绑定的配置意图与并发检查

2026-11-15。对应 [配置变更意图与并发协调方案](../CONFIGURATION-INTENT-AND-CONCURRENCY-PLAN.zh.md)阶段一。只实施工具绑定合同及其共用准入、诊断和提交边界；通知观察保护与规则记录级改造属于阶段二，跨领域真实 DSH／平台组合验收属于阶段三。

## 实现与合同

- `tool-bindings.json` 实际变更时写 schema 2，保留全局 `revision`，新增规范 JSON 四元组键的 `target_revisions`。键为 Agent／Membership／Tool／Platform，Room 及实例由文件归属确定。禁用和遗留目标标记参与 3200 个目标上限；原 1 MiB 文件上限保留。标记不回收、不截断，溢出明确失败。确定无变化不写入、不递增、不取消调用。
- schema 1 读取时在内存中以原全局修订补齐已存在项；读取不迁移，首次实际变更升级，保留 `LegacyUnknown`。成员回执 schema 1／2 及 onboarding schema 1／2／3 继续按历史证据读取，旧 unknown 不执行。
- Agent 普通启停及飞书动作增补省略工具版本即可表达绝对意图。`member get.binding_revision` 和可选 `--binding-revision N`／`expected_binding_revision` 保护已读绑定；显式 0 表示此前从未配置。human `/v1/tools/bind` 必须提供准确绑定前提，设置页从 `target_revisions` 取值。旧 `--tool-revision`、HTTP `input.tool_revision` 和 bind 的 `revision` 写合同拒绝，包括显式 0／null。
- 飞书准备冻结准确绑定标记、账号和动作计划；远程核验及可取消调用机制保留，回来只复查准确目标及真实账号／成员／凭据／能力有效期。权限缺口分支执行同样的目标复查。提交加载最新工具文件、只替换本目标，授权与能力回执使用实际提交版本；preserved 读取当前完整文件。
- 纯禁用及授权减少保持及时取消本目标活动调用；未授权发送、本人关系核验、能力证据和调用结算的既有保护保留。
- 新工具成员回执用 schema 3，业务身份只包含明确请求和可选观察前提；动作集合排序去重，默认计划与明确动作集合区分。内部捕获版本不进入请求身份。原 ID 查询优先，同来源换 ID 及已登记较新同目标配置阻止旧来源执行；诊断提供原操作 ID。保存前可证明未应用时报告 `outcome=not_applied`，进入权威保存后的错误继续按 unknown 防重放。
- 新 onboarding 用 schema 4，冻结 `before_binding_revision`；无关成员／工具的保存不阻塞。工具步骤在尝试保存前写 unknown 栅栏，不根据全局 before+1 或内容相同推断成功。旧待处理工具步骤缺少准确前提时返回 `binding_observation_required`，需要人工核对；不转换旧步骤后执行。
- 设置页冲突保留本次开关选择，展示未保存反馈，读回后不自动重发。账号恢复提示不会遮住本次工具操作结果。普通界面不展示版本、不增加确认步骤。帮助、平台 Skill 和既有调用示例同步更新。

命令示例：

```sh
teamx platforms member tool --platform-id ID --platform-version V \
  --operation-id TOOL_STEP_ID --source-seq SEQ --enabled true

# 需要保护先前观察时，使用 member get 返回的准确 binding_revision。
teamx platforms member tool --platform-id ID --platform-version V \
  --binding-revision N --operation-id TOOL_STEP_ID --source-seq SEQ --enabled false
```

同一原消息开启两个平台时分别使用稳定操作 ID。`result.version`／能力回执记录实际文件提交或 preserved 读回版本；后续编辑使用准确 `binding_revision`，不能将文件版本替代绑定前提。请求超时后先结合结算、原 status 和权威配置核对，不能根据一次未找到记录就换 ID 重发。

## 验证

普通全仓 Go 回归、工具／绑定／onboarding／成员配置 integration 回归、前端 614 项测试通过；准确目标、真实 CLI、旧回执、调用结算及权限撤回专项通过 race。`e2e` 标签编译通过，仅验证编译，没有执行真实 DSH／平台验收。新增 `tools_target_concurrency_test.go`、`tools_target_cli_integration_test.go` 与原工具测试共同覆盖：

- 同一真实 human 来源的飞书／GitLab 顺序、逆序和重叠准备；绝对意图及两个准确零值观察均不受无关保存阻塞，保留两项实际绑定。
- 两成员独立、同目标变化及 ABA、账号／凭据／generation／成员替换、权限缺口窗口、能力过期、纯禁用及授权减少的取消与结算。
- schema 1 只读兼容和变更升级、保留目标容量、文件字节上限、版本溢出、动作集合规范化、显式观察身份、原来源及较新来源限制。
- 真实 CLI 子进程通过 HTTP 操作本地模拟提供方，同 ID 复用／不同输入拒绝、提交前拒绝、保存尝试失败、HTTP 响应丢失、最终回执丢失和重启；旧 schema 1／2 unknown 只读恢复。
- 工具保存成功而调用结算中断时，unknown 回执仍保留实际授权／能力版本；不会因为等待失败而写出不一致投影或重新提交。

主要复验命令：

```sh
go test ./...
node --test internal/web/tests/*_test.mjs
go test -tags integration ./internal/room \
  -run 'Test.*(Tool|Binding|Onboarding|PlatformMember|GitLabMember)' -count=1
go test -race -tags integration ./internal/room \
  -run '^TestLarkSelfSendRevocationRaces$|^TestTool|^TestLarkToolConfiguration|^TestLarkPermissionGap|^TestOnboardingTool|^TestOnboardingFrozenSettings' -count=1
TEAMX_CONNECT_BROWSER=1 NODE_PATH="$PWD/packaging/verifier/node_modules" \
  go test -tags integration ./internal/room \
  -run '^TestLarkAuthorizationAndMemberRepairBrowser$' -count=1 -v
git diff --check
```

真实 Chromium 的桌面与 390px 手机验证通过，使用隔离的模拟官方 CLI 和生产 HTTP／前端：页面读到绑定版本后，另一 human 写入改变该绑定，页面旧请求获得实际 409；草稿和反馈保留，无自动重发，后续明确操作使用新的准确观察。截图位于 `.local/acceptance/configuration-intent-tools/`，包括各端 `binding-conflict` 和 `member-repaired`。没有发送真实平台消息或触发真实 OAuth。

这些证据属于真实 CLI／浏览器加模拟提供方。本阶段未修改日常运行配置、未重启已有服务、未进行真实 DSH 或真实平台组合验收，不据此宣布整个方案可发布。

## 部署边界

部署时同时更新二进制、前端和指引，并先停止旧进程、备份工具文件及操作记录。写入 schema 2／成员工具 schema 3／onboarding schema 4 后使用支持新格式的版本；不得只还原工具文件或删除 unknown 记录来回退。旧待处理工具步骤需要核对后以新请求开始，历史已完成／未知回执继续保留。完整发布仍须完成阶段二、三。
