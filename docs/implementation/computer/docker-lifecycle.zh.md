# Computer 本机 Docker 托管启停

原始日志、截图和 JSON 回执仅在本地保存；本文保留验收结论、复跑入口及已知限制。

日期：2026-11-24。范围：共享 Computer 方案 C5。代码、macOS Docker Desktop、真实设置页、双真实 DSH／模型及 Chromium 全局停启闭环已验；固定候选已推送 GHCR 并核对远端 digest，包当前 private；公开分发、匿名冷拉取和 Linux 宿主仍待完成，详见[发布记录](docker-publication.zh.md)，因此不标记 C5 全部完成。C3b 文件交付、远端 Docker 和多空间编排未纳入。

## 行为与代码接点

「设置 → 电脑」增加本机 Docker 托管和手动连接两种方式。新增安装的页面推荐托管，但全局缺省关闭；已有配置缺少 `runtime_mode` 仍按手动连接解释，不收编 C4 的日常容器。托管模式隐藏地址／密钥，显式环境检查只读。全局开启按需准备固定镜像、私有密钥、卷与容器，Runtime 管理合同通过后才提交有效连接；准备中可关闭，两个请求保留同一应用回执。

- `internal/computer/docker` 只管理本机 CLI、固定镜像和自有容器，不包含 Room、模型或工具动作。context、Unix socket、daemon、容器／卷归属、镜像及挂载均核对；不跟随已保存连接之外的全局 context 变化，不接受远端 TCP／SSH 引擎。
- `computer_connection.go`／`computer_docker.go` 复用一个应用日志和连接锁，后台执行长步骤并持久记录每一步。Docker 成功但回执丢失后先检查原资源；准备、API 就绪、连接核验、成员／Session 应用和停止分别推进，不让一次 10 秒连接步骤承载镜像下载。
- 关闭先拒绝新调用和接管，结清输入、调用、screen 和 Session，再停止容器并检查进程状态。Docker 离线或结果不确定时保留关闭意图及等待状态。停止保留两个命名卷、profile、文件和密钥；成员／Room 操作仍只停止对应 screen。
- Runtime 重启时旧服务租约继续失权；托管连接按 TeamX 服务代次与已认证 Runtime boot 派生新的 Computer 服务代次。旧执行仍先对账，不重放业务。Docker 动态端口改变时只接受由同一容器派生的 endpoint 更新，并刷新成员连接；此更新也覆盖提交回执丢失后的恢复。
- 正常 TeamX 退出不修改全局 enabled，托管容器可保留运行；服务恢复后异步确保原实例。正常全局停用完成后，启动及打开设置不需要 Docker，也不发起 Docker／Provider IO。

托管切到手动连接时必须提供手动 Runtime 的密钥，不把自动生成的托管密钥发往新地址；同一个托管 instance 不允许直接改作手动连接再停止它，拆离／收编仍后置。首次准备中关闭会保留用户选择的运行方式与资源参数，尚未进入容器创建时可修正 CPU／内存；已创建资源保持原配额。

容器保留 `--init`、资源限制、`no-new-privileges`、命名卷和 loopback 管理端口；不挂宿主目录或 Docker socket，不发布 VNC／CDP／worker。密钥用 root 持有的私有文件注入，不进入参数、环境变量、标签、日志或浏览器存储。公共镜像拉取使用独立匿名 Docker 客户端，避免后台服务等待桌面钥匙串助手。

## 镜像候选与发布边界

