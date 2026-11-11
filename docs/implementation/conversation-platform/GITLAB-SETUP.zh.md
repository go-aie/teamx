# GitLab 首次应用配置回流

2026-11-11：CG4 已实现并通过本地服务、CLI、模拟 DOM 和真实 Chromium 组合验证。同日通过单账号真实首次应用／GitLab／模型 Agent 的“对话发起 → 人工创建应用／安全导入 → 所有者核验／授权 → 保存原连接 → 无新聊天自动开启个人待办”组合验收。

## 行为和边界

新 GitLab 连接可省略 `--application-id`。服务按原 human 消息和完整 Room／Agent／Membership 保存 `waiting/application` 操作；`start/status` 与原消息组件返回实例、官方个人应用管理链接、实际服务回调、必需 `api` scope 和现有应用兼容性。已有账号复用与明确指定应用仍遵守原合同，指定不兼容应用仍拒绝，不静默换目标。

原消息的“配置 GitLab 应用”打开现有完整设置表单，固定原实例、连接名称和 provider。用户在官方页面创建应用，在受保护表单中填写凭据或选择兼容应用。表单默认仅请求 `api`，不会因普通设置的默认值额外勾选 `write_repository`。提交以原操作 ID、当前版本和原 human 身份绑定应用引用，并使用原 Cookie／state／PKCE 授权服务。导入应用仍须经 GitLab 原生个人应用列表核验所有者、回调和 scopes；授权回调是唯一的应用／凭据／连接提交路径，保存至原连接目标，不经过旧 onboarding 的通知和工具默认启用逻辑。

原 Agent 的固定业务输入保持原样，human 后补的应用引用单独持久化；重复 start 可恢复同一操作，不能借改草稿、旧表单、多标签页或其他浏览器替换已绑定应用。关闭设置、返回列表或刷新不取消对话授权；显式取消沿用原结清与结果事件规则。未绑定应用的等待操作可跨服务重启继续；已创建临时 OAuth 会话的重启仍按原规则核对已保存效果，不恢复临时会话。

操作文件只保存公开引用和指纹，不额外持久化 Secret、Token、Cookie、state、PKCE、授权码、临时授权链接或公开配置投影。凭据继续由既有平台存储负责。通知、项目活动和工具仍为独立的成员配置操作，三种接续共享原结果及配置回执。

## 本地验证

- `go test ./... -count=1 -timeout=8m` 通过。
- `node --test --test-reporter=dot internal/web/tests/web_*test.mjs` 通过。
- `go test -race -tags=integration ./internal/room -run '^Test(GitLabConnect|GitLabAuth|GitLabSetup|GitLabMember|GitLabSelection|GitLabReuse|GitLabSharedApplication|PlatformConnect|PlatformSelection|PlatformMember|LarkAuth|LarkSetup)' -count=1 -timeout=5m` 通过。一次前序组合运行在既有 Session 轮换测试的消费确认处遇到 `ErrBusy`；原失败测试与新增组合测试的 race 单独复验通过，随后上述完整专项复跑通过，未改动调度或 Inbox 生产代码。
- `TEAMX_CONNECT_BROWSER=1 go test -tags=integration ./internal/room -run '^Test(GitLabConnectConversationBrowser|DefaultAssistantGitLabConnectConversationBrowser|GitLabConnectFirstApplicationConversationBrowser)$' -count=1 -v -timeout=90s` 通过。浏览器与 TeamX HTTP／独立 CLI 真实运行；远端 GitLab 使用模拟适配器。

新增服务组合覆盖：缺少应用的创建／纯读查询／恢复，安全导入与已有兼容应用选择，回调不匹配修正后继续同一操作，原生所有者／回调／scope 不符，其他实例或操作者、替换共享应用的导入、取消、旧表单、固定输入变化，以及首次连接后个人待办配置幂等。保存连接后操作回执中断，通过真正关闭并重开存储核对已采用凭据，不重放授权码、不撤销已采用 Token。测试不能在未知写入后的 fail-stop 存储上直接调用恢复；最初故障注入测试因此失败，已改为重开再核对。

飞书普通 Room、默认个人助理及 GitLab 项目选择的真实 Chromium 回归另行通过。首次飞书回归发现额外的 GitLab 管理链接影响旧链接定位，已将该链接限定到 GitLab 组件，复跑两条飞书浏览器链路通过；一次默认个人助理的目录读取暂态失败也在该次完整复跑中通过。

