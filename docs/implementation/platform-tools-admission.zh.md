# 平台工具准入、仓库禁用与 Linux 飞书上下文

日期：2026-11-24。范围：多空间方案 S3b.2a。**代码已实施，组件、个人回归及真实 Linux CLI 底层检查通过；真实飞书用户 OAuth、刷新与 DPoP 仍待验收，S3b.2a 的完整出口尚未关闭。** 多用户服务总开关保持关闭，不宣称 S3、S4 或正式多用户支持完成。

用户确认首发必须包含 GitLab 和飞书；Git 仓库代执行与 Scion 可后置。两个平台继续复用个人版的平台注册、连接、成员授权、通知、动作、CLI 和结果 owner，不另建多用户平台实现，也未新增 GitLab HTTP 业务封装。S4b 的共享 GitLab 应用来源／交换仍是独立后续单元。

## 首发能力与装配

`hostToolPolicy` 由部署装配固定后传给 SpaceRuntime／manager／service，不从空间设置、浏览器参数或 Agent 环境选择。个人组合保留原 catalog、工具调用及 Git 行为；受限组合从同一 catalog 选择 GitLab、飞书，去掉 Scion 及未验收的独立可执行工具。Computer 未列入本次受限组合。

| 工具族 | 本次行为 |
| --- | --- |
| TeamX 消息、输入、事项、规则、日历、模型及文件 | 复用原入口、空间授权和结果 owner |
| GitLab 平台 | 复用现有连接、Go API 适配、通知、托管动作及 tools；原 glab 保留 API 请求，原 work-items／issue discussions 扩展保留 |
| 飞书平台 | 复用原 setup／login／logout、Client、Pinned、工具、动作及通知 reader；Linux 注入私有配置与数据根并启用经过实际固定账号校验的发送能力投影 |
| 核心 Git 仓库代执行、Scion | 受限组合禁用；个人保留原能力；不影响已按执行策略验收的沙盒内 Git |

多用户启动的原有拒绝门槛没有移除。当前受限组合通过组件及 CLI 检查验证；正常多用户服务与真实模型／工具的完整组合属于 S3b.3，其后认证仍按 S4a 推进。

## GitLab 复用与 Git 入口清单

服务端工具仍使用现有 `tool.Process` 和 `teamx-gitlab`。受限调用运行在已有临时私有目录，保留原 token 租约、连接／generation／成员绑定及撤权结清。`TEAMX_GITLAB_API_ONLY` 仅由服务端进程环境注入，不能通过工具 argv 放开。

原生 `glab api` 需要明确的相对 endpoint；允许 method、raw/typed fields、JSON stdin、分页等有界参数。拒绝 hostname／header 覆盖、绝对 URL、仓库 placeholder、文件路径输入、form 文件上传及其他原生命令。复杂带 placeholder 风险的 typed JSON 使用 raw fields 或 JSON stdin。现有 Go 扩展仍走原实现，文件正文仅允许 `--file -`。

真实固定版本 glab 即使执行 API 也会在启动时探测 Git。因此临时环境使用仅含固定拒绝程序的 PATH，使隐式 `git` 探测失败；glab 在收窄 PATH 前已解析为可信服务端程序。API host 从选中连接写入临时公开配置，保留自建实例端口，不依据当前目录推断。没有重写 HTTP API 或新增 CLI 沙箱。

| 可达入口 | 受限组合的拒绝位置 |
| --- | --- |
| 仓库 UI／建议 | Workspace 响应给出 `managed_repositories=false`；隐藏仓库设置和初始化选择，保留目录选择器 |
| `/v1/workspace/apply`、直接内部申请 | 平台准备及持久申请前检查目标，拒绝 Repository／Binding |
| `/v1/workspace/retry`、恢复的旧操作 | 原操作和后台执行分别检查；保留失败收据，不启动仓库准备 |
| Room 初始化、打开和自动准备 | `ensureWorkspaces`／有效 Workspace 检查在任何 adapter／Git 探测前拒绝 |
| `workspace-git`、旧客户端 `tools/run` | CLI 帮助投影、工具入口和准备函数均受限；后台不靠隐藏按钮 |
| 平台工具转调 | 同一 tools wrapper 限制 argv；glab 隐式 Git 只能命中拒绝程序；Scion 不装配且旧专用路由拒绝 |
| 仓库 credential/helper 桥接 | `connectionExecute` 和 Scion 桥接在连接解析及执行前拒绝 |

Agent 工具列表、Skill、帮助和有效调用保持一致。新 Session 注入 `TEAMX_TOOL_CONTRACT=shared-api`，该变量是保留的服务端绑定；执行层只负责传递，不将其作为授权凭证。修改本地环境不能放开服务端入口。

