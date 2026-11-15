# TeamX MCP 默认读取与有限恢复

日期：2026-11-15。对应[整体方案](../TEAMX-MCP-INBOX-PLAN.zh.md)阶段 3。阶段 1–2 的协议、图文及输出确认合同保持；阶段 4 的 stdio／其他 Agent 接入尚未实施。

## 实现与验收

生产成员通过共用的 `memberBackend` 默认选择 HTTP MCP，创建与正常恢复继续自动注入成员凭据与 `teamx` server。启动配置新增 `[server] inbox_mode = "http"`，缺省相同；显式 `"cli"` 选择原文字消费入口，其他值启动时报错。首个支持路径是当前 DSH；HTTP 不受支持或连接失败会明确报错，不在回合中静默回退。

稳定入口与指引继续参与 Session 指纹，临时 token 排除。启动恢复在具备完整运行配置时先核对原配置指纹；不匹配的已结清 Session 通过原恢复协调器受控轮换，沿用暂停意图、来源／动作结果核验及资源结清检查，不改旧恢复证据。CLI 回退同样使用正常重启和受控轮换，保留原 Inbox、未确认批次及消费进度。没有清空或迁移业务记录，也没有为回退增加第二份 Inbox。

生产 prompt 材料准备仅包含原 Room 路径，取消 Inbox 正文和图片的读取前供给；Inbox 唤醒只提供状态。MCP 指引明确使用发现的工具和本轮 `current_turn`，同批按 `next_cursor` 读至 `has_more=false`。Room 图片及页面只读预览保留。旧材料组件夹具继续验证下载、预算和只读投影，不能当作生产 Inbox prompt 入口。生产适配回归已改为断言没有提前供给 Inbox 正文／图片，同时仍能供给 Room 图片。

停滞恢复状态保存在现有 `collaboration.json`：原始范围、消费起点、停滞时间、已尝试次数及下次时间。最近一次人工继续时间与累计次数也保存于同一文件供审计，不新增交付账本、任务系统或 completed 回合补确认。加载兼容旧记录，新增状态按边界校验。

成员周期同步在正常完成、固定范围未发生消费进展时建立停滞状态。原 FIFO 调度器最多接纳 3 次恢复回合，分别退避 15 秒、60 秒、300 秒；尝试额度在执行前随原回合计数持久保留。新通知、启动恢复、Session 轮换、过期或跳过不刷新额度；实际消费前进才自动重置。最新观察序号与停滞固定范围分别保留，超过 25 条的积压分批持续处理，每批内部仍可分页。

恢复继续受暂停、取消结清、执行公平性、原预算、模型及存储故障门槛约束。调度前检查已保存未知动作，阻断自动恢复和人工继续；新的 Room 输入仍按原业务入口核实结果。失败／中断的业务回合不自动套用无进展重试。人工「继续读取收件箱」可在结清、解除暂停并通过独立门槛后重置恢复额度，不追加预算、不发新聊天；显式继续可重新读取中断输入，已确认消费不回滚。暂未解决的正式读取／确认错误进入现有成员诊断，工具重试成功则清除读取错误。

恢复原因、范围、当前积压、下次时间与次数耗尽状态复用成员状态和通知；原因放在折叠设置外，继续操作复用现有快捷控制及展开控制。等待时间按系统时区显示。收件箱页复用同一运行说明，保留页面预览不消费的边界。界面沿用现有绿色主题和按钮样式。

确定性用例覆盖历史 `LastOffered` 停滞、无新聊天补读 61 条、单批 7 条分页、三次退避／可见停止、新通知不续额、磁盘恢复与实际自动 Session 轮换不续额、显式继续审计、暂停／预算／未知结果门槛、取消后不自动重试，以及读取已确认而业务失败不归类为输入停滞。默认模式、显式 CLI 配置及非法模式也有回归。原 MCP 的字节预算、同页重放、短写／断线、确认失败、并发与凭据撤销用例继续通过。

通过的检查：

```sh
go test ./... -count=1 -timeout=15m
go test -race -tags=integration ./internal/room -run '^Test(MCP|InboxRecovery|InboxScheduling|DSHMaterialsProduction|LarkInboxImagePreview)' -count=1 -timeout=4m
node --test internal/web/tests/*_test.mjs
go vet -tags=e2e ./...
git diff --check
```

