# DSH 内部助手操作与生命周期验证

2026-11-21，实施[上下文简化方案 S2a](../AGENT-CONTEXT-SIMPLIFICATION-ANALYSIS.zh.md#96-完成-s0s1c-后还值得继续做什么)。**状态：S2a 有界验证已交付，部分场景不兼容；不宣称全部验收通过。** 逐项保留通过、失败与未验证结果；不调整默认工具组合，也不以此宣称整个 S2 或架构 P4b 完成。

## 运行对象与接线

受测版本固定为仓库 `packaging/dsh/package.json` 的 **DSH `0.2.2-rc.1`**，模型为现有默认 `deepseek-v4.1-flash-1017`，执行器 Node `v26.7.0`。现有打包运行目录缺少 macOS 原生绑定，不能启动 ACP；另在私有临时目录按仓库 lockfile 安装完整依赖后验收，未修改原运行目录、日常配置或已运行 Session。

复用 A3 的真实调查、内部通信和 review 用例；新增 opt-in [`TestDSHCollaborationBoundariesReal`](../../internal/room/dsh_collaboration_boundary_real_test.go)。使用真实 DSH／模型、生产成员调度、工厂、宿主、CLI、HTTP、Store、输入桥与轮换。双成员 Room 中仅 A 执行，独立 reviewer 不启用。业务写入仅限隔离 Room；在途子任务由本地 HTTP gate 确认，测试此后才触发取消或关闭。

凭据从现有凭据库只读取得，在测试进程中临时设置相应模型环境变量，结束后恢复；不初始化凭据库。A3 移除了原组件获取替身，恢复也沿生产工厂选择 Open／Resume。测试未修改公开 API、生产提示词、平台合同或生产生命周期。S1d 的 URL 成本门槛不在本次范围。

原生持久记录用于核对父／子实际工具 schema、system／指南哈希、请求前收到的完整公共／DSH 指南和准确成员身份，以及实际调用归属。HTTP 回执核对成员、原 ID、状态和 Room seq；输入检查核对完整结果摘要、Received 和 settled 状态，不能用 ACP completed 或模型自述代替。

## 结果与适用范围

最终结果见脱敏证据（本地验收文件 `agent-collaboration-boundaries-evidence.json`）。原始模型／工具正文只保存在私有临时目录；公开证据保留版本、快照哈希、工具名、状态、固定测试消息与失败原因，不包含凭据或临时执行绑定。

| 场景 | 结果 | 证据边界 |
| --- | --- | --- |
| A3 内部调查、补充消息、所属成员核验交付 | PASS | 同一打包版本重新运行，不沿用本地旧版本结论 |
| A3 内部 review 与独立 reviewer 归属 | PASS | 复算实际 4／期望 3，独立 reviewer 不执行 |
| 子 Agent 直接 TeamX CLI 只读 | PASS | 当前成员 `policy read`，不把父 Agent 的读取算给子 Agent |
| 子 Agent 本地 Room 发布与跨成员拒绝 | PASS | 原 ID／正文、成员 A、reviewer 探测 403，实际调用来自子 Session |
| 子 Agent 直接 History MCP | FAIL | 原生子 Session 缺少对应 schema；父 Session 有该工具。没有用父读取替代子读取的证据 |
| 子任务完成后正常关闭／原 Session 恢复 | PASS | generation 与底层 SessionID 均应保持，保留原 Room 消息 |
| 子任务在途时取消／关闭的正常结清 | FAIL | 首轮与最终有效的取消／关闭样本均报告 `cancelled DSH tools did not stop within settlement budget`；不将强制退出称为正常关闭 |
| 旧执行退出屏障及受控轮换恢复 | PASS | 取消、关闭两条路径均确认 gate 断开、CheckReset 成立后，按原机制完成轮换；原 Session 的 Resume 仍被拒绝 |
| 恢复／轮换后的原回执、输入证明与无重放 | PASS | 核对旧 seq／ID、实际 History 调用和正式输入证明；原 send 总尝试数不得增加 |
| fork／workflow 实际执行、并发 Workspace／远端动作、图片、自动轮换、多模型 | 未验证 | schema 可见不代表行为通过；不扩成完整 S2／P4b 矩阵 |

最终有效样本共 8 个 TeamX 业务回合（A3、直接操作／正常恢复、在途取消、在途关闭各 2 回合）；不以此推算统计可靠性。A3 和直接操作／正常恢复用例通过；取消、关闭用例保留非零失败结果，虽然退出屏障和轮换后的恢复检查均通过。直接操作和两条生命周期样本的固定原消息均只有一次成功 send；拒绝性探测另有一次 403。轮换恢复后仍是原 seq／ID，未新增 send 尝试。

失败样本没有删除：现有运行目录缺原生依赖；旧组件替身的空接口掩盖了启动错误；A3 的“不能代表 reviewer”被旧措辞断言误判；最初要求子 Agent 读取 History 但实际无该工具；轮换测试 ID 不符合原合同；一次关闭样本由父 Agent 执行了阻塞步骤。测试接线、措辞断言和非法轮换 ID 已修正并重跑；当前直接只读路径单独验证 CLI policy，不冒充子 Agent History 已通过。错误执行者样本没有算作子任务在途证据，明确要求同一子 Agent 完成发布和阻塞后单独复验关闭，仍复现结清失败。具体样本路径、原因与最终证据均保留。

## 失败与后续修复单

1. **子 Agent 的 History／Inbox MCP 可达性。** 当前只读 CLI 和本地发布应分别判定；不能默认把父 Session 的四个 MCP 工具交给子 Agent。若要支持直接子 Agent History／正式输入，先核对锁定包 `dsh-subagent` 的 `applyChildComposition` 与 `dsh-mcp-client` 的注册作用域，再沿 TeamX generation／turn 绑定补齐，再验证实际输出与持久接收证明；不能复制凭据或建议绕开成员绑定。
2. **在途子任务的取消传播与正常关闭。** 定位入口为锁定包 `dsh-acp.cancel`、`dsh-subagent` 的 `interrupt/drain`，以及 TeamX [`process.waitToolsStopped`](../../internal/agent/dsh/process.go)；核对子 Agent／shell job 是否接收父回合取消、清理是否纳入终止／关闭屏障。当前轨迹尚未将根因归到其中某一处，不能仅扩大超时或放松该屏障。复验要求是 gate 真实在途、子任务退出、终态和保存证明一致，再决定是否允许正常 Resume。失败后仍须保留旧 Session 不可复用的门槛；有退出证明才可受控轮换，不能重放旧 prompt 或发送。

这两项只限制上述直接 MCP 和在途生命周期场景，不默认禁用全部内部调查、review、CLI 或 Room 协作能力。具体修复未夹带在本轮测试中。版本／原生轨迹可供[架构 P4b](../GORDIS-PLUGIN-AND-SELECTIVE-BUILD-PLAN.zh.md#95-p4ap4bdsh-工厂与后端边界)复用；能力归属集中、图片、模型应用、自动轮换等仍独立待验。

## 复现与检查

在临时运行目录复制仓库 `packaging/dsh/package.json` 与 `package-lock.json`，用 `npm ci --workspaces=false --include=optional --no-audit --no-fund` 安装；使用符合依赖要求的 Node 启动。下列测试需各自全新、尚不存在的输出目录，且系统有 `zstd`：

```sh
TEAMX_COLLAB_DSH_ENTRY=/absolute/runtime/node_modules/@deepseek-ai/dsh/lib/bin.js \
TEAMX_COLLAB_DSH_VERSION=0.2.2-rc.1 \
TEAMX_INPUT_COMPARISON_CONFIG=/absolute/config.toml \
TEAMX_DSH_OUTPUT=/absolute/fresh-output \
TEAMX_COLLAB_BOUNDARIES_REAL=1 \
go test -tags=e2e ./internal/room \
  -run '^TestDSHCollaborationBoundariesReal$' -count=1 -v -timeout=22m
```

A3 使用同样的版本／配置参数，改为 `TEAMX_INTERNAL_COLLAB_REAL=1` 和 `-run '^TestDSHInternalCollaborationReal$'`。取消／关闭分支保留真实失败，直到对应修复通过复验；没有把 opt-in 失败改成 skip。普通编译不启动模型。定向输入证明／恢复／凭据撤销／最终指南的 integration＋race、agent／DSH 组件包 race，以及 e2e 无 opt-in 的编译／观察器检查均通过，`git diff --check` 通过。首次回归遇到并行工作树中的编译错误，后续原工作树已通过；未修改那部分文件，也不宣称全仓验收。

本次未切换日常服务、轮换现有用户 Session、进行真实平台业务写入、提交或 push。
