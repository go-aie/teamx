# Agent 工作目录实施记录

对应方案：[Agent 指定工作目录与服务端目录选择方案](../AGENT-WORKSPACE-DIRECTORY-PLAN.zh.md)。阶段一交付管理 API 与完整后端切换流程，阶段二交付目录浏览与成员界面；阶段三已补充本机交付版 DSH／真实 Chromium、容器挂载检查和使用文档。真实手机／另一设备与容器内真实 DSH 仍保留为独立待验项。

## 已实现行为

- `workspace.member_directories` 以当前 Room 的 membership ID 保存服务端绝对目录。删除键恢复默认；成员原有 `workspace` 默认相对路径保持原义。模板不接受成员目录覆盖。
- 统一解析实际 cwd，供运行时、当前模型配置、模型迁移和普通平台工具使用。历史模型 Session 快照保持不可变；原 Backend 恢复指纹检查继续生效。
- `[server].workspace_roots` 缺省使用服务账号 home，显式 `[]` 关闭外部绑定。最多 16 个绝对根目录，启动加载、重启生效；不可用根不会阻止服务启动或扩大允许范围。
- 外部目录必须已经存在，允许中文、空格与允许范围内的符号链接；受理时固定真实路径，提交、打开 Session 和执行前重新检查。拒绝文件、控制字符、越界链接、权限不足和管理数据目录。
- 目录共用仅允许同 Room 串行成员使用完全相同的外部路径；拒绝父子重叠、并行共用及跨 Room 重叠。目录引用直接从配置和未结清操作重建；未完成操作同时保留原／目标引用，结清后释放旧引用。
- 纯目录操作在平台准备入口前分流，不 clone、Configure、查询平台或刷新凭据，也不改成员保存的 Instructions／ToolEnv。已有仓库初始化仍只准备默认受管目录并保留覆盖值。
- 复用 Workspace 操作 ID、版本、结清、提交与重试流程。目录修改只轮换执行配置发生变化且已有 Session 引用的成员；其余成员冻结并复核原引用，不制造空 Session。公共历史、预算和暂停／继续规则沿用现有合同。
- 同一规范目标返回 `unchanged=true`，不新增操作、增长版本或轮换 Session。同 ID 重发已受理请求返回原收据，符号链接改指向不会重定向旧目标。
- 外部目录失效时保留配置，状态查询返回 `directory_error`；即使成员服务无法打开，也可使用目录级 human 身份修复，但必须从原有持久状态证明旧执行已结清。
- 外部目录的成员不注入受管仓库运行说明与 askpass 环境；已显式启用的平台工具保持其授权并使用实际 cwd。`workspace-git` 返回 `managed_repository_required`。
- 恢复默认和删除 Room 不遍历或删除外部文件；备份不沿 JSON 中的覆盖路径收集内容。现有受管 Room 保持固定成员名单，仍拒绝 `/v1/member/remove`，没有新增成员移除入口。

阶段一仅补齐既有 Workspace 网页的协议兼容：保存仓库设置时保留覆盖 map，正确处理无变化响应；阶段二再交付目录选择按钮，见下文。

## API 入口

继续使用 `GET /v1/workspace`、`POST /v1/workspace/apply`、`POST /v1/workspace/retry`。

应用请求保持完整 human／Room 身份、目录管理器 `service_epoch`、稳定 `operation_id` 与 `expected_version`。先读取当前 Workspace，复制完整目标，只修改需要更改的 `member_directories` 项；同一次请求不混合目录覆盖变更和仓库／绑定变更。

状态成员项新增 `default_directory`、`effective_directory`、`directory_source`。原成员项和操作记录的 `workspace` 仍为默认相对路径。内部保留的 Session 引用和配置恢复材料不会通过操作响应导出。

## 验证与边界

新增组件／模拟集成测试覆盖路径策略、权限、配置 map 所有权、A → B → 默认、串行共用、其他成员 Session 保留、正常重启、不可用目录修复、版本／身份拒绝、未结清占用、符号链接固定目标、配置及成员进度提交中断重试、模型变更、工具 cwd、受管 Git 拒绝与删除／备份边界。

已验证的独立入口：

```sh
go test -timeout=3m ./internal/agent/dsh ./internal/web
go test -tags=integration -timeout=8m ./internal/room -run 'Workspace|Directory|Tool|Model'
go test -race -tags=integration -timeout=8m ./internal/room -run 'Workspace|Directory'
go test -tags=integration -timeout=90s ./internal/room -run 'TestWorkspaceDirectory|TestWorkspaceMemberDirectory|TestWorkspaceRootsConfiguration|TestExternalWorkspace' -count=1
node --test internal/web/tests/web_app_test.mjs
```

网页组件回归通过 192 项；HTTP + DOM substitute 的 Workspace 应用与管理流程也通过。它们不是真实浏览器证据。

