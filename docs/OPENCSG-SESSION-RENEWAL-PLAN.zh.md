# OpenCSG 登录会话与自动续期方案

日期：2026-11-26。状态：**代码及本地验证完成；Linux 已升级，真实刷新验收暂缓**。用户已调整实施顺序为先完成代码和必要本地验证，再在现有 Linux 服务验证真实合同与安装行为。本文保留设计与验收目标，实际实现及证据见 [实施记录](implementation/opencsg-session-renewal.zh.md)；未验证的远端能力不视为已通过。

## 1. 结论与交付范围

采用“**长期 TeamX 本地会话 + 宿主服务端维护 IAM 凭据 + 到期前按需续期**”。作为一个完整改动实施、验证并交付，实施中设置三个连续检查点：IAM 行为确认、完整代码实现、组合验收。不先交付临时延长到 8 小时的版本，也不拆成多轮功能上线。

推荐默认登录策略：最长 7 天，不设置闲置超时。页面打开时按需自动续期，不检测点击、输入或滚动等用户活动；页面关闭后无需持续刷新，下次访问时检查并恢复。刷新不延长这 7 天的绝对期限，提前退出、停用或 IAM 凭据失效仍按既定规则处理。临时网络故障提供恢复机会，确实需要重新认证时保留草稿与页面位置。

本轮覆盖多用户 OpenCSG 登录、凭据持久化、并发刷新、重启恢复、退出和本地停用、SSE、会话限额与前端恢复。个人版登录行为保持原有语义；Room、模型调用、平台授权不接入 IAM token。

改动集中于 Provider、宿主会话 owner 和 HTTP／前端接入。业务继续接收原有认证主体与 Space 上下文，不增加刷新状态、认证重试或 IAM 可用性判断。采用现有存储与连接恢复能力，接受升级时重新登录、SSE 受控重连，以及少数无法安全恢复的轮换故障需要重新登录。

明确边界：

- 普通退出或 IAM 登录失效只结束相关浏览器会话，不等于停用 TeamX 用户，不停止后台 Agent。
- 管理员本地停用继续通过现有用户状态、`AuthVersion`、运行时撤销及结清机制完成。
- 不自动执行 IAM 全局登出，不联动撤销 GitLab／飞书授权，不修改用户模型选择或 Gateway Key。
- 本轮不引入数据库、分布式会话服务、通用 OAuth 凭据平台或静默 iframe 登录。

## 2. 当前实现与需要修改的接点

以下依据当前源码，部署中的二进制版本需在真实验收时另行核对。

| 接点 | 当前行为 | 本轮责任 |
| --- | --- | --- |
| [authn.go](../internal/authn/authn.go) | Provider 只提供登录地址、完成登录；Identity 不包含凭据 | 增加窄的可续期认证合同，身份与凭据继续分开 |
| [oidc.go](../internal/authn/opencsg/oidc.go) | 授权码、PKCE、nonce；请求 `openid profile`；`ValidUntil` 来自 ID token | 获取 refresh token，增加刷新及身份复核，保留现有验证与端点约束 |
| [external_login.go](../internal/room/external_login.go) | 宿主持有一次性 state、浏览器绑定及 admit 边界 | 在相同事务内交付登录结果；凭据落盘后才发 Cookie |
| [hosted_users.go](../internal/room/hosted_users.go) | `externalSessionLifetime = 15m`；期限再受 `Identity.ValidUntil` 限制 | 分离本地会话绝对期限及远端认证检查期限 |
| [access_sessions.go](../internal/room/access_sessions.go) | v2 单文件快照，仅保存 Cookie 摘要、主体、期限；全局最多 16 条 | 多用户 v3 保存会话及私有认证材料；统一串行提交，磁盘写入与鉴权读取锁分离 |
| [server_access.go](../internal/room/server_access.go) | 中间件检查固定到期时间；退出删除当前会话 | 在业务处理前统一检查／续期，增加明确错误分类 |
| [space_server.go](../internal/room/space_server.go) | 外层路由先调用 `browserSubject` 选择默认空间、检查 owner | 区分本地路由提示与完整认证，避免续期尚未执行就返回 403 |
| [access_settings.go](../internal/room/access_settings.go) | session 响应只含用户、安装和空间信息 | 增加不含凭据的期限与续期状态 |
| [model_defaults.go](../internal/room/model_defaults.go) | 登录装饰器调用 `CompleteWithToken`，取本人 Gateway Key | 同一次授权码交换结果用于模型初始化和登录；刷新不重复初始化模型 |
| [ui_updates_http.go](../internal/room/ui_updates_http.go) | 建连时复制期限并设置固定定时器 | 保留固定 lease，认证检查期限到达后允许受控重连 |
| [user_management.go](../internal/room/user_management.go) | 停用先持久增加认证版本，再撤销登录、流与所属运行时 | 保持顺序，将在途刷新纳入失效与关闭管理 |
| [client.mjs](../internal/web/assets/client.mjs)、[updates.mjs](../internal/web/assets/updates.mjs)、[access.mjs](../internal/web/assets/access.mjs) | 401 跳转登录；SSE 失败通过 session 接口检查认证 | 单一续期 owner、临时故障反馈和登录后恢复 |

