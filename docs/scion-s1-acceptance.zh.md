# Scion S1：独立工具

日期：2026-11-07。范围为 [接入方案](SCION-SOFTWARE-DEVELOPMENT-PLAN.zh.md) 的 S1。

结论：**S1 通过。** 新增只依赖 Go 标准库的 `tools/scion`，可独立构建 `teamx-scion`。固定官方 `scn 0.1.13` 的帮助、原生执行与隔离验证通过；新封装也完成了目标实例的三项只读复核。TeamX 的 Catalog、连接、成员绑定、skill 和 Workspace 接入仍待 S2–S4，不把操作者独立运行记为 Agent 或页面验收。

## 已交付

- [独立封装](../tools/scion/main.go)：从嵌入 lock 读取精确版本；按 `TEAMX_SCN_BIN` 绝对覆盖、同目录、绝对 PATH 项解析依赖。显式失效不回退，缺失/不兼容/探测失败返回 127，实际业务不启动。版本探测最多 5 秒。
- [固定依赖](../tools/scion/scn.lock.json)：`v0.1.13` 发布和下载地址；仅包含已核实的 `darwin_arm64` 包及独立 `.sha256` 文件、压缩包与解包后二进制摘要。其他平台未核实，不自动安装或升级。
- 每次创建并清理临时私有 HOME、配置、缓存与默认空凭据路径。固定 file 凭据与禁用自动 daemon，过滤 `SCION_TOKEN`、全局配置/登录、Git 环境和其他平台/模型凭据；只接受明确的 endpoint/Hub/repo/profile/author 运行输入。版本探测不加载运行 profile。
- 不使用 shell；保留 argv、stdin、cwd、原生 `-C`、stdout/stderr 字节和正常退出码。不包装原生 JSON，不重试或回退 REST。Unix 独立会话与进程组支持取消、遗留后代清理与等待；信号取消返回 130。
- [安装与使用说明](../tools/scion/README.zh.md)：校验发布文件、构建与路径配置、原生隔离登录示例、只读调用、作者及 Scion 语义、未知写入处理和后续边界。

S1 只负责独立运行。profile 的受管解析、账号漂移、连接/目标实例匹配、成员选择和服务端环境装配在 S2 实施，不能把当前独立环境输入当作已验证的成员连接。

## 组件与官方二进制验证

执行成功：

```sh
go test -race ./tools/scion
go vet ./tools/scion
go build -o .local/scion-s1-20261107-1/teamx-scion ./tools/scion
TEAMX_SCN_BIN="$PWD/.local/scion-s0-20261107-1/bin/scn" \
  go test -race -count=1 -v ./tools/scion
```

| 范围 | 实际结果 |
| --- | --- |
| argv、stdin、输出与正常退出码 | 假原生进程验证空参数、空格、换行、字面 shell 内容、`--help` 正文、`--`、NUL stdin/输出，以及 0/1/2/3/4/5/42，逐字节与逐退出码一致 |
| 版本与二进制路径 | 错误版本、额外版本文本、探测失败、失效/相对显式路径均拒绝，未执行业务；显式、同目录与 PATH 顺序通过，不搜索相对 PATH |
| 隔离与清理 | 探测及业务环境均未继承替代 token、全局配置、daemon 和其他平台/模型凭据；业务仅保留明确输入，私有 HOME 在返回前删除 |
| 进程生命周期 | 版本探测和业务进程取消均返回 130，同组父/子进程消失；父进程正常退出后清理遗留后代；无控制终端 |
| 官方帮助与版本 | `--version`、`--help`、`help review`、带 `--json` 的 Review 帮助，以及 login/create/comment/两种 land 帮助均退出 0；不可用 endpoint/Hub 和不存在 profile 不妨碍帮助 |
| 原生错误与 JSON | 非法 Review 子命令保留退出 2 和原生错误；实际相对 `-C` 与 config JSON schema 通过；上次调用写入的临时配置不影响下一次 |
| 独立依赖 | `go list -deps` 仅有 Go 标准库和本工具包，无 TeamX 内部包及第三方依赖 |

额外以构建后的实际入口运行进程探测：分别发送 **SIGINT、SIGTERM**，均返回 130，原生进程及其 `sleep` 子进程均已消失。使用 PTY 为封装提供真实控制终端，再执行官方 `scn --help`，退出 0，没有终端颜色问询或等待。

还逐字节比较了直接执行官方 scn 与封装的版本、评审评论帮助、非法子命令错误，以及在 S0 独立工作副本上使用相对 `-C` 的 `--json status --no-ahead-behind`：四项 stdout/stderr/退出码完全一致。状态的原生 schema 为 `scion.cli.status/v1`，base 仍为 S0 的完整基线 `c1078e705cc235a88699caf8aaf51c45803df33aafb8849ea36949756f8a36d4a0`。

## 真实只读复核

复用 S0 的官方二进制和已登录的隔离 profile；本次再次计算二进制 SHA-256，匹配 lock 的 `1d5c4c2b09aeed7245fae2a0bd0a5a13d3f927f2406d211b7d78f14953f4ff3f`。本次未重新下载压缩包；lock 的压缩包摘要来自 S0 已核实的发布文件。

通过构建后的 `teamx-scion`，明确设置 endpoint、Hub 同为 `https://scion.opencsg.com`，只引用 S0 的凭据文件，不重新读取 `.env` 或注入 PAT。额外给封装一个无效的全局 `SCION_TOKEN` 测试值，确认其被过滤而未覆盖 profile。

| 调用 | 退出码 | 实际结果 |
| --- | --- | --- |
| `--json whoami https://scion.opencsg.com` | 0 | `schema=scion.cli.whoami/v1`，subject `csgship` |
| `--json repo info OpenCSG/teamx-demo` | 0 | `schema=scion.cli.repo/v1`，name `OpenCSG/teamx-demo`，默认分支 `main` |
| `--json --repo OpenCSG/teamx-demo review ls --state all --limit 100` | 0 | `schema=scion.cli.reviews/v1`，repo 匹配，`reviews=[]`、`truncated=false`；显式 Hub 链路通过 |

本次运行产物位于 Git ignore 的 `.local/scion-s1-20261107-1/`，原始只读 stdout/stderr、读取摘要、原生字节比较和实际信号/PTY 检查摘要在其 `evidence/` 下。私有目录为 0700，读取原始输出为 0600；凭据内容未复制到该目录、仓库或公共文档。

## 边界

没有新增 TeamX provider/Catalog/skill 或成员配置，没有修改 Room、Session、Workspace 和角色模板，没有 clone、commit、分支发布、创建/更新 Review、发表评论、approve、合入或关闭。本次通过不代表 S2–S4、真实 DSH 协作或浏览器闭环通过。非 Unix 的进程树结清和其他发布平台尚未验收。

验证限于独立工具及本阶段文档；未运行 Room/前端全量回归，已有并行工作不在本次验证范围。S1 新增文件和修改的方案文档已完成换行、尾空格及敏感信息检查；未提交或 push。
