# 对话式平台接入：连接 CLI 实施记录

日期：2026-11-11。对应方案 C1；C2 后续实施见 [成员配置 CLI](MEMBER-CONFIG.zh.md)，C3 连接组件、本地验证及单账号真实组合验收见 [最小对话连接](CONNECTION-UI.zh.md)；C4 见 [Inbox 类型扩展](INBOX-EVENTS.zh.md)；C5–C6 尚未实施。本阶段证据为本地服务、模拟官方 CLI 和真实 TeamX CLI 子进程，不代表真实飞书、Agent 或浏览器端到端验收。

## 已交付

- `platforms skill`：通过现有嵌入式只读 Skill 机制提供连接流程。校验当前完整 Room／Agent／Membership 身份，不要求已有平台或工具绑定；Agent 指南仅增加发现入口。
- `platforms list`：列出飞书连接 ID、名称、账号、当前版本及可用状态，不返回凭据。已有连接复用必须指定 ID 和观察到的版本，不自动换账号。
- `platforms connect start/status/wait/cancel`：固定操作 ID、用户消息关联、分项回执、稳定交互引用及版本；保留原 `platforms upgrade`。
- 复用设置页的飞书配置／授权进程、串行准入和配置锁。新连接使用服务分配的全新 profile；Agent 不能指定原生可执行文件、现有 profile、凭据或伪造原始用户文本。
- 只有指定 profile 的用户账号核验及连接保存均成功才返回 `completed`。仅连接不创建通知订阅、工具绑定或发送能力。

## 命令与最小契约

命令在当前 Agent 的完整绑定环境中执行；人类终端可查看 `--help`，完整管理继续使用设置页。业务入参暂为 `provider=lark`、连接名称，以及可选的已有连接 ID／版本。

```sh
teamx platforms skill
teamx platforms list
teamx platforms connect status --source-seq 18
teamx platforms connect start --provider lark --name '工作飞书' \
  --operation-id 0123456789abcdef0123456789abcdef --source-seq 18
teamx platforms connect status --operation-id 0123456789abcdef0123456789abcdef
teamx platforms connect wait --operation-id 0123456789abcdef0123456789abcdef --timeout 20s
teamx platforms connect cancel --operation-id 0123456789abcdef0123456789abcdef
```

`start` 读取并保存真实 human Room 消息。同 ID、同固定入参及原消息返回原操作；变更入参冲突。同一成员／原消息换新 ID 也被拒绝，避免丢失响应后重复配置应用。`status --source-seq` 可恢复原 ID；仅明确的 `operation_not_found` 表示该成员／请求无关联操作，权限与传输失败不表示不存在。

操作保存在服务数据目录的 `platform-connect/`，记录完整成员、原始用户证据、固定入参、公开 profile 元数据、账号核验回执及独立分项结果。平台配置仍以现有 registry 为权威，操作中的账号仅是历史回执。

- `version`：持久化回执修订；`result_version`：结清结果版本，运行中为 0，终态为 1。
- `stage`：`application`、`authorization`、`connection`、`complete`。
- `status`：`running`、`waiting`、`cancelling`、`completed`、`cancelled`、`expired`、`failed`、`unknown`。
- `application`、`authorization`、`connection`：分别表示应用配置、用户授权和连接保存，部分成功不会合并成连接成功。
- `interaction`：稳定 `id=operation_id` 与 `kind=lark_authorization`。授权链接／二维码地址仅在活动会话的查询投影中提供，不写入操作文件；Secret、Token、device code、临时会话 ID 均不进入操作记录。

`wait` 只在 CLI 按 250ms 节奏查询原操作，整体默认 20 秒、最多 2 分钟，每次请求最多 5 秒。到期返回 `pending=true`；CLI 退出、观察请求取消或 wait 中断均不取消后台操作。授权交互需先发布链接，再等待；应用配置和用户授权可能分别产生链接。

`cancel` 先持久化取消请求，再停止原进程并有界等待结清。尚未结清时返回 `cancelling`，不提前伪造终态；晚到的成功只记录已发生的部分效果，不再保存连接。取消不撤销既有应用配置、用户登录或已完成连接。

## 故障与恢复

- `status` 纯读，不触发授权、连接保存或恢复写入。当前已完成回执是历史事实；当前账号可用性由 `platforms list` 核对。
- 服务启动时，只按原平台 ID、名称、profile 和已核验账号检查已保存结果；匹配时补齐成功回执，不重建连接。无可确认结果时记为 `unknown`，不自动重放授权。已保存取消请求不会复活。
- 不恢复临时 OAuth 会话。部分配置或中断授权需要通过现有设置入口明确恢复，不在查询时重新配置应用。
- 保存结果不确定时沿用 Store 的 commit-unknown 写入冻结，当前请求不报告成功；重启后按磁盘结果核对，不能改 ID 重试。
- 授权结束后再次检查原成员仍存在；成员退出或替换不会将连接操作转交给同名新成员。原 human 管理接口的调用检查保持原有边界。

## 验证

```sh
go test ./... -timeout=3m
go test -race -tags=integration ./internal/room \
  -run '^TestPlatformConnect|^TestLark(Setup|Auth|Login|Logout)|^TestPlatformsUpgrade' \
  -count=1 -timeout 120s
```

覆盖：正常保存、无工具绑定读取 Skill、真实用户依据与伪造字段拒绝、重复 ID／改变入参、按原消息恢复、已有连接版本复查、wait 超时和中断、真实 CLI start 进程退出后后台继续、显式取消及晚到结果、授权过期、应用配置成功后授权失败、原成员移除、结果保存前后不确定、结果保存后重启及恢复期间不重复写连接。原设置的配置／登录／退出路径一并回归。

结果：全仓普通测试及上述 C1／原授权路径 race 集成测试通过。额外加入 `^TestOnboarding` 的扩大回归存在一项既有失败：`TestOnboardingLarkDefaultsAndRestart` 在第 163 行报 `wrong Lark defaults`。在独立、无本次改动的 HEAD `62795a4` 工作树运行同一测试也失败；当前生产使用搜索采集默认配置，该旧测试仍检查历史默认值。本次未修改这项既有测试或通知配置语义。

## 后续边界

群搜索／分页与成员配置已在后续 C2 交付；对话组件、浏览器交互与设置回流属于 C3／C6；Inbox 类型扩展及自动接续属于 C4／C5。C1 本身没有通知订阅、自动唤醒、群选择组件或真实平台／Agent／浏览器验收结论。
