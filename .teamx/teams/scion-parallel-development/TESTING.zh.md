# Scion 并行编程操作步骤

本文供你在 TeamX 房间中逐步操作，包含环境配置、可复制的消息、交付检查和中断恢复。团队概要与角色说明见 [README](README.md)。

## 操作前确认

第一次手动测试推荐新建一个 Room，保留已有验收房间及其交付。已有 Scion 平台和默认模型可直接复用，不需要再次登录或申请多个账号；首次配置见下文。

房间、模型和平台配置应在同一个 TeamX 服务中完成。若同时运行安装版和项目版，使用不同端口，并核对当前页面连接的服务；团队列表中没有「Scion 并行开发」时，先按下文核对该服务实际加载的模板目录。

## 1. 创建房间并配置四个成员

1. 在会话列表点击「＋ → 新建房间」，选择 **Scion 并行开发**，点击「创建并进入」。确认有 architect、developer-a、developer-b、developer-c 四个成员。不要选择原「软件开发」团队；已有串行 Room 不能在运行中切换为并发。
2. 打开每个成员的「平台 → 添加平台」，选择已有、已验证为 `csgship` 的 Scion 平台，勾选「使用工具」并「添加并启用」。四个人都要配置；不需要通知订阅。已经绑定且工具已启用时直接复用。
3. 打开输入框下方的 **Workspace**，选择以下内容并应用，等待初始化完成：

   | 字段 | 本例填写 |
   | --- | --- |
   | 仓库类型 | Scion |
   | 仓库 URL | `https://scion.opencsg.com/OpenCSG/teamx-demo` |
   | 初始分支 | `main` |
   | 平台 | 上一步的已有 Scion 平台 |

   Workspace 初始化会给每个成员准备独立副本。它与成员工具绑定分别配置，两项都要完成。
4. 检查四个成员的协作状态：显示「启用协作」时点击一次；已经启用或正在运行时无需重复点击。确认四个成员都有可用模型。

## 2. 先发送一次只读核验

在 Room 直接发送下面这条消息。工具入口、Room ID 和 membership ID 由各成员从自己的运行环境取得，你不需要手工填写。

```text
本轮准备测试 Scion 并行编码。本条只核验环境，不编码、不提交、不创建或发布分支。

请 architect、developer-a、developer-b、developer-c 分别通过自己已启用的 Scion 工具，核对：
1. 实例 https://scion.opencsg.com，实际账号 csgship，仓库 OpenCSG/teamx-demo。
2. 自己的独立 Workspace、当前分支 main、完整基线 head，以及 dirty、pending、冲突状态。
3. 测试入口 python3 可用。
每人只回复一次实际核验结果；账号、仓库或基线不一致，或有未处理改动时先报告阻塞，不擅自覆盖。
```

等四个成员都确认账号与仓库正确、基线一致、工作副本可用，再发送编码需求。若有成员未响应，先看其协作状态、模型和平台绑定，不反复发送同一条消息。

## 3. 发送编码需求与任务分支授权

本节是按模块分工的版本。若要让多名 Agent 修改同一文件，改用[脚本准备 base 的独立实验](OVERLAPPING-FILES.zh.md)，发送它生成的提示词；两种场景择一运行。

下面是可直接发送的示例，沿用刚才的核验结果。它会真实发布到 `OpenCSG/teamx-demo` 的新任务分支。使用其他仓库时，先统一修改 Workspace、核验消息和需求中的仓库信息。