## 飞书 Linux 存储与账号固定

装配向原 `ClientFactory` 注入本 Space 私有根及服务端固定的 lark-cli 路径。每次新应用 setup 生成稳定随机 profile，其摘要索引一个私有上下文：

```text
<space-home>/platform-cli/lark/<profile-sha256>/
  profile.json       # 公开引用元数据，不保存 token／密钥
  config/            # LARKSUITE_CLI_CONFIG_DIR
  data/              # LARKSUITE_CLI_DATA_DIR，官方 CLI 管理密钥、密文及 DPoP
```

只有显式 setup 分配新上下文；读取、重启、授权及注销不会自动补建缺失的原目录。所有原调用族和通知 reader 使用同一个 factory。每次进程执行校验目录，缺失、错误程序、跨空间 profile 或符号链接拒绝，不回落部署者默认配置。发现仅枚举本 Space 的已登记上下文，普通用户不能选择服务器可执行文件或提交目录。

Pinned 继续使用原单应用／单用户临时配置与实际账号核验，Linux 保持原 dataDir，不复制密钥或刷新状态。关闭只删除临时配置；注销沿用原连接影响检查，且不触及另一 Space 的本地凭据。相同外部账号的远端授权关系仍由平台决定，不承诺远端撤权互不影响。

配置协调锁按私有根分开，原 profile 门和账号队列仍在。个人默认上下文及 macOS Keychain 路线保持原行为。Linux 原生底层依据固定 [lark-cli 1.0.99 凭据后端](https://github.com/larksuite/cli/blob/v1.0.99/internal/keychain/keychain_other.go)，没有在 TeamX 重写凭据加解密。

## 目录与基础保护

受限组合没有宿主 HOME 的目录浏览兜底；默认成员目录继续可用，显式管理员允许根仍走规范路径、保护根和跨空间占用检查。目录浏览和文件读取的策略快照携带同一归属检查，链接不能绕开其他 Space 的占用。原文件内容、下载、图片、上传及队列限制保留。

复用已有共享 Space／Session 额度及结清 owner；受限组合省略／填零时采用有限默认：总 Space 64、活动 Space 16、每用户 16／8；Session 总数 32、每用户 8、每 Space 4。显式正数仍由管理员配置。原生个人组合的零值语义不变。

平台成员调用最多同时保留 16 个原 call，最长 5 分钟；取消／超时只发信号，原调用返回与清理后才完成收据。受限进程 stdin 在启动远端操作前完整接收，最多 1 MiB／30 秒；stdout＋stderr 共 16 MiB，超额取消并返回失败。原动作的 unknown／核实语义、授权及唯一结果 owner 保留；这些是基础保护，不是 OS 硬配额、公平调度或独立可用性承诺。

## 已执行验证与未关闭门槛

真实 Linux CLI 检查的测试产物摘要、UID、网络范围和输出见证据（本地验收文件 `platform-tools-admission-evidence.json`）。

- 本机：Lark／tool／内置平台／GitLab tools／SRT 包测试；Room 的 Workspace、工具、平台、Lark、DSH、执行、资源和 SpaceRuntime 定向回归；受影响 Workspace 操作及 CLI 撤权结清集成测试。
- 竞态：新增平台准入／目录／资源检查，以及私有上下文生命周期的定向 race。
- Web：应用、文件、CLI tools 的 244 项既有回归通过；新增仓库能力投影测试通过；文件路径及 Space 上下文的 4 项测试通过；语法与 diff 检查通过。DOM 测试不称作真实浏览器验收。
- Linux arm64 本地容器、禁外网、UID/GID 65534：固定 glab 1.127.0 通过本地 TLS 合成 API 验证所选 host／token、私有配置和宿主 Git 拒绝；固定 lark-cli 1.0.99 通过真实官方加密后端生成两个独立主密钥与密文，验证原上下文重开、注销及默认 HOME 无落盘。仅使用合成凭据，不访问真实平台。
- 同一 Linux 容器：原生产 Lark setup、登录、Pinned、重开、注销及双上下文交叉拒绝通过合成 CLI 生命周期测试；该层不冒充真实用户 OAuth／刷新／DPoP。

**仍需真实飞书测试连接完成用户 OAuth、实际刷新、DPoP、授权变化与已选平台操作的验收。** 本轮没有读取日常平台凭据、向真实平台写入、开放第二用户或实施 S3b.2b／S4b。真实工具完整组合与发布出口仍按 S3b.3／所选 S5 判断；未满足的真实验收明确保留，不能仅凭组件测试宣布 S3b.2a 完整通过。
