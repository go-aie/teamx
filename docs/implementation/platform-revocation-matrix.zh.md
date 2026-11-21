# 动作与引用撤权测试收缩

对应[测试改造方案 H3.1](../TEST-SUITE-REFACTOR-PLAN.zh.md)；本阶段已完成。范围仅为动作与引用 owner 的重复交叉，不改变正式实现、持久同步、四槽并行或 provider 协议。

## 覆盖承接

| 原组合／断言 | 实际生产分支与下层承接 | 上层保留 |
| --- | --- | --- |
| 动作 2 种目标 × 3 个阶段 × 9 种撤权入口 | `preparePlatformCallLocked` 共用登记；`Calls` 的取消、重复 Finish、等待超时、账号隔离与并发测试原样保留 | object 的 body 阶段保留九种真实入口；disable 保留 object／discussion 各自三个阶段，共 14 场景 |
| 引用 3 个阶段 × 9 种撤权入口 | 相同登记表；`bindPlatformReferences` 的 `referenceCommit` 在落盘前检查成员、scope 取消及账号访问 | returned 保留九种真实入口，disable 另保留 before-send／body，共 11 场景；保留独立 commit gate 测试 |
| 凭据轮换／重新授权 | 原两种命名均经 `testReauthorizePlatform` 触发同一 invalidation／settlement／grant commit 边界，并非两个独立正式入口 | 动作和读取各保留两个原场景及账号 101 结果事实；新增 `TestPlatformCallRevocationOAuthCallbackWaitsForEveryCall` 经真实 login／callback，证明正式重新授权及凭据换代等待全部旧调用结清 |
| 禁用／换绑 | `/v1/tools/bind` → `changeToolBindingLocked`，按 Room／成员／membership／工具／平台匹配，落盘后取消，锁外等待 | 两类调用各保留真实 HTTP 入口；换绑仍建立第二账号并禁用旧绑定，再启用新绑定 |
| 移除成员／轮换 Session | `service.remove`／`rotateSession` → `beginPlatformMemberChange`，设置准入门、取消精确成员并等待 | 两类调用各保留两个真实入口，Session 场景仍创建实际 MemberAgent |
| 关闭 Room／删除 Room／shutdown | `shutdownRuntime`／`startDeletion`／`close` → 对应 owner 或全局取消，再等待结清 | 两类调用各保留三个入口；删除仍等待真实异步操作完成 |
| 派发前／响应体／结果返回后 | 登记表语义复用 `TestCallsCancellationRequiresSettlementAndPreservesOtherAccounts`；HTTP 与提交阶段由 owner 检查 | 两种动作各保留零 POST／单 POST unknown／returned；object returned=succeeded，discussion returned=unknown。读取保留三个阶段及晚到结果不落盘 |
| 账号、Room、成员及 membership 隔离／ABA | 实际 owner 匹配条件与既有 `TestToolPreparedBindingRejectsABAAndAllowsOtherMembers` | 新增 `TestPlatformCallRevocationPreservesUnrelatedScopes`，分别通过 binding HTTP、成员变更、凭据变更及 Room 关闭核对 Room ID／instance、agent、membership、platform、tool 各坐标；既有 ABA、绑定准入、原账号事实、未知结果、重启测试保留 |

移除的是入口与共享阶段之间无额外生产分支的交叉；动作与读取的返回及提交路线分别保留。阻塞屏障先证明请求已进入指定阶段、真实入口已取消 context、登记仍未结清，再释放响应并验证入口完成及登记表为空。不会仅用即时未返回推断结清正确。

新增隔离检查仅将调用元数据与完成回执放入生产 `Calls`，其 manager、账号、绑定及服务均为私有真实组件；不同坐标的回执独立持有。取消发生后经过 manager 锁屏障核对整个匹配结果，再只结清其他已取消调用，保留准确目标阻塞入口；成员变更同时验证 `ErrBusy` 准入门。20ms 有界负向窗口只补充已确定的取消／未完成回执证据，正确性不依赖即时 select。OAuth 检查复用原登录夹具和正式 callback，第一条调用完成后仍等待第二条，最后验证凭据版本只增加一次及单次授权交换。没有复制取消算法或增加另一套调用表。

## 装配次数与隔离

完整计时选择涵盖两份 managed 文件的全部测试、新增两项 owner 检查及既有 ABA／其他成员检查；下层 `Calls` 普通／race 独立通过。未修改任何共用 helper，其他 helper 调用方不受此次改动影响。

