# Agent 历史恢复与集成验收

范围对应[统一历史读取方案 H3](../AGENT-HISTORY-PLAN.zh.md)。H3 完成隔离接入与集成验收；`teamx read` 保留，正式服务和现有 Session 尚未切换，入口移除与授权启用属于 H4。

## 恢复接入

`service.historyEnabled` 仅由隔离验收显式设置，生产配置没有新增启用入口。隔离 Session 使用新的 Agent guide 和恢复 guide；HTTP Inbox 提供 Inbox＋History 四个 MCP 工具，CLI Inbox 继续通过 CLI 正式收件，同时提供两个 History MCP 工具。后端共用原执行绑定、输入 bridge 和输出结清机制。

恢复材料增加 `room_stream_ref`，保持原有供给时机、固定快照、最多最近 20 条可见 Room 消息、事项摘要和未知动作提示。已全部消费的 Room 仍有初始预览；失败的首次业务回合可重供，成功后不逐回合重复。可选记录仍按整条裁减，引用和必须的恢复指导不因预算裁减丢失。

新指导说明：已知流直接读，缺少引用才列目录；正文续页只传 read cursor；最近内容不等于完整远端历史。预览与 History 不消费，未读仍须正式交付；重复正文不成为新的任务或动作许可。已有答案、事项进度、最新 operation 和成功／未知动作继续查原业务记录，未知结果不换 ID 重发。

原轮换验收的恢复请求改为使用自动预览及按需 History，不再硬编码 `read --all`。旧入口的专属 CLI 解析测试和旧格式回归保留到 H4。[Inbox 手册](../inbox.zh.md)、[通知参考](../reference/notifications.zh.md)和[存储架构](../storage-architecture.zh.md)同步说明当前隔离边界和最终使用方式。

## 真实模型与平台证据

当前配置模型为 `deepseek-v4.1-flash-1017`，通过真实 DSH／ACP、输入 bridge 和 HTTP MCP 执行。汇总见 [H3 可复核摘要](agent-history-h3-evidence.json)，完整正文、答案、工具 trace、图片块及权威状态对照保存在 `.local/` 私有报告中。

| 场景 | 次数 | 实际结果 |
| --- | ---: | --- |
| 最近 Room 决策 | 3 | 已知引用，一次 History 调用取得随机事实标记 |
| 已消费 MR 流回查 | 3 | 一次 History 调用，未改变保存状态或消费 |
| 无未读输入时目录发现 | 3 | 一次 list＋一次 read，准确定位已消费目标 |
| 飞书流图片 | 3 | 一次 read，模型读出只存在于 fixture 像素里的完整验证码 |
| 较早正文续读 | 3 | 首次 limit=1，随后两次仅传 read cursor，找到原始事实后停止 |
| 轮换恢复及原业务核实 | 3 | HTTP／CLI 两种模式；一次 History 回查，加正常 Room 正式交付及真实事项／动作账本查询 |
| GitLab 真实保存通知／真实图片 | 3 | 真实同接收账号 GET 下载，经 MCP 供给；模型读出像素正文 |
| 飞书真实保存通知／真实图片 | 3 | 真实成员绑定和 CLI profile，经已有资源下载器与 MCP 供给；模型读出截图错误 |

前五类的通知／图片为本地 fixture；后两类直接使用已有并获准读取的真实保存通知。GitLab 使用严格限定实例和 GET 方法的同接收账号测试凭据，复用原上传定位和下载器，未覆盖托管 OAuth 刷新生命周期；飞书使用真实托管连接、成员绑定和原资源下载流程。未发送、评论、合并或修改任何正式订阅、Room 和 Workspace。真实 GitLab 所选截图是聊天输入框，首次沿用另一张历史截图的预期导致验收失败；核对实际 MCP 像素后修正预期并重跑通过，未将失败样本计入通过次数。

恢复用例先通过现有本地动作引擎生成成功与未知两条回执、保存并关联一次人类答案，再由原启动升级器构造完整隔离输入数据。真实模型恢复后，原动作文件逐字节一致、问题和答案集合不变、平台消费不变；新回合只产生一页已结清的 Room 正式交付。动作测试使用本地 contract sink；业务 CLI 的事项读取走原服务，独立服务缺少 manager 动作路由，验收适配器使用真实 `queryActions`／原业务投影和身份中间件提供只读动作查询。这个边界不代表真实外部动作执行验收。

## 工具集安全刷新

三个恢复样本均先运行并正常关闭旧 Session，再显式启用隔离 History 组合。实际调用 `dsh.Resume` 验证修改后的指导无法静默进入旧配置指纹，返回 `ErrRecoveryUnsupported`。在已经结清的隔离 Session 上切换代际后，新 Session 取得不同的 ACP SessionID；trace 同时记录实际 `tools/list` 和 `history_read`。HTTP 模式发现四工具；CLI 模式发现两个 History 工具，正式 Inbox 仍通过 CLI 交付。

因此，静态工具注册成功不被当作当前 Session 已刷新。正式切换须沿 H4 的授权目标和安全窗口处理；本次未重启正式服务，也未轮换任何现有 Session。

## 容量与并发

以 Room、成员私有和共享输入各 10,000 条、2,001 个逻辑流验收。正文约 512 字节，达到记录数容量上限，不代表三份存储同时达到字节容量上限。首次建样使用私有批量导入和共享正常 append；后续从已校验的完整 fixture 复制到新 Store 重开。报告区分首次构建后的读取和重开后的读取，包含原生 Room 校验的既有成本；测试与同机其他回归并行，数值是本机观测，不是部署 SLA。

