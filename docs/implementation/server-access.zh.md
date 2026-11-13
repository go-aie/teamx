# Server token 访问、模型设置与 HTTPS 入口

日期：2026-11-12。对应[常驻 Server 方案 W2](../DESKTOP-APPLICATION-ANALYSIS-AND-PLAN.zh.md#w2token-访问与远程-web)。W2 代码已实施；本机 HTTPS／真实 Chromium／真实模型对话已通过。公网证书、异机访问、真实手机与最终 standard 归档仍须分别验收。

## 本机免登录与访问设置补充

全新 home 默认仅监听回环地址，无需 token 即可进入首次模型设置。初始化仍持久保存固定 `operator_id`，不生成未使用的管理 token。已有安装省略 `access.login_enabled` 时继续启用认证，不因升级降低保护。

「设置 → 安全」按访问方式、登录验证和条件连接设置分层。访问方式为「仅本机」「局域网」「HTTPS 代理」；后两种自动开启登录验证。首次勾选登录验证生成 32 随机字节的 token；也可输入 12–256 个可打印 ASCII 字符的自定义 token，不允许首尾空白；新设置会拦截内置常见密码和明显连续、重复组合。token 是可直接使用的凭据，仍保存在部署账号拥有的 0600 私有文件，不写入网页 localStorage／sessionStorage、URL、日志或业务材料。

已启用认证时，主页面显示「令牌已设置」与「更换令牌」，不展示当前令牌输入框。更换操作仅生成候选，使用现有登录会话即可；最后保存时才在确认弹窗输入当前 token。取消保留设置草稿并清除验证输入；关闭验证、更换令牌和其他访问设置保存都沿用原 token 校验。当前 token 的查看入口与 reveal API 已移除。新 token 先写入新的不可变私有文件，再通过共享配置文档的原子替换激活 `access.admin_token_file`；写入失败不会覆盖旧凭据。旧文件不会自动删除，曾自行配置 token-file 的人工 CLI 需改用当前活跃文件。网页保存访问设置撤销当前浏览器 Cookie；登录模式或 token 变化还撤销全部浏览器会话。Agent 凭据不受影响。

`server.access_mode` 保存 `local`、`lan` 或 `proxy`。仅本机使用 loopback，可选登录；局域网使用 `0.0.0.0:<端口>` 和强制登录，显示本机私有 IPv4 的访问地址与 HTTP 未加密提示；HTTPS 代理要求一个 HTTPS 根 origin，切入时恢复 loopback，默认信任本机代理 IP，代理与证书须另外部署。已有 `remote_enabled`／HTTPS 配置在缺少 access_mode 时继续识别为代理模式，不自动降级。

访问方式、监听端口与 origin 在正常重启后生效，保存后显示待重启。局域网地址区分“待生效”和当前已启用状态。关闭仍在运行的非本机入口时必须先保持认证，保存并重启回到仅本机，再关闭登录。后台系统服务同样支持局域网模式，但始终要求登录；管理器与内部 Agent 继续连接 loopback endpoint。

局域网直连要求实际 socket peer 是私有地址或 loopback，Host 必须为本机启动时枚举的私有 IP 与实际端口；拒绝任意域名、其他 IP、公网来源、转发协议／Host 头和跨源写入。HTTP 浏览器 Cookie 为 HttpOnly／SameSite=Lax；页面的操作 ID 在缺少 randomUUID 时使用 getRandomValues 生成，复制不可用时选中地址／候选令牌供手动复制。HTTP 无法防止同网段窃听，只用于可信局域网。人工 CLI 仍不向非 loopback HTTP 发送凭据；GitLab OAuth 保持原规范入口约束，需在本机或 HTTPS 代理入口完成连接，已有平台连接的使用不因此改变。

免登录入口只接受实际 loopback peer、精确内部 Host 或同端口 localhost，拒绝转发头、外部 peer 与伪造 Host。浏览器写请求仍要求同源 Origin；人工 CLI 自动携带 endpoint Origin，并从 session 入口取得固定操作者。无凭据的 agent 身份、无效 Bearer 和伪造 human 均不能降级成匿名调用。以下 W2 原始记录中的“默认认证”及固定随机格式，已由本节替代。

## 使用方式

全新安装直接启动，本机免登录进入设置：

```sh
teamx serve --home '/absolute/home'
```

版本目录部署继续使用 `serve --server-root DIR --home DIR`；初始化命令不负责安装版本、下载依赖或登记系统服务。可移动归档先执行 `./start init --non-interactive` 准备运行时和模板，无需额外初始化 token。若部署前就要启用认证，可先运行 `bin/teamx access init --config <home>/config.toml`。未配置模型时，在网页填写协议、Base URL、模型 ID 和 API Key；直接源码部署须先提供可用的 DSH 路径或将 DSH 放入 PATH。已有非空密钥过期、模型服务拒绝请求或需要更换默认模型时，部署者先正常停止，再以 `teamx serve --home DIR --setup-model` 进入同一受保护表单；保存后自动进入正常服务。此入口不修改已有 Room 的冻结模型，也不是浏览器中的服务重启接口。

管理 token 保存在配置旁的 `auth/admin-token`，也可通过 `access.admin_token_file` 指定相对于配置的路径。初始化只在缺少时生成 32 随机字节的无填充 base64url 值，不输出 token 本身、不覆盖已有文件。文件必须由部署账号拥有、权限为 0600 的普通文件，拒绝符号链接。`access.operator_id` 同时初始化并保持固定，不再依赖宿主 USER。

浏览器输入该 token 后取得随机 Cookie 会话；页面不把 token 写入 URL、localStorage 或 sessionStorage。会话默认 24 小时、最多 16 个，仅存摘要于 Server 内存；退出只撤销当前会话，重启会使全部浏览器会话失效。轮换时在部署机替换私有 token 文件并正常重启，保留 `operator_id` 和业务目录。

人工 CLI 从文件读取 Bearer：

```sh
export TEAMX_ADMIN_TOKEN_FILE='/absolute/home/auth/admin-token'
teamx read --endpoint http://127.0.0.1:8088 --room ROOM_ID --latest 20
# 也可在命令上使用 --token-file FILE；不支持 argv 中的 token 值。
```

人工请求的正文 Caller 和查询 caller_id 必须与认证所得的固定 human 一致；Agent 不能使用管理文件。CLI 拒绝向非 loopback 的 HTTP 地址发送凭据，并禁止携带凭据跟随重定向。

## 实现边界

- 全新 `--home` 和 bundle 启动默认仅本机免登录；已有安装保持认证。启用认证后缺少／无效管理文件或固定操作者时启动失败，不创建业务服务。源码的已配置开发入口 `--config` 保留原模型检查；产品首次设置／修复使用 `--home`。
- 统一 HTTP 入口保护静态业务界面、全部私有读写 API、附件和诊断。公开范围只含登录所需的三项资源、最少健康信息及两个精确 GitLab OAuth 回调／完成路径；OAuth 原有 Cookie、state、PKCE、成员与操作检查继续执行。
- `POST /v1/access/login`、`POST /v1/access/logout`、`GET /v1/access/session` 提供轻量会话。登录失败限流，管理 token 摘要采用标准库常数时间比较；登录与 Cookie 写请求要求规范 Origin。Bearer 的空 Origin 不替代认证。
- HTTPS Cookie 为 `__Host-teamx-session`，Secure、HttpOnly、SameSite=Lax、Path=/、无 Domain。本机 HTTP 使用独立的 `teamx-local-session`。
- Agent 凭据在 DSH 会话打开时随机签发，绑定 RoomID、RoomInstance、AgentID、MembershipID、ServiceEpoch 和明确的工具路由集合。原业务处理器继续检查实际成员和权限。会话关闭／打开失败后撤销；服务换代重新签发，无固定小时 TTL。
- 管理凭据、管理文件路径和浏览器会话变量从服务与 DSH 的继承环境过滤，禁止配置为 ToolEnv。执行凭据只放在独立的临时 Options 字段，不进入 Runtime、ToolEnv、prompt、恢复摘要或材料文件；不改写请求 JSON、Room 调度或 Inbox 消费语义。
- `GET /v1/setup` 和 `POST /v1/setup/model` 与正常业务使用同一认证。设置状态不创建 Room manager／平台 worker／DSH；保存时复用模型结构校验，以 0600 临时文件、同步和原子替换保留其他配置，拒绝覆盖外部变更。模型的兼容性与可选参数保留。保存后先结清设置 HTTP／锁，再在相同端口按正常入口启动；没有热重配 Room manager。原内存登录会话失效，浏览器重新登录。
- 设置成功仅表示结构校验与持久保存，不伪称模型已连通；正常对话另验模型，不在每次启动时自动消耗模型调用来验证凭据。无效表单可直接修正，文件写入失败不进入业务状态。

### DSH 临时传递契约补充

真实验收发现，DSH 的 shell 子进程会按 `KEY|PASSWORD|SECRET|TOKEN` 过滤继承变量。仅向 DSH 传 `TEAMX_EXECUTION_TOKEN`，并不足以让 Agent CLI 收到凭据；首次探针因此返回 `invalid_origin`，Agent 反复诊断后触及 `max_tokens`。

适配器现在显式传递临时的 `TEAMX_EXECUTION_AUTH`，值为同一个执行凭据；CLI 优先读取 `TEAMX_EXECUTION_TOKEN`，被 DSH 过滤时读取该传递变量。两者都从宿主继承环境中过滤、禁止进入持久 ToolEnv，只有会话打开时注入。此补充不传递管理凭据，不改变原六项身份、授权范围和撤销时机；真实 Room 工具调用已验证通过。固定版 0.2.2 的子进程过滤实现也具有相同规则；本次模型运行使用本机 DSH 源码构建，未替代最终归档验收。

### 规范地址与代理

```toml
[server]
listen = '127.0.0.1:8088'
public_base_url = 'https://assistant.example.com'
trusted_proxies = ['127.0.0.1']
```

只支持一个 HTTPS 根 origin，不接受 userinfo、子路径、query 或 fragment。未设置外部地址时使用实际 loopback origin。远程代理必须保留规范 Host，且仅由明确配置的 socket peer 提供单值 `X-Forwarded-Proto: https`；代理须删除 `X-Forwarded-Host`／`Forwarded`，服务遇到这些头会拒绝；也不使用转发 IP 确定身份。错误 Host、Origin、协议或代理来源均拒绝；不开放 CORS。

内部 Agent／人工 CLI 可继续以 Bearer 使用实际 loopback endpoint；本机最少健康查询也可使用该 endpoint。GitLab 回调地址改为从规范外部 origin 生成，并沿用同浏览器的临时授权绑定。规范地址变化不自动改写远端 Application；完整平台登录在 W4 验收。

## 验证证据

本机 macOS arm64：

- `go test ./... -timeout=8m` 通过；认证及相关 Server／CLI／配置／设置进程测试带 race 通过。
- 427 项 Web 单元测试、11 项相关打包脚本测试通过。
- 新测试覆盖全部私有入口统一保护、Caller／查询身份、会话退出／到期／数量上限／重启／轮换、执行凭据六项绑定与撤销、无固定 TTL、代理伪造、HTTPS Cookie、流式工具输入、设置修正与正常启动、执行环境及持久材料秘密隔离。
- 使用真实 Go Server、本机 TLS 反向代理、自签名测试证书和 headless Chromium，完成登录 → 无效模型修正 → 保存并启动 → 重新登录 → 默认助理真实模型对话 → 正常回合结清 → 退出登录。模型为 `deepseek-v4.1-flash-1017`，DSH 为本机 `0.2.1-alpha.1` 源码构建。
- Agent 成功调用真实 `read --unread`，回复 `W2_HTTPS_OK`。未登录和退出后私有 API 均为 401；伪造 human 为 403；Cookie 为 Secure／HttpOnly／Lax；浏览器无脚本错误。浏览器存储、服务日志和业务数据扫描未发现管理 token 或模型密钥。

隔离验收报告、截图位于 `.local/server-access-check/report.json`、`conversation.png`。测试不复用现有业务目录或平台连接，不执行真实平台写入。合成业务后端的历史进程 fixture 仍通过测试依赖注入使用可信 HTTP；生产可执行程序没有关闭认证的 CLI／环境开关，新增 W2 与真实 DSH 验证使用默认认证入口。

本次 HTTPS 在同一台机器上使用测试证书，浏览器允许该测试证书；它证明 TLS 代理和应用链路，不证明公网可信证书、异机网络或真实手机验收。W3 的完整移动体验、W4 平台 OAuth、W5 系统服务／部署／升级、W6 最终归档与设备矩阵均未提前声明通过。

## 本次访问模式变更验证

- `go test ./... -timeout=8m` 全量通过。

- 新增访问控制、旧配置保留认证、固定操作者、Agent 边界、token 轮换／会话撤销、外部配置冲突、持久版本、远程关闭顺序及 wildcard 监听测试；相关测试带 race 通过。IPv4／IPv6 按配置分别使用 tcp4／tcp6，内部 endpoint 始终使用回环地址。
- Web 单元测试 468 项通过。新增首次开启生成候选、远程强制登录、当前 token 核对、失败草稿保留及迟到候选清除检查。
- 隔离临时 home、真实 Go Server 与 Chromium 完成 10 项检查：免登录进入、通用设置入口、随机候选、自定义 token 登录、旧会话失效、当前 token 关闭验证、免登录刷新、远程要求登录、390px 视口无横向溢出、关闭设置清除秘密。报告和截图位于 `.local/access-settings-check/`，浏览器脚本错误为零。

本次浏览器使用隔离的空团队目录，不调用模型或真实平台；远程入口的策略和 wildcard 回环链路已通过 Go 测试，不据此声明公网证书、异机网络或真实手机已验收。

## 访问设置界面整理

访问与安全移至独立的「设置 → 安全」，通用页保留名称与时区。常驻提示缩短，代理细节按需展开；显示／复制位于令牌输入框内，保存操作固定在面板底部。四个分类支持键盘切换，单 Room 模式也提供安全入口；离开分类继续清除秘密输入。

本次仅整理界面与入口文案，认证规则不变。468 项 Web 单元测试、Web 嵌入资源检查通过；隔离 Go Server 配合当前源码页面的 Chromium 检查通过，包含桌面和 390px 视口、保存按钮可见、无横向溢出及登录流程。截图与报告位于 `.local/security-tab-check/`。

## 保存时确认交互

取消勾选只标记「有未保存的更改」，点击保存后才按操作显示「关闭登录验证」「更换访问令牌」或「保存访问设置」确认窗口。首次开启时没有旧令牌，不要求此验证；已有认证的保存仍在服务端核对当前 token。新令牌候选不改变当前凭据，生成失败不修改设置。隐藏的新令牌与远程字段不参与表单校验。

取消、Escape 和浏览器 Back 只关闭确认窗口并清除验证输入，草稿保留；验证失败在弹窗内修正，不返回主表单追加字段。提交中不重复发送，也不允许关闭确认窗口；页面离开清除候选和验证输入，不自动重放写入。

验证：472 项 Web 单元测试、Web 嵌入资源检查通过；访问控制相关 Go 测试带 race 通过。隔离 Go Server 与当前源码 UI 的 Chromium 检查覆盖首次开启、已设置摘要、未保存关闭草稿、取消、浏览器 Back、更换候选／最终验证、错误修正、关闭后免登录刷新、远程约束和页面关闭清理，桌面／390px 窗口均已检查。无脚本错误，不调用真实模型或平台；截图和报告位于 `.local/access-confirm-check/`。

令牌操作补充：显示／隐藏使用眼睛图标，复制使用双纸张图标；均带 title 与无障碍名称。复制成功显示 1.5 秒勾号，编辑、重新生成或清空令牌会重置反馈；「取消更换」「重新生成」保留文字。473 项 Web 单元测试与嵌入资源检查通过，Chromium 桌面／390px 视口检查通过，图标触控区域至少 44px。截图和报告位于 `.local/token-icons-check/`；浏览器剪贴板使用测试替身，不代表系统剪贴板权限验收。


## 三种访问方式与连接分层

面板顶部为纵向三选一，随后是登录验证，底部按所选模式显示局域网端口／可复制地址或 HTTPS 地址／折叠代理说明。保存按钮固定在底部；切换草稿保留暂时隐藏的字段，但保存只提交所选模式的连接参数。未重启的入口持续保留认证，局域网地址标记待生效。

验证：`go test ./... -timeout=8m` 全量通过，访问／后台服务相关 race 测试和最终资源检查通过；486 项 Web 单元测试通过。隔离 Go Server 与 Chromium 在真实本机局域网 IPv4 上完成 25 项检查，覆盖 HTTP 登录、私有 API 拒绝未认证访问、模式与端口草稿、重启前后地址状态、HTTP 下模型编辑器操作 ID、复制降级，以及切回仅本机后才允许关闭认证。桌面和 390px 视口无横向溢出，连接字段可滚动至固定保存栏上方，无浏览器脚本错误。报告与截图位于 `.local/access-modes-check/`。

本次为同机浏览器访问真实局域网接口，未验证另一台设备、真实手机、模型调用或新的平台授权；不改动现有业务实例的访问设置。


## 手机输入与新密码检查

自定义访问密码下限由 16 降为 12，仍支持最多 256 个可打印 ASCII 字符及中间空格，不强制混用大小写、数字或符号。随机生成继续使用 32 随机字节。输入框下仅保留一行动态提示，显示具体拒绝原因或“较弱／一般／较强”的粗略建议，不显示安全百分比。常见密码及简单数字后缀／字符替换、完整重复组合、连续数字／字母／键盘顺序由前后端同时检查。规则不是完整泄露密码数据库，强度提示也不保证安全程度。

服务端嵌入一份规则数据，通过访问设置响应交给浏览器，保存时仍独立校验。拒绝发生在创建新凭据文件或写入配置之前，不返回候选内容。现有密码的读取、登录和无密码变更的设置保存只校验凭据格式，不追溯应用新建规则；已有安装不会因新规则被锁定。连续登录失败 5 次暂停 1 分钟的限制保持原样。

登录与保存确认使用 current-password，新密码使用 new-password，并提供固定账号元数据供密码管理器识别，不增加账号输入步骤。网页仍不自行保存明文凭据；是否由设备密码管理器保存由用户及浏览器控制。

验证：Go 全量测试通过，访问控制相关 race 测试与 Web 资源检查通过；492 项 Web 单元测试通过。共用测试样例检查前后端规则一致性，API 测试覆盖绕过前端的弱密码拒绝、无配置／凭据文件副作用、12 字符 Bearer 与重启读取、已有弱密码登录和保留。隔离真实 Go Server 与 Chromium 完成 20 项检查，包括新密码提示、单次保存、重启后登录、390px 无横向溢出、自动填充标记和无新增可见账号字段。截图与报告在 `.local/password-policy-check/`；没有调用真实模型，也未实测操作系统／手机的密码管理器。
