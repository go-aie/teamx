# Workspace 技术参考

本文维护配置快照、准备协议、管理接口及真实验证入口。页面操作见 [Workspace 使用指南](../workspace.zh.md)。

## 配置与准备

生效配置保存在 `room-config.json`，带 `workspace_version`。默认根目录为房间内 `workspaces`，每个成员的默认路径为 `<root>/<membership_id>/`；默认仓库按成员独立 clone。

`workspace.member_directories` 以当前 Room 的 `membership_id` 为键、服务端规范绝对目录为值，覆盖成员实际 cwd。缺省使用默认路径，删除该键恢复默认。成员记录的 `workspace` 始终保存默认相对路径；状态同时返回 Room 冻结的 `scheduling`（`serial`／`parallel`），供网页限制共用目录选择；成员状态另给出 `default_directory`、`effective_directory`、`directory_source`（`managed`／`external`）。模板禁止携带非空目录覆盖，避免传播机器路径。旧快照缺少字段时行为不变。

当前执行组装、模型配置、普通平台工具 cwd 和状态展示均通过统一解析器得到有效目录。历史 Backend 使用冻结 cwd 与指纹恢复，不能把新覆盖值写入旧 Session。源码见 [workspace_directories.go](../../internal/room/workspace_directories.go)、[workspace_config.go](../../internal/room/workspace_config.go)。

根路径只接受房间内相对目录，拒绝越界、符号链接及与管理数据重叠。团队模板的仓库和平台字段只提供预填建议，不在创建时自动 clone 或绑定。具体模型与校验见 [workspace_config.go](../../internal/room/workspace_config.go)。

外部目录受部署级 `server.workspace_roots` 限制。缺省使用服务账号 home，显式 `[]` 关闭选择；绝对根最多 16 个，启动时加载。路径最多 4096 字节，不展开 shell 表达式；符号链接固定为允许根内的真实路径，提交／打开时复核。根不可用不会扩大范围，已保存路径失效不妨碍读取快照与修复。受保护的 TeamX 管理目录不可被指定。选择祖先项目目录不提供后代文件隔离，实际权限仍由服务账号及沙箱约束。

串行 Room 允许成员共用完全相同的外部目录；并行 Room 拒绝重叠，同 Room 父子目录及跨 Room 相同／重叠绑定均拒绝。管理器在锁下核对配置与未结清操作的原／目标预留，重启从持久证据重建；旧执行结清后才释放引用。它不提供跨进程文件锁，也不隔离共用分支和暂存区。删除、备份和恢复默认不沿外部覆盖路径遍历文件。

| 配置 | 准备内容 |
| --- | --- |
| 普通目录 | 创建或核对各成员目录 |
| 仅 Git | 使用 HTTPS 仓库 URL，首次 clone 并按需 checkout |
| GitLab 绑定 | 从仓库 URL 派生实例与项目，核验目标与账号，准备 Git 作者和限定仓库路径的认证 |

GitLab 绑定只引用全局 OAuth 连接。URL 不接受内嵌账号、密码、查询参数或 fragment。短期令牌不进入 remote URL、命令参数、成员环境或响应；网络 Git 使用 `teamx workspace-git fetch/push/ls-remote`，由服务取得有界令牌许可并等待进程结束。Git 读写需要 `write_repository`，本地 diff／commit 仍直接使用 Git。

已有仓库、目录和项目只读，不能通过设置换仓库；同仓库可补 GitLab 或替换认证。普通根路径调整仅适用于未关联仓库的房间。首次绑定使用新的受管目录，保留旧普通目录及产物。

回访已有绑定只做本地目录、origin 和配置核对，不自动 fetch、checkout、reset、clean 或查询远端。clone 失败保留 `.clone-pending` 等现场，重试不得覆盖半成品或脏文件。实现见 [workspace.go](../../internal/room/workspace.go)。

仓库准备已通过 [Repository 接口与注册表](../../internal/workspace/repository.go)解耦。[共用流程](../../internal/room/workspace.go)负责所有成员的目录检查、失败标记和准备调度；[Git 实现](../../internal/workspace/git/adapter.go)负责 `.git`、origin、clone 和本地配置。[装配与平台访问桥接](../../internal/room/workspace_adapters.go)提供 GitLab 仓库核验、认证资源引用及按需作者查询，实际进程仍沿用服务的取消、凭据许可和结清机制。Git URL/ref 校验由后端提供，GitLab ProjectID 校验与目标填充位于平台桥接边界。

