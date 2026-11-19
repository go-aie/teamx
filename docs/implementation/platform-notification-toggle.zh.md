# 平台通知总开关语义

日期：2026-11-18。对应[平台通知默认值与对话选择方案](../PLATFORM-NOTIFICATION-DEFAULTS-AND-SELECTION-PLAN.zh.md)第 1 步。只完成配置语义与本地回归；第 2 步的一步入口和第 3 步的卡片、公共导航及真实验收仍待实施。

## 当前行为

- 飞书和 GitLab 均支持完整参数模式的通知 `--enabled true|false`。首次纯开启分别复用 `m.defaultLarkConfig` 和 `DefaultNotificationConfig`，与手动 onboarding 一致。
- 已有配置只切换总开关，保留来源、指定范围、筛选、间隔、已锁定起点与持久采集进度。飞书双分支和 GitLab 待办＋活动可整体恢复；显式单来源操作仍保留防扩张检查。
- 无配置纯关闭保存 `completed/preserved` 回执，不创建配置、订阅或接收起点；已正确应用的同状态请求同样 preserved，不增加版本或重启采集器。
- GitLab saved version、实际来源订阅和 BackfillLocked 均可证明已有配置；默认草稿不算配置，desired version 为 0 的旧采集器仍保留原意图。
- 首次定制不带入额外默认来源。总开关与来源组合时，显式 false 决定最终暂停；已有配置没有来源时，纯开启提示先选择来源。GitLab 手动配置和 onboarding 同步拒绝无来源开启。
- GitLab applying、pending/errors 和原 unknown 保持原恢复机制；通知及工具绑定独立，账号、成员、human seq、版本和持久化回执校验保持有效。

## 使用与范围

仍需恢复原操作、读取准确版本，并提交完整参数：

```sh
"$TEAMX_CLI" platforms member notifications --platform-id ID --platform-version V \
  --notification-version N --operation-id OP_ID --source-seq SEQ --enabled true
```

CLI 帮助、平台 Skill 和[通知技术参考](../reference/notifications.zh.md)已同步上述语义。尚不支持省略版本与操作 ID，也未修改 get 当前观测协议、选择卡片、Session 指导或生产通知配置。

## 验证

- 成员配置与观察保护回归通过，覆盖来源隔离、定制、陈旧版本、unknown 和部分应用。
- 新增两平台真实配置服务测试：首次默认、无配置关闭、同状态回执、指定范围关开和持久进度；CLI 显式 false；GitLab 旧采集器及 false 优先。
- 集成测试直接比对手动 onboarding 与成员配置的规范化默认结果；方案指定的成员／onboarding／Lark search／selection 集成回归通过。
- Web 通知、onboarding 和 selection 共 144 项测试通过；平台 Skill 所在包测试通过；e2e 标签仅编译通过。
- 全包 `go test ./internal/room ./internal/platform/lark ./internal/platform/gitlab` 本次未通过：`TestDefaultAssistantConnectionAutomaticContinuationAndFailure` 遇到 `ErrBusy`，随后 room 包触及默认 10 分钟总超时，当时正在执行 `TestToolIndependentTargetsMergeAndProjectActualCommit`。这两个用例单独合并复跑通过（11.488 秒），尚不能据此把全包结果记为通过。Lark／GitLab 包测试通过。

本次未运行真实 DSH、模型、Chromium 或真实提供方验收，未验证实际通知到达与提醒送达。上述本地证据不代表个人助理的一步配置和卡片路径已经完成。
