# 独立 Scion 工具

`teamx-scion` 是只依赖 Go 标准库的原生 CLI 封装，每次先核对 **scn 0.1.13**，再原样执行命令。不导入 TeamX 内部包，不调用内部协议，不重写 Review API。通过 TeamX 开发与评审的首次配置见 [Scion 使用说明](../../docs/scion.zh.md)；本文维护安装、认证与执行细节。[接入方案](../../docs/SCION-SOFTWARE-DEVELOPMENT-PLAN.zh.md)的 S1–S4 已实施，真实流程与未验项见 [S4 验收记录](../../docs/scion-s4-acceptance.zh.md)。

## 安装

当前仅核实 macOS arm64 官方发布包，其他平台需分别核实发布和进程生命周期。[scn.lock.json](scn.lock.json) 记录版本、发布 URL、平台、包名及其独立 `.sha256` 文件；该发布没有 `SHA256SUMS`。运行时精确检查版本，安装时由操作者核对发布文件校验和；封装不自动下载、安装或升级。

1. 从 [v0.1.13 发布页](https://scion.opencsg.com/OpenCSG/scion/releases/v0.1.13) 下载 `scn-v0.1.13-aarch64-apple-darwin.tar.gz` 及同名 `.sha256`，该实例需要登录。下载 URL 见 lock；凭据不要写进命令参数、仓库或共享日志。
2. 在下载目录校验，并将摘要与 lock 中的 `4a3d15ce772070fddd89837d66e443b22f4baf9cc253bd64175e3b19456eeb07` 核对：

   ```sh
   shasum -a 256 -c scn-v0.1.13-aarch64-apple-darwin.tar.gz.sha256
   tar -tzf scn-v0.1.13-aarch64-apple-darwin.tar.gz
   ```

3. 检查压缩包内容，将其解包到新的空目录，安装其中的 `scn`。在 TeamX 仓库根目录构建：

   ```sh
   mkdir -p bin
   go build -o bin/teamx-scion ./tools/scion
   install -m 0755 /absolute/path/to/extracted/scn bin/scn
   bin/scn --version
   bin/teamx-scion --help
   ```

二进制解析顺序：显式 `TEAMX_SCN_BIN` 绝对路径 → `teamx-scion` 同目录的 `scn` → 服务 PATH 中的绝对目录。不搜索成员 Workspace，不接受相对覆盖路径或相对 PATH 项。显式覆盖失效不会回退；存在但不可运行的同目录依赖也会清楚报错。依赖缺失、版本探测失败（上限 5 秒）或版本不符返回 127，并提供安装诊断，不运行实际业务命令。

TeamX 对封装的解析：`TEAMX_SCION_BIN` → TeamX 同目录的 `teamx-scion` → 服务 PATH。连接 profile 的 `executable` 显式指定所选 Scion CLI（`teamx-scion` 或原生 `scn`）的绝对路径，身份核验、业务调用和无凭据帮助均使用该路径。直接选择 scn 时，同样经过 TeamX 私有进程环境、目的地址和业务调用前的固定版本/账号校验。原生 `TEAMX_SCN_BIN` 从服务环境传给封装；服务端调用所需路径应在启动服务前配置，Agent shell 内临时修改 PATH、环境或 `cd` 不会传给服务端。

## TeamX 页面登录

正常使用见 [Scion 使用说明](../../docs/scion.zh.md#首次配置)。页面填写实例和 `teamx-scion` / `scn` 路径后，自动检测当前服务受管目录中的账号；单个账号自动选中，多个账号显式选择。没有可用账号时，可在页面输入 PAT 登录；后台生成新的 profile ID，调用固定 CLI 的 `login --with-token`（仅经 stdin 传入 PAT），再用 `whoami` 核实账号。成功后保存连接，无需手工准备 profile。

PAT 不进入命令参数、公开响应、Room、成员环境或连接 JSON；凭据文件仍由 scn 在服务私有目录中保存。重新登录始终创建新 profile，原连接和在途调用继续使用原凭据，直到操作者保存替换引用；保存时沿用现有引用确认和调用结清。登录可取消，服务退出会等待登录进程结清；失败或取消清理本次未完成的新 profile。

检测只枚举当前服务的受管 profile，不隐式扫描全局登录文件或 Keychain；已有手工配置也可自动发现。原生 device flow 是否可用仍取决于服务端能力，本次页面入口使用 PAT，不需要 device flow。

## 独立调用与环境

帮助、版本和原生用法错误无需登录、Workspace 或业务 API：

```sh
bin/teamx-scion --version
bin/teamx-scion help review
bin/teamx-scion review show --help
bin/teamx-scion review comment --help
```

每次调用使用新的临时私有 HOME、XDG 配置和缓存目录、Scion 配置文件与默认空凭据路径，结束后删除。强制 `SCION_CREDENTIALS=file`、`SCION_NO_DAEMON=1`；不读取 Keychain 或操作者全局 Scion/Git 配置。仅保留 PATH、语言、时区、TLS 证书与代理环境，以及以下显式运行输入：

| 变量 | 含义 |
| --- | --- |
| `SCION_ENDPOINT` | 明确的仓库服务地址 |
| `SCION_HUB` | 明确的 Review Hub 地址；v0.1.13 默认推导至 7088，当前实例必须显式设置 |
| `SCION_REPO` | 仓库 canonical name，原生 `--repo` 也可指定 |
| `SCION_CREDENTIALS_FILE` | 已登录的隔离凭据文件绝对路径；未提供时只使用本次临时空路径 |
| `SCION_AUTHOR` | 明确的非秘密提交作者，如 `Developer <dev@example.com>`；也可使用原生 `--author` |

`SCION_TOKEN` 始终被过滤，即使操作者环境中存在也不会覆盖已保存登录。其他 `SCION_*`、Git 环境、SSH agent、其他平台和模型凭据均不传入原生进程。版本探测只用空凭据和临时配置，不注入上述运行输入。原生参数保留，帮助由 scn 自己解析，不通过扫描参数中的 `--help` 改变认证或执行方式。

独立运行由操作者明确提供 endpoint、Hub 和 profile。TeamX S2 在服务端约束连接身份与凭据目的地址；独立封装本身没有成员连接上下文。工具使用账号实际权限，不构成平台权限沙箱。不要把 shell 中已有的全局配置当作成员绑定。

操作者可用**原生 scn** 为独立 profile 登录；目录权限为 0700、凭据文件为 0600，通过 stdin 提供可交换 PAT：

```sh
env -u SCION_TOKEN \
  SCION_CREDENTIALS=file \
  SCION_CREDENTIALS_FILE=/absolute/private/profile/credentials.json \
  SCION_NO_DAEMON=1 \
  bin/scn login https://scion.opencsg.com --with-token < /absolute/private/pat.txt
```

常规工具不负责登录、切换账号或查找凭据。使用已登录文件进行只读调用：

```sh
export SCION_CREDENTIALS_FILE=/absolute/private/profile/credentials.json
export SCION_ENDPOINT=https://scion.opencsg.com
export SCION_HUB=https://scion.opencsg.com
bin/teamx-scion --json whoami "$SCION_ENDPOINT"
bin/teamx-scion --json repo info OpenCSG/teamx-demo
bin/teamx-scion --json --repo OpenCSG/teamx-demo review ls --state all --limit 100
```

## 执行合同

封装保留启动时的 cwd。TeamX 在成员有效 Workspace 启动；未配置时使用本次临时目录。按子目录运行显式使用原生 `-C PATH`，相对路径由原生执行目录解释。参数通过 argv 直接传给 scn，不使用 shell，也不转换 GitLab 命令、对象或 JSON schema。

stdin 支持文件和管道；stdout/stderr 按字节流转发，不增加标题或重写 JSON。Unix 使用独立会话与进程组，不继承控制终端；原生输出通过管道转发。正常结束也清理同组遗留后代。SIGINT/SIGTERM 取消会终止原生进程组、等待结清并清理临时目录，返回 130；版本探测阶段同样受取消管理。首期关闭自动 daemon，不应在常规任务中显式启动 daemon 或执行 self-update。

原生正常退出码保持不变：0、1、2、3、4、5 分别保留原意；5 既可能是认证失败，也可能是权限不足。依赖/启动错误为 127，取消或信号终止为 130。封装没有自动重试或 REST 回退。远端写入在超时、取消、断流后的结果可能未知，应先只读核对实际目标，不能重复创建 Review、评论、提交或发布来探测结果。

原生开发语义仍须遵守：指定路径 commit（没有 Git 暂存区）；`scn land` 发布当前分支，`scn review land N` 才是 Review 合入；使用完整 Scion ObjectId；发布后核对实际 head；评审用 `switch --detach FULL_HEAD`。团队开发、修正与复审流程见 [Scion 使用说明](../../docs/scion.zh.md#日常使用)。

## 验证

```sh
go test -race ./tools/scion
go vet ./tools/scion
TEAMX_SCN_BIN=/absolute/path/to/verified/scn \
  go test -race -count=1 ./tools/scion
```

前两项为组件验证；最后一项额外使用固定官方二进制验证离线帮助、版本、原生用法错误、相对 `-C`、JSON 和配置隔离。组件测试覆盖 argv/stdin/输出/退出码保留、二进制解析、错误版本、取消探测与业务进程、后代结清及无控制终端。未提供 `TEAMX_SCN_BIN` 时，官方二进制测试明确跳过。实际阶段证据见 [S1 验收记录](../../docs/scion-s1-acceptance.zh.md)。

连接与页面回归由 `go test -race ./internal/platform/scion ./internal/room -run '^TestScion|^TestProfile'` 和 `node --test internal/room/web*_test.mjs` 验证。`TestScionRealLoginDiscovery` 通过 `TEAMX_SCION_AUTH_REAL=1` 显式启用，需提供 `TEAMX_SCION_TEST_CREDENTIALS` 私有原生 PAT 凭据源、`TEAMX_SCN_BIN` 与 `TEAMX_SCION_BIN` 绝对路径。

2026-11-07 已在隔离服务根实测两种 CLI 路径的 PAT 登录、自动发现、连接保存/验证及成员仓库读取，实际 subject 为 csgship；原生 scn 路径的三份 Workspace clone 与重开回归也通过，没有仓库远端写入。实际浏览器控制仍超时，页面事件测试不能代替浏览器操作验收。

## TeamX 连接与成员绑定（S2）

正常页面使用无需手填 profile ID。下面保留命令行部署的手工登录方式，已有配置可由页面自动检测。在 TeamX 服务私有数据根目录（管理模式的 rooms 根目录）创建 `scion-profiles/<32位小写十六进制ID>/credentials.json`。`scion-profiles` 和 profile 目录均为 0700，凭据为 0600，不能使用符号链接。不要填写全局登录目录或任意凭据路径。

```sh
scion_profiles_root=/absolute/service/private-root/scion-profiles
scion_profile_id=0123456789abcdef0123456789abcdef
scion_profile_root="$scion_profiles_root/$scion_profile_id"
install -d -m 0700 "$scion_profiles_root" "$scion_profile_root"
env -u SCION_TOKEN SCION_CREDENTIALS=file \
  SCION_CREDENTIALS_FILE="$scion_profile_root/credentials.json" SCION_NO_DAEMON=1 \
  /absolute/path/scn login https://scion.opencsg.com --with-token < /absolute/private/pat.txt
chmod 0600 "$scion_profile_root/credentials.json"
```

页面「设置 → 平台连接 → 新增连接」选择 Scion，填写实例 HTTPS origin 和 CLI 路径。检测后选择上述 profile 对应的账号，无需输入 ID；保存后自动运行固定 CLI 的 `--json whoami`。有效的 `scion:subject` 账号才能被成员选择。内部引用结构保持不变，例如（不含秘密）：

```json
{
  "provider": "scion",
  "url": "https://scion.opencsg.com",
  "cli_profile": {
    "version": 1,
    "brand": "scion",
    "profile": "0123456789abcdef0123456789abcdef",
    "executable": "/absolute/path/teamx-scion",
    "cli_version": "0.1.13"
  }
}
```

每名成员再到详情的工具设置单独选择 Scion 连接并应用。无工作副本可通过 `tools run scion --json --repo OWNER/REPOSITORY review ls` 读取 Review；启用工具不初始化 `.scion`。Workspace 设置选择 Scion 类型、仓库 HTTPS URL、分支和已验证连接，再显式应用，为每个 membership 分别 clone；留空分支会核实并冻结默认分支。提交作者每次用 `--author` 明确提供；三角色流程与验收见 [S4 记录](../../docs/scion-s4-acceptance.zh.md)。

服务为每次调用复制仅含当前 origin 的凭据至私有临时文件，并用该文件核对实际 subject 后执行同一调用；结束或取消后删除。原 profile 不被业务调用修改。此路径只验收原生 PAT，不承诺 refresh-token 轮换保存。原生错误输出保留；执行前检查失败返回 5 和安全分类，如 `identity_mismatch` / `credential_unavailable`，业务命令不启动。

在页面重新登录会自动创建新 profile ID，保存替换引用后重新验证，再核对成员绑定。命令行重新登录也应使用新 profile。替换引用、重新验证、停用、关房和退出均取消并等待在途调用。外部原地换号也会被下一次调用的 subject 检查阻止；已接纳调用只使用其已核验的凭据副本。帮助无需凭据，但仍要求该成员已显式绑定工具。

服务固定 Endpoint/Hub 为所选 HTTPS origin，拒绝指向其他 origin 的全局参数或仓库 URL，不继承全局 `SCION_*`。成员入口不执行 login/logout/token/config/daemon/self-update；这些由操作者维护。常规提交明确提供 `--author`。测试与实际边界见 [S2 验收记录](../../docs/scion-s2-acceptance.zh.md)。
