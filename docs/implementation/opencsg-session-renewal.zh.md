# OpenCSG 浏览器会话续期实施记录

日期：2026-11-26。状态：代码及本地验证完成，Linux 已升级，真实刷新验收因 IAM 应用配置暂缓。按用户调整，先实现和验证，再在 Linux 服务确认实际登录／刷新合同。设计依据：[续期方案](../OPENCSG-SESSION-RENEWAL-PLAN.zh.md)。

## 实现

- `authn.RenewableProvider` 分开完成登录、Refresh 和 Verify；Identity 不包含凭据。OpenCSG 长期登录请求 offline_access，必须取得 refresh token；保留身份探针的普通登录合同。刷新允许没有 ID token／nonce，出现时校验签名及身份声明，不扩大 scope。
- 宿主 `access_session_renewal.go` 统一维护 v3 私有快照、绝对期限、远端复核、revision、每会话单个在途任务、有界外呼与退避。使用宿主 context，取消一个等待者不取消共享续期。落盘先于新认证期限发布，轮换后的 Grant 先于 userinfo 保存。
- 全部会话变更使用专用提交锁，鉴权锁不跨磁盘保存或 IAM 请求。退出、停用和替换使旧结果失去发布资格；提交不确定暂停浏览器认证，关闭时等待任务及提交结束后再关闭 writer。
- 保持个人格式及行为。多用户 OpenCSG 的 v2 登录失效升级到 v3；新登录默认 7 天，配置最长 30 天，重启只可缩短。每用户 4 个、总计 256 个、16 MiB 文件上限，读取上限同步提高，普通配置仍为 1 MiB。
- 同次登录完成准入及本人 Gateway Key 初始化；续期不读取 Key 或改变模型。session／renew／logout 位于 Space 业务准入之前；平台 OAuth 回调在最终鉴权后才交给业务。
- SSE 保留固定检查 lease、撤销及重连语义。前端单一会话控制器在可见页面续期、隐藏时暂停、恢复时检查；503 不跳登录且阻止新业务写，401 只导航一次，身份匹配后恢复允许的本地位置。沿用现有隔离草稿，业务写不自动重放。

## 本地验证

覆盖 Provider 初始及刷新合同、签名和身份拒绝、无 ID token／nonce、refresh token 保留及轮换、scope 限制、错误脱敏；会话期限及重启、策略缩短、v2 清理、并发等待者及取消、跨用户并发提交、退出／停用／替换、临时失败及恢复、轮换后保存失败、rename／sync 不确定及重启、关闭结清、慢磁盘锁边界、256 会话与文件容量、SSE 固定 lease、归档后的续期／退出及 Gateway 初始化。

执行命令：

```sh
go test ./internal/authn/... -count=1
go test -race ./internal/room -run '^Test(AccessSession|Hosted|OpenCSG|ExternalLogin|UIUpdates|UIUpdate)' -count=1 -timeout=8m
node --test internal/web/tests/web*_test.mjs
go vet ./internal/authn/... ./internal/room
go test ./... -run '^$'
```

日志保存在忽略的 `.local/session-renewal-*`。本地结果只证明替代服务与组件合同，不替代真实 IAM 或浏览器安装验收。

## Linux 与 IAM 验收

初始验收目标（实际结果见下文）：以开发候选升级现有 Linux 服务，核对私有身份及模型数据保留、v2 清理与 v3 权限；正常浏览器授权取得 refresh token，记录 token TTL／轮换及 userinfo 行为；自然 access token 到期前后自动续期、续期后重启、临时 IAM 故障及恢复、SSE 重连、退出与受控账号本地停用。现有个人模式回归与组合出口仍独立判断。

真实 grant 撤销、账号冻结、全局登出尚未验证；不能根据 discovery 声明传播保证。冻结／撤销只操作明确授权的测试账号或 grant。脱敏收据留在本地或服务器私有证据目录，不提交 token、原始日志或浏览器凭据。

## 本地验证（2026-11-26）

已通过：

```sh
go test ./internal/authn/... -count=1
go test -race ./internal/room -run '^Test(AccessSession|Hosted|OpenCSG|ExternalLogin|UIUpdates|UIUpdate)' -count=1 -timeout=8m
node --test internal/web/tests/web*_test.mjs
go test ./internal/web -count=1
go vet ./internal/authn/... ./internal/room
go test ./... -run '^$'
```

