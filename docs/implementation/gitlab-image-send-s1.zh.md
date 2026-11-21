# GitLab 隔离托管图文动作

日期：2026-11-21。对应[方案](../GITLAB-IMAGE-SEND-PLAN.zh.md)阶段 1。

阶段 1 已完成隔离实现及本地验证。普通 MR 评论和准确讨论回复共用正式实现，只有测试 manager 注入图文执行器及发现合同；生产目录仍注册 `gitlab.ReferenceActions`，生产 manager 仍调用文字 preparer，未开放含图发送。没有真实 GitLab 上传／评论、真实模型调用、日常配置变更、服务重启或 Session 轮换。

方案要求的前置真实接口小验证尚无准确账号、项目、MR／讨论、原图及内容的写授权，因此按方案允许的夹具路径推进。目标实例的实际上传响应、引用路径及正文规范化仍待真实验证；以下证据不能替代这一门槛。

## 实现范围

- `gitlab/image_actions.go` 严格解码 body 和／或 1–4 个有序 Room 图片引用；含图使用独立的目标解码路径，纯图片无需占位文字。使用实际原图字节、MIME、SHA256 和既有大小限制。
- 私有 `roomActionContext.Input` 保存不可变原输入 JSON。执行 preparer 在取得 Spec、检查授权和落账之前选定文字／含图实现；含图为直接 Room 指令、一次尝试、不重试 unknown，文字保留原有授权及重试策略。非法图片输入不会退回文字路径。
- 直接复用 `prepareRoomActionImages` 的来源核验、成员／回合／Session 检查、快照和对象 pin；整个 session 准备一次集合并统一释放。内容摘要下沉为 platform 纯函数；固定兼容向量及既有飞书测试验证 v1 结果不变。
- `image_upload_http.go` 仅向准确 MR 所属项目提交 multipart 图片。安全名称与真实 MIME 由内部生成；逐次取得原账号 OAuth 使用许可，响应完整读取／关闭后释放。请求有界、不重定向、不重放，不依赖 glab、临时图片文件或 Agent 提供的服务端路径。
- 校验 url／full_path 的 origin、项目、secret、准确文件名及资源一致性；不用响应自带 markdown。上传前后使用同一 Markdown 构造／AST 检查，核对追加图片节点和顺序。独立 quick action 行、吞图的未闭合代码／HTML 块在上传前拒绝，不修正文案。
- 原 body 仍限制 4096 UTF-8 字节；生成正文单独限制 8192 字节。两种固定工厂共用现有 NoteAPI HTTP 实现，文字工厂上限没有扩大。
- 全部上传成功后复核准确目标，取得本次 note 凭据许可，再检查原图 lease 和完整冻结动作；最终检查接收原 body + images，生成 Markdown 只传给原生 NoteAPI。含图 session 使用一次建立的 150 秒绝对期限，单个网络请求最多 30 秒，继承更早期限。
- 只提交一次 note POST。完整响应可解码时先保存正整数 note ID；独立 GET 核验作者、项目／MR、完整正文，讨论回复另核验准确线程。回读或线程失败保留 ID 并记 unknown，不补发。上传失败和最终检查失败不发布部分内容，明确提示未引用上传附件可能保留。

## Agent 合同

GitLab 的工具发现明确给出实际 Skill 命令，区分 `tools run ENTRY` 原生 CLI 与根 `actions resolve/execute --tool ENTRY` 托管动作。动态 schema 宣告当前可用内容；生产 schema 继续是文字合同。

GitLab MR 解析使用局部投影，保留单份账号、准确项目、MR ID／IID 和完整 discussion ID；相邻说明文字授权与含图直接指令条件。终态回执保留完整原始 input、一个动作状态、次数、单份账号／目标、verified、已知 note ID／URL 和必要版本／失败原因。只有准确成功结果为 verified=true；非三种终态、Policy 非空或缺少准确 Result 的反馈保持原样。

已知有效目标和当前合同、来源及依据时，执行请求只需 target_ref、turn_number、id、operation、input、policy_token、authority 七个顶层字段；authority 只需 type、room_seq、reason。有事项时可以用 context 代替重复的 target_ref／事项字段，服务仍校验匹配及版本。

根 `actions execute --help` 提供普通评论和准确讨论回复两个完整图文示例，复用 request／request-file／stdin；公共字段错误可定位到 `input.images[0].attachment_id`。没有新增图片 resolve、强制详情查询或私有恢复入口。`actions read --id` 继续返回完整旧证据，读取不进入 preparer，也不重新上传。

GitLab Skill 只补本阶段必要入口、整条发送及 unknown 指引，保留开发、评审和待办等工作流。阶段 2 仍需依据真实 Agent 轨迹收敛指引及修复反馈。

