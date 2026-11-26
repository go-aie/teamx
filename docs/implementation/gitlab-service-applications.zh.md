# GitLab 服务应用与空间私有授权

日期：2026-11-24。对应多空间方案 S4b。应用来源、受控 OAuth 交换／刷新／撤销、本地管理员入口、用户界面和空间绑定已接通；组件与 HTTP 生命周期验证见下文。真实 GitLab、多账号浏览器与选定 Linux 产物的 S5 验收仍须单独通过，本记录不声明托管 GitLab 正式支持。

## 共用业务与归属

`internal/platform/gitlaboauth.Source` 提供应用查找、版本检查和一次操作的受控使用权。个人实现读取原空间注册表；服务实现读取控制面应用目录。授权、PKCE、账号核验、grant 提交、刷新 singleflight、连接 generation、通知、工具调用和断开继续使用原实现。业务编排不接收 `deployment.mode`；部署装配只选择应用来源。

服务应用保存在部署 Home 的 `gitlab-applications.json`，文件权限为 0600，使用控制面的原子 writer。Secret 只在受控实现内用于 OAuth 请求，不返回普通用户，不写入空间配置、平台快照或 Agent 环境。管理员返回值和影响清单也不包含 Secret。

服务应用与登录认证 Provider 无关。OpenCSG 部署固定使用同一 Home，日常升级保留原目录；`service upgrade` 在停服后记录已有应用目录，启动后核对完整配置仍然一致，缺失、不可读或变化时报告失败并保留备份，不进入远程部署的成功清理。核对包括应用身份、版本及私有凭据，但输出不包含目录内容。未配置过应用的部署仍允许升级；恢复备份继续使用其原有整目录恢复语义。切换认证源若需新建 Home，应显式带入服务应用，用户和个人授权另按身份边界处理。

空间的 `platform-connections.json` 保存无 Secret 的应用引用与版本，以及各自的 access／refresh token。服务 grant 增加本地 `owner_user_id`、`space_id` 和授权 `operation_id`；加载时核对实际空间 owner，复制另一空间的快照不能认领其 grant。GitLab 应用所有者和授权用户分别保存，授权不要求用户拥有应用。

个人与共享应用、默认与其他空间统一使用固定回调 `/v1/platforms/gitlab/callback`。授权开始时固定用户、空间、运行实例、应用版本、操作、state、PKCE 和浏览器 Cookie；公共入口按临时 state 绑定定位原运行实例，原空间重新验证登录主体和空间权限。路由不再由共享应用目录保存；state 在消费、取消或授权结束时移除，过期和已停止的运行实例不能接收回调。短时 OAuth Cookie 使用根路径，以同时覆盖空间内操作与共享回调；Cookie 名包含独立会话 ID，并保持 HttpOnly／SameSite=Lax 及 HTTPS Secure。完成页显式返回原空间，不受当前默认空间或其他标签页影响。重启结束未完成授权，不重放授权码。带空间前缀的回调入口不再接收授权；曾使用此地址的个人应用需显式更新 GitLab 和 TeamX 应用配置后重新授权，已有 grant 不自动改绑。

应用 Secret 轮换、停用和重新启用均增加版本。控制面先持久保存新状态，再取消旧使用权并等待实际结清；旧授权／刷新不能提交为可用结果，已有 grant 不因 Secret 轮换而被宣称远端撤销。在途 API 调用持有应用使用权直到响应结清。刷新已发送而结果不确定时保留原持久 intent 和不可用状态，重启不重试旧 refresh token。

## 部署与管理员操作

仅在已选多用户组合的部署配置中增加：

```toml
[deployment.gitlab]
enabled = true
```

个人部署不填写此项，继续在当前空间创建／导入私有应用。未选托管 GitLab 的多用户组合使用显式禁用实现；界面隐藏入口，直接调用授权、导入、连接和相关业务入口也不能借用个人应用或宿主登录。

