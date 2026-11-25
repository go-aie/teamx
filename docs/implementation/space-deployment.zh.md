# 空间部署与恢复验收

日期：2026-11-25。对应空间方案 S5a。**本轮开发实施、Linux 实机预验收及人工验证环境交付已收尾，进入用户人工验证留存期；S5a 正式出口尚未完成。** 多用户生产门槛保持关闭，不将开发环境可用等同于最终生产候选或完整组合验收通过。后续操作见 [Linux 打包、部署与升级](../linux-deployment.zh.md)。

本文中的域名、账号及部署路径使用脱敏别名；原值与完整记录仅留在本地 `.local/linux-environment.md`、`.local/space-deployment-private.zh.md`，均被 Git 忽略。别名不能直接用于连接或运维，产物摘要与验收结论保持原记录。

## 阶段交接（2026-11-25）

用户要求当前阶段暂告一段落，保留 Linux 环境继续人工验证。本轮收尾只核对环境和整理文档，不重启／升级服务，不更改 DNS、Caddy、准入、账号或数据，不清理发布树、构建材料及备份。

| 已完成 | 已有证据／范围 |
| --- | --- |
| Linux 打包与依赖适配 | 原生 amd64 standard 开发归档及完整 smoke；Ubuntu 22.04 固定依赖组、普通账号真实 SRT；保留 24.04 合同 |
| 正式安装与服务入口 | 初始 HTTPS／模式／认证配置、元数据权限、独立本机认证控制面；systemd 普通账号、权限限制及启停／同模式升级 |
| 个人模式回归 | 真实模型验证、全新 Room 私有模型响应、同一私有 Session 重启恢复及 HTTPS Cookie 恢复；用户确认个人模式人工验收完成 |
| 多用户真实认证与模型首用 | 真实 OpenCSG 登录、持久 User／默认 Space、本人模型文本／工具／清理验证；用户确认首用成功 |
| 开放认证后准入 | 显式允许其他已认证账号自动注册；双账号隔离／停用等受控回归通过；网页登录链真实 Chromium 复验通过 |
| 已有空间升级保留 | 原安装／User／Space、模型收据与成员快照保持；原 SRT／DSH 运行指纹与旧 CLI 真实检查通过，首次超时及重试记录保留 |
| 一致备份与交付 | 个人及多用户的一致备份完成；当前 alpha 域名、服务、程序／数据及历史材料保留 |

**未完成项按以下边界交接：**

1. **下一候选源码同步。** `prepareFirstAccess` 的“仅开放开关、无名单”启动修正及从首次初始化贯通的回归已在工作区完成，尚未重建到当前包。当前 alpha 以开放开关＋原名单兼容启动，实际准入不受该名单限制；重建时须包含全部相关公开实现，不能只取主仓旧 HEAD。
2. **备份清单容量。** 本次三份发布目录的清单为 66,694,220 字节，接近当前安装包的 64 MiB 上限。后续部署脚本增量已将源码上限扩为有界 512 MiB，大于旧上限的有效清单和篡改／超界拒绝回归通过；尚未重建或部署到 alpha。下一次须将修复纳入 commit，使用新包安装器引导，并补实际多版本备份验证，不以删除旧运行描述／发布树绕过。
3. **完整 S5a 恢复证据。** 原绝对路径完整备份还原、带真实多用户业务 Session 的升级／恢复、故障／重启恢复仍须补齐。已有模型运行指纹相同、备份生成成功或个人重启成功，均不替代这些项；整机重启后的完整访问链也未验收。
4. **真实多账号人工验证。** 已开放其他账号，但未声称第二个真实账号已完成登录、各自业务及交叉拒绝验证。受控双用户测试不能替代此项；后续回执按实际账号验证结果补录，不在本轮自动创建身份或代用户操作。
5. **正式前置与冻结。** 所选 S3／S4 的真实验收门槛仍保留，特别是 S3b.2a 飞书 OAuth／刷新／DPoP 及其依赖出口；最终源码归档、首发候选冻结与正式版本多用户门槛尚未关闭。S5b 的容量／并发、长时观察、认证故障／撤销传播及所选真实平台验收独立保留；托管 Git／Computer 不因本轮获得支持声明。

