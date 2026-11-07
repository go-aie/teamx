# Scion S4：通用软件开发团队与版本化协作

日期：2026-11-07。范围为 [接入方案](SCION-SOFTWARE-DEVELOPMENT-PLAN.zh.md) 第 9.0 节的 S4 POC。

结论：**S4 已实施，本地定向回归及真实 DSH 开发 → Review 创建 → 独立评审 → 修正 → 原 Review revision 更新 → 复审闭环通过；实际评论响应丢失后的只读核查通过。** 模型网关多次限流后，通过核实结清及正常恢复入口分段续验完成，不声称连续无干预协作通过。实际浏览器操作仍待验。

## 交付

复用现有 `software-development`，没有新增团队或改变默认 opening message。

- [三角色模板](../.teamx/teams/software-development/prompts/)共用职责、简单/复杂需求分流、分步评审、完整版本交接与用户决定规则。平台对象统称平台评审，按当前 Workspace 与显式绑定选择 GitLab MR / Scion Review，保留各自原生提交 ID 和命令语义。
- [团队 README](../.teamx/teams/software-development/README.md)说明两种配置路径，以及 Scion 任务分支发布、Review 创建、独立检出测试、版本化普通评论、原 Review 更新与复审。新模板只用于新 Room；已有角色、成员、Session 快照不自动修改。
- [Scion skill](../internal/platform/tool/skills/scion/SKILL.md)补充完整 changeset 前缀、原生网页路由，以及不能把父目录 Git 仓库当成当前项目。固定 v0.1.13 的网页路由依据为本地 tag 的 `web/src/lib/routes.ts`；CLI Review JSON 没有 URL 字段不代表创建失败。
- [动态运行指引](../internal/room/dsh.go)将固定查询 MR/SHA 改为核对当前平台对象、状态及原生 revision/base/head。GitLab 专属 action 适配器说明仍只描述其实际能力，不把 Scion 伪装成 GitLab。
- [真实协作验收](../internal/room/scion_collaboration_real_test.go)使用生产模板、正常 Room/Workspace/成员绑定、真实 DSH 和原生 CLI，默认跳过。另有[显式续验入口](../internal/room/scion_collaboration_continue_real_test.go)：旧执行必须通过 `CheckReset`，先读现有 Review/评论，再经正常 enable/rotation gate 继续；不重建 Review、不重放未知写入，不修改冻结配置。续验逐成员执行以降低模型服务压力。

本阶段不实现 approve/request-changes、Review 合入或关闭，不接通知、Inbox、followups、Git gateway 或第二种登录路径。

## 本地验证

通过：

```sh
go test -race ./internal/room \
  -run '^(TestTeam|TestManagedTeamRuntimeFrozenAtCreation|TestModelModalitiesCopies|TestDSHAdapter|TestScion|TestWorkspace|TestPreparation|TestCommonPreparation)' \
  -count=1 -timeout 5m
go test ./internal/platform/scion ./internal/platform/tool ./tools/scion ./internal/workspace/... -count=1 -timeout 2m
go test ./internal/platform -run '^TestCapabilityImportBoundaries$' -count=1
go vet ./internal/room ./internal/platform/tool ./internal/platform/scion
node --test internal/room/web*_test.mjs
git diff --check
```

Room 定向 race 回归通过，未执行完整 Room suite。页面测试首次 176/177，一项 GitLab 换号展示断言失败；该项独立复跑和完整复跑均通过，最终 177/177。不把页面事件测试写成实际浏览器验收。

## 真实实例记录

使用固定 `scn 0.1.13`、S0 原生 PAT profile，实际账号 `csgship`，实例 `https://scion.opencsg.com`，仓库 `OpenCSG/teamx-demo`，目标 `main`。新的软件开发 Room 为 `b2cfa55b2e027ee209893f5024be2288`；三个 membership 各有一份独立原生 clone，逐成员显式绑定。真实模型为配置中的 `deepseek-v4.1-flash-1017`，续验冻结的单次输出上限为 8192，未改用户模型配置。

