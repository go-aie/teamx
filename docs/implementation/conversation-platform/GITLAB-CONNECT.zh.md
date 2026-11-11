# 对话式 GitLab 连接实施记录

日期：2026-11-11。对应方案 CG1。连接 CLI、固定授权组件、浏览器会话桥接、OAuth 回调回执和 Inbox 结果生产者已实施；本地验证通过，并已通过单账号真实浏览器＋GitLab＋模型 Agent 的“对话发起 → 人工授权 → 保存 → 无新聊天自动接续”组合验收。CG2 后续实现见 [GitLab 成员配置](GITLAB-MEMBER.zh.md)；CG3–CG4 未实施。

## 交付范围

- `platforms list` 返回飞书及 GitLab 连接的实例、账号、连接版本，以及 GitLab 应用的公开 ID／实例和回调／scopes 兼容性，不返回凭据。
- `connect start --provider gitlab --origin URL --name NAME --application-id ID --account-username USER --operation-id ID --source-seq SEQ [--notify inbox]` 发起新连接。实例使用无尾斜杠的规范 origin；应用必须已验证且兼容当前服务回调。固定输入包含预期账号时，授权账号不符即失败，不替换目标。
- 复用已核验账号时使用 `--platform-id ID --platform-version V`，保留精确实例、名称及可选账号约束，不执行新 OAuth。连接操作、原请求和完整成员身份沿用原幂等检查。`status`、有界 `wait`、`cancel`、`notify`、`acknowledge` 共用原操作和回执。
- 原 human 消息及完整设置显示同一 GitLab 固定授权组件。`/v1/platforms/gitlab/connect` 只接收原 human、当前操作版本和完整 Room 身份，核对规范服务地址、成员及固定应用后建立原 OAuth 会话。入口位于原 Cookie 路径内，保留 HttpOnly、SameSite、state 与 PKCE；同一浏览器的标签页复用会话，其他浏览器不能接管。Agent 的 start/status 不创建浏览器会话或接收 OAuth URL。
- 回调仍是唯一凭据／连接提交路径，不调用旧 onboarding 的默认通知／工具配置。回调提交前保存精确账号依据，再次检查原操作、取消状态及成员；提交后只核对既有连接并保存终态。操作及消息不保存 Cookie、state、PKCE、授权码或临时链接。应用指纹只用于固定配置的变更检测。
- 保存终态后复用现有可靠结果通知、稳定事件 ID、投递回执及 Inbox 调度，无新增信封、消费游标或调度分支。同步等待、主动继续及自动接续查询同一结果版本。服务重启核对原固定目标、应用及已验证账号，不能重放授权码或再次保存连接。
- 缺少／不兼容应用返回配置原因和“设置 → 平台”入口。接入 Skill、帮助和 Agent 指南已更新；GitLab 首次应用配置、账号／项目选择、通知及工具配置仍使用原完整设置，不宣称 CG2–CG4 已交付。只连接不创建 GitLab 通知配置或工具绑定。

## 本地证据

```sh
go test ./... -count=1 -timeout=8m
go test -race -tags=integration ./internal/room \
  -run '^Test(GitLabConnect|GitLabAuth|GitLabSetup|PlatformConnect|PlatformSelection|PlatformMember|LarkSetup|LarkAuth)' \
  -count=1 -timeout=4m
node --test --test-reporter=dot internal/web/tests/web_*test.mjs
TEAMX_CONNECT_BROWSER=1 go test -tags=integration ./internal/room \
  -run '^TestGitLabConnectConversationBrowser$' -count=1 -v -timeout=90s
git diff --check
```

GitLab 专项覆盖真实服务 CLI、重复 start／改变固定输入、查询／wait／主动继续、已有账号复用、重复点击／多标签页、错误浏览器／回调来源／操作者、账号不匹配、同实例账号已连接、拒绝授权、取消及交换期间取消、会话过期、成员移除、应用变化和服务重启。通过故障注入覆盖连接已提交而操作回执写入中断（替换前／后），核对重启后不重新交换、不改写连接、不撤销已采用 Token。

连接结果生产者已通过真实 Room／Inbox／成员调度及受控 backend 的自动接续测试：只有原一条 human 消息，回调后通过普通 Inbox 进入回合，读取并确认原操作事件。原 C4–C5 的混合队列、阻塞、暂停、容量、回收及 Session 轮换测试继续复用，未新增另一套队列框架。

真实 Chromium＋TeamX HTTP／独立 CLI 测试通过，远端 GitLab OAuth 使用模拟适配器。覆盖授权按钮、重复点击／标签页、原 Cookie 路径、设置关闭、刷新、回调保存、结果恢复和 390px 窄屏。前端模拟 DOM 另行覆盖目标 origin 检查、禁止跳转到其他站点、稳定节点和接续提示。这些证据不代替真实平台或真实模型验收。

