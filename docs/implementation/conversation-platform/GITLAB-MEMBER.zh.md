# 对话式 GitLab 成员配置实施记录

日期：2026-11-11。对应方案 CG2。已实现当前成员的 GitLab 个人待办通知和独立工具开关。全量本地验证及真实默认个人助理的已有账号复用链路已通过；新 OAuth 后自动接续的真实链路仍等待人工授权。CG3 的账号／项目选择、项目活动配置和 CG4 的首次应用配置回流不在本阶段范围内。

## 命令与回执

沿用 `platforms list/connect/member/skill`，不依赖已有平台工具绑定或 Workspace 项目：

```sh
teamx platforms member get --platform-id ID --platform-version V
teamx platforms member notifications --platform-id ID --platform-version V \
  --notification-version N --operation-id STEP_ID --source-seq SEQ --todos-enabled true
teamx platforms member tool --platform-id ID --platform-version V \
  --tool-revision N --operation-id OTHER_STEP_ID --source-seq SEQ --enabled true
teamx platforms member status --operation-id STEP_ID
teamx platforms member status --source-seq SEQ
```

`--todos-enabled false` 只关闭待办来源；工具 `--enabled false` 独立关闭指定连接对当前成员的绑定。GitLab 通知不接受飞书群参数或总开关 `--enabled`，平台类型从已核验连接读取。通知与工具各用稳定操作 ID，各自核对结果。只连接不创建后续配置。

`get` 返回完整 Room／Agent／Membership、原生账号、连接版本、通知版本、工具修订及可用性。`gitlab_notifications` 复用统一配置服务的配置、来源应用状态、pending/errors 和历史起点锁定信息；版本 0 且两个来源都没有订阅 ID 时只是首次草稿，不能作为已保存订阅的证据。成员配置回执的 `gitlab_notification_config` 保存该步配置，完成时包含配置服务固定的接收起点。既有飞书字段及持久化记录继续可读。

新增个人待办订阅覆盖该账号全部可访问项目，从保存时开始接收，项目活动关闭。已有配置保留项目活动、筛选、轮询间隔、接收起点、来源进度和其他接收目标；不根据默认值扩充旧配置。总开关关闭但项目活动来源仍被选中时，开启待办会返回冲突，要求在完整设置明确选择，不能顺带启用或关闭项目活动。已有未结清配置同样返回冲突。

工具启停复用原绑定与结清服务，重复开启保留既有授权；新绑定不授予回复、完成待办等外部写入能力。通知、工具及后续业务授权保持独立。

## 正确性与代码落点

- `platform_member.go`、`platform_member_gitlab.go`：按明确 provider 分派；保留当前成员、真实 human 消息依据、连接及配置版本检查。调用原 GitLab 统一通知服务，不直接创建另一份订阅权威状态。
- `platform_member_store.go`：扩展固定待办输入及 GitLab 配置回执。保持原操作 ID／原请求关联和持久化 `unknown` 提交栅栏；相同请求重查或晚到事件不会重放配置，换 ID 也不能绕过原请求保留的操作。
- GitLab 配置及来源写入：在原锁中检查成员与账号，期望配置版本在保存后推进到本次已接受版本；等待或远端读取后再检查，防止旧入参继续写入。部分应用、取消或回执保存失败保留可核对效果，不能报告整体成功。原 human HTTP 准入不放宽。
- CLI 帮助、接入 Skill 和 Agent 指南：补齐“连接／复用 → 自动接续 → 当前成员待办 → 核对回执”闭环；独立说明工具、版本冲突、未知结果和完整设置恢复。无新增 Inbox 类型、游标或调度分支。

## 本地验证

```sh
go test ./... -count=1 -timeout=8m
go test -race -tags=integration ./internal/room \
  -run '^Test(GitLabMember|DefaultAssistantGitLabConnect|PlatformMember|GitLabUnified|GitLabConnect|GitLabAuth|GitLabSetup|PlatformConnect|PlatformSelection|LarkSetup|LarkAuth)' \
  -count=1 -timeout=4m
node --test --test-reporter=dot internal/web/tests/web_*test.mjs
git diff --check
```

