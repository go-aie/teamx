# 受管 Computer 与 AIO Screen Runtime

原始日志、截图和 JSON 回执仅在本地保存；本文保留验收结论、复跑入口及已知限制。

日期：2026-11-23。状态：C1a 已实施并通过。范围：共享 Computer 方案 C1a。该阶段只提供受控服务入口；普通 Agent、成员设置、TeamX `/mcp` 工具发布和全局设置尚未接通。

## 实现边界

- `internal/computer` 定义 Provider／Screen、完整所有者与成员绑定、执行作用域、原生工具合同和错误。`Service` 在发出变更前持久保存最小未结清关联；重复未结清调用只查原 ID。该层不导入 AIO、Room、Actor 或 DSH。
- `internal/computer/aio` 是不可变连接的认证管理客户端，只允许明确的 HTTP(S) origin；非 loopback 连接要求 HTTPS，不跟随重定向。原生 MCP 地址、端口和客户端不返回给调用者。
- `internal/computer/aio/runtime` 拥有 screen、服务租约和远端执行事实。Linux 进程实现按专属 UID 管理 Xvnc、XFCE、D-Bus、Chromium、browser MCP、aiod 与 worker。
- `cmd/teamx-computer` 是可选容器辅助程序。正常 TeamX 的构建、启动和现有 MCP 路径没有导入或构造该服务；未新增成员调度或配置字段。
- `packaging/computer/aio/Dockerfile.runtime` 使用 C0 相同的固定基础镜像与依赖锁。原 C0 Dockerfile、原型及证据继续保留，可以独立复跑。

Screen 使用随机 ID，完整 `installation + owner + computer + RoomID + RoomInstance + AgentID + MembershipID` 决定归属。并发 Ensure 共享同一个创建结果；容量先预留再启动，部分失败清理整个 UID 范围。成员替换必须先停止旧 screen；Room 重建退役旧 Room 实例的所有成员，新成员不继承 profile。旧 Room 不能通过换 Agent 名称重新获得访问。

服务绑定使用显式 CAS Claim。新服务代次撤销旧服务租约，被撤销的代次不能重新 Claim；同一 Claim 丢失响应可以取得原结果。存在在途或未结清执行时拒绝交接。Runtime 重启先撤销旧租约，再对账进程和操作；恢复失败只阻断对应 screen。原生动作不会作为恢复步骤重发。

## 唯一执行路径与原生合同

受控调用路径为 `computer.Service → Screen.Call → Runtime 准入／接受记录 → 绑定 screen 的 aiod /mcp`。Runtime 管理入口只有认证后的 Inspect、Claim、Ensure、Tools、Call、Operation、Drain、Stop；不提供裸 MCP 代理、任意目标地址或进程命令接口。未来 TeamX facade 在 C2 组合该路径。

工具合同保留 C0 锁定的 21 项完整原生定义，包括扩展字段、annotations、输入／输出 Schema 和名称映射。摘要还覆盖被接受的工具集合、独立审核的只读策略及影响执行的版本。发布文档原结构不因摘要规范化而改变。每次 screen 启动都与实际 `tools/list` 比对，缺失目录只在启动预算内等待，已出现但定义不同立即拒绝。

启动同时检查 Chromium CDP 与 worker；不能仅凭 aiod 初始化或工具清单就宣布 screen 可用。实际 `tools/call` 不做网络、HTTP 或业务错误重试。原生文本、图片、结构化输出、`isError` 与未知字段按完整 JSON 传回，不翻译成自定义成功回执。GUI 复合工具保留原 Schema，由独立准入策略拒绝锁定白名单之外的动作。

## 执行事实与恢复

每 screen 同时只准入一个原生调用，冲突立即返回 busy，没有无界队列；不同 screen 独立执行。Drain 持久关闭指定 Session／turn 的新准入，再等待已经接受的调用，包括只读调用。客户端断开只停止等待，不把远端执行标成结束。

变更执行先保存 ID、作用域、工具、参数摘要及 screen epoch，随后才调用上游。同 ID 同摘要返回原状态／尚可用的原生结果，不重复执行；同 ID 不同摘要拒绝。Runtime 接受后崩溃、远端响应无法核验，或执行后的终态提交失败，都进入 unknown。停止时先通过私有 CDP 管理通道发送固定 `Browser.close`，使刚写入的 Cookie 刷新；这不是 Agent 工具或任意 CDP 代理。随后 Stop 根据专属 UID 和全部保留端口核验停止，才将未知执行标成已结清；网站业务结果仍为 unknown。

Runtime 的持久快照不含原始参数、密码、截图或完整结果。响应只在内存中保留最多一分钟，合计最多 16 MiB／64 项；重启或缓存过期后只返回操作状态与结清事实，并明确 `result_available=false`。只读工具没有持久操作／结果账本，但参与同一在途门。

状态目录使用独占文件锁和共享原子快照 writer；提交结果不确定时关闭写准入，必须重新打开对账。宿主关联和 Runtime 终态分别持有各自的职责，没有两份完整结果账本。硬上限为 256 个持久 screen、每 screen 1024 条变更操作／已关闭作用域、1024 个旧服务代次；达到上限明确拒绝，不清除未解决记录。请求上限 128 KiB，原生响应上限 8 MiB，超限不截断 JSON 或伪造结果。

## 运行与凭据

