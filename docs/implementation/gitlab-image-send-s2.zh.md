# GitLab 图文：真实 Agent 使用与恢复

对应[方案](../GITLAB-IMAGE-SEND-PLAN.zh.md)阶段 2，日期：2026-11-21。阶段 2 已完成实现及隔离验收；普通 Room／默认助理各 16 个真实 DSH 场景通过，正式图文注册继续关闭，没有真实 GitLab 上传／评论或日常服务配置变更。

## 实现与合同

复用阶段 1 的同一隔离图文实现、原图 lease、按输入选择的授权／重试 Spec、一次 note POST 和独立回读。没有新增发送入口、配置开关、上传进度或账本结构。

GitLab Skill 将托管评论收敛为确认目标与完整内容、提交一条动作、按回执报告三步。已有目标和当前合同直接复用；缺合同才 resolve。有事项时复用 context 和当前版本，没有事项不制造事项。完整普通评论和准确讨论回复示例继续共用根帮助。含图直接人类依据、当前 policy token、整条发送及 unknown 原回执查询保持明确；错误恢复不允许拆分、降级或绕过不确定动作。

真实默认助理的早期样本曾把 Room 附件展示链接追加到指定说明，再同时提交 images。这个样本未算通过。动态 schema 和 Skill 现明确 body 仅为用户要求的原始说明，纯图片省略，服务端生成图片 Markdown；正常成功直接报告，详情仅用于不确定、恢复或明确查询。

真实重启样本还发现 DSH 的只读恢复预检没有与启动路径一样解析 StateDir 的符号链接：输入桥配置内嵌物理路径，调用方继续使用 `/tmp` 的别名时被误判为配置变化。`CheckResumeConfig` 现先解析同一物理目录，再做原有指纹和准确材料比较；新增回归同时验证真实材料变更仍拒绝。没有修改配置匹配标准、Session 结算要求或开启新的 Session 绕过恢复。

## 验收装配

`TestGitlabImagesRealAgent` 使用已配置的真实 DSH／模型、普通 Room／默认助理、普通调度、发布的原图、正式 Inbox／History MCP、内置 GitLab 合同／Skill、真实 CLI／HTTP 和持久动作记录。仅 GitLab HTTP 目的地映射为本地 TLS 夹具，不使用真实 GitLab 凭据或远端写入。

测试准备四条人类原图消息和两个已有的托管目标，原生 MR 和准确线程通过准入的账号只读能力核验并保存；这属于既有目标的夹具前置条件，不是新增任意 URL／IID 的 Agent 解析能力。每个业务任务只包含自然请求、实际托管引用（首次）及稳定动作 ID，不追加 JSON 层级或命令修法教程。

每种入口覆盖普通评论与准确讨论的纯单图、纯多图、图文和文字，History 选图及跨回合只发第二张，来源／线程歧义，含图授权限制，服务重启原回执查询，上传失败、取消、unknown 和只读恢复。校验完整原 input、选图顺序、一次尝试及准确目标；模型说“已发出”不能代替账本和 HTTP 计数。原生夹具为各条评论分配独立 note ID，避免把夹具复用 ID 的假象转嫁为 Agent 恢复问题。

成本和原生命令检查读取完整 DSH 事件归档，按工具 ID 去重，再以 shell 分词统计实际子命令。生产活动诊断继续保持输入 2 KiB／输出 4 KiB 上限；不能拿被截断的长报告误判为格式错误，也不能把报告正文里的命令示例算成真实调用。早期诊断截断误判、空 body 修正和重复 resolve 的样本分别保留为未通过记录。

`TestGitlabImagesIndependentProcessRecovery` 为两种操作各覆盖已结算、第二张上传中、POST 已返回但回执未交付、独立回读前退出。首次派发和两次恢复各在独立 OS 进程运行。网络暂停期间，同 ID 返回 processing、改 input 冲突，另 ID 相同内容拒绝，账本读取可完成。恢复执行器故意不可用，验证原回执查询及同 ID 重查不进入 preparer，不续传或发布；完整 input／摘要、旧记录和 pin 释放仍核对。

## 成本与结果

完整结果及全部工具 ID／输入、输出字节数／摘要、动作记录和原始报告位置见[验收证据](gitlab-image-send-agent-evidence.json)。两种入口均为 8 条 succeeded、2 条 not_applied、1 条 unknown，各 9 次 note POST（包含 unknown 的一次）；失败、取消和查询未新增评论。所有动作只有一次尝试，含图 Spec 在落账前已限制为直接指令／一次尝试。

| 入口 | 全部工具 | CLI 子命令 | resolve／execute | 帮助 | 格式错误 | History | 发现 | 本地报告 | 回执查询 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 普通 Room／16 场景 | 60 | 47 | 13 | 8 | 0 | 1 | 3 | 16 | 7 |
| 默认助理／16 场景 | 51 | 35 | 13 | 0 | 0 | 1 | 2 | 16 | 4 |

两种入口的 policy／事项读取均为 0：当前 policy token 来自正常运行时上下文，任务已有托管目标且无关联事项要求；不是省掉未知状态的读取。首次普通评论和首次线程回复各为 resolve＋execute，其后已有目标的 6 个正常发送均仅 execute 一次；上传失败、取消、unknown 的三个独立任务也各 execute 一次。模型主动做的回执预查全部计入，没有作为接口必需步骤，也没有从成本扣除。

