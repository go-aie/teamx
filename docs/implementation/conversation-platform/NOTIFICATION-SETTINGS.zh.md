# 对话通知配置扩展实施记录

日期：2026-11-12。对应[对话式平台设置方案](../../CONVERSATION-PLATFORM-SETTINGS-PLAN.zh.md) S1。范围为当前成员的飞书基础通知配置；GitLab 来源／筛选和工具独立配置沿用已有实现。指定私聊选择、完整通知卡片、高级参数及共享草稿均未实施。

## 行为与命令

明确的真实 human 指令通过已有成员配置服务直接执行。先读取 `platforms list`、原 `member status` 和当前 `member get`，保留完整成员、精确账号、连接版本及通知版本，将同一请求的修改合成一个稳定操作。无需新增通知草稿或确认卡片。

以下均为 `platforms member notifications` 的飞书参数，且仍需提供 `--platform-id`、`--platform-version`、`--notification-version`、`--operation-id` 和 `--source-seq`：

| 设置 | 参数与语义 |
| --- | --- |
| 增加群 | 重复 `--add-chat-id ID`；保留原关注时开启该群分支和总通知的行为 |
| 移除群 | 重复 `--remove-chat-id ID`；无需远端仍可访问，未关注的 ID 不产生变更 |
| 完整群列表 | 重复 `--group-chat-id ID`；单个 `--group-chat-id ''` 显式清空，省略则保留 |
| 群范围 | `--group-scope all` 或 `specified`；全部切指定必须同时提供完整列表 |
| 群开关 | `--group-enabled true/false`，关闭保留范围和条件 |
| 群条件 | 原 `--group-message-mode all/mentions`，新增独立 `--group-at-me true/false` 和 `--group-at-all true/false` |
| 私聊开关 | `--private-enabled true/false`，保留既有指定范围及列表 |
| 全部私聊 | `--private-scope all`；需要开启时显式提供私聊开关 |

例如，用户明确要求只接收两个群、关闭私聊且只保留 @我时，一次提交：

```sh
teamx platforms member notifications --platform-id P --platform-version V \
  --notification-version N --operation-id STEP_ID --source-seq SEQ \
  --group-scope specified --group-chat-id oc_ONE --group-chat-id oc_TWO \
  --group-enabled true --private-enabled false \
  --group-message-mode mentions --group-at-me true --group-at-all false
```

`P/V/N` 必须来自当前实际查询，群 ID 必须是该账号下经核验的真实群；每条新请求使用其真实 human 消息，不能复用此例占位值或换 ID 重试未知提交。

只调整范围或条件不自动启用通知。显式开启群／私聊分支会恢复总通知，除非同时明确 `--enabled false`；如果恢复会同时激活未请求的休眠来源，返回冲突，需明确选择两类来源。原追加关注同样不能隐式唤醒其他休眠来源。

移除或清空最后一个指定群会关闭群分支；无其他启用类别时同时关闭总通知。显式要求开启空的指定范围返回冲突。全部群无排除列表，不能将取消某群变为枚举可见群；增删与完整替换互斥，同一 ID 不能同时增删。全部消息模式保留提及偏好但不按其过滤；仅提及模式下显式关闭两种提及条件返回冲突。

## 实现与恢复

- 飞书补丁类型、语义合成和无变更判断集中于 `platform_member_lark.go`；`platform_member.go` 继续负责身份、来源、操作去重、核验和提交，不新增配置权威。
- JSON 输入使用 `groups`／`private` 类型化补丁，独立可选布尔值保留显式 false；`groups.chat_ids` 用可选列表区分省略和 `[]`。列表状态可通过持久化、状态查询与重启读取保留；旧追加回执仍按原字段读取，无需迁移。
- 追加与完整替换逐个核验群元数据，回执冻结对应精确 ID 的证据；移除和单独关闭不读取远端会话。远端读取后和配置写入前复查完整成员、账号与版本。
- 修改复用原 `configureLarkSubscription`，保留已存在的历史起点和正常采集进度。首次实际开启的新订阅沿原保存时起点规则；关闭不存在的订阅不会创建新订阅或提前锁定起点。
- 未发生配置变化时保存 `completed/result.status=preserved` 回执，版本保持原值；没有订阅时版本为 0，不调用配置服务。真正变更仍先保存 `unknown` 围栏，再写权威配置，最后保存完成回执。
- 同 ID／输入查询原回执，改变输入冲突，同来源换 ID 不绕过限制。未知回执不重放，重启后仍先查原操作和当前配置；配置冲突不套最新版本盲目重试。
- 新字段拒绝跨 GitLab／工具操作使用。新指定私聊范围、私聊列表和轮询参数仍被拒绝；原 human 设置接口及账号／群／项目选择器语义保持。
- 内置平台 Skill 与 CLI 帮助同步更新：已生效配置的明确请求直接修改；旧选择卡片取消勾选不代表移除；不承诺读取浏览器未提交输入；设置保存、Inbox 收件和提醒送达分别反馈。

## 验证边界

新增行为测试覆盖群增删／替换、显式空列表、群／私聊启停、独立提及、休眠来源冲突、无订阅关闭不落订阅、非法输入与 provider 边界。集成测试使用真实 TeamX CLI 子进程和模拟官方飞书 CLI，验证准确群类型、完整范围修改、原起点／工具保留、原回执重复查询及未知回执重启不重放。

同时修正成员配置集成 fixture 对新版搜索 fixture 的私聊类型覆盖，避免测试将私聊误当群；不修改生产查询或放宽群核验。

以下检查均通过：

```sh
go test ./... -count=1 -timeout=8m
go test -tags=integration ./internal/room \
  -run '^TestPlatformMemberLark' -count=1 -timeout=3m
go test -race -tags=integration ./internal/room \
  -run '^Test(PlatformMember|PlatformSelection|GitLabMember|GitLabProject|GitLabAccountSelection)' \
  -count=1 -timeout=4m
node --test --test-reporter=dot internal/web/tests/web_*test.mjs
git diff --check
```

新增文件另做空白检查，方案与实施记录的本地链接及代码围栏已核验。真实飞书／GitLab 账号、模型与浏览器组合验收属于 S3，本次不将模拟提供方、CLI 或 Web 单元测试当作真实端到端通过；指定私聊属于 S2。
