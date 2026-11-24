# 默认空间与正式身份（S1a）

日期：2026-11-24。方案：[多空间与多用户方案 §11.3](../WORKSPACE-AND-MULTI-USER-ANALYSIS-PLAN.zh.md#113-第一批实施单默认空间与最小组合)。源码起点：`479e79f35157cf15b8f60c793a7840b59d07647c`；开工时已有两份文档修改和多份未跟踪文件，保留原内容。本次范围仅为 S1a。

## 实现与责任

- 控制面自动登记唯一 `User` 和默认 `SpaceRecord`，保留 `access.operator_id` 作为既有 UserID。`users.json`、`spaces.json` 及 `personal-space-registration.json` 保存于配置 home。既有 data、模型、teams、Computer 状态／凭据继续使用原位置；显式单 Room 登记实际 `--state-dir`。登记不遍历或重写 Room、成员、消息、Session、平台授权与通知游标。
- 登记先持久化包含稳定身份和资源根的 reservation，再发布两个目录，最后标记完成；使用现有快照 writer 和短期 `.identity.lock`。中断后只继续原 reservation，已有但不一致／损坏的文件不覆盖。完成后缺目录、登记、访问配置或安装身份，或 owner／资源根漂移时拒绝启动，要求从备份核实修复；不生成替代身份来掩盖损坏。登记不提供第二用户／空间写入口。
- 原访问策略继续决定是否需要登录。认证产生 `userSubject`，统一核验用户状态、认证版本、默认 Space owner／状态，再产生请求的 `SpaceContext`。`Caller` 只匹配该主体。`requestOperator` 不再回退 OS 用户；原 `serverAccess.operator` 字段已移除。OS 用户名只用于展示和提及匹配。无 access 边界的内部组件装配必须显式给出 owner，测试在测试代码中注入自己的主体。
- 当前 manager 继续唯一持有业务根、Room、后台采集、定时任务、恢复和关闭责任。正式管理、单 Room、setup、HTTP／SSE、Computer 观看／接管均经过同一默认空间边界；实际服务拒绝绕过访问包装直接调用 handler。已读状态、后台及 DSH 指引使用装配的 owner。
- CLI 执行令牌、MCP Session／输入令牌均固定 User／认证版本、Space 和运行代际，保留既有 Room／成员／Session generation／turn 验证。`/mcp` 仍从令牌取得空间；错误作用域在读取业务对象前拒绝。后台不能借用浏览器当前选择。
- 浏览器会话升级为 v2，每条会话保存 UserID、认证版本、原到期时间及 token 摘要。v1 只在原安装／owner／凭据绑定匹配时接入已登记用户，并先持久化升级；原撤销、到期及重启失效规则保留。Cookie 不保存空间选择。

ComputerID 继续由原 installation＋owner 派生；本次仅将原资源登记为默认空间所有，不改其 ID、成员映射、profile、指纹或执行记录。新空间的独立 Computer 分配属于 S2 及后续。

## 入口合同

`GET /v1/access/session` 增加 `user_id`、`auth_version`、`space_id`；兼容 `operator` 始终等于该 UserID。`GET /v1/spaces` 只返回已登记的默认空间。业务 handler 可经 `/v1/spaces/{space_id}/...` 访问，例如 `/setup`、`/rooms`、`/workspace/...`；不存在或非 owner 空间均拒绝，不读取目标对象。UI bootstrap 和 SSE hello 增加 `space_id`。

现有 UI／人类 CLI 的 `/v1/...` 入口在本阶段固定映射到已登记默认空间；Agent 依令牌显式绑定。不维护全局 currentSpace，不开放切换。S2a 接入多空间 UI／CLI 时收敛这些兼容入口；不能把默认映射扩成任意空间兜底。

## 验证

普通测试新增或覆盖：

- 无账号操作的新安装、无模型 setup、刷新／重启、原登录开启及关闭；User／Space ID 不变。
- 用户／空间／完成记录发布前后中断、未知提交后重试；缺失／损坏／symlink／非私有目录拒绝，第二用户登记拒绝，旧资源字节保持。
- 合成非 owner、空主体、旧认证版本、停用用户、错误 Space、伪造 caller／声明身份、旧运行代际及错误 CLI／MCP 令牌拒绝；缺 owner 的组件在打开数据前失败。
- manager 和单 Room 真实 handler 的授权、SSE 身份与退出撤销、已有模型 setup 转业务、默认助理／任务、平台与 Computer 回归。
- 先建立 Computer 控制器、再登记默认空间并重启，原 Computer owner／ID 保持；未启用时不需要 Docker。

已通过：

- 定向 race：正式主体／错误 Space／MCP 提前拒绝、SSE 读与重连、浏览器登录重启。
- `go test -tags integration ./internal/room -run 'TestServerCancellationReapsACPAndRestartsSameHome|TestHumanReadHTTPEntryMetadataAndDeltaValidation|TestNormalServerUpgradesRoomBeforeReady' -count=1`：实际受管／显式单 Room 服务入口、离线 ACP 子进程取消与重启、已读 HTTP 合同。
- Chromium：`TestPersonalSpaceIdentitySetupBrowser`、`TestDirectConversationBrowser`、`TestDefaultAssistantRestorationBrowser`。前者使用真正自动初始化的新 home，确认无需登录即可进入原模型 setup、刷新身份稳定、错误 Space 拒绝；后两者使用受控业务后端，覆盖默认助理、设置及恢复。
- `go test -tags e2e ./... -run '^$'`：全部验收代码编译通过，无真实模型／平台调用。
- `git diff --check`。

`go test -json ./...` 完整通过：32 个含测试包通过，无失败；同轮包含新增身份／登记、访问、Session、通知、日历、默认助理、工作目录及 Computer 普通回归。最后补充的访问配置缺失保护和 SSE `space_id` 断言另行定向通过。

定向 race 的命令为 `go test -race ./internal/room -run 'TestSpaceAuthorization|TestMCPRejectsForeignSpace|TestUIUpdatesHTTPMessageReadAndReconnect|TestAccessSessionsSurviveRestart' -count=1`。

未声明完整 integration 全量通过。额外在开工 HEAD 的独立源码副本核对 `TestCLIFixtureWorkflow`，其原本即失败于 `cli_integration_test.go:52`（readiness 输出解析错误）；本次没有扩展去修复这条旧测试进程启动路径。CLI／Agent 的本轮证据来自普通回归中的真实 HTTP／CLI 日历与输入调用，以及上述带认证的实际服务器／ACP 集成。

旧的直接 handler／裸组件夹具改为由测试工厂显式提供 owner／请求主体。没有给生产代码增加免认证测试开关；浏览器脚本的旧“启用协作”断言同步为当前已有的“启用”文案。

## 停止点

仅交付 S1a。没有提取 `SpaceRuntime` 或部署组合，没有引入 Gordis、第二 manager、第二用户／空间、归档／删除或执行隔离。S1b 继续复用这份身份登记和原资源根，提取实际装配与生命周期接点；S1 整体尚未完成。

本轮后端恢复使用真实 ACP 子进程及离线模型 fixture；浏览器使用真实 Chromium。没有对日常数据、运行中的 Session 或真实平台进行升级试验／写操作，也没有新跑真实模型、平台凭据生命周期或 Linux AIO 容器验收。这些检查不替代 S2 的双空间隔离及 S3–S5 的托管验收。
