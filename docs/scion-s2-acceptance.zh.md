# Scion S2：连接与工具绑定

日期：2026-11-07。范围为 [接入方案](SCION-SOFTWARE-DEVELOPMENT-PLAN.zh.md) 的 S2。

结论：**S2 已实施；组件、成员工具入口、真实只读及管理器正常关闭/重开验证通过。连接和成员设置的页面自动化通过，实际浏览器操作待验。** 没有初始化 Scion Workspace、修改角色模板或执行远端业务写入。

## 交付

- [Scion provider](../internal/room/platform.go)：复用当前连接快照 v4、原生 `AccountIdentity`、显式验证与连接结清，不增加快照版本或隐式迁移。身份来自实际封装版本检查与 `scn --json whoami`，核对 schema、endpoint 与非 anonymous subject；保存 `namespace=scion:subject`，数字 `UserID` 为空。
- [受管 profile 与核验](../internal/platform/scion/profile.go)：引用为 `CLIProfile{version:1, brand:scion, profile:<opaque ID>, executable:<绝对封装路径>, cli_version:0.1.13}`。目录固定为服务私有根的 `scion-profiles/<ID>/credentials.json`；目录 0700、文件 0600，拒绝符号链接、路径穿越、无效 JSON 和非私有权限。凭据不进入连接公开 API、成员配置、Room 或 Session。
- [工具准备](../internal/room/tools.go)在原有平台调用接纳之后、manager 锁之外执行。每次调用先把所选 origin 的凭据复制至进程私有文件，用同一副本核对真实账号并执行业务，结束或取消时删除。外部原地换号不能让已核验调用变成另一账号；后续调用发现不符返回 `identity_mismatch`，不启动业务。
- [Catalog 与进程](../internal/platform/tool/process.go)：新增 scion、固定版本说明、skill 和 Scion 环境映射，转发服务端 `TEAMX_SCN_BIN`。业务与帮助使用所选 profile 的封装绝对路径；帮助不加载凭据，但仍需该成员显式绑定。Endpoint/Hub 同为连接 origin，不继承全局 Scion token、Hub、配置或其他模型/平台凭据。
- [目的地址检查](../internal/platform/scion/arguments.go)：拒绝不匹配的 Endpoint、Hub 和仓库 URL；阻止成员执行登录、登出、token、配置、daemon、自更新。原生命令仍使用账号权限，没有新增项目白名单、每操作授权表、Review API 或 MR 兼容层。特别区分 `self-update --version TAG` 与无凭据全局 `--version`，避免把值参数当作帮助。
- 页面平台选项增加 Scion，填写实例、profile ID、封装路径，保存后自动验证；使用现有成员工具设置逐一显式绑定。profile 变化沿用引用房间确认、旧调用结清及重新验证。未验证连接不能绑定；无 clone 可显式 `--repo` 读取 Review。
- [按需 skill](../internal/platform/tool/skills/scion/SKILL.md)说明两种 land、指定路径提交、明确作者、完整 ObjectId、发布后 head、原生 JSON、detached 独立评审、revision 评论、更新复审和未知写入核查。当前 Workspace 初始化边界也明确写入说明。

