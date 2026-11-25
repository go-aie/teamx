# Lark 图文动作的规则、协议与执行边界

2026-11-25，实施[测试改造方案](../TEST-SUITE-REFACTOR-PLAN.zh.md) §12.4 的 G1。只调整测试职责、夹具与文档；生产权限、持久化、fsync、发送和关闭合同保持原状。不扩展 G2–G4，不改变 F3 已收口的图片／文本进程死亡矩阵。

## 覆盖承接

| 原组合／断言 | 承接 | 上层保留原因 |
| --- | --- | --- |
| `TestLarkImagesManagedCLI` 的 self／group × 单图、多图、caption、上传失败与结果分支，共 14 组 | 既有 `TestImageSendInputAndTextAdapterBoundary`、`TestImageSendExactContentVerification`、`TestImageSendNativePostMetadata`、`TestImageSendAdapterSettlement`；新增 `TestPinnedImageSendProtocol` 直接调用真实 `Pinned.SendImages` | 保留 self 单图、group 完整 caption、self 发出后 unknown、group 写后核验失败，共 4 组。真实 CLI／HTTP／账号路由、原 ID 重放、改正文／图片顺序／数量拒绝、unknown 等价原图换 ID 防重、已知 ID、原图丢失及 Room／History 状态不变继续验证 |
| `TestLarkImagesDispatchGuards` 的两种源格式 × concurrent／cancel／digest／input／policy，共 10 组 | `TestImageActionExecutionGuards` 直接组装真实 Room actor、Store、`service.executeReferenceAction`、`WithImageActions` 与原图租约；受控 writer 在屏障后执行真实 lease／dispatch 回调。通用字段矩阵沿用 `TestActionDispatchFrozenContentAndExactRecord` | 每种源格式保留一次真正的原生上传暂停：源清理被 pin 阻止、Room 可追加、同 ID 返回 processing、结清后清理成功。两条分别验证 policy 更新阻止最后 POST、manager 取消经图片适配器终止原生上传；组件层继续穷举全部变更分支 |
| `TestLarkImagesLedgerFailureReleasesSource` 的两种源格式 × intent／attempt／unknown-intent，共 6 组 | `TestImageActionLedgerFailureReleasesSource` 保留全部 6 个真实文件故障点，在最小执行组件中观察源 pin 和 writer 零调用；unknown commit 冻结写入，原 ID 只读 unknown／Attempts=0 | 与目标无关的 manager、平台账号初始化、HTTP、CLI 和四张图片准备退出；真实 intent／attempt 先于上传另由保留入口的上传屏障读取准确记录证明 |
| OptionalPermission 1 组及 PermissionRecovery 的 application／user／unchecked 3 组 | 新增 `TestImageUploadPermissionKeepsTextIndependent`，配合原 provider 权限解析／协议测试；`TestImagePermissionRepairGuidance` 直接测正式修复指引 | 合并为一条 application scope 缺失 → 图片不上传 → 文本仍可发 → 原连接修复 → 图片成功的完整流程；无新绑定或账号替换 |
| BuiltinActionFieldDiagnostics 的完整图片夹具 | `TestBuiltinActionFieldDiagnostics` 使用 `Definition().Tool.Bind` 正式注册运行时及 `builtinToolHost` 的实际严格解码 | 不再初始化账号、manager、图片、HTTP 或上传；正式入口接线继续由 `TestLarkActionAgentContract` 验证 |
| `imageSendCLI` 内扫描全部 Room ledger、推导图片来源的断言 | `assertImageActionAttempt` 在 Go 上传／死亡屏障读取准确 action 文件，验证成员、请求、unknown、Attempts=1、完整 digest 和正式记录校验；`assertImageUploadedOriginals` 校验实际上传字节散列和选择顺序 | CLI 假进程只处理 argv、字节、次序、原生响应、POST 和退出；成功／崩溃恢复路径均显式承接账本与原图断言 |

`TestPinnedImageSendProtocol` 不注入 writer：通过固定账号的真实 `Pinned` 与本地 CLI 子进程验证两种目标、三种 payload、全部四个上传失败位置、guard／dispatch 拒绝、上传中取消、POST 后取消／丢失应答／损坏输出。独立构造预期 payload，核对 argv、uuid、完整中文正文、空行、图片字节与次序、上传全部成功后仅一次 POST、失败分类及 0700／0600 临时目录／文件清理。没有 Room、manager、业务授权或 ledger 依赖。

## 实际退出的工作

原主文件共 35 次完整 `imageSendHarness` 装配（33 次并行持槽，另有 OptionalPermission 与字段诊断两次），现为 7 次：4 次发送、2 次原生 guard、1 次权限修复。净退出 **28 次 manager／Room／平台配置／成员授权／HTTP／CLI 组合装配**，对应四图源准备从 140 次降到 28 次。新执行组件另准备 11 × 2 张原图，保留真实文件和 actor；这部分计入改后成本，不将 28 次装配直接换算为时间。

