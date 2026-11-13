# 团队配置

默认 `teamx serve` 从 `~/.teamx/config.toml` 旁的 `teams/` 读取各子目录的 `team.json`。项目内运行时指定 `--config .teamx/config.toml`，使用仓库中的 `.teamx/teams/`；也可通过 `server.teams_dir` 指定其他目录。团队目录与 DSH、模型和环境配置的使用方法见 [README](../../README.md#快速开始)和[配置示例](../config.example.toml)。发行时一并提供示例及团队模板，用户目录中的副本可自行编辑。

一个团队包含 `id`、`name`、`description`、`opening_message`、`members`。通常省略 `runtime`，使用全局 DSH 安装；模型由成员的 `model_selection` 引用集中列表。旧模板内嵌的模型会在一次迁移中转为引用，原 DSH 安装保留。完整示例见各子目录。团队 ID 和成员 ID 使用 1—64 位英文字母、数字、`-`、`_`；团队 ID 在整个目录内唯一，成员 ID 在团队内唯一。目录名可以与 ID 不同。

[个人助手](personal-assistant/README.md)提供单个 `assistant` 成员，用于识别 GitLab 个人待办、在实际工具能力内代办常规事务，以及在需要本人介入时在房间里 @ 用户。个人待办可独立配置，无需 Workspace；跨项目回复等能力边界见该团队说明。

- `runtime.dsh`：普通安装只需 `binary_path`，填写完整安装的 `dsh` 可执行程序绝对路径（可用 `command -v dsh` 查找）。TeamX 通过 `--version` 读取安装版本，自动追加 `--profile acp` 启动会话，并将实际身份记录在私有 Session 中；恢复前核对版本及 ACP 运行时身份。开发时也可用 Node 加 `binary_args` 中的 JS 参数启动，JS 路径必须为绝对路径，且需保留完整依赖。`artifact` 为可选的构建来源标注，显式提供时必须同时包含 `source`、`version`，并参与恢复核对；普通用户无需填写。
- `scheduling`：可选，省略或空字符串为 `serial`，整个 Room 最多一个成员执行；`parallel` 允许不同成员同时执行，每个成员仍最多一个回合。创建时保存有效模式，已有 Room 沿用快照，运行中不能切换。[Scion 并行开发](scion-parallel-development/README.md)提供四成员并发编码示例；原软件开发团队保持串行。
- `members[]`：`id`、`name`，且必须只指定 `instructions` 或 `instructions_file` 之一。文件相对于 `team.json` 所在目录解析。角色文件只需描述职责、参与条件和业务流程；公共 TeamX 指引由程序统一组装，见 [Agent Instructions 的组装](../../docs/agent-instructions.zh.md)。
- `tool_env` 为成员可选的字符串映射。以 `./` 开头的值表示团队内的资源路径，如 `"HELPER": "./tools/helper.py"`；创建时检查资源存在，打开时解析到房间内的资源副本。其他值按字面量传递，不展开环境变量或 shell 表达式。模型凭据由服务按 Session 的不可变引用注入。不能覆盖 TeamX 身份、`TEAMX_CLI`、DSH 保留环境或模型密钥绑定。
- `workspace`：可选，`root` 默认为 `./workspaces`，只接受房间内相对路径；各成员使用 `<root>/<membership_id>/`，不是共享目录。`repository`、`binding` 作为设置入口的建议值预填，但创建时只采用根路径，不初始化仓库或 GitLab。模板不能保存 PAT 或房间凭据引用。[Workspace](../../docs/workspace.zh.md)通过输入框下方 Workspace 设置入口显式应用；建议值只预填文本，不自动勾选或执行绑定。

同一团队的成员可以选择不同模型。省略 `model_selection` 时使用默认；固定引用示例为 `"model_selection": {"mode":"explicit","entry_id":"vision"}`，其中 ID 必须来自全局列表，且不能处于待删除状态。模板只初始化新成员，后续不会覆盖已有成员的选择。工作区、成员代际及 Session 目录由服务生成。每个房间保存 runtime、展开后的指令及资源副本，返回旧房间或重启时不把模板合并到生效配置；设置弹层可读取当前可用模板的建议值。配置只在服务启动时加载，编辑模板后重启才影响后续新建。

编写角色提示词时，集中描述身份与关注点、何时介入、必要场景和业务限制、交付及停止条件。公共的发言、提问、授权、读取和工具协议由 TeamX 统一提供，不需在每个角色复制；平台细节使用对应工具 skill 与帮助。成员 Session 独立，团队背景可各保留短摘要，避免依赖同伴上下文。各团队的具体边界见 [角色模板说明](../../docs/agent-instructions.zh.md#角色模板的职责边界)。

模型地址、ID、API Key、推理强度和 developer 兼容性在「设置 → 模型」维护。保存前会真实验证执行参数；应用时各成员等待当前回合结束后切换，私有上下文重置。仅改名或固定引用当前模型不触发轮换。模板固定引用会列为删除阻断项，须先修正模板再删除对应条目。

出现 `DSH stopped: max_tokens` 时，应区分单次输出触顶和私有上下文已满。可在模型设置中核对实际网关的输出与上下文上限。不要直接修改 Session 生成配置或恢复指纹；需要清除单个成员的私有上下文时可使用现有 Session 轮换入口，公共历史和工作区保留。

团队 JSON 上限 1 MiB；每个团队最多 32 个成员，团队目录最多 32 个团队，资源最多 1024 个文件、合计 4 MiB。拒绝符号链接和目录穿越；隐藏目录及 `__pycache__` 不打包，请不要从这些目录引用提示词或工具。无需将项目仓库、模型产物或凭据文件放进团队目录。新建管理房间仅使用普通目录；GitLab 共用资源由服务内适配器提供，团队无需包含脚本。旧软件开发房间保留原冻结工具和工作区，但打开要求显式迁移或新建房间，不再运行团队专用准备脚本。
