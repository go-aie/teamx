# 飞书发送：Agent 接口与指引简化

对应[方案第 8.4 节](../LARK-IMAGE-SEND-PLAN.zh.md#84-独立前置任务agent-接口与指引简化)。日期：2026-11-20。

第 8.4 节已实施并完成隔离验收：本地合同、共享入口、可靠性回归和最终 race，以及普通 Room／默认助理各 17 个真实 DSH／Agent 场景均通过。飞书侧为原生 CLI 子进程夹具，此结果不代表阶段 4 开放。阶段 3 [原记录](lark-image-send-s3.zh.md)和原证据（本地验收文件 `lark-image-send-agent-evidence.json`）保留。

## 最终合同

| 环节 | 简化前 | 简化后 |
| --- | --- | --- |
| 群候选 | 取 `candidates[i].target`，再手工包装 `locator.candidate` | 直接复制 `resolution.candidates[i].locator` 到下一请求的 `locator`；保留标签、账号、完整性和原生目标，选择后重新核验 |
| 帮助 | 托管 actions 不支持子命令 help，需转根命令 | 同一准确入口支持 `actions resolve/execute --help`，与根命令共用 JSON 合同和字段诊断；根命令的路由参数不显示在托管入口 |
| 请求 | 从通用 ActionSpec 判断授权分支 | 返回 `required` 与群的 `authority:{type:"human_reply",required:["room_seq","reason"]}`；本人只需五个顶层字段，群增加 policy_token 和 authority |
| 解析返回 | 完整原生解析和执行策略 | 账号 scope 只表达一次；目标保留全部 object／container 原生 ID。返回可执行性、动态 input_schema、必填依据及错误原因／下一步 |
| 执行返回 | 回执缺少完整发送内容，重复表达状态 | 返回准确 input、必要 authority、统一状态、尝试次数、账号／目标、verified 和已知远端 ID；完整冻结身份及证据仍通过 `actions read --id ID` 查询 |
| 指引 | 主 Skill 重复格式、上限、OAuth／上传／Session 过程 | 保留目标、内容、整条提交及本地报告；本人／群各一个准确示例，歧义澄清、整条单次提交和 unknown 原回执查询。上限由动态 schema 提供，修复随错误按需展开 |

推荐路径始终是 `tools run ENTRY actions resolve/execute`，ENTRY 来自 tools list；已有有效入口或目标直接复用。`tools list --json` 与默认 JSON 输出一致；两种 actions 入口均支持 `--request` 或 `--request-file`，文件在 Agent CLI 所在进程读取，`-` 读取 stdin，服务端不读取客户端指定路径。

候选 locator 不冻结 turn_number 或 policy_token。用户澄清后的请求必须使用当前运行时值。本人发送不接收虚构 authority／policy_token；群依据必须来自实际直接人类 Room 指令。没有增加附件解析调用、自动补造业务依据、上传工具、候选持久化或新授权入口。

常规发送目标的 provider／instance／tenant 由同级 account 给出；target 保留原生 object 和 containers。候选的 locator 仍包含可直接送回解析器的完整身份，不能把精简后的已解析 target 当成 candidate。其他平台及普通回复保留原投影。完整 actions read 保持原合同，支持历史回执和 unknown 下的只读恢复。

## 调用与篇幅

同一个已知入口、已取得图片引用的新任务，本人最多“解析＋执行”两次业务调用；从群名开始为“查询候选＋核验选择＋执行”三次，跨澄清回合累计。有效目标可直接执行。实际工具调用还包括入口／Skill 首次发现、Inbox／History、Room 报告和恢复查询，另列于证据，不从总数扣除。

执行请求的必填业务信息没有减少或隐藏：本人为 target_ref、turn_number、id、operation、input；群再增加 policy_token 和 authority，后者含 type、room_seq、reason。图片每项仍为 message_seq／attachment_id，均来自已有读取。减少的是候选身份的手工包装、分支推断及无效格式尝试。

| 主指引／帮助（UTF-8 字节） | 简化前 | 简化后 |
| --- | ---: | ---: |
| 内置飞书主 Skill | 9662 | 5067 |
| actions resolve --help | 2473 | 1602 |
| actions execute --help | 5115 | 2440 |

入口发现的飞书条目由 2904 降至 1860 字节；保留准确账号、当前能力／缺权原因与动态图片开放状态，并给出直接 Skill 命令。

篇幅基线是本任务开始时工作区中的阶段 3 内容，不是 Git HEAD；后者尚未包含全部已有阶段改动。正常返回在同一实际群图文动作上，对比未投影的原解析／回执与新输出，保留完整内容后合计由 2512 降至 2433 字节；候选查询的实际输出成本另见分场景证据。

## 真实 Agent 结果与成本

完整摘要见简化验收证据（本地验收文件 `lark-image-send-agent-simplification-evidence.json`）。两种入口均完成 17 个场景，每种有 9 条 succeeded、2 条 not_applied、1 条 unknown，全部最多一次尝试；10 次原生消息 POST 包含注入 unknown 的那一次。失败、取消、原回执查询及重启均未增加消息。正式图文注册保持关闭。

下表对比阶段 3 原有的同一组 12 个业务场景；新增的五个矩阵场景另外计入全部 17 场景总数。工具数按真实 ACP 工具 ID 去重，包含失败、格式处理、History、发现、报告和恢复。帮助／发现／回执查询按实际 CLI 子命令计数，通过审计命令分词重算；一条 shell 工具调用可包含多条 CLI 命令，各列不能简单相加。解析／执行列描述目标核验和发送；可选的正常回执预查及异常恢复查询均列在回执查询列，没有从总工具数中扣除。

| 入口／范围 | 全部工具调用 | 解析／执行 | 帮助查询 | 格式错误 | History 读取 | 发现 | 本地报告 | 回执查询 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 普通 Room／简化前 12 场景 | 83 | 20 | 12 | 2 | 4 | 9 | 12 | 26 |
| 普通 Room／简化后相同 12 场景 | 57 | 15 | 5 | 0 | 4 | 4 | 12 | 11 |
| 普通 Room／简化后全部 17 场景 | 72 | 20 | 5 | 0 | 4 | 4 | 17 | 11 |
| 默认助理／简化前 12 场景 | 68 | 18 | 15 | 2 | 2 | 7 | 12 | 15 |
| 默认助理／简化后相同 12 场景 | 64 | 13 | 10 | 0 | 4 | 4 | 12 | 15 |
| 默认助理／简化后全部 17 场景 | 84 | 18 | 10 | 0 | 4 | 4 | 17 | 20 |

两种入口的本人新目标均为“解析＋执行”两次；重名群为首回合查询一次、澄清后核验与执行两次，合计三次。已有有效目标的正常后续任务均只执行一次。Agent 的可选旧回执预查并非接口前置条件，全部保留在实际成本中。新旧样本使用同一模型条目和 text-only 图片材料方式，但模型的主动核查和帮助选择会变化；原样本首条请求显式要求发现入口／Skill，新样本使用自然任务表达，不把单次样本当成稳定性能保证。

早期失败样本也保留在证据中：`tools list --json` 的不一致已修复；重启后猜测根参数和创建本地进度文件的样本未计为通过。最终发现返回直接给出 Skill 命令并提示每个 Session 首次动作读取它，Skill 明确由动作回执保存进度，未通过临时用户教程或放宽审计过关。本次没有把模型限流／超时改记为通过。

复验命令（不写回日常配置；输出目录必须是新的绝对路径）：

```sh
TEAMX_LARK_IMAGE_AGENT_REAL=1 \
TEAMX_LARK_IMAGE_AGENT_MODE=managed \
TEAMX_INPUT_COMPARISON_CONFIG="$PWD/.teamx/config.toml" \
TEAMX_DSH_OUTPUT=/absolute/fresh/evidence-directory \
go test -tags=e2e ./internal/room -run '^TestLarkImagesRealAgent$' -count=1 -timeout=30m -v
```

将 mode 改为 `default-assistant` 可独立复验另一入口；不设 mode 则两种均运行。两个通过父命令的原始报告路径和全部场景成本分别保存在证据中。

## 验证与证据复用

已通过：

```sh
go test -tags=integration ./internal/room -run '^(TestLarkActionAgentContract|TestLarkImages|TestLarkSelfSendManagedCapability|TestLarkSendStandaloneCommonContract|TestActionLedgerReadableDuringSessionRecovery|TestReferenceAction|TestActionDispatch|TestActionInputCanonical|TestToolEntry|TestPlatformEntr|TestExplicitToolBinding|TestAgentCommandHelp|TestAgentJSON|TestAgentPolicyAndActionDiagnostic|TestAgentHTTPJSON)' -count=1

go test -race -tags=integration ./internal/room -run '^(TestLarkActionAgentContract|TestLarkImagesDispatchGuards|TestActionLedgerReadableDuringSessionRecovery|TestReferenceActionInternalHTTPWithoutCLI)$' -count=1

go test ./internal/platform/tool ./internal/platform/lark -count=1

go test -tags=integration ./internal/room -run '^(TestLarkActionAgentContract|TestToolEntr|TestPlatformEntr|TestExplicitToolBinding|TestLarkImageAvailability|TestLarkMemberCapability|TestLarkImagesPermissionRecoveryKeepsTextIndependent|TestLarkSendStandaloneCommonContract|TestReferenceActionInternalHTTPWithoutCLI|TestReferenceCrossPlatform)' -count=1
```

新合同测试实际经过 CLI、HTTP、固定账号原生子进程和账本：候选整体复制、重名及不完整标记、澄清后新回合、帮助与字段错误一致、stdin JSON、篡改 tenant／目标被拒绝、完整有序图文、旧回执在 Store 重开后恢复且不补发。格式错误仍严格拒绝；图片额外字段／类型错误精确定位到 input.images[0]，不回显值。

真实 Agent 复用阶段 3 装配、同一 deepseek-v4.1-flash-1017 模型及飞书原生子进程夹具。用户任务自然表达，不追加 JSON 层级或字段修法教程。矩阵扩充为普通 Room／默认助理各覆盖本人和群的文字、单图、纯多图、图文，再覆盖跨回合 History、同名群和多来源澄清、上传失败、取消、unknown／重启查询及缺图片权限。失败样本单列保留，不计为通过。

来源存储、pin／字节校验、完整内容冻结、上传器／回读归一和独立进程故障实现未改变，沿用阶段 1～3 证据。与新合同有关的托管图文、撤权、取消、unknown、原回执／重启和共享入口兼容定向复跑。没有重跑与本轮无关的浏览器或全仓验收。

## 开放边界

未向真实飞书账号上传或发送；正式图文注册仍为文字 schema。未修改日常配置、服务或 Session。真实接口小验证及阶段 4 仍须按原方案另行确定准确账号、目标和内容后执行。
