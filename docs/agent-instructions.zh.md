# Agent Instructions 的组装

TeamX 将稳定指引组成一份 Session Instructions，由 DSH 写入 Session 专属的 `home/AGENTS.md`。每轮 prompt 只提供动态状态和本轮材料，不重复追加整份固定指引。固定指令仍属于模型上下文，减少的是每轮新消息中的重复副本。

## 内容与来源

| 内容 | 来源 | 用途 |
| --- | --- | --- |
| 公共指引 | [`agent-guide.md`](../internal/room/prompts/agent-guide.md)，通过 `go:embed` 编入 TeamX | 所有成员通用的参与、阅读、授权、规则维护、动作与工具协议 |
| 角色指令 | 团队成员的 `instructions` 或 `instructions_file`，创建房间时保存展开后的文本 | 角色职责、介入条件、业务流程及交付要求 |
| Session 身份 | Go 根据当前 Room、Agent、Membership 和操作者生成 | 明确执行身份及提问时应提及谁 |
| 轮换状态 | Go 根据当前 Session 目录代际生成 | 标明私有上下文已重置；如何恢复共享上下文由公共指引说明 |
| 本轮上下文 | 当前轮次、长期规则上下文、未读数量、Inbox 状态和可用图片材料 | 本轮需要处理的状态与事实 |

公共指引说明“如何使用 TeamX”，角色指令说明“这个成员负责什么”。角色的权限和任务条件仍须满足，拥有工具能力不等于获得业务授权。按需读取的 CLI `--help` 和 `tools skill` 负责字段、平台细节与原生工作流程，不另行拼入固定指令。

## 角色模板的职责边界

角色文件保留四类内容：角色身份与关注点、介入条件、必要的团队场景及业务限制、交付与停止条件。公共指引负责通用参与和沉默原则、只代表自己发言、区分事实与建议、提问和授权、读取与发送协议；角色文件不再重复这些说明。平台命令、账号绑定、JSON 字段和未知写入处理留给公共指引及按需帮助。

| 团队 | 角色文件需要保留的内容 |
| --- | --- |
| 书店夜场 | 统筹、策划、运营、体验的不同视角；活动规模、预算、人手及空间；不强制社交、优先减轻负担、何时收口 |
| 外卖讨论 | 选菜、独立核算、口味需求的分工；以用户菜单和人数为准；模拟讨论、不联网查价或实际下单 |
| 软件开发 | 简单任务直接实施、复杂任务先设计；完整版本交接、逐步独立评审；各角色的修改与合入边界 |
| 个人助手 | GitLab 个人待办的代办职责；需要用户判断的事项、实际账号区别、业务核实后才完成待办 |

各成员的 Session 相互隔离，必要的团队背景和业务约束可在角色文件中各保留一份短摘要，不能假设会读取同伴的提示词。这样的重复让每个成员获得完整任务边界，不是同一 Session 内的重复注入；当前不增加团队共享指令字段或文件 include 机制。

精简时优先删除公共规则和同义反复；涉及真实业务边界的条件应保留。例如，不能把“简单任务无需设计”泛化为“所有任务都可直接做”，也不能将“本步无阻塞”当作整体完成。模板简洁不等于放宽授权或角色权限。

## 组装路径

```text
team.json 的 instructions / instructions_file
  → 创建房间时保存角色文本与资源快照
  → runtime_assembly.go 从房间配置建立 dshProfile.Instructions
                                      ┐
嵌入的 agent-guide.md                  ├→ dshBackend.componentOptions
当前成员身份与必要的轮换状态           ┘    → dsh.Options.Instructions
                                           → DSH Session 的 home/AGENTS.md

每轮：policy JSON + dshNotification + 本轮可用材料
  → session/prompt（沿用同一个 Session）
```

`componentOptions` 按“公共指引 → Session 身份 → 角色指令”的顺序组装，已轮换的 Session 另附状态提示。源码分开维护，运行时只有一份合成 Instructions；`AGENTS.md` 是其生成产物，不是另一套需要人工同步的指令。不要用段落顺序处理互相矛盾的协议，应修正源文件中的重复或过时说明。

旧单房间模式从 `--dsh-config` 中指定 profile 的 `instructions` 取得角色文本，后续组装相同。

主要实现位置：

- [teams.go](../internal/room/teams.go)：读取团队角色文件并建立房间快照。
- [runtime_assembly.go](../internal/room/runtime_assembly.go)：从已保存成员配置建立运行 profile。
- [dsh.go](../internal/room/dsh.go)：组装固定 Instructions、生成通知和每轮 prompt。
- [DSH state.go](../internal/agent/dsh/state.go)：写入 `AGENTS.md`、计算配置指纹及核对恢复条件。

## 每轮提供什么

每轮保留 `current_turn`、policy revision/token、Room 未读数量和 Inbox 批次可用性。完整身份仍随 policy JSON 提供，读取范围由服务端固定，Agent 无需从通知文本自行计算。

