# 软件开发：Architect、Developer 与 Reviewer

architect 负责复杂需求的设计，developer 修改代码、测试并提交平台评审（GitLab MR / Scion Review），reviewer 独立评审设计及实现。用户决定需求取舍、合并或关闭。职责见 [architect](prompts/architect.md)、[developer](prompts/developer.md)、[reviewer](prompts/reviewer.md)，具体命令按需读取当前工具 skill。

| 需求类型 | 协作流程 |
|---|---|
| 简单需求 | developer 直接开发和验证 → reviewer 独立评审 → developer 修正 → reviewer 复审；无需设计文档或等待 architect |
| 复杂需求 | architect 编写 `design.md` → reviewer 评审设计 → developer 按已评审设计实现 → reviewer 评审实现 |
| 跨阶段或多步骤任务 | `design.md` 列出步骤、依赖、交付物和完成标准；完成当前一步且评审无阻塞后进入下一步，最后核对整体验收 |

局部且方案明确的修改走简单流程；涉及职责、重要接口、数据模型、复杂并发恢复或迁移时先设计。只有执行步骤较多、没有架构变化时，developer 可自行编写简短实施计划并交 reviewer。步骤推进不额外要求用户确认；用户限定阶段或要求暂停时遵从。

所有成员收到 Room 通知，提及只作高亮。architect 仅在用户要求设计、developer 提出设计需求或发现具体架构问题时介入；简单需求保持沉默。developer 交付可取得的版本后，reviewer 自动独立评审。平台对象或评论暂时受阻时，先交接完整版本和 Room 结论，并标明平台流程待完成。工具配置阻塞由负责该动作的成员报告，不重复同伴的请求。

`design.md` 放在项目根目录，按实际复杂度写明目标、范围、当前依据、职责、接口、流程、失败恢复、取舍与验收。成员使用独立 Workspace，通过文档提交的完整原生 ID 和平台评审交接，说明设计目标分支与实现依赖。没有平台条件时，architect 保存文档并在 Room 发布完整版本正文，由 developer 保存；本地路径或摘要不能完成交接。

## 配置与使用

首次使用 Scion，按 [Scion 使用说明](../../../docs/scion.zh.md)完成登录、连接、成员工具和 Workspace 配置，再发送需求。

1. 按[团队配置说明](../README.md)配置 DSH 和模型，启动 TeamX，新建「软件开发」Room。服务不自动加载 `.env`。
2. 安装所选工具：GitLab 使用固定 glab 及 [teamx-gitlab](../../../tools/gitlab/README.zh.md)；Scion 使用固定 scn 0.1.13，可配套 [teamx-scion](../../../tools/scion/README.zh.md)。程序路径由服务配置，成员不自行搜索安装或登录。
3. 在「设置 → 平台连接」保存并验证对应连接。Scion 填写程序路径和实例，自动检测当前服务已有账号，或在页面用 PAT 登录；后台管理 profile ID。凭据不放进角色或消息。
4. 在需要平台工具的成员「工具设置」中分别选择已验证连接并「应用」。每成员每工具独立绑定，展示实际账号和实例。工具开关不是平台权限沙箱。
5. 在 Workspace 设置选择 Git / Scion、仓库和连接，并显式应用。每个 membership 独立初始化；clone 连接不自动启用成员工具。保留已有文件，回访不自动同步或切分支。详见 [Workspace 文档](../../../docs/workspace.zh.md)。

统一发现入口：

```sh
"$TEAMX_CLI" tools list
"$TEAMX_CLI" tools run scion --help  # GitLab 对应 gitlab
"$TEAMX_CLI" tools skill scion
```

## Scion 开发与评审

Scion 使用 `.scion` 和完整原生 ObjectId，不能执行 Git fetch/checkout 或使用 MR refs、40 位 SHA。先核对实际仓库、目标分支、基线、文件状态与测试入口。

- developer 从核实的目标基线建立含 Room、membership 和任务标识的任务分支，修改并测试；明确作者、显式文件路径 commit，再 `land` 发布任务分支。发布后重取实际 head，代码树变化时补测；先查已有任务 Review，再创建并回读对象。
- reviewer 读取 Review 和完整讨论，从对应 revision 核对完整 base/head。在自己的副本检查 dirty、pending 和冲突后 `switch --detach FULL_HEAD`，核实 base=head、detached=true、pending 为空，独立读代码并测试。发布前再核对版本，以 `--revision` 发表普通评审评论，回读真实评论 ID、作者和正文后交 Room。
- developer 修正后在原分支 commit/land，用 `review push NUMBER FULL_HEAD` 更新同一个 Review，再读新 revision，发送 [待复审]；reviewer 对新版本复审。旧版本通过不等于新版本通过。

Room 交接包含实例、canonical repo、真实 Review 链接与编号、target、revision、完整 base/head、实际测试及未验项。三个独立 Workspace 可以共用一个平台账号，平台评论作者相同不代表没有独立检出和测试。

`land` 发布任务分支；`review land` 合入目标分支。当前 Scion 流程只用普通版本化评论，不自动 approve、request-changes、合入或关闭。合入属于 [S5](../../../docs/SCION-SOFTWARE-DEVELOPMENT-PLAN.zh.md)，须先解决原子版本校验和排队结果限制。未知写入先查远端，不重发或改用 REST 重试。

GitLab 仍使用 Git 和原生 glab 语法，具体准确检出、普通评论、已确认 head 的合并规则见 `tools skill gitlab`。保留已有修改，不直接发布目标分支、不 force push、不部署、不删除远端分支、不 reset/clean。

## 已有 Room

新模板只影响新建 Room。已有 Room 保留角色、成员和 Session 快照，不自动替换角色或重放任务；首次 Scion 协作使用新 Room，保留原 GitLab Room。当前动态工具说明适用于已有成员，绑定仍须显式应用。

架构见[平台架构](../../../docs/platform-architecture.zh.md)，分阶段验收见 [Scion 方案](../../../docs/SCION-SOFTWARE-DEVELOPMENT-PLAN.zh.md)与 [GitLab 工具验收](../../../tools/gitlab/README.zh.md#真实验收)。