安装与受管目录登录步骤见 [工具手册](../tools/scion/README.zh.md#teamx-连接与成员绑定s2)。首期仅验收原生 PAT 登录；业务使用临时副本，不写回 refresh-token 更新，不把它声称为第二条已完成的认证路径。

## 组件与回归

通过的主要命令：

```sh
go test -race ./internal/platform/scion ./internal/platform/tool
go test -race ./internal/room -run '^TestScion' -count=1
go test ./internal/platform -run TestCapabilityImportBoundaries -count=1
go vet ./internal/platform/scion ./internal/platform/tool ./tools/scion ./internal/room
node --test internal/room/web*_test.mjs
```

默认 `TestScionRealMemberRead` 明确跳过；真实启用结果见下一节。另定向复跑现有工具的显式绑定、无凭据帮助、双向标准流、取消、连接轮换、删除和服务退出回归。

| 验证 | 实际结果 |
| --- | --- |
| 未验证、未绑定与不同 membership | 未验证不能绑定；未绑定成员不能运行或读取帮助；工具仅出现在已选成员列表 |
| 账号变化、未登录与帮助 | alice→bob 阻止业务，删除凭据阻止业务；两种情况下帮助仍可用；失效 Catalog 路径不影响显式选择的封装路径 |
| 私有凭据及实例边界 | 只复制当前 origin；外部替换源文件不改变已核验副本；符号链接、错误权限、路径穿越、anonymous、错误版本引用、外部目的地址均拒绝；公开列表无凭据路径或秘密 |
| 业务标准流与退出码 | 原生 stdout 和退出 23 保留；准备失败返回 5 的安全分类，无业务执行；结束后临时凭据已删除 |
| 在身份核验及业务阶段的生命周期 | 两阶段分别验证请求取消、成员停用、profile 替换、删房、服务退出，共 10 个场景；调用退出 130、追踪集合归零、临时凭据删除 |
| 页面自动化 | 169 项通过，包括新增 Scion 保存/自动验证/dirty 拦截，以及成员显式选择、过滤未验证连接与不附加内部 action grants；既有 GitLab/Lark 页面回归通过 |
| 模块依赖 | 独立 tools/scion 不依赖 TeamX 内部包；通用连接/工具/通知不反向依赖 Scion provider；边界检查通过 |

扩大执行 `go test -race ./internal/platform/... ./tools/scion ./internal/room` 时，platform 各包及 tools/scion 均返回成功。Room 运行约 6 分钟仍未完成，随后主动以 SIGQUIT 取栈并中止；当时执行到 `TestReferenceInternalRevocationSettlesHTTPAndRejectsLateResults`，栈中还可见 Inbox 保存与 fsync。该既有测试随后单独 race 复跑通过（11.9 秒），先前查看的 `TestLarkNotificationsRestartSwitchAndDeliveryCapacity` 也单独通过（5.1 秒）。这些证据不足以判定死锁或归因于本次改动；**完整 Room suite 没有完成，不能记为全量通过。**

最初误用 `node --test internal/room/web*.mjs`，将需要专用服务地址的 HTTP 驱动脚本也作为独立测试启动，5 个脚本因未提供环境失败。随后按实际测试文件 `web*_test.mjs` 执行，169 项全部通过；没有把驱动脚本误执行记作产品验收。

## 真实只读与重开验证

复用 S0 已登录的原生 PAT profile，未重新读取 `.env`。构建本轮 `teamx-scion` 并显式使用 S0 官方 `scn 0.1.13`；后者 SHA-256 为 `1d5c4c2b09aeed7245fae2a0bd0a5a13d3f927f2406d211b7d78f14953f4ff3f`，与 lock 一致。

[真实测试](../internal/room/platform_scion_real_test.go)将 profile 放进新隔离服务根，经过实际连接保存/验证与成员绑定，然后通过 `runToolsWithClient → /v1/tools/run → 进程准备 → teamx-scion → scn` 读取。正常关闭管理器和 Room，再从同一根目录重开，保留连接与绑定并再次读取：

| 调用 | 首次及重开后结果 |
| --- | --- |
| `--json whoami https://scion.opencsg.com` | 退出 0，`schema=scion.cli.whoami/v1`，subject `csgship`，匹配已绑定账号 |
| `--json repo info OpenCSG/teamx-demo` | 退出 0，`schema=scion.cli.repo/v1`，canonical name `OpenCSG/teamx-demo` |
| `--json --repo OpenCSG/teamx-demo review ls --state all --limit 100` | 退出 0，`schema=scion.cli.reviews/v1`，repo 匹配，`truncated=false`；显式 Hub 链路成功 |

启用方式：

```sh
TEAMX_SCION_REAL=1 \
TEAMX_SCION_TEST_CREDENTIALS=/absolute/private/logged-in/credentials.json \
TEAMX_SCION_BIN=/absolute/path/teamx-scion \
TEAMX_SCN_BIN=/absolute/path/scn \
  go test -race -v ./internal/room -run '^TestScionRealMemberRead$' -count=1
```

本次测试通过（最终复核 4.76 秒，含首次与重开后的 6 个只读业务命令）。没有 clone，不依赖本地 `.scion` 或 Git 仓库。服务根与运行时凭据由测试临时目录管理并清理；仅保留无秘密的验证摘要。实际成员读取与前端日志位于 Git ignore 的 `.local/scion-s2-20261107-1/evidence/`（目录 0700，日志 0600）。

## 未验项与范围

- 浏览器控制接口在 `cua.getState()` 超时并重置，未取得页面可用状态。因此实际浏览器新增连接、成员选择及错误展示未验；DOM 事件自动化不冒充浏览器验收。
- 没有真实 DSH 模型驱动；S4 负责角色与模型协作闭环。
- Workspace 类型/clone/多成员独立副本与操作恢复待 S3；Review 创建、评审评论、更新与复审待 S4；合入/关闭待单独授权及 S5。
- 身份漂移与登出使用受控 profile 组件场景验证，未在真实账号上执行登出、换号或 token 吊销；没有测试失效真实 PAT 或服务端写权限。
- 当前一种 macOS arm64 发布及 PAT 路径；没有新增自动登录、PAT 存储入口、分离 Hub、通知、Inbox、followup 或自动远端写入恢复。

本轮未操作正在使用的 Room、成员绑定或平台连接，未执行 commit/push。工作区存在并行提交 `520279e`，已包含本轮部分 Scion 页面改动；该提交由并行工作产生，保留其结果，未改写历史或索引。现有其他改动均保留。

S2 新增及修改内容的凭据值扫描、换行和改动行尾空格检查通过。
