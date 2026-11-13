# Scion 共享账号真实并发验收

日期：2026-11-13。范围：[Room 调度与 Scion 并行开发方案](../ROOM-SCHEDULING-AND-SCION-PARALLEL-DEVELOPMENT-PLAN.zh.md)阶段 3。

状态：**阶段 3 完成**。真实并发编码、远端发布、Room 交接、独立集成测试及重启核对通过；误建分支已按用户授权清理，偏差保留在下文。账号模式为**共享身份**，不代表多 subject 隔离；本轮没有浏览器验收。

## 实际运行

复用阶段 2 的 Room `9e485abe9cac7bc739ec0980a1ac8d82`／instance `bda6755bf2e43649c27752ea15f6edc0`，创建快照为 `scheduling: parallel`。四个成员、membership、独立 Workspace 和平台绑定均未替换，详见[环境准备记录](scion-parallel-environment.zh.md)。

实例为 `https://scion.opencsg.com`，仓库为 `OpenCSG/teamx-demo`。四成员通过各自正常 `teamx tools run` → 成员 HTTP 入口复核 subject 均为 `csgship`，使用同一个已有受管平台。实际模型为 `deepseek-v4.1-flash-1017`（受管 `daily` 模型，revision 1），DSH 为 `0.2.1-alpha.1`，native scn 为 `0.1.13`。没有替换 Backend、Session、平台工具或模型响应。

初始完整基线及最终保留的 `main`：

```text
c1078e705cc235a88699caf8aaf51c45803df33aafb8849ea36949756f8a36d4a0
```

需求为 Python 标准库记账摘要：金额解析、分类汇总、确定性 JSON 报告。architect 主持任务拆解；三个 developer 读取准确设计版本后执行各自模块、边界测试和发布。developer-a 等三项完整交付后，在自己的独立集成分支通过远端 `cat` 取回准确版本，组合运行真实端到端测试。

设计 V1 发布后，团队自行发现并修正了零合计示例笔误（V2），又在 Room 澄清“集成分支必须自包含、应提交所有取得的来源文件”（V3）。接口与三项任务版本未改变。architect 最后在自己的 Workspace 检出准确集成版本，独立运行 62 项测试，回到 DESIGN 分支后发布需求覆盖结论。

## 并发证据

[提取工具](../../scripts/scion-parallel-evidence.py)读取真实 DSH v4 zstd journals，核对 journal 所属 membership 的 Workspace，关联实际源码 `write` 及其成功结果、实际 `unittest` 调用及通过结果；不使用人为屏障、延迟工具或任务完成消息时间代替执行证据。

| 成员 | 首次源码写入（Asia/Shanghai） | 首次单模块测试通过 | 测试 |
| --- | --- | --- | --- |
| developer-a | 19:34:03.178 | 19:34:11.962 | 18 项 |
| developer-b | 19:33:43.894 | 19:33:53.071 | 21 项 |
| developer-c | 19:33:45.364 | 19:33:47.313 | 16 项 |

developer-b 与 developer-c 的真实源码写入至单模块测试通过区间，在 **19:33:45.364–19:33:47.313** 共同重叠 **1.949 秒**（UTC 为 11:33:45.364–11:33:47.313）。满足“至少两个开发成员真实执行区间有交集”。三成员的回合也曾同时 active，但三个编码／测试区间没有共同交集，因此不声明“三开发者同时编码”。这段较窄区间内没有重叠的两个工具调用，不将其描述为工具进程同时运行或模型 HTTP 请求并发的单独证明。

本机保留 `execution.jsonl` 状态采样、各成员 DSH diagnostics 和带 journal／tool call ID 的 `development-overlap.json`；底层 journal 保留实际 turn 起止及中断原因。下述环境中断发生于上述源码／测试时间段之后，没有以暂停前后累计时长制造重叠。

## 远端交付与集成

任务分支均为 `teamx/<完整 room_id>/<完整 membership_id>/<任务>`。各成员发布版本及作者经独立远端读取核对，未用 Agent 自述代替检查。

| 任务 | 成员 | 最终完整 Scion head |
| --- | --- | --- |
| DESIGN（V3） | architect | `c1ad885729d7b27a4e141933103fdfc3bb2cd68093ec82284223a5f01fcd9f5656` |
| T1 金额解析 | developer-a | `c12290fd7a49a0f4e1b32a62e2ade8e7fcefd7b79abed709c8f406cdd311a746a8` |
| T2 分类汇总 | developer-b | `c1fd78f244b06e566b1963860461003765824f506f929be426bc09d13e7db31ea1` |
| T3 JSON 报告 | developer-c | `c1538652ca1743a1bdf5e64facc802048daf1963ea88afeb332b2540196cb2a571` |
| INTEGRATION | developer-a | `c1aec1218a3c1dc161546cf0cec978c6547877ed995e66203c13994d8dd81755d4` |

集成分支：

```text
teamx/9e485abe9cac7bc739ec0980a1ac8d82/2b4d31d6dee6b7af0c54089c9c0ccdd2/INTEGRATION
```

验证器从远端精确 head 重新下载文件至独立目录，逐字节比较设计、三模块和三项任务测试与各自来源版本；检查集成相对基线恰好新增 9 个文件，根 README 不变。随后重跑模型交付的整套测试，并额外运行外部契约检查：金额解析 → 分类汇总 → JSON，覆盖负数、零、中文、空数据、非法金额、确定性和输入不变。

各提交作者明确为 `TeamX 成员ID <成员ID@teamx.local>`；远端各任务分支 reflog 的创建与发布记录均实际显示 `actor: csgship`，且发布记录匹配对应完整 head。署名区分贡献，不构成独立账号身份。

