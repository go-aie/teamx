# Scion 并行团队与环境准备

日期：2026-11-13。范围：[Room 调度与 Scion 并行开发方案](../ROOM-SCHEDULING-AND-SCION-PARALLEL-DEVELOPMENT-PLAN.zh.md)阶段 2。模板、真实平台绑定和独立工作副本准备已完成；阶段 3 的模型并发编码、远端发布和集成验收尚未开始。

后续状态：阶段 3 已完成，见[共享账号真实并发验收](scion-parallel-acceptance.zh.md)。本文保留阶段 2 完成时的范围与证据。

## 团队模板

新增 [scion-parallel-development](../../.teamx/teams/scion-parallel-development/README.md)，包含 `team.json`、README、architect 提示词和三个开发成员共用的 developer 提示词。四个独立成员为 `architect`、`developer-a`、`developer-b`、`developer-c`，创建时冻结 `scheduling: parallel`。原 `software-development` 模板保持串行。

architect 主持每轮拆解并发布带版本的设计／任务表，明确唯一负责人、接口、文件范围、依赖和完成标准。独立任务并行，依赖任务取得准确交付版本后开始。没有独立 reviewer 或全房间逐阶段评审门禁；developer-a 在全部交付后负责独立集成任务分支及整体验收，architect 核对需求覆盖。集成职责不包含目标分支合入权限。

README 说明共享现有平台、逐成员启用工具、独立 Workspace、完整交付格式、作者署名与认证主体的区别，以及后续真实并发验收。模板不包含凭据、具体 Room、平台绑定或测试仓库记录。发行显式清单已加入四个资源文件及团队 ID，未编辑生成副本，也未重新构建发行包。

## 实际环境

复用现有 Scion 平台 `1de9e1b042fb418b8fa3acb950a1e004`，实例 `https://scion.opencsg.com`，受管 profile 不变，实际 subject 为 `csgship`。没有新建平台记录、复制 PAT 或签发账号。

通过正常创建／打开、工具绑定及 Workspace 应用接口准备持久 Room：

- Room ID：`9e485abe9cac7bc739ec0980a1ac8d82`
- Room instance：`bda6755bf2e43649c27752ea15f6edc0`
- 团队：`scion-parallel-development`；模式：`parallel`
- 仓库：`OpenCSG/teamx-demo`；基线分支：`main`
- 完整 Scion 基线：`c1078e705cc235a88699caf8aaf51c45803df33aafb8849ea36949756f8a36d4a0`
- Workspace 操作：`96b3e35578e9e54bb5f6d7bd1dc6e03b`，已完成并提交配置，四个成员引用均已更新。

各成员独立绑定同一个平台；Workspace 初始化的平台选择没有代替成员工具启用。路径共同前缀为 `.teamx/data/rooms/9e485abe9cac7bc739ec0980a1ac8d82/workspaces/repos/96b3e35578e9e54bb5f6d7bd1dc6e03b/`，后接各自 membership ID：

| 成员 | Membership ID／独立路径后缀 | 正常工具入口 subject | 提交作者安排 |
| --- | --- | --- | --- |
| architect | `da35fd77d7dda8a782d296697436961f` | `csgship` | `TeamX architect <architect@teamx.local>` |
| developer-a | `2b4d31d6dee6b7af0c54089c9c0ccdd2` | `csgship` | `TeamX developer-a <developer-a@teamx.local>` |
| developer-b | `b4612d03299b84689cb53a00f5f5b1ce` | `csgship` | `TeamX developer-b <developer-b@teamx.local>` |
| developer-c | `5c4c1f7bde06afd028256892e89c8f61` | `csgship` | `TeamX developer-c <developer-c@teamx.local>` |

四个成员分别通过实际 `teamx tools run <tool-entry-id>` 子进程和带成员身份的 HTTP 工具入口执行 `whoami`、`repo info`、`status`。均确认账号与实例正确、canonical repo 一致、基线相同、处于 `main`、无 pending／dirty／冲突；各自 `.scion/state.json` 的仓库、实例、基线及 membership 路径也已核对。Session 状态按 membership 分目录；本阶段没有打开模型回合。

只读组织成员查询确认 `csgship` 在 `OpenCSG` 的角色为 `owner`，仓库未归档，`repo rules` 返回空规则；具备准备测试所需的组织开发角色，未通过试写探测权限。该角色不表示本轮新增了系统管理员权限，也不构成目标分支写入授权。

后续任务分支约定为 `teamx/<完整 room_id>/<完整 membership_id>/<task>`，设计、三个开发任务及 developer-a 集成任务分别使用独立任务标识。本阶段只确定命名和作者安排，没有创建或发布任务分支。阶段 3 实际写入前仍需核对仓库、当时基线及允许发布的具体任务分支范围。不同 `--author` 不改变共享认证主体。

## 验证与证据边界

以下验证通过：

```sh
go test ./internal/room -run '^TestTeam' -count=1
go test -race ./internal/room -run '^TestTeam' -count=1
go test -race -tags=integration ./internal/room -run '^(TestManagedTeam|TestScionWorkspaceApplyThreeMembersAndLocalRevisit)' -count=1
node --test scripts/package-lib.test.mjs
node scripts/check-packaging.mjs --target darwin-arm64 --profile standard
```

模板快照测试覆盖实际资源加载、四个独立成员及不同角色身份、两次创建的 membership／Workspace 隔离、并发模式传入运行配置、模板移除后可恢复，以及模板不自动授予平台绑定。打包检查使用临时 Git index 纳入新增模板，工作区实际暂存区未改变；结果为 4 targets、5 teams 的显式清单有效，不代表完整发行包验收。

真实准备入口为 `TestScionParallelDevelopmentEnvironment`（`e2e` 标签），需显式启用、提供已停止服务的现有配置、平台 ID、仓库、创建 ID、绝对 CLI 路径和全新的报告目录。它复用实际受管 profile 和真实 scn 0.1.13，运行真实创建、绑定、clone 及成员 CLI/HTTP 链路。测试驱动按生产协议签发短时成员执行凭据，不经模型触发；未替换 Scion 或 Workspace 实现。Workspace 完成回执出现后还等待运行时恢复结清，才继续使用该 Room。

本机最终非秘密结果及原生读回证据保存于 `.local/room-scion-parallel/evidence-final/report.json`；此文件不含令牌。准备期间短暂停止本地服务，结束后恢复 `bin/teamx serve --config .teamx/config.toml`，实际目录已加载新团队，原房间仍可用，新 Room 的 `parallel` 模式和四个绑定均保留。公共消息数、各成员回合数均为 0。

证据包括组件、race、本地配置集成及真实 Scion 只读／clone；没有真实 DSH 模型执行、远端业务写入或浏览器验收。本阶段没有创建 Review、发送评论、合入、关闭或部署，也没有提交／推送本仓库代码。阶段 3 的实际执行重叠、远端交付、Room 消息交接和最终集成仍待单独实施；多 subject 隔离继续作为后续可选项。
