# TeamX 文档

第一次使用，从[项目介绍与快速开始](../README.md#快速开始)进入。已经启动服务时，可以直接阅读会话和平台指南。

## 安装与使用

| 你要做什么 | 文档 |
| --- | --- |
| 安装服务、登录、配置模型、远程访问、备份和升级 | [安装与运行](installation.zh.md) |
| 在 Linux 从源码打包、准备 HTTPS／SRT、部署和升级服务器 | [Linux 打包、部署与升级](linux-deployment.zh.md) |
| 使用个人助理、创建房间、查看提及、控制执行 | [会话使用](conversations.zh.md) |
| 理解团队、成员、上下文、工作区和平台连接 | [核心概念](concepts.zh.md) |
| 接入 GitLab，接收待办或开展开发评审 | [GitLab 使用指南](gitlab.zh.md) |
| 接入飞书 / Lark，关注会话和持续跟进 | [飞书使用指南](lark.zh.md) |
| 使用 Scion 仓库与 Review | [Scion 使用指南](scion.zh.md) |
| 设置通知、查看收件箱、保存长期规则和跟进事务 | [平台通知与跟进](notifications.zh.md) |

## 自定义与配置

| 你要做什么 | 文档 |
| --- | --- |
| 定义团队、角色、模型选择和串并行协作 | [团队配置](../.teamx/teams/README.md) |
| 为成员准备工作目录、Git 或 Scion 仓库 | [Workspace](workspace.zh.md) |
| 调整模型、时区或服务配置 | [安装与运行：后续设置](installation.zh.md#模型与平台的后续设置)、[配置示例](../.teamx/config.example.toml) |
| 配置共享项目收件箱、调整保留、容量和故障恢复 | [Agent 收件箱](inbox.zh.md) |

## 架构与开发

Agent 由 DSH 执行模型回合和工具；TeamX 管理会话、消息、调度和持久化。理解实现时，建议按 Server → Room → Agent 运行时的顺序阅读，再按需查看平台与工具。

| 主题 | 文档 |
| --- | --- |
| 浏览器与服务职责、存储及生命周期 | [Server 架构](server.zh.md) |
| 成员身份、消息读取、调度与恢复 | [Room 架构](room-architecture.zh.md) |
| 正文、消费、输入 owner、发布根和故障恢复 | [存储架构](storage-architecture.zh.md) |
| DSH 配置、Session、取消与轮换 | [Agent 运行时](agent-runtime.zh.md) |
| 公共指引、角色指令及每轮上下文 | [Agent Instructions 组装](agent-instructions.zh.md) |
| 平台连接、工具适配与引用能力 | [平台架构](platform-architecture.zh.md) |
| 通知采集、跟进协议、动作及接口 | [通知技术参考](reference/notifications.zh.md) |
| 会话创建、阅读状态与默认助理接口 | [会话接口参考](reference/conversations.zh.md) |
| Workspace 准备、恢复与管理接口 | [Workspace 技术参考](reference/workspace.zh.md) |
| 独立 CLI 安装、命令及凭据 | [GitLab 工具](../tools/gitlab/README.zh.md)、[Scion 工具](../tools/scion/README.zh.md) |
| 源码运行与配置排查 | [开发运行参考](../internal/room/README.zh.md) |
| 回归检查、集成与真实服务验收 | [测试说明](testing.zh.md) |
| 构建分发包与发布版本 | [打包指南](../packaging/README.zh.md)、[正式发行指南](../packaging/releasing.zh.md) |

后续规划见[后续方向](roadmap.zh.md)。阶段方案和[实施记录](implementation/)用于追溯设计及验证；当前使用方式以以上指南为准。

共享电脑的接入、成员开关、观看与接管见 [Agent 电脑与 MCP](agent-mcp.zh.md)，可选运行环境的构建部署见 [AIO 打包说明](../packaging/computer/aio/README.zh.md)。