当前注册 Git 与 Scion 两种后端，`repository.kind` 选择工作副本类型，`binding.provider` 选择托管平台。缺省 kind 仍按 Git 解释，读取时不重写旧快照；显式 `git` 也可使用。Git 保持原有认证方式，Scion 必须选择已验证的 Scion CLI profile 连接，项目身份使用 canonical name，`project_id` 为空。

Scion 页面按类型派生 origin 与仓库名，不追加 `.git`；分支留空时核实并冻结默认分支。应用前读取仓库与实际分支列表，拒绝不存在或没有提交的分支。每名成员独立执行原生 clone，并核对 `.scion/state.json` 的 format、endpoint、canonical repo、完整 changeset ID 及 dirstate。回访只检查本地状态，保留任务分支、dirty 文件和 detached 状态；不执行 clone/sync/switch。clone 使用 Workspace 连接，日常命令使用成员单独启用的工具连接，两者互不授予权限。

Scion 准备使用固定封装版本、所选账号的私有凭据副本和隔离环境，沿用进程取消与持久 PID 结清。作者通过每次 commit 的 `--author 'Name <email>'` 明确提供，不能把平台 subject 当作提交作者。S3b 实际结果与未验项见 [Scion 验证与待验项](../scion.zh.md#验证与待验项)。

## 应用与恢复

```mermaid
flowchart LR
  A[校验并记录操作] --> B[停止调度并结清旧执行]
  B --> C[准备并复核目标]
  C --> D[提交配置]
  D --> E[更新已有 Session 代际]
  E --> F[保存完成状态]
```

操作由服务持有，使用稳定操作 ID、原配置版本、冻结目标及提交进度。纯目录修改只验证本地目标，不做仓库准备、平台查询或凭据刷新；覆盖与仓库设置由页面分开提交。只轮换有效 cwd 改变的已有 Session；未受影响 Session 保留，从未启动的成员不做空轮换。同目标不产生轮换。新 Session 按需创建，成功不重发草稿，也不自动重放失败、中断或排队旧工作。

成员服务因路径失效无法打开时，目录级 human 身份仍可读取状态并应用纯目录修复。必须有完整快照、无冲突操作以及已结清的持久归属证据，不能用内存中缺少 service 代替结清。操作沿用提交／Session 进度恢复，重试不重复轮换已完成成员。

clone 失败会保留目标旁的 `.clone-pending` 标记，即使原生 CLI 已移除失败目录也不自动重放。操作者须先检查现场；只有核实目标未创建、可安全重试时才移除对应标记，再显式重试原操作。非空、混合或身份不符的目标须保留检查，不自动清空。已完成的成员副本复用原目录。

配置、操作与 Session 引用跨文件无原子事务。失败或中断保留证据并阻止执行；显式重试核对当前配置是否为原状态或完整目标，再按原目标恢复。已准备阶段只做本地复核，不重新 clone 或查询平台。替换结果不确定时冻结存储，关闭并重新打开后核查。

操作期间公共历史与状态可观察，成员执行及冲突管理操作被阻断。完成后成员服务 epoch 变化；客户端刷新身份，不能用旧身份继续写入。计时记录中的 `sessions` 是引用更新与组装耗时，不等于真实模型或 ACP 新建耗时。

## 管理接口

| 接口 | 身份与行为 |
| --- | --- |
| `GET /v1/workspace` | 目录 `service_epoch`、`caller_id`、RoomID/RoomInstance，可带 `operation_id`；返回配置、成员路径、操作状态及建议值 |
| `GET /v1/workspace/directories` | 目录身份加 `agent_id`、`membership_id`，可带绝对 `path`；只向 human 返回允许根与一层可浏览子目录 |
| `POST /v1/workspace/apply` | human caller、目录/房间身份、32 位小写十六进制 `operation_id`、`expected_version`、`target`；连接引用另带 `connection_version` |
| `POST /v1/workspace/retry` | 同样的管理身份及原操作 ID；拒绝新目标或凭据字段 |

同 ID 同参数返回已有操作，不重新执行；参数不同则冲突。操作只保存平台连接引用；`credential_status=configured` 表示当前授权证据可解析，不证明远端权限仍有效。请求结构与实际阶段见 [workspace_management.go](../../internal/room/workspace_management.go)、[workspace_records.go](../../internal/room/workspace_records.go)。

目录浏览沿用 human 认证与来源检查，不接受 Agent 执行凭据；响应 `Cache-Control: no-store`。最多扫描 5000 项、返回 1000 个目录，序列化最多 256 KiB，达到上限返回 `truncated`；只枚举一层且不读取文件正文。目录 IO 不持有管理器全局锁，返回前复核身份与 Workspace 版本。外部路径定位和应用使用同一策略，手填不能绕过范围。

省略 `path` 时，优先打开成员当前生效目录，包括默认目录。当前目录不可用时，返回可用根目录和 `current_directory_error`，保留已保存配置。显式传入的无效路径直接报错，不自动定位到其他目录。页面顶部路径栏始终可编辑，另提供返回上级与回到允许根目录的快捷操作，缺省根为服务账号 home。

受管目录浏览仅对当前成员自己的默认目录开放，即使关闭外部目录选择仍可浏览；其他成员目录、Session 和凭据目录继续受保护。响应以 `directory_source=managed` 标识默认目录范围，其根路径 `can_select=true`，子目录 `can_select=false`。选择默认根路径在前端表示删除该成员覆盖值，不能把受管路径提交为外部目录；外部目录绑定策略保持不变。受管默认路径仍拒绝符号链接替换，子目录链接按真实路径重新检查。

外部目录作为本地 cwd 使用，不继承默认仓库凭据；`teamx workspace-git` 返回 `managed_repository_required`。持久 Instructions／ToolEnv 保留，运行时按目录来源过滤受管仓库能力及说明，恢复默认后可重新使用。普通平台工具使用实际 cwd 与成员明确启用的连接。

## 文件只读接口

文件浏览使用管理器的 `service_epoch`，不依赖成员运行时启动。所有请求沿用登录／同源校验，要求 `caller_id` 等于当前 human operator，以及准确的 `room_id + room_instance`；执行凭据不能访问。当前实现支持 macOS 和 Linux。

| GET 接口 | 增量参数与响应 |
| --- | --- |
| `/v1/workspace/files/sources` | 返回成员目录来源、可用性、`root_id`、共用目录展示键和能力上限 |
| `/v1/workspace/files` | `membership_id + root_id + path`，可带 `show_hidden=true`；返回相对路径、目录优先的条目与 `truncated` |
| `/v1/workspace/files/preview` | `membership_id + root_id + path`，或互斥的 `message_seq + path`；返回来源、能力上限、当前元数据、`preview_kind` 与有界文本或受控图片 `content_url` |
| `/v1/workspace/files/content` | 同一互斥来源选择器与 `path`；默认仅交付验证后的 PNG／JPEG／WebP，`download=true` 流式下载普通文件 |

`path` 必填，根目录用空字符串表示，不接受绝对路径、越界路径、控制字符或反斜杠。标准查询解码只执行一次。目录最多扫描 5,000 项、返回 1,000 项，JSON 最多约 256 KiB；文本最多 1 MiB，全局最多 4 个并发文件读取，等待可取消，请求预算 10 秒。能力由 `sources`／`preview` 的 `limits` 返回；图片、文档相对资源和下载沿用同一来源校验。下载的总预算为 30 秒。

`root_id` 是 `wr1_` 加 SHA-256：确定性输入为版本、RoomID、RoomInstance、membership 和规范绝对目录，不包含整体配置版本。它只用于识别来源变化，不授予权限。服务重启和同目录内容变化不改变指纹；当前成员退出、目录变化或 Room 不可用后拒绝旧来源，不回退到其他成员或服务 cwd。

消息预览通过当前可见的原生 Agent 消息取得作者 membership 及 `workspace_root_id`，再核对其当前目录；不能另传 membership／root 覆盖原作者。服务端不解析 Markdown，也不限制为正文列出的路径。每次请求及交付前都复核消息可见性与来源，清空消息后新请求返回 `reference_unavailable`。从预览进入目录列表时，显式改用当前工作区选择器。

`workspace_root_id` 是消息的可选内部执行事实：Session 冻结 cwd → 现有 executionBinding → 已认证 HTTP 上下文 → send → 原子消息提交。同一指纹随已有私有 Session 配置快照冻结，避免恢复时路径别名换目标；新代际重新冻结自己的来源。外部 `/v1/send` JSON 和 CLI 没有可写入口。字段参与容量和分段日志不可变比较，幂等重试沿用首条消息的值；human 读取可见，Agent 业务 DTO 不增加此字段。恢复会话只使用原冻结配置；缺少可信快照或规范目录证据时不附加来源，不阻断文字发送，也不补写旧消息。

读取以根目录句柄逐级约束；根内符号链接重新从根解析并校验保护路径，文件非阻塞打开后复核类型与对象身份。拒绝 FIFO、设备、socket、根外目标、TeamX 管理／凭据路径及 `.git`；受管目录仅豁免其管理祖先，不开放同级管理数据。正文读取后、交付前再次核对来源及文件变化；不持有管理器锁执行文件读取。

响应带 `X-Content-Type-Options: nosniff` 和 `Cache-Control: private, no-store`。常见错误为 `workspace_source_unavailable`、`workspace_source_changed`、`reference_unavailable`、`file_not_found`、`file_not_allowed`、`file_changed`。超限或不支持类型仅返回元数据及原因，不发送截断正文。文本 1 MiB；图片 16 MiB／2,500 万像素；下载 64 MiB；全局读取与下载队列 4 个、共用附件解码队列 2 个。上限随 `sources`／`preview` 的 `limits` 返回，单篇文档最多自动加载 20 张图片。

下载使用固定打开句柄、`application/octet-stream` 和标准库编码的 `Content-Disposition: attachment`；图片使用验证后的 MIME 和 `inline`。下载总时限 30 秒，通过 `ResponseController` 设置响应写截止时间，覆盖服务默认 15 秒写超时；客户端取消或服务关闭可中止传输。交付前重新核对文件与来源／消息，已准入传输不逐块重新解析路径，最多交付初始大小；之后来源失效的新请求拒绝。源码见 [内容与传输](../../internal/room/workspace_file_content.go)、[文件读取](../../internal/room/workspace_files.go)和 [HTTP 适配](../../internal/room/workspace_files_http.go)。

## 页面与凭据

页面展示当前目录、各成员默认／实际路径、配置版本和初始化进度。选择／恢复默认只修改草稿，应用才提交完整 Workspace；单成员表单保留其他成员覆盖与仓库字段。轮询不覆盖未提交草稿，版本变化后须显式刷新，拒绝自动合并。目录响应按 Room 实例、管理器 epoch、成员和请求序号隔离，关闭重开也丢弃迟到响应。页面不接收凭据，选择已有 GitLab OAuth 或 Scion CLI profile 连接。浏览器仅保存按房间和操作者隔离的原操作 ID。

响应超时先查原操作，不自动重发。失败时只能显式重试原目标；查不到收据不证明原操作从未提交，需核查后主动放弃未确认状态、重新填写并使用新 ID。连接轮换、引用保护与工具进程结清见[平台架构](../platform-architecture.zh.md)。

当前受管房间要求完整 runtime 与 Workspace 快照，不再提供旧运行时或 Workspace 迁移命令。使用方法见[根 README](../../README.md)。

## 真实验收

### 文件生成与预览

[TestWorkspaceFilesRealDSH](../../internal/room/workspace_files_real_test.go) 从指定配置复用 DSH 启动器及默认模型，只将选中模型和凭据复制到全新的私有 home。测试构建并启动独立 TeamX 服务，由真实模型生成 Markdown、相对文档、PNG 和下载文件，通过真实 CLI 发送文件链接，再用 Chromium 完成打开、修改刷新、正常重启、目录变化与消息清空验收。

```sh
NODE_PATH="$PWD/packaging/verifier/node_modules" \
TEAMX_WORKSPACE_FILES_REAL=1 \
TEAMX_WORKSPACE_FILES_CONFIG=/absolute/private-home/config.toml \
TEAMX_DSH_OUTPUT=/absolute/new-files-evidence \
go test -tags=e2e -timeout=18m ./internal/room \
  -run '^TestWorkspaceFilesRealDSH$' -count=1 -v
```

需已安装 Playwright 与 Chromium，证据目录必须全新。测试只在隔离 Room 与工作目录中写入；没有复制平台连接，也不更改日常部署。`report.json` 记录实际模型／启动器、后端二进制摘要、真实 SessionID、生成文件摘要和浏览器步骤，截图覆盖桌面、窄桌面和手机尺寸；私有目录还包含凭据与 Session 数据，不应提交或公开。可分享结果及未验项见[文件真实闭环记录](../implementation/workspace-files-real-acceptance.zh.md)。

### 成员指定目录

[TestWorkspaceDirectoriesRealDSH](../../internal/room/workspace_directories_real_test.go) 使用新建私有 home、Room 和已有测试目录，构建并启动真实 TeamX 进程，通过 Chromium 页面应用目录，再由实际 DSH／模型创建 cwd 与共享文件证据。配置必须有可用的模型目录和对应私有凭据；只复制选中模型，使用本次启动器重新执行真实模型验证，不修改原部署。

```sh
NODE_PATH="$PWD/packaging/verifier/node_modules" \
TEAMX_WORKSPACE_DIRECTORIES_REAL=1 \
TEAMX_WORKSPACE_DIRECTORIES_CONFIG=/absolute/private-home/config.toml \
TEAMX_DSH_BINARY_PATH=/absolute/installed-bundle/bin/dsh \
TEAMX_DSH_BINARY_ARGS='[]' \
TEAMX_DSH_OUTPUT=/absolute/new-directory-evidence \
go test -tags=e2e -timeout=20m ./internal/room \
  -run '^TestWorkspaceDirectoriesRealDSH$' -count=1 -v
```

需已安装 Playwright 与 Chromium；`NODE_PATH` 指向其实际依赖目录。未显式指定 DSH 时使用配置启动器；发布验收应明确选择交付包入口，避免误用开发源码版本。证据目录必须全新；其中包含私有模型凭据和 Session 数据，不应提交或公开分享。

场景覆盖串行共用未提交文件、独立 SessionID、通过管理 API 单成员换目录及恢复默认、正常停服重启恢复，以及路径缺失后从聊天框目录入口统一恢复默认并显式执行。逐成员覆盖保留为底层能力，网页仅提供各自默认或全员共用。`report.json` 和截图记录模型、启动器、路径、各阶段 SessionID 及页面操作；外部仓库的 HEAD、dirty 内容和本地配置也会独立核对。

目录变化的托管模型轮换通过既有 `rotation.json` 的 `model_target` 冻结新 cwd 与模型执行事实，在发布 pending Session 引用前保存新代际模型快照。重试复用原轮换目标，旧 Backend 与模型快照保持原样。

[TestWorkspaceDirectoryMountedPath](../../internal/room/workspace_directory_mount_test.go) 是独立的容器挂载门禁：传入专用已有 `TEAMX_WORKSPACE_MOUNT_PATH`，目录内准备内容为 `HOST_MOUNT_INPUT` 的 `mounted-input.txt`，在容器运行 Linux 测试二进制。它验证生产目录浏览／应用接口、真实子进程 cwd 和宿主文件可见性；`TEAMX_WORKSPACE_MOUNT_REPORT` 可指定 JSON 证据路径。该测试的模型为受控替身，不代表容器内真实 DSH／沙箱验收。

桌面 Chromium 与窄屏窗口不替代真实手机或另一设备访问；设备验收、容器真实 DSH 和 Git／Scion 平台验收分别记录。当前结果及未验项见[实施记录](../implementation/agent-workspace-directories.zh.md)。

### 默认 GitLab 仓库

[TestRealWorkspaceGitLabDSH](../../internal/room/workspace_real_e2e_test.go) 使用[已安装的 DSH 与模型环境](../agent-runtime.zh.md#真实验收)，通过 `TEAMX_REAL_OAUTH_SNAPSHOT` 指定已登录的私有 OAuth 连接快照（`write_repository` 已授权）：

```sh
TEAMX_REAL_OAUTH_SNAPSHOT=/absolute/platform-connections.json \
 go test -tags=e2e ./internal/room -run '^TestRealWorkspaceGitLabDSH$' -count=1 -v -timeout 20m -args \
  -teamx-workspace-real-output /absolute/new-workspace-evidence \
  -teamx-workspace-repository https://gitlab.example/group/project.git \
  -teamx-management-env-file /absolute/credentials.env \
  -teamx-management-base-url https://model.example/v1
```

输出与失败处理要求见[验证说明](../../internal/room/README.zh.md#验证入口)。默认 DOM 驱动连接生产 HTTP，不算实际浏览器；追加 `-teamx-workspace-browser` 才等待人工页面创建、讨论和绑定，再核对后端结果。该场景不写远端，只读与 clone 不证明 push、MR 或评论通过。

当前仍需补验 Go Workspace / Git 请求链的真实 HTTPS clone/fetch/push/askpass、准确 SHA checkout，以及 Workspace 的实际浏览器闭环。离线 bare Git 测试不能替代目标实例验证。