Workspace／Directory／Tool／Model 集成通过（291.434 秒）；Workspace／Directory 竞态检查通过（145.686 秒）。全量 `internal/room` 测试曾分别超过 3 分钟和 10 分钟时限，超时时仍在执行其他测试，不能记为全量通过。

已有回归测试同步适配：同目标应用返回无变化，因此版本历史测试改用真实后续目录变更；HTTP 界面测试替换团队 fixture 后显式发布目录快照，避免页面读取旧团队列表。

以上为阶段一验证边界；阶段二的真实浏览器结果见下节。实际交付 DSH、真实手机与 Docker 挂载验收，以及既有远端 GitLab／Scion 待验项仍未完成。

## 阶段二：服务端目录选择与成员界面

新增 human 专用 `GET /v1/workspace/directories`。请求使用目录管理器 `service_epoch`、`caller_id`、`room_id/room_instance` 和准确的 `agent_id/membership_id`；成员执行凭据无权调用。接口沿用访问认证与来源检查，响应禁止缓存，Room 未打开或 Workspace 操作进行中也可浏览。

浏览复用阶段一的路径策略，返回真实目录、可用／不可用根目录、受限的上级路径及一层子目录，不返回文件名或正文。默认从成员实际目录开始，不可浏览时回到允许根；保留已保存配置。扫描上限 5000 项、展示上限 1000 个目录、响应上限 256 KiB，达到上限提示列表不完整，仍可输入绝对路径定位。目录枚举不持有管理器全局锁，返回前复核身份与 epoch。

成员设置增加目录来源、准确路径、按钮选择、恢复默认及应用；房间总览显示默认／实际路径，可同时修复多个覆盖值或全部恢复默认。仓库设置明确只配置默认受管目录，与成员目录分开应用。共用目录提示说明共享文件、分支及 Git 暂存区的影响。

网页选择器独立模块提供根目录切换、一层浏览、返回上级、路径定位、使用当前目录与取消。目录名按文本渲染，保持绿色控件、窄屏换行、原生 dialog 焦点约束与 Escape 返回。选择和恢复默认只更新草稿；应用前提示受影响成员及私有上下文重置，应用沿用既有稳定操作 ID、状态查询和显式重试。

每个成员与房间批量编辑器的草稿分别基于观察版本复制完整 Workspace。轮询不会覆盖未提交草稿；版本变化后保留草稿并禁止应用，必须显式刷新再选择。不自动合并其他标签页设置；成员保存只修改对应覆盖项并保留其他成员及仓库字段。目录浏览按 Room 实例、管理器 epoch、成员和请求序号丢弃迟到结果，关闭重开弹窗也隔离旧关闭事件。

阶段二的浏览器测试使用真实 Chromium、生产 HTTP／Store 和隔离目录，模型组件为受控替身。阶段三仍需实际交付 DSH 的 cwd、共享未提交文件、Session 切换／重启恢复，以及真实设备与 Docker 挂载验收。

阶段二验证入口与结果：

```sh
node --test internal/web/tests/*_test.mjs
go test -timeout=3m ./internal/web ./internal/agent/dsh
go test -tags=integration -timeout=8m ./internal/room -run 'Workspace|Directory|Tool|Model'
go test -race -tags=integration -timeout=8m ./internal/room -run 'Workspace|Directory'
NODE_PATH="$PWD/packaging/verifier/node_modules" TEAMX_WORKSPACE_BROWSER=1 \
  go test -tags=e2e -timeout=3m ./internal/room -run '^TestWorkspaceDirectoriesBrowser$' -count=1 -v
```

- 网页组件回归通过 745 项，新增覆盖成员应用／恢复默认、保留其他覆盖与仓库字段、版本冲突、批量修复、目录错误、连续定位、切成员／切房／关闭重开迟到响应，以及另一成员未提交草稿的保留。
- Workspace／Directory／Tool／Model 集成通过（289.528 秒）；Workspace／Directory 竞态检查通过（164.741 秒）。新增目录 HTTP 测试同时验证 human 与 Agent 凭据隔离、来源检查、空目录与错误区分、链接与保护策略，以及数量／序列化响应上限。
- 真实 Chromium 通过按钮进入／返回上级、绝对路径 Enter 定位、Escape 取消与焦点返回、键盘选择、正式成员目录应用和房间级恢复默认。1440×1000、390×844、320×568、844×390 的选择弹窗无横向溢出，确认／取消按钮可见，浏览器无页面异常。窄屏为 Chromium 窗口尺寸验证，不代表真实手机设备或远端部署验收。

`*_http.mjs` 等文件是由 Go 测试传入隔离服务地址的驱动脚本，不是可直接通配执行的 Node 单元测试。上述浏览器入口可使用 `TEAMX_WORKSPACE_SCREENSHOTS=/existing/directory` 输出截图；它不调用真实模型、不进行远端平台写入。