长期规则正文在新建、恢复或轮换后的供给作用域中首次提供；有效规则 token 改变时再次完整提供。连续成功回合中 token 未变，仅提供上下文元数据。`effective` 出现时整体替换旧集合，包括空集合；Agent 缺少对应正文时可用 `policy read` 恢复。失败回合不视为已成功供给。

可用图片材料按本轮固定读取范围另附；第三方材料警示、供应状态和不完整覆盖说明仍随材料提供。它们描述本轮证据，不替代授权，也不会自动消费 Room 或 Inbox。

## 修改与生效边界

- 修改公共指引需要重新编译 TeamX；公共指引没有额外的部署文件依赖。
- 修改团队角色模板，重启加载后只影响后续新建房间；已有房间继续使用保存的角色文本。单纯轮换 Session 不会重新读取最新团队模板。
- 合成 Instructions 参与 DSH 配置指纹。公共指引、角色文本或其他固定内容变化后，旧 Session 配置不匹配时沿用现有拒绝恢复机制，不自动修改生成文件、替换 Session 或重放旧操作。需要通过现有显式轮换流程建立新 Session；轮换保留公共历史、Workspace 和身份，重置私有上下文。
- 本次将公共指引从每轮 prompt 移入固定 Instructions，会改变旧版 Session 的指纹；不会自动迁移已保存的角色文本。采用清理后的模板需新建房间，不能将轮换误当成模板更新。

操作入口见[运行手册](../internal/room/README.zh.md)，配置来源见[团队配置](../.teamx/teams/README.md)。

## 维护与后续改进

1. 公共规则只维护在 `agent-guide.md`；角色文件保留特有职责、参与条件和业务流程，避免复制 CLI 字段或公共安全约束。
2. 平台命令、JSON 字段、可用动作和重试细节由当前帮助、动作解析结果及工具 skill 说明；协议变化时同步检查这些按需入口。
3. 本地检查应覆盖固定指引只进入 Session、连续回合不重复注入、角色与身份保持隔离，以及规则上下文和图片材料仍按原协议供给。
4. 固定指令在真实 DSH 恢复及上下文压缩后的可见性、有效性专项验收留待后续。本次沿用现有 Instructions / AGENTS.md 机制，不新增首轮标记、压缩回调或补注入策略。

这里记录当前实现与生效边界；后续若引入房间级指令编辑或公共指引版本管理，应同时明确快照、升级和 Session 轮换策略。

## 长期规则验证

自然语言规则的真实 DSH／模型固定场景和独立进程正常启动／恢复已有通过记录；平台写入边界使用本地 contract fixture。实际浏览器、真实平台写入和旧数据清理后的正式启用仍需分别核验。

下面的入口不触碰正式数据，输出目录须为尚不存在的绝对路径；模型及密钥来自显式配置和环境，`TEAMX_N3_MODEL` 可覆盖验收模型：

```sh
TEAMX_N3_PROCESS=1 TEAMX_N3_CONFIG="$PWD/.teamx/config.toml" \
TEAMX_DSH_OUTPUT=/absolute/new-rules-process \
go test -tags=e2e ./internal/room -run '^TestNaturalRulesBrowserProcessRecovery$' -count=1 -timeout=3m -v

TEAMX_N3_DSH_REAL=1 TEAMX_N3_CONFIG="$PWD/.teamx/config.toml" \
TEAMX_DSH_ENV_FILE="$PWD/.env" TEAMX_DSH_OUTPUT=/absolute/new-rules-model \
go test -tags=e2e ./internal/room -run '^TestNaturalRulesRealDSH$' -count=1 -timeout=23m -v
```

真实浏览器模式将 `TEAMX_N3_PROCESS=1` 换成 `TEAMX_N3_BROWSER=1`，超时改为 `22m`。打开 `browser-ready.json` 的 endpoint，在「长期规则」新增 `AgenticHub 产品负责人是 huayi` 和 `AgenticHub 创建智能体时报错优先建议找 hongwei 和 luojun。`；将第二条改为 `AgenticHub 创建智能体时报错，优先建议找 hongwei 和 luojun；联系前先询问。` 后撤销。核对正文、来源、有效期、v1／v2／v3 修订和第一条仍有效。完成实际操作后才写 `browser-done.json` 为 `{"status":"passed"}`；夹具重启后打开 `browser-restarted.json` 的新 endpoint，再核对状态和完整历史，完成后写同格式的 `browser-restart-done.json`。失败写 `failed`，页面截图与后端持久化核对分别保留。

旧结构不兼容且 `policy.json` 仍为版本 1。启用前由操作者停服并核实准确的数据目录，按已选择的重建方式处理旧规则、含旧规则快照的事项／动作和旧 Session；仅删除 `policy.json` 不够，不因本说明自动清理数据。核对实际可用模型后使用干净数据重建规则。模型固定样本通过不保证自然语言语义永不误判；真实平台动作仍须独立核实目标、账号和具体授权。