```text
开始本轮并行编码测试，轮次标识 LEDGER1。

实例：https://scion.opencsg.com
仓库：OpenCSG/teamx-demo
基线：采用刚才四个成员在 Room 中核验一致的 main 完整 head；若基线不一致或 main 已变化，先停下说明，不自行换基线。

允许发布的分支限定为 teamx/<当前完整 Room ID>/<本人完整 membership ID>/<任务标识>：
architect 只用 LEDGER1-DESIGN；developer-a 只用 LEDGER1-T1 和 LEDGER1-INTEGRATION；developer-b 只用 LEDGER1-T2；developer-c 只用 LEDGER1-T3。
使用各自已绑定的 csgship 平台，明确各自的提交作者，只提交任务文件。不要直接发布 main，不创建或评论 Review，不 approve、合入、关闭、部署或删除远端分支。

需求：用 Python 标准库实现记账摘要，代码放在 parallel_ledger/，无需 __init__.py。
- T1 / developer-a：amount.py 的 parse_amount(text) 把十进制金额字符串转为整数分，支持首尾空白、正负号及最多两位小数，拒绝空值、过多小数位、NaN、Inf；编写 test_amount.py。
- T2 / developer-b：summary.py 的 summarize(entries) 接收 (分类名称, 整数分) 条目，逐分类累加，空输入返回 {}，保留零合计分类；编写 test_summary.py。
- T3 / developer-c：report.py 的 render_report(totals) 返回 JSON，结构为 {"categories":[{"category":"分类","cents":100}],"total_cents":100}；分类按名称排序，支持中文，不修改输入；编写 test_report.py。

architect 先核实需求、发布 design.md 和带版本的任务表，明确接口、文件范围及完成标准；三项任务之间无开发依赖，开发者取得准确设计版本后可以并行开始，不逐个等待同伴完成。

三项完整交付后，由 developer-a 从本轮基线建立独立集成分支，按准确发布 head 从远端取得最终设计、三个模块及对应测试，原样纳入集成分支；新增 parallel_ledger/test_integration.py 和 parallel_ledger/README.md。一并提交所有集成所需文件，使远端集成分支可以独立测试。
整体验收必须串联 parse_amount → summarize → render_report，覆盖正常、负数、零、中文、空数据、非法金额拒绝及输入不变。运行 python3 -m unittest discover -s parallel_ledger -p 'test_*.py' -v，发布后回读远端内容和完整 head。

在 Room 交付设计、三项任务和集成分支的准确版本、实际测试结果及可点击的代码／提交链接。architect 最后核对需求覆盖。只报告实际交付、变化或阻塞，无任务时等待。
```

## 4. 观察协作并查看交付

- architect 先交接设计，三名 developer 随后各做自己的模块；无须你分别发三遍需求或逐个批准开工。
- 在成员详情的「活动记录」查看当前回合、工具调用及测试输出。多名成员同时运行是观察入口；`active` 也包含 Session 打开与等待，不能单凭状态或先后出现的完成消息断言同时编码。
- Room 应收齐三个 `[任务完成]`、developer-a 的集成交付，以及 architect 的需求覆盖结论。核对每项的分支、完整 head 和测试结果。
- 打开集成版本的代码／提交链接查看远端结果。仓库首页默认显示 `main`，本流程不会合入它；应切换到交付的集成任务分支或打开准确 head 的链接。

若要严格证明并发，须核对至少两名 developer 的真实编码回合／工具时间区间重叠，并独立读回远端版本与重跑集成测试。已完成的一轮实例、时间证据和核验方法见[共享账号真实并发验收记录](../../../docs/implementation/scion-parallel-acceptance.zh.md)。其中的账本证据提取工具针对该示例和固定 DSH journal 格式，不是任意项目的通用页面按钮。

## 5. 中断恢复与下一轮

成员因模型限流、账号或工具问题暂停时，先查看原因并修复，再点击该成员的「启用协作」继续；保留原 Room、工作副本、分支和已有交付。发布结果未知时先读回远端，不重发整条需求。

复用已有房间时，先核对各成员当前分支、已发布版本和未提交修改；回访不会自动把所有副本切回 `main`。下一轮使用新的轮次标识及任务分支，不覆盖之前的交付，也不要重新发送本例的 `LEDGER1` 请求。需要重新开始独立测试时，新建 Room 并重新绑定成员工具、初始化 Workspace 即可；全局平台与模型继续复用。

## 首次配置与环境细节

按[团队配置说明](../README.md)配置 DSH 和默认模型，安装 scn 0.1.13，并按 [Scion 使用说明](../../../docs/scion.zh.md)配置服务可用的程序路径与已有账号。账号应具备测试仓库的读取、任务分支创建及发布权限；只核对可见的角色／权限信息，不试写未知目标。服务不自动读取 `.env`；凭据不进入模板、Room 消息或成员环境。

