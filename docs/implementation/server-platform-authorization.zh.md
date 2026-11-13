# 浏览器平台授权与模型切换接续

2026-11-12。对应[常驻 Server 与 Web Client 方案](../DESKTOP-APPLICATION-ANALYSIS-AND-PLAN.zh.md) W4。代码与自动验证已实施，真实桌面／手机的真实平台授权出口待补。

## 实施范围

- 飞书复用 Server 持有的官方 Device Flow、账号核验和连接保存。对话中的官方链接支持新标签；窗口受限时在同一页打开。二维码继续通过已有受保护的图片接口显示。离开页面不取消 Server 操作，页面恢复后读取原结果；设备码与 token 不交给浏览器。
- GitLab 对话卡片和完整设置的授权均支持同页退路。沿用规范回调地址、原浏览器 HttpOnly Cookie、state、PKCE、应用版本和完整成员身份校验。其他浏览器可以在正常登录后查看共享结果，不能接管原 OAuth 绑定。
- GitLab 完成页提供窄屏 viewport 和固定同源的“返回 TeamX 查看结果”链接。原 OAuth Cookie 匹配时，返回链接只携带实例、操作者、Room、成员、原请求序号及 operation ID；不包含 OAuth 会话 ID、code、state、验证器或 token。不接受外部 return URL。没有有效原绑定时只提供平台设置入口。
- 对话授权离开前保存非秘密引用和既有草稿；完成返回时先读取受保护的实例和 Room 目录，再核对完整原操作。原消息已不在最近一页时补读至当前历史起点的连续区间并定位卡片，保留正常历史翻页游标。刷新、重新登录、终态结果和浏览器返回只查询，不创建连接、不重放授权或业务消息。普通刷新不会因后台进度抢走正在输入的焦点。
- 设置页仅保存实例／操作者／服务代际、原授权会话 ID、平台及 onboarding 引用。刷新后仍通过原 HttpOnly Cookie 查询原会话；临时读取失败保留查询，绑定失效时明确提示核对连接，不自动登录。授权 URL、Secret、设备码和 token 不进入草稿或 history state。原页面关闭与显式取消保持区分；显式取消仍走既有接口。
- 访问登录页和 API 登录到期跳转只保留固定平台返回片段，重新登录后继续核对原操作；返回片段不授予访问权限。
- 修复模型切换的工具准入交集：已登记模型目标、仍处于等待轮换的当前 turn 保留 CLI／平台请求能力。例外要求真实受管模型 gate、活动 turn、`model_change` 原因、等待状态及非取消轮换同时成立。turn 结束后继续由原 rotation／平台生命周期门收拢调用，不改变暂停、取消、成员替换、授权写入及 Inbox 消费规则。

没有增加跨 App 票据、GitLab Device Flow、OAuth 网关、Linux 发送适配或新的模型／Session 状态机。W5 部署、备份和升级，W6 最终设备／发行验收，以及 W7–W10 原生客户端继续保留在各自阶段。

## 自动验证

基线为 `baf64e2` 加本次 W4 修改和已有的 Web 页面改动；不作为干净 HEAD 或最终发行归档证据。

验证期间，共享工作区另有访问开关／设置开发，曾暂时缺少定义而无法编译；定义补齐后，当前组合的浏览器 CLI fixture 又出现身份不匹配。没有回退或接管这些并行改动。本记录的最终 Go／浏览器证据使用隔离源码快照：`baf64e2` 叠加本次 Go 实现、相关测试／驱动，以及已保存的 Web 树。快照位置、文件摘要、报告、日志和截图保存在 `.local/server-platform-authorization-check/`。它证明 W4 基线候选，不代表另一组访问设置与 W4 的最终组合已经通过集成。