| 场景夹具 | 改前实际执行 | 改后实际执行 |
| --- | ---: | ---: |
| managed action | 70 | 30 |
| managed reference | 31 | 15 |
| 原 ABA Lark 夹具 | 2 | 2 |
| 新增 owner 隔离夹具 | 0 | 4 |
| 新增正式 OAuth callback 夹具 | 0 | 1 |
| 合计 | 103 | 52 |

按成功执行的叶子及其无条件 fixture 调用逐项计数，每个叶子构建一次私有 manager；不是只静态比较表格项数。合计减少 51 次场景装配；其中矩阵减少 56 次，新增证明补回 5 次。这里不统计 helper 内部额外对象、原生 CLI 或系统调用数，也不将装配次数换算成 I/O 根因。TLS、磁盘同步、独立 Store 和实际删除／关闭仍走原路径；全部选择零跳过。

## 验证与计时

环境为 2026-11-21、macOS ARM64、Go 1.27.1，HEAD `0069e0283a98a552827aef7be089a5a3d416daee` 加原有工作树。源码摘要、完整选择、逐项结果及 wait／held 见[机器证据](platform-revocation-matrix-evidence.json)。匹配源码的 `go test -c -vet=off -tags=integration` 二进制经 `go tool test2json -t` 执行，编译不计入切片墙钟；`-count=1`、四槽、默认并发，单次包级诊断上限四分钟。普通改前／最终改后顺序运行，最终计时期间没有并行运行本轮其他检查。

| 检查 | 结果 | 命令墙钟／包执行（秒） |
| --- | --- | ---: |
| 改前完整相关族 | 11 顶层、含父子 108 项通过 | 42.445／42.215 |
| 最终改后，包含新增证明及 ABA | 13 顶层、含父子 58 项通过 | 22.135／21.911 |
| 同范围 `-parallel=1` | 13 顶层、含父子 58 项通过 | 26.634／26.417 |
| 扩大的 Room 定向 race | 38 顶层、含父子 173 项通过，无竞态报告 | 34.301／34.030 |
| 下层 `Calls`／`WaitCalls` 普通及 race | 各 4 项通过，无竞态报告 | —／1.006、2.108 |
| 默认／integration／e2e 全仓编译 | 三种均通过，`-run '^$' -vet=off` | 不作性能对照 |
| 标准 `go vet ./...` | 未通过：原有 `room_action_images.go:22` 重复 `room_id`／`room_instance` JSON tag | 不作性能对照 |

全部执行选择零失败／零跳过。普通编译与回归显式禁用 vet 是为了分别记录测试和既有阻挡；标准 vet 独立执行，未宣称全绿。定向 race 还覆盖完整 ReferenceAction 族的撤权、重试、unknown／重启及持久失败，CLI 结清、成员工具撤权和原 OAuth PKCE／双账号重新授权隔离。三种标签检查仅编译，不等于真实 e2e。

| 父组 | 首子项 cont 至末子项完成（秒），改前 → 改后 | 叶子累计 wait（槽秒） | 叶子累计 held（槽秒） |
| --- | ---: | ---: | ---: |
| 动作撤权 | 24.332 → 6.047 | 132.305 → 23.298 | 96.124 → 22.274 |
| 引用撤权 | 7.886 → 3.042 | 38.552 → 9.300 | 30.836 → 11.212 |
| 新 owner 隔离组 | — → 0.726 | — → 0.000001 | — → 2.864 |

Go 对这两个并行父项报告的 `Elapsed` 为 0；表中使用事件时间戳计算实际子组跨度，完整父 run 至末子项完成的跨度也保存在机器证据。wait／held 是累加槽位时间，不能相加当作墙钟。新增 callback 顶层与其他顶层共同计入最终整族墙钟。

整族局部观察为 **42.445 → 22.135 秒，少约 20.3 秒（47.9%）**。这是一次成对样本，不能保证稳定百分比，也不能外推全仓分钟级收益。首个开发样本 20.939 秒尚无正式 callback 证明；补齐后的 20.429 秒样本与 race 编译短暂重叠，因此另取最终独立的 22.135 秒，不用前两者作为交付计时。沙箱首次禁止 httptest 监听，后在沙箱外完整通过；该环境失败不计入性能对照。原始日志位于 `/tmp/teamx-owner-matrix-20261121/`，永久记录保留判定、逐项数据和摘要，不依赖临时目录存续。

本阶段已满足覆盖承接、相关回归、单并发、定向 race 及收益记录条件。未再跑全仓核心／integration／race，未执行真实平台、模型或浏览器；当前全仓健康与三分钟／八分钟预算仍待最终里程碑验证。标准 vet 的重复 JSON tag 保留为独立阻挡。H3.2、H4、H5 均未实施，不因本次局部收益自动扩展。