浏览器另行覆盖：公开配置提示与官方链接、固定实例表单、最小 scope、关闭后重新进入、安全导入后直接打开原操作授权、Secret 清空、授权期间关闭设置、回调后结果恢复及 390px 窄屏。模拟 DOM 另核对没有调用原设置的独立登录／取消或 onboarding 入口。原设置的添加、改名、应用编辑、重授权、核验、取消、撤销、共享影响和飞书行为由既有服务／前端回归分别验证。

本地截图与真实验收证据保存在 `.local/acceptance/conversation-gitlab-setup/`，不纳入 Git。

## 真实验收

用户确认沿用实例 `https://git-devops.opencsg.com`、账号 `RussellLuo`。隔离服务为 `http://127.0.0.1:18092/`，回调 `http://127.0.0.1:18092/v1/platforms/gitlab/callback`。验收初始没有平台连接、OAuth 应用、账号凭据、通知、工具或 Workspace 项目绑定；未复制日常服务的平台配置。

真实 Chromium 已在全新默认个人助理会话发送一条自然语言请求：连接指定账号并开启全部可访问项目的个人待办，首次应用由用户在官方页面及安全设置完成，之后自动继续；不启用项目活动或工具。真实模型已读取流程并创建操作 `940a0787cc7aa60074ccbdd25fa12ac3`，状态为 `waiting/application`，结果已订阅 Inbox，第一回合已完成。初次等待时只有这一条 human 消息，完成后仍只有这一条。

用户通过官方页面创建新应用，并经原消息的安全设置表单导入和授权。回调于 **2026-11-11 13:46:35 +08:00** 完成，独立核对如下：

1. 应用引用 `4004621720842c9d73e6559bf2856361` 的原生所有者为 **13**，回调为本次 18092 地址、scopes 仅 `api`。导入候选已由原回调提交，未留下待核验导入；原操作的 Agent 固定输入仍不包含应用 ID，human 后补的绑定引用可核对。
2. 原操作 `940a0787cc7aa60074ccbdd25fa12ac3` 为 `completed/result_version=1`，应用与授权为 applied；连接 `13edc0f7295aacf4fb31aec4d8dfc186` 对应 **RussellLuo / 13**。首次操作生成的原连接目标未替换，没有经独立设置再创建第二个连接。
3. 稳定事件 `platform-connect:940a0787cc7aa60074ccbdd25fa12ac3:1` 投递至原完整成员的 Inbox 序号 **1**；独立检查交付回执、任务事件关联和 `consumption.json through=1`，结果处理确认 `handled_version=1`。真实 Agent 自动进入第二回合，human 消息数始终为 **1**。
4. 成员通知操作 `b4506675157db9548697bdf25519bc40` 为 completed/applied/version 1，目标为原个人助理的完整 Room／Agent／Membership。个人待办覆盖该个人账号全部可访问项目，接收起点为 `2026-11-11T05:46:44Z`；项目活动关闭，工具无绑定，Workspace 无项目绑定，权威配置与操作回执一致且无待应用错误。
5. 独立 CLI 的 `connect status --source-seq 1` 与 `connect wait --operation-id ...` 返回同一原操作及结果版本，wait 为 `pending=false`。浏览器刷新、完整设置的同一操作投影、关闭设置和 390px 窄屏检查通过。

无凭据核对报告为 `.local/acceptance/conversation-gitlab-setup/real/verification.json`，最终 `passed=true`。保留 `completed.png`、`operation.png`、`settings.png`、`mobile.png`、`mobile-result.png` 及 CLI 状态／wait 回执。临时授权 URL、Cookie、state、PKCE、授权码和 Token 不写入核对报告。

本次首次应用组合同时覆盖 CG2 的“新 OAuth → 不补聊自动开启个人待办”闭环；CG2 旧隔离服务的独立等待记录保留，不将旧操作改写为成功。浏览器截图期间曾出现一次只读同步 `ErrBusy` 提示，页面随后自动恢复；保留 `mobile-transient-busy.png` 及恢复后的截图，连接和订阅回执未改变。

真实验收只确认订阅已保存并生效，不声称真实待办已进入 Inbox 并产生提醒。真实多账号、远端权限／并发故障仍沿用各阶段未验边界；本次单账号通过不替代这些证据。

验收结束后已停止本次 18092 隔离服务及专用浏览器，保留配置、原始回执、报告和截图；日常服务未修改。临时验收二进制和一次性启动／核对脚本已清理，可从当前源码与上述测试重建。