服务从实际 `server.teams_dir` 加载模板。项目内通常为 `.teamx/teams/`，安装环境通常为配置旁的 `teams/`；不要编辑 `teamx-space/share/...` 等生成副本。模板更新后重启服务，再新建房间。`scheduling: parallel` 和角色在创建时冻结，已有 Room 不随模板变化；无需为阅读本文重启正在使用的房间。

四个成员各自绑定工具，Workspace 也按 membership 隔离，详见 [Workspace](../../../docs/workspace.zh.md)。共享平台账号不会合并成员的 Session、工作目录或 `.scion` 状态，也不代表多 subject 身份隔离。

下面是成员在自己运行环境中的工具发现和只读核验入口（`<tool-entry-id>` 取自列表）。在浏览器测试时发送上面的核验消息即可，无需你在普通终端手动设置成员执行环境：

```sh
"$TEAMX_CLI" tools list
"$TEAMX_CLI" tools skill <tool-entry-id>
"$TEAMX_CLI" tools run <tool-entry-id> --json whoami <instance-url>
"$TEAMX_CLI" tools run <tool-entry-id> --json repo info <canonical-repo>
"$TEAMX_CLI" tools run <tool-entry-id> --json status
```

只准备环境时到此即可；要真实编码，再按上面的房间步骤发送核验和需求。平台或模型不可用时，分别记录模板可用与实际环境待验，不能以全局 `whoami` 代替各成员核验。

## 角色与交付约定

实际写入前明确实例、canonical repo、目标分支、完整基线以及允许发布的 `teamx/<room>/<membership>/<task>` 分支范围。architect 发布带版本的设计／任务表，列出任务 ID、唯一负责人、文件范围、接口、依赖及完成标准，并安排 developer-a 的最终集成任务。设计通过已发布文档的完整原生 ID 交接；平台暂不可用时，在 Room 提供带版本的完整必要设计，不只给本地路径或摘要。

开发者只做自己获配的任务，独立任务并行，依赖任务取得前置任务的准确发布版本后开始。没有任务时保持沉默，不轮流确认、重复任务表或催促同伴。每个成员在自己的任务分支明确 `--author`，如 `TeamX developer-b <developer-b@teamx.local>`，只 commit 指定文件，再用 `scn land` 发布；重取实际完整 head，发生重写时核对内容并按需补测。作者署名不改变实际认证账号 `csgship`。

交付格式：

```text
[任务完成] T2 / developer-b
实例／仓库：<instance-url> / <canonical-repo>
分支：teamx/<room>/<membership>/T2
设计版本：<文档完整原生 ID 或 Room 消息版本>
基线／发布版本：<完整 base> / <发布后实际完整 Scion head>
完成：……
验证：……
依赖／未验项：……
```

全部开发任务交付后，developer-a 在独立集成任务分支取得各准确版本、组合代码、运行整体验收，发布并回读集成版本。architect 核对需求覆盖，业务取舍交用户。集成不授予目标分支合入权限；`scn land` 发布任务分支，`scn review land` 合入 Review，本流程不自动 approve、合入或关闭，也不要求 Review、评论或 Scion 通知回流。

## 正式验收边界

模板可加载并创建 `parallel` Room，以及共享平台绑定、四个正常工具入口身份、独立工作副本与完整基线，分别记录。真实编码按[并行开发方案阶段 3](../../../docs/ROOM-SCHEDULING-AND-SCION-PARALLEL-DEVELOPMENT-PLAN.zh.md#73-阶段-3共享账号真实并发验收)核验：以回合／工具起止证明至少两个开发成员的执行区间重叠；声称三个同时编码时须证明三者共同重叠。核对远端分支、内容、完整 head、作者署名和实际共享账号，收齐 Room 交付及最终集成结果。组件、真实 DSH、真实 Scion 与浏览器证据分别报告，不能以先后完成消息替代并发证据。一次通过的账本需求不保证以后每轮都产生相同长度的重叠，应以当轮证据为准。
