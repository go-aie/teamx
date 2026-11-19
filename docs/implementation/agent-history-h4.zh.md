# Agent 历史入口收敛与生产路径验收

对应[统一历史读取方案 H4](../AGENT-HISTORY-PLAN.zh.md)。**入口收敛与生产默认路径已完成，当前日常服务尚未部署切换，因此 H4 的正式服务采用门槛仍未完成。** 本次保留当前服务及既有 Session，未覆盖 `bin/teamx`、重启服务或轮换其私有上下文。该边界与 H3 的隔离启用证据分开。

## 入口与默认注册

- 删除 `teamx read` 的解析、参数、帮助、生产输出代码及 Agent／恢复调用指引；不保留 `--all` 兼容分支，不新增 History CLI。旧调用统一返回未知命令。
- History 纳入正常执行的 MCP 注册，移除隔离采用开关与注册注入。两条执行凭证路径共用按 Inbox 模式选择的工具集合：HTTP 模式为 `inbox_list`、`inbox_read`、`history_list`、`history_read`；CLI 模式仅提供两个 History 工具，正式收件继续用 Inbox CLI。
- 保留原生 Room 读取与确认、恢复预览、统一 Inbox 内部扫描及业务接口。原 CLI 读取测试改用原生 HTTP 测试 helper；进程、协作、轮换与浏览器脚本也已移除旧命令依赖。旧正式页的正文、证明与结清协议不改写，不迁移历史数据。
- 正常服务的 Session 指引直接说明 History 的只读、账号／对象范围、图片、cursor 续页、预算停止及动作核实规则。首个成功业务回合的有界 Room 预览继续提供流引用，已消费和未消费记录均可出现；预览和 History 不消费，重复正文不成为新任务。

用户入口说明同步到 Room 的中英文 README、Inbox 手册、通知参考和存储架构文档。过去 H1–H3 记录中的隔离范围是当时的验收事实，继续保留。

## 生产默认路径证据

[可复核摘要](agent-history-h4-evidence.json)记录本次工具集合、调用数及权威状态。完整提示、答案、工具 trace 和图片块保存在 `.local/history-service-real-20261119-h4-01/report.json`；恢复记录在 `.local/history-recovery-real-20261119-h4-01/report.json`。

新增 `TestHistoryProductionServiceRealDSH` 使用普通 `newService`、成员注册、实际调度器和生产 DSH backend，使用正常执行凭证与默认工具注册。没有显式安装 History transport，也没有替换 backend 的 Open。HTTP／CLI 两种模式各一个新服务、新成员和新 Session，以真实 `deepseek-v4.1-flash-1017`、DSH／ACP 验证：

| 检查 | HTTP Inbox | CLI Inbox |
| --- | --- | --- |
| 实际 tools/list | 四工具 | 两个 History 工具 |
| 目录定位 | 一次 history_list | 一次 history_list |
| 正文与图片 | 一次首次 read，实际一块 image | 一次首次 read，实际一块 image |
| 向更早内容续读 | 两次仅传 cursor | 两次仅传 cursor |
| 答案 | 命中原始随机事实和完整像素码 | 命中原始随机事实和完整像素码 |
| 正式消费 | Room 游标到 1，一页证明 settled | Inbox CLI 完成交付，Room 游标到 1，一页证明 settled |
| 历史只读 | 三条平台通知仍待消费 | 三条平台通知仍待消费 |

图文通知和下载器是 fixture；模型、DSH／ACP、普通服务构造和调度、MCP 发现与调用是真实路径。真实 GitLab／飞书平台材料证据沿用 H3，本次没有新增外部业务写入，也未将 fixture 验收称为真实平台材料验收。

恢复用例按当前默认注册重跑三次，覆盖 HTTP／CLI 模式。每次通过一次 History 回查恢复既有事实，核实原事项问答与 succeeded／unknown 动作；动作文件逐字节一致、平台消费不变，新增 Room 页正常结清。测试有意给旧隔离 Session 的冻结指引增加差异，实际 Resume 拒绝配置变化，再在该 disposable Session 上显式切换代际；两代都使用当前默认工具集合。这验证当前恢复保护，**不是重新模拟 H3 的旧工具集合升级，也不是当前日常 Session 的切换证据**。

## 回归与复跑

已通过针对性 integration＋race：History、保留元数据、MCP、共享授权、图文、执行生命周期、正常收件、恢复与轮换，以及 CLI 退出和原生 Room 读取／输出失败／确认。三包运行中 Room 约 70 秒；另有末次入口／预览专项检查。全部包 e2e 编译、`go vet -tags=integration -composites=false ./...`、生产 CLI 构建和 diff 检查通过。标准 composites 告警及 H2／H3 的 Room 全量超时边界未改动；本次不宣称 Room 全量测试通过。

构建产物在 `.local/teamx-history-h4`，没有替换正在使用的二进制。六种旧命令形式在编译产物上全部返回未知命令，根帮助没有旧入口。

```sh
TEAMX_HISTORY_SERVICE_REAL=1 \
TEAMX_INPUT_COMPARISON_CONFIG=/absolute/config.toml \
TEAMX_DSH_OUTPUT=/absolute/fresh-service-evidence \
go test -tags=e2e ./internal/room -run '^TestHistoryProductionServiceRealDSH$' -v -count=1 -timeout=8m

TEAMX_HISTORY_RECOVERY_REAL=1 \
TEAMX_INPUT_COMPARISON_CONFIG=/absolute/config.toml \
TEAMX_DSH_OUTPUT=/absolute/fresh-recovery-evidence \
go test -tags=e2e ./internal/room -run '^TestHistoryRealDSHRecoveryAndToolRefresh$' -v -count=1 -timeout=12m
```

## 正式切换的剩余门槛

当前 `./bin/teamx serve --config .teamx/config.toml` 及其旧 Session 没有切换。新指引改变冻结配置指纹，已有恢复检查会拒绝不兼容配置；重启不能保证这些 Session 继续工作。H4 方案要求先结清旧执行，且不为刷新工具集擅自轮换 Session。

部署前须确定当前服务中的采用成员和安全窗口，结清旧在途页、工具与执行。对新 Session 启用新版本；如果需要既有成员立即采用，则须先明确允许其私有上下文重置，沿原轮换与结清流程处理，不能改写旧 backend 证据伪装兼容。部署后仍须在目标服务核验 tools/list、一次目录／图文读取、正文续页、正常消费和恢复。新版本的默认路径验收已经通过，这些日常服务证据仍待安全切换后补录。
