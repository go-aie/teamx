# 构建与发行指南

TeamX 使用按系统、架构分开的目录归档，包内包含程序、固定版本的 Node/DSH 和团队模板。使用归档无需安装 Go 或 Node；首次启动和升级方法见[发行包使用说明](release-README.zh.md)。

| 目的 | 入口 | 产物位置 |
| --- | --- | --- |
| 在 GitHub 获取开发快照 | [Package snapshots](../.github/workflows/package.yml) | 对应 Actions 运行的 Artifacts |
| 在本机打包 | [scripts/package.sh](../scripts/package.sh) | 默认 `dist/` |
| 构建版本候选并正式发行 | [正式发行操作指南](releasing.zh.md) | 候选 Artifacts，验收后发布到 GitHub Release |

## 包的选择与系统要求

| 包 | 包含内容 | 使用条件 |
| --- | --- | --- |
| `core` | TeamX、Node/DSH、公开团队模板 | 普通对话协作无需 Git；Git Workspace 和 GitLab／飞书能力所需工具另行安装 |
| `standard`（默认） | core 的内容，加 glab、lark-cli 及配套资源 | GitLab／飞书账号需登录和绑定；Git Workspace 需要自行安装 Git |
| `full` | standard 的内容，加完整 Git 运行时 | 无需另装 Git；当前不在默认正式发行范围内 |

所有包都需要使用者配置模型服务与凭据，软件开发任务自己的工具链由项目决定。Scion 当前暂不随包提供运行能力。DSH 固定为 `0.2.2-rc.1` 预发布版本，精确依赖见 [dependencies.lock.json](dependencies.lock.json)。

构建矩阵包括以下目标；列入矩阵不代表已经通过完整发行验收。

| 目标参数 | 系统与架构 | 验证基线 |
| --- | --- | --- |
| `darwin-arm64` | Apple Silicon Mac | macOS 15 |
| `darwin-amd64` | Intel Mac | macOS 15 |
| `linux-amd64` | Linux x86-64 | Ubuntu 24.04／glibc 2.39 |
| `linux-arm64` | Linux ARM64 | Ubuntu 24.04／glibc 2.39 |

运行环境需具备系统 shell、常用命令、证书、进程观察权限及可写的私有目录。macOS 的 DSH 命令工具还需要 `/usr/bin/sandbox-exec`；Linux 需要可用的 DSH 沙箱后端和声明的动态库。不支持 Windows 或 Alpine/musl。

**当前仍为开发预览，完整矩阵和正式发行闭环尚未验收完成。** Linux full 存在 `git_tls_runtime` 阻塞：固定 Git 归档没有携带 HTTPS helper 所需的完整 TLS 动态库。脚本会拒绝组装该包；构建机恰好安装了这些库也不能消除缺项。standard 不携带 Git，不受这一组包阻塞影响。

## 在 GitHub 获取开发快照

先将打包代码及 Workflow 文件提交到 GitHub 仓库。手动入口需要 Workflow 已进入默认分支，所选构建分支也应包含完整打包代码；只推送到其他 Git 托管服务不会触发 GitHub Actions。入口条件见 [GitHub 手动运行说明](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/manually-run-a-workflow)。

1. 打开仓库的 **Actions → Package snapshots → Run workflow**。
2. 选择构建分支和 `profile`，通常使用 `standard`；只需团队协作时选 `core`，依赖齐全时才选 `full`。
3. 等待各平台的构建和下载复验完成。一次运行覆盖全部四个目标，不提供单目标选择。
4. 在该次运行的 **Artifacts** 下载对应平台的 `snapshot-<target>-<profile>-<attempt>`。

也可使用已登录的 GitHub CLI；将 `OWNER/REPO` 和 `BRANCH` 换成实际仓库与分支：

```sh
gh workflow run package.yml --repo OWNER/REPO --ref BRANCH -f profile=standard
```

Artifact 内含 `.tar.gz`、同名 `.sha256`、构建检查报告和 `snapshot.json`。先解开 Artifact 的外层压缩包，再按[使用说明](release-README.zh.md)校验、解压其中的 `.tar.gz`。名称中的 attempt 区分同一次 Workflow 的不同执行轮次。

Pull request 也会自动构建快照，默认使用 standard。快照流程不需要模型或平台账号，不执行真实模型回合，也不发布 GitHub Release。它会在匹配架构的 runner 上组装并检查最终归档，再下载同一归档重新检查；结果用于开发预览。快照 Artifact 保留 7 天。

## 在本机打包

在目标系统与架构上准备 Go **1.27.1**、Node、Git、curl 和 tar，以下命令在仓库根目录执行。Node 用于运行编排脚本；包内 Node 与 npm 由 lock 固定并校验。tar 需要支持目标依赖的压缩格式，Linux 解包 Node 还需 xz。

```sh
# 为当前机器生成默认的 standard 开发包。
./scripts/package.sh --channel dev --profile standard

# 缓存齐全时禁止联网下载。
./scripts/package.sh --channel dev --profile standard --offline
```

`--target` 选择目标，默认是当前 Node 进程的系统与架构。**当前入口要求匹配的目标环境，不支持直接跨平台打完整包。** 例如以下命令须在 Linux amd64 环境执行；在 Mac 上设置该参数仍会被拒绝：

```sh
./scripts/package.sh --channel dev --profile standard --target linux-amd64
```

| 参数 | 含义 |
| --- | --- |
| `--profile core\|standard\|full` | 包的能力；默认 standard |
| `--channel dev\|release` | 开发快照或版本构建；开发快照会记录 commit 和 dirty 状态 |
| `--cache-dir DIR` | 依赖缓存目录，默认 `.packaging/cache/` |
| `--output-dir DIR` | 产物目录，默认 `dist/` |
| `--offline` | 缺少任何固定依赖、目标 npm 缓存或 Go 模块缓存即失败 |

成功后生成 `teamx-dev-<commit>[-dirty]-<target>-<profile>.tar.gz`、同名 `.sha256` 和 `-report.json`。归档通过检查后才进入成功产物目录；参数、依赖或检查失败均以非零状态退出。

可以对归档单独重跑检查；将路径替换为实际文件：

```sh
./scripts/package-smoke.sh /absolute/teamx-archive.tar.gz --report /absolute/new-report.json
```

归档检查覆盖内容、权限、依赖、初始化、服务启停与固定 ACP fixture，不会调用真实模型或登录平台。正式发行还需要真实 DSH、浏览器及用户环境验收。

## 正式发行与维护

正式发行的包型由 lock 中的 `release.profiles` 决定，当前为 core 和 standard，推荐下载 standard。流程先从干净的 `vX.Y.Z` tag 构建候选，再对同一批归档验收和发布；发布时不重新构建。配置测试模型、准备人工验收记录及处理失败的方法见[正式发行操作指南](releasing.zh.md)。默认允许未签名、未公证的 macOS 包，下载后的系统行为仍需验证。

升级依赖时显式更新统一 lock、对应 npm lock、许可证及相关版本契约，重新验证受影响目标。公开模板与随包文件由 [bundle-files.json](bundle-files.json) 明确列出；本机配置、`.env`、Room 数据和平台登录资料不属于分发内容。

需要追溯设计、历史归档摘要或当时的验收范围时，查阅[实施记录](../docs/implementation/packaging/README.zh.md)。这些记录不作为当前操作入口。
