# TeamX 本地打包与 GitHub 发行方案

本文保留设计、阶段定义及实施进度，供维护时追溯。当前操作方法见[打包指南](../packaging/README.zh.md)和[正式发行操作指南](../packaging/releasing.zh.md)。

状态：P0 已实施依赖与平台核查，见 [P0 交付与核查记录](implementation/packaging/P0.zh.md)。P1 公共启动/初始化/诊断、core 开发包组装与归档 smoke 已实施，见 [P1 记录](implementation/packaging/P1.zh.md)。P2 已实施目标产物组装、full 启动器、Git 源码材料及扩展 smoke，但完整矩阵出口尚未达到，具体证据和阻塞见 [P2 记录](implementation/packaging/P2.zh.md)。P3 已按用户调整实施 macOS arm64 本地验收入口，执行结果见 [P3 记录](implementation/packaging/P3.zh.md)，Linux arm64 结果见 [P3 Linux 记录](implementation/packaging/P3-linux.zh.md)。P4.1 快照 Workflow 与下载复验已实施，GitHub 四平台真实执行仍待完成，见 [P4.1 记录](implementation/packaging/P4.1.zh.md)。P4.2 候选 Workflow 及 P4.3 完整矩阵汇总、GitHub 验收与发行流程已实施；真实矩阵和远端正式发行仍待完成，见 [P4.2](implementation/packaging/P4.2.zh.md) 与 [P4.3](implementation/packaging/P4.3.zh.md)。

2026-11-08 实施范围调整：按用户要求暂缓 Scion。当前 core/full 均不承诺 Scion 功能，`scn` 不作为依赖收敛或 full 交付的门槛；保留已有 `teamx-scion` 封装构建入口。下文原方案中涉及 Scion 的收集、许可及真实验收要求留待恢复该能力时执行。P0 已将四个平台全部纳入本期交付矩阵；纳入不表示已通过运行验收。

2026-11-08 最新范围调整：按用户确认增加 `standard = core + glab + lark-cli`，作为默认构建和推荐下载；`full = standard + 完整 Git` 保留开发／候选入口。本期正式发行固定为四平台 × core/standard，写入统一 lock 的 `release.profiles`，不再要求 full 同时完成。Linux full 的 Git/TLS 缺项继续阻止 full 构建，但不阻塞上述正式矩阵。下文早期阶段将 full 列为一期必交付的要求，以本次明确调整为准；各正式包自身的真实验收缺项仍阻止发行。最新操作见[构建指南](../packaging/README.zh.md)和[正式发行指南](../packaging/releasing.zh.md)。

核对日期：2026-11-08。

## 1. 建议结论

采用**按操作系统与架构构建的目录发行包**：将 TeamX 程序、独立工具、完整 DSH 运行时、固定版本的平台 CLI、团队模板和启动入口放进同一个 `tar.gz`。本地和 GitHub Actions 调用同一套打包脚本，开发场景生成快照包，用户场景发布经过验收的版本包。

默认提供 `standard` 包，用户解压后无需 Go、Node、npm/pnpm，也无需克隆 TeamX 或 DSH 源码，即可使用普通协作及 GitLab／飞书平台能力。Git Workspace 和依赖本地 Git 的 glab 命令仍需用户自行安装 Git。另提供基础 `core`；包含完整 Git 的 `full` 暂留开发／候选，能力和发行范围分别声明。

“立即使用”定义为：在受支持的系统上，解压、配置模型地址/名称及 API Key、运行一个启动命令，即可打开 Web 并开始协作。GitLab、飞书、Scion 仍需用户登录和绑定连接；不随包预置账号、凭据或通知订阅。软件开发任务自己的 Go/Python/Java 等工具链仍由项目决定，不承诺一个包包含任意开发环境。

采用**一期、五个阶段**实施：依赖与平台核查 → 公共打包能力 → 多平台产物组装 → 逐平台验收 → CI 与正式发行。**macOS arm64、Linux amd64 为本期必交付目标**；Linux arm64、macOS amd64 在依赖核查阶段评估，若只需补充目标依赖条目和矩阵配置，即纳入本期交付。各平台共用打包入口，不分别建设完整流程；只有依赖齐全且真实验收通过后才承诺 `full` 包。具体步骤和完成条件见第 10 节。

## 2. 当前源码中的实际组成

### 2.1 自有构建产物

当前仓库只有以下三个 `package main` 入口，没有需要另外构建的独立 HTTP server：

| 产物 | 构建入口 | 用途 |
| --- | --- | --- |
| `teamx` | `./cmd/teamx` | Web 服务、Room 管理、Agent CLI、平台内部能力、Workspace Git |
| `teamx-gitlab` | `./tools/gitlab` | Agent 主动使用的 GitLab CLI，代理 glab 并提供 Go 扩展 |
| `teamx-scion` | `./tools/scion` | 固定版本 scn 的独立封装 |

Web HTML/CSS/JS 已由 [web.go](../internal/room/web.go) 嵌入；平台 skill、GitLab askpass/使用说明等也已有嵌入入口。不需要额外前端服务器、Node 前端构建进程或外部 skill 目录才能运行 TeamX 服务。

根模块 [go.mod](../go.mod) 当前要求 Go 1.27，无相邻源码目录 `replace`。打包机需要 Go；发行包的使用者不需要 Go。

### 2.2 运行依赖

