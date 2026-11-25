# TeamX

**Teamwork for AI Agents**

TeamX 是一个 AI Agent 协作工具。你可以与个人助理对话，也可以创建团队房间，让不同角色的 Agent 分工完成任务。

每位 Agent 拥有独立上下文和工作区，能够使用真实 CLI 工具。你提出目标、参与讨论，随时查看执行过程或暂停协作。Agent 在服务机器上运行，关闭浏览器后继续工作。

## 能做什么

- **个人助理**：梳理需求、读取 GitLab 待办，通过对话连接飞书、关注群和保存持续提醒规则。
- **团队协作**：按任务创建房间，让不同角色共同讨论方案、编写代码和独立评审。
- **平台集成**：接收 GitLab 活动、待办和飞书消息，使用所选账号读取上下文、按你的要求处理事务；支持 Scion 开发与 Review。
- **执行控制**：查看成员的思考和工具调用，暂停、取消或恢复执行，保留各会话的历史和工作文件。
- **电脑**：在右侧观看 Agent 使用的浏览器或桌面，接管完成登录后交还任务；见[使用说明](docs/conversations.zh.md#电脑)。

## 快速开始

已有运行中的 TeamX 服务时，直接在电脑或手机浏览器打开服务地址并登录，无需安装。

自行部署需要 macOS 或带 systemd 的 Linux，以及模型服务的地址、模型 ID 和 API Key；无需另装 Go 或 Node。系统与架构要求见[安装指南](docs/installation.zh.md#下载与校验)。当前为开发预览，实际验证范围见对应版本说明。

### 1. 安装并启动

在运行 Agent 的服务机器上执行（需有包含安装器的正式 Release）：

```sh
curl -fsSL https://github.com/go-aie/teamx/releases/latest/download/install.sh | bash
```

脚本下载 standard 包并启动常驻服务。macOS 不加 sudo，Linux 按提示获取权限。离线安装、开发快照或尚无对应 Release 时，见[下载与校验](docs/installation.zh.md#下载与校验)；开发者可选择[从源码运行](internal/room/README.zh.md#从源码运行)。

已有安装升级到最新稳定版，执行 `teamx service upgrade`（Linux 加 sudo）。它会自动下载校验、创建一致备份并重启；已是目标版本时直接退出。旧版 CLI 的过渡方式及离线升级见[备份与升级](docs/installation.zh.md#备份与升级)。重复运行安装脚本会在下载应用包前提示升级入口。

### 2. 登录并配置模型

打开安装输出的网址，按提示从服务机器读取管理 token 并登录。在网页填写模型协议、Base URL、模型 ID 和 API Key，点击「验证并开始使用」。无需提前 export 密钥；登录和远程访问详见[首次使用与访问](docs/installation.zh.md#首次使用与访问)。

### 3. 开始一次协作

模型配置完成后，进入默认「个人助理」私信，直接发送需求。想体验团队协作：

1. 点击左侧「＋ → 新建房间」，选择「朋友聚餐」。
2. 确认或修改输入框中的开场消息，点击「发送」。
3. 查看成员讨论，补充人数、预算或口味；点击成员查看活动记录，按需暂停或启用协作。

新任务可以创建新房间，已有会话可以继续讨论。首次体验无需连接外部平台。会话操作见[使用指南](docs/conversations.zh.md)，服务启停、日志、备份和升级见[安装与运行](docs/installation.zh.md)。

## 团队模板

| 团队 | 适合体验 |
| --- | --- |
| [朋友聚餐](.teamx/teams/takeout-dinner/README.md) | 协调选菜、预算份量和口味，推荐首次体验 |
| [书店夜场筹备](.teamx/teams/bookstore-night/README.md) | 共同策划活动，平衡预算、运营和来宾体验 |
| [软件开发](.teamx/teams/software-development/README.md) | 方案设计、代码开发与独立评审 |

可以[自定义团队和角色](.teamx/teams/README.md)。默认成员依次执行，也可为新建房间配置并发协作；不同房间可同时运行。

## 平台集成（可选）

| 平台 | 可以做什么 | 从这里开始 |
| --- | --- | --- |
| GitLab | 接收项目活动与个人待办，开发、评审和处理事务 | [GitLab 使用指南](docs/gitlab.zh.md) |
| 飞书 / Lark | 接收私聊和群消息，按规则跟进，回复或向指定群发送消息 | [飞书使用指南](docs/lark.zh.md) |
| Scion | 使用原生仓库开发、创建 Review 和独立评审 | [Scion 使用指南](docs/scion.zh.md) |

例如，在个人助理中说「连接飞书，关注产品讨论群，以后需要我处理时在这里提醒我」，按对话提示完成授权和选择。平台可用动作及限制见对应指南。

## 文档导航

- [安装与运行](docs/installation.zh.md)：安装、模型首配、远程访问和维护。
- [Linux 打包、部署与升级](docs/linux-deployment.zh.md)：维护者操作路径、当前验收边界及 alpha 留存环境。
- [会话使用](docs/conversations.zh.md)：个人助理、团队房间、提及和执行控制。
- [核心概念](docs/concepts.zh.md)：团队、成员、上下文、工作区与平台连接。
- [完整文档目录](docs/README.zh.md)：平台接入、自定义配置、架构和开发参考。

## 开发

从[源码运行](internal/room/README.zh.md#从源码运行)开始；检查方式见[测试说明](docs/testing.zh.md)，分发与发布见[打包指南](packaging/README.zh.md)和[正式发行指南](packaging/releasing.zh.md)。

Agent 由 [DeepSeek Harness（DSH）](https://github.com/deepseek-ai/deepseek-harness) 运行，内部职责与扩展入口见 [Server 架构](docs/server.zh.md)。
