# 使用 Scion 开发与评审

在「软件开发」团队中使用 Scion，可以让 developer 开发并创建 Review、reviewer 独立测试和评论，再由 developer 修正并更新同一个 Review，交 reviewer 复审。复杂需求由 architect 先交付设计。GitLab 与 Scion 共用这套团队，根据 Workspace 和成员工具绑定选择对应流程。

## 首次配置

### 1. 准备程序并启动服务

已安装 Scion、配置好 DSH 和模型时，可直接进入下一步。首次安装按[快速开始](../README.md#快速开始)准备 TeamX，并安装固定 **scn 0.1.13**；下载与校验见 [Scion 工具手册](../tools/scion/README.zh.md#安装)，当前已核实的平台为 macOS arm64。

TeamX 支持填写原生 `scn` 或配套 `teamx-scion` 的路径。源码更新后重新构建并启动（先正常退出旧服务，等待执行结清）：

```sh
go build -o bin/teamx ./cmd/teamx
./bin/teamx serve --config .teamx/config.toml
```

需要薄封装时另运行 `go build -o bin/teamx-scion ./tools/scion`，并将 scn 放在它的同目录，或配置服务环境中的 `TEAMX_SCN_BIN`。服务不自动加载 `.env`。

### 2. 在页面配置 Scion 连接

打开「设置 → 平台连接 → 新增连接」，选择 **Scion**：

1. 填写连接名称和实例地址，例如 `https://scion.opencsg.com`。
2. 程序路径会自动检测；如需手工指定，在「高级设置」中填写服务机器上 `scn` 或 `teamx-scion` 的绝对路径。
3. 页面自动检测当前服务已有的登录：单个账号自动选中；多个账号由你选择。
4. 没有可用登录时点击「登录」，已有账号时点击「重新登录」，再输入该实例的 PAT 并点击输入区内的「登录」。登录成功后显示实际账号，再点击「保存连接」。

**不需要手工创建目录或填写 profile ID。** 后台调用 Scion CLI 登录并管理独立 profile，PAT 输入框提交后立即清空。登录响应丢失时只查询状态，不会自动重发 PAT。可以取消正在进行的登录。

连接保存后会再次验证账号。PAT 及账号须具备目标仓库所需的读取、任务分支发布和 Review 写入权限。

已登录时，可在账号旁点击「退出登录」。确认后会停止正在使用该账号的操作，清除当前 TeamX 受管账号的本地凭据，并使共享此账号的连接失效；连接配置和房间绑定保留。重新登录后需保存连接并重新验证。退出不影响其他独立登录，也不会撤销 Scion 平台上的 PAT；撤销令牌请通过「查看或创建 PAT」进入令牌管理页面。

### 3. 新建软件开发 Room

新建「软件开发」Room，先完成下面的工具和 Workspace 配置，再发送需求。新模板只影响新建 Room；已有 Room 保留旧角色快照。使用 `~/.teamx/teams/` 或自定义团队目录时，先将更新后的软件开发团队模板同步到实际加载目录。

### 4. 为成员启用工具

进入各成员详情的「平台 → 添加平台」，选择刚保存的连接，勾选「使用工具」并「添加并启用」；之后可在平台行直接开关工具。Scion 显示「无通知能力」。developer、reviewer 分别绑定；architect 需要提交设计时也须绑定。可以共用一个平台账号，也可以分别选择各自的已验证连接。

### 5. 初始化 Scion Workspace

打开输入框下方的 **Workspace**，选择 Scion 类型，填写仓库和分支，选择已验证连接并应用。例如：

| 字段 | 示例 |
| --- | --- |
| 仓库类型 | Scion |
| 仓库 URL | `https://scion.opencsg.com/OpenCSG/teamx-demo` |
| 初始分支 | `main`；留空则核实并使用仓库默认分支 |
| 连接 | 刚验证的 Scion 连接 |

等待初始化完成，每个成员会拥有独立的 `.scion` 工作副本。**Workspace 连接与成员工具绑定分别配置**，初始化不会自动启用工具。已有非空目录不会被清空覆盖；目录保护与恢复说明见 [Workspace](workspace.zh.md)。

## 日常使用

配置完成后，直接发送业务需求，例如：

> 帮我做个命令行 Todo 看板，能添加、查看和完成任务，关掉再打开数据还在。

无需在消息里列 Scion 命令或评审步骤。developer 完成开发、测试并交接 Review 链接和完整版本；reviewer 在自己的副本检出准确版本、独立测试并发表评论。你可以继续提出修改，成员会更新原 Review 并复审新 revision。简单需求不要求 architect 额外发言。

查看 Room 中的 `[待评审]`、`[待复审]` 和评审结果，或点击 Review 链接查看平台版本和评论。成员暂停时，在详情查看原因，处理后通过「启用协作」继续。

当前流程使用普通版本化评论，**不自动 approve、合入或关闭 Review**。Scion 的任务分支 `land` 是发布，`review land` 才是合入目标分支；评审通过不代表已经合入。成员使用原生 Scion 命令，不在 `.scion` 副本执行 Git 开发流程。

## 常见问题

| 现象 | 处理 |
| --- | --- |
| 程序找不到或版本不符，退出 127 | 检查服务端的封装、scn 路径和固定版本；路径配置改变后重启服务。成员 shell 中修改 PATH 不会传给服务端。 |
| 连接验证失败或 `credential_unavailable` | 点击「刷新账号」核对当前服务的登录，必要时使用 PAT 重新登录。命令行配置的诊断见工具手册。 |
| `identity_mismatch` | 实际账号与连接记录不符；在页面使用 PAT 重新登录并保存连接，重新验证后核对成员绑定。 |
| Workspace 初始化失败 | 查看页面错误和目标目录，保留现场；分支须存在且有提交。核实后按 Workspace 文档显式重试，不删除工作文件来清场。 |
| 模型 HTTP 429 | 等待模型服务额度恢复，再「启用协作」；不要反复发送同一任务。 |
| 创建、发布或评论结果未知 | 先核查远端分支、Review 或评论，再决定后续操作，不重复写入来探测结果。 |

角色职责见[团队 README](../.teamx/teams/software-development/README.md)，验证边界见下节。

## 验证与待验项

固定 CLI 的只读成员调用、三份原生 clone、失败后的显式重试和正常重启已有通过记录。真实 DSH 已完成开发、创建 Review、独立评审、修正、更新同一 Review revision 与复审，并在一次评论响应丢失后只读核查作者、正文、版本与唯一性。续验经过模型限流后的结清与正常恢复，不证明连续无干预协作可用。

实际连接、工具绑定与 Workspace 页面操作，多任务／多账号、完整故障矩阵、复杂设计交接和 Review 合入仍待验证；其他发布平台及非 Unix 进程树结清也未验收。组件或模拟页面通过不替代这些结果。

只读入口为 `TestScionRealMemberRead`、`TestScionRealWorkspace`（`internal/room`，`-tags=e2e`），设置 `TEAMX_SCION_REAL=1`、`TEAMX_SCION_TEST_CREDENTIALS`、`TEAMX_SCION_BIN`、`TEAMX_SCN_BIN`，路径均明确指向隔离凭据和实际程序；登录发现另受 `TEAMX_SCION_AUTH_REAL` 控制。新协作验收会真实发布任务分支、提交、Review 及评论，须先指定仓库与写入范围：

```sh
TEAMX_SCION_S4_REAL=1 TEAMX_SCION_S4_MAX_TOKENS=8192 \
TEAMX_SCION_CONFIG=/absolute/config.toml TEAMX_DSH_ENV_FILE=/absolute/private/model.env \
TEAMX_DSH_OUTPUT=/absolute/new-evidence-dir \
TEAMX_SCION_TEST_CREDENTIALS=/absolute/private/credentials.json \
TEAMX_SCION_BIN=/absolute/teamx-scion TEAMX_SCN_BIN=/absolute/scn \
go test -tags=e2e ./internal/room -run '^TestScionRealDSHCollaboration$' -count=1 -timeout=25m -v
```

失败后先检查原 Room、分支、pending、Review 和评论，不直接重跑新建流程。`TestScionRealDSHReviewContinuation` 只用于已经核实的既有 Review，`TEAMX_SCION_S4_SOURCE` 指向原证据根；从修正阶段续验时设置 `TEAMX_SCION_S4_START=correction` 及 `TEAMX_SCION_S4_PRIOR_EVIDENCE`。保留工作副本和原回执，通过正式恢复门禁继续，不重建 Review 或重放未知写入。
