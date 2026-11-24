# 飞书图文消息发送接入方案

状态：阶段 1～4 及第 8.4 节 Agent 接口与指引简化均已实施。阶段 4 的真实 DSH／飞书验收及六样本回流检查通过，正式 self／指定单群图文 schema 与托管执行已开放；普通回复保持文字。实际在批准范围内发送 7 条消息、上传 12 次，含修复后的额外多图复验；历史 unknown 保留。阶段记录见[阶段 1](implementation/lark-image-send-s1.zh.md)、[阶段 2](implementation/lark-image-send-s2.zh.md)、[阶段 3](implementation/lark-image-send-s3.zh.md)、[接口简化](implementation/lark-image-send-agent-simplification.zh.md)及[阶段 4](implementation/lark-image-send-s4.zh.md)。代码注册与日常部署分别记录；未重启日常服务或轮换现有 Session。本文不授权额外真实写入。

设计基线：2026-11-20 当前工作区；本机 lark-cli 1.0.99。实施前复核相关入口及 CLI schema，保留其他任务的工作区改动。

读取边界复核结论：inbox／history 协议不变；原生快照／保留 pin 下沉为 Store 私有能力，并补齐内嵌存储的清空保护。依据、文件边界和实现顺序见第 4.1～4.3、5.1 节。

实现原则：复用完整动作记录冻结内容，只新增一份可信内容摘要，不扩展共享指令依据结构；上传期间采用轻量检查，最终消息发送前集中复核完整授权。保留完整图文与可靠回执，不增加通用媒体框架或恢复状态机。

Agent 接口原则：在必要信息完整的前提下，工具输入、返回与 Skill 言简意赅。业务选择由 Agent 完成，身份与资源核验由服务端承担；以实际调用次数、必填参数、格式试错及指引篇幅检验简化效果。

## 1. 目标、首期范围与后续路线

用户在 Room 发送图片后，可以要求“把上面的两张图发到我的飞书”，或“把这段说明和两张图一起发到项目群”。Agent 使用当前成员已绑定的飞书用户账号，向准确的本人自聊或指定单群发送一条完整消息，并核实该消息的文字、图片及目标。用户无需下载图片、提供服务器文件路径或手动转发。

“原图”表示上传 Room 保存的原始图片字节；TeamX 不做 OCR 替代、重新绘制、压缩或格式转换，不承诺飞书客户端展示或下载时不再处理图片。成功表示远端消息已核实，不表示用户已读。

| 范围 | 本方案安排 |
| --- | --- |
| 当前 Room 已发布的人类图片附件 → 飞书本人自聊或指定单群 | 首期完整交付；覆盖托管 Room 中的普通会话和默认助理私信，目的地沿用现有 self／group 目标解析 |
| 纯文字、单图、多图、文字加图片 | 首期使用统一 body + images 输入；一次动作对应一条远端消息，图片顺序与数组一致 |
| PNG、JPEG、WebP | 沿用 Room 已接受的格式与硬上限；另满足飞书上传限制 |
| 已有文字本人投递、群发送、普通回复 | 兼容旧 body 输入；本人和群沿用各自授权边界，普通回复仍只支持原有文字 |
| 同类 Room 图片 → 普通消息回复 | 后续扩展，补回复目标的内容授权与真实验收 |
| 飞书／GitLab 通知来源图片 → 飞书 | 后续扩展，增加来源账号、原通知／原消息归属及下载权限核验 |
| 任意段落与图片交错、标题／Markdown／多语言、文件／视频／语音 | 后续扩展；首期图文固定为“说明文字在前，图片按序在后” |
| 多群批量发送 | 不纳入首期；一次动作只接收一个准确目标，不自动广播 |
| 任意其他人的主动私信、机器人发图 | 不随本次开放，继续遵守原目标和身份边界 |
| 单 Room 独立运行模式 | 不新增平台绑定／发送设施；首期沿用现有托管飞书动作的可用范围 |

首期不新增通用附件管理平台、上传工具、发送页面或后台重试队列，不允许 Agent 传入磁盘路径、URL、Base64 或任意飞书 image_key。后续范围保留，但不能因为共享上传器可用就提前在生产 schema 中开放。

“指定单群”沿用当前 [SendGroup](../internal/platform/lark/send_targets.go) 的可用范围：同租户、非外部、正常状态、普通 group／chat 类型、发言策略为 all_members，且已核实绑定用户在群内。外部群、话题群、特殊发言策略及成员核验不完整的群不在首期开放；不因本次图文接入扩展这些目标规则，也不要求群被通知订阅。

## 2. 已确认的问题与可复用基础

