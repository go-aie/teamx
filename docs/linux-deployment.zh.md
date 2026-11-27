# Linux 打包、部署与升级

本文供维护者准备 Linux 服务器和执行后续运维。通用安装参数见[安装与运行](installation.zh.md)，发布流程见[正式发行指南](../packaging/releasing.zh.md)，本轮实机结果与未完成项见[空间部署验收记录](implementation/space-deployment.zh.md#阶段交接2026-11-25)。

**当前交付的是开发验收环境。** `linux-amd64 standard` 的原生打包、systemd、HTTPS、OpenCSG 登录、首次模型验证与同模式升级已有实机证据；S5a 正式出口、最终生产候选及所选前置验收尚未关闭。正式 `release` 构建仍拒绝多用户启动。下面给出服务器操作路径；多用户部分当前用于明确选择的 `dev`／`dev-*` 验收包，不能通过改版本名或配置绕过生产门槛。

## 1. 构建输入与目标机

本手册的隔离组合是 **Linux amd64 + standard + SRT**。发行矩阵默认 Linux 基线仍为 Ubuntu 24.04／glibc 2.39；SRT 合同另包含 Ubuntu 22.04／glibc 2.35，已在本轮 Ubuntu 22.04.5 原生 amd64 主机验证。不要将这一结果推广为其他发行版、Alpine/musl 或 Linux ARM64 的多用户验收通过。

区分两台机器的要求：

| 机器 | 要求 |
| --- | --- |
| 构建机 | 匹配的原生 Linux amd64 环境；Go 1.27.1、Node（本轮为 24.22.2）、Git、curl、tar、xz；足够的模块／npm 缓存和磁盘空间 |
| 运行机 | 所选 Ubuntu／glibc 基线、systemd；安装管理使用 sudo，核心以专用普通账号执行；运行完整归档无需预装 Go 或 Node |
| 选择 SRT 的运行机 | 固定宿主工具和动态库、服务账号可用的 user／mount／PID／IPC／network namespace、seccomp、procfs；实际检查失败拒绝启动，不降级原生 |

构建和运行可以使用不同主机，运行机仍须满足产物及执行合同。构建过程不从构建机复制系统库来补齐任意目标系统。不要在 macOS 上仅设置 `GOOS` 或 `--target` 就把交叉编译的单个二进制当作完整 Linux 分发包。

先将本阶段要交付的**公开实现及固定依赖文件**完整纳入源码提交，再从该提交构建。当前工作区含未提交／未跟踪实现，不能认为主仓 HEAD 已包含当前 alpha 的全部代码。alpha 的包来自独立验收快照仓库，不是正式 tag。构建树不复制 `.teamx/`、`.env`、运行数据、模型 Key 或平台登录资料。

## 2. 原生打包与校验

以下命令在 Linux amd64 的完整源码目录执行，普通构建账号须能写入示例目录。替换输出路径，使用新的输出目录，构建期间不改源码：

```sh
TEAMX_BUILD_ROOT=/srv/teamx-build
TEAMX_BUILD_OUTPUT="$TEAMX_BUILD_ROOT/dist/$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$TEAMX_BUILD_ROOT/cache" "$TEAMX_BUILD_ROOT/dist"

./scripts/package.sh --channel dev --target linux-amd64 --profile standard \
  --cache-dir "$TEAMX_BUILD_ROOT/cache" --output-dir "$TEAMX_BUILD_OUTPUT"
```

成功产物包括 `.tar.gz`、同名 `.sha256` 和 `-report.json`。开发版本会标记源码 commit／dirty 状态。完整归档包含 TeamX、包内 Node／DSH、团队模板、glab、lark-cli 及这个目标的 SRT 资源；不包含 Git 完整运行时或私有账号。

缓存齐全后可追加 `--offline`。首次准备缓存使用正常联网构建；离线模式缺少固定归档、目标 npm 缓存或 Go 模块时明确失败，不会自行换成其他版本。Go 缓存可通过标准 `GOMODCACHE`／`GOCACHE` 配置到容量足够的磁盘。

将同批次归档、同名摘要和报告传到运行机，例如 `/srv/teamx-delivery`，保留原文件名；不要混用不同构建的文件。将下列文件名替换为实际产物，校验时位于归档目录：

```sh
cd /srv/teamx-delivery
sha256sum -c teamx-dev-COMMIT-linux-amd64-standard.tar.gz.sha256
```

同时核对可信构建记录中的摘要、源码版本、目标／包型和报告 `status`。同一目录里的归档与摘要都被替换时，二者相符不能证明来源可信。需要独立重跑归档检查时，在源码目录执行：

```sh
./scripts/package-smoke.sh /srv/teamx-delivery/teamx-dev-COMMIT-linux-amd64-standard.tar.gz \
  --report /srv/teamx-delivery/recheck.json
```

归档 smoke 覆盖 inventory／权限／依赖、初始化和受控 ACP fixture；不代表真实模型、浏览器、SSO、平台账号或完整备份恢复已通过。

正式版本仅在所选发布门槛关闭、源码已纳入提交、工作区干净、`vX.Y.Z` tag 精确指向 HEAD 后构建：

```sh
./scripts/package.sh --channel release --version vX.Y.Z \
  --target linux-amd64 --profile standard \
  --cache-dir /srv/teamx-build/cache --output-dir /srv/teamx-build/dist/vX.Y.Z
```

此命令不创建 GitHub Release。推送 tag 会触发公开发布，按[正式发行指南](../packaging/releasing.zh.md)单独操作；本文不要求为验收推送 tag。当前多用户生产门槛仍关闭，成功组包也不能使正式版本支持多用户。

## 3. 新运行机准备

下面使用 `/opt/teamx` 存放程序、`/var/lib/teamx` 存放私有配置与数据、`/usr/local/bin/teamx` 作为稳定命令。三者用途不同，安装根与 home 不得互相包含。既有安装沿用登记路径；不要直接套用新装命令转换个人数据或抢占另一实例的 `teamx.service`。

选择 SRT 时，按宿主安装**同一组**固定依赖：

```sh
# Ubuntu 22.04 amd64
sudo apt-get install bubblewrap=0.6.1-1ubuntu0.3 socat=1.7.4.1-3ubuntu4 ripgrep=13.0.0-2ubuntu0.1 \
  libstdc++6 libgcc-s1 libcap2 libselinux1 libpcre2-8-0 libssl3 libwrap0 ca-certificates

# Ubuntu 24.04 amd64：仅在这一宿主使用此组
sudo apt-get install bubblewrap=0.9.0-1build1 socat=1.8.0.0-4build3 ripgrep=14.1.0-1 \
  libstdc++6 libgcc-s1 libcap2 libselinux1 libpcre2-8-0 libssl3t64 libwrap0 ca-certificates
```

对应合同为 [runtime.lock.json](../packaging/srt/runtime.lock.json)，归档中是 `share/teamx/srt-runtime.json`。它包含固定 `.deb` 来源／摘要、程序摘要、库和内核要求。软件源找不到固定版本时，使用合同中的可信包和摘要准备依赖，不改装 `latest` 或跳过检查。不要同时执行两组安装命令。

系统库更新可能使既有执行描述的摘要失效；升级宿主库前也要安排停服、备份和恢复验证。不要通过降低 namespace、capabilities 或路径检查使启动通过。

规划域名、DNS 与 HTTPS 代理。TeamX 保持 loopback 监听，公开 80／443 与证书由代理管理员配置；安装器不修改 DNS、Caddy 或防火墙。

## 4. 首次配置与安装

在目标机创建仅管理员可读的 `/srv/teamx-secrets/initial.toml`，目录 `0700`、文件 `0600`。多用户开发验收示例：

```sh
sudo install -d -m 700 /srv/teamx-secrets
sudoedit /srv/teamx-secrets/initial.toml
sudo chmod 600 /srv/teamx-secrets/initial.toml
```

```toml
[server]
listen = "127.0.0.1:8088"
access_mode = "proxy"
public_base_url = "https://assistant.example.com"
trusted_proxies = ["127.0.0.1"]

[deployment]
mode = "multi-user"

[deployment.authentication]
provider = "opencsg"
issuer = "https://iam.opencsg.com"
client_id = "REPLACE_WITH_TEAMX_CLIENT_ID"
client_secret_file = "/srv/teamx-opencsg/client-secret"
hub_origin = "https://hub.opencsg.com"
allowed_subjects = ["REPLACE_WITH_VERIFIED_SUBJECT_UUID"]
# 明确向所有通过所选认证源验证的账号开放时启用：
# allow_all_authenticated = true
```

域名、端口、OpenCSG IAM 应用和稳定 subject 必须换成实际值，具体要求见 [OpenCSG 登录](opencsg-login.zh.md)。安装前准备服务账号可读的私有 Secret 文件及目录，文件权限为 `0600`；示例放在新 Home 之外以便首次安装读取，此外部凭据需单独备份。subject 来自受保护身份验证，不是昵称。默认仅名单准入；显式开启 `allow_all_authenticated` 后，其他已认证账号也可注册，每人获得独立默认空间。

当前源码的 OpenCSG 多用户部署在登录时取得**该用户本人的默认 AI Gateway API Key**，为空模型目录自动配置 `https://ai.space.opencsg.com/v1`、`deepseek-v4.1-flash-1017`，完成文本与工具调用验证后初始化助理。验证和聊天均使用本人的 Key、消耗本人的额度，不使用部署账号或其他用户的 Key。已有但尚未配置模型的空间也会在下次登录后自动初始化；验证失败可在原模型配置页面重试。用户随后通过原有模型设置自行修改，其他用户的配置不受影响；后续登录不会覆盖已有模型。个人版仍由用户配置第一个模型。此能力需重新构建部署，旧 alpha 包不包含此改动。

内置默认模型使用文本和图片输入、`context_window = 1048576`、`max_tokens = 32768`、`reasoning_effort = "off"`，并以 system 兼容不支持 developer 的网关。这些参数随新用户和新空间的默认模型一同初始化；已有模型通过模型设置管理，服务启动不会自动改写。

多个空间同时验证时，会有界等待共享 DSH 验证槽位；仅重试尚未开始执行的 `admission/busy`，不自动重放已进入执行或清理失败的验证。

个人部署使用 `mode = "personal"`，删除整个 `[deployment.authentication]` 节；可省略下面的 SRT 参数以使用默认 native。个人与多用户必须使用各自的新业务根，修改模式不会迁移旧数据。

使用**已校验归档内的安装器**。以下命令中的归档名替换为实际值，且同名 `.sha256` 留在旁边：

```sh
TEAMX_ARCHIVE='/srv/teamx-delivery/teamx-dev-COMMIT-linux-amd64-standard.tar.gz'
TEAMX_ARCHIVE_NAME=$(basename "$TEAMX_ARCHIVE" .tar.gz)
tar -xOf "$TEAMX_ARCHIVE" "$TEAMX_ARCHIVE_NAME/install.sh" > /srv/teamx-delivery/install.sh

sudo bash /srv/teamx-delivery/install.sh --archive "$TEAMX_ARCHIVE" \
  --server-root /opt/teamx --home /var/lib/teamx --bin-dir /usr/local/bin \
  --user teamx --deployment-config /srv/teamx-secrets/initial.toml \
  --execution srt \
  --srt-endpoint http://127.0.0.1:8088 \
  --srt-endpoint https://ai.space.opencsg.com
```

多用户必须选择 SRT。`--srt-endpoint` 是精确 origin，须包含固定内部回调和所有计划使用的模型上游；模型 UI 中的 Base URL 可以包含 `/v1`，此参数不带路径、凭据或通配符。安装后不能靠重装／普通升级扩展执行方式与端点授权，需要另行规划受控变更。

安装器创建或复用专用普通账号，校验完整包、宿主依赖和真实 namespace，准备私有 home、安装登记、执行描述与 systemd，并启动认证检查。无需先运行 `start init`；不要以 root 长期运行 `serve`。同一稳定命令和固定服务名只管理一个登记实例。

## 5. HTTPS 与 OpenCSG

宿主原生 Caddy 示例（替换域名与端口，保留其他站点）：

```caddyfile
assistant.example.com {
    reverse_proxy 127.0.0.1:8088 {
        header_up -X-Forwarded-Host
        header_up -Forwarded
    }
}
```

```sh
sudo caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

保留公开 Host，代理传递 HTTPS 信息；TeamX 仅信任配置中的代理来源。若 Caddy 在容器里，`127.0.0.1` 指向容器自身，须另外设计容器到宿主 loopback 的受控上游。当前 alpha 的 bridge／socket 转发是该主机专用配置，不应照搬到另一台机器。

OpenCSG 多用户部署统一使用 OpenCSG IAM 独立应用，登记准确回调 `https://assistant.example.com/v1/access/sso/callback`，保留动态 `state` 参数。登录路径为 TeamX → OpenCSG IAM → TeamX；配置 `hub_origin` 后取得本人模型 Key。个人版登录不受此项影响，其他用户管理方式继续通过认证 Provider 扩展。部署配置与身份目录统一使用 `opencsg`，启动时校验 provider／issuer 一致性，不自动改写身份。日常升级沿用原 Home、用户、空间和 Secret 路径。不要把聊天中的旧授权链接或回调 token 当作可复用登录入口。

安装后检查：

```sh
sudo /usr/local/bin/teamx service status --server-root /opt/teamx
sudo /usr/local/bin/teamx service logs --server-root /opt/teamx --lines 100
systemctl is-active teamx.service
systemctl is-enabled teamx.service
curl -fsS https://assistant.example.com/v1/access/method
```

`service status` 应认证到正确 installation ID；多用户控制面 `ready` 表示可以登录，不表示每个空间已配置模型。真实浏览器还需完成 OpenCSG 回调、本人空间的模型文本／工具验证，再检查消息、空间与业务权限。多用户部署 token 仅用于本机管理，不能填入网页当作业务用户登录。

## 6. 升级、备份与恢复

飞书平台程序随接收调用的控制面绑定到物理 release，升级不再要求连接中记录的历史 executable 与新 release 相同。原连接和 Space 私有凭据保持原样；管理员覆盖使用服务启动环境的 `TEAMX_LARK_BIN` 绝对路径，修改后需重启。完整安装中的程序缺失不会选择 PATH 中的其他版本。旧 Space／Session 的 DSH、Agent CLI、执行描述及 release 留存规则继续独立生效。

GitLab CLI 同样使用控制面绑定的 `teamx-gitlab` 与其同目录 `glab`；管理员覆盖分别为 `TEAMX_GITLAB_BIN`、`TEAMX_GLAB_BIN`，必须使用规范化绝对路径并重启服务。可用性检查与执行共享固定的原生路径，安装缺失或覆盖错误不回退到 PATH。CLI 故障只影响外部工具，HTTP 通知、账号核验与内置评论／回复继续使用原账号及权限。

Scion 的程序分离已接通个人服务的核验、工具和 Workspace，但 Linux 多用户准入继续拒绝。此次不因程序修复开放 Scion／仓库执行，也不改变旧 Session 的程序引用。

恢复时先只读检查程序、原账号与权限，再观察原通知查询；采集仍可能等待原退避到期。若接入记录只有工具未完成，读取原 operation 并继续该步骤，不重建连接、订阅或重走 OAuth。`unknown/submission_unconfirmed` 保持原结果核对规则。[程序绑定的 3A 产物验收](implementation/platform-program-dependencies.zh.md#linux-安装产物验收3a)已固定候选归档与 SHA-256；[本轮 3B](implementation/platform-program-dependencies.zh.md#目标-linux-服务恢复3b)使用同一份包完成原生 Ubuntu 22.04／glibc 2.35 的 systemd 升级，原通知和成员工具恢复已确认。用户选择成员“开启工具”恢复，原 onboarding 未续跑；实际刷新、第三方写入及未检查的旧 Session 不计作通过。


先确认运行版本、目标包摘要、原登记路径和足够磁盘空间。新备份使用版本 3：`snapshot.tar` 保存整个 home、安装登记等可变信息及可用的服务单元，`backup.json` 记录当前版本和配置／Space／Room／Session 所需程序归档的 SHA-256、大小与原 release 名称。程序文件不再写入数据快照，也不生成逐文件摘要清单。数据归档流式写入、同步落盘并顺序回读校验。

程序原归档在安装时校验并登记到 `<server_root>/programs/`；旧 release 首次使用时，在停服前校验并补登记一次。备份父目录的 `.teamx-programs/<实例摘要>/` 保存只读程序对象，各备份通过自身 `programs/<SHA-256>.tar.gz` 的硬链接持有对象。相同文件系统内不重复存储程序字节；安装目录与备份跨文件系统时，首次向备份侧入库会复制并校验归档，后续仍复用。备份侧文件系统须支持硬链接，能力不满足会在停服前拒绝。

每份备份拥有完整恢复所需的程序对象，不依赖构建缓存、已安装 releases 或共享池路径。搬运／异地保存时复制整个备份目录，保留 `programs/` 内实际文件内容和只读权限，不只复制 `snapshot.tar` 与清单；恢复仍遵循下面的同实例、同账号、原绝对路径约束。程序存储目录仅允许维护账号访问，程序归档为 `0400`，禁止原地修改；备份路径只检查引用和文件元数据，恢复时完整校验程序归档及其展开清单，后台留存清理也会顺序校验保留备份的程序归档，同一 inode 在一次清理中只读取一次。

备份父目录须存在，目标目录须全新，且在安装根／home 之外：

```sh
sudo install -d -m 700 /srv/teamx-backups
sudo /usr/local/bin/teamx service upgrade --server-root /opt/teamx \
  --archive /srv/teamx-delivery/new-standard.tar.gz \
  --backup-dir /srv/teamx-backups/before-upgrade-UNIQUE
```

升级复用登记的账号、home、执行方式和端点；不再传首次 `--deployment-config`、`--execution` 或 `--srt-endpoint`。它在旧服务继续运行时校验目标包、准备执行环境及程序归档，再停服、结清、取得锁、重新确认依赖引用、一致备份、提交新描述、切换 `current` 并启动认证检查。停服后的路径不临时装配程序；引用变化导致依赖未准备时明确失败并保留原选择。仅使用受信任且已验收的对应版本；生产机建议固定离线包与摘要，避免无意追随 `latest`。

托管已有 Space 保留其固定旧 release 的 DSH、原执行描述和 CLI；新 Space 使用当前 release。备份和自动清理均保护已有配置、归档 Space、Room 和冻结 Session 引用的版本；被引用版本可使总数超过保留数。不要手工删除旧 releases／`home/execution`，或编辑模型收据／成员快照来消除运行指纹不匹配。旧依赖缺失或恢复条件不符时，应保留现场并明确处理，不重置 Session 或重放业务。

旧的逐文件清单备份（版本 1）和包含完整程序的流式归档备份（版本 2）仍可校验和恢复，旧清单保留 512 MiB 有界读取上限。使用新包内安装器才能采用程序／数据分离备份；直接调用旧 CLI 仍使用旧版本逻辑。恢复继续要求相同实例、相同绝对路径，并保留恢复前数据及已有版本。

升级后的检查包括：正确安装身份、原 User／Space／Room／成员身份、原模型收据与运行指纹、原 Session 能否恢复、有效浏览器会话、进行中操作状态，以及真实模型与所选平台。自然过期的登录会话应重新登录；控制面 ready 和归档 smoke 都不能替代这些业务检查。当前阶段尚未完成完整业务 Session 升级／故障恢复及备份还原验收。

单独备份与原路径恢复：

```sh
# 备份完成后保持停止；确认成功后再显式启动。
sudo /usr/local/bin/teamx service backup --server-root /opt/teamx \
  --backup-dir /srv/teamx-backups/manual-UNIQUE
sudo /usr/local/bin/teamx service start --server-root /opt/teamx

# 恢复前先保留当前状态到另一个新目录。
sudo /usr/local/bin/teamx service restore --server-root /opt/teamx \
  --from /srv/teamx-backups/before-upgrade-UNIQUE \
  --backup-dir /srv/teamx-backups/before-restore-UNIQUE
```

恢复要求同一安装登记、账号和**原绝对路径**；完整 home／releases、私有配置／历史 Key、执行描述及宿主依赖须匹配。不是将旧个人数据改成多用户，也不是任意换机器／换 UID 的迁移命令。

失败时先读命令输出、`service status` 和 `service logs`：尚未切版本的备份失败可在排除原因后启动原版本；已经切版本或数据则依据完整一致备份恢复。部署命令不会自动切回旧 binary；不要只改 `current` 软链接来回滚数据格式。平台授权／刷新和不兼容模型操作仍按各自状态处理。

## 7. 本轮 alpha 留存环境

2026-11-25 交接时已保留多用户开发验收环境，包含 HTTPS／OpenCSG、普通账号 systemd、SRT、原个人数据与一致备份。已完成与未完成范围见[阶段交接](implementation/space-deployment.zh.md#阶段交接2026-11-25)。

实际 SSH 主机／IP、域名、内部代理地址、安装／数据／备份目录和工具链路径仅保存在本地被 Git 忽略的文件中：

- `.local/linux-environment.md`：实际访问入口、路径表、只读状态／日志命令及部署命令。
- `.local/deploy-linux.json`：当前主机的实际部署参数，不含 SSH host／port。
- `.local/space-deployment-private.zh.md`：脱敏前的验收记录，用于定位历史证据。

仓库文档及配置示例只保留模板或脱敏别名；请勿把它们当作当前实例的真实路径。上述本地文件以 `0600` 保存，不随仓库分发，另换工作区时须自行安全保管。

环境继续留存供人工测试，本轮收尾不变更 DNS／Caddy、服务配置、账号、数据、版本或备份。后续维护另按明确安排执行；优先补齐[阶段交接中的未完成项](implementation/space-deployment.zh.md#阶段交接2026-11-25)，再冻结生产候选。

## 8. 一条命令部署本地最新 commit

仓库提供 [deploy-linux.sh](../scripts/deploy-linux.sh)，本地需要 Node、Git、tar 和可非交互登录的 SSH；远端需要原生 Linux amd64、systemd（含 systemd-run）、Git、tar、Go／Node 及所选运行依赖。仓库配置只含通用占位值，须先在忽略的本地副本中填写实际工具链、目录及 SRT 上游；当前留存环境的副本已保存在 `.local/deploy-linux.json`，沿用时不要被模板覆盖。SSH 主机必须使用 `--host` 指定，非默认 SSH 端口使用 `--port`，JSON 不接受这两个字段。非 root SSH 账号还须有可用的 `sudo -n` 管理权限。

在本地仓库根目录准备配置：

```sh
mkdir -p .local
# 仅为新环境创建副本；已有文件时保留原值。
if [ ! -e .local/deploy-linux.json ]; then
  cp packaging/deploy-linux.example.json .local/deploy-linux.json
fi
chmod 600 .local/deploy-linux.json
```

`teamx-linux` 是 SSH 别名示例，也可传 `user@server`。可在仓库外的 `~/.ssh/config` 配置实际地址、账号、端口和私钥位置，命令只引用别名；私钥继续由 SSH 管理，不写入 JSON。若需要覆盖端口，在命令追加 `--port 2222`。

`server_root`／`home`／`bin_dir` 必须指向原安装登记的路径；`node_binary`／`build_path` 指向可用工具链。SRT 的 loopback 端口、模型域名也必须换成实际 origin；`https://model.example.com` 仅为占位值。这些环境参数及初始配置路径也只保存在本地副本中。

先确认要交付的代码已提交到本地 Git；不要求 push。脚本在启动时固定 HEAD 的完整 commit，使用 Git bundle 传输已提交对象；远端运行器及其依赖也取自同一 commit，避免与打包脚本的参数协议不一致。工作区未提交修改、未跟踪文件及日常运行 home 不会混入；新增部署能力也须提交后才会在远端生效。Git 中若误提交已知私有运行路径会提前拒绝。构建包内记录的 commit 必须与请求一致。

```sh
# 查看固定的 commit、主机、远端新任务目录，不连接 SSH、不部署。
./scripts/deploy-linux.sh --host teamx-linux --config .local/deploy-linux.json --dry-run

# 实际上传并只构建、校验归档，不执行安装／升级。
./scripts/deploy-linux.sh --host teamx-linux --config .local/deploy-linux.json --build-only

# 上传、构建、校验，并自动选择首次安装或升级。
./scripts/deploy-linux.sh --host teamx-linux --config .local/deploy-linux.json

# 显式要求完整 smoke；已缓存的快速检查产物只补跑 smoke，不重新构建。
./scripts/deploy-linux.sh --host teamx-linux --config .local/deploy-linux.json --full-smoke
```

缓存完整时可追加 `--offline`；新增固定依赖缺缓存时，使用默认联网模式补齐，不修改 lock。每次调用仍创建新的任务目录，但会自动复用已验证结果：

- **依赖复用**：`<cache_dir>/prepared-dependencies/<key>/` 保存已展开依赖的归档、文件清单与元数据。key 包含目标平台、包型、libc、固定依赖及依赖装配代码摘要；应用源码、主打包流程和压缩模块变化可命中，锁文件／工具版本／依赖装配代码变化会重新准备。复用时核对缓存归档、文件哈希／权限／内部链接，并重新执行原生模块检查；普通应用更新执行快速应用检查；首次部署、无法确定基线、运行时依赖／打包／安装恢复逻辑变化时执行完整 smoke。
- **同 commit 归档复用**：`<work_dir>/artifacts/<key>/` 保存通过相应检查的原归档、摘要和报告，报告明确标记 `smoke_level: quick|full`。key 包含完整 commit、目标／包型、构建宿主信息及部署运行器摘要。命中后再次核对归档 SHA-256、完整源码身份、报告摘要和 smoke 验证器指纹，复用原字节与原报告。`--build-only` 后可直接执行普通部署；安装失败后重试也不必重新编译和 smoke。

构建失败或 smoke 未通过的产物不进入可复用归档缓存。缓存损坏／身份不匹配时在安装前拒绝，保留现场；检查后只移走报错指向的缓存项再重试。依赖缓存和归档缓存用途不同：需要同 commit 强制重建时，只移走对应 `artifacts/<key>/`，下一次仍可复用未变化的依赖。旧脚本任务未登记这些缓存，首次执行新脚本仍需构建。

流程为：本地固定 HEAD → SSH 上传 bundle／运行器 → 远端检出同一 commit → 复用 Go／依赖缓存打 `linux-amd64 standard` 开发包 → 核对摘要、smoke 报告和包内源码身份 → **使用新归档内的安装器** → 检查已安装 CLI 的 commit 和认证服务状态 → 排入独立后台留存清理任务，部署命令返回。已有 `service.json` 时升级，不再传首次 home／账号／执行选择；否则新装。升级使用全新备份目录，不跳过备份，不自动回滚。

JSON 配置可设置 `"keep_releases": 2`、`"keep_backups": 2`，省略时均为 2。程序版本至少保留 2 个（当前及上一个成功版本），配置／Space／Room／Session 仍引用的版本额外保留；完整备份至少保留 1 份，默认保留最近两次成功部署的升级前快照。每份新备份收录完整 home 和所需程序归档的引用及只读文件链接；清理安装目录不会改变已发布备份的恢复能力。删除程序登记和旧备份后，仅回收共享池中已无其他硬链接的程序对象；手工及失败部署备份持有的链接同样受到保护。

清理调用 `teamx service prune`，在安装根的私有 `retention.json` 中登记成功部署的版本和备份路径，按部署顺序保留，不以目录修改时间判断新旧。只清理已登记的部署备份；既有手工备份、其他实例及失败部署的未登记备份保持原样，不会因目录名相似而被删除。首次使用新策略时，这些旧备份仍需按实际恢复价值手工整理。相同归档重跑未切版本时不登记不存在的备份。

归档校验、安装／升级或服务身份检查失败时不执行清理。清理前再次认证服务、核对版本引用，并完整校验要保留的备份；不能确认依赖或备份时停止清理。清理通过独立 `teamx-retention-*.service` 执行，`deployment.json` 的 `retention` 保存排队结果、unit、回执与日志路径；真正的执行状态由任务内 `retention.json` 的 `queued/running/passed/failed` 表示，结果位于 `result`，错误位于 `error`。清理排队或执行失败不改变已部署状态，新版服务继续运行；可根据 `retention-request.json` 中的原 `backup`，使用 `teamx service prune --server-root DIR --from BACKUP` 重试并补登记该备份（首次安装或同包无操作时省略 `--from`）。下一次构建可与上一次清理并行，但安装前会等待前次清理结束，保持版本／备份登记顺序。后台清理与其他直接调用的维护命令也共用维护锁，最多等待 15 分钟，可取消；等待不停止业务服务。直接调用 `service upgrade`／`backup` 不会自动执行保留策略。

远端通过独立 systemd 临时服务执行任务，SSH 只跟随 `run.log`，断线不会中断任务。相同 commit、相同行为参数的命令可重新执行以跟随原任务；不同 commit 或检查／构建选项会明确拒绝并给出原任务位置。任务目录是 `<work_dir>/jobs/<commit>-<time>-<random>/`，不覆盖旧源码快照。每个任务保留 `source/`、`dist/`、`source.bundle` 和 `deployment.json`；后者通过原子写入和落盘记录源码 commit、归档 SHA-256、检查级别、PID、开始／更新时间、当前阶段、备份路径、缓存命中及最终状态；`finished_at` 表示部署及清理排队收尾完成，不表示后台清理结束。重连回执的 `followed_job` 指向实际执行任务。失败保留现场，以服务状态／日志和一致备份诊断，不把旧服务仍可访问当作新部署成功。

终端会显示 `[local]` 的 bundle／上传／远端总耗时，`[deploy]` 的构建或复用／校验／安装／认证检查耗时。首次构建还有 `[package]` 的依赖、原生模块、各个 Go 程序、清单、压缩及完整 smoke 耗时；smoke 各检查显示相邻检查之间的耗时和累计时间。SSH 与远端长任务期间每 15 秒报告仍在执行。

开发包使用 gzip level 1 流式压缩，正式发行保留 level 6；格式仍为 `.tar.gz`，构建机无需额外安装压缩工具。完整包扫描结果同时用于各 runtime 摘要和 checksums，完整 smoke 复用解压扫描结果校验 SRT。依赖准备另记录装配、npm 安装、文件扫描、缓存 tar 写入与哈希耗时及条目数／归档字节数；smoke 将归档摘要、列目录、解压和文件扫描分别计时，避免将这些成本归入 SRT 模块加载。远端在一次扫描中读取 manifest 与安装器，并逐字节比对固定 commit 中的安装器源码；安装器另在一次扫描中提取并核对唯一 bootstrap。包摘要、最终解压校验和一致备份继续保留。

新 CLI 同时提供 `browser_url`，部署后从该浏览器入口检查匿名登录页及递归依赖、私有接口仍返回 401；不以内部端口匿名访问替代公网代理验收。该检查不登录真实账号、不触发模型或平台写入。

远端任务内可回看 `run.log`（完整后台输出）、`build.log`（实际构建时产生）、`installer.log`（安装或升级时产生）、`deployment.json`、`retention.json`（后台清理状态和结果／错误）及 `retention.log`（后台清理日志），实际构建另有 `dist/*-timing.json`。升级 CLI 把归档准备、执行环境检查、程序入库、停服、锁定、数据归档落盘、归档回读校验、版本切换、启动和认证检查分别计时，并在长阶段每 15 秒输出执行中状态，并显示停服流程耗时；详细输出留在 `installer.log`。日志及缓存含环境路径，按私有运维材料保存，不提交到仓库。

先将本次脚本、打包辅助模块、备份和保留策略改动纳入 commit，再使用上述命令测量。构建／完整 smoke 发生在旧服务继续运行期间，备份和切换阶段才进入停服流程；清理在新版启动认证后进行。备份和落盘校验始终保留；完整 smoke 按改动范围选择，正式发行和 CI 仍要求完整检查。

新装其他主机时，先按第 3–5 节准备宿主依赖、HTTPS、专用部署目录和认证配置。在 JSON 中增加 `"deployment_config": "/remote/private/initial.toml"`，仅引用远端已存在的 0600 文件，不在本地配置中写 Key／密码。首次选择 SRT 时，`execution`／`srt_endpoints` 必须与私有初始配置匹配。脚本不配置 Caddy、DNS、模型 Key 或平台登录，也不绕过当前正式多用户生产门槛。

`.local/`、`.env*` 和私有运行文件不应提交；公开示例不写真实主机、域名、账号标识或 Key。命令行和终端日志可能显示 SSH 目标，有保密要求时优先使用 SSH 别名。当前文件脱敏不会删除已经进入 Git 历史的信息；脚本传输的 Git bundle 也包含 HEAD 的可达历史，不能当作历史脱敏工具。

脚本的 Git／tar／SSH 传输、commit 固定、新装／升级参数、失败阻断、只构建、保留数量、同包重跑与清理失败隔离有本地夹具回归；备份依赖选择、独立恢复、旧格式兼容、引用保护和中断清理另有 Go 回归。此次保留策略尚未在 alpha 执行真实升级与清理。检查入口：

```sh
node --test scripts/deploy-linux.test.mjs scripts/package-cache.test.mjs
go test -race ./internal/room -run '^(TestService|TestSpaceExecution)' -count=1 -timeout=180s
```
