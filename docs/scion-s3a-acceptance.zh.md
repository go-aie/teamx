# Scion S3a：Workspace 解耦

日期：2026-11-07。范围为 [接入方案](SCION-SOFTWARE-DEVELOPMENT-PLAN.zh.md) 的 S3a。

结论：**S3a 已实施，Git/GitLab 定向回归、race 检查、架构边界与页面自动化通过。** 本阶段保留既有配置、操作记录和 Session 生命周期；没有引入 Scion Workspace 初始化。

## 交付与边界

- [独立接口](../internal/workspace/repository.go)：`Source`、`Access`、`Repository` 与静态注册表。接口只接收仓库目标、已校验路径、公开运行引用和受管执行能力，不接收 Room/Session 配置、GitLab API 或数字项目 ID。`Check` 只读本地，`Clone` 显式初始化，`Configure` 区分首次应用与本地回访。当前缺省类型仍为 Git，未注册类型报错。
- [Git 实现](../internal/workspace/git/adapter.go)：迁入 `.git` 结构与符号链接检查、实际工作树、原始/有效/push origin 校验、clone、认证引用与作者配置；[语法校验](../internal/workspace/git/config.go)保留原 HTTPS URL/ref 限制。无 Room、平台 provider 或工具包依赖；支持无平台绑定的 Git 仓库。
- [Room 装配与 GitLab 桥接](../internal/room/workspace_adapters.go)：选择后端，准备现有冻结资源，核对连接代际与仓库身份，按缺失作者情况惰性查询并共享本次查询结果。[配置桥接](../internal/room/workspace_git_config.go)保留 GitLab 数字 ProjectID 等要求，[连接桥接](../internal/room/workspace_connections.go)负责填充核实后的目标与凭据租用。秘密只在实际受管进程执行时注入，公开 `ToolEnv` 和 Session 不增加秘密。
- [共用准备](../internal/room/workspace.go)：不执行 Git/scn、不判断 `.git/.scion`，先检查全部成员路径及本地目标，再准备平台访问并逐成员初始化。clone 前持久写 `.clone-pending`，clone 后经适配器复核才清除；失败保留现场，回访不重新 clone 或切分支。
- [管理操作](../internal/room/workspace_management.go)和[操作记录](../internal/room/workspace_records.go)沿用操作 ID、版本、等待结清、配置提交、Session 代际与显式重试合同；共用配置与操作校验不直接解释 GitLab ProjectID。既有数字身份校验没有删除或放宽。

这是准备边界的最小抽取，不新增快照版本或 `repository.kind`，不重写历史 Room，也不搬迁所有既有 Git/GitLab 辅助入口。各成员继续独立 clone；当前 Git 绑定共用原有非秘密平台资源引用，没有把未来不同成员的 Scion 作者或认证配置合并进共享环境。

## 验证

实际通过的主要命令：

```sh
go test -race ./internal/workspace/... ./internal/room \
  -run 'TestWorkspace|TestPreparation|TestOAuth.*(Workspace|ManagedGit|RepositoryScope)|TestRepository|TestCommonPreparation' \
  -count=1 -timeout 5m
go test ./internal/platform/gitlab -run '^Test(Adapter|Askpass)' -count=1
go test ./internal/platform -run '^TestCapabilityImportBoundaries$' -count=1
go vet ./internal/workspace/... ./internal/room
node --test --test-name-pattern='[Ww]orkspace' internal/room/web_app_test.mjs
git diff --check
```

| 层次 | 实际覆盖 |
| --- | --- |
| 仓库边界 | 生产依赖图验证接口/后端不反向依赖 Room 或平台，接口不依赖具体后端；共用准备、配置、操作/恢复文件不选择原生命令、元数据或数字项目身份 |
| 不透明后端 | 使用没有 `.git` 且禁止共用流程执行命令的替身，完成两个成员独立初始化和回访；冻结配置、成员文件与隔离保持不变 |
| 失败保护 | 第二个成员路径不安全时，第一成员与平台均未开始准备；clone 失败且目标不存在、clone 身份不匹配时均保留 marker，后续调用不自动重放 |
| 本地 Git | 真实本地 Git 仓库、多成员独立 clone、规范 URL/浏览器 URL、初始分支、dirty/任务分支保留、origin/push 校验、非空目录与符号链接保护、失败现场 |
| GitLab 与 OAuth | 假 HTTP 的仓库/作者核验、冻结资源、已有作者避免查询、认证配置漂移拒绝；生产凭据桥接验证有界网络访问与本地命令不刷新/不暴露令牌 |
| 操作与 Session | 显式应用/重试、同 ID 收据、等待旧 Session、跨文件提交中断、部分成员失败、代际轮换、正常管理器关闭重开、存储不确定时冻结 |
| 进程 | 输出有界、错误不泄露 stderr、超时/取消及父进程退出后子进程结清 |
| 页面 | 13 项 Workspace DOM 用例通过；`TestWorkspaceAppHTTPBinding` 另经生产 HTTP 管理入口完成显式绑定 |

## 未验项与后续范围

没有运行完整 Room suite，也没有在本阶段运行真实 GitLab/DSH、实际浏览器或远端写入验收。本地 Git、假平台与 DOM/HTTP 验证分别证明各自的准备和编排合同，不代表目标实例验证。

ScionAdapter、`repository.kind`、Scion 仓库与分支核验、三份原生 clone、页面类型/连接选择及其真实重启验证留给 S3b。角色与真实 Review 开发、修正/复审留给 S4；合入/关闭仍属后续范围。
