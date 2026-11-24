# Agent 后端身份与通信说明

日期：2026-11-21。对应[上下文简化方案 S1a-A3](../AGENT-CONTEXT-SIMPLIFICATION-ANALYSIS.zh.md#92-可独立交付的工作单元)。基准为 `9a7ed3d` 加现有 S0、A1、A2 工作树；保留其他未提交修改。

## 实施范围

公共指南允许在当前角色和授权范围内使用运行时内部协作，明确内部助手不是其他 TeamX 成员，结果仍由所属成员核验和负责。内部审查不能代替其他 Room 成员的评审、同意或授权。角色允许的 Room 同伴交接继续通过 TeamX send；成员管理权限没有扩大。默认助理同步去掉笼统的委派禁令，保留不能为其他成员分派工作、管理成员或修改其他会话的角色边界。

新增内嵌英文 [`dsh-guide.md`](../../internal/room/prompts/dsh-guide.md)，说明：

- `subagent`、`subagent_fork` 和 `workflow` 遵循原生合同；任务需携带相关上下文与限制，不能假定完整指南自动继承。使用 TeamX 工具时必须收到当前成员身份、权限和操作约束并保留绑定。
- `send_message` 使用运行时返回的内部 Agent ID，Room 成员名和 @mention 不能代替它。内部发送成功不证明 Room 已发布；公开消息和角色允许的交接使用 TeamX send。
- 所属成员核验并交付内部调查或 review，不能将其报告为 Room 独立 reviewer 的结论。

原 `dshBackend.componentOptions` 按“公共指南 → DSH 补充 → Session 身份 → 成员角色”合成一次，经 `sessionHost.PrepareOpen → Launch.Instructions → 所选 DSH 工厂` 投递。没有向通用 Session 复制业务指导，没有改动输入桥、生命周期、原生工具组合、团队角色模板或公共 API，也未增加注册表或工具映射框架。

## 验证

既有最终指南测试扩展到历史 fixture、流式 CLI 和 HTTP MCP，覆盖默认助理与已保存自定义角色，检查顺序、唯一性、角色原文及公共/后端职责。隔离和私有输出、恢复、轮换相关 integration＋race 回归通过；`internal/agent/dsh` 与公开 `agent` 包全量测试、构建和差异空白检查通过。没有据此宣称 Room 核心包全量通过；A2 记录的两项既有架构断言失败未在本次处理。

新增 opt-in `TestDSHInternalCollaborationReal` 使用真实 DSH/模型、生产宿主/CLI/HTTP/Store、隔离的双成员 Room 和只读本地任务文件。独立 reviewer 成员存在但未启用。运行方式：

```sh
TEAMX_INTERNAL_COLLAB_REAL=1 \
TEAMX_INPUT_COMPARISON_CONFIG=/absolute/path/to/config.toml \
TEAMX_DSH_OUTPUT=/absolute/path/to/fresh-output \
go test -tags=e2e ./internal/room -run '^TestDSHInternalCollaborationReal$' -count=1 -v
```

测试读取已有默认模型凭据，不初始化或修改日常凭据库；`zstd` 用于解码受测 DSH v4 持久记录。输出目录按已有真实验收规则创建；不要复用非空目录。

| 场景 | 改前对照 | 改后，两次独立运行 |
| --- | --- | --- |
| 内部调查，启动后补充消息，再由所属成员核验与公开交付 | 通过；总数 42、差值 8 | 均通过；内部消息使用实际 Agent ID，成员 A 核验后发布 |
| 内部 review，核对 `Mean(2,4)` 实际 4、期望 3，并说明独立评审归属 | 父回合结束时仍等待内部结果，没有完成 Room 交付 | 均通过；A 发布核验结果，明确不构成 Room reviewer 的独立评审 |

改前使用本次修改前的文件快照，通过 Go overlay 恢复原指南、助理角色和合成代码；已核对实际生成的 AGENTS.md 确实保留旧禁令且没有新 DSH 指南。改前调查仍能委派，不能把 review 未交付直接归因为某一句禁令，也不能声称本次修复了底层生命周期问题。

最终改后测试自动解码父/子 Agent 的持久消息，确认完整公共指南与准确成员身份在首次模型请求前实际加载，双向 `send_message` 接收者均为真实内部 ID，内部助手没有通过 TeamX CLI 发布，Room 结果由 A 交付，reviewer 没有执行，源文件未修改。另核对父/子实际请求的 schema：原生 `subagent`、`subagent_fork`、`workflow` 和 `send_message` 均保留；最终父/子加载的消息也包含完整 DSH 补充指南。没有把父 Agent 收到指南当成子 Agent 继承的证明。

受测模型为 `deepseek-v4.1-flash-1017`；实际启动的是本地 checkout 的 `@deepseek-ai/dsh 0.2.1-alpha.1`，ACP agentInfo 为 `deepseek-harness-acp 0.0.1`。分析采样的打包版本 `0.2.2-rc.1` 未做本次真实模型验收。两次改后各有两个真实模型业务回合；样本不足以证明统计可靠性、token 成本或总体调用量改善。原始轨迹保存在私有临时目录，脱敏摘要、快照哈希与准确命令见证据（本地验收文件 `agent-backend-guidance-evidence.json`）。

## 生效与后续边界

固定指南继续参与原配置指纹；已有 Session 不能在原地替换 AGENTS.md 后强行恢复。采用新构建时，先按原控制结算旧 Session，再受控轮换。回退同样遵守结算与兼容检查，不重放未知写入。已经保存的助理角色不会随内嵌模板自动更新；其中如有显式禁止内部委派的旧角色文字，须按原配置路径明确更新，不能静默重写保存角色。

本次未重启日常服务、轮换现有用户 Session、执行真实平台写入、提交或 push。A3 的内部调查/review 最小验证已完成；fork/workflow 的实际执行、并发 Workspace/外部动作、子 Agent 直接执行 TeamX 操作、暂停/取消及恢复的完整矩阵仍属于 S2，不将 schema 可见等同于全部行为验收。B1/B2 topic、C1/C2 别名清理和其他后端实现仍按各自范围推进。
