# TeamX 安装与运行

桌面和手机通过浏览器使用 TeamX，Agent 在 Server 所在机器执行。若已有 Server，直接打开它的地址并按提示登录，浏览器所在设备无需下载安装。

自行部署只需运行一次安装脚本。脚本下载完整的 standard 包并安装常驻服务，无需另装 Go 或 Node，也不要求提前配置模型或 export Key。安装完成后统一使用 `teamx` 管理，模型在网页设置。

安装前准备模型服务的地址、模型 ID 和 API Key。发行矩阵默认基线为 macOS 15 或 Ubuntu 24.04／glibc 2.39；Linux amd64 standard 的可选 SRT 组合另有 Ubuntu 22.04／glibc 2.35 的固定依赖合同与实机证据，范围见 [SRT 执行](#linux-amd64-的可选-srt-执行)。Linux 常驻安装需要 systemd；支持的架构及具体依赖见[下载与校验](#下载与校验)。

| 你要做什么 | 阅读位置 |
| --- | --- |
| 安装 TeamX | [一键安装](#一键安装) |
| 离线安装或使用开发快照 | [下载与校验](#下载与校验) |
| 设置模型、登录和远程访问 | [首次使用与访问](#首次使用与访问) |
| 查日志、启停或卸载 | [日常维护](#日常维护) |
| 备份、升级或恢复 | [备份与升级](#备份与升级) |

当前为开发预览，完整平台与设备的真实环境验证尚未完成。运行条件及实际测试范围以对应版本说明为准；CI 归档检查通过不等于该目标的真实模型、平台账号或设备已全部验证。

## 一键安装

在要运行 Server 的机器上执行：

```sh
curl -fsSL https://github.com/go-aie/teamx/releases/latest/download/install.sh | bash
```

此地址随包含安装脚本的正式 Release 发布；尚无对应 Release 时，使用下文的离线安装或开发快照。不要把开发分支源码当作已经发布的版本。

脚本自动识别系统和原生架构，下载并校验对应 standard 归档，准备独立数据目录，注册并启动系统服务，同时建立稳定的 `teamx` 命令入口。**无需再执行 `init`、`start` 或 `teamx service install`。** 安装完成后按输出打开网页、读取本机管理 token 并登录，再配置模型。

macOS 使用已登录的普通用户，不在命令前加 sudo；Linux 需要 systemd，脚本按需通过 sudo 获取安装权限，创建或复用专用 `teamx` 普通账号。关闭终端或浏览器不会停止服务。macOS 用户注销、电脑休眠或关机期间不保证执行。

| 系统 | 程序安装根 | 私有配置和数据 | 命令入口 |
| --- | --- | --- | --- |
| macOS | `~/Library/Application Support/TeamX/server` | `~/.teamx` | `~/.local/bin/teamx` |
| Linux | `/opt/teamx` | `/var/lib/teamx` | `/usr/local/bin/teamx` |

个人运行统一默认使用 `~/.teamx`，包括直接运行 `teamx serve`、新解压便携包和新 macOS 常驻安装。已有常驻安装继续使用登记的数据目录，已有便携包继续使用包内 `.teamx`，不自动搬迁；显式 `--home` 优先。独立调试或运行多个实例时，请为每个实例指定不同 home。

macOS 若命令目录不在 PATH，脚本会配置当前用户的 shell 启动文件，并提示新开终端或在当前终端执行的 PATH 设置。服务已经运行，不需要因此重新安装。Linux 后续管理命令前加 sudo。

`teamx` 命令始终指向安装根的 `current/bin/teamx`，升级后无需更换命令路径。通过该入口管理时会自动找到所属安装根。需要指定版本或目录时，可传参数，例如：

```sh
curl -fsSL https://github.com/go-aie/teamx/releases/download/vX.Y.Z/install.sh | bash -s -- \
  --bin-dir /absolute/command-bin \
  --server-root /absolute/server-root --home /absolute/private-home
```

版本替换为实际发布值，目录须为绝对路径且安装根与 home 互不重叠。自定义命令目录后应确保当前终端和 Linux sudo 的 PATH 可找到它，也可使用输出中的完整命令路径。Linux 可加 `--user NAME` 选择专用普通账号。`bash install.sh --help` 查看完整选项。

安装器在下载应用包前检查安装登记和当前程序；已有有效安装时显示当前版本、位置及升级命令后退出，不停服、不下载应用包。仅有 PATH 中的同名命令不算有效安装；登记损坏或不完整时显示检查提示。已有实例更换版本见[备份与升级](#备份与升级)。它不会自动导入其他 bundle 或开发目录的数据，不覆盖已有身份、模型或用户模板，也不覆盖命令目录中无关的同名文件。

注销服务后重新登记原版本，可使用 `bash ./install.sh --repair --archive /absolute/original-standard.tar.gz`，保留同名 `.sha256`；这不用于更换版本。部署失败会显示失败信息和保留的日志路径，排查后再按提示重试。

## 下载与校验

无法访问 GitHub 时，可以让维护者提供与你的系统和架构匹配的 `*-standard-offline.zip`。解压 ZIP，在其中的目录打开终端，执行：

```sh
bash ./install-offline.sh
```

这个离线包已包含原始应用归档、摘要和安装器；不需要再解压内层 `.tar.gz`、填写归档名或访问 GitHub。安装后按输出打开网页登录，之后统一使用 `teamx service` 管理。macOS 不加 sudo；Linux 安装器按需申请权限。离线安装包由维护者按需生成，不保证每个 Release 都附带；模型和平台服务仍需要各自的网络。

离线部署、开发快照或需要预先审阅脚本时，从项目 GitHub Releases 下载对应归档、同名 `.sha256` 和 `install.sh`。开发快照从 **Actions → Package snapshots → 对应运行 → Artifacts** 下载；Artifact 外层压缩包内才是应用 `.tar.gz`、摘要和检查报告，先解开外层。

| 包 | 包含内容与选择建议 |
| --- | --- |
| `standard`（安装脚本使用） | TeamX、Node／DSH、模板、glab 和 lark-cli；Git Workspace 仍需自行安装 Git |
| `core` | TeamX、Node／DSH 和模板；供开发与自定义部署，不用于常驻安装脚本 |
| `full` | standard 加完整 Git；当前不在默认正式发行范围，Linux full 仍受 Git TLS 依赖缺项阻断 |

模型服务、API Key、平台账号和开发任务自己的工具链由使用者准备。Scion 运行能力暂不随包提供。DSH 固定为 `0.2.2-rc.1` 预发布版本，精确依赖见包内 `manifest.json`。

归档有 `darwin-arm64`（Apple Silicon Mac）、`darwin-amd64`（Intel Mac）、`linux-amd64` 和 `linux-arm64` 四个构建目标。基线为 macOS 15 或 Ubuntu 24.04／glibc 2.39；常驻部署首期验收目标为 macOS arm64、Linux amd64 standard，其他目标的局部验证不扩大此声明。不支持 Windows 或 Alpine/musl。

系统需要 Bash、curl（联网安装）、tar、SHA-256 工具、证书、进程观察权限及可写私有目录。macOS 的 DSH 工具需要 `/usr/bin/sandbox-exec`；Linux 需要可用的沙箱后端和动态库，具体以 `manifest.json` 的 `qualification_baseline` 为准。安装器不会安装任务工具链或全部系统依赖。

正式 Release 提供全局 `SHA256SUMS` 和逐包摘要，开发快照提供同名 `.tar.gz.sha256`。从可信渠道取得摘要；摘要不代替签名。安装脚本在执行包内程序前自动核对归档摘要，也可先手工核对：

```sh
# macOS
shasum -a 256 teamx-archive.tar.gz
# Linux
sha256sum teamx-archive.tar.gz
```

若已下载独立 `install.sh`，无需手工解压应用归档，直接安装：

```sh
bash ./install.sh --archive /absolute/teamx-standard.tar.gz
```

脚本读取归档旁同名 `.sha256`，也可传 `--sha256 HEX`。开发快照和归档内也包含同一安装器；只有归档时，先用系统 tar 解压取得 `install.sh`，再执行上面的命令并指定原始归档。安装过程无需联网，不执行临时目录里的 `start` 或 `bin/teamx`。保留原归档和摘要供恢复或升级校验。

### macOS 下载隔离

包可能未进行 TeamX Developer ID 签名或 Apple 公证；具体状态见下载页及 `release.json` 的各包 `signing`。上游或 ad-hoc 签名不代表 TeamX 已签名。

安装器从校验后的归档读取引导程序，再写入正式版本目录，不恢复浏览器下载目录的扩展属性；用户无需先运行 `start init` 解除临时包的下载隔离。这不会签名、公证或全局关闭 Gatekeeper。若仍被系统拦截，保留实际提示和报错。

### Linux amd64 的可选 SRT 执行

Linux amd64 standard 包另含固定 SRT `0.0.83` 及完整 npm 依赖，复用包内 Node。默认安装继续使用原生 DSH；个人安装不需要 SRT 辅助程序或 namespace 权限。

显式选择 SRT 时，交付合同包含 **Ubuntu 22.04 amd64／glibc 2.35** 和 **Ubuntu 24.04 amd64／glibc 2.39** 两组宿主依赖。安装器按实际系统选择对应工具，不将新版 Ubuntu 的程序直接用于旧版系统；实际执行与多用户支持仍按下文验收边界判断。宿主须预先准备对应的固定系统包及其依赖；安装器不安装系统软件，也不接受未登记版本或同名替代程序：

Ubuntu 22.04：

```sh
sudo apt-get install bubblewrap=0.6.1-1ubuntu0.3 socat=1.7.4.1-3ubuntu4 ripgrep=13.0.0-2ubuntu0.1 \
  libstdc++6 libgcc-s1 libcap2 libselinux1 libssl3 libwrap0 ca-certificates
```

Ubuntu 24.04：

```sh
sudo apt-get install bubblewrap=0.9.0-1build1 socat=1.8.0.0-4build3 ripgrep=14.1.0-1 \
  libstdc++6 libgcc-s1 libcap2 libselinux1 libpcre2-8-0 libssl3t64 libwrap0 ca-certificates
```

归档的 `share/teamx/srt-runtime.json` 列出这些包的固定 HTTPS 下载地址、包摘要、`/usr/bin/bwrap`、`/usr/bin/socat`、`/usr/bin/rg` 的程序摘要、动态库和内核前置。服务账号必须能创建 user／mount／PID／IPC／network namespaces，并使用 seccomp、procfs；Ubuntu 的 AppArmor、宿主容器或服务管理器也须允许这些操作。保留宿主安全策略；TeamX 不自动修改 sysctl、AppArmor 或容器权限。选中 SRT 后，实际 namespace 检查失败会拒绝启动，不回退原生。

仅在全新安装中选择执行方式，并逐个指定固定内部回调与模型上游的 origin（不带路径、凭据或通配符）：

```sh
bash install.sh --archive /absolute/teamx-archive.tar.gz --execution srt \
  --srt-endpoint http://127.0.0.1:8088 \
  --srt-endpoint https://model.example.com
```

将归档路径和模型地址换成实际值。Linux 安装流程使用专用普通服务账号，并仅为 SRT 生成 `NoNewPrivileges=yes`、空 `CapabilityBoundingSet` 和空 `AmbientCapabilities`。安装器校验宿主程序版本／摘要及 Node、辅助程序的动态库闭包；私有 home 的 `execution/<release>.json` 以 `0600` 保存实际 release 路径、依赖／系统库摘要、私有根与端点，目录为 `0700`。部署描述不含 token，服务启动核对安装选择、release 和端点，拒绝配置不一致。

普通升级保留原执行选择、旧 release、描述和稳定业务路径；升级前校验目标依赖，停服并一致备份后生成新描述。依赖、执行策略或路径变化若不满足原 Session 恢复条件，按已有恢复合同报告并保留证据，不自动轮换、重置或重放。整份备份恢复仍使用原绝对路径；固定宿主程序和系统库由宿主管理，恢复前也须满足原描述摘要。重新安装不能转换执行组合或修改端点授权。

目前这条链路用于交付准备。多用户开放仍受执行、平台及真实认证验收门槛控制；包含 SRT 资源不代表已完成 systemd／HTTPS 实机部署或多用户发布验收。

管理员需要在首次初始化前选定 HTTPS／认证组合时，可提供 `--deployment-config /private/initial.toml`。文件须为 `0600`，仅接受 `[server]` 的监听、访问模式、公开地址、可信代理和时区，以及 `[deployment]` 的模式、认证、资源与 GitLab 选择；工作目录、执行程序、环境变量、模型和凭据仍由各自配置入口管理。例如：

```toml
[server]
listen = "127.0.0.1:18088"
access_mode = "proxy"
public_base_url = "https://alpha.teamx.opencsg.com"
trusted_proxies = ["127.0.0.1"]

[deployment]
mode = "multi-user"

[deployment.authentication]
provider = "opencsg"
allowed_subjects = ["服务端验证得到的用户 UUID"]
```

若要向所有已认证的 OpenCSG 账号开放，可在此节增加 `allow_all_authenticated = true`；启用后，`allowed_subjects` 不再限制其他已认证账号。默认不开放；这一部署配置只改变认证后的准入，各账号仍有独立用户与空间，本地停用和权限边界继续生效。

配合 `--execution srt` 及上述监听端口的 `--srt-endpoint` 使用。正式版本在多用户生产门槛关闭前仍拒绝启动；`dev`／`dev-*` 开发版本允许在全新业务根开展人工验收，仍须通过 Linux、SRT、明确账号准入及完整执行检查，不代表发布支持。首次配置先于个人身份登记写入；中断保留已选组合，重复输入只能与已有设置一致，不能将已有个人 home 改绑为多用户。常规重装／修复可省略该参数，升级始终沿用原配置。OpenCSG 还须信任该公开域名下的 `/v1/access/sso/callback`，并保留动态 `state` 参数；认证服务拒绝回调时，TeamX 不会取得登录身份。

## 首次使用与访问

### 登录并配置模型

打开安装输出的 Server 地址，默认 `http://127.0.0.1:8088`。管理 token 文件路径由安装输出提示，默认是私有 home 的 `auth/admin-token`；在部署机读取该文件，将内容填入网页登录框。Linux 文件属于专用服务账号，读取时需要 sudo。

缺少可用模型时进入 `setup_required`，设置页可用，Room／平台采集／Agent 尚未启动。网页填写协议、Base URL、模型 ID 和 API Key，点击「验证并开始使用」；真实验证通过后才保存并初始化个人助理，失败不发布模型。默认协议为 `openai-completions`，高级设置按上游能力填写。

API Key 保存在服务端私有凭据库，不回显，后续无需 export；已有有效模型不在每次启动时联网重验。在默认「个人助理」私信直接发送需求；团队协作可用左侧「＋ → 新建房间」选择模板。

首次体验可选择「朋友聚餐」，确认开场消息后发送，观察成员讨论并补充需求，无需连接外部平台。会话操作、提及和暂停恢复见[会话使用指南](https://github.com/go-aie/teamx/blob/main/docs/conversations.zh.md)。

### 访问方式

在「设置 → 安全」选择访问模式；模式、端口和 HTTPS 入口保存后须重启 Server：

| 模式 | 如何访问 |
| --- | --- |
| 仅本机 | 默认 loopback，要求登录 |
| 局域网 | 必须登录，通过面板列出的本机私有 IP 和端口访问；HTTP 未加密，仅用于可信网络 |
| HTTPS 代理 | 必须登录；独立配置域名、证书和反向代理，填写唯一 HTTPS 根地址，受管服务保持 loopback |

Linux 远程初次配置可使用 SSH 隧道：`ssh -L 8088:127.0.0.1:8088 HOST`，再访问本机 8088 端口。手机上的 localhost 指手机自身，手机远程使用设备可达的 HTTPS 地址。

HTTPS 代理可参考包内 `share/teamx/service/Caddyfile.example`。配置 `server.public_base_url` 和明确的 `server.trusted_proxies` IP，保留规范 Host，向 loopback 转发单值 `X-Forwarded-Proto: https`。不支持子路径、别名 origin 或转发 Host。证书续期、防火墙和代理由部署者管理，安装命令不修改它们。

可在安全设置更换令牌，或自定义至少 12 个字符的访问密码；常见密码和明显连续、重复组合会被拒绝。请复制保存。更改 token 会撤销浏览器会话，重启 Server 也需重新登录。常驻安装始终要求登录。

token 不放 URL 或浏览器持久存储；浏览器只保留 HttpOnly 会话 Cookie。人工 CLI 使用 `--token-file FILE` 或 `TEAMX_ADMIN_TOKEN_FILE`，不在 argv 中传 token 值。已有实例凭据丢失时，停服后在部署机用 `teamx access init --config <home>/config.toml` 显式修复，保持原所有者 ID；已有损坏文件需先处理，命令不覆盖它。

### 模型与平台的后续设置

| 需要调整 | 入口与生效方式 |
| --- | --- |
| 模型与密钥 | 设置 → 模型；验证保存后按成员执行进度切换，详见下文 |
| 系统时区 | 设置 → 通用；搜索 IANA 时区并保存，立即更新页面时间，Agent 从下一正常回合使用新时区 |
| 访问方式与端口 | 设置 → 安全；保存后重启服务 |
| 数据目录及其他启动配置 | 编辑本次服务实际使用的 home 下 `config.toml`，停服修改后重启；路径见下文 |

默认时区为 `Asia/Shanghai`，所有会话共用。「使用浏览器时区」仅填入草稿，仍需保存。时区改变不改写原始时间；手工修改 `server.time_zone` 需重启，详细行为见[配置参考](https://github.com/go-aie/teamx/blob/main/internal/room/README.zh.md#服务配置)。

新常驻安装的配置分别位于 macOS `~/.teamx/config.toml`、Linux `/var/lib/teamx/config.toml`；已有安装和自定义安装以实际 home 为准。「设置 → 通用 → 存储位置」显示当前实例的主目录；业务数据或团队模板放在主目录之外时，会额外显示对应路径。路径属于 Server 所在设备。仓库内的 `.teamx/config.toml` 仅适用于显式使用该目录的源码运行。数据目录由 `server.data_dir` 指定；修改路径不会搬迁原数据，已有数据迁移应先一致备份。

「设置 → 模型」维护条目和默认引用，成员可跟随默认或固定模型。执行配置或 Key 变化会等待当前回合结束后分别切换，**私有上下文会重置**；成员身份、Workspace 和暂停意图保留。配置保存与全部成员实际生效分别查看，等待或部分失败按原操作显式重试；删除被引用模型须选择替代条目，模板固定引用须先修正。Key 历史版本需随备份保留，以便旧 Session 恢复。

平台账号、成员工具和通知由使用者显式连接和启用。服务使用固定 `HOME/USER/LOGNAME/PATH/LANG`，不加载 shell 配置，不继承终端中 export 的 Key 或代理。平台登录和 CLI 配置属于**实际部署账号**，优先按 [GitLab](https://github.com/go-aie/teamx/blob/main/docs/gitlab.zh.md)、[飞书](https://github.com/go-aie/teamx/blob/main/docs/lark.zh.md)或 [Scion](https://github.com/go-aie/teamx/blob/main/docs/scion.zh.md)指南在网页接入；GitLab 使用 TeamX 管理的 OAuth 授权，不复用操作者的全局 glab 登录。GitLab 回调地址须匹配当前入口，并在原浏览器完成授权。需要出口代理或额外 CA 时先验证系统与工具支持，不直接修改生成 unit 绕过登记校验。

Linux `lark-cli 1.0.99` 使用官方加密文件后端和部署账号私有主密钥；macOS 使用官方 Keychain。Linux 固定账号发送仍受 `account_pinning_unavailable` 阻断；凭据保存不代表自动续期或全部平台动作可用。macOS 重启后的 Keychain 解锁和真实账号续期仍需实测。

### 可选择的工作目录

成员可以通过网页选择服务端已有目录，操作见 [Workspace 使用指南](https://github.com/go-aie/teamx/blob/main/docs/workspace.zh.md#选择已有目录)。在私有 home 的 `config.toml` 中设置允许范围，修改后重启服务：

```toml
[server]
workspace_roots = ["/srv/projects", "/mnt/shared"]
```

未配置时使用服务账号的用户主目录；`workspace_roots = []` 明确关闭外部目录浏览与绑定，默认受管目录仍可用。根必须为服务机器的绝对路径，最多 16 个。根暂时缺失或不可读时，页面显示不可用原因，其他根仍可使用；不会回退到文件系统根。已绑定目录失效时，需要恢复挂载／权限，或在房间 Workspace 总览修复设置。

远端部署需先准备目录并授予服务账号访问权限。容器部署需先把宿主项目挂载进去，例如宿主 `/data/projects` 挂到容器 `/workspace`，配置 `workspace_roots = ["/workspace"]`；页面和 Agent 使用 `/workspace/...`，不能填写容器未挂载的宿主路径。容器内运行 TeamX／DSH 仍需满足系统与沙箱依赖；目录选择功能不会安装依赖或自动挂载。

外部目录不会纳入 TeamX Room 备份或随 Room 删除，需单独备份。允许选择只证明目录当前可进入、可列举，实际文件写权限仍由服务账号和沙箱决定。

## 日常维护

安装后在部署机直接使用 `teamx`。Linux 命令前加 sudo，macOS 不使用 sudo；`teamx service --help` 可查看参数：

```sh
teamx service status
teamx service logs
teamx service logs --lines 200 --follow
teamx service stop
teamx service start
teamx service restart
teamx service disable
teamx service enable
teamx service inspect
teamx service uninstall
```

`inspect` 核对登记；`status` 区分管理器、所有者认证、实例、设置／业务状态和模型操作。端口可连接或公开 health 不能证明业务 ready。平台连接、采集状态、积压、错误和限流恢复在网页查看；不要因单个平台或模型失败反复重启服务。

`stop` 等待数据结清并保持停止，网页随后无法连接，用 `teamx service start` 恢复；它保留下次开机／登录启用状态。`disable` 只禁用后续自动启动，长期停用先 stop 再 disable。`uninstall` 先停止再注销服务，保留 home、全部 releases、登记与 `teamx` 命令入口；使用原归档运行 `bash ./install.sh --repair --archive FILE` 可重新登记服务。不要按进程名称或猜测的 PID 清理服务。

若提示找不到 `teamx`，先新开终端或按安装输出设置 PATH，无需重新安装；也可使用安装输出的命令完整路径。通过其他独立二进制管理时，可显式传 `--server-root /absolute/server-root`。

统一使用 `teamx service logs` 查看最近 100 行，`--lines 200` 调整行数，`--follow` 持续查看，Ctrl-C 退出日志查看。Linux 加 sudo，底层使用 journald，保留策略沿用主机设置；包内 `share/teamx/service/journald.conf.example` 是主机级容量参考，安装不会修改日志策略。macOS 日志在 `~/Library/Logs/TeamX/server.log`，10 MiB 轮换，保留 5 份历史。故障重试有界，耗尽后排查并显式 start；模型／平台降级不是重启理由。留意磁盘余量。

关闭浏览器不会停止 Agent 或后台采集；平台授权成功、通知入 Inbox、助理处理和外部提醒送达分别确认。断线或响应未知时，先查询原操作，不用新 ID 重发。当前没有离线发送队列、Web Push 或原生 App；浏览器与手机的实际测试范围见对应版本说明，手机视口模拟不等于真机验收。

## 备份与升级

### 常驻服务

升级到最新稳定版，无需事先查询版本号：

```sh
teamx service upgrade
```

Linux 命令前加 sudo，macOS 不加 sudo。升级先查询 GitHub 最新正式稳定 Release，固定目标 tag，再下载对应本机 standard 包和同一 tag 的摘要。显示当前与目标版本，校验完成后停服、创建一致备份、切换版本并检查启动。已是目标稳定版本时不下载应用包、不停服；目标比当前稳定版本更旧时拒绝联网降级。下载、校验和备份路径检查失败时尚未停服。

升级会先检查监听地址：属于当前安装的旧服务会自动停止，再启动新版；被其他程序或另一 TeamX 实例占用时，直接提示冲突，保留原服务。认证或安装身份不匹配会及时报错，暂时无法连接等情况才继续等待。若切换后启动失败，保留新版本和备份，先按提示排查端口、状态和日志，再决定继续启动或恢复备份。

升级默认在登记 home 旁的 `<home>-backups` 创建备份父目录，再生成带时间戳的新备份目录。例如 macOS 默认是 `~/.teamx-backups/before-upgrade-<时间戳>`，Linux 默认是 `/var/lib/teamx-backups/before-upgrade-<时间戳>`；自定义 home 以实际输出为准。也可通过 `--backup-dir` 指定新目录，父目录须事先存在且可写。备份位置在停服前和完成后都会显示。

指定版本或使用离线包：

```sh
teamx service upgrade --version vX.Y.Z
teamx service upgrade --archive /absolute/new-standard.tar.gz
```

将 `vX.Y.Z` 替换为实际正式版本。`--version latest` 等同于省略版本；`--archive` 与 `--version` 互斥。离线包须有同名 `.sha256`，或显式提供 `--sha256 HEX`，无需联网；备份默认同上。`teamx service upgrade --help` 查看完整参数与示例。

**旧版 CLI 的过渡升级**：如果帮助中还没有 `--version`，不能直接使用无参数联网升级。在包含新版安装器的 Release 发布后执行：

```sh
curl -fsSL https://github.com/go-aie/teamx/releases/latest/download/install.sh | bash -s -- --upgrade
```

这会使用校验后的新版引导程序显式升级已有安装，并自动备份。自定义安装根时追加原来的 `--server-root`，自定义命令目录时追加 `--bin-dir`，均使用绝对路径；home 和账号从原登记复用。重复运行普通安装命令时，会提前识别旧 CLI 并输出带原路径的过渡命令。离线转交包可执行 `bash ./install-offline.sh --upgrade`，需由包含新版安装器的归档生成。

准备足够空间存放 home、全部 releases 和新归档。单版 standard 展开可能超过 1 GiB，首版保留全部旧版本，不自动回收。单独备份和恢复仍须显式提供安装根与 home 外的**新目录**，父目录已存在。下面继续使用日常维护中的部署账号和 `teamx` 路径约定：

```sh
# 一致备份会停服，完成后保持停止。
teamx service backup \
  --backup-dir /absolute/private-backups/before-maintenance
teamx service start

# 校验新包、停旧、锁住数据、备份、切换 current 并启动认证检查。
teamx service upgrade \
  --archive /absolute/new-standard.tar.gz \
  --backup-dir /absolute/private-backups/before-upgrade

# 恢复同一实例的原绝对路径，同时备份恢复前状态。
teamx service restore \
  --from /absolute/private-backups/before-upgrade \
  --backup-dir /absolute/private-backups/before-restore
```

备份包含整个 home、全部 releases、原 current 和安装登记：覆盖配置、首启状态、托管 Key 历史、模型操作／验证／迁移备份、用户模板、Room／Session／rotation 与 Workspace。发布备份和恢复前均核对文件摘要、模式与内部链接。备份是私密明文材料，目录 0700；自行安排受控介质、加密和保留策略。

此入口只覆盖受管目录，外部 data／teams／token／Runtime 或越界链接会明确拒绝。Workspace 外部依赖、挂载和 Git worktree 外部仓库需单独一致保存。**平台官方凭据位于账号 HOME／Keychain，TeamX home 备份不覆盖它们**，按官方后端另行保存或恢复后重新授权。

恢复要求仍有原登记的同一实例、同一账号与同一绝对路径；保留被替换的 `home.before-restore-*` 和程序版本。灾难后重建机器的账号／路径／登记须另行规划；安装根丢失时，先从备份的 `registration` 恢复匹配登记和全部版本。启动检查失败会保留新选择与备份、停止服务，**不自动切回旧 binary**；数据格式变化时需恢复整份一致备份。

失败输出会说明是否已经请求停服、是否已切换版本/数据，以及状态和日志命令；已有完整备份时提供原路径和可复制的恢复命令。停服后备份失败且尚未切换版本时，排除原因后可用 `teamx service start` 启动原版本；切换后应先检查日志，并按输出恢复一致备份。

升级不强制联网重验已有模型。重新登录后核对模型操作与实际 Session，显式重试中断目标；已完成成员不重复轮换，不自动重放模型验证或未知平台动作。核对实际后续调用后，才确认该实例升级成功。

### 旧归档与开发数据

停止旧进程并等待退出，备份私有 home；新包解压到独立目录，先用新 home 检查，再显式指定原 home。初始化不覆盖旧模板或自动更新已有运行时路径。保留旧包：已有 Room 冻结原 DSH 路径，平台连接也可能引用旧 CLI 绝对路径，不要删除旧包后期待历史 Session 自动恢复。

已有旧模型配置在业务恢复前一次迁移，保留私有 `model-migration-backup/` 和进度；不联网验证，导入条目标为待验证，缺 Key 在设置中补填。中断按原目标续接，冲突时保留受保护的查询与重试入口；旧 Session 保留原执行快照。此有限迁移不等于从其他安装自动导入全部数据。

### Room 输入自动升级

新版本启动会在取得数据目录独占锁后、开放会话和通知采集前校验输入格式。已使用 `room.json` 分段发布根的 Room 直接重开；旧内联历史及旧输入通过既有框架自动转换，保留消息和事件序号、成员身份、清空边界及消费进度。共同存储内核沿用分段磁盘格式，不要求清空会话或手工搬文件。

发生转换的 Room 保留 `input-upgrade.json` 和 `input-upgrade/<id>/backup/`，对应清单在 `input-upgrade/<id>/backup-manifest.json`；未完成时另有 staging。日志显示 Room、备份路径和失败原因。若启动中断，排除磁盘空间或权限问题后，用同一配置重新启动新版本继续；不要删除进度或围栏来绕过校验。当前根中缺失或损坏的正文不会被当作空会话。

这些备份只保存转换前输入，不能代替上面的整实例一致备份。转换后已有新消息、消费或 Session 结果时，先停止服务并保存当前增量，核对权威文件再决定修复或恢复；不要混用不同时间的 Room 根、正文段及成员状态，也不要只切回旧 binary。持久协议见[存储架构](https://github.com/go-aie/teamx/blob/main/docs/storage-architecture.zh.md#自动升级与备份)。

## 发行材料与许可

归档保留 `manifest.json`、`checksums.txt`、`LICENSE`、`THIRD_PARTY_NOTICES/` 与运行时许可。`checksums.txt` 是包内不可变文件的 JSON 清单，包含权限和链接目标。full 的 Git 对应源码与构建材料在 `THIRD_PARTY_NOTICES/git-source/dugite/`。

正式 Release 提供四个平台的 core／standard 共 8 个归档及同名 `.sha256`，另有固定到该版本的 `install.sh`、逐包 `*-verification.json`、`release.json`、全局 `SHA256SUMS`、`LICENSES.tar.gz`、本指南的 `README.zh.md` 和 `release-notes.md`。

`*-verification.json` 记录对应归档已完成的 CI 身份、摘要与原生 smoke 检查。检查使用固定协议 fixture，不代表真实模型、浏览器、平台账号或用户设备测试通过。

`release.json` 记录版本、源码、构建批次、完整矩阵、签名模式和资产摘要。安装脚本也纳入 `SHA256SUMS`，索引另记录其摘要；汇总与发布保持原始归档字节不变。开发快照的报告仅对应自身归档。
