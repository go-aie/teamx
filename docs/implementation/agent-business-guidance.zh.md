# Agent 正常业务指导校正

日期：2026-11-21。对应[上下文简化方案 S1a-A2](../AGENT-CONTEXT-SIMPLIFICATION-ANALYSIS.zh.md#92-可独立交付的工作单元)。基准为 `9a7ed3d` 加 S0、A1 及既有工作树；本次只修改指导、帮助、ENTRY 替换和对应验证。

## 最终路径

| 场景 | 指导与边界 |
| --- | --- |
| 正式输入 | 按当前 Session transport 直接读已知 stream；缺选择才 list。GitLab/飞书 Skill 不再使用旧批次和 task_event 分类指导生产输入 |
| 来源恢复 | 已知 source_ref 直接 `matters source --source`；缺引用才 sources。续页使用返回的 cursor，保留 object 条件；resolve 仅显式重试不足的解析，独立讨论需要时才 ensure，使用实际 source_ref/target_ref/current turn |
| 事项答复与指令 | read 自动关联显式人类 reply_to；普通文字的真实答复有依据时才 reply。立即授权工作不先 instruct；跨轮意图才保存指令，托管动作成功引用该指令后自动履行，撤销和非托管完成仍可显式记录 |
| 平台设置 | 准确账号的通知总开关直接提交；复杂设置保持观察版本。选择卡片直接 start 并订阅，组件负责搜索/分页，结束回合等待正常接续；核对原意图、最新限制和已处理版本后协调/acknowledge |
| 排他接收范围 | 已选群后的“只接收这个群”包括关闭私聊；“群只接收这个群，其他设置保留”只改群范围。复用选定 native ID，不重新选群 |
| 托管发送 | GitLab/飞书统一推荐根 `actions resolve/execute --tool ENTRY`；已保存目标/context 能恢复原账号而 ENTRY 未知时省略 --tool，不填 provider 别名。飞书当前内容/上下文使用 `tools run ENTRY read`。复用有效目标和合同，缺失/歧义/实际失效才 resolve；新动作不预查回执、不创建无关事项 |
| GitLab 评审评论 | 当前托管能力覆盖的 MR 评论/讨论回复统一使用 managed，包括无 Inbox matter 的直接请求；移除原生 `mr note create` 的平行建议。原生代理及其他已授权操作继续可用 |
| 无账号本地工具 | 使用准确 ENTRY 和原成员绑定；缺用法才 help，只在声明 Skill 且需要流程时读取。不虚构平台账号、托管动作或 Workspace 前置 |
| 定时执行 | due 事件已有 run_id 时直接 claim；finish 是报告发布入口，不再另 send 相同报告 |

公共指南保留权限、全部有效规则、最新撤销、准确账号/对象/版本、unknown 核实、稳定 ID、消费与业务完成的区别，以及 Markdown 链接、行内代码和准确 URL 规则。新增说明：显示格式不允许改写用户指定的外部发送正文。

默认助理删去重复的 send、平台入口和 Calendar 协议，使用公共指南；保留职责、参与条件、提醒和配置范围。动态 workflow 改为当前入口、目标/合同复用和 unknown 恢复。飞书 Skill 的根 actions 示例通过原 `entrySkill` 补齐准确 ENTRY 替换；Scion 开头不再要求重复加载自身 Skill。无新增 topic、DTO、执行合同或扩展 API。显式查询回执须实际 read 原 ID，即使上下文已有旧回执；普通新动作不增加预查。

## 验证与证据

固定指南验证沿正常 `OpenSession → sessionHost.PrepareOpen → Launch.Instructions → 所选工厂` 捕获最终 Instructions，覆盖流式/历史 fixture 及默认/已保存自定义角色；公共指南、角色与 Calendar 不重复。没有绕过宿主或修改 Session 执行语义。

相关集成与 race 回归覆盖来源分页/独立讨论、通知与选择入口、无账号工具、飞书 self/group/reply、GitLab 直接动作、真实答复自动关联、指令结算、撤销、unknown 和 Session 恢复。真实模型使用隔离 Room、生产 CLI/宿主和本地平台 fixture，模型为 `deepseek-v4.1-flash-1017`；没有向真实平台发送消息或修改日常服务。

受测运行时由现有配置选择：本地 checkout 的 `@deepseek-ai/dsh 0.2.1-alpha.1`，ACP agentInfo 为 `deepseek-harness-acp 0.0.1`。已核对实际启动脚本所在 package 及保存的 backend 元数据；原 artifact 声明见证据。这与分析文档采样的打包静态版本 `0.2.2-rc.1` 不同，本次不声称已完成该打包版本的真实模型验收，也没有替换用户运行时。

验收中发现旧 `policyRealBackend` 在公开工厂已持有 Component 后才替换 `dshSession.component`，因此漏记了 ACP 回合。观察器现改为在所选工厂返回 Component 时包装，保留原工厂、实际 prompt、图像能力、运行身份、私密值遮蔽与 Activity；新增正常 OpenSession 回归防止零轨迹被误当零调用。这个修复只影响测试。修复前的功能结果仍有持久回执/HTTP 证据，但不用于调用量比较。

首轮相关 integration＋race 回归 **38 个顶层用例、含子项 123 项通过**；补充的帮助、最终指南、根 actions、观察器与 Scion 检查也通过。各次运行有重叠，不相加计数；准确用例名、次数、文本快照哈希和模型轨迹摘要见脱敏证据（本地验收文件 `agent-business-guidance-evidence.json`）。构建和差异空白检查通过。

一次核心包全量检查中，A2 引起的旧字符串断言和将业务 turn_number 误判为输入参数的断言已修正并复验；以下两项在改前 overlay 也失败，保持原状，不能宣称核心包全绿：

- `TestStaticAssemblySourcesAndCapabilitiesAgree`：GitLab Events 来源声明与生命周期绑定检查不一致。
- `TestInboxAndActionCoreHasNoNativeProviderRules`：既有 actions_target.go 的 lark 分支及 agent_contract.go 的原生 lark import。

真实模型小样本的设置链路在改前暴露排他选群仍保留私聊的问题；改后完整通过通知开关、两次真实 Chromium 选群/提交、自动结果接续、精确 acknowledgment 和受控 Session 轮换，共 8 回合。完整 URL 的两次 GitLab 评论/回复，改前与改后均为 10 次模型工具调用、4 次托管业务调用，零参数修正，每次仅一个本地 note POST；没有据此宣称总调用量下降。

GitLab 已知引用链路还暴露过 provider 别名尝试和显式回执询问使用缓存两处问题，已分别补齐“原账号可恢复时省略未知 ENTRY”和“显式询问实际 read”指导。并行补采曾遇到 HTTP 429，后续改为串行；失败记录保留在证据中。来源恢复、自动答复关联、自动履行和撤销边界由对应生产合同集成测试验证；不冒充全部场景的真实平台验收。

最终 GitLab 链路 **16 个真实模型回合全部通过**：首次 MR/讨论各 resolve＋execute 两次业务调用，后续六次发送均仅 execute；另验证歧义/不支持授权时不发送、服务重启后的原回执恢复、上传失败、取消、unknown 和显式 fresh receipt read。零参数修正，9 次本地 note POST 含 1 次 unknown 样本，恢复未新增 POST。没有原生 CLI 发送、无关事项创建或手动 instruction-status。

助理角色资源从 4,375 降至 3,259 UTF-8 字节；公共/流式指南补入来源与恢复边界，未证明总常驻上下文更小。没有采集实际模型 token 用量，也没有以文字长度推算成本、耗时或统计可靠性收益。后续按 topic 缩小长 Skill 仍属于 B1/B2。

## 生效与回退

固定指南参与现有配置指纹。采用时应在原 Session 已结算后，按现有控制轮换并恢复执行；不能直接修改生成的 AGENTS.md 或跳过兼容检查。已有 Room 保存的助理角色不会随模板自动更新，公共指南仍为旧角色提供所需路由；角色更新需走原明确配置路径。回退同样遵守结算与指纹检查，不重放未知写入。

本次没有替换日常服务、轮换用户现有 Session、提交或 push。A3 后端身份/委派说明、B1/B2 topic 与 C1/C2 别名清理仍按各自范围推进；旧输入 fixture、飞书根读取和嵌套 actions 实现均保留。