飞书／lark-cli 已支持图片及富文本消息，当前缺口在 TeamX 的发送封装。本机只读检查已确认：`im +messages-send --help` 有 `--image` 和 `--msg-type post --content`；`schema im.images.create` 声明支持 user／bot，用户上传需检查资源权限。通过 post 可以把文字和多张图片放在同一条消息中。官方说明见[消息发送](https://github.com/larksuite/cli/blob/main/skills/lark-im/references/lark-im-messages-send.md)及[IM 能力](https://github.com/larksuite/cli/blob/main/skills/lark-im/SKILL.md)。这些是接口能力证据，尚不是 TeamX 图文发送的真实验收结果。

| 当前入口 | 现状与本次增量 |
| --- | --- |
| [self_send.go](../internal/platform/lark/self_send.go)、[send.go](../internal/platform/lark/send.go) | self／指定群已有文字动作，发送和回读固定 text。两者统一支持含图消息，目标及授权检查仍分开 |
| [platform_lark_actions.go](../internal/room/platform_lark_actions.go) | 已负责账号固定、绑定检查、取消及发送器装配；在此组合 Room 图片来源与飞书上传发送 |
| [tools_builtin.go](../internal/room/tools_builtin.go) | 飞书是受控 read／actions 分发，不透传原生 CLI；继续保持 |
| [followups_actions.go](../internal/room/followups_actions.go) | ActionInput 已保留适配器自定义 JSON；复用，不给公共输入结构硬编码图片字段 |
| [actions_store.go](../internal/room/actions_store.go)、[followups_actions_service.go](../internal/room/followups_actions_service.go)、[followups_actions.go](../internal/room/followups_actions.go) | 已有完整 Request.Input、写前落账、稳定幂等键和 unknown 阻断；补一份整条消息摘要及最终发送前的一致性检查，不扩展共享指令依据 |
| [room_attachments.go](../internal/room/room_attachments.go)、[room_attachment_storage.go](../internal/room/room_attachment_storage.go) | 已有图片对象、SHA256、字节校验、保留与对象 pin；复用存储，不复制为长期发送缓存 |
| [room_attachment_materials.go](../internal/room/room_attachment_materials.go)、[history.go](../internal/room/history.go) | 模型取图绑定正式页／History，保持原样；仅从 History 抽出原生快照读取和保留 pin，供动作独立取图，具体边界见第 5.1 节 |
| [agent_read_views.go](../internal/room/agent_read_views.go)、[input_stream_body.go](../internal/room/input_stream_body.go) | 正式读取和 History 已返回消息 seq、附件 ID 与摘要；Agent 直接选用这些字段，不增加必需的附件 resolve 调用 |
| [permissions.go](../internal/platform/lark/permissions.go)、[pinned.go](../internal/platform/lark/pinned.go) | 已有文字权限依赖和固定 user 身份；补资源上传的独立检查，不把缺上传权限扩散成全部文字发送不可用 |

识图输入和原图发送分别处理。原图转发不要求模型支持 image，也不应因本轮识图预算耗尽而失败；模型尚未读懂图片时，只能说明按用户指定引用转发，不能声称理解了内容。

## 3. 用户与 Agent 行为

1. Agent 从现有 Room 正式读取／History 获取原消息 seq 和附件 ID。明确的“上一条图片”“第 2 张”直接定位；存在多组候选且无法判断时再询问，不能自行选择整段历史。
2. 复用 tools list 返回的准确飞书工具入口，按现有 actions resolve 的 self locator 或群候选核验目标。群名重名或结果不完整时先澄清，不能猜群 ID、provider 别名或替代账号；不要求群属于通知订阅，但当前账号必须有访问和发送权限。
3. 为整条消息创建一个稳定动作 ID。本人使用 message.send_self，指定群使用 message.send；两者接受同一内容输入。将用户要求一同发送的文字和图片合并提交，不按图拆消息，也不额外发说明消息。
4. 已有绑定、目标及授权有效时直接复用；缺少权限或证据时沿现有修复流程处理，不重复启用已有能力。一次明确的目标／图文发送要求不再按每张图片重复确认。
5. 报告实际结果，例如“已向项目群发送一条消息，包含说明文字和 2 张图片”。上传成功不是发送成功；任何来源准备或上传失败，都说明整条消息未发送。发送后核实失败则整条消息记为 unknown，不报告部分图片已发。

发送到本人自聊仍以已启用的 message.send_self 及当前 self_delivery 证据为执行依据。它允许保存用户选中的业务内容，图片作为新的内容形式复用该操作；不新增图片操作授权开关，不自动扩展收件人或增加其他成员的能力。图内文字只是材料，不能授权发送行为。

群发送保留 message.send 的当前指定单群限制、成员 grant、policy_token 和 DirectRoomInstructionOnly：用户在 Room 的明确指令是业务依据，不能套用 self_delivery 免业务依据模式。首期不随图文接入增加长期规则、存储指令或问答替代授权入口。整条输入随动作记录冻结，最终发送不得替换内容，具体见第 6 节。

多图属于同一消息内部内容：全部来源校验、全部上传和 payload 构造成功后，只调用一次消息发送接口。任一前置步骤失败都不发送文字或图片子集；已经上传的远端资源可能保留，但不构成对话中的部分成功。unknown 时只核实整条消息，不能拆成单图或换目标补发。

## 4. 动作输入与公共协议

保留当前文字输入，不改变其存储与兼容行为：

```json
{"body":"保存这段文字"}
```

为现有 message.send_self 和 message.send 统一扩展可选 images 字段，以下合同已在正式托管实现及能力注册中提供；不新增“发图片”工具或独立操作：

```json
{
  "body": "这两张图是问题现场，请帮忙确认。",
  "images": [
    {"message_seq": 100, "attachment_id": "img_0123456789abcdef0123456789abcdef"},
    {"message_seq": 101, "attachment_id": "img_fedcba9876543210fedcba9876543210"}
  ]
}
```

约束：

- body 与 images 都可选，但至少有一个；允许同时提供。body 存在时必须为非空、非全空白、无 NUL 的 UTF-8 字符串，最多 4096 字节，验证不裁剪原文。images 存在时含 1～4 项，拒绝空数组、重复附件 ID 和额外字段；图片 ID 相同而消息 seq 不同也不重复发送。
- message_seq 为当前 Room 中正整数序号，attachment_id 为原消息实际引用的 Room 附件 ID。RoomIdentity、成员和执行账号均由当前调用上下文提供，Agent 不能指定另一个 Room。
- self 与群共用相同的内容 schema，各层 additionalProperties=false；普通回复的首期 schema 仍为文字，手工提交 images 同样拒绝。
- body 为准确文字，不自动解释 Markdown、图片链接、@ 提及或客户端路径；图片来自 images，不能隐藏在 body 中触发额外上传。首期版式固定为文字在前、图片按数组顺序在后，不引入通用内容块编辑协议。
- 完整 actions execute 信封、target_ref、turn_number 和稳定 id 沿用现有合同；self 与群的 authority 按各自动作填写。帮助与内置飞书 Skill 增加图文示例，不修改 MCP 工具数量或新增长链路前置步骤。
- 同一 ID 的 input、目标或依据发生变化按现有冲突处理；旧文字动作和回执保持可读，不做旧数据迁移。

完整调用示意如下。TOOL_ENTRY、target_ref、turn_number、message_seq 和附件 ID 均须使用实际读取／解析结果；id 在首次提交前确定，后续查询和重试保持一致：

以下为第 8.4 节简化后的调用合同，内容、授权及回执语义保持不变。本人先以 `locator:{self:true}` 解析；按群名查找时使用 `locator:{query:{object_type:"chat",text:"群名"}}`，将返回的 `resolution.candidates[i].locator` 整体作为下一请求的 locator，在同一入口重新核验。每次请求使用当前 turn_number，群 policy_token 使用当前运行时值。已有有效 target_ref 可以直接复用。两种动作均可在原托管入口追加 `--help` 查看同一请求合同。阶段 4 使用此合同的最终验收结果。

```sh
"$TEAMX_CLI" tools run TOOL_ENTRY actions execute --request '{
  "id": "save-room-message-100-101",
  "target_ref": "ref_FROM_SELF_RESOLVE",
  "turn_number": 7,
  "operation": "message.send_self",
  "input": {
    "body": "这两张图是问题现场，请帮忙确认。",
    "images": [
      {"message_seq": 100, "attachment_id": "img_0123456789abcdef0123456789abcdef"},
      {"message_seq": 101, "attachment_id": "img_fedcba9876543210fedcba9876543210"}
    ]
  }
}'
```

该示例沿用本人能力依据，不添加 policy_token 或虚构 authority。发到指定群时，内容输入保持一致，target_ref 使用已核实的群目标，并按现有合同补齐业务依据：

```json
{
  "id": "send-project-message-100-101",
  "target_ref": "ref_FROM_GROUP_RESOLVE",
  "turn_number": 7,
  "operation": "message.send",
  "policy_token": "CURRENT_POLICY_TOKEN_FROM_RUNTIME",
  "authority": {
    "type": "human_reply",
    "room_seq": 102,
    "reason": "按这条 Room 指令向已核实的项目群发送所选说明和两张原图"
  },
  "input": {
    "body": "这两张图是问题现场，请帮忙确认。",
    "images": [
      {"message_seq": 100, "attachment_id": "img_0123456789abcdef0123456789abcdef"},
      {"message_seq": 101, "attachment_id": "img_fedcba9876543210fedcba9876543210"}
    ]
  }
}
```

policy_token 是占位，实际必须使用运行时返回的当前 token；authority.room_seq 是发群指令，images[].message_seq 是图片来源，两者可以不同。一个动作始终只有一个目标和一个远端消息 ID。

| 输入内容 | 远端类型与版式 |
| --- | --- |
| 仅 body | 沿用 text 消息 |
| 仅一项 images | image 消息 |
| body + images，或仅多项 images | 一条 post 富文本，包含可选说明和全部有序图片 |

### 4.1 inbox／history 协议核对结论

**首期不修改 inbox／history 的请求、响应、确认、游标或存储协议。需要增加的是 action 的图片输入，以及服务端原生快照／保留保护的少量实现复用。** 以下结论已对照当前代码逐项核实。

| 实际路径 | 已有证据 | 设计决定 |
| --- | --- | --- |
| inbox_read → prepareRoomPage → projectRoomMessage → projectInputBody | inputRoomBody 含 seq 和 attachments；附件摘要含 id、kind、mime_type、size、width、height | 直接使用 entries[i].seq + entries[i].attachments[j].id，无需增加 image_ref、下载地址或发送凭据 |
| history_read → historyReadPage → historyEntry | Room 分支调用同一个 projectRoomMessage／projectInputBody，保留上述原生标识 | 已消费消息只要仍保留即可选图，不新增 History 选择／授权接口 |
| HTTP MCP | inputPageResult.mcpResult 输出正文，再附可选图片内容块 | 原始附件引用属于正文，与 MCP 图片块序号无关 |
| CLI Inbox | serveInputCLI 使用 UseNumber 解码正文，保留 entries／attachments；图片块另附在 content | HTTP／CLI 共用同一图片动作输入，不能要求 CLI 增加传文件步骤 |
| 模型不支持图片、图片预算耗尽 | prepareInputMaterials／historyReadMaterials 只改变图片覆盖说明及内容块，消息的附件摘要仍在 | 允许按明确引用转发；不扩大模型预算，不要求先识图 |
| 正式读取确认、重放、历史分页 | ConfirmInput、teamx/received、固定结果、History cursor 各有原有职责 | 发送不调用它们，不建立第二条消费或确认路径 |

注意三个标识的区别：附件 ID 来自 attachments[].id；history 的 images[].content_index 是当前返回帧中的图片块位置；inbox 的 images[].positions／shared_room_seq 是识图覆盖和复用说明。后两者会随预算、去重和分页变化，不能作为发送动作的图片身份。

读取不是发送许可。动作重新核对“当前成员仍可访问这些保留的原生消息及附件”，执行权来自相应成员动作绑定及 self／群的不同授权依据；**不要求提供本次读取的 read_id、receipt、cursor、stream_ref 或 MCP token**。这既支持跨回合历史引用，也避免把图片外发耦合到正式页是否尚未确认。任一原消息已清空或原图不可用时整条拒绝；即使旧模型页面缓存中仍有图片，也不能借此恢复发送来源。

### 4.2 明确保持不变的范围

以下生产实现不因接入发图而修改：input_stream_transport.go、input_stream_body.go、input_stream_room.go、input_stream_materials.go、input_stream_cli.go、input_stream_cli_http.go、history_mcp.go、history_materials.go、room_attachment_materials.go，以及 internal/inputstream 的读取／协调器合同。保持现有 MCP 工具定义、HTTP 四工具／CLI 两个 History 工具注册方式，已退役的 teamx read 不恢复。

不增加或修改正式页、ReadAttempt、input-state、MaterialSeen、MaterialImages／MaterialBytes、History 查询缓存／预算、Room ReadThrough／ReadProofs 或新的持久附件访问凭据。动作可以使现有消息保留 pin 临时增加；这不属于消费状态，也不形成新的协议版本。

history.go 仅允许第 5.1.2 节规定的私有快照／pin 函数委托调整；读取选择、可见性、分页、原错误分类和输出不变。原公共 Agent guide 与 MCP 描述无需修改，图片说明放在按需读取的内置飞书 Skill／actions help 中，避免仅为发图改变所有 Session 的冻结指令／工具指纹。部署仍遵循既有 Session 管理规则，不自动轮换日常 Session。

### 4.3 读取相关代码的具体改动边界

| 文件 | 允许的增量 | 不应扩展到的内容 |
| --- | --- | --- |
| 新增 room_retained_read.go | Store 原生快照捕获、锁外正文读取、保留 pin 检查与释放 | 输入消费、History 查询状态或平台权限 |
| history.go | pinHistoryRoom／historyRoom 委托底层方法，保留现有签名、bound 和错误映射 | schema、目录／选页、cursor、缓存／预算、MCP framing |
| storage.go | SaveRoom 对两种存储都检查现有原生保留 pin | Room 消息结构、InputLog 发布合同、读游标或新增持久文件 |
| input_stream_runtime.go | roomInputProtected 的现有 pin 检查委托同一底层方法，其余清空限制不动 | input owner、协调器、正式页／确认算法 |
| room_attachment_storage.go／room_attachment_materials.go | 直接复用已有元数据／字节原语，不要求修改 | 人类预览权限、模型 Page／Range 证据及下载预算 |
| 新增 room_action_images.go、修改 platform_lark_actions.go | 图片动作自己的来源 lease 和平台装配 | 把动作登记成一个输入流，或从 History 获取授权 |
| followups_actions_service.go | 新动作校验后传递私有 action 调用上下文；已有回执快路径不动 | 新增读取步骤、修改任何 MCP 或 Agent 请求信封 |

该边界是首期实现约束：若出现必须修改第 4.2 节读取协议／协调器的实现方向，应首先对照本节检查是否误用了模型材料接口，而不是直接把图片动作需求扩散进读取主流程。

## 5. 分层与执行链路

```text
Agent 确认目标、准确文字和全部有序附件引用
  → 现有 actions.execute / message.send_self 或 message.send
  → 目标、基础权限和动作授权预检
  → Room 服务一次性核对全部源消息、可见性和图片对象
  → 飞书 Inspect 返回整条消息内容摘要（只读）
  → 现有账本持久化完整 Request.Input、原授权依据和一份内容摘要
  → 固定账号按序上传全部图片
  → 构造唯一 image／post payload，集中复核完整动作与当前授权
  → 固定账号只调用一次消息发送
  → 按 message_id 回读核验
  → 持久化整条消息回执 → Agent 报告完整结果
```

### 5.1 Room 拥有图片来源

新增 room_action_images.go，负责“当前成员可发送的 Room 图片”。它直接读取原生保存对象，独立于模型材料链路；不是调用 History MCP 再解析其输出，也不是给 materialAttachments 增加第三种授权来源。

#### 5.1.1 当前执行身份从动作入口取得

当前 referenceActionPreparer 只接收 context、MemberIdentity、AccessContext，平台 TargetActionRequest 本身没有 turn_number。这是必须补齐的内部装配信息，不能假设图片回调天然能取得当前 inputSession。

选择 Room 包内的私有 action 调用上下文，不扩展 Agent 或 MCP 信封：

1. service.executeReferenceAction 保留现有 existingReferenceAction 快路径。已有 ID 直接返回原回执，即使源图片已清空也不重新取图或上传。
2. 新动作在 referenceActionEvidence 核对当前 running turn 后，将 ServiceInfo、MemberIdentity、q.TurnNumber 和 q.ID 写入不可由客户端构造的私有 context 值，再调用原 prepare。动作 ID 用于写前回查已落账依据；该值不包含图片字节或读取回执。
3. platform_lark_actions.go 的 self／群含图消息 Inspect 包装器读取此上下文，首次准备来源时调用 executionBindingForTurn(id, turn)，捕获实际 turnExecution（Generation、Turn）。resolve、tools list 和纯文字动作不要求存在此图片上下文。
4. 后续上传前／发送前复核同一成员、运行回合、服务及 Session 代次。复用当前平台调用取消、成员变更／轮换取消与 session.Check，不以 inputStorage 或 newInputSession 生成另一份执行状态。

现有 preparePlatformCallLocked 创建的 callCtx 派生自传入 ctx，Lark merge 又派生自 callCtx，能够保留该私有值；实现测试须覆盖这条实际上下文传递，不能在测试中手工塞一个模型 inputSession 就算接入成功。

#### 5.1.2 只下沉原生快照与保留保护

新增 room_retained_read.go，属于 Room 原生存储读取底座。将 history.go 中 pinHistoryRoom／historyRoom 的“读取已发布 root、固定保留边界、读取正文”下沉为 Store 私有方法，原 History 方法保留签名并委托调用。建议内部合同如下，名称可按实际代码调整：

```go
// Captures the native publication, immutable cache and retention pin under Store.mu.
func (s *Store) pinRoomPublication(id MemberIdentity, bound *inputstream.Snapshot, metadata bool) (RoomSnapshot, *roomLogCache, func(), error)

// Reads/validates the already pinned publication outside Store.mu.
func (s *Store) readPinnedRoom(room RoomSnapshot, cache *roomLogCache) (RoomSnapshot, error)
```

pinRoomPublication 保留当前身份／成员检查、可选 bound 检查、metadata 模式下的原生历史索引选择和幂等 release；不接受 inputSession，不读 input-state，也不创建 History 查询。History 继续提供原 bound，动作以 nil bound 捕获当前保留 root、metadata=false。底层保留失效错误由 History 包装器映射回现有 snapshot_expired，不能改变已有 cursor 失效语义。

readPinnedRoom 使用现有 readRoomLogView 和捕获的不可变 roomInputCache，在锁外读取／验证 InputLog；内嵌 Messages 直接使用捕获的 root。不要改为持 Store.mu 调用 loadRoom／LoadRoom 完整解码分段日志；也不要新增随机访问索引、正文副本、来源注册表或新 Actor。内嵌格式本身仍需按既有方式解析一次完整 root，不把它宣称为常数时间元数据读取。当前日志读取自身有界，本次沿用该容量与缓存机制，不承诺读取单图就是 O(1)。

**已确认的差异：当前 SaveRoom 只在 InputLog != nil 分支调用 roomInputProtected，内嵌 Messages 的清空不检查该 pin。** 为兑现两种存储的发图保护，需要一处显式增量：把 roomHistoryPins 的计数检查抽为原生保留检查，在 SaveRoom 两种存储发布路径都执行；input_stream_runtime.go 的 roomInputProtected 继续复用它，正式页／input-state 的其他清空限制仍仅按原条件执行。保留原 roomHistoryPins 字段，避免全包重命名，也不新增动作专用 pin 表。

这会让内嵌存储在原生保留 pin 存活期间也返回已有 ErrBusy。它是存储生命周期补齐，不是 inbox／history 协议变化；不要顺带改变清空进度、自动延迟执行清空或加入新的重试调度。

#### 5.1.3 整条消息的来源租约与装配顺序

来源返回 action 私有 lease，包含捕获的 turnExecution、有序图片集合（源消息／附件描述、原始字节、MIME、SHA256）以及统一 Check／Close。一次动作只取一个 Room 原生快照，并在其中验证所有来源；不为每张图创建 inputSession 或 History 查询。它仅在本次 referenceActionSession 内存在，不写入 input-state、History 缓存或 Agent 响应。

1. 通过第 5.1.2 节取得带保留 pin 的原生快照，在锁外解码正文。对所有来源验证消息 seq 仍保留、roomVisible(..., true)、roomDeliverable、Author.Kind=human、Event=nil、Source=nil，并查找完全匹配的附件 ID。不能用只含摘要的历史索引代替正文。
2. 按 attachmentLife → Store.mu 的既有顺序，逐个读取 attachmentMeta，核对完整服务器附件描述、RoomIdentity、meta.Operator 与各原消息作者、对象不在 active／unconfirmed 状态且可用，然后增加对象 pin。所选正式消息本身就是保留引用；不能仅因对象还在草稿 TTL 内就放行。
3. 释放 Store.mu，在 attachmentLife 的短文件读取区间调用现有 readAttachment，逐个验证字节大小及 SHA256，再用既有图片校验器验证 MIME／像素并比对描述，同时检查整条消息总字节上限。任一失败释放已取得的全部资源；文件阶段结束即释放 attachmentLife，对象 pin 和单个 Room 消息保留 pin 保留至 lease.Close。
4. Check 依靠现有 session.Check／平台取消门核对当前服务、成员和绑定；对已捕获身份的安全固定路径读取并校验 agent／session 私有元数据，核对准确的运行回合与 Session 代次。正文在该保留 root 内不可变，不在每个 guard 内重读 room.json 或解码全历史。现有 runningTurnLocked 会经过 memberPath→loadRoom，不能把它作为新增的频繁整日志扫描路径；局部元数据复核不得省略文件身份、完整结构和状态检查。
5. self／群含图消息 Inspect 先沿用目标／授权／权限预检，再懒加载一次完整 lease，并将准确 body 和有序图片摘要合成第 6 节的 content_digest。Execute 复用同一 lease，不重新选图或重新创建读页；与 request 的稳定幂等键和完整 input 对应。纯文字或前置权限失败不分配 lease，读取完全部图片并不代表群动作业务授权已落账，最终仍须通过 beginReferenceActionWithBasis。
6. 每张上传前只组合 session.Check 与 lease.Check，检查取消、绑定和来源有效性，不重复扫描完整账本／policy；唯一消息 POST 前再执行第 6 节的完整复核。Inspect 尚未落账，不调用写前账本复核。Close 组合 lease.Close、pin.Close、finishPlatformCall；所有拒绝、冲突、取消、落账失败和正常结算路径均幂等释放。公共动作框架只维持现有 session 生命周期，不增加图片状态机。

不要直接复用现有 pinAttachment(operator,id,seq)：其接口是人类预览／草稿身份，内部还会 loadRoom，不能将 Agent 伪装成人类 operator。也不要放宽 roomAttachmentMessageLocked 的 Page／Range 检查或给它新增“动作模式”；模型原有来源证明继续完整保留。复用的边界是 attachmentMeta、readAttachment、ValidateImage、原生快照和 pin 原语。

正常追加 Room 消息、其他成员 inbox／history 读取不因网络上传持锁。动作期间清空推进越过所持保留边界会返回 ErrBusy；对象回收跳过被 pin 的全部所选图片，其他过期对象仍可回收。保留 pin 沿用当前 root 边界粒度，可能短暂阻止清空比所选图片更早的前缀；首期接受这一与 History 相同的粒度，不新建精细范围保留系统。

该保留保护可能持续到整条含图动作超时，是避免来源在发送途中被清空的明确取舍。正常取消或失败后立即释放；不增加后台等待清空／自动重试清空机制。并发验收须验证“清空暂时 busy，正常追加和读取仍可完成”。

service.executeReferenceAction 已在整个动作及 session.Close 期间持有 service.changes.RLock，来源 helper 不得再次获取它；Go 的 RWMutex 在有 writer 等待时重复 RLock 可能死锁。这也是不能直接调用现有 downloadRoomAttachment 的原因之一。原生快照与文件读取先后执行，不同时持 Store.mu 再获取 attachmentLife；任何 manager 检查、Actor 调用和远端网络请求都在这些文件／数据锁外完成。

图片字节不返回 Agent，不写入动作 JSON，不依赖 DSH 私有附件或正式页缓存。飞书包只接收可信字节、MIME／摘要及 guard；不导入 Room、不访问 Room 存储。源图不存在时失败，不能从模型图片块、预览 URL 或旧固定结果里兜底取图。

### 5.2 飞书拥有上传与发送

建议新增 internal/platform/lark/image_send.go，封装图片上传与完整消息构造／发送／回读。self 和指定群使用同一内容发送器，分别调用现有 self／group 目标检查；SendText 和原文字验证继续兼容。普通回复不因共用代码而自动开放图片。

入口按是否含 images 选择既有纯文字或新增含图路径，不将 body 和 images 互斥。含图消息不能通过伪造 body 进入现有 SendActions 的文字 Inspect／Execute；目标检查提取成不依赖内容形状的只读函数，self 关系和群成员／发言权限各自完整保留。

选择显式分离全部图片上传和唯一消息发送，便于确定前置失败时没有发出残缺消息。不要使用会在图片下载／上传失败时去掉图片继续发送的 Markdown 自动解析路径，也不让 Agent 直接提供原生 post JSON。

1. 在 Inspect 取得全部来源和整条内容摘要；账本及授权依据落盘后，确认当前 pinned user、上传权限及组合 guard。创建私有临时目录，按数组序号和可信 MIME 写入 image-1.png 等固定名称；CLI 以该目录为 cwd、使用相对路径，结束后清理，不使用用户文件名作为路径。
2. 按序经既有 Client.runInDir 执行 im images create，image_type=message、--as user；原始参数由 schema 和测试确定。当前 schema 支持 --file image=./image-1.png。逐个解析 ok、identity 和 image_key，任一失败立即终止整条动作，已上传 key 仅在本次内存中保留。
3. 全部上传后，按第 4 节映射生成一个确定性 payload：单图且无文字使用 image；图文或多图使用 post，仅一个 zh_cn 内容区、无标题，以 text 节点承载准确 body，再按数组顺序加入 img 节点。首期不转 Markdown、不插入提及或装饰文字，所有图片均必须出现且只能出现一次。
4. 在唯一消息 POST 前执行最终 guard：核对绑定／账号、全部来源、准备内容与已落账记录一致，再检查 self_delivery 或群的直接指令／当前 policy 和群发言权限。仍通过既有受控原生消息端点及服务端生成的单个 uuid，只调用一次 POST。不能接收 Agent 提供的替代 key、原生内容或收件地址。
5. 按唯一 message_id 独立回读，核对非撤回、准确 self／群会话、user 作者、租户、预期的 text／image／post 类型及无意外 parent／root／thread。image 核对一张图片；post 核对完整文字、全部图片、数量及顺序，不只核对第一张或提取后的纯文本摘要。

post 构造器与回读归一器必须成对实现。固定 body 的换行与 text 段落映射，以纯文本语义还原准确原文；只接受实际 API 已确认的 locale 包装／默认字段差异，忽略 JSON 字段顺序，不忽略任何文字或图片。多语言、非空标题、额外可见节点、缺图或乱序均不能按成功处理。用多行文字、空行、中文及看起来像 Markdown／@ 的字面字符验证无静默变换。

例如，说明加两张图片对应的 content 解码后如下；真正 API 请求的 content 仍为 JSON 字符串，图片 key 全部由本次受控上传填入，不是 Agent 输入：

```json
{
  "zh_cn": {
    "title": "",
    "content": [
      [{"tag": "text", "text": "这两张图是问题现场，请帮忙确认。"}],
      [{"tag": "img", "image_key": "img_FROM_UPLOAD_1"}],
      [{"tag": "img", "image_key": "img_FROM_UPLOAD_2"}]
    ]
  }
}
```

真实验收必须确认上传 key 与回读 key 的实际关系；发生等价 key 变化时，使用官方可验证的关系或独立资源比对，不能跳过图片内容验证。一条 post 中只核实部分图片不构成部分成功，而是整条 unknown。

上传与发送均进入现有账号级请求预算，本人投递继续沿用账号级串行化，群发送沿用现有调用限额；每条消息的上传在内部顺序执行，不新增并发队列。避免持有 Client 锁再调用会获取同一锁的 helper。含图动作固定总上限建议 180 秒（最多 4 次上传＋1 次发送＋1 次回读，每次 CLI 最多 30 秒），始终受更早的调用／回合 deadline 限制。不要先套既有文字的 30 秒总期限；原纯文字超时不变，不为本次增加配置项。

### 5.3 权限、能力与交互

首期增加资源上传的独立依赖，例如 image.upload，不把它无条件并入 message.send_self／message.send 的文字基础依赖。含图消息执行需要“所选目标动作可执行”与“资源上传就绪”同时满足。

- 当前 CLI schema 同时列出 im:resource:upload 和 im:resource，文字说明明确用户上传需要 im:resource。首期以已确认的 user 路径检查 im:resource；不能把 schema 列表误当作必须同时授权，也不能未经真实验证就承诺较窄 scope 足够。
- 将选定的资源 scope 纳入现有默认检查及授权请求候选集合，沿用与应用已开通权限求交、同账号修复流程。区分应用未开通、用户未授权、检查过期／失败和固定账号不可用，不能只提示重新启用工具。
- 默认检查看到上传权限缺口，不代表必须阻断文字成员绑定。调整 ToolAuthorizationRequired、绑定准入和 UI 聚合判断，使图片的可选依赖不导致文字能力整体失败；用户明确要求修复图片权限时才沿原授权流程处理。
- tools list／设置能力展示增加“图片上传”状态；image.upload 只是依赖状态，不是 Agent 可独立调用的上传工具或新业务 grant。self grant 不授权群目标，群 grant 不授权本人自聊；资源上传就绪也不能替代其中任一 grant。
- self／群 schema 共同说明支持 body 和 images；运行时含图分支给出准确 blocked／unchecked 原因，文字仍可执行。复用当前 60 秒能力快照有效期和 next_action，最终写前检查不能仅依赖 UI 快照。
- 已有 grants 与有效 self 证据继续使用，不迁移、不自动加权限；OAuth 修复如果触发既有凭据代次变化／证据失效，就沿现有合同重新核验，不用本次功能绕过。

## 6. 动作账本、内容身份与失败处理

所有外部上传必须发生在现有 beginReferenceActionWithBasis 成功持久化之后。Inspect 只做读取和校验，不提前上传；actions resolve、tools list、展示 schema 都不能产生上传副作用。

使用现有动作记录作为内容冻结的唯一权威：Request.Input 已保留准确 body 和有序图片引用，原有 Basis／CapabilityBasis 保存授权来源。新增一份 Inspection.content_digest 表示实际准备的完整内容，不再向共享指令依据复制同一摘要。

| 事项 | 沿用已有机制 | 首期必要增量 |
| --- | --- | --- |
| 授权 | self 能力依据；群的 grant、policy_token、直接 Room 指令和准确目标 | 发送内容与已落账完整 Request 一致；最终发送前再核验一次当前授权 |
| 幂等 | 稳定动作 ID、现有 uuid、写前落账、同 ID 返回原回执／变更冲突 | 结构化输入也按完整内容比较，不再只看 Body |
| unknown | 原有 unknown 状态、最大一次尝试、禁止自动补发、重启只查询 | 将图片发送的最终调用边界接入原状态分类，以一份摘要补足原内容等价判断 |

幂等的主键始终是现有动作 ID，内容摘要不是新的动作 ID、授权凭据或永久去重键。同一图文已成功后，用户明确要求再发一次可以创建新动作；只因内容相同不能永久禁止发送。首期不新增发送阶段持久状态、全局去重表、上传断点续传、后台重试／对账服务或自动消解 unknown 的机制。

- 在 TargetActionInspection 增加可选、由服务端提供的 content_digest，连同当前 Inspection 保存。使用固定结构的规范 JSON（version=1、准确 body 或空字符串、有序图片 SHA256 数组）再取 SHA256；不是把字符串无分隔拼接。摘要覆盖整条说明、全部图片及顺序，不依赖上传 key、临时路径或 JSON 输入字段顺序。
- Room 来源复核计算每张实际字节摘要并保留图片顺序；同字节但不同来源引用仍是等价发送内容，来源合法性单独核对。公共账本只验证摘要格式和相等性，不认识 message_seq、images 或 lark 内容类型。公共字段缺省时保持旧文字行为，无需重写旧账本。
- 同 ID 重放继续优先返回原回执；现有 sameBusiness 已比较完整 Input，改变目标、输入或业务依据按原冲突处理。新增可信摘要用于识别同内容的不同来源引用；在对应账号／准确目标上已有相同完整摘要的 unknown 时，换 ID、来源或输入字段顺序不得补发。
- 现有来源／操作／入口的 unknown 防补发规则继续生效，不能用“只在同一目标比较摘要”替换原有保护。只将其中对结构化内容不适用的 Body-only 等价比较改为完整内容等价；既不把同正文不同图片误判为相同，也不因空正文放过相同图片的未知发送。
- 首期来源不跨 Room；跨 Room 的语义去重不是本次新增的全局账本功能。账号级串行化／限流继续复用；未知结果需要保留并报告，不能宣称实现了所有 Room 的全局恰好一次投递。
- 回执保留完整 body、有序源消息／附件引用、整体摘要、目标账号／会话和唯一远端消息 ID。input 已含内容，Inspection 含摘要，Result 含远端 ID；不重复保存图片字节、临时路径或凭据。输入字段的整体上限继续沿用公共 16 KiB 约束。

授权和最终发送复核只保留以下必要步骤：

1. beginReferenceActionWithBasis 按原流程在一个持久化门内核验目标／账号、运行回合与授权，保存完整 Request、Inspection 和原 Basis／CapabilityBasis。群沿用 member grant、policy_token、DirectRoomInstructionOnly、准确 authority.room_seq；self 沿用现有能力依据。
2. 原 referenceActionAttempt 继续在执行前检查当前授权并登记唯一尝试。核对传入请求与该动作的完整 Request 一致，不能只比较 Body，也不能修改已保存输入来适应本次准备结果。
3. 上传期间，每张图之前只检查取消、当前成员／绑定和来源 lease。完整动作内容已固定，没必要每张图都重新加载 policy、扫描账本或遍历历史事项；当前权限缺口仍由相应上传 API 和已有绑定检查阻断。
4. 全部上传完成后，在唯一消息 POST 前增加一次只读最终检查（例如 checkActionDispatch）：按准确动作 ID 读取本次记录并复用记录校验，核对完整 Request、账号／目标、Inspection.content_digest 与当前 lease／body 一致，再复核当前运行回合及 self 证据或群直接指令／policy。它不扫描全部动作和事项、不增加 Attempts，也不更新冻结内容。
5. 最终检查通过后发送固定 payload；任一检查失败按尚未进入消息发送处理。不得重复调用 referenceActionAttempt 来实现这些只读检查，否则会错误增加尝试次数。

followupReplyBasis、followupIntent、持久指令及问答协议保持不变，不在 followups_instructions.go 增加媒体字段。首期群只接受当前 Room 的直接指令，不开放媒体指令保存／问答授权；原授权来源检查和完整动作输入检查分别负责各自的约束，不把空 Body 当作完整图文内容已获授权。

公共动作核心的增量限定为：Inspection 的可选摘要、完整内容一致性比较／unknown 等价补足、一次最终只读检查。图片来源解析、body 与图片摘要合成、post 构造和上传顺序仍归组合层／飞书适配器。旧记录摘要为空时维持文字行为，记录查询不得因为源图已清空而失败；不迁移旧账本，不建立新的通用授权系统。

阶段 3 的实际恢复验证补齐了既有回执查询准入：仅 actions read/list 可在 Session 恢复等待 unknown 核实时读取本成员已落账记录，仍核验服务、Room／成员身份及成员变更、存储健康门槛。resolve、execute 与 Session 重建继续阻断；不靠清除 unknown 或强行轮换恢复发送。此项只恢复只读可观测性，不增加动作状态、授权来源或恢复状态机。

| 发生位置 | 对消息发送结果的处理 |
| --- | --- |
| 任一来源不可见、附件失效、单图／总量超限、格式错误、缺上传权限或群授权 | 拒绝或 not_applied，整条尚未发送，不退化成纯文字或部分图片 |
| 第 N 张上传失败／超时，程序确定尚未调用消息发送 | not_applied；前 N−1 张可能已有未引用的远端资源，但目标会话没有本次消息 |
| 全部上传成功后 payload 构造失败、取消、授权撤销或任一来源复核失败 | not_applied；不发送任何子集，清理本地数据，不谎称远端上传已删除 |
| 消息发送调用已经开始，遇到超时、非可信响应、取消或进程错误 | unknown，不因 CLI 错误码推断未发送 |
| 得到 message_id，但回读失败或内容／身份不符 | unknown，保留已知 ID，只核实不重发 |
| 回读身份、会话、准确文字、全部图片及顺序、类型一致 | succeeded，一条消息只有一个最终结果 |
| 写前落账后进程崩溃，无法证明是否进入消息发送 | 保守保留 unknown；不新增自动恢复发送任务 |

MaxAttempts=1、RetryUnknown=false。已证明 not_applied 的动作仍按现有回执返回；用户要求重试时，读取原结果、修复原因后对完整消息创建明确的新尝试。unknown 只能核实原整条消息，Agent 不得修改文字、拆图、调整顺序或换目标补发；服务端精确内容摘要用于相同完整消息拦截，不宣称可以自动识别任意变体绕过。没有远端 ID 时明确报告无法自动核实，不扫描到“相似图片”就认定成功。

首期不持久化可复用上传 key，也不增加上传阶段恢复状态机。重启不会自动继续发送；临时资源按现有私有临时文件策略处理，正常和取消路径必须删除，异常进程退出的残留清理可复用既有启动清理能力，但不能据此触发发送。

建议在现有 ReasonCode 内增加 source_image_unavailable、image_limit_exceeded、image_upload_not_sent／image_upload_failed、message_content_invalid、message_content_authority_mismatch、message_send_result_unknown、message_verification_failed 等可区分原因。错误可附失败图片位置以便修复，但不能误报该位置之前的图片已发送。权限沿用现有 application_scopes_missing、user_scopes_missing 和对应 next_action；这些细分原因不改变三种动作结果状态的含义。

## 7. 限制与生命周期

首期按当前 Room 已保存图片的硬限制执行：PNG／JPEG／WebP、单张最多 2 MiB、最多 16 Mi 像素；同时检查飞书当前 schema 的单边分辨率约束，不能只检查像素总数。若平台进一步收窄，执行更严格限制并返回准确原因，不自动压缩。

一条外发消息最多 4 张、原图字节合计最多 4 MiB，可来自当前 Room 的多条保留消息；不因跨多个来源而累加绕过单次上限。超限时明确反馈，请用户缩小选择或明确拆成多条消息，Agent 不自动拆分。该上限属于发送资源约束，不消耗或扩大模型本轮图片数、识图开关及提示词预算。

本人自聊默认不自动采集／触发 Agent 的既有规则保持有效，包括 image／post 及其变更。群内本人发出的图文若被现有订阅收集，仍作为 context_only 进展，不触发新的发送或待办。解绑、成员退出、账号切换、群失去访问／发言权限及服务关闭须停止后续上传／发送；已经可能发出的消息仍按 unknown 或已核实结果结算，不能因绑定撤销抹掉回执。

当前 pinned user 发送仅在已验收的 macOS 账号存储路径开放；本次不顺带开放 Linux／Windows 发送。其他宿主继续返回原固定账号能力限制，跨宿主支持需要另行完成账号隔离验收。

## 8. 实施阶段与开放条件

保留“原图准备 → 托管动作 → Agent 使用 → 真实开放”四阶段定义，当前阶段 1～4 及第 8.4 节任务均已实施，真实验收及开放记录见阶段 4 记录。完整衔接顺序为“阶段 3 收尾 → Agent 接口与指引简化并验收 → 阶段 4 真实飞书验收及开放”；简化作为独立前置任务，不重编号、不追溯改写阶段 3 的完成条件。真实接口验证已在第 8.2、8.3 节授权范围内完成并留证。每阶段有独立可验证行为和停止点；阶段编号仅用于计划，长期文件／测试按行为命名。不把上传、发送、授权和回执再拆成独立阶段，也不拆成本人先做、群随后补做。

| 阶段 | 完整交付内容 | 完成条件与开放边界 |
| --- | --- | --- |
| 1：原图准备与保留保护 | 第 4.3、5.1 节的原生快照／保留 pin 复用、两种存储清空保护、完整来源集合校验、实际字节读取和资源释放 | 隔离服务中跑通“已有消息／附件引用 → 准确原图字节 → 校验与释放”；多图失败无残留 pin。原消息追加、inbox／history、图片预算与重放不回归。不上传、不发送、不开放含图动作 |
| 2：完整托管图文动作 | 接入阶段 1 的来源能力，完成 self／群统一输入、全部上传后单次发送、image／post 回读、可选权限、完整动作冻结／最终授权复核、幂等与 unknown；同期提供 schema、帮助及错误反馈 | 通过实际托管服务／CLI 链路和平台子进程夹具完成两种目标的单图／多图／图文；第 N 张上传失败时发送为零，成功时只有一个远端 ID；同 ID、基本并发／取消和 unknown 不补发成立。仅隔离实例注册含图能力 |
| 3：Agent 使用与恢复 | 完成内置 Skill、能力／设置反馈和权限修复交互；真实 DSH／Agent 选择准确群及图片、构造整条消息；独立进程恢复和并发故障验证 | 隔离托管服务下真实 Agent 使用实际工具，图文／多图只提交一条完整动作；平台可用夹具。重名澄清、跨回合历史、取消、重启和 unknown 均不重复发送；界面不把未开放能力显示为已可用。日常生产仍不开放 |
| 4：真实飞书验收与开放 | 第 8.4 节简化任务验收通过后，按明确授权，对指定本人会话和单个受支持群使用最终合同完成真实 Agent 图文发送、独立回读及回流检查；通过后更新正式能力注册、用户说明与部署采用记录 | 真正跑通 Room → Agent → 托管动作 → 飞书 user → self／群；核实作者、目标、准确文字、全部图片及顺序。两种目标分别留证，原回执重查／重启不额外发送；未通过的模式不公布，原文字能力保留 |

### 8.1 阶段衔接与验证归属

- 阶段 1 必须在实际 Store／服务装配中验证原图准备闭环，不以搬文件或定义接口作为完成；不要求提前扩展动作账本或接入飞书传输。
- 阶段 2 将输入、错误、授权、幂等和基本失败测试与发送行为一起交付，不能先做一个可发送但不具备可靠回执的版本。帮助按当前注册 schema 声明能力，不能等到阶段 3 才补调用合同。
- 阶段 3 在阶段 2 已可靠的托管动作上验证 Agent 决策、修复交互与跨进程恢复，不重做另一套发送实现。第 9.1 节核心／组件测试主要归阶段 1、2；第 9.2 节主要归阶段 3；第 9.3 节主要归阶段 4。
- 阶段 3 的记录保留为简化前基线；独立简化任务补充合同、调用成本及受影响行为的验证，第 8.4 节验收通过后才进入第 9.3 节。不因简化重复创建整套底层图片测试，也不以既有阶段 3 通过替代新合同验收。
- 阶段 1～3 及独立简化任务复用当前隔离 manager 的能力注入路径，测试同一正式实现，不新增日常配置开关或持久阶段标记。阶段 4 才修改正式注册；代码验收、能力开放与日常服务／Session 采用分别记录，不擅自重启或轮换现有 Session。

### 8.2 真实接口小验证前置

在阶段 2 开头安排一项有界真实接口验证，可与主体开发并行，不另设实施阶段：使用准确 user 身份，在明确授权的隔离样本中确认图片上传、post 发送／回读、换行及上传 key 与回读 key 的关系。尽早依据实际返回结果确定构造／归一规则，避免完成 Agent 接入后才发现接口假设不成立。

该验证可以直接调用官方 CLI 或受控发送器，只证明平台接口合同，不代替阶段 3 的真实 Agent 验证或阶段 4 的完整飞书链路验收。不做未经授权的写后超时故障注入。未获真实写授权时，可以继续夹具开发，但必须将“本地阶段通过”和“真实接口待验证”分别记录；已有验证样本在账号、接口及合同一致时复用，避免为阶段编号重复发送。

### 8.3 真实写入与最终开放

任一真实接口验证或阶段 4 验收之前，必须确定账号、本人会话／准确群、文字、源图片、每条内容和允许发送的消息条数／上传张数。不能把本人验收授权扩展成向群发送。额外故障注入不从一次成功验收授权中推定；能用夹具证明的边界不额外污染真实会话。本方案评估通过本身不代表授权这些真实写操作。

若只批准某一阶段，完成该阶段后报告实际通过的测试层次、仍未执行的真实验证、仍关闭的能力和剩余阶段。全部阶段继续遵守第 4.2 节协议不变和第 6 节机制收敛边界；阶段拆分不新增功能或架构。

### 8.4 独立前置任务：Agent 接口与指引简化

本节已在阶段 3 之后独立实施并完成隔离验收，详见[实施与成本记录](implementation/lark-image-send-agent-simplification.zh.md)及验收证据（本地验收文件 `lark-image-send-agent-simplification-evidence.json`）。以下保留完整交付与完成条件；阶段 4 使用本节验收后的合同。目标是让 Agent 用简短、准确的合同完成飞书发送，减少身份对象搬运、字段猜测、重复读取和分支说明；不能只删提示词而保留难用的接口。

#### 范围与交付

收敛飞书发送从入口发现、候选选择到结果查询的完整路径；复用现有动作及服务端检查，不全面改造其他平台。

| 改动 | 实施要求 |
| --- | --- |
| 候选直接衔接下一步 | 群候选返回可直接作为下一次请求 locator 的对象，Agent 选择后整体传回，不再从 candidates[i].target 手工构造 locator.candidate。沿用现有原生目标解析，不新增候选持久化或引用系统。保留账号及区分同名群所需的信息、结果不完整标记；选择仍须重新核验目标与权限，不能直接执行 |
| 入口、帮助和错误一致 | 沿用 tools list 的准确入口，以 tools run ENTRY actions resolve／execute 为推荐路径；托管入口与根 actions 命令共享参数合同、帮助和字段诊断，托管入口支持相应 --help。格式错误返回准确字段路径、预期结构；权限／账号错误返回当前原因及下一步，不再要求 Agent 记忆两套帮助路径 |
| 精简请求与动作说明 | 图片内容仍为 body + images，不增加必需的附件解析调用。执行上下文已有且能可信确定的 Room、成员、账号由服务端提供；self／群按当前可执行动作说明各自必填依据，不让 Agent 从多套通用授权分支反推。保留稳定 id、当前 turn_number、群 policy_token 和实际人类指令依据，不猜测或自动补造授权 |
| 精简正常返回 | 候选保留选择与继续解析所需字段；解析结果保留准确账号／目标、target_ref、当前动作输入及必要依据；执行结果保留 id、状态、准确目标、文字和有序图片引用、核实结果及已知远端 ID。合并重复的身份／状态表达，完整原生身份及内部冻结证据按需通过现有详情查询，不截断恢复所需内容 |
| 收敛 Skill 与修复引导 | 主 Skill 只保留“确认目标 → 选择内容 → 提交完整动作 → 按回执报告”、本人／群准确示例，以及歧义澄清、完整消息单次提交、unknown 查原回执三项关键约束。格式与上限以动作 schema 为准，缺权／账号修复随对应错误按需提供；公共帮助与 Skill 避免重复解释上传、OAuth 和 Session 内部过程 |

候选应返回可复用的 locator，而不是冻结整份跨回合请求；用户澄清后的 turn_number 和 policy_token 仍取当前运行时值。若已有有效入口、目标或回执，沿原合同复用，不为了“简化”增加 get／resolve 前置步骤。候选、标签和复制回来的 locator 均不是授权证据。

正常返回必须足以判断“当前账号向哪个目标、发送了哪些完整内容、是否核实成功”。详情按需展开只减少内部重复，不隐藏业务依据、失败含义、完整性标记或跨回合恢复需要的内容。正式未开放时，动态能力仍准确声明文字 schema；不要用长期 Skill 中的验收阶段说明代替实际能力约束。

#### 实施边界

- 保留现有授权、目标及账号核验、图片来源／字节检查、完整内容冻结、最终发送复核、幂等和 unknown 防补发；不改变本人／群的授权区别，不增加自动重试或恢复发送。
- 延续第 4.2 节 inbox／history 协议与冻结公共指引边界；本轮只收敛动作入口、Agent 返回投影、按需帮助及内置 Skill，不改变 MCP 工具数量、图片预算或日常 Session。
- 不新增通用工具框架、候选存储、媒体管理、独立上传工具、发送 UI 或配置开关；其他平台只验证受共享诊断／投影改动影响的兼容行为。
- 先完成候选衔接、入口一致性及错误反馈，再同步精简返回、示例和 Skill，作为一个完整任务验收；阶段 4 以该合同为准。原文字动作、旧动作记录和回执继续可用，无旧数据迁移。

#### 完成条件与证据复用

1. **合同可直接使用。** 定向测试覆盖候选 locator 整体复用、同名选择、跨回合当前值、托管／根入口一致的帮助和字段错误；目标变更、账号切换或缺权仍被拒绝。正常返回可核对完整内容，详情查询可恢复旧回执；不靠放宽严格输入校验来消除错误。
2. **真实 Agent 验收。** 在普通托管 Room 和默认助理中分别覆盖本人／群选择，并覆盖文字、单图、纯多图、说明加图；保留跨回合 History、重名／多来源澄清、权限失败、取消及 unknown／重启查询。复用阶段 3 隔离服务、真实 DSH／模型和平台子进程夹具，用户请求自然表达，不在测试指令中临时补充 JSON 层级、字段修法或专用命令教程来帮助过关。
3. **调用成本可比较。** 以阶段 3 证据为基线，在相同业务场景、尽量相同模型条件下记录完整工具调用数、业务调用数、需手填的参数、格式错误及帮助查询次数、主 Skill／帮助／正常返回的篇幅。已知入口、已取到图片引用的新任务，本人目标按“解析＋执行”两次、按群名寻找目标按“查询候选＋核验选择＋执行”三次业务调用验收；已有有效目标可更少，不为达标跳过必要核验。入口／Skill 首次发现、History、Room 内报告和异常恢复调用单独列出，不从总成本中隐去。正常路径无字段猜测或格式试错；故意构造的格式错误能依据一次准确反馈修正；澄清后不让用户重复已明确的选择，指引和返回篇幅实际下降且必要信息完整。
4. **原可靠性边界成立。** 对受影响的文字／图文合同、取消、权限撤销、旧回执、unknown 和重启查询运行定向回归，确认不拆消息、不补发、不退化为文字或图片子集。若修改共享入口或投影，补齐受影响的其他动作兼容检查。
5. **证据归属清楚。** 阶段 3 原记录保留；在 docs/implementation 单独记录简化前后合同、成本及验收结果。未改动的来源存储、上传器和底层进程故障证据可复用，说明复用依据；只重跑受影响场景及必要检查，出现新问题先定向定位，最终复验受影响的完整链路。模型限流或超时单独留证，不据此删减验收条件或标记通过。

完成本节全部条件后再推进阶段 4；仅完成示例／报错修补或本地组件测试不足以开放。本任务不向真实飞书上传／发送，不修改正式图文注册，也不擅自重启服务或轮换日常 Session；真实接口小验证另按第 8.2、8.3 节授权执行。

## 9. 验证清单

### 9.1 核心与组件

- 输入：旧 body、纯单图、纯多图、body + images；二者都缺失、空 body／数组、超 4 张、重复附件 ID、非法字段、任意路径／URL／平台 key 拒绝；普通回复含图仍拒绝。
- 来源：多条保留消息组合、同名异图、同字节不同附件、错误 seq、草稿、跨 Room、不可见消息、过期／清空、损坏字节、错误 MIME／摘要、单图／总量／分辨率超限；内嵌和 InputLog 两种存储。
- 目标／权限：self 与群 grant 不互相替代，群名重名／非成员／禁言／移出拒绝；外部群、话题群、特殊发言策略继续按现有群目标规则拒绝；上传权限缺口只阻断含图，文字仍可用；旧 self 证据和 OAuth 修复失效规则不回归。
- 传输：真实 CLI 子进程夹具断言各相对路径和实际字节、上传／发送同一 user；N 次上传后最多一次消息 POST，准确 uuid；post 包含准确文字、全部图片和顺序，不只 mock 成功 ID。
- 回读：正确 text／image／post，换行／空行／中文／字面 Markdown；不同 locale 包装，缺图、乱序、额外文字／节点、错误标题、身份／目标不符均不能成功。
- 授权／账本：所有上传前已有完整 Request.Input 与单份 Inspection 摘要；同 ID 替换／增删／重排图片或改文字返回冲突，提交的 payload 与冻结记录不符时最终发送被拒绝。并发／重启／完整等价内容的 unknown 拦截有效，原有来源／操作／入口防补发不回归；旧文字、指令／问答及其他平台动作兼容。
- 检查边界：N 张图片的中间上传不触发 N 次完整账本／policy 扫描；保留原执行前授权检查，唯一消息 POST 前新增一次最终复核。中间取消／解绑能停止，准备期间 policy 变化在最终发送前被发现，Attempts 不因只读复核而增加。
- 失败：每个图片位置均注入来源／上传失败，消息 POST 必须为零；全部上传后取消／撤销／构造错误同样不发送。发送后超时／回读失败记整条 unknown，区分“未引用上传资源”和“会话消息可能发送”。

读取边界必须补充可直接断言的验收：

- 使用原封不动的 HTTP inbox_read、CLI inbox read、history_read 获取同一原生 seq／附件 ID，再调用图片动作；新动作不需要携带任何读取 receipt 或 frame 索引。
- 拍摄输入状态前后快照：允许动作账本及预期平台调用变化；原生 ReadThrough／ReadProofs、input-state（含读取尝试、预算及去重）、正式结果文件、History 已建查询／cursor 的内容不因动作改变。若 Agent 主动发起额外 History 调用，其正常查询开销单独归因，不误记为动作副作用。
- 先正式消费，再从保留历史选择图片；另测模型 text-only 或读取图片额度已耗尽。动作仍取得原始字节，已有读取覆盖说明／预算不改变。
- 正式页曾交付图片后删除原对象：正式页仍可按原机制重放，但新发图动作必须拒绝，不拿页面缓存当原图仓库；已有成功动作同 ID 查询仍返回旧回执。
- 两种存储均覆盖：多图取图／中间位置上传暂停时，清空返回 ErrBusy；新消息发布和其他成员读取仍完成，回收只跳过全部被 pin 的对象；部分准备失败、关闭、取消、冲突、落账失败后无遗留 pin，释放后原清空语义恢复。
- 保留现有 TestHistorySchemasAndProductionReadOnlyDefinitions、TestHistoryMCPModesSchemasAndNoConsumption、TestHistorySnapshotBoundAndNativePinLifetime、TestRoomAttachmentMaterialsMCPReadHistoryAndReplay、TestRoomAttachmentMaterialsMissingAndBudgets 的断言；底层抽取不能以调整这些测试的协议预期换取通过。

### 9.2 服务装配与 Agent

- 使用隔离托管 Room 和默认助理私信，走实际附件上传、消息提交、Agent 正式读取／History、tools run/actions.execute 和账本查询路径。
- 真实 DSH／Agent 覆盖 self／指定群、“上一条图”“两张一起发”“只发第二张”“说明和两张图发成一条”、重名群／多来源澄清、跨回合历史、识图关闭但按明确引用转发；核对只构造一条完整动作、内容数组与顺序正确。
- 对上传或权限错误不改发文字／部分图片，不搜索磁盘、调用裸 lark-cli 或切换账号；unknown 不拆消息重发；正确发送后本地报告完整结果，不额外发送远端说明。
- 记录模型／工具真实调用和账本证据；不要用测试直接调用发送器替代 Agent 接入验证。

### 9.3 真实飞书与交付

- 进入条件：第 8.4 节独立简化任务已实施并验收；以下真实 Agent 调用、帮助及 Skill 均使用简化后的最终合同。
- 在授权范围内对本人会话和指定单群分别覆盖纯单图，以及“说明＋两张不同图片”的一条 post；纯多图模式也须验证。核对每条只有一个远端 ID、准确 user 作者／目标、原文和全部图片顺序。UI 预览只作辅助，不替代原生回读。
- 重复同 ID、重启后查询原回执，不产生额外远端消息；本人自聊仍排除，群内本人发出的图文仍为 context_only，不形成自动处理回路。
- 界面只检查新增能力标签、权限反馈和正常图片显示，不增加新的发送 UI。报告组件、真实 Agent、真实飞书、浏览器各自通过范围。
- 运行受影响 Go 包的定向单元／集成与必要 race，以及改动到的前端测试；如项目约定另有必需检查则补齐。真实平台及真实模型验收保持显式 opt-in，不放入普通 CI。

## 10. 改动面、取舍与后续扩展

能力可行，完整交付的复杂度为中等偏高，主要成本在原图生命周期、整条图文回读和兼容性验证。授权／幂等／unknown 的新增实现收敛为中等范围：复用原框架，补足完整内容比较和最终检查；验证成本仍须覆盖并发、取消、超时和重启，不能因机制简化而省略。读取相关改动已在第 4.3 节逐文件限定，不以预设文件数量作为拆分要求。

关键控制点是三个：self／群复用统一内容输入和发送器，各自保留授权；一条消息内全部准备后单次发送，不建立多消息批量事务；只增加服务端来源 lease 和内容摘要，不建立通用附件注册表或上传恢复平台。主要成本在来源／生命周期、完整内容授权与回读、权限不回归，HTTP 上传本身不是主要难点。

首期用完整 Request.Input + 单份 Inspection.content_digest 约束已提交内容，沿用原群授权来源，不扩展共享指令结构，也不把完整授权检查重复到每张上传。新增只读检查按当前动作 ID 取记录，不另建缓存、授权对象或索引；按需下沉的原生读取 helper 不发展为通用资源服务。

普通回复后续再复用来源集合及发送器，并补齐回复／话题目标、对应授权入口和真实验收；外部群和特殊发言策略另行验证，不能只复用 post 构造就宣布支持。当前本人／指定单群的一条图文能力必须完整交付，不因收敛机制退回逐图发送或文字替代。

跨平台图片来源后续通过独立适配接入，保留原通知、账号和消息关系；不能把平台 key 当作 Room 附件 ID，也不能假设跨账号／应用可复用 key。任意段落／图片交错将来确有需求时再扩展内容模型，首期 body + images 已支持一条图文，不提前引入通用内容块树。

交付时更新 [飞书使用说明](lark.zh.md)和内置 [飞书 Skill](../internal/platform/tool/skills/lark/SKILL.md)，明确支持的来源和目的地。实现及验收记录遵循仓库既有目录约定，读者说明不堆叠阶段清单；各任务状态分别记录，独立简化任务已验收，阶段 4 的真实验收、正式开放及部署采用情况以[阶段 4 记录](implementation/lark-image-send-s4.zh.md)为准。

## 11. 方案制定时的核查记录

本节保留实施前的设计基线；阶段 1 新增实现与验证以[实施记录](implementation/lark-image-send-s1.zh.md)为准。

2026-11-20 仅分析代码、修订本文并运行现有基线测试，未实施图片发送。已核对两种 Inbox 传输、History 正文／图片投影、动作准入与上下文、原生快照、附件存储和清空保护；结论是无需修改 inbox／history 协议，内部复用与已发现的清空差异按第 4.3、5.1 节处理。

以下两组共 9 项顶层现有测试均通过，用于确认设计基线；不代表新增 Store helper、来源 lease、内嵌清空保护或真实飞书链路已通过验收：

```sh
go test ./internal/room -run '^(TestRoomAttachmentMaterialsMCPReadHistoryAndReplay|TestRoomAttachmentMaterialsMissingAndBudgets|TestHistorySchemasAndProductionReadOnlyDefinitions|TestHistorySnapshotBoundAndNativePinLifetime|TestHistoryMCPModesSchemasAndNoConsumption)$' -count=1
go test -tags=integration ./internal/room -run '^(TestRoomAttachmentMaterialsServiceAssembly|TestRoomAttachmentMaterialsSelectedRangeAndLifecycle|TestHistoryMCPImagePositionsDedupAndIndependentBudget|TestHistoryRoomVisibilityFrozenPagesAndNoWrites)$' -count=1
```

当前收敛设计已对照完整动作记录、sameBusiness、DirectRoomInstructionOnly、actionAuthorityLocked／referenceActionAttempt 和 SendGroup 的实际范围：复用已冻结 Request.Input，只增加一份可信摘要；轻量检查贯穿上传，完整授权在唯一消息发送前集中复核；指令／问答和 inbox／history 协议保持原状。本次只修订方案和校验文档，未重跑上述业务测试，也未验证新增的群图文实现；这些基线结果不能替代第 8、9 节的新验收要求。
