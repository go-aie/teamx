# TeamX

**Teamwork for AI Agents**

让拥有不同职责的 AI Agent 在同一个房间里协作。你提出目标、参与讨论，Agent 通过共享消息、独立工作区和真实 CLI 工具完成任务。

通过内置 Web 界面参与协作，Agent 由 [DeepSeek Harness（DSH）](https://github.com/deepseek-ai/deepseek-harness) 运行。每位成员保留自己的私有上下文，需要与团队交流时主动向房间发言。

## 能做什么

- **组织团队协作**：定义成员角色，创建多个任务房间，保留各自的消息、会话和工作区。
- **设计、开发与评审代码**：让成员分工设计方案、编写代码和独立评审。
- **处理个人事务**：助手读取 GitLab 待办、核实上下文，按用户意图回复和处理，需要决定时在房间里 @ 你。
- **接收平台通知**：接收 GitLab 项目活动和个人待办，将飞书私信和群提及送入助手收件箱。
- **观察与控制执行**：查看成员状态、思考及工具调用，暂停或取消回合，重置私有会话并恢复协作。

一个服务管理多个独立运行的房间；不同房间可以同时执行，每个房间内的成员回合依次执行。标签页各自选择查看对象，切走或关闭页面不停止协作。

## 快速开始

### 1. 准备环境并构建

需要 Go **1.27+**、已安装的 DSH，以及可用的模型 API Key。DSH 的 Node 和依赖需完整保留，安装说明见[Agent 运行时](docs/agent-runtime.zh.md#准备-dsh-与配置)。

在 TeamX 根目录执行：

```sh
go build -o bin/teamx ./cmd/teamx
```

Web 界面已嵌入程序，无需单独启动前端。

### 2. 配置运行环境

项目内使用时，复制[配置示例](.teamx/config.example.toml)，按本机安装与模型服务填写：

```sh
cp .teamx/config.example.toml .teamx/config.toml
```

| 配置 | 内容 |
| --- | --- |
| `runtime.dsh.binary_path` | 已安装的 `dsh` 可执行程序的绝对路径 |
| `runtime.model` | 模型协议、地址、名称及密钥环境变量名 |
| `server.dsh_turn_timeout` | DSH 单回合总时长，默认 `"15m"`；大型任务可设 `"30m"` |
| `env.OPENAI_API_KEY` | 模型密钥；也可通过服务环境提供，已有环境变量优先 |

可用 `command -v dsh` 查找 DSH 路径。模型密钥可填写在本地配置的 `[env]` 中，也可通过环境变量提供；服务不会自动加载 `.env`。

### 3. 启动服务

```sh
./bin/teamx serve --config .teamx/config.toml
```

打开 **http://127.0.0.1:8088/**。此配置默认将运行数据保存在 `.teamx/data/`。

### 4. 开始协作

1. 点击左侧「＋」，选择「朋友聚餐」，创建并进入房间。
2. 确认或修改输入框中的开场消息，点击「发送」。
3. 查看成员讨论，补充需求；点击成员查看活动记录，按需暂停或启用协作。
4. 新任务创建新房间，点击已有房间可继续之前的讨论。

退出时在服务终端按 **Ctrl-C**，等待清理完成。再次使用相同配置启动可继续已有房间。

配置加载规则、用户目录用法及恢复说明见[运行手册](internal/room/README.zh.md#服务配置)。

## 发行包与自行构建

发行包包含固定 Node/DSH，使用者无需安装 Go 或 Node；首次启动与升级见[发行包使用说明](packaging/release-README.zh.md)。通过 GitHub Actions 获取开发快照、本地打包及正式发行的方法见[构建与发行指南](packaging/README.zh.md)。当前为开发预览，完整平台矩阵和正式发行尚未验收完成。

## 团队模板

| 团队 | 适合体验 |
| --- | --- |
| [朋友聚餐](.teamx/teams/takeout-dinner/README.md) | 三位成员协调选菜、预算份量和口味，无需 GitLab |
| [书店夜场筹备](.teamx/teams/bookstore-night/README.md) | 四位成员共同策划活动，处理预算、运营和来宾体验 |
| [软件开发](.teamx/teams/software-development/README.md) | 方案设计、代码开发与独立评审 |
| [个人助手](.teamx/teams/personal-assistant/README.md) | 接收个人待办，核实并处理常规事务，需要决定时 @ 用户 |

编辑团队的 `team.json` 或角色指令后，重启服务并新建房间生效；已有房间保留创建时的配置。

## 使用 GitLab（可选）

需要 GitLab 功能时，在服务机器上安装 **glab 1.127.0** 到 `bin/glab`，再构建 GitLab 工具并重启 TeamX。安装及路径配置见[工具手册](tools/gitlab/README.zh.md)。

```sh
go build -o bin/teamx-gitlab ./tools/gitlab
./bin/teamx serve --config .teamx/config.toml
```

服务启动后：

1. 在左下角「设置 → 平台连接」填写连接名称和实例地址，点击「登录 GitLab」完成授权。首次按页面引导注册一次 GitLab 个人应用，填写 Application ID 和 Secret；配置完成后只需账号登录。
2. 点击成员，进入「工具设置」，为该成员选择 GitLab 连接并应用。
3. 在房间里提出具体任务。工具沿用所选账号的有效权限，操作取决于用户意图和成员职责。

需要 OAuth 仓库读写时，首次配置时选择「允许拉取和推送代码」，并在 GitLab 应用中勾选 `api` 和 `write_repository`。成员用 `teamx workspace-git fetch/push/ls-remote` 执行网络 Git，本地 diff／commit 仍使用 Git；API-only 授权不会自动扩权。本次 OAuth 简化后的真实账号与 Git 网络链路尚未重新验收。

GitLab 仅支持 OAuth。旧版本数据需先停止服务，再运行 `teamx connections upgrade --rooms-dir ROOT`；已有 OAuth 授权保留，旧 PAT 连接保留原 ID 和房间引用并需要重新登录。升级不访问 GitLab，也不撤销远端 PAT。

**工具、Workspace 和通知分别配置**：

| 需要 | 配置入口 |
| --- | --- |
| Agent 调用 GitLab API | 成员详情 → 工具设置 |
| 克隆仓库、修改代码或本地评审 | 输入框下方的 Workspace，填写 HTTPS 仓库地址并应用 |
| 接收项目活动或个人待办 | 右侧通知源 → GitLab，选择连接、投递目标、公共检查周期及消息类型，显式开启 |

API 工具和通知无需先克隆仓库；开启通知不会自动启用成员工具。连接凭据只在设置中填写。

## 使用 Scion（可选）

「软件开发」团队也支持 Scion 原生仓库与 Review。安装固定 **scn 0.1.13**，在平台连接页面填写实例和 `scn` / `teamx-scion` 路径，自动检测账号或使用 PAT 登录；保存连接后，为成员绑定工具、应用 Scion Workspace，即可发送需求，进行开发、独立评审、修正与复审。

首次配置、日常使用和常见问题见 [Scion 使用说明](docs/scion.zh.md)。

## 使用飞书（可选）

在服务机器上安装并以用户身份登录官方 **lark-cli 1.0.99**，然后在界面中配置：

1. 「设置 → 平台连接」：填写 CLI 绝对路径和已登录的 profile，保存并验证账号。
2. 「通知源 → 飞书消息」：选择连接、接收成员及指定／全部会话范围，显式开启通知。
3. 按需补读最新内容或前后文时，在「成员详情 → 工具设置」为成员的 `lark` 读取能力选择连接并应用；处理已收到的通知无需此步骤。

在成员收件箱查看通知，启用协作后由助手读取和跟进，需要决定时在 Room 提问。当前支持只读收件，飞书回复尚未开放；首次配置、测试步骤和诊断恢复见 [飞书使用说明](docs/lark.zh.md)。

## 常用操作

- **调整端口或数据目录**：编辑 `.teamx/config.toml` 中的 `server.listen`、`server.data_dir` 后重启服务。
- **调整系统时区**：打开「设置 → 通用」，搜索 IANA 时区并保存。默认 `Asia/Shanghai`，全部 Room 共用；页面时间、日期分组和时间输入立即更新，其他页面自动刷新。「使用浏览器时区」只填入草稿，仍需保存。设置写回服务实际使用的配置文件；手工修改 `server.time_zone` 需重启。Agent 每轮收到当前本地时间，时区变更从下一正常回合生效；原始时间数据保持不变。
- **查看或暂停成员**：点击成员进入详情，查看活动记录并使用「暂停 / 启用协作」；成员可以阅读后保持沉默。
- **重新开始讨论**：房间头部「··· → 重新开始」，清空公共消息并重置私有会话，保留工作区和已用预算。
- **删除房间**：左侧房间的删除操作会永久移除该房间消息、私有会话和工作区文件。

每位成员默认有 16 个执行回合的预算，可在配置的 `server.max_turns` 中调整新房间的预算。成员控制、预算与恢复说明见[运行手册](internal/room/README.zh.md)。

## 进一步阅读

| 文档 | 内容 |
| --- | --- |
| [团队配置](.teamx/teams/README.md) | 自定义角色、模型、DSH 和工作区建议值 |
| [运行手册](internal/room/README.zh.md) | 服务配置、用户目录、成员控制与恢复 |
| [GitLab 工具](tools/gitlab/README.zh.md) | 安装、命令帮助、WorkItem 扩展与凭据配置 |
| [Scion 使用说明](docs/scion.zh.md) | 页面 PAT 登录、账号自动检测、成员绑定与开发评审流程 |
| [飞书使用说明](docs/lark.zh.md) | CLI profile、成员绑定、消息通知、收件测试与诊断恢复 |
| [核心概念](docs/concepts.zh.md) | 对象、职责、配置边界及架构阅读路径 |
| [Room 架构](docs/room-architecture.zh.md) / [Agent 运行时](docs/agent-runtime.zh.md) | 消息、调度、身份、生命周期及 DSH |
| [Agent Instructions 组装](docs/agent-instructions.zh.md) | 公共指引、角色指令、Session 身份与每轮上下文 |
| [Workspace](docs/workspace.zh.md) | 目录、仓库、配置应用与恢复 |
| [平台架构](docs/platform-architecture.zh.md) / [通知](docs/notifications.zh.md) | 连接、独立工具、GitLab 活动／待办与飞书消息 |
| [后续方向](docs/roadmap.zh.md) | 尚未实现的可选增强 |

## 开发

常规检查需要 Go 和 Node。默认流程只执行核心回归；本地 CLI、进程恢复和完整业务链路单独执行 integration：

```sh
make test
go vet ./...
node --test internal/room/web*_test.mjs
```

涉及 CLI、平台或完整业务流程时，按修改范围执行常用 integration，例如：

```sh
make test-integration TEST_PACKAGES=./internal/room TEST_RUN=Lark
```

阶段性执行 `make test-integration` 覆盖全仓本地集成；修改并发、存储或资源持有逻辑时执行 `make test-race`。无需每次修改都串行运行这两套完整检查。

按模块运行、计时及测试分层约定见[测试说明](docs/testing.zh.md)。代码生成、CLI 夹具及真实服务验收入口见[验证说明](internal/room/README.zh.md#验证入口)。

当前 GitLab 工具、Workspace Git、浏览器操作和真实团队协作仍有专项待验项，范围见[当前待验项](internal/room/README.zh.md#当前待验项)。普通测试通过不代表这些真实链路已全部验证。