Room 消息包含设计交接、三项完整任务交付、设计勘误与澄清、采用四个准确依赖版本的集成交付和 architect 覆盖结论；最终消息序号为 11。没有为恢复重发原需求或业务写入。

## 中断、偏差与恢复

首次驱动在发送任务后重复调用手动运行入口，遇到 `busy` 而退出；当时无代码或任务分支写入。驱动已移除重复启动，并对恢复入口的明确 `ErrBusy` 作有界等待。原需求只发送一次，后续复用同一 Room 与已有状态。

运行期间，另一个安装版 TeamX 开始监听 `127.0.0.1:8088`，覆盖项目服务的本地访问，成员请求返回 401。保留工作副本与远端状态后改用独立的验收服务端口继续，未切换账号、复制凭据或改变任务版本。结束后项目服务恢复在 `http://127.0.0.1:18088/`（启动参数覆盖端口，原配置未改写）；另一个安装实例保持原状。

architect 查询分支时误用 `scn branch list`，在 DESIGN V3 head 创建了范围外分支 `list`，并在 Room 第 10、11 条消息报告偏差。首次完整独立核验如实因该分支失败，业务测试通过未掩盖范围错误。用户随后明确授权只删除该误建分支。清理前再次核对其 version 1、非默认分支、完整 head 等于上述 DESIGN head；通过 architect 的正常成员工具入口执行一次 `branch rm list`，取得 `action: deleted` 及准确 head 回执，并核对分支总数减少一、其他所有分支 head 不变。清理后的完整独立核验通过。该偏差及授权不因清理而从记录中移除。

当前工具 skill 已明确：列分支使用 `branch` 或 `branch ls`；`branch list` 会创建分支，读操作也不能猜子命令。没有扩大产品权限或自动清理所有异常分支。

## 入口与证据边界

[真实验收入口](../../internal/room/scion_parallel_collaboration_real_test.go)为 `TestScionParallelRealDSHCollaboration`（`e2e` 标签）。必须停止配置对应的常驻服务、传入绝对配置／CLI 路径、准确 Room identity 和全新报告目录，防止同时持有 Store。

- `inspect`：复核已准备的空 Room、成员工具账号、基线和原生读协议。
- `start`：只接受空 Room，且必须显式设置 `TEAMX_SCION_PARALLEL_WRITE=OpenCSG/teamx-demo/task-branches`；发送一次带固定消息 ID 的需求，正常唤醒成员。
- `resume`：同样要求写入 opt-in，检查已有任务，只恢复已暂停成员，不再次发送需求。
- `observe`：采样已有协作，不发送消息或启用成员。
- `verify`：使用 `TEAMX_SCION_PARALLEL_PRIOR_REPORT` 指定初次写入前的报告，独立检查所有远端版本、作者、交付、文件内容、整体验收、真实 DSH 编码重叠及完整分支范围；不因业务测试通过而忽略范围外分支。

`TEAMX_DSH_OUTPUT` 必须是尚不存在的绝对目录。DSH 时间证据提取需要 Python 3.14 的标准库 `compression.zstd`。误建分支清理使用一次性、精确 head 校验的测试 overlay，未在产品或长期验收入口增加通用删除能力。

本轮通过的检查：

```sh
go test -race ./internal/room -run '^(TestTurnScheduler|TestRoomTurns|TestRoomSlot|TestServiceShutdown|TestTeam|TestRotation|TestParallel)' -count=1
go test ./internal/platform/tool -count=1
```

真实验收以 `go test -tags=e2e ./internal/room -run '^TestScionParallelRealDSHCollaboration$' -count=1 -v -timeout=40m` 配合上述显式环境参数运行。协作结清检查通过；清理后 `verify` 完整检查通过，独立重跑 **62 项测试**和额外端到端契约检查通过。

本机非秘密证据保留在 `.local/room-scion-parallel/`：

| 文件／目录 | 内容 |
| --- | --- |
| `concurrency-start/report.json` | 初次业务写入前的完整分支清单、基线、四成员身份和唯一需求回执 |
| `concurrency-live/execution.jsonl` | 正常服务实际开发阶段的状态采样；后因端口冲突停止观察 |
| `concurrency-isolated-resume/` | 隔离端口下继续原 Room 的完整消息、状态采样和各回合 diagnostics |
| `concurrency-independent-verify/report.json` | 功能通过但因范围外 `list` 分支失败的完整原始记录 |
| `concurrency-cleanup-verify/report.json` | 用户授权后的精确删除回执及清理前后完整分支清单 |
| `concurrency-final-verify/report.json` | 最终账号、远端 head／作者／reflog、文件比较、独立测试和编码重叠证据；状态 `remote_delivery_and_integration_verified` |
| `concurrency-final-verify/published-integration/` | 从远端精确集成 head 取得的文件，供独立测试与复查 |
| `recovery-final.json` | 正常服务恢复后的新 service epoch、parallel 模式、11 条消息、唯一需求、四成员空闲状态及五个交付版本 |

最后一次正常服务恢复后，四成员均空闲且无错误，Room identity、模式和成员绑定保留，工作副本仍指向相应发布 head；公共消息仍为 11 条、原需求只出现一次。未通过重新发送业务写入验证恢复。

组件／race 与真实 DSH、Scion 的结果分别报告。本轮没有浏览器操作，没有多 subject 隔离验收，也没有创建／评论／approve／合入／关闭 Review 或部署；这些不加入共享账号并发编码的完成门槛。没有提交或推送本仓库改动。
