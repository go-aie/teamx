# 测试

## 日常入口

从仓库根目录执行。局部修改先跑所属包核心及受影响的集成／前端检查；阶段性完整本地回归使用 `make test-integration`，它已包含核心，无需先完整跑一次 `make test`。并发或生命周期修改先做定向 race，最终交付再做全仓 race：

```sh
make test                     # 核心回归，等价于 go test ./... 并附带超时
make test-integration         # 核心 + 本地集成，不访问真实平台或模型
make test-race                # 核心 + 本地集成的 race 检查
go vet ./...
node --test internal/web/tests/web*_test.mjs
```

本地集成夹具还需要 Python 3、Git 和支持 `env -S` 的环境（macOS 或 GNU env）。

保留 Go 默认的构建和结果缓存。需要确认修改后的真实耗时或排查偶发问题时，加 `-count=1` 强制执行；日常入口不强制清缓存或禁用结果缓存。

Makefile 支持 `TEST_FLAGS`，只将选项传给本次顶层 `go test`。例如保留逐项计时日志：

```sh
make test-integration TEST_FLAGS='-json -count=1' TEST_TIMEOUT=25m > /tmp/teamx-integration.jsonl
```

不要用全局 `GOFLAGS=-json` 采集测试日志：夹具中的 `go list`、`go mod edit` 等子命令也会受影响，可能改变协议输出。诊断时放宽 `TEST_TIMEOUT` 只为收齐结果，不表示达到核心三分钟／完整集成八分钟的目标。

按改动模块执行 integration，无需每次覆盖整个仓库：

```sh
make test-integration TEST_PACKAGES=./internal/room TEST_RUN='Lark'
make test-integration TEST_PACKAGES=./internal/platform/lark
make test-integration TEST_PACKAGES='./internal/room ./internal/platform/gitlab' TEST_RUN='GitLab|ReferenceAction'
```

`TEST_RUN` 是 Go 的测试名正则。未匹配的测试不会运行；按模块的验证不能描述为全仓通过。通过 Make 指定结尾锚点时写 `$$`，例如 `TEST_RUN='^TestName$$'`，展开后 Go 才会收到单个 `$`。

## 分层约定

| 层级 | 内容 | 入口 |
| --- | --- | --- |
| 核心 | 状态转换、数据校验、权限、存储提交边界、Actor 生命周期和快速组件回归 | 无额外构建标签 |
| integration | 本地 CLI 协议、HTTP/前端组合、独立进程重启、Git 仓库操作、完整业务链路与组合矩阵 | `integration` |
| 浏览器功能 | 原生 EventSource、实际 DOM、焦点／滚动／触摸、页面恢复与跨会话隔离 | `e2e`，另加各功能场景的显式开关 |
| 专项验收／测量 | 长时间静置、满容量性能、已安装工具、真实平台／模型与实体手机 | `e2e`，长窗口、容量和真实依赖分别显式选择 |

`e2e` 会同时编入核心和本地集成；带标签全跑不等于“仅跑 e2e”。浏览器功能开关不会开启长窗口或满容量专项。

integration 文件使用 `//go:build integration || e2e`，使 e2e 同时具备本地测试的辅助代码。真实验收函数必须受 `e2e` 标签保护，不能仅靠环境变量在默认测试中跳过。复用的辅助函数留在普通 `*_test.go`；不得将模拟器或测试开关加入正式 CLI。

核心测试可以使用临时目录和 `httptest`。是否归为 integration 取决于是否验证进程、协议或完整业务组合，不能只看文件名或是否读写文件。关键校验和失败处理仍需在核心层有快速回归；移到其他层的测试继续保留全部断言。

## 保持执行速度

- Room 中拥有独立 Store、账号队列且不修改共享环境的测试使用 `parallelFixtureTest`；Lark 协议夹具使用 `parallelClientTest`。各包内同时最多执行 4 个，限制 CLI 进程与磁盘同步压力。修改环境、当前目录或共享状态的测试保持串行。不要在并行父测试中调用 `t.Setenv` 或 `t.Chdir`。
- 本地飞书 CLI 夹具使用独立 `ClientFactory`，保留队列、取消、容量及身份校验，但不等待模拟远端请求的生产限流间隔。真实验收和正式服务使用进程级生产队列；FIFO、间隔、冷却与取消由 `internal/platform/lark/budget_test.go` 专门验证。
- 快照目录初始化按目录和父目录身份复用，避免每次写入前重复同步；每次提交仍执行文件同步和重命名后的目录同步。目录或父目录被替换、初始化失败时不会复用旧证据。
- Store 关闭时显式释放文件锁，避免并发 fork 尚未 exec 的子进程通过继承描述符短暂保留旧锁；核心测试通过复制描述符稳定验证这一边界。
- CLI 夹具复用正在运行的测试二进制，保留构建标签和 race 检测，避免嵌套编译；需要正式 CLI 的本地测试共享一次生产构建，loopback 测试无需预先准备二进制。仅短 CLI 子进程取消 race 检测器默认的退出等待，其他 race 选项保留。
- Python 协议夹具仅使用标准库，以 `python3 -S` 启动，避免加载用户 site 配置。
- 新增业务组合优先放在组件层；真实进程测试覆盖必要的协议、重启和资源清理边界。不要给每个业务分支重复完整进程启动流程。