新增 provider 协议包含 13 个本地原生发送场景，取代上层协议矩阵，仍计入整族墙钟。原 10 个图片独立进程恢复分支、文本恢复组和共享夹具直接调用方全部纳入回归；按需真实平台／模型／浏览器的调用方只作 e2e 编译检查，本轮不执行外部写入。

## 验证与测量

环境：Go 1.27.1、macOS ARM64。完整选择包含原主文件、原图租约／dispatch、图片进程恢复、文本恢复、Agent 合同／恢复期读取、既有与新增 provider 承接及 Lark runtime 测试。三个包顺序运行同一测试名并集，使用 `integration`、`-count=1`，包含正式 CLI 解析／HTTP 往返、原生 CLI、恢复子进程及清理；测试二进制构建另列，未另行构建生产 CLI。新增承接计入改后成本，退出名字在改后不再存在。不是一次 `go test ./...` 的全仓墙钟。

| 正式健康对照 | 改前 → 最终改后 |
| --- | --- |
| Room 相关族 | 235.465 → 157.295 秒 |
| Lark provider，含新增真实发送协议与权限承接 | 16.027 → 25.000 秒 |
| Lark application runtime，含修复指引承接 | 0.310 → 0.105 秒 |
| 三包顺序执行合计 | **251.802 → 182.400 秒，少 69.402 秒** |
| 最终改后三个测试二进制编译 | Room 18.326、provider 0.766、runtime 0.471 秒，合计 19.563 秒 |

改前 36 项顶层／含子项 229 项通过；改后 39 项顶层／含子项 232 项通过，均零失败，均有 1 项原有 subprocess helper 在父进程跳过；该 helper 由图片恢复场景实际启动，并未跳过对应恢复证明。测试数量变化不是验收目标。

构建缓存保留，编译成本单列，不把缓存命中当作测试改造收益。改前首次 Room／provider 二进制编译分别为 25.979／0.613 秒，runtime 独立构建未单独计时，因此不伪造完整的改前编译总数。采取更保守口径：即使把改后 **19.563 秒全部编译成本** 加入、改前编译按零计，202.0 秒仍比改前仅执行的 251.8 秒少约 **49.8 秒**。这说明本次确有成组工作退出；样本有限，不能外推为稳定收益或全仓下降。

首次改前探测 Room 为 294.535 秒，正式顺序重放为 235.465 秒，表明本机时间波动明显。最终版本曾有一次整组运行在图片 `embedded/self/before_verification` 的 20 秒原生屏障等待超时，Room 总计 188.445 秒；这一失败样本不参与提速计算。未修改等待阈值，复用同一二进制单独运行该分支 15.221 秒通过，随后上述最终完整选择通过。根因未确定，不能称为已修复间歇性超时；保留失败证据，不继续重跑寻找更好数字。

对照二进制的相关生产路径基于 `6057a7e`，G1 所选文件散列固定。运行期间另一任务独立提交了 `f3b0663` 的 service retention／身份 owner 修复；未覆盖或回退这些工作树修改。阶段 race 通过 Go 标准 `-overlay` 将 `service_retention.go`、`space_identity.go`、`service_retention_test.go` 固定到计时版本，其他 G1 源码保持最终散列；三种标签编译和标准 vet 则针对包含该独立修复的最终工作树执行。

最终定向 integration race 同一完整选择全部通过：39 项顶层／含子项 232 项通过，零失败、零竞态报告，1 项原有 subprocess helper 在父进程跳过。含构建墙钟 Room 215.983、provider 33.685、runtime 2.599 秒，合计 252.266 秒；这是验证成本，不作为普通对照的提速数字。

最终工作树的 `go test -run '^$' ./...`、`go test -run '^$' -tags=integration ./...`、`go test -run '^$' -tags=e2e ./...` 全部通过（约 19.741／19.251／22.688 秒）；`go vet ./...` 通过（3.008 秒），三种编译入口也包含对应测试的默认 vet 检查。源码摘要复核无 G1 文件漂移。编译只证明标签接线，不代表完整执行或真实验收。本地验收文件 `lark-image-action-responsibilities-evidence.json` 保存完整选择、源码摘要、命令口径与结果，按仓库约定不入库；必要结论已保留在本记录中。临时原始日志位于 `/tmp/teamx-g1/`，本记录不依赖该目录长期存在。

默认核心三分钟、完整集成八分钟仍待整轮统一验收；本轮未跑完整 integration、全仓 race、Web／浏览器、真实模型或平台验收，不声明这些门槛通过。G1 按本切片收口，G2–G4 不自动实施。
