# GitLab 完整 URL 托管目标入口

日期：2026-11-21。对应[方案第 3.2 节](../GITLAB-IMAGE-SEND-PLAN.zh.md#32-完整-url-定位与托管目标建立阶段-4已实施)的阶段 4。URL resolver、Room 装配、持久引用及 Agent／用户指引已实施。组件、全新成员托管链路和真实只读定位已通过；两条授权的真实评论均已发布并独立核实。原始真实发送整轮因多余的原生只读调用被审计判失败，保留原报告；后续只用隔离模型验收及只读恢复验证修复，不增加真实写入。

## 实现

`internal/platform/gitlab/action_locator.go` 只接受所选账号同实例的完整 HTTPS MR URL，或准确 `#note_ID`／`#discussion_FULL_ID`。按段解码项目路径，整体转义一次作为 projects API 参数；拒绝查询参数、额外视图路径、点路径、编码分隔符、重复编码、用户信息、控制字符及不支持的锚点。主机名大小写和末尾单个 `/` 可规范化。

读取顺序固定为当前账号、完整项目路径、项目 ID 下的 MR。核对账号 ID／用户名、`path_with_namespace`、项目 ID、MR 全局 ID／IID 及完整网页路径。普通 MR 不扫描讨论；直接 discussion 走准确单条读取；note 映射最多 10 页／500 条 note，找到唯一映射即可停止，缺失边界、冲突、上限和未找到均保留 incomplete，不证明不存在。不可回复的独立普通 note 不降级为 MR 评论。全部读取共享 30 秒期限，单响应最多 1 MiB。

Room 在准确工具入口下复用既有 admission、逐次 OAuth 许可、取消及 Close。每次读取前后复查，保存临界区内再比较当前绑定、账号及凭据版本，并在 Store 锁内检查当前回合。成功目标使用真实 Project／MR ID 和 IID alias，线程保留完整 discussion ID，不把定位 note 当发送 Message。相同目标在同一成员内复用稳定 ref，旧引用不能被另一账号接管。使用原目标文件和账本，无新索引、事项或通知。

Agent 使用根 `actions resolve --tool ENTRY --request JSON` 的 `locator.url`，然后直接执行返回的 ref。旧 ref、通知来源解析、文字授权／重试及图文单次派发继续使用原实现。帮助、字段诊断、工具发现和 GitLab Skill 已同步；Lark 独立入口的示例继续使用 self locator。

## 自动及隔离验收

从无通知、事项、Workspace 或预存 action target 的新成员数据开始，通过正式 CLI／HTTP／manager 验证 MR 文字、单图、图文、note 锚点图片回复及直接 discussion 图片回复。重复 resolve 保持 ref；重开 Store 保留引用和原动作；同 ID／详情恢复不访问 GitLab，不新增上传或评论。按文件树比较，动作没有改变 Inbox／History 的消费、读取或消息状态。

覆盖错误账号／实例、项目／MR／网页路径冲突、非法编码、独立 note、重复映射、分页缺边界／上限、响应超限、权限／404／限流、期限／取消，以及最后一次 Check 之后发生的解绑、OAuth 登出和回合变化。提交竞争测试不依赖“先取消”来代替实际绑定／凭据变化。

验证命令：

```sh
go test ./internal/platform ./internal/platform/gitlab ./internal/platform/lark ./internal/platform/tool ./internal/app/platforms -count=1 -timeout=4m

go test -race -tags=integration ./internal/room -run '^(TestGitlabActionURL.*|TestGitlabImage.*|TestGitlabToolEntryManagedWorkflow|TestReferenceAction.*|TestActionDispatch.*|TestRoomActionImages.*|TestLarkActionAgentContract|Test.*Resolve.*|TestAgentCommandHelp.*|TestAgentJSON.*)$' -count=1 -timeout=8m

go test -tags=integration ./internal/room -run '^(TestActions.*|TestAgentCommandHelp.*|TestAgentJSON.*|TestLarkActionAgentContract|TestLarkReplyProductionResolvesCapturedMessageWithReadOnlyBinding|TestGitlabToolEntryManagedWorkflow)$' -count=1 -timeout=3m
```

三组通过，定向 race 为 193.732 秒。最终帮助／URL 合同又做了定向验证；E2E 新入口及原派发预算／命令计数测试通过。未运行全仓测试，普通 CI 不启用真实模型或真实写入。

真实 DSH／模型通过普通 Room 和默认助理的自然请求测试，未预注入 ref／原生 ID，也未追加命令教程。正常路径的目标调用是一次 resolve 加一次 execute；发现、Skill、消息读取和本地报告单独计数。调用及失败样本见结构化证据（本地验收文件 `gitlab-image-send-url-evidence.json`）。

## 真实账号、发布与恢复

用户单独授权[准确清单](gitlab-image-send-url-write-sample.zh.md)。账号 RussellLuo／13、项目 `product/agentichub/teamx-demo`／482、MR 全局 ID 13190／IID 12。全新默认助理只收到自然请求中的完整 URL，未预置目标。实际写入刚好 2 次图片上传、2 条 note：

| 动作 | 结果 | 内容 |
| --- | --- | --- |
| `gitlab-mr12-img1-comment-20261121` | [note 228616](https://git-devops.opencsg.com/product/agentichub/teamx-demo/-/merge_requests/12#note_228616) | 准确说明“TeamX GitLab URL 入口验收：第一张原图（蓝色）。”和第一张蓝图 |
| `gitlab-mr12-note228578-img2-reply-20261121` | [note 228617](https://git-devops.opencsg.com/product/agentichub/teamx-demo/-/merge_requests/12#note_228617) | 仅第二张绿图，回复 note 228578 所在的完整讨论 `680f5bcbb2f1d20b8beff17953e04503799e674f` |

两条账本均 succeeded、一次尝试，作者为 user 13。独立 GET 核对完整生成正文、MR 和线程，认证下载逐图比对原 SHA256；每张均为原 302 字节 PNG。无额外测试讨论、文字降级、部分发布或重复补发。

正式只读 URL 验收另完成 11 次 GET、零写入；MR URL、note 228578 和完整 discussion URL 均准确，后两个得到同一持久线程 ref。真实发送样本的独立进程恢复只读原 Store，通过带执行凭据的 CLI 读取两条完整回执并各重查两次同 ID；发送 preparer 和网络均关闭，不续传或重新发布。独立远端内容复核与本地账本恢复的请求分别统计。

## 失败记录及采用边界

真实发送原报告 `.local/gitlab-image-send/url-real-20261121/run/report.json` 保持 failed：虽然两条动作、独立图片下载及线程核实均成功，模型在 MR 发送前还尝试了 `mr view` 和 note listing 两次原生只读查询，验收网关均返回 403，没有取得原生身份，也没有额外远端写入。后来将“直接 URL 评论先走托管 resolve，无需先原生查询”的指引提前至 Skill 顶部和工具发现。

早期普通 Room 样本发生多余的动作预查；收敛自然请求及入口说明后通过。后续一轮默认助理在发现工具前执行了无关的 `ls "$DSH_HOME"`，该轮同样保留为失败，不因最终评论成功而算通过。只读恢复首轮因测试服务器未装配执行凭据而失败，改用现有带访问中间件的服务器夹具后复验。上述失败均不作为重发真实评论的理由。

因此真实平台的成功写入／内容证据与模型调用规范的复验分开报告；没有把首次整轮调用审计改成通过，也没有声称在修订指引后再次完成新的真实写入。浏览器和通知回流沿用阶段 3 已有证据，本阶段没有重复做其真实验收。

日常服务 PID 20380 的启动时间核实为 2026-11-21 11:18:09（Asia/Shanghai），本轮未重启，既有 Session 采用本次能力的情况未验收。测试只运行独立数据和临时二进制，没有提交或推送。

原生读取协议参考：[项目 API](https://docs.gitlab.com/api/projects/)、[MR API](https://docs.gitlab.com/api/merge_requests/)、[MR discussions API](https://docs.gitlab.com/api/discussions/#list-all-merge-request-discussion-items)。目标实例的实际响应以上述真实读取及发布回读为证。
