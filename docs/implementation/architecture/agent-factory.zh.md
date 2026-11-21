# DSH 工厂与跨包宿主合同

日期：2026-11-21。对应[第二阶段方案 P4a](../../GORDIS-PLUGIN-AND-SELECTIVE-BUILD-PLAN.zh.md#95-p4ap4bdsh-工厂与后端边界)，P4a 已完成。实施基准为 HEAD `bd299103af8379a9cbab17a58cc8b9f4ae462b9d` 加既有工作树；保留正在进行的测试／文档修改。P4b 的能力归属整理与真实 DSH／模型对照另行实施。

## 实施与责任

| 位置 | 实际职责 |
| --- | --- |
| `agent` | 工厂、Session 宿主、引用检查点、输入快照与必要执行值；公共合同的生产依赖只有标准库及中立 `identity`，不反向导入 DSH、Room 或内置工厂 |
| `builtin/agents/dsh.NewFactory` | 零资源构造；创建使用宿主合同的 Session，选择原 `Open`／`Resume`，解释现有引用，提供原恢复／结清／配置及运行身份检查 |
| `room.agent_assembly` | 唯一默认工厂选择；正常成员与 `storedSessionBackend` 共用 `newSessionBackend`。默认仍只接受 DSH，无注册表、第二套配置或热切换 |
| `room.sessionHost.PrepareOpen` | 原 Workspace、引用及固定模型快照检查，原引用发布与执行／MCP 绑定；只交出有效 Launch 值，不向工厂传 service、Store、access 或模型服务 |
| `room.dshSession.prepareTurn` | 原 policy／时区、材料／恢复上下文、输入目录及 discovery 预算、generation＋turn 绑定、正式接收证明和私有诊断；通过 PreparedTurn 交出 Prompt、结果确认与资源释放操作 |
| 原 MemberAgent／Store／输入 owner | 成员状态机、调度和轮换、检查点 writer／目录锁、输入与 History 消费权威、撤权等待均保持；未搬迁 R5b.2 或新增业务 owner |

`agent.Session` 只编排打开、Prompt、宿主确认、释放和活动读取。ACP SessionID、backend／opening／进程文件及协议处理仍由 `internal/agent/dsh` 独占。必要值沿原 JSON／TOML 字段与标签共享；DSH 的 ModelConfig 保留自身校验方法，使用同一值结构转换，不增加持久字段。凭据、执行 token 和 MCP headers 仍为临时绑定。

## 正常入口与已保存状态

正常 `newMember → executionBackend → memberBackend` 和关闭 Room 的 `storedSessionBackend` 使用同一装配构造及工厂。`runtime_assembly`／`runtime_recovery`、`rotation_state`、模型应用、scheduling／默认助理、Workspace、关闭／删除和 activity 继续消费 R5b.1 合同。

引用读取仍在原调用者的目录／Store 锁内，经受限 Checkpoints 提供有界快照和指定代际存在性查询；工厂核验原版本、成员身份、目录格式、pending 与孤立目录保护。`session.json`、`session.json.dsh*`、模型快照及 JSONL 路径／格式不变，读取不创建替代空状态。

打开、正常恢复、结清检查、恢复配置检查及模型应用的运行身份检查已删除 Room 对 DSH 组件入口的直接调用。模型切换仍使用已经准备好的固定 ModelTarget；没有把当前目录的动态模型解析加入已准备的切换。历史迁移／输入升级及纯文件读取也使用同一默认工厂的引用与结清能力。架构测试限制新增构造／检查旁路。

## 打开、执行与关闭

宿主先按原顺序恢复固定模型、核验安装身份、保存模型快照、发布引用，再申请执行与 MCP 绑定；工厂随后打开组件。准备失败不变成已执行事实。无组件的打开失败释放绑定；返回组件且同时返回错误时，Session 仍交给原 ownedSession／discard 路径关闭，不能丢弃清理所有权。

宿主准备 Prompt 时只取得图片能力及敏感 token 脱敏操作。工厂执行 Prompt 后，Finish 独立核验输入／MCP 接收证明、记录供给状态和可选诊断，Close 再逆序释放临时输入绑定／文件及活动登记。ACP 成功不能覆盖宿主证明失败；材料预览不推进消费。原直接注入组件的隔离 fixture 共用这些操作，不用于普通成员装配。

Session 关闭仍先撤销并等待原 MCP／输入授权，再关闭组件。原 `dshCloseResult` 保留 errors.Is／As 和 cleanup pending／已结清但保存失败的区别；`closeOwned` 继续等待实际进程观察结清，Member／调度／Store 不因超时或取消而提前失权。未新建租约或持久关闭记录。

## 验证与限制

后续 [S2a 内部助手边界验证](../agent-collaboration-boundaries.zh.md)补充了打包 DSH `0.2.2-rc.1` 的真实版本、原生工具／指南与生命周期轨迹，供 P4b 复用；其中保留子 Agent MCP 缺失和在途取消／关闭失败。该专项不代替 P4b 的能力归属整理及其余验收矩阵。

逐命令、测试数量、源码哈希及日志位置见[验证证据](agent-factory-validation.json)。公开包集成 fixture 只导入 `agent` 与 `builtin/agents/dsh`，启动本地确定性 ACP 子进程，验证新建／原 Session 恢复、独立输入证明失败、关闭与 PID 消失；它不使用真实模型或平台。

验证还覆盖隐藏后端具体类型的模型轮换 fixture、部分打开失败、撤销等待、手动／自动轮换、未知提交重试、关闭 Room 模型应用、正式输入、History、图文材料、恢复与 Store 所有权。默认构建、help、装配检查和全仓 e2e 配置编译单独记录；编译不计为真实后端验收。

| 检查 | 结果 |
| --- | --- |
| 公共合同／工厂、DSH 组件与架构整包 race | 74 个顶层用例、含子用例 212 项通过 |
| Room 定向 integration＋race，`-parallel=1` | 125 个顶层用例、含子用例 257 项通过，包内 77.145 秒 |
| 最终适配与公开进程 fixture race | 11 个顶层用例、含子用例 13 项通过 |
| 默认构建、help、extensions check | 通过 |
| 全仓 e2e 配置编译 | 最终通过；首轮发现旧审计 fixture 需要私有 `Run` 便捷接口，已保留，公开合同仅要求 `RunPrompt` |
| 公共合同／工厂、组件与架构 vet | 通过；Room 标准 vet 仍有下述原告警 |
| 公共合同生产依赖与差异检查 | `agent` 的项目依赖只有中立 `internal/identity`；`git diff --check` 通过 |

首轮较宽 Room race 的材料超时用例一次返回 attachment unavailable，未满足其 timed out 状态断言；该用例独立 race 重跑通过，保留首轮失败记录，不修改材料算法或测试门槛。标准 vet 的原 `room_action_images.go` 两条重复 JSON tag 告警仍保留。完整业务／全仓 race、真实 DSH／模型及浏览器不计通过。

## 收益与停止点

DSH 的组件获取、引用解释和恢复／结清／身份核验现在有一个可独立验证的公开工厂，普通执行和离线检查共用选择；外部实现无需取得完整 Room 或 Store。原准备、确认和存储算法只保留在宿主，工厂不复制输入、模型或成员 owner。公开类型限于这些实际调用所需的值和操作，根模块依赖不变。

P4a 到此停止。DSH 模型验证、故障分类、活动及材料映射仍有 Room 内专用适配，留给 P4b 集中并记录 ACP／DSH 扩展／TeamX 接收桥接的能力归属，再完成代表性真实后端对照。尚未接入 Codex、Gordis DSH 生命周期或后端裁剪；没有部署、日常服务切换或远端业务写入。
