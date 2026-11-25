# Agent 自动轮换与故障恢复

新格式 Room 的自动轮换读取统一输入状态，不再打开旧 Inbox。取得生命周期锁后重新检查当前输入，成员 Actor 再核对运行意愿、会话代次和执行状态。旧格式 Room 保留原入口。

临时的准入超时不会把会话标为损坏。网络、服务端临时故障、HTTP 408/429 和短期限流会在执行结清后自动恢复；恢复等待依次为 1 秒、15 秒、1 分钟、5 分钟，此后每 15 分钟重试。次数、阶段、诊断和下次时间保存在成员的 `collaboration.json.session_recovery` 中，成功完成工作后清除。历史的一次性恢复记录转换为保留已用次数的冷却重试。

轮换中途发生可重试故障时继续同一个操作编号和目标代次；每次仍经过生命周期与动作结果检查。用户暂停优先于异步恢复。状态损坏、提交结果不确定、没有结清证明、认证失败、模型不存在或明确的额度耗尽不会通过自动换会话绕过。新 Session 打开失败且没有独立结清证据时也保持阻塞。

界面在可恢复等待期间显示下次重试时间，保留暂停入口；收件校验失败单独显示诊断，不据此要求重置私有会话。当前 ACP 错误未提供结构化 `Retry-After`，等待时间采用本地退避；只有明确的 `insufficient_quota` / `quota_exceeded` 代码被识别为额度耗尽。

回归入口：

```sh
go test ./internal/room ./internal/agent/dsh -run 'Test(UnifiedInputAutomatic|AutomaticRotation|AutomaticRecovery|SessionRecovery|ExecutionNotice|RPCErrorSafeProvider)' -count=1
go test -race ./internal/room -run 'Test(UnifiedInputAutomatic|AutomaticRotation|AutomaticRecovery|SessionRecovery)' -count=1
node --test internal/web/tests/web_app_test.mjs
```

覆盖新格式平台通知与 Room 原生消费连续跨两次会话阈值、临时准入失败、继续原轮换操作、连续模型服务故障与最终恢复、冷却状态重新加载、旧恢复记录转换、手动暂停，以及损坏输入不授权会话重置。上述模型执行使用本地组件替身，不代表真实 DSH 或线上平台验收。

已有服务需加载修正版。因旧轮换入口误判而停止、仍保留启用意愿的成员，会在正常启动恢复后重新进入调度；不会自动启用用户已暂停的成员。

本次验证：相关集成与 race 回归通过，包含显式重新开始讨论、启用、轮换和自动恢复的组合；877 项前端测试、静态检查与构建通过。全仓集成发现飞书两组 scope 测试的模拟器缺少现有的 `docx:document:readonly` 权限；这些文件未修改。Room 整套集成未在 8 分钟总时限内完成，超时时正在开始新用例，不能计为完整通过。运行中的服务未切换版本。
