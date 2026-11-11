# Scion 接入 TeamX 软件开发团队方案

状态：S0–S4 已实施，原生 CLI、成员只读、独立 Workspace、正常重启及真实 DSH Review 版本化协作已有通过记录；实际浏览器、复杂设计交接、完整故障矩阵及 S5 合入仍待验证。当前复跑方法和边界见[Scion 验证与待验项](scion.zh.md#验证与待验项)。

调研日期：2026-11-07。目标团队为 `.teamx/teams/software-development`，目标实例为 `https://scion.opencsg.com`。用户已提供并指定后续实施与验证仓库：[OpenCSG/teamx-demo](https://scion.opencsg.com/OpenCSG/teamx-demo)。

范围说明：第 1–8 节描述接入的完整设计方向；**第一期 POC 的必需范围与暂缓项以第 9.0 节为准**。后续增强不作为 POC 完成的前置条件。

## 1. 建议与首期交付

新增与 `gitlab` 并列的 `scion` 工具，沿用 `teamx tools list/run/skill`、成员工具绑定、平台连接和 Workspace 管理。使用 Scion 原生 `scn`，每个成员拥有独立的 `.scion` 工作副本。

TeamX 继续负责 Room 协作、成员身份、进程生命周期和 Workspace 切换；Scion 负责仓库、提交、分支、Review 和服务端合入。首期无需接入通知、Inbox、followups 或 MCP，也无需改造 Scion 服务端。

首期完成以下闭环：

1. 配置并验证 Scion 连接，architect、developer、reviewer 分别显式启用工具。
2. 配置 demo 仓库，各成员独立 clone 到自己的 Workspace。
3. architect 按需交付设计；developer 修改、测试、提交并发布任务分支，创建 Review。
4. reviewer 读取 Review、完整讨论和准确版本的代码，在自己的 Workspace 独立测试，发布版本化评审。
5. developer 修正并更新同一个 Review，reviewer 对新 revision 复审；Room 交付实际链接、版本和结论。

本方案中的“MR/PR”是业务称呼，Scion 原生对象叫 **Review**，不是 GitLab Merge Request。首期交付开发与评审，不承诺版本锁定的自动合入；相关原生接口限制和后续方案见第 8 节。

## 2. 调研基线与实际验证

### 2.1 版本基线

| 项目 | 核实结果 |
| --- | --- |
| 本地源码 | `/Users/russellluo/Projects/work/opencsg/projects/scion` |
| 本地 HEAD | `2c3253832b6694af2be444ac71f6c8446fd72a6c` |
| `v0.1.13` Git tag | `3820ffd1d3a00b82c4c798e1ecc377753b05b499` |
| 线上发布 | [v0.1.13](https://scion.opencsg.com/OpenCSG/scion/releases/v0.1.13)，已认证读取成功 |
| 本机发布包 | `scn-v0.1.13-aarch64-apple-darwin.tar.gz`，已下载并核对同发布的 `.sha256` |
| 发布包 SHA-256 | `4a3d15ce772070fddd89837d66e443b22f4baf9cc253bd64175e3b19456eeb07` |
| 实际版本输出 | `scn 0.1.13` |

本地 HEAD 已包含 tag 之后的改动，例如 Review 列表分页调整、建议应用，以及 Hub 地址推导变化。下面的基线能力以 `git show v0.1.13:<文件>` 和实际发布包帮助为准，不能直接按 HEAD 的 README 实施。

### 2.2 初次方案调研完成的只读验证

- 未登录时，线上发布 API 和下载地址返回 401；这是认证要求，不是功能缺失。
- 初次调研使用用户临时提供的 Bearer，读取发布详情、`OpenCSG/` 仓库列表均返回 200；该次历史快照仅含 `OpenCSG/scion`。用户随后已上传并指定 `OpenCSG/teamx-demo` 用于后续实施与验证，此历史列表不代表当前仓库状态。
- 发布文件采用每个压缩包对应的 `.sha256`；该发布不存在 `SHA256SUMS`。不能原样套用依赖 `SHA256SUMS` 的安装脚本。
- 在临时隔离目录运行官方发布包，核实 `--version`、`review create --help`、`review land --help`。
- 原生 `scn --json doctor https://scion.opencsg.com` 成功，报告服务端 `0.1.13`、协议 `1.0`、`auth.required`；当时没有声明 `auth.device`，因此当前实例不能直接按浏览器 device flow 登录。
- 同一个短期 Bearer 可访问 REST，但以 `SCION_TOKEN` 注入 CLI 后，`whoami` 和 `repo info` 均退出 5，错误为 `Unauthenticated: access tokens cannot be exchanged`。

临时 Bearer 没有写入本文、仓库配置或凭据文件。本文不记录其内容；其有效期不适合作为未来实施条件。

**初次调研结束时尚未验证：** CLI 浏览器登录、PAT 登录、原生 clone、Review 读取/创建/评论、真实 DSH 三人协作、浏览器配置闭环和任何远端写入。源码存在相关能力，不等于上述场景已验收。后续 S0 的实际结果见下节。

### 2.3 S0 实际探测（2026-11-07）

使用固定官方 `scn 0.1.13` 和用户提供的原生 PAT 完成指定私有凭据文件登录，实际 subject 为 `csgship`；后续新进程读取已保存凭据，`whoami`、`repo info OpenCSG/teamx-demo` 和显式 Hub 的 `review ls` 均成功。当前实例仍未声明 `auth.device`。

仓库 canonical name 已核实，默认分支为 `main`。初次探测时分支列表为空，定向读取返回 `REF_NOT_FOUND`；用户随后明确选择用已展示的最小 README 初始化。现已完成原生 clone、仅 README 的 commit、分支 land、远端逐项核验及另一目录的独立 clone，完整基线为 `c1078e705cc235a88699caf8aaf51c45803df33aafb8849ea36949756f8a36d4a0`。原生默认提交作者是 `scion <scion@localhost>`，与认证 subject 不同；后续成员作者配置仍须显式提供。当前复跑方法与验证边界见 [Scion 验证与待验项](scion.zh.md#验证与待验项)。

## 3. Scion 的实际能力与语义

### 3.1 能力对应关系

| 开发动作 | Scion v0.1.13 | 接入注意点 |
| --- | --- | --- |
| 登录与身份 | `login`、`logout`、`whoami`、`token` | 顶层命令，没有统一的 `auth` 命令组 |
| 仓库读取 | `repo info NAME`、`repo ls PREFIX` | 核对 canonical name 和默认分支 |
| 本地工作副本 | `clone URL DIR --ref BRANCH` | 生成 `.scion/`，没有 `.git/` |
| 状态、文件差异 | `status`、`diff`、`cat`、`log` | 历史和版本读取依赖服务端，无本地 Git 对象库 |
| 任务分支 | `switch -c NAME`、`branch` | 分支位于服务端；名称须区分 Room、成员和任务 |
| 提交 | `commit -m TEXT PATH...` | 服务端创建 pending commit，不自动推进当前分支 |
| 发布任务分支 | `land` | 推进当前分支；不能误在目标分支执行 |
| 同步 | `sync`、`resolve` | 可 rebase pending commit，冲突需明确处理 |
| 创建、读取 Review | `review create/show/ls` | number 属于具体仓库，不是 GitLab IID/全局 ID |
| 更新 Review | `review push NUMBER HEAD`、`review update NUMBER` | 更新 revision；后者可能服务端 rebase，旧结论须重新判断 |
| 评审评论 | `review comments/comment/resolve` | 支持 revision、行定位与线程回复 |
| 正式 verdict | `review approve/request-changes` | 会改变平台评审状态；普通文字评审不等同于 approve |
| 合入、关闭 | `review land/close` | 与任务分支 `land` 区分；自动合入另设实施边界 |

### 3.2 必须写进工具 skill 的差异

**有两种 land：** `scn land` 发布当前任务分支，相当于开发交接中的 push；`scn review land N` 将 Review 合入目标分支。后者需要具体用户决定，不能因为前者是正常开发动作就自动执行。

**没有 staging area：** 不带路径的 `scn commit` 包含全部本地改动。默认用明确路径提交任务文件；不把 `scn add` 当成暂存隔离。测试产物、`.env`、TeamX 管理文件等应在正确目录和 ignore 规则下处理。

**commit ID 不是 Git SHA：** 原生 changeset 使用完整的 Scion ObjectId。交接应写完整 base/head commit ID，保留命名空间；不得强制套用 40 位 Git SHA 校验。只有实际使用 Git gateway 时，才单独保存明确验证过的 Git SHA 映射。

**发布后再取 head：** 任务分支 `land` 可能 rebase 并返回 rewritten 映射，提交前的 ID 不一定是发布后的 head。测试和交接必须对应发布后的实际版本；代码树变化需补测。

**不能使用 Git 的评审检出流程：** reviewer 使用 `scn switch --detach FULL_HEAD`，不执行 Git fetch、`FETCH_HEAD`、`refs/merge-requests/*` 或 Git checkout。

**显式指定 Hub：** v0.1.13 默认把 Hub 推导为主机的 7088 端口。本实例统一入口应显式设置 `SCION_ENDPOINT=https://scion.opencsg.com` 和 `SCION_HUB=https://scion.opencsg.com`，并真实验证两条链路；不能依赖较新源码的同地址推导。

**JSON 保持原生 schema：** `review show --json` 的 Review 字段在顶层，附带 `revisions/checks/verdicts/timeline`；准确 base 从对应 revision 取，不能假定存在 GitLab 风格的嵌套字段。`review ls` 默认最多 50 条，`truncated=true` 时提高读取范围，不能据第一页判断没有现有 Review。

### 3.3 是否使用 Git gateway

源码也提供 Git gateway，未来可支持“Git 工作副本 + Scion Review”。本次推荐原生 `scn` 路径：用户希望新增 Scion 工具，原生路径可覆盖源码管理和代码评审，并避免首期同时维护两类提交 ID、认证链路和工作副本。

Git gateway 留作独立后续能力，不把 Scion 识别成 GitLab，也不经 `glab` 调用 Scion。

## 4. 工具与连接设计

### 4.1 统一入口与独立工具

新增薄封装 `tools/scion`，构建为 `teamx-scion`，在工具目录注册 `name=scion, provider=scion`。

```sh
"$TEAMX_CLI" tools list
"$TEAMX_CLI" tools run scion --help
"$TEAMX_CLI" tools skill scion
"$TEAMX_CLI" tools run scion --json review show 12
```

`teamx-scion` 负责解析固定 `scn`、核对版本、组装明确的运行环境、非交互执行和子进程结清；命令、stdout/stderr、JSON schema 和退出码尽量沿用原生 CLI。不重写 Review API，不创建假的 `mr`/`pr` 兼容层，不导入 TeamX `internal/` 包或回调内部业务协议。

建议解析顺序：

- TeamX 找封装：`TEAMX_SCION_BIN` → TeamX 同目录 `teamx-scion` → 服务 PATH。
- 封装找原生 CLI：`TEAMX_SCN_BIN` → 封装同目录 `scn` → 服务 PATH。

显式路径失效不回退。新增 `scn.lock.json` 固定版本、发布 URL、支持的平台、包名和对应校验文件；使用本次已核对的 macOS arm64 包补齐一项，其他平台另行核实。服务不自动 self-update，版本变化由操作者升级并重新验证。

进程在服务端、对应成员的有效 Workspace 根目录启动；Agent shell 中临时 `cd` 和环境设置不会传给服务端。按子目录执行时显式使用原生 `-C`，按实际执行目录解释相对文件参数。帮助无需凭据或业务 API；版本不匹配/缺少依赖返回明确安装诊断。

### 4.2 原生认证：建议优先复用登录能力

v0.1.13 已实现：

```sh
scn login https://scion.opencsg.com
scn login https://scion.opencsg.com --with-token < /private/path/token.txt
scn whoami https://scion.opencsg.com
scn logout https://scion.opencsg.com
scn token --help
```

浏览器登录使用 device flow，成功后由 CLI 保存 refresh token；服务器须支持 `AUTH_DEVICE`（特性名 `auth.device`）。**当前线上实例未声明此能力，首期走原生 PAT 登录，浏览器登录待服务端启用后再验收。** `login --with-token` 也先交换并验证凭据，不能把本次短期 access token 换个入口当长期凭据。

| 接法 | 适用性与成本 | 建议 |
| --- | --- | --- |
| CLI 原生登录 + 隔离凭据文件 | 当前用 PAT 登录并由 CLI 保存；启用 device flow 后可保存 refresh token。TeamX 保存引用，需增加 profile 运行环境与身份复核 | 推荐的常规入口 |
| TeamX 保存 PAT，再注入 `SCION_TOKEN` | 可沿用现有私有凭据存储；需操作者提供可交换的有效 PAT | 可作无浏览器部署的替代，按需实施 |
| 保存网页短期 Bearer | REST 可用，但本版本 CLI 会再次交换并失败，且很快过期 | 不作为 CLI 接入方式 |

当前页面可自动发现当前服务的受管账号，或由操作者提交 PAT，通过原生 `login --with-token` 完成新的隔离登录，再保存并验证连接；无需手工填写 profile ID。命令行预先登录仍可用；“在 TeamX 页面直接启动浏览器登录”可后续独立交付，依赖服务端启用 device flow。Agent 正常开发不负责登录、切换账号或搜索全局凭据。

### 4.3 隔离登录配置与账号证据

Scion 没有原生命名 profile，但支持 `SCION_CREDENTIALS_FILE`。可据此构造 TeamX 的连接级 profile：

```text
<service-private-root>/scion-profiles/<opaque-profile-id>/credentials.json
```

每个 profile 一个受管目录，0700；凭据文件 0600。操作者登录及工具执行必须使用同一个显式 `SCION_CREDENTIALS_FILE`，并设置 `SCION_CREDENTIALS=file`、`SCION_NO_DAEMON=1`。避免依赖 macOS Keychain 弹窗和后台 daemon 脱离 TeamX 进程结清。

例如，实施时由操作者在已创建的私有 profile 目录中完成登录；执行环境须清除已有的 `SCION_TOKEN`，避免它覆盖已保存凭据：

```sh
SCION_CREDENTIALS=file \
SCION_CREDENTIALS_FILE=/private/scion-profiles/<profile-id>/credentials.json \
SCION_NO_DAEMON=1 \
/absolute/path/scn login https://scion.opencsg.com --with-token < /private/path/pat.txt
```

随后以同一个凭据文件执行 `whoami`，核实实际账号；`/private/...` 是路径占位，不应把 PAT 写入命令参数、团队文件或文档。

TeamX 连接保存 profile 的非秘密引用，不复制 refresh token 到 Room、成员 `tool_env`、DSH 配置或前端。不得隐式使用操作者 `~/.config/scion/credentials.json`。现有 `CLIProfile` 可作为引用载体，但应增加 Scion 自己的验证与路径解析，不能沿用 Lark 的 brand/profile 校验规则。

连接验证执行固定版本、隔离环境下的 `scn --json whoami`，核对实例、schema 和非 anonymous subject，再存入现有原生账号结构，例如 `namespace=scion:subject`。不转换为整数 `UserID`，不把 JWT 本地解码结果当作已验证账号。

S2 实施时当前连接快照已升级为 v4，支持原生账号与 CLI profile；Scion 沿用此版本，不静默迁移旧快照。旧版本由现有显式升级路径处理；GitLab 保持 OAuth-only 合同。

**profile 引用有效不等于登录身份不变。** 当前 profile 指纹主要来自引用元数据，不能证明外部凭据文件仍对应原账号。Scion 适配器须在使用前读取实际身份，并与连接绑定 subject 核对；切换/重新登录按连接变更结清在途工具。重新登录建议在新的 profile 目录完成，验证成功后显式切换代际，避免原地覆盖仍被任务使用的文件。身份不符停止执行，不自动接受新账号。

普通工具执行仍使用现有临时 HOME；单独注入经过服务端解析的 Scion 凭据文件引用。PAT 模式仅向工具进程注入秘密。禁止继承全局 `SCION_TOKEN`、`SCION_HUB` 或备用登录来源；显式实例/Hub/仓库 URL 必须与当前连接匹配，避免将凭据发送到其他主机。

本实例首期 Endpoint 与 Hub 为同一 HTTPS origin。未来若需要分离 Hub，须显式声明可信映射并验证，不能让模型自由指定接收凭据的地址。

### 4.4 复用的生命周期与不复用的业务

继续复用成员显式绑定、连接代际、取消、关房/退出等待、工具 stdout/stderr 流。profile 模式需要补充进程私有运行值，因为当前 `prepareToolLocked` 主要只组装 `instance/token`，不能只给 Catalog 增加一项就认为已接通。

账号可执行操作由平台权限决定；工具开关和角色 prompt 不构成权限沙箱。不新增每操作权限表或项目白名单。首期常规评审使用普通评论；不自动调用 approve、request-changes、合入或关闭。

Scion 不接入 GitLab 的身份解析、MR 评论内部适配器或 Todo。后续若接通知，单独增加 `provider=scion` 的来源、对象与动作适配器，再接现有通用编排。

## 5. Workspace：作为独立版本管理后端

### 5.1 现状与必要改动

S3a 已抽出 `internal/workspace` 仓库接口与 `internal/workspace/git`，共用准备通过接口执行本地检查、初始化和配置；GitLab 身份校验与访问能力由 Room 装配边界桥接。S3b 已增加 `repository.kind` 与 ScionAdapter，平台绑定按 provider 校验；Scion 初始化沿用相同操作与 Session 生命周期。既有无 kind 快照保持原 Git 语义，不在读取时重写。

建议抽出共用 Workspace 管理与仓库适配接口，为仓库增加 `kind`，通过注册表选择 `git` 或 `scion` 适配器，不在通用准备流程中堆叠类型分支。已有未填写 kind 的仓库按既有 Git 语义读取；读取时不自动重写冻结快照。Workspace 根目录仍是第一层能力，没有仓库时保持普通目录。

以下为 S3b 已支持的有效配置形状；管理 apply 请求另带操作 ID、版本与 caller，首次仓库根目录由服务分配：

```json
{
  "root": "workspaces",
  "repository": {
    "kind": "scion",
    "url": "https://scion.opencsg.com/OpenCSG/teamx-demo",
    "branch": "main"
  },
  "binding": {
    "provider": "scion",
    "url": "https://scion.opencsg.com",
    "project": "OpenCSG/teamx-demo",
    "connection_id": "<connection-id>",
    "connection_generation": "<verified-generation>"
  }
}
```

`project` 在 Scion 分支中保存服务端核实的 canonical repository name；不要求 GitLab 的数字 `ProjectID`。如果需要保存用户输入路径与 canonical name 的差异，用明确字段分别保存。新校验按 provider 分流，不能放宽现有 GitLab 的身份要求。

clone 用 Workspace 选择的连接；日常命令用各成员显式绑定的连接。clone 连接不自动授予成员工具权限，也不把所有人的开发写入绑定到 clone 账号。首期通常复用同一连接；有独立平台账号时可分别绑定，clone 凭据不写入 `.scion`。

### 5.2 目录与初始化

```text
<room>/workspaces/<architect-membership-id>/.scion/
<room>/workspaces/<developer-membership-id>/.scion/
<room>/workspaces/<reviewer-membership-id>/.scion/
```

使用 membership ID 隔离，沿用已有 Workspace 切换、操作收据和 Session 更新。工具在各自根目录执行，不创建共享 working copy，也不依赖跨成员本地路径传文件。

初始化顺序：

1. 验证连接与仓库身份，读取默认分支，并核对所选分支确实存在。v0.1.13 clone 对不存在的 ref 可得到空工作副本，不能把拼错分支当作成功。
2. 检查全部成员目标目录和路径归属，再执行 clone。目标只能不存在或为空，现有非空普通目录、Git 仓库与 Scion 仓库不互相覆盖转换。
3. 每成员执行固定 CLI 的 `clone URL MEMBER_DIR --ref VERIFIED_BRANCH`，首期完整检出；暂不增加 sparse 配置和缓存 daemon。
4. 保留 TeamX 的 `.clone-pending` 与操作阶段记录。Scion CLI 在部分 clone 失败时可能移除自己创建的目录，所以即使目录消失也不能据此自动重试；失败后由原操作的显式重试处理已核实目标。
5. 核对 `.scion/state.json` 的 format、endpoint、canonical repo 与操作目标，再提交有效 Workspace 快照并更新对应 Session。已有准备完成的成员按原操作收据恢复，不能因另一个成员失败而重 clone 全部目录。

回访只检查本地结构、仓库身份和有效配置，不自动 clone/sync/switch/land，不把成员的任务分支或 detached 状态改回初始 main。连接与工具的真实身份核验属于显式平台调用，不伪装成本地回访。

### 5.3 页面与作者配置

Workspace 页面添加“Git / Scion”仓库类型选择，再填写仓库 URL、分支、连接。由 URL 派生 origin 和仓库路径；不要求用户重复填写能推导的信息，不按域名猜类型。Scion URL 不自动追加 `.git`。

成员工具设置展示 `Scion`、当前账号、实例、连接状态，继续分别点击应用；工具开关不安装程序。目录、clone 进度及失败重试沿用现有交互。

临时 HOME 会隔离 `scn config` 和全局 Git 作者配置。提交作者由明确的非秘密成员配置或每次 `--author 'Name <email>'` 提供，需要时注入 `SCION_AUTHOR`；不要指导 Agent 在每次都会消失的临时 HOME 中永久设置作者。提交 author 与认证 subject 是两种证据，不伪称配置作者改变了实际操作账号。

### 5.4 抽象边界：共用管理、仓库适配、平台访问

这次解耦只针对 Workspace 的仓库准备。通用通知、Inbox、followups 和 Agent/DSH 运行内核不因此增加 Git/Scion 分支。软件开发角色中的平台特定指令另按第 6 节调整。

```text
Room / Session
      |
Workspace 管理：目录归属、操作记录、准备调度、配置提交、Session 切换
      |
RepositoryAdapter 注册表（按 repository.kind 选择）
      +-- GitAdapter：.git、origin、Git clone、Git 本地认证配置
      +-- ScionAdapter：.scion、canonical repo、scn clone、本地状态

平台访问：连接、账号、凭据租用、仓库核验、调用取消与结清
      +-- GitLab：为 Git 提供经过核实的仓库与认证能力
      +-- Scion：为 Scion 提供经过核实的仓库与认证能力
```

**版本管理类型与托管平台分开建模。** `repository.kind=git/scion` 决定本地工作副本实现，`binding.provider=gitlab/scion` 决定平台访问。Git 可以来自 GitLab、其他 Git 服务或没有平台绑定的仓库，不能把 `GitAdapter` 实现成 `GitLabAdapter`。未来接 GitHub 时应复用 GitAdapter。

职责分配如下：

| 归属 | 承担职责 | 不应包含 |
| --- | --- | --- |
| 共用 Workspace 管理 | Room/membership 路径、目录越界与重叠检查、操作 ID/版本、持久准备进度、取消、失败显式重试、配置提交、Session 更新 | `.git/.scion` 判断、GitLab ProjectID、scn 参数、平台认证协议 |
| 仓库适配器 | 仓库配置规范化、本地元数据和仓库身份检查、显式初始化/配置、非秘密执行配置与工具说明 | Room 索引读写、Session 旋转、通知投递、MR/Review 业务动作 |
| 平台访问适配器 | 账号与连接核验、远端仓库身份、默认分支、认证访问、短期凭据和身份代际 | 成员目录布局、Workspace 操作状态机 |

接口保持围绕初始化与回访的最小范围。以下是拟议 Go 形状，DTO 名称不代表已存在代码：

```go
type RepositoryAdapter interface {
    Normalize(RepositorySpec) (RepositorySpec, error)
    InspectLocal(context.Context, LocalRequest) (LocalInspection, error)
    Prepare(context.Context, PrepareRequest, PreparationHost) (PreparedRepository, error)
}
```

- `Normalize`：纯配置处理，无网络或文件修改。公共层校验目录和类型，适配器校验自身 URL/ref 语法；平台绑定的账号和原生仓库 ID 仍由平台适配器核验。
- `InspectLocal`：只读检查，返回 missing/empty/ready/incomplete/mismatch 等明确状态、可验证的本地仓库身份及非秘密运行描述。Git 检查 `.git`、实际工作树和有效 origin；Scion 检查 `.scion` 格式、endpoint、repo。不自动联网、修复、切分支或修改文件。
- `Prepare`：只在显式 Workspace 应用/获准的原操作重试中调用。根据已检查状态初始化缺失副本、准备认证引用和作者等配置，再返回验证结果。遇到非空不兼容目录或未确认失败现场时拒绝；不承担恢复时的自动同步。

`PrepareRequest` 使用经过核心校验的成员路径、仓库目标、已核实远端身份和访问引用；不包含整份 Room/Session 配置。`PreparationHost` 只提供受管进程执行及当前操作的准备检查点等窄能力，不提供任意 Room 文件写入。平台访问桥接通过组合注入，接口不暴露 `gitlab.ReadAPI`、`gitlab.Binding` 或整数项目 ID；不把多平台差异藏进无校验的 `map[string]any`。

GitLab 相关的仓库核验、OAuth 凭据租用和 Git HTTPS 认证由平台桥接提供给 GitAdapter。现有 `workspace-git fetch/push/ls-remote` 仍可作为 Git 的专属能力保留；不要求 Scion 伪装成 Git 子命令，也不让共用 Workspace 管理理解 `write_repository`。

`PreparedRepository` 与 `LocalInspection` 提供成员级结果，包括已验证本地身份、认证引用/辅助程序路径及必要工具说明。持久内容不得包含 PAT 或 access token；成员不同的配置不能再合并为一份共享环境。临时凭据在实际进程执行时经平台访问能力取得，沿用既有取消、轮换和结清机制。回访使用保存的运行描述并作本地检查，不重新申请凭据或执行远端初始化。

公共准备流程统一为：检查所有目录与目标 → 选择已注册适配器 → 本地检查 → 按显式操作核实远端及准备各成员 → 本地复核并保存准备结果 → 提交有效配置 → 更新 Session。普通目录只经过共用目录管理，不需要实现一个虚假的仓库适配器。

持久准备进度由共用操作管理持有。现有 `.clone-pending` 语义继续覆盖失败现场，必要时补充按 membership 记录的准备完成证据；适配器不另建一套独立状态机。失败的多成员操作只能在原目标上显式恢复，对已完成副本先做本地核验，不能无条件再次 clone。

首期只注册两个实现，不建设动态插件系统，也不把 commit、push、review、merge 抽进通用 Workspace 接口。这些命令语义不同，继续由原生工具和各自 skill 表达。

### 5.5 解耦顺序与完成判据

先抽出 GitAdapter，保持现有 Git/GitLab 行为并验证回归，再增加 ScionAdapter。不要一边重写现有生命周期，一边引入新的版本管理后端。

建议目录为 `internal/workspace/`（接口和经过校验的共用 DTO）、`internal/workspace/git/`、`internal/workspace/scion/`；Room 保留操作状态机与 Session 编排，并在装配处注册具体实现。依赖方向是 Room → Workspace 接口/适配器，适配器不得反向导入 Room。

解耦完成需要满足：

1. 共用 Workspace 准备和恢复流程不再直接调用 Git/scn，或判断 `.git/.scion`；具体实现仅在装配处选择。
2. 共用配置与操作校验不要求 GitLab 数字项目 ID；GitLab 历史字段继续由其适配层验证，不能直接删除或放宽。
3. 读取旧快照仍按原有 Git 语义，路径、连接和 Session 身份不变；不因新增 kind 自动重写历史配置。若确需不兼容结构变化，单独定义显式版本处理。
4. Git 与 Scion 共用目录归属、失败重试、取消、配置提交和 Session 切换的合同测试；各适配器分别验证原生元数据、认证配置和真实 clone。
5. 回访不会把当前任务分支或 detached 状态强制改为初始 ref；initial branch 只用于初始化，不成为永久分支约束。

## 6. 三个成员的实际协作流程

### 6.1 Developer 的正常路径

下面命令展示拟接入后的原生语法；仓库、编号、分支及完整 ID 均须由任务实际读取，示例不是现在执行远端写入的指令。

```sh
# tools run 默认就在本成员 Workspace，先检查状态、仓库与目标分支。
"$TEAMX_CLI" tools run scion --json status --no-ahead-behind
"$TEAMX_CLI" tools run scion remote get-url
"$TEAMX_CLI" tools run scion --json repo info OpenCSG/teamx-demo

# 确认起点后创建唯一任务分支，再修改、测试、检查 diff。
"$TEAMX_CLI" tools run scion switch -c teamx/<room>/<membership>/<task>
"$TEAMX_CLI" tools run scion commit -m '实现需求' --author 'Name <email>' src tests
"$TEAMX_CLI" tools run scion --json land

# 核对发布后的 head，并先查是否已有本任务 Review。
"$TEAMX_CLI" tools run scion --json review ls --state all --limit 100
"$TEAMX_CLI" tools run scion --json review create <published-full-head> --target main --title '实现需求' --description '范围与验证结果'
"$TEAMX_CLI" tools run scion --json review show <number>
```

起点必须是用户指定或核实默认目标的当前版本；不能在 reviewer 的 detached 版本上机械复用创建步骤。已有任务分支/Review 时恢复该任务，不重复创建。

版本化交接包含：实例、canonical repo、Review number 和实际网页链接、target、revision、对应 base/head、测试和未验证项。发布后的 head/代码树如与已测试版本不同，需要按差异重新验证。

修正时继续原任务分支，提交并 `land` 后执行 `review push NUMBER FULL_HEAD`，读取同一个 Review 的新 revision，再发 `[待复审]`。`review update` 的服务端 rebase 同样属于版本变化，不能自动沿用旧评审。

### 6.2 Reviewer 的独立评审

1. 读取 `review show NUMBER`、`review comments NUMBER`，核对实例、仓库、open 状态、目标、revision、对应的完整 base/head。阅读完整上下文；读取失败或列表截断不表示没有评论。
2. 检查自己的 Workspace 属于同一仓库，本地文件、pending stack 和冲突均允许安全切换。存在未保存工作时先保留并报告，不 reset/clean 或擅自用 stash 清场。
3. `switch --detach FULL_HEAD` 后用 `status --no-ahead-behind` 核对实际 base 等于 head、detached=true 且没有 pending；必要时对照 `diff FULL_BASE FULL_HEAD` 与完整文件，独立运行测试。
4. 发布前再读 Review。版本已变时，旧结论只能标记适用旧 revision，不能宣布最新版本通过。
5. 发布普通评审评论，明确完整 base/head、revision、设计/步骤版本、实际测试、证据问题和未验证项。例如 `review comment NUMBER '评审正文' --revision REVISION`；行评审另外给出 path/side/line，线程回复用真实 comment ID。
6. 发布后读取实际评论 ID、作者、正文及当前 Review 版本，再向 Room 发 `[评审结果]`。网络结果未知时先只读核对，不能再次发同文尝试探测。

本地独立 Workspace 不等于独立平台账号：三人绑定同一个账号时，平台作者可能相同。普通评论仍可用角色文字说明职责；若未来要求原生 approve，须核实不同 subject、不能自批及平台分支规则。

### 6.3 Architect 与角色指引

保持现有“简单需求直接开发，复杂需求先设计”的协作约定。architect 的 `design.md` 通过其任务分支、完整 commit ID 和 Review 交接；developer 从已核实版本读取正文或以原生 cherry-pick 等方式明确集成，不凭另一个成员本地路径工作。设计 Review 的目标分支与实现依赖应写清，避免重复引入设计改动。

角色 prompt 保留职责、版本化交接和用户决策规则，移除必须使用 Git/glab/GitLab SHA 的前提，具体协议由当前工具说明和 `tools skill scion/gitlab` 提供。平台 Review 创建受阻时仍可先在 Room 交付完整版本和评审，但明确标注平台闭环待完成。

新角色模板只影响新 Room；已有成员、Session 和角色快照不自动改写。新增工具绑定可按当前机制显式应用，动态说明不得继续强制旧 GitLab 协议。首次三人验收建议使用新 Room，保留原 GitLab Room。

## 7. demo 仓库与运行前提

用户已上传 [OpenCSG/teamx-demo](https://scion.opencsg.com/OpenCSG/teamx-demo)，并明确允许后续实施和验证使用该仓库。真实 clone、任务分支发布、Review 创建/更新、评审评论及复审均以此为目标，不再安排创建 demo 仓库，也不对 `OpenCSG/scion` 做试验写入。目标授权不扩大当前实施阶段范围，实际进度以各阶段验收记录为准。

实施验收时由仓库负责人准备：

- 在已指定仓库中核实 canonical name、默认目标分支和基线提交，读取现有 README、项目测试入口和 ignore 规则；按实际内容确定验收任务。本文的 `main` 仍为示例，实施时以已核实目标为准。
- 选定账号的有效登录/可交换 PAT，及该仓库所需 read、write、land 权限。任务分支 land 也需要对应平台权限，不能仅验证 read 后推定可发布。
- 确认 Endpoint 与 Hub 都可用；先 `whoami` 和 `repo info`，再读 Review 验证 Hub。暂不配置强制 approve/CI gate，除非对应账号、runner 和检查已准备好。
- 用平台 branch rules 保护 main，开发发布只落到任务分支；TeamX 角色约定不能代替服务器权限控制。

需求仍可沿用现有简短 Todo 看板示例，不把登录、仓库路径、命令清单和评审规范塞进用户 opening message。启动说明、工具 skill 和运行时上下文承担这些信息。

## 8. 自动合入、未知结果与扩展边界

### 8.1 v0.1.13 的两个具体限制

`LandReviewRequest` 只有 repo、number、delete_branch，没有 expected head/revision/target tip；`review land` 帮助也没有相应参数。执行前读一次 head 不能消除读取和写入之间的竞争。服务端还可能基于更新后的目标分支 rebase。

此外，Hub 的 land 可能入队，返回空 tip 且 Review 仍未 landed；v0.1.13 CLI 的 `ReviewDoneOut` 仍可写 `action=landed`。因此退出 0 或 action 文本不能作为最终成功证据，必须独立读 Review 的实际 state、landed commit 与目标分支结果。

### 8.2 建议后续独立交付

首期完成“创建 → 阅读 → 独立评审 → 修正 → 复审”，由用户决定后续合入方式。原生工具保留账号有效能力，但团队正常流程不承诺自动执行 Review 合入/关闭；不得把“评审通过”当作用户确认。

若希望 TeamX 可靠执行“只合入用户确认的版本”，建议先在 Scion API/CLI 增加可原子核对的 expected revision/head，并明确目标 tip/服务端 rebase 策略；对变化返回冲突，入队明确返回 queued 和可查询标识。再增加用户决定绑定、对应版本复核、最终状态查询和真实验收。只在封装里“查完立即 land”不满足这个保证。

普通评论可显式指定 revision，首期按该版本发表；原生 approve/request-changes 对当前 revision 生效，不自动转换普通评审为 verdict。

### 8.3 写入与恢复的最低合同

- 原生退出码按命令解释：0 成功返回；1 一般失败；2 用法问题；3 冲突；4 暂时性错误；5 同时可能表示认证失败或权限不足。不能统一把 5 解释为登录过期。
- create、comment、review push、land 等发生超时、中断、断流后，不自动重发或切换 REST 重试。先记录已知目标、时间、head/revision 和返回标识，再查远端。
- 创建前以仓库、任务标识、author、目标和完整 head 查已有 Review；评论以作者、正文、revision 和实际 comment ID 核对。原生 API 未证明支持幂等键，不声称稳定 Room 消息 ID 能为远端写入去重。
- 任务分支发布不确定时，查询该分支实际 tip 和本地 pending 状态；不生成新分支或新 commit 绕过未确认结果。
- 本期不新增通用动作回执框架。需要 durable 自动恢复和通知写回时，单独接入现有平台 action/followup 生命周期，不能让薄 CLI 封装承担第二套 Room 状态机。

## 9. 建议实施顺序

### 9.0 第一期 POC 与后续增强

POC 的目标是验证：**现有软件开发团队能通过 TeamX 的正常工具入口，在独立 Scion Workspace 中完成一次真实开发、Review 交接、独立评审、修正和复审。** 不要求同时完成所有平台能力、管理便利功能和故障场景。

#### POC 必须实现

| 范围 | 最小交付 | 完成依据 |
| --- | --- | --- |
| 实例与仓库 | 一个真实 demo 仓库、已存在的目标分支和基线提交；当前运行平台的固定 v0.1.13 CLI | 有效凭据下 `whoami`、仓库与 Hub 读取通过；具备任务分支发布及 Review 写入权限 |
| 一条认证路径 | 操作者在页面或命令行以原生 PAT 登录独立凭据文件，TeamX 管理 profile 引用并核验 subject | 工具明确使用该凭据与实例，不回退全局登录；身份不符时停止并要求重新验证 |
| 正常工具接入 | 最薄的 `teamx-scion`、固定版本检查、Catalog、Scion skill、成员显式绑定、当前 Workspace 中执行 | Agent 可通过 `tools list/skill/run` 使用 scn；保留原生输出/退出码，沿用取消和工具结清 |
| 最小 Workspace 解耦 | 共用管理通过仓库接口调 GitAdapter/ScionAdapter；区分 kind 与 provider | 共用主流程不判断 `.git/.scion`、不直接执行 Git/scn；既有 Git 路径的相关回归通过 |
| Scion Workspace | 三个 membership 的独立 clone、本地仓库身份检查、最小仓库类型/连接配置入口 | 工作副本不互相覆盖；配置提交与 Session 路径更新沿用已有机制，已有副本回访不重新 clone/切分支 |
| 开发与评审 | 调整角色中的 GitLab 专属前提；实现任务分支提交/发布、Review 创建/读取、准确版本检出、普通评审评论、原 Review 更新与复审 | 真实远端对象、完整 base/head、revision、评论、测试及 Room 交接相符 |
| 真实验收 | 用真实 DSH 成员完成上述闭环；最小页面配置路径实际可用 | 不是仅由操作者脚本完成；已实现的工具绑定/Workspace 页面做基本浏览器验收 |

POC 允许三个成员共用一个平台账号、一个实例和一个 demo 仓库，但每个 membership 仍独立绑定并拥有自己的工作副本。架构师保留在团队中；简单需求可不介入，不要求为了演示而让三个人都发言。复杂需求若在 POC 中选择验证设计交接，则记录为追加验收，不强制加入最小闭环。

POC 只实现所选的原生 PAT 登录路径，不同时建设 TeamX 保存 PAT 的第二套入口。当前已补齐操作者页面 PAT 登录、受管 profile 自动发现与账号选择，后台仍通过原生 CLI 保存同一类凭据，不是第二种 PAT 存储模式；不新增 device flow 授权流程。固定实例、完整检出、当前一种运行平台即可；实例与仓库参数仍来自配置，不硬编码进工具或角色 prompt。

Workspace 的接口职责和两种实现必须成立，但不要求同期把所有 Git/GitLab 辅助函数迁到最终目录、重构全部旧配置或为未来第三种后端提前扩展接口。已有 Git 专属入口可以保留在明确的适配边界内；需要修改的准备主流程先完成解耦。

#### POC 也不能省略的基本约束

- 凭据只走受管引用/进程，不进入角色 prompt、Room 消息、公共回执或仓库；连接账号与目标实例须明确核实。
- 目录归属、成员隔离、非空目标保护、仓库匹配必须保留；禁止为了 clone 自动清空已有文件。保留失败标记并清楚报错，失败由操作者检查后显式处理。
- 保留现有操作 ID、配置版本、取消与 Session 更新合同；不新增自动任务重放，不在重启时重新初始化工作副本。正常重启做一次基本回归，不要求首期覆盖所有中间状态的崩溃恢复。
- 评审基于完整版本，并在发布结果前核对 revision；修正必须更新原 Review。只做普通评论，不自动 approve、合入或关闭。
- 远端写入结果未知时停止重复写入、先只读核查并报告。这个处理规则是 POC 必需的；自动恢复和全部断流故障注入可后置。

#### 可后续实施的完善与增强

| 范围 | 可后置内容 |
| --- | --- |
| 登录体验 | TeamX 内启动 device flow、授权进度页面、账号快速切换、重新登录向导；device flow 另依赖实例配置 |
| 认证覆盖 | 第二种 PAT 存储模式、多账号/多 profile 管理体验、自动轮换与重新认证；已有连接变更的取消/结清规则仍须保留 |
| Workspace 工程整理 | 全量目录搬迁、历史配置结构统一、更丰富的错误修复向导、批量仓库配置；不影响首期两种适配器的职责隔离 |
| 恢复增强 | 各中间阶段的崩溃/断电恢复矩阵、专门的成员级恢复收据扩展、远端写入的持久自动对账；先复用已有收据，不另建状态机 |
| 安装与性能 | 多平台安装包覆盖、自动安装/受控升级、稀疏检出、daemon/缓存、并行 clone 和性能优化 |
| 评审增强 | 行内评论、线程解决、代码建议、正式 approve/request-changes、原生评审人邀请及多账号身份展示 |
| 合入与关闭 | 用户决定后的自动合入/关闭；强版本保证须先补齐 Scion API/CLI 的原子校验和明确排队结果 |
| 平台扩展 | Scion 通知、Inbox/followups 写回、Issue、CI runner/强制检查、MCP、Git gateway、多实例/独立 Hub 支持 |
| 验证扩展 | 多任务并发、跨平台、多账号轮换、大仓库、冲突处理及完整故障注入矩阵 |

POC 验收只要求“一次正确的真实闭环 + 新边界的必要回归”，不以完整能力矩阵全部通过为条件。设计文档中描述的恢复保障不能因暂未做全量故障注入就被删掉；也不能把尚未实现/验收的增强写成已具备。

#### POC 推荐落地顺序

1. 先做 S0 的 CLI/账号/仓库/Hub 实测，尽早确认目标实例可进行必要读写。
2. 完成 S1 与 S2 的最小工具、连接引用和成员绑定；暂不做登录便利功能。
3. 完成 S3a 的准备接口与 Git 回归，再做 S3b 的 Scion 初始化；保留已有 Room/Session 生命周期。
4. 完成 S4 的简单需求真实闭环，必须包含一次修正和同一 Review 的复审；单独记录剩余未验项。

后面的阶段表描述总体路线，其中 S0–S4 仅取以上 POC 必需内容；S5 和本节增强项目另行立项，不自动纳入第一期。

### 总体阶段

| 阶段 | 可单独评审的交付 | 验收标准与后续边界 |
| --- | --- | --- |
| S0：凭据与实例探测 | 当前实例先用有效 PAT 完成原生隔离登录；核实已指定的 `OpenCSG/teamx-demo`、目标分支与账号 | 固定发布包 `whoami/repo info` 和 Hub 读取成功；不把本次 REST Bearer 当作 CLI 通过证据；浏览器登录需另启用 device flow |
| S1：独立工具（已通过） | `tools/scion`、lock、安装说明、原生执行和错误保留 | 固定版本帮助、参数/stdin/输出/退出码、取消及环境隔离通过；独立工具无 TeamX 内部依赖，见 [Scion 验证与待验项](scion.zh.md#验证与待验项) |
| S2：连接与工具绑定（已实施） | Scion provider、profile 身份核验、运行环境注入、Catalog、skill、成员设置 | 每成员显式绑定；未登录、账号变化、连接轮换、取消/关房均正确结清；可无 clone 读取 Review。组件与真实只读/重启通过，实际浏览器操作待验，见 [Scion 验证与待验项](scion.zh.md#验证与待验项) |
| S3a：Workspace 解耦（已通过） | 共用仓库接口、GitAdapter、平台访问桥接；保留操作与 Session 生命周期 | 既有 Git/GitLab 行为回归通过，共用层不直接执行 Git 或读取 `.git`；此阶段不引入 Scion 初始化，见 [Scion 验证与待验项](scion.zh.md#验证与待验项) |
| S3b：原生 Scion Workspace（已实施） | ScionAdapter、repository.kind、操作恢复、页面类型/连接选择 | 组件、三份真实独立 clone、空分支/错误仓库/非空目录/失败重试、回访及重启恢复通过；复用 S3a 的生命周期合同。页面自动化通过，实际浏览器操作待验，见 [Scion 验证与待验项](scion.zh.md#验证与待验项) |
| S4：团队协作（已实施） | 通用三角色 prompt、团队 README、Scion 开发/评审流程 | 真实 DSH 在 demo 上创建 Review #1、独立检出测试、版本化评论、更新 revision 2 与复审，Room 交接一致；限流后分段续验、真实未知评论核查通过，实际浏览器操作待验，见 [Scion 验证与待验项](scion.zh.md#验证与待验项) |
| S5：用户决定后的合入 | 可选，需先明确/补齐 Scion 的版本锁定能力 | 版本改变拒绝、并发、排队、最终 landed、未知写入恢复均真实验证；不计入首期完成条件 |

S1–S4 为接入主线，第一期取第 9.0 节定义的最小范围；S0 的成功是目标实例验收前提。可逐阶段实施并记录各自证据，不要求一次完成所有扩展。

### 9.1 预计改动位置

| 位置 | 主要改动 |
| --- | --- |
| `tools/scion/`（新增） | 独立封装、固定 CLI 依赖、帮助和使用文档 |
| `internal/platform/scion/`（新增） | provider 规范化、CLI/profile 身份与仓库核验；不实现 GitLab 对象适配 |
| `internal/platform/tool/process.go`、`skills.go` 及新增 Scion skill | 注册与工具说明、profile 私有运行值、固定环境、进程生命周期 |
| `internal/room/platform.go`、`tools.go`、`platform_call_lifecycle.go` | provider 装配、按账号来源注入、调用前身份核对和连接结清 |
| `internal/platform/connection/*`（按需） | profile 引用版本、代际切换和非整数账号；保持现有 GitLab/Lark 合同 |
| `internal/workspace/`、`internal/workspace/git/`（S3a 已新增）、`internal/workspace/scion/`（S3b 已新增） | 仓库适配接口、Git/Scion 实现；不反向依赖 Room 或承担 MR/Review 业务 |
| `internal/room/workspace_config.go`、`workspace.go`、`workspace_connections.go`、`workspace_management.go`、`workspace_records.go` | 显式仓库类型、适配器装配、平台访问桥接与原操作恢复；共用管理不直接执行 Git/scn |
| `internal/web/assets/app.js`、`index.html` | Scion 连接、成员工具和 Workspace 类型选择；不强制新增登录授权页 |
| `.teamx/teams/software-development/` | 协议中立的角色职责和两种工具使用说明 |
| `docs/workspace.zh.md`、`platform-architecture.zh.md` | 记录实际接口、认证路径与验收边界 |

工作区目前还有其他未提交的连接/OAuth/UI 改动。实施前以当时源码为基线确认接口，只修改本阶段必要文件，不覆盖其他工作。

### 9.2 必须区分的验证层次

以下是完整接入的验收目录。POC 执行第 9.0 节的真实闭环、正常重启及必要边界回归；各阶段故障注入、多账号等覆盖按后续范围补齐，逐项标明是否实际执行。

组件验证覆盖版本锁定、profile 身份漂移、实例/仓库匹配、无凭据帮助、退出码、进程取消、独立目录和失败操作恢复；这些是编排合同验证，不能代替目标实例验收。

真实验收使用专用 demo 与新 Room，至少覆盖：

1. 原生登录后重启服务仍可读取；过期、登出或账号变化时清楚报告，不使用全局备用凭据。
2. 三个 membership 对应三份 `.scion`；一人改文件/切分支不改变他人工作副本。未启用 Scion 的成员不能通过 TeamX 正常入口调用。
3. developer 发布任务分支和创建 Review 后，远端 repo、作者、target、head、revision 与 Room 交接一致。
4. reviewer 在自己的 Workspace 核对完整 head 并独立测试；远端评论 ID、revision、正文、作者与公开结果一致。
5. developer 更新同一个 Review；revision 变化后旧结论不冒充最新通过，新版完成复审。
6. 一次受控 clone 失败/显式重试、正常服务重启和一次受控未知写入核查，确认不重复创建 Review/评论，也不破坏本地文件。
7. 真实 DSH 模型通过 `tools list → help/skill → run` 自主完成上述流程；实际浏览器验证连接、工具绑定、Workspace 状态和错误反馈。

若未来单独验收 S5，还要验证目标分支前进、head/revision 并发变化、gate 入队和最终合入状态。不可用“帮助显示命令”“源码有接口”“组件测试通过”或本次 REST 读取替代这些结果。

## 10. 依据与复核入口

远端已认证读取：[发布页面](https://scion.opencsg.com/OpenCSG/scion/releases/v0.1.13)、[发布详情 API](https://scion.opencsg.com/api/v1/repos/OpenCSG/scion/-/releases/v0.1.13)、[OpenCSG 仓库入口](https://scion.opencsg.com/OpenCSG/)。上述地址需要相应登录或认证；匿名 401 不用于判断能力。

Scion 本地复核时，以下路径均以 `v0.1.13` tag 为基线：

```sh
git -C /Users/russellluo/Projects/work/opencsg/projects/scion show v0.1.13:client/cli/src/login.rs
git -C /Users/russellluo/Projects/work/opencsg/projects/scion show v0.1.13:client/cli/src/cmd/review.rs
git -C /Users/russellluo/Projects/work/opencsg/projects/scion show v0.1.13:proto/scion/hub/v1/review.proto
```

其他关键证据：`client/cli/src/ctx.rs`（凭据、仓库与作者）、`creds.rs`/`daemon.rs`（隔离和后台行为）、`cmd/commit.rs`（发布与 rewritten）、`client/wc/src/state.rs`/`lib.rs`（本地状态和 clone）、`hub/hub/src/review.rs`（入队与合入）、`hub/hub/src/download.rs`（发布下载）、`common/object/src/id.rs`（原生 ID）。

TeamX 现状以当前工作区的 [工具执行](../internal/platform/tool/process.go)、[成员绑定](../internal/room/tools.go)、[连接装配](../internal/room/platform.go)、[连接验证](../internal/platform/registry/verification.go)、[Workspace 配置](../internal/room/workspace_config.go)、[Workspace 准备](../internal/room/workspace.go)、[团队说明](../.teamx/teams/software-development/README.md) 为依据。S1 独立工具、S2 连接/工具绑定、S3a Workspace 解耦、S3b 原生 Scion Workspace 及 S4 团队协作已按阶段交付；S5 尚未实施的阶段交付仍为提案。S3a 实际接口采用 `Check/Clone/Configure` 与独立纯配置校验，未要求按第 5.4 节拟议 DTO 名称全量改写历史结构，见 [Scion 验证与待验项](scion.zh.md#验证与待验项)。
