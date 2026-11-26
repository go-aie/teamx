# OpenCSG SSO 与多用户身份生命周期

日期：2026-11-24。对应多空间方案 S4a。**认证模块、宿主登录、持久用户／会话、空间授权和本地管理／停用结清代码已接通，本地定向验证通过；真实 HTTPS 单账号 SSO 身份交换已通过，完整 Linux 组合验收尚待完成。生产多用户门槛仍关闭，不声明 S4a／S4／S5 正式通过。**

2026-11-25 阶段交接：Linux 安装实例上的真实单账号登录、默认空间登记与首次模型验证已通过，用户已确认；按用户要求显式开放其他已认证账号准入，alpha 继续留存人工验证。第二个真实账号、远端撤权传播及正式组合出口仍保留；当前包的开放开关／名单启动兼容项与源码待重包状态，统一见[阶段交接](space-deployment.zh.md#阶段交接2026-11-25)。

## 登录与身份

独立 Casdoor Application 的直连 OIDC 入口、配置及验收边界见 [Casdoor 独立应用登录](../casdoor-login.zh.md)。以下为保留的 OpenCSG broker 协议。

沿用用户指定的 CSGClaw 自有 SSO：登录源 `https://opencsg.com`，CSGHub 默认 `https://hub.opencsg.com`，入口 `/sso/login?redirect_url=...`。不另配 JWT 公钥、服务 API key 或 IAM OAuth 应用。回调参数 `jwt_token`／`jwt` 是原协议字段，不是新增 JWT 配置。

TeamX 发起入口为 `POST /v1/access/sso/login`，回调为 `/v1/access/sso/callback`。远程使用部署配置的 HTTPS origin；本机验收允许字面回环 IP 的 HTTP origin，如 `http://127.0.0.1:18088`，不允许普通 HTTP 域名或局域网地址。浏览器完成登录后直接访问本机回调，不要求 OpenCSG 服务端连接本机端口。

`internal/authn` 提供窄 Provider 合同；`internal/authn/opencsg` 仅把 payload 中的用户名当作路由提示，再向固定 CSGHub 发出 CSGClaw 同款受保护请求 `/api/v1/user/{user}/tokens?app=git`。稳定主体来自响应的 `user_name`／`user_uuid`，拒绝缺失、混合身份、错误状态、超大响应和跨站重定向。过期信息只用于缩短本地会话，不能代替远端确认。原始 token、不可信 uuid、公开 profile 和邮箱均不作为本地身份绑定依据；不获取模型 Key，不持久保存或分发业务 token。

协议证据参考本机 CSGClaw HEAD `7227930b0ff534db0154c2d98e02010727b29da9` 的 `internal/auth/login.go`；身份响应依据本机 `starhub-server` HEAD `045cce6f94a11f431ab4b6294b8430520d3d3999` 的 userMatch、GetUserTokens、GetTokens 和 CheckAccessTokenResp。源码证明候选协议，公共服务实际返回仍须真实登录验证。实际无凭据 GET 已确认第一跳返回 302 到 IAM，外层 state 完整保留传入的 TeamX 回调及查询参数；这不等于整个用户登录通过。

## 宿主与持久状态

- 宿主持有五分钟随机登录尝试、浏览器 Cookie 绑定与原子单次消费；同时最多 128 个未完成尝试，每分钟最多发起 60 次。发起要求同源 POST，成功和失败均跳到固定本地地址清除 URL 敏感参数，设置 no-store／no-referrer。HTTPS 使用 Secure 和 `__Host-` Cookie；字面 HTTP 回环使用独立名称的本机 Cookie。
- `users.json` 是多用户组合唯一用户目录，保存正式 User、auth version、外部 provider／authority／subject 绑定与默认空间的不可变登记预约。认证源在首次准入时固定，换源不会自动创建替代身份或接管旧用户。
- 用户预约先于 `spaces.json` 发布；中断／重启沿原 UserID 和 SpaceID 补齐，不重新分配。初始化有独立持久完成记录；登记损坏、缺失及跨模式复用保留证据并拒绝，个人原接入流程不变。
- 会话只保存随机 token 的摘要，绑定安装与认证源；有效期最多 15 分钟，且不晚于远端 token 的期限。重启重读用户状态和认证版本。普通退出只撤销当前会话；管理 token 不作为多用户业务凭据。
- SpaceRuntime 继续只代表真实空间。没有登录用户时由共享控制入口服务；空间装配配方与业务运行对象分开，不创建虚构 owner 或空业务 SpaceRuntime。
- 空间列表、默认空间、创建／重试、改名、归档／恢复、业务请求、事件流及执行凭据使用认证主体。跨用户 ID、重复 request_id、旧认证版本不授予访问；目录占用和共享 Space／Session 额度沿用原 owner。

个人组合不调用外部认证工厂；多用户组合在启动装配选择 Provider。受控替代实现已通过相同宿主路由完成验证，不让 Room／Inbox／History 依赖 OpenCSG 实现。未选 Computer、托管 Git 仍由原受限工具策略关闭。

## 准入、管理与撤权

配置合同如下（生产总门槛未关闭前，不表示可直接启用）：

```toml
[deployment]
mode = "multi-user"

[deployment.authentication]
provider = "opencsg"
allowed_subjects = ["已核实的外部用户 UUID"]
# 若明确向所有已认证账号开放，增加：
# allow_all_authenticated = true
# login_origin = "https://opencsg.com"
# hub_origin = "https://hub.opencsg.com"
```

默认仍要求 `allowed_subjects`。仅部署管理员可显式设置 `allow_all_authenticated = true`，允许通过所选认证源受保护身份交换的任意非空稳定 subject 首次注册和登录；不会接受匿名身份、改变已固定的 provider／authority、恢复本地已停用账号或授予部署管理权限。每个新账号仍建立独立 User／默认 Space，不继承其他用户的模型、凭据和连接。准入配置在启动时读取，修改后需重启。

此开关控制认证回调的准入，已有会话仍按用户状态与原有效期处理；需要立即撤销访问时，使用本机停用命令。

远程配置继续使用 `server.public_base_url` 和原可信代理合同；本机回环不设置 public_base_url。Linux 多用户仍要求 SRT 及相应 manifest。普通用户不能更改部署访问配置、认证源或管理他人。

管理使用服务机器上的私有部署 token，入口只接受无浏览器 Origin、无代理转发的 loopback 请求：

```sh
teamx access users list --config /absolute/config.toml
teamx access users disable --user owner_<id> --config /absolute/config.toml
teamx access users enable --user owner_<id> --config /absolute/config.toml
```

停用先持久递增认证版本，再撤销会话、流与执行凭据，取消并等待所属空间的原生命周期 owner 结清。结清成功后空间为 suspended；超时保留同一对象、archiving、锁与证据，可重试，不返回虚假成功。启用需旧操作已结清，不复活旧 Cookie／凭据，也不自动恢复空间运行，用户仍可显式恢复原空间。中断在持久用户停用之后、运行时结清之前时，重启按 disabled 状态完成归档，不启动旧业务。

本地 Cookie 的 15 分钟上限不是远端停用传播时限。OpenCSG 外部冻结／撤销的实际行为尚未验证，不声明即时同步；本地管理撤权已实现。认证服务失败不产生新会话或替代用户，已有会话到原期限结束。

## 验证与剩余门槛

已执行认证交换、两用户各两空间交叉拒绝、稳定身份／会话恢复、并发回调、浏览器绑定／重放／过期、部分登记恢复、unknown 提交重启核实、停用／启用、已有流及执行凭据撤销、未结清对象保留、本机管理 CLI、启动装配与跨模式拒绝的定向测试。最终检查包括相关 Go race、`go vet ./internal/authn/... ./internal/room`、243 项前端测试及 diff 检查，均通过；测试替代了外部 Provider 或执行选择的场景，不代表真实 OpenCSG 或 Linux SRT 组合。

真实本机浏览器 SSO 入口由显式 e2e 测试提供：

```sh
TEAMX_SSO_ACCEPTANCE_HOME=/absolute/fresh/private-directory \
go test -tags=e2e ./internal/room -run '^TestOpenCSGSSOLoopback$' -v -count=1 -timeout=35m
```

测试监听 `127.0.0.1:18088`，等待用户在本机浏览器完成登录，只写入不含 token 的私有身份收据；不授予业务访问，用以取得管理员允许名单所需的服务端稳定主体。它验证登录事务与真实身份交换，不能替代真实 Linux 多用户、双账号及撤销验收。

远端验收可显式提供 `TEAMX_SSO_ACCEPTANCE_LISTEN=127.0.0.1:8088` 和 `TEAMX_SSO_ACCEPTANCE_ORIGIN=https://assistant.example.com`。监听仍限于 loopback，复用正式 Host／可信代理校验，只信任 loopback TLS 代理的 `X-Forwarded-Proto: https`；代理须保留 Host 并移除 `X-Forwarded-Host`／`Forwarded`。验证页使用 `Referrer-Policy: same-origin`，使浏览器原生表单 POST 保留有效 Origin；敏感登录／回调继续使用 `no-referrer`。只有真实受保护身份交换成功并写入收据，才计为登录验证通过。

S3b.2a 的真实飞书 OAuth／刷新／DPoP 和 S3b.3 的正式出口继续依原记录判断，生产 `multi-user` 启动门槛没有移除。本轮不改写这些前置状态，不声明 Gordis 插件化、S4b、迁移、内置账号、可选 Computer 或 S5 完成。

第一轮真实本机验证已启动并等待十分钟，但未收到成功回调而到期，不计作 SSO 协议通过或错误凭据测试；未生成身份收据。第二轮延长为三十分钟等待用户浏览器操作，仍不以“已打开页面”代替完成登录。

2026-11-25 远端 alpha HTTPS 实测先后暴露表单 Origin 和上游 `untrusted signin callback target`；修正表单并重新发起登录后，用户完成 OpenCSG／IAM 登录，TeamX 收到真实回调并通过受保护身份交换，取得 0600 身份收据。单账号真实 SSO 验证已通过，不等同于安装产物的多用户业务会话及完整 Linux 组合验收。当前协议复用 OpenCSG 的 Casdoor 客户端：IAM 回调 OpenCSG，OpenCSG 再按 state 中的目标回调 TeamX，无需另建 TeamX Casdoor 客户端。详情见[空间部署记录](space-deployment.zh.md)。