初次 Chromium 验证发现桥接入口不在原 Cookie 路径内，重复点击不能携带浏览器绑定；修正为 GitLab 路径内入口后，完整 Chromium 专项通过。没有扩大 Cookie 路径或放宽回调校验。

本地日志及截图：`.local/acceptance/conversation-gitlab-connect/`，不纳入 Git。

## 单账号真实验收

用户指定实例 `https://git-devops.opencsg.com`、账号 `RussellLuo`。独立服务使用 `http://127.0.0.1:18089/`，配置与数据位于 `.local/acceptance/conversation-gitlab-connect/real/`；8088 日常服务未修改。

首次准备曾使用历史验收应用，其回调仍为旧路径，不能用于当前服务。用户随后明确提供了应用 ID，并确认修改回调；服务侧核对该 ID 匹配日常安装中已有的受保护应用配置，独立验收只复用该应用配置并使用 18089 回调，不复制日常 Room、账号凭据或连接。未自动创建或修改远端应用，未改动 8088 服务配置。该应用与日常服务共享远端身份，不能把它称为独立创建的验收应用。

第一次真实请求的授权入口出现通用 `identity` 403，未保存连接。通过旧错误无法确认具体校验项；原暂态会话在正常退出后结清为 cancelled，数据保存在 `real/`，没有复用原操作 ID 或改写其终态。补充诊断后，在保留旧证据的基础上使用 `real-retry/` 新独立数据完成完整验收。精确诊断区分过期的页面／服务身份、非规范服务来源、原成员不可用和已绑定的浏览器会话；前端不再把所有身份错误一律解释为“换回原浏览器”。这些改动保持原 403、原身份约束和 Cookie 校验。旧请求的通用错误不能作为根因已定位或原会话恢复通过的证据。

成功链路：

1. 12:38 左右，真实 Chromium 在独立默认“个人助理”会话发送一条 human 请求，固定实例、账号 `RussellLuo` 和连接名“CG1 GitLab 真实验收”，明确只连接。真实模型读取接入 Skill，查询原请求及可用应用，发布 GitLab 授权组件并确认 Inbox 订阅。
2. 用户通过原消息的授权按钮，在真实 GitLab 页面完成人工授权。回调于 **2026-11-11 12:39:25 +08:00** 保存连接，核验原生用户 ID **13**、账号 **RussellLuo**；操作 `54ce4574090e6cdb17ef5509f6d13674` 为 `completed/result_version=1`，连接 ID `8d09ed31e88d888088a60b35e14ed57d`。
3. 同一结果生成稳定 `platform-connect:<operation-id>:1` 事件，交付到原完整 Room／Agent／Membership 的 Inbox 序号 **1**。模型自动进入第二回合，查询原操作及实际账号，保存 `handled_version=1` 并反馈连接结果。**human 消息始终只有一条**，没有通过补发聊天触发接续。
4. 分别核对持久化事件、交付回执、任务事件关联和 `consumption.json through=1`，成员第二回合为 completed。消费游标位于独立消费文件，不从 `inbox.json` 的元数据或结果处理回执推断消费。操作、连接、交付、消费和处理确认分别成立。
5. 独立 CLI 的 `connect status --source-seq 1` 和 `connect wait --operation-id ID` 查询同一操作及结果版本；wait 返回 `pending=false`。首次未授权时的有界 wait 返回 pending 且未取消操作。主动继续的查询路径没有向 Room 再发聊天，也没有重放授权或连接。
6. GitLab 订阅配置、成员配置操作和工具绑定均未创建。完整设置与原消息共享已连接／已处理回执；真实 Chromium 的刷新恢复、设置返回、390px 窄屏及脚本错误检查通过。

安全报告：`.local/acceptance/conversation-gitlab-connect/real-retry/verification.json`，最终 `passed=true`。真实截图为同目录的 `connected-real.png`、`settings-real.png`、`mobile-real.png`；CLI 查询结果也保留在该私有目录。不记录临时 OAuth 链接、Cookie、state、PKCE、授权码或 Token 于验收报告。

首次完整本地回归的 Room 包耗时 152.071 秒；此前相关 integration＋race 回归耗时 48.080 秒。403 诊断补充后新增默认助理授权／回调与真实 Chromium 测试，覆盖原 Room 和默认助理两种会话；针对过期页面、错误规范来源和浏览器绑定分别核对拒绝原因，保持拒绝时不创建授权会话。前端对应提示独立回归。最终 integration＋race 专项通过（Room 包 41.303 秒），前端全量 391 项通过，默认助理和普通 Room 的 GitLab Chromium 专项均通过。

CG1 的单账号真实验收出口已通过。已有账号复用、错误账号／浏览器、重复／晚到回调、并发取消、异常存储和恢复仍以本地测试为证据；不宣称真实多账号、真实并发故障或旧失败会话恢复已验收。个人待办通知的后续实现与验收见 [GitLab 成员配置](GITLAB-MEMBER.zh.md)；账号／项目选择及首次应用配置回流继续由 CG3–CG4 负责。