全量日志为 `.local/acceptance/mcp-default-all-final.log`。最初的全量运行暴露两个只保存最小后端证据的旧恢复夹具；配置预检查现仅对具备可执行配置的后端运行，两项专项复测及后续单次全量均通过。最终继续／取消补强后另跑恢复竞态专项（`.local/acceptance/mcp-default-recovery-race-last.log`）；未把早期失败日志改写成通过。Web 最终全量确定性测试共 646 项通过（包含工作区同期新增用例）；日志为 `.local/acceptance/mcp-default-web-complete.log`。

## 历史范围检测补强

2026-11-15 后续生产排查发现：成员曾提供 Inbox 范围后，又完成了只处理 Room 消息的回合。此时 `TurnRecord` 的 Inbox 字段为空，但 `LastOfferedInboxThrough/Seq` 仍准确保留此前未读取的范围。恢复检测及未知动作门槛现统一使用该持久范围，并继续要求最近回合正常结束；不能因 Room-only 回合覆盖而漏掉历史停滞。

回归使用真实 Inbox actor，在历史范围之后完成并读完 Room 输入，再验证无需新增聊天即可通过 MCP 持续消费 61 条；另核对同样状态下的未知动作仍阻断自动及人工恢复。专项竞态和 `go vet` 通过，生产操作另保存在私有 `.local/acceptance/inbox-production-recovery-20261115/`，不改写早期隔离验收结果。

## 独立真实验收

真实 DSH `0.2.1-alpha.1`、`qwen3.8-flash` 通过生产默认选择路径，分别在新建和正常恢复 Session 各调用一次 `inbox_read`，一次返回两张图片。每次使用仅存在于像素中的新随机内容，正文和 Workspace 文件不提供答案；正式消费随输出推进，图像预算与凭据恢复仍沿阶段 2 的实现。本项使用本地附件 seam，证明真实 ACP／MCP／模型理解，不替代真实平台下载。平台下载沿用阶段 2 的独立证据。本次没有重新执行平台远端写入。

真实 Chromium 使用实际管理页面、HTTP、成员调度器及 Inbox 持久状态，通知、账号证据和 Agent 组件为合成夹具。桌面与 390×844 窄屏均检查等待原因、固定范围、61 条积压、三次恢复后停止和可见继续按钮；点击后完整读完积压，核对没有新增 Room 聊天。截图已人工查看，窄屏无横向溢出。这是浏览器视口验收，不是实体手机键盘／系统焦点验收，也不证明真实 Agent 在无人干预时必然读完任意积压。

私有报告：

- `.local/acceptance/mcp-default-dsh-20261115-1/report.json`
- `.local/acceptance/mcp-default-browser-20261115-9/report.json`

复跑浏览器入口：

```sh
TEAMX_INBOX_RECOVERY_BROWSER=1 \
TEAMX_INBOX_RECOVERY_OUTPUT=/absolute/path/to/fresh-evidence-directory \
NODE_PATH=/absolute/path/to/playwright/node_modules \
go test -tags=e2e ./internal/room -run '^TestInboxRecoveryBrowser$' -count=1 -v
```

本次只实现并在隔离环境验收默认切换，没有启动、恢复或修改现有生产 Room，也未发送远端评论或修改 Todo。服务端完整写出仍不等于模型感知；未确认输入可能在新合法回合重复交付，已确认动作不重做。stdio proxy、其他 Agent 的发现与识图继续留在阶段 4。


## 生产补读结果（2026-11-15 后续操作）

经用户明确要求，定位到「日常迭代」assistant 的消费停在 #165、最新 #259，原积压范围共 94 条；其他成员当时无 Inbox 积压。保存目标 Room 的完整私有备份后，仅对该成员调用一次现有 `member/run` 显式重试。真实 MCP 消费依次推进到 #190、#215、#240、#259；原范围已全部确认，当前积压为零。原过期边界仍为 #144，保留记录仍为 115 条，没有通过清空、跳过或直接改游标完成修复。未发现未知动作回执。最后一次取证时成员仍在正常处理末批业务；输入确认不代表事项已办结。

操作没有发送额外聊天来触发恢复，也没有重启或中断其他正在执行的成员。历史范围检测补丁已通过竞态回归、更新到本地 `bin/teamx`；当前进程继续运行原版本，补丁于下次正常服务重启时加载。本次已有显式重试入口足以恢复原积压。私有操作报告与备份位于 `.local/acceptance/inbox-production-recovery-20261115/report.json` 和同目录 `room.before.tar.gz`，不包含在常规 CI 或早期隔离验收结果中。