## 本地证据与成本

改动前的文字 schema、帮助及 Skill 样本保存在[基线](gitlab-image-send-baseline.json)；实际 manager／service／CLI 的请求、内部 DTO、正常投影及冻结记录保存在[合同样本](gitlab-image-send-contract-evidence.json)。样本为本地 HTTPS 夹具，非真实平台结果。没有已交付的旧 GitLab 图文链路，因此无旧图文性能基线。

下面的目标／回执样本均为准确讨论回复；字节数按实际紧凑 JSON 的 UTF-8 字节计，帮助／Skill 按正文计。

| 样本 | 原 DTO／原帮助 | 最终投影／新帮助 |
| --- | ---: | ---: |
| 文字目标合同 | 1361 | 1018 |
| 文字回执 | 778 | 757 |
| 图文目标合同 | 2154 | 1949 |
| 图文回执 | 807 | 958 |
| resolve 帮助 | 1602 | 1654 |
| execute 帮助 | 2440 | 4219 |
| GitLab Skill | 10090 | 11116 |

图文回执新增完整原 input 和 operation，因此比不含这些业务内容的内部回执多 151 字节；不能把省略原内容当作精简。execute 帮助增加两个完整示例及含图授权／关闭状态说明。样本执行请求为 575 字节、七个顶层字段；业务调用路径为已有合同的一次 execute，或首次取得合同的 resolve + execute。图片发布、已有托管目标建立、必要 policy／History／事项读取分别计为前置工作，未把它们冒充免费；真实 Agent 调用成本、格式试错和跨回合轨迹留待阶段 2 测量。

## 验证

通过的定向检查：

```sh
go test ./internal/platform ./internal/platform/gitlab ./internal/platform/lark

go test ./internal/room -run 'Test(ReferenceAction|RoomActionImages|ActionDispatch|AgentCommandHelp|AgentJSON|HistoryMCP|HistoryImage|HistoryPage|HistoryQuery|History.*Budget)' -count=1

TEAMX_GITLAB_IMAGE_EVIDENCE="$PWD/docs/implementation/gitlab-image-send-contract-evidence.json" \
  go test -tags=integration ./internal/room \
  -run 'Test(GitlabImage|AgentTypedAction|ReferenceActionInternalHTTPWithoutCLI|ReferenceActionInternalRetriesAndUnknown|ReferenceEntry|LarkActionAgentContract|AgentJSONInputManagedActionReplayAndLimits)' -count=1

go test -race ./internal/platform ./internal/platform/gitlab ./internal/platform/lark

go test -race -tags=integration ./internal/room \
  -run 'Test(GitlabImage|RoomActionImages|ActionDispatch|LarkImagesManagedCLI|LarkImagesDispatchGuards|LarkActionAgentContract|ReferenceActionInternalHTTPWithoutCLI|ReferenceActionInternalRetriesAndUnknown|AgentJSONInputManagedActionReplayAndLimits)' -count=1

go test -race -tags=integration ./internal/room \
  -run 'Test(GitlabToolEntry|GitlabImagesExistingMatterContext|AgentTypedActionsRestoreExactTarget|GitlabImagesFinalDispatchAndDeadline)' -count=1
```

平台组件覆盖严格输入、Markdown／quick action、边界长度、multipart 原字节与 MIME、上传响应路径／跨项目／跨 origin／重定向／取消／坏 JSON／超限、一次 POST、独立回读、作者／正文／线程失败及保留 ID。

实际托管入口覆盖纯图片、第二张、有序多图和四图、4096 字节 caption、两种操作、旧文字限制、条件 Spec 在落账前选定、规则／持久指令／问答拒绝、过期 policy 零上传、尝试前策略变化同时保留 Policy 与 Result、最终回调原输入、继承同一更早绝对期限、上传中取消／回合／Session 变化停止后续请求、幂等及完整输入冲突、同字节不同来源的 unknown 保护、来源失效后旧回执仍可查、事项 context 复用、字段诊断和文件输入。

动作前后比较非 action 持久文件，确认没有改动 Room／input-state／消费／History 状态；各出口检查原图 pin 和平台调用释放。原图准备、History 查询／预算／正式消费独立性继续由既有 `TestRoomActionImages*` 与定向 History 回归承接。原文字精确目标测试改为读取正式 Agent 投影并结合账号 scope 核对完整原生身份，未降低目标核验。

未运行全仓或完整集成套件。阶段 2 的真实 DSH／默认助理轨迹、跨进程恢复与并发专项，阶段 3 的真实 GitLab 发布、逐图下载比对、通知回流及浏览器核对均未完成。生产发现／执行仍保持文字注册，阶段 1 通过不代表正式图文开放或日常服务已采用。
