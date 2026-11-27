# 平台程序依赖与连接身份分离方案

状态：阶段 1A、1B、2 的本地开发验收及阶段 3A 的 Linux amd64 产物验收已完成。阶段 3B 已按本轮用户指令部署同一候选归档，确认原通知及成员工具恢复、已检查的旧 Session 继续运行；原 onboarding 续跑由用户选择的“开启工具”操作替代，真实刷新及第三方写入未验。候选、环境限制及逐项证据见[平台程序绑定](implementation/platform-program-dependencies.zh.md#目标-linux-服务恢复3b)，不将这些未验项计作通过。

日期：2026-11-27。代码基线：`69828ce4260a`。实施前复核相关入口的最新代码；当前工作区已有其他任务改动，本方案不包含它们。

## 1. 目标与范围

平台连接负责回答“使用哪个账号和哪份凭据”，服务运行环境负责回答“使用哪个程序”。升级服务时，平台程序可以随受信任的服务版本更新，既有账号、通知订阅和成员权限不必随路径变化而重建。

本期统一程序选择、依赖可用性和调用期间的固定规则，覆盖 GitLab、飞书、Scion。各平台的 HTTP/CLI 协议、凭据存储、版本解析、权限和原生账号核验继续由原适配器负责。

交付后的行为：

- 托管服务升级后，飞书旧连接使用新服务绑定的 `lark-cli`，无需改写连接或重新授权。
- 通知、权限检查、成员工具和账号固定检查使用同一个平台程序绑定；界面检查与实际执行不再各读一个路径。
- GitLab 保持 HTTP 通知和内置动作；CLI 工具继续使用运行时程序。Scion 消除验证、工具和 Workspace 中直接执行历史路径的分支。
- 已受理调用保持自己的程序及凭据上下文，结束后按既有生命周期结清。
- 程序缺失、版本不兼容、私有凭据目录异常、账号不匹配有可区分的反馈。

不在本期范围：新的平台执行框架、动态插件系统、统一 OAuth/Keychain/PAT 存储、通知算法重写、自动安装 CLI、凭据格式迁移、跨机器恢复，以及开放 Linux 多用户 Scion/仓库执行。

## 2. 现状与故障依据

### 2.1 本次 Linux 现场

本轮已读取 Linux 服务的接入记录、通知状态、程序和目录元数据。记录中的结果如下，后续实施验收须重新采集，不能把此处当作修复后的证据。

| 项目 | 已确认结果 |
| --- | --- |
| 飞书接入 | 账号 `connected`，通知配置 `applied`，工具 `failed / apply_interrupted` |
| 已保存 CLI | 指向 `dev-2a5cb5ad8ffe` release |
| 当前服务 | 运行 `dev-69828ce4260a` release |
| 旧程序及目录 | 旧 CLI 仍存在且可执行；profile 与连接一致，私有目录权限正常 |
| 飞书采集 | 同一 Agent 的各查询分支报 `invalid_cli_storage`，尚无成功拉取 |
| GitLab 采集 | 同一 Agent 的 Todos/Events 仍在运行，检查时连续失败数为 0 |

故障链路：

```text
升级后的 Space 装配选择当前服务旁的 lark-cli
  → WithStorage(root, currentExecutable)
  → 读取仍指向旧 release 的连接及 profile.json
  → cliStorage.bind 要求 saved.Executable == storage.executable
  → invalid_cli_storage，在权限查询前被拒绝
  → onboardingFailure 丢失具体类别，页面只显示“工具：未完成”
```

“通知：已保存”仅证明订阅配置提交，不证明远端采集成功。此次问题也不能据此归因于用户缺少飞书 scopes。

### 2.2 各平台已有策略

| 能力 | 当前路径来源与主要入口 | 问题/保留部分 |
| --- | --- | --- |
| GitLab 通知、内置动作 | `platform/gitlab` HTTP 适配器，`room/platform_notification_gitlab.go` 等 | 不依赖 CLI，不引入 CLI 前置条件 |
| GitLab CLI 工具 | `Catalog.Tools` → `tool.ResolveExecutable` → `teamx-gitlab` → `glab` | 已基本分离；需保证可用性检查和 wrapper 使用同一依赖路径 |
| 飞书 | `space_runtime_serve.go` 注入程序，`lark/storage.go` 又核对历史程序路径，`lark/client.go` 按 profile 路径执行 | 校验与执行均需调整，不能只删除一次相等判断 |
| Scion | profile 保存程序，`VerifyProfile/Prepare`、`tools.go`、`workspace_scion.go` 直接使用 | 旧程序存在时继续用旧版；旧目录删除后失败，属于类似依赖问题 |

### 2.3 必须尊重的代码约束

1. [registry/verification.go](../internal/platform/registry/verification.go) 的 `credentialEvidence` 对整个 `CLIProfile` 求摘要。改写 `Executable` 会改变凭据指纹，影响账号/订阅证据。因此本期不改写既有 profile，也不改摘要算法。
2. [platform/boundaries_test.go](../internal/platform/boundaries_test.go) 禁止飞书/GitLab 适配器依赖 `platform/tool`，独立 `tools/gitlab`、`tools/scion` 也不能 import TeamX 内部包。共同解析放在外层，向适配器注入结果。
3. [Catalog.Tools](../internal/app/platforms/catalog.go) 已支持由外层解析程序，静态 Definition 构建不能执行文件检查或版本查询。
4. [space_runtime.go](../internal/room/space_runtime.go) 和部署机制保留旧 Space 的执行配置、DSH 及 Agent CLI。此次调整只涉及控制面接受的平台调用，不切换历史 Session 的后端或 Agent CLI。
5. 飞书私有 storage、临时目录、请求队列和账号固定已有实现；Scion 有凭据快照、subject/endpoint 检查和进程跟踪。程序绑定不接管这些状态。

## 3. 设计决策

### 3.1 统一的三个职责

| 对象 | 保存/拥有的内容 | 生命周期 |
| --- | --- | --- |
| 连接与凭据引用 | 平台 ID、账号、generation、credential version、profile/授权引用 | 持久化，维持现有格式 |
| 程序绑定 | 程序逻辑名、已解析绝对路径、选择来源 | 服务运行环境内的只读值，不写入业务快照 |
| 平台调用 | 本次程序绑定、已准入账号/凭据、取消和结清凭据 | 一次调用，直到进程和结果处理结清 |

现存 `CLIProfile.Executable` 暂保留为历史配置/兼容元数据。在服务管理程序的模式下，它不再选择实际执行文件；`Profile/Brand/CLIVersion` 及完整持久记录仍参与原有结构与身份检查。

不引入第二个连接管理器、持久程序注册表、热切换状态机或通用版本探测引擎。

### 3.2 程序选择的权威与兼容模式

只保留两种程序来源，由可信服务装配确定，不从 Agent 参数或旧路径字符串推导。

| 来源 | 适用范围与选择规则 |
| --- | --- |
| 服务提供 | 已安装服务，包括个人模式服务，以及已由服务绑定私有 storage 的 Linux 多用户飞书。使用服务选定程序，管理员显式覆盖优先；历史 profile 路径不决定执行文件 |
| 原有外部程序 | 独立个人/源码运行中的飞书、Scion profile，沿用各自保存的程序路径；无 profile 的默认工具沿用服务同目录/PATH 规则 |

服务装配复用 `serve.go` 已验证的 layout/release、现有服务环境及飞书 `WithStorage` 注入，只传递所需的内存程序来源上下文。不要增加用户可配置的策略矩阵，也不要仅用 `hostTools.restricted` 判断所有场景。

现有 `CLIProfile` 没有记录路径是自动发现还是手工选择，因此本期不识别、不猜测这个历史来源。规则按运行场景统一确定：已安装个人服务也采用服务提供的程序，这是本方案明确的行为调整；需自定义服务程序时使用管理员覆盖。独立个人运行继续保留不同 profile 的不同外部程序，不把它们合并成 provider 全局路径。

托管模式中的旧网页可能回传隐藏的历史路径：新 UI 的发现/新建请求省略这个执行选择；服务端在该模式只接受空值或与当前绑定相同的执行选择。已有完整 `cli_profile` 的读、核验和授权请求仍按原持久记录核对，不能因其中有历史路径而要求重新建连接。

### 3.3 解析与执行合同

复用并收敛 [tool/executable.go](../internal/platform/tool/executable.go) 的逻辑：

1. 以服务捕获的环境为输入，不在执行阶段读取 Agent 环境或工作目录。显式覆盖必须是规范化绝对路径，失败时不换用其他程序。
2. 已安装服务默认绑定其物理 release 目录，解析时展开 `current` 等受信任安装链接；调用不持有可随升级改变的 `current/bin/...` 路径。
3. 完整安装包声明的同目录依赖缺失/损坏时报安装错误。独立运行才允许原有的服务 PATH 发现；忽略相对/空 PATH 项，不从 cwd/仓库/临时目录寻找程序。
4. wrapper 的原生依赖以已选 wrapper 所在目录为基准，再应用管理员覆盖。冻结主程序和原生依赖两层路径，避免检查 `glab A` 却执行 `glab B`。
5. 已有 wrapper 保留独立运行能力。服务调用时用现有 `TEAMX_GLAB_BIN`、`TEAMX_SCN_BIN` 注入已选原生路径；wrapper 不引入内部包依赖，显式覆盖失败不回退。
6. 列表检查只做有界本地可用性判断，不运行 CLI、不触发网络/OAuth。具体版本核验沿用飞书、GitLab wrapper、Scion 的现有解析和超时，放在锁外。
7. 一次调用取得绑定后，其核验、准备和实际执行使用同一组路径；不在重试、子步骤或请求结果不确定时重新选择程序。

程序绑定不是“永久可用”的缓存：文件执行权限、私有目录和调用准入仍按原入口检查；失败不缓存成永久状态。程序修复或服务重启后可重新检查，不改成员授权。程序版本/凭据格式不兼容须明确拒绝，不能借本次分离绕过。

### 3.4 一处选择，外层注入

复用现有 `Catalog.Tools` 解析回调、`tool.ResolveExecutable`、工具描述和服务环境。在外层增加一个小型 `programFor(provider, originalProfile)` 类选择入口，接收可信程序来源上下文，返回本次主程序及必要的原生依赖路径。名称和返回形式按代码风格确定，不为这几个路径建立新的公共框架。

```text
可信安装目录 + 已有服务环境 + 原 profile
  → 一处程序选择
  → 已有工具描述 / adapter 参数 / wrapper 依赖环境
  → 当前调用使用同一结果直至结清
```

| 消费位置 | 使用选择结果的方式 |
| --- | --- |
| 工具主程序 | 投影到现有 `tool.Definition.Executable` |
| wrapper 原生依赖 | 在现有 `ProcessEnvironment` 中替换 `TEAMX_GLAB_BIN` / `TEAMX_SCN_BIN`，预检和执行都读这份值 |
| 飞书 | 向 factory 注入执行路径，原始 profile 保持不变 |
| Scion 核验/准备/Workspace | 显式传入所选执行路径和必要依赖环境，凭据仍引用原 profile |
| 可用性判断 | 使用同一选择入口及其派生工具描述；内置飞书直接检查有效 CLI，通知不要求先开启成员工具 |

工具描述、adapter 参数和依赖环境都是同一选择结果的投影，没有独立更新权威。不新增 provider→程序注册表、Catalog 程序枚举层、依赖结果缓存或 `ExecutableDependency` 的第二套路径字段。GitLab 已有依赖声明继续复用；Scion 原生依赖可按已有 `Dependencies` 机制补充，但只用于明确的 wrapper 组合，不能妨碍直接选择原生 `scn`。

飞书新增只绑定程序的 `ClientFactory.WithExecutable`，`WithStorage(root, executable)` 同时设置该执行字段；无注入时沿用 profile 程序。托管个人服务可更换执行程序并继续使用原 Keychain，凭据后端不因此切换。飞书不 import `platform/tool`。

Scion 的 `VerifyProfile/VerifyFile/VerifyWithRun/Prepare` 显式接收执行路径；有自定义 runner 的入口同时携带固定的原生依赖环境。`Discover/Login` 已有 executable 参数，直接使用外层结果。避免复制 profile 并覆盖其路径，也不新增有状态 client/factory。

一次调用用局部值、现有工具描述或闭包保留所选路径即可。取消、等待、进程树清理和结果不确定性继续由现有 `platformCall/Process` 等 owner 处理；不新增程序 generation、租约、引用计数或升级结清状态机。

版本常量继续以各 adapter/现有锁文件为准，公共规格不再维护一份重复版本号。公共层负责程序选择和依赖故障分类，适配器负责证明所选程序符合自己的协议版本。

## 4. 平台接入细节

### 4.1 飞书：连接数据不变，执行程序独立绑定

修改 [lark/client.go](../internal/platform/lark/client.go)、[clients.go](../internal/platform/lark/clients.go)、[storage.go](../internal/platform/lark/storage.go)，并接通 [auth.go](../internal/platform/lark/auth.go) 的发现入口：

1. `Client` 区分原始 `profile` 与本次有效执行程序；`command` 只使用后者。
2. 托管 storage 的 `bind` 仍要求磁盘 profile 与连接 profile 完全相同；保留路径结构校验、目录私有性、禁止符号链接、Space/profile 隔离。仅移除“历史 executable 必须等于当前服务 executable”的条件。
3. 新建 profile 的 `provision` 仍只能使用本服务允许的程序，不因兼容旧记录而允许请求方指定任意程序。旧 metadata 的允许读取与新 profile 的创建准入分开。
4. `WithTemporaryRoot`、`WithStorage` 及新增配置方法组合时必须完整传播执行字段、storage、队列和 gates 的原有语义，不新增独立执行队列。
5. `Discover/ConfigureApplication/Login/Logout/Verify/CheckPermissions/Pin`、消息/文档/图片读取和动作都走同一 `Client.command`。发现旧 profile 时也可使用当前程序，不能因旧文件不存在而拒绝。
6. `Pinned` 继续固定原账号和私有 data 目录，只创建既有临时配置；它继承本次程序绑定，不复制 token/DPoP/主密钥，也不重选程序。

当前非私有 storage 的 `ClientFactory.Discover` 调用包级 `New`，会绕过 factory；这里必须改为承接注入程序的构造路径。私有 storage 的发现还直接创建临时 `Client`，也必须设置同一执行字段。新增字段不能只接通普通 `f.New(profile)`，否则发现/首次接入仍可能选错程序。

新版本服务若只升级安装路径、CLI 协议版本仍兼容，就可读取旧连接。若将来 `CLIVersion`、CLI 存储或认证协议发生不兼容变化，保留现有拒绝及独立迁移边界，不能宣称任意 CLI 大版本都无需迁移。

同步 [tool_entries.go](../internal/room/tool_entries.go)：内置飞书工具检查上述有效 CLI，不再检查历史 `account.CLIProfile.Executable`，也不因为 `InternalOnly` 工具没有外部主程序而误报缺失。无需为此把飞书变成外部工具，或新增通知对工具目录的依赖。

个人外部 CLI 模式继续按用户已选择的程序执行，保持 macOS Keychain 和原 profile 语义；对托管程序的自动更新不扩展为自动接管个人本地登录。

### 4.2 GitLab：复用现有结构，保证两层依赖一致

- `gitlab/http.go`、通知查询和内置动作保持 HTTP 路径；CLI 不可用不阻断这些能力。
- `Catalog.Tools` 的 `teamx-gitlab` 与 `glab` 依赖都从公共绑定取得，`Process` 用已过滤的服务环境注入确定的 `TEAMX_GLAB_BIN`。
- 合并环境时替换指定依赖键，不追加重复键；维持其他环境白名单，不转发完整服务环境。
- `tools/gitlab` 的默认独立发现能力保留；只在覆盖/同目录失效行为与公共规则不一致处做小调整及对应测试。
- 现有 OAuth 账号授权、token lease、刷新、项目限定和写入结清不随程序升级改变。

### 4.3 Scion：一次覆盖完整执行链

| 入口 | 改法 |
| --- | --- |
| `platform_scion_auth.go` 的 discover/login | 服务模式使用外层绑定；独立个人模式保留明确的外部程序选择；新 profile 保存逻辑不重写旧连接 |
| `platform_assembly.go:verifyScionProfile` | 原 profile 用于凭据引用，所选程序独立传入 `VerifyProfile/VerifyFile` |
| `scion/profile.go:Prepare/VerifyWithRun` | 账号核验与真正执行使用同一程序；不从 profile 再取一个执行路径 |
| `tools.go` 的 help/普通调用 | 移除直接覆盖为旧 `CLIProfile.Executable` 的逻辑；help 不读取凭据 |
| `workspace_scion.go` | `whoami`、版本核验、repo/branch/clone 使用本次主程序、必要依赖环境及同一份凭据快照 |
| `tool_entries.go` | 可用性使用相同的主程序和 `scn` 依赖 |
| `tools/scion` | 接收固定的 `TEAMX_SCN_BIN`；保留独立运行、隔离环境、原生输出和版本检查 |

个人显式选择原生 `scn` 的既有路径继续支持：此时主程序就是 `scn`，不强制要求再安装 `teamx-scion`，也不重复声明一层相同原生依赖。托管安装以发行包声明的 wrapper/native 组合为准，不能在 wrapper 损坏后悄悄换成 PATH 中的原生程序。

继续保留 subject、origin/endpoint、仓库 canonical identity、分支和参数检查，取消与 process settlement 也沿用原 owner。Linux 多用户目录和准入仍不包含 Scion；此次修复不能成为开启它的隐式开关。

## 5. 持久化、升级与恢复

### 5.1 不做业务数据迁移

不为程序路径变化改写 `platform-connections.json`、私有 `profile.json`、credential fingerprint、platform generation、成员绑定、订阅 ID、baseline/cursor、动作回执或 Session 描述。新建连接继续使用现有格式；本期不删除 `CLIProfile.Executable` 字段。正常核验、授权或原工具步骤的提交仍可按既有规则更新相关版本和状态，这不属于路径迁移。

兼容旧数据不等于忽略 profile 不一致：若连接与私有 profile 已被单边改写，仍应拒绝。程序缺失和 storage 损坏是两类错误，不能互相掩盖。

### 5.2 服务与 Session 的两个程序生命周期

```text
控制面版本 A：平台调用 A → 固定平台程序 A → 结清
正常停服/升级：停止新准入 → 结清原调用 → 切换服务
控制面版本 B：读原连接 → 平台调用 B → 固定平台程序 B

旧 Space/Session：继续使用其原 DSH、Agent CLI、执行描述和恢复规则
```

“新程序”指接收本次平台调用的控制面所绑定的 release，不是全局追随 `current`，也不是把旧 Space 中所有 CLI 改到最新版本。旧 Agent CLI 经现有服务协议调用平台工具时，服务端使用自己的平台程序；协议兼容性仍需验证。

本期不做进程内热更新。服务覆盖配置变化需要原有重启/重装配流程；不能在一半准备、一半执行之间重新绑定。目录清理继续保留旧配置/Session 引用的 release，不调整 `serviceRequiredReleases` 来强行压缩留存数量。

### 5.3 本次故障的恢复路径

1. 部署修复后原样加载飞书连接，先执行只读程序/账号/权限检查，确认当前程序可读取原私有凭据。
2. 原通知按原订阅继续调度，等待现有退避到期，或使用已有核验恢复入口；不清 cursor、不移动 baseline、不新建订阅。依赖恢复不承诺立刻拉取。
3. 读取原 onboarding operation，再继续未完成工具步骤。已保存通知保留，不能重新执行完整接入或触发新 OAuth。
4. 继续之前按原逻辑复核成员、平台、generation 和绑定版本。若已发生设置冲突，应报告冲突；若是 `unknown/submission_unconfirmed`，保持原结果核对规则，不自动重放。
5. 真实账号检查发现缺 scopes、token 失效或账号变化时，按原授权流程单独处理；路径修复不等于授权已经充分。

本期不增加启动后自动 apply onboarding，也不自动重试第三方写入。

## 6. 错误反馈与界面

沿用既有状态/投影和页面布局，补齐错误原因，不再建诊断中心或安装向导。

| 稳定类别 | 含义与操作 |
| --- | --- |
| `cli_unavailable` / 现有 `executable_unavailable` | 所选程序或依赖不存在/不可执行；管理员检查安装 |
| `cli_version_mismatch` | 所选程序协议版本不符；管理员安装兼容版本 |
| `invalid_cli_storage` | Space 私有 profile/目录不一致或不安全；保留数据并检查 |
| `identity_mismatch` | 原生账号与连接证据不同；停止调用，按账号变化处理 |
| 原有 `settings_changed/target_changed/user_authorization_required` | 继续使用原业务语义 |
| 超时/取消/未知 | 保留对应类别与原结果确定性，不伪装成安装错误 |

后台日志可记录 provider、逻辑程序名、绑定来源、operation ID、失败阶段和分类；完整程序路径仅限管理员诊断，不向普通用户暴露服务目录。禁止打印 token、密钥、OAuth 链接、原始 stderr 和业务正文。

`onboardingFailure` 在保留原身份/版本错误优先级的前提下，显式保留白名单中的 adapter 错误。旧的 `apply_interrupted` 仍能读取，不根据旧错误码反推原因。只有本次重新检查获得的结果才能更新后续操作反馈。

`onboarding.mjs` 的全局接入结果和成员平台行共用同一错误文案映射；保存配置与实际采集健康分别呈现。普通用户可看到“通知配置已保存；工具暂不可用，请管理员检查服务程序”，采集异常复用通知已有错误状态，不能把“已保存”改成未经验证的“已正常接收”。

托管模式隐藏/禁用执行路径输入仍保留，但发现请求不再提交隐藏的旧路径。若需要展示当前运行程序，只读运行诊断与持久 profile 分开，不能拿运行路径回填并保存连接。

## 7. 分阶段实施

阶段按完整可用行为划分，每阶段包含入口、可用性、诊断、对应测试和使用文档。测试和长期代码按行为命名，不使用临时阶段编号。阶段 3A、3B 是交付验收关口，各平台必要的行为验证随其实施阶段完成。

本次推荐顺序为：**阶段 1、2 本地实施并验收 → 阶段 3A 本地 Linux 产物验收 → 部署服务 → 阶段 3B 线上恢复验收**。开发及部署前验收不依赖持续连接线上服务，也不将线上账号、凭据或业务目录用作开发夹具。各阶段按验收证据分别报告，不把“本地通过”写成“线上已恢复”。

### 阶段 1：托管飞书升级闭环及 GitLab 一致性

范围：一处程序选择与必要依赖投影、托管飞书所有入口与旧数据兼容、GitLab 两层程序路径一致性、接入/通知错误反馈。独立个人运行的外部 profile 继续原选择规则。

主要改动：`platform/tool/executable.go` 与必要的工具环境投影，`room/serve.go`、`service_config.go`、`space_runtime_serve.go`、`tools.go`、`tool_entries.go`，飞书 client/factory/storage/auth，以及 onboarding 和必要前端入口。`Catalog.Tools` 保留原回调形态；仅实际需要时小改工具定义/环境传递，不先扩展 Catalog API 或改造通用 `Process` 生命周期。接通后删除被替代的路径解析分支。

内部按以下两个批次推进，可分别提交和验证；两者都通过才算阶段 1 完成：

| 批次 | 范围 | 验收出口 |
| --- | --- | --- |
| 1A：公共入口与飞书闭环 | 公共选择入口随飞书落地，接通 factory、Discover、storage、权限检查、通知、工具可用性及错误反馈 | 在本地夹具中以 A 程序创建连接、B 程序打开原数据；删除 A 程序后仍能核验、采集并继续原 operation 的工具步骤；跨 Space、profile 篡改、目录异常和原身份/权限 gate 继续拒绝 |
| 1B：GitLab 两层程序一致性 | 接通 `teamx-gitlab/glab` 的选择结果和依赖环境 | 预检与执行使用同一原生程序，覆盖/缺失/版本不兼容反馈准确；HTTP 通知与内置动作不受 CLI 故障影响 |

1A 包含 Linux 私有 storage 所需的定向原生检查，使用隔离目录和合成凭据，不推迟到 3A 首次验证。修改公共解析或可用性入口时，1A、1B 都回归既有 GitLab/Scion 行为；Scion 在阶段 2 接入前保持原链路，不提前套用半套新规则，不为阶段过渡新增功能开关。

本阶段出口在本地夹具和隔离环境验证，通过后进入阶段 2；Scion 统一和线上恢复分别由后续阶段验收。

### 阶段 2：Scion 完整接入

范围：第 4.3 节所有入口，托管个人服务随版本更新，独立个人外部程序模式保持显式选择。阶段 1 的公共绑定直接复用，不新增 Scion 专用解析器。

出口：旧 profile 的版本核验、subject 核验、help、成员工具和仓库准备全部使用同一绑定；原生 `scn` 依赖覆盖与执行一致；凭据快照和目标限制保持。多用户 Scion 仍被拒绝。

可按入口逐步编码，但核验、准备、工具和 Workspace 的一致性作为一个完整出口验收；同时完成对应测试、使用文档及飞书/GitLab 的受影响回归。

### 阶段 3：安装产物与组合验收

**3A：部署前，在本地 Linux 环境验收。** 使用与目标服务相同架构的 Linux 容器或虚拟机，以非 root 用户、真实发行包和独立 home 验证程序运行、私有目录权限、CLI 存储、A→B 升级及原 operation 恢复。平台协议和通知通过受控夹具验证，CLI 私有存储使用合成凭据；不依赖线上 OAuth 或复制线上凭据。macOS 原生测试不能替代这一层。若容器依靠指令集模拟或不能运行 systemd，分别记录环境限制，宿主 systemd 和实际服务升级留给 3B，不把未执行项标为通过。

3A 通过且部署前必测项无未解决失败后，才将候选包部署到目标服务。缺少 Linux 验收环境时，应明确补齐环境或调整验收安排，不能跳过这一层并宣称全部部署前验收通过。

3A 以最终发行包、非 root 装配和升级组合为重点，复用阶段 1、2 的行为验证结果；不重复覆盖所有协议用例。通过后记录候选包的源码 commit、目标架构和归档 SHA-256，部署及 3B 使用这同一个包。候选代码或包发生变化时，重跑受影响检查并重新确认 3A 出口，不把另一份重建包直接视为已验收产物。

**3B：部署后，在目标 Linux 服务做有界恢复验收。** 核对原账号、连接、Space/Room/成员和 Session；确认当前程序可读取既有飞书私有凭据，权限检查返回准确结果，原通知订阅恢复成功查询，原 onboarding 可继续尚未完成的工具设置。核对 GitLab 采集及所需只读工具调用，保留原订阅身份、起点、已处理进度和权限选择。Scion 在目标部署仍禁用时，只核对准入继续拒绝，不为验收临时开放。

3B 先只读检查，再核验原工具设置的恢复；不要求为了证明本次路径修复发送飞书消息或发布 GitLab 评论。真实 OAuth/refresh/DPoP 检查按此次变更涉及的链路安排，正常凭据不强制退出或重新授权；没有发生的真实刷新不能报告成已验证。复用原部署脚本、backup/retention 和服务结清，不写一套新升级工具。

平台架构、Linux 部署及对应平台使用文档随阶段 1、2 更新，明确“平台服务程序”和“旧 Session 执行程序”的区别；阶段 3 补充实际产物和恢复证据。实际发送/评论等写入只在实施时具有明确验收目标和授权的情况下执行；无授权时保留未验项，不能用模拟通过代替。

## 8. 验证设计

### 8.1 最小有意义的回归矩阵

| 层次 | 必须证明的行为 | 建议承接位置 |
| --- | --- | --- |
| 解析单测 | 管理员覆盖、安装同目录、绝对 PATH、损坏覆盖不回退、绑定到物理路径 | `tool/executable_test.go` |
| 外层装配 | Catalog 保持纯描述；飞书执行路径由外层注入，未启用成员工具也能采集通知 | 既有 Catalog 测试及少量 Space/Room 装配测试 |
| 两层程序集成 | UI 可用性/预检和 wrapper 实际调用同一路径；依赖故障不选择其他版本 | `tool/dependencies_test.go`、独立 wrapper 测试 |
| 飞书升级 | A 创建 profile，B 打开原 storage；A 不存在时仍能 Discover/Verify/CheckPermissions/Pin；非私有 storage 的 Discover 也承接注入程序 | 扩展既有 storage 生命周期及 auth 夹具 |
| 飞书安全与身份 | profile 不一致、跨 Space、data 缺失/符号链接拒绝；账号变化仍拒绝 | 复用既有 storage/pin 断言，补升级组合 |
| 飞书上层闭环 | 通知已保存、工具未完成的原 operation 可继续；订阅 ID/baseline/游标不重建；unknown 不重放 | 少量 `room` integration |
| GitLab | CLI 缺失时 HTTP 通知/内置动作仍可用；CLI 两层路径随服务更新 | 既有 capability/工具集成 |
| Scion | 同一主程序和必要依赖环境覆盖 Verify/Prepare/Run/Workspace；旧路径消失不影响托管连接；外部模式保持各 profile 的程序，直接原生 CLI 不要求 wrapper | `scion` 与少量 Room/Workspace integration |
| 生命周期 | 已受理调用结清前不换程序；取消不等于结清；新实例使用新绑定 | 既有 call/process owner 测试扩展 |
| 前端 | 展示具体错误，配置保存不伪装采集成功，托管发现不携带隐藏旧路径 | 对应 onboarding、member-platform、connect 测试 |

只读夹具、真实 Linux CLI 合成凭据测试、真实账号 OAuth/refresh/DPoP、真实第三方动作分别报告结果，不相互代替。升级前后比对业务 ID、generation、credential version、fingerprint 和订阅起点；原生 CLI 合法刷新造成的 token 文件变化单独记录，不把整个 data 字节不变作为通过条件。

### 8.2 检查顺序

1. 先跑受影响包的普通单测、导入边界测试和前端定向测试。
2. 跑相关 `integration` 组合；并发部分定向 `race`，不为路径值解析反复跑全部真实模型验收。
3. 对上述包运行 `go vet`，编译相关 `e2e` 入口；采用仓库当前 Makefile 的 `TEST_PACKAGES/TEST_RUN` 限定范围。
4. 完成阶段 3A：在本地 Linux 的隔离 home 中使用真实安装产物验证 A→B 升级，确认部署前必测项通过，记录候选 commit、架构和包摘要。完整打包 smoke 不能代替平台业务验收。
5. 部署同一候选包后完成阶段 3B：核对包摘要和运行版本，连接目标 Linux 服务，先做只读检查，再验证原通知及未完成工具设置恢复；不在开发阶段提前修改线上数据来代替本地验证。
6. 回退验证只覆盖相同 CLI 协议/凭据格式兼容的 release。发现 CLI 存储格式变化时停在独立迁移评估，不承诺任意新旧版本双向兼容。

每层只保留其独有断言：下层验证解析、存储和原生协议，上层验证实际装配与持久恢复。不要为每种远端功能复制整套升级测试。

### 8.3 验收结果的报告边界

| 结果 | 可以得出的结论 | 仍不能得出的结论 |
| --- | --- | --- |
| 本地开发验收通过 | 解析、装配、平台行为、界面及恢复逻辑的受控回归通过 | Linux 原生程序和线上凭据可用 |
| 本地 Linux 产物验收通过（3A） | 候选包在所记录 Linux 环境中完成程序、存储和隔离升级验证，可进入部署 | 目标服务的旧 Space、真实通知或 OAuth 已恢复 |
| 部署后恢复验收通过（3B） | 所列目标连接和原通知/工具设置已恢复，已检查的旧 Session 保持可用 | 未执行的真实刷新、外部写入或其他平台功能也全部通过 |

整体交付时同时列出通过项、环境限制及未验项。可以先完成全部本地开发和部署前验收，再进入部署；本次线上故障的关闭仍以目标服务恢复证据为准。

## 9. 改动面、风险与完成条件

这是一项中等范围的运行时装配调整。核心工作集中于公共解析/绑定、飞书执行字段分离、Scion 多入口收敛和错误反馈；不涉及重写平台业务。

估算：阶段 1 约 2–3 人日，阶段 2 约 1–2 人日，产物与真实环境验收约 1–2 人日，总体约 4–7 人日。它是熟悉代码前提下的投入量级，不是工期承诺；真实 OAuth 配合、凭据格式兼容和部署故障不包含在固定耗时保证中。

主要风险与处理：

- **多份程序结果各自成为权威**：工具描述、adapter 参数及 wrapper 环境只从同一结果派生；不建注册表、缓存或新状态 owner。
- **从历史路径猜测运行策略**：来源由可信服务装配决定，按第 3.2 节的两种规则处理，不识别旧路径的自动/手工来源。
- **改 profile 导致身份连锁变化**：本期保持持久记录及 fingerprint 算法，运行值独立。
- **只修飞书 bind、漏掉实际执行/可用性/发现**：阶段 1 必须覆盖全入口与 UI 隐藏旧路径，删除旧 CLI 的测试必过。
- **只修 Scion 普通工具、遗漏核验和 Workspace**：以第 4.3 节作为调用点清单，核验与业务进程必须同一绑定。
- **公共抽象破坏模块边界**：共同解析在外层，飞书只接收路径；独立 wrapper 不 import 内部包。
- **把控制面更新扩大到 Session 迁移**：保留原执行描述、DSH、Agent CLI、恢复和留存规则。
- **路径修复后暴露真实权限/网络错误**：准确呈现新检查结果，不能自动补授权或声称已完成。

实施的复杂度边界是：只在装配和 adapter 调用处增加必要的运行值，接通后移除重复路径读取。通知 Actor/算法、授权模型、持久 schema、动作账本和 Session 恢复协议不因程序选择而增加分支。若实现需要修改这些核心机制，应先检查是否越过本方案边界。

全部完成应同时满足：三个平台遵循同一程序选择合同；托管旧连接无需改数据即可升级；通知与工具的可用性和实际程序一致；原身份/权限/Space 隔离与结清规则保持；真实验收和未验项有明确记录。阶段完成、完整方案完成、线上恢复成功分别报告。
