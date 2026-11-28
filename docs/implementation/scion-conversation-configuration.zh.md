# Scion 工具／通知对话配置

日期：2026-11-27。对应 [Scion 计划 N1b](../SCION-ISSUE-REVIEW-NOTIFICATION-PLAN.zh.md#n1b工具通知的对话式配置)，继续沿用 N1 的 **open Issue 新建通知**范围。

## 行为与边界

一句“给你开启 Scion 工具，并关注 OpenCSG/scion 从今天开始的新 Issue”可以完成两项本地配置。已有准确连接直接复用；有多个候选时使用原账号选择卡片，返回真实 PlatformID／版本及成员身份。没有可用连接时引导已有 Scion 登录页面；登录后重新读真实连接，PAT 不进入 Room 或配置 JSON。选择本身不开启工具或通知。

工具继续使用原绑定与取消结清服务；通知继续使用 `configureScionSubscription`，与 N1 页面读取、修改同一订阅。两项配置复用原 human SourceSeq，各有稳定操作 ID 和准确结果；一项失败时保留另一项实际结果，不增加跨配置事务。新 Scion 回执使用 schema 4，原合法 schema 1／2／3 回执继续读取，不做迁移或自动启用。

`member get` 返回 `scion_notifications`、准确订阅 ID／版本、冻结起点及工具状态。通知初次配置使用独立 `scion` patch；CLI 支持 `--scion-repository`、`--scion-object-type issue`、`--scion-backfill-start RFC3339` 或 `--scion-backfill-seconds`、`--scion-interval-seconds`。开始时间在操作接纳时冻结。首次纯开启缺少仓库明确返回“请选择仓库”；未配置时关闭保留无订阅状态。后续开关保留范围、起点、进度和原回执；间隔修改使用完整观察版本模式。

Scion、GitLab、飞书配置字段互斥。远端仓库读取之后和最终保存前重查原成员、账号／凭据、订阅身份和版本。换号不能继承旧订阅；同账号凭据更新仍须刷新绑定。原 ID 的 unknown 回执重复请求只返回原结果，status 不重新 apply。

未开放 Review、评论／revision 动态、类型编辑、仓库替换、起点重置、共享 Inbox、全程对话登录、登录后后台自动续配或 Linux 多用户 Scion。本次未部署，也未给正式服务启用采集。

## 验证

组件／集成覆盖 CLI 初次工具＋通知、同一人类消息两项独立配置、页面／对话一致、暂停／恢复、间隔修改保留起点、工具关闭保留通知、缺仓库和范围锁定、unknown／应答丢失防重及重启回执读取、成员版本围栏、字段互斥和只选择不配置。GitLab／飞书原配置和选择路径一起回归。

```sh
make test-race TEST_PACKAGES=./internal/room \
  TEST_RUN='TestScionMember|TestScionNotification|TestPlatformMember|TestPlatformSelection|TestGitLabMember'
make test TEST_PACKAGES='./internal/platform/scion ./internal/app/builtinplatforms/scion ./internal/app/platforms ./internal/platform/tool'
node --test internal/web/tests/web_platform_selection_test.mjs \
  internal/web/tests/web_member_platforms_test.mjs \
  internal/web/tests/web_platform_connect_test.mjs \
  internal/web/tests/web_scion_notifications_test.mjs \
  internal/web/tests/web_platform_onboarding_test.mjs
```

定向 race 与平台包回归通过，前端 142 项通过。未重新宣称 Room 整包全绿；N1 记录中的既有整包失败不在本阶段修复范围。

真实验收另用 `TestScionMemberRealConversation`：安装的 DSH／当前配置模型、正常 Agent 指引和 CLI、真实 Chromium、隔离凭据副本、真实原生只读 Scion 查询和成员输入消费。读保护包装器只转发 version／whoami／repo info／issue ls，其他原生命令拒绝。真实验收已通过：当前配置的 `deepseek-v4.1-flash-1017` 经安装的 DSH，先在没有连接时给出登录引导；Chromium 打开既有 PAT 表单，随后测试装配隔离的已验证凭据副本。模型创建 Scion 账号选择卡片，浏览器按准确连接提交，模型用同一 human SourceSeq 分别完成工具与通知配置，并实际执行只读原生仓库查询。两次独立成功运行中，后台 worker 分别交付 5 条／3 条查询时仍为 open 的真实 Issue，模型经正式输入工具读取并确认其中一条；读取结算依据持久化的 settled 页，不以模型答复代替。

页面独立核实保存的同一仓库、工具／通知开关和冻结起点；账号卡片有桌面／手机截图，PAT 登录表单与配置页另有截图，页面错误为 0。私有证据目录为 `.local/scion-member-conversation-20261127-04/` 和 `.local/scion-member-conversation-20261127-05/`，包括模型会话、请求回执、原生命令记录、输入消费状态和截图。测试没有执行 PAT 提交登录、远端业务写入或正式服务配置；首次登录只验证引导与实际表单，不宣称完整对话式登录。最后一轮额外捕获工具 HTTP 响应，确认一次原生 JSON 读取成功且退出码为 0。两个运行使用不同时间的实时 open 集合，条数不作为同一快照比较。

```sh
TEAMX_SCION_MEMBER_REAL=1 \
TEAMX_SCION_MEMBER_OUTPUT=/absolute/fresh-output \
TEAMX_SETTINGS_CONFIG=/absolute/config.toml \
TEAMX_SCION_TEST_CREDENTIALS=/absolute/credentials-copy.json \
TEAMX_SCION_BIN=/absolute/scn \
go test -tags=e2e ./internal/room -run '^TestScionMemberRealConversation$' -count=1 -v -timeout=15m
```
