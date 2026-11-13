# Scion 并行开发

architect 拆解并分配任务，developer-a、developer-b、developer-c 在独立 Scion 工作副本中并行编码。全部交付后，developer-a 在独立集成分支运行整体验收，architect 核对需求覆盖。

## 使用

1. 配置可用的 DSH、默认模型和 scn 0.1.13，复用已有、验证通过的 Scion 平台。
2. 新建「Scion 并行开发」Room，为四个成员分别启用该平台的工具。
3. 在 Workspace 中选择测试仓库、基线分支及平台，应用并等待各成员的独立副本准备完成。
4. 在 Room 明确需求和允许发布的任务分支范围，由 architect 分配后开始；查看三项开发交付、集成版本与测试结果。

**按步骤操作请看 [并行编程操作步骤](TESTING.zh.md)**；想观察多名 Agent 改同一文件时的冲突与集成问题，使用[脚本准备 base 的独立实验](OVERLAPPING-FILES.zh.md)，自动生成初始分支和 Room 提示词。

`scheduling: parallel` 在创建时冻结，已有串行 Room 不能运行中切换。模板目录与模型配置见[团队配置说明](../README.md)，平台准备见 [Scion 使用说明](../../../docs/scion.zh.md)。

## 协作边界

四名成员可共用已有 `csgship` 账号，但各自绑定工具、保持独立 Session 和 Workspace；不同提交作者不代表不同认证身份。每项交付须提供准确分支、完整 head 和实际测试结果。

本团队用于并发编码测试，没有独立 reviewer，不自动合入目标分支、approve 或关闭 Review；原「软件开发」团队继续保持串行流程。具体职责见 [architect](prompts/architect.md) 和 [developer](prompts/developer.md)。

已完成的真实运行与并发证据见[共享账号验收记录](../../../docs/implementation/scion-parallel-acceptance.zh.md)。