- Go 全量：隔离基线的 `go test ./... -timeout=8m` 通过，Room 包耗时 431.697 秒。随后补齐旧消息返回时的连续历史读取；前端全量与 `go test ./internal/web` 再次通过。最后的测试 helper 复用由定向竞态检查复核。
- 前端全量：`node --test internal/web/tests/*_test.mjs`，464 项通过。新增检查覆盖弹窗受限时同页打开、白名单引用、完整成员／实例隔离、完成返回只读查询、设置页刷新查询原会话、失效绑定及登录后恢复固定返回片段。
- 定向竞态：文末列出的 `go test -race` 检查通过（85.838 秒）；两个 Inbox 竞态场景另以 `-count=3` 通过。覆盖 GitLab 完成页 Cookie 绑定、固定返回地址、无秘密输出；原有错误浏览器、重复回调、过期、取消晚到结果、成员移除、应用变化和自动 Inbox 接续继续回归。全量运行曾暴露旧 Inbox 确认测试的 `busy` 竞态；确认改为仅对明确未受理的 `busy` 使用同一回执做有界重试，自动配置 Todos 的接续测试也复用此 helper，未改生产错误处理或重放规则。
- 新增模型交集使用真实模型 controller、Session rotation、连接操作存储和 Inbox，模型执行及平台响应由 fixture 控制。飞书／GitLab 分别覆盖结果在当前 turn 中到达或轮换后到达、用户暂停后保持暂停、轮换后查询同一操作与原 Inbox 消费。另覆盖模型等待期间主动取消 GitLab、轮换后同名成员替换及晚到结果拒绝；取消不产生 OAuth token exchange，替换不保存连接或继承原操作。
- 真实 headless Chromium + TeamX HTTP／CLI：GitLab 的普通对话、首次应用配置、弹窗受限的 390px 同页授权；飞书的 Room、默认助理、390px 同页官方链接与 Back。六个浏览器场景使用模拟平台／CLI，检查二维码、账号阶段、刷新、设置关闭、原浏览器回调、连接保存、返回定位与页面无脚本错误。既有浏览器驱动同步适配用户菜单，并将飞书应用与账号授权域名都隔离为 fixture。

复跑入口：

```sh
node --test internal/web/tests/*_test.mjs
go test ./... -timeout=8m
go test -race ./internal/room -run 'TestPlatformAuthorizationSurvivesModel|TestGitLabConnect|TestDefaultAssistantGitLabConnect|TestGitLabCompletion|TestModelRotation|TestOnlineModelsWait|TestPlatformConnect.*Cancel|TestPlatformConnect.*Replace|Test.*Rotation.*Tool' -count=1 -timeout=3m
TEAMX_CONNECT_BROWSER=1 go test -tags=integration ./internal/room -run 'TestGitLabConnectConversationBrowser|TestGitLabConnectFirstApplicationConversationBrowser|TestGitLabConnectSamePageConversationBrowser|TestPlatformConnectConversationBrowser|TestDefaultAssistantPlatformConnectionBrowser|TestPlatformConnectSamePageConversationBrowser' -count=1 -v -timeout=3m
```

浏览器检查需要可用的 Playwright／Chromium。可设置 `TEAMX_CONNECT_BROWSER_ARTIFACTS` 保存截图。

## 尚未完成的真实出口

本次没有可由工具操作的真实手机，也未进行真实账号的新授权或真实外部写入。上述 fixture 和触控视口不替代以下验收：

| 场景 | 待验结果 |
| --- | --- |
| 真实桌面、至少一部真实手机 | 分别完成飞书／GitLab 单账号授权、账号核验、连接保存；记录设备、系统、浏览器、源码／候选和入口 |
| 真实远程 HTTPS | GitLab Application 的规范回调与原浏览器 Cookie 成立，完成后可返回原操作 |
| 手机官方跳转 | 飞书链接／扫码、可能的 App 跳转及手动返回；GitLab 同页／新标签、登录到期恢复 |
| 真实自动接续 | 已订阅结果经原成员 Inbox 自动继续，无需另发聊天；结果保存与原业务需求完成分别核对 |
| 无桌面 Linux | 凭据续期、读取、通知和账号固定发送继续按 W5 单列；`account_pinning_unavailable` 限制没有解除 |

因此 W4 当前交付代码与自动验证通过的功能候选，真实平台／手机验收仍待补；不据此宣布无人值守部署或正式支持矩阵通过。