上述验证均通过：全量 Go 的 Room 包耗时 155.657 秒，integration＋race 专项耗时 70.885 秒，前端测试 391 项通过；Skill 入口修正后补跑入口／身份专项通过。

新增专项覆盖默认个人助理无工具／无项目的 CLI 配置、同请求回执复用、换 ID／改变输入、跨 provider 参数拒绝、成员替换、连接／通知／工具版本冲突、存储中断及重启不重放、通知部分成功与工具独立成功、重复工具开启保留授权、既有项目／待办筛选及起点／进度／其他目标保持、隐式启用项目活动冲突。另注入“期望配置已保存、账号版本变化”的边界，确认不再保存旧账号的来源配置。

默认个人助理的真实 Room／Inbox／成员调度与受控 backend 组合测试覆盖“原一条 human 消息 → OAuth 回调 → 普通 Inbox 自动接续 → 保存自身待办 → 确认消费”，无新增聊天、平台工具或 Workspace 项目依赖。此处 GitLab OAuth 和模型 backend 为测试适配器，不能代替下方真实证据。

## 默认个人助理真实小验收

验收条件：默认个人助理，初始无平台工具绑定、无 Workspace 项目绑定；只通过真实浏览器发送一条自然语言请求“连接指定 GitLab 账号并为你开启个人待办通知”。新账号人工授权后不补发聊天，助理应经 Inbox 自动接续；已有核验连接直接复用。核对自身完整身份、全部可访问项目的个人待办订阅、原操作／事件／消费关联／处理确认及成员配置回执，项目活动与工具保持关闭。

新 OAuth 隔离服务地址为 `http://127.0.0.1:18089/`；实例 `https://git-devops.opencsg.com`，账号 `RussellLuo`。数据与证据位于 `.local/acceptance/conversation-gitlab-member/`，不纳入 Git。新 OAuth 链路只复用原受保护应用配置，尚未带入既有连接或账号凭据；日常 8088 服务未修改。真实模型已发起连接并订阅结果，操作 `6ebb7342afcf4e90c58b8692b7cc6fb3` 当前等待人工授权；human 消息仍只有一条，未将此链路记为真实验收通过。

已有账号复用链路另用 18090 隔离服务，`real-reuse/` 只复制 CG1 已核验平台连接及其受保护凭据，没有复制 Room／工具／Workspace 配置，也不修改原数据。真实 Chromium 在新默认个人助理会话发出一条自然语言请求，模型查询指定实例与账号后直接使用现有连接，不发起新 OAuth，也不需要额外连接操作或连接结果确认。

通过事实：

- 账号 `RussellLuo`、原生用户 ID `13`，复用连接 `8d09ed31e88d888088a60b35e14ed57d`；当前 Room `e7532ccdbaafb6d3180b7ae342f107fe`、Agent `chief-of-staff`、Membership `ed09a54d52f7d2cc681da14dafd164ec`。
- 成员配置操作 `a98a25f9fb3741c49aed3892077cbf38` 为 `completed/result_version=1/result.status=applied`；统一配置版本 1，个人待办订阅 `3eec6034822516a3787cb96afb11a1eb`，接收起点为 `2026-11-11 12:58:20 +08:00`。配置和来源快照的完整目标一致；筛选覆盖当前支持的全部个人待办类型／原因，无项目限制。
- 模型再次读取 `member get` 并核对回执；待办来源已开始真实只读扫描，配置无 pending/errors。human 消息只有一条，模型第一回合完成；这条同步复用链路不产生连接结果事件或消费回执，不能将它当作新 OAuth 自动接续的证据。
- 项目活动关闭，工具未绑定、工具修订 0，Workspace 没有 repository/binding。真实 Chromium 核对通知开启／工具关闭、刷新、完整设置、390px 窄屏和脚本错误检查通过。

`real-reuse/verification.json` 为 `passed=true` 的无凭据核对报告；截图为同目录 `completed.png`、`settings.png`、`mobile.png`。新 OAuth 与已有连接复用的证据分别记录，不以复用通过替代人工授权后的自动接续验收。复用验收结束后已停止 18090 服务；18089 保留运行，供完成人工授权。

保存订阅不等于收到真实待办或触发提醒；本阶段不据此宣称真实待办提醒链路已验收。