需要查看耗时明细时：

```sh
go test -json -count=1 -timeout=8m -tags=integration ./internal/room > /tmp/teamx-room-tests.jsonl
```

JSON 中测试的 `pass` / `fail` 事件包含 `Elapsed`。串行父测试通常已包含串行子测试耗时；并行父、子测试的计时还会受暂停、等待和资源竞争影响，不能简单累加。整个套件应看包级结果及从开始到结束的墙钟时间。

## 私信与默认助理

模型与生命周期回归覆盖私信固定单成员、创建去重、冻结配置、旧单成员 Room 不改型、跨文件一致性、默认一次性创建、无输入不启动模型、暂停和失败恢复、默认删除保护，以及旧删除记录的显式恢复与提交中断。

按需 Skill 的核心检查覆盖当前成员身份、服务代次、浏览器边界和固定资源请求。独立二进制在源码目录外读取使用指南、无平台绑定读取接入 Skill，以及平台 Skill 的准确账号入口替换，可按下列本地集成入口验证；这些检查不代替真实模型遵循工作流程的验收：

```sh
go test -tags=integration ./internal/room \
  -run 'TestUsageSkill|TestDefaultAssistantSkill|TestExplicitToolBindingPreservesRolesAndReadsSkillWithoutCredentials' \
  -count=1 -timeout=2m
```

```sh
go test ./internal/room -run 'TestDefaultAssistant|TestDirectConversation|TestRoomWithoutDirectFields' -count=1 -timeout=3m
go test -race -tags=integration ./internal/room \
  -run 'TestDefaultAssistant|TestDirectConversation|TestRoomWithoutDirectFields|TestPlatform(Connect|Selection|Member)' \
  -count=1 -timeout=4m
```

真实私信提及验收复用配置中的默认模型，在独立私信中检查普通回复、决策提问、即时提醒通过生产 `send` 保存的正文，并等待每轮结束后再判断。用户测试消息不提示是否使用提及；不读取或写入外部平台。输出目录必须尚不存在：

```sh
TEAMX_CONVERSATION_DSH_REAL=1 \
TEAMX_INPUT_COMPARISON_CONFIG=/absolute/path/to/config.toml \
TEAMX_DSH_OUTPUT=/absolute/path/to/fresh-output \
go test -tags=e2e ./internal/room \
  -run '^TestDirectConversationRealAgentMentions$' -count=1 -timeout=7m -v
```

可选 Chromium 场景需要本机 Playwright 和 Chromium，使用实际 HTTP／CLI 及临时数据，飞书 CLI／成员 backend 为本地夹具：

```sh
TEAMX_DM_BROWSER=1 go test -tags=e2e ./internal/room \
  -run '^(TestDirectConversationBrowser|TestDefaultAssistantRestorationBrowser)$' -count=1 -timeout=3m
TEAMX_CONNECT_BROWSER=1 go test -tags=e2e ./internal/room \
  -run '^(TestDefaultAssistantPlatformConnectionBrowser|TestPlatformConnectConversationBrowser)$' -count=1 -timeout=3m
TEAMX_SELECTION_BROWSER=1 go test -tags=e2e ./internal/room \
  -run '^(TestDefaultAssistantPlatformOnboardingBrowser|TestPlatformSelectionConversationBrowser)$' -count=1 -timeout=3m
```

浏览器覆盖分组和选择恢复、默认删除保护、旧实例显式恢复、普通私信删除、独立草稿、详情 Tab／焦点、窄屏，以及账号／群选择和完整设置返回。接续测试核对原请求、完整成员身份、Inbox 结果与单次配置；受控 backend 的自动回合与 CLI 驱动的等待／主动继续分别验证，不称为真实模型。