镜像路径为用户选择的 `ghcr.io/go-aie/teamx/computer`。固定 manifest／config digest 见[镜像锁](../../../internal/computer/docker/image.lock.json)；同时记录 config digest 以兼容 Docker 传统与 containerd 存储的本地 image ID 差异。构建元数据的两类摘要见 [Docker build metadata](https://docs.docker.com/reference/cli/docker/buildx/build/#write-build-result-metadata-to-a-file---metadata-file)。

`scripts/computer_image.py build` 生成候选，不默认发布；`publish` 仅推送已锁定的候选并生成实际 registry digest 回执和发布锁。独立手动 workflow 保留显式 publish 输入，不改变普通 Release CI。发布使用仓库 source label 关联 `go-aie/teamx`；GHCR 首次发布缺省可见性和匿名拉取须另行核验，见 [GHCR 文档](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)。

首轮实施未发布镜像；原检查记录保留当时的未发布状态。后续复用用户已有 GHCR 登录，已成功推送固定候选 `runtime-a6ce189578019f83`；[发布回执与远端核验](docker-publication.zh.md)确认 registry digest 与原镜像锁一致。Docker Desktop 凭据助手等待问题通过临时客户端配置改用同一 macOS 钥匙串登录解决，未修改全局配置。当前包为 private，匿名请求返回 HTTP 401，公开可见性与匿名冷拉取通过前，不宣称新安装的一键启用已交付。未执行 Git 提交／推送或日常服务更新。

## 验证与待完成项

受控测试覆盖完整启停、停止失败保留待处理、准备中取消、应用逐步重启与原回执恢复、daemon／容器／卷漂移拒绝、创建／启动回执丢失、密钥文件、传统／containerd 镜像身份、只读环境检查及关闭后的零 IO。租约测试验证同一 TeamX 进程可获得新 Runtime boot 的控制权，同时旧代次仍被拒绝。CPU／内存在尚未进入容器创建时允许修正，创建后保留原资源。

设置页的普通前端与真实 Chromium 组件测试覆盖托管／手动切换、隐藏地址与密钥、显式检查不保存、模拟架构提示、准备中关闭以及桌面／390px 视口。组件 HTTP 后端为夹具，不充当真实 Docker 证据。

真实验收入口：

```sh
TEAMX_COMPUTER_DOCKER_REAL=1 TEAMX_COMPUTER_EVIDENCE="$PWD/.local/computer-docker-proof" \
  go test ./internal/room -run '^TestComputerRealManagedDockerLifecycle$' -count=1 -timeout=12m -v
python3 scripts/computer_managed_acceptance.py --run --output .local/computer-managed-browser
```

第二条可增加 `--agent-config /absolute/path/to/config.toml`，组合两位真实 DSH／模型并记录实际模型请求中的工具名称；不提供则是受控 Agent。生产全局保存／应用 HTTP、Docker、Runtime、浏览器和虚构账号为真实实现。每次只创建一次性资源，测试清理单独核对；日常容器不属于测试清理范围。

## 本轮实际证据

最终组合回执和运行报告来自固定派生镜像、ARM64 macOS 宿主上的 amd64 模拟执行、生产设置页及 HTTP、真实 Runtime、两位真实 DSH 与 `deepseek-v4.1-flash-1017`、真实 Chromium。用户菜单打开设置后，实际页面操作四次全局开关：首次自动创建／启动、关闭、再次开启、最终关闭；每次只有一个保存请求，不手动执行部署用的 docker create／start／stop。

两个真实回合发生重叠，A 人工接管时 B 完成两次观察，累计 B 模型回合为 3。实际模型请求工具名及摘要包含 22 次请求：15 次在全局启用时，7 次在全局关闭时；关闭后的请求均无 Computer Schema。关闭同时验证新 Session 的能力快照，再开启复用同一容器、Runtime instance、凭据和持久卷，A 的虚构登录保留且 B 不继承。

真实浏览器报告、桌面、390px 模拟视口、成员选择、实际设置页开启及关闭记录像素、键盘／鼠标、排他租约、登录与交还。测试表单只通过 VNC 输入；只读观测接口确认远端页面加载、焦点及输入长度／匹配标志，不以固定等待猜测输入已到达，也不通过观测接口写入表单值。

清理回执确认仅本次容器及两个测试卷已停止并删除；生产关闭仍只停止并保留数据。浏览器针对夹具模型目录的 `/v1/models` 附带请求出现 403，这是既有目录夹具边界，不作为模型设置页验收；电脑链路和浏览器 JS 错误检查通过。真实手机、原生 ARM64 性能和日常服务切换均未据此宣称完成。

普通设置组件记录使用真实 Chromium 和 HTTP 夹具，单独覆盖桌面／手机布局及无地址／密钥的环境检查；它与上述实际设置页驱动真实容器的证据分开。

检查清单记录全仓 Go、866 项前端、Computer／Runtime 定向测试、race、vet 和 e2e 编译。首次全仓尝试中 Inbox 调度夹具失败一次，Room 包达默认十分钟限额；相关失败／超时案例定向复验通过，后续显式 20 分钟预算的完整全仓运行通过，Room 包耗时 533.860 秒。历史失败与修正保留在尝试记录，没有将失败日志算为通过项。

以下门槛仍分别报告：

| 门槛 | 当前范围 |
| --- | --- |
| macOS Docker Desktop | ARM64 宿主运行 amd64 镜像，实际设置、双模型／浏览器、全局停启／登录恢复已通过；按模拟执行记录 |
| Linux Docker Engine | 尚无本轮真实 Linux 宿主验收，不由 macOS 或受控适配器测试推导通过 |
| GHCR 分发 | 固定候选已推送且远端 digest 已核对；当前 private，匿名请求 HTTP 401，公开分发与冷拉取待验，见[发布记录](docker-publication.zh.md) |
| 真实模型 | 本轮双真实 DSH／模型及实际请求 Schema 对比已通过，独立入口保持显式运行 |
| 回归 | 本轮全仓、定向、race、866 项前端及 vet 通过；此前失败与超时保留原记录 |

测试期间曾修正单线程虚构站点被 Chromium 预连接占用的问题；另观察到冷启动原生 MCP 目录暂不可达及 Runtime／Docker 查询超时。目录就绪现在只重查 `tools/list`，每次探测有独立时限、整体沿用 screen 启动预算；原生 `tools/call` 仍只有一次执行尝试。失败尝试不算已通过的启停或登录恢复证明，也没有为通过测试放宽合同、执行结清或输入围栏。

## 日常手动连接切换的停止恢复修复

2026-11-24 日常配置从手动连接切换到本机托管时，新候选已就绪，但旧 Runtime 有一个 `blocked` screen。连接应用的 `stop` 步骤在 `Drain` 返回 `operation_unknown` 后直接等待，未继续执行合同要求的停止核验，导致应用无法推进。现仅对此错误继续调用原屏幕的 `Stop`；其他排空失败及停止未确认仍保持原操作等待，不重放业务调用，已停止的未知业务结果仍保留为 unknown。

修复与恢复记录包含替换／停用、停止失败、原请求重试、未知结果保留、活跃调用与回执丢失的定向回归和 race；全部 `TestComputer` 回归及 `bin/teamx` 编译通过。日常这次操作经认证的原 Runtime 停止接口核实旧屏幕停止后，由运行中的 controller 正常完成配置提交与成员 Session 切换，未手改状态文件、删除容器／卷或重启日常服务。新编译的修复随下次服务启动生效；本轮未重跑双模型／浏览器组合验收。

## 当前镜像的公开登录页兼容性限制

2026-11-24 日常测试发现阿里邮箱登录组件在当前 amd64 镜像的 Apple Silicon 模拟执行环境中无法渲染。独立对照使用相同 Chromium `146.0.7680.31`、User-Agent、启动参数、全新 profile 和资源限制，只启动浏览器、不经过 TeamX 或 AIO daemon：旧 AIO 的原生 arm64 浏览器显示完整登录表单，见截图；新镜像下载主页面、登录 iframe 和 JS 均为 HTTP 200，但 DOM 与截图查询持续超时。关闭 V8 JIT 的对照仍失败，具体浏览器构建与模拟执行原因尚未区分。测试未使用账号数据，临时容器已清理；此限制仍待修复，不将先前虚构站点的验收推导为任意公开登录页兼容。
