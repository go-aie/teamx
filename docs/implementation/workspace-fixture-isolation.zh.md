# Workspace 夹具环境隔离与四槽并行

2026-11-24，实施测试改造方案 §11.6 的 F4。只修改测试夹具、调用方和实施记录，生产 Git 环境、授权、持久同步与进程结清路径不变。

## 环境所有权与覆盖

`newWorkspacePreflightFixture` 不再修改进程级 `HOME`／`XDG_CONFIG_HOME`。每个 fixture 拥有私有 home、Git 全局配置、空模板及 hooks 目录；禁用系统 Git 配置。`workspaceGit` 改为 fixture 的 `git` 方法，源仓库初始化／查询显式使用同一环境。`f.run` 为每次命令创建独立环境 map，保留实际 preparer 的凭据和选项，同时固定私有 home 与 Git 配置，再调用生产 `runPreparation`。

本地 fetch／push／精确 SHA 回归也显式合并私有环境；manager 的 `workspaceRun`、共享 Workspace helper 和重启后的 `restored.workspaceRun` 均继续使用同一 `f.run`，不依赖进程环境副作用。没有共享运行中的 Store、manager、源仓库或后台 worker，没有引入只读种子缓存。

| 原覆盖 | 本轮承接／变化 | 保留边界 |
| --- | --- | --- |
| 源仓库 Git 初始化、查询及实际准备子进程 | fixture 私有环境贯穿两条命令路径 | 真实 Git init／commit／clone／config，独立成员 clone、用户文件／分支／作者保留、回访不访问远端 |
| 两个 Workspace 与宿主 Git 配置隔离 | 新增 `TestWorkspaceFixtureIsolatesGitHomesAndHostConfiguration` | 注入冲突的 HOME、XDG、全局／系统配置、inline 配置、GIT_DIR、helper、hooks 和模板；两个私有 home 并发准备，真实 credential helper 分别返回各自身份，宿主配置字节与进程环境不变，宿主 hook／helper 未执行 |
| Workspace apply、失败／unknown 提交、重启和精确身份 | 原测试体与真实 Store／manager 保留 | 原目标、版本、旧文件、符号链接目标重核验、跨文件中断、关闭重开及不自动重放 |
| 外部目录、共享目录与 Git／Scion 区别 | 原真实目录与 Scion 协议测试保留 | 外部用户文件、共享／父子目录准入、Session 代际、模型快照与本地回访；未将 Scion 替成 Git |
| 准备进程取消、失败及持有权 | 原真实进程测试保留 | 有界输出、父进程退出／超时后的子进程清理、starting／running 中断、拒绝未结清重试与删除；结清后重试成功，manager 关闭后目录锁可重开 |
| 共享凭据轮换测试中的既有竞态 | 先等取消，再有界等待实际结清门闩 | 等待期间持续核对凭据未提交；请求不得提前返回，旧 worker 释放后才允许新版本提交 |

35 项原先串行的私有测试接入现有四槽，范围为 Workspace 准备／管理、opaque backend、外部目录及相关 Git／OAuth 调用方。`testWorkspaceApplyJoinsRunningSessions` 已在 helper 内持槽，入口不重复持槽。没有修改槽数。

仍修改进程 HOME 的目录默认策略／浏览检查、新增宿主环境注入检查、Scion ambient-secret 拒绝检查以及会替换 PATH 的 OAuth managed-Git 检查保持串行；使用显式外部挂载路径的验收也保持串行。其他原有浏览器／真实平台／模型验收不扩大执行范围。

## 验证与计时

环境：macOS ARM64、Go 1.27.1，HEAD `479e79f35157cf15b8f60c793a7840b59d07647c` 加既有工作树。按当前文件及夹具调用关系提取完整相关族：原有 93 项顶层，改后增加 1 项隔离检查。挂载路径验收在本机均按原条件跳过；没有新增 skip。

计时采用预编译的 integration 测试二进制，从 `internal/room` 执行，使用 `-test.count=1 -test.timeout=5m`。墙钟包含进程启动、执行和清理，不包含 Room 测试二进制编译。有效对照期间没有并跑构建、race 或其他检查。改前通过 Go overlay 恢复 F4 前测试源文件，只保留与改后相同的结清门闩同步修正；其余生产源码与既有工作树保持相同。

| 检查 | 顶层结果 | 含清理墙钟 |
| --- | --- | ---: |
| 改前完整相关族，Go parallel=4 | 92 通过、1 原有跳过 | 77.845 秒 |
| 改后完整相关族，Go parallel=4，含新增隔离检查 | 93 通过、1 原有跳过 | 60.011 秒 |
| 改后同范围单槽语义，Go parallel=1 | 93 通过、1 原有跳过 | 91.504 秒 |
| 改后同范围 race，Go 默认并发、fixture 仍为四槽 | 93 通过、1 原有跳过 | 65.507 秒 |

本轮单对样本净减少 **17.834 秒（约 22.9%）**。这是这组测试的局部结果，不是稳定提速率或全仓预算证明；保留现有 35 项并行范围，不继续扩散或增加槽数。

原始改前 79.569 秒和仅隔离环境后的单槽 90.678 秒均因旧查询取消与结清门闩之间的竞态失败，不作为健康对照。第一次并行尝试 57.773 秒因遗漏 OAuth managed-Git 的 `t.Setenv("PATH", …)` 而 panic；已将该项恢复串行，并中止其旧二进制的单槽后续检查。上述未完成样本不计入收益，修正后重新跑完本表范围。停止后未发现遗留的本轮测试／sleep 进程。

两个环境／结清检查已连续 3 次通过。默认、integration、e2e 的 Room 编译及 integration 标签的 Room `go vet` 已通过。同范围定向 race 也已通过，没有竞争报告。

结果、选择范围与源码摘要见[机器证据](workspace-fixture-isolation-evidence.json)，临时原始日志位于 `/tmp/teamx-f4/`；临时目录不是永久 gate。全仓 integration、全仓 race／vet、Web 及其余统一预算验收归 F5。真实平台、模型、浏览器与外部挂载未在本轮执行。
