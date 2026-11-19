# 私信一次性定时任务交付记录

2026-11-19。对应会话日历方案 C1。真实验收的结构化记录见 [once-evidence.json](once-evidence.json)。

## 已交付范围

- `internal/calendar` 保存一次性任务、固定时区规则、任务／定义／执行版本、操作幂等回执及冻结执行快照。只开放 `once + all_days`，时间为分钟精度；DST 不存在／重复的本地时刻拒绝保存。信息不足可保存 `needs_info`。
- 每进程一个 CalendarService／公共循环，每个使用日历的完整 RoomIdentity 一个 CalendarActor。变更在 Actor 队列排序，受控 worker 保存候选，可靠保存后发布投影。无日历文件的会话不恢复日历 Actor。受管与单房间服务使用同一装配；受管恢复独立于浏览器选择。
- 聊天 MCP、CLI 和日程管理接口共用服务，支持创建、读取、完整编辑、暂停／恢复、取消，以及对已核实失败、阻塞或漏跑的显式重试。Agent 变更要求本私信真实 human 消息；表单来源由服务生成。相同 operation_id 重用原回执，异正文冲突，过期资源版本不覆盖。
- 到期先保存 run 和事件意图，再向指定 membership 投递稳定 `calendar_due`。原有 Agent 回合门禁决定执行；附加就绪不会启用暂停 Agent。每回合最多领取一个 run，领取与 finish 绑定原 generation／turn。Session 凭据本身不能领取；CLI 还携带私有逐回合桥接凭据，业务 JSON 不携带这些凭据。
- finish 先保存完整报告和发布意图，再以原成员身份公开发布，取得 seq 后才进入结果终态。与跟进提醒共享独立于可见历史的持久公开消息回执。同 ID 异正文拒绝；普通私有模型输出不算报告。
- 一次性任务默认在到期一小时内补跑，超时漏跑；暂停期间不补跑。无进展最多继续两次，次数持久保存。失败或未知结果不自动重做业务。暂停／取消只请求停止原领取回合，整个回合结清前保留占用。重启讨论先暂停日历；单纯清空可见历史保留安排和发送回执。
- 私信标题下提供「聊天／日历」，使用最终功能模块 `features/calendar.mjs`。最小日程列表显示安排、状态、任务详情和报告，可以暂停、恢复和取消；切换保留聊天草稿。更新复用现有 SSE 失效／重读通道。

## 验证证据

真实 DSH／模型在隔离目录按聊天创建近未来任务，正常服务调度后由下一回合领取和 finish，报告发布回同一私信。记录核对了 task、run、generation／turn、结果正文与公开 seq。任务只发送本地测试提醒，没有平台读写；这不代表已经验收真实项目统计数据的完整性。

Chromium 实际浏览器覆盖 1280×900 桌面与 390×844 手机视口，验证任务可见、暂停／恢复和聊天草稿保留，控制台无异常。手机证据为视口模拟，没有真机证据。

受控测试覆盖任务版本与幂等、DST、一次性漏跑、无进展次数恢复、冻结快照、显式重试、身份拒绝、唯一 Actor、跨会话延迟提交隔离、无新 Room 未读仍经正常回合处理且不重投事件、公开报告确认丢失后清空历史再重启复用原 seq、异正文冲突，以及取消原回合和未结清时保持目录锁。跟进提醒及原输入消费合同继续执行回归。

本次检查通过：`go test ./...`（Room 包 467.333 秒）、相关 race 检查、前端 754 项测试、真实 DSH 一次性闭环，以及 Chromium 桌面／手机视口验收。全量回归后补充的未知结果保留、取消草稿恢复、无新未读及跨会话隔离边界另经相关单测和 race 验证。

复现入口（真实模型与浏览器不加入日常 CI）：

```sh
go test ./...
go test -race ./internal/calendar ./internal/room \
  -run 'Test(Calendar|Once|TaskIdempotency|PauseCancel|LateOnce|Unknown|ExplicitRetry|IncompleteTask|UnifiedDSH|InputToolsMinimal|UnifiedUnusedProvider|UnifiedMCPAuthentication|MCPStandardHTTPClient|Followup.*(Notice|Clear))' -count=1
node --test internal/web/tests/*_test.mjs
TEAMX_CALENDAR_BROWSER=1 NODE_PATH="$PWD/packaging/verifier/node_modules" \
  go test ./internal/room -run '^TestCalendarBrowser$' -count=1 -v
TEAMX_CALENDAR_DSH_REAL=1 TEAMX_INPUT_COMPARISON_CONFIG="$PWD/.teamx/config.toml" \
  TEAMX_CALENDAR_OUTPUT=/absolute/new-calendar-evidence \
  go test -tags=e2e ./internal/room -run '^TestCalendarRealDSH$' -count=1 -v
```

真实验证要求输出目录尚不存在；配置使用已有默认模型凭据，只读取配置及凭据，不改生产会话数据。

## 后续边界

C2 才开放 daily／weekly、工作日、年度节假日数据、单次排除和多周期水位；C3 扩展同一个前端模块的周视图、范围投影、完整管理和互相跳转；C4 做组合验收及发布收尾。当前日程读取为有界任务列表及执行记录分页，不声称已交付最终周视图或完整 occurrence 范围投影。
