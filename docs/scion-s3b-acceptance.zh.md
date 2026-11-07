# Scion S3b：原生 Workspace

日期：2026-11-07。范围为 [接入方案](SCION-SOFTWARE-DEVELOPMENT-PLAN.zh.md) 的 S3b。

结论：**S3b 已实施，组件、Git/GitLab 定向回归、三份真实原生 clone、受控失败/显式重试及正常管理器重启通过。页面自动化通过，实际浏览器操作待验。** 没有修改软件开发角色模板，没有创建提交、发布任务分支、创建/更新 Review、发表评论或合入。

## 交付

- [类型配置](../internal/room/workspace_backend_config.go)：`repository.kind=scion/git` 选择后端；缺省 kind 保持原 Git 语义，读取不重写旧快照。`binding.provider` 与后端分别校验，Scion 使用 canonical repository name，不使用数字 ProjectID；GitLab 原有身份要求保留在 [Git 配置边界](../internal/room/workspace_git_config.go)。不匹配或未知类型拒绝，不回退 Git。
- [ScionAdapter](../internal/workspace/scion/adapter.go)：原生 `clone URL MEMBER_DIR --ref VERIFIED_BRANCH`，核对本地 state/dirstate 的格式、endpoint、canonical repo 与完整 changeset ID。固定 v0.1.13 的 changeset 文本为 `c1` 加 64 位十六进制摘要；不接受 Git SHA、无前缀摘要或其他对象类型。首次 clone 额外核对初始 ref、非 detached、完整检出；回访允许成员自己的任务分支、dirty/pending 和 detached 状态。
- [平台仓库核验](../internal/platform/scion/repository.go)：实际读取 `repo info` 与 `branch ls`，核对原生 schema、canonical name、有效分支和非空 changeset。留空分支时核实默认分支，并冻结到原操作 Target；RequestTarget 保留初次请求，重复操作仍按原意图返回收据。目标分支不存在时，即使原生 clone 可退出 0，也不会开始初始化。
- [私有执行桥接](../internal/room/workspace_scion.go)：每次操作固定所选 profile，复制当前 origin 凭据，核对版本与实际 subject，再执行；临时 HOME 和环境不继承全局 Scion token、登录、配置或模型秘密。核验和 clone 均沿用 Workspace 持久 PID 跟踪、取消、进程组结清及平台连接调用集合，结束后清理凭据副本。账号变化拒绝业务。修正了旧项目连接复核中对 CLIProfile 指针地址的比较，改为完整值比较，保持连接代际与授权证据核对。
- [共用准备](../internal/room/workspace.go)与管理/操作记录继续由 S3a 接口驱动，无 Git/scn 命令或 `.git/.scion` 分支。先检查全部成员目标，再访问平台和 clone；非空普通目录、Git 目录、混合仓库、错误身份与符号链接拒绝。失败保留 `.clone-pending`，目标消失也不自动重试；已完成成员复用原副本。沿用原操作 ID、版本、配置提交与 Session 更新合同，没有新增恢复状态机。
- [页面](../internal/room/web/index.html)提供 Git / Scion 类型选择，从 URL 派生 origin 和仓库名，Scion 不追加 `.git`；选择已验证的 Scion 连接，展示账号。类型与已生效仓库锁定；错误、操作进度和显式重试沿用现有交互。通知的 GitLab/Lark 连接列表保持各自 provider，不混入 Scion。
- Workspace clone 连接不自动启用成员工具。日常工具仍逐成员显式绑定；作者使用每次 commit 的 `--author 'Name <email>'` 明确提供，不在临时 HOME 写永久作者配置。更新了按需 Scion skill、工具手册与 Workspace 文档中的初始化边界。

## 验证

主要命令：

```sh
go test -race ./internal/workspace/... ./internal/platform/scion ./internal/room \
  -run 'TestScion|TestWorkspace|TestPreparation|TestOAuth.*(Workspace|ManagedGit|RepositoryScope)|TestRepository|TestCommonPreparation' \
  -count=1 -timeout 5m
go test ./internal/platform/scion ./internal/platform/tool ./internal/platform/gitlab ./tools/scion -count=1 -timeout 2m
go test ./internal/platform -run '^TestCapabilityImportBoundaries$' -count=1
go vet ./internal/workspace/... ./internal/platform/scion ./internal/room
node --test internal/room/web*_test.mjs
git diff --check
```

