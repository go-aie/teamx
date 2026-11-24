# 默认空间运行时与个人部署装配（S1b）

日期：2026-11-24。方案：[多空间与多用户方案 §11.3](../WORKSPACE-AND-MULTI-USER-ANALYSIS-PLAN.zh.md#113-第一批实施单默认空间与最小组合)。源码 HEAD：`479e79f35157cf15b8f60c793a7840b59d07647c`；开工工作树包含已完成但未提交的 S1a、两份方案和其他资料。本次承接 S1a，只实施 S1b；不提交或覆盖其他工作。

## 实现与归属

- 启动层使用 `personalDeployment` 和普通 Go 函数工厂，先校验 service、manager、模型、模型控制器、平台客户端、Computer、setup 与迁移处理的必需绑定。选择及校验不读取凭据、不打开业务目录、不监听、不启动子进程或后台任务；缺绑定在任何初始化前失败。`boot.Run` 传入的已选平台目录继续透传，不扩大扩展选择。当前仅选择个人组合，没有提前开放多用户模式开关。
- `SpaceRuntime` 直接拥有原 `roomManager`，提供单次启动、请求、状态和有界关闭接点；显式单 Room 使用同一运行时及组合。setup、模型迁移阻塞、正常业务、后台启动与恢复均从同一运行时入口分派。删除原固定 `serveRoomManager` 构造入口，模型与 Computer 构造改由所选工厂执行；保留原 setup／迁移处理器自己的数据锁及 worker 结清责任。
- 配置文件及业务数据不搬迁。构造运行时先核对 S1a 登记的 User／Space owner、状态、epoch 及 Home／Data／Teams 根；不匹配时拒绝接入。空间使用的启动配置去除监听、代理、远程入口、安装身份及访问配置；访问策略和凭据仍由控制面 `serverAccess` 管理。现有单文件配置作为持久化载体保留，业务设置与控制面设置复用同一个原子配置文档，不建立竞争 writer。
- 模型目录、Key、验证及 pending 操作沿登记 Home；连接及通知沿 Data；自定义模板沿 Teams；Computer 沿原 Home、installation＋owner 及既有 ComputerID。身份及目录均不重算。每个实际 manager 获得独立的飞书请求队列及 profile gate，保持原生产节流参数；这不代表已经实现第二空间的 CLI 凭据隔离。
- `[env]` 解析为对应空间的显式子进程环境，保留“已导出变量（包括空值）优先”的旧规则；不再通过 `os.Setenv` 安装配置环境。DSH／Agent Launch、模型验证和平台工具接收该环境；DSH 的管理凭据过滤、工具环境白名单及所选连接凭据覆盖仍生效。环境与 Key 不进入 Session 持久配置或恢复指纹。发行包的 PATH 设置仍属于部署级启动。

## 唯一关闭权威

原 `roomManager.shutdown` 继续负责冻结、取消、锁外并行等待、通知 Stop／WaitRoom、业务回执、日历及 Store。`SpaceRuntime` 不复制 Room／通知结清状态，不在已停止的对象上 Start。

模型控制器的取消与 worker 等待并入原关闭协调和共同 deadline，移除关闭前无界等待的固定入口。运行时显式登记其拥有的 Computer 控制器：manager 在 Room／操作结清后、释放 Store 前关闭它；单 Room 在成员结清后、释放 Store 前关闭它。不能从 settings 的引用推断关闭所有权。Computer 的等待、host、writer 与锁按同一 context 关闭，可重试且幂等。

关闭超时保留原 manager、Computer、操作证据、数据根锁及部署 home 锁；组合保留原运行时引用，重试继续作用于原对象。只有原协调报告结清才释放 home。重新运行创建新 manager／通知对象与运行代际，原用户、空间、Room、成员与 Session 恢复合同保留。

## 验证

使用临时目录及受控来源执行：

- 必需工厂逐项缺失：在创建 home／访问凭据／监听之前失败。运行时拒绝未登记的资源根，不创建替代目录。
- 初始化失败清理、停止对象拒绝重启；实际 manager 的未结清操作导致关闭超时，保留原对象及 Computer／数据／home 锁；操作结清后原对象重试关闭成功，同根可创建全新 manager／通知 Manager。
- 定向 race：`TestDeploymentSelection`、`TestSpaceRuntime`、`TestAccessBrowserModelSetupStartsNormalServer`、`TestComputerPendingRecoveryDoesNotEraseLocalResumeEligibility`。
- 实际 ACP 子进程集成：`TestServerCancellationReapsACPAndRestartsSameHome`、`TestNormalServerUpgradesRoomBeforeReady` 及 `TestRuntimeShutdown`。覆盖原目录重启、正常关闭、进程退出及原 manager 的超时保留协议；模型为离线 fixture。
- 入口与恢复定向检查：setup／home／data 锁、启动失败、模型修复、发行切换；GitLab／飞书通知重启、连接 receipt 恢复；模型默认／显式选择和 pending／commit-unknown 重启；Computer pending、关闭回执、禁用及本机托管操作恢复。
- Chromium：`TestPersonalSpaceIdentitySetupBrowser`、`TestSpaceRuntimeBusinessBrowser`。后者经过实际组合、SpaceRuntime、manager、默认助理、访问包装及页面资源，验证默认空间页面、scoped API、错误空间拒绝和刷新；业务执行使用受控后端。浏览器断言无页面异常及非导航取消的请求失败。
- DSH、平台工具、Agent／后端工厂及 boot 包回归，包含显式环境的真实子进程启动、凭据过滤、依赖定位与持久文件不含环境秘密。
- `go test -tags e2e ./... -run '^$'`：全仓验收代码编译，不执行真实模型／平台。
- `git diff --check`。

真实后端：`TestSpaceRuntimeInstalledDSHRecovery` 通过。读取明确指定的历史验收 home 的模型描述和 Key，在新的 `.local/space-runtime-real-20261124-04/home` 初始化独立模型目录与凭据、执行真实模型验证，从本轮个人组合启动实际 manager／默认助理，完成两次对话及正常停服／重启。原 User、Space、Room、成员和 DSH SessionID 保持，空间 runtime epoch 更新。实际后端为本机 Node＋DSH `0.2.1-alpha.1`，模型 `deepseek-v4.1-flash-1017`；来源不代表其他打包版本已通过。为使验收进程调用正确 CLI，仅将测试程序的 `os.Executable()` 绑定替换为本轮编译的正常 TeamX CLI，其余仍走默认服务工厂。报告见脱敏证据（本地验收文件 `space-runtime-s1b-evidence.json`）。

真实验收重启重用同一监听地址，保留已有 MCP endpoint／Session 配置指纹合同；随机端口变化导致的自动轮换不作为原 Session 恢复证据。早期夹具的 CLI 绑定、后端文件读取及端口问题已修正，只有 `-04` 的完整结果计为通过。没有新跑外部平台授权生命周期或 Linux AIO／Docker 容器验收；这些资源的身份与本机托管恢复沿用有效 S1a／Computer 证据，本轮补相应组件回归。全部检查按实际影响选择，没有重跑整个 Room 包或全仓测试。

## 停止点

本次只交付 S1b；与 S1a 合并构成 S1 及个人最小组合。下一单元为 S2a。未开放第二空间／用户、空间切换／归档、Gordis 或强执行隔离；不把本轮环境绑定和原根接入表述为多空间／多用户隔离验收。
