# Room 授权与工具测试收缩

对应[测试改造方案 §9.2 第二批](../TEST-SUITE-REFACTOR-PLAN.zh.md#92-最推荐的实施顺序)。本次的断言承接、隔离及验证记录如下。

| 原断言 | 下层承接 | 保留的真实边界 |
| --- | --- | --- |
| `TestReferenceActionRefusals` 的请求形状、版本、动作类型／重试限制 | 直接调用 `referenceActionRequestError`、`referenceActionUnitConflict`、`validReferenceSpec` | owner 保留目标不符、过期 turn、账号、未注册能力、inspection 不符、伪造人类及过期 policy 的零派发检查 |
| 同组的指令正文改变、撤销、到期及答复歧义；`TestReferenceActionRuleLifecycleAdmission` 的六类规则状态 | 最小私有 Store + 合法 Room；直接调用 `referenceAuthorityLocked`，不用 Actor、Inbox、resolver 或模型；规则 suggested／expired／revoked／old-version／old-token／foreign-member 均直接验证 | owner 保留 revoked／old-token 的不落盘／不派发与刷新回执；`TestReferenceActionRechecksAuthorityBeforeRetry` 保留派发后撤权／取消／过期 turn；`TestReferenceConversationDirectInstructionRevocationAndPendingQuestions` 保留真实指令生命周期 |
| `TestReferenceActionThreeAuthorities` 的 object/discussion × 四类依据 | 最小 Store 直接覆盖两种目标的指令／规则依据；`referenceAuthorityLocked` → `replyBasis` 覆盖两种目标的准确答复及讨论答复歧义 | discussion 四类依据与 object 人类依据仍经真实 owner 落盘、派发、原 ID 重放及不同正文冲突；指令清历史后履行继续保留 |
| 工具入口默认授权、自证及重放 | 既有 `TestLarkToolGrantPlan` 承接默认计划；纯投影测试承接提交版本与精确目标选择 | 移除三个入口夹具中无关 GitLab 账号／可执行文件；HTTP/member/onboarding 各自保留正常接线、真实权限读取与独有重放；自证缺失保留实际执行 guard |
| 独立目标的两个顺序与两类 overlap | 顺序保存使用受控连接生成合法账号及真实 `changeToolBindingLocked`，不启动原生 CLI；免除无关远端权限读取；提交投影直接测试 | overlap／preserved-overlap 继续经过真实 owner，在 prepare 中插入其他目标提交，检查实际提交版本与原 ID 无写入重放；ABA、其他成员、未知提交与重启测试保留 |
| 跨模型 provider × 状态 × 完成时机 | 移除 Lark 场景无关 GitLab 夹具；GitLab 复用当前 manager 装配认证材料；每个场景仅初始化实际轮换所需的 A、B 模型 | 两个 provider 的轮内／轮后结果、暂停、成员替换及 GitLab 取消保持独立；原 operation、精确成员、Inbox 自动继续、暂停和单次 OAuth 交换断言保留。没有证据证明可合并的时序不删除 |

保持四槽、真实私有目录和正式持久同步；不改变生产授权、存储或关闭逻辑。

## 改造范围与隔离

原六组切片的 6 项顶层、含子项 52 项，改后为 15 项顶层、含子项 98 项；新增 7 项动作规则检查、2 项工具检查。动作拒绝由 14 个 owner 场景缩为 7 个；授权成功由 8 个缩为 5 个；规则准入由 6 个缩为 2 个；独立目标由 4 个完整配置组合缩为 2 个 overlap 加 2 个真实存储顺序检查。退出上层的每项均由表中下层生产规则和保留组件边界承接，没有增加跳过或移动构建标签。

三个工具入口仍各自使用私有 manager／Room、独立账号队列及真实 CLI 只读权限流程。新顺序保存检查复用现有 `connectPending`／`connectSucceed` 创建合法 Lark 账号，再通过真实注册表和绑定 owner 保存；不伪造第二套账号或授权决策。GitLab 使用现有受控 OAuth 夹具。各自临时目录、队列和回执均隔离，子场景串行执行。新增并行顶层继续共用 `parallelFixtureTest` 四槽；传递 helper 不改环境或工作目录。

模型组合保留原 11 个场景。轮内完成与轮换后完成、两个 provider、暂停和成员替换具有独有断言，本次没有证明可删除的交叉项，因此只缩小不相关初始化：Lark 不再创建 GitLab manager／应用／TLS server，GitLab 在当前 manager 保存应用并复用受控传输，每次只初始化 A、B 两个模型。通用 `onlineModelManager` 仍默认初始化 A、B、C；其余调用方行为不变。

另校准 `TestLarkAuthHTTPGuardsCancellationAndShutdown` 的过期 CSP 断言：从 `img-src 'self';` 更新为当前正式 Web 及其合同测试均采用的 `img-src 'self' blob:;`。保留整条指令匹配、同源 QR、认证互斥、取消及关闭断言；不修改 Web 生产策略。这是上一批完整集成已确认的唯一失败。

## 验证与计时

环境：2026-11-20，macOS ARM64、Go 1.27.1，当前 HEAD `e098afda95bed073fe127b8e02602661a124ecaa` 加已有工作树。本次只改测试与实施文档，保留既有图片 UI 和第一批改动。精确选择、源码摘要、逐项 wait／held 和结果见机器可读证据（本地验收文件 `authorization-tool-matrix-evidence.json`）。

| 检查 | 结果 | 命令墙钟／包执行（秒） |
| --- | --- | ---: |
| 改前核心切片，默认并发 | 6 项顶层／含子项 52 项通过，零跳过 | 22.108／21.898 |
| 改后核心切片，含全部新增下层检查 | 15 项顶层／含子项 98 项通过，零跳过 | 19.192／19.070 |
| 改后核心切片，`-test.parallel=1` | 同范围全部通过，零跳过 | 25.053／24.887 |
| 扩大调用方的 `-race -tags=integration` | 94 项顶层／含子项 382 项通过，零跳过，无竞态报告 | 96.772／96.648 |
| 全仓核心＋本地集成 | 1,924 项顶层／含子项 4,527 项通过，9 项顶层跳过，零失败；本批 15 项全部通过 | 768.749／Room 737.881 |
| 全仓 `go vet ./...` | 通过 | 3.599／— |
| 全仓 e2e 标签编译，`-run '^$'` | 通过；未运行浏览器／真实依赖 | 15.545／— |

主对照顺序运行，保留构建缓存，用匹配源码的 `go test -c` 二进制经 `go tool test2json -t` 执行，工作目录为 `internal/room`，参数为 `-test.v=test2json -test.count=1 -test.timeout=3m -test.run=<证据中的 selection_regex>`；编译不计入切片墙钟。比较覆盖相同业务范围，改后包含全部新增承接检查；不是只比较剩余上层测试。

同范围墙钟 **22.108 → 19.192 秒，缩短约 13.2%**。独立目标合并的持槽时间 9.878 → 4.672 秒，动作拒绝 5.125 → 3.079 秒，授权成功 6.058 → 3.948 秒，规则准入 3.746 → 1.576 秒；这些区间重叠，不能相加当作墙钟。新增顺序保存组持槽 1.521 秒；长任务成本降低仍会受其他夹具和队列影响，不外推全仓收益。

开发中的首次对照在保留规则覆盖后为 22.108 → 22.055 秒，几乎持平；新增顺序组当时仍初始化原生 CLI。随后改用受控连接并去除模型 C 初始化，复用同一份改前结果，才得到上述最终对照。更早一次新增规则测试因无运行 Agent 的视图 `current_turn=0` 与请求 turn=1 的刷新视图比较失败，已按生产合同修正期望；该失败样本及开发样本不纳入提速结论。

完整集成只运行一次 `go test -json -tags=integration -count=1 -timeout=15m ./...`，正常收完：22 个包通过，5 个包无测试。墙钟包含编译，15 分钟仅为沿用上一批的显式诊断上限，未修改 Makefile。CSP 检查在定向 race 和完整集成中均通过；完整集成健康门槛现已补齐。原先 831.331 秒的全量因 CSP 失败且工作树负载不同，不作为本轮健康提速基线，不据此宣称全仓下降比例。

定向 race 覆盖全部本批规则／组件及扩大的动作 owner、真实撤权／ABA／未知提交／重启、GitLab 认证／接入和模型 helper 调用方。最终源码摘要核对一致；`go vet ./...` 与全仓 e2e 标签编译通过。没有运行全仓 race、独立全仓核心计时或真实浏览器／模型／平台验收。本次完整集成约 12 分 49 秒，仍超过八分钟目标；独立核心三分钟预算未重测。第三批及后续优化继续按原方案独立实施，本轮不扩展。
