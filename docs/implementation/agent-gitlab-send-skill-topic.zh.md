# GitLab 发送 Skill 按任务加载

2026-11-21，当前 HEAD `ce5c4e3`，实施 [Agent 上下文简化分析](../AGENT-CONTEXT-SIMPLIFICATION-ANALYSIS.zh.md) 的 **S1b-B2**。

## 交付与兼容

GitLab 托管 MR 评论及准确讨论回复直接读取发送 topic，无需先读目录或开发评审全文：

```sh
"$TEAMX_CLI" tools skill ENTRY --topic managed-send
```

ENTRY 必须是当前成员已绑定的准确工具入口。CLI → `/v1/tools/skill` 沿原成员、Membership 和绑定校验读取嵌入资源；topic 中的命令会代入该入口。读取不需要可用 OAuth、原生 CLI 或 Workspace，也不授予发送权限。

Topic 包含文本、纯图片、图文、MR／准确讨论锚点、完整 URL 短路径、既有 target_ref／合同跨轮复用、当前规则及人类证据、完整内容单次提交、稳定 ID、动态 input_schema／重试合同、回执报告和 unknown 原 ID 恢复。保留原图 seq／attachment ID、含图必须来自直接 Room 人类指令、不得改为部分图片或文字、not_applied 显式重试等边界。

省略 topic 仍返回完整 GitLab 指南，开发、原生读取、准确版本评审、合并与其他授权原生操作仍可达；原生代理未删除。未知／路径形式／大小写错误的 topic 明确返回 400，不按路径读取或回退全文。Lark、Scion 及未声明 Skill 的工具不获得该 topic，原默认读取行为保留；`tools list/run` 拒绝非空 topic。没有新增公共扩展 Skill/topic API，也没有改变动作、授权、重试或持久化合同。

## 入口与大小

- [嵌入读取](../../internal/platform/tool/skills.go)、[入口替换](../../internal/room/tool_entries.go)、[CLI](../../internal/room/tools_cli.go)和 [HTTP](../../internal/room/tools_http.go)沿原路径增加有限资源选择；`tools skill --help` 与 `tools skill ENTRY --help` 可离线读取。
- [发送 topic](../../internal/platform/tool/skills/gitlab/managed-send.md)自足返回；[完整指南](../../internal/platform/tool/skills/gitlab/SKILL.md)保留原章节并提供直接路由。
- [tools-list workflow](../../internal/room/actions_agent_view.go)及[公共指南](../../internal/room/prompts/agent-guide.md)直接给出发送 topic；其他任务仍读默认完整指南。保存角色同样经公共指南取得入口，不要求改写角色。

当前完整源资源 15,287 UTF-8 字节，发送 topic 7,869 字节，单次资源读取减少约 **48.5%**（未计准确 ENTRY 替换造成的小量差异）。这不代表模型总 token、耗时或调用数同比减少。

## 验证与证据

验证结果与改前／改后模型样本见脱敏证据（本地验收文件 `agent-gitlab-send-skill-topic-evidence.json`）。

组件及集成检查覆盖 CLI／HTTP 默认和 topic 读取、无凭据／原生程序、未知 topic、准确入口替换、别名、未绑定／其他成员／旧 Membership、人类伪装、其他操作拒绝 topic、Lark／Scion 默认资源以及无 Skill 工具。固定指南沿 `OpenSession → PrepareOpen → Launch.Instructions → 所选工厂` 检查保存角色和三个输入合同。动作回归复用原 URL、图文、策略、取消、重启和原 ID 恢复测试。定向 `integration -race` 共 30 个顶层用例通过（204.362 秒）；最终 topic／装配的 4 个 race 用例通过，顶层离线帮助接线后的 5 个定向 race 用例也通过（2.578 秒）；观察器正反例及调用计数测试通过。