| 依赖 | 当前依据 | 打包要求 |
| --- | --- | --- |
| DSH | [官方 Release：dsh-v0.2.2-rc.1](https://github.com/deepseek-ai/deepseek-harness/releases/tag/dsh-v0.2.2-rc.1) 与[运行时手册](agent-runtime.zh.md) | 锁定 `0.2.2-rc.1`，通过兼容性验收后用于发行；完整 CLI、依赖、profile 模板及资源，不能只复制 JS 入口 |
| Node | DSH 需要 Node | 随包提供经验证的固定版本，不能依赖用户全局 Node |
| glab | [glab.lock.json](../tools/gitlab/glab.lock.json)：1.127.0 | 同目录随包提供；补齐各目标平台的下载摘要 |
| scn | [scn.lock.json](../tools/scion/scn.lock.json)：0.1.13 | 当前仅记录 darwin_arm64；该实例下载需要登录，其他平台待核实 |
| lark-cli | [client.go](../internal/platform/lark/client.go)：1.0.99 | 固定版本及完整安装内容，先查清其实际分发形式和系统依赖 |
| Git | Workspace 的 Git 后端 | `full` 应带经验证的可搬移 Git 运行时，包括 helper、exec-path 和 TLS 依赖；只复制一个 git 文件不够 |
| 系统基础能力 | DSH 进程治理及 shell 工具 | macOS/Linux，`/bin/ps`、shell、证书与正常进程观察权限；macOS DSH 命令工具还需系统 `/usr/bin/sandbox-exec`；Linux 明确最低发行版/glibc 及沙箱后端基线 |

上述平台 CLI 版本来自当前仓库契约，不代表上游最新版本。DSH 按第 5 节锁定为已选定的 `0.2.2-rc.1`，实施时须完成兼容性验收。打包和启动均不能自动取 latest，否则可能违反封装的精确版本检查或破坏构建复现。

DSH 上游也明确需要 Node，且仍在快速变化；随包 Node 必须满足所选 DSH 版本的运行要求，并与 DSH 一起锁定和验收。见 [DSH 官方说明](https://github.com/deepseek-ai/deepseek-harness#run)。

### 2.3 `.teamx` 的边界

必须分开处理：

- `.teamx/teams/`：受版本控制的团队定义、角色提示词及团队资源，属于发行资产。
- `.teamx/config.example.toml`：公开配置样例，属于发行资产。
- `.teamx/config.toml`：当前机器的配置，可能包含密钥，不打包。
- `.teamx/data/`：Room、历史、平台连接、Scion 凭据、Workspace 等私有数据，不打包。

团队资源会在创建 Room 时复制并冻结。更新发行模板只影响新 Room，不覆盖旧 Room 的指令和资源。

## 3. 包的种类与目录

### 3.1 能力包与发布渠道分开

| 维度 | 选项 | 行为 |
| --- | --- | --- |
| 能力 | `core` | 三个自有程序、DSH + Node、全部公开团队模板；平台原生 CLI/Git 可由操作者自行安装 |
| 能力 | `standard` | core + glab、lark-cli 及配套资源；默认推荐，使用 Git Workspace 仍需宿主 Git |
| 能力 | `full` | standard + 可搬移 Git 及其必需资源；当前仅开发／候选 |
| 渠道 | `dev` | 本地或手动 CI 快照，保留调试信息，标明 commit 和 dirty 状态 |
| 渠道 | `release` | 干净且已标记的源码、固定依赖、完整验证和校验和，供用户下载 |

core 包有工具封装但不代表原生依赖已齐全，检查报告必须显示缺项。各包缺少所声明的依赖均打包失败，不能静默降级。正式矩阵按 lock 中的 `release.profiles` 生成，当前 core/standard 全矩阵必须通过；full 的延期和 Git 缺项单独记录，不称作已验收的 full。

示例文件名：

```text
teamx-vX.Y.Z-darwin-arm64-full.tar.gz
teamx-vX.Y.Z-linux-amd64-full.tar.gz
teamx-vX.Y.Z-linux-amd64-core.tar.gz
teamx-dev-<commit>-dirty-darwin-arm64-core.tar.gz
SHA256SUMS
```

### 3.2 解压后的目录

```text
teamx-vX.Y.Z-darwin-arm64-full/
├── start                         # 用户唯一启动入口，POSIX shell
├── bin/
│   ├── teamx
│   ├── teamx-gitlab
│   ├── teamx-scion
│   ├── glab                      # standard/full
│   ├── lark-cli                  # standard/full；可为调用 runtime 的启动器
│   ├── git                       # full；可为调用 runtime 的启动器
│   └── dsh                       # 调用包内固定 Node + 完整 DSH
├── runtime/
│   ├── node/                     # 对应 OS/arch 的完整运行文件
│   ├── dsh/                      # 完整部署，依赖链接仅指向包内
│   ├── lark/                     # 如实际发行需要附带资源
│   └── git/                      # 完整 Git 布局及必需动态库等
├── share/teamx/
│   ├── config.example.toml
│   └── teams/                    # 从受控模板清单复制
├── .teamx/                       # 首次启动生成，不包含在压缩包中
│   ├── config.toml
│   ├── teams/                    # 首次复制，可由用户编辑
│   └── data/                     # 服务生成
├── manifest.json
├── checksums.txt                 # 包内不可变文件摘要
├── LICENSE
├── THIRD_PARTY_NOTICES/
└── README.zh.md                  # 发行包使用说明
```

把发行原件放在 `share/teamx`，运行时才生成 `.teamx`，避免“打包开发者的隐藏目录”这一错误来源。原始模板和用户修改也能明确区分。

不把 Docker 作为主要交付形式：它可作为后续服务部署补充，但会额外引入容器、挂载和宿主工具链问题，不能替代本次目录解压包。

## 4. 启动、初始化与路径规则

### 4.1 默认便携模式

以下是实施后的预期使用方式：

```sh
tar -xzf teamx-vX.Y.Z-darwin-arm64-full.tar.gz
cd teamx-vX.Y.Z-darwin-arm64-full
./start init
# 编辑 .teamx/config.toml 中的模型协议、地址、模型名
export OPENAI_API_KEY='用户自己的密钥'
./start
```

`start` 根据自身文件位置计算绝对 bundle root，支持空格路径和从任意 cwd 调用；前置包内 `bin` 与 Node 目录到服务 PATH，最后 `exec` TeamX，保留 Ctrl-C/SIGTERM 的退出语义。不得通过字符串拼接 `eval` 启动服务。

计划新增 `teamx init` 和 `teamx doctor`，由 Go 负责配置序列化、模板复制和检查；shell 仅负责路径及进程启动，避免 shell 手工转义 TOML：

```text
start init   → bin/teamx init --bundle-root ABS --home ABS
start doctor → bin/teamx doctor --config ABS/config.toml --bundle-root ABS
start        → 初始化缺失配置并检查 → bin/teamx serve --config ABS/config.toml
```

自动初始化发现模型尚未配置时，输出配置文件位置和准确缺项后退出；再次执行即可启动。不生成假模型、不发送探测 prompt，不自动登录或开启通知。

初始化只创建缺失内容，已有配置、模板、凭据、数据一律保留。并发初始化应使用锁及原子写入；自定义监听地址、数据目录、模型参数不能被再次启动覆盖。`init` 不是旧数据迁移入口。

默认生成配置的关键内容：

```toml
[server]
listen = "127.0.0.1:8088"
data_dir = "./data"
teams_dir = "./teams"
max_turns = 16
dsh_turn_timeout = "15m"

[runtime.dsh]
binary_path = "../bin/dsh"

[runtime.model]
protocol = "openai-completions"
base_url = "https://model.example/v1"
model_id = "your-model"
api_key_env = "OPENAI_API_KEY"
```

当前 [config.go](../internal/room/config.go) 已按配置文件所在目录解析 DSH、teams、data 路径。相对路径不展开 `~` 或环境变量。`TEAMX_SCION_BIN` / `TEAMX_SCN_BIN` 当前也没有 GitLab 路径变量的配置归一化逻辑，不能假设所有 `[env]` 路径都能写相对值；优先使用已有同目录发现，必要时由启动入口提供绝对路径。

GitLab 的三件套以及 Scion 的两件套放在 `bin/`，复用现有同目录发现。飞书/Scion 连接保存的是绝对 CLI 路径；设置页面由操作者选用包内路径。可后续增加包内路径预填，但不能虚构账号/profile 或自动建立连接。

### 4.2 用户目录模式

预期支持：

```sh
/opt/teamx-vX.Y.Z/start init --home "$HOME/.teamx"
/opt/teamx-vX.Y.Z/start --home "$HOME/.teamx"
```

配置、模板和数据进入显式 home；DSH 指向本次 bundle 的绝对入口，模板仍复制自 `share/teamx/teams`。包本体可保持只读，home 必须可写。不改写系统 PATH、不要求 sudo；同一个 home 同时只允许一个服务实例持有。

开发者继续可以使用仓库 `.teamx/config.toml` 和 `.teamx/teams`，不必先打包才能开发。需要复现用户问题时使用新解压包及独立 home，避免读取工作区里的配置和凭据。

## 5. 外部运行时如何收集

已建立 [`packaging/dependencies.lock.json`](../packaging/dependencies.lock.json) 统一记录：每项依赖版本、来源、目标平台、下载文件、SHA-256、解包布局、许可证与验证方式。DSH 的精确 npm 解析结果保存在 `packaging/dsh/package-lock.json`；glab 的归档摘要复用现有 lock，Scion 标记暂缓。`node scripts/check-packaging.mjs` 离线校验各 lock、飞书版本常量及资产清单的一致性。

DSH 还需记录精确版本、官方 release/tag、完整源码 commit、是否预发布、选型核对日期、npm tarball integrity（采用 npm 产物时）、Node/pnpm 版本、锁文件摘要、ACP 身份及已验收能力。不能仅记录 `dsh --version` 就认为依赖完全相同。

### DSH + Node

**版本策略：本期所有纳入交付的平台均以官方 `dsh-v0.2.2-rc.1` 为 DSH 基线。** 已明确接受该 RC 版本；通过第 9 节兼容性与真实运行验收后，可用于 TeamX 的开发快照及正式发行包，不以等待上游稳定版为发行门槛。lock、manifest 和发行说明仍须准确标明 DSH 为预发布版本，不能将其写成稳定版。

2026-11-08 核对[官方 Release API](https://api.github.com/repos/deepseek-ai/deepseek-harness/releases?per_page=100)：返回的 37 个 Release 均为预发布；本次选定的 [`dsh-v0.2.2-rc.1`](https://github.com/deepseek-ai/deepseek-harness/releases/tag/dsh-v0.2.2-rc.1) 于 2026-11-04 发布，官方标为 Pre-release。实施时核对该标签、完整 commit 和分发产物，不自动切换到届时最新版本。

选型与升级流程：

1. P0 依赖收敛时将 `dsh-v0.2.2-rc.1` 的 release/tag、完整 commit、来源及摘要写入 lock；所有交付平台复用同一版本，并按 OS/arch 独立部署和验收。后续显式升级优先评估最新正式稳定版；升级需更新 lock，并对受影响的已支持平台重新验收。
2. 优先使用与该标签对应的精确官方 npm 发布包，在隔离 staging 中安装并固定完整解析结果；若没有适用 npm 产物，可从同一标签对应的完整 commit 构建，按 [现有部署流程](agent-runtime.zh.md#准备-dsh-与配置) 产出独立安装目录。两种来源必须在 manifest 中可辨认。
3. 使用所选版本完成第 9 节要求的真实 DSH/ACP 兼容性验收，再作为发行依赖。若所需能力不兼容，先修复适配或记录阻塞；不静默回退旧版、切换任意开发 commit 或使用本机安装替代。
4. 打包脚本只消费已锁定产物，不在每次构建或启动时追踪上游 latest。若需要使用其他预发布版本或未发布补丁，应另行明确调整基线，并记录来源、差异和重新验收结果。

所选 `0.2.2-rc.1` 的官方说明要求 Node.js 至少 24.9；随包 Node 必须满足该下限，并在依赖收敛时锁定经验证的精确版本。

DSH 安装必须在对应目标平台完成，保留生产依赖、profile 和运行资源。禁止 `pnpm link`、指向开发机 store 的绝对符号链接和源码目录逃逸。不能把 macOS 的 node_modules 复制到 Linux 当作通用运行时；原生模块和可选依赖必须按目标平台处理。

`bin/dsh` 使用相对自身位置定位 Node 与 CLI JS，向下原样传递 argv；不依赖全局 `node`、`npx`，运行期间不在线安装。启动入口同时提供必要 PATH，确保 DSH 子工具也能找到包内命令。完整运行时摘要放进 manifest，包含 Node、JS 和依赖，而非只计算这个很薄的启动器。

### 平台 CLI 与 Git

glab/scn 用锁定的官方目标平台发布资产，下载后先核对预先审核的摘要，再解包；版本探测也必须通过。现有 glab lock 的 observed binary 仅覆盖一份本机证据，不能当作所有平台压缩包的校验和。

Scion 私有下载凭据仅用于 CI 拉取依赖，不能进入归档、manifest 或日志。正式公开随包分发前核实其再分发许可及资产访问条件；不能把“能够登录下载”理解成“已经允许公开再分发”。若暂时不能公开分发，则该 full 变体只能用于获准内部开发，公开渠道先发布准确标注能力的 core 包。

飞书先确认 1.0.99 的官方安装形式和许可证，补全包摘要及资源布局；用户在目标机器正常登录，绝不复制打包机 profile/Keychain。随包版本不在启动时自动更新。

Git 从可再分发且已验收的可搬移安装产物收集，包含 git-core helpers、HTTPS/TLS、模板等；通过启动器设置必需的 exec-path，不能写死 staging 路径。`full` 在无宿主 Git 的环境验收 clone、本地 commit、受控网络 Git。Alpine/musl 不沿用 glibc 发行承诺；macOS 如需要系统组件，在能力报告中明确。

## 6. 本地如何打包

### 6.1 计划新增的入口

```text
packaging/
  dependencies.lock.json       # 每个平台的固定依赖清单
  bundle-files.json            # 自有入口、模板、文档的明确允许清单
  start                        # 启动脚本源文件
scripts/
  package.sh                   # 编排依赖、构建、组装、验证、归档
  package-smoke.sh             # 从归档解压后验收
```

`package.sh` 是唯一编排入口，CI 不复制一份组装逻辑。参数均为拟议接口：

实施时将 `/dist/` 与构建 staging 加入 `.gitignore`，避免上一次构建产物影响正式包的干净工作树判断；不能为了通过检查而忽略真实源码修改。

```sh
# 当前机器开发快照，默认不读取本机 bin/ 中的未知版本
./scripts/package.sh --channel dev --profile standard --target darwin-arm64

# 同一期：在 Linux amd64 构建环境中使用同一入口
./scripts/package.sh --channel dev --profile standard --target linux-amd64

# core；显式使用已经按 lock 验证的下载缓存，禁止联网下载
./scripts/package.sh --channel dev --profile core --offline

# 正式包：要求干净工作树、tag 与 HEAD 一致、依赖及验收齐全
./scripts/package.sh --channel release --candidate --profile standard --version vX.Y.Z
```

流程：

1. 校验参数、源码状态、目标支持、Go 版本、依赖 lock 与许可证材料。
2. 创建新的 staging；仅从允许清单复制模板和资源。开发快照可纳入清单内当前修改，但记录 dirty、源码差异摘要；正式包从干净 tag 构建。无关 untracked 文件不参与。
3. 拉取或复用经 SHA-256 验证的依赖缓存，再按目标平台部署 DSH。离线模式缺缓存立即失败，不切换版本。
4. 构建三个自有二进制；release 使用 `-trimpath`，`CGO_ENABLED=0` 仅用于已验证支持的自有 Go 程序，不能据此推断 Node/Git 静态链接。dev 保留调试信息。
5. 写入启动器、模板原件、精简使用说明、许可证和 manifest；不要扫描整个 `.teamx` 或直接复制 `bin/`。
6. 检查符号链接均留在包内、权限与目标架构正确；生成包内不可变文件摘要，manifest 不含 staging/开发机绝对路径或秘密。
7. 先归档，再解压到新目录跑 smoke；验收对象必须是最后准备发布的归档。
8. 输出 `dist/<文件名>.tar.gz`、摘要及验证报告。失败不产出标记为成功的包。

`manifest.json` 至少记录包版本、渠道、profile、OS/arch、源码 commit/dirty、Go 构建版本、全部运行依赖版本/来源/摘要、模板清单和支持基线。`checksums.txt` 覆盖 manifest 及不可变资产，排除它自身和启动后生成的 `.teamx`；归档的 SHA-256 单独放在包外，避免摘要循环。

当前三个构建命令可直接复用：

```sh
go build -o STAGING/bin/teamx ./cmd/teamx
go build -o STAGING/bin/teamx-gitlab ./tools/gitlab
go build -o STAGING/bin/teamx-scion ./tools/scion
```

这里的 `STAGING` 是示意目录，真实脚本须创建并安全引用绝对路径。本期采用 shell 编排加少量 Go 初始化/检查逻辑即可，不必先引入 GoReleaser。后续若采用 GoReleaser，也继续复用依赖组装和归档验收。

## 7. GitHub Workflow 如何打包

### 7.1 两类工作流

| 工作流 | 触发 | 输出 |
| --- | --- | --- |
| `package.yml` | `workflow_dispatch`；PR 运行不依赖秘密的 core 检查 | 开发快照、checksums、smoke 报告作为 Actions artifact |
| `release.yml` | 推送 `v*` tag | 全部受支持目标验收通过后上传 GitHub Release assets |

私有依赖的 full 构建只对可信分支/tag 或手动任务开放，不向 fork PR 暴露依赖下载凭据。不使用 `pull_request_target` 执行外部分支的打包脚本。

构建与快照矩阵来自 P0 确定的本期交付目标，用于组装和验证各目标；正式发行矩阵只包含其中**已完成全链验证**的目标，且须覆盖本期全部必交付目标。不能写死四个平台就默认支持。Go 部分可交叉编译，但 DSH/Node 原生依赖、Git 和最终 smoke 优先使用对应 OS/arch 原生 runner。没有 runner 的目标不能靠“编译成功”升级为正式支持。

`darwin-arm64`、`linux-amd64` 始终为本期必交付目标。P0 若确认 `linux-arm64`、`darwin-amd64` 的依赖和 runner 齐全，且无需额外平台专项开发，则将其一并纳入交付矩阵；否则记录具体缺项并延后，不预先安排为独立期次。纳入后的目标须通过相同验收，不得为通过发布而静默移除失败项或降级为 core。

### 7.2 Job 依赖

```text
checks（Go 测试/vet、Web Node 测试）
  └─ package matrix（固定依赖 → 同一本地脚本 → 解包 smoke → 上传归档）
       └─ release（汇总归档和 SHA256SUMS → 发布资产）
```

常规源码检查沿用 README 中的入口；组件测试可用 CI Node，发布包 smoke 必须用随包 Node，避免掩盖缺件。

下载缓存按 OS/arch、依赖 lock 摘要、Node 版本等隔离；命中缓存仍核对摘要。DSH 生产部署产物不能跨平台共用。构建不写业务平台，不使用开发者的真实 Room 或平台会话。

### 7.3 Workflow 骨架

下面展示包含两个必交付目标的发布编排契约，不能原样执行：Action 引用需实施时换成审核后的完整 commit SHA，`checks` 的测试步骤需补齐；P0 纳入的其他目标也须补入矩阵。runner 标签是拟议选择，实施时按 [GitHub 官方 runner 列表](https://github.com/actions/runner-images#available-images) 核对架构、可用性和系统基线，不使用已退出服务的镜像。

```yaml
name: release
on:
  push:
    tags: ['v*']
permissions:
  contents: read

jobs:
  checks:
    runs-on: ubuntu-latest
    steps:
      # checkout、固定 Go/Node、README 中的源码检查
      - run: echo '此处须替换为真实检查，不能作为发布门禁'

  package:
    needs: checks
    strategy:
      fail-fast: false
      matrix:
        include:
          - runner: macos-15
            target: darwin-arm64
          - runner: ubuntu-22.04
            target: linux-amd64
    runs-on: ${{ matrix.runner }}
    steps:
      - uses: actions/checkout@<PINNED_SHA>
      - uses: actions/setup-go@<PINNED_SHA>
        with:
          go-version-file: go.mod
      # 固定构建用 Node/pnpm；可信上下文中拉取需登录的依赖
      - name: Package and verify final archive
        env:
          PACKAGE_VERSION: ${{ github.ref_name }}
          PACKAGE_TARGET: ${{ matrix.target }}
        run: >-
          ./scripts/package.sh --channel release --profile full
          --target "$PACKAGE_TARGET" --version "$PACKAGE_VERSION"
      - uses: actions/upload-artifact@<PINNED_SHA>
        with:
          name: bundle-${{ matrix.target }}
          path: |
            dist/*.tar.gz
            dist/*.sha256
            dist/*-report.json
          if-no-files-found: error

  release:
    needs: package
    runs-on: ubuntu-latest
    permissions:
      contents: write
    steps:
      # checkout / download-artifact / 核对完整矩阵、SHA、版本与摘要
      # 合并 SHA256SUMS；用 gh release 上传已验收的同一批资产
      # 禁止在此重新构建，禁止失败后发布残缺矩阵
      - run: echo '此处须补齐资产汇总与发布'
```

实际 Workflow 要核对 runner 架构与 matrix 一致，不在 macOS ARM runner 上生成 amd64 Node 部署再直接执行。GitHub Release 面向用户，Actions artifact 面向开发快照与跨 job 传递，二者职责见 [GitHub artifact 文档](https://docs.github.com/en/actions/tutorials/store-and-share-data)。

**上传预先生成的 tar.gz，不能直接上传 staging 目录**：artifact 默认忽略隐藏文件，直接压缩上传也可能丢失可执行权限；tar 可保留包内目录、隐藏资源和模式。参见 [upload-artifact 官方说明](https://github.com/actions/upload-artifact#limitations)。

构建 job 只读，发布 job 才授予 `contents: write`；凭据通过受限环境传入。依据 [GitHub Token 权限说明](https://docs.github.com/en/actions/tutorials/authenticate-with-github_token)。正式 release 建议先生成 draft，待相应真实验收证据齐全再公开；该机制实施后由发布流程执行，本文不创建远端 Release。

## 8. 升级与搬目录的限制

全新解压包在首次启动前可以搬到任意受支持位置。**已有数据的包搬家和恢复旧 DSH Session 是另一项能力，不能包含在“解压即用”的默认承诺中。**

当前 Room 冻结 DSH 绝对路径、参数等；DSH 又核对启动程序摘要、版本、ACP 身份和正常关闭证据。连接 profile 也保存绝对 CLI 路径。因此相对配置解决的是启动路径解析，不会自动改写已有 Room/profile。

本期升级规则：

1. 停止旧服务并等待结清，备份私有 home；新包解压到独立目录，保留旧包及运行时路径。
2. 用新包和隔离 home 验证新版本，再显式指定原 home。不得自动覆盖旧模板或运行 `connections upgrade` 等迁移。
3. 已有 Room 保持其冻结运行时，旧路径必须仍可用；更新默认 runtime 只给新 Room 使用。新旧平台 CLI 版本及连接引用有变化时另行验证，不能静默替换。
4. 需要切换旧 Room 的运行时时，走现有受控 Session 结清/轮换或新建 Room；不编辑 backend 指纹、不伪造正常关闭证据。

如果后续要求“删除旧包仍能恢复所有历史房间”，再增加独立的版本化运行时安装目录与显式迁移设计。本期不额外引入运行时包管理器。二进制回滚也不等于数据格式可回滚；格式变化需要单独的兼容/备份恢复说明。

## 9. 验收标准

### 9.1 每次打包必须通过

- 内容与允许清单一致，不包含 `.env`、本机 config、data、平台 profile、缓存、源码工作区或旧二进制；包含全部公开团队及指令资源。
- 包内依赖、版本、架构、摘要、许可证材料齐全；符号链接不逃逸；从带空格的路径和其他 cwd 能调用启动入口。
- 在清理过 PATH、没有宿主 Go/Node/平台 CLI 的新环境中运行；full 还须排除宿主 Git。系统 shell、`/bin/ps` 和基础证书保留并声明基线。
- 所有随包 CLI 的版本及无凭据帮助可用，DSH 使用包内 Node；不发生下载、更新、npm 安装或业务平台请求。
- 所有团队可加载，启动后首页和管理 API 可访问，能创建测试 Room；用固定 ACP fixture 验证接线，报告标明 fixture。
- Ctrl-C/SIGTERM 后端口、子进程、文件锁按既有合同结清；同一配置重启读取数据；再次 init 不覆盖用户配置和模板。
- 对最终 tar.gz 解压验收，而非只验证 staging；归档摘要与上传资产一致。

### 9.2 每个正式支持平台的真实验收

使用包内真实 DSH 及单独的测试模型配置，完成建会话、最小回合、工具调用、正常关闭和原 Session 恢复。standard/full 再验收隔离平台账号登录、只读命令及绑定；包含 Git 的 full 另验收 Git helper/TLS/clone/commit。远端写入只在单独获准的测试对象上验收，不属于日常打包 smoke。

macOS 在干净机器验证下载后的签名/quarantine 行为；Linux 记录最低系统及动态库基线。需要签名/公证时，将其放在生成最终归档及摘要之前。不能把开发机上可运行当作用户下载后必然可运行。

结果分别报告：源码检查、离线归档 smoke、真实 DSH、真实平台、实际浏览器。真实测试未执行或受环境限制时保留缺项，fixture 不替代真实验收。

## 10. 一期实施阶段与步骤

本期只建设一套公共打包与发行流程，按 P0 → P1 → P2 → P3 → P4 推进。macOS arm64 和 Linux amd64 均在本期完成，不要求先完成一个平台的正式发行才开始另一个平台。每阶段完成后记录交付和证据，后续阶段复用已有成果。

### 10.1 本期平台范围与纳入条件

| 平台 | 本期定位 | 交付要求 |
| --- | --- | --- |
| macOS arm64（`darwin-arm64`） | 必交付 | core/full 归档、真实验收、CI 快照和正式发行闭环 |
| Linux amd64（`linux-amd64`） | 必交付 | 同上；明确最低发行版、glibc 和动态库基线 |
| Linux arm64（`linux-arm64`） | P0 评估后争取纳入 | 对应依赖、再分发条件和原生 runner 齐全，且能直接复用公共流程时纳入；纳入后执行同等交付与验收 |
| macOS amd64（`darwin-amd64`） | P0 评估后争取纳入 | 同上；另核对 Intel Mac 的最低系统要求 |

P0 同时核查上述四个目标。若全部依赖和 runner 可用，且另外两个目标只需增加目标依赖条目、摘要和矩阵配置，则四个平台在同一期完成。若额外目标需要专项移植、独立 Git 运行时方案或缺少原生 CLI 产物，则明确记录延后原因；未纳入的候选目标不阻塞两个必交付目标。Windows、Alpine/musl 及其他平台不在本期范围。

平台支持表须区分“候选”“本期交付”“已验收”及具体缺项；候选不代表正式支持。P0 确定本期交付矩阵，后续若发现新阻塞，须显式记录范围变化及原因，不得在 CI 失败后自动缩减矩阵。两个必交付目标存在缺项时，本期仍未完成。

### 10.2 P0：依赖与平台核查

实施记录：四目标的来源及原生依赖、runner 已核查并纳入交付；统一 lock、724 项 npm 依赖解析、公开资产允许清单和首批许可证材料已落地。DSH 完整部署、Git 完整再分发材料和逐平台真实验收仍未完成，完整状态见 [P0 交付与核查记录](implementation/packaging/P0.zh.md)。

| 步骤 | 工作与交付 | 完成条件 |
| --- | --- | --- |
| P0.1 固定版本和来源 | 建立统一 lock；锁定 DSH `dsh-v0.2.2-rc.1` 及满足 Node.js 24.9 下限的精确 Node 版本，校验现有 CLI 版本契约 | 各目标的来源、完整 commit/版本、下载摘要、布局和许可证可追溯；不使用浮动 latest |
| P0.2 核查平台依赖 | 同时核对四个平台的 Node、DSH 原生依赖、glab、scn、lark-cli 和可搬移 Git；列明再分发条件 | 区分已有产物、需适配项和缺项；尤其不能用 darwin_arm64 的 scn lock 推断其他目标已齐全 |
| P0.3 确定系统与 runner | 记录最低 macOS/Linux、glibc/动态库、shell、证书、进程观察要求及匹配架构的 runner | 每个拟纳入目标有可执行的构建和真实验收环境，不以交叉编译代替运行环境 |
| P0.4 固定交付范围和资产清单 | 输出平台支持表、core/full 能力表、`bundle-files.json` 允许清单及再分发材料 | 明确本期交付矩阵和延后目标的原因；必交付目标的 full 阻塞不得以只交 core 消除 |

阶段出口：依赖来源、平台差异和缺项均已核查，明确本期交付目标。若必交付目标的依赖尚未取得，可继续不依赖该项的公共工作，但 full 组装和正式发行保持未完成。

### 10.3 P1：公共打包能力

实施记录：公共入口与 macOS arm64 core 开发归档验证见 [P1 实施记录](implementation/packaging/P1.zh.md)。普通协作不依赖 Git；core 的 Git 缺项只影响 Git Workspace。full 和正式发行门槛保持关闭。

| 步骤 | 工作与交付 | 完成条件 |
| --- | --- | --- |
| P1.1 启动与初始化 | 实现公共 `start`、`teamx init`、`teamx doctor`，支持便携与显式 home 模式 | 从其他 cwd、空格路径启动；初始化不覆盖配置/模板；诊断准确显示版本、路径和缺项 |
| P1.2 构建与组装入口 | 实现 `package.sh`，按 target/profile/channel 消费 lock，构建三个 Go 程序并复制允许资产 | 平台差异通过依赖条目和必要适配表达，所有目标复用同一编排；不读取开发机私有配置和未知二进制 |
| P1.3 归档与检查 | 生成 manifest、包内摘要、归档摘要、许可证、使用说明及 `package-smoke.sh` | 最终 tar.gz 能在新目录解压检查；权限、架构和符号链接正确；失败不生成成功报告 |

阶段出口：公共入口和核心检查可用，至少一个目标的 core 开发归档通过第 9.1 节检查；此结果只证明公共流程可用，不代表其他平台或 full 已验收。

### 10.4 P2：多平台产物组装

实施记录：见 [P2 组装与验证](implementation/packaging/P2.zh.md)。新增确认的 Linux full 阻塞为 `git_tls_runtime`：固定 dugite 归档的 HTTPS helper 依赖宿主 libcurl 及其 TLS 库，归档没有携带这些库。补齐固定库归档及再分发材料之前，Linux full 组装必须失败；四目标交付矩阵保持不变，P2 不因单目标成功而结束。

| 步骤 | 工作与交付 | 完成条件 |
| --- | --- | --- |
| P2.1 各目标 core | 优先跑通 macOS arm64、Linux amd64，再对 P0 纳入的其他目标部署完整 DSH/Node 并组装 core | 各目标在匹配平台运行，脱离源码和宿主 Node；依赖不跨平台复制，不存在 staging 路径逃逸 |
| P2.2 各目标 full | 收集固定 glab/scn/lark-cli、可搬移 Git 及资源，补齐启动器与路径配置 | 所有承诺依赖随包完整提供，版本和无凭据帮助可用；缺项即 full 失败，不静默降级 |
| P2.3 输出开发归档 | 同一入口为交付矩阵生成 core/full、manifest、摘要与 smoke 报告 | 对每个最终归档在干净 PATH 下执行第 9.1 节检查；full 不借用宿主 Git |

阶段出口：本期交付矩阵的各目标归档均组装完成并通过离线 smoke；真实模型、平台账号及浏览器验收留到 P3，不能由此报告正式支持。

### 10.5 P3：逐平台验收

2026-11-08 本次范围调整：按用户要求先验证 macOS arm64；Git、Scion 不作为本次必需项，GitLab／飞书留给用户手动验证。现有归档先执行本地 DSH/ACP 与实际浏览器验收，记录见 [P3](implementation/packaging/P3.zh.md)。四目标完整矩阵、干净机器／下载后的系统安全检查和正式发行资格仍按下述原出口保留缺项，不以本次范围通过消除 P2 阻塞。

| 步骤 | 工作与交付 | 完成条件 |
| --- | --- | --- |
| P3.1 真实 DSH/ACP | 每个平台使用包内 DSH 和独立测试模型，验证最小回合、工具调用、取消/关闭、重启及原 Session 恢复 | 真实运行结果通过；端口、子进程和锁按合同结清；fixture 结果单独记录 |
| P3.2 Git 与平台能力 | 验证 Git helper/TLS、本地 commit、受控 clone，以及隔离账号登录、只读命令和连接绑定 | 无宿主工具补装即可完成承诺流程；远端写入仅在单独获准测试对象上执行 |
| P3.3 用户环境与浏览器 | 验证首页、建 Room、配置保留、独立 home；macOS 验证下载后签名/quarantine，Linux 验证声明系统基线 | 逐平台记录源码检查、离线 smoke、真实 DSH、真实平台和实际浏览器结果，受限或未执行项保留缺项 |

阶段出口：各交付平台通过第 9 节要求，报告绑定归档摘要和依赖指纹。任何必交付目标未通过时，不认定本期完成。

### 10.6 P4：CI 与正式发行

2026-11-08 并行范围调整：按用户要求在 Linux arm64 的 P3 验收期间实施 P4.1。四目标快照矩阵来自统一 lock；PR 使用 core，手动任务可选 core/full，full 缺项仍失败。此调整允许 CI 建设提前进行，不改变 P2/P3 或正式发行出口。实现与本地验证见 [P4.1 记录](implementation/packaging/P4.1.zh.md)，GitHub 原生 runner、artifact 实际上传下载的全矩阵结果需另行记录。

2026-11-08 进一步并行实施 P4.2：新增 tag 候选 Workflow、干净 tag 构建、可选 macOS Developer ID 签名／公证及下载复验；候选与正式发行资格分开，最终归档必须重新绑定验收证据。实现及缺项见 [P4.2 记录](implementation/packaging/P4.2.zh.md)。

2026-11-08 按用户要求实施 P4.3，并明确本期 macOS 不要求签名、公证或 Apple 证书。新增手动触发的 `release-publish.yml`：读取指定成功 tag 运行的同一批候选，在 GitHub 原生 runner 执行真实 DSH/ACP 与浏览器验收；干净机器、下载后的系统行为和 full 平台能力接入逐归档维护者人工证据。仅完整四平台 × core/full 全部具备资格后汇总并上传 draft，下载验证所有远端资产，再公开 Release。Linux full TLS 缺项、真实验收缺项仍阻止发布，不妨碍流程代码先行实施。实现、使用方法及未执行项见 [P4.3 记录](implementation/packaging/P4.3.zh.md)。

| 步骤 | 工作与交付 | 完成条件 |
| --- | --- | --- |
| P4.1 CI 快照 | 实现 `package.yml`、目标原生 runner、隔离缓存、artifact 上传与下载复验 | CI 调用同一本地入口，最终归档通过 smoke；下载 artifact 后能力和摘要可核对 |
| P4.2 正式候选产物 | 实现 tag 触发的 `release.yml`，从干净 tag 构建，完成必要签名/公证、归档及验证 | 各目标最终候选资产具备匹配的验收证据；若相对 P3 的程序、运行时、配置或最终归档变化，复验受影响项目，不能直接沿用旧归档的通过结论 |
| P4.3 完整矩阵发行 | 汇总校验和、许可证、首次使用/升级说明及报告，再上传已验收的同一批 Release assets | 覆盖全部必交付和已纳入目标，无失败后自动缺项发布；发布 job 不重新构建，上传资产摘要与验收归档一致 |

阶段出口：两个必交付平台及 P0 纳入的其他平台，均按明确的正式包型范围完成打包、真实验收、CI 和发行闭环；当前为 core/standard 共 8 项。范围内不允许缺项发布；范围外 full 保留未完成状态，不将 standard 的通过推广为 full 已通过。

实施文件以 `packaging/`、`scripts/`、初始化/诊断 CLI、`.github/workflows/` 和使用说明为主。现有 Room、DSH 恢复检查及平台账号授权保持各自职责。P0 不修改业务实现，也不表示任何新发行包已经构建或验证。