完整前端回归共 906 项。相关 Go race 回归包含取消等待者、并发提交、退出／停用／替换竞争、Grant 保存后 userinfo 暂时失败及重启恢复、rename／目录 sync 不确定后受控重启、慢磁盘不持鉴权锁、256 条且超过旧 1 MiB 读取上限、字节超限、固定 SSE lease 与归档后的控制接口。登录容量／存储错误使用固定无敏感参数的回跳标记，并已单独回归。

这些结果来自可控时钟、替代 IAM 和持久化 fault seam。真实 IAM 应用签发、自然 access token 到期、远端撤销传播及 Linux 安装行为依下节记录，不以本地通过代替。

## Linux 与真实 IAM

已从隔离源码快照构建并升级 Linux amd64 standard／SRT 开发实例，构建标识 `dev-d56391bbe641`（快照提交仅用于验收产物追溯，未提交当前工作区）。归档 SHA-256：`453d7548506b` 前缀；完整摘要、升级备份及清单仅保存在私有证据目录。使用正式离线升级器，未执行发布／备份保留清理。

安装启动及 HTTPS 登录入口核对通过：2 个用户、2 个 Space、3 个 Room、历史序号、安装登记、认证源／client／secret 路径和 12 个保护文件全部保持一致。v2 已升级为 v3。已观察到一次真实 IAM 登录，Grant 含 access／refresh token，文件没有保存原始 ID token；绝对期限从初次登录起恰为 7 天。HTTPS Cookie、PKCE S256、`openid profile offline_access` 和前端控制器资源返回均核对通过。

实际 IAM access token 有效期也是 7 天，refresh expiry 未声明，不猜测填写。当前无法用短时观察代替自然 access token 到期验收；15 分钟认证窗口到期前执行 userinfo 复核。另用受停服锁保护的显式 e2e 探针检查真实 refresh、最新 Grant 保存、userinfo 及读盘恢复；该探针不构造 Cookie，不改变绝对期限，不建立强制续期 HTTP 入口。探针结果单独记录，不能标记为自然到期通过。

真实登录由用户正常完成，不代填凭据。真实浏览器 SSE 重连、普通退出及本地停用／恢复、IAM 暂时故障后恢复和远端撤权传播仍需逐项记录；替代服务已有相应边界覆盖。未自动执行账号冻结、grant 撤销或全局登出。

### 已发现的 IAM 配置前置

真实停服探针在既有 Home／Space 运行锁保护下，以服务账号执行；第一次实际 Refresh 返回 `client_configuration`，服务随后已恢复。进一步使用无效占位 refresh token（不消费真实 Grant）的有界请求核对，绑定的 token endpoint 返回 HTTP 400 `unsupported_grant_type`。因此 discovery 的全局声明及登录响应含 refresh token 均不能证明该应用已启用刷新。

当前需在现有 TeamX IAM 应用保留 Authorization Code 并允许 Refresh Token grant。未更改 IAM 应用、未切换到未声明的其他端点，也未将错误判为用户失效或停用用户。已有浏览器会话保留，后台业务服务已恢复。应用配置修正前，真实刷新／轮换／刷新后恢复不记为通过；独立探针 `TestOpenCSGStoppedSessionRefresh` 已准备，可在相同锁边界下继续验证，测试默认跳过，必须显式提供配置、目标用户和私有收据路径。

其余真实门槛仍保留：自然 access token 到期、页面无交互的提前复核、SSE 到期重连及状态补齐、IAM 暂时故障恢复、本地退出／停用，以及已授权测试 grant 的远端撤销。没有对实际账号执行冻结／全局登出或复用旧 refresh token 的破坏性检查。

### 当前交付边界（用户决定暂不启用 IAM 刷新）

2026-11-26 用户选择保留本轮代码和续期能力，暂不启用 IAM 应用的 Refresh Token grant。停止继续真实刷新／撤权检查，Linux 开发实例维持当前版本和已有登录，未修改 IAM 应用。

