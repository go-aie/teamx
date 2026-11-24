# 私信重复任务与工作日交付记录

2026-11-19，对应[会话日历方案](../../CONVERSATION-CALENDAR-AND-SCHEDULED-TASKS-PLAN.zh.md) C2。规则与真实 DSH／浏览器记录见 recurrence-evidence.json（本地验收文件 `recurrence-evidence.json`），界面见浏览器截图（本地验收文件 `recurrence-browser.png`）。C1／C1.5 的执行、发布和逐回合身份机制继续复用。

## 交付行为

- 支持 once／daily／weekly，weekly 使用 ISO 星期 1–7。重复任务省略 start_date 时，服务按提交时的规则时区保存当天日期，再取提交之后的首个有效候选；明确提供的日期、时区与漏跑策略保留。end_date 包含当天，excluded_dates 直接排除、不顺延。活动一次性任务被日期筛选排除时拒绝保存。
- 规则按本地日期推进，固定保存 IANA 时区；修改系统显示时区不移动任务。重复 DST 空缺跳过，折叠只取第一次；一次性空缺和歧义时刻拒绝。预览、到期判断及单次跳过共用规则函数。
- all_days 包含周末和节假日；weekdays 只按周一至周五；business_days 必须指定 CN，表示中国大陆法定工作日，包含调休补班。年度 JSON 随版本离线发布，服务启动校验完整年度覆盖、元数据、重复键、矛盾日期及年度重叠。覆盖外为 unknown，不能以星期兜底。
- 离线数据覆盖 2026-01-01 至 2027-12-31，来源为[2026 年国务院办公厅通知（北京市政府转载）](https://www.beijing.gov.cn/fuwu/bmfw/sy/jrts/202511/t20251104_4258838.html)及[2027 年国务院办公厅通知](https://www.gov.cn/zhengce/content/202611/content_7085101.htm)，后者同时核对[人社部转载](https://chinajob.mohrss.gov.cn/h5/c/2026-11-09/635850.shtml)。数据文件记录版本、来源、发布日期、休息日及补班覆盖；不是模型生成或运行时抓取。
- 覆盖不足时保留未决候选和阻塞原因，未过原一小时截止时间可在更新数据后恢复，过期则保留漏跑。已开始／结束的 run 冻结日历版本；尚未开始的候选重新核实新版数据，不能继续使用已失效的工作日资格。每天／周一至周五不依赖年度数据执行。
- 每个任务持久保存生效边界和已评估水位。重复扫描、重开、时钟回拨及历史清理不重新生成旧周期；暂停后恢复从提交后的下一候选继续，保留防回拨水位。编辑先结算旧定义到提交边界，再建立新定义，不改写旧快照。
- 停机／休眠跨多个周期只允许最近一次在一小时内入队，旧周期以冻结定义、日期范围、次数及原因保存；长停机分批推进，每任务每批最多 512 个候选。上次仍执行则记录 overlap，上次结果未知或未结清则阻塞；不自动重做业务。
- skip_once 使用 task_id、expected_version 和规则时区的未来 local_date，递增任务资源版本、不递增定义版本，保留已跳过记录。单次完成保持重复任务 active；结束日期耗尽且运行结清后 completed。

## 入口及展示

MCP／Agent CLI／HTTP 沿用五个工具与统一业务投影，新增重复字段、日期筛选和 skip_once。get 工作日任务返回年度来源及覆盖；范围 list 明确覆盖不足。history 按同一快照分页，旧周期以 kind=missed_range、due_at／until_at／count／reason 展示，不能领取。Agent 输出继续排除执行绑定、attempt 等内部字段，claim 无需 get 预读。

管理范围查询合并临时计划与已物化执行，以同一 occurrence 身份去重，实际记录覆盖预览；每页最多 200 条，范围最多 42 天，游标绑定完整会话、筛选、显示时区和快照。返回日期标记、年度数据和覆盖不足。查询不写文件、不开启 Agent。

原日程模块增加一次性／每天／每周表单、星期及日期筛选、固定时区、结束日期、自然语言范围和待完善选项；仍使用同一 CalendarActor。列表展示重复规则、覆盖及来源，支持跳过下一次。显示时间带年份，区分当前显示时区和规则时区。保存回执丢失重放原 operation_id；保存成功后刷新失败仍按已保存处理，避免再次创建。

C3 继续完成周视图、完整编辑、执行历史交互、日期导航和报告跳转；C4 做跨功能组合验收。本阶段没有开放团队房间、复杂 RRULE 或真实外部写入。

## 验证

受控时间验证覆盖首个候选、跨年、每周、DST、节假日和周末补班、未知覆盖、数据补齐与过期、已开始版本冻结、单次跳过、范围预览、暂停恢复、旧定义边界、多周期漏跑、历史清理、回拨、重开和分页。服务测试经过 MCP／CLI／HTTP、Actor 提交和真实文件路径；重复任务报告发布后仍 active，下一次时间保留。

真实 DSH 使用已安装运行时和默认模型 deepseek-v4.1-flash-1017，在新建隔离目录按 human 请求建立 daily + business_days + CN，省略 start_date。Agent 公开解释固定 UTC 时刻、中国大陆工作日及补班口径、漏跑规则。2026-11-19 03:48 UTC 到期后，下一回合直接 claim、finish，核实唯一公开报告 seq=4，generation=session.json.dsh、turn=2；任务保持 active，next_at=2026-11-20T03:48:00Z，工作日版本 CN-2026-2025-11-04。

实际调用为 update 1、get task 1、claim 1、finish 1。get 是创建后读取保存的定义与工作日历覆盖以解释规则，没有领取预读或报告轮询；受控合同仍验证 update／claim／finish 三次最短路径。首轮同样完成了报告，但因沿用一次性验收的 get=0 限制而未通过；核对调用后将重复验收允许的 get 限定为一次同任务定义读取，再次真实复验通过（75.85 秒）。

Chromium 通过桌面 1280×900 和手机 390×844 视口验证：聊天草稿保留、暂停恢复、每周工作日表单、单次跳过、覆盖不足、跨年日期、回执丢失重放及保存后刷新失败；控制台无异常。截图及结构化记录随附。手机仅视口模拟，没有真机证据。真实模型只处理本地提醒，没有读取真实项目或写入外部平台；跨日、跨周期由受控时间验证，不冒充长期实跑。

验证命令：

```sh
go test ./...
go test -race ./internal/calendar ./internal/room \
  -run 'Test(Calendar|Recurring|OfficialWork|WorkCalendar|ExplicitExcluded|Once|TaskIdempotency|PauseCancel|LateOnce|Unknown|ExplicitRetry|IncompleteTask|CreateDefaults|EditReplaces|UnifiedDSH|InputToolsMinimal|UnifiedUnusedProvider|UnifiedMCPAuthentication|MCPStandardHTTPClient|Followup.*(Notice|Clear))' -count=1
node --test internal/web/tests/*_test.mjs
TEAMX_CALENDAR_BROWSER=1 NODE_PATH="$PWD/packaging/verifier/node_modules" \
  go test -tags=e2e ./internal/room -run '^TestCalendarBrowser$' -count=1 -v
TEAMX_CALENDAR_RECURRING_DSH_REAL=1 TEAMX_INPUT_COMPARISON_CONFIG="$PWD/.teamx/config.toml" \
  TEAMX_CALENDAR_OUTPUT=/absolute/fresh-calendar-recurring-output \
  go test -tags=e2e ./internal/room -run '^TestCalendarRecurringRealDSH$' -count=1 -v
```

真实模型与浏览器均为显式启用的独立验收，不加入日常 CI。

本次验证全部通过：`go test ./...`（Room 包 467.915 秒）、相关 race 检查、前端 754 项、真实 DSH 闭环及 Chromium。全量回归启动后补充的覆盖重验、范围边界、暂停回拨水位和末次跳过不影响待执行任务等检查，另经最终 calendar 全包／Room 日历专项 race 验证；表单回执故障经最终浏览器验证。
