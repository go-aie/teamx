# Agent 与操作者帮助视图

日期：2026-11-21。对应[上下文简化方案 S1a-A1](../AGENT-CONTEXT-SIMPLIFICATION-ANALYSIS.zh.md#92-可独立交付的工作单元)，已完成。基准为 `9a7ed3de5732a4a53eafd58c3894f117ff8e886d` 加 S0 及既有工作树；保留其他测试与文档调整。

## 入口与受众

根帮助只根据本地环境选择视图：存在 `TEAMX_AGENT_ID` 或 `TEAMX_MEMBERSHIP_ID` 时显示成员业务目录；六项成员绑定缺项时保留业务视图并提示绑定不完整，不切换成维护建议。普通操作者环境保留 service、serve、access、member、离线 upgrade、extensions check 等维护入口。帮助只呈现入口，实际权限仍由原服务端检查。

平台/工具目录沿 `boot.Run → room.RunWithCatalog` 使用同一所选 catalog，仅读取静态声明；没有查找可执行文件、连接服务、读取凭据或探测成员可用性。空组合保持空，仅本地工具的组合不补入平台账号；根帮助区分所选声明与 `tools list` 返回的实际绑定。本地工具不因此要求平台账号。

`TEAMX_INPUT_CONTRACT` 只能证明流式协议，不能区分 MCP 与 CLI。帮助因此保留两种传输的准确入口，明确以当前 Session 指导为准，没有新增传输环境变量或猜测模式。

## 分命令帮助

| 入口 | 现在返回的内容 |
| --- | --- |
| `inbox list/read --help`，流式协议 | 独立的发现/正式交付合同，实际 stream/cursor/limit 参数、示例及消费边界；无需执行绑定即可读帮助 |
| `platforms --help`、`connect/selection --help` | 简短目录与下一层帮助入口 |
| `connect start/authorize/status/wait/cancel/notify/acknowledge --help` | 当前操作的参数、返回、示例和直接相关恢复规则 |
| `selection start/status/update/wait/cancel/notify/acknowledge --help` | 当前操作的身份、观察版本、稳定结果、授权和恢复规则 |
| `platforms chats/projects --help` | 各自账号、元数据搜索、分页与不完整结果的合同，不再返回整个连接/通知说明 |
| `calendar --help` | 查询、定义修改、领取和报告的目录 |
| `calendar list/get --help` | 只读查询、实际筛选与历史/结果分页参数 |
| `calendar update --help` | 创建/编辑与生命周期操作分别说明；保留调度、真实人类来源、版本及原 ID 恢复合同 |
| `calendar claim/finish --help` | 领取冻结任务与报告发布各自的执行合同，不再混入创建/编辑定义 |

规范帮助请求在加载凭据前直接返回；带参数的 Inbox 帮助也在解析执行绑定前返回。正式 Inbox 请求仍使用原 CLI 参数校验、身份、执行绑定、调用标识、HTTP 端点和输出确认；connect/selection 的帮助正常结束，实际业务请求与持久状态机未改。历史批次 Inbox 帮助和 S0 的来源参数仍可用。

高级和恢复入口没有删除。connect/selection 的目录仍能找到 wait、notify、cancel、acknowledge；相应帮助保留稳定 ID、unknown 不重放、精确结果版本、先协调后确认、已处理晚到事件不重复执行等限制。Calendar 保留人类授权、冻结执行、取消优先、版本冲突、原 ID 恢复、唯一报告发布和 report_seq 证明。

## 验证

- 启动层测试覆盖标准、显式空、仅本地工具三种 composition，分别运行操作者/成员帮助，检查不创建 HOME 或运行文件；测试声明中的工具路径不可执行。
- 本地 CLI 检查受众选择、六项绑定逐项缺失、所选目录和 transport 不可推断。27 个帮助入口各验证 `--help`/`-h`；环境含不存在的管理凭据文件、无效执行凭据/绑定与 HTTP 哨兵，帮助仍成功且零服务请求。
- Inbox 帮助额外验证带 stream/limit 参数时完全不读取执行环境。Calendar 原帮助检查改为核对各子命令应保留的合同。
- 相关 integration＋race 回归：**54 个顶层用例、含子项 201 项通过，零跳过、零竞态报告**。其中 boot 2/5，Room 52/196；Room 包耗时 39.588 秒。

```sh
go test -race -tags=integration -timeout=4m ./boot ./internal/room \
  -run '^(TestRootHelp|TestScopedHelp|TestInputCLI|TestCalendarCLIHelp|TestCalendarAgent|TestAgentCommandHelp|TestAgentCLIRejects|TestUnifiedCLIProcess|TestPlatformConnect|TestPlatformSelection|TestLarkAuthorizationCLI|TestMattersCLI|TestStaticCheck)' \
  -count=1
```

回归包含真实 CLI 输入进程适配及确认、接入进程生命周期、原账号/成员和版本边界、选择结果/通知恢复、Calendar 创建/领取/报告与过期执行拒绝，以及 S0 新旧来源合同。使用本地 fixture，不代表真实平台或模型验收。

`go build -o /tmp/teamx-context-help ./cmd/teamx` 通过；构建产物实际输出帮助通过，差异空白检查通过。

以下只比较 UTF-8 输出字节：改前取基准 HEAD 中原帮助常量，改后取构建产物输出；根帮助使用标准组合及完整成员绑定。没有据此推算模型耗时、成本或成功率。

| 帮助 | 改前字节 | 改后字节 |
| --- | ---: | ---: |
| 成员根帮助 | 1,793 | 1,363 |
| `platforms connect status --help` | 12,239 | 1,026 |
| `platforms selection acknowledge --help` | 2,991 | 803 |
| `calendar claim --help` | 6,553 | 920 |

只完成 A1。A2 的公共/角色/平台业务指导、A3 的后端身份说明、Skill topic 与别名清理仍待实施；未执行全仓测试、真实模型、浏览器、平台写入或日常服务替换。
