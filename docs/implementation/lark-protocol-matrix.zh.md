# Lark 原生协议测试矩阵下沉

对应[测试改造方案 §9.2 第三批](../TEST-SUITE-REFACTOR-PLAN.zh.md#92-最推荐的实施顺序)。已将候选校验提取为原生产路径直接调用的函数，并按以下映射收缩原生进程组合。

| 原断言 | 直接测试的生产规则 | 保留的实际进程证明 |
| --- | --- | --- |
| history 错误分类、JSON 格式、缺少字段、重复、时间和会话范围 | `TestCLIErrorClassification`／`TestDecodeSingleJSONValue`／`TestMessagePageValidation` → `classify`／`decode`／`validateMessagePage` | 退出码／stderr、输出上限、malformed stdout、bot、账号切换、分页及失败零候选 |
| search 完整性、游标、重复、ID／时间／会话／发送者范围 | `TestMessageSearchPageValidation` → `messageSearchWire`／`validateMessageSearchPage`；`TestMessageReadErrorCategories` 承接 native code | notice、独有 native code、bot、账号切换、argv 和单页不扩展、取消 |
| mget 缺失 detail、重复／未请求 ID、时间和 reply ID | `TestMessageBatchValidation` → `messageBatchWire`／`validateMessageBatch`；同上 native code 分类 | 乱序、缺项和删除的实际 DTO；独有 detail error、bot、账号切换、取消、单次 mget |
| sender 可选姓名的重复、非请求 ID、缺项、无效姓名 | `TestSenderNamesValidation` → `senderNamesWire`／`validateSenderNames` | 成功 POST、权限失败、格式失败、超时、账号切换、10+10+5 分批 |
| membership 完整性、重复、游标和租户 | `TestMembershipPageValidation` → `membershipWire`／`validateMembershipPage` | 两页独立 membership 证明、错误租户、账号切换、单页调用次数 |
| image 格式和资源归属 | `TestImageMessageValidation` → `validImageMessage`；保留已有 `ImageKeys`／attachment 校验 | 文件大小／流上限、身份变化、资源归属、下载格式、取消和文件清理仍真实执行 |
| reply/send | 已有输入／目标规则测试；不以纯规则替代提交语义 | 保留 user/chat 各自 argv、权限／guard 拒绝、损坏 stdout 和派发后取消，单次 POST 及未知结果语义 |

四槽上限保持不变，`parallelClientTest` 增加 wait／held（含清理）诊断。候选校验只从原路径提取，生产身份核验不缓存、不跳过。新增八项顶层核心测试全部不启动进程、不装配 Store。匿名 wire 类型仅按原职责命名，字段／校验顺序和原边界不变。

## 收缩范围与保留原因

| 原组合 | 改前 → 改后 | 删除组合的具体承接 |
| --- | ---: | --- |
| history 拒绝矩阵 | 19 → 14 | wrong_chat、duplicate、bad_time、null_items、loop 分别由 `TestMessagePageValidation` 覆盖，包含缺失／null 与空页区别、毫秒边界与相同时间 |
| search 拒绝矩阵 | 26 → 5 | loop／bad_token／missing_token／missing_more／missing_items／null_items／duplicate／oversize、ID／chat／type／sender／时间范围由候选页测试承接；permission／rate／cursor／network／unknown_error／malformed／large 的通用进程通道由 history 矩阵保留，分类由核心测试承接。missing_more 仍保留作为本 DTO 拒绝接线 |
| batch 拒绝矩阵 | 19 → 4 | 字段缺失／null、重复／非请求 ID、时间／reply ID 由 batch 校验承接；共享错误通道同上；missing_content 仍保留，另保留独有 detail_error |
| sender 可选姓名 | 8 → 4 | partial／wrong／duplicate／invalid 直接检查精确 ID 对应、合法项保留、无效姓名不赋值；成功、权限拒绝、坏 stdout、真实超时仍走原进程 |
| membership 调用 | 7 → 5 | loop／duplicate 下沉；两页原生 DTO、missing、wrong_tenant 和 switch 继续真实执行 |
| image 读取矩阵 | 10 → 8 | wrong_chat／deleted 下沉；wrong_key 继续证明原生资源拒绝，format／bad_size／pixels／bytes／stream／switch 和清理继续真实执行 |

`TestUserIdentityAndSinglePageReads`、`TestMessageSearchSinglePageContract`、`TestMessageBatchNativeDetailsAndOmissions`、`TestMessageBatchUsesSingleBoundedBatch` 保留 native detail、单页／单次 mget 和不隐式展开。history、search、batch 各自保留前后身份变化后零候选的真实证明。取消／profile gate、sender 中途切换、image 下载后切换继续独立验证。

reply/send 已有 `TestReplyAdapterRejectsBeforeDispatch`、`TestSendRejectsBeforeDispatch` 等轻量输入规则测试；其余原生模式对应不同的真实派发边界，因此本批不收缩。`TestPinnedReplyTransportSinglePOSTAndCancellation` 和 `TestPinnedSendTransportSinglePOSTAndCancellation` 的 user/chat argv、精确正文、权限／guard 零派发、损坏 stdout 及派发后取消全部保留；结果不明确时不得报告未发送。发送目标的 `TestPinnedSendTargetNativeEvidence` 也完整保留：私聊 peer／租户／成员完整性、群成员资格／游标循环、禁言及缺失／未知 policy、topic／外部／解散／thread 群，以及两类候选原生搜索各自验证独有准入证据，未在没有下层承接时删除。以上成本均计入下面的同范围对照。

## 同条件计时

2026-11-20，macOS ARM64，Go 1.27.1，HEAD `e098afd` 加当前工作树。顺序执行，保留构建缓存，用 `-count=1` 排除结果缓存；改前已装上相同四槽诊断。命令均为：

```sh
go test -json -tags=integration -count=1 -timeout=4m ./internal/platform/lark
```

| 范围 | 改前 | 改后 |
| --- | ---: | ---: |
| 整个 Lark 核心＋本地集成包墙钟，含新增轻量检查和保留的 reply/send | 74.142 秒 | 59.599 秒 |
| 顶层通过／跳过 | 135／0 | 143／0 |

本次同范围墙钟缩短约 **19.6%**。只是一轮本机切片对照，不外推为全仓核心主路径提速；本批没有移标签、提高并发上限或使用结果缓存换取这项变化。

| 顶层 | 改前 wait／held（秒） | 改后 wait／held（秒） |
| --- | ---: | ---: |
| history 拒绝 | 5.313／11.283 | 3.346／6.772 |
| search 拒绝 | 6.940／17.484 | 3.217／3.346 |
| batch 拒绝 | 8.406／13.126 | 7.516／2.830 |
| sender 可选姓名 | 5.938／8.905 | 2.932／5.778 |
| membership | 2.087／2.462 | 6.726／1.833 |
| image 矩阵 | 4.796／7.331 | 3.889／5.718 |

wait／held 只用于区分四槽排队与持槽（含清理）；不包含 Go `-parallel` 暂停，也不是 CLI CPU 时间。调度顺序会变化，不能相加这些顶层或以 membership 排队增加判定行为回退。

## 验证

| 检查 | 结果 |
| --- | --- |
| 正常四槽，同范围整个 Lark 包 | 143 项顶层通过，59.599 秒墙钟 |
| `-parallel=1`，受影响规则、DTO、身份／取消及 reply/send | 29 项顶层通过，43.984 秒；这是较小选择集，不能与整包墙钟相比较 |
| `-race -tags=integration`，整个 Lark 包 | 143 项顶层通过，251.480 秒墙钟 |
| Room 调用方定向 race | 7 项顶层通过，32.554 秒墙钟 |
| `go test -tags=e2e ./... -run '^$'` | 22 包编译通过，未执行真实 e2e |
| `go vet -tags=integration ./internal/platform/lark` | 通过 |
| 完整核心＋本地集成 | 1,932 项顶层通过、9 项跳过，22 包通过、八分钟预算未达；约 12 分 48 秒（Go JSON 事件跨度），Room 包 764.639 秒 |

Room 调用方选择：`TestLarkPlatformAndMembershipReadBoundary`、`TestLarkInboxImagePreviewAndModelPrompt`、`TestLarkImageSourceScopeAndBudgets`、`TestLarkImageLifecycleCancellation`、`TestLarkReplyProductionResolvesCapturedMessageWithReadOnlyBinding`、`TestLarkSendProductionGateAndCatalog`、`TestLarkHistoricalConversationUsesCurrentCredentialForSameAccount`。没有把组件／编译结果表述为真实平台、模型或浏览器验收。

本批未运行独立全仓核心或全仓 race，也不重新执行原本未改动的真实平台／模型验收。原始 JSON 在 `/tmp/teamx-lark-matrix/`；命令、选择、每项通过记录、wait／held 和源码校验值固化于 lark-protocol-matrix-evidence.json（本地验收文件 `lark-protocol-matrix-evidence.json`），不依赖临时目录长期存在。

完整集成使用 `go test -json -count=1 -tags=integration -timeout=16m ./... -run '.'`，相当于 `make test-integration TEST_TIMEOUT=16m` 加本次运行参数。16 分钟只用于完成健康诊断，没有修改 Makefile 的八分钟默认预算。

计时驱动的初次运行将 `-json` 放进 `GOFLAGS`，被 Scion 测试内部 `go list` 继承而失败，82.460 秒时中止，不计为验收；修正为直接传参后，全部 22 包均有最终 pass、没有 fail。外层汇总脚本在子命令结束后发生 KeyError，故此次完整集成只报告 Go JSON 首末事件跨度，不冒充切片对照所用的独立 Python 墙钟。两次运行及该限制均保留在证据文件中，不因外层汇总错误重跑已完成的全量。
