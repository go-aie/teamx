# Scion 工具／通知对话配置

日期：2026-11-27。对应 [Scion 计划 N1b](../SCION-ISSUE-REVIEW-NOTIFICATION-PLAN.zh.md#n1b工具通知的对话式配置)，继续沿用 N1 的 **open Issue 新建通知**范围。

## 行为与边界

一句“给你开启 Scion 工具，并关注 OpenCSG/scion 从今天开始的新 Issue”可以完成两项本地配置。已有准确连接直接复用；有多个候选时使用原账号选择卡片，返回真实 PlatformID／版本及成员身份。没有可用连接时，可在同一张账号卡片内填写 PAT 登录，页面自动保存并核验连接；PAT 不进入 Room 消息或配置 JSON。选择本身不开启工具或通知。

工具继续使用原绑定与取消结清服务；通知继续使用 `configureScionSubscription`，与 N1 页面读取、修改同一订阅。两项配置复用原 human SourceSeq，各有稳定操作 ID 和准确结果；一项失败时保留另一项实际结果，不增加跨配置事务。新 Scion 回执使用 schema 4，原合法 schema 1／2／3 回执继续读取，不做迁移或自动启用。

`member get` 返回 `scion_notifications`、准确订阅 ID／版本、冻结起点及工具状态。通知初次配置使用独立 `scion` patch；CLI 支持 `--scion-repository`、`--scion-object-type issue`、`--scion-backfill-start RFC3339` 或 `--scion-backfill-seconds`、`--scion-interval-seconds`。开始时间在操作接纳时冻结。首次纯开启缺少仓库明确返回“请选择仓库”；未配置时关闭保留无订阅状态。后续开关保留范围、起点、进度和原回执；间隔修改使用完整观察版本模式。

Scion、GitLab、飞书配置字段互斥。远端仓库读取之后和最终保存前重查原成员、账号／凭据、订阅身份和版本。换号不能继承旧订阅；同账号凭据更新仍须刷新绑定。原 ID 的 unknown 回执重复请求只返回原结果，status 不重新 apply。

未开放 Review、评论／revision 动态、类型编辑、仓库替换、起点重置、共享 Inbox、在聊天中输入 PAT 或 Linux 多用户 Scion。本次未部署，也未给正式服务启用采集。

## 2026-11-28：账号卡片登录接续

空账号卡片明确提示尚未登录，直接展示 PAT 密码框、查看／创建 PAT 链接、「登录并继续」和次要的完整设置，隐藏刷新、提交与取消选择；账号读取失败时提供重试。已有可选账号后显示正常选择操作，空提交始终禁用。多个失效连接按名称、实例分别展示，用户明确选择要恢复的连接；已知实例不重复询问，仅实例不明确时显示地址输入。卡片直接复用现有 human-only 登录、状态、保存和核验接口；一次点击自动完成登录、保存、核验，再按原选择操作 ID、原草稿版本提交准确的连接 ID／版本，结果走原 Inbox 接续。重新登录保留原连接 ID、名称，并展示与提交共享会话的影响范围，不另建重复连接。

PAT 只存在于活的密码控件和首次登录请求中，提交及 pagehide 时清空，不进入消息、选择草稿或任何模型参数。授权状态沿用现有 SSE／观察调度；各写入发出前固定阶段，登录应答丢失按 session ID 查询，保存／核验应答丢失按原连接 ID 和 profile 核对，不重发凭据或重放写入。登录失败、核验失败、账号／原成员／草稿变化不会覆盖原选择；未提交的输入不完成选择，从完整设置返回及平台更新会刷新卡片账号。浏览器刷新前已保存的连接，可回到原卡片刷新并选择。此次未增加后台持久登录会话或新数据迁移，未改变工具／通知的独立配置边界。

前端回归覆盖卡片内登录、重新登录、空提交、多个失效连接、登录／核验失败、版本冲突、成员变化、登录／保存／核验／选择应答丢失、SSE 迟到结果、重复点击、未知实例、PAT 链接限制及 pagehide 清空。`TestScionAccountSelectionLoginBrowser` 使用真实 Chromium、TeamX HTTP 和隔离的模拟 Scion CLI，覆盖首次登录及退出后的原连接恢复、桌面／手机布局、未提交密码的刷新清空、一次点击自动保存核验、原卡片提交、页面刷新后回执恢复及原 Agent Inbox 结果交付；全程不打开平台设置。该浏览器测试不调用真实 Scion 服务或模型。

## Scion 接入卡片顺序

Scion 账号和仓库选择分别在聊天流中显示独立卡片，按已持久化操作的创建时间与文本消息排序，不再一起追加到最初的用户消息下。账号卡片内仍是一键登录、保存、核验、提交；随后 Agent 处理原结果并创建独立仓库卡片。完成后的两张卡片均保留，不折叠；GitLab、飞书保持原展示。

独立卡片只是原操作的页面投影，不新增 Room 消息、不改变原 human SourceSeq、选择回执、成员授权或 Inbox 接续。刷新复用同一操作和控件，页面重载恢复顺序与结果；清空记录或切换会话后不残留旧卡片。前端回归覆盖两步顺序、同毫秒时间精度、未提交 PAT 的保留及重载恢复；`TestScionRepositorySelectionBrowser` 用真实 Chromium 和隔离模拟 CLI 验证两张卡片分开、消息先后、桌面／手机展示和重载恢复。

## 个人版新空间接入

个人版默认空间和新空间使用同一目录声明及部署策略判断 Scion 支持，不再按“是否原默认空间”禁止接入。Space Runtime 核对注册根目录后装配私有 Scion 能力；`/v1/spaces`、HTTP 准入和 Agent `platforms list` 保持一致，受限多用户部署仍拒绝 Scion。凭据沿用各空间独立 Data 下的 `scion-profiles`，没有复制默认凭据、增加迁移或改变登录接口。Agent 账号列表补回 Scion 连接，并返回 `scion_enabled`；不可用时准确说明能力限制，不引导 PAT 登录或等待恢复。

`TestScionSpacesCredentialIsolationLogoutAndRestart` 经过完整 Space 路由验证独立登录、相同登录操作 ID／连接 ID 的空间隔离、拒绝借用其他空间的 profile、账号列表／卡片选择、原请求的工具和 Issue 通知配置、独立退出，以及重启后的回执、订阅身份和接收起点恢复。`TestScionNewSpaceAccountLoginBrowser` 真正创建并初始化一个新空间，通过该空间的 Agent 执行凭据创建卡片，再用 Chromium 一键登录／保存／核验／提交，核实原 Inbox 交付和默认空间凭据未变；使用隔离模拟 CLI、受控模型组件，不声称真实 Scion 服务或真实模型验收。

复现新空间验收：

```sh
make test-race TEST_PACKAGES=./internal/room \
  TEST_RUN='TestScionSpace|TestSpaceRuntime|TestSpacesIndependentSetupRoutingAndRestart|TestSpacesScopedCLIAndCallback|TestSharedHostTools'
TEAMX_SELECTION_BROWSER=1 go test -tags=e2e ./internal/room \
  -run '^TestScionNewSpaceAccountLoginBrowser$' -count=1 -timeout=3m
```

## 验证

组件／集成覆盖 CLI 初次工具＋通知、同一人类消息两项独立配置、页面／对话一致、暂停／恢复、间隔修改保留起点、工具关闭保留通知、缺仓库和范围锁定、unknown／应答丢失防重及重启回执读取、成员版本围栏、字段互斥和只选择不配置。GitLab／飞书原配置和选择路径一起回归。

```sh
make test-race TEST_PACKAGES=./internal/room \
  TEST_RUN='TestScionMember|TestScionNotification|TestPlatformMember|TestPlatformSelection|TestGitLabMember'
make test TEST_PACKAGES='./internal/platform/scion ./internal/app/builtinplatforms/scion ./internal/app/platforms ./internal/platform/tool'
node --test internal/web/tests/web_platform_selection_test.mjs \
  internal/web/tests/web_member_platforms_test.mjs \
  internal/web/tests/web_platform_connect_test.mjs \
  internal/web/tests/web_scion_notifications_test.mjs \
  internal/web/tests/web_platform_onboarding_test.mjs
```

定向 race 与平台包回归通过，前端 142 项通过。未重新宣称 Room 整包全绿；N1 记录中的既有整包失败不在本阶段修复范围。

真实验收另用 `TestScionMemberRealConversation`：安装的 DSH／当前配置模型、正常 Agent 指引和 CLI、真实 Chromium、隔离凭据副本、真实原生只读 Scion 查询和成员输入消费。读保护包装器只转发 version／whoami／repo info／issue ls，其他原生命令拒绝。真实验收已通过：当前配置的 `deepseek-v4.1-flash-1017` 经安装的 DSH，先在没有连接时给出登录引导；Chromium 打开既有 PAT 表单，随后测试装配隔离的已验证凭据副本。模型创建 Scion 账号选择卡片，浏览器按准确连接提交，模型用同一 human SourceSeq 分别完成工具与通知配置，并实际执行只读原生仓库查询。两次独立成功运行中，后台 worker 分别交付 5 条／3 条查询时仍为 open 的真实 Issue，模型经正式输入工具读取并确认其中一条；读取结算依据持久化的 settled 页，不以模型答复代替。

页面独立核实保存的同一仓库、工具／通知开关和冻结起点；账号卡片有桌面／手机截图，PAT 登录表单与配置页另有截图，页面错误为 0。私有证据目录为 `.local/scion-member-conversation-20261127-04/` 和 `.local/scion-member-conversation-20261127-05/`，包括模型会话、请求回执、原生命令记录、输入消费状态和截图。测试没有执行 PAT 提交登录、远端业务写入或正式服务配置；首次登录只验证引导与实际表单，不宣称完整对话式登录。最后一轮额外捕获工具 HTTP 响应，确认一次原生 JSON 读取成功且退出码为 0。两个运行使用不同时间的实时 open 集合，条数不作为同一快照比较。

```sh
TEAMX_SCION_MEMBER_REAL=1 \
TEAMX_SCION_MEMBER_OUTPUT=/absolute/fresh-output \
TEAMX_SETTINGS_CONFIG=/absolute/config.toml \
TEAMX_SCION_TEST_CREDENTIALS=/absolute/credentials-copy.json \
TEAMX_SCION_BIN=/absolute/scn \
go test -tags=e2e ./internal/room -run '^TestScionMemberRealConversation$' -count=1 -v -timeout=15m
```