当前服务器计算的期限为 `min(登录时间 + 15m, ID token.exp)`；Cookie 的 Max-Age 则固定为 15 分钟。因此只改 IAM token 期限或只改 Cookie，均不能完成目标。

2026-11-26 只读核对 [IAM discovery](https://iam.opencsg.com/.well-known/openid-configuration)，确认其声明 `refresh_token`、`offline_access`、userinfo 和 introspection；尚未证明 TeamX 应用获准签发 refresh token，也未证明远端冻结／撤销的实际传播行为。不能把 discovery 支持声明写成真实验收结果。

## 3. 会话与期限设计

### 3.1 两种独立期限

| 项目 | 规则 |
| --- | --- |
| 绝对期限 `absolute_expires_at` | 初次登录时间 + `session_max_age`，默认 168h；刷新不延长 |
| 远端检查期限 `verified_until` | 最近成功远端复核时间 + 1h，并受当前 access token 有效期限约束 |

本地会话存在不代表任何时候都可执行请求：用户状态／认证版本、绝对期限都必须有效；远端检查已过期时，先成功复核才能继续业务处理。

ID token 仍在登录时验证 `exp`，但不再用它直接裁剪本地会话的绝对期限。后续身份依据由受控刷新及远端复核更新，不能通过删除期限检查直接获得长期访问。依据见 [OIDC ID Token](https://openid.net/specs/openid-connect-core-1_0.html#IDToken)。

### 3.2 配置

拟在现有部署认证配置增加一项，下面是拟议配置，并非当前已支持字段。它仅作用于 `deployment.mode = "multi-user"` 的 OpenCSG 登录；当前多用户部署要求 Linux＋SRT，个人版继续沿用原登录策略。

```toml
[deployment.authentication]
# provider、issuer、client_id、client_secret_file、准入与 hub_origin 沿用现有配置
session_max_age = "168h"
```

`session_max_age` 为正 duration，默认 168h，最长上限暂定 30 天，超限或格式错误在启动时明确拒绝。远端检查窗口 1 小时作为内部安全默认值，本轮不增加调节开关；refresh token 有效期由 IAM 决定，不能用本地配置延长。

重启读取新策略时，以持久的原登录时间重新计算，并取旧期限与新策略计算值的较早者，避免重启或调大配置自动延长已有会话。新策略的更长期限在下次重新登录时适用。

## 4. 认证职责与登录接入

### 4.1 Provider 与宿主边界

保留现有 Provider 基础合同，新增可选的可续期能力，供多用户 OpenCSG 装配使用；生产 OpenCSG 配置必须具备此能力。个人版及纯身份验证探针不因此获得长期会话。

新增能力只围绕以下认证过程组织，具体 Go 名称可在实现时确定；不要求为每个过程独立建设一套公共扩展接口：

1. **完成登录**：单次消费授权码，返回已验证 Identity、私有 Grant 及认证检查结果。
2. **更新 Grant**：对绑定的 IAM token endpoint 执行 refresh grant，返回最新凭据；不创建 TeamX 用户或会话。
3. **复核身份**：验证可用 token 及远端身份，确认 provider／issuer／sub 仍与原登录相同。

`Identity` 不添加 access／refresh token 字段。Grant 只在认证模块与宿主私有会话 owner 间流转，不进入 `users.json`、Space、业务 Session、模型参数、HTTP 响应或日志。避免让 `oauth2.TokenSource` 脱离宿主会话 owner 自行轮换，导致最新 refresh token 没有持久提交。

### 4.2 初次登录顺序

沿用现有五分钟登录尝试、state、nonce、PKCE、浏览器绑定和回调 URL：

1. 请求 `openid profile offline_access`，按 IAM 应用实际要求完成 consent；保留 confidential client 的客户端认证。
2. 交换并验证登录结果、userinfo 主体及 refresh token 可用性；缺少 refresh token 时明确拒绝本次长期登录，不静默降级为 7 天 Cookie。
3. 通过现有准入与预约逻辑确定稳定 User／默认 Space；本地 disabled 用户不可登录。
4. 配置 `hub_origin` 时，使用同一次交换的 access token 获取本人 Gateway Key，保留现有账号归属、Key 保存与模型初始化语义。
5. 宿主持久提交完整会话后发 Cookie，清理回调参数并跳回固定本地入口。

调整 `modelBootstrapLogin`，避免装饰器丢失新增的 Grant／续期能力；不能对同一授权码调用两次 Complete。准入、Key 或会话保存失败均不发业务 Cookie。原 User／Space 预约可以按已有机制留待下次补齐。

refresh 流程只处理认证材料，不再次取 Gateway Key，不触发模型验证，不覆盖用户自选模型。

### 4.3 刷新结果的验证

保留当前 HTTPS、配置 issuer、端点固定、禁止重定向、超时、大小上限及错误脱敏要求。读取 scope 不得扩大原授权范围。

- refresh 响应未返回新 refresh token 时保留原值；返回新值时必须更新，不能继续持有旧值。
- 新 ID token 若存在，验证签名、issuer、subject、audience、azp 和有效期等；nonce 可省略，出现时须与原登录 nonce 一致。`auth_time` 表示原认证时间，不得当成新的登录起点。
- refresh 响应可以没有 ID token，不能原样复用初次登录的“必须有 ID token／nonce”判断。此时通过新 access token 的 userinfo 绑定原 issuer／sub。
- 远端复核复用现有 userinfo 及主体一致性校验。本轮不为多种远端账号状态来源新增可配置策略；introspection 等能力留待有明确需求与真实合同后单独接入。

refresh 成功不天然证明账号未被冻结。记录真实 IAM 对 grant 撤销、账号冻结与全局登出的实际响应；收到明确失效或身份不一致仍须撤销对应本地会话。TeamX 保证本地停用生效、认证过期前复核以及可观测失效时拒绝访问，不承诺固定的 IAM 远端撤权传播时限。IAM 未提供的冻结／全局登出联动不作为本次交付的新增硬门槛，也不借此扩展为 IAM 改造项目。[OIDC refresh 规则](https://openid.net/specs/openid-connect-core-1_0.html#RefreshTokenResponse)、[OAuth refresh token 保护](https://www.rfc-editor.org/rfc/rfc9700.html#section-4.14)

## 5. 私有存储与重启恢复

### 5.1 复用单份快照

继续使用部署 Home 的 `auth/browser-sessions.json`，多用户升级为 v3；会话与其 Grant 在同一条记录、同一次原子快照中提交。不另设 token 文件、凭据索引或跨文件事务。Cookie 原值仍不保存，只以 SHA-256 摘要索引。

拟议记录包含：

| 类别 | 字段／含义 |
| --- | --- |
| 主体 | UserID、AuthVersion；provider／issuer／sub 与用户目录绑定 |
| 期限 | created_at、absolute_expires_at、verified_at、verified_until |
| 私有 Grant | access token、refresh token、access expiry、已知的 refresh expiry；验证所需的原始 nonce／audience／auth_time 等最少元信息 |
| 状态 | revision，用于在途刷新与撤销／替换的发布校验 |

不持久化原始 token endpoint 响应、原始 ID token、无关用户信息或待验证 token 队列。ID token 在内存中完成校验，只保存后续验证所需的最少元信息；验证无法完成时不增加授权期限，无法安全恢复的轮换允许重新登录。记录中未知的 refresh expiry 不猜测填写；`invalid_grant` 为最终判断之一。

继续将安装、部署模式、provider、issuer、client_id 纳入绑定。`0600`、当前服务账号 owner、普通文件及防符号链接检查必须保留；父目录采用现有私有目录权限。会话文件现在包含可用 IAM 凭据，运维查看和备份按凭据处理，不打印内容；不额外引入一把与文件放在相同目录的“加密密钥”。

恢复时检查时间字段关系、记录版本、主体／Grant 绑定及大小；损坏或权限不符时拒绝使用并保留证据，不解释为全新用户目录。对已过期、已停用或旧认证版本的条目执行持久裁剪。首次升级识别到合法的旧 v2 多用户快照时，原子替换为空 v3，会话统一重新登录，不保留 legacy 条目；个人版仍沿用原格式与兼容路径。

### 5.2 容量与读取

多用户初始限制建议为每用户 4 个会话、全部署 256 个会话，快照上限 16 MiB，token 单项限制沿用有界输入要求。计数和总字节均是独立上限，任何一项达到上限都应在写入前明确处理。

- 登录先清理过期／失效记录。同浏览器重新登录时，仅在旧 Cookie 确实属于本次同一用户时原子替换旧会话。
- 每用户超过 4 个时，按创建时间原子移除该用户最旧的会话并加入新会话；不能踢出其他用户。
- 全部署或字节容量不足时返回明确的 `session_capacity_reached`，保留其他会话，不写出超限文件。
- 个人版继续使用原 16 条／原文件上限与策略。

必须同时修改恢复时的文件读取上限：当前 `readSettingsFile` 自身只接受 1 MiB，不能只放宽 `maxAccessSessionBytes`。提取可传上限的安全读取 helper，原配置／身份读取仍保持各自原上限。

### 5.3 串行提交与锁边界

继续使用现有 `snapshotWriter`／`persistence.Writer` 原子替换、目录同步和 `ErrCommitUnknown` 语义；不新建通用事务框架。当前 `replaceSessions` 在持有 `a.mu` 时写整个快照，不能随快照容量扩大而直接照搬。

所有会话集合修改统一进入一个串行提交入口，包括登录、刷新保存、退出、淘汰及过期裁剪。提交过程为：基于最新集合构造不可变候选 → 释放鉴权读取锁执行快照保存 → 短暂加锁发布。可用专用提交锁或等价的单写入 owner 串行化，`a.mu` 只保护内存读取、发布及撤销标记，不跨 IAM 外呼、磁盘写入或等待任务。

提交必须合并最新的其他会话记录，不能把 IAM 请求前复制的整个 map 覆盖回去。退出／停用可先在内存隔离相关会话及在途结果，持久成功后才返回完成；隔离标记不因稍后到达的提交结果消失。现有中间件直接删除过期条目的路径也需归入该 owner，避免绕过提交顺序。提交结果发布前再次检查撤销标记、主体版本及宿主关闭状态。

### 5.4 轮换与失败恢复

轮换按“验证新凭据 → 保存最新 Grant → 复核远端身份 → 发布新检查期限”组织：

1. 验证 token 响应格式及新 ID token（如有）的签名、身份绑定与声明，原始 ID token 不落盘。
2. 原子保存最新 access／refresh token，保留原 `verified_until`，并确保其不晚于新 access token 的有效期限。
3. 通过最新 access token 执行 userinfo；成功后持久更新 `verified_at`／`verified_until`。暂时失败时保留凭据、沿用原期限及退避规则，后续用最新 Grant 重试复核。

此过程只维护 Grant 和认证期限，不增加持久的待复核状态、恢复阶段或任务队列。对接点须能够在 token exchange 成功后交回最新 Grant，不能让后续 userinfo 错误把新 refresh token 隐藏在一个总错误中。复核发现明确失效或身份不一致时撤销会话。

远端轮换与本地快照无法构成原子事务。收到新凭据后本地保存失败，或进程在远端轮换后、保存前崩溃时，允许该会话需要重新登录。保存失败须隔离该会话、返回存储错误，不循环使用旧 refresh token，不绕过验证延长权限。

`ErrCommitUnknown` 时暂停本认证存储的授权发布并返回存储暂不可用。当前 writer 没有读盘后清除不确定状态的接口；本轮通过受控服务重启、关闭旧 writer 并重新读取快照恢复，不实现在线修复或直接替换 writer 的旁路。单份快照可能使本进程所有浏览器认证暂不可用，其影响不扩大为自动重置用户、停止全部 Agent 或丢弃业务数据。退出保存失败不返回“已持久退出”。

启动恢复只做本地读取、校验和裁剪，不因 IAM 暂时不可用阻止后台业务启动；需要远端复核的浏览器在首次访问时完成检查。用户／Space／Gateway Key 不迁移、不重新分配。

## 6. 续期调度、并发与失败处理

### 6.1 一个宿主 owner

统一由共享 `serverAccess.authority()` 的会话管理逻辑维护刷新，不能让每个 Space、SSE 或前端组件分别刷新。同一会话至多一个在途刷新／复核，多标签页请求等待同一结果；不同会话独立执行。

正常业务鉴权顺序：本地用户及版本 → 会话期限 → 远端检查期限 → Space 归属与业务权限。刷新和复核发生在业务 handler 之前，不读取／重放业务请求体。

外层 `spaceServer.ServeHTTP` 的 `browserSubject` 查询只做本地检查并提供路由候选，不在持有 `s.mu` 时访问 IAM；最终授权由统一会话检查完成。远端检查期限已过但本地会话尚有效时，仍能路由到鉴权入口。缺失／过期 Cookie 应进入 401 处理，不先变成 `identity_mismatch`；已认证用户访问他人 Space 则继续拒绝。GitLab OAuth 回调等直接使用 `browserSubject` 的入口也需审计，不能绕过最终认证，或在等待续期前提前消费一次性业务授权结果。

- `verified_until` 前约 60 秒触发提前复核；令牌足够有效时可直接复核，接近到期才刷新，避免每次检查都轮换 token。短 TTL 使用按剩余期限缩短的提前量，避免立即循环刷新。
- 可见页面由会话控制器调度；页面睡眠或关闭后无须持续刷新。下次访问按需恢复，前提是本地与 IAM grant 仍有效。
- 在原 `verified_until` 以内，提前刷新遇到暂时错误仍可使用既有授权；越过该期限后请求等待复核，失败则暂时拒绝业务。
- 全局并发外呼设有界 semaphore（初始 8），失败使用带抖动的 1s、5s、15s、30s 退避，并尊重有界的 `Retry-After`。重启不重建更长的授权窗口。

刷新执行使用宿主生命周期 context 和独立有界超时，单个 HTTP 等待者取消只结束自己的等待，不让共享刷新被随意取消。IAM 外呼与持久化均遵循第 5.3 节的锁边界。续期等待预算与前端请求超时需配套；请求 context 已取消时不再进入业务 handler，共享刷新仍可为其他等待者完成。

### 6.2 发布规则

取出本会话 revision／主体后释放锁，执行外呼；回来提交时重新核对：会话仍存在、revision 未被替换、UserID／AuthVersion 和外部身份一致、账号 active、绝对期限未过、宿主未关闭。

每个会话只维护一份 revision。退出／停用／替换会话必须使旧刷新结果失去发布资格。禁止在刷新回调里重新创建已经删除的会话。

关闭宿主时先禁止新刷新、取消在途外呼并等待任务结清，再关闭 session writer、释放现有部署锁。不要在关闭 writer 后允许回调继续写快照。

### 6.3 对外结果

| 条件 | 处理与前端行为 |
| --- | --- |
| Cookie 缺失、绝对期限已过 | 401 `login_required`，进入重新登录 |
| 确认 refresh 失效／撤销，或身份绑定不一致 | 撤销对应浏览器会话与流；401 `login_required`；不能自动停用用户或其他设备 |
| 网络超时、429、IAM 5xx | 原检查期限内保持访问并重试；期限外 503 `authentication_temporarily_unavailable`，保留本地会话及草稿 |
| 客户端配置错误，如 `invalid_client` | 503 并产生脱敏运维诊断；避免误判成所有用户失效并触发登录循环 |
| session 保存失败／提交不确定 | 不发布新授权；503 `session_storage_unavailable`，按存储恢复规则处理 |
| 本地用户被停用／认证版本失配 | 沿用现有拒绝、流撤销和运行时结清；刷新不能越过此边界 |

结构化诊断仅记录操作类型、用户标识、错误类别及耗时，不包含 token、授权码、客户端 secret、原始响应体、完整回调 URL 或 Cookie。

## 7. HTTP、前端与 SSE

### 7.1 会话接口

保留 `GET /v1/access/session`，新增无凭据元数据：`absolute_expires_at`、`verified_until`、`renew_after`、`renewal_supported`。返回时间由服务端计算；GET 可复用统一鉴权执行到期复核，不延长绝对期限。

拟新增同源 `POST /v1/access/session/renew`，请求体为 `{}`，只执行必要的认证复核／续期，不接受指定用户、namespace、token 或任意期限。成功返回相同 session 元数据，所有 POST 沿用现有 Origin 防护。

该入口必须基于共享 Cookie 校验，位于 Space 业务准入之前；默认 Space 被归档／挂起时仍能续期或退出。不能为此放开任何 Space 业务权限。`POST /v1/access/logout` 也不要求先访问 IAM：本地会话尚可识别时，即使远端检查过期或临时失败，仍允许持久撤销并清 Cookie。

Cookie Max-Age 使用剩余绝对期限，不因刷新无限滚动；保持 HttpOnly、Secure、SameSite 和现有名称、Path 合同。常规刷新不旋转浏览器 Cookie，IAM token 轮换由服务端处理。

### 7.2 前端单一控制器

新增小型会话控制模块，由应用启动／登录设置页按需挂载，复用现有 request 层；避免控制器调用 request 又递归触发自身续期。页面内合并 renew 请求，多标签页并发由服务端保证正确，不以跨标签页选主作为前提。

- 按服务端 `renew_after` 在可见页面检查，无需任何用户交互。隐藏页面暂停主动定时调度，恢复显示后立即检查；不增加用户活动事件监听或上报。
- 503 显示“暂时无法验证登录，正在重试”，保留页面与草稿。鉴权尚未恢复时暂停提交新业务操作；不把 503 导向登录页。
- 401 才进入重新登录；避免并发请求重复导航。跨 Space 的请求保持现有 `spaceURL` 与 reset 校验。
- 通用请求层不自动重试业务 POST。恢复后读取状态／回执，沿用已有 request ID、client message ID 和 uncertain 处理，不重放消息、审批或平台写操作。

真实重新登录前保存必要草稿，沿用已有按安装、operator、Room instance 的草稿隔离。仅将页面路由／会话选择等非敏感恢复信息写入浏览器 sessionStorage；callback 继续固定跳回本地入口，由启动逻辑在确认安装及用户一致后恢复位置。恢复路径限定同源允许的应用路径，不接受任意 `return_to` 外链，不把 token 放进 URL 或存储。

### 7.3 SSE 与撤销

保留现有固定 stream lease、到期定时器、`done` 撤销通道、写超时和心跳。建连时将 lease 截止时间设为 `min(absolute_expires_at, verified_until)`；到期后关闭，即使本地会话已完成续期，也允许此连接按原 lease 结束。

前端复用 EventSource／session 检查和现有 reconcile：登录仍有效时重新连接，读取断开期间的最新状态；需要复核时先经统一认证入口完成。保留原发送取消与心跳检查，不为续期新增动态延期定时器或逐帧远端认证；SSE 发送及 lease 路径不访问 IAM。

退出／本地停用仍立即关闭对应 lease，不等待下一次心跳。验收目标为受控重连不跳登录、不遗漏业务状态、不重复执行命令；允许短暂连接恢复提示，不要求同一条 SSE 连接跨所有续期持续存活。暂时认证错误退避，401 才进入登录。

## 8. 实施检查点与代码组织

三个检查点属于同一次实施任务，全部完成后统一交付。

### 检查点 1：确认真实 IAM 合同

通过 TeamX 独立应用和受控测试账号确认以下内容，记录脱敏结果：

1. 允许的 grant、scope、consent 及客户端认证方式；正常 code exchange 能取得 refresh token。
2. access／refresh 有效期、轮换规则，以及 refresh 响应 ID token／nonce 的实际形式。
3. 原 token 是否在轮换后失效；refresh 失败的错误格式；userinfo 正常绑定主体及明确拒绝时的响应。
4. 对 IAM grant 撤销、账号冻结和全局登出做能力核对，在受控条件下记录可验证的行为与限制；不将全部远端撤权能力补齐设为本轮前置。

第 1–3 项的可用登录／刷新合同是必要前置；第 4 项用于明确产品与运维承诺。没有实际能力的远端联动标记为不支持或未验证，不标记为已通过，也不扩大本次实现范围。真实登录需用户正常完成授权；冻结／撤销等破坏性检查须使用明确授权的测试账号或 grant。文档编写阶段只读取了 discovery，没有执行上述动作。

### 检查点 2：完成实现与本地闭环

建议按依赖顺序连续开发：

1. authn 可续期合同、OpenCSG exchange／refresh／verify 与错误分类。
2. 多用户 v3 快照、策略配置、容量限制、旧登录失效、串行提交与持久化故障处理。
3. 宿主统一会话 owner、并发合并、权限门禁、退出／停用／关闭竞争。
4. 同一次登录的 Gateway Key 装饰器接入，确保首次模型设置及用户隔离不回归。
5. session／renew 接口、SSE 固定 lease 的重连接入、前端续期调度及恢复。

新增长生命周期代码与测试按行为命名，例如 `access_session_renewal.go`／`access_session_renewal_test.go`、`session.mjs`；不以检查点编号命名。修改认证接点时补充对应合同注释，移除“宿主不持久化任何 IAM token”等已不成立的表述。

### 检查点 3：组合验收与交付

| 验证层 | 必须覆盖的行为 |
| --- | --- |
| Provider | 正常初始／刷新；ID token 可缺失；nonce 可省略；错误签名、issuer、sub、aud／azp 拒绝；空 refresh token 保留旧值；新值轮换；端点约束及脱敏 |
| 持久会话 | 绝对期限与远端检查期限；任何刷新／请求均不延长绝对期限；重启不延长；合法旧 v2 原子失效并重新登录；策略缩短；配置／安装绑定；权限、损坏、超限；最新 Grant 保存后复核重试 |
| 并发与故障 | 多请求只消费一次 refresh；一名等待者取消不影响其他人且不进入其业务 handler；退出／停用／替换与刷新竞争；不同会话并发提交不丢更新；远端轮换后落盘失败；rename／sync 提交不确定及受控重启恢复；关闭等待在途任务 |
| 业务隔离 | 两用户、多 Space、多设备互不串用 token；每用户限额按创建时间淘汰本人旧会话，不驱逐他人；默认 Space 归档后仍能退出／续期；外层路由／平台 OAuth 回调不误拒绝或绕过认证；个人版不受影响 |
| Gateway 模型 | 同一授权码只交换一次；刷新不取 Key、不覆盖模型；本人 Key／额度隔离；hub_origin 未配置时仍可正常续期 |
| 前端与 SSE | 可见／隐藏／恢复；无交互时正常提前续期；页面关闭后再打开按需恢复；固定 lease 到期重连不跳登录且 reconcile 补齐状态；期限外 IAM 故障暂时拒绝、恢复后重连；退出及时断流；草稿／位置恢复；业务写不自动重放 |
| 容量 | 256 会话上限下的读取、快照写入及并发等待；慢磁盘提交不持有鉴权读取锁；字节上限拒绝明确，不产生登录风暴或无界任务 |

本地测试使用可控时钟、替代 IAM、现有持久化 fault seam，避免靠真实等待 7 天验证。可在现有测试基础上执行：

```sh
go test ./internal/authn/... -count=1
go test -race ./internal/room -run 'Test(AccessSession|Hosted|OpenCSG|ExternalLogin|UIUpdates|UIUpdate)' -count=1 -timeout=8m
node --test internal/web/tests/web*_test.mjs
go vet ./internal/authn/... ./internal/room
go test ./... -run '^$'
```

新增测试命名需进入上述选择或补充选择命令；现有 e2e／integration 标签测试按仓库约定单独运行，不能把默认测试通过当作这些场景已覆盖。

真实安装验收至少完成一次自然 access token 到期前后的自动续期、一次续期后服务重启、本地退出和本地停用，并确认 SSE 受控重连、默认 Space、模型及后台业务行为符合边界。还应验证 IAM 暂时不可用后恢复。正常浏览器工作流不应每 15 分钟出现登录页。

对实际可用且已授权的 IAM grant 撤销另做验证并记录结果；冻结／全局登出等无可验证合同的能力如实列为不支持或未验证。替代服务必须覆盖明确 `invalid_grant`、userinfo 拒绝及身份不一致的本地撤销响应，不能用远端能力限制省略这些处理。

## 9. 升级、回退与完成标准

升级保留部署 Home、用户及 Space 身份、Gateway Key 和平台连接。首次升级将合法 v2 多用户登录快照原子替换为空 v3，已有 Cookie 统一失效，用户重新登录一次完成 refresh grant 授权；后续重启正常恢复完整 v3 会话。个人版登录不受影响，无需批量重建业务数据。

v3 快照含 IAM 凭据，现有私有部署备份会将其包含在内。恢复旧备份可能带回已轮换失效的 refresh token，允许要求用户重新登录，不能因此回退到更宽松授权。旧二进制不能假设能读取 v3；回退时应停服，按运维步骤使浏览器登录快照失效后重新登录，保留其他认证配置、用户和业务数据；不要直接恢复旧 token 文件来“恢复登录”。

实施完成时同步更新 [OpenCSG 登录说明](opencsg-login.zh.md)、[认证实现说明](implementation/opencsg-auth-preflight.zh.md)及必要测试文档，明确 IAM token 新的私有保存位置、会话期限、退出含义、升级重登录和实际远端撤权能力。脱敏验收记录保存在本地私有证据目录，不提交 token／日志原文。

交付须同时满足：长期会话及自动续期完整接通；串行提交、并发、故障、重启、退出和本地停用边界通过；真实 IAM 登录／刷新与安装验收通过；SSE 重连恢复、默认模型和个人版回归通过；实际远端撤权能力与限制明确记录。仅修改常量、替代服务测试通过或 discovery 宣告支持，均不作为最终完成依据。