管理员先在目标 GitLab 配置 confidential OAuth application，将回调设为 TeamX 规范 HTTPS origin 加 `/v1/platforms/gitlab/callback`，允许 `api`。应用可包含 `write_repository` 这一最大 scope，但它不启用托管仓库执行；当前多用户界面按执行能力默认只申请 API 权限。GitLab 实例、Client ID、所有者、回调和最大 scopes 构成固定应用身份；改变这些字段需登记新的应用，不能把现有 grant 静默改绑。

应用凭据不填写到默认 `config.toml`、安装模板、部署脚本或仓库文档中。`config.toml` 只保存上面的启用开关；应用 ID／Secret 由管理员通过私有文件单独导入。输入文件应放在仓库之外、权限为 0700 的私有目录中，并在写入凭据前设为 0600，例如：

```json
{
  "version": 0,
  "application": {
    "name": "公司 GitLab",
    "origin": "https://gitlab.example.com",
    "owner_id": 123,
    "client_id": "GITLAB_APPLICATION_ID",
    "secret": "GITLAB_APPLICATION_SECRET"
  }
}
```

```sh
chmod 600 /private/path/gitlab-application.json
teamx access gitlab-applications configure --config /absolute/config.toml --input /private/path/gitlab-application.json
teamx access gitlab-applications list --config /absolute/config.toml
```

以上命令以部署服务账号运行；例如 root 运维时通过 `sudo -u SERVICE_USER -- teamx ...` 调用，并确保输入文件及其父目录只向该账号开放必要读取权限。CLI 校验管理员 token 的文件属主，不能直接用 root 身份借用服务账号的 token。

首次配置由 CLI 生成应用 ID，并补齐服务来源、默认回调、active 状态和 `api` scopes。这里的 `owner_id` 是应用所有者的 GitLab 原生用户 ID，Client ID 是 GitLab 展示的 Application ID。

首次启用时，先在部署 Home 的 `config.toml` 增加开关，再按服务生命周期正常重启，使控制面加载应用目录，然后执行 `configure`。不要仅在输入文件中填写凭据后就认为平台已接入；以 `list` 返回 active 应用、回调与服务 HTTPS origin 匹配为准。导入确认成功后可删除临时输入文件，后续轮换重新准备私有文件；不要将 Secret 放在命令行参数、shell history 或公开验收输出中。

导入后的应用凭据以明文 JSON 保存在部署 Home 的 `gitlab-applications.json`，依靠私有目录与 0600 文件权限保护，不是默认配置或可分发资源。包含该 Home 的备份同样属于敏感资料。各用户的 OAuth token 仍由本人空间保存，共享应用不会替任何用户自动创建连接或完成授权。

轮换时把 `list` 中的完整应用公开元数据放回 `application`，填写新 `secret`，外层 `version` 使用已观察的 `application_version`。停用时把 `status` 改为 `disabled`，可省略 `secret` 以保留控制面原值；重新启用使用 `active`。实例、Client ID、回调等固定身份字段保持一致。服务不会替管理员修改 GitLab 远端应用，也不会把本地轮换等同于远端 Secret 已轮换。

管理接口是 `/v1/deployment/gitlab-applications`，沿用本地回环地址＋私有管理员 token 的独立入口，拒绝浏览器 Origin、代理转发与普通用户／执行凭据。`list` 同时展示各空间持久 grant 和在途操作的影响范围。更新超时先检查 `list`，再对原应用结清，不重复提交旧版本：

```sh
teamx access gitlab-applications settle --config /absolute/config.toml --application APPLICATION_ID
```

保存结果未知时目录关闭新的授权与调用；保留原文件和 writer 状态，重启重新核实。不覆盖失败现场。

## 用户使用与远端撤销

用户在空间内添加 GitLab，使用服务提供的平台授权本人账号，无需填写 Client ID／Secret。只有一个服务应用时自动选中，隐藏平台选择下拉，只展示不可编辑的实例地址；多个应用时明确选择。配置缺失或不兼容时提示联系管理员，不能转入个人导入流程。Agent 发起的连接卡片也使用同一目录和授权编排，不能修改服务应用。

