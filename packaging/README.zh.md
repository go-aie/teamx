# 打包指南

本文供维护者从源码构建分发包，包含固定依赖、平台矩阵、本地与 CI 打包。下载、安装和运行见[安装与运行](../docs/installation.zh.md)；候选验收、签名公证和发布见[正式发行指南](releasing.zh.md)；组件原理见 [Server 架构](../docs/server.zh.md)。

| 目的 | 入口 | 产物位置 |
| --- | --- | --- |
| 用 GitHub Actions 构建开发快照 | [Package snapshots](../.github/workflows/package.yml) | 对应 Actions 运行的 Artifacts |
| 在本机打包 | [scripts/package.sh](../scripts/package.sh) | 默认 `dist/` |
| 构建版本候选并正式发行 | [正式发行操作指南](releasing.zh.md) | 候选 Artifacts，验收后发布到 GitHub Release |

## 包型与构建目标

| 包型 | 归档内容 | 构建与验收边界 |
| --- | --- | --- |
| `core` | TeamX、Node/DSH、公开团队模板 | 不包含 Git 或平台 CLI 运行时 |
| `standard`（默认） | core 的内容，加 glab、lark-cli 及配套资源 | 增加平台工具依赖与登录／只读／绑定验收，不包含 Git |
| `full` | standard 的内容，加完整 Git 运行时 | 增加 Git／TLS 与对应源码材料门槛；不在默认正式发行范围 |

各包均按统一 lock 组装，不从开发机复制运行时或私有状态。Scion 运行能力暂不随包提供。DSH 固定为 `0.2.2-rc.1` 预发布版本，精确依赖见 [dependencies.lock.json](dependencies.lock.json)。

构建矩阵包括以下目标；列入矩阵不代表已经通过完整发行验收。

| 目标参数 | 系统与架构 | 验证基线 |
| --- | --- | --- |
| `darwin-arm64` | Apple Silicon Mac | macOS 15 |
| `darwin-amd64` | Intel Mac | macOS 15 |
| `linux-amd64` | Linux x86-64 | Ubuntu 24.04／glibc 2.39 |
| `linux-arm64` | Linux ARM64 | Ubuntu 24.04／glibc 2.39 |

目标包的运行条件见[安装指南](../docs/installation.zh.md#下载与校验)及 manifest 的 `qualification_baseline`；构建通过不替代该目标的用户环境验收。

**当前仍为开发预览，完整矩阵和正式发行闭环尚未验收完成。** Linux full 存在 `git_tls_runtime` 阻塞：固定 Git 归档没有携带 HTTPS helper 所需的完整 TLS 动态库。脚本会拒绝组装该包；构建机恰好安装了这些库也不能消除缺项。standard 不携带 Git，不受这一组包阻塞影响。

## 用 GitHub Actions 构建开发快照

先将打包代码及 Workflow 文件提交到 GitHub 仓库。手动入口需要 Workflow 已进入默认分支，所选构建分支也应包含完整打包代码；只推送到其他 Git 托管服务不会触发 GitHub Actions。入口条件见 [GitHub 手动运行说明](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/manually-run-a-workflow)。

1. 打开仓库的 **Actions → Package snapshots → Run workflow**。
2. 选择构建分支和 `profile`，通常使用 `standard`；只需团队协作时选 `core`，依赖齐全时才选 `full`。
3. 等待各平台的构建和下载复验完成。一次运行覆盖全部四个目标，不提供单目标选择。
4. 在该次运行的 **Artifacts** 下载对应平台的 `snapshot-<target>-<profile>-<attempt>`。

也可使用已登录的 GitHub CLI；将 `OWNER/REPO` 和 `BRANCH` 换成实际仓库与分支：

```sh
gh workflow run package.yml --repo OWNER/REPO --ref BRANCH -f profile=standard
```

Artifact 内含 `.tar.gz`、同名 `.sha256`、构建检查报告和 `snapshot.json`。先解开 Artifact 的外层压缩包，再按[使用说明](../docs/installation.zh.md#下载与校验)用安装器消费原始 standard 归档。名称中的 attempt 区分同一次 Workflow 的不同执行轮次。

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

正式发行的包型由 lock 中的 `release.profiles` 决定，当前为 core 和 standard。流程先从干净的 `vX.Y.Z` tag 构建候选，再对同一批归档验收和发布；发布时不重新构建。配置测试模型、准备人工验收记录及处理失败的方法见[正式发行操作指南](releasing.zh.md)。默认允许未签名、未公证的 macOS 包，下载后的系统行为仍需验证。

升级依赖时显式更新统一 lock、对应 npm lock、许可证及相关版本契约，重新验证受影响目标。公开模板与随包文件由 [bundle-files.json](bundle-files.json) 明确列出；本机配置、`.env`、Room 数据和平台登录资料不属于分发内容。

## 随包文档

普通用户统一安装常驻服务。归档白名单包含 [install.sh](install.sh)，支持离线 `bash install.sh --archive FILE`；正式发行另外导出固定到该版本和仓库的 `install.sh`，用户可以通过 `curl …/releases/latest/download/install.sh | bash` 一次完成下载、校验、部署、启动和命令入口配置。安装器内部调用 `teamx service install`，不会要求用户再执行 install 或 init。macOS 的 `~/.local/bin/teamx`、Linux 的 `/usr/local/bin/teamx` 指向受管 `current/bin/teamx`，后续管理统一使用 `teamx service`。

`start`／`init` 保留为开发诊断入口，终端前台运行方法见[Room 开发说明](../internal/room/README.zh.md#归档调试运行)。它们不再作为普通安装的准备步骤；开发快照和旧归档不获得额外正式支持声明。

安装、运行和维护只维护 [docs/installation.zh.md](../docs/installation.zh.md) 这一份指南。[bundle-files.json](bundle-files.json) 将它原样复制为包内 `README.zh.md`；正式发布脚本也将同一来源复制为 Release 的 `README.zh.md`。指南使用文档内锚点导航，独立下载后可完整阅读，不依赖仓库中的其他文件。不再分别维护 bundle、Release 或 Server 使用说明模板。

需要追溯设计、历史归档摘要或当时的验收范围时，查阅[实施记录](../docs/implementation/packaging/README.zh.md)。这些记录不作为当前操作入口。