| 层次 | 实际覆盖 |
| --- | --- |
| 配置与身份 | kind 缺省/显式 Git、未知 kind、kind/provider 不匹配、Scion 缺连接/错误 canonical repo/数字 ID、分支与完整原生 ID 校验 |
| 本地目标 | 三成员隔离、非空普通目录、Git/混合仓库、错误 endpoint/repo/format、无 base、缺 dirstate、元数据符号链接、首次错误 ref/detached/sparse 拒绝 |
| 平台前置检查 | 空分支列表、拼错分支、错误 canonical name、换号均拒绝；配置和操作列表不变，无 clone |
| 操作恢复 | 第二份 clone 失败且目标不存在时保留标记；直接 retry 拒绝；失败后正常管理器重启不重放，检查目标并移除其标记后显式 retry 原 ID；第一份文件保留 |
| 本地回访与取消 | 不调用平台或 clone、不重写冻结配置、不清除成员改动；阻塞 clone 经操作取消或 profile 轮换后 PID 记录、平台调用集合与临时凭据均结清 |
| 既有回归 | Git/GitLab Workspace、OAuth 仓库权限与受管 Git、准备进程、操作恢复/Session、仓库与平台架构边界 |
| 页面 | 全部 `web*_test.mjs` 通过；新增类型选择、已验证连接过滤、canonical URL、无数字项目身份、保存/重开和错误实例/未验证连接拒绝 |

没有运行完整 Room suite；上述定向回归不写成全量通过。原有测试中的真实外部场景默认跳过，实际启用结果单独记录如下。

## 真实实例与重启

[TestScionRealWorkspace](../internal/room/workspace_scion_real_test.go) 复用 S0 的已登录原生 PAT profile，在新的隔离服务根创建三成员 Room，经生产 Workspace HTTP 管理入口与固定 `teamx-scion → scn 0.1.13` 初始化。实际读取账号为 `csgship`，实例为 `https://scion.opencsg.com`，仓库为 `OpenCSG/teamx-demo`，默认分支为 `main`。

三个 membership 分别以 architect、developer、reviewer 命名；使用受控 Session factory，不启动真实 DSH 模型。三份原生 clone 的实际 base 均为：

```text
c1078e705cc235a88699caf8aaf51c45803df33aafb8849ea36949756f8a36d4a0
```

实际验证：

1. 拼错分支被前置检查拒绝，未预留操作或 clone。
2. 第二份 clone 在启动原生 clone 前注入受控失败，第一份为真实 clone；标记保留。未检查的 retry 不启动新 clone，检查确认目标不存在并仅移除相应标记后，原操作完成其余两份真实 clone，第一份 state 字节保持不变。
3. clone 不授予成员工具权限。逐成员显式绑定后，通过生产成员 CLI/HTTP 工具入口在各自 Workspace 执行原生 `--json status`。
4. reviewer 经其工具入口 `switch --detach FULL_BASE`，developer 留下本地未提交文件，其他成员保持独立。
5. 正常关闭管理器，并临时把原生依赖路径设为不存在，再从同一服务根重开 Room；成功说明回访没有调用 scn。三份 state 字节保持一致，detached、dirty 文件和原操作完成收据保留。

启用命令：

```sh
TEAMX_SCION_REAL=1 \
TEAMX_SCION_TEST_CREDENTIALS=/absolute/private/logged-in/credentials.json \
TEAMX_SCION_BIN=/absolute/path/teamx-scion \
TEAMX_SCN_BIN=/absolute/path/scn \
  go test -race -v ./internal/room -run '^TestScionRealWorkspace$' -count=1 -timeout 3m
```

仅在 Git ignore 的 `.local/scion-s3b-20261107-1/evidence/` 保留无秘密的摘要日志；实际凭据副本和测试工作副本由临时目录清理。不操作现有使用中的 Room、成员绑定或平台连接。

## 未验项与后续范围

- 实际浏览器控制在 `cua.getState()` 超时并重置，未取得页面状态；页面事件自动化不等同实际浏览器验收。
- 没有真实 DSH 模型驱动；角色模板、任务分支提交/发布、Review 创建/更新、评论和复审由 S4 完成。没有将本次成员工具执行称为模型验收。
- 本次受控失败发生在原生 clone 启动前；原生网络中断、各崩溃中间阶段、refresh token 轮换、多账号、多平台、大仓库和完整故障矩阵未验。
- 当前固定 v0.1.13、原生 PAT profile、完整检出；没有 device flow、第二种认证入口、sparse/daemon、Git gateway、通知或自动合入/关闭。
