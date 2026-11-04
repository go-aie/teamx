# 个人助手

团队 ID 为 `personal-assistant`，成员 `assistant` 理解 GitLab 个人待办、核实上下文，按用户意图代办常规事务；需要本人判断时在 Room @ 操作者。角色职责见 [assistant](prompts/assistant.md)。

1. 按[团队配置说明](../README.md)配置模型与 DSH，启动服务并新建「个人助手」房间。构建独立 GitLab 工具并配置固定 glab，见 [GitLab 工具手册](../../../tools/gitlab/README.zh.md)。
2. 在「设置 → 平台连接」保存并验证个人连接，Token 仅在设置中填写。
3. 打开 assistant 的「工具设置」，选择 GitLab 连接并点击「应用」。界面展示实际账号及实例，明确沿用账号的全部有效权限；不再配置预设、操作勾选或已投递对象范围。工具开关不承诺隔离该账号已有的平台权限。
4. 在「通知源 → GitLab → 我的待办」另行选择接收连接、范围与过滤并显式开启。首次只接收基线后新待办，不回放旧任务，见[通知说明](../../../docs/notifications.zh.md)。API 工具不需要 Workspace；开启通知不自动启用工具。
5. 发送个人偏好与任务，保持 assistant 启用且有预算，需要决定时直接在 Room 回复。

成员先运行 `"$TEAMX_CLI" tools list` 核对账号和实例，再用 `"$TEAMX_CLI" tools run gitlab --help` 逐层发现命令；复杂待办、讨论与 WorkItem 流程按需读取 `"$TEAMX_CLI" tools skill gitlab`。目标来自通知、用户或实际查询，不要求 source/config version/operation ID。普通回复不是 approve，完成待办不等于完成业务；只在问题处理且核实后完成确切待办，不批量清空。

指派、关闭、删除、实质审批、合并等依照用户对具体对象和动作的决定执行；事实充分的常规代办直接处理。平台命令沿用实际账号权限，增加工具能力不意味着用户已经要求执行某个动作。结果按命令退出码及内容报告，超时、中断或响应未知先查询远端，不自动重发。

已有成员升级后显式选择连接并应用；旧权限不会自动扩展。当前每回合工具说明引导冻结角色使用新入口，原 Room / Session / 通知进度 / 历史操作文件保留，不自动重放。未知历史写入需人工核查。架构见[平台架构](../../../docs/platform-architecture.zh.md)，真实 GitLab / DSH 工具调用与浏览器验收的待补项见 [GitLab 工具验收](../../../tools/gitlab/README.zh.md#真实验收)。