**留存快照：** `operator@linux-host.example.com`，入口 `https://assistant.example.com/`，Ubuntu 22.04.5 amd64／glibc 2.35。当前 `dev-f7d7360b0d4b`（归档 SHA-256 `36d1f0ff9b6b4261761f478a8fbc948f4adaec0b9e63f921b345f56e864264c6`），`teamx.service` 与 `teamx-alpha-proxy.socket` 本次只读核对均为 active／enabled。程序、home、管理命令分别为 `/srv/teamx-example/hosted/server`、`/srv/teamx-example/hosted/home`、`/srv/teamx-example/hosted/bin/teamx`。原个人目录 `/srv/teamx-example/personal` 及 `/srv/teamx-example/backups` 的历史备份保留；运行入口和路径表见 [Linux 环境说明](../linux-deployment.zh.md#7-本轮-alpha-留存环境)。账号准入已开放，新账号各自配置模型；已过期的浏览器会话须正常重新登录。

以下各节保留实施过程及失败／修复证据；其中“当前”“随后”等措辞按对应批次理解，最终交接状态以上述快照为准。

## 目标机与依赖

用户指定 `operator@linux-host.example.com`，公开地址为 `https://assistant.example.com`。实际主机是原生 amd64 KVM、Ubuntu 22.04.5、glibc 2.35、Linux 5.15.0-101、systemd 249；使用普通服务账号 `teamx`（UID/GID 997）。验收数据放在 `/data` 独立目录，不升级宿主发行版或修改 namespace 的全局限制。

SRT 交付合同增加 Ubuntu 22.04 的独立固定工具组，保留原 Ubuntu 24.04 组：

| 系统 | bubblewrap | socat | ripgrep |
| --- | --- | --- | --- |
| Ubuntu 22.04／glibc 2.35 | 0.6.1-1ubuntu0.3 | 1.7.4.1-3ubuntu4 | 13.0.0-2ubuntu0.1 |
| Ubuntu 24.04／glibc 2.39 | 0.9.0-1build1 | 1.8.0.0-4build3 | 14.1.0-1 |

安装按实际 OS／glibc 选择对应的完整工具组，仍核对包与程序摘要、非 setuid 及动态库闭包；未知系统或混搭工具拒绝。Node、DSH 和 SRT 的版本／npm integrity 不变。服务账号清空 capabilities 并设置 no-new-privileges 后，已用原生构建归档实际通过 `srt.InstalledConfig` 和 `Factory.Check`，进入真实 namespace，登记 24 个只读发布／系统库项。此项证明 22.04 基础运行能力，不替代 DSH 业务及恢复验收。

## 安装入口与控制面修复

`install.sh`／`teamx service install` 增加 `--deployment-config FILE`，在首次身份初始化前选定模式、HTTPS 和认证。输入是 0600 TOML，仅接受部署配置及有限服务访问字段；受管 DSH、数据根、凭据和环境不从该文件导入。初始配置复用原默认配置生成器；已有设置只能匹配，不能通过重新安装转换模式或覆盖已有配置。已登记身份旁丢失配置时保留现场并拒绝。多用户仍受原生产验收门槛约束。

服务管理增加独立的本机 `GET /v1/deployment/status`：只接受 loopback、无 Origin／代理转发的私有管理员 token，返回安装身份及控制面就绪状态，不取得业务用户身份、不读取模型或 Room。用户目录损坏、服务关闭中返回不可用。多用户 `service` 的就绪及端口归属检查使用该入口；个人路径不变。安装提示也区分账号登录和管理员 token。

首次实机安装发现原 `service.json` 为 root 的 0600，普通核心在检查安装执行选择时无法读取，systemd 启动失败。改为 root 拥有的 0644 元数据，内容仅为安装路径、账号、执行选择和无凭据端点；配置、执行描述和凭据继续私有。失败包及日志保留，不能据此声明首次安装通过；修复须由重建归档复验。

修复后的 `dev-d4359ca36e3e` 已在新的 `/srv/teamx-example/personal` 经归档内安装器安装，使用初始部署配置固定 `127.0.0.1:18088`／alpha HTTPS origin。普通账号 systemd 运行，管理员认证检查通过，首次为 setup_required；随后经实际配置 API 完成一次真实模型验证，约 38 秒进入业务运行。来源模型 Key 只经私有临时文件提交到验收实例，提交后删除临时副本，未改来源安装。

真实 Room 调用另发现 DSH `InspectRuntime` 虽校验了物理 release 中的程序，却以原 `current/bin/dsh` 链接执行版本查询。SRT 不挂载该链接，所以查询失败，成员在创建私有 Session 前暂停。无凭据对照探针确认链接路径为 ENOENT、物理路径正常返回版本；修复让查询使用同一已验证的物理程序，不增加沙箱读取授权。模型验证自身通过不代表此 Room 路径已通过；新归档的实际调用及恢复仍据下文报告判定。

## HTTPS 与真实认证

现有 HTTPS 由 Docker Caddy 提供，443 经过原 nginx SNI 转发。为 alpha 增加精确域名规则，复用原证书；其余域名规则保留。Caddy 上游经专用 `teamx-alpha-proxy.socket`／`.service`，仅从 Docker bridge 转到宿主 loopback。此桥接不放开 TeamX 的公网监听。

Caddy 按其[代理头配置](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy#headers)保留 Host，移除 `X-Forwarded-Host`／`Forwarded`，TeamX 固定公开 origin 并只信任本机代理。随包 Caddy 示例同步修正。配置经实际 Caddy 校验及热加载，原配置备份为 `/srv/teamx-example/acceptance/evidence/Caddyfile.before`。

独立真实登录探针继续监听 loopback，可显式声明 HTTPS origin，并复用正式请求边界。实测发现浏览器原生表单在 `Referrer-Policy: no-referrer` 下发送 `Origin: null`；验证页改为 same-origin，敏感 SSO 登录／回调继续 no-referrer。Chromium 已复现原错误并确认修复后正确发送 alpha Origin，POST 返回 303，进入 OpenCSG／IAM。

首次上游登录返回 `untrusted signin callback target`，当时未收到成功回调。后续重新发起的真实 HTTPS 登录已通过：OpenCSG 返回 TeamX，固定 CSGHub 的受保护身份交换成功，取得稳定 subject，收据以 0600 保存在 `/srv/teamx-example/sso-probe/attempt-recheck/identity-receipt.json`。脱敏收据摘要为 `aa2d47fd32b73653681cba2e1958c6659557830ca3c35743269ff64a808c0db1`；身份探针没有登记业务 User 或发放业务会话。该结果关闭单账号真实身份交换缺口，不代替安装产物上的完整多用户组合。

## 产物、验证及停止点

构建与私有证据位于 `/srv/teamx-example/acceptance`，本地收据放在 `.packaging/deployment-work`。原生构建使用已核对摘要的 Go 1.27.1、Node 24.22.2，以及锁定 npm 缓存。GitHub 下载在目标机遇到 TLS 证书错误后，改用本地已有、再次核对固定摘要的归档缓存，没有跳过 TLS 校验。构建来源是本工作区公开文件快照；快照仓库的提交只标识验收材料，不是本项目提交或正式 tag。

以下开发归档完成完整 smoke：

| 归档版本 | SHA-256 | 范围 |
| --- | --- | --- |
| dev-0988be37f173 | faf6afe5e686cd0c3f381187efc9acfc96ccc293ba0b5d420db35fcd66ec53e6 | 22.04 适配；真实 SRT 基础检查；安装暴露元数据权限缺陷 |
| dev-4ab48d90fc0b | b326d06ea9d9479e7e73d3ed48eb9cc5d2d85ee96433bd8eefd48fb0f533cb2a | 关闭中控制面拒绝、HTTPS 示例与说明增量 |
| dev-d4359ca36e3e | 3393a4293d451548998926558700daf83643514b32d55bb77fd5be85018de952 | 元数据权限及首次配置修复；普通账号正式安装和真实模型验证通过；Room 版本查询暴露链接路径缺陷 |
| dev-e4f42fb4997b | 90e9aa51ea561a0c3c8aa7a785214ba578eb618df70ff792d0393d8e81ae8a30 | DSH 使用已验证物理路径；普通升级、全新 Room 真实模型私有响应、同一 Session 重启恢复及 HTTPS Cookie 恢复通过 |
| dev-f0b7f25a07f5 | 5046cbc67ca60a0052ec8115b6634badec42254b5ae4c248578215cf87418d59 | 正式登录页使用 same-origin，敏感回调保持 no-referrer；归档 smoke 通过 |

修复后的最终开发归档及安装／升级／恢复结果另据实际报告补录，不借上述归档覆盖后续代码。Go race 定向验证覆盖部署状态的权限／身份／关闭、初始配置不改绑、受管默认路径及原服务回归；打包测试覆盖两组依赖的完整 pin、资源损坏拒绝和参数透传。

正式出口仍须所选 S3／S4 真实验收关闭、最终候选冻结，以及同一候选上的真实认证／本地会话、普通账号 systemd／HTTPS、新装、同组合版本升级、故障／启停、原路径完整备份还原和身份／业务恢复。S5b 的真实平台、并发及容量独立保留。托管 Git 或 Computer 不在当前验收范围。

## 开发包多用户人工验收

2026-11-25 用户确认个人模式人工验收完成，要求切换 alpha 进行多用户人工验证。新增范围限定为 `dev`／`dev-*` 构建的开发验收：正式版本仍拒绝多用户，Linux／SRT、显式 provider 与稳定 subject 准入、完整执行依赖及端点检查继续执行。不会通过运行时配置解除正式版本门槛。源构建及 Linux 定向测试覆盖开发／正式版本边界。

个人 `/srv/teamx-example/personal` 保留；新多用户实例使用 `/srv/teamx-example/hosted/server` 与 `/srv/teamx-example/hosted/home`。初始准入从已验证的私有 OpenCSG 身份收据读取，仅开放该稳定 subject；用户确认沿用首个验收账号，其他账号需要另行明确准入。新安装不复制个人 User／Space、业务凭据或浏览器会话。

个人实例已完成停服一致备份 `/srv/teamx-example/backups/personal-before-hosted`，通过正式 `service uninstall` 仅注销旧服务登记。首个多用户开发包 `dev-1601e2bcb26a`（SHA-256 `7aaa4631b6db50f5345669fecd8c49512b18a8c6eccaa04f7fcc5aa9b1368fa1`）经归档安装器冷安装成功，普通账号 systemd 及本机认证控制面就绪；管理员 token 请求业务会话返回 401，个人身份文件与备份一致。

实际 Chromium 点击正式登录页发现另一个阻塞：网页 `form-action 'self'` 拒绝原生表单的外部重定向链；独立身份探针没有这份网页 CSP，不能替代产物页面检查。修复保持严格 CSP：浏览器通过本站 JSON POST 建立原来的 state／浏览器 Cookie，然后单独导航到 provider 的登录 URL。原生 POST 的 303 接口保留，JSON 路径共用同一 Origin、限流、Cookie、一次性回调与持久准入检查；回归覆盖 JSON 登录后一次有效回调和重放拒绝，前端覆盖提交及导航。该修复须重建产物并再测正式网页跳转。

修复包 `dev-8068c9601f20`（SHA-256 `431b1fa31adc80a7d8be7b6f296fb0f018ef731d758e90bd7753376c3d213b8c`）完整归档 smoke 通过后，经正式 `service upgrade` 升级同一多用户实例；一致备份为 `/srv/teamx-example/backups/hosted-before-login-fix`。安装身份保持不变，服务账号仍为 UID 997，systemd active／enabled，认证控制面返回 multi-user／ready。管理员 token 访问业务会话仍为 401。

实际 Chromium 正式网页复验通过：个人 token 表单隐藏、OpenCSG 登录按钮可用，严格 `form-action 'self'` 保持；本站 POST 返回 200 JSON，Origin 为 alpha HTTPS，浏览器持有 Secure／HttpOnly／SameSite=Lax 的登录尝试 Cookie，并实际经 OpenCSG 到达 IAM 授权页。IAM 的 OAuth redirect_uri 仍为 OpenCSG broker 回调，其 state 中的最终目标正确为 alpha `/v1/access/sso/callback`。报告为 `hosted-installed-check.json`、`hosted-service-status.json`、`hosted-login-upgrade.log`、`hosted-login-entry.json`，远端位于 `/srv/teamx-example/acceptance/evidence`，本地私有工作目录保留副本。验证前多用户登记数为零；这里没有代替用户完成真实业务账号登录或声称双账号验收通过。

人工入口保持 `https://assistant.example.com/`，刷新后使用首个验收账号点击 OpenCSG 登录，在新空间设置模型。个人身份文件已与一致备份逐一核对相同；原个人目录、备份、域名及当前多用户服务均保留。此开发包切换和同模式升级证据不关闭正式 S5a／S5 出口，后续人工验证期间不主动重启或清理环境。

## 多用户首次模型验证：受保护目录缺失

随后用户在已安装实例完成 OpenCSG 登录并进入默认空间的模型设置页，服务已持久登记一个真实用户及其默认 Space。第一次模型提交失败码为 `local_identity:tool_acp`，发生在本机运行环境检查，尚未发送模型请求；页面将它概括为接口协议／工具支持问题，归因不准确。

原因是多用户只在激活时创建各 Space 的数据目录，部署顶层 `home/data` 未创建，但 `selectAgentExecution` 将该配置路径追加到 SRT 的受保护根。SRT 在执行准入时解析每个根，遇到不存在的顶层目录报 `SRT workspace exposes a private root`。无凭据对照在同一安装版本、普通 UID 997、空 capabilities／no-new-privileges 下复现：仅安装描述检查成功，追加实际服务保护根后失败；以服务账号补齐顶层 `data`（0700）后，同一保护配置的 `DSH --version` 返回 `0.2.2-rc.1`。

目标机已补齐目录，未重启或切换当前服务，用户与浏览器会话保留。初始化源码补齐多用户部署的配置数据目录创建，避免新安装缺失该保护根；本机 race 回归及 Linux 普通账号执行的真实初始化入口测试通过，验证目录为 0700、重复 init 可继续、不登记个人身份／业务用户。诊断源码同时区分本机运行环境失败与模型协议／工具能力失败。源码增量尚未重建到当前 `dev-8068c9601f20` 归档，须随下一候选构建纳入；当前机器依靠已持久补齐的目录继续验收。

失败提交的临时 Key 按设计已删除，未恢复或借用个人实例的凭据。独立真实模型探针因候选密钥已删除而未运行模型请求；完整验证由用户在原页面重新提交 Key。相关证据为 `/srv/teamx-example/acceptance/evidence/hosted-dsh-identity-after-data-root-fix.log`、`hosted-data-bootstrap-tests.log`；后续模型结果以重新提交的操作状态为准。

用户重新提交后，对应的模型验证操作已变为 `saved`，安装实例的私有验证收据确认文本、工具往返及清理均通过，执行类型为 SRT。用户随后明确确认“可以了”。当前服务仍为同一进程，未通过重启清除现场；脱敏验收报告为 `/srv/teamx-example/acceptance/evidence/hosted-first-model-acceptance.json`。这补齐安装实例单账号真实登录／首次模型设置的闭环，尚不代替带业务会话的升级、完整备份还原或最终候选冻结。

## 显式开放已认证账号准入

用户随后要求开放其他账号登录和准入。新增部署配置 `deployment.authentication.allow_all_authenticated = true`，允许所选 OpenCSG 认证源完成受保护身份交换的任意非空稳定 subject 注册／登录；默认仍为明确 subject 名单，不将缺省名单解释为开放。新账号各有独立 User／默认 Space 和模型／凭据目录，既有 provider／authority 固定、本地停用／auth version、跨 owner 拒绝及本机部署管理边界保持。该配置不是匿名访问，普通用户不能通过自己的 Space 设置更改全局准入。

为让这次核心升级保留已有模型／成员的运行指纹，托管 Space 若已固定旧 release 的 DSH，则从同一部署私有 `execution` 目录选取对应原描述，保留原发布树及工具 CLI。所选描述必须为私有普通文件，Node／SRT 指向同一旧 release，端点及私有根与当前部署完全一致；实际执行仍校验全部程序／库摘要，缺失或不一致拒绝，不回退原生或更换 Session。新 Space 使用当前 release，独立源码／非托管 SRT 组合保留原显式选择。此处不自动重写模型验证收据或旧成员快照。

定向 race 回归覆盖开放准入下双账号独立空间、交叉访问拒绝、空身份／换源拒绝、本地停用不被重新登录恢复、缺省名单仍关闭、初始配置显式开放、旧描述缺失／链接／扩大端点或私有根拒绝，以及非托管 SRT 不受影响。本机与 Linux 检查通过；实际部署及已安装模型运行指纹验证另据本次升级收据补录。

最终开发包 `dev-f7d7360b0d4b`（SHA-256 `36d1f0ff9b6b4261761f478a8fbc948f4adaec0b9e63f921b345f56e864264c6`）完成完整归档 smoke，经正式升级入口部署；一致备份为 `/srv/teamx-example/backups/hosted-before-open-admission`。包含前述多用户初始化数据目录和模型本机失败诊断增量。对已安装的原模型快照，在 UID 997、空 capabilities／no-new-privileges 下运行真实 SRT／DSH 指纹检查：首次检查超时，保留失败记录；随后同条件重试 22.80 秒通过，原运行身份及旧工具 CLI 保持。没有重置成员或重写模型收据来通过检查。

启用开放配置时发现 `prepareFirstAccess` 还保留必须有非空名单的旧检查，导致仅设置开放开关的首次启动失败。当前安装包使用 `allow_all_authenticated = true` 并保留原 `allowed_subjects` 作为启动兼容项；实际准入使用开放分支，原名单不限制其他已认证账号。源码已补齐该检查，回归改为从空名单的首次初始化一路验证双用户准入／隔离／停用，本机 race 通过；这两项源码增量尚未重建进本包，下一候选须纳入，不能声明本包支持仅开放开关而无名单的冷启动配置。

最终服务 active／enabled、认证控制面 ready；安装身份、原 1 个用户／1 个空间、模型验证收据及成员模型快照核对保留。原浏览器会话的到期时间为 11:03:34（UTC+8），早于升级，其过期记录按既有策略清理，不延长或重新签发会话；保留所有未过期会话的检查通过。真实 Chromium 再次确认 HTTPS Origin、Secure／HttpOnly 登录尝试 Cookie、OpenCSG broker 与 IAM／alpha 回调路径；未代替第二个真实账号登录。原用户需重新登录，其他账号可开始人工验证，各自配置模型。

证据位于 `/srv/teamx-example/acceptance/evidence/open-admission-*`，包括 `after-state.json`、`service-status.json`、`retained-runtime.log`（首次失败另存 `retained-runtime-timeout.log`）、`login-entry.json`、`upgrade.log`、`restart.log`／`start.log`。本次三份发布目录的一致备份清单为 66,694,220 字节，接近当前 64 MiB 读取上限，更多版本时的备份清单容量仍需后续 S5a 处理。完整原路径备份还原、正式前置验收与候选冻结继续保留，不将开放开发环境视为正式出口完成。