## 阶段三：真实运行与使用文档

2026-11-18 使用独立私有 home、串行双成员 Room 和专用已有测试目录完成本机验收。实际启动器来自已安装 standard 包，DSH CLI 为 `0.2.2-rc.1`，模型为 `deepseek-v4.1-flash-1017`；隔离部署先通过本次启动器的真实文本／工具往返与清理验证，未复用开发源码启动器的验证身份。

入口为 `TestWorkspaceDirectoriesRealDSH`，复现命令见 [Workspace 技术参考](../reference/workspace.zh.md#成员指定目录)。它构建真实 TeamX 服务进程，以 Chromium 页面选择目录／应用／恢复默认，实际 DSH 执行 Python 写入 cwd 证据，测试端独立核对 Backend、SessionID 和文件内容。原配置与平台连接不修改，不做远端平台写入。

| 场景 | 实际结果 |
| --- | --- |
| 双成员共用目录 | Developer 创建未提交文件，Reviewer 从相同目录读到精确内容；各自私有 SessionID 独立 |
| 只切换 Developer | 新 cwd 与新 SessionID 生效，Reviewer 原 SessionID／cwd 保留 |
| 正常停服重启 | 复用监听地址，两个原 SessionID／cwd 均恢复，并完成下一真实回合 |
| Developer 恢复默认 | 回到原受管目录，Reviewer 保留原 Session；两个外部目录的旧文件保留 |
| Reviewer 路径失效 | 重启后相关 Room 显示错误且保留路径；网页可查看原历史、禁止发送、应用恢复默认，然后显式执行成功 |
| 外部 Git 现场 | 本地仓库 HEAD、tracked dirty 内容、本地配置和模型创建的未提交文件均保持 |

验收发现并修复两处完整链路问题：

- Workspace 轮换先在既有轮换记录中保存新 cwd 对应的 `ModelTarget`，再由原轮换流程保存新代际模型快照并发布 pending 引用。修复托管模型切目录后下一回合找不到 `.model.json` 的问题；旧快照、Backend 与指纹检查保持有效。新增回归覆盖切到外部目录、尚未执行就恢复默认、完成收据重放及旧模型证据保留。
- 目录失效导致成员服务无法打开时，点击该 Room 仍选择其管理视图；首次打开才发现目录错误也使用同一流程，且仍校验选择版本。Workspace 总览和历史读取使用目录级身份，发送保持禁用。修复原先留在另一房间、无法操作目标目录的问题，并验证不会误改另一房间或发送模型任务。

正常重启验收保留服务地址。MCP endpoint 属于原 Session 指纹，改用随机新端口会触发现有安全轮换，不能把这种环境变更误判为目录恢复失败，也不能忽略指纹检查来获得原 SessionID。

脱敏结果见 [运行证据](agent-workspace-directories-runtime-evidence.json)。完整私有报告与截图保存在 `.local/workspace-directories-real-20261118-15/`，其中含私有凭据与 Session 数据，不提交公开；证据 JSON 保存其摘要和归一化目录关系。最后一轮实际 DSH／Chromium 测试通过，耗时 109.33 秒，含故障 Room 历史可见与发送禁用的独立页面断言。

容器门禁 `TestWorkspaceDirectoryMountedPath` 在 Docker 的 Linux 测试进程中，通过真实 bind mount 读取宿主标记；生产目录浏览／应用接口接受容器内 `/workspace/project`，实际子进程的 `pwd -P` 写回宿主后也为该路径。恢复默认后宿主文件保留。使用 `teamx-p2-ubuntu:arm64`、普通 UID、禁用网络，结果位于 `.local/workspace-directories-mount-20261118/report.json`。此项使用受控模型，不代表容器内实际 DSH 或沙箱兼容性通过。

本轮回归结果：

- `node --test internal/web/tests/*_test.mjs`：748 项通过。
- `go test -timeout=3m ./internal/agent/dsh ./internal/web`：通过。
- `go test -json -timeout=20m ./internal/room`：通过，447.472 秒；1721 个测试项通过、5 个可选项跳过（含子测试）。先前 3 分钟／8 分钟运行达到累计时限，未计为通过。
- Workspace／Directory／Tool／Model 集成通过，394.130 秒；快照修复后的 Workspace／Directory／Model 针对性回归通过，91.196 秒；Workspace／Directory／Apply 竞态检查通过，103.576 秒。

已更新 Workspace 使用指南、技术参考、安装配置／容器挂载说明及服务／开发说明。真实手机或另一设备访问、容器内真实 DSH 仍待验；既有 GitLab clone/fetch/push/askpass 与 Scion 平台待验项保持原边界。当前证据支持本机运行与容器路径映射，尚不据此宣称所有部署与设备整体交付完成。