重启后的回执查询、取消和明确要求的 unknown 原回执查询均经过真实 DSH；未以记忆中的结果冒充新的查询。HTTP 夹具使用完整保存的原生目标，note ID 按条分配；这些仍是合成资源身份，不能当作真实 GitLab 作者／线程的验收。

调用计数包含全部 ACP 工具、实际 CLI 子命令、帮助／发现、policy／事项／History、报告和回执查询；同一 shell 中的多条命令分别计数。四条源图发布、两个已有目标准备和运行时提供的当前 turn／policy 前置条件单独列出，不视为免费业务发现。

没有已交付的旧 GitLab 图文链路，因此无旧图文性能基线，不作模型速度或稳定性能的前后结论。阶段 1 的合同样本仅用于同场景字段和 UTF-8 字节比较：

| 内容／同一夹具快照（UTF-8 字节） | 阶段 1 | 当前 |
| --- | ---: | ---: |
| 完整 GitLab Skill（保留开发／评审等工作流） | 11116 | 13440 |
| resolve 帮助 | 1654 | 1654 |
| execute 帮助 | 4219 | 4219 |
| 图文目标 Agent 投影 | 1949 | 2467 |
| 图文回执 Agent 投影 | 958 | 958 |

当前发现条目为 1082 字节。当前图文原 DTO／投影分别为目标 2351／2467、回执 807／958；文字为目标 1361／1339、回执 778／757。投影保留完整 input，比不含原始内容的回执 DTO 多 151 字节；目标增加必填依据和短工作流，不能用省略业务内容换取更小数字。指引／目标的必要增量用于真实轨迹中发现的空 body、CLI 信封猜测、跨回合重复 resolve、歧义处理和 unknown 恢复；没有新增正常路径的上传／候选／事项／固定详情调用。实际各次工具输出字节亦保存在证据中。

早期失败样本单列保留，均不计入通过矩阵：旧夹具输入格式、DSH 符号链接恢复误判、重复拼接 Room 图片链接、空 body、猜测 CLI flags、重复 resolve、长诊断截断误判、取消夹具清理阻塞及模型 HTTP 429。空 body 的真实失败样本依据一次准确反馈改为省略该字段，没有追加用户教程；最终正常矩阵为零格式试错。模型验收最终串行执行，未把限流／超时算作通过。

## 验证与开放边界

通过的检查（仅受影响范围，没有全仓或真实平台写入）：

```sh
go test ./internal/platform ./internal/platform/gitlab ./internal/platform/lark ./internal/platform/tool ./internal/agent/dsh -count=1 -timeout=4m

go test -tags=integration ./internal/room -run '^(TestGitlabImage|TestReferenceAction|TestActionDispatch|TestRoomActionImages|TestAgentCommandHelp|TestAgentJSON|TestHistoryMCP|TestHistoryImage|TestHistoryPage|TestHistoryQuery|TestHistory.*Budget|TestLarkActionAgentContract)' -count=1 -timeout=8m

go test -race -tags=integration ./internal/room -run '^(TestGitlabImage.*|TestGitlabToolEntryManagedWorkflow|TestActionsLegacyReceiptAndSchemaPreflight|TestActionsLedgerFailureNeverReplays|TestActionLedgerReadableDuringSessionRecovery|TestReferenceActionInternalHTTPWithoutCLI|TestReferenceActionInternalRetriesAndUnknown|TestLarkActionAgentContract|TestRoomActionImages.*|TestActionDispatch.*)$' -count=1 -timeout=5m

go test -race ./internal/agent/dsh -run '^(TestInputBridgeResumeConfigThroughStateSymlink|TestNormalResumeKeepsMemoryAndRefreshesEnvironment|TestExecutionTokenDoesNotChangeMaterialOrRecoveryFingerprint|TestInputBridgeCredentialsAndImplementationIdentity)$' -count=1

go test -tags=e2e ./internal/room -run '^(TestGitlabImageAgentCommandAccounting|TestGitlabToolEntryManagedWorkflow)$' -count=1
```

最后一轮 Room race 为 140.413 秒，通过跨进程矩阵、旧回执、落账失败、文字动作、原图 lease／最终派发和飞书合同兼容。DSH 完整包及其恢复 race 通过。原图／History／预算协议没有修改，既有定向回归继续通过；Agent 自身 History 读取的开销独立计入。

真实 Agent 复验使用已配置 DSH／模型及 Python 3、zstd，输出目录必须是新的绝对路径；两个 mode 串行运行：

```sh
TEAMX_GITLAB_IMAGE_AGENT_REAL=1 \
TEAMX_GITLAB_IMAGE_AGENT_MODE=managed \
TEAMX_INPUT_COMPARISON_CONFIG="$PWD/.teamx/config.toml" \
TEAMX_DSH_OUTPUT=/absolute/fresh/evidence-directory \
go test -tags=e2e ./internal/room -run '^TestGitlabImagesRealAgent$' -count=1 -timeout=30m -v
```

将 mode 改为 `default-assistant` 独立复验另一入口；不设 mode 则测试内部顺序运行两者。测试只在隔离 manager 注入同一正式图文实现，模型输出、CLI 实际请求及最终账本共同验收，不给自然任务补充命令或 JSON 修法。

阶段 3 的真实 GitLab 接口形态、发布、逐图下载 SHA256、通知回流和浏览器核对仍未完成；夹具和真实模型不能替代这些证据。生产目录／manager 的 GitLab 图文发现及执行继续关闭；未采用到日常服务或轮换日常 Session。
