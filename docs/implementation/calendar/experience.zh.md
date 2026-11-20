# 会话日历体验与管理

本记录对应[会话日历方案](../../CONVERSATION-CALENDAR-AND-SCHEDULED-TASKS-PLAN.zh.md) C3。在 C1/C2 的同一 CalendarActor、范围查询和前端模块上完成管理体验；调度、领取、发布和恢复沿用既有实现。C4 的跨功能组合验收和发布收尾继续保留。

## 已实施行为

桌面默认绿色周时间轴，「日／周」只选择日历日期范围，均使用时间轴网格；前后导航按日／周步进。两枚图标切换「日历／任务列表」，任务列表按 task_id 每个任务一行，显示标题、规则、下一次执行时刻和任务状态，不受日历日期范围限制，支持任务状态筛选。删除按日期分组的执行条目列表，将原折叠的任务管理提升为独立列表，避免重复入口。任务列表隐藏日期导航、日／周按钮及单次执行筛选；切回日历恢复日期、范围和滚动位置，两种视图分别保留滚动位置。打开时间轴定位当前时刻或首个安排附近；任务使用时间点条目，不虚构占用时长。同小时密集安排可展开，真实执行覆盖同 occurrence 预览。今天使用绿色圆形日期标记；普通周末弱化文字，节假日和补班显示「休／补」并提供完整提示，工作日历未覆盖单独说明，不为普通周末固定预留标注行。日期弹窗统一使用圆形标记，以绿色描边区分未选中的今天，以绿色实心底色和白字标识所选日期，去掉当前周逐日的浅灰底；底部使用「今天」跳转。已跳过／取消条目可以隐藏。

手机默认任务列表，切换日历后默认单日时间轴，保留周范围选择；手机与桌面统一使用网格日期表头，移除额外的一周日期条，通过“今天”、前后切换和日期选择器导航；单日时间轴适配窄屏，无需横向滚动。调整视口大小保留用户已选择的视图和日期范围。完整任务详情使用原生模态抽屉，键盘焦点限制在详情内，支持返回按钮、Escape 和浏览器返回。关闭详情保持原日期、滚动和当前私信，聊天草稿保留。待完善、暂停、远期和取消任务均可在任务列表中找到，任务列表中新建任务默认使用今天，不沿用日历曾查看的其他日期。

详情包含说明、业务范围、规则及固定时区、显示时区、本次生效后的首次／下次时刻、漏跑策略、工作日历覆盖／版本／来源、阻塞原因及历史。历史按 50 条加载，显示保留期说明；可查看本次冻结定义、报告及此前尝试，并跳转私信报告。聊天来源、公开报告和 Agent 保存回执均能返回对应安排。较早报告复用现有消息分页定位，已清理的消息明确提示，报告正文仍可在任务中读取。

编辑复用原表单与 changes 合同，只提交用户修改的完整字段。未改动的已核实结构化 scope、规则时区和单次排除原样保留；待完善可补齐后启用，暂停任务只能显式恢复。保存使用稳定 operation_id 和打开表单时的 task.version；丢失回执重放原请求，版本冲突保留草稿且不自动换版本覆盖。支持暂停／恢复、软取消及跳过下一次。

范围查询使用显示时区的本地日期换算 UTC `[from,to)`，DST 周可能为 167／169 小时。重复候选由服务端既有规则展开。日历执行条目每页最多 200 条，前端按绑定快照读取全部页面，快照过期完整重读一次；再次失败保留已展示投影并显示错误。任务列表使用无日期范围的管理查询，直接读取每个管理响应均包含的完整任务集合及消息关联，不跟随仅用于执行条目的分页游标。任务详情的历史分页复用既有业务投影，完整 run 在用户打开本次详情时读取。管理投影新增显示时区和来源／公开回执关联，历史之外的聊天消息也能对应安排。所有读取只用已提交状态，不创建空日历文件或启动模型。