已有单账号真实 DSH／模型、飞书与浏览器验证，覆盖默认 DM 的新 OAuth 连接和自动接续，以及复用已核验账号后的群关注、持续规则保存、暂停后恢复接续。用户主动继续的原操作查询由本地 CLI 回归覆盖；第二真实账号、新群消息触发实际业务提醒、真实远端故障和并发自然语言反馈去重仍未实测。连接、订阅保存与实际提醒不能互相代替，复验范围见[飞书真实验收](#飞书真实验收)。

## 本机验证记录

2026-11-20 最近一次独立全仓核心通过，墙钟 **453.328 秒（7 分 33 秒）**，较 T1 修复后的 557.748 秒缩短约 **18.7%**，仍超过 Makefile 的三分钟默认上限。§9.2 第四批后的完整核心＋本地集成 1,933 项顶层通过、9 项跳过，22 包通过；墙钟 948.975 秒（15 分 49 秒），八分钟预算未达。第四批同范围切片三样本墙钟中位数 12.439 → 9.427 秒（约缩短 24.2%）；单并发、定向 race、默认／e2e 编译及 vet 通过，全仓 race 尚未运行，核心预算未重新独立测量。覆盖与计时口径见[定时等待与装配实施记录](implementation/timer-assembly.zh.md)；前三批历史证据保留在各自记录中。不要沿用 11 月 8 日约 70 秒核心／4 分 45 秒集成的数据估算当前运行。

实际测量、覆盖承接和剩余门槛统一见[测试实施与耗时记录](implementation/test-suite-health.zh.md)。Makefile 的三分钟／八分钟上限未因诊断放宽；局部反馈使用 `TEST_PACKAGES`／`TEST_RUN`，长跑前说明范围和预计成本。结果缓存继续保留，计时才加 `-count=1`。

## 少量按需 e2e

会话共享阅读的核心检查覆盖普通打开位置与逐条提及的独立合并、计数去重、v1 有限转换、存储故障、提及匹配和只读目录统计；integration 覆盖固定进入快照后新到消息、独立 HTTP 客户端、重启、生命周期及实时阅读锁隔离。前端 DOM 用例检查回看集合、固定分隔线、详情／后台暂停与移动列表退出、键盘裁剪、目录角标、旧响应丢弃和失败重试，不能代替视觉检查：

```sh
go test ./internal/room -run 'HumanRead|ConversationUnread|ConversationMention' -count=1
node --test internal/web/tests/web*_test.mjs
make test-integration TEST_PACKAGES=./internal/room TEST_RUN='HumanRead|ConversationUnread|ConversationMention'
make test-race TEST_PACKAGES=./internal/room TEST_RUN='HumanRead|ConversationUnread|ConversationMention'
```

已安装 Node、Playwright 和 Chromium 时，可用临时服务、真实文件和两个独立浏览器上下文复验，不需要真实模型或平台。可选的 `TEAMX_HUMAN_READ_ARTIFACTS` 保存截图：

```sh
TEAMX_HUMAN_READ_BROWSER=1 go test -tags=e2e ./internal/room \
  -run '^TestHumanRead(Browser|CatalogBrowser)$' -count=1 -v -timeout=5m
TEAMX_HUMAN_READ_PERF=1 go test -tags=e2e ./internal/room \
  -run '^TestHumanReadCatalogPerformance$' -count=1 -v -timeout=3m
```

浏览器的 `combined-1440`／`combined-390` 子场景从第 1～100 条、默认最近 50 条开始，提及位于第 10、70、95 条，另含 3 条自己的消息及重复 @。检查普通打开后仅剩提及未读、一次点击自动补载最早提及、自然滚动确认、独立页面刷新保留未查看提及、精确剩余数和固定分隔线，以及退出丢弃普通回看提示；还注入保存成功但响应丢失，检查自动同步恢复后计数不复活。可单独复跑：

```sh
TEAMX_HUMAN_READ_BROWSER=1 go test -tags=e2e ./internal/room \
  -run '^TestHumanReadBrowser/combined-' -count=1 -v -timeout=2m
```

其余浏览器场景覆盖未打开会话计数、两个独立上下文的阅读／列表同步、手机列表往返、手动单批与自动连续补载、取消／失败后的接续、清空／切换会话时迟到历史响应失效、单项目录故障、阅读故障期间发送、长名称与 `99+`、1440／390／320px 及短视口布局和触摸长按。仅阅读变化须保持消息节点与滚动位置，不产生「回到最新消息」。

普通 `TestHumanReadBrowser/history-functional` 使用 600 条消息，从最近 50 条出发，保留跨页失败后的已提交前缀、取消后的迟到响应失效、接续、可见锚点、分隔线／节点去重与实时追加。桌面和 390px 各保留完整提及流程；320px 的布局、可见裁剪与真实 Chromium 触摸由阅读及目录场景承接，无需重复完整业务流程。

满容量浏览器测量单独选择，普通阅读开关不会启用它：

```sh
NODE_PATH="$PWD/packaging/verifier/node_modules" TEAMX_HUMAN_READ_CAPACITY=1 \
go test -tags=e2e ./internal/room -run '^TestHumanReadCapacityBrowser$' -count=1 -v -timeout=5m
```

专项的 `history` 和 `history-large` 均从默认 50 条出发，实际构建全部 10,000 条 DOM；后者单条普通正文 6,600 字节，总正文约 62.9 MiB。两者包含网络失败、取消、接续定位、连续输入、可见锚点稳定、日期／新消息分隔线去重、全量历史后的真实消息刷新与滚动，输出批次耗时、Long Tasks、输入响应、阅读帧、序号元数据和 CDP 内存采样。批次墙钟包含主动让出主线程的等待；embedder heap 是估计值，不能当作进程 RSS，几项内存峰值不能相加。大正文保留全部节点仍有较高内存成本，不能据此宣称低内存。

目录性能场景交替运行旧目录文件投影与新目录接口，报告单 Room 10,000 条、10 个 Room 各 1,000 条、接近 64 MiB 样本的 p50／p95、busy 延迟、state 大小与正常读取次数。这些是本机有界测量，不覆盖 1,000 Room 上限，也不是 CI 的性能门槛。

实际手机验收仍待完成；Chromium 小屏、触摸输入和 DOM 键盘裁剪模拟不能替代手机浏览器。复验时在实际手机与电脑连接同一服务，记录设备、系统、浏览器版本及以下结果：

1. 前台打开聊天清除普通未读，未查看提及仍保留；点击较早提及自动补载，成功显示后立即确认，另一端同步。
2. 展开详情／设置、失焦或临时切到后台期间不确认遮挡的提及；恢复后保留本次回看分界和剩余数量。
3. 唤起和收起软键盘，检查消息可见裁剪、右上角回看／右下角返回最新、执行提示和输入框的避让及点击。
4. 返回会话列表再进入，旧普通回看提示消失，未查看提及保留；刷新和跨端操作后计数、位置符合相同规则。

这些实际平台检查完成前，不宣称会话阅读调整整体完成或具备独立发布条件。

日常只检查 e2e 是否能够编译：

```sh
make test-e2e-check
```

真实验收按需选择一个场景，提供该场景要求的配置、授权和独立数据目录：

```sh
go test -tags=e2e ./internal/room -run '^TestRealDSHRoom$' -count=1 -timeout=25m -v
```

该命令仍需场景的显式启用参数，否则会跳过。具体方式见 [Agent 运行时](agent-runtime.zh.md#真实验收)、[Workspace](reference/workspace.zh.md#真实验收)、[通知](reference/notifications.zh.md#接口与验证)与 [GitLab 工具](../tools/gitlab/README.zh.md#真实验收)。不要把全部 e2e 加进日常核心或 integration 入口。

阶段验收结束后，将仍需维护的复跑方法与未完成项收进对应模块说明，删除重复的阶段记录；不要求每次实施新增一份永久验收文档。私有回执、防重记录和必要失败证据独立保留。

## 飞书真实验收

当前验证范围按能力分别判断：

| 能力 | 已有真实验证 | 仍待验证 |
| --- | --- | --- |
| 连接引导 | 飞书已有应用的官方页面回传、profile 与用户 OAuth 核验；默认 DM 的真实 Agent 发起、保存连接及 Inbox 自动接续 | 新应用、Lark 网页路径、实体手机扫码及第二真实账号 |
| 对话群关注与持续规则 | 单账号的账号／群选择、精确群订阅、长期规则保存、暂停后恢复自动接续及真实浏览器设置返回 | 新群消息触发实际业务提醒、真实远端故障及并发自然语言反馈去重；准入阻塞和晚到竞争由本地测试覆盖 |
| 收件与模型消费 | 指定历史窗口到 Inbox、稳定引用、DSH 摘要和 Room 提问 | 现场新消息触发、长期覆盖与时延、后台身份／钥匙串及跨令牌到期续期 |
| 文档 URL 读取 | 单账号、指定 wiki 及其对应 docx 直链，经 TeamX tools CLI／HTTP 到真实 lark-cli，核对正文一致 | 其他账号／知识空间及真实缺权样本；缺权和撤销由本地夹具覆盖 |
| 普通回复、指定单群发送 | 指定真实目标的 user 身份 Agent 闭环、独立正文／作者／目标回读及服务重载后不重发 | 话题回复、其他主动私信、正式 bot Agent 链路及真实多平台同时写入 |
| 本人投递 | 一次真实 Agent 投递且无 Inbox 回流；历史真实回执在独立进程恢复；Chromium 界面检查 | 真实平台写后超时与并发发送故障；已有故障注入使用隔离测试端 |

### 飞书连接验证

`node internal/room/testdata/lark_guided_connection_browser.cjs` 检查生产页面的引导状态及桌面／手机尺寸布局，API 使用隔离模拟数据；不能算作新应用、Lark 或实体手机的真实连接通过。

### 飞书消息与动作验证

历史窗口闭环入口为 `TestRealLarkNotificationInbox`（`internal/room`，`-tags=e2e`），通过 `TEAMX_LARK_TEST_INPUT` 提供私有 JSON：明确的 `profile`、`brand`、两个 `chats` 和不超过 24 小时的 Unix 秒 `start/end`。真实模型另设 `TEAMX_LARK_DSH_REAL=1`、全新绝对 `TEAMX_DSH_OUTPUT` 及[DSH 安装／模型环境](agent-runtime.zh.md#真实验收)。

持续发现仅使用隔离来源状态，不创建生产订阅或更改登录：

```sh
TEAMX_LARK_ALL_REAL=1 TEAMX_LARK_TEST_PROFILE=EXPLICIT_PROFILE \
TEAMX_LARK_TEST_BIN=/absolute/lark-cli TEAMX_LARK_TEST_BRAND=feishu \
TEAMX_LARK_RUN_SECONDS=180 TEAMX_LARK_REPORT=/absolute/private/report.json \
go test -tags=e2e ./internal/platform/lark -run '^TestRealLarkAllChatsContinuous$' -count=1 -v
```

允许运行 30～1800 秒，报告只保存聚合数量；前台 CLI 持续运行不替代后台部署及跨到期验证。普通回复、群发送和本人投递的复跑分别使用 `platform_lark_reply_dsh_real_test.go`、`platform_lark_send_dsh_real_test.go` 和 `platform_lark_self_send_dsh_real_test.go` 的显式开关与输入合同。新发送须另行指定准确账号、目标、正文及次数；保留 `.local/lark-l4-c/*native-attempt*`、`.local/lark-self-send-s2/native-attempt*`、原消息 ID 和 unknown 回执，不删除防重记录来重试。`TestRealLarkSelfReadOnlyRecovery` 可通过 `TEAMX_LARK_SELF_RECOVERY_SOURCE` 指向成功投递的 rooms root 只读复验，不新增发送。

### 飞书文档只读验证

使用已登录的准确 profile 和允许读取的文档 URL，沿正式 tools CLI／HTTP 入口验证，不创建文档、不授权、不运行真实模型。日志只报告正文大小和完整性，不输出正文：

```sh
TEAMX_LARK_TEST_BIN=/absolute/lark-cli TEAMX_LARK_TEST_PROFILE=EXPLICIT_PROFILE \
TEAMX_LARK_TEST_DOC_URL='https://example.feishu.cn/wiki/TOKEN' \
go test -tags=e2e ./internal/room -run '^TestRealLarkDocumentTool$' -count=1 -v -timeout=90s
```

Lark 账号另设 `TEAMX_LARK_TEST_BRAND=lark`。wiki 返回不同的原生文档 ID 时，还会读取同一文档的 docx 直链并核对正文一致。本地回归覆盖 `/docx/`、`/wiki/`、URL 校验、引用／警告保留、应用和用户缺权、文档无权访问、输出超限、账号切换及读取中的工具撤销；真实单文档读取不替代真实模型选用工具或完整权限矩阵验收。

### 飞书图片验证

真实下载和模型读图已有通过记录，实际浏览器图片视觉检查尚未通过；服务重启后的新消息闭环、已消费历史消息补图、恢复／轮换后识图及完整权限变化矩阵仍待验证。

复跑入口为 `TestRealLarkImageMaterials`（`internal/room`，`-tags=e2e`）：`TEAMX_LARK_IMAGE_INPUT` 指向包含 `profile`（`platform.CLIProfile`）、已保存通知 `item` 和可选 `model` 的私有 JSON，不含 OAuth 凭据；`TEAMX_DSH_OUTPUT` 使用全新绝对路径。模型验证另设 `TEAMX_LARK_IMAGE_MODEL=1` 及 `TEAMX_LARK_IMAGE_EXPECT`（像素中独有文字的 JSON 数组，不放入提示），安装与凭据沿用 DSH 配置。`TEAMX_LARK_IMAGE_BROWSER=1` 启动隔离页面，实际目视确认后才写输出目录的 `browser-done`。下载、模型结果与页面显示分别判定，不互相替代。

## Scion 真实验收

固定 CLI 的只读成员调用、三份原生 clone、失败后的显式重试和正常重启已有通过记录。真实 DSH 已完成开发、创建 Review、独立评审、修正、更新同一 Review revision 与复审，并在一次评论响应丢失后只读核查作者、正文、版本与唯一性。续验经过模型限流后的结清与正常恢复，不证明连续无干预协作可用。

实际连接、工具绑定与 Workspace 页面操作，多任务／多账号、完整故障矩阵、复杂设计交接和 Review 合入仍待验证；其他发布平台及非 Unix 进程树结清也未验收。组件或模拟页面通过不替代这些结果。

只读入口为 `TestScionRealMemberRead`、`TestScionRealWorkspace`（`internal/room`，`-tags=e2e`），设置 `TEAMX_SCION_REAL=1`、`TEAMX_SCION_TEST_CREDENTIALS`、`TEAMX_SCION_BIN`、`TEAMX_SCN_BIN`，路径均明确指向隔离凭据和实际程序；登录发现另受 `TEAMX_SCION_AUTH_REAL` 控制。新协作验收会真实发布任务分支、提交、Review 及评论，须先指定仓库与写入范围：

```sh
TEAMX_SCION_S4_REAL=1 TEAMX_SCION_S4_MAX_TOKENS=8192 \
TEAMX_SCION_CONFIG=/absolute/config.toml TEAMX_DSH_ENV_FILE=/absolute/private/model.env \
TEAMX_DSH_OUTPUT=/absolute/new-evidence-dir \
TEAMX_SCION_TEST_CREDENTIALS=/absolute/private/credentials.json \
TEAMX_SCION_BIN=/absolute/teamx-scion TEAMX_SCN_BIN=/absolute/scn \
go test -tags=e2e ./internal/room -run '^TestScionRealDSHCollaboration$' -count=1 -timeout=25m -v
```

失败后先检查原 Room、分支、pending、Review 和评论，不直接重跑新建流程。`TestScionRealDSHReviewContinuation` 只用于已经核实的既有 Review，`TEAMX_SCION_S4_SOURCE` 指向原证据根；从修正阶段续验时设置 `TEAMX_SCION_S4_START=correction` 及 `TEAMX_SCION_S4_PRIOR_EVIDENCE`。保留工作副本和原回执，通过正式恢复门禁继续，不重建 Review 或重放未知写入。

## Web 更新通知与自动读取

生产会话页面使用统一 SSE；无变化时不进行周期显示读取。普通调度回归使用注入时钟和 EventSource，不依赖浏览器、真实模型或外部平台：

```sh
node --test internal/web/tests/web*_test.mjs
go test -race -tags=integration ./internal/room -run '^TestUIUpdate' -count=1 -timeout=4m
```

安装 Playwright／Chromium 后，普通功能集覆盖两种服务的原生订阅、消息补读、后台恢复、草稿／焦点及模型回执丢失，不进入 60 秒空闲窗口。模型验证使用受控 probe，无外部模型调用：

```sh
NODE_PATH="$PWD/packaging/verifier/node_modules" TEAMX_WEB_UPDATES_BROWSER=1 \
go test -tags=e2e ./internal/room \
  -run '^TestWebUpdates(ApplicationBrowser|ModelOutcomeBrowser|AuthenticationBrowser|BrowserSubscription)$' -count=1 -v -timeout=5m
```

通知来源、分类、调度和多端隔离见 [Web 更新说明](implementation/web-updates.zh.md)。阅读、Inbox、Policy 与授权组合可通过以下入口复跑；实际手机与部署代理需独立验收。

Room 切换的独立浏览器检查使用隔离服务和受控 runtime，为普通 HTTP 读取增加 100ms 延迟，并拦住 Workspace 响应，验证缓存内容在 HTTP 核对前显示、发送仍禁用。另检查单轮聊天读取、全局配置复用、草稿和手机返回导航；不调用真实模型或外部平台：

```sh
NODE_PATH="$PWD/packaging/verifier/node_modules" TEAMX_WEB_UPDATES_BROWSER=1 \
go test -tags=e2e ./internal/room -run '^TestWebRoomLoadingBrowser$' -count=1 -v -timeout=2m
```

```sh
NODE_PATH="$PWD/packaging/verifier/node_modules" \
TEAMX_HUMAN_READ_BROWSER=1 TEAMX_CONFIGURATION_BROWSER=1 TEAMX_CONNECT_BROWSER=1 \
go test -tags=e2e ./internal/room \
  -run '^(TestConfigurationObservationBrowser|TestLarkAuthorizationAndMemberRepairBrowser|TestHumanReadBrowser)$/(desktop|mobile|reading|combined-1440|locating)$' \
  -count=1 -v -timeout=4m

NODE_PATH="$PWD/packaging/verifier/node_modules" TEAMX_HUMAN_READ_BROWSER=1 \
go test -tags=e2e ./internal/room -run '^TestHumanReadCatalogBrowser$' -count=1 -v -timeout=2m

# OUTPUT 必须是尚不存在的绝对目录。
NODE_PATH="$PWD/packaging/verifier/node_modules" TEAMX_INBOX_BROWSER=1 \
TEAMX_INBOX_BROWSER_OUTPUT=/tmp/teamx-inbox-browser-new-run \
go test -tags=e2e ./internal/room -run '^TestInboxPageBrowser$' -count=1 -v -timeout=3m
```

活动采集的普通功能回归使用隔离服务、GitLab 空查询和原生 Lark CLI fixture，等待实际诊断帧与采集轮次；两个独立登录上下文核对摘要/诊断读取、同会话及不同会话、后台恢复与单端退出。无真实模型或外部平台写入。设置输出文件可保留每页和合计请求/正文字节、通知类别及 DOM 延迟：

```sh
NODE_PATH="$PWD/packaging/verifier/node_modules" TEAMX_WEB_UPDATES_SCOPE_BROWSER=1 \
TEAMX_WEB_UPDATES_SCOPE_OUTPUT=/tmp/teamx-web-updates-scope.json \
go test -tags=e2e ./internal/room -run '^TestWebUpdatesNotificationScopeBrowser$' -count=1 -v -timeout=5m
```

真实长窗口单独启用：空闲专项分别观察单 Room／manager 60 秒零额外读取及至少三次生产心跳；采集专项保留原 60 秒窗口、至少 55 轮实际采集、各 provider 至少五次查询及每端至少 20 个诊断帧，记录请求／字节与消息 DOM 延迟。受控前端时钟检查不作为这些真实时间证据：

```sh
NODE_PATH="$PWD/packaging/verifier/node_modules" TEAMX_WEB_UPDATES_IDLE=1 \
go test -tags=e2e ./internal/room -run '^TestWebUpdatesIdleBrowser$' -count=1 -v -timeout=3m

NODE_PATH="$PWD/packaging/verifier/node_modules" TEAMX_WEB_UPDATES_SCOPE_LONG=1 \
TEAMX_WEB_UPDATES_SCOPE_OUTPUT=/tmp/teamx-web-updates-scope-long.json \
go test -tags=e2e ./internal/room -run '^TestWebUpdatesNotificationScopeLongWindowBrowser$' -count=1 -v -timeout=3m
```

旧轮询的 scope 对照继续保留，但 `TEAMX_WEB_UPDATES_SCOPE_BASELINE=1` 必须搭配长窗口入口，不能进入普通功能集。历史 frozen／FIFO／四工具／两工具及配对模型记录和未完成验收继续按各能力文档维护，本次没有删除其证据或加入日常门槛。package／release CI 保留核心、标准 vet、Web 和打包检查，浏览器、平台与模型验收仍显式按需运行。

分类、安装结果不变、summary 合并优先、成员收据与目录隔离的普通/race 回归：

```sh
go test -race -tags=integration ./internal/room ./internal/platform/notification \
  -run '^(TestNotificationUpdate|TestMemberObservation|TestInstalledStateObservation|TestUIUpdate)' -count=1 -timeout=4m
```

诊断详情可见时仍会按限频读取，实际错误或业务变化也会触发补读；有 SSE 不等于没有 HTTP。验收应分别统计普通页面、诊断详情和业务变化窗口，确认采集指标没有连带刷新 Inbox、成员或完整目录。

配对性能测试只在显式开启时运行，需要本机安装 Playwright／Chromium。它通过 `git archive` 只读加载指定提交的旧轮询静态资产，运行在当前后端及各自新建的相同数据上，不切换工作区；基线引用必须仍包含旧显示轮询。测试同时记录实际 HTTP、独立历史分页、字节、CPU、物理块计数、进程 mutex 等待、内存抽样、长任务和安装至 DOM 延迟；完整配对运行检查总 HTTP／字节及负载窗口的单位时间 CPU 下降，同时披露按实际提交数归一化的 CPU 成本；不要求每个小场景的每条消息 CPU 都下降。

```sh
NODE_PATH="$PWD/packaging/verifier/node_modules" TEAMX_WEB_UPDATES_PERF=1 \
TEAMX_WEB_UPDATES_POLLING_REF=7be23946f31d7502959a4cbe26abf23afa3d7acd \
TEAMX_WEB_UPDATES_PERF_OUTPUT=/tmp/teamx-web-updates-new-measurement \
go test -tags=e2e ./internal/room -run '^TestWebUpdatesPerformance$' -count=1 -v -timeout=8m

NODE_PATH="$PWD/packaging/verifier/node_modules" TEAMX_WEB_UPDATES_BROWSER=1 \
go test -tags=e2e ./internal/room -run '^TestWebUpdatesRestartBrowser$' -count=1 -v -timeout=2m
```

性能测量应在其他回归结束后单独执行，输出目录使用新路径。正式比较保留每个实际测量窗口与注入数；热缓存物理读取为零不等于没有文件读取，进程 mutex 等待也不能归因于某一把锁。

全仓 fixture 数量较多时可通过 `TEST_TIMEOUT` 指定单包总时限；默认 core 仍为 3 分钟、integration／race 为 8 分钟，不改变单项用例的超时或断言：

```sh
make test TEST_TIMEOUT=20m
make test-integration TEST_TIMEOUT=20m
make test-race TEST_TIMEOUT=30m TEST_PACKAGES='./internal/room ./internal/platform/notification ./internal/agent/dsh'
make test-e2e-check
```


### Agent History 生产入口

History 默认工具集与旧 Room 读取 CLI 退出的证据见 [H4 实施记录](implementation/agent-history-h4.zh.md)。`TestHistoryProductionServiceRealDSH` 在显式启用时通过正常服务构造、成员调度、真实 DSH／ACP 和模型核验两种 Inbox 模式的工具发现、图文、cursor 续页与消费边界；通知和图片下载使用 fixture。它与真实平台材料验收、日常运行服务的正式切换分别记录，不进入普通 CI。复跑需要现有模型目录和新绝对输出目录，命令见该记录。

## 共享电脑

本机 Docker 托管启停的显式验收为 `scripts/computer_managed_acceptance.py --run --output <新目录>`，可加 `--agent-config <配置>` 组合双真实 DSH／模型及实际请求 Schema 检查。正常回归不调用 Docker；本轮真实平台、镜像发布和失败尝试的边界见[托管启停记录](implementation/computer/docker-lifecycle.zh.md)。

普通 Go／前端回归及 `make test-e2e-check` 不依赖运行中的 Computer。连接缺省关闭和已正常停用的无容器路径由核心测试覆盖；真实模型、供应方与浏览器始终需要显式开关，不因检测到 Docker 而自动执行。

```sh
go test -race ./internal/computer/... -timeout=3m
go test -race -tags=integration ./internal/room \
  -run 'TestComputer|TestEnvironment|TestRemovedMember' -timeout=8m
python3 scripts/computer_view_acceptance.py --run --group \
  --agent-config /absolute/path/to/config.toml \
  --output .local/computer-combined-new
```

最后一个命令运行真实固定 AIO Runtime、TeamX HTTP、两个 DSH／模型 Agent 和 Chromium 接管交还，使用一次性虚构账号。省略 `--agent-config` 时 Agent 为受控后端；两种证据分别标记。`390x844` 只是移动视口模拟，不代表实际手机验收。图像任务、实际请求 Schema 卸载及无容器真实模型恢复另见[成员能力验收](implementation/computer/agent-capabilities.zh.md)。旧 `TEAMX_ENVIRONMENT_CONFIG` 裸网关验收已移除，历史证据保留。

## Space 重置

个人及 Linux 多用户的行为、恢复边界和复验命令分别见[个人空间重置](implementation/space-reset-personal.zh.md)与[多用户空间重置](implementation/space-reset-hosted.zh.md)；最终归档的新装／升级及重置冒烟见[安装交付验证](implementation/space-reset-delivery.zh.md)。核心测试不读取日常凭据；真实 DSH／模型、Linux 凭证材料、Chromium 与独立进程中断均通过显式开关运行，不能用夹具结果代替真实验收。Linux 开发版已接通重置，正式发行沿用既有前置。
