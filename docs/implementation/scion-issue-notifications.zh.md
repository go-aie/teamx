# Scion open Issue 新建通知首批交付

日期：2026-11-27。按本次授权，将 [N1 方案](../SCION-ISSUE-REVIEW-NOTIFICATION-PLAN.zh.md) 收敛为 **Issue 首批**，Review 留下一批。

## 已实现

- 原 Scion 平台行、通知开关和共用编辑器：已有连接／成员上下文、仓库验证、开始时间、默认 60 秒间隔、暂停／恢复、立即同步和显式重新读取。支持通知单独开启，不要求工具或 Workspace。
- 静态 `scion/workitems` 声明和显式运行绑定；平台适配器负责原生读取、状态、创建键及输入 codec，不引入 Scion 调度器或改变通用通知存储。
- 固定 `scn 0.1.13`，只执行 `repo info` 和 `issue ls --state open`。仓库使用规范名称＋原生创建时间核验；每个 Step 最多一个业务查询。列表从 200 逐步扩大到 2000，截断不推进完整时间。
- 创建时间毫秒下界、原生创建键、持久去重及冻结 Route；同号重开不重发。仓库／类型／开始时间保存后固定，账号变化不继承旧源。
- 业务 stdout 8 MiB、stderr 64 KiB，有界取消并等待进程组结清；复用程序选择、私有凭据快照、账号核验和平台调用生命周期。普通原生 exit 5 保留业务错误，不直接判定凭据过期。
- 现有成员私有输入存储、正式输入读取和持久确认，包含应答丢失防重、过期／来源释放证据和容量保护。服务端拒绝共享 Inbox 目标。
- Scion 不注册 followup／写动作。缺少解析能力时明确返回 `unsupported`，已授权保存正文及原链接仍可正式读取。

## 验证

组件／集成覆盖时间边界、相同毫秒不同 Issue、重复列表／重启、保存失败、截断扩大与上限、容量阻挡／释放、过期终态证据、codec 往返与摘要防篡改、管理版本和范围锁定、通知不授工具、账号恢复／换号、取消／暂停／退出登录／凭据撤权及临时文件结清。GitLab／飞书受影响通知路径另有回归。

```sh
make test TEST_PACKAGES='./internal/platform/scion ./internal/app/builtinplatforms/scion ./internal/app/platforms'
make test-race TEST_PACKAGES='./internal/platform/scion ./internal/app/builtinplatforms/scion ./internal/room' TEST_RUN='TestScionNotification|TestScionWorkitems'
node --test internal/web/tests/web_scion_notifications_test.mjs
```

相关前端回归共 213 项通过，Scion／静态装配包测试、通知与来源解析定向 race 测试通过。新增 Scion 后，三个仍固定两平台数量的输入存储／Session／可选 codec 测试断言已更新，定向复跑通过。

整包 `./internal/room` 核心回归**未通过**：`TestCancelEndpointBindsOperatorMemberAndExecution` 的旧停止提示断言失败，`TestDiscussionRestartSettlesWholeTeamBeforeClearing` 存在异步状态断言失败，且整包触及默认 3 分钟上限。用只读 Go 编译 overlay 撤去本次 Go 改动、保留其他工作区内容后，停止提示失败仍可复现；讨论重启当前／对照各重复 10 次，均失败 7 次。未修改这两个既有测试或其功能，也不把定向通过表述为整包全绿。

独立真实只读验收使用隔离服务根和凭据副本：Chromium 正常页面验证／保存 `OpenCSG/scion`，选择最近一天；后台 worker 采集并投递 **5 条**当时仍为 open 的 Issue，正式输入工具读取并确认 **1 条**，其他成员无输入。随后关闭／重开服务，再次同步仍为 5 条，仓库与冻结起点保持；另用独立测试子进程打开同一隔离服务根，完成新的原生列表读取，仍保留 5 条输入及原先 1 条正式确认。桌面、手机编辑器和重启后页面均有独立截图，页面错误为 0。

本地私有证据目录：`.local/scion-issue-notifications-20261127-08/`，含 `report.json`、`recovery-report.json`、页面截图及隔离持久状态；不将平台正文和凭据提交为 fixture。复跑使用新输出目录：

```sh
TEAMX_SCION_NOTIFICATION_REAL=1 \
TEAMX_SCION_NOTIFICATION_OUTPUT=/absolute/fresh-output \
TEAMX_SCION_TEST_CREDENTIALS=/absolute/credentials-copy.json \
TEAMX_SCION_BIN=/absolute/scn \
go test -tags=e2e ./internal/room -run '^TestScionNotificationRealReadOnly$' -count=1 -v -timeout=4m
```

## 边界

后续 N1b 已补成员对话入口，见 [Scion 对话配置](scion-conversation-configuration.zh.md)；以下保留 N1 首批交付时的验收边界。

只覆盖查询时仍为 open 的 Issue 新建通知。未开放 Review、评论、评审动态、通知对话配置、类型编辑、仓库替换、共享 Inbox 或 Linux 多用户 Scion。轮询间新建又关闭且未被看见的 Issue 不在覆盖保证内。

本次真实证据是原生平台读取、页面、后台 worker、正式输入工具与服务关闭／重开、独立进程恢复；未运行真实模型 DSH，也未做代表规模／发现延迟验收。没有创建远端 Issue／Review，没有给正式服务启用订阅，没有部署。