已知流的模型调用数为 1，目录首屏定位为 2。2,001 流、目录 limit=20 时，完整目录需 101 次 list，最后一屏定位再读正文合计 102 次；这是顺序目录的明确成本。常用路径复用 Input directory、恢复材料及已有引用，目录按最近保存时间排序并以来源／账号／对象标签区分；代表模型样本全部达到 1／2 次首批读取目标。不自动遍历正文弥补定位成本，不为本阶段新增搜索合同；久远目标的大目录定位仍需按实际预算停止。

最终重开样本的观测值如下；括号内为样本数。初建样本的目录／已知流 P95 为 148／127ms，重开与并行负载结果也完整保留，未只采用较快的一轮。

| 项目 | 观测 |
| --- | ---: |
| 首个目录 | 718.425ms（1） |
| 热进程新目录 P95 | 975.299ms（20） |
| 已知流最近读取 P95 | 104.159ms（20） |
| 连续正文 4 页 | 1,160.522ms（1） |
| 完整目录 101 页 | 18,521.587ms（1） |
| 持有 History 输出时获取 Store 锁 | 1.103ms（1） |
| 并发正式消费 20 条 Room 消息 | 14,882.039ms（1） |
| 释放 History 输出后的正式消费对照 | 13,041.573ms（1） |
| 并行回收 32 条后追加 | 2,679.083ms（1） |
| 并行另一 Room 普通读取 | 0.039ms（1） |

重开私有输入存储耗时 1,593.418ms；最终堆约 116.8 MiB，较初始增加 115.6 MiB；Go Sys 约 314.4 MiB，未把这些终点值称为峰值。满容量正式收件在没有 History 输出的对照中仍需约 13.0 秒，本阶段保留该既有成本；History 输出没有持有 Store 锁。

`limit=10000` 的 Room 请求只供给完整的最近 429 条、261,388 字节，保留更早 cursor；连续小页覆盖 10 条记录，4 页无重复遗漏。文字单页 256 KiB、回合累计 512 KiB，以及独立的图片 4 张／4 MiB 上限继续采用；原 History 材料预算／缓存重放／省略原因测试及真实图像读取通过。没有通过历史查询刷新正式 Inbox 额度。

准备好的历史响应被保留到输出结清期间，另一个成员仍可正式消费 Room；并行完成 32 条旧私有记录回收、一次追加、另一 Room 的普通读取。Store 锁在持有历史输出期间可立即获取。图片下载阻塞专项也验证追加、另一成员消费、其他 provider 回收与另一 Room 读取能够完成；同一成员仍服从既有串行读取 lease。具体延迟、P95、重开成本、内存以及正常消费对照数值见 JSON 摘要；单次场景不作为统计上的 P95。

## 回归与复跑

已通过：History／保留元数据、MCP、共享授权、输入 bridge、正常收件、材料、Session 恢复与自动轮换的针对性 integration＋race；新增恢复预算与并发专项 race；DSH 和 inputstream 两包完整测试；`go test -tags=e2e ./... -run '^$'` 的全部包编译；禁用既有外部结构体非具名告警后的 vet 和 diff 检查。本次不宣称 Room 全量测试通过；此前两阶段的全量超时边界仍保留在原记录中。

使用当前模型目录和已有只读平台凭据复跑；每次 `TEAMX_DSH_OUTPUT` 必须是新绝对目录：

```sh
TEAMX_HISTORY_ACCEPTANCE_REAL=1 \
TEAMX_INPUT_COMPARISON_CONFIG=/absolute/config.toml \
TEAMX_DSH_OUTPUT=/absolute/fresh-scenarios \
go test -tags=e2e ./internal/room -run '^TestHistoryRealDSHScenarios$' -v -count=1 -timeout=20m

TEAMX_HISTORY_RECOVERY_REAL=1 \
TEAMX_INPUT_COMPARISON_CONFIG=/absolute/config.toml \
TEAMX_DSH_OUTPUT=/absolute/fresh-recovery \
go test -tags=e2e ./internal/room -run '^TestHistoryRealDSHRecoveryAndToolRefresh$' -v -count=1 -timeout=15m

TEAMX_HISTORY_PERFORMANCE_OUTPUT=/absolute/fresh-performance \
go test -tags=e2e ./internal/room -run '^TestHistoryCapacityPerformance$' -v -count=1 -timeout=20m
```

性能默认重新构建满容量数据；可用 `TEAMX_HISTORY_CAPACITY_FIXTURE` 指定完整的隔离样本目录重开，须包含正文发布根、记录段、共享 Grant 及成员容量账本。真实平台入口为 `TestHistoryRealSavedPlatform`，选择 `TEAMX_HISTORY_PLATFORM_REAL=gitlab|lark`，分别提供 `TEAMX_GITLAB_IMAGE_NOTICE`／`TEAMX_LARK_IMAGE_INPUT`；`TEAMX_HISTORY_PLATFORM_EXPECT` 仅由测试端检查像素答案，不进入提示或保存正文。GitLab 凭据沿原安全环境文件入口 `TEAMX_DSH_ENV_FILE` 加载；模型配置沿上述 `TEAMX_INPUT_COMPARISON_CONFIG`，不向 Agent 提供凭据。

H3 不引入生产开关或数据迁移，不删除旧命令；H4 仍需独立完成最终入口删除、已授权服务的安全切换，以及目标 Session 的实际工具发现、图文读取、续页、正常消费和恢复检查。
