# 日历 Agent 工具合同交付记录

当前 Calendar 入口已改为按需 CLI，见[Calendar 按需 CLI 入口](cli.zh.md)。以下保留当时 MCP 合同和真实验收的历史记录，不代表当前仍注册 Calendar MCP 工具。

2026-11-19，对应会话日历方案 C1.5。最终字段和恢复合同见[方案第 7.1 节](../../CONVERSATION-CALENDAR-AND-SCHEDULED-TASKS-PLAN.zh.md#71-agent-工具)，真实模型调用及内部执行证据见 agent-contract-evidence.json（本地验收文件 `agent-contract-evidence.json`）。C1 原交付及浏览器记录保留在 [once.zh.md](once.zh.md)。

## 已交付行为

- Agent list 默认按任务返回 active／paused／needs_info，包含远于 7 天的安排；completed／cancelled 显式查。日期筛选按有效候选筛任务，每任务一条，不展开日期。任务和历史每页 50 条，cursor 绑定会话身份、筛选、读取选项和快照；快照变化明确报错，没有静默截断。
- get task_id 返回可编辑业务定义，history=true 才读取分页执行历史；get run_id 返回本次冻结定义、执行版本、名义到期时间、时区、业务状态／原因和结果回执，result=true 才返回正文／证据。Agent 输出使用独立业务投影，不序列化持久任务或执行对象。
- create 不需要 expected_version；省略 time_zone 时由服务固定保存系统时区，day_filter=all_days、misfire=latest_within_one_hour。规则摘要给出服务实际保存的时区、筛选、首次时间及漏跑策略。已有资源变更仍要求 expected_version；版本冲突在 MCP、HTTP 和 CLI 保留目标及 actual_version。
- edit 使用 changes 明确替换每个提供的业务字段，未提供的保持；scope=null 清除，misfire=null 恢复默认；拒绝不可编辑字段、必需字段的 null、不完整 schedule 和完整 definition 编辑。暂停任务只能 resume。冻结旧执行不受后续定义编辑影响。
- claim 只传 run_id，直接取得冻结 instruction／scope、due_at、time_zone、misfire 和领取状态。执行绑定、attempt、definition_version、供给及调度记录留在服务内部；重复领取仍重验取消和执行资格。
- finish 必填 run_id／outcome／report；可选 evidence.source_refs 复用现有 src_ 来源引用，最多 16 项、4 KiB。正常回执仅 run_id／status，发布完成后附 report_seq，存在时附 reason。publishing 由服务继续原报告发布，Agent 不重复 send、不为正常发布持续轮询。未知结果不自动重做业务。
- MCP schema、实际 MCP 桥输出、Agent CLI、动态指引及静态提示词一致。UI 管理读取仍使用原投影，前端没有变更；保留 C1 Chromium 桌面和手机视口证据，本阶段无需新增浏览器验收，不把视口模拟称作真机。

CalendarActor、持久状态、公共调度器、执行绑定及可靠报告机制继续复用 C1。响应丢失后重放原 operation_id／原请求；先查原操作再解释默认值。冻结存储仍拒绝重放，安全重开后恢复原操作。没有增加第二套 Agent 入口或放宽会话／成员／逐回合鉴权。

## 调用成本与代表性结果

业务最短路径为 **3 次工具调用**，创建 update 一次，到期 claim 一次，finish 一次；不要求任何 get 预读。认证／逐回合凭据由桥接层提供，CLI --turn 与业务 JSON 分开。

| 调用 | 正常业务必填 |
| --- | --- |
| create update | action、operation_id、human_source.message_seq、definition.title／instruction、schedule.frequency=once／start_date／local_time；任务需要范围时提供已核实 scope |
| claim | run_id |
| finish | run_id、outcome、report |

真实 DSH 使用既有默认模型 deepseek-v4.1-flash-1017，在新建隔离目录创建并执行一次近未来提醒；没有平台读取或远端写入。实际日历调用为 **list 1、update 1、claim 1、finish 1、get 0**。模型先做一次可选 list 检查已有安排，创建省略 expected_version、day_filter 和 misfire；服务保存实际时区与默认值。到期回合直接 claim，完整 scope 的透传另由受控 MCP／CLI 测试验证；这次真实提醒本身不需要外部业务 scope。

创建回执：

```json
{"task_id":"task_03ab9649fe188e17df1e21cd65f1e4e3","version":1,"state":"active","rule_summary":"once 2026-11-19 11:14 (Asia/Shanghai); all_days; misfire=latest_within_one_hour","next_at":"2026-11-19T03:14:00Z"}
```

finish 实际回执：

```json
{"run_id":"run_a48439989751f338b0074e3fb2b768cb","status":"publishing"}
```

随后从服务侧核实 run 已 succeeded，原执行绑定为 generation=`session.json.dsh`／turn=2，公开报告回执 **seq=4**，本私信中正文匹配且只有一份报告。内部绑定仅保留在服务侧验收证据，没有返回模型。没有以模型输出或 finish 受理代替实际发布。

## 验证

受控服务／MCP／CLI 验证覆盖省略创建版本和默认值、原请求丢失后重放（含清空 human 历史）、系统时区／日期跨重试固定、版本冲突目标与版本、编辑替换和显式清除、取消后旧 claim 拒绝、finish 发布中／已发布、异正文冲突、唯一公开报告、显式历史和结果读取、cursor 变化拒绝、证据边界及内部字段不泄露。冻结日历拒绝重放，安全重开后复用原创建回执。C1 原取消、关停、清空历史后持久报告去重和输入门禁继续回归。

验证命令：

```sh
go test ./...
go test -race ./internal/calendar ./internal/room \
  -run 'Test(Calendar|Once|TaskIdempotency|PauseCancel|LateOnce|Unknown|ExplicitRetry|IncompleteTask|CreateDefaults|EditReplaces|UnifiedDSH|InputToolsMinimal|UnifiedUnusedProvider|UnifiedMCPAuthentication|MCPStandardHTTPClient|Followup.*(Notice|Clear))' -count=1
node --test internal/web/tests/*_test.mjs
TEAMX_CALENDAR_DSH_REAL=1 TEAMX_INPUT_COMPARISON_CONFIG="$PWD/.teamx/config.toml" \
  TEAMX_CALENDAR_OUTPUT=/absolute/fresh-calendar-contract-output \
  go test -tags=e2e ./internal/room -run '^TestCalendarRealDSH$' -count=1 -v
```

真实 DSH 一次性闭环通过（92.06 秒），相关 race 回归通过，前端 754 项通过，`go test ./...` 全量通过（Room 包 467.783 秒）。全量回归启动后补充的 CLI 统一业务投影、直接 claim／finish、冻结重放以及详情去重另经专项 race 检查通过。真实模型与浏览器不进入日常 CI。

C2 才扩展重复／工作日／单次跳过；C3 扩展日历管理体验，复用这份合同；C4 做组合验收和发布收尾。
