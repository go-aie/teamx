# 常驻 Server 与 Web Client：W0 契约及探针

日期：2026-11-12。对应[第三版方案 W0](../DESKTOP-APPLICATION-ANALYSIS-AND-PLAN.zh.md#w0环境与最小契约验证)。本阶段固定后续实现契约，增加可复跑的隔离探针；不提供生产认证、远程入口或服务安装成品。后续 W1 已实施，见[独立 Server 记录](server-lifecycle.zh.md)；W2 已实施，见[访问与模型设置记录](server-access.zh.md)；W3–W6 尚未实施。

## 固定的后续契约

以下是 W0 固定的阶段契约。W1 的目录与生命周期行为现已实现，见[独立 Server 记录](server-lifecycle.zh.md)；认证配置和接口已在 [W2](server-access.zh.md) 接入，部署命令仍须等 W5 实现后使用。已有 `serve --config`、`server.listen/data_dir/teams_dir` 和 `bundle --home` 继续使用。

### 地址、所有者和认证

| 契约 | 固定命名与行为 | 落地阶段 |
| --- | --- | --- |
| 内部地址 | `internal_endpoint` 是运行时值，沿用 ready 的 `endpoint` 与现有 `TEAMX_ENDPOINT`；由实际 listener 计算，不另设可伪造的配置值 | W1 |
| 外部地址 | `server.public_base_url`；未设置时为纯本地模式，使用实际 loopback 地址；设置时只接受一个 HTTPS 根 origin，无 userinfo、子路径、query、fragment | W2 |
| 代理 | `server.trusted_proxies` 为显式 IP 列表，默认空；只允许来自该 socket peer 的单值 `X-Forwarded-Proto: https`，Host 必须匹配规范 origin；不使用转发的 Host 推导外部地址，不由转发 IP 决定身份 | W2 |
| 固定 human | `access.operator_id`，初始化时生成 `owner_` + 16 随机字节的十六进制值；持久保存，不从 `USER` 获取，不随 token、设备、版本变化 | W2 |
| 管理文件 | `access.admin_token_file`，默认相对 config 的 `auth/admin-token`；普通私有文件、部署账号所有、0600，拒绝符号链接及宽松权限 | W2 |
| 管理值 | 32 随机字节的无填充 base64url（43 字符，可有一个末尾换行）；外部输入采用同一编码和长度。已有文件不覆盖，缺失／无效拒绝业务入口；不是每次启动生成 | W2 |
| 浏览器会话 | 随机值，服务仅存摘要与固定 human；内存保存，默认 24h、最多 16 个，退出／到期／重启失效；无持久会话／刷新协议 | W2 |
| Cookie | HTTPS 用 `__Host-teamx-session`、Secure、HttpOnly、SameSite=Lax、Path=/、无 Domain；纯本地 HTTP 用独立 `teamx-local-session` | W2 |
| 登录边界 | `POST /v1/access/login`、`POST /v1/access/logout`、`GET /v1/access/session`；登录、Cookie 写请求要求规范 Origin；仅最少静态登录资源、健康信息和精确 OAuth 回调例外 | W2 |
| 人工 CLI | `--token-file`／`TEAMX_ADMIN_TOKEN_FILE` 读取私有管理文件，发送 Bearer；不接受 argv 中的 token 值。正文 Caller 只作固定 human 一致性核对 | W2 |
| Agent CLI | 临时 `TEAMX_EXECUTION_TOKEN`（DSH 子工具由同值 `TEAMX_EXECUTION_AUTH` 传递，见 W2 实测补充），Bearer 映射为绑定 agent，并核对原六项身份字段；不持有管理 token、Cookie 或管理文件路径 | W2 |

有 Bearer 的 CLI 请求仍需认证，空 Origin 不提供任何权限；有 Origin 时必须符合规范检查。Cookie 请求与登录不能靠空 Origin 绕过 CSRF。公网 `X-Forwarded-*` 不成为信任依据；代理配置错误应拒绝并报告，不能开放 CORS 兜底。

Agent 执行凭据绑定 RoomID、RoomInstance、AgentID、MembershipID、ServiceEpoch 和允许工具范围。随会话结清／成员替换／Server 换代失效；恢复重新发放，没有固定小时 TTL。只进入临时执行环境，不进入冻结 Runtime、ToolEnv、prompt 或恢复指纹。HTTP／CLI 入口检查实际认证主体和正文 Caller，不改写 JSON，不把认证状态机放进 Room／Inbox。

`TEAMX_ADMIN_TOKEN_FILE` 及任何未来管理凭据变量须从 `service.toolEnv` 和 DSH 的继承环境过滤。默认值由 W2 常量维护，不为这些边界新增通用 RBAC 或动态策略配置。首次模型设置复用此认证，配置成功后再正常启动业务。

### 目录、版本和部署入口

Linux 默认部署账号为专用普通用户 `teamx`；默认 server root `/opt/teamx`、home `/var/lib/teamx`、日志由 journald 管理。macOS 使用当前用户，默认分别为 `~/Library/Application Support/TeamX/server`、`~/Library/Application Support/TeamX/home`、`~/Library/Logs/TeamX`。这些路径由部署入口解析为绝对路径，不修改宿主 `HOME`。

```text
<server-root>/installation.json
<server-root>/releases/<build-id>/    # 完整且不可变的 standard 内容
<server-root>/current                # 仅选择下一次启动版本
<home>/config.toml
<home>/auth/admin-token
<home>/teams/
<home>/data/
```

`build-id` 使用归档清单版本／平台／profile 与归档摘要前 12 位组合；同 ID 的不同内容拒绝安装。W1 以真实版本路径运行，包内默认 DSH 先 `EvalSymlinks` 再冻结；显式外部 Runtime 不自动替换，旧 Room 引用的完整版本目录保留。W1 增加最少安装元数据和数据独占；W5 完成验证归档、发布、安装串行锁、备份与切换。

固定部署命令族为 `teamx service`（部署机执行，不作为 Agent 工具）：

| 入口 | 行为与阶段 |
| --- | --- |
| `teamx access init --home DIR` | W2 生成固定 operator ID 与管理文件；已有初始化不覆盖；只报告私有文件路径，不打印 token |
| `teamx service install --archive FILE --server-root DIR --home DIR` | W5 校验 standard、登记实例、安装系统服务；首次设置和认证就绪前只允许隔离本地访问 |
| `teamx service inspect/start/status/stop/restart --server-root DIR` | W5 核对登记实例后操作服务管理器；状态区分存活、认证 ready 和平台降级 |
| `teamx service upgrade --archive FILE --server-root DIR` | W5 串行安装、备份、停旧、发布、切换、检查；不删除被引用版本、不自动迁移 schema |
| `teamx service uninstall --server-root DIR` | W5 停止并注销，保留 home；首版不提供顺带删除数据的选项 |

服务固定名称为 Linux `teamx.service`／macOS `com.teamx.server`。停止预算至少 45s，大于当前 30s；Linux 需验证 `KillMode=mixed` 对 DSH 独立进程组的清理。`stop` 不修改下次登录／开机启用状态，管理员停机意图不能被 KeepAlive 立即撤销。跨重启启用由 install 和服务管理器显式配置，不能把“当前进程退出”当作禁用服务。管理 token 轮换采用部署机替换 0600 文件再正常重启，不增加公网找回或在线轮换协议。

W0 只运行已有 foreground `start --home`，未注册上述服务。新数据根不导入开发目录或旧 bundle。关浏览器、退出登录、暂停 Agent、停止服务保持独立。

## Linux 飞书结论

固定版本仍为 lark-cli **1.0.99**，commit `8d8357d39dd15ac5e9aa815d4ab2639d56bd3239`；没有升级 CLI 或解开 TeamX 的 Linux 发送保护。

官方固定源码的 [Linux keychain](https://github.com/larksuite/cli/blob/8d8357d39dd15ac5e9aa815d4ab2639d56bd3239/internal/keychain/keychain_other.go) 使用 AES-256-GCM 文件，默认 `~/.local/share/lark-cli`，支持 `LARKSUITE_CLI_DATA_DIR/lark-cli`。主密钥为该目录的 0600 `master.key`。因此依赖清单中旧的 D-Bus／Secret Service 必需条件已修正；加密文件和同目录密钥仍依赖部署账号／文件权限保护。

离线探针调用**已校验摘要的官方 Linux amd64 二进制**，每次命令都启动新 Ubuntu 24.04 容器，禁止网络、无桌面、无宿主凭据挂载，使用合成 app secret。通过的事实：

- 官方 init 写入引用配置、加密文件与私有主密钥，配置和密文不含合成明文。
- 新容器的 `auth status --json` 经官方配置解析读取 app secret，不需要 GUI／D-Bus。
- 只含公开 app 身份和官方 secret 引用的独立配置可复用同一官方存储；修改普通配置不改变独立配置选择；主密钥损坏后新进程拒绝读取。

这验证的是 **app secret 存储及跨进程读取**，不是用户 token、登录成功、自动续期、DPoP 恢复或账号固定发送。`auth status` 中 bot 可用也不代表已经在线核验，探针没有发送任何平台消息。

可行路线是在 W5 继续使用该官方存储，复用现有 profile 锁、公开配置隔离和完整账号验证。官方 [UAT 存储](https://github.com/larksuite/cli/blob/8d8357d39dd15ac5e9aa815d4ab2639d56bd3239/internal/auth/token_store.go) 按 appID/openID 寻址，但真实用户固定、注销／切换竞态、DPoP 材料路径及重启后续期仍需验证。暂不需要提出 CLI 升级预算；当前 `Pinned.Pin` 的非 Darwin 保护不变，**Linux 发送仍是发行阻断**。不能仅删除 GOOS 条件后宣布支持。

| 能力 | 当前结论 | 后续门槛 |
| --- | --- | --- |
| 无桌面 app secret 存储／进程重启读取 | Linux amd64 容器通过；Mac arm64 宿主上为模拟执行 | W5 原生 Linux amd64、普通部署账号和真实机器重启 |
| 用户登录、读取、通知 | 本次未运行真实平台 | W4／W5 分别记录真实证据 |
| 用户 token 续期与 DPoP 恢复 | 未验证 | W5 无人值守、服务重启与凭据过期场景 |
| 账号固定发送 | 当前 TeamX 返回 `account_pinning_unavailable` | W5 适配并验证完整账号、锁、切换／注销／密钥丢失；W6 仅使用明确授权目标 |

## 验证入口与证据

运行目录统一放在被 Git 忽略的 `.local/server-foundation-probe/`，没有读取现有 TeamX 数据或真实平台凭据。探针代码按行为命名，无阶段编号；不进入发行包。

```sh
# 生成本机 standard 开发候选，复用固定依赖缓存；非发行资格
node scripts/package.mjs --profile standard --offline --output-dir .local/server-foundation-probe/dist

# 已下载官方 Linux amd64 归档；脚本先核对 dependencies.lock.json 的 SHA-256
python3 scripts/lark-headless-probe.py \
  --archive .local/server-foundation-probe/lark-linux-amd64.tar.gz \
  --report .local/server-foundation-probe/lark-headless.json

# amd64 Docker image 必须预先可用；Mac ARM 上明确只算模拟执行
# docker pull --platform linux/amd64 ubuntu:24.04

# Playwright 只作验证依赖；verifier 指向已安装 playwright 的独立目录
# 默认使用独立测试配置的本机 Chrome；可用 --channel 指定其他已安装通道
node scripts/server-background-probe.mjs --archive PATH_TO_STANDARD_ARCHIVE \
  --output NEW_ISOLATED_OUTPUT_DIR --verifier .packaging/p4.3-verifier

go test -race -tags=integration ./internal/room \
  -run '^Test(NotificationWorkerSurvivesWebClientDisconnect|GitLabEventsWorkerDeliversCurrentRoomAndDrainsInactiveBacklog|GitLabIntegrationNotificationsRespectPauseBudgetAndFault)$' \
  -count=1 -timeout=2m
```

后台执行探针使用独立 standard home、真实 Chromium 和带闸门的 ACP fixture：从 UI 发起执行，等 prompt 开始后关闭整个测试浏览器，再解除闸门；无后续 UI／HTTP 轮询参与执行，检查完成、正常停止、session close 和所拥有的子进程退出。它证明进程与浏览器生命周期独立，不冒充真实模型／平台验收。通知测试使用生产 worker、磁盘与 Room 投递，加上模拟远端事件；先断开 Web TCP 客户端，再释放查询，检查持久消息和来源缓冲确认。

首次 standard 构建因并行工作区修改被源码漂移保护拒绝；后续构建包含当时未提交的其他业务修改，不能声称是纯 HEAD。现有 smoke 在恢复期间目录查询／创建返回 `409 busy` 时失败，因此增加有界等待：仅处理目录读取，以及携带原 create/open ID、明确返回 `busy`（受理前拒绝）的目录操作；不重试网络失败或未知结果。打包器将输出保留为诊断候选；修复后对同一归档字节单独重跑 smoke 通过，没有宣称原打包命令成功或发布正式归档。归档身份、实测指标和最终通过情况见下方记录。

### 本次实际结果

| 项目 | 结果／边界 |
| --- | --- |
| 源码身份 | `cd2bbbbc72b11fa6e2010693d6c8c87b89fa5521` + dirty；当时 tracked diff SHA-256 `38d760def67fdb6d9e399679a769c0699005281dabaebe96a6229a9a72bc7171`；完整输入摘要保存在报告 |
| standard 依赖 | Node 24.22.2、DSH 0.2.2-rc.1、glab 1.127.0、lark-cli 1.0.99，归档 smoke 的版本／help／原生模块检查通过 |
| 开发实验归档 | `.local/server-foundation-probe/standard-experiment.tar.gz`；SHA-256 `4fbcafdc4a10d4d371fb5859648bfe75a6d542592fab68f86c39025a966a1d36`；不是正式发行产物 |
| 归档／每版本文件体积 | 310,189,405 字节归档；1,138,084,050 字节普通文件逻辑总量；未含文件系统分配／用户数据，完整版本保留会累加占用 |
| 本机执行环境 | macOS 15.7.4 arm64；独立测试 Chrome 157.0.8103.51，headless；非 Safari 或真实手机 |
| 浏览器退出后的执行 | `background-final/report.json` 为 passed，6 项检查；关闭时 prompt 尚未完成，关闭后才解除闸门并完成，Server 无 UI 轮询仍持久化 completed turn；之后正常 session close，ACP PID 已退出 |
| 启动／内存单次样本 | 从启动入口到 ready 4,244ms（含 doctor）；完成 fixture 后 Go Server RSS 22,352 KiB，不含浏览器／整个进程树，也不是实际模型负载性能结论 |
| 通知与调度 | 3 个定向 integration 测试带 race 通过，含断开 Web 连接后的采集／投递、未活跃 Room 积压、暂停／故障等边界；模拟远端事件，不是实际平台通知 |
| standard 归档 smoke | `archive-smoke.json` 为 passed；文件摘要／模式、隔离 init、外置 home、目录锁、停止／重启持久化、ACP fixture 与清理均通过 |
| Linux 探针 | `lark-headless.json` 为 passed，9 项检查；Ubuntu 24.04 amd64 容器运行于 Docker aarch64（模拟）；官方 CLI 归档 SHA-256 `fd29f51030dde6919b6ca9173bc87852d3846f253d198022ea2d9ee63527b945` |
| 辅助检查 | Linux standard 依赖清单校验、脚本语法、`git diff --check` 通过；11 个打包库／入口测试首次全通过；重跑时已有 PTY 交互测试出现 2 次输入时序失败，单文件串行复验 5/5 通过，未改动该交互测试或启动器 |

上述相对报告路径均位于 `.local/server-foundation-probe/`，浏览器截图和 ACP 方法／时间记录在 `background-final/before-close.png`、`background-final/acp.jsonl`。报告另存探针与 fixture 的源码摘要。探针最初遇到缺失 Playwright 默认浏览器、隔离环境未设 glab 配置目录和旧 UI 选择器，均已修正为独立 glab 目录、本机 Chrome 通道和默认助理正常入口；未修改产品 UI 来迁就探针。

## 支持范围与下一阶段

首批目标保持 **Linux amd64 Server、macOS arm64 Server**；浏览器候选为桌面 Chromium／Safari、iOS Safari／Android Chrome。W0 中的 headless Chromium 自动化只算桌面引擎行为证据；Safari 和两类真实手机没有验收。Linux 模拟容器不替代原生 Server，不能填写成已支持。暂不新增其他架构或 profile。

W1 已按本契约实现生命周期／真实版本目录及受限首次设置状态，见[独立 Server 记录](server-lifecycle.zh.md)；W2 才加入访问认证／设置表单／代理；W3／W4 分别验收手机体验／平台授权；W5 的 Linux 用户凭据、DPoP、续期与固定账号是明确门槛；W6 决定最终支持声明。本阶段不开放公网、不安装常驻服务、不执行外部写入。
