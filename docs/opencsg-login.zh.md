# OpenCSG 登录

多用户版统一使用 OpenCSG IAM 的 OAuth／OIDC 授权码登录，配置名称为 `provider = "opencsg"`，登录按钮显示「使用 OpenCSG 登录」。TeamX 使用独立应用直接接入 IAM。个人版继续使用原登录方式；多用户认证保留 Provider 扩展点，其他用户管理方式可独立接入。

## 应用与部署配置

在 OpenCSG IAM 应用中启用 Authorization Code，登记准确的回调 URL。多用户长期登录还需为应用启用 Refresh Token／offline access，按 IAM 应用要求完成 consent。TeamX 请求 `openid profile offline_access`，使用每次登录独立的 `state`、nonce 和 S256 PKCE。应用有 Client secret 时同时使用 `client_secret_post`；只有确实配置为公共客户端时才省略 secret 文件。

以下为生产 IAM 的配置模板，替换 Client ID 后写入私有配置，服务配置文件应为 `0600`：

```toml
[server]
listen = "127.0.0.1:8088"
access_mode = "proxy"
public_base_url = "https://alpha.teamx.opencsg.com"
trusted_proxies = ["127.0.0.1"]

[deployment]
mode = "multi-user"

[deployment.authentication]
provider = "opencsg"
issuer = "https://iam.opencsg.com"
client_id = "REPLACE_WITH_TEAMX_CLIENT_ID"
client_secret_file = "/data/teamx-alpha-hosted/home/auth/opencsg-client-secret"
allow_all_authenticated = true
session_max_age = "168h"
hub_origin = "https://hub.opencsg.com"
```

`client_secret_file` 是运行 TeamX 的机器上的绝对路径。文件只保存 secret 本身，必须是 `0600` 的普通文件，不能是符号链接；由运行服务的账号读取。当前 alpha 服务账号为 `teamx`。不要把 secret 写入仓库、授权 URL、命令参数、模型环境变量或 Space 设置。源码不会自动读取仓库根目录的 `.env`。

`allow_all_authenticated = true` 允许通过该应用身份验证的账号准入；若只允许指定账号，删除此项并设置 `allowed_subjects = ["已验证的 OIDC sub"]`。`sub` 来自已验证的 ID token，不是邮箱或昵称。

OpenCSG IAM 中登记的回调为：

```text
https://alpha.teamx.opencsg.com/v1/access/sso/callback
```

`state` 单独传递且必须原样返回，不加入固定回调白名单，不设为固定字符串。TeamX 验证 ID token 的签名、issuer、audience、有效期和 nonce；浏览器 Cookie 与一次性 state 仍由现有宿主登录事务管理。secret、授权码和原始 ID token 不进入用户目录或业务会话。宿主将可续期 Grant 私有保存在部署 Home 的 `auth/browser-sessions.json`，不向 Space、模型或浏览器下发 IAM token。

## 现有身份和模型

部署配置和持久化身份统一使用 `opencsg`。既有部署继续沿用原 Home，日常发布使用 `service upgrade` 并保留一致备份；配置、UserID、OIDC sub、SpaceID、停用状态、模型凭据和平台授权继续使用原目录。Secret 文件按配置中的实际路径读取，无需因示例文件名不同而移动。

启动要求身份目录中的 provider／authority 与配置的 `opencsg`／IAM issuer 一致；不再自动改写旧 provider 名称或身份快照，不按邮箱或昵称合并账号。未完成改名的旧配置或身份目录应先使用包含迁移能力的版本完成升级；恢复旧备份时使用对应版本。

OpenCSG broker 登录已移除，不接受 `login_origin` 或 `jwt_token`／`jwt` 回调。

配置 `hub_origin` 后，TeamX 在本次登录中核对 IAM access token 的 userinfo 与已验证 ID token 的 `sub` 一致，再通过 Hub 的 OAuth 认证读取该用户个人 namespace 的内置 AI Gateway Key。请求不接受回调中的 namespace 或用户名提示，不使用部署账号的 Key；返回值须属于该用户个人 namespace、应用为 `aigateway` 且处于启用状态。同一次授权码交换的 access token 同时用于本人 Key 初始化与宿主会话；刷新不会重新读取 Key、验证模型或覆盖用户模型选择。本人 Gateway Key 仍保存至独立私有用户凭据目录。

新空间以及尚未配置模型的现有空间，会使用本人 Key 自动配置并验证默认模型 `deepseek-v4.1-flash-1017`（`https://ai.space.opencsg.com/v1`）。已有模型配置保持不变。模型验证失败时保留普通模型设置页供重试；本人 Key 获取失败时本次登录失败，不回退到环境变量或其他账号的额度。

