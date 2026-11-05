# 独立 GitLab 工具

独立 Go 程序，原生命令交给固定版本 **glab v1.127.0**，缺口由本目录内的 Go 扩展处理。只依赖 Go 标准库，不需要启动 TeamX，不读取 Room、通知、旧授权或操作回执。已接入 `teamx tools run gitlab`。由 TeamX 启动时，成员显式选择连接，凭据仅注入工具进程；也可独立使用标准环境运行。角色复杂流程通过 `teamx tools skill gitlab` 按需读取，见[平台架构](../../docs/platform-architecture.zh.md)。

```sh
go build -o bin/teamx-gitlab ./tools/gitlab
bin/teamx-gitlab --help
bin/teamx-gitlab work-items --help
bin/teamx-gitlab issue discussions --help
```

从[固定版本发布页](https://gitlab.com/gitlab-org/cli/-/releases/v1.127.0)下载对应系统的 glab，并按发布校验和核对。依赖记录在 [glab.lock.json](glab.lock.json)。程序解析顺序为：显式 `TEAMX_GLAB_BIN` → `teamx-gitlab` 可执行文件同目录的 `glab` → PATH。显式路径失效不会回退到其他版本；执行原生命令前检查精确版本，不匹配或缺失返回 127。Go 扩展及其帮助无需 glab；缺少 glab 时父级帮助仍显示已知扩展，并返回 127，表示原生帮助不完整。

TeamX 服务按相同顺序解析工具：显式 `TEAMX_GITLAB_BIN` → TeamX 可执行文件同目录的 `teamx-gitlab` → 服务 PATH。推荐将 `teamx`、`teamx-gitlab`、`glab` 放在同一个安装目录。`tools run` 在服务端启动进程，Agent shell 的 PATH 或环境变量不会转发给服务。路径变量可写入 `config.toml` 的 `[env]`，或在启动 TeamX 服务前导出，已有服务环境优先；工具启用只选择账号，不安装程序。遇到 127 应报告安装错误，修正服务环境后重启，不让 Agent 搜索二进制或绕过入口读取凭据。

工具使用标准环境变量：

- `GITLAB_HOST`：明确的实例，例如 `git-devops.opencsg.com`；Go 扩展也接受 HTTPS origin。
- `GITLAB_TOKEN`：本次账号的 Token。工具不自动加载 `.env`；本仓库的 `GITLAB_PAT` 需要调用方映射到此变量。
- `TEAMX_GLAB_BIN`：可选的 glab 可执行文件路径。

凭据不进入 argv；原生子进程使用临时私有 glab 配置，不读取操作者的全局 glab 登录信息，过滤其他 GLAB/GITLAB 环境及替代 Token 来源。帮助不解析实例/凭据、不调用业务 API。此配置隔离不构成安全沙箱；账号的实际权限决定可用操作。Go 扩展只接受 HTTPS origin、不跟随重定向；不默认选择 gitlab.com。

开发合同：TeamX 自身的来源身份解析、目标核验和两类 MR 动作已迁入内置 `internal/platform/gitlab`，通过受控 GET 和受限单次 POST 执行；本工具已移除 `__reference-resolve`、`__reference-action`、私有协议 DTO 和旧 `notifications resolve`。Agent 主动使用的 glab 代理、Work Item 扩展和 Issue 讨论查询／回复仍为独立 CLI，不导入 TeamX 内部包，也不回调内部能力接口。内部办理需要当前成员有效的 GitLab 绑定，无须安装本工具；`tools run gitlab` 仍须安装本工具及其原生命令需要的 glab。

原生子进程按非交互方式运行：Unix 上使用独立会话，stdout/stderr 通过流式管道转发，即使从终端直接启动也不继承控制终端，不等待终端颜色探测响应。stdin 的文件与管道输入仍可使用。帮助被 Ctrl-C 中断时返回 130，只报告帮助中断，不再追加扩展列表或提示远端写入结果未确认。

原生命令的参数、stdin、stdout、stderr 和退出码保持原样。项目由任务显式提供，无需本地 Git 仓库，例如：

```sh
bin/teamx-gitlab mr view 12 --repo https://git-devops.opencsg.com/product/agentichub/teamx-demo -F json
bin/teamx-gitlab api --hostname "$GITLAB_HOST" user
bin/teamx-gitlab issue update 8 --repo product/agentichub/teamx-demo --assignee '+alice'
```

`api` 的无仓库调用应显式给出 `--hostname`，沿用 glab 原生语义。扩展通过 `--repo` 明确指定项目或组的完整路径、`--iid` 指定工作项/Issue IID；仅扩展使用这些参数约定，不改写原生参数。

| 扩展命令 | 参数及行为 |
| --- | --- |
| `work-items view` | 读取类型、正文、状态及完整指派列表，输出 JSON |
| `work-items close` | GraphQL `workItemUpdate(stateEvent: CLOSE)`，核对返回的对象与关闭状态 |
| `work-items assignees` | `--mode add/remove/replace/clear`；除 clear 外用 `--users 123,456` 指定数字用户 ID；读取完整当前集合，写入一次后核查实际集合 |
| `work-items note` | `--message TEXT` 或 `--file PATH`；`--file -` 读取 stdin；可选 `--discussion-id` 回复实际讨论 |
| `work-items discussions list` | 自动读取讨论及每个讨论内评论的全部分页，保留实际 `id` 和 `replyId` |
| `issue discussions list` | Discussions REST 全部分页 |
| `issue discussions reply` | `--discussion-id ID`，正文参数与 note 相同 |

```sh
bin/teamx-gitlab work-items view --repo product/agentichub/teamx-demo --iid 8
bin/teamx-gitlab work-items assignees --repo product/agentichub/teamx-demo --iid 8 --mode replace --users 123
bin/teamx-gitlab work-items note --repo product/agentichub/teamx-demo --iid 8 --file review.md
```

Go 扩展使用原实现中的固定 GraphQL 请求及解析方式，移除了身份快照、对象投递范围、确认令牌、查重标记和持久回执。扩展不采用“原生失败后切换 API”；路由在执行前确定。GraphQL 顶层 `errors`、mutation `errors`、空 payload 或分页元数据缺失均返回非零。写入超时/中断不自动重发，应先查询远端。Unix 上收到取消时结算 glab 进程组；原生命令的异步结果沿用 glab 输出，不能将提交成功解释为已经合并。

原生 MR 合并应明确指定 `--sha HEAD_SHA --auto-merge=false`；普通评审用带 base/head 的 `mr note create --resolvable=false`，不等同于 approve。WorkItem 删除继续原样调用原生 `work-items delete`；仅依据网页 URL 不足以判断对象 API 类型。

离线验证：

```sh
go test -race ./tools/gitlab
go vet ./tools/gitlab
TEAMX_GLAB_BIN=/absolute/path/to/glab go test -count=1 -run '^TestPinnedGlabOfflineHelp$' ./tools/gitlab
```

## 真实验收

真实实例读探测为 opt-in 的 e2e 测试。先导出 `GITLAB_HOST` / `GITLAB_TOKEN`，使用尚不存在的私有绝对输出路径；它只读取项目，账号身份需独立核对，不代表功能验收：

```sh
TEAMX_GITLAB_REAL=1 \
TEAMX_GITLAB_TEST_REPO=product/agentichub/teamx-demo \
TEAMX_GITLAB_PROBE_EVIDENCE=/absolute/private/new-probe.json \
go test -tags=e2e -run '^TestRealReadProbe$' -count=1 -v ./tools/gitlab
```

当前目标实例的完整能力矩阵仍待真实验收：

| 范围 | 必须核对的结果 |
| --- | --- |
| 读取 | 项目、MR、Issue、实际通用 WorkItem 的类型、身份和正文；MR diff |
| 讨论 | MR/Issue/WorkItem 完整分页、WorkItem 嵌套评论分页；创建评论及按真实讨论 ID 回复 |
| 工作项 | Issue 与 WorkItem 分别关闭/删除；指派 add/remove/replace/clear 后核对有效账号集合 |
| 待办 | pending/done/all；只完成专用验收对象的确切个人待办 |
| MR 写入 | 创建；带 base/head 的普通评审；固定 head SHA、关闭自动合并后的实际 merged 状态；另一个 MR 的关闭 |
| 失败边界 | 权限不足、GraphQL 顶层及 mutation 错误、分页、中断、异步结果及不自动重发 |

工具集成还需补验真实 DSH 首次调用、已有成员显式接入、通知驱动的个人事务与开发写回，以及浏览器中的成员工具设置。

先确认账号和获准项目，再使用单独授权的临时对象；记录类型、项目、IID/ID、SHA、退出码和独立远端查询。Issue 不能替代通用 WorkItem，帮助输出、离线测试和旧工具的通过记录不证明当前实例效果。输出与失败处理要求、其他待验项见[验证说明](../../internal/room/README.zh.md#验证入口)。

历史 R4 的服务专用动作协议已由[内部能力方案 B](../../docs/PLATFORM-INTERNAL-CAPABILITIES-PLAN.zh.md#b-实施记录2026-11-05)移除。当前内部动作及回执语义见[平台架构](../../docs/platform-architecture.zh.md)；旧工具协议测试和真实验收仅说明当时版本，不能证明当前内部路径的真实实例效果。

当前内部路径的真实读取和恢复证据见[内部能力 C 验收](../../docs/reference-followups-acceptance.zh.md#内部能力-c真实来源读取与恢复)：已用当前成员绑定核实来源，并在缺少本工具的隔离副本完成显式恢复。该结果不代表 Agent CLI 可免安装，也不代表执行了真实 GitLab 写入或恢复了日常失败 Session。独立工具仍单独构建和回归，不承担内部身份解析协议。
