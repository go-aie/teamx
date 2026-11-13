# TeamX 安装与运行

桌面和手机通过浏览器使用 TeamX，Agent 在 Server 所在机器执行。若已有 Server，直接打开它的地址并按提示登录，浏览器所在设备无需下载或安装运行时。

自行部署时，使用与服务机器系统、架构匹配的归档，包内包含 TeamX、固定 Node／DSH 和团队模板，无需另装 Go 或 Node。安装不要求提前配置模型或 export Key；Server 启动后在网页完成模型设置。

| 你要做什么 | 阅读位置 |
| --- | --- |
| 下载并选择包 | [下载与校验](#下载与校验) |
| 让助理在关闭终端后继续运行 | [常驻安装](#常驻安装standard) |
| 只在当前终端体验 | [前台运行](#前台运行) |
| 设置模型、登录和远程访问 | [首次使用与访问](#首次使用与访问) |
| 查日志、启停或卸载 | [日常维护](#日常维护) |
| 备份、升级或恢复 | [备份与升级](#备份与升级) |

当前为开发预览，完整平台矩阵和正式发行验收尚未完成。支持范围以对应归档的发行评估为准；代码或局部测试通过不等于该目标已正式支持。

## 下载与校验

在项目 GitHub 仓库的 Releases 获取已发布版本；开发快照从 **Actions → Package snapshots → 对应运行 → Artifacts** 下载。没有适用快照时，维护者可手动运行该 Workflow，通常选择 `standard`。Artifact 外层压缩包内才是应用 `.tar.gz`、摘要和检查报告，先解开外层。

| 包 | 包含内容与选择建议 |
| --- | --- |
| `standard`（推荐） | TeamX、Node／DSH、模板、glab 和 lark-cli；可安装常驻服务。Git Workspace 仍需自行安装 Git |
| `core` | TeamX、Node／DSH 和模板；用于前台运行，平台 CLI 和 Git 按需另装 |
| `full` | standard 加完整 Git；当前不在默认正式发行范围，Linux full 仍受 Git TLS 依赖缺项阻断 |

模型服务、API Key、平台账号和开发任务自己的工具链由使用者准备。Scion 运行能力暂不随包提供。DSH 固定为 `0.2.2-rc.1` 预发布版本，精确依赖见解压后的 `manifest.json`。

归档有 `darwin-arm64`（Apple Silicon Mac）、`darwin-amd64`（Intel Mac）、`linux-amd64` 和 `linux-arm64` 四个构建目标。基线为 macOS 15 或 Ubuntu 24.04／glibc 2.39；常驻部署首期验收目标为 macOS arm64、Linux amd64 standard，其他目标的局部验证不扩大此声明。不支持 Windows 或 Alpine/musl。

系统需要 shell、常用命令、证书、进程观察权限及可写私有目录。macOS 的 DSH 工具需要 `/usr/bin/sandbox-exec`；Linux 需要可用的沙箱后端和动态库，具体以 `manifest.json` 的 `qualification_baseline` 为准。仅解压归档不会安装系统依赖。

正式 Release 提供全局 `SHA256SUMS` 和逐包摘要，开发快照提供同名 `.tar.gz.sha256`。从可信渠道取得摘要，按系统选择下面一条命令计算归档 SHA-256，与同名文件的记录逐项比较；摘要不代替签名。文件名替换为实际值：

```sh
# macOS
shasum -a 256 teamx-archive.tar.gz
# Linux
sha256sum teamx-archive.tar.gz
```

确认摘要一致后再解压、运行，目录名以实际解压结果为准：

```sh
tar -xzf teamx-archive.tar.gz
cd teamx-archive
```

保留原归档和摘要供安装或升级命令校验。

### macOS 下载隔离

包可能未进行 TeamX Developer ID 签名或 Apple 公证；具体状态见下载页及 `release.json` 的 `signing`。上游或 ad-hoc 签名不代表 TeamX 已签名。

首次运行使用系统 shell 调用 `start`。脚本发现下载隔离标记后，会显示包目录；核对来源后明确输入 `yes` 才会解除该目录及附带工具的隔离，其他输入取消且不执行包内程序。这不会签名、公证或全局关闭 Gatekeeper。目录须可读写；无终端输入或 `--non-interactive` 不会自动同意。若仍被系统拦截，按实际提示处理并保留报错。

常驻安装前，在临时解压目录运行 `/bin/sh ./start init` 完成此准备。它只写临时包的基础文件，不启动 Server，不配置模型；正式 home 仍由安装命令创建。新下载版本用于升级前也先完成此步骤。前台体验直接使用下文的 `/bin/sh ./start`，会经过相同检查。

## 常驻安装（standard）

使用本机架构的 standard 归档。安装命令校验完整包，部署到版本目录，准备独立 home，并登记、启用和启动系统服务；无需先以前台方式运行 `./start`。安装不覆盖已有身份、模型或用户模板，也不自动导入其他 bundle 或开发目录的数据。

### macOS

完成下载隔离准备后，在已登录普通用户的终端执行，不使用 sudo：

```sh
/absolute/unpacked/bin/teamx service install \
  --archive /absolute/teamx-darwin-arm64-standard.tar.gz
```

默认安装根为 `~/Library/Application Support/TeamX/server`，私有 home 为 `~/Library/Application Support/TeamX/home`。LaunchAgent 位于 `~/Library/LaunchAgents/com.teamx.server.plist`，登录后启动。用户注销、电脑休眠或关机期间不保证执行。

可通过 `--server-root`、`--home` 选择互不重叠的绝对目录，后续管理须使用相同安装根。这里的 `~` 仅用于说明用户目录，配置文件不展开 `~` 或变量。

### Linux

需要 systemd。管理员先创建专用普通账号，保留真实可写的账号 HOME，不将它改成 TeamX 数据根；账号已存在时跳过创建：

```sh
sudo useradd --system --create-home --home-dir /home/teamx --shell /usr/sbin/nologin teamx
sudo /absolute/unpacked/bin/teamx service install \
  --archive /absolute/teamx-linux-amd64-standard.tar.gz \
  --server-root /opt/teamx --home /var/lib/teamx --user teamx
sudo /opt/teamx/current/bin/teamx service status --server-root /opt/teamx
```

安装及管理以 root 执行，初始化和 Server 以 `--user` 普通账号执行。unit 固定为 `/etc/systemd/system/teamx.service`，不覆盖其他安装的同名服务。系统服务不依赖 SSH 登录；实际机器上的退出 SSH、开机运行及子进程清理仍需核对验收范围。

安装默认读取归档旁同名 `.sha256`，也可传 `--sha256 HEX`。新 home 权限为 0700，凭据为私有文件；已有目录不递归改属主。不从临时解压目录启动持久 Room，安装后使用 `<server-root>/current/bin/teamx` 作为稳定管理入口。

## 前台运行

不安装系统服务时，在解压目录执行：

```sh
/bin/sh ./start
```

该命令准备缺失基础文件并启动 Server，需要保持进程运行；按 Ctrl-C 正常停止，等待清理完成后再启动。默认 home 为包内 `.teamx/`，配置、用户模板和数据保存在其中。也可选择独立 home 或避开端口冲突：

```sh
/bin/sh ./start --home "$HOME/.teamx"
/bin/sh ./start --home /absolute/private-home --listen 127.0.0.1:8090
```

这两个示例是替代用法，不要同时运行。同一 home 只允许一个服务实例。可从其他目录通过绝对路径调用 `start`；外部 home 可写且完成 macOS 隔离处理后，包本体可只读。

`./start init` 只初始化文件后退出，是可选步骤；`./start doctor` 诊断依赖，不发送模型请求。

## 首次使用与访问

### 登录并配置模型

打开 Server 地址，默认 `http://127.0.0.1:8088`。常驻安装必须登录，管理 token 文件路径由安装输出提示，默认是私有 home 的 `auth/admin-token`。普通前台新 home 默认仅本机免登录；已有实例保持原认证状态。

缺少可用模型时进入 `setup_required`，设置页可用，Room／平台采集／Agent 尚未启动。网页填写协议、Base URL、模型 ID 和 API Key，点击「验证并开始使用」；真实验证通过后才保存并初始化个人助理，失败不发布模型。默认协议为 `openai-completions`，高级设置按上游能力填写。

API Key 保存在服务端私有凭据库，不回显，后续无需 export；已有有效模型不在每次启动时联网重验。在默认「个人助理」私信直接发送需求；团队协作可用左侧「＋ → 新建房间」选择模板。

### 访问方式

在「设置 → 安全」选择访问模式；模式、端口和 HTTPS 入口保存后须重启 Server：

| 模式 | 如何访问 |
| --- | --- |
| 仅本机 | 默认 loopback；常驻安装要求登录，普通新 home 可免登录 |
| 局域网 | 必须登录，通过面板列出的本机私有 IP 和端口访问；HTTP 未加密，仅用于可信网络 |
| HTTPS 代理 | 必须登录；独立配置域名、证书和反向代理，填写唯一 HTTPS 根地址，受管服务保持 loopback |

Linux 远程初次配置可使用 SSH 隧道：`ssh -L 8088:127.0.0.1:8088 HOST`，再访问本机 8088 端口。手机上的 localhost 指手机自身，手机远程使用设备可达的 HTTPS 地址。

HTTPS 代理可参考包内 `share/teamx/service/Caddyfile.example`。配置 `server.public_base_url` 和明确的 `server.trusted_proxies` IP，保留规范 Host，向 loopback 转发单值 `X-Forwarded-Proto: https`。不支持子路径、别名 origin 或转发 Host。证书续期、防火墙和代理由部署者管理，安装命令不修改它们。

普通本机实例可在安全设置开启登录，生成令牌或自定义至少 12 个字符的访问密码；常见密码和明显连续、重复组合会被拒绝。请复制保存。更改 token 会撤销浏览器会话；开启登录后，重启 Server 需重新登录。关闭远程入口后须先重启回到本机模式，再关闭普通实例的登录验证。常驻安装不能关闭登录。

token 不放 URL 或浏览器持久存储；浏览器只保留 HttpOnly 会话 Cookie。人工 CLI 使用 `--token-file FILE` 或 `TEAMX_ADMIN_TOKEN_FILE`，不在 argv 中传 token 值。已有实例凭据丢失时，停服后在部署机用 `teamx access init --config <home>/config.toml` 显式修复，保持原所有者 ID；已有损坏文件需先处理，命令不覆盖它。

### 模型与平台的后续设置

「设置 → 模型」维护条目和默认引用，成员可跟随默认或固定模型。执行配置或 Key 变化会等待当前回合结束后分别切换，**私有上下文会重置**；成员身份、Workspace 和暂停意图保留。配置保存与全部成员实际生效分别查看，等待或部分失败按原操作显式重试；删除被引用模型须选择替代条目，模板固定引用须先修正。Key 历史版本需随备份保留，以便旧 Session 恢复。

平台账号、成员工具和通知由使用者显式连接和启用。服务使用固定 `HOME/USER/LOGNAME/PATH/LANG`，不加载 shell 配置，不继承终端中 export 的 Key 或代理。平台登录应由**实际部署账号**使用包内固定 CLI 完成，先查看 `current/bin/lark-cli auth --help`／`current/bin/glab auth --help`，也可使用网页接入引导。GitLab 回调地址须匹配当前入口，并在原浏览器完成授权。需要出口代理或额外 CA 时先验证系统与工具支持，不直接修改生成 unit 绕过登记校验。

Linux `lark-cli 1.0.99` 使用官方加密文件后端和部署账号私有主密钥；macOS 使用官方 Keychain。Linux 固定账号发送仍受 `account_pinning_unavailable` 阻断；凭据保存不代表自动续期或全部平台动作可用。macOS 重启后的 Keychain 解锁和真实账号续期仍需实测。

## 日常维护

常驻安装的管理命令在部署机运行。下文 `teamx` 均替换为 `<server-root>/current/bin/teamx` 的实际绝对路径；macOS 路径含空格时用引号。Linux 命令前加 sudo，macOS 不使用 sudo：

```sh
teamx service inspect --server-root /absolute/server-root
teamx service status --server-root /absolute/server-root
teamx service stop --server-root /absolute/server-root
teamx service start --server-root /absolute/server-root
teamx service restart --server-root /absolute/server-root
teamx service disable --server-root /absolute/server-root
teamx service enable --server-root /absolute/server-root
teamx service uninstall --server-root /absolute/server-root
```

`inspect` 核对登记；`status` 区分管理器、所有者认证、实例、设置／业务状态和模型操作。端口可连接或公开 health 不能证明业务 ready。平台连接、采集状态、积压、错误和限流恢复在网页查看；不要因单个平台或模型失败反复重启服务。

`stop` 等待数据结清并保持停止，保留下次开机／登录启用状态；`disable` 只禁用后续自动启动，长期停用先 stop 再 disable。`uninstall` 先停止再注销服务，保留 home、全部 releases 和登记，同一归档重新安装可恢复登记。不要按进程名称或猜测的 PID 清理服务。

Linux 日志使用 `sudo journalctl -u teamx.service -n 100`，保留策略沿用主机 journald；包内 `share/teamx/service/journald.conf.example` 是主机级容量参考，安装不会修改日志策略。macOS 日志在 `~/Library/Logs/TeamX/server.log`，10 MiB 轮换，保留 5 份历史。故障重试有界，耗尽后排查并显式 start；模型／平台降级不是重启理由。留意磁盘余量。

关闭浏览器不会停止 Agent 或后台采集；平台授权成功、通知入 Inbox、助理处理和外部提醒送达分别确认。断线或响应未知时，先查询原操作，不用新 ID 重发。当前没有离线发送队列、Web Push 或原生 App；浏览器实际支持项见该归档外部评估的 `supported_browsers`／`pending_browsers`，手机视口模拟不等于真机验收。

## 备份与升级

### 常驻服务

准备足够空间存放 home、全部 releases 和新归档。单版 standard 展开可能超过 1 GiB，首版保留全部旧版本，不自动回收。备份目标须是安装根与 home 外的**新目录**，父目录已存在。下面继续使用日常维护中的部署账号和 `teamx` 路径约定：

```sh
# 一致备份会停服，完成后保持停止。
teamx service backup --server-root /absolute/server-root \
  --backup-dir /absolute/private-backups/before-maintenance
teamx service start --server-root /absolute/server-root

# 校验新包、停旧、锁住数据、备份、切换 current 并启动认证检查。
teamx service upgrade --server-root /absolute/server-root \
  --archive /absolute/new-standard.tar.gz \
  --backup-dir /absolute/private-backups/before-upgrade

# 恢复同一实例的原绝对路径，同时备份恢复前状态。
teamx service restore --server-root /absolute/server-root \
  --from /absolute/private-backups/before-upgrade \
  --backup-dir /absolute/private-backups/before-restore
```

备份包含整个 home、全部 releases、原 current 和安装登记：覆盖配置、首启状态、托管 Key 历史、模型操作／验证／迁移备份、用户模板、Room／Session／rotation 与 Workspace。发布备份和恢复前均核对文件摘要、模式与内部链接。备份是私密明文材料，目录 0700；自行安排受控介质、加密和保留策略。

此入口只覆盖受管目录，外部 data／teams／token／Runtime 或越界链接会明确拒绝。Workspace 外部依赖、挂载和 Git worktree 外部仓库需单独一致保存。**平台官方凭据位于账号 HOME／Keychain，TeamX home 备份不覆盖它们**，按官方后端另行保存或恢复后重新授权。

恢复要求仍有原登记的同一实例、同一账号与同一绝对路径；保留被替换的 `home.before-restore-*` 和程序版本。灾难后重建机器的账号／路径／登记须另行规划；安装根丢失时，先从备份的 `registration` 恢复匹配登记和全部版本。启动检查失败会保留新选择与备份、停止服务，**不自动切回旧 binary**；数据格式变化时需恢复整份一致备份。

升级不强制联网重验已有模型。重新登录后核对模型操作与实际 Session，显式重试中断目标；已完成成员不重复轮换，不自动重放模型验证或未知平台动作。核对实际后续调用后，才确认该实例升级成功。

### 前台归档与旧数据

停止旧进程并等待退出，备份私有 home；新包解压到独立目录，先用新 home 检查，再显式指定原 home。初始化不覆盖旧模板或自动更新已有运行时路径。保留旧包：已有 Room 冻结原 DSH 路径，平台连接也可能引用旧 CLI 绝对路径，不要删除旧包后期待历史 Session 自动恢复。

已有旧模型配置在业务恢复前一次迁移，保留私有 `model-migration-backup/` 和进度；不联网验证，导入条目标为待验证，缺 Key 在设置中补填。中断按原目标续接，冲突时保留受保护的查询与重试入口；旧 Session 保留原执行快照。此有限迁移不等于从其他安装自动导入全部数据。

## 发行材料与许可

归档保留 `manifest.json`、`checksums.txt`、`LICENSE`、`THIRD_PARTY_NOTICES/` 与运行时许可。`checksums.txt` 是包内不可变文件的 JSON 清单，包含权限和链接目标。full 的 Git 对应源码与构建材料在 `THIRD_PARTY_NOTICES/git-source/dugite/`。

正式 Release 另有 `release.json`、逐包 `*-assessment.json`、全局 `SHA256SUMS` 和 `LICENSES.tar.gz`。资格绑定同一归档摘要，包内 manifest 保留构建时状态，不为更新资格重打包；开发快照不能沿用其他归档的报告。
