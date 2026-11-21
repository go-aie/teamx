# GitLab 图文评论发送接入方案

状态：阶段 1～3 已完成；验证及边界分别见[阶段 1](implementation/gitlab-image-send-s1.zh.md)、[阶段 2](implementation/gitlab-image-send-s2.zh.md)、[阶段 3](implementation/gitlab-image-send-s3.zh.md)。阶段 3 已在用户指定的 RussellLuo／teamx-demo MR !12 完成有界真实 GitLab 验收，并同步正式发现／执行注册和用户文档。真实接口小验证复用这批成功样本，没有额外上传。日常运行服务和既有 Session 尚未采用本次代码；本次授权不扩大为其他对象或日常发送授权。

阶段 4 已实施：第 3.2 节补齐“用户提供完整 MR／讨论 URL → 核实并保存 target_ref → 发送”，同时用于纯文字和图文。全新成员托管链路、组件及定向 race、真实模型隔离验收、真实账号只读定位和两条单独授权的真实发布已验证。首次真实发送整轮的多余原生只读调用被审计拒绝，原报告保留 failed；指引修订后的隔离复验和原回执只读恢复分别留证，没有额外真实补发。详见[阶段 4 实施记录](implementation/gitlab-image-send-s4.zh.md)。日常服务未重启，既有 Session 采用情况未验收；本次写授权不扩为日常发送授权。

设计基线：2026-11-20 核查的工作区及本地 `bin/glab` 1.127.0（2e352a17e）；2026-11-21 补充核对飞书方案第 8.4 节及当前 Agent 接口。相关代码正在演进，实施前须复核本文列出的入口与依赖，保留其他任务的改动。

建议采用“共享基础、按渠道独立交付”：复用飞书已加入的 Room 原图准备、保留保护、完整动作记录、可信内容摘要和最终写前检查，GitLab 单独完成项目上传、Markdown 合成与评论核验。无需等待飞书真实开放，也不能把飞书隔离验证视为 GitLab 验收。

总体目标：Agent 看到的工具输入、输出和提示词尽量简短、准确、可直接使用，减少字段猜测、身份搬运、授权分支推断和重复调用，提高可靠性与稳定性。接口、正常返回、错误反馈和指引与图文功能一起设计、交付和验收；不能只缩短提示词或把复杂度转移成更多详情查询。必要的业务信息、授权、内容核验与恢复能力保持完整。

## 1. 目标与范围

用户在 Room 发布图片后，可以要求“把这段说明和上面的两张图评论到这个 MR”，或“把第二张图回复到这条讨论”。Agent 使用当前成员已绑定的 GitLab 用户账号，一次动作创建一条含全部指定图片的评论或讨论回复，返回准确 note ID 和可核实结果。

| 范围 | 本期安排 |
| --- | --- |
| 当前 Room 已发布且仍保留的人类图片附件 | 支持 PNG／JPEG／WebP，发送保存的原始字节，不做 OCR 替代、压缩或转码 |
| 已有托管目标中的 MR 普通评论、准确讨论回复 | 分别扩展 `object.comment`、`discussion.reply`；沿用当前目标、账号、Project ID、MR ID／IID 和完整 discussion ID 核验 |
| 单图、多图、正文加图片 | 使用 `body + images[]`；正文在前、有序图片在后，一次动作只创建一条 note |
| 含图授权 | 首期只支持直接 Room 人类指令及现有 policy token，不开放规则、持久指令、问答授权的含图发送 |
| 原有纯文字评论／回复 | 保留输入、授权、目标和重试行为；旧记录不迁移 |
| Issue、通用 Work Item、代码行评论、创建新的可解决讨论、内部评论模式 | 不随本次扩大托管能力；原生 CLI 已有功能继续按其现有合同使用 |
| 通知中的 GitLab／飞书图片、任意磁盘文件或远端图片 | 不作为本期新上传来源；需要时另做来源权限设计 |
| 完整 MR URL、带准确评论／讨论锚点的 URL | 阶段 4 新增目标解析入口，见第 3.2 节；阶段 1～3 不包含 |
| 裸 IID、项目名搜索、无绑定发送、跨 Room、批量目标 | 不纳入本方案；不从模糊描述猜测目标或账号 |

“支持普通评论”指沿用现有 MR notes 路径，不等同于新建 resolvable discussion。已有讨论回复必须留在准确线程，失败不得降级为普通评论。用户给出的 MR 尚无当前可用托管引用时，通过阶段 4 的 URL 解析建立引用。两个入口均不能为了发图伪造 Inbox 事项或改走裸 CLI 绕过账本。

本期不新增发送 UI、独立上传工具、媒体授权系统、上传恢复状态机或自动回滚任务。用户无需下载 Room 图片或提供服务端路径。

## 2. 当前代码与 CLI 能力

| 位置 | 已核实的现状 | 本期处理 |
| --- | --- | --- |
| [tools/gitlab/main.go](../tools/gitlab/main.go)、[工具手册](../tools/gitlab/README.zh.md) | `teamx-gitlab` 代理固定版本 glab，保留原生命令参数和结果 | 不重写代理，不给内部动作恢复旧 CLI 私有协议 |
| 原生 `glab mr note create` | 1.127.0 已有可重复的 `--attach`、`--reply`；`--attach` 与 `--unique` 互斥，能力标记为 Experimental | 记录可用性，保留既有直接任务路径；不能用帮助输出证明托管功能已完成 |
| [actions.go](../internal/platform/gitlab/actions.go) | 内置 MR 评论／回复只接受非空 `body`；普通评论最多两次尝试、允许不确定结果的有限重试，讨论回复一次 | 增加含图分支，纯文字行为保留 |
| [actions_http.go](../internal/platform/gitlab/actions_http.go) | `NoteAPI` 限定为 MR note 或原生讨论回复的单次 POST；不自动重放或跟随重定向 | 继续复用，不扩为任意方法／URL 客户端 |
| [uploads.go](../internal/platform/gitlab/uploads.go) | 已有上传引用解析和认证下载，没有新文件上传 | 增加独立、受限的项目图片上传函数 |
| [platform_gitlab_actions.go](../internal/room/platform_gitlab_actions.go) | 固定成员连接、核对账号和绑定、合并取消、按单次请求取得 OAuth 使用许可 | 在此装配 Room 原图、受限上传与最终派发检查 |
| [room_action_images.go](../internal/room/room_action_images.go)、[room_retained_read.go](../internal/room/room_retained_read.go) | 飞书阶段 1 已加入平台无关的原图集合准备、快照／对象 pin、执行身份检查及释放 | 直接复用，不新增另一套 GitLab 来源存储 |
| [reference_capability.go](../internal/platform/reference_capability.go)、[actions_store.go](../internal/room/actions_store.go) | 当前已有可选 `Inspection.ContentDigest`、完整输入比较、内容等价保护和 `checkActionDispatch` | 复用现有账本与最终只读检查，不复制摘要到指令依据 |
| [platform_lark_actions.go](../internal/room/platform_lark_actions.go)、[image_send.go](../internal/platform/lark/image_send.go) | 飞书阶段 2 已接通隔离图文动作，正式开放及真实接口证据仍待完成 | 复用平台无关行为，避免 GitLab 导入 lark 类型或发送器 |
| [actions_agent_view.go](../internal/room/actions_agent_view.go)、[agent_contract.go](../internal/room/agent_contract.go)、[cli_help.go](../internal/room/cli_help.go) | 飞书第 8.4 节已简化其 Agent 合同；当前正常返回投影明确限定 Lark，GitLab 尚未自动采用。公共请求解码、文件输入和字段诊断可复用 | 为 GitLab MR 评论／回复增加局部 Agent 投影与准确帮助，复用现有输入信封和 context，不重构全部平台 |

