# 飞书图文发送：完整托管动作

日期：2026-11-20。对应[接入方案](../LARK-IMAGE-SEND-PLAN.zh.md)阶段 2。

本阶段接入 Room 原图来源与 self／指定单群的一条完整图文动作。含图能力只由隔离 manager 注入；正式注册仍使用原文字能力，普通回复继续拒绝 images。未修改日常配置、重启服务或轮换 Session。

## 实现与边界

- 飞书 self／群共用严格的 body + images schema、内容摘要、上传器及回读校验。最多四个不同附件引用，保留准确文字与图片顺序；单图无文字使用 image，其余含图输入使用一个 post。Agent 不能提供路径、URL、Base64、平台 key 或原生 post。
- Inspect 先核验目标与上传权限，再懒加载阶段 1 的原图 lease；Execute 复用同一份来源。完整 Request.Input 和唯一 Inspection.content_digest 落盘、原尝试检查通过后才开始上传。
- 固定 user 账号按序上传全部原始字节；每个上传使用私有临时目录内的固定相对文件名。每张图之前检查取消、绑定、成员／Session 与来源，不重复完整账本或 policy 扫描。两种目标沿用账号级限额，本人沿用串行化。含图托管调用总期限 180 秒，单个 CLI 子进程仍最多 30 秒，并受更早 deadline 限制。
- 唯一消息 POST 前，重新核验 self 关系或群成员／发言条件，并按准确动作 ID 只读复核完整请求、账号／目标、可信摘要、回合及当前授权。上下文存在时读取准确事项，不投影或扫描所有动作；复核不会增加 Attempts。
- 消息 POST 尚未开始的失败结算为 not_applied；已上传但未引用的资源可能保留。POST 开始后的异常或回读不一致结算为整条 unknown，保留已知远端 ID，不能拆图或换 ID 自动补发。成功只返回一个远端消息 ID。
- 结构化输入按完整内容比较，并规范化嵌套 JSON 字段顺序。摘要使用 version=1、准确 body、有序实际图片 SHA256 的规范 JSON 再取 SHA256；同字节的不同源引用仍属于相同发送内容。既有来源／操作／入口 unknown 保护继续存在，新增同账号／准确目标的完整摘要比较。旧文字记录不迁移，成功回执查询不再取原图。
- `image.upload` 独立依赖 user `im:resource`，纳入默认检查及应用已开通权限的授权交集。缺图权限不会阻断文字绑定和发送；预检经托管 CLI 返回明确原因。界面只增加「图片上传权限」标签，权限就绪不代表图文发送已经开放；更完整的能力／修复交互与内置 Skill 留在阶段 3。actions help 依 resolve 实际 schema 说明隔离图文调用合同。

未改 inbox／history 协议、消费状态、模型材料或图片预算；未扩展共享指令／问答依据、普通回复、多群广播或通知来源图片。

## 验证

已通过的核心与托管测试覆盖：严格输入／正式注册边界、文字及四张图的完整回读、两种目标的单图／多图／图文、四个上传位置分别失败且消息 POST 为零、成功仅一个 ID、同 ID 内容变化冲突、unknown 换 ID／等价原图来源阻断、原图删除后的成功回执查询及新动作拒绝。完整 Store 文件摘要对比确认图文动作不修改原生读取、消费与 History 状态。

两种存储均覆盖上传暂停期间清空 busy、并发追加、同 ID 返回既有 processing、取消、冻结摘要／输入替换及 policy 改变；取消或最终复核失败均无消息 POST，Attempts 保持 1。持久意图写入失败、尝试记录失败及意图提交不确定均不上传，并释放全部来源 pin。适配器测试拒绝缺图／乱序／额外文字、作者／目标／类型变化与异常 thread；已知 ID 在核实失败时保留。

验证命令（本地完成；未运行全仓测试）：

```sh
go test -tags=integration ./internal/platform/lark -count=1

go test ./internal/room -run '^(TestRoomActionImages|TestRoomAttachments|TestRoomAttachmentMaterials|TestHistory|TestReferenceAction|TestActionDispatch|TestActionInputCanonical|TestLarkSelfSend|TestLarkMemberCapability|TestLarkToolConfiguration)' -count=1

go test -tags=integration ./internal/room -run '^TestLarkImages' -count=1
go test -tags=integration ./internal/room -run '^TestLarkImagesLedgerFailureReleasesSource$' -count=1
go test -tags=integration ./internal/room -run '^TestLarkImagesManagedCLI/(self|group)/caption$' -count=1

go test -tags=integration ./internal/room -run '^(TestLarkSelfSendManagedCapability|TestLarkSelfSendRevocationRaces|TestLarkSelfSendConcurrentRoomsAndMembers|TestReferenceActionInternal.*|TestRoomActionImagesManagedActionContextAndCleanup|TestRoomAttachmentMaterialsServiceAssembly|TestRoomAttachmentMaterialsSelectedRangeAndLifecycle|TestHistoryMCPImagePositionsDedupAndIndependentBudget|TestHistoryRoomVisibilityFrozenPagesAndNoWrites)$' -count=1

go test -race -tags=integration ./internal/room -run '^TestLarkImagesDispatchGuards$' -count=1
go test -race ./internal/room -run '^(TestActionDispatch|TestActionInputCanonical|TestRoomActionImagesOriginalsAndRetention)' -count=1
go test -race ./internal/room -run '^TestRoomActionImagesValidateActualFormatDimensionsAndTotal/pixels$' -count=1

node --test internal/web/tests/web_platform_onboarding_test.mjs internal/web/tests/web_member_platforms_test.mjs internal/web/tests/web_platform_selection_test.mjs
git diff --check
```

第一条托管命令覆盖当时已有的完整动作、权限及 guard 测试；后两条分别补验新增的落账失败，以及原图删除后的原回执与新动作。前端三个文件共 95 项通过，未做浏览器或真实服务 UI 验收。

核心 race 首轮同时运行飞书／Room 测试：飞书适配器通过，Room 原有大像素 PNG 样本准备超过 fixture 的 10 秒 deadline；该项隔离重跑通过（4.578 秒），没有修改测试期限或协议预期。最终内容复核、原图保留及托管并发／取消 race 定向测试通过。

平台 API 返回值由本地 CLI 子进程夹具提供；不是真实飞书接口证据。构造器／回读器只接受严格匹配的 image，以及单一 zh_cn 包装或无 locale 包装的 post。夹具验证换行、空行、中文和字面 Markdown／@ 原样保留；额外标题／locale／可见节点、缺图、乱序、作者／目标／类型不符均不成功。

## 仍待验证及后续阶段

方案第 8.2 节的真实接口小验证未执行：没有本次真实写授权，也未将此前文字验收视为图片上传／发送授权。真实上传 key 与回读 key 的关系、实际 post 空行及默认字段形态仍须以明确授权的隔离样本确认；当前精确 key 校验不将未知关系当作等价，也不放宽不一致的回读结果。

阶段 3 的真实 DSH／Agent 决策与恢复、独立进程图片动作故障验证、完整设置修复反馈和内置 Skill，以及阶段 4 的真实本人／群发送与正式开放均未执行。阶段 1 的来源／读取验证、阶段 2 的托管／子进程夹具证据，不能代替这两层验收。
