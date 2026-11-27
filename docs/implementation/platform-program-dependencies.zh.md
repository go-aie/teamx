# 平台程序绑定、飞书升级与 GitLab／Scion CLI 一致性

日期：2026-11-27。范围：[方案](../PLATFORM-PROGRAM-DEPENDENCIES-PLAN.zh.md)阶段 1A、1B、2 的本地开发验收、阶段 3A 的本地 Linux amd64 产物验收，以及阶段 3B 的目标服务升级和有界恢复检查。3A 使用 Rosetta 模拟环境；3B 按本轮用户指令部署同一候选，通知和成员工具恢复已确认。原 onboarding 续跑、真实刷新及第三方写入未计作通过，具体边界见[目标服务恢复](#目标-linux-服务恢复3b)。

## 行为与实现

- `tool.ResolveExecutableIn` 接收可信安装目录与服务环境：管理员规范化绝对覆盖优先，安装依赖不回退；独立发现只搜索物理服务同目录及绝对 PATH 项。程序选择不运行 CLI，符号链接展开到物理路径。
- `room.platformProgram/configureLarkRuntime` 使用控制面 release，已安装个人服务和多用户服务都绑定程序；独立个人 profile 保留外部路径。旧 Space／Session 的 DSH、Agent CLI 和执行描述保持原合同。
- `ClientFactory.WithExecutable`、`WithStorage`、`WithTemporaryRoot` 保留原队列和 gate。client 独立保存有效 executable，所有执行通过 `command` 使用该路径。Pin 继续引用原私有 data；非私有 Discover 和私有临时 Discover 都承接绑定。
- 私有 storage 的读取仍要求连接 profile 与磁盘 profile 完全一致，移除与新运行程序的历史路径相等要求。新 profile 创建仍只接受服务允许的程序。连接、credential fingerprint、generation、成员权限和订阅进度没有路径迁移。
- 工具列表检查有效飞书程序，通知不依赖先启用成员工具。接入保留白名单错误类别，全局与成员结果共用错误文案；配置保存和采集状态继续分别显示。托管发现／新建请求省略执行选择，已有完整 profile 的权限和授权请求保留历史引用。

## 飞书本地验证（1A）

受控行为包括：A 创建 profile、B 打开原数据、删除 A 后发现／核验／权限／Pin；完整 profile 篡改、跨 Space、data 缺失／符号链接拒绝；factory 组合共享队列，旧 client 不追随新 factory；程序修复后重新检查；原 operation 继续工具步骤且订阅 ID、版本、baseline 和查询进度不重建，`unknown` 不重放；未开启成员工具的通知仍用 B 查询。

认证回归夹具补齐现有的 `docx:document:readonly` scope，使其与当前权限合同一致；生产权限列表未变。

本地检查已通过：

| 检查 | 范围与结果 |
| --- | --- |
| 普通单测／导入边界 | `lark`、`tool`、`platform`、`app/platforms`、`room` 的程序选择、factory、storage 和依赖边界定向检查通过 |
| 适配器 integration | `internal/platform/lark`、`tool`、`platform`、`app/platforms`、`platform/scion`、`platform/gitlab` 全部通过 |
| Room integration | 接入、程序升级、权限准备、成员通知接入、工具发现、SpaceRuntime、GitLab/Lark entry、Scion 身份／help／生命周期／授权／退出定向回归通过 |
| 独立 wrapper | `tools/gitlab`、`tools/scion` integration 通过，未接入 1B／阶段 2 的新选择规则 |
| race | factory／storage／升级恢复与权限准备、unknown 结果相关的 `lark`、`room` 定向检查通过 |
| 前端 | 飞书、接入、成员平台、CLI 工具和 Scion 的 217 项组合测试通过；另跑托管发现／新建 3 项定向测试（含 1 项新增）通过 |
| 静态与编译 | 受影响包 `go vet -tags=integration`、相关 `make test-e2e-check` 通过；后者仅编译，不是实际浏览器验收 |
| diff | `git diff --check` 通过 |

使用仓库 Makefile 的 `TEST_PACKAGES`／`TEST_RUN` 限定范围。适配器回归命令为：

```sh
make test-integration TEST_PACKAGES='./internal/platform/lark ./internal/platform/tool ./internal/platform ./internal/app/platforms ./internal/platform/scion ./internal/platform/gitlab'
make test-integration TEST_PACKAGES='./tools/gitlab ./tools/scion'
make test-integration TEST_PACKAGES='./internal/room' \
  TEST_RUN='Test(Onboarding|ManagedLark|LarkProgram|LarkToolConfiguration|MemberNotification.*Onboarding|SharedHostTools|SpaceRuntime|ToolDiscovery|PlatformEntr|ScionMemberTools|ScionCallLifecycle|ScionAuth|ScionLogout)'
```

工作日志位于忽略目录 `.local/platform-program-dependencies/`，不包含真实账号或凭据。

## Linux 原生私有存储

在本机 Docker 的 Ubuntu 24.04／glibc 2.39、Linux arm64 环境，以 UID/GID 1000:1000 和 `--network none` 执行 `TestLinuxNativeLarkCredentialStorage`，已通过。使用锁文件固定的官方 `lark-cli 1.0.99` arm64 归档，SHA-256 为 `2837145e72922d558538494d0da30ac6c509681fc0791f8818f4ddb87f5125b4`。

测试在隔离 home 下用合成 app secret 创建两个 Space 的原生配置与加密文件，确认主密钥独立、无明文合成凭据、服务 home 无 CLI 回落写入；将同一协议版本的原生 CLI 复制为 A/B 两个物理程序，删除 A 后 B 可继续读取原私有配置并发现 profile，metadata 未被改写。

CLI 的 `config init` 会在本地保存之后探测应用。测试通过本地拒绝代理及禁网阻断该探测，允许其已知退出码 3，并独立检查配置、加密存储和升级后读取；不将该退出当成真实应用或 OAuth 验证成功。测试未使用线上账号、token、DPoP 或业务目录。

可复现命令（将 `NATIVE_FIXTURES` 指向只含测试二进制和锁定 CLI 的本地目录）：

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -tags=integration -c \
  -o "$NATIVE_FIXTURES/lark.test" ./internal/platform/lark

docker run --rm --network none --user 1000:1000 \
  -v "$NATIVE_FIXTURES:/fixtures:ro" \
  -e TEAMX_LARK_STORAGE_TEST_BIN=/fixtures/lark-cli \
  --entrypoint /fixtures/lark.test teamx-p2-ubuntu:arm64 \
  -test.run '^TestLinuxNativeLarkCredentialStorage$' -test.v
```

此处是 1A 的定向原生存储检查：未使用最终发行包，未运行 systemd，也未验证目标 Linux amd64 服务的真实凭据、通知、OAuth 刷新或旧 Session。不能替代阶段 3A、3B。


## GitLab 两层程序一致性（1B）

`room.platformProgram` 接通 `teamx-gitlab` 的可信服务选择，`tool.ResolveDependencyIn` 复用同一解析实现，从所选 wrapper 所在目录选择 `glab`。已安装服务绑定物理 release；管理员的两层绝对路径覆盖优先，完整安装缺失或覆盖失效不回退。独立运行仍允许服务同目录及绝对 PATH 发现，Scion 保留原有链路。

外层把原生依赖路径投影到 `Definition.ProcessEnvironment` 的 `TEAMX_GLAB_BIN`，按键替换并删除重复项，不修改原服务环境。列表检查和 `Process` 预检读取该值，子进程继续使用既有环境白名单；Catalog 不增加程序枚举或新状态，Process 的准入、取消和结清 owner 未变。

wrapper 独立运行保留同目录／绝对 PATH 发现，拒绝相对覆盖，先解析到物理路径，再用同一路径进行版本探测和实际命令。依赖缺失／无法运行与已确认版本不兼容保留不同诊断，均返回 127；取消保留 130 和原结果不确定性规则。列表不运行版本检查或网络请求。

本地验证已通过：

| 检查 | 结果 |
| --- | --- |
| 选择与装配 | 个人／多用户装配覆盖服务安装、独立 PATH、wrapper 覆盖后的同目录依赖、原生覆盖、重复环境键、缺失／不可执行／相对覆盖；预检与执行共享同一选择 |
| 调用固定 | 安装链接切到 B 后，已受理调用和仍运行的 A 服务继续使用 A 的两层程序；新服务装配使用 B。删除 B 的原生依赖返回 127，不调用 wrapper／PATH；修复后重新检查可用，成员选择不改写。此为受控程序夹具，不是服务热更新或完整部署测试 |
| wrapper 固定 | 版本探测期间改变符号链接，实际命令仍使用通过探测的原程序；缺失、版本不符、启动失败及取消／子进程结清回归通过 |
| HTTP 独立性 | 在缺失安装目录及原生依赖的服务配置下，Events／Todos 查询、OAuth 刷新和原进度核对通过；内置文字／图片评论及回复的受控 HTTP 成功、失败、unknown 与去重组合通过 |
| 平台回归 | `tool`、`platform` 导入边界、`app/platforms`、GitLab／Lark／Scion adapter 与两类独立 wrapper integration 通过；Room 工具发现、准确平台入口、多账号、飞书升级、Scion 准入和生命周期定向回归通过 |
| 原生离线 | 新编译的 `teamx-gitlab` 通过生产 Catalog／工具服务调用本机 `glab 1.127.0 (2e352a17e)`，父级／原生命令／扩展帮助均通过；无账号、无第三方 API 调用 |
| 并发及静态 | 程序选择、环境过滤、安装切换、工具取消／结清与飞书升级相关定向 race 通过；受影响包 `go vet -tags=integration`、相关 e2e 编译和 `git diff --check` 通过 |

主要复验入口：

```sh
make test-integration TEST_PACKAGES='./internal/platform/tool ./tools/gitlab ./tools/scion ./internal/platform ./internal/app/platforms ./internal/platform/gitlab ./internal/platform/lark ./internal/platform/scion'
make test-integration TEST_PACKAGES='./internal/room' \
  TEST_RUN='Test(GitlabProgramPair|LarkProgram|ManagedLark|SharedHostTools|ToolDiscovery|PlatformEntr|ScionMemberTools|ScionCallLifecycle|ScionAuth|ScionLogout|OAuthBusiness|GitLabEvents|GitlabImagesManagedCLI|GitlabActionURL|HelpSkipsCredential|CLICancellation|ProcessLifecycle)'
make test-race TEST_PACKAGES='./internal/platform/tool ./tools/gitlab ./internal/room' \
  TEST_RUN='Test(GitlabProgramPair|NativeProgram|DependencySelection|ProcessUsesBound|LarkProgram|ManagedLark|HelpSkipsCredential|CLICancellation|ProcessLifecycle)'
```

原生离线帮助检查使用 `TestIndependentGitLabHelp`，设置 `TEAMX_TEST_GITLAB_BIN` 指向本轮编译的 wrapper，`TEAMX_GLAB_BIN` 指向本机锁定版本 glab；普通 integration 未提供这两个程序时不会把该检查视为通过。此处未验证 Linux 最终发行包、目标服务真实通知／OAuth、Session 恢复或真实外部写入，不能代替阶段 3A、3B。

## Scion 全入口程序绑定（阶段 2）

阶段 2 已完成本地开发验收。`platformProgram` 复用公共解析规则选择 Scion 主程序，`scionProgram` 仅把主程序与必要的原生依赖投影到既有 `tool.Definition`；GitLab 的依赖绑定函数按实际用途改名为 `bindProgramDependencies`，没有增加解析器、持久 schema 或状态 owner。

已安装个人服务使用控制面物理 release 与管理员覆盖，独立个人／源码运行仍分别使用原 profile 的外部程序。明确的封装组合固定 `TEAMX_SCN_BIN`，按键替换重复环境项；直接原生 `scn` 不要求封装或另一层依赖。发现／登录、账号核验、工具列表、help、准备、业务调用和 Workspace 均使用外层选择，Scion adapter 不再重新发现程序。原生封装也将路径展开为物理路径，版本探测期间切换链接不会改变实际执行文件；无效覆盖与损坏同目录依赖拒绝回退。

Scion adapter 的核验和准备接收已有 `tool.Definition`，其中携带本次执行路径及固定依赖环境；原始 profile 保持独立、原样参与身份／凭据证据。Workspace 的版本、whoami 和业务子进程使用同一选择及同一凭据副本，仍经过原 PID／取消／结清 tracker；服务捕获的环境覆盖进程后续环境变化，不增加 Session 字段或迁移历史 DSH／Agent CLI。

托管网页发现和新登录省略执行选择，隐藏程序高级设置；服务拒绝请求方指定其他程序。升级后，已有账号的保存、核验和退出继续使用原完整 profile，不因新程序路径而替换凭据引用。Linux 多用户 Scion 与仓库执行继续禁止。

本地验证已通过：

| 检查 | 结果 |
| --- | --- |
| 来源与两层路径 | 托管旧 profile、主程序／原生覆盖、重复依赖环境、安装缺失、失效覆盖不回退、不同外部 profile 及直接原生程序通过；列表不启动 CLI |
| 升级与在途调用 | 删除历史程序后仍可核验与调用 B，已受理 A 调用的准备和业务继续用 A；连接快照（含 generation／credential version／fingerprint）及成员绑定未改写。依赖损坏时列表与调用共同拒绝，help 在凭据缺失时仍可使用当前程序 |
| Workspace | 旧路径删除后经过当前 wrapper／native 完成 repo／branch 核验与三成员 clone；源凭据在版本探测后被替换，当前调用仍用原副本核验 subject 和执行业务，结束后删除副本。既有目标、账号、权限、部分失败／重试与取消结清回归通过 |
| 发现与登录 | 托管发现及 PAT 登录使用当前程序；其他请求程序被拒绝；不兼容原生版本被拒绝。此为合成凭据夹具，不是实际账号登录 |
| 平台与 Room 回归 | Scion／Lark／GitLab adapters、tool、导入边界、Catalog、两类独立 wrapper integration 通过；Room 的 Scion 全族、工具入口、SpaceRuntime、GitLab／飞书升级与生命周期定向回归通过 |
| 前端 | Scion、成员平台、接入和飞书通知的 204 项事件测试通过，包括托管请求省略路径、保存原 profile、退出原账号和登录前程序选择失败不轮询不存在的会话；未执行真实浏览器操作 |
| 并发与静态 | Scion 调用／认证／退出、凭据副本、程序绑定及 GitLab／飞书升级相关定向 race 通过；受影响包 `go vet -tags=integration` 和相关 e2e 入口编译通过，`git diff --check` 通过 |
| 原生离线 | 本机 macOS arm64 的 `bin/scn` 通过 `TestPinnedScnOffline`：固定 0.1.13 版本、9 种版本／帮助请求、原生用法错误、相对 `-C` 和私有配置 JSON 隔离通过；没有真实账号或第三方 API 调用 |

主要复验入口：

```sh
make test-integration TEST_PACKAGES='./internal/platform/scion ./tools/scion ./internal/platform/tool ./internal/platform ./internal/app/platforms ./internal/platform/gitlab ./internal/platform/lark ./tools/gitlab'
make test-integration TEST_PACKAGES='./internal/room' \
  TEST_RUN='Test(Scion|SharedHostTools|ToolDiscovery|PlatformEntr|GitlabProgramPair|LarkProgram|ManagedLark|HelpSkipsCredential|CLICancellation|ProcessLifecycle|SpaceRuntime)'
make test-race TEST_PACKAGES='./internal/platform/scion ./tools/scion ./internal/room' \
  TEST_RUN='Test(ScionProgram|ScionCallLifecycle|ScionAuth|ScionLogout|ProfileEvidence|NativeProgram|BrokenSibling|GitlabProgramPair|LarkProgram|ManagedLark)'
node --test internal/web/tests/web_scion_auth_test.mjs \
  internal/web/tests/web_platform_onboarding_test.mjs \
  internal/web/tests/web_member_platforms_test.mjs \
  internal/web/tests/web_lark_notifications_test.mjs
TEAMX_SCN_BIN="$PWD/bin/scn" go test -tags=e2e ./tools/scion -run '^TestPinnedScnOffline$' -v
```

本轮日志位于 `.local/platform-program-dependencies/scion-*.log`。未部署、未修改线上数据、未执行远端写入；未复验真实账号／OAuth／PAT 登录，也未验证 Linux 最终发行包、目标服务恢复或旧 Session 组合。阶段 3A、3B 继续保留这些验收边界。

## Linux 安装产物验收（3A）

验收入口为 [`scripts/platform-program-acceptance.mjs`](../../scripts/platform-program-acceptance.mjs)。它要求非 root 的 Linux amd64 环境、两个不同源码 commit 的干净 `standard` 归档，以及构建输入与候选归档完全一致的干净源码。仅验收入口、原生存储测试和只读 CLI 夹具允许独立修订，报告分别记录候选与验收源码 commit；其他源码差异或任意归档构建输入摘要不一致均拒绝。输出目录必须是新目录；可复用与归档 SHA-256 匹配的 full smoke 结果，quick 结果不能替代 full。

```sh
node scripts/platform-program-acceptance.mjs /delivery/teamx-CANDIDATE-linux-amd64-standard.tar.gz \
  --previous-archive /delivery/teamx-PREVIOUS-linux-amd64-standard.tar.gz \
  --output-dir /evidence/platform-program-upgrade
```

指令集模拟环境必须显式增加 `--emulated`。构建时先缓存 Go 模块和锁定的发行依赖；验收建议在 `--init --network none` 的容器内执行，保留 loopback 供本地 HTTP 服务使用，不挂载个人 home、线上账号、凭据或业务目录。`HOME`、Go 缓存、源码和输出目录均由该非 root 用户拥有。PID 1 须回收退出的孤儿进程，避免僵尸 PID 干扰原有进程结清断言；模拟环境的存储测试仅允许服务 home 中空的 `.cache/rosetta` 目录，任何其他目录或缓存文件仍拒绝。

入口验证三项组合：

- **完整包与程序**：校验两份归档的 full smoke、架构、profile 和干净源码身份；运行包内 TeamX、Node、DSH、GitLab wrapper／原生 CLI 和飞书 CLI。Scion 原生 CLI 仍不在 standard 包中，保留缺失提示及多用户禁用规则。
- **真实私有存储**：`TestLinuxNativeLarkCredentialStorage` 接收两份包内飞书程序，使用合成 app secret 检查加密、目录／文件私有权限、两个 Space 的主密钥隔离；删除旧程序后读取原数据，检查 profile 未改写，且服务 home 无 CLI 回落写入。已知的配置后网络探测失败不算真实应用核验通过。
- **升级与恢复**：在真实 `server-root/releases/current` 布局中运行归档 A 的服务；通过公开 API 创建连接和 operation，保存通知后使工具程序不可用，正常停服并切换 B。B 使用管理员绑定的只读协议夹具，删除的历史 CLI 不再参与选择；原 operation 继续工具步骤，原订阅成功查询，连接快照逐字节保持，订阅 ID／版本／baseline／配置／账号保持。夹具拒绝业务写入，DSH 使用现有离线 ACP 夹具。另在 Linux 执行阶段 1、2 的程序装配、在途调用、GitLab 原生帮助及多用户准入定向回归。

这项测试中的 release 切换仅用于隔离验收；实际部署继续使用原 installer、备份和结清流程。宿主 systemd、目标服务的真实通知／OAuth／refresh／DPoP、远端写入及旧真实 Session 的验收仍属于 3B。

### 本轮固定产物与环境

当前工作分支未提交。候选代码固定在独立 Git 快照，程序源码 commit 与验收代码 commit 分别保存，未把其他文档／测试整理任务混入候选。两份完整包均由 Linux amd64 的 Go 1.27.1／Node 24.22.2 进程通过原 `package.mjs --smoke full` 构建；没有交叉编译单个程序替代发行包。

| 项目 | 本轮记录 |
| --- | --- |
| 旧包 A 源码 | `69828ce4260af8dfd4ae946efb1d4d0962bcb514` |
| 候选包 B 源码 | `de26062af32aa56f3f82b56929b096e5c52f9c77` |
| 验收代码源码 | `0a916044c2539f3fe480055ea4e9b09f9951991f`；相对候选仅两处验收代码变化，另逐项核对候选的全部程序及打包输入摘要 |
| A 归档 SHA-256 | `48e86f7bd3d1dce577772055b8d21b519ebab34caace721577bb0d7f33d131b0` |
| B 归档 SHA-256 | `19dc744f3c05c319fd0fb158ff26288d9322b8a7ae3113873fe5d385779086a7` |
| Linux 环境 | Ubuntu 24.04、glibc 2.39、Linux amd64；宿主为 arm64，使用 Docker Desktop Rosetta 指令翻译 |
| 运行身份与隔离 | UID/GID `1001:1001`；最终组合使用 `--init --network none`，仅 loopback；独立 home、合成凭据与只读平台协议夹具 |
| 验收镜像 | `sha256:ebe4b9e1ca8e97701410e2450063c94377017e53e5ab66d999c1f5b3ae8a863b` |

两份归档及 full smoke 回执保存于 `.local/platform-program-dependencies/artifacts/{previous,candidate}/`。B 文件名为 `teamx-dev-de26062af32a-linux-amd64-standard.tar.gz`；独立源码分别保存为 `candidate-source.bundle` 和 `verifier-source.bundle`，均位于同一工作日志根目录。3B 应直接使用这份 B 归档；普通 HEAD-only 部署若另建候选，须重新确认受影响检查与 3A 出口。

首次组合发现并定位了两项验收环境差异：Rosetta 创建空 `.cache/rosetta`；无 init 的容器保留已退出的僵尸 PID。前者通过保留 home 的诊断检查确认为空目录，后者通过进程 `STAT=Z` 确认。最终存储测试只对显式模拟环境接受上述空目录；Scion 原有进程结清断言在 `--init` 容器中重新通过。没有修改生产存储、执行或进程结清代码来适配这两项差异。

真实服务组合还核对了启动时序：HTTP ready 之后，Room 可能仍在异步恢复；过早继续工具步骤会返回已有的 `busy` 反馈。验收入口先通过原成员目标的只读查询等待该恢复 gate，再提交一次原 operation 的 apply，不自动重放未知提交或第三方写入。

### 验收结果

**3A 已通过，未部署。** 最终报告为 `.local/platform-program-dependencies/linux-acceptance/report.json`，SHA-256 为 `4b3da357442052a44dc425c943b1f1f0673c324ab82a41aa3aed12c17e5246a3`。五个必测组合均为 `passed`，没有跳过其中的原生存储或 GitLab 原生帮助检查。

| 检查 | 结果与证据 |
| --- | --- |
| A、B 完整归档 | 两份 full smoke 均通过；摘要与保存的归档逐一匹配，含清单／权限／依赖、受限 PATH 的 GitLab launcher、只读包目录、HTTP、Room 持久化、固定 ACP 夹具和正常结清 |
| 原生私有存储 | 使用 A／B 包内 `lark-cli 1.0.99` 通过 Linux 合成凭据检查；私有权限、加密、独立主密钥、删除 A 后读取原 metadata、Space 隔离与服务 home 无 CLI 回落写入均通过；Rosetta 的两个空目录单独记录 |
| Linux 程序装配 | `composition.log` 的 43 个测试项（含子项）均通过，无 skip：托管飞书恢复、通知无工具依赖、GitLab 两层程序固定与真实离线帮助、Scion 调用／Workspace、外部程序模式、多用户拒绝和导入边界 |
| 真实归档服务升级 | A 的原 operation 为“通知 applied／工具 failed”；B 读取原 operation 后仅继续未完成工具步骤，工具变为 applied，原订阅成功完成查询；服务 A／B 正常结清 |
| 业务数据保持 | 原 operation ID 不变；连接文件 SHA-256 为 `2fa57b6e4a361959d43ae7afa2692c3269b36df0943a609553b66b3582116437`，升级及停服前后逐字节相同；订阅 ID `10ce80471a1479df5bb6354ce804a6bb`、版本、baseline、配置和账号保持，查询进度允许正常前进 |
| 受影响回归 | Linux 的 `lark`／`tool`／平台边界／Catalog／Scion／GitLab adapters 与两类独立 wrapper integration 通过；Scion 的进程结清测试采用带 init 的容器复验通过。阶段 1、2 的并发及其余行为证据继续复用 |
| 前端与静态 | 本轮 204 项前端事件测试、飞书协议／升级定向测试、受影响包 `go vet -tags=integration`、Linux 与 macOS 的相关 e2e 入口编译、脚本语法与 `git diff --check` 均通过；e2e 编译不计作浏览器或模型验收 |

最终组合日志为 `linux-acceptance-final.log` 与 `linux-acceptance/`，环境记录为 `linux-init-environment.json`，均位于上述工作日志根目录。首次失败与原因定位日志保留在 `failed-native-logs/`、`failed-service-logs/`、`linux-storage-*-diagnosis.log`；Scion 重新通过的完整结果见 `linux-scion-init.log`。这些环境／验收时序问题均已关闭，没有改变已验收候选的归档字节。

以上 3A 出口仅证明所记录 Ubuntu 24.04／glibc 2.39 模拟环境中的产物、私有存储和隔离升级组合；当时未执行宿主 systemd、目标服务实际升级、真实 OAuth／refresh／DPoP、真实账号通知、旧真实 Session 或第三方写入。后续按用户指令部署同一份 B 包的结果见下文，不把这些实机结果追溯计入 3A。

## 目标 Linux 服务恢复（3B）

本轮按用户“实施阶段 3B”的指令升级既有多用户开发验收服务。部署原 3A 候选归档，没有重新构建或通过 HEAD-only 脚本交付其他代码；生产源码没有新增改动。目标为原生 Linux amd64、Ubuntu 22.04.5／glibc 2.35、systemd，服务仍以 UID/GID 997 的普通账号运行。

| 项目 | 实机结果 |
| --- | --- |
| 候选身份 | `dev-de26062af32a`；源码 `de26062af32aa56f3f82b56929b096e5c52f9c77`，`dirty=false`；归档 SHA-256 仍为 `19dc744f3c05c319fd0fb158ff26288d9322b8a7ae3113873fe5d385779086a7` |
| 停服前检查 | 上传原包并校验摘要；在独立目录以服务账号运行包内 TeamX、Node 24.22.2、飞书 CLI 1.0.99、GitLab wrapper／glab 1.127.0 和 DSH 0.2.2-rc.1，确认目标 glibc 可运行。没有用该独立目录替代安装根 |
| 升级 | 使用归档内 `install.sh --upgrade --archive … --sha256 … --backup-dir …`，由独立 systemd 临时服务执行；沿用原服务登记、执行环境准备、正常结清、一致备份、切换和认证检查。停服流程含备份／启动检查共 20.49 秒 |
| 服务与入口 | 既有 installation ID 保持，systemd `active/running`、`enabled`，认证控制面 `ready`；公网登录页及递归静态依赖可访问，私有接口仍拒绝匿名访问 |
| 备份与保留 | 新一致备份完成流式写入及顺序回读校验；调用原 `service prune --from … --keep-releases 2 --keep-backups 2` 成功。保留 4 个仍需留存的 release，包括旧 Space／Session 使用的版本，以及最近两份已登记备份 |

首次 systemd 临时任务缺少管理进程的 `HOME`，安装器在检查原登记时退出，尚未停服或切换。补齐管理进程原有 home 环境后，原安装器成功执行；首次失败日志保留。没有修改服务配置或生产代码来绕过检查。停服前独立目录检查也曾因父目录不可遍历而失败，将无凭据的检查程序放到服务可访问的独立目录后通过，没有放宽已有私有业务目录权限。

### 身份、凭据和恢复结果

升级前保存业务基线，升级后及查询恢复后再次核对。真实账号凭据留在目标服务器；本地仅保存检查结果和日志，不复制原 token、主密钥或 DPoP 数据。

| 检查 | 结果与范围 |
| --- | --- |
| 身份与配置 | 既有 User／Space 目录保持；6 个连接文件逐字节一致，generation、credential version、账号和 fingerprint 不因程序更新改变；Room 目录、成员身份、Space 配置、模型验证收据和私有 `profile.json` 保持 |
| 真实飞书凭据 | 使用已安装物理 release 的 CLI，以服务账号和原 Space storage 执行当前 adapter 的账号／权限／Pin 检查，两份原账号均匹配，私有凭据可读，账号固定成功。诊断程序使用与候选相同的 Lark adapter 源文件，不代替下面的服务实际采集证据 |
| 权限结果 | 故障账号的应用／用户 scopes、续期条件和各项能力检查通过。另一账号准确报告 `docx:document:readonly`、`contact:user.basic_profile:readonly`、`contact:user:search` 缺失，文档／联系人能力 blocked；消息和通知能力 ready。未补授权或重新 OAuth |
| 飞书原通知 | 3 个原订阅的 private／groups／own 共 9 个分支均在新版服务成功查询。故障账号两个订阅原有的 `invalid_cli_storage` 分支依次等待原退避后恢复；到 2026-11-27 06:24:24 UTC，全部分支失败计数为 0、无错误。未清游标或移动 baseline |
| GitLab 原通知 | 7 个原通道（2 个 Events、5 个 Todos）均在新版服务继续成功查询、连续失败数为 0。订阅 ID／版本、账号绑定、过滤条件、起点保持，watermark／查询进度正常前进 |
| 成员工具恢复 | 用户在原成员页面点击“开启工具”；原目标的飞书绑定保存成功，工具列表呈现 valid 及 read／原有动作能力。没有新建连接／订阅或要求重新授权，另一个已启用成员的工具保持 |
| 旧 Session | 6 份原 Session 描述逐字节保持。已检查的原 Session 继续使用 `dev-bf3d23cc8384` 的 DSH 和 Agent CLI，经过新版控制面完成工具列表、GitLab 原生帮助和 `api user --method GET`，返回原账号；原 Session 回合从 54 推进到 58 并 completed，未手工轮换／重置。其余休眠 Session 仅核对保存描述，不算全部实际恢复通过 |
| 多用户限制 | 对目标 Space 的 Scion discover 做有界拒绝探测，返回 HTTP 403／`space_capability_unavailable`；未临时开放 Scion 或仓库执行 |

正常停服在 Room 目录中保存的 `shutdowns` 由新版恢复流程处理后清除；最终目录与升级前基线一致。工具绑定变化来自用户本轮“开启工具”，成员 `agent.json` 的回合变化来自恢复后的正常运行；这些正常提交没有被误记为路径迁移。

### 原接入记录及未验项

原 operation `881824c5…` 仍保留“通知 applied／工具 failed，apply_interrupted”的历史回执。用户在当前页面未见旧继续入口，选择通过原成员设置开启工具；本轮确认了这次实际保存和可用性，**没有在目标服务续跑原 onboarding apply，也没有把内容一致的成员设置冒充原 operation 的成功回执**。原记录逐字节保持；成员绑定已变化，不再为验收重放旧 apply。3A 中原 operation 续跑的夹具结果继续独立记录，不能代替此项真实验收。

因此本轮结论为：**同一候选已部署，已列原通知和成员工具恢复、已检查旧 Session 运行通过；原 onboarding 续跑未验。** 实际 OAuth token 刷新未被独立证实，不强制使正常凭据失效；当前原生 CLI 的真实读取和 Pin 成功不等于完整 DPoP 协议专项验收。验收未主动发送飞书消息、发布 GitLab 评论，也未执行备份还原或任意版本回退。

私有证据入口：`.local/platform-program-dependencies/target-3b/report.json`，报告状态为 `recovery_passed_with_unverified_items`，20 项恢复检查全部为 true，未验项另列；报告 SHA-256 为 `38a9d6de89d8b32786663a19a6e2f6c620d04e8215003ffe53093598cdb743b1`。实际主机／目录仍见 `.local/linux-environment.md`；完整基线、运行结果和诊断程序保存在目标机独立的本轮验收目录。安装器、retention 日志和公网入口检查结果同时保存于本地 `target-3b/`。本轮未提交或 push 工作区其他任务改动。
