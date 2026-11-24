# 平台设置 Skill 按任务加载

2026-11-21，实施 [Agent 上下文简化分析](../AGENT-CONTEXT-SIMPLIFICATION-ANALYSIS.zh.md) 的 **S1b-B1**。

## 交付与兼容

`platforms skill` 新增两个固定 topic，每次返回该路径的完整流程及必要公共边界，不要求先读目录或完整指南：

```sh
"$TEAMX_CLI" platforms skill --topic notifications
"$TEAMX_CLI" platforms skill --topic selection
```

`notifications` 用于飞书/GitLab 纯通知总开关：核对真实人类请求、当前成员与准确账号，直接提交原 seq 和 enabled；包含首次默认、恢复已保存来源、无有效来源时的处理、历史回执与当前状态的区别，以及 unknown/部分应用恢复。完成的成员保存不需要 acknowledge 或额外 get。

`selection` 用于账号/群/项目卡片：原来源恢复、准确账号与版本、稳定 ID、`start --notify inbox` 后结束回合、组件搜索分页与真人提交，以及基于 `result_ref` 的接续。包含原人类意图与较新指令核对、card-only 不改订阅、精确 `result_version` 确认、已处理结果去重、取消/过期/unknown 和中断恢复。复杂后续配置明确回到完整指南，保留观察版本及独立操作回执。

省略 topic 仍返回完整平台设置 Skill，连接、复杂来源/过滤设置、工具启用与同账号修复继续可读。未知 topic 返回带 `topic` 和有效入口提示的 400；不把它解释为文件路径，也不回退到全文。CLI 只在 skill 注册 `--topic`，HTTP 对其他连接操作携带非空 topic 明确拒绝。

CLI → `/v1/platforms/skill` 仍校验当前成员身份，无需已有平台连接或业务工具绑定。Topic 不进入持久操作或配置模型，不改变设置执行、授权、版本、消费和结果确认语义。没有新增公共扩展 Skill API 或加载框架。

## 入口与资源

- [嵌入资源读取](../../internal/platform/tool/skills.go)：有限 topic，两个资源共享随返回附带的身份/授权边界；[完整指南](../../internal/platform/tool/skills/platforms/SKILL.md) 保留。
- [CLI](../../internal/room/platform_connect_cli.go)、[请求字段](../../internal/room/platform_connect_store.go)、[HTTP](../../internal/room/platform_connect.go)：通过原读取链传递 topic，保留现有身份门槛。
- [公共指南](../../internal/room/prompts/agent-guide.md)、[usage](../../internal/platform/tool/skills/usage/SKILL.md)、[平台帮助](../../internal/room/platform_cli_help.go)及[成员帮助](../../internal/room/platform_member_cli.go)：已知任务直接读取相应 topic；selection/connect 结果分别进入相应恢复路径。既有保存角色也从公共指南取得路由。

当前完整指南为 33,282 UTF-8 字节；含公共边界的通知 topic 为 4,466 字节、选择 topic 为 7,719 字节，单次对应资源读取分别减少约 **86.6% / 76.8%**。这只是资源返回量，不代表模型总 token、耗时或工具调用数按同比下降。

## 验证与证据

结构化结果、改前/改后样本及失败记录见脱敏证据（本地验收文件 `agent-platform-skill-topics-evidence.json`）。

无绑定 CLI/HTTP 检查覆盖默认完整指南和两个 topic、未知/路径形式/大小写错误、其他操作不接受 topic、伪造人类或旧 Membership 拒绝；读取不创建连接或绑定。离线帮助无需环境/服务；固定指南沿正常 `OpenSession → PrepareOpen → Launch.Instructions → 所选工厂` 检查默认与保存角色、三种输入合同的实际交付。

相关 `integration -race` 回归 **76 个顶层用例、含子项 172 项通过，零失败/跳过**。覆盖连接/授权、来源恢复、选择卡片、原结果确认/去重、成员总开关/复杂设置、unknown、重启、身份与独立版本。真实生产 CLI 构建也由隔离集成链使用；没有据此宣称全仓或全部平台验收通过。

真实模型采用配置中的 `deepseek-v4.1-flash-1017` 和产品默认助理角色，使用真实 DSH、CLI、HTTP、持久回执及 Chromium；平台元数据模拟，不启动采集器或写入远端业务。沿用 `TestPlatformNotificationSelectionRealDSH`，新增已知任务读取 topic 的断言及异步接续请求记录。

飞书改后完整 **8 回合通过**：首次通知开启、两次同一卡片的桌面/手机搜索分页提交及自动接续、准确 handled version、排他选群、通知关闭/再开启，以及受控 Session 轮换。通知和卡片均直接读取相应 topic，复杂设置读取完整指南；纯开关无 get/status 前置，card-only 展示与提交均不改订阅。

GitLab 改后补采完整 **7 回合通过**：首次通知开启、两次 Chromium 项目卡片提交及原结果确认、轮换后关闭/再开启；两条路径直接读取对应 topic，没有卡片导致的订阅变更。连同飞书，共有 15 个完整通过的真实模型回合、4 次模型创建卡片的真实浏览器提交及自动接续。

改前飞书样本覆盖相同路径，最终第二张卡片接续遇到 HTTP 429，GitLab 改前首轮也因 429 失败。改后 GitLab 初次采样在第二张卡片创建回合也遇到 429，间隔后的完整补采通过。失败产物保留，不将其记为完整通过，也不把限流解释为业务执行失败。调用数与资源读取量分别记录；改前复用全文、部分场景省略来源恢复，不能据此宣称改后总调用下降。

复验命令（输出目录必须全新，模型凭据沿现有安全配置读取）：

```sh
TEAMX_NOTIFICATION_SELECTION_REAL=1 \
TEAMX_SETTINGS_CONFIG="$PWD/.teamx/config.toml" \
TEAMX_DSH_OUTPUT=/absolute/fresh/output \
go test -tags=e2e ./internal/room \
  -run '^TestPlatformNotificationSelectionRealDSH$/lark/run1$' -count=1 -timeout=15m
```

GitLab 将子测试过滤换为 `/gitlab/run1$`。浏览器产物和含实际指令/请求的原始报告保存在私有临时目录；仓库只保存脱敏摘要。

## 生效与范围

Skill/topic 随构建嵌入，公共指南参与原 Session 配置指纹。正式采用仍应先结清原 Session，再按现有控制轮换和恢复；不直接改生成的 AGENTS.md，也不绕过兼容检查。本次只轮换隔离测试 Session，没有替换日常服务、触碰现有订阅或轮换用户 Session。

B2 GitLab 业务发送 topic、其余平台章节拆分、后端组合及 C1/C2 别名清理均未实施。没有提交或 push。
