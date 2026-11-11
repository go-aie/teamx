# 对话式平台接入：成员配置 CLI 实施记录

日期：2026-11-11。对应方案 C2。证据来自本地服务、模拟官方飞书 CLI 和真实 TeamX CLI 子进程；未进行真实飞书／模型 Agent／浏览器端到端验收。C3 连接组件、本地验证及单账号真实组合验收见 [最小对话连接](CONNECTION-UI.zh.md)；C4 见 [Inbox 类型扩展](INBOX-EVENTS.zh.md)；C5–C6 尚未实施。

## 已交付

- `platforms chats`：已核验飞书账号的群目录／搜索／分页，返回账号、连接版本、群 ID、名称、类型及查询警告；同名群保留独立候选。仅返回普通群和话题群，过滤后的空页仍保留分页信息。
- `platforms member get`：读取当前完整 Room／Agent／Membership 身份的订阅配置、订阅版本、接收起点、工具修订及可用状态，不要求已有平台工具绑定。
- `platforms member notifications`：按明确群 ID 追加关注，或显式开启／关闭通知、修改群消息模式。提交前通过同一账号重新读取每个群的元数据，拒绝私聊 ID；复用现有通知配置服务。
- `platforms member tool`：独立启用／关闭当前成员的飞书工具，复用现有工具绑定服务和调用结清；不新增外部写入授权。对同一平台 generation 重复启用保留原授权及其他字段；重新绑定已换代的平台时沿用原服务规则，不转移旧授权。
- `platforms member status`：按操作 ID 查询原回执，或按原用户消息查找全部成员配置步骤。接入 Skill 和 Agent 指南已更新。

仅连接平台的流程仍在连接核验后结束，不顺带启用通知或工具。群关注、工具启用分别使用独立的稳定操作 ID，不需要对话选择组件或操作结果 Inbox 事件。

## 命令示例

以下命令在完整绑定的当前 Agent 环境中执行。`P`、`V` 来自 `platforms list` 中明确选定的可用连接；`SEQ` 来自真实 human Room 消息；每个配置步骤使用独立的 32 位十六进制操作 ID。

```sh
teamx platforms list
teamx platforms member status --source-seq SEQ
teamx platforms member get --platform-id P --platform-version V
teamx platforms chats --platform-id P --platform-version V --query '产品'
teamx platforms chats --platform-id P --platform-version V --query '产品' --page-token TOKEN

teamx platforms member notifications --platform-id P --platform-version V \
  --notification-version N --operation-id STEP_ID --source-seq SEQ \
  --add-chat-id oc_SELECTED
teamx platforms member tool --platform-id P --platform-version V \
  --tool-revision R --operation-id OTHER_STEP_ID --source-seq SEQ --enabled true

teamx platforms member status --operation-id STEP_ID
teamx platforms member get --platform-id P --platform-version V
```

`N`、`R` 分别来自 `member get` 的 `notification_version` 和 `tool_revision`；不存在时为 0。通知版本只约束目标成员的该平台订阅；工具修订沿用现有 Room 级修订，其他成员修改工具后也需要重新读取。

`--add-chat-id` 可重复，最多 32 个；追加时默认开启通知。新订阅仅关注选中群，群消息模式为 `all`，私聊及独立提及接收默认关闭，接收起点为首次保存时间。已有订阅保留其他群、私聊、提及选项、消息模式、轮询间隔及既有历史接收起点；仅显式传入 `--group-message-mode all|mentions` 时修改群消息模式。`--enabled true|false` 修改总开关，关闭后保留选择与起点。

已有 `groups.scope=all` 时，追加指定群会返回冲突并引导完整设置明确调整范围；不静默把既有全群范围替换成选中群。现有配置模型的群消息模式对整个群范围生效，CLI 不引入每群独立模式。

显式选择 `--group-message-mode mentions` 时，保留已有提及筛选；原来 `at_me`、`at_all` 均关闭则开启 `at_me`，避免仅剩自有消息而未接收提及。

## 正确性与恢复

成员配置记录位于服务数据目录 `platform-member/`，保存完整成员身份、服务读取的原始用户证据、固定输入、已核验账号、群元数据、拟应用的规范化通知配置及回执。通知和工具文件继续是当前配置的权威来源，不复制 Token、Secret、CLI 凭据或临时授权链接。

- 同 ID、同成员／来源／操作类型／固定输入返回原回执；改变输入冲突。同一成员、原消息、平台和操作类型不能换新 ID 再次提交。丢失 ID 时先按 `source-seq` 查找；同一消息可以包含独立的通知和工具步骤。
- 身份校验不依赖工具绑定。服务验证当前完整成员和真实 human 消息；Agent 自报 human、外部通知、伪造来源字段、跨成员目标和浏览器直调均被拒绝。原 human HTTP 接口未放宽。
- 群候选携带账号和连接版本；提交时复查账号版本、原成员及准确群 ID，远端读取后及配置锁内再次检查。订阅／工具版本变化拒绝写入，保留其他设置。
- 首先持久化 `unknown` 提交栅栏，再调用原配置服务；完成后保存 `completed` 回执。`result.status=applied|preserved`、`result.version` 为配置回执，`result_version=1` 为结清结果。未确认操作的 `result_version=0`，可能仍在执行或已中断；不得当成未执行。
- `status` 和重复提交均只读取原记录，不重新调用配置服务。写入失败、调用结清中断或结果保存不确定时保留未知结果，使用 `status` 和 `get` 核对原目标；通过完整设置明确恢复，不自动重试、不换 ID 重放。重启也不执行旧配置步骤。
- `commit_unknown` 沿用 Store 的写入冻结；先重启并读取磁盘回执／当前配置，不在冻结状态下把查询失败当成操作不存在。
- 连接版本与配置版本独立；历史完成回执不代表当前配置。`member get` 核对实际保存的订阅和工具可用性。配置成功、通知接收、Inbox 消费与原业务完成仍为独立事实。
- 群查询／核验沿用 30 秒服务超时，成员提交有 40 秒总时限，CLI HTTP 请求为 45 秒；CLI 响应丢失不允许据此重发未知动作。

## 验证

```sh
go test ./... -timeout=3m
go test -race -tags=integration ./internal/room \
  -run '^TestPlatformMember|^TestPlatformConnect|^TestLarkChatPicker|^TestMemberNotificationLarkOnboarding|^TestMemberPlatform' \
  -count=1 -timeout=2m
```

覆盖：真实 CLI 子进程复用已核验连接 → 群搜索与分页 → 同名候选准确选择 → 通知配置 → 独立工具配置 → 核对／关闭；保留既有接收起点、私聊、其他群、模式和间隔；保留其他成员绑定及既有工具授权；重复 ID／变更输入／换 ID、伪造用户依据、成员替换、账号／配置版本冲突及群核验期间的账号／成员／订阅变更；配置已保存但回执替换前后报错、重启查询与禁止重放。原 C1、human 群查询及成员 onboarding 路径纳入 race 回归。

结果：全仓普通测试、上述 race 集成回归，以及最终成员配置 race 测试均通过。`git diff --check` 和新增文件空白检查通过。

未新增对话组件、群选择表单、内部 Inbox 事件、结果通知订阅、自动唤醒或调度分支。上述能力仍按 C3–C6 推进。
