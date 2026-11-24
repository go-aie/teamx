# 空间与执行资源准入

日期：2026-11-24。属于[多空间方案 S3b](../WORKSPACE-AND-MULTI-USER-ANALYSIS-PLAN.zh.md#115-第三至五批的装配与验收边界)的部分实现；**S3b、S3 尚未完成，未开放多用户或提供强执行隔离**。

**路线及阶段调整（同日）**：共享核心＋独立 Agent 执行，复用已取得的 S3a.1 独立机制证据，下一步 S3b.1 验证实际 DSH 接入；额度后续复用归 S3b.2a，Git 代执行的 S3b.2b 独立后置。本文额度代码保留，本轮只同步交接，未接入生产 SRT、改动额度行为或补做运行验收。`active_spaces` 是业务空间记录数，`sessions` 是占有 Session 数，不是 worker／prompt 数；新增监督／代理结清仍归原关闭合同。

## 已接入的行为

服务启动时从部署配置创建唯一资源计数器，所有 SpaceRuntime、Room 和 DSH 后端共享同一个对象。新空间及归档后的重开不重置总额，空间配置不能单独提高控制面的额度。配置变更需重启服务。

```toml
[deployment.resources]
spaces = 16
spaces_per_user = 8
active_spaces = 4
active_spaces_per_user = 2
sessions = 32
sessions_per_user = 16
sessions_per_space = 8
```

这些值只是配置示例，不是容量测量结论。各字段缺省或 0 表示不新增该项限制，保留个人组合原有行为；既有空间数量硬上限 32 仍生效。负数或超过 65536 的值拒绝加载。此配置不是托管隔离的合格声明。

- 空间数包含归档空间；活跃空间数包含 active 和 archiving 记录。创建、恢复在持久提交前检查，超额不写入新状态、不启动运行时。启动时先核验已登记的空间；若额度低于现状，拒绝启动，不静默关闭某些空间。归档只有结清并持久保存 suspended 后才释放活跃额度。
- Session 数覆盖已经占有的 DSH Session，包括空闲、恢复、部分打开失败及关闭未结清的实例。启动组件、准备 Session 引用和签发执行凭据之前先取得额度；没有组件返回时释放；返回组件后由原 Session 关闭路径持有。
- 本地关闭与可选 Computer scope 结清后才释放 Session 额度。取消、普通关闭错误、cleanup pending 和超时不释放；已结清但原 Session 不可恢复的错误可释放额度，但保留原错误及恢复限制。重复释放幂等。
- 超额返回现有 busy 错误，不排队、不抢占其他空间或自动轮换 Session。仍由原 Member 生命周期决定暂停、重试和恢复。计数器不记录正文、凭据、消费进度或外部动作结果，也不成为持久业务状态的新 owner。

## 验证与剩余工作

新增测试覆盖跨用户／空间总额、100 个并发准入争用、重复释放、未结清关闭及重试、组件启动前拒绝、归档保留额度、恢复准入、失败恢复不修改版本与状态，以及新空间／恢复沿用同一计数器。既有 Space／DSH 定向回归通过。

验证结果：

- `go test ./internal/room -run 'TestSpace|TestDSH' -count=1`：通过，55.043 秒。
- `go test -race ./internal/room -run 'TestExecutionResource|TestSpaceResource' -count=1`：通过，5.264 秒，包含部分打开失败的额度归属检查。
- `go test ./internal/room -count=1`：465.034 秒，失败一项 `TestSpacesArchivedPendingNotificationsRecoverWithoutReplay`，通知 pending 未在其 5 秒窗口内结清。该夹具未启用新增额度；单独 `-count=3` 复跑全部通过，5.911 秒。尚未确认全量运行时的失败原因，不能将此次全量回归记作通过，未修改通知语义或放宽测试等待。
- `git diff --check`：通过。

本次没有正式容器 worker，也不再以此为当前主线。活跃空间额度不是执行环境或 cgroup 限制；Session 额度也不是 OS 进程、CPU、内存或磁盘硬限制。后续按主方案推进：

1. S3a.1：复用已取得的 SRT 独立机制证据及目录／监督适用条件；旧 Docker／UID 实验不代替新证据，实际 DSH 兼容留 S3b.1 接入与后续组合验收。
2. S3b.1：从实际 Room／成员入口接入执行适配，核心及 manager 留在原进程。保留原关闭权威；代理、桥接和后代结清前不得释放 Session 额度。正常关闭恢复、故障、归档超时与重开的合同均需验证。
3. S3b.2a：首发开放工具、目录／CLI 作用域与基础保护，复用本记录额度及原结果 owner；未适配的托管 Git 在 UI、API／CLI、自动准备及工具转调中关闭，个人 Git 保留。S3b.2b 的 Git 安全适配与精细硬配额分别后置，不承诺资源公平。多用户仍需有限业务额度，数据／执行隔离缺失仍拒绝；只同步后续交接，不改变既有实现或证据。
4. S3b.3：S3b.2a 完成后另行开展首发组合增量验收，不改其实施范围、不等待 S3b.2b；复用本记录及 S3b.1／S3b.2a 的有效证据，只补正式双空间串联、真实外部模型或受影响能力的未覆盖项。已有准入、pending／unknown、恢复和结清用例不默认重跑；所选 Computer／后续托管 Git 按实际证据缺口补对应组合，不把额度已可用视为这些能力已完成。

本记录不替代 S3a 的真实工具链证据，也不能作为 S3b 验收通过或 S4 开放认证的依据。