当前默认本地绝对期限为 168h，实测该应用 access token 也为 168h。15 分钟是远端 userinfo 复核窗口；IAM 和本地身份均有效时无需每 15 分钟重新登录。当前被观测浏览器登录创建于北京时间 2026-11-26 21:22:04，绝对期限为 2026-12-03 21:22:04，重启不延长。其他浏览器分别从各自初次登录计算；退出、停用或远端明确失效可提前终止。到达绝对期限后需正常重新登录，即使以后启用 Refresh Token grant，刷新也不延长已有会话的绝对期限。

状态为“代码及本地验证完成，Linux 开发实例升级与真实登录／原会话重启恢复已验证；真实刷新验收暂缓”，不声明方案的最终组合验收全部完成。首次刷新探针因应用配置被拒绝，未轮换当前 Grant；随后服务已恢复 `ready / running`，原 v3 会话及绝对期限保持。

### 远端复核窗口调整

按用户决定，源码将远端身份复核窗口由 15 分钟改为 1 小时，初次登录、成功复核和持久快照校验共用同一常量。仍受 access token 到期限制，最长提前 60 秒检查，SSE 沿用相同认证期限；7 天绝对登录期限和本地停用的立即撤销保持不变。已有会话的持久检查期限不会因重启直接延长，成功复核后才应用新窗口。

此调整已升级至现有 Linux 实例 `dev-297aa4cd027e`（归档 SHA-256 `4a9cff9cb7c0c311df42a3914e8d08304687b9a2f654472333312d6f14c0f427`），服务 `ready / running`。2 个浏览器会话的 User、创建时间、7 天绝对期限及 Grant 保持，重启未延长已有 15 分钟检查窗口；下次成功远端复核后使用 1 小时窗口。2 个用户、2 个 Space、3 个 Room 及历史序号保持。

升级前在线清单与升级后相比，有一份平台连接文件摘要变化；该文件及其余保护文件与升级器停服时的一致备份逐项匹配，未将在线变动当作升级覆盖。私有升级备份、核对收据和日志保留，未执行保留清理。IAM Refresh Token grant 仍保持未启用，未重复真实刷新／撤权探针，真实刷新验收继续暂缓。

本次 Provider 测试、会话／SSE race 回归、`go vet` 及 e2e 标签编译检查通过。新增覆盖 1 小时登录／复核窗口、59 分钟提前调度、短 access token 到期限制、超过 1 小时的窗口拒绝，以及重启保留原绝对期限和旧检查期限。上文 `dev-d56391bbe641` 及 15 分钟窗口保留为原始验收记录。

### 临时复核故障的自动恢复（2026-11-27，本地源码）

按用户确认，将一小时窗口的复核提前量调整为 10 分钟，短 TTL 使用窗口四分之一与 10 分钟的较小值。网络超时、429、IAM 5xx 等明确临时故障允许固定宽限至 `verified_until + 5m`，并截断到当前 access token 和登录绝对期限；成功复核才更新持久验证期限。宽限由原快照计算，重试和重启均不延期，重启后仍需实际检查才能重新确认临时故障。客户端配置／未分类错误、存储故障和明确失效不获得宽限，本地退出／停用仍立即撤销。

会话元数据增加 `usable_until`，HTTP 和新 SSE lease 共用这一截止时间，既有流不延期。可见页面按服务端退避期限重试；后台检查临时失败在已授予期限内静默，超过宽限仍失败才提示并暂停新业务操作，保留草稿，恢复后自动解除。首页和 Space 导航返回不含用户数据的 HTML 恢复页面，依赖静态资源无需先完成远端认证；页面自动读取会话，成功后重新加载原地址及 hash，业务 POST 不重放。

本地验证：会话、Hosted 路由和 SSE 的定向 integration race 回归通过；922 项前端测试、`go vet ./internal/authn/... ./internal/room ./internal/web` 通过。新增用例覆盖宽限内继续访问、重试／重启不延期、SSE 截止与撤销、各类失效边界、期限外 API 仍返回 JSON 503，以及恢复页资源可加载。Chromium 使用本地故障替代服务验证连续两次失败后的自动恢复、原 Space 路径及 hash 保持、无页面脚本错误和业务写重放。此记录不代表真实 IAM 故障或 Linux 已升级；本轮未部署。
