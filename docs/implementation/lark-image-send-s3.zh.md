# 飞书图文发送：Agent 使用与恢复

日期：2026-11-20。对应[方案](../LARK-IMAGE-SEND-PLAN.zh.md)阶段 3。

阶段 3 已实施并完成隔离验收：指引、权限反馈、独立进程恢复、定向 Go／race、浏览器，以及托管 Room／默认助理各 12 回合的真实 DSH／Agent 均已留证。飞书侧使用本地原生 CLI 子进程夹具，没有向真实账号上传或发送；正式图文能力仍不开放。没有修改日常配置、重启现有服务或轮换日常 Session。

## 实现

- 内置飞书 Skill 补齐当前 Room 原图引用、上一张／第二张／多图选择、重名群与多组来源澄清、跨回合 History、text-only 转发、self／群不同依据、整条消息单次提交、失败与 unknown 恢复。明确使用 candidates[i].target 而非候选包装对象，并给出完整目标示例与根 CLI 的 schema help 路径。问题和结果发布到本 Room，不留在私有输出或额外发送远端说明。普通回复仍为文字；以 resolve 实际 schema 中的 images 为使用前提。
- platforms Skill 和飞书指南补充原账号图片权限修复：应用开通用户 `im:resource`、用户补全授权、重新检查及固定账号核验。资源权限不成为成员 grant，不修改其他成员或通知设置，缺图片权限不阻断原文字绑定。
- 成员能力投影新增 `image_actions`，只从已注册、未禁用的 self／群动作 schema 推导支持的图片输入；该字段描述接口支持，不能代替目标动作的 grant、self 证据或即时权限。正式注册返回空数组，隔离注册才返回相应操作名；旧回执无需迁移。
- 平台行单独显示图片上传状态及「查看图片权限」，不把可选图片缺口聚合成账号或文字动作整体失败；未开放时明确显示「图文发送尚未开放」。设置及授权卡片展示准确资源权限与官方修复入口，权限就绪不表示发送已开放。沿用既有 60 秒检查有效期。
- 含图预检错误区分 configure_application／authorize_user／retry_check／revalidate_account，保留原账号及整条未发送语义。未新增上传工具、发送 UI、日常开关或后台恢复任务。
- 托管飞书 actions 的 JSON 解码复用根 CLI 的字段诊断，返回准确未知字段路径、不回显私有值，替代不具可修复信息的笼统错误；仍拒绝额外字段和格式错误，解析失败不上传或发送。
- 修复实际恢复链路发现的只读缺口：仅 actions read/list 可在 Session 等待 unknown 核实时读取本成员账本。身份、Room／成员代次、成员变更和存储健康检查保留；发送、目标解析及 Session 重建仍受原门槛限制，不清除 unknown、不补发。新增 waiting／closing／failed 三种状态的身份、无写入和阻断回归。

没有修改 inbox／history 生产协议、图片预算、共享指令依据或正式能力注册。

## 本地验证

独立 OS 进程覆盖 self／群 × 两种存储 × 成功结算／第二张上传中崩溃／消息发送后崩溃／回读前崩溃，共 16 个场景。在暂停的原生子进程期间同 ID 再入只返回 processing。崩溃后连续两次新进程查询保留完整输入、摘要与唯一 Attempts；输入变化冲突，上传数和消息 POST 数不增加。中途崩溃保守保留 unknown，不自动恢复发送。正常取消及最终授权检查沿用托管动作实现。真实后端的正常重启回查在已结算动作下验证；存在 unknown 时保留既有 platform_settlement 阻断，另用绑定身份的独立 CLI 查询账本，不绕过 Session 恢复门槛。

补充原生应用缺权、用户缺权、检查失败的实际托管 CLI 预检，验证正确修复提示、上传与发送均为零；可选图片 scope 缺口下，原文字动作仍可发送。既有多图来源、四个上传位置失败、取消／并发／policy 变化、内容冻结、unknown 防补发和读取无副作用回归通过。

已通过的命令：

```sh
go test -tags=integration ./internal/platform/lark -count=1
go test ./internal/platform/tool ./internal/web -count=1

go test -tags=integration ./internal/room -run '^(TestLarkImages|TestLarkSelfSendIndependentProcessRecovery|TestLarkMemberCapability|TestLarkImageAvailability|TestLarkToolConfiguration|TestRoomActionImages|TestActionDispatch|TestActionInputCanonical)' -count=1

go test -tags=integration ./internal/room -run '^(TestLarkImagesManagedCLI|TestLarkImagesPermissionRecoveryKeepsTextIndependent|TestLarkImagesIndependentProcessRecovery)$' -count=1

go test -race -tags=integration ./internal/room -run '^(TestLarkImagesDispatchGuards|TestLarkImagesIndependentProcessRecovery|TestLarkImageAvailabilityRequiresDeclaredSendSchema)$' -count=1

go test -race -tags=integration ./internal/room -run '^(TestActionLedgerReadableDuringSessionRecovery|TestLarkImagesDispatchGuards)$' -count=1
go test -tags=integration ./internal/room -run '^(TestActionLedgerReadableDuringSessionRecovery|TestReferenceAction|TestActionDispatch|TestActionInputCanonical|TestToolEntry|TestPlatformEntr|TestExplicitToolBinding|TestRetiredRPCAndCLI)' -count=1
go test -tags=integration ./internal/room -run '^(TestLarkImagesBuiltinActionFieldDiagnostics|TestActionLedgerReadableDuringSessionRecovery|TestPlatformEntr|TestReferenceAction)' -count=1

go test ./internal/room -run '^(TestHistorySchemasAndProductionReadOnlyDefinitions|TestHistoryMCPModesSchemasAndNoConsumption|TestHistorySnapshotBoundAndNativePinLifetime|TestRoomAttachmentMaterialsMCPReadHistoryAndReplay|TestRoomAttachmentMaterialsMissingAndBudgets|TestLarkImageAvailabilityRequiresDeclaredSendSchema|TestLarkMemberCapabilityProjectionKeepsIntentSeparateFromReadiness)$' -count=1

node --test internal/web/tests/web_platform_onboarding_test.mjs internal/web/tests/web_member_platforms_test.mjs internal/web/tests/web_platform_connect_test.mjs internal/web/tests/web_platform_selection_test.mjs
git diff --check
```

