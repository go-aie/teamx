# 指定人员私聊实施记录

日期：2026-11-12。对应[对话式平台设置方案](../../CONVERSATION-PLATFORM-SETTINGS-PLAN.zh.md) S2，基于已交付的 [S1 通知补丁](NOTIFICATION-SETTINGS.zh.md)。本阶段增加当前成员、精确飞书账号的人员私聊查询和列表修改；真实模型与浏览器组合验收仍属 S3。

## 查询与澄清

```sh
teamx platforms chats --platform-id P --platform-version V --type p2p --query 姓名
teamx platforms chats --platform-id P --platform-version V --type p2p
teamx platforms chats --platform-id P --platform-version V --type p2p --page-token TOKEN
```

`P/V` 来自当前连接查询。省略 `--type` 或指定 `group` 保持原群／话题群查询。`p2p` 只返回同账号下已核验的真实人员会话，排除机器人、群和本人自聊；结果使用原生 `oc_…` 会话 ID，不能用人员 `ou_…` ID 代替。

姓名搜索复用既有 `contact +search-user --has-chatted`，不调用机器人搜索。尚无会话的人员不会被补建会话；不会发送平台消息。搜索结果仍逐个复用 `NotificationChat` 核验关系。姓名、精确 ID 和同名候选均保留，由 Agent 根据平台 Skill 在目标不唯一时澄清，不自动选择首项，也不增加人员选择卡片。

私聊目录复用 `Chats`，每页最多 20 个原始候选，以限制关系核验成本。过滤后可能是空页，仍保留服务端 `has_more/page_token`，必须继续使用原令牌。姓名搜索沿已有 CLI 合同无法翻页，超过结果上限时保留 `refine_query` 提示，需缩小搜索词或改查目录，不能编造分页令牌。

保留 `search_warnings`，包括搜索权限不足和部分候选关系不可核验。能核验的候选仍可显示；权限不足、不可核验或部分结果不证明目标不存在。身份漂移、超时或无效响应直接失败，不返回未经核验的候选。

## 指定范围修改

明确用户请求后，先恢复原 `member status`，读取当前 `member get`，一次提交该请求的全部通知字段：

```sh
teamx platforms member notifications --platform-id P --platform-version V \
  --notification-version N --operation-id STEP_ID --source-seq SEQ \
  --private-scope specified --private-chat-id oc_ONE --private-chat-id oc_TWO \
  --private-enabled true
```

`N`、`STEP_ID`、`SEQ` 分别是观察到的通知版本、稳定操作 ID 和原真实 human 消息序号。示例占位值不能直接执行。列表是完整替换，最多 32 项；省略保留，单个 `--private-chat-id ''` 清空，重复 ID 或混用空值与非空值被拒绝。JSON 的 `private.chat_ids` 使用可选列表保留省略与 `[]` 的区别，显式 false 继续独立表示关闭。

从全部切到指定必须同时提供 `--private-scope specified` 与完整列表。单纯修改范围或列表不自动开启通知；明确请求开启时加 `--private-enabled true`，并沿 S1 的休眠来源约束处理另一来源。关闭私聊保留范围和列表；清空最后一个指定私聊会关闭私聊分支，无其他启用来源时关闭总通知。显式开启空的指定范围返回冲突。全部范围下清空保存的指定列表不关闭全部接收。

新增或完整替换的每个 ID 在保存时重新读取会话元数据、成员列表与原生人员会话映射，只接受 `peer` 关系且账号及会话 ID 精确一致；不依赖搜索显示名。群和私聊列表可在同一操作修改，回执按群列表、私聊列表的输入顺序冻结对应核验证据。回执读取同样验证私聊关系，不能用别的账号或别的会话证据替代。

清空／单独关闭无需读取旧目标，未修改的既有列表不因远端不可访问而被删除。继续复用成员配置服务与 `configureLarkSubscription`；远端读取前后及提交前重查完整成员、账号、连接版本和通知版本。工具修订独立，历史起点、采集进度和未指定配置保持原契约。

同 ID／同输入返回原回执，改变输入冲突，同来源换 ID 不能绕过原操作；未知提交保留原围栏，重启后不重读远端或重放配置。无订阅时单独清空返回 `preserved`、版本 0，不创建订阅。

## 验证与边界

行为测试覆盖补丁合成、列表显式清空／持久化、指定切换前置条件、关闭后保留、休眠来源和类型化参数边界。`TestPlatformMemberPrivate…` 集成专项使用真实 TeamX CLI 子进程及模拟飞书 CLI，覆盖：

- 同名人员、无会话人员、姓名搜索截断、目录空页继续分页、搜索与成员读取权限不足。
- 机器人／群／自聊／不属于本账号的会话、人员 ID 和不完整成员列表不能保存。
- 指定替换与清空、原回执重读、最终配置、工具／群选择／原起点／另一成员不变，无新增卡片。
- 远端核验期间成员替换、账号换代及通知版本变化被拒绝；未知回执重启保留证据且不重放，同 ID 改输入与同来源换 ID 冲突。

以下基础与回归检查均通过：

```sh
go test ./... -count=1 -timeout=8m
go test -race -tags=integration ./internal/room \
  -run '^Test(PlatformMember|PlatformSelection|GitLabMember|GitLabProject|GitLabAccountSelection)' \
  -count=1 -timeout=4m
node --test --test-reporter=dot internal/web/tests/web_*test.mjs
TEAMX_SELECTION_BROWSER=1 TEAMX_CONNECT_BROWSER=1 go test -tags=integration ./internal/room \
  -run '^Test(PlatformSelectionConversationAndMemberConfiguration|GitLabProjectSelectionConversationBrowser)$' \
  -count=1 -timeout=3m
go test -tags=integration ./internal/platform/lark -run '^TestChatSearch' -count=1 -timeout=2m
git diff --check
```

目录页大小调整后另通过私聊／飞书补丁专项竞争检测；群和私聊列表在同一原操作内替换的 CLI 检查也通过。新文件单独做空白检查，文档链接和代码围栏已核验。

额外 `go vet ./...` 未通过：既有 `platform_connect_interaction_test.go:99/144` 和 `platform_connect_test.go:161` 在非测试 goroutine 中调用含 `t.Fatal` 的 `connectSucceed`。这些文件本次未修改，未为本阶段扩展修复范围。

本阶段不修改真实平台订阅或运行配置。上述浏览器检查使用模拟提供方，回归已有群／GitLab 选择和成员配置流程，不替代指定私聊的真实模型／浏览器组合验收。真实同名澄清、S1／S2 组合修改及浏览器最终展示留到 S3；未据此宣称平台事件入 Inbox 或提醒送达。
