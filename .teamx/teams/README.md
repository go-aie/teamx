# 团队配置

默认 `teamx serve` 从 `~/.teamx/config.toml` 旁的 `teams/` 读取各子目录的 `team.json`。项目内运行时指定 `--config .teamx/config.toml`，使用仓库中的 `.teamx/teams/`；也可通过 `server.teams_dir` 指定其他目录。团队目录与 DSH、模型和环境配置的使用方法见 [README](../../README.md#快速开始)和[配置示例](../config.example.toml)。发行时一并提供示例及团队模板，用户目录中的副本可自行编辑。

一个团队包含 `id`、`name`、`description`、`opening_message`、`members`。省略 `runtime` 时采用全局默认；如需不同模型或 DSH，可提供包含 `dsh` 和 `model` 的完整 `runtime`，整体替换默认值，不逐字段合并。两处均未提供完整 runtime 时启动失败。完整示例见各子目录。团队 ID 和成员 ID 使用 1—64 位英文字母、数字、`-`、`_`；团队 ID 在整个目录内唯一，成员 ID 在团队内唯一。目录名可以与 ID 不同。

[个人助手](personal-assistant/README.md)提供单个 `assistant` 成员，用于识别 GitLab 个人待办、在实际工具能力内代办常规事务，以及在需要本人介入时在房间里 @ 用户。个人待办可独立配置，无需 Workspace；跨项目回复等能力边界见该团队说明。

- `runtime.dsh`：普通安装只需 `binary_path`，填写完整安装的 `dsh` 可执行程序绝对路径（可用 `command -v dsh` 查找）。TeamX 通过 `--version` 读取安装版本，自动追加 `--profile acp` 启动会话，并将实际身份记录在私有 Session 中；恢复前核对版本及 ACP 运行时身份。开发时也可用 Node 加 `binary_args` 中的 JS 参数启动，JS 路径必须为绝对路径，且需保留完整依赖。`artifact` 为可选的构建来源标注，显式提供时必须同时包含 `source`、`version`，并参与恢复核对；普通用户无需填写。
- `runtime.model`：`protocol` 为 `openai-completions` 或 `deepseek-messages`，包含 `model_id`、`api_key_env` 和可选 `base_url`；Completions 必须配置地址。仅保存密钥环境变量名称。
- `runtime.model.context_window`、`runtime.model.max_tokens`：可选的上下文容量和单次生成 token 上限。Completions 未配置时保持原默认值 65,536 / 4,096；显式值须为正整数，输出上限不能超过上下文容量。`context_window` 仅用于 Completions；`max_tokens` 也可配置 DeepSeek Messages，未设置时沿用 DSH 默认。容量须与模型和实际网关一致，配置不会扩大服务端容量。例如 DeepSeek V4 Flash 可按实际服务配置 1,048,576 / 32,768；代理网关如有更低限制须相应下调。
- `runtime.model.reasoning_effort`：可选，省略或空字符串保持原 DSH / 网关默认行为。DeepSeek 支持 `off`、`low`、`high`、`max`；其他 Completions 模型还可填写 `minimal`、`medium`、`xhigh`，实际支持以模型和网关为准，配置不会验证服务端能力。`low` 用于减少思考，`off` 用于关闭。DeepSeek Messages 传给 DSH 的 `reasoningEffort`；Completions 传给 provider 的 `reasoning` 并声明模型的档位。模型 ID 以 `deepseek-` 开头时，还配置 `thinkingFormat: deepseek`，使 `off` 明确发送 `thinking: {type: disabled}`，其他档位发送 `thinking: {type: enabled}` 和 `reasoning_effort`，并保持 `system` 提示角色。其他 Completions 模型的 `off` 不发送 reasoning 字段，是否关闭取决于接口默认行为。单房间旧配置同样可在各 profile 的 `model` 中设置该字段。
- `members[]`：`id`、`name`，且必须只指定 `instructions` 或 `instructions_file` 之一。文件相对于 `team.json` 所在目录解析。
- `runtime.model.compat.supports_developer_role`：可选布尔值，仅支持 `openai-completions`。设为 `false` 时，DSH 对该精确模型保持 `system` 提示角色，适用于拒绝 `developer` 角色的兼容网关；省略保持原默认，显式值不关闭 `reasoning_effort`，也不会清除 DeepSeek 的 `thinkingFormat`。JSON 例如 `"compat": {"supports_developer_role": false}`；全局 TOML 写在 `[runtime.model.compat]` 段。该字段随房间快照冻结，改变会影响 Session 恢复匹配。
- `tool_env` 为成员可选的字符串映射。以 `./` 开头的值表示团队内的资源路径，如 `"HELPER": "./tools/helper.py"`；创建时检查资源存在，打开时解析到房间内的资源副本。其他值按字面量传递，不展开环境变量或 shell 表达式。凭据继续从环境继承。不能覆盖 TeamX 身份、`TEAMX_CLI`、DSH 保留环境或模型密钥绑定。
- `workspace`：可选，`root` 默认为 `./workspaces`，只接受房间内相对路径；各成员使用 `<root>/<membership_id>/`，不是共享目录。`repository`、`binding` 作为设置入口的建议值预填，但创建时只采用根路径，不初始化仓库或 GitLab。模板不能保存 PAT 或房间凭据引用。[Workspace](../../docs/workspace.zh.md)通过输入框下方 Workspace 设置入口显式应用；建议值只预填文本，不自动勾选或执行绑定。

每个团队统一使用一套 runtime；不支持成员模型覆盖、跨团队引用或继承。工作区、成员代际及 Session 目录由服务生成。每个房间保存 runtime、展开后的指令及资源副本，返回旧房间或重启时不把模板合并到生效配置；设置弹层可读取当前可用模板的建议值。配置只在服务启动时加载，编辑模板后重启才影响后续新建。

例如，减少 OpenAI 兼容 DeepSeek 网关的思考量，可在全局 `[runtime.model]` 设置 `reasoning_effort = "low"`（或 `"off"`）。团队完整覆盖 runtime 时，其 `model` 部分可写为：

```json
{
  "protocol": "openai-completions",
  "base_url": "https://model.example/v1",
  "model_id": "deepseek-v4-flash",
  "api_key_env": "OPENAI_API_KEY",
  "reasoning_effort": "low"
}
```

此参数随房间快照和私有 Session 配置冻结；修改模板后须重启服务并新建房间。不要直接修改已有房间或 DSH Session 的配置文件，原 Session 恢复会拒绝配置不匹配。未设置该字段的旧房间继续使用原配置与恢复指纹。

出现 `DSH stopped: max_tokens` 时，应区分单次输出触顶和私有上下文已满。DSH 的 Completions 适配层会按配置的上下文剩余量压低输出额度；若输入已超过配置窗口，可能连续只生成 1 token 就停止，反复启用不能解决。已有房间保留创建时的 runtime，新参数需重启服务后新建房间；继续已有开发时在新房间明确指定已有 MR/分支并要求先检查实际状态。不要直接修改旧 Session 的生成配置或恢复指纹。临时释放旧房间单个成员的私有上下文，可使用操作者 CLI 的 `member session rotate --agent developer --cancel`，等待轮换完成后再显式启用；公共历史和工作区保留，私有上下文被重置，原 token 配置不变。

团队 JSON 上限 1 MiB；每个团队最多 32 个成员，团队目录最多 32 个团队，资源最多 1024 个文件、合计 4 MiB。拒绝符号链接和目录穿越；隐藏目录及 `__pycache__` 不打包，请不要从这些目录引用提示词或工具。无需将项目仓库、模型产物或凭据文件放进团队目录。新建管理房间仅使用普通目录；GitLab 共用资源由服务内适配器提供，团队无需包含脚本。旧软件开发房间保留原冻结工具和工作区，但打开要求显式迁移或新建房间，不再运行团队专用准备脚本。
