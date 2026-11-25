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
allowed_subjects = ["REPLACE_WITH_VERIFIED_SUBJECT_UUID"]
# 明确向所有通过所选认证源验证的账号开放时启用：
# allow_all_authenticated = true
```

域名、端口和稳定 subject 必须换成实际值。subject 来自受保护身份验证，不是昵称。默认仅名单准入；显式开启 `allow_all_authenticated` 后，其他已认证账号也可注册，每人获得独立默认空间并配置自己的模型。alpha 当前包保留名单作为启动兼容项；只设开放开关的源码修正尚未重打包，不能把空名单配置用于该旧包。

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

OpenCSG 必须允许最终回调 `https://assistant.example.com/v1/access/sso/callback`，保留动态 `state` 参数。当前协议使用 OpenCSG broker：TeamX → OpenCSG → IAM → OpenCSG → TeamX；IAM 的 `redirect_uri` 指向 OpenCSG 自己的回调是正常行为，不需要因此为 TeamX 新建 Casdoor client。不要把聊天中的旧授权链接或回调 token 当作可复用登录入口。

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

先确认运行版本、目标包摘要、原登记路径和足够磁盘空间。备份包含整个 home 与全部 releases，不只是聊天记录；一份 standard 展开可能超过 1 GiB，多版本与逐文件校验会明显增加停服时间。备份父目录须存在，目标目录须全新，且在安装根／home 之外：

```sh
sudo install -d -m 700 /srv/teamx-backups
sudo /usr/local/bin/teamx service upgrade --server-root /opt/teamx \
  --archive /srv/teamx-delivery/new-standard.tar.gz \
  --backup-dir /srv/teamx-backups/before-upgrade-UNIQUE
```

升级复用登记的账号、home、执行方式和端点；不再传首次 `--deployment-config`、`--execution` 或 `--srt-endpoint`。它校验目标包后停服、结清、取得锁、一致备份、切换 `current`、生成新描述并启动认证检查。仅使用受信任且已验收的对应版本；生产机建议固定离线包与摘要，避免无意追随 `latest`。

托管已有 Space 保留其固定旧 release 的 DSH、原执行描述和 CLI；新 Space 使用当前 release。不要手工删除旧 releases／`home/execution` 来节省空间，或编辑模型收据／成员快照来消除运行指纹不匹配。旧依赖缺失或恢复条件不符时，应保留现场并明确处理，不重置 Session 或重放业务。

**当前已知容量边界**：alpha 三份发布目录的备份清单已有 66,694,220 字节，接近当前读取上限 64 MiB。继续增加版本前必须处理并复验该上限；本轮不将这条多版本升级链宣布为无限可用。不要以删备份、删历史 release 或跳过一致备份替代修复。

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

以下是 2026-11-25 交接时只读核对的状态，供继续人工验证；本节不要求再次安装、升级、重启或恢复：

| 项目 | 当前值 |
| --- | --- |
| 登录入口 | https://alpha.teamx.opencsg.com/ |
| SSH | `root@8.152.215.150` |
| 运行组合 | Ubuntu 22.04.5 amd64、glibc 2.35、standard＋SRT、多用户开发验收 |
| 当前版本 | `dev-f7d7360b0d4b`；SHA-256 `36d1f0ff9b6b4261761f478a8fbc948f4adaec0b9e63f921b345f56e864264c6` |
| 程序／私有 home | `/data/teamx-alpha-hosted/server`；`/data/teamx-alpha-hosted/home` |
| 管理入口 | `/data/teamx-alpha-hosted/bin/teamx` |
| 服务 | `teamx.service`，普通账号 `teamx`（UID/GID 997），active／enabled |
| 访问链 | Caddy → bridge `10.251.0.1:18089` → `teamx-alpha-proxy.socket`／service → `127.0.0.1:18088` |
| 准入 | 所有通过所选 OpenCSG 认证源验证的账号；原名单兼容项保留，新账号自己配置模型 |
| 原个人环境 | `/data/teamx-alpha-final` 保留，原服务登记已注销，不与当前服务同时运行 |
| 一致备份 | `/data/teamx-alpha-backups/personal-before-hosted`、`/data/teamx-alpha-backups/hosted-before-open-admission`；其他历史备份也保留 |
| 构建／私有证据 | `/data/teamx-acceptance`；当前包位于其 `open-admission-final/` |

只读查看当前环境：

```sh
ssh root@8.152.215.150
/data/teamx-alpha-hosted/bin/teamx service status --server-root /data/teamx-alpha-hosted/server
/data/teamx-alpha-hosted/bin/teamx service logs --server-root /data/teamx-alpha-hosted/server --lines 100
```

环境继续留存供人工测试，本轮收尾不变更 DNS／Caddy、服务配置、账号、数据、版本或备份。后续维护另按明确安排执行；优先补齐[阶段交接中的未完成项](implementation/space-deployment.zh.md#阶段交接2026-11-25)，再冻结生产候选。
