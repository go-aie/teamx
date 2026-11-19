# 会话接口参考

本文面向接口调用与实现维护。页面操作见[会话使用指南](../conversations.zh.md)，身份与持久化约束见[Room 架构](../room-architecture.zh.md)。

## 管理接口

管理请求携带目录的 `service_epoch` 和 human `caller`。`GET /v1/rooms` 提供 `agent_roles`、会话类型及 `default_assistant` 引用；`is_direct` 贯通目录、成员列表和 `/v1/info`，不属于身份字段。

每项 `human_read` 是只读摘要：`status: ready` 时包含 `revision`、`snapshot_seq`、`cleared_through`、`unread_count` 和 `mention_count`；`status: unavailable` 时省略计数。`unread_count` 是普通新消息与未查看提及的去重并集，`mention_count` 是未查看提及数。摘要扫描完整保留历史，不启动会话或模型，也不写入目录文件。

`POST /v1/human-read/state` 返回版本 2 的 `opened_through`、`mention_seen_seqs`、`unseen_mention_seqs` 及计数；首次进入传 `include_new_messages: true`，额外取得该快照下的 `new_message_seqs`，普通轮询省略此字段。`POST /v1/human-read/confirm` 提交 `snapshot_seq`，以及 `opened_through`、最多 100 个 `mention_seen_seqs` 中的至少一项。打开位置取最大值、提及取并集，原子保存，互不代替；序号不得超过提交快照。旧 `seen_seqs` 请求不再接受。两个接口均使用会话的完整身份、当前服务代次和 human caller，不使用 Agent 的阅读确认接口。

通过 `POST /v1/rooms/create` 新建独立私信：

```json
{
  "service_epoch": "目录返回的服务代次",
  "caller": {"kind": "human", "id": "操作者"},
  "create_id": "本次创建的稳定ID",
  "is_direct": true,
  "agent_role_id": "assistant",
  "name": "个人助理"
}
```

普通房间使用 `team_id`；私信使用 `agent_role_id`，两者互斥。名称可省略。私信使用服务默认 runtime，模型和角色在创建时冻结，恢复不重新读取当前模板。新建个人助理使用 `assistant` 角色；已有默认助理保留冻结的角色与成员身份，内置角色更名不会重建会话或改写订阅、规则及历史回执。相同 `create_id` 的重试必须保留类型、来源和名称；改变参数报冲突。创建后使用原 `/v1/rooms/open` 流程，详见[运行手册](../../internal/room/README.zh.md#多房间服务)。

默认初始化的管理入口如下：

| 入口 | 请求与行为 |
| --- | --- |
| `GET /v1/rooms` | `default_assistant` 返回完整 Room 引用和 `pending`／`completed` 状态；`deleting`／`deleted` 保留旧版本删除记录 |
| `POST /v1/rooms/default-assistant/retry` | 仅提交当前 `service_epoch` 和 human `caller`；重试原待完成初始化，不热加载配置、不重新启用已完成实例，也不恢复已删除实例 |
| `POST /v1/rooms/default-assistant/restore` | 另提交已删除实例的完整 `room_id`／`room_instance`；核对原删除引用后保存显式恢复意图，再新建默认实例，陈旧／重复请求不重复创建 |

`configuration_required` 提示补齐模型／DSH 配置后重启，再继续原创建；`initialization_failed` 保留实际错误。已经预留的恢复使用冻结配置和身份。`system-default-assistant` 为保留创建 ID，普通创建接口不能使用。

类型与持久化不变量见[Room 架构](../room-architecture.zh.md#私信与默认实例)，可复用验证入口和未验范围见[测试说明](../testing.zh.md#私信与默认助理)。

## Agent 消息投影

Session 的 `Session identity` 根据服务保存的 `is_direct` 自动标明当前 TeamX 会话是 `direct`（私信）还是 `group`（群聊），与外部通知的来源会话区分。该上下文随 Session 创建、恢复和轮换统一组装，不在每轮输入或每条消息中重复。提及行为统一由公共 Agent 指南规定，角色提示词和发送工具说明不重复定义。已有 Session 保留冻结指令，更新指南后通过正常 Session 轮换采用新指令，不改写历史 Session 文件。

Agent 通过 `read` 读取消息和恢复 Session 时，消息正文 `text` 使用当前提及名称；不额外返回 `display_text`、提及位置或内部成员绑定。原始正文和提及身份仍保存在内部，页面读取保持原有结构。
