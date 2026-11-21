# 核心测试的规则承接与夹具收缩

对应[测试改造方案 H2](../TEST-SUITE-REFACTOR-PLAN.zh.md#104-h2成组削减默认核心的重装配和重复规则矩阵)。本次只修改测试与实施记录；四个业务族一起计时，新增的下层检查、保留的组件与集成都计入范围。没有移动 build tags、增加跳过、扩大四槽，或修改正式存储同步、授权与关闭行为。

## 覆盖承接

| 原测试／重复工作 | 下层承接或收缩依据 | 保留的上层边界 |
| --- | --- | --- |
| `TestInboxPayloadLegacyAndInvalidTypes` 的七种落盘／重建 | `TestInboxPayloadValidation` 直接对 JSON 往返后的 `InboxEntry.validate` 检查合法平台／本地事件及全部非法 envelope | 旧空 type 的真实加载／原序号防重、未知 type 的真实启动拒绝；task event 的持久关联、四个 link／ack 失败点、三个 delivery 失败点、回收与重启全部保留 |
| History／MCP Inbox 请求形状矩阵 | `TestHistoryReadArgumentValidation` 调用 `decodeHistoryRead`；`TestMCPInboxArgumentValidation` 调用 `decodeMCPInboxArguments`；正常边界值也直接检查 | History dispatch 仍拒绝消费字段；MCP 仍拒绝身份覆盖及其他 turn，检查空页不生成 receipt；输出失败、正式确认、租约与终结屏障原位保留 |
| Room 附件的 text-only／数量／字节矩阵、History 的格式／像素错误矩阵 | `TestMaterialImageBudgetsAndFailureCoverage` 调用共用 `notificationImageBudget.prepare` 和真实 `attachment.ValidateImage`，检查不下载、尝试次数、字节累计及失败原因 | Room 真实文件缺失、损坏、元数据像素上限仍经正式读取与 History 各运行；History 保留 text-only 和 unavailable 的 MCP 文本／reason 投影。数量的跨页执行额度、同图复用、正式／History 独立额度、选页身份、撤权、回收和重开仍保留 |
| Workspace 路径策略为了调用规则而创建 manager | 路径策略夹具只提供私有目录、配置 roots 和 Store 路径；直接调用生产 `canonicalWorkspaceDirectory`；快照验证直接使用 `managedRoomConfig` | 真实目录权限、逃逸、符号链接替换、不可用目录修复、外部文件保留、多成员目录 claim、Session 结清及重启均保留 |
| `TestWorkspaceRejectsUnsafeOrIncompleteTargets` 的八次 Git 源仓库初始化 | 六个在 clone 前拒绝的场景只装配 preparer；foreign-origin／foreign-push 仍使用真实提交和 clone | 八项准入全部运行，继续检查失败前无 clone／API、用户文件不被删除。成功 clone、失败证据、子进程回收和超时未删减 |
| Workspace operation 的六种非法进度记录只在 integration 中检查 | 默认层 `TestWorkspaceOperationValidation` 直接调用生产 `workspaceOperation.validate` | 集成保留保存幂等、稳定 ID 目标冲突、重叠操作、进度倒退、真实配置提交、未知提交和重启 |
| GitLab connect helper 为每次调用先创建另一个 manager、平台账号与 TLS server，再复制应用 | 在本次实际 manager 安装所需 OAuth application，复用原受控 OAuth transport；setup/import 流程继续由自己的测试验证 | 真正的 start、PKCE 参数、callback、cookie／origin／actor、原账号／成员与版本、单次 exchange、丢失回执、防重及重启路径都保留；没有缓存或跳过正式身份核验 |
| GitLab setup 的 foreign owner／origin／replacement 三次完整操作 | `TestConnectApplicationImportAuthority` 直接测生产 `connectApplication`，同时覆盖错误 ID、已绑定 fingerprint 变化与成功精确匹配 | callback 不符、取消、错误 human、Agent 的 HTTP 拒绝保留；导入者远端身份、scope、callback 核验和恢复仍保留 |
| 模型轮换 provider × 状态 × 完成时机的 11 项组合 | 两种 provider 的结果投递汇合到 `deliverOperationResultLocked`／`operationContinuationLocked`，轮换和暂停由共同 MemberAgent 生命周期负责；保留 7 个实际轮换场景 | enabled 的轮内／轮后、paused 的轮内／轮后各一项；GitLab 取消、两种 provider 的晚到成员替换分别保留。原 operation、精确成员、自动继续、暂停与单次 OAuth 断言原位保留 |
| current member 的 66 个字段 × 值组合，以及 read／顶层字段／其他 endpoint 矩阵 | `TestPlatformMemberCurrentRequestFields` 直接调用生产 `validatePlatformMemberRequestFields`，保留 null／显式零／空值区别及正常请求 | HTTP get／notifications 各保留一次显式零拒绝；布尔字段类型、伪造人类 source、零 operation 副作用继续经真实入口检查 |
| current CLI 两种 provider 各四次开关；两种 provider 相同历史回执与保存前失败 | Lark 保留完整四次开关和重放，GitLab 保留成功配置、一次业务请求和原 ID 重放；历史回执读取及 reserve／corrupt 在 `configureCurrentPlatformMemberNotifications` 中先于 provider 分派 | 两种 provider 的默认配置／已存范围承接继续由现有 toggle／adoption 测试执行；历史回执、账号删除和成员撤销由 Lark 保留；两种 provider 的实际写入后 completion 保存失败均保留；全部九个真实并发 reservation／账号／成员／订阅变更场景保留 |
| 输入升级每个发布点都构造一份两成员旧 Room／队列 | `inputUpgradeSeed` 以单成员旧 Room、真实 task event 和平台通知构造材料；关闭 Runtime／Store 后，在各父组内只生成一次，再复制到每个子项的私有目录 | 九个真实发布中断点及五种 authority 损坏仍独立执行；原始备份 ID、部分发布拒绝、完成后无二次升级、进程死亡恢复、未知动作恢复门槛和实际 DSH 工具换代全部保留 |

没有退出支持的格式或能力，也没有删除顶层业务测试。Input reader 的协调器租约、业务来源关联、输出确认／丢失应答、保留／回收、跨成员及共享授权检查，Workspace 的跨文件发布、真实进程结清和恢复均属于独有组件风险，本轮没有继续合并。DSH 包外夹具仍按方案作为后置候选，不投入非 Room 主路径的改造；H3–H5 未实施。

## 实际工作量与隔离

升级夹具在本次选择中原来构造 18 次，新版构造 6 次；其中九个发布点共用一个关闭后的种子，五种损坏共用另一个，另外四个单例各自构造。初始成员材料由 36 个减为 6 个。原始 request 直接由真实 `SaveRoom` 写入种子，不再为每份种子另做一次 Room Post。task event 与通知仍经原生产接收路径持久化；每份目标均重新打开自己的 Store，没有共享可变 Store、已打开 manager、journal、backup 或正式读取缓存。

Workspace 六次多余源仓库构造被取消，对应每次 init／config name／config email／add／commit 共五个 Git 进程，即 30 次初始化进程；目标检查及其实际 Git 验证仍按原路径执行。模型完整装配 11 → 7；current storage failure 装配 8 → 5，两个 provider 的 completion 均真实执行；历史回执装配 2 → 1。GitLab helper 的每次调用取消一个临时 manager／账号／TLS server；本轮没有埋点统计全部原生 CLI 调用总数，不把静态删除的装配数冒充原生调用计数或墙钟。

只读种子的生命周期归各自父测试，父级不持有四槽；子项仍串行且每项独立复制。沿用原并发策略，没有把串行场景挪到并行尾部来制造时间下降。共用 helper 的外部调用方仍走原正式流程，三种标签编译检查用于核对 helper 可见性。

## 验证与计时

环境为 macOS ARM64、Go 1.27.1、HEAD `26940f5c28a61dfc5f448ef705ff03053845ba05` 加原有工作树。改前／最终改后顺序执行，保留构建缓存，使用 `go test -json -tags=integration -count=1 -timeout=12m ./internal/room -run '<自动从四族文件提取的完整测试名选择>'`。12 分钟仅为本次有界诊断上限，没有改 Makefile。

| 检查 | 结果 | 命令墙钟／Room 包执行（秒） |
| --- | --- | ---: |
| 改前四族 | 279 顶层通过、1 既有跳过；含子项 584 通过、1 跳过 | 235.776／226.884 |
| 最终改后四族，含全部新增承接 | 286 顶层通过、同一项跳过；含子项 597 通过、1 跳过；零失败 | 201.325／191.674 |
| 同范围定向 race | 286 顶层通过、1 既有跳过；无竞态报告 | 264.610／244.095 |
| 默认／integration／e2e 全仓编译 | 三种均通过；只编译，不执行 e2e | 24.334／24.884／15.522（并发检查，非计时对照） |
| 单并发重点回归 | 24 项顶层／含子项 92 项通过，零跳过；覆盖升级整族、全部新增规则、原生 callback、current 保存失败及模型轮换 | 33.109／— |
| 标准 vet | 未通过：原有未跟踪文件 `internal/room/room_action_images.go:21` 重复嵌入 `room_id`／`room_instance` JSON tag；与 H1 已记录的问题相同 | 6.753／— |

唯一跳过项是原有 `TestWorkspaceDirectoryMountedPath`，需要显式指定真实挂载路径；本次没有改变其条件。输入升级的进程死亡仍由 `TestInputUpgradeRecoversAfterProcessDeath` 实际启动子进程，不把 helper 在普通入口返回当成进程验收。完整选择、逐项结果、槽位记录和最终源码摘要见[机器证据](core-test-fixtures-evidence.json)；它是此次结果的证据，不是需要永久手工维护的选择 gate。

同范围最终墙钟观察为 **235.776 → 201.325 秒，少约 34.5 秒（14.6%）**。首个顶层 run 到首个顶层 cont 从 193.269 → 161.960 秒，其后并行尾部从 31.980 → 28.957 秒；这是该四族选择的阶段跨度，不能外推全仓。已有夹具日志中，Workspace 累计 held 49.104 → 45.182 槽秒，平台累计 held 98.580 → 91.617 槽秒；不能把 held 相加当作墙钟。

**分钟级收益目标未达，且不能把 34.5 秒全部归因于本轮改动。** 152 项源码文件未变、改前报告耗时至少 0.1 秒的共同测试，其耗时比值中位数也为 0.859；并行用例还包含槽位等待，因此这一比值只说明对照存在普遍波动，不能证明特定外部负载、磁盘或 `fsync` 是原因。第一次改后开发样本为 211.320 秒／200.996 秒包执行，但当时模型组合只有六项；复核后补回 paused／轮内结果这个有交互价值的组合并重新测最终七项，不把开发样本作为最终源码证据。

标准 vet 的失败位于本次未修改的生产文件，未扩展 H2 去改动图片动作接口。最终检查完成后核对源码摘要；没有遗留在生产路径的计时埋点或新增模拟开关。

本阶段复用方案已有的全仓健康基线，没有再跑完整核心、全仓 integration 或全仓 race，也未运行真实平台、模型或浏览器。默认核心三分钟与完整本地集成八分钟仍未重新验收，不能以本次局部对照宣布达成。后续优先按 H3 审查仍需真实 provider／owner 组合的实际工作量；H4 的并行扩散与 H5 的历史验收退役仍须单独实施。
