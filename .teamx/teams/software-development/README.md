# 软件开发：Architect、Developer 与 Reviewer

architect 负责复杂需求的设计，developer 修改代码、测试并提交 MR，reviewer 独立评审设计及实现。用户决定需求取舍、合并或关闭。角色职责见 [architect](prompts/architect.md)、[developer](prompts/developer.md)、[reviewer](prompts/reviewer.md)，工具调用流程按需读取工具 skill。

| 需求类型 | 协作流程 |
|---|---|
| 简单需求 | developer 直接开发和验证 → reviewer 评审、修正后复审；无需设计文档或等待 architect |
| 复杂需求 | architect 先编写 `design.md` → reviewer 评审设计 → developer 按已评审设计实现 → reviewer 评审实现 |
| 跨阶段或多步骤的大任务 | 在 `design.md` 中列明步骤、依赖、交付物和完成标准；developer 完成并验证当前一步，reviewer 确认无阻塞后再进入下一步，最后核对整体验收 |

局部且方案明确的修改走简单流程；涉及跨模块职责调整、重要接口或数据模型变化、复杂并发与恢复、迁移或关键方案不明时先设计。只有执行步骤较多、没有架构变化时，developer 可自行编写简短实施计划并交 reviewer。步骤推进不额外要求用户逐步确认；用户明确限定阶段或要求暂停时遵从。实现中出现实质设计变化，先更新并重新评审设计。

`design.md` 放在项目仓库根目录，按需写明目标与范围、现状、模块职责、接口和数据变化、主要流程、风险与失败恢复、关键取舍、验证方式及分步验收。成员使用独立 Workspace，通过文档提交和 MR 的完整 SHA 交接；没有仓库或平台交付条件时，architect 保存文档并将完整正文按版本交接到 Room，由 developer 保存为其项目的 `design.md`。只发送本地路径或摘要不能完成交接。

## 配置与使用

1. 按[团队配置说明](../README.md)配置 DSH 和模型，启动 TeamX 并新建「软件开发」房间。服务不自动加载 `.env`。
2. 构建 `teamx` 和 `tools/gitlab` 独立程序，安装固定 glab 1.127.0，配置服务的 `TEAMX_GITLAB_BIN` / `TEAMX_GLAB_BIN`，详见 [GitLab 工具手册](../../../tools/gitlab/README.zh.md)。
3. 在「设置 → 平台连接」保存并验证连接。在需要使用 GitLab 的 architect、developer 和 reviewer 的「工具设置」分别选择连接并点击「应用」。每成员每工具一份绑定，界面明确展示账号、实例及「沿用账号的全部有效权限」。工具开关不是平台权限沙箱；角色职责和用户意图决定实际动作，不再配置逐操作权限或项目白名单。
4. 本地开发及完整评审另行配置 Workspace 的 HTTPS 仓库；私有 Git 认证可引用已验证连接。工具、Workspace 和通知分别配置；API 操作不要求 clone。保留已有文件，不自动 fetch/checkout。

成员通过统一入口发现能力和按需读取流程：

```sh
"$TEAMX_CLI" tools list
"$TEAMX_CLI" tools run gitlab --help
"$TEAMX_CLI" tools skill gitlab
```

原生命令保持 glab 语法，WorkItem / Issue 讨论缺口由工具内 Go 扩展完成，具体参数从帮助取得。评审是带完整 base/head 的普通评论，不是 approve。合并固定已确认 head、关闭自动合并，不默认删分支。退出码及输出按命令自身语义解释；异步请求已接受不等于已合并，未知结果先查远端，不自动重发。

## 已有成员切换

新增 architect 及本次协作指令在服务重启后，对新建「软件开发」房间生效。已有房间保留创建时的成员和角色快照，不会自动加入 architect 或替换指令；需要三人流程时新建房间，并明确交接已有分支、MR 和进度。

停止旧服务并结清执行；历史未知写入由操作者核查。升级后，现有成员在「工具设置」显式选择连接并应用。旧的只读/待办范围配置不会自动扩为账号完整权限。每回合的当前工具说明会覆盖冻结角色中的旧调用协议；Room、角色快照、Session、消息和历史操作文件均保留，不要求新建房间，也不自动重放任务。旧 Workspace 脚本不作为替代 API 入口。

架构见[平台架构](../../../docs/platform-architecture.zh.md)，真实 GitLab / DSH 工具调用及浏览器验收的待补项见 [GitLab 工具验收](../../../tools/gitlab/README.zh.md#真实验收)。