正式运行模板见[打包说明](../../../packaging/computer/aio/README.zh.md)。必须使用容器 init／subreaper，孤儿进程才能被回收并纳入停止证明；辅助程序拒绝直接作为无 init 的 PID 1 运行。无需 privileged、Docker socket 或宿主 Workspace 挂载。

持久卷目录：

```text
/var/lib/teamx-computer/runtime/          # root 0700，身份、租约、操作事实
/var/lib/teamx-computer/profiles/<id>/    # 专属 UID 0700，独立 profile
/workspace/agents/<id>/downloads/        # 对应 screen 的下载目录
/workspace/shared/                      # screens 组可协作的显式共享目录
/run/teamx-computer/<id>/                # 独立 Xauthority、XDG、临时日志
```

管理密钥由 root 私有文件读取，不通过参数或环境变量值传入，不下发给 screen 进程。screen 用户没有 sudo 或额外附属组；不可读取 Runtime 元数据、管理密钥或其他 screen 的私有 profile。AIO 的 `AIO_RUST_LOG_LEVEL=error` 禁用默认请求正文日志；Runtime 自身也不记录参数和结果。

Chromium 沿用 C0 的 `--no-sandbox` 与容器共同信任范围；专属 UID 是工作状态与凭据边界，不承诺同容器中的恶意代码隔离。真实验收另外探测固定镜像在默认 seccomp／no-new-privileges 下的 Chromium sandbox 可用性，并记录实际诊断；不把缺少浏览器 sandbox 描述为已完成安全隔离。

## 验证与证据

默认回归不探测 Docker，也不需要辅助程序。真实运行显式执行：

```sh
python3 scripts/computer_runtime_acceptance.py --run \
  --output .local/computer-runtime-acceptance-fresh
```

脚本静态交叉编译辅助程序，从固定 digest 构建随机名称镜像，创建专用数据卷和仅绑定宿主 loopback 的随机管理端口，使用虚构本地站点；不接触日常 TeamX、现有测试 Agent 或试验容器。结束时停止并核验本次容器，删除本次容器与卷。测试过程只将临时密钥文件复制到容器私有位置，密钥不进入证据。

替身合同测试覆盖两种原生工具目录、并发创建唯一性、成员／Room 替换、容量、部分启动清理、服务 CAS、代次 fencing、只读 Drain、取消与结清分离、单屏失败隔离及原生扩展透传。受控 HTTP 测试在远端变更完成后丢弃响应，宿主重开再核验原 ID，实际调用计数保持一次。精确的“动作完成、终态提交前崩溃”使用快照故障注入，验证重开后 unknown 而不重放。

真实 AIO 验收通过 Go Provider 的认证 HTTP 路径执行双屏 GUI／DOM、图片、虚构登录、相同 ID 点击去重、单屏停止及 profile 恢复。另在容器保持运行、UID 子进程仍存活时 SIGKILL Runtime，并人为保留一个脱离原进程组的 screen UID 后台进程；新 Runtime 必须先停止并核验这些范围，保留旧 pending 的 unknown 事实，再接受新租约。该证据不同于停止整个容器后的进程自然消失。

最终实测结果、资源限制、构建 ID 与清理证明见验收报告，执行阶段、重启对账和回归命令分别保留证据。替身测试、真实供应方、真实 DSH／模型和浏览器接管分别属于不同证据；本阶段不声称已完成后两者。C1b 负责全局在线连接设置；C2 负责正式成员与 Agent 闭环；观看接管和文件交付分别留给 C3a／C3b。

本次真实验收结果：

| 项目 | 结果 |
| --- | --- |
| 平台与配额 | ARM 宿主模拟运行固定 linux/amd64 镜像；4 CPU、6 GiB 内存、1 GiB shm，默认 seccomp、no-new-privileges、init，无 privileged／Docker socket |
| 构建 | `sha256:fed4ecc7e0038520bd8b352e2140c7f0793aa9142ba330cb1f71f2ebfba10923`；工具合同包含原生完整 Schema、只读策略、GUI 动作准入摘要与固定依赖版本 |
| 实际路径 | Go AIO Provider → 认证 HTTP → Runtime → 绑定 screen 原生 MCP；并发 Ensure 唯一、容量明确拒绝、GUI／DOM 并行、各自截图、虚构登录、同 ID 点击仅一次 |
| 停止／恢复 | 单屏停止后另一屏继续；同 profile 新 epoch 恢复登录；杀死 Runtime 后独立后台进程仍存活，重开完成 UID／端口停止核验，旧 pending 为 unknown + settled，旧租约失效 |
| 凭据 | screen UID 实际读取管理密钥和 Runtime 元数据均被拒绝；原始参数和完整结果没有进入持久快照 |
| 浏览器 sandbox | 固定镜像在默认 seccomp／no-new-privileges 下失败，实际诊断为 namespace Operation not permitted；继续采用已声明的容器共同信任范围与 `--no-sandbox` |
| 结束清理 | 最终 screen UID 进程数量为零；本次容器已停止且 PID 为零，本次容器和两个数据卷均已删除；构建缓存保留 |

实际截图 A和实际截图 B显示不同背景、页面和各自标记，已逐张查看。真实供应方的执行／恢复两段耗时分别为 31.62 秒／20.43 秒，不含镜像构建；此数据来自模拟运行，不推导原生 amd64 的容量或性能。完整 Go 回归、真实 DSH／模型、TeamX 侧观看接管及文件交付均没有被本阶段的定向验证替代。
