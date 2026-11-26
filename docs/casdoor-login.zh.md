# Casdoor 独立应用登录

TeamX 可直接作为 Casdoor OIDC 客户端，使用独立 Application 的授权码登录，不经过 OpenCSG broker。原有 `provider = "opencsg"` 入口保留。

## 应用与部署配置

在 Casdoor Application 中启用 Authorization Code，登记准确的回调 URL。TeamX 请求 `openid profile`，使用每次登录独立的 `state`、nonce 和 S256 PKCE。应用有 Client secret 时同时使用 `client_secret_post`；只有确实配置为公共客户端时才省略 secret 文件。

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
provider = "casdoor"
issuer = "https://iam.opencsg.com"
client_id = "REPLACE_WITH_TEAMX_CLIENT_ID"
client_secret_file = "/data/teamx-alpha-hosted/home/auth/casdoor-client-secret"
allow_all_authenticated = true
hub_origin = "https://hub.opencsg.com"
```

`client_secret_file` 是运行 TeamX 的机器上的绝对路径。文件只保存 secret 本身，必须是 `0600` 的普通文件，不能是符号链接；由运行服务的账号读取。当前 alpha 服务账号为 `teamx`。不要把 secret 写入仓库、授权 URL、命令参数、模型环境变量或 Space 设置。源码不会自动读取仓库根目录的 `.env`。

`allow_all_authenticated = true` 允许通过该应用身份验证的账号准入；若只允许指定账号，删除此项并设置 `allowed_subjects = ["已验证的 OIDC sub"]`。`sub` 来自已验证的 ID token，不是邮箱或昵称。

Casdoor 中登记的回调为：

```text
https://alpha.teamx.opencsg.com/v1/access/sso/callback
```

`state` 单独传递且必须原样返回，不加入固定回调白名单，不设为固定字符串。TeamX 验证 ID token 的签名、issuer、audience、有效期和 nonce；浏览器 Cookie 与一次性 state 仍由现有宿主登录事务管理。secret、授权码、ID token 和 access token 不写入用户目录或业务会话。

## 现有身份和模型

OpenCSG broker 与 Casdoor 直连是不同认证源。现有数据目录固定原来的 provider／authority，不能通过改配置自动接管或合并旧用户。首次验证应使用全新的独立 Home；原 Home 和配置保留，切回 OpenCSG 时仍使用原目录。正式沿用旧数据需要另行设计明确的身份迁移。

配置 `hub_origin` 后，TeamX 在本次登录中核对 IAM access token 的 userinfo 与已验证 ID token 的 `sub` 一致，再通过 Hub 的 OAuth 认证读取该用户个人 namespace 的内置 AI Gateway Key。请求不接受回调中的 namespace 或用户名提示，不使用部署账号的 Key；返回值须属于该用户个人 namespace、应用为 `aigateway` 且处于启用状态。IAM token 随登录请求结束即丢弃，只有本人 Gateway Key 保存至私有用户凭据目录。

新空间以及尚未配置模型的现有空间，会使用本人 Key 自动配置并验证默认模型 `deepseek-v4.1-flash-1017`（`https://ai.space.opencsg.com/v1`）。已有模型配置保持不变。模型验证失败时保留普通模型设置页供重试；本人 Key 获取失败时本次登录失败，不回退到环境变量或其他账号的额度。

不配置 `hub_origin` 时，登录不获取新的 Gateway Key；尚未保存本人 Key 的用户需手动填写模型和 API Key。已保存的私有凭据仍可使用。已通过 IAM 登录的用户在启用此功能后，需要重新登录一次，以获取本人 Key；已有业务会话不保存 IAM token。

发行版的 Linux／SRT、多用户组合及生产发布门槛保持原有约束，配置样例不代表已经部署或通过完整验收。

## 独立登录验证

认证探针复用正式登录事务和 Provider，只保存不含凭据的身份收据，不登记业务用户或发放业务会话：

```sh
TEAMX_CASDOOR_ACCEPTANCE_CONFIG=/absolute/private/config.toml \
TEAMX_SSO_ACCEPTANCE_HOME=/absolute/fresh/private/receipt-directory \
TEAMX_SSO_ACCEPTANCE_LISTEN=127.0.0.1:18090 \
TEAMX_SSO_ACCEPTANCE_ORIGIN=https://alpha.teamx.opencsg.com \
go test -tags=e2e ./internal/room -run '^TestCasdoorSSOLoopback$' -v -count=1 -timeout=35m
```

配置须为 `0600` 的普通文件，`TEAMX_SSO_ACCEPTANCE_HOME` 须尚不存在。使用公开 origin 时，需要将该 origin 的探针入口和回调路由到此监听端口，并保留 Host／可信代理合同；仅运行命令不会改变现有 alpha 服务或代理。不设置公开 origin 时，使用监听地址作为回调，必须另在 Casdoor 登记对应的字面 loopback 回调 URL。

探针最多等待 30 分钟，只有浏览器完成授权、TeamX 验证身份并保存收据，才计为真实登录通过。仅核对应用凭据、看到 Casdoor 登录页面或完成本地替代服务测试都不等于完整登录验收。