Agent 的 `platforms list` 返回 `gitlab_managed`、`gitlab_enabled` 及公开应用列表。托管模式只存在一个兼容应用时，直接使用返回的实例和应用发起授权；多个候选才选择，缺失或不兼容时提示联系管理员。个人应用保留原配置流程。

未启用托管 GitLab 时，平台类型列表不提供 GitLab，并默认选择实际可用的类型；没有可用类型时显示部署未开放平台的提示，不请求已禁用的平台接口。从其他类型切回 GitLab 时重新读取服务应用目录，托管模式始终隐藏应用凭据字段。

用户断开只提交本人空间的本地不可用状态，保留连接身份及原业务结清规则。远端撤销经受控接口执行，不取得 Secret。执行前在应用门闩内检查各空间持久 grant、其他空间未确认候选和在途操作；存在同一原生用户的共享 grant、未知候选或无法确认的影响时保留撤销材料，显示本地已断开／远端未确认。撤销期间不接纳新应用操作。不同原生用户的独立 grant 可以受控撤销；重启不重放未确认的破坏性请求。

同一 GitLab 用户重复授权不同空间可能关联远端 grant，不能由本地分开保存 token 推断远端完全独立。Token 不跨空间复制，服务应用停用不替代用户远端撤权。

## 验证及交付边界

定向验证覆盖：

- 两个非应用所有者并发授权；跨浏览器 Cookie、跨用户／空间、错误 origin／应用、重放拒绝。
- 私有 grant 的 owner／空间／操作绑定；空间中不保存服务 Secret；复制快照拒绝。
- 单用户刷新、scope 变化和断开不改写另一用户 grant；独立账号远端撤销与共享原生账号保护。
- 轮换／停用取消旧授权、刷新及 API 使用权，并等待原调用结清；旧结果拒绝；未知保存不降级。
- 规范共享回调返回非默认空间；用户会话、应用引用及 grant 正常重启保留；用户在交换期间停用后，旧结果不能激活 grant。
- 管理员本地 CLI 配置、轮换、停用、列出影响及结清；普通用户管理拒绝。
- 个人自建／导入、所有者核验、应用替换、授权、刷新、退出及业务 OAuth 的原路径回归。

复核命令：

```sh
go test -race ./internal/platform/registry ./internal/platform/gitlaboauth -count=1
go test -race -tags integration ./internal/room -run 'TestServiceGitLab|TestHosted|TestGitLab(Auth|Connect|Setup)|TestOAuth|TestSpacesScopedCLIAndCallback|TestSharedHostTools|TestSharedCapacity' -count=1 -timeout=180s
node --test internal/web/tests/web_gitlab_oauth_test.mjs internal/web/tests/web_platform_onboarding_test.mjs internal/web/tests/web_platform_connect_test.mjs internal/web/tests/web_platform_availability_test.mjs
NODE_PATH="$PWD/packaging/verifier/node_modules" TEAMX_GITLAB_SERVICE_BROWSER=1 go test -tags e2e ./internal/room -run '^TestServiceGitLabBrowserApplicationSelectionAndAuthorization$' -count=1 -timeout=90s
```

以上定向测试、竞态检查、71 项 Web 回归和 Chromium 应用选择／授权闭环已通过。Chromium 检查包含隐藏应用凭据字段、按能力申请 API 权限、原生页面跳转与授权后自动显示连接。

2026-11-25 平台入口修复增加了禁用 GitLab 时选择可用类型、没有可用类型时阻止连接，以及切回 GitLab 时重新加载服务应用的回归。当前上述 Web 命令共 93 项通过，Chromium 服务应用选择／授权闭环再次通过；均使用受控响应，不替代真实用户在 GitLab 上登录授权。

本地 HTTP／Chromium 使用受控 GitLab 响应和临时目录，不接入真实账号，不执行真实外部写入。S5 仍需在所选 Linux 产物上用真实 GitLab 验证两账号授权、刷新、重启、撤权及明确授权的 API 操作；S3、S4a 尚未关闭的真实组合门槛继续保留。S3b.2b 托管 Git、S4c／S4d 和 Gordis 装配不在本单元范围内。