原生 CLI 能力示例，仅说明已有命令，不是本方案托管调用或本次执行记录：

```sh
glab mr note create 123 -R group/project \
  --resolvable=false -m "问题截图与修复效果" \
  --attach ./before.png --attach ./after.png
```

依据：[glab 1.127.0 对应文档](https://gitlab.com/gitlab-org/cli/-/blob/v1.127.0/docs/source/mr/note/create.md)。本次只检查本地版本／帮助和官方文档，没有执行真实上传或评论。

## 3. 托管传输选择与复用边界

本期选择：在现有内置 GitLab 适配器中补项目上传 HTTP 边界，评论仍经现有 `NoteAPI` 提交。不要把托管动作改为运行 `glab mr note create --attach`。

原因是当前内部动作已经独立于 Agent CLI：无 glab 可执行文件时也能完成托管文字评论。直接复用现有 HTTP、账号许可和取消机制，只需补上传原语；改用组合 CLI 会新增二进制依赖、临时文件和输出解析，并把“上传中失败”与“评论已提交但回执丢失”包在同一次进程调用内。`--unique` 也不能为附件评论提供幂等保障。

这不是重新实现一套 glab：本期只增加一个必要上传端点和现有评论的内容适配。原生代理仍可用于其既有任务；Agent 不得在托管失败后切换 CLI、账号或 API 补发。

| 归属 | 负责内容 | 不承担内容 |
| --- | --- | --- |
| Room | 当前成员／回合、可见源消息、准确原图字节、pin 生命周期、动作授权和冻结记录、最终派发检查 | GitLab Markdown、上传 URL、原生 note 协议 |
| platform 公共代码 | 既有动作 DTO／账本合同；必要时下沉一个纯内容摘要函数 | 媒体事务、平台分支、上传队列、通用富文本树 |
| GitLab 适配器 | 严格输入、目标检查、项目上传、受控 Markdown、note／线程核验、结果分类 | 读取或推进 Inbox／History 状态、解释人类指令、扫描本地文件 |

两渠道保持相同的 `images[].message_seq/attachment_id` 语义。允许各自保留很小的输入 DTO 和平台校验；不为消除少量结构重复建立新框架。当前内容摘要算法在 lark 包中，可把“version=1、准确 body、有序实际图片 SHA256 的规范 JSON 再取 SHA256”下沉为 platform 包内的纯函数，两适配器调用，必须保持飞书既有摘要结果不变。摘要不含平台上传 URL、账号或目标，后两者仍由现有动作身份单独约束。

### 3.1 Agent 最小可用合同

参考[飞书方案第 8.4 节](LARK-IMAGE-SEND-PLAN.zh.md#84-独立前置任务agent-接口与指引简化)，采用其接口与指引共同收敛的原则，按 GitLab 现有入口实施：

| 环节 | Agent 正常路径需要看到的内容 | 收敛要求 |
| --- | --- | --- |
| 入口与帮助 | 准确账号入口、托管 actions 命令、直接 Skill／帮助位置、当前可用状态 | 区分原生 CLI 代理与根 actions 托管入口，一次说明；不新增统一代理或要求 Agent 记忆两套同义调用 |
| 目标解析 | 已验证 target_ref、单份账号及项目／MR／准确线程信息、动态 input_schema、必填字段、可执行性／失败原因 | 不要求 Agent 手工包装原生身份；不重复输出完整 AccessContext、冻结证据和内部策略对象 |
| 含图依据 | `authority.type=human_reply`、真实 room_seq／reason、当前 policy_token、最多一次尝试 | 在输入 schema 相邻给出短小的含图要求；旧文字授权分支按需查看，不让 Agent 从通用授权状态树推断；不新增条件表达式语言或复制整份输入 schema |
| 执行请求 | 准确目标或已有事项 context、当前 turn_number、稳定 id、operation、完整 input、必要授权 | Room／成员由已有可信运行上下文提供，账号从选定目标与绑定核验；不让 Agent 回填整份身份、生成正文或上传 URL |
| 执行结果 | id、一个动作状态、尝试次数、准确账号／目标、原始 body 与有序附件引用、verified、已知 note ID／URL，以及必要版本或失败信息 | 每类业务信息表达一次；succeeded 才能为 verified=true，unknown 保留已知 ID。正常返回足以报告结果，不强制再读详情确认内容 |
| 详情与恢复 | `actions read --id ID` 的既有完整证据和旧回执 | 保持详情合同及旧记录可读；仅核查／恢复时展开，不能把正常判断必需的信息藏进详情 |

Agent 输出投影与存储 DTO 分离。给 `projectActionTarget`／`projectActionReceipt` 增加明确的 GitLab MR 评论／讨论回复分支，复用已有小型投影 helper；不能简单移除 Lark guard 并把所有平台套进 self／群授权语义。项目、MR 和完整 discussion 的必要标识不能因合并 scope 而丢失，精简后的目标展示也不能被误当成新的原生 locator 输入。正常返回不暴露上传进度、内部凭据代次或重复的完整原生证据；未开放时仍准确展示文字合同。

执行投影只处理有准确 GitLab Result 且 Policy 为空的三种终态。当前服务在 policy token 过期等情况下还会返回 `policy_changed` 和 `Policy`，可能没有 Result／Account；尝试前发现策略变化时也可能同时有 not_applied Result 和 Policy。凡 Policy 非空或不是上述可投影结果，都保留原反馈，不套成功／失败的图文结果，也不丢弃当前策略或修复信息。`actions read --id ID` 已通过 `actionQueryView(...,true)` 返回完整 detail 和原始 input；保持该恢复入口，不另加 --detail、恢复 API 或新的查询步骤。

主指引只保留“确认目标和内容 → 提交一条完整动作 → 按回执报告”，配普通评论／准确讨论回复示例，以及歧义澄清、整条单次提交、unknown 查原回执三项关键约束。结构和上限由动态 schema 提供；上传、OAuth、Session 内部过程留在开发说明，修复方法随对应错误按需提供。GitLab Skill 还承担开发、评审、待办等任务，只收敛图文相关部分，不删减其他工作流。

已有有效入口、目标合同或回执直接复用；不新增必需的图片 resolve、候选 get、matter ensure、持久指令保存或发送后的固定详情查询。服务端每次执行仍核验当前目标、账号、授权和内容。用户澄清后沿用已经明确的选择，同时采用当前 turn_number／policy_token；不能冻结跨回合请求或自动替换事项版本。

### 3.2 完整 URL 定位与托管目标建立（阶段 4，已实施）

目标：用户在普通 Room／默认助理中直接提供完整 GitLab 链接，即可向指定 MR 评论或回复准确讨论，无需事先订阅通知、接收事件、建立事项或绑定 Workspace。URL 是定位线索；服务端核实后生成的 `target_ref` 继续承担内部执行目标引用。新增入口同时服务原有文字与图文动作，不改发送器或扩大图片来源／授权范围。

**Agent 入口与返回。** 复用根 `actions resolve --tool ENTRY --request JSON`；`ENTRY` 必须取自当前成员 `tools list` 的准确账号入口。公共 `TargetLocator.URL` 已存在，只补 GitLab 实现，不新增工具或要求 Agent 查询 glab 后回填原生 ID。拟支持的完整解析请求如下，示例不是执行记录，账号入口及回合须替换为实际值：

```json
{
  "locator": {
    "url": "https://git-devops.opencsg.com/product/agentichub/teamx-demo/-/merge_requests/12"
  },
  "turn_number": 1
}
```

解析一次返回可直接执行的 `target_ref`、单份账号／项目／MR／准确线程摘要及现有动态动作合同。正常路径为一次 resolve 加一次 execute；已有有效 ref／合同时继续复用，不新增候选确认、原生身份包装、事项 ensure 或强制回执查询。返回 verified target 不构成发送授权；execute 仍按原有 policy、直接人类指令、当前回合及完整内容检查执行。

**链接范围与线程语义。**

| 链接 | 解析结果及限制 |
| --- | --- |
| `https://实例/组/子组/项目/-/merge_requests/IID` | 核实后生成 MR 对象 ref，提供 `object.comment`；支持多级 namespace，不把 IID 当全局 MR ID |
| 同一 MR URL 加 `#note_<正整数>` | 在该 MR 的 discussions 中准确找到 note，核实其所属完整 discussion；只有现有可回复线程才能生成 `discussion.reply` 目标 |
| 同一 MR URL 加 `#discussion_<完整原生ID>` | 读取并核实该 discussion 归属后生成线程 ref；原生 ID 不能截断、猜测或用 note ID 替代 |
| 不认识的锚点、孤立普通 note、删除的 note／线程、无法完整定位 | 返回具体不可用原因，不去掉锚点改成普通评论；用户明确改为 MR 普通评论后，可解析不带锚点的 MR 链接 |

只给 MR 链接却要求“回复某条讨论”时，Agent 须先澄清线程，不能自行挑选讨论；URL resolve 也不搜索“最近的讨论”。本阶段不创建新讨论、不把 `individual_note=true` 的单条评论转换为线程；既有代码行讨论仅按已支持的原生线程回复，不增加新建行评论能力。

用标准 URL 解析器校验 HTTPS、长度（最多 4096 字节）、origin、项目路径、规范正整数 IID 和完整锚点。允许规范化 host 大小写和末尾单个 `/`；首版不支持 query、额外视图路径、短链、用户信息、控制字符、反斜杠、点路径或含糊的重复编码，明确反馈，不靠字符串截取／路径清理猜目标。项目路径按段解码校验，再把完整 namespace/project 作为一个 API 参数转义一次；不得双重解码或把编码分隔符当作路径结构。实例必须与选定账号的既有 origin 一致，沿用现有不支持实例子路径的边界。

**服务端流程与开发落点。**

1. 在 `internal/platform/gitlab` 增加局部 URL locator 解析／核实实现（建议 `action_locator.go`），保持现有通知来源解析和已保存 ref 的重验路径。先校验链接，再通过现有有界 `ReadAPI` 依次 GET `/user`、`/projects/{转义后的完整项目路径}`、`/projects/{已核实项目ID}/merge_requests/{IID}`；核对账号、`path_with_namespace`、项目 ID、MR 全局 ID／IID 和完整规范网页路径。不能只沿用 `objectURLMatches` 的路径后缀检查，不能跟随项目迁移／跨实例重定向接受另一个目标。
2. 锚点需附加读取准确 discussion；note→discussion 映射复用或小范围提取 `references.go` 的分页和归属校验。直接 discussion ID 走单条读取；note 映射最多 10 页／500 条 note，单响应不超过 1 MiB，整个 resolve 共用 30 秒绝对期限。找到唯一准确映射后可停止继续翻页；分页失败、边界缺失、重复冲突或达到上限仍未找到，返回 incomplete，不宣称线程不存在。普通 MR URL 不扫描 discussions。
3. Room 在 `platform_gitlab_actions.go` 装配 `resolveGitlabActionTarget`，在 `platform_references.go` 的 `actionResolvers` 注册 GitLab。复用准确工具入口、`preparePlatformCallLocked`、`gitlabAccountRead`／逐次 OAuth 许可、取消与 Close；必要时只提取小型公共 admission／guard helper，不复制整套发送 session。显式 URL 无通知接收人，执行 AccessContext 的 Account／Receiver 均从已核实的当前绑定账号取得，不制造 SourceRef 或假通知。保留已有 ref 的通知接收账号检查。
4. 构造既有 `TargetResolution`：MR target 使用真实 Project ID 和 MR 全局 ID，已核实 IID 放入 alias；线程 target 加完整 discussion ID，不把用于定位的 note 填成发送 Message target。用既有 `saveActionTarget`／`targetReference` 持久化，不创建 followup unit、不改账本 schema 或引入 URL→ref 第二份索引。相同原生目标在同 Room／Membership 内保持稳定 ref；不同 Room／成员隔离，已有账号证据不能被另一个账号静默接管。
5. 每次远端读取前后检查当前 admission，保存 ref 前在提交临界区再次比较绑定、账号、凭据版本和回合，避免核实期间解绑／撤销后仍提交旧证据；执行阶段继续重新核验。网络读取不持有 manager／Store 锁，不向用户 URL 直接发送凭据；API 请求固定构造于绑定 origin，保留无重定向约束。失败不落可执行 ref、不上传、不发 note。
6. 复用 `actions_agent_view.go` 的 GitLab 投影；失败保留明确状态、reason_code、短原因及必要修复指引，区分链接不支持、实例不匹配、权限／凭据、404 未找到或不可见、限流和解析未完成。不存在的设置开关不能作为修复建议。同步 `cli_help.go` 的 URL 示例、公共字段诊断提示和 GitLab Skill／用户文档，移除“任意 URL 一律不能建目标”的过期说明，同时保留其他平台及旧 ref 合同。

原生接口依据：[项目 API](https://docs.gitlab.com/api/projects/)、[MR API](https://docs.gitlab.com/api/merge_requests/)、[讨论读取](https://docs.gitlab.com/api/discussions/#list-all-merge-request-discussion-items)。文档只说明读取协议；目标实例的锚点及线程响应仍须通过阶段 4 验证。

**阶段 4 必需验证。** 从没有预存 action target、通知来源或事项的全新成员数据开始，通过正式 CLI／HTTP／manager 入口覆盖“URL→持久 ref→execute”：MR 文字评论、MR 单图／图文、准确线程图片回复；用固定目标 ref 的旧夹具不能替代。覆盖多级项目路径与转义、错误账号／实例、错误项目／MR 响应、锚点不降级、分页上限、超时／撤销与提交竞争，并保留 Lark、通知来源、原文字和图文重试／unknown 回归。重复解析、重开 Store 和同 ID 回执恢复均不新建事项或重复发送；新解析不改变 Inbox／History 消费状态。

真实 Agent 以自然请求“把这张图评论到 <完整 MR URL>”和“把第二张图回复到 <线程内评论 URL>”完成任务，禁止测试预注入 ref／原生 ID 或追加命令教程；必要的工具发现、History／policy 读取单独计数。真实 GitLab 写验收需重新明确账号、目标、正文、原图和写入额度；独立回读、图片字节／顺序及线程归属沿用阶段 3 标准，不复用旧写授权。分别记录组件、托管装配、真实模型／平台结果和运行服务／Session 采用情况；没有真实写授权时可完成前置开发与只读核实，但阶段 4 不标为整体验收完成。

## 4. 输入、目标及授权合同

继续使用既有 `teamx actions execute --request …` 信封和操作名；GitLab 的 `teamx tools run <实际入口> …` 当前仍是独立 CLI 代理，不把它误写成飞书式 `tools run … actions execute`。下面仅是拟新增的 `input` 部分，不是完整调用：

```json
{
  "body": "问题截图与修复效果如下。",
  "images": [
    {"message_seq": 120, "attachment_id": "img_0123456789abcdef0123456789abcdef"},
    {"message_seq": 121, "attachment_id": "img_fedcba9876543210fedcba9876543210"}
  ]
}
```

`object.comment` 和 `discussion.reply` 使用相同内容输入。Agent 沿用当前 tools list 的准确 GitLab 入口、已保存的 target_ref、事项版本和现有授权字段；有事项时不能省略其原有信封要求。`authority.room_seq` 指授权发出评论的人类指令，不是图片来源序号；图片可以来自更早的多条消息。

- `body` 与 `images` 至少提供一个。body 若提供，须非空、有效 UTF-8、无 NUL，最多 4096 字节；正文沿用 GitLab Markdown 语义。
- images 若提供，须为 1～4 个准确引用，不接受 null、空数组、额外字段或重复附件 ID。每个元素只含正整数 message_seq 和合法附件 ID，不把模型 content_index、页面内序号当作消息序号。
- 图片上限沿用已实现来源能力：单张 2 MiB、合计 4 MiB、单张 16 Mi 像素；图片顺序就是数组顺序。平台更严格时收窄并准确报错，不自动压缩或拆评论。
- 不接受磁盘路径、URL、Base64、GitLab 上传路径／ID 作为图片来源。不把正文中的既有链接下载为上传材料，也不把它们算作本动作提供的 Room 附件。
- JSON schema 负责结构说明，运行时负责字节长度、重复、原图归属及实际格式；仅有 maxLength 不能替代 UTF-8 字节限制。

含图时使用 `DirectRoomInstructionOnly=true`、`MaxAttempts=1`、`RetryUnknown=false`，绑定证据仍为空，沿用业务授权及现有 policy token。Room 人类指令必须明确覆盖目标和选定内容；来源消息有图片不等于允许外发。代码层使用准确人类来源和完整冻结输入限制派发，不声称能够机械证明所有自然语言授权语义。

纯文字仍允许原来的规则／指令／问答入口。含图不能把原有只描述文字的持久指令当作图片授权，不修改 `followupIntent`、`followupReplyBasis`、持久指令或问答协议。图片专用 grant、全局“允许外发附件”授权及新的 policy 状态均不需要。

### 4.1 在落账前选定含图策略

当前 `ReferenceActions` 按操作返回静态 `ActionSpec`，服务在 Inspect 前就检查授权，并按同一个 Spec 持久化和决定重试。不能只在 Execute 中看到 images 后返回不重试：那样可能保留过宽授权与错误的账本策略。

采用局部装配方式，不增加公共动态策略协议：

1. 在 `service.executeReferenceAction` 已通过 `referenceActionEvidence` 后，为既有私有 `roomActionContext` 增加 `Input string`，值为 `string(actionInputJSON(q.Input))`。使用不可变字符串，避免向 lease 保存的上下文引入可修改的 RawMessage 切片；保留先查旧回执的快路径，不把它放进 Agent／MCP 信封，也不包含图片字节。
2. GitLab preparer 在执行上下文存在时用同一严格解码器解析输入。纯文字选择原动作实现；含图选择图文实现，并在服务取得 cap 之前设置上述授权与重试 Spec。每次调用构造自己的 Actions map／Spec，不修改 catalog 或其他请求共享的能力。非法输入直接报错，不能退回纯文字分支。无需扩展 preparer 签名或增加公共 SelectSpec 回调。
3. resolve／能力发现没有执行 Input：已开放时展示兼容文字与图片的联合 schema，并按第 3.1 节直接说明含图必填依据与一次尝试限制；不能把整个操作都展示为只支持直接指令而隐藏原文字能力。这里不决定某次执行的策略，也不取原图或上传。
4. `beginReferenceActionWithBasis` 保存实际选中的 Spec；`referenceActionAttempt` 和 `checkActionDispatch` 使用原机制。新建的含图账本必须记录一次尝试与直接指令约束，不能事后修改 Spec。

此路径必须覆盖真实 manager／service 装配测试；纯文字、飞书及旧记录没有新上下文读取要求。私有 Input 只用于选路，不能替代账本中的完整 Request.Input 或最终比对。

含图解码不能调用当前 `actionInput`：它在进入目标解析前强制要求非空 body，会拒绝合法的纯图片输入。图文分支解码后直接复用 `actionNativeTarget` 和 `inspectAction` 的账号／目标核验；原文字分支保持 `actionInput`。纯图片账本仍保存原始无 body 输入和完整图片摘要，不能塞占位文字来通过旧校验。

### 4.2 最少字段与按需帮助

已知有效 target_ref 的含图新动作使用现有七个顶层字段：target_ref、turn_number、id、operation、input、policy_token、authority；authority 只需 type=human_reply、room_seq、reason。字段值来自当前运行时、真实人类指令及已有目标，不由服务端猜测或补造。无事项关联时不要求 object_ref／unit_ref／unit_version。

有关联事项时，优先复用已有 `context:{object_ref,unit_ref,unit_version}`，不同时提交顶层事项字段；目标与该事项一致时，按现有合同由服务端取得 target_ref，无需重复填写。若显式目标另有合法语义，仍遵守当前目标匹配检查，不能为减少字段改变作用范围。

帮助保留普通评论和准确讨论回复各一个完整含图请求示例，直接采用当前 `agentActionRequest`／`agentActionAuthority`，不使用内部 RuleID 等存储字段。两例共用 body + images 结构；operation 和准确 target_ref 决定普通评论或讨论回复，不让 Agent 另传原生 project_id／mr_iid／discussion_id。当前动态 schema 已包含结构和上限时，主 Skill 不再重复整份 schema。

复用 `--request`／`--request-file` 及 stdin 的现有读取和校验合同；文件由 Agent CLI 所在进程读取，不开放服务端任意路径。输入错误沿用公共诊断，准确定位字段路径和预期结构，例如 `input.images[0].attachment_id`；权限／账号／目标／版本错误给出相应原因及下一步，不回显敏感值、不返回整套无关修复教程。修复仍经过原严格校验，不能忽略未知字段来换取少报错。

## 5. 执行链路与实现指导

```text
Agent 定位准确 MR／讨论、正文和有序 Room 附件引用
  → 现有托管动作入口，按输入选定文字／含图 Spec
  → 只读 Inspect：核验账号、Project、MR／discussion，准备全部原图与摘要
  → 现有账本保存完整 Request.Input、原授权依据、摘要及含图 Spec
  → 逐张上传到准确 MR 所属项目
  → 合成为唯一 Markdown 正文，复核目标与完整动作
  → 唯一一次 note POST
  → 按 note ID 独立 GET；回复额外核验准确线程
  → 持久结算一条动作回执，释放全部来源 pin
```

### 5.1 来源与生命周期

在 `platform_gitlab_actions.go` 内组合 `svc.prepareRoomActionImages`，每个动作 session 只准备一次完整来源集合，Inspect／Execute 重用同一 lease；后续请求的 body 或有序引用变化须拒绝。不要调用 History MCP、图片材料接口或新建 inputSession 取原图。

当前 lease 已核对原生人类消息、可见性／可投递性、附件上传者与保存关系、SHA256、MIME、尺寸、总量，以及成员／服务／回合／Session 代次。保持来源和对象 pin 直到 session.Close；正常、失败、取消和落账错误都释放。图片来自当前 Room，无通知图片或 History 缓存兜底。

上传前使用 lease.Check 与当前 GitLab 绑定／调用取消检查；不能在每张图前重新扫描全部 Room、动作或事项。不得在网络请求期间持有 Store.mu、attachmentLife 或 manager.mu；服务已有 changes.RLock 生命周期由现有动作所有者负责，不递归加锁。

### 5.2 项目上传边界

新增 GitLab 包内受限上传函数，建议放 `image_upload_http.go`，避免与已有“下载 Upload”的类型混淆：

- 仅允许 `POST /projects/{verified_project_id}/uploads`，Project ID 来自已核验目标，不能由 Agent 单独指定；MR 属于 fork 场景时也是该 MR 所属项目，不猜 source_project_id。
- `multipart/form-data` 的 file 内容使用 lease 中的实际字节。使用内部生成的 `image-1.png`／`.jpg`／`.webp` 等安全文件名和真实 MIME，不透传用户文件名；可用有界内存 buffer，无需落盘或增加临时文件管理。
- 沿用当前 HTTPS origin、认证设置、受限 HTTP client、取消、脱敏和响应大小限制。每次调用最多 30 秒，禁重定向，禁业务重试；不因 307／308、401 或 OAuth 修复重放 POST。
- 通过现有 `acquireGitLabAccess` 为每次上传单独取得同账号短期凭据许可，直到完整读取／关闭响应才释放。可复用现有续期机制，但账号、连接代次不允许切换；不把令牌放进 argv、日志或回执。
- 校验成功响应的 url／可用 full_path：同 origin、准确项目、合法上传 secret 和预期安全文件名，不含查询、片段、路径穿越或 Markdown 注入字符。尽量复用 `ParseUpload` 的路径核验；url 与 full_path 同时存在时须指向同一资源。不要要求新版才有的 upload id，也不要把 upload id 当 note ID。
- 不直接信任返回的 markdown 字符串。根据已验证的路径自行构造图片引用，防止响应附带其他正文或目的地。实际实例不支持所选路径时明确失败，不尝试另一个项目或端点。

官方合同见[项目 Markdown 上传接口](https://docs.gitlab.com/api/project_markdown_uploads/)。上传权限继续依赖当前 OAuth 及项目实际权限；不增加飞书式图片 scope 或新的 OAuth 流程。上传权限缺失仅影响含图请求，不阻断已有文字能力。

### 5.3 Markdown 合成和长度

固定生成“准确 body（若有）＋空行＋每张图片的 Markdown 引用”，图片标签使用内部固定名称，例如 `![image-1](已验证的上传路径)`，各图片间空行分隔；图片之外不添加未经要求的说明、署名或重试标记。纯图片生成只有图片引用的一条 note。

区分两个限制：输入 body 仍最多 4096 字节；最终生成的 note 正文单独限制为 8 KiB，包含全部引用。当前 `NoteAPI` 复用 `actionText`，其 4096 字节检查不能直接照搬到生成正文。保留 `NoteAPI` 函数类型，将 `NewNoteAPIWithCredential` 的现有实现下沉为一个私有、接收固定正文上限的构造 helper；旧工厂传 4096，新增图文工厂（如 `NewImageNoteAPIWithCredential`）传 8192。共同复用 JSON 构造、认证、单次 POST、响应读取、脱敏和 `noteError` 分类，不复制一套发送器。正文非空／UTF-8／NUL 校验可提取为带上限的纯函数，`actionText` 仍固定 4096；它还用于项目路径等其他字段，不能整体放宽到 8192。正文上限由内部工厂选择，不作为 Agent 参数。

上传前用受控占位路径检查合成结构，上传后按真实路径再次检查最终字节长度及全部图片节点。复用仓库已有 goldmark 解析能力，确认追加部分的图片节点、路径及顺序真实存在，避免正文未闭合代码块或 HTML 块吞掉图片；不能自动闭合、截断或改写用户正文。占位路径只用于本地检查，绝不发送。只用一个受限构造／检查 helper 分别处理占位路径和真实路径；占位标识须与用户正文区分，不能拿正文原有的图片节点冒充追加成功。不建立 Markdown 渲染器、格式修复器或 GitLab 扩展语法兼容层。

图片 AST 检查不能证明正文没有额外业务动作。[GitLab quick actions](https://docs.gitlab.com/user/project/quick_actions/) 可随评论保存执行；图文分支在任何上传前单独拒绝形如 `/close`、`/assign …` 的独立命令行。首期逐行用 `strings.Fields` 取得首个 token，若完整匹配 `^/[A-Za-z][A-Za-z0-9_-]*$` 则拒绝；这样兼顾 CRLF／Unicode 空白，普通 `/src/file.go` 路径不命中。不枚举命令或模拟平台执行语义；代码块中的同形独立行也保守拒绝，准确提示可改为行内说明，不能自动删除／转义后发送。此限制仅用于新增含图分支，旧文字合同不变，关闭／指派等业务需求仍按其原动作授权处理。

最终内容使用本次已验证上传引用，不接受 Agent 直接提交原生 Markdown payload。全部上传成功、完整结构检查通过后，才允许调用 note POST；任一失败不发布文字或图片子集。

### 5.4 最终授权与 OAuth 上下文

当前 GitLab `requestContext` 从 callCtx 派生，不会自动保留 Execute 期间才加入的 `actionDispatchCheckKey`。需要像现有 Lark merge 一样显式传递最终检查值；单测不得靠手工注入绕过实际合并链路。

最终 POST 前依次核对 lease／当前绑定、只读目标检查、完整动作派发检查。复用既有 `checkActionDispatch`，一次核对准确 ID、Request.Input、账号／目标、摘要、稳定幂等键、当前回合及直接指令／policy，不增加 Attempts 或扫描所有动作。检查须在取得本次 note 使用许可后、实际调用 HTTP 发送前完成；若检查或许可取得失败，明确标记请求未发送。

派发回调接收的必须仍是原 `TargetActionRequest`，其中 Input 为冻结的 body + images。最终 Markdown 只作为 `NoteAPI` 的 body 参数传递，不能先用生成正文替换 request.Input 再检查，否则会与账本必然冲突。完整内容摘要相同、实际 Markdown 和本次上传映射相符，是组合层的检查职责；共享检查不解析 GitLab Markdown。

原图摘要来自 body 和实际有序字节，不因上传产生新 URL 而改变。最终 Markdown 是该冻结内容在准确项目中的派生结果，组合层必须核对正文、张数、顺序和本次上传映射；不把最终 Markdown 反写 Request.Input，不增加上传 URL 的第二份持久授权副本。

沿用凭据撤销时取消后续调用、已知写结果仍持久结算的规则，不因写后解绑抹掉结果。上传和核验均使用同一个动作所准入的账号。当前 CLI `actionClientTimeout` 是 3 分钟，不能把服务端工作期限同样用满 180 秒。含图 session 采用固定 150 秒总期限，在 GitLab preparer 建立一次，涵盖 Inspect、上传、POST 与回读；所有合并上下文都保留这个绝对 deadline，不逐张或在 Execute 中重新起算。每次网络请求仍最多 30 秒，受更早调用／回合 deadline 限制；为落账和回传留出余量，但不保证客户端中断时一定收到回执。纯文字及公共 CLI 超时不变，不增加配置项或请求预算调度器。

### 5.5 结果核验

note POST 返回后先从可完整解码的响应提取正整数 note ID，再验证响应；即使后续失败，也尽可能把已知 ID 留在 unknown 回执。当前文字 Execute 直到所有检查成功才填 ResultID，不能把其整段 Execute 原样包一层后用于图文；只复用其中目标和 note 校验函数，图文编排在取得 ID 后立即设置结果字段。ID 只是远端返回的线索，身份或内容未核实时 verified 仍为 false，不把它当成成功证据，也不从截断 JSON 中猜 ID。随后通过准确项目／MR 的 notes/{note_id} 独立 GET，核对非 system note、作者 ID／已验证账号、MR ID／IID、完整生成正文及全部有序图片引用。讨论回复还须从准确 discussion GET 确认该 note 属于目标线程，不用普通 note 的存在替代线程归属。

正常路径信任经过边界校验的本次认证上传响应及其精确引用，不逐次重新下载全部图片。真实验收必须独立下载最终引用并与 Room 原图比较 SHA256，验证平台是否保留字节及路径关系；这与运行时完整 note 回读分开记录。

默认按最终 Markdown 准确比较，不 trim、忽略丢图、调整顺序或使用“差不多相同”的匹配。实例存在正文换行等规范化时，只能依据前置真实小验证补充狭窄、可测试的等价规则；未经验证的变化记 unknown。成功只表示准确评论已经确认，不表示他人已读、MR 已批准或图片永远可访问。

## 6. 幂等、unknown 与失败语义

所有外部上传都在现有写前落账成功后开始，Inspect、resolve、tools list、帮助均不得上传。一次含图动作最多一次最终 note POST，没有平台原生幂等保证；现有稳定动作 ID 和写前落账提供本地约束，不能宣称远端 exactly-once。

| 边界 | 整条评论的结果 |
| --- | --- |
| 输入／来源／授权／目标／本地合成预检失败 | 拒绝或 not_applied；无上传和 note POST |
| 第 N 张上传失败或响应无法核验，尚未调用 note POST | not_applied；前面或本次上传可能留下未引用项目附件，但评论未发送 |
| 全部上传后，最终合成、绑定、lease、授权或许可失败 | not_applied；不发文字或图片子集，不声称远端附件已删除 |
| note POST 已交给传输，超时、中断、5xx 或非可信响应 | unknown；含图分支不继承文字的 transient 重试 |
| 原有受限写边界已明确证明请求未发出／被拒绝 | 沿用其可靠 not_applied 分类；不单靠通用 CLI 退出码判断 |
| 有 note ID，但独立回读、正文、作者、目标或线程归属不符 | unknown，保留已知 ID；不重发 |
| 准确 note 与全部派生内容核验通过 | succeeded，一个 note ID、一条回执 |
| 写前落账后崩溃／结算失败，不能证明是否提交 | 保守保持 unknown；重启只查询原记录和远端，不恢复上传或发布 |

同 ID 重查返回原回执，源图清空也不重新准备；同 ID 改正文、替换／增删／重排图片须冲突。原有完整输入、可信摘要、来源及入口的 unknown 保护全部保留，不能改为仅比较 caption 或仅限同一操作。摘要不是永久去重键，也不能自动识别所有改词／拆图变体；Agent 指引仍须禁止绕过不确定动作。

not_applied 后若用户明确要求重试，先读取原结果、修复原因，再对完整消息建立新动作；不在同 ID 下复活执行。unknown 不自动转成功、不自动补发；有 ID 时可独立只读核实，没有 ID 时报告无法确认，不把扫描到的一条相似评论认作当前动作证据。首期不新增人工改账或后台核对服务。

上传 URL 和暂存字节仅随动作 session 生存，不持久化上传进度。失败可能留下未引用附件，明确报告这一上传副作用；首期不自动删除，不增加 DELETE 授权、清理队列或跨项目资源回收。

## 7. 改动位置与不变边界

| 文件／模块 | 必要改动 |
| --- | --- |
| `internal/platform/gitlab/image_actions.go`（建议新增） | 含图输入／schema、准备内容、平台校验、Markdown 构造、只读 Inspect 和含图 Execute；复用现有目标／note 核验 |
| `internal/platform/gitlab/image_upload_http.go`（建议新增） | 固定项目 multipart 上传、响应路径核验、有界请求及错误分类 |
| [actions.go](../internal/platform/gitlab/actions.go)、[actions_http.go](../internal/platform/gitlab/actions_http.go) | 提取最少必要的既有 note 校验／HTTP 复用点，提供受限生成正文路径，保持旧文字合同 |
| [platform_gitlab_actions.go](../internal/room/platform_gitlab_actions.go) | 按可信输入选策略，组合来源 lease、逐次 OAuth 许可、上传与最终检查，传递派发上下文并统一 Close |
| [room_action_images.go](../internal/room/room_action_images.go)、[followups_actions_service.go](../internal/room/followups_actions_service.go) | 私有执行上下文增加只读 Input；不改变原图准备逻辑／preparer 公共形状，不增加动态策略框架 |
| `internal/platform/action_content.go`（建议新增）、[lark/image_send.go](../internal/platform/lark/image_send.go) | 如需抽取，只有两渠道共同调用的纯摘要函数，保证飞书摘要兼容；不移动其发送器和平台类型 |
| [app/platforms/builtins.go](../internal/app/platforms/builtins.go)、[platform_references.go](../internal/room/platform_references.go) | 当前目录注册以 `gitlab.ReferenceActions(nil,nil,"")` 提供发现 Spec；先隔离注册图文实现，真实验收后同步目录／发现和执行路径，不能只开 schema 或只换 executor |
| [actions_agent_view.go](../internal/room/actions_agent_view.go)、[actions_service.go](../internal/room/actions_service.go)、[action_execute_service.go](../internal/room/action_execute_service.go) | 扩展 GitLab MR 评论／回复的正常解析及回执投影，保持完整内容、准确线程和未知结果；根托管入口使用同一正式投影，不建立第二条发送实现 |
| [agent_contract.go](../internal/room/agent_contract.go)、[cli_help.go](../internal/room/cli_help.go)、[request_diagnostics.go](../internal/room/request_diagnostics.go) | 优先复用已有请求／context、文件输入、帮助和字段诊断，仅补图文相关说明及准确错误；不改公共存储协议或重做 CLI |
| [GitLab Skill](../internal/platform/tool/skills/gitlab/SKILL.md)、[GitLab 使用说明](gitlab.zh.md) | 最短图文工作流、两个准确示例、动态 schema／按需修复及 unknown 指引；保留其他 GitLab 工作流 |

inbox／history 的 Agent 协议、seq／attachments 投影、消费与读取证明、页重放、模型材料、图片预算和协调器均不修改。Agent 已能从正式读取／History 取得准确消息 seq 和附件 ID，不新增必需的附件 resolve 调用。

原有 `actions_store`、共享指令／问答结构和 OAuth 授权模型原则上没有新增结构需求；复用飞书已加入的能力。若实施发现需要修改上述协议、增加新账本 schema 或使内置动作依赖 glab，应先回查装配方式，不能直接扩大改动面。

本方案所有“已加入”只描述当前工作区代码，不代表对应飞书或 GitLab 功能已发布、真实验收或可供日常使用。公共基础尚未稳定时先完成其已有测试，不复制成 GitLab 私有版本；GitLab 可在基础稳定后独立实施，无需等待飞书生产开放。

### 7.1 本次代码对照后确定的实现边界

| 接缝 | 已确定的落点与限制 |
| --- | --- |
| 文字／含图策略选择 | 在既有私有执行上下文携带不可变 Input，GitLab 局部工厂返回本次 Spec；不新增公共选择器、操作名或策略状态 |
| 纯图片与目标核验 | 独立严格解码，复用 `actionNativeTarget`／`inspectAction`；不通过假 body 绕过旧解码器 |
| 4096／8192 字节正文 | 一个私有 HTTP 构造 helper、两个固定上限工厂、同一个 NoteAPI 类型和错误体系；不修改其他使用 actionText 的字段 |
| 最终派发 | 合并及 OAuth 派生上下文保留回调与 session 的 150 秒绝对 deadline；回调检查原始输入，Markdown 单独传给原生写入函数 |
| 回执投影／恢复 | 三种终态精简，policy_changed／Policy 等早期反馈完整保留；现有 actions read 已提供原 input 和 detail，恢复不调用 prepare／resolve／上传 |
| 未知结果的 ID | 图文编排提前保存可解码的 note ID，后续失败仍返回 unknown＋ID；不改共享结果 schema，不自动核实改账或重发 |
| Markdown／quick action | 复用现有解析器做占位／真实引用检查，另作保守命令行拒绝；不复制通知图片解析的去重／截断规则，不模拟完整平台语法 |

上述均已能根据当前代码确定，不留到实施时再选择另一套架构。平台实际响应、路径或正文规范化仍按有界真实接口小验证确认；不能用本地代码分析代替真实接口证据。

## 8. 实施阶段与停止点

不重复飞书的原图准备阶段，也不把上传、授权、评论、回执分别作为可开放阶段。第 3.1、4.2 节的 Agent 合同属于每阶段的完成条件，不另设后置接口简化阶段。

| 阶段 | 完整行为与验证 | 停止点 |
| --- | --- | --- |
| 1：隔离托管动作与最小 Agent 合同 | 复用来源／摘要基础，完成输入选策略、项目上传、Markdown、一次 note POST、独立回读、线程归属、最终授权、幂等及基本失败分类；同期完成动态 schema／必填依据、正常返回投影、字段错误、帮助和两类完整示例。通过实际 manager／service／CLI 与 HTTP 夹具验证两种操作 | 合同可直接用于下一阶段，不靠 Agent 拼内部对象；只在隔离实例注入同一正式实现，已有生产文字仍可用，含图尚不开放 |
| 2：真实 Agent 的简洁使用与恢复 | 按最终合同收敛图文 Skill 和修复指引；普通托管 Room／默认助理中的真实 DSH／Agent 选图、定位 MR／线程，只提交一条完整动作。验证调用成本、无格式试错、跨回合复用，并补跨进程重启、并发、取消、旧记录及读取不回归；平台可用夹具 | 自然任务无需补充命令／JSON 教程；完成第 8.1 节成本与可靠性条件。若暴露接口负担先修正阶段 1 合同并定向复验；不把 Agent＋夹具视为真实 GitLab 成功 |
| 3：真实 GitLab 验收与开放 | 使用阶段 2 已验收合同，对明确授权账号、项目、MR／线程和准确内容完成真实 Agent 发布；独立回读 note、逐图下载比对，验证回执重查和回流。通过后同步正式发现／执行注册与用户文档 | 不用真实写验收临时补接口教程；未通过模式保持关闭。代码验证、能力开放和日常服务／Session 采用分别记录 |
| 4：完整 URL 定位与直接任务发送（待实施） | 按第 3.2 节新增 GitLab URL resolver，复用正式 resolve、持久 target 和既有文字／图文 executor；同步帮助、Skill、错误指引与用户文档。从无通知／事项／预置 ref 的全新会话验证 MR 和准确线程的完整链路，完成必要定向回归及有界真实 Agent／GitLab 验收 | 交付“贴链接即可交办”的完整入口，不以 API 能读取或预置目标发送成功代替。URL 解析不授予写权限；真实写授权、验收及日常采用分别记录，未完成门禁明确保留 |

阶段 1 开头前置有界真实接口小验证，尽早确定目标实例上传响应、引用路径、Markdown／换行和 note 回读形态；可在明确授权范围内调用原生 glab 或受控 API。它只提供原生接口证据，不另设阶段，也不替代阶段 2 的真实 Agent 或阶段 3 的完整链路验收。没有真实写授权时仍可推进夹具开发，原生接口合同须标为待验证。

阶段 1 优先用同一隔离托管实现验证第 7.1 节接缝，再补完整矩阵：纯图片经过实际入口；满 4096 字节 body 追加图片后能发布；旧文字仍拒绝超限正文；policy token 过期零上传且返回 Policy，带 Result 的策略反馈同样不被投影吞掉；最终检查收到原输入；回读／线程核验失败保留 note ID；多次网络调用不重置 150 秒总期限；未闭合 Markdown 和 quick action 在上传前拒绝。期限测试使用已有可控取消／deadline 夹具，不真实等待 150 秒。此顺序属于阶段 1 内部开发顺序，不新增阶段、功能开关或只会发送的临时版本。

实施及验收记录写入仓库现有 `docs/implementation/` 约定，长期代码／测试按行为命名。隔离能力使用现有测试注入方式，不新增阶段配置、持久功能开关或另一条生产实现。

### 8.1 Agent 成本与完成条件

阶段 1 留存改动前的文字／目标／帮助样本及新图文合同的字节数、字段和调用路径；阶段 2 按同一业务场景、尽量相同模型条件记录真实轨迹。GitLab 没有已交付图文旧链路时明确标记“无旧图文基线”，不伪造前后性能结论；可比较同一动作内部 DTO 与最终 Agent 投影，分别报告新增必要图片内容和去除的重复字段。

| 已有条件 | 正常业务调用目标 |
| --- | --- |
| 已有有效目标及当前动作合同、图片引用和必要依据 | 一次 execute；不要求再次 resolve、保存 instruction、ensure matter 或固定回执预查／后查 |
| 已有托管引用，但首次需要取得／刷新动作合同 | 一次 resolve 加一次 execute；不增加候选 get、图片解析或复制原生身份的步骤 |
| 阶段 4：已有准确账号入口、完整支持的 URL、图片引用及必要依据，但无托管引用 | 一次 URL resolve 加一次 execute；不预建事项、通知或先用 CLI 查询原生 ID |
| 目标、来源或授权有实际歧义／变化 | 必要澄清／读取单列；澄清后保留已确认选择，采用当前回合和有效版本，不重复整条发现流程 |

阶段 1～3 的计数基于既有托管目标范围；阶段 4 纳入一次 URL 定位。不能省掉尚未完成的来源解析、业务必需读取、事项 requires_read 或当前授权取得。当前 policy_token 等必需状态不在已知上下文时，其真实读取成本单独列入总数；不把必要前置条件假设成免费。上述调用目标不减少执行端的任何重新核验。

完成条件：

1. 普通 Room／默认助理均以自然请求覆盖普通评论与准确线程、单图／多图／图文、跨回合来源及恢复；测试不得追加 JSON 层级、字段修法或专用命令教程帮助过关。
2. 正常路径无字段猜测或格式试错；故意构造的字段错误能依据一次准确反馈修正。可用最终回执直接说明“哪个账号向哪个 MR／线程发了哪些完整内容、是否核实”，unknown 可取得已知 note ID 并只读恢复。
3. 记录全部工具调用、业务 CLI 调用、必填字段、格式错误、帮助、发现、policy／事项／History 读取、本地报告和恢复查询。一次工具调用包含多条 CLI 命令时分别计数，不能通过打包 shell 命令隐藏成本。
4. 记录主指引、相关帮助、发现条目和正常返回的 UTF-8 字节数；消除重复及与当前任务无关的说明。若明确的含图授权／错误信息带来必要增量，说明原因，不为绝对篇幅指标删掉完整内容、精确目标或恢复依据。
5. 原文字授权、旧回执、权限撤销、取消、完整内容冻结、幂等、unknown 和重启查询继续通过；阶段 2 完成前收敛发现的问题，不能以“接口能发出”或组件测试代替 Agent 验收。

飞书第 8.4 节已有公共解码／帮助、文件输入及无关底层可靠性证据可按实际未变范围复用；GitLab 的新投影、准确线程、条件授权和真实 Agent 轨迹必须单独验证。只重跑受影响场景和必要回归；改动共享投影 helper 时补飞书兼容检查，不复制其全部验收矩阵。

## 9. 验证清单

### 9.1 组件与托管装配

- 输入：旧 body、纯单图、纯多图、图文、跨多条源消息的顺序；空值、null、非法字段、路径／URL／上传 key、重复、超量和整数精度拒绝。
- 来源复用：已有 `TestRoomActionImages*` 保持通过；正式消费后、text-only 或模型识图额度耗尽仍能使用保留原图。新动作遇已失效对象拒绝；旧成功动作仍可查回执。
- 路由策略：含图在 Inspect／落账前即为直接指令和一次尝试；规则、持久指令、问答／message_refs 无上传地拒绝。纯文字维持原授权及重试，旧账本可读取；发现阶段不要求执行上下文。
- Agent 合同：发现／解析明确当前开放状态、动态 schema 和含图依据；目标与完整线程标识准确，正常回执含完整原始 input、单份结果及已知 ID。详情能恢复旧证据；context 可复用且不重复提交事项字段；共享帮助、文件输入及字段诊断一致，严格校验不放宽。
- HTTP：准确项目、账号／实例、multipart 字节／MIME、无令牌输出；跨 origin／项目响应、非法路径、坏 JSON、超限、重定向、取消和逐次凭据许可。缺 glab 可执行文件也能完成托管链路。
- 内容：中文／换行／Markdown、纯图片、安全名称、正文 4096 与生成正文 8 KiB 边界、代码／HTML 块吞图、追加图片缺失／乱序。上传返回任意 markdown 不得原样执行；预检零上传，最终构造失败零 note POST。
- 一条完整动作：全部图片上传后恰好一次 note POST；第 N 张上传失败，note POST 为零；caption 或部分图片不得降级发布。
- 授权时序：真实上下文传递；上传期间解绑／撤销／回合或 Session 变化停止后续请求；最终许可取得后、POST 前完整检查失败为未发送。写后已知结果仍能落账。
- 回执：按 note ID 独立 GET；作者、MR、准确生成正文、每张引用和线程归属；回读失败保留 ID。未知结果时服务和 Agent 都不补发，包含普通评论原重试策略不能泄漏到含图分支。
- 接缝回归：第 7.1 节逐项经过实际托管入口；policy_changed／Policy 不被投影吞掉，恢复读取不进入发送 preparer。已有上下文及原字段严格校验保持有效，不因 generated body、150 秒工作期限或提前保存 ID 改变旧文字行为。
- 幂等／恢复：同 ID 只执行一次，变更输入冲突；同字节不同来源仍有内容等价保护；并发和跨进程重启不续传／发布。所有出口释放 pin，清空阻塞恢复、正常追加／读取不被网络锁阻塞。
- 核心不回归：比较来源准备前后 input-state、消费游标、读取证明、History 查询／页面／预算；动作本身不创建额外读取状态。Agent 主动读 History 的正常开销单独归因。

复用并运行受影响 GitLab、Room、Lark 的定向单元／集成和必要 race，特别保留现有文字动作、原图来源、History 协议及飞书摘要测试。具体命令按落地的行为测试名补入实施记录，不把本文清单写成已经通过的测试报告。普通 CI 不包含真实模型或真实平台写入。

### 9.2 Agent、真实平台与回流

- 真实 Agent 按第 8.1 节自然请求覆盖“说明和两张图一起评论”“只发第二张”“回复这条讨论”、跨回合来源、目标／线程歧义及不支持授权入口；记录实际调用成本、格式／帮助次数和稳定动作 ID，不仅核对最终是否发布。
- 不要求安装裸 glab 来修复托管能力，不搜索磁盘，不制造事项，不改用其他账号或原生 CLI 补发；缺权限时沿既有连接修复路径反馈。
- 真实 GitLab 对普通评论与准确讨论回复分别留证，覆盖纯图片与图文／多图；核对一个 note ID、准确项目／MR／线程、user 作者、完整正文、全部图片顺序及独立下载字节。
- 同 ID 重查和重启读取不新增上传／评论。若通知订阅会收集本人发布内容，验证现有本人／context-only 处理，不能因此触发新一轮自动回复或发送；如发现缺口，修复相应 GitLab 回流判定，不扩展 Inbox／History 协议。
- 浏览器只核对已有评论／通知图片展示和能力说明，不新增发送页面。分别报告组件、托管装配、真实模型、真实 GitLab、浏览器证据；其中任何一层不能替代其他层。

真实接口小验证和最终验收都须明确账号、项目、MR／discussion、准确文字、源图片及允许的上传张数／评论条数。评估通过不等于获准真实写入；成功样本可复用，故障边界优先用夹具，不为阶段编号重复污染真实对象。

## 10. 可行性、复杂度与后续范围

可行，基于当前已加入的公共基础，GitLab 增量属于中等复杂度，主要集中在适配器与 Room 装配。上传 HTTP 本身较小；最需控制的是按输入选定授权／重试 Spec、最终 Markdown 与准确回执，以及取消／重启下的结果边界。

动作核心新增范围限定为一份私有输入上下文和可复用纯摘要函数；Agent 层另补 GitLab 的局部正常返回投影、准确帮助和图文指引，增量低到中等。不改读取主流程、不增加状态机、不迁移旧记录，也不以 Agent 简化为由统一所有平台入口。两个渠道的上传、内容表现和真实验收天然不同，放在同一个交付批次不会消除这些工作。按本方案独立实施，复用生命周期／账本及已加入的 Agent 合同基础即可避免主要重复，无需返工飞书发送器。

后续若要支持 Issue／Work Item，应单独补原生目标、身份和 API 合同；若要支持持久指令／问答授权图片，需设计其完整内容授权，不能只扩大 schema；若要支持通知图片，应补来源账号及原通知证据。这些均保留为后续能力，不作为本期“通用化”前提。

新增阶段 4 是文字／图文共享的目标入口补齐，采用第 3.2 节的局部 resolver 与现有目标存储，不重做上传、授权、执行或恢复体系；其改动落点及完成条件以该节为准。第 2、7 节的图文基础对照保留为阶段 1～3 的设计记录，不能据此判断当前代码仍为 body-only 或阶段 4 已实施。

## 11. 本次核查记录

本次仅阅读当前代码、飞书阶段记录、固定版本 glab 的离线版本／帮助和官方 API 文档，并编写本文。已确认 glab 图文命令存在、内部动作仍是 body-only、公共来源与派发检查已加入工作区，以及 GitLab 合并上下文和静态 Spec 的具体适配点。

未修改业务实现，未新增或运行本功能测试，未上传图片或发布评论。飞书阶段记录中的测试属于其既有证据，不能视为本方案 GitLab 功能已经验证。

2026-11-21 修订：对照飞书第 8.4 节、其[实施记录](implementation/lark-image-send-agent-simplification.zh.md)及当前 `actions_agent_view`／`agent_contract`／公共帮助代码，将降低 Agent 认知负担纳入总体目标、设计、改动位置和三阶段完成条件；明确 GitLab 尚未自动采用 Lark 投影，以及现有 context 可减少重复目标字段。本次只修订本文并检查文档结构、链接和示例，未实施代码或执行真实平台／模型验收。

同日补充代码预判：逐项核对 Spec 选择时序、纯图片解码、NoteAPI 内联长度校验、派发回调输入、policy_changed 返回、ResultID 设置位置、actions read 的完整恢复内容及 CLI 的 3 分钟期限，按第 7.1 节确定局部实现方式。独立临时 Go 探针使用仓库现有 goldmark 依赖：普通正文可解析到 1 个追加图片节点；未闭合代码围栏／script 块均为 0；`/close` 正文仍为 1，证明图片 AST 检查不能替代 quick action 排除。探针位于系统临时目录，未修改生产代码、测试文件或真实 GitLab 状态；这些是本地设计证据，不是新增图文实现的测试通过记录。

2026-11-21 URL 入口补充：对照当前 `actions_target`、GitLab 读取／账号 admission、通知来源解析和阶段 3 预置目标验收，新增第 3.2 节及阶段 4，更新范围和调用成本口径。仅修改本方案，未实现 resolver、运行功能测试、重启服务或执行真实平台写入；新阶段与既有完成记录分别保留。