保存和状态变化复用 calendar SSE 失效通道；未变化快照避免重复重建界面，编辑中的表单不被刷新覆盖。系统显示时区变更触发范围重读，已有任务规则时区保持固定。Agent MCP／CLI 指引和静态提示词同步给出使用真实 task_id 的「查看日历」链接；普通消息链接的协议限制仍保留。

## 验证与证据边界

2026-11-19 的视图调整通过前端 760 项检查及 `TestCalendarBrowser`、`TestCalendarConversationRecoveryBrowser` 两项 Chromium 验收。覆盖日／周网格、独立任务列表的去重及无日期范围查询、视图切换时日期和滚动位置恢复、按范围步进、单日星期和当前时间线位置、休假与补班标记，以及手机任务列表与单日时间轴无横向溢出。手机仍为视口模拟；本次未重新运行下文原交付时的全量 Go 回归。

受控服务验证经过 HTTP、唯一 CalendarActor 和真实文件提交，覆盖完整历史分页、区间外来源／报告关联、只读修订不变、编辑不改写历史／已核实 scope、旧游标与过期任务版本拒绝，以及预览与物化执行同身份去重。日期测试覆盖跨年、UTC 日期差及 DST 长短日／周；Markdown 测试校验日历链接仅在显式本地处理器下开放，并拒绝不完整引用和其他协议。

实际 Chromium 验证桌面 1440×1000 与手机 390×844 视口，截图及[结构化证据](experience/browser-evidence.json)保存在 `experience/`。手机为视口模拟，无真机证据。任务、历史及报告来自隔离目录中的受控服务和真实持久路径，未调用真实模型或外部平台。本阶段未改变领取／执行合同，真实 DSH 沿用 [C1.5](agent-contract.zh.md) 与 [C2](recurrence.zh.md) 的独立证据，不把受控历史当作真实 Agent 长期执行。

检查全部通过：`go test ./...`（Room 包 469.771 秒）、相关 Go race 检查、前端 757 项和最终 Chromium 验收（11.26 秒）。全量回归后的界面修正另经最终前端／Chromium 检查；浏览器夹具加入隔离 settings 文件后再次执行日历、工具桥及相关恢复路径的专项 race 检查。

Chromium 实际覆盖完整范围翻页与游标过期重读、同刻任务、55 条历史分页、冻结报告、较早私信报告定位、双向链接、编辑后保留 scope、真实系统显示时区切换后规则时区不变、版本冲突草稿、其他标签页 SSE 更新、暂停恢复、聊天草稿、日期和滚动返回、手机浏览器 Back、每周工作日表单、单次跳过、待完善补齐、回执丢失重放、保存后刷新失败、覆盖不足、软取消及空日历读取不创建状态文件。控制台无异常。

原交付截图（早于本次任务列表调整）：[桌面周视图](experience/desktop-week.png)、[桌面详情](experience/desktop-detail.png)、[手机按日列表](experience/mobile-day.png)、[手机详情](experience/mobile-detail.png)、[手机空态](experience/mobile-empty.png)。

```sh
go test ./...
go test -race ./internal/calendar ./internal/room \
  -run 'Test(Calendar|Recurring|OfficialWork|WorkCalendar|ExplicitExcluded|Once|TaskIdempotency|PauseCancel|LateOnce|Unknown|ExplicitRetry|IncompleteTask|CreateDefaults|EditReplaces|UnifiedDSH|InputToolsMinimal|UnifiedUnusedProvider|UnifiedMCPAuthentication|MCPStandardHTTPClient|Followup.*(Notice|Clear))' -count=1
node --test internal/web/tests/*_test.mjs
TEAMX_CALENDAR_BROWSER=1 NODE_PATH="$PWD/packaging/verifier/node_modules" \
  TEAMX_CALENDAR_ARTIFACTS="$PWD/docs/implementation/calendar/experience" \
  go test -tags=e2e ./internal/room -run '^TestCalendarBrowser$' -count=1 -v
```

浏览器验收为显式启用，未加入日常 CI。C4 继续验证重复任务、会话切换、服务重启及页面状态的组合，并完成首版发布记录；月视图、拖拽、外部同步、团队房间和真机测试仍不在本阶段交付声明中。
