# 私信日历首版交付与组合验收

本记录对应[会话日历方案](../../CONVERSATION-CALENDAR-AND-SCHEDULED-TASKS-PLAN.zh.md) C4，完成 C1 → C1.5 → C2 → C3 之后的组合回归、读者文档与首版交付收尾。没有新增调度机制或任务类型，也没有改动 Agent 工具合同。这里的交付指仓库实现与验收完成，不表示已经发布版本或部署到用户服务。

## 首版能力与使用入口

私信可从聊天或标题下的日历建立一次性、每天、每周指定星期的任务，按每天、周一至周五或中国大陆法定工作日（含补班）筛选。规则保存固定时区，工作日历离线覆盖 2026、2027 年，覆盖外阻塞。任务可编辑后续安排、暂停／恢复、取消、跳过单次或保存为待完善；日历提供桌面周视图、手机按日列表、详情、历史及私信报告跳转。

安排属于创建它的私信，经 CalendarActor 可靠保存并在到点后交由现有 Agent 回合执行，结果回到同一私信。切换会话和关闭浏览器不影响后台任务；正常服务恢复不依赖逐一打开私信，也不自动启用暂停的 Agent 或任务。停机仅补最近一次且到期不超过一小时的执行，旧周期记录漏跑；失败和未知结果不自动重做。

操作说明见[会话指南](../../conversations.zh.md#私信日历与定时任务)，执行与工具合同见[Agent 运行时](../../agent-runtime.zh.md#私信定时任务)。月视图、拖拽、团队房间、更多地区、外部日历同步、跨会话代管及真机验收继续保留为后续范围。

## 组合场景

新增回归使用隔离目录、真实 roomManager／Room／CalendarActor、公共调度循环和文件提交路径，只替换模型组件及用于跨周期的时间输入。创建和启停经过管理 HTTP；claim／finish 使用实际回合绑定进入同一服务适配，报告通过正式持久发布路径生成。不能把这些报告称为真实模型生成。

| 场景 | 验证结果与入口 |
| --- | --- |
| 同角色的两条私信 | 相同创建 operation_id 得到各自任务；唯一会话 Actor 和共享调度器不合并任务或报告。`TestCalendarManagedRecurringPauseRecoveryAndPublication` |
| 到期排队 → 关闭 → 未选中恢复 | 不调用 rooms/open 或读取日历，启动恢复即装配已有日历；排队 run 保留、暂停 Agent 不启动、旧服务 epoch 的修改被拒绝 |
| 公开报告 → 暂停另一任务 → 重启 → 恢复 | 使用真实运行回合领取和完成；任务暂停持久保留，恢复后可办理，重复任务继续 active 并保留下次时刻；再次重启仍只保留原 run 和公开 seq，两条私信互不串报告 |
| 跨四个周期的离线恢复 → 再次重启 | 用过去的受控提交时间保存规则，正式启动后由公共调度循环按当前时间计算：三次旧周期记为 skipped_late，最近一次在一小时内入队；再次启动不重建旧周期，暂停 Agent 的 Session 回合数始终为 0。`TestCalendarManagedOfflineRecoveryOnlyLatestWhileAgentsPaused` |
| 页面切换 → 未选中会话发布 → 重建两次 | 实际 Chromium 显示每条私信独立任务与聊天草稿，丢弃切换后才返回的前一会话范围响应；甲页面打开时乙报告发布。重建后页面自动重新识别 epoch，甲任务仍暂停；显式恢复后显示活动安排。乙历史、报告和原 seq 保留，修改请求没有自动重放。`TestCalendarConversationRecoveryBrowser` |

这里的“重建”是在同一测试进程关闭管理器和运行时、释放锁后，从原隔离目录重新装配服务；不是宿主崩溃或强制杀进程测试。跨日使用受控时间，不等待真实隔天，不声明长期实跑。

浏览器组合证据：[结构化结果](release/combination-evidence.json)、[桌面恢复](release/desktop-recovered.png)、[手机恢复后的历史](release/mobile-recovered-history.png)。桌面为 Chromium 1440×1000，手机为 Chromium 390×844 模拟视口，无真机证据。页面控制台无异常。保存请求总数为四次显式暂停／恢复，两次重建均没有重放请求。

## 前阶段证据与保留边界

| 证据类型 | 已有记录与本次处理 |
| --- | --- |
| 真实 DSH／模型 | 沿用 2026-11-19 的 [C1 一次性](once.zh.md)、[C1.5 最终工具合同](agent-contract.zh.md)、[C2 重复工作日](recurrence.zh.md)证据，模型为 deepseek-v4.1-flash-1017；均核实本私信的唯一公开报告。C4 未改变合同，本次没有重跑真实模型 |
| 真实外部平台 | 日历验收没有读取真实项目或写入外部平台；现有提醒报告不能证明项目统计、当前迭代切换或权限失效场景已经通过真实业务验收。项目任务继续复用已有连接和工具，必须有已核实范围；不构造缺失数据的统计结果 |
| 规则与生命周期 | 本次重跑日历专项、相关 race 与集成检查，复验 DST、工作日覆盖与补班、漏跑水位、取消／暂停、领取门禁、未知提交、历史清理后的原报告回执和原跟进提醒路径 |
| 浏览器 | 本次同时重跑 C3 的完整管理脚本与新增组合脚本。完整管理证据见 [browser-evidence.json](release/browser-evidence.json)，覆盖分页、历史、编辑、时区、SSE、回执故障、草稿、跳转、覆盖不足和空日历只读 |
| 真机与故障接管 | 无真机、宿主崩溃、强杀或多年实际运行证据；不由浏览器视口、正常关闭恢复或受控时间测试替代 |

## 复验方法

普通回归包含新增受管恢复测试，真实模型和浏览器继续显式启用，不加入日常 CI：

```sh
go test ./...
go test -race ./internal/calendar ./internal/room \
  -run 'Test(Calendar|Recurring|OfficialWork|WorkCalendar|ExplicitExcluded|Once|TaskIdempotency|PauseCancel|LateOnce|Unknown|ExplicitRetry|IncompleteTask|CreateDefaults|EditReplaces|UnifiedDSH|InputToolsMinimal|UnifiedUnusedProvider|UnifiedMCPAuthentication|MCPStandardHTTPClient|Followup.*(Notice|Clear))' -count=1
go test -tags=integration ./internal/room \
  -run 'Test(Calendar|RuntimeRecoveryRestoresEnabledRoomsWithoutSelection|RuntimeRecoveryFailureAndSlowFactoryAreLocal|DirectConversationCreationIsolationAndFrozenRecovery|Followup.*(Notice|Clear))' -count=1
node --test internal/web/tests/*_test.mjs
TEAMX_CALENDAR_BROWSER=1 NODE_PATH="$PWD/packaging/verifier/node_modules" \
  TEAMX_CALENDAR_ARTIFACTS="$PWD/docs/implementation/calendar/release" \
  go test ./internal/room -run '^TestCalendar(Browser|ConversationRecoveryBrowser)$' -count=1 -v
```

真实 DSH 的独立复验方法保留在上述 C1.5／C2 记录，要求新建隔离输出目录。最终检查结果见 [validation.json](release/validation.json)。

本次检查全部通过：`go test ./...`（Room 包 507.660 秒）、相关 Go race、集成检查、前端 757 项、完整管理 Chromium 和组合 Chromium；组合脚本另经最终 race 验证。全量回归编译后新增的离线恢复用例和夹具收尾，由最终受管用例 race／集成及浏览器 race 覆盖。最终组合浏览器 race 用时 14.856 秒。