简单需求为标准库 Python Todo 看板。architect 未发布额外确认或重复评审；developer 自主发现工具、读取 skill、建立任务分支、实现和测试，通过原生 commit/land 发布并创建 [Review #1](https://scion.opencsg.com/OpenCSG/teamx-demo/reviews/1)。任务分支为 `room-b2cfa55b-6c78ab16-scion-s4-b2cfa55b`。

| 版本 | 完整 base | 完整 head | 实际证据 |
| --- | --- | --- | --- |
| revision 1 | `c1078e705cc235a88699caf8aaf51c45803df33aafb8849ea36949756f8a36d4a0` | `c15c895f89f0fb5495eefaf32ebdaba6e52e1df63375ee4817ce89aaba8ffdb3dd` | developer 11 项测试；reviewer 在独立 detached 副本以 Python 3.14.3、3.9.13 各跑 11 项测试，普通评论 ID 259、作者 csgship、revision 1、完整正文已回读；Room seq 4 一致 |
| revision 2 | `c1078e705cc235a88699caf8aaf51c45803df33aafb8849ea36949756f8a36d4a0` | `c1058f25c5b9a894e2b1e0b641d4e31d044bbbe91d00d74468455090704d5ad21c` | 原分支三文件修正、原 Review push 更新；reviewer 在独立副本以 Python 3.14.3、3.9.13 各跑 14 项测试；普通评论 ID 260、作者 csgship、revision 2、完整正文已回读；Room seq 7 待复审与 seq 8 复审结果一致 |

首轮评审无阻塞。随后补充 stats 需求，要求总数/未完成数/已完成数及空列表、完成后统计测试，明确更新原 Review；这是补充验收触发的修正，不声称首轮发现了阻塞缺陷。

模型网关多次返回 HTTP 429：首轮开发前的另一次尝试只建立了任务分支并留下本地修改，没有 commit/Review；本 Room 的 developer 已创建 Review 后，其他模型调用被限流。续验先确认旧进程结清，reviewer 完成首轮评审；developer 又在完成三文件修正和两种 Python 下各 14 项测试后、提交前遇到 429。所有现场保留，未绕过恢复门禁或重建交付。最后一次续验通过，原分支发布并更新 Review #1 至 revision 2，reviewer 完成新版本复审；`TestScionRealDSHReviewContinuation` 返回 PASS（约 139 秒）。architect 的模型曾被限流暂停，因此“未额外发言”不作为其复杂设计或沉默语义的完整模型验收。

首轮 developer 因 CLI 无 URL 字段，交接包含对象和完整版本但未给网页链接；续验给出经原生路由核实的链接，并补充到按需 skill。首次故障包装器误将 `review comment --help` 纳入响应丢失，已修正为只对实际评论写入生效；帮助响应丢失不计作未知写入验收。正确注入发生在 revision 2 的真实评论：原生写入成功后隐藏 stdout、返回退出码 4 和结果未知提示；reviewer 只读查询 `review comments`，确认评论 260 的作者、正文、版本和唯一性后发布 Room 结果。原生调用日志只有两次实际 comment（每个 revision 一次，排除帮助）；没有重复评论。

私有证据位于 Git ignore 的 `.local/scion-s4-20261107-*/`，其中 `-4` 保存原 Room 与工作副本，`-6/first_review.json` 保存首轮真实模型测试/评论/回读证据，`-8/report.json` 为最终 PASS，另有修正与复审的实际工具诊断。main 始终为最初基线，Review 保持 open、未 approve、未 landed。凭据仅在私有 profile 和进程中使用，不进入本文或公共配置。

启用新验收（会在指定 demo 进行真实分支、提交、Review 和普通评论写入）：

```sh
TEAMX_SCION_S4_REAL=1 TEAMX_SCION_S4_MAX_TOKENS=8192 \
TEAMX_SCION_CONFIG=/absolute/config.toml \
TEAMX_DSH_ENV_FILE=/absolute/private/model.env \
TEAMX_DSH_OUTPUT=/absolute/new-evidence-dir \
TEAMX_SCION_TEST_CREDENTIALS=/absolute/private/credentials.json \
TEAMX_SCION_BIN=/absolute/teamx-scion TEAMX_SCN_BIN=/absolute/scn \
  go test -tags e2e -v ./internal/room -run '^TestScionRealDSHCollaboration$' -count=1 -timeout 25m
```

失败后先检查原 Room、分支、pending、Review 和评论，不能直接重跑新建流程。显式续验入口仅针对已核实的既有 Review #1；`TEAMX_SCION_S4_SOURCE` 指向原证据根。若首轮评论已确认而修正尚未发布，另以 `TEAMX_SCION_S4_START=correction` 和 `TEAMX_SCION_S4_PRIOR_EVIDENCE` 引用首轮模型证据；保留工作副本，通过正式恢复门禁继续。本次最终续验使用这一入口，未修改冻结角色或模型配置。

## 尚未完成的验收

- CUA `getState()` 本次再次超时并重置，没有取得页面状态；实际连接、工具绑定和 Workspace 页面基本浏览器验收未通过。
- 连续自动调度仍受模型网关限流影响；本次证明了分段恢复后的真实闭环，不证明该网关下三成员连续并发可用。
- 多任务、多账号、完整故障矩阵、复杂设计交接及 S5 合入不在本次完成依据内。
