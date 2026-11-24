# 日历界面与任务信息分层

在首版能力上统一收紧主视图、任务表单和详情，沿用 TeamX 的绿色、字体、字号与控件变量。任务合同、存储、Agent 执行和发布机制保持原有行为。

## 界面调整

顶部移除重复的会话日历标题，合并日期范围与日期选择入口，提供按日／周／日程分段按钮。日期弹层支持翻月、选日和精确日期跳转，不增加月视图。周表头缩至 54px，时间轴采用固定 56px／小时；条目按显示时区的分钟位置放置，少量碰撞并列、密集安排展开，均不虚构占用时长。当前时间线每 30 秒本地更新，离开周视图或进入表单后停止计时，不增加后台请求。普通工作日历覆盖信息使用低强调文字，覆盖不足仍保留警示。

新建与编辑时聚焦表单工作区，宽度最多 720px，返回恢复原日历位置。主表单保留标题、说明、日期时刻和重复方式；星期、日期筛选按规则出现。时区、结束日期、任务范围、错过后的处理收进高级设置，保留一次性日期筛选和结构化 scope；保存前显示实际时区与补跑规则。「保存为待完善」是独立次要操作，不要求虚填日期或时刻。服务仍校验全部业务字段。

详情突出状态、标题、时间、正文和最近执行，一次性安排不重复列出相同的开始／首次／下次时间。完整配置与历史折叠保留；有历史时可直接打开最近结果。暂停／恢复直接可用，单次跳过和软取消放入更多菜单。规则与显示时区不同时明确区分，历史继续读取冻结定义。

## 验证与证据

前端测试覆盖日期跨年、DST、月历网格、分钟位置及碰撞分组；实际 Chromium 通过真实服务和文件路径检查桌面 1440×1000 与手机 390×844 视口。除新增视觉检查外，继续验证范围分页、快照失效、55 条历史、报告跳转、scope／时区保留、SSE、暂停恢复、草稿、回执丢失重放、保存后刷新失败、覆盖不足，以及两条私信切换和服务重建。

手机是模拟视口，无真机证据；模型与时间使用受控夹具，未读取或写入外部平台。真实 DSH 合同未改，沿用[首版交付](release.zh.md)中的独立模型证据。

- 周视图（本地验收文件 `desktop-week.png`）与日期选择（本地验收文件 `desktop-date-picker.png`）
- 简单提醒详情（本地验收文件 `desktop-reminder-detail.png`）
- 桌面表单（本地验收文件 `desktop-simple-form.png`）与手机表单（本地验收文件 `mobile-simple-form.png`）
- 管理浏览器证据（本地验收文件 `browser-evidence.json`）与跨会话恢复证据（本地验收文件 `combination-evidence.json`）

复验：

```sh
node --test internal/web/tests/*_test.mjs
go test ./internal/calendar ./internal/room -run '^Test(Calendar|Recurring|Once)' -count=1
TEAMX_CALENDAR_BROWSER=1 NODE_PATH="$PWD/packaging/verifier/node_modules" \
  TEAMX_CALENDAR_ARTIFACTS="$PWD/docs/implementation/calendar/refinement" \
  go test ./internal/room -run '^TestCalendar(Browser|ConversationRecoveryBrowser)$' -count=1 -v
```

本次检查全部通过：前端 759 项、相关 Go 回归、二进制构建及两条 Chromium 验收（合计 24.029 秒）。控制台无异常，表单返回位置、scope／时区保留与跨会话恢复通过；结果见 validation.json（本地验收文件 `validation.json`）。