不配置 `hub_origin` 时，登录不获取新的 Gateway Key；尚未保存本人 Key 的用户需手动填写模型和 API Key。已保存的私有凭据仍可使用。已通过 IAM 登录的用户在启用此功能后，需要重新登录一次，以获取本人 Key；Room、模型调用和平台授权不使用 IAM token。

发行版的 Linux／SRT、多用户组合及生产发布门槛保持原有约束，配置样例不代表已经部署或通过完整验收。

## 独立登录验证

认证探针复用正式登录事务和 Provider，只保存不含凭据的身份收据，不登记业务用户或发放业务会话：

```sh
TEAMX_OPENCSG_ACCEPTANCE_CONFIG=/absolute/private/config.toml \
TEAMX_SSO_ACCEPTANCE_HOME=/absolute/fresh/private/receipt-directory \
TEAMX_SSO_ACCEPTANCE_LISTEN=127.0.0.1:18090 \
TEAMX_SSO_ACCEPTANCE_ORIGIN=https://alpha.teamx.opencsg.com \
go test -tags=e2e ./internal/room -run '^TestOpenCSGSSOLoopback$' -v -count=1 -timeout=35m
```

配置须为 `0600` 的普通文件，`TEAMX_SSO_ACCEPTANCE_HOME` 须尚不存在。使用公开 origin 时，需要将该 origin 的探针入口和回调路由到此监听端口，并保留 Host／可信代理合同；仅运行命令不会改变现有 alpha 服务或代理。不设置公开 origin 时，使用监听地址作为回调，必须另在 OpenCSG IAM 登记对应的字面 loopback 回调 URL。

探针最多等待 30 分钟，只有浏览器完成授权、TeamX 验证身份并保存收据，才计为真实登录通过。仅核对应用凭据、看到 OpenCSG 登录页面或完成本地替代服务测试都不等于完整登录验收。

## 会话期限、续期与升级

多用户 OpenCSG 的 `session_max_age` 默认 `168h`，必须为正 duration，最长 `720h`。这是从登录开始计算的绝对期限，刷新、请求和重启均不延长；缩短配置会在重启时缩短现有会话，调大配置只对重新登录生效。不设置闲置超时，不收集点击、输入或滚动活动。

宿主每次远端复核最多授予 1 小时检查窗口，且不晚于当前 access token 到期；接近检查期限时由可见页面提前复核，需要时刷新。关闭／隐藏页面不持续轮换，下次访问按需恢复。同会话的多标签页请求合并为一次续期。页面不直接持有 IAM access／refresh token。

`GET /v1/access/session` 和同源 `POST /v1/access/session/renew` 返回 `absolute_expires_at`、`verified_until`、`renew_after`、`renewal_supported` 等无凭据元数据。SSE 使用固定 `min(absolute_expires_at, verified_until)` lease，到期重连并重新读取业务状态。IAM 临时故障在检查期限外返回 503，页面保留草稿并暂停新写操作；401 才要求重新登录。业务 POST 不自动重放。

`auth/browser-sessions.json` 的多用户格式升级为 v3，文件包含可用 IAM 凭据，必须为运行服务账号持有的 `0600` 普通文件，目录私有；查看、复制及备份应按凭据处理。升级会原子清除合法 v2 多用户登录，用户需要重新登录一次；保留 User、Space、模型 Key 和平台连接。个人版保持原格式和登录语义。每用户最多 4 个浏览器会话，超出时淘汰该用户最早创建的会话；全部署最多 256 个且文件不超过 16 MiB，超限明确拒绝新登录。

普通退出只撤销当前浏览器会话和对应流，不停用用户、不停止后台 Agent，不执行 IAM 全局登出。管理员本地停用仍增加 AuthVersion 并撤销所属运行时。IAM 明确 `invalid_grant`、userinfo 拒绝或身份不一致时撤销相关浏览器会话；账号冻结及全局登出的远端传播尚需实测，不承诺 discovery 未证明的传播时限。

轮换后先保存最新凭据，再复核 userinfo 并发布新检查期限。轮换后保存失败会隔离该会话；快照提交结果不确定时暂停本进程浏览器认证，需受控重启并重新读盘恢复，不能在线替换 writer 或重置业务目录。回退到旧二进制前应停服并使浏览器登录快照失效，然后重新登录；不要恢复旧 refresh token 来尝试恢复登录。

本次实现与实测状态见[续期实现记录](implementation/opencsg-session-renewal.zh.md)。上述行为不代表真实 IAM／Linux 安装组合已经验收通过。
