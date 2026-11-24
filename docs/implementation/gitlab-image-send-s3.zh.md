# GitLab 图文真实验收与开放

日期：2026-11-21。对应[方案](../GITLAB-IMAGE-SEND-PLAN.zh.md)阶段 3。真实 GitLab 发布、独立原图比对、原回执重查、真实通知回流和浏览器验证已完成，正式目录／执行注册已同步开放。日常服务及既有 Session 未切换；没有提交或推送代码。

## 授权与真实结果

用户指定 RussellLuo 和 `product/agentichub/teamx-demo` MR !12，并明确允许新增一条文字测试讨论。实际核对账号 ID 13、项目 ID 482、MR 全局 ID 13190／IID 12。总计新增 3 条 note、上传 3 次，没有重复发送或其他平台写入：

| 用途 | 内容与真实结果 |
| --- | --- |
| 文字讨论种子 | [note 228578](https://git-devops.opencsg.com/product/agentichub/teamx-demo/-/merge_requests/12#note_228578)；正文“TeamX GitLab 图文验收讨论：仅用于验证同一线程内的单张原图回复。”；完整 discussion ID 为 `680f5bcbb2f1d20b8beff17953e04503799e674f` |
| 真实 Agent 普通评论 | [note 228579](https://git-devops.opencsg.com/product/agentichub/teamx-demo/-/merge_requests/12#note_228579)；稳定动作 `real-gitlab-images-comment`；两行说明和蓝、绿两张原图，顺序完整 |
| 真实 Agent 讨论回复 | [note 228580](https://git-devops.opencsg.com/product/agentichub/teamx-demo/-/merge_requests/12#note_228580)；稳定动作 `real-gitlab-images-reply`；仅第二张绿色原图，无正文，留在上述准确讨论 |

普通评论的准确说明：

```text
TeamX GitLab 图文验收：两张测试原图依次为蓝色与绿色。
验证一条评论保留完整说明、图片顺序和原始字节。
```

两张原图均为隔离会话中发布的人类 PNG 测试附件，160×90、每张 302 字节。下载 SHA256 分别为：

- 蓝图：`9a499c9b0a09795fd237027e41983dfb27438cd602a18fc3003bb5f49cc1659c`
- 绿图：`03e8d7eac53d3c71b26bfcd0447701eacc8bd418c86fdac571202e98af9c0b6e`

默认助理中的真实 DSH／模型通过普通调度、正式 Inbox／History、生产 CLI／HTTP 和持久动作提交两条完整请求。只预置经真实账号只读核实的既有托管目标，不向 Agent 增加 URL／原生 ID 发送接口，不制造事项，不追加 JSON 或命令教程。两条动作均为 succeeded、一次尝试、直接人类依据，回执保存准确账号、完整原 input 和 note ID。

平台分别返回三个上传的相对 `/uploads/<secret>/image-N.png` 路径和 HTTP 201；评论正文及空行未被规范化。独立 GET 核对作者、MR、全部生成正文及讨论归属，再逐图认证下载比对原始 SHA256。前置原生接口小验证复用这些成功样本，没有额外上传或评论。JPEG／WebP、上限和故障矩阵沿用组件证据，没有重复制造远端故障。

## 回执、回流和浏览器

独立 OS 进程重开原发送 Store，通过带执行凭据的真实 CLI 查询两条原回执，并各执行两次同 ID 重查。发送 preparer 故意不可用，平台请求计数为零；旧动作、完整 input、note ID 和结果未改变。早期真实 Agent 也已重新查询两条原回执；其后的回流装配失败未被算作整轮成功。

真实项目 Events 分别返回事件 237891／237892。验收使用正式订阅配置核对账号及项目，将这两条真实事件经过现有 Accept／Notification／持久收件／Acknowledge 链路交给新的隔离观察会话。它们的项目、账号、MR、note ID 均能在原发送 Store 中准确匹配 Agent 账本；观察会话本身只有账号上下文，不冒充另一个会话的发送者。

观察会话中的真实 Agent 正式读取并消费两条通知，新增外部动作 0、事项 0、平台写入 0。这里验证的是选定真实事件的完整接收与读取，不另称为重新验收周期轮询、分页、远端 Todos 或日常订阅。原发送测试中被错误装配中断的输入状态保留，未清除、改游标或绕过恢复屏障。

Playwright Chromium 核对实际 Go 服务和真实 GitLab 图片代理：桌面 1440×1000 展示图文评论和单图回复，三张图均加载为 160×90；原有放大预览可用；390×844 移动视口重新打开通知详情后，两类图片可见且无页面横向溢出，未发现脚本错误。最终浏览器检查重开已验收的观察数据，没有额外模型回合。截图及原始报告位于本机忽略目录 `.local/gitlab-image-send/real-20261121/browser-final/`，这是视口模拟，不是实体手机验收。

## 改动与开放边界

- 正式 Catalog 和 manager 共用 `ImageReferenceActions`，移除仅在测试中切换图文实现的装配。现有图文 CLI、授权、恢复测试直接走正式注册；原“生产应拒绝图片”的过期断言随开放移除，原安全拒绝矩阵继续保留。
- 仅实际含图执行设置 150 秒绝对期限；纯文字保持原授权、重试和时间行为。发现展示文字／图片联合 schema，实际含图在落账前仍选择直接指令、一次尝试策略。
- GitLab 通知投影按相同原生账号 ID 标记 context_only，并按原生项目／MR／note ID 匹配账本。历史已保存的发送者元数据可直接投影，不迁移旧记录、不猜测文本作者。
- 真实平台发现讨论回复使用 `DiscussionNote`。采集器现将该原生类型按评论解码，保留真实目标类型；不再漏掉讨论回复回流，没有增加新的发送操作。
- GitLab Skill、根帮助和[用户文档](../gitlab.zh.md#发送图文评论)同步当前可用状态；未增加发送 UI、上传工具、功能开关或媒体状态机。

详细结果及调用成本见验收摘要（本地验收文件 `gitlab-image-send-real-evidence.json`）。发送两条任务合计 10 次模型工具调用、9 条 CLI 子命令，其中 resolve／execute 共 4 次、发现 2 次、回执查询 1 次、本地报告 2 次；帮助、格式错误、History、policy、事项读取均为 0。目标及原图发布属于独立前置条件，当前 turn／policy 由正常运行时提供；没有把未知状态的读取算作免费。

通过的回流样本为 5 次工具调用、1 条 CLI 子命令，业务动作 0；包含 History 和事项查询的实际成本，不扣除模型主动读取。没有已交付的旧图文基线，不作性能提升结论。

失败证据均保留：发送前的本地身份／成员装配错误没有远端图片写入；成功发送后的旧匿名 CLI 查询、重复本地打开操作 ID、漏解码 DiscussionNote、缺少订阅绑定及来源快照装配错误均不计为通过，没有为修复它们补发。额外只读样本 `recovery8` 虽然未创建事项／外部动作，但执行了不必要的 Workspace `ls`，被严格调用审计拒绝，未计入通过样本，也未放宽验收器。通过的真实回流证据为 `recovery7`；最终移动图片浏览器复验单独保存在 `browser-final`。

## 验证与复验

受影响平台包、正式目录、普通文字、图文托管入口、策略反馈、完整输入冻结、撤销／取消、原图 pin、幂等、独立进程恢复、本人通知及飞书合同的定向验证通过。最终 Room race 为 131.002 秒；正式 CLI／授权／字段反馈组合为 26.353 秒；安全 e2e 预留／命令计数为 1.010 秒。没有运行全仓测试。普通 CI 不运行真实模型或真实平台写入。

```sh
go test ./internal/platform ./internal/platform/gitlab ./internal/platform/lark ./internal/platform/tool ./internal/app/platforms -count=1 -timeout=4m

go test -tags=integration ./internal/room -run '^(TestGitlabImagesPreflightAndPolicy|TestGitlabImagesManagedCLI|TestAgentCommandHelp.*|TestAgentJSON.*)$' -count=1 -timeout=4m

go test -race -tags=integration ./internal/room -run '^(TestGitlabImage.*|TestGitlabToolEntryManagedWorkflow|TestReferenceActionInternalHTTPWithoutCLI|TestReferenceActionInternalRetriesAndUnknown|TestOwnGitlab.*|TestOwnLark.*|TestActionDispatch.*|TestRoomActionImages.*|TestLarkActionAgentContract)$' -count=1 -timeout=6m

go test -tags=e2e ./internal/room -run '^(TestGitlabImageDispatchReservation|TestGitlabImageAgentCommandAccounting)$' -count=1
```

真实入口为 `TestRealGitlabImageSendPreflight`（GET-only）、`TestRealGitlabImageAgentSend`（最多两条图文 note／三次上传）、`TestRealGitlabImageReceiptAndReturn`（已有发送报告的只读继续）、`TestRealGitlabImageSavedReturnBrowser`（已有观察报告的浏览器继续）。均为显式启用的 e2e 测试；写入口要求冻结的 manifest、两张原图 SHA256 和全新持久 dispatch 目录，并在每次原生写入前以排他文件预留额度。额度消耗后不能重跑补发。

发送输入由 `TEAMX_GITLAB_IMAGE_SEND_INPUT` 指定；准确 OAuth 连接使用 `TEAMX_REAL_OAUTH_SNAPSHOT`／`TEAMX_REAL_OAUTH_CONNECTION`，模型使用 `TEAMX_INPUT_COMPARISON_CONFIG`，输出使用全新的绝对路径 `TEAMX_DSH_OUTPUT`。本次只复制仍有效的现有 access grant，传输拒绝 OAuth refresh／revoke，未旋转或改写日常服务凭据。

只读继续设置 `TEAMX_GITLAB_IMAGE_SEND_RECOVERY=1` 和原 `TEAMX_GITLAB_IMAGE_SEND_REPORT`；需要浏览器时另设 `TEAMX_GITLAB_IMAGE_SEND_BROWSER=1`，待生成 browser-ready.json 后运行 `node internal/room/testdata/gitlab_image_send_browser.mjs OUTPUT`。单独重看已验收观察会话可用 `TEAMX_GITLAB_IMAGE_SEND_BROWSER_REPORT`，无需新一轮模型调用或真实发送。

代码验证、正式注册开放和日常采用分别记录：前两项已完成，当前运行服务未重启、既有 Session 未轮换；其他账号、项目、目标和新发送不在本次写授权内。