另外执行完整 `go test ./internal/platform/tool ./internal/room -count=1 -timeout=8m`：tool 包通过，Room 在 465.878 秒结束，有两项架构断言失败：`TestStaticAssemblySourcesAndCapabilitiesAgree`（GitLab events 声明与生命周期装配）、`TestInboxAndActionCoreHasNoNativeProviderRules`（`actions_target.go`／`agent_contract.go` 的 Lark 依赖）。这些被检生产文件与断言均未在本任务修改；在临时 `git archive HEAD` 中独立运行这两项，复现完全相同的失败（0.422 秒），确认是既存问题。本单元未扩大修复范围，也不把核心整包记为通过。

真实模型使用现有默认配置、真实 DSH、产品默认助理、生产 CLI／HTTP 和持久动作账本；GitLab HTTP 指向本地 HTTPS fixture，没有远端业务写入。URL 样本首次发送只给完整 MR URL 与普通人类原图，不预造通知、事项、Workspace 或 native ID。已有目标样本则显式提供保存的目标；最终补采也提供准确 ENTRY。后续跨轮检查准确讨论、普通文本、完整图文、已有目标／合同复用和 unknown 原回执恢复。

改前两回合 URL 样本通过；改后两回合样本通过且首次直接读取一次 topic、没有完整 Skill 前置。前者 CLI 调用数为 7／3，后者为 6／3。扩展样本保留全部调用；其他采样存在更多帮助读取，不能据此宣称总调用稳定下降。

改前已有 target_ref 场景首次出现 JSON 字段格式修正；改后初次同场景猜测了不存在的工具入口，未读到 topic，也未通过成本检查。两次业务均到达成功回执，但不计为完整通过。公共指导进一步明确：指导缺失且 ENTRY 未知时只发现一次准确入口，禁止猜测；已有指导及目标合同则不重复发现。补采中仍出现过提前猜测入口、重复 topic 读取或无关 `ls` 的模型偏差，均记为失败，不能据此宣布无偏差或稳定减少调用。

后续已有目标样本通过八次发送及两次歧义澄清，但旧测试的“本次不要发送”也被模型理解为不在 Room 发言，整套历史矩阵停止，未进入它的重启／unknown 后续。补采夹具明确提供准确 ENTRY，并把该任务改为“不要向 GitLab 发送、请在 Room 说明”，区分外部动作与本地报告；这不是同输入前后对照。该最终补采完整 **16 回合通过**（183.119 秒），覆盖八次文本／图片／图文发送、两次歧义澄清、含图授权边界说明、服务重启后原回执查询、上传失败、取消、unknown 及原 ID 恢复；全程只读取一次发送 topic、没有读取完整 Skill，也没有格式修正或原生代理回退。unknown 之后查询原回执，不新增发布。

扩展 URL 样本还暴露测试观察器把读取自己刚生成并实际提交的请求 JSON 误报为宿主探测。已仅允许同命令内的固定 JSON body 检查，保留任意文件、凭据／环境读取及原生代理的拒绝判定，并验证正反例。失败轨迹保留，不能用资源字数替代行为结果。

复验使用全新输出目录，凭据沿原安全配置读取：

```sh
TEAMX_GITLAB_IMAGE_AGENT_REAL=1 \
TEAMX_GITLAB_IMAGE_AGENT_MODE=default-assistant \
TEAMX_INPUT_COMPARISON_CONFIG="$PWD/.teamx/config.toml" \
TEAMX_DSH_OUTPUT=/absolute/fresh/output \
go test -tags=e2e ./internal/room \
  -run '^TestGitlabActionURLRealAgent$/default-assistant$' -count=1 -timeout=15m -v
```

原始模型输入、请求、活动与回执报告存于私有临时目录，仓库只保存脱敏统计。已有 target_ref 的完整恢复矩阵可用 `TestGitlabImagesRealAgent/default-assistant` 单独复验。

## 生效与范围

资源随构建嵌入，公共指南继续参与现有 Session 指纹。采用时按原流程结清、受控轮换和恢复，不直接编辑生成的 AGENTS.md 或绕过指纹检查。本次只运行隔离实例，没有重启日常服务或轮换用户 Session。其余短 Skill 拆分、B1 的设置执行语义、C1/C2 别名清理、后端组合与真实平台写入均不在本单元范围；未提交或 push。
