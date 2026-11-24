# Agent 工具目录精简投影

2026-11-21，实施[上下文简化分析](../AGENT-CONTEXT-SIMPLIFICATION-ANALYSIS.zh.md)的 **S3b**。基于当前工作树，导出时 HEAD 为 `9efe816`；保留已有其他修改。

## 返回合同

`teamx tools list` 仍经原成员准入、所选 catalog 与已启用绑定读取，只调整 Agent 返回视图。投影从动作文件移到 [tools_agent_view.go](../../internal/room/tools_agent_view.go)，不再直接嵌入管理用的 `toolSelection`。

| 信息 | Agent 目录 |
| --- | --- |
| 准确入口与用途 | `name`、`description`、`provider`、`tool_type`；本地工具保留声明的用途 |
| 账号区分 | 原 `platform_id/platform_name/instance/username`；飞书继续提供原生 `account` |
| 实际可用性 | 原 `status/capabilities/unavailable_reason/action_disabled_reason`；无能力时返回空数组，保留诊断 |
| 保存的配置授权 | `configured_actions` 保留每项 operation、target_kind、object_type；未配置时省略，明确空授权时为 `[]`。它不代表当前就绪或业务授权 |
| 飞书检查与修复 | 保留 checked_at、account_fixed、tool_enabled、image_actions，以及逐能力的 configured、state、reason_code、缺失 scopes、evidence_state、next_action |
| 已声明指导 | `skill` 转成准确入口的 CLI 命令；GitLab 原 managed-send 短流程不变，Scion 原 Skill 路径转成可直接读取的命令；没有声明则省略 |
| 内部元数据 | 不返回重复的 agent_id/membership_id、定义 version/internal_only 或 action_authorization 的版本／revision 外壳 |

可执行文件、环境映射和依赖原本已由 `json:"-"` 隐藏，**不计入本次缩减收益**。人工 `/v1/tools/bindings`、绑定持久化和执行／动作合同保持原结构。确需检查绑定版本和完整配置时使用已有 `platforms member get --platform-id ID`；原生命令语法／版本仍通过准确入口的 `tools run ENTRY --help/--version` 查询。普通发现无需增加 detail 或设置查询。

已启用但账号／程序不可用的绑定继续出现在目录中，避免只看到空列表而丢失修复原因；显式停用的历史绑定、其他成员的绑定及未被选中 catalog 声明的工具仍不出现。连接代际失配时保留 platform_id、tool_type 和 `platform_changed`，省略不可调用的 name、Skill 和 workflow，避免将平台类型别名误作准确 ENTRY。不会猜测或自动切换其他账号。

无账号工具不补造账号、平台、Skill 或托管动作；未声明 Skill 的飞书 fixture 也不再自动获得 Skill 链接。飞书原有紧凑视图继续保留检查信息，并显式保留配置授权的目标限制；不承诺该分支进一步缩小。

## 验证与输出量

[规则测试](../../internal/room/tools_agent_view_test.go)覆盖字段保留、目标限制、明确空授权、无 Skill／本地工具、连接失配与投影不改原数据。[隔离集成](../../internal/room/tools_discovery_integration_test.go)覆盖默认／显式空／公开外部组合、同实例多账号、发现后直接执行、失效账号和程序修复、飞书缺权限／检查过期／未配置动作，以及停用和其他成员隔离。既有飞书动作夹具补齐生产中实际声明的 Skill。

以下比较相同正常绑定的紧凑 JSON，不含末尾换行；GitLab 基线已包含 A2/B2 缩短后的 workflow，不重复计算旧阶段收益：

| 隔离目录 | 原返回 | 新返回 | 减少 |
| --- | ---: | ---: | ---: |
| 两个 GitLab 账号 | 2,188 B | 1,970 B | 10.0% |
| Scion | 391 B | 333 B | 14.8% |
| 公开外部本地工具 | 194 B | 102 B | 47.4% |

真实模型使用 [只读对照测试](../../internal/room/tools_discovery_real_test.go)，固定打包 DSH `0.2.2-rc.1`、Node `v26.7.0`、现有默认模型 `deepseek-v4.1-flash-1017`。同一组三个绑定为两个 GitLab 账号和一个本地工具；旧分支通过原目录形状返回，两个分支均使用真实 CLI／HTTP 准入和相同隔离执行进程。

- 场景一：同一实例下选择 `user202` 的准确入口，只调用 `--help`；场景二：选择本地构建结果读取工具，只调用 `--help`。
- 每场景、每分支重复两次，共八个新 Session 回合。旧／新分别 **4/4 成功**，每次恰好 `tools list → tools run ENTRY --help` 两次业务请求，无详情／平台配置／Skill 补读，无错误账号调用。
- 模型实际收到的完整目录输出含换行为 **2,383 → 2,073 B，减少 310 B（约 13.0%）**；帮助输出相同。未测算总 token、缓存成本或可靠性提升；小样本只证明这两个场景未增加发现调用且仍可正确使用入口。
- 首次尝试原打包运行目录时 ACP 初始化 EOF，尚未进入模型任务。既有[后端记录](agent-collaboration-boundaries.zh.md)已记录该目录的原生依赖缺失；随后复用临时目录中依赖完整、版本和文件哈希固定的相同打包版本通过验收。未改原 node_modules、日常配置、运行服务或既有 Session。

脱敏计量、运行版本和原始证据校验值见 agent-tool-discovery-evidence.json（本地验收文件 `agent-tool-discovery-evidence.json`）。原始报告、目录和会话保存在该文件所列的私有临时路径，不把模型思考或凭据提交到仓库。外部平台请求与写入均为零。

验证命令：

```sh
go test -tags=integration -race ./internal/room -run '^Test(ToolDiscovery|GitlabToolEntry|PlatformEntries|PlatformEntry|LarkActionAgentContract|ToolSkillTopic|ExplicitToolBinding|CLIDuplex|HelpSkipsCredential|BindingCAS|MissingTool|ScionMemberTools|SelectedCatalog|EmptyCatalog)' -count=1 -timeout=4m
go test ./internal/platform/tool ./internal/app/platforms ./internal/app/extensions ./cmd/teamx -count=1

TEAMX_TOOL_DISCOVERY_REAL=1 \
TEAMX_TOOL_DISCOVERY_CONFIG=/absolute/path/to/config.toml \
TEAMX_DSH_OUTPUT=/fresh/private/output \
TEAMX_COLLAB_DSH_ENTRY=/complete/runtime/node_modules/@deepseek-ai/dsh/lib/bin.js \
TEAMX_COLLAB_DSH_VERSION=0.2.2-rc.1 \
go test -tags=e2e ./internal/room -run '^TestToolDiscoveryRealDSHComparison$' -count=1 -v -timeout=15m
```

定向集成 race、组件和真实模型对照均通过；后续补充的缺权限／过期检查用例再次通过定向 race。未运行全仓测试或真实平台验收。本单元随服务端新构建采用，未部署、未提交、未 push；S1d 的 URL 成本门槛及其他 S2/S3/S4 单元状态不变。