前端四文件共 125 项通过，包含可选缺权不关闭文字开关、准确原账号修复、官方应用链接，以及权限就绪不冒充已开放。未运行全仓回归。

实际 Chromium 通过 ready／应用缺上传权限两种状态的隔离服务检查，保留正式文字 schema：平台行显示图文未开放且文字工具保持开启，设置展示正确图片权限及原应用官方补权入口；1440／390px 无横向溢出、无页面脚本错误。没有浏览器授权或平台写入。截图位于本机 `/tmp/teamx-lark-image-browser-evidence-final/`；自动化验证不是实体手机验收。

```sh
NODE_PATH="$PWD/packaging/verifier/node_modules" \
TEAMX_LARK_IMAGE_SETTINGS_BROWSER=1 \
TEAMX_LARK_IMAGE_BROWSER_OUTPUT=/absolute/browser-evidence-directory \
go test -tags=e2e ./internal/room -run '^TestLarkImagePermissionSettingsBrowser$' -count=1 -v
```

## 真实 Agent 证据与范围

测试 `TestLarkImagesRealAgent` 使用当前已配置模型、已安装 DSH、真实成员调度、HTTP MCP Inbox／History、内置 Skill、实际 TeamX CLI tools run、固定账号原生 CLI 子进程夹具和动作账本。原图通过实际 HTTP 上传／发布进入 Room；模型显式 text-only，以验证按引用转发不依赖识图。覆盖普通托管 Room 和默认助理私信，飞书侧没有真实账号凭据。

两种入口各完成 12 回合：单图、跨回合双图、只选第二张、重名群澄清后图文、来源澄清、已结算状态下服务重启后查询、上传失败、取消、unknown 原回执查询及缺图片权限反馈。每个入口有 4 条 succeeded、2 条 not_applied、1 条 unknown；最多一次尝试，每条完整内容和原图顺序均准确。各有 5 次原生消息 POST（包含一次注入 unknown 的 POST），失败和取消没有额外消息。问题与结果实际发布到 Room。

unknown 之后再次重启，Session 按既有 platform_settlement 阻断；独立的真实 CLI 子进程使用绑定成员身份仍可读取完整 unknown 回执，Attempts 保持 1，无新增上传或发送。测试保留正式角色／服务／执行入口，只为隔离任务限定 TeamX CLI 和已提供的 Inbox／History 工具；允许整理返回 JSON／stdin，不允许磁盘搜索、裸平台命令或替代账号。没有验证通用 MCP 资源发现工具或任意模型都能可靠完成这些任务。

本次真实运行结果保存在[证据摘要](lark-image-send-agent-evidence.json)：

| 模型与入口 | 实际结果 |
| --- | --- |
| deepseek-v4.1-flash-1017，托管 Room | 最终 managed 独立子测试 PASS，12 回合，重启后的真实模型／工具调用也纳入审计 |
| deepseek-v4.1-flash-1017，默认助理 | default-assistant 子测试 PASS，12 回合，原角色与本人／群边界保持 |
| 历史失败样本 | 早期限流、qwen3.8-flash 首回合超时、MCP 资源发现后的 ACP 协议错误，以及已修正的夹具／指引问题均保留失败记录，不计入通过样本 |

通过证据按入口复用：默认助理子测试来自一次两入口运行，其中旧 managed 审计误判了 stdin 过滤，所以该次父命令整体失败；随后 managed 独立运行 PASS 补齐矩阵。没有将历史失败父命令改记为 PASS，两个入口的通过范围分别由各自完整子测试断言。

装配复用正式工具目录与内置 Skill，原生夹具按冻结动作的实际附件引用校验原图，并提供完整群元数据。修复了只读账本准入和错误字段诊断；没有放宽发送授权、图片字节校验或 Session 重建门槛。真实 Agent 审计在自动后端恢复后继续保留，完整记录新旧 Session 的 24 个真实回合。

重新验证命令如下；选择其他模型只修改本次隔离测试，不写回日常配置：

```sh
TEAMX_LARK_IMAGE_AGENT_REAL=1 \
TEAMX_INPUT_COMPARISON_CONFIG="$PWD/.teamx/config.toml" \
TEAMX_DSH_OUTPUT=/absolute/fresh/evidence-directory \
go test -tags=e2e ./internal/room -run '^TestLarkImagesRealAgent$' -count=1 -timeout=35m -v
```

可用 `TEAMX_LARK_IMAGE_AGENT_MODE=managed` 或 `default-assistant` 单独运行；`TEAMX_LARK_IMAGE_AGENT_MODEL_ENTRY` 选已有模型条目。测试从安全凭据存储读取所选条目，不要求在命令行粘贴 key。未知或失败样本只留证，不向真实飞书重放。

阶段 2 真实接口小验证、阶段 4 真实飞书回读与回流验收仍需准确授权；本阶段通过的原生夹具、进程、浏览器及真实模型证据不能替代真实飞书接口和最终开放验收。
