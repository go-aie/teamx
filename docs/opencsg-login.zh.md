# OpenCSG 登录

多用户版统一使用 OpenCSG IAM 的 OAuth／OIDC 授权码登录，配置名称为 `provider = "opencsg"`，登录按钮显示「使用 OpenCSG 登录」。TeamX 使用独立应用直接接入 IAM。个人版继续使用原登录方式；多用户认证保留 Provider 扩展点，其他用户管理方式可独立接入。

## 应用与部署配置

在 OpenCSG IAM 应用 中启用 Authorization Code，登记准确的回调 URL。TeamX 请求 `openid profile`，使用每次登录独立的 `state`、nonce 和 S256 PKCE。应用有 Client secret 时同时使用 `client_secret_post`；只有确实配置为公共客户端时才省略 secret 文件。

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
hub_origin = "https://hub.opencsg.com"
```

`client_secret_file` 是运行 TeamX 的机器上的绝对路径。文件只保存 secret 本身，必须是 `0600` 的普通文件，不能是符号链接；由运行服务的账号读取。当前 alpha 服务账号为 `teamx`。不要把 secret 写入仓库、授权 URL、命令参数、模型环境变量或 Space 设置。源码不会自动读取仓库根目录的 `.env`。

`allow_all_authenticated = true` 允许通过该应用身份验证的账号准入；若只允许指定账号，删除此项并设置 `allowed_subjects = ["已验证的 OIDC sub"]`。`sub` 来自已验证的 ID token，不是邮箱或昵称。

OpenCSG IAM 中登记的回调为：

```text
https://alpha.teamx.opencsg.com/v1/access/sso/callback
```

`state` 单独传递且必须原样返回，不加入固定回调白名单，不设为固定字符串。TeamX 验证 ID token 的签名、issuer、audience、有效期和 nonce；浏览器 Cookie 与一次性 state 仍由现有宿主登录事务管理。secret、授权码、ID token 和 access token 不写入用户目录或业务会话。

## 现有身份和模型

已有直连 IAM 部署中的旧名称 `provider = "casdoor"` 只作为一次性改名迁移输入。启动时先将私有配置改为 `opencsg`，再在身份目录中原子更新 provider；仅允许原 authority 与配置的 IAM issuer 一致。UserID、OIDC sub、SpaceID、停用状态及认证版本不变，模型凭据、个人平台授权和服务级应用继续使用原目录，不需要新建 Home。中断后重启沿原身份完成迁移，不创建替代用户。旧浏览器会话失效，用户重新登录即可进入原空间。

旧 Secret 文件的实际路径保持不变，无需因示例改名而移动文件。日常发布继续使用 `service upgrade`，升级前保留一致备份。新版写入配置和身份目录后，不应直接用旧程序回退；回退应恢复对应旧版本及完整备份。

更早的 OpenCSG broker 登录已移除：不再接受 `login_origin`，不再处理 `jwt_token`／`jwt` 回调。该机制的 Hub authority 与 IAM issuer 是不同认证源，不能自动合并或接管旧用户；若仍使用 broker Home，须先单独设计身份迁移，再升级到新版。此次改名迁移不更换 issuer，也不按邮箱或昵称匹配账号。

配置 `hub_origin` 后，TeamX 在本次登录中核对 IAM access token 的 userinfo 与已验证 ID token 的 `sub` 一致，再通过 Hub 的 OAuth 认证读取该用户个人 namespace 的内置 AI Gateway Key。请求不接受回调中的 namespace 或用户名提示，不使用部署账号的 Key；返回值须属于该用户个人 namespace、应用为 `aigateway` 且处于启用状态。IAM token 随登录请求结束即丢弃，只有本人 Gateway Key 保存至私有用户凭据目录。

新空间以及尚未配置模型的现有空间，会使用本人 Key 自动配置并验证默认模型 `deepseek-v4.1-flash-1017`（`https://ai.space.opencsg.com/v1`）。已有模型配置保持不变。模型验证失败时保留普通模型设置页供重试；本人 Key 获取失败时本次登录失败，不回退到环境变量或其他账号的额度。

不配置 `hub_origin` 时，登录不获取新的 Gateway Key；尚未保存本人 Key 的用户需手动填写模型和 API Key。已保存的私有凭据仍可使用。已通过 IAM 登录的用户在启用此功能后，需要重新登录一次，以获取本人 Key；已有业务会话不保存 IAM token。

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
